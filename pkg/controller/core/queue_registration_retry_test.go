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

package core

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/event"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	utilqueue "sigs.k8s.io/kueue/pkg/util/queue"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestClusterQueueReconcileRecoversFailedInitialBackfill(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)
	cq := utiltestingapi.MakeClusterQueue("cq").Generation(1).Obj()
	cq.Finalizers = []string{kueue.ResourceInUseFinalizerName}
	lq := utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj()
	wl := utiltestingapi.MakeWorkload("wl", "ns").Queue("lq").Obj()
	errList := errors.New("injected LocalQueue List failure")
	failOnce := false
	failedLists := 0

	cl := utiltesting.NewClientBuilder().
		WithObjects(cq, lq, wl).
		WithStatusSubresource(cq, lq).
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(ctx context.Context, delegate client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				if _, ok := list.(*kueue.LocalQueueList); ok && failOnce {
					failOnce = false
					failedLists++
					return errList
				}
				return delegate.List(ctx, list, opts...)
			},
		}).
		Build()
	cache := schdcache.New(cl)
	if err := cache.AddClusterQueue(ctx, cq); err != nil {
		t.Fatalf("adding ClusterQueue to scheduler cache: %v", err)
	}
	manager := qcache.NewManagerForUnitTests(cl, cache)
	if err := manager.AddLocalQueue(ctx, lq); err != nil {
		t.Fatalf("adding existing LocalQueue: %v", err)
	}
	r := NewClusterQueueReconciler(cl, manager, cache)
	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(cq)}
	failOnce = true

	if _, err := r.Reconcile(ctx, req); !errors.Is(err, errList) {
		t.Fatalf("first Reconcile error = %v, want %v", err, errList)
	}
	if manager.ClusterQueueExists("cq") {
		t.Fatal("failed registration left a ClusterQueue in the queue manager")
	}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("retrying Reconcile: %v", err)
	}
	if !manager.ClusterQueueExists("cq") {
		t.Fatal("ClusterQueue was not registered after recovery")
	}
	if pending, err := manager.Pending(cq); err != nil || pending != 1 {
		t.Fatalf("recovered pending Workloads = (%d, %v), want (1, nil)", pending, err)
	}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("Reconcile should be idempotent after recovery: %v", err)
	}
	if failedLists != 1 {
		t.Fatalf("injected failures = %d, want 1", failedLists)
	}
}

func TestLocalQueueReconcileRecoversFailedInitialBackfill(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)
	cq := utiltestingapi.MakeClusterQueue("cq").Active(metav1.ConditionTrue).Obj()
	lq := utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj()
	wl := utiltestingapi.MakeWorkload("wl", "ns").Queue("lq").Obj()
	errList := errors.New("injected Workload List failure")
	failOnce := false
	failedLists := 0

	cl := utiltesting.NewClientBuilder().
		WithObjects(cq, lq, wl).
		WithStatusSubresource(cq, lq).
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(ctx context.Context, delegate client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				if _, ok := list.(*kueue.WorkloadList); ok && failOnce {
					failOnce = false
					failedLists++
					return errList
				}
				return delegate.List(ctx, list, opts...)
			},
		}).
		Build()
	cache := schdcache.New(cl)
	if err := cache.AddClusterQueue(ctx, cq); err != nil {
		t.Fatalf("adding ClusterQueue to scheduler cache: %v", err)
	}
	manager := qcache.NewManagerForUnitTests(cl, cache)
	if err := manager.AddClusterQueue(ctx, cq); err != nil {
		t.Fatalf("adding ClusterQueue to queue manager: %v", err)
	}
	r := NewLocalQueueReconciler(cl, manager, cache)
	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(lq)}
	failOnce = true

	if _, err := r.Reconcile(ctx, req); !errors.Is(err, errList) {
		t.Fatalf("first Reconcile error = %v, want %v", err, errList)
	}
	if manager.LocalQueueExists(utilqueue.Key(lq)) {
		t.Fatal("failed registration left a LocalQueue in the queue manager")
	}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("retrying Reconcile: %v", err)
	}
	if !manager.LocalQueueExists(utilqueue.Key(lq)) {
		t.Fatal("LocalQueue was not registered after recovery")
	}
	if pending, err := manager.PendingWorkloads(lq); err != nil || pending != 1 {
		t.Fatalf("recovered pending Workloads = (%d, %v), want (1, nil)", pending, err)
	}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("Reconcile should be idempotent after recovery: %v", err)
	}
	if failedLists != 1 {
		t.Fatalf("injected failures = %d, want 1", failedLists)
	}
}

func TestLocalQueueReconcileDoesNotRestoreStoppedOrDeletedQueue(t *testing.T) {
	for _, tc := range []struct {
		name   string
		remove func(t *testing.T, ctx context.Context, cl client.Client, r *LocalQueueReconciler, lq *kueue.LocalQueue)
	}{
		{
			name: "stopped LocalQueue",
			remove: func(t *testing.T, ctx context.Context, cl client.Client, r *LocalQueueReconciler, lq *kueue.LocalQueue) {
				current := &kueue.LocalQueue{}
				if err := cl.Get(ctx, client.ObjectKeyFromObject(lq), current); err != nil {
					t.Fatalf("reading LocalQueue before stopping: %v", err)
				}
				policy := kueue.Hold
				current.Spec.StopPolicy = &policy
				if err := cl.Update(ctx, current); err != nil {
					t.Fatalf("stopping LocalQueue: %v", err)
				}
				// Update is a controller-runtime event predicate; its signature has no context.
				//nolint:contextcheck // The informer event callback cannot accept the test context.
				r.Update(event.TypedUpdateEvent[*kueue.LocalQueue]{ObjectOld: lq, ObjectNew: current})
			},
		},
		{
			name: "deleted LocalQueue",
			remove: func(t *testing.T, ctx context.Context, cl client.Client, r *LocalQueueReconciler, lq *kueue.LocalQueue) {
				if err := cl.Delete(ctx, lq); err != nil {
					t.Fatalf("deleting LocalQueue: %v", err)
				}
				r.Delete(event.TypedDeleteEvent[*kueue.LocalQueue]{Object: lq})
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			cq := utiltestingapi.MakeClusterQueue("cq").Active(metav1.ConditionTrue).Obj()
			lq := utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj()
			wl := utiltestingapi.MakeWorkload("wl", "ns").Queue("lq").Obj()
			snapshotRead := make(chan struct{})
			resumeReconcile := make(chan struct{})
			var paused atomic.Bool
			cl := utiltesting.NewClientBuilder().
				WithObjects(cq, lq, wl).
				WithStatusSubresource(cq, lq).
				WithInterceptorFuncs(interceptor.Funcs{
					Get: func(ctx context.Context, delegate client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
						if err := delegate.Get(ctx, key, obj, opts...); err != nil {
							return err
						}
						if _, ok := obj.(*kueue.LocalQueue); ok && paused.CompareAndSwap(false, true) {
							// The reconciler now holds the original active snapshot.
							close(snapshotRead)
							<-resumeReconcile
						}
						return nil
					},
				}).
				Build()
			cache := schdcache.New(cl)
			if err := cache.AddClusterQueue(ctx, cq); err != nil {
				t.Fatalf("adding ClusterQueue to scheduler cache: %v", err)
			}
			manager := qcache.NewManagerForUnitTests(cl, cache)
			if err := manager.AddClusterQueue(ctx, cq); err != nil {
				t.Fatalf("adding ClusterQueue to manager: %v", err)
			}
			if err := manager.AddLocalQueue(ctx, lq); err != nil {
				t.Fatalf("adding LocalQueue to manager: %v", err)
			}
			r := NewLocalQueueReconciler(cl, manager, cache)
			reconcileErr := make(chan error, 1)
			go func() {
				_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(lq)})
				reconcileErr <- err
			}()

			select {
			case <-snapshotRead:
			case <-time.After(10 * time.Second):
				close(resumeReconcile)
				t.Fatal("Reconcile did not read the initial LocalQueue snapshot")
			}

			// Process the stop/delete event while Reconcile still holds the old
			// active snapshot, before it tries to recover a missing registration.
			tc.remove(t, ctx, cl, r, lq)
			if manager.LocalQueueExists(utilqueue.Key(lq)) {
				close(resumeReconcile)
				t.Fatal("stop/delete event did not remove the LocalQueue")
			}
			close(resumeReconcile)
			select {
			case err := <-reconcileErr:
				if err != nil {
					t.Fatalf("stale Reconcile returned an error: %v", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("stale Reconcile did not return")
			}
			if manager.LocalQueueExists(utilqueue.Key(lq)) {
				t.Fatal("stale Reconcile restored a stopped or deleted LocalQueue")
			}
		})
	}
}

func TestClusterQueueReconcileRegistersLatestCohortAfterConcurrentUpdate(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)
	cq := utiltestingapi.MakeClusterQueue("cq").Cohort("cohort-a").Generation(1).Obj()
	cq.Finalizers = []string{kueue.ResourceInUseFinalizerName}
	lq := utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj()
	wl := utiltestingapi.MakeWorkload("wl", "ns").Queue("lq").Obj()

	snapshotRead := make(chan struct{})
	resumeReconcile := make(chan struct{})
	var paused atomic.Bool
	cl := utiltesting.NewClientBuilder().
		WithObjects(cq, lq, wl).
		WithStatusSubresource(cq, lq).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, delegate client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if err := delegate.Get(ctx, key, obj, opts...); err != nil {
					return err
				}
				if _, ok := obj.(*kueue.ClusterQueue); ok && paused.CompareAndSwap(false, true) {
					close(snapshotRead)
					<-resumeReconcile
				}
				return nil
			},
		}).
		Build()
	cache := schdcache.New(cl)
	if err := cache.AddClusterQueue(ctx, cq); err != nil {
		t.Fatalf("adding ClusterQueue to scheduler cache: %v", err)
	}
	manager := qcache.NewManagerForUnitTests(cl, cache)
	if err := manager.AddLocalQueue(ctx, lq); err != nil {
		t.Fatalf("adding LocalQueue: %v", err)
	}
	r := NewClusterQueueReconciler(cl, manager, cache)
	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(cq)}
	reconcileErr := make(chan error, 1)
	go func() {
		_, err := r.Reconcile(ctx, req)
		reconcileErr <- err
	}()

	select {
	case <-snapshotRead:
	case <-time.After(10 * time.Second):
		close(resumeReconcile)
		t.Fatal("Reconcile did not read the original ClusterQueue")
	}

	// The Update event runs before the paused Reconcile can register the
	// ClusterQueue. An old snapshot would incorrectly attach cohort-a.
	current := cq.DeepCopy()
	current.Spec.CohortName = "cohort-b"
	current.Generation = 2
	if err := cl.Update(ctx, current); err != nil {
		close(resumeReconcile)
		t.Fatalf("updating ClusterQueue Cohort: %v", err)
	}
	r.Update(event.TypedUpdateEvent[*kueue.ClusterQueue]{ObjectOld: cq, ObjectNew: current})
	close(resumeReconcile)
	var reconcileResult error
	select {
	case reconcileResult = <-reconcileErr:
	case <-time.After(10 * time.Second):
		t.Fatal("Reconcile did not complete")
	}

	var parent kueue.CohortReference
	manager.RLock()
	q := manager.ClusterQueueForWorkloadWithoutLock(wl)
	if q != nil && q.HasParent() {
		parent = q.Parent().GetName()
	}
	manager.RUnlock()
	if parent != "cohort-b" {
		t.Fatalf("recovered ClusterQueue Cohort = %q, want cohort-b (Reconcile error: %v)", parent, reconcileResult)
	}
	if reconcileResult != nil {
		t.Fatalf("Reconcile after cohort update: %v", reconcileResult)
	}
}
