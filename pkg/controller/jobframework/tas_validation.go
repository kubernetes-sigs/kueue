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
	"fmt"
	"strconv"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	metavalidation "k8s.io/apimachinery/pkg/apis/meta/v1/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/utils/ptr"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/util/orderedgroups"
)

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

		if sliceSizeFound {
			allErrs = append(allErrs, field.Forbidden(annotationsPath.Key(kueue.PodSetGroupName), fmt.Sprintf("may not be set when '%s' is specified", kueue.PodSetSliceSizeAnnotation)))
		}

		if sliceRequiredFound {
			allErrs = append(allErrs, field.Forbidden(annotationsPath.Key(kueue.PodSetGroupName), fmt.Sprintf("may not be set when '%s' is specified", kueue.PodSetSliceRequiredTopologyAnnotation)))
		}

		if !preferredFound && !requiredFound {
			allErrs = append(allErrs, field.Forbidden(annotationsPath.Key(kueue.PodSetGroupName), fmt.Sprintf("may not be set when neither '%s' nor '%s' is specified", kueue.PodSetPreferredTopologyAnnotation, kueue.PodSetRequiredTopologyAnnotation)))
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
		allErrs = append(allErrs, field.Forbidden(annotationsPath.Key(kueue.PodSetSliceSizeAnnotation), fmt.Sprintf("may not be set when '%s' is not specified", kueue.PodSetSliceRequiredTopologyAnnotation)))
	}

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
	sliceSizeValue, sliceSizeFound := replicaMetadata.Annotations[kueue.PodSetSliceSizeAnnotation]
	if !sliceSizeFound || podSet == nil {
		return nil
	}

	annotationsPath := replicaPath.Child("annotations")

	val, err := strconv.ParseInt(sliceSizeValue, 10, 32)
	if err != nil {
		return field.ErrorList{
			field.Invalid(
				annotationsPath.Key(kueue.PodSetSliceSizeAnnotation), sliceSizeValue, "must be a numeric value",
			),
		}
	}

	if int32(val) > podSet.Count {
		return field.ErrorList{
			field.Invalid(
				annotationsPath.Key(kueue.PodSetSliceSizeAnnotation), sliceSizeValue,
				fmt.Sprintf("must not be greater than pod set count %d", podSet.Count),
			),
		}
	}

	return nil
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
