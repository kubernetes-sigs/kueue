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

package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"sort"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apiruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	"k8s.io/apimachinery/pkg/util/validation/field"
	corev1ac "k8s.io/client-go/applyconfigurations/core/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
	jobsetv1alpha2 "sigs.k8s.io/jobset/api/jobset/v1alpha2"
	jobsetv1alpha2ac "sigs.k8s.io/jobset/client-go/applyconfiguration/jobset/v1alpha2"

	configapi "github.com/kubeflow/trainer/v2/pkg/apis/config/v1alpha1"
	trainer "github.com/kubeflow/trainer/v2/pkg/apis/trainer/v1alpha1"
	"github.com/kubeflow/trainer/v2/pkg/apply"
	"github.com/kubeflow/trainer/v2/pkg/constants"
	"github.com/kubeflow/trainer/v2/pkg/runtime"
	fwkcore "github.com/kubeflow/trainer/v2/pkg/runtime/framework/core"
	fwkplugins "github.com/kubeflow/trainer/v2/pkg/runtime/framework/plugins"
	idxer "github.com/kubeflow/trainer/v2/pkg/runtime/indexer"
	trainingruntimeutil "github.com/kubeflow/trainer/v2/pkg/util/trainingruntime"
)

var (
	errorNotFoundSpecifiedTrainingRuntime = errors.New("TrainingRuntime specified in TrainJob is not found")
)

type TrainingRuntime struct {
	framework *fwkcore.Framework
	client    client.Client
}

var TrainingRuntimeGroupKind = schema.GroupKind{
	Group: trainer.GroupVersion.Group,
	Kind:  trainer.TrainingRuntimeKind,
}.String()

var _ runtime.Runtime = (*TrainingRuntime)(nil)

var trainingRuntimeFactory *TrainingRuntime

func NewTrainingRuntime(ctx context.Context, c client.Client, indexer client.FieldIndexer, cfg *configapi.Configuration) (runtime.Runtime, error) {
	if err := indexer.IndexField(ctx, &trainer.TrainJob{}, idxer.TrainJobRuntimeRefKey, idxer.IndexTrainJobTrainingRuntime); err != nil {
		return nil, fmt.Errorf("setting index on TrainingRuntime for TrainJob: %w", err)
	}
	if err := indexer.IndexField(ctx, &trainer.TrainJob{}, idxer.TrainJobClusterRuntimeRefKey, idxer.IndexTrainJobClusterTrainingRuntime); err != nil {
		return nil, fmt.Errorf("setting index on ClusterTrainingRuntime for TrainJob: %w", err)
	}
	fwk, err := fwkcore.New(ctx, c, fwkplugins.NewRegistry(), indexer, cfg)
	if err != nil {
		return nil, err
	}
	trainingRuntimeFactory = &TrainingRuntime{
		framework: fwk,
		client:    c,
	}
	return trainingRuntimeFactory, nil
}

func (r *TrainingRuntime) NewObjects(ctx context.Context, trainJob *trainer.TrainJob) ([]apiruntime.ApplyConfiguration, error) {
	var trainingRuntime trainer.TrainingRuntime
	// Try to get runtime from snapshot first
	if err := getRuntimeSnapshot(ctx, r.client, trainJob, &trainingRuntime); err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("unable to get runtime snapshot: %w", err)
		}

		// Snapshot doesn't exist, load runtime from API server
		err := r.client.Get(ctx, client.ObjectKey{Namespace: trainJob.Namespace, Name: trainJob.Spec.RuntimeRef.Name}, &trainingRuntime)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", errorNotFoundSpecifiedTrainingRuntime, err)
		}

		// Create snapshot for future reconciliations
		if err := createRuntimeSnapshot(ctx, r.client, trainJob, &trainingRuntime); err != nil {
			return nil, fmt.Errorf("creating runtime snapshot: %w", err)
		}
	}

	info, err := r.RuntimeInfo(trainJob, trainingRuntime.Spec.Template, trainingRuntime.Spec.MLPolicy, trainingRuntime.Spec.PodGroupPolicy)
	if err != nil {
		return nil, err
	}
	return r.framework.RunComponentBuilderPlugins(ctx, info, trainJob)
}

func (r *TrainingRuntime) RuntimeInfo(
	trainJob *trainer.TrainJob, runtimeTemplateSpec any, mlPolicy *trainer.MLPolicy, podGroupPolicy *trainer.PodGroupPolicy,
) (*runtime.Info, error) {

	jobSetTemplateSpec, ok := runtimeTemplateSpec.(trainer.JobSetTemplateSpec)
	if !ok {
		return nil, fmt.Errorf("unsupported runtimeTemplateSpec")
	}
	info, err := r.newRuntimeInfo(trainJob, jobSetTemplateSpec, mlPolicy, podGroupPolicy)
	if err != nil {
		return nil, err
	}
	if err = r.framework.RunEnforceMLPolicyPlugins(info, trainJob); err != nil {
		return nil, err
	}
	if err = r.framework.RunEnforcePodGroupPolicyPlugins(info, trainJob); err != nil {
		return nil, err
	}

	if err = r.framework.RunPodNetworkPlugins(info, trainJob); err != nil {
		return nil, err
	}

	return info, nil
}

func (r *TrainingRuntime) newRuntimeInfo(
	trainJob *trainer.TrainJob, jobSetTemplateSpec trainer.JobSetTemplateSpec, mlPolicy *trainer.MLPolicy, podGroupPolicy *trainer.PodGroupPolicy,
) (*runtime.Info, error) {
	propagationLabels := maps.Clone(jobSetTemplateSpec.Labels)
	propagationAnnotations := maps.Clone(jobSetTemplateSpec.Annotations)
	for _, patch := range trainJob.Spec.RuntimePatches {
		if patch.TrainingRuntimeSpec != nil && patch.TrainingRuntimeSpec.Template != nil && patch.TrainingRuntimeSpec.Template.Metadata != nil {
			if propagationLabels == nil && len(patch.TrainingRuntimeSpec.Template.Metadata.Labels) > 0 {
				propagationLabels = make(map[string]string)
			}
			for k, v := range patch.TrainingRuntimeSpec.Template.Metadata.Labels {
				propagationLabels[k] = v
			}
			if propagationAnnotations == nil && len(patch.TrainingRuntimeSpec.Template.Metadata.Annotations) > 0 {
				propagationAnnotations = make(map[string]string)
			}
			for k, v := range patch.TrainingRuntimeSpec.Template.Metadata.Annotations {
				propagationAnnotations[k] = v
			}
		}
	}
	err := r.mergeRuntimePatches(trainJob, &jobSetTemplateSpec)
	if err != nil {
		return nil, err
	}

	jobSetSpecApply, err := apply.FromTypedObjWithFields[jobsetv1alpha2ac.JobSetSpecApplyConfiguration](&jobsetv1alpha2.JobSet{
		TypeMeta: metav1.TypeMeta{
			APIVersion: jobsetv1alpha2.GroupVersion.String(),
			Kind:       "JobSet",
		},
		Spec: jobSetTemplateSpec.Spec,
	}, "spec")
	if err != nil {
		return nil, err
	}

	opts := []runtime.InfoOption{
		runtime.WithLabels(propagationLabels),
		runtime.WithAnnotations(propagationAnnotations),
		runtime.WithMLPolicySource(mlPolicy),
		runtime.WithPodGroupPolicy(podGroupPolicy),
		runtime.WithTemplateSpecObjApply(jobSetSpecApply),
	}

	for i, rJob := range jobSetSpecApply.ReplicatedJobs {
		// TODO: Support multiple replicas ('.template.spec.replicatedJobs[*].replicas') for replicated Jobs.
		// REF: https://github.com/kubeflow/trainer/issues/2318
		count := ptr.Deref(rJob.Template.Spec.Parallelism, 1)
		var ancestor *string
		if metadata := rJob.Template.ObjectMetaApplyConfiguration; metadata != nil && metadata.Labels != nil {
			if labelAncestor, ok := metadata.Labels[constants.LabelTrainJobAncestor]; ok {
				if labelAncestor == constants.AncestorTrainer && mlPolicy != nil {
					count = ptr.Deref(mlPolicy.NumNodes, 1)
				}
				ancestor = &labelAncestor
			}
		}
		if trainJob.Spec.Trainer != nil && trainJob.Spec.Trainer.ResourcesPerNode != nil {
			isTrainerAncestor := ancestor != nil && *ancestor == constants.AncestorTrainer && mlPolicy != nil
			isMPILauncherAsNode := mlPolicy != nil && mlPolicy.MPI != nil &&
				ptr.Deref(mlPolicy.MPI.RunLauncherAsNode, false) && *rJob.Name == constants.Node
			if isTrainerAncestor || isMPILauncherAsNode {
				if applyPodSpec := jobSetSpecApply.ReplicatedJobs[i].Template.Spec.Template.Spec; applyPodSpec != nil {
					for k := range applyPodSpec.Containers {
						if ptr.Deref(applyPodSpec.Containers[k].Name, "") != constants.Node {
							continue
						}
						var baseRes corev1.ResourceRequirements
						if r := applyPodSpec.Containers[k].Resources; r != nil {
							if r.Limits != nil {
								baseRes.Limits = *r.Limits
							}
							if r.Requests != nil {
								baseRes.Requests = *r.Requests
							}
						}
						mergedRes, mergeErr := trainingruntimeutil.MergeResourceRequirements(
							baseRes, *trainJob.Spec.Trainer.ResourcesPerNode)
						if mergeErr != nil {
							return nil, mergeErr
						}
						applyRes := &corev1ac.ResourceRequirementsApplyConfiguration{}
						if mergedRes.Limits != nil {
							limits := maps.Clone(mergedRes.Limits)
							applyRes.Limits = &limits
						}
						if mergedRes.Requests != nil {
							requests := maps.Clone(mergedRes.Requests)
							applyRes.Requests = &requests
						}
						applyPodSpec.Containers[k].Resources = applyRes
						jobSetTemplateSpec.Spec.ReplicatedJobs[i].Template.Spec.Template.Spec.Containers[k].Resources = mergedRes
						break
					}
				}
			}
		}
		opts = append(opts, runtime.WithPodSet(
			*rJob.Name,
			ancestor,
			count,
			*jobSetTemplateSpec.Spec.ReplicatedJobs[i].Template.Spec.Template.Spec.DeepCopy(),
			rJob.Template.Spec.Template.Spec),
		)
	}

	return runtime.NewInfo(opts...), nil
}

func (r *TrainingRuntime) mergeRuntimePatches(trainJob *trainer.TrainJob, jobSetTemplateSpec *trainer.JobSetTemplateSpec) error {
	// Capture the original ReplicatedJobs ordering since SMP may reorder the list.
	order := make(map[string]int, len(jobSetTemplateSpec.Spec.ReplicatedJobs))
	for i, rJob := range jobSetTemplateSpec.Spec.ReplicatedJobs {
		order[rJob.Name] = i
	}

	for _, runtimePatch := range trainJob.Spec.RuntimePatches {
		if runtimePatch.TrainingRuntimeSpec == nil ||
			runtimePatch.TrainingRuntimeSpec.Template == nil ||
			runtimePatch.TrainingRuntimeSpec.Template.Spec == nil {
			continue
		}
		source, err := json.Marshal(jobSetTemplateSpec.Spec)
		if err != nil {
			return err
		}
		patch, err := json.Marshal(runtimePatch.TrainingRuntimeSpec.Template.Spec)
		if err != nil {
			return err
		}
		merged, err := strategicpatch.StrategicMergePatch(source, patch, jobsetv1alpha2.JobSetSpec{})
		if err != nil {
			return err
		}
		mergedSpec := jobsetv1alpha2.JobSetSpec{}
		if err := json.Unmarshal(merged, &mergedSpec); err != nil {
			return err
		}
		jobSetTemplateSpec.Spec = mergedSpec
	}

	// Restore the ReplicatedJobs order defined in the runtime template.
	sort.SliceStable(jobSetTemplateSpec.Spec.ReplicatedJobs, func(i, j int) bool {
		return order[jobSetTemplateSpec.Spec.ReplicatedJobs[i].Name] <
			order[jobSetTemplateSpec.Spec.ReplicatedJobs[j].Name]
	})

	return nil
}

func (r *TrainingRuntime) TrainJobStatus(ctx context.Context, trainJob *trainer.TrainJob) (*trainer.TrainJobStatus, error) {
	return r.framework.RunTrainJobStatusPlugin(ctx, trainJob)
}

func (r *TrainingRuntime) EventHandlerRegistrars() []runtime.ReconcilerBuilder {
	var builders []runtime.ReconcilerBuilder
	for _, ex := range r.framework.WatchExtensionPlugins() {
		builders = append(builders, ex.ReconcilerBuilders()...)
	}
	return builders
}

func (r *TrainingRuntime) ValidateObjects(ctx context.Context, old, new *trainer.TrainJob) (admission.Warnings, field.ErrorList) {
	trainingRuntime := &trainer.TrainingRuntime{}
	if err := r.client.Get(ctx, client.ObjectKey{
		Namespace: new.Namespace,
		Name:      new.Spec.RuntimeRef.Name,
	}, trainingRuntime); err != nil {
		return nil, field.ErrorList{
			field.Invalid(field.NewPath("spec", "runtimeRef"), new.Spec.RuntimeRef,
				fmt.Sprintf("%v: specified trainingRuntime must be created before the TrainJob is created", err)),
		}
	}
	info, _ := r.newRuntimeInfo(new, trainingRuntime.Spec.Template, trainingRuntime.Spec.MLPolicy, trainingRuntime.Spec.PodGroupPolicy) // ignoring the error here as the runtime configured should be valid
	return r.framework.RunCustomValidationPlugins(ctx, info, old, new)
}
