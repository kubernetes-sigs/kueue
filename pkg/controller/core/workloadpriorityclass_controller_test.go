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
		"reconcile leaves a Workload MultiKueue created here alone": {
			wpc: utiltestingapi.MakeWorkloadPriorityClass("high").PriorityValue(1000).Obj(),
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("remote", "default").
					Priority(100).
					WorkloadPriorityClassRef("high").
					Label(kueue.MultiKueueOriginLabel, "manager").
					Obj(),
				*utiltestingapi.MakeWorkload("local", "default").
					Priority(100).
					WorkloadPriorityClassRef("high").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("local", "default").
					Priority(1000).
					WorkloadPriorityClassRef("high").
					Obj(),
				*utiltestingapi.MakeWorkload("remote", "default").
					Priority(100).
					WorkloadPriorityClassRef("high").
					Label(kueue.MultiKueueOriginLabel, "manager").
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
		// MultiKueue copies the manager's resolution onto the remote Workload,
		// and a class of the same name here is not the one it came from.
		"created by MultiKueue on this cluster": {
			eventType: "create",
			newWL: utiltestingapi.MakeWorkload("wl", "ns").WorkloadPriorityClassRef("high").
				Label(kueue.MultiKueueOriginLabel, "manager").Obj(),
			want: false,
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
		"reference added on a Workload MultiKueue created here": {
			eventType: "update",
			oldWL:     utiltestingapi.MakeWorkload("wl", "ns").Label(kueue.MultiKueueOriginLabel, "manager").Obj(),
			newWL: utiltestingapi.MakeWorkload("wl", "ns").WorkloadPriorityClassRef("high").
				Label(kueue.MultiKueueOriginLabel, "manager").Obj(),
			want: false,
		},
		// The manager resolved the value this Workload carries. Once the label
		// saying so is gone, this cluster's class is what it has to follow, and
		// the reference did not have to move for that to be true.
		"ownership moved here with the reference unchanged": {
			eventType: "update",
			oldWL: utiltestingapi.MakeWorkload("wl", "ns").WorkloadPriorityClassRef("high").
				Label(kueue.MultiKueueOriginLabel, "manager").Obj(),
			newWL: utiltestingapi.MakeWorkload("wl", "ns").WorkloadPriorityClassRef("high").Obj(),
			want:  true,
		},
		"ownership moved away with the reference unchanged": {
			eventType: "update",
			oldWL:     utiltestingapi.MakeWorkload("wl", "ns").WorkloadPriorityClassRef("high").Obj(),
			newWL: utiltestingapi.MakeWorkload("wl", "ns").WorkloadPriorityClassRef("high").
				Label(kueue.MultiKueueOriginLabel, "manager").Obj(),
			want: false,
		},
		"ownership moved here on a Pod PriorityClass reference": {
			eventType: "update",
			oldWL: utiltestingapi.MakeWorkload("wl", "ns").PodPriorityClassRef("high").
				Label(kueue.MultiKueueOriginLabel, "manager").Obj(),
			newWL: utiltestingapi.MakeWorkload("wl", "ns").PodPriorityClassRef("high").Obj(),
			want:  false,
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

func TestWorkloadPriorityClassReferenceReconcile(t *testing.T) {
	cases := map[string]struct {
		workload     *kueue.Workload
		class        *kueue.WorkloadPriorityClass
		wantPriority *int32
		wantWrites   int
	}{
		"a value left behind by an earlier reconcile": {
			workload:     utiltestingapi.MakeWorkload("wl", "ns").WorkloadPriorityClassRef("high").Priority(100).Obj(),
			class:        utiltestingapi.MakeWorkloadPriorityClass("high").PriorityValue(200).Obj(),
			wantPriority: new(int32(200)),
			wantWrites:   1,
		},
		// A reference can arrive before anything has filled the priority, so
		// unset is a state to resolve rather than a value to leave alone.
		"a reference with nothing in the priority yet": {
			workload:     utiltestingapi.MakeWorkload("wl", "ns").WorkloadPriorityClassRef("high").Obj(),
			class:        utiltestingapi.MakeWorkloadPriorityClass("high").PriorityValue(200).Obj(),
			wantPriority: new(int32(200)),
			wantWrites:   1,
		},
		// The rule this pins: the class wins over a value the user chose. A
		// Workload created already referencing a class is resolved from it at
		// once, where before this the value stood until the class next moved.
		// A later numeric-only update is left alone, so an override is still
		// available, just not in the same request as the reference.
		"a value the user chose, against the class it references": {
			workload:     utiltestingapi.MakeWorkload("wl", "ns").WorkloadPriorityClassRef("high").Priority(123).Obj(),
			class:        utiltestingapi.MakeWorkloadPriorityClass("high").PriorityValue(200).Obj(),
			wantPriority: new(int32(200)),
			wantWrites:   1,
		},
		"a value already in step with the class": {
			workload:     utiltestingapi.MakeWorkload("wl", "ns").WorkloadPriorityClassRef("high").Priority(200).Obj(),
			class:        utiltestingapi.MakeWorkloadPriorityClass("high").PriorityValue(200).Obj(),
			wantPriority: new(int32(200)),
		},
		// The class sweeps what references it when it is created.
		"a class that does not exist yet": {
			workload:     utiltestingapi.MakeWorkload("wl", "ns").WorkloadPriorityClassRef("high").Priority(100).Obj(),
			wantPriority: new(int32(100)),
		},
		"a Workload MultiKueue created here": {
			workload: utiltestingapi.MakeWorkload("wl", "ns").WorkloadPriorityClassRef("high").Priority(100).
				Label(kueue.MultiKueueOriginLabel, "manager").Obj(),
			class:        utiltestingapi.MakeWorkloadPriorityClass("high").PriorityValue(200).Obj(),
			wantPriority: new(int32(100)),
		},
		"a reference to a Pod PriorityClass": {
			workload:     utiltestingapi.MakeWorkload("wl", "ns").PodPriorityClassRef("high").Priority(100).Obj(),
			class:        utiltestingapi.MakeWorkloadPriorityClass("high").PriorityValue(200).Obj(),
			wantPriority: new(int32(100)),
		},
		"no reference at all": {
			workload:     utiltestingapi.MakeWorkload("wl", "ns").Priority(100).Obj(),
			class:        utiltestingapi.MakeWorkloadPriorityClass("high").PriorityValue(200).Obj(),
			wantPriority: new(int32(100)),
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			var writes, workloadLists int
			objs := []client.Object{tc.workload}
			if tc.class != nil {
				objs = append(objs, tc.class)
			}
			cl := utiltesting.NewClientBuilder().
				WithObjects(objs...).
				WithInterceptorFuncs(interceptor.Funcs{
					// Counted whichever verb carries it, so the count says how
					// often the workload was written rather than which call did it.
					Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
						writes++
						return c.Update(ctx, obj, opts...)
					},
					Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
						writes++
						return c.Patch(ctx, obj, patch, opts...)
					},
					List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
						if _, isWorkloads := list.(*kueue.WorkloadList); isWorkloads {
							workloadLists++
						}
						return c.List(ctx, list, opts...)
					},
				}).Build()

			r := NewWorkloadPriorityClassReferenceReconciler(cl, cl, nil)
			if _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(tc.workload)}); err != nil {
				t.Fatalf("Reconcile() = %v", err)
			}

			var got kueue.Workload
			if err := cl.Get(ctx, client.ObjectKeyFromObject(tc.workload), &got); err != nil {
				t.Fatalf("reading the workload back: %v", err)
			}
			if diff := cmp.Diff(tc.wantPriority, got.Spec.Priority); diff != "" {
				t.Errorf("workload priority (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantWrites, writes); diff != "" {
				t.Errorf("workload writes (-want +got):\n%s", diff)
			}
			// The whole point of keying on the Workload: one arriving at a
			// class must not cost a pass over every other Workload using it.
			if workloadLists != 0 {
				t.Errorf("listed workloads %d times, want none", workloadLists)
			}
		})
	}
}

// TestWorkloadPriorityClassValueSurvivesLateReference walks the ordering the
// watch exists for: the class update is reconciled while no Workload references
// the class yet, and the Workload then arrives carrying the value its own
// lookup returned before the change.
func TestWorkloadPriorityClassValueSurvivesLateReference(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)
	high := utiltestingapi.MakeWorkloadPriorityClass("high").PriorityValue(200).Obj()
	wl := utiltestingapi.MakeWorkload("wl", "ns").WorkloadPriorityClassRef("low").Priority(100).Obj()

	cl := utiltesting.NewClientBuilder().
		WithObjects(high, wl).
		WithIndex(&kueue.Workload{}, indexer.WorkloadPriorityClassKey, indexer.IndexWorkloadPriorityClass).
		Build()

	classReconciler := NewWorkloadPriorityClassReconciler(cl, nil)
	if _, err := classReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: "high"}}); err != nil {
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
	refReconciler := NewWorkloadPriorityClassReferenceReconciler(cl, cl, nil)
	if _, err := refReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(wl)}); err != nil {
		t.Fatalf("reconciling the workload: %v", err)
	}

	var got kueue.Workload
	if err := cl.Get(ctx, client.ObjectKeyFromObject(wl), &got); err != nil {
		t.Fatalf("reading the workload back: %v", err)
	}
	if diff := cmp.Diff(new(int32(200)), got.Spec.Priority); diff != "" {
		t.Errorf("workload priority (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(kueue.NewWorkloadPriorityClassRef("high"), got.Spec.PriorityClassRef); diff != "" {
		t.Errorf("workload priority class reference (-want +got):\n%s", diff)
	}
}
