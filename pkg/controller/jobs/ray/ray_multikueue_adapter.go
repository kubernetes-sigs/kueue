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
	// remoteSpecSync is optional. When set, the adapter forwards manager-side spec
	// changes onto the remote copy on the worker cluster after admission (see
	// RemoteSpecSyncer).
	remoteSpecSync RemoteSpecSyncer[PtrT]
	// markInactive is optional. When set, it is applied to the local (manager)
	// job's status right after its remote copy is deleted (see
	// WithMarkInactiveOnDelete).
	markInactive func(PtrT)
}

// RemoteSpecSyncer lets a job type forward selected spec changes from the manager
// copy to its worker copy after the job is admitted, via an in-place patch of the
// remote. Each job type decides which fields to forward and when a sync is needed.
type RemoteSpecSyncer[PtrT any] interface {
	// NeedsSync reports whether a manager-side change must be forwarded to the
	// worker copy. It must be side-effect free.
	NeedsSync(remote, local PtrT) bool
	// Apply copies the safe fields from local onto remote. It is invoked only when
	// NeedsSync returned true, and must be idempotent.
	Apply(remote, local PtrT)
}

// ElasticReplicaSync carries the type-specific hooks that let the MultiKueue
// adapter reconcile the worker replica counts of an elastic workload (the
// ElasticJobsViaWorkloadSlices feature) between the manager and the worker
// cluster. The forward direction (SyncReplicas/WorkerReplicas) pushes
// manager-driven replica edits onto the worker copy; the reverse direction
// (Runtime) reflects worker-side autoscaler resizes back onto the manager. A
// type may wire the forward hooks, Runtime, or both. RayService wires neither
// and keeps the create-once behavior.
type ElasticReplicaSync[PtrT objAsPtr[T], T any] struct {
	// SyncReplicas copies the worker replica counts from src into dst, returning
	// whether dst changed.
	SyncReplicas func(dst, src PtrT) bool
	// WorkerReplicas returns the per-worker-group replica counts keyed by PodSet
	// reference. Used to detect a replica change and its direction.
	WorkerReplicas func(PtrT) map[kueue.PodSetReference]int32
	// Runtime carries the reverse direction: worker-side autoscaler resizes are
	// reflected onto the manager copy, leaving the manager spec untouched.
	Runtime *RuntimeReplicaSync[PtrT]
	// WorkloadNameExtraPart mirrors ElasticWorkloadNameProvider for the type; it
	// is used to compute the workload name of the object's current slice.
	WorkloadNameExtraPart func(PtrT) string
	// AutoscalingEnabled reports whether the job runs the Ray Autoscaler on the
	// worker cluster, making the worker the source of truth for worker replica
	// counts. Optional; when nil the reverse (worker-to-manager) sync is disabled.
	AutoscalingEnabled func(PtrT) bool
	// IsSuspended reports whether the manager job is suspended.
	IsSuspended func(PtrT) bool
}

// FetchResult is the worker-side runtime state observed by RuntimeReplicaSync.Fetch.
type FetchResult struct {
	// Counts holds the effective pod counts keyed by PodSet reference.
	Counts map[kueue.PodSetReference]int32
	// Revision identifies the observed runtime state; it is folded into the
	// elastic workload-slice name so each reflected resize mints a fresh slice.
	Revision string
}

// RuntimeReplicaSync reflects the worker replica counts of a job's runtime
// children in the worker cluster onto the manager's copy.
type RuntimeReplicaSync[PtrT any] struct {
	// Fetch reads the runtime state from the worker cluster.
	// A nil result means the runtime object does not exist yet.
	Fetch func(ctx context.Context, remoteClient client.Client, remoteJob PtrT) (*FetchResult, error)
	// Apply records the runtime state from the worker cluster onto the manager
	// copy, returning whether anything changed.
	Apply func(localJob client.Object, result FetchResult) bool
}

// Option configures a Ray MultiKueue adapter.
type Option[PtrT objAsPtr[T], T any] func(*adapter[PtrT, T])

// WithElasticReplicaSync enables elastic replica reconciliation over MultiKueue
// for job types that support it (see ElasticReplicaSync). An incomplete wiring
// panics here so the mistake fails at startup, not at reconcile time.
func WithElasticReplicaSync[PtrT objAsPtr[T], T any](e *ElasticReplicaSync[PtrT, T]) Option[PtrT, T] {
	if e.AutoscalingEnabled != nil && (e.Runtime == nil || e.IsSuspended == nil) {
		panic("ElasticReplicaSync: Runtime and IsSuspended are required when AutoscalingEnabled is set")
	}
	if e.Runtime != nil && (e.Runtime.Fetch == nil || e.Runtime.Apply == nil) {
		panic("ElasticReplicaSync: Runtime requires Fetch and Apply")
	}
	return func(a *adapter[PtrT, T]) {
		a.elastic = e
	}
}

// WithRemoteSpecSync enables forwarding manager-side spec changes to the worker copy
// for job types that support it (see RemoteSpecSyncer).
func WithRemoteSpecSync[PtrT objAsPtr[T], T any](s RemoteSpecSyncer[PtrT]) Option[PtrT, T] {
	return func(a *adapter[PtrT, T]) {
		a.remoteSpecSync = s
	}
}

// WithMarkInactiveOnDelete supplies the type-specific status update applied to
// the local (manager) job right after its remote copy is deleted.
//
// A MultiKueue-managed Ray object's status only ever changes because MultiKueue
// mirrors it from the remote copy. The manager-side Ray operator never touches
// it, since spec.managedBy points at MultiKueue instead. If the remote gets
// deleted before its stopped status makes it back to the manager, the manager
// copy is stuck on whatever it last saw, Initializing say, forever. IsActive()
// reads straight from that status, so the job looks active forever too, and
// eviction can never finish or release the workload's quota. See
// https://github.com/kubernetes-sigs/kueue/issues/15380.
//
// Two other approaches were considered first:
//
//   - Sync the remote's status one more time right before deleting it. Dropped
//     because it is still a race: the remote may not have reached a stopped
//     state yet at the moment we read it, same bug, just narrower.
//   - Fix this in the generic scheduler instead, treating any workload whose
//     remote is gone as inactive wherever Kueue checks that. Dropped because it
//     is a much bigger change, touches code every job type shares, and a mistake
//     there has a lot more blast radius than a mistake in one adapter.
//
// What we do instead: once delete succeeds, that is proof nothing is running
// anywhere for this job. We do not need to ask the remote what happened, we
// already know. So the type-specific function passed here just writes the
// manager status straight to whatever its own IsActive() treats as inactive.
func WithMarkInactiveOnDelete[PtrT objAsPtr[T], T any](fn func(PtrT)) Option[PtrT, T] {
	return func(a *adapter[PtrT, T]) {
		a.markInactive = fn
	}
}

type fullInterface interface {
	jobframework.MultiKueueAdapter
	jobframework.MultiKueueWatcher
	jobframework.MultiKueueLocalJobWatcher
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
		if a.workerOwnsReplicas(localJob) {
			if a.elastic.IsSuspended(localJob) {
				return false, nil
			}
			changed, err := a.reflectRuntimeState(ctx, localClient, remoteClient, localJob, remoteJob)
			if err != nil {
				return false, err
			}
			if changed {
				// The manager copy changed: the slicing machinery produces a
				// replacement slice and its reconcile repoints the remote.
				// Repointing now would still name the pre-resize slice.
				return false, nil
			}
			return false, a.repointPrebuiltWorkload(ctx, remoteClient, workloadName, remoteJob)
		}
		if a.needElasticSync(ctx, workloadName, localJob, remoteJob) {
			return false, a.syncElastic(ctx, remoteClient, workloadName, localJob, remoteJob)
		}
		if a.remoteSpecSync != nil && features.Enabled(features.MultiKueueRemoteSpecSync) && a.remoteSpecSync.NeedsSync(remoteJob, localJob) {
			return false, a.syncRemoteSpec(ctx, remoteClient, localJob, remoteJob)
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
	// Without spec-based replicas there is nothing the manager could push
	// (RayJob keeps its create-once behavior when not autoscaling).
	if a.elastic.SyncReplicas == nil {
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

// workerOwnsReplicas reports whether the worker cluster owns the job's worker
// replica counts: an elastic job that runs the Ray Autoscaler there. In
// this mode the forward direction never pushes replicas; the manager's only
// forward-direction duty is repointing the remote's prebuilt-workload marker.
func (a *adapter[PtrT, T]) workerOwnsReplicas(localJob PtrT) bool {
	if a.elastic == nil || a.elastic.AutoscalingEnabled == nil {
		return false
	}
	if !features.Enabled(features.MultiKueueRayInTreeAutoscaling) || !workloadslicing.Enabled(localJob) {
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

// reflectRuntimeState reads the job's runtime worker state from the worker
// cluster (via Runtime.Fetch) and records it onto the manager copy (via
// Runtime.Apply), so the manager's PodSets derivation and
// workload-slice naming can follow autoscaler-driven resizes of children that
// do not exist on the manager. Returns whether the manager copy was changed.
func (a *adapter[PtrT, T]) reflectRuntimeState(ctx context.Context, localClient, remoteClient client.Client, localJob, remoteJob PtrT) (bool, error) {
	result, err := a.elastic.Runtime.Fetch(ctx, remoteClient, remoteJob)
	if err != nil {
		return false, fmt.Errorf("failed to fetch runtime worker state for %s: %w", a.gvk.Kind, err)
	}
	if result == nil {
		// The runtime object does not exist on the worker yet (or is suspended).
		return false, nil
	}
	changed := false
	if err := clientutil.Patch(ctx, localClient, localJob, func() (bool, error) {
		changed = a.elastic.Runtime.Apply(localJob, *result)
		return changed, nil
	}); err != nil {
		return false, fmt.Errorf("failed to reflect runtime worker state on manager %s: %w", a.gvk.Kind, err)
	}
	return changed, nil
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

// syncRemoteSpec patches the remote object's spec fields to match the local
// (manager) object via the configured RemoteSpecSyncer. It should only be called
// when the syncer's NeedsSync returns true.
func (a *adapter[PtrT, T]) syncRemoteSpec(ctx context.Context, remoteClient client.Client, localJob, remoteJob PtrT) error {
	if err := clientutil.Patch(ctx, remoteClient, remoteJob, func() (bool, error) {
		if !a.remoteSpecSync.NeedsSync(remoteJob, localJob) {
			return false, nil
		}
		a.remoteSpecSync.Apply(remoteJob, localJob)
		return true, nil
	}); err != nil {
		return fmt.Errorf("failed to sync remote %s spec: %w", a.gvk.Kind, err)
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

func (a *adapter[PtrT, T]) DeleteRemoteObject(ctx context.Context, localClient client.Client, remoteClient client.Client, key types.NamespacedName) error {
	job := PtrT(new(T))
	job.SetName(key.Name)
	job.SetNamespace(key.Namespace)
	if err := client.IgnoreNotFound(remoteClient.Delete(ctx, job)); err != nil {
		return err
	}

	if a.markInactive == nil {
		return nil
	}
	localJob := PtrT(new(T))
	if err := localClient.Get(ctx, key, localJob); err != nil {
		return client.IgnoreNotFound(err)
	}
	return clientutil.PatchStatus(ctx, localClient, localJob, func() (bool, error) {
		a.markInactive(localJob)
		return true, nil
	})
}

func (a *adapter[PtrT, T]) GetEmptyList() client.ObjectList {
	return a.emptyList()
}

// NewEmptyLocalJob lets the MultiKueue controller watch the manager job so a spec
// change promptly triggers a sync. It is wired only for types that forward spec
// changes after admission (remoteSpecSync); create-once types return nil and are
// not watched.
func (a *adapter[PtrT, T]) NewEmptyLocalJob() client.Object {
	if a.remoteSpecSync == nil {
		return nil
	}
	return PtrT(new(T))
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
