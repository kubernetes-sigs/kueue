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

package scheduler

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/component-base/metrics/testutil"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/util/queue"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingmetrics "sigs.k8s.io/kueue/pkg/util/testing/metrics"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestPodsReadyActiveWorkloads(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	role := roletracker.RoleStandalone

	makeReservedWorkload := func(name, queueName string) *utiltestingapi.WorkloadWrapper {
		return utiltestingapi.MakeWorkload(name, metav1.NamespaceDefault).
			UID(types.UID(name+"-uid")).
			Queue(kueue.LocalQueueName(queueName)).
			Request(corev1.ResourceCPU, "1").
			ReserveQuotaAt(
				utiltestingapi.MakeAdmission("cq").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "default", "1").Obj()).Obj(),
				now,
			)
	}

	t.Run("CustomMetricLabels disabled lifecycle, flapping, deactivation, finish, eviction, and ForgetWorkload", func(t *testing.T) {
		ctx, log := utiltesting.ContextWithLog(t)
		t.Cleanup(func() { metrics.InitMetricVectors(nil) })
		features.SetFeatureGateDuringTest(t, features.CustomMetricLabels, false)
		features.SetFeatureGateDuringTest(t, features.LocalQueueMetrics, true)
		metrics.InitMetricVectors(nil)

		cache := New(
			utiltesting.NewFakeClient(),
			WithLocalQueueMetrics(&metrics.LocalQueueMetricsConfig{
				Enabled:       true,
				QueueSelector: labels.Everything(),
			}),
		)
		cache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("default").Obj())

		cq := utiltestingapi.MakeClusterQueue("cq").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "5").Obj()).
			Obj()
		if err := cache.AddClusterQueue(ctx, cq); err != nil {
			t.Fatalf("Failed to add ClusterQueue: %v", err)
		}
		lq := utiltestingapi.MakeLocalQueue("lq", metav1.NamespaceDefault).ClusterQueue("cq").Obj()
		if err := cache.AddLocalQueue(lq); err != nil {
			t.Fatalf("Failed to add LocalQueue: %v", err)
		}

		expectGauges := func(step string, want float64) {
			t.Helper()
			gotCQ, err := testutil.GetGaugeMetricValue(metrics.PodsReadyActiveWorkloads.WithLabelValues("cq", role))
			if err != nil || gotCQ != want {
				t.Fatalf("%s: pods_ready_active_workloads = %v (err %v), want %v", step, gotCQ, err, want)
			}
			gotLQ, err := testutil.GetGaugeMetricValue(metrics.LocalQueuePodsReadyActiveWorkloads.WithLabelValues("lq", metav1.NamespaceDefault, role))
			if err != nil || gotLQ != want {
				t.Fatalf("%s: local_queue_pods_ready_active_workloads = %v (err %v), want %v", step, gotLQ, err, want)
			}
		}

		// Quota-reserving workload that is NOT yet Admitted, even with a stale PodsReady=True condition, must report 0.
		wl1 := makeReservedWorkload("wl1", "lq").PodsReadyAt(true, now).Obj()
		cache.AddOrUpdateWorkload(ctx, log, wl1)
		expectGauges("unadmitted with stale PodsReady=True", 0)

		// Admitted with PodsReady=False -> 0.
		wl1AdmittedNotReady := makeReservedWorkload("wl1", "lq").AdmittedAt(true, now).PodsReadyAt(false, now).Obj()
		cache.AddOrUpdateWorkload(ctx, log, wl1AdmittedNotReady)
		expectGauges("admitted with PodsReady=False", 0)

		// Flapping False -> True -> False -> True -> 1 -> 0 -> 1.
		wl1Ready := makeReservedWorkload("wl1", "lq").AdmittedAt(true, now).PodsReadyAt(true, now).Obj()
		cache.AddOrUpdateWorkload(ctx, log, wl1Ready)
		expectGauges("PodsReady False -> True", 1)

		cache.AddOrUpdateWorkload(ctx, log, wl1AdmittedNotReady)
		expectGauges("PodsReady True -> False", 0)

		cache.AddOrUpdateWorkload(ctx, log, wl1Ready)
		expectGauges("PodsReady False -> True again", 1)

		// Second admitted workload with PodsReady=True -> 2.
		wl2Ready := makeReservedWorkload("wl2", "lq").AdmittedAt(true, now).PodsReadyAt(true, now).Obj()
		cache.AddOrUpdateWorkload(ctx, log, wl2Ready)
		expectGauges("two PodsReady workloads", 2)

		// Finishing wl1 while PodsReady=True remains on status -> drops to 1.
		wl1Finished := makeReservedWorkload("wl1", "lq").AdmittedAt(true, now).PodsReadyAt(true, now).Finished().Obj()
		cache.AddOrUpdateWorkload(ctx, log, wl1Finished)
		expectGauges("after wl1 finished", 1)

		// Deactivating wl2 while PodsReady=True -> drops to 0.
		wl2Deactivated := makeReservedWorkload("wl2", "lq").AdmittedAt(true, now).PodsReadyAt(true, now).Active(false).Obj()
		cache.AddOrUpdateWorkload(ctx, log, wl2Deactivated)
		expectGauges("after wl2 deactivated", 0)

		// Eviction (DeleteWorkload) while PodsReady=True -> drops back to 0.
		wl3Ready := makeReservedWorkload("wl3", "lq").AdmittedAt(true, now).PodsReadyAt(true, now).Obj()
		cache.AddOrUpdateWorkload(ctx, log, wl3Ready)
		expectGauges("after wl3 ready", 1)
		if err := cache.DeleteWorkload(log, workload.Key(wl3Ready)); err != nil {
			t.Fatalf("DeleteWorkload failed: %v", err)
		}
		expectGauges("after wl3 evicted", 0)

		// ForgetWorkload where the passed-in deleted tombstone lacks conditions -> still drops cached PodsReady=True count to 0.
		wl4Ready := makeReservedWorkload("wl4", "lq").AdmittedAt(true, now).PodsReadyAt(true, now).Obj()
		cache.AddOrUpdateWorkload(ctx, log, wl4Ready)
		expectGauges("after wl4 ready", 1)
		tombstoneWl4 := utiltestingapi.MakeWorkload("wl4", metav1.NamespaceDefault).UID(types.UID("wl4-uid")).Obj()
		if _, err := cache.ForgetWorkload(log, tombstoneWl4); err != nil {
			t.Fatalf("ForgetWorkload failed: %v", err)
		}
		expectGauges("after wl4 forgotten via bare tombstone", 0)
	})

	t.Run("LocalQueueMetrics selector, late AddLocalQueue, and selector opt-in zero pruning", func(t *testing.T) {
		ctx, log := utiltesting.ContextWithLog(t)
		t.Cleanup(func() { metrics.InitMetricVectors(nil) })
		features.SetFeatureGateDuringTest(t, features.CustomMetricLabels, true)
		features.SetFeatureGateDuringTest(t, features.LocalQueueMetrics, true)

		customLabels := metrics.NewCustomLabels([]configapi.ControllerMetricsCustomLabel{
			utiltestingapi.MakeCustomLabel("wl_kind").SourceLabelKey("kind").
				SourceKind(configapi.SourceKindWorkload).TrackedValues("training", "inference").Obj(),
		})
		selector, err := metav1.LabelSelectorAsSelector(&metav1.LabelSelector{
			MatchLabels: map[string]string{"expose": "true"},
		})
		if err != nil {
			t.Fatalf("Invalid selector: %v", err)
		}
		cache := New(
			utiltesting.NewFakeClient(),
			WithCustomLabels(customLabels),
			WithLocalQueueMetrics(&metrics.LocalQueueMetricsConfig{
				Enabled:       true,
				QueueSelector: selector,
			}),
		)
		cache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("default").Obj())

		cq := utiltestingapi.MakeClusterQueue("cq").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "5").Obj()).
			Obj()
		if err := cache.AddClusterQueue(ctx, cq); err != nil {
			t.Fatalf("Failed to add ClusterQueue: %v", err)
		}

		// Add running workloads BEFORE LocalQueues are added to the cache.
		wlExposed := makeReservedWorkload("wl-exposed", "lq-exposed").Label("kind", "training").AdmittedAt(true, now).PodsReadyAt(true, now).Obj()
		wlHidden := makeReservedWorkload("wl-hidden", "lq-hidden").Label("kind", "inference").AdmittedAt(true, now).PodsReadyAt(true, now).Obj()
		cache.AddOrUpdateWorkload(ctx, log, wlExposed)
		cache.AddOrUpdateWorkload(ctx, log, wlHidden)

		lqExposed := utiltestingapi.MakeLocalQueue("lq-exposed", metav1.NamespaceDefault).
			ClusterQueue("cq").Label("expose", "true").Obj()
		lqHidden := utiltestingapi.MakeLocalQueue("lq-hidden", metav1.NamespaceDefault).
			ClusterQueue("cq").Label("expose", "false").Obj()
		if err := cache.AddLocalQueue(lqExposed); err != nil {
			t.Fatalf("Failed to add exposed LocalQueue: %v", err)
		}
		if err := cache.AddLocalQueue(lqHidden); err != nil {
			t.Fatalf("Failed to add hidden LocalQueue: %v", err)
		}

		if got, err := testutil.GetGaugeMetricValue(metrics.LocalQueuePodsReadyActiveWorkloads.WithLabelValues("lq-exposed", metav1.NamespaceDefault, role, "training")); err != nil || got != 1 {
			t.Fatalf("local_queue_pods_ready_active_workloads(lq-exposed) = %v (err %v), want 1", got, err)
		}
		if hiddenSeries := testingmetrics.CollectFilteredGaugeVec(metrics.LocalQueuePodsReadyActiveWorkloads, map[string]string{
			"name":      "lq-hidden",
			"namespace": metav1.NamespaceDefault,
		}); len(hiddenSeries) != 0 {
			t.Fatalf("expected no metric series for non-matching lq-hidden, got %v", hiddenSeries)
		}

		// Finish wl-hidden ("inference") while lq-hidden is still excluded by QueueSelector,
		// then opt lq-hidden into QueueSelector and add a "training" workload:
		// the finished "inference" label set must NOT be resurrected on lq-hidden.
		wlHiddenFinished := makeReservedWorkload("wl-hidden", "lq-hidden").Label("kind", "inference").AdmittedAt(true, now).PodsReadyAt(true, now).Finished().Obj()
		cache.AddOrUpdateWorkload(ctx, log, wlHiddenFinished)

		lqNowExposed := lqHidden.DeepCopy()
		lqNowExposed.Labels["expose"] = "true"
		if err := cache.UpdateLocalQueue(lqHidden, lqNowExposed); err != nil {
			t.Fatalf("Failed to update lq-hidden: %v", err)
		}
		cache.ResyncLocalQueueGaugeMetrics("cq", queue.Key(lqNowExposed))

		wlNewTraining := makeReservedWorkload("wl-hidden-training", "lq-hidden").Label("kind", "training").AdmittedAt(true, now).PodsReadyAt(true, now).Obj()
		cache.AddOrUpdateWorkload(ctx, log, wlNewTraining)

		wantOptInSeries := []testingmetrics.MetricDataPoint{{
			Labels: map[string]string{
				"name":           "lq-hidden",
				"namespace":      metav1.NamespaceDefault,
				"replica_role":   role,
				"custom_wl_kind": "training",
			},
			Value: 1,
		}}
		gotOptInSeries := testingmetrics.CollectFilteredGaugeVec(metrics.LocalQueuePodsReadyActiveWorkloads, map[string]string{
			"name":      "lq-hidden",
			"namespace": metav1.NamespaceDefault,
		})
		if diff := cmp.Diff(wantOptInSeries, gotOptInSeries); diff != "" {
			t.Fatalf("Unexpected lq-hidden series after selector opt-in (-want,+got):\n%s", diff)
		}
	})

	t.Run("CustomMetricLabels with ClusterQueue, LocalQueue, and Workload labels including relabel and late AddLocalQueue", func(t *testing.T) {
		ctx, log := utiltesting.ContextWithLog(t)
		t.Cleanup(func() { metrics.InitMetricVectors(nil) })
		features.SetFeatureGateDuringTest(t, features.CustomMetricLabels, true)
		features.SetFeatureGateDuringTest(t, features.LocalQueueMetrics, true)

		customLabels := metrics.NewCustomLabels([]configapi.ControllerMetricsCustomLabel{
			utiltestingapi.MakeCustomLabel("cq_team").SourceLabelKey("team").SourceKind(configapi.SourceKindClusterQueue).Obj(),
			utiltestingapi.MakeCustomLabel("lq_team").SourceLabelKey("team").SourceKind(configapi.SourceKindLocalQueue).Obj(),
			utiltestingapi.MakeCustomLabel("wl_kind").SourceLabelKey("kind").SourceKind(configapi.SourceKindWorkload).TrackedValues("training", "inference").Obj(),
		})
		cache := New(
			utiltesting.NewFakeClient(),
			WithCustomLabels(customLabels),
			WithLocalQueueMetrics(&metrics.LocalQueueMetricsConfig{
				Enabled:       true,
				QueueSelector: labels.Everything(),
			}),
		)
		cache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("default").Obj())

		cq := utiltestingapi.MakeClusterQueue("cq").
			Label("team", "ml").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "5").Obj()).
			Obj()
		customLabels.CQStore("cq", cq.Labels, cq.Annotations)
		if err := cache.AddClusterQueue(ctx, cq); err != nil {
			t.Fatalf("Failed to add ClusterQueue: %v", err)
		}

		// Add running workload BEFORE AddLocalQueue to verify addLocalQueue populates LabelValsTracker.
		wl1 := makeReservedWorkload("wl1", "lq").Label("kind", "training").AdmittedAt(true, now).PodsReadyAt(true, now).Obj()
		wlUntracked := makeReservedWorkload("wl-untracked", "lq").Label("kind", "batch").AdmittedAt(true, now).PodsReadyAt(true, now).Obj()
		cache.AddOrUpdateWorkload(ctx, log, wl1)
		cache.AddOrUpdateWorkload(ctx, log, wlUntracked)

		lq := utiltestingapi.MakeLocalQueue("lq", metav1.NamespaceDefault).
			Label("team", "data").ClusterQueue("cq").Obj()
		if err := cache.AddLocalQueue(lq); err != nil {
			t.Fatalf("Failed to add LocalQueue: %v", err)
		}

		expectLabeled := func(step, wlKind string, want float64) {
			t.Helper()
			gotCQ, err := testutil.GetGaugeMetricValue(metrics.PodsReadyActiveWorkloads.WithLabelValues("cq", role, "ml", wlKind))
			if err != nil || gotCQ != want {
				t.Fatalf("%s: pods_ready_active_workloads{wl_kind=%q} = %v (err %v), want %v", step, wlKind, gotCQ, err, want)
			}
			gotLQ, err := testutil.GetGaugeMetricValue(metrics.LocalQueuePodsReadyActiveWorkloads.WithLabelValues("lq", metav1.NamespaceDefault, role, "data", wlKind))
			if err != nil || gotLQ != want {
				t.Fatalf("%s: local_queue_pods_ready_active_workloads{wl_kind=%q} = %v (err %v), want %v", step, wlKind, gotLQ, err, want)
			}
		}

		expectLabeled("initial training", "training", 1)
		expectLabeled("initial untracked", configapi.UntrackedCustomLabelValue, 1)

		// Relabel wl1 in place from training -> inference while PodsReady=True.
		wl1Relabeled := wl1.DeepCopy()
		wl1Relabeled.Labels["kind"] = "inference"
		cache.AddOrUpdateWorkload(ctx, log, wl1Relabeled)

		expectLabeled("after relabel training", "training", 0)
		expectLabeled("after relabel inference", "inference", 1)
		expectLabeled("after relabel untracked", configapi.UntrackedCustomLabelValue, 1)

		// Delete both workloads -> all series drop to 0.
		if err := cache.DeleteWorkload(log, workload.Key(wl1Relabeled)); err != nil {
			t.Fatalf("DeleteWorkload(wl1) failed: %v", err)
		}
		if err := cache.DeleteWorkload(log, workload.Key(wlUntracked)); err != nil {
			t.Fatalf("DeleteWorkload(wlUntracked) failed: %v", err)
		}
		expectLabeled("after delete inference", "inference", 0)
		expectLabeled("after delete untracked", configapi.UntrackedCustomLabelValue, 0)
	})

	t.Run("ConcurrentAdmission ignores parent workload and counts variant workload", func(t *testing.T) {
		ctx, log := utiltesting.ContextWithLog(t)
		t.Cleanup(func() { metrics.InitMetricVectors(nil) })
		features.SetFeatureGateDuringTest(t, features.CustomMetricLabels, false)
		features.SetFeatureGateDuringTest(t, features.LocalQueueMetrics, true)
		features.SetFeatureGateDuringTest(t, features.ConcurrentAdmission, true)
		metrics.InitMetricVectors(nil)

		cache := New(
			utiltesting.NewFakeClient(),
			WithLocalQueueMetrics(&metrics.LocalQueueMetricsConfig{
				Enabled:       true,
				QueueSelector: labels.Everything(),
			}),
		)
		cache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("default").Obj())

		cq := utiltestingapi.MakeClusterQueue("cq").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "5").Obj()).
			Obj()
		cq.Spec.ConcurrentAdmissionPolicy = &kueue.ConcurrentAdmissionPolicy{}
		if err := cache.AddClusterQueue(ctx, cq); err != nil {
			t.Fatalf("Failed to add ClusterQueue: %v", err)
		}
		lq := utiltestingapi.MakeLocalQueue("lq", metav1.NamespaceDefault).ClusterQueue("cq").Obj()
		if err := cache.AddLocalQueue(lq); err != nil {
			t.Fatalf("Failed to add LocalQueue: %v", err)
		}

		parent := makeReservedWorkload("parent", "lq").AdmittedAt(true, now).PodsReadyAt(true, now).Obj()
		if added := cache.AddOrUpdateWorkload(ctx, log, parent); added {
			t.Fatalf("Expected non-variant parent workload to be skipped on ConcurrentAdmission CQ")
		}
		if got, err := testutil.GetGaugeMetricValue(metrics.PodsReadyActiveWorkloads.WithLabelValues("cq", role)); err != nil || got != 0 {
			t.Fatalf("pods_ready_active_workloads after parent = %v (err %v), want 0", got, err)
		}

		variant := makeReservedWorkload("variant", "lq").AdmittedAt(true, now).PodsReadyAt(true, now).Obj()
		variant.OwnerReferences = []metav1.OwnerReference{{
			APIVersion: kueue.SchemeGroupVersion.String(),
			Kind:       "Workload",
			Name:       "parent",
			UID:        "parent-uid",
		}}
		if added := cache.AddOrUpdateWorkload(ctx, log, variant); !added {
			t.Fatalf("Expected variant workload to be added on ConcurrentAdmission CQ")
		}
		if got, err := testutil.GetGaugeMetricValue(metrics.PodsReadyActiveWorkloads.WithLabelValues("cq", role)); err != nil || got != 1 {
			t.Fatalf("pods_ready_active_workloads after variant = %v (err %v), want 1", got, err)
		}
		if got, err := testutil.GetGaugeMetricValue(metrics.LocalQueuePodsReadyActiveWorkloads.WithLabelValues("lq", metav1.NamespaceDefault, role)); err != nil || got != 1 {
			t.Fatalf("local_queue_pods_ready_active_workloads after variant = %v (err %v), want 1", got, err)
		}
	})
}
