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

package handlers

import (
	"context"
	"fmt"

	"github.com/gin-gonic/gin"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"

	kueueapi "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

// CohortsWebSocketHandler streams all cohorts
func (h *Handlers) CohortsWebSocketHandler() gin.HandlerFunc {
	// Cohorts are derived from ClusterQueues, so use ClusterQueue informer
	return h.GenericWebSocketHandler(func(ctx context.Context) (any, error) {
		return h.fetchCohorts(ctx)
	}, ClusterQueuesGVK())
}

// CohortDetailsWebSocketHandler streams details for a specific cohort
func (h *Handlers) CohortDetailsWebSocketHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		cohortName := c.Param("cohort_name")

		h.GenericWebSocketHandler(func(ctx context.Context) (any, error) {
			return h.fetchCohortDetails(ctx, cohortName)
		}, ClusterQueuesGVK())(c)
	}
}

// Fetch all cohorts
func (h *Handlers) fetchCohorts(ctx context.Context) (any, error) {
	// Fetch all Cohort CRD objects directly
	cohortList := &kueueapi.CohortList{}
	if err := h.client.List(ctx, cohortList); err != nil {
		return nil, fmt.Errorf("error fetching cohorts: %v", err)
	}

	// Fetch all ClusterQueues to map them to cohorts
	cql := &kueueapi.ClusterQueueList{}
	if err := h.client.List(ctx, cql); err != nil {
		return nil, fmt.Errorf("error fetching cluster queues: %v", err)
	}

	// Build a map of cohort name to ClusterQueues
	cohortQueues := make(map[string][]map[string]any)
	for _, cq := range cql.Items {
		cohortName := string(cq.Spec.CohortName)
		if cohortName == "" {
			continue
		}
		cohortQueues[cohortName] = append(cohortQueues[cohortName], map[string]any{
			"name": cq.Name,
		})
	}

	// Build result from Cohort CRD objects
	var result []map[string]any
	for _, cohort := range cohortList.Items {
		queues := cohortQueues[cohort.Name]
		if queues == nil {
			queues = []map[string]any{}
		}
		item := map[string]any{
			"name":          cohort.Name,
			"clusterQueues": queues,
		}
		if cohort.Spec.ParentName != "" {
			item["parentName"] = string(cohort.Spec.ParentName)
		}
		result = append(result, item)
	}

	return result, nil
}

// Fetch details for a specific cohort
func (h *Handlers) fetchCohortDetails(ctx context.Context, cohortName string) (map[string]any, error) {
	// Fetch the specific Cohort CRD
	cohort := &kueueapi.Cohort{}
	if err := h.client.Get(ctx, ctrlclient.ObjectKey{Name: cohortName}, cohort); err != nil {
		return nil, fmt.Errorf("error fetching cohort %s: %v", cohortName, err)
	}

	// Retrieve all cluster queues
	cql := &kueueapi.ClusterQueueList{}
	if err := h.client.List(ctx, cql); err != nil {
		return nil, fmt.Errorf("error fetching cluster queues: %v", err)
	}

	// Filter ClusterQueues by cohort name
	var clusterQueues []map[string]any
	for _, item := range cql.Items {
		if string(item.Spec.CohortName) == cohortName {
			clusterQueues = append(clusterQueues, map[string]any{
				"name":   item.GetName(),
				"spec":   item.Spec,
				"status": item.Status,
			})
		}
	}

	if clusterQueues == nil {
		clusterQueues = []map[string]any{}
	}

	return map[string]any{
		"cohort":        cohortName,
		"spec":          cohort.Spec,
		"status":        cohort.Status,
		"clusterQueues": clusterQueues,
	}, nil
}
