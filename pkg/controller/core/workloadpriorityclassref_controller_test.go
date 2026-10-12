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

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestWorkloadPriorityClassRefReconcile(t *testing.T) {
	errTest := errors.New("test error")
	cases := map[string]struct {
		workload     *kueue.Workload
		clientFuncs  *interceptor.Funcs
		wantWorkload *kueue.Workload
		wantError    error
	}{
		"reconcile gives a workload with another value the class value": {
			workload: utiltestingapi.MakeWorkload("wl", "default").
				WorkloadPriorityClassRef("high").
				Priority(100).
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "default").
				ResourceVersion("1000").
				WorkloadPriorityClassRef("high").
				Priority(1000).
				Obj(),
		},
		"reconcile does not write a workload that has the class value": {
			workload: utiltestingapi.MakeWorkload("wl", "default").
				WorkloadPriorityClassRef("high").
				Priority(1000).
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "default").
				ResourceVersion("999").
				WorkloadPriorityClassRef("high").
				Priority(1000).
				Obj(),
		},
		"reconcile leaves a MultiKueue remote workload alone": {
			workload: utiltestingapi.MakeWorkload("wl", "default").
				Label(kueue.MultiKueueOriginLabel, "origin").
				WorkloadPriorityClassRef("high").
				Priority(100).
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "default").
				ResourceVersion("999").
				Label(kueue.MultiKueueOriginLabel, "origin").
				WorkloadPriorityClassRef("high").
				Priority(100).
				Obj(),
		},
		"reconcile leaves a workload with a Pod PriorityClass of the same name alone": {
			workload: utiltestingapi.MakeWorkload("wl", "default").
				PodPriorityClassRef("high").
				Priority(100).
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "default").
				ResourceVersion("999").
				PodPriorityClassRef("high").
				Priority(100).
				Obj(),
		},
		"reconcile leaves a workload that references a missing class alone": {
			workload: utiltestingapi.MakeWorkload("wl", "default").
				WorkloadPriorityClassRef("missing").
				Priority(100).
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "default").
				ResourceVersion("999").
				WorkloadPriorityClassRef("missing").
				Priority(100).
				Obj(),
		},
		"reconcile writes the new value when the class changes while the workload is written": {
			workload: utiltestingapi.MakeWorkload("wl", "default").
				WorkloadPriorityClassRef("high").
				Priority(100).
				Obj(),
			clientFuncs: &interceptor.Funcs{
				// Before the write of 1000 lands, the class changes to 100 and its
				// reconcile skips the workload, which still holds 100.
				Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
					if wl, ok := obj.(*kueue.Workload); ok && ptr.Deref(wl.Spec.Priority, 0) == 1000 {
						wpc := &kueue.WorkloadPriorityClass{}
						if err := c.Get(ctx, client.ObjectKey{Name: "high"}, wpc); err != nil {
							return err
						}
						wpc.Value = 100
						if err := c.Update(ctx, wpc); err != nil {
							return err
						}
						if _, err := NewWorkloadPriorityClassReconciler(c, nil).Reconcile(ctx, reconcile.Request{Name: "high"}); err != nil {
							return err
						}
					}
					return c.Patch(ctx, obj, patch, opts...)
				},
			},
			wantWorkload: utiltestingapi.MakeWorkload("wl", "default").
				ResourceVersion("1001").
				WorkloadPriorityClassRef("high").
				Priority(100).
				Obj(),
		},
		"reconcile writes once when the stored value differs from the written one but the class does not change": {
			workload: utiltestingapi.MakeWorkload("wl", "default").
				WorkloadPriorityClassRef("high").
				Priority(100).
				Obj(),
			clientFuncs: &interceptor.Funcs{
				// A mutating webhook stores 500 instead; a second write fails.
				Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
					wl, ok := obj.(*kueue.Workload)
					if !ok {
						return c.Patch(ctx, obj, patch, opts...)
					}
					stored := &kueue.Workload{}
					if err := c.Get(ctx, client.ObjectKeyFromObject(wl), stored); err != nil {
						return err
					}
					if ptr.Deref(stored.Spec.Priority, 0) == 500 {
						return errTest
					}
					wl.Spec.Priority = new(int32(500))
					return c.Patch(ctx, wl, patch, opts...)
				},
			},
			wantWorkload: utiltestingapi.MakeWorkload("wl", "default").
				ResourceVersion("1000").
				WorkloadPriorityClassRef("high").
				Priority(500).
				Obj(),
		},
		"reconcile returns the error of a failed write": {
			workload: utiltestingapi.MakeWorkload("wl", "default").
				WorkloadPriorityClassRef("high").
				Priority(100).
				Obj(),
			clientFuncs: &interceptor.Funcs{
				Patch: func(context.Context, client.WithWatch, client.Object, client.Patch, ...client.PatchOption) error {
					return errTest
				},
			},
			wantWorkload: utiltestingapi.MakeWorkload("wl", "default").
				ResourceVersion("999").
				WorkloadPriorityClassRef("high").
				Priority(100).
				Obj(),
			wantError: errTest,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			builder := utiltesting.NewClientBuilder().
				WithObjects(utiltestingapi.MakeWorkloadPriorityClass("high").PriorityValue(1000).Obj(), tc.workload).
				WithIndex(&kueue.Workload{}, indexer.WorkloadPriorityClassKey, indexer.IndexWorkloadPriorityClass)
			if tc.clientFuncs != nil {
				builder = builder.WithInterceptorFuncs(*tc.clientFuncs)
			}
			k8sClient := builder.Build()

			reconciler := newWorkloadPriorityClassRefReconciler(k8sClient, nil)
			_, gotError := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(tc.workload)})
			if diff := cmp.Diff(tc.wantError, gotError, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Unexpected error (-want/+got):\n%s", diff)
			}

			gotWorkload := &kueue.Workload{}
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(tc.workload), gotWorkload); err != nil {
				t.Fatalf("Failed to get workload: %v", err)
			}
			if diff := cmp.Diff(tc.wantWorkload, gotWorkload, cmpopts.EquateEmpty(), cmpopts.IgnoreFields(kueue.Workload{}, "TypeMeta")); diff != "" {
				t.Errorf("Unexpected workload (-want/+got):\n%s", diff)
			}
		})
	}
}

func TestWorkloadPriorityClassRefPredicates(t *testing.T) {
	cases := map[string]struct {
		eventType   string
		oldWorkload *kueue.Workload
		newWorkload *kueue.Workload
		want        bool
	}{
		"create event of a workload referencing a WorkloadPriorityClass triggers reconcile": {
			eventType: "create",
			newWorkload: utiltestingapi.MakeWorkload("wl", "default").
				WorkloadPriorityClassRef("high").
				Priority(100).
				Obj(),
			want: true,
		},
		"create event of a workload referencing a Pod PriorityClass does not trigger reconcile": {
			eventType: "create",
			newWorkload: utiltestingapi.MakeWorkload("wl", "default").
				PodPriorityClassRef("high").
				Priority(100).
				Obj(),
			want: false,
		},
		"update event that moves a workload to a WorkloadPriorityClass triggers reconcile": {
			eventType: "update",
			oldWorkload: utiltestingapi.MakeWorkload("wl", "default").
				WorkloadPriorityClassRef("low").
				Priority(10).
				Obj(),
			newWorkload: utiltestingapi.MakeWorkload("wl", "default").
				WorkloadPriorityClassRef("high").
				Priority(10).
				Obj(),
			want: true,
		},
		"update event that replaces a workload under the same name triggers reconcile": {
			eventType: "update",
			oldWorkload: utiltestingapi.MakeWorkload("wl", "default").
				UID("old-uid").
				WorkloadPriorityClassRef("high").
				Priority(100).
				Obj(),
			newWorkload: utiltestingapi.MakeWorkload("wl", "default").
				UID("new-uid").
				WorkloadPriorityClassRef("high").
				Priority(100).
				Obj(),
			want: true,
		},
		"update event that changes only the priority does not trigger reconcile": {
			eventType: "update",
			oldWorkload: utiltestingapi.MakeWorkload("wl", "default").
				WorkloadPriorityClassRef("high").
				Priority(1000).
				Obj(),
			newWorkload: utiltestingapi.MakeWorkload("wl", "default").
				WorkloadPriorityClassRef("high").
				Priority(100).
				Obj(),
			want: false,
		},
		"delete event does not trigger reconcile": {
			eventType: "delete",
			oldWorkload: utiltestingapi.MakeWorkload("wl", "default").
				WorkloadPriorityClassRef("high").
				Priority(100).
				Obj(),
			want: false,
		},
		"generic event does not trigger reconcile": {
			eventType: "generic",
			newWorkload: utiltestingapi.MakeWorkload("wl", "default").
				WorkloadPriorityClassRef("high").
				Priority(100).
				Obj(),
			want: false,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			reconciler := newWorkloadPriorityClassRefReconciler(nil, nil)
			var got bool

			switch tc.eventType {
			case "create":
				got = reconciler.Create(event.TypedCreateEvent[*kueue.Workload]{Object: tc.newWorkload})
			case "update":
				got = reconciler.Update(event.TypedUpdateEvent[*kueue.Workload]{
					ObjectOld: tc.oldWorkload,
					ObjectNew: tc.newWorkload,
				})
			case "delete":
				got = reconciler.Delete(event.TypedDeleteEvent[*kueue.Workload]{Object: tc.oldWorkload})
			case "generic":
				got = reconciler.Generic(event.TypedGenericEvent[*kueue.Workload]{Object: tc.newWorkload})
			}

			if got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}
