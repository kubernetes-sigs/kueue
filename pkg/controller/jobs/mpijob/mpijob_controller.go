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

package mpijob

import (
	"context"
	"strings"

	kubeflow "github.com/kubeflow/mpi-operator/pkg/apis/kubeflow/v2beta1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/util/maps"
)

var (
	gvk = kubeflow.SchemeGroupVersionKind

	FrameworkName = "kubeflow.org/mpijob"
)

func init() {
	utilruntime.Must(jobframework.RegisterIntegration(FrameworkName, jobframework.IntegrationCallbacks{
		SetupIndexes:           SetupIndexes,
		NewReconciler:          NewReconciler,
		SetupWebhook:           SetupMPIJobWebhook,
		JobType:                &kubeflow.MPIJob{},
		AddToScheme:            kubeflow.AddToScheme,
		IsManagingObjectsOwner: isMPIJob,
	}))
}

// +kubebuilder:rbac:groups=scheduling.k8s.io,resources=priorityclasses,verbs=list;get;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;watch;update;patch
// +kubebuilder:rbac:groups=kubeflow.org,resources=mpijobs,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=kubeflow.org,resources=mpijobs/status,verbs=get;update
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/finalizers,verbs=update
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=resourceflavors,verbs=get;list;watch

var NewReconciler = jobframework.NewGenericReconciler(func() jobframework.GenericJob { return &MPIJob{} }, nil)

func isMPIJob(owner *metav1.OwnerReference) bool {
	return owner.Kind == "MPIJob" && strings.HasPrefix(owner.APIVersion, "kubeflow.org/v2")
}

type MPIJob kubeflow.MPIJob

var _ jobframework.GenericJob = (*MPIJob)(nil)
var _ jobframework.JobWithPriorityClass = (*MPIJob)(nil)

func (j *MPIJob) Object() client.Object {
	return (*kubeflow.MPIJob)(j)
}

func fromObject(o runtime.Object) *MPIJob {
	return (*MPIJob)(o.(*kubeflow.MPIJob))
}

func (j *MPIJob) IsSuspended() bool {
	return j.Spec.RunPolicy.Suspend != nil && *j.Spec.RunPolicy.Suspend
}

func (j *MPIJob) IsActive() bool {
	for _, replicaStatus := range j.Status.ReplicaStatuses {
		if replicaStatus.Active != 0 {
			return true
		}
	}
	return false
}

func (j *MPIJob) Suspend() {
	j.Spec.RunPolicy.Suspend = ptr.To(true)
}

func (j *MPIJob) GVK() schema.GroupVersionKind {
	return gvk
}

func (j *MPIJob) PodSets() []kueue.PodSet {
	replicaTypes := orderedReplicaTypes(&j.Spec)
	podSets := make([]kueue.PodSet, len(replicaTypes))
	for index, mpiReplicaType := range replicaTypes {
		podSets[index] = kueue.PodSet{
			Name:     strings.ToLower(string(mpiReplicaType)),
			Template: *j.Spec.MPIReplicaSpecs[mpiReplicaType].Template.DeepCopy(),
			Count:    podsCount(&j.Spec, mpiReplicaType),
		}
	}
	return podSets
}

func (j *MPIJob) RunWithPodSetsInfo(podSetInfos []jobframework.PodSetInfo) error {
	j.Spec.RunPolicy.Suspend = ptr.To(false)
	orderedReplicaTypes := orderedReplicaTypes(&j.Spec)

	if len(podSetInfos) != len(orderedReplicaTypes) {
		return jobframework.BadPodSetsInfoLenError(len(orderedReplicaTypes), len(podSetInfos))
	}

	// The node selectors are provided in the same order as the generated list of
	// podSets, use the same ordering logic to restore them.
	for index := range podSetInfos {
		replicaType := orderedReplicaTypes[index]
		info := podSetInfos[index]
		replicaSpec := &j.Spec.MPIReplicaSpecs[replicaType].Template.Spec
		replicaSpec.NodeSelector = maps.MergeKeepFirst(info.NodeSelector, replicaSpec.NodeSelector)
	}
	return nil
}

func (j *MPIJob) RestorePodSetsInfo(podSetInfos []jobframework.PodSetInfo) bool {
	orderedReplicaTypes := orderedReplicaTypes(&j.Spec)
	changed := false
	for index, info := range podSetInfos {
		replicaType := orderedReplicaTypes[index]
		replicaSpec := &j.Spec.MPIReplicaSpecs[replicaType].Template.Spec
		if !equality.Semantic.DeepEqual(replicaSpec.NodeSelector, info.NodeSelector) {
			changed = true
			replicaSpec.NodeSelector = maps.Clone(info.NodeSelector)
		}
	}
	return changed
}

func (j *MPIJob) Finished() (metav1.Condition, bool) {
	var conditionType kubeflow.JobConditionType
	var finished bool
	for _, c := range j.Status.Conditions {
		if (c.Type == kubeflow.JobSucceeded || c.Type == kubeflow.JobFailed) && c.Status == corev1.ConditionTrue {
			conditionType = c.Type
			finished = true
			break
		}
	}

	message := "Job finished successfully"
	if conditionType == kubeflow.JobFailed {
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

// PriorityClass calculates the priorityClass name needed for workload according to the following priorities:
//  1. .spec.runPolicy.schedulingPolicy.priorityClass
//  2. .spec.mpiReplicaSpecs[Launcher].template.spec.priorityClassName
//  3. .spec.mpiReplicaSpecs[Worker].template.spec.priorityClassName
//
// This function is inspired by an analogous one in mpi-controller:
// https://github.com/kubeflow/mpi-operator/blob/5946ef4157599a474ab82ff80e780d5c2546c9ee/pkg/controller/podgroup.go#L69-L72
func (j *MPIJob) PriorityClass() string {
	if j.Spec.RunPolicy.SchedulingPolicy != nil && len(j.Spec.RunPolicy.SchedulingPolicy.PriorityClass) != 0 {
		return j.Spec.RunPolicy.SchedulingPolicy.PriorityClass
	} else if l := j.Spec.MPIReplicaSpecs[kubeflow.MPIReplicaTypeLauncher]; l != nil && len(l.Template.Spec.PriorityClassName) != 0 {
		return l.Template.Spec.PriorityClassName
	} else if w := j.Spec.MPIReplicaSpecs[kubeflow.MPIReplicaTypeWorker]; w != nil && len(w.Template.Spec.PriorityClassName) != 0 {
		return w.Template.Spec.PriorityClassName
	}
	return ""
}

func (j *MPIJob) PodsReady() bool {
	for _, c := range j.Status.Conditions {
		if c.Type == kubeflow.JobRunning && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func SetupIndexes(ctx context.Context, indexer client.FieldIndexer) error {
	return jobframework.SetupWorkloadOwnerIndex(ctx, indexer, gvk)
}

func orderedReplicaTypes(jobSpec *kubeflow.MPIJobSpec) []kubeflow.MPIReplicaType {
	var result []kubeflow.MPIReplicaType
	if _, ok := jobSpec.MPIReplicaSpecs[kubeflow.MPIReplicaTypeLauncher]; ok {
		result = append(result, kubeflow.MPIReplicaTypeLauncher)
	}
	if _, ok := jobSpec.MPIReplicaSpecs[kubeflow.MPIReplicaTypeWorker]; ok {
		result = append(result, kubeflow.MPIReplicaTypeWorker)
	}
	return result
}

func podsCount(jobSpec *kubeflow.MPIJobSpec, mpiReplicaType kubeflow.MPIReplicaType) int32 {
	return ptr.Deref(jobSpec.MPIReplicaSpecs[mpiReplicaType].Replicas, 1)
}

func GetWorkloadNameForMPIJob(jobName string) string {
	return jobframework.GetWorkloadNameForOwnerWithGVK(jobName, gvk)
}
