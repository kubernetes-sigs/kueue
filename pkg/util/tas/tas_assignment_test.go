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
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"k8s.io/utils/ptr"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

type TestCase struct {
	name             string
	internal         *TopologyAssignment
	v1beta2          *kueue.TopologyAssignment
	podCounts        []int32
	totalDomainCount int
}

// bothWaysTestCases expect that internal <-> v1beta2 maps in both ways.
var bothWaysTestCases = []TestCase{
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
						Universal: ptr.To(int32(1)),
					},
					ValuesPerLevel: []kueue.TopologyAssignmentSliceLevelValues{
						{
							Universal: ptr.To("a1"),
						},
						{
							Universal: ptr.To("b1"),
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
						Universal: ptr.To(int32(1)),
					},
					ValuesPerLevel: []kueue.TopologyAssignmentSliceLevelValues{
						{
							Individual: &kueue.TopologyAssignmentSliceLevelIndividualValues{
								CommonPrefix: ptr.To("a"),
								Roots:        []string{"1", "2"},
							},
						},
						{
							Individual: &kueue.TopologyAssignmentSliceLevelIndividualValues{
								CommonPrefix: ptr.To("b"),
								Roots:        []string{"1", "2"},
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
								CommonPrefix: ptr.To("p.a"),
								CommonSuffix: ptr.To(".s"),
								Roots:        []string{"1", "2"},
							},
						},
						{
							Individual: &kueue.TopologyAssignmentSliceLevelIndividualValues{
								CommonPrefix: ptr.To("p.b"),
								CommonSuffix: ptr.To(".s"),
								Roots:        []string{"1", "2"},
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
							Universal: ptr.To("a1"),
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
						Universal: ptr.To(int32(1)),
					},
					ValuesPerLevel: []kueue.TopologyAssignmentSliceLevelValues{
						{
							Individual: &kueue.TopologyAssignmentSliceLevelIndividualValues{
								CommonPrefix: ptr.To("a1"),
								Roots:        []string{"23", "2", "", "3"},
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
						Universal: ptr.To(int32(1)),
					},
					ValuesPerLevel: []kueue.TopologyAssignmentSliceLevelValues{
						{
							Individual: &kueue.TopologyAssignmentSliceLevelIndividualValues{
								CommonSuffix: ptr.To("3b"),
								Roots:        []string{"12", "2", "", "1"},
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
						Universal: ptr.To(int32(1)),
					},
					ValuesPerLevel: []kueue.TopologyAssignmentSliceLevelValues{
						{
							Individual: &kueue.TopologyAssignmentSliceLevelIndividualValues{
								CommonPrefix: ptr.To("ab"),
								CommonSuffix: ptr.To("acada"),
								Roots:        []string{"ac", ""},
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
						Universal: ptr.To(int32(1)),
					},
					ValuesPerLevel: []kueue.TopologyAssignmentSliceLevelValues{
						// Prefix shrunk to "" -> it shouldn't be set at all.
						{
							Individual: &kueue.TopologyAssignmentSliceLevelIndividualValues{
								CommonSuffix: ptr.To("aba"),
								Roots:        []string{"ab", ""},
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
						Universal: ptr.To(int32(1)),
					},
					ValuesPerLevel: []kueue.TopologyAssignmentSliceLevelValues{
						// Prefix shrunk to "" -> it shouldn't be set at all.
						{
							Individual: &kueue.TopologyAssignmentSliceLevelIndividualValues{
								CommonSuffix: ptr.To("aba"),
								Roots:        []string{"", "ab"},
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
var oneWayTestCases = []TestCase{
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
							Universal: ptr.To("a1"),
						},
						{
							Individual: &kueue.TopologyAssignmentSliceLevelIndividualValues{
								CommonPrefix: ptr.To("b"),
								CommonSuffix: ptr.To("-s"),
								Roots:        []string{"1", "2"},
							},
						},
					},
				},
				{
					DomainCount: 3,
					PodCounts: kueue.TopologyAssignmentSlicePodCounts{
						Universal: ptr.To(int32(5)),
					},
					ValuesPerLevel: []kueue.TopologyAssignmentSliceLevelValues{
						{
							Individual: &kueue.TopologyAssignmentSliceLevelIndividualValues{
								CommonPrefix: ptr.To("a"),
								Roots:        []string{"2", "3", "4"},
							},
						},
						{
							Individual: &kueue.TopologyAssignmentSliceLevelIndividualValues{
								CommonSuffix: ptr.To("-t"),
								Roots:        []string{"x", "y", "z"},
							},
						},
					},
				},
				{
					DomainCount: 2,
					PodCounts: kueue.TopologyAssignmentSlicePodCounts{
						Universal: ptr.To(int32(10)),
					},
					ValuesPerLevel: []kueue.TopologyAssignmentSliceLevelValues{
						{
							Universal: ptr.To("a10"),
						},
						{
							Universal: ptr.To("b10"),
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
				Universal: ptr.To(int32(1)),
			},
			ValuesPerLevel: []kueue.TopologyAssignmentSliceLevelValues{
				{
					Universal: ptr.To("a1"),
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

func TestInternalSeqFrmV1Beta2_iteratorStops(t *testing.T) {
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

func TestValuesAtLevel(t *testing.T) {
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
						Universal: ptr.To("a1"),
					},
					{
						Individual: &kueue.TopologyAssignmentSliceLevelIndividualValues{
							CommonPrefix: ptr.To("b"),
							CommonSuffix: ptr.To("-s"),
							Roots:        []string{"1", "2"},
						},
					},
				},
			},
			{
				DomainCount: 3,
				PodCounts: kueue.TopologyAssignmentSlicePodCounts{
					Universal: ptr.To(int32(5)),
				},
				ValuesPerLevel: []kueue.TopologyAssignmentSliceLevelValues{
					{
						Individual: &kueue.TopologyAssignmentSliceLevelIndividualValues{
							CommonPrefix: ptr.To("a"),
							Roots:        []string{"2", "3", "4"},
						},
					},
					{
						Individual: &kueue.TopologyAssignmentSliceLevelIndividualValues{
							CommonSuffix: ptr.To("-t"),
							Roots:        []string{"x", "y", "z"},
						},
					},
				},
			},
			{
				DomainCount: 2,
				PodCounts: kueue.TopologyAssignmentSlicePodCounts{
					Universal: ptr.To(int32(10)),
				},
				ValuesPerLevel: []kueue.TopologyAssignmentSliceLevelValues{
					{
						Universal: ptr.To("a10"),
					},
					{
						Universal: ptr.To("b10"),
					},
				},
			},
		},
	}
	testCases := []struct {
		name     string
		levelIdx int
		want     []string
	}{
		{
			name:     "at level 0",
			levelIdx: 0,
			want:     []string{"a1", "a1", "a2", "a3", "a4", "a10", "a10"},
		},
		{
			name:     "at level 1",
			levelIdx: 1,
			want:     []string{"b1-s", "b2-s", "x-t", "y-t", "z-t", "b10", "b10"},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			seq := ValuesAtLevel(v1beta2, tc.levelIdx)
			got := slices.Collect(seq)
			if diff := cmp.Diff(tc.want, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("unexpected result (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestValuesAtLevel_forNil(t *testing.T) {
	got := ValuesAtLevel(nil, 0)
	if got != nil {
		t.Errorf("unexpected result for nil: %+v", got)
	}
}

func TestValuesAtLevel_iteratorStops(t *testing.T) {
	for range ValuesAtLevel(twoDomains, 0) {
		// Break the loop prematurely.
		// If the iterator isn't smart enough to stop, this will panic.
		break
	}
}
