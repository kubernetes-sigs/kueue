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
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/scheduler/flavorassigner"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

// TestFairSharingIteratorPush pins the contract push relies on: an entry
// inserted into a running iteration is ranked against the snapshot state at
// the time of the next pop, because pop recomputes the per-entry fair-sharing
// values on every call. The scenario mirrors an admission: cq-a's head is
// popped, cq-a's snapshot usage grows past its nominal quota, and cq-a's next
// workload is pushed. Ranked by the pre-admission state (or by FIFO, being
// older), a2 would win the next pop; against the fresh state cq-a is
// borrowing, so cq-b's newer workload wins. Mid-cycle re-entry consumers
// beyond refill depend on the same contract.
func TestFairSharingIteratorPush(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	ctx, log := utiltesting.ContextWithLog(t)

	cqA := utiltestingapi.MakeClusterQueue("iter-a").
		Cohort("iter-cohort").
		ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
			Resource(corev1.ResourceCPU, "2", "8").Obj()).
		Obj()
	cqB := utiltestingapi.MakeClusterQueue("iter-b").
		Cohort("iter-cohort").
		ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
			Resource(corev1.ResourceCPU, "8").Obj()).
		Obj()

	cache := schdcache.New(utiltesting.NewFakeClient())
	cache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("default").Obj())
	for _, cq := range []*kueue.ClusterQueue{cqA, cqB} {
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

	// Entries carry an assignment, as pushed production entries do; its usage
	// is added to the entry's ClusterQueue during ranking.
	makeEntry := func(name string, cq *schdcache.ClusterQueueSnapshot, creation time.Time) entry {
		wl := utiltestingapi.MakeWorkload(name, "default").
			Creation(creation).
			PodSets(*utiltestingapi.MakePodSet("one", 1).
				Request(corev1.ResourceCPU, "1").
				Obj()).
			Obj()
		return entry{
			Info: *workload.NewInfo(wl),
			assignment: flavorassigner.Assignment{
				Usage: workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{
					{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(1_000),
				}}},
			},
			clusterQueueSnapshot: cq,
		}
	}
	// FIFO favors cq-a's workloads: all are older than cq-b's.
	entryA1 := makeEntry("a1", snapA, now.Add(-4*time.Minute))
	entryA2 := makeEntry("a2", snapA, now.Add(-3*time.Minute))
	entryB1 := makeEntry("b1", snapB, now.Add(-2*time.Minute))
	entryB2 := makeEntry("b2", snapB, now.Add(-time.Minute))

	iterator := makeFairSharingIterator(ctx, []entry{entryA1, entryB1},
		workload.Ordering{PodsReadyRequeuingTimestamp: config.CreationTimestamp})

	popNames := []string{iterator.pop().Obj.Name}

	// Simulate the effect of admitting a1: cq-a's usage grows to 3 CPU,
	// 1 above its nominal quota, so cq-a is now borrowing and its DRS is
	// positive while cq-b's stays zero.
	snapA.AddUsage(workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{
		{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(3_000),
	}}})
	iterator.push(&entryA2)

	for iterator.hasNext() {
		popNames = append(popNames, iterator.pop().Obj.Name)
	}

	// Refill's other production shape: a push after the cycle's last entry.
	if iterator.hasNext() {
		t.Fatal("Iterator is not drained after popping all entries")
	}
	iterator.push(&entryB2)
	if !iterator.hasNext() {
		t.Fatal("Iterator does not resume after a push into the drained state")
	}
	popNames = append(popNames, iterator.pop().Obj.Name)

	// a2 is older than b1, so FIFO alone would pop it first; only the
	// fair-sharing values recomputed after the usage change put b1 ahead.
	wantOrder := []string{"a1", "b1", "a2", "b2"}
	if diff := cmp.Diff(wantOrder, popNames); diff != "" {
		t.Errorf("Unexpected pop order (-want,+got):\n%s", diff)
	}
}
