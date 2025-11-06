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
	internal *TopologyAssignment
	v1beta2  *kueue.TopologyAssignment
}

var testCases map[string]TestCase = map[string]TestCase{
	"nil": {
		internal: nil,
		v1beta2:  nil,
	},
	"empty": {
		internal: &TopologyAssignment{
			Levels: []string{"a", "b"},
		},
		v1beta2: &kueue.TopologyAssignment{
			Levels: []string{"a", "b"},
			Slices: []kueue.TopologyAssignmentSlice{},
		},
	},
	"one domain": {
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
					DomainCount:       1,
					UniversalPodCount: ptr.To(int32(1)),
					ValuesPerLevel: []kueue.TopologyAssignmentSliceLevelValues{
						{
							UniversalValue: ptr.To("a1"),
						},
						{
							UniversalValue: ptr.To("b1"),
						},
					},
				},
			},
		},
	},
	"multiple domains, same counts, no common prefix": {
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
					DomainCount:       2,
					UniversalPodCount: ptr.To(int32(1)),
					ValuesPerLevel: []kueue.TopologyAssignmentSliceLevelValues{
						{
							Prefix: ptr.To("a"),
							Roots:  []string{"1", "2"},
						},
						{
							Prefix: ptr.To("b"),
							Roots:  []string{"1", "2"},
						},
					},
				},
			},
		},
	},
	"multiple domains, different counts, common prefix and suffix": {
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
					PodCounts:   []int32{1, 2},
					ValuesPerLevel: []kueue.TopologyAssignmentSliceLevelValues{
						{
							Prefix: ptr.To("p.a"),
							Suffix: ptr.To(".s"),
							Roots:  []string{"1", "2"},
						},
						{
							Prefix: ptr.To("p.b"),
							Suffix: ptr.To(".s"),
							Roots:  []string{"1", "2"},
						},
					},
				},
			},
		},
	},
	"multiple domains, universal value": {
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
					PodCounts:   []int32{1, 2},
					ValuesPerLevel: []kueue.TopologyAssignmentSliceLevelValues{
						{
							UniversalValue: ptr.To("a1"),
						},
						{
							Roots: []string{"x", "y"},
						},
					},
				},
			},
		},
	},
	"multiple domains, same counts, shrinking prefix": {
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
					DomainCount:       4,
					UniversalPodCount: ptr.To(int32(1)),
					ValuesPerLevel: []kueue.TopologyAssignmentSliceLevelValues{
						{
							Prefix: ptr.To("a1"),
							Roots:  []string{"23", "2", "", "3"},
						},
					},
				},
			},
		},
	},
	"multiple domains, same counts, shrinking suffix": {
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
					DomainCount:       4,
					UniversalPodCount: ptr.To(int32(1)),
					ValuesPerLevel: []kueue.TopologyAssignmentSliceLevelValues{
						{
							Suffix: ptr.To("3b"),
							Roots:  []string{"12", "2", "", "1"},
						},
					},
				},
			},
		},
	},
	"multiple domains, same counts, max prefix & suffix overlap partially": {
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
					DomainCount:       2,
					UniversalPodCount: ptr.To(int32(1)),
					ValuesPerLevel: []kueue.TopologyAssignmentSliceLevelValues{
						{
							Prefix: ptr.To("ab"),
							Suffix: ptr.To("acada"),
							Roots:  []string{"ac", ""},
						},
					},
				},
			},
		},
	},
	"multiple domains, same counts, max prefix & suffix overlap fully": {
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
					DomainCount:       2,
					UniversalPodCount: ptr.To(int32(1)),
					ValuesPerLevel: []kueue.TopologyAssignmentSliceLevelValues{
						// Prefix shrunk to "" -> it shouldn't be set at all.
						{
							Suffix: ptr.To("aba"),
							Roots:  []string{"ab", ""},
						},
					},
				},
			},
		},
	},
}

func TestV1Beta2FromInternal(t *testing.T) {
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			got := V1Beta2From(tc.internal)
			if diff := cmp.Diff(tc.v1beta2, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("unexpected result (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestInternalFromV1Beta2(t *testing.T) {
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			got := InternalFrom(tc.v1beta2)
			if diff := cmp.Diff(tc.internal, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("unexpected result (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestInternalSeqFromV1Beta2(t *testing.T) {
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			seq := InternalSeqFrom(tc.v1beta2)
			if seq == nil || tc.internal == nil {
				if tc.internal != nil {
					t.Errorf("unexpected nil returned from InternalSeqFrom")
				}
				if seq != nil {
					t.Errorf("unexpected non-nil result returned from InternalSeqFrom: %+v", seq)
				}
				return
			}
			got := slices.Collect(seq)
			want := tc.internal.Domains
			if diff := cmp.Diff(want, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("unexpected result (-want,+got):\n%s", diff)
			}
		})
	}
}
