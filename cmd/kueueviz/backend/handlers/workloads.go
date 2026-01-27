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
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"

	kueueapi "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

type workloadResult struct {
	*kueueapi.Workload

	Preemption       map[string]any   `json:"preemption"`
	ClusterQueueName string           `json:"clusterQueueName,omitempty"`
	Pods             []map[string]any `json:"pods,omitempty"`
}

func (h *Handlers) WorkloadsWebSocketHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Extract namespace query parameter if provided
		namespace := c.Query("namespace")
		h.GenericWebSocketHandler(func(ctx context.Context) (any, error) {
			workloads, err := h.fetchWorkloads(ctx, namespace)
			result := map[string]any{
				"workloads": workloads,
			}
			return result, err
		})(c)
	}
}

func (h *Handlers) WorkloadDetailsWebSocketHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		namespace := c.Param("namespace")
		workloadName := c.Param("workload_name")
		h.GenericWebSocketHandler(func(ctx context.Context) (any, error) {
			return h.fetchWorkloadDetails(ctx, namespace, workloadName)
		})(c)
	}
}

func (h *Handlers) fetchWorkloads(ctx context.Context, namespace string) (any, error) {
	wl := &kueueapi.WorkloadList{}
	err := h.client.List(ctx, wl, ctrlclient.InNamespace(namespace))

	if err != nil {
		return nil, fmt.Errorf("error fetching workloads: %v", err)
	}

	return wl, nil
}

func (h *Handlers) fetchWorkloadDetails(ctx context.Context, namespace, workloadName string) (any, error) {
	// Fetch the workload details
	w := &kueueapi.Workload{}
	err := h.client.Get(ctx, ctrlclient.ObjectKey{Namespace: namespace, Name: workloadName}, w)
	if err != nil {
		return nil, fmt.Errorf("error fetching workload %s: %v", workloadName, err)
	}

	// Add preemption details if available
	preempted := apimeta.FindStatusCondition(w.Status.Conditions, kueueapi.WorkloadPreempted)

	res := workloadResult{
		Workload:   w,
		Preemption: map[string]any{},
	}
	if preempted != nil {
		res.Preemption["preempted"] = preempted.Status == metav1.ConditionTrue
		res.Preemption["reason"] = preempted.Reason
	}

	// Get the local queue name from workload's spec
	localQueueName := string(w.Spec.QueueName)

	// If local queue name is found, fetch the local queue and its cluster queue
	if localQueueName != "" {
		lq := &kueueapi.LocalQueue{}
		err = h.client.Get(ctx, ctrlclient.ObjectKey{Namespace: namespace, Name: localQueueName}, lq)
		if err != nil {
			return nil, fmt.Errorf("error fetching local queue %s: %v", localQueueName, err)
		}
		cqn := string(lq.Spec.ClusterQueue)
		if cqn != "" {
			res.ClusterQueueName = cqn
		} else {
			res.ClusterQueueName = "Unknown"
		}
	} else {
		res.ClusterQueueName = "Unknown"
	}

	return w, nil
}

func (h *Handlers) WorkloadEventsWebSocketHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		namespace := c.Param("namespace")
		workloadName := c.Param("workload_name")

		h.GenericWebSocketHandler(func(ctx context.Context) (any, error) {
			return h.fetchWorkloadEvents(ctx, namespace, workloadName)
		})(c)
	}
}

func (h *Handlers) fetchWorkloadEvents(ctx context.Context, namespace, workloadName string) (any, error) {
	el := &corev1.EventList{}
	err := h.client.List(ctx, el, &ctrlclient.ListOptions{
		Namespace: namespace,
		// TODO: needed index here
		FieldSelector: fields.OneTermEqualSelector("involvedObject.name", workloadName),
	})
	if err != nil {
		return nil, fmt.Errorf("error fetching events for workload %s: %v", workloadName, err)
	}

	return el.Items, nil
}
