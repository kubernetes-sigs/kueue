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
	"k8s.io/apimachinery/pkg/api/resource"

	"sigs.k8s.io/kueue/pkg/resources"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

// The two paths an exact Amount costs something on: the fold of a workload's
// usage into a queue's, which every admission and removal runs, and the clone
// a snapshot takes. Written against the API both sides of this change carry, so
// the numbers can be compared with benchstat.

var benchNode resourceNode

func benchQuantities(n int) resources.FlavorResourceQuantities {
	frq := make(resources.FlavorResourceQuantities, n)
	for i := range n {
		fr := resources.FlavorResource{Flavor: "f", Resource: corev1.ResourceName(string(rune('a' + i)))}
		frq[fr] = resources.NewAmount(int64(i+1) << 30)
	}
	return frq
}

func BenchmarkUpdateFlavorUsage(b *testing.B) {
	delta := benchQuantities(16)
	total := benchQuantities(16)
	for b.Loop() {
		updateFlavorUsage(delta, total, add)
		updateFlavorUsage(delta, total, subtract)
	}
}

func BenchmarkResourceNodeClone(b *testing.B) {
	n := NewResourceNode()
	n.Usage = benchQuantities(16)
	n.SubtreeQuota = benchQuantities(16)
	var out resourceNode
	for b.Loop() {
		out = n.Clone()
	}
	benchNode = out
}

// The fair-sharing tournament computes a share for the candidate's
// ClusterQueue and for every ancestor up to the root, for every candidate. The
// operands are inside int64 in the small case and past it in the large one.

var benchDRS DRS

func benchFairSharing(b *testing.B, lenderQuota string) (dominantResourceShareNode, dominantResourceShareNode, resources.FlavorResourceQuantities) {
	b.Helper()
	ctx, log := utiltesting.ContextWithLog(b)
	cache := New(utiltesting.NewFakeClient())
	cache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("default").Obj())
	if err := cache.AddOrUpdateCohort(utiltestingapi.MakeCohort("cohort").Obj()); err != nil {
		b.Fatalf("AddOrUpdateCohort() = %v", err)
	}
	for name, quota := range map[string]string{"lender": lenderQuota, "borrower": "0"} {
		cq := utiltestingapi.MakeClusterQueue(name).
			Cohort("cohort").
			NamespaceSelector(nil).
			FairWeight(resource.MustParse("1")).
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
				ResourceQuotaWrapper("cpu").NominalQuota(quota).Append().Obj()).
			Obj()
		if err := cache.AddClusterQueue(ctx, cq); err != nil {
			b.Fatalf("AddClusterQueue(%s) = %v", name, err)
		}
	}
	snapshot, err := cache.Snapshot(ctx)
	if err != nil {
		b.Fatalf("Snapshot() = %v", err)
	}
	var cohort dominantResourceShareNode
	for _, c := range snapshot.Cohorts() {
		cohort = c
	}
	req := resources.FlavorResourceQuantities{
		{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(1_000),
	}
	return snapshot.ClusterQueue("borrower"), cohort, req
}

func BenchmarkDominantResourceShareSmall(b *testing.B) {
	cq, _, req := benchFairSharing(b, "1000")
	for b.Loop() {
		benchDRS = dominantResourceShare(cq, req)
	}
}

func BenchmarkDominantResourceShareLarge(b *testing.B) {
	cq, _, req := benchFairSharing(b, "1E")
	for b.Loop() {
		benchDRS = dominantResourceShare(cq, req)
	}
}

// The whole walk the tournament takes for one candidate.
func BenchmarkFairSharingComputeDRS(b *testing.B) {
	cq, cohort, req := benchFairSharing(b, "1000")
	for b.Loop() {
		benchDRS = dominantResourceShare(cq, req)
		benchDRS = dominantResourceShare(cohort, req)
	}
}
