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
	"fmt"
	"strings"

	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	rayutils "github.com/ray-project/kuberay/ray-operator/controllers/ray/utils"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/podset"
)

var (
	gvk = rayv1.GroupVersion.WithKind("RayJob")
)

const (
	headGroupPodSetName = "head"
	FrameworkName       = "ray.io/rayjob"
)

func init() {
	utilruntime.Must(jobframework.RegisterIntegration(FrameworkName, jobframework.IntegrationCallbacks{
		SetupIndexes:           SetupIndexes,
		NewJob:                 NewJob,
		NewReconciler:          NewReconciler,
		SetupWebhook:           SetupRayJobWebhook,
		JobType:                &rayv1.RayJob{},
		AddToScheme:            rayv1.AddToScheme,
		IsManagingObjectsOwner: isRayJob,
	}))
}

// +kubebuilder:rbac:groups="",resources=events,verbs=create;watch;update
// +kubebuilder:rbac:groups=ray.io,resources=rayjobs,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=ray.io,resources=rayjobs/status,verbs=get;update
// +kubebuilder:rbac:groups=ray.io,resources=rayjobs/finalizers,verbs=get;update
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/finalizers,verbs=update
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=resourceflavors,verbs=get;list;watch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloadpriorityclasses,verbs=get;list;watch

func NewJob() jobframework.GenericJob {
	return &RayJob{}
}

var NewReconciler = jobframework.NewGenericReconcilerFactory(NewJob)

type RayJob rayv1.RayJob

var _ jobframework.GenericJob = (*RayJob)(nil)

func (j *RayJob) Object() client.Object {
	return (*rayv1.RayJob)(j)
}

func (j *RayJob) IsSuspended() bool {
	return j.Spec.Suspend
}

func (j *RayJob) IsActive() bool {
	return j.Status.JobDeploymentStatus != rayv1.JobDeploymentStatusSuspended
}

func (j *RayJob) Suspend() {
	j.Spec.Suspend = true
}

func (j *RayJob) GVK() schema.GroupVersionKind {
	return gvk
}

func (j *RayJob) PodLabelSelector() string {
	if j.Status.RayClusterName != "" {
		return fmt.Sprintf("%s=%s", rayutils.RayClusterLabelKey, j.Status.RayClusterName)
	}
	return ""
}

func (j *RayJob) PodSets() []kueue.PodSet {
	// len = workerGroups + head
	podSets := make([]kueue.PodSet, len(j.Spec.RayClusterSpec.WorkerGroupSpecs)+1)

	// head
	podSets[0] = kueue.PodSet{
		Name:            headGroupPodSetName,
		Template:        *j.Spec.RayClusterSpec.HeadGroupSpec.Template.DeepCopy(),
		Count:           1,
		TopologyRequest: jobframework.PodSetTopologyRequest(&j.Spec.RayClusterSpec.HeadGroupSpec.Template.ObjectMeta, nil, nil, nil),
	}

	// workers
	for index := range j.Spec.RayClusterSpec.WorkerGroupSpecs {
		wgs := &j.Spec.RayClusterSpec.WorkerGroupSpecs[index]
		count := int32(1)
		if wgs.Replicas != nil {
			count = *wgs.Replicas
		}
		if wgs.NumOfHosts > 1 {
			count *= wgs.NumOfHosts
		}
		podSets[index+1] = kueue.PodSet{
			Name:            strings.ToLower(wgs.GroupName),
			Template:        *wgs.Template.DeepCopy(),
			Count:           count,
			TopologyRequest: jobframework.PodSetTopologyRequest(&wgs.Template.ObjectMeta, nil, nil, nil),
		}
	}
	return podSets
}

func (j *RayJob) RunWithPodSetsInfo(podSetsInfo []podset.PodSetInfo) error {
	expectedLen := len(j.Spec.RayClusterSpec.WorkerGroupSpecs) + 1
	if len(podSetsInfo) != expectedLen {
		return podset.BadPodSetsInfoLenError(expectedLen, len(podSetsInfo))
	}

	j.Spec.Suspend = false

	// head
	headPod := &j.Spec.RayClusterSpec.HeadGroupSpec.Template
	info := podSetsInfo[0]
	if err := podset.Merge(&headPod.ObjectMeta, &headPod.Spec, info); err != nil {
		return err
	}

	// workers
	for index := range j.Spec.RayClusterSpec.WorkerGroupSpecs {
		workerPod := &j.Spec.RayClusterSpec.WorkerGroupSpecs[index].Template
		info := podSetsInfo[index+1]
		if err := podset.Merge(&workerPod.ObjectMeta, &workerPod.Spec, info); err != nil {
			return err
		}
	}
	return nil
}

func (j *RayJob) RestorePodSetsInfo(podSetsInfo []podset.PodSetInfo) bool {
	if len(podSetsInfo) != len(j.Spec.RayClusterSpec.WorkerGroupSpecs)+1 {
		return false
	}

	// head
	headPod := &j.Spec.RayClusterSpec.HeadGroupSpec.Template
	changed := podset.RestorePodSpec(&headPod.ObjectMeta, &headPod.Spec, podSetsInfo[0])

	// workers
	for index := range j.Spec.RayClusterSpec.WorkerGroupSpecs {
		workerPod := &j.Spec.RayClusterSpec.WorkerGroupSpecs[index].Template
		info := podSetsInfo[index+1]
		changed = podset.RestorePodSpec(&workerPod.ObjectMeta, &workerPod.Spec, info) || changed
	}
	return changed
}

func (j *RayJob) Finished() (message string, success, finished bool) {
	message = j.Status.Message
	success = j.Status.JobStatus == rayv1.JobStatusSucceeded
	finished = j.Status.JobDeploymentStatus == rayv1.JobDeploymentStatusFailed || j.Status.JobDeploymentStatus == rayv1.JobDeploymentStatusComplete
	return message, success, finished
}

func (j *RayJob) PodsReady() bool {
	return j.Status.RayClusterStatus.State == rayv1.Ready
}

func SetupIndexes(ctx context.Context, indexer client.FieldIndexer) error {
	return jobframework.SetupWorkloadOwnerIndex(ctx, indexer, gvk)
}

func GetWorkloadNameForRayJob(jobName string, jobUID types.UID) string {
	return jobframework.GetWorkloadNameForOwnerWithGVK(jobName, jobUID, gvk)
}

func isRayJob(owner *metav1.OwnerReference) bool {
	return owner.Kind == "RayJob" && strings.HasPrefix(owner.APIVersion, "ray.io/v1")
}
