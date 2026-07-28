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
						quota:  resourceFloat(cache.resourceFormatter, fr.Resource, cache.hm.Cohort(cohortLeft).resourceNode.SubtreeQuota[fr].Int64()),
					},
					{
						cohort: cohortRoot,
						fr:     fr,
						quota:  resourceFloat(cache.resourceFormatter, fr.Resource, cache.hm.Cohort(cohortRoot).resourceNode.SubtreeQuota[fr].Int64()),
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
				labels := cohortQuotaMetricLabels(want.cohort, want.fr)
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

func TestRecordCohortMetrics_QuotaHierarchyLikeIntegration(t *testing.T) {
	fixture := newCohortMetricsFixture(t)
	cache := fixture.cache
	ctx, log := fixture.ctx, fixture.log

	frDefaultCPU := resources.FlavorResource{Flavor: "default", Resource: corev1.ResourceCPU}
	frFlavor1CPU := resources.FlavorResource{Flavor: "flavor1", Resource: corev1.ResourceCPU}
	frFlavor1GPU := resources.FlavorResource{Flavor: "flavor1", Resource: corev1.ResourceName("nvidia.com/gpu")}

	setupRecordMetricsHierarchy(ctx, t, log, cache,
		[]*kueue.ResourceFlavor{
			utiltestingapi.MakeResourceFlavor("default").Obj(),
			utiltestingapi.MakeResourceFlavor("flavor1").Obj(),
		},
		[]*kueue.Cohort{
			utiltestingapi.MakeCohort("root").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("flavor1").
					Resource(corev1.ResourceCPU, "20").
					Resource("nvidia.com/gpu", "5").
					Obj()).
				Obj(),
			utiltestingapi.MakeCohort("ch1").
				Parent("root").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
					Resource(corev1.ResourceCPU, "15").
					Obj()).
				Obj(),
			utiltestingapi.MakeCohort("ch2").
				Parent("root").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
					Resource(corev1.ResourceCPU, "10").
					Obj()).
				Obj(),
			utiltestingapi.MakeCohort("ch3").
				Parent("root").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("flavor1").
					Resource(corev1.ResourceCPU, "5").
					Resource("nvidia.com/gpu", "1").
					Obj()).
				Obj(),
		},
		[]*kueue.ClusterQueue{
			utiltestingapi.MakeClusterQueue("cqa").
				Cohort("ch1").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "10").Obj()).
				Obj(),
			utiltestingapi.MakeClusterQueue("cqb").
				Cohort("ch1").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "5").Obj()).
				Obj(),
			utiltestingapi.MakeClusterQueue("cqd").
				Cohort("ch2").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "5").Obj()).
				Obj(),
			utiltestingapi.MakeClusterQueue("cqe").
				Cohort("ch2").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "5").Obj()).
				Obj(),
		},
	)
	t.Run("QuotaHierarchyLikeIntegration", func(t *testing.T) {
		clearCohortMetricsForTest(t, "root", "ch1", "ch2", "ch3")

		cache.RecordCohortMetrics(log, "ch1")
		cache.RecordCohortMetrics(log, "ch2")
		cache.RecordCohortMetrics(log, "ch3")

		expectGaugeValue(t, kueuemetrics.CohortSubtreeQuota, cohortQuotaMetricLabels("ch1", frDefaultCPU), 30)
		expectGaugeValue(t, kueuemetrics.CohortSubtreeQuota, cohortQuotaMetricLabels("ch2", frDefaultCPU), 20)
		expectGaugeValue(t, kueuemetrics.CohortSubtreeQuota, cohortQuotaMetricLabels("ch3", frFlavor1CPU), 5)
		expectGaugeValue(t, kueuemetrics.CohortSubtreeQuota, cohortQuotaMetricLabels("ch3", frFlavor1GPU), 1)

		expectGaugeValue(t, kueuemetrics.CohortSubtreeQuota, cohortQuotaMetricLabels("root", frDefaultCPU), 50)
		expectGaugeValue(t, kueuemetrics.CohortSubtreeQuota, cohortQuotaMetricLabels("root", frFlavor1CPU), 25)
		expectGaugeValue(t, kueuemetrics.CohortSubtreeQuota, cohortQuotaMetricLabels("root", frFlavor1GPU), 6)
	})
}

func TestRecordCohortMetrics_ReservationsChildParentLikeIntegration(t *testing.T) {
	fixture := newCohortMetricsFixture(t)
	cache := fixture.cache
	ctx, log := fixture.ctx, fixture.log
	frDefaultCPU := resources.FlavorResource{Flavor: "default", Resource: corev1.ResourceCPU}

	setupDefaultRootCh1Ch2Hierarchy(ctx, t, log, cache)

	clearCohortMetricsForTest(t, "root", "ch1", "ch2")

	addTestUsage(cache, []usageChange{
		{cqName: "cqa", fr: frDefaultCPU, val: 6_000},
	})
	cache.RecordCohortMetrics(log, "ch1")
	expectGaugeValue(t, kueuemetrics.CohortSubtreeResourceReservations, cohortQuotaMetricLabels("ch1", frDefaultCPU), 6)
	expectGaugeValue(t, kueuemetrics.CohortSubtreeResourceReservations, cohortQuotaMetricLabels("root", frDefaultCPU), 6)

	addTestUsage(cache, []usageChange{
		{cqName: "cqa", fr: frDefaultCPU, val: 16_000},
	})
	cache.RecordCohortMetrics(log, "ch1")
	expectGaugeValue(t, kueuemetrics.CohortSubtreeResourceReservations, cohortQuotaMetricLabels("ch1", frDefaultCPU), 16)
	expectGaugeValue(t, kueuemetrics.CohortSubtreeResourceReservations, cohortQuotaMetricLabels("root", frDefaultCPU), 16)

	addTestUsage(cache, []usageChange{
		{cqName: "cqb", fr: frDefaultCPU, val: 7_000},
	})
	cache.RecordCohortMetrics(log, "ch2")
	expectGaugeValue(t, kueuemetrics.CohortSubtreeResourceReservations, cohortQuotaMetricLabels("ch2", frDefaultCPU), 7)
	expectGaugeValue(t, kueuemetrics.CohortSubtreeResourceReservations, cohortQuotaMetricLabels("root", frDefaultCPU), 23)

	addTestUsage(cache, []usageChange{
		{cqName: "cqa", fr: frDefaultCPU, val: 6_000},
	})
	cache.RecordCohortMetrics(log, "ch1")
	expectGaugeValue(t, kueuemetrics.CohortSubtreeResourceReservations, cohortQuotaMetricLabels("ch1", frDefaultCPU), 6)
	expectGaugeValue(t, kueuemetrics.CohortSubtreeResourceReservations, cohortQuotaMetricLabels("ch2", frDefaultCPU), 7)
	expectGaugeValue(t, kueuemetrics.CohortSubtreeResourceReservations, cohortQuotaMetricLabels("root", frDefaultCPU), 13)
}

func TestClearCohortMetrics_ClearsTargetAndAncestors(t *testing.T) {
	fixture := newCohortMetricsFixture(t)
	cache := fixture.cache
	ctx, log := fixture.ctx, fixture.log
	frDefaultCPU := resources.FlavorResource{Flavor: "default", Resource: corev1.ResourceCPU}

	setupDefaultRootCh1Ch2Hierarchy(ctx, t, log, cache)

	clearCohortMetricsForTest(t, "root", "ch1", "ch2")
	addTestUsage(cache, []usageChange{
		{cqName: "cqa", fr: frDefaultCPU, val: 6_000},
		{cqName: "cqb", fr: frDefaultCPU, val: 7_000},
	})

	cache.RecordCohortMetrics(log, "ch1")
	cache.RecordCohortMetrics(log, "ch2")
	expectGaugeValue(t, kueuemetrics.CohortSubtreeQuota, cohortQuotaMetricLabels("ch1", frDefaultCPU), 18)
	expectGaugeValue(t, kueuemetrics.CohortSubtreeQuota, cohortQuotaMetricLabels("ch2", frDefaultCPU), 14)
	expectGaugeValue(t, kueuemetrics.CohortSubtreeQuota, cohortQuotaMetricLabels("root", frDefaultCPU), 62)
	expectGaugeValue(t, kueuemetrics.CohortSubtreeResourceReservations, cohortQuotaMetricLabels("root", frDefaultCPU), 13)

	cache.ClearCohortMetrics(log, "ch1")
	expectGaugeCount(t, kueuemetrics.CohortSubtreeQuota, 0, cohortQuotaMetricLabels("ch1", frDefaultCPU))
	expectGaugeCount(t, kueuemetrics.CohortSubtreeResourceReservations, 0, cohortQuotaMetricLabels("ch1", frDefaultCPU))
	expectGaugeValue(t, kueuemetrics.CohortSubtreeQuota, cohortQuotaMetricLabels("ch2", frDefaultCPU), 14)
	expectGaugeValue(t, kueuemetrics.CohortSubtreeResourceReservations, cohortQuotaMetricLabels("ch2", frDefaultCPU), 7)
	expectGaugeValue(t, kueuemetrics.CohortSubtreeQuota, cohortQuotaMetricLabels("root", frDefaultCPU), 44)
	expectGaugeValue(t, kueuemetrics.CohortSubtreeResourceReservations, cohortQuotaMetricLabels("root", frDefaultCPU), 7)
}

func TestAddOrUpdateCohort_Table(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T, cache *Cache)
	}{
		{
			name: "IntegratedInfoMetricsOnReparent",
			run: func(t *testing.T, cache *Cache) {
				clearCohortMetricsForTest(t, "child", "new-root")
				clearClusterQueueInfoMetricsForTest(t, "cq1")

				newRoot := utiltestingapi.MakeCohort("new-root").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "20").Obj()).
					Obj()
				if err := cache.AddOrUpdateCohort(newRoot); err != nil {
					t.Fatalf("adding new-root: %v", err)
				}

				child := utiltestingapi.MakeCohort("child").Parent("new-root").Obj()
				if err := cache.AddOrUpdateCohort(child); err != nil {
					t.Fatalf("reparenting child: %v", err)
				}

				expectGaugeCount(t, kueuemetrics.CohortInfo, 0, cohortMetricInfoLabels("child", "root", "root"))
				expectGaugeValue(t, kueuemetrics.CohortInfo, cohortMetricInfoLabels("new-root", "", "new-root"), 1)
				expectGaugeValue(t, kueuemetrics.CohortInfo, cohortMetricInfoLabels("child", "new-root", "new-root"), 1)
				expectGaugeValue(t, kueuemetrics.ClusterQueueInfo, cqMetricInfoLabels("cq1", "child", "new-root"), 1)
				// root is explicit: handleParentUpdate must not clear its CohortInfo even though child moved away.
				expectGaugeValue(t, kueuemetrics.CohortInfo, cohortMetricInfoLabels("root", "", "root"), 1)
			},
		},
		{
			name: "DeleteAndRecreateWithDifferentHierarchy",
			run: func(t *testing.T, cache *Cache) {
				clearCohortMetricsForTest(t, "child", "root2")
				clearClusterQueueInfoMetricsForTest(t, "cq1")

				cache.DeleteCohort(kueue.CohortReference("child"))

				if err := cache.AddOrUpdateCohort(utiltestingapi.MakeCohort("child").Parent("root2").Obj()); err != nil {
					t.Fatalf("recreating child under root2: %v", err)
				}

				childImpl := cache.hm.Cohort("child")
				if childImpl == nil {
					t.Fatal("child cohort not found after recreation")
				}

				expectGaugeValue(t, kueuemetrics.CohortInfo, cohortMetricInfoLabels("child", "root2", "root2"), 1)
				expectGaugeValue(t, kueuemetrics.CohortInfo, cohortMetricInfoLabels("root2", "", "root2"), 1)
				expectGaugeValue(t, kueuemetrics.ClusterQueueInfo, cqMetricInfoLabels("cq1", "child", "root2"), 1)
				expectGaugeCount(t, kueuemetrics.CohortInfo, 0, cohortMetricInfoLabels("child", "root", "root"))
			},
		},
		{
			name: "ClearsInfoForRemovedImplicitOldParent",
			run: func(t *testing.T, cache *Cache) {
				clearCohortMetricsForTest(t, "implicit-old", "child", "root")

				childWithImplicitParent := utiltestingapi.MakeCohort("child").Parent("implicit-old").Obj()
				if err := cache.AddOrUpdateCohort(childWithImplicitParent); err != nil {
					t.Fatalf("setting child parent to implicit-old: %v", err)
				}

				expectGaugeValue(t, kueuemetrics.CohortInfo, cohortMetricInfoLabels("child", "implicit-old", "implicit-old"), 1)
				expectGaugeValue(t, kueuemetrics.CohortInfo, cohortMetricInfoLabels("implicit-old", "", "implicit-old"), 1)

				if err := cache.AddOrUpdateCohort(utiltestingapi.MakeCohort("child").Parent("root").Obj()); err != nil {
					t.Fatalf("moving child to root: %v", err)
				}

				if cache.hm.Cohort("implicit-old") != nil {
					t.Fatal("implicit-old should be removed from hierarchy manager")
				}
				expectGaugeCount(t, kueuemetrics.CohortInfo, 0, cohortMetricInfoLabels("implicit-old", "", "implicit-old"))
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newCohortMetricsFixture(t)
			setupDefaultRootChildCQHierarchy(fixture.ctx, t, fixture.log, fixture.cache)
			tc.run(t, fixture.cache)
		})
	}
}

func TestUpdateClusterQueue_Table(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T, cache *Cache, ctx context.Context, log logr.Logger)
	}{
		{
			name: "UpdatesInfoMetricsOnHierarchyChange",
			run: func(t *testing.T, cache *Cache, ctx context.Context, log logr.Logger) {
				clearCohortMetricsForTest(t, "old-root", "old-child", "new-root", "new-child")
				clearClusterQueueInfoMetricsForTest(t, "cq1")

				setupRecordMetricsHierarchy(ctx, t, log, cache,
					[]*kueue.ResourceFlavor{
						utiltestingapi.MakeResourceFlavor("default").Obj(),
					},
					[]*kueue.Cohort{
						utiltestingapi.MakeCohort("old-root").
							ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
								Resource(corev1.ResourceCPU, "10").Obj()).
							Obj(),
						utiltestingapi.MakeCohort("old-child").Parent("old-root").Obj(),
						utiltestingapi.MakeCohort("new-root").
							ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
								Resource(corev1.ResourceCPU, "20").Obj()).
							Obj(),
						utiltestingapi.MakeCohort("new-child").Parent("new-root").Obj(),
					},
					[]*kueue.ClusterQueue{
						utiltestingapi.MakeClusterQueue("cq1").
							Cohort("old-child").
							ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
								Resource(corev1.ResourceCPU, "5").Obj()).
							Obj(),
					},
				)

				updatedCQ := utiltestingapi.MakeClusterQueue("cq1").
					Cohort("new-child").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "5").Obj()).
					Obj()
				if err := cache.UpdateClusterQueue(log, updatedCQ); err != nil {
					t.Fatalf("moving cq1 to new-child: %v", err)
				}

				expectGaugeCount(t, kueuemetrics.ClusterQueueInfo, 0, cqMetricInfoLabels("cq1", "old-child", "old-root"))
				expectGaugeValue(t, kueuemetrics.ClusterQueueInfo, cqMetricInfoLabels("cq1", "new-child", "new-root"), 1)
			},
		},
		{
			name: "ExplicitParentRetainsCohortInfoAfterCQMoves",
			run: func(t *testing.T, cache *Cache, ctx context.Context, log logr.Logger) {
				clearCohortMetricsForTest(t, "source", "dest")
				clearClusterQueueInfoMetricsForTest(t, "cq1")

				setupRecordMetricsHierarchy(ctx, t, log, cache,
					[]*kueue.ResourceFlavor{
						utiltestingapi.MakeResourceFlavor("default").Obj(),
					},
					[]*kueue.Cohort{
						utiltestingapi.MakeCohort("source").
							ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
								Resource(corev1.ResourceCPU, "10").Obj()).
							Obj(),
						utiltestingapi.MakeCohort("dest").
							ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
								Resource(corev1.ResourceCPU, "20").Obj()).
							Obj(),
					},
					[]*kueue.ClusterQueue{
						utiltestingapi.MakeClusterQueue("cq1").
							Cohort("source").
							ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
								Resource(corev1.ResourceCPU, "5").Obj()).
							Obj(),
					},
				)

				expectGaugeValue(t, kueuemetrics.CohortInfo, cohortMetricInfoLabels("source", "", "source"), 1)

				updatedCQ := utiltestingapi.MakeClusterQueue("cq1").
					Cohort("dest").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "5").Obj()).
					Obj()
				if err := cache.UpdateClusterQueue(log, updatedCQ); err != nil {
					t.Fatalf("moving cq1 to dest: %v", err)
				}

				// source is explicit: its CohortInfo must be retained even though cq1 left.
				expectGaugeValue(t, kueuemetrics.CohortInfo, cohortMetricInfoLabels("source", "", "source"), 1)
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newCohortMetricsFixture(t)
			tc.run(t, fixture.cache, fixture.ctx, fixture.log)
		})
	}
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

func setupDefaultRootChildCQHierarchy(
	ctx context.Context,
	t *testing.T,
	log logr.Logger,
	cache *Cache,
) {
	t.Helper()
	setupRecordMetricsHierarchy(ctx, t, log, cache,
		[]*kueue.ResourceFlavor{
			utiltestingapi.MakeResourceFlavor("default").Obj(),
		},
		[]*kueue.Cohort{
			utiltestingapi.MakeCohort("root").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
					Resource(corev1.ResourceCPU, "10").Obj()).
				Obj(),
			utiltestingapi.MakeCohort("child").Parent("root").Obj(),
		},
		[]*kueue.ClusterQueue{
			utiltestingapi.MakeClusterQueue("cq1").
				Cohort("child").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
					Resource(corev1.ResourceCPU, "5").Obj()).
				Obj(),
		},
	)
}

func setupDefaultRootCh1Ch2Hierarchy(
	ctx context.Context,
	t *testing.T,
	log logr.Logger,
	cache *Cache,
) {
	t.Helper()
	setupRecordMetricsHierarchy(ctx, t, log, cache,
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
		cache.hm.ClusterQueue(val.cqName).resourceNode.Usage[val.fr] = resources.NewAmount(val.val)
	}
}

func clearCohortMetricsForTest(t *testing.T, cohorts ...kueue.CohortReference) {
	t.Helper()
	for _, cohort := range cohorts {
		if cohort != "" {
			kueuemetrics.ClearCohortMetrics(cohort)
			kueuemetrics.ClearCohortInfo(cohort)
		}
	}
	t.Cleanup(func() {
		for _, cohort := range cohorts {
			if cohort != "" {
				kueuemetrics.ClearCohortMetrics(cohort)
				kueuemetrics.ClearCohortInfo(cohort)
			}
		}
	})
}
func clearClusterQueueInfoMetricsForTest(t *testing.T, cqs ...kueue.ClusterQueueReference) {
	t.Helper()
	for _, cq := range cqs {
		if cq != "" {
			kueuemetrics.ClearClusterQueueInfo(cq)
		}
	}
	t.Cleanup(func() {
		for _, cq := range cqs {
			if cq != "" {
				kueuemetrics.ClearClusterQueueInfo(cq)
			}
		}
	})
}

func cohortQuotaMetricLabels(cohortName kueue.CohortReference, fr resources.FlavorResource) map[string]string {
	return map[string]string{
		"cohort":       string(cohortName),
		"flavor":       string(fr.Flavor),
		"resource":     string(fr.Resource),
		"replica_role": "standalone",
	}
}

func cohortMetricInfoLabels(cohortName kueue.CohortReference, parentCohort, rootCohort string) map[string]string {
	return map[string]string{
		"cohort":        string(cohortName),
		"parent_cohort": parentCohort,
		"root_cohort":   rootCohort,
		"replica_role":  "standalone",
	}
}

func cqMetricInfoLabels(cqName kueue.ClusterQueueReference, parentCohort, rootCohort string) map[string]string {
	return map[string]string{
		"cluster_queue": string(cqName),
		"parent_cohort": parentCohort,
		"root_cohort":   rootCohort,
		"replica_role":  "standalone",
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

func TestDeleteClusterQueue_Table(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T, cache *Cache, ctx context.Context, log logr.Logger)
	}{
		{
			name: "ClearsInfoForImplicitParentAfterCQDeleted",
			run: func(t *testing.T, cache *Cache, ctx context.Context, log logr.Logger) {
				clearCohortMetricsForTest(t, "implicit")
				clearClusterQueueInfoMetricsForTest(t, "cq1")

				setupRecordMetricsHierarchy(ctx, t, log, cache,
					[]*kueue.ResourceFlavor{
						utiltestingapi.MakeResourceFlavor("default").Obj(),
					},
					nil,
					[]*kueue.ClusterQueue{
						utiltestingapi.MakeClusterQueue("cq1").
							Cohort("implicit").
							ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
								Resource(corev1.ResourceCPU, "5").Obj()).
							Obj(),
					},
				)

				expectGaugeValue(t, kueuemetrics.ClusterQueueInfo, cqMetricInfoLabels("cq1", "implicit", "implicit"), 1)
				expectGaugeCount(t, kueuemetrics.CohortInfo, 0, cohortMetricInfoLabels("implicit", "", "implicit"))

				cache.DeleteClusterQueue(utiltestingapi.MakeClusterQueue("cq1").Cohort("implicit").Obj())

				if cache.hm.ClusterQueue("cq1") != nil {
					t.Fatal("cq1 should be deleted from hierarchy manager")
				}
				if cache.hm.Cohort("implicit") != nil {
					t.Fatal("implicit cohort should be removed from hierarchy manager")
				}

				expectGaugeCount(t, kueuemetrics.ClusterQueueInfo, 0, cqMetricInfoLabels("cq1", "implicit", "implicit"))
				expectGaugeCount(t, kueuemetrics.CohortInfo, 0, cohortMetricInfoLabels("implicit", "", "implicit"))
			},
		},
		{
			name: "RetainsCohortInfoForExplicitParentAfterLastCQDeleted",
			run: func(t *testing.T, cache *Cache, ctx context.Context, log logr.Logger) {
				clearCohortMetricsForTest(t, "explicit-root")
				clearClusterQueueInfoMetricsForTest(t, "sole-cq")

				setupRecordMetricsHierarchy(ctx, t, log, cache,
					[]*kueue.ResourceFlavor{
						utiltestingapi.MakeResourceFlavor("default").Obj(),
					},
					[]*kueue.Cohort{
						utiltestingapi.MakeCohort("explicit-root").
							ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
								Resource(corev1.ResourceCPU, "10").Obj()).
							Obj(),
					},
					[]*kueue.ClusterQueue{
						utiltestingapi.MakeClusterQueue("sole-cq").
							Cohort("explicit-root").
							ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
								Resource(corev1.ResourceCPU, "5").Obj()).
							Obj(),
					},
				)

				expectGaugeValue(t, kueuemetrics.CohortInfo, cohortMetricInfoLabels("explicit-root", "", "explicit-root"), 1)

				cache.DeleteClusterQueue(utiltestingapi.MakeClusterQueue("sole-cq").Cohort("explicit-root").Obj())

				// CohortInfo must be retained even though it now has no CQs.
				expectGaugeValue(t, kueuemetrics.CohortInfo, cohortMetricInfoLabels("explicit-root", "", "explicit-root"), 1)
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newCohortMetricsFixture(t)
			tc.run(t, fixture.cache, fixture.ctx, fixture.log)
		})
	}
}

func TestAddClusterQueue_RecordsInfoOnCreateWithoutCohort(t *testing.T) {
	fixture := newCohortMetricsFixture(t)
	cache := fixture.cache
	ctx, log := fixture.ctx, fixture.log

	cache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("default").Obj())

	clearClusterQueueInfoMetricsForTest(t, "cqa")
	if err := cache.AddClusterQueue(ctx, utiltestingapi.MakeClusterQueue("cqa").
		ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
			Resource(corev1.ResourceCPU, "10").
			Obj()).
		Obj()); err != nil {
		t.Fatalf("adding clusterQueue cqa: %v", err)
	}

	expectGaugeValue(t, kueuemetrics.ClusterQueueInfo, cqMetricInfoLabels("cqa", "", ""), 1)
}

func TestResyncGaugeMetrics_ReportsHierarchyInfoWithFairSharing(t *testing.T) {
	ctx, log := utiltesting.ContextWithLog(t)
	cache := New(utiltesting.NewFakeClient(), WithFairSharing(true))

	setupDefaultRootChildCQHierarchy(ctx, t, log, cache)

	clearCohortMetricsForTest(t, "root", "child")
	clearClusterQueueInfoMetricsForTest(t, "cq1")

	cache.ResyncGaugeMetrics(log)

	expectGaugeValue(t, kueuemetrics.ClusterQueueInfo, cqMetricInfoLabels("cq1", "child", "root"), 1)
	expectGaugeValue(t, kueuemetrics.CohortInfo, cohortMetricInfoLabels("root", "", "root"), 1)
	expectGaugeValue(t, kueuemetrics.CohortInfo, cohortMetricInfoLabels("child", "root", "root"), 1)

	weightedShareLabels := map[string]string{
		"cohort":       "child",
		"replica_role": "standalone",
	}
	if got := len(utiltestingmetrics.CollectFilteredGaugeVec(kueuemetrics.CohortWeightedShare, weightedShareLabels)); got != 1 {
		t.Fatalf("expected one weighted-share metric for child cohort, got=%d", got)
	}
}

func TestResyncGaugeMetrics_SkipsCohortInfoForCycle(t *testing.T) {
	_, log := utiltesting.ContextWithLog(t)
	cache := New(utiltesting.NewFakeClient())
	clearCohortMetricsForTest(t, "cohort-a", "cohort-b")

	if err := cache.AddOrUpdateCohort(utiltestingapi.MakeCohort("cohort-a").Parent("cohort-b").Obj()); err != nil {
		t.Fatal("Expected success: no cycle yet")
	}
	// cohort-b -> cohort-a closes the cycle; error is expected.
	_ = cache.AddOrUpdateCohort(utiltestingapi.MakeCohort("cohort-b").Parent("cohort-a").Obj())

	// ResyncGaugeMetrics bulk-resets info metrics then selectively re-emits.
	// Cycle cohorts are skipped (HasCycle=true), so they end up with no CohortInfo.
	cache.ResyncGaugeMetrics(log)

	expectGaugeCount(t, kueuemetrics.CohortInfo, 0, map[string]string{"cohort": "cohort-a"})
	expectGaugeCount(t, kueuemetrics.CohortInfo, 0, map[string]string{"cohort": "cohort-b"})
}
