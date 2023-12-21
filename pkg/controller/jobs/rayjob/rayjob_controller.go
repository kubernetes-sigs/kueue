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
	"strings"

	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/discovery"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/podset"
	discoveryutil "sigs.k8s.io/kueue/pkg/util/discovery"
)

var (
	gvk = rayv1.GroupVersion.WithKind("RayJob")
)

const (
	headGroupPodSetName = "head"
	FrameworkName       = "ray.io/rayjob"
)

func init() {
	utilruntime.Must(jobframework.RegisterIntegration(FrameworkName,
		func(dc discovery.DiscoveryInterface) (jobframework.IntegrationCallbacks, error) {
			if res, err := discoveryutil.HasAPIResourceForGVK(dc, gvk); err != nil {
				return jobframework.IntegrationCallbacks{}, err
			} else if !res {
				if res, err = discoveryutil.HasAPIResourceForGVK(dc, gvkV1alpha1); err != nil {
					return jobframework.IntegrationCallbacks{}, err
				} else if res {
					// Use Ray v1alpha1 integration only when v1 is not available
					return rayJobV1alpha1Callbacks, nil
				}
			}
			// Otherwise use the Ray v1 integration
			return rayJobV1Callbacks, nil
		},
	))
}

var rayJobV1Callbacks = jobframework.IntegrationCallbacks{
	SetupIndexes:           SetupIndexes,
	NewReconciler:          NewReconciler,
	SetupWebhook:           SetupRayJobWebhook,
	JobType:                &rayv1.RayJob{},
	AddToScheme:            addToScheme,
	IsManagingObjectsOwner: isRayJob,
}

func addToScheme(s *runtime.Scheme) error {
	// It's needed to register the v1alpha1 types for the v1alpha1 NOOP webhook
	if err := rayJobV1alpha1Callbacks.AddToScheme(s); err != nil {
		return err
	}
	return rayv1.AddToScheme(s)
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

var NewReconciler = jobframework.NewGenericReconciler(func() jobframework.GenericJob { return &RayJob{} }, nil)

func isRayJob(owner *metav1.OwnerReference) bool {
	return owner.Kind == "RayCluster" &&
		(strings.HasPrefix(owner.APIVersion, "ray.io/v1alpha1") ||
			strings.HasPrefix(owner.APIVersion, "ray.io/v1"))
}

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

	changed := false
	// head
	headPod := &j.Spec.RayClusterSpec.HeadGroupSpec.Template
	changed = podset.RestorePodSpec(&headPod.ObjectMeta, &headPod.Spec, podSetsInfo[0]) || changed

	// workers
	for index := range j.Spec.RayClusterSpec.WorkerGroupSpecs {
		workerPod := &j.Spec.RayClusterSpec.WorkerGroupSpecs[index].Template
		info := podSetsInfo[index+1]
		changed = podset.RestorePodSpec(&workerPod.ObjectMeta, &workerPod.Spec, info) || changed
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

	return condition, j.Status.JobStatus == rayv1.JobStatusFailed || j.Status.JobStatus == rayv1.JobStatusSucceeded
}

func (j *RayJob) PodsReady() bool {
	return j.Status.RayClusterStatus.State == rayv1.Ready
}

func SetupIndexes(ctx context.Context, indexer client.FieldIndexer) error {
	return jobframework.SetupWorkloadOwnerIndex(ctx, indexer, gvk)
}

func GetWorkloadNameForRayJob(jobName string) string {
	return jobframework.GetWorkloadNameForOwnerWithGVK(jobName, gvk)
}
