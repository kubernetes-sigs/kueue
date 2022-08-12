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
	"strings"

	corev1 "k8s.io/api/core/v1"
	metavalidation "k8s.io/apimachinery/pkg/apis/meta/v1/validation"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
)

// log is for logging in this package.
var resourceFlavorLog = ctrl.Log.WithName("resource-flavor-webhook")

type ResourceFlavorWebhook struct{}

func setupWebhookForResourceFlavor(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(&kueue.ResourceFlavor{}).
		WithDefaulter(&ResourceFlavorWebhook{}).
		WithValidator(&ResourceFlavorWebhook{}).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-kueue-x-k8s-io-v1alpha1-resourceflavor,mutating=true,failurePolicy=fail,sideEffects=None,groups=kueue.x-k8s.io,resources=resourceflavors,verbs=create,versions=v1alpha1,name=mresourceflavor.kb.io,admissionReviewVersions=v1

var _ webhook.CustomDefaulter = &ResourceFlavorWebhook{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the type
func (w *ResourceFlavorWebhook) Default(ctx context.Context, obj runtime.Object) error {
	rf := obj.(*kueue.ResourceFlavor)
	resourceFlavorLog.V(5).Info("Applying defaults", "resourceFlavor", klog.KObj(rf))

	if !controllerutil.ContainsFinalizer(rf, kueue.ResourceInUseFinalizerName) {
		controllerutil.AddFinalizer(rf, kueue.ResourceInUseFinalizerName)
	}
	return nil
}

// +kubebuilder:webhook:path=/validate-kueue-x-k8s-io-v1alpha1-resourceflavor,mutating=false,failurePolicy=fail,sideEffects=None,groups=kueue.x-k8s.io,resources=resourceflavors,verbs=create;update,versions=v1alpha1,name=vresourceflavor.kb.io,admissionReviewVersions=v1

var _ webhook.CustomValidator = &ResourceFlavorWebhook{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *ResourceFlavorWebhook) ValidateCreate(ctx context.Context, obj runtime.Object) error {
	rf := obj.(*kueue.ResourceFlavor)
	resourceFlavorLog.V(5).Info("Validating create", "resourceFlavor", klog.KObj(rf))
	return ValidateResourceFlavor(rf).ToAggregate()
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *ResourceFlavorWebhook) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) error {
	newRF := newObj.(*kueue.ResourceFlavor)
	resourceFlavorLog.V(5).Info("Validating update", "resourceFlavor", klog.KObj(newRF))
	return ValidateResourceFlavor(newRF).ToAggregate()
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type
func (w *ResourceFlavorWebhook) ValidateDelete(ctx context.Context, obj runtime.Object) error {
	return nil
}

func ValidateResourceFlavor(rf *kueue.ResourceFlavor) field.ErrorList {
	var allErrs field.ErrorList

	labelsPath := field.NewPath("labels")
	if len(rf.Labels) > 8 {
		allErrs = append(allErrs, field.Invalid(labelsPath, rf.Labels, "must have at most 8 elements"))
	}
	allErrs = append(allErrs, metavalidation.ValidateLabels(rf.Labels, labelsPath)...)

	taintsPath := field.NewPath("taints")
	if len(rf.Taints) > 8 {
		allErrs = append(allErrs, field.Invalid(taintsPath, rf.Taints, "must have at most 8 elements"))
	}
	allErrs = append(allErrs, validateNodeTaints(rf.Taints, taintsPath)...)
	return allErrs
}

// validateNodeTaints is extracted from git.k8s.io/kubernetes/pkg/apis/core/validation/validation.go
func validateNodeTaints(taints []corev1.Taint, fldPath *field.Path) field.ErrorList {
	allErrors := field.ErrorList{}

	uniqueTaints := map[corev1.TaintEffect]sets.String{}

	for i, currTaint := range taints {
		idxPath := fldPath.Index(i)
		// validate the taint key
		allErrors = append(allErrors, metavalidation.ValidateLabelName(currTaint.Key, idxPath.Child("key"))...)
		// validate the taint value
		if errs := validation.IsValidLabelValue(currTaint.Value); len(errs) != 0 {
			allErrors = append(allErrors, field.Invalid(idxPath.Child("value"), currTaint.Value, strings.Join(errs, ";")))
		}
		// validate the taint effect
		allErrors = append(allErrors, validateTaintEffect(&currTaint.Effect, false, idxPath.Child("effect"))...)

		// validate if taint is unique by <key, effect>
		if len(uniqueTaints[currTaint.Effect]) > 0 && uniqueTaints[currTaint.Effect].Has(currTaint.Key) {
			duplicatedError := field.Duplicate(idxPath, currTaint)
			duplicatedError.Detail = "taints must be unique by key and effect pair"
			allErrors = append(allErrors, duplicatedError)
			continue
		}

		// add taint to existingTaints for uniqueness check
		if len(uniqueTaints[currTaint.Effect]) == 0 {
			uniqueTaints[currTaint.Effect] = sets.String{}
		}
		uniqueTaints[currTaint.Effect].Insert(currTaint.Key)
	}
	return allErrors
}

// validateTaintEffect is extracted from git.k8s.io/kubernetes/pkg/apis/core/validation/validation.go
func validateTaintEffect(effect *corev1.TaintEffect, allowEmpty bool, fldPath *field.Path) field.ErrorList {
	if !allowEmpty && len(*effect) == 0 {
		return field.ErrorList{field.Required(fldPath, "")}
	}

	allErrors := field.ErrorList{}
	switch *effect {
	// TODO: Replace next line with subsequent commented-out line when implement TaintEffectNoScheduleNoAdmit.
	case corev1.TaintEffectNoSchedule, corev1.TaintEffectPreferNoSchedule, corev1.TaintEffectNoExecute:
		// case core.TaintEffectNoSchedule, core.TaintEffectPreferNoSchedule, core.TaintEffectNoScheduleNoAdmit, core.TaintEffectNoExecute:
	default:
		validValues := []string{
			string(corev1.TaintEffectNoSchedule),
			string(corev1.TaintEffectPreferNoSchedule),
			string(corev1.TaintEffectNoExecute),
			// TODO: Uncomment this block when implement TaintEffectNoScheduleNoAdmit.
			// string(core.TaintEffectNoScheduleNoAdmit),
		}
		allErrors = append(allErrors, field.NotSupported(fldPath, *effect, validValues))
	}
	return allErrors
}
