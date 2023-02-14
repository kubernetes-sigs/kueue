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
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/util/pointer"
)

type JobWebhook struct {
	manageJobsWithoutQueueName bool
}

// SetupWebhook configures the webhook for batchJob.
func SetupWebhook(mgr ctrl.Manager, opts ...Option) error {
	options := defaultOptions
	for _, opt := range opts {
		opt(&options)
	}
	wh := &JobWebhook{
		manageJobsWithoutQueueName: options.manageJobsWithoutQueueName,
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
	parentWorkloadKeyPath = field.NewPath("metadata", "annotations").Key(constants.ParentWorkloadAnnotation)
)

// Default implements webhook.CustomDefaulter so a webhook will be registered for the type
func (w *JobWebhook) Default(ctx context.Context, obj runtime.Object) error {
	job := obj.(*batchv1.Job)
	log := ctrl.LoggerFrom(ctx).WithName("job-webhook")
	log.V(5).Info("Applying defaults", "job", klog.KObj(job))

	if queueName(job) == "" && !w.manageJobsWithoutQueueName {
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
	return validateCreate(job)
}

func validateCreate(job *batchv1.Job) error {
	if value, exists := job.Annotations[constants.ParentWorkloadAnnotation]; exists {
		if errs := validation.IsDNS1123Subdomain(value); len(errs) > 0 {
			return field.Invalid(parentWorkloadKeyPath, value, strings.Join(errs, ","))
		}
	}
	return nil
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *JobWebhook) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) error {
	oldJob := oldObj.(*batchv1.Job)
	newJob := newObj.(*batchv1.Job)
	log := ctrl.LoggerFrom(ctx).WithName("job-webhook")
	log.V(5).Info("Validating update", "job", klog.KObj(newJob))

	return validateUpdate(oldJob, newJob)
}

func validateUpdate(oldJob, newJob *batchv1.Job) error {
	suspendPath := field.NewPath("job", "spec", "suspend")

	if queueName(oldJob) == "" && queueName(newJob) != "" && !*newJob.Spec.Suspend {
		return field.Forbidden(suspendPath, "suspend should be true when adding the queue name")
	}

	if !*newJob.Spec.Suspend && (queueName(oldJob) != queueName(newJob)) {
		return field.Forbidden(suspendPath, "should not update queue name when job is unsuspend")
	}
	if errList := apivalidation.ValidateImmutableField(newJob.Annotations[constants.ParentWorkloadAnnotation],
		oldJob.Annotations[constants.ParentWorkloadAnnotation], parentWorkloadKeyPath); len(errList) > 0 {
		return field.Forbidden(parentWorkloadKeyPath, "this annotation is immutable")
	}
	return nil
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type
func (w *JobWebhook) ValidateDelete(ctx context.Context, obj runtime.Object) error {
	return nil
}
