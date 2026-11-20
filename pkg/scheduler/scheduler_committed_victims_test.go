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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/component-base/featuregate"
	"k8s.io/klog/v2"
	kubefeatures "k8s.io/kubernetes/pkg/features"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/cache/scheduler/was"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/scheduler/flavorassigner"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	"sigs.k8s.io/kueue/pkg/workload"
)

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
			asgn, targets, _, err := s.getAssignments(ctx, incoming, snap)
			if err != nil {
				t.Fatalf("getAssignments() error = %v", err)
			}

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
	cache.TASCache().SyncNode(testingnode.MakeNode("n1").
		Label("tas-node", "true").Label(corev1.LabelHostname, "n1").
		StatusAllocatable(corev1.ResourceList{
			corev1.ResourceCPU:  resource.MustParse("6"),
			corev1.ResourcePods: resource.MustParse("32"),
		}).Ready().Obj())
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
	asgn, targets, _, err := s.getAssignments(ctx, &e.Info, snap)
	if err != nil {
		t.Fatalf("getAssignments() error = %v", err)
	}
	if got := asgn.RepresentativeMode(); got != flavorassigner.Preempt || len(targets) != 1 || workload.Key(targets[0].WorkloadInfo.Obj) != "ns/v1" {
		t.Fatalf("nomination: mode %v with %d targets, want Preempt of ns/v1", got, len(targets))
	}
	e.recordAssignment(asgn, targets, nil)

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
