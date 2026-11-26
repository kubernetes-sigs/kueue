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
	"maps"

	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/dynamic-resource-allocation/structured"
	"k8s.io/utils/ptr"
)

// deviceState is what the allocator treats as taken. Every allocator reads these sets
// on each Allocate call, so the next call sees a device taken or released here. Only
// the counters left in a pool are computed once, so nodeAllocators builds the allocator
// of a node whose pools use shared counters again when its devices change.
type deviceState struct {
	allocated structured.AllocatedState
	// consumableCapacity is the allocator's own setting, so the state and the
	// allocator agree on whether a shared device is partly or wholly consumed.
	consumableCapacity bool
	// poolGenerations name the state of each pool: a generation is never used for another
	// state, so a result derived from the devices of some pools can tell it is out of date.
	poolGenerations map[poolKey]uint64
	lastGeneration  uint64
	// probes are the generations that each open probe changed, as they were before it.
	probes []map[poolKey]uint64
}

// deviceHold is the devices one allocation takes, kept so they can be given back.
type deviceHold struct {
	devices  []structured.DeviceID
	shared   []structured.SharedDeviceID
	capacity []structured.DeviceConsumedCapacity
}

func (h deviceHold) isEmpty() bool {
	return len(h.devices) == 0 && len(h.shared) == 0 && len(h.capacity) == 0
}

func newDeviceState(consumableCapacity bool) *deviceState {
	return &deviceState{
		allocated: structured.AllocatedState{
			AllocatedDevices:         sets.New[structured.DeviceID](),
			AllocatedSharedDeviceIDs: sets.New[structured.SharedDeviceID](),
			AggregatedCapacity:       structured.NewConsumedCapacityCollection(),
		},
		consumableCapacity: consumableCapacity,
		poolGenerations:    make(map[poolKey]uint64),
	}
}

// holdOf skips admin access, which takes no device, as in kube-scheduler.
func (s *deviceState) holdOf(results []resourceapi.DeviceRequestAllocationResult) deviceHold {
	var hold deviceHold
	for _, result := range results {
		if ptr.Deref(result.AdminAccess, false) {
			continue
		}
		deviceID := structured.MakeDeviceID(result.Driver, result.Pool, result.Device)
		if s.consumableCapacity && result.ShareID != nil {
			hold.shared = append(hold.shared, structured.MakeSharedDeviceID(deviceID, result.ShareID))
			if result.ConsumedCapacity != nil {
				hold.capacity = append(hold.capacity, structured.NewDeviceConsumedCapacity(deviceID, result.ConsumedCapacity))
			}
			continue
		}
		hold.devices = append(hold.devices, deviceID)
	}
	return hold
}

// holdOfAllocations merges the holds of the results Allocate returns, one per claim.
func (s *deviceState) holdOfAllocations(allocations []resourceapi.AllocationResult) deviceHold {
	var hold deviceHold
	for i := range allocations {
		h := s.holdOf(allocations[i].Devices.Results)
		hold.devices = append(hold.devices, h.devices...)
		hold.shared = append(hold.shared, h.shared...)
		hold.capacity = append(hold.capacity, h.capacity...)
	}
	return hold
}

func (s *deviceState) take(hold deviceHold) {
	s.touch(hold)
	s.allocated.AllocatedDevices.Insert(hold.devices...)
	s.allocated.AllocatedSharedDeviceIDs.Insert(hold.shared...)
	for _, c := range hold.capacity {
		s.allocated.AggregatedCapacity.Insert(c)
	}
}

func (s *deviceState) release(hold deviceHold) {
	s.touch(hold)
	s.allocated.AllocatedDevices.Delete(hold.devices...)
	s.allocated.AllocatedSharedDeviceIDs.Delete(hold.shared...)
	for _, c := range hold.capacity {
		s.allocated.AggregatedCapacity.Remove(c)
	}
}

// touch gives every pool the hold takes devices from a new generation.
func (s *deviceState) touch(hold deviceHold) {
	for _, id := range hold.devices {
		s.renewPool(poolKey{driver: id.Driver.String(), pool: id.Pool.String()})
	}
	for _, id := range hold.shared {
		s.renewPool(poolKey{driver: id.Driver.String(), pool: id.Pool.String()})
	}
	for _, c := range hold.capacity {
		s.renewPool(poolKey{driver: c.Driver.String(), pool: c.Pool.String()})
	}
}

// renewPool gives the pool a generation no pool had before, and inside a probe records
// the one it had, for the probe's end.
func (s *deviceState) renewPool(pool poolKey) {
	if n := len(s.probes); n > 0 {
		if _, found := s.probes[n-1][pool]; !found {
			s.probes[n-1][pool] = s.poolGenerations[pool]
		}
	}
	s.lastGeneration++
	s.poolGenerations[pool] = s.lastGeneration
}

// beginProbe starts a change that the caller undoes in full, such as a count. The
// returned end puts back the generations the probe changed, so results derived from
// these pools before the probe still hold after it.
func (s *deviceState) beginProbe() (end func()) {
	s.probes = append(s.probes, make(map[poolKey]uint64))
	return func() {
		n := len(s.probes)
		maps.Copy(s.poolGenerations, s.probes[n-1])
		s.probes = s.probes[:n-1]
	}
}

// poolsGeneration changes whenever a device in one of the pools is taken or released.
// A change gives a pool a generation higher than any before, and a probe's end puts
// back earlier ones, so the highest generation names the state of the pools.
func (s *deviceState) poolsGeneration(pools []poolKey) uint64 {
	var highest uint64
	for _, pool := range pools {
		highest = max(highest, s.poolGenerations[pool])
	}
	return highest
}
