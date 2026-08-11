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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/features"
	clientutil "sigs.k8s.io/kueue/pkg/util/client"
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
	// remoteSpecSync is optional. When set, the adapter forwards manager-side spec
	// changes onto the remote copy on the worker cluster after admission (see
	// RemoteSpecSyncer).
	remoteSpecSync RemoteSpecSyncer[PtrT]
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

// Option configures a Ray MultiKueue adapter.
type Option[PtrT objAsPtr[T], T any] func(*adapter[PtrT, T])

// WithRemoteSpecSync enables forwarding manager-side spec changes to the worker copy
// for job types that support it (see RemoteSpecSyncer).
func WithRemoteSpecSync[PtrT objAsPtr[T], T any](s RemoteSpecSyncer[PtrT]) Option[PtrT, T] {
	return func(a *adapter[PtrT, T]) {
		a.remoteSpecSync = s
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

	// if the remote exists, just copy the status
	if err == nil {
		if err := clientutil.PatchStatus(ctx, localClient, localJob, func() (bool, error) {
			a.copyStatus(localJob, remoteJob)
			return true, nil
		}); err != nil {
			return false, err
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

func (a *adapter[PtrT, T]) DeleteRemoteObject(ctx context.Context, _ client.Client, remoteClient client.Client, key types.NamespacedName) error {
	job := PtrT(new(T))
	job.SetName(key.Name)
	job.SetNamespace(key.Namespace)
	return client.IgnoreNotFound(remoteClient.Delete(ctx, job))
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
