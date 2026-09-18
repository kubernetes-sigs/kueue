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

package simulator

import (
	"context"
	"iter"
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
)

type testCandidate struct {
	node          *corev1.Node
	id            utiltas.TopologyDomainID
	affinityScore int64
}

func (c *testCandidate) GetNode() *corev1.Node           { return c.node }
func (c *testCandidate) GetID() utiltas.TopologyDomainID { return c.id }
func (c *testCandidate) GetAffinityScore() int64         { return c.affinityScore }
func (c *testCandidate) SetAffinityScore(score int64)    { c.affinityScore = score }

type passthroughChecker struct{}

func (p *passthroughChecker) Simulate(_ context.Context, fn func()) error {
	fn()
	return nil
}

func (p *passthroughChecker) PreemptWorkload(_ context.Context, _ client.ObjectKey) (func() error, error) {
	return func() error { return nil }, nil
}

func (p *passthroughChecker) FindFeasibleNodes(_ context.Context, candidates iter.Seq[Candidate], _ *PodRequirements, stats *NodeExclusionStats) ([]MatchedCandidate, error) {
	var result []MatchedCandidate
	for c := range candidates {
		mc := c.(MatchedCandidate)
		stats.TotalNodes++
		result = append(result, mc)
	}
	return result, nil
}

func TestDRACheckerFindFeasibleNodes(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.KueueDRAIntegrationExtendedResource, true)
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = resourceapi.AddToScheme(scheme)

	gpuNode := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "gpu-node"},
	}
	cpuNode := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "cpu-node"},
	}

	gpuDeviceClass := &resourceapi.DeviceClass{
		ObjectMeta: metav1.ObjectMeta{Name: "gpu.example.com"},
	}
	// Backs an extended resource rather than being named by a claim, so kube-scheduler
	// creates the claim itself once the Pod is scheduled. It carries no selectors, so it
	// draws from the same devices as the class above.
	gpuExtendedClass := &resourceapi.DeviceClass{
		ObjectMeta: metav1.ObjectMeta{Name: "gpu-extended.example.com"},
		Spec: resourceapi.DeviceClassSpec{
			ExtendedResourceName: new("example.com/gpu"),
		},
	}
	extendedGPU := func(count string) corev1.ResourceList {
		return corev1.ResourceList{"example.com/gpu": resource.MustParse(count)}
	}

	gpuClaimTemplate := &resourceapi.ResourceClaimTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "gpu-template", Namespace: "default"},
		Spec: resourceapi.ResourceClaimTemplateSpec{
			Spec: resourceapi.ResourceClaimSpec{
				Devices: resourceapi.DeviceClaim{
					Requests: []resourceapi.DeviceRequest{
						{
							Name: "gpu",
							Exactly: &resourceapi.ExactDeviceRequest{
								DeviceClassName: "gpu.example.com",
								AllocationMode:  resourceapi.DeviceAllocationModeExactCount,
								Count:           1,
							},
						},
					},
				},
			},
		},
	}
	gpuSlice := &resourceapi.ResourceSlice{
		ObjectMeta: metav1.ObjectMeta{Name: "gpu-node-slice"},
		Spec: resourceapi.ResourceSliceSpec{
			Driver:   "gpu.example.com",
			NodeName: new("gpu-node"),
			Pool: resourceapi.ResourcePool{
				Name:               "gpu-pool",
				Generation:         1,
				ResourceSliceCount: 1,
			},
			Devices: []resourceapi.Device{
				{Name: "gpu-0"},
				{Name: "gpu-1"},
			},
		},
	}

	// A device that only binds once a condition reports True. kube-scheduler can
	// still select it, so the simulation has to as well.
	bindingNode := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "binding-node"},
	}
	bindingSlice := &resourceapi.ResourceSlice{
		ObjectMeta: metav1.ObjectMeta{Name: "binding-node-slice"},
		Spec: resourceapi.ResourceSliceSpec{
			Driver:   "gpu.example.com",
			NodeName: new("binding-node"),
			Pool: resourceapi.ResourcePool{
				Name:               "binding-pool",
				Generation:         1,
				ResourceSliceCount: 1,
			},
			Devices: []resourceapi.Device{{
				Name:              "gpu-0",
				BindingConditions: []string{"example.com/device-ready"},
			}},
		},
	}

	gpuClaim := &resourceapi.ResourceClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "existing-gpu-claim", Namespace: "default"},
		Spec: resourceapi.ResourceClaimSpec{
			Devices: resourceapi.DeviceClaim{
				Requests: []resourceapi.DeviceRequest{
					{
						Name: "gpu",
						Exactly: &resourceapi.ExactDeviceRequest{
							DeviceClassName: "gpu.example.com",
							AllocationMode:  resourceapi.DeviceAllocationModeExactCount,
							Count:           1,
						},
					},
				},
			},
		},
	}

	tests := map[string]struct {
		objects      []runtime.Object
		podTemplate  *corev1.PodTemplateSpec
		candidates   []*testCandidate
		wantFeasible []string
		// wantDRANoFit is how many nodes the device check excluded. It reaches the
		// user as the draNoFit count in the Workload's message.
		wantDRANoFit int
		wantErr      bool
	}{
		"non-DRA pod passes through all nodes": {
			objects: []runtime.Object{gpuSlice, gpuDeviceClass},
			podTemplate: &corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "c", Image: "busybox"}},
				},
			},
			candidates: []*testCandidate{
				{node: gpuNode, id: "gpu-node"},
				{node: cpuNode, id: "cpu-node"},
			},
			wantFeasible: []string{"gpu-node", "cpu-node"},
		},
		"extended resource pod filters out nodes without matching devices": {
			objects: []runtime.Object{gpuSlice, gpuExtendedClass},
			podTemplate: &corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Namespace: "default"},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:      "c",
						Image:     "busybox",
						Resources: corev1.ResourceRequirements{Requests: extendedGPU("1")},
					}},
				},
			},
			candidates: []*testCandidate{
				{node: gpuNode, id: "gpu-node"},
				{node: cpuNode, id: "cpu-node"},
			},
			wantFeasible: []string{"gpu-node"},
			wantDRANoFit: 1,
		},
		"extended resource beyond what any node holds excludes every node": {
			objects: []runtime.Object{gpuSlice, gpuExtendedClass},
			podTemplate: &corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Namespace: "default"},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:      "c",
						Image:     "busybox",
						Resources: corev1.ResourceRequirements{Requests: extendedGPU("4")},
					}},
				},
			},
			candidates: []*testCandidate{
				{node: gpuNode, id: "gpu-node"},
				{node: cpuNode, id: "cpu-node"},
			},
			wantDRANoFit: 2,
		},
		"an extended resource no DeviceClass backs is left to the node filters": {
			objects: []runtime.Object{gpuSlice},
			podTemplate: &corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Namespace: "default"},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:      "c",
						Image:     "busybox",
						Resources: corev1.ResourceRequirements{Requests: extendedGPU("1")},
					}},
				},
			},
			candidates: []*testCandidate{
				{node: gpuNode, id: "gpu-node"},
				{node: cpuNode, id: "cpu-node"},
			},
			wantFeasible: []string{"gpu-node", "cpu-node"},
		},
		"a plain init container raises the count rather than adding to it": {
			objects: []runtime.Object{gpuSlice, gpuExtendedClass},
			podTemplate: &corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Namespace: "default"},
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{{
						Name:      "setup",
						Image:     "busybox",
						Resources: corev1.ResourceRequirements{Requests: extendedGPU("2")},
					}},
					Containers: []corev1.Container{{
						Name:      "c",
						Image:     "busybox",
						Resources: corev1.ResourceRequirements{Requests: extendedGPU("2")},
					}},
				},
			},
			candidates: []*testCandidate{
				{node: gpuNode, id: "gpu-node"},
				{node: cpuNode, id: "cpu-node"},
			},
			wantFeasible: []string{"gpu-node"},
			wantDRANoFit: 1,
		},
		"a plain init container is counted alongside the sidecars that precede it": {
			objects: []runtime.Object{gpuSlice, gpuExtendedClass},
			podTemplate: &corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Namespace: "default"},
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{
						{
							Name:          "sidecar",
							Image:         "busybox",
							RestartPolicy: new(corev1.ContainerRestartPolicyAlways),
							Resources:     corev1.ResourceRequirements{Requests: extendedGPU("1")},
						},
						{
							Name:      "setup",
							Image:     "busybox",
							Resources: corev1.ResourceRequirements{Requests: extendedGPU("2")},
						},
					},
					Containers: []corev1.Container{{Name: "c", Image: "busybox"}},
				},
			},
			candidates: []*testCandidate{
				{node: gpuNode, id: "gpu-node"},
				{node: cpuNode, id: "cpu-node"},
			},
			wantDRANoFit: 2,
		},
		"a sidecar's devices add to the Pod's total": {
			objects: []runtime.Object{gpuSlice, gpuExtendedClass},
			podTemplate: &corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Namespace: "default"},
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{{
						Name:          "sidecar",
						Image:         "busybox",
						RestartPolicy: new(corev1.ContainerRestartPolicyAlways),
						Resources:     corev1.ResourceRequirements{Requests: extendedGPU("2")},
					}},
					Containers: []corev1.Container{{
						Name:      "c",
						Image:     "busybox",
						Resources: corev1.ResourceRequirements{Requests: extendedGPU("1")},
					}},
				},
			},
			candidates: []*testCandidate{
				{node: gpuNode, id: "gpu-node"},
				{node: cpuNode, id: "cpu-node"},
			},
			wantDRANoFit: 2,
		},
		"nil PodTemplate passes through": {
			objects:     []runtime.Object{gpuSlice, gpuDeviceClass},
			podTemplate: nil,
			candidates: []*testCandidate{
				{node: gpuNode, id: "gpu-node"},
			},
			wantFeasible: []string{"gpu-node"},
		},
		"DRA pod filters out nodes without matching devices": {
			objects: []runtime.Object{gpuSlice, gpuDeviceClass, gpuClaimTemplate},
			podTemplate: &corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Namespace: "default"},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "c", Image: "busybox"}},
					ResourceClaims: []corev1.PodResourceClaim{
						{
							Name:                      "gpu",
							ResourceClaimTemplateName: new("gpu-template"),
						},
					},
				},
			},
			candidates: []*testCandidate{
				{node: gpuNode, id: "gpu-node"},
				{node: cpuNode, id: "cpu-node"},
			},
			wantFeasible: []string{"gpu-node"},
			wantDRANoFit: 1,
		},
		"missing ResourceClaimTemplate returns error": {
			objects: []runtime.Object{gpuSlice, gpuDeviceClass},
			podTemplate: &corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Namespace: "default"},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "c", Image: "busybox"}},
					ResourceClaims: []corev1.PodResourceClaim{
						{
							Name:                      "gpu",
							ResourceClaimTemplateName: new("nonexistent-template"),
						},
					},
				},
			},
			candidates: []*testCandidate{
				{node: gpuNode, id: "gpu-node"},
			},
			wantErr: true,
		},
		"missing ResourceClaimName returns error": {
			objects: []runtime.Object{gpuSlice, gpuDeviceClass},
			podTemplate: &corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Namespace: "default"},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "c", Image: "busybox"}},
					ResourceClaims: []corev1.PodResourceClaim{
						{
							Name:              "gpu",
							ResourceClaimName: new("nonexistent-claim"),
						},
					},
				},
			},
			candidates: []*testCandidate{
				{node: gpuNode, id: "gpu-node"},
			},
			wantErr: true,
		},
		"candidate without a node is reported, not silently admitted": {
			objects: []runtime.Object{gpuSlice, gpuDeviceClass, gpuClaimTemplate},
			podTemplate: &corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Namespace: "default"},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "c", Image: "busybox"}},
					ResourceClaims: []corev1.PodResourceClaim{
						{
							Name:                      "gpu",
							ResourceClaimTemplateName: new("gpu-template"),
						},
					},
				},
			},
			candidates: []*testCandidate{
				{node: nil, id: "no-node"},
				{node: gpuNode, id: "gpu-node"},
				{node: cpuNode, id: "cpu-node"},
			},
			wantErr: true,
		},
		"multiple claims on one pod": {
			objects: []runtime.Object{
				gpuSlice, gpuDeviceClass, gpuClaimTemplate,
				&resourceapi.ResourceClaimTemplate{
					ObjectMeta: metav1.ObjectMeta{Name: "gpu-template-2", Namespace: "default"},
					Spec: resourceapi.ResourceClaimTemplateSpec{
						Spec: resourceapi.ResourceClaimSpec{
							Devices: resourceapi.DeviceClaim{
								Requests: []resourceapi.DeviceRequest{
									{
										Name: "gpu2",
										Exactly: &resourceapi.ExactDeviceRequest{
											DeviceClassName: "gpu.example.com",
											AllocationMode:  resourceapi.DeviceAllocationModeExactCount,
											Count:           1,
										},
									},
								},
							},
						},
					},
				},
			},
			podTemplate: &corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Namespace: "default"},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "c", Image: "busybox"}},
					ResourceClaims: []corev1.PodResourceClaim{
						{
							Name:                      "gpu",
							ResourceClaimTemplateName: new("gpu-template"),
						},
						{
							Name:                      "gpu2",
							ResourceClaimTemplateName: new("gpu-template-2"),
						},
					},
				},
			},
			candidates: []*testCandidate{
				{node: gpuNode, id: "gpu-node"},
			},
			wantFeasible: []string{"gpu-node"},
		},
		"device with binding conditions stays feasible": {
			objects: []runtime.Object{bindingSlice, gpuDeviceClass, gpuClaimTemplate},
			podTemplate: &corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Namespace: "default"},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "c", Image: "busybox"}},
					ResourceClaims: []corev1.PodResourceClaim{
						{
							Name:                      "gpu",
							ResourceClaimTemplateName: new("gpu-template"),
						},
					},
				},
			},
			candidates: []*testCandidate{
				{node: bindingNode, id: "binding-node"},
				{node: cpuNode, id: "cpu-node"},
			},
			wantFeasible: []string{"binding-node"},
			wantDRANoFit: 1,
		},
		// A device held only through admin access is not consumed, so it stays
		// available to everyone else.
		"devices held through admin access are still available": {
			objects: []runtime.Object{
				gpuSlice, gpuDeviceClass, gpuClaimTemplate,
				&resourceapi.ResourceClaim{
					ObjectMeta: metav1.ObjectMeta{Name: "admin-claim", Namespace: "other"},
					Status: resourceapi.ResourceClaimStatus{
						Allocation: &resourceapi.AllocationResult{
							Devices: resourceapi.DeviceAllocationResult{
								Results: []resourceapi.DeviceRequestAllocationResult{
									{Request: "gpu", Driver: "gpu.example.com", Pool: "gpu-pool", Device: "gpu-0", AdminAccess: new(true)},
									{Request: "gpu", Driver: "gpu.example.com", Pool: "gpu-pool", Device: "gpu-1", AdminAccess: new(true)},
								},
							},
						},
					},
				},
			},
			podTemplate: &corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Namespace: "default"},
				Spec: corev1.PodSpec{
					Containers:     []corev1.Container{{Name: "c", Image: "busybox"}},
					ResourceClaims: []corev1.PodResourceClaim{{Name: "gpu", ResourceClaimTemplateName: new("gpu-template")}},
				},
			},
			candidates: []*testCandidate{
				{node: gpuNode, id: "gpu-node"},
			},
			wantFeasible: []string{"gpu-node"},
		},
		// A shared device is consumed by share, not as a whole device, so it is not
		// exhausted by one holder.
		"a shared device is not counted as a whole device": {
			objects: []runtime.Object{
				gpuSlice, gpuDeviceClass, gpuClaimTemplate,
				&resourceapi.ResourceClaim{
					ObjectMeta: metav1.ObjectMeta{Name: "shared-claim", Namespace: "other"},
					Status: resourceapi.ResourceClaimStatus{
						Allocation: &resourceapi.AllocationResult{
							Devices: resourceapi.DeviceAllocationResult{
								Results: []resourceapi.DeviceRequestAllocationResult{
									{Request: "gpu", Driver: "gpu.example.com", Pool: "gpu-pool", Device: "gpu-0", ShareID: new(types.UID("share-1"))},
									{Request: "gpu", Driver: "gpu.example.com", Pool: "gpu-pool", Device: "gpu-1", ShareID: new(types.UID("share-1"))},
								},
							},
						},
					},
				},
			},
			podTemplate: &corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Namespace: "default"},
				Spec: corev1.PodSpec{
					Containers:     []corev1.Container{{Name: "c", Image: "busybox"}},
					ResourceClaims: []corev1.PodResourceClaim{{Name: "gpu", ResourceClaimTemplateName: new("gpu-template")}},
				},
			},
			candidates: []*testCandidate{
				{node: gpuNode, id: "gpu-node"},
			},
			wantFeasible: []string{"gpu-node"},
		},
		// A pre-provisioned claim that is already allocated needs no second device:
		// buildAllocatedState already counts the one it holds.
		// The allocation says where it lives, so the Pod cannot go elsewhere.
		"DRA pod with all devices allocated filters out all nodes": {
			objects: []runtime.Object{
				gpuSlice, gpuDeviceClass, gpuClaimTemplate,
				&resourceapi.ResourceClaim{
					ObjectMeta: metav1.ObjectMeta{Name: "existing-claim", Namespace: "other"},
					Spec: resourceapi.ResourceClaimSpec{
						Devices: resourceapi.DeviceClaim{
							Requests: []resourceapi.DeviceRequest{
								{
									Name: "gpu",
									Exactly: &resourceapi.ExactDeviceRequest{
										DeviceClassName: "gpu.example.com",
										Count:           1,
									},
								},
							},
						},
					},
					Status: resourceapi.ResourceClaimStatus{
						Allocation: &resourceapi.AllocationResult{
							Devices: resourceapi.DeviceAllocationResult{
								Results: []resourceapi.DeviceRequestAllocationResult{
									{Request: "gpu", Driver: "gpu.example.com", Pool: "gpu-pool", Device: "gpu-0"},
									{Request: "gpu", Driver: "gpu.example.com", Pool: "gpu-pool", Device: "gpu-1"},
								},
							},
						},
					},
				},
			},
			podTemplate: &corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Namespace: "default"},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "c", Image: "busybox"}},
					ResourceClaims: []corev1.PodResourceClaim{
						{
							Name:                      "gpu",
							ResourceClaimTemplateName: new("gpu-template"),
						},
					},
				},
			},
			candidates: []*testCandidate{
				{node: gpuNode, id: "gpu-node"},
			},
			wantFeasible: nil,
			wantDRANoFit: 1,
		},
		"a direct ResourceClaim reference is refused rather than skipped": {
			// KueueDRAIntegration rejects these Workloads before scheduling, so this
			// path is unreachable. Failing loudly keeps a broken guarantee visible
			// instead of reporting every node as feasible.
			objects: []runtime.Object{gpuSlice, gpuDeviceClass, gpuClaim},
			podTemplate: &corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Namespace: "default"},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "c", Image: "busybox"}},
					ResourceClaims: []corev1.PodResourceClaim{
						{Name: "gpu", ResourceClaimName: new("existing-gpu-claim")},
					},
				},
			},
			candidates: []*testCandidate{
				{node: gpuNode, id: "gpu-node"},
			},
			wantErr: true,
		},
		"PodResourceClaim with neither name nor template is skipped": {
			objects: []runtime.Object{gpuSlice, gpuDeviceClass},
			podTemplate: &corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Namespace: "default"},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "c", Image: "busybox"}},
					ResourceClaims: []corev1.PodResourceClaim{
						{Name: "empty"},
					},
				},
			},
			candidates: []*testCandidate{
				{node: gpuNode, id: "gpu-node"},
				{node: cpuNode, id: "cpu-node"},
			},
			wantFeasible: []string{"gpu-node", "cpu-node"},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			cl := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(tc.objects...).
				WithIndex(&resourceapi.DeviceClass{}, indexer.DeviceClassExtendedResourceNameIndex,
					indexer.IndexDeviceClassExtendedResourceName).
				Build()
			inner := &passthroughChecker{}
			checker := NewDRAChecker(inner, cl)

			candidateSeq := func(yield func(Candidate) bool) {
				for _, c := range tc.candidates {
					if !yield(c) {
						return
					}
				}
			}

			stats := &NodeExclusionStats{}
			feasible, err := checker.FindFeasibleNodes(t.Context(), candidateSeq, &PodRequirements{PodTemplate: tc.podTemplate}, stats)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error but got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("FindFeasibleNodes returned error: %v", err)
			}

			var gotNames []string
			for _, mc := range feasible {
				if mc.GetNode() != nil {
					gotNames = append(gotNames, mc.GetNode().Name)
				} else {
					gotNames = append(gotNames, string(mc.GetID()))
				}
			}

			if diff := cmp.Diff(tc.wantFeasible, gotNames); diff != "" {
				t.Errorf("feasible nodes mismatch (-want +got):\n%s", diff)
			}
			if stats.DRANoFit != tc.wantDRANoFit {
				t.Errorf("stats.DRANoFit = %d, want %d", stats.DRANoFit, tc.wantDRANoFit)
			}
		})
	}
}

// The allocator belongs to the snapshot, not the call: repeated attempts must not
// repeat the cluster-wide Lists it is built from.
func TestDRACheckerListsClusterStateOncePerSnapshot(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = resourceapi.AddToScheme(scheme)

	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "gpu-node"}}
	deviceClass := &resourceapi.DeviceClass{ObjectMeta: metav1.ObjectMeta{Name: "gpu.example.com"}}
	claimTemplate := &resourceapi.ResourceClaimTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "gpu-template", Namespace: "default"},
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
		ObjectMeta: metav1.ObjectMeta{Name: "gpu-node-slice"},
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

	checker := NewDRAChecker(&passthroughChecker{}, cl)
	requirements := &PodRequirements{
		PodTemplate: &corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Namespace: "default"},
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
		stats := &NodeExclusionStats{}
		candidateSeq := func(yield func(Candidate) bool) {
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
