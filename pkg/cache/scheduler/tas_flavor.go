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
	"sync"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/cache/scheduler/simulator"
	"sigs.k8s.io/kueue/pkg/features"
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

	// schedulingSimulator performs the node feasibility check
	// based on topology requirements.
	schedulingSimulator simulator.SchedulingSimulator
}

func (t *tasCache) NewTASFlavorCache(topologyInfo topologyInformation,
	flavorInfo flavorInformation) *TASFlavorCache {
	return &TASFlavorCache{
		client:              t.client,
		topology:            topologyInfo,
		flavor:              flavorInfo,
		usage:               make(map[utiltas.TopologyDomainID]resources.Requests),
		wlUsage:             make(map[workload.Reference][]workload.TopologyDomainRequests),
		nonTasUsageCache:    t.nonTasUsageCache,
		schedulingSimulator: t.schedulingSimulator,
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

func (c *TASFlavorCache) snapshot(log logr.Logger, nodes []*corev1.Node, aggregatedDomainUsages map[utiltas.TopologyDomainID]resources.Requests) (*TASFlavorSnapshot, error) {
	c.RLock()
	defer c.RUnlock()

	infoKV := []any{
		"nodeLabels", c.flavor.NodeLabels,
		"levels", c.topology.Levels,
		"nodeCount", len(nodes),
	}
	if features.Enabled(features.TASHandleOverlappingFlavors) {
		infoKV = append(infoKV, "crossFlavorAggregation", aggregatedDomainUsages != nil)
	}
	log.V(3).Info("Constructing TAS snapshot", infoKV...)

	feasibilityChecker, err := c.schedulingSimulator.NewFeasibilityChecker(nodes)
	if err != nil {
		return nil, err
	}

	snapshot := newTASFlavorSnapshot(log, c.flavor.TopologyName, c.topology.Levels, c.flavor.Tolerations, feasibilityChecker)
	nodeToDomain := make(map[string]utiltas.TopologyDomainID)
	for _, node := range nodes {
		nodeToDomain[node.Name] = snapshot.addNode(node)
	}
	snapshot.initialize()

	tasDomainUsages := c.usage
	if features.Enabled(features.TASHandleOverlappingFlavors) && aggregatedDomainUsages != nil {
		tasDomainUsages = aggregatedDomainUsages
	}
	for domainID, usage := range tasDomainUsages {
		snapshot.addTASUsage(domainID, usage)
	}
	c.nonTasUsageCache.forEachNodeUsage(func(nodeName string, usage resources.Requests) {
		if domainID, ok := nodeToDomain[nodeName]; ok {
			snapshot.addNonTASUsage(domainID, usage)
		}
	})
	return snapshot, nil
}

func (c *TASFlavorCache) addUsage(log logr.Logger, key workload.Reference, topologyRequests []workload.TopologyDomainRequests) {
	if _, found := c.wlUsage[key]; found {
		log.V(2).Info("Workload usage already exists in TAS flavor cache, self-healing by replacing it", "workload", key)
		c.removeUsage(log, key)
	}
	c.wlUsage[key] = slices.Clone(topologyRequests)
	c.updateUsage(topologyRequests, add)
}

func (c *TASFlavorCache) removeUsage(log logr.Logger, key workload.Reference) {
	value, found := c.wlUsage[key]
	if !found {
		log.V(2).Info("Workload usage not found during removal from TAS flavor cache", "workload", key)
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
