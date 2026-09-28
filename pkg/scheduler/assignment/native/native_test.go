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

package native

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/testr"
	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/component-base/featuregate"
	clocktesting "k8s.io/utils/clock/testing"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/scheduler/flavorassigner"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
	"sigs.k8s.io/kueue/pkg/util/tas"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
)

const (
	tasTestCQ       = "tas-cq"
	tasTestFlavor   = "tas-flavor"
	tasTestNode     = "node-1"
	tasTestNodeCPUs = "6"

	blockerName = "blocker"
	victimName  = "victim"

	// The incoming workload may preempt workloads with a lower priority only.
	lowPriority      = 0
	incomingPriority = 50
	highPriority     = 100
)

// newTASTestSnapshot builds a snapshot with a single TAS flavor backed by one
// hostname-level node with 6 CPU, and a ClusterQueue with the given CPU quota
// that allows preempting lower-priority workloads within the ClusterQueue.
// Two admitted workloads, "blocker" and "victim", occupy 2 CPU of the node each,
// leaving 2 CPU free. The victim always has a lower priority than the incoming
// workload. The blocker has a higher priority, unless evictableBlocker is set.
func newTASTestSnapshot(ctx context.Context, t *testing.T, log logr.Logger, quota string, evictableBlocker bool) *schdcache.Snapshot {
	t.Helper()

	cache := schdcache.New(utiltesting.NewFakeClient())
	cache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor(tasTestFlavor).
		NodeLabel("tas", "true").TopologyName("topology").Obj())
	cache.AddOrUpdateTopology(log, utiltestingapi.MakeTopology("topology").Levels(corev1.LabelHostname).Obj())
	cache.TASCache().SyncNode(testingnode.MakeNode(tasTestNode).
		Label("tas", "true").
		Label(corev1.LabelHostname, tasTestNode).
		StatusAllocatable(corev1.ResourceList{
			corev1.ResourceCPU:  resource.MustParse(tasTestNodeCPUs),
			corev1.ResourcePods: resource.MustParse("32"),
		}).
		Ready().
		Obj())
	if err := cache.AddClusterQueue(ctx, utiltestingapi.MakeClusterQueue(tasTestCQ).
		Preemption(kueue.ClusterQueuePreemption{WithinClusterQueue: kueue.PreemptionPolicyLowerPriority}).
		ResourceGroup(*utiltestingapi.MakeFlavorQuotas(tasTestFlavor).Resource(corev1.ResourceCPU, quota).Obj()).
		Obj()); err != nil {
		t.Fatalf("adding ClusterQueue: %v", err)
	}

	blockerPriority := int32(highPriority)
	if evictableBlocker {
		blockerPriority = lowPriority
	}
	for name, priority := range map[string]int32{blockerName: blockerPriority, victimName: lowPriority} {
		wl := utiltestingapi.MakeWorkload(name, "default").
			Priority(priority).
			PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
				RequiredTopologyRequest(corev1.LabelHostname).
				Request(corev1.ResourceCPU, "2").
				Obj()).
			ReserveQuotaAt(utiltestingapi.MakeAdmission(tasTestCQ).
				PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
					Assignment(corev1.ResourceCPU, tasTestFlavor, "2").
					TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
						Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{tasTestNode}, 1).Obj()).
						Obj()).
					Obj()).
				Obj(), time.Now()).
			Obj()
		if !cache.AddOrUpdateWorkload(ctx, log, wl) {
			t.Fatalf("adding workload %q to the cache", name)
		}
	}

	snapshot, err := cache.Snapshot(ctx)
	if err != nil {
		t.Fatalf("building snapshot: %v", err)
	}
	return snapshot
}

func freeCapacity(t *testing.T, snapshot *schdcache.Snapshot) string {
	t.Helper()
	got, err := snapshot.ClusterQueue(tasTestCQ).TASFlavors[tasTestFlavor].SerializeFreeCapacityPerDomain()
	if err != nil {
		t.Fatalf("serializing TAS free capacity: %v", err)
	}
	return got
}

func targetNames(targets []*preemption.Target) []string {
	var names []string
	for _, target := range targets {
		names = append(names, target.WorkloadInfo.Obj.Name)
	}
	slices.Sort(names)
	return names
}

func TestPlanElasticSliceWithResidualTASPods(t *testing.T) {
	cases := map[string]struct {
		residual           bool
		remainingVictim    bool
		removedPredecessor bool
		wantMode           flavorassigner.FlavorAssignmentMode
		wantWaiting        bool
	}{
		"waits for a deleting Pod without counting the predecessor twice": {
			residual: true, wantMode: flavorassigner.DeferredFit, wantWaiting: true,
		},
		"fits after the deleting Pod has disappeared": {wantMode: flavorassigner.Fit},
		"waits for an existing evicted victim without preempting an unrelated workload": {
			residual: true, remainingVictim: true, wantMode: flavorassigner.DeferredFit, wantWaiting: true,
		},
		"does not subtract a predecessor removed before the remaining victim probe": {
			residual: true, remainingVictim: true, removedPredecessor: true,
			wantMode: flavorassigner.DeferredFit, wantWaiting: true,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.TopologyAwareScheduling, true)
			features.SetFeatureGateDuringTest(t, features.ElasticJobsViaWorkloadSlices, true)
			features.SetFeatureGateDuringTest(t, features.ElasticJobsViaWorkloadSlicesWithTAS, true)
			ctx, log := utiltesting.ContextWithLog(t)
			cache := schdcache.New(utiltesting.NewFakeClient())
			cache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor(tasTestFlavor).
				NodeLabel("tas", "true").TopologyName("topology").Obj())
			cache.AddOrUpdateTopology(log, utiltestingapi.MakeTopology("topology").Levels(corev1.LabelHostname).Obj())
			nodeCPU := "4"
			newCount := 4
			if tc.remainingVictim {
				nodeCPU = "6"
				newCount = 6
			}
			cache.TASCache().SyncNode(testingnode.MakeNode(tasTestNode).
				Label("tas", "true").Label(corev1.LabelHostname, tasTestNode).
				StatusAllocatable(corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse(nodeCPU), corev1.ResourcePods: resource.MustParse("32"),
				}).Ready().Obj())
			if err := cache.AddClusterQueue(ctx, utiltestingapi.MakeClusterQueue(tasTestCQ).
				Preemption(kueue.ClusterQueuePreemption{WithinClusterQueue: kueue.PreemptionPolicyLowerPriority}).
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas(tasTestFlavor).Resource(corev1.ResourceCPU, "10").Obj()).Obj()); err != nil {
				t.Fatalf("adding ClusterQueue: %v", err)
			}
			old := utiltestingapi.MakeWorkload("old", "default").UID("old-uid").Priority(incomingPriority).
				Annotation("kueue.x-k8s.io/elastic-job", "true").
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).
					Request(corev1.ResourceCPU, "1").UnconstrainedTopologyRequest().Obj()).
				ReserveQuotaAt(utiltestingapi.MakeAdmission(tasTestCQ).PodSets(
					utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Count(2).
						Assignment(corev1.ResourceCPU, tasTestFlavor, "2").
						TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
							Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{tasTestNode}, 2).Obj()).Obj()).Obj()).Obj(), time.Now()).
				AdmittedAt(true, time.Now()).Obj()
			if !cache.AddOrUpdateWorkload(ctx, log, old) {
				t.Fatal("adding predecessor")
			}
			if tc.remainingVictim {
				cache.TASCache().SyncNode(testingnode.MakeNode("node-2").
					Label("tas", "true").Label(corev1.LabelHostname, "node-2").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("4"), corev1.ResourcePods: resource.MustParse("32"),
					}).Ready().Obj())
				for name, node := range map[string]string{"victim": tasTestNode, "other": "node-2"} {
					count := 1
					cpu := "1"
					if name == "other" {
						cpu = "4"
					}
					victim := utiltestingapi.MakeWorkload(name, "default").Priority(lowPriority).
						PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, count).
							Request(corev1.ResourceCPU, cpu).UnconstrainedTopologyRequest().Obj()).
						ReserveQuotaAt(utiltestingapi.MakeAdmission(tasTestCQ).PodSets(
							utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Count(int32(count)).
								Assignment(corev1.ResourceCPU, tasTestFlavor, cpu).
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{node}, int32(count)).Obj()).Obj()).Obj()).Obj(), time.Now())
					if name == "victim" {
						victim.EvictedAt(time.Now())
					}
					if !cache.AddOrUpdateWorkload(ctx, log, victim.Obj()) {
						t.Fatalf("adding %s", name)
					}
				}
			}
			if tc.residual {
				pod := &corev1.Pod{
					Namespace: "default", Name: "residual", UID: "residual-uid",
					Annotations:       map[string]string{kueue.WorkloadAnnotation: "released"},
					DeletionTimestamp: new(metav1.Now()),
					Spec: corev1.PodSpec{NodeName: tasTestNode, Containers: []corev1.Container{{Name: "main",
						Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")}},
					}}},
				}
				cache.TASCache().UpdateTASPodUsage(pod, log)
			}
			snapshot, err := cache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("snapshot: %v", err)
			}
			cq := snapshot.ClusterQueue(tasTestCQ)
			predecessor := cq.Workloads[workload.Key(old)]
			if tc.removedPredecessor {
				snapshot.RemoveWorkload(predecessor)
			}
			incoming := workload.NewInfo(log, utiltestingapi.MakeWorkload("new", "default").Priority(incomingPriority).
				Annotation("kueue.x-k8s.io/elastic-job", "true").
				Annotation(workloadslicing.WorkloadSliceReplacementFor, string(workload.Key(old))).
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, newCount).
					Request(corev1.ResourceCPU, "1").UnconstrainedTopologyRequest().Obj()).Obj())
			incoming.ClusterQueue = tasTestCQ
			preemptor := preemption.New(utiltesting.NewFakeClient(), workload.Ordering{}, &utiltesting.EventRecorder{}, nil, false,
				clocktesting.NewFakeClock(time.Now()), nil, preemptexpectations.New(), nil)
			assigner := flavorassigner.New(incoming, cq, snapshot.ResourceFlavors, false,
				preemption.NewOracle(preemptor, snapshot), predecessor,
				configapi.QuotaCheckBlockUndeclared, resources.NewResourceFormatter(), 0)
			asgn := assigner.AssignFlavors(ctx, log, nil)
			wantQuotaMode := flavorassigner.Fit
			if tc.remainingVictim && !tc.removedPredecessor {
				wantQuotaMode = flavorassigner.Preempt
			}
			if got := asgn.RepresentativeMode(); got != wantQuotaMode {
				t.Fatalf("initial quota mode=%v, want %v", got, wantQuotaMode)
			}
			before := freeCapacity(t, snapshot)
			usageBefore := cq.ResourceNode.Usage.Clone()
			plan := NewPlanner(incoming, snapshot, preemptor, assigner).Plan(ctx, &asgn)
			if got := plan.Assignment.RepresentativeMode(); got != tc.wantMode {
				t.Errorf("plan mode=%v, want %v", got, tc.wantMode)
			}
			if asgn.WaitingForResidualTASPods != tc.wantWaiting {
				t.Errorf("waiting=%v, want %v", asgn.WaitingForResidualTASPods, tc.wantWaiting)
			}
			if len(plan.PreemptionTargets) != 0 {
				t.Errorf("unexpected preemption targets: %v", targetNames(plan.PreemptionTargets))
			}
			if after := freeCapacity(t, snapshot); before != after {
				t.Errorf("probe changed TAS capacity: before=%s after=%s", before, after)
			}
			if diff := cmp.Diff(usageBefore, cq.ResourceNode.Usage); diff != "" {
				t.Errorf("probe changed quota usage (-want +got):\n%s", diff)
			}
			if got := cq.Workloads[workload.Key(old)] == nil; got != tc.removedPredecessor {
				t.Errorf("probe changed predecessor presence: removed=%v, want %v", got, tc.removedPredecessor)
			}
		})
	}
}

// TestPlan runs Plan with the real flavor assigner and preemptor. The
// initial assignment is computed by the flavor assigner, as in the scheduler,
// so that only the combinations of quota, topology and preemption outcomes that
// can happen in practice are covered.
func TestPlan(t *testing.T) {
	onNode := &utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
		Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{tasTestNode}, 1).Obj()).
		TopologyAssignment

	cases := map[string]struct {
		quota            string
		evictableBlocker bool
		// cpu is the CPU requested by the single pod of the incoming workload.
		cpu string
		// initialMode documents the quota-based mode computed by the flavor assigner.
		initialMode flavorassigner.FlavorAssignmentMode

		wantMode               flavorassigner.FlavorAssignmentMode
		wantTargets            []string
		wantFit                bool
		wantNoFitReason        string
		wantTopologyAssignment *tas.TopologyAssignment
	}{
		"quota and topology fit: fits without preemption": {
			quota:                  "20",
			cpu:                    "2",
			initialMode:            flavorassigner.Fit,
			wantMode:               flavorassigner.Fit,
			wantFit:                true,
			wantTopologyAssignment: onNode,
		},
		"quota fits, topology does not: fits by preempting the lower-priority workload": {
			quota:                  "20",
			cpu:                    "3",
			initialMode:            flavorassigner.Fit,
			wantMode:               flavorassigner.Preempt,
			wantTargets:            []string{victimName},
			wantFit:                true,
			wantTopologyAssignment: onNode,
		},
		"quota fits, topology does not and preemption cannot free enough: reserves topology assuming an empty cluster": {
			quota:                  "20",
			cpu:                    "5",
			initialMode:            flavorassigner.Fit,
			wantMode:               flavorassigner.Preempt,
			wantFit:                false,
			wantTopologyAssignment: onNode,
		},
		"quota fits, topology does not fit even in an empty cluster: does not fit": {
			quota:           "20",
			cpu:             "7",
			initialMode:     flavorassigner.Fit,
			wantMode:        flavorassigner.NoFit,
			wantFit:         false,
			wantNoFitReason: kueue.WorkloadQuotaReservedReasonTopologyPlacementFailed,
		},
		"quota requires preemption, topology does not fit even in an empty cluster: does not fit": {
			quota:           "8",
			cpu:             "7",
			initialMode:     flavorassigner.Preempt,
			wantMode:        flavorassigner.NoFit,
			wantFit:         false,
			wantNoFitReason: kueue.WorkloadQuotaReservedReasonTopologyPlacementFailed,
		},
		"quota requires preemption: fits by preempting the lower-priority workload": {
			quota:                  "4",
			cpu:                    "2",
			initialMode:            flavorassigner.Preempt,
			wantMode:               flavorassigner.Preempt,
			wantTargets:            []string{victimName},
			wantFit:                true,
			wantTopologyAssignment: onNode,
		},
		"quota requires preemption, only a higher-priority workload could free it: reserves topology assuming an empty cluster": {
			quota:                  "4",
			cpu:                    "4",
			initialMode:            flavorassigner.Preempt,
			wantMode:               flavorassigner.Preempt,
			wantFit:                false,
			wantTopologyAssignment: onNode,
		},
		"request exceeds quota: does not fit": {
			quota:           "4",
			cpu:             "5",
			initialMode:     flavorassigner.NoFit,
			wantMode:        flavorassigner.NoFit,
			wantFit:         false,
			wantNoFitReason: kueue.WorkloadQuotaReservedReasonExceedsMaxQuota,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			// Given
			features.SetFeatureGatesDuringTest(t, map[featuregate.Feature]bool{
				features.TopologyAwareScheduling:          true,
				features.UnadmittedWorkloadsObservability: true,
			})

			log := testr.New(t)
			ctx := ctrllog.IntoContext(t.Context(), log)
			snapshot := newTASTestSnapshot(ctx, t, log, tc.quota, tc.evictableBlocker)

			wl := workload.NewInfo(log, utiltestingapi.MakeWorkload("incoming", "default").
				Priority(incomingPriority).
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					RequiredTopologyRequest(corev1.LabelHostname).
					Request(corev1.ResourceCPU, tc.cpu).
					Obj()).
				Obj())
			wl.ClusterQueue = tasTestCQ

			preemptor := preemption.New(
				utiltesting.NewFakeClient(), workload.Ordering{}, &utiltesting.EventRecorder{}, nil, false,
				clocktesting.NewFakeClock(time.Now()), nil, preemptexpectations.New(), nil,
			)
			assigner := flavorassigner.New(
				wl, snapshot.ClusterQueue(tasTestCQ), snapshot.ResourceFlavors, false,
				preemption.NewOracle(preemptor, snapshot), nil,
				configapi.QuotaCheckBlockUndeclared, resources.NewResourceFormatter(), 0,
			)
			initialAssignment := assigner.AssignFlavors(ctx, log, nil)
			if initialMode := initialAssignment.RepresentativeMode(); initialMode != tc.initialMode {
				t.Fatalf("AssignFlavors() misconfigured - incorrect initial mode: want %v, got %v", tc.initialMode, initialMode)
			}
			freeCapacityBefore := freeCapacity(t, snapshot)

			// When
			planner := NewPlanner(wl, snapshot, preemptor, assigner)
			gotPlan := planner.Plan(ctx, &initialAssignment)

			// Then
			if gotPlan.Assignment != &initialAssignment {
				t.Errorf("Plan() returned a different assignment object than the initial one")
			}
			if gotMode := gotPlan.Assignment.RepresentativeMode(); gotMode != tc.wantMode {
				t.Errorf("Plan() assignment mode mismatch: want %v, got %v", tc.wantMode, gotMode)
			}
			if diff := cmp.Diff(tc.wantTargets, targetNames(gotPlan.PreemptionTargets)); diff != "" {
				t.Errorf("Plan() preemption targets mismatch (-want +got):\n%s", diff)
			}
			if tc.wantFit != gotPlan.CanFit() {
				t.Errorf("Plan() returned unexpected verdict - want fit: %v, got: %v", tc.wantFit, gotPlan.CanFit())
			}
			if gotPlan.Assignment.NoFitReason != tc.wantNoFitReason {
				t.Errorf("Plan() assignment NoFitReason mismatch: want %q, got %q", tc.wantNoFitReason, gotPlan.Assignment.NoFitReason)
			}
			if diff := cmp.Diff(tc.wantTopologyAssignment, gotPlan.Assignment.PodSets[0].TopologyAssignment); diff != "" {
				t.Errorf("Plan() topology assignment mismatch (-want +got):\n%s", diff)
			}
			if freeCapacityAfter := freeCapacity(t, snapshot); freeCapacityAfter != freeCapacityBefore {
				t.Errorf("Plan() did not restore the snapshot TAS free capacity: before %s, after %s", freeCapacityBefore, freeCapacityAfter)
			}
		})
	}
}

func TestPlanWithResidualTASPod(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.TopologyAwareScheduling, true)
	cases := map[string]struct {
		podWorkload, podOwnerUID, wlOwnerUID string
		deleting                             bool
		wantMode                             flavorassigner.FlavorAssignmentMode
		wantWaiting                          bool
	}{
		"own bound Pod":             {podWorkload: "incoming", podOwnerUID: "job-uid", wlOwnerUID: "job-uid", wantMode: flavorassigner.DeferredFit, wantWaiting: true},
		"deleting unrelated Pod":    {podWorkload: "old", deleting: true, wantMode: flavorassigner.DeferredFit, wantWaiting: true},
		"same name different owner": {podWorkload: "incoming", podOwnerUID: "old-job", wlOwnerUID: "new-job", wantMode: flavorassigner.Preempt},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			log := testr.New(t)
			ctx := ctrllog.IntoContext(t.Context(), log)
			cache := schdcache.New(utiltesting.NewFakeClient())
			cache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor(tasTestFlavor).
				NodeLabel("tas", "true").TopologyName("topology").Obj())
			cache.AddOrUpdateTopology(log, utiltestingapi.MakeTopology("topology").Levels(corev1.LabelHostname).Obj())
			cache.TASCache().SyncNode(testingnode.MakeNode(tasTestNode).
				Label("tas", "true").Label(corev1.LabelHostname, tasTestNode).
				StatusAllocatable(corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse("6"), corev1.ResourcePods: resource.MustParse("32"),
				}).Ready().Obj())
			if err := cache.AddClusterQueue(ctx, utiltestingapi.MakeClusterQueue(tasTestCQ).
				Preemption(kueue.ClusterQueuePreemption{WithinClusterQueue: kueue.PreemptionPolicyLowerPriority}).
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas(tasTestFlavor).Resource(corev1.ResourceCPU, "20").Obj()).Obj()); err != nil {
				t.Fatalf("adding ClusterQueue: %v", err)
			}
			mid := utiltestingapi.MakeWorkload("mid", "default").Priority(lowPriority).
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					RequiredTopologyRequest(corev1.LabelHostname).Request(corev1.ResourceCPU, "2").Obj()).
				ReserveQuotaAt(utiltestingapi.MakeAdmission(tasTestCQ).
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, tasTestFlavor, "2").
						TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
							Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{tasTestNode}, 1).Obj()).Obj()).Obj()).Obj(), time.Now()).Obj()
			if !cache.AddOrUpdateWorkload(ctx, log, mid) {
				t.Fatal("adding mid workload")
			}
			pod := &corev1.Pod{
				Namespace: "default", Name: "bound-pod", UID: types.UID("bound-pod-uid"),
				Annotations: map[string]string{kueue.WorkloadAnnotation: tc.podWorkload}, Spec: corev1.PodSpec{NodeName: tasTestNode, Containers: []corev1.Container{{
					Name: "main", Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("3")}},
				}}}}
			if tc.podOwnerUID != "" {
				pod.OwnerReferences = []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", UID: types.UID(tc.podOwnerUID), Controller: new(true)}}
			}
			if tc.deleting {
				now := metav1.Now()
				pod.DeletionTimestamp = &now
			}
			cache.TASCache().UpdateTASPodUsage(pod, log)
			snapshot, err := cache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("snapshot: %v", err)
			}
			incomingObj := utiltestingapi.MakeWorkload("incoming", "default").Priority(incomingPriority).
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					RequiredTopologyRequest(corev1.LabelHostname).Request(corev1.ResourceCPU, "3").Obj()).Obj()
			if tc.wlOwnerUID != "" {
				incomingObj.OwnerReferences = []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", UID: types.UID(tc.wlOwnerUID)}}
			}
			incoming := workload.NewInfo(log, incomingObj)
			incoming.ClusterQueue = tasTestCQ
			preemptor := preemption.New(utiltesting.NewFakeClient(), workload.Ordering{}, &utiltesting.EventRecorder{}, nil, false,
				clocktesting.NewFakeClock(time.Now()), nil, preemptexpectations.New(), nil)
			assigner := flavorassigner.New(incoming, snapshot.ClusterQueue(tasTestCQ), snapshot.ResourceFlavors, false,
				preemption.NewOracle(preemptor, snapshot), nil, configapi.QuotaCheckBlockUndeclared, resources.NewResourceFormatter(), 0)
			asgn := assigner.AssignFlavors(ctx, log, nil)
			if got := asgn.RepresentativeMode(); got != flavorassigner.Fit {
				t.Fatalf("initial quota mode=%v, want Fit", got)
			}
			before := freeCapacity(t, snapshot)
			plan := NewPlanner(incoming, snapshot, preemptor, assigner).Plan(ctx, &asgn)
			if got := plan.Assignment.RepresentativeMode(); got != tc.wantMode {
				t.Fatalf("plan mode=%v, want %v", got, tc.wantMode)
			}
			if asgn.WaitingForResidualTASPods != tc.wantWaiting {
				t.Fatalf("waiting for residual Pods=%v, want %v", asgn.WaitingForResidualTASPods, tc.wantWaiting)
			}
			if tc.wantWaiting && len(plan.PreemptionTargets) != 0 {
				t.Fatalf("deferred fit preempted another workload: %v", targetNames(plan.PreemptionTargets))
			}
			if after := freeCapacity(t, snapshot); after != before {
				t.Fatalf("probe changed TAS free capacity: before %s, after %s", before, after)
			}
		})
	}
}
