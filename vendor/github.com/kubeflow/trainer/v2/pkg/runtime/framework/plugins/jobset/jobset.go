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
	"fmt"
	"maps"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apiruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/validation/field"
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

	trainer "github.com/kubeflow/trainer/v2/pkg/apis/trainer/v1alpha1"
	"github.com/kubeflow/trainer/v2/pkg/apply"
	"github.com/kubeflow/trainer/v2/pkg/constants"
	"github.com/kubeflow/trainer/v2/pkg/runtime"
	"github.com/kubeflow/trainer/v2/pkg/runtime/framework"
)

var (
	runtimeRefPath          = field.NewPath("spec").Child("runtimeRef")
	podTemplateOverridePath = field.NewPath("spec").Child("podTemplateOverrides")
)

type JobSet struct {
	client     client.Client
	restMapper meta.RESTMapper
	scheme     *apiruntime.Scheme
	logger     logr.Logger
}

var _ framework.WatchExtensionPlugin = (*JobSet)(nil)
var _ framework.PodNetworkPlugin = (*JobSet)(nil)
var _ framework.ComponentBuilderPlugin = (*JobSet)(nil)
var _ framework.TrainJobStatusPlugin = (*JobSet)(nil)
var _ framework.CustomValidationPlugin = (*JobSet)(nil)

const Name = constants.JobSetKind

// +kubebuilder:rbac:groups=jobset.x-k8s.io,resources=jobsets,verbs=create;get;list;watch;update;patch

func New(ctx context.Context, client client.Client, _ client.FieldIndexer) (framework.Plugin, error) {
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
		if containers, ok := rJobContainerNames[constants.DatasetInitializer]; !ok {
			allErrs = append(allErrs, field.Invalid(runtimeRefPath, newObj.Spec.RuntimeRef, fmt.Sprintf("must have %s job when trainJob is configured with input datasetConfig", constants.DatasetInitializer)))
		} else if !containers.Has(constants.DatasetInitializer) {
			allErrs = append(allErrs, field.Invalid(runtimeRefPath, newObj.Spec.RuntimeRef, fmt.Sprintf("must have container with name - %s in the %s job", constants.DatasetInitializer, constants.DatasetInitializer)))
		}
	}

	if newObj.Spec.Initializer != nil && newObj.Spec.Initializer.Model != nil {
		if containers, ok := rJobContainerNames[constants.ModelInitializer]; !ok {
			allErrs = append(allErrs, field.Invalid(runtimeRefPath, newObj.Spec.RuntimeRef, fmt.Sprintf("must have %s job when trainJob is configured with input modelConfig", constants.ModelInitializer)))
		} else if !containers.Has(constants.ModelInitializer) {
			allErrs = append(allErrs, field.Invalid(runtimeRefPath, newObj.Spec.RuntimeRef, fmt.Sprintf("must have container with name - %s in the %s job", constants.ModelInitializer, constants.ModelInitializer)))
		}
	}

	allErrs = append(allErrs, j.checkPodTemplateOverridesImmutability(ctx, oldObj, newObj)...)

	// TODO (andreyvelich): Validate Volumes, VolumeMounts, and Tolerations.
	for _, podTemplateOverride := range newObj.Spec.PodTemplateOverrides {
		for _, targetJob := range podTemplateOverride.TargetJobs {
			containers, ok := rJobContainerNames[targetJob.Name]
			if !ok {
				allErrs = append(allErrs, field.Invalid(podTemplateOverridePath, newObj.Spec.PodTemplateOverrides, "must not have targetJob that doesn't exist in the runtime job template"))
			}
			if podTemplateOverride.Spec != nil {
				for _, overrideContainer := range podTemplateOverride.Spec.InitContainers {
					if !containers.Has(overrideContainer.Name) {
						allErrs = append(allErrs, field.Invalid(podTemplateOverridePath, newObj.Spec.PodTemplateOverrides, fmt.Sprintf("must not have initContainer that doesn't exist in the runtime job %s", targetJob.Name)))
					}
				}
				for _, overrideContainer := range podTemplateOverride.Spec.Containers {
					if !containers.Has(overrideContainer.Name) {
						allErrs = append(allErrs, field.Invalid(podTemplateOverridePath, newObj.Spec.PodTemplateOverrides, fmt.Sprintf("must not have container that doesn't exist in the runtime job %s", targetJob.Name)))
						// Trainer and Initializer APIs should be used to set TrainJob envs for the reserved containers.
					} else if len(overrideContainer.Env) > 0 && (overrideContainer.Name == constants.DatasetInitializer || overrideContainer.Name == constants.ModelInitializer || overrideContainer.Name == constants.Node) {
						allErrs = append(allErrs, field.Invalid(podTemplateOverridePath, newObj.Spec.PodTemplateOverrides,
							fmt.Sprintf("must not have envs for the %s, %s, %s containers", constants.DatasetInitializer, constants.ModelInitializer, constants.Node)))
					}
				}
			}
		}

	}

	return nil, allErrs
}

func (j *JobSet) checkPodTemplateOverridesImmutability(ctx context.Context, oldObj, newObj *trainer.TrainJob) field.ErrorList {
	var allErrs field.ErrorList

	if oldObj == nil {
		// Checking immutability makes only sense on updates
		return allErrs
	}

	jobSet := &jobsetv1alpha2.JobSet{}
	changed := !equality.Semantic.DeepEqual(oldObj.Spec.PodTemplateOverrides, newObj.Spec.PodTemplateOverrides)
	suspended := ptr.Equal(newObj.Spec.Suspend, ptr.To(true))
	if changed {
		if !suspended {
			allErrs = append(allErrs, field.Forbidden(podTemplateOverridePath, "PodTemplateOverrides can only be modified when the TrainJob is suspended"))
		} else if err := j.client.Get(ctx, client.ObjectKeyFromObject(newObj), jobSet); client.IgnoreNotFound(err) != nil {
			allErrs = append(allErrs, field.InternalError(podTemplateOverridePath, err))
		} else {
			// If the JobSet exists, check whether it's inactive
			// so changes won't have side effects on the JobSet's Pods
			// that are still running.
			// This can happen while the TrainJob is transitioning out
			// from unsuspended state.
			for _, replicatedJob := range jobSet.Status.ReplicatedJobsStatus {
				if replicatedJob.Active > 0 {
					allErrs = append(allErrs, field.Forbidden(podTemplateOverridePath,
						fmt.Sprintf("PodTemplateOverrides cannot be modified when the JobSet's ReplicatedJob %s is still active", replicatedJob.Name)))
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

func (j *JobSet) IdentifyPodNetwork(info *runtime.Info, trainJob *trainer.TrainJob) error {
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
		// TODO: Support multiple replicas for replicated Jobs.
		// REF: https://github.com/kubeflow/trainer/issues/2318
		podCount := info.TemplateSpec.PodSets[rJobIdx].Count
		rJobReplicas := constants.DefaultJobReplicas
		info.TemplateSpec.PodSets[rJobIdx].Endpoints = func(yield func(string) bool) {
			for podIdx := range ptr.Deref(podCount, 1) {
				endpoint := fmt.Sprintf("%s-%s-%d-%d.%s", trainJob.Name, *rJob.Name, rJobReplicas-1, podIdx, subDomain)
				if !yield(endpoint) {
					return
				}
			}
		}
	}
	return nil
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

	for psIdx, ps := range info.TemplateSpec.PodSets {
		if ps.Count != nil {
			jobSetSpec.ReplicatedJobs[psIdx].Template.Spec.Parallelism = ps.Count
			jobSetSpec.ReplicatedJobs[psIdx].Template.Spec.Completions = ps.Count
		}
		apply.UpsertVolumes(&jobSetSpec.ReplicatedJobs[psIdx].Template.Spec.Template.Spec.Volumes, ps.Volumes...)
		for containerIdx, container := range ps.Containers {
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
