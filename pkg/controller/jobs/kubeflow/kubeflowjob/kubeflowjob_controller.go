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

package kubeflowjob

import (
	kftraining "github.com/kubeflow/training-operator/pkg/apis/kubeflow.org/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/podset"
	utilpodset "sigs.k8s.io/kueue/pkg/util/podset"
)

type KubeflowJob struct {
	KFJobControl KFJobControl
}

var _ jobframework.GenericJob = (*KubeflowJob)(nil)
var _ jobframework.JobWithPriorityClass = (*KubeflowJob)(nil)
var _ jobframework.JobWithCustomValidation = (*KubeflowJob)(nil)
var _ jobframework.JobWithManagedBy = (*KubeflowJob)(nil)

func (j *KubeflowJob) Object() client.Object {
	return j.KFJobControl.Object()
}

func (j *KubeflowJob) IsSuspended() bool {
	return j.KFJobControl.RunPolicy().Suspend != nil && *j.KFJobControl.RunPolicy().Suspend
}

func (j *KubeflowJob) Suspend() {
	j.KFJobControl.RunPolicy().Suspend = ptr.To(true)
}

func (j *KubeflowJob) RunWithPodSetsInfo(podSetsInfo []podset.PodSetInfo) error {
	j.KFJobControl.RunPolicy().Suspend = ptr.To(false)
	orderedReplicaTypes := j.OrderedReplicaTypes()

	if len(podSetsInfo) != len(orderedReplicaTypes) {
		return podset.BadPodSetsInfoLenError(len(orderedReplicaTypes), len(podSetsInfo))
	}
	// The node selectors are provided in the same order as the generated list of
	// podSets, use the same ordering logic to restore them.
	for index := range podSetsInfo {
		replicaType := orderedReplicaTypes[index]
		info := podSetsInfo[index]
		replica := &j.KFJobControl.ReplicaSpecs()[replicaType].Template
		if err := podset.Merge(&replica.ObjectMeta, &replica.Spec, info); err != nil {
			return err
		}
	}
	return nil
}

func (j *KubeflowJob) RestorePodSetsInfo(podSetsInfo []podset.PodSetInfo) bool {
	orderedReplicaTypes := j.OrderedReplicaTypes()
	changed := false
	for index, info := range podSetsInfo {
		replicaType := orderedReplicaTypes[index]
		replica := &j.KFJobControl.ReplicaSpecs()[replicaType].Template
		changed = podset.RestorePodSpec(&replica.ObjectMeta, &replica.Spec, info) || changed
	}
	return changed
}

func (j *KubeflowJob) Finished() (message string, success, finished bool) {
	if j.KFJobControl.JobStatus() == nil {
		return "", false, false
	}

	for _, c := range j.KFJobControl.JobStatus().Conditions {
		if (c.Type == kftraining.JobSucceeded || c.Type == kftraining.JobFailed) && c.Status == corev1.ConditionTrue {
			return c.Message, c.Type != kftraining.JobFailed, true
		}
	}

	return "", true, false
}

func (j *KubeflowJob) PodSets() ([]kueue.PodSet, error) {
	replicaTypes := j.OrderedReplicaTypes()
	podSets := make([]kueue.PodSet, len(replicaTypes))
	for index, replicaType := range replicaTypes {
		podSets[index] = kueue.PodSet{
			Name:     kueue.NewPodSetReference(string(replicaType)),
			Template: *j.KFJobControl.ReplicaSpecs()[replicaType].Template.DeepCopy(),
			Count:    podsCount(j.KFJobControl.ReplicaSpecs(), replicaType),
		}
		if features.Enabled(features.TopologyAwareScheduling) {
			topologyRequest, err := jobframework.NewPodSetTopologyRequest(
				&j.KFJobControl.ReplicaSpecs()[replicaType].Template.ObjectMeta).PodIndexLabel(
				ptr.To(kftraining.ReplicaIndexLabel)).Build()
			if err != nil {
				return nil, err
			}
			podSets[index].TopologyRequest = topologyRequest
		}
	}
	return podSets, nil
}

func (j *KubeflowJob) IsActive() bool {
	for _, replicaStatus := range j.KFJobControl.JobStatus().ReplicaStatuses {
		if replicaStatus.Active != 0 {
			return true
		}
	}
	return false
}

func (j *KubeflowJob) PodsReady() bool {
	for _, c := range j.KFJobControl.JobStatus().Conditions {
		if c.Type == kftraining.JobRunning && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func (j *KubeflowJob) GVK() schema.GroupVersionKind {
	return j.KFJobControl.GVK()
}

func (j *KubeflowJob) PodLabelSelector() string {
	if jobWithPodLabelSelector, ok := j.KFJobControl.(jobframework.JobWithPodLabelSelector); ok {
		return jobWithPodLabelSelector.PodLabelSelector()
	}
	return ""
}

// PriorityClass calculates the priorityClass name needed for workload according to the following priorities:
//  1. .spec.runPolicy.schedulingPolicy.priorityClass
//  2. .spec.replicaSpecs[OrderedReplicaTypes[0]].template.spec.priorityClassName
//  3. .spec.replicaSpecs[OrderedReplicaTypes[1]].template.spec.priorityClassName
//  4. ...
//
// This function is inspired by an analogous one in mpi-controller:
// https://github.com/kubeflow/mpi-operator/blob/5946ef4157599a474ab82ff80e780d5c2546c9ee/pkg/controller/podgroup.go#L69-L72
func (j *KubeflowJob) PriorityClass() string {
	if j.KFJobControl.RunPolicy().SchedulingPolicy != nil && len(j.KFJobControl.RunPolicy().SchedulingPolicy.PriorityClass) != 0 {
		return j.KFJobControl.RunPolicy().SchedulingPolicy.PriorityClass
	}
	replicaTypes := j.OrderedReplicaTypes()
	for _, replicaType := range replicaTypes {
		if m := j.KFJobControl.ReplicaSpecs()[replicaType]; m != nil && len(m.Template.Spec.PriorityClassName) != 0 {
			return m.Template.Spec.PriorityClassName
		}
	}
	return ""
}

func (j *KubeflowJob) OrderedReplicaTypes() []kftraining.ReplicaType {
	replicaTypes := j.KFJobControl.OrderedReplicaTypes()
	result := make([]kftraining.ReplicaType, 0, len(replicaTypes))
	for _, replicaType := range replicaTypes {
		if j.KFJobControl.ReplicaSpecs()[replicaType] != nil {
			result = append(result, replicaType)
		}
	}
	return result
}

func (j *KubeflowJob) ValidateOnCreate() (field.ErrorList, error) {
	if !features.Enabled(features.TopologyAwareScheduling) {
		return nil, nil
	}

	podSets, podSetsErr := j.PodSets()

	var allErrs field.ErrorList
	replicaTypes := j.OrderedReplicaTypes()
	for _, replicaType := range replicaTypes {
		replicaSpecsPath := field.NewPath("spec", j.KFJobControl.ReplicaSpecsFieldName())
		allErrs = append(allErrs, jobframework.ValidateTASPodSetRequest(
			replicaSpecsPath.Key(string(replicaType)).Child("template", "metadata"),
			&j.KFJobControl.ReplicaSpecs()[replicaType].Template.ObjectMeta,
		)...)

		if podSetsErr != nil {
			continue
		}

		podSet := utilpodset.FindPodSetByName(podSets, kueue.NewPodSetReference(string(replicaType)))
		allErrs = append(allErrs, jobframework.ValidateSliceSizeAnnotationUpperBound(
			replicaSpecsPath.Key(string(replicaType)).Child("template", "metadata"),
			&j.KFJobControl.ReplicaSpecs()[replicaType].Template.ObjectMeta,
			podSet,
		)...)
	}

	if len(allErrs) > 0 {
		return allErrs, nil
	}

	return nil, podSetsErr
}

func (j *KubeflowJob) ValidateOnUpdate(_ jobframework.GenericJob) (field.ErrorList, error) {
	return j.ValidateOnCreate()
}

func podsCount(replicaSpecs map[kftraining.ReplicaType]*kftraining.ReplicaSpec, replicaType kftraining.ReplicaType) int32 {
	return ptr.Deref(replicaSpecs[replicaType].Replicas, 1)
}

func (j *KubeflowJob) CanDefaultManagedBy() bool {
	jobSpecManagedBy := j.KFJobControl.RunPolicy().ManagedBy
	return features.Enabled(features.MultiKueue) &&
		(jobSpecManagedBy == nil || *jobSpecManagedBy == kftraining.KubeflowJobsController)
}

func (j *KubeflowJob) ManagedBy() *string {
	return j.KFJobControl.RunPolicy().ManagedBy
}

func (j *KubeflowJob) SetManagedBy(managedBy *string) {
	j.KFJobControl.RunPolicy().ManagedBy = managedBy
}
