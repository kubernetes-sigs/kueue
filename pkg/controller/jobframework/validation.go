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
	"strings"

	apivalidation "k8s.io/apimachinery/pkg/api/validation"
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
)

func ValidateCreateForQueueName(job GenericJob) field.ErrorList {
	var allErrs field.ErrorList
	allErrs = append(allErrs, ValidateLabelAsCRDName(job, constants.QueueLabel)...)
	allErrs = append(allErrs, ValidateAnnotationAsCRDName(job, constants.QueueAnnotation)...)
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
