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

package multikueue

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobs"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func expectCQClusterStatusMetric(t *testing.T, cqName, cluster string, want metav1.ConditionStatus) {
	t.Helper()
	for _, status := range metrics.ConditionStatusValues {
		wantV := 0.0
		if status == want {
			wantV = 1.0
		}
		got := testutil.ToFloat64(metrics.MultiKueueClusterByStatus.WithLabelValues(cqName, cluster, string(status), roletracker.RoleStandalone))
		if got != wantV {
			t.Errorf("cluster_status{cluster_queue=%q, cluster=%q, active=%s}: want %v, got %v", cqName, cluster, status, wantV, got)
		}
	}
}

func TestCQReconcilerReportsClusterStatusMetric(t *testing.T) {
	metrics.MultiKueueClusterByStatus.Reset()
	t.Cleanup(metrics.MultiKueueClusterByStatus.Reset)

	ctx, _ := utiltesting.ContextWithLog(t)

	cq := utiltestingapi.MakeClusterQueue("cq1").
		ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource("cpu", "0").Obj()).
		AdmissionChecks("ac1").
		Obj()
	ac := utiltestingapi.MakeAdmissionCheck("ac1").
		ControllerName(kueue.MultiKueueControllerName).
		Parameters(kueue.SchemeGroupVersion.Group, "MultiKueueConfig", "config1").
		Obj()
	cfg := utiltestingapi.MakeMultiKueueConfig("config1").Clusters("worker1", "worker2").Obj()
	// worker1 is connected, worker2 is not.
	worker1 := utiltestingapi.MakeMultiKueueCluster("worker1").Active(metav1.ConditionTrue, "Active", "Connected", 1).Obj()
	worker2 := utiltestingapi.MakeMultiKueueCluster("worker2").Active(metav1.ConditionFalse, "ClientConnectionFailed", "connection lost", 1).Obj()

	c := utiltesting.NewClientBuilder().
		WithObjects(cq, ac, cfg, worker1, worker2).
		WithIndex(&kueue.AdmissionCheck{}, AdmissionCheckControllerNameKey, admissionCheckControllerNameIndexerFunc).
		WithIndex(&kueue.AdmissionCheck{}, AdmissionCheckUsingConfigKey, admissioncheck.IndexerByConfigFunction(kueue.MultiKueueControllerName, configGVK)).
		WithIndex(&kueue.ClusterQueue{}, ClusterQueueAdmissionChecksKey, clusterQueueAdmissionChecksIndexerFunc).
		WithStatusSubresource(cq).
		Build()
	helper, _ := admissioncheck.NewMultiKueueStoreHelper(c)
	adapters, _ := jobs.NewIntegrationManager().GetMultiKueueAdapters(sets.New("batch/job"))
	cRec := newClustersReconciler(c, TestNamespace, 0, defaultOrigin, nil, adapters, nil, nil, &utiltesting.EventRecorder{})
	reconciler := newCQReconciler(c, helper, cRec, nil, 100*time.Millisecond)

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "cq1"}}
	if _, err := reconciler.Reconcile(ctx, req); err != nil {
		t.Fatalf("unexpected reconcile error: %v", err)
	}

	// Each worker is reported under the ClusterQueue that references it.
	expectCQClusterStatusMetric(t, "cq1", "worker1", metav1.ConditionTrue)
	expectCQClusterStatusMetric(t, "cq1", "worker2", metav1.ConditionFalse)

	// Dropping worker2 from the config must stop reporting it, otherwise a cluster
	// a ClusterQueue no longer uses would linger as if it were still relevant.
	updatedCfg := &kueue.MultiKueueConfig{}
	if err := c.Get(ctx, types.NamespacedName{Name: "config1"}, updatedCfg); err != nil {
		t.Fatalf("unexpected error reading the config: %v", err)
	}
	updatedCfg.Spec.Clusters = []string{"worker1"}
	if err := c.Update(ctx, updatedCfg); err != nil {
		t.Fatalf("unexpected error updating the config: %v", err)
	}
	if _, err := reconciler.Reconcile(ctx, req); err != nil {
		t.Fatalf("unexpected reconcile error after config change: %v", err)
	}
	if got := testutil.CollectAndCount(metrics.MultiKueueClusterByStatus); got != len(metrics.ConditionStatusValues) {
		t.Errorf("expected only worker1 series to remain, got %d series", got)
	}
	expectCQClusterStatusMetric(t, "cq1", "worker1", metav1.ConditionTrue)

	// A cluster whose Active condition has not been written yet is the one case that is
	// genuinely unknown, and the only one reported as Unknown.
	worker3 := utiltestingapi.MakeMultiKueueCluster("worker3").Obj()
	if err := c.Create(ctx, worker3); err != nil {
		t.Fatalf("unexpected error creating the worker cluster: %v", err)
	}
	updatedCfg.Spec.Clusters = []string{"worker1", "worker3"}
	if err := c.Update(ctx, updatedCfg); err != nil {
		t.Fatalf("unexpected error updating the config: %v", err)
	}
	if _, err := reconciler.Reconcile(ctx, req); err != nil {
		t.Fatalf("unexpected reconcile error after adding a cluster: %v", err)
	}
	expectCQClusterStatusMetric(t, "cq1", "worker1", metav1.ConditionTrue)
	expectCQClusterStatusMetric(t, "cq1", "worker3", metav1.ConditionUnknown)

	// A cluster the ClusterQueue still references but that has no MultiKueueCluster
	// object has no Active condition to mirror, so it is not reported at all. The
	// AdmissionCheck surfaces it as a missing cluster instead.
	if err := c.Delete(ctx, worker3); err != nil {
		t.Fatalf("unexpected error deleting the worker cluster: %v", err)
	}
	if _, err := reconciler.Reconcile(ctx, req); err != nil {
		t.Fatalf("unexpected reconcile error after cluster deletion: %v", err)
	}
	if got := testutil.CollectAndCount(metrics.MultiKueueClusterByStatus); got != len(metrics.ConditionStatusValues) {
		t.Errorf("expected the missing cluster not to be reported, got %d series", got)
	}
	expectCQClusterStatusMetric(t, "cq1", "worker1", metav1.ConditionTrue)

	// Deleting the ClusterQueue drops its series entirely.
	if err := c.Delete(ctx, cq); err != nil {
		t.Fatalf("unexpected error deleting the ClusterQueue: %v", err)
	}
	if _, err := reconciler.Reconcile(ctx, req); err != nil {
		t.Fatalf("unexpected reconcile error after deletion: %v", err)
	}
	if got := testutil.CollectAndCount(metrics.MultiKueueClusterByStatus); got != 0 {
		t.Errorf("expected all series to be cleared, got %d", got)
	}
}

func TestCQReconcilerKeepsClusterStatusMetricOnReadError(t *testing.T) {
	readErr := errors.New("simulated read failure")

	cases := map[string]struct {
		failRead func(obj client.Object) bool
	}{
		"reading the MultiKueueConfig fails": {
			failRead: func(obj client.Object) bool {
				_, isConfig := obj.(*kueue.MultiKueueConfig)
				return isConfig
			},
		},
		"reading the MultiKueueCluster fails": {
			failRead: func(obj client.Object) bool {
				_, isCluster := obj.(*kueue.MultiKueueCluster)
				return isCluster
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			metrics.MultiKueueClusterByStatus.Reset()
			t.Cleanup(metrics.MultiKueueClusterByStatus.Reset)

			ctx, _ := utiltesting.ContextWithLog(t)

			cq := utiltestingapi.MakeClusterQueue("cq1").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource("cpu", "0").Obj()).
				AdmissionChecks("ac1").
				Obj()
			ac := utiltestingapi.MakeAdmissionCheck("ac1").
				ControllerName(kueue.MultiKueueControllerName).
				Parameters(kueue.SchemeGroupVersion.Group, "MultiKueueConfig", "config1").
				Obj()
			cfg := utiltestingapi.MakeMultiKueueConfig("config1").Clusters("worker1").Obj()
			worker1 := utiltestingapi.MakeMultiKueueCluster("worker1").Active(metav1.ConditionTrue, "Active", "Connected", 1).Obj()

			readsFail := false
			c := utiltesting.NewClientBuilder().
				WithObjects(cq, ac, cfg, worker1).
				WithIndex(&kueue.AdmissionCheck{}, AdmissionCheckControllerNameKey, admissionCheckControllerNameIndexerFunc).
				WithIndex(&kueue.AdmissionCheck{}, AdmissionCheckUsingConfigKey, admissioncheck.IndexerByConfigFunction(kueue.MultiKueueControllerName, configGVK)).
				WithIndex(&kueue.ClusterQueue{}, ClusterQueueAdmissionChecksKey, clusterQueueAdmissionChecksIndexerFunc).
				WithStatusSubresource(cq).
				WithInterceptorFuncs(interceptor.Funcs{
					Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
						if readsFail && tc.failRead(obj) {
							return readErr
						}
						return cl.Get(ctx, key, obj, opts...)
					},
				}).
				Build()
			helper, _ := admissioncheck.NewMultiKueueStoreHelper(c)
			adapters, _ := jobs.NewIntegrationManager().GetMultiKueueAdapters(sets.New("batch/job"))
			cRec := newClustersReconciler(c, TestNamespace, 0, defaultOrigin, nil, adapters, nil, nil, &utiltesting.EventRecorder{})
			reconciler := newCQReconciler(c, helper, cRec, nil, 100*time.Millisecond)

			req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "cq1"}}
			if _, err := reconciler.Reconcile(ctx, req); err != nil {
				t.Fatalf("unexpected reconcile error: %v", err)
			}
			expectCQClusterStatusMetric(t, "cq1", "worker1", metav1.ConditionTrue)

			// A read failure must fail the reconcile so that it is retried, and must leave
			// the statuses reported so far in place: failing to read is not evidence that
			// the ClusterQueue stopped using its workers.
			readsFail = true
			if _, err := reconciler.Reconcile(ctx, req); !errors.Is(err, readErr) {
				t.Fatalf("expected the reconcile to fail with %q, got %v", readErr, err)
			}
			expectCQClusterStatusMetric(t, "cq1", "worker1", metav1.ConditionTrue)
		})
	}
}

func TestCQReconcilerClearsClusterStatusMetricPerClusterQueue(t *testing.T) {
	metrics.MultiKueueClusterByStatus.Reset()
	t.Cleanup(metrics.MultiKueueClusterByStatus.Reset)

	ctx, _ := utiltesting.ContextWithLog(t)

	// Both ClusterQueues use the same admission check, so they share worker1.
	cq1 := utiltestingapi.MakeClusterQueue("cq1").
		ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource("cpu", "0").Obj()).
		AdmissionChecks("ac1").
		Obj()
	cq2 := utiltestingapi.MakeClusterQueue("cq2").
		ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource("cpu", "0").Obj()).
		AdmissionChecks("ac1").
		Obj()
	ac := utiltestingapi.MakeAdmissionCheck("ac1").
		ControllerName(kueue.MultiKueueControllerName).
		Parameters(kueue.SchemeGroupVersion.Group, "MultiKueueConfig", "config1").
		Obj()
	cfg := utiltestingapi.MakeMultiKueueConfig("config1").Clusters("worker1").Obj()
	worker1 := utiltestingapi.MakeMultiKueueCluster("worker1").Active(metav1.ConditionTrue, "Active", "Connected", 1).Obj()

	c := utiltesting.NewClientBuilder().
		WithObjects(cq1, cq2, ac, cfg, worker1).
		WithIndex(&kueue.AdmissionCheck{}, AdmissionCheckControllerNameKey, admissionCheckControllerNameIndexerFunc).
		WithIndex(&kueue.AdmissionCheck{}, AdmissionCheckUsingConfigKey, admissioncheck.IndexerByConfigFunction(kueue.MultiKueueControllerName, configGVK)).
		WithIndex(&kueue.ClusterQueue{}, ClusterQueueAdmissionChecksKey, clusterQueueAdmissionChecksIndexerFunc).
		WithStatusSubresource(cq1, cq2).
		Build()
	helper, _ := admissioncheck.NewMultiKueueStoreHelper(c)
	adapters, _ := jobs.NewIntegrationManager().GetMultiKueueAdapters(sets.New("batch/job"))
	cRec := newClustersReconciler(c, TestNamespace, 0, defaultOrigin, nil, adapters, nil, nil, &utiltesting.EventRecorder{})
	reconciler := newCQReconciler(c, helper, cRec, nil, 100*time.Millisecond)

	req1 := reconcile.Request{NamespacedName: types.NamespacedName{Name: "cq1"}}
	req2 := reconcile.Request{NamespacedName: types.NamespacedName{Name: "cq2"}}
	if _, err := reconciler.Reconcile(ctx, req1); err != nil {
		t.Fatalf("unexpected reconcile error for cq1: %v", err)
	}
	if _, err := reconciler.Reconcile(ctx, req2); err != nil {
		t.Fatalf("unexpected reconcile error for cq2: %v", err)
	}

	// The shared worker is reported once per ClusterQueue.
	expectCQClusterStatusMetric(t, "cq1", "worker1", metav1.ConditionTrue)
	expectCQClusterStatusMetric(t, "cq2", "worker1", metav1.ConditionTrue)

	// Deleting one ClusterQueue drops only its own series, otherwise the other
	// ClusterQueue would stop reporting a worker it still uses.
	if err := c.Delete(ctx, cq1); err != nil {
		t.Fatalf("unexpected error deleting the ClusterQueue: %v", err)
	}
	if _, err := reconciler.Reconcile(ctx, req1); err != nil {
		t.Fatalf("unexpected reconcile error after deletion: %v", err)
	}
	if got := testutil.CollectAndCount(metrics.MultiKueueClusterByStatus); got != len(metrics.ConditionStatusValues) {
		t.Errorf("expected only the cq2 series to remain, got %d series", got)
	}
	expectCQClusterStatusMetric(t, "cq2", "worker1", metav1.ConditionTrue)
}
