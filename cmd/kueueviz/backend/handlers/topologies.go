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
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"

	kueueapi "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

// TopologiesWebSocketHandler streams all topologies
func (h *Handlers) TopologiesWebSocketHandler() gin.HandlerFunc {
	return h.GenericWebSocketHandler(func(ctx context.Context) (any, error) {
		return h.fetchTopologies(ctx)
	})
}

// TopologyDetailsWebSocketHandler streams details for a specific topology
func (h *Handlers) TopologyDetailsWebSocketHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		topologyName := c.Param("topology_name")
		h.GenericWebSocketHandler(func(ctx context.Context) (any, error) {
			return h.fetchTopologyDetails(ctx, topologyName)
		})(c)
	}
}

func (h *Handlers) fetchTopologies(ctx context.Context) (any, error) {
	tl := &kueueapi.TopologyList{}
	if err := h.client.List(ctx, tl); err != nil {
		return nil, fmt.Errorf("error fetching topologies: %v", err)
	}

	rfl := &kueueapi.ResourceFlavorList{}
	if err := h.client.List(ctx, rfl); err != nil {
		return nil, fmt.Errorf("error fetching resource flavors: %v", err)
	}

	topologyFlavors := make(map[string][]string)
	for _, rf := range rfl.Items {
		if rf.Spec.TopologyName != nil {
			name := string(*rf.Spec.TopologyName)
			topologyFlavors[name] = append(topologyFlavors[name], rf.Name)
		}
	}

	result := make([]map[string]any, 0, len(tl.Items))
	for _, t := range tl.Items {
		flavors := topologyFlavors[t.Name]
		if flavors == nil {
			flavors = []string{}
		}
		result = append(result, map[string]any{
			"name":            t.Name,
			"levelCount":      len(t.Spec.Levels),
			"levels":          t.Spec.Levels,
			"resourceFlavors": flavors,
		})
	}

	return result, nil
}

func (h *Handlers) fetchTopologyDetails(ctx context.Context, topologyName string) (map[string]any, error) {
	topology := &kueueapi.Topology{}
	if err := h.client.Get(ctx, ctrlclient.ObjectKey{Name: topologyName}, topology); err != nil {
		return nil, fmt.Errorf("error fetching topology %s: %v", topologyName, err)
	}

	rfl := &kueueapi.ResourceFlavorList{}
	if err := h.client.List(ctx, rfl); err != nil {
		return nil, fmt.Errorf("error fetching resource flavors: %v", err)
	}

	var referencingFlavors []map[string]any
	var matchingFlavors []kueueapi.ResourceFlavor
	for _, rf := range rfl.Items {
		if rf.Spec.TopologyName != nil && string(*rf.Spec.TopologyName) == topologyName {
			referencingFlavors = append(referencingFlavors, map[string]any{
				"name": rf.Name,
			})
			matchingFlavors = append(matchingFlavors, rf)
		}
	}
	if referencingFlavors == nil {
		referencingFlavors = []map[string]any{}
	}

	levelKeys := make([]string, 0, len(topology.Spec.Levels))
	for _, level := range topology.Spec.Levels {
		levelKeys = append(levelKeys, level.NodeLabel)
	}

	domainTree, err := h.buildDomainTree(ctx, topologyName, levelKeys, matchingFlavors)
	if err != nil {
		return nil, err
	}

	return map[string]any{
		"name":            topologyName,
		"levels":          topology.Spec.Levels,
		"levelKeys":       levelKeys,
		"resourceFlavors": referencingFlavors,
		"domainTree":      domainTree,
	}, nil
}

// buildDomainTree constructs a nested tree from live nodes grouped by topology level labels.
func (h *Handlers) buildDomainTree(ctx context.Context, topologyName string, levelKeys []string, flavors []kueueapi.ResourceFlavor) (map[string]any, error) {
	if len(levelKeys) == 0 || len(flavors) == 0 {
		return nil, nil
	}

	nl := &corev1.NodeList{}
	if err := h.client.List(ctx, nl); err != nil {
		return nil, fmt.Errorf("error fetching nodes: %v", err)
	}

	// Collect nodes that match any referencing flavor and have all level labels
	seen := make(map[string]bool)
	var entries []nodeEntry

	for _, flavor := range flavors {
		for _, node := range nl.Items {
			if seen[node.Name] {
				continue
			}
			if !hasMatchingLabels(node.Labels, flavor.Spec.NodeLabels) {
				continue
			}
			values := make([]string, 0, len(levelKeys))
			valid := true
			for _, key := range levelKeys {
				v, ok := node.Labels[key]
				if !ok {
					valid = false
					break
				}
				values = append(values, v)
			}
			if !valid {
				continue
			}
			seen[node.Name] = true
			entries = append(entries, nodeEntry{name: node.Name, levelValues: values})
		}
	}

	if len(entries) == 0 {
		return nil, nil
	}

	// Build nested tree: root → level0 domains → level1 domains → ... → leaf nodes
	root := map[string]any{
		"name":  topologyName,
		"level": "root",
	}
	insertNode(root, entries, levelKeys, 0)
	return root, nil
}

// insertNode recursively groups nodeEntries by level values and builds children.
func insertNode(parent map[string]any, entries []nodeEntry, levelKeys []string, depth int) {
	if depth >= len(levelKeys) {
		return
	}

	groups := make(map[string][]nodeEntry)
	var order []string
	for _, e := range entries {
		key := e.levelValues[depth]
		if _, exists := groups[key]; !exists {
			order = append(order, key)
		}
		groups[key] = append(groups[key], e)
	}

	isLeafLevel := depth == len(levelKeys)-1
	var children []map[string]any

	for _, key := range order {
		group := groups[key]
		child := map[string]any{
			"name":  key,
			"level": levelKeys[depth],
		}
		if isLeafLevel {
			var leafChildren []map[string]any
			for _, e := range group {
				leafChildren = append(leafChildren, map[string]any{
					"name":  e.name,
					"level": levelKeys[depth],
					"value": 1,
				})
			}
			child["children"] = leafChildren
		} else {
			insertNode(child, group, levelKeys, depth+1)
		}
		children = append(children, child)
	}

	parent["children"] = children
}

type nodeEntry struct {
	name        string
	levelValues []string
}
