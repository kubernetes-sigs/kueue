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

func drainIter(it *candidateIterator, borrow bool) []string {
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

func TestIteratorMergesBucketsInOrder(t *testing.T) {
	ctx := preemptorCtx(t, 10, kueue.PreemptionPolicyAny, kueue.PreemptionPolicyNever, nil)
	// All within the preemptor CQ -> WithinCQ, so none is filtered out.
	a := candidateInfo(t, "a", preemptorCQName, 1)
	b := candidateInfo(t, "b", preemptorCQName, 1)
	c := candidateInfo(t, "c", preemptorCQName, 1)
	d := candidateInfo(t, "d", preemptorCQName, 1)

	var segs [numSegments][]*bucket
	segs[0] = []*bucket{
		segmentBucket([]*workload.Info{a, c}, false),
		segmentBucket([]*workload.Info{b, d}, false),
	}
	it := &candidateIterator{segments: segs, cmp: byName, hierarchicalReclaimCtx: ctx}

	if got := drainIter(it, false); !slices.Equal(got, []string{"a", "b", "c", "d"}) {
		t.Errorf("merge order = %v, want [a b c d]", got)
	}
}

func TestIteratorSegmentPrecedenceOverridesOrder(t *testing.T) {
	ctx := preemptorCtx(t, 10, kueue.PreemptionPolicyAny, kueue.PreemptionPolicyNever, nil)
	// "z" sits in an earlier segment than "a"; segment order must win over cmp.
	z := candidateInfo(t, "z", preemptorCQName, 1)
	a := candidateInfo(t, "a", preemptorCQName, 1)

	var segs [numSegments][]*bucket
	segs[2] = []*bucket{segmentBucket([]*workload.Info{z}, false)}
	segs[3] = []*bucket{segmentBucket([]*workload.Info{a}, false)}
	it := &candidateIterator{segments: segs, cmp: byName, hierarchicalReclaimCtx: ctx}

	if got := drainIter(it, false); !slices.Equal(got, []string{"z", "a"}) {
		t.Errorf("segment precedence = %v, want [z a] (earlier segment first)", got)
	}
}

func TestIteratorSkipsNeverCandidates(t *testing.T) {
	ctx := preemptorCtx(t, 10, kueue.PreemptionPolicyAny, kueue.PreemptionPolicyNever, nil)
	real1 := candidateInfo(t, "real1", preemptorCQName, 1)
	real2 := candidateInfo(t, "real2", preemptorCQName, 1)
	// fake requests nothing under the contested flavor -> classifies as Never.
	fake := candidateInfo(t, "fake", preemptorCQName, 1)
	for i := range fake.TotalRequests {
		fake.TotalRequests[i].Flavors = nil
	}

	var segs [numSegments][]*bucket
	segs[0] = []*bucket{segmentBucket([]*workload.Info{real1, fake, real2}, false)}
	it := &candidateIterator{segments: segs, cmp: byName, hierarchicalReclaimCtx: ctx}

	if got := drainIter(it, false); !slices.Equal(got, []string{"real1", "real2"}) {
		t.Errorf("Never candidate not skipped: got %v, want [real1 real2]", got)
	}
}

func TestIteratorBorrowFilterAndReset(t *testing.T) {
	// reclaim=Any, borrow forbidden (nil) -> other-CQ candidates without
	// hierarchical advantage classify as ReclaimWithoutBorrowing.
	ctx := preemptorCtx(t, 10, kueue.PreemptionPolicyNever, kueue.PreemptionPolicyAny, nil)
	c1 := candidateInfo(t, "c1", otherCQName, 1)
	c2 := candidateInfo(t, "c2", otherCQName, 1)

	var segs [numSegments][]*bucket
	segs[0] = []*bucket{segmentBucket([]*workload.Info{c1, c2}, false)}
	it := &candidateIterator{segments: segs, cmp: byName, hierarchicalReclaimCtx: ctx}

	// Borrowing run: ReclaimWithoutBorrowing candidates are skipped element-wise.
	if got := drainIter(it, true); len(got) != 0 {
		t.Errorf("borrow run should skip ReclaimWithoutBorrowing, got %v", got)
	}

	// Reset must rewind the drained cursors so the non-borrowing run sees them.
	it.Reset()
	if got := drainIter(it, false); !slices.Equal(got, []string{"c1", "c2"}) {
		t.Errorf("non-borrow run after reset = %v, want [c1 c2]", got)
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

	classes := []classifiedClusterQueue{
		// cqA has both an evicted prefix (evictedCount=1) and a non-evicted tail.
		// buildBuckets splits at the cache-computed evictedCount, not by re-probing
		// the workload conditions.
		{cq: cqA, sorted: []*workload.Info{e, n1}, evictedCount: 1},
		// cqB has only a non-evicted workload.
		{cq: cqB, sorted: []*workload.Info{n2}, evictedCount: 0},
		// cqEmpty contributes nothing.
		{cq: cqEmpty, sorted: nil},
	}

	evicted, nonEvicted := buildBuckets(classes)

	// Only cqA produces an evicted bucket.
	if len(evicted) != 1 {
		t.Fatalf("evicted buckets = %d, want 1", len(evicted))
	}
	if evicted[0].cq.Name != "cqA" || evicted[0].top().Obj.Name != "e" {
		t.Errorf("evicted bucket = (%q, %q), want (cqA, e)", evicted[0].cq.Name, evicted[0].top().Obj.Name)
	}

	// cqA (non-evicted tail) and cqB produce non-evicted buckets; cqEmpty is skipped.
	if len(nonEvicted) != 2 {
		t.Fatalf("non-evicted buckets = %d, want 2", len(nonEvicted))
	}
	gotCQs := []string{string(nonEvicted[0].cq.Name), string(nonEvicted[1].cq.Name)}
	if !slices.Equal(gotCQs, []string{"cqA", "cqB"}) {
		t.Errorf("non-evicted bucket CQs = %v, want [cqA cqB]", gotCQs)
	}
}
