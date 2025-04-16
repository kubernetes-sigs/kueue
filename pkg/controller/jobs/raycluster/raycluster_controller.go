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

package raycluster

import (
	"context"
	"fmt"

	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	rayutils "github.com/ray-project/kuberay/ray-operator/controllers/ray/utils"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/podset"
)

var (
	gvk = rayv1.GroupVersion.WithKind("RayCluster")
)

const (
	headGroupPodSetName = "head"
	FrameworkName       = "ray.io/raycluster"
)

func init() {
	utilruntime.Must(jobframework.RegisterIntegration(FrameworkName, jobframework.IntegrationCallbacks{
		SetupIndexes:      SetupIndexes,
		NewJob:            NewJob,
		NewReconciler:     NewReconciler,
		SetupWebhook:      SetupRayClusterWebhook,
		JobType:           &rayv1.RayCluster{},
		AddToScheme:       rayv1.AddToScheme,
		MultiKueueAdapter: &multiKueueAdapter{},
	}))
}

// +kubebuilder:rbac:groups="",resources=events,verbs=create;watch;update
// +kubebuilder:rbac:groups=ray.io,resources=rayclusters,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=ray.io,resources=rayclusters/status,verbs=get;patch;update
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/finalizers,verbs=update
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=resourceflavors,verbs=get;list;watch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloadpriorityclasses,verbs=get;list;watch
// +kubebuilder:rbac:groups=ray.io,resources=rayclusters/finalizers,verbs=get;update

func NewJob() jobframework.GenericJob {
	return &RayCluster{}
}

var NewReconciler = jobframework.NewGenericReconcilerFactory(NewJob)

type RayCluster rayv1.RayCluster

var _ jobframework.GenericJob = (*RayCluster)(nil)
var _ jobframework.JobWithManagedBy = (*RayCluster)(nil)

func (j *RayCluster) Object() client.Object {
	return (*rayv1.RayCluster)(j)
}

func (j *RayCluster) IsSuspended() bool {
	return j.Spec.Suspend != nil && *j.Spec.Suspend
}

func (j *RayCluster) IsActive() bool {
	return j.Status.State == rayv1.Ready
}

func (j *RayCluster) Suspend() {
	j.Spec.Suspend = ptr.To(true)
}

func (j *RayCluster) GVK() schema.GroupVersionKind {
	return gvk
}

func (j *RayCluster) PodLabelSelector() string {
	return fmt.Sprintf("%s=%s", rayutils.RayClusterLabelKey, j.Name)
}

func (j *RayCluster) PodSets() ([]kueue.PodSet, error) {
	// len = workerGroups + head
	podSets := make([]kueue.PodSet, len(j.Spec.WorkerGroupSpecs)+1)

	// head
	podSets[0] = kueue.PodSet{
		Name:     headGroupPodSetName,
		Template: *j.Spec.HeadGroupSpec.Template.DeepCopy(),
		Count:    1,
	}

	if features.Enabled(features.TopologyAwareScheduling) {
		podSets[0].TopologyRequest = jobframework.PodSetTopologyRequest(
			&j.Spec.HeadGroupSpec.Template.ObjectMeta,
			nil, nil, nil,
		)
	}

	// workers
	for index := range j.Spec.WorkerGroupSpecs {
		wgs := &j.Spec.WorkerGroupSpecs[index]
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
			podSets[index+1].TopologyRequest = jobframework.PodSetTopologyRequest(
				&wgs.Template.ObjectMeta,
				nil, nil, nil,
			)
		}
	}
	return podSets, nil
}

func (j *RayCluster) RunWithPodSetsInfo(podSetsInfo []podset.PodSetInfo) error {
	expectedLen := len(j.Spec.WorkerGroupSpecs) + 1
	if len(podSetsInfo) != expectedLen {
		return podset.BadPodSetsInfoLenError(expectedLen, len(podSetsInfo))
	}

	j.Spec.Suspend = ptr.To(false)

	// head
	headPod := &j.Spec.HeadGroupSpec.Template
	info := podSetsInfo[0]
	if err := podset.Merge(&headPod.ObjectMeta, &headPod.Spec, info); err != nil {
		return err
	}

	// workers
	for index := range j.Spec.WorkerGroupSpecs {
		workerPod := &j.Spec.WorkerGroupSpecs[index].Template

		info := podSetsInfo[index+1]
		if err := podset.Merge(&workerPod.ObjectMeta, &workerPod.Spec, info); err != nil {
			return err
		}
	}
	return nil
}

func (j *RayCluster) RestorePodSetsInfo(podSetsInfo []podset.PodSetInfo) bool {
	if len(podSetsInfo) != len(j.Spec.WorkerGroupSpecs)+1 {
		return false
	}

	// head
	headPod := &j.Spec.HeadGroupSpec.Template
	changed := podset.RestorePodSpec(&headPod.ObjectMeta, &headPod.Spec, podSetsInfo[0])

	// workers
	for index := range j.Spec.WorkerGroupSpecs {
		workerPod := &j.Spec.WorkerGroupSpecs[index].Template
		info := podSetsInfo[index+1]
		changed = podset.RestorePodSpec(&workerPod.ObjectMeta, &workerPod.Spec, info) || changed
	}
	return changed
}

func (j *RayCluster) Finished() (message string, success, finished bool) {
	// Technically a RayCluster is never "finished"
	return j.Status.Reason, j.Status.State != rayv1.Failed, false
}

func (j *RayCluster) PodsReady() bool {
	return j.Status.State == rayv1.Ready
}

func SetupIndexes(ctx context.Context, indexer client.FieldIndexer) error {
	return jobframework.SetupWorkloadOwnerIndex(ctx, indexer, gvk)
}

func GetWorkloadNameForRayCluster(jobName string, jobUID types.UID) string {
	return jobframework.GetWorkloadNameForOwnerWithGVK(jobName, jobUID, gvk)
}

func fromObject(o runtime.Object) *RayCluster {
	return (*RayCluster)(o.(*rayv1.RayCluster))
}

func (j *RayCluster) CanDefaultManagedBy() bool {
	jobSpecManagedBy := j.Spec.ManagedBy
	return features.Enabled(features.MultiKueue) &&
		(jobSpecManagedBy == nil || *jobSpecManagedBy == rayutils.KubeRayController)
}

func (j *RayCluster) ManagedBy() *string {
	return j.Spec.ManagedBy
}

func (j *RayCluster) SetManagedBy(managedBy *string) {
	j.Spec.ManagedBy = managedBy
}
