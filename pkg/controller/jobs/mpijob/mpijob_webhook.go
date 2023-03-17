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

package mpijob

import (
	"context"

	kubeflow "github.com/kubeflow/mpi-operator/pkg/apis/kubeflow/v2beta1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	"sigs.k8s.io/kueue/pkg/controller/jobframework"
)

type MPIJobWebhook struct {
	manageJobsWithoutQueueName bool
}

// SetupWebhook configures the webhook for kubeflow MPIJob.
func SetupMPIJobWebhook(mgr ctrl.Manager, opts ...jobframework.Option) error {
	options := jobframework.DefaultOptions
	for _, opt := range opts {
		opt(&options)
	}
	wh := &MPIJobWebhook{
		manageJobsWithoutQueueName: options.ManageJobsWithoutQueueName,
	}
	return ctrl.NewWebhookManagedBy(mgr).
		For(&kubeflow.MPIJob{}).
		WithDefaulter(wh).
		WithValidator(wh).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-kubeflow-org-v2beta1-mpijob,mutating=true,failurePolicy=fail,sideEffects=None,groups=kubeflow.org,resources=mpijobs,verbs=create,versions=v2beta1,name=mmpijob.kb.io,admissionReviewVersions=v1

var _ webhook.CustomDefaulter = &MPIJobWebhook{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the type
func (w *MPIJobWebhook) Default(ctx context.Context, obj runtime.Object) error {
	job := obj.(*kubeflow.MPIJob)
	log := ctrl.LoggerFrom(ctx).WithName("job-webhook")
	log.V(5).Info("Applying defaults", "job", klog.KObj(job))

	jobframework.ApplyDefaultForSuspend(&MPIJob{*job}, w.manageJobsWithoutQueueName)
	return nil
}

// +kubebuilder:webhook:path=/validate-kubeflow-org-v2beta1-mpijob,mutating=false,failurePolicy=fail,sideEffects=None,groups=kubeflow.org,resources=mpijobs,verbs=update,versions=v2beta1,name=vmpijob.kb.io,admissionReviewVersions=v1

var _ webhook.CustomValidator = &MPIJobWebhook{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *MPIJobWebhook) ValidateCreate(ctx context.Context, obj runtime.Object) error {
	job := obj.(*kubeflow.MPIJob)
	log := ctrl.LoggerFrom(ctx).WithName("job-webhook")
	log.Info("Validating create", "job", klog.KObj(job))
	return validateCreate(&MPIJob{*job}).ToAggregate()
}

func validateCreate(job jobframework.GenericJob) field.ErrorList {
	return jobframework.ValidateAnnotationAsCRDName(job, jobframework.QueueAnnotation)
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *MPIJobWebhook) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) error {
	oldJob := oldObj.(*kubeflow.MPIJob)
	newJob := newObj.(*kubeflow.MPIJob)
	log := ctrl.LoggerFrom(ctx).WithName("job-webhook")
	log.Info("Validating update", "job", klog.KObj(newJob))
	return jobframework.ValidateUpdateForQueueName(&MPIJob{*oldJob}, &MPIJob{*newJob}).ToAggregate()
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type
func (w *MPIJobWebhook) ValidateDelete(ctx context.Context, obj runtime.Object) error {
	return nil
}
