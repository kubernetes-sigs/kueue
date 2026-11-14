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
	"errors"
	"fmt"

	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	ctrl "sigs.k8s.io/controller-runtime"
)

var errIncompletePool = errors.New("pool is incomplete")

// poolID identifies a ResourceSlice pool. Pool names are only unique per driver.
type poolID struct {
	driver string
	pool   string
}

// poolInfo holds the slices of the highest observed generation of a pool.
type poolInfo struct {
	generation         int64
	resourceSliceCount int64
	slices             []*resourcev1.ResourceSlice
}

// eligibleSlices returns the slices of the pools that the DRA allocator can
// allocate from: pools that are complete and valid at their highest observed
// generation. The listing order is kept because callers match devices greedily.
func eligibleSlices(ctx context.Context, slices []resourcev1.ResourceSlice) []*resourcev1.ResourceSlice {
	log := ctrl.LoggerFrom(ctx)
	eligibleGenerations := make(map[poolID]int64)
	for id, pool := range groupSlicesByPool(slices) {
		if err := pool.validate(); err != nil {
			// Incomplete pools are expected while a driver publishes slices.
			// Invalid pools indicate a driver bug, so they are visible at the default log level.
			verbosity := 2
			if errors.Is(err, errIncompletePool) {
				verbosity = 4
			}
			log.V(verbosity).Info("Skipping ResourceSlice pool", "driver", id.driver, "pool", id.pool, "generation", pool.generation, "reason", err.Error())
			continue
		}
		eligibleGenerations[id] = pool.generation
	}

	var eligible []*resourcev1.ResourceSlice
	for i := range slices {
		slice := &slices[i]
		gen, ok := eligibleGenerations[poolIDOf(slice)]
		if ok && slice.Spec.Pool.Generation == gen {
			eligible = append(eligible, slice)
		}
	}
	return eligible
}

func poolIDOf(slice *resourcev1.ResourceSlice) poolID {
	return poolID{driver: slice.Spec.Driver, pool: slice.Spec.Pool.Name}
}

// groupSlicesByPool keeps only the slices of the highest observed generation of each pool.
func groupSlicesByPool(slices []resourcev1.ResourceSlice) map[poolID]*poolInfo {
	pools := make(map[poolID]*poolInfo)
	for i := range slices {
		slice := &slices[i]
		id := poolIDOf(slice)
		gen := slice.Spec.Pool.Generation
		existing, ok := pools[id]
		switch {
		case !ok || gen > existing.generation:
			pools[id] = &poolInfo{
				generation:         gen,
				resourceSliceCount: slice.Spec.Pool.ResourceSliceCount,
				slices:             []*resourcev1.ResourceSlice{slice},
			}
		case gen == existing.generation:
			existing.slices = append(existing.slices, slice)
		}
	}
	return pools
}

// validate mirrors the pool checks of the DRA allocator in
// k8s.io/dynamic-resource-allocation/structured. The allocator does not
// allocate devices from incomplete or invalid pools.
func (p *poolInfo) validate() error {
	if int64(len(p.slices)) != p.resourceSliceCount {
		return errIncompletePool
	}

	deviceNames := sets.New[string]()
	counterSets := make(map[string]sets.Set[string])
	for _, slice := range p.slices {
		for i := range slice.Spec.SharedCounters {
			counterSet := &slice.Spec.SharedCounters[i]
			if _, found := counterSets[counterSet.Name]; found {
				return fmt.Errorf("duplicate counter set name %s", counterSet.Name)
			}
			counterSets[counterSet.Name] = sets.KeySet(counterSet.Counters)
		}
		for i := range slice.Spec.Devices {
			name := slice.Spec.Devices[i].Name
			if deviceNames.Has(name) {
				return fmt.Errorf("duplicate device name %s", name)
			}
			deviceNames.Insert(name)
		}
	}

	// Counter sets may be published in any slice of the pool, so devices are
	// checked only after all counter sets are known.
	for _, slice := range p.slices {
		for i := range slice.Spec.Devices {
			for _, consumption := range slice.Spec.Devices[i].ConsumesCounters {
				counters, found := counterSets[consumption.CounterSet]
				if !found {
					return fmt.Errorf("counter set %s not found", consumption.CounterSet)
				}
				for counterName := range consumption.Counters {
					if !counters.Has(counterName) {
						return fmt.Errorf("counter %s not found in counter set %s", counterName, consumption.CounterSet)
					}
				}
			}
		}
	}
	return nil
}
