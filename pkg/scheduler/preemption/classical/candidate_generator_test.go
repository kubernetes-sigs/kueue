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
	"slices"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
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

func byName(a, b *workload.Info) int {
	return strings.Compare(a.Obj.Name, b.Obj.Name)
}

func segmentBucket(sorted []*workload.Info, advantage bool) *bucket {
	return &bucket{
		isSameQueue:              true,
		hasHierarchicalAdvantage: advantage,
		sorted:                   sorted,
	}
}

func drainIter(it *cacheCandidateIterator, borrow bool) []string {
	var got []string
	for {
		wl, _ := it.Next(borrow)
		if wl == nil {
			break
		}
		got = append(got, wl.Obj.Name)
	}
	return got
}

func TestCacheIterator(t *testing.T) {
	// shared workloads
	a := candidateInfo(t, "a", preemptorCQName, 1)
	b := candidateInfo(t, "b", preemptorCQName, 1)
	c := candidateInfo(t, "c", preemptorCQName, 1)
	d := candidateInfo(t, "d", preemptorCQName, 1)
	z := candidateInfo(t, "z", preemptorCQName, 1)
	real1 := candidateInfo(t, "real1", preemptorCQName, 1)
	real2 := candidateInfo(t, "real2", preemptorCQName, 1)
	fake := candidateInfo(t, "fake", preemptorCQName, 1)
	for i := range fake.TotalRequests {
		fake.TotalRequests[i].Flavors = nil
	}
	c1 := candidateInfo(t, "c1", otherCQName, 1)
	c2 := candidateInfo(t, "c2", otherCQName, 1)

	// ctxWithin: reclaim=Never, so cross-CQ candidates are not filtered by
	// policy; used for pure iteration-order cases.
	ctxWithin := preemptorCtx(t, 10, kueue.PreemptionPolicyAny, kueue.PreemptionPolicyNever, nil)
	// ctxReclaim: reclaim=Any but borrow forbidden; other-CQ candidates
	// classify as ReclaimWithoutBorrowing and are skipped on a borrow run.
	ctxReclaim := preemptorCtx(t, 10, kueue.PreemptionPolicyNever, kueue.PreemptionPolicyAny, nil)

	cases := map[string]struct {
		ctx      *HierarchicalPreemptionCtx
		segments [numSegments][]*bucket
		// first pass
		borrow bool
		want   []string // nil means expect no results
		// optional Reset + second pass
		doReset        bool
		resetBorrow    bool
		wantAfterReset []string
	}{
		"merges buckets from the same segment in comparator order": {
			ctx: ctxWithin,
			segments: func() [numSegments][]*bucket {
				var s [numSegments][]*bucket
				s[0] = []*bucket{
					segmentBucket([]*workload.Info{a, c}, false),
					segmentBucket([]*workload.Info{b, d}, false),
				}
				return s
			}(),
			borrow: false,
			want:   []string{"a", "b", "c", "d"},
		},
		"earlier segment always precedes a later segment regardless of element order": {
			// "z" sits in segment 2, "a" in segment 3: segment index must win over cmp.
			ctx: ctxWithin,
			segments: func() [numSegments][]*bucket {
				var s [numSegments][]*bucket
				s[2] = []*bucket{segmentBucket([]*workload.Info{z}, false)}
				s[3] = []*bucket{segmentBucket([]*workload.Info{a}, false)}
				return s
			}(),
			borrow: false,
			want:   []string{"z", "a"},
		},
		"Never candidates are silently skipped": {
			// fake uses no contested flavor -> classifies as Never; real1/real2 pass.
			ctx: ctxWithin,
			segments: func() [numSegments][]*bucket {
				var s [numSegments][]*bucket
				s[0] = []*bucket{segmentBucket([]*workload.Info{real1, fake, real2}, false)}
				return s
			}(),
			borrow: false,
			want:   []string{"real1", "real2"},
		},
		"borrow run skips ReclaimWithoutBorrowing; non-borrow run after Reset sees them": {
			ctx: ctxReclaim,
			segments: func() [numSegments][]*bucket {
				var s [numSegments][]*bucket
				s[0] = []*bucket{segmentBucket([]*workload.Info{c1, c2}, false)}
				return s
			}(),
			borrow:         true,
			want:           nil,
			doReset:        true,
			resetBorrow:    false,
			wantAfterReset: []string{"c1", "c2"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			it := &cacheCandidateIterator{segments: tc.segments, cmp: byName, hierarchicalReclaimCtx: tc.ctx}
			if got := drainIter(it, tc.borrow); !slices.Equal(got, tc.want) {
				t.Errorf("first pass = %v, want %v", got, tc.want)
			}
			if tc.doReset {
				it.Reset()
				if got := drainIter(it, tc.resetBorrow); !slices.Equal(got, tc.wantAfterReset) {
					t.Errorf("after Reset = %v, want %v", got, tc.wantAfterReset)
				}
			}
		})
	}
}

func TestBucketCursor(t *testing.T) {
	w := []*workload.Info{
		{Obj: utiltestingapi.MakeWorkload("a", "ns").Obj()},
		{Obj: utiltestingapi.MakeWorkload("b", "ns").Obj()},
		{Obj: utiltestingapi.MakeWorkload("c", "ns").Obj()},
	}

	t.Run("forward consumption", func(t *testing.T) {
		b := &bucket{sorted: w}
		got := []string{}
		for !b.empty() {
			got = append(got, b.top().Obj.Name)
			b.advance()
		}
		if !slices.Equal(got, []string{"a", "b", "c"}) {
			t.Errorf("consumed %v, want [a b c]", got)
		}
		if !b.empty() {
			t.Error("bucket should be empty after full consumption")
		}
	})

	t.Run("empty slice", func(t *testing.T) {
		b := &bucket{sorted: nil}
		if !b.empty() {
			t.Error("nil-backed bucket should be empty")
		}
	})

	t.Run("drain exhausts immediately", func(t *testing.T) {
		b := &bucket{sorted: w}
		if b.empty() {
			t.Fatal("bucket should start non-empty")
		}
		b.drain()
		if !b.empty() {
			t.Error("bucket should be empty after drain()")
		}
	})

	t.Run("reset rewinds after advance", func(t *testing.T) {
		b := &bucket{sorted: w}
		b.advance()
		b.advance()
		b.reset()
		if b.empty() || b.top().Obj.Name != "a" {
			t.Errorf("after reset top should be a, got empty=%v", b.empty())
		}
	})

	t.Run("reset rewinds after drain", func(t *testing.T) {
		b := &bucket{sorted: w}
		b.drain()
		b.reset()
		if b.empty() || b.top().Obj.Name != "a" {
			t.Error("reset must rewind a drained bucket so it participates in the next run")
		}
	})
}

func TestBuildBuckets(t *testing.T) {
	e := evictedInfo(t, "e", true)
	n1 := evictedInfo(t, "n1", false)
	n2 := evictedInfo(t, "n2", false)

	cqA := &schdcache.ClusterQueueSnapshot{Name: "cqA"}
	cqB := &schdcache.ClusterQueueSnapshot{Name: "cqB"}
	cqEmpty := &schdcache.ClusterQueueSnapshot{Name: "cqEmpty"}

	cases := map[string]struct {
		classes       []classifiedClusterQueue
		wantEvicted   int
		wantNonEvicted int
		wantEvictedCQ   string
		wantEvictedTop  string
		wantNonEvictedCQs []string
	}{
		"evicted bucket, non-evicted bucket, empty CQ skipped": {
			classes: []classifiedClusterQueue{
				// cqA has both an evicted prefix (evictedCount=1) and a non-evicted tail.
				// buildBuckets splits at the cache-computed evictedCount, not by re-probing
				// the workload conditions.
				{cq: cqA, sorted: []*workload.Info{e, n1}, evictedCount: 1},
				// cqB has only a non-evicted workload.
				{cq: cqB, sorted: []*workload.Info{n2}, evictedCount: 0},
				// cqEmpty contributes nothing.
				{cq: cqEmpty, sorted: nil},
			},
			wantEvicted:       1,
			wantNonEvicted:    2,
			wantEvictedCQ:     "cqA",
			wantEvictedTop:    "e",
			wantNonEvictedCQs: []string{"cqA", "cqB"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			evicted, nonEvicted := buildBuckets(tc.classes)

			if len(evicted) != tc.wantEvicted {
				t.Fatalf("evicted buckets = %d, want %d", len(evicted), tc.wantEvicted)
			}
			if tc.wantEvicted > 0 {
				if evicted[0].cq.Name != kueue.ClusterQueueReference(tc.wantEvictedCQ) || evicted[0].top().Obj.Name != tc.wantEvictedTop {
					t.Errorf("evicted bucket = (%q, %q), want (%q, %q)", evicted[0].cq.Name, evicted[0].top().Obj.Name, tc.wantEvictedCQ, tc.wantEvictedTop)
				}
			}

			if len(nonEvicted) != tc.wantNonEvicted {
				t.Fatalf("non-evicted buckets = %d, want %d", len(nonEvicted), tc.wantNonEvicted)
			}
			gotCQs := make([]string, len(nonEvicted))
			for i, b := range nonEvicted {
				gotCQs[i] = string(b.cq.Name)
			}
			if !slices.Equal(gotCQs, tc.wantNonEvictedCQs) {
				t.Errorf("non-evicted bucket CQs = %v, want %v", gotCQs, tc.wantNonEvictedCQs)
			}
		})
	}
}
