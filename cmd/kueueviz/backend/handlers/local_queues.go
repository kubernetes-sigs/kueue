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

// LocalQueuesWebSocketHandler streams all local queues
func LocalQueuesWebSocketHandler(dynamicClient dynamic.Interface) gin.HandlerFunc {
	return GenericWebSocketHandler(func(ctx context.Context) (any, error) {
		return fetchLocalQueues(ctx, dynamicClient)
	})
}

// LocalQueueDetailsWebSocketHandler streams details for a specific local queue
func LocalQueueDetailsWebSocketHandler(dynamicClient dynamic.Interface) gin.HandlerFunc {
	return func(c *gin.Context) {
		namespace := c.Param("namespace")
		queueName := c.Param("queue_name")
		GenericWebSocketHandler(func(ctx context.Context) (any, error) {
			return fetchLocalQueueDetails(ctx, dynamicClient, namespace, queueName)
		})(c)
	}
}

// Fetch all local queues
func fetchLocalQueues(ctx context.Context, dynamicClient dynamic.Interface) (any, error) {
	result, err := dynamicClient.Resource(LocalQueuesGVR()).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("error fetching local queues: %v", err)
	}

	var queues []map[string]any
	for _, item := range result.Items {
		queue := item.Object
		queue["namespace"] = item.GetNamespace()
		queue["name"] = item.GetName()
		status, statusExists := item.Object["status"].(map[string]any)
		// Include the status if it exists
		if statusExists {
			queue["status"] = status
		}
		queues = append(queues, queue)
	}
	return queues, nil
}

// Fetch details for a specific local queue
func fetchLocalQueueDetails(ctx context.Context, dynamicClient dynamic.Interface, namespace, queueName string) (any, error) {
	result, err := dynamicClient.Resource(LocalQueuesGVR()).Namespace(namespace).Get(ctx, queueName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("error fetching details for local queue %s: %v", queueName, err)
	}
	return result.Object, nil
}
