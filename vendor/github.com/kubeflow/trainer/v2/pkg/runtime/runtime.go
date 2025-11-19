/*
Copyright 2024 The Kubeflow Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package runtime

import (
	"iter"
	"maps"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	corev1ac "k8s.io/client-go/applyconfigurations/core/v1"
	resourcehelpers "k8s.io/component-helpers/resource"
	"k8s.io/utils/ptr"
	jobsetv1alpha2ac "sigs.k8s.io/jobset/client-go/applyconfiguration/jobset/v1alpha2"

	trainer "github.com/kubeflow/trainer/v2/pkg/apis/trainer/v1alpha1"
	"github.com/kubeflow/trainer/v2/pkg/constants"
)

type Info struct {
	// Labels and Annotations to add to the RuntimeJobTemplate.
	Labels      map[string]string
	Annotations map[string]string
	// Original policy values from the runtime.
	RuntimePolicy RuntimePolicy
	// Scheduler parameters to add to the RuntimeJobTemplate.
	Scheduler *Scheduler
	// TemplateSpec is TrainingRuntime Template object.
	// ObjApply podSpecs and this PodSets should be kept in sync by info.SyncPodSetsToTemplateSpec().
	TemplateSpec TemplateSpec
}

type RuntimePolicy struct {
	MLPolicySource *trainer.MLPolicySource
	PodGroupPolicy *trainer.PodGroupPolicy
}

type TemplateSpec struct {
	// ObjApply is ApplyConfiguration for the TrainingRuntimes Template field.
	ObjApply any
	// PodSets is a set of Pod extracted from ObjApply.
	// This is abstract concept to represent multiple PodSpec as a unit.
	PodSets []PodSet
}

type PodSet struct {
	// PodSet name is the name to identify PodSpec.
	// This typically has the name stored in each PodSpec.
	Name string
	// Ancestor is built by `trainer.kubeflow.org/trainjob-ancestor-step` label value
	// in Runtime CRDs.
	Ancestor       *string
	Count          *int32
	InitContainers []Container
	Containers     []Container
	Volumes        []corev1ac.VolumeApplyConfiguration
	Endpoints      iter.Seq[string]
	// The total PodSet requests can be calculated with
	// SinglePodRequests x Count.
	SinglePodRequests corev1.ResourceList
}

type Container struct {
	Name         string
	Env          []corev1ac.EnvVarApplyConfiguration
	Ports        []corev1ac.ContainerPortApplyConfiguration
	VolumeMounts []corev1ac.VolumeMountApplyConfiguration
}

// TODO (andreyvelich): Potentially, we can add ScheduleTimeoutSeconds to the Scheduler for consistency.
type Scheduler struct {
	PodLabels      map[string]string
	PodAnnotations map[string]string
}

type InfoOptions struct {
	labels        map[string]string
	annotations   map[string]string
	runtimePolicy RuntimePolicy
	templateSpec  TemplateSpec
}

type InfoOption func(options *InfoOptions)

var defaultOptions = InfoOptions{}

func WithLabels(labels map[string]string) InfoOption {
	return func(o *InfoOptions) {
		o.labels = maps.Clone(labels)
	}
}

func WithAnnotations(annotations map[string]string) InfoOption {
	return func(o *InfoOptions) {
		o.annotations = maps.Clone(annotations)
	}
}

func WithMLPolicySource(mlPolicy *trainer.MLPolicy) InfoOption {
	return func(o *InfoOptions) {
		if mlPolicy != nil {
			o.runtimePolicy.MLPolicySource = &mlPolicy.MLPolicySource
		}
	}
}

func WithPodGroupPolicy(pgPolicy *trainer.PodGroupPolicy) InfoOption {
	return func(o *InfoOptions) {
		o.runtimePolicy.PodGroupPolicy = pgPolicy
	}
}

func WithTemplateSpecObjApply(objApply any) InfoOption {
	return func(o *InfoOptions) {
		o.templateSpec.ObjApply = objApply
	}
}

// WithPodSet construct Info.TemplateSpec.PodSet from PodSpec.
// The forth argument, 'typedPodSpec' is used only to calculate requested resources.
func WithPodSet(
	psName string, ancestor *string, count int32, typedPodSpec corev1.PodSpec, podSpecApply *corev1ac.PodSpecApplyConfiguration,
) InfoOption {
	return func(o *InfoOptions) {
		ps := PodSet{
			Name:              psName,
			Ancestor:          ancestor,
			Count:             ptr.To(max(count, 1)),
			Volumes:           podSpecApply.Volumes,
			SinglePodRequests: resourcehelpers.PodRequests(&corev1.Pod{Spec: typedPodSpec}, resourcehelpers.PodResourcesOptions{}),
			InitContainers:    slices.Collect(toPodSetContainer(podSpecApply.InitContainers...)),
			Containers:        slices.Collect(toPodSetContainer(podSpecApply.Containers...)),
		}
		o.templateSpec.PodSets = append(o.templateSpec.PodSets, ps)
	}
}

func toPodSetContainer(containerApply ...corev1ac.ContainerApplyConfiguration) iter.Seq[Container] {
	return func(yield func(Container) bool) {
		for _, cApply := range containerApply {
			container := Container{
				Name:         ptr.Deref(cApply.Name, ""),
				Env:          cApply.Env,
				Ports:        cApply.Ports,
				VolumeMounts: cApply.VolumeMounts,
			}
			if !yield(container) {
				return
			}
		}
	}
}

func NewInfo(opts ...InfoOption) *Info {
	options := defaultOptions
	for _, opt := range opts {
		opt(&options)
	}

	info := &Info{
		Labels:        make(map[string]string),
		Annotations:   make(map[string]string),
		RuntimePolicy: options.runtimePolicy,
		Scheduler: &Scheduler{
			PodLabels: make(map[string]string),
		},
		TemplateSpec: options.templateSpec,
	}
	if options.labels != nil {
		info.Labels = options.labels
	}
	if options.annotations != nil {
		info.Annotations = options.annotations
	}
	return info
}

func TemplateSpecApply[A any](info *Info) (*A, bool) {
	spec, ok := info.TemplateSpec.ObjApply.(*A)
	return spec, ok
}

// FindContainerByPodSetAncestorContainerName finds runtime.Container from Info.TemplateSpec.PodSet by PodSet Ancestor and Container name.
func (i *Info) FindContainerByPodSetAncestorContainerName(psAncestor, containerName string) *Container {
	ps := i.FindPodSetByAncestor(psAncestor)
	if ps == nil {
		return nil
	}
	if idx := slices.IndexFunc(ps.Containers, func(c Container) bool { return c.Name == containerName }); idx != -1 {
		return &ps.Containers[idx]
	}
	return nil
}

func (i *Info) FindPodSetByAncestor(ancestor string) *PodSet {
	if idx := slices.IndexFunc(i.TemplateSpec.PodSets, func(ps PodSet) bool { return ptr.Equal(ps.Ancestor, &ancestor) }); idx != -1 {
		return &i.TemplateSpec.PodSets[idx]
	}
	return nil
}

func (i *Info) FindPodSetByName(psName string) *PodSet {
	if idx := slices.IndexFunc(i.TemplateSpec.PodSets, func(ps PodSet) bool { return ps.Name == psName }); idx != -1 {
		return &i.TemplateSpec.PodSets[idx]
	}
	return nil
}

func RuntimeRefToRuntimeRegistryKey(runtimeRef trainer.RuntimeRef) string {
	return schema.GroupKind{
		Group: ptr.Deref(runtimeRef.APIGroup, ""),
		Kind:  ptr.Deref(runtimeRef.Kind, ""),
	}.String()
}

// ExtractResourcePerNodeFromRuntime extracts the Trainer resource per node from the Info object.
func ExtractResourcePerNodeFromRuntime(info *Info) *corev1.ResourceRequirements {
	if jobSetSpec, ok := TemplateSpecApply[jobsetv1alpha2ac.JobSetSpecApplyConfiguration](info); ok {
		for _, rJob := range jobSetSpec.ReplicatedJobs {
			if rJob.Name != nil && *rJob.Name == constants.Node || rJob.Template.Labels[constants.LabelTrainJobAncestor] == constants.AncestorTrainer {
				for _, container := range rJob.Template.Spec.Template.Spec.Containers {
					if container.Name != nil && *container.Name == constants.Node && container.Resources != nil {
						res := &corev1.ResourceRequirements{
							Limits:   corev1.ResourceList{},
							Requests: corev1.ResourceList{},
						}
						if container.Resources.Limits != nil {
							res.Limits = *container.Resources.Limits
						}
						if container.Resources.Requests != nil {
							res.Requests = *container.Resources.Requests
						}
						return res
					}
				}
			}
		}
	}
	return nil
}

// GetNumGPUPerNode returns the GPU count if found in container resources.
func GetNumGPUPerNode(res *corev1.ResourceRequirements) int {
	if res == nil {
		return 0
	}
	gpuQ := numGPU(res.Requests)
	if limitGpuQ := numGPU(res.Limits); gpuQ == 0 && limitGpuQ > 0 {
		gpuQ = limitGpuQ
	}
	return gpuQ
}

func numGPU(resourcePerNode corev1.ResourceList) int {
	for resName, resQ := range resourcePerNode {
		if strings.Contains(strings.ToLower(resName.String()), "gpu") {
			return int(resQ.Value())
		}
	}
	return 0
}
