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
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/tas/indexer"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

// BenchmarkSnapshot benchmarks the Snapshot method with different scales of data.
func BenchmarkSnapshot(b *testing.B) {
	ctx, log := utiltesting.ContextWithLog(b)

	benchmarks := map[string]struct {
		numCqs            int
		numCohorts        int
		numRfs            int
		numWlsPerCq       int
		numResourceGroups int
		numNodes          int
	}{
		"Small":  {5, 2, 3, 10, 2, 100},
		"Medium": {50, 10, 20, 50, 3, 1000},
		"Large":  {200, 50, 100, 100, 5, 5000},
		"XLarge": {500, 100, 200, 200, 8, 20000},
	}

	for name, bm := range benchmarks {
		b.Run(name, func(b *testing.B) {
			cache := setupBenchmarkCache(b, ctx, log, bm.numCqs, bm.numCohorts, bm.numRfs, bm.numWlsPerCq, bm.numResourceGroups, bm.numNodes)

			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				snapshot, err := cache.Snapshot(ctx)
				if err != nil {
					b.Fatalf("Failed to create snapshot: %v", err)
				}
				// Prevent compiler optimization
				_ = snapshot
			}
		})
	}
}

// setupBenchmarkCache creates a cache with the specified number of objects for benchmarking.
func setupBenchmarkCache(b *testing.B, ctx context.Context, log logr.Logger, numCqs, numCohorts, numRfs, numWlsPerCq, numResourceGroups, numNodes int) *Cache {
	clientBuilder := utiltesting.NewClientBuilder()

	if err := indexer.SetupIndexes(ctx, utiltesting.AsIndexer(clientBuilder)); err != nil {
		b.Fatalf("Failed to setup TAS indexer: %v", err)
	}

	fakeClient := clientBuilder.Build()
	cache := New(fakeClient)

	// Create resource flavors with topology for TAS
	for i := 0; i < numRfs; i++ {
		rf := utiltestingapi.MakeResourceFlavor(fmt.Sprintf("flavor-%d", i)).
			NodeLabel("tas-node-group", "true").
			TopologyName("default-topology").
			Obj()
		cache.AddOrUpdateResourceFlavor(log, rf)
	}

	topology := utiltestingapi.MakeTopology("default-topology").
		Levels("cloud.provider.com/topology-block", "cloud.provider.com/topology-rack").
		Obj()
	cache.AddOrUpdateTopology(log, topology)

	for i := 0; i < numCohorts; i++ {
		cohort := utiltestingapi.MakeCohort(kueue.CohortReference(fmt.Sprintf("cohort-%d", i)))

		// Add resource groups to cohorts
		for j := 0; j < numResourceGroups && j < numRfs; j++ {
			flavorQuotas := *utiltestingapi.MakeFlavorQuotas(fmt.Sprintf("flavor-%d", j)).
				Resource(corev1.ResourceCPU, "1000").
				Resource(corev1.ResourceMemory, "100Gi").
				Obj()
			cohort.ResourceGroup(flavorQuotas)
		}

		cache.AddOrUpdateCohort(cohort.Obj())
	}

	for i := 0; i < numNodes; i++ {
		block := i % 1000     // 1000 blocks
		rack := i % 1000 / 10 // 100 racks per block (1000/10)
		nodeInRack := i % 60  // 60 nodes per rack

		node := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: fmt.Sprintf("node-%d", i),
				Labels: map[string]string{
					"cloud.provider.com/topology-block": fmt.Sprintf("block-%d", block),
					"cloud.provider.com/topology-rack":  fmt.Sprintf("rack-%d", rack),
					"kubernetes.io/hostname":            fmt.Sprintf("node-%d", nodeInRack),
					"tas-node-group":                    "true", // For TAS flavor matching
					"kubernetes.io/arch":                "amd64",
					"kubernetes.io/os":                  "linux",
				},
			},
			Spec: corev1.NodeSpec{
				Unschedulable: false,
				Taints: []corev1.Taint{
					{
						Key:    "dedicated",
						Value:  fmt.Sprintf("flavor-%d", i%numRfs),
						Effect: corev1.TaintEffectNoSchedule,
					},
				},
			},
			Status: corev1.NodeStatus{
				Conditions: []corev1.NodeCondition{
					{
						Type:   corev1.NodeReady,
						Status: corev1.ConditionTrue,
					},
				},
				Allocatable: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("4"),
					corev1.ResourceMemory: resource.MustParse("16Gi"),
					corev1.ResourcePods:   resource.MustParse("110"),
				},
			},
		}

		err := cache.client.Create(ctx, node)
		if err != nil {
			b.Fatalf("Failed to create node %d: %v", i, err)
		}

		cache.tasCache.SyncNode(node)
	}

	now := time.Now().Truncate(time.Second)

	// Create cluster queues and workloads
	for i := 0; i < numCqs; i++ {
		cqBuilder := utiltestingapi.MakeClusterQueue(fmt.Sprintf("cq-%d", i))

		if numCohorts > 0 {
			cqBuilder.Cohort(kueue.CohortReference(fmt.Sprintf("cohort-%d", i%numCohorts)))
		}

		for j := 0; j < numResourceGroups && j < numRfs; j++ {
			flavorQuotas := *utiltestingapi.MakeFlavorQuotas(fmt.Sprintf("flavor-%d", j)).
				Resource(corev1.ResourceCPU, "100").
				Resource(corev1.ResourceMemory, "10Gi").
				Obj()
			cqBuilder.ResourceGroup(flavorQuotas)
		}

		cq := cqBuilder.Obj()
		if err := cache.AddClusterQueue(ctx, cq); err != nil {
			b.Fatalf("Failed adding ClusterQueue %d: %v", i, err)
		}

		for j := 0; j < numWlsPerCq; j++ {
			wl := utiltestingapi.MakeWorkload(fmt.Sprintf("wl-%d-%d", i, j), "default").
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					Request(corev1.ResourceCPU, "100m").
					Request(corev1.ResourceMemory, "100Mi").
					Obj()).
				ReserveQuotaAt(&kueue.Admission{ClusterQueue: kueue.ClusterQueueReference(cq.Name)}, now).
				Obj()
			cache.AddOrUpdateWorkload(log, wl)
		}
	}

	return cache
}
