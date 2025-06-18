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

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
)

func ValidateTASPodSetRequest(replicaPath *field.Path, replicaMetadata *metav1.ObjectMeta) field.ErrorList {
	var allErrs field.ErrorList
	requiredValue, requiredFound := replicaMetadata.Annotations[kueuealpha.PodSetRequiredTopologyAnnotation]
	preferredValue, preferredFound := replicaMetadata.Annotations[kueuealpha.PodSetPreferredTopologyAnnotation]
	_, unconstrainedFound := replicaMetadata.Annotations[kueuealpha.PodSetUnconstrainedTopologyAnnotation]
	sliceRequiredValue, sliceRequiredFound := replicaMetadata.Annotations[kueuealpha.PodSetSliceRequiredTopologyAnnotation]
	sliceSizeValue, sliceSizeFound := replicaMetadata.Annotations[kueuealpha.PodSetSliceSizeAnnotation]

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
				kueuealpha.PodSetRequiredTopologyAnnotation,
				kueuealpha.PodSetPreferredTopologyAnnotation,
				kueuealpha.PodSetUnconstrainedTopologyAnnotation),
		))
	}

	// validate labels
	if requiredFound {
		allErrs = append(allErrs, metavalidation.ValidateLabelName(requiredValue, annotationsPath.Key(kueuealpha.PodSetRequiredTopologyAnnotation))...)
	}
	if preferredFound {
		allErrs = append(allErrs, metavalidation.ValidateLabelName(preferredValue, annotationsPath.Key(kueuealpha.PodSetPreferredTopologyAnnotation))...)
	}
	if sliceRequiredFound {
		allErrs = append(allErrs, metavalidation.ValidateLabelName(sliceRequiredValue, annotationsPath.Key(kueuealpha.PodSetSliceRequiredTopologyAnnotation))...)
	}

	// validate slice size annotation
	if sliceSizeFound {
		val, err := strconv.ParseInt(sliceSizeValue, 10, 32)
		if err != nil {
			allErrs = append(allErrs, field.Invalid(annotationsPath.Key(kueuealpha.PodSetSliceSizeAnnotation), sliceSizeValue, "must be a numeric value"))
		} else if int32(val) < 1 {
			allErrs = append(allErrs, field.Invalid(annotationsPath.Key(kueuealpha.PodSetSliceSizeAnnotation), sliceSizeValue, "must be greater than or equal to 1"))
		}
	}

	// validate slice annotations
	if sliceRequiredFound {
		if !sliceSizeFound {
			allErrs = append(allErrs, field.Required(annotationsPath.Key(kueuealpha.PodSetSliceSizeAnnotation), "slice size is required if slice topology is requested"))
		}
	}

	if !sliceRequiredFound && sliceSizeFound {
		allErrs = append(allErrs, field.Forbidden(annotationsPath.Key(kueuealpha.PodSetSliceSizeAnnotation), fmt.Sprintf("cannot be set when '%s' is not present", kueuealpha.PodSetSliceRequiredTopologyAnnotation)))
	}

	return allErrs
}
