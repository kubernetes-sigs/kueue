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
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"k8s.io/utils/ptr"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

func TestV1Beta2FromInternal(t *testing.T) {
	testcases := map[string]struct {
		original *TopologyAssignment
		expected *kueue.TopologyAssignment
	}{
		"nil": {
			original: nil,
			expected: nil,
		},
		"empty": {
			original: &TopologyAssignment{
				Levels: []string{"a", "b"},
			},
			expected: &kueue.TopologyAssignment{
				Levels: []string{"a", "b"},
				Slices: []kueue.TopologyAssignmentSlice{},
			},
		},
		"one domain": {
			original: &TopologyAssignment{
				Levels: []string{"a", "b"},
				Domains: []TopologyDomainAssignment{
					{
						Values: []string{"a1", "b1"},
						Count:  1,
					},
				},
			},
			expected: &kueue.TopologyAssignment{
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
			original: &TopologyAssignment{
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
			expected: &kueue.TopologyAssignment{
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
			original: &TopologyAssignment{
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
			expected: &kueue.TopologyAssignment{
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
			original: &TopologyAssignment{
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
			expected: &kueue.TopologyAssignment{
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
			original: &TopologyAssignment{
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
			expected: &kueue.TopologyAssignment{
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
			original: &TopologyAssignment{
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
			expected: &kueue.TopologyAssignment{
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
			original: &TopologyAssignment{
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
			expected: &kueue.TopologyAssignment{
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
			original: &TopologyAssignment{
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
			expected: &kueue.TopologyAssignment{
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

	for name, tc := range testcases {
		t.Run(name, func(t *testing.T) {
			got := V1Beta2From(tc.original)
			if diff := cmp.Diff(tc.expected, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("unexpected result (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestInternalFromV1Beta2(t *testing.T) {
	testcases := map[string]struct {
		original *kueue.TopologyAssignment
		expected *TopologyAssignment
	}{
		"nil": {
			original: nil,
			expected: nil,
		},
		"empty": {
			original: &kueue.TopologyAssignment{
				Levels: []string{"a", "b"},
			},
			expected: &TopologyAssignment{
				Levels:  []string{"a", "b"},
				Domains: []TopologyDomainAssignment{},
			},
		},
		"one slice": {
			original: &kueue.TopologyAssignment{
				Levels: []string{"a", "b"},
				Slices: []kueue.TopologyAssignmentSlice{
					{
						DomainCount: 2,
						PodCounts:   []int32{1, 2},
						ValuesPerLevel: []kueue.TopologyAssignmentSliceLevelValues{
							{
								Prefix: ptr.To("p."),
								Suffix: ptr.To(".s"),
								Roots:  []string{"a1", "a2"},
							},
							{
								UniversalValue: ptr.To("b"),
							},
						},
					},
				},
			},
			expected: &TopologyAssignment{
				Levels: []string{"a", "b"},
				Domains: []TopologyDomainAssignment{
					{
						Values: []string{"p.a1.s", "b"},
						Count:  1,
					},
					{
						Values: []string{"p.a2.s", "b"},
						Count:  2,
					},
				},
			},
		},
		"multiple slices": {
			original: &kueue.TopologyAssignment{
				Levels: []string{"a", "b"},
				Slices: []kueue.TopologyAssignmentSlice{
					{
						DomainCount:       1,
						UniversalPodCount: ptr.To(int32(1)),
						ValuesPerLevel: []kueue.TopologyAssignmentSliceLevelValues{
							{
								Roots: []string{"a1"},
							},
							{
								Roots: []string{"b1"},
							},
						},
					},
					{
						DomainCount: 2,
						PodCounts:   []int32{2, 3},
						ValuesPerLevel: []kueue.TopologyAssignmentSliceLevelValues{
							{
								Roots: []string{"a2", "a3"},
							},
							{
								Roots: []string{"b2", "b3"},
							},
						},
					},
				},
			},
			expected: &TopologyAssignment{
				Levels: []string{"a", "b"},
				Domains: []TopologyDomainAssignment{
					{
						Values: []string{"a1", "b1"},
						Count:  1,
					},
					{
						Values: []string{"a2", "b2"},
						Count:  2,
					},
					{
						Values: []string{"a3", "b3"},
						Count:  3,
					},
				},
			},
		},
	}

	for name, tc := range testcases {
		t.Run(name, func(t *testing.T) {
			got := InternalFrom(tc.original)
			if diff := cmp.Diff(tc.expected, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("unexpected result (-want,+got):\n%s", diff)
			}
		})
	}
}
