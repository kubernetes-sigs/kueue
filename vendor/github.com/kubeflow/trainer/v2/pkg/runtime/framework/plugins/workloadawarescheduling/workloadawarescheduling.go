/*
Copyright The Kubeflow Authors.

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

package workloadawarescheduling

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	schedulingv1beta1 "k8s.io/api/scheduling/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apiruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
	corev1ac "k8s.io/client-go/applyconfigurations/core/v1"
	metav1ac "k8s.io/client-go/applyconfigurations/meta/v1"
	schedulingv1beta1ac "k8s.io/client-go/applyconfigurations/scheduling/v1beta1"
	"k8s.io/component-helpers/scheduling/schedulingv1/workloadbuilder"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	configapi "github.com/kubeflow/trainer/v2/pkg/apis/config/v1alpha1"
	trainer "github.com/kubeflow/trainer/v2/pkg/apis/trainer/v1alpha1"
	"github.com/kubeflow/trainer/v2/pkg/apply"
	"github.com/kubeflow/trainer/v2/pkg/features"
	"github.com/kubeflow/trainer/v2/pkg/runtime"
	"github.com/kubeflow/trainer/v2/pkg/runtime/framework"
	"github.com/kubeflow/trainer/v2/pkg/util/trainjob"
)

const (
	Name = "WorkloadAwareScheduling"

	// maxNameLength is the maximum length of the generated Workload, PodGroupTemplate, and PodGroup
	// names, which must be DNS labels.
	maxNameLength = validation.DNS1123LabelMaxLength
	// nameHashLength is the length of the hash in the generated names.
	nameHashLength = 10

	resourceClaimsErrorMsg = "must not be set when the runtime sets spec.scheduling.replicatedJobs[].job.resourceClaims"
)

// WorkloadAwareScheduling plugin integrates TrainJob with the Kubernetes Workload-Aware Scheduling APIs.
type WorkloadAwareScheduling struct {
	client client.Client
}

var _ framework.CustomValidationPlugin = (*WorkloadAwareScheduling)(nil)
var _ framework.EnforceSchedulingPlugin = (*WorkloadAwareScheduling)(nil)
var _ framework.ComponentBuilderPlugin = (*WorkloadAwareScheduling)(nil)
var _ framework.ComponentDeleterPlugin = (*WorkloadAwareScheduling)(nil)
var _ framework.WatchExtensionPlugin = (*WorkloadAwareScheduling)(nil)

// +kubebuilder:rbac:groups=scheduling.k8s.io,resources=workloads;podgroups,verbs=create;get;list;watch;patch;delete

// New returns the WorkloadAwareScheduling plugin. It fails when the cluster doesn't serve the Workload and PodGroup
// APIs, so the controller doesn't start with the TrainJobWorkloadAwareScheduling feature gate enabled
// on a cluster without Workload-Aware Scheduling.
func New(_ context.Context, c client.Client, _ client.FieldIndexer, _ *configapi.Configuration) (framework.Plugin, error) {
	for _, kind := range []string{"Workload", "PodGroup"} {
		gvk := schedulingv1beta1.SchemeGroupVersion.WithKind(kind)
		if _, err := c.RESTMapper().RESTMapping(gvk.GroupKind(), gvk.Version); err != nil {
			return nil, fmt.Errorf("the %s feature gate requires the cluster to serve the %s API: %w",
				features.TrainJobWorkloadAwareScheduling, gvk, err)
		}
	}
	return &WorkloadAwareScheduling{client: c}, nil
}

func (w *WorkloadAwareScheduling) Name() string {
	return Name
}

func (w *WorkloadAwareScheduling) Validate(_ context.Context, info *runtime.Info, oldObj, newObj *trainer.TrainJob) (admission.Warnings, field.ErrorList) {
	if info == nil || newObj == nil || !hasJobResourceClaims(info.RuntimePolicy.Scheduling) {
		return nil, nil
	}
	var allErrs field.ErrorList
	if oldObj == nil {
		for _, path := range trainjob.ResourceClaimsPaths(newObj) {
			allErrs = append(allErrs, field.Forbidden(path, resourceClaimsErrorMsg))
		}
		return nil, allErrs
	}
	// On update, spec.trainer and the existing Pod resourceClaims of spec.runtimePatches are
	// immutable, so only Pod resourceClaims added to a runtimePatch need validation. The patched Pod
	// specs are identified by the patch manager and ReplicatedJob name, since list indices may change.
	type patchedPodSpec struct {
		manager  string
		rJobName string
	}
	oldClaims := sets.New[patchedPodSpec]()
	trainjob.ForEachRuntimePatchPodSpec(oldObj, func(_ *field.Path, manager, rJobName string, podSpec *trainer.PodSpecPatch) {
		if len(podSpec.ResourceClaims) > 0 {
			oldClaims.Insert(patchedPodSpec{manager: manager, rJobName: rJobName})
		}
	})
	trainjob.ForEachRuntimePatchPodSpec(newObj, func(podSpecPath *field.Path, manager, rJobName string, podSpec *trainer.PodSpecPatch) {
		if len(podSpec.ResourceClaims) > 0 && !oldClaims.Has(patchedPodSpec{manager: manager, rJobName: rJobName}) {
			allErrs = append(allErrs, field.Forbidden(podSpecPath.Child("resourceClaims"), resourceClaimsErrorMsg))
		}
	})
	return nil, allErrs
}

// hasJobResourceClaims reports whether any Job-level (level 3) scheduling entry sets resourceClaims.
func hasJobResourceClaims(scheduling *trainer.TrainingRuntimeScheduling) bool {
	if scheduling == nil {
		return false
	}
	for _, entry := range scheduling.ReplicatedJobs {
		if entry.Job != nil && len(entry.Job.ResourceClaims) > 0 {
			return true
		}
	}
	return false
}

// EnforceScheduling assigns the Pods of every PodSet to the PodGroup of the scheduling group which
// its ReplicatedJob belongs to.
func (w *WorkloadAwareScheduling) EnforceScheduling(info *runtime.Info, trainJob *trainer.TrainJob) error {
	if info == nil || trainJob == nil || info.RuntimePolicy.Scheduling == nil {
		return nil
	}
	for i, ps := range info.TemplateSpec.PodSets {
		templateName, ok := podGroupTemplateName(trainJob, info.RuntimePolicy.Scheduling, ps.Name)
		if !ok {
			continue
		}
		info.TemplateSpec.PodSets[i].SchedulingGroup = corev1ac.PodSchedulingGroup().
			WithPodGroupName(podGroupName(trainJob, templateName))
	}
	return nil
}

// Build builds the Workload and the PodGroups of the TrainJob. The Workload and the PodGroups are
// never updated once created, so only the missing objects are built.
func (w *WorkloadAwareScheduling) Build(ctx context.Context, info *runtime.Info, trainJob *trainer.TrainJob) ([]apiruntime.ApplyConfiguration, error) {
	// Delete() removes the Workload and PodGroups while suspended, so do not recreate them here.
	if info == nil || trainJob == nil || info.RuntimePolicy.Scheduling == nil || ptr.Deref(trainJob.Spec.Suspend, false) {
		return nil, nil
	}
	workload, err := buildWorkload(info, trainJob)
	if err != nil || workload == nil {
		return nil, err
	}

	var objects []apiruntime.ApplyConfiguration
	// The Workload and PodGroups are applied only when they don't exist, since most of their spec
	// fields are immutable. Applying them on every reconcile might break running TrainJobs after a
	// controller upgrade: if the new version computes a slightly different spec, the apply would be rejected.
	if err := w.client.Get(ctx, client.ObjectKeyFromObject(workload), &schedulingv1beta1.Workload{}); err != nil {
		if client.IgnoreNotFound(err) != nil {
			return nil, err
		}
		spec, err := apply.FromTypedObjWithFields[schedulingv1beta1ac.WorkloadSpecApplyConfiguration](workload, "spec")
		if err != nil {
			return nil, err
		}
		objects = append(objects, schedulingv1beta1ac.Workload(workload.Name, workload.Namespace).
			WithOwnerReferences(metav1ac.OwnerReference().
				WithAPIVersion(trainer.GroupVersion.String()).
				WithKind(trainer.TrainJobKind).
				WithName(trainJob.Name).
				WithUID(trainJob.UID).
				WithController(true).
				WithBlockOwnerDeletion(true)).
			WithSpec(spec))
	}

	// Materialize the PodGroups from the PodGroupTemplates of the compiled Workload.
	podGroupBuilder := workloadbuilder.NewBuilderFromExistingWorkload(workload, buildOptions(trainJob))
	for _, template := range workload.Spec.PodGroupTemplates {
		podGroup, err := podGroupBuilder.NewPodGroup(podGroupName(trainJob, template.Name), template.Name)
		if err != nil {
			return nil, err
		}
		if err := w.client.Get(ctx, client.ObjectKeyFromObject(podGroup), &schedulingv1beta1.PodGroup{}); err != nil {
			if client.IgnoreNotFound(err) != nil {
				return nil, err
			}
			spec, err := apply.FromTypedObjWithFields[schedulingv1beta1ac.PodGroupSpecApplyConfiguration](podGroup, "spec")
			if err != nil {
				return nil, err
			}
			objects = append(objects, schedulingv1beta1ac.PodGroup(podGroup.Name, podGroup.Namespace).
				WithOwnerReferences(metav1ac.OwnerReference().
					WithAPIVersion(trainer.GroupVersion.String()).
					WithKind(trainer.TrainJobKind).
					WithName(trainJob.Name).
					WithUID(trainJob.UID).
					WithController(true).
					WithBlockOwnerDeletion(true)).
				WithSpec(spec))
		}
	}
	return objects, nil
}

// Delete returns the Workload and PodGroups of a suspended, complete, or failed TrainJob.
func (w *WorkloadAwareScheduling) Delete(ctx context.Context, trainJob *trainer.TrainJob) ([]client.Object, error) {
	if trainJob == nil || (!trainjob.IsTrainJobFinished(trainJob) && !ptr.Deref(trainJob.Spec.Suspend, false)) {
		return nil, nil
	}

	var objects []client.Object
	podGroups := &schedulingv1beta1.PodGroupList{}
	if err := w.client.List(ctx, podGroups, client.InNamespace(trainJob.Namespace)); err != nil {
		return nil, err
	}
	for i := range podGroups.Items {
		if metav1.IsControlledBy(&podGroups.Items[i], trainJob) {
			objects = append(objects, &podGroups.Items[i])
		}
	}
	workload := &schedulingv1beta1.Workload{}
	if err := w.client.Get(ctx, client.ObjectKey{Namespace: trainJob.Namespace, Name: workloadName(trainJob)}, workload); err != nil {
		if client.IgnoreNotFound(err) != nil {
			return nil, err
		}
	} else if metav1.IsControlledBy(workload, trainJob) {
		objects = append(objects, workload)
	}
	return objects, nil
}

func (w *WorkloadAwareScheduling) ReconcilerBuilders() []runtime.ReconcilerBuilder {
	return []runtime.ReconcilerBuilder{
		func(b *builder.Builder, cl client.Client, cache cache.Cache) *builder.Builder {
			return b.Watches(
				&schedulingv1beta1.Workload{},
				handler.EnqueueRequestForOwner(
					w.client.Scheme(), w.client.RESTMapper(), &trainer.TrainJob{}, handler.OnlyControllerOwner(),
				),
			)
		},
		func(b *builder.Builder, cl client.Client, cache cache.Cache) *builder.Builder {
			return b.Watches(
				&schedulingv1beta1.PodGroup{},
				handler.EnqueueRequestForOwner(
					w.client.Scheme(), w.client.RESTMapper(), &trainer.TrainJob{}, handler.OnlyControllerOwner(),
				),
			)
		},
	}
}

// buildWorkload compiles the Workload of the TrainJob, or returns nil when the runtime has no
// ReplicatedJobs to schedule. The PodGroupTemplates compiled by every Builder are merged into a
// single Workload.
func buildWorkload(info *runtime.Info, trainJob *trainer.TrainJob) (*schedulingv1beta1.Workload, error) {
	var workload *schedulingv1beta1.Workload
	for _, b := range newWorkloadBuilders(info, trainJob) {
		wl, err := b.BuildWorkload()
		if err != nil {
			return nil, err
		}
		if workload == nil {
			workload = wl
			continue
		}
		workload.Spec.PodGroupTemplates = append(workload.Spec.PodGroupTemplates, wl.Spec.PodGroupTemplates...)
	}
	return workload, nil
}

// ValidateScheduling validates the runtime spec.scheduling with the workloadbuilder library, which
// runs the upstream declarative validation and the scheduling policy checks of the PodGroupTemplates.
// The PodGroupTemplates are built for a TrainJob without spec, since their validation doesn't depend
// on the TrainJob.
// TODO (andreyvelich): Refactor this validation once users can override .scheduling via TrainJob.
func ValidateScheduling(ctx context.Context, spec *trainer.TrainingRuntimeSpec) field.ErrorList {
	if spec.Scheduling == nil {
		return nil
	}
	trainJob := &trainer.TrainJob{ObjectMeta: metav1.ObjectMeta{Name: "trainjob"}}
	opts := []runtime.InfoOption{runtime.WithScheduling(spec.Scheduling)}
	for _, rJob := range spec.Template.Spec.ReplicatedJobs {
		opts = append(opts, runtime.WithPodSet(rJob.Name, nil, 1, corev1.PodSpec{}, corev1ac.PodSpec()))
	}
	var allErrs field.ErrorList
	for _, b := range newWorkloadBuilders(runtime.NewInfo(opts...), trainJob) {
		allErrs = append(allErrs, b.Validate(ctx, workloadbuilder.ValidationInput{})...)
	}
	return allErrs
}

// workloadName returns the name of the Workload owned by the TrainJob.
func workloadName(trainJob *trainer.TrainJob) string {
	return hashedName(trainJob.Name, nameHash(string(trainJob.UID)))
}

// podGroupTemplateName returns the name of the PodGroupTemplate which the given ReplicatedJob
// belongs to, or false when no replicatedJobs entry targets the ReplicatedJob.
func podGroupTemplateName(trainJob *trainer.TrainJob, scheduling *trainer.TrainingRuntimeScheduling, rJobName string) (string, bool) {
	if len(scheduling.ReplicatedJobs) == 0 {
		return trainJob.Name, true
	}
	for i, entry := range scheduling.ReplicatedJobs {
		if !slices.Contains(entry.TargetReplicatedJobs, rJobName) {
			continue
		}
		if entry.Job != nil {
			// The targeted ReplicatedJob has at most 1 replica, so its only Job has the index 0.
			return boundedName(fmt.Sprintf("%s-%d", rJobName, 0)), true
		}
		// ReplicatedJob names can't start with a digit, so the entry index keeps the name unique.
		return boundedName(fmt.Sprintf("%d-%s", i, strings.Join(entry.TargetReplicatedJobs, "-"))), true
	}
	return "", false
}

// podGroupName returns the name of the PodGroup materialized from the given PodGroupTemplate.
// The TrainJob-level PodGroupTemplate is named after the TrainJob, so its redundant name segment
// is omitted.
func podGroupName(trainJob *trainer.TrainJob, templateName string) string {
	wlName := workloadName(trainJob)
	prefix := wlName
	if templateName != trainJob.Name {
		prefix = fmt.Sprintf("%s-%s", wlName, templateName)
	}
	return hashedName(prefix, nameHash(fmt.Sprintf("%s/%s", wlName, templateName)))
}

// boundedName returns the name if it fits into a DNS label, and a truncated hashed name otherwise.
func boundedName(name string) string {
	if len(name) <= maxNameLength {
		return name
	}
	return hashedName(name, nameHash(name))
}

// hashedName appends "-<hash>" to the prefix, truncating the prefix so the name fits into a DNS
// label while remaining deterministic and unique, since the hash encodes the full identity.
func hashedName(prefix, hash string) string {
	name := fmt.Sprintf("%s-%s", prefix, hash)
	if len(name) <= maxNameLength {
		return name
	}
	return fmt.Sprintf("%s-%s", prefix[:maxNameLength-1-len(hash)], hash)
}

// nameHash returns the hash of the identity used in the generated names.
func nameHash(identity string) string {
	hash := sha1.Sum([]byte(identity))
	return hex.EncodeToString(hash[:])[:nameHashLength]
}
