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
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	utilindexer "sigs.k8s.io/kueue/pkg/controller/core/indexer"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

var errRebuildList = errors.New("rebuild list failed")

type rebuildListClient struct {
	client.Client
	failAt int
	calls  int
}

func (c *rebuildListClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if c.failAt > 0 {
		c.calls++
		if c.calls == c.failAt {
			c.failAt = 0
			return errRebuildList
		}
	}
	return c.Client.List(ctx, list, opts...)
}

func TestClusterQueueRecreationRecoversAfterRelist(t *testing.T) {
	cases := map[string]struct {
		failAt      int
		consecutive bool
	}{
		"consecutive update-only recreations": {consecutive: true},
		"update-only recreation":              {},
		"retry queue manager listing":         {failAt: 1},
		"retry scheduler LocalQueue listing":  {failAt: 2},
		"retry scheduler Workload listing":    {failAt: 3},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, log := utiltesting.ContextWithLog(t)
			now := time.Now().Truncate(time.Second)
			oldCQ := utiltestingapi.MakeClusterQueue("cq").UID("old").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "10").Obj()).
				Active(metav1.ConditionTrue).Obj()
			newCQ := utiltestingapi.MakeClusterQueue("cq").UID("new").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "10").Obj()).
				Active(metav1.ConditionTrue).Obj()
			lq := utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj()
			reserved := utiltestingapi.MakeWorkload("reserved", "ns").Queue("lq").Request(corev1.ResourceCPU, "4").
				SimpleReserveQuota("cq", "default", now).AdmittedAt(true, now).Obj()
			pending := utiltestingapi.MakeWorkload("pending", "ns").Queue("lq").Request(corev1.ResourceCPU, "1").Obj()
			cl := &rebuildListClient{Client: utiltesting.NewClientBuilder().WithObjects(newCQ, lq, reserved, pending).
				WithIndex(&corev1.LimitRange{}, utilindexer.LimitRangeHasContainerOrPodType, utilindexer.IndexLimitRangeHasContainerOrPodType).
				WithStatusSubresource(&kueue.ClusterQueue{}, &kueue.LocalQueue{}).Build()}
			cache := schdcache.New(cl)
			cache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("default").Obj())
			manager := qcache.NewManagerForUnitTests(cl, cache, qcache.WithPreemptionExpectations(preemptexpectations.New()))
			if err := cache.AddClusterQueue(ctx, oldCQ); err != nil {
				t.Fatal(err)
			}
			if err := manager.AddClusterQueue(ctx, oldCQ); err != nil {
				t.Fatal(err)
			}
			if err := manager.AddLocalQueue(ctx, lq); err != nil {
				t.Fatal(err)
			}
			if err := manager.AddOrUpdateWorkload(log, pending); err != nil {
				t.Fatal(err)
			}
			// This reservation is deliberately absent from the API. Rebuilding must
			// restore live usage rather than relabel the old cache's accumulated usage.
			stale := utiltestingapi.MakeWorkload("stale", "ns").Queue("lq").Request(corev1.ResourceCPU, "2").
				SimpleReserveQuota("cq", "default", now).AdmittedAt(true, now).Obj()
			if !cache.AddOrUpdateWorkload(log, stale) {
				t.Fatal("adding stale reservation")
			}
			watcher := &recoveryWatcher{}
			r := NewClusterQueueReconciler(cl, manager, cache, WithWatchers(watcher))
			if !r.Update(event.TypedUpdateEvent[*kueue.ClusterQueue]{ObjectOld: oldCQ, ObjectNew: newCQ}) {
				t.Fatal("recreation did not enqueue reconciliation")
			}
			if tc.consecutive {
				intermediate := utiltestingapi.MakeClusterQueue("cq").UID("intermediate").Obj()
				// Re-deliver A -> B -> C before the queued reconciliation runs.
				r.Update(event.TypedUpdateEvent[*kueue.ClusterQueue]{ObjectOld: oldCQ, ObjectNew: intermediate})
				r.Update(event.TypedUpdateEvent[*kueue.ClusterQueue]{ObjectOld: intermediate, ObjectNew: newCQ})
			}
			cl.failAt = tc.failAt
			req := reconcile.Request{NamespacedName: client.ObjectKey{Name: "cq"}}
			if tc.failAt > 0 {
				if _, err := r.Reconcile(ctx, req); !errors.Is(err, errRebuildList) {
					t.Fatalf("first reconcile error = %v", err)
				}
				if stats, err := cache.LocalQueueUsage(lq); err != nil || stats.ClusterQueueUID != "" || len(stats.ReservedResources) != 0 {
					t.Fatalf("partial scheduler cache survived failed rebuild: %+v, %v", stats, err)
				}
			}
			// Retry without another informer event, then verify idempotence.
			for range 2 {
				if _, err := r.Reconcile(ctx, req); err != nil {
					t.Fatal(err)
				}
			}
			if watcher.oldUID != "old" || watcher.newUID != "new" {
				t.Fatalf("recovery notification = %q -> %q, want old -> new", watcher.oldUID, watcher.newUID)
			}
			stats, err := cache.LocalQueueUsage(lq)
			if err != nil {
				t.Fatal(err)
			}
			if stats.ClusterQueueUID != "new" {
				t.Fatalf("cached UID = %q, want new", stats.ClusterQueueUID)
			}
			wantUsage := []kueue.LocalQueueFlavorUsage{{Name: "default", Resources: []kueue.LocalQueueResourceUsage{{Name: corev1.ResourceCPU, Total: resource.MustParse("4")}}}}
			if diff := cmp.Diff(wantUsage, stats.ReservedResources); diff != "" {
				t.Fatalf("reserved usage (-want/+got): %s", diff)
			}
			if diff := cmp.Diff(wantUsage, stats.AdmittedResources); diff != "" {
				t.Fatalf("admitted usage (-want/+got): %s", diff)
			}
			queued := manager.PendingWorkloadsInfo("cq")
			if len(queued) != 1 || queued[0].Obj.Name != "pending" {
				t.Fatalf("unexpected pending workloads: %v", queued)
			}
			lqr := NewLocalQueueReconciler(cl, manager, cache)
			result, err := lqr.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKey{Name: "lq", Namespace: "ns"}})
			if err != nil || result != (reconcile.Result{}) {
				t.Fatalf("LocalQueue reconciliation did not converge: %v, %v", result, err)
			}
			got := &kueue.LocalQueue{}
			if err := cl.Get(ctx, client.ObjectKey{Name: "lq", Namespace: "ns"}, got); err != nil {
				t.Fatal(err)
			}
			if got.Status.ReservingWorkloads != 1 || got.Status.AdmittedWorkloads != 1 {
				t.Fatalf("unexpected workload counts: %+v", got.Status)
			}
			if diff := cmp.Diff(wantUsage, got.Status.FlavorsUsage); diff != "" {
				t.Fatalf("published usage (-want/+got): %s", diff)
			}
		})
	}
}

type recoveryWatcher struct{ oldUID, newUID string }

func (w *recoveryWatcher) NotifyClusterQueueUpdate(oldCQ, newCQ *kueue.ClusterQueue) {
	w.oldUID = string(oldCQ.UID)
	w.newUID = string(newCQ.UID)
}
