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
	"maps"
	"sync"

	corev1 "k8s.io/api/core/v1"

	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/util/queue"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
)

type LocalQueue struct {
	sync.RWMutex
	key                queue.LocalQueueReference
	reservingWorkloads int
	admittedWorkloads  int
	totalReserved      resources.FlavorResourceQuantities
	admittedUsage      resources.FlavorResourceQuantities
	// values extracted from K8s labels/annotations, used as custom Prometheus metric labels
	customMetricLabelValues []string
	labels                  map[string]string
}

func (q *LocalQueue) GetAdmittedUsage() corev1.ResourceList {
	q.RLock()
	defer q.RUnlock()
	return q.admittedUsage.FlattenFlavors().ToResourceList()
}

func (q *LocalQueue) GetLabels() map[string]string {
	q.RLock()
	defer q.RUnlock()
	return maps.Clone(q.labels)
}

func (q *LocalQueue) resetFlavorsAndResources(cqUsage resources.FlavorResourceQuantities, cqAdmittedUsage resources.FlavorResourceQuantities) {
	// Clean up removed flavors or resources.
	q.Lock()
	defer q.Unlock()
	q.totalReserved = resetUsage(q.totalReserved, cqUsage)
	q.admittedUsage = resetUsage(q.admittedUsage, cqAdmittedUsage)
}

func (q *LocalQueue) updateAdmittedUsage(usage resources.FlavorResourceQuantities, op usageOp) {
	q.Lock()
	defer q.Unlock()
	updateFlavorUsage(usage, q.admittedUsage, op)
}

func (q *LocalQueue) reportActiveWorkloads(tracker *roletracker.RoleTracker) {
	namespace, name := queue.MustParseLocalQueueReference(q.key)
	lqRef := metrics.LocalQueueReference{
		Name:      name,
		Namespace: namespace,
	}
	metrics.ReportLocalQueueAdmittedActiveWorkloads(lqRef, q.admittedWorkloads, q.customMetricLabelValues, tracker)
	metrics.ReportLocalQueueReservingActiveWorkloads(lqRef, q.reservingWorkloads, q.customMetricLabelValues, tracker)
}

func (q *LocalQueue) reportResourceMetrics(cqQuotas map[resources.FlavorResource]ResourceQuota, tracker *roletracker.RoleTracker) {
	namespace, name := queue.MustParseLocalQueueReference(q.key)
	lqRef := metrics.LocalQueueReference{Name: name, Namespace: namespace}
	for fr := range cqQuotas {
		fName, rName := string(fr.Flavor), string(fr.Resource)
		metrics.ReportLocalQueueResourceReservations(lqRef, fName, rName, resourceFloat(fr.Resource, q.totalReserved[fr]), q.customMetricLabelValues, tracker)
		metrics.ReportLocalQueueResourceUsage(lqRef, fName, rName, resourceFloat(fr.Resource, q.admittedUsage[fr]), q.customMetricLabelValues, tracker)
	}
}

func (q *LocalQueue) shouldExposeMetrics(lqMetrics *metrics.LocalQueueMetricsConfig) bool {
	q.RLock()
	defer q.RUnlock()
	return lqMetrics.ShouldExposeLocalQueueMetrics(q.labels)
}
