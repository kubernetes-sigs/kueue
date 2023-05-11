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

package jobset

import (
	"context"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	jobset "sigs.k8s.io/jobset/api/jobset/v1alpha2"

	"sigs.k8s.io/kueue/pkg/controller/jobframework"
)

type JobSetWebhook struct {
	manageJobsWithoutQueueName bool
}

// SetupJobSetWebhook configures the webhook for kubeflow JobSet.
func SetupJobSetWebhook(mgr ctrl.Manager, opts ...jobframework.Option) error {
	options := jobframework.DefaultOptions
	for _, opt := range opts {
		opt(&options)
	}
	wh := &JobSetWebhook{
		manageJobsWithoutQueueName: options.ManageJobsWithoutQueueName,
	}
	return ctrl.NewWebhookManagedBy(mgr).
		For(&jobset.JobSet{}).
		WithDefaulter(wh).
		WithValidator(wh).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-jobset-x-k8s-io-v1alpha1-jobset,mutating=true,failurePolicy=fail,sideEffects=None,groups=jobset.x-k8s.io,resources=jobsets,verbs=create,versions=v1alpha1,name=mjobset.kb.io,admissionReviewVersions=v1

var _ webhook.CustomDefaulter = &JobSetWebhook{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the type
func (w *JobSetWebhook) Default(ctx context.Context, obj runtime.Object) error {
	job := obj.(*jobset.JobSet)
	log := ctrl.LoggerFrom(ctx).WithName("job-webhook")
	log.V(5).Info("Applying defaults", "job", klog.KObj(job))

	jobframework.ApplyDefaultForSuspend((*JobSet)(job), w.manageJobsWithoutQueueName)
	return nil
}

// +kubebuilder:webhook:path=/validate-jobset-x-k8s-io-v1alpha1-jobset,mutating=false,failurePolicy=fail,sideEffects=None,groups=jobset.x-k8s.io,resources=jobsets,verbs=update,versions=v1alpha1,name=vjobset.kb.io,admissionReviewVersions=v1

var _ webhook.CustomValidator = &JobSetWebhook{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *JobSetWebhook) ValidateCreate(ctx context.Context, obj runtime.Object) error {
	job := obj.(*jobset.JobSet)
	log := ctrl.LoggerFrom(ctx).WithName("job-webhook")
	log.Info("Validating create", "job", klog.KObj(job))
	js := JobSet(*job)
	return validateCreate(&js).ToAggregate()
}

func validateCreate(job jobframework.GenericJob) field.ErrorList {
	return jobframework.ValidateCreateForQueueName(job)
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *JobSetWebhook) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) error {
	oldJob := oldObj.(*jobset.JobSet)
	oldGenJob := JobSet(*oldJob)
	newJob := newObj.(*jobset.JobSet)
	newGenJob := JobSet(*newJob)
	log := ctrl.LoggerFrom(ctx).WithName("job-webhook")
	log.Info("Validating update", "job", klog.KObj(newJob))
	allErrs := jobframework.ValidateUpdateForQueueName(&oldGenJob, &newGenJob)
	allErrs = append(allErrs, jobframework.ValidateUpdateForOriginalNodeSelectors(&oldGenJob, &newGenJob)...)
	return allErrs.ToAggregate()
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type
func (w *JobSetWebhook) ValidateDelete(ctx context.Context, obj runtime.Object) error {
	return nil
}
