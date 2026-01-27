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
	"k8s.io/api/core/v1"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"

	kueueapi "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

// ResourceFlavorsWebSocketHandler streams all resource flavors
func (h *Handlers) ResourceFlavorsWebSocketHandler() gin.HandlerFunc {
	return h.GenericWebSocketHandler(func(ctx context.Context) (any, error) {
		return h.fetchResourceFlavors(ctx)
	})
}

// ResourceFlavorDetailsWebSocketHandler streams details for a specific resource flavor
func (h *Handlers) ResourceFlavorDetailsWebSocketHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		flavorName := c.Param("flavor_name")
		h.GenericWebSocketHandler(func(ctx context.Context) (any, error) {
			return h.fetchResourceFlavorDetails(ctx, flavorName)
		})(c)
	}
}

// Fetch all resource flavors
func (h *Handlers) fetchResourceFlavors(ctx context.Context) (any, error) {
	rfl := &kueueapi.ResourceFlavorList{}
	err := h.client.List(ctx, rfl)
	if err != nil {
		return nil, fmt.Errorf("error fetching resource flavors: %v", err)
	}

	type rfResult struct {
		kueueapi.ResourceFlavor

		Name    string `json:"name"`
		Details any    `json:"details"`
	}

	var flavors []rfResult
	for _, item := range rfl.Items {
		flavors = append(flavors, rfResult{
			ResourceFlavor: item,
			Name:           item.GetName(),
			Details:        item.Spec,
		})
	}
	return flavors, nil
}

// Fetch details for a specific Resource Flavor
func (h *Handlers) fetchResourceFlavorDetails(ctx context.Context, flavorName string) (map[string]any, error) {
	// Fetch the specified resource flavor details
	flavor := &kueueapi.ResourceFlavor{}
	err := h.client.Get(ctx, ctrlclient.ObjectKey{Name: flavorName}, flavor)
	if err != nil {
		return nil, fmt.Errorf("error fetching resource flavor %s: %v", flavorName, err)
	}

	// List all cluster queues
	cql := &kueueapi.ClusterQueueList{}
	err = h.client.List(ctx, cql)
	if err != nil {
		return nil, fmt.Errorf("error listing cluster queues: %v", err)
	}

	queuesUsingFlavor := []map[string]any{}

	// Iterate through each cluster queue to find queues using the specified flavor
	for _, item := range cql.Items {
		queueName := item.GetName()
		resourceGroups := item.Spec.ResourceGroups

		for _, group := range resourceGroups {
			for _, fl := range group.Flavors {
				if string(fl.Name) == flavorName {
					// Collect resource and quota information
					quotaInfo := []map[string]any{}

					for _, res := range fl.Resources {
						resourceName := string(res.Name)
						nominalQuota := res.NominalQuota.String()

						quotaInfo = append(quotaInfo, map[string]any{
							"resource":     resourceName,
							"nominalQuota": nominalQuota,
						})
					}

					queuesUsingFlavor = append(queuesUsingFlavor, map[string]any{
						"queueName": queueName,
						"quota":     quotaInfo,
					})
					// log.Println(queuesUsingFlavor)
					break // Stop searching this queue once the flavor is found
				}
			}
		}
	}

	// Retrieve matching nodes for the flavor (assumes getNodesForFlavor is implemented)
	matchingNodes, _ := h.getNodesForFlavor(ctx, flavorName)

	details := map[string]any{
		"tolerations": flavor.Spec.Tolerations,
		"taints":      flavor.Spec.NodeTaints,
	}

	// Construct the result
	result := map[string]any{
		"name":    flavorName,
		"details": details,
		"queues":  queuesUsingFlavor,
		"nodes":   matchingNodes,
	}

	return result, nil
}

// getNodesForFlavor retrieves a list of nodes that match a specific resource flavor.
func (h *Handlers) getNodesForFlavor(ctx context.Context, flavorName string) ([]map[string]any, error) {
	rf := &kueueapi.ResourceFlavor{}
	err := h.client.Get(ctx, ctrlclient.ObjectKey{Name: flavorName}, rf)
	if err != nil {
		return nil, fmt.Errorf("error fetching resource flavor %s: %v", flavorName, err)
	}

	// List all nodes
	nl := &v1.NodeList{}
	err = h.client.List(ctx, nl)
	if err != nil {
		return nil, fmt.Errorf("error fetching nodes: %v", err)
	}

	var matchingNodes []map[string]any
	nodeLabels := rf.Spec.NodeLabels

	// Iterate through each node to find matches for the flavor's nodeLabels
	for _, node := range nl.Items {
		nodeName := node.GetName()
		// Check if the node has all the labels specified in the flavor
		if hasMatchingLabels(node.Labels, nodeLabels) {
			taints := []map[string]any{}
			for _, taint := range node.Spec.Taints {
				taints = append(taints, map[string]any{
					"key":    taint.Key,
					"value":  taint.Value,
					"effect": string(taint.Effect),
				})
			}
			matchingNodes = append(matchingNodes, map[string]any{
				"name":        nodeName,
				"labels":      node.Labels,
				"taints":      taints,
				"tolerations": []v1.Taint{},
			})
		}
	}
	return matchingNodes, nil
}

// hasMatchingLabels checks if a node's labels contain all the labels specified in nodeLabels.
func hasMatchingLabels(nodeLabels map[string]string, flavorLabels map[string]string) bool {
	for key, value := range flavorLabels {
		if nodeLabels[key] != value {
			return false
		}
	}
	return true
}
