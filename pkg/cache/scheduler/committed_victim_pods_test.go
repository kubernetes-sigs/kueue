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
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/cache/scheduler/simulator"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
)

// podPreemptingSimulator records the Pods removed from it, and fails to remove
// the Pods listed in failFor.
type podPreemptingSimulator struct {
	simulator.SchedulerSimulator
	preempted []simulator.PodRef
	failFor   map[client.ObjectKey]bool
}

func (s *podPreemptingSimulator) PreemptPods(_ context.Context, pods []simulator.PodRef) (func() error, error) {
	for _, pod := range pods {
		if s.failFor[pod.Key] {
			return nil, errors.New("pod not found in the simulator")
		}
	}
	s.preempted = append(s.preempted, pods...)
	return func() error { return nil }, nil
}

func TestAddTerminatingPodsAsCommittedVictims(t *testing.T) {
	now := metav1.NewTime(time.Now().Truncate(time.Second))
	// pod makes a TAS Pod of the Workload bound to node-a, using 1 CPU.
	pod := func(name, wlName string, deleting bool) *corev1.Pod {
		p := testingpod.MakePod(name, "ns").UID(name+"-uid").NodeName("node-a").
			Request(corev1.ResourceCPU, "1").Annotation(kueue.WorkloadAnnotation, wlName).Obj()
		if deleting {
			p.DeletionTimestamp = &now
		}
		return p
	}
	ref := func(name string) simulator.PodRef {
		return simulator.PodRef{Key: client.ObjectKey{Namespace: "ns", Name: name}, UID: types.UID(name + "-uid")}
	}
	cases := map[string]struct {
		deepIntegration bool
		pods            []*corev1.Pod
		failFor         []string
		wantPods        []simulator.PodRef
		// wantReleasedCPU is the CPU released on node-a by removing
		// CommittedVictimPodsUsage.
		wantReleasedCPU int64
	}{
		"Pods being deleted while no Workload holding quota covers them": {
			deepIntegration: true,
			pods: []*corev1.Pod{
				pod("terminating-1", "released", true),
				pod("terminating-2", "released", true),
				pod("running", "released", false),
			},
			wantPods:        []simulator.PodRef{ref("terminating-1"), ref("terminating-2")},
			wantReleasedCPU: 2,
		},
		"Pods being deleted of a Workload holding quota": {
			deepIntegration: true,
			pods:            []*corev1.Pod{pod("terminating", "held", true)},
		},
		"Pods which cannot be removed from the simulator": {
			deepIntegration: true,
			pods: []*corev1.Pod{
				pod("terminating", "released", true),
				pod("unremovable", "released", true),
			},
			failFor:         []string{"unremovable"},
			wantPods:        []simulator.PodRef{ref("terminating")},
			wantReleasedCPU: 1,
		},
		"no committed victims without SchedulerLibraryDeepIntegration": {
			pods: []*corev1.Pod{pod("terminating", "released", true)},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.TopologyAwareScheduling, true)
			features.SetFeatureGateDuringTest(t, features.SchedulerLibraryDeepIntegration, tc.deepIntegration)
			ctx, log := utiltesting.ContextWithLog(t)
			cache := New(utiltesting.NewFakeClient())
			cache.AddOrUpdateTopology(log, utiltestingapi.MakeDefaultOneLevelTopology("topology"))
			cache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("flavor").TopologyName("topology").Obj())
			cache.TASCache().SyncNode(testingnode.MakeNode("node-a").Label(corev1.LabelHostname, "node-a").
				StatusAllocatable(corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("8"), corev1.ResourcePods: resource.MustParse("10")}).Ready().Obj())
			if err := cache.AddClusterQueue(ctx, utiltestingapi.MakeClusterQueue("cq").ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("flavor").Resource(corev1.ResourceCPU, "8").Obj()).Obj()); err != nil {
				t.Fatalf("adding ClusterQueue: %v", err)
			}
			held := utiltestingapi.MakeWorkload("held", "ns").
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
					Assignment(corev1.ResourceCPU, "flavor", "1").
					TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
						Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"node-a"}, 1).Obj()).Obj()).Obj()).Obj(), now.Time).
				Obj()
			if !cache.AddOrUpdateWorkload(ctx, log, held) {
				t.Fatal("adding the Workload holding quota")
			}
			for _, p := range tc.pods {
				cache.TASCache().UpdateTASPodUsage(p, log)
			}
			snapshot, err := cache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("snapshot: %v", err)
			}
			sim := &podPreemptingSimulator{failFor: map[client.ObjectKey]bool{}}
			for _, name := range tc.failFor {
				sim.failFor[client.ObjectKey{Namespace: "ns", Name: name}] = true
			}
			snapshot.SchedulerSimulator = sim

			snapshot.AddTerminatingPodsAsCommittedVictims(ctx)

			sortRefs := cmpopts.SortSlices(func(a, b simulator.PodRef) bool { return a.Key.Name < b.Key.Name })
			if diff := cmp.Diff(tc.wantPods, snapshot.committedVictims.Pods, sortRefs, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Unexpected committed victim Pods (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantPods, sim.preempted, sortRefs, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Unexpected Pods removed from the simulator (-want,+got):\n%s", diff)
			}
			if got, want := snapshot.HasCommittedVictims(), len(tc.wantPods) > 0; got != want {
				t.Errorf("HasCommittedVictims() = %v, want %v", got, want)
			}
			opts := simulator.BuildScheduleOptions(snapshot.CommittedVictimsScheduleOptions()...)
			if diff := cmp.Diff(tc.wantPods, opts.CommittedPreemptionPods, sortRefs, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Unexpected committed Pods passed to placement simulations (-want,+got):\n%s", diff)
			}

			freeCPU := func() int64 {
				t.Helper()
				serialized, err := snapshot.ClusterQueue("cq").TASFlavors["flavor"].SerializeFreeCapacityPerDomain()
				if err != nil {
					t.Fatalf("serializing capacity: %v", err)
				}
				var capacity map[string]struct {
					FreeCapacity map[string]string `json:"freeCapacity"`
				}
				if err := json.Unmarshal([]byte(serialized), &capacity); err != nil {
					t.Fatalf("decoding capacity: %v", err)
				}
				q := resource.MustParse(capacity["node-a"].FreeCapacity["cpu"])
				return q.Value()
			}
			before := freeCPU()
			revert := snapshot.SimulatePodUsageRemoval(snapshot.CommittedVictims().PodsUsage)
			if got := freeCPU() - before; got != tc.wantReleasedCPU {
				t.Errorf("SimulatePodUsageRemoval() released %d CPU, want %d", got, tc.wantReleasedCPU)
			}
			revert()
			if got := freeCPU(); got != before {
				t.Errorf("Free CPU after reverting = %d, want %d", got, before)
			}
		})
	}
}
