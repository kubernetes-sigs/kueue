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

package webhooks

import (
	"context"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	kueuev1alpha1 "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	"sigs.k8s.io/kueue/pkg/features"
)

// DynamicResourceAllocationConfigWebhook validates create / update requests.
type DynamicResourceAllocationConfigWebhook struct{}

func setupWebhookForDynamicResourceAllocationConfig(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(&kueuev1alpha1.DynamicResourceAllocationConfig{}).
		WithValidator(&DynamicResourceAllocationConfigWebhook{}).
		Complete()
}

// +kubebuilder:webhook:path=/validate-kueue-x-k8s-io-v1alpha1-dynamicresourceallocationconfig,mutating=false,failurePolicy=fail,sideEffects=None,groups=kueue.x-k8s.io,resources=dynamicresourceallocationconfigs,verbs=create;update,versions=v1alpha1,name=vdynamicresourceallocationconfig.kb.io,admissionReviewVersions=v1

var _ webhook.CustomValidator = &DynamicResourceAllocationConfigWebhook{}

// ValidateCreate blocks CR creation when the feature-gate is off.
func (w *DynamicResourceAllocationConfigWebhook) ValidateCreate(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	if !features.Enabled(features.DynamicResourceAllocation) {
		return nil, field.Forbidden(field.NewPath(""), "DynamicResourceAllocation feature gate is disabled")
	}
	draCfg := obj.(*kueuev1alpha1.DynamicResourceAllocationConfig)

	var allErrs field.ErrorList
	if draCfg.Name != "default" {
		allErrs = append(allErrs, field.Invalid(field.NewPath("metadata", "name"), draCfg.Name, "must be 'default'"))
	}
	if draCfg.Namespace != "" {
		allErrs = append(allErrs, field.Invalid(field.NewPath("metadata", "namespace"), draCfg.Namespace, "must be empty for cluster-scoped resource"))
	}

	allErrs = append(allErrs, validateDynamicResourceAllocationConfig(draCfg)...)

	if len(allErrs) > 0 {
		return nil, allErrs.ToAggregate()
	}
	return nil, nil
}

// ValidateUpdate blocks updates when the feature-gate is off.
func (w *DynamicResourceAllocationConfigWebhook) ValidateUpdate(_ context.Context, _ runtime.Object, newObj runtime.Object) (admission.Warnings, error) {
	if !features.Enabled(features.DynamicResourceAllocation) {
		return nil, field.Forbidden(field.NewPath(""), "DynamicResourceAllocation feature gate is disabled")
	}
	draCfg := newObj.(*kueuev1alpha1.DynamicResourceAllocationConfig)
	var allErrs field.ErrorList

	if draCfg.Name != "default" {
		allErrs = append(allErrs, field.Invalid(field.NewPath("metadata", "name"), draCfg.Name, "must be 'default'"))
	}
	if draCfg.Namespace != "" {
		allErrs = append(allErrs, field.Invalid(field.NewPath("metadata", "namespace"), draCfg.Namespace, "must be empty for cluster-scoped resource"))
	}

	allErrs = append(allErrs, validateDynamicResourceAllocationConfig(draCfg)...)

	if len(allErrs) > 0 {
		return nil, allErrs.ToAggregate()
	}

	return nil, nil
}

// ValidateDelete – no special handling.
func (w *DynamicResourceAllocationConfigWebhook) ValidateDelete(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

func validateDynamicResourceAllocationConfig(draCfg *kueuev1alpha1.DynamicResourceAllocationConfig) field.ErrorList {
	var allErrs field.ErrorList

	for i := range draCfg.Spec.Resources {
		res := &draCfg.Spec.Resources[i]
		resPath := field.NewPath("spec", "resources").Index(i)
		allErrs = append(allErrs, validateResourceName(res.Name, resPath.Child("name"))...)

		for j := range res.DeviceClassNames {
			dc := res.DeviceClassNames[j]
			allErrs = append(allErrs, validateResourceName(dc, resPath.Child("deviceClassNames").Index(j))...)
		}
	}
	return allErrs
}
