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
	"context"
	"time"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/metrics"
)

const pendingWorkloadAgeReportingInterval = 5 * time.Second

// Start refreshes time-dependent queue metrics even when no queue events occur.
func (m *Manager) Start(ctx context.Context) error {
	m.reportPendingWorkloadAges()
	timer := m.clock.NewTimer(pendingWorkloadAgeReportingInterval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C():
			m.reportPendingWorkloadAges()
			timer.Reset(pendingWorkloadAgeReportingInterval)
		}
	}
}

// NeedLeaderElection allows followers to report their view of pending workloads,
// consistent with the other queue gauges.
func (m *Manager) NeedLeaderElection() bool { return false }

func (m *Manager) reportPendingWorkloadAges() {
	m.RLock()
	defer m.RUnlock()
	for _, cq := range m.hm.ClusterQueues() {
		m.reportOldestPendingWorkloadAge(cq)
	}
}

func (m *Manager) reportOldestPendingWorkloadAge(cq *ClusterQueue) {
	age := cq.workloads.oldestPendingAge(m.clock.Now())
	metrics.ReportOldestPendingWorkloadAge(cq.name, age, m.customLabels.CQGet(cq.name), m.roleTracker)
}

// oldestPendingAge scans all pending sets, since scheduling order need not be age
// order. It runs periodically rather than on every queue mutation to avoid a
// linear scan for every workload event.
func (p *PendingWorkloads) oldestPendingAge(now time.Time) time.Duration {
	p.RLock()
	defer p.RUnlock()
	var oldest time.Duration
	for _, info := range p.active.List() {
		oldest = max(oldest, pendingWorkloadAge(info.Obj, now))
	}
	for _, info := range p.inadmissible {
		oldest = max(oldest, pendingWorkloadAge(info.Obj, now))
	}
	for _, info := range p.inflight {
		oldest = max(oldest, pendingWorkloadAge(info.Obj, now))
	}
	return oldest
}

func pendingWorkloadAge(wl *kueue.Workload, now time.Time) time.Duration {
	if !wl.DeletionTimestamp.IsZero() {
		return 0
	}
	start := wl.CreationTimestamp.Time
	if c := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadRequeued); c != nil && c.LastTransitionTime.After(start) {
		start = c.LastTransitionTime.Time
	}
	// Preemption can release quota without updating Requeued. Use the quota
	// release time so the preceding execution is not counted as pending time.
	if apimeta.IsStatusConditionTrue(wl.Status.Conditions, kueue.WorkloadEvicted) {
		if c := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadQuotaReserved); c != nil && c.Status == metav1.ConditionFalse && c.LastTransitionTime.After(start) {
			start = c.LastTransitionTime.Time
		}
	}
	return max(0, now.Sub(start))
}
