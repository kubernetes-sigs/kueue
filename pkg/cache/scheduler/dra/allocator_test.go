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
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/cache/scheduler/podtracker"
	"sigs.k8s.io/kueue/pkg/cache/scheduler/simulator"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingdra "sigs.k8s.io/kueue/pkg/util/testingjobs/dra"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
)

func TestCheckerListsClusterStateOncePerSnapshot(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.KueueDRAIntegrationDeviceTaints, true)
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = resourceapi.AddToScheme(scheme)

	node := &corev1.Node{Name: "gpu-node"}
	deviceClass := &resourceapi.DeviceClass{Name: "gpu.example.com"}
	claimTemplate := &resourceapi.ResourceClaimTemplate{
		Name: "gpu-template", Namespace: "default",
		Spec: resourceapi.ResourceClaimTemplateSpec{
			Spec: resourceapi.ResourceClaimSpec{
				Devices: resourceapi.DeviceClaim{
					Requests: []resourceapi.DeviceRequest{{
						Name:    "gpu",
						Exactly: &resourceapi.ExactDeviceRequest{DeviceClassName: "gpu.example.com"},
					}},
				},
			},
		},
	}
	slice := testingdra.MakeResourceSlice("gpu-node-slice", "gpu.example.com").
		NodeName("gpu-node").
		Pool("gpu-node", 1, 1).
		Device("gpu-0").
		Obj()

	var listCalls int
	cl := fake.NewClientBuilder().WithScheme(scheme).
		WithRuntimeObjects(node, deviceClass, claimTemplate, slice).
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				listCalls++
				return c.List(ctx, list, opts...)
			},
		}).Build()

	checker := NewChecker(cl, &CELCache{}, true, SnapshotState{}).WrapNodeFeasibilityEvaluator(&passthroughChecker{})
	requirements := &simulator.PodRequirements{
		PodTemplate: &corev1.PodTemplateSpec{
			Namespace: "default",
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "c", Image: "busybox"}},
				ResourceClaims: []corev1.PodResourceClaim{
					{Name: "gpu", ResourceClaimTemplateName: new("gpu-template")},
				},
			},
		},
	}

	const calls = 10
	for range calls {
		stats := &simulator.NodeExclusionStats{}
		candidateSeq := func(yield func(simulator.Candidate) bool) {
			yield(&testCandidate{node: node, id: "gpu-node"})
		}
		if _, err := checker.FindFeasibleNodes(t.Context(), candidateSeq, requirements, stats); err != nil {
			t.Fatalf("FindFeasibleNodes returned error: %v", err)
		}
	}

	// ResourceSlices, DeviceTaintRules, ResourceClaims and DeviceClasses, once for
	// the snapshot. The DeviceTaintRule list is skipped when KueueDRAIntegrationDeviceTaints or its
	// Kubernetes gate is off.
	const wantListCalls = 4
	if listCalls != wantListCalls {
		t.Errorf("cluster-wide List calls over %d assignment attempts = %d, want %d", calls, listCalls, wantListCalls)
	}
}

func TestCELCacheIsSharedAcrossCheckers(t *testing.T) {
	cl := fake.NewClientBuilder().Build()
	shared := &CELCache{}

	// A Checker is built per scheduling cycle, so two of them stand for two cycles.
	first := NewChecker(cl, shared, true, SnapshotState{}).celCache.get()
	second := NewChecker(cl, shared, true, SnapshotState{}).celCache.get()
	if first != second {
		t.Error("the shared CELCache compiled a second cache, so selectors are not reused across cycles")
	}
	if first == nil {
		t.Fatal("CELCache.get() = nil, want a compiled cache")
	}

	if other := (&CELCache{}).get(); other == first {
		t.Error("two CELCaches returned the same cache, so the value is not per-CELCache")
	}
}

func TestCheckerReusesCounts(t *testing.T) {
	podTemplate := &corev1.PodTemplateSpec{
		Namespace: "default",
		Spec: corev1.PodSpec{
			Containers:     []corev1.Container{{Name: "c", Image: "busybox"}},
			ResourceClaims: []corev1.PodResourceClaim{{Name: "gpu", ResourceClaimTemplateName: new("gpu-template")}},
		},
	}
	onePod := DevicePlacement{PodTemplate: podTemplate, NodeCounts: map[string]int32{"gpu-node": 1}}
	// An admitted Workload holds one of the three GPUs, which a check of the empty
	// cluster gives back for its duration.
	held := utiltestingapi.MakeWorkload("held", "default").
		PodSets(*utiltestingapi.MakePodSet("main", 1).PodSpec(podTemplate.Spec).Obj()).
		ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").PodSets(utiltestingapi.MakePodSetAssignment("main").Count(1).Obj()).Obj(), time.Now()).
		Obj()
	heldPod := testingpod.MakePod("held-0", "default").
		UID("held-0").
		Annotation(kueue.WorkloadAnnotation, "held").
		NodeName("gpu-node").
		Obj()

	// The first check counts 2 Pods, and 1 next to a leader. The test then marks every
	// count that found all that fit as 4, so a second check that gives 4 read its counts
	// from the cache.
	cases := map[string]struct {
		leader bool
		// firstPodCount is how many Pods the first check asks for, 4 when unset.
		firstPodCount int32
		// What the snapshot does between the two checks.
		checkEmptyCluster bool
		checkFit          []DevicePlacement
		reserve           []DevicePlacement
		// removeHeld removes the held Workload, as a preemption simulation does.
		removeHeld bool
		wantLimit  simulator.DevicePodLimit
	}{
		"nothing between: reused": {
			wantLimit: simulator.DevicePodLimit{Pods: 4, PodsWithLeader: 4},
		},
		"next to a leader: reused": {
			leader:    true,
			wantLimit: simulator.DevicePodLimit{Pods: 4, PodsWithLeader: 4},
		},
		"after a check of the empty cluster: reused": {
			checkEmptyCluster: true,
			wantLimit:         simulator.DevicePodLimit{Pods: 4, PodsWithLeader: 4},
		},
		"after a check that Pods fit: reused": {
			checkFit:  []DevicePlacement{onePod},
			wantLimit: simulator.DevicePodLimit{Pods: 4, PodsWithLeader: 4},
		},
		"after a Pod takes a device on the node: counted again": {
			reserve:   []DevicePlacement{onePod},
			wantLimit: simulator.DevicePodLimit{Pods: 1, PodsWithLeader: 1},
		},
		"after a preemption simulation gives a Workload's devices back: counted again": {
			removeHeld: true,
			wantLimit:  simulator.DevicePodLimit{Pods: 3, PodsWithLeader: 3},
		},
		"a count that stopped at fewer Pods than asked now: counted again": {
			firstPodCount: 1,
			wantLimit:     simulator.DevicePodLimit{Pods: 2, PodsWithLeader: 2},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			_ = corev1.AddToScheme(scheme)
			_ = resourceapi.AddToScheme(scheme)
			node := testingnode.MakeNode("gpu-node").Obj()
			cl := fake.NewClientBuilder().WithScheme(scheme).
				WithRuntimeObjects(
					node,
					testingdra.MakeDeviceClass("gpu.example.com").Obj(),
					utiltesting.MakeResourceClaimTemplate("gpu-template", "default").
						DeviceRequest("gpu", "gpu.example.com", 1).
						Obj(),
					testingdra.MakeResourceSlice("gpu-node-slice", "gpu.example.com").
						NodeName("gpu-node").
						Pool("gpu-node", 1, 1).
						Device("gpu-0").
						Device("gpu-1").
						Device("gpu-2").
						Obj(),
					utiltesting.MakeResourceClaim("held-0-gpu", "default").
						DeviceRequest("gpu", "gpu.example.com", 1).
						Allocated("gpu", "gpu.example.com", "gpu-node", "gpu-0").
						ReservedFor("held-0").
						Obj(),
				).
				Build()
			pods := &podtracker.Tracker{}
			pods.Track(heldPod)
			checker := NewChecker(cl, &CELCache{}, true, SnapshotState{
				Nodes:            []*corev1.Node{node},
				Pods:             pods,
				AssumedWorkloads: []*kueue.Workload{held},
			})
			checkerNodeEvaluator := checker.WrapNodeFeasibilityEvaluator(&passthroughChecker{})
			requirements := &simulator.PodRequirements{PodTemplate: podTemplate, PodCount: 4}
			if tc.leader {
				requirements.LeaderPodTemplate = podTemplate
			}

			first := *requirements
			if tc.firstPodCount != 0 {
				first.PodCount = tc.firstPodCount
			}
			candidates := []*testCandidate{{node: node, id: "gpu-node"}}
			if _, err := checkerNodeEvaluator.FindFeasibleNodes(t.Context(), simulator.AsCandidates(slices.Values(candidates)), &first, &simulator.NodeExclusionStats{}); err != nil {
				t.Fatalf("FindFeasibleNodes returned error: %v", err)
			}
			devices := checker.devices.built()
			for key, entry := range devices.counts {
				if entry.exact {
					entry.fit = 4
					devices.counts[key] = entry
				}
			}

			if tc.checkEmptyCluster {
				empty := &simulator.PodRequirements{PodTemplate: podTemplate, PodCount: 4, SimulateEmpty: true}
				if _, err := checkerNodeEvaluator.FindFeasibleNodes(
					t.Context(),
					simulator.AsCandidates(slices.Values([]*testCandidate{{node: node, id: "gpu-node"}})),
					empty,
					&simulator.NodeExclusionStats{},
				); err != nil {
					t.Fatalf("FindFeasibleNodes of the empty cluster returned error: %v", err)
				}
			}
			if tc.checkFit != nil {
				if fits, err := checker.PodsFit(t.Context(), "default/other", nil, tc.checkFit); err != nil || !fits {
					t.Fatalf("PodsFit = %v, %v, want true", fits, err)
				}
			}
			if tc.removeHeld {
				checker.RemoveWorkload("default/held")
			}
			for _, placement := range tc.reserve {
				if _, ok, err := checker.ReservePods(t.Context(), placement); err != nil || !ok {
					t.Fatalf("ReservePods = %v, %v, want ok", ok, err)
				}
			}

			candidates = []*testCandidate{{node: node, id: "gpu-node"}}
			if _, err := checkerNodeEvaluator.FindFeasibleNodes(t.Context(), simulator.AsCandidates(slices.Values(candidates)), requirements, &simulator.NodeExclusionStats{}); err != nil {
				t.Fatalf("FindFeasibleNodes returned error: %v", err)
			}
			if diff := cmp.Diff(tc.wantLimit, candidates[0].devicePodLimit); diff != "" {
				t.Errorf("device Pod limit (-want,+got):\n%s", diff)
			}
		})
	}
}
