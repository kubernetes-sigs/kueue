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

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"sigs.k8s.io/kueue/pkg/cache/scheduler/simulator"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	kueuedra "sigs.k8s.io/kueue/pkg/dra"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingdra "sigs.k8s.io/kueue/pkg/util/testingjobs/dra"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
)

func TestCheckerDeviceState(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.KueueDRAIntegrationExtendedResource, true)
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

	type reservation struct {
		nodeCounts map[string]int32
		wantOK     bool
	}
	tests := map[string]struct {
		objects  []runtime.Object
		reserve  *reservation
		podCount int32
		// wantLimits is each feasible node's device Pod limit. A node that is left out
		// is infeasible.
		wantLimits map[string]int32
		// wantLimitsAfterRevert is checked after the reservation is reverted.
		wantLimitsAfterRevert map[string]int32
		// podSpec replaces the Pod spec that claims one GPU.
		podSpec *corev1.PodSpec
		// leader adds a leader Pod with the same claims, and wantLimitsWithLeader is how
		// many Pods fit next to it.
		leader               bool
		wantLimitsWithLeader map[string]int32
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
			cl := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(tc.objects...).
				WithIndex(&resourceapi.DeviceClass{}, indexer.DeviceClassExtendedResourceNameIndex,
					indexer.IndexDeviceClassExtendedResourceName).
				Build()
			checker := NewChecker(&passthroughChecker{}, cl, &CELCache{}, true, SnapshotState{Nodes: []*corev1.Node{gpuNode}})
			template := podTemplate
			if tc.podSpec != nil {
				template = &corev1.PodTemplateSpec{Namespace: "default", Spec: *tc.podSpec}
			}
			requirements := &simulator.PodRequirements{PodTemplate: template, PodCount: tc.podCount}
			if tc.leader {
				requirements.LeaderPodTemplate = template
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
			feasible, err := checker.FindFeasibleNodes(t.Context(), simulator.AsCandidates(slices.Values(candidates)), requirements, &simulator.NodeExclusionStats{})
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
		"a zero request for an extended resource that a DeviceClass backs": {
			requests:           corev1.ResourceList{"example.com/gpu": resource.MustParse("0")},
			draBackedResources: gpuBacked,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
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
			checker := NewChecker(&passthroughChecker{}, cl, &CELCache{}, true, SnapshotState{DRABackedResources: tc.draBackedResources})
			podTemplate := &corev1.PodTemplateSpec{Namespace: "default", Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "c", Image: "busybox", Resources: corev1.ResourceRequirements{Requests: tc.requests}}},
			}}
			placement := DevicePlacement{PodTemplate: podTemplate, NodeCounts: map[string]int32{"node": 1}}
			if _, ok, err := checker.ReservePods(t.Context(), placement); err != nil || !ok {
				t.Errorf("ReservePods = ok %v, err %v, want ok", ok, err)
			}
			if listed := listCalls > 0; listed != tc.wantListed {
				t.Errorf("listed the cluster = %v (%d List calls), want %v", listed, listCalls, tc.wantListed)
			}
		})
	}
}
