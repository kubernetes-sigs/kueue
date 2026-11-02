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
	"slices"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	clocktesting "k8s.io/utils/clock/testing"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/resources"
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

const (
	residualTestCQ           = "tas-cq"
	residualTestFlavor       = "tas-flavor"
	residualTestNode         = "node-1"
	residualIncomingPriority = 50
	residualLowPriority      = 0
)

func residualFreeCapacity(t *testing.T, snapshot *schdcache.Snapshot) string {
	t.Helper()
	got, err := snapshot.ClusterQueue(residualTestCQ).TASFlavors[residualTestFlavor].SerializeFreeCapacityPerDomain()
	if err != nil {
		t.Fatalf("serializing TAS free capacity: %v", err)
	}
	return got
}

func residualTargetNames(targets []*preemption.Target) []string {
	var names []string
	for _, target := range targets {
		names = append(names, target.WorkloadInfo.Obj.Name)
	}
	slices.Sort(names)
	return names
}

func TestElasticSliceWithResidualTASPods(t *testing.T) {
	runElasticSliceWithResidualTASPods(t)
}

func runElasticSliceWithResidualTASPods(t *testing.T, cacheOptions ...schdcache.Option) {
	t.Helper()
	features.SetFeatureGateDuringTest(t, features.TASResidualPodAccounting, true)
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
			cache := schdcache.New(utiltesting.NewFakeClient(), cacheOptions...)
			cache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor(residualTestFlavor).
				NodeLabel("tas", "true").TopologyName("topology").Obj())
			cache.AddOrUpdateTopology(log, utiltestingapi.MakeTopology("topology").Levels(corev1.LabelHostname).Obj())
			nodeCPU := "4"
			newCount := 4
			if tc.remainingVictim {
				nodeCPU = "6"
				newCount = 6
			}
			cache.TASCache().SyncNode(testingnode.MakeNode(residualTestNode).
				Label("tas", "true").Label(corev1.LabelHostname, residualTestNode).
				StatusAllocatable(corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse(nodeCPU), corev1.ResourcePods: resource.MustParse("32"),
				}).Ready().Obj())
			if err := cache.AddClusterQueue(ctx, utiltestingapi.MakeClusterQueue(residualTestCQ).
				Preemption(kueue.ClusterQueuePreemption{WithinClusterQueue: kueue.PreemptionPolicyLowerPriority}).
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas(residualTestFlavor).Resource(corev1.ResourceCPU, "10").Obj()).Obj()); err != nil {
				t.Fatalf("adding ClusterQueue: %v", err)
			}
			old := utiltestingapi.MakeWorkload("old", "default").UID("old-uid").Priority(residualIncomingPriority).
				Annotation("kueue.x-k8s.io/elastic-job", "true").
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).
					Request(corev1.ResourceCPU, "1").UnconstrainedTopologyRequest().Obj()).
				ReserveQuotaAt(utiltestingapi.MakeAdmission(residualTestCQ).PodSets(
					utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Count(2).
						Assignment(corev1.ResourceCPU, residualTestFlavor, "2").
						TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
							Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{residualTestNode}, 2).Obj()).Obj()).Obj()).Obj(), time.Now()).
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
				for name, node := range map[string]string{"victim": residualTestNode, "other": "node-2"} {
					count := 1
					cpu := "1"
					if name == "other" {
						cpu = "4"
					}
					victim := utiltestingapi.MakeWorkload(name, "default").Priority(residualLowPriority).
						PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, count).
							Request(corev1.ResourceCPU, cpu).UnconstrainedTopologyRequest().Obj()).
						ReserveQuotaAt(utiltestingapi.MakeAdmission(residualTestCQ).PodSets(
							utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Count(int32(count)).
								Assignment(corev1.ResourceCPU, residualTestFlavor, cpu).
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
				pod := testingpod.MakePod("residual", "default").UID("residual-uid").
					NodeName(residualTestNode).Request(corev1.ResourceCPU, "1").
					Annotation(kueue.WorkloadAnnotation, "released").Obj()
				pod.DeletionTimestamp = new(metav1.Now())
				cache.TASCache().UpdateTASPodUsage(pod, log)
			}
			snapshot, err := cache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("snapshot: %v", err)
			}
			cq := snapshot.ClusterQueue(residualTestCQ)
			predecessor := cq.Workloads[workload.Key(old)]
			if tc.removedPredecessor {
				snapshot.RemoveWorkload(predecessor)
			}
			incoming := workload.NewInfo(log, utiltestingapi.MakeWorkload("new", "default").Priority(residualIncomingPriority).
				Annotation("kueue.x-k8s.io/elastic-job", "true").
				Annotation(workloadslicing.WorkloadSliceReplacementFor, string(workload.Key(old))).
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, newCount).
					Request(corev1.ResourceCPU, "1").UnconstrainedTopologyRequest().Obj()).Obj())
			incoming.ClusterQueue = residualTestCQ
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
			before := residualFreeCapacity(t, snapshot)
			usageBefore := cq.ResourceNode.Usage.Clone()
			asgn, targets := New(nil, cache, utiltesting.NewFakeClient(), &utiltesting.EventRecorder{}).getAssignments(ctx, incoming, snapshot)
			if got := asgn.RepresentativeMode(); got != tc.wantMode {
				t.Errorf("plan mode=%v, want %v", got, tc.wantMode)
			}
			if asgn.WaitingForResidualTASPods != tc.wantWaiting {
				t.Errorf("waiting=%v, want %v", asgn.WaitingForResidualTASPods, tc.wantWaiting)
			}
			wantTargets := []string(nil)
			if tc.wantMode == flavorassigner.Fit && !tc.removedPredecessor {
				wantTargets = []string{"old"}
			}
			if diff := cmp.Diff(wantTargets, residualTargetNames(targets)); diff != "" {
				t.Errorf("replacement targets (-want,+got):\n%s", diff)
			}
			if after := residualFreeCapacity(t, snapshot); before != after {
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

func TestAssignmentWithResidualTASPod(t *testing.T) {
	runAssignmentWithResidualTASPod(t)
}

func runAssignmentWithResidualTASPod(t *testing.T, cacheOptions ...schdcache.Option) {
	t.Helper()
	features.SetFeatureGateDuringTest(t, features.TASResidualPodAccounting, true)
	features.SetFeatureGateDuringTest(t, features.TopologyAwareScheduling, true)
	cases := map[string]struct {
		podWorkload, podOwnerUID, wlOwnerUID string
		deleting                             bool
		disableResidual                      bool
		wantMode                             flavorassigner.FlavorAssignmentMode
		wantWaiting                          bool
		nodeSelector                         map[string]string
	}{
		"own bound Pod": {podWorkload: "incoming", podOwnerUID: "job-uid", wlOwnerUID: "job-uid", wantMode: flavorassigner.DeferredFit, wantWaiting: true},
		"disabled accounting keeps reservation-only placement": {podWorkload: "incoming", podOwnerUID: "job-uid", wlOwnerUID: "job-uid", disableResidual: true, wantMode: flavorassigner.Fit},
		"deleting unrelated Pod":                               {podWorkload: "old", deleting: true, wantMode: flavorassigner.DeferredFit, wantWaiting: true},
		"same name different owner":                            {podWorkload: "incoming", podOwnerUID: "old-job", wlOwnerUID: "new-job", wantMode: flavorassigner.Preempt},
		"node selector mismatch is not rescued by Pod release": {podWorkload: "old", deleting: true, nodeSelector: map[string]string{"unmatched": "true"}, wantMode: flavorassigner.NoFit},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if tc.disableResidual {
				features.SetFeatureGateDuringTest(t, features.TASResidualPodAccounting, false)
			}
			ctx, log := utiltesting.ContextWithLog(t)
			cache := schdcache.New(utiltesting.NewFakeClient(), cacheOptions...)
			cache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor(residualTestFlavor).
				NodeLabel("tas", "true").TopologyName("topology").Obj())
			cache.AddOrUpdateTopology(log, utiltestingapi.MakeTopology("topology").Levels(corev1.LabelHostname).Obj())
			cache.TASCache().SyncNode(testingnode.MakeNode(residualTestNode).
				Label("tas", "true").Label(corev1.LabelHostname, residualTestNode).
				StatusAllocatable(corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse("6"), corev1.ResourcePods: resource.MustParse("32"),
				}).Ready().Obj())
			if err := cache.AddClusterQueue(ctx, utiltestingapi.MakeClusterQueue(residualTestCQ).
				Preemption(kueue.ClusterQueuePreemption{WithinClusterQueue: kueue.PreemptionPolicyLowerPriority}).
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas(residualTestFlavor).Resource(corev1.ResourceCPU, "20").Obj()).Obj()); err != nil {
				t.Fatalf("adding ClusterQueue: %v", err)
			}
			mid := utiltestingapi.MakeWorkload("mid", "default").Priority(residualLowPriority).
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					RequiredTopologyRequest(corev1.LabelHostname).Request(corev1.ResourceCPU, "2").Obj()).
				ReserveQuotaAt(utiltestingapi.MakeAdmission(residualTestCQ).
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, residualTestFlavor, "2").
						TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
							Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{residualTestNode}, 1).Obj()).Obj()).Obj()).Obj(), time.Now()).Obj()
			if !cache.AddOrUpdateWorkload(ctx, log, mid) {
				t.Fatal("adding mid workload")
			}
			pod := testingpod.MakePod("bound-pod", "default").UID("bound-pod-uid").
				NodeName(residualTestNode).Request(corev1.ResourceCPU, "3").
				Annotation(kueue.WorkloadAnnotation, tc.podWorkload).Obj()
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
			incomingObj := utiltestingapi.MakeWorkload("incoming", "default").Priority(residualIncomingPriority).
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					RequiredTopologyRequest(corev1.LabelHostname).Request(corev1.ResourceCPU, "3").Obj()).Obj()
			incomingObj.Spec.PodSets[0].Template.Spec.NodeSelector = tc.nodeSelector
			if tc.wlOwnerUID != "" {
				incomingObj.OwnerReferences = []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", UID: types.UID(tc.wlOwnerUID)}}
			}
			incoming := workload.NewInfo(log, incomingObj)
			incoming.ClusterQueue = residualTestCQ
			preemptor := preemption.New(utiltesting.NewFakeClient(), workload.Ordering{}, &utiltesting.EventRecorder{}, nil, false,
				clocktesting.NewFakeClock(time.Now()), nil, preemptexpectations.New(), nil)
			assigner := flavorassigner.New(incoming, snapshot.ClusterQueue(residualTestCQ), snapshot.ResourceFlavors, false,
				preemption.NewOracle(preemptor, snapshot), nil, configapi.QuotaCheckBlockUndeclared, resources.NewResourceFormatter(), 0)
			asgn := assigner.AssignFlavors(ctx, log, nil)
			if got := asgn.RepresentativeMode(); got != flavorassigner.Fit {
				t.Fatalf("initial quota mode=%v, want Fit", got)
			}
			before := residualFreeCapacity(t, snapshot)
			asgn, targets := New(nil, cache, utiltesting.NewFakeClient(), &utiltesting.EventRecorder{}).getAssignments(ctx, incoming, snapshot)
			if got := asgn.RepresentativeMode(); got != tc.wantMode {
				t.Fatalf("plan mode=%v, want %v", got, tc.wantMode)
			}
			if asgn.WaitingForResidualTASPods != tc.wantWaiting {
				t.Fatalf("waiting for residual Pods=%v, want %v", asgn.WaitingForResidualTASPods, tc.wantWaiting)
			}
			var wantTargets []string
			if tc.wantMode == flavorassigner.Preempt {
				wantTargets = []string{"mid"}
			}
			if diff := cmp.Diff(wantTargets, residualTargetNames(targets)); diff != "" {
				t.Errorf("preemption targets (-want,+got):\n%s", diff)
			}
			if after := residualFreeCapacity(t, snapshot); after != before {
				t.Fatalf("probe changed TAS free capacity: before %s, after %s", before, after)
			}
		})
	}
}
