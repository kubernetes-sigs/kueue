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
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/kueue/pkg/constants"
)

// Same as the constraint of the spec.ManagedBy field for Jobs
const MaxGateNameLengthForAdmissionGatedBy = 63

var admissionGatedByAnnotationsPath = field.NewPath("metadata", "annotations").Key(constants.AdmissionGatedByAnnotation)

func ValidateAdmissionGatedByAnnotationOnCreate(obj client.Object) field.ErrorList {
	var newValue *string
	if val, exists := obj.GetAnnotations()[constants.AdmissionGatedByAnnotation]; exists {
		newValue = &val
	}
	return ValidateAdmissionGatedByAnnotation(nil, newValue, false)
}

func ValidateAdmissionGatedByAnnotationOnUpdate(oldObj, newObj client.Object) field.ErrorList {
	var oldValue, newValue *string
	if val, exists := oldObj.GetAnnotations()[constants.AdmissionGatedByAnnotation]; exists {
		oldValue = &val
	}
	if val, exists := newObj.GetAnnotations()[constants.AdmissionGatedByAnnotation]; exists {
		newValue = &val
	}
	return ValidateAdmissionGatedByAnnotation(oldValue, newValue, true)
}

// ValidateAdmissionGatedByAnnotation validates the AdmissionGatedBy annotation.
// oldValue and newValue are pointers to allow distinguishing between "annotation doesn't exist" (nil)
// and "annotation exists but is empty" ("").
// isUpdate indicates whether this is an update operation (true) or create operation (false).
func ValidateAdmissionGatedByAnnotation(oldValue, newValue *string, isUpdate bool) field.ErrorList {
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

	if isUpdate {
		// Cannot add annotation after creation (oldValue was nil or empty, newValue is non-empty)
		if (oldValue == nil || oldVal == "") && newVal != "" {
			allErrs = append(allErrs, field.Forbidden(admissionGatedByAnnotationsPath,
				"cannot add admission gate after creation"))
		}

		// Can only remove gates or remove entire annotation
		if oldVal != "" && newVal != "" {
			oldGates := strings.Split(oldVal, ",")

			for newGate := range strings.SplitSeq(newVal, ",") {
				if !slices.Contains(oldGates, newGate) {
					allErrs = append(allErrs, field.Forbidden(admissionGatedByAnnotationsPath,
						"can only remove gates, not add new ones"))
					break
				}
			}
		}
	}

	// Validate format if annotation is present and non-empty
	if newVal != "" {
		gates := strings.Split(newVal, ",")
		seen := make(map[string]bool)

		for _, gate := range gates {
			gate = strings.TrimSpace(gate)

			// Check for empty gates
			if gate == "" {
				allErrs = append(allErrs, field.Invalid(admissionGatedByAnnotationsPath, newVal,
					"cannot contain empty gate names"))
				continue
			}

			// Check for duplicates
			if seen[gate] {
				allErrs = append(allErrs, field.Invalid(admissionGatedByAnnotationsPath, newVal,
					fmt.Sprintf("duplicate gate name: %s", gate)))
				continue
			}
			seen[gate] = true

			allErrs = append(allErrs, validation.IsDomainPrefixedPath(admissionGatedByAnnotationsPath, gate)...)

			if len(gate) > MaxGateNameLengthForAdmissionGatedBy {
				allErrs = append(allErrs, field.TooLong(admissionGatedByAnnotationsPath, "" /*unused*/, MaxGateNameLengthForAdmissionGatedBy))
			}
		}
	}

	return allErrs
}
