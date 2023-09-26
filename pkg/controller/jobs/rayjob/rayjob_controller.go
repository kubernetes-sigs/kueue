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

package rayjob

import (
	"context"
	"maps"
	"strings"

	rayjobapi "github.com/ray-project/kuberay/ray-operator/apis/ray/v1alpha1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	utilmaps "sigs.k8s.io/kueue/pkg/util/maps"
)

var (
	gvk           = rayjobapi.GroupVersion.WithKind("RayJob")
	FrameworkName = "ray.io/rayjob"
)

const (
	headGroupPodSetName = "head"
)

func init() {
	utilruntime.Must(jobframework.RegisterIntegration(FrameworkName, jobframework.IntegrationCallbacks{
		SetupIndexes:  SetupIndexes,
		NewReconciler: NewReconciler,
		SetupWebhook:  SetupRayJobWebhook,
		JobType:       &rayjobapi.RayJob{},
		AddToScheme:   rayjobapi.AddToScheme,
	}))
}

// +kubebuilder:rbac:groups="",resources=events,verbs=create;watch;update
// +kubebuilder:rbac:groups=ray.io,resources=rayjobs,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=ray.io,resources=rayjobs/status,verbs=get;update
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/finalizers,verbs=update
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=resourceflavors,verbs=get;list;watch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloadpriorityclasses,verbs=get;list;watch

var NewReconciler = jobframework.NewGenericReconciler(func() jobframework.GenericJob { return &RayJob{} }, nil)

type RayJob rayjobapi.RayJob

var _ jobframework.GenericJob = (*RayJob)(nil)

func (j *RayJob) Object() client.Object {
	return (*rayjobapi.RayJob)(j)
}

func (j *RayJob) IsSuspended() bool {
	return j.Spec.Suspend
}

func (j *RayJob) IsActive() bool {
	return j.Status.JobDeploymentStatus != rayjobapi.JobDeploymentStatusSuspended
}

func (j *RayJob) Suspend() {
	j.Spec.Suspend = true
}

func (j *RayJob) GVK() schema.GroupVersionKind {
	return gvk
}

func (j *RayJob) PodSets() []kueue.PodSet {
	// len = workerGroups + head
	podSets := make([]kueue.PodSet, len(j.Spec.RayClusterSpec.WorkerGroupSpecs)+1)

	// head
	podSets[0] = kueue.PodSet{
		Name:     headGroupPodSetName,
		Template: *j.Spec.RayClusterSpec.HeadGroupSpec.Template.DeepCopy(),
		Count:    1,
	}

	// workers
	for index := range j.Spec.RayClusterSpec.WorkerGroupSpecs {
		wgs := &j.Spec.RayClusterSpec.WorkerGroupSpecs[index]
		replicas := int32(1)
		if wgs.Replicas != nil {
			replicas = *wgs.Replicas
		}
		podSets[index+1] = kueue.PodSet{
			Name:     strings.ToLower(wgs.GroupName),
			Template: *wgs.Template.DeepCopy(),
			Count:    replicas,
		}
	}
	return podSets
}

func (j *RayJob) RunWithPodSetsInfo(podSetInfos []jobframework.PodSetInfo) error {
	expectedLen := len(j.Spec.RayClusterSpec.WorkerGroupSpecs) + 1
	if len(podSetInfos) != expectedLen {
		return jobframework.BadPodSetsInfoLenError(expectedLen, len(podSetInfos))
	}

	j.Spec.Suspend = false

	// head
	headPodSpec := &j.Spec.RayClusterSpec.HeadGroupSpec.Template.Spec
	headPodSpec.NodeSelector = utilmaps.MergeKeepFirst(podSetInfos[0].NodeSelector, headPodSpec.NodeSelector)

	// workers
	for index := range j.Spec.RayClusterSpec.WorkerGroupSpecs {
		workerPodSpec := &j.Spec.RayClusterSpec.WorkerGroupSpecs[index].Template.Spec
		workerPodSpec.NodeSelector = utilmaps.MergeKeepFirst(podSetInfos[index+1].NodeSelector, workerPodSpec.NodeSelector)
	}
	return nil
}

func (j *RayJob) RestorePodSetsInfo(podSetInfos []jobframework.PodSetInfo) bool {
	if len(podSetInfos) != len(j.Spec.RayClusterSpec.WorkerGroupSpecs)+1 {
		return false
	}

	changed := false
	// head
	headPodSpec := &j.Spec.RayClusterSpec.HeadGroupSpec.Template.Spec
	if !equality.Semantic.DeepEqual(headPodSpec.NodeSelector, podSetInfos[0].NodeSelector) {
		headPodSpec.NodeSelector = maps.Clone(podSetInfos[0].NodeSelector)
		changed = true
	}

	// workers
	for index := range j.Spec.RayClusterSpec.WorkerGroupSpecs {
		workerPodSpec := &j.Spec.RayClusterSpec.WorkerGroupSpecs[index].Template.Spec
		if !equality.Semantic.DeepEqual(workerPodSpec.NodeSelector, podSetInfos[index+1].NodeSelector) {
			workerPodSpec.NodeSelector = maps.Clone(podSetInfos[index+1].NodeSelector)
			changed = true
		}
	}
	return changed
}

func (j *RayJob) Finished() (metav1.Condition, bool) {
	condition := metav1.Condition{
		Type:    kueue.WorkloadFinished,
		Status:  metav1.ConditionTrue,
		Reason:  string(j.Status.JobStatus),
		Message: j.Status.Message,
	}
	return condition, rayjobapi.IsJobTerminal(j.Status.JobStatus)
}

func (j *RayJob) PodsReady() bool {
	return j.Status.RayClusterStatus.State == rayjobapi.Ready
}

func SetupIndexes(ctx context.Context, indexer client.FieldIndexer) error {
	return jobframework.SetupWorkloadOwnerIndex(ctx, indexer, gvk)
}

func GetWorkloadNameForRayJob(jobName string) string {
	return jobframework.GetWorkloadNameForOwnerWithGVK(jobName, gvk)
}
