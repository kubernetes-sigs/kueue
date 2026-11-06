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

package workloaddispatcher

import (
	"context"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/util/workqueue"
	testingclock "k8s.io/utils/clock/testing"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/admissionchecks/multikueue"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestAllAtOnceDispatcherReconciler_Reconcile(t *testing.T) {
	const (
		workloadName = "test-workload"
		acName       = "ac1"
	)

	now := time.Now()
	fakeClock := testingclock.NewFakeClock(now)
	baseWorkload := utiltestingapi.MakeWorkload(workloadName, metav1.NamespaceDefault).
		AdmissionCheck(kueue.AdmissionCheckState{Name: acName, State: kueue.CheckStatePending}).
		ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now)
	activeCluster := func(name string) kueue.MultiKueueCluster {
		return *utiltestingapi.MakeMultiKueueCluster(name).
			Active(metav1.ConditionTrue, "Active", "", 1).
			Obj()
	}

	tests := map[string]struct {
		workload              *kueue.Workload
		clusters              []kueue.MultiKueueCluster
		wantNominatedClusters []string
	}{
		"nominates all active clusters": {
			workload:              baseWorkload.Clone().Obj(),
			clusters:              []kueue.MultiKueueCluster{activeCluster("cluster1"), activeCluster("cluster2")},
			wantNominatedClusters: []string{"cluster1", "cluster2"},
		},
		"inactive cluster is not nominated": {
			workload: baseWorkload.Clone().Obj(),
			clusters: []kueue.MultiKueueCluster{
				activeCluster("cluster1"),
				*utiltestingapi.MakeMultiKueueCluster("cluster2").Active(metav1.ConditionFalse, "Inactive", "", 1).Obj(),
			},
			wantNominatedClusters: []string{"cluster1"},
		},
		"workload being evicted is not nominated": {
			workload: baseWorkload.Clone().EvictedAt(now).Obj(),
			clusters: []kueue.MultiKueueCluster{activeCluster("cluster1"), activeCluster("cluster2")},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			objs := append(multiKueueObjects(acName, "cluster1", "cluster2"), tc.workload)
			for i := range tc.clusters {
				objs = append(objs, &tc.clusters[i])
			}
			cl := utiltesting.NewClientBuilder().WithObjects(objs...).WithStatusSubresource(tc.workload).Build()
			rec := &AllAtOnceDispatcherReconciler{dispatcher: newTestDispatcher(t, cl, fakeClock)}

			req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(tc.workload)}
			ctx, _ := utiltesting.ContextWithLog(t)
			if _, err := rec.Reconcile(ctx, req); err != nil {
				t.Fatalf("Reconcile returned unexpected error: %v", err)
			}

			gotWl := &kueue.Workload{}
			if err := cl.Get(ctx, req.NamespacedName, gotWl); err != nil {
				t.Fatalf("Fail to get workload: %v", err)
			}
			if diff := cmp.Diff(tc.wantNominatedClusters, gotWl.Status.NominatedClusterNames); diff != "" {
				t.Errorf("Unexpected nominated clusters (-want/+got)\n%s", diff)
			}
		})
	}
}

func TestAllAtOnceDispatcherNominateWorkers(t *testing.T) {
	const testName = "test-wl"
	now := time.Now()
	fakeClock := testingclock.NewFakeClock(now)
	baseWl := utiltestingapi.MakeWorkload(testName, metav1.NamespaceDefault).
		AdmissionCheck(kueue.AdmissionCheckState{
			Name:  "ac1",
			State: kueue.CheckStatePending,
		})

	testCases := map[string]struct {
		remoteClusters        sets.Set[string]
		workload              *kueue.Workload
		wantNominatedClusters []string
		wantPatched           bool
	}{
		"no remotes": {
			remoteClusters:        make(sets.Set[string]),
			workload:              baseWl.Clone().Obj(),
			wantNominatedClusters: nil,
		},
		"one remote": {
			remoteClusters:        sets.New("A"),
			workload:              baseWl.Clone().Obj(),
			wantNominatedClusters: []string{"A"},
			wantPatched:           true,
		},
		"three remotes": {
			remoteClusters:        sets.New("A", "B", "C"),
			workload:              baseWl.Clone().Obj(),
			wantNominatedClusters: []string{"A", "B", "C"},
			wantPatched:           true,
		},
		"remotes returned in sorted order": {
			remoteClusters:        sets.New("C", "A", "B"),
			workload:              baseWl.Clone().Obj(),
			wantNominatedClusters: []string{"A", "B", "C"},
			wantPatched:           true,
		},
		"all already nominated, no patch needed": {
			remoteClusters:        sets.New("A", "B", "C"),
			workload:              baseWl.Clone().NominatedClusterNames("A", "B", "C").Obj(),
			wantNominatedClusters: []string{"A", "B", "C"},
		},
		"partial existing nomination, expanded to full set": {
			remoteClusters:        sets.New("A", "B", "C", "D"),
			workload:              baseWl.Clone().NominatedClusterNames("A", "B").Obj(),
			wantNominatedClusters: []string{"A", "B", "C", "D"},
			wantPatched:           true,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			patched := false
			cl := utiltesting.NewClientBuilder().
				WithObjects(tc.workload).WithStatusSubresource(tc.workload).
				WithInterceptorFuncs(interceptor.Funcs{
					SubResourceApply: func(ctx context.Context, c client.Client, subResourceName string, obj runtime.ApplyConfiguration, opts ...client.SubResourceApplyOption) error {
						patched = true
						return c.SubResource(subResourceName).Apply(ctx, obj, opts...)
					},
				}).Build()

			reconciler := &AllAtOnceDispatcherReconciler{client: cl, clock: fakeClock}

			ctx, log := utiltesting.ContextWithLog(t)
			if _, err := reconciler.nominateWorkers(ctx, tc.workload, tc.remoteClusters, log); err != nil {
				t.Fatalf("nominateWorkers returned unexpected error: %v", err)
			}

			if diff := cmp.Diff(tc.wantNominatedClusters, tc.workload.Status.NominatedClusterNames); diff != "" {
				t.Errorf("unexpected nominated clusters (-want/+got):\n%s", diff)
			}
			if patched != tc.wantPatched {
				t.Errorf("unexpected status patch: want %t, got %t", tc.wantPatched, patched)
			}
		})
	}
}

func TestAllAtOnceClusterHandler(t *testing.T) {
	const acName = "ac1"
	cluster := func(status metav1.ConditionStatus, reason, message string) *kueue.MultiKueueCluster {
		return utiltestingapi.MakeMultiKueueCluster("cluster1").Active(status, reason, message, 1).Obj()
	}
	active := cluster(metav1.ConditionTrue, "Active", "Connected")
	inactive := cluster(metav1.ConditionFalse, "ClientConnectionFailed", "connection refused")
	queued := []reconcile.Request{{Namespace: metav1.NamespaceDefault, Name: "wl1"}}

	type queue = workqueue.TypedRateLimitingInterface[reconcile.Request]
	tests := map[string]struct {
		send       func(ctx context.Context, h *allAtOnceClusterHandler, q queue)
		wantQueued []reconcile.Request
	}{
		"create": {
			send: func(ctx context.Context, h *allAtOnceClusterHandler, q queue) {
				h.Create(ctx, event.CreateEvent{Object: utiltestingapi.MakeMultiKueueCluster("cluster1").Obj()}, q)
			},
		},
		"update deactivating the cluster": {
			send: func(ctx context.Context, h *allAtOnceClusterHandler, q queue) {
				h.Update(ctx, event.UpdateEvent{ObjectOld: active, ObjectNew: inactive}, q)
			},
			wantQueued: queued,
		},
		"update activating the cluster": {
			send: func(ctx context.Context, h *allAtOnceClusterHandler, q queue) {
				h.Update(ctx, event.UpdateEvent{ObjectOld: inactive, ObjectNew: active}, q)
			},
			wantQueued: queued,
		},
		"update activating a new cluster": {
			send: func(ctx context.Context, h *allAtOnceClusterHandler, q queue) {
				h.Update(ctx, event.UpdateEvent{ObjectOld: utiltestingapi.MakeMultiKueueCluster("cluster1").Obj(), ObjectNew: active}, q)
			},
			wantQueued: queued,
		},
		"update changing the reason of an active cluster": {
			send: func(ctx context.Context, h *allAtOnceClusterHandler, q queue) {
				h.Update(ctx, event.UpdateEvent{ObjectOld: active, ObjectNew: cluster(metav1.ConditionTrue, "Reconnected", "Connected")}, q)
			},
		},
		"update changing the message of an inactive cluster": {
			send: func(ctx context.Context, h *allAtOnceClusterHandler, q queue) {
				h.Update(ctx, event.UpdateEvent{ObjectOld: inactive, ObjectNew: cluster(metav1.ConditionFalse, "ClientConnectionFailed", "timeout")}, q)
			},
		},
		"delete": {
			send: func(ctx context.Context, h *allAtOnceClusterHandler, q queue) {
				h.Delete(ctx, event.DeleteEvent{Object: active}, q)
			},
			wantQueued: queued,
		},
		"delete a cluster no config lists": {
			send: func(ctx context.Context, h *allAtOnceClusterHandler, q queue) {
				h.Delete(ctx, event.DeleteEvent{Object: utiltestingapi.MakeMultiKueueCluster("cluster2").Obj()}, q)
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			objs := append(multiKueueObjects(acName, "cluster1"),
				utiltestingapi.MakeWorkload("wl1", metav1.NamespaceDefault).
					AdmissionCheck(kueue.AdmissionCheckState{Name: acName, State: kueue.CheckStatePending}).
					Obj(),
			)
			cl := newIndexedClientBuilder(ctx, t).WithObjects(objs...).Build()
			q := &utiltesting.MockTypedRateLimitingInterface{}

			tc.send(ctx, &allAtOnceClusterHandler{client: cl}, q)

			if diff := cmp.Diff(tc.wantQueued, q.Items); diff != "" {
				t.Errorf("Unexpected queued requests (-want/+got)\n%s", diff)
			}
		})
	}
}

// newIndexedClientBuilder returns a fake client builder with the field indexes the
// AllAtOnce dispatcher's event handlers list through.
func newIndexedClientBuilder(ctx context.Context, t *testing.T) *fake.ClientBuilder {
	t.Helper()
	builder := utiltesting.NewClientBuilder().
		WithIndex(&kueue.Workload{}, indexer.WorkloadAdmissionCheckKey, indexer.IndexWorkloadAdmissionCheck)
	if err := multikueue.SetupIndexer(ctx, utiltesting.AsIndexer(builder), metav1.NamespaceDefault); err != nil {
		t.Fatalf("Failed to set up the MultiKueue indexes: %v", err)
	}
	return builder
}
