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

package queue

import (
	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/cache/hierarchy"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/util/queue"
	utilresource "sigs.k8s.io/kueue/pkg/util/resource"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
)

// reportPendingWorkloads reports metrics for both ClusterQueue,
// and all of its matching LocalQueues.
func reportPendingWorkloads(m *Manager, cqRef kueue.ClusterQueueReference) {
	cq := m.hm.ClusterQueue(cqRef)
	if cq == nil {
		return
	}
	reportCQPendingWorkloads(m, cq)

	if !m.lqMetrics.IsEnabled() {
		return
	}
	for _, lq := range m.localQueues {
		if lq.ClusterQueue == cqRef {
			reportLQPendingWorkloads(m, lq)
		}
	}
}

func reportCQPendingWorkloads(m *Manager, cq *ClusterQueue) {
	active, inadmissible := cq.PendingBreakdown()
	cqActive := m.statusChecker == nil || m.statusChecker.ClusterQueueActive(cq.name)
	if !cqActive {
		inadmissible = metrics.MergedTracker(inadmissible, active)
		active = metrics.NewLabelValsTracker()
	}
	cqCustomLabels := m.customLabels.CQGet(cq.name)

	if features.Enabled(features.CustomMetricLabels) && m.customLabels.KindConfigured(config.SourceKindWorkload) {
		// Clear zero count label sets.
		clearZeroWorkloadCounts(m, cq.name, active, metrics.PendingStatusActive)
		clearZeroWorkloadCounts(m, cq.name, inadmissible, metrics.PendingStatusInadmissible)
		// Populate metrics for non-zero counts.
		reportPendingWorkloadCounts(m, cq.name, active, metrics.PendingStatusActive)
		reportPendingWorkloadCounts(m, cq.name, inadmissible, metrics.PendingStatusInadmissible)
	} else {
		metrics.ReportPendingWorkloads(cq.name, metrics.PendingStatusActive, active.Total(), cqCustomLabels, m.roleTracker)
		metrics.ReportPendingWorkloads(cq.name, metrics.PendingStatusInadmissible, inadmissible.Total(), cqCustomLabels, m.roleTracker)
	}

	if features.Enabled(features.SchedulingEquivalenceHashing) {
		var activeHashes, inadmissibleHashes int
		if !cqActive {
			activeHashes, inadmissibleHashes = cq.pendingSchedulingHashesForInactiveClusterQueue()
		} else {
			activeHashes, inadmissibleHashes = cq.PendingSchedulingHashes()
		}
		metrics.ReportPendingSchedulingHashes(cq.name, activeHashes, inadmissibleHashes, cqCustomLabels, m.roleTracker)
	}

	if m.resourceMetricsEnabled {
		// pendingResourcesTotal carries 0 entries for configured resources (seeded by
		// Update), so iterating it once covers both the zero-series and actual pending.
		pendingResources := cq.pendingResources()
		for resourceName, v := range pendingResources {
			q := m.resourceFormatter.ResourceQuantity(resourceName, v)
			metrics.ReportClusterQueueResourcePending(string(cq.name), string(resourceName), utilresource.QuantityToFloat(&q), cqCustomLabels, m.roleTracker)
		}
	}

	reportCohortSubtreePendingWorkloads(m, cq, active.Total(), inadmissible.Total())
}

// reportCohortSubtreePendingWorkloads applies the delta between cq's previous
// and current pending counts to every ancestor cohort, then re-emits the
// kueue_cohort_subtree_pending_workloads gauge for each. newActive and
// newInadmissible reflect any active/inadmissible swap already applied for
// an inactive CQ.
func reportCohortSubtreePendingWorkloads(m *Manager, cq *ClusterQueue, newActive, newInadmissible int) {
	cohort := cq.Parent()
	if cohort == nil || hierarchy.HasCycle(cohort) {
		return
	}

	activeDelta := newActive - cq.lastReportedCohort.active
	inadmissibleDelta := newInadmissible - cq.lastReportedCohort.inadmissible
	cq.lastReportedCohort.active = newActive
	cq.lastReportedCohort.inadmissible = newInadmissible

	cohort.updatePendingWorkloadsCount(activeDelta, inadmissibleDelta)

	for ancestor := range cohort.PathSelfToRoot() {
		metrics.ReportCohortSubtreePendingWorkloads(ancestor.Name, metrics.PendingStatusActive, ancestor.pendingActiveCount, m.customLabels.CohortGet(ancestor.Name), m.roleTracker)
		metrics.ReportCohortSubtreePendingWorkloads(ancestor.Name, metrics.PendingStatusInadmissible, ancestor.pendingInadmissibleCount, m.customLabels.CohortGet(ancestor.Name), m.roleTracker)
	}
}

func clearZeroWorkloadCounts(m *Manager, cq kueue.ClusterQueueReference, tracker *metrics.LabelValsTracker, pendingStatus string) {
	cqCustomLabels := m.customLabels.CQGet(cq)
	for wlLabelVals := range tracker.PopZeroCounts() {
		customLabels := m.customLabels.CombineLabelValues(map[config.SourceKind][]string{
			config.SourceKindClusterQueue: cqCustomLabels,
			config.SourceKindWorkload:     wlLabelVals.OrderedList(),
		})
		metrics.ClearPendingWorkloads(cq, pendingStatus, customLabels, m.roleTracker)
	}
}

func reportPendingWorkloadCounts(m *Manager, cq kueue.ClusterQueueReference, tracker *metrics.LabelValsTracker, pendingStatus string) {
	cqCustomLabels := m.customLabels.CQGet(cq)
	for wlLabelVals, count := range tracker.Iter() {
		customLabels := m.customLabels.CombineLabelValues(map[config.SourceKind][]string{
			config.SourceKindClusterQueue: cqCustomLabels,
			config.SourceKindWorkload:     wlLabelVals.OrderedList(),
		})
		metrics.ReportPendingWorkloads(cq, pendingStatus, count, customLabels, m.roleTracker)
	}
}

func reportLQPendingWorkloads(m *Manager, lq *LocalQueue) {
	if !m.lqMetrics.ShouldExposeLocalQueueMetrics(lq.labels) {
		return
	}
	namespace, lqName := queue.MustParseLocalQueueReference(lq.Key)
	lqRef := metrics.LocalQueueReference{Name: lqName, Namespace: namespace}
	cqActive := m.statusChecker == nil || m.statusChecker.ClusterQueueActive(lq.ClusterQueue)

	if features.Enabled(features.CustomMetricLabels) && m.customLabels.KindConfigured(config.SourceKindWorkload) {
		var active, inadmissible *metrics.LabelValsTracker
		if cq := m.getClusterQueueLockless(lq.ClusterQueue); cq != nil {
			active, inadmissible = cq.PendingBreakdownInLocalQueue(lq.Key)
		} else {
			active, inadmissible = metrics.NewLabelValsTracker(), metrics.NewLabelValsTracker()
		}
		if !cqActive {
			inadmissible = metrics.MergedTracker(inadmissible, active)
			active = metrics.NewLabelValsTracker()
		}
		// Clear all existing series before re-reporting to remove stale label combinations.
		metrics.ClearLocalQueuePendingWorkloadsSeries(lqRef)
		lqCustomLabels := m.customLabels.LQGet(lq.Key)
		reportLQPendingWorkloadCounts(m, lqRef, lqCustomLabels, active, metrics.PendingStatusActive)
		reportLQPendingWorkloadCounts(m, lqRef, lqCustomLabels, inadmissible, metrics.PendingStatusInadmissible)
	} else {
		var active, inadmissible int
		if cq := m.getClusterQueueLockless(lq.ClusterQueue); cq != nil {
			active, inadmissible = cq.PendingInLocalQueue(lq.Key)
		}
		if !cqActive {
			inadmissible += active
			active = 0
		}
		metrics.ReportLocalQueuePendingWorkloads(lqRef, active, inadmissible, m.customLabels.LQGet(lq.Key), m.roleTracker)
	}
}

func reportLQPendingWorkloadCounts(m *Manager, lqRef metrics.LocalQueueReference, lqCustomLabels []string, tracker *metrics.LabelValsTracker, status string) {
	for wlLabelVals, count := range tracker.Iter() {
		customLabels := m.customLabels.CombineLabelValues(map[config.SourceKind][]string{
			config.SourceKindLocalQueue: lqCustomLabels,
			config.SourceKindWorkload:   wlLabelVals.OrderedList(),
		})
		metrics.ReportLocalQueuePendingWorkloadsByWorkload(lqRef, status, count, customLabels, m.roleTracker)
	}
}

func reportLQFinishedWorkloads(m *Manager, lq *LocalQueue) {
	if !m.lqMetrics.ShouldExposeLocalQueueMetrics(lq.labels) {
		return
	}
	namespace, lqName := queue.MustParseLocalQueueReference(lq.Key)
	metrics.ReportLocalQueueFinishedWorkloads(metrics.LocalQueueReference{
		Name:      lqName,
		Namespace: namespace,
	}, lq.finishedWorkloads.Len(), m.customLabels.LQGet(lq.Key), m.roleTracker)
}

func reportCQFinishedWorkloads(cq *ClusterQueue, roleTracker *roletracker.RoleTracker, cl *metrics.CustomLabels) {
	metrics.ReportFinishedWorkloads(cq.name, cq.finishedWorkloads.Len(), cl.CQGet(cq.name), roleTracker)
}

func clearCQMetrics(cqRef kueue.ClusterQueueReference) {
	metrics.ClearClusterQueueMetrics(cqRef)
}

func clearLQMetrics(lqRef queue.LocalQueueReference) {
	namespace, lqName := queue.MustParseLocalQueueReference(lqRef)
	metrics.ClearLocalQueueMetrics(metrics.LocalQueueReference{
		Name:      lqName,
		Namespace: namespace,
	})
}
