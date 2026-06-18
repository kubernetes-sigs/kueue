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
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"

	kueueapi "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

// WorkloadsDashboardWebSocketHandler streams workloads along with attached pod details
// Watches Workloads, Pods, ClusterQueues, LocalQueues, and ResourceFlavors for comprehensive updates
func (h *Handlers) WorkloadsDashboardWebSocketHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Extract namespace query parameter if provided
		namespace := c.Query("namespace")

		// Create a closure that captures the namespace parameter
		dataFetcher := func(ctx context.Context) (any, error) {
			return h.fetchDashboardData(ctx, namespace)
		}

		h.GenericWebSocketHandler(dataFetcher,
			WorkloadsGVK(),
			PodsGVK(),
			ClusterQueuesGVK(),
			LocalQueuesGVK(),
			ResourceFlavorsGVK(),
		)(c)
	}
}

func (h *Handlers) fetchDashboardData(ctx context.Context, namespace string) (map[string]any, error) {
	resourceFlavors, err := h.fetchResourceFlavors(ctx)
	if err != nil {
		return nil, err
	}
	clusterQueues, err := h.fetchClusterQueues(ctx)
	if err != nil {
		return nil, err
	}
	localQueues, err := h.fetchLocalQueues(ctx)
	if err != nil {
		return nil, err
	}
	workloads, err := h.fetchWorkloadsDashboardData(ctx, namespace)
	if err != nil {
		return nil, err
	}
	result := map[string]any{
		"flavors":       resourceFlavors,
		"clusterQueues": clusterQueues,
		"queues":        localQueues,
		"workloads":     workloads,
	}
	return result, nil
}

func (h *Handlers) fetchWorkloadsDashboardData(ctx context.Context, namespace string) (any, error) {
	wql := &kueueapi.WorkloadList{}
	err := h.client.List(ctx, wql, ctrlclient.InNamespace(namespace))

	if err != nil {
		return nil, fmt.Errorf("error fetching workloads in namespace %s: %w", namespace, err)
	}

	workloadsByUID := make(map[types.UID]string)
	var processedWorkloads []workloadResult

	for _, workload := range wql.Items {
		namespace := workload.Namespace
		workloadName := workload.Name
		workloadUID := workload.UID
		jobUID := workload.Labels["kueue.x-k8s.io/job-uid"]

		pl := &corev1.PodList{}
		err = h.client.List(ctx, pl, ctrlclient.InNamespace(namespace))
		if err != nil {
			return nil, fmt.Errorf("error fetching pods in namespace %s: %w", namespace, err)
		}

		var workloadPods []map[string]any
		for _, pod := range pl.Items {
			podLabels := pod.GetLabels()
			controllerUID := podLabels["controller-uid"]
			if controllerUID == jobUID {
				podDetails := map[string]any{
					"name":   pod.GetName(),
					"status": pod.Status,
				}
				workloadPods = append(workloadPods, podDetails)
			}
		}

		cond := meta.FindStatusCondition(workload.Status.Conditions, kueueapi.WorkloadPreempted)

		preemption := map[string]any{"preempted": false, "reason": ""}
		if cond != nil && cond.Status == metav1.ConditionTrue {
			preemption["preempted"] = true
			preemption["reason"] = cond.Reason
		}

		workloadsByUID[workloadUID] = workloadName
		processedWorkloads = append(processedWorkloads, workloadResult{
			Workload:   &workload,
			Preemption: preemption,
			Pods:       workloadPods,
		})
	}
	workloads := map[string]any{
		"items":            processedWorkloads,
		"workloads_by_uid": workloadsByUID,
	}

	return workloads, nil
}
