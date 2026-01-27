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

	kueueapi "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

// CohortsWebSocketHandler streams all cohorts
func (h *Handlers) CohortsWebSocketHandler() gin.HandlerFunc {
	return h.GenericWebSocketHandler(func(ctx context.Context) (any, error) {
		return h.fetchCohorts(ctx)
	})
}

// CohortDetailsWebSocketHandler streams details for a specific cohort
func (h *Handlers) CohortDetailsWebSocketHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		cohortName := c.Param("cohort_name")

		h.GenericWebSocketHandler(func(ctx context.Context) (any, error) {
			return h.fetchCohortDetails(ctx, cohortName)
		})(c)
	}
}

// Fetch all cohorts
func (h *Handlers) fetchCohorts(ctx context.Context) (any, error) {
	cql := &kueueapi.ClusterQueueList{}
	err := h.client.List(ctx, cql)

	if err != nil {
		return nil, fmt.Errorf("error fetching cohorts: %v", err)
	}
	cohorts := make(map[string]map[string]any)

	// Iterate through cluster queue items
	for _, item := range cql.Items {
		// Get cohort name from the spec
		cohortName := string(item.Spec.CohortName)
		if cohortName == "" {
			continue
		}

		// Initialize the cohort in the map if it doesn't exist
		if _, exists := cohorts[cohortName]; !exists {
			cohorts[cohortName] = map[string]any{
				"name":          cohortName,
				"clusterQueues": []map[string]any{},
			}
		}

		// Add the current cluster queue to the cohort
		clusterQueuesList := cohorts[cohortName]["clusterQueues"].([]map[string]any)
		clusterQueuesList = append(clusterQueuesList, map[string]any{
			"name": item.Name,
		})
		cohorts[cohortName]["clusterQueues"] = clusterQueuesList
	}

	// Convert the cohorts map to a list
	var result []map[string]any
	for _, cohort := range cohorts {
		result = append(result, cohort)
	}

	return result, nil
}

// Fetch details for a specific cohort
func (h *Handlers) fetchCohortDetails(ctx context.Context, cohortName string) (map[string]any, error) {
	// Retrieve all cluster queues
	cql := &kueueapi.ClusterQueueList{}
	err := h.client.List(ctx, cql)
	if err != nil {
		return nil, fmt.Errorf("error fetching cohort details: %v", err)
	}

	// Prepare the result
	cohortDetails := make(map[string]any)
	cohortDetails["cohort"] = cohortName
	cohortDetails["clusterQueues"] = []map[string]any{}

	// Iterate through the cluster queues and filter by cohort name
	for _, item := range cql.Items {
		if string(item.Spec.CohortName) == cohortName {
			queueDetails := map[string]any{
				"name":   item.GetName(),
				"spec":   item.Spec,
				"status": item.Status,
			}
			cohortDetails["clusterQueues"] = append(cohortDetails["clusterQueues"].([]map[string]any), queueDetails)
		}
	}

	return cohortDetails, nil
}
