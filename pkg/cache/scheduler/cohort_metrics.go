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
	"slices"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/cache/hierarchy"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/workload"
)

type cohortMetricPoint struct {
	cohortName      kueue.CohortReference
	flavorResource  resources.FlavorResource
	quotaQty        int64
	reservationsQty int64
}

func (c *Cache) RecordCohortMetrics(log logr.Logger, cohortName kueue.CohortReference) {
	if cohortName == "" {
		log.V(4).Info("Cohort name is empty, skipping metrics recording")
		return
	}
	log = c.withCohortLogger(log, cohortName)
	log.V(4).Info("Recording metrics for cohort")

	points := c.collectCohortMetricPoints(cohortName, false)
	for _, p := range points {
		c.applyCohortMetricPoint(p)
	}
}

func (c *Cache) ClearCohortMetrics(log logr.Logger, cohortName kueue.CohortReference) {
	if cohortName == "" {
		log.V(4).Info("Cohort name is empty, skipping clearing metrics")
		return
	}

	log = c.withCohortLogger(log, cohortName)
	log.V(4).Info("Clearing metrics for cohort")

	points := c.collectCohortMetricPoints(cohortName, true)
	for _, p := range points {
		c.applyCohortMetricPoint(p)
	}
}

// collectCohortMetricPoints prepares subtree metric points for the target cohort
// and all cohorts on the path from the target to the root.
// When simulateRemoval=true, it computes post-removal subtree values by subtracting
// the target cohort's subtree contribution from each cohort's current subtree totals.
// This is used when clearing metrics so ancestor subtree gauges are updated too,
// rather than left with stale values after the target cohort is removed.
func (c *Cache) collectCohortMetricPoints(cohortName kueue.CohortReference, simulateRemoval bool) []cohortMetricPoint {
	c.RLock()
	defer c.RUnlock()

	ch := c.hm.Cohort(cohortName)
	if ch == nil || hierarchy.HasCycle(ch) {
		if simulateRemoval {
			return []cohortMetricPoint{{cohortName: cohortName}}
		}
		return nil
	}

	// Memoize subtree reservation totals across the ancestor walk so each subtree is
	// aggregated once per call instead of being recomputed from each ancestor.
	reservationMemo := newSubtreeReservationMemo()
	chSubtreeReservations := reservationMemo.total(ch)

	var points []cohortMetricPoint
	for ancestor := range ch.PathSelfToRoot() {
		ancestorSubtreeQuota := ancestor.resourceNode.SubtreeQuota
		ancestorSubtreeReservations := reservationMemo.total(ancestor)

		if simulateRemoval {
			ancestorSubtreeQuota = ancestorSubtreeQuota.Sub(ch.resourceNode.SubtreeQuota)
			ancestorSubtreeReservations = ancestorSubtreeReservations.Sub(chSubtreeReservations)
		}

		for fr := range flavorResourceKeys(ancestorSubtreeQuota, ancestorSubtreeReservations) {
			points = append(points, cohortMetricPoint{
				cohortName:      ancestor.Name,
				flavorResource:  fr,
				quotaQty:        ancestorSubtreeQuota[fr],
				reservationsQty: ancestorSubtreeReservations[fr],
			})
		}
	}
	return points
}

func (c *Cache) withCohortLogger(log logr.Logger, cohortName kueue.CohortReference) logr.Logger {
	return log.WithValues("cohort", cohortName)
}

func flavorResourceKeys(quota, reservations resources.FlavorResourceQuantities) sets.Set[resources.FlavorResource] {
	keys := sets.New[resources.FlavorResource]()
	keys.Insert(slices.Collect(maps.Keys(quota))...)
	keys.Insert(slices.Collect(maps.Keys(reservations))...)
	return keys
}

func (c *Cache) applyCohortMetricPoint(p cohortMetricPoint) {
	flavor := p.flavorResource.Flavor
	resource := p.flavorResource.Resource

	if p.quotaQty <= 0 {
		metrics.ClearCohortSubtreeQuota(p.cohortName, flavor, resource)
	} else {
		metrics.ReportCohortSubtreeQuota(p.cohortName, flavor, resource, p.quotaQty, c.customLabels.CohortGet(p.cohortName), c.roleTracker)
	}

	if p.reservationsQty <= 0 {
		metrics.ClearCohortSubtreeResourceReservations(p.cohortName, flavor, resource)
	} else {
		metrics.ReportCohortSubtreeResourceReservations(p.cohortName, flavor, resource, p.reservationsQty, c.customLabels.CohortGet(p.cohortName), c.roleTracker)
	}
}

type subtreeReservationMemo struct {
	cache map[*cohort]resources.FlavorResourceQuantities
}

func newSubtreeReservationMemo() subtreeReservationMemo {
	return subtreeReservationMemo{
		cache: make(map[*cohort]resources.FlavorResourceQuantities),
	}
}

// total returns aggregated reservations for all clusterQueues reachable from the
// cohort subtree (direct child CQs and descendant cohorts).
func (m subtreeReservationMemo) total(ch *cohort) resources.FlavorResourceQuantities {
	if cached, found := m.cache[ch]; found {
		return cached
	}

	total := make(resources.FlavorResourceQuantities)
	for _, cq := range ch.ChildCQs() {
		accumulateReservations(total, cq.getResourceNode().Usage)
	}

	for _, child := range ch.ChildCohorts() {
		accumulateReservations(total, m.total(child))
	}

	m.cache[ch] = total
	return total
}

func accumulateReservations(total, usage resources.FlavorResourceQuantities) {
	for fr, qty := range usage {
		total[fr] += qty
	}
}

func (c *Cache) ReportCohortSubtreeAdmittedWorkload(log logr.Logger, wl *kueue.Workload) {
	c.RLock()
	defer c.RUnlock()

	ancestors, err := c.workloadAncestors(wl)
	if err != nil {
		log.Error(err, "Failed getting ancestors for workload", "workload", klog.KObj(wl))
		return
	}

	for _, ancestor := range ancestors {
		metrics.ReportCohortSubtreeAdmittedWorkload(
			ancestor,
			workload.PriorityClassName(wl),
			c.customLabels.CohortGet(ancestor),
			c.roleTracker,
		)
	}
}
