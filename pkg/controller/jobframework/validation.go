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
	"maps"
	"slices"
	"strconv"
	"strings"

	kfmpi "github.com/kubeflow/mpi-operator/pkg/apis/kubeflow/v2beta1"
	kftrainer "github.com/kubeflow/trainer/v2/pkg/apis/trainer/v1alpha1"
	kftraining "github.com/kubeflow/training-operator/pkg/apis/kubeflow.org/v1"
	awv1beta2 "github.com/project-codeflare/appwrapper/api/v1beta2"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apivalidation "k8s.io/apimachinery/pkg/api/validation"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"
	jobset "sigs.k8s.io/jobset/api/jobset/v1alpha2"

	"sigs.k8s.io/kueue/pkg/controller/constants"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
)

var (
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
		kftrainer.SchemeGroupVersion.WithKind(kftrainer.TrainJobKind).String(),
		kfmpi.SchemeGroupVersion.WithKind(kfmpi.Kind).String(),
		rayv1.SchemeGroupVersion.WithKind("RayJob").String(),
		corev1.SchemeGroupVersion.WithKind("Pod").String(),
		rayv1.SchemeGroupVersion.WithKind("RayCluster").String(),
		awv1beta2.GroupVersion.WithKind(awv1beta2.AppWrapperKind).String(),
	)
)

// ValidateJobOnCreate encapsulates all GenericJob validations that must be performed on a Create operation
func ValidateJobOnCreate(job GenericJob) field.ErrorList {
	allErrs := ValidateQueueName(job.Object())
	allErrs = append(allErrs, validateCreateForPrebuiltWorkload(job)...)
	allErrs = append(allErrs, validateCreateForMaxExecTime(job)...)
	return allErrs
}

// ValidateJobOnUpdate encapsulates all GenericJob validations that must be performed on a Update operation
func ValidateJobOnUpdate(oldJob, newJob GenericJob, defaultQueueExist func(string) bool) field.ErrorList {
	allErrs := validateUpdateForQueueName(oldJob, newJob, defaultQueueExist)
	allErrs = append(allErrs, validateUpdateForPrebuiltWorkload(oldJob, newJob)...)
	allErrs = append(allErrs, validateUpdateForMaxExecTime(oldJob, newJob)...)
	allErrs = append(allErrs, validateJobUpdateForWorkloadPriorityClassName(oldJob, newJob)...)
	allErrs = append(allErrs, validatedUpdateForEnabledWorkloadSlice(oldJob, newJob)...)
	return allErrs
}

func validateCreateForPrebuiltWorkload(job GenericJob) field.ErrorList {
	var allErrs field.ErrorList
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
	return allErrs
}

func validateUpdateForQueueName(oldJob, newJob GenericJob, defaultQueueExist func(string) bool) field.ErrorList {
	var allErrs field.ErrorList
	if !newJob.IsSuspended() {
		allErrs = append(allErrs, apivalidation.ValidateImmutableField(QueueName(newJob), QueueName(oldJob), queueNameLabelPath)...)
	}
	if QueueName(newJob) == "" && QueueName(oldJob) != "" && defaultQueueExist(oldJob.Object().GetNamespace()) {
		allErrs = append(allErrs, field.Invalid(queueNameLabelPath, "", "queue-name must not be empty in namespace with default queue"))
	}

	return allErrs
}

func validateUpdateForPrebuiltWorkload(oldJob, newJob GenericJob) field.ErrorList {
	if newJob.IsSuspended() || workloadslicing.Enabled(oldJob.Object()) {
		return validateCreateForPrebuiltWorkload(newJob)
	}

	oldWlName, _ := PrebuiltWorkloadFor(oldJob)
	newWlName, _ := PrebuiltWorkloadFor(newJob)
	return apivalidation.ValidateImmutableField(newWlName, oldWlName, labelsPath.Key(constants.PrebuiltWorkloadLabel))
}

func validateJobUpdateForWorkloadPriorityClassName(oldJob, newJob GenericJob) field.ErrorList {
	return ValidateUpdateForWorkloadPriorityClassName(newJob.IsSuspended(), oldJob.Object(), newJob.Object())
}

// validatedUpdateForEnabledWorkloadSlice validates that the workload-slicing toggle remains immutable on update.
//
// It compares the boolean returned by workloadslicing.Enabled for the old and new Job objects.
// If the value changed, it returns a field.ErrorList with a single field.Invalid pointing at
// labels[workloadslicing.EnabledAnnotationKey] and using apivalidation.FieldImmutableErrorMsg.
// If the value did not change, // it returns nil.
func validatedUpdateForEnabledWorkloadSlice(oldJob, newJob GenericJob) field.ErrorList {
	if oldEnabled, newEnabled := workloadslicing.Enabled(oldJob.Object()), workloadslicing.Enabled(newJob.Object()); oldEnabled != newEnabled {
		return field.ErrorList{field.Invalid(labelsPath.Key(workloadslicing.EnabledAnnotationKey), newEnabled, apivalidation.FieldImmutableErrorMsg)}
	}
	return nil
}

func ValidateUpdateForWorkloadPriorityClassName(isSuspended bool, oldObj, newObj client.Object) field.ErrorList {
	// Cannot ADD a priority class to a NON-suspended (running) workload && wpc is empty
	if !isSuspended && IsWorkloadPriorityClassNameEmpty(oldObj) {
		if !IsWorkloadPriorityClassNameEmpty(newObj) {
			return field.ErrorList{field.Invalid(workloadPriorityClassNamePath, WorkloadPriorityClassName(newObj), "WorkloadPriorityClass cannot be added to a non-suspended workload")}
		}
	}
	// Cannot REMOVE a priority class from a workload (regardless of suspended/running)
	if IsWorkloadPriorityClassNameEmpty(newObj) {
		if !IsWorkloadPriorityClassNameEmpty(oldObj) {
			return field.ErrorList{field.Invalid(workloadPriorityClassNamePath, WorkloadPriorityClassName(newObj), "WorkloadPriorityClass cannot be removed from a workload")}
		}
	}
	return nil
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

// ValidateImmutablePodGroupPodSpec function is used for serving workloads to ensure no changes are allowed
// to the PodSpec except fields that required for role-hash generation.
func ValidateImmutablePodGroupPodSpec(newPodSpec *corev1.PodSpec, oldPodSpec *corev1.PodSpec, fieldPath *field.Path) field.ErrorList {
	return validateImmutablePodGroupPodSpecPath(utilpod.SpecShape(newPodSpec), utilpod.SpecShape(oldPodSpec), fieldPath)
}

func validateImmutablePodGroupPodSpecPath(newShape, oldShape map[string]any, fieldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList

	fields := sets.New[string]()
	fields.Insert(slices.Collect(maps.Keys(newShape))...)
	fields.Insert(slices.Collect(maps.Keys(oldShape))...)

	for _, fieldName := range fields.UnsortedList() {
		childFieldPath := fieldPath.Child(fieldName)

		switch newValue := newShape[fieldName].(type) {
		case []map[string]any:
			oldValue := oldShape[fieldName].([]map[string]any)

			if len(newValue) != len(oldValue) {
				allErrs = append(allErrs, apivalidation.ValidateImmutableField(newValue, oldValue, childFieldPath)...)
				continue
			}

			for i := range newValue {
				allErrs = append(allErrs, validateImmutablePodGroupPodSpecPath(newValue[i], oldValue[i], childFieldPath.Index(i))...)
			}
		case map[string]any:
			oldValue := oldShape[fieldName].(map[string]any)
			allErrs = append(allErrs, validateImmutablePodGroupPodSpecPath(newValue, oldValue, childFieldPath)...)
		default:
			allErrs = append(allErrs, apivalidation.ValidateImmutableField(newShape[fieldName], oldShape[fieldName], childFieldPath)...)
		}
	}

	return allErrs
}

func IsWorkloadPriorityClassNameEmpty(obj client.Object) bool {
	return WorkloadPriorityClassName(obj) == ""
}
