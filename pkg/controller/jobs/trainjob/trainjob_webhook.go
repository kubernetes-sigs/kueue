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

package trainjob

import (
	"context"

	kftrainerapi "github.com/kubeflow/trainer/v2/pkg/apis/trainer/v1alpha1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	controllerconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobframework/webhook"
)

type TrainJobWebhook struct {
	client                       client.Client
	manageJobsWithoutQueueName   bool
	managedJobsNamespaceSelector labels.Selector
	queues                       *qcache.Manager
	cache                        *schdcache.Cache
}

// SetupTrainJobWebhook configures the webhook for kubeflow TrainJob.
func SetupTrainJobWebhook(mgr ctrl.Manager, opts ...jobframework.Option) error {
	options := jobframework.ProcessOptions(opts...)
	wh := &TrainJobWebhook{
		client:                       mgr.GetClient(),
		manageJobsWithoutQueueName:   options.ManageJobsWithoutQueueName,
		managedJobsNamespaceSelector: options.ManagedJobsNamespaceSelector,
		queues:                       options.Queues,
		cache:                        options.Cache,
	}
	obj := &kftrainerapi.TrainJob{}
	return webhook.WebhookManagedBy(mgr).
		For(obj).
		WithMutationHandler(admission.WithCustomDefaulter(mgr.GetScheme(), obj, wh)).
		WithValidator(wh).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-trainer-kubeflow-org-v1alpha1-trainjob,mutating=true,failurePolicy=fail,sideEffects=None,groups=trainer.kubeflow.org,resources=trainjobs,verbs=create,versions=v1alpha1,name=mtrainjob.kb.io,admissionReviewVersions=v1

var _ admission.CustomDefaulter = &TrainJobWebhook{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the type
func (w *TrainJobWebhook) Default(ctx context.Context, obj runtime.Object) error {
	trainJob := fromObject(obj)
	log := ctrl.LoggerFrom(ctx).WithName("trainjob-webhook")
	log.V(5).Info("Applying defaults")

	jobframework.ApplyDefaultLocalQueue(trainJob.Object(), w.queues.DefaultLocalQueueExist)
	jobframework.ApplyDefaultForManagedBy(trainJob, w.queues, w.cache, log)
	suspend, err := jobframework.WorkloadShouldBeSuspended(ctx, trainJob.Object(), w.client, w.manageJobsWithoutQueueName, w.managedJobsNamespaceSelector)
	if err != nil {
		return err
	}
	if suspend {
		trainJob.Suspend()
		if trainJobQueueName := jobframework.QueueNameForObject(trainJob.Object()); trainJobQueueName != "" {
			if trainJob.Spec.Labels == nil {
				trainJob.Spec.Labels = make(map[string]string, 1)
			}
			trainJob.Spec.Labels[controllerconstants.QueueLabel] = string(trainJobQueueName)
		}
	}
	return nil
}

// +kubebuilder:webhook:path=/validate-trainer-kubeflow-org-v1alpha1-trainjob,mutating=false,failurePolicy=fail,sideEffects=None,groups=trainer.kubeflow.org,resources=trainjobs,verbs=create;update,versions=v1alpha1,name=vtrainjob.kb.io,admissionReviewVersions=v1

var _ admission.CustomValidator = &TrainJobWebhook{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *TrainJobWebhook) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	trainjob := fromObject(obj)
	log := ctrl.LoggerFrom(ctx).WithName("trainjob-webhook")
	log.Info("Validating create")
	return nil, w.validateCreate(trainjob).ToAggregate()
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *TrainJobWebhook) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	oldTrainJob := fromObject(oldObj)
	newTrainJob := fromObject(newObj)
	log := ctrl.LoggerFrom(ctx).WithName("trainjob-webhook")
	log.Info("Validating update")
	return nil, w.validateUpdate(oldTrainJob, newTrainJob).ToAggregate()
}

func (w *TrainJobWebhook) validateUpdate(oldTrainJob, newTrainJob *TrainJob) field.ErrorList {
	var allErrs field.ErrorList
	allErrs = append(allErrs, jobframework.ValidateJobOnUpdate(oldTrainJob, newTrainJob, w.queues.DefaultLocalQueueExist)...)
	allErrs = append(allErrs, w.validateCreate(newTrainJob)...)
	return allErrs
}

func (w *TrainJobWebhook) validateCreate(trainjob *TrainJob) field.ErrorList {
	var allErrs field.ErrorList
	allErrs = append(allErrs, jobframework.ValidateJobOnCreate(trainjob)...)
	return allErrs
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type
func (w *TrainJobWebhook) ValidateDelete(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	return nil, nil
}
