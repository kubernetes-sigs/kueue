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

func (h *Handlers) LocalQueueWorkloadsWebSocketHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		namespace := c.Param("namespace")
		queueName := c.Param("queue_name")
		h.GenericWebSocketHandler(func(ctx context.Context) (any, error) {
			return h.fetchLocalQueueWorkloads(ctx, namespace, queueName)
		})(c)
	}
}

func (h *Handlers) fetchLocalQueueWorkloads(ctx context.Context, namespace, queueName string) (any, error) {
	wql := &kueueapi.WorkloadList{}
	err := h.client.List(ctx, wql, ctrlclient.InNamespace(namespace))
	if err != nil {
		return nil, fmt.Errorf("error fetching workloads for local queue %s: %v", queueName, err)
	}

	var workloads []any
	for _, item := range wql.Items {
		workloads = append(workloads, item)
	}
	return workloads, nil
}
