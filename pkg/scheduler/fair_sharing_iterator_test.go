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

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
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
			Info:                 *workload.NewInfo(wl),
			clusterQueueSnapshot: cq,
		})
	}

	iterator := makeFairSharingIterator(t.Context(), entries, workload.Ordering{})
	var got []string
	for iterator.hasNext() {
		got = append(got, iterator.pop().Obj.Name)
	}
	// The entry with quota already reserved goes first, the remaining ones
	// follow in FIFO order.
	want := []string{"second-pass", "head-old", "head-new"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("unexpected pop order (-want,+got):\n%s", diff)
	}
}
