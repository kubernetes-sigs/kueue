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
	"fmt"
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	tasindexer "sigs.k8s.io/kueue/pkg/controller/tas/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/util/tas"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/pkg/workload"
)

// TestFindTopologyAssignmentsIndexedReplacement checks repairs of persisted
// rank intervals, including consecutive failures and slice-alignment rejection.
func TestFindTopologyAssignmentsIndexedReplacement(t *testing.T) {
	const (
		zoneLabel = "example.com/zone"
		rackLabel = "example.com/rack"
	)
	cases := map[string]struct {
		original     []tas.TopologyDomainAssignment
		capacity     map[string]int32
		failures     [][]string
		want         [][]tas.TopologyDomainAssignment
		nonIndexed   bool
		singleFailed bool
		sliceSize    int32
		grouped      bool
		leaderNode   string
		wantLeader   string
		nodeRacks    map[string]string
		constraints  []kueue.PodsetSliceRequiredTopologyConstraint
		wantFailure  string
	}{
		"middle node is replaced without shifting the last rank": {
			original: []tas.TopologyDomainAssignment{
				{Values: []string{"node-a"}, Count: 1},
				{Values: []string{"node-b"}, Count: 1},
				{Values: []string{"node-c"}, Count: 1},
			},
			capacity: map[string]int32{"node-a": 1, "node-b": 1, "node-c": 1, "node-z": 1},
			failures: [][]string{{"node-b"}},
			want: [][]tas.TopologyDomainAssignment{{
				{Values: []string{"node-a"}, Count: 1},
				{Values: []string{"node-z"}, Count: 1},
				{Values: []string{"node-c"}, Count: 1},
			}},
		},
		"single-node replacement also preserves ranks with the multiple-failure gate off": {
			original: []tas.TopologyDomainAssignment{
				{Values: []string{"node-a"}, Count: 1},
				{Values: []string{"node-b"}, Count: 1},
				{Values: []string{"node-c"}, Count: 1},
			},
			capacity:     map[string]int32{"node-a": 1, "node-b": 1, "node-c": 1, "node-z": 1},
			failures:     [][]string{{"node-b"}},
			singleFailed: true,
			want: [][]tas.TopologyDomainAssignment{{
				{Values: []string{"node-a"}, Count: 1},
				{Values: []string{"node-z"}, Count: 1},
				{Values: []string{"node-c"}, Count: 1},
			}},
		},
		"nonindexed repair keeps the sorted merge": {
			original: []tas.TopologyDomainAssignment{
				{Values: []string{"node-a"}, Count: 1},
				{Values: []string{"node-b"}, Count: 1},
				{Values: []string{"node-c"}, Count: 1},
			},
			capacity:   map[string]int32{"node-a": 1, "node-b": 1, "node-c": 1, "node-z": 1},
			failures:   [][]string{{"node-b"}},
			nonIndexed: true,
			want: [][]tas.TopologyDomainAssignment{{
				{Values: []string{"node-a"}, Count: 1},
				{Values: []string{"node-c"}, Count: 1},
				{Values: []string{"node-z"}, Count: 1},
			}},
		},
		"a failed run can use several replacement domains": {
			original: []tas.TopologyDomainAssignment{
				{Values: []string{"node-a"}, Count: 3},
				{Values: []string{"node-b"}, Count: 4},
				{Values: []string{"node-c"}, Count: 2},
			},
			capacity: map[string]int32{"node-a": 3, "node-b": 4, "node-c": 2, "node-y": 1, "node-z": 3},
			failures: [][]string{{"node-b"}},
			want: [][]tas.TopologyDomainAssignment{{
				{Values: []string{"node-a"}, Count: 3},
				{Values: []string{"node-y"}, Count: 1},
				{Values: []string{"node-z"}, Count: 3},
				{Values: []string{"node-c"}, Count: 2},
			}},
		},
		"replacement counts span separated failed runs": {
			original: []tas.TopologyDomainAssignment{
				{Values: []string{"node-b"}, Count: 2},
				{Values: []string{"node-a"}, Count: 2},
				{Values: []string{"node-b"}, Count: 1},
			},
			capacity: map[string]int32{"node-a": 2, "node-b": 3, "node-y": 1, "node-z": 2},
			failures: [][]string{{"node-b"}},
			want: [][]tas.TopologyDomainAssignment{{
				{Values: []string{"node-y"}, Count: 1},
				{Values: []string{"node-z"}, Count: 1},
				{Values: []string{"node-a"}, Count: 2},
				{Values: []string{"node-z"}, Count: 1},
			}},
		},
		"a replacement node can fail again in several separated runs": {
			original: []tas.TopologyDomainAssignment{
				{Values: []string{"node-b"}, Count: 2},
				{Values: []string{"node-a"}, Count: 2},
				{Values: []string{"node-b"}, Count: 1},
			},
			capacity: map[string]int32{"node-a": 2, "node-b": 3, "node-y": 3, "node-z": 3},
			failures: [][]string{{"node-b"}, {"node-y"}},
			want: [][]tas.TopologyDomainAssignment{
				{
					{Values: []string{"node-y"}, Count: 2},
					{Values: []string{"node-a"}, Count: 2},
					{Values: []string{"node-y"}, Count: 1},
				},
				{
					{Values: []string{"node-z"}, Count: 2},
					{Values: []string{"node-a"}, Count: 2},
					{Values: []string{"node-z"}, Count: 1},
				},
			},
		},
		"queued failures are repaired in place one at a time": {
			original: []tas.TopologyDomainAssignment{
				{Values: []string{"node-a"}, Count: 1},
				{Values: []string{"node-b"}, Count: 1},
				{Values: []string{"node-c"}, Count: 1},
				{Values: []string{"node-d"}, Count: 1},
			},
			capacity: map[string]int32{"node-a": 1, "node-b": 1, "node-c": 1, "node-d": 1, "node-y": 1, "node-z": 1},
			failures: [][]string{{"node-b", "node-d"}, {"node-d"}},
			want: [][]tas.TopologyDomainAssignment{
				{
					{Values: []string{"node-a"}, Count: 1},
					{Values: []string{"node-y"}, Count: 1},
					{Values: []string{"node-c"}, Count: 1},
					{Values: []string{"node-d"}, Count: 1},
				},
				{
					{Values: []string{"node-a"}, Count: 1},
					{Values: []string{"node-y"}, Count: 1},
					{Values: []string{"node-c"}, Count: 1},
					{Values: []string{"node-z"}, Count: 1},
				},
			},
		},
		"reusing a surviving node does not move its existing rank": {
			original: []tas.TopologyDomainAssignment{
				{Values: []string{"node-a"}, Count: 1},
				{Values: []string{"node-b"}, Count: 2},
				{Values: []string{"node-c"}, Count: 1},
			},
			capacity: map[string]int32{"node-a": 1, "node-b": 2, "node-c": 2, "node-z": 1},
			failures: [][]string{{"node-b"}},
			want: [][]tas.TopologyDomainAssignment{{
				{Values: []string{"node-a"}, Count: 1},
				{Values: []string{"node-c"}, Count: 1},
				{Values: []string{"node-z"}, Count: 1},
				{Values: []string{"node-c"}, Count: 1},
			}},
		},
		"partial slice remains at the tail even when its node also holds an earlier slice": {
			original: []tas.TopologyDomainAssignment{
				{Values: []string{"node-b"}, Count: 2},
				{Values: []string{"node-a"}, Count: 2},
				{Values: []string{"node-b"}, Count: 1},
			},
			capacity:  map[string]int32{"node-a": 2, "node-b": 3, "node-z": 2},
			failures:  [][]string{{"node-a"}},
			sliceSize: 2,
			want: [][]tas.TopologyDomainAssignment{{
				{Values: []string{"node-b"}, Count: 2},
				{Values: []string{"node-z"}, Count: 2},
				{Values: []string{"node-b"}, Count: 1},
			}},
		},
		"replacement rejects a split rack slice": {
			original: []tas.TopologyDomainAssignment{
				{Values: []string{"node-f"}, Count: 1},
				{Values: []string{"node-a"}, Count: 1},
				{Values: []string{"node-f"}, Count: 1},
				{Values: []string{"node-b"}, Count: 1},
			},
			capacity:  map[string]int32{"node-f": 2, "node-a": 1, "node-b": 1, "node-c": 2},
			nodeRacks: map[string]string{"node-f": "r1", "node-a": "r1", "node-b": "r1", "node-c": "r2"},
			failures:  [][]string{{"node-f"}},
			constraints: []kueue.PodsetSliceRequiredTopologyConstraint{
				{Topology: rackLabel, Size: 2},
			},
			wantFailure: "cannot replace the node node-f without splitting a PodSet slice",
		},
		"outer zone alignment cannot hide a split inner rack slice": {
			original: []tas.TopologyDomainAssignment{
				{Values: []string{"node-f"}, Count: 1},
				{Values: []string{"node-a"}, Count: 1},
				{Values: []string{"node-f"}, Count: 1},
				{Values: []string{"node-b"}, Count: 1},
			},
			capacity:  map[string]int32{"node-f": 2, "node-a": 1, "node-b": 1, "node-c": 2},
			nodeRacks: map[string]string{"node-f": "r1", "node-a": "r1", "node-b": "r1", "node-c": "r2"},
			failures:  [][]string{{"node-f"}},
			constraints: []kueue.PodsetSliceRequiredTopologyConstraint{
				{Topology: zoneLabel, Size: 4},
				{Topology: rackLabel, Size: 2},
			},
			wantFailure: "cannot replace the node node-f without splitting a PodSet slice",
		},
		"multilayer replacement keeps complete rack slices in the zone": {
			original: []tas.TopologyDomainAssignment{
				{Values: []string{"node-f"}, Count: 2},
				{Values: []string{"node-a"}, Count: 2},
			},
			capacity:  map[string]int32{"node-f": 2, "node-a": 2, "node-c": 2},
			nodeRacks: map[string]string{"node-f": "r1", "node-a": "r1", "node-c": "r2"},
			failures:  [][]string{{"node-f"}},
			constraints: []kueue.PodsetSliceRequiredTopologyConstraint{
				{Topology: zoneLabel, Size: 4},
				{Topology: rackLabel, Size: 2},
			},
			want: [][]tas.TopologyDomainAssignment{{
				{Values: []string{"node-c"}, Count: 2},
				{Values: []string{"node-a"}, Count: 2},
			}},
		},
		"multilayer replacement can use consecutive nodes in the same rack": {
			original: []tas.TopologyDomainAssignment{
				{Values: []string{"node-f"}, Count: 1},
				{Values: []string{"node-a"}, Count: 1},
				{Values: []string{"node-f"}, Count: 1},
				{Values: []string{"node-b"}, Count: 1},
			},
			capacity:  map[string]int32{"node-f": 2, "node-a": 1, "node-b": 1, "node-c": 2},
			nodeRacks: map[string]string{"node-f": "r1", "node-a": "r1", "node-b": "r1", "node-c": "r1"},
			failures:  [][]string{{"node-f"}},
			constraints: []kueue.PodsetSliceRequiredTopologyConstraint{
				{Topology: zoneLabel, Size: 4},
				{Topology: rackLabel, Size: 2},
			},
			want: [][]tas.TopologyDomainAssignment{{
				{Values: []string{"node-c"}, Count: 1},
				{Values: []string{"node-a"}, Count: 1},
				{Values: []string{"node-c"}, Count: 1},
				{Values: []string{"node-b"}, Count: 1},
			}},
		},
		"grouped repair keeps the sibling assignment and surviving worker ranks": {
			original: []tas.TopologyDomainAssignment{
				{Values: []string{"node-a"}, Count: 1},
				{Values: []string{"node-b"}, Count: 1},
				{Values: []string{"node-c"}, Count: 1},
			},
			capacity: map[string]int32{"node-a": 2, "node-b": 1, "node-c": 1, "node-z": 1},
			failures: [][]string{{"node-b"}},
			grouped:  true,
			want: [][]tas.TopologyDomainAssignment{{
				{Values: []string{"node-a"}, Count: 1},
				{Values: []string{"node-z"}, Count: 1},
				{Values: []string{"node-c"}, Count: 1},
			}},
		},
		"a group with no healthy ranks is placed again with its admitted counts": {
			original: []tas.TopologyDomainAssignment{
				{Values: []string{"node-b"}, Count: 2},
			},
			capacity:   map[string]int32{"node-b": 3, "node-z": 3},
			failures:   [][]string{{"node-b"}},
			grouped:    true,
			leaderNode: "node-b",
			wantLeader: "node-z",
			want: [][]tas.TopologyDomainAssignment{{
				{Values: []string{"node-z"}, Count: 2},
			}},
		},
	}
	for name, tc := range cases {
		for _, prefixEncoding := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/prefixEncoding=%t", name, prefixEncoding), func(t *testing.T) {
				ctx, log := utiltesting.ContextWithLog(t)
				features.SetFeatureGateDuringTest(t, features.TASAssignmentsEncodingByHostnamePrefix, prefixEncoding)
				features.SetFeatureGateDuringTest(t, features.TASReplaceMultipleFailedNodes, !tc.singleFailed)
				features.SetFeatureGateDuringTest(t, features.TASPartialSlices, true)
				features.SetFeatureGateDuringTest(t, features.TASMultiLayerTopology, true)
				assignment := &tas.TopologyAssignment{Levels: []string{corev1.LabelHostname}, Domains: tc.original}
				count := tas.CountPodsInAssignment(assignment)
				ps := utiltestingapi.MakePodSet("workers", int(count)).UnconstrainedTopologyRequest()
				if !tc.nonIndexed {
					ps.PodIndexLabel(new(batchv1.JobCompletionIndexAnnotation))
				}
				if tc.sliceSize > 0 {
					ps.SliceRequiredTopologyRequest(corev1.LabelHostname).SliceSizeTopologyRequest(tc.sliceSize)
				}
				if len(tc.constraints) > 0 {
					ps.SliceRequiredTopologyConstraints(tc.constraints...)
				}
				leader := utiltestingapi.MakePodSet("leader", 1).
					UnconstrainedTopologyRequest().PodIndexLabel(new(batchv1.JobCompletionIndexAnnotation))
				leaderNode := tc.leaderNode
				if leaderNode == "" {
					leaderNode = "node-a"
				}
				leaderAssignment := &tas.TopologyAssignment{
					Levels: []string{corev1.LabelHostname},
					Domains: []tas.TopologyDomainAssignment{
						{Values: []string{leaderNode}, Count: 1},
					},
				}
				if tc.grouped {
					ps.TopologyRequest.PodSetGroupName = new("group")
					leader.TopologyRequest.PodSetGroupName = new("group")
				}
				failed := make(map[string]bool)
				for step, failures := range tc.failures {
					for _, node := range failures {
						failed[node] = true
					}
					var nodes []*corev1.Node
					var objects []client.Object
					for name, cpu := range tc.capacity {
						node := testingnode.MakeNode(name).Label(corev1.LabelHostname, name).
							StatusAllocatable(corev1.ResourceList{
								corev1.ResourceCPU:  resource.MustParse(fmt.Sprint(cpu)),
								corev1.ResourcePods: resource.MustParse("100"),
							})
						if tc.nodeRacks != nil {
							node.Label(zoneLabel, "z1").Label(rackLabel, tc.nodeRacks[name])
						}
						if failed[name] {
							node.NotReady()
						} else {
							node.Ready()
						}
						nodes = append(nodes, node.Obj())
						objects = append(objects, node.Obj())
					}
					builder := utiltesting.NewClientBuilder().WithObjects(objects...)
					if err := tasindexer.SetupIndexes(ctx, utiltesting.AsIndexer(builder)); err != nil {
						t.Fatal(err)
					}
					cache := NewTASCache(builder.Build(), resources.NewResourceFormatter())
					for _, node := range nodes {
						cache.SyncNode(node)
					}
					occupied := slices.Clone(assignment.Domains)
					if tc.grouped {
						occupied = append(occupied, leaderAssignment.Domains...)
					}
					for i, domain := range occupied {
						node := domain.Values[0]
						if !failed[node] {
							for j := range domain.Count {
								pod := testingpod.MakePod(fmt.Sprintf("pod-%d-%d", i, j), "ns").
									NodeName(node).Request(corev1.ResourceCPU, "1").Obj()
								cache.UpdateNonTASUsage(pod, log)
							}
						}
					}
					levels := assignment.Levels
					if tc.nodeRacks != nil {
						levels = []string{zoneLabel, rackLabel, corev1.LabelHostname}
					}
					fc := cache.NewTASFlavorCache(topologyInformation{Levels: levels}, flavorInformation{TopologyName: "default"})
					snapshot, err := fc.snapshot(ctx, log, newDefaultSimulator(), nil)
					if err != nil {
						t.Fatal(err)
					}
					encoded := tas.V1Beta2From(assignment)
					if prefixEncoding {
						// Also exercise reading an admission split over several API slices.
						encoded.Slices = nil
						for _, domain := range assignment.Domains {
							encoded.Slices = append(encoded.Slices, tas.V1Beta2From(&tas.TopologyAssignment{
								Levels: assignment.Levels, Domains: []tas.TopologyDomainAssignment{domain},
							}).Slices...)
						}
					}
					admission := utiltestingapi.MakeAdmission("cq").
						PodSets(utiltestingapi.MakePodSetAssignment("workers").Count(count).
							TopologyAssignment(encoded).Obj())
					wl := utiltestingapi.MakeWorkload("wl", "ns").PodSets(*ps.Obj()).UnhealthyNodes(failures...)
					requests := FlavorTASRequests{{
						PodSet: ps.Obj(), Count: count, PodSetGroupName: ps.TopologyRequest.PodSetGroupName,
						SinglePodRequests: resources.NewRequestsFromMap(map[corev1.ResourceName]int64{corev1.ResourceCPU: 1000}),
					}}
					if tc.grouped {
						admission.PodSets(admission.PodSetAssignments[0], utiltestingapi.MakePodSetAssignment("leader").Count(1).
							TopologyAssignment(tas.V1Beta2From(leaderAssignment)).Obj())
						wl.PodSets(*ps.Obj(), *leader.Obj())
						requests = append(requests, TASPodSetRequests{
							PodSet: leader.Obj(), Count: 1, PodSetGroupName: leader.TopologyRequest.PodSetGroupName,
							SinglePodRequests: resources.NewRequestsFromMap(map[corev1.ResourceName]int64{corev1.ResourceCPU: 1000}),
						})
					}
					wl.Admission(admission.Obj())
					original := wl.Obj().DeepCopy()
					got := snapshot.FindTopologyAssignmentsForFlavor(ctx, requests, WithWorkloadInfo(workload.NewInfo(log, wl.Obj())))
					if tc.wantFailure != "" {
						if got["workers"].FailureReason != tc.wantFailure {
							t.Fatalf("step %d: got failure %q, want %q", step, got["workers"].FailureReason, tc.wantFailure)
						}
						if got["workers"].TopologyAssignment != nil {
							t.Errorf("step %d: rejected replacement returned assignment: %v", step, got["workers"].TopologyAssignment)
						}
						if diff := cmp.Diff(original, wl.Obj()); diff != "" {
							t.Errorf("rejected replacement mutated the admitted workload (-want,+got):\n%s", diff)
						}
						continue
					}
					if got["workers"].FailureReason != "" {
						t.Fatalf("step %d: %s", step, got["workers"].FailureReason)
					}
					want := &tas.TopologyAssignment{Levels: assignment.Levels, Domains: tc.want[step]}
					if diff := cmp.Diff(want, got["workers"].TopologyAssignment); diff != "" {
						t.Fatalf("step %d: unexpected assignment (-want,+got):\n%s", step, diff)
					}
					assignment = tas.InternalFrom(tas.V1Beta2From(got["workers"].TopologyAssignment))
					if diff := cmp.Diff(want, assignment); diff != "" {
						t.Fatalf("step %d: encoding changed rank order (-want,+got):\n%s", step, diff)
					}
					if tc.grouped {
						wantLeader := leaderAssignment
						if tc.wantLeader != "" {
							wantLeader = &tas.TopologyAssignment{
								Levels: assignment.Levels,
								Domains: []tas.TopologyDomainAssignment{
									{Values: []string{tc.wantLeader}, Count: 1},
								},
							}
						}

						if diff := cmp.Diff(wantLeader, got["leader"].TopologyAssignment); diff != "" {
							t.Errorf("sibling assignment changed (-want,+got):\n%s", diff)
						}
					}
					if diff := cmp.Diff(original, wl.Obj()); diff != "" {
						t.Errorf("replacement mutated the admitted workload (-want,+got):\n%s", diff)
					}
				}
			})
		}
	}
}

// TestReplaceDomainInOrderRejectsInvalidCounts checks that malformed replacement
// counts cannot produce an assignment with missing or extra rank intervals.
func TestReplaceDomainInOrderRejectsInvalidCounts(t *testing.T) {
	cases := map[string]struct {
		count int32
		want  string
	}{
		"missing replacement slots": {
			count: 1,
			want:  "replacement assignment count 1 does not match unhealthy domain count 2",
		},
		"extra replacement slots": {
			count: 3,
			want:  "replacement assignment count 3 does not match unhealthy domain count 2",
		},
		"zero replacement count": {
			want: "replacement domain [rack node-z] has invalid count 0",
		},
		"negative replacement count": {
			count: -1,
			want:  "replacement domain [rack node-z] has invalid count -1",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			original := &tas.TopologyAssignment{
				Levels: []string{"rack", corev1.LabelHostname},
				Domains: []tas.TopologyDomainAssignment{
					{Values: []string{"rack", "node-b"}, Count: 2},
				},
			}
			replacement := &tas.TopologyAssignment{
				Levels: original.Levels,
				Domains: []tas.TopologyDomainAssignment{
					{Values: []string{"rack", "node-z"}, Count: tc.count},
				},
			}
			got, err := replaceDomainInOrder(original, replacement, "node-b")
			if err == nil || err.Error() != tc.want {
				t.Fatalf("got error %v, want %q", err, tc.want)
			}
			if got != nil {
				t.Errorf("invalid replacement returned assignment: %v", got)
			}
		})
	}
}
