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

package multikueue

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingalpha "sigs.k8s.io/kueue/pkg/util/testing/v1alpha1"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

type workerState struct {
	lqs      []*kueue.LocalQueue
	cqs      []*kueue.ClusterQueue
	inactive bool
}

func asObjs[T client.Object](in []T) []client.Object {
	out := make([]client.Object, len(in))
	for i, v := range in {
		out[i] = v
	}
	return out
}

func TestCapacityProviderReconcile(t *testing.T) {
	capacityProvider := func(flavor string) *kueuealpha.CapacityProvider {
		cp := utiltestingalpha.MakeCapacityProvider("cp1").
			ControllerName(kueuealpha.MultiKueueCapacityProviderControllerName).
			OrchestratedFlavors(flavor).
			Parameters(kueuealpha.SchemeGroupVersion.Group, kueuealpha.MultiKueueCapacityProviderConfigKind, "mkcpc").
			Obj()
		cp.Generation = 1
		return cp
	}
	providerConfig := func() *kueuealpha.MultiKueueCapacityProviderConfig {
		return &kueuealpha.MultiKueueCapacityProviderConfig{
			Name: "mkcpc",
			Spec: kueuealpha.MultiKueueCapacityProviderConfigSpec{
				ManagerSubtreeRootRef: kueuealpha.CapacityDistributionSubtreeRootRef{Kind: kueuealpha.ClusterQueueSubtreeRootRefKind, Name: "cq1"},
			},
		}
	}
	managerCQ := func(resources ...corev1.ResourceName) *kueue.ClusterQueue {
		flavor := utiltestingapi.MakeFlavorQuotas("default")
		for _, name := range resources {
			flavor.Resource(name, "1")
		}
		return utiltestingapi.MakeClusterQueue("cq1").ResourceGroup(*flavor.Obj()).AdmissionChecks("ac1").Obj()
	}
	managerObjs := func(objs ...client.Object) []client.Object {
		return append(objs,
			utiltestingapi.MakeAdmissionCheck("ac1").
				ControllerName(kueue.MultiKueueControllerName).
				Parameters(kueue.SchemeGroupVersion.Group, "MultiKueueConfig", "config1").
				Obj(),
			utiltestingapi.MakeMultiKueueConfig("config1").Clusters("worker1", "worker2").Obj(),
			utiltestingapi.MakeLocalQueue("lq1", TestNamespace).ClusterQueue("cq1").Obj(),
		)
	}
	// The worker2 ClusterQueue has an effective quota, which replaces its spec quota.
	// The unrelated worker1 ClusterQueue has no LocalQueue that matches a manager LocalQueue.
	workers := func(inactive ...string) map[string]workerState {
		ws := map[string]workerState{
			"worker1": {
				lqs: []*kueue.LocalQueue{
					utiltestingapi.MakeLocalQueue("lq1", TestNamespace).ClusterQueue("w1-cq1").Obj(),
					utiltestingapi.MakeLocalQueue("other-lq", TestNamespace).ClusterQueue("w1-unrelated").Obj(),
				},
				cqs: []*kueue.ClusterQueue{
					utiltestingapi.MakeClusterQueue("w1-cq1").
						ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource("cpu", "10").Resource("memory", "20Gi").Obj()).
						Obj(),
					utiltestingapi.MakeClusterQueue("w1-unrelated").
						ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource("nvidia.com/gpu", "8").Obj()).
						Obj(),
				},
			},
			"worker2": {
				lqs: []*kueue.LocalQueue{utiltestingapi.MakeLocalQueue("lq1", TestNamespace).ClusterQueue("w2-cq1").Obj()},
				cqs: []*kueue.ClusterQueue{
					utiltestingapi.MakeClusterQueue("w2-cq1").
						ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource("cpu", "1").Resource("memory", "1Gi").Obj()).
						EffectiveQuotas(*utiltestingapi.MakeFlavorQuotas("default").Resource("cpu", "5").Resource("memory", "10Gi").Obj()).
						Obj(),
				},
			},
		}
		for _, name := range inactive {
			w := ws[name]
			w.inactive = true
			ws[name] = w
		}
		return ws
	}
	condition := func(status metav1.ConditionStatus, reason, message string) metav1.Condition {
		return metav1.Condition{
			Type:               kueuealpha.CapacityProviderCapacitySynchronized,
			Status:             status,
			Reason:             reason,
			Message:            message,
			ObservedGeneration: 1,
		}
	}

	cases := map[string]struct {
		cp            *kueuealpha.CapacityProvider
		objs          []client.Object
		workers       map[string]workerState
		wantCapacity  *kueuealpha.CapacityProviderNormalizedCapacity
		wantCondition metav1.Condition
	}{
		"sums the quotas of the related worker ClusterQueues": {
			cp:      capacityProvider("default"),
			objs:    managerObjs(providerConfig(), managerCQ("cpu", "memory")),
			workers: workers(),
			wantCapacity: utiltestingalpha.MakeNormalizedCapacity().Flavors(
				utiltestingalpha.MakeNormalizedCapacityFlavor("default").Resource("cpu", "15").Resource("memory", "30Gi").Obj(),
			).Obj(),
			wantCondition: condition(metav1.ConditionTrue, kueuealpha.CapacityProviderReasonSynchronized,
				"Capacity synchronized with 2 of 2 worker clusters"),
		},
		"skips a disconnected worker cluster": {
			cp:      capacityProvider("default"),
			objs:    managerObjs(providerConfig(), managerCQ("cpu", "memory")),
			workers: workers("worker2"),
			wantCapacity: utiltestingalpha.MakeNormalizedCapacity().Flavors(
				utiltestingalpha.MakeNormalizedCapacityFlavor("default").Resource("cpu", "10").Resource("memory", "20Gi").Obj(),
			).Obj(),
			wantCondition: condition(metav1.ConditionTrue, kueuealpha.CapacityProviderReasonSynchronized,
				"Capacity synchronized with 1 of 2 worker clusters; disconnected worker clusters: worker2"),
		},
		"no worker cluster is connected": {
			cp:      capacityProvider("default"),
			objs:    managerObjs(providerConfig(), managerCQ("cpu", "memory")),
			workers: workers("worker1", "worker2"),
			wantCondition: condition(metav1.ConditionFalse, kueuealpha.CapacityProviderReasonSourceUnavailable,
				"No worker cluster of the manager ClusterQueue is connected"),
		},
		"MultiKueueCapacityProviderConfig not found": {
			cp:      capacityProvider("default"),
			objs:    managerObjs(managerCQ("cpu", "memory")),
			workers: workers(),
			wantCondition: condition(metav1.ConditionFalse, kueuealpha.CapacityProviderReasonMisconfigured,
				`MultiKueueCapacityProviderConfig "mkcpc" not found`),
		},
		"orchestrated flavor is not the manager flavor": {
			cp:      capacityProvider("other"),
			objs:    managerObjs(providerConfig(), managerCQ("cpu", "memory")),
			workers: workers(),
			wantCondition: condition(metav1.ConditionFalse, kueuealpha.CapacityProviderReasonMisconfigured,
				`ClusterQueue "cq1" must have exactly one ResourceFlavor, which is "other"`),
		},
		"manager ClusterQueue does not cover a worker resource": {
			cp:      capacityProvider("default"),
			objs:    managerObjs(providerConfig(), managerCQ("cpu")),
			workers: workers(),
			wantCondition: condition(metav1.ConditionFalse, kueuealpha.CapacityProviderReasonMisconfigured,
				`ClusterQueue "cq1" does not cover resources of the related worker ClusterQueues: [memory]`),
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			c := utiltesting.NewClientBuilder().
				WithObjects(tc.cp).
				WithObjects(tc.objs...).
				WithStatusSubresource(tc.cp).
				Build()

			cRec := newClustersReconciler(c, TestNamespace)
			for worker, wState := range tc.workers {
				rc := newRemoteClient(c, nil, nil, nil, defaultOrigin, worker, nil)
				rc.client = NewNeverCachingClient(utiltesting.NewClientBuilder().
					WithObjects(asObjs(wState.cqs)...).
					WithObjects(asObjs(wState.lqs)...).
					Build())
				// newRemoteClient starts disconnected; mark the active workers connected.
				if !wState.inactive {
					rc.connState.markConnected()
				}
				cRec.remoteClients[worker] = rc
			}
			helper, _ := admissioncheck.NewMultiKueueStoreHelper(c)
			reconciler := newCapacityProviderReconciler(c, helper, cRec, nil, time.Second)

			if _, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(tc.cp)}); err != nil {
				t.Fatalf("Unexpected reconcile error: %v", err)
			}

			gotCP := &kueuealpha.CapacityProvider{}
			if err := c.Get(ctx, client.ObjectKeyFromObject(tc.cp), gotCP); err != nil {
				t.Fatalf("Failed to get the CapacityProvider: %v", err)
			}
			if diff := cmp.Diff(tc.wantCapacity, gotCP.Status.Capacity); diff != "" {
				t.Errorf("Unexpected status.capacity (-want/+got):\n%s", diff)
			}
			gotCondition := apimeta.FindStatusCondition(gotCP.Status.Conditions, kueuealpha.CapacityProviderCapacitySynchronized)
			if diff := cmp.Diff(&tc.wantCondition, gotCondition, cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime")); diff != "" {
				t.Errorf("Unexpected CapacitySynchronized condition (-want/+got):\n%s", diff)
			}
		})
	}
}
