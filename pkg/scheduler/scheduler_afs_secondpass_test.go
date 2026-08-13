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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	testingclock "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	tasindexer "sigs.k8s.io/kueue/pkg/controller/tas/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
	utilqueue "sigs.k8s.io/kueue/pkg/util/queue"
	"sigs.k8s.io/kueue/pkg/util/routine"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	"sigs.k8s.io/kueue/pkg/workload"
)

// TestSecondPassDoesNotRepushEntryPenalty exercises the real scheduler second-pass
// path (delayed topology assignment) for an AdmissionFairSharing ClusterQueue and
// asserts that re-assuming a workload which already holds a quota reservation does not
// push another entry penalty.
//
// This is the end-to-end companion to the predicate-level TestShouldApplyEntryPenalty:
// it proves the second-pass re-assume actually reaches the push site, so the
// HasQuotaReservation guard is load-bearing rather than merely correct in isolation.
// Without the guard the second-pass assume would re-push the penalty; the ledger's
// replace semantics would absorb the duplicate, but the guard keeps the push and
// its rollback paired to one reservation.
func TestSecondPassDoesNotRepushEntryPenalty(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.TopologyAwareScheduling, true)
	features.SetFeatureGateDuringTest(t, features.AdmissionFairSharing, true)

	now := time.Now().Truncate(time.Second)
	fakeClock := testingclock.NewFakeClock(now)
	afsConfig := &config.AdmissionFairSharing{
		UsageHalfLifeTime:     metav1.Duration{Duration: 10 * time.Second},
		UsageSamplingInterval: metav1.Duration{Duration: 1 * time.Second},
	}

	topology := *utiltestingapi.MakeDefaultOneLevelTopology("tas-single-level")
	tasFlavor := *utiltestingapi.MakeResourceFlavor("tas-default").
		NodeLabel("tas-node", "true").
		TopologyName("tas-single-level").
		Obj()
	node := *testingnode.MakeNode("x1").
		Label("tas-node", "true").
		Label(corev1.LabelHostname, "x1").
		StatusAllocatable(corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("1"),
			corev1.ResourceMemory: resource.MustParse("1Gi"),
			corev1.ResourcePods:   resource.MustParse("10"),
		}).
		Ready().
		Obj()
	provCheck := *utiltestingapi.MakeAdmissionCheck("prov-check").
		ControllerName(kueue.ProvisioningRequestControllerName).
		Condition(metav1.Condition{Type: kueue.AdmissionCheckActive, Status: metav1.ConditionTrue}).
		Obj()
	cq := *utiltestingapi.MakeClusterQueue("cq").
		ResourceGroup(*utiltestingapi.MakeFlavorQuotas("tas-default").
			Resource(corev1.ResourceCPU, "50").
			Resource(corev1.ResourceMemory, "50Gi").Obj()).
		AdmissionChecks(kueue.AdmissionCheckReference(provCheck.Name)).
		AdmissionMode(kueue.UsageBasedAdmissionFairSharing).
		Obj()
	lq := *utiltestingapi.MakeLocalQueue("lq", "default").
		FairSharing(&kueue.FairSharing{Weight: new(resource.MustParse("1"))}).
		ClusterQueue("cq").
		Obj()

	// A workload mid-second-pass: it already holds quota from the first pass (which
	// pushed and settled its entry penalty already), all admission checks are ready,
	// and only the delayed topology assignment is still pending. The second pass
	// re-assumes it to complete the assignment.
	wl := *utiltestingapi.MakeWorkload("foo", "default").
		Queue("lq").
		PodSets(*utiltestingapi.MakePodSet("one", 1).
			RequiredTopologyRequest(corev1.LabelHostname).
			Request(corev1.ResourceCPU, "1").
			Obj()).
		ReserveQuotaAt(
			utiltestingapi.MakeAdmission("cq").
				PodSets(utiltestingapi.MakePodSetAssignment("one").
					Assignment(corev1.ResourceCPU, "tas-default", "1000m").
					DelayedTopologyRequest(kueue.DelayedTopologyRequestStatePending).
					Obj()).
				Obj(), now).
		AdmissionCheck(kueue.AdmissionCheckState{Name: "prov-check", State: kueue.CheckStateReady}).
		Obj()

	ctx, log := utiltesting.ContextWithLog(t)

	clientBuilder := utiltesting.NewClientBuilder().
		WithLists(
			&kueue.WorkloadList{Items: []kueue.Workload{wl}},
			&kueue.ClusterQueueList{Items: []kueue.ClusterQueue{cq}},
			&kueue.LocalQueueList{Items: []kueue.LocalQueue{lq}}).
		WithObjects(utiltesting.MakeNamespace("default"), &provCheck).
		WithStatusSubresource(&kueue.Workload{}).
		WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: utiltesting.TreatSSAAsStrategicMerge})
	if err := tasindexer.SetupIndexes(ctx, utiltesting.AsIndexer(clientBuilder)); err != nil {
		t.Fatalf("setting up TAS indexes: %v", err)
	}
	cl := clientBuilder.Build()

	recorder := &utiltesting.EventRecorder{}
	cqCache := schdcache.New(cl, schdcache.WithFairSharing(true), schdcache.WithAdmissionFairSharing(afsConfig))
	qManager := qcache.NewManagerForUnitTests(cl, cqCache, qcache.WithClock(fakeClock), qcache.WithAdmissionFairSharing(afsConfig))

	cqCache.TASCache().SyncNode(&node)
	cqCache.AddOrUpdateAdmissionCheck(log, &provCheck)
	cqCache.AddOrUpdateResourceFlavor(log, &tasFlavor)
	cqCache.AddOrUpdateTopology(log, &topology)
	if err := cqCache.AddClusterQueue(ctx, &cq); err != nil {
		t.Fatalf("inserting clusterQueue in cache: %v", err)
	}
	if err := qManager.AddClusterQueue(ctx, &cq); err != nil {
		t.Fatalf("inserting clusterQueue in manager: %v", err)
	}
	if err := qManager.AddLocalQueue(ctx, &lq); err != nil {
		t.Fatalf("inserting localQueue in manager: %v", err)
	}
	// The reserved workload contributes its usage to the cache, mirroring production.
	cqCache.AddOrUpdateWorkload(log, &wl)
	if !qManager.QueueSecondPassIfNeeded(ctx, &wl, 0) {
		t.Fatal("expected the workload to be queued for a second pass")
	}
	fakeClock.Step(time.Second)

	scheduler := New(qManager, cqCache, cl, recorder,
		WithFairSharing(&config.FairSharing{}),
		WithAdmissionFairSharing(afsConfig),
		WithClock(t, fakeClock),
		WithPreemptionExpectations(preemptexpectations.New()))
	wg := sync.WaitGroup{}
	scheduler.setAdmissionRoutineWrapper(routine.NewWrapper(
		func() { wg.Add(1) },
		func() { wg.Done() },
	))

	schedCtx, cancel := context.WithTimeout(ctx, queueingTimeout)
	defer cancel()
	go qManager.CleanUpOnContext(schedCtx)

	scheduler.schedule(schedCtx)
	wg.Wait()

	// Guard against a vacuous test: the second pass must actually have run and admitted
	// the workload (completing the topology assignment), otherwise no re-assume happened
	// and the penalty assertion below would pass trivially.
	gotWl := &kueue.Workload{}
	if err := cl.Get(ctx, client.ObjectKeyFromObject(&wl), gotWl); err != nil {
		t.Fatalf("getting workload after scheduling: %v", err)
	}
	if !workload.IsAdmitted(gotWl) {
		t.Fatalf("expected the second pass to admit the workload, but it is not admitted: %+v", gotWl.Status.Conditions)
	}

	lqKey := utilqueue.NewLocalQueueReference("default", "lq")
	if qManager.AfsUsageLedger.HasPendingPenalty(lqKey) {
		t.Errorf("second pass pushed a duplicate entry penalty for an already-reserved workload; pending penalty = %v, want empty",
			qManager.AfsUsageLedger.PeekPenalty(lqKey))
	}
}
