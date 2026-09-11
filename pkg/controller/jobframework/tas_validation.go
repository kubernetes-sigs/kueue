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

package jobframework

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	metavalidation "k8s.io/apimachinery/pkg/apis/meta/v1/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/utils/ptr"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/orderedgroups"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
)

// maxSpreadingSelectorRequirements is the number of workloadLabelSelectors
// requirements supported in the alpha milestone of TASTopologySpreading.
const maxSpreadingSelectorRequirements = 1

func ValidateTASPodSetRequest(replicaPath *field.Path, replicaMetadata *metav1.ObjectMeta) field.ErrorList {
	var allErrs field.ErrorList
	requiredValue, requiredFound := replicaMetadata.Annotations[kueue.PodSetRequiredTopologyAnnotation]
	preferredValue, preferredFound := replicaMetadata.Annotations[kueue.PodSetPreferredTopologyAnnotation]
	_, unconstrainedFound := replicaMetadata.Annotations[kueue.PodSetUnconstrainedTopologyAnnotation]
	sliceRequiredValue, sliceRequiredFound := replicaMetadata.Annotations[kueue.PodSetSliceRequiredTopologyAnnotation]
	_, sliceSizeFound := replicaMetadata.Annotations[kueue.PodSetSliceSizeAnnotation]

	// validate no more than 1 annotation
	asInt := func(b bool) int {
		if b {
			return 1
		}
		return 0
	}
	annotationFoundCount := asInt(requiredFound) + asInt(preferredFound) + asInt(unconstrainedFound)
	annotationsPath := replicaPath.Child("annotations")
	if annotationFoundCount > 1 {
		allErrs = append(allErrs, field.Invalid(annotationsPath, field.OmitValueType{},
			fmt.Sprintf("must not contain more than one topology annotation: [%q, %q, %q]",
				kueue.PodSetRequiredTopologyAnnotation,
				kueue.PodSetPreferredTopologyAnnotation,
				kueue.PodSetUnconstrainedTopologyAnnotation),
		))
	}

	// validate labels
	if requiredFound {
		allErrs = append(allErrs, metavalidation.ValidateLabelName(requiredValue, annotationsPath.Key(kueue.PodSetRequiredTopologyAnnotation))...)
	}
	if preferredFound {
		allErrs = append(allErrs, metavalidation.ValidateLabelName(preferredValue, annotationsPath.Key(kueue.PodSetPreferredTopologyAnnotation))...)
	}
	if sliceRequiredFound {
		allErrs = append(allErrs, metavalidation.ValidateLabelName(sliceRequiredValue, annotationsPath.Key(kueue.PodSetSliceRequiredTopologyAnnotation))...)
	}

	// validate PodSetGroupName annotation
	podSetGroupNameValue, podSetGroupNameFound := replicaMetadata.Annotations[kueue.PodSetGroupName]
	if podSetGroupNameFound {
		allErrs = append(allErrs, validatePodSetGroupNameAnnotation(podSetGroupNameValue, annotationsPath.Key(kueue.PodSetGroupName))...)

		if !features.Enabled(features.TASGroupedPodSetSlicing) {
			if sliceSizeFound {
				allErrs = append(allErrs, field.Forbidden(annotationsPath.Key(kueue.PodSetGroupName), fmt.Sprintf("may not be set when '%s' is specified", kueue.PodSetSliceSizeAnnotation)))
			}

			if sliceRequiredFound {
				allErrs = append(
					allErrs,
					field.Forbidden(annotationsPath.Key(kueue.PodSetGroupName), fmt.Sprintf("may not be set when '%s' is specified", kueue.PodSetSliceRequiredTopologyAnnotation)),
				)
			}
		}

		if !preferredFound && !requiredFound {
			allErrs = append(
				allErrs,
				field.Forbidden(
					annotationsPath.Key(kueue.PodSetGroupName),
					fmt.Sprintf("may not be set when neither '%s' nor '%s' is specified", kueue.PodSetPreferredTopologyAnnotation, kueue.PodSetRequiredTopologyAnnotation),
				),
			)
		}
	}

	unconstrainedErrs := validateTASUnconstrained(annotationsPath, replicaMetadata)
	allErrs = append(allErrs, unconstrainedErrs...)

	sliceSizeAnnotationErr := validateSliceSizeAnnotation(annotationsPath, replicaMetadata)
	allErrs = append(allErrs, sliceSizeAnnotationErr...)

	// validate slice annotations
	if sliceRequiredFound && !sliceSizeFound {
		allErrs = append(allErrs, field.Required(annotationsPath.Key(kueue.PodSetSliceSizeAnnotation), fmt.Sprintf("must be set when '%s' is specified", kueue.PodSetSliceRequiredTopologyAnnotation)))
	}
	if !sliceRequiredFound && sliceSizeFound {
		allErrs = append(
			allErrs,
			field.Forbidden(annotationsPath.Key(kueue.PodSetSliceSizeAnnotation), fmt.Sprintf("may not be set when '%s' is not specified", kueue.PodSetSliceRequiredTopologyAnnotation)),
		)
	}

	// validate multi-level constraints annotation
	allErrs = append(allErrs, validateSliceRequiredTopologyConstraintsAnnotation(annotationsPath, replicaMetadata, sliceRequiredFound, sliceSizeFound, podSetGroupNameFound)...)

	// validate topology spreading annotation
	allErrs = append(allErrs, validateTopologySpreadingAnnotation(annotationsPath, replicaMetadata, requiredFound)...)

	return allErrs
}

func validateTASUnconstrained(annotationsPath *field.Path, replicaMetadata *metav1.ObjectMeta) field.ErrorList {
	if val, ok := replicaMetadata.Annotations[kueue.PodSetUnconstrainedTopologyAnnotation]; ok {
		if _, err := strconv.ParseBool(val); err != nil {
			return field.ErrorList{
				field.Invalid(
					annotationsPath.Key(kueue.PodSetUnconstrainedTopologyAnnotation), val, "must be a boolean value",
				),
			}
		}
	}
	return nil
}

func validateSliceSizeAnnotation(annotationsPath *field.Path, replicaMetadata *metav1.ObjectMeta) field.ErrorList {
	sliceSizeValue, sliceSizeFound := replicaMetadata.Annotations[kueue.PodSetSliceSizeAnnotation]
	if !sliceSizeFound {
		return nil
	}

	val, err := strconv.ParseInt(sliceSizeValue, 10, 32)
	if err != nil {
		return field.ErrorList{
			field.Invalid(
				annotationsPath.Key(kueue.PodSetSliceSizeAnnotation), sliceSizeValue, "must be a numeric value",
			),
		}
	}

	if int32(val) < 1 {
		return field.ErrorList{
			field.Invalid(
				annotationsPath.Key(kueue.PodSetSliceSizeAnnotation), sliceSizeValue,
				"must be greater than or equal to 1",
			),
		}
	}

	return nil
}

func validatePodSetGroupNameAnnotation(groupName string, annotationPath *field.Path) field.ErrorList {
	if _, err := strconv.ParseUint(groupName, 10, 64); err == nil {
		return field.ErrorList{
			field.Invalid(
				annotationPath, groupName, "must not be a number",
			),
		}
	}

	return nil
}

func ValidateSliceSizeAnnotationUpperBound(replicaPath *field.Path, replicaMetadata *metav1.ObjectMeta, podSet *kueue.PodSet) field.ErrorList {
	if podSet == nil {
		return nil
	}

	var allErrs field.ErrorList
	annotationsPath := replicaPath.Child("annotations")

	if sliceSizeValue, sliceSizeFound := replicaMetadata.Annotations[kueue.PodSetSliceSizeAnnotation]; sliceSizeFound {
		val, err := strconv.ParseInt(sliceSizeValue, 10, 32)
		if err != nil {
			return field.ErrorList{
				field.Invalid(
					annotationsPath.Key(kueue.PodSetSliceSizeAnnotation), sliceSizeValue, "must be a numeric value",
				),
			}
		}

		if int32(val) > podSet.Count {
			allErrs = append(allErrs, field.Invalid(
				annotationsPath.Key(kueue.PodSetSliceSizeAnnotation), sliceSizeValue,
				fmt.Sprintf("must not be greater than pod set count %d", podSet.Count),
			))
		}
	}

	// when logic reaches here, PodSetSliceRequiredTopologyConstraintsAnnotation is already guaranteed to be mutually exclusive
	// with PodSetSliceSizeAnnotation, so no need to checkTASMultiLayerTopology feature gate
	if constraintsJSON, constraintsFound := replicaMetadata.Annotations[kueue.PodSetSliceRequiredTopologyConstraintsAnnotation]; constraintsFound {
		var constraints []kueue.PodsetSliceRequiredTopologyConstraint
		if err := json.Unmarshal([]byte(constraintsJSON), &constraints); err == nil && len(constraints) > 0 {
			if constraints[0].Size > podSet.Count {
				allErrs = append(allErrs, field.Invalid(
					annotationsPath.Key(kueue.PodSetSliceRequiredTopologyConstraintsAnnotation),
					constraints[0].Size,
					fmt.Sprintf("must not be greater than pod set count %d", podSet.Count),
				))
			}
		}
	}

	return allErrs
}

func ValidatePodSetGroupingTopology(podSets []kueue.PodSet, podSetAnnotationsByName map[kueue.PodSetReference]*field.Path) field.ErrorList {
	podSetGroups := orderedgroups.NewOrderedGroups[string, kueue.PodSet]()
	for _, podSet := range podSets {
		if podSet.TopologyRequest == nil || podSet.TopologyRequest.PodSetGroupName == nil {
			continue
		}
		groupName := *podSet.TopologyRequest.PodSetGroupName
		podSetGroups.Insert(groupName, podSet)
	}

	var allErrs field.ErrorList

	for groupName, podSets := range podSetGroups.InOrder {
		if groupSize := len(podSets); groupSize != 2 {
			for _, podSet := range podSets {
				allErrs = append(
					allErrs,
					field.Invalid(
						podSetAnnotationsByName[podSet.Name].Key(kueue.PodSetGroupName),
						groupName,
						fmt.Sprintf("can only define groups of exactly 2 pod sets, got: %d pod set(s)", groupSize),
					),
				)
			}
			continue
		}

		podSet1, podSet2 := podSets[0], podSets[1]
		annotationsPath1 := podSetAnnotationsByName[podSet1.Name]
		annotationsPath2 := podSetAnnotationsByName[podSet2.Name]

		// Validate group size
		if podSet1.Count != 1 && podSet2.Count != 1 {
			sizeErrorMessage := fmt.Sprintf(
				"can only define groups where at least one pod set has only 1 replica, got: %d replica(s) and %d replica(s) in the group",
				podSet1.Count,
				podSet2.Count,
			)
			allErrs = append(allErrs,
				field.Invalid(
					annotationsPath1.Key(kueue.PodSetGroupName),
					groupName,
					sizeErrorMessage,
				),
				field.Invalid(
					annotationsPath2.Key(kueue.PodSetGroupName),
					groupName,
					sizeErrorMessage,
				),
			)
		}

		if features.Enabled(features.TASTopologySpreading) &&
			podSet1.Template.Annotations[kueue.PodSetTopologySpreadingAnnotation] != podSet2.Template.Annotations[kueue.PodSetTopologySpreadingAnnotation] {
			spreadingErrorMessage := fmt.Sprintf(
				"must specify the same '%s' annotation as '%s' in group '%s', or neither pod set may specify it",
				kueue.PodSetTopologySpreadingAnnotation,
				"%s",
				groupName,
			)
			allErrs = append(allErrs,
				field.Invalid(annotationsPath1, field.OmitValueType{}, fmt.Sprintf(spreadingErrorMessage, annotationsPath2)),
				field.Invalid(annotationsPath2, field.OmitValueType{}, fmt.Sprintf(spreadingErrorMessage, annotationsPath1)),
			)
		}

		if !topologyRequestsValid(podSet1.TopologyRequest, podSet2.TopologyRequest) {
			errorMessageTemplate := fmt.Sprintf(
				"must specify '%s' or '%s' topology consistent with '%%s' in group '%s'",
				kueue.PodSetRequiredTopologyAnnotation,
				kueue.PodSetPreferredTopologyAnnotation,
				groupName,
			)
			allErrs = append(
				allErrs,
				field.Invalid(
					annotationsPath1,
					field.OmitValueType{},
					fmt.Sprintf(errorMessageTemplate, annotationsPath2),
				),
				field.Invalid(
					annotationsPath2,
					field.OmitValueType{},
					fmt.Sprintf(errorMessageTemplate, annotationsPath1),
				),
			)
		}
	}

	return allErrs
}

func topologyRequestsValid(r1, r2 *kueue.PodSetTopologyRequest) bool {
	// Check that the requests have exactly one of `Required` and `Preferred`.
	if r1.Required == nil && r1.Preferred == nil {
		return false
	}
	if r2.Required == nil && r2.Preferred == nil {
		return false
	}
	// Check that the non-nil pair has the same value.
	return ptr.Equal(r1.Required, r2.Required) && ptr.Equal(r1.Preferred, r2.Preferred)
}

// validateSliceRequiredTopologyConstraintsAnnotation validates the
// multi-layer topology constraints annotation syntactically.
// NOTE: Coarsest-to-finest ordering is NOT validated here because the
// Topology CR's level hierarchy is unavailable at admission time (the
// ResourceFlavor is assigned by the scheduler). Ordering validation
// happens at scheduling time in TASFlavorSnapshot.findTopologyAssignment.
func validateSliceRequiredTopologyConstraintsAnnotation(
	annotationsPath *field.Path,
	replicaMetadata *metav1.ObjectMeta,
	sliceRequiredFound bool,
	sliceSizeFound bool,
	podSetGroupNameFound bool,
) field.ErrorList {
	var allErrs field.ErrorList

	constraintsJSON, constraintsFound := replicaMetadata.Annotations[kueue.PodSetSliceRequiredTopologyConstraintsAnnotation]
	if !constraintsFound {
		return nil
	}

	fldPath := annotationsPath.Key(kueue.PodSetSliceRequiredTopologyConstraintsAnnotation)

	if !features.Enabled(features.TASMultiLayerTopology) {
		allErrs = append(allErrs, field.Forbidden(fldPath,
			fmt.Sprintf("the %s feature gate must be enabled to use this annotation", features.TASMultiLayerTopology)))
		return allErrs
	}

	// Mutual exclusivity with two-level fields.
	if sliceRequiredFound {
		allErrs = append(allErrs, field.Forbidden(fldPath,
			fmt.Sprintf("may not be set when '%s' is specified", kueue.PodSetSliceRequiredTopologyAnnotation)))
	}
	if sliceSizeFound {
		allErrs = append(allErrs, field.Forbidden(fldPath,
			fmt.Sprintf("may not be set when '%s' is specified", kueue.PodSetSliceSizeAnnotation)))
	}

	// Incompatible with podset-group-name when TASGroupedPodSetSlicing is disabled.
	if podSetGroupNameFound && !features.Enabled(features.TASGroupedPodSetSlicing) {
		allErrs = append(allErrs, field.Forbidden(annotationsPath.Key(kueue.PodSetGroupName),
			fmt.Sprintf("may not be set when '%s' is specified", kueue.PodSetSliceRequiredTopologyConstraintsAnnotation)))
	}

	// Parse JSON.
	var constraints []kueue.PodsetSliceRequiredTopologyConstraint
	if err := json.Unmarshal([]byte(constraintsJSON), &constraints); err != nil {
		allErrs = append(allErrs, field.Invalid(fldPath, constraintsJSON, fmt.Sprintf("must be a valid JSON array: %v", err)))
		return allErrs
	}

	if len(constraints) == 0 || len(constraints) > 3 {
		allErrs = append(allErrs, field.Invalid(fldPath, constraintsJSON, errTopologyConstraintsLayerCount.Error()))
		return allErrs
	}

	// Validate each entry.
	for i, c := range constraints {
		entryPath := fldPath.Index(i)
		allErrs = append(allErrs, metavalidation.ValidateLabelName(c.Topology, entryPath.Child("topology"))...)
		if c.Size < 1 {
			allErrs = append(allErrs, field.Invalid(entryPath.Child("size"), c.Size, "must be greater than or equal to 1"))
		}
	}

	// Validate uniqueness of topology labels.
	seen := make(map[string]int, len(constraints))
	for i, c := range constraints {
		if prevIdx, ok := seen[c.Topology]; ok {
			allErrs = append(allErrs, field.Duplicate(fldPath.Index(i).Child("topology"),
				fmt.Sprintf("%s (also at index %d)", c.Topology, prevIdx)))
		} else {
			seen[c.Topology] = i
		}
	}

	// Validate divisibility: each layer's size must evenly divide the layer above it.
	for i := range len(constraints) - 1 {
		if constraints[i+1].Size > 0 && constraints[i].Size%constraints[i+1].Size != 0 {
			allErrs = append(allErrs, field.Invalid(fldPath.Index(i+1).Child("size"),
				constraints[i+1].Size,
				fmt.Sprintf("must evenly divide the parent layer size %d", constraints[i].Size)))
		}
	}

	return allErrs
}

// validateTopologySpreadingAnnotation validates the topology-spreading
// annotation syntactically. It cannot validate anything that depends on the
// ResourceFlavor's Topology - the ResourceFlavor is unassigned at admission
// time - which rules out both checking that a rule's "topologyKey" is a level
// of that Topology and checking that the key is not below the level requested
// by PodSetRequiredTopologyAnnotation. Both happen at scheduling time.
func validateTopologySpreadingAnnotation(
	annotationsPath *field.Path,
	replicaMetadata *metav1.ObjectMeta,
	requiredFound bool,
) field.ErrorList {
	var allErrs field.ErrorList

	value, found := replicaMetadata.Annotations[kueue.PodSetTopologySpreadingAnnotation]
	if !found {
		return nil
	}

	// While the gate is off the annotation is inert - the scheduler ignores it
	// entirely - so it is left unvalidated rather than rejected. That lets
	// operators annotate their workloads ahead of enabling the gate, staging
	// the configuration instead of having to land it in the same change.
	if !features.Enabled(features.TASTopologySpreading) {
		return nil
	}

	fldPath := annotationsPath.Key(kueue.PodSetTopologySpreadingAnnotation)

	// Spreading counts a group as occupying one domain per rule level, which
	// only holds for required topology - preferred/unconstrained placements
	// spread a group across several domains, breaking maxShareAllowingPlacement.
	// Requiring the companion annotation also rules out a Workload that
	// carries spreading but never engages TAS at all.
	if !requiredFound {
		allErrs = append(allErrs, field.Forbidden(fldPath,
			fmt.Sprintf("may only be set together with '%s'", kueue.PodSetRequiredTopologyAnnotation)))
		return allErrs
	}

	spec, err := utiltas.ParseSpreadingAnnotation(value)
	if err != nil {
		switch {
		case errors.Is(err, utiltas.ErrTopologySpreadingRuleCount):
			allErrs = append(allErrs, field.Invalid(fldPath, value, err.Error()))
		case errors.Is(err, utiltas.ErrTopologySpreadingSelectorMissing):
			allErrs = append(allErrs, field.Required(fldPath.Child("workloadLabelSelectors"), ""))
		case errors.Is(err, utiltas.ErrTopologySpreadingSelectorInvalid):
			allErrs = append(allErrs, field.Invalid(fldPath.Child("workloadLabelSelectors"), value, err.Error()))
		default:
			allErrs = append(allErrs, field.Invalid(fldPath, value, fmt.Sprintf("must be a valid JSON object: %v", err)))
		}
		return allErrs
	}

	// A selector that does not compile at all was already rejected by the
	// parse above; what is left to check here are the requirement-shape rules
	// that compile fine but alpha does not accept.
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

		if rule.EnforcementMode != kueue.TopologySpreadingEnforcementModeRequired && rule.EnforcementMode != kueue.TopologySpreadingEnforcementModePreferred {
			allErrs = append(allErrs, field.NotSupported(entryPath.Child("enforcementMode"), rule.EnforcementMode,
				[]kueue.TopologySpreadingEnforcementMode{kueue.TopologySpreadingEnforcementModeRequired, kueue.TopologySpreadingEnforcementModePreferred}))
		}

		if prevIdx, ok := seen[rule.TopologyKey]; ok {
			allErrs = append(allErrs, field.Duplicate(entryPath.Child("topologyKey"), fmt.Sprintf("%s (also at index %d)", rule.TopologyKey, prevIdx)))
		} else {
			seen[rule.TopologyKey] = i
		}
	}

	return allErrs
}

// isValidShare reports whether q is a share strictly between 0 and 1, the
// range maxShareAllowingPlacement is defined over. Both bounds are excluded:
// 0 would make every domain ineligible and 1 would never constrain anything.
func isValidShare(q resource.Quantity) bool {
	return q.CmpInt64(0) > 0 && q.CmpInt64(1) < 0
}

// validateSpreadingSelectors validates the workloadLabelSelectors
// requirements. Alpha accepts a single "In" requirement; the wire format is
// metav1.LabelSelectorRequirement so the restriction can be lifted without an
// annotation format change.
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
