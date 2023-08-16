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

package kubeflowjob

import (
	"strings"

	kftraining "github.com/kubeflow/training-operator/pkg/apis/kubeflow.org/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/util/maps"
)

type KubeflowJob struct {
	KFJobControl KFJobControl
}

var _ jobframework.GenericJob = (*KubeflowJob)(nil)
var _ jobframework.JobWithPriorityClass = (*KubeflowJob)(nil)

func (j *KubeflowJob) Object() client.Object {
	return j.KFJobControl.Object()
}

func (j *KubeflowJob) IsSuspended() bool {
	return j.KFJobControl.RunPolicy().Suspend != nil && *j.KFJobControl.RunPolicy().Suspend
}

func (j *KubeflowJob) Suspend() {
	j.KFJobControl.RunPolicy().Suspend = ptr.To(true)
}

func (j *KubeflowJob) RunWithPodSetsInfo(podSetInfos []jobframework.PodSetInfo) error {
	j.KFJobControl.RunPolicy().Suspend = ptr.To(false)
	orderedReplicaTypes := j.KFJobControl.OrderedReplicaTypes(j.KFJobControl.ReplicaSpecs())

	if len(podSetInfos) != len(orderedReplicaTypes) {
		return jobframework.BadPodSetsInfoLenError(len(orderedReplicaTypes), len(podSetInfos))
	}
	// The node selectors are provided in the same order as the generated list of
	// podSets, use the same ordering logic to restore them.
	for index := range podSetInfos {
		replicaType := orderedReplicaTypes[index]
		info := podSetInfos[index]
		replicaSpec := &j.KFJobControl.ReplicaSpecs()[replicaType].Template.Spec
		replicaSpec.NodeSelector = maps.MergeKeepFirst(info.NodeSelector, replicaSpec.NodeSelector)
	}
	return nil
}

func (j *KubeflowJob) RestorePodSetsInfo(podSetInfos []jobframework.PodSetInfo) bool {
	orderedReplicaTypes := j.KFJobControl.OrderedReplicaTypes(j.KFJobControl.ReplicaSpecs())
	changed := false
	for index, info := range podSetInfos {
		replicaType := orderedReplicaTypes[index]
		replicaSpec := &j.KFJobControl.ReplicaSpecs()[replicaType].Template.Spec
		if !equality.Semantic.DeepEqual(replicaSpec.NodeSelector, info.NodeSelector) {
			changed = true
			replicaSpec.NodeSelector = maps.Clone(info.NodeSelector)
		}
	}
	return changed
}

func (j *KubeflowJob) Finished() (metav1.Condition, bool) {
	var conditionType kftraining.JobConditionType
	var finished bool
	for _, c := range j.KFJobControl.JobStatus().Conditions {
		if (c.Type == kftraining.JobSucceeded || c.Type == kftraining.JobFailed) && c.Status == corev1.ConditionTrue {
			conditionType = c.Type
			finished = true
			break
		}
	}
	message := "Job finished successfully"
	if conditionType == kftraining.JobFailed {
		message = "Job failed"
	}
	condition := metav1.Condition{
		Type:    kueue.WorkloadFinished,
		Status:  metav1.ConditionTrue,
		Reason:  "JobFinished",
		Message: message,
	}
	return condition, finished
}

func (j *KubeflowJob) PodSets() []kueue.PodSet {
	replicaTypes := j.KFJobControl.OrderedReplicaTypes(j.KFJobControl.ReplicaSpecs())
	podSets := make([]kueue.PodSet, len(replicaTypes))
	for index, replicaType := range replicaTypes {
		podSets[index] = kueue.PodSet{
			Name:     strings.ToLower(string(replicaType)),
			Template: *j.KFJobControl.ReplicaSpecs()[replicaType].Template.DeepCopy(),
			Count:    podsCount(j.KFJobControl.ReplicaSpecs(), replicaType),
		}
	}
	return podSets
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

func (j *KubeflowJob) PriorityClass() string {
	return j.KFJobControl.PriorityClass()
}

func podsCount(replicaSpecs map[kftraining.ReplicaType]*kftraining.ReplicaSpec, replicaType kftraining.ReplicaType) int32 {
	return ptr.Deref(replicaSpecs[replicaType].Replicas, 1)
}
