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
	"maps"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/component-base/featuregate"
	"k8s.io/klog/v2"
	kubefeatures "k8s.io/kubernetes/pkg/features"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/cache/scheduler/was"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/scheduler/flavorassigner"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
)

// TestCommittedVictimsOfEarlierCycles checks which Workloads of the snapshot are
// committed victims of earlier scheduling cycles.
func TestCommittedVictimsOfEarlierCycles(t *testing.T) {
	ctx, log := utiltesting.ContextWithLog(t)
	now := time.Now().Truncate(time.Second)
	admitted := func(name string) *utiltestingapi.WorkloadWrapper {
		return utiltestingapi.MakeWorkload(name, "ns").UID(types.UID(name+"-uid")).
			Request(corev1.ResourceCPU, "1").
			ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
				Assignment(corev1.ResourceCPU, "default", "1").Obj()).Obj(), now)
	}
	evicted := func(name, reason string) *kueue.Workload {
		return admitted(name).Condition(metav1.Condition{
			Type:               kueue.WorkloadEvicted,
			Status:             metav1.ConditionTrue,
			Reason:             reason,
			LastTransitionTime: metav1.NewTime(now),
		}).Obj()
	}
	cache := schdcache.New(utiltesting.NewFakeClient())
	cache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("default").Obj())
	if err := cache.AddClusterQueue(ctx, utiltestingapi.MakeClusterQueue("cq").
		ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "10").Obj()).
		Obj()); err != nil {
		t.Fatalf("adding ClusterQueue: %v", err)
	}
	for _, w := range []*kueue.Workload{
		admitted("running").Obj(),
		admitted("preemption-issued").Obj(),
		admitted("recreated").Obj(),
		evicted("preempted", kueue.WorkloadEvictedByPreemption),
		evicted("pods-ready-timeout", kueue.WorkloadEvictedByPodsReadyTimeout),
	} {
		if !cache.AddOrUpdateWorkload(ctx, log, w) {
			t.Fatalf("adding workload %s", w.Name)
		}
	}
	snap, err := cache.Snapshot(ctx)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	preemptionExpectations := preemptexpectations.New()
	preemptionExpectations.ExpectUIDs(log, types.NamespacedName{Namespace: "ns", Name: "preemption-issued"}, []types.UID{"preemption-issued-uid"})
	// The preemption was issued for an earlier Workload of the same name.
	preemptionExpectations.ExpectUIDs(log, types.NamespacedName{Namespace: "ns", Name: "recreated"}, []types.UID{"recreated-old-uid"})
	s := New(nil, cache, utiltesting.NewFakeClient(), &utiltesting.EventRecorder{}, WithPreemptionExpectations(preemptionExpectations))

	var got []workload.Reference
	for _, wl := range s.committedVictimsOfEarlierCycles(snap) {
		got = append(got, workload.Key(wl.Obj))
	}
	want := []workload.Reference{"ns/pods-ready-timeout", "ns/preempted", "ns/preemption-issued"}
	if diff := cmp.Diff(want, got, cmpopts.SortSlices(func(a, b workload.Reference) bool { return a < b })); diff != "" {
		t.Errorf("Unexpected committed victims of earlier cycles (-want,+got):\n%s", diff)
	}
}

// TestGetAssignmentsWithCommittedVictims checks how getAssignments accounts for
// committed preemption victims with SchedulerLibraryDeepIntegration, using the
// scheduling simulator of scheduler-library.
func TestGetAssignmentsWithCommittedVictims(t *testing.T) {
	const committedVictim = "committed-victim"
	now := time.Now().Truncate(time.Second)
	// running makes an admitted Workload running a single Pod on the node.
	running := func(name string, priority int32, cpu, node string) *kueue.Workload {
		return utiltestingapi.MakeWorkload(name, "ns").UID(types.UID(name+"-uid")).Priority(priority).
			PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
				Request(corev1.ResourceCPU, cpu).RequiredTopologyRequest(corev1.LabelHostname).Obj()).
			ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
				Assignment(corev1.ResourceCPU, "tas", cpu).
				TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
					Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{node}, 1).Obj()).Obj()).Obj()).Obj(), now).
			Obj()
	}
	// quotaOnly makes a Workload holding quota, with no Pods yet, e.g. while
	// waiting on AdmissionChecks before its topology is assigned.
	quotaOnly := func(name string, priority int32, cpu string) *kueue.Workload {
		return utiltestingapi.MakeWorkload(name, "ns").UID(types.UID(name+"-uid")).Priority(priority).
			PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
				Request(corev1.ResourceCPU, cpu).RequiredTopologyRequest(corev1.LabelHostname).Obj()).
			ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
				Assignment(corev1.ResourceCPU, "tas", cpu).Obj()).Obj(), now).
			Obj()
	}

	cases := map[string]struct {
		cqQuota   string
		workloads []*kueue.Workload
		// incomingCPU is requested by each Pod of the incoming Workload.
		incomingCPU string
		// incomingCount is the number of Pods of the incoming Workload, 1 if unset.
		incomingCount int32
		// incomingMinCount makes the incoming Workload partially admissible.
		incomingMinCount *int32
		// fairSharing enables the Fair Sharing preemption strategies.
		fairSharing bool

		wantMode    flavorassigner.FlavorAssignmentMode
		wantNode    string
		wantCount   int32
		wantTargets []workload.Reference
	}{
		"fits once the committed victim without Pods releases its quota": {
			cqQuota: "5",
			workloads: []*kueue.Workload{
				quotaOnly(committedVictim, 0, "2"),
				running("other", 100, "3", "n1"),
			},
			incomingCPU: "2",
			wantMode:    flavorassigner.DeferredFit,
			wantNode:    "n2",
		},
		"partially admissible workload waits at its full count for the committed victim": {
			cqQuota: "10",
			workloads: []*kueue.Workload{
				running(committedVictim, 0, "4", "n1"),
			},
			incomingCPU:      "4",
			incomingCount:    2,
			incomingMinCount: new(int32(1)),
			wantMode:         flavorassigner.DeferredFit,
			wantCount:        2,
		},
		"fits right away, as neither the node nor the quota of the committed victim is needed": {
			cqQuota: "10",
			workloads: []*kueue.Workload{
				running(committedVictim, 0, "2", "n1"),
			},
			incomingCPU: "2",
			wantMode:    flavorassigner.Fit,
			wantNode:    "n2",
		},
		"fits once the committed victim releases its quota": {
			cqQuota: "5",
			workloads: []*kueue.Workload{
				running(committedVictim, 0, "1", "n1"),
				running("other", 100, "3", "n1"),
			},
			incomingCPU: "2",
			wantMode:    flavorassigner.DeferredFit,
			wantNode:    "n2",
		},
		"fits once the committed victim releases its quota, with Fair Sharing and no other candidate": {
			cqQuota: "5",
			workloads: []*kueue.Workload{
				running(committedVictim, 0, "1", "n1"),
				running("other", 100, "3", "n1"),
			},
			incomingCPU: "2",
			fairSharing: true,
			wantMode:    flavorassigner.DeferredFit,
			wantNode:    "n2",
		},
		"fits once the committed victim releases its node": {
			cqQuota: "10",
			workloads: []*kueue.Workload{
				running(committedVictim, 0, "4", "n1"),
				running("other", 100, "4", "n2"),
			},
			incomingCPU: "4",
			wantMode:    flavorassigner.DeferredFit,
			wantNode:    "n1",
		},
		"fits once the committed victim and a new victim release their node": {
			cqQuota: "10",
			workloads: []*kueue.Workload{
				running(committedVictim, 0, "2", "n1"),
				running("low", 1, "2", "n1"),
				running("other", 100, "4", "n2"),
			},
			incomingCPU: "4",
			wantMode:    flavorassigner.Preempt,
			wantNode:    "n1",
			wantTargets: []workload.Reference{"ns/low"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, map[featuregate.Feature]bool{
				kubefeatures.GenericWorkload:             true,
				features.TopologyAwareScheduling:         true,
				features.SchedulerLibraryIntegration:     true,
				features.SchedulerLibraryDeepIntegration: true,
				features.PartialAdmission:                true,
			})
			ctx, log := utiltesting.ContextWithLog(t)

			simulatorFactory, err := was.NewWASSimulatorFactory(klog.NewContext(ctx, logr.Discard()), nil)
			if err != nil {
				t.Fatalf("NewWASSimulatorFactory: %v", err)
			}
			cl := utiltesting.NewFakeClient()
			cache := schdcache.New(cl, schdcache.WithSimulatorFactory(simulatorFactory), schdcache.WithFairSharing(tc.fairSharing))
			cache.AddOrUpdateTopology(log, utiltestingapi.MakeDefaultOneLevelTopology("topology"))
			cache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("tas").
				NodeLabel("tas-node", "true").TopologyName("topology").Obj())
			for _, node := range []string{"n1", "n2"} {
				cache.TASCache().SyncNode(testingnode.MakeNode(node).
					Label("tas-node", "true").Label(corev1.LabelHostname, node).
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("4"),
						corev1.ResourcePods: resource.MustParse("32"),
					}).Ready().Obj())
			}
			if err := cache.AddClusterQueue(ctx, utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("tas").Resource(corev1.ResourceCPU, tc.cqQuota).Obj()).
				Preemption(kueue.ClusterQueuePreemption{WithinClusterQueue: kueue.PreemptionPolicyLowerPriority}).
				Obj()); err != nil {
				t.Fatalf("adding ClusterQueue: %v", err)
			}
			for _, w := range tc.workloads {
				if !cache.AddOrUpdateWorkload(ctx, log, w) {
					t.Fatalf("adding workload %s", w.Name)
				}
			}
			snap, err := cache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("snapshot: %v", err)
			}
			cq := snap.ClusterQueue("cq")
			snap.AddCommittedVictims(ctx, cq.Workloads[workload.NewReference("ns", committedVictim)])
			usageBefore := maps.Clone(cq.ResourceNode.Usage)

			incomingPodSet := utiltestingapi.MakePodSet(kueue.DefaultPodSetName, int(max(tc.incomingCount, 1))).
				Request(corev1.ResourceCPU, tc.incomingCPU)
			if tc.incomingMinCount != nil {
				incomingPodSet.SetMinimumCount(*tc.incomingMinCount).UnconstrainedTopologyRequest()
			} else {
				incomingPodSet.RequiredTopologyRequest(corev1.LabelHostname)
			}
			incoming := workload.NewInfo(log, utiltestingapi.MakeWorkload("incoming", "ns").UID("incoming-uid").Priority(10).
				PodSets(*incomingPodSet.Obj()).Obj())
			incoming.ClusterQueue = "cq"
			var opts []Option
			if tc.fairSharing {
				opts = append(opts, WithFairSharing(&config.FairSharing{}))
			}
			s := New(nil, cache, cl, &utiltesting.EventRecorder{}, opts...)
			plan, _ := s.getAssignments(ctx, incoming, snap)
			if plan.Error != nil {
				t.Fatalf("getAssignments() error = %v", plan.Error)
			}
			asgn, targets := plan.Assignment, plan.PreemptionTargets

			if got := asgn.RepresentativeMode(); got != tc.wantMode {
				t.Errorf("getAssignments() mode = %v, want %v", got, tc.wantMode)
			}
			var gotNode string
			if ta := asgn.PodSets[0].TopologyAssignment; ta != nil && len(ta.Domains) == 1 {
				gotNode = ta.Domains[0].Values[0]
			}
			if gotNode != tc.wantNode {
				t.Errorf("getAssignments() placed the Pod on node %q, want %q", gotNode, tc.wantNode)
			}
			if tc.wantCount != 0 && asgn.PodSets[0].Count != tc.wantCount {
				t.Errorf("getAssignments() count = %d, want %d", asgn.PodSets[0].Count, tc.wantCount)
			}
			var gotTargets []workload.Reference
			for _, target := range targets {
				gotTargets = append(gotTargets, workload.Key(target.WorkloadInfo.Obj))
			}
			if diff := cmp.Diff(tc.wantTargets, gotTargets); diff != "" {
				t.Errorf("getAssignments() preemption targets mismatch (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(usageBefore, cq.ResourceNode.Usage); diff != "" {
				t.Errorf("getAssignments() changed the quota usage (-before,+after):\n%s", diff)
			}
			if !snap.HasWorkload(snap.CommittedVictims().Workloads[workload.NewReference("ns", committedVictim)]) {
				t.Error("getAssignments() did not put the committed victim back into the snapshot")
			}
		})
	}
}

// TestOverlapRecomputeWithCommittedVictims checks that, with
// SchedulerLibraryDeepIntegration, the overlap recompute counts on the quota
// of all the victims of the earlier preemptions in the cycle, as without it,
// including the victims which the Workload could not preempt itself. Here,
// the Workload borrows, so it may only preempt in its own ClusterQueue.
func TestOverlapRecomputeWithCommittedVictims(t *testing.T) {
	cases := map[string]struct {
		// freeNode adds a node which B fits on right away.
		freeNode bool
	}{
		"fits on the node of the victims once they are gone": {},
		// B is placed right away, but only on the quota of the victims.
		"fits on a free node, but on the quota of the victims": {freeNode: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			testOverlapRecomputeWithCommittedVictims(t, tc.freeNode)
		})
	}
}

func testOverlapRecomputeWithCommittedVictims(t *testing.T, freeNode bool) {
	features.SetFeatureGatesDuringTest(t, map[featuregate.Feature]bool{
		kubefeatures.GenericWorkload:                             true,
		features.TopologyAwareScheduling:                         true,
		features.SchedulerLibraryIntegration:                     true,
		features.SchedulerLibraryDeepIntegration:                 true,
		features.RecomputeAssignmentUponPreemptionTargetsOverlap: true,
	})
	ctx, log := utiltesting.ContextWithLog(t)
	now := time.Now().Truncate(time.Second)
	// admitted makes a Workload holding quota of its ClusterQueue for a
	// single Pod on n1.
	admitted := func(name string, cq kueue.ClusterQueueReference, priority int32, cpu string) *kueue.Workload {
		return utiltestingapi.MakeWorkload(name, "ns").UID(types.UID(name+"-uid")).Priority(priority).
			PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
				Request(corev1.ResourceCPU, cpu).RequiredTopologyRequest(corev1.LabelHostname).Obj()).
			ReserveQuotaAt(utiltestingapi.MakeAdmission(cq).PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
				Assignment(corev1.ResourceCPU, "tas", cpu).
				TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
					Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"n1"}, 1).Obj()).Obj()).Obj()).Obj(), now).
			Obj()
	}

	simulatorFactory, err := was.NewWASSimulatorFactory(klog.NewContext(ctx, logr.Discard()), nil)
	if err != nil {
		t.Fatalf("NewWASSimulatorFactory: %v", err)
	}
	cl := utiltesting.NewFakeClient()
	cache := schdcache.New(cl, schdcache.WithSimulatorFactory(simulatorFactory))
	cache.AddOrUpdateTopology(log, utiltestingapi.MakeDefaultOneLevelTopology("topology"))
	cache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("tas").
		NodeLabel("tas-node", "true").TopologyName("topology").Obj())
	nodes := []string{"n1"}
	if freeNode {
		nodes = append(nodes, "n2")
	}
	for _, node := range nodes {
		cache.TASCache().SyncNode(testingnode.MakeNode(node).
			Label("tas-node", "true").Label(corev1.LabelHostname, node).
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:  resource.MustParse("6"),
				corev1.ResourcePods: resource.MustParse("32"),
			}).Ready().Obj())
	}
	// The victims run in cq-b, on the quota which cq-a lends it.
	for _, cq := range []*kueue.ClusterQueue{
		utiltestingapi.MakeClusterQueue("cq-a").Cohort("cohort").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("tas").Resource(corev1.ResourceCPU, "6").Obj()).
			Preemption(kueue.ClusterQueuePreemption{ReclaimWithinCohort: kueue.PreemptionPolicyAny}).Obj(),
		utiltestingapi.MakeClusterQueue("cq-b").Cohort("cohort").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("tas").Resource(corev1.ResourceCPU, "0").Obj()).
			Preemption(kueue.ClusterQueuePreemption{WithinClusterQueue: kueue.PreemptionPolicyLowerPriority}).Obj(),
	} {
		if err := cache.AddClusterQueue(ctx, cq); err != nil {
			t.Fatalf("adding ClusterQueue %s: %v", cq.Name, err)
		}
	}
	for _, w := range []*kueue.Workload{
		admitted("v1", "cq-b", 1, "3"),
		admitted("v2", "cq-b", 10, "3"),
	} {
		if !cache.AddOrUpdateWorkload(ctx, log, w) {
			t.Fatalf("adding workload %s", w.Name)
		}
	}
	snap, err := cache.Snapshot(ctx)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	s := New(nil, cache, cl, &utiltesting.EventRecorder{})

	// B may preempt v1, but not v2, which has a higher priority.
	b := workload.NewInfo(log, utiltestingapi.MakeWorkload("b", "ns").UID("b-uid").Priority(5).
		PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
			Request(corev1.ResourceCPU, "2").RequiredTopologyRequest(corev1.LabelHostname).Obj()).Obj())
	b.ClusterQueue = "cq-b"
	e := entry{Info: *b}
	e.clusterQueueSnapshot = snap.ClusterQueue("cq-b")
	plan, _ := s.getAssignments(ctx, &e.Info, snap)
	if plan.Error != nil {
		t.Fatalf("getAssignments() error = %v", plan.Error)
	}
	asgn, targets := plan.Assignment, plan.PreemptionTargets
	if got := asgn.RepresentativeMode(); got != flavorassigner.Preempt || len(targets) != 1 || workload.Key(targets[0].WorkloadInfo.Obj) != "ns/v1" {
		t.Fatalf("nomination: mode %v with %d targets, want Preempt of ns/v1", got, len(targets))
	}
	e.recordAssignment(plan, nil)

	// Earlier in the cycle, A preempted v1 and v2, and booked its usage.
	cqB := snap.ClusterQueue("cq-b")
	preempted := preemption.PreemptedWorkloads{}
	var victims []*workload.Info
	for _, name := range []string{"v1", "v2"} {
		victim := cqB.Workloads[workload.NewReference("ns", name)]
		preempted[workload.NewReference("ns", name)] = &preemption.Target{WorkloadInfo: victim}
		victims = append(victims, victim)
	}
	snap.AddCommittedVictims(ctx, victims...)
	a := workload.NewInfo(log, admitted("a", "cq-a", 20, "4"))
	snap.AddWorkload(a)

	// A and B fit together once both victims are gone.
	fits, err := s.updateAssignmentIfNeeded(ctx, log, &e, snap, cqB, preempted)
	if err != nil {
		t.Fatalf("updateAssignmentIfNeeded() error = %v", err)
	}
	if got := e.assignment.RepresentativeMode(); got != flavorassigner.DeferredFit {
		t.Errorf("recompute: mode = %v, want DeferredFit", got)
	}
	if !fits {
		t.Error("recompute: the DeferredFit does not fit once the victims are gone")
	}
	if len(e.preemptionTargets) != 0 {
		t.Errorf("recompute: preemption targets %v, want none", e.preemptionTargets)
	}
}

// TestBookingInSimulator checks that, with SchedulerLibraryDeepIntegration, the
// usage booked by a workload earlier in the cycle also takes its nodes in the
// scheduling simulator, so that the workloads processed later plan around it.
func TestBookingInSimulator(t *testing.T) {
	cases := map[string]struct {
		taintedNodes bool
	}{
		"untainted nodes": {},
		"tainted nodes tolerated via ResourceFlavor": {taintedNodes: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, map[featuregate.Feature]bool{
				kubefeatures.GenericWorkload:             true,
				features.TopologyAwareScheduling:         true,
				features.SchedulerLibraryIntegration:     true,
				features.SchedulerLibraryDeepIntegration: true,
			})
			ctx, log := utiltesting.ContextWithLog(t)
			now := time.Now().Truncate(time.Second)
			// running makes an admitted Workload running a single Pod on the node.
			running := func(name string, priority int32, node string) *kueue.Workload {
				return utiltestingapi.MakeWorkload(name, "ns").UID(types.UID(name+"-uid")).Priority(priority).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
						Request(corev1.ResourceCPU, "4").RequiredTopologyRequest(corev1.LabelHostname).Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "tas", "4").
						TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
							Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{node}, 1).Obj()).Obj()).Obj()).Obj(), now).
					Obj()
			}
			pending := func(name string) *workload.Info {
				info := workload.NewInfo(log, utiltestingapi.MakeWorkload(name, "ns").UID(types.UID(name+"-uid")).Priority(10).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
						Request(corev1.ResourceCPU, "4").RequiredTopologyRequest(corev1.LabelHostname).Obj()).Obj())
				info.ClusterQueue = "cq"
				return info
			}

			simulatorFactory, err := was.NewWASSimulatorFactory(klog.NewContext(ctx, logr.Discard()), nil)
			if err != nil {
				t.Fatalf("NewWASSimulatorFactory: %v", err)
			}
			cl := utiltesting.NewFakeClient()
			cache := schdcache.New(cl, schdcache.WithSimulatorFactory(simulatorFactory))
			cache.AddOrUpdateTopology(log, utiltestingapi.MakeDefaultOneLevelTopology("topology"))
			rf := utiltestingapi.MakeResourceFlavor("tas").
				NodeLabel("tas-node", "true").TopologyName("topology")
			if tc.taintedNodes {
				rf.Toleration(corev1.Toleration{
					Key:      "example.com/gpu",
					Operator: corev1.TolerationOpExists,
				})
			}
			cache.AddOrUpdateResourceFlavor(log, rf.Obj())
			for _, nodeName := range []string{"n1", "n2"} {
				node := testingnode.MakeNode(nodeName).
					Label("tas-node", "true").Label(corev1.LabelHostname, nodeName).
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("4"),
						corev1.ResourcePods: resource.MustParse("32"),
					}).Ready()
				if tc.taintedNodes {
					node.Taints(corev1.Taint{
						Key:    "example.com/gpu",
						Value:  "present",
						Effect: corev1.TaintEffectNoSchedule,
					})
				}
				cache.TASCache().SyncNode(node.Obj())
			}
			if err := cache.AddClusterQueue(ctx, utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("tas").Resource(corev1.ResourceCPU, "12").Obj()).
				Preemption(kueue.ClusterQueuePreemption{WithinClusterQueue: kueue.PreemptionPolicyLowerPriority}).
				Obj()); err != nil {
				t.Fatalf("adding ClusterQueue: %v", err)
			}
			for _, w := range []*kueue.Workload{running("victim", 0, "n1"), running("other", 100, "n2")} {
				if !cache.AddOrUpdateWorkload(ctx, log, w) {
					t.Fatalf("adding workload %s", w.Name)
				}
			}
			snap, err := cache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("snapshot: %v", err)
			}
			s := New(nil, cache, cl, &utiltesting.EventRecorder{})
			// Earlier in the cycle, another workload preempted the victim on n1.
			victim := snap.ClusterQueue("cq").Workloads[workload.NewReference("ns", "victim")]
			preempted := preemption.PreemptedWorkloads{workload.Key(victim.Obj): {WorkloadInfo: victim}}
			snap.AddCommittedVictims(ctx, victim)

			// X fits on n1 once the victim is gone, and books its usage.
			x := entry{Info: *pending("x")}
			x.clusterQueueSnapshot = snap.ClusterQueue("cq")
			plan, _ := s.getAssignments(ctx, &x.Info, snap)
			if plan.Error != nil {
				t.Fatalf("getAssignments(x) error = %v", plan.Error)
			}
			if got := plan.Assignment.RepresentativeMode(); got != flavorassigner.DeferredFit {
				t.Fatalf("getAssignments(x) mode = %v, want DeferredFit", got)
			}
			x.recordAssignment(plan, nil)
			s.processEntry(ctx, &x, snap, preempted, map[kueue.ClusterQueueReference]int{})
			if x.status == skipped {
				t.Fatalf("processEntry(x) skipped the workload: %s", x.inadmissibleMsg)
			}

			// Y could only take n1 as well: X is there now, also in the simulator.
			y := entry{Info: *pending("y")}
			y.clusterQueueSnapshot = snap.ClusterQueue("cq")
			plan, _ = s.getAssignments(ctx, &y.Info, snap)
			if plan.Error != nil {
				t.Fatalf("getAssignments(y) error = %v", plan.Error)
			}
			if got := plan.Assignment.RepresentativeMode(); got == flavorassigner.Fit || got == flavorassigner.DeferredFit {
				t.Errorf("getAssignments(y) mode = %v, want neither Fit nor DeferredFit, as X takes n1", got)
			}
		})
	}
}

// TestDeferredFitOfSliceReplacement checks that, with
// SchedulerLibraryDeepIntegration, a workload slice replacement waiting for a
// committed victim keeps the replaced slice among its preemption targets, so
// that the replaced slice's usage is taken out along with it.
func TestDeferredFitOfSliceReplacement(t *testing.T) {
	features.SetFeatureGatesDuringTest(t, map[featuregate.Feature]bool{
		kubefeatures.GenericWorkload:                 true,
		features.TopologyAwareScheduling:             true,
		features.SchedulerLibraryIntegration:         true,
		features.SchedulerLibraryDeepIntegration:     true,
		features.ElasticJobsViaWorkloadSlices:        true,
		features.ElasticJobsViaWorkloadSlicesWithTAS: true,
	})
	ctx, log := utiltesting.ContextWithLog(t)
	now := time.Now().Truncate(time.Second)
	// admitted makes an admitted Workload of count Pods of 1 CPU on the node.
	admitted := func(name string, count int32, node string) *utiltestingapi.WorkloadWrapper {
		return utiltestingapi.MakeWorkload(name, "ns").UID(types.UID(name+"-uid")).
			PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, int(count)).
				Request(corev1.ResourceCPU, "1").UnconstrainedTopologyRequest().Obj()).
			ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
				Count(count).
				Assignment(corev1.ResourceCPU, "tas", resource.NewQuantity(int64(count), resource.DecimalSI).String()).
				TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
					Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{node}, count).Obj()).Obj()).Obj()).Obj(), now).
			AdmittedAt(true, now)
	}

	simulatorFactory, err := was.NewWASSimulatorFactory(klog.NewContext(ctx, logr.Discard()), nil)
	if err != nil {
		t.Fatalf("NewWASSimulatorFactory: %v", err)
	}
	cl := utiltesting.NewFakeClient()
	cache := schdcache.New(cl, schdcache.WithSimulatorFactory(simulatorFactory))
	cache.AddOrUpdateTopology(log, utiltestingapi.MakeDefaultOneLevelTopology("topology"))
	cache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("tas").
		NodeLabel("tas-node", "true").TopologyName("topology").Obj())
	for node, cpu := range map[string]string{"n1": "4", "n2": "2"} {
		cache.TASCache().SyncNode(testingnode.MakeNode(node).
			Label("tas-node", "true").Label(corev1.LabelHostname, node).
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:  resource.MustParse(cpu),
				corev1.ResourcePods: resource.MustParse("32"),
			}).Ready().Obj())
	}
	if err := cache.AddClusterQueue(ctx, utiltestingapi.MakeClusterQueue("cq").
		ResourceGroup(*utiltestingapi.MakeFlavorQuotas("tas").Resource(corev1.ResourceCPU, "10").Obj()).
		Obj()); err != nil {
		t.Fatalf("adding ClusterQueue: %v", err)
	}
	// The replaced slice takes half of n1, and the victim, being evicted, all of n2.
	old := admitted("old", 2, "n1").Annotation(constants.ElasticJobAnnotation, "true").Obj()
	victim := admitted("victim", 2, "n2").Obj()
	for _, w := range []*kueue.Workload{old, victim} {
		if !cache.AddOrUpdateWorkload(ctx, log, w) {
			t.Fatalf("adding workload %s", w.Name)
		}
	}
	snap, err := cache.Snapshot(ctx)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	snap.AddCommittedVictims(ctx, snap.ClusterQueue("cq").Workloads[workload.NewReference("ns", "victim")])

	// The replacement grows the slice to 4 Pods, which fit only once the victim is gone.
	replacement := workload.NewInfo(log, utiltestingapi.MakeWorkload("replacement", "ns").UID("replacement-uid").
		Annotation(constants.ElasticJobAnnotation, "true").
		Annotation(workloadslicing.WorkloadSliceReplacementFor, "ns/old").
		Annotation(kueue.WorkloadSliceNameAnnotation, "old").
		PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 4).
			Request(corev1.ResourceCPU, "1").UnconstrainedTopologyRequest().Obj()).Obj())
	replacement.ClusterQueue = "cq"
	s := New(nil, cache, cl, &utiltesting.EventRecorder{})
	e := entry{Info: *replacement}
	e.clusterQueueSnapshot = snap.ClusterQueue("cq")
	plan, retryAt := s.getAssignments(ctx, &e.Info, snap)
	if plan.Error != nil {
		t.Fatalf("getAssignments() error = %v", plan.Error)
	}
	if got := plan.Assignment.RepresentativeMode(); got != flavorassigner.DeferredFit {
		t.Fatalf("getAssignments() mode = %v, want %v", got, flavorassigner.DeferredFit)
	}
	var gotTargets []workload.Reference
	for _, target := range plan.PreemptionTargets {
		gotTargets = append(gotTargets, workload.Key(target.WorkloadInfo.Obj))
	}
	if diff := cmp.Diff([]workload.Reference{"ns/old"}, gotTargets); diff != "" {
		t.Errorf("getAssignments() preemption targets mismatch (-want,+got):\n%s", diff)
	}

	e.recordAssignment(plan, retryAt)
	s.processEntry(ctx, &e, snap, preemption.PreemptedWorkloads{}, map[kueue.ClusterQueueReference]int{})
	if e.status == skipped {
		t.Fatalf("processEntry() skipped the replacement: %s", e.inadmissibleMsg)
	}
	if want := kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads; e.quotaReservedReason != want {
		t.Errorf("processEntry() quota reserved reason = %q, want %q", e.quotaReservedReason, want)
	}
}

// TestDeferredFitRequeueReason checks that, with SchedulerLibraryDeepIntegration,
// only the preemptor of its ClusterQueue keeps its place at the head while
// waiting for committed victims, using the scheduling simulator of scheduler-library.
func TestDeferredFitRequeueReason(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	running := func(name string, priority int32, node string) *kueue.Workload {
		return utiltestingapi.MakeWorkload(name, "ns").UID(types.UID(name+"-uid")).Priority(priority).
			PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
				Request(corev1.ResourceCPU, "4").RequiredTopologyRequest(corev1.LabelHostname).Obj()).
			ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
				Assignment(corev1.ResourceCPU, "tas", "4").
				TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
					Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{node}, 1).Obj()).Obj()).Obj()).Obj(), now).
			Obj()
	}
	cases := map[string]struct {
		isPreemptor bool
		want        qcache.RequeueReason
	}{
		"the preemptor of the ClusterQueue keeps its place at the head": {
			isPreemptor: true,
			want:        qcache.RequeueReasonPendingPreemption,
		},
		"another workload waits for the evictions to complete": {
			isPreemptor: false,
			want:        qcache.RequeueReasonWaitingForEvictions,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, map[featuregate.Feature]bool{
				kubefeatures.GenericWorkload:             true,
				features.TopologyAwareScheduling:         true,
				features.SchedulerLibraryIntegration:     true,
				features.SchedulerLibraryDeepIntegration: true,
			})
			ctx, log := utiltesting.ContextWithLog(t)

			simulatorFactory, err := was.NewWASSimulatorFactory(klog.NewContext(ctx, logr.Discard()), nil)
			if err != nil {
				t.Fatalf("NewWASSimulatorFactory: %v", err)
			}
			cl := utiltesting.NewFakeClient()
			cache := schdcache.New(cl, schdcache.WithSimulatorFactory(simulatorFactory))
			cache.AddOrUpdateTopology(log, utiltestingapi.MakeDefaultOneLevelTopology("topology"))
			cache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("tas").
				NodeLabel("tas-node", "true").TopologyName("topology").Obj())
			for _, node := range []string{"n1", "n2"} {
				cache.TASCache().SyncNode(testingnode.MakeNode(node).
					Label("tas-node", "true").Label(corev1.LabelHostname, node).
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("4"),
						corev1.ResourcePods: resource.MustParse("32"),
					}).Ready().Obj())
			}
			if err := cache.AddClusterQueue(ctx, utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("tas").Resource(corev1.ResourceCPU, "12").Obj()).
				Obj()); err != nil {
				t.Fatalf("adding ClusterQueue: %v", err)
			}
			for _, w := range []*kueue.Workload{running("victim", 0, "n1"), running("other", 100, "n2")} {
				if !cache.AddOrUpdateWorkload(ctx, log, w) {
					t.Fatalf("adding workload %s", w.Name)
				}
			}
			snap, err := cache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("snapshot: %v", err)
			}
			cq := snap.ClusterQueue("cq")
			snap.AddCommittedVictims(ctx, cq.Workloads[workload.NewReference("ns", "victim")])

			incoming := workload.NewInfo(log, utiltestingapi.MakeWorkload("incoming", "ns").UID("incoming-uid").Priority(10).
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					Request(corev1.ResourceCPU, "4").RequiredTopologyRequest(corev1.LabelHostname).Obj()).Obj())
			incoming.ClusterQueue = "cq"
			s := New(nil, cache, cl, &utiltesting.EventRecorder{})
			e := entry{Info: *incoming, IsPreemptor: tc.isPreemptor}
			e.clusterQueueSnapshot = cq
			plan, _ := s.getAssignments(ctx, &e.Info, snap)
			if plan.Error != nil {
				t.Fatalf("getAssignments() error = %v", plan.Error)
			}
			if got := plan.Assignment.RepresentativeMode(); got != flavorassigner.DeferredFit {
				t.Fatalf("getAssignments() mode = %v, want %v", got, flavorassigner.DeferredFit)
			}
			e.recordAssignment(plan, nil)

			s.processEntry(ctx, &e, snap, preemption.PreemptedWorkloads{}, map[kueue.ClusterQueueReference]int{})
			if e.requeueReason != tc.want {
				t.Errorf("processEntry() requeue reason = %q, want %q", e.requeueReason, tc.want)
			}
		})
	}
}

// TestTerminatingPodsAsCommittedVictims checks that a Workload waits for the
// Pods being deleted while no Workload holding quota covers them, e.g. the Pods
// of an evicted Workload which already released its quota, instead of looking
// for other placements or victims. It uses the scheduling simulator of
// scheduler-library.
func TestTerminatingPodsAsCommittedVictims(t *testing.T) {
	now := metav1.NewTime(time.Now().Truncate(time.Second))
	cases := map[string]struct {
		// deleting is whether the Pod of the released Workload is being deleted.
		deleting bool
		// podCPU is the CPU of that Pod on n1, 4 (all of n1) by default.
		podCPU string
		// lowerPriorityOnN1 adds a Workload of lower priority than the
		// incoming one, taking the rest of n1.
		lowerPriorityOnN1 bool

		wantMode    flavorassigner.FlavorAssignmentMode
		wantTargets []workload.Reference
		wantNode    string
		wantMessage string
		wantReason  qcache.RequeueReason
	}{
		// The quota of the Pod is free already, so the Workload keeps its place
		// at the head while waiting for it, as without SchedulerLibraryDeepIntegration.
		"waits for the Pod being deleted": {
			deleting:    true,
			wantMode:    flavorassigner.DeferredFit,
			wantNode:    "n1",
			wantMessage: "Workload will fit after bound Pods from released TAS reservations terminate or are deleted",
			wantReason:  qcache.RequeueReasonPendingPreemption,
		},
		"does not wait for a Pod which is not being deleted": {
			wantMode: flavorassigner.Preempt,
		},
		// Like a committed victim Workload, the Pod being deleted counts as
		// gone in the search for new victims.
		"preempts next to a Pod being deleted": {
			deleting:          true,
			podCPU:            "2",
			lowerPriorityOnN1: true,
			wantMode:          flavorassigner.Preempt,
			wantTargets:       []workload.Reference{"ns/lower"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, map[featuregate.Feature]bool{
				kubefeatures.GenericWorkload:             true,
				features.TopologyAwareScheduling:         true,
				features.SchedulerLibraryIntegration:     true,
				features.SchedulerLibraryDeepIntegration: true,
			})
			ctx, log := utiltesting.ContextWithLog(t)

			simulatorFactory, err := was.NewWASSimulatorFactory(klog.NewContext(ctx, logr.Discard()), nil)
			if err != nil {
				t.Fatalf("NewWASSimulatorFactory: %v", err)
			}
			cl := utiltesting.NewFakeClient()
			cache := schdcache.New(cl, schdcache.WithSimulatorFactory(simulatorFactory))
			cache.AddOrUpdateTopology(log, utiltestingapi.MakeDefaultOneLevelTopology("topology"))
			cache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("tas").
				NodeLabel("tas-node", "true").TopologyName("topology").Obj())
			for _, node := range []string{"n1", "n2"} {
				cache.TASCache().SyncNode(testingnode.MakeNode(node).
					Label("tas-node", "true").Label(corev1.LabelHostname, node).
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("4"),
						corev1.ResourcePods: resource.MustParse("32"),
					}).Ready().Obj())
			}
			if err := cache.AddClusterQueue(ctx, utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("tas").Resource(corev1.ResourceCPU, "10").Obj()).
				Preemption(kueue.ClusterQueuePreemption{WithinClusterQueue: kueue.PreemptionPolicyLowerPriority}).
				Obj()); err != nil {
				t.Fatalf("adding ClusterQueue: %v", err)
			}
			// A Workload of higher priority than the incoming one takes n2.
			other := utiltestingapi.MakeWorkload("other", "ns").UID("other-uid").Priority(100).
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					Request(corev1.ResourceCPU, "4").RequiredTopologyRequest(corev1.LabelHostname).Obj()).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
					Assignment(corev1.ResourceCPU, "tas", "4").
					TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
						Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"n2"}, 1).Obj()).Obj()).Obj()).Obj(), now.Time).
				Obj()
			if !cache.AddOrUpdateWorkload(ctx, log, other) {
				t.Fatal("adding workload other")
			}
			if tc.lowerPriorityOnN1 {
				lower := utiltestingapi.MakeWorkload("lower", "ns").UID("lower-uid").Priority(1).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
						Request(corev1.ResourceCPU, "2").RequiredTopologyRequest(corev1.LabelHostname).Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "tas", "2").
						TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
							Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"n1"}, 1).Obj()).Obj()).Obj()).Obj(), now.Time).
					Obj()
				if !cache.AddOrUpdateWorkload(ctx, log, lower) {
					t.Fatal("adding workload lower")
				}
			}
			podCPU := tc.podCPU
			if podCPU == "" {
				podCPU = "4"
			}
			// The Pod of a Workload which released its quota still takes n1.
			pod := testingpod.MakePod("released-pod", "ns").UID("released-pod-uid").NodeName("n1").
				Request(corev1.ResourceCPU, podCPU).Annotation(kueue.WorkloadAnnotation, "released").Obj()
			if tc.deleting {
				pod.DeletionTimestamp = &now
			}
			cache.TrackPod(ctx, pod)
			cache.TASCache().UpdateTASPodUsage(pod, log)

			snap, err := cache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("snapshot: %v", err)
			}
			snap.AddTerminatingPodsAsCommittedVictims(ctx)

			incoming := workload.NewInfo(log, utiltestingapi.MakeWorkload("incoming", "ns").UID("incoming-uid").Priority(10).
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					Request(corev1.ResourceCPU, "4").RequiredTopologyRequest(corev1.LabelHostname).Obj()).Obj())
			incoming.ClusterQueue = "cq"
			s := New(nil, cache, cl, &utiltesting.EventRecorder{})
			e := entry{Info: *incoming}
			e.clusterQueueSnapshot = snap.ClusterQueue("cq")
			plan, _ := s.getAssignments(ctx, &e.Info, snap)
			if plan.Error != nil {
				t.Fatalf("getAssignments() error = %v", plan.Error)
			}
			asgn, targets := plan.Assignment, plan.PreemptionTargets
			if got := asgn.RepresentativeMode(); got != tc.wantMode {
				t.Errorf("getAssignments() mode = %v, want %v", got, tc.wantMode)
			}
			var gotTargets []workload.Reference
			for _, target := range targets {
				gotTargets = append(gotTargets, workload.Key(target.WorkloadInfo.Obj))
			}
			if diff := cmp.Diff(tc.wantTargets, gotTargets); diff != "" {
				t.Errorf("getAssignments() preemption targets mismatch (-want,+got):\n%s", diff)
			}
			if tc.wantNode == "" {
				return
			}
			if ta := asgn.PodSets[0].TopologyAssignment; ta == nil || len(ta.Domains) != 1 || ta.Domains[0].Values[0] != tc.wantNode {
				t.Errorf("getAssignments() topology assignment = %v, want the node %q", ta, tc.wantNode)
			}
			// The fit check accounts for the Pod going away.
			e.recordAssignment(plan, nil)
			s.processEntry(ctx, &e, snap, preemption.PreemptedWorkloads{}, map[kueue.ClusterQueueReference]int{})
			if e.inadmissibleMsg != tc.wantMessage {
				t.Errorf("processEntry() message = %q, want %q", e.inadmissibleMsg, tc.wantMessage)
			}
			if e.requeueReason != tc.wantReason {
				t.Errorf("processEntry() requeue reason = %q, want %q", e.requeueReason, tc.wantReason)
			}
		})
	}
}

// TestDeferFailedTASReplacementWaitingForCommittedVictims checks that, with
// SchedulerLibraryDeepIntegration, a failed TAS replacement is deferred while
// it waits for committed victims, whether Workloads or Pods.
func TestDeferFailedTASReplacementWaitingForCommittedVictims(t *testing.T) {
	cases := map[string]struct {
		mode                      flavorassigner.FlavorAssignmentMode
		waitingForResidualTASPods bool
		want                      bool
	}{
		"waits for committed victim Workloads": {
			mode: flavorassigner.DeferredFit,
			want: true,
		},
		"waits for committed victim Pods": {
			mode:                      flavorassigner.DeferredFit,
			waitingForResidualTASPods: true,
			want:                      true,
		},
		"does not wait": {
			mode: flavorassigner.NoFit,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, map[featuregate.Feature]bool{
				features.TopologyAwareScheduling:                                    true,
				features.SchedulerLibraryIntegration:                                true,
				features.SchedulerLibraryDeepIntegration:                            true,
				features.TASNodeReplacementDeferFailFastWhenUsefulInflightEvictions: true,
			})
			ctx, log := utiltesting.ContextWithLog(t)
			timeout := time.Minute
			s := New(nil, schdcache.New(utiltesting.NewFakeClient()), utiltesting.NewFakeClient(), &utiltesting.EventRecorder{},
				WithFailedTASReplacementRecoveryTimeout(&timeout))
			e := entry{}
			e.assignment = flavorassigner.Assignment{PodSets: []flavorassigner.PodSetAssignment{{Name: kueue.DefaultPodSetName}}}
			e.assignment.SetRepresentativeMode(tc.mode)
			e.assignment.WaitingForResidualTASPods = tc.waitingForResidualTASPods

			if got := s.tryDeferFailedTASReplacement(ctx, log, &e, nil); got != tc.want {
				t.Errorf("tryDeferFailedTASReplacement() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestResidualWaitRecomputedWhenNoLongerFitting checks that, with
// SchedulerLibraryDeepIntegration, a DeferredFit waiting for Pods being deleted
// is recomputed within the cycle once another workload takes their node, as
// the fit check releases the usage of these Pods.
func TestResidualWaitRecomputedWhenNoLongerFitting(t *testing.T) {
	features.SetFeatureGatesDuringTest(t, map[featuregate.Feature]bool{
		kubefeatures.GenericWorkload:                         true,
		features.TopologyAwareScheduling:                     true,
		features.SchedulerLibraryIntegration:                 true,
		features.SchedulerLibraryDeepIntegration:             true,
		features.TASRecomputeAssignmentWithinSchedulingCycle: true,
	})
	ctx, log := utiltesting.ContextWithLog(t)
	now := metav1.NewTime(time.Now().Truncate(time.Second))

	simulatorFactory, err := was.NewWASSimulatorFactory(klog.NewContext(ctx, logr.Discard()), nil)
	if err != nil {
		t.Fatalf("NewWASSimulatorFactory: %v", err)
	}
	cl := utiltesting.NewFakeClient()
	cache := schdcache.New(cl, schdcache.WithSimulatorFactory(simulatorFactory))
	cache.AddOrUpdateTopology(log, utiltestingapi.MakeDefaultOneLevelTopology("topology"))
	cache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("tas").
		NodeLabel("tas-node", "true").TopologyName("topology").Obj())
	for _, node := range []string{"n1", "n2"} {
		cache.TASCache().SyncNode(testingnode.MakeNode(node).
			Label("tas-node", "true").Label(corev1.LabelHostname, node).
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:  resource.MustParse("4"),
				corev1.ResourcePods: resource.MustParse("32"),
			}).Ready().Obj())
	}
	if err := cache.AddClusterQueue(ctx, utiltestingapi.MakeClusterQueue("cq").
		ResourceGroup(*utiltestingapi.MakeFlavorQuotas("tas").Resource(corev1.ResourceCPU, "12").Obj()).
		Preemption(kueue.ClusterQueuePreemption{WithinClusterQueue: kueue.PreemptionPolicyLowerPriority}).
		Obj()); err != nil {
		t.Fatalf("adding ClusterQueue: %v", err)
	}
	// A Workload of higher priority than the incoming ones takes n2.
	other := utiltestingapi.MakeWorkload("other", "ns").UID("other-uid").Priority(100).
		PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
			Request(corev1.ResourceCPU, "4").RequiredTopologyRequest(corev1.LabelHostname).Obj()).
		ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
			Assignment(corev1.ResourceCPU, "tas", "4").
			TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
				Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"n2"}, 1).Obj()).Obj()).Obj()).Obj(), now.Time).
		Obj()
	if !cache.AddOrUpdateWorkload(ctx, log, other) {
		t.Fatal("adding workload other")
	}
	// The Pod of a Workload which released its quota takes n1, and is being deleted.
	pod := testingpod.MakePod("released-pod", "ns").UID("released-pod-uid").NodeName("n1").
		Request(corev1.ResourceCPU, "4").Annotation(kueue.WorkloadAnnotation, "released").Obj()
	pod.DeletionTimestamp = &now
	cache.TrackPod(ctx, pod)
	cache.TASCache().UpdateTASPodUsage(pod, log)

	snap, err := cache.Snapshot(ctx)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	snap.AddTerminatingPodsAsCommittedVictims(ctx)
	s := New(nil, cache, cl, &utiltesting.EventRecorder{})
	cq := snap.ClusterQueue("cq")
	// pending makes an entry which, with the Pod being deleted, waits for it on n1.
	pending := func(name string) *entry {
		t.Helper()
		info := workload.NewInfo(log, utiltestingapi.MakeWorkload(name, "ns").UID(types.UID(name+"-uid")).Priority(10).
			PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
				Request(corev1.ResourceCPU, "4").RequiredTopologyRequest(corev1.LabelHostname).Obj()).Obj())
		info.ClusterQueue = "cq"
		e := &entry{Info: *info}
		e.clusterQueueSnapshot = cq
		plan, retryAt := s.getAssignments(ctx, &e.Info, snap)
		if plan.Error != nil {
			t.Fatalf("getAssignments(%s) error = %v", name, plan.Error)
		}
		if got := plan.Assignment.RepresentativeMode(); got != flavorassigner.DeferredFit || !plan.Assignment.WaitingForResidualTASPods {
			t.Fatalf("getAssignments(%s) = %v (waiting for residual TAS Pods: %v), want a DeferredFit waiting for them",
				name, got, plan.Assignment.WaitingForResidualTASPods)
		}
		e.recordAssignment(plan, retryAt)
		return e
	}
	x, y := pending("x"), pending("y")

	// Y takes n1.
	s.processEntry(ctx, y, snap, preemption.PreemptedWorkloads{}, map[kueue.ClusterQueueReference]int{})
	if y.status == skipped {
		t.Fatalf("processEntry(y) skipped the workload: %s", y.inadmissibleMsg)
	}
	// Meanwhile, another workload preempted the one on n2.
	snap.AddCommittedVictims(ctx, cq.Workloads[workload.NewReference("ns", "other")])

	// X no longer fits on n1, and is recomputed to wait for the victim on n2.
	fits, err := s.updateAssignmentIfNeeded(ctx, log, x, snap, cq, preemption.PreemptedWorkloads{})
	if err != nil {
		t.Fatalf("updateAssignmentIfNeeded(x) error = %v", err)
	}
	if !fits {
		t.Errorf("updateAssignmentIfNeeded(x) = false, want true after recomputing onto n2")
	}
	if ta := x.assignment.PodSets[0].TopologyAssignment; ta == nil || len(ta.Domains) != 1 || ta.Domains[0].Values[0] != "n2" {
		t.Errorf("recomputed topology assignment = %v, want the node n2", ta)
	}
}
