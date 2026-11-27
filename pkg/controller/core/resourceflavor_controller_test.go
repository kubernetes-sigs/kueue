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
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"go.uber.org/mock/gomock"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	mockscore "sigs.k8s.io/kueue/internal/mocks/controller/core"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

type rfEventType string

const (
	rfEventCreate  rfEventType = "create"
	rfEventDelete  rfEventType = "delete"
	rfEventUpdate  rfEventType = "update"
	rfEventGeneric rfEventType = "generic"
)

func newTestRFReconciler(cl *fake.ClientBuilder) *ResourceFlavorReconciler {
	c := cl.Build()
	cqCache := schdcache.New(c)
	qManager := qcache.NewManagerForUnitTests(c, nil)
	tracker := roletracker.NewFakeRoleTracker(roletracker.RoleLeader)
	return NewResourceFlavorReconciler(c, qManager, cqCache, tracker)
}

func TestResourceFlavorPredicates(t *testing.T) {
	flavor := utiltestingapi.MakeResourceFlavor("test-flavor").Obj()
	deletedFlavor := utiltestingapi.MakeResourceFlavor("test-flavor").DeletionTimestamp(time.Now()).Obj()

	cases := map[string]struct {
		eventType rfEventType
		oldRF     *kueue.ResourceFlavor
		newRF     *kueue.ResourceFlavor
		wantBool  bool
		wantOld   *kueue.ResourceFlavor
		wantNew   *kueue.ResourceFlavor
	}{
		"create returns true and notifies with nil old": {
			eventType: rfEventCreate,
			newRF:     flavor,
			wantBool:  true,
			wantOld:   nil,
			wantNew:   flavor,
		},
		"delete returns false and notifies with nil new": {
			eventType: rfEventDelete,
			oldRF:     flavor,
			wantBool:  false,
			wantOld:   flavor,
			wantNew:   nil,
		},
		"update without deletion timestamp returns false": {
			eventType: rfEventUpdate,
			oldRF:     flavor,
			newRF:     flavor,
			wantBool:  false,
			wantOld:   flavor,
			wantNew:   flavor,
		},
		"update with deletion timestamp returns true": {
			eventType: rfEventUpdate,
			oldRF:     flavor,
			newRF:     deletedFlavor,
			wantBool:  true,
			wantOld:   flavor,
			wantNew:   deletedFlavor,
		},
		"generic returns true without notifying": {
			eventType: rfEventGeneric,
			newRF:     flavor,
			wantBool:  true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			reconciler := newTestRFReconciler(utiltesting.NewClientBuilder())
			watcher := mockscore.NewMockResourceFlavorUpdateWatcher(ctrl)
			reconciler.AddUpdateWatcher(watcher)

			if tc.eventType != rfEventGeneric {
				watcher.EXPECT().NotifyResourceFlavorUpdate(tc.wantOld, tc.wantNew)
			}

			var got bool
			switch tc.eventType {
			case rfEventCreate:
				got = reconciler.Create(event.TypedCreateEvent[*kueue.ResourceFlavor]{Object: tc.newRF})
			case rfEventDelete:
				got = reconciler.Delete(event.TypedDeleteEvent[*kueue.ResourceFlavor]{Object: tc.oldRF})
			case rfEventUpdate:
				got = reconciler.Update(event.TypedUpdateEvent[*kueue.ResourceFlavor]{
					ObjectOld: tc.oldRF,
					ObjectNew: tc.newRF,
				})
			case rfEventGeneric:
				got = reconciler.Generic(event.TypedGenericEvent[*kueue.ResourceFlavor]{Object: tc.newRF})
			}

			if got != tc.wantBool {
				t.Errorf("predicate returned %v, want %v", got, tc.wantBool)
			}
		})
	}
}

func TestResourceFlavorReconcile(t *testing.T) {
	cases := map[string]struct {
		flavor        *kueue.ResourceFlavor
		clusterQueues []*kueue.ClusterQueue
		skipStore     bool
		wantFlavor    *kueue.ResourceFlavor
		wantDeleted   bool
		wantError     bool
	}{
		"adds finalizer when missing": {
			flavor:     utiltestingapi.MakeResourceFlavor("flavor").Obj(),
			wantFlavor: utiltestingapi.MakeResourceFlavor("flavor").Finalizers(kueue.ResourceInUseFinalizerName).Obj(),
		},
		"no-op when finalizer already present": {
			flavor:     utiltestingapi.MakeResourceFlavor("flavor").Finalizers(kueue.ResourceInUseFinalizerName).Obj(),
			wantFlavor: utiltestingapi.MakeResourceFlavor("flavor").Finalizers(kueue.ResourceInUseFinalizerName).Obj(),
		},
		"removes finalizer when deleted and not in use": {
			flavor:      utiltestingapi.MakeResourceFlavor("flavor").Finalizers(kueue.ResourceInUseFinalizerName).Obj(),
			wantDeleted: true,
		},
		"keeps finalizer when deleted but still in use": {
			flavor: utiltestingapi.MakeResourceFlavor("flavor").Finalizers(kueue.ResourceInUseFinalizerName).Obj(),
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("cq-using-flavor").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("flavor").
						Resource(corev1.ResourceCPU, "10").
						Obj()).
					Obj(),
			},
			wantFlavor: utiltestingapi.MakeResourceFlavor("flavor").Finalizers(kueue.ResourceInUseFinalizerName).Obj(),
		},
		"ignores not-found flavor": {
			flavor:    utiltestingapi.MakeResourceFlavor("flavor").Obj(),
			skipStore: true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)

			builder := utiltesting.NewClientBuilder()
			if !tc.skipStore {
				builder = builder.WithObjects(tc.flavor)
			}
			cl := builder.Build()

			cqCache := schdcache.New(cl)
			qManager := qcache.NewManagerForUnitTests(cl, nil)
			tracker := roletracker.NewFakeRoleTracker(roletracker.RoleLeader)
			reconciler := NewResourceFlavorReconciler(cl, qManager, cqCache, tracker)

			for _, cq := range tc.clusterQueues {
				if err := cqCache.AddClusterQueue(ctx, cq); err != nil {
					t.Fatalf("failed to add ClusterQueue to cache: %v", err)
				}
			}

			if tc.wantDeleted {
				if err := cl.Delete(ctx, tc.flavor); err != nil {
					t.Fatalf("failed to delete ResourceFlavor: %v", err)
				}
			}

			req := reconcile.Request{Name: tc.flavor.Name}
			_, err := reconciler.Reconcile(ctx, req)

			if tc.wantError && err == nil {
				t.Error("expected error but got nil")
			} else if !tc.wantError && err != nil {
				t.Errorf("unexpected error: %v", err)
			}

			if tc.skipStore {
				return
			}

			got := &kueue.ResourceFlavor{}
			getErr := cl.Get(ctx, types.NamespacedName{Name: tc.flavor.Name}, got)

			if tc.wantDeleted {
				if !apierrors.IsNotFound(getErr) {
					t.Errorf("expected ResourceFlavor to be deleted, got: %v", getErr)
				}
				return
			}

			if getErr != nil {
				t.Fatalf("failed to get ResourceFlavor: %v", getErr)
			}

			if diff := cmp.Diff(tc.wantFlavor, got,
				cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion"),
				cmpopts.EquateEmpty(),
			); diff != "" {
				t.Errorf("unexpected ResourceFlavor (-want +got):\n%s", diff)
			}
		})
	}
}

func TestNotifyClusterQueueUpdate(t *testing.T) {
	makeCQWithFlavors := func(name string, flavors ...string) *kueue.ClusterQueue {
		fqs := make([]kueue.FlavorQuotas, len(flavors))
		for i, f := range flavors {
			fqs[i] = *utiltestingapi.MakeFlavorQuotas(f).Resource(corev1.ResourceCPU, "10").Obj()
		}
		return utiltestingapi.MakeClusterQueue(name).ResourceGroup(fqs...).Obj()
	}

	cases := map[string]struct {
		oldCQ       *kueue.ClusterQueue
		newCQ       *kueue.ClusterQueue
		expectEvent bool
	}{
		"create event (oldCQ nil) does not send": {
			oldCQ:       nil,
			newCQ:       makeCQWithFlavors("cq", "flavor-a"),
			expectEvent: false,
		},
		"delete event (newCQ nil) sends old CQ to channel": {
			oldCQ:       makeCQWithFlavors("cq", "flavor-a"),
			newCQ:       nil,
			expectEvent: true,
		},
		"update with same flavors does not send": {
			oldCQ:       makeCQWithFlavors("cq", "flavor-a"),
			newCQ:       makeCQWithFlavors("cq", "flavor-a"),
			expectEvent: false,
		},
		"update with added flavor sends": {
			oldCQ:       makeCQWithFlavors("cq", "flavor-a"),
			newCQ:       makeCQWithFlavors("cq", "flavor-a", "flavor-b"),
			expectEvent: true,
		},
		"update with removed flavor sends": {
			oldCQ:       makeCQWithFlavors("cq", "flavor-a", "flavor-b"),
			newCQ:       makeCQWithFlavors("cq", "flavor-a"),
			expectEvent: true,
		},
		"update with replaced flavor sends": {
			oldCQ:       makeCQWithFlavors("cq", "flavor-a"),
			newCQ:       makeCQWithFlavors("cq", "flavor-b"),
			expectEvent: true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			reconciler := newTestRFReconciler(utiltesting.NewClientBuilder())

			reconciler.NotifyClusterQueueUpdate(tc.oldCQ, tc.newCQ)

			select {
			case <-reconciler.cqUpdateCh:
				if !tc.expectEvent {
					t.Error("unexpected event sent to channel")
				}
			default:
				if tc.expectEvent {
					t.Error("expected event in channel but channel was empty")
				}
			}
		})
	}
}

func TestCqHandlerGeneric(t *testing.T) {
	makeCQWithFlavors := func(name string, flavors ...string) *kueue.ClusterQueue {
		fqs := make([]kueue.FlavorQuotas, len(flavors))
		for i, f := range flavors {
			fqs[i] = *utiltestingapi.MakeFlavorQuotas(f).Resource(corev1.ResourceCPU, "10").Obj()
		}
		return utiltestingapi.MakeClusterQueue(name).ResourceGroup(fqs...).Obj()
	}

	cases := map[string]struct {
		cq            *kueue.ClusterQueue
		clusterQueues []*kueue.ClusterQueue
		wantEnqueued  []string
	}{
		"empty CQ name is a no-op": {
			cq:           &kueue.ClusterQueue{},
			wantEnqueued: nil,
		},
		"enqueues flavors not used by any other CQ": {
			cq:           makeCQWithFlavors("cq", "flavor-orphan"),
			wantEnqueued: []string{"flavor-orphan"},
		},
		"does not enqueue flavors still used by another CQ": {
			cq: makeCQWithFlavors("cq", "flavor-shared"),
			clusterQueues: []*kueue.ClusterQueue{
				makeCQWithFlavors("other-cq", "flavor-shared"),
			},
			wantEnqueued: nil,
		},
		"enqueues only orphaned flavors when CQ has multiple": {
			cq: makeCQWithFlavors("cq", "flavor-orphan", "flavor-shared"),
			clusterQueues: []*kueue.ClusterQueue{
				makeCQWithFlavors("other-cq", "flavor-shared"),
			},
			wantEnqueued: []string{"flavor-orphan"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			cl := utiltesting.NewClientBuilder().Build()
			cqCache := schdcache.New(cl)

			for _, cq := range tc.clusterQueues {
				if err := cqCache.AddClusterQueue(ctx, cq); err != nil {
					t.Fatalf("failed to add ClusterQueue to cache: %v", err)
				}
			}

			h := &cqHandler{cache: cqCache}
			q := &utiltesting.MockTypedRateLimitingInterface{}
			h.Generic(ctx, event.GenericEvent{Object: tc.cq}, q)

			var gotNames []string
			for _, r := range q.Items {
				gotNames = append(gotNames, r.Name)
			}

			if diff := cmp.Diff(tc.wantEnqueued, gotNames,
				cmpopts.SortSlices(func(a, b string) bool { return a < b }),
				cmpopts.EquateEmpty(),
			); diff != "" {
				t.Errorf("enqueued requests mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestFlavorSchedulingFieldsChanged(t *testing.T) {
	cases := map[string]struct {
		oldRF *kueue.ResourceFlavor
		newRF *kueue.ResourceFlavor
		want  bool
	}{
		"topologyName nil to nil unchanged": {
			oldRF: utiltestingapi.MakeResourceFlavor("rf").NodeLabel("k", "v").Obj(),
			newRF: utiltestingapi.MakeResourceFlavor("rf").NodeLabel("k", "v").Obj(),
			want:  false,
		},
		"topologyName set to same value unchanged": {
			oldRF: utiltestingapi.MakeResourceFlavor("rf").TopologyName("topo-a").Obj(),
			newRF: utiltestingapi.MakeResourceFlavor("rf").TopologyName("topo-a").Obj(),
			want:  false,
		},
		"topologyName set from nil to A": {
			oldRF: utiltestingapi.MakeResourceFlavor("rf").Obj(),
			newRF: utiltestingapi.MakeResourceFlavor("rf").TopologyName("topo-a").Obj(),
			want:  true,
		},
		"topologyName unset from A to nil": {
			oldRF: utiltestingapi.MakeResourceFlavor("rf").TopologyName("topo-a").Obj(),
			newRF: utiltestingapi.MakeResourceFlavor("rf").Obj(),
			want:  true,
		},
		"topologyName changed from A to B": {
			oldRF: utiltestingapi.MakeResourceFlavor("rf").TopologyName("topo-a").Obj(),
			newRF: utiltestingapi.MakeResourceFlavor("rf").TopologyName("topo-b").Obj(),
			want:  true,
		},
		"unrelated metadata changed": {
			oldRF: utiltestingapi.MakeResourceFlavor("rf").TopologyName("topo-a").Label("a", "1").Obj(),
			newRF: utiltestingapi.MakeResourceFlavor("rf").TopologyName("topo-a").Label("a", "2").Obj(),
			want:  false,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := flavorSchedulingFieldsChanged(tc.oldRF, tc.newRF); got != tc.want {
				t.Errorf("flavorSchedulingFieldsChanged() = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestResourceFlavorUpdate(t *testing.T) {
	const (
		cqName = kueue.ClusterQueueReference("cq")
		lqName = kueue.LocalQueueName("lq")
	)

	cases := map[string]struct {
		oldRF               *kueue.ResourceFlavor
		newRF               *kueue.ResourceFlavor
		wantReconcile       bool
		wantActive          map[kueue.ClusterQueueReference][]workload.Reference
		wantInadmissible    map[kueue.ClusterQueueReference][]workload.Reference
		wantTASCachePresent bool
	}{
		"unset topologyName retries inadmissible workloads and clears TAS cache": {
			oldRF: utiltestingapi.MakeResourceFlavor("rf").NodeLabel("node-group", "tas").TopologyName("topo").Obj(),
			newRF: utiltestingapi.MakeResourceFlavor("rf").NodeLabel("node-group", "tas").Obj(),
			wantActive: map[kueue.ClusterQueueReference][]workload.Reference{
				cqName: {workload.NewReference("default", "inadmissible-wl")},
			},
			wantTASCachePresent: false,
		},
		"unset topologyName on terminating ResourceFlavor clears TAS cache and requests reconcile": {
			oldRF:         utiltestingapi.MakeResourceFlavor("rf").NodeLabel("node-group", "tas").TopologyName("topo").DeletionTimestamp(time.Now()).Obj(),
			newRF:         utiltestingapi.MakeResourceFlavor("rf").NodeLabel("node-group", "tas").DeletionTimestamp(time.Now()).Obj(),
			wantReconcile: true,
			wantActive: map[kueue.ClusterQueueReference][]workload.Reference{
				cqName: {workload.NewReference("default", "inadmissible-wl")},
			},
			wantTASCachePresent: false,
		},
		"set topologyName retries inadmissible workloads and populates TAS cache": {
			oldRF: utiltestingapi.MakeResourceFlavor("rf").NodeLabel("node-group", "tas").Obj(),
			newRF: utiltestingapi.MakeResourceFlavor("rf").NodeLabel("node-group", "tas").TopologyName("topo").Obj(),
			wantActive: map[kueue.ClusterQueueReference][]workload.Reference{
				cqName: {workload.NewReference("default", "inadmissible-wl")},
			},
			wantTASCachePresent: true,
		},
		"unrelated label update does not retry inadmissible workloads": {
			oldRF: utiltestingapi.MakeResourceFlavor("rf").NodeLabel("node-group", "tas").TopologyName("topo").Obj(),
			newRF: utiltestingapi.MakeResourceFlavor("rf").NodeLabel("node-group", "tas").TopologyName("topo").Label("updated", "true").Obj(),
			wantInadmissible: map[kueue.ClusterQueueReference][]workload.Reference{
				cqName: {workload.NewReference("default", "inadmissible-wl")},
			},
			wantTASCachePresent: true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, log := utiltesting.ContextWithLog(t)
			wl := utiltestingapi.MakeWorkload("inadmissible-wl", "default").Queue(lqName).Obj()
			cl := utiltesting.NewClientBuilder().
				WithIndex(&corev1.LimitRange{}, indexer.LimitRangeHasContainerOrPodType, indexer.IndexLimitRangeHasContainerOrPodType).
				WithObjects(utiltesting.MakeNamespace("default"), wl).
				Build()
			cqCache := schdcache.New(cl)
			cqCache.AddOrUpdateTopology(log, utiltestingapi.MakeDefaultOneLevelTopology("topo"))
			cqCache.AddOrUpdateResourceFlavor(log, tc.oldRF.DeepCopy())

			cq := utiltestingapi.MakeClusterQueue(string(cqName)).
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("rf").Resource(corev1.ResourceCPU, "10").Obj()).
				Obj()
			if err := cqCache.AddClusterQueue(ctx, cq); err != nil {
				t.Fatalf("adding ClusterQueue to cache: %v", err)
			}

			qManager, requeuer := qcache.NewManagerForUnitTestsWithRequeuer(cl, cqCache,
				qcache.WithPreemptionExpectations(preemptexpectations.New()),
			)
			if err := qManager.AddClusterQueue(ctx, cq); err != nil {
				t.Fatalf("adding ClusterQueue to queue manager: %v", err)
			}
			lq := utiltestingapi.MakeLocalQueue(string(lqName), "default").ClusterQueue(string(cqName)).Obj()
			if err := qManager.AddLocalQueue(ctx, lq); err != nil {
				t.Fatalf("adding LocalQueue: %v", err)
			}
			if err := qManager.AddOrUpdateWorkload(ctx, log, wl); err != nil {
				t.Fatalf("adding Workload: %v", err)
			}
			requeuer.ProcessRequeues(ctx)
			heads := qManager.Heads(ctx)
			if len(heads) != 1 {
				t.Fatalf("expected 1 head before requeue, got %d", len(heads))
			}
			if !qManager.RequeueWorkload(ctx, &heads[0].Info, qcache.RequeueReasonNoFit, qcache.QuotaReservedReason(kueue.WorkloadQuotaReservedReasonNoMatchingFlavor)) {
				t.Fatal("expected RequeueWorkload to move workload to inadmissible")
			}

			r := NewResourceFlavorReconciler(cl, qManager, cqCache, roletracker.NewFakeRoleTracker(roletracker.RoleLeader))
			if gotReconcile := r.Update(event.TypedUpdateEvent[*kueue.ResourceFlavor]{ObjectOld: tc.oldRF.DeepCopy(), ObjectNew: tc.newRF.DeepCopy()}); gotReconcile != tc.wantReconcile {
				t.Errorf("Update() = %t, want %t", gotReconcile, tc.wantReconcile)
			}
			requeuer.ProcessRequeues(ctx)

			if diff := cmp.Diff(tc.wantActive, qManager.Dump(), cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Unexpected active workloads (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantInadmissible, qManager.DumpInadmissible(), cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Unexpected inadmissible workloads (-want +got):\n%s", diff)
			}
			if got := cqCache.TASCache().Get("rf") != nil; got != tc.wantTASCachePresent {
				t.Errorf("TASCache().Get(rf) != nil is %t, want %t", got, tc.wantTASCachePresent)
			}
		})
	}
}

func TestResourceFlavorUpdateBroadcastsOnCQActivation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const (
			cqName = kueue.ClusterQueueReference("cq")
			lqName = kueue.LocalQueueName("lq")
		)

		ctx, log := utiltesting.ContextWithLog(t)
		wl := utiltestingapi.MakeWorkload("pending-wl", "default").Queue(lqName).Obj()
		cl := utiltesting.NewClientBuilder().
			WithIndex(&corev1.LimitRange{}, indexer.LimitRangeHasContainerOrPodType, indexer.IndexLimitRangeHasContainerOrPodType).
			WithObjects(utiltesting.MakeNamespace("default"), wl).
			Build()
		cqCache := schdcache.New(cl)

		oldRF := utiltestingapi.MakeResourceFlavor("rf").NodeLabel("node-group", "tas").TopologyName("missing-topo").Obj()
		cqCache.AddOrUpdateResourceFlavor(log, oldRF)

		cq := utiltestingapi.MakeClusterQueue(string(cqName)).
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("rf").Resource(corev1.ResourceCPU, "10").Obj()).
			Obj()
		if err := cqCache.AddClusterQueue(ctx, cq); err != nil {
			t.Fatalf("adding ClusterQueue to cache: %v", err)
		}

		qManager, _ := qcache.NewManagerForUnitTestsWithRequeuer(cl, cqCache,
			qcache.WithPreemptionExpectations(preemptexpectations.New()),
		)
		if err := qManager.AddClusterQueue(ctx, cq); err != nil {
			t.Fatalf("adding ClusterQueue to queue manager: %v", err)
		}
		lq := utiltestingapi.MakeLocalQueue(string(lqName), "default").ClusterQueue(string(cqName)).Obj()
		if err := qManager.AddLocalQueue(ctx, lq); err != nil {
			t.Fatalf("adding LocalQueue: %v", err)
		}
		if err := qManager.AddOrUpdateWorkload(ctx, log, wl); err != nil {
			t.Fatalf("adding Workload: %v", err)
		}

		headsCtx, cancelHeads := context.WithCancel(ctx)
		defer func() {
			cancelHeads()
			qManager.Broadcast()
		}()
		headsDone := make(chan []qcache.Head, 1)
		go func() {
			headsDone <- qManager.Heads(headsCtx)
		}()
		synctest.Wait()

		r := NewResourceFlavorReconciler(cl, qManager, cqCache, roletracker.NewFakeRoleTracker(roletracker.RoleLeader))
		r.Update(event.TypedUpdateEvent[*kueue.ResourceFlavor]{
			ObjectOld: oldRF,
			ObjectNew: utiltestingapi.MakeResourceFlavor("rf").NodeLabel("node-group", "tas").Obj(),
		})
		synctest.Wait()

		select {
		case gotHeads := <-headsDone:
			wantKey := workload.NewReference("default", "pending-wl")
			if len(gotHeads) != 1 || workload.Key(gotHeads[0].Obj) != wantKey {
				t.Fatalf("Heads after Broadcast returned %v, want %s", gotHeads, wantKey)
			}
		default:
			t.Fatal("expected Broadcast on CQ activation to wake parked Heads")
		}

		if cqCache.TASCache().Get("rf") != nil {
			t.Error("expected TASCache().Get(rf) == nil after unsetting missing topologyName")
		}
	})
}
