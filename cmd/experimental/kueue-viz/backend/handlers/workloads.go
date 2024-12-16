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

	"github.com/gin-gonic/gin"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
)

func WorkloadsWebSocketHandler(dynamicClient dynamic.Interface) gin.HandlerFunc {
	return GenericWebSocketHandler(func() (interface{}, error) {
		workloads, err := fetchWorkloads(dynamicClient)
		result := map[string]interface{}{
			"workloads": workloads,
		}
		return result, err
	})
}

func WorkloadDetailsWebSocketHandler(dynamicClient dynamic.Interface) gin.HandlerFunc {
	return func(c *gin.Context) {
		namespace := c.Param("namespace")
		workloadName := c.Param("workload_name")
		GenericWebSocketHandler(func() (interface{}, error) {
			return fetchWorkloadDetails(dynamicClient, namespace, workloadName)
		})(c)
	}
}

func fetchWorkloads(dynamicClient dynamic.Interface) (interface{}, error) {
	result, err := dynamicClient.Resource(WorkloadsGVR()).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("error fetching resource flavors: %v", err)
	}
	return result, nil
}

func fetchWorkloadDetails(dynamicClient dynamic.Interface, namespace, workloadName string) (interface{}, error) {
	// Fetch the workload details
	workload, err := dynamicClient.Resource(WorkloadsGVR()).Namespace(namespace).Get(context.TODO(), workloadName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("error fetching workload %s: %v", workloadName, err)
	}

	// Add preemption details if available
	preempted := false
	if status, ok := workload.Object["status"].(map[string]interface{}); ok {
		preempted, _ = status["preempted"].(bool)
	}
	preemptionReason := "None"
	if reason, ok := workload.Object["status"].(map[string]interface{})["preemptionReason"].(string); ok {
		preemptionReason = reason
	}
	workload.Object["preemption"] = map[string]interface{}{
		"preempted": preempted,
		"reason":    preemptionReason,
	}

	// Get the local queue name from workload's spec
	localQueueName := ""
	if spec, ok := workload.Object["spec"].(map[string]interface{}); ok {
		localQueueName, _ = spec["queueName"].(string)
	}

	// If local queue name is found, fetch the local queue and its cluster queue
	if localQueueName != "" {
		localQueue, err := dynamicClient.Resource(LocalQueuesGVR()).Namespace(namespace).Get(context.TODO(), localQueueName, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("error fetching local queue %s: %v", localQueueName, err)
		}
		// Retrieve the targeted cluster queue name from the local queue's spec
		if spec, ok := localQueue.Object["spec"].(map[string]interface{}); ok {
			clusterQueueName, _ := spec["clusterQueue"].(string)
			workload.Object["clusterQueueName"] = clusterQueueName
		} else {
			workload.Object["clusterQueueName"] = "Unknown"
		}
	} else {
		workload.Object["clusterQueueName"] = "Unknown"
	}

	return workload, nil
}

func WorkloadEventsWebSocketHandler(dynamicClient dynamic.Interface) gin.HandlerFunc {
	return func(c *gin.Context) {
		namespace := c.Param("namespace")
		workloadName := c.Param("workload_name")

		GenericWebSocketHandler(func() (interface{}, error) {
			return fetchWorkloadEvents(dynamicClient, namespace, workloadName)
		})(c)
	}
}

func fetchWorkloadEvents(dynamicClient dynamic.Interface, namespace, workloadName string) (interface{}, error) {
	result, err := dynamicClient.Resource(EventsGVR()).Namespace(namespace).List(context.TODO(), metav1.ListOptions{
		FieldSelector: fmt.Sprintf("involvedObject.name=%s", workloadName),
	})
	if err != nil {
		return nil, fmt.Errorf("error fetching events for workload %s: %v", workloadName, err)
	}

	var events []map[string]interface{}
	for _, item := range result.Items {
		events = append(events, item.Object)
	}
	return events, nil
}
