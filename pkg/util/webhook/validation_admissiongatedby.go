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

package webhook

import (
	"fmt"
	"slices"

	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/util/csv"
)

// Same as the constraint of the spec.ManagedBy field for Jobs
const MaxGateNameLengthForAdmissionGatedBy = 63

var admissionGatedByAnnotationsPath = field.NewPath("metadata", "annotations").Key(constants.AdmissionGatedByAnnotation)

func ValidateAdmissionGatedByAnnotationOnCreate(obj client.Object) field.ErrorList {
	var newValue = ""
	if val, exists := obj.GetAnnotations()[constants.AdmissionGatedByAnnotation]; exists {
		newValue = val
	}

	return validateAdmissionGatedByAnnotationFormat(newValue)
}

func ValidateAdmissionGatedByAnnotationOnUpdate(oldObj, newObj client.Object) field.ErrorList {
	var oldValue, newValue *string
	if val, exists := oldObj.GetAnnotations()[constants.AdmissionGatedByAnnotation]; exists {
		oldValue = &val
	}
	if val, exists := newObj.GetAnnotations()[constants.AdmissionGatedByAnnotation]; exists {
		newValue = &val
	}
	var allErrs field.ErrorList

	// Get actual values, treating nil as empty string for comparison
	oldVal := ""
	if oldValue != nil {
		oldVal = *oldValue
	}
	newVal := ""
	if newValue != nil {
		newVal = *newValue
	}

	// Cannot add annotation after creation (oldValue was nil or empty, newValue is non-empty)
	if (oldValue == nil || oldVal == "") && newVal != "" {
		allErrs = append(allErrs, field.Forbidden(admissionGatedByAnnotationsPath,
			"cannot add admission gate after creation"))
	}

	// Can only remove gates or remove entire annotation
	if oldVal != "" && newVal != "" {
		oldGates := csv.Parse(oldVal)

		for _, newGate := range csv.Parse(newVal) {
			if !slices.Contains(oldGates, newGate) {
				allErrs = append(allErrs, field.Forbidden(admissionGatedByAnnotationsPath,
					"can only remove gates, not add new ones"))
				break
			}
		}
	}

	// Validate format if annotation is present and non-empty
	allErrs = append(allErrs, validateAdmissionGatedByAnnotationFormat(newVal)...)

	return allErrs
}

// validateAdmissionGatedByAnnotationFormat validates the format of the AdmissionGatedBy annotation value.
// This is common validation logic used by both create and update operations.
func validateAdmissionGatedByAnnotationFormat(value string) field.ErrorList {
	var allErrs field.ErrorList

	// Only validate if value is non-empty
	if value == "" {
		return allErrs
	}

	gates := csv.Parse(value)
	seen := make(map[string]bool)

	for _, gate := range gates {
		// Check for empty gates
		if gate == "" {
			allErrs = append(allErrs, field.Invalid(admissionGatedByAnnotationsPath, value,
				"cannot contain empty gate names"))
			continue
		}

		// Check for duplicates
		if seen[gate] {
			allErrs = append(allErrs, field.Invalid(admissionGatedByAnnotationsPath, value,
				fmt.Sprintf("duplicate gate name: %s", gate)))
			continue
		}
		seen[gate] = true

		// This is the same test that kubernetes uses for the spec.ManagedBy field of Jobs
		allErrs = append(allErrs, validation.IsDomainPrefixedPath(admissionGatedByAnnotationsPath, gate)...)

		if len(gate) > MaxGateNameLengthForAdmissionGatedBy {
			allErrs = append(allErrs, field.TooLong(admissionGatedByAnnotationsPath, "" /*unused*/, MaxGateNameLengthForAdmissionGatedBy))
		}
	}

	return allErrs
}
