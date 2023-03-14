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
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	apivalidation "k8s.io/apimachinery/pkg/api/validation"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/workload/jobframework"
	"sigs.k8s.io/kueue/pkg/util/pointer"
)

type JobWebhook struct {
	manageJobsWithoutQueueName bool
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
		For(&batchv1.Job{}).
		WithDefaulter(wh).
		WithValidator(wh).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-batch-v1-job,mutating=true,failurePolicy=fail,sideEffects=None,groups=batch,resources=jobs,verbs=create,versions=v1,name=mjob.kb.io,admissionReviewVersions=v1

var _ webhook.CustomDefaulter = &JobWebhook{}

var (
	annotationsPath       = field.NewPath("metadata", "annotations")
	suspendPath           = field.NewPath("job", "spec", "suspend")
	parentWorkloadKeyPath = annotationsPath.Key(constants.ParentWorkloadAnnotation)
)

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
			job.Annotations[constants.ParentWorkloadAnnotation] = pwName
		}
	}

	batchJob := Job{*job}
	if batchJob.QueueName() == "" && !w.manageJobsWithoutQueueName {
		return nil
	}

	if !(*job.Spec.Suspend) {
		job.Spec.Suspend = pointer.Bool(true)
	}

	return nil
}

// +kubebuilder:webhook:path=/validate-batch-v1-job,mutating=false,failurePolicy=fail,sideEffects=None,groups=batch,resources=jobs,verbs=update,versions=v1,name=vjob.kb.io,admissionReviewVersions=v1

var _ webhook.CustomValidator = &JobWebhook{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *JobWebhook) ValidateCreate(ctx context.Context, obj runtime.Object) error {
	job := obj.(*batchv1.Job)
	return validateCreate(job).ToAggregate()
}

func validateCreate(job *batchv1.Job) field.ErrorList {
	var allErrs field.ErrorList
	for _, crdNameAnnotation := range []string{constants.ParentWorkloadAnnotation, constants.QueueAnnotation} {
		if value, exists := job.Annotations[crdNameAnnotation]; exists {
			if errs := validation.IsDNS1123Subdomain(value); len(errs) > 0 {
				allErrs = append(allErrs, field.Invalid(annotationsPath.Key(crdNameAnnotation), value, strings.Join(errs, ",")))
			}
		}
	}
	return allErrs
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *JobWebhook) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) error {
	oldJob := oldObj.(*batchv1.Job)
	newJob := newObj.(*batchv1.Job)
	log := ctrl.LoggerFrom(ctx).WithName("job-webhook")
	log.V(5).Info("Validating update", "job", klog.KObj(newJob))
	return validateUpdate(oldJob, newJob).ToAggregate()
}

func validateUpdate(oldJob, newJob *batchv1.Job) field.ErrorList {
	allErrs := validateCreate(newJob)

	oldBatchJob := Job{*oldJob}
	newBatchJob := Job{*newJob}
	if !*newJob.Spec.Suspend && (oldBatchJob.QueueName() != newBatchJob.QueueName()) {
		allErrs = append(allErrs, field.Forbidden(suspendPath, "must not update queue name when job is unsuspend"))
	}

	if errList := apivalidation.ValidateImmutableField(newJob.Annotations[constants.ParentWorkloadAnnotation],
		oldJob.Annotations[constants.ParentWorkloadAnnotation], parentWorkloadKeyPath); len(errList) > 0 {
		allErrs = append(allErrs, field.Forbidden(parentWorkloadKeyPath, "this annotation is immutable"))
	}
	return allErrs
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type
func (w *JobWebhook) ValidateDelete(ctx context.Context, obj runtime.Object) error {
	return nil
}
