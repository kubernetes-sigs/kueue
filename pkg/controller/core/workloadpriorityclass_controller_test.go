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
	stderrors "errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestWorkloadPriorityClassPredicates(t *testing.T) {
	cases := map[string]struct {
		eventType string
		oldWPC    *kueue.WorkloadPriorityClass
		newWPC    *kueue.WorkloadPriorityClass
		want      bool
	}{
		"create event should trigger reconcile": {
			eventType: "create",
			newWPC:    utiltestingapi.MakeWorkloadPriorityClass("test").PriorityValue(100).Obj(),
			want:      true,
		},
		"delete event should not trigger reconcile": {
			eventType: "delete",
			oldWPC:    utiltestingapi.MakeWorkloadPriorityClass("test").PriorityValue(100).Obj(),
			want:      false,
		},
		"update event with changed priority should trigger reconcile": {
			eventType: "update",
			oldWPC:    utiltestingapi.MakeWorkloadPriorityClass("test").PriorityValue(100).Obj(),
			newWPC:    utiltestingapi.MakeWorkloadPriorityClass("test").PriorityValue(200).Obj(),
			want:      true,
		},
		"update event with unchanged priority should not trigger reconcile": {
			eventType: "update",
			oldWPC:    utiltestingapi.MakeWorkloadPriorityClass("test").PriorityValue(100).Obj(),
			newWPC:    utiltestingapi.MakeWorkloadPriorityClass("test").PriorityValue(100).Obj(),
			want:      false,
		},
		"generic event should not trigger reconcile": {
			eventType: "generic",
			newWPC:    utiltestingapi.MakeWorkloadPriorityClass("test").PriorityValue(100).Obj(),
			want:      false,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			reconciler := NewWorkloadPriorityClassReconciler(nil, nil)
			var got bool

			switch tc.eventType {
			case "create":
				got = reconciler.Create(event.TypedCreateEvent[*kueue.WorkloadPriorityClass]{Object: tc.newWPC})
			case "delete":
				got = reconciler.Delete(event.TypedDeleteEvent[*kueue.WorkloadPriorityClass]{Object: tc.oldWPC})
			case "update":
				got = reconciler.Update(event.TypedUpdateEvent[*kueue.WorkloadPriorityClass]{
					ObjectOld: tc.oldWPC,
					ObjectNew: tc.newWPC,
				})
			case "generic":
				got = reconciler.Generic(event.TypedGenericEvent[*kueue.WorkloadPriorityClass]{Object: tc.newWPC})
			}

			if got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestWorkloadPriorityClassReconcile(t *testing.T) {
	cases := map[string]struct {
		wpc           *kueue.WorkloadPriorityClass
		workloads     []kueue.Workload
		wantWorkloads []kueue.Workload
		wantError     bool
		clientFuncs   *interceptor.Funcs
	}{
		"reconcile updates workload priority when WPC priority changes": {
			wpc: utiltestingapi.MakeWorkloadPriorityClass("high").PriorityValue(1000).Obj(),
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl1", "default").
					Priority(100).
					WorkloadPriorityClassRef("high").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl1", "default").
					Priority(1000).
					WorkloadPriorityClassRef("high").
					Obj(),
			},
		},
		"reconcile updates multiple workloads": {
			wpc: utiltestingapi.MakeWorkloadPriorityClass("high").PriorityValue(1000).Obj(),
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl1", "default").
					Priority(100).
					WorkloadPriorityClassRef("high").
					Obj(),
				*utiltestingapi.MakeWorkload("wl2", "default").
					Priority(200).
					WorkloadPriorityClassRef("high").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl1", "default").
					Priority(1000).
					WorkloadPriorityClassRef("high").
					Obj(),
				*utiltestingapi.MakeWorkload("wl2", "default").
					Priority(1000).
					WorkloadPriorityClassRef("high").
					Obj(),
			},
		},
		"reconcile skips workloads with up-to-date priority": {
			wpc: utiltestingapi.MakeWorkloadPriorityClass("high").PriorityValue(1000).Obj(),
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl1", "default").
					Priority(1000).
					WorkloadPriorityClassRef("high").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl1", "default").
					Priority(1000).
					WorkloadPriorityClassRef("high").
					Obj(),
			},
		},
		"reconcile succeeds when no workloads use the WPC": {
			wpc:           utiltestingapi.MakeWorkloadPriorityClass("high").PriorityValue(1000).Obj(),
			workloads:     []kueue.Workload{},
			wantWorkloads: []kueue.Workload{},
		},
		"reconcile handles workload not found error": {
			wpc: utiltestingapi.MakeWorkloadPriorityClass("high").PriorityValue(1000).Obj(),
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl1", "default").
					Priority(100).
					WorkloadPriorityClassRef("high").
					Obj(),
			},
			clientFuncs: &interceptor.Funcs{
				Update: func(ctx context.Context, client client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
					return errors.NewNotFound(kueue.Resource("workload"), "wl1")
				},
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl1", "default").
					Priority(100).
					WorkloadPriorityClassRef("high").
					Obj(),
			},
		},
		"reconcile returns error when update fails": {
			wpc: utiltestingapi.MakeWorkloadPriorityClass("high").PriorityValue(1000).Obj(),
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl1", "default").
					Priority(100).
					WorkloadPriorityClassRef("high").
					Obj(),
			},
			clientFuncs: &interceptor.Funcs{
				Update: func(ctx context.Context, client client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
					return stderrors.New("update failed")
				},
			},
			wantError: true,
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl1", "default").
					Priority(100).
					WorkloadPriorityClassRef("high").
					Obj(),
			},
		},
		"reconcile handles partial update failures": {
			wpc: utiltestingapi.MakeWorkloadPriorityClass("high").PriorityValue(1000).Obj(),
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl1", "default").
					Priority(100).
					WorkloadPriorityClassRef("high").
					Obj(),
				*utiltestingapi.MakeWorkload("wl2", "default").
					Priority(200).
					WorkloadPriorityClassRef("high").
					Obj(),
			},
			clientFuncs: &interceptor.Funcs{
				Update: func(ctx context.Context, client client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
					wl := obj.(*kueue.Workload)
					if wl.Name == "wl2" {
						return stderrors.New("update failed for wl2")
					}
					return client.Update(ctx, obj, opts...)
				},
			},
			wantError: true,
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl1", "default").
					Priority(1000).
					WorkloadPriorityClassRef("high").
					Obj(),
				*utiltestingapi.MakeWorkload("wl2", "default").
					Priority(200).
					WorkloadPriorityClassRef("high").
					Obj(),
			},
		},
		"reconcile handles WPC not found": {
			wpc:           utiltestingapi.MakeWorkloadPriorityClass("high").PriorityValue(1000).Obj(),
			workloads:     []kueue.Workload{},
			wantWorkloads: []kueue.Workload{},
			clientFuncs: &interceptor.Funcs{
				Get: func(ctx context.Context, client client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					return errors.NewNotFound(kueue.Resource("workloadpriorityclass"), key.Name)
				},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()

			builder := utiltesting.NewClientBuilder().
				WithObjects(tc.wpc).
				WithIndex(&kueue.Workload{}, indexer.WorkloadPriorityClassKey, indexer.IndexWorkloadPriorityClass).
				WithStatusSubresource(&kueue.Workload{})
			for i := range tc.workloads {
				builder = builder.WithObjects(&tc.workloads[i])
			}
			if tc.clientFuncs != nil {
				builder = builder.WithInterceptorFuncs(*tc.clientFuncs)
			}
			k8sClient := builder.Build()

			reconciler := NewWorkloadPriorityClassReconciler(k8sClient, nil)
			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name: tc.wpc.Name,
				},
			}

			_, gotErr := reconciler.Reconcile(ctx, req)

			if tc.wantError && gotErr == nil {
				t.Errorf("expected error but got nil")
			} else if !tc.wantError && gotErr != nil {
				t.Errorf("unexpected error: %v", gotErr)
			}
			// Verify workloads are in the expected state
			for _, wantWl := range tc.wantWorkloads {
				gotWl := &kueue.Workload{}
				err := k8sClient.Get(ctx, types.NamespacedName{Name: wantWl.Name, Namespace: wantWl.Namespace}, gotWl)
				if err != nil {
					t.Fatalf("failed to get workload %s: %v", wantWl.Name, err)
				}
				if diff := cmp.Diff(wantWl.Spec.Priority, gotWl.Spec.Priority); diff != "" {
					t.Errorf("workload %s priority mismatch (-want +got):\n%s", wantWl.Name, diff)
				}
			}
		})
	}
}

func TestWorkloadPriorityClassRefChangedPredicate(t *testing.T) {
	cases := map[string]struct {
		eventType string
		oldWL     *kueue.Workload
		newWL     *kueue.Workload
		want      bool
	}{
		"created already referencing a class": {
			eventType: "create",
			newWL:     utiltestingapi.MakeWorkload("wl", "ns").WorkloadPriorityClassRef("high").Obj(),
			want:      true,
		},
		"created referencing a Pod PriorityClass": {
			eventType: "create",
			newWL:     utiltestingapi.MakeWorkload("wl", "ns").PodPriorityClassRef("high").Obj(),
			want:      false,
		},
		"created referencing nothing": {
			eventType: "create",
			newWL:     utiltestingapi.MakeWorkload("wl", "ns").Obj(),
			want:      false,
		},
		"reference added": {
			eventType: "update",
			oldWL:     utiltestingapi.MakeWorkload("wl", "ns").Obj(),
			newWL:     utiltestingapi.MakeWorkload("wl", "ns").WorkloadPriorityClassRef("high").Obj(),
			want:      true,
		},
		// Only the group tells these apart, so a predicate comparing names alone
		// would call this unchanged and leave the class unreconciled.
		"moved from a Pod PriorityClass of the same name": {
			eventType: "update",
			oldWL:     utiltestingapi.MakeWorkload("wl", "ns").PodPriorityClassRef("high").Obj(),
			newWL:     utiltestingapi.MakeWorkload("wl", "ns").WorkloadPriorityClassRef("high").Obj(),
			want:      true,
		},
		"moved between classes": {
			eventType: "update",
			oldWL:     utiltestingapi.MakeWorkload("wl", "ns").WorkloadPriorityClassRef("low").Obj(),
			newWL:     utiltestingapi.MakeWorkload("wl", "ns").WorkloadPriorityClassRef("high").Obj(),
			want:      true,
		},
		// What Reconcile itself writes.
		"same class, priority value rewritten": {
			eventType: "update",
			oldWL:     utiltestingapi.MakeWorkload("wl", "ns").WorkloadPriorityClassRef("high").Priority(100).Obj(),
			newWL:     utiltestingapi.MakeWorkload("wl", "ns").WorkloadPriorityClassRef("high").Priority(200).Obj(),
			want:      false,
		},
		"same class, nothing changed": {
			eventType: "update",
			oldWL:     utiltestingapi.MakeWorkload("wl", "ns").WorkloadPriorityClassRef("high").Obj(),
			newWL:     utiltestingapi.MakeWorkload("wl", "ns").WorkloadPriorityClassRef("high").Obj(),
			want:      false,
		},
		"moved to a Pod PriorityClass": {
			eventType: "update",
			oldWL:     utiltestingapi.MakeWorkload("wl", "ns").WorkloadPriorityClassRef("high").Obj(),
			newWL:     utiltestingapi.MakeWorkload("wl", "ns").PodPriorityClassRef("high").Obj(),
			want:      false,
		},
		"deleted": {
			eventType: "delete",
			oldWL:     utiltestingapi.MakeWorkload("wl", "ns").WorkloadPriorityClassRef("high").Obj(),
			want:      false,
		},
		"generic": {
			eventType: "generic",
			newWL:     utiltestingapi.MakeWorkload("wl", "ns").WorkloadPriorityClassRef("high").Obj(),
			want:      false,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			p := workloadPriorityClassRefChanged()
			var got bool
			switch tc.eventType {
			case "create":
				got = p.Create(event.TypedCreateEvent[*kueue.Workload]{Object: tc.newWL})
			case "update":
				got = p.Update(event.TypedUpdateEvent[*kueue.Workload]{ObjectOld: tc.oldWL, ObjectNew: tc.newWL})
			case "delete":
				got = p.Delete(event.TypedDeleteEvent[*kueue.Workload]{Object: tc.oldWL})
			case "generic":
				got = p.Generic(event.TypedGenericEvent[*kueue.Workload]{Object: tc.newWL})
			default:
				t.Fatalf("unknown event type %q", tc.eventType)
			}
			if got != tc.want {
				t.Errorf("predicate = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestEnqueueReferencedWorkloadPriorityClass(t *testing.T) {
	cases := map[string]struct {
		eventType string
		oldWL     *kueue.Workload
		newWL     *kueue.Workload
		want      []reconcile.Request
	}{
		"created referencing a class": {
			eventType: "create",
			newWL:     utiltestingapi.MakeWorkload("wl", "ns").WorkloadPriorityClassRef("high").Obj(),
			want:      []reconcile.Request{{NamespacedName: types.NamespacedName{Name: "high"}}},
		},
		"created referencing a Pod PriorityClass": {
			eventType: "create",
			newWL:     utiltestingapi.MakeWorkload("wl", "ns").PodPriorityClassRef("high").Obj(),
		},
		// The class being left has nothing to repair, and a map function would
		// enqueue it too.
		"moved between classes": {
			eventType: "update",
			oldWL:     utiltestingapi.MakeWorkload("wl", "ns").WorkloadPriorityClassRef("low").Obj(),
			newWL:     utiltestingapi.MakeWorkload("wl", "ns").WorkloadPriorityClassRef("high").Obj(),
			want:      []reconcile.Request{{NamespacedName: types.NamespacedName{Name: "high"}}},
		},
		"moved to a Pod PriorityClass": {
			eventType: "update",
			oldWL:     utiltestingapi.MakeWorkload("wl", "ns").WorkloadPriorityClassRef("high").Obj(),
			newWL:     utiltestingapi.MakeWorkload("wl", "ns").PodPriorityClassRef("high").Obj(),
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			h := enqueueReferencedWorkloadPriorityClass()
			q := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]())
			defer q.ShutDown()

			switch tc.eventType {
			case "create":
				h.Create(t.Context(), event.TypedCreateEvent[*kueue.Workload]{Object: tc.newWL}, q)
			case "update":
				h.Update(t.Context(), event.TypedUpdateEvent[*kueue.Workload]{ObjectOld: tc.oldWL, ObjectNew: tc.newWL}, q)
			default:
				t.Fatalf("unknown event type %q", tc.eventType)
			}

			var got []reconcile.Request
			for q.Len() > 0 {
				req, _ := q.Get()
				got = append(got, req)
				q.Done(req)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("enqueued requests (-want +got):\n%s", diff)
			}
		})
	}
}

// TestWorkloadPriorityClassValueSurvivesLateReference walks the ordering the
// watch exists for: the class update is reconciled while no Workload references
// the class yet, and the Workload then arrives carrying the value its own
// lookup returned before the change.
func TestWorkloadPriorityClassValueSurvivesLateReference(t *testing.T) {
	ctx := t.Context()
	high := utiltestingapi.MakeWorkloadPriorityClass("high").PriorityValue(200).Obj()
	wl := utiltestingapi.MakeWorkload("wl", "ns").WorkloadPriorityClassRef("low").Priority(100).Obj()

	cl := utiltesting.NewClientBuilder().
		WithObjects(high, wl).
		WithIndex(&kueue.Workload{}, indexer.WorkloadPriorityClassKey, indexer.IndexWorkloadPriorityClass).
		Build()
	r := NewWorkloadPriorityClassReconciler(cl, nil)

	classReq := reconcile.Request{NamespacedName: types.NamespacedName{Name: "high"}}
	if _, err := r.Reconcile(ctx, classReq); err != nil {
		t.Fatalf("reconciling the class before it is referenced: %v", err)
	}

	oldWL := wl.DeepCopy()
	newWL := wl.DeepCopy()
	newWL.Spec.PriorityClassRef = kueue.NewWorkloadPriorityClassRef("high")
	if err := cl.Update(ctx, newWL); err != nil {
		t.Fatalf("pointing the workload at the class: %v", err)
	}

	if !workloadPriorityClassRefChanged().Update(event.TypedUpdateEvent[*kueue.Workload]{ObjectOld: oldWL, ObjectNew: newWL}) {
		t.Fatal("the reference change was filtered out, so nothing reaches the queue")
	}
	q := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]())
	defer q.ShutDown()
	enqueueReferencedWorkloadPriorityClass().Update(ctx, event.TypedUpdateEvent[*kueue.Workload]{ObjectOld: oldWL, ObjectNew: newWL}, q)
	for q.Len() > 0 {
		req, _ := q.Get()
		if _, err := r.Reconcile(ctx, req); err != nil {
			t.Fatalf("reconciling %v: %v", req, err)
		}
		q.Done(req)
	}

	var got kueue.Workload
	if err := cl.Get(ctx, client.ObjectKeyFromObject(wl), &got); err != nil {
		t.Fatalf("reading the workload back: %v", err)
	}
	if got.Spec.Priority == nil {
		t.Fatal("workload priority is unset, want 200")
	}
	if *got.Spec.Priority != 200 {
		t.Errorf("workload priority = %d, want 200", *got.Spec.Priority)
	}
}
