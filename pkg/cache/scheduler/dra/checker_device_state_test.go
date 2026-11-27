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
	kueuedra "sigs.k8s.io/kueue/pkg/dra"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingdra "sigs.k8s.io/kueue/pkg/util/testingjobs/dra"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestCheckerDeviceState(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = resourceapi.AddToScheme(scheme)

	gpuNode := testingnode.MakeNode("gpu-node").Obj()
	gpuDeviceClass := testingdra.MakeDeviceClass("gpu.example.com").Obj()
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
					Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"gpu-node"}, 2).Obj()).
					Obj()).
				Obj(),
		).Obj(), time.Now()).
		Obj()
	admittedOnePod := utiltestingapi.MakeWorkload("one-pod", "default").
		PodSets(*utiltestingapi.MakePodSet("main", 1).PodSpec(podSpec).Obj()).
		ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").PodSets(
			utiltestingapi.MakePodSetAssignment("main").Count(1).
				TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
					Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"gpu-node"}, 1).Obj()).
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
		ResourceClaimTemplate("gpu", "gpu-template").
		ResourceClaimStatus("gpu", "kept-0-gpu").
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
		ResourceClaimTemplate("gpu", "gpu-template").
		ResourceClaimStatus("gpu", "admitted-0-gpu").
		NodeName("gpu-node").
		Obj()
	// A Pod left over from an earlier admission of "admitted", still being deleted.
	leftoverPod := testingpod.MakePod("admitted-old-0", "default").
		UID("admitted-old-0").
		Annotation(kueue.WorkloadAnnotation, "admitted").
		Label(constants.PodSetLabel, "main").
		ResourceClaimTemplate("gpu", "gpu-template").
		ResourceClaimStatus("gpu", "admitted-old-0-gpu").
		NodeName("gpu-node").
		DeletionTimestamp(time.Now()).
		Obj()
	leftoverPodClaim := utiltesting.MakeResourceClaim("admitted-old-0-gpu", "default").
		DeviceRequest("gpu", "gpu.example.com", 1).
		Allocated("gpu", "gpu.example.com", "gpu-pool", "gpu-0").
		ReservedFor("admitted-old-0").
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
	extendedClass := testingdra.MakeDeviceClass("gpu.example.com").ExtendedResourceName("example.com/gpu").Obj()
	extendedPodSpec := &corev1.PodSpec{Containers: []corev1.Container{{
		Name: "c", Image: "busybox",
		Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{"example.com/gpu": resource.MustParse("1")}},
	}}}
	// Admitted onto gpu-node with two Pods that each take one GPU through example.com/gpu.
	admittedExtended := utiltestingapi.MakeWorkload("admitted-extended", "default").
		PodSets(*utiltestingapi.MakePodSet("main", 2).PodSpec(*extendedPodSpec).Obj()).
		ReserveQuotaAt(admitted.Status.Admission, time.Now()).
		Obj()

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
		"a Pod left over from an earlier admission does not stand in for a Pod of this one": {
			objects:    []runtime.Object{gpuDeviceClass, gpuSlice, gpuTemplate, leftoverPodClaim},
			pods:       []*corev1.Pod{leftoverPod},
			assumed:    []*kueue.Workload{admitted},
			podCount:   6,
			wantLimits: map[string]int32{"gpu-node": 1},
		},
		"a bound Pod is counted by its claim, not again as a promise": {
			objects:    []runtime.Object{gpuDeviceClass, gpuSlice, gpuTemplate, boundPodClaim},
			pods:       []*corev1.Pod{boundPod},
			assumed:    []*kueue.Workload{admitted},
			podCount:   6,
			wantLimits: map[string]int32{"gpu-node": 2},
		},
		// kube-scheduler reserves the claim before it binds the Pod, so the tracker does not
		// show the Pod yet. The claim holds its device, and the Pod is not promised another.
		"a Pod that kube-scheduler is binding is counted by its claim, not again as a promise": {
			objects: []runtime.Object{gpuDeviceClass, gpuSlice, gpuTemplate,
				utiltesting.MakeResourceClaim("admitted-0-gpu", "default").
					DeviceRequest("gpu", "gpu.example.com", 1).
					Allocated("gpu", "gpu.example.com", "gpu-pool", "gpu-0").
					AllocatedOnNode("gpu-node").
					ReservedFor("admitted-0").
					Obj(),
				testingpod.MakePod("admitted-0", "default").
					UID("admitted-0").
					Annotation(kueue.WorkloadAnnotation, "admitted").
					Label(constants.PodSetLabel, "main").
					ResourceClaimTemplate("gpu", "gpu-template").
					ResourceClaimStatus("gpu", "admitted-0-gpu").
					Obj(),
			},
			assumed:    []*kueue.Workload{admitted},
			podCount:   6,
			wantLimits: map[string]int32{"gpu-node": 2},
		},
		// The Pod being bound has two claims, and still counts as one Pod, so the other Pod of
		// the Workload keeps its promise.
		"a Pod that kube-scheduler is binding counts once for all its claims": {
			objects: []runtime.Object{gpuDeviceClass, gpuSlice, gpuTemplate,
				utiltesting.MakeResourceClaim("admitted-0-gpu", "default").
					DeviceRequest("gpu", "gpu.example.com", 1).
					Allocated("gpu", "gpu.example.com", "gpu-pool", "gpu-0").
					AllocatedOnNode("gpu-node").
					ReservedFor("admitted-0").
					Obj(),
				utiltesting.MakeResourceClaim("admitted-0-extra", "default").
					DeviceRequest("gpu", "gpu.example.com", 1).
					Allocated("gpu", "gpu.example.com", "gpu-pool", "gpu-1").
					AllocatedOnNode("gpu-node").
					ReservedFor("admitted-0").
					Obj(),
				testingpod.MakePod("admitted-0", "default").
					UID("admitted-0").
					Annotation(kueue.WorkloadAnnotation, "admitted").
					Label(constants.PodSetLabel, "main").
					ResourceClaimTemplate("gpu", "gpu-template").
					ResourceClaimStatus("gpu", "admitted-0-gpu").
					Obj(),
			},
			assumed:    []*kueue.Workload{admitted},
			podCount:   6,
			wantLimits: map[string]int32{"gpu-node": 1},
		},
		// A StatefulSet can recreate a Pod before the claim of the old Pod is freed. The new
		// Pod takes its GPU through extended resources and has no claim yet, so only the UID
		// tells it from a Pod being bound.
		"a new Pod with the name of the Pod a claim is reserved for keeps its promise": {
			objects: []runtime.Object{extendedClass, gpuSlice,
				utiltesting.MakeResourceClaim("admitted-extended-0-extended-resources", "default").
					DeviceRequest("gpu", "gpu.example.com", 1).
					Allocated("gpu", "gpu.example.com", "gpu-pool", "gpu-0").
					AllocatedOnNode("gpu-node").
					ReservedFor("admitted-extended-0").
					Obj(),
				testingpod.MakePod("admitted-extended-0", "default").
					UID("admitted-extended-0-replacement").
					Annotation(kueue.WorkloadAnnotation, "admitted-extended").
					Label(constants.PodSetLabel, "main").
					Request("example.com/gpu", "1").
					Obj(),
			},
			assumed:    []*kueue.Workload{admittedExtended},
			podSpec:    extendedPodSpec,
			podCount:   6,
			wantLimits: map[string]int32{"gpu-node": 1},
		},
		// Pods and claims come from different informers. Until the claim is seen allocated,
		// the bound Pod keeps its promise, so that its device is not counted free.
		"a Pod seen bound before its claim is seen allocated keeps its promise": {
			objects:    []runtime.Object{gpuDeviceClass, gpuSlice, gpuTemplate},
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
		// The node has two GPUs for three promised Pods. one-pod keeps its GPU when admitted
		// is removed before the devices are built, as admitted would leave it none.
		"a Workload removed before the devices are built leaves the others their promises": {
			objects: []runtime.Object{gpuDeviceClass, gpuTemplate,
				testingdra.MakeResourceSlice("gpu-node-slice", "gpu.example.com").
					NodeName("gpu-node").
					Pool("gpu-pool", 1, 1).
					Device("gpu-0").
					Device("gpu-1").
					Obj(),
			},
			assumed:               []*kueue.Workload{admitted, admittedOnePod},
			preempt:               "default/admitted",
			podCount:              2,
			wantLimits:            map[string]int32{"gpu-node": 1},
			wantLimitsAfterRevert: map[string]int32{},
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
		// The claim lists the replaced Pod, so removing admitted does not give it back,
		// though the new Pod with the same name belongs to admitted.
		"a removed Workload does not give back the claim of a Pod replaced by a new Pod with the same name": {
			objects: []runtime.Object{gpuDeviceClass, gpuSlice, gpuTemplate, boundPodClaim},
			pods: []*corev1.Pod{testingpod.MakePod("admitted-0", "default").
				UID("admitted-0-replacement").
				Annotation(kueue.WorkloadAnnotation, "admitted").
				Label(constants.PodSetLabel, "main").
				ResourceClaimTemplate("gpu", "gpu-template").
				ResourceClaimStatus("gpu", "admitted-0-replacement-gpu").
				NodeName("gpu-node").
				Obj()},
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
		// Another Pod still uses the claim, so preempting one of its Pods frees no device.
		"a preempted Pod gives back nothing of a claim another Pod still uses": {
			objects: []runtime.Object{gpuDeviceClass, gpuSlice, gpuTemplate,
				utiltesting.MakeResourceClaim("shared-gpu", "default").
					DeviceRequest("gpu", "gpu.example.com", 1).
					Allocated("gpu", "gpu.example.com", "gpu-pool", "gpu-0").
					ReservedFor("first").
					ReservedFor("second").
					Obj(),
			},
			preemptPods:             []simulator.PodRef{{Key: client.ObjectKey{Namespace: "default", Name: "first"}, UID: "first"}},
			wantNoPreemptPodsRevert: true,
			podCount:                6,
			wantLimits:              map[string]int32{"gpu-node": 3},
			wantLimitsAfterRevert:   map[string]int32{"gpu-node": 3},
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
			// Once the check is done, the devices of admitted Workloads are taken again.
			wantLimitsAfterRevert: map[string]int32{"gpu-node": 1},
		},
		// The Pod names a Workload that is not in the snapshot, so preempting every
		// Workload does not free its GPU.
		"an empty cluster keeps the devices of Pods whose Workload is not admitted": {
			objects:               []runtime.Object{gpuDeviceClass, gpuSlice, gpuTemplate, boundPodClaim},
			pods:                  []*corev1.Pod{boundPod},
			simulateEmpty:         true,
			podCount:              6,
			wantLimits:            map[string]int32{"gpu-node": 3},
			wantLimitsAfterRevert: map[string]int32{"gpu-node": 3},
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
			checker := NewChecker(cl, &CELCache{}, true, SnapshotState{Nodes: []*corev1.Node{gpuNode}, Pods: pods, AssumedWorkloads: tc.assumed})
			eval := checker.WrapNodeFeasibilityEvaluator(&passthroughChecker{})
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
			var revertPreemptPods func()
			if tc.preemptPods != nil {
				// PreemptPods gives back only devices already built. SimulateResidualTASPodRelease
				// builds them first through ReleaseFreeingDevices.
				if _, err := checker.devices.get(t.Context()); err != nil {
					t.Fatalf("Building the device state returned error: %v", err)
				}
				revertPreemptPods = checker.PreemptPods(tc.preemptPods)
				if gotNoRevert := revertPreemptPods == nil; gotNoRevert != tc.wantNoPreemptPodsRevert {
					t.Errorf("PreemptPods returned no revert = %v, want %v", gotNoRevert, tc.wantNoPreemptPodsRevert)
				}
			}
			var revertReserve func()
			if tc.reserve != nil {
				var ok bool
				var err error
				revertReserve, ok, err = checker.ReservePods(t.Context(), DevicePlacement{PodTemplate: podTemplate, NodeCounts: tc.reserve.nodeCounts})
				if err != nil {
					t.Fatalf("ReservePods returned error: %v", err)
				}
				if ok != tc.reserve.wantOK {
					t.Errorf("ReservePods ok = %v, want %v", ok, tc.reserve.wantOK)
				}
			}

			candidates := []*testCandidate{{node: gpuNode, id: "gpu-node"}}
			feasible, err := eval.FindFeasibleNodes(t.Context(), simulator.AsCandidates(slices.Values(candidates)), requirements, &simulator.NodeExclusionStats{})
			if err != nil {
				t.Fatalf("FindFeasibleNodes returned error: %v", err)
			}
			gotLimits := make(map[string]int32)
			gotWithLeader := make(map[string]int32)
			for _, mc := range feasible {
				limit := mc.(*testCandidate).devicePodLimit
				gotLimits[mc.GetNode().Name] = limit.Pods
				gotWithLeader[mc.GetNode().Name] = limit.PodsWithLeader
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
				revertPreemptPods()
			}
			if revertReserve != nil {
				revertReserve()
			}
			requirements.SimulateEmpty = false
			candidates = []*testCandidate{{node: gpuNode, id: "gpu-node"}}
			feasible, err = eval.FindFeasibleNodes(t.Context(), simulator.AsCandidates(slices.Values(candidates)), requirements, &simulator.NodeExclusionStats{})
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

// A Pod reaches boundPods twice when the tracker adds it after the Pods being bound were
// read, or when it has several claims. It counts once either way.
func TestBoundPodsCountsEachPodOnce(t *testing.T) {
	pod := testingpod.MakePod("admitted-0", "default").
		UID("admitted-0").
		Annotation(kueue.WorkloadAnnotation, "admitted").
		Label(constants.PodSetLabel, "main").
		ResourceClaimTemplate("gpu", "gpu-template").
		NodeName("gpu-node").
		Obj()
	admitted := utiltestingapi.MakeWorkload("admitted", "default").Obj()
	cases := map[string]struct {
		tracked []*corev1.Pod
		binding []*corev1.Pod
	}{
		"a Pod in the tracker and among the Pods being bound": {
			tracked: []*corev1.Pod{pod},
			binding: []*corev1.Pod{pod.DeepCopy()},
		},
		"a Pod twice among the Pods being bound": {
			binding: []*corev1.Pod{pod.DeepCopy(), pod.DeepCopy()},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			pods := &podtracker.Tracker{}
			for _, p := range tc.tracked {
				pods.Track(p)
			}
			owners := newPodOwners(SnapshotState{Pods: pods, AssumedWorkloads: []*kueue.Workload{admitted}})
			want := map[workload.Reference]map[kueue.PodSetReference]map[string]int32{
				"default/admitted": {"main": {"gpu-node": 1}},
			}
			got := owners.boundPods(func(*corev1.PodSpec) bool { return true }, func(*corev1.Pod) bool { return true }, tc.binding)
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("unexpected bound Pods (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestCheckerReplacePods(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = resourceapi.AddToScheme(scheme)

	gpuNode := testingnode.MakeNode("gpu-node").Obj()
	objects := []runtime.Object{
		testingdra.MakeDeviceClass("gpu.example.com").Obj(),
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
					Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"gpu-node"}, 1).Obj()).
					Obj()).
				Obj(),
		).Obj(), time.Now()).
		Obj()
	// Admitted onto gpu-node with two Pods. One is bound and holds its GPU through its claim.
	admittedTwoPods := utiltestingapi.MakeWorkload("two-pods", "default").
		PodSets(*utiltestingapi.MakePodSet("main", 2).PodSpec(podTemplate.Spec).Obj()).
		ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").PodSets(
			utiltestingapi.MakePodSetAssignment("main").Count(2).
				TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
					Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"gpu-node"}, 2).Obj()).
					Obj()).
				Obj(),
		).Obj(), time.Now()).
		Obj()
	boundPod := testingpod.MakePod("two-pods-0", "default").
		UID("two-pods-0").
		Annotation(kueue.WorkloadAnnotation, "two-pods").
		Label(constants.PodSetLabel, "main").
		ResourceClaimTemplate("gpu", "gpu-template").
		ResourceClaimStatus("gpu", "two-pods-0-gpu").
		NodeName("gpu-node").
		Obj()
	boundPodClaim := utiltesting.MakeResourceClaim("two-pods-0-gpu", "default").
		DeviceRequest("gpu", "gpu.example.com", 1).
		Allocated("gpu", "gpu.example.com", "gpu-pool", "gpu-0").
		ReservedFor("two-pods-0").
		Obj()

	tests := map[string]struct {
		assumed []*kueue.Workload
		// boundPods and claims are the Pods and allocated claims in the cluster.
		boundPods []*corev1.Pod
		claims    []runtime.Object
		ownership PodOwnership
		wlKey     workload.Reference
		replaced  []workload.Reference
		pods      int32
		// removeAfter removes and restores the Workload, as a preemption simulation does.
		removeAfter bool
		wantOK      bool
		wantLimit   int32
		// wantLimitAfterRemove is the limit while the Workload is removed, with removeAfter.
		wantLimitAfterRemove int32
		// wantLimitAfterRevert is the limit once the replacement is reverted.
		wantLimitAfterRevert int32
		// simulateEmpty checks the limits as if every admitted Workload were preempted.
		simulateEmpty bool
	}{
		"a Workload placed again keeps only its new placement": {
			assumed: []*kueue.Workload{admitted},
			wlKey:   "default/admitted", pods: 2,
			wantOK: true, wantLimit: 2, wantLimitAfterRevert: 3,
		},
		"an elastic slice takes over the devices of the slice it replaces": {
			assumed: []*kueue.Workload{admitted},
			wlKey:   "default/admitted-slice-2", replaced: []workload.Reference{"default/admitted"}, pods: 3,
			wantOK: true, wantLimit: 1, wantLimitAfterRevert: 3,
		},
		"a Workload placed alongside another takes devices on top": {
			assumed: []*kueue.Workload{admitted},
			wlKey:   "default/other", pods: 2,
			wantOK: true, wantLimit: 1, wantLimitAfterRevert: 3,
		},
		"removing and restoring a replaced Workload keeps its new devices taken": {
			assumed: []*kueue.Workload{admitted},
			wlKey:   "default/admitted", pods: 2, removeAfter: true,
			wantOK: true, wantLimit: 2, wantLimitAfterRemove: 4, wantLimitAfterRevert: 3,
		},
		// Preemption cannot remove a Workload placed in this cycle, as TAS keeps its usage.
		"an empty cluster keeps the devices of a Workload placed in this cycle": {
			assumed: []*kueue.Workload{admitted},
			wlKey:   "default/other", pods: 2, simulateEmpty: true,
			wantOK: true, wantLimit: 2, wantLimitAfterRevert: 4,
		},
		"a placement that does not fit changes nothing": {
			assumed: []*kueue.Workload{admitted},
			wlKey:   "default/other", pods: 4,
			wantOK: false, wantLimit: 3, wantLimitAfterRevert: 3,
		},
		"a Workload placed again that does not fit keeps its devices": {
			assumed: []*kueue.Workload{admitted},
			wlKey:   "default/admitted", pods: 5,
			wantOK: false, wantLimit: 3, wantLimitAfterRevert: 3,
		},
		"a Workload placed again keeps the devices its bound Pods hold": {
			assumed:   []*kueue.Workload{admittedTwoPods},
			boundPods: []*corev1.Pod{boundPod},
			claims:    []runtime.Object{boundPodClaim},
			wlKey:     "default/two-pods", pods: 2,
			wantOK: true, wantLimit: 2, wantLimitAfterRevert: 2,
		},
		"a Workload placed again keeps the devices of bound Pods it does not own exactly, as a JobSet's": {
			assumed:   []*kueue.Workload{admittedTwoPods},
			boundPods: []*corev1.Pod{boundPod},
			claims:    []runtime.Object{boundPodClaim},
			ownership: func(*kueue.Workload) PodMatcher {
				return func(*corev1.Pod) (covers, owns bool) { return true, false }
			},
			wlKey: "default/two-pods", pods: 2,
			wantOK: true, wantLimit: 2, wantLimitAfterRevert: 2,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			cl := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(append(objects, tc.claims...)...).
				WithIndex(&resourceapi.DeviceClass{}, indexer.DeviceClassExtendedResourceNameIndex,
					indexer.IndexDeviceClassExtendedResourceName).
				Build()
			pods := &podtracker.Tracker{}
			for _, pod := range tc.boundPods {
				pods.Track(pod)
			}
			checker := NewChecker(cl, &CELCache{}, true, SnapshotState{
				Nodes: []*corev1.Node{gpuNode}, Pods: pods, AssumedWorkloads: tc.assumed, Ownership: tc.ownership,
			})
			eval := checker.WrapNodeFeasibilityEvaluator(&passthroughChecker{})
			requirements := &simulator.PodRequirements{PodTemplate: podTemplate, PodCount: 4, SimulateEmpty: tc.simulateEmpty}
			placement := NewDevicePlacement(&tc.assumed[0].Spec.PodSets[0], "default", map[string]int32{"gpu-node": tc.pods})
			revert, ok, err := checker.ReplacePods(t.Context(), tc.wlKey, tc.replaced, []DevicePlacement{placement})
			if err != nil {
				t.Fatalf("ReplacePods returned error: %v", err)
			}
			if ok != tc.wantOK {
				t.Errorf("ReplacePods ok = %v, want %v", ok, tc.wantOK)
			}
			if tc.removeAfter {
				checker.RemoveWorkload(tc.wlKey)
				candidates := []*testCandidate{{node: gpuNode, id: "gpu-node"}}
				if _, err := eval.FindFeasibleNodes(t.Context(), simulator.AsCandidates(slices.Values(candidates)), requirements, &simulator.NodeExclusionStats{}); err != nil {
					t.Fatalf("FindFeasibleNodes returned error: %v", err)
				}
				if got := candidates[0].devicePodLimit.Pods; got != tc.wantLimitAfterRemove {
					t.Errorf("device Pod limit while removed = %d, want %d", got, tc.wantLimitAfterRemove)
				}
				checker.AddWorkload(tc.wlKey)
			}
			candidates := []*testCandidate{{node: gpuNode, id: "gpu-node"}}
			if _, err := eval.FindFeasibleNodes(t.Context(), simulator.AsCandidates(slices.Values(candidates)), requirements, &simulator.NodeExclusionStats{}); err != nil {
				t.Fatalf("FindFeasibleNodes returned error: %v", err)
			}
			if got := candidates[0].devicePodLimit.Pods; got != tc.wantLimit {
				t.Errorf("device Pod limit = %d, want %d", got, tc.wantLimit)
			}

			if revert != nil {
				revert()
			}
			candidates = []*testCandidate{{node: gpuNode, id: "gpu-node"}}
			if _, err := eval.FindFeasibleNodes(t.Context(), simulator.AsCandidates(slices.Values(candidates)), requirements, &simulator.NodeExclusionStats{}); err != nil {
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

	gpuBacked := kueuedra.NewExtendedResourceCache()
	gpuBacked.Add("example.com/gpu", "gpu.example.com")

	cases := map[string]struct {
		requests           corev1.ResourceList
		draBackedResources *kueuedra.ExtendedResourceCache
		featureGates       map[featuregate.Feature]bool
		wantListed         bool
	}{
		"no claims and no extended resources": {},
		"an extended resource that a device plugin serves": {
			requests:           corev1.ResourceList{"example.com/gpu": resource.MustParse("1")},
			draBackedResources: kueuedra.NewExtendedResourceCache(),
		},
		"an extended resource that a DeviceClass backs": {
			requests:           corev1.ResourceList{"example.com/gpu": resource.MustParse("1")},
			draBackedResources: gpuBacked,
			wantListed:         true,
		},
		"an extended resource that a DeviceClass backs, with the extended resource gate off": {
			requests:           corev1.ResourceList{"example.com/gpu": resource.MustParse("1")},
			draBackedResources: gpuBacked,
			featureGates:       map[featuregate.Feature]bool{features.KueueDRAIntegrationExtendedResource: false},
		},
		"a zero request for an extended resource that a DeviceClass backs": {
			requests:           corev1.ResourceList{"example.com/gpu": resource.MustParse("0")},
			draBackedResources: gpuBacked,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, tc.featureGates)
			podTemplate := &corev1.PodTemplateSpec{Namespace: "default", Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "c", Image: "busybox", Resources: corev1.ResourceRequirements{Requests: tc.requests}}},
			}}
			wl := utiltestingapi.MakeWorkload("workload", "default").
				PodSets(*utiltestingapi.MakePodSet("main", 1).PodSpec(podTemplate.Spec).Obj()).
				Obj()
			placement := DevicePlacement{PodTemplate: podTemplate, NodeCounts: map[string]int32{"node": 1}}
			// Each operation gets its own checker, so one that lists cannot hide another that
			// should list and skips it.
			for _, operation := range []string{"ReservePods", "ReplacePods", "ReleaseFreeingDevices", "RemoveWorkload"} {
				var listCalls int
				cl := fake.NewClientBuilder().WithScheme(scheme).
					WithIndex(&resourceapi.DeviceClass{}, indexer.DeviceClassExtendedResourceNameIndex,
						indexer.IndexDeviceClassExtendedResourceName).
					WithInterceptorFuncs(interceptor.Funcs{
						List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
							listCalls++
							return c.List(ctx, list, opts...)
						},
					}).Build()
				checker := NewChecker(cl, &CELCache{}, true, SnapshotState{DRABackedResources: tc.draBackedResources})
				wantListed := tc.wantListed
				switch operation {
				case "ReservePods":
					if _, ok, err := checker.ReservePods(t.Context(), placement); err != nil || !ok {
						t.Errorf("ReservePods = ok %v, err %v, want ok", ok, err)
					}
				case "ReplacePods":
					revert, ok, err := checker.ReplacePods(t.Context(), "default/workload", nil, []DevicePlacement{placement})
					if err != nil || !ok {
						t.Errorf("ReplacePods = ok %v, err %v, want ok", ok, err)
					}
					if revert != nil {
						revert()
					}
				case "ReleaseFreeingDevices":
					if _, released, err := checker.ReleaseFreeingDevices(t.Context(), wl); err != nil || released {
						t.Errorf("ReleaseFreeingDevices = released %v, err %v, want nothing released", released, err)
					}
				case "RemoveWorkload":
					// It only reads devices that were built, so it never lists.
					wantListed = false
					if checker.RemoveWorkload("default/workload") {
						t.Error("RemoveWorkload reported a change for a Workload that holds no devices")
					}
				}
				if listed := listCalls > 0; listed != wantListed {
					t.Errorf("%s listed the cluster = %v (%d List calls), want %v", operation, listed, listCalls, wantListed)
				}
			}
		})
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
	replacementPod := testingpod.MakePod("replaced", "default").UID("replacement").NodeName("gpu-node").Obj()
	// One GPU on the node, which the claim of each case holds.
	gpuNode := testingnode.MakeNode("gpu-node").Obj()
	objects := []runtime.Object{
		testingdra.MakeDeviceClass("gpu.example.com").Obj(),
		testingdra.MakeResourceSlice("gpu-node-slice", "gpu.example.com").
			NodeName("gpu-node").
			Pool("gpu-pool", 1, 1).
			Device("gpu-0").
			Obj(),
		utiltesting.MakeResourceClaimTemplate("gpu-template", "default").
			DeviceRequest("gpu", "gpu.example.com", 1).
			Obj(),
	}

	tests := map[string]struct {
		claim *resourceapi.ResourceClaim
		pods  []*corev1.Pod
		// apiPods are Pods the API has but the tracker does not, as it tracks only Pods
		// that run on a node.
		apiPods      []runtime.Object
		wantReleased bool
	}{
		"a claim reserved for a Pod that is gone is being freed": {
			claim: utiltesting.MakeResourceClaim("gone-gpu", "default").
				Allocated("gpu", "gpu.example.com", "gpu-pool", "gpu-0").
				ReservedFor("gone").
				Obj(),
			wantReleased: true,
		},
		"a claim reserved for a Pod that kube-scheduler is binding is not being freed": {
			claim: utiltesting.MakeResourceClaim("binding-gpu", "default").
				Allocated("gpu", "gpu.example.com", "gpu-pool", "gpu-0").
				ReservedFor("binding").
				Obj(),
			apiPods: []runtime.Object{testingpod.MakePod("binding", "default").UID("binding").Obj()},
		},
		"a claim of a Pod replaced by a new Pod with the same name is being freed while the new Pod runs": {
			claim: utiltesting.MakeResourceClaim("replaced-gpu", "default").
				Allocated("gpu", "gpu.example.com", "gpu-pool", "gpu-0").
				ReservedFor("replaced").
				Obj(),
			pods:         []*corev1.Pod{replacementPod},
			apiPods:      []runtime.Object{replacementPod.DeepCopy()},
			wantReleased: true,
		},
		"a claim reserved for a finished Pod is being freed": {
			claim: utiltesting.MakeResourceClaim("done-gpu", "default").
				Allocated("gpu", "gpu.example.com", "gpu-pool", "gpu-0").
				ReservedFor("done").
				Obj(),
			apiPods:      []runtime.Object{testingpod.MakePod("done", "default").UID("done").NodeName("gpu-node").StatusPhase(corev1.PodSucceeded).Obj()},
			wantReleased: true,
		},
		"a deleted claim that no Pod uses is being freed": {
			claim: utiltesting.MakeResourceClaim("deleted-gpu", "default").
				Allocated("gpu", "gpu.example.com", "gpu-pool", "gpu-0").
				DeletionTimestamp(time.Now()).
				Finalizers("resource.kubernetes.io/delete-protection").
				Obj(),
			wantReleased: true,
		},
		"a deleted claim whose Pod still runs is not being freed": {
			claim: utiltesting.MakeResourceClaim("deleted-gpu", "default").
				Allocated("gpu", "gpu.example.com", "gpu-pool", "gpu-0").
				ReservedFor("running").
				DeletionTimestamp(time.Now()).
				Finalizers("resource.kubernetes.io/delete-protection").
				Obj(),
			pods: []*corev1.Pod{runningPod},
		},
		"a claim that something other than Pods also uses is not being freed": {
			claim: utiltesting.MakeResourceClaim("shared-gpu", "default").
				Allocated("gpu", "gpu.example.com", "gpu-pool", "gpu-0").
				ReservedFor("gone").
				ReservedForConsumer("example.com", "widgets", "w").
				Obj(),
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
			cl := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(append(append(objects, tc.claim), tc.apiPods...)...).
				WithIndex(&resourceapi.DeviceClass{}, indexer.DeviceClassExtendedResourceNameIndex,
					indexer.IndexDeviceClassExtendedResourceName).
				Build()
			pods := &podtracker.Tracker{}
			for _, pod := range tc.pods {
				pods.Track(pod)
			}
			checker := NewChecker(cl, &CELCache{}, true, SnapshotState{Nodes: []*corev1.Node{gpuNode}, Pods: pods})
			eval := checker.WrapNodeFeasibilityEvaluator(&passthroughChecker{})
			revert, released, err := checker.ReleaseFreeingDevices(t.Context(), preemptor)
			if err != nil {
				t.Fatalf("ReleaseFreeingDevices returned error: %v", err)
			}
			if released != tc.wantReleased {
				t.Errorf("ReleaseFreeingDevices released = %v, want %v", released, tc.wantReleased)
			}
			// The node's one GPU is free for a Pod only once the claim is released.
			requirements := &simulator.PodRequirements{PodTemplate: &corev1.PodTemplateSpec{Namespace: "default", Spec: podSpec}, PodCount: 1}
			candidates := []*testCandidate{{node: gpuNode, id: "gpu-node"}}
			feasible, err := eval.FindFeasibleNodes(t.Context(), simulator.AsCandidates(slices.Values(candidates)), requirements, &simulator.NodeExclusionStats{})
			if err != nil {
				t.Fatalf("FindFeasibleNodes returned error: %v", err)
			}
			if gotFree := len(feasible) == 1; gotFree != tc.wantReleased {
				t.Errorf("the GPU is free = %v, want %v", gotFree, tc.wantReleased)
			}
			// The revert takes the GPU again.
			revert()
			candidates = []*testCandidate{{node: gpuNode, id: "gpu-node"}}
			feasible, err = eval.FindFeasibleNodes(t.Context(), simulator.AsCandidates(slices.Values(candidates)), requirements, &simulator.NodeExclusionStats{})
			if err != nil {
				t.Fatalf("FindFeasibleNodes after revert returned error: %v", err)
			}
			if len(feasible) != 0 {
				t.Error("the GPU is free after the revert, want it taken")
			}
		})
	}
}
