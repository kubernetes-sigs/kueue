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

	apivalidation "k8s.io/apimachinery/pkg/api/validation"
	metav1validation "k8s.io/apimachinery/pkg/apis/meta/v1/validation"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/workload"
)

type WorkloadWebhook struct{}

func setupWebhookForWorkload(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(&kueue.Workload{}).
		WithDefaulter(&WorkloadWebhook{}).
		WithValidator(&WorkloadWebhook{}).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-kueue-x-k8s-io-v1beta1-workload,mutating=true,failurePolicy=fail,sideEffects=None,groups=kueue.x-k8s.io,resources=workloads,verbs=create;update,versions=v1beta1,name=mworkload.kb.io,admissionReviewVersions=v1

var _ webhook.CustomDefaulter = &WorkloadWebhook{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the type
func (w *WorkloadWebhook) Default(ctx context.Context, obj runtime.Object) error {
	wl := obj.(*kueue.Workload)
	log := ctrl.LoggerFrom(ctx).WithName("workload-webhook")
	log.V(5).Info("Applying defaults", "workload", klog.KObj(wl))

	// Only when we have one podSet and its name is empty,
	// we'll set it to the default name `main`.
	if len(wl.Spec.PodSets) == 1 {
		podSet := &wl.Spec.PodSets[0]
		if len(podSet.Name) == 0 {
			podSet.Name = kueue.DefaultPodSetName
		}
	}
	return nil
}

// +kubebuilder:webhook:path=/validate-kueue-x-k8s-io-v1beta1-workload,mutating=false,failurePolicy=fail,sideEffects=None,groups=kueue.x-k8s.io,resources=workloads;workloads/status,verbs=create;update,versions=v1beta1,name=vworkload.kb.io,admissionReviewVersions=v1

var _ webhook.CustomValidator = &WorkloadWebhook{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *WorkloadWebhook) ValidateCreate(ctx context.Context, obj runtime.Object) error {
	wl := obj.(*kueue.Workload)
	log := ctrl.LoggerFrom(ctx).WithName("workload-webhook")
	log.V(5).Info("Validating create", "workload", klog.KObj(wl))
	return ValidateWorkload(wl).ToAggregate()
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *WorkloadWebhook) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) error {
	newWL := newObj.(*kueue.Workload)
	oldWL := oldObj.(*kueue.Workload)
	log := ctrl.LoggerFrom(ctx).WithName("workload-webhook")
	log.V(5).Info("Validating update", "workload", klog.KObj(newWL))
	return ValidateWorkloadUpdate(newWL, oldWL).ToAggregate()
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type
func (w *WorkloadWebhook) ValidateDelete(ctx context.Context, obj runtime.Object) error {
	return nil
}

func ValidateWorkload(obj *kueue.Workload) field.ErrorList {
	var allErrs field.ErrorList
	specPath := field.NewPath("spec")

	for i := range obj.Spec.PodSets {
		allErrs = append(allErrs, validatePodSet(&obj.Spec.PodSets[i], specPath.Child("podSets").Index(i))...)
	}

	if len(obj.Spec.PriorityClassName) > 0 {
		msgs := validation.IsDNS1123Subdomain(obj.Spec.PriorityClassName)
		if len(msgs) > 0 {
			for _, msg := range msgs {
				allErrs = append(allErrs, field.Invalid(specPath.Child("priorityClassName"), obj.Spec.PriorityClassName, msg))
			}
		}
		if obj.Spec.Priority == nil {
			allErrs = append(allErrs, field.Invalid(specPath.Child("priority"), obj.Spec.Priority, "priority should not be nil when priorityClassName is set"))
		}
	}

	if len(obj.Spec.QueueName) > 0 {
		allErrs = append(allErrs, validateNameReference(obj.Spec.QueueName, specPath.Child("queueName"))...)
	}

	statusPath := field.NewPath("status")
	if workload.IsAdmitted(obj) {
		allErrs = append(allErrs, validateAdmission(obj, statusPath.Child("admission"))...)
	}

	allErrs = append(allErrs, metav1validation.ValidateConditions(obj.Status.Conditions, statusPath.Child("conditions"))...)

	return allErrs
}

func validatePodSet(ps *kueue.PodSet, path *field.Path) field.ErrorList {
	var allErrs field.ErrorList
	// Apply the same validation as container names.
	for _, msg := range validation.IsDNS1123Label(ps.Name) {
		allErrs = append(allErrs, field.Invalid(path.Child("name"), ps.Name, msg))
	}
	return allErrs
}

func validateAdmission(obj *kueue.Workload, path *field.Path) field.ErrorList {
	admission := obj.Status.Admission
	var allErrs field.ErrorList
	allErrs = append(allErrs, validateNameReference(string(admission.ClusterQueue), path.Child("clusterQueue"))...)

	names := sets.NewString()
	for _, ps := range obj.Spec.PodSets {
		names.Insert(ps.Name)
	}
	psFlavorsPath := path.Child("podSetFlavors")
	if names.Len() != len(admission.PodSetAssignments) {
		allErrs = append(allErrs, field.Invalid(psFlavorsPath, field.OmitValueType{}, "must have the same number of podSets as the spec"))
	}

	for i, ps := range admission.PodSetAssignments {
		if !names.Has(ps.Name) {
			allErrs = append(allErrs, field.NotFound(psFlavorsPath.Index(i).Child("name"), ps.Name))
		}
	}

	return allErrs
}

func ValidateWorkloadUpdate(newObj, oldObj *kueue.Workload) field.ErrorList {
	var allErrs field.ErrorList
	specPath := field.NewPath("spec")
	allErrs = append(allErrs, ValidateWorkload(newObj)...)
	allErrs = append(allErrs, apivalidation.ValidateImmutableField(newObj.Spec.PodSets, oldObj.Spec.PodSets, specPath.Child("podSets"))...)
	if workload.IsAdmitted(newObj) && workload.IsAdmitted(oldObj) {
		allErrs = append(allErrs, apivalidation.ValidateImmutableField(newObj.Spec.QueueName, oldObj.Spec.QueueName, specPath.Child("queueName"))...)
	}
	allErrs = append(allErrs, validateAdmissionUpdate(newObj.Status.Admission, oldObj.Status.Admission, field.NewPath("status", "admission"))...)

	return allErrs
}

// validateAdmissionUpdate validates that admission can be set or unset, but the
// fields within can't change.
func validateAdmissionUpdate(new, old *kueue.Admission, path *field.Path) field.ErrorList {
	if old == nil || new == nil {
		return nil
	}
	return apivalidation.ValidateImmutableField(new, old, path)
}
