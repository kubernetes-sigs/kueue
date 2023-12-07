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

package jobframework

import (
	"fmt"
	"strings"

	apivalidation "k8s.io/apimachinery/pkg/api/validation"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"

	"sigs.k8s.io/kueue/pkg/controller/constants"
)

var (
	annotationsPath               = field.NewPath("metadata", "annotations")
	labelsPath                    = field.NewPath("metadata", "labels")
	parentWorkloadKeyPath         = annotationsPath.Key(constants.ParentWorkloadAnnotation)
	queueNameLabelPath            = labelsPath.Key(constants.QueueLabel)
	workloadPriorityClassNamePath = labelsPath.Key(constants.WorkloadPriorityClassLabel)
	supportedPrebuiltWlJobGVKs    = sets.New("batch/v1, Kind=Job")
)

func ValidateCreateForQueueName(job GenericJob) field.ErrorList {
	var allErrs field.ErrorList
	allErrs = append(allErrs, ValidateLabelAsCRDName(job, constants.QueueLabel)...)
	allErrs = append(allErrs, ValidateLabelAsCRDName(job, constants.PrebuiltWorkloadLabel)...)
	allErrs = append(allErrs, validateOnlyOneLablel(job, constants.QueueLabel, constants.PrebuiltWorkloadLabel)...)
	allErrs = append(allErrs, ValidateAnnotationAsCRDName(job, constants.QueueAnnotation)...)

	// this rule should be relaxed when its confirmed that running wit a prebuilt wl is fully supported by each integration
	if _, hasPrebuilt := job.Object().GetLabels()[constants.PrebuiltWorkloadLabel]; hasPrebuilt {
		gvk := job.GVK().String()
		if !supportedPrebuiltWlJobGVKs.Has(gvk) {
			allErrs = append(allErrs, field.Forbidden(labelsPath.Key(constants.PrebuiltWorkloadLabel), fmt.Sprintf("Is not supported for %q", gvk)))
		}
	}
	return allErrs
}

func validateOnlyOneLablel(job GenericJob, keys ...string) field.ErrorList {
	var allErrs field.ErrorList
	labels := job.Object().GetLabels()
	keysFound := make([]string, 0, len(labels))
	for _, lk := range keys {
		if _, found := labels[lk]; found {
			keysFound = append(keysFound, lk)
		}
	}
	if len(keysFound) > 1 {
		allErrs = append(allErrs, field.Invalid(labelsPath, keysFound, "Only one label allowed"))
	}
	return allErrs
}

func ValidateAnnotationAsCRDName(job GenericJob, crdNameAnnotation string) field.ErrorList {
	var allErrs field.ErrorList
	if value, exists := job.Object().GetAnnotations()[crdNameAnnotation]; exists {
		if errs := validation.IsDNS1123Subdomain(value); len(errs) > 0 {
			allErrs = append(allErrs, field.Invalid(annotationsPath.Key(crdNameAnnotation), value, strings.Join(errs, ",")))
		}
	}
	return allErrs
}

func ValidateLabelAsCRDName(job GenericJob, crdNameLabel string) field.ErrorList {
	var allErrs field.ErrorList
	if value, exists := job.Object().GetLabels()[crdNameLabel]; exists {
		if errs := validation.IsDNS1123Subdomain(value); len(errs) > 0 {
			allErrs = append(allErrs, field.Invalid(labelsPath.Key(crdNameLabel), value, strings.Join(errs, ",")))
		}
	}
	return allErrs
}

func ValidateCreateForParentWorkload(job GenericJob) field.ErrorList {
	var allErrs field.ErrorList
	if _, exists := job.Object().GetAnnotations()[constants.ParentWorkloadAnnotation]; exists {
		if job.Object().GetOwnerReferences() == nil {
			allErrs = append(allErrs, field.Forbidden(parentWorkloadKeyPath, "must not add a parent workload annotation to job without OwnerReference"))
		}
	}
	return allErrs
}

func ValidateUpdateForQueueName(oldJob, newJob GenericJob) field.ErrorList {
	var allErrs field.ErrorList
	if !newJob.IsSuspended() {
		allErrs = append(allErrs, apivalidation.ValidateImmutableField(QueueName(oldJob), QueueName(newJob), queueNameLabelPath)...)
	}

	oldHasPrebuiltWl, oldWlName := prebuiltWorkload(oldJob)
	newHasPrebuiltWl, newWlName := prebuiltWorkload(newJob)
	if oldHasPrebuiltWl || newHasPrebuiltWl {
		allErrs = append(allErrs, apivalidation.ValidateImmutableField(oldWlName, newWlName, labelsPath.Key(constants.PrebuiltWorkloadLabel))...)
	}
	return allErrs
}

func ValidateUpdateForParentWorkload(oldJob, newJob GenericJob) field.ErrorList {
	allErrs := apivalidation.ValidateImmutableField(ParentWorkloadName(newJob), ParentWorkloadName(oldJob), parentWorkloadKeyPath)
	return allErrs
}

func ValidateUpdateForWorkloadPriorityClassName(oldJob, newJob GenericJob) field.ErrorList {
	allErrs := apivalidation.ValidateImmutableField(workloadPriorityClassName(oldJob), workloadPriorityClassName(newJob), workloadPriorityClassNamePath)
	return allErrs
}
