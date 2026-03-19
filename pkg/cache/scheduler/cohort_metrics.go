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
	"k8s.io/klog/v2"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/cache/hierarchy"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/workload"
)

type cohortMetricPoint struct {
	cohortName     kueue.CohortReference
	flavorResource resources.FlavorResource
	qty            int64
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

	removedQuota := ch.resourceNode.SubtreeQuota
	var points []cohortMetricPoint
	for ancestor := range ch.PathSelfToRoot() {
		quotas := ancestor.resourceNode.SubtreeQuota
		if simulateRemoval {
			quotas = removedQuota
		}

		for flr, qty := range quotas {
			if simulateRemoval {
				qty = max(ancestor.resourceNode.SubtreeQuota[flr]-qty, 0)
			}
			points = append(points, cohortMetricPoint{
				cohortName:     ancestor.Name,
				flavorResource: flr,
				qty:            qty,
			})
		}
	}
	return points
}

func (c *Cache) withCohortLogger(log logr.Logger, cohortName kueue.CohortReference) logr.Logger {
	return log.WithValues("cohort", cohortName)
}

func (c *Cache) applyCohortMetricPoint(p cohortMetricPoint) {
	if p.qty <= 0 {
		metrics.ClearCohortSubtreeQuota(
			p.cohortName,
			string(p.flavorResource.Flavor),
			string(p.flavorResource.Resource),
		)
		return
	}
	metrics.ReportCohortSubtreeQuota(
		p.cohortName,
		string(p.flavorResource.Flavor),
		string(p.flavorResource.Resource),
		float64(p.qty),
		c.roleTracker,
	)
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
