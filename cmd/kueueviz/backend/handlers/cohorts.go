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
	"k8s.io/client-go/dynamic"
)

// CohortsWebSocketHandler streams all cohorts
func CohortsWebSocketHandler(dynamicClient dynamic.Interface) gin.HandlerFunc {
	return GenericWebSocketHandler(func(ctx context.Context) (any, error) {
		return fetchCohorts(ctx, dynamicClient)
	})
}

// CohortDetailsWebSocketHandler streams details for a specific cohort
func CohortDetailsWebSocketHandler(dynamicClient dynamic.Interface) gin.HandlerFunc {
	return func(c *gin.Context) {
		cohortName := c.Param("cohort_name")

		GenericWebSocketHandler(func(ctx context.Context) (any, error) {
			return fetchCohortDetails(ctx, dynamicClient, cohortName)
		})(c)
	}
}

// Fetch all cohorts
func fetchCohorts(ctx context.Context, dynamicClient dynamic.Interface) (any, error) {
	clusterQueues, err := fetchClusterQueuesList(ctx, dynamicClient)

	if err != nil {
		return nil, fmt.Errorf("error fetching cohorts: %v", err)
	}
	cohorts := make(map[string]map[string]any)

	// Iterate through cluster queue items
	for _, item := range clusterQueues.Items {
		// Extract spec and metadata
		spec, specExists := item.Object["spec"].(map[string]any)
		metadata, metadataExists := item.Object["metadata"].(map[string]any)
		if !specExists || !metadataExists {
			continue
		}

		// Get cohort name from the spec
		cohortName, cohortExists := spec["cohort"].(string)
		if !cohortExists || cohortName == "" {
			continue
		}

		// Get cluster queue name
		queueName, queueNameExists := metadata["name"].(string)
		if !queueNameExists {
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
			"name": queueName,
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
func fetchCohortDetails(ctx context.Context, dynamicClient dynamic.Interface, cohortName string) (map[string]any, error) {
	// Retrieve all cluster queues
	clusterQueues, err := fetchClusterQueuesList(ctx, dynamicClient)
	if err != nil {
		return nil, fmt.Errorf("error fetching cohort details: %v", err)
	}

	// Prepare the result
	cohortDetails := make(map[string]any)
	cohortDetails["cohort"] = cohortName
	cohortDetails["clusterQueues"] = []map[string]any{}

	// Iterate through the cluster queues and filter by cohort name
	for _, item := range clusterQueues.Items {
		queue := item.Object
		if queueSpec, ok := queue["spec"].(map[string]any); ok {
			if queueSpec["cohort"] == cohortName {
				queueDetails := map[string]any{
					"name":   item.GetName(),
					"spec":   queueSpec,
					"status": queue["status"],
				}
				cohortDetails["clusterQueues"] = append(cohortDetails["clusterQueues"].([]map[string]any), queueDetails)
			}
		}
	}

	return cohortDetails, nil
}
