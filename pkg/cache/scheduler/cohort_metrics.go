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
	"github.com/go-logr/logr"
	"k8s.io/utils/ptr"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/cache/hierarchy"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/resources"
)

type cohortMetricPoint struct {
	cohortName          kueue.CohortReference
	flavorResource      resources.FlavorResource
	subtreeNominalQuota int64
	borrowingLimit      *int64
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

	removedSubtreeQuota := ch.resourceNode.SubtreeQuota
	removedQuotas := ch.resourceNode.Quotas
	var points []cohortMetricPoint
	for ancestor := range ch.PathSelfToRoot() {
		ancestorSubtreeQuota := ancestor.resourceNode.SubtreeQuota

		if simulateRemoval {
			ancestorSubtreeQuota = removedSubtreeQuota
		}

		isRemovedCohort := simulateRemoval && ancestor.Name == cohortName

		for flr, qty := range ancestorSubtreeQuota {
			if simulateRemoval {
				qty = max(ancestor.resourceNode.SubtreeQuota[flr]-qty, 0)
			}
			p := cohortMetricPoint{
				cohortName:          ancestor.Name,
				flavorResource:      flr,
				subtreeNominalQuota: qty,
				// Default behavior: borrowing limit is local to the current ancestor.
				borrowingLimit: ancestor.resourceNode.Quotas[flr].BorrowingLimit,
			}

			if isRemovedCohort && removedQuotas[flr].BorrowingLimit != nil {
				p.borrowingLimit = ptr.To(int64(0))
			}
			points = append(points, p)
		}
	}
	return points
}

func (c *Cache) withCohortLogger(log logr.Logger, cohortName kueue.CohortReference) logr.Logger {
	return log.WithValues("cohort", cohortName)
}

func (c *Cache) applyCohortMetricPoint(p cohortMetricPoint) {
	if p.subtreeNominalQuota > 0 {
		metrics.ReportCohortSubtreeQuota(
			p.cohortName,
			p.flavorResource.Flavor,
			p.flavorResource.Resource,
			p.subtreeNominalQuota,
			c.roleTracker,
		)
	} else {
		metrics.ClearCohortSubtreeQuota(
			p.cohortName,
			p.flavorResource.Flavor,
			p.flavorResource.Resource,
		)
	}

	if p.borrowingLimit != nil && *p.borrowingLimit > 0 {
		metrics.ReportCohortSubtreeBorrowingLimits(
			p.cohortName,
			p.flavorResource.Flavor,
			p.flavorResource.Resource,
			*p.borrowingLimit,
			c.roleTracker,
		)
	} else {
		metrics.ClearCohortSubtreeBorrowingLimits(
			p.cohortName,
			p.flavorResource.Flavor,
			p.flavorResource.Resource,
		)
	}
}
