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

package dra

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/component-base/featuregate"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/cache/scheduler/podtracker"
	"sigs.k8s.io/kueue/pkg/cache/scheduler/simulator"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingdra "sigs.k8s.io/kueue/pkg/util/testingjobs/dra"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestCheckerDeviceState(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.KueueDRAIntegrationExtendedResource, true)
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = resourceapi.AddToScheme(scheme)

	gpuNode := &corev1.Node{Name: "gpu-node"}
	gpuDeviceClass := &resourceapi.DeviceClass{Name: "gpu.example.com"}
	gpuSlice := testingdra.MakeResourceSlice("gpu-node-slice", "gpu.example.com").
		NodeName("gpu-node").
		Pool("gpu-pool", 1, 1).
		Device("gpu-0").
		Device("gpu-1").
		Device("gpu-2").
		Device("gpu-3").
		Obj()
	gpuTemplate := utiltesting.MakeResourceClaimTemplate("gpu-template", "default").
		DeviceRequest("gpu", "gpu.example.com", 1).
		Obj()
	podSpec := corev1.PodSpec{
		Containers: []corev1.Container{{Name: "c", Image: "busybox"}},
		ResourceClaims: []corev1.PodResourceClaim{
			{Name: "gpu", ResourceClaimTemplateName: new("gpu-template")},
		},
	}
	podTemplate := &corev1.PodTemplateSpec{Namespace: "default", Spec: podSpec}

	// Admitted onto gpu-node with two Pods that each take one GPU.
	admitted := utiltestingapi.MakeWorkload("admitted", "default").
		PodSets(*utiltestingapi.MakePodSet("main", 2).PodSpec(podSpec).Obj()).
		ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").PodSets(
			utiltestingapi.MakePodSetAssignment("main").Count(2).
				TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
					Domain(utiltas.TopologyDomainAssignment{Values: []string{"gpu-node"}, Count: 2}).
					Obj()).
				Obj(),
		).Obj(), time.Now()).
		Obj()
	admittedOnePod := utiltestingapi.MakeWorkload("one-pod", "default").
		PodSets(*utiltestingapi.MakePodSet("main", 1).PodSpec(podSpec).Obj()).
		ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").PodSets(
			utiltestingapi.MakePodSetAssignment("main").Count(1).
				TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
					Domain(utiltas.TopologyDomainAssignment{Values: []string{"gpu-node"}, Count: 1}).
					Obj()).
				Obj(),
		).Obj(), time.Now()).
		Obj()
	reclaimed := utiltestingapi.MakeWorkload("reclaimed", "default").
		PodSets(*utiltestingapi.MakePodSet("main", 2).PodSpec(podSpec).Obj()).
		ReserveQuotaAt(admitted.Status.Admission, time.Now()).
		ReclaimablePods(kueue.ReclaimablePod{Name: "main", Count: 2}).
		Obj()
	// Admitted with two of its four Pods, which both finished.
	reclaimedPartly := utiltestingapi.MakeWorkload("reclaimed-partly", "default").
		PodSets(*utiltestingapi.MakePodSet("main", 4).PodSpec(podSpec).Obj()).
		ReserveQuotaAt(admitted.Status.Admission, time.Now()).
		ReclaimablePods(kueue.ReclaimablePod{Name: "main", Count: 2}).
		Obj()
	// A later slice of an elastic Job admitted as "admitted", and the Pod an earlier
	// slice created and the later one keeps.
	laterSlice := utiltestingapi.MakeWorkload("admitted-slice-2", "default").
		Annotation(kueue.WorkloadSliceNameAnnotation, "admitted").
		PodSets(*utiltestingapi.MakePodSet("main", 2).PodSpec(podSpec).Obj()).
		ReserveQuotaAt(admitted.Status.Admission, time.Now()).
		Obj()
	keptPod := testingpod.MakePod("kept-0", "default").
		UID("kept-0").
		Annotation(kueue.WorkloadAnnotation, "admitted-slice-1").
		Annotation(kueue.WorkloadSliceNameAnnotation, "admitted").
		Label(constants.PodSetLabel, "main").
		NodeName("gpu-node").
		Obj()
	keptPodClaim := utiltesting.MakeResourceClaim("kept-0-gpu", "default").
		DeviceRequest("gpu", "gpu.example.com", 1).
		Allocated("gpu", "gpu.example.com", "gpu-pool", "gpu-0").
		ReservedFor("kept-0").
		Obj()
	// A ConcurrentAdmission variant of "admitted", whose Pods name the parent.
	variant := utiltestingapi.MakeWorkload("admitted-variant", "default").
		OwnerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "admitted", "parent-uid").
		PodSets(*utiltestingapi.MakePodSet("main", 2).PodSpec(podSpec).Obj()).
		ReserveQuotaAt(admitted.Status.Admission, time.Now()).
		Obj()
	boundPod := testingpod.MakePod("admitted-0", "default").
		UID("admitted-0").
		Annotation(kueue.WorkloadAnnotation, "admitted").
		Label(constants.PodSetLabel, "main").
		NodeName("gpu-node").
		Obj()
	boundPodClaim := utiltesting.MakeResourceClaim("admitted-0-gpu", "default").
		DeviceRequest("gpu", "gpu.example.com", 1).
		Allocated("gpu", "gpu.example.com", "gpu-pool", "gpu-0").
		ReservedFor("admitted-0").
		Obj()
	foreignPod := testingpod.MakePod("foreign", "default").UID("foreign").NodeName("gpu-node").Obj()
	foreignClaim := utiltesting.MakeResourceClaim("foreign-gpu", "default").
		DeviceRequest("gpu", "gpu.example.com", 1).
		Allocated("gpu", "gpu.example.com", "gpu-pool", "gpu-3").
		ReservedFor("foreign").
		Obj()

	// One device shared by capacity: four 20Gi shares of 80Gi.
	sharedSlice := testingdra.MakeResourceSlice("gpu-node-slice", "gpu.example.com").
		NodeName("gpu-node").
		Pool("gpu-pool", 1, 1).
		Device("gpu-0").
		AllowMultipleAllocations(true).
		DeviceCapacity("memory", "80Gi", nil).
		Obj()
	shareTemplate := utiltesting.MakeResourceClaimTemplate("gpu-template", "default").
		DeviceRequest("gpu", "gpu.example.com", 1).
		WithCapacityRequests(map[string]string{"memory": "20Gi"}).
		Obj()
	// Four partitions of one GPU, each taking 40Gi of its 80Gi: only two fit at once.
	counterSlice := testingdra.MakeResourceSlice("gpu-node-counters", "gpu.example.com").
		NodeName("gpu-node").
		Pool("gpu-pool", 1, 2).
		CounterSet("gpu-0", "memory", "80Gi").
		Obj()
	partitionSlice := testingdra.MakeResourceSlice("gpu-node-partitions", "gpu.example.com").
		NodeName("gpu-node").
		Pool("gpu-pool", 1, 2).
		Device("part-0").CounterConsumption("gpu-0", "memory", "40Gi").
		Device("part-1").CounterConsumption("gpu-0", "memory", "40Gi").
		Device("part-2").CounterConsumption("gpu-0", "memory", "40Gi").
		Device("part-3").CounterConsumption("gpu-0", "memory", "40Gi").
		Obj()
	adminTemplate := utiltesting.MakeResourceClaimTemplate("gpu-template", "default").
		DeviceRequest("gpu", "gpu.example.com", 1).
		WithAdminAccess(true).
		Obj()
	// The DeviceClass behind the extended resource example.com/gpu.
	extendedClass := &resourceapi.DeviceClass{
		Name: "gpu.example.com",
		Spec: resourceapi.DeviceClassSpec{ExtendedResourceName: new("example.com/gpu")},
	}
	extendedPodSpec := &corev1.PodSpec{Containers: []corev1.Container{{
		Name: "c", Image: "busybox",
		Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{"example.com/gpu": resource.MustParse("1")}},
	}}}

	type reservation struct {
		nodeCounts map[string]int32
		wantOK     bool
	}
	tests := map[string]struct {
		objects       []runtime.Object
		pods          []*corev1.Pod
		assumed       []*kueue.Workload
		preempt       workload.Reference
		preemptPods   []simulator.PodRef
		reserve       *reservation
		simulateEmpty bool
		podCount      int32
		// wantLimits is each feasible node's device Pod limit. A node that is left out
		// is infeasible.
		wantLimits map[string]int32
		// wantLimitsAfterRevert is checked after the preemption, Pod preemption or
		// reservation is reverted.
		wantLimitsAfterRevert map[string]int32
		// podSpec replaces the Pod spec that claims one GPU.
		podSpec *corev1.PodSpec
		// leader adds a leader Pod with the same claims, and wantLimitsWithLeader is how
		// many Pods fit next to it.
		leader               bool
		wantLimitsWithLeader map[string]int32
		featureGates         map[featuregate.Feature]bool
		// wantNoPreemptPodsRevert is whether PreemptPods returns no revert, because it
		// gave back nothing.
		wantNoPreemptPodsRevert bool
	}{
		"counts how many Pods the devices take": {
			objects:    []runtime.Object{gpuDeviceClass, gpuSlice, gpuTemplate},
			podCount:   6,
			wantLimits: map[string]int32{"gpu-node": 4},
		},
		"counts fewer Pods next to a leader that takes a device": {
			objects:              []runtime.Object{gpuDeviceClass, gpuSlice, gpuTemplate},
			leader:               true,
			podCount:             6,
			wantLimits:           map[string]int32{"gpu-node": 4},
			wantLimitsWithLeader: map[string]int32{"gpu-node": 3},
		},
		"counts the shares of a device shared by capacity": {
			objects:    []runtime.Object{gpuDeviceClass, sharedSlice, shareTemplate},
			podCount:   6,
			wantLimits: map[string]int32{"gpu-node": 4},
		},
		"counts partitions by the counters they consume, not by device": {
			objects:    []runtime.Object{gpuDeviceClass, counterSlice, partitionSlice, gpuTemplate},
			podCount:   6,
			wantLimits: map[string]int32{"gpu-node": 2},
		},
		"counts partitions of a pool that serves every node by the counters they consume": {
			objects: []runtime.Object{gpuDeviceClass,
				testingdra.MakeResourceSlice("all-nodes-counters", "gpu.example.com").
					AllNodes().
					Pool("gpu-pool", 1, 2).
					CounterSet("gpu-0", "memory", "80Gi").
					Obj(),
				testingdra.MakeResourceSlice("all-nodes-partitions", "gpu.example.com").
					AllNodes().
					Pool("gpu-pool", 1, 2).
					Device("part-0").CounterConsumption("gpu-0", "memory", "40Gi").
					Device("part-1").CounterConsumption("gpu-0", "memory", "40Gi").
					Device("part-2").CounterConsumption("gpu-0", "memory", "40Gi").
					Device("part-3").CounterConsumption("gpu-0", "memory", "40Gi").
					Obj(),
				gpuTemplate},
			podCount:   6,
			wantLimits: map[string]int32{"gpu-node": 2},
		},
		"counts partitions on the node by counters that a slice for every node declares": {
			objects: []runtime.Object{gpuDeviceClass,
				testingdra.MakeResourceSlice("all-nodes-counters", "gpu.example.com").
					AllNodes().
					Pool("gpu-pool", 1, 2).
					CounterSet("gpu-0", "memory", "80Gi").
					Obj(),
				partitionSlice,
				gpuTemplate},
			podCount:   6,
			wantLimits: map[string]int32{"gpu-node": 2},
		},
		"a promised partition consumes counters the next Pods cannot use": {
			objects:    []runtime.Object{gpuDeviceClass, counterSlice, partitionSlice, gpuTemplate},
			assumed:    []*kueue.Workload{admittedOnePod},
			podCount:   6,
			wantLimits: map[string]int32{"gpu-node": 1},
		},
		"admin access takes no device, so it does not bound the count": {
			objects:    []runtime.Object{gpuDeviceClass, gpuSlice, adminTemplate},
			podCount:   6,
			wantLimits: map[string]int32{"gpu-node": 6},
		},
		"counts the Pods that DRA-backed extended resources fit": {
			objects:    []runtime.Object{extendedClass, gpuSlice},
			podSpec:    extendedPodSpec,
			podCount:   6,
			wantLimits: map[string]int32{"gpu-node": 4},
		},
		"stops counting at the Pods requested": {
			objects:    []runtime.Object{gpuDeviceClass, gpuSlice, gpuTemplate},
			podCount:   2,
			wantLimits: map[string]int32{"gpu-node": 2},
		},
		"an admitted Workload's Pods hold their devices before kube-scheduler allocates them": {
			objects:    []runtime.Object{gpuDeviceClass, gpuSlice, gpuTemplate},
			assumed:    []*kueue.Workload{admitted},
			podCount:   6,
			wantLimits: map[string]int32{"gpu-node": 2},
		},
		"a PodSet whose Pods all finished is not promised devices": {
			objects:    []runtime.Object{gpuDeviceClass, gpuSlice, gpuTemplate},
			assumed:    []*kueue.Workload{reclaimed},
			podCount:   6,
			wantLimits: map[string]int32{"gpu-node": 4},
		},
		"a partly admitted PodSet whose admitted Pods all finished is not promised devices": {
			objects:    []runtime.Object{gpuDeviceClass, gpuSlice, gpuTemplate},
			assumed:    []*kueue.Workload{reclaimedPartly},
			podCount:   6,
			wantLimits: map[string]int32{"gpu-node": 4},
		},
		"without ReclaimablePods, a PodSet whose Pods all finished is still promised devices": {
			objects:      []runtime.Object{gpuDeviceClass, gpuSlice, gpuTemplate},
			assumed:      []*kueue.Workload{reclaimed},
			podCount:     6,
			wantLimits:   map[string]int32{"gpu-node": 2},
			featureGates: map[featuregate.Feature]bool{features.ReclaimablePods: false},
		},
		"a bound Pod is counted by its claim, not again as a promise": {
			objects:    []runtime.Object{gpuDeviceClass, gpuSlice, gpuTemplate, boundPodClaim},
			pods:       []*corev1.Pod{boundPod},
			assumed:    []*kueue.Workload{admitted},
			podCount:   6,
			wantLimits: map[string]int32{"gpu-node": 2},
		},
		"an elastic slice counts the Pods an earlier slice created as its own": {
			objects:               []runtime.Object{gpuDeviceClass, gpuSlice, gpuTemplate, keptPodClaim},
			pods:                  []*corev1.Pod{keptPod},
			assumed:               []*kueue.Workload{laterSlice},
			preempt:               "default/admitted-slice-2",
			podCount:              6,
			wantLimits:            map[string]int32{"gpu-node": 4},
			wantLimitsAfterRevert: map[string]int32{"gpu-node": 2},
		},
		"a ConcurrentAdmission variant counts the Pods that name its parent as its own": {
			objects:               []runtime.Object{gpuDeviceClass, gpuSlice, gpuTemplate, boundPodClaim},
			pods:                  []*corev1.Pod{boundPod},
			assumed:               []*kueue.Workload{variant},
			preempt:               "default/admitted-variant",
			podCount:              6,
			wantLimits:            map[string]int32{"gpu-node": 4},
			wantLimitsAfterRevert: map[string]int32{"gpu-node": 2},
		},
		"a removed Workload gives back its devices, and restoring it takes them again": {
			objects:               []runtime.Object{gpuDeviceClass, gpuSlice, gpuTemplate, boundPodClaim, foreignClaim},
			pods:                  []*corev1.Pod{boundPod, foreignPod},
			assumed:               []*kueue.Workload{admitted},
			preempt:               "default/admitted",
			podCount:              6,
			wantLimits:            map[string]int32{"gpu-node": 3},
			wantLimitsAfterRevert: map[string]int32{"gpu-node": 1},
		},
		"a preempted bound Pod gives back its devices until the revert": {
			objects:               []runtime.Object{gpuDeviceClass, gpuSlice, gpuTemplate, boundPodClaim},
			pods:                  []*corev1.Pod{boundPod},
			assumed:               []*kueue.Workload{admitted},
			preemptPods:           []simulator.PodRef{{Key: client.ObjectKeyFromObject(boundPod), UID: boundPod.UID}},
			podCount:              6,
			wantLimits:            map[string]int32{"gpu-node": 3},
			wantLimitsAfterRevert: map[string]int32{"gpu-node": 2},
		},
		"a preempted Pod holding no devices gives back nothing and needs no revert": {
			objects:                 []runtime.Object{gpuDeviceClass, gpuSlice, gpuTemplate},
			pods:                    []*corev1.Pod{foreignPod},
			preemptPods:             []simulator.PodRef{{Key: client.ObjectKeyFromObject(foreignPod), UID: foreignPod.UID}},
			wantNoPreemptPodsRevert: true,
			podCount:                6,
			wantLimits:              map[string]int32{"gpu-node": 4},
		},
		"an empty cluster keeps the devices of Pods Kueue does not manage": {
			objects:       []runtime.Object{gpuDeviceClass, gpuSlice, gpuTemplate, boundPodClaim, foreignClaim},
			pods:          []*corev1.Pod{boundPod, foreignPod},
			assumed:       []*kueue.Workload{admitted},
			simulateEmpty: true,
			podCount:      6,
			wantLimits:    map[string]int32{"gpu-node": 3},
		},
		"Pods placed in this cycle hold their devices until the revert": {
			objects:               []runtime.Object{gpuDeviceClass, gpuSlice, gpuTemplate},
			reserve:               &reservation{nodeCounts: map[string]int32{"gpu-node": 3}, wantOK: true},
			podCount:              6,
			wantLimits:            map[string]int32{"gpu-node": 1},
			wantLimitsAfterRevert: map[string]int32{"gpu-node": 4},
		},
		"a placement that does not fit takes no devices": {
			objects:    []runtime.Object{gpuDeviceClass, gpuSlice, gpuTemplate},
			reserve:    &reservation{nodeCounts: map[string]int32{"gpu-node": 5}, wantOK: false},
			podCount:   6,
			wantLimits: map[string]int32{"gpu-node": 4},
		},
		"the node is infeasible once every device is taken": {
			objects:  []runtime.Object{gpuDeviceClass, gpuSlice, gpuTemplate},
			reserve:  &reservation{nodeCounts: map[string]int32{"gpu-node": 4}, wantOK: true},
			podCount: 6,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, tc.featureGates)
			cl := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(tc.objects...).
				WithIndex(&resourceapi.DeviceClass{}, indexer.DeviceClassExtendedResourceNameIndex,
					indexer.IndexDeviceClassExtendedResourceName).
				Build()
			pods := &podtracker.Tracker{}
			for _, pod := range tc.pods {
				pods.Track(pod)
			}
			checker := NewChecker(&passthroughChecker{}, cl, &CELCache{}, true, SnapshotState{Nodes: []*corev1.Node{gpuNode}, Pods: pods, AssumedWorkloads: tc.assumed})
			template := podTemplate
			if tc.podSpec != nil {
				template = &corev1.PodTemplateSpec{Namespace: "default", Spec: *tc.podSpec}
			}
			requirements := &simulator.PodRequirements{PodTemplate: template, PodCount: tc.podCount, SimulateEmpty: tc.simulateEmpty}
			if tc.leader {
				requirements.LeaderPodTemplate = template
			}

			if tc.preempt != "" {
				checker.RemoveWorkload(tc.preempt)
			}
			var revertPreemptPods func() error
			if tc.preemptPods != nil {
				// PreemptPods gives back only devices already built. SimulateResidualTASPodRelease
				// builds them first through ReleaseFreeingDevices.
				if _, err := checker.devices.get(t.Context()); err != nil {
					t.Fatalf("Building the device state returned error: %v", err)
				}
				var err error
				revertPreemptPods, err = checker.PreemptPods(t.Context(), tc.preemptPods)
				if err != nil {
					t.Fatalf("PreemptPods returned error: %v", err)
				}
				if gotNoRevert := revertPreemptPods == nil; gotNoRevert != tc.wantNoPreemptPodsRevert {
					t.Errorf("PreemptPods returned no revert = %v, want %v", gotNoRevert, tc.wantNoPreemptPodsRevert)
				}
			}
			var revertReserve func()
			if tc.reserve != nil {
				var ok bool
				var err error
				revertReserve, ok, err = checker.ReservePods(t.Context(), simulator.DevicePlacement{PodTemplate: podTemplate, NodeCounts: tc.reserve.nodeCounts})
				if err != nil {
					t.Fatalf("ReservePods returned error: %v", err)
				}
				if ok != tc.reserve.wantOK {
					t.Errorf("ReservePods ok = %v, want %v", ok, tc.reserve.wantOK)
				}
			}

			candidates := []*testCandidate{{node: gpuNode, id: "gpu-node"}}
			feasible, err := checker.FindFeasibleNodes(t.Context(), simulator.AsCandidates(slices.Values(candidates)), requirements, &simulator.NodeExclusionStats{})
			if err != nil {
				t.Fatalf("FindFeasibleNodes returned error: %v", err)
			}
			gotLimits := make(map[string]int32)
			gotWithLeader := make(map[string]int32)
			for _, mc := range feasible {
				limit := mc.(*testCandidate).devicePodLimit
				gotLimits[mc.GetNode().Name] = limit.Pods
				if limit.WithLeader != nil {
					gotWithLeader[mc.GetNode().Name] = *limit.WithLeader
				}
			}
			wantLimits := tc.wantLimits
			if wantLimits == nil {
				wantLimits = map[string]int32{}
			}
			if diff := cmp.Diff(wantLimits, gotLimits); diff != "" {
				t.Errorf("device Pod limits mismatch (-want +got):\n%s", diff)
			}
			if tc.leader {
				if diff := cmp.Diff(tc.wantLimitsWithLeader, gotWithLeader); diff != "" {
					t.Errorf("device Pod limits next to the leader mismatch (-want +got):\n%s", diff)
				}
			}

			if tc.wantLimitsAfterRevert == nil {
				return
			}
			if tc.preempt != "" {
				checker.AddWorkload(tc.preempt)
			}
			if revertPreemptPods != nil {
				if err := revertPreemptPods(); err != nil {
					t.Fatalf("PreemptPods revert returned error: %v", err)
				}
			}
			if revertReserve != nil {
				revertReserve()
			}
			candidates = []*testCandidate{{node: gpuNode, id: "gpu-node"}}
			feasible, err = checker.FindFeasibleNodes(t.Context(), simulator.AsCandidates(slices.Values(candidates)), requirements, &simulator.NodeExclusionStats{})
			if err != nil {
				t.Fatalf("FindFeasibleNodes after revert returned error: %v", err)
			}
			gotLimits = make(map[string]int32)
			for _, mc := range feasible {
				gotLimits[mc.GetNode().Name] = mc.(*testCandidate).devicePodLimit.Pods
			}
			if diff := cmp.Diff(tc.wantLimitsAfterRevert, gotLimits); diff != "" {
				t.Errorf("device Pod limits after revert mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestCheckerReplacePods(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = resourceapi.AddToScheme(scheme)

	gpuNode := &corev1.Node{Name: "gpu-node"}
	objects := []runtime.Object{
		&resourceapi.DeviceClass{Name: "gpu.example.com"},
		testingdra.MakeResourceSlice("gpu-node-slice", "gpu.example.com").
			NodeName("gpu-node").
			Pool("gpu-pool", 1, 1).
			Device("gpu-0").
			Device("gpu-1").
			Device("gpu-2").
			Device("gpu-3").
			Obj(),
		utiltesting.MakeResourceClaimTemplate("gpu-template", "default").
			DeviceRequest("gpu", "gpu.example.com", 1).
			Obj(),
	}
	podTemplate := &corev1.PodTemplateSpec{Namespace: "default", Spec: corev1.PodSpec{
		Containers:     []corev1.Container{{Name: "c", Image: "busybox"}},
		ResourceClaims: []corev1.PodResourceClaim{{Name: "gpu", ResourceClaimTemplateName: new("gpu-template")}},
	}}
	// Admitted onto gpu-node with one Pod and one GPU, whose claim is not allocated yet.
	admitted := utiltestingapi.MakeWorkload("admitted", "default").
		PodSets(*utiltestingapi.MakePodSet("main", 1).PodSpec(podTemplate.Spec).Obj()).
		ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").PodSets(
			utiltestingapi.MakePodSetAssignment("main").Count(1).
				TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
					Domain(utiltas.TopologyDomainAssignment{Values: []string{"gpu-node"}, Count: 1}).
					Obj()).
				Obj(),
		).Obj(), time.Now()).
		Obj()

	tests := map[string]struct {
		wlKey    workload.Reference
		replaced []workload.Reference
		pods     int32
		// removeAfter removes and restores the Workload, as a preemption simulation does.
		removeAfter bool
		// removeReplaced removes the replaced Workloads before the replacement and
		// restores them after it, as a preemption simulation around it does.
		removeReplaced bool
		wantOK         bool
		wantLimit      int32
		// wantLimitAfterRemove is the limit while the Workload is removed, with removeAfter.
		wantLimitAfterRemove int32
		// wantLimitAfterRevert is the limit once the replacement is reverted.
		wantLimitAfterRevert int32
	}{
		"a Workload placed again keeps only its new placement": {
			wlKey: "default/admitted", pods: 2,
			wantOK: true, wantLimit: 2, wantLimitAfterRevert: 3,
		},
		"an elastic slice takes over the devices of the slice it replaces": {
			wlKey: "default/admitted-slice-2", replaced: []workload.Reference{"default/admitted"}, pods: 3,
			wantOK: true, wantLimit: 1, wantLimitAfterRevert: 3,
		},
		"restoring a removed slice that an elastic slice replaced takes no devices again": {
			wlKey: "default/admitted-slice-2", replaced: []workload.Reference{"default/admitted"}, pods: 3, removeReplaced: true,
			wantOK: true, wantLimit: 1, wantLimitAfterRevert: 3,
		},
		"a Workload placed alongside another takes devices on top": {
			wlKey: "default/other", pods: 2,
			wantOK: true, wantLimit: 1, wantLimitAfterRevert: 3,
		},
		"removing and restoring a replaced Workload keeps its new devices taken": {
			wlKey: "default/admitted", pods: 2, removeAfter: true,
			wantOK: true, wantLimit: 2, wantLimitAfterRemove: 4, wantLimitAfterRevert: 3,
		},
		"a placement that does not fit changes nothing": {
			wlKey: "default/other", pods: 4,
			wantOK: false, wantLimit: 3, wantLimitAfterRevert: 3,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			cl := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(objects...).
				WithIndex(&resourceapi.DeviceClass{}, indexer.DeviceClassExtendedResourceNameIndex,
					indexer.IndexDeviceClassExtendedResourceName).
				Build()
			checker := NewChecker(&passthroughChecker{}, cl, &CELCache{}, true, SnapshotState{
				Nodes: []*corev1.Node{gpuNode}, AssumedWorkloads: []*kueue.Workload{admitted},
			})
			requirements := &simulator.PodRequirements{PodTemplate: podTemplate, PodCount: 4}
			placement := simulator.DevicePlacement{PodTemplate: podTemplate, NodeCounts: map[string]int32{"gpu-node": tc.pods}}
			if tc.removeReplaced {
				for _, replaced := range tc.replaced {
					checker.RemoveWorkload(replaced)
				}
			}

			revert, ok, err := checker.ReplacePods(t.Context(), tc.wlKey, tc.replaced, []simulator.DevicePlacement{placement})
			if err != nil {
				t.Fatalf("ReplacePods returned error: %v", err)
			}
			if ok != tc.wantOK {
				t.Errorf("ReplacePods ok = %v, want %v", ok, tc.wantOK)
			}
			if tc.removeAfter {
				checker.RemoveWorkload(tc.wlKey)
				candidates := []*testCandidate{{node: gpuNode, id: "gpu-node"}}
				if _, err := checker.FindFeasibleNodes(t.Context(), simulator.AsCandidates(slices.Values(candidates)), requirements, &simulator.NodeExclusionStats{}); err != nil {
					t.Fatalf("FindFeasibleNodes returned error: %v", err)
				}
				if got := candidates[0].devicePodLimit.Pods; got != tc.wantLimitAfterRemove {
					t.Errorf("device Pod limit while removed = %d, want %d", got, tc.wantLimitAfterRemove)
				}
				checker.AddWorkload(tc.wlKey)
			}
			if tc.removeReplaced {
				for _, replaced := range tc.replaced {
					checker.AddWorkload(replaced)
				}
			}
			candidates := []*testCandidate{{node: gpuNode, id: "gpu-node"}}
			if _, err := checker.FindFeasibleNodes(t.Context(), simulator.AsCandidates(slices.Values(candidates)), requirements, &simulator.NodeExclusionStats{}); err != nil {
				t.Fatalf("FindFeasibleNodes returned error: %v", err)
			}
			if got := candidates[0].devicePodLimit.Pods; got != tc.wantLimit {
				t.Errorf("device Pod limit = %d, want %d", got, tc.wantLimit)
			}

			if revert != nil {
				revert()
			}
			candidates = []*testCandidate{{node: gpuNode, id: "gpu-node"}}
			if _, err := checker.FindFeasibleNodes(t.Context(), simulator.AsCandidates(slices.Values(candidates)), requirements, &simulator.NodeExclusionStats{}); err != nil {
				t.Fatalf("FindFeasibleNodes after revert returned error: %v", err)
			}
			if got := candidates[0].devicePodLimit.Pods; got != tc.wantLimitAfterRevert {
				t.Errorf("device Pod limit after revert = %d, want %d", got, tc.wantLimitAfterRevert)
			}
		})
	}
}

// A Workload without devices must not cost the cluster-wide Lists the device state is
// built from, so the gate costs nothing to Workloads that do not use DRA.
func TestCheckerSkipsWorkloadsWithoutDevices(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = resourceapi.AddToScheme(scheme)

	var listCalls int
	cl := fake.NewClientBuilder().WithScheme(scheme).
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				listCalls++
				return c.List(ctx, list, opts...)
			},
		}).Build()
	checker := NewChecker(&passthroughChecker{}, cl, &CELCache{}, true, SnapshotState{})
	podTemplate := &corev1.PodTemplateSpec{Namespace: "default", Spec: corev1.PodSpec{
		Containers: []corev1.Container{{Name: "c", Image: "busybox"}},
	}}
	wl := utiltestingapi.MakeWorkload("cpu-only", "default").
		PodSets(*utiltestingapi.MakePodSet("main", 1).PodSpec(podTemplate.Spec).Obj()).
		Obj()

	placement := simulator.DevicePlacement{PodTemplate: podTemplate, NodeCounts: map[string]int32{"node": 1}}
	if _, ok, err := checker.ReservePods(t.Context(), placement); err != nil || !ok {
		t.Errorf("ReservePods = ok %v, err %v, want ok", ok, err)
	}
	if revert, ok, err := checker.ReplacePods(t.Context(), "default/cpu-only", nil, []simulator.DevicePlacement{placement}); err != nil || !ok || revert != nil {
		t.Errorf("ReplacePods = revert %v, ok %v, err %v, want no change", revert != nil, ok, err)
	}
	if _, released, err := checker.ReleaseFreeingDevices(t.Context(), wl); err != nil || released {
		t.Errorf("ReleaseFreeingDevices = released %v, err %v, want nothing released", released, err)
	}
	if checker.RemoveWorkload("default/cpu-only") {
		t.Error("RemoveWorkload reported a change for a Workload that holds no devices")
	}
	if listCalls != 0 {
		t.Errorf("cluster-wide List calls = %d, want 0", listCalls)
	}
}

func TestCheckerReleaseFreeingDevices(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = resourceapi.AddToScheme(scheme)

	podSpec := corev1.PodSpec{
		Containers:     []corev1.Container{{Name: "c", Image: "busybox"}},
		ResourceClaims: []corev1.PodResourceClaim{{Name: "gpu", ResourceClaimTemplateName: new("gpu-template")}},
	}
	preemptor := utiltestingapi.MakeWorkload("preemptor", "default").
		PodSets(*utiltestingapi.MakePodSet("main", 1).PodSpec(podSpec).Obj()).
		Obj()
	runningPod := testingpod.MakePod("running", "default").UID("running").NodeName("gpu-node").Obj()

	tests := map[string]struct {
		claim        *resourceapi.ResourceClaim
		pods         []*corev1.Pod
		wantReleased bool
	}{
		"a claim reserved for a Pod that is gone is being freed": {
			claim: utiltesting.MakeResourceClaim("gone-gpu", "default").
				Allocated("gpu", "gpu.example.com", "gpu-pool", "gpu-0").
				ReservedFor("gone").
				Obj(),
			wantReleased: true,
		},
		"a deleted claim is being freed": {
			claim: utiltesting.MakeResourceClaim("deleted-gpu", "default").
				Allocated("gpu", "gpu.example.com", "gpu-pool", "gpu-0").
				ReservedFor("running").
				DeletionTimestamp(time.Now()).
				Finalizers("resource.kubernetes.io/delete-protection").
				Obj(),
			pods:         []*corev1.Pod{runningPod},
			wantReleased: true,
		},
		"a claim reserved for a running Pod is not being freed": {
			claim: utiltesting.MakeResourceClaim("running-gpu", "default").
				Allocated("gpu", "gpu.example.com", "gpu-pool", "gpu-0").
				ReservedFor("running").
				Obj(),
			pods: []*corev1.Pod{runningPod},
		},
		"a claim used by something other than Pods is not being freed": {
			claim: utiltesting.MakeResourceClaim("other-gpu", "default").
				Allocated("gpu", "gpu.example.com", "gpu-pool", "gpu-0").
				ReservedForConsumer("example.com", "widgets", "w").
				Obj(),
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			cl := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(tc.claim).Build()
			pods := &podtracker.Tracker{}
			for _, pod := range tc.pods {
				pods.Track(pod)
			}
			checker := NewChecker(&passthroughChecker{}, cl, &CELCache{}, true, SnapshotState{Pods: pods})
			_, released, err := checker.ReleaseFreeingDevices(t.Context(), preemptor)
			if err != nil {
				t.Fatalf("ReleaseFreeingDevices returned error: %v", err)
			}
			if released != tc.wantReleased {
				t.Errorf("ReleaseFreeingDevices released = %v, want %v", released, tc.wantReleased)
			}
		})
	}
}
