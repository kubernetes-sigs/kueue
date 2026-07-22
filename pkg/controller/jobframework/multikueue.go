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

// MultiKueueAdapter interface needed for MultiKueue job delegation.
type MultiKueueAdapter interface {
	// SyncJob creates the Job object in the worker cluster using remote client, if not already created.
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
	// DeleteRemoteObject deletes the Job in the worker cluster.
	DeleteRemoteObject(ctx context.Context, localClient client.Client, remoteClient client.Client, key types.NamespacedName) error
	// IsJobManagedByKueue returns:
	// - a bool indicating if the job object identified by key is managed by kueue and can be delegated.
	// - a reason indicating why the job is not managed by Kueue
	// - any API error encountered during the check
	IsJobManagedByKueue(ctx context.Context, localClient client.Client, key types.NamespacedName) (bool, string, error)
	// GVK returns GVK (Group Version Kind) for the job.
	GVK() schema.GroupVersionKind
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

// MultiKueueMultiWorkloadAdapter is an optional interface for MultiKueue adapters
// whose jobs create multiple workloads (e.g., LeaderWorkerSet creates one workload per replica).
type MultiKueueMultiWorkloadAdapter interface {
	// GetExpectedWorkloadCount returns the number of workloads the job creates.
	GetExpectedWorkloadCount(ctx context.Context, c client.Client, key types.NamespacedName) (int, error)
	// GetWorkloadIndex extracts the numeric index from the workload for ordering.
	// Returns -1 if the index cannot be determined.
	GetWorkloadIndex(wl *kueue.Workload) int
}
