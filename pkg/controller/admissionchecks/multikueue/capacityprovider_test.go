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
	"errors"
	"fmt"
	"maps"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobs"
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

type cpWorkerState struct {
	lqs          []*kueue.LocalQueue
	cqs          []*kueue.ClusterQueue
	disconnected bool
	listErr      error
	// clusterDeleted means that the MultiKueueCluster object is gone, but the
	// clusters reconciler did not remove the remote client yet.
	clusterDeleted bool
}

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
	sourceUnavailableCondition := func(message string, generation int64) metav1.Condition {
		return metav1.Condition{
			Type:               kueuealpha.CapacityProviderCapacitySynchronized,
			Status:             metav1.ConditionFalse,
			Reason:             kueuealpha.CapacityProviderReasonSourceUnavailable,
			Message:            message,
			ObservedGeneration: generation,
		}
	}
	synchronizedCondition := func(message string, generation int64) metav1.Condition {
		return metav1.Condition{
			Type:               kueuealpha.CapacityProviderCapacitySynchronized,
			Status:             metav1.ConditionTrue,
			Reason:             kueuealpha.CapacityProviderReasonSynchronized,
			Message:            message,
			ObservedGeneration: generation,
		}
	}

	errWorkerList := errors.New("worker API transient failure")
	now := time.Now()

	cases := map[string]struct {
		cp                      *kueuealpha.CapacityProvider
		providerConfigs         []*kueuealpha.MultiKueueCapacityProviderConfig
		cqs                     []*kueue.ClusterQueue
		lqs                     []*kueue.LocalQueue
		acs                     []*kueue.AdmissionCheck
		mkConfigs               []*kueue.MultiKueueConfig
		workers                 map[string]cpWorkerState
		workerResourceCount     int
		deleteBeforeStatusPatch bool

		wantCP               *kueuealpha.CapacityProvider
		wantStatusPatches    int
		wantMessageTruncated bool
		wantErr              error
	}{
		"CapacityProvider not found": {},
		"CapacityProvider being deleted is ignored": {
			cp: utiltestingalpha.MakeCapacityProvider("cp1").
				ControllerName(kueue.MultiKueueControllerName).
				OrchestratedFlavors("default").
				Finalizers("example.com/finalizer").
				DeletionTimestamp(now).
				Generation(1).
				Obj(),
			wantCP: utiltestingalpha.MakeCapacityProvider("cp1").
				ControllerName(kueue.MultiKueueControllerName).
				OrchestratedFlavors("default").
				Finalizers("example.com/finalizer").
				DeletionTimestamp(now).
				Generation(1).
				Obj(),
		},
		"concurrent deletion before status patch is ignored": {
			cp: utiltestingalpha.MakeCapacityProvider("cp1").
				ControllerName(kueue.MultiKueueControllerName).
				OrchestratedFlavors("default").
				Generation(1).
				Obj(),
			deleteBeforeStatusPatch: true,
			wantStatusPatches:       1,
		},
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
		"multiple resources for single LQs and CQs": {
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
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource("cpu", "1").Resource("memory", "1Gi").Obj()).
					AdmissionChecks("ac1").
					Obj(),
			},
			lqs: []*kueue.LocalQueue{
				utiltestingapi.MakeLocalQueue("lq1", TestNamespace).ClusterQueue("cq1").Obj(),
			},
			acs: []*kueue.AdmissionCheck{
				utiltestingapi.MakeAdmissionCheck("ac1").
					ControllerName(kueue.MultiKueueControllerName).
					Parameters(kueue.SchemeGroupVersion.Group, "MultiKueueConfig", "config1").
					Obj(),
			},
			mkConfigs: []*kueue.MultiKueueConfig{
				utiltestingapi.MakeMultiKueueConfig("config1").Clusters("worker1", "worker2").Obj(),
			},
			workers: map[string]cpWorkerState{
				"worker1": {
					lqs: []*kueue.LocalQueue{utiltestingapi.MakeLocalQueue("lq1", TestNamespace).ClusterQueue("w1-cq1").Obj()},
					cqs: []*kueue.ClusterQueue{
						utiltestingapi.MakeClusterQueue("w1-cq1").
							ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource("cpu", "10", "5", "2").Resource("memory", "20Gi").Obj()).
							Obj(),
					},
				},
				"worker2": {
					lqs: []*kueue.LocalQueue{utiltestingapi.MakeLocalQueue("lq1", TestNamespace).ClusterQueue("w2-cq1").Obj()},
					cqs: []*kueue.ClusterQueue{
						utiltestingapi.MakeClusterQueue("w2-cq1").
							ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource("cpu", "5").Resource("memory", "10Gi").Obj()).
							Obj(),
					},
				},
			},
			wantCP: utiltestingalpha.MakeCapacityProvider("cp1").
				ControllerName(kueue.MultiKueueControllerName).
				OrchestratedFlavors("default").
				Parameters(kueuealpha.SchemeGroupVersion.Group, testProviderConfigKind, "cp-config1").
				Generation(1).
				Capacity(utiltestingalpha.MakeNormalizedCapacity().
					Flavors(utiltestingalpha.MakeNormalizedCapacityFlavor("default").Resource("cpu", "15").Resource("memory", "30Gi").Obj()).
					Obj(),
				).
				Condition(synchronizedCondition("Capacity synchronized from MultiKueue worker clusters", 1)).
				Obj(),
			wantStatusPatches: 1,
		},
		"unrelated CQs, missing worker CQs, and disconnected or missing workers": {
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
			lqs: []*kueue.LocalQueue{
				utiltestingapi.MakeLocalQueue("lq1", TestNamespace).ClusterQueue("cq1").Obj(),
				utiltestingapi.MakeLocalQueue("lq-dangling", TestNamespace).ClusterQueue("cq1").Obj(),
			},
			acs: []*kueue.AdmissionCheck{
				utiltestingapi.MakeAdmissionCheck("ac1").
					ControllerName(kueue.MultiKueueControllerName).
					Parameters(kueue.SchemeGroupVersion.Group, "MultiKueueConfig", "config1").
					Obj(),
			},
			mkConfigs: []*kueue.MultiKueueConfig{
				// Listed out of order to verify deterministic sorting of disconnected workers.
				utiltestingapi.MakeMultiKueueConfig("config1").Clusters("worker3", "worker1", "worker2").Obj(),
			},
			workers: map[string]cpWorkerState{
				"worker1": {
					lqs: []*kueue.LocalQueue{
						utiltestingapi.MakeLocalQueue("lq1", TestNamespace).ClusterQueue("w1-cq1").Obj(),
						utiltestingapi.MakeLocalQueue("lq-dangling", TestNamespace).ClusterQueue("w1-cq-missing").Obj(),
					},
					cqs: []*kueue.ClusterQueue{
						utiltestingapi.MakeClusterQueue("w1-cq1").
							ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource("cpu", "10").Obj()).
							Obj(),
						utiltestingapi.MakeClusterQueue("w1-cq-unrelated").
							ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource("cpu", "100").Obj()).
							Obj(),
					},
				},
				"worker2": {
					lqs: []*kueue.LocalQueue{utiltestingapi.MakeLocalQueue("lq1", TestNamespace).ClusterQueue("w2-cq1").Obj()},
					cqs: []*kueue.ClusterQueue{
						// Extra uncovered resource "tpu" on a disconnected worker does not trigger Misconfigured.
						utiltestingapi.MakeClusterQueue("w2-cq1").
							ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource("cpu", "500").Resource("tpu", "4").Obj()).
							Obj(),
					},
					disconnected: true,
				},
				// "worker3" is omitted from workers (client not found).
			},
			wantCP: utiltestingalpha.MakeCapacityProvider("cp1").
				ControllerName(kueue.MultiKueueControllerName).
				OrchestratedFlavors("default").
				Parameters(kueuealpha.SchemeGroupVersion.Group, testProviderConfigKind, "cp-config1").
				Generation(1).
				Capacity(utiltestingalpha.MakeNormalizedCapacity().
					Flavors(utiltestingalpha.MakeNormalizedCapacityFlavor("default").Resource("cpu", "10").Obj()).
					Obj(),
				).
				Condition(synchronizedCondition("Capacity synchronized from connected MultiKueue worker clusters; disconnected worker clusters: [worker2 worker3]", 1)).
				Obj(),
			wantStatusPatches: 1,
		},
		"deleted MultiKueueCluster is skipped before the clusters reconciler removes its remote client": {
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
			lqs: []*kueue.LocalQueue{
				utiltestingapi.MakeLocalQueue("lq1", TestNamespace).ClusterQueue("cq1").Obj(),
			},
			acs: []*kueue.AdmissionCheck{
				utiltestingapi.MakeAdmissionCheck("ac1").
					ControllerName(kueue.MultiKueueControllerName).
					Parameters(kueue.SchemeGroupVersion.Group, "MultiKueueConfig", "config1").
					Obj(),
			},
			mkConfigs: []*kueue.MultiKueueConfig{
				utiltestingapi.MakeMultiKueueConfig("config1").Clusters("worker1", "worker2").Obj(),
			},
			workers: map[string]cpWorkerState{
				"worker1": {
					lqs: []*kueue.LocalQueue{utiltestingapi.MakeLocalQueue("lq1", TestNamespace).ClusterQueue("w1-cq1").Obj()},
					cqs: []*kueue.ClusterQueue{
						utiltestingapi.MakeClusterQueue("w1-cq1").
							ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource("cpu", "10").Obj()).
							Obj(),
					},
				},
				"worker2": {
					lqs: []*kueue.LocalQueue{utiltestingapi.MakeLocalQueue("lq1", TestNamespace).ClusterQueue("w2-cq1").Obj()},
					cqs: []*kueue.ClusterQueue{
						utiltestingapi.MakeClusterQueue("w2-cq1").
							ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource("cpu", "500").Obj()).
							Obj(),
					},
					clusterDeleted: true,
				},
			},
			wantCP: utiltestingalpha.MakeCapacityProvider("cp1").
				ControllerName(kueue.MultiKueueControllerName).
				OrchestratedFlavors("default").
				Parameters(kueuealpha.SchemeGroupVersion.Group, testProviderConfigKind, "cp-config1").
				Generation(1).
				Capacity(utiltestingalpha.MakeNormalizedCapacity().
					Flavors(utiltestingalpha.MakeNormalizedCapacityFlavor("default").Resource("cpu", "10").Obj()).
					Obj(),
				).
				Condition(synchronizedCondition("Capacity synchronized from connected MultiKueue worker clusters; disconnected worker clusters: [worker2]", 1)).
				Obj(),
			wantStatusPatches: 1,
		},
		"multiple LQs pointing to same and different CQs on workers": {
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
			lqs: []*kueue.LocalQueue{
				utiltestingapi.MakeLocalQueue("lq1", TestNamespace).ClusterQueue("cq1").Obj(),
				utiltestingapi.MakeLocalQueue("lq2", TestNamespace).ClusterQueue("cq1").Obj(),
			},
			acs: []*kueue.AdmissionCheck{
				utiltestingapi.MakeAdmissionCheck("ac1").
					ControllerName(kueue.MultiKueueControllerName).
					Parameters(kueue.SchemeGroupVersion.Group, "MultiKueueConfig", "config1").
					Obj(),
			},
			mkConfigs: []*kueue.MultiKueueConfig{
				utiltestingapi.MakeMultiKueueConfig("config1").Clusters("worker1", "worker2").Obj(),
			},
			workers: map[string]cpWorkerState{
				"worker1": {
					lqs: []*kueue.LocalQueue{
						utiltestingapi.MakeLocalQueue("lq1", TestNamespace).ClusterQueue("w1-cq1").Obj(),
						utiltestingapi.MakeLocalQueue("lq2", TestNamespace).ClusterQueue("w1-cq1").Obj(),
					},
					cqs: []*kueue.ClusterQueue{
						utiltestingapi.MakeClusterQueue("w1-cq1").
							ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource("cpu", "10").Obj()).
							Obj(),
					},
				},
				"worker2": {
					lqs: []*kueue.LocalQueue{
						utiltestingapi.MakeLocalQueue("lq1", TestNamespace).ClusterQueue("w2-cq1").Obj(),
						utiltestingapi.MakeLocalQueue("lq2", TestNamespace).ClusterQueue("w2-cq2").Obj(),
					},
					cqs: []*kueue.ClusterQueue{
						utiltestingapi.MakeClusterQueue("w2-cq1").
							ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource("cpu", "5").Obj()).
							Obj(),
						utiltestingapi.MakeClusterQueue("w2-cq2").
							ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource("cpu", "8").Obj()).
							Obj(),
					},
				},
			},
			wantCP: utiltestingalpha.MakeCapacityProvider("cp1").
				ControllerName(kueue.MultiKueueControllerName).
				OrchestratedFlavors("default").
				Parameters(kueuealpha.SchemeGroupVersion.Group, testProviderConfigKind, "cp-config1").
				Generation(1).
				Capacity(utiltestingalpha.MakeNormalizedCapacity().
					Flavors(utiltestingalpha.MakeNormalizedCapacityFlavor("default").Resource("cpu", "23").Obj()).
					Obj(),
				).
				Condition(synchronizedCondition("Capacity synchronized from MultiKueue worker clusters", 1)).
				Obj(),
			wantStatusPatches: 1,
		},
		"multiple flavors and incompatible ResourceGroups across workers": {
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
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource("cpu", "1").Resource("gpu", "1").Obj()).
					AdmissionChecks("ac1").
					Obj(),
			},
			lqs: []*kueue.LocalQueue{
				utiltestingapi.MakeLocalQueue("lq1", TestNamespace).ClusterQueue("cq1").Obj(),
			},
			acs: []*kueue.AdmissionCheck{
				utiltestingapi.MakeAdmissionCheck("ac1").
					ControllerName(kueue.MultiKueueControllerName).
					Parameters(kueue.SchemeGroupVersion.Group, "MultiKueueConfig", "config1").
					Obj(),
			},
			mkConfigs: []*kueue.MultiKueueConfig{
				utiltestingapi.MakeMultiKueueConfig("config1").Clusters("worker1", "worker2").Obj(),
			},
			workers: map[string]cpWorkerState{
				"worker1": {
					lqs: []*kueue.LocalQueue{utiltestingapi.MakeLocalQueue("lq1", TestNamespace).ClusterQueue("w1-cq1").Obj()},
					cqs: []*kueue.ClusterQueue{
						utiltestingapi.MakeClusterQueue("w1-cq1").
							ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource("cpu", "10").Obj()).
							ResourceGroup(*utiltestingapi.MakeFlavorQuotas("gpu").Resource("gpu", "1").Obj()).
							Obj(),
					},
				},
				"worker2": {
					lqs: []*kueue.LocalQueue{utiltestingapi.MakeLocalQueue("lq1", TestNamespace).ClusterQueue("w2-cq1").Obj()},
					cqs: []*kueue.ClusterQueue{
						utiltestingapi.MakeClusterQueue("w2-cq1").
							ResourceGroup(
								*utiltestingapi.MakeFlavorQuotas("default").Resource("cpu", "5").Resource("gpu", "2").Obj(),
								*utiltestingapi.MakeFlavorQuotas("other").Resource("cpu", "4").Resource("gpu", "1").Obj(),
							).
							Obj(),
					},
				},
			},
			wantCP: utiltestingalpha.MakeCapacityProvider("cp1").
				ControllerName(kueue.MultiKueueControllerName).
				OrchestratedFlavors("default").
				Parameters(kueuealpha.SchemeGroupVersion.Group, testProviderConfigKind, "cp-config1").
				Generation(1).
				Capacity(utiltestingalpha.MakeNormalizedCapacity().
					Flavors(utiltestingalpha.MakeNormalizedCapacityFlavor("default").Resource("cpu", "19").Resource("gpu", "4").Obj()).
					Obj(),
				).
				Condition(synchronizedCondition("Capacity synchronized from MultiKueue worker clusters", 1)).
				Obj(),
			wantStatusPatches: 1,
		},
		"workers with a subset of manager resources report explicit zero for missing resource": {
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
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource("cpu", "1").Resource("memory", "1Gi").Resource("gpu", "1").Obj()).
					AdmissionChecks("ac1").
					Obj(),
			},
			lqs: []*kueue.LocalQueue{
				utiltestingapi.MakeLocalQueue("lq1", TestNamespace).ClusterQueue("cq1").Obj(),
			},
			acs: []*kueue.AdmissionCheck{
				utiltestingapi.MakeAdmissionCheck("ac1").
					ControllerName(kueue.MultiKueueControllerName).
					Parameters(kueue.SchemeGroupVersion.Group, "MultiKueueConfig", "config1").
					Obj(),
			},
			mkConfigs: []*kueue.MultiKueueConfig{
				utiltestingapi.MakeMultiKueueConfig("config1").Clusters("worker1", "worker2").Obj(),
			},
			workers: map[string]cpWorkerState{
				"worker1": {
					lqs: []*kueue.LocalQueue{utiltestingapi.MakeLocalQueue("lq1", TestNamespace).ClusterQueue("w1-cq1").Obj()},
					cqs: []*kueue.ClusterQueue{
						utiltestingapi.MakeClusterQueue("w1-cq1").
							ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource("cpu", "10").Resource("memory", "20Gi").Obj()).
							Obj(),
					},
				},
				"worker2": {
					lqs: []*kueue.LocalQueue{utiltestingapi.MakeLocalQueue("lq1", TestNamespace).ClusterQueue("w2-cq1").Obj()},
					cqs: []*kueue.ClusterQueue{
						utiltestingapi.MakeClusterQueue("w2-cq1").
							ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource("cpu", "5").Obj()).
							Obj(),
					},
				},
			},
			wantCP: utiltestingalpha.MakeCapacityProvider("cp1").
				ControllerName(kueue.MultiKueueControllerName).
				OrchestratedFlavors("default").
				Parameters(kueuealpha.SchemeGroupVersion.Group, testProviderConfigKind, "cp-config1").
				Generation(1).
				Capacity(utiltestingalpha.MakeNormalizedCapacity().
					Flavors(utiltestingalpha.MakeNormalizedCapacityFlavor("default").Resource("cpu", "15").Resource("memory", "20Gi").Resource("gpu", "0").Obj()).
					Obj(),
				).
				Condition(synchronizedCondition("Capacity synchronized from MultiKueue worker clusters", 1)).
				Obj(),
			wantStatusPatches: 1,
		},
		"worker status.effectiveQuotas takes precedence over spec.resourceGroups": {
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
			lqs: []*kueue.LocalQueue{
				utiltestingapi.MakeLocalQueue("lq1", TestNamespace).ClusterQueue("cq1").Obj(),
			},
			acs: []*kueue.AdmissionCheck{
				utiltestingapi.MakeAdmissionCheck("ac1").
					ControllerName(kueue.MultiKueueControllerName).
					Parameters(kueue.SchemeGroupVersion.Group, "MultiKueueConfig", "config1").
					Obj(),
			},
			mkConfigs: []*kueue.MultiKueueConfig{
				utiltestingapi.MakeMultiKueueConfig("config1").Clusters("worker1", "worker2").Obj(),
			},
			workers: map[string]cpWorkerState{
				"worker1": {
					lqs: []*kueue.LocalQueue{utiltestingapi.MakeLocalQueue("lq1", TestNamespace).ClusterQueue("w1-cq1").Obj()},
					cqs: []*kueue.ClusterQueue{
						utiltestingapi.MakeClusterQueue("w1-cq1").
							ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource("cpu", "100").Obj()).
							EffectiveQuotas(*utiltestingapi.MakeFlavorQuotas("default").Resource("cpu", "7").Obj()).
							Obj(),
					},
				},
				"worker2": {
					lqs: []*kueue.LocalQueue{utiltestingapi.MakeLocalQueue("lq1", TestNamespace).ClusterQueue("w2-cq1").Obj()},
					cqs: []*kueue.ClusterQueue{
						utiltestingapi.MakeClusterQueue("w2-cq1").
							ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource("cpu", "5").Obj()).
							Obj(),
					},
				},
			},
			wantCP: utiltestingalpha.MakeCapacityProvider("cp1").
				ControllerName(kueue.MultiKueueControllerName).
				OrchestratedFlavors("default").
				Parameters(kueuealpha.SchemeGroupVersion.Group, testProviderConfigKind, "cp-config1").
				Generation(1).
				Capacity(utiltestingalpha.MakeNormalizedCapacity().
					Flavors(utiltestingalpha.MakeNormalizedCapacityFlavor("default").Resource("cpu", "12").Obj()).
					Obj(),
				).
				Condition(synchronizedCondition("Capacity synchronized from MultiKueue worker clusters", 1)).
				Obj(),
			wantStatusPatches: 1,
		},
		"worker status.effectiveQuotas with empty resourceGroups contributes zero": {
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
			lqs: []*kueue.LocalQueue{
				utiltestingapi.MakeLocalQueue("lq1", TestNamespace).ClusterQueue("cq1").Obj(),
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
			workers: map[string]cpWorkerState{
				"worker1": {
					lqs: []*kueue.LocalQueue{utiltestingapi.MakeLocalQueue("lq1", TestNamespace).ClusterQueue("w1-cq1").Obj()},
					cqs: []*kueue.ClusterQueue{
						utiltestingapi.MakeClusterQueue("w1-cq1").
							ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource("cpu", "100").Obj()).
							EffectiveQuotaStatus(utiltestingapi.MakeEffectiveQuotaStatus().Obj()).
							Obj(),
					},
				},
			},
			wantCP: utiltestingalpha.MakeCapacityProvider("cp1").
				ControllerName(kueue.MultiKueueControllerName).
				OrchestratedFlavors("default").
				Parameters(kueuealpha.SchemeGroupVersion.Group, testProviderConfigKind, "cp-config1").
				Generation(1).
				Capacity(utiltestingalpha.MakeNormalizedCapacity().
					Flavors(utiltestingalpha.MakeNormalizedCapacityFlavor("default").Resource("cpu", "0").Obj()).
					Obj(),
				).
				Condition(synchronizedCondition("Capacity synchronized from MultiKueue worker clusters", 1)).
				Obj(),
			wantStatusPatches: 1,
		},
		"deprecated MultiKueueConfig spec.quotaManagement Manual is ignored": {
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
			lqs: []*kueue.LocalQueue{
				utiltestingapi.MakeLocalQueue("lq1", TestNamespace).ClusterQueue("cq1").Obj(),
			},
			acs: []*kueue.AdmissionCheck{
				utiltestingapi.MakeAdmissionCheck("ac1").
					ControllerName(kueue.MultiKueueControllerName).
					Parameters(kueue.SchemeGroupVersion.Group, "MultiKueueConfig", "config1").
					Obj(),
			},
			mkConfigs: []*kueue.MultiKueueConfig{
				utiltestingapi.MakeMultiKueueConfig("config1").
					Clusters("worker1").
					QuotaManagement(kueue.MultiKueueConfigQuotaManagementMode("Manual")).
					Obj(),
			},
			workers: map[string]cpWorkerState{
				"worker1": {
					lqs: []*kueue.LocalQueue{utiltestingapi.MakeLocalQueue("lq1", TestNamespace).ClusterQueue("w1-cq1").Obj()},
					cqs: []*kueue.ClusterQueue{
						utiltestingapi.MakeClusterQueue("w1-cq1").
							ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource("cpu", "10").Obj()).
							Obj(),
					},
				},
			},
			wantCP: utiltestingalpha.MakeCapacityProvider("cp1").
				ControllerName(kueue.MultiKueueControllerName).
				OrchestratedFlavors("default").
				Parameters(kueuealpha.SchemeGroupVersion.Group, testProviderConfigKind, "cp-config1").
				Generation(1).
				Capacity(utiltestingalpha.MakeNormalizedCapacity().
					Flavors(utiltestingalpha.MakeNormalizedCapacityFlavor("default").Resource("cpu", "10").Obj()).
					Obj(),
				).
				Condition(synchronizedCondition("Capacity synchronized from MultiKueue worker clusters", 1)).
				Obj(),
			wantStatusPatches: 1,
		},
		"all workers disconnected sets SourceUnavailable and preserves existing capacity": {
			cp: utiltestingalpha.MakeCapacityProvider("cp1").
				ControllerName(kueue.MultiKueueControllerName).
				OrchestratedFlavors("default").
				Parameters(kueuealpha.SchemeGroupVersion.Group, testProviderConfigKind, "cp-config1").
				Generation(1).
				Capacity(utiltestingalpha.MakeNormalizedCapacity().
					Flavors(utiltestingalpha.MakeNormalizedCapacityFlavor("default").Resource("cpu", "10").Obj()).
					Obj(),
				).
				Condition(synchronizedCondition("Capacity synchronized from MultiKueue worker clusters", 1)).
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
			lqs: []*kueue.LocalQueue{
				utiltestingapi.MakeLocalQueue("lq1", TestNamespace).ClusterQueue("cq1").Obj(),
			},
			acs: []*kueue.AdmissionCheck{
				utiltestingapi.MakeAdmissionCheck("ac1").
					ControllerName(kueue.MultiKueueControllerName).
					Parameters(kueue.SchemeGroupVersion.Group, "MultiKueueConfig", "config1").
					Obj(),
			},
			mkConfigs: []*kueue.MultiKueueConfig{
				utiltestingapi.MakeMultiKueueConfig("config1").Clusters("worker2", "worker1").Obj(),
			},
			workers: map[string]cpWorkerState{
				"worker1": {
					disconnected: true,
				},
				// "worker2" omitted (client not found).
			},
			wantCP: utiltestingalpha.MakeCapacityProvider("cp1").
				ControllerName(kueue.MultiKueueControllerName).
				OrchestratedFlavors("default").
				Parameters(kueuealpha.SchemeGroupVersion.Group, testProviderConfigKind, "cp-config1").
				Generation(1).
				Capacity(utiltestingalpha.MakeNormalizedCapacity().
					Flavors(utiltestingalpha.MakeNormalizedCapacityFlavor("default").Resource("cpu", "10").Obj()).
					Obj(),
				).
				Condition(sourceUnavailableCondition("No MultiKueue worker clusters are connected; disconnected worker clusters: [worker1 worker2]", 1)).
				Obj(),
			wantStatusPatches: 1,
		},
		"missing manager coveredResources sets Misconfigured and preserves existing capacity": {
			cp: utiltestingalpha.MakeCapacityProvider("cp1").
				ControllerName(kueue.MultiKueueControllerName).
				OrchestratedFlavors("default").
				Parameters(kueuealpha.SchemeGroupVersion.Group, testProviderConfigKind, "cp-config1").
				Generation(1).
				Capacity(utiltestingalpha.MakeNormalizedCapacity().
					Flavors(utiltestingalpha.MakeNormalizedCapacityFlavor("default").Resource("cpu", "10").Obj()).
					Obj(),
				).
				Condition(synchronizedCondition("Capacity synchronized from MultiKueue worker clusters", 1)).
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
			lqs: []*kueue.LocalQueue{
				utiltestingapi.MakeLocalQueue("lq1", TestNamespace).ClusterQueue("cq1").Obj(),
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
			workers: map[string]cpWorkerState{
				"worker1": {
					lqs: []*kueue.LocalQueue{utiltestingapi.MakeLocalQueue("lq1", TestNamespace).ClusterQueue("w1-cq1").Obj()},
					cqs: []*kueue.ClusterQueue{
						utiltestingapi.MakeClusterQueue("w1-cq1").
							ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource("memory", "10Gi").Resource("gpu", "2").Obj()).
							Obj(),
					},
				},
			},
			wantCP: utiltestingalpha.MakeCapacityProvider("cp1").
				ControllerName(kueue.MultiKueueControllerName).
				OrchestratedFlavors("default").
				Parameters(kueuealpha.SchemeGroupVersion.Group, testProviderConfigKind, "cp-config1").
				Generation(1).
				Capacity(utiltestingalpha.MakeNormalizedCapacity().
					Flavors(utiltestingalpha.MakeNormalizedCapacityFlavor("default").Resource("cpu", "10").Obj()).
					Obj(),
				).
				Condition(misconfiguredCondition("manager-side coveredResources is missing resources configured on workers: [gpu memory]", 1)).
				Obj(),
			wantStatusPatches: 1,
		},
		"condition message is truncated at 32 KiB": {
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
			workers:             map[string]cpWorkerState{"worker1": {}},
			workerResourceCount: 550, // Enough valid resource names to exceed the 32-KiB condition message limit.
			wantCP: utiltestingalpha.MakeCapacityProvider("cp1").
				ControllerName(kueue.MultiKueueControllerName).
				OrchestratedFlavors("default").
				Parameters(kueuealpha.SchemeGroupVersion.Group, testProviderConfigKind, "cp-config1").
				Generation(1).
				Condition(misconfiguredCondition("", 1)).
				Obj(),
			wantStatusPatches:    1,
			wantMessageTruncated: true,
		},
		"remote List error on connected worker fails reconcile without updating status": {
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
			lqs: []*kueue.LocalQueue{
				utiltestingapi.MakeLocalQueue("lq1", TestNamespace).ClusterQueue("cq1").Obj(),
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
			workers: map[string]cpWorkerState{
				"worker1": {
					listErr: errWorkerList,
				},
			},
			wantCP: utiltestingalpha.MakeCapacityProvider("cp1").
				ControllerName(kueue.MultiKueueControllerName).
				OrchestratedFlavors("default").
				Parameters(kueuealpha.SchemeGroupVersion.Group, testProviderConfigKind, "cp-config1").
				Generation(1).
				Obj(),
			wantErr: errWorkerList,
		},
		"unchanged Synchronized status is not written again": {
			cp: utiltestingalpha.MakeCapacityProvider("cp1").
				ControllerName(kueue.MultiKueueControllerName).
				OrchestratedFlavors("default").
				Parameters(kueuealpha.SchemeGroupVersion.Group, testProviderConfigKind, "cp-config1").
				Generation(1).
				Capacity(utiltestingalpha.MakeNormalizedCapacity().
					Flavors(utiltestingalpha.MakeNormalizedCapacityFlavor("default").Resource("cpu", "10").Obj()).
					Obj(),
				).
				Condition(synchronizedCondition("Capacity synchronized from MultiKueue worker clusters", 1)).
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
			lqs: []*kueue.LocalQueue{
				utiltestingapi.MakeLocalQueue("lq1", TestNamespace).ClusterQueue("cq1").Obj(),
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
			workers: map[string]cpWorkerState{
				"worker1": {
					lqs: []*kueue.LocalQueue{utiltestingapi.MakeLocalQueue("lq1", TestNamespace).ClusterQueue("w1-cq1").Obj()},
					cqs: []*kueue.ClusterQueue{
						utiltestingapi.MakeClusterQueue("w1-cq1").
							ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource("cpu", "10").Obj()).
							Obj(),
					},
				},
			},
			wantCP: utiltestingalpha.MakeCapacityProvider("cp1").
				ControllerName(kueue.MultiKueueControllerName).
				OrchestratedFlavors("default").
				Parameters(kueuealpha.SchemeGroupVersion.Group, testProviderConfigKind, "cp-config1").
				Generation(1).
				Capacity(utiltestingalpha.MakeNormalizedCapacity().
					Flavors(utiltestingalpha.MakeNormalizedCapacityFlavor("default").Resource("cpu", "10").Obj()).
					Obj(),
				).
				Condition(synchronizedCondition("Capacity synchronized from MultiKueue worker clusters", 1)).
				Obj(),
		},
		"Synchronized status of an older generation is updated even when capacity is unchanged": {
			cp: utiltestingalpha.MakeCapacityProvider("cp1").
				ControllerName(kueue.MultiKueueControllerName).
				OrchestratedFlavors("default").
				Parameters(kueuealpha.SchemeGroupVersion.Group, testProviderConfigKind, "cp-config1").
				Generation(2).
				Capacity(utiltestingalpha.MakeNormalizedCapacity().
					Flavors(utiltestingalpha.MakeNormalizedCapacityFlavor("default").Resource("cpu", "10").Obj()).
					Obj(),
				).
				Condition(synchronizedCondition("Capacity synchronized from MultiKueue worker clusters", 1)).
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
			lqs: []*kueue.LocalQueue{
				utiltestingapi.MakeLocalQueue("lq1", TestNamespace).ClusterQueue("cq1").Obj(),
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
			workers: map[string]cpWorkerState{
				"worker1": {
					lqs: []*kueue.LocalQueue{utiltestingapi.MakeLocalQueue("lq1", TestNamespace).ClusterQueue("w1-cq1").Obj()},
					cqs: []*kueue.ClusterQueue{
						utiltestingapi.MakeClusterQueue("w1-cq1").
							ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource("cpu", "10").Obj()).
							Obj(),
					},
				},
			},
			wantCP: utiltestingalpha.MakeCapacityProvider("cp1").
				ControllerName(kueue.MultiKueueControllerName).
				OrchestratedFlavors("default").
				Parameters(kueuealpha.SchemeGroupVersion.Group, testProviderConfigKind, "cp-config1").
				Generation(2).
				Capacity(utiltestingalpha.MakeNormalizedCapacity().
					Flavors(utiltestingalpha.MakeNormalizedCapacityFlavor("default").Resource("cpu", "10").Obj()).
					Obj(),
				).
				Condition(synchronizedCondition("Capacity synchronized from MultiKueue worker clusters", 2)).
				Obj(),
			wantStatusPatches: 1,
		},
		"existing capacity is replaced when worker quotas or coveredResources change": {
			cp: utiltestingalpha.MakeCapacityProvider("cp1").
				ControllerName(kueue.MultiKueueControllerName).
				OrchestratedFlavors("default").
				Parameters(kueuealpha.SchemeGroupVersion.Group, testProviderConfigKind, "cp-config1").
				Generation(1).
				Capacity(utiltestingalpha.MakeNormalizedCapacity().
					Flavors(utiltestingalpha.MakeNormalizedCapacityFlavor("default").Resource("cpu", "10").Resource("old-resource", "5").Obj()).
					Obj(),
				).
				Condition(synchronizedCondition("Capacity synchronized from MultiKueue worker clusters", 1)).
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
			lqs: []*kueue.LocalQueue{
				utiltestingapi.MakeLocalQueue("lq1", TestNamespace).ClusterQueue("cq1").Obj(),
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
			workers: map[string]cpWorkerState{
				"worker1": {
					lqs: []*kueue.LocalQueue{utiltestingapi.MakeLocalQueue("lq1", TestNamespace).ClusterQueue("w1-cq1").Obj()},
					cqs: []*kueue.ClusterQueue{
						utiltestingapi.MakeClusterQueue("w1-cq1").
							ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource("cpu", "25").Obj()).
							Obj(),
					},
				},
			},
			wantCP: utiltestingalpha.MakeCapacityProvider("cp1").
				ControllerName(kueue.MultiKueueControllerName).
				OrchestratedFlavors("default").
				Parameters(kueuealpha.SchemeGroupVersion.Group, testProviderConfigKind, "cp-config1").
				Generation(1).
				Capacity(utiltestingalpha.MakeNormalizedCapacity().
					Flavors(utiltestingalpha.MakeNormalizedCapacityFlavor("default").Resource("cpu", "25").Obj()).
					Obj(),
				).
				Condition(synchronizedCondition("Capacity synchronized from MultiKueue worker clusters", 1)).
				Obj(),
			wantStatusPatches: 1,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			lqs := tc.lqs
			workers := tc.workers
			if tc.workerResourceCount > 0 {
				workers = maps.Clone(tc.workers)
				worker := workers["worker1"]
				// Each resource list may contain at most 64 entries.
				for start := 0; start < tc.workerResourceCount; start += 64 {
					flavor := utiltestingapi.MakeFlavorQuotas("default")
					for i := start; i < min(start+64, tc.workerResourceCount); i++ {
						flavor.Resource(corev1.ResourceName(fmt.Sprintf("example.com/r%050d", i)), "1")
					}
					cqName := fmt.Sprintf("w1-cq%d", start/64)
					lqName := fmt.Sprintf("lq%d", start/64)
					worker.cqs = append(worker.cqs, utiltestingapi.MakeClusterQueue(cqName).ResourceGroup(*flavor.Obj()).Obj())
					worker.lqs = append(worker.lqs, utiltestingapi.MakeLocalQueue(lqName, TestNamespace).ClusterQueue(cqName).Obj())
					lqs = append(lqs, utiltestingapi.MakeLocalQueue(lqName, TestNamespace).ClusterQueue(tc.cqs[0].Name).Obj())
				}
				workers["worker1"] = worker
			}

			ctx, _ := utiltesting.ContextWithLog(t)

			gotStatusPatches := 0
			builder := utiltesting.NewClientBuilder().
				WithObjects(asObjs(tc.providerConfigs)...).
				WithObjects(asObjs(tc.cqs)...).
				WithObjects(asObjs(lqs)...).
				WithObjects(asObjs(tc.acs)...).
				WithObjects(asObjs(tc.mkConfigs)...).
				WithStatusSubresource(&kueuealpha.CapacityProvider{}).
				WithInterceptorFuncs(interceptor.Funcs{
					SubResourcePatch: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
						if _, isCP := obj.(*kueuealpha.CapacityProvider); isCP {
							gotStatusPatches++
							if tc.deleteBeforeStatusPatch {
								if err := c.Delete(ctx, obj); err != nil {
									return err
								}
							}
						}
						return c.SubResource(subResourceName).Patch(ctx, obj, patch, opts...)
					},
				})
			if tc.cp != nil {
				builder = builder.WithObjects(tc.cp)
			}
			for worker, wState := range workers {
				if !wState.clusterDeleted {
					builder = builder.WithObjects(utiltestingapi.MakeMultiKueueCluster(worker).Obj())
				}
			}
			if err := SetupIndexer(ctx, utiltesting.AsIndexer(builder), TestNamespace); err != nil {
				t.Fatalf("Failed to set up indexes: %v", err)
			}
			c := builder.Build()

			adapters, err := jobs.NewIntegrationManager().GetMultiKueueAdapters(sets.New("batch/job"))
			if err != nil {
				t.Fatalf("Failed to get MultiKueue adapters: %v", err)
			}
			cRec := newClustersReconciler(c, TestNamespace, withAdapters(adapters), withEventRecorder(&utiltesting.EventRecorder{}))
			cRec.rootContext = ctx
			for worker, wState := range workers {
				workerBuilder := utiltesting.NewClientBuilder().
					WithObjects(asObjs(wState.cqs)...).
					WithObjects(asObjs(wState.lqs)...)
				if wState.listErr != nil {
					workerBuilder = workerBuilder.WithInterceptorFuncs(interceptor.Funcs{
						List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
							return wState.listErr
						},
					})
				}
				rc := newRemoteClient(c, nil, nil, nil, defaultOrigin, worker, adapters)
				rc.client = NewNeverCachingClient(workerBuilder.Build())
				if !wState.disconnected {
					rc.connState.markConnected()
				}
				cRec.remoteClients[worker] = rc
			}

			helper, err := admissioncheck.NewMultiKueueStoreHelper(c)
			if err != nil {
				t.Fatalf("Failed to create the MultiKueue store helper: %v", err)
			}
			reconciler := newCPReconciler(c, helper, cRec, nil, 100*time.Millisecond)

			_, err = reconciler.Reconcile(ctx, reconcile.Request{Name: "cp1"})
			if diff := cmp.Diff(tc.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Fatalf("Unexpected Reconcile() error (-want/+got):\n%s", diff)
			}

			gotCP := &kueuealpha.CapacityProvider{}
			if err := c.Get(ctx, types.NamespacedName{Name: "cp1"}, gotCP); err != nil {
				if tc.wantCP != nil || !apierrors.IsNotFound(err) {
					t.Fatalf("Failed to get the CapacityProvider: %v", err)
				}
				gotCP = nil
			}
			ignoredCondFields := []string{"LastTransitionTime"}
			if tc.wantMessageTruncated {
				ignoredCondFields = append(ignoredCondFields, "Message")
				gotCond := apimeta.FindStatusCondition(gotCP.Status.Conditions, kueuealpha.CapacityProviderCapacitySynchronized)
				if gotCond == nil || len(gotCond.Message) != 32*1024 {
					var gotLen int
					if gotCond != nil {
						gotLen = len(gotCond.Message)
					}
					t.Errorf("Expected stored condition message to be 32 KiB, got %d bytes", gotLen)
				}
			}
			if diff := cmp.Diff(tc.wantCP, gotCP,
				cmpopts.IgnoreTypes(metav1.TypeMeta{}),
				cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion"),
				cmpopts.IgnoreFields(metav1.Condition{}, ignoredCondFields...),
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
				wantItems = []reconcile.Request{{Name: "cp1"}}
			}
			if diff := cmp.Diff(wantItems, mockQ.Items, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Unexpected workqueue items (-want/+got):\n%s", diff)
			}
		})
	}
}
