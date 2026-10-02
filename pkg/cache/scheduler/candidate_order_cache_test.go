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
	"slices"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
	workloadevict "sigs.k8s.io/kueue/pkg/workload/evict"
)

func evictedInfo(t *testing.T, name string, evicted bool) *workload.Info {
	t.Helper()
	_, log := utiltesting.ContextWithLog(t)
	wl := utiltestingapi.MakeWorkload(name, "ns")
	if evicted {
		wl = wl.Condition(metav1.Condition{
			Type:               kueue.WorkloadEvicted,
			Status:             metav1.ConditionTrue,
			Reason:             "test",
			LastTransitionTime: metav1.NewTime(time.Now()),
		})
	}
	return workload.NewInfo(log, wl.Obj())
}

func orderCmp(a, b *workload.Info) int {
	aEv, bEv := workloadevict.IsEvicted(a.Obj), workloadevict.IsEvicted(b.Obj)
	if aEv != bEv {
		if aEv {
			return -1
		}
		return 1
	}
	return cmp.Compare(a.Obj.Name, b.Obj.Name)
}

func cqWith(name kueue.ClusterQueueReference, infos ...*workload.Info) *ClusterQueueSnapshot {
	wls := make(map[workload.Reference]*workload.Info, len(infos))
	for _, wl := range infos {
		wls[workload.Key(wl.Obj)] = wl
	}
	return &ClusterQueueSnapshot{Name: name, Workloads: wls}
}

func names(infos []*workload.Info) []string {
	out := make([]string, len(infos))
	for i, wl := range infos {
		out[i] = wl.Obj.Name
	}
	return out
}

// TestBuildCandidateOrderEntry checks the entry a miss produces: workloads sorted
// by the comparator with the evicted prefix length recorded.
func TestBuildCandidateOrderEntry(t *testing.T) {
	e1 := evictedInfo(t, "e1", true)
	e2 := evictedInfo(t, "e2", true)
	n1 := evictedInfo(t, "n1", false)
	n2 := evictedInfo(t, "n2", false)

	cases := map[string]struct {
		cq          *ClusterQueueSnapshot
		wantSorted  []string
		wantEvicted int
	}{
		"mixed evicted and non-evicted are sorted with evicted prefix": {
			cq:          cqWith("cq", n2, e2, n1, e1),
			wantSorted:  []string{"e1", "e2", "n1", "n2"},
			wantEvicted: 2,
		},
		"only non-evicted workloads produce zero evicted prefix": {
			cq:          cqWith("cq", n2, n1),
			wantSorted:  []string{"n1", "n2"},
			wantEvicted: 0,
		},
		"only evicted workloads produce full-length evicted prefix": {
			cq:          cqWith("cq", e2, e1),
			wantSorted:  []string{"e1", "e2"},
			wantEvicted: 2,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := buildCandidateOrderEntry(tc.cq, orderCmp)
			if got := names(e.sorted); !slices.Equal(got, tc.wantSorted) {
				t.Errorf("sorted names = %v, want %v", got, tc.wantSorted)
			}
			if e.evictedCount != tc.wantEvicted {
				t.Errorf("evictedCount = %d, want %d", e.evictedCount, tc.wantEvicted)
			}
		})
	}
}

// TestGetBuildsThenReuses verifies Get builds the entry on the first call and
// returns the same cache-owned slice on a subsequent call while membership is
// unchanged.
func TestGetBuildsThenReuses(t *testing.T) {
	cache := newCandidateOrderCache()
	cq := cqWith("cq", evictedInfo(t, "n1", false), evictedInfo(t, "e1", true))

	var first []*workload.Info
	t.Run("miss builds and returns sorted order", func(t *testing.T) {
		var evicted int
		first, evicted = cache.Get(cq, orderCmp)
		if got, want := names(first), []string{"e1", "n1"}; !slices.Equal(got, want) {
			t.Fatalf("Get names = %v, want %v", got, want)
		}
		if evicted != 1 {
			t.Fatalf("Get evictedCount = %d, want 1", evicted)
		}
	})

	t.Run("hit reuses cached slice without rebuilding", func(t *testing.T) {
		second, _ := cache.Get(cq, orderCmp)
		if &first[0] != &second[0] {
			t.Errorf("second Get rebuilt the entry; want the cached slice reused")
		}
	})
}

// TestGetInvalidatesOnMembershipChange verifies that once a CQ's workload set
// changes, matches rejects the stale entry and Get rebuilds it.
func TestGetInvalidatesOnMembershipChange(t *testing.T) {
	cache := newCandidateOrderCache()
	n1 := evictedInfo(t, "n1", false)
	cq := cqWith("cq", n1)

	if got, _ := cache.Get(cq, orderCmp); !slices.Equal(names(got), []string{"n1"}) {
		t.Fatalf("initial Get names = %v, want [n1]", names(got))
	}

	// Add a workload under the same CQ name: the old entry no longer matches.
	n2 := evictedInfo(t, "n2", false)
	cq.Workloads[workload.Key(n2.Obj)] = n2

	got, _ := cache.Get(cq, orderCmp)
	if want := []string{"n1", "n2"}; !slices.Equal(names(got), want) {
		t.Errorf("Get after membership change = %v, want %v", names(got), want)
	}
}

// TestMatches covers the per-read validation: an entry matches only when its
// length and every member key still line up with the CQ's current workloads.
func TestMatches(t *testing.T) {
	n1 := evictedInfo(t, "n1", false)
	n2 := evictedInfo(t, "n2", false)
	n3 := evictedInfo(t, "n3", false)
	base := cqWith("cq", n1, n2)
	e := buildCandidateOrderEntry(base, orderCmp)

	cases := map[string]struct {
		cq   *ClusterQueueSnapshot
		want bool
	}{
		"same membership matches": {
			cq:   base,
			want: true,
		},
		"shorter CQ after removal does not match": {
			cq:   cqWith("cq", n1),
			want: false,
		},
		"same length but different key does not match": {
			cq:   cqWith("cq", n1, n3),
			want: false,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := matches(e, tc.cq); got != tc.want {
				t.Errorf("matches() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestWarm verifies Warm populates entries for every CQ so later Get calls hit
// the cache, and that already-warm entries are left untouched.
func TestWarm(t *testing.T) {
	cache := newCandidateOrderCache()
	cqA := cqWith("a", evictedInfo(t, "n1", false), evictedInfo(t, "e1", true))
	cqB := cqWith("b", evictedInfo(t, "n2", false))

	t.Run("populates entries for all CQs", func(t *testing.T) {
		cache.Warm([]*ClusterQueueSnapshot{cqA, cqB}, orderCmp)

		entryA, okA := cache.entries[cqA.Name]
		_, okB := cache.entries[cqB.Name]
		if !okA || !okB {
			t.Fatalf("Warm did not populate both entries: a=%v b=%v", okA, okB)
		}
		if got, want := names(entryA.sorted), []string{"e1", "n1"}; !slices.Equal(got, want) {
			t.Errorf("warmed a sorted = %v, want %v", got, want)
		}
	})

	t.Run("does not rebuild an unchanged entry on second Warm", func(t *testing.T) {
		entryA := cache.entries[cqA.Name]
		cache.Warm([]*ClusterQueueSnapshot{cqA}, orderCmp)
		if cache.entries[cqA.Name] != entryA {
			t.Errorf("Warm rebuilt an unchanged entry; want it reused")
		}
	})
}

// TestEvictedPrefixLen covers the boundary the cache computes once per sort and
// hands to buildBuckets: evicted workloads rank first under CandidatesOrdering,
// so they form a prefix whose length the binary search must find exactly.
func TestEvictedPrefixLen(t *testing.T) {
	e1 := evictedInfo(t, "e1", true)
	e2 := evictedInfo(t, "e2", true)
	n1 := evictedInfo(t, "n1", false)
	n2 := evictedInfo(t, "n2", false)

	cases := map[string]struct {
		in   []*workload.Info
		want int
	}{
		"empty":          {in: nil, want: 0},
		"all evicted":    {in: []*workload.Info{e1, e2}, want: 2},
		"none evicted":   {in: []*workload.Info{n1, n2}, want: 0},
		"evicted prefix": {in: []*workload.Info{e1, e2, n1, n2}, want: 2},
		"single evicted": {in: []*workload.Info{e1, n1}, want: 1},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := evictedPrefixLen(tc.in); got != tc.want {
				t.Errorf("evictedPrefixLen() = %d, want %d", got, tc.want)
			}
		})
	}
}
