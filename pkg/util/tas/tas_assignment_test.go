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

package tas

import (
	"fmt"
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
)

type testCase struct {
	name             string
	internal         *TopologyAssignment
	v1beta2          *kueue.TopologyAssignment
	podCounts        []int32
	totalDomainCount int
}

// bothWaysTestCases expect that internal <-> v1beta2 maps in both ways.
var bothWaysTestCases = []testCase{
	{
		name: "empty",
		internal: &TopologyAssignment{
			Levels: []string{"a", "b"},
		},
		v1beta2: &kueue.TopologyAssignment{
			Levels: []string{"a", "b"},
			Slices: []kueue.TopologyAssignmentSlice{},
		},
		podCounts:        []int32{},
		totalDomainCount: 0,
	},
	{
		name: "one domain",
		internal: &TopologyAssignment{
			Levels: []string{"a", "b"},
			Domains: []TopologyDomainAssignment{
				{
					Values: []string{"a1", "b1"},
					Count:  1,
				},
			},
		},
		v1beta2: &kueue.TopologyAssignment{
			Levels: []string{"a", "b"},
			Slices: []kueue.TopologyAssignmentSlice{
				{
					DomainCount: 1,
					PodCounts: kueue.TopologyAssignmentSlicePodCounts{
						Universal: new(int32(1)),
					},
					ValuesPerLevel: []kueue.TopologyAssignmentSliceLevelValues{
						{
							Universal: new("a1"),
						},
						{
							Universal: new("b1"),
						},
					},
				},
			},
		},
		podCounts:        []int32{1},
		totalDomainCount: 1,
	},
	{
		name: "multiple domains, same counts, no common prefix",
		internal: &TopologyAssignment{
			Levels: []string{"a", "b"},
			Domains: []TopologyDomainAssignment{
				{
					Values: []string{"a1", "b1"},
					Count:  1,
				},
				{
					Values: []string{"a2", "b2"},
					Count:  1,
				},
			},
		},
		v1beta2: &kueue.TopologyAssignment{
			Levels: []string{"a", "b"},
			Slices: []kueue.TopologyAssignmentSlice{
				{
					DomainCount: 2,
					PodCounts: kueue.TopologyAssignmentSlicePodCounts{
						Universal: new(int32(1)),
					},
					ValuesPerLevel: []kueue.TopologyAssignmentSliceLevelValues{
						{
							Individual: &kueue.TopologyAssignmentSliceLevelIndividualValues{
								Prefix: new("a"),
								Roots:  []string{"1", "2"},
							},
						},
						{
							Individual: &kueue.TopologyAssignmentSliceLevelIndividualValues{
								Prefix: new("b"),
								Roots:  []string{"1", "2"},
							},
						},
					},
				},
			},
		},
		podCounts:        []int32{1, 1},
		totalDomainCount: 2,
	},
	{
		name: "multiple domains, different counts, common prefix and suffix",
		internal: &TopologyAssignment{
			Levels: []string{"a", "b"},
			Domains: []TopologyDomainAssignment{
				{
					Values: []string{"p.a1.s", "p.b1.s"},
					Count:  1,
				},
				{
					Values: []string{"p.a2.s", "p.b2.s"},
					Count:  2,
				},
			},
		},
		v1beta2: &kueue.TopologyAssignment{
			Levels: []string{"a", "b"},
			Slices: []kueue.TopologyAssignmentSlice{
				{
					DomainCount: 2,
					PodCounts: kueue.TopologyAssignmentSlicePodCounts{
						Individual: []int32{1, 2},
					},
					ValuesPerLevel: []kueue.TopologyAssignmentSliceLevelValues{
						{
							Individual: &kueue.TopologyAssignmentSliceLevelIndividualValues{
								Prefix: new("p.a"),
								Suffix: new(".s"),
								Roots:  []string{"1", "2"},
							},
						},
						{
							Individual: &kueue.TopologyAssignmentSliceLevelIndividualValues{
								Prefix: new("p.b"),
								Suffix: new(".s"),
								Roots:  []string{"1", "2"},
							},
						},
					},
				},
			},
		},
		podCounts:        []int32{1, 2},
		totalDomainCount: 2,
	},
	{
		name: "multiple domains, universal value",
		internal: &TopologyAssignment{
			Levels: []string{"a", "b"},
			Domains: []TopologyDomainAssignment{
				{
					Values: []string{"a1", "x"},
					Count:  1,
				},
				{
					Values: []string{"a1", "y"},
					Count:  2,
				},
			},
		},
		v1beta2: &kueue.TopologyAssignment{
			Levels: []string{"a", "b"},
			Slices: []kueue.TopologyAssignmentSlice{
				{
					DomainCount: 2,
					PodCounts: kueue.TopologyAssignmentSlicePodCounts{
						Individual: []int32{1, 2},
					},
					ValuesPerLevel: []kueue.TopologyAssignmentSliceLevelValues{
						{
							Universal: new("a1"),
						},
						{
							Individual: &kueue.TopologyAssignmentSliceLevelIndividualValues{
								Roots: []string{"x", "y"},
							},
						},
					},
				},
			},
		},
		podCounts:        []int32{1, 2},
		totalDomainCount: 2,
	},
	{
		name: "multiple domains, same counts, shrinking prefix",
		internal: &TopologyAssignment{
			Levels: []string{"a"},
			Domains: []TopologyDomainAssignment{
				{
					Values: []string{"a123"},
					Count:  1,
				},
				{
					Values: []string{"a12"},
					Count:  1,
				},
				{
					Values: []string{"a1"},
					Count:  1,
				},
				{
					Values: []string{"a13"},
					Count:  1,
				},
			},
		},
		v1beta2: &kueue.TopologyAssignment{
			Levels: []string{"a"},
			Slices: []kueue.TopologyAssignmentSlice{
				{
					DomainCount: 4,
					PodCounts: kueue.TopologyAssignmentSlicePodCounts{
						Universal: new(int32(1)),
					},
					ValuesPerLevel: []kueue.TopologyAssignmentSliceLevelValues{
						{
							Individual: &kueue.TopologyAssignmentSliceLevelIndividualValues{
								Prefix: new("a1"),
								Roots:  []string{"23", "2", "", "3"},
							},
						},
					},
				},
			},
		},
		podCounts:        []int32{1, 1, 1, 1},
		totalDomainCount: 4,
	},
	{
		name: "multiple domains, same counts, shrinking suffix",
		internal: &TopologyAssignment{
			Levels: []string{"a"},
			Domains: []TopologyDomainAssignment{
				{
					Values: []string{"123b"},
					Count:  1,
				},
				{
					Values: []string{"23b"},
					Count:  1,
				},
				{
					Values: []string{"3b"},
					Count:  1,
				},
				{
					Values: []string{"13b"},
					Count:  1,
				},
			},
		},
		v1beta2: &kueue.TopologyAssignment{
			Levels: []string{"a"},
			Slices: []kueue.TopologyAssignmentSlice{
				{
					DomainCount: 4,
					PodCounts: kueue.TopologyAssignmentSlicePodCounts{
						Universal: new(int32(1)),
					},
					ValuesPerLevel: []kueue.TopologyAssignmentSliceLevelValues{
						{
							Individual: &kueue.TopologyAssignmentSliceLevelIndividualValues{
								Suffix: new("3b"),
								Roots:  []string{"12", "2", "", "1"},
							},
						},
					},
				},
			},
		},
		podCounts:        []int32{1, 1, 1, 1},
		totalDomainCount: 4,
	},
	{
		name: "multiple domains, same counts, max prefix & suffix overlap partially",
		internal: &TopologyAssignment{
			Levels: []string{"a"},
			Domains: []TopologyDomainAssignment{
				// Max prefix: "abaca"
				// Max suffix: "acada"
				// They overlap - we must shrink them so that they don't.
				// (The current implementation chooses to shrink the prefix: "abaca" -> "ab").
				{
					Values: []string{"abacacada"},
					Count:  1,
				},
				{
					Values: []string{"abacada"},
					Count:  1,
				},
			},
		},
		v1beta2: &kueue.TopologyAssignment{
			Levels: []string{"a"},
			Slices: []kueue.TopologyAssignmentSlice{
				{
					DomainCount: 2,
					PodCounts: kueue.TopologyAssignmentSlicePodCounts{
						Universal: new(int32(1)),
					},
					ValuesPerLevel: []kueue.TopologyAssignmentSliceLevelValues{
						{
							Individual: &kueue.TopologyAssignmentSliceLevelIndividualValues{
								Prefix: new("ab"),
								Suffix: new("acada"),
								Roots:  []string{"ac", ""},
							},
						},
					},
				},
			},
		},
		podCounts:        []int32{1, 1},
		totalDomainCount: 2,
	},
	{
		name: "multiple domains, same counts, max prefix & suffix overlap fully, shrinking strings",
		internal: &TopologyAssignment{
			Levels: []string{"a"},
			Domains: []TopologyDomainAssignment{
				{
					Values: []string{"ababa"},
					Count:  1,
				},
				{
					Values: []string{"aba"},
					Count:  1,
				},
			},
		},
		v1beta2: &kueue.TopologyAssignment{
			Levels: []string{"a"},
			Slices: []kueue.TopologyAssignmentSlice{
				{
					DomainCount: 2,
					PodCounts: kueue.TopologyAssignmentSlicePodCounts{
						Universal: new(int32(1)),
					},
					ValuesPerLevel: []kueue.TopologyAssignmentSliceLevelValues{
						// Prefix shrunk to "" -> it shouldn't be set at all.
						{
							Individual: &kueue.TopologyAssignmentSliceLevelIndividualValues{
								Suffix: new("aba"),
								Roots:  []string{"ab", ""},
							},
						},
					},
				},
			},
		},
		podCounts:        []int32{1, 1},
		totalDomainCount: 2,
	},
	{
		name: "multiple domains, same counts, max prefix & suffix overlap fully, growing strings",
		internal: &TopologyAssignment{
			Levels: []string{"a"},
			Domains: []TopologyDomainAssignment{
				{
					Values: []string{"aba"},
					Count:  1,
				},
				{
					Values: []string{"ababa"},
					Count:  1,
				},
			},
		},
		v1beta2: &kueue.TopologyAssignment{
			Levels: []string{"a"},
			Slices: []kueue.TopologyAssignmentSlice{
				{
					DomainCount: 2,
					PodCounts: kueue.TopologyAssignmentSlicePodCounts{
						Universal: new(int32(1)),
					},
					ValuesPerLevel: []kueue.TopologyAssignmentSliceLevelValues{
						// Prefix shrunk to "" -> it shouldn't be set at all.
						{
							Individual: &kueue.TopologyAssignmentSliceLevelIndividualValues{
								Suffix: new("aba"),
								Roots:  []string{"", "ab"},
							},
						},
					},
				},
			},
		},
		podCounts:        []int32{1, 1},
		totalDomainCount: 2,
	},
}

// oneWayTestCases expect that v1beta2 -> internal (via InternalFrom).
// (these v1beta2 values cannot be produced by V1Beta2From as of now - though this may change in the future)
var oneWayTestCases = []testCase{
	{
		name: "multiple slices, no prefixes/suffixes or universal values",
		internal: &TopologyAssignment{
			Levels: []string{"a", "b"},
			Domains: []TopologyDomainAssignment{
				{
					Values: []string{"a", "b"},
					Count:  1,
				},
				{
					Values: []string{"c", "d"},
					Count:  2,
				},
				{
					Values: []string{"e", "f"},
					Count:  3,
				},
			},
		},
		v1beta2: &kueue.TopologyAssignment{
			Levels: []string{"a", "b"},
			Slices: []kueue.TopologyAssignmentSlice{
				{
					DomainCount: 1,
					PodCounts: kueue.TopologyAssignmentSlicePodCounts{
						Individual: []int32{1},
					},
					ValuesPerLevel: []kueue.TopologyAssignmentSliceLevelValues{
						{
							Individual: &kueue.TopologyAssignmentSliceLevelIndividualValues{
								Roots: []string{"a"},
							},
						},
						{
							Individual: &kueue.TopologyAssignmentSliceLevelIndividualValues{
								Roots: []string{"b"},
							},
						},
					},
				},
				{
					DomainCount: 2,
					PodCounts: kueue.TopologyAssignmentSlicePodCounts{
						Individual: []int32{2, 3},
					},
					ValuesPerLevel: []kueue.TopologyAssignmentSliceLevelValues{
						{
							Individual: &kueue.TopologyAssignmentSliceLevelIndividualValues{
								Roots: []string{"c", "e"},
							},
						},
						{
							Individual: &kueue.TopologyAssignmentSliceLevelIndividualValues{
								Roots: []string{"d", "f"},
							},
						},
					},
				},
			},
		},
		podCounts:        []int32{1, 2, 3},
		totalDomainCount: 3,
	},
	{
		name: "multiple slices, prefixes, suffixes & universal values",
		internal: &TopologyAssignment{
			Levels: []string{"a", "b"},
			Domains: []TopologyDomainAssignment{
				{
					Values: []string{"a1", "b1-s"},
					Count:  2,
				},
				{
					Values: []string{"a1", "b2-s"},
					Count:  3,
				},
				{
					Values: []string{"a2", "x-t"},
					Count:  5,
				},
				{
					Values: []string{"a3", "y-t"},
					Count:  5,
				},
				{
					Values: []string{"a4", "z-t"},
					Count:  5,
				},
				{
					Values: []string{"a10", "b10"},
					Count:  10,
				},
				{
					Values: []string{"a10", "b10"},
					Count:  10,
				},
			},
		},
		v1beta2: &kueue.TopologyAssignment{
			Levels: []string{"a", "b"},
			Slices: []kueue.TopologyAssignmentSlice{
				{
					DomainCount: 2,
					PodCounts: kueue.TopologyAssignmentSlicePodCounts{
						Individual: []int32{2, 3},
					},
					ValuesPerLevel: []kueue.TopologyAssignmentSliceLevelValues{
						{
							Universal: new("a1"),
						},
						{
							Individual: &kueue.TopologyAssignmentSliceLevelIndividualValues{
								Prefix: new("b"),
								Suffix: new("-s"),
								Roots:  []string{"1", "2"},
							},
						},
					},
				},
				{
					DomainCount: 3,
					PodCounts: kueue.TopologyAssignmentSlicePodCounts{
						Universal: new(int32(5)),
					},
					ValuesPerLevel: []kueue.TopologyAssignmentSliceLevelValues{
						{
							Individual: &kueue.TopologyAssignmentSliceLevelIndividualValues{
								Prefix: new("a"),
								Roots:  []string{"2", "3", "4"},
							},
						},
						{
							Individual: &kueue.TopologyAssignmentSliceLevelIndividualValues{
								Suffix: new("-t"),
								Roots:  []string{"x", "y", "z"},
							},
						},
					},
				},
				{
					DomainCount: 2,
					PodCounts: kueue.TopologyAssignmentSlicePodCounts{
						Universal: new(int32(10)),
					},
					ValuesPerLevel: []kueue.TopologyAssignmentSliceLevelValues{
						{
							Universal: new("a10"),
						},
						{
							Universal: new("b10"),
						},
					},
				},
			},
		},
		podCounts:        []int32{2, 3, 5, 5, 5, 10, 10},
		totalDomainCount: 7,
	},
}

var twoDomains = &kueue.TopologyAssignment{
	Levels: []string{"a"},
	Slices: []kueue.TopologyAssignmentSlice{
		{
			DomainCount: 2,
			PodCounts: kueue.TopologyAssignmentSlicePodCounts{
				Universal: new(int32(1)),
			},
			ValuesPerLevel: []kueue.TopologyAssignmentSliceLevelValues{
				{
					Universal: new("a1"),
				},
			},
		},
	},
}

func TestV1Beta2FromInternal(t *testing.T) {
	for _, tc := range bothWaysTestCases {
		t.Run(tc.name, func(t *testing.T) {
			got := V1Beta2From(tc.internal)
			if diff := cmp.Diff(tc.v1beta2, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("unexpected result (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestV1Beta2FromInternal_forNil(t *testing.T) {
	got := V1Beta2From(nil)
	if got != nil {
		t.Errorf("unexpected result for nil: %+v", got)
	}
}

func TestCompactTopologyAssignmentEncodingWithHostnamePrefixRuns(t *testing.T) {
	tests := map[string]struct {
		internal *TopologyAssignment
		want     *kueue.TopologyAssignment
	}{
		"GKE node pools": {
			internal: &TopologyAssignment{
				Levels: []string{corev1.LabelHostname},
				Domains: []TopologyDomainAssignment{
					{Values: []string{"gke-c-pool-a-hash1-aa"}, Count: 3},
					{Values: []string{"gke-c-pool-a-hash1-bb"}, Count: 3},
					{Values: []string{"gke-c-pool-b-hash2-cc"}, Count: 4},
					{Values: []string{"gke-c-pool-b-hash2-dd"}, Count: 5},
				},
			},
			want: &kueue.TopologyAssignment{
				Levels: []string{corev1.LabelHostname},
				Slices: []kueue.TopologyAssignmentSlice{
					{
						DomainCount: 2,
						PodCounts: kueue.TopologyAssignmentSlicePodCounts{
							Universal: ptr.To[int32](3),
						},
						ValuesPerLevel: []kueue.TopologyAssignmentSliceLevelValues{
							{
								Individual: &kueue.TopologyAssignmentSliceLevelIndividualValues{
									Prefix: new("gke-c-pool-a-hash1-"),
									Roots:  []string{"aa", "bb"},
								},
							},
						},
					},
					{
						DomainCount: 2,
						PodCounts: kueue.TopologyAssignmentSlicePodCounts{
							Individual: []int32{4, 5},
						},
						ValuesPerLevel: []kueue.TopologyAssignmentSliceLevelValues{
							{
								Individual: &kueue.TopologyAssignmentSliceLevelIndividualValues{
									Prefix: new("gke-c-pool-b-hash2-"),
									Roots:  []string{"cc", "dd"},
								},
							},
						},
					},
				},
			},
		},
		"AWS EKS private DNS names": {
			internal: &TopologyAssignment{
				Levels: []string{corev1.LabelHostname},
				Domains: []TopologyDomainAssignment{
					{Values: []string{"ip-10-24-34-0.us-west-2.compute.internal"}, Count: 2},
					{Values: []string{"ip-10-24-34-1.us-west-2.compute.internal"}, Count: 2},
					{Values: []string{"ip-10-24-35-0.us-west-2.compute.internal"}, Count: 3},
					{Values: []string{"ip-10-24-35-1.us-west-2.compute.internal"}, Count: 4},
				},
			},
			want: &kueue.TopologyAssignment{
				Levels: []string{corev1.LabelHostname},
				Slices: []kueue.TopologyAssignmentSlice{
					{
						DomainCount: 2,
						PodCounts: kueue.TopologyAssignmentSlicePodCounts{
							Universal: new(int32(2)),
						},
						ValuesPerLevel: []kueue.TopologyAssignmentSliceLevelValues{
							{
								Individual: &kueue.TopologyAssignmentSliceLevelIndividualValues{
									Prefix: new("ip-10-24-34-"),
									Suffix: new(".us-west-2.compute.internal"),
									Roots:  []string{"0", "1"},
								},
							},
						},
					},
					{
						DomainCount: 2,
						PodCounts: kueue.TopologyAssignmentSlicePodCounts{
							Individual: []int32{3, 4},
						},
						ValuesPerLevel: []kueue.TopologyAssignmentSliceLevelValues{
							{
								Individual: &kueue.TopologyAssignmentSliceLevelIndividualValues{
									Prefix: new("ip-10-24-35-"),
									Suffix: new(".us-west-2.compute.internal"),
									Roots:  []string{"0", "1"},
								},
							},
						},
					},
				},
			},
		},
		"Azure AKS VMSS node pools": {
			internal: &TopologyAssignment{
				Levels: []string{corev1.LabelHostname},
				Domains: []TopologyDomainAssignment{
					{Values: []string{"aks-nodepool1-12345678-vmss000000"}, Count: 1},
					{Values: []string{"aks-nodepool1-12345678-vmss000001"}, Count: 1},
					{Values: []string{"aks-nodepool2-87654321-vmss000000"}, Count: 2},
					{Values: []string{"aks-nodepool2-87654321-vmss000001"}, Count: 3},
				},
			},
			want: &kueue.TopologyAssignment{
				Levels: []string{corev1.LabelHostname},
				Slices: []kueue.TopologyAssignmentSlice{
					{
						DomainCount: 2,
						PodCounts: kueue.TopologyAssignmentSlicePodCounts{
							Universal: new(int32(1)),
						},
						ValuesPerLevel: []kueue.TopologyAssignmentSliceLevelValues{
							{
								Individual: &kueue.TopologyAssignmentSliceLevelIndividualValues{
									Prefix: new("aks-nodepool1-12345678-vmss00000"),
									Roots:  []string{"0", "1"},
								},
							},
						},
					},
					{
						DomainCount: 2,
						PodCounts: kueue.TopologyAssignmentSlicePodCounts{
							Individual: []int32{2, 3},
						},
						ValuesPerLevel: []kueue.TopologyAssignmentSliceLevelValues{
							{
								Individual: &kueue.TopologyAssignmentSliceLevelIndividualValues{
									Prefix: new("aks-nodepool2-87654321-vmss00000"),
									Roots:  []string{"0", "1"},
								},
							},
						},
					},
				},
			},
		},
		"on-premises UUID node names": {
			internal: &TopologyAssignment{
				Levels: []string{corev1.LabelHostname},
				Domains: []TopologyDomainAssignment{
					{Values: []string{"550e8400-e29b-41d4-a716-446655440000"}, Count: 1},
					{Values: []string{"6ba7b810-9dad-11d1-80b4-00c04fd430c8"}, Count: 2},
					{Values: []string{"f47ac10b-58cc-4372-a567-0e02b2c3d479"}, Count: 3},
				},
			},
			want: &kueue.TopologyAssignment{
				Levels: []string{corev1.LabelHostname},
				Slices: []kueue.TopologyAssignmentSlice{
					{
						DomainCount: 3,
						PodCounts: kueue.TopologyAssignmentSlicePodCounts{
							Individual: []int32{1, 2, 3},
						},
						ValuesPerLevel: []kueue.TopologyAssignmentSliceLevelValues{
							{
								Individual: &kueue.TopologyAssignmentSliceLevelIndividualValues{
									Roots: []string{
										"550e8400-e29b-41d4-a716-446655440000",
										"6ba7b810-9dad-11d1-80b4-00c04fd430c8",
										"f47ac10b-58cc-4372-a567-0e02b2c3d479",
									},
								},
							},
						},
					},
				},
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := compactTopologyAssignmentEncodingWithHostnamePrefixRuns(tc.internal)
			if diff := cmp.Diff(tc.want, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("unexpected result (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestCompactTopologyAssignmentEncodingWithHostnamePrefixRuns_withoutHostnameLevel(t *testing.T) {
	internal := &TopologyAssignment{
		Levels: []string{"cloud.example.com/rack"},
		Domains: []TopologyDomainAssignment{
			{Values: []string{"rack-a"}, Count: 1},
			{Values: []string{"rack-b"}, Count: 2},
		},
	}

	got := compactTopologyAssignmentEncodingWithHostnamePrefixRuns(internal)
	if gotSlices := len(got.Slices); gotSlices != 1 {
		t.Fatalf("unexpected number of slices: got %d, want 1", gotSlices)
	}
	if diff := cmp.Diff(internal, InternalFrom(got), cmpopts.EquateEmpty()); diff != "" {
		t.Errorf("unexpected round trip (-want,+got):\n%s", diff)
	}
}

func TestV1Beta2FromInternal_hostnamePrefixFeatureGateAndFallback(t *testing.T) {
	tests := map[string]struct {
		enabled                    bool
		domainCount                int
		wantHostnamePrefixEncoding bool
	}{
		"enabled keeps one-slice hostname-prefix encoding": {
			enabled:                    true,
			domainCount:                1,
			wantHostnamePrefixEncoding: true,
		},
		"enabled uses smaller single-slice encoding": {
			enabled:     true,
			domainCount: 4,
		},
		"enabled uses smaller hostname-prefix encoding": {
			enabled:                    true,
			domainCount:                20,
			wantHostnamePrefixEncoding: true,
		},
		"disabled uses single-slice encoding": {
			enabled:     false,
			domainCount: 20,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.TASAssignmentsEncodingByHostnamePrefix, tc.enabled)
			internal := hostnamePrefixAssignment(tc.domainCount)

			want := singleCompactTopologyAssignmentEncoding(internal)
			if tc.wantHostnamePrefixEncoding {
				want = compactTopologyAssignmentEncodingWithHostnamePrefixRuns(internal)
			}
			got := V1Beta2From(internal)
			if diff := cmp.Diff(want, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("unexpected result (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(internal, InternalFrom(got), cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("unexpected round trip (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestCompactTopologyAssignmentEncodingWithHostnamePrefixRuns_preservesDomainOrder(t *testing.T) {
	internal := &TopologyAssignment{
		Levels: []string{corev1.LabelHostname},
		Domains: []TopologyDomainAssignment{
			{Values: []string{"gke-c-pool-a-hash1-aa"}, Count: 1},
			{Values: []string{"gke-c-pool-b-hash2-cc"}, Count: 2},
			{Values: []string{"gke-c-pool-a-hash1-bb"}, Count: 3},
			{Values: []string{"gke-c-pool-b-hash2-dd"}, Count: 4},
		},
	}

	encoded := compactTopologyAssignmentEncodingWithHostnamePrefixRuns(internal)
	if gotSlices := len(encoded.Slices); gotSlices != 4 {
		t.Fatalf("unexpected number of slices: got %d, want 4", gotSlices)
	}
	got := InternalFrom(encoded)
	if diff := cmp.Diff(internal.Domains, got.Domains); diff != "" {
		t.Fatalf("hostname-prefix encoding must preserve domain order (-want,+got):\n%s", diff)
	}
}

func TestV1Beta2FromInternal_largeHostnameAssignmentWithManyPools(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.TASAssignmentsEncodingByHostnamePrefix, true)
	const (
		poolCount    = 2_000
		nodesPerPool = 100
		domainCount  = poolCount * nodesPerPool
	)

	internal := &TopologyAssignment{
		Levels:  []string{corev1.LabelHostname},
		Domains: make([]TopologyDomainAssignment, domainCount),
	}
	for poolID := range poolCount {
		for nodeID := range nodesPerPool {
			i := poolID*nodesPerPool + nodeID
			internal.Domains[i] = TopologyDomainAssignment{
				Values: []string{fmt.Sprintf("gke-cluster-pool-%04d-%08x-%04x", poolID, poolID, nodeID)},
				Count:  1,
			}
		}
	}

	got := V1Beta2From(internal)
	if len(got.Slices) != 2 {
		t.Fatalf("unexpected number of slices: got %d, want 2", len(got.Slices))
	}
	for i, slice := range got.Slices {
		if slice.DomainCount != maxDomainsPerTopologyAssignmentSlice {
			t.Errorf("unexpected domain count for slice %d: got %d, want %d", i, slice.DomainCount, maxDomainsPerTopologyAssignmentSlice)
		}
	}
	if got := TotalDomainCount(got); got != domainCount {
		t.Errorf("unexpected total domain count: got %d, want %d", got, domainCount)
	}
}

func TestReusableHostnamePrefixKeys(t *testing.T) {
	tests := map[string]struct {
		domains []TopologyDomainAssignment
		want    []string
	}{
		"longest reusable prefixes": {
			domains: []TopologyDomainAssignment{
				{Values: []string{"gke-c-pool-a-hash1-aa"}},
				{Values: []string{"gke-c-pool-a-hash1-bb"}},
				{Values: []string{"gke-c-pool-b-hash2-cc"}},
				{Values: []string{"gke-c-pool-b-hash2-dd"}},
			},
			want: []string{
				"gke-c-pool-a-hash1-",
				"gke-c-pool-a-hash1-",
				"gke-c-pool-b-hash2-",
				"gke-c-pool-b-hash2-",
			},
		},
		"no reusable prefix": {
			domains: []TopologyDomainAssignment{
				{Values: []string{"node-a"}},
				{Values: []string{"host-b"}},
			},
			want: []string{"", ""},
		},
		"no delimiter": {
			domains: []TopologyDomainAssignment{
				{Values: []string{"nodea"}},
				{Values: []string{"hostb"}},
			},
			want: nil,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := reusableHostnamePrefixKeys([]string{corev1.LabelHostname}, tc.domains)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("unexpected prefix keys (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestReusableHostnamePrefixKeys_backsOffToFitSliceLimit(t *testing.T) {
	domains := make([]TopologyDomainAssignment, maxTopologyAssignmentSlices+1)
	want := make([]string, len(domains))
	for i := range domains {
		group := "x"
		if i%2 == 1 {
			group = "y"
		}
		domains[i].Values = []string{fmt.Sprintf("a-%s-%04d", group, i)}
		want[i] = "a-"
	}

	got := reusableHostnamePrefixKeys([]string{corev1.LabelHostname}, domains)
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("unexpected prefix keys after backoff (-want,+got):\n%s", diff)
	}
}

func TestCompactTopologyAssignmentEncodingWithHostnamePrefixRuns_fallsBackWhenNoPrefixDepthFitsSliceLimit(t *testing.T) {
	internal := &TopologyAssignment{
		Levels:  []string{corev1.LabelHostname},
		Domains: make([]TopologyDomainAssignment, maxTopologyAssignmentSlices+1),
	}
	for i := range internal.Domains {
		prefix := "a-x"
		if i%2 == 1 {
			prefix = "b-y"
		}
		internal.Domains[i] = TopologyDomainAssignment{
			Values: []string{fmt.Sprintf("%s-%04d", prefix, i)},
			Count:  1,
		}
	}

	if got := reusableHostnamePrefixKeys(internal.Levels, internal.Domains); got != nil {
		t.Errorf("expected no usable prefix depth, got %v", got)
	}

	got := compactTopologyAssignmentEncodingWithHostnamePrefixRuns(internal)
	if gotSlices := len(got.Slices); gotSlices != 1 {
		t.Fatalf("unexpected number of slices: got %d, want 1", gotSlices)
	}
	if diff := cmp.Diff(internal, InternalFrom(got), cmpopts.EquateEmpty()); diff != "" {
		t.Errorf("unexpected round trip (-want,+got):\n%s", diff)
	}
}

func TestCompactDomainRuns(t *testing.T) {
	domains := make([]TopologyDomainAssignment, 4)
	for i := range domains {
		domains[i] = TopologyDomainAssignment{
			Values: []string{fmt.Sprintf("node-%04d", i)},
			Count:  1,
		}
	}

	tests := map[string]struct {
		domains    []TopologyDomainAssignment
		prefixKeys []string
		want       []int
	}{
		"empty": {
			want: nil,
		},
		"no prefix keys": {
			domains: domains,
			want:    []int{4},
		},
		"groups consecutive domains": {
			domains:    domains,
			prefixKeys: []string{"prefix-a-", "prefix-a-", "prefix-b-", "prefix-a-"},
			want:       []int{2, 1, 1},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			runs := compactDomainRuns(tc.domains, tc.prefixKeys)
			got := make([]int, len(runs))
			for i := range runs {
				got[i] = len(runs[i])
			}
			if diff := cmp.Diff(tc.want, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("unexpected run lengths (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestChunkCount(t *testing.T) {
	tests := map[string]struct {
		length    int
		chunkSize int
		want      int
	}{
		"empty":         {length: 0, chunkSize: 100, want: 0},
		"partial chunk": {length: 1, chunkSize: 100, want: 1},
		"exact chunk":   {length: 100, chunkSize: 100, want: 1},
		"two chunks":    {length: 101, chunkSize: 100, want: 2},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if got := chunkCount(tc.length, tc.chunkSize); got != tc.want {
				t.Errorf("unexpected chunk count: got %d, want %d", got, tc.want)
			}
		})
	}
}

func hostnamePrefixAssignment(domainCount int) *TopologyAssignment {
	domainsPerPrefix := domainCount / 2
	internal := &TopologyAssignment{
		Levels:  []string{corev1.LabelHostname},
		Domains: make([]TopologyDomainAssignment, domainCount),
	}
	for i := range domainCount {
		prefix := "a"
		nodeID := i
		if i >= domainsPerPrefix {
			prefix = "b"
			nodeID -= domainsPerPrefix
		}
		internal.Domains[i] = TopologyDomainAssignment{
			Values: []string{fmt.Sprintf("pool-%s-node-%05d", prefix, nodeID)},
			Count:  1,
		}
	}
	return internal
}

func TestInternalFromV1Beta2(t *testing.T) {
	for _, tc := range slices.Concat(bothWaysTestCases, oneWayTestCases) {
		t.Run(tc.name, func(t *testing.T) {
			got := InternalFrom(tc.v1beta2)
			if diff := cmp.Diff(tc.internal, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("unexpected result (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestInternalFromV1Beta2_forNil(t *testing.T) {
	got := InternalFrom(nil)
	if got != nil {
		t.Errorf("unexpected result for nil: %+v", got)
	}
}

func TestInternalSeqFromV1Beta2(t *testing.T) {
	for _, tc := range slices.Concat(bothWaysTestCases, oneWayTestCases) {
		t.Run(tc.name, func(t *testing.T) {
			seq := InternalSeqFrom(tc.v1beta2)
			got := slices.Collect(seq)
			want := tc.internal.Domains
			if diff := cmp.Diff(want, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("unexpected result (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestInternalSeqFromV1Beta2_forNil(t *testing.T) {
	got := InternalSeqFrom(nil)
	if got != nil {
		t.Errorf("unexpected result for nil: %+v", got)
	}
}

func TestInternalSeqFromV1Beta2_iteratorStops(t *testing.T) {
	for range InternalSeqFrom(twoDomains) {
		// Break the loop prematurely.
		// If the iterator isn't smart enough to stop, this will panic.
		break
	}
}

func TestPodCounts(t *testing.T) {
	for _, tc := range slices.Concat(bothWaysTestCases, oneWayTestCases) {
		t.Run(tc.name, func(t *testing.T) {
			seq := PodCounts(tc.v1beta2)
			got := slices.Collect(seq)
			want := tc.podCounts
			if diff := cmp.Diff(want, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("unexpected result (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestPodCounts_forNil(t *testing.T) {
	got := PodCounts(nil)
	if got != nil {
		t.Errorf("unexpected result for nil: %+v", got)
	}
}

func TestPodCounts_iteratorStops(t *testing.T) {
	for range PodCounts(twoDomains) {
		// Break the loop prematurely.
		// If the iterator isn't smart enough to stop, this will panic.
		break
	}
}

func TestTotalDomainCount(t *testing.T) {
	for _, tc := range slices.Concat(bothWaysTestCases, oneWayTestCases) {
		t.Run(tc.name, func(t *testing.T) {
			got := TotalDomainCount(tc.v1beta2)
			if diff := cmp.Diff(tc.totalDomainCount, got); diff != "" {
				t.Errorf("unexpected result (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestTotalDomainCount_forNil(t *testing.T) {
	got := TotalDomainCount(nil)
	if got != 0 {
		t.Errorf("unexpected result for nil: %d", got)
	}
}

func TestLowestLevel(t *testing.T) {
	v1beta2 := &kueue.TopologyAssignment{
		Levels: []string{"a", "b"},
		Slices: []kueue.TopologyAssignmentSlice{
			{
				DomainCount: 2,
				PodCounts: kueue.TopologyAssignmentSlicePodCounts{
					Individual: []int32{2, 3},
				},
				ValuesPerLevel: []kueue.TopologyAssignmentSliceLevelValues{
					{
						Universal: new("a1"),
					},
					{
						Individual: &kueue.TopologyAssignmentSliceLevelIndividualValues{
							Prefix: new("b"),
							Suffix: new("-s"),
							Roots:  []string{"1", "2"},
						},
					},
				},
			},
			{
				DomainCount: 3,
				PodCounts: kueue.TopologyAssignmentSlicePodCounts{
					Universal: new(int32(5)),
				},
				ValuesPerLevel: []kueue.TopologyAssignmentSliceLevelValues{
					{
						Individual: &kueue.TopologyAssignmentSliceLevelIndividualValues{
							Prefix: new("a"),
							Roots:  []string{"2", "3", "4"},
						},
					},
					{
						Individual: &kueue.TopologyAssignmentSliceLevelIndividualValues{
							Suffix: new("-t"),
							Roots:  []string{"x", "y", "z"},
						},
					},
				},
			},
			{
				DomainCount: 2,
				PodCounts: kueue.TopologyAssignmentSlicePodCounts{
					Universal: new(int32(10)),
				},
				ValuesPerLevel: []kueue.TopologyAssignmentSliceLevelValues{
					{
						Universal: new("a10"),
					},
					{
						Universal: new("b10"),
					},
				},
			},
		},
	}
	want := []string{"b1-s", "b2-s", "x-t", "y-t", "z-t", "b10", "b10"}
	seq := LowestLevelValues(v1beta2)
	got := slices.Collect(seq)
	if diff := cmp.Diff(want, got, cmpopts.EquateEmpty()); diff != "" {
		t.Errorf("unexpected result (-want,+got):\n%s", diff)
	}
}

func TestLowestLevelValues_forNil(t *testing.T) {
	got := LowestLevelValues(nil)
	if got != nil {
		t.Errorf("unexpected result for nil: %+v", got)
	}
}

func TestLowestLevelValues_iteratorStops(t *testing.T) {
	for range LowestLevelValues(twoDomains) {
		// Break the loop prematurely.
		// If the iterator isn't smart enough to stop, this will panic.
		break
	}
}

func TestHasNodeInPodSetAssignment(t *testing.T) {
	testCases := []struct {
		name     string
		psa      *kueue.PodSetAssignment
		nodeName string
		want     bool
	}{
		{
			name:     "nil assignment",
			psa:      nil,
			nodeName: "node1",
			want:     false,
		},
		{
			name:     "no topology assignment",
			psa:      &kueue.PodSetAssignment{Name: "ps1"},
			nodeName: "node1",
			want:     false,
		},
		{
			name: "topology level is not hostname",
			psa: &kueue.PodSetAssignment{
				Name: "ps1",
				TopologyAssignment: V1Beta2From(&TopologyAssignment{
					Levels: []string{"example.com/rack"},
					Domains: []TopologyDomainAssignment{
						{Values: []string{"rack1"}, Count: 1},
					},
				}),
			},
			nodeName: "node1",
			want:     false,
		},
		{
			name: "pod on target node",
			psa: &kueue.PodSetAssignment{
				Name: "ps1",
				TopologyAssignment: V1Beta2From(&TopologyAssignment{
					Levels: []string{corev1.LabelHostname},
					Domains: []TopologyDomainAssignment{
						{Values: []string{"node1"}, Count: 1},
					},
				}),
			},
			nodeName: "node1",
			want:     true,
		},
		{
			name: "target node with zero pods",
			psa: &kueue.PodSetAssignment{
				Name: "ps1",
				TopologyAssignment: V1Beta2From(&TopologyAssignment{
					Levels: []string{corev1.LabelHostname},
					Domains: []TopologyDomainAssignment{
						{Values: []string{"node1"}, Count: 0},
					},
				}),
			},
			nodeName: "node1",
			want:     false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := HasNodeInPodSetAssignment(tc.psa, tc.nodeName)
			if got != tc.want {
				t.Errorf("HasNodeInPodSetAssignment() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestHasTASAssignmentOnNode(t *testing.T) {
	testCases := []struct {
		name      string
		admission *kueue.Admission
		nodeName  string
		want      bool
	}{
		{
			name:      "nil admission",
			admission: nil,
			nodeName:  "node1",
			want:      false,
		},
		{
			name:      "empty pod set assignments",
			admission: &kueue.Admission{},
			nodeName:  "node1",
			want:      false,
		},
		{
			name: "no topology assignment",
			admission: &kueue.Admission{
				PodSetAssignments: []kueue.PodSetAssignment{
					{
						Name: "ps1",
					},
				},
			},
			nodeName: "node1",
			want:     false,
		},
		{
			name: "topology level is not hostname",
			admission: &kueue.Admission{
				PodSetAssignments: []kueue.PodSetAssignment{
					{
						Name: "ps1",
						TopologyAssignment: &kueue.TopologyAssignment{
							Levels: []string{"example.com/rack"},
							Slices: []kueue.TopologyAssignmentSlice{
								{
									DomainCount: 1,
									PodCounts: kueue.TopologyAssignmentSlicePodCounts{
										Universal: ptr.To[int32](1),
									},
									ValuesPerLevel: []kueue.TopologyAssignmentSliceLevelValues{
										{
											Universal: new("rack1"),
										},
									},
								},
							},
						},
					},
				},
			},
			nodeName: "node1",
			want:     false,
		},
		{
			name: "pod on target node",
			admission: &kueue.Admission{
				PodSetAssignments: []kueue.PodSetAssignment{
					{
						Name: "ps1",
						TopologyAssignment: &kueue.TopologyAssignment{
							Levels: []string{corev1.LabelHostname},
							Slices: []kueue.TopologyAssignmentSlice{
								{
									DomainCount: 1,
									PodCounts: kueue.TopologyAssignmentSlicePodCounts{
										Universal: ptr.To[int32](1),
									},
									ValuesPerLevel: []kueue.TopologyAssignmentSliceLevelValues{
										{
											Universal: new("node1"),
										},
									},
								},
							},
						},
					},
				},
			},
			nodeName: "node1",
			want:     true,
		},
		{
			name: "pod on different node",
			admission: &kueue.Admission{
				PodSetAssignments: []kueue.PodSetAssignment{
					{
						Name: "ps1",
						TopologyAssignment: &kueue.TopologyAssignment{
							Levels: []string{corev1.LabelHostname},
							Slices: []kueue.TopologyAssignmentSlice{
								{
									DomainCount: 1,
									PodCounts: kueue.TopologyAssignmentSlicePodCounts{
										Universal: ptr.To[int32](1),
									},
									ValuesPerLevel: []kueue.TopologyAssignmentSliceLevelValues{
										{
											Universal: new("node2"),
										},
									},
								},
							},
						},
					},
				},
			},
			nodeName: "node1",
			want:     false,
		},
		{
			name: "multiple domains, target node present in one",
			admission: &kueue.Admission{
				PodSetAssignments: []kueue.PodSetAssignment{
					{
						Name: "ps1",
						TopologyAssignment: &kueue.TopologyAssignment{
							Levels: []string{corev1.LabelHostname},
							Slices: []kueue.TopologyAssignmentSlice{
								{
									DomainCount: 2,
									PodCounts: kueue.TopologyAssignmentSlicePodCounts{
										Individual: []int32{2, 3},
									},
									ValuesPerLevel: []kueue.TopologyAssignmentSliceLevelValues{
										{
											Individual: &kueue.TopologyAssignmentSliceLevelIndividualValues{
												Roots: []string{"node1", "node2"},
											},
										},
									},
								},
							},
						},
					},
				},
			},
			nodeName: "node1",
			want:     true,
		},
		{
			name: "multiple pod sets, target node present in multiple",
			admission: &kueue.Admission{
				PodSetAssignments: []kueue.PodSetAssignment{
					{
						Name: "ps1",
						TopologyAssignment: &kueue.TopologyAssignment{
							Levels: []string{corev1.LabelHostname},
							Slices: []kueue.TopologyAssignmentSlice{
								{
									DomainCount: 1,
									PodCounts: kueue.TopologyAssignmentSlicePodCounts{
										Universal: ptr.To[int32](1),
									},
									ValuesPerLevel: []kueue.TopologyAssignmentSliceLevelValues{
										{
											Universal: new("node1"),
										},
									},
								},
							},
						},
					},
					{
						Name: "ps2",
						TopologyAssignment: &kueue.TopologyAssignment{
							Levels: []string{corev1.LabelHostname},
							Slices: []kueue.TopologyAssignmentSlice{
								{
									DomainCount: 1,
									PodCounts: kueue.TopologyAssignmentSlicePodCounts{
										Universal: ptr.To[int32](5),
									},
									ValuesPerLevel: []kueue.TopologyAssignmentSliceLevelValues{
										{
											Universal: new("node1"),
										},
									},
								},
							},
						},
					},
				},
			},
			nodeName: "node1",
			want:     true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := HasTASAssignmentOnNode(tc.admission, tc.nodeName)
			if got != tc.want {
				t.Errorf("HasTASAssignmentOnNode() = %v, want %v", got, tc.want)
			}
		})
	}
}
