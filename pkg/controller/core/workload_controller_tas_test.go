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

package core

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	testingclock "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	coreindexer "sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/scheduler/assignment/native"
	"sigs.k8s.io/kueue/pkg/scheduler/flavorassigner"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestWorkloadEventPreservesResidualTASReleaseIntent(t *testing.T) {
	cases := map[string]struct {
		create       bool
		active       bool
		evictedFalse bool
		wantMode     flavorassigner.FlavorAssignmentMode
	}{
		"deactivation before eviction condition exists": {wantMode: flavorassigner.DeferredFit},
		"deactivation before eviction condition changes": {
			evictedFalse: true, wantMode: flavorassigner.DeferredFit,
		},
		"inactive informer startup": {create: true, wantMode: flavorassigner.DeferredFit},
		"active quota release without eviction still permits necessary preemption": {
			active: true, wantMode: flavorassigner.Preempt,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.TopologyAwareScheduling, true)
			ctx, log := utiltesting.ContextWithLog(t)
			clientBuilder := utiltesting.NewClientBuilder()
			if err := utiltesting.AsIndexer(clientBuilder).IndexField(ctx, &corev1.LimitRange{},
				coreindexer.LimitRangeHasContainerOrPodType, coreindexer.IndexLimitRangeHasContainerOrPodType); err != nil {
				t.Fatalf("setting up client indexes: %v", err)
			}
			cl := clientBuilder.Build()
			cache := schdcache.New(cl)
			queues := qcache.NewManagerForUnitTests(cl, cache,
				qcache.WithPreemptionExpectations(preemptexpectations.New()))
			r := NewWorkloadReconciler(cl, queues, cache, &utiltesting.EventRecorder{})
			cache.AddOrUpdateTopology(log, utiltestingapi.MakeDefaultOneLevelTopology("topology"))
			cache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("tas").
				NodeLabel("group", "tas").TopologyName("topology").Obj())
			cache.TASCache().SyncNode(testingnode.MakeNode("node").
				Label("group", "tas").Label(corev1.LabelHostname, "node").Ready().
				StatusAllocatable(corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse("6"), corev1.ResourcePods: resource.MustParse("32"),
				}).Obj())
			cq := utiltestingapi.MakeClusterQueue("cq").
				Preemption(kueue.ClusterQueuePreemption{WithinClusterQueue: kueue.PreemptionPolicyLowerPriority}).
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("tas").Resource(corev1.ResourceCPU, "20").Obj()).Obj()
			setupClusterQueue(ctx, t, cl, queues, cache, cq, false)
			if err := cache.AddClusterQueue(ctx, cq); err != nil {
				t.Fatalf("adding ClusterQueue: %v", err)
			}
			setupLocalQueue(ctx, t, cl, queues, utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj(), false)
			var old *kueue.Workload
			for name, cpu := range map[string]string{"old": "3", "healthy": "2"} {
				wl := utiltestingapi.MakeWorkload(name, "ns").UID(types.UID(name+"-uid")).Queue("lq").Priority(1).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
						RequiredTopologyRequest(corev1.LabelHostname).Request(corev1.ResourceCPU, cpu).Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").PodSets(
						utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "tas", cpu).
							TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
								Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"node"}, 1).Obj()).Obj()).Obj()).Obj(), time.Now()).Obj()
				if !cache.AddOrUpdateWorkload(ctx, log, wl) {
					t.Fatalf("adding %s reservation", name)
				}
				if name == "old" {
					old = wl
				}
			}
			pod := testingpod.MakePod("old-pod", "ns").UID("old-pod-uid").NodeName("node").
				Annotation(kueue.WorkloadAnnotation, old.Name).Request(corev1.ResourceCPU, "3").Obj()
			cache.TASCache().UpdateTASPodUsage(pod, log)
			updated := old.DeepCopy()
			updated.Spec.Active = new(tc.active)
			if tc.evictedFalse {
				updated.Status.Conditions = append(updated.Status.Conditions,
					metav1.Condition{Type: kueue.WorkloadEvicted, Status: metav1.ConditionFalse})
			}
			if tc.active {
				updated.Status.Admission = nil
				updated.Status.Conditions = nil
			}
			// Do not run Reconcile: the next scheduling cycle can precede its
			// asynchronous Evicted status update.
			if tc.create {
				r.handleCreate(ctx, event.TypedCreateEvent[*kueue.Workload]{Object: updated})
			} else {
				r.handleUpdate(ctx, event.TypedUpdateEvent[*kueue.Workload]{ObjectOld: old, ObjectNew: updated})
			}
			if cache.IsAdded(*workload.NewInfo(log, old)) {
				t.Fatal("event retained the old reservation")
			}
			incoming := workload.NewInfo(log, utiltestingapi.MakeWorkload("incoming", "ns").Priority(3).
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					RequiredTopologyRequest(corev1.LabelHostname).Request(corev1.ResourceCPU, "3").Obj()).Obj())
			incoming.ClusterQueue = "cq"
			for _, released := range []bool{false, true} {
				if released {
					cache.TASCache().DeleteTASPodUsageByKey(client.ObjectKeyFromObject(pod), log)
				}
				snapshot, err := cache.Snapshot(ctx)
				if err != nil {
					t.Fatalf("snapshot: %v", err)
				}
				cqSnapshot := snapshot.ClusterQueue("cq")
				flavor := cqSnapshot.TASFlavors["tas"]
				capacityBefore, err := flavor.SerializeFreeCapacityPerDomain()
				if err != nil {
					t.Fatalf("serializing TAS capacity: %v", err)
				}
				var domains map[string]struct {
					FreeCapacity corev1.ResourceList `json:"freeCapacity"`
					TASUsage     corev1.ResourceList `json:"tasUsage"`
				}
				if err := json.Unmarshal([]byte(capacityBefore), &domains); err != nil {
					t.Fatalf("decoding TAS capacity: %v", err)
				}
				wantCPU, wantPods := "3", "31"
				if released {
					wantCPU, wantPods = "6", "32"
				}
				capacity := domains["node"].FreeCapacity
				if capacity.Cpu().Cmp(resource.MustParse(wantCPU)) != 0 || capacity.Pods().Cmp(resource.MustParse(wantPods)) != 0 {
					t.Fatalf("released=%t: physical capacity=%s, want %s CPU and %s Pod slots", released, capacityBefore, wantCPU, wantPods)
				}
				if usage := domains["node"].TASUsage; usage.Cpu().Cmp(resource.MustParse("2")) != 0 || usage.Pods().Cmp(resource.MustParse("1")) != 0 {
					t.Fatalf("healthy reservation changed: %s", capacityBefore)
				}
				usageBefore := cqSnapshot.ResourceNode.Usage.Clone()
				preemptor := preemption.New(cl, workload.Ordering{}, &utiltesting.EventRecorder{}, nil, false,
					testingclock.NewFakeClock(time.Now()), nil, preemptexpectations.New(), nil)
				assigner := flavorassigner.New(incoming, cqSnapshot, snapshot.ResourceFlavors, false,
					preemption.NewOracle(preemptor, snapshot), nil, configapi.QuotaCheckBlockUndeclared, resources.NewResourceFormatter(), 0)
				asgn := assigner.AssignFlavors(ctx, log, nil)
				if got := asgn.RepresentativeMode(); got != flavorassigner.Fit {
					t.Fatalf("initial quota mode=%v, want Fit", got)
				}
				plan := native.NewPlanner(incoming, snapshot, preemptor, assigner).Plan(ctx, &asgn)
				wantMode := tc.wantMode
				if released {
					wantMode = flavorassigner.Fit
				}
				if got := plan.Assignment.RepresentativeMode(); got != wantMode {
					t.Fatalf("released=%t: plan mode=%v, want %v; targets=%v", released, got, wantMode, plan.PreemptionTargets)
				}
				if got := asgn.WaitingForResidualTASPods; got != (wantMode == flavorassigner.DeferredFit) {
					t.Errorf("released=%t: waiting=%t, want %t", released, got, wantMode == flavorassigner.DeferredFit)
				}
				if got := len(plan.PreemptionTargets); got != 0 && wantMode != flavorassigner.Preempt {
					t.Errorf("released=%t: selected %d unnecessary victims", released, got)
				}
				if wantMode == flavorassigner.Preempt && len(plan.PreemptionTargets) != 1 {
					t.Error("active, non-evicted residual Pod should still permit necessary preemption")
				}
				if wantMode == flavorassigner.Preempt && len(plan.PreemptionTargets) == 1 && plan.PreemptionTargets[0].WorkloadInfo.Obj.Name != "healthy" {
					t.Error("legitimate preemption selected an unexpected Workload")
				}
				capacityAfter, err := flavor.SerializeFreeCapacityPerDomain()
				if err != nil || capacityBefore != capacityAfter {
					t.Errorf("release probe changed physical capacity: before=%s, after=%s, error=%v", capacityBefore, capacityAfter, err)
				}
				if diff := cmp.Diff(usageBefore, cqSnapshot.ResourceNode.Usage); diff != "" {
					t.Errorf("release probe changed quota usage (-want +got):\n%s", diff)
				}
			}
		})
	}
}
