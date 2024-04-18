package webhooks

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

func validateResourceName(name corev1.ResourceName, fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList
	for _, msg := range validation.IsQualifiedName(string(name)) {
		allErrs = append(allErrs, field.Invalid(fldPath, name, msg))
	}
	return allErrs
}
