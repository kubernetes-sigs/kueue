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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
)

// CohortsWebSocketHandler streams all cohorts
func CohortsWebSocketHandler(dynamicClient dynamic.Interface) gin.HandlerFunc {
	return GenericWebSocketHandler(func() (any, error) {
		return fetchCohorts(dynamicClient)
	})
}

// CohortDetailsWebSocketHandler streams details for a specific cohort
func CohortDetailsWebSocketHandler(dynamicClient dynamic.Interface) gin.HandlerFunc {
	return func(c *gin.Context) {
		cohortName := c.Param("cohort_name")

		GenericWebSocketHandler(func() (any, error) {
			return fetchCohortDetails(dynamicClient, cohortName)
		})(c)
	}
}

// Fetch all cohorts
func fetchCohorts(dynamicClient dynamic.Interface) (any, error) {
	cohortList, err := dynamicClient.Resource(CohortsGVR()).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("error fetching cohorts: %v", err)
	}

	clusterQueues, err := fetchClusterQueuesList(dynamicClient)
	if err != nil {
		return nil, fmt.Errorf("error fetching cluster queues: %v", err)
	}

	cohortToQueues := make(map[string][]map[string]any)

	// Iterate through cluster queue items
	for _, item := range clusterQueues.Items {
		// Extract spec and metadata
		spec, specExists := item.Object["spec"].(map[string]any)
		if !specExists {
			continue
		}

		cohortName, cohortExists := spec["cohort"].(string)
		if !cohortExists || cohortName == "" {
			continue
		}

		// Get cluster queue name
		queueName := item.GetName()
		if cohortToQueues[cohortName] == nil {
			cohortToQueues[cohortName] = []map[string]any{}
		}

		cohortToQueues[cohortName] = append(cohortToQueues[cohortName], map[string]any{
			"name": queueName,
		})
	}

	// Process all cohort resources
	var result []map[string]any
	for _, item := range cohortList.Items {
		cohortName := item.GetName()

		clusterQueues := cohortToQueues[cohortName]
		if clusterQueues == nil {
			clusterQueues = []map[string]any{}
		}

		result = append(result, map[string]any{
			"name":          cohortName,
			"clusterQueues": clusterQueues,
		})
	}

	return result, nil
}

// Fetch details for a specific cohort
func fetchCohortDetails(dynamicClient dynamic.Interface, cohortName string) (map[string]any, error) {
	cohortResource, err := dynamicClient.Resource(CohortsGVR()).Get(context.TODO(), cohortName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("error fetching cohort %s: %v", cohortName, err)
	}

	// Retrieve all cluster queues
	clusterQueues, err := fetchClusterQueuesList(dynamicClient)
	if err != nil {
		return nil, fmt.Errorf("error fetching cluster queues: %v", err)
	}

	// Prepare the result
	cohortDetails := make(map[string]any)
	cohortDetails["cohort"] = cohortName
	cohortDetails["spec"] = cohortResource.Object["spec"]
	cohortDetails["status"] = cohortResource.Object["status"]
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
