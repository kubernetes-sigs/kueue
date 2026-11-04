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

package filters

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"k8s.io/apimachinery/pkg/util/sets"

	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

type stubWorkloadFilter struct {
	matchNames sets.Set[string]
}

func (f stubWorkloadFilter) Matches(wl *workload.Info) bool {
	return f.matchNames.Has(wl.Obj.Name)
}

type stubWorkloadTimeFilter struct {
	retryAtByName map[string]*time.Time
}

func (f stubWorkloadTimeFilter) Matches(wl *workload.Info) (*time.Time, bool) {
	retryAt, blocked := f.retryAtByName[wl.Obj.Name]
	return retryAt, !blocked
}

func TestCandidateFilters_MatchesClusterQueue(t *testing.T) {
	cq := &schdcache.ClusterQueueSnapshot{Name: "cq1"}

	cases := map[string]struct {
		filter    CandidateFilters
		wantMatch bool
	}{
		"matches when no ClusterQueueFilters are configured": {
			wantMatch: true,
		},
		"matches when all ClusterQueueFilters match": {
			filter: CandidateFilters{
				CQFilters: []ClusterQueueFilter{
					NewWithinClusterQueueFilter("cq1"),
					NewWithinClusterQueueFilter("cq1"),
				},
			},
			wantMatch: true,
		},
		"returns false when any ClusterQueueFilter does not match": {
			filter: CandidateFilters{
				CQFilters: []ClusterQueueFilter{
					NewWithinClusterQueueFilter("cq1"),
					NewWithinClusterQueueFilter("cq2"),
				},
			},
			wantMatch: false,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := tc.filter.MatchesClusterQueue(cq); got != tc.wantMatch {
				t.Errorf("MatchesClusterQueue() = %v, want %v", got, tc.wantMatch)
			}
		})
	}
}

func TestCandidateFilters_MatchesWorkload(t *testing.T) {
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	tEarly := now.Add(15 * time.Second)
	tLate := now.Add(40 * time.Second)
	wl := &workload.Info{Obj: utiltestingapi.MakeWorkload("a1", "ns").Obj()}

	cases := map[string]struct {
		filter      CandidateFilters
		wantMatches bool
		wantRetryAt *time.Time
	}{
		"matches when no filters are configured": {
			wantMatches: true,
		},
		"matches when all static and time filters match": {
			filter: CandidateFilters{
				WLFilters: []WorkloadFilter{
					stubWorkloadFilter{matchNames: sets.New("a1")},
					stubWorkloadFilter{matchNames: sets.New("a1")},
				},
				WLTimeFilters: []WorkloadTimeFilter{stubWorkloadTimeFilter{}},
			},
			wantMatches: true,
		},
		"static WorkloadFilter mismatch short-circuits WorkloadTimeFilters": {
			filter: CandidateFilters{
				WLFilters: []WorkloadFilter{
					stubWorkloadFilter{matchNames: sets.New("a1")},
					stubWorkloadFilter{matchNames: sets.New("a2")},
				},
				WLTimeFilters: []WorkloadTimeFilter{
					stubWorkloadTimeFilter{retryAtByName: map[string]*time.Time{"a1": &tEarly}},
				},
			},
		},
		"failing WorkloadTimeFilter alongside matching WorkloadTimeFilter returns retryAt": {
			filter: CandidateFilters{
				WLTimeFilters: []WorkloadTimeFilter{
					stubWorkloadTimeFilter{},
					stubWorkloadTimeFilter{retryAtByName: map[string]*time.Time{"a1": &tEarly}},
				},
			},
			wantRetryAt: &tEarly,
		},
		"WorkloadTimeFilter mismatch with nil retryAt cancels earlier retryAt": {
			filter: CandidateFilters{
				WLTimeFilters: []WorkloadTimeFilter{
					stubWorkloadTimeFilter{retryAtByName: map[string]*time.Time{"a1": &tEarly}},
					stubWorkloadTimeFilter{retryAtByName: map[string]*time.Time{"a1": nil}},
				},
			},
		},
		"multiple failing WorkloadTimeFilters return latest retryAt (early then late)": {
			filter: CandidateFilters{
				WLTimeFilters: []WorkloadTimeFilter{
					stubWorkloadTimeFilter{retryAtByName: map[string]*time.Time{"a1": &tEarly}},
					stubWorkloadTimeFilter{retryAtByName: map[string]*time.Time{"a1": &tLate}},
				},
			},
			wantRetryAt: &tLate,
		},
		"multiple failing WorkloadTimeFilters return latest retryAt (late then early)": {
			filter: CandidateFilters{
				WLTimeFilters: []WorkloadTimeFilter{
					stubWorkloadTimeFilter{retryAtByName: map[string]*time.Time{"a1": &tLate}},
					stubWorkloadTimeFilter{retryAtByName: map[string]*time.Time{"a1": &tEarly}},
				},
			},
			wantRetryAt: &tLate,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			gotRetryAt, gotMatches := tc.filter.MatchesWorkload(wl)
			if gotMatches != tc.wantMatches {
				t.Errorf("MatchesWorkload() matches = %v, want %v", gotMatches, tc.wantMatches)
			}
			if diff := cmp.Diff(tc.wantRetryAt, gotRetryAt); diff != "" {
				t.Errorf("MatchesWorkload() retryAt (-want,+got):\n%s", diff)
			}
		})
	}
}
