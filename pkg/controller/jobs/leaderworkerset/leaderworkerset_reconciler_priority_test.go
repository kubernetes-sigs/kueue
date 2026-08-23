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

package leaderworkerset

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/google/go-cmp/cmp"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/component-base/featuregate"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	leaderworkersetv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/util/testingjobs/leaderworkerset"
)

// classReadCount is counted through the interceptor, for the cases that pin how
// often the reconcile looks the class up.
type classReadCount struct {
	reads atomic.Int32
}

func countingClassReads(s *classReadCount) interceptor.Funcs {
	return interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*kueue.WorkloadPriorityClass); ok {
				s.reads.Add(1)
			}
			return c.Get(ctx, key, obj, opts...)
		},
	}
}

// refusingUpdateOf refuses writes to one Workload by name and lets the rest
// through, so a case can watch what one component's failure costs the others.
func refusingUpdateOf(name string) func(*classReadCount) interceptor.Funcs {
	return func(*classReadCount) interceptor.Funcs {
		return interceptor.Funcs{
			Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
				if wl, ok := obj.(*kueue.Workload); ok && wl.Name == name {
					return apierrors.NewConflict(kueue.Resource("workloads"), wl.Name, errors.New("conflict"))
				}
				return c.Update(ctx, obj, opts...)
			},
		}
	}
}

type wantComponent struct {
	className string
	priority  int32
}

func TestReconcilerResolvesPriorityClassOnce(t *testing.T) {
	features.SetFeatureGatesDuringTest(t, map[featuregate.Feature]bool{features.TopologyAwareScheduling: false})
	request := reconcile.Request{NamespacedName: types.NamespacedName{Name: testLWS, Namespace: testNS}}
	comp0 := GetWorkloadName(testLWS, testLWS, "0")
	comp1 := GetWorkloadName(testLWS, testLWS, "1")
	comp5 := GetWorkloadName(testLWS, testLWS, "5")

	cases := map[string]struct {
		leaderWorkerSet *leaderworkersetv1.LeaderWorkerSet
		classes         []*kueue.WorkloadPriorityClass
		components      []*kueue.Workload
		interceptors    func(s *classReadCount) interceptor.Funcs
		wantErr         bool
		wantCount       int
		// Nil where the count is not part of what the case pins.
		wantClassReads *int32
		want           map[string]wantComponent
		wantAbsent     []string
	}{
		"one read serves a set whose components all move to the class": {
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
				WorkloadPriorityClass("new-wpc").Replicas(2).UID(testLWS).Obj(),
			classes: []*kueue.WorkloadPriorityClass{
				utiltestingapi.MakeWorkloadPriorityClass("old-wpc").PriorityValue(1000).Obj(),
				utiltestingapi.MakeWorkloadPriorityClass("new-wpc").PriorityValue(5000).Obj(),
			},
			components:     []*kueue.Workload{lwsComponent("0", "old-wpc", 1000), lwsComponent("1", "old-wpc", 1000)},
			interceptors:   countingClassReads,
			wantCount:      2,
			wantClassReads: new(int32(1)),
			want: map[string]wantComponent{
				comp0: {className: "new-wpc", priority: 5000},
				comp1: {className: "new-wpc", priority: 5000},
			},
		},
		"one read serves a set whose components are all created": {
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
				WorkloadPriorityClass("new-wpc").Replicas(2).UID(testLWS).Obj(),
			classes: []*kueue.WorkloadPriorityClass{
				utiltestingapi.MakeWorkloadPriorityClass("new-wpc").PriorityValue(5000).Obj(),
			},
			interceptors:   countingClassReads,
			wantCount:      2,
			wantClassReads: new(int32(1)),
			want: map[string]wantComponent{
				comp0: {className: "new-wpc", priority: 5000},
				comp1: {className: "new-wpc", priority: 5000},
			},
		},
		"one read serves a set that is part created and part updated": {
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
				WorkloadPriorityClass("new-wpc").Replicas(2).UID(testLWS).Obj(),
			classes: []*kueue.WorkloadPriorityClass{
				utiltestingapi.MakeWorkloadPriorityClass("old-wpc").PriorityValue(1000).Obj(),
				utiltestingapi.MakeWorkloadPriorityClass("new-wpc").PriorityValue(5000).Obj(),
			},
			components:     []*kueue.Workload{lwsComponent("0", "old-wpc", 1000)},
			interceptors:   countingClassReads,
			wantCount:      2,
			wantClassReads: new(int32(1)),
			want: map[string]wantComponent{
				comp0: {className: "new-wpc", priority: 5000},
				comp1: {className: "new-wpc", priority: 5000},
			},
		},
		"a component already naming the class keeps the value it has": {
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
				WorkloadPriorityClass("high").Replicas(2).UID(testLWS).Obj(),
			classes: []*kueue.WorkloadPriorityClass{
				utiltestingapi.MakeWorkloadPriorityClass("high").PriorityValue(200).Obj(),
			},
			components:     []*kueue.Workload{lwsComponent("0", "high", 100)},
			interceptors:   countingClassReads,
			wantCount:      2,
			wantClassReads: new(int32(1)),
			want: map[string]wantComponent{
				comp0: {className: "high", priority: 100},
				comp1: {className: "high", priority: 200},
			},
		},
		"a sibling transitioning does not move a component already on the class": {
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
				WorkloadPriorityClass("high").Replicas(2).UID(testLWS).Obj(),
			classes: []*kueue.WorkloadPriorityClass{
				utiltestingapi.MakeWorkloadPriorityClass("high").PriorityValue(200).Obj(),
				utiltestingapi.MakeWorkloadPriorityClass("low").PriorityValue(10).Obj(),
			},
			components: []*kueue.Workload{lwsComponent("0", "high", 500), lwsComponent("1", "low", 10)},
			wantCount:  2,
			want: map[string]wantComponent{
				comp0: {className: "high", priority: 500},
				comp1: {className: "high", priority: 200},
			},
		},
		"a surplus component is deleted even though the class is missing": {
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
				WorkloadPriorityClass("missing-wpc").Replicas(2).UID(testLWS).Obj(),
			components: []*kueue.Workload{
				lwsComponent("0", "missing-wpc", 100),
				lwsComponent("5", "missing-wpc", 100),
			},
			wantErr:    true,
			wantAbsent: []string{comp5},
		},
		"one component's failed write does not hold back the others": {
			leaderWorkerSet: leaderworkerset.MakeLeaderWorkerSet(testLWS, testNS).
				Queue("lws-queue").WorkloadPriorityClass("new-wpc").Replicas(2).UID(testLWS).Obj(),
			classes: []*kueue.WorkloadPriorityClass{
				utiltestingapi.MakeWorkloadPriorityClass("old-wpc").PriorityValue(1000).Obj(),
				utiltestingapi.MakeWorkloadPriorityClass("new-wpc").PriorityValue(5000).Obj(),
			},
			// Neither carries the queue name yet, so both go through the queue
			// write first, and only one of them fails there.
			components:   []*kueue.Workload{lwsComponent("0", "old-wpc", 1000), lwsComponent("1", "old-wpc", 1000)},
			interceptors: refusingUpdateOf(comp0),
			wantErr:      true,
			want:         map[string]wantComponent{comp1: {className: "new-wpc", priority: 5000}},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			stats := &classReadCount{}
			clientBuilder := utiltesting.NewClientBuilder(leaderworkersetv1.AddToScheme)
			if tc.interceptors != nil {
				clientBuilder = clientBuilder.WithInterceptorFuncs(tc.interceptors(stats))
			}
			indexer := utiltesting.AsIndexer(clientBuilder)
			objs := []client.Object{tc.leaderWorkerSet}
			for _, class := range tc.classes {
				objs = append(objs, class)
			}
			for _, wl := range tc.components {
				objs = append(objs, wl)
			}
			kClient := clientBuilder.WithObjects(objs...).Build()

			reconciler, err := NewReconciler(ctx, kClient, indexer, &utiltesting.EventRecorder{})
			if err != nil {
				t.Fatalf("Creating the reconciler: %v", err)
			}
			_, err = reconciler.Reconcile(ctx, request)
			if gotErr := err != nil; gotErr != tc.wantErr {
				t.Fatalf("Reconcile() error = %v, want an error = %v", err, tc.wantErr)
			}

			if tc.wantClassReads != nil {
				if diff := cmp.Diff(*tc.wantClassReads, stats.reads.Load()); diff != "" {
					t.Errorf("WorkloadPriorityClass reads (-want,+got):\n%s", diff)
				}
			}

			var got kueue.WorkloadList
			if err := kClient.List(ctx, &got, client.InNamespace(testNS)); err != nil {
				t.Fatalf("Listing workloads: %v", err)
			}
			if tc.wantCount > 0 && len(got.Items) != tc.wantCount {
				t.Fatalf("got %d workloads, want %d", len(got.Items), tc.wantCount)
			}
			byName := make(map[string]*kueue.Workload, len(got.Items))
			for i := range got.Items {
				byName[got.Items[i].Name] = &got.Items[i]
			}
			for wlName, want := range tc.want {
				wl, ok := byName[wlName]
				if !ok {
					t.Errorf("%s: not found", wlName)
					continue
				}
				if diff := cmp.Diff(kueue.NewWorkloadPriorityClassRef(want.className), wl.Spec.PriorityClassRef); diff != "" {
					t.Errorf("%s: priority class reference (-want,+got):\n%s", wlName, diff)
				}
				if diff := cmp.Diff(new(want.priority), wl.Spec.Priority); diff != "" {
					t.Errorf("%s: priority (-want,+got):\n%s", wlName, diff)
				}
			}
			for _, wlName := range tc.wantAbsent {
				if _, ok := byName[wlName]; ok {
					t.Errorf("%s is still there, want it deleted", wlName)
				}
			}
		})
	}
}
