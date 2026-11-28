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

package classical

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption/policy"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

func afsCandidate(t *testing.T, name, cq, lq string, priority int32, usage *float64) *candidateElem {
	t.Helper()
	_, log := utiltesting.ContextWithLog(t)
	obj := utiltestingapi.MakeWorkload(name, "ns").
		Queue(kueue.LocalQueueName(lq)).
		Priority(priority).
		Obj()
	obj.UID = types.UID(name)
	info := workload.NewInfo(log, obj)
	info.ClusterQueue = kueue.ClusterQueueReference(cq)
	info.LocalQueueFSUsage = usage
	return &candidateElem{wl: info}
}

func TestSortCandidateGroup(t *testing.T) {
	now := time.Now()
	_, log := utiltesting.ContextWithLog(t)

	candidates := []*candidateElem{
		afsCandidate(t, "a", "q1", "lq1", 5, new(10.0)),
		afsCandidate(t, "b", "q1", "lq2", 0, new(1.0)),
		afsCandidate(t, "c", "q2", "lq3", 3, nil),
	}

	permutations := [][]int{
		{0, 1, 2}, {0, 2, 1}, {1, 0, 2},
		{1, 2, 0}, {2, 0, 1}, {2, 1, 0},
	}

	cases := map[string]struct {
		afsEnabled bool
		want       []string
	}{
		"AdmissionFairSharing: LocalQueue usage within q1, priority across ClusterQueues": {
			afsEnabled: true,
			want:       []string{"c", "a", "b"},
		},
		"without AdmissionFairSharing: priority is a global order": {
			afsEnabled: false,
			want:       []string{"b", "c", "a"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			comparator := func(a, b *candidateElem) int {
				return policy.CandidatesOrdering(log, tc.afsEnabled, a.wl, b.wl, "p", now)
			}
			for _, permutation := range permutations {
				input := make([]*candidateElem, 0, len(permutation))
				for _, i := range permutation {
					input = append(input, candidates[i])
				}
				got := sortCandidateGroup(input, tc.afsEnabled, comparator)
				gotNames := make([]string, 0, len(got))
				for _, candidate := range got {
					gotNames = append(gotNames, candidate.wl.Obj.Name)
				}
				if diff := cmp.Diff(tc.want, gotNames); diff != "" {
					t.Errorf("sortCandidateGroup(%v) returned unexpected order (-want,+got):\n%s", permutation, diff)
				}
				if len(got) != len(input) {
					t.Errorf("sortCandidateGroup(%v) returned %d candidates, want %d", permutation, len(got), len(input))
				}
			}
		})
	}
}

func TestSortCandidateGroupKeepsEvictedFirst(t *testing.T) {
	now := time.Now()
	_, log := utiltesting.ContextWithLog(t)

	evicted := afsCandidate(t, "evicted", "q2", "lq4", 100, nil)
	evicted.wl.Obj.Status.Conditions = []metav1.Condition{{
		Type:               kueue.WorkloadEvicted,
		Status:             metav1.ConditionTrue,
		LastTransitionTime: metav1.NewTime(now),
	}}
	candidates := []*candidateElem{
		afsCandidate(t, "a", "q1", "lq1", 5, new(10.0)),
		afsCandidate(t, "b", "q1", "lq2", 0, new(1.0)),
		afsCandidate(t, "c", "q2", "lq3", 3, nil),
		evicted,
	}
	comparator := func(a, b *candidateElem) int {
		return policy.CandidatesOrdering(log, true, a.wl, b.wl, "p", now)
	}

	got := sortCandidateGroup(candidates, true, comparator)
	gotNames := make([]string, 0, len(got))
	for _, candidate := range got {
		gotNames = append(gotNames, candidate.wl.Obj.Name)
	}
	if diff := cmp.Diff([]string{"evicted", "c", "a", "b"}, gotNames); diff != "" {
		t.Errorf("sortCandidateGroup returned unexpected order (-want,+got):\n%s", diff)
	}

	evictedPrefix, nonEvicted := splitEvicted(got)
	if len(evictedPrefix) != 1 || len(nonEvicted) != 3 {
		t.Errorf("splitEvicted got %d evicted and %d non-evicted candidates, want 1 and 3; evicted candidates must form a prefix", len(evictedPrefix), len(nonEvicted))
	}
}
