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
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/informers"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	resourceslicetracker "k8s.io/dynamic-resource-allocation/resourceslice/tracker"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"sigs.k8s.io/kueue/pkg/cache/scheduler/simulator"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

// The slices come from the client's cache, so a taint must land on a copy or it would
// persist into every later scheduling cycle.
func TestApplyRulesToSliceLeavesItsInputAlone(t *testing.T) {
	slice := &resourceapi.ResourceSlice{
		Name: "gpu-node-slice",
		Spec: resourceapi.ResourceSliceSpec{
			Driver:  "gpu.example.com",
			Pool:    resourceapi.ResourcePool{Name: "gpu-pool", ResourceSliceCount: 1},
			Devices: []resourceapi.Device{{Name: "gpu-0"}, {Name: "gpu-1"}},
		},
	}
	want := slice.DeepCopy()
	rules := []*resourceapi.DeviceTaintRule{{
		Name: "maintenance",
		Spec: resourceapi.DeviceTaintRuleSpec{
			Taint: resourceapi.DeviceTaint{
				Key:    "example.com/maintenance",
				Effect: resourceapi.DeviceTaintEffectNoSchedule,
			},
		},
	}}

	patched := applyRulesToSlice(slice, rules)

	if diff := cmp.Diff(want, slice); diff != "" {
		t.Errorf("the input slice was modified (-want +got):\n%s", diff)
	}
	if len(patched.Spec.Devices[0].Taints) != 1 {
		t.Errorf("the returned slice carries %d taints, want 1", len(patched.Spec.Devices[0].Taints))
	}
}

// Pins applyRulesToSlice to resourceslice/tracker, so a vendor bump that changes the
// upstream rule fails here. Taint order is not compared: the tracker's is undefined.
func TestDeviceTaintsMatchUpstreamTracker(t *testing.T) {
	deviceSlices := []*resourceapi.ResourceSlice{
		utiltesting.MakeResourceSlice("gpu-slice", "gpu.example.com").
			Pool("gpu-pool", 1, 1).Device("gpu-0").Device("gpu-1").Obj(),
		utiltesting.MakeResourceSlice("other-slice", "other.example.com").
			Pool("other-pool", 1, 1).Device("acc-0").Obj(),
	}
	// A taint the driver already published, which the rules add to rather than replace.
	deviceSlices[0].Spec.Devices[1].Taints = []resourceapi.DeviceTaint{{
		Key:    "example.com/driver-owned",
		Effect: resourceapi.DeviceTaintEffectNoSchedule,
	}}

	cases := map[string][]*resourceapi.DeviceTaintRule{
		"no rules":            nil,
		"driver-wide":         {utiltesting.MakeDeviceTaintRule("r", "k").Driver("gpu.example.com").Obj()},
		"non-matching driver": {utiltesting.MakeDeviceTaintRule("r", "k").Driver("absent.example.com").Obj()},
		"pool-wide":           {utiltesting.MakeDeviceTaintRule("r", "k").Pool("gpu-pool").Obj()},
		"non-matching pool":   {utiltesting.MakeDeviceTaintRule("r", "k").Pool("absent-pool").Obj()},
		"one device":          {utiltesting.MakeDeviceTaintRule("r", "k").Driver("gpu.example.com").Device("gpu-0").Obj()},
		"device name only":    {utiltesting.MakeDeviceTaintRule("r", "k").Device("gpu-0").Obj()},
		"empty selector":      {utiltesting.MakeDeviceTaintRule("r", "k").Obj()},
		"absent selector":     {utiltesting.MakeDeviceTaintRule("r", "k").NoSelector().Obj()},
		"driver and pool disagree": {utiltesting.MakeDeviceTaintRule("r", "k").
			Driver("gpu.example.com").Pool("other-pool").Obj()},
		"two rules on one device": {
			utiltesting.MakeDeviceTaintRule("r1", "k1").Driver("gpu.example.com").Obj(),
			utiltesting.MakeDeviceTaintRule("r2", "k2").Device("gpu-0").Obj(),
		},
		"NoExecute": {utiltesting.MakeDeviceTaintRule("r", "k").
			Driver("gpu.example.com").Effect(resourceapi.DeviceTaintEffectNoExecute).Obj()},
	}

	for name, rules := range cases {
		t.Run(name, func(t *testing.T) {
			objects := make([]runtime.Object, 0, len(deviceSlices)+len(rules))
			for _, slice := range deviceSlices {
				objects = append(objects, slice.DeepCopy())
			}
			for _, rule := range rules {
				objects = append(objects, rule.DeepCopy())
			}
			ctx := t.Context()
			factory := informers.NewSharedInformerFactory(k8sfake.NewClientset(objects...), 0)
			tracker, err := resourceslicetracker.StartTracker(ctx, resourceslicetracker.Options{
				EnableDeviceTaintRules: true,
				SliceInformer:          factory.Resource().V1().ResourceSlices(),
				TaintInformer:          factory.Resource().V1().DeviceTaintRules(),
			})
			if err != nil {
				t.Fatalf("starting the upstream tracker: %v", err)
			}
			t.Cleanup(tracker.Stop)
			factory.Start(ctx.Done())
			factory.WaitForCacheSync(ctx.Done())
			if err := wait.PollUntilContextTimeout(ctx, time.Millisecond, 10*time.Second, true,
				func(context.Context) (bool, error) { return tracker.HasSynced(), nil }); err != nil {
				t.Fatalf("waiting for the upstream tracker to sync: %v", err)
			}
			want, err := tracker.ListPatchedResourceSlices()
			if err != nil {
				t.Fatalf("listing patched ResourceSlices: %v", err)
			}

			got := make([]*resourceapi.ResourceSlice, len(deviceSlices))
			for i, slice := range deviceSlices {
				got[i] = applyRulesToSlice(slice, rules)
			}

			if diff := cmp.Diff(want, got,
				cmpopts.IgnoreFields(resourceapi.ResourceSlice{}, "TypeMeta", "ObjectMeta"),
				cmpopts.SortSlices(func(a, b *resourceapi.ResourceSlice) bool { return a.Name < b.Name }),
				cmpopts.SortSlices(func(a, b resourceapi.DeviceTaint) bool { return a.Key < b.Key }),
				cmpopts.EquateEmpty(),
			); diff != "" {
				t.Errorf("slices differ from resourceslice/tracker (-tracker +kueue):\n%s", diff)
			}
		})
	}
}

// On Kubernetes 1.36 and older the rules are not served as v1, and listing them would
// cost a discovery request every cycle, so the check never lists them there.
func TestDeviceTaintRulesAreNotListedWhenNotServed(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := resourceapi.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	node := &corev1.Node{Name: "gpu-node"}
	deviceClass := &resourceapi.DeviceClass{Name: "gpu.example.com"}
	claimTemplate := utiltesting.MakeResourceClaimTemplate("gpu-template", "default").
		DeviceRequest("gpu", "gpu.example.com", 1).Obj()
	slice := utiltesting.MakeResourceSlice("gpu-node-slice", "gpu.example.com").
		NodeName("gpu-node").Pool("gpu-pool", 1, 1).Device("gpu-0").Obj()

	cl := fake.NewClientBuilder().WithScheme(scheme).
		WithRuntimeObjects(node, deviceClass, claimTemplate, slice).
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				if _, ok := list.(*resourceapi.DeviceTaintRuleList); ok {
					t.Error("DeviceTaintRules were listed although the cluster does not serve them")
				}
				return c.List(ctx, list, opts...)
			},
		}).Build()

	checker := NewChecker(&passthroughChecker{}, cl, &CELCache{}, false)
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
	candidates := func(yield func(simulator.Candidate) bool) {
		yield(&testCandidate{node: node, id: "gpu-node"})
	}

	stats := &simulator.NodeExclusionStats{}
	feasible, err := checker.FindFeasibleNodes(t.Context(), candidates, requirements, stats)
	if err != nil {
		t.Fatalf("FindFeasibleNodes returned %v", err)
	}
	if len(feasible) != 1 {
		t.Errorf("got %d feasible nodes, want 1: the node has an untainted device", len(feasible))
	}
}
