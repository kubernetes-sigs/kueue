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

package preemption

import (
	"sigs.k8s.io/kueue/pkg/workload"
)

// PreemptionTargets holds the workloads a single preemptor evicts, in the order
// they were selected, with at most one target per workload.
//
// The same workload can be selected from more than one source: a replaced
// workload slice is seeded as a target up front and, because it stays in the
// snapshot as an admitted workload, the preemptor can select it again. A
// duplicate would subtract the workload's usage twice when simulating whether
// the preemptor fits, and would leave a second target behind that is evicted
// through preemption rather than replaced.
type PreemptionTargets []*Target

// Insert appends the targets whose workload is not in p yet, preserving the
// order in which targets were added and keeping the target already present.
func (p *PreemptionTargets) Insert(targets ...*Target) {
	for _, target := range targets {
		if !p.has(target) {
			*p = append(*p, target)
		}
	}
}

// has reports whether p already targets target's workload. Preemptors evict a
// handful of workloads at most, so a linear scan is cheaper than maintaining a
// key set alongside the slice.
func (p *PreemptionTargets) has(target *Target) bool {
	key := workload.Key(target.WorkloadInfo.Obj)
	for _, present := range *p {
		if workload.Key(present.WorkloadInfo.Obj) == key {
			return true
		}
	}
	return false
}
