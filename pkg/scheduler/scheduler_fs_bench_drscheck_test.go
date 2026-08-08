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

	corev1 "k8s.io/api/core/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

// TestFairSharingFixtureHasDistinctShares asserts that the benchmark fixture
// produces ClusterQueues with different DominantResourceShare values. With every
// share zero the tournament degenerates into FIFO with randomized tie-breaking,
// and BenchmarkSchedulerFairSharing would be measuring that instead.
func TestFairSharingFixtureHasDistinctShares(t *testing.T) {
	for _, topology := range []string{"flat", "roots", "subtree"} {
		t.Run(topology, func(t *testing.T) {
			roots, cqsPerRoot := 8, 4
			if topology == "flat" {
				roots, cqsPerRoot = 1, 32
			}
			f := makeFairSharingFixture(topology, roots, cqsPerRoot, 6)

			ctx, log := utiltesting.ContextWithLog(t)
			cl := utiltesting.NewClientBuilder(kueue.AddToScheme, corev1.AddToScheme).
				WithObjects(utiltesting.MakeNamespaceWrapper("default").Obj()).
				WithLists(
					&kueue.WorkloadList{Items: f.pendingWorkloads},
					&kueue.LocalQueueList{Items: f.localQueues},
					&kueue.ClusterQueueList{Items: f.clusterQueues},
				).
				WithStatusSubresource(&kueue.Workload{}).
				Build()

			cqCache := schdcache.New(cl, schdcache.WithFairSharing(true))
			cqCache.AddOrUpdateResourceFlavor(log, f.flavor)
			for i := range f.cohorts {
				if err := cqCache.AddOrUpdateCohort(&f.cohorts[i]); err != nil {
					t.Fatalf("adding cohort: %v", err)
				}
			}
			for i := range f.clusterQueues {
				if err := cqCache.AddClusterQueue(ctx, &f.clusterQueues[i]); err != nil {
					t.Fatalf("adding ClusterQueue: %v", err)
				}
			}
			for i := range f.admittedWorkloads {
				if !cqCache.AddOrUpdateWorkload(log, &f.admittedWorkloads[i]) {
					t.Fatalf("adding admitted workload %s", f.admittedWorkloads[i].Name)
				}
			}

			snapshot, err := cqCache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("snapshot: %v", err)
			}

			shares := make(map[float64]int)
			borrowing := 0
			for name, cq := range snapshot.ClusterQueues() {
				drs := cq.DominantResourceShare()
				share := drs.PreciseWeightedShare()
				shares[share]++
				if share > 0 {
					borrowing++
				}
				t.Logf("%s share=%v", name, share)
			}
			if borrowing == 0 {
				t.Fatalf("no ClusterQueue is borrowing: every DominantResourceShare is zero, "+
					"so the tournament is degenerate (shares=%v)", shares)
			}
			if len(shares) < 3 {
				t.Fatalf("expected at least 3 distinct shares (zero plus two borrowing levels), got %v", shares)
			}
		})
	}
}
