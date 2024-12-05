/*
Copyright 2024 The Kubernetes Authors.

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
		GenericWebSocketHandler(func() (interface{}, error) {
			return fetchDashboardData(dynamicClient)
		})(c)
	}
}

func fetchDashboardData(dynamicClient dynamic.Interface) (map[string]interface{}, error) {
	resourceFlavors, _ := fetchResourceFlavors(dynamicClient)
	clusterQueues, _ := fetchClusterQueues(dynamicClient)
	localQueues, _ := fetchLocalQueues(dynamicClient)
	workloads := fetchWorkloadsDashboardData(dynamicClient)
	result := map[string]interface{}{
		"flavors":       removeManagedFields(resourceFlavors),
		"clusterQueues": removeManagedFields(clusterQueues),
		"queues":        removeManagedFields(localQueues),
		"workloads":     removeManagedFields(workloads),
	}
	return result, nil

}

func fetchWorkloadsDashboardData(dynamicClient dynamic.Interface) interface{} {
	workloadList, err := dynamicClient.Resource(WorkloadsGVR()).List(context.TODO(), metav1.ListOptions{})
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

		podList, err := dynamicClient.Resource(PodsGVR()).Namespace(namespace).List(context.TODO(), metav1.ListOptions{})
		podList = removeManagedFieldsFromUnstructuredList(podList)
		if err != nil {
			fmt.Printf("error fetching pods in namespace %s: %v", namespace, err)
			return nil
		}

		var workloadPods []map[string]interface{}
		for _, pod := range podList.Items {
			podLabels, _, _ := unstructured.NestedStringMap(pod.Object, "metadata", "labels")
			controllerUID := podLabels["controller-uid"]
			if controllerUID == jobUID {
				podDetails := map[string]interface{}{
					"name":   pod.Object["metadata"].(map[string]interface{})["name"],
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

		preemption := map[string]interface{}{"preempted": preempted, "reason": preemptionReason}
		unstructured.SetNestedField(workload.Object, preemption, "preemption")
		addPodsToWorkload(&workload, workloadPods)
		workloadsByUID[workloadUID] = workloadName
		processedWorkloads = append(processedWorkloads, workload)
	}
	workloads := map[string]interface{}{
		"items":            processedWorkloads,
		"workloads_by_uid": workloadsByUID,
	}

	return workloads
}

func addPodsToWorkload(workload *unstructured.Unstructured, pods []map[string]interface{}) error {
	// Convert []map[string]interface{} to []interface{}
	var podsInterface []interface{}
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
	obj.Object = removeManagedFields(obj.Object).(map[string]interface{})
	return obj
}

// removeManagedFields recursively removes "managedFields" from maps and slices.
func removeManagedFields(obj interface{}) interface{} {
	switch val := obj.(type) {
	case map[string]interface{}:
		// Remove "managedFields" if present
		delete(val, "managedFields")

		// Recursively apply to nested items
		for key, value := range val {
			val[key] = removeManagedFields(value)
		}
		return val

	case []interface{}:
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
