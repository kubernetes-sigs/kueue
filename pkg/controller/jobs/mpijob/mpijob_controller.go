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
	"fmt"
	"strings"

	kfmpi "github.com/kubeflow/mpi-operator/pkg/apis/kubeflow/v2beta1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/podset"
)

var (
	gvk = kfmpi.SchemeGroupVersionKind

	FrameworkName = "kubeflow.org/mpijob"
)

func init() {
	utilruntime.Must(jobframework.RegisterIntegration(FrameworkName, jobframework.IntegrationCallbacks{
		SetupIndexes:           SetupIndexes,
		NewJob:                 NewJob,
		NewReconciler:          NewReconciler,
		SetupWebhook:           SetupMPIJobWebhook,
		JobType:                &kfmpi.MPIJob{},
		AddToScheme:            kfmpi.AddToScheme,
		IsManagingObjectsOwner: isMPIJob,
		MultiKueueAdapter:      &multikueueAdapter{},
	}))
}

// +kubebuilder:rbac:groups=scheduling.k8s.io,resources=priorityclasses,verbs=list;get;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;watch;update;patch
// +kubebuilder:rbac:groups=kubeflow.org,resources=mpijobs,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=kubeflow.org,resources=mpijobs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kubeflow.org,resources=mpijobs/finalizers,verbs=get;update
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/finalizers,verbs=update
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=resourceflavors,verbs=get;list;watch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloadpriorityclasses,verbs=get;list;watch

func NewJob() jobframework.GenericJob {
	return &MPIJob{}
}

var NewReconciler = jobframework.NewGenericReconcilerFactory(NewJob)

func isMPIJob(owner *metav1.OwnerReference) bool {
	return owner.Kind == "MPIJob" && strings.HasPrefix(owner.APIVersion, kfmpi.SchemeGroupVersion.Group)
}

type MPIJob kfmpi.MPIJob

var _ jobframework.GenericJob = (*MPIJob)(nil)
var _ jobframework.JobWithPriorityClass = (*MPIJob)(nil)

func (j *MPIJob) Object() client.Object {
	return (*kfmpi.MPIJob)(j)
}

func fromObject(o runtime.Object) *MPIJob {
	return (*MPIJob)(o.(*kfmpi.MPIJob))
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

func (j *MPIJob) PodLabelSelector() string {
	return fmt.Sprintf("%s=%s,%s=%s", kfmpi.JobNameLabel, j.Name, kfmpi.OperatorNameLabel, kfmpi.OperatorName)
}

func (j *MPIJob) PodSets() []kueue.PodSet {
	replicaTypes := orderedReplicaTypes(&j.Spec)
	podSets := make([]kueue.PodSet, len(replicaTypes))
	for index, mpiReplicaType := range replicaTypes {
		podSets[index] = kueue.PodSet{
			Name:            strings.ToLower(string(mpiReplicaType)),
			Template:        *j.Spec.MPIReplicaSpecs[mpiReplicaType].Template.DeepCopy(),
			Count:           podsCount(&j.Spec, mpiReplicaType),
			TopologyRequest: jobframework.PodSetTopologyRequest(&j.Spec.MPIReplicaSpecs[mpiReplicaType].Template.ObjectMeta),
		}
	}
	return podSets
}

func (j *MPIJob) RunWithPodSetsInfo(podSetsInfo []podset.PodSetInfo) error {
	j.Spec.RunPolicy.Suspend = ptr.To(false)
	orderedReplicaTypes := orderedReplicaTypes(&j.Spec)

	if len(podSetsInfo) != len(orderedReplicaTypes) {
		return podset.BadPodSetsInfoLenError(len(orderedReplicaTypes), len(podSetsInfo))
	}

	// The node selectors are provided in the same order as the generated list of
	// podSets, use the same ordering logic to restore them.
	for index := range podSetsInfo {
		replicaType := orderedReplicaTypes[index]
		info := podSetsInfo[index]
		replica := &j.Spec.MPIReplicaSpecs[replicaType].Template
		if err := podset.Merge(&replica.ObjectMeta, &replica.Spec, info); err != nil {
			return err
		}
	}
	return nil
}

func (j *MPIJob) RestorePodSetsInfo(podSetsInfo []podset.PodSetInfo) bool {
	orderedReplicaTypes := orderedReplicaTypes(&j.Spec)
	changed := false
	for index, info := range podSetsInfo {
		replicaType := orderedReplicaTypes[index]
		replica := &j.Spec.MPIReplicaSpecs[replicaType].Template
		changed = podset.RestorePodSpec(&replica.ObjectMeta, &replica.Spec, info) || changed
	}
	return changed
}

func (j *MPIJob) Finished() (message string, success, finished bool) {
	for _, c := range j.Status.Conditions {
		if (c.Type == kfmpi.JobSucceeded || c.Type == kfmpi.JobFailed) && c.Status == corev1.ConditionTrue {
			return c.Message, c.Type != kfmpi.JobFailed, true
		}
	}

	return "", true, false
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
	} else if l := j.Spec.MPIReplicaSpecs[kfmpi.MPIReplicaTypeLauncher]; l != nil && len(l.Template.Spec.PriorityClassName) != 0 {
		return l.Template.Spec.PriorityClassName
	} else if w := j.Spec.MPIReplicaSpecs[kfmpi.MPIReplicaTypeWorker]; w != nil && len(w.Template.Spec.PriorityClassName) != 0 {
		return w.Template.Spec.PriorityClassName
	}
	return ""
}

func (j *MPIJob) PodsReady() bool {
	for _, c := range j.Status.Conditions {
		if c.Type == kfmpi.JobRunning && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func SetupIndexes(ctx context.Context, indexer client.FieldIndexer) error {
	return jobframework.SetupWorkloadOwnerIndex(ctx, indexer, gvk)
}

func orderedReplicaTypes(jobSpec *kfmpi.MPIJobSpec) []kfmpi.MPIReplicaType {
	var result []kfmpi.MPIReplicaType
	if _, ok := jobSpec.MPIReplicaSpecs[kfmpi.MPIReplicaTypeLauncher]; ok {
		result = append(result, kfmpi.MPIReplicaTypeLauncher)
	}
	if _, ok := jobSpec.MPIReplicaSpecs[kfmpi.MPIReplicaTypeWorker]; ok {
		result = append(result, kfmpi.MPIReplicaTypeWorker)
	}
	return result
}

func podsCount(jobSpec *kfmpi.MPIJobSpec, mpiReplicaType kfmpi.MPIReplicaType) int32 {
	return ptr.Deref(jobSpec.MPIReplicaSpecs[mpiReplicaType].Replicas, 1)
}

func GetWorkloadNameForMPIJob(jobName string, jobUID types.UID) string {
	return jobframework.GetWorkloadNameForOwnerWithGVK(jobName, jobUID, gvk)
}
