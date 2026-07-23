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
	"k8s.io/klog/v2"
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
	// AutoscalingEnabled reports whether the job runs the in-tree autoscaler on
	// the worker cluster. When it does, the worker is the source of truth for
	// worker replica counts: autoscaler-driven resizes on the remote copy are
	// written back onto the manager object (reverse of the manager-driven sync),
	// letting the manager's workload-slicing machinery re-reserve quota.
	// Optional; when nil the reverse (worker-to-manager) sync is disabled.
	AutoscalingEnabled func(PtrT) bool
	// RemoteSuspended reports whether the remote copy is currently suspended.
	// A suspended remote's replica counts were restored by the worker's Kueue
	// while stopping the job, not set by the autoscaler, so they must not be
	// written back to the manager. Required when AutoscalingEnabled is set.
	RemoteSuspended func(PtrT) bool
	// FetchRuntimeWorkerState is for job types whose worker replicas live on
	// runtime child objects in the worker cluster rather than on the job's own
	// spec (e.g. the RayCluster a RayJob creates). It reads the children via the
	// remote client and returns the effective per-worker-group pod counts plus a
	// revision string identifying the observed runtime state (e.g. the child's
	// generation, used to derive a new workload-slice name). found=false means
	// the runtime objects do not exist yet and the sync is skipped. When set,
	// autoscaler-driven resizes are reflected onto the manager copy as
	// annotations (see reverseSyncRuntime) instead of a spec write-back.
	FetchRuntimeWorkerState func(ctx context.Context, remoteClient client.Client, remoteJob PtrT) (counts map[kueue.PodSetReference]int32, revision string, found bool, err error)
	// ApplyRuntimeWorkerState records the fetched runtime worker state onto the
	// manager copy (typically as annotations consumed by the job's PodSets
	// derivation and workload-slice naming), returning whether it changed
	// anything. Required when FetchRuntimeWorkerState is set.
	ApplyRuntimeWorkerState func(localJob PtrT, counts map[kueue.PodSetReference]int32, revision string) bool
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
		if a.autoscalingEnabled(localJob) {
			// A suspended remote's replicas were restored by the worker's Kueue
			// while stopping, not set by the autoscaler; do not reflect them back.
			if !a.elastic.RemoteSuspended(remoteJob) {
				changed, err := a.reverseSync(ctx, localClient, remoteClient, localJob, remoteJob)
				if err != nil || changed {
					return false, err
				}
			}
			return false, a.repointPrebuiltWorkload(ctx, remoteClient, workloadName, remoteJob)
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
	// Without the spec-based hooks the manager-driven replica push is not
	// supported for this job type (RayJob keeps its create-once behavior when
	// not autoscaling).
	if a.elastic.WorkerReplicas == nil || a.elastic.SyncReplicas == nil {
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

// autoscalingEnabled reports whether the job is an elastic job that runs the
// in-tree autoscaler on the worker cluster. In this mode the forward direction
// never pushes replicas (the worker cluster owns them); the manager's only
// forward-direction duty is repointing the remote's prebuilt-workload marker.
func (a *adapter[PtrT, T]) autoscalingEnabled(localJob PtrT) bool {
	if a.elastic == nil || a.elastic.AutoscalingEnabled == nil {
		return false
	}
	if !features.Enabled(features.ElasticJobsViaWorkloadSlices) || !workloadslicing.Enabled(localJob) {
		return false
	}
	return a.elastic.AutoscalingEnabled(localJob)
}

// repointPrebuiltWorkload ensures the remote copy's prebuilt-workload marker
// points at the currently reconciled workload slice, no-oping when it already
// does. This is slice-identity bookkeeping, not a replica sync: after a
// scale-up replacement slice is admitted, the remote must be repointed onto it
// or the worker's jobframework would keep looking up the finished slice.
func (a *adapter[PtrT, T]) repointPrebuiltWorkload(ctx context.Context, remoteClient client.Client, workloadName string, remoteJob PtrT) error {
	if err := clientutil.Patch(ctx, remoteClient, remoteJob, func() (bool, error) {
		if jobframework.PrebuiltWorkloadNameFor(remoteJob) == workloadName {
			return false, nil
		}
		jobframework.SetPrebuiltWorkloadName(remoteJob, workloadName)
		return true, nil
	}); err != nil {
		return fmt.Errorf("failed to repoint the prebuilt workload of remote %s: %w", a.gvk.Kind, err)
	}
	return nil
}

// reverseSync reverses a worker-side elastic event onto the manager copy. It
// runs the steps the job type has wired: a spec write-back for job types whose
// worker replicas live on the job object itself (RayCluster), and a
// runtime-children reflection for job types whose worker replicas live on
// runtime children in the worker cluster (RayJob). Change detection for the
// spec step is an inline count comparison (both objects are at hand); for the
// runtime step it happens inside reverseSyncRuntime, since it requires reading
// the children remotely. Returns whether the manager copy was changed.
func (a *adapter[PtrT, T]) reverseSync(ctx context.Context, localClient, remoteClient client.Client, localJob, remoteJob PtrT) (bool, error) {
	changed := false
	if a.elastic.WorkerReplicas != nil && a.elastic.SyncReplicas != nil &&
		!maps.Equal(a.elastic.WorkerReplicas(remoteJob), a.elastic.WorkerReplicas(localJob)) {
		if err := a.reverseSyncElastic(ctx, localClient, localJob, remoteJob); err != nil {
			return false, err
		}
		changed = true
	}
	if a.elastic.FetchRuntimeWorkerState != nil && a.elastic.ApplyRuntimeWorkerState != nil {
		runtimeChanged, err := a.reverseSyncRuntime(ctx, localClient, remoteClient, localJob, remoteJob)
		if err != nil {
			return false, err
		}
		changed = changed || runtimeChanged
	}
	return changed, nil
}

// reverseSyncRuntime reads the job's runtime worker state from the worker
// cluster (via FetchRuntimeWorkerState) and records it onto the manager copy
// (via ApplyRuntimeWorkerState), so the manager's PodSets derivation and
// workload-slice naming can follow autoscaler-driven resizes of children that
// do not exist on the manager. Returns whether the manager copy was changed.
func (a *adapter[PtrT, T]) reverseSyncRuntime(ctx context.Context, localClient, remoteClient client.Client, localJob, remoteJob PtrT) (bool, error) {
	counts, revision, found, err := a.elastic.FetchRuntimeWorkerState(ctx, remoteClient, remoteJob)
	if err != nil {
		return false, fmt.Errorf("failed to fetch runtime worker state for %s: %w", a.gvk.Kind, err)
	}
	if !found {
		return false, nil
	}
	changed := false
	if err := clientutil.Patch(ctx, localClient, localJob, func() (bool, error) {
		changed = a.elastic.ApplyRuntimeWorkerState(localJob, counts, revision)
		return changed, nil
	}); err != nil {
		return false, fmt.Errorf("failed to reflect runtime worker state on manager %s: %w", a.gvk.Kind, err)
	}
	return changed, nil
}

// reverseSyncElastic patches the manager object's worker replicas to match the
// remote (worker) copy after an autoscaler-driven resize. It should only be
// called when needReverseElasticSync returns true.
func (a *adapter[PtrT, T]) reverseSyncElastic(ctx context.Context, localClient client.Client, localJob, remoteJob PtrT) error {
	if err := clientutil.Patch(ctx, localClient, localJob, func() (bool, error) {
		return a.elastic.SyncReplicas(localJob, remoteJob), nil
	}); err != nil {
		return fmt.Errorf("failed to write back autoscaler-driven replicas to manager %s: %w", a.gvk.Kind, err)
	}
	return nil
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
		return nil, fmt.Errorf("no prebuilt workload found for %s: %s", a.gvk.Kind, klog.KObj(job))
	}

	return []types.NamespacedName{{Name: prebuiltWorkload, Namespace: job.GetNamespace()}}, nil
}
