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
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/metrics"
	utilqueue "sigs.k8s.io/kueue/pkg/util/queue"
)

const (
	// Fallback reason when no valid condition is present
	reasonPending = "Pending"
)

// Extracts the scheduling-relevant reason from a workload's QuotaReserved condition.
func workloadQueueReason(wl *kueue.Workload) string {
	cond := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadQuotaReserved)
	if cond == nil || cond.Status == metav1.ConditionTrue || cond.Reason == "" {
		return reasonPending
	}
	return cond.Reason
}

// Counts pending workloads in a ClusterQueue grouped by reason.
func cqQueuedReasonCounts(cq *ClusterQueue) map[string]int {
	counts := make(map[string]int)
	for _, info := range cq.Snapshot() {
		counts[workloadQueueReason(info.Obj)]++
	}
	return counts
}

// Counts pending workloads in a LocalQueue grouped by reason.
func lqQueuedReasonCounts(q *LocalQueue) map[string]int {
	counts := make(map[string]int)
	for _, info := range q.items {
		counts[workloadQueueReason(info.Obj)]++
	}
	return counts
}

// Publishes queued workload metrics for a ClusterQueue.
// Must be called with at least a read lock held on the Manager.
func reportCQQueuedWorkloads(m *Manager, cq *ClusterQueue) {
	customLabelValues := m.customLabels.CQGet(cq.name)
	metrics.ReportQueuedWorkloads(
		cq.name,
		cqQueuedReasonCounts(cq),
		customLabelValues,
		m.roleTracker,
	)
}

// Publishes queued workload metrics for a LocalQueue.
// No-ops when LocalQueue metrics are disabled.
// Must be called with at least a read lock held on the Manager.
func reportLQQueuedWorkloads(m *Manager, q *LocalQueue) {
	if !m.lqMetrics.IsEnabled() {
		return
	}

	ns, name := utilqueue.MustParseLocalQueueReference(q.Key)

	lqRef := metrics.LocalQueueReference{
		Name:      name,
		Namespace: ns,
	}
	customLabelValues := m.customLabels.LQGet(q.Key)
	metrics.ReportLocalQueueQueuedWorkloads(
		lqRef,
		lqQueuedReasonCounts(q),
		customLabelValues,
		m.roleTracker,
	)
}
