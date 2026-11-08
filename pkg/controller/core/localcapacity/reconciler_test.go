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
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/event"
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

// deletingNode marks the node as being deleted. The finalizer keeps the fake client from removing it.
func deletingNode(node *corev1.Node) *corev1.Node {
	node.DeletionTimestamp = new(metav1.Now())
	node.Finalizers = []string{"example.com/test"}
	return node
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
		topologies         []*kueue.Topology
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
		"excludes NotReady and unschedulable nodes": {
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
			},
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
				Conditions: []metav1.Condition{synced("h100: 1 node; excluded: NotReady=2, Unschedulable=1")},
			},
		},
		"excludes nodes that are being deleted": {
			provider: utiltestingalpha.MakeCapacityProvider("nodes").
				ControllerName(ControllerName).
				OrchestratedFlavors("h100").
				Obj(),
			flavors: []*kueue.ResourceFlavor{h100Flavor},
			nodes: []*corev1.Node{
				gpuNode("ready", "h100").Ready().Obj(),
				deletingNode(gpuNode("deleting", "h100").Ready().Obj()),
			},
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
				Conditions: []metav1.Condition{synced("h100: 1 node; excluded: Deleting=1")},
			},
		},
		"counts tainted nodes, because workloads may tolerate the taints": {
			provider: utiltestingalpha.MakeCapacityProvider("nodes").
				ControllerName(ControllerName).
				OrchestratedFlavors("h100").
				Obj(),
			flavors: []*kueue.ResourceFlavor{h100Flavor},
			nodes: []*corev1.Node{
				gpuNode("no-schedule", "h100").Ready().
					Taints(corev1.Taint{Key: "maintenance", Effect: corev1.TaintEffectNoSchedule}).
					Obj(),
				gpuNode("no-execute", "h100").Ready().
					Taints(corev1.Taint{Key: "maintenance", Effect: corev1.TaintEffectNoExecute}).
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
		"counts only nodes with every level of the flavor's Topology": {
			provider: utiltestingalpha.MakeCapacityProvider("nodes").
				ControllerName(ControllerName).
				OrchestratedFlavors("h100").
				Obj(),
			flavors: []*kueue.ResourceFlavor{
				utiltestingapi.MakeResourceFlavor("h100").
					NodeLabel("example.com/gpu-type", "h100").
					TopologyName("default").
					Obj(),
			},
			topologies: []*kueue.Topology{
				utiltestingapi.MakeTopology("default").Levels("example.com/block", corev1.LabelHostname).Obj(),
			},
			nodes: []*corev1.Node{
				gpuNode("all-levels-1", "h100").Ready().
					Label("example.com/block", "b1").
					Label(corev1.LabelHostname, "all-levels-1").
					Obj(),
				gpuNode("all-levels-2", "h100").Ready().
					Label("example.com/block", "b1").
					Label(corev1.LabelHostname, "all-levels-2").
					Obj(),
				gpuNode("missing-block", "h100").Ready().
					Label(corev1.LabelHostname, "missing-block").
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
		"is misconfigured when the flavor's Topology is missing": {
			provider: utiltestingalpha.MakeCapacityProvider("nodes").
				ControllerName(ControllerName).
				OrchestratedFlavors("h100").
				Capacity(previousCapacity).
				Obj(),
			flavors: []*kueue.ResourceFlavor{
				utiltestingapi.MakeResourceFlavor("h100").
					NodeLabel("example.com/gpu-type", "h100").
					TopologyName("default").
					Obj(),
			},
			nodes: []*corev1.Node{gpuNode("h100-1", "h100").Ready().Obj()},
			wantStatus: kueuealpha.CapacityProviderStatus{
				Capacity:   previousCapacity,
				Conditions: []metav1.Condition{misconfigured(`Topology "default" of ResourceFlavor "h100" not found`)},
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
		"flavor of another provider with a missing Topology does not count as overlap": {
			provider: utiltestingalpha.MakeCapacityProvider("nodes").
				ControllerName(ControllerName).
				OrchestratedFlavors("h100").
				Obj(),
			otherProviders: []*kueuealpha.CapacityProvider{
				utiltestingalpha.MakeCapacityProvider("default").
					ControllerName(ControllerName).
					OrchestratedFlavors("default").
					Obj(),
			},
			flavors: []*kueue.ResourceFlavor{
				h100Flavor,
				utiltestingapi.MakeResourceFlavor("default").TopologyName("missing").Obj(),
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
		"spec.parameters is not supported and keeps the last capacity": {
			provider: utiltestingalpha.MakeCapacityProvider("nodes").
				ControllerName(ControllerName).
				OrchestratedFlavors("h100").
				Parameters("example.com", "LocalCapacity", "params").
				Capacity(previousCapacity).
				Obj(),
			flavors: []*kueue.ResourceFlavor{h100Flavor},
			nodes:   []*corev1.Node{gpuNode("h100-1", "h100").Ready().Obj()},
			wantStatus: kueuealpha.CapacityProviderStatus{
				Capacity:   previousCapacity,
				Conditions: []metav1.Condition{misconfigured("spec.parameters is not supported by kueue.x-k8s.io/local-capacity")},
			},
		},
		"does nothing for a provider served by another controller": {
			provider: utiltestingalpha.MakeCapacityProvider("external").
				ControllerName("example.com/external").
				OrchestratedFlavors("h100").
				Obj(),
			flavors:    []*kueue.ResourceFlavor{h100Flavor},
			nodes:      []*corev1.Node{gpuNode("h100-1", "h100").Ready().Obj()},
			wantStatus: kueuealpha.CapacityProviderStatus{},
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
			features.SetFeatureGateDuringTest(t, features.DQOLocalCapacityProvider, !tc.disableFeatureGate)

			objs := []client.Object{tc.provider}
			statusObjs := []client.Object{tc.provider}
			for _, p := range tc.otherProviders {
				objs = append(objs, p)
				statusObjs = append(statusObjs, p)
			}
			for _, f := range tc.flavors {
				objs = append(objs, f)
			}
			for _, topology := range tc.topologies {
				objs = append(objs, topology)
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
	features.SetFeatureGateDuringTest(t, features.DQOLocalCapacityProvider, true)

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

func TestNodeEventHandler(t *testing.T) {
	h100Flavor := utiltestingapi.MakeResourceFlavor("h100").NodeLabel("example.com/gpu-type", "h100").Obj()
	a100Flavor := utiltestingapi.MakeResourceFlavor("a100").NodeLabel("example.com/gpu-type", "a100").Obj()
	// The default flavor has no nodeLabels, so it matches every node and overlaps with the others.
	defaultFlavor := utiltestingapi.MakeResourceFlavor("default").Obj()
	tasFlavor := utiltestingapi.MakeResourceFlavor("tas").
		NodeLabel("example.com/gpu-type", "l4").
		TopologyName("block").
		Obj()
	// Shares the "block" Topology with tasFlavor, so the Topology is read once for both.
	tasFlavor2 := utiltestingapi.MakeResourceFlavor("tas-2").
		NodeLabel("example.com/gpu-type", "l40").
		TopologyName("block").
		Obj()
	orphanFlavor := utiltestingapi.MakeResourceFlavor("orphan").
		NodeLabel("example.com/gpu-type", "l4").
		TopologyName("missing").
		Obj()
	blockTopology := utiltestingapi.MakeTopology("block").Levels("example.com/block").Obj()

	h100Provider := utiltestingalpha.MakeCapacityProvider("h100-pool").ControllerName(ControllerName).OrchestratedFlavors("h100").Obj()
	a100Provider := utiltestingalpha.MakeCapacityProvider("a100-pool").ControllerName(ControllerName).OrchestratedFlavors("a100").Obj()
	defaultProvider := utiltestingalpha.MakeCapacityProvider("default-pool").ControllerName(ControllerName).OrchestratedFlavors("default").Obj()
	tasProvider := utiltestingalpha.MakeCapacityProvider("tas-pool").ControllerName(ControllerName).OrchestratedFlavors("tas").Obj()
	tasProvider2 := utiltestingalpha.MakeCapacityProvider("tas-pool-2").ControllerName(ControllerName).OrchestratedFlavors("tas-2").Obj()
	orphanProvider := utiltestingalpha.MakeCapacityProvider("orphan-pool").ControllerName(ControllerName).OrchestratedFlavors("orphan").Obj()
	// Two providers that claim the same flavor are Misconfigured, so both must be reconciled when a matching Node changes.
	h100DuplicateProvider := utiltestingalpha.MakeCapacityProvider("h100-pool-2").ControllerName(ControllerName).OrchestratedFlavors("h100").Obj()
	externalProvider := utiltestingalpha.MakeCapacityProvider("external").ControllerName("example.com/external").OrchestratedFlavors("a100").Obj()

	type nodeEvent struct {
		kind     string
		old, new *corev1.Node
		// other replaces the event object with something that is not a Node.
		other client.Object
	}
	create := func(n *corev1.Node) nodeEvent { return nodeEvent{kind: "create", new: n} }
	del := func(n *corev1.Node) nodeEvent { return nodeEvent{kind: "delete", old: n} }
	update := func(o, n *corev1.Node) nodeEvent { return nodeEvent{kind: "update", old: o, new: n} }
	generic := func(n *corev1.Node) nodeEvent { return nodeEvent{kind: "generic", new: n} }
	createOther := func(o client.Object) nodeEvent { return nodeEvent{kind: "create", other: o} }

	cases := map[string]struct {
		providers  []*kueuealpha.CapacityProvider
		flavors    []*kueue.ResourceFlavor
		topologies []*kueue.Topology
		failGet    string
		failList   bool
		event      nodeEvent
		want       []string
	}{
		"create enqueues the owner of the matched flavor": {
			providers: []*kueuealpha.CapacityProvider{h100Provider, a100Provider},
			flavors:   []*kueue.ResourceFlavor{h100Flavor, a100Flavor},
			event:     create(gpuNode("n", "h100").Obj()),
			want:      []string{"h100-pool"},
		},
		"create enqueues every provider that owns the matched flavor": {
			providers: []*kueuealpha.CapacityProvider{h100Provider, h100DuplicateProvider},
			flavors:   []*kueue.ResourceFlavor{h100Flavor},
			event:     create(gpuNode("n", "h100").Obj()),
			want:      []string{"h100-pool", "h100-pool-2"},
		},
		"delete enqueues the owner of the matched flavor": {
			providers: []*kueuealpha.CapacityProvider{h100Provider, a100Provider},
			flavors:   []*kueue.ResourceFlavor{h100Flavor, a100Flavor},
			event:     del(gpuNode("n", "a100").Obj()),
			want:      []string{"a100-pool"},
		},
		"generic enqueues the owner of the matched flavor": {
			providers: []*kueuealpha.CapacityProvider{h100Provider, a100Provider},
			flavors:   []*kueue.ResourceFlavor{h100Flavor, a100Flavor},
			event:     generic(gpuNode("n", "a100").Obj()),
			want:      []string{"a100-pool"},
		},
		"update that does not change labels enqueues the owner once": {
			providers: []*kueuealpha.CapacityProvider{h100Provider, a100Provider},
			flavors:   []*kueue.ResourceFlavor{h100Flavor, a100Flavor},
			event:     update(gpuNode("n", "h100").Ready().Obj(), gpuNode("n", "h100").Obj()),
			want:      []string{"h100-pool"},
		},
		"update adding the flavor label enqueues the new owner": {
			providers: []*kueuealpha.CapacityProvider{h100Provider, a100Provider},
			flavors:   []*kueue.ResourceFlavor{h100Flavor, a100Flavor},
			event:     update(testingnode.MakeNode("n").Obj(), gpuNode("n", "h100").Obj()),
			want:      []string{"h100-pool"},
		},
		"update removing the flavor label enqueues the previous owner": {
			providers: []*kueuealpha.CapacityProvider{h100Provider, a100Provider},
			flavors:   []*kueue.ResourceFlavor{h100Flavor, a100Flavor},
			event:     update(gpuNode("n", "h100").Obj(), testingnode.MakeNode("n").Obj()),
			want:      []string{"h100-pool"},
		},
		"update moving a node between flavors enqueues both owners": {
			providers: []*kueuealpha.CapacityProvider{h100Provider, a100Provider},
			flavors:   []*kueue.ResourceFlavor{h100Flavor, a100Flavor},
			event:     update(gpuNode("n", "h100").Obj(), gpuNode("n", "a100").Obj()),
			want:      []string{"a100-pool", "h100-pool"},
		},
		"update changing only an unrelated label enqueues nobody": {
			providers: []*kueuealpha.CapacityProvider{h100Provider, a100Provider},
			flavors:   []*kueue.ResourceFlavor{h100Flavor, a100Flavor},
			event: update(
				gpuNode("n", "l4").Obj(),
				gpuNode("n", "l4").Label("example.com/other", "x").Obj(),
			),
			want: []string{},
		},
		"node matching no flavor enqueues nobody": {
			providers: []*kueuealpha.CapacityProvider{h100Provider, a100Provider},
			flavors:   []*kueue.ResourceFlavor{h100Flavor, a100Flavor},
			event:     create(gpuNode("n", "l4").Obj()),
			want:      []string{},
		},
		"not ready and cordoned nodes still enqueue the owner": {
			providers: []*kueuealpha.CapacityProvider{h100Provider, a100Provider},
			flavors:   []*kueue.ResourceFlavor{h100Flavor, a100Flavor},
			event:     update(gpuNode("n", "h100").Ready().Obj(), gpuNode("n", "h100").NotReady().Unschedulable().Obj()),
			want:      []string{"h100-pool"},
		},
		"node matching flavors of two providers enqueues both": {
			providers: []*kueuealpha.CapacityProvider{h100Provider, defaultProvider},
			flavors:   []*kueue.ResourceFlavor{h100Flavor, defaultFlavor},
			event:     create(gpuNode("n", "h100").Obj()),
			want:      []string{"default-pool", "h100-pool"},
		},
		"flavor of a provider served by another controller is ignored": {
			providers: []*kueuealpha.CapacityProvider{h100Provider, externalProvider},
			flavors:   []*kueue.ResourceFlavor{h100Flavor, a100Flavor},
			event:     create(gpuNode("n", "a100").Obj()),
			want:      []string{},
		},
		"topology flavor requires the level labels": {
			providers:  []*kueuealpha.CapacityProvider{h100Provider, tasProvider},
			flavors:    []*kueue.ResourceFlavor{h100Flavor, tasFlavor},
			topologies: []*kueue.Topology{blockTopology},
			event:      create(gpuNode("n", "l4").Obj()),
			want:       []string{},
		},
		"topology flavor matches a node with the level labels": {
			providers:  []*kueuealpha.CapacityProvider{h100Provider, tasProvider},
			flavors:    []*kueue.ResourceFlavor{h100Flavor, tasFlavor},
			topologies: []*kueue.Topology{blockTopology},
			event:      create(gpuNode("n", "l4").Label("example.com/block", "b1").Obj()),
			want:       []string{"tas-pool"},
		},
		"update adding the level label enqueues the topology flavor owner": {
			providers:  []*kueuealpha.CapacityProvider{h100Provider, tasProvider},
			flavors:    []*kueue.ResourceFlavor{h100Flavor, tasFlavor},
			topologies: []*kueue.Topology{blockTopology},
			event:      update(gpuNode("n", "l4").Obj(), gpuNode("n", "l4").Label("example.com/block", "b1").Obj()),
			want:       []string{"tas-pool"},
		},
		"flavor with a missing topology matches nothing": {
			providers: []*kueuealpha.CapacityProvider{h100Provider, orphanProvider},
			flavors:   []*kueue.ResourceFlavor{h100Flavor, orphanFlavor},
			event:     create(gpuNode("n", "l4").Label("example.com/block", "b1").Obj()),
			want:      []string{},
		},
		"flavor that does not exist matches nothing": {
			providers: []*kueuealpha.CapacityProvider{h100Provider, a100Provider},
			flavors:   []*kueue.ResourceFlavor{h100Flavor},
			event:     create(gpuNode("n", "a100").Obj()),
			want:      []string{},
		},
		"flavors sharing a Topology enqueue only the owner of the matched flavor": {
			providers:  []*kueuealpha.CapacityProvider{tasProvider, tasProvider2},
			flavors:    []*kueue.ResourceFlavor{tasFlavor, tasFlavor2},
			topologies: []*kueue.Topology{blockTopology},
			event:      create(gpuNode("n", "l40").Label("example.com/block", "b1").Obj()),
			want:       []string{"tas-pool-2"},
		},
		"failure to list the providers enqueues nobody": {
			providers: []*kueuealpha.CapacityProvider{h100Provider, a100Provider},
			flavors:   []*kueue.ResourceFlavor{h100Flavor, a100Flavor},
			failList:  true,
			event:     create(gpuNode("n", "h100").Obj()),
			want:      []string{},
		},
		"an object that is not a Node enqueues all providers": {
			providers: []*kueuealpha.CapacityProvider{h100Provider, a100Provider},
			flavors:   []*kueue.ResourceFlavor{h100Flavor, a100Flavor},
			event:     createOther(&corev1.Pod{}),
			want:      []string{"a100-pool", "h100-pool"},
		},
		"failure to get a ResourceFlavor enqueues all providers": {
			providers: []*kueuealpha.CapacityProvider{h100Provider, a100Provider},
			flavors:   []*kueue.ResourceFlavor{h100Flavor, a100Flavor},
			failGet:   "ResourceFlavor",
			event:     create(gpuNode("n", "l4").Obj()),
			want:      []string{"a100-pool", "h100-pool"},
		},
		"failure to get a Topology enqueues all providers": {
			providers:  []*kueuealpha.CapacityProvider{h100Provider, tasProvider},
			flavors:    []*kueue.ResourceFlavor{h100Flavor, tasFlavor},
			topologies: []*kueue.Topology{blockTopology},
			failGet:    "Topology",
			event:      create(gpuNode("n", "l4").Obj()),
			want:       []string{"h100-pool", "tas-pool"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var objs []client.Object
			for _, p := range tc.providers {
				objs = append(objs, p)
			}
			for _, f := range tc.flavors {
				objs = append(objs, f)
			}
			for _, topology := range tc.topologies {
				objs = append(objs, topology)
			}
			builder := utiltesting.NewClientBuilder().WithObjects(objs...)
			if tc.failList {
				builder = builder.WithInterceptorFuncs(interceptor.Funcs{
					List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
						if _, ok := list.(*kueuealpha.CapacityProviderList); ok {
							return errors.New("listing CapacityProviders failed")
						}
						return cl.List(ctx, list, opts...)
					},
				})
			}
			if tc.failGet != "" {
				builder = builder.WithInterceptorFuncs(interceptor.Funcs{
					Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
						switch obj.(type) {
						case *kueue.ResourceFlavor:
							if tc.failGet == "ResourceFlavor" {
								return errors.New("getting ResourceFlavor failed")
							}
						case *kueue.Topology:
							if tc.failGet == "Topology" {
								return errors.New("getting Topology failed")
							}
						}
						return cl.Get(ctx, key, obj, opts...)
					},
				})
			}
			r := NewReconciler(builder.Build())
			h := r.nodeEventHandler()
			q := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]())
			defer q.ShutDown()

			ctx, _ := utiltesting.ContextWithLog(t)
			switch tc.event.kind {
			case "create":
				var obj client.Object = tc.event.new
				if tc.event.other != nil {
					obj = tc.event.other
				}
				h.Create(ctx, event.CreateEvent{Object: obj}, q)
			case "update":
				h.Update(ctx, event.UpdateEvent{ObjectOld: tc.event.old, ObjectNew: tc.event.new}, q)
			case "delete":
				h.Delete(ctx, event.DeleteEvent{Object: tc.event.old, DeleteStateUnknown: true}, q)
			case "generic":
				h.Generic(ctx, event.GenericEvent{Object: tc.event.new}, q)
			}

			got := []string{}
			for q.Len() > 0 {
				item, _ := q.Get()
				got = append(got, item.Name)
				q.Done(item)
			}
			slices.Sort(got)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("Unexpected enqueued providers (-want,+got):\n%s", diff)
			}
		})
	}
}
