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

package dra

import (
	"context"
	"fmt"
	"slices"

	corev1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	dracel "k8s.io/dynamic-resource-allocation/cel"
	"k8s.io/dynamic-resource-allocation/structured"
)

// nodeAllocators serves the allocator to use on a node. The allocator computes the
// counters left in a pool once and keeps them, so a node whose pools use shared
// counters gets its own allocator, built again when its devices change.
type nodeAllocators struct {
	shared structured.Allocator

	// withCounters are the nodes whose own pools use shared counters. allWithCounters
	// is set when a pool that is not local to one node uses them, as every node can.
	withCounters    sets.Set[string]
	allWithCounters bool
	byNode          map[string]nodeAllocator

	features    structured.Features
	state       *deviceState
	classLister structured.DeviceClassLister
	celCache    *dracel.Cache
	slices      map[poolKey][]*resourceapi.ResourceSlice
	pools       *slicePools
}

type nodeAllocator struct {
	allocator  structured.Allocator
	generation uint64
}

func newNodeAllocators(ctx context.Context, features structured.Features, state *deviceState, classLister structured.DeviceClassLister,
	deviceSlices []*resourceapi.ResourceSlice, celCache *dracel.Cache, pools *slicePools) (*nodeAllocators, error) {
	shared, err := structured.NewAllocator(ctx, features, state.allocated, classLister, deviceSlices, celCache)
	if err != nil {
		return nil, err
	}
	a := &nodeAllocators{
		shared:       shared,
		withCounters: sets.New[string](),
		byNode:       make(map[string]nodeAllocator),
		features:     features,
		state:        state,
		classLister:  classLister,
		celCache:     celCache,
		slices:       make(map[poolKey][]*resourceapi.ResourceSlice),
		pools:        pools,
	}
	countedPools := sets.New[poolKey]()
	for _, slice := range deviceSlices {
		pool := slicePoolKey(slice)
		a.slices[pool] = append(a.slices[pool], slice)
		if usesCounters(slice) {
			countedPools.Insert(pool)
		}
	}
	if countedPools.Len() == 0 {
		return a, nil
	}
	if slices.ContainsFunc(pools.shared, countedPools.Has) {
		a.allWithCounters = true
		return a, nil
	}
	for nodeName := range pools.onNode {
		if slices.ContainsFunc(pools.forNode(nodeName), countedPools.Has) {
			a.withCounters.Insert(nodeName)
		}
	}
	return a, nil
}

// forNode returns the allocator that sees the current state on node.
func (a *nodeAllocators) forNode(ctx context.Context, node *corev1.Node) (structured.Allocator, error) {
	if !a.allWithCounters && !a.withCounters.Has(node.Name) {
		return a.shared, nil
	}
	generation := a.pools.generation(a.state, node.Name)
	if cached, found := a.byNode[node.Name]; found && cached.generation == generation {
		return cached.allocator, nil
	}
	var nodeSlices []*resourceapi.ResourceSlice
	for _, pool := range a.pools.forNode(node.Name) {
		nodeSlices = append(nodeSlices, a.slices[pool]...)
	}
	allocator, err := structured.NewAllocator(ctx, a.features, a.state.allocated, a.classLister, nodeSlices, a.celCache)
	if err != nil {
		return nil, fmt.Errorf("creating DRA allocator for node %q: %w", node.Name, err)
	}
	a.byNode[node.Name] = nodeAllocator{allocator: allocator, generation: generation}
	return allocator, nil
}

func usesCounters(slice *resourceapi.ResourceSlice) bool {
	if len(slice.Spec.SharedCounters) > 0 {
		return true
	}
	for i := range slice.Spec.Devices {
		if len(slice.Spec.Devices[i].ConsumesCounters) > 0 {
			return true
		}
	}
	return false
}

// poolKey names a resource pool, which a driver names uniquely.
type poolKey struct {
	driver, pool string
}

// slicePools are the pools whose devices each node can use: the pools of its own
// slices, and those of slices that are not local to one node.
type slicePools struct {
	onNode map[string][]poolKey
	shared []poolKey
}

func newSlicePools(deviceSlices []*resourceapi.ResourceSlice) *slicePools {
	p := &slicePools{onNode: make(map[string][]poolKey)}
	for _, slice := range deviceSlices {
		pool := slicePoolKey(slice)
		if slice.Spec.NodeName == nil || *slice.Spec.NodeName == "" {
			if !slices.Contains(p.shared, pool) {
				p.shared = append(p.shared, pool)
			}
			continue
		}
		if !slices.Contains(p.onNode[*slice.Spec.NodeName], pool) {
			p.onNode[*slice.Spec.NodeName] = append(p.onNode[*slice.Spec.NodeName], pool)
		}
	}
	return p
}

// forNode lists each pool once, though a pool can have slices for the node and for every node.
func (p *slicePools) forNode(nodeName string) []poolKey {
	pools := slices.Clone(p.onNode[nodeName])
	for _, pool := range p.shared {
		if !slices.Contains(pools, pool) {
			pools = append(pools, pool)
		}
	}
	return pools
}

// generation names the state of the devices that node can use, and changes whenever one is
// taken or released. It is read for every count, so unlike forNode it allocates nothing.
func (p *slicePools) generation(state *deviceState, nodeName string) uint64 {
	return max(state.poolsGeneration(p.onNode[nodeName]), state.poolsGeneration(p.shared))
}

func slicePoolKey(slice *resourceapi.ResourceSlice) poolKey {
	return poolKey{driver: slice.Spec.Driver, pool: slice.Spec.Pool.Name}
}
