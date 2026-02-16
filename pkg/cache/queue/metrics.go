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
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/util/queue"
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

	if !features.Enabled(features.LocalQueueMetrics) {
		return
	}
	for _, lq := range m.localQueues {
		if lq.ClusterQueue == cqRef {
			reportLQPendingWorkloads(m, lq)
		}
	}
}

func reportCQPendingWorkloads(m *Manager, cq *ClusterQueue) {
	active, inadmissible := cq.Pending()
	if m.statusChecker != nil && !m.statusChecker.ClusterQueueActive(cq.name) {
		inadmissible += active
		active = 0
	}
	metrics.ReportPendingWorkloads(cq.name, active, inadmissible, m.roleTracker)
}

func reportLQPendingWorkloads(m *Manager, lq *LocalQueue) {
	if !features.Enabled(features.LocalQueueMetrics) {
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
	}, active, inadmissible, m.roleTracker)
}

func reportLQFinishedWorkloads(m *Manager, lq *LocalQueue) {
	if !features.Enabled(features.LocalQueueMetrics) {
		return
	}
	namespace, lqName := queue.MustParseLocalQueueReference(lq.Key)
	metrics.ReportLocalQueueFinishedWorkloads(metrics.LocalQueueReference{
		Name:      lqName,
		Namespace: namespace,
	}, lq.finishedWorkloads.Len(), m.roleTracker)
}

func reportCQFinishedWorkloads(cq *ClusterQueue, roleTracker *roletracker.RoleTracker) {
	metrics.ReportFinishedWorkloads(cq.name, cq.finishedWorkloads.Len(), roleTracker)
}

func clearCQMetrics(cqRef kueue.ClusterQueueReference) {
	metrics.ClearClusterQueueMetrics(cqRef)
}

func clearLQMetrics(lqRef queue.LocalQueueReference) {
	if !features.Enabled(features.LocalQueueMetrics) {
		return
	}
	namespace, lqName := queue.MustParseLocalQueueReference(lqRef)
	metrics.ClearLocalQueueMetrics(metrics.LocalQueueReference{
		Name:      lqName,
		Namespace: namespace,
	})
}
