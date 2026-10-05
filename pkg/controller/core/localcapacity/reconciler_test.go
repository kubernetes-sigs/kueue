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

package localcapacity

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingalpha "sigs.k8s.io/kueue/pkg/util/testing/v1alpha1"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
)

const gpuResource corev1.ResourceName = "nvidia.com/gpu"

func gpuNode(name, gpuType string) *testingnode.NodeWrapper {
	return testingnode.MakeNode(name).
		Label("example.com/gpu-type", gpuType).
		StatusAllocatable(corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("120"),
			corev1.ResourceMemory: resource.MustParse("900Gi"),
			gpuResource:           resource.MustParse("8"),
		})
}

func synced(message string) metav1.Condition {
	return metav1.Condition{
		Type:    kueuealpha.CapacityProviderCapacitySynchronized,
		Status:  metav1.ConditionTrue,
		Reason:  kueuealpha.CapacityProviderReasonSynchronized,
		Message: message,
	}
}

// manyResourcesNode returns an h100 node that advertises count allocatable resources.
func manyResourcesNode(name string, count int) *corev1.Node {
	allocatable := corev1.ResourceList{}
	for i := range count {
		allocatable[corev1.ResourceName(fmt.Sprintf("example.com/res-%d", i))] = resource.MustParse("1")
	}
	return testingnode.MakeNode(name).
		Label("example.com/gpu-type", "h100").
		StatusAllocatable(allocatable).
		Ready().
		Obj()
}

// failNodeList makes listing Nodes fail, to simulate an unavailable capacity source.
var failNodeList = interceptor.Funcs{
	List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
		if _, ok := list.(*corev1.NodeList); ok {
			return errors.New("listing nodes failed")
		}
		return cl.List(ctx, list, opts...)
	},
}

func misconfigured(message string) metav1.Condition {
	return metav1.Condition{
		Type:    kueuealpha.CapacityProviderCapacitySynchronized,
		Status:  metav1.ConditionFalse,
		Reason:  kueuealpha.CapacityProviderReasonMisconfigured,
		Message: message,
	}
}

func TestReconcile(t *testing.T) {
	h100Flavor := utiltestingapi.MakeResourceFlavor("h100").NodeLabel("example.com/gpu-type", "h100").Obj()
	a100Flavor := utiltestingapi.MakeResourceFlavor("a100").NodeLabel("example.com/gpu-type", "a100").Obj()
	previousCapacity := utiltestingalpha.MakeNormalizedCapacity().
		Flavors(utiltestingalpha.MakeNormalizedCapacityFlavor("h100").Resource(gpuResource, "16").Obj()).
		Obj()

	cases := map[string]struct {
		disableFeatureGate bool
		failNodeList       bool
		provider           *kueuealpha.CapacityProvider
		otherProviders     []*kueuealpha.CapacityProvider
		flavors            []*kueue.ResourceFlavor
		nodes              []*corev1.Node
		wantStatus         kueuealpha.CapacityProviderStatus
		wantErr            bool
	}{
		"sums allocatable of eligible nodes per flavor": {
			provider: utiltestingalpha.MakeCapacityProvider("nodes").
				ControllerName(ControllerName).
				OrchestratedFlavors("h100", "a100").
				Obj(),
			flavors: []*kueue.ResourceFlavor{h100Flavor, a100Flavor},
			nodes: []*corev1.Node{
				gpuNode("h100-1", "h100").Ready().Obj(),
				gpuNode("h100-2", "h100").Ready().Obj(),
				gpuNode("a100-1", "a100").Ready().Obj(),
				gpuNode("other", "l4").Ready().Obj(),
			},
			wantStatus: kueuealpha.CapacityProviderStatus{
				Capacity: utiltestingalpha.MakeNormalizedCapacity().
					Flavors(
						utiltestingalpha.MakeNormalizedCapacityFlavor("a100").
							Resource(corev1.ResourceCPU, "120").
							Resource(corev1.ResourceMemory, "900Gi").
							Resource(gpuResource, "8").
							Obj(),
						utiltestingalpha.MakeNormalizedCapacityFlavor("h100").
							Resource(corev1.ResourceCPU, "240").
							Resource(corev1.ResourceMemory, "1800Gi").
							Resource(gpuResource, "16").
							Obj(),
					).
					Obj(),
				Conditions: []metav1.Condition{synced("a100: 1 node; h100: 2 nodes")},
			},
		},
		"excludes NotReady, unschedulable and untolerated-tainted nodes": {
			provider: utiltestingalpha.MakeCapacityProvider("nodes").
				ControllerName(ControllerName).
				OrchestratedFlavors("h100").
				Obj(),
			flavors: []*kueue.ResourceFlavor{h100Flavor},
			nodes: []*corev1.Node{
				gpuNode("ready", "h100").Ready().Obj(),
				gpuNode("not-ready", "h100").NotReady().Obj(),
				gpuNode("no-condition", "h100").Obj(),
				gpuNode("cordoned", "h100").Ready().Unschedulable().Obj(),
				gpuNode("tainted", "h100").Ready().
					Taints(corev1.Taint{Key: "maintenance", Effect: corev1.TaintEffectNoSchedule}).
					Obj(),
				gpuNode("prefer-no-schedule", "h100").Ready().
					Taints(corev1.Taint{Key: "soft", Effect: corev1.TaintEffectPreferNoSchedule}).
					Obj(),
			},
			wantStatus: kueuealpha.CapacityProviderStatus{
				Capacity: utiltestingalpha.MakeNormalizedCapacity().
					Flavors(
						utiltestingalpha.MakeNormalizedCapacityFlavor("h100").
							Resource(corev1.ResourceCPU, "240").
							Resource(corev1.ResourceMemory, "1800Gi").
							Resource(gpuResource, "16").
							Obj(),
					).
					Obj(),
				Conditions: []metav1.Condition{synced("h100: 2 nodes; excluded: NotReady=2, Unschedulable=1, UntoleratedTaint=1")},
			},
		},
		"counts tainted nodes when the flavor tolerates or declares the taint": {
			provider: utiltestingalpha.MakeCapacityProvider("nodes").
				ControllerName(ControllerName).
				OrchestratedFlavors("h100").
				Obj(),
			flavors: []*kueue.ResourceFlavor{
				utiltestingapi.MakeResourceFlavor("h100").
					NodeLabel("example.com/gpu-type", "h100").
					Toleration(corev1.Toleration{Key: "tolerated", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule}).
					Taint(corev1.Taint{Key: "nvidia.com/gpu", Effect: corev1.TaintEffectNoSchedule}).
					Obj(),
			},
			nodes: []*corev1.Node{
				gpuNode("tolerated", "h100").Ready().
					Taints(corev1.Taint{Key: "tolerated", Effect: corev1.TaintEffectNoSchedule}).
					Obj(),
				gpuNode("declared", "h100").Ready().
					Taints(corev1.Taint{Key: "nvidia.com/gpu", Effect: corev1.TaintEffectNoSchedule}).
					Obj(),
			},
			wantStatus: kueuealpha.CapacityProviderStatus{
				Capacity: utiltestingalpha.MakeNormalizedCapacity().
					Flavors(
						utiltestingalpha.MakeNormalizedCapacityFlavor("h100").
							Resource(corev1.ResourceCPU, "240").
							Resource(corev1.ResourceMemory, "1800Gi").
							Resource(gpuResource, "16").
							Obj(),
					).
					Obj(),
				Conditions: []metav1.Condition{synced("h100: 2 nodes")},
			},
		},
		"publishes a flavor without eligible nodes with empty resources": {
			provider: utiltestingalpha.MakeCapacityProvider("nodes").
				ControllerName(ControllerName).
				OrchestratedFlavors("h100").
				Capacity(previousCapacity).
				Obj(),
			flavors: []*kueue.ResourceFlavor{h100Flavor},
			wantStatus: kueuealpha.CapacityProviderStatus{
				Capacity: utiltestingalpha.MakeNormalizedCapacity().
					Flavors(utiltestingalpha.MakeNormalizedCapacityFlavor("h100").Obj()).
					Obj(),
				Conditions: []metav1.Condition{synced("h100: 0 nodes")},
			},
		},
		"missing ResourceFlavor is misconfigured and keeps the last capacity": {
			provider: utiltestingalpha.MakeCapacityProvider("nodes").
				ControllerName(ControllerName).
				OrchestratedFlavors("h100").
				Capacity(previousCapacity).
				Obj(),
			nodes: []*corev1.Node{gpuNode("h100-1", "h100").Ready().Obj()},
			wantStatus: kueuealpha.CapacityProviderStatus{
				Capacity:   previousCapacity,
				Conditions: []metav1.Condition{misconfigured("ResourceFlavors not found: h100")},
			},
		},
		"node matching multiple orchestrated flavors is misconfigured": {
			provider: utiltestingalpha.MakeCapacityProvider("nodes").
				ControllerName(ControllerName).
				OrchestratedFlavors("h100").
				Capacity(previousCapacity).
				Obj(),
			otherProviders: []*kueuealpha.CapacityProvider{
				utiltestingalpha.MakeCapacityProvider("default").
					ControllerName(ControllerName).
					OrchestratedFlavors("default").
					Obj(),
			},
			flavors: []*kueue.ResourceFlavor{
				h100Flavor,
				utiltestingapi.MakeResourceFlavor("default").Obj(),
			},
			nodes: []*corev1.Node{gpuNode("h100-1", "h100").Ready().Obj()},
			wantStatus: kueuealpha.CapacityProviderStatus{
				Capacity:   previousCapacity,
				Conditions: []metav1.Condition{misconfigured(`Node "h100-1" matches multiple orchestrated ResourceFlavors: default, h100`)},
			},
		},
		"flavor orchestrated by multiple local-capacity providers is misconfigured": {
			provider: utiltestingalpha.MakeCapacityProvider("nodes").
				ControllerName(ControllerName).
				OrchestratedFlavors("h100").
				Obj(),
			otherProviders: []*kueuealpha.CapacityProvider{
				utiltestingalpha.MakeCapacityProvider("nodes-dup").
					ControllerName(ControllerName).
					OrchestratedFlavors("h100").
					Obj(),
			},
			flavors: []*kueue.ResourceFlavor{h100Flavor},
			wantStatus: kueuealpha.CapacityProviderStatus{
				Conditions: []metav1.Condition{misconfigured(`ResourceFlavor "h100" is orchestrated by multiple local-capacity CapacityProviders: nodes, nodes-dup`)},
			},
		},
		"flavors of providers served by other controllers do not count as overlap": {
			provider: utiltestingalpha.MakeCapacityProvider("nodes").
				ControllerName(ControllerName).
				OrchestratedFlavors("h100").
				Obj(),
			otherProviders: []*kueuealpha.CapacityProvider{
				utiltestingalpha.MakeCapacityProvider("external").
					ControllerName("example.com/external").
					OrchestratedFlavors("default").
					Obj(),
			},
			flavors: []*kueue.ResourceFlavor{
				h100Flavor,
				utiltestingapi.MakeResourceFlavor("default").Obj(),
			},
			nodes: []*corev1.Node{gpuNode("h100-1", "h100").Ready().Obj()},
			wantStatus: kueuealpha.CapacityProviderStatus{
				Capacity: utiltestingalpha.MakeNormalizedCapacity().
					Flavors(
						utiltestingalpha.MakeNormalizedCapacityFlavor("h100").
							Resource(corev1.ResourceCPU, "120").
							Resource(corev1.ResourceMemory, "900Gi").
							Resource(gpuResource, "8").
							Obj(),
					).
					Obj(),
				Conditions: []metav1.Condition{synced("h100: 1 node")},
			},
		},
		"keeps the last capacity when Nodes cannot be listed": {
			failNodeList: true,
			provider: utiltestingalpha.MakeCapacityProvider("nodes").
				ControllerName(ControllerName).
				OrchestratedFlavors("h100").
				Capacity(previousCapacity).
				Obj(),
			flavors: []*kueue.ResourceFlavor{h100Flavor},
			nodes:   []*corev1.Node{gpuNode("h100-1", "h100").Ready().Obj()},
			wantStatus: kueuealpha.CapacityProviderStatus{
				Capacity: previousCapacity,
				Conditions: []metav1.Condition{{
					Type:    kueuealpha.CapacityProviderCapacitySynchronized,
					Status:  metav1.ConditionFalse,
					Reason:  kueuealpha.CapacityProviderReasonSourceUnavailable,
					Message: "listing nodes failed",
				}},
			},
			wantErr: true,
		},
		"more resources than the cap is misconfigured and keeps the last capacity": {
			provider: utiltestingalpha.MakeCapacityProvider("nodes").
				ControllerName(ControllerName).
				OrchestratedFlavors("h100").
				Capacity(previousCapacity).
				Obj(),
			flavors: []*kueue.ResourceFlavor{h100Flavor},
			nodes:   []*corev1.Node{manyResourcesNode("h100-1", maxResourcesPerFlavor+1)},
			wantStatus: kueuealpha.CapacityProviderStatus{
				Capacity:   previousCapacity,
				Conditions: []metav1.Condition{misconfigured(`ResourceFlavor "h100" has 65 resources, more than the maximum of 64`)},
			},
		},
		"does nothing when the feature gate is disabled": {
			disableFeatureGate: true,
			provider: utiltestingalpha.MakeCapacityProvider("nodes").
				ControllerName(ControllerName).
				OrchestratedFlavors("h100").
				Obj(),
			flavors:    []*kueue.ResourceFlavor{h100Flavor},
			nodes:      []*corev1.Node{gpuNode("h100-1", "h100").Ready().Obj()},
			wantStatus: kueuealpha.CapacityProviderStatus{},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.DynamicQuotaOrchestration, true)
			features.SetFeatureGateDuringTest(t, features.LocalCapacityProvider, !tc.disableFeatureGate)

			objs := []client.Object{tc.provider}
			statusObjs := []client.Object{tc.provider}
			for _, p := range tc.otherProviders {
				objs = append(objs, p)
				statusObjs = append(statusObjs, p)
			}
			for _, f := range tc.flavors {
				objs = append(objs, f)
			}
			for _, n := range tc.nodes {
				objs = append(objs, n)
			}
			builder := utiltesting.NewClientBuilder().WithObjects(objs...).WithStatusSubresource(statusObjs...)
			if tc.failNodeList {
				builder = builder.WithInterceptorFuncs(failNodeList)
			}
			cl := builder.Build()
			r := NewReconciler(cl)

			ctx, _ := utiltesting.ContextWithLog(t)
			if _, err := r.Reconcile(ctx, reconcile.Request{Name: tc.provider.Name}); (err != nil) != tc.wantErr {
				t.Fatalf("Reconcile() error = %v, wantErr %v", err, tc.wantErr)
			}

			var got kueuealpha.CapacityProvider
			if err := cl.Get(ctx, types.NamespacedName{Name: tc.provider.Name}, &got); err != nil {
				t.Fatalf("Get CapacityProvider: %v", err)
			}
			if diff := cmp.Diff(tc.wantStatus, got.Status,
				cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime", "ObservedGeneration"),
				cmpopts.EquateEmpty(),
			); diff != "" {
				t.Errorf("Unexpected status (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestReconcileSkipsUnchangedStatus(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.DynamicQuotaOrchestration, true)
	features.SetFeatureGateDuringTest(t, features.LocalCapacityProvider, true)

	provider := utiltestingalpha.MakeCapacityProvider("nodes").
		ControllerName(ControllerName).
		OrchestratedFlavors("h100").
		Obj()
	var statusUpdates int
	cl := utiltesting.NewClientBuilder().
		WithObjects(
			provider,
			utiltestingapi.MakeResourceFlavor("h100").NodeLabel("example.com/gpu-type", "h100").Obj(),
			gpuNode("h100-1", "h100").Ready().Obj(),
		).
		WithStatusSubresource(provider).
		WithInterceptorFuncs(interceptor.Funcs{SubResourceUpdate: utiltesting.CountSubResourceUpdates(&statusUpdates)}).
		Build()
	r := NewReconciler(cl)

	ctx, _ := utiltesting.ContextWithLog(t)
	for range 2 {
		if _, err := r.Reconcile(ctx, reconcile.Request{Name: provider.Name}); err != nil {
			t.Fatalf("Reconcile() error = %v", err)
		}
	}
	if statusUpdates != 1 {
		t.Errorf("Unexpected number of status updates: want 1, got %d", statusUpdates)
	}
}

func TestIsLocalCapacityProvider(t *testing.T) {
	cases := map[string]struct {
		obj  client.Object
		want bool
	}{
		"local-capacity provider": {
			obj:  utiltestingalpha.MakeCapacityProvider("nodes").ControllerName(ControllerName).Obj(),
			want: true,
		},
		"provider served by another controller": {
			obj:  utiltestingalpha.MakeCapacityProvider("external").ControllerName("example.com/external").Obj(),
			want: false,
		},
		"not a CapacityProvider": {
			obj:  utiltestingapi.MakeResourceFlavor("h100").Obj(),
			want: false,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := isLocalCapacityProvider(tc.obj); got != tc.want {
				t.Errorf("isLocalCapacityProvider() = %v, want %v", got, tc.want)
			}
		})
	}
}
