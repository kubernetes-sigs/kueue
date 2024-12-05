/*
Copyright 2024 The Kubernetes Authors.

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
	"fmt"

	"github.com/gin-gonic/gin"
	"k8s.io/client-go/dynamic"
)

// CohortsWebSocketHandler streams all cohorts
func CohortsWebSocketHandler(dynamicClient dynamic.Interface) gin.HandlerFunc {
	return GenericWebSocketHandler(func() (interface{}, error) {
		return fetchCohorts(dynamicClient)
	})
}

// CohortDetailsWebSocketHandler streams details for a specific cohort
func CohortDetailsWebSocketHandler(dynamicClient dynamic.Interface) gin.HandlerFunc {
	return func(c *gin.Context) {
		cohortName := c.Param("cohort_name")

		GenericWebSocketHandler(func() (interface{}, error) {
			return fetchCohortDetails(dynamicClient, cohortName)
		})(c)
	}
}

// Fetch all cohorts
func fetchCohorts(dynamicClient dynamic.Interface) (interface{}, error) {
	clusterQueues, err := fetchClusterQueuesList(dynamicClient)

	if err != nil {
		return nil, fmt.Errorf("error fetching cohorts: %v", err)
	}
	cohorts := make(map[string]map[string]interface{})

	// Iterate through cluster queue items
	for _, item := range clusterQueues.Items {
		// Extract spec and metadata
		spec, specExists := item.Object["spec"].(map[string]interface{})
		metadata, metadataExists := item.Object["metadata"].(map[string]interface{})
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
			cohorts[cohortName] = map[string]interface{}{
				"name":          cohortName,
				"clusterQueues": []map[string]interface{}{},
			}
		}

		// Add the current cluster queue to the cohort
		clusterQueuesList := cohorts[cohortName]["clusterQueues"].([]map[string]interface{})
		clusterQueuesList = append(clusterQueuesList, map[string]interface{}{
			"name": queueName,
		})
		cohorts[cohortName]["clusterQueues"] = clusterQueuesList
	}

	// Convert the cohorts map to a list
	var result []map[string]interface{}
	for _, cohort := range cohorts {
		result = append(result, cohort)
	}

	return result, nil
}

// Fetch details for a specific cohort
func fetchCohortDetails(dynamicClient dynamic.Interface, cohortName string) (map[string]interface{}, error) {
	// Retrieve all cluster queues
	clusterQueues, err := fetchClusterQueuesList(dynamicClient)
	if err != nil {
		return nil, fmt.Errorf("error fetching cohort details: %v", err)
	}

	// Prepare the result
	cohortDetails := make(map[string]interface{})
	cohortDetails["cohort"] = cohortName
	cohortDetails["clusterQueues"] = []map[string]interface{}{}

	// Iterate through the cluster queues and filter by cohort name
	for _, item := range clusterQueues.Items {
		queue := item.Object
		if queueSpec, ok := queue["spec"].(map[string]interface{}); ok {
			if queueSpec["cohort"] == cohortName {
				queueDetails := map[string]interface{}{
					"name":   item.GetName(),
					"spec":   queueSpec,
					"status": queue["status"],
				}
				cohortDetails["clusterQueues"] = append(cohortDetails["clusterQueues"].([]map[string]interface{}), queueDetails)
			}
		}
	}

	return cohortDetails, nil
}
