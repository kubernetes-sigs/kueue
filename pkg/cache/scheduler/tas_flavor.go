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
	"context"
	"fmt"
	"maps"
	"slices"
	"sync"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/tas/indexer"
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
	}
}

func (c *TASFlavorCache) snapshot(ctx context.Context) (*TASFlavorSnapshot, error) {
	log := ctrl.LoggerFrom(ctx)
	nodes := &corev1.NodeList{}

	var requiredLabels client.MatchingLabels = maps.Clone(c.flavor.NodeLabels)
	var requiredLabelKeys client.HasLabels = slices.Clone(c.topology.Levels)

	err := c.client.List(ctx, nodes, requiredLabels, requiredLabelKeys, client.MatchingFields{
		indexer.ReadyNode:       "true",
		indexer.SchedulableNode: "true",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list nodes for TAS: %w", err)
	}
	return c.snapshotForNodes(log, nodes.Items), nil
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

func (c *TASFlavorCache) snapshotForNodes(log logr.Logger, nodes []corev1.Node) *TASFlavorSnapshot {
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
