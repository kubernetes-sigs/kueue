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
	// Fetch all Cohort CRD objects directly
	cohortList, err := dynamicClient.Resource(CohortsGVR()).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("error fetching cohorts: %v", err)
	}

	// Fetch all ClusterQueues to map them to cohorts
	clusterQueues, err := fetchClusterQueuesList(ctx, dynamicClient)
	if err != nil {
		return nil, fmt.Errorf("error fetching cluster queues: %v", err)
	}

	// Build a map of cohort name to ClusterQueues
	cohortQueues := make(map[string][]map[string]any)
	for _, item := range clusterQueues.Items {
		spec, specExists := item.Object["spec"].(map[string]any)
		if !specExists {
			continue
		}
		cohortName, _ := spec["cohort"].(string)
		if cohortName == "" {
			continue
		}
		cohortQueues[cohortName] = append(cohortQueues[cohortName], map[string]any{
			"name": item.GetName(),
		})
	}

	// Build result from Cohort CRD objects
	var result []map[string]any
	for _, cohort := range cohortList.Items {
		name := cohort.GetName()
		queues := cohortQueues[name]
		if queues == nil {
			queues = []map[string]any{}
		}
		result = append(result, map[string]any{
			"name":          name,
			"clusterQueues": queues,
		})
	}

	return result, nil
}

// Fetch details for a specific cohort
func fetchCohortDetails(ctx context.Context, dynamicClient dynamic.Interface, cohortName string) (map[string]any, error) {
	// Fetch the specific Cohort CRD
	cohort, err := dynamicClient.Resource(CohortsGVR()).Get(ctx, cohortName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("error fetching cohort %s: %v", cohortName, err)
	}

	// Retrieve all cluster queues
	clusterQueues, err := fetchClusterQueuesList(ctx, dynamicClient)
	if err != nil {
		return nil, fmt.Errorf("error fetching cluster queues: %v", err)
	}

	// Filter ClusterQueues by cohort name
	var clusterQueueDetails []map[string]any
	for _, item := range clusterQueues.Items {
		queue := item.Object
		if queueSpec, ok := queue["spec"].(map[string]any); ok {
			if queueSpec["cohort"] == cohortName {
				clusterQueueDetails = append(clusterQueueDetails, map[string]any{
					"name":   item.GetName(),
					"spec":   queueSpec,
					"status": queue["status"],
				})
			}
		}
	}

	if clusterQueueDetails == nil {
		clusterQueueDetails = []map[string]any{}
	}

	return map[string]any{
		"cohort":        cohortName,
		"spec":          cohort.Object["spec"],
		"status":        cohort.Object["status"],
		"clusterQueues": clusterQueueDetails,
	}, nil
}
