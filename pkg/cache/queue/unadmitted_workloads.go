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
	"sync"

	"github.com/go-logr/logr"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/util/queue"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/pkg/workload/concurrentadmission"
	workloadfinish "sigs.k8s.io/kueue/pkg/workload/finish"
)

type unadmittedWorkloadStatus struct {
	ClusterQueue        kueue.ClusterQueueReference
	LocalQueueName      kueue.LocalQueueName
	LocalQueueNamespace string
	Reason              string
	UnderlyingCause     string
}

type unadmittedCQStatus struct {
	ClusterQueue    kueue.ClusterQueueReference
	Reason          string
	UnderlyingCause string
}

func (s unadmittedWorkloadStatus) ClusterQueueStatus() unadmittedCQStatus {
	return unadmittedCQStatus{
		ClusterQueue:    s.ClusterQueue,
		Reason:          s.Reason,
		UnderlyingCause: s.UnderlyingCause,
	}
}

// unadmittedWorkloads encapsulates internal state for tracking unadmitted workloads
// and their metric counts per ClusterQueue and LocalQueue.
type unadmittedWorkloads struct {
	rwm      sync.RWMutex
	statuses map[workload.Reference]unadmittedWorkloadStatus
	perCQ    map[unadmittedCQStatus]int
	perLQ    map[unadmittedWorkloadStatus]int
}

func newUnadmittedWorkloads() *unadmittedWorkloads {
	return &unadmittedWorkloads{
		statuses: make(map[workload.Reference]unadmittedWorkloadStatus),
		perCQ:    make(map[unadmittedCQStatus]int),
		perLQ:    make(map[unadmittedWorkloadStatus]int),
	}
}

func (u *unadmittedWorkloads) update(
	log logr.Logger,
	wl *kueue.Workload,
	cqName kueue.ClusterQueueReference,
	lqExists bool,
	m *Manager,
) {
	u.rwm.Lock()
	defer u.rwm.Unlock()

	log = log.WithValues("workload", klog.KObj(wl))
	status := getUnadmittedWorkloadStatus(log, wl)
	wlKey := workload.Key(wl)
	oldStatus, ok := u.statuses[wlKey]

	if status != nil {
		if cqName != "" {
			status.ClusterQueue = cqName
		} else {
			log.V(3).Info("Failed to resolve ClusterQueue for unadmitted workload", "queue", wl.Spec.QueueName)
		}
		if ok && oldStatus == *status {
			log.V(5).Info("Workload unadmitted status unchanged, skipping update")
			return
		}
	}

	log.V(4).Info("Updating unadmitted workload tracking", "oldStatus", oldStatus, "newStatus", status)

	if ok {
		u.decrementStatusCounts(log, oldStatus, lqExists, m)
		delete(u.statuses, wlKey)
	}
	if status != nil {
		u.statuses[wlKey] = *status
		u.incrementStatusCounts(log, *status, lqExists, m)
	}
}

func (u *unadmittedWorkloads) remove(
	log logr.Logger,
	wlKey workload.Reference,
	m *Manager,
) {
	u.rwm.Lock()
	defer u.rwm.Unlock()

	oldStatus, ok := u.statuses[wlKey]
	if ok {
		log.V(4).Info("Removing workload from unadmitted tracking", "workload", wlKey, "status", oldStatus)
		u.decrementStatusCounts(log, oldStatus, true, m)
		delete(u.statuses, wlKey)
	} else {
		log.V(4).Info("Workload to remove was not tracked as unadmitted", "workload", wlKey)
	}
}

func (u *unadmittedWorkloads) incrementStatusCounts(
	log logr.Logger,
	status unadmittedWorkloadStatus,
	lqExists bool,
	m *Manager,
) {
	u.updateUnadmittedWorkloadMetric(log, status, 1, lqExists, m)
}

func (u *unadmittedWorkloads) decrementStatusCounts(
	log logr.Logger,
	status unadmittedWorkloadStatus,
	lqExists bool,
	m *Manager,
) {
	u.updateUnadmittedWorkloadMetric(log, status, -1, lqExists, m)
}

func (u *unadmittedWorkloads) updateUnadmittedWorkloadMetric(
	log logr.Logger,
	status unadmittedWorkloadStatus,
	diff int,
	lqExists bool,
	m *Manager,
) {
	cqKey := status.ClusterQueueStatus()
	u.perCQ[cqKey] += diff
	count := u.perCQ[cqKey]

	if count <= 0 {
		delete(u.perCQ, cqKey)
	}
	if status.ClusterQueue != "" {
		cqCustomLabels := m.customLabels.CQGet(status.ClusterQueue)
		if count <= 0 {
			log.V(4).Info("Clearing CQ unadmitted workload metric", "clusterQueue", status.ClusterQueue, "status", cqKey)
			metrics.ClearUnadmittedWorkloadLabelValues(
				status.ClusterQueue,
				status.Reason,
				status.UnderlyingCause,
				cqCustomLabels,
				m.roleTracker,
			)
		} else {
			log.V(4).Info("Reporting CQ unadmitted workload metric", "clusterQueue", status.ClusterQueue, "status", cqKey, "count", count)
			metrics.ReportUnadmittedWorkload(
				status.ClusterQueue,
				status.Reason,
				status.UnderlyingCause,
				cqCustomLabels,
				m.roleTracker,
				count,
			)
		}
	} else {
		log.V(4).Info("Skipping CQ unadmitted metric report due to empty ClusterQueue", "status", status)
	}

	if !m.lqMetrics.IsEnabled() {
		return
	}
	u.perLQ[status] += diff
	countLQ := u.perLQ[status]

	if countLQ <= 0 {
		delete(u.perLQ, status)
	}

	lqRefKey := queue.NewLocalQueueReference(status.LocalQueueNamespace, status.LocalQueueName)
	if lqExists && status.ClusterQueue != "" {
		lqRef := metrics.LocalQueueReference{Name: status.LocalQueueName, Namespace: status.LocalQueueNamespace}
		lqCustomLabels := m.customLabels.LQGet(lqRefKey)
		if countLQ <= 0 {
			log.V(4).Info("Clearing LQ unadmitted workload metric", "localQueue", lqRefKey, "status", status)
			metrics.ClearLocalQueueUnadmittedWorkloadLabelValues(
				lqRef,
				status.ClusterQueue,
				status.Reason,
				status.UnderlyingCause,
				lqCustomLabels,
				m.roleTracker,
			)
		} else {
			log.V(4).Info("Reporting LQ unadmitted workload metric", "localQueue", lqRefKey, "status", status, "count", countLQ)
			metrics.ReportLocalQueueUnadmittedWorkload(
				lqRef,
				status.ClusterQueue,
				status.Reason,
				status.UnderlyingCause,
				lqCustomLabels,
				m.roleTracker,
				countLQ,
			)
		}
	} else {
		log.V(4).Info("Skipping LQ unadmitted metric report", "status", status, "qExists", lqExists)
	}
}

func (u *unadmittedWorkloads) resyncCQMetrics(cqName kueue.ClusterQueueReference, m *Manager) {
	u.rwm.RLock()
	defer u.rwm.RUnlock()

	for status, count := range u.perCQ {
		if status.ClusterQueue == cqName {
			cqCustomLabels := m.customLabels.CQGet(cqName)
			metrics.ReportUnadmittedWorkload(
				status.ClusterQueue,
				status.Reason,
				status.UnderlyingCause,
				cqCustomLabels,
				m.roleTracker,
				count,
			)
		}
	}
}

func (u *unadmittedWorkloads) resyncLQMetrics(lqRef queue.LocalQueueReference, m *Manager) {
	u.rwm.RLock()
	defer u.rwm.RUnlock()

	lqNamespace, lqName := queue.MustParseLocalQueueReference(lqRef)
	for status, count := range u.perLQ {
		if status.ClusterQueue != "" && status.LocalQueueNamespace == lqNamespace && status.LocalQueueName == lqName {
			lqCustomLabels := m.customLabels.LQGet(lqRef)
			lqRefKey := metrics.LocalQueueReference{Name: lqName, Namespace: lqNamespace}
			metrics.ReportLocalQueueUnadmittedWorkload(
				lqRefKey,
				status.ClusterQueue,
				status.Reason,
				status.UnderlyingCause,
				lqCustomLabels,
				m.roleTracker,
				count,
			)
		}
	}
}

func getUnadmittedWorkloadStatus(log logr.Logger, wl *kueue.Workload) *unadmittedWorkloadStatus {
	if concurrentadmission.IsVariant(wl) {
		log.V(5).Info("Ignoring variant workload for unadmitted metrics tracking", "workload", klog.KObj(wl))
		return nil
	}

	if workload.IsAdmitted(wl) || workloadfinish.IsFinished(wl) || !wl.DeletionTimestamp.IsZero() {
		log.V(4).Info("Workload is not unadmitted",
			"isAdmitted", workload.IsAdmitted(wl),
			"isFinished", workloadfinish.IsFinished(wl),
			"isDeleting", !wl.DeletionTimestamp.IsZero(),
		)
		return nil
	}

	admittedCond := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadAdmitted)

	var reason string
	if admittedCond != nil {
		reason = admittedCond.Reason
	} else {
		// If the UnadmittedWorkloadsExplicitStatus feature gate is disabled, a newly
		// created workload has no status conditions. Fall back to NoReservation.
		reason = kueue.WorkloadAdmittedReasonNoReservation
	}
	quotaReservedCond := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadQuotaReserved)
	underlyingCause := resolveUnadmittedUnderlyingCause(quotaReservedCond)

	return &unadmittedWorkloadStatus{
		LocalQueueName:      wl.Spec.QueueName,
		LocalQueueNamespace: wl.Namespace,
		Reason:              reason,
		UnderlyingCause:     underlyingCause,
	}
}

func resolveUnadmittedUnderlyingCause(quotaReservedCond *metav1.Condition) string {
	if quotaReservedCond == nil {
		// If the scheduler has not evaluated the workload yet, or if the UnadmittedWorkloadsExplicitStatus
		// feature gate is disabled (meaning conditions are not populated), the QuotaReserved condition
		// will be missing. We assume the workload is pending evaluation.
		return kueue.WorkloadQuotaReservedReasonPendingEvaluation
	}
	if quotaReservedCond.Status == metav1.ConditionTrue {
		return ""
	}
	return quotaReservedCond.Reason
}
