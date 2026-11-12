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
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/prometheus/client_golang/prometheus/testutil"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
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

func TestACReconcilerReportsClusterStatusMetric(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.MultiKueueMetricForClusterByStatus, true)
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
		WithStatusSubresource(cq, ac).
		Build()
	helper, _ := admissioncheck.NewMultiKueueStoreHelper(c)
	reconciler := newACReconciler(c, helper, nil)

	req := reconcile.Request{Name: "ac1"}
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

func TestACReconcilerKeepsClusterStatusMetricOnReadError(t *testing.T) {
	readErr := errors.New("simulated read failure")

	cases := map[string]struct {
		failRead func(obj client.Object) bool
		failList func(obj client.ObjectList) bool
	}{
		"listing ClusterQueues fails":   {failList: func(obj client.ObjectList) bool { _, ok := obj.(*kueue.ClusterQueueList); return ok }},
		"listing AdmissionChecks fails": {failList: func(obj client.ObjectList) bool { _, ok := obj.(*kueue.AdmissionCheckList); return ok }},
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

			features.SetFeatureGateDuringTest(t, features.MultiKueueMetricForClusterByStatus, true)
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

			cq2 := utiltestingapi.MakeClusterQueue("cq2").AdmissionChecks("ac2").Obj()
			ac2 := utiltestingapi.MakeAdmissionCheck("ac2").ControllerName(kueue.MultiKueueControllerName).Parameters(kueue.SchemeGroupVersion.Group, "MultiKueueConfig", "config2").Obj()
			cfg2 := utiltestingapi.MakeMultiKueueConfig("config2").Clusters("worker2").Obj()
			worker2 := utiltestingapi.MakeMultiKueueCluster("worker2").Active(metav1.ConditionFalse, "Disconnected", "Unavailable", 1).Obj()
			readsFail := false
			c := utiltesting.NewClientBuilder().
				WithObjects(cq, ac, cfg, worker1, cq2, ac2, cfg2, worker2).
				WithIndex(&kueue.AdmissionCheck{}, AdmissionCheckControllerNameKey, admissionCheckControllerNameIndexerFunc).
				WithIndex(&kueue.AdmissionCheck{}, AdmissionCheckUsingConfigKey, admissioncheck.IndexerByConfigFunction(kueue.MultiKueueControllerName, configGVK)).
				WithStatusSubresource(cq, ac, cq2, ac2, worker2).
				WithInterceptorFuncs(interceptor.Funcs{
					List: func(ctx context.Context, cl client.WithWatch, obj client.ObjectList, opts ...client.ListOption) error {
						if readsFail && tc.failList != nil && tc.failList(obj) {
							return readErr
						}
						return cl.List(ctx, obj, opts...)
					},
					Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
						if readsFail && tc.failRead != nil && tc.failRead(obj) && key.Name != "worker2" && key.Name != "config2" {
							return readErr
						}
						return cl.Get(ctx, key, obj, opts...)
					},
				}).
				Build()
			helper, _ := admissioncheck.NewMultiKueueStoreHelper(c)
			reconciler := newACReconciler(c, helper, nil)

			req := reconcile.Request{Name: "ac1"}
			if _, err := reconciler.Reconcile(ctx, req); err != nil {
				t.Fatalf("unexpected reconcile error: %v", err)
			}
			expectCQClusterStatusMetric(t, "cq1", "worker1", metav1.ConditionTrue)
			expectCQClusterStatusMetric(t, "cq2", "worker2", metav1.ConditionFalse)

			// A read failure must fail the reconcile so that it is retried, and must leave
			// the statuses reported so far in place: failing to read is not evidence that
			// the ClusterQueue stopped using its workers.
			worker2.Status.Conditions[0].Status = metav1.ConditionTrue
			if err := c.Status().Update(ctx, worker2); err != nil {
				t.Fatal(err)
			}
			readsFail = true
			if _, err := reconciler.Reconcile(ctx, req); !errors.Is(err, readErr) {
				t.Fatalf("expected the reconcile to fail with %q, got %v", readErr, err)
			}
			expectCQClusterStatusMetric(t, "cq1", "worker1", metav1.ConditionTrue)
			expectCQClusterStatusMetric(t, "cq2", "worker2", metav1.ConditionFalse)
		})
	}
}

func TestACReconcilerClearsClusterStatusMetricPerClusterQueue(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.MultiKueueMetricForClusterByStatus, true)
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
		WithStatusSubresource(cq1, cq2, ac).
		Build()
	helper, _ := admissioncheck.NewMultiKueueStoreHelper(c)
	reconciler := newACReconciler(c, helper, nil)

	req1 := reconcile.Request{Name: "ac1"}
	req2 := reconcile.Request{Name: "ac1"}
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

func TestACReconcilerClearsObsoleteClusterStatusMetrics(t *testing.T) {
	cases := map[string]struct {
		cq     *kueue.ClusterQueue
		ac     *kueue.AdmissionCheck
		config *kueue.MultiKueueConfig
	}{
		"deleted ClusterQueue":   {},
		"unlinked ClusterQueue":  {cq: utiltestingapi.MakeClusterQueue("cq").Obj()},
		"deleted AdmissionCheck": {cq: utiltestingapi.MakeClusterQueue("cq").AdmissionChecks("ac").Obj()},
		"different AdmissionCheck controller": {
			cq: utiltestingapi.MakeClusterQueue("cq").AdmissionChecks("ac").Obj(),
			ac: utiltestingapi.MakeAdmissionCheck("ac").ControllerName("other").Obj(),
		},
		"missing parameters": {
			cq: utiltestingapi.MakeClusterQueue("cq").AdmissionChecks("ac").Obj(),
			ac: utiltestingapi.MakeAdmissionCheck("ac").ControllerName(kueue.MultiKueueControllerName).Obj(),
		},
		"invalid parameters": {
			cq: utiltestingapi.MakeClusterQueue("cq").AdmissionChecks("ac").Obj(),
			ac: utiltestingapi.MakeAdmissionCheck("ac").ControllerName(kueue.MultiKueueControllerName).Parameters("other", "Other", "config").Obj(),
		},
		"deleted config": {
			cq: utiltestingapi.MakeClusterQueue("cq").AdmissionChecks("ac").Obj(),
			ac: utiltestingapi.MakeAdmissionCheck("ac").ControllerName(kueue.MultiKueueControllerName).Parameters(kueue.SchemeGroupVersion.Group, "MultiKueueConfig", "config").Obj(),
		},
		"empty config": {
			cq:     utiltestingapi.MakeClusterQueue("cq").AdmissionChecks("ac").Obj(),
			ac:     utiltestingapi.MakeAdmissionCheck("ac").ControllerName(kueue.MultiKueueControllerName).Parameters(kueue.SchemeGroupVersion.Group, "MultiKueueConfig", "config").Obj(),
			config: utiltestingapi.MakeMultiKueueConfig("config").Obj(),
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.MultiKueueMetricForClusterByStatus, true)
			features.SetFeatureGateDuringTest(t, features.DQOMultiKueueCapacityProvider, false)
			ctx, _ := utiltesting.ContextWithLog(t)
			builder := utiltesting.NewClientBuilder().WithStatusSubresource(&kueue.AdmissionCheck{}).WithObjects(
				utiltestingapi.MakeClusterQueue("other-cq").AdmissionChecks("other-ac").Obj(),
				utiltestingapi.MakeAdmissionCheck("other-ac").ControllerName(kueue.MultiKueueControllerName).Parameters(kueue.SchemeGroupVersion.Group, "MultiKueueConfig", "other-config").Obj(),
				utiltestingapi.MakeMultiKueueConfig("other-config").Clusters("worker").Obj(),
				utiltestingapi.MakeMultiKueueCluster("worker").Active(metav1.ConditionFalse, "Disconnected", "Unavailable", 1).Obj(),
			)
			if err := SetupIndexer(ctx, utiltesting.AsIndexer(builder), TestNamespace); err != nil {
				t.Fatal(err)
			}
			if tc.cq != nil {
				builder.WithObjects(tc.cq)
			}
			if tc.ac != nil {
				builder.WithObjects(tc.ac)
			}
			if tc.config != nil {
				builder.WithObjects(tc.config)
			}
			c := builder.Build()
			helper, err := admissioncheck.NewMultiKueueStoreHelper(c)
			if err != nil {
				t.Fatal(err)
			}
			metrics.MultiKueueClusterByStatus.Reset()
			t.Cleanup(metrics.MultiKueueClusterByStatus.Reset)
			metrics.ReportMultiKueueClusterStatus("cq", "worker", metav1.ConditionTrue, nil)
			metrics.ReportMultiKueueClusterStatus("other-cq", "worker", metav1.ConditionFalse, nil)
			r := newACReconciler(c, helper, nil)
			if _, err := r.Reconcile(ctx, reconcile.Request{Name: "ac"}); err != nil {
				t.Fatal(err)
			}
			if got := testutil.CollectAndCount(metrics.MultiKueueClusterByStatus); got != len(metrics.ConditionStatusValues) {
				t.Fatalf("Remaining series = %d, want %d", got, len(metrics.ConditionStatusValues))
			}
			expectCQClusterStatusMetric(t, "other-cq", "worker", metav1.ConditionFalse)
			if tc.cq != nil {
				got := &kueue.ClusterQueue{}
				if err := c.Get(ctx, client.ObjectKeyFromObject(tc.cq), got); err != nil {
					t.Fatal(err)
				}
				if diff := cmp.Diff(tc.cq, got); diff != "" {
					t.Errorf("Metric reconciliation changed ClusterQueue (-want,+got): %s", diff)
				}
			}
		})
	}
}

func TestACReconcilerReadsSharedWorkersOncePerSnapshot(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.MultiKueueMetricForClusterByStatus, true)
	metrics.MultiKueueClusterByStatus.Reset()
	t.Cleanup(metrics.MultiKueueClusterByStatus.Reset)
	ctx, _ := utiltesting.ContextWithLog(t)
	builder := utiltesting.NewClientBuilder().WithObjects(
		utiltestingapi.MakeMultiKueueConfig("config").Clusters("worker").Obj(),
		utiltestingapi.MakeMultiKueueCluster("worker").Active(metav1.ConditionTrue, "Active", "Connected", 1).Obj(),
	)
	const queueCount = 500
	for i := range queueCount {
		builder.WithObjects(utiltestingapi.MakeClusterQueue(fmt.Sprintf("cq-%d", i)).AdmissionChecks(kueue.AdmissionCheckReference(fmt.Sprintf("ac-%d", i%2))).Obj())
	}
	for i := range 2 {
		builder.WithObjects(
			utiltestingapi.MakeAdmissionCheck(fmt.Sprintf("ac-%d", i)).ControllerName(kueue.MultiKueueControllerName).Parameters(kueue.SchemeGroupVersion.Group, "MultiKueueConfig", "config").Obj(),
		)
	}
	if err := SetupIndexer(ctx, utiltesting.AsIndexer(builder), TestNamespace); err != nil {
		t.Fatal(err)
	}
	reads := 0
	c := builder.WithInterceptorFuncs(interceptor.Funcs{Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
		if _, ok := obj.(*kueue.MultiKueueCluster); ok {
			reads++
		}
		return cl.Get(ctx, key, obj, opts...)
	}}).Build()
	helper, err := admissioncheck.NewMultiKueueStoreHelper(c)
	if err != nil {
		t.Fatal(err)
	}
	r := newACReconciler(c, helper, nil)
	if _, err := r.Reconcile(ctx, reconcile.Request{Name: "deleted-ac"}); err != nil {
		t.Fatal(err)
	}
	if reads != 1 {
		t.Errorf("Shared worker reads = %d, want 1", reads)
	}
	if got := testutil.CollectAndCount(metrics.MultiKueueClusterByStatus); got != queueCount*len(metrics.ConditionStatusValues) {
		t.Errorf("Metric series = %d, want %d", got, queueCount*len(metrics.ConditionStatusValues))
	}
}

func BenchmarkACReconcilerClusterStatusSnapshot(b *testing.B) {
	for _, queueCount := range []int{100, 1000} {
		b.Run(fmt.Sprintf("queues-%d", queueCount), func(b *testing.B) {
			features.SetFeatureGateDuringTest(b, features.MultiKueueMetricForClusterByStatus, true)
			metrics.MultiKueueClusterByStatus.Reset()
			b.Cleanup(metrics.MultiKueueClusterByStatus.Reset)
			ctx, _ := utiltesting.ContextWithLog(b)
			builder := utiltesting.NewClientBuilder().WithObjects(
				utiltestingapi.MakeAdmissionCheck("ac").ControllerName(kueue.MultiKueueControllerName).Parameters(kueue.SchemeGroupVersion.Group, "MultiKueueConfig", "config").Obj(),
				utiltestingapi.MakeMultiKueueConfig("config").Clusters("worker").Obj(),
				utiltestingapi.MakeMultiKueueCluster("worker").Active(metav1.ConditionTrue, "Active", "Connected", 1).Obj(),
			)
			for i := range queueCount {
				builder.WithObjects(utiltestingapi.MakeClusterQueue(fmt.Sprintf("cq-%d", i)).AdmissionChecks("ac").Obj())
			}
			if err := SetupIndexer(ctx, utiltesting.AsIndexer(builder), TestNamespace); err != nil {
				b.Fatal(err)
			}
			c := builder.Build()
			helper, err := admissioncheck.NewMultiKueueStoreHelper(c)
			if err != nil {
				b.Fatal(err)
			}
			r := newACReconciler(c, helper, nil)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if _, err := r.Reconcile(ctx, reconcile.Request{Name: "deleted-ac"}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
