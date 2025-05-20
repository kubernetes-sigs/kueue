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
	"strings"

	corev1 "k8s.io/api/core/v1"
	metavalidation "k8s.io/apimachinery/pkg/apis/meta/v1/validation"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/features"
)

type ResourceFlavorWebhook struct{}

func setupWebhookForResourceFlavor(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(&kueue.ResourceFlavor{}).
		WithDefaulter(&ResourceFlavorWebhook{}).
		WithValidator(&ResourceFlavorWebhook{}).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-kueue-x-k8s-io-v1beta1-resourceflavor,mutating=true,failurePolicy=fail,sideEffects=None,groups=kueue.x-k8s.io,resources=resourceflavors,verbs=create,versions=v1beta1,name=mresourceflavor.kb.io,admissionReviewVersions=v1

var _ webhook.CustomDefaulter = &ResourceFlavorWebhook{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the type
func (w *ResourceFlavorWebhook) Default(ctx context.Context, obj runtime.Object) error {
	rf := obj.(*kueue.ResourceFlavor)
	log := ctrl.LoggerFrom(ctx).WithName("resourceflavor-webhook")
	log.V(5).Info("Applying defaults")

	if !controllerutil.ContainsFinalizer(rf, kueue.ResourceInUseFinalizerName) {
		controllerutil.AddFinalizer(rf, kueue.ResourceInUseFinalizerName)
	}
	return nil
}

// +kubebuilder:webhook:path=/validate-kueue-x-k8s-io-v1beta1-resourceflavor,mutating=false,failurePolicy=fail,sideEffects=None,groups=kueue.x-k8s.io,resources=resourceflavors,verbs=create;update,versions=v1beta1,name=vresourceflavor.kb.io,admissionReviewVersions=v1

var _ webhook.CustomValidator = &ResourceFlavorWebhook{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *ResourceFlavorWebhook) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	rf := obj.(*kueue.ResourceFlavor)
	log := ctrl.LoggerFrom(ctx).WithName("resourceflavor-webhook")
	log.V(5).Info("Validating create")
	return nil, ValidateResourceFlavor(rf).ToAggregate()
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *ResourceFlavorWebhook) ValidateUpdate(ctx context.Context, _, newObj runtime.Object) (admission.Warnings, error) {
	newRF := newObj.(*kueue.ResourceFlavor)
	log := ctrl.LoggerFrom(ctx).WithName("resourceflavor-webhook")
	log.V(5).Info("Validating update")
	return nil, ValidateResourceFlavor(newRF).ToAggregate()
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type
func (w *ResourceFlavorWebhook) ValidateDelete(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

func ValidateResourceFlavor(rf *kueue.ResourceFlavor) field.ErrorList {
	var allErrs field.ErrorList

	specPath := field.NewPath("spec")
	allErrs = append(allErrs, metavalidation.ValidateLabels(rf.Spec.NodeLabels, specPath.Child("nodeLabels"))...)

	allErrs = append(allErrs, validateNodeTaints(rf.Spec.NodeTaints, specPath.Child("nodeTaints"))...)
	allErrs = append(allErrs, validateTolerations(rf.Spec.Tolerations, specPath.Child("tolerations"))...)

	// Validate dynamicResources if feature gate is enabled
	if len(rf.Spec.DynamicResources) > 0 && features.Enabled(features.DynamicResourceStructuredParameters) {
		allErrs = append(allErrs, validateDynamicResources(rf.Spec.DynamicResources, specPath.Child("dynamicResources"))...)
	}

	return allErrs
}

// validateNodeTaints is extracted from git.k8s.io/kubernetes/pkg/apis/core/validation/validation.go
func validateNodeTaints(taints []corev1.Taint, fldPath *field.Path) field.ErrorList {
	var allErrors field.ErrorList

	uniqueTaints := make(map[corev1.TaintEffect]sets.Set[string])

	for i, currTaint := range taints {
		idxPath := fldPath.Index(i)
		// validate the taint key
		allErrors = append(allErrors, metavalidation.ValidateLabelName(currTaint.Key, idxPath.Child("key"))...)
		// validate the taint value
		if errs := validation.IsValidLabelValue(currTaint.Value); len(errs) != 0 {
			allErrors = append(allErrors, field.Invalid(idxPath.Child("value"), currTaint.Value, strings.Join(errs, ";")))
		}

		// validate if taint is unique by <key, effect>
		if len(uniqueTaints[currTaint.Effect]) > 0 && uniqueTaints[currTaint.Effect].Has(currTaint.Key) {
			duplicatedError := field.Duplicate(idxPath, currTaint)
			duplicatedError.Detail = "taints must be unique by key and effect pair"
			allErrors = append(allErrors, duplicatedError)
			continue
		}

		// add taint to existingTaints for uniqueness check
		if len(uniqueTaints[currTaint.Effect]) == 0 {
			uniqueTaints[currTaint.Effect] = sets.New[string]()
		}
		uniqueTaints[currTaint.Effect].Insert(currTaint.Key)
	}
	return allErrors
}

// validateDynamicResources validates the DynamicResources field
func validateDynamicResources(dynamicResources []kueue.DynamicResourceMapping, fldPath *field.Path) field.ErrorList {
	var allErrors field.ErrorList
	resourceNames := sets.NewString()

	for i, res := range dynamicResources {
		idxPath := fldPath.Index(i)

		// Validate resource name
		if len(res.Name) == 0 {
			allErrors = append(allErrors, field.Required(idxPath.Child("name"), "resource name is required"))
		} else if errs := validation.IsQualifiedName(string(res.Name)); len(errs) > 0 {
			allErrors = append(allErrors, field.Invalid(idxPath.Child("name"), res.Name, strings.Join(errs, ";")))
		}

		// Check for duplicate resource names
		if resourceNames.Has(string(res.Name)) {
			allErrors = append(allErrors, field.Duplicate(idxPath.Child("name"), res.Name))
		}
		resourceNames.Insert(string(res.Name))

		// Validate deviceClassNames
		if len(res.DeviceClassNames) == 0 {
			allErrors = append(allErrors, field.Required(idxPath.Child("deviceClassNames"), "at least one device class name is required"))
		}

		// Validate each deviceClassName
		deviceClassNames := sets.NewString()
		for j, deviceClassName := range res.DeviceClassNames {
			deviceIdxPath := idxPath.Child("deviceClassNames").Index(j)

			if len(deviceClassName) == 0 {
				allErrors = append(allErrors, field.Required(deviceIdxPath, "device class name cannot be empty"))
			} else if errs := validation.IsQualifiedName(string(deviceClassName)); len(errs) > 0 {
				allErrors = append(allErrors, field.Invalid(deviceIdxPath, deviceClassName, strings.Join(errs, ";")))
			}

			// Check for duplicate device class names
			if deviceClassNames.Has(string(deviceClassName)) {
				allErrors = append(allErrors, field.Duplicate(deviceIdxPath, deviceClassName))
			}
			deviceClassNames.Insert(string(deviceClassName))
		}
	}

	return allErrors
}
