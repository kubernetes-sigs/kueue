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
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

var ErrRemoteObjectNotOwnedByMultiKueue = errors.New("remote object is not owned by MultiKueue")
var ErrMultiKueueOriginEmpty = errors.New("multikueue origin is empty")

// ValidateRemoteObjectOwnership validates that the object is managed by this MultiKueue controller.
// A MultiKueue-managed object must have the MultiKueueOriginLabel set to the controller origin.
func ValidateRemoteObjectOwnership(ctx context.Context, obj client.Object, origin string) error {
	log := ctrl.LoggerFrom(ctx).WithValues("remoteObject", client.ObjectKeyFromObject(obj), "objectType", fmt.Sprintf("%T", obj), "origin", origin)

	if origin == "" {
		log.Error(ErrMultiKueueOriginEmpty, "Remote object ownership validation failed because origin is empty")
		return ErrMultiKueueOriginEmpty
	}

	if objOrigin, owned := obj.GetLabels()[kueue.MultiKueueOriginLabel]; owned && objOrigin == origin {
		return nil
	}

	return fmt.Errorf("%w: expected %q=%q on %T %q", ErrRemoteObjectNotOwnedByMultiKueue, kueue.MultiKueueOriginLabel, origin, obj, client.ObjectKeyFromObject(obj))
}

// DeleteRemoteObjectIfOwned fetches the remote object for the given adapter's GVK and key,
// skips deletion if the object does not exist or is not owned by this MultiKueue origin,
// and otherwise delegates to adapter.DeleteRemoteObject.
func DeleteRemoteObjectIfOwned(ctx context.Context, localClient client.Client, remoteClient client.Client, adapter MultiKueueAdapter, key types.NamespacedName, origin string) error {
	log := ctrl.LoggerFrom(ctx).WithValues("remoteObject", key, "adapterGVK", adapter.GVK().String(), "origin", origin)

	if origin == "" {
		log.Error(ErrMultiKueueOriginEmpty, "Skipping remote object deletion because origin is empty")
		return ErrMultiKueueOriginEmpty
	}

	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(adapter.GVK())

	if err := remoteClient.Get(ctx, key, obj); err != nil {
		if client.IgnoreNotFound(err) == nil {
			log.V(2).Info("Skipping remote object action because object was not found")
			return nil
		}
		return err
	}

	objOrigin, owned := obj.GetLabels()[kueue.MultiKueueOriginLabel]
	if !owned || objOrigin != origin {
		log.V(2).Info("Skipping remote object action because object is not owned by this MultiKueue origin", "hasOriginLabel", owned, "objectOrigin", objOrigin)
		return nil
	}

	return adapter.DeleteRemoteObject(ctx, localClient, remoteClient, key)
}

// MultiKueueAdapter interface needed for MultiKueue job delegation.
type MultiKueueAdapter interface {
	// SyncJob creates the Job object in the worker cluster using remote client, if not already created.
	// Copy the status from the remote job if already exists.
	SyncJob(ctx context.Context, localClient client.Client, remoteClient client.Client, key types.NamespacedName, workloadName, origin string) error
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
