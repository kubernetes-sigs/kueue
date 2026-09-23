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
	"testing"

	corev1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"sigs.k8s.io/kueue/pkg/cache/scheduler/simulator"
)

func TestCheckerListsClusterStateOncePerSnapshot(t *testing.T) {
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
	slice := &resourceapi.ResourceSlice{
		Name: "gpu-node-slice",
		Spec: resourceapi.ResourceSliceSpec{
			NodeName: new("gpu-node"),
			Driver:   "gpu.example.com",
			Pool:     resourceapi.ResourcePool{Name: "gpu-node", ResourceSliceCount: 1},
			Devices:  []resourceapi.Device{{Name: "gpu-0"}},
		},
	}

	var listCalls int
	cl := fake.NewClientBuilder().WithScheme(scheme).
		WithRuntimeObjects(node, deviceClass, claimTemplate, slice).
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				listCalls++
				return c.List(ctx, list, opts...)
			},
		}).Build()

	checker := NewChecker(&passthroughChecker{}, cl, &CELCache{})
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

	// ResourceSlices, ResourceClaims and DeviceClasses, once for the snapshot.
	const wantListCalls = 3
	if listCalls != wantListCalls {
		t.Errorf("cluster-wide List calls over %d assignment attempts = %d, want %d", calls, listCalls, wantListCalls)
	}
}

func TestCELCacheIsSharedAcrossCheckers(t *testing.T) {
	cl := fake.NewClientBuilder().Build()
	shared := &CELCache{}

	// A Checker is built per scheduling cycle, so two of them stand for two cycles.
	first := NewChecker(&passthroughChecker{}, cl, shared).celCache.get()
	second := NewChecker(&passthroughChecker{}, cl, shared).celCache.get()
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
