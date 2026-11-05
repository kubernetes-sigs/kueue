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
	"context"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingalpha "sigs.k8s.io/kueue/pkg/util/testing/v1alpha1"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

const (
	testProviderConfigKind = "MultiKueueCapacityProviderConfig"
	testOtherController    = "example.com/other"
)

func asObjs[T client.Object](objs []T) []client.Object {
	res := make([]client.Object, len(objs))
	for i, obj := range objs {
		res[i] = obj
	}
	return res
}

func TestCPReconcile(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.DQOMultiKueueCapacityProvider, true)

	misconfiguredCondition := func(message string, generation int64) metav1.Condition {
		return metav1.Condition{
			Type:               kueuealpha.CapacityProviderCapacitySynchronized,
			Status:             metav1.ConditionFalse,
			Reason:             kueuealpha.CapacityProviderReasonMisconfigured,
			Message:            message,
			ObservedGeneration: generation,
		}
	}

	cases := map[string]struct {
		cp              *kueuealpha.CapacityProvider
		providerConfigs []*kueuealpha.MultiKueueCapacityProviderConfig
		cqs             []*kueue.ClusterQueue
		acs             []*kueue.AdmissionCheck
		mkConfigs       []*kueue.MultiKueueConfig

		wantCP            *kueuealpha.CapacityProvider
		wantStatusPatches int
	}{
		"CapacityProvider not found": {},
		"CapacityProvider of another controller is ignored": {
			cp: utiltestingalpha.MakeCapacityProvider("cp1").
				ControllerName(testOtherController).
				OrchestratedFlavors("default").
				Generation(1).
				Obj(),
			wantCP: utiltestingalpha.MakeCapacityProvider("cp1").
				ControllerName(testOtherController).
				OrchestratedFlavors("default").
				Generation(1).
				Obj(),
		},
		"parameters not set": {
			cp: utiltestingalpha.MakeCapacityProvider("cp1").
				ControllerName(kueue.MultiKueueControllerName).
				OrchestratedFlavors("default").
				Generation(1).
				Obj(),
			wantCP: utiltestingalpha.MakeCapacityProvider("cp1").
				ControllerName(kueue.MultiKueueControllerName).
				OrchestratedFlavors("default").
				Generation(1).
				Condition(misconfiguredCondition("spec.parameters must reference a MultiKueueCapacityProviderConfig in the kueue.x-k8s.io API group", 1)).
				Obj(),
			wantStatusPatches: 1,
		},
		"parameters reference another kind": {
			cp: utiltestingalpha.MakeCapacityProvider("cp1").
				ControllerName(kueue.MultiKueueControllerName).
				OrchestratedFlavors("default").
				Parameters(kueuealpha.SchemeGroupVersion.Group, "DynamicQuotaOrchestrator", "cp-config1").
				Generation(1).
				Obj(),
			wantCP: utiltestingalpha.MakeCapacityProvider("cp1").
				ControllerName(kueue.MultiKueueControllerName).
				OrchestratedFlavors("default").
				Parameters(kueuealpha.SchemeGroupVersion.Group, "DynamicQuotaOrchestrator", "cp-config1").
				Generation(1).
				Condition(misconfiguredCondition("spec.parameters must reference a MultiKueueCapacityProviderConfig in the kueue.x-k8s.io API group", 1)).
				Obj(),
			wantStatusPatches: 1,
		},
		"parameters reference another API group": {
			cp: utiltestingalpha.MakeCapacityProvider("cp1").
				ControllerName(kueue.MultiKueueControllerName).
				OrchestratedFlavors("default").
				Parameters("example.com", testProviderConfigKind, "cp-config1").
				Generation(1).
				Obj(),
			wantCP: utiltestingalpha.MakeCapacityProvider("cp1").
				ControllerName(kueue.MultiKueueControllerName).
				OrchestratedFlavors("default").
				Parameters("example.com", testProviderConfigKind, "cp-config1").
				Generation(1).
				Condition(misconfiguredCondition("spec.parameters must reference a MultiKueueCapacityProviderConfig in the kueue.x-k8s.io API group", 1)).
				Obj(),
			wantStatusPatches: 1,
		},
		"more than one orchestrated flavor": {
			cp: utiltestingalpha.MakeCapacityProvider("cp1").
				ControllerName(kueue.MultiKueueControllerName).
				OrchestratedFlavors("default", "other").
				Parameters(kueuealpha.SchemeGroupVersion.Group, testProviderConfigKind, "cp-config1").
				Generation(1).
				Obj(),
			wantCP: utiltestingalpha.MakeCapacityProvider("cp1").
				ControllerName(kueue.MultiKueueControllerName).
				OrchestratedFlavors("default", "other").
				Parameters(kueuealpha.SchemeGroupVersion.Group, testProviderConfigKind, "cp-config1").
				Generation(1).
				Condition(misconfiguredCondition("spec.orchestratedFlavors must have exactly one ResourceFlavor, found 2", 1)).
				Obj(),
			wantStatusPatches: 1,
		},
		"MultiKueueCapacityProviderConfig not found": {
			cp: utiltestingalpha.MakeCapacityProvider("cp1").
				ControllerName(kueue.MultiKueueControllerName).
				OrchestratedFlavors("default").
				Parameters(kueuealpha.SchemeGroupVersion.Group, testProviderConfigKind, "cp-config1").
				Generation(1).
				Obj(),
			wantCP: utiltestingalpha.MakeCapacityProvider("cp1").
				ControllerName(kueue.MultiKueueControllerName).
				OrchestratedFlavors("default").
				Parameters(kueuealpha.SchemeGroupVersion.Group, testProviderConfigKind, "cp-config1").
				Generation(1).
				Condition(misconfiguredCondition(`MultiKueueCapacityProviderConfig "cp-config1" not found`, 1)).
				Obj(),
			wantStatusPatches: 1,
		},
		"manager ClusterQueue not found": {
			cp: utiltestingalpha.MakeCapacityProvider("cp1").
				ControllerName(kueue.MultiKueueControllerName).
				OrchestratedFlavors("default").
				Parameters(kueuealpha.SchemeGroupVersion.Group, testProviderConfigKind, "cp-config1").
				Generation(1).
				Obj(),
			providerConfigs: []*kueuealpha.MultiKueueCapacityProviderConfig{
				utiltestingalpha.MakeMultiKueueCapacityProviderConfig("cp-config1").ManagerSubtreeRootRef(kueuealpha.ClusterQueueSubtreeRootRefKind, "cq1").Obj(),
			},
			wantCP: utiltestingalpha.MakeCapacityProvider("cp1").
				ControllerName(kueue.MultiKueueControllerName).
				OrchestratedFlavors("default").
				Parameters(kueuealpha.SchemeGroupVersion.Group, testProviderConfigKind, "cp-config1").
				Generation(1).
				Condition(misconfiguredCondition(`ClusterQueue "cq1" not found`, 1)).
				Obj(),
			wantStatusPatches: 1,
		},
		"manager ClusterQueue without AdmissionChecks": {
			cp: utiltestingalpha.MakeCapacityProvider("cp1").
				ControllerName(kueue.MultiKueueControllerName).
				OrchestratedFlavors("default").
				Parameters(kueuealpha.SchemeGroupVersion.Group, testProviderConfigKind, "cp-config1").
				Generation(1).
				Obj(),
			providerConfigs: []*kueuealpha.MultiKueueCapacityProviderConfig{
				utiltestingalpha.MakeMultiKueueCapacityProviderConfig("cp-config1").ManagerSubtreeRootRef(kueuealpha.ClusterQueueSubtreeRootRefKind, "cq1").Obj(),
			},
			cqs: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("cq1").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource("cpu", "1").Obj()).
					Obj(),
			},
			wantCP: utiltestingalpha.MakeCapacityProvider("cp1").
				ControllerName(kueue.MultiKueueControllerName).
				OrchestratedFlavors("default").
				Parameters(kueuealpha.SchemeGroupVersion.Group, testProviderConfigKind, "cp-config1").
				Generation(1).
				Condition(misconfiguredCondition(`ClusterQueue "cq1" has no MultiKueue AdmissionCheck`, 1)).
				Obj(),
			wantStatusPatches: 1,
		},
		"manager ClusterQueue with an AdmissionCheck of another controller": {
			cp: utiltestingalpha.MakeCapacityProvider("cp1").
				ControllerName(kueue.MultiKueueControllerName).
				OrchestratedFlavors("default").
				Parameters(kueuealpha.SchemeGroupVersion.Group, testProviderConfigKind, "cp-config1").
				Generation(1).
				Obj(),
			providerConfigs: []*kueuealpha.MultiKueueCapacityProviderConfig{
				utiltestingalpha.MakeMultiKueueCapacityProviderConfig("cp-config1").ManagerSubtreeRootRef(kueuealpha.ClusterQueueSubtreeRootRefKind, "cq1").Obj(),
			},
			cqs: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("cq1").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource("cpu", "1").Obj()).
					AdmissionChecks("ac1").
					Obj(),
			},
			acs: []*kueue.AdmissionCheck{
				utiltestingapi.MakeAdmissionCheck("ac1").ControllerName(testOtherController).Obj(),
			},
			wantCP: utiltestingalpha.MakeCapacityProvider("cp1").
				ControllerName(kueue.MultiKueueControllerName).
				OrchestratedFlavors("default").
				Parameters(kueuealpha.SchemeGroupVersion.Group, testProviderConfigKind, "cp-config1").
				Generation(1).
				Condition(misconfiguredCondition(`ClusterQueue "cq1" has no MultiKueue AdmissionCheck`, 1)).
				Obj(),
			wantStatusPatches: 1,
		},
		"MultiKueue AdmissionCheck without parameters": {
			cp: utiltestingalpha.MakeCapacityProvider("cp1").
				ControllerName(kueue.MultiKueueControllerName).
				OrchestratedFlavors("default").
				Parameters(kueuealpha.SchemeGroupVersion.Group, testProviderConfigKind, "cp-config1").
				Generation(1).
				Obj(),
			providerConfigs: []*kueuealpha.MultiKueueCapacityProviderConfig{
				utiltestingalpha.MakeMultiKueueCapacityProviderConfig("cp-config1").ManagerSubtreeRootRef(kueuealpha.ClusterQueueSubtreeRootRefKind, "cq1").Obj(),
			},
			cqs: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("cq1").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource("cpu", "1").Obj()).
					AdmissionChecks("ac1").
					Obj(),
			},
			acs: []*kueue.AdmissionCheck{
				utiltestingapi.MakeAdmissionCheck("ac1").ControllerName(kueue.MultiKueueControllerName).Obj(),
			},
			wantCP: utiltestingalpha.MakeCapacityProvider("cp1").
				ControllerName(kueue.MultiKueueControllerName).
				OrchestratedFlavors("default").
				Parameters(kueuealpha.SchemeGroupVersion.Group, testProviderConfigKind, "cp-config1").
				Generation(1).
				Condition(misconfiguredCondition(`AdmissionCheck "ac1" has invalid parameters: missing parameters reference`, 1)).
				Obj(),
			wantStatusPatches: 1,
		},
		"MultiKueue AdmissionCheck with parameters of another kind": {
			cp: utiltestingalpha.MakeCapacityProvider("cp1").
				ControllerName(kueue.MultiKueueControllerName).
				OrchestratedFlavors("default").
				Parameters(kueuealpha.SchemeGroupVersion.Group, testProviderConfigKind, "cp-config1").
				Generation(1).
				Obj(),
			providerConfigs: []*kueuealpha.MultiKueueCapacityProviderConfig{
				utiltestingalpha.MakeMultiKueueCapacityProviderConfig("cp-config1").ManagerSubtreeRootRef(kueuealpha.ClusterQueueSubtreeRootRefKind, "cq1").Obj(),
			},
			cqs: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("cq1").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource("cpu", "1").Obj()).
					AdmissionChecks("ac1").
					Obj(),
			},
			acs: []*kueue.AdmissionCheck{
				utiltestingapi.MakeAdmissionCheck("ac1").
					ControllerName(kueue.MultiKueueControllerName).
					Parameters(kueue.SchemeGroupVersion.Group, "ProvisioningRequestConfig", "config1").
					Obj(),
			},
			wantCP: utiltestingalpha.MakeCapacityProvider("cp1").
				ControllerName(kueue.MultiKueueControllerName).
				OrchestratedFlavors("default").
				Parameters(kueuealpha.SchemeGroupVersion.Group, testProviderConfigKind, "cp-config1").
				Generation(1).
				Condition(misconfiguredCondition(`AdmissionCheck "ac1" has invalid parameters: wrong kind "ProvisioningRequestConfig", expecting "MultiKueueConfig": bad parameters reference`, 1)).
				Obj(),
			wantStatusPatches: 1,
		},
		"MultiKueueConfig not found": {
			cp: utiltestingalpha.MakeCapacityProvider("cp1").
				ControllerName(kueue.MultiKueueControllerName).
				OrchestratedFlavors("default").
				Parameters(kueuealpha.SchemeGroupVersion.Group, testProviderConfigKind, "cp-config1").
				Generation(1).
				Obj(),
			providerConfigs: []*kueuealpha.MultiKueueCapacityProviderConfig{
				utiltestingalpha.MakeMultiKueueCapacityProviderConfig("cp-config1").ManagerSubtreeRootRef(kueuealpha.ClusterQueueSubtreeRootRefKind, "cq1").Obj(),
			},
			cqs: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("cq1").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource("cpu", "1").Obj()).
					AdmissionChecks("ac1").
					Obj(),
			},
			acs: []*kueue.AdmissionCheck{
				utiltestingapi.MakeAdmissionCheck("ac1").
					ControllerName(kueue.MultiKueueControllerName).
					Parameters(kueue.SchemeGroupVersion.Group, "MultiKueueConfig", "config1").
					Obj(),
			},
			wantCP: utiltestingalpha.MakeCapacityProvider("cp1").
				ControllerName(kueue.MultiKueueControllerName).
				OrchestratedFlavors("default").
				Parameters(kueuealpha.SchemeGroupVersion.Group, testProviderConfigKind, "cp-config1").
				Generation(1).
				Condition(misconfiguredCondition(`MultiKueueConfig "config1" not found`, 1)).
				Obj(),
			wantStatusPatches: 1,
		},
		"manager ClusterQueue with two ResourceFlavors": {
			cp: utiltestingalpha.MakeCapacityProvider("cp1").
				ControllerName(kueue.MultiKueueControllerName).
				OrchestratedFlavors("default").
				Parameters(kueuealpha.SchemeGroupVersion.Group, testProviderConfigKind, "cp-config1").
				Generation(1).
				Obj(),
			providerConfigs: []*kueuealpha.MultiKueueCapacityProviderConfig{
				utiltestingalpha.MakeMultiKueueCapacityProviderConfig("cp-config1").ManagerSubtreeRootRef(kueuealpha.ClusterQueueSubtreeRootRefKind, "cq1").Obj(),
			},
			cqs: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("cq1").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default").Resource("cpu", "1").Obj(),
						*utiltestingapi.MakeFlavorQuotas("other").Resource("cpu", "1").Obj(),
					).
					AdmissionChecks("ac1").
					Obj(),
			},
			acs: []*kueue.AdmissionCheck{
				utiltestingapi.MakeAdmissionCheck("ac1").
					ControllerName(kueue.MultiKueueControllerName).
					Parameters(kueue.SchemeGroupVersion.Group, "MultiKueueConfig", "config1").
					Obj(),
			},
			mkConfigs: []*kueue.MultiKueueConfig{
				utiltestingapi.MakeMultiKueueConfig("config1").Clusters("worker1").Obj(),
			},
			wantCP: utiltestingalpha.MakeCapacityProvider("cp1").
				ControllerName(kueue.MultiKueueControllerName).
				OrchestratedFlavors("default").
				Parameters(kueuealpha.SchemeGroupVersion.Group, testProviderConfigKind, "cp-config1").
				Generation(1).
				Condition(misconfiguredCondition(`ClusterQueue "cq1" must have exactly one ResourceFlavor`, 1)).
				Obj(),
			wantStatusPatches: 1,
		},
		"manager ClusterQueue with two ResourceGroups": {
			cp: utiltestingalpha.MakeCapacityProvider("cp1").
				ControllerName(kueue.MultiKueueControllerName).
				OrchestratedFlavors("default").
				Parameters(kueuealpha.SchemeGroupVersion.Group, testProviderConfigKind, "cp-config1").
				Generation(1).
				Obj(),
			providerConfigs: []*kueuealpha.MultiKueueCapacityProviderConfig{
				utiltestingalpha.MakeMultiKueueCapacityProviderConfig("cp-config1").ManagerSubtreeRootRef(kueuealpha.ClusterQueueSubtreeRootRefKind, "cq1").Obj(),
			},
			cqs: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("cq1").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource("cpu", "1").Obj()).
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("other").Resource("memory", "1Gi").Obj()).
					AdmissionChecks("ac1").
					Obj(),
			},
			acs: []*kueue.AdmissionCheck{
				utiltestingapi.MakeAdmissionCheck("ac1").
					ControllerName(kueue.MultiKueueControllerName).
					Parameters(kueue.SchemeGroupVersion.Group, "MultiKueueConfig", "config1").
					Obj(),
			},
			mkConfigs: []*kueue.MultiKueueConfig{
				utiltestingapi.MakeMultiKueueConfig("config1").Clusters("worker1").Obj(),
			},
			wantCP: utiltestingalpha.MakeCapacityProvider("cp1").
				ControllerName(kueue.MultiKueueControllerName).
				OrchestratedFlavors("default").
				Parameters(kueuealpha.SchemeGroupVersion.Group, testProviderConfigKind, "cp-config1").
				Generation(1).
				Condition(misconfiguredCondition(`ClusterQueue "cq1" must have exactly one ResourceFlavor`, 1)).
				Obj(),
			wantStatusPatches: 1,
		},
		"manager ResourceFlavor does not match orchestratedFlavors": {
			cp: utiltestingalpha.MakeCapacityProvider("cp1").
				ControllerName(kueue.MultiKueueControllerName).
				OrchestratedFlavors("other").
				Parameters(kueuealpha.SchemeGroupVersion.Group, testProviderConfigKind, "cp-config1").
				Generation(1).
				Obj(),
			providerConfigs: []*kueuealpha.MultiKueueCapacityProviderConfig{
				utiltestingalpha.MakeMultiKueueCapacityProviderConfig("cp-config1").ManagerSubtreeRootRef(kueuealpha.ClusterQueueSubtreeRootRefKind, "cq1").Obj(),
			},
			cqs: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("cq1").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource("cpu", "1").Obj()).
					AdmissionChecks("ac1").
					Obj(),
			},
			acs: []*kueue.AdmissionCheck{
				utiltestingapi.MakeAdmissionCheck("ac1").
					ControllerName(kueue.MultiKueueControllerName).
					Parameters(kueue.SchemeGroupVersion.Group, "MultiKueueConfig", "config1").
					Obj(),
			},
			mkConfigs: []*kueue.MultiKueueConfig{
				utiltestingapi.MakeMultiKueueConfig("config1").Clusters("worker1").Obj(),
			},
			wantCP: utiltestingalpha.MakeCapacityProvider("cp1").
				ControllerName(kueue.MultiKueueControllerName).
				OrchestratedFlavors("other").
				Parameters(kueuealpha.SchemeGroupVersion.Group, testProviderConfigKind, "cp-config1").
				Generation(1).
				Condition(misconfiguredCondition(`ClusterQueue "cq1" ResourceFlavor "default" does not match the ResourceFlavor "other" in spec.orchestratedFlavors`, 1)).
				Obj(),
			wantStatusPatches: 1,
		},
		"supported configuration": {
			cp: utiltestingalpha.MakeCapacityProvider("cp1").
				ControllerName(kueue.MultiKueueControllerName).
				OrchestratedFlavors("default").
				Parameters(kueuealpha.SchemeGroupVersion.Group, testProviderConfigKind, "cp-config1").
				Generation(1).
				Obj(),
			providerConfigs: []*kueuealpha.MultiKueueCapacityProviderConfig{
				utiltestingalpha.MakeMultiKueueCapacityProviderConfig("cp-config1").ManagerSubtreeRootRef(kueuealpha.ClusterQueueSubtreeRootRefKind, "cq1").Obj(),
			},
			cqs: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("cq1").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource("cpu", "1").Obj()).
					AdmissionChecks("ac1").
					Obj(),
			},
			acs: []*kueue.AdmissionCheck{
				utiltestingapi.MakeAdmissionCheck("ac1").
					ControllerName(kueue.MultiKueueControllerName).
					Parameters(kueue.SchemeGroupVersion.Group, "MultiKueueConfig", "config1").
					Obj(),
			},
			mkConfigs: []*kueue.MultiKueueConfig{
				utiltestingapi.MakeMultiKueueConfig("config1").Clusters("worker1").Obj(),
			},
			wantCP: utiltestingalpha.MakeCapacityProvider("cp1").
				ControllerName(kueue.MultiKueueControllerName).
				OrchestratedFlavors("default").
				Parameters(kueuealpha.SchemeGroupVersion.Group, testProviderConfigKind, "cp-config1").
				Generation(1).
				Obj(),
		},
		"unchanged Misconfigured condition is not written again": {
			cp: utiltestingalpha.MakeCapacityProvider("cp1").
				ControllerName(kueue.MultiKueueControllerName).
				OrchestratedFlavors("default").
				Parameters(kueuealpha.SchemeGroupVersion.Group, testProviderConfigKind, "cp-config1").
				Generation(1).
				Condition(misconfiguredCondition(`MultiKueueCapacityProviderConfig "cp-config1" not found`, 1)).
				Obj(),
			wantCP: utiltestingalpha.MakeCapacityProvider("cp1").
				ControllerName(kueue.MultiKueueControllerName).
				OrchestratedFlavors("default").
				Parameters(kueuealpha.SchemeGroupVersion.Group, testProviderConfigKind, "cp-config1").
				Generation(1).
				Condition(misconfiguredCondition(`MultiKueueCapacityProviderConfig "cp-config1" not found`, 1)).
				Obj(),
		},
		"Misconfigured condition of an older generation is updated": {
			cp: utiltestingalpha.MakeCapacityProvider("cp1").
				ControllerName(kueue.MultiKueueControllerName).
				OrchestratedFlavors("default").
				Parameters(kueuealpha.SchemeGroupVersion.Group, testProviderConfigKind, "cp-config1").
				Generation(2).
				Condition(misconfiguredCondition(`MultiKueueCapacityProviderConfig "cp-config1" not found`, 1)).
				Obj(),
			wantCP: utiltestingalpha.MakeCapacityProvider("cp1").
				ControllerName(kueue.MultiKueueControllerName).
				OrchestratedFlavors("default").
				Parameters(kueuealpha.SchemeGroupVersion.Group, testProviderConfigKind, "cp-config1").
				Generation(2).
				Condition(misconfiguredCondition(`MultiKueueCapacityProviderConfig "cp-config1" not found`, 2)).
				Obj(),
			wantStatusPatches: 1,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)

			gotStatusPatches := 0
			builder := utiltesting.NewClientBuilder().
				WithObjects(asObjs(tc.providerConfigs)...).
				WithObjects(asObjs(tc.cqs)...).
				WithObjects(asObjs(tc.acs)...).
				WithObjects(asObjs(tc.mkConfigs)...).
				WithStatusSubresource(&kueuealpha.CapacityProvider{}).
				WithInterceptorFuncs(interceptor.Funcs{
					SubResourcePatch: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
						if _, isCP := obj.(*kueuealpha.CapacityProvider); isCP {
							gotStatusPatches++
						}
						return c.SubResource(subResourceName).Patch(ctx, obj, patch, opts...)
					},
				})
			if tc.cp != nil {
				builder = builder.WithObjects(tc.cp)
			}
			if err := SetupIndexer(ctx, utiltesting.AsIndexer(builder), TestNamespace); err != nil {
				t.Fatalf("Failed to set up indexes: %v", err)
			}
			c := builder.Build()

			helper, err := admissioncheck.NewMultiKueueStoreHelper(c)
			if err != nil {
				t.Fatalf("Failed to create the MultiKueue store helper: %v", err)
			}
			reconciler := newCPReconciler(c, helper, nil, nil, 100*time.Millisecond)

			if _, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: "cp1"}}); err != nil {
				t.Errorf("Unexpected reconcile error: %v", err)
			}

			gotCP := &kueuealpha.CapacityProvider{}
			if err := c.Get(ctx, types.NamespacedName{Name: "cp1"}, gotCP); err != nil {
				if tc.wantCP != nil || !apierrors.IsNotFound(err) {
					t.Fatalf("Failed to get the CapacityProvider: %v", err)
				}
				gotCP = nil
			}
			if diff := cmp.Diff(tc.wantCP, gotCP,
				cmpopts.IgnoreTypes(metav1.TypeMeta{}),
				cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion"),
				cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime"),
				cmpopts.EquateEmpty(),
			); diff != "" {
				t.Errorf("Unexpected CapacityProvider (-want/+got):\n%s", diff)
			}
			if gotStatusPatches != tc.wantStatusPatches {
				t.Errorf("Unexpected number of CapacityProvider status patches, want %d, got %d", tc.wantStatusPatches, gotStatusPatches)
			}
		})
	}
}

func TestCPReconcilerEventHandlers(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.DQOMultiKueueCapacityProvider, true)

	cp := utiltestingalpha.MakeCapacityProvider("cp1").
		ControllerName(kueue.MultiKueueControllerName).
		OrchestratedFlavors("default").
		Parameters(kueuealpha.SchemeGroupVersion.Group, testProviderConfigKind, "cp-config1").
		Obj()
	// Not indexed, so it is never queued.
	otherCP := utiltestingalpha.MakeCapacityProvider("cp-other").
		ControllerName(testOtherController).
		OrchestratedFlavors("default").
		Parameters(kueuealpha.SchemeGroupVersion.Group, testProviderConfigKind, "cp-config1").
		Obj()
	providerConfig := utiltestingalpha.MakeMultiKueueCapacityProviderConfig("cp-config1").
		ManagerSubtreeRootRef(kueuealpha.ClusterQueueSubtreeRootRefKind, "cq1").
		Obj()
	cq := utiltestingapi.MakeClusterQueue("cq1").AdmissionChecks("ac1").Obj()
	otherCQ := utiltestingapi.MakeClusterQueue("cq2").Obj()
	ac := utiltestingapi.MakeAdmissionCheck("ac1").
		ControllerName(kueue.MultiKueueControllerName).
		Parameters(kueue.SchemeGroupVersion.Group, "MultiKueueConfig", "config1").
		Obj()
	mkConfig := utiltestingapi.MakeMultiKueueConfig("config1").Clusters("cluster1").Obj()
	cluster := utiltestingapi.MakeMultiKueueCluster("cluster1").Obj()
	lq := utiltestingapi.MakeLocalQueue("lq1", TestNamespace).ClusterQueue("cq1").Obj()
	otherLQ := utiltestingapi.MakeLocalQueue("lq2", TestNamespace).ClusterQueue("cq2").Obj()

	ctx, _ := utiltesting.ContextWithLog(t)
	builder := utiltesting.NewClientBuilder().
		WithObjects(cp, otherCP, providerConfig, cq, otherCQ, ac, mkConfig, cluster, lq, otherLQ)
	if err := SetupIndexer(ctx, utiltesting.AsIndexer(builder), TestNamespace); err != nil {
		t.Fatalf("Failed to set up indexes: %v", err)
	}
	r := &cpReconciler{client: builder.Build()}

	type mockQueue = utiltesting.MockTypedRateLimitingInterface

	cases := map[string]struct {
		handler       func(mockQ *mockQueue)
		wantReconcile bool
	}{
		"MultiKueueCapacityProviderConfig Create": {
			handler: func(mockQ *mockQueue) {
				(&cpProviderConfigHandler{reconciler: r}).Create(ctx, event.CreateEvent{Object: providerConfig}, mockQ)
			},
			wantReconcile: true,
		},
		"MultiKueueCapacityProviderConfig Update (relevant)": {
			handler: func(mockQ *mockQueue) {
				newProviderConfig := utiltestingalpha.MakeMultiKueueCapacityProviderConfig("cp-config1").
					ManagerSubtreeRootRef(kueuealpha.ClusterQueueSubtreeRootRefKind, "cq2").
					Obj()
				newProviderConfig.Generation = 1
				(&cpProviderConfigHandler{reconciler: r}).Update(ctx, event.UpdateEvent{ObjectOld: providerConfig, ObjectNew: newProviderConfig}, mockQ)
			},
			wantReconcile: true,
		},
		"MultiKueueCapacityProviderConfig Update (irrelevant)": {
			handler: func(mockQ *mockQueue) {
				(&cpProviderConfigHandler{reconciler: r}).Update(ctx, event.UpdateEvent{ObjectOld: providerConfig, ObjectNew: providerConfig}, mockQ)
			},
			wantReconcile: false,
		},
		"MultiKueueCapacityProviderConfig Delete": {
			handler: func(mockQ *mockQueue) {
				(&cpProviderConfigHandler{reconciler: r}).Delete(ctx, event.DeleteEvent{Object: providerConfig}, mockQ)
			},
			wantReconcile: true,
		},
		"ClusterQueue Create": {
			handler: func(mockQ *mockQueue) {
				(&cpCQHandler{reconciler: r}).Create(ctx, event.CreateEvent{Object: cq}, mockQ)
			},
			wantReconcile: true,
		},
		"ClusterQueue Create (not referenced)": {
			handler: func(mockQ *mockQueue) {
				(&cpCQHandler{reconciler: r}).Create(ctx, event.CreateEvent{Object: otherCQ}, mockQ)
			},
			wantReconcile: false,
		},
		"ClusterQueue Update (relevant)": {
			handler: func(mockQ *mockQueue) {
				newCQ := utiltestingapi.MakeClusterQueue("cq1").AdmissionChecks("ac1").Generation(1).Obj()
				(&cpCQHandler{reconciler: r}).Update(ctx, event.UpdateEvent{ObjectOld: cq, ObjectNew: newCQ}, mockQ)
			},
			wantReconcile: true,
		},
		"ClusterQueue Update (status only)": {
			handler: func(mockQ *mockQueue) {
				newCQ := utiltestingapi.MakeClusterQueue("cq1").
					AdmissionChecks("ac1").
					EffectiveQuotaStatus(utiltestingapi.MakeEffectiveQuotaStatus().Obj()).
					Obj()
				(&cpCQHandler{reconciler: r}).Update(ctx, event.UpdateEvent{ObjectOld: cq, ObjectNew: newCQ}, mockQ)
			},
			wantReconcile: false,
		},
		"ClusterQueue Delete": {
			handler: func(mockQ *mockQueue) {
				(&cpCQHandler{reconciler: r}).Delete(ctx, event.DeleteEvent{Object: cq}, mockQ)
			},
			wantReconcile: true,
		},
		"LocalQueue Create": {
			handler: func(mockQ *mockQueue) {
				(&cpLQHandler{reconciler: r}).Create(ctx, event.CreateEvent{Object: lq}, mockQ)
			},
			wantReconcile: true,
		},
		"LocalQueue Create (ClusterQueue not referenced)": {
			handler: func(mockQ *mockQueue) {
				(&cpLQHandler{reconciler: r}).Create(ctx, event.CreateEvent{Object: otherLQ}, mockQ)
			},
			wantReconcile: false,
		},
		"LocalQueue Update (irrelevant)": {
			handler: func(mockQ *mockQueue) {
				(&cpLQHandler{reconciler: r}).Update(ctx, event.UpdateEvent{ObjectOld: lq, ObjectNew: lq}, mockQ)
			},
			wantReconcile: false,
		},
		"LocalQueue Delete": {
			handler: func(mockQ *mockQueue) {
				(&cpLQHandler{reconciler: r}).Delete(ctx, event.DeleteEvent{Object: lq}, mockQ)
			},
			wantReconcile: true,
		},
		"AdmissionCheck Create": {
			handler: func(mockQ *mockQueue) {
				(&cpACHandler{reconciler: r}).Create(ctx, event.CreateEvent{Object: ac}, mockQ)
			},
			wantReconcile: true,
		},
		"AdmissionCheck Update (relevant)": {
			handler: func(mockQ *mockQueue) {
				newAC := utiltestingapi.MakeAdmissionCheck("ac1").
					ControllerName(kueue.MultiKueueControllerName).
					Parameters(kueue.SchemeGroupVersion.Group, "MultiKueueConfig", "config2").
					Generation(1).
					Obj()
				(&cpACHandler{reconciler: r}).Update(ctx, event.UpdateEvent{ObjectOld: ac, ObjectNew: newAC}, mockQ)
			},
			wantReconcile: true,
		},
		"AdmissionCheck Update (irrelevant)": {
			handler: func(mockQ *mockQueue) {
				(&cpACHandler{reconciler: r}).Update(ctx, event.UpdateEvent{ObjectOld: ac, ObjectNew: ac}, mockQ)
			},
			wantReconcile: false,
		},
		"AdmissionCheck Delete": {
			handler: func(mockQ *mockQueue) {
				(&cpACHandler{reconciler: r}).Delete(ctx, event.DeleteEvent{Object: ac}, mockQ)
			},
			wantReconcile: true,
		},
		"MultiKueueConfig Create": {
			handler: func(mockQ *mockQueue) {
				(&cpMKConfigHandler{reconciler: r}).Create(ctx, event.CreateEvent{Object: mkConfig}, mockQ)
			},
			wantReconcile: true,
		},
		"MultiKueueConfig Update (relevant)": {
			handler: func(mockQ *mockQueue) {
				newMKConfig := utiltestingapi.MakeMultiKueueConfig("config1").Clusters("cluster1", "cluster2").Obj()
				newMKConfig.Generation = 1
				(&cpMKConfigHandler{reconciler: r}).Update(ctx, event.UpdateEvent{ObjectOld: mkConfig, ObjectNew: newMKConfig}, mockQ)
			},
			wantReconcile: true,
		},
		"MultiKueueConfig Update (irrelevant)": {
			handler: func(mockQ *mockQueue) {
				(&cpMKConfigHandler{reconciler: r}).Update(ctx, event.UpdateEvent{ObjectOld: mkConfig, ObjectNew: mkConfig}, mockQ)
			},
			wantReconcile: false,
		},
		"MultiKueueConfig Delete": {
			handler: func(mockQ *mockQueue) {
				(&cpMKConfigHandler{reconciler: r}).Delete(ctx, event.DeleteEvent{Object: mkConfig}, mockQ)
			},
			wantReconcile: true,
		},
		"MultiKueueCluster Create": {
			handler: func(mockQ *mockQueue) {
				(&cpMKClusterHandler{reconciler: r}).Create(ctx, event.CreateEvent{Object: cluster}, mockQ)
			},
			wantReconcile: true,
		},
		"MultiKueueCluster Update (relevant)": {
			handler: func(mockQ *mockQueue) {
				newCluster := utiltestingapi.MakeMultiKueueCluster("cluster1").
					Active(metav1.ConditionTrue, "ByTest", "by test", 1).Obj()
				(&cpMKClusterHandler{reconciler: r}).Update(ctx, event.UpdateEvent{ObjectOld: cluster, ObjectNew: newCluster}, mockQ)
			},
			wantReconcile: true,
		},
		"MultiKueueCluster Update (irrelevant)": {
			handler: func(mockQ *mockQueue) {
				(&cpMKClusterHandler{reconciler: r}).Update(ctx, event.UpdateEvent{ObjectOld: cluster, ObjectNew: cluster}, mockQ)
			},
			wantReconcile: false,
		},
		"MultiKueueCluster Delete": {
			handler: func(mockQ *mockQueue) {
				(&cpMKClusterHandler{reconciler: r}).Delete(ctx, event.DeleteEvent{Object: cluster}, mockQ)
			},
			wantReconcile: true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			mockQ := &mockQueue{}
			tc.handler(mockQ)

			var wantItems []reconcile.Request
			if tc.wantReconcile {
				wantItems = []reconcile.Request{{NamespacedName: types.NamespacedName{Name: "cp1"}}}
			}
			if diff := cmp.Diff(wantItems, mockQ.Items, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Unexpected workqueue items (-want/+got):\n%s", diff)
			}
		})
	}
}
