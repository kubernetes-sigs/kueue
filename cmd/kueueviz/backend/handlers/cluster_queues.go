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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
)

// ClusterQueuesWebSocketHandler streams all cluster queues
func ClusterQueuesWebSocketHandler(dynamicClient dynamic.Interface) gin.HandlerFunc {
	return GenericWebSocketHandler(func(ctx context.Context) (any, error) {
		return fetchClusterQueues(ctx, dynamicClient)
	})
}

// ClusterQueueDetailsWebSocketHandler streams details for a specific cluster queue
func ClusterQueueDetailsWebSocketHandler(dynamicClient dynamic.Interface) gin.HandlerFunc {
	return func(c *gin.Context) {
		clusterQueueName := c.Param("cluster_queue_name")
		GenericWebSocketHandler(func(ctx context.Context) (any, error) {
			return fetchClusterQueueDetails(ctx, dynamicClient, clusterQueueName)
		})(c)
	}
}

// Fetch all cluster queues
func fetchClusterQueues(ctx context.Context, dynamicClient dynamic.Interface) ([]map[string]any, error) {
	// Fetch the list of ClusterQueue objects
	clusterQueues, err := dynamicClient.Resource(ClusterQueuesGVR()).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("error fetching cluster queues: %v", err)
	}

	// Process the ClusterQueue objects
	var result []map[string]any
	for _, item := range clusterQueues.Items {
		// Extract relevant fields
		name := item.GetName()
		spec, specExists := item.Object["spec"].(map[string]any)
		status, statusExists := item.Object["status"].(map[string]any)

		var cohort string
		var resourceGroups []any
		if specExists {
			cohort, _ = spec["cohort"].(string)
			resourceGroups, _ = spec["resourceGroups"].([]any)
		}

		var admittedWorkloads, pendingWorkloads, reservingWorkloads int64
		if statusExists {
			admittedWorkloads, _ = status["admittedWorkloads"].(int64)
			pendingWorkloads, _ = status["pendingWorkloads"].(int64)
			reservingWorkloads, _ = status["reservingWorkloads"].(int64)
		}

		// Extract flavors from resourceGroups
		var flavors []string
		for _, rg := range resourceGroups {
			rgMap, ok := rg.(map[string]any)
			if !ok {
				continue
			}
			flavorsList, _ := rgMap["flavors"].([]any)
			for _, flavor := range flavorsList {
				flavorMap, ok := flavor.(map[string]any)
				if !ok {
					continue
				}
				if flavorName, ok := flavorMap["name"].(string); ok {
					flavors = append(flavors, flavorName)
				}
			}
		}

		// Add the cluster queue to the result list
		result = append(result, map[string]any{
			"name":               name,
			"cohort":             cohort,
			"resourceGroups":     resourceGroups,
			"admittedWorkloads":  admittedWorkloads,
			"pendingWorkloads":   pendingWorkloads,
			"reservingWorkloads": reservingWorkloads,
			"flavors":            flavors,
		})
	}

	return result, nil
}

// Fetch details for a specific cluster queue
func fetchClusterQueueDetails(ctx context.Context, dynamicClient dynamic.Interface, clusterQueueName string) (map[string]any, error) {
	// Fetch the specific ClusterQueue
	clusterQueue, err := dynamicClient.Resource(ClusterQueuesGVR()).Get(ctx, clusterQueueName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("error fetching cluster queue %s: %v", clusterQueueName, err)
	}

	// Retrieve all LocalQueues
	localQueues, err := dynamicClient.Resource(LocalQueuesGVR()).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("error fetching local queues: %v", err)
	}

	// Filter LocalQueues based on the ClusterQueue name
	var queuesUsingClusterQueue []map[string]any
	for _, item := range localQueues.Items {
		spec, specExists := item.Object["spec"].(map[string]any)
		if !specExists {
			continue
		}
		clusterQueueRef, _ := spec["clusterQueue"].(string)
		if clusterQueueRef != clusterQueueName {
			continue
		}

		// Extract relevant fields
		namespace := item.GetNamespace()
		name := item.GetName()
		status, statusExists := item.Object["status"].(map[string]any)

		var reservation, usage any
		if statusExists {
			reservation = status["flavorsReservation"]
			usage = status["flavorUsage"]
		}

		queuesUsingClusterQueue = append(queuesUsingClusterQueue, map[string]any{
			"namespace":   namespace,
			"name":        name,
			"reservation": reservation,
			"usage":       usage,
		})
	}

	// Attach the queues information to the ClusterQueue details
	clusterQueueDetails := clusterQueue.Object
	clusterQueueDetails["queues"] = queuesUsingClusterQueue

	return clusterQueueDetails, nil
}

func fetchClusterQueuesList(ctx context.Context, dynamicClient dynamic.Interface) (*unstructured.UnstructuredList, error) {
	clusterQueues, err := dynamicClient.Resource(ClusterQueuesGVR()).List(ctx, metav1.ListOptions{})
	return clusterQueues, err
}
