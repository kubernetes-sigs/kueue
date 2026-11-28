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
	"testing/synctest"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/component-base/featuregate"
	testingclock "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/features"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingalpha "sigs.k8s.io/kueue/pkg/util/testing/v1alpha1"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestRequeueAndUpdateRetryAt(t *testing.T) {
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)

	cases := map[string]struct {
		cohortName             kueue.CohortReference
		status                 entryStatus
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
		"Generic (AssignmentError, e.g. WAS deep integration) with retryAt arms ClusterQueue delay": {
			status:                 notNominated,
			requeueReason:          qcache.RequeueReasonGeneric,
			retryAt:                new(now.Add(10 * time.Second)),
			wantMoved:              1,
			wantClusterQueueDelays: map[kueue.ClusterQueueReference]time.Duration{"cq": 10 * time.Second},
		},
		"Generic after nomination with retryAt arms no delay": {
			status:        nominated,
			requeueReason: qcache.RequeueReasonGeneric,
			retryAt:       new(now.Add(10 * time.Second)),
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

			status := tc.status
			if status == "" && tc.requeueReason != qcache.RequeueReasonGeneric {
				status = nominated
			}
			e := entry{
				Head:          heads[0],
				status:        status,
				requeueReason: tc.requeueReason,
			}
			e.recordAssignment(assignmentResult{retryAt: tc.retryAt})
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

func TestRequeueAfterRetryAtTiming(t *testing.T) {
	features.SetFeatureGatesDuringTest(t, map[featuregate.Feature]bool{
		features.ConfigurablePreemptions:                     true,
		features.TopologyAwareScheduling:                     true,
		features.TASRecomputeAssignmentWithinSchedulingCycle: true,
	})

	cases := map[string]struct {
		assignmentError bool
	}{
		"TAS recompute across flavor switch requeues only after retryAt":                       {},
		"AssignmentError (WAS deep integration) in BestEffortFIFO requeues only after retryAt": {assignmentError: true},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				now := time.Now()
				const minExecutionSeconds = 60
				retryAt := now.Add(minExecutionSeconds * time.Second)

				ctx, log := utiltesting.ContextWithLog(t)
				ctx, cancel := context.WithCancel(ctx)
				defer cancel()

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
					return utiltestingapi.MakeWorkload(name, "ns").Queue("lq").Label("flavor", flavor).Priority(1).
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
				// Only the blocker on flavor A can become eligible for preemption.
				preemptionConfig := utiltestingalpha.MakePreemptionConfig("min-execution").
					Rule("within-queue", kueuealpha.InsufficientQuota,
						utiltestingalpha.MakeCandidateSelector(kueuealpha.WithinClusterQueue).
							LabelSelector(&metav1.LabelSelector{MatchLabels: map[string]string{"flavor": "flavor-a"}}).
							Execution(utiltestingalpha.MakeDurationConstraint().MinSeconds(minExecutionSeconds).Obj()).
							Obj()).
					Obj()
				cq := utiltestingapi.MakeClusterQueue("cq").
					Annotation(kueuealpha.PreemptionConfigNameAnnotation, preemptionConfig.Name).
					QueueingStrategy(kueue.BestEffortFIFO).
					FlavorFungibility(kueue.FlavorFungibility{WhenCanPreempt: kueue.MayStopSearch}).
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
					WithObjects(utiltesting.MakeNamespace("ns"), topology, rfA, rfB, cq, preemptionConfig).
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
				requeuer := qcache.NewRequeuer()
				qManager := qcache.NewManager(cl, cqCache, requeuer, qcache.WithPreemptionExpectations(preemptionExpectations))
				if err := qManager.AddLocalQueue(ctx, lq); err != nil {
					t.Fatalf("adding LocalQueue to manager: %v", err)
				}
				if err := qManager.AddClusterQueue(ctx, cq); err != nil {
					t.Fatalf("adding ClusterQueue to manager: %v", err)
				}
				done := make(chan error, 1)
				go func() { done <- requeuer.Start(ctx) }()
				defer func() {
					cancel()
					if err := <-done; err != nil {
						t.Errorf("requeuer shutdown: %v", err)
					}
				}()
				// Flush the initial AddClusterQueue requeue notification before starting the cycle.
				synctest.Sleep(time.Second)
				scheduler := New(qManager, cqCache, cl, &utiltesting.EventRecorder{},
					WithPreemptionExpectations(preemptionExpectations),
				)
				if tc.assignmentError {
					heads := qManager.Heads(ctx)
					if len(heads) != 1 {
						t.Fatalf("Heads() returned %d workloads, want 1", len(heads))
					}
					scheduler.requeueAndUpdate(ctx, entry{
						Head:                heads[0],
						quotaReservedReason: kueue.WorkloadQuotaReservedReasonAssignmentError,
						retryAt:             &retryAt,
					})
				} else {
					// Flavor A lacks quota and its blocker is too young to preempt.
					// Flavor B has spare quota but no space on its only node.
					scheduler.schedule(ctx)
				}

				if got := qManager.Dump(); len(got) != 0 {
					t.Fatalf("expected active queue empty immediately after requeue, got %v", got)
				}
				wantQueued := map[kueue.ClusterQueueReference][]workload.Reference{"cq": {workload.Key(incoming)}}
				if diff := cmp.Diff(wantQueued, qManager.DumpInadmissible()); diff != "" {
					t.Fatalf("inadmissible queue after scheduling (-want,+got):\n%s", diff)
				}

				// Must not be rescheduled before retryAt passes.
				synctest.Sleep(time.Until(retryAt) - time.Nanosecond)
				if got := qManager.Dump(); len(got) != 0 {
					t.Fatalf("workload rescheduled before retryAt elapsed: %v", got)
				}

				// Must move back to active queue as soon as retryAt passes.
				synctest.Sleep(time.Nanosecond)
				if diff := cmp.Diff(wantQueued, qManager.Dump()); diff != "" {
					t.Errorf("active queue after retryAt (-want,+got):\n%s", diff)
				}
			})
		})
	}
}
