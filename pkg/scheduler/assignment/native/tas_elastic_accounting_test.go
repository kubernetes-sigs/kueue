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
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	clocktesting "k8s.io/utils/clock/testing"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/constants"
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

func TestPlanElasticReplacementPreemption(t *testing.T) {
	cases := map[string]struct {
		quota           string
		wantInitialMode flavorassigner.FlavorAssignmentMode
	}{
		"topology needs preemption while quota fits": {quota: "20", wantInitialMode: flavorassigner.Fit},
		"quota and topology both need preemption":    {quota: "4", wantInitialMode: flavorassigner.Preempt},
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
			cache.TASCache().SyncNode(testingnode.MakeNode(tasTestNode).
				Label("tas", "true").Label(corev1.LabelHostname, tasTestNode).
				StatusAllocatable(corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse("4"), corev1.ResourcePods: resource.MustParse("32"),
				}).Ready().Obj())
			if err := cache.AddClusterQueue(ctx, utiltestingapi.MakeClusterQueue(tasTestCQ).
				Preemption(kueue.ClusterQueuePreemption{WithinClusterQueue: kueue.PreemptionPolicyLowerPriority}).
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas(tasTestFlavor).Resource(corev1.ResourceCPU, tc.quota).Obj()).Obj()); err != nil {
				t.Fatalf("adding ClusterQueue: %v", err)
			}
			for name, priority := range map[string]int32{"old": incomingPriority, victimName: lowPriority} {
				wl := utiltestingapi.MakeWorkload(name, "default").UID(types.UID(name+"-uid")).Priority(priority).
					PodSets(*utiltestingapi.MakePodSet("workers", 2).
						Request(corev1.ResourceCPU, "1").UnconstrainedTopologyRequest().Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission(tasTestCQ).PodSets(
						utiltestingapi.MakePodSetAssignment("workers").Count(2).
							Assignment(corev1.ResourceCPU, tasTestFlavor, "2").
							TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
								Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{tasTestNode}, 2).Obj()).Obj()).Obj()).Obj(), time.Now()).
					AdmittedAt(true, time.Now())
				if name == "old" {
					wl.Annotation(constants.ElasticJobAnnotation, "true")
				}
				if !cache.AddOrUpdateWorkload(ctx, log, wl.Obj()) {
					t.Fatalf("adding workload %s", name)
				}
			}
			snapshot, err := cache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("snapshot: %v", err)
			}
			cq := snapshot.ClusterQueue(tasTestCQ)
			old := cq.Workloads["default/old"]
			incoming := workload.NewInfo(log, utiltestingapi.MakeWorkload("new", "default").Priority(incomingPriority).
				Annotation(constants.ElasticJobAnnotation, "true").
				Annotation(workloadslicing.WorkloadSliceReplacementFor, string(workload.Key(old.Obj))).
				PodSets(*utiltestingapi.MakePodSet("workers", 4).
					Request(corev1.ResourceCPU, "1").UnconstrainedTopologyRequest().Obj()).Obj())
			incoming.ClusterQueue = tasTestCQ
			preemptor := preemption.New(utiltesting.NewFakeClient(), workload.Ordering{}, &utiltesting.EventRecorder{}, nil, false,
				clocktesting.NewFakeClock(time.Now()), nil, preemptexpectations.New(), nil)
			assigner := flavorassigner.New(incoming, cq, snapshot.ResourceFlavors, false,
				preemption.NewOracle(preemptor, snapshot), old,
				configapi.QuotaCheckBlockUndeclared, resources.NewResourceFormatter(), 0)
			asgn := assigner.AssignFlavors(ctx, log, nil)
			if got := asgn.RepresentativeMode(); got != tc.wantInitialMode {
				t.Fatalf("initial quota mode=%v, want %v", got, tc.wantInitialMode)
			}
			capacityBefore := freeCapacity(t, snapshot)
			quotaBefore := cq.ResourceNode.Usage.Clone()
			plan := NewPlanner(incoming, snapshot, preemptor, assigner).Plan(ctx, &asgn)
			if !plan.CanFit() || asgn.RepresentativeMode() != flavorassigner.Preempt {
				t.Errorf("replacement should fit after preemption: mode=%v, canFit=%v", asgn.RepresentativeMode(), plan.CanFit())
			}
			if diff := cmp.Diff([]string{victimName}, targetNames(plan.PreemptionTargets)); diff != "" {
				t.Errorf("preemption targets (-want +got):\n%s", diff)
			}
			if got := asgn.PodSets[0].TopologyAssignment; got == nil || tas.CountPodsInAssignment(got) != 4 {
				t.Errorf("replacement should have a topology assignment for all four workers, got %v", got)
			}
			if after := freeCapacity(t, snapshot); after != capacityBefore {
				t.Errorf("planning changed TAS capacity: before=%s after=%s", capacityBefore, after)
			}
			if diff := cmp.Diff(quotaBefore, cq.ResourceNode.Usage); diff != "" {
				t.Errorf("planning changed quota usage (-want +got):\n%s", diff)
			}
			if !cq.WorkloadUsageIsAccounted(old) || len(cq.Workloads) != 2 {
				t.Error("planning did not restore the predecessor and victim")
			}
		})
	}
}
