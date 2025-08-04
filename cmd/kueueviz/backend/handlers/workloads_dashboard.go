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
	"log"

	"github.com/gin-gonic/gin" // Import v1 for Pod and PodStatus
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
)

// WorkloadsDashboardWebSocketHandler streams workloads along with attached pod details
func WorkloadsDashboardWebSocketHandler(dynamicClient dynamic.Interface) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Extract namespace query parameter if provided
		namespace := c.Query("namespace")

		// Create a closure that captures the namespace parameter
		dataFetcher := func(ctx context.Context) (any, error) {
			return fetchDashboardData(ctx, dynamicClient, namespace)
		}

		GenericWebSocketHandler(dataFetcher)(c)
	}
}

func fetchDashboardData(ctx context.Context, dynamicClient dynamic.Interface, namespace string) (map[string]any, error) {
	resourceFlavors, _ := fetchResourceFlavors(ctx, dynamicClient)
	clusterQueues, _ := fetchClusterQueues(ctx, dynamicClient)
	localQueues, _ := fetchLocalQueues(ctx, dynamicClient)
	workloads := fetchWorkloadsDashboardData(ctx, dynamicClient, namespace)
	result := map[string]any{
		"flavors":       removeManagedFields(resourceFlavors),
		"clusterQueues": removeManagedFields(clusterQueues),
		"queues":        removeManagedFields(localQueues),
		"workloads":     removeManagedFields(workloads),
	}
	return result, nil
}

func fetchWorkloadsDashboardData(ctx context.Context, dynamicClient dynamic.Interface, namespace string) any {
	// Filter workloads by namespace if provided, otherwise fetch all
	workloadList, err := dynamicClient.Resource(WorkloadsGVR()).Namespace(namespace).List(ctx, metav1.ListOptions{})

	if err != nil {
		fmt.Printf("error fetching workloads: %v", err)
	}

	workloadList = removeManagedFieldsFromUnstructuredList(workloadList)
	workloadsByUID := make(map[string]string)
	var processedWorkloads []unstructured.Unstructured

	for _, workload := range workloadList.Items {
		metadata, _, _ := unstructured.NestedMap(workload.Object, "metadata")
		status, _, _ := unstructured.NestedMap(workload.Object, "status")
		labels, _, _ := unstructured.NestedStringMap(metadata, "labels")
		namespace := metadata["namespace"].(string)
		workloadName := metadata["name"].(string)
		workloadUID := metadata["uid"].(string)
		jobUID := labels["kueue.x-k8s.io/job-uid"]

		podList, err := dynamicClient.Resource(PodsGVR()).Namespace(namespace).List(ctx, metav1.ListOptions{})
		podList = removeManagedFieldsFromUnstructuredList(podList)
		if err != nil {
			fmt.Printf("error fetching pods in namespace %s: %v", namespace, err)
			return nil
		}

		var workloadPods []map[string]any
		for _, pod := range podList.Items {
			podLabels, _, _ := unstructured.NestedStringMap(pod.Object, "metadata", "labels")
			controllerUID := podLabels["controller-uid"]
			if controllerUID == jobUID {
				podDetails := map[string]any{
					"name":   pod.Object["metadata"].(map[string]any)["name"],
					"status": pod.Object["status"],
				}
				workloadPods = append(workloadPods, podDetails)
			}
		}

		preempted := false
		if preemptedVal, ok := status["preempted"].(bool); ok {
			preempted = preemptedVal
		} else {
			preempted = false // Default to false if not found or not a bool
		}

		preemptionReason := "None"
		if reason, ok := status["preemptionReason"].(string); ok {
			preemptionReason = reason
		}

		preemption := map[string]any{"preempted": preempted, "reason": preemptionReason}
		if err := unstructured.SetNestedField(workload.Object, preemption, "preemption"); err != nil {
			fmt.Printf("error setting nested field %v", err)
			return nil
		}
		if err := addPodsToWorkload(&workload, workloadPods); err != nil {
			fmt.Printf("error adding pods to workload: %v", err)
			return nil
		}
		workloadsByUID[workloadUID] = workloadName
		processedWorkloads = append(processedWorkloads, workload)
	}
	workloads := map[string]any{
		"items":            processedWorkloads,
		"workloads_by_uid": workloadsByUID,
	}

	return workloads
}

func addPodsToWorkload(workload *unstructured.Unstructured, pods []map[string]any) error {
	// Convert []map[string]any to []any
	var podsInterface []any
	for _, pod := range pods {
		podsInterface = append(podsInterface, pod)
	}

	// Add pods as a field named "pods" in workload.Object
	err := unstructured.SetNestedField(workload.Object, podsInterface, "pods")
	if err != nil {
		log.Printf("Error setting pods in workload: %v", err)
		return err
	}
	return nil
}

// removeManagedFieldsFromUnstructuredList removes "managedFields" recursively from *unstructured.UnstructuredList
func removeManagedFieldsFromUnstructuredList(list *unstructured.UnstructuredList) *unstructured.UnstructuredList {
	for i, item := range list.Items {
		list.Items[i] = *removeManagedFieldsFromUnstructured(&item)
	}
	return list
}

// removeManagedFieldsFromUnstructured removes "managedFields" recursively from *unstructured.Unstructured
func removeManagedFieldsFromUnstructured(obj *unstructured.Unstructured) *unstructured.Unstructured {
	obj.Object = removeManagedFields(obj.Object).(map[string]any)
	return obj
}

// removeManagedFields recursively removes "managedFields" from maps and slices.
func removeManagedFields(obj any) any {
	switch val := obj.(type) {
	case map[string]any:
		// Remove "managedFields" if present
		delete(val, "managedFields")

		// Recursively apply to nested items
		for key, value := range val {
			val[key] = removeManagedFields(value)
		}
		return val

	case []any:
		// Recursively apply to each item in the list
		for i, item := range val {
			val[i] = removeManagedFields(item)
		}
		return val

	default:
		// Return the object as is if it's neither a map nor a slice
		return obj
	}
}
