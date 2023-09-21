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

package paddlejob

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

type PaddleJobWebhook struct {
	manageJobsWithoutQueueName bool
}

// SetupPaddleJobWebhook configures the webhook for kubeflow PaddleJob.
func SetupPaddleJobWebhook(mgr ctrl.Manager, opts ...jobframework.Option) error {
	options := jobframework.DefaultOptions
	for _, opt := range opts {
		opt(&options)
	}
	wh := &PaddleJobWebhook{
		manageJobsWithoutQueueName: options.ManageJobsWithoutQueueName,
	}
	return ctrl.NewWebhookManagedBy(mgr).
		For(&kftraining.PaddleJob{}).
		WithDefaulter(wh).
		WithValidator(wh).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-kubeflow-org-v1-paddlejob,mutating=true,failurePolicy=fail,sideEffects=None,groups=kubeflow.org,resources=paddlejobs,verbs=create,versions=v1,name=mpaddlejob.kb.io,admissionReviewVersions=v1

var _ webhook.CustomDefaulter = &PaddleJobWebhook{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the type
func (w *PaddleJobWebhook) Default(ctx context.Context, obj runtime.Object) error {
	job := fromObject(obj)
	log := ctrl.LoggerFrom(ctx).WithName("paddlejob-webhook")
	log.V(5).Info("Applying defaults", "paddlejob", klog.KObj(job.Object()))
	jobframework.ApplyDefaultForSuspend(job, w.manageJobsWithoutQueueName)
	return nil
}

// +kubebuilder:webhook:path=/validate-kubeflow-org-v1-paddlejob,mutating=false,failurePolicy=fail,sideEffects=None,groups=kubeflow.org,resources=paddlejobs,verbs=create;update,versions=v1,name=vpaddlejob.kb.io,admissionReviewVersions=v1

var _ webhook.CustomValidator = &PaddleJobWebhook{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *PaddleJobWebhook) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	job := fromObject(obj)
	log := ctrl.LoggerFrom(ctx).WithName("paddlejob-webhook")
	log.Info("Validating create", "paddlejob", klog.KObj(job.Object()))
	return nil, validateCreate(job).ToAggregate()
}

func validateCreate(job jobframework.GenericJob) field.ErrorList {
	return jobframework.ValidateCreateForQueueName(job)
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *PaddleJobWebhook) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	oldJob := fromObject(oldObj)
	newJob := fromObject(newObj)
	log := ctrl.LoggerFrom(ctx).WithName("paddlejob-webhook")
	log.Info("Validating update", "paddlejob", klog.KObj(newJob.Object()))
	allErrs := jobframework.ValidateUpdateForQueueName(oldJob, newJob)
	return nil, allErrs.ToAggregate()
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type
func (w *PaddleJobWebhook) ValidateDelete(context.Context, runtime.Object) (admission.Warnings, error) {
	return nil, nil
}
