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
	"context"
	"testing"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"
	corev1 "k8s.io/api/core/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	kueuemetrics "sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/resources"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingmetrics "sigs.k8s.io/kueue/pkg/util/testing/metrics"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestRecordCohortMetrics_Guards(t *testing.T) {
	type wantPoint struct {
		cohort       kueue.CohortReference
		fr           resources.FlavorResource
		quota        float64
		reservations float64
	}

	cohortLeft := kueue.CohortReference("record-left")
	cohortRight := kueue.CohortReference("record-right")
	cohortRoot := kueue.CohortReference("record-root")
	fr := resources.FlavorResource{Flavor: "flavor1", Resource: corev1.ResourceCPU}

	cases := []struct {
		name           string
		cohortToRecord kueue.CohortReference
		setup          func(t *testing.T, ctx context.Context, log logr.Logger, cache *Cache) []wantPoint
		wantNoMetrics  []kueue.CohortReference
	}{
		{
			name:           "Empty cohort name skips recording",
			cohortToRecord: "",
			setup:          func(t *testing.T, ctx context.Context, log logr.Logger, cache *Cache) []wantPoint { return nil },
			wantNoMetrics:  []kueue.CohortReference{"empty-sentinel"},
		},
		{
			name:           "Unknown cohort skips recording",
			cohortToRecord: "missing-cohort",
			setup:          func(t *testing.T, ctx context.Context, log logr.Logger, cache *Cache) []wantPoint { return nil },
			wantNoMetrics:  []kueue.CohortReference{"missing-cohort"},
		},
		{
			name:           "Records target cohort and ancestors only",
			cohortToRecord: "record-left",
			setup: func(t *testing.T, ctx context.Context, log logr.Logger, cache *Cache) []wantPoint {
				t.Helper()

				setupRecordMetricsHierarchy(
					ctx, t, log, cache,
					[]*kueue.ResourceFlavor{
						utiltestingapi.MakeResourceFlavor("flavor1").Obj(),
					},
					[]*kueue.Cohort{
						utiltestingapi.MakeCohort(cohortRoot).
							ResourceGroup(*utiltestingapi.MakeFlavorQuotas("flavor1").
								Resource(corev1.ResourceCPU, "30").
								Obj()).
							Obj(),
						utiltestingapi.MakeCohort(cohortLeft).
							Parent(cohortRoot).
							ResourceGroup(*utiltestingapi.MakeFlavorQuotas("flavor1").
								Resource(corev1.ResourceCPU, "20").
								Obj()).
							Obj(),
						utiltestingapi.MakeCohort(cohortRight).
							Parent(cohortRoot).
							ResourceGroup(*utiltestingapi.MakeFlavorQuotas("flavor1").
								Resource(corev1.ResourceCPU, "10").
								Obj()).
							Obj(),
					},
					[]*kueue.ClusterQueue{
						utiltestingapi.MakeClusterQueue("record-left-cq").
							Cohort(cohortLeft).
							ResourceGroup(*utiltestingapi.MakeFlavorQuotas("flavor1").
								Resource(corev1.ResourceCPU, "10").
								Obj()).
							Obj(),
						utiltestingapi.MakeClusterQueue("record-right-cq").
							Cohort(cohortRight).
							ResourceGroup(*utiltestingapi.MakeFlavorQuotas("flavor1").
								Resource(corev1.ResourceCPU, "5").
								Obj()).
							Obj(),
					},
				)

				return []wantPoint{
					{
						cohort: cohortLeft,
						fr:     fr,
						quota:  resourceFloat(fr.Resource, cache.hm.Cohort(cohortLeft).resourceNode.SubtreeQuota[fr]),
					},
					{
						cohort: cohortRoot,
						fr:     fr,
						quota:  resourceFloat(fr.Resource, cache.hm.Cohort(cohortRoot).resourceNode.SubtreeQuota[fr]),
					},
				}
			},
			wantNoMetrics: []kueue.CohortReference{cohortRight},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, log := utiltesting.ContextWithLog(t)
			cache := New(utiltesting.NewFakeClient())

			wantPoints := tc.setup(t, ctx, log, cache)

			// Ensure clean slate for all cohorts referenced in this test case.
			cohortsToClear := make([]kueue.CohortReference, 0, len(tc.wantNoMetrics)+len(wantPoints)+1)
			cohortsToClear = append(cohortsToClear, tc.wantNoMetrics...)
			cohortsToClear = append(cohortsToClear, tc.cohortToRecord)
			for _, p := range wantPoints {
				cohortsToClear = append(cohortsToClear, p.cohort)
			}
			clearCohortMetricsForTest(t, cohortsToClear...)

			cache.RecordCohortMetrics(log, tc.cohortToRecord)

			for _, want := range wantPoints {
				labels := cohortMetricLabels(want.cohort, want.fr)
				expectGaugeValue(t, kueuemetrics.CohortSubtreeQuota, labels, want.quota)
				if want.reservations != 0 {
					expectGaugeValue(t, kueuemetrics.CohortSubtreeResourceReservations, labels, want.reservations)
				} else {
					expectGaugeCount(t, kueuemetrics.CohortSubtreeResourceReservations, 0, labels)
				}
			}

			for _, cohortName := range tc.wantNoMetrics {
				expectGaugeCount(t, kueuemetrics.CohortSubtreeQuota, 0, map[string]string{"cohort": string(cohortName)})
				expectGaugeCount(t, kueuemetrics.CohortSubtreeResourceReservations, 0, map[string]string{"cohort": string(cohortName)})
			}
		})
	}
}

func TestCohortMetrics_QuotaHierarchyScenarios(t *testing.T) {
	frDefaultCPU := resources.FlavorResource{Flavor: "default", Resource: corev1.ResourceCPU}
	frFlavor1CPU := resources.FlavorResource{Flavor: "flavor1", Resource: corev1.ResourceCPU}
	frFlavor1GPU := resources.FlavorResource{Flavor: "flavor1", Resource: corev1.ResourceName("nvidia.com/gpu")}

	testCases := []struct {
		name string
		run  func(t *testing.T, cache *Cache, log logr.Logger)
	}{
		{
			name: "RecordCohortMetrics_QuotaHierarchyLikeIntegration",
			run: func(t *testing.T, cache *Cache, log logr.Logger) {
				cache.RecordCohortMetrics(log, "ch1")
				cache.RecordCohortMetrics(log, "ch2")
				cache.RecordCohortMetrics(log, "ch3")

				expectGaugeValue(t, kueuemetrics.CohortSubtreeQuota, cohortMetricLabels("ch1", frDefaultCPU), 30)
				expectGaugeValue(t, kueuemetrics.CohortSubtreeQuota, cohortMetricLabels("ch2", frDefaultCPU), 20)
				expectGaugeValue(t, kueuemetrics.CohortSubtreeQuota, cohortMetricLabels("ch3", frFlavor1CPU), 5)
				expectGaugeValue(t, kueuemetrics.CohortSubtreeQuota, cohortMetricLabels("ch3", frFlavor1GPU), 1)

				expectGaugeValue(t, kueuemetrics.CohortSubtreeQuota, cohortMetricLabels("root", frDefaultCPU), 50)
				expectGaugeValue(t, kueuemetrics.CohortSubtreeQuota, cohortMetricLabels("root", frFlavor1CPU), 25)
				expectGaugeValue(t, kueuemetrics.CohortSubtreeQuota, cohortMetricLabels("root", frFlavor1GPU), 6)
			},
		},
		{
			name: "DeleteCohort_DoesNotRepublishStaleAncestorQuotaMetrics",
			run: func(t *testing.T, cache *Cache, log logr.Logger) {
				cqd := utiltestingapi.MakeClusterQueue("cqd").
					Cohort("ch3").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default").
							Resource(corev1.ResourceCPU, "5").
							Obj(),
					).
					Obj()
				if err := cache.UpdateClusterQueue(log, cqd); err != nil {
					t.Fatalf("moving cqd to ch3: %v", err)
				}

				cqe := utiltestingapi.MakeClusterQueue("cqe").
					Cohort("ch3").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default").
							Resource(corev1.ResourceCPU, "5").
							Obj(),
					).
					Obj()
				if err := cache.UpdateClusterQueue(log, cqe); err != nil {
					t.Fatalf("moving cqe to ch3: %v", err)
				}

				cache.RecordCohortMetrics(log, "ch1")
				cache.RecordCohortMetrics(log, "ch2")
				cache.RecordCohortMetrics(log, "ch3")

				expectGaugeValue(t, kueuemetrics.CohortSubtreeQuota, cohortMetricLabels("root", frDefaultCPU), 50)
				expectGaugeValue(t, kueuemetrics.CohortSubtreeQuota, cohortMetricLabels("root", frFlavor1CPU), 25)
				expectGaugeValue(t, kueuemetrics.CohortSubtreeQuota, cohortMetricLabels("root", frFlavor1GPU), 6)

				cache.ClearCohortMetrics(log, "ch2")
				expectGaugeValue(t, kueuemetrics.CohortSubtreeQuota, cohortMetricLabels("root", frDefaultCPU), 40)

				cache.DeleteCohort("ch2")

				// Mimics the  republish (e.g. AFS driven) from another cohort update.
				// Before the fix, stale root.SubtreeQuota caused this call to write 50 again.
				cache.RecordCohortMetrics(log, "ch3")

				expectGaugeValue(t, kueuemetrics.CohortSubtreeQuota, cohortMetricLabels("ch3", frDefaultCPU), 10)
				expectGaugeValue(t, kueuemetrics.CohortSubtreeQuota, cohortMetricLabels("root", frDefaultCPU), 40)
				expectGaugeValue(t, kueuemetrics.CohortSubtreeQuota, cohortMetricLabels("root", frFlavor1CPU), 25)
				expectGaugeValue(t, kueuemetrics.CohortSubtreeQuota, cohortMetricLabels("root", frFlavor1GPU), 6)
			},
		},
		{
			name: "ClearCohortMetrics_ClearsTargetAndAncestors",
			run: func(t *testing.T, cache *Cache, log logr.Logger) {
				addTestUsage(cache, []usageChange{
					{cqName: "cqa", fr: frDefaultCPU, val: 6_000},
					{cqName: "cqd", fr: frDefaultCPU, val: 7_000},
				})

				cache.RecordCohortMetrics(log, "ch1")
				cache.RecordCohortMetrics(log, "ch2")
				expectGaugeValue(t, kueuemetrics.CohortSubtreeQuota, cohortMetricLabels("ch1", frDefaultCPU), 30)
				expectGaugeValue(t, kueuemetrics.CohortSubtreeQuota, cohortMetricLabels("ch2", frDefaultCPU), 20)
				expectGaugeValue(t, kueuemetrics.CohortSubtreeQuota, cohortMetricLabels("root", frDefaultCPU), 50)
				expectGaugeValue(t, kueuemetrics.CohortSubtreeResourceReservations, cohortMetricLabels("root", frDefaultCPU), 13)

				cache.ClearCohortMetrics(log, "ch1")
				expectGaugeCount(t, kueuemetrics.CohortSubtreeQuota, 0, cohortMetricLabels("ch1", frDefaultCPU))
				expectGaugeCount(t, kueuemetrics.CohortSubtreeResourceReservations, 0, cohortMetricLabels("ch1", frDefaultCPU))
				expectGaugeValue(t, kueuemetrics.CohortSubtreeQuota, cohortMetricLabels("ch2", frDefaultCPU), 20)
				expectGaugeValue(t, kueuemetrics.CohortSubtreeResourceReservations, cohortMetricLabels("ch2", frDefaultCPU), 7)
				expectGaugeValue(t, kueuemetrics.CohortSubtreeQuota, cohortMetricLabels("root", frDefaultCPU), 20)
				expectGaugeValue(t, kueuemetrics.CohortSubtreeResourceReservations, cohortMetricLabels("root", frDefaultCPU), 7)
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newCohortMetricsFixture(t)
			cache := fixture.cache
			ctx, log := fixture.ctx, fixture.log

			setupRecordMetricsHierarchy(
				ctx, t, log, cache,
				[]*kueue.ResourceFlavor{
					utiltestingapi.MakeResourceFlavor("default").Obj(),
					utiltestingapi.MakeResourceFlavor("flavor1").Obj(),
				},
				[]*kueue.Cohort{
					utiltestingapi.MakeCohort("root").
						ResourceGroup(
							*utiltestingapi.MakeFlavorQuotas("flavor1").
								Resource(corev1.ResourceCPU, "20").
								Resource("nvidia.com/gpu", "5").
								Obj(),
						).
						Obj(),
					utiltestingapi.MakeCohort("ch1").
						Parent("root").
						ResourceGroup(
							*utiltestingapi.MakeFlavorQuotas("default").
								Resource(corev1.ResourceCPU, "15").
								Obj(),
						).
						Obj(),
					utiltestingapi.MakeCohort("ch2").
						Parent("root").
						ResourceGroup(
							*utiltestingapi.MakeFlavorQuotas("default").
								Resource(corev1.ResourceCPU, "10").
								Obj(),
						).
						Obj(),
					utiltestingapi.MakeCohort("ch3").
						Parent("root").
						ResourceGroup(
							*utiltestingapi.MakeFlavorQuotas("flavor1").
								Resource(corev1.ResourceCPU, "5").
								Resource("nvidia.com/gpu", "1").
								Obj(),
						).
						Obj(),
				},
				[]*kueue.ClusterQueue{
					utiltestingapi.MakeClusterQueue("cqa").
						Cohort("ch1").
						ResourceGroup(
							*utiltestingapi.MakeFlavorQuotas("default").
								Resource(corev1.ResourceCPU, "10").
								Obj(),
						).
						Obj(),
					utiltestingapi.MakeClusterQueue("cqb").
						Cohort("ch1").
						ResourceGroup(
							*utiltestingapi.MakeFlavorQuotas("default").
								Resource(corev1.ResourceCPU, "5").
								Obj(),
						).
						Obj(),
					utiltestingapi.MakeClusterQueue("cqd").
						Cohort("ch2").
						ResourceGroup(
							*utiltestingapi.MakeFlavorQuotas("default").
								Resource(corev1.ResourceCPU, "5").
								Obj(),
						).
						Obj(),
					utiltestingapi.MakeClusterQueue("cqe").
						Cohort("ch2").
						ResourceGroup(
							*utiltestingapi.MakeFlavorQuotas("default").
								Resource(corev1.ResourceCPU, "5").
								Obj(),
						).
						Obj(),
				},
			)

			clearCohortMetricsForTest(t, "root", "ch1", "ch2", "ch3")

			tc.run(t, cache, log)
		})
	}
}
func TestRecordCohortMetrics_ReservationsChildParentLikeIntegration(t *testing.T) {
	fixture := newCohortMetricsFixture(t)
	cache := fixture.cache
	ctx, log := fixture.ctx, fixture.log
	frDefaultCPU := resources.FlavorResource{Flavor: "default", Resource: corev1.ResourceCPU}

	setupRecordMetricsHierarchy(
		ctx, t, log, cache,
		[]*kueue.ResourceFlavor{
			utiltestingapi.MakeResourceFlavor("default").Obj(),
		},
		[]*kueue.Cohort{
			utiltestingapi.MakeCohort("root").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "30").Obj()).
				Obj(),
			utiltestingapi.MakeCohort("ch1").
				Parent("root").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "8").Obj()).
				Obj(),
			utiltestingapi.MakeCohort("ch2").
				Parent("root").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "6").Obj()).
				Obj(),
		},
		[]*kueue.ClusterQueue{
			utiltestingapi.MakeClusterQueue("cqa").
				Cohort("ch1").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "10").Obj()).
				Obj(),
			utiltestingapi.MakeClusterQueue("cqb").
				Cohort("ch2").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "8").Obj()).
				Obj(),
		},
	)

	clearCohortMetricsForTest(t, "root", "ch1", "ch2")

	addTestUsage(cache, []usageChange{
		{cqName: "cqa", fr: frDefaultCPU, val: 6_000},
	})
	cache.RecordCohortMetrics(log, "ch1")
	expectGaugeValue(t, kueuemetrics.CohortSubtreeResourceReservations, cohortMetricLabels("ch1", frDefaultCPU), 6)
	expectGaugeValue(t, kueuemetrics.CohortSubtreeResourceReservations, cohortMetricLabels("root", frDefaultCPU), 6)

	addTestUsage(cache, []usageChange{
		{cqName: "cqa", fr: frDefaultCPU, val: 16_000},
	})
	cache.RecordCohortMetrics(log, "ch1")
	expectGaugeValue(t, kueuemetrics.CohortSubtreeResourceReservations, cohortMetricLabels("ch1", frDefaultCPU), 16)
	expectGaugeValue(t, kueuemetrics.CohortSubtreeResourceReservations, cohortMetricLabels("root", frDefaultCPU), 16)

	addTestUsage(cache, []usageChange{
		{cqName: "cqb", fr: frDefaultCPU, val: 7_000},
	})
	cache.RecordCohortMetrics(log, "ch2")
	expectGaugeValue(t, kueuemetrics.CohortSubtreeResourceReservations, cohortMetricLabels("ch2", frDefaultCPU), 7)
	expectGaugeValue(t, kueuemetrics.CohortSubtreeResourceReservations, cohortMetricLabels("root", frDefaultCPU), 23)

	addTestUsage(cache, []usageChange{
		{cqName: "cqa", fr: frDefaultCPU, val: 6_000},
	})
	cache.RecordCohortMetrics(log, "ch1")
	expectGaugeValue(t, kueuemetrics.CohortSubtreeResourceReservations, cohortMetricLabels("ch1", frDefaultCPU), 6)
	expectGaugeValue(t, kueuemetrics.CohortSubtreeResourceReservations, cohortMetricLabels("ch2", frDefaultCPU), 7)
	expectGaugeValue(t, kueuemetrics.CohortSubtreeResourceReservations, cohortMetricLabels("root", frDefaultCPU), 13)
}

type cohortMetricsFixture struct {
	ctx   context.Context
	log   logr.Logger
	cache *Cache
}

func newCohortMetricsFixture(t *testing.T) cohortMetricsFixture {
	t.Helper()
	ctx, log := utiltesting.ContextWithLog(t)
	return cohortMetricsFixture{ctx: ctx, log: log, cache: New(utiltesting.NewFakeClient())}
}

func setupRecordMetricsHierarchy(
	ctx context.Context,
	t *testing.T,
	log logr.Logger,
	cache *Cache,
	flavors []*kueue.ResourceFlavor,
	cohorts []*kueue.Cohort,
	cqs []*kueue.ClusterQueue,
) {
	t.Helper()
	for _, flavor := range flavors {
		cache.AddOrUpdateResourceFlavor(log, flavor)
	}

	for _, ch := range cohorts {
		if err := cache.AddOrUpdateCohort(ch); err != nil {
			t.Fatalf("adding cohort %q: %v", ch.Name, err)
		}
	}

	for _, cq := range cqs {
		if err := cache.AddClusterQueue(ctx, cq); err != nil {
			t.Fatalf("adding clusterQueue %q: %v", cq.Name, err)
		}
	}
}

type usageChange struct {
	cqName kueue.ClusterQueueReference
	fr     resources.FlavorResource
	val    int64
}

// addUsage adds usage to the current node, and adds usage past localQuota
// to its Cohort.
func addTestUsage(cache *Cache, fr []usageChange) {
	for _, val := range fr {
		cache.hm.ClusterQueue(val.cqName).resourceNode.Usage[val.fr] = val.val
	}
}

func clearCohortMetricsForTest(t *testing.T, cohorts ...kueue.CohortReference) {
	t.Helper()
	for _, cohort := range cohorts {
		if cohort != "" {
			kueuemetrics.ClearCohortMetrics(cohort)
		}
	}
	t.Cleanup(func() {
		for _, cohort := range cohorts {
			if cohort != "" {
				kueuemetrics.ClearCohortMetrics(cohort)
			}
		}
	})
}

func cohortMetricLabels(cohortName kueue.CohortReference, fr resources.FlavorResource) map[string]string {
	return map[string]string{
		"cohort":       string(cohortName),
		"flavor":       string(fr.Flavor),
		"resource":     string(fr.Resource),
		"replica_role": "standalone",
	}
}

func expectGaugeCount(t *testing.T, collector prometheus.Collector, want int, labels map[string]string) {
	t.Helper()
	got := len(utiltestingmetrics.CollectFilteredGaugeVec(collector, labels))
	if got != want {
		t.Fatalf("unexpected metric count for labels %v: got=%d want=%d", labels, got, want)
	}
}

func expectGaugeValue(t *testing.T, collector prometheus.Collector, labels map[string]string, want float64) {
	t.Helper()
	dps := utiltestingmetrics.CollectFilteredGaugeVec(collector, labels)
	if len(dps) != 1 {
		t.Fatalf("expected exactly one metric for labels %v, got=%d", labels, len(dps))
	}
	if dps[0].Value != want {
		t.Fatalf("unexpected metric value for labels %v: got=%v want=%v", labels, dps[0].Value, want)
	}
}
