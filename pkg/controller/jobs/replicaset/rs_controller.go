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

package replicaset

import (
	"context"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/podset"
)

var (
	gvk = appsv1.SchemeGroupVersion.WithKind("ReplicaSet")

	FrameworkName = "replicaset"
)

func init() {
	utilruntime.Must(jobframework.RegisterIntegration(FrameworkName, jobframework.IntegrationCallbacks{
		SetupIndexes:  SetupIndexes,
		NewJob:        NewJob,
		NewReconciler: NewReconciler,
		SetupWebhook:  SetupWebhook,
		JobType:       &appsv1.ReplicaSet{},
	}))
}

// +kubebuilder:rbac:groups=scheduling.k8s.io,resources=priorityclasses,verbs=list;get;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;watch;update;patch
// +kubebuilder:rbac:groups=apps,resources=replicasets,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=apps,resources=replicasets/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=apps,resources=replicasets/finalizers,verbs=get;update;patch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/finalizers,verbs=update
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=resourceflavors,verbs=get;list;watch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloadpriorityclasses,verbs=get;list;watch

func NewJob() jobframework.GenericJob {
	return &ReplicaSet{}
}

var NewReconciler = jobframework.NewGenericReconcilerFactory(NewJob)

type ReplicaSet appsv1.ReplicaSet

var _ jobframework.GenericJob = (*ReplicaSet)(nil)

func (r *ReplicaSet) Object() client.Object {
	return (*appsv1.ReplicaSet)(r)
}

func fromObject(o runtime.Object) *ReplicaSet {
	return (*ReplicaSet)(o.(*appsv1.ReplicaSet))
}

// IsSuspended always returns false since ReplicaSet cannot be suspended.
func (r *ReplicaSet) IsSuspended() bool {
	return false
}

// IsActive always returns true since ReplicaSet cannot be deactivated.
func (r *ReplicaSet) IsActive() bool {
	return true
}

// Suspend will suspend the job
// This functionality is nto applicable to ReplicaSet jobs.
func (r *ReplicaSet) Suspend() {}

func (r *ReplicaSet) GVK() schema.GroupVersionKind {
	return gvk
}

func (r *ReplicaSet) PodLabelSelector() string {
	return r.Spec.Selector.String()
}

func (r *ReplicaSet) PodSets() ([]kueue.PodSet, error) {
	podSet := kueue.PodSet{
		Name:     kueue.DefaultPodSetName,
		Template: *r.Spec.Template.DeepCopy(),
		Count:    ptr.Deref(r.Spec.Replicas, 0),
	}
	return []kueue.PodSet{
		podSet,
	}, nil
}

func (r *ReplicaSet) RunWithPodSetsInfo(podSetsInfo []podset.PodSetInfo) error {
	if len(podSetsInfo) != 1 {
		return podset.BadPodSetsInfoLenError(1, len(podSetsInfo))
	}
	return podset.Merge(&r.Spec.Template.ObjectMeta, &r.Spec.Template.Spec, podSetsInfo[0])
}

func (r *ReplicaSet) RestorePodSetsInfo(podSetsInfo []podset.PodSetInfo) bool {
	if len(podSetsInfo) == 0 {
		return false
	}
	return podset.RestorePodSpec(&r.Spec.Template.ObjectMeta, &r.Spec.Template.Spec, podSetsInfo[0])
}

// Finished means whether the job is completed/failed or not condition represents the workload finished condition.
// This functionality is not applicable to ReplicaSet and the return value is always finished = false.
func (r *ReplicaSet) Finished() (message string, success, finished bool) {
	return "", true, false
}

func (r *ReplicaSet) PodsReady() bool {
	return r.Status.ReadyReplicas == r.Status.Replicas
}

func SetupIndexes(ctx context.Context, fieldIndexer client.FieldIndexer) error {
	if err := fieldIndexer.IndexField(ctx, &appsv1.ReplicaSet{}, indexer.OwnerReferenceUID, indexer.IndexOwnerUID); err != nil {
		return err
	}

	return indexer.SetupWorkloadOwnerIndex(ctx, fieldIndexer, gvk)
}
