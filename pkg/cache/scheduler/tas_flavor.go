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

package scheduler

import (
	"slices"
	"strings"
	"sync"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/resources"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	"sigs.k8s.io/kueue/pkg/workload"
)

// usageOp indicates whether we should add or subtract the usage.
type usageOp int

const (
	// add usage to the cache
	add usageOp = iota
	// subtract usage from the cache
	subtract
)

func (u usageOp) asSignedOne() int {
	if u == add {
		return 1
	}
	return -1
}

type flavorInformation struct {
	// Name indicates the name of the topology specified in the
	// ResourceFlavor spec.topologyName field.
	TopologyName kueue.TopologyReference

	// nodeLabels is a map of nodeLabels defined in the ResourceFlavor object.
	NodeLabels map[string]string
	// tolerations represents the list of tolerations specified for the resource
	// flavor
	Tolerations []corev1.Toleration
}

type topologyInformation struct {
	// levels is a list of levels defined in the Topology object referenced
	// by the flavor corresponding to the cache.
	Levels []string
}

type TASFlavorCache struct {
	sync.RWMutex

	client client.Client

	// topology represents the part of the Topology specification, e.g. the list
	// of topology levels, relevant for TAS-scheduling.
	topology topologyInformation

	// flavor represents the part of the ResourceFlavor specification, e.g. the
	// list of node labels and tolerations, relevant for TAS-scheduling.
	flavor flavorInformation

	// usage maintains the usage per topology domain
	usage map[utiltas.TopologyDomainID]resources.Requests

	// wlUsage tracks the usage coming from workloads, so that we can make the
	// usage removal indempotent - skip if it was not added.
	wlUsage map[workload.Reference][]workload.TopologyDomainRequests

	// nonTasUsageCache maintains the usage coming from non-TAS pods,
	// e.g. static Pods or DaemonSet pods.
	nonTasUsageCache *nonTasUsageCache

	// nodesCache is used to resolve hostname-only domain IDs to full-path domain
	// IDs when reporting multi-level metrics for topologies where the lowest level
	// is kubernetes.io/hostname.
	nodesCache *nodesCache
}

func (t *tasCache) NewTASFlavorCache(topologyInfo topologyInformation,
	flavorInfo flavorInformation) *TASFlavorCache {
	return &TASFlavorCache{
		client:           t.client,
		topology:         topologyInfo,
		flavor:           flavorInfo,
		usage:            make(map[utiltas.TopologyDomainID]resources.Requests),
		wlUsage:          make(map[workload.Reference][]workload.TopologyDomainRequests),
		nonTasUsageCache: t.nonTasUsageCache,
		nodesCache:       t.nodesCache,
	}
}

func (c *TASFlavorCache) NodeLabels() map[string]string {
	return c.flavor.NodeLabels
}

func (c *TASFlavorCache) Topology() kueue.TopologyReference {
	return c.flavor.TopologyName
}

func (c *TASFlavorCache) TopologyLevels() []string {
	return c.topology.Levels
}

func (c *TASFlavorCache) snapshot(log logr.Logger, nodes []*nodeInfo) *TASFlavorSnapshot {
	c.RLock()
	defer c.RUnlock()
	log.V(3).Info("Constructing TAS snapshot", "nodeLabels", c.flavor.NodeLabels,
		"levels", c.topology.Levels, "nodeCount", len(nodes))
	snapshot := newTASFlavorSnapshot(log, c.flavor.TopologyName, c.topology.Levels, c.flavor.Tolerations)
	nodeToDomain := make(map[string]utiltas.TopologyDomainID)
	for _, node := range nodes {
		nodeToDomain[node.Name] = snapshot.addNode(node)
	}
	snapshot.initialize()
	for domainID, usage := range c.usage {
		snapshot.addTASUsage(domainID, usage)
	}
	for nodeName, usage := range c.nonTasUsageCache.usagePerNode() {
		if domainID, ok := nodeToDomain[nodeName]; ok {
			snapshot.addNonTASUsage(domainID, usage)
		}
	}
	return snapshot
}

func (c *TASFlavorCache) addUsage(key workload.Reference, topologyRequests []workload.TopologyDomainRequests) {
	c.wlUsage[key] = slices.Clone(topologyRequests)
	c.updateUsage(topologyRequests, add)
}

func (c *TASFlavorCache) removeUsage(key workload.Reference) {
	value, found := c.wlUsage[key]
	if !found {
		return
	}
	c.updateUsage(value, subtract)
	delete(c.wlUsage, key)
}

func (c *TASFlavorCache) updateUsage(topologyRequests []workload.TopologyDomainRequests, op usageOp) {
	c.Lock()
	defer c.Unlock()
	for _, tr := range topologyRequests {
		domainID := utiltas.DomainID(tr.Values)
		_, found := c.usage[domainID]
		if !found {
			c.usage[domainID] = resources.Requests{}
		}
		if op == subtract {
			c.usage[domainID].Sub(tr.TotalRequests())
			c.usage[domainID].Sub(resources.Requests{corev1.ResourcePods: int64(tr.Count)})
		} else {
			c.usage[domainID].Add(tr.TotalRequests())
			c.usage[domainID].Add(resources.Requests{corev1.ResourcePods: int64(tr.Count)})
		}
	}
}

// reportDomainMetrics clears and re-reports kueue_tas_domain_usage for every
// topology level.  For each level the leaf-domain usage values are aggregated
// into that level's domains so operators can observe both host-level and
// rack/block-level totals.
func (c *TASFlavorCache) reportDomainMetrics(flavorName kueue.ResourceFlavorReference) {
	if len(c.topology.Levels) == 0 {
		return
	}
	c.RLock()
	defer c.RUnlock()
	metrics.ClearTASDomainUsageForFlavor(string(flavorName))
	// When the lowest topology level is kubernetes.io/hostname, buildAssignment
	// stores only the hostname value in the workload's TopologyAssignment, so
	// the usage map keys are hostname-only (e.g. "host-1" instead of
	// "rack-1,host-1").  Resolve them to full-path IDs before aggregating.
	fullPathUsage := c.resolveFullPathUsage()
	for levelIdx, levelKey := range c.topology.Levels {
		levelUsage := make(map[utiltas.TopologyDomainID]resources.Requests)
		for fullPathID, requests := range fullPathUsage {
			levelDomainID := domainIDPrefix(fullPathID, levelIdx+1)
			if _, ok := levelUsage[levelDomainID]; !ok {
				levelUsage[levelDomainID] = requests.Clone()
			} else {
				levelUsage[levelDomainID].Add(requests)
			}
		}
		for domainID, reqs := range levelUsage {
			for resourceName, quantity := range reqs {
				if resourceName == corev1.ResourcePods {
					continue
				}
				metrics.ReportTASDomainUsage(string(flavorName), levelKey, string(domainID), string(resourceName), resourceFloat(resourceName, quantity))
			}
		}
	}
}

// resolveFullPathUsage returns a merged usage map keyed by full-path domain IDs,
// combining TAS workload usage with non-TAS pod usage (DaemonSets, static pods).
// For topologies where buildAssignment stores only the hostname, the keys in
// c.usage are hostname-only; buildNodeToFullPath resolves them to the full path.
// Non-TAS usage is keyed by node name and is only included for nodes that match
// this flavor's node labels (mirroring how snapshot.go filters non-TAS usage).
func (c *TASFlavorCache) resolveFullPathUsage() map[utiltas.TopologyDomainID]resources.Requests {
	nodeToFullPath := c.buildNodeToFullPath()
	result := make(map[utiltas.TopologyDomainID]resources.Requests, len(c.usage))

	for domainID, requests := range c.usage {
		fullPathID := domainID
		if len(strings.Split(string(domainID), ",")) < len(c.topology.Levels) {
			if full, ok := nodeToFullPath[string(domainID)]; ok {
				fullPathID = full
			}
		}
		if existing, ok := result[fullPathID]; ok {
			existing.Add(requests)
		} else {
			result[fullPathID] = requests.Clone()
		}
	}

	for nodeName, requests := range c.nonTasUsageCache.usagePerNode() {
		if fullPathID, ok := nodeToFullPath[nodeName]; ok {
			if existing, ok := result[fullPathID]; ok {
				existing.Add(requests)
			} else {
				result[fullPathID] = requests.Clone()
			}
		}
	}

	return result
}

// buildNodeToFullPath queries the nodesCache for nodes matching this flavor and
// returns a map from node identifier to the full comma-joined topology path.
// Both the node name (used by nonTasUsageCache) and the kubernetes.io/hostname
// label value (used as the domain ID key in c.usage) are stored as keys.
func (c *TASFlavorCache) buildNodeToFullPath() map[string]utiltas.TopologyDomainID {
	if c.nodesCache == nil {
		return nil
	}
	nodes := c.nodesCache.find(c.flavor.NodeLabels, c.topology.Levels)
	mapping := make(map[string]utiltas.TopologyDomainID, len(nodes)*2)
	for _, node := range nodes {
		levelValues := make([]string, len(c.topology.Levels))
		for i, level := range c.topology.Levels {
			levelValues[i] = node.Labels[level]
		}
		fullPathID := utiltas.DomainID(levelValues)
		mapping[node.Name] = fullPathID
		if hostname := node.Labels[corev1.LabelHostname]; hostname != "" {
			mapping[hostname] = fullPathID
		}
	}
	return mapping
}

// domainIDPrefix returns the first n comma-separated components of a domain ID.
// For example, domainIDPrefix("rack-1,host-2", 1) returns "rack-1".
func domainIDPrefix(domainID utiltas.TopologyDomainID, n int) utiltas.TopologyDomainID {
	parts := strings.SplitN(string(domainID), ",", n+1)
	if len(parts) <= n {
		return domainID
	}
	return utiltas.TopologyDomainID(strings.Join(parts[:n], ","))
}

type nodeInfo struct {
	// Name holds the node's name, used to evaluate node affinity.
	Name string

	// Labels are used to match Topology levels and NodeSelectors.
	Labels map[string]string

	// Taints are used to check tolerations.
	Taints []corev1.Taint

	// Allocatable capacity from Status.Allocatable.
	Allocatable corev1.ResourceList
}

func newNodeInfo(node *corev1.Node) *nodeInfo {
	return &nodeInfo{
		Name:        node.Name,
		Labels:      node.Labels,
		Taints:      node.Spec.Taints,
		Allocatable: node.Status.Allocatable,
	}
}

func (ni *nodeInfo) toNode() *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   ni.Name,
			Labels: ni.Labels,
		},
		Spec: corev1.NodeSpec{
			Taints: ni.Taints,
		},
		Status: corev1.NodeStatus{
			Allocatable: ni.Allocatable,
		},
	}
}
