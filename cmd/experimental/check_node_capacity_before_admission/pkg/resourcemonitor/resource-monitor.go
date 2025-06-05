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

package resourcemonitor

import (
	"context"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
)

// As a first implementation we decided the following scheduling logic:
// 1. only consider Guaranteed pods: For every Container in the Pod, the resource limit must equal the resource request.
// 2. the above rule applies to both workloads and the logic for counting the available capacity of nodes
// 		2.1 i.e. if a Node has a running pod that states a resource limit for CPU but only a resource request for Memory, then the available capacity of such Node will remain untouched for both CPU and Memory by not considering the entire pod
// 3. If a workload does not fit the bill for bullet 1, the scheduler prints an info message and accepts it.
// 		3.1 i.e. if a Job has a resource request for 3 CPUs but doesn't specify the CPU resource limit, then it gets accepted by the scheduler (without comparing it with the snapshot) and Kueue will take care of it.
// 4. If a workload lists in its limits a resource that is not present in the limits of ANY node, the scheduler prints an info message and accepts it.

// PodInfo holds information about each pod running on a node
type PodInfo struct {
	Name      string              `json:"name"`
	UUID      string              `json:"uuid"`
	Namespace string              `json:"namespace"`
	Resources corev1.ResourceList `json:"resources"` // Tracks resource usage by this pod
}

// NodeUsage holds the resource usage data for a single node
type NodeUsage struct {
	NodeName  string              `json:"node_name"`
	UUID      string              `json:"uuid"` // Unique identifier for the node
	Taints    []corev1.Taint      `json:"taints"`
	Labels    map[string]string   `json:"labels"`    // Node labels for scheduling decisions
	Remaining corev1.ResourceList `json:"remaining"` // corev1.ResourceList tracking the currently remaining resources for incoming workloads
	Pods      []PodInfo           `json:"pods"`      // List of pods running on the node
}

// Snapshot holds resource data for all nodes and the last check timestamp
type Snapshot struct {
	LastCheck time.Time   `json:"last_check"` // Timestamp of the last resource check
	Nodes     []NodeUsage `json:"nodes"`
}

// SnapshotManager manages the snapshot and provides methods to access and update it
type SnapshotManager struct {
	snapshot Snapshot
	mu       sync.RWMutex
}

// NewSnapshotManager initializes a new SnapshotManager
func NewSnapshotManager() *SnapshotManager {
	return &SnapshotManager{
		snapshot: Snapshot{
			LastCheck: time.Now(),
			Nodes:     []NodeUsage{},
		},
	}
}

// UpdateSnapshot updates the current snapshot with resource usage information
func (sm *SnapshotManager) UpdateSnapshot(clientset *kubernetes.Clientset, namespace, nodeName string) {
	logger := ctrl.LoggerFrom(context.TODO())

	// Fetch all nodes from the Kubernetes cluster
	nodes, err := clientset.CoreV1().Nodes().List(context.Background(), metav1.ListOptions{})
	if err != nil {
		logger.Error(err, "Error listing nodes")
		return
	}

	// Map to hold pods running on each node
	nodePods := make(map[string][]PodInfo)

	// Fetch all pods from the specified namespace
	pods, err := clientset.CoreV1().Pods(namespace).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		logger.Info("Error listing pods in namespace", "namespace", namespace, "err", err)
		return
	}

	// Map to track used resources for each node
	nodeUsedResources := make(map[string]corev1.ResourceList)

	// Iterate through all pods to calculate their resource usage and group them by node
	for _, pod := range pods.Items {
		// Skip pods that have already succeeded or do not match the target node (if specified)
		if pod.Status.Phase != "Succeeded" && (nodeName == "" || pod.Spec.NodeName == nodeName) {
			// Aggregate resources for the pod
			podResourceUsage := corev1.ResourceList{}

			// Iterate over containers in the pod to calculate resource requests and limits
			validPod := true
			for _, container := range pod.Spec.Containers {
				for resourceName, limit := range container.Resources.Limits {
					// Check if the resource request equals the resource limit
					request, requestExists := container.Resources.Requests[resourceName]
					if !requestExists || !request.Equal(limit) {
						validPod = false // not a Guanranteed pod
						break
					}

					// Accumulate resource usage
					if existing, exists := podResourceUsage[resourceName]; exists {
						existing.Add(limit)
						podResourceUsage[resourceName] = existing
					} else {
						podResourceUsage[resourceName] = limit.DeepCopy()
					}
				}
				if !validPod {
					// Break if the container is invalid
					break
				}

				// // Additional check for requests without limits (if this is a requirement)
				// for resourceName := range container.Resources.Requests {
				// 	if _, exists := container.Resources.Limits[resourceName]; !exists {
				// 		validPod = false
				// 		log.Infof("Pod %s/%s container %s has a request but no limit for resource %s. Skipping pod.",
				// 			pod.Namespace, pod.Name, container.Name, resourceName)
				// 		break
				// 	}
				// }
				// if !validPod {
				// 	break
				// }
			}

			// Only include pods with valid resource limits for all resources
			if validPod {
				// Check if the node already has a record in the nodeUsedResources map
				if nodeUsage, exists := nodeUsedResources[pod.Spec.NodeName]; exists {
					// If the node already has recorded resource usage, iterate over the current pod's resource usage
					for resourceName, quantity := range podResourceUsage {
						// Check if the resource type (e.g., CPU, memory) is already tracked for this node
						if existing, exists := nodeUsage[resourceName]; exists {
							// If the resource type exists, add the pod's resource usage to the existing usage
							existing.Add(quantity)
							nodeUsage[resourceName] = existing // Update the resource usage for this resource
						} else {
							// If the resource type is not yet tracked, add it with the pod's usage
							nodeUsage[resourceName] = quantity.DeepCopy()
						}
					}
				} else {
					// If the node doesn't have any recorded usage yet, initialize it with the pod's resource usage
					nodeUsedResources[pod.Spec.NodeName] = podResourceUsage
				}
			}

			// Add pod information to the node's pod list
			nodePods[pod.Spec.NodeName] = append(nodePods[pod.Spec.NodeName], PodInfo{
				Name:      pod.Name,
				UUID:      string(pod.UID),
				Namespace: pod.Namespace,
				Resources: podResourceUsage,
			})
		}
	}

	// Initialize a new snapshot to capture the current state
	newSnapshot := Snapshot{
		LastCheck: time.Now(), // Record the current timestamp
	}

	// Iterate through all nodes to collect their resource data and associated pods
	for _, node := range nodes.Items {
		// Skip nodes if a specific nodeName is provided and this node does not match
		if nodeName == "" || node.Name == nodeName {
			// Get the allocatable resources for the node (= the total resource on a Node)
			allocatable := node.Status.Allocatable

			if len(allocatable) == 0 { // Ensuring that allocatable has a valid value
				continue
			}

			// Calculate remaining resources by subtracting used resources from allocatable resources
			remaining := corev1.ResourceList{}

			// Check if there are recorded resource usages for this node
			if used, exists := nodeUsedResources[node.Name]; exists {
				// Iterate through all allocatable resources for the node (e.g., CPU, memory, GPUs)
				for resourceName, allocQuantity := range allocatable {
					// Get the used quantity for the same resource type from the recorded usage
					usedQuantity := used[resourceName]

					// Check if the allocatable amount is greater than the used amount
					if allocQuantity.Cmp(usedQuantity) > 0 {
						// If true, calculate the remaining capacity by subtracting the used amount from the allocatable amount
						// Retrieve the value as a variable
						remainingQuantity := allocQuantity.DeepCopy()

						// Subtract the used quantity
						remainingQuantity.Sub(usedQuantity)

						// Store the modified value back in the map
						remaining[resourceName] = remainingQuantity
					}
					// If allocatable is not greater than used, do not include this resource in the remaining list
					// This ensures resources with zero or negative capacity are not added.
				}
			} else {
				// If no resource usage is recorded for this node, assume all allocatable resources are still available
				remaining = allocatable.DeepCopy()
			}

			// Add the node data, including resource limits, remaining capacity, and running pods, to the snapshot
			newSnapshot.Nodes = append(newSnapshot.Nodes, NodeUsage{
				NodeName:  node.Name,
				UUID:      string(node.UID),
				Taints:    node.Spec.Taints,
				Labels:    node.Labels,
				Remaining: remaining,
				Pods:      nodePods[node.Name],
			})
			// i think we should register the nodeselector list as key : value []
		}
	}

	// Lock the SnapshotManager to safely update the snapshot
	sm.mu.Lock()
	sm.snapshot = newSnapshot
	sm.mu.Unlock()
}

// GetSnapshot returns the current resource snapshot
func (sm *SnapshotManager) GetSnapshot() Snapshot {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.snapshot
}

// // formatTaints converts taints into a string format
// func formatTaints(taints []corev1.Taint) string {
// 	var taintStrings []string
// 	for _, taint := range taints {
// 		taintStrings = append(taintStrings, fmt.Sprintf("%s=%s:%s", taint.Key, taint.Value, taint.Effect))
// 	}
// 	return strings.Join(taintStrings, ", ")
// }
