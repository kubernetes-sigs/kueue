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

package replicaset

import (
	"context"

	appsv1 "k8s.io/api/apps/v1"
	apivalidation "k8s.io/apimachinery/pkg/api/validation"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobframework/webhook"
	"sigs.k8s.io/kueue/pkg/queue"
)

type Webhook struct {
	client                       client.Client
	manageJobsWithoutQueueName   bool
	managedJobsNamespaceSelector labels.Selector
	queues                       *queue.Manager
}

func SetupWebhook(mgr ctrl.Manager, opts ...jobframework.Option) error {
	options := jobframework.ProcessOptions(opts...)
	wh := &Webhook{
		client:                       mgr.GetClient(),
		manageJobsWithoutQueueName:   options.ManageJobsWithoutQueueName,
		managedJobsNamespaceSelector: options.ManagedJobsNamespaceSelector,
		queues:                       options.Queues,
	}
	obj := &appsv1.Deployment{}
	return webhook.WebhookManagedBy(mgr).
		For(obj).
		WithMutationHandler(webhook.WithLosslessDefaulter(mgr.GetScheme(), obj, wh)).
		WithValidator(wh).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-apps-v1-replicaset,mutating=true,failurePolicy=fail,sideEffects=None,groups="apps",resources=replicasets,verbs=create;update,versions=v1,name=mreplicaset.kb.io,admissionReviewVersions=v1

var _ admission.CustomDefaulter = &Webhook{}

func (wh *Webhook) Default(ctx context.Context, obj runtime.Object) error {
	rs := fromObject(obj)

	log := ctrl.LoggerFrom(ctx).WithName("replicaset-webhook")
	log.V(5).Info("Applying defaults")

	jobframework.ApplyDefaultLocalQueue(rs.Object(), wh.queues.DefaultLocalQueueExist)
	suspend, err := jobframework.WorkloadShouldBeSuspended(ctx, rs.Object(), wh.client, wh.manageJobsWithoutQueueName, wh.managedJobsNamespaceSelector)
	if err != nil {
		return err
	}
	if suspend {
		queueName := jobframework.QueueNameForObject(rs.Object())
		if queueName == "" {
			queueName = "manage-jobs-without-queue-name"
		}
		if rs.Spec.Template.Labels == nil {
			rs.Spec.Template.Labels = make(map[string]string, 1)
		}
		rs.Spec.Template.Labels[constants.QueueLabel] = queueName
	}

	return nil
}

// +kubebuilder:webhook:path=/validate-apps-v1-replicaset,mutating=false,failurePolicy=fail,sideEffects=None,groups="apps",resources=replicasets,verbs=create;update,versions=v1,name=vreplicaset.kb.io,admissionReviewVersions=v1

var _ admission.CustomValidator = &Webhook{}

func (wh *Webhook) ValidateCreate(ctx context.Context, obj runtime.Object) (warnings admission.Warnings, err error) {
	rs := fromObject(obj)

	log := ctrl.LoggerFrom(ctx).WithName("replicaset-webhook")
	log.V(5).Info("Validating create")

	allErrs := jobframework.ValidateQueueName(rs.Object())

	return nil, allErrs.ToAggregate()
}

var (
	labelsPath         = field.NewPath("metadata", "labels")
	queueNameLabelPath = labelsPath.Key(constants.QueueLabel)
)

func (wh *Webhook) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (warnings admission.Warnings, err error) {
	oldReplicaSet := fromObject(oldObj)
	newReplicaSet := fromObject(newObj)

	log := ctrl.LoggerFrom(ctx).WithName("replicaset-webhook")
	log.V(5).Info("Validating update")

	oldQueueName := jobframework.QueueNameForObject(oldReplicaSet.Object())
	newQueueName := jobframework.QueueNameForObject(newReplicaSet.Object())

	allErrs := field.ErrorList{}
	allErrs = append(allErrs, jobframework.ValidateQueueName(newReplicaSet.Object())...)

	// Prevents updating the queue-name if at least one Pod is not suspended
	// or if the queue-name has been deleted.
	if oldReplicaSet.Status.ReadyReplicas > 0 || newQueueName == "" {
		allErrs = append(allErrs, apivalidation.ValidateImmutableField(oldQueueName, newQueueName, queueNameLabelPath)...)
	}

	return warnings, allErrs.ToAggregate()
}

func (wh *Webhook) ValidateDelete(context.Context, runtime.Object) (warnings admission.Warnings, err error) {
	return nil, nil
}
