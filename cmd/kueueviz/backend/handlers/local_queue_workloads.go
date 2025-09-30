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

func LocalQueueWorkloadsWebSocketHandler(dynamicClient dynamic.Interface) gin.HandlerFunc {
	return func(c *gin.Context) {
		namespace := c.Param("namespace")
		queueName := c.Param("queue_name")
		GenericWebSocketHandler(func(ctx context.Context) (any, error) {
			return fetchLocalQueueWorkloads(ctx, dynamicClient, namespace, queueName)
		})(c)
	}
}

func fetchLocalQueueWorkloads(ctx context.Context, dynamicClient dynamic.Interface, namespace, queueName string) (any, error) {
	result, err := dynamicClient.Resource(WorkloadsGVR()).Namespace(namespace).List(ctx, metav1.ListOptions{
		FieldSelector: fmt.Sprintf("spec.queueName=%s", queueName),
	})
	if err != nil {
		return nil, fmt.Errorf("error fetching workloads for local queue %s: %v", queueName, err)
	}

	var workloads []map[string]any
	for _, item := range result.Items {
		workloads = append(workloads, item.Object)
	}
	return workloads, nil
}
