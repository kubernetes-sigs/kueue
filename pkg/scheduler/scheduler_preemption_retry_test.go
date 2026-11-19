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
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/component-base/featuregate"
	testingclock "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/scheduler/flavorassigner"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
)

func TestRequeueAndUpdateRetryAt(t *testing.T) {
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)

	cases := map[string]struct {
		cohortName             kueue.CohortReference
		requeueReason          qcache.RequeueReason
		retryAt                *time.Time
		beforeRequeue          func(context.Context, client.Client, *kueue.Workload) error
		wantMoved              int
		wantClusterQueueDelays map[kueue.ClusterQueueReference]time.Duration
		wantCohortDelays       map[kueue.CohortReference]time.Duration
	}{
		"PreemptionNoCandidates with retryAt on standalone ClusterQueue arms ClusterQueue delay": {
			requeueReason:          qcache.RequeueReasonPreemptionNoCandidates,
			retryAt:                new(now.Add(10 * time.Second)),
			wantMoved:              1,
			wantClusterQueueDelays: map[kueue.ClusterQueueReference]time.Duration{"cq": 10 * time.Second},
		},
		"PreemptionNoCandidates with retryAt on cohort ClusterQueue arms root Cohort delay": {
			cohortName:       "cohort",
			requeueReason:    qcache.RequeueReasonPreemptionNoCandidates,
			retryAt:          new(now.Add(15 * time.Second)),
			wantMoved:        1,
			wantCohortDelays: map[kueue.CohortReference]time.Duration{"cohort": 15 * time.Second},
		},
		"PreemptionNoCandidates with non-future retryAt still notifies requeuer": {
			requeueReason: qcache.RequeueReasonPreemptionNoCandidates,
			retryAt:       new(now.Add(-time.Second)),
			wantMoved:     1,
		},
		"PreemptionNoCandidates without retryAt arms no delay": {
			requeueReason: qcache.RequeueReasonPreemptionNoCandidates,
		},
		"NoFit with retryAt arms ClusterQueue delay": {
			requeueReason:          qcache.RequeueReasonNoFit,
			retryAt:                new(now.Add(10 * time.Second)),
			wantMoved:              1,
			wantClusterQueueDelays: map[kueue.ClusterQueueReference]time.Duration{"cq": 10 * time.Second},
		},
		"PendingPreemption with retryAt arms no delay": {
			requeueReason: qcache.RequeueReasonPendingPreemption,
			retryAt:       new(now.Add(10 * time.Second)),
		},
		"deleted workload with retryAt arms no delay": {
			requeueReason: qcache.RequeueReasonPreemptionNoCandidates,
			retryAt:       new(now.Add(10 * time.Second)),
			beforeRequeue: func(ctx context.Context, cl client.Client, wl *kueue.Workload) error {
				return cl.Delete(ctx, wl)
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			wl := utiltestingapi.MakeWorkload("wl", "ns").Queue("lq").Request(corev1.ResourceCPU, "1").Obj()
			cq := utiltestingapi.MakeClusterQueue("cq").Cohort(tc.cohortName).Obj()
			lq := utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj()

			cl := utiltesting.NewClientBuilder().
				WithLists(&kueue.WorkloadList{Items: []kueue.Workload{*wl}}, &kueue.LocalQueueList{Items: []kueue.LocalQueue{*lq}}).
				WithObjects(utiltesting.MakeNamespace("ns"), cq).
				WithStatusSubresource(&kueue.Workload{}).
				Build()
			cqCache := schdcache.New(cl)
			if tc.cohortName != "" {
				if err := cqCache.AddOrUpdateCohort(utiltestingapi.MakeCohort(tc.cohortName).Obj()); err != nil {
					t.Fatalf("adding Cohort to cache: %v", err)
				}
			}
			if err := cqCache.AddClusterQueue(ctx, cq); err != nil {
				t.Fatalf("adding ClusterQueue to cache: %v", err)
			}
			preemptionExpectations := preemptexpectations.New()
			qManager, requeuer := qcache.NewManagerForUnitTestsWithRequeuer(cl, cqCache, qcache.WithPreemptionExpectations(preemptionExpectations))
			if tc.cohortName != "" {
				qManager.AddOrUpdateCohort(ctx, utiltestingapi.MakeCohort(tc.cohortName).Obj())
			}
			if err := qManager.AddLocalQueue(ctx, lq); err != nil {
				t.Fatalf("adding LocalQueue to manager: %v", err)
			}
			if err := qManager.AddClusterQueue(ctx, cq); err != nil {
				t.Fatalf("adding ClusterQueue to manager: %v", err)
			}
			requeuer.ProcessRequeues(ctx)

			heads := qManager.Heads(ctx)
			if len(heads) != 1 {
				t.Fatalf("Heads() returned %d workloads, want 1", len(heads))
			}

			scheduler := New(
				qManager, cqCache, cl, &utiltesting.EventRecorder{},
				WithClock(t, testingclock.NewFakeClock(now)),
				WithPreemptionExpectations(preemptionExpectations),
			)

			e := entry{
				Head:          heads[0],
				status:        nominated,
				requeueReason: tc.requeueReason,
			}
			e.recordAssignment(flavorassigner.Assignment{}, nil, tc.retryAt)
			if tc.beforeRequeue != nil {
				if err := tc.beforeRequeue(ctx, cl, wl); err != nil {
					t.Fatalf("beforeRequeue(): %v", err)
				}
			}
			scheduler.requeueAndUpdate(ctx, e)

			if diff := cmp.Diff(tc.wantClusterQueueDelays, requeuer.ClusterQueueDelays(), cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("ClusterQueueDelays() (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantCohortDelays, requeuer.CohortDelays(), cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("CohortDelays() (-want,+got):\n%s", diff)
			}
			if got := requeuer.ProcessRequeues(ctx); got != tc.wantMoved {
				t.Errorf("ProcessRequeues() = %d, want %d", got, tc.wantMoved)
			}
		})
	}
}

// TODO(#17600): Replace this test with a regular scheduler.schedule test once
// time filters are wired up in PreemptionConfig.
func TestTASRecomputePreservesRetryAtFromSkippedFlavor(t *testing.T) {
	features.SetFeatureGatesDuringTest(t, map[featuregate.Feature]bool{
		features.TopologyAwareScheduling:                     true,
		features.TASRecomputeAssignmentWithinSchedulingCycle: true,
	})

	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	retryDelay := 60 * time.Second
	retryAt := now.Add(retryDelay)

	ctx, log := utiltesting.ContextWithLog(t)
	topology := utiltestingapi.MakeDefaultOneLevelTopology("hostname")
	makeFlavor := func(name, labelVal string) *kueue.ResourceFlavor {
		return utiltestingapi.MakeResourceFlavor(name).NodeLabel("tas-flavor", labelVal).TopologyName(topology.Name).Obj()
	}
	makeNode := func(name, labelVal string) *corev1.Node {
		return testingnode.MakeNode(name).
			Label("tas-flavor", labelVal).
			Label(corev1.LabelHostname, name).
			StatusAllocatable(corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1"), corev1.ResourcePods: resource.MustParse("10")}).
			Ready().Obj()
	}
	makeAdmitted := func(name, flavor, node string) *kueue.Workload {
		return utiltestingapi.MakeWorkload(name, "ns").Queue("lq").Priority(10).
			PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).RequiredTopologyRequest(corev1.LabelHostname).Request(corev1.ResourceCPU, "1").Obj()).
			ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
				PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
					Assignment(corev1.ResourceCPU, kueue.ResourceFlavorReference(flavor), "1").
					TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
						Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{node}, 1).Obj()).Obj()).
					Obj()).
				Obj(), now).
			AdmittedAt(true, now).Obj()
	}

	rfA, rfB := makeFlavor("flavor-a", "a"), makeFlavor("flavor-b", "b")
	cq := utiltestingapi.MakeClusterQueue("cq").
		QueueingStrategy(kueue.BestEffortFIFO).
		FlavorFungibility(kueue.FlavorFungibility{WhenCanPreempt: kueue.MayStopSearch}).
		Preemption(kueue.ClusterQueuePreemption{WithinClusterQueue: kueue.PreemptionPolicyLowerPriority}).
		ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas("flavor-a").Resource(corev1.ResourceCPU, "1").Obj(),
			*utiltestingapi.MakeFlavorQuotas("flavor-b").Resource(corev1.ResourceCPU, "2").Obj(),
		).Obj()
	lq := utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue(cq.Name).Obj()
	blockerA, blockerB := makeAdmitted("blocker-a", "flavor-a", "node-a"), makeAdmitted("blocker-b", "flavor-b", "node-b")
	incoming := utiltestingapi.MakeWorkload("incoming", "ns").Queue("lq").Priority(5).
		PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).RequiredTopologyRequest(corev1.LabelHostname).Request(corev1.ResourceCPU, "1").Obj()).Obj()

	cl := utiltesting.NewClientBuilder().
		WithLists(&kueue.WorkloadList{Items: []kueue.Workload{*blockerA, *blockerB, *incoming}}, &kueue.LocalQueueList{Items: []kueue.LocalQueue{*lq}}).
		WithObjects(utiltesting.MakeNamespace("ns"), topology, rfA, rfB, cq).
		WithStatusSubresource(&kueue.Workload{}).
		Build()

	cqCache := schdcache.New(cl)
	cqCache.AddOrUpdateTopology(log, topology)
	cqCache.AddOrUpdateResourceFlavor(log, rfA)
	cqCache.AddOrUpdateResourceFlavor(log, rfB)
	cqCache.TASCache().SyncNode(makeNode("node-a", "a"))
	cqCache.TASCache().SyncNode(makeNode("node-b", "b"))
	if err := cqCache.AddClusterQueue(ctx, cq); err != nil {
		t.Fatalf("adding ClusterQueue to cache: %v", err)
	}
	cqCache.AddOrUpdateWorkload(ctx, log, blockerA)
	cqCache.AddOrUpdateWorkload(ctx, log, blockerB)

	preemptionExpectations := preemptexpectations.New()
	qManager, requeuer := qcache.NewManagerForUnitTestsWithRequeuer(cl, cqCache, qcache.WithPreemptionExpectations(preemptionExpectations))
	if err := qManager.AddLocalQueue(ctx, lq); err != nil {
		t.Fatalf("adding LocalQueue to manager: %v", err)
	}
	if err := qManager.AddClusterQueue(ctx, cq); err != nil {
		t.Fatalf("adding ClusterQueue to manager: %v", err)
	}
	requeuer.ProcessRequeues(ctx)

	heads := qManager.Heads(ctx)
	if len(heads) != 1 {
		t.Fatalf("Heads() returned %d workloads, want 1", len(heads))
	}
	snap, err := cqCache.Snapshot(ctx)
	if err != nil {
		t.Fatalf("taking snapshot: %v", err)
	}
	scheduler := New(qManager, cqCache, cl, &utiltesting.EventRecorder{},
		WithClock(t, testingclock.NewFakeClock(now)),
		WithPreemptionExpectations(preemptionExpectations),
	)

	e := entry{Head: heads[0], clusterQueueSnapshot: snap.ClusterQueue(heads[0].ClusterQueue)}
	asgn, targets, _, err := scheduler.getAssignments(ctx, &e.Info, snap)
	if err != nil {
		t.Fatalf("getAssignments() unexpected error: %v", err)
	}
	if got := asgn.PodSets[0].Flavors[corev1.ResourceCPU].Name; got != "flavor-b" {
		t.Fatalf("nominated flavor = %q, want %q", got, "flavor-b")
	}

	// Simulate nomination having learned retryAt = now+60s while probing flavor-a
	// before switching to flavor-b.
	e.recordAssignment(asgn, targets, &retryAt)
	scheduler.processEntry(ctx, &e, snap, make(preemption.PreemptedWorkloads), map[kueue.ClusterQueueReference]int{})
	scheduler.finishEntry(ctx, log, &e)

	if e.requeueReason != qcache.RequeueReasonPreemptionNoCandidates {
		t.Fatalf("requeueReason = %v, want %v", e.requeueReason, qcache.RequeueReasonPreemptionNoCandidates)
	}
	wantDelays := map[kueue.ClusterQueueReference]time.Duration{"cq": retryDelay}
	if diff := cmp.Diff(wantDelays, requeuer.ClusterQueueDelays(), cmpopts.EquateEmpty()); diff != "" {
		t.Errorf("ClusterQueueDelays() (-want,+got):\n%s", diff)
	}
}
