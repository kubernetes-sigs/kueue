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

	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	"sigs.k8s.io/kueue/pkg/controller/jobframework"
)

type JobWebhook struct {
	manageJobsWithoutQueueName bool
}

func WebhookType() runtime.Object {
	return &batchv1.Job{}
}

// SetupWebhook configures the webhook for batchJob.
func SetupWebhook(mgr ctrl.Manager, opts ...jobframework.Option) error {
	options := jobframework.DefaultOptions
	for _, opt := range opts {
		opt(&options)
	}
	wh := &JobWebhook{
		manageJobsWithoutQueueName: options.ManageJobsWithoutQueueName,
	}
	return ctrl.NewWebhookManagedBy(mgr).
		For(WebhookType()).
		WithDefaulter(wh).
		WithValidator(wh).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-batch-v1-job,mutating=true,failurePolicy=fail,sideEffects=None,groups=batch,resources=jobs,verbs=create,versions=v1,name=mjob.kb.io,admissionReviewVersions=v1

var _ webhook.CustomDefaulter = &JobWebhook{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the type
func (w *JobWebhook) Default(ctx context.Context, obj runtime.Object) error {
	job := obj.(*batchv1.Job)
	log := ctrl.LoggerFrom(ctx).WithName("job-webhook")
	log.V(5).Info("Applying defaults", "job", klog.KObj(job))

	if owner := metav1.GetControllerOf(job); owner != nil && jobframework.KnownWorkloadOwner(owner) {
		if job.Annotations == nil {
			job.Annotations = make(map[string]string)
		}
		if pwName, err := jobframework.GetWorkloadNameForOwnerRef(owner); err != nil {
			return err
		} else {
			job.Annotations[jobframework.ParentWorkloadAnnotation] = pwName
		}
	}

	jobframework.ApplyDefaultForSuspend(&Job{*job}, w.manageJobsWithoutQueueName)
	return nil
}

// +kubebuilder:webhook:path=/validate-batch-v1-job,mutating=false,failurePolicy=fail,sideEffects=None,groups=batch,resources=jobs,verbs=update,versions=v1,name=vjob.kb.io,admissionReviewVersions=v1

var _ webhook.CustomValidator = &JobWebhook{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *JobWebhook) ValidateCreate(ctx context.Context, obj runtime.Object) error {
	job := obj.(*batchv1.Job)
	log := ctrl.LoggerFrom(ctx).WithName("job-webhook")
	log.V(5).Info("Validating create", "job", klog.KObj(job))
	return validateCreate(&Job{*job}).ToAggregate()
}

func validateCreate(job jobframework.GenericJob) field.ErrorList {
	var allErrs field.ErrorList
	allErrs = append(allErrs, jobframework.ValidateAnnotationAsCRDName(job, jobframework.ParentWorkloadAnnotation)...)
	allErrs = append(allErrs, jobframework.ValidateCreateForQueueName(job)...)
	return allErrs
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *JobWebhook) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) error {
	oldJob := oldObj.(*batchv1.Job)
	newJob := newObj.(*batchv1.Job)
	log := ctrl.LoggerFrom(ctx).WithName("job-webhook")
	log.V(5).Info("Validating update", "job", klog.KObj(newJob))
	return validateUpdate(&Job{*oldJob}, &Job{*newJob}).ToAggregate()
}

func validateUpdate(oldJob, newJob jobframework.GenericJob) field.ErrorList {
	allErrs := validateCreate(newJob)
	allErrs = append(allErrs, jobframework.ValidateUpdateForParentWorkload(oldJob, newJob)...)
	allErrs = append(allErrs, jobframework.ValidateUpdateForOriginalNodeSelectors(oldJob, newJob)...)
	allErrs = append(allErrs, jobframework.ValidateUpdateForQueueName(oldJob, newJob)...)
	return allErrs
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type
func (w *JobWebhook) ValidateDelete(ctx context.Context, obj runtime.Object) error {
	return nil
}
