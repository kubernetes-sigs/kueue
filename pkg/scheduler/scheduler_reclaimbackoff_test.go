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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/clock"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption"
	"sigs.k8s.io/kueue/pkg/scheduler/reclaimbackoff"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestIsReclaimReason(t *testing.T) {
	cases := map[string]struct {
		reason string
		want   bool
	}{
		"in-cohort reclamation":                 {reason: kueue.InCohortReclamationReason, want: true},
		"in-cohort reclaim while borrowing":     {reason: kueue.InCohortReclaimWhileBorrowingReason, want: true},
		"same-clusterqueue priority preemption": {reason: kueue.InClusterQueueReason, want: false},
		"empty reason":                          {reason: "", want: false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := isReclaimReason(tc.reason); got != tc.want {
				t.Errorf("isReclaimReason(%q) = %v, want %v", tc.reason, got, tc.want)
			}
		})
	}
}

// TestBorrowedFlavorResources verifies that only the dimensions the victim
// actually occupies AND that its ClusterQueue is borrowing at scheduling time
// are returned — resources within nominal quota must not be included.
func TestBorrowedFlavorResources(t *testing.T) {
	ctx, log := utiltesting.ContextWithLog(t)

	cpuFR := resources.FlavorResource{Flavor: "default", Resource: corev1.ResourceCPU}
	memFR := resources.FlavorResource{Flavor: "default", Resource: corev1.ResourceMemory}

	// test-cq has nominal cpu=2, mem=8Gi; the cohort provides spare capacity so
	// usage above nominal counts as borrowing.
	cohort := utiltestingapi.MakeCohort("root").
		ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
			Resource(corev1.ResourceCPU, "8").
			Resource(corev1.ResourceMemory, "32Gi").Obj()).Obj()
	testCq := *utiltestingapi.MakeClusterQueue("test-cq").
		Cohort("root").
		ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
			Resource(corev1.ResourceCPU, "2").
			Resource(corev1.ResourceMemory, "8Gi").Obj()).
		Obj()

	cache := schdcache.New(utiltesting.NewFakeClient())
	if err := cache.AddOrUpdateCohort(cohort); err != nil {
		t.Fatalf("Couldn't add Cohort to cache: %v", err)
	}
	if err := cache.AddClusterQueue(ctx, &testCq); err != nil {
		t.Fatalf("Failed to add CQ to cache: %v", err)
	}
	cache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("default").Obj())

	snapshot, err := cache.Snapshot(ctx)
	if err != nil {
		t.Fatalf("unexpected error while building snapshot: %v", err)
	}
	cq := snapshot.ClusterQueue("test-cq")
	// Push cpu into borrowing (4 > nominal 2) while mem stays within nominal (4Gi < 8Gi).
	cq.AddUsage(workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{
		cpuFR: resources.NewAmount(4_000),
		memFR: resources.NewAmount(resources.ResourceValue(corev1.ResourceMemory, resource.MustParse("4Gi"))),
	}}})

	victim := &workload.Info{
		TotalRequests: []workload.PodSetResources{{
			Name: kueue.DefaultPodSetName,
			Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
				corev1.ResourceCPU:    "default",
				corev1.ResourceMemory: "default",
			},
		}},
	}

	got := borrowedFlavorResources(victim, cq)
	if !got.Has(cpuFR) {
		t.Errorf("expected cpu to be reported as borrowed, got %v", got.UnsortedList())
	}
	if got.Has(memFR) {
		t.Errorf("mem is within nominal quota and must not be reported as borrowed, got %v", got.UnsortedList())
	}
	if got.Len() != 1 {
		t.Errorf("expected exactly one borrowed FlavorResource, got %v", got.UnsortedList())
	}
}

// TestRecordReclaimBackoffOnlyArmsIssuedTargets verifies that only preemption
// targets whose eviction was actually issued in the current cycle arm the
// backoff: targets skipped by IssuePreemptions (already evicted, awaiting
// observation, or failed to evict) must not grow the cooldown.
func TestRecordReclaimBackoffOnlyArmsIssuedTargets(t *testing.T) {
	ctx, log := utiltesting.ContextWithLog(t)

	cpuFR := resources.FlavorResource{Flavor: "default", Resource: corev1.ResourceCPU}

	cohort := utiltestingapi.MakeCohort("root").
		ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
			Resource(corev1.ResourceCPU, "16").Obj()).Obj()
	cqA := *utiltestingapi.MakeClusterQueue("cq-a").
		Cohort("root").
		ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
			Resource(corev1.ResourceCPU, "2").Obj()).
		Obj()
	cqB := *utiltestingapi.MakeClusterQueue("cq-b").
		Cohort("root").
		ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
			Resource(corev1.ResourceCPU, "2").Obj()).
		Obj()

	cache := schdcache.New(utiltesting.NewFakeClient())
	if err := cache.AddOrUpdateCohort(cohort); err != nil {
		t.Fatalf("Couldn't add Cohort to cache: %v", err)
	}
	for _, cq := range []*kueue.ClusterQueue{&cqA, &cqB} {
		if err := cache.AddClusterQueue(ctx, cq); err != nil {
			t.Fatalf("Failed to add CQ to cache: %v", err)
		}
	}
	cache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("default").Obj())

	snapshot, err := cache.Snapshot(ctx)
	if err != nil {
		t.Fatalf("unexpected error while building snapshot: %v", err)
	}
	// Push both CQs into borrowing (4 > nominal 2).
	for _, cqName := range []string{"cq-a", "cq-b"} {
		snapshot.ClusterQueue(kueue.ClusterQueueReference(cqName)).AddUsage(workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{
			cpuFR: resources.NewAmount(4_000),
		}}})
	}

	victim := func(name, cqName string) *workload.Info {
		return &workload.Info{
			Obj: utiltestingapi.MakeWorkload(name, "ns").Obj(),
			TotalRequests: []workload.PodSetResources{{
				Name: kueue.DefaultPodSetName,
				Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
					corev1.ResourceCPU: "default",
				},
			}},
			ClusterQueue: kueue.ClusterQueueReference(cqName),
		}
	}
	victimA := victim("victim-a", "cq-a")
	victimB := victim("victim-b", "cq-b")

	targets := []*preemption.Target{
		{WorkloadInfo: victimA, Reason: kueue.InCohortReclamationReason, WorkloadCq: snapshot.ClusterQueue("cq-a")},
		{WorkloadInfo: victimB, Reason: kueue.InCohortReclamationReason, WorkloadCq: snapshot.ClusterQueue("cq-b")},
	}
	// Only victim-a's eviction was issued this cycle; victim-b's failed.
	issuedTargets := sets.New(types.NamespacedName{Name: victimA.Obj.Name, Namespace: victimA.Obj.Namespace})

	tracker := reclaimbackoff.New(time.Minute, time.Hour, 10*time.Minute, clock.RealClock{})
	s := &Scheduler{
		reclaimBackoff: tracker,
		queues:         qcache.NewManagerForUnitTests(utiltesting.NewFakeClient(), nil),
	}
	s.recordReclaimBackoff(log, targets, issuedTargets)

	if !tracker.IsBackingOff("cq-a", cpuFR) {
		t.Error("expected cq-a to be backing off: its eviction was issued this cycle")
	}
	if tracker.IsBackingOff("cq-b", cpuFR) {
		t.Error("expected cq-b not to be backing off: its eviction failed, nothing was reclaimed")
	}
}
