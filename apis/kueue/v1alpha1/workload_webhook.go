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

package v1alpha1

import (
	apivalidation "k8s.io/apimachinery/pkg/api/validation"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
)

const (
	DefaultPodSetName = "main"
)

// log is for logging in this package.
var workloadlog = ctrl.Log.WithName("workload-webhook")

func (r *Workload) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(r).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-kueue-x-k8s-io-v1alpha1-workload,mutating=true,failurePolicy=fail,sideEffects=None,groups=kueue.x-k8s.io,resources=workloads,verbs=create;update,versions=v1alpha1,name=mworkload.kb.io,admissionReviewVersions=v1

var _ webhook.Defaulter = &Workload{}

// Default implements webhook.Defaulter so a webhook will be registered for the type
func (r *Workload) Default() {
	workloadlog.V(5).Info("defaulter", "workload", klog.KObj(r))

	for i := range r.Spec.PodSets {
		podSet := &r.Spec.PodSets[i]
		if len(podSet.Name) == 0 {
			podSet.Name = DefaultPodSetName
		}
	}
}

// +kubebuilder:webhook:path=/validate-kueue-x-k8s-io-v1alpha1-workload,mutating=false,failurePolicy=fail,sideEffects=None,groups=kueue.x-k8s.io,resources=workloads,verbs=create;update,versions=v1alpha1,name=vworkload.kb.io,admissionReviewVersions=v1

var _ webhook.Validator = &Workload{}

// ValidateCreate implements webhook.Validator so a webhook will be registered for the type
func (r *Workload) ValidateCreate() error {
	return ValidateWorkload(r).ToAggregate()
}

// ValidateUpdate implements webhook.Validator so a webhook will be registered for the type
func (r *Workload) ValidateUpdate(old runtime.Object) error {
	return ValidateWorkloadUpdate(r, old.(*Workload)).ToAggregate()
}

// ValidateDelete implements webhook.Validator so a webhook will be registered for the type
func (r *Workload) ValidateDelete() error {
	return nil
}

func ValidateWorkload(obj *Workload) field.ErrorList {
	var allErrs field.ErrorList
	specField := field.NewPath("spec")
	podSetsField := specField.Child("podSets")
	if len(obj.Spec.PodSets) == 0 {
		allErrs = append(allErrs, field.Required(podSetsField, "at least one podSet is required"))
	}

	for i, podSet := range obj.Spec.PodSets {
		if podSet.Count <= 0 {
			allErrs = append(allErrs, field.Invalid(
				podSetsField.Index(i).Child("count"),
				podSet.Count,
				"count must be greater than 0"),
			)
		}
	}

	if len(obj.Spec.PriorityClassName) > 0 {
		msgs := validation.IsDNS1123Subdomain(obj.Spec.PriorityClassName)
		if len(msgs) > 0 {
			classNameField := specField.Child("priorityClassName")
			for _, msg := range msgs {
				allErrs = append(allErrs, field.Invalid(classNameField, obj.Spec.PriorityClassName, msg))
			}
		}
	}
	return allErrs
}

func ValidateWorkloadUpdate(newObj, oldObj *Workload) field.ErrorList {
	allErrs := field.ErrorList{}
	allErrs = append(allErrs, ValidateWorkload(newObj)...)
	allErrs = append(allErrs, apivalidation.ValidateImmutableField(newObj.Spec.PodSets, oldObj.Spec.PodSets, field.NewPath("spec", "podSets"))...)
	allErrs = append(allErrs, validateQueueNameUpdate(newObj.Spec.QueueName, oldObj.Spec.QueueName)...)
	allErrs = append(allErrs, validateAdmissionUpdate(newObj.Spec.Admission, oldObj.Spec.Admission)...)

	return allErrs
}

/// validateQueueNameUpdate validates that queueName is set once
func validateQueueNameUpdate(new, old string) field.ErrorList {
	if len(old) == 0 {
		return field.ErrorList{}
	}
	return apivalidation.ValidateImmutableField(new, old, field.NewPath("spec", "queueName"))
}

// validateAdmissionUpdate validates that admission is set once
func validateAdmissionUpdate(new, old *Admission) field.ErrorList {
	if old == nil {
		return field.ErrorList{}
	}
	return apivalidation.ValidateImmutableField(new, old, field.NewPath("spec", "admission"))
}
