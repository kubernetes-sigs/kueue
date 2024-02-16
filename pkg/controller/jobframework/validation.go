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

	batchv1 "k8s.io/api/batch/v1"
	apivalidation "k8s.io/apimachinery/pkg/api/validation"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
	jobset "sigs.k8s.io/jobset/api/jobset/v1alpha2"

	"sigs.k8s.io/kueue/pkg/controller/constants"
)

var (
	annotationsPath               = field.NewPath("metadata", "annotations")
	labelsPath                    = field.NewPath("metadata", "labels")
	queueNameLabelPath            = labelsPath.Key(constants.QueueLabel)
	workloadPriorityClassNamePath = labelsPath.Key(constants.WorkloadPriorityClassLabel)
	supportedPrebuiltWlJobGVKs    = sets.New(batchv1.SchemeGroupVersion.WithKind("Job").String(),
		jobset.SchemeGroupVersion.WithKind("JobSet").String())
)

func ValidateCreateForQueueName(job GenericJob) field.ErrorList {
	var allErrs field.ErrorList
	allErrs = append(allErrs, ValidateLabelAsCRDName(job, constants.QueueLabel)...)
	allErrs = append(allErrs, ValidateLabelAsCRDName(job, constants.PrebuiltWorkloadLabel)...)
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

func ValidateUpdateForQueueName(oldJob, newJob GenericJob) field.ErrorList {
	var allErrs field.ErrorList
	if !newJob.IsSuspended() {
		allErrs = append(allErrs, apivalidation.ValidateImmutableField(QueueName(oldJob), QueueName(newJob), queueNameLabelPath)...)
	}

	oldWlName, _ := PrebuiltWorkloadFor(oldJob)
	newWlName, _ := PrebuiltWorkloadFor(newJob)
	allErrs = append(allErrs, apivalidation.ValidateImmutableField(oldWlName, newWlName, labelsPath.Key(constants.PrebuiltWorkloadLabel))...)
	return allErrs
}

func ValidateUpdateForWorkloadPriorityClassName(oldJob, newJob GenericJob) field.ErrorList {
	allErrs := apivalidation.ValidateImmutableField(workloadPriorityClassName(oldJob), workloadPriorityClassName(newJob), workloadPriorityClassNamePath)
	return allErrs
}
