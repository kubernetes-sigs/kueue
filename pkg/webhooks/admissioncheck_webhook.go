/*
Copyright 2023 The Kubernetes Authors.

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

	apivalidation "k8s.io/apimachinery/pkg/api/validation"
	"k8s.io/apimachinery/pkg/runtime"
	utilvalidation "k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
)

type AdmissionCheckWebhook struct{}

func setupWebhookForAdmissionCheck(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(&kueue.AdmissionCheck{}).
		WithValidator(&AdmissionCheckWebhook{}).
		Complete()
}

// +kubebuilder:webhook:path=/validate-kueue-x-k8s-io-v1beta1-admissioncheck,mutating=false,failurePolicy=fail,sideEffects=None,groups=kueue.x-k8s.io,resources=admissionchecks,verbs=create;update,versions=v1beta1,name=vadmissioncheck.kb.io,admissionReviewVersions=v1

var _ webhook.CustomValidator = &AdmissionCheckWebhook{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *AdmissionCheckWebhook) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	ac := obj.(*kueue.AdmissionCheck)
	log := ctrl.LoggerFrom(ctx).WithName("admissioncheck-webhook")
	log.V(5).Info("Validating create", "admissionCheck", klog.KObj(ac))
	return nil, validateAdmissionCheck(ac).ToAggregate()
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *AdmissionCheckWebhook) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	oldAc := oldObj.(*kueue.AdmissionCheck)
	newAc := newObj.(*kueue.AdmissionCheck)
	log := ctrl.LoggerFrom(ctx).WithName("admissioncheck-webhook")
	log.V(5).Info("Validating update", "admissionCheck", klog.KObj(newAc))
	return nil, validateAdmissionCheckUpdate(oldAc, newAc).ToAggregate()
}

func validateAdmissionCheckUpdate(oldAc, newAc *kueue.AdmissionCheck) field.ErrorList {
	var allErrs field.ErrorList
	allErrs = append(allErrs, apivalidation.ValidateImmutableField(oldAc.Spec.ControllerName, newAc.Spec.ControllerName, field.NewPath("spec", "controllerName"))...)
	allErrs = append(allErrs, validateAdmissionCheck(newAc)...)
	return allErrs
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type
func (w *AdmissionCheckWebhook) ValidateDelete(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

func validateAdmissionCheck(ac *kueue.AdmissionCheck) field.ErrorList {
	var allErrs field.ErrorList
	specPath := field.NewPath("spec")
	if len(ac.Spec.ControllerName) == 0 {
		allErrs = append(allErrs, field.Required(specPath.Child("controllerName"), utilvalidation.EmptyError()))
	}
	if ac.Spec.Parameters != nil {
		allErrs = append(allErrs, validateParameters(ac.Spec.Parameters, field.NewPath("spec", "parameters"))...)
	}
	return allErrs
}

func validateParameters(ref *kueue.AdmissionCheckParametersReference, fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList

	if errs := utilvalidation.IsDNS1123Subdomain(ref.APIGroup); len(errs) > 0 {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("apiGroup"), ref.APIGroup, "should match: "+strings.Join(errs, ",")))
	}

	if errs := utilvalidation.IsDNS1035Label(strings.ToLower(ref.Kind)); len(errs) > 0 {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("kind"), ref.Kind, "may have mixed case, but should otherwise match: "+strings.Join(errs, ",")))
	}

	if errs := utilvalidation.IsDNS1123Label(ref.Name); len(errs) > 0 {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("name"), ref.Name, "should match: "+strings.Join(errs, ",")))
	}
	return allErrs
}
