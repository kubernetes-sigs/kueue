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
	"slices"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/google/go-cmp/cmp"
	"github.com/prometheus/client_golang/prometheus"
	corev1 "k8s.io/api/core/v1"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingmetrics "sigs.k8s.io/kueue/pkg/util/testing/metrics"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

func admittedWorkload(name, kindLabel string, cqName kueue.ClusterQueueReference, now time.Time) *utiltestingapi.WorkloadWrapper {
	return utiltestingapi.MakeWorkload(name, "ns").
		Label("kind", kindLabel).
		Request(corev1.ResourceCPU, "1").
		SimpleReserveQuota(cqName, "default", now).
		AdmittedAt(true, now)
}

// customLabelsCleanupCache builds a cache configured with one ClusterQueue label
// and one Workload label, mirroring the shape of a real configuration. The
// Workload entry is the one at risk of accumulating values, since every workload
// adds an entry to the store.
func customLabelsCleanupCache(t *testing.T) (*Cache, *metrics.CustomLabels) {
	t.Helper()
	customLabels := metrics.NewCustomLabels([]configapi.ControllerMetricsCustomLabel{
		utiltestingapi.MakeCustomLabel("team").SourceKind(configapi.SourceKindClusterQueue).Obj(),
		utiltestingapi.MakeCustomLabel("wl_kind").SourceLabelKey("kind").
			SourceKind(configapi.SourceKindWorkload).TrackedValues("training", "inference").Obj(),
	})
	t.Cleanup(func() {
		metrics.InitMetricVectors(nil)
	})
	return New(utiltesting.NewFakeClient(), WithCustomLabels(customLabels)), customLabels
}

func makeCleanupClusterQueue(name string) *kueue.ClusterQueue {
	return utiltestingapi.MakeClusterQueue(name).
		ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "10").Obj()).
		Label("team", "platform").
		Obj()
}

// TestWorkloadCustomLabelsCleanup verifies that the cached custom label values of
// a Workload are dropped from the store whenever the cache stops tracking that
// Workload. The store is keyed by workload and shared by all ClusterQueues, so a
// missed deletion keeps user-supplied label values alive for the lifetime of the
// process.
func TestWorkloadCustomLabelsCleanup(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.CustomMetricLabels, true)
	now := time.Now()
	wl1Key := workload.Key(admittedWorkload("wl1", "training", "cq", now).Obj())

	cases := map[string]struct {
		operation func(log logr.Logger, cache *Cache, cq *kueue.ClusterQueue) error
		wantRefs  []string
	}{
		"workload deleted from the cache": {
			operation: func(log logr.Logger, cache *Cache, _ *kueue.ClusterQueue) error {
				return cache.DeleteWorkload(log, wl1Key)
			},
			wantRefs: []string{"ns/wl2"},
		},
		"workload finished": {
			operation: func(log logr.Logger, cache *Cache, _ *kueue.ClusterQueue) error {
				cache.AddOrUpdateWorkload(log, admittedWorkload("wl1", "training", "cq", now).Finished().Obj())
				return nil
			},
			wantRefs: []string{"ns/wl2"},
		},
		"workload deactivated": {
			operation: func(log logr.Logger, cache *Cache, _ *kueue.ClusterQueue) error {
				cache.AddOrUpdateWorkload(log, admittedWorkload("wl1", "training", "cq", now).Active(false).Obj())
				return nil
			},
			wantRefs: []string{"ns/wl2"},
		},
		// The workload is still tracked, just by another ClusterQueue: the entry
		// must be re-stored rather than dropped when it moves.
		"workload moved to another ClusterQueue": {
			operation: func(log logr.Logger, cache *Cache, _ *kueue.ClusterQueue) error {
				cache.AddOrUpdateWorkload(log, admittedWorkload("wl1", "training", "other-cq", now).Obj())
				return nil
			},
			wantRefs: []string{"ns/wl1", "ns/wl2"},
		},
		"ClusterQueue holding the workloads deleted": {
			operation: func(_ logr.Logger, cache *Cache, cq *kueue.ClusterQueue) error {
				cache.DeleteClusterQueue(cq)
				return nil
			},
			wantRefs: nil,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, log := utiltesting.ContextWithLog(t)
			cache, customLabels := customLabelsCleanupCache(t)

			cq := makeCleanupClusterQueue("cq")
			for _, q := range []*kueue.ClusterQueue{cq, makeCleanupClusterQueue("other-cq")} {
				if err := cache.AddClusterQueue(ctx, q); err != nil {
					t.Fatalf("Failed to add ClusterQueue %s: %v", q.Name, err)
				}
			}
			cache.AddOrUpdateWorkload(log, admittedWorkload("wl1", "training", "cq", now).Obj())
			cache.AddOrUpdateWorkload(log, admittedWorkload("wl2", "inference", "cq", now).Obj())
			if diff := cmp.Diff([]string{"ns/wl1", "ns/wl2"}, storedWorkloadRefs(customLabels)); diff != "" {
				t.Fatalf("Unexpected stored workload references before the operation (-want +got):\n%s", diff)
			}

			if err := tc.operation(log, cache, cq); err != nil {
				t.Fatalf("Operation failed: %v", err)
			}

			if diff := cmp.Diff(tc.wantRefs, storedWorkloadRefs(customLabels)); diff != "" {
				t.Errorf("Unexpected stored workload references (-want +got):\n%s", diff)
			}
		})
	}
}

// TestDeleteClusterQueueClearsCustomLabelledSeries verifies that removing a
// ClusterQueue removes its metric series whatever custom label values they
// carry, so that no queue or workload metadata is left exposed at /metrics.
func TestDeleteClusterQueueClearsCustomLabelledSeries(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.CustomMetricLabels, true)
	ctx, log := utiltesting.ContextWithLog(t)
	now := time.Now()

	cache, customLabels := customLabelsCleanupCache(t)
	cq := makeCleanupClusterQueue("cq")
	customLabels.CQStore(kueue.ClusterQueueReference(cq.Name), cq.Labels, cq.Annotations)
	if err := cache.AddClusterQueue(ctx, cq); err != nil {
		t.Fatalf("Failed to add ClusterQueue: %v", err)
	}
	cache.AddOrUpdateWorkload(log, admittedWorkload("wl1", "training", "cq", now).Obj())
	cache.AddOrUpdateWorkload(log, admittedWorkload("wl2", "inference", "cq", now).Obj())

	cqSeries := map[string]string{"cluster_queue": cq.Name}
	cqScopedMetrics := map[string]*prometheus.GaugeVec{
		"kueue_admitted_active_workloads":  metrics.AdmittedActiveWorkloads,
		"kueue_reserving_active_workloads": metrics.ReservingActiveWorkloads,
		"kueue_cluster_queue_status":       metrics.ClusterQueueByStatus,
	}

	// Checked before the deletion so that the assertions below cannot pass just
	// because a vector never held a series for this ClusterQueue.
	for name, vec := range cqScopedMetrics {
		if got := testingmetrics.CollectFilteredGaugeVec(vec, cqSeries); len(got) == 0 {
			t.Fatalf("Expected %s to hold series for the ClusterQueue before the deletion", name)
		}
	}
	// kueue_admitted_active_workloads splits per distinct workload label value.
	if got := testingmetrics.CollectFilteredGaugeVec(metrics.AdmittedActiveWorkloads, cqSeries); len(got) != 2 {
		t.Fatalf("Expected 2 kueue_admitted_active_workloads series before the deletion, got %v", got)
	}

	cache.DeleteClusterQueue(cq)

	for name, vec := range cqScopedMetrics {
		if got := testingmetrics.CollectFilteredGaugeVec(vec, cqSeries); len(got) != 0 {
			t.Errorf("Expected no %s series for the deleted ClusterQueue, got %v", name, got)
		}
	}
}

func storedWorkloadRefs(cl *metrics.CustomLabels) []string {
	refs := cl.StoredRefsForTest(configapi.SourceKindWorkload)
	slices.Sort(refs)
	return refs
}
