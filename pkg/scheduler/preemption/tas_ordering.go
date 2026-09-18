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
	"cmp"
	"maps"
	"slices"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/resources"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	"sigs.k8s.io/kueue/pkg/workload"
)

// domainKey identifies one topology domain within a resource flavor.
type domainKey struct {
	flavor kueue.ResourceFlavorReference
	domain utiltas.TopologyDomainID
}

// tasDomainRanks ranks every candidate by how promising its topology domain is,
// lowest rank first, so that victims freeing the same domain are popped
// consecutively. Returns nil when the preemptor has no TAS requests.
//
// CandidatesOrdering has no topology term, so victims otherwise come out spread
// across domains and a workload needing a whole domain never fits, even when a
// set that would free one exists (kubernetes-sigs/kueue#10497).
func tasDomainRanks(candidates []*workload.Info, tasRequests schdcache.WorkloadTASRequests) map[workload.Reference]int {
	if len(tasRequests) == 0 {
		return nil
	}

	// need[flavor] is the largest single-pod request the preemptor has to place
	// into a single domain of that flavor.
	need := make(map[kueue.ResourceFlavorReference]resources.Requests, len(tasRequests))
	for flavor, podSets := range tasRequests {
		largest := resources.Requests{}
		for _, ps := range podSets {
			for res, v := range ps.SinglePodRequests {
				largest[res] = max(largest[res], v)
			}
		}
		need[flavor] = largest
	}

	freed := make(map[domainKey]resources.Requests)
	victims := make(map[domainKey]int)
	occupies := make(map[workload.Reference][]domainKey, len(candidates))
	for _, c := range candidates {
		key := workload.Key(c.Obj)
		for flavor, domainRequests := range c.TASUsage() {
			if _, contended := need[flavor]; !contended {
				continue
			}
			for _, dr := range domainRequests {
				dk := domainKey{flavor: flavor, domain: utiltas.DomainID(dr.Values)}
				if freed[dk] == nil {
					freed[dk] = resources.Requests{}
				}
				freed[dk].Add(dr.TotalRequests())
				victims[dk]++
				occupies[key] = append(occupies[key], dk)
			}
		}
	}

	// coverage is the mean fraction of the preemptor's per-domain requirement
	// that this domain's candidates could free, capped at 1 per resource.
	coverage := func(dk domainKey) float64 {
		want := need[dk.flavor]
		if len(want) == 0 {
			return 0
		}
		var total float64
		for res, v := range want {
			if v <= 0 {
				continue
			}
			total += min(float64(freed[dk][res])/float64(v), 1)
		}
		return total / float64(len(want))
	}

	domains := slices.Collect(maps.Keys(freed))
	slices.SortFunc(domains, func(a, b domainKey) int {
		return cmp.Or(
			cmp.Compare(coverage(b), coverage(a)),
			cmp.Compare(victims[a], victims[b]),
			cmp.Compare(a.flavor, b.flavor),
			cmp.Compare(a.domain, b.domain),
		)
	})
	domainRank := make(map[domainKey]int, len(domains))
	for i, dk := range domains {
		domainRank[dk] = i
	}

	// Candidates outside every contended domain cannot help the topology fit, so
	// they sort after the ones that can.
	ranks := make(map[workload.Reference]int, len(candidates))
	for _, c := range candidates {
		key := workload.Key(c.Obj)
		best := len(domains)
		for _, dk := range occupies[key] {
			best = min(best, domainRank[dk])
		}
		ranks[key] = best
	}
	return ranks
}
