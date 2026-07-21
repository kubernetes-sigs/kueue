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

package ray

import (
	"context"
	"fmt"
	"maps"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/features"
	clientutil "sigs.k8s.io/kueue/pkg/util/client"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
)

type objAsPtr[T any] interface {
	metav1.Object
	client.Object
	*T
}

type adapter[PtrT objAsPtr[T], T any] struct {
	copySpec     func(dst, src PtrT)
	copyStatus   func(dst, src PtrT)
	emptyList    func() client.ObjectList
	gvk          schema.GroupVersionKind
	getManagedBy func(PtrT) *string
	setManagedBy func(PtrT, *string)
	// elastic is optional. When set, the adapter propagates manager-driven,
	// in-place worker replica changes of an elastic workload to the remote copy
	// on the worker cluster. It is left unset for job types that do not support
	// this (see ElasticReplicaSync).
	elastic *ElasticReplicaSync[PtrT, T]
}

// ElasticReplicaSync carries the type-specific hooks that let the MultiKueue
// adapter propagate manager-driven, in-place worker replica changes of an
// elastic workload (the ElasticJobsViaWorkloadSlices feature) to the remote
// copy on the worker cluster.
//
// Only job types whose worker replicas live directly on the Kueue-managed
// object wire this. RayCluster does. RayJob and RayService do not: their worker
// replicas belong to a child RayCluster on the worker cluster that has no
// representation on the manager, so they keep the create-once behavior and
// leave this unset.
//
// Scope: detection compares the effective per-group pod count (WorkerReplicas)
// of worker groups that exist on both the manager and the remote. Changes that
// keep the count equal but reshape its inputs (e.g. replicas vs. NumOfHosts for
// RayCluster), and worker groups present only on the manager, are out of scope:
// the PodSet count is what Kueue admits, so only the effective count matters.
type ElasticReplicaSync[PtrT objAsPtr[T], T any] struct {
	// SyncReplicas copies the worker replica counts from src into dst, returning
	// whether dst changed.
	SyncReplicas func(dst, src PtrT) bool
	// WorkerReplicas returns the per-worker-group replica counts keyed by PodSet
	// reference. Used to detect a replica change and its direction.
	WorkerReplicas func(PtrT) map[kueue.PodSetReference]int32
	// WorkloadNameExtraPart mirrors ElasticWorkloadNameProvider for the type; it
	// is used to compute the workload name of the object's current slice.
	WorkloadNameExtraPart func(PtrT) string
}

// Option configures a Ray MultiKueue adapter.
type Option[PtrT objAsPtr[T], T any] func(*adapter[PtrT, T])

// WithElasticReplicaSync enables manager-driven elastic replica propagation
// over MultiKueue for job types that support it (see ElasticReplicaSync).
func WithElasticReplicaSync[PtrT objAsPtr[T], T any](e *ElasticReplicaSync[PtrT, T]) Option[PtrT, T] {
	return func(a *adapter[PtrT, T]) {
		a.elastic = e
	}
}

type fullInterface interface {
	jobframework.MultiKueueAdapter
	jobframework.MultiKueueWatcher
}

// NewMKAdapter creates a generic MultiKueue adapter for Ray job types.
// It follows the same pattern as kubeflowjob.NewMKAdapter but adapted for
// Ray types (RayCluster, RayJob, RayService) which share an identical
// MultiKueue adapter structure.
func NewMKAdapter[PtrT objAsPtr[T], T any](
	copySpec func(dst, src PtrT),
	copyStatus func(dst, src PtrT),
	emptyList func() client.ObjectList,
	gvk schema.GroupVersionKind,
	getManagedBy func(PtrT) *string,
	setManagedBy func(PtrT, *string),
	opts ...Option[PtrT, T],
) fullInterface {
	a := &adapter[PtrT, T]{
		copySpec:     copySpec,
		copyStatus:   copyStatus,
		emptyList:    emptyList,
		gvk:          gvk,
		getManagedBy: getManagedBy,
		setManagedBy: setManagedBy,
	}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

func (a *adapter[PtrT, T]) GVK() schema.GroupVersionKind {
	return a.gvk
}

func (a *adapter[PtrT, T]) IsJobManagedByKueue(ctx context.Context, c client.Client, key types.NamespacedName) (bool, string, error) {
	job := PtrT(new(T))
	err := c.Get(ctx, key, job)
	if err != nil {
		return false, "", err
	}

	jobControllerName := ptr.Deref(a.getManagedBy(job), "")
	if jobControllerName != kueue.MultiKueueControllerName {
		return false, fmt.Sprintf("Expecting spec.managedBy to be %q not %q", kueue.MultiKueueControllerName, jobControllerName), nil
	}
	return true, "", nil
}

func (a *adapter[PtrT, T]) SyncJob(
	ctx context.Context,
	localClient client.Client,
	remoteClient client.Client,
	key types.NamespacedName,
	workloadName, origin string,
) (bool, error) {
	localJob := PtrT(new(T))
	err := localClient.Get(ctx, key, localJob)
	if err != nil {
		return false, err
	}

	remoteJob := PtrT(new(T))
	err = remoteClient.Get(ctx, key, remoteJob)
	if client.IgnoreNotFound(err) != nil {
		return false, err
	}

	// if the remote exists, copy the status and, for elastic workloads,
	// propagate any manager-driven worker replica change to the remote.
	if err == nil {
		if err := clientutil.PatchStatus(ctx, localClient, localJob, func() (bool, error) {
			a.copyStatus(localJob, remoteJob)
			return true, nil
		}); err != nil {
			return false, err
		}
		if a.needElasticSync(ctx, workloadName, localJob, remoteJob) {
			return false, a.syncElastic(ctx, remoteClient, workloadName, localJob, remoteJob)
		}
		return false, nil
	}

	remoteJob = PtrT(new(T))
	a.copySpec(remoteJob, localJob)

	// Add prebuilt workload name and multikueue origin
	jobframework.SetMultiKueueMeta(remoteJob, workloadName, origin)

	// clearing the managedBy enables the controller to take over
	a.setManagedBy(remoteJob, nil)

	return false, remoteClient.Create(ctx, remoteJob)
}

// needElasticSync reports whether the remote object must be updated to reflect
// a manager-driven worker replica change of an elastic workload. It mirrors the
// batch/Job adapter, including the stale scale-up guard.
func (a *adapter[PtrT, T]) needElasticSync(ctx context.Context, workloadName string, localJob, remoteJob PtrT) bool {
	if a.elastic == nil {
		return false
	}
	if !features.Enabled(features.ElasticJobsViaWorkloadSlices) || !workloadslicing.Enabled(localJob) {
		return false
	}

	oldCounts := a.elastic.WorkerReplicas(remoteJob)
	newCounts := a.elastic.WorkerReplicas(localJob)

	// Skip stale local Workload updates caused by a scale-up event. During
	// scale-up the GenericJobReconciler creates a new, larger Workload slice that
	// finalizes the old one. If this reconcile still observes the old slice
	// (workloadName) while the local object's replicas have already grown, the
	// observed state is stale and must not be propagated to the worker cluster.
	newWorkloadName := jobframework.GenerateWorkloadNameWithExtra(
		localJob.GetName(), localJob.GetUID(), a.gvk, a.elastic.WorkloadNameExtraPart(localJob))
	if totalReplicas(oldCounts) < totalReplicas(newCounts) && workloadName != newWorkloadName {
		ctrl.LoggerFrom(ctx).V(2).Info("Skipping stale ElasticWorkload sync",
			"observedWorkloadName", workloadName, "currentWorkloadName", newWorkloadName)
		return false
	}

	return !maps.Equal(oldCounts, newCounts) || jobframework.PrebuiltWorkloadNameFor(remoteJob) != workloadName
}

// syncElastic patches the remote object's worker replicas and prebuilt workload
// label to match the local (manager) object. It should only be called when
// needElasticSync returns true.
func (a *adapter[PtrT, T]) syncElastic(ctx context.Context, remoteClient client.Client, workloadName string, localJob, remoteJob PtrT) error {
	if err := clientutil.Patch(ctx, remoteClient, remoteJob, func() (bool, error) {
		changed := a.elastic.SyncReplicas(remoteJob, localJob)
		if jobframework.PrebuiltWorkloadNameFor(remoteJob) != workloadName {
			jobframework.SetPrebuiltWorkloadName(remoteJob, workloadName)
			changed = true
		}
		return changed, nil
	}); err != nil {
		return fmt.Errorf("failed to patch remote %s: %w", a.gvk.Kind, err)
	}
	return nil
}

func totalReplicas(counts map[kueue.PodSetReference]int32) int32 {
	var total int32
	for _, c := range counts {
		total += c
	}
	return total
}

func (a *adapter[PtrT, T]) DeleteRemoteObject(ctx context.Context, _ client.Client, remoteClient client.Client, key types.NamespacedName) error {
	job := PtrT(new(T))
	job.SetName(key.Name)
	job.SetNamespace(key.Namespace)
	return client.IgnoreNotFound(remoteClient.Delete(ctx, job))
}

func (a *adapter[PtrT, T]) GetEmptyList() client.ObjectList {
	return a.emptyList()
}

func (a *adapter[PtrT, T]) WorkloadKeysFor(o runtime.Object) ([]types.NamespacedName, error) {
	job, isTheJob := o.(PtrT)
	if !isTheJob {
		return nil, fmt.Errorf("not a %s", a.gvk.Kind)
	}

	prebuiltWorkload := jobframework.PrebuiltWorkloadNameFor(job)
	if prebuiltWorkload == "" {
		if workloadslicing.Enabled(job) {
			prebuiltWorkload = jobframework.GetWorkloadNameForOwnerWithGVKAndGeneration(job.GetName(), job.GetUID(), a.gvk, job.GetGeneration())
		} else {
			prebuiltWorkload = jobframework.GetWorkloadNameForOwnerWithGVK(job.GetName(), job.GetUID(), a.gvk)
		}
	}

	return []types.NamespacedName{{Name: prebuiltWorkload, Namespace: job.GetNamespace()}}, nil
}
