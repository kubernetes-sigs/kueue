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

package deployment

import (
	"context"

	appsv1 "k8s.io/api/apps/v1"
	apivalidation "k8s.io/apimachinery/pkg/api/validation"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobframework/webhook"
)

type Webhook struct {
	client client.Client
}

func SetupWebhook(mgr ctrl.Manager, _ ...jobframework.Option) error {
	wh := &Webhook{
		client: mgr.GetClient(),
	}
	obj := &appsv1.Deployment{}
	return webhook.WebhookManagedBy(mgr).
		For(obj).
		WithMutationHandler(webhook.WithLosslessDefaulter(mgr.GetScheme(), obj, wh)).
		WithValidator(wh).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-apps-v1-deployment,mutating=true,failurePolicy=fail,sideEffects=None,groups="apps",resources=deployments,verbs=create,versions=v1,name=mdeployment.kb.io,admissionReviewVersions=v1

var _ admission.CustomDefaulter = &Webhook{}

func (wh *Webhook) Default(ctx context.Context, obj runtime.Object) error {
	deployment := fromObject(obj)

	log := ctrl.LoggerFrom(ctx).WithName("deployment-webhook")
	log.V(5).Info("Applying defaults")

	if queueName := jobframework.QueueNameForObject(deployment.Object()); queueName != "" {
		if deployment.Spec.Template.Labels == nil {
			deployment.Spec.Template.Labels = make(map[string]string, 1)
		}
		deployment.Spec.Template.Labels[constants.QueueLabel] = queueName
	}

	return nil
}

// +kubebuilder:webhook:path=/validate-apps-v1-deployment,mutating=false,failurePolicy=fail,sideEffects=None,groups="apps",resources=deployments,verbs=create;update,versions=v1,name=vdeployment.kb.io,admissionReviewVersions=v1

var _ admission.CustomValidator = &Webhook{}

func (wh *Webhook) ValidateCreate(ctx context.Context, obj runtime.Object) (warnings admission.Warnings, err error) {
	deployment := fromObject(obj)

	log := ctrl.LoggerFrom(ctx).WithName("deployment-webhook")
	log.V(5).Info("Validating create")

	allErrs := jobframework.ValidateQueueName(deployment.Object())

	return nil, allErrs.ToAggregate()
}

var (
	labelsPath                = field.NewPath("metadata", "labels")
	queueNameLabelPath        = labelsPath.Key(constants.QueueLabel)
	podSpecLabelPath          = field.NewPath("spec", "template", "metadata", "labels")
	podSpecQueueNameLabelPath = podSpecLabelPath.Key(constants.QueueLabel)
)

func (wh *Webhook) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (warnings admission.Warnings, err error) {
	oldDeployment := fromObject(oldObj)
	newDeployment := fromObject(newObj)

	log := ctrl.LoggerFrom(ctx).WithName("deployment-webhook")
	log.V(5).Info("Validating update")

	oldQueueName := jobframework.QueueNameForObject(oldDeployment.Object())
	newQueueName := jobframework.QueueNameForObject(newDeployment.Object())

	allErrs := apivalidation.ValidateImmutableField(oldQueueName, newQueueName, queueNameLabelPath)
	allErrs = append(allErrs, apivalidation.ValidateImmutableField(
		newDeployment.Spec.Template.GetLabels()[constants.QueueLabel],
		oldDeployment.Spec.Template.GetLabels()[constants.QueueLabel],
		podSpecQueueNameLabelPath,
	)...)

	return warnings, allErrs.ToAggregate()
}

func (wh *Webhook) ValidateDelete(context.Context, runtime.Object) (warnings admission.Warnings, err error) {
	return nil, nil
}
