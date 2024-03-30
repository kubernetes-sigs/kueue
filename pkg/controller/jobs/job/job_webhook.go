/*
Copyright 2022 The Kubernetes Authors.

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

package job

import (
	"context"
	"fmt"
	"strconv"

	batchv1 "k8s.io/api/batch/v1"
	apivalidation "k8s.io/apimachinery/pkg/api/validation"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/util/kubeversion"
)

var (
	minPodsCountAnnotationsPath   = field.NewPath("metadata", "annotations").Key(JobMinParallelismAnnotation)
	syncCompletionAnnotationsPath = field.NewPath("metadata", "annotations").Key(JobCompletionsEqualParallelismAnnotation)
)

type JobWebhook struct {
	manageJobsWithoutQueueName bool
	kubeServerVersion          *kubeversion.ServerVersionFetcher
}

// SetupWebhook configures the webhook for batchJob.
func SetupWebhook(mgr ctrl.Manager, opts ...jobframework.Option) error {
	options := jobframework.ProcessOptions(opts...)
	wh := &JobWebhook{
		manageJobsWithoutQueueName: options.ManageJobsWithoutQueueName,
		kubeServerVersion:          options.KubeServerVersion,
	}
	return ctrl.NewWebhookManagedBy(mgr).
		For(&batchv1.Job{}).
		WithDefaulter(wh).
		WithValidator(wh).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-batch-v1-job,mutating=true,failurePolicy=fail,sideEffects=None,groups=batch,resources=jobs,verbs=create,versions=v1,name=mjob.kb.io,admissionReviewVersions=v1

var _ webhook.CustomDefaulter = &JobWebhook{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the type
func (w *JobWebhook) Default(ctx context.Context, obj runtime.Object) error {
	job := fromObject(obj)
	log := ctrl.LoggerFrom(ctx).WithName("job-webhook")
	log.V(5).Info("Applying defaults", "job", klog.KObj(job))

	jobframework.ApplyDefaultForSuspend(job, w.manageJobsWithoutQueueName)
	return nil
}

// +kubebuilder:webhook:path=/validate-batch-v1-job,mutating=false,failurePolicy=fail,sideEffects=None,groups=batch,resources=jobs,verbs=create;update,versions=v1,name=vjob.kb.io,admissionReviewVersions=v1

var _ webhook.CustomValidator = &JobWebhook{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *JobWebhook) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	job := fromObject(obj)
	log := ctrl.LoggerFrom(ctx).WithName("job-webhook")
	log.V(5).Info("Validating create", "job", klog.KObj(job))
	return nil, w.validateCreate(job).ToAggregate()
}

func (w *JobWebhook) validateCreate(job *Job) field.ErrorList {
	var allErrs field.ErrorList
	allErrs = append(allErrs, jobframework.ValidateCreateForQueueName(job)...)
	allErrs = append(allErrs, w.validatePartialAdmissionCreate(job)...)
	return allErrs
}

func (w *JobWebhook) validatePartialAdmissionCreate(job *Job) field.ErrorList {
	var allErrs field.ErrorList
	if strVal, found := job.Annotations[JobMinParallelismAnnotation]; found {
		v, err := strconv.Atoi(strVal)
		if err != nil {
			allErrs = append(allErrs, field.Invalid(minPodsCountAnnotationsPath, job.Annotations[JobMinParallelismAnnotation], err.Error()))
		} else {
			if int32(v) >= job.podsCount() || v <= 0 {
				allErrs = append(allErrs, field.Invalid(minPodsCountAnnotationsPath, v, fmt.Sprintf("should be between 0 and %d", job.podsCount()-1)))
			}
		}
	}
	if strVal, found := job.Annotations[JobCompletionsEqualParallelismAnnotation]; found {
		enabled, err := strconv.ParseBool(strVal)
		if err != nil {
			allErrs = append(allErrs, field.Invalid(syncCompletionAnnotationsPath, job.Annotations[JobCompletionsEqualParallelismAnnotation], err.Error()))
		}
		if enabled {
			if job.Spec.CompletionMode == nil || *job.Spec.CompletionMode == batchv1.NonIndexedCompletion {
				allErrs = append(allErrs, field.Invalid(syncCompletionAnnotationsPath, job.Annotations[JobCompletionsEqualParallelismAnnotation], "should not be enabled for NonIndexed jobs"))
			}
			if w.kubeServerVersion != nil {
				version := w.kubeServerVersion.GetServerVersion()
				if version.String() == "" || version.LessThan(kubeversion.KubeVersion1_27) {
					allErrs = append(allErrs, field.Invalid(syncCompletionAnnotationsPath, job.Annotations[JobCompletionsEqualParallelismAnnotation], "only supported in Kubernetes 1.27 or newer"))
				}
			}
			if ptr.Deref(job.Spec.Parallelism, 1) != ptr.Deref(job.Spec.Completions, 1) {
				allErrs = append(allErrs, field.Invalid(field.NewPath("spec", "completions"), job.Spec.Completions, fmt.Sprintf("should be equal to parallelism when %s is annotation is true", JobCompletionsEqualParallelismAnnotation)))
			}
		}
	}
	return allErrs
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *JobWebhook) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	oldJob := fromObject(oldObj)
	newJob := fromObject(newObj)
	log := ctrl.LoggerFrom(ctx).WithName("job-webhook")
	log.V(5).Info("Validating update", "job", klog.KObj(newJob))
	return nil, w.validateUpdate(oldJob, newJob).ToAggregate()
}

func (w *JobWebhook) validateUpdate(oldJob, newJob *Job) field.ErrorList {
	allErrs := w.validateCreate(newJob)
	allErrs = append(allErrs, jobframework.ValidateUpdateForQueueName(oldJob, newJob)...)
	allErrs = append(allErrs, validatePartialAdmissionUpdate(oldJob, newJob)...)
	allErrs = append(allErrs, jobframework.ValidateUpdateForWorkloadPriorityClassName(oldJob, newJob)...)
	return allErrs
}

func validatePartialAdmissionUpdate(oldJob, newJob *Job) field.ErrorList {
	var allErrs field.ErrorList
	if _, found := oldJob.Annotations[JobMinParallelismAnnotation]; found {
		if !oldJob.IsSuspended() && ptr.Deref(oldJob.Spec.Parallelism, 1) != ptr.Deref(newJob.Spec.Parallelism, 1) {
			allErrs = append(allErrs, field.Forbidden(field.NewPath("spec", "parallelism"), "cannot change when partial admission is enabled and the job is not suspended"))
		}
	}
	if oldJob.IsSuspended() == newJob.IsSuspended() && !newJob.IsSuspended() && oldJob.syncCompletionWithParallelism() != newJob.syncCompletionWithParallelism() {
		allErrs = append(allErrs, field.Forbidden(syncCompletionAnnotationsPath, fmt.Sprintf("%s while the job is not suspended", apivalidation.FieldImmutableErrorMsg)))
	}
	return allErrs
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type
func (w *JobWebhook) ValidateDelete(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	return nil, nil
}
