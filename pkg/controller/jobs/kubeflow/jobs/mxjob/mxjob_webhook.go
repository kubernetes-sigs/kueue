/*
Copyright 2023 The Kubernetes Authors.

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

package mxjob

import (
	"context"

	kftraining "github.com/kubeflow/training-operator/pkg/apis/kubeflow.org/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"sigs.k8s.io/kueue/pkg/controller/jobframework"
)

type MXJobWebhook struct {
	manageJobsWithoutQueueName bool
}

// SetupMXJobWebhook configures the webhook for kubeflow MXJob.
func SetupMXJobWebhook(mgr ctrl.Manager, opts ...jobframework.Option) error {
	options := jobframework.ProcessOptions(opts...)
	wh := &MXJobWebhook{
		manageJobsWithoutQueueName: options.ManageJobsWithoutQueueName,
	}
	return ctrl.NewWebhookManagedBy(mgr).
		For(&kftraining.MXJob{}).
		WithDefaulter(wh).
		WithValidator(wh).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-kubeflow-org-v1-mxjob,mutating=true,failurePolicy=fail,sideEffects=None,groups=kubeflow.org,resources=mxjobs,verbs=create,versions=v1,name=mmxjob.kb.io,admissionReviewVersions=v1

var _ webhook.CustomDefaulter = &MXJobWebhook{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the type
func (w *MXJobWebhook) Default(ctx context.Context, obj runtime.Object) error {
	job := fromObject(obj)
	log := ctrl.LoggerFrom(ctx).WithName("mxjob-webhook")
	log.V(5).Info("Applying defaults", "mxjob", klog.KObj(job.Object()))
	jobframework.ApplyDefaultForSuspend(job, w.manageJobsWithoutQueueName)
	return nil
}

// +kubebuilder:webhook:path=/validate-kubeflow-org-v1-mxjob,mutating=false,failurePolicy=fail,sideEffects=None,groups=kubeflow.org,resources=mxjobs,verbs=create;update,versions=v1,name=vmxjob.kb.io,admissionReviewVersions=v1

var _ webhook.CustomValidator = &MXJobWebhook{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *MXJobWebhook) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	job := fromObject(obj)
	log := ctrl.LoggerFrom(ctx).WithName("mxjob-webhook")
	log.V(5).Info("Validating create", "mxjob", klog.KObj(job.Object()))
	return nil, validateCreate(job).ToAggregate()
}

func validateCreate(job jobframework.GenericJob) field.ErrorList {
	return jobframework.ValidateCreateForQueueName(job)
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *MXJobWebhook) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	oldJob := fromObject(oldObj)
	newJob := fromObject(newObj)
	log := ctrl.LoggerFrom(ctx).WithName("mxjob-webhook")
	log.Info("Validating update", "mxjob", klog.KObj(newJob.Object()))
	allErrs := jobframework.ValidateUpdateForQueueName(oldJob, newJob)
	allErrs = append(allErrs, jobframework.ValidateUpdateForWorkloadPriorityClassName(oldJob, newJob)...)
	return nil, allErrs.ToAggregate()
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type
func (w *MXJobWebhook) ValidateDelete(context.Context, runtime.Object) (admission.Warnings, error) {
	return nil, nil
}
