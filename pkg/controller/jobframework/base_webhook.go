/*
Copyright 2024 The Kubernetes Authors.

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

package jobframework

import (
	"context"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"sigs.k8s.io/kueue/pkg/controller/jobframework/webhook"
)

// BaseWebhook applies basic defaulting and validation for jobs.
type BaseWebhook struct {
	Client                       client.Client
	ManageJobsWithoutQueueName   bool
	ManagedJobsNamespaceSelector labels.Selector
	FromObject                   func(runtime.Object) GenericJob
}

func BaseWebhookFactory(job GenericJob, fromObject func(runtime.Object) GenericJob) func(ctrl.Manager, ...Option) error {
	return func(mgr ctrl.Manager, opts ...Option) error {
		options := ProcessOptions(opts...)
		wh := &BaseWebhook{
			Client:                       mgr.GetClient(),
			ManageJobsWithoutQueueName:   options.ManageJobsWithoutQueueName,
			ManagedJobsNamespaceSelector: options.ManagedJobsNamespaceSelector,
			FromObject:                   fromObject,
		}
		return webhook.WebhookManagedBy(mgr).
			For(job.Object()).
			WithMutationHandler(webhook.WithLosslessDefaulter(mgr.GetScheme(), job.Object(), wh)).
			WithValidator(wh).
			Complete()
	}
}

var _ admission.CustomDefaulter = &BaseWebhook{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the type
func (w *BaseWebhook) Default(ctx context.Context, obj runtime.Object) error {
	job := w.FromObject(obj)
	log := ctrl.LoggerFrom(ctx)
	log.V(5).Info("Applying defaults")
	return ApplyDefaultForSuspend(ctx, job, w.Client, w.ManageJobsWithoutQueueName, w.ManagedJobsNamespaceSelector)
}

var _ admission.CustomValidator = &BaseWebhook{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *BaseWebhook) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	job := w.FromObject(obj)
	log := ctrl.LoggerFrom(ctx)
	log.V(5).Info("Validating create")
	allErrs := ValidateJobOnCreate(job)
	if jobWithValidation, ok := job.(JobWithCustomValidation); ok {
		allErrs = append(allErrs, jobWithValidation.ValidateOnCreate()...)
	}
	return nil, allErrs.ToAggregate()
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *BaseWebhook) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	oldJob := w.FromObject(oldObj)
	newJob := w.FromObject(newObj)
	log := ctrl.LoggerFrom(ctx)
	log.Info("Validating update")
	allErrs := ValidateJobOnUpdate(oldJob, newJob)
	if jobWithValidation, ok := newJob.(JobWithCustomValidation); ok {
		allErrs = append(allErrs, jobWithValidation.ValidateOnUpdate(oldJob)...)
	}
	return nil, allErrs.ToAggregate()
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type
func (w *BaseWebhook) ValidateDelete(context.Context, runtime.Object) (admission.Warnings, error) {
	return nil, nil
}
