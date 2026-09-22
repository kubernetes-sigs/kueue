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

package tas

import (
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	metavalidation "k8s.io/apimachinery/pkg/apis/meta/v1/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

// maxSpreadingSelectorRequirements is the number of workloadLabelSelectors
// requirements supported in the alpha milestone of TASTopologySpreading.
const maxSpreadingSelectorRequirements = 1

// ValidateSpreadingAnnotation validates a topology-spreading annotation,
// reporting errors under fldPath. Callers handle feature gates and the
// required-topology prerequisite. Omitted selectors are accepted without a Job UID.
func ValidateSpreadingAnnotation(fldPath *field.Path, value string) field.ErrorList {
	var allErrs field.ErrorList

	// Default selectors are resolved when building the scheduling spec.
	spec, err := ParseSpreadingAnnotation(value, "")
	if err != nil {
		switch {
		case errors.Is(err, ErrTopologySpreadingRuleCount):
			allErrs = append(allErrs, field.Invalid(fldPath, value, err.Error()))
		case errors.Is(err, ErrTopologySpreadingSelectorInvalid):
			allErrs = append(allErrs, field.Invalid(fldPath.Child("workloadLabelSelectors"), value, err.Error()))
		default:
			allErrs = append(allErrs, field.Invalid(fldPath, value, fmt.Sprintf("must be a valid JSON object: %v", err)))
		}
		return allErrs
	}

	allErrs = append(allErrs, validateSpreadingSelectors(fldPath.Child("workloadLabelSelectors"), spec.WorkloadLabelSelectors)...)

	rulesPath := fldPath.Child("rules")
	seen := make(map[string]int, len(spec.Rules))
	for i, rule := range spec.Rules {
		entryPath := rulesPath.Index(i)

		allErrs = append(allErrs, metavalidation.ValidateLabelName(rule.TopologyKey, entryPath.Child("topologyKey"))...)

		if !isValidShare(rule.MaxShareAllowingPlacement) {
			allErrs = append(allErrs, field.Invalid(entryPath.Child("maxShareAllowingPlacement"),
				rule.MaxShareAllowingPlacement.String(), "must be greater than 0 and less than 1"))
		}

		if rule.EnforcementMode != TopologySpreadingEnforcementModeRequired && rule.EnforcementMode != TopologySpreadingEnforcementModePreferred {
			allErrs = append(allErrs, field.NotSupported(entryPath.Child("enforcementMode"), rule.EnforcementMode,
				[]TopologySpreadingEnforcementMode{TopologySpreadingEnforcementModeRequired, TopologySpreadingEnforcementModePreferred}))
		}

		if prevIdx, ok := seen[rule.TopologyKey]; ok {
			allErrs = append(allErrs, field.Duplicate(entryPath.Child("topologyKey"), fmt.Sprintf("%s (also at index %d)", rule.TopologyKey, prevIdx)))
		} else {
			seen[rule.TopologyKey] = i
		}
	}

	return allErrs
}

func isValidShare(q resource.Quantity) bool {
	return q.CmpInt64(0) > 0 && q.CmpInt64(1) < 0
}

func validateSpreadingSelectors(fldPath *field.Path, selectors []metav1.LabelSelectorRequirement) field.ErrorList {
	var allErrs field.ErrorList

	if len(selectors) > maxSpreadingSelectorRequirements {
		allErrs = append(allErrs, field.TooMany(fldPath, len(selectors), maxSpreadingSelectorRequirements))
	}

	for i, requirement := range selectors {
		entryPath := fldPath.Index(i)

		allErrs = append(allErrs, metavalidation.ValidateLabelName(requirement.Key, entryPath.Child("key"))...)

		if requirement.Operator != metav1.LabelSelectorOpIn {
			allErrs = append(allErrs, field.NotSupported(entryPath.Child("operator"), requirement.Operator,
				[]metav1.LabelSelectorOperator{metav1.LabelSelectorOpIn}))
			continue
		}
		if len(requirement.Values) == 0 {
			allErrs = append(allErrs, field.Required(entryPath.Child("values"),
				fmt.Sprintf("must contain at least one value for the %q operator", metav1.LabelSelectorOpIn)))
		}
	}

	return allErrs
}
