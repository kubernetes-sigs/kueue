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
	"cmp"
	"fmt"
	"maps"
	"slices"

	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
)

// HasRequiredSpreadingLevels reports whether this flavor's topology has every
// level named by a Required rule in spec, so callers can skip a flavor that
// can't honour it instead of admitting it with spreading left unenforced.
// Preferred rules don't gate flavor selection - an unmet one is just skipped.
func (s *TASFlavorSnapshot) HasRequiredSpreadingLevels(spec *utiltas.SpreadingSpec) bool {
	if spec == nil {
		return true
	}
	for _, rule := range spec.Rules {
		if rule.EnforcementMode != utiltas.TopologySpreadingEnforcementModeRequired {
			continue
		}
		if _, found := s.resolveLevelIdx(rule.TopologyKey); !found {
			return false
		}
	}
	return true
}

// validateSpreadingLevels fails if a rule names a level below
// requestedLevelIdx (unenforceable). A level absent from this flavor's
// topology is skipped, not rejected, so one spreading annotation stays usable
// across flavors with different topologies.
func (s *TASFlavorSnapshot) validateSpreadingLevels(spec *utiltas.SpreadingSpec, requestedLevelIdx int) string {
	if spec == nil {
		return ""
	}
	for _, rule := range spec.Rules {
		levelIdx, found := s.resolveLevelIdx(rule.TopologyKey)
		if !found {
			s.log.V(3).Info("Topology spreading rule skipped, its level is not part of the flavor topology",
				"level", rule.TopologyKey, "topology", s.topologyName)
			continue
		}
		// A rule below the requested level cannot be enforced: the group lands
		// in a single domain at the requested level, so it takes whichever of
		// that domain's descendants it needs and no choice is left to make at
		// the rule's level.
		if levelIdx > requestedLevelIdx {
			return fmt.Sprintf("topology spreading level %s is below the podset topology %s",
				rule.TopologyKey, s.levelKeys[requestedLevelIdx])
		}
	}
	return ""
}

// resolveSpreadLevelRules resolves spec's rules against this flavor's
// topology levels, keyed by level index. Returns nil if spec is nil or if none
// of its rules resolve - so a non-empty result is the single signal that
// spreading applies, and the callers threading it through the placement loops
// need no gate check of their own. The feature gate is checked once by
// findTopologyAssignment, which is the only caller.
func (s *TASFlavorSnapshot) resolveSpreadLevelRules(spec *utiltas.SpreadingSpec) map[int]utiltas.SpreadingRule {
	if spec == nil {
		return nil
	}
	rules := make(map[int]utiltas.SpreadingRule, len(spec.Rules))
	for _, rule := range spec.Rules {
		levelIdx, found := s.resolveLevelIdx(rule.TopologyKey)
		if !found {
			continue
		}
		rules[levelIdx] = rule
	}
	return rules
}

// evaluateSpreadRule reports whether ds's domain is over rule's share of its
// parent. Whether "over" bans or merely penalizes is up to the caller.
// Requires populateSpreadCounts to have already run.
func evaluateSpreadRule(rule utiltas.SpreadingRule, ds *domainState) bool {
	return rule.ExceedsShare(ds.spread.count, ds.spread.parentCount)
}

// bannedBySpreading reports whether d, or an ancestor of d, is over a
// Required rule's threshold at its own level. Ancestors count because a rule
// governs its whole subtree: an over-allowance block bans its racks even
// though the block level is never a placement candidate itself. Requires
// populateSpreadCounts to have already run.
func (s *TASFlavorSnapshot) bannedBySpreading(d *domain, rules map[int]utiltas.SpreadingRule) bool {
	for ; d != nil; d = d.parent {
		rule, ok := rules[len(d.levelValues)-1]
		if ok && rule.EnforcementMode == utiltas.TopologySpreadingEnforcementModeRequired && evaluateSpreadRule(rule, s.domainStateOf(d)) {
			return true
		}
	}
	return false
}

// filterOutBannedDomains drops the domains banned by a Required rule - a hard
// "never this domain for this Workload" removal, not a signal to try a
// coarser level. Callers must handle an empty result: if spreading leaves
// nothing, the Workload waits rather than violating the rule.
func (s *TASFlavorSnapshot) filterOutBannedDomains(domains []*domain, rules map[int]utiltas.SpreadingRule) []*domain {
	if len(rules) == 0 {
		return domains
	}
	result := make([]*domain, 0, len(domains))
	for _, d := range domains {
		if !s.bannedBySpreading(d, rules) {
			result = append(result, d)
		}
	}
	if len(result) == 0 && len(domains) > 0 {
		s.log.V(4).Info("Topology spreading excluded every candidate domain", "topology", s.topologyName, "excludedDomains", len(domains))
	}
	return result
}

// spreadTier ranks ds's domain by topology-spreading usage, low to high:
//
//   - 0: already used by this group, and still under rule's threshold - reuse
//     these before spinning up a fresh domain (bin-pack, per design doc R8).
//   - 1: never used by this group.
//   - 2: over rule's threshold - a last resort.
func spreadTier(rule utiltas.SpreadingRule, ds *domainState) int {
	if evaluateSpreadRule(rule, ds) {
		return 2
	}
	if ds.spread.count == 0 {
		return 1
	}
	return 0
}

// ancestorAtLevel returns the domain containing d at levelIdx, or nil if d is
// itself above that level.
func ancestorAtLevel(d *domain, levelIdx int) *domain {
	for d != nil && len(d.levelValues)-1 > levelIdx {
		d = d.parent
	}
	if d == nil || len(d.levelValues)-1 != levelIdx {
		return nil
	}
	return d
}

// compareSpreadPriority orders two domains of the same topology level by how
// well they satisfy the spreading rules, best first. levels holds the rule
// levels, coarsest first; at each one the domains' ancestors are compared by
// spreadTier, then, within the "over threshold" tier, by occupancy (lowest
// first, so as not to deepen the skew the rule exists to prevent). Domains
// that tie stay in the capacity/affinity order the caller established.
func (s *TASFlavorSnapshot) compareSpreadPriority(a, b *domain, levels []int, rules map[int]utiltas.SpreadingRule) int {
	for _, levelIdx := range levels {
		ancestorA, ancestorB := ancestorAtLevel(a, levelIdx), ancestorAtLevel(b, levelIdx)
		if ancestorA == nil || ancestorB == nil {
			continue
		}
		rule := rules[levelIdx]
		stateA, stateB := s.domainStateOf(ancestorA), s.domainStateOf(ancestorB)
		tierA, tierB := spreadTier(rule, stateA), spreadTier(rule, stateB)
		if tierA != tierB {
			return cmp.Compare(tierA, tierB)
		}
		// Equal tiers, so one check answers it for both.
		if evaluateSpreadRule(rule, stateA) && stateA.spread.count != stateB.spread.count {
			return cmp.Compare(stateA.spread.count, stateB.spread.count)
		}
	}
	return 0
}

// sortedBySpreadPriority sorts domains by compareSpreadPriority, and is the
// last step of sortedDomains and sortedDomainsWithLeader. The sort is stable,
// so domains that spreading ranks equally keep the order those two gave them.
func (s *TASFlavorSnapshot) sortedBySpreadPriority(domains []*domain, rules map[int]utiltas.SpreadingRule) []*domain {
	if len(rules) == 0 {
		return domains
	}
	// Sorted once here rather than on every comparison.
	levels := slices.Sorted(maps.Keys(rules))
	result := slices.Clone(domains)
	slices.SortStableFunc(result, func(a, b *domain) int {
		return s.compareSpreadPriority(a, b, levels, rules)
	})
	return result
}

// populateSpreadCounts copies each domain's occupancy count and its parent's
// count from counts into domainState, once for the whole tree per
// findTopologyAssignment call. It does not evaluate any rule or threshold -
// that's evaluateSpreadRule's job.
func (s *TASFlavorSnapshot) populateSpreadCounts(counts *SpreadTreeCount) {
	if counts == nil {
		return
	}
	for _, levelDomains := range s.domainsPerLevel {
		for _, d := range levelDomains {
			parentCount := counts.Total
			if d.parent != nil {
				parentCount = counts.ByDomain[d.parent.id]
			}
			s.domainStateOf(d).spread = spreadOccupancy{
				count:       counts.ByDomain[d.id],
				parentCount: parentCount,
			}
		}
	}
}

// topSpreadTierDomains truncates the candidate list to the leading domains that
// the spreading rules rank equally best, so BestFit minimizes capacity within
// that tier rather than across it. Without it BestFit picks the tightest
// sufficient domain, which is the most loaded one - the exact inverse of the
// spreading order - and a Preferred rule has nothing else enforcing it. A
// Required rule survives either way, because filterOutBannedDomains has already
// removed the domains it forbids.
//
// Mirrors topAffinityTierDomains, and like it requires the candidates to be
// sorted already, here by sortedBySpreadPriority.
func (s *TASFlavorSnapshot) topSpreadTierDomains(candidates []*domain, rules map[int]utiltas.SpreadingRule) []*domain {
	if len(rules) == 0 || len(candidates) == 0 {
		return candidates
	}
	levels := slices.Sorted(maps.Keys(rules))
	for i, c := range candidates {
		if s.compareSpreadPriority(candidates[0], c, levels, rules) != 0 {
			return candidates[:i]
		}
	}
	return candidates
}
