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

package jobframework

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/google/go-cmp/cmp"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
)

// priorityStats is counted through the interceptors, for the cases that care how
// often the class was read or a workload written.
type priorityStats struct {
	classReads     int
	workloadWrites int
}

func TestUpdateWorkloadPriority(t *testing.T) {
	// One invocation of the helper, with the change to the cluster that only
	// means something between two of them.
	type step struct {
		before  func(t *testing.T, ctx context.Context, cl client.Client)
		wantErr bool
	}
	// A nil field is not asserted.
	type wantWorkload struct {
		refName  *string
		priority *int32
	}

	cases := map[string]struct {
		class        *kueue.WorkloadPriorityClass
		workloads    []*kueue.Workload
		interceptors func(s *priorityStats) interceptor.Funcs
		steps        []step
		want         map[string]wantWorkload
		// Nil where the count is not part of what the case pins.
		wantClassReads     *int
		wantWorkloadWrites *int
		wantDistinctRefs   bool
	}{
		// The workloads of one job are written one after another, so a class edited
		// in between could otherwise give each of them a different value, and later
		// reconciles would not repair that because they compare the class name only.
		"resolves the class once for the whole set": {
			class: utiltestingapi.MakeWorkloadPriorityClass("high").PriorityValue(100).Obj(),
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload("first", "ns").WorkloadPriorityClassRef("low").Priority(10).Obj(),
				utiltestingapi.MakeWorkload("second", "ns").WorkloadPriorityClassRef("low").Priority(10).Obj(),
			},
			// Every read reports a different value, standing in for an administrator
			// editing the class while the workloads are being written.
			interceptors: func(s *priorityStats) interceptor.Funcs {
				return interceptor.Funcs{
					Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
						if err := c.Get(ctx, key, obj, opts...); err != nil {
							return err
						}
						if class, ok := obj.(*kueue.WorkloadPriorityClass); ok && class.Name == "high" {
							s.classReads++
							class.Value = int32(100 * s.classReads)
						}
						return nil
					},
				}
			},
			steps: []step{{}},
			want: map[string]wantWorkload{
				"first":  {refName: new("high"), priority: new(int32(100))},
				"second": {refName: new("high"), priority: new(int32(100))},
			},
			wantClassReads:   new(1),
			wantDistinctRefs: true,
		},

		// A quota-reserved workload with no priorityClassRef must be left untouched
		// on the ordinary (non-slice) path too: the API server refuses to add a ref
		// once quota is reserved, so attempting it would wedge the reconcile. The
		// fake client does not run those CEL rules, so losing the guard shows up
		// here as a write that should not have happened.
		"leaves a quota-reserved workload with no priority class alone": {
			class: utiltestingapi.MakeWorkloadPriorityClass("high").PriorityValue(100).Obj(),
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload("reserved", "ns").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "AdmittedByTest",
						Message:            "reserved",
						LastTransitionTime: metav1.Now(),
					}).Obj(),
			},
			interceptors: countingWrites,
			steps:        []step{{}},
			want: map[string]wantWorkload{
				"reserved": {refName: new("")},
			},
			wantWorkloadWrites: new(0),
		},

		// A workload that already carries the right class name but a stale value is
		// only repaired if the comparison looks past the name.
		"converges a workload that already names the class but holds a stale value": {
			class: utiltestingapi.MakeWorkloadPriorityClass("high").PriorityValue(200).Obj(),
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload("first", "ns").WorkloadPriorityClassRef("high").Priority(100).Obj(),
				utiltestingapi.MakeWorkload("second", "ns").WorkloadPriorityClassRef("low").Priority(10).Obj(),
			},
			interceptors: countingClassReads,
			steps:        []step{{}},
			want: map[string]wantWorkload{
				"first":  {refName: new("high"), priority: new(int32(200))},
				"second": {refName: new("high"), priority: new(int32(200))},
			},
			wantClassReads: new(1),
		},

		// The split state is reachable from a real partial write: the first workload
		// persists at the old value, the second fails, the class value then changes,
		// and the retry must still bring both to the new value.
		"converges after a conflict when the class value changes before the retry": {
			class: utiltestingapi.MakeWorkloadPriorityClass("high").PriorityValue(100).Obj(),
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload("first", "ns").WorkloadPriorityClassRef("low").Priority(10).Obj(),
				utiltestingapi.MakeWorkload("second", "ns").WorkloadPriorityClassRef("low").Priority(10).Obj(),
			},
			interceptors: func(s *priorityStats) interceptor.Funcs {
				failSecondOnce := true
				return interceptor.Funcs{
					Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
						if wl, ok := obj.(*kueue.Workload); ok && wl.Name == "second" && failSecondOnce {
							failSecondOnce = false
							return errors.New("simulated conflict")
						}
						return c.Update(ctx, obj, opts...)
					},
				}
			},
			steps: []step{
				{wantErr: true},
				{before: raiseClassTo(200)},
			},
			want: map[string]wantWorkload{
				"first":  {refName: new("high"), priority: new(int32(200))},
				"second": {refName: new("high"), priority: new(int32(200))},
			},
		},

		// Same-name workloads are repaired before the name-changing ones, so a failure
		// among them leaves a name mismatch behind for the next call to find. The
		// values disagreeing would say so too, but the order keeps the cheaper signal.
		"keeps a retry marker when a write fails": {
			class: utiltestingapi.MakeWorkloadPriorityClass("high").PriorityValue(200).Obj(),
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload("stale", "ns").WorkloadPriorityClassRef("high").Priority(100).Obj(),
				utiltestingapi.MakeWorkload("transitioning", "ns").WorkloadPriorityClassRef("low").Priority(10).Obj(),
			},
			// Fails whichever workload is written second, so the case does not depend
			// on the order it exists to pin.
			interceptors: func(s *priorityStats) interceptor.Funcs {
				return interceptor.Funcs{
					Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
						if _, ok := obj.(*kueue.Workload); ok {
							s.workloadWrites++
							if s.workloadWrites == 2 {
								return errors.New("simulated conflict")
							}
						}
						return c.Update(ctx, obj, opts...)
					},
				}
			},
			steps: []step{{wantErr: true}, {}},
			want: map[string]wantWorkload{
				"stale":         {priority: new(int32(200))},
				"transitioning": {priority: new(int32(200))},
			},
		},

		// One workload the API server will not take is its own problem. Handing the
		// set to one helper should not turn it into everyone's.
		"a same-class write that keeps failing does not hold back a transition": {
			class: utiltestingapi.MakeWorkloadPriorityClass("high").PriorityValue(200).Obj(),
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload("stuck", "ns").WorkloadPriorityClassRef("high").Priority(100).Obj(),
				utiltestingapi.MakeWorkload("moving", "ns").WorkloadPriorityClassRef("low").Priority(10).Obj(),
			},
			interceptors: func(*priorityStats) interceptor.Funcs {
				return interceptor.Funcs{
					Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
						if wl, ok := obj.(*kueue.Workload); ok && wl.Name == "stuck" {
							return errors.New("simulated rejection")
						}
						return c.Update(ctx, obj, opts...)
					},
				}
			},
			steps: []step{{wantErr: true}},
			want: map[string]wantWorkload{
				"stuck":  {refName: new("high"), priority: new(int32(100))},
				"moving": {refName: new("high"), priority: new(int32(200))},
			},
		},

		// Once the names match, a partial write leaves nothing for a name-driven
		// retry to notice. The workloads disagreeing with each other is what says
		// the write did not finish.
		"converges after a partial write leaves no name to change": {
			class: utiltestingapi.MakeWorkloadPriorityClass("high").PriorityValue(200).Obj(),
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload("stale", "ns").WorkloadPriorityClassRef("high").Priority(100).Obj(),
				utiltestingapi.MakeWorkload("other", "ns").WorkloadPriorityClassRef("high").Priority(100).Obj(),
				utiltestingapi.MakeWorkload("moving", "ns").WorkloadPriorityClassRef("low").Priority(10).Obj(),
			},
			interceptors: func(*priorityStats) interceptor.Funcs {
				failStaleOnce := true
				return interceptor.Funcs{
					Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
						if wl, ok := obj.(*kueue.Workload); ok && wl.Name == "stale" && failStaleOnce {
							failStaleOnce = false
							return errors.New("simulated conflict")
						}
						return c.Update(ctx, obj, opts...)
					},
				}
			},
			steps: []step{{wantErr: true}, {}},
			want: map[string]wantWorkload{
				"stale":  {refName: new("high"), priority: new(int32(200))},
				"other":  {refName: new("high"), priority: new(int32(200))},
				"moving": {refName: new("high"), priority: new(int32(200))},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			job := testingjob.MakeJob("job", "ns").WorkloadPriorityClass("high").Obj()

			objs := []client.Object{job, tc.class}
			names := make([]string, 0, len(tc.workloads))
			for _, wl := range tc.workloads {
				objs = append(objs, wl)
				names = append(names, wl.Name)
			}

			s := &priorityStats{}
			builder := utiltesting.NewClientBuilder(batchv1.AddToScheme, kueue.AddToScheme).WithObjects(objs...)
			if tc.interceptors != nil {
				builder = builder.WithInterceptorFuncs(tc.interceptors(s))
			}
			cl := builder.Build()

			// Read afresh before every invocation, the way a reconcile would, so a
			// regression that only updates the in-memory copies cannot pass.
			readLive := func() []*kueue.Workload {
				out := make([]*kueue.Workload, 0, len(names))
				for _, n := range names {
					wl := &kueue.Workload{}
					if err := cl.Get(ctx, types.NamespacedName{Namespace: "ns", Name: n}, wl); err != nil {
						t.Fatalf("getting workload %s: %v", n, err)
					}
					out = append(out, wl)
				}
				return out
			}

			var live []*kueue.Workload
			for i, st := range tc.steps {
				if st.before != nil {
					st.before(t, ctx, cl)
				}
				live = readLive()
				err := UpdateWorkloadPriority(ctx, cl, &utiltesting.EventRecorder{}, job, nil, live...)
				switch {
				case st.wantErr && err == nil:
					t.Fatalf("invocation %d: want an error, got none", i)
				case !st.wantErr && err != nil:
					t.Fatalf("invocation %d: %v", i, err)
				}
			}

			for n, want := range tc.want {
				persisted := &kueue.Workload{}
				if err := cl.Get(ctx, types.NamespacedName{Namespace: "ns", Name: n}, persisted); err != nil {
					t.Fatalf("re-getting workload %s: %v", n, err)
				}
				if want.refName != nil {
					got := ""
					if persisted.Spec.PriorityClassRef != nil {
						got = persisted.Spec.PriorityClassRef.Name
					}
					if got != *want.refName {
						t.Errorf("%s priorityClassRef name = %q, want %q", n, got, *want.refName)
					}
				}
				if want.priority != nil {
					if persisted.Spec.Priority == nil || *persisted.Spec.Priority != *want.priority {
						t.Errorf("%s priority = %v, want %d", n, persisted.Spec.Priority, *want.priority)
					}
				}
			}

			if tc.wantClassReads != nil && s.classReads != *tc.wantClassReads {
				t.Errorf("class reads = %d, want %d", s.classReads, *tc.wantClassReads)
			}
			if tc.wantWorkloadWrites != nil && s.workloadWrites != *tc.wantWorkloadWrites {
				t.Errorf("workload writes = %d, want %d", s.workloadWrites, *tc.wantWorkloadWrites)
			}
			if tc.wantDistinctRefs && live[0].Spec.PriorityClassRef == live[1].Spec.PriorityClassRef {
				t.Error("both workloads point at one priorityClassRef, so editing either would move both")
			}
		})
	}
}

// countingClassReads counts how often the priority class is read, for the cases
// asserting that it is resolved once for the whole set.
func countingClassReads(s *priorityStats) interceptor.Funcs {
	return interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if err := c.Get(ctx, key, obj, opts...); err != nil {
				return err
			}
			if class, ok := obj.(*kueue.WorkloadPriorityClass); ok && class.Name == "high" {
				s.classReads++
			}
			return nil
		},
	}
}

// countingWrites counts workload writes, for the cases asserting that a workload
// was left alone.
func countingWrites(s *priorityStats) interceptor.Funcs {
	return interceptor.Funcs{
		Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			if _, ok := obj.(*kueue.Workload); ok {
				s.workloadWrites++
			}
			return c.Update(ctx, obj, opts...)
		},
	}
}

// raiseClassTo changes the class value between two invocations.
func raiseClassTo(value int32) func(t *testing.T, ctx context.Context, cl client.Client) {
	return func(t *testing.T, ctx context.Context, cl client.Client) {
		class := &kueue.WorkloadPriorityClass{}
		if err := cl.Get(ctx, types.NamespacedName{Name: "high"}, class); err != nil {
			t.Fatalf("getting class: %v", err)
		}
		class.Value = value
		if err := cl.Update(ctx, class); err != nil {
			t.Fatalf("updating class: %v", err)
		}
	}
}

// TestApplyWorkloadPriorityLeavesUnlabelledWorkloadsAlone pins the boundary of
// the stale-value repair. With no class named, every workload without a
// reference classifies as naming the same (empty) class, and rewriting those
// would take back a value that nothing here owns.
func TestApplyWorkloadPriorityLeavesUnlabelledWorkloadsAlone(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)
	job := testingjob.MakeJob("job", "ns").Obj()
	wl := utiltestingapi.MakeWorkload("wl", "ns").Priority(1000).Obj()
	var writes int
	cl := utiltesting.NewClientBuilder().WithObjects(wl).
		WithInterceptorFuncs(interceptor.Funcs{
			Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
				writes++
				return c.Update(ctx, obj, opts...)
			},
		}).Build()

	if err := ApplyWorkloadPriority(ctx, cl, &utiltesting.EventRecorder{}, job, nil, 0, wl); err != nil {
		t.Fatalf("ApplyWorkloadPriority() = %v", err)
	}

	var got kueue.Workload
	if err := cl.Get(ctx, client.ObjectKeyFromObject(wl), &got); err != nil {
		t.Fatalf("reading the workload back: %v", err)
	}
	if got.Spec.Priority == nil || *got.Spec.Priority != 1000 {
		t.Errorf("workload priority = %d, want it left at 1000", ptr.Deref(got.Spec.Priority, 0))
	}
	if got.Spec.PriorityClassRef != nil {
		t.Errorf("workload gained a priority class reference: %v", got.Spec.PriorityClassRef)
	}
	if writes != 0 {
		t.Errorf("wrote the workload %d times, want none", writes)
	}
}

// TestApplyWorkloadPriorityAttemptsEverySibling pins the failure boundary. One
// workload that will not take the write used to end the batch, leaving the rest
// waiting on it for as long as it kept failing.
func TestApplyWorkloadPriorityAttemptsEverySibling(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)
	job := testingjob.MakeJob("job", "ns").WorkloadPriorityClass("high").Obj()
	first := utiltestingapi.MakeWorkload("first", "ns").WorkloadPriorityClassRef("high").Priority(100).Obj()
	second := utiltestingapi.MakeWorkload("second", "ns").WorkloadPriorityClassRef("high").Priority(100).Obj()

	errRefused := errors.New("the write was refused")
	cl := utiltesting.NewClientBuilder().
		WithObjects(first, second).
		WithInterceptorFuncs(interceptor.Funcs{
			Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
				if obj.GetName() == "first" {
					return errRefused
				}
				return c.Update(ctx, obj, opts...)
			},
		}).Build()

	err := ApplyWorkloadPriority(ctx, cl, &utiltesting.EventRecorder{}, job,
		kueue.NewWorkloadPriorityClassRef("high"), 200, first, second)
	if !errors.Is(err, errRefused) {
		t.Fatalf("ApplyWorkloadPriority() = %v, want the refused write reported", err)
	}

	var got kueue.Workload
	if err := cl.Get(ctx, client.ObjectKey{Namespace: "ns", Name: "second"}, &got); err != nil {
		t.Fatalf("reading the sibling back: %v", err)
	}
	if diff := cmp.Diff(new(int32(200)), got.Spec.Priority); diff != "" {
		t.Errorf("the sibling was not attempted (-want +got):\n%s", diff)
	}
}

// TestApplyWorkloadPriorityRepairsAStaleValue is the other half of the
// unlabelled case: with a class named, a workload already on it whose value was
// left behind takes the supplied one.
func TestApplyWorkloadPriorityRepairsAStaleValue(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)
	job := testingjob.MakeJob("job", "ns").WorkloadPriorityClass("high").Obj()
	wl := utiltestingapi.MakeWorkload("wl", "ns").WorkloadPriorityClassRef("high").Priority(100).Obj()
	cl := utiltesting.NewClientBuilder().WithObjects(wl).Build()

	ref := kueue.NewWorkloadPriorityClassRef("high")
	if err := ApplyWorkloadPriority(ctx, cl, &utiltesting.EventRecorder{}, job, ref, 200, wl); err != nil {
		t.Fatalf("ApplyWorkloadPriority() = %v", err)
	}

	var got kueue.Workload
	if err := cl.Get(ctx, client.ObjectKeyFromObject(wl), &got); err != nil {
		t.Fatalf("reading the workload back: %v", err)
	}
	if diff := cmp.Diff(ref, got.Spec.PriorityClassRef); diff != "" {
		t.Errorf("priority class reference (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(new(int32(200)), got.Spec.Priority); diff != "" {
		t.Errorf("priority (-want +got):\n%s", diff)
	}
}

// TestApplyWorkloadPriorityStopsWhenCancelled pins that the batch does not
// carry on issuing writes after the reconcile is over. Each component used to
// write from its own worker, which checked for cancellation before starting.
func TestApplyWorkloadPriorityStopsWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	job := testingjob.MakeJob("job", "ns").WorkloadPriorityClass("high").Obj()

	wls := make([]*kueue.Workload, 0, 20)
	objs := make([]client.Object, 0, 20)
	for i := range 20 {
		wl := utiltestingapi.MakeWorkload(fmt.Sprintf("wl-%d", i), "ns").
			WorkloadPriorityClassRef("high").Priority(100).Obj()
		wls = append(wls, wl)
		objs = append(objs, wl)
	}

	var writes atomic.Int32
	cl := utiltesting.NewClientBuilder().WithObjects(objs...).
		WithInterceptorFuncs(interceptor.Funcs{
			Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
				writes.Add(1)
				return c.Update(ctx, obj, opts...)
			},
		}).Build()

	cancel()
	_ = ApplyWorkloadPriority(ctx, cl, &utiltesting.EventRecorder{}, job,
		kueue.NewWorkloadPriorityClassRef("high"), 200, wls...)

	if got := writes.Load(); got != 0 {
		t.Errorf("wrote %d workloads after cancellation, want none", got)
	}
}
