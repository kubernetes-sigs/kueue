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
	apivalidation "k8s.io/apimachinery/pkg/api/validation"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
)

// log is for logging in this package.
var workloadlog = ctrl.Log.WithName("workload-webhook")

type WorkloadWebhook struct{}

func setupWebhookForWorkload(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(&kueue.Workload{}).
		WithDefaulter(&WorkloadWebhook{}).
		WithValidator(&WorkloadWebhook{}).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-kueue-x-k8s-io-v1alpha1-workload,mutating=true,failurePolicy=fail,sideEffects=None,groups=kueue.x-k8s.io,resources=workloads,verbs=create;update,versions=v1alpha1,name=mworkload.kb.io,admissionReviewVersions=v1

var _ webhook.CustomDefaulter = &WorkloadWebhook{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the type
func (w *WorkloadWebhook) Default(ctx context.Context, obj runtime.Object) error {
	wl := obj.(*kueue.Workload)
	workloadlog.V(5).Info("Applying defaults", "workload", klog.KObj(wl))

	if len(wl.Spec.PodSets) == 1 {
		podSet := &wl.Spec.PodSets[0]
		if len(podSet.Name) == 0 {
			podSet.Name = kueue.DefaultPodSetName
		}
	}
	for i := range wl.Spec.PodSets {
		podSet := &wl.Spec.PodSets[i]
		setContainersDefaults(podSet.Spec.InitContainers)
		setContainersDefaults(podSet.Spec.Containers)
	}
	return nil
}

func setContainersDefaults(containers []corev1.Container) {
	for i := range containers {
		c := &containers[i]
		if c.Resources.Limits != nil {
			if c.Resources.Requests == nil {
				c.Resources.Requests = make(corev1.ResourceList)
			}
			for k, v := range c.Resources.Limits {
				if _, exists := c.Resources.Requests[k]; !exists {
					c.Resources.Requests[k] = v.DeepCopy()
				}
			}
		}
	}
}

// +kubebuilder:webhook:path=/validate-kueue-x-k8s-io-v1alpha1-workload,mutating=false,failurePolicy=fail,sideEffects=None,groups=kueue.x-k8s.io,resources=workloads,verbs=create;update,versions=v1alpha1,name=vworkload.kb.io,admissionReviewVersions=v1

var _ webhook.CustomValidator = &WorkloadWebhook{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *WorkloadWebhook) ValidateCreate(ctx context.Context, obj runtime.Object) error {
	wl := obj.(*kueue.Workload)
	workloadlog.V(5).Info("Validating create", "workload", klog.KObj(wl))
	return ValidateWorkload(wl).ToAggregate()
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *WorkloadWebhook) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) error {
	newWL := newObj.(*kueue.Workload)
	oldWL := oldObj.(*kueue.Workload)
	workloadlog.V(5).Info("Validating update", "workload", klog.KObj(newWL))
	return ValidateWorkloadUpdate(newWL, oldWL).ToAggregate()
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type
func (w *WorkloadWebhook) ValidateDelete(ctx context.Context, obj runtime.Object) error {
	return nil
}

func ValidateWorkload(obj *kueue.Workload) field.ErrorList {
	var allErrs field.ErrorList
	specPath := field.NewPath("spec")
	podSetsPath := specPath.Child("podSets")
	if len(obj.Spec.PodSets) == 0 {
		allErrs = append(allErrs, field.Required(podSetsPath, "at least one podSet is required"))
	}
	if len(obj.Spec.PodSets) > 8 {
		allErrs = append(allErrs, field.TooMany(podSetsPath, len(obj.Spec.PodSets), 8))
	}

	for i, podSet := range obj.Spec.PodSets {
		path := podSetsPath.Index(i)
		allErrs = append(allErrs, validatePodSetName(podSet.Name, path.Child("name"))...)
		if podSet.Count <= 0 {
			allErrs = append(allErrs, field.Invalid(
				path.Child("count"),
				podSet.Count,
				"count must be greater than 0"),
			)
		}
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

	return allErrs
}

func validatePodSetName(name string, fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList
	// Apply the same validation as container names.
	for _, msg := range validation.IsDNS1123Label(name) {
		allErrs = append(allErrs, field.Invalid(fldPath, name, msg))
	}
	return allErrs
}

func ValidateWorkloadUpdate(newObj, oldObj *kueue.Workload) field.ErrorList {
	var allErrs field.ErrorList
	specPath := field.NewPath("spec")
	allErrs = append(allErrs, ValidateWorkload(newObj)...)
	allErrs = append(allErrs, apivalidation.ValidateImmutableField(newObj.Spec.PodSets, oldObj.Spec.PodSets, specPath.Child("podSets"))...)
	allErrs = append(allErrs, validateQueueNameUpdate(newObj.Spec.QueueName, oldObj.Spec.QueueName, specPath.Child("queueName"))...)
	allErrs = append(allErrs, validateAdmissionUpdate(newObj.Spec.Admission, oldObj.Spec.Admission, specPath.Child("admission"))...)

	return allErrs
}

// validateQueueNameUpdate validates that queueName is set once
func validateQueueNameUpdate(new, old string, path *field.Path) field.ErrorList {
	if len(old) == 0 {
		return nil
	}
	return apivalidation.ValidateImmutableField(new, old, path)
}

// validateAdmissionUpdate validates that admission is set once
func validateAdmissionUpdate(new, old *kueue.Admission, path *field.Path) field.ErrorList {
	if old == nil {
		return nil
	}
	return apivalidation.ValidateImmutableField(new, old, path)
}
