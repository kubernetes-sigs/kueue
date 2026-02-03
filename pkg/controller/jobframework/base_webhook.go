/*
Copyright The Kubernetes Authors.

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

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	"sigs.k8s.io/kueue/pkg/util/webhook"
)

// BaseWebhook applies basic defaulting and validation for jobs.
type BaseWebhook[T any] struct {
	Client                       client.Client
	ManageJobsWithoutQueueName   bool
	ManagedJobsNamespaceSelector labels.Selector
	FromObject                   func(T) GenericJob
	Queues                       *qcache.Manager
	Cache                        *schdcache.Cache
}

func BaseWebhookFactory[T runtime.Object](obj T, fromObject func(T) GenericJob) func(ctrl.Manager, ...Option) error {
	return func(mgr ctrl.Manager, opts ...Option) error {
		options := ProcessOptions(opts...)
		wh := &BaseWebhook[T]{
			Client:                       mgr.GetClient(),
			ManageJobsWithoutQueueName:   options.ManageJobsWithoutQueueName,
			ManagedJobsNamespaceSelector: options.ManagedJobsNamespaceSelector,
			FromObject:                   fromObject,
			Queues:                       options.Queues,
			Cache:                        options.Cache,
		}
		if options.NoopWebhook {
			return webhook.SetupNoopWebhook(mgr, obj)
		}
		return ctrl.NewWebhookManagedBy(mgr, obj).
			WithDefaulter(wh).
			WithValidator(wh).
			WithLogConstructor(WebhookLogConstructor(fromObject(obj).GVK(), options.RoleTracker)).
			Complete()
	}
}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the type
func (w *BaseWebhook[T]) Default(ctx context.Context, obj T) error {
	job := w.FromObject(obj)
	log := ctrl.LoggerFrom(ctx)
	log.V(5).Info("Applying defaults")
	ApplyDefaultLocalQueue(job.Object(), w.Queues.DefaultLocalQueueExist)
	if err := ApplyDefaultForSuspend(ctx, job, w.Client, w.ManageJobsWithoutQueueName, w.ManagedJobsNamespaceSelector); err != nil {
		return err
	}
	ApplyDefaultForManagedBy(job, w.Queues, w.Cache, log)
	return nil
}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *BaseWebhook[T]) ValidateCreate(ctx context.Context, obj T) (admission.Warnings, error) {
	job := w.FromObject(obj)
	log := ctrl.LoggerFrom(ctx)
	log.V(5).Info("Validating create")
	allErrs := ValidateJobOnCreate(job)
	if jobWithValidation, ok := job.(JobWithCustomValidation); ok {
		validationErrs, err := jobWithValidation.ValidateOnCreate(ctx)
		if err != nil {
			return nil, err
		}
		allErrs = append(allErrs, validationErrs...)
	}
	return nil, allErrs.ToAggregate()
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *BaseWebhook[T]) ValidateUpdate(ctx context.Context, oldObj, newObj T) (admission.Warnings, error) {
	oldJob := w.FromObject(oldObj)
	newJob := w.FromObject(newObj)
	log := ctrl.LoggerFrom(ctx)
	log.Info("Validating update")
	allErrs := ValidateJobOnUpdate(oldJob, newJob, w.Queues.DefaultLocalQueueExist)
	if jobWithValidation, ok := newJob.(JobWithCustomValidation); ok {
		validationErrs, err := jobWithValidation.ValidateOnUpdate(ctx, oldJob)
		if err != nil {
			return nil, err
		}
		allErrs = append(allErrs, validationErrs...)
	}
	return nil, allErrs.ToAggregate()
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type
func (w *BaseWebhook[T]) ValidateDelete(context.Context, T) (admission.Warnings, error) {
	return nil, nil
}

// WebhookLogConstructor adds group, kind and replicaRole information to the base log.
func WebhookLogConstructor(gvk schema.GroupVersionKind, roleTracker *roletracker.RoleTracker) func(base logr.Logger, req *admission.Request) logr.Logger {
	return func(base logr.Logger, req *admission.Request) logr.Logger {
		rtWebhookLogConstructor := roletracker.WebhookLogConstructor(roleTracker)
		return rtWebhookLogConstructor(base.WithValues("webhookGroup", gvk.Group, "webhookKind", gvk.Kind), req)
	}
}
