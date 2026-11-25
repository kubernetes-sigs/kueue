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

	corev1 "k8s.io/api/core/v1"

	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/util/priority"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	"sigs.k8s.io/kueue/pkg/workload"
)

// WithPriorityColocation enables score-first packing for an eligible native plan.
func WithPriorityColocation() FindTopologyAssignmentsOption {
	return func(o *findTopologyAssignmentsOption) { o.colocation = true }
}

// WithPinnedColocation limits a probe to the domains of a verified plan.
func WithPinnedColocation(ta *utiltas.TopologyAssignment) FindTopologyAssignmentsOption {
	return func(o *findTopologyAssignmentsOption) {
		o.colocation = true
		o.pinnedColocation = ta
	}
}

func (s *TASFlavorSnapshot) updateColocationUsage(usage workload.TASFlavorUsage, p int64, op usageOp) {
	if !features.Enabled(features.AggressivePriorityColocate) {
		return
	}
	if s.colocationUsage == nil {
		s.colocationUsage = make(map[int64]map[int]resources.Requests)
	}
	if s.colocationUsage[p] == nil {
		s.colocationUsage[p] = make(map[int]resources.Requests)
	}
	for _, tr := range usage {
		for d := s.usageDomain(utiltas.DomainID(tr.Values)); d != nil; d = d.parent {
			s.colocationUsage[p][d.idx] = updateUsage(s.colocationUsage[p][d.idx], tr.TotalRequests(), op)
		}
	}
}

func (s *TASFlavorSnapshot) colocationCapacityOf(d *domain) resources.Requests {
	if capacity := s.colocationCapacity[d.idx]; capacity != nil {
		return capacity
	}
	if s.colocationCapacity == nil {
		s.colocationCapacity = make(map[int]resources.Requests)
	}
	var capacity resources.Requests
	if len(d.children) == 0 {
		capacity = s.leaves[d.id].capacity
	} else {
		capacity = resources.NewRequests()
		for _, child := range d.children {
			capacity.Add(s.colocationCapacityOf(child))
		}
	}
	s.colocationCapacity[d.idx] = capacity
	return capacity
}

func colocationResources(requested resources.Requests) []corev1.ResourceName {
	var names []corev1.ResourceName
	requested.ForEach(func(name corev1.ResourceName, amount resources.Amount) {
		if name != corev1.ResourcePods && amount.Sign() > 0 {
			names = append(names, name)
		}
	})
	slices.Sort(names)
	return names
}

func (s *TASFlavorSnapshot) colocationScore(d *domain, p int64, names []corev1.ResourceName) float64 {
	usage := s.colocationUsage[p][d.idx]
	if usage == nil {
		return 0
	}
	capacity := s.colocationCapacityOf(d)
	var score float64
	for _, name := range names {
		if total := capacity.ResourceValue(name); total.Sign() > 0 {
			score += usage.ResourceValue(name).PerThousandOf(total)
		}
	}
	return score
}

func (s *TASFlavorSnapshot) populateColocationScores(wl *workload.Info, requested resources.Requests) {
	p := priority.EffectivePriority(s.log, wl.Obj)
	names := colocationResources(requested)
	for _, level := range s.domainsPerLevel {
		for _, d := range level {
			s.domainStateOf(d).colocationScore = s.colocationScore(d, p, names)
		}
	}
}

// ColocationScores returns pod-weighted scores, from coarsest to finest declared
// level. The incoming workload is not reserved while these scores are compared.
func (s *TASFlavorSnapshot) ColocationScores(wl *workload.Info, requested resources.Requests, ta *utiltas.TopologyAssignment) []float64 {
	result := make([]float64, s.usageLevelIdx()+1)
	names := colocationResources(requested)
	p := priority.EffectivePriority(s.log, wl.Obj)
	var count int32
	for _, assignment := range ta.Domains {
		d := s.usageDomain(utiltas.DomainID(assignment.Values))
		count += assignment.Count
		for ; d != nil; d = d.parent {
			level := len(d.levelValues) - 1
			result[level] += float64(assignment.Count) * s.colocationScore(d, p, names)
		}
	}
	if count > 0 {
		for i := range result {
			result[i] /= float64(count)
		}
	}
	return result
}

func (s *TASFlavorSnapshot) pinnedColocationCount(leaf *leafDomain) int32 {
	for _, d := range s.pinnedColocation.Domains {
		matches := len(d.Values) == len(s.pinnedColocation.Levels)
		for i, key := range s.pinnedColocation.Levels {
			level := slices.Index(s.levelKeys, key)
			if level < 0 || i >= len(d.Values) || leaf.levelValues[level] != d.Values[i] {
				matches = false
				break
			}
		}
		if matches {
			return d.Count
		}
	}
	return 0
}
