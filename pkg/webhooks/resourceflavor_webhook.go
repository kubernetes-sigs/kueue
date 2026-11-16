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
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/validate/content"
	metavalidation "k8s.io/apimachinery/pkg/apis/meta/v1/validation"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
)

type ResourceFlavorWebhook struct{}

func setupWebhookForResourceFlavor(mgr ctrl.Manager, roleTracker *roletracker.RoleTracker) error {
	return ctrl.NewWebhookManagedBy(mgr, &kueue.ResourceFlavor{}).
		WithDefaulter(&ResourceFlavorWebhook{}).
		WithValidator(&ResourceFlavorWebhook{}).
		WithLogConstructor(roletracker.WebhookLogConstructor(roleTracker)).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-kueue-x-k8s-io-v1beta2-resourceflavor,mutating=true,failurePolicy=fail,sideEffects=None,groups=kueue.x-k8s.io,resources=resourceflavors,verbs=create,versions=v1beta2,name=mresourceflavor.kb.io,admissionReviewVersions=v1

var _ admission.Defaulter[*kueue.ResourceFlavor] = &ResourceFlavorWebhook{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the type
func (w *ResourceFlavorWebhook) Default(ctx context.Context, rf *kueue.ResourceFlavor) error {
	log := ctrl.LoggerFrom(ctx).WithName("resourceflavor-webhook")
	log.V(5).Info("Applying defaults")

	if !controllerutil.ContainsFinalizer(rf, kueue.ResourceInUseFinalizerName) {
		controllerutil.AddFinalizer(rf, kueue.ResourceInUseFinalizerName)
	}
	return nil
}

// +kubebuilder:webhook:path=/validate-kueue-x-k8s-io-v1beta2-resourceflavor,mutating=false,failurePolicy=fail,sideEffects=None,groups=kueue.x-k8s.io,resources=resourceflavors,verbs=create;update,versions=v1beta2,name=vresourceflavor.kb.io,admissionReviewVersions=v1

var _ admission.Validator[*kueue.ResourceFlavor] = &ResourceFlavorWebhook{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *ResourceFlavorWebhook) ValidateCreate(ctx context.Context, rf *kueue.ResourceFlavor) (admission.Warnings, error) {
	log := ctrl.LoggerFrom(ctx).WithName("resourceflavor-webhook")
	log.V(5).Info("Validating create")
	return nil, ValidateResourceFlavor(rf).ToAggregate()
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *ResourceFlavorWebhook) ValidateUpdate(ctx context.Context, oldRF, newRF *kueue.ResourceFlavor) (admission.Warnings, error) {
	log := ctrl.LoggerFrom(ctx).WithName("resourceflavor-webhook")
	log.V(5).Info("Validating update")
	return nil, validateResourceFlavor(newRF, oldRF).ToAggregate()
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type
func (w *ResourceFlavorWebhook) ValidateDelete(_ context.Context, _ *kueue.ResourceFlavor) (admission.Warnings, error) {
	return nil, nil
}

func ValidateResourceFlavor(rf *kueue.ResourceFlavor) field.ErrorList {
	return validateResourceFlavor(rf, nil)
}

func validateResourceFlavor(rf, oldRF *kueue.ResourceFlavor) field.ErrorList {
	var allErrs field.ErrorList

	specPath := field.NewPath("spec")
	allErrs = append(allErrs, metavalidation.ValidateLabels(rf.Spec.NodeLabels, specPath.Child("nodeLabels"))...)

	allErrs = append(allErrs, validateNodeTaints(rf.Spec.NodeTaints, specPath.Child("nodeTaints"))...)
	allErrs = append(allErrs, validateTolerations(rf.Spec.Tolerations, specPath.Child("tolerations"))...)
	// ResourceFlavors created before the value check may hold an invalid value.
	// Check the values only when the tolerations change, so that such a
	// ResourceFlavor can still be updated, for example to remove its finalizer.
	if oldRF == nil || !equality.Semantic.DeepEqual(oldRF.Spec.Tolerations, rf.Spec.Tolerations) {
		allErrs = append(allErrs, validateTolerationValues(rf.Spec.Tolerations, specPath.Child("tolerations"))...)
	}
	return allErrs
}

// validateTolerationValues checks that the value of each Equal toleration is a
// valid label value, as kube-apiserver does for Pods. Kueue adds the
// ResourceFlavor tolerations to the Pod templates of the Jobs it admits, so an
// invalid value would only be rejected then, and those Jobs could not start.
func validateTolerationValues(tolerations []corev1.Toleration, fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList
	for i, toleration := range tolerations {
		// An empty operator means Equal.
		if toleration.Operator != corev1.TolerationOpEqual && toleration.Operator != "" {
			continue
		}
		if errs := content.IsLabelValue(toleration.Value); len(errs) != 0 {
			allErrs = append(allErrs, field.Invalid(fldPath.Index(i).Child("value"), toleration.Value, strings.Join(errs, ";")))
		}
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
		if errs := content.IsLabelValue(currTaint.Value); len(errs) != 0 {
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
