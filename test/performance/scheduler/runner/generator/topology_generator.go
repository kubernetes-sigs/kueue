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

package generator

import (
	"context"
	"errors"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	testutil "sigs.k8s.io/kueue/test/util"
)

const (
	tasNodeGroupLabel = "tas-node-group"
)

// TopologyConfig represents the topology configuration from YAML
type TopologyConfig struct {
	Name   string          `json:"name"`
	Levels []TopologyLevel `json:"levels"`
}

// TopologyLevel represents a single level in the topology hierarchy
type TopologyLevel struct {
	Name      string `json:"name"`
	Count     int    `json:"count"`
	NodeLabel string `json:"nodeLabel"`
	Capacity  struct {
		CPU    string `json:"cpu"`
		Memory string `json:"memory"`
	} `json:"capacity,omitempty"`
}

// ResourceFlavorConfig represents the resource flavor configuration
type ResourceFlavorConfig struct {
	Name         string `json:"name"`
	NodeLabel    string `json:"nodeLabel,omitempty"`
	TopologyName string `json:"topologyName,omitempty"`
}

// generateTopologyNodes creates nodes with hierarchical topology labels
func generateTopologyNodes(ctx context.Context, c client.Client, config TopologyConfig) error {
	log := ctrl.LoggerFrom(ctx).WithName("generate topology nodes")
	log.Info("Start topology node generation", "config", config.Name)
	defer log.Info("End topology node generation")

	// Validate that we have at least 2 levels (we need rack and node at minimum)
	if len(config.Levels) < 2 {
		return fmt.Errorf("topology must have at least 2 levels, got %d", len(config.Levels))
	}

	// Find the node level (last level with capacity)
	nodeLevel := config.Levels[len(config.Levels)-1]
	if nodeLevel.Capacity.CPU == "" {
		nodeLevel.Capacity.CPU = "96"
	}
	if nodeLevel.Capacity.Memory == "" {
		nodeLevel.Capacity.Memory = "256Gi"
	}

	// Calculate total nodes
	totalNodes := 1
	for _, level := range config.Levels {
		totalNodes *= level.Count
	}
	log.Info("Generating nodes", "totalNodes", totalNodes)

	// Generate nodes with hierarchical labels
	nodes := make([]corev1.Node, 0, totalNodes)

	// Generate nodes recursively based on hierarchy
	generateNodesRecursive(config.Levels, 0, []string{}, nodeLevel.Capacity.CPU, nodeLevel.Capacity.Memory, &nodes)

	// Create nodes with status Ready
	log.Info("Updating node status to Ready")
	testutil.CreateNodesWithStatus(ctx, c, nodes)

	log.Info("Successfully generated nodes", "count", len(nodes))
	return nil
}

// generateNodesRecursive generates nodes recursively based on topology hierarchy
func generateNodesRecursive(levels []TopologyLevel, currentLevelIdx int, labelValues []string, cpu, memory string, nodes *[]corev1.Node) {
	if len(levels) == currentLevelIdx {
		// We've reached the leaf level - create a node
		nodeName := buildNodeName(labelValues)

		capacityList := corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse(cpu),
			corev1.ResourceMemory: resource.MustParse(memory),
			corev1.ResourcePods:   resource.MustParse("110"),
		}

		node := testingnode.MakeNode(nodeName).
			Label(CleanupLabel, "true").
			Label(tasNodeGroupLabel, "tas").
			StatusAllocatable(capacityList).
			Ready()

		// Add all topology labels
		for i := range labelValues {
			// labelValues[i] is in format "label=value"
			// We need to split and apply
			node = node.Label(levels[i].NodeLabel, labelValues[i])
		}

		*nodes = append(*nodes, *node.Obj())
		return
	}

	for i := range levels[currentLevelIdx].Count {
		labelValue := fmt.Sprintf("%s-%d", levels[currentLevelIdx].Name, i)
		newLabelValues := append(labelValues, labelValue)
		generateNodesRecursive(levels, currentLevelIdx+1, newLabelValues, cpu, memory, nodes)
	}
}

// buildNodeName creates a node name from label values
func buildNodeName(labelValues []string) string {
	if len(labelValues) == 0 {
		return "node"
	}

	// For labels like ["block-0", "rack-2", "node-15"]
	// Create name like "node-b0-r2-n15"
	var nameBuilder strings.Builder
	nameBuilder.WriteString("node")
	for _, val := range labelValues {
		// Extract number from "prefix-number" format
		nameBuilder.WriteString("-")
		nameBuilder.WriteByte(val[0])
		nameBuilder.WriteString(strings.Split(val, "-")[1])
	}
	return nameBuilder.String()
}

// generateTopologyResource creates the Topology CR
func generateTopologyResource(ctx context.Context, c client.Client, config TopologyConfig) error {
	log := ctrl.LoggerFrom(ctx).WithName("generate topology resource")
	log.Info("Start topology resource generation", "name", config.Name)
	defer log.Info("End topology resource generation")

	// Build topology levels
	topologyLevels := make([]string, len(config.Levels))
	for i, level := range config.Levels {
		topologyLevels[i] = level.NodeLabel
	}

	// Create topology resource
	topology := utiltestingapi.MakeTopology(config.Name).
		Levels(topologyLevels...).
		Label(CleanupLabel, "true").Obj()

	if err := c.Create(ctx, topology); err != nil {
		return fmt.Errorf("creating topology: %w", err)
	}

	log.Info("Successfully created topology resource")
	return nil
}

// generateResourceFlavor creates the ResourceFlavor CR linked to topology
func generateResourceFlavor(ctx context.Context, c client.Client, flavorConfig ResourceFlavorConfig) error {
	log := ctrl.LoggerFrom(ctx).WithName("generate resource flavor")
	log.Info("Start resource flavor generation", "name", flavorConfig.Name)
	defer log.Info("End resource flavor generation")

	rfBuilder := utiltestingapi.MakeResourceFlavor(flavorConfig.Name).Label(CleanupLabel, "true")
	if flavorConfig.TopologyName != "" {
		if flavorConfig.NodeLabel == "" {
			return errors.New("resourceFlavor.nodeLabel is required when TAS is enabled")
		}
		rfBuilder = rfBuilder.
			TopologyName(flavorConfig.TopologyName).
			NodeLabel(flavorConfig.NodeLabel, "tas")
	}

	rf := rfBuilder.Obj()
	if err := c.Create(ctx, rf); err != nil {
		return fmt.Errorf("creating resource flavor: %w", err)
	}

	log.Info("Successfully created resource flavor")
	return nil
}
