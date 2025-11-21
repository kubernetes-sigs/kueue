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
	kueueapi "sigs.k8s.io/kueue/apis/kueue/v1beta1"
)

// LocalQueuesWebSocketHandler streams all local queues
func LocalQueuesWebSocketHandler(client Client) gin.HandlerFunc {
	return GenericWebSocketHandler(func(ctx context.Context) (any, error) {
		return fetchLocalQueues(ctx, client)
	})
}

// LocalQueueDetailsWebSocketHandler streams details for a specific local queue
func LocalQueueDetailsWebSocketHandler(client Client) gin.HandlerFunc {
	return func(c *gin.Context) {
		namespace := c.Param("namespace")
		queueName := c.Param("queue_name")
		GenericWebSocketHandler(func(ctx context.Context) (any, error) {
			return fetchLocalQueueDetails(ctx, client, namespace, queueName)
		})(c)
	}
}

// Fetch all local queues
func fetchLocalQueues(ctx context.Context, client Client) (any, error) {
	lql := &kueueapi.LocalQueueList{}
	err := client.List(ctx, lql)
	if err != nil {
		return nil, fmt.Errorf("error fetching local queues: %v", err)
	}

	type lqResult struct {
		Namespace string                    `json:"namespace"`
		Name      string                    `json:"name"`
		Status    kueueapi.LocalQueueStatus `json:"status,omitempty"`
	}

	var queues []lqResult
	for _, item := range lql.Items {
		queues = append(queues, lqResult{
			Namespace: item.GetNamespace(),
			Name:      item.GetName(),
			Status:    item.Status,
		})
	}
	return queues, nil
}

// Fetch details for a specific local queue
func fetchLocalQueueDetails(ctx context.Context, client Client, namespace, queueName string) (any, error) {
	lq := &kueueapi.LocalQueue{}
	err := client.Get(ctx, ctrlclient.ObjectKey{Namespace: namespace, Name: queueName}, lq)
	if err != nil {
		return nil, fmt.Errorf("error fetching details for local queue %s: %v", queueName, err)
	}
	return lq, nil
}
