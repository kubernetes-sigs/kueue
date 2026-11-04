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

package multikueue

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	toolscache "k8s.io/client-go/tools/cache"
	"k8s.io/utils/clock"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

// remoteWorkloadWriteTTL bounds how long a Workload written by this manager is read from the
// API server instead of the cache. The cache normally observes the write within a second; the
// bound only matters when the informer never reports the written resourceVersion, e.g. when a
// relist skips it, and keeps the pending writes from growing.
const remoteWorkloadWriteTTL = 30 * time.Second

// remoteWorkloadCache serves the reads of the remote Workloads labeled with the manager's origin
// from an informer on the worker cluster, so that a reconcile does not pay a round trip to every
// worker. A read is not served, and the caller reads the API server, while the informer is not
// synced, and for a Workload this manager wrote until the informer observed that write.
type remoteWorkloadCache struct {
	reader    client.Reader
	hasSynced func() bool
	clock     clock.Clock

	mu      sync.Mutex
	pending map[types.NamespacedName]pendingRemoteWrite
}

// pendingRemoteWrite is a write the cache may not have observed yet. A write whose outcome is
// unknown (it failed, or its response carries no resourceVersion) has neither field set, so it
// is only cleared by the TTL.
type pendingRemoteWrite struct {
	resourceVersion string
	deleted         bool
	expires         time.Time
}

func newRemoteWorkloadCache(reader client.Reader, hasSynced func() bool, clk clock.Clock) *remoteWorkloadCache {
	return &remoteWorkloadCache{
		reader:    reader,
		hasSynced: hasSynced,
		clock:     clk,
		pending:   make(map[types.NamespacedName]pendingRemoteWrite),
	}
}

// startRemoteWorkloadCache starts an informer, on the cluster of restConfig, for the Workloads
// labeled with origin. It runs until ctx is canceled. onEvent is called for every change observed
// after the initial list: a reconcile triggered by it is guaranteed to read the change from the
// cache, while one triggered by the remote Workload watch may run before the informer caught up.
func startRemoteWorkloadCache(ctx context.Context, restConfig *rest.Config, scheme *runtime.Scheme, origin string, clk clock.Clock, onEvent func(types.NamespacedName)) (*remoteWorkloadCache, error) {
	remoteCache, err := cache.New(restConfig, cache.Options{
		Scheme: scheme,
		ByObject: map[client.Object]cache.ByObject{
			&kueue.Workload{}: {Label: labels.SelectorFromSet(labels.Set{kueue.MultiKueueOriginLabel: origin})},
		},
		DefaultTransform: cache.TransformStripManagedFields(),
	})
	if err != nil {
		return nil, fmt.Errorf("creating remote workload cache: %w", err)
	}
	informer, err := remoteCache.GetInformer(ctx, &kueue.Workload{}, cache.BlockUntilSynced(false))
	if err != nil {
		return nil, fmt.Errorf("getting remote workload informer: %w", err)
	}
	queue := func(obj any) {
		if tombstone, ok := obj.(toolscache.DeletedFinalStateUnknown); ok {
			obj = tombstone.Obj
		}
		if wl, ok := obj.(*kueue.Workload); ok {
			onEvent(client.ObjectKeyFromObject(wl))
		}
	}
	if _, err := informer.AddEventHandler(toolscache.ResourceEventHandlerDetailedFuncs{
		// The initial list is covered by the remote Workload watch, which replays every
		// Workload when it is established, and by the reconnect resync.
		AddFunc: func(obj any, isInInitialList bool) {
			if !isInInitialList {
				queue(obj)
			}
		},
		UpdateFunc: func(_, newObj any) { queue(newObj) },
		DeleteFunc: queue,
	}); err != nil {
		return nil, fmt.Errorf("adding remote workload event handler: %w", err)
	}
	go func() {
		if err := remoteCache.Start(ctx); err != nil {
			ctrl.LoggerFrom(ctx).Error(err, "Remote workload cache execution failed")
		}
	}()
	return newRemoteWorkloadCache(remoteCache, informer.HasSynced, clk), nil
}

// get reads key into wl from the cache. It returns false, without an error, when the cache cannot
// serve the read and the caller has to read the API server.
func (c *remoteWorkloadCache) get(ctx context.Context, key types.NamespacedName, wl *kueue.Workload) (bool, error) {
	if c == nil || !c.hasSynced() {
		return false, nil
	}
	err := c.reader.Get(ctx, key, wl)
	found := err == nil
	// A failed cache read is not served, so the caller reads the API server instead.
	served := (found || apierrors.IsNotFound(err)) && c.observedWrite(key, wl, found)
	if !served {
		return false, nil
	}
	return true, err
}

// observedWrite reports whether the cached state of key, wl when found, reflects the last write
// this manager made to it.
func (c *remoteWorkloadCache) observedWrite(key types.NamespacedName, wl *kueue.Workload, found bool) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	p, ok := c.pending[key]
	if !ok {
		return true
	}
	var observed bool
	switch {
	case !c.clock.Now().Before(p.expires):
		observed = true
	case p.deleted:
		observed = !found || !wl.DeletionTimestamp.IsZero()
	case p.resourceVersion != "" && found:
		observed = resourceVersionAtLeast(wl.ResourceVersion, p.resourceVersion)
	}
	if observed {
		delete(c.pending, key)
	}
	return observed
}

// resourceVersionAtLeast reports whether resource version a is not older than b. Kubernetes
// API servers backed by etcd issue increasing decimal resource versions; a version that is not
// one is never treated as observed, so the read falls back to the API server until the TTL.
func resourceVersionAtLeast(a, b string) bool {
	av, aErr := strconv.ParseUint(a, 10, 64)
	bv, bErr := strconv.ParseUint(b, 10, 64)
	return aErr == nil && bErr == nil && av >= bv
}

func (c *remoteWorkloadCache) recordWrite(key types.NamespacedName, w pendingRemoteWrite) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.clock.Now()
	for k, p := range c.pending {
		if !now.Before(p.expires) {
			delete(c.pending, k)
		}
	}
	w.expires = now.Add(remoteWorkloadWriteTTL)
	c.pending[key] = w
}

// workloadWriteRecordingClient records every remote Workload write in the cache, so that the
// Workload is read from the API server until the cache observed the write.
type workloadWriteRecordingClient struct {
	SelectivelyCachingClient
	cache *remoteWorkloadCache
}

func (c *workloadWriteRecordingClient) record(obj client.Object, deleted bool, err error) {
	gvk, gvkErr := apiutil.GVKForObject(obj, c.Scheme())
	if gvkErr != nil || gvk.GroupKind() != kueue.SchemeGroupVersion.WithKind("Workload").GroupKind() {
		return
	}
	w := pendingRemoteWrite{}
	switch {
	case deleted:
		// A failed delete leaves the object as it was, or it is already gone.
		w.deleted = err == nil || apierrors.IsNotFound(err)
	case err == nil:
		w.resourceVersion = obj.GetResourceVersion()
	}
	c.cache.recordWrite(client.ObjectKeyFromObject(obj), w)
}

func (c *workloadWriteRecordingClient) recordApply(ac runtime.ApplyConfiguration, err error) {
	// Kueue applies Workloads through unstructured apply configurations, which are objects
	// refreshed with the response.
	if obj, ok := ac.(client.Object); ok {
		c.record(obj, false, err)
	}
}

func (c *workloadWriteRecordingClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	err := c.SelectivelyCachingClient.Create(ctx, obj, opts...)
	c.record(obj, false, err)
	return err
}

func (c *workloadWriteRecordingClient) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	err := c.SelectivelyCachingClient.Update(ctx, obj, opts...)
	c.record(obj, false, err)
	return err
}

func (c *workloadWriteRecordingClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	err := c.SelectivelyCachingClient.Patch(ctx, obj, patch, opts...)
	c.record(obj, false, err)
	return err
}

func (c *workloadWriteRecordingClient) Apply(ctx context.Context, ac runtime.ApplyConfiguration, opts ...client.ApplyOption) error {
	err := c.SelectivelyCachingClient.Apply(ctx, ac, opts...)
	c.recordApply(ac, err)
	return err
}

func (c *workloadWriteRecordingClient) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	err := c.SelectivelyCachingClient.Delete(ctx, obj, opts...)
	c.record(obj, true, err)
	return err
}

func (c *workloadWriteRecordingClient) Status() client.SubResourceWriter {
	return &workloadStatusWriteRecorder{SubResourceWriter: c.SelectivelyCachingClient.Status(), c: c}
}

type workloadStatusWriteRecorder struct {
	client.SubResourceWriter
	c *workloadWriteRecordingClient
}

func (w *workloadStatusWriteRecorder) Update(ctx context.Context, obj client.Object, opts ...client.SubResourceUpdateOption) error {
	err := w.SubResourceWriter.Update(ctx, obj, opts...)
	w.c.record(obj, false, err)
	return err
}

func (w *workloadStatusWriteRecorder) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
	err := w.SubResourceWriter.Patch(ctx, obj, patch, opts...)
	w.c.record(obj, false, err)
	return err
}

func (w *workloadStatusWriteRecorder) Apply(ctx context.Context, ac runtime.ApplyConfiguration, opts ...client.SubResourceApplyOption) error {
	err := w.SubResourceWriter.Apply(ctx, ac, opts...)
	w.c.recordApply(ac, err)
	return err
}
