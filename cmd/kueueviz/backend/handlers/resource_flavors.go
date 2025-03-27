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

	"github.com/gin-gonic/gin"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
)

// ResourceFlavorsWebSocketHandler streams all resource flavors
func ResourceFlavorsWebSocketHandler(dynamicClient dynamic.Interface) gin.HandlerFunc {

	return GenericWebSocketHandler(func() (interface{}, error) {
		return fetchResourceFlavors(dynamicClient)
	})
}

// ResourceFlavorDetailsWebSocketHandler streams details for a specific resource flavor
func ResourceFlavorDetailsWebSocketHandler(dynamicClient dynamic.Interface) gin.HandlerFunc {
	return func(c *gin.Context) {
		flavorName := c.Param("flavor_name")
		GenericWebSocketHandler(func() (interface{}, error) {
			return fetchResourceFlavorDetails(dynamicClient, flavorName)
		})(c)
	}
}

// Fetch all resource flavors
func fetchResourceFlavors(dynamicClient dynamic.Interface) (interface{}, error) {
	result, err := dynamicClient.Resource(ResourceFlavorsGVR()).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("error fetching resource flavors: %v", err)
	}

	var flavors []map[string]interface{}
	for _, item := range result.Items {
		object := item.Object
		object["name"] = item.GetName()
		spec, _ := item.Object["spec"].(map[string]interface{})
		object["details"] = spec

		flavors = append(flavors, object)
	}
	return flavors, nil
}

// Fetch details for a specific Resource Flavor
func fetchResourceFlavorDetails(dynamicClient dynamic.Interface, flavorName string) (map[string]interface{}, error) {
	// Fetch the specified resource flavor details
	flavor, err := dynamicClient.Resource(ResourceFlavorsGVR()).Get(context.TODO(), flavorName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("error fetching resource flavor %s: %v", flavorName, err)
	}

	// List all cluster queues
	clusterQueues, err := dynamicClient.Resource(ClusterQueuesGVR()).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("error listing cluster queues: %v", err)
	}

	queuesUsingFlavor := []map[string]interface{}{}

	// Iterate through each cluster queue to find queues using the specified flavor
	for _, item := range clusterQueues.Items {
		queueName, _, _ := unstructured.NestedString(item.Object, "metadata", "name")
		resourceGroups, _, _ := unstructured.NestedSlice(item.Object, "spec", "resourceGroups")

		for _, group := range resourceGroups {
			groupMap, ok := group.(map[string]interface{})
			if !ok {
				continue
			}
			flavors, _, _ := unstructured.NestedSlice(groupMap, "flavors")

			for _, fl := range flavors {
				flavorMap, ok := fl.(map[string]interface{})
				if !ok {
					continue
				}
				name, _, _ := unstructured.NestedString(flavorMap, "name")
				if name == flavorName {
					// Collect resource and quota information
					quotaInfo := []map[string]interface{}{}
					resources, _, _ := unstructured.NestedSlice(flavorMap, "resources")

					for _, res := range resources {
						resMap, ok := res.(map[string]interface{})
						if !ok {
							continue
						}
						resourceName, _, _ := unstructured.NestedString(resMap, "name")
						nominalQuota, _, _ := unstructured.NestedString(resMap, "nominalQuota")

						quotaInfo = append(quotaInfo, map[string]interface{}{
							"resource":     resourceName,
							"nominalQuota": nominalQuota,
						})
					}

					queuesUsingFlavor = append(queuesUsingFlavor, map[string]interface{}{
						"queueName": queueName,
						"quota":     quotaInfo,
					})
					//log.Println(queuesUsingFlavor)
					break // Stop searching this queue once the flavor is found
				}
			}
		}
	}

	// Retrieve matching nodes for the flavor (assumes getNodesForFlavor is implemented)
	matchingNodes, _ := getNodesForFlavor(dynamicClient, flavorName)
	log.Println(matchingNodes)

	details := map[string]interface{}{
		"tolerations": []map[string]interface{}{},
		"taints":      []map[string]interface{}{},
	}
	if spec, exists := flavor.Object["spec"]; exists {
		details = spec.(map[string]interface{})
	}
	tolerations, found, err := unstructured.NestedSlice(flavor.Object, "spec", "tolerations")
	if err == nil && found {
		details["tolerations"] = tolerations
	}
	details["taints"] = queuesUsingFlavor

	// Construct the result
	result := map[string]interface{}{
		"name":    flavorName,
		"details": details,
		"queues":  queuesUsingFlavor,
		"nodes":   matchingNodes,
	}

	return result, nil
}

// getNodesForFlavor retrieves a list of nodes that match a specific resource flavor.
func getNodesForFlavor(dynamicClient dynamic.Interface, flavorName string) ([]map[string]interface{}, error) {

	flavor, err := dynamicClient.Resource(ResourceFlavorsGVR()).Get(context.TODO(), flavorName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("error fetching resource flavor %s: %v", flavorName, err)
	}

	// List all nodes
	nodeList, err := dynamicClient.Resource(NodesGVR()).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("error fetching nodes: %v", err)
	}

	var matchingNodes []map[string]interface{}
	nodeLabels, _, err := unstructured.NestedMap(flavor.Object, "spec", "nodeLabels")
	if err != nil {
		return nil, fmt.Errorf("error reading nodeLabels for flavor %s: %v", flavorName, err)
	}

	// Iterate through each node to find matches for the flavor's nodeLabels
	for _, node := range nodeList.Items {
		nodeName := node.GetName()
		// Convert the unstructured node object to the corev1.Node type
		nodeObj := &v1.Node{}
		err := runtime.DefaultUnstructuredConverter.FromUnstructured(node.Object, nodeObj)
		if err != nil {
			log.Printf("Error converting node %s to corev1.Node: %v", nodeName, err)
			continue
		}
		// Check if the node has all the labels specified in the flavor
		if hasMatchingLabels(nodeObj.Labels, nodeLabels) {
			taints := []map[string]interface{}{}
			for _, taint := range nodeObj.Spec.Taints {
				taints = append(taints, map[string]interface{}{
					"key":    taint.Key,
					"value":  taint.Value,
					"effect": string(taint.Effect),
				})
			}
			matchingNodes = append(matchingNodes, map[string]interface{}{
				"name":        nodeName,
				"labels":      nodeObj.Labels,
				"taints":      taints,
				"tolerations": []v1.Taint{},
			})
		}
	}
	return matchingNodes, nil
}

// hasMatchingLabels checks if a node's labels contain all the labels specified in nodeLabels.
func hasMatchingLabels(nodeLabels map[string]string, flavorLabels map[string]interface{}) bool {
	for key, value := range flavorLabels {
		strValue, ok := value.(string)
		if !ok || nodeLabels[key] != strValue {
			return false
		}
	}
	return true
}
