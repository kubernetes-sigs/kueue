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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	apimachineryvalidation "k8s.io/apimachinery/pkg/api/validation"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
)

func validateResourceName(name corev1.ResourceName, fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList
	for _, msg := range validation.IsQualifiedName(string(name)) {
		allErrs = append(allErrs, field.Invalid(fldPath, name, msg))
	}
	return allErrs
}

type validationConfig struct {
	hasParent                        bool
	enforceNominalGreaterThanLending bool
}

// validateFairSharing validates the FairSharing config for both ClusterQueues and Cohorts.
func validateFairSharing(fs *kueue.FairSharing, fldPath *field.Path) field.ErrorList {
	if fs == nil || fs.Weight == nil {
		return nil
	}
	var allErrs field.ErrorList

	// validate non-negative
	if fs.Weight.Cmp(resource.Quantity{}) < 0 {
		allErrs = append(allErrs, field.Invalid(fldPath, fs.Weight.String(), apimachineryvalidation.IsNegativeErrorMsg))
	}

	// validate that not a value which will collapse:
	// 0 < value <= 10e-9
	if fs.Weight.Cmp(resource.Quantity{}) > 0 && fs.Weight.Cmp(resource.MustParse("1n")) <= 0 {
		allErrs = append(allErrs, field.Invalid(fldPath, fs.Weight.String(), "When not 0, weight must be > 10e-9"))
	}
	return allErrs
}
