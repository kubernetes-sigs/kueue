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
	"kueueviz/middleware"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"

	kueueapi "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

// WorkloadsDashboardWebSocketHandler streams workloads along with attached pod details
// Watches Workloads, Pods, ClusterQueues, LocalQueues, and ResourceFlavors for comprehensive updates
func (h *Handlers) WorkloadsDashboardWebSocketHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Extract namespace query parameter if provided
		namespace := c.Query("namespace")

		identity, _ := middleware.IdentityFromContext(c)
		// Create a closure that captures the namespace parameter and identity
		dataFetcher := func(ctx context.Context) (any, error) {
			return h.fetchDashboardData(ctx, namespace, identity)
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

func (h *Handlers) fetchDashboardData(ctx context.Context, namespace string, identity middleware.Identity) (map[string]any, error) {
	var resourceFlavors any = []any{}
	var clusterQueues any = []any{}

	hasClusterAccess := true
	if h.authorizer != nil {
		allowed, err := h.authorizer.Authorize(ctx, identity, middleware.ResourceAccess("list", ClusterQueuesGVR(), "", ""))
		if err != nil || !allowed {
			hasClusterAccess = false
		}
	}

	var err error
	if hasClusterAccess {
		resourceFlavors, err = h.fetchResourceFlavors(ctx)
		if err != nil {
			return nil, err
		}
		clusterQueues, err = h.fetchClusterQueues(ctx)
		if err != nil {
			return nil, err
		}
	}

	localQueues, err := h.fetchLocalQueues(ctx, namespace, identity)
	if err != nil {
		return nil, err
	}
	workloads, err := h.fetchWorkloadsDashboardData(ctx, namespace, identity)
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

func (h *Handlers) fetchWorkloadsDashboardData(ctx context.Context, namespace string, identity middleware.Identity) (any, error) {
	wql := &kueueapi.WorkloadList{}
	err := h.client.List(ctx, wql, ctrlclient.InNamespace(namespace))

	if err != nil {
		return nil, fmt.Errorf("error fetching workloads in namespace %s: %w", namespace, err)
	}

	items := wql.Items
	workloadsByUID := make(map[types.UID]string, len(items))
	processedWorkloads := make([]workloadResult, 0, len(items))

	podIndex, err := h.buildWorkloadPodsIndex(ctx, identity, items)
	if err != nil {
		return nil, err
	}

	for _, workload := range items {
		workloadName := workload.Name
		workloadUID := workload.UID
		jobUID := workload.Labels["kueue.x-k8s.io/job-uid"]
		workloadPods := podIndex.podsFor(workload.Namespace, jobUID)

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

// workloadPodsIndex stores dashboard pod details by namespace and controller UID.
// The namespace key preserves the All namespaces dashboard view semantics.
type workloadPodsIndex struct {
	podsByNamespace map[string]map[string][]map[string]any
}

// buildWorkloadPodsIndex lists pods once per workload namespace and indexes them
// for lookup by each workload's job UID, but respects RBAC.
func (h *Handlers) buildWorkloadPodsIndex(ctx context.Context, identity middleware.Identity, workloads []kueueapi.Workload) (workloadPodsIndex, error) {
	workloadNamespaces := make(map[string]struct{})
	for i := range workloads {
		workloadNamespaces[workloads[i].Namespace] = struct{}{}
	}

	index := workloadPodsIndex{
		podsByNamespace: make(map[string]map[string][]map[string]any, len(workloadNamespaces)),
	}
	for namespace := range workloadNamespaces {
		if h.authorizer != nil {
			allowed, err := h.authorizer.Authorize(ctx, identity, middleware.ResourceAccess("list", PodsGVR(), namespace, ""))
			if err != nil || !allowed {
				continue
			}
		}

		pl := &corev1.PodList{}
		if err := h.client.List(ctx, pl, ctrlclient.InNamespace(namespace)); err != nil {
			return workloadPodsIndex{}, fmt.Errorf("error fetching pods in namespace %s: %w", namespace, err)
		}

		podsByControllerUID := make(map[string][]map[string]any)
		for _, pod := range pl.Items {
			podLabels := pod.GetLabels()
			controllerUID := podLabels["controller-uid"]
			podDetails := map[string]any{
				"name":   pod.GetName(),
				"status": pod.Status,
			}
			podsByControllerUID[controllerUID] = append(podsByControllerUID[controllerUID], podDetails)
		}
		index.podsByNamespace[namespace] = podsByControllerUID
	}
	return index, nil
}

// podsFor returns the pod details matching the workload namespace and job UID.
func (i workloadPodsIndex) podsFor(namespace, jobUID string) []map[string]any {
	return i.podsByNamespace[namespace][jobUID]
}
