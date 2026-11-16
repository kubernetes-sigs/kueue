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
	"context"
	"errors"
	"iter"
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"sigs.k8s.io/kueue/pkg/cache/scheduler/simulator"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/util/tas"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	"sigs.k8s.io/kueue/pkg/workload"
)

type countingNodeMatcher struct {
	simulator.SchedulerSimulator
	calls      int
	failOnCall int
}

func (s *countingNodeMatcher) FindFeasibleNodes(
	ctx context.Context,
	candidates iter.Seq[simulator.Candidate],
	requirements *simulator.PodRequirements,
	stats *simulator.NodeExclusionStats,
) ([]simulator.MatchedCandidate, error) {
	s.calls++
	if s.calls == s.failOnCall {
		for candidate := range candidates {
			candidate.(simulator.MatchedCandidate).SetAffinityScore(1000)
			break
		}
		return nil, errors.New("injected node matching failure")
	}
	return s.SchedulerSimulator.FindFeasibleNodes(ctx, candidates, requirements, stats)
}

func TestTopologyAssignmentsPreserveCachedAffinity(t *testing.T) {
	testTopologyAssignmentsPreserveCachedAffinity(t, newDefaultSimulatorFactory())
}

func testTopologyAssignmentsPreserveCachedAffinity(t *testing.T, factory simulator.Factory) {
	t.Helper()
	const rackLabel = "example.com/rack"
	cases := map[string]struct {
		cacheEnabled bool
		withLeader   bool
		withoutUID   bool
		affinityOff  bool
		failOnCall   int
	}{
		"cached workers":                      {cacheEnabled: true},
		"uncached workers":                    {},
		"cached workers with a leader":        {cacheEnabled: true, withLeader: true},
		"uncached workers with a leader":      {withLeader: true},
		"no workload identity to cache":       {cacheEnabled: true, withoutUID: true},
		"preferred affinity disabled":         {cacheEnabled: true, affinityOff: true},
		"leader with preferred affinity off":  {cacheEnabled: true, withLeader: true, affinityOff: true},
		"leader without an identity to cache": {cacheEnabled: true, withLeader: true, withoutUID: true},
		"retry failed worker matching":        {cacheEnabled: true, failOnCall: 1},
		"retry failed leader matching":        {cacheEnabled: true, withLeader: true, failOnCall: 2},
		"retry uncached worker matching":      {failOnCall: 1},
		"retry uncached leader matching":      {withLeader: true, failOnCall: 2},
	}
	for name, tc := range cases {
		for topologyName, levels := range map[string][]string{
			"hostname":         {rackLabel, corev1.LabelHostname},
			"virtual hostname": {rackLabel},
		} {
			t.Run(name+"/"+topologyName, func(t *testing.T) {
				features.SetFeatureGateDuringTest(t, features.TASCacheNodeMatchResults, tc.cacheEnabled)
				features.SetFeatureGateDuringTest(t, features.TASRespectNodeAffinityPreferred, !tc.affinityOff)
				features.SetFeatureGateDuringTest(t, features.TASLeaderPodSetFeasibility, true)
				features.SetFeatureGateDuringTest(t, features.TASNodeFeasibilityForAllLevels, true)
				ctx, log := utiltesting.ContextWithLog(t)
				nodeBase := node.MakeNode("").StatusAllocatable(corev1.ResourceList{
					corev1.ResourceCPU:  resource.MustParse("8"),
					corev1.ResourcePods: resource.MustParse("10"),
				}).Ready()
				nodes := []*corev1.Node{
					nodeBase.Clone().Name("node-a").Label(corev1.LabelHostname, "node-a").Label(rackLabel, "rack-a").Obj(),
					nodeBase.Clone().Name("node-z").Label(corev1.LabelHostname, "node-z").Label(rackLabel, "rack-z").Obj(),
				}
				schedulerSimulator, err := factory.NewSimulator(ctx, nodes)
				if err != nil {
					t.Fatalf("creating simulator: %v", err)
				}
				matcher := &countingNodeMatcher{SchedulerSimulator: schedulerSimulator, failOnCall: tc.failOnCall}
				snapshot := newTASFlavorSnapshot(log, flavorInformation{TopologyName: "topology"}, newTopologyTree(levels, nodes, 0), matcher)
				workers := utiltestingapi.MakePodSet("workers", 2).UnconstrainedTopologyRequest().
					PreferredNodeSelectorRequirement(20, rackLabel, corev1.NodeSelectorOpIn, "rack-z")
				requests := FlavorTASRequests{{
					PodSet: workers.Obj(), Count: 2,
					SinglePodRequests: resources.NewRequestsFromMap(map[corev1.ResourceName]int64{corev1.ResourceCPU: 1000}),
				}}
				if tc.withLeader {
					workers.PodSetGroup("group")
					requests = append(requests, TASPodSetRequests{
						PodSet: utiltestingapi.MakePodSet("leader", 1).UnconstrainedTopologyRequest().PodSetGroup("group").
							PreferredNodeSelectorRequirement(50, rackLabel, corev1.NodeSelectorOpIn, "rack-a").Obj(),
						Count: 1, SinglePodRequests: requests[0].SinglePodRequests,
					})
				}
				wl := utiltestingapi.MakeWorkload("wl", "default").UID("wl").Obj()
				if tc.withoutUID {
					wl.UID = ""
				}
				info := workload.NewInfo(log, wl)
				if tc.failOnCall != 0 {
					result := snapshot.FindTopologyAssignmentsForFlavor(ctx, requests, WithWorkloadInfo(info))
					wantReason := "unable to calculate domain capacities for PodSet workers, error: injected node matching failure"
					if failure := result.Failure(); failure == nil || failure.Reason != wantReason {
						t.Fatalf("matching failure = %v, want %q", failure, wantReason)
					}
				}
				wantValues := []string{"rack-z", "node-z"}
				if tc.affinityOff {
					wantValues = []string{"rack-a", "node-a"}
				}
				wantLevels := levels
				if len(levels) == 2 {
					wantLevels = levels[1:]
					wantValues = wantValues[1:]
				} else {
					wantValues = wantValues[:1]
				}
				want := &tas.TopologyAssignment{Levels: wantLevels, Domains: []tas.TopologyDomainAssignment{{Count: 2, Values: wantValues}}}
				for attempt := range 3 {
					result := snapshot.FindTopologyAssignmentsForFlavor(ctx, requests, WithWorkloadInfo(info))
					if failure := result.Failure(); failure != nil {
						t.Fatalf("attempt %d failed: %s", attempt, failure.Reason)
					}
					if diff := cmp.Diff(want, result["workers"].TopologyAssignment); diff != "" {
						t.Errorf("attempt %d: unexpected worker placement (-want,+got):\n%s", attempt, diff)
					}
					if attempt == 1 {
						// A different PodSet overwrites the reusable simulator candidates
						// before the original Workload uses its cached result again.
						other := FlavorTASRequests{{
							PodSet: utiltestingapi.MakePodSet("other", 1).UnconstrainedTopologyRequest().
								PreferredNodeSelectorRequirement(80, rackLabel, corev1.NodeSelectorOpIn, "rack-a").Obj(),
							Count: 1, SinglePodRequests: requests[0].SinglePodRequests,
						}}
						if failure := snapshot.FindTopologyAssignmentsForFlavor(ctx, other, WithWorkloadInfo(info)).Failure(); failure != nil {
							t.Fatalf("interleaved assignment failed: %s", failure.Reason)
						}
					}
				}
				wantCalls := 3*len(requests) + 1
				wantCalls += tc.failOnCall
				if tc.cacheEnabled && !tc.withoutUID {
					wantCalls = len(requests) + 1
					if tc.failOnCall != 0 {
						wantCalls++
					}
				}
				if matcher.calls != wantCalls {
					t.Errorf("simulator calls = %d, want %d", matcher.calls, wantCalls)
				}
			})
		}
	}
}
