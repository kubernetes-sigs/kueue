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
	"testing"

	"github.com/go-logr/logr/testr"
	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
)

// newSpreadingTestSnapshot builds a snapshot over block/rack/hostname, the
// level set the spreading helpers are keyed against.
func newSpreadingTestSnapshot(t *testing.T) *TASFlavorSnapshot {
	t.Helper()
	levels := []string{treeTestBlockLabel, treeTestRackLabel, corev1.LabelHostname}
	return newTASFlavorSnapshot(testr.New(t), flavorInformation{TopologyName: "topology"},
		newTopologyTree(levels, []*corev1.Node{
			makeTreeTestNode("n1", "b1", "r1"),
			makeTreeTestNode("n2", "b2", "r2"),
		}, 0), newDefaultSimulatorSnapshot())
}

func spreadingRule(key string, mode utiltas.TopologySpreadingEnforcementMode) utiltas.SpreadingRule {
	return utiltas.SpreadingRule{TopologyKey: key, EnforcementMode: mode}
}

// spreadingSpecFor builds a spec directly, since these cases exercise the
// rules rather than the selector.
func spreadingSpecFor(t *testing.T, rules ...utiltas.SpreadingRule) *utiltas.SpreadingSpec {
	t.Helper()
	spec, err := utiltas.NewSpreadingSpec(nil, rules, "")
	if err != nil {
		t.Fatalf("NewSpreadingSpec() unexpected error: %v", err)
	}
	return spec
}

func TestHasRequiredSpreadingLevels(t *testing.T) {
	cases := map[string]struct {
		rules   []utiltas.SpreadingRule
		nilSpec bool
		want    bool
	}{
		"no spreading spec": {
			nilSpec: true,
			want:    true,
		},
		"Required rule naming a level of this topology": {
			rules: []utiltas.SpreadingRule{spreadingRule(treeTestRackLabel, utiltas.TopologySpreadingEnforcementModeRequired)},
			want:  true,
		},
		"Required rule naming a level absent from this topology": {
			rules: []utiltas.SpreadingRule{spreadingRule("cloud.provider.com/topology-zone", utiltas.TopologySpreadingEnforcementModeRequired)},
			want:  false,
		},
		// A Preferred rule is a preference, so a flavor missing its level
		// stays usable with the rule skipped.
		"Preferred rule naming a level absent from this topology": {
			rules: []utiltas.SpreadingRule{spreadingRule("cloud.provider.com/topology-zone", utiltas.TopologySpreadingEnforcementModePreferred)},
			want:  true,
		},
		"one Required rule present, one Preferred rule absent": {
			rules: []utiltas.SpreadingRule{
				spreadingRule(treeTestBlockLabel, utiltas.TopologySpreadingEnforcementModeRequired),
				spreadingRule("cloud.provider.com/topology-zone", utiltas.TopologySpreadingEnforcementModePreferred),
			},
			want: true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			snapshot := newSpreadingTestSnapshot(t)
			var spec *utiltas.SpreadingSpec
			if !tc.nilSpec {
				spec = spreadingSpecFor(t, tc.rules...)
			}
			if got := snapshot.HasRequiredSpreadingLevels(spec); got != tc.want {
				t.Errorf("HasRequiredSpreadingLevels() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestResolveSpreadLevelRules(t *testing.T) {
	cases := map[string]struct {
		rules   []utiltas.SpreadingRule
		nilSpec bool
		want    map[int]utiltas.SpreadingRule
	}{
		"no spreading spec": {
			nilSpec: true,
			want:    nil,
		},
		"rules keyed by the level index they resolve to": {
			rules: []utiltas.SpreadingRule{
				spreadingRule(treeTestBlockLabel, utiltas.TopologySpreadingEnforcementModeRequired),
				spreadingRule(treeTestRackLabel, utiltas.TopologySpreadingEnforcementModePreferred),
			},
			want: map[int]utiltas.SpreadingRule{
				0: spreadingRule(treeTestBlockLabel, utiltas.TopologySpreadingEnforcementModeRequired),
				1: spreadingRule(treeTestRackLabel, utiltas.TopologySpreadingEnforcementModePreferred),
			},
		},
		// A level absent from this flavor's topology is skipped, so one
		// annotation stays usable across flavors with different topologies.
		"rule naming a level absent from this topology is skipped": {
			rules: []utiltas.SpreadingRule{
				spreadingRule(treeTestBlockLabel, utiltas.TopologySpreadingEnforcementModeRequired),
				spreadingRule("cloud.provider.com/topology-zone", utiltas.TopologySpreadingEnforcementModeRequired),
			},
			want: map[int]utiltas.SpreadingRule{
				0: spreadingRule(treeTestBlockLabel, utiltas.TopologySpreadingEnforcementModeRequired),
			},
		},
		// An empty result must be empty rather than absent: a non-empty map is
		// the signal that spreading applies at all.
		"no rule resolves": {
			rules: []utiltas.SpreadingRule{
				spreadingRule("cloud.provider.com/topology-zone", utiltas.TopologySpreadingEnforcementModeRequired),
			},
			want: map[int]utiltas.SpreadingRule{},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			snapshot := newSpreadingTestSnapshot(t)
			var spec *utiltas.SpreadingSpec
			if !tc.nilSpec {
				spec = spreadingSpecFor(t, tc.rules...)
			}
			got := snapshot.resolveSpreadLevelRules(spec)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("resolveSpreadLevelRules() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestAncestorAtLevel(t *testing.T) {
	snapshot := newSpreadingTestSnapshot(t)
	blockIdx, rackIdx, hostIdx := 0, 1, 2
	block := snapshot.domainsPerLevel[blockIdx]["b1"]
	rack := snapshot.domainsPerLevel[rackIdx]["b1,r1"]
	// A leaf is keyed by its hostname alone, not by its full level-values path.
	leaf := snapshot.domainsPerLevel[hostIdx]["n1"]
	if block == nil || rack == nil || leaf == nil {
		t.Fatalf("test topology is missing a domain: block=%v rack=%v leaf=%v", block, rack, leaf)
	}

	cases := map[string]struct {
		domain   *domain
		levelIdx int
		want     *domain
	}{
		"leaf to its own level": {domain: leaf, levelIdx: hostIdx, want: leaf},
		"leaf to its rack":      {domain: leaf, levelIdx: rackIdx, want: rack},
		"leaf to its block":     {domain: leaf, levelIdx: blockIdx, want: block},
		"rack to its own level": {domain: rack, levelIdx: rackIdx, want: rack},
		// The domain is above the requested level, so it spans every domain
		// there and pins none of them.
		"block to a level below it": {domain: block, levelIdx: rackIdx, want: nil},
		"rack to a level below it":  {domain: rack, levelIdx: hostIdx, want: nil},
		"nil domain":                {domain: nil, levelIdx: blockIdx, want: nil},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := ancestorAtLevel(tc.domain, tc.levelIdx); got != tc.want {
				t.Errorf("ancestorAtLevel() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestCompareSpreadPriorityAboveRuleLevel covers comparing domains that sit
// above a rule's level: neither has an ancestor there, so the rule cannot
// order them and the capacity order the caller established is preserved.
func TestCompareSpreadPriorityAboveRuleLevel(t *testing.T) {
	snapshot := newSpreadingTestSnapshot(t)
	rackIdx := 1
	rules := map[int]utiltas.SpreadingRule{
		rackIdx: spreadingRule(treeTestRackLabel, utiltas.TopologySpreadingEnforcementModeRequired),
	}

	b1, b2 := snapshot.domainsPerLevel[0]["b1"], snapshot.domainsPerLevel[0]["b2"]
	if b1 == nil || b2 == nil {
		t.Fatalf("test topology is missing a block domain: b1=%v b2=%v", b1, b2)
	}
	// Occupancy that would order the two blocks if the rule applied to them.
	snapshot.domainStateOf(b1).spread = spreadOccupancy{count: 2, parentCount: 2}
	snapshot.domainStateOf(b2).spread = spreadOccupancy{count: 0, parentCount: 2}

	if got := snapshot.compareSpreadPriority(b1, b2, []int{rackIdx}, rules); got != 0 {
		t.Errorf("compareSpreadPriority() = %d, want 0 for domains above the rule's level", got)
	}
	if got := snapshot.sortedBySpreadPriority([]*domain{b1, b2}, rules); got[0] != b1 {
		t.Errorf("sortedBySpreadPriority() reordered domains above the rule's level")
	}
}

func TestFindLevelWithFitDomainsSpreading(t *testing.T) {
	cases := map[string]struct {
		leaderCount int32
		mode        utiltas.TopologySpreadingEnforcementMode
		allBanned   bool
		want        []string
		wantReason  string
	}{
		"required worker excludes occupied rack": {mode: utiltas.TopologySpreadingEnforcementModeRequired, want: []string{"b2,r2"}},
		"required leader excludes occupied rack": {leaderCount: 1, mode: utiltas.TopologySpreadingEnforcementModeRequired, want: []string{"b2,r2"}},
		"required worker excludes all racks": {
			mode:       utiltas.TopologySpreadingEnforcementModeRequired,
			allBanned:  true,
			wantReason: "topology spreading excludes all topology domains at level: " + treeTestRackLabel,
		},
		"required leader excludes all racks": {
			leaderCount: 1,
			mode:        utiltas.TopologySpreadingEnforcementModeRequired,
			allBanned:   true,
			wantReason:  "topology spreading excludes all topology domains at level: " + treeTestRackLabel,
		},
		"preferred leader favors unused rack": {leaderCount: 1, mode: utiltas.TopologySpreadingEnforcementModePreferred, want: []string{"b2,r2"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			snapshot := newSpreadingTestSnapshot(t)
			r1 := snapshot.domainsPerLevel[1]["b1,r1"]
			r2 := snapshot.domainsPerLevel[1]["b2,r2"]
			for _, d := range []*domain{r1, r2} {
				state := snapshot.domainStateOf(d)
				state.sliceCount = 2
				state.sliceCountWithLeader = 2
				state.leaderCount = 1
			}
			snapshot.domainStateOf(r1).leaderCount = 2
			snapshot.domainStateOf(r1).spread = spreadOccupancy{count: 1, parentCount: 1}
			if tc.allBanned {
				snapshot.domainStateOf(r2).spread = spreadOccupancy{count: 1, parentCount: 1}
			} else {
				snapshot.domainStateOf(r2).spread = spreadOccupancy{parentCount: 1}
			}
			rule := spreadingRule(treeTestRackLabel, tc.mode)
			rule.MaxShareAllowingPlacement = resource.MustParse("0.5")
			_, got, reason := snapshot.findLevelWithFitDomains(1, &findTopologyAssignmentState{
				topologyAssignmentParameters: topologyAssignmentParameters{
					count: 1, sliceSize: 1, required: true, leaderCount: tc.leaderCount,
					spreadRules: map[int]utiltas.SpreadingRule{1: rule},
				},
			})
			if reason != tc.wantReason {
				t.Errorf("findLevelWithFitDomains() reason = %q, want %q", reason, tc.wantReason)
			}
			if diff := cmp.Diff(tc.want, domainIDs(got)); diff != "" {
				t.Errorf("findLevelWithFitDomains() domains mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
