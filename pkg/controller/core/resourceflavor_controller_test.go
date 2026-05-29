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
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"go.uber.org/mock/gomock"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	mockscore "sigs.k8s.io/kueue/internal/mocks/controller/core"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
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

			req := reconcile.Request{NamespacedName: types.NamespacedName{Name: tc.flavor.Name}}
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
			cq:   makeCQ(),
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
			got := sets.List(resourceFlavors(tc.cq))

			if diff := cmp.Diff(tc.want, got,
				cmpopts.EquateEmpty(),
			); diff != "" {
				t.Errorf("resourceFlavors mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
