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
		class            *kueue.WorkloadPriorityClass
		workloads        []*kueue.Workload
		job              *batchv1.Job
		podPriorityClass []schedulingv1.PriorityClass
		interceptors     func(s *priorityStats) interceptor.Funcs
		steps            []step
		want             map[string]wantWorkload
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

		// Group and kind are frozen while quota is reserved.
		"leaves a quota-reserved workload alone when the owner falls back to a pod priority class": {
			job: testingjob.MakeJob("job", "ns").PriorityClass("podpc").Obj(),
			podPriorityClass: []schedulingv1.PriorityClass{
				{ObjectMeta: metav1.ObjectMeta{Name: "podpc"}, Value: 50},
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload("reserved", "ns").
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).PriorityClass("podpc").Obj()).
					WorkloadPriorityClassRef("low").Priority(10).
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
				"reserved": {refName: new("low"), priority: new(int32(10))},
			},
			wantWorkloadWrites: new(0),
		},

		// A nil resolved ref is a removal, frozen while quota is reserved.
		"leaves a quota-reserved workload alone when the owner's class stops resolving": {
			job: testingjob.MakeJob("job", "ns").Obj(),
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload("reserved", "ns").
					WorkloadPriorityClassRef("low").Priority(10).
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
				"reserved": {refName: new("low"), priority: new(int32(10))},
			},
			wantWorkloadWrites: new(0),
		},

		// Same group and kind, so the rename is legal while reserved.
		"moves a quota-reserved workload between workload priority classes": {
			class: utiltestingapi.MakeWorkloadPriorityClass("low").PriorityValue(10).Obj(),
			job:   testingjob.MakeJob("job", "ns").WorkloadPriorityClass("low").Obj(),
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload("reserved", "ns").
					WorkloadPriorityClassRef("high").Priority(100).
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
				"reserved": {refName: new("low"), priority: new(int32(10))},
			},
			wantWorkloadWrites: new(1),
		},

		"still moves a workload without a reservation onto a pod priority class": {
			job: testingjob.MakeJob("job", "ns").PriorityClass("podpc").Obj(),
			podPriorityClass: []schedulingv1.PriorityClass{
				{ObjectMeta: metav1.ObjectMeta{Name: "podpc"}, Value: 50},
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload("free", "ns").
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).PriorityClass("podpc").Obj()).
					WorkloadPriorityClassRef("low").Priority(10).Obj(),
			},
			steps: []step{{}},
			want: map[string]wantWorkload{
				"free": {refName: new("podpc"), priority: new(int32(50))},
			},
		},

		"writes the unreserved half of a batch and skips the reserved half": {
			job: testingjob.MakeJob("job", "ns").PriorityClass("podpc").Obj(),
			podPriorityClass: []schedulingv1.PriorityClass{
				{ObjectMeta: metav1.ObjectMeta{Name: "podpc"}, Value: 50},
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload("reserved", "ns").
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).PriorityClass("podpc").Obj()).
					WorkloadPriorityClassRef("low").Priority(10).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "AdmittedByTest",
						Message:            "reserved",
						LastTransitionTime: metav1.Now(),
					}).Obj(),
				utiltestingapi.MakeWorkload("free", "ns").
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).PriorityClass("podpc").Obj()).
					WorkloadPriorityClassRef("low").Priority(10).Obj(),
			},
			interceptors: countingWrites,
			steps:        []step{{}},
			want: map[string]wantWorkload{
				"reserved": {refName: new("low"), priority: new(int32(10))},
				"free":     {refName: new("podpc"), priority: new(int32(50))},
			},
			wantWorkloadWrites: new(1),
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

		// Same-name workloads are repaired before the name-changing ones, so a failed
		// write leaves a name mismatch behind. That mismatch is the only thing that
		// makes a later call resolve the class again, so writing the two groups the
		// other way round strands the stale one for good.
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
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			job := tc.job
			if job == nil {
				job = testingjob.MakeJob("job", "ns").WorkloadPriorityClass("high").Obj()
			}

			objs := []client.Object{job}
			if tc.class != nil {
				objs = append(objs, tc.class)
			}
			for i := range tc.podPriorityClass {
				objs = append(objs, &tc.podPriorityClass[i])
			}
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
