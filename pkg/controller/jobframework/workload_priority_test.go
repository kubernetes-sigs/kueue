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
	"testing"

	"github.com/google/go-cmp/cmp"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	schedulingv1 "k8s.io/api/scheduling/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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
		// The owner's kueue.x-k8s.io/priority-class label, "high" when unset and
		// left off entirely when empty.
		ownerClass   *string
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

		// A repair under a matching name has nothing of its own to bring a later
		// call back, so the transition waits: the name it has not changed yet is
		// what the next call finds.
		"a same-class write that keeps failing holds the transition back": {
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
				"moving": {refName: new("low"), priority: new(int32(10))},
			},
		},

		// spec.priority is mutable, so two workloads under one name disagreeing is
		// not on its own an unfinished write. WorkloadPriorityClassReconciler lists
		// by class name and holds the value that settles it; this helper is not
		// asked, and does not read the class to guess.
		"leaves values under a name that already matches the class": {
			class: utiltestingapi.MakeWorkloadPriorityClass("high").PriorityValue(500).Obj(),
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload("chosen", "ns").WorkloadPriorityClassRef("high").Priority(100).Obj(),
				utiltestingapi.MakeWorkload("other", "ns").WorkloadPriorityClassRef("high").Priority(200).Obj(),
			},
			interceptors: countingReadsAndWrites,
			steps:        []step{{}},
			want: map[string]wantWorkload{
				"chosen": {refName: new("high"), priority: new(int32(100))},
				"other":  {refName: new("high"), priority: new(int32(200))},
			},
			wantClassReads:     new(0),
			wantWorkloadWrites: new(0),
		},

		// Without the label every workload with no reference reads as naming the
		// same class, and the name that would be resolved is the empty one. Writing
		// on the strength of that takes back a value this helper was never given,
		// and can leave a Pod PriorityClass reference behind where there was none.
		"leaves an owner with no priority class alone": {
			ownerClass: new(""),
			class:      utiltestingapi.MakeWorkloadPriorityClass("high").PriorityValue(500).Obj(),
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload("first", "ns").Priority(100).Obj(),
				utiltestingapi.MakeWorkload("second", "ns").Priority(200).Obj(),
			},
			interceptors: countingReadsAndWrites,
			steps:        []step{{}},
			want: map[string]wantWorkload{
				"first":  {refName: new(""), priority: new(int32(100))},
				"second": {refName: new(""), priority: new(int32(200))},
			},
			wantClassReads:     new(0),
			wantWorkloadWrites: new(0),
		},

		// A repair that fails leaves its own class name still matching, which is
		// the only thing that brings the next call back to finish the transition.
		"a failed repair leaves the marker for the next call": {
			class: utiltestingapi.MakeWorkloadPriorityClass("high").PriorityValue(200).Obj(),
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload("stale", "ns").WorkloadPriorityClassRef("high").Priority(100).Obj(),
				utiltestingapi.MakeWorkload("transitioning", "ns").WorkloadPriorityClassRef("low").Priority(10).Obj(),
			},
			interceptors: failingFirstWriteTo("stale"),
			steps:        []step{{wantErr: true}, {}},
			want: map[string]wantWorkload{
				"stale":         {refName: new("high"), priority: new(int32(200))},
				"transitioning": {refName: new("high"), priority: new(int32(200))},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			jobWrapper := testingjob.MakeJob("job", "ns")
			if className := ptr.Deref(tc.ownerClass, "high"); className != "" {
				jobWrapper = jobWrapper.WorkloadPriorityClass(className)
			}
			job := jobWrapper.Obj()

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
						t.Errorf("%s priority = %d, want %d", n, ptr.Deref(persisted.Spec.Priority, 0), *want.priority)
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

// countingReadsAndWrites counts both, for the cases asserting that a set was
// neither resolved for nor written to. An empty class name resolves through a
// PriorityClass list rather than a class read, so that shape counts too.
func countingReadsAndWrites(s *priorityStats) interceptor.Funcs {
	return interceptor.Funcs{
		Get: countingClassReads(s).Get,
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if _, ok := list.(*schedulingv1.PriorityClassList); ok {
				s.classReads++
			}
			return c.List(ctx, list, opts...)
		},
		Update: countingWrites(s).Update,
	}
}

// failingFirstWriteTo refuses the first write to one workload and lets every
// later one through, so a case can watch what a failed repair leaves behind.
func failingFirstWriteTo(name string) func(*priorityStats) interceptor.Funcs {
	return func(s *priorityStats) interceptor.Funcs {
		refused := false
		return interceptor.Funcs{
			Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
				if wl, ok := obj.(*kueue.Workload); ok && wl.Name == name && !refused {
					refused = true
					return errors.New("simulated conflict")
				}
				s.workloadWrites++
				return c.Update(ctx, obj, opts...)
			},
		}
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

// TestExtractPriorityReportsMissingWorkloadPriorityClass pins which lookup
// failure is reported. Only the class the label named can be reported by name;
// a failure anywhere else leaves the answer unknown, so saying the class does
// not exist would be a guess.
func TestExtractPriorityReportsMissingWorkloadPriorityClass(t *testing.T) {
	forbidden := apierrors.NewForbidden(
		kueue.SchemeGroupVersion.WithResource("workloadpriorityclasses").GroupResource(),
		"denied", errors.New("not allowed"))

	cases := map[string]struct {
		job          *batchv1.Job
		podSets      []kueue.PodSet
		objects      []client.Object
		interceptors interceptor.Funcs
		wantEvents   []utiltesting.EventRecord
		wantErr      error
	}{
		"an explicitly referenced class that does not exist": {
			job: testingjob.MakeJob("job", "ns").WorkloadPriorityClass("missing").Obj(),
			wantEvents: []utiltesting.EventRecord{{
				Key:       types.NamespacedName{Namespace: "ns", Name: "job"},
				EventType: corev1.EventTypeWarning,
				Reason:    ReasonWorkloadPriorityClassNotFound,
				Message:   `WorkloadPriorityClass "missing" not found`,
			}},
			wantErr: apierrors.NewNotFound(
				kueue.SchemeGroupVersion.WithResource("workloadpriorityclasses").GroupResource(),
				"missing",
			),
		},
		"an explicitly referenced class that exists": {
			job: testingjob.MakeJob("job", "ns").WorkloadPriorityClass("present").Obj(),
			objects: []client.Object{
				utiltestingapi.MakeWorkloadPriorityClass("present").PriorityValue(10).Obj(),
			},
		},
		"no class referenced at all": {
			job: testingjob.MakeJob("job", "ns").Obj(),
		},
		// scheduling.k8s.io is a different resource and keeps its own handling.
		"a pod template naming a PriorityClass that does not exist": {
			job:     testingjob.MakeJob("job", "ns").Obj(),
			podSets: []kueue.PodSet{*utiltestingapi.MakePodSet("main", 1).PriorityClass("missing-pc").Obj()},
			wantErr: apierrors.NewNotFound(schedulingv1.Resource("priorityclasses"), "missing-pc"),
		},
		"a lookup that failed for another reason": {
			job: testingjob.MakeJob("job", "ns").WorkloadPriorityClass("denied").Obj(),
			interceptors: interceptor.Funcs{
				Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if _, isClass := obj.(*kueue.WorkloadPriorityClass); isClass {
						return forbidden
					}
					return c.Get(ctx, key, obj, opts...)
				},
			},
			wantErr: forbidden,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cl := utiltesting.NewClientBuilder().
				WithObjects(tc.objects...).
				WithInterceptorFuncs(tc.interceptors).
				Build()
			podSets := tc.podSets
			if podSets == nil {
				podSets = []kueue.PodSet{*utiltestingapi.MakePodSet("main", 1).Obj()}
			}
			recorder := &utiltesting.EventRecorder{}

			// The event does not stand in for the error: the reconcile still fails.
			_, _, err := ExtractPriority(t.Context(), cl, recorder, tc.job, podSets, nil)

			if diff := cmp.Diff(tc.wantErr, err); diff != "" {
				t.Fatalf("ExtractPriority() error (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantEvents, recorder.RecordedEvents); diff != "" {
				t.Errorf("recorded events (-want +got):\n%s", diff)
			}
		})
	}
}

// ApplyWorkloadPriority writes only the targets ClassifyWorkloadsForPriorityUpdate
// picked, so a workload the classifier left out keeps what it carries even when
// the resolved value differs.
func TestApplyWorkloadPriority(t *testing.T) {
	cases := map[string]struct {
		ownerClass   string
		workload     *kueue.Workload
		classRef     *kueue.PriorityClassRef
		priority     int32
		wantRefName  *string
		wantPriority *int32
	}{
		"a workload with no class reference is left alone": {
			workload:     utiltestingapi.MakeWorkload("wl", "ns").Priority(1000).Obj(),
			wantPriority: new(int32(1000)),
		},
		"a value already under the resolved class is left alone": {
			ownerClass:   "high",
			workload:     utiltestingapi.MakeWorkload("wl", "ns").WorkloadPriorityClassRef("high").Priority(500).Obj(),
			classRef:     kueue.NewWorkloadPriorityClassRef("high"),
			priority:     100,
			wantRefName:  new("high"),
			wantPriority: new(int32(500)),
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, log := utiltesting.ContextWithLog(t)
			jobWrapper := testingjob.MakeJob("job", "ns")
			if tc.ownerClass != "" {
				jobWrapper = jobWrapper.WorkloadPriorityClass(tc.ownerClass)
			}
			job := jobWrapper.Obj()

			s := &priorityStats{}
			cl := utiltesting.NewClientBuilder().WithObjects(tc.workload).
				WithInterceptorFuncs(countingWrites(s)).Build()

			_, targets := ClassifyWorkloadsForPriorityUpdate(log, job, []*kueue.Workload{tc.workload})
			if err := ApplyWorkloadPriority(ctx, cl, &utiltesting.EventRecorder{}, job, tc.classRef, tc.priority, targets...); err != nil {
				t.Fatalf("ApplyWorkloadPriority() = %v", err)
			}

			got := &kueue.Workload{}
			if err := cl.Get(ctx, client.ObjectKeyFromObject(tc.workload), got); err != nil {
				t.Fatalf("reading the workload back: %v", err)
			}
			var gotRefName *string
			if got.Spec.PriorityClassRef != nil {
				gotRefName = &got.Spec.PriorityClassRef.Name
			}
			if diff := cmp.Diff(tc.wantRefName, gotRefName); diff != "" {
				t.Errorf("priority class reference name (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantPriority, got.Spec.Priority); diff != "" {
				t.Errorf("priority (-want,+got):\n%s", diff)
			}
			if s.workloadWrites != 0 {
				t.Errorf("wrote the workload %d times, want none", s.workloadWrites)
			}
		})
	}
}
