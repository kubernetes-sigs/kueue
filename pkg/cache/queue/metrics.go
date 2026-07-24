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
	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
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
	active, inadmissible := cq.Pending(m.customLabels)
	if m.statusChecker != nil && !m.statusChecker.ClusterQueueActive(cq.name) {
		inadmissible = metrics.MergedTracker(inadmissible, active)
		active = metrics.NewLabelValsTracker()
	}
	cqCustomLabels := m.customLabels.CQGet(cq.name)

	if features.Enabled(features.CustomMetricLabels) && m.customLabels.KindConfigured(configapi.SourceKindWorkload) {
		// Clear the metric to remove series with stale workload label value combinations
		metrics.ClearPendingWorkloads(cq.name)

		// Report data for every recorded unique workload label values combination.
		for wlLabelVals, counts := range metrics.ParallelIter(active, inadmissible) {
			customLabels := m.customLabels.CombineLabelValues(map[configapi.SourceKind][]string{
				configapi.SourceKindClusterQueue: cqCustomLabels,
				configapi.SourceKindWorkload:     wlLabelVals.OrderedList(),
			})
			metrics.ReportPendingWorkloads(cq.name, counts.First, counts.Second, customLabels, m.roleTracker)
		}
	} else {
		metrics.ReportPendingWorkloads(cq.name, active.Total(), inadmissible.Total(), cqCustomLabels, m.roleTracker)
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
}

func reportLQPendingWorkloads(m *Manager, lq *LocalQueue) {
	if !m.lqMetrics.ShouldExposeLocalQueueMetrics(lq.labels) {
		return
	}
	var active, inadmissible int
	if cq := m.getClusterQueueLockless(lq.ClusterQueue); cq != nil {
		active, inadmissible = cq.PendingInLocalQueue(lq.Key)
	}
	if m.statusChecker != nil && !m.statusChecker.ClusterQueueActive(lq.ClusterQueue) {
		inadmissible += active
		active = 0
	}
	namespace, lqName := queue.MustParseLocalQueueReference(lq.Key)
	metrics.ReportLocalQueuePendingWorkloads(metrics.LocalQueueReference{
		Name:      lqName,
		Namespace: namespace,
	}, active, inadmissible, m.customLabels.LQGet(lq.Key), m.roleTracker)
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
