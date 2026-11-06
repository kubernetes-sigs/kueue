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
	"maps"
	"slices"

	"sigs.k8s.io/kueue/pkg/workload"
)

type PreemptedWorkloads map[workload.Reference]*workload.Info

func (p PreemptedWorkloads) HasAny(newTargets []*Target) bool {
	for _, target := range newTargets {
		if _, found := p[workload.Key(target.WorkloadInfo.Obj)]; found {
			return true
		}
	}
	return false
}

func (p PreemptedWorkloads) Insert(newTargets []*Target) {
	for _, target := range newTargets {
		p[workload.Key(target.WorkloadInfo.Obj)] = target.WorkloadInfo
	}
}

// MergeWithTargets returns the union of workloads already preempted and
// the new preemption targets, deduplicated by workload key.
// The receiver is not mutated.
func (p PreemptedWorkloads) MergeWithTargets(targets []*Target) PreemptedWorkloads {
	merged := maps.Clone(p)
	if merged == nil {
		merged = make(PreemptedWorkloads, len(targets))
	}
	merged.Insert(targets)
	return merged
}

func (p PreemptedWorkloads) Workloads() []*workload.Info {
	return slices.Collect(maps.Values(p))
}

func (p PreemptedWorkloads) Delete(targets []*Target) {
	for _, target := range targets {
		delete(p, workload.Key(target.WorkloadInfo.Obj))
	}
}
