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

package cache

import (
	"context"
	"sync"

	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/kueue/pkg/resources"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	"sigs.k8s.io/kueue/pkg/workload"
)

// usageOp indicates whether we should add or subtract the usage.
type usageOp bool

const (
	// add usage to the cache
	add usageOp = true
	// subtract usage from the cache
	subtract usageOp = false
)

type TASFlavorCache struct {
	sync.RWMutex

	client client.Client

	// nodeLabels is a map of nodeLabels defined in the ResourceFlavor object.
	NodeLabels map[string]string
	// levels is a list of levels defined in the Topology object referenced
	// by the flavor corresponding to the cache.
	Levels []string

	// usageMap maintains the usage per topology domain
	usageMap map[utiltas.TopologyDomainID]resources.Requests
}

func (c *TASFlavorCache) snapshot(ctx context.Context) *TASFlavorSnapshot {
	log := ctrl.LoggerFrom(ctx)
	nodeList := &corev1.NodeList{}
	matcher := client.MatchingLabels{}
	for k, v := range c.NodeLabels {
		matcher[k] = v
	}
	err := c.client.List(ctx, nodeList, matcher)
	if err != nil {
		log.Error(err, "failed to list nodes for TAS", "nodeLabels", c.NodeLabels)
	}
	log.V(3).Info("Constructing TAS snapshot", "nodeLabels", c.NodeLabels,
		"levels", c.Levels, "nodeCount", len(nodeList.Items))
	return c.snapshotForNodes(nodeList.Items)
}

func (c *TASFlavorCache) snapshotForNodes(nodes []corev1.Node) *TASFlavorSnapshot {
	c.RLock()
	defer c.RUnlock()
	snapshot := newTASFlavorSnapshot(c.Levels)
	for _, node := range nodes {
		levelValues := utiltas.LevelValues(c.Levels, node.Labels)
		capacity := resources.NewRequests(node.Status.Allocatable)
		domainID := utiltas.DomainID(levelValues)
		snapshot.levelValuesPerDomain[domainID] = levelValues
		snapshot.addCapacity(domainID, capacity)
	}
	snapshot.initialize()
	for domainID, usage := range c.usageMap {
		snapshot.addUsage(domainID, usage)
	}
	return snapshot
}

func (c *TASFlavorCache) addUsage(topologyRequests []workload.TopologyDomainRequests) {
	c.updateUsage(topologyRequests, add)
}

func (c *TASFlavorCache) removeUsage(topologyRequests []workload.TopologyDomainRequests) {
	c.updateUsage(topologyRequests, subtract)
}

func (c *TASFlavorCache) updateUsage(topologyRequests []workload.TopologyDomainRequests, op usageOp) {
	c.Lock()
	defer c.Unlock()
	for _, tr := range topologyRequests {
		domainID := utiltas.DomainID(tr.Values)
		_, found := c.usageMap[domainID]
		if !found {
			c.usageMap[domainID] = resources.Requests{}
		}
		if op == subtract {
			c.usageMap[domainID].Sub(tr.Requests)
		} else {
			c.usageMap[domainID].Add(tr.Requests)
		}
	}
}
