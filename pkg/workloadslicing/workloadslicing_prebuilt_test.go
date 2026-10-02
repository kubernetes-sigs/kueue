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

package workloadslicing_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	testingclock "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	workloadfinish "sigs.k8s.io/kueue/pkg/workload/finish"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
)

func TestFinishReplacedWorkloadSlices(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.WorkloadRequestUseMergePatch, true)
	now := time.Now().Truncate(time.Second)
	old := utiltestingapi.MakeWorkload("old", "ns").Creation(now.Add(-time.Minute)).
		PodSets(*utiltestingapi.MakePodSet("main", 1).Obj()).
		ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").Obj(), now).AdmittedAt(true, now)
	replacement := utiltestingapi.MakeWorkload("new", "ns").Creation(now).
		Annotation(kueue.WorkloadSliceNameAnnotation, "old").
		Annotation(workloadslicing.WorkloadSliceReplacementFor, "ns/old").
		PodSets(*utiltestingapi.MakePodSet("main", 2).Obj())
	admitted := replacement.Clone().ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").Obj(), now).AdmittedAt(true, now)
	listError := errors.New("list failed")
	conflict := apierrors.NewConflict(schema.GroupResource{Group: kueue.SchemeGroupVersion.Group, Resource: "workloads"}, "old", errors.New("stale resource version"))
	tests := map[string]struct {
		workloads    []*kueue.Workload
		wantFinished bool
		listError    error
		finishError  error
	}{
		"no slices":                               {},
		"single slice":                            {workloads: []*kueue.Workload{old.Obj()}},
		"admitted ownerless replacement":          {workloads: []*kueue.Workload{old.Obj(), admitted.Obj()}, wantFinished: true},
		"pending replacement retains predecessor": {workloads: []*kueue.Workload{old.Obj(), replacement.Obj()}},
		"quota reservation is sufficient before full admission": {
			workloads:    []*kueue.Workload{old.Obj(), replacement.Clone().ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").Obj(), now).Obj()},
			wantFinished: true,
		},
		"multiple pending generations are preserved": {
			workloads: []*kueue.Workload{old.Obj(), replacement.Obj(), replacement.Clone().Name("newer").
				Annotation(workloadslicing.WorkloadSliceReplacementFor, "ns/new").Creation(now.Add(time.Minute)).Obj()},
		},
		"reserved slice without replacement relationship is preserved": {
			workloads: []*kueue.Workload{old.Obj(), utiltestingapi.MakeWorkload("unlinked", "ns").
				Annotation(kueue.WorkloadSliceNameAnnotation, "old").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").Obj(), now).Obj()},
		},
		"finished replacement preserves evidence after releasing quota": {
			workloads: []*kueue.Workload{old.Obj(), replacement.Clone().
				Condition(metav1.Condition{Type: kueue.WorkloadFinished, Status: metav1.ConditionTrue, Reason: kueue.WorkloadSliceReplaced, LastTransitionTime: metav1.NewTime(now)}).Obj(),
				admitted.Clone().Name("newer").Annotation(workloadslicing.WorkloadSliceReplacementFor, "ns/new").Obj()},
			wantFinished: true,
		},
		"failed replacement is not evidence": {
			workloads: []*kueue.Workload{old.Obj(), admitted.Clone().
				Condition(metav1.Condition{Type: kueue.WorkloadFinished, Status: metav1.ConditionTrue, Reason: kueue.WorkloadFinishedReasonFailed, LastTransitionTime: metav1.NewTime(now)}).Obj()},
		},
		"self reference does not finish a slice": {
			workloads: []*kueue.Workload{old.Clone().Annotation(workloadslicing.WorkloadSliceReplacementFor, "ns/old").Obj()},
		},
		"already finished predecessor": {
			workloads: []*kueue.Workload{
				old.Clone().Condition(metav1.Condition{Type: kueue.WorkloadFinished, Status: metav1.ConditionTrue, Reason: kueue.WorkloadSliceReplaced, LastTransitionTime: metav1.NewTime(now)}).Obj(),
				admitted.Obj(),
			},
			wantFinished: true,
		},
		"evicted predecessor retained until replacement admitted": {workloads: []*kueue.Workload{old.Clone().EvictedAt(now).Obj(), replacement.Obj()}},
		"quota-only replacement takes over evicted predecessor": {
			workloads:    []*kueue.Workload{old.Clone().EvictedAt(now).Obj(), replacement.Clone().ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").Obj(), now).Obj()},
			wantFinished: true,
		},
		"evicted replacement does not take over":              {workloads: []*kueue.Workload{old.Clone().EvictedAt(now).Obj(), admitted.Clone().EvictedAt(now).Obj()}},
		"admitted replacement takes over evicted predecessor": {workloads: []*kueue.Workload{old.Clone().EvictedAt(now).Obj(), admitted.Obj()}, wantFinished: true},
		"evicted predecessor without quota can finish": {
			workloads:    []*kueue.Workload{utiltestingapi.MakeWorkload("old", "ns").Creation(now.Add(-time.Minute)).EvictedAt(now).Obj(), admitted.Obj()},
			wantFinished: true,
		},
		"list error": {listError: listError},
		"finish conflict is returned and retry recovers": {workloads: []*kueue.Workload{old.Obj(), admitted.Obj()}, finishError: conflict, wantFinished: true},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			objects := []client.Object{
				utiltestingapi.MakeWorkload("unrelated", "ns").Obj(),
				utiltestingapi.MakeWorkload("other-namespace", "other").Annotation(kueue.WorkloadSliceNameAnnotation, "old").Obj(),
			}
			for _, wl := range tc.workloads {
				objects = append(objects, wl.DeepCopy())
			}
			injected := tc.finishError
			cl := utiltesting.NewClientBuilder().WithObjects(objects...).WithStatusSubresource(&kueue.Workload{}).
				WithIndex(&kueue.Workload{}, indexer.WorkloadSliceNameKey, indexer.IndexWorkloadSliceName).
				WithInterceptorFuncs(interceptor.Funcs{
					List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
						if tc.listError != nil {
							return tc.listError
						}
						return c.List(ctx, list, opts...)
					},
					SubResourcePatch: func(ctx context.Context, c client.Client, sub string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
						if injected != nil {
							err := injected
							injected = nil
							return err
						}
						return c.SubResource(sub).Patch(ctx, obj, patch, opts...)
					},
				}).Build()
			clk := testingclock.NewFakeClock(now)
			err := workloadslicing.FinishReplacedWorkloadSlices(ctx, cl, clk, old.Obj())
			wantErr := tc.listError
			if tc.finishError != nil {
				wantErr = tc.finishError
			}
			if !errors.Is(err, wantErr) {
				t.Fatalf("error = %v, want %v", err, wantErr)
			}
			if tc.listError != nil {
				return
			}
			if tc.finishError != nil {
				got := &kueue.Workload{}
				if err := cl.Get(ctx, client.ObjectKeyFromObject(old.Obj()), got); err != nil {
					t.Fatal(err)
				}
				if workloadfinish.IsFinished(got) {
					t.Fatal("failed finish changed workload status")
				}
			}
			// A retry after an error, and repeated successful calls, must both be safe.
			for range 2 {
				if err := workloadslicing.FinishReplacedWorkloadSlices(ctx, cl, clk, old.Obj()); err != nil {
					t.Fatal(err)
				}
			}
			list := &kueue.WorkloadList{}
			if err := cl.List(ctx, list); err != nil {
				t.Fatal(err)
			}
			if len(list.Items) != len(objects) {
				t.Fatalf("workload count = %d, want %d", len(list.Items), len(objects))
			}
			for _, before := range objects {
				want := before.(*kueue.Workload)
				got := &kueue.Workload{}
				if err := cl.Get(ctx, client.ObjectKeyFromObject(want), got); err != nil {
					t.Fatal(err)
				}
				if diff := cmp.Diff(want.Spec, got.Spec, cmpopts.EquateEmpty()); diff != "" {
					t.Errorf("%s spec changed (-want,+got): %s", got.Name, diff)
				}
				if diff := cmp.Diff(want.OwnerReferences, got.OwnerReferences); diff != "" {
					t.Errorf("ownership changed: %s", diff)
				}
				finished := workloadfinish.IsFinished(want) || (got.Namespace == "ns" && got.Name == "old" && tc.wantFinished)
				if workloadfinish.IsFinished(got) != finished {
					t.Errorf("%s: Finished = %v, want %v", got.Name, workloadfinish.IsFinished(got), finished)
				}
				if got.Name == "old" && tc.wantFinished && !workloadslicing.IsReplaced(got.Status) {
					t.Errorf("predecessor was not finished with WorkloadSliceReplaced: %v", got.Status.Conditions)
				}
				if !finished || workloadfinish.IsFinished(want) {
					if diff := cmp.Diff(want.Status, got.Status, cmpopts.EquateEmpty()); diff != "" {
						t.Errorf("%s status changed unexpectedly: %s", got.Name, diff)
					}
				}
			}
		})
	}
}
