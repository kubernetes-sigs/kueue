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
	"iter"
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/component-base/featuregate"
	kubefeatures "k8s.io/kubernetes/pkg/features"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"sigs.k8s.io/kueue/pkg/cache/scheduler/simulator"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
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

func (p *passthroughChecker) FindFeasibleNodes(
	_ context.Context,
	candidates iter.Seq[simulator.Candidate],
	_ *simulator.PodRequirements,
	stats *simulator.NodeExclusionStats,
) ([]simulator.MatchedCandidate, error) {
	var result []simulator.MatchedCandidate
	for c := range candidates {
		mc := c.(simulator.MatchedCandidate)
		stats.TotalNodes++
		result = append(result, mc)
	}
	return result, nil
}

func TestCheckerFindFeasibleNodes(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.KueueDRAIntegrationExtendedResource, true)
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = resourceapi.AddToScheme(scheme)

	gpuNode := &corev1.Node{
		Name: "gpu-node",
	}
	cpuNode := &corev1.Node{
		Name: "cpu-node",
	}

	gpuDeviceClass := &resourceapi.DeviceClass{
		Name: "gpu.example.com",
	}
	// Backs an extended resource rather than being named by a claim, so kube-scheduler
	// creates the claim itself once the Pod is scheduled. It carries no selectors, so it
	// draws from the same devices as the class above.
	gpuExtendedClass := &resourceapi.DeviceClass{
		Name: "gpu-extended.example.com",
		Spec: resourceapi.DeviceClassSpec{
			ExtendedResourceName: new("example.com/gpu"),
		},
	}
	extendedGPU := func(count string) corev1.ResourceList {
		return corev1.ResourceList{"example.com/gpu": resource.MustParse(count)}
	}

	gpuClaimTemplate := &resourceapi.ResourceClaimTemplate{
		Name: "gpu-template", Namespace: "default",
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
		Name: "gpu-node-slice",
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
		Name: "binding-node",
	}
	bindingSlice := &resourceapi.ResourceSlice{
		Name: "binding-node-slice",
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
		Name: "existing-gpu-claim", Namespace: "default",
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

	// Tolerates the taint that the DeviceTaintRule cases apply, so the same devices
	// stay allocatable for it.
	tolerantTemplate := utiltesting.MakeResourceClaimTemplate("tolerant-template", "default").
		DeviceRequest("gpu", "gpu.example.com", 1).
		WithToleration("example.com/maintenance", resourceapi.DeviceTaintEffectNoSchedule).
		Obj()

	tests := map[string]struct {
		objects      []runtime.Object
		podTemplate  *corev1.PodTemplateSpec
		candidates   []*testCandidate
		wantFeasible []string
		// wantDRANoFit is how many nodes the device check excluded. It reaches the
		// user as the draNoFit count in the Workload's message.
		wantDRANoFit int
		wantErr      bool
		featureGates map[featuregate.Feature]bool
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
				Namespace: "default",
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
		"a DeviceClass is reachable by its implicit extended resource name": {
			// Every DeviceClass carries deviceclass.resource.kubernetes.io/<name>
			// whether or not it declares an extendedResourceName, so a Pod asking for
			// the implicit form is DRA-backed and has to be checked.
			objects: []runtime.Object{gpuSlice, gpuDeviceClass},
			podTemplate: &corev1.PodTemplateSpec{
				Namespace: "default",
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  "c",
						Image: "busybox",
						Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
							"deviceclass.resource.kubernetes.io/gpu.example.com": resource.MustParse("1"),
						}},
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
				Namespace: "default",
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
				Namespace: "default",
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
				Namespace: "default",
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
				Namespace: "default",
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
				Namespace: "default",
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
				Namespace: "default",
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
				Namespace: "default",
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
				Namespace: "default",
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
				Namespace: "default",
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
					Name: "gpu-template-2", Namespace: "default",
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
				Namespace: "default",
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
				Namespace: "default",
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
					Name: "admin-claim", Namespace: "other",
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
				Namespace: "default",
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
					Name: "shared-claim", Namespace: "other",
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
				Namespace: "default",
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
					Name: "existing-claim", Namespace: "other",
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
				Namespace: "default",
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
				Namespace: "default",
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
		"a DeviceTaintRule makes the devices it selects unusable": {
			objects: []runtime.Object{gpuSlice, gpuDeviceClass, gpuClaimTemplate,
				utiltesting.MakeDeviceTaintRule("maintenance", "example.com/maintenance").
					Driver("gpu.example.com").Obj()},
			podTemplate: &corev1.PodTemplateSpec{
				Namespace: "default",
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "c", Image: "busybox"}},
					ResourceClaims: []corev1.PodResourceClaim{
						{Name: "gpu", ResourceClaimTemplateName: new("gpu-template")},
					},
				},
			},
			candidates:   []*testCandidate{{node: gpuNode, id: "gpu-node"}},
			wantDRANoFit: 1,
		},
		"a request tolerating the taint still fits": {
			objects: []runtime.Object{gpuSlice, gpuDeviceClass, tolerantTemplate,
				utiltesting.MakeDeviceTaintRule("maintenance", "example.com/maintenance").
					Driver("gpu.example.com").Obj()},
			podTemplate: &corev1.PodTemplateSpec{
				Namespace: "default",
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "c", Image: "busybox"}},
					ResourceClaims: []corev1.PodResourceClaim{
						{Name: "gpu", ResourceClaimTemplateName: new("tolerant-template")},
					},
				},
			},
			candidates:   []*testCandidate{{node: gpuNode, id: "gpu-node"}},
			wantFeasible: []string{"gpu-node"},
		},
		"a DeviceTaintRule naming one device leaves the rest allocatable": {
			objects: []runtime.Object{gpuSlice, gpuDeviceClass, gpuClaimTemplate,
				utiltesting.MakeDeviceTaintRule("one-device", "example.com/maintenance").
					Driver("gpu.example.com").Device("gpu-0").Obj()},
			podTemplate: &corev1.PodTemplateSpec{
				Namespace: "default",
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "c", Image: "busybox"}},
					ResourceClaims: []corev1.PodResourceClaim{
						{Name: "gpu", ResourceClaimTemplateName: new("gpu-template")},
					},
				},
			},
			candidates:   []*testCandidate{{node: gpuNode, id: "gpu-node"}},
			wantFeasible: []string{"gpu-node"},
		},
		"a NoExecute DeviceTaintRule keeps the devices out too": {
			// The allocator refuses NoExecute and NoSchedule alike, so a rule meant to
			// drain running Pods also stops Kueue admitting new ones onto the device.
			objects: []runtime.Object{gpuSlice, gpuDeviceClass, gpuClaimTemplate,
				utiltesting.MakeDeviceTaintRule("draining", "example.com/maintenance").
					Driver("gpu.example.com").
					Effect(resourceapi.DeviceTaintEffectNoExecute).Obj()},
			podTemplate: &corev1.PodTemplateSpec{
				Namespace: "default",
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "c", Image: "busybox"}},
					ResourceClaims: []corev1.PodResourceClaim{
						{Name: "gpu", ResourceClaimTemplateName: new("gpu-template")},
					},
				},
			},
			candidates:   []*testCandidate{{node: gpuNode, id: "gpu-node"}},
			wantDRANoFit: 1,
		},
		"DeviceTaintRules do not apply when the Kubernetes gate is off": {
			objects: []runtime.Object{gpuSlice, gpuDeviceClass, gpuClaimTemplate,
				utiltesting.MakeDeviceTaintRule("maintenance", "example.com/maintenance").
					Driver("gpu.example.com").Obj()},
			podTemplate: &corev1.PodTemplateSpec{
				Namespace: "default",
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "c", Image: "busybox"}},
					ResourceClaims: []corev1.PodResourceClaim{
						{Name: "gpu", ResourceClaimTemplateName: new("gpu-template")},
					},
				},
			},
			candidates:   []*testCandidate{{node: gpuNode, id: "gpu-node"}},
			wantFeasible: []string{"gpu-node"},
			featureGates: map[featuregate.Feature]bool{kubefeatures.DRADeviceTaintRules: false},
		},
		"PodResourceClaim with neither name nor template is skipped": {
			objects: []runtime.Object{gpuSlice, gpuDeviceClass},
			podTemplate: &corev1.PodTemplateSpec{
				Namespace: "default",
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
			features.SetFeatureGatesDuringTest(t, tc.featureGates)
			cl := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(tc.objects...).
				WithIndex(&resourceapi.DeviceClass{}, indexer.DeviceClassExtendedResourceNameIndex,
					indexer.IndexDeviceClassExtendedResourceName).
				Build()
			inner := &passthroughChecker{}
			checker := NewChecker(inner, cl, &CELCache{}, true)

			candidateSeq := func(yield func(simulator.Candidate) bool) {
				for _, c := range tc.candidates {
					if !yield(c) {
						return
					}
				}
			}

			stats := &simulator.NodeExclusionStats{}
			feasible, err := checker.FindFeasibleNodes(t.Context(), candidateSeq, &simulator.PodRequirements{PodTemplate: tc.podTemplate}, stats)
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
