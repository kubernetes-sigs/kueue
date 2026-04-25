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
	"slices"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

// mockRFWatcher records watcher calls for test assertions.
type mockRFWatcher struct {
	calls []struct{ old, new *kueue.ResourceFlavor }
}

func (m *mockRFWatcher) NotifyResourceFlavorUpdate(old, new *kueue.ResourceFlavor) {
	m.calls = append(m.calls, struct{ old, new *kueue.ResourceFlavor }{old, new})
}

// mockQueue collects enqueued reconcile requests.
type mockQueue struct {
	items []reconcile.Request
}

func (q *mockQueue) Add(item reconcile.Request)     { q.items = append(q.items, item) }
func (q *mockQueue) Len() int                       { return len(q.items) }
func (q *mockQueue) Get() (reconcile.Request, bool) { return reconcile.Request{}, false }
func (q *mockQueue) Done(reconcile.Request)         {}
func (q *mockQueue) ShutDown()                      {}
func (q *mockQueue) ShutDownWithDrain()             {}
func (q *mockQueue) ShuttingDown() bool             { return false }
func (q *mockQueue) AddAfter(item reconcile.Request, _ time.Duration) {
	q.items = append(q.items, item)
}
func (q *mockQueue) AddRateLimited(item reconcile.Request) { q.items = append(q.items, item) }
func (q *mockQueue) Forget(reconcile.Request)              {}
func (q *mockQueue) NumRequeues(reconcile.Request) int     { return 0 }

var _ workqueue.TypedRateLimitingInterface[reconcile.Request] = (*mockQueue)(nil)

func newTestRFReconciler(cl *fake.ClientBuilder) *ResourceFlavorReconciler {
	c := cl.Build()
	cqCache := schdcache.New(c)
	qManager := qcache.NewManagerForUnitTests(c, nil)
	tracker := roletracker.NewFakeRoleTracker(roletracker.RoleLeader)
	return NewResourceFlavorReconciler(c, qManager, cqCache, tracker)
}

func TestResourceFlavorPredicates(t *testing.T) {
	flavor := utiltestingapi.MakeResourceFlavor("test-flavor").Obj()
	deletedFlavor := utiltestingapi.MakeResourceFlavor("test-flavor").Obj()
	deletedFlavor.DeletionTimestamp = &metav1.Time{Time: time.Now()}

	cases := map[string]struct {
		eventType string
		oldRF     *kueue.ResourceFlavor
		newRF     *kueue.ResourceFlavor
		wantBool  bool
		wantOld   *kueue.ResourceFlavor
		wantNew   *kueue.ResourceFlavor
	}{
		"create returns true and notifies with nil old": {
			eventType: "create",
			newRF:     flavor,
			wantBool:  true,
			wantOld:   nil,
			wantNew:   flavor,
		},
		"delete returns false and notifies with nil new": {
			eventType: "delete",
			oldRF:     flavor,
			wantBool:  false,
			wantOld:   flavor,
			wantNew:   nil,
		},
		"update without deletion timestamp returns false": {
			eventType: "update",
			oldRF:     flavor,
			newRF:     flavor,
			wantBool:  false,
			wantOld:   flavor,
			wantNew:   flavor,
		},
		"update with deletion timestamp returns true": {
			eventType: "update",
			oldRF:     flavor,
			newRF:     deletedFlavor,
			wantBool:  true,
			wantOld:   flavor,
			wantNew:   deletedFlavor,
		},
		"generic returns true without notifying": {
			eventType: "generic",
			newRF:     flavor,
			wantBool:  true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			reconciler := newTestRFReconciler(utiltesting.NewClientBuilder())
			watcher := &mockRFWatcher{}
			reconciler.AddUpdateWatcher(watcher)

			var got bool
			switch tc.eventType {
			case "create":
				got = reconciler.Create(event.TypedCreateEvent[*kueue.ResourceFlavor]{Object: tc.newRF})
			case "delete":
				got = reconciler.Delete(event.TypedDeleteEvent[*kueue.ResourceFlavor]{Object: tc.oldRF})
			case "update":
				got = reconciler.Update(event.TypedUpdateEvent[*kueue.ResourceFlavor]{
					ObjectOld: tc.oldRF,
					ObjectNew: tc.newRF,
				})
			case "generic":
				got = reconciler.Generic(event.TypedGenericEvent[*kueue.ResourceFlavor]{Object: tc.newRF})
			}

			if got != tc.wantBool {
				t.Errorf("predicate returned %v, want %v", got, tc.wantBool)
			}

			if tc.eventType == "generic" {
				if len(watcher.calls) != 0 {
					t.Errorf("generic event should not notify watchers, got %d calls", len(watcher.calls))
				}
				return
			}

			if len(watcher.calls) != 1 {
				t.Fatalf("expected 1 watcher call, got %d", len(watcher.calls))
			}
			if watcher.calls[0].old != tc.wantOld {
				t.Errorf("watcher old: got %v, want %v", watcher.calls[0].old, tc.wantOld)
			}
			if watcher.calls[0].new != tc.wantNew {
				t.Errorf("watcher new: got %v, want %v", watcher.calls[0].new, tc.wantNew)
			}
		})
	}
}

func TestResourceFlavorReconcile(t *testing.T) {
	cases := map[string]struct {
		setupFlavor   func() *kueue.ResourceFlavor
		setupCache    func(ctx context.Context, cache *schdcache.Cache)
		skipStore     bool
		wantFinalizer bool
		wantDeleted   bool
		wantError     bool
	}{
		"adds finalizer when missing": {
			setupFlavor:   func() *kueue.ResourceFlavor { return utiltestingapi.MakeResourceFlavor("flavor").Obj() },
			wantFinalizer: true,
		},
		"no-op when finalizer already present": {
			setupFlavor: func() *kueue.ResourceFlavor {
				f := utiltestingapi.MakeResourceFlavor("flavor").Obj()
				f.Finalizers = []string{kueue.ResourceInUseFinalizerName}
				return f
			},
			wantFinalizer: true,
		},
		"removes finalizer when deleted and not in use": {
			setupFlavor: func() *kueue.ResourceFlavor {
				f := utiltestingapi.MakeResourceFlavor("flavor").Obj()
				f.Finalizers = []string{kueue.ResourceInUseFinalizerName}
				return f
			},
			// The test will call cl.Delete to trigger the deletion flow.
			wantDeleted: true,
		},
		"keeps finalizer when deleted but still in use": {
			setupFlavor: func() *kueue.ResourceFlavor {
				f := utiltestingapi.MakeResourceFlavor("flavor").Obj()
				f.Finalizers = []string{kueue.ResourceInUseFinalizerName}
				return f
			},
			setupCache: func(ctx context.Context, cache *schdcache.Cache) {
				cq := utiltestingapi.MakeClusterQueue("cq-using-flavor").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("flavor").
						Resource(corev1.ResourceCPU, "10").
						Obj()).
					Obj()
				if err := cache.AddClusterQueue(ctx, cq); err != nil {
					panic("failed to add ClusterQueue to cache: " + err.Error())
				}
			},
			wantFinalizer: true,
		},
		"ignores not-found flavor": {
			setupFlavor: func() *kueue.ResourceFlavor { return utiltestingapi.MakeResourceFlavor("flavor").Obj() },
			skipStore:   true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)

			flavor := tc.setupFlavor()
			builder := utiltesting.NewClientBuilder()
			if !tc.skipStore {
				builder = builder.WithObjects(flavor)
			}
			cl := builder.Build()

			cqCache := schdcache.New(cl)
			qManager := qcache.NewManagerForUnitTests(cl, nil)
			tracker := roletracker.NewFakeRoleTracker(roletracker.RoleLeader)
			reconciler := NewResourceFlavorReconciler(cl, qManager, cqCache, tracker)

			if tc.setupCache != nil {
				tc.setupCache(ctx, cqCache)
			}

			if tc.wantDeleted {
				// Trigger Kubernetes deletion flow (sets DeletionTimestamp, keeps finalizer).
				if err := cl.Delete(ctx, flavor); err != nil {
					t.Fatalf("failed to delete ResourceFlavor: %v", err)
				}
			}

			req := reconcile.Request{NamespacedName: types.NamespacedName{Name: flavor.Name}}
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
			getErr := cl.Get(ctx, types.NamespacedName{Name: flavor.Name}, got)

			if tc.wantDeleted {
				if !apierrors.IsNotFound(getErr) {
					t.Errorf("expected ResourceFlavor to be deleted, got: %v", getErr)
				}
				return
			}

			if getErr != nil {
				t.Fatalf("failed to get ResourceFlavor: %v", getErr)
			}

			hasFinalizer := slices.Contains(got.Finalizers, kueue.ResourceInUseFinalizerName)
			if hasFinalizer != tc.wantFinalizer {
				t.Errorf("finalizer present: got %v, want %v", hasFinalizer, tc.wantFinalizer)
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
		wantChannel bool
	}{
		"create event (oldCQ nil) does not send": {
			oldCQ:       nil,
			newCQ:       makeCQWithFlavors("cq", "flavor-a"),
			wantChannel: false,
		},
		"delete event (newCQ nil) sends old CQ to channel": {
			oldCQ:       makeCQWithFlavors("cq", "flavor-a"),
			newCQ:       nil,
			wantChannel: true,
		},
		"update with same flavors does not send": {
			oldCQ:       makeCQWithFlavors("cq", "flavor-a"),
			newCQ:       makeCQWithFlavors("cq", "flavor-a"),
			wantChannel: false,
		},
		"update with added flavor sends": {
			oldCQ:       makeCQWithFlavors("cq", "flavor-a"),
			newCQ:       makeCQWithFlavors("cq", "flavor-a", "flavor-b"),
			wantChannel: true,
		},
		"update with removed flavor sends": {
			oldCQ:       makeCQWithFlavors("cq", "flavor-a", "flavor-b"),
			newCQ:       makeCQWithFlavors("cq", "flavor-a"),
			wantChannel: true,
		},
		"update with replaced flavor sends": {
			oldCQ:       makeCQWithFlavors("cq", "flavor-a"),
			newCQ:       makeCQWithFlavors("cq", "flavor-b"),
			wantChannel: true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			reconciler := newTestRFReconciler(utiltesting.NewClientBuilder())

			reconciler.NotifyClusterQueueUpdate(tc.oldCQ, tc.newCQ)

			select {
			case <-reconciler.cqUpdateCh:
				if !tc.wantChannel {
					t.Error("unexpected event sent to channel")
				}
			default:
				if tc.wantChannel {
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
		cq           *kueue.ClusterQueue
		cacheSetup   func(ctx context.Context, cache *schdcache.Cache)
		wantEnqueued []string
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
			cacheSetup: func(ctx context.Context, cache *schdcache.Cache) {
				other := makeCQWithFlavors("other-cq", "flavor-shared")
				if err := cache.AddClusterQueue(ctx, other); err != nil {
					panic("failed to add ClusterQueue: " + err.Error())
				}
			},
			wantEnqueued: nil,
		},
		"enqueues only orphaned flavors when CQ has multiple": {
			cq: makeCQWithFlavors("cq", "flavor-orphan", "flavor-shared"),
			cacheSetup: func(ctx context.Context, cache *schdcache.Cache) {
				other := makeCQWithFlavors("other-cq", "flavor-shared")
				if err := cache.AddClusterQueue(ctx, other); err != nil {
					panic("failed to add ClusterQueue: " + err.Error())
				}
			},
			wantEnqueued: []string{"flavor-orphan"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			cl := utiltesting.NewClientBuilder().Build()
			cqCache := schdcache.New(cl)

			if tc.cacheSetup != nil {
				tc.cacheSetup(ctx, cqCache)
			}

			h := &cqHandler{cache: cqCache}
			q := &mockQueue{}
			h.Generic(ctx, event.GenericEvent{Object: tc.cq}, q)

			var gotNames []string
			for _, r := range q.items {
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

func TestResourceFlavors(t *testing.T) {
	makeCQ := func(groups ...[]string) *kueue.ClusterQueue {
		w := utiltestingapi.MakeClusterQueue("cq")
		for _, flavors := range groups {
			fqs := make([]kueue.FlavorQuotas, len(flavors))
			for i, f := range flavors {
				fqs[i] = *utiltestingapi.MakeFlavorQuotas(f).Resource(corev1.ResourceCPU, "10").Obj()
			}
			w = w.ResourceGroup(fqs...)
		}
		return w.Obj()
	}

	cases := map[string]struct {
		cq   *kueue.ClusterQueue
		want []kueue.ResourceFlavorReference
	}{
		"empty resource groups": {
			cq:   utiltestingapi.MakeClusterQueue("cq").Obj(),
			want: nil,
		},
		"single group single flavor": {
			cq:   makeCQ([]string{"flavor-a"}),
			want: []kueue.ResourceFlavorReference{"flavor-a"},
		},
		"single group multiple flavors": {
			cq:   makeCQ([]string{"flavor-a", "flavor-b"}),
			want: []kueue.ResourceFlavorReference{"flavor-a", "flavor-b"},
		},
		"multiple groups": {
			cq:   makeCQ([]string{"flavor-a"}, []string{"flavor-b"}),
			want: []kueue.ResourceFlavorReference{"flavor-a", "flavor-b"},
		},
		"duplicate flavor across groups": {
			cq:   makeCQ([]string{"flavor-a"}, []string{"flavor-a"}),
			want: []kueue.ResourceFlavorReference{"flavor-a"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := resourceFlavors(tc.cq)
			gotSlice := got.UnsortedList()

			if diff := cmp.Diff(tc.want, gotSlice,
				cmpopts.SortSlices(func(a, b kueue.ResourceFlavorReference) bool { return a < b }),
				cmpopts.EquateEmpty(),
			); diff != "" {
				t.Errorf("resourceFlavors mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestAddUpdateWatcher(t *testing.T) {
	reconciler := newTestRFReconciler(utiltesting.NewClientBuilder())

	w1 := &mockRFWatcher{}
	w2 := &mockRFWatcher{}
	reconciler.AddUpdateWatcher(w1, w2)

	flavor := utiltestingapi.MakeResourceFlavor("test").Obj()
	reconciler.Create(event.TypedCreateEvent[*kueue.ResourceFlavor]{Object: flavor})

	if len(w1.calls) != 1 {
		t.Errorf("w1: expected 1 call, got %d", len(w1.calls))
	}
	if len(w2.calls) != 1 {
		t.Errorf("w2: expected 1 call, got %d", len(w2.calls))
	}
}
