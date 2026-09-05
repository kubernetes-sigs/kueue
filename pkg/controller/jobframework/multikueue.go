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

package jobframework

import (
	"context"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

// MultiKueueAdapter is the interface used for MultiKueue job delegation.
//
// In production SyncJob and DeleteRemoteObject receive an identity-guarded
// remote client. That client only permits objects of GVK(), validates the
// MultiKueue origin, manager-object UID, and Workload association, and rejects
// operations that cannot carry an enforceable identity check. In particular,
// cross-GVK access, subresource reads, apply operations, and alternate
// subresource bodies are unsupported. Updates and patches require the checked
// UID and resourceVersion; deletes use UID preconditions. Custom adapters must
// use only this restricted client contract.
type MultiKueueAdapter interface {
	// SyncJob creates the Job object in the worker cluster using the guarded
	// remote client, if not already created.
	// Copy the status from the remote job if already exists.
	//
	// The returned deferred flag reports that the adapter intentionally skipped
	// propagating the remote Job's status to the local Job (typically because the
	// local Job is still suspended and a status patch would violate K8s
	// suspend-validation rules). It is not an error: the caller should requeue the
	// workload on a short timer so the sync can be retried once the local Job is
	// unsuspended. Without that requeue the next reconcile may be up to
	// workerLostTimeout away and the local Job's status.Active will not catch up.
	// See https://github.com/kubernetes-sigs/kueue/issues/11115; for the design
	// discussion see
	// https://github.com/kubernetes-sigs/kueue/pull/11730#issuecomment-4566063844.
	SyncJob(ctx context.Context, localClient client.Client, remoteClient client.Client, key types.NamespacedName, workloadName, origin string) (deferred bool, err error)
	// DeleteRemoteObject deletes the Job using the same guarded-client contract.
	DeleteRemoteObject(ctx context.Context, localClient client.Client, remoteClient client.Client, key types.NamespacedName) error
	// IsJobManagedByKueue returns:
	// - a bool indicating if the job object identified by key is managed by kueue and can be delegated.
	// - a reason indicating why the job is not managed by Kueue
	// - any API error encountered during the check
	IsJobManagedByKueue(ctx context.Context, localClient client.Client, key types.NamespacedName) (bool, string, error)
	// GVK returns GVK (Group Version Kind) for the job.
	GVK() schema.GroupVersionKind
}

// MultiKueueObjectAssociation identifies the origin and remote Workload that
// mutable MultiKueue metadata associates with an object.
type MultiKueueObjectAssociation struct {
	Origin           string
	WorkloadName     string
	ManagerObjectUID types.UID
}

// MultiKueueRemoteObjectCleanupContext carries the expected remote object
// identity and the Workload metadata needed for adapter-specific cleanup.
// WorkloadAnnotations and ManagerObjectUIDs must be copies so later API
// mutations cannot change the cleanup decision.
type MultiKueueRemoteObjectCleanupContext struct {
	RemoteObjectUID     types.UID
	Association         MultiKueueObjectAssociation
	WorkloadKey         types.NamespacedName
	WorkloadAnnotations map[string]string
	ManagerObjectUIDs   map[string]types.UID
}

// MultiKueueAdapterWithRemoteObjectCleanup is an optional extension for adapters
// whose cleanup must remain bound to an exact remote controller-object instance.
// Multi-object cleanup is one use case.
type MultiKueueAdapterWithRemoteObjectCleanup interface {
	MultiKueueAdapter
	DeleteRemoteObjectWithCleanupContext(
		ctx context.Context,
		localClient client.Client,
		remoteClient client.Client,
		key types.NamespacedName,
		cleanupContext MultiKueueRemoteObjectCleanupContext,
	) error
}

// MultiKueueAdapterWithWorkloadReassignment marks adapters that can intentionally
// move one stable remote controller object between Workload slices. The adapter
// must authorize the specific manager object, and the remote object must carry
// that object's exact UID before reassignment is allowed.
type MultiKueueAdapterWithWorkloadReassignment interface {
	MultiKueueAdapter
	CanReassignWorkload(ctx context.Context, localClient client.Client, key types.NamespacedName) (bool, error)
}

// MultiKueueWatcher optional interface that can be implemented by a MultiKueueAdapter
// to receive job related watch events from the worker cluster.
// If not implemented, MultiKueue will only receive events related to the job's workload.
type MultiKueueWatcher interface {
	// GetEmptyList returns an empty list of objects
	GetEmptyList() client.ObjectList
	// WorkloadKeysFor returns the keys of the workloads of interest
	// - the object name for workloads
	// - the prebuilt workload(s) for job types
	WorkloadKeysFor(runtime.Object) ([]types.NamespacedName, error)
}

// MultiKueueLocalJobWatcher is an optional interface for MultiKueue adapters that
// forward manager-side spec changes to the worker after admission (see the Ray
// adapter's RemoteSpecSyncer). Watching the local (manager) job lets a spec change
// promptly trigger a workload reconcile (and thus SyncJob) instead of waiting for
// the next periodic requeue. Adapters that don't forward spec changes simply do not
// implement it.
type MultiKueueLocalJobWatcher interface {
	// NewEmptyLocalJob returns an empty job object of the adapter's type to watch,
	// or nil when this adapter does not currently need a local watch.
	NewEmptyLocalJob() client.Object
}

// MultiKueueMultiWorkloadAdapter is an optional interface for MultiKueue adapters
// whose jobs create multiple workloads (e.g., LeaderWorkerSet creates one workload per replica).
type MultiKueueMultiWorkloadAdapter interface {
	// GetExpectedWorkloadCount returns the number of workloads the job creates.
	GetExpectedWorkloadCount(ctx context.Context, c client.Client, key types.NamespacedName) (int, error)
	// GetWorkloadIndex extracts the numeric index from the workload for ordering.
	// Returns -1 if the index cannot be determined.
	GetWorkloadIndex(wl *kueue.Workload) int
}
