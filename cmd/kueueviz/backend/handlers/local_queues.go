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

// LocalQueuesWebSocketHandler streams all local queues
func (h *Handlers) LocalQueuesWebSocketHandler() gin.HandlerFunc {
	return h.GenericWebSocketHandler(func(ctx context.Context) (any, error) {
		return h.fetchLocalQueues(ctx)
	}, LocalQueuesGVK())
}

// LocalQueueDetailsWebSocketHandler streams details for a specific local queue
func (h *Handlers) LocalQueueDetailsWebSocketHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		namespace := c.Param("namespace")
		queueName := c.Param("queue_name")

		h.GenericWebSocketHandler(func(ctx context.Context) (any, error) {
			return h.fetchLocalQueueDetails(ctx, namespace, queueName)
		}, LocalQueuesGVK())(c)
	}
}

// Fetch all local queues
func (h *Handlers) fetchLocalQueues(ctx context.Context) (any, error) {
	lql := &kueueapi.LocalQueueList{}
	err := h.client.List(ctx, lql)
	if err != nil {
		return nil, fmt.Errorf("error fetching local queues: %v", err)
	}

	var queues []map[string]any
	for _, item := range lql.Items {
		queues = append(queues, map[string]any{
			"namespace": item.GetNamespace(),
			"name":      item.GetName(),
			"spec": map[string]any{
				"clusterQueue": string(item.Spec.ClusterQueue),
			},
			"status": map[string]any{
				"admittedWorkloads":  item.Status.AdmittedWorkloads,
				"pendingWorkloads":   item.Status.PendingWorkloads,
				"reservingWorkloads": item.Status.ReservingWorkloads,
				"flavorsUsage":       convertLocalQueueFlavorsUsage(item.Status.FlavorsUsage),
				"flavorsReservation": convertLocalQueueFlavorsUsage(item.Status.FlavorsReservation),
				"conditions":         item.Status.Conditions,
			},
		})
	}
	return queues, nil
}

// Fetch details for a specific local queue
func (h *Handlers) fetchLocalQueueDetails(ctx context.Context, namespace, queueName string) (any, error) {
	lq := &kueueapi.LocalQueue{}
	err := h.client.Get(ctx, ctrlclient.ObjectKey{Namespace: namespace, Name: queueName}, lq)
	if err != nil {
		return nil, fmt.Errorf("error fetching details for local queue %s: %v", queueName, err)
	}
	return lq, nil
}
