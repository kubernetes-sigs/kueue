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
	"strconv"
	"strings"

	kfmpi "github.com/kubeflow/mpi-operator/pkg/apis/kubeflow/v2beta1"
	kftraining "github.com/kubeflow/training-operator/pkg/apis/kubeflow.org/v1"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apivalidation "k8s.io/apimachinery/pkg/api/validation"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"
	jobset "sigs.k8s.io/jobset/api/jobset/v1alpha2"

	"sigs.k8s.io/kueue/pkg/controller/constants"
)

var (
	annotationsPath               = field.NewPath("metadata", "annotations")
	labelsPath                    = field.NewPath("metadata", "labels")
	queueNameLabelPath            = labelsPath.Key(constants.QueueLabel)
	maxExecTimeLabelPath          = labelsPath.Key(constants.MaxExecTimeSecondsLabel)
	workloadPriorityClassNamePath = labelsPath.Key(constants.WorkloadPriorityClassLabel)
	supportedPrebuiltWlJobGVKs    = sets.New(
		batchv1.SchemeGroupVersion.WithKind("Job").String(),
		jobset.SchemeGroupVersion.WithKind("JobSet").String(),
		kftraining.SchemeGroupVersion.WithKind(kftraining.TFJobKind).String(),
		kftraining.SchemeGroupVersion.WithKind(kftraining.PaddleJobKind).String(),
		kftraining.SchemeGroupVersion.WithKind(kftraining.PyTorchJobKind).String(),
		kftraining.SchemeGroupVersion.WithKind(kftraining.XGBoostJobKind).String(),
		kfmpi.SchemeGroupVersion.WithKind(kfmpi.Kind).String(),
		rayv1.SchemeGroupVersion.WithKind("RayJob").String(),
		corev1.SchemeGroupVersion.WithKind("Pod").String())
)

// ValidateJobOnCreate encapsulates all GenericJob validations that must be performed on a Create operation
func ValidateJobOnCreate(job GenericJob) field.ErrorList {
	allErrs := validateCreateForQueueName(job)
	allErrs = append(allErrs, validateCreateForMaxExecTime(job)...)
	return allErrs
}

// ValidateJobOnUpdate encapsulates all GenericJob validations that must be performed on a Update operation
func ValidateJobOnUpdate(oldJob, newJob GenericJob) field.ErrorList {
	allErrs := validateUpdateForQueueName(oldJob, newJob)
	allErrs = append(allErrs, validateUpdateForWorkloadPriorityClassName(oldJob, newJob)...)
	allErrs = append(allErrs, validateUpdateForMaxExecTime(oldJob, newJob)...)
	return allErrs
}

func validateCreateForQueueName(job GenericJob) field.ErrorList {
	var allErrs field.ErrorList
	allErrs = append(allErrs, ValidateQueueName(job.Object())...)
	allErrs = append(allErrs, ValidateLabelAsCRDName(job.Object(), constants.PrebuiltWorkloadLabel)...)

	// this rule should be relaxed when its confirmed that running with a prebuilt wl is fully supported by each integration
	if _, hasPrebuilt := job.Object().GetLabels()[constants.PrebuiltWorkloadLabel]; hasPrebuilt {
		gvk := job.GVK().String()
		if !supportedPrebuiltWlJobGVKs.Has(gvk) {
			allErrs = append(allErrs, field.Forbidden(labelsPath.Key(constants.PrebuiltWorkloadLabel), fmt.Sprintf("Is not supported for %q", gvk)))
		}
	}
	return allErrs
}

func ValidateAnnotationAsCRDName(obj client.Object, crdNameAnnotation string) field.ErrorList {
	var allErrs field.ErrorList
	if value, exists := obj.GetAnnotations()[crdNameAnnotation]; exists {
		if errs := validation.IsDNS1123Subdomain(value); len(errs) > 0 {
			allErrs = append(allErrs, field.Invalid(annotationsPath.Key(crdNameAnnotation), value, strings.Join(errs, ",")))
		}
	}
	return allErrs
}

func ValidateLabelAsCRDName(obj client.Object, crdNameLabel string) field.ErrorList {
	var allErrs field.ErrorList
	if value, exists := obj.GetLabels()[crdNameLabel]; exists {
		if errs := validation.IsDNS1123Subdomain(value); len(errs) > 0 {
			allErrs = append(allErrs, field.Invalid(labelsPath.Key(crdNameLabel), value, strings.Join(errs, ",")))
		}
	}
	return allErrs
}

func ValidateQueueName(obj client.Object) field.ErrorList {
	var allErrs field.ErrorList
	allErrs = append(allErrs, ValidateLabelAsCRDName(obj, constants.QueueLabel)...)
	allErrs = append(allErrs, ValidateAnnotationAsCRDName(obj, constants.QueueAnnotation)...)
	return allErrs
}

func validateUpdateForQueueName(oldJob, newJob GenericJob) field.ErrorList {
	var allErrs field.ErrorList
	if !newJob.IsSuspended() {
		allErrs = append(allErrs, apivalidation.ValidateImmutableField(QueueName(newJob), QueueName(oldJob), queueNameLabelPath)...)
	}

	oldWlName, _ := PrebuiltWorkloadFor(oldJob)
	newWlName, _ := PrebuiltWorkloadFor(newJob)
	allErrs = append(allErrs, apivalidation.ValidateImmutableField(newWlName, oldWlName, labelsPath.Key(constants.PrebuiltWorkloadLabel))...)
	return allErrs
}

func validateUpdateForWorkloadPriorityClassName(oldJob, newJob GenericJob) field.ErrorList {
	allErrs := apivalidation.ValidateImmutableField(workloadPriorityClassName(newJob), workloadPriorityClassName(oldJob), workloadPriorityClassNamePath)
	return allErrs
}

func validateCreateForMaxExecTime(job GenericJob) field.ErrorList {
	if strVal, found := job.Object().GetLabels()[constants.MaxExecTimeSecondsLabel]; found {
		v, err := strconv.Atoi(strVal)
		if err != nil {
			return field.ErrorList{field.Invalid(maxExecTimeLabelPath, strVal, err.Error())}
		}

		if v <= 0 {
			return field.ErrorList{field.Invalid(maxExecTimeLabelPath, v, "should be greater than 0")}
		}
	}
	return nil
}

func validateUpdateForMaxExecTime(oldJob, newJob GenericJob) field.ErrorList {
	if !newJob.IsSuspended() || !oldJob.IsSuspended() {
		return apivalidation.ValidateImmutableField(newJob.Object().GetLabels()[constants.MaxExecTimeSecondsLabel], oldJob.Object().GetLabels()[constants.MaxExecTimeSecondsLabel], maxExecTimeLabelPath)
	}
	return nil
}

// ValidateImmutablePodSpec function is used for serving workloads to ensure no changes are allowed
// to the PodSpec except for the image field in containers.
func ValidateImmutablePodSpec(newPodSpec *corev1.PodSpec, oldPodSpec *corev1.PodSpec, fieldPath *field.Path) field.ErrorList {
	// handle updateable fields by munging those fields prior to deep equal comparison.
	mungedPodSpec := newPodSpec.DeepCopy()

	// munge spec.containers[*].image
	newContainers := make([]corev1.Container, 0, len(newPodSpec.Containers))
	for i, container := range mungedPodSpec.Containers {
		container.Image = oldPodSpec.Containers[i].Image
		newContainers = append(newContainers, container)
	}
	mungedPodSpec.Containers = newContainers

	// munge spec.initContainers[*].image
	newInitContainers := make([]corev1.Container, 0, len(newPodSpec.InitContainers))
	for ix, container := range mungedPodSpec.InitContainers {
		container.Image = oldPodSpec.InitContainers[ix].Image
		newInitContainers = append(newInitContainers, container)
	}
	mungedPodSpec.InitContainers = newInitContainers

	return apivalidation.ValidateImmutableField(mungedPodSpec, oldPodSpec, fieldPath)
}

func IsManagedByKueue(obj client.Object) bool {
	objectOwner := metav1.GetControllerOf(obj)
	if objectOwner != nil && IsOwnerManagedByKueue(objectOwner) {
		return false
	} else if QueueNameForObject(obj) != "" {
		return true
	}
	return false
}
