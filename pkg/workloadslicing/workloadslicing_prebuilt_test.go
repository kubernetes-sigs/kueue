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
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	testingclock "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
)

func TestFinishReplacedWorkloadSlices(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	admission := utiltestingapi.MakeAdmission("cq").Obj()
	old := utiltestingapi.MakeWorkload("old", "ns").
		PodSets(*utiltestingapi.MakePodSet("main", 1).Obj()).
		ReserveQuotaAt(admission, now).AdmittedAt(true, now)
	pending := utiltestingapi.MakeWorkload("new", "ns").
		Annotation(kueue.WorkloadSliceNameAnnotation, "old").
		Annotation(workloadslicing.WorkloadSliceReplacementFor, "ns/old").
		PodSets(*utiltestingapi.MakePodSet("main", 1).Obj())
	admitted := pending.Clone().ReserveQuotaAt(admission, now).AdmittedAt(true, now)
	finished := func(reason string) metav1.Condition {
		return metav1.Condition{Type: kueue.WorkloadFinished, Status: metav1.ConditionTrue, Reason: reason, LastTransitionTime: metav1.NewTime(now)}
	}

	tests := map[string]struct {
		workloads    []*kueue.Workload
		wantFinished []string
	}{
		"no slices":    {},
		"single slice": {workloads: []*kueue.Workload{old.Obj()}},
		"admitted replacement finishes the predecessor": {
			workloads:    []*kueue.Workload{old.Obj(), admitted.Obj()},
			wantFinished: []string{"old"},
		},
		"quota reservation is sufficient before full admission": {
			workloads:    []*kueue.Workload{old.Obj(), pending.Clone().ReserveQuotaAt(admission, now).Obj()},
			wantFinished: []string{"old"},
		},
		"evicted predecessor with admitted replacement": {
			workloads:    []*kueue.Workload{old.Clone().EvictedAt(now).Obj(), admitted.Obj()},
			wantFinished: []string{"old"},
		},
		"every replaced slice in a chain is finished": {
			workloads: []*kueue.Workload{
				old.Obj(),
				admitted.Clone().Name("mid").Obj(),
				admitted.Clone().Name("last").
					Annotation(workloadslicing.WorkloadSliceReplacementFor, "ns/mid").Obj(),
			},
			wantFinished: []string{"old", "mid"},
		},
		"pending replacement retains the predecessor": {
			workloads: []*kueue.Workload{old.Obj(), pending.Obj()},
		},
		"evicted replacement is not evidence": {
			workloads: []*kueue.Workload{old.Obj(), admitted.Clone().EvictedAt(now).Obj()},
		},
		"finished replacement is not evidence": {
			workloads: []*kueue.Workload{old.Obj(), admitted.Clone().Condition(finished(kueue.WorkloadFinishedReasonFailed)).Obj()},
		},
		"already finished predecessor is left untouched": {
			workloads: []*kueue.Workload{old.Clone().Condition(finished(kueue.WorkloadSliceReplaced)).Obj(), admitted.Obj()},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			var objects []client.Object
			for _, wl := range tc.workloads {
				objects = append(objects, wl.DeepCopy())
			}
			cl := utiltesting.NewClientBuilder().WithObjects(objects...).WithStatusSubresource(&kueue.Workload{}).
				WithIndex(&kueue.Workload{}, indexer.WorkloadSliceNameKey, indexer.IndexWorkloadSliceName).
				WithInterceptorFuncs(interceptor.Funcs{SubResourceApply: utiltesting.TreatSSAAsStrategicMergeForApplyConfiguration}).
				Build()
			clk := testingclock.NewFakeClock(now)

			if err := workloadslicing.FinishReplacedWorkloadSlices(ctx, cl, clk, pending.Obj()); err != nil {
				t.Fatal(err)
			}

			wantFinished := sets.New(tc.wantFinished...)
			for _, o := range objects {
				want := o.(*kueue.Workload)
				got := &kueue.Workload{}
				if err := cl.Get(ctx, client.ObjectKeyFromObject(want), got); err != nil {
					t.Fatal(err)
				}
				if diff := cmp.Diff(want.Spec, got.Spec, cmpopts.EquateEmpty()); diff != "" {
					t.Errorf("%s spec changed (-want,+got): %s", got.Name, diff)
				}
				if wantFinished.Has(got.Name) {
					if !workloadslicing.IsReplaced(got.Status) {
						t.Errorf("%s was not finished with WorkloadSliceReplaced: %v", got.Name, got.Status.Conditions)
					}
					continue
				}
				if diff := cmp.Diff(want.Status, got.Status, cmpopts.EquateEmpty()); diff != "" {
					t.Errorf("%s status changed unexpectedly (-want,+got): %s", got.Name, diff)
				}
			}
		})
	}
}
