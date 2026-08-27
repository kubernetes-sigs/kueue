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
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	testingclock "k8s.io/utils/clock/testing"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
	"sigs.k8s.io/kueue/pkg/util/routine"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

// TestNominateReleasesHeadAlreadyAccountedInCache covers the one exit where a
// popped head leaves the cycle without being requeued or deleted: its
// nomination is dropped because the scheduler cache already accounts for it.
// With inflight claims keyed per workload, nothing else would release that
// claim, and the workload could never be queued again.
//
// Not expressible as a scheduleTestCase: the harness derives the queues and
// the scheduler cache from one workload list, so no workload can be pending in
// the queue and admitted in the cache at the same time.
func TestNominateReleasesHeadAlreadyAccountedInCache(t *testing.T) {
	ctx, log := utiltesting.ContextWithLog(t)

	now := time.Now().Truncate(time.Second)
	flavor := utiltestingapi.MakeResourceFlavor("default").Obj()
	cq := utiltestingapi.MakeClusterQueue("cq").
		ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
			Resource(corev1.ResourceCPU, "8", "0").Obj()).
		Obj()
	lq := utiltestingapi.MakeLocalQueue("lq", "default").ClusterQueue("cq").Obj()
	staleWrapper := utiltestingapi.MakeWorkload("stale", "default").
		Queue("lq").
		Creation(now.Add(-time.Minute)).
		PodSets(*utiltestingapi.MakePodSet("one", 1).
			Request(corev1.ResourceCPU, "1").
			Obj())
	stale := staleWrapper.Obj()

	cl := utiltesting.NewClientBuilder().
		WithLists(
			&kueue.WorkloadList{Items: []kueue.Workload{*stale}},
			&kueue.LocalQueueList{Items: []kueue.LocalQueue{*lq}},
		).
		WithObjects(utiltesting.MakeNamespaceWrapper("default").Obj()).
		WithStatusSubresource(&kueue.Workload{}).
		Build()

	cqCache := schdcache.New(cl)
	qManager := qcache.NewManagerForUnitTests(cl, cqCache,
		qcache.WithPreemptionExpectations(preemptexpectations.New()))
	cqCache.AddOrUpdateResourceFlavor(log, flavor)
	if err := cqCache.AddClusterQueue(ctx, cq); err != nil {
		t.Fatalf("Inserting clusterQueue in cache: %v", err)
	}
	if err := qManager.AddClusterQueue(ctx, cq); err != nil {
		t.Fatalf("Inserting clusterQueue in manager: %v", err)
	}
	if err := qManager.AddLocalQueue(ctx, lq); err != nil {
		t.Fatalf("Inserting localQueue in manager: %v", err)
	}

	// The queue keeps the pending copy it was loaded with, which is the state
	// Heads observes when the workload was admitted between the cycle's
	// snapshot and the pop.
	admittedStale := staleWrapper.Clone().
		ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").PodSets(
			utiltestingapi.MakePodSetAssignment("one").
				Assignment(corev1.ResourceCPU, "default", "1").Count(1).Obj(),
		).Obj(), now).
		Obj()
	if !cqCache.AddOrUpdateWorkload(log, admittedStale) {
		t.Fatal("Failed to account the workload in the scheduler cache")
	}

	scheduler := New(qManager, cqCache, cl, &utiltesting.EventRecorder{},
		WithClock(t, testingclock.NewFakeClock(now)),
		WithPreemptionExpectations(preemptexpectations.New()),
	)
	wg := sync.WaitGroup{}
	scheduler.setAdmissionRoutineWrapper(routine.NewWrapper(
		func() { wg.Add(1) },
		func() { wg.Done() },
	))
	ctx, cancel := context.WithTimeout(ctx, queueingTimeout)
	defer cancel()
	go qManager.CleanUpOnContext(ctx)

	scheduler.schedule(ctx)
	wg.Wait()

	if got := qManager.Dump()["cq"]; len(got) != 0 {
		t.Errorf("Workloads left on the heap after the dropped nomination: %v", got)
	}
	if got := qManager.DumpInflight()["cq"]; len(got) != 0 {
		t.Errorf("Inflight claims left after the dropped nomination: %v", got)
	}

	// The workload controller re-adds the workload once it is pending again.
	// A claim left behind by the drop would make this a no-op.
	if err := qManager.AddOrUpdateWorkload(log, stale.DeepCopy()); err != nil {
		t.Fatalf("Re-adding the workload: %v", err)
	}
	want := []workload.Reference{"default/stale"}
	if diff := cmp.Diff(want, qManager.Dump()["cq"], cmpDump); diff != "" {
		t.Errorf("The workload did not return to the queue (-want,+got):\n%s", diff)
	}
}
