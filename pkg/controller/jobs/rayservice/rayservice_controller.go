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

package rayservice

import (
	"context"
	"fmt"

	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	rayutils "github.com/ray-project/kuberay/ray-operator/controllers/ray/utils"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/podset"
)

var (
	gvk = rayv1.GroupVersion.WithKind("RayService")
)

const (
	headGroupPodSetName = "head"
	FrameworkName       = "ray.io/rayservice"
)

func init() {
	utilruntime.Must(jobframework.RegisterIntegration(FrameworkName, jobframework.IntegrationCallbacks{
		SetupIndexes:      SetupIndexes,
		NewJob:            NewJob,
		NewReconciler:     NewReconciler,
		SetupWebhook:      SetupRayServiceWebhook,
		JobType:           &rayv1.RayService{},
		AddToScheme:       rayv1.AddToScheme,
		MultiKueueAdapter: &multiKueueAdapter{},
	}))
}

// +kubebuilder:rbac:groups="",resources=events,verbs=create;watch;update
// +kubebuilder:rbac:groups=ray.io,resources=rayservices,verbs=get;list;watch;update;patch;delete
// +kubebuilder:rbac:groups=ray.io,resources=rayservices/status,verbs=get;patch;update
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/finalizers,verbs=update
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=resourceflavors,verbs=get;list;watch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloadpriorityclasses,verbs=get;list;watch
// +kubebuilder:rbac:groups=ray.io,resources=rayservices/finalizers,verbs=get;update

func NewJob() jobframework.GenericJob {
	return &RayService{}
}

var NewReconciler = jobframework.NewGenericReconcilerFactory(NewJob)

type RayService rayv1.RayService

var _ jobframework.GenericJob = (*RayService)(nil)

func (j *RayService) Object() client.Object {
	return (*rayv1.RayService)(j)
}

func (j *RayService) IsSuspended() bool {
	return j.Spec.RayClusterSpec.Suspend != nil && *j.Spec.RayClusterSpec.Suspend
}

func (j *RayService) IsActive() bool {
	return j.Status.ServiceStatus == rayv1.Running
}

func (j *RayService) Suspend() {
	j.Spec.RayClusterSpec.Suspend = ptr.To(true)
}

func (j *RayService) GVK() schema.GroupVersionKind {
	return gvk
}

func (j *RayService) PodLabelSelector() string {
	if j.Status.ActiveServiceStatus.RayClusterName != "" {
		return fmt.Sprintf("%s=%s", rayutils.RayClusterLabelKey, j.Status.ActiveServiceStatus.RayClusterName)
	}
	return ""
}

func (j *RayService) PodSets(ctx context.Context) ([]kueue.PodSet, error) {
	// len = workerGroups + head
	podSets := make([]kueue.PodSet, len(j.Spec.RayClusterSpec.WorkerGroupSpecs)+1)

	// head
	podSets[0] = kueue.PodSet{
		Name:     headGroupPodSetName,
		Template: *j.Spec.RayClusterSpec.HeadGroupSpec.Template.DeepCopy(),
		Count:    1,
	}

	if features.Enabled(features.TopologyAwareScheduling) {
		topologyRequest, err := jobframework.NewPodSetTopologyRequest(
			&j.Spec.RayClusterSpec.HeadGroupSpec.Template.ObjectMeta).Build()
		if err != nil {
			return nil, err
		}
		podSets[0].TopologyRequest = topologyRequest
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
			Name:     kueue.NewPodSetReference(wgs.GroupName),
			Template: *wgs.Template.DeepCopy(),
			Count:    count,
		}
		if features.Enabled(features.TopologyAwareScheduling) {
			topologyRequest, err := jobframework.NewPodSetTopologyRequest(
				&wgs.Template.ObjectMeta).Build()
			if err != nil {
				return nil, err
			}
			podSets[index+1].TopologyRequest = topologyRequest
		}
	}
	return podSets, nil
}

func (j *RayService) RunWithPodSetsInfo(ctx context.Context, podSetsInfo []podset.PodSetInfo) error {
	expectedLen := len(j.Spec.RayClusterSpec.WorkerGroupSpecs) + 1
	if len(podSetsInfo) != expectedLen {
		return podset.BadPodSetsInfoLenError(expectedLen, len(podSetsInfo))
	}

	j.Spec.RayClusterSpec.Suspend = ptr.To(false)

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

func (j *RayService) RestorePodSetsInfo(podSetsInfo []podset.PodSetInfo) bool {
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

func (j *RayService) Finished(ctx context.Context) (message string, success, finished bool) {
	// RayService is a long-running service, not a batch job
	// It's considered "finished" only if it's in a terminal failure state
	message = string(j.Status.ServiceStatus)
	success = false
	// RayService doesn't have terminal failure states like jobs do
	// It's meant to run continuously
	finished = false
	return message, success, finished
}

func (j *RayService) PodsReady(ctx context.Context) bool {
	return j.Status.ServiceStatus == rayv1.Running
}

func SetupIndexes(ctx context.Context, indexer client.FieldIndexer) error {
	return jobframework.SetupWorkloadOwnerIndex(ctx, indexer, gvk)
}

func GetWorkloadNameForRayService(serviceName string, serviceUID types.UID) string {
	return jobframework.GetWorkloadNameForOwnerWithGVK(serviceName, serviceUID, gvk)
}
