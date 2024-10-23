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
	"maps"
	"slices"
	"sync"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

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

type TASFlavorCache struct {
	sync.RWMutex

	client client.Client

	// nodeLabels is a map of nodeLabels defined in the ResourceFlavor object.
	NodeLabels map[string]string
	// levels is a list of levels defined in the Topology object referenced
	// by the flavor corresponding to the cache.
	Levels []string

	// usage maintains the usage per topology domain
	usage map[utiltas.TopologyDomainID]resources.Requests
}

func (t *TASCache) NewTASFlavorCache(labels []string, nodeLabels map[string]string) *TASFlavorCache {
	return &TASFlavorCache{
		client:     t.client,
		Levels:     slices.Clone(labels),
		NodeLabels: maps.Clone(nodeLabels),
		usage:      make(map[utiltas.TopologyDomainID]resources.Requests),
	}
}

func (c *TASFlavorCache) snapshot(ctx context.Context) *TASFlavorSnapshot {
	log := ctrl.LoggerFrom(ctx)
	nodeList := &corev1.NodeList{}
	requiredLabels := client.MatchingLabels{}
	for k, v := range c.NodeLabels {
		requiredLabels[k] = v
	}
	requiredLabelKeys := client.HasLabels{}
	requiredLabelKeys = append(requiredLabelKeys, c.Levels...)
	err := c.client.List(ctx, nodeList, requiredLabels, requiredLabelKeys)
	if err != nil {
		log.Error(err, "failed to list nodes for TAS", "nodeLabels", c.NodeLabels)
	}
	return c.snapshotForNodes(log, nodeList.Items)
}

func (c *TASFlavorCache) snapshotForNodes(log logr.Logger, nodes []corev1.Node) *TASFlavorSnapshot {
	c.RLock()
	defer c.RUnlock()

	log.V(3).Info("Constructing TAS snapshot", "nodeLabels", c.NodeLabels,
		"levels", c.Levels, "nodeCount", len(nodes))
	snapshot := newTASFlavorSnapshot(log, c.Levels)
	for _, node := range nodes {
		levelValues := utiltas.LevelValues(c.Levels, node.Labels)
		capacity := resources.NewRequests(node.Status.Allocatable)
		domainID := utiltas.DomainID(levelValues)
		snapshot.levelValuesPerDomain[domainID] = levelValues
		snapshot.addCapacity(domainID, capacity)
	}
	snapshot.initialize()
	for domainID, usage := range c.usage {
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
		_, found := c.usage[domainID]
		if !found {
			c.usage[domainID] = resources.Requests{}
		}
		if op == subtract {
			c.usage[domainID].Sub(tr.Requests)
		} else {
			c.usage[domainID].Add(tr.Requests)
		}
	}
}
