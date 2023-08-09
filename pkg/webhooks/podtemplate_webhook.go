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

package webhooks

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

type PodTemplateWebhook struct{}

func setupWebhookForPodTemplate(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(&corev1.PodTemplate{}).
		WithDefaulter(&PodTemplateWebhook{}).
		WithValidator(&PodTemplateWebhook{}).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-v1-job,mutating=true,failurePolicy=fail,sideEffects=None,groups="",resources=podtemplate,verbs=create,versions=v1,name=mpodtemplate.kb.io,admissionReviewVersions=v1

var _ webhook.CustomDefaulter = &PodTemplateWebhook{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the type
func (w *PodTemplateWebhook) Default(ctx context.Context, obj runtime.Object) error {
	pt := obj.(*corev1.PodTemplate)
	log := ctrl.LoggerFrom(ctx).WithName("podtemplate-webhook")
	log.V(5).Info("Applying defaults", "podtemplate", klog.KObj(pt))

	return nil
}

// +kubebuilder:webhook:path=/validate-v1-job,mutating=false,failurePolicy=fail,sideEffects=None,groups="",resources=podtemplate,verbs=create;update,versions=v1,name=vpodtemplate.kb.io,admissionReviewVersions=v1

var _ webhook.CustomValidator = &PodTemplateWebhook{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *PodTemplateWebhook) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	wl := obj.(*corev1.PodTemplate)
	log := ctrl.LoggerFrom(ctx).WithName("podtemplate-webhook")
	log.V(5).Info("Validating create", "podtemplate", klog.KObj(wl))
	return nil, ValidatePodTemplate(wl).ToAggregate()
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *PodTemplateWebhook) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	newWL := newObj.(*corev1.PodTemplate)
	oldWL := oldObj.(*corev1.PodTemplate)
	log := ctrl.LoggerFrom(ctx).WithName("podtemplate-webhook")
	log.V(5).Info("Validating update", "podtemplate", klog.KObj(newWL))
	return nil, ValidatePodTemplateUpdate(newWL, oldWL).ToAggregate()
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type
func (w *PodTemplateWebhook) ValidateDelete(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

func ValidatePodTemplate(obj *corev1.PodTemplate) field.ErrorList {
	var allErrs field.ErrorList
	specPath := field.NewPath("spec")

	if len(obj.Template.Spec.PriorityClassName) > 0 {
		msgs := validation.IsDNS1123Subdomain(obj.Template.Spec.PriorityClassName)
		if len(msgs) > 0 {
			for _, msg := range msgs {
				allErrs = append(allErrs, field.Invalid(specPath.Child("priorityClassName"), obj.Template.Spec.PriorityClassName, msg))
			}
		}
		if obj.Template.Spec.Priority == nil {
			allErrs = append(allErrs, field.Invalid(specPath.Child("priority"), obj.Template.Spec.Priority, "priority should not be nil when priorityClassName is set"))
		}
	}

	// validate initContainers
	icPath := specPath.Child("template", "spec", "initContainers")
	for ci := range obj.Template.Spec.InitContainers {
		allErrs = append(allErrs, validateContainer(&obj.Template.Spec.InitContainers[ci], icPath.Index(ci))...)
	}
	// validate containers
	cPath := specPath.Child("template", "spec", "containers")
	for ci := range obj.Template.Spec.Containers {
		allErrs = append(allErrs, validateContainer(&obj.Template.Spec.Containers[ci], cPath.Index(ci))...)
	}

	return allErrs
}

func ValidatePodTemplateUpdate(newObj, oldObj *corev1.PodTemplate) field.ErrorList {
	var allErrs field.ErrorList
	allErrs = append(allErrs, ValidatePodTemplate(newObj)...)
	return allErrs
}

func validateContainer(c *corev1.Container, path *field.Path) field.ErrorList {
	var allErrs field.ErrorList
	rPath := path.Child("resources", "requests")
	for name := range c.Resources.Requests {
		if name == corev1.ResourcePods {
			allErrs = append(allErrs, field.Invalid(rPath.Key(string(name)), corev1.ResourcePods, "the key is reserved for internal kueue use"))
		}
	}
	return allErrs
}
