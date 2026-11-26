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
	"sigs.k8s.io/kueue/pkg/scheduler/preemption"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption/limits"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption/policy"
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

// TODO(#17600): Drive nomination through scheduler.schedule once time filters
// are wired up in PreemptionConfig.
func TestRequeueAfterRetryAtTiming(t *testing.T) {
	features.SetFeatureGatesDuringTest(t, map[featuregate.Feature]bool{
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
				retryDelay := 60 * time.Second
				retryAt := now.Add(retryDelay)

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
				retryAt = time.Now().Add(retryDelay)

				heads := qManager.Heads(ctx)
				if len(heads) != 1 {
					t.Fatalf("Heads() returned %d workloads, want 1", len(heads))
				}
				snap, err := cqCache.Snapshot(ctx)
				if err != nil {
					t.Fatalf("taking snapshot: %v", err)
				}
				scheduler := New(qManager, cqCache, cl, &utiltesting.EventRecorder{},
					WithPreemptionExpectations(preemptionExpectations),
				)

				e := entry{Head: heads[0], clusterQueueSnapshot: snap.ClusterQueue(heads[0].ClusterQueue)}
				if tc.assignmentError {
					e.quotaReservedReason = kueue.WorkloadQuotaReservedReasonAssignmentError
					e.retryAt = &retryAt
					scheduler.requeueAndUpdate(ctx, e)
				} else {
					result, err := scheduler.getAssignments(ctx, log, &e.Info, snap)
					if err != nil {
						t.Fatalf("getAssignments() unexpected error: %v", err)
					}
					result.retryAt = &retryAt
					e.recordAssignment(result)
					scheduler.processEntry(ctx, &e, snap, make(preemption.PreemptedWorkloads), map[kueue.ClusterQueueReference]int{})
					scheduler.finishEntry(ctx, log, &e)
				}

				if got := qManager.Dump(); len(got) != 0 {
					t.Fatalf("expected active queue empty immediately after requeue, got %v", got)
				}
				if got := qManager.DumpInadmissible(); len(got["cq"]) != 1 {
					t.Fatalf("expected workload in inadmissible queue, got %v", got)
				}

				// Must not be rescheduled before retryAt passes.
				synctest.Sleep(retryDelay - time.Nanosecond)
				if got := qManager.Dump(); len(got) != 0 {
					t.Fatalf("workload rescheduled before retryAt elapsed: %v", got)
				}

				// Must move back to active queue as soon as retryAt passes.
				synctest.Sleep(time.Nanosecond)
				wantActive := map[kueue.ClusterQueueReference][]workload.Reference{"cq": {workload.Key(incoming)}}
				if diff := cmp.Diff(wantActive, qManager.Dump()); diff != "" {
					t.Errorf("active queue after retryAt (-want,+got):\n%s", diff)
				}
			})
		})
	}
}

func TestGetAssignmentsPreemptionLimits(t *testing.T) {
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	rfOnDemand := utiltestingapi.MakeResourceFlavor("on-demand").Obj()
	rfSpot := utiltestingapi.MakeResourceFlavor("spot").Obj()
	preemptionCfg := utiltestingalpha.MakePreemptionConfig("limits-cfg").
		Rule("within-cq", kueuealpha.InsufficientQuota,
			utiltestingalpha.MakeCandidateSelector(kueuealpha.WithinClusterQueue).Obj(),
		).
		Obj()
	preemptionLimit := utiltestingalpha.MakePreemptionLimit("per-workload", kueuealpha.ByPreempteeWorkloadLimitAggregation).
		UID("per-workload-uid").
		Limit(1).WindowSeconds(60).Obj()
	victim := utiltestingapi.MakeWorkload("v-od", "default").
		Queue("limits-lq").
		Request(corev1.ResourceCPU, "1").
		SimpleReserveQuota("limits-cq", "on-demand", now).
		AdmittedAt(true, now).
		Obj()
	incoming := utiltestingapi.MakeWorkload("incoming", "default").
		Queue("limits-lq").
		Request(corev1.ResourceCPU, "1").
		Obj()

	cases := map[string]struct {
		spotQuota    string
		wantRetryAt  *time.Time
		wantBlocking *policy.BlockingPreemptionLimit
	}{
		"returns retryAt and blockingLimit when all flavors are blocked by expiring limit": {
			spotQuota:   "0",
			wantRetryAt: new(now.Add(10 * time.Second)),
			wantBlocking: &policy.BlockingPreemptionLimit{
				Limit:       "per-workload",
				Aggregation: kueuealpha.ByPreempteeWorkloadLimitAggregation,
				RetryAt:     new(now.Add(10 * time.Second)),
			},
		},
		"returns nil blockingLimit, preserves retryAt, and leaves preemptor drained when fallback flavor fits": {
			spotQuota:   "1",
			wantRetryAt: new(now.Add(10 * time.Second)),
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, map[featuregate.Feature]bool{
				features.ConfigurablePreemptions:      true,
				features.ConfigurablePreemptionLimits: true,
			})
			ctx, log := utiltesting.ContextWithLog(t)
			cq := utiltestingapi.MakeClusterQueue("limits-cq").
				Annotation(kueuealpha.PreemptionConfigNameAnnotation, "limits-cfg").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "1").Obj(),
					*utiltestingapi.MakeFlavorQuotas("spot").Resource(corev1.ResourceCPU, tc.spotQuota).Obj(),
				).
				Obj()
			lq := utiltestingapi.MakeLocalQueue("limits-lq", "default").ClusterQueue("limits-cq").Obj()
			cl := utiltesting.NewClientBuilder().WithObjects(
				utiltesting.MakeNamespace("default"),
				rfOnDemand, rfSpot, cq, lq, preemptionCfg, preemptionLimit, victim, incoming,
			).Build()

			cqCache := schdcache.New(cl)
			cqCache.AddOrUpdateResourceFlavor(log, rfOnDemand)
			cqCache.AddOrUpdateResourceFlavor(log, rfSpot)
			if err := cqCache.AddClusterQueue(ctx, cq); err != nil {
				t.Fatalf("adding ClusterQueue to cache: %v", err)
			}

			limitTracker := limits.NewPreemptionLimitTracker()
			limitTracker.Sync(preemptionLimit)
			limitTracker.Record(now.Add(-50*time.Second), policy.ConsumedLimits{
				"per-workload": policy.NewConsumedLimit(preemptionLimit, "default/v-od"),
			})

			qManager := qcache.NewManagerForUnitTests(cl, cqCache)
			sched := New(
				qManager, cqCache, cl, &utiltesting.EventRecorder{},
				WithClock(t, testingclock.NewFakeClock(now)),
				WithPreemptionExpectations(preemptexpectations.New()),
				WithPreemptionLimitTracker(limitTracker),
			)
			sched.preemptor.SnapshotPreemptionLimits()
			snap, err := cqCache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("Snapshot: %v", err)
			}

			wlInfo := workload.NewInfo(log, incoming)
			wlInfo.ClusterQueue = "limits-cq"
			result, err := sched.getAssignments(ctx, log, wlInfo, snap)
			if err != nil {
				t.Fatalf("getAssignments() unexpected error: %v", err)
			}
			if diff := cmp.Diff(tc.wantRetryAt, result.retryAt); diff != "" {
				t.Errorf("getAssignments retryAt (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantBlocking, result.blockingLimit); diff != "" {
				t.Errorf("getAssignments blockingLimit (-want,+got):\n%s", diff)
			}
			if leaked := sched.preemptor.TakeRetryAt(); leaked != nil {
				t.Errorf("preemptor.TakeRetryAt() = %v after getAssignments, want nil", leaked)
			}
			if leakedBlocking := sched.preemptor.TakeBlockingLimit(); leakedBlocking != nil {
				t.Errorf("preemptor.TakeBlockingLimit() = %v after getAssignments, want nil", leakedBlocking)
			}
		})
	}
}

func TestSchedulePreemptionLimits(t *testing.T) {
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	fakeClock := testingclock.NewFakeClock(now)
	ignoreEventMessageCmpOpts := cmp.Options{cmpopts.IgnoreFields(utiltesting.EventRecord{}, "Message")}

	resourceFlavors := []*kueue.ResourceFlavor{
		utiltestingapi.MakeResourceFlavor("default").Obj(),
		utiltestingapi.MakeResourceFlavor("on-demand").Obj(),
		utiltestingapi.MakeResourceFlavor("spot").Obj(),
	}
	preemptionCfg := utiltestingalpha.MakePreemptionConfig("limits-cfg").
		Rule("within-cq", kueuealpha.InsufficientQuota,
			utiltestingalpha.MakeCandidateSelector(kueuealpha.WithinClusterQueue).Obj(),
		).
		Obj()
	perWorkloadLimit := utiltestingalpha.MakePreemptionLimit("per-workload", kueuealpha.ByPreempteeWorkloadLimitAggregation).
		UID("per-workload-uid").
		Limit(1).WindowSeconds(60).Obj()
	vODConsumed := policy.ConsumedLimits{"per-workload": policy.NewConsumedLimit(perWorkloadLimit, "default/v-od")}
	vSpotConsumed := policy.ConsumedLimits{"per-workload": policy.NewConsumedLimit(perWorkloadLimit, "default/v-spot")}

	cases := map[string]scheduleTestCase{
		"configurable preemption within a PreemptionLimit is issued while blocked ClusterQueue reports BlockingPreemptionLimit": {
			featureGates: map[featuregate.Feature]bool{
				features.ConfigurablePreemptions:      true,
				features.ConfigurablePreemptionLimits: true,
			},
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("limited-cq").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "1").Obj()).
					Annotation(kueuealpha.PreemptionConfigNameAnnotation, "limited-config").
					Obj(),
				*utiltestingapi.MakeClusterQueue("blocked-cq").
					Label("preemption", "blocked").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "1").Obj()).
					Annotation(kueuealpha.PreemptionConfigNameAnnotation, "limited-config").
					Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltestingapi.MakeLocalQueue("limited-lq", "sales").ClusterQueue("limited-cq").Obj(),
				*utiltestingapi.MakeLocalQueue("blocked-lq", "sales").ClusterQueue("blocked-cq").Obj(),
			},
			objects: []client.Object{
				utiltestingalpha.MakePreemptionConfig("limited-config").
					Rule("within-cq", kueuealpha.Always, kueuealpha.PreemptionConfigPreemptionCandidateSelector{Scope: kueuealpha.WithinClusterQueue}).
					Obj(),
				utiltestingalpha.MakePreemptionLimit("global", kueuealpha.GlobalLimitAggregation).UID("global-uid").Limit(1).WindowSeconds(60).Obj(),
				utiltestingalpha.MakePreemptionLimit("blocked", kueuealpha.GlobalLimitAggregation).
					UID("blocked-uid").
					PreemptorClusterQueueSelector(&metav1.LabelSelector{MatchLabels: map[string]string{"preemption": "blocked"}}).
					Limit(0).
					WindowSeconds(60).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("new", "sales").
					Queue("limited-lq").
					Request(corev1.ResourceCPU, "1").
					Obj(),
				*utiltestingapi.MakeWorkload("victim", "sales").
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("limited-cq").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "1").
							Obj()).
						Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("blocked", "sales").
					Queue("blocked-lq").
					Request(corev1.ResourceCPU, "1").
					Obj(),
				*utiltestingapi.MakeWorkload("blocked-victim", "sales").
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("blocked-cq").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "1").
							Obj()).
						Obj(), now).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("blocked", "sales").
					Queue("blocked-lq").
					Request(corev1.ResourceCPU, "1").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonWaitingForQuota,
						Message:            "couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor default, 1 more needed; preemptions blocked by PreemptionLimit blocked (aggregation Global)",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadAdmittedReasonNoReservation,
						Message:            "The workload has no reservation",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "main",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("1"),
						},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("blocked-victim", "sales").
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("blocked-cq").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "1").
							Obj()).
						Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("new", "sales").
					Queue("limited-lq").
					Request(corev1.ResourceCPU, "1").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads,
						Message:            "couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor default, 1 more needed. Pending the preemption of 1 workload(s)",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadAdmittedReasonNoReservation,
						Message:            "The workload has no reservation",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "main",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("1"),
						},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("victim", "sales").
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("limited-cq").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "1").
							Obj()).
						Obj(), now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             kueue.WorkloadEvictedByPreemption,
						Message:            "Preempted by sales/new because of preemption config limited-config rule within-cq/0",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             kueue.ConfigurablePreemptionReason,
						Message:            "Preempted by sales/new because of preemption config limited-config rule within-cq/0",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{
						Reason:          kueue.WorkloadEvictedByPreemption,
						UnderlyingCause: "limited-config",
						Count:           1,
					}).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				// Preemptor is not admitted in this cycle; it waits for the victim's eviction to complete.
				"limited-cq": {"sales/new"},
			},
			wantInadmissibleLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"blocked-cq": {"sales/blocked"},
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				// Removal from cache for the preempted workload is deferred until we receive Workload updates.
				"sales/victim": *utiltestingapi.MakeAdmission("limited-cq").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "default", "1").
						Obj()).
					Obj(),
				"sales/blocked-victim": *utiltestingapi.MakeAdmission("blocked-cq").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "default", "1").
						Obj()).
					Obj(),
			},
		},
		"standalone ClusterQueue requeues workload after earliest PreemptionLimit expiry across flavors": {
			featureGates: map[featuregate.Feature]bool{
				features.ConfigurablePreemptions:      true,
				features.ConfigurablePreemptionLimits: true,
			},
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("limits-cq").
					Annotation(kueuealpha.PreemptionConfigNameAnnotation, "limits-cfg").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "1").Obj(),
						*utiltestingapi.MakeFlavorQuotas("spot").Resource(corev1.ResourceCPU, "1").Obj(),
					).
					Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltestingapi.MakeLocalQueue("limits-lq", "default").ClusterQueue("limits-cq").Obj(),
			},
			objects: []client.Object{
				preemptionCfg,
				perWorkloadLimit.DeepCopy(),
			},
			recordedPreemptions: []recordedPreemption{
				{at: now.Add(-35 * time.Second), consumed: vODConsumed},
				{at: now.Add(-50 * time.Second), consumed: vSpotConsumed},
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("v-od", "default").
					Queue("limits-lq").
					Request(corev1.ResourceCPU, "1").
					SimpleReserveQuota("limits-cq", "on-demand", now).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("v-spot", "default").
					Queue("limits-lq").
					Request(corev1.ResourceCPU, "1").
					SimpleReserveQuota("limits-cq", "spot", now).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("incoming", "default").
					Queue("limits-lq").
					Request(corev1.ResourceCPU, "1").
					Creation(now).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("incoming", "default").
					Queue("limits-lq").
					Request(corev1.ResourceCPU, "1").
					Creation(now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonWaitingForQuota,
						Message:            "couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor on-demand, 1 more needed, insufficient unused quota for cpu in flavor spot, 1 more needed; preemptions blocked by PreemptionLimit per-workload (aggregation ByPreempteeWorkload) until 2026-01-01T00:00:10Z",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadAdmittedReasonNoReservation,
						Message:            "The workload has no reservation",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name:      kueue.DefaultPodSetName,
						Resources: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("v-od", "default").
					Queue("limits-lq").
					Request(corev1.ResourceCPU, "1").
					SimpleReserveQuota("limits-cq", "on-demand", now).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("v-spot", "default").
					Queue("limits-lq").
					Request(corev1.ResourceCPU, "1").
					SimpleReserveQuota("limits-cq", "spot", now).
					AdmittedAt(true, now).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"default/v-od": *utiltestingapi.MakeAdmission("limits-cq").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "on-demand", "1").Obj()).
					Obj(),
				"default/v-spot": *utiltestingapi.MakeAdmission("limits-cq").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "spot", "1").Obj()).
					Obj(),
			},
			wantInadmissibleLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"limits-cq": {"default/incoming"},
			},
			wantClusterQueueRequeueDelays: map[kueue.ClusterQueueReference]time.Duration{
				"limits-cq": 10 * time.Second,
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       client.ObjectKey{Namespace: "default", Name: "incoming"},
					Reason:    kueue.WorkloadQuotaReservedReasonWaitingForQuota,
					EventType: corev1.EventTypeWarning,
					Message:   "couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor on-demand, 1 more needed, insufficient unused quota for cpu in flavor spot, 1 more needed; preemptions blocked by PreemptionLimit per-workload (aggregation ByPreempteeWorkload) until 2026-01-01T00:00:10Z",
				},
			},
		},
		"reduced count of partially admissible workload arms PreemptionLimit retry delay": {
			featureGates: map[featuregate.Feature]bool{
				features.PartialAdmission:             true,
				features.ConfigurablePreemptions:      true,
				features.ConfigurablePreemptionLimits: true,
			},
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("limits-cq").
					Annotation(kueuealpha.PreemptionConfigNameAnnotation, "limits-cfg").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "2").Obj(),
					).
					Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltestingapi.MakeLocalQueue("limits-lq", "default").ClusterQueue("limits-cq").Obj(),
			},
			objects: []client.Object{
				preemptionCfg,
				perWorkloadLimit.DeepCopy(),
			},
			recordedPreemptions: []recordedPreemption{
				{at: now.Add(-50 * time.Second), consumed: vODConsumed},
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("v-od", "default").
					Queue("limits-lq").
					Request(corev1.ResourceCPU, "2").
					SimpleReserveQuota("limits-cq", "on-demand", now).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("incoming", "default").
					Queue("limits-lq").
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 4).
							SetMinimumCount(1).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Creation(now).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("incoming", "default").
					Queue("limits-lq").
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 4).
							SetMinimumCount(1).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Creation(now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonExceedsMaxQuota,
						Message:            "couldn't assign flavors to pod set main: insufficient quota for cpu in flavor on-demand, previously considered podsets requests (0) + current podset request (4) > maximum capacity (2); preemptions blocked by PreemptionLimit per-workload (aggregation ByPreempteeWorkload) until 2026-01-01T00:00:10Z",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadAdmittedReasonNoReservation,
						Message:            "The workload has no reservation",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name:      kueue.DefaultPodSetName,
						Resources: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4")},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("v-od", "default").
					Queue("limits-lq").
					Request(corev1.ResourceCPU, "2").
					SimpleReserveQuota("limits-cq", "on-demand", now).
					AdmittedAt(true, now).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"default/v-od": *utiltestingapi.MakeAdmission("limits-cq").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "on-demand", "2").Obj()).
					Obj(),
			},
			wantInadmissibleLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"limits-cq": {"default/incoming"},
			},
			wantClusterQueueRequeueDelays: map[kueue.ClusterQueueReference]time.Duration{
				"limits-cq": 10 * time.Second,
			},
			eventCmpOpts: ignoreEventMessageCmpOpts,
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "incoming", kueue.WorkloadQuotaReservedReasonExceedsMaxQuota, corev1.EventTypeWarning).Obj(),
			},
		},
	}

	runScheduleTestCases(t, scheduleTestConfig{
		resourceFlavors: resourceFlavors,
		fakeClock:       fakeClock,
	}, cases)
}
