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

// ClusterQueuesWebSocketHandler streams all cluster queues
func (h *Handlers) ClusterQueuesWebSocketHandler() gin.HandlerFunc {
	return h.GenericWebSocketHandler(func(ctx context.Context) (any, error) {
		return h.fetchClusterQueues(ctx)
	})
}

// ClusterQueueDetailsWebSocketHandler streams details for a specific cluster queue
func (h *Handlers) ClusterQueueDetailsWebSocketHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		clusterQueueName := c.Param("cluster_queue_name")
		h.GenericWebSocketHandler(func(ctx context.Context) (any, error) {
			return h.fetchClusterQueueDetails(ctx, clusterQueueName)
		})(c)
	}
}

// Fetch all cluster queues
func (h *Handlers) fetchClusterQueues(ctx context.Context) ([]map[string]any, error) {
	// Fetch the list of ClusterQueue objects
	l := &kueueapi.ClusterQueueList{}
	err := h.client.List(ctx, l)
	if err != nil {
		return nil, fmt.Errorf("error fetching cluster queues: %v", err)
	}

	// Process the ClusterQueue objects
	var result []map[string]any
	for _, item := range l.Items {
		// Extract relevant fields
		name := item.GetName()

		cohort := string(item.Spec.CohortName)
		admittedWorkloads := item.Status.AdmittedWorkloads
		pendingWorkloads := item.Status.PendingWorkloads
		reservingWorkloads := item.Status.ReservingWorkloads

		// Extract flavors from resourceGroups
		var flavors []string
		for _, rg := range item.Spec.ResourceGroups {
			for _, flavor := range rg.Flavors {
				flavors = append(flavors, string(flavor.Name))
			}
		}

		// Add the cluster queue to the result list
		result = append(result, map[string]any{
			"name":               name,
			"cohort":             cohort,
			"resourceGroups":     item.Spec.ResourceGroups,
			"admittedWorkloads":  admittedWorkloads,
			"pendingWorkloads":   pendingWorkloads,
			"reservingWorkloads": reservingWorkloads,
			"flavors":            flavors,
		})
	}

	return result, nil
}

// Fetch details for a specific cluster queue
func (h *Handlers) fetchClusterQueueDetails(ctx context.Context, name string) (any, error) {
	// Fetch the specific ClusterQueue
	cq := &kueueapi.ClusterQueue{}
	err := h.client.Get(ctx, ctrlclient.ObjectKey{Name: name}, cq)
	if err != nil {
		return nil, fmt.Errorf("error fetching cluster queue %s: %v", name, err)
	}

	// Retrieve all LocalQueues
	lql := &kueueapi.LocalQueueList{}
	err = h.client.List(ctx, lql)
	if err != nil {
		return nil, fmt.Errorf("error fetching local queues: %v", err)
	}

	// Filter LocalQueues based on the ClusterQueue name
	var queuesUsingClusterQueue []map[string]any
	for _, item := range lql.Items {
		if name != string(item.Spec.ClusterQueue) {
			continue
		}

		var reservation any = item.Status.FlavorsReservation
		var usage any = item.Status.FlavorsUsage

		queuesUsingClusterQueue = append(queuesUsingClusterQueue, map[string]any{
			"namespace":   item.GetNamespace(),
			"name":        item.GetName(),
			"reservation": reservation,
			"usage":       usage,
		})
	}

	type cqResult struct {
		*kueueapi.ClusterQueue

		Queues []map[string]any `json:"queues"`
	}

	// Attach the queues information to the ClusterQueue details
	return cqResult{
		ClusterQueue: cq,
		Queues:       queuesUsingClusterQueue,
	}, nil
}
