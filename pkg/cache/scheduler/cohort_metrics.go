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

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/cache/hierarchy"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/resources"
)

type cohortMetricPoint struct {
	cohortName     kueue.CohortReference
	flavorResource resources.FlavorResource
	quotaQty       int64
	usageQty       int64
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
	removedUsage := ch.resourceNode.Usage

	var points []cohortMetricPoint
	for ancestor := range ch.PathSelfToRoot() {
		quotas := ancestor.resourceNode.SubtreeQuota
		usages := ancestor.resourceNode.Usage

		if simulateRemoval {
			quotas = removedQuota
			usages = removedUsage
		}

		keys := make(map[resources.FlavorResource]struct{}, len(quotas)+len(usages))
		for fr := range quotas {
			keys[fr] = struct{}{}
		}
		for fr := range usages {
			keys[fr] = struct{}{}
		}

		for fr := range keys {
			quotaQty := quotas[fr]
			usageQty := usages[fr]
			if simulateRemoval {
				quotaQty = max(ancestor.resourceNode.SubtreeQuota[fr]-quotaQty, 0)
				usageQty = max(ancestor.resourceNode.Usage[fr]-usageQty, 0)
			}
			points = append(points, cohortMetricPoint{
				cohortName:     ancestor.Name,
				flavorResource: fr,
				quotaQty:       quotaQty,
				usageQty:       usageQty,
			})
		}
	}
	return points
}

func (c *Cache) withCohortLogger(log logr.Logger, cohortName kueue.CohortReference) logr.Logger {
	return log.WithValues("cohort", cohortName)
}

func (c *Cache) applyCohortMetricPoint(p cohortMetricPoint) {
	flavor := string(p.flavorResource.Flavor)
	resource := string(p.flavorResource.Resource)

	if p.quotaQty <= 0 {
		metrics.ClearCohortSubtreeQuota(p.cohortName, flavor, resource)
	} else {
		metrics.ReportCohortSubtreeQuota(p.cohortName, flavor, resource, float64(p.quotaQty), c.roleTracker)
	}

	if p.usageQty <= 0 {
		metrics.ClearCohortSubtreeResourceUsage(p.cohortName, flavor, resource)
	} else {
		metrics.ReportCohortSubtreeResourceUsage(p.cohortName, flavor, resource, float64(p.usageQty), c.roleTracker)
	}
}
