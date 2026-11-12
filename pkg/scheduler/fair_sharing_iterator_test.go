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
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/scheduler/flavorassigner"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

// TestFairSharingIteratorReturnsAllEntriesOfClusterQueue verifies that the
// iterator considers every entry of a ClusterQueue within a cycle, in
// compareEntries order. A ClusterQueue can have several entries in one cycle:
// workloads queued for a second pass of scheduling in addition to the queue
// head.
func TestFairSharingIteratorReturnsAllEntriesOfClusterQueue(t *testing.T) {
	now := time.Now()
	ctx, log := utiltesting.ContextWithLog(t)
	cq := &schdcache.ClusterQueueSnapshot{Name: "cq"}

	secondPass := utiltestingapi.MakeWorkload("second-pass", "ns").
		Creation(now.Add(2*time.Minute)).
		SimpleReserveQuota("cq", "default", now).
		Obj()
	headOld := utiltestingapi.MakeWorkload("head-old", "ns").
		Creation(now).
		Obj()
	headNew := utiltestingapi.MakeWorkload("head-new", "ns").
		Creation(now.Add(time.Minute)).
		Obj()

	entries := make([]entry, 0, 3)
	for _, wl := range []*kueue.Workload{headNew, headOld, secondPass} {
		entries = append(entries, entry{
			Head:                 qcache.Head{Info: *workload.NewInfo(log, wl)},
			clusterQueueSnapshot: cq,
		})
	}

	iterator := makeFairSharingIterator(ctx, entries, workload.Ordering{})
	var got []string
	for iterator.hasNext() {
		got = append(got, iterator.pop().Obj.Name)
	}
	// The entry with quota already reserved goes first, the remaining ones
	// follow in FIFO order.
	want := []string{"second-pass", "head-old", "head-new"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Unexpected pop order (-want,+got):\n%s", diff)
	}
}

// TestFairSharingIteratorScoresClusterQueueNominee verifies that the tournament
// scores each ClusterQueue by the entry it nominates, its first one. A
// second-pass workload adds no quota, so its ClusterQueue competes with its
// current share. Scoring another entry would leave the nominee without a DRS
// value, which counts as zero and wrongly puts it ahead.
func TestFairSharingIteratorScoresClusterQueueNominee(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	ctx, log := utiltesting.ContextWithLog(t)

	cqA := utiltestingapi.MakeClusterQueue("iter-a").
		Cohort("iter-cohort").
		ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
			Resource(corev1.ResourceCPU, "2", "20").Obj()).
		Obj()
	cqB := utiltestingapi.MakeClusterQueue("iter-b").
		Cohort("iter-cohort").
		ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
			Resource(corev1.ResourceCPU, "2", "20").Obj()).
		Obj()
	// Lends the capacity that iter-a and iter-b borrow.
	cqLender := utiltestingapi.MakeClusterQueue("iter-lender").
		Cohort("iter-cohort").
		ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
			Resource(corev1.ResourceCPU, "20").Obj()).
		Obj()

	cache := schdcache.New(utiltesting.NewFakeClient())
	cache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("default").Obj())
	for _, cq := range []*kueue.ClusterQueue{cqA, cqB, cqLender} {
		if err := cache.AddClusterQueue(ctx, cq); err != nil {
			t.Fatalf("Failed to add ClusterQueue %s to cache: %v", cq.Name, err)
		}
	}
	if err := cache.AddOrUpdateCohort(utiltestingapi.MakeCohort("iter-cohort").Obj()); err != nil {
		t.Fatalf("Failed to add cohort to cache: %v", err)
	}
	snapshot, err := cache.Snapshot(ctx)
	if err != nil {
		t.Fatalf("unexpected error while building snapshot: %v", err)
	}
	snapA := snapshot.ClusterQueue("iter-a")
	snapB := snapshot.ClusterQueue("iter-b")
	if snapA == nil || snapB == nil {
		t.Fatalf("Failed to look up ClusterQueue snapshots")
	}
	cpu := func(milli int64) workload.Usage {
		return workload.Usage{Quota: resources.FlavorResourceQuantities{
			{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(milli),
		}}
	}
	// iter-a borrows 1 CPU, including the second-pass workload's reservation;
	// iter-b uses none of its quota.
	snapA.AddUsage(cpu(3_000))

	makeEntry := func(wl *kueue.Workload, cq *schdcache.ClusterQueueSnapshot, milli int64) entry {
		return entry{
			Head:                 qcache.Head{Info: *workload.NewInfo(log, wl)},
			assignment:           flavorassigner.Assignment{Usage: cpu(milli)},
			clusterQueueSnapshot: cq,
		}
	}
	// Ranked with its current share: iter-a borrows 1 CPU. It is the oldest
	// workload, so it would win a tie on DRS.
	secondPass := makeEntry(utiltestingapi.MakeWorkload("a-second-pass", "default").
		Creation(now.Add(-3*time.Minute)).
		SimpleReserveQuota("iter-a", "default", now).
		Obj(), snapA, 1_000)
	headA := makeEntry(utiltestingapi.MakeWorkload("a-head", "default").
		Creation(now.Add(-2*time.Minute)).
		Obj(), snapA, 1_000)
	// Fits in iter-b's nominal quota, so iter-b does not borrow.
	headB := makeEntry(utiltestingapi.MakeWorkload("b-head", "default").
		Creation(now.Add(-time.Minute)).
		Obj(), snapB, 1_000)

	iterator := makeFairSharingIterator(ctx, []entry{headA, headB, secondPass},
		workload.Ordering{PodsReadyRequeuingTimestamp: config.CreationTimestamp})
	var got []string
	for iterator.hasNext() {
		got = append(got, iterator.pop().Obj.Name)
	}
	// iter-b does not borrow, so its head goes before iter-a's nominee, which
	// borrows 1 CPU.
	want := []string{"b-head", "a-second-pass", "a-head"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Unexpected pop order (-want,+got):\n%s", diff)
	}
}
