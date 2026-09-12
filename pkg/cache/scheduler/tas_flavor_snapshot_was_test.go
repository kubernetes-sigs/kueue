//go:build !exclude_scheduler_library

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
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/cache/scheduler/simulator"
	"sigs.k8s.io/kueue/pkg/cache/scheduler/was"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/resources"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
)

const wasRackLabel = "cloud.provider.com/topology-rack"

// wasSnapshotWithVictim builds a TAS snapshot over one node whose single host port
// is taken by victimKey, using the scheduler-library simulator rather than a stand-in.
func wasSnapshotWithVictim(t *testing.T, victimKey client.ObjectKey) (*TASFlavorSnapshot, simulator.SimulatorSnapshot) {
	t.Helper()
	ctx, log := utiltesting.ContextWithLog(t)

	nodes := []*corev1.Node{
		node.MakeNode("n1").
			Label(corev1.LabelHostname, "n1").
			Label(wasRackLabel, "r1").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:  resource.MustParse("4"),
				corev1.ResourcePods: resource.MustParse("10"),
			}).Ready().Obj(),
	}
	sim, err := was.NewWASSimulator(ctx, nil)
	if err != nil {
		t.Fatalf("NewWASSimulator() error = %v", err)
	}
	sim.TrackPod(ctx, testingpod.MakePod("victim-pod", victimKey.Namespace).
		UID("victim-pod").
		Annotation(kueue.WorkloadAnnotation, victimKey.Name).
		NodeName("n1").
		StatusPhase(corev1.PodRunning).
		Port(8080, 8080, corev1.ProtocolTCP).
		Obj())
	simSnapshot, err := sim.Snapshot(ctx, nodes)
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	tree := newTopologyTree([]string{wasRackLabel, corev1.LabelHostname}, nodes, 0)
	return newTASFlavorSnapshot(log, "tas-topology", tree, nil, simSnapshot), simSnapshot
}

// wantsTheSamePort is a PodSet asking for the host port the victim holds.
func wantsTheSamePort() FlavorTASRequests {
	unconstrained := true
	return FlavorTASRequests{{
		PodSet: &kueue.PodSet{
			Name:            "main",
			TopologyRequest: &kueue.PodSetTopologyRequest{Unconstrained: &unconstrained},
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				Containers: []corev1.Container{{
					Name:  "c",
					Ports: []corev1.ContainerPort{{ContainerPort: 8080, HostPort: 8080, Protocol: corev1.ProtocolTCP}},
				}},
			}},
		},
		SinglePodRequests: resources.NewRequestsFromMap(map[corev1.ResourceName]int64{corev1.ResourceCPU: 1000}),
		Count:             1,
	}}
}

// The matching-leaves cache must not answer an empty-cluster question with the result
// of a normal one. TAS asks both in the same cycle for the same PodSet.
func TestMatchingLeavesCacheSeparatesSimulateEmpty(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.TASCacheNodeMatchResults, true)
	features.SetFeatureGateDuringTest(t, features.SchedulerLibraryIntegration, true)
	ctx, _ := utiltesting.ContextWithLog(t)
	snapshot, _ := wasSnapshotWithVictim(t, client.ObjectKey{Namespace: "default", Name: "victim"})
	requests := wantsTheSamePort()
	wl := &kueue.Workload{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "wl", UID: "wl-uid"}}

	if snapshot.FindTopologyAssignmentsForFlavor(ctx, requests, WithWorkload(wl)).Failure() == nil {
		t.Fatal("FindTopologyAssignmentsForFlavor() found a fit, want none while the victim holds the port")
	}
	if failure := snapshot.FindTopologyAssignmentsForFlavor(ctx, requests, WithWorkload(wl), WithSimulateEmpty(true)).Failure(); failure != nil {
		t.Errorf("FindTopologyAssignmentsForFlavor(simulateEmpty) = %v, want a fit once the port is assumed free", failure)
	}
}

// Releasing a Workload changes what the simulator reports, and so does putting it
// back, so the cached leaf sets cannot outlive either.
func TestMatchingLeavesCacheFollowsPreemption(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.TASCacheNodeMatchResults, true)
	features.SetFeatureGateDuringTest(t, features.SchedulerLibraryIntegration, true)
	ctx, _ := utiltesting.ContextWithLog(t)
	victim := client.ObjectKey{Namespace: "default", Name: "victim"}
	snapshot, simSnapshot := wasSnapshotWithVictim(t, victim)
	requests := wantsTheSamePort()
	wl := &kueue.Workload{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "wl", UID: "wl-uid"}}
	fits := func() bool {
		return snapshot.FindTopologyAssignmentsForFlavor(ctx, requests, WithWorkload(wl)).Failure() == nil
	}

	// The flavor assigner asks first, while the victim still holds the port.
	if fits() {
		t.Fatal("FindTopologyAssignmentsForFlavor() found a fit, want none while the victim holds the port")
	}
	revert, err := simSnapshot.PreemptWorkload(ctx, victim)
	if err != nil {
		t.Fatalf("PreemptWorkload() error = %v", err)
	}
	snapshot.forgetMatchingLeaves()
	// The scheduler asks again once the victim is released.
	if !fits() {
		t.Error("FindTopologyAssignmentsForFlavor() found no fit after the victim was released, want a fit")
	}
	if err := revert(); err != nil {
		t.Fatalf("revert() error = %v", err)
	}
	snapshot.forgetMatchingLeaves()
	// And the answer given while it was released must not outlive it.
	if fits() {
		t.Error("FindTopologyAssignmentsForFlavor() found a fit after the victim was restored, want none")
	}
}
