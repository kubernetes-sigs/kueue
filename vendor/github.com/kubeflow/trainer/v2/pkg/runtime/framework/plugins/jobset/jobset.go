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

package jobset

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apiruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
	corev1ac "k8s.io/client-go/applyconfigurations/core/v1"
	metav1ac "k8s.io/client-go/applyconfigurations/meta/v1"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
	jobsetv1alpha2 "sigs.k8s.io/jobset/api/jobset/v1alpha2"
	jobsetv1alpha2ac "sigs.k8s.io/jobset/client-go/applyconfiguration/jobset/v1alpha2"

	configapi "github.com/kubeflow/trainer/v2/pkg/apis/config/v1alpha1"
	trainer "github.com/kubeflow/trainer/v2/pkg/apis/trainer/v1alpha1"
	"github.com/kubeflow/trainer/v2/pkg/apply"
	"github.com/kubeflow/trainer/v2/pkg/constants"
	"github.com/kubeflow/trainer/v2/pkg/runtime"
	"github.com/kubeflow/trainer/v2/pkg/runtime/framework"
	jobsetplgconsts "github.com/kubeflow/trainer/v2/pkg/runtime/framework/plugins/jobset/constants"
	"github.com/kubeflow/trainer/v2/pkg/util/trainjob"
)

var (
	runtimeRefPath     = field.NewPath("spec").Child("runtimeRef")
	runtimePatchesPath = field.NewPath("spec").Child("runtimePatches")

	errorInvalidReplicas                = errors.New("replicatedJob replicas must be greater than or equal to 1")
	errorPodCountNotDivisibleByReplicas = errors.New("replicatedJob Pod count must be divisible by replicas")
)

type JobSet struct {
	client     client.Client
	restMapper meta.RESTMapper
	scheme     *apiruntime.Scheme
	logger     logr.Logger
}

var _ framework.WatchExtensionPlugin = (*JobSet)(nil)
var _ framework.PreComponentBuilderPlugin = (*JobSet)(nil)
var _ framework.ComponentBuilderPlugin = (*JobSet)(nil)
var _ framework.TrainJobStatusPlugin = (*JobSet)(nil)
var _ framework.CustomValidationPlugin = (*JobSet)(nil)

const Name = constants.JobSetKind

// +kubebuilder:rbac:groups=jobset.x-k8s.io,resources=jobsets,verbs=create;delete;get;list;watch;update;patch

func New(ctx context.Context, client client.Client, _ client.FieldIndexer, _ *configapi.Configuration) (framework.Plugin, error) {
	return &JobSet{
		client:     client,
		restMapper: client.RESTMapper(),
		scheme:     client.Scheme(),
		logger:     ctrl.LoggerFrom(ctx).WithValues("pluginName", constants.JobSetKind),
	}, nil
}

func (j *JobSet) Name() string {
	return Name
}

func (j *JobSet) Validate(ctx context.Context, info *runtime.Info, oldObj, newObj *trainer.TrainJob) (admission.Warnings, field.ErrorList) {
	var allErrs field.ErrorList
	jobSetSpec, ok := runtime.TemplateSpecApply[jobsetv1alpha2ac.JobSetSpecApplyConfiguration](info)
	if !ok {
		return nil, nil
	}

	// TODO (andreyvelich): Refactor this test to verify the ancestor label in PodTemplate.
	rJobContainerNames := make(map[string]sets.Set[string])
	for _, rJob := range jobSetSpec.ReplicatedJobs {
		rJobContainerNames[*rJob.Name] = sets.New[string]()

		// Names of initContainer and containers are unique.
		for _, c := range rJob.Template.Spec.Template.Spec.InitContainers {
			rJobContainerNames[*rJob.Name].Insert(*c.Name)
		}
		for _, c := range rJob.Template.Spec.Template.Spec.Containers {
			rJobContainerNames[*rJob.Name].Insert(*c.Name)
		}
	}

	if newObj.Spec.Initializer != nil && newObj.Spec.Initializer.Dataset != nil {
		containers, ok := rJobContainerNames[constants.DatasetInitializer]
		if !ok {
			allErrs = append(allErrs, field.Invalid(runtimeRefPath, newObj.Spec.RuntimeRef, fmt.Sprintf("must have %s job when trainJob is configured with input datasetConfig", constants.DatasetInitializer)))
		} else if !containers.Has(constants.DatasetInitializer) {
			allErrs = append(allErrs, field.Invalid(runtimeRefPath, newObj.Spec.RuntimeRef, fmt.Sprintf("must have container with name - %s in the %s job", constants.DatasetInitializer, constants.DatasetInitializer)))
		} else {
			hasVolumeMount := false
			for _, rJob := range jobSetSpec.ReplicatedJobs {
				if *rJob.Name != constants.DatasetInitializer {
					continue
				}
				for _, c := range rJob.Template.Spec.Template.Spec.Containers {
					if *c.Name != constants.DatasetInitializer {
						continue
					}
					for _, vm := range c.VolumeMounts {
						if *vm.Name == jobsetplgconsts.VolumeNameInitializer {
							hasVolumeMount = true
							break
						}
					}
				}
			}
			if !hasVolumeMount {
				allErrs = append(allErrs, field.Invalid(runtimeRefPath, newObj.Spec.RuntimeRef, fmt.Sprintf("must have volumeMount with name - %s in container %s of the %s job", jobsetplgconsts.VolumeNameInitializer, constants.DatasetInitializer, constants.DatasetInitializer)))
			}
		}
	}

	if newObj.Spec.Initializer != nil && newObj.Spec.Initializer.Model != nil {
		containers, ok := rJobContainerNames[constants.ModelInitializer]
		if !ok {
			allErrs = append(allErrs, field.Invalid(runtimeRefPath, newObj.Spec.RuntimeRef, fmt.Sprintf("must have %s job when trainJob is configured with input modelConfig", constants.ModelInitializer)))
		} else if !containers.Has(constants.ModelInitializer) {
			allErrs = append(allErrs, field.Invalid(runtimeRefPath, newObj.Spec.RuntimeRef, fmt.Sprintf("must have container with name - %s in the %s job", constants.ModelInitializer, constants.ModelInitializer)))
		} else {
			hasVolumeMount := false
			for _, rJob := range jobSetSpec.ReplicatedJobs {
				if *rJob.Name != constants.ModelInitializer {
					continue
				}
				for _, c := range rJob.Template.Spec.Template.Spec.Containers {
					if *c.Name != constants.ModelInitializer {
						continue
					}
					for _, vm := range c.VolumeMounts {
						if *vm.Name == jobsetplgconsts.VolumeNameInitializer {
							hasVolumeMount = true
							break
						}
					}
				}
			}
			if !hasVolumeMount {
				allErrs = append(allErrs, field.Invalid(runtimeRefPath, newObj.Spec.RuntimeRef, fmt.Sprintf("must have volumeMount with name - %s in container %s of the %s job", jobsetplgconsts.VolumeNameInitializer, constants.ModelInitializer, constants.ModelInitializer)))
			}
		}
	}

	allErrs = append(allErrs, j.checkRuntimePatchesImmutability(ctx, oldObj, newObj)...)

	// Validate per-patch: ensure patched resources exist in the runtime template
	for _, runtimePatch := range newObj.Spec.RuntimePatches {
		allErrs = append(allErrs, validation.IsDomainPrefixedPath(runtimePatchesPath.Child("manager"), runtimePatch.Manager)...)
		if runtimePatch.TrainingRuntimeSpec == nil || runtimePatch.TrainingRuntimeSpec.Template == nil ||
			runtimePatch.TrainingRuntimeSpec.Template.Spec == nil {
			continue
		}
		for _, rJobPatch := range runtimePatch.TrainingRuntimeSpec.Template.Spec.ReplicatedJobs {
			containers, ok := rJobContainerNames[rJobPatch.Name]
			if !ok {
				allErrs = append(allErrs, field.Invalid(runtimePatchesPath, newObj.Spec.RuntimePatches,
					"must not have replicated job that doesn't exist in the runtime job template"))
				continue
			}
			if rJobPatch.Template == nil || rJobPatch.Template.Spec == nil ||
				rJobPatch.Template.Spec.Template == nil || rJobPatch.Template.Spec.Template.Spec == nil {
				continue
			}
			podSpecPatch := rJobPatch.Template.Spec.Template.Spec

			for _, c := range podSpecPatch.InitContainers {
				if !containers.Has(c.Name) {
					allErrs = append(allErrs, field.Invalid(runtimePatchesPath, newObj.Spec.RuntimePatches,
						fmt.Sprintf("must not have initContainer that doesn't exist in the runtime job %s", rJobPatch.Name)))
				}
			}
			for _, c := range podSpecPatch.Containers {
				if !containers.Has(c.Name) {
					allErrs = append(allErrs, field.Invalid(runtimePatchesPath, newObj.Spec.RuntimePatches,
						fmt.Sprintf("must not have container that doesn't exist in the runtime job %s", rJobPatch.Name)))
				} else if len(c.Env) > 0 && (c.Name == constants.DatasetInitializer || c.Name == constants.ModelInitializer || c.Name == constants.Node) {
					allErrs = append(allErrs, field.Invalid(runtimePatchesPath, newObj.Spec.RuntimePatches,
						fmt.Sprintf("must not have envs for the %s, %s, %s containers", constants.DatasetInitializer, constants.ModelInitializer, constants.Node)))
				}
			}
		}
	}

	// Every container resources.claims entry must reference a Pod-level resourceClaims entry,
	// otherwise the API server rejects the Pod only after the JobSet is created.
	// The runtime webhook rejects dangling claims in the runtime template and
	// spec.trainer.resourceClaimsPerNode adds both the Pod-level and the container-level entries
	// (or neither while the feature gate is disabled), so a dangling reference in the merged spec
	// comes from spec.runtimePatches, or from a runtime (or runtime snapshot) created before that
	// validation existed.
	for _, rJob := range jobSetSpec.ReplicatedJobs {
		podSpec := rJob.Template.Spec.Template.Spec
		podClaims := sets.New[string]()
		for _, pc := range podSpec.ResourceClaims {
			podClaims.Insert(ptr.Deref(pc.Name, ""))
		}
		for _, c := range slices.Concat(podSpec.InitContainers, podSpec.Containers) {
			if c.Resources == nil {
				continue
			}
			for _, claim := range c.Resources.Claims {
				if name := ptr.Deref(claim.Name, ""); !podClaims.Has(name) {
					allErrs = append(allErrs, field.Invalid(runtimePatchesPath, name,
						fmt.Sprintf("container %q in job %q references resourceClaim %q which is not defined in the Pod's resourceClaims",
							ptr.Deref(c.Name, ""), ptr.Deref(rJob.Name, ""), name)))
				}
			}
		}
	}

	// Validate volumeMounts and tolerations introduced by runtime patches.
	allErrs = append(allErrs, validateRuntimePatchesPodSpec(jobSetSpec, runtimePatchesPath, newObj)...)

	// numNodes is the total number of nodes across all trainer replicas, so it must be
	// divisible by replicas to have the same number of nodes in every replicated Job.
	if trainerReplicas := runtime.TrainerReplicas(info); trainerReplicas > 1 {
		numNodes := ptr.Deref(ptr.Deref(info.FindPodSetByAncestor(constants.AncestorTrainer), runtime.PodSet{}).Count, 1)
		if newObj.Spec.Trainer != nil && newObj.Spec.Trainer.NumNodes != nil {
			numNodes = *newObj.Spec.Trainer.NumNodes
		}
		if numNodes%trainerReplicas != 0 {
			allErrs = append(allErrs, field.Invalid(field.NewPath("spec", "trainer", "numNodes"), numNodes,
				fmt.Sprintf("must be divisible by the trainer replicatedJob replicas: %d", trainerReplicas)))
		}
	}

	// Validate TrainJob name length against JobSet pod name constraints.
	if oldObj == nil {
		minMaxLen := runtime.MaxTrainJobNameLength(info, newObj)
		if len(newObj.Name) > minMaxLen {
			allErrs = append(allErrs, field.Invalid(
				field.NewPath("metadata", "name"),
				newObj.Name,
				fmt.Sprintf("TrainJob name is too long, pod names generated for this JobSet will exceed 63 characters (maximum length is %d)", minMaxLen),
			))
		}
	}

	return nil, allErrs
}

// validateRuntimePatchesPodSpec validates volumeMounts and tolerations introduced by each runtime patch.
func validateRuntimePatchesPodSpec(jobSetSpec *jobsetv1alpha2ac.JobSetSpecApplyConfiguration, runtimePatchesPath *field.Path, trainJob *trainer.TrainJob) field.ErrorList {
	var allErrs field.ErrorList

	if len(trainJob.Spec.RuntimePatches) == 0 {
		return allErrs
	}

	// For each runtime patch, validate the fields it introduces
	for i, patch := range trainJob.Spec.RuntimePatches {
		patchPath := runtimePatchesPath.Index(i)

		if patch.TrainingRuntimeSpec == nil || patch.TrainingRuntimeSpec.Template == nil ||
			patch.TrainingRuntimeSpec.Template.Spec == nil || patch.TrainingRuntimeSpec.Template.Spec.ReplicatedJobs == nil {
			continue
		}

		for j, patchRJob := range patch.TrainingRuntimeSpec.Template.Spec.ReplicatedJobs {
			if patchRJob.Template == nil || patchRJob.Template.Spec == nil ||
				patchRJob.Template.Spec.Template == nil || patchRJob.Template.Spec.Template.Spec == nil {
				continue
			}

			patchPodSpec := patchRJob.Template.Spec.Template.Spec
			replicatedJobPath := patchPath.Child("trainingRuntimeSpec", "template", "spec", "replicatedJobs").Index(j)
			podSpecPath := replicatedJobPath.Child("template", "spec", "template", "spec")

			// Validate InitContainers from this patch
			allErrs = append(allErrs, validateVolumeMountsPatch(podSpecPath.Child("initContainers"), patchPodSpec.InitContainers)...)

			// Validate Containers from this patch
			allErrs = append(allErrs, validateVolumeMountsPatch(podSpecPath.Child("containers"), patchPodSpec.Containers)...)

			// Validate Tolerations from this patch
			for t, tol := range patchPodSpec.Tolerations {
				tolerationPath := podSpecPath.Child("tolerations").Index(t)
				// Validate operator (if specified)
				if tol.Operator != "" && !slices.Contains([]corev1.TolerationOperator{corev1.TolerationOpEqual, corev1.TolerationOpExists, corev1.TolerationOpLt, corev1.TolerationOpGt}, tol.Operator) {
					allErrs = append(allErrs, field.NotSupported(tolerationPath.Child("operator"), tol.Operator, []string{string(corev1.TolerationOpEqual), string(corev1.TolerationOpExists), string(corev1.TolerationOpLt), string(corev1.TolerationOpGt)}))
				}
				// Validate operator Exists must have empty value
				if tol.Operator == corev1.TolerationOpExists && tol.Value != "" {
					allErrs = append(allErrs, field.Forbidden(tolerationPath.Child("value"), "must be empty when operator is Exists"))
				}
				// Validate empty key must use operator Exists
				if tol.Key == "" && tol.Operator != corev1.TolerationOpExists {
					allErrs = append(allErrs, field.Invalid(tolerationPath.Child("operator"), tol.Operator, "must be Exists when key is empty"))
				}
				// Validate tolerationSeconds requires effect NoExecute
				if tol.TolerationSeconds != nil && tol.Effect != corev1.TaintEffectNoExecute {
					allErrs = append(allErrs, field.Invalid(tolerationPath.Child("effect"), tol.Effect, "must be NoExecute when tolerationSeconds is set"))
				}
				// Validate effect (if specified)
				// Empty effect ("") is valid and matches any taint effect
				if tol.Effect != "" && tol.Effect != corev1.TaintEffectNoSchedule && tol.Effect != corev1.TaintEffectPreferNoSchedule && tol.Effect != corev1.TaintEffectNoExecute {
					allErrs = append(allErrs, field.NotSupported(tolerationPath.Child("effect"), tol.Effect, []string{string(corev1.TaintEffectNoSchedule), string(corev1.TaintEffectPreferNoSchedule), string(corev1.TaintEffectNoExecute), ""}))
				}
			}
		}
	}

	return allErrs
}

// validateVolumeMountsPatch validates volumeMounts in containers (or initContainers).
// It checks that volumeMounts have non-empty mountPath and no duplicate mountPaths.
// Note: Volume existence is not validated here because plugins may inject volumes during
// the EnforceMLPolicy stage (after ValidateObjects). Kubernetes will validate volume
// references when the Pod is created.
func validateVolumeMountsPatch(fldPath *field.Path, containers []trainer.ContainerPatch) field.ErrorList {
	var allErrs field.ErrorList

	for k, container := range containers {
		containerPath := fldPath.Index(k)
		mountPaths := sets.New[string]()
		for l, vm := range container.VolumeMounts {
			volumeMountPath := containerPath.Child("volumeMounts").Index(l)
			// Validate mountPath is not empty
			if vm.MountPath == "" {
				allErrs = append(allErrs, field.Required(volumeMountPath.Child("mountPath"), ""))
			}
			// Validate duplicate mountPath within the same container
			if vm.MountPath != "" {
				if mountPaths.Has(vm.MountPath) {
					allErrs = append(allErrs, field.Duplicate(volumeMountPath.Child("mountPath"), vm.MountPath))
				}
				mountPaths.Insert(vm.MountPath)
			}
		}
	}

	return allErrs
}

func (j *JobSet) checkRuntimePatchesImmutability(ctx context.Context, oldObj, newObj *trainer.TrainJob) field.ErrorList {
	var allErrs field.ErrorList

	if oldObj == nil {
		// Checking immutability makes only sense on updates
		return allErrs
	}

	jobSet := &jobsetv1alpha2.JobSet{}
	changed := !equality.Semantic.DeepEqual(oldObj.Spec.RuntimePatches, newObj.Spec.RuntimePatches)
	// Allow modifying RuntimePatches if the TrainJob is suspended before or
	// after the update (i.e. block only when it stays fully unsuspended).
	// This lets external controllers (e.g. Kueue) update RuntimePatches and
	// toggle spec.suspend in a single API request.
	oldSuspended := ptr.Equal(oldObj.Spec.Suspend, new(true))
	newSuspended := ptr.Equal(newObj.Spec.Suspend, new(true))
	if changed {
		if !oldSuspended && !newSuspended {
			allErrs = append(allErrs, field.Forbidden(runtimePatchesPath, "RuntimePatches can only be modified when the TrainJob is suspended before or after the update"))
		} else if err := j.client.Get(ctx, client.ObjectKeyFromObject(newObj), jobSet); client.IgnoreNotFound(err) != nil {
			allErrs = append(allErrs, field.InternalError(runtimePatchesPath, err))
		} else {
			// If the JobSet exists, check whether it's inactive
			// so changes won't have side effects on the JobSet's Pods
			// that are still running.
			// This can happen while the TrainJob is transitioning out
			// from unsuspended state.
			for _, replicatedJob := range jobSet.Status.ReplicatedJobsStatus {
				if replicatedJob.Active > 0 {
					allErrs = append(allErrs, field.Forbidden(runtimePatchesPath,
						fmt.Sprintf("RuntimePatches cannot be modified when the JobSet's ReplicatedJob %s is still active", replicatedJob.Name)))
				}
			}
		}
	}

	return allErrs
}

func (j *JobSet) ReconcilerBuilders() []runtime.ReconcilerBuilder {
	if _, err := j.restMapper.RESTMapping(
		schema.GroupKind{Group: jobsetv1alpha2.GroupVersion.Group, Kind: constants.JobSetKind},
		jobsetv1alpha2.SchemeGroupVersion.Version,
	); err != nil {
		// TODO (tenzen-y): After we provide the Configuration API, we should return errors based on the enabled plugins.
		j.logger.Error(err, "JobSet CRDs must be installed in advance")
	}
	return []runtime.ReconcilerBuilder{
		func(b *builder.Builder, cl client.Client, cache cache.Cache) *builder.Builder {
			return b.Watches(
				&jobsetv1alpha2.JobSet{},
				handler.EnqueueRequestForOwner(
					j.client.Scheme(), j.client.RESTMapper(), &trainer.TrainJob{}, handler.OnlyControllerOwner(),
				),
			)
		},
	}
}

// PreBuildSync consolidates the Info object with the JobSet template before any
// component is materialized.
func (j *JobSet) PreBuildSync(info *runtime.Info, trainJob *trainer.TrainJob) error {
	if err := j.syncPodNetwork(info, trainJob); err != nil {
		return err
	}
	return j.syncParallelCount(info)
}

// SyncPodNetwork identifies the Pod-to-Pod communication network endpoints for each Pod
// and stores them in PodSets.Endpoints.
func (j *JobSet) syncPodNetwork(info *runtime.Info, trainJob *trainer.TrainJob) error {
	if info == nil || trainJob == nil {
		return nil
	}
	spec, ok := runtime.TemplateSpecApply[jobsetv1alpha2ac.JobSetSpecApplyConfiguration](info)
	if !ok {
		return nil
	}
	subDomain := trainJob.Name
	if jobSetNet := spec.Network; jobSetNet != nil && jobSetNet.Subdomain != nil {
		subDomain = *jobSetNet.Subdomain
	}
	for rJobIdx, rJob := range spec.ReplicatedJobs {
		rJobReplicas := ptr.Deref(rJob.Replicas, constants.DefaultJobReplicas)
		perReplica, err := podsPerReplica(info.TemplateSpec.PodSets[rJobIdx].Count, rJobReplicas, ptr.Deref(rJob.Name, ""))
		if err != nil {
			return err
		}
		info.TemplateSpec.PodSets[rJobIdx].Endpoints = func(yield func(string) bool) {
			for replicaIdx := range rJobReplicas {
				for podIdx := range perReplica {
					endpoint := fmt.Sprintf("%s-%s-%d-%d.%s", trainJob.Name, *rJob.Name, replicaIdx, podIdx, subDomain)
					if !yield(endpoint) {
						return
					}
				}
			}
		}
	}
	return nil
}

// SyncParallelCount propagates PodSets.Count into template-level Parallelism/Completions.
// PodSets.Count is the total number of Pods across all replicas, so each replicated Job
// gets PodSets.Count / replicas Pods.
func (j *JobSet) syncParallelCount(info *runtime.Info) error {
	if info == nil {
		return nil
	}
	jobSetSpec, ok := runtime.TemplateSpecApply[jobsetv1alpha2ac.JobSetSpecApplyConfiguration](info)
	if !ok || jobSetSpec == nil {
		return nil
	}
	for _, ps := range info.TemplateSpec.PodSets {
		if ps.Count == nil {
			continue
		}
		for rJobIdx := range jobSetSpec.ReplicatedJobs {
			rJob := &jobSetSpec.ReplicatedJobs[rJobIdx]
			if rJob.Name != nil && *rJob.Name == ps.Name {
				count, err := podsPerReplica(ps.Count, ptr.Deref(rJob.Replicas, constants.DefaultJobReplicas), ps.Name)
				if err != nil {
					return err
				}
				rJob.Template.Spec.Parallelism = &count
				rJob.Template.Spec.Completions = &count
				break
			}
		}
	}
	return nil
}

// podsPerReplica returns the number of Pods in a single replicated Job, given the total
// number of Pods across all replicas.
func podsPerReplica(totalCount *int32, replicas int32, rJobName string) (int32, error) {
	count := ptr.Deref(totalCount, 1)
	if replicas < 1 {
		return 0, fmt.Errorf("%w: replicatedJob %q has %d replicas", errorInvalidReplicas, rJobName, replicas)
	}
	if count%replicas != 0 {
		return 0, fmt.Errorf("%w: replicatedJob %q has %d Pods and %d replicas", errorPodCountNotDivisibleByReplicas, rJobName, count, replicas)
	}
	return count / replicas, nil
}

func (j *JobSet) Build(ctx context.Context, info *runtime.Info, trainJob *trainer.TrainJob) ([]apiruntime.ApplyConfiguration, error) {
	if info == nil || trainJob == nil {
		return nil, fmt.Errorf("runtime info or object is missing")
	}

	// Do not update the JobSet if it already exists and is not suspended
	oldJobSet := &jobsetv1alpha2.JobSet{}
	if err := j.client.Get(ctx, client.ObjectKeyFromObject(trainJob), oldJobSet); err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, err
		}
		oldJobSet = nil
	}
	if oldJobSet != nil &&
		!ptr.Deref(trainJob.Spec.Suspend, false) &&
		!ptr.Deref(oldJobSet.Spec.Suspend, false) {
		return nil, nil
	}

	jobSetSpec, ok := runtime.TemplateSpecApply[jobsetv1alpha2ac.JobSetSpecApplyConfiguration](info)
	if !ok {
		return nil, nil
	}

	if len(jobSetSpec.ReplicatedJobs) < len(info.TemplateSpec.PodSets) {
		return nil, fmt.Errorf(
			"runtime template has %d replicatedJobs but runtime info has %d podSets",
			len(jobSetSpec.ReplicatedJobs),
			len(info.TemplateSpec.PodSets),
		)
	}

	for psIdx, ps := range info.TemplateSpec.PodSets {
		apply.UpsertVolumes(&jobSetSpec.ReplicatedJobs[psIdx].Template.Spec.Template.Spec.Volumes, ps.Volumes...)
		if ps.SchedulingGroup != nil {
			jobSetSpec.ReplicatedJobs[psIdx].Template.Spec.Template.Spec.SchedulingGroup = ps.SchedulingGroup
		}
		apply.UpsertPodResourceClaims(&jobSetSpec.ReplicatedJobs[psIdx].Template.Spec.Template.Spec.ResourceClaims, ps.ResourceClaims...)
		for containerIdx, container := range ps.Containers {
			if len(container.Command) > 0 {
				jobSetSpec.ReplicatedJobs[psIdx].Template.Spec.Template.Spec.Containers[containerIdx].Command = container.Command
			}
			if container.Image != "" {
				jobSetSpec.ReplicatedJobs[psIdx].Template.Spec.Template.Spec.Containers[containerIdx].Image = ptr.To(container.Image)
			}
			apply.UpsertEnvVars(
				&jobSetSpec.ReplicatedJobs[psIdx].Template.Spec.Template.Spec.Containers[containerIdx].Env,
				container.Env...,
			)
			apply.UpsertPort(
				&jobSetSpec.ReplicatedJobs[psIdx].Template.Spec.Template.Spec.Containers[containerIdx].Ports,
				container.Ports...,
			)
			apply.UpsertVolumeMounts(
				&jobSetSpec.ReplicatedJobs[psIdx].Template.Spec.Template.Spec.Containers[containerIdx].VolumeMounts,
				container.VolumeMounts...,
			)
			upsertContainerResourceClaims(&jobSetSpec.ReplicatedJobs[psIdx].Template.Spec.Template.Spec.Containers[containerIdx], container.ResourceClaims...)
		}
		initContainers := jobSetSpec.ReplicatedJobs[psIdx].Template.Spec.Template.Spec.InitContainers
		for containerIdx, container := range ps.InitContainers {
			if containerIdx >= len(initContainers) {
				// Auto-append a new initContainer slot so plugins only need to
				// write to the PodSet abstraction (PodSet → runtime.Container → JobSet).
				jobSetSpec.ReplicatedJobs[psIdx].Template.Spec.Template.Spec.InitContainers = append(
					jobSetSpec.ReplicatedJobs[psIdx].Template.Spec.Template.Spec.InitContainers,
					*corev1ac.Container().WithName(container.Name),
				)
				// Refresh the local slice after append.
				initContainers = jobSetSpec.ReplicatedJobs[psIdx].Template.Spec.Template.Spec.InitContainers
			}
			if len(container.Command) > 0 {
				jobSetSpec.ReplicatedJobs[psIdx].Template.Spec.Template.Spec.InitContainers[containerIdx].Command = container.Command
			}
			if container.Image != "" {
				jobSetSpec.ReplicatedJobs[psIdx].Template.Spec.Template.Spec.InitContainers[containerIdx].Image = ptr.To(container.Image)
			}
			apply.UpsertEnvVars(
				&jobSetSpec.ReplicatedJobs[psIdx].Template.Spec.Template.Spec.InitContainers[containerIdx].Env,
				container.Env...,
			)
			apply.UpsertPort(
				&jobSetSpec.ReplicatedJobs[psIdx].Template.Spec.Template.Spec.InitContainers[containerIdx].Ports,
				container.Ports...,
			)
			apply.UpsertVolumeMounts(
				&jobSetSpec.ReplicatedJobs[psIdx].Template.Spec.Template.Spec.InitContainers[containerIdx].VolumeMounts,
				container.VolumeMounts...,
			)
			upsertContainerResourceClaims(&jobSetSpec.ReplicatedJobs[psIdx].Template.Spec.Template.Spec.InitContainers[containerIdx], container.ResourceClaims...)
		}
	}

	// Init the JobSet apply configuration from the runtime template spec
	jobSetBuilder := NewBuilder(jobsetv1alpha2ac.JobSet(trainJob.Name, trainJob.Namespace).
		WithLabels(maps.Clone(info.Labels)).
		WithAnnotations(maps.Clone(info.Annotations)).
		WithSpec(jobSetSpec))

	// TODO (andreyvelich): Refactor the builder with wrappers for PodSpec.
	// TODO: Once we remove deprecated runtime.Info.Trainer, we should remove JobSet Builder with DeprecatedTrainer().
	jobSet := jobSetBuilder.
		Initializer(trainJob).
		Trainer(info, trainJob).
		PodLabels(info.Scheduler.PodLabels).
		PodAnnotations(info.Scheduler.PodAnnotations).
		Suspend(trainJob.Spec.Suspend).
		Build().
		WithOwnerReferences(metav1ac.OwnerReference().
			WithAPIVersion(trainer.GroupVersion.String()).
			WithKind(trainer.TrainJobKind).
			WithName(trainJob.Name).
			WithUID(trainJob.UID).
			WithController(true).
			WithBlockOwnerDeletion(true))

	return []apiruntime.ApplyConfiguration{jobSet}, nil
}

func (j *JobSet) Status(ctx context.Context, trainJob *trainer.TrainJob) (*trainer.TrainJobStatus, error) {
	jobSet := &jobsetv1alpha2.JobSet{}
	if err := j.client.Get(ctx, client.ObjectKeyFromObject(trainJob), jobSet); err != nil {
		if apierrors.IsNotFound(err) && trainjob.IsTrainJobFinished(trainJob) {
			// The JobSet may have been automatically deleted in case its TTL duration has been set
			// and has expired.
			return nil, nil
		}
		return nil, err
	}
	status := trainJob.Status.DeepCopy()

	if completed := meta.FindStatusCondition(jobSet.Status.Conditions, string(jobsetv1alpha2.JobSetCompleted)); completed != nil && completed.Status == metav1.ConditionTrue {
		completed.Type = trainer.TrainJobComplete
		meta.SetStatusCondition(&status.Conditions, *completed)
	}
	if failed := meta.FindStatusCondition(jobSet.Status.Conditions, string(jobsetv1alpha2.JobSetFailed)); failed != nil && failed.Status == metav1.ConditionTrue {
		failed.Type = trainer.TrainJobFailed
		meta.SetStatusCondition(&status.Conditions, *failed)
	}

	var statuses []trainer.JobStatus
	for _, status := range jobSet.Status.ReplicatedJobsStatus {
		statuses = append(statuses, trainer.JobStatus{
			Name:      status.Name,
			Ready:     &status.Ready,
			Succeeded: &status.Succeeded,
			Failed:    &status.Failed,
			Active:    &status.Active,
			Suspended: &status.Suspended,
		})
	}
	status.JobsStatus = statuses

	return status, nil
}

// upsertContainerResourceClaims upserts the DRA resource claims into the container resources.claims.
func upsertContainerResourceClaims(container *corev1ac.ContainerApplyConfiguration, claims ...corev1ac.ResourceClaimApplyConfiguration) {
	if len(claims) == 0 {
		return
	}
	if container.Resources == nil {
		container.Resources = corev1ac.ResourceRequirements()
	}
	apply.UpsertResourceClaims(&container.Resources.Claims, claims...)
}
