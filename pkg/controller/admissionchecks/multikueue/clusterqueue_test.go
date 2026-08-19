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
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/prometheus/client_golang/prometheus/testutil"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobs"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

type workerState struct {
	lqs      []*kueue.LocalQueue
	cqs      []*kueue.ClusterQueue
	inactive bool
}

func TestCQReconcile(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.MultiKueueManagerQuotaAutomation, true)

	cases := map[string]struct {
		cq      *kueue.ClusterQueue
		lqs     []*kueue.LocalQueue
		acs     []*kueue.AdmissionCheck
		configs []*kueue.MultiKueueConfig
		workers map[string]workerState

		wantQuotaAutomated bool
		wantNominalQuotas  map[string]string // Ignored if wantQuotaAutomated == false
		wantCondition      *metav1.Condition
	}{
		"multiple resources for single LQs and CQs": {
			cq: utiltestingapi.MakeClusterQueue("cq1").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource("cpu", "0").Resource("memory", "0").Obj()).
				AdmissionChecks("ac1").
				Obj(),
			lqs: []*kueue.LocalQueue{
				utiltestingapi.MakeLocalQueue("lq1", TestNamespace).ClusterQueue("cq1").Obj(),
			},
			acs: []*kueue.AdmissionCheck{
				utiltestingapi.MakeAdmissionCheck("ac1").
					ControllerName(kueue.MultiKueueControllerName).
					Parameters(kueue.SchemeGroupVersion.Group, "MultiKueueConfig", "config1").
					Obj(),
			},
			configs: []*kueue.MultiKueueConfig{
				utiltestingapi.MakeMultiKueueConfig("config1").Clusters("worker1", "worker2").QuotaManagement(kueue.QuotaManagementAutomated).Obj(),
			},
			workers: map[string]workerState{
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
							ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource("cpu", "5").Resource("memory", "10Gi").Obj()).
							Obj(),
					},
				},
			},
			wantQuotaAutomated: true,
			wantNominalQuotas: map[string]string{
				"cpu":    "15",
				"memory": "30Gi",
			},
			wantCondition: &metav1.Condition{
				Type:    kueue.MultiKueueManagerQuotaAutomation,
				Status:  metav1.ConditionTrue,
				Reason:  "QuotaAutomated",
				Message: "ClusterQueue quota is automatically managed based on MultiKueue workers.",
			},
		},
		"unrelated CQs and inactive workers are ignored": {
			cq: utiltestingapi.MakeClusterQueue("cq1").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource("cpu", "0").Obj()).
				AdmissionChecks("ac1").
				Obj(),
			lqs: []*kueue.LocalQueue{
				utiltestingapi.MakeLocalQueue("lq1", TestNamespace).ClusterQueue("cq1").Obj(),
			},
			acs: []*kueue.AdmissionCheck{
				utiltestingapi.MakeAdmissionCheck("ac1").
					ControllerName(kueue.MultiKueueControllerName).
					Parameters(kueue.SchemeGroupVersion.Group, "MultiKueueConfig", "config1").
					Obj(),
			},
			configs: []*kueue.MultiKueueConfig{
				utiltestingapi.MakeMultiKueueConfig("config1").Clusters("worker1", "worker2").QuotaManagement(kueue.QuotaManagementAutomated).Obj(),
			},
			workers: map[string]workerState{
				"worker1": {
					lqs: []*kueue.LocalQueue{utiltestingapi.MakeLocalQueue("lq1", TestNamespace).ClusterQueue("w1-cq1").Obj()},
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
						utiltestingapi.MakeClusterQueue("w2-cq1").
							ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource("cpu", "500").Obj()).
							Obj(),
					},
					inactive: true,
				},
			},
			wantQuotaAutomated: true,
			wantNominalQuotas: map[string]string{
				"cpu": "10",
			},
			wantCondition: &metav1.Condition{
				Type:    kueue.MultiKueueManagerQuotaAutomation,
				Status:  metav1.ConditionTrue,
				Reason:  "QuotaAutomated",
				Message: "ClusterQueue quota is automatically managed based on MultiKueue workers.",
			},
		},
		"multiple LQs pointing to same and different CQs on workers": {
			cq: utiltestingapi.MakeClusterQueue("cq1").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource("cpu", "0").Obj()).
				AdmissionChecks("ac1").
				Obj(),
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
			configs: []*kueue.MultiKueueConfig{
				utiltestingapi.MakeMultiKueueConfig("config1").Clusters("worker1", "worker2").QuotaManagement(kueue.QuotaManagementAutomated).Obj(),
			},
			workers: map[string]workerState{
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
			wantQuotaAutomated: true,
			wantNominalQuotas: map[string]string{
				"cpu": "23", // 10 + 5 + 8; 10 should be counted only once
			},
			wantCondition: &metav1.Condition{
				Type:    kueue.MultiKueueManagerQuotaAutomation,
				Status:  metav1.ConditionTrue,
				Reason:  "QuotaAutomated",
				Message: "ClusterQueue quota is automatically managed based on MultiKueue workers.",
			},
		},
		"multiple flavors and incompatible ResourceGroups across workers": {
			cq: utiltestingapi.MakeClusterQueue("cq1").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource("cpu", "0").Resource("gpu", "0").Obj()).
				AdmissionChecks("ac1").
				Obj(),
			lqs: []*kueue.LocalQueue{
				utiltestingapi.MakeLocalQueue("lq1", TestNamespace).ClusterQueue("cq1").Obj(),
			},
			acs: []*kueue.AdmissionCheck{
				utiltestingapi.MakeAdmissionCheck("ac1").
					ControllerName(kueue.MultiKueueControllerName).
					Parameters(kueue.SchemeGroupVersion.Group, "MultiKueueConfig", "config1").
					Obj(),
			},
			configs: []*kueue.MultiKueueConfig{
				utiltestingapi.MakeMultiKueueConfig("config1").Clusters("worker1", "worker2").QuotaManagement(kueue.QuotaManagementAutomated).Obj(),
			},
			workers: map[string]workerState{
				"worker1": {
					lqs: []*kueue.LocalQueue{utiltestingapi.MakeLocalQueue("lq1", TestNamespace).ClusterQueue("w1-cq1").Obj()},
					cqs: []*kueue.ClusterQueue{
						utiltestingapi.MakeClusterQueue("w1-cq1").
							ResourceGroup(
								*utiltestingapi.MakeFlavorQuotas("default").Resource("cpu", "10").Obj(),
							).
							ResourceGroup(
								*utiltestingapi.MakeFlavorQuotas("gpu").Resource("gpu", "1").Obj(),
							).
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
			wantQuotaAutomated: true,
			wantNominalQuotas: map[string]string{
				"cpu": "19",
				"gpu": "4",
			},
			wantCondition: &metav1.Condition{
				Type:    kueue.MultiKueueManagerQuotaAutomation,
				Status:  metav1.ConditionTrue,
				Reason:  "QuotaAutomated",
				Message: "ClusterQueue quota is automatically managed based on MultiKueue workers.",
			},
		},
		"workers with a subset of manager resources": {
			cq: utiltestingapi.MakeClusterQueue("cq1").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource("cpu", "0").Resource("memory", "0").Resource("gpu", "0").Obj()).
				AdmissionChecks("ac1").
				Obj(),
			lqs: []*kueue.LocalQueue{
				utiltestingapi.MakeLocalQueue("lq1", TestNamespace).ClusterQueue("cq1").Obj(),
			},
			acs: []*kueue.AdmissionCheck{
				utiltestingapi.MakeAdmissionCheck("ac1").
					ControllerName(kueue.MultiKueueControllerName).
					Parameters(kueue.SchemeGroupVersion.Group, "MultiKueueConfig", "config1").
					Obj(),
			},
			configs: []*kueue.MultiKueueConfig{
				utiltestingapi.MakeMultiKueueConfig("config1").Clusters("worker1", "worker2").QuotaManagement(kueue.QuotaManagementAutomated).Obj(),
			},
			workers: map[string]workerState{
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
			wantQuotaAutomated: true,
			wantNominalQuotas: map[string]string{
				"cpu":    "15",
				"memory": "20Gi",
				"gpu":    "0",
			},
			wantCondition: &metav1.Condition{
				Type:    kueue.MultiKueueManagerQuotaAutomation,
				Status:  metav1.ConditionTrue,
				Reason:  "QuotaAutomated",
				Message: "ClusterQueue quota is automatically managed based on MultiKueue workers.",
			},
		},
		"quota automation not requested": {
			cq: utiltestingapi.MakeClusterQueue("cq1").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource("cpu", "100").Obj()).
				AdmissionChecks("ac1").
				Obj(),
			lqs: []*kueue.LocalQueue{
				utiltestingapi.MakeLocalQueue("lq1", TestNamespace).ClusterQueue("cq1").Obj(),
			},
			acs: []*kueue.AdmissionCheck{
				utiltestingapi.MakeAdmissionCheck("ac1").
					ControllerName(kueue.MultiKueueControllerName).
					Parameters(kueue.SchemeGroupVersion.Group, "MultiKueueConfig", "config1").
					Obj(),
			},
			configs: []*kueue.MultiKueueConfig{
				utiltestingapi.MakeMultiKueueConfig("config1").Clusters("worker1").QuotaManagement(kueue.QuotaManagementManual).Obj(),
			},
			workers: map[string]workerState{
				"worker1": {
					lqs: []*kueue.LocalQueue{utiltestingapi.MakeLocalQueue("lq1", TestNamespace).ClusterQueue("w1-cq1").Obj()},
					cqs: []*kueue.ClusterQueue{
						utiltestingapi.MakeClusterQueue("w1-cq1").
							ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource("cpu", "10").Resource("memory", "20Gi").Obj()).
							Obj(),
					},
				},
			},
			wantQuotaAutomated: false,
			wantCondition: &metav1.Condition{
				Type:    kueue.MultiKueueManagerQuotaAutomation,
				Status:  metav1.ConditionFalse,
				Reason:  "NotRequested",
				Message: "MultiKueue manager quota automation has not been requested.",
			},
		},
		"quota automation unsupported for multiple manager flavors": {
			cq: utiltestingapi.MakeClusterQueue("cq1").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource("cpu", "0").Obj()).
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("gpu").Resource("gpu", "0").Obj()).
				AdmissionChecks("ac1").
				Obj(),
			acs: []*kueue.AdmissionCheck{
				utiltestingapi.MakeAdmissionCheck("ac1").
					ControllerName(kueue.MultiKueueControllerName).
					Parameters(kueue.SchemeGroupVersion.Group, "MultiKueueConfig", "config1").
					Obj(),
			},
			configs: []*kueue.MultiKueueConfig{
				utiltestingapi.MakeMultiKueueConfig("config1").Clusters("worker1").QuotaManagement(kueue.QuotaManagementAutomated).Obj(),
			},
			wantQuotaAutomated: false,
			wantCondition: &metav1.Condition{
				Type:    kueue.MultiKueueManagerQuotaAutomation,
				Status:  metav1.ConditionFalse,
				Reason:  "UnsupportedConfiguration",
				Message: "Quota automation requires that the manager-side ClusterQueue has exactly one ResourceFlavor",
			},
		},
		"quota automation unsupported for missing manager covered resources": {
			cq: utiltestingapi.MakeClusterQueue("cq1").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource("cpu", "0").Obj()).
				AdmissionChecks("ac1").
				Obj(),
			lqs: []*kueue.LocalQueue{
				utiltestingapi.MakeLocalQueue("lq1", TestNamespace).ClusterQueue("cq1").Obj(),
			},
			acs: []*kueue.AdmissionCheck{
				utiltestingapi.MakeAdmissionCheck("ac1").
					ControllerName(kueue.MultiKueueControllerName).
					Parameters(kueue.SchemeGroupVersion.Group, "MultiKueueConfig", "config1").
					Obj(),
			},
			configs: []*kueue.MultiKueueConfig{
				utiltestingapi.MakeMultiKueueConfig("config1").Clusters("worker1").QuotaManagement(kueue.QuotaManagementAutomated).Obj(),
			},
			workers: map[string]workerState{
				"worker1": {
					lqs: []*kueue.LocalQueue{utiltestingapi.MakeLocalQueue("lq1", TestNamespace).ClusterQueue("w1-cq1").Obj()},
					cqs: []*kueue.ClusterQueue{
						utiltestingapi.MakeClusterQueue("w1-cq1").
							ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource("memory", "10Gi").Resource("gpu", "2").Obj()).
							Obj(),
					},
				},
			},
			wantQuotaAutomated: false,
			wantCondition: &metav1.Condition{
				Type:    kueue.MultiKueueManagerQuotaAutomation,
				Status:  metav1.ConditionFalse,
				Reason:  "UnsupportedConfiguration",
				Message: "manager-side coveredResources is missing resources configured on workers: [gpu memory]",
			},
		},
		"not a MultiKueue manager ClusterQueue": {
			cq: utiltestingapi.MakeClusterQueue("cq1").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource("cpu", "100").Obj()).
				Condition(kueue.MultiKueueManagerQuotaAutomation, metav1.ConditionTrue, "QuotaAutomated", "ClusterQueue quota is automatically managed based on MultiKueue workers.").
				Obj(),
			lqs: []*kueue.LocalQueue{
				utiltestingapi.MakeLocalQueue("lq1", TestNamespace).ClusterQueue("cq1").Obj(),
			},
			wantQuotaAutomated: false,
		},
		"referenced MultiKueueConfig not found": {
			cq: utiltestingapi.MakeClusterQueue("cq1").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource("cpu", "100").Obj()).
				AdmissionChecks("ac1").
				Obj(),
			acs: []*kueue.AdmissionCheck{
				utiltestingapi.MakeAdmissionCheck("ac1").
					ControllerName(kueue.MultiKueueControllerName).
					Parameters(kueue.SchemeGroupVersion.Group, "MultiKueueConfig", "config-not-found").
					Obj(),
			},
			wantQuotaAutomated: false,
			wantCondition: &metav1.Condition{
				Type:    kueue.MultiKueueManagerQuotaAutomation,
				Status:  metav1.ConditionFalse,
				Reason:  "UnsupportedConfiguration",
				Message: "The referenced MultiKueueConfig was not found.",
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			c := utiltesting.NewClientBuilder().
				WithObjects(tc.cq).
				WithObjects(asObjs(tc.lqs)...).
				WithObjects(asObjs(tc.acs)...).
				WithObjects(asObjs(tc.configs)...).
				WithIndex(&kueue.AdmissionCheck{}, AdmissionCheckControllerNameKey, admissionCheckControllerNameIndexerFunc).
				WithIndex(&kueue.AdmissionCheck{}, AdmissionCheckUsingConfigKey, admissioncheck.IndexerByConfigFunction(kueue.MultiKueueControllerName, configGVK)).
				WithIndex(&kueue.ClusterQueue{}, ClusterQueueAdmissionChecksKey, clusterQueueAdmissionChecksIndexerFunc).
				WithStatusSubresource(tc.cq).
				Build()

			adapters, _ := jobs.NewIntegrationManager().GetMultiKueueAdapters(sets.New("batch/job"))
			recorder := &utiltesting.EventRecorder{}
			cRec := newClustersReconciler(c, TestNamespace, 0, defaultOrigin, nil, adapters, nil, nil, recorder)
			cRec.rootContext = ctx
			for worker, wState := range tc.workers {
				workerClient := NewNeverCachingClient(utiltesting.NewClientBuilder().
					WithObjects(asObjs(wState.cqs)...).
					WithObjects(asObjs(wState.lqs)...).
					Build())
				rc := newRemoteClient(c, nil, nil, nil, defaultOrigin, worker, adapters)
				rc.client = workerClient
				// newRemoteClient starts disconnected; mark the active workers connected.
				if !wState.inactive {
					rc.connState.markConnected()
				}
				cRec.remoteClients[worker] = rc
			}

			helper, _ := admissioncheck.NewMultiKueueStoreHelper(c)
			reconciler := newCQReconciler(c, helper, cRec, nil, 100*time.Millisecond)

			_, gotErr := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: tc.cq.Name}})
			if gotErr != nil {
				t.Errorf("unexpected reconcile error: %v", gotErr)
			}

			gotCQ := &kueue.ClusterQueue{}
			if err := c.Get(ctx, types.NamespacedName{Name: tc.cq.Name}, gotCQ); err != nil {
				t.Fatalf("failed to fetch local ClusterQueue: %v", err)
			}

			// Verify quotas
			if !tc.wantQuotaAutomated {
				if diff := cmp.Diff(tc.cq.Spec.ResourceGroups, gotCQ.Spec.ResourceGroups); diff != "" {
					t.Errorf("unexpected resourceGroups changes for quota automation disabled (-want/+got):\n%s", diff)
				}
			} else {
				if len(gotCQ.Spec.ResourceGroups) != 1 {
					t.Fatalf("expected exactly 1 resource group, got: %d", len(gotCQ.Spec.ResourceGroups))
				}
				if len(gotCQ.Spec.ResourceGroups[0].Flavors) != 1 {
					t.Fatalf("expected exactly 1 resource flavor, got: %d", len(gotCQ.Spec.ResourceGroups[0].Flavors))
				}
				quotas := gotCQ.Spec.ResourceGroups[0].Flavors[0].Resources

				if len(quotas) != len(tc.wantNominalQuotas) {
					t.Errorf("expected exactly %d nominal quotas, got: %d", len(tc.wantNominalQuotas), len(quotas))
				}

				for _, resQuota := range quotas {
					expectedVal, exists := tc.wantNominalQuotas[string(resQuota.Name)]
					if !exists {
						t.Errorf("unexpected resource quota: %s", resQuota.Name)
						continue
					}
					expectedQty := resource.MustParse(expectedVal)
					if !resQuota.NominalQuota.Equal(expectedQty) {
						t.Errorf("expected resource %q nominalQuota: %s, got: %s", resQuota.Name, expectedVal, resQuota.NominalQuota.String())
					}
				}
			}

			// Verify condition state
			gotCond := apimeta.FindStatusCondition(gotCQ.Status.Conditions, kueue.MultiKueueManagerQuotaAutomation)
			if diff := cmp.Diff(tc.wantCondition, gotCond, cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime")); diff != "" {
				t.Errorf("Unexpected status condition (-want/+got):\n%s", diff)
			}
		})
	}
}

func asObjs[T client.Object](objs []T) []client.Object {
	res := make([]client.Object, len(objs))
	for i, obj := range objs {
		res[i] = obj
	}
	return res
}

func TestCQReconciler_EventHandlers(t *testing.T) {
	cq := utiltestingapi.MakeClusterQueue("cq1").AdmissionChecks("ac1").Obj()
	ac := utiltestingapi.MakeAdmissionCheck("ac1").
		ControllerName(kueue.MultiKueueControllerName).
		Parameters(kueue.SchemeGroupVersion.Group, "MultiKueueConfig", "config1").
		Obj()
	cfg := utiltestingapi.MakeMultiKueueConfig("config1").Clusters("cluster1").Obj()
	cluster := utiltestingapi.MakeMultiKueueCluster("cluster1").Obj()
	lq := utiltestingapi.MakeLocalQueue("lq1", TestNamespace).ClusterQueue("cq1").Obj()

	c := utiltesting.NewClientBuilder().
		WithObjects(cq, ac, cfg, cluster, lq).
		WithIndex(&kueue.AdmissionCheck{}, AdmissionCheckControllerNameKey, admissionCheckControllerNameIndexerFunc).
		WithIndex(&kueue.AdmissionCheck{}, AdmissionCheckUsingConfigKey, admissioncheck.IndexerByConfigFunction(kueue.MultiKueueControllerName, configGVK)).
		WithIndex(&kueue.ClusterQueue{}, ClusterQueueAdmissionChecksKey, clusterQueueAdmissionChecksIndexerFunc).
		WithIndex(&kueue.MultiKueueConfig{}, UsingMultiKueueClusters, multiKueueClustersIndexerFunc).
		Build()

	r := &cqReconciler{client: c}

	ctx, _ := utiltesting.ContextWithLog(t)

	type mockQueue = utiltesting.MockTypedRateLimitingInterface

	cases := map[string]struct {
		handler       func(mockQ *mockQueue)
		wantReconcile bool
	}{
		"LocalQueue Create": {
			handler: func(mockQ *mockQueue) {
				(&lqHandler{reconciler: r}).Create(ctx, event.CreateEvent{Object: lq}, mockQ)
			},
			wantReconcile: true,
		},
		"LocalQueue Update (irrelevant)": {
			handler: func(mockQ *mockQueue) {
				(&lqHandler{reconciler: r}).Update(ctx, event.UpdateEvent{ObjectOld: lq, ObjectNew: lq}, mockQ)
			},
			wantReconcile: false,
		},
		"LocalQueue Delete": {
			handler: func(mockQ *mockQueue) {
				(&lqHandler{reconciler: r}).Delete(ctx, event.DeleteEvent{Object: lq}, mockQ)
			},
			wantReconcile: true,
		},
		"AdmissionCheck Create": {
			handler: func(mockQ *mockQueue) {
				(&acHandler{reconciler: r}).Create(ctx, event.CreateEvent{Object: ac}, mockQ)
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
				(&acHandler{reconciler: r}).Update(ctx, event.UpdateEvent{ObjectOld: ac, ObjectNew: newAC}, mockQ)
			},
			wantReconcile: true,
		},
		"AdmissionCheck Update (irrelevant)": {
			handler: func(mockQ *mockQueue) {
				(&acHandler{reconciler: r}).Update(ctx, event.UpdateEvent{ObjectOld: ac, ObjectNew: ac}, mockQ)
			},
			wantReconcile: false,
		},
		"AdmissionCheck Delete": {
			handler: func(mockQ *mockQueue) {
				(&acHandler{reconciler: r}).Delete(ctx, event.DeleteEvent{Object: ac}, mockQ)
			},
			wantReconcile: true,
		},
		"MultiKueueConfig Create": {
			handler: func(mockQ *mockQueue) {
				(&cqConfigHandler{reconciler: r}).Create(ctx, event.CreateEvent{Object: cfg}, mockQ)
			},
			wantReconcile: true,
		},
		"MultiKueueConfig Update (relevant)": {
			handler: func(mockQ *mockQueue) {
				newCfg := utiltestingapi.MakeMultiKueueConfig("config1").Clusters("cluster1", "cluster2").Obj()
				newCfg.Generation = 1
				(&cqConfigHandler{reconciler: r}).Update(ctx, event.UpdateEvent{ObjectOld: cfg, ObjectNew: newCfg}, mockQ)
			},
			wantReconcile: true,
		},
		"MultiKueueConfig Update (irrelevant)": {
			handler: func(mockQ *mockQueue) {
				(&cqConfigHandler{reconciler: r}).Update(ctx, event.UpdateEvent{ObjectOld: cfg, ObjectNew: cfg}, mockQ)
			},
			wantReconcile: false,
		},
		"MultiKueueConfig Delete": {
			handler: func(mockQ *mockQueue) {
				(&cqConfigHandler{reconciler: r}).Delete(ctx, event.DeleteEvent{Object: cfg}, mockQ)
			},
			wantReconcile: true,
		},
		"MultiKueueCluster Create": {
			handler: func(mockQ *mockQueue) {
				(&cqClusterHandler{reconciler: r}).Create(ctx, event.CreateEvent{Object: cluster}, mockQ)
			},
			wantReconcile: true,
		},
		"MultiKueueCluster Update (relevant)": {
			handler: func(mockQ *mockQueue) {
				newCluster := utiltestingapi.MakeMultiKueueCluster("cluster1").
					Active(metav1.ConditionTrue, "ByTest", "by test", 1).Obj()
				(&cqClusterHandler{reconciler: r}).Update(ctx, event.UpdateEvent{ObjectOld: cluster, ObjectNew: newCluster}, mockQ)
			},
			wantReconcile: true,
		},
		"MultiKueueCluster Update (irrelevant)": {
			handler: func(mockQ *mockQueue) {
				(&cqClusterHandler{reconciler: r}).Update(ctx, event.UpdateEvent{ObjectOld: cluster, ObjectNew: cluster}, mockQ)
			},
			wantReconcile: false,
		},
		"MultiKueueCluster Delete": {
			handler: func(mockQ *mockQueue) {
				(&cqClusterHandler{reconciler: r}).Delete(ctx, event.DeleteEvent{Object: cluster}, mockQ)
			},
			wantReconcile: true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			mockQ := &mockQueue{}
			tc.handler(mockQ)

			if tc.wantReconcile {
				if len(mockQ.Items) != 1 {
					t.Fatalf("expected exactly 1 item in workqueue, got %d", len(mockQ.Items))
				}
				if mockQ.Items[0].Name != "cq1" {
					t.Errorf("expected workqueue item name to be %q, got %q", "cq1", mockQ.Items[0].Name)
				}
			} else if len(mockQ.Items) != 0 {
				t.Fatalf("expected 0 items in workqueue, got %d", len(mockQ.Items))
			}
		})
	}
}

func expectCQClusterStatusMetric(t *testing.T, cqName, cluster string, want metav1.ConditionStatus) {
	t.Helper()
	for _, status := range metrics.ConditionStatusValues {
		wantV := 0.0
		if status == want {
			wantV = 1.0
		}
		got := testutil.ToFloat64(metrics.MultiKueueClusterByStatus.WithLabelValues(cqName, cluster, string(status), roletracker.RoleStandalone))
		if got != wantV {
			t.Errorf("cluster_status{cluster_queue=%q, cluster=%q, active=%s}: want %v, got %v", cqName, cluster, status, wantV, got)
		}
	}
}

func TestCQReconcilerReportsClusterStatusMetric(t *testing.T) {
	metrics.MultiKueueClusterByStatus.Reset()
	t.Cleanup(metrics.MultiKueueClusterByStatus.Reset)

	ctx, _ := utiltesting.ContextWithLog(t)

	cq := utiltestingapi.MakeClusterQueue("cq1").
		ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource("cpu", "0").Obj()).
		AdmissionChecks("ac1").
		Obj()
	ac := utiltestingapi.MakeAdmissionCheck("ac1").
		ControllerName(kueue.MultiKueueControllerName).
		Parameters(kueue.SchemeGroupVersion.Group, "MultiKueueConfig", "config1").
		Obj()
	cfg := utiltestingapi.MakeMultiKueueConfig("config1").Clusters("worker1", "worker2").Obj()
	// worker1 is connected, worker2 is not.
	worker1 := utiltestingapi.MakeMultiKueueCluster("worker1").Active(metav1.ConditionTrue, "Active", "Connected", 1).Obj()
	worker2 := utiltestingapi.MakeMultiKueueCluster("worker2").Active(metav1.ConditionFalse, "ClientConnectionFailed", "connection lost", 1).Obj()

	c := utiltesting.NewClientBuilder().
		WithObjects(cq, ac, cfg, worker1, worker2).
		WithIndex(&kueue.AdmissionCheck{}, AdmissionCheckControllerNameKey, admissionCheckControllerNameIndexerFunc).
		WithIndex(&kueue.AdmissionCheck{}, AdmissionCheckUsingConfigKey, admissioncheck.IndexerByConfigFunction(kueue.MultiKueueControllerName, configGVK)).
		WithIndex(&kueue.ClusterQueue{}, ClusterQueueAdmissionChecksKey, clusterQueueAdmissionChecksIndexerFunc).
		WithStatusSubresource(cq).
		Build()
	helper, _ := admissioncheck.NewMultiKueueStoreHelper(c)
	adapters, _ := jobs.NewIntegrationManager().GetMultiKueueAdapters(sets.New("batch/job"))
	cRec := newClustersReconciler(c, TestNamespace, 0, defaultOrigin, nil, adapters, nil, nil, &utiltesting.EventRecorder{})
	reconciler := newCQReconciler(c, helper, cRec, nil, 100*time.Millisecond)

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "cq1"}}
	if _, err := reconciler.Reconcile(ctx, req); err != nil {
		t.Fatalf("unexpected reconcile error: %v", err)
	}

	// Each worker is reported under the ClusterQueue that references it.
	expectCQClusterStatusMetric(t, "cq1", "worker1", metav1.ConditionTrue)
	expectCQClusterStatusMetric(t, "cq1", "worker2", metav1.ConditionFalse)

	// Dropping worker2 from the config must stop reporting it, otherwise a cluster
	// a ClusterQueue no longer uses would linger as if it were still relevant.
	updatedCfg := &kueue.MultiKueueConfig{}
	if err := c.Get(ctx, types.NamespacedName{Name: "config1"}, updatedCfg); err != nil {
		t.Fatalf("unexpected error reading the config: %v", err)
	}
	updatedCfg.Spec.Clusters = []string{"worker1"}
	if err := c.Update(ctx, updatedCfg); err != nil {
		t.Fatalf("unexpected error updating the config: %v", err)
	}
	if _, err := reconciler.Reconcile(ctx, req); err != nil {
		t.Fatalf("unexpected reconcile error after config change: %v", err)
	}
	if got := testutil.CollectAndCount(metrics.MultiKueueClusterByStatus); got != len(metrics.ConditionStatusValues) {
		t.Errorf("expected only worker1 series to remain, got %d series", got)
	}
	expectCQClusterStatusMetric(t, "cq1", "worker1", metav1.ConditionTrue)

	// A cluster whose Active condition has not been written yet is the one case that is
	// genuinely unknown, and the only one reported as Unknown.
	worker3 := utiltestingapi.MakeMultiKueueCluster("worker3").Obj()
	if err := c.Create(ctx, worker3); err != nil {
		t.Fatalf("unexpected error creating the worker cluster: %v", err)
	}
	updatedCfg.Spec.Clusters = []string{"worker1", "worker3"}
	if err := c.Update(ctx, updatedCfg); err != nil {
		t.Fatalf("unexpected error updating the config: %v", err)
	}
	if _, err := reconciler.Reconcile(ctx, req); err != nil {
		t.Fatalf("unexpected reconcile error after adding a cluster: %v", err)
	}
	expectCQClusterStatusMetric(t, "cq1", "worker1", metav1.ConditionTrue)
	expectCQClusterStatusMetric(t, "cq1", "worker3", metav1.ConditionUnknown)

	// A cluster the ClusterQueue still references but that has no MultiKueueCluster
	// object has no Active condition to mirror, so it is not reported at all. The
	// AdmissionCheck surfaces it as a missing cluster instead.
	if err := c.Delete(ctx, worker3); err != nil {
		t.Fatalf("unexpected error deleting the worker cluster: %v", err)
	}
	if _, err := reconciler.Reconcile(ctx, req); err != nil {
		t.Fatalf("unexpected reconcile error after cluster deletion: %v", err)
	}
	if got := testutil.CollectAndCount(metrics.MultiKueueClusterByStatus); got != len(metrics.ConditionStatusValues) {
		t.Errorf("expected the missing cluster not to be reported, got %d series", got)
	}
	expectCQClusterStatusMetric(t, "cq1", "worker1", metav1.ConditionTrue)

	// Deleting the ClusterQueue drops its series entirely.
	if err := c.Delete(ctx, cq); err != nil {
		t.Fatalf("unexpected error deleting the ClusterQueue: %v", err)
	}
	if _, err := reconciler.Reconcile(ctx, req); err != nil {
		t.Fatalf("unexpected reconcile error after deletion: %v", err)
	}
	if got := testutil.CollectAndCount(metrics.MultiKueueClusterByStatus); got != 0 {
		t.Errorf("expected all series to be cleared, got %d", got)
	}
}

func TestCQReconcilerKeepsClusterStatusMetricOnReadError(t *testing.T) {
	readErr := errors.New("simulated read failure")

	cases := map[string]struct {
		failRead func(obj client.Object) bool
	}{
		"reading the MultiKueueConfig fails": {
			failRead: func(obj client.Object) bool {
				_, isConfig := obj.(*kueue.MultiKueueConfig)
				return isConfig
			},
		},
		"reading the MultiKueueCluster fails": {
			failRead: func(obj client.Object) bool {
				_, isCluster := obj.(*kueue.MultiKueueCluster)
				return isCluster
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			metrics.MultiKueueClusterByStatus.Reset()
			t.Cleanup(metrics.MultiKueueClusterByStatus.Reset)

			ctx, _ := utiltesting.ContextWithLog(t)

			cq := utiltestingapi.MakeClusterQueue("cq1").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource("cpu", "0").Obj()).
				AdmissionChecks("ac1").
				Obj()
			ac := utiltestingapi.MakeAdmissionCheck("ac1").
				ControllerName(kueue.MultiKueueControllerName).
				Parameters(kueue.SchemeGroupVersion.Group, "MultiKueueConfig", "config1").
				Obj()
			cfg := utiltestingapi.MakeMultiKueueConfig("config1").Clusters("worker1").Obj()
			worker1 := utiltestingapi.MakeMultiKueueCluster("worker1").Active(metav1.ConditionTrue, "Active", "Connected", 1).Obj()

			readsFail := false
			c := utiltesting.NewClientBuilder().
				WithObjects(cq, ac, cfg, worker1).
				WithIndex(&kueue.AdmissionCheck{}, AdmissionCheckControllerNameKey, admissionCheckControllerNameIndexerFunc).
				WithIndex(&kueue.AdmissionCheck{}, AdmissionCheckUsingConfigKey, admissioncheck.IndexerByConfigFunction(kueue.MultiKueueControllerName, configGVK)).
				WithIndex(&kueue.ClusterQueue{}, ClusterQueueAdmissionChecksKey, clusterQueueAdmissionChecksIndexerFunc).
				WithStatusSubresource(cq).
				WithInterceptorFuncs(interceptor.Funcs{
					Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
						if readsFail && tc.failRead(obj) {
							return readErr
						}
						return cl.Get(ctx, key, obj, opts...)
					},
				}).
				Build()
			helper, _ := admissioncheck.NewMultiKueueStoreHelper(c)
			adapters, _ := jobs.NewIntegrationManager().GetMultiKueueAdapters(sets.New("batch/job"))
			cRec := newClustersReconciler(c, TestNamespace, 0, defaultOrigin, nil, adapters, nil, nil, &utiltesting.EventRecorder{})
			reconciler := newCQReconciler(c, helper, cRec, nil, 100*time.Millisecond)

			req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "cq1"}}
			if _, err := reconciler.Reconcile(ctx, req); err != nil {
				t.Fatalf("unexpected reconcile error: %v", err)
			}
			expectCQClusterStatusMetric(t, "cq1", "worker1", metav1.ConditionTrue)

			// A read failure must fail the reconcile so that it is retried, and must leave
			// the statuses reported so far in place: failing to read is not evidence that
			// the ClusterQueue stopped using its workers.
			readsFail = true
			if _, err := reconciler.Reconcile(ctx, req); !errors.Is(err, readErr) {
				t.Fatalf("expected the reconcile to fail with %q, got %v", readErr, err)
			}
			expectCQClusterStatusMetric(t, "cq1", "worker1", metav1.ConditionTrue)
		})
	}
}

func TestCQReconcilerClearsClusterStatusMetricPerClusterQueue(t *testing.T) {
	metrics.MultiKueueClusterByStatus.Reset()
	t.Cleanup(metrics.MultiKueueClusterByStatus.Reset)

	ctx, _ := utiltesting.ContextWithLog(t)

	// Both ClusterQueues use the same admission check, so they share worker1.
	cq1 := utiltestingapi.MakeClusterQueue("cq1").
		ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource("cpu", "0").Obj()).
		AdmissionChecks("ac1").
		Obj()
	cq2 := utiltestingapi.MakeClusterQueue("cq2").
		ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource("cpu", "0").Obj()).
		AdmissionChecks("ac1").
		Obj()
	ac := utiltestingapi.MakeAdmissionCheck("ac1").
		ControllerName(kueue.MultiKueueControllerName).
		Parameters(kueue.SchemeGroupVersion.Group, "MultiKueueConfig", "config1").
		Obj()
	cfg := utiltestingapi.MakeMultiKueueConfig("config1").Clusters("worker1").Obj()
	worker1 := utiltestingapi.MakeMultiKueueCluster("worker1").Active(metav1.ConditionTrue, "Active", "Connected", 1).Obj()

	c := utiltesting.NewClientBuilder().
		WithObjects(cq1, cq2, ac, cfg, worker1).
		WithIndex(&kueue.AdmissionCheck{}, AdmissionCheckControllerNameKey, admissionCheckControllerNameIndexerFunc).
		WithIndex(&kueue.AdmissionCheck{}, AdmissionCheckUsingConfigKey, admissioncheck.IndexerByConfigFunction(kueue.MultiKueueControllerName, configGVK)).
		WithIndex(&kueue.ClusterQueue{}, ClusterQueueAdmissionChecksKey, clusterQueueAdmissionChecksIndexerFunc).
		WithStatusSubresource(cq1, cq2).
		Build()
	helper, _ := admissioncheck.NewMultiKueueStoreHelper(c)
	adapters, _ := jobs.NewIntegrationManager().GetMultiKueueAdapters(sets.New("batch/job"))
	cRec := newClustersReconciler(c, TestNamespace, 0, defaultOrigin, nil, adapters, nil, nil, &utiltesting.EventRecorder{})
	reconciler := newCQReconciler(c, helper, cRec, nil, 100*time.Millisecond)

	req1 := reconcile.Request{NamespacedName: types.NamespacedName{Name: "cq1"}}
	req2 := reconcile.Request{NamespacedName: types.NamespacedName{Name: "cq2"}}
	if _, err := reconciler.Reconcile(ctx, req1); err != nil {
		t.Fatalf("unexpected reconcile error for cq1: %v", err)
	}
	if _, err := reconciler.Reconcile(ctx, req2); err != nil {
		t.Fatalf("unexpected reconcile error for cq2: %v", err)
	}

	// The shared worker is reported once per ClusterQueue.
	expectCQClusterStatusMetric(t, "cq1", "worker1", metav1.ConditionTrue)
	expectCQClusterStatusMetric(t, "cq2", "worker1", metav1.ConditionTrue)

	// Deleting one ClusterQueue drops only its own series, otherwise the other
	// ClusterQueue would stop reporting a worker it still uses.
	if err := c.Delete(ctx, cq1); err != nil {
		t.Fatalf("unexpected error deleting the ClusterQueue: %v", err)
	}
	if _, err := reconciler.Reconcile(ctx, req1); err != nil {
		t.Fatalf("unexpected reconcile error after deletion: %v", err)
	}
	if got := testutil.CollectAndCount(metrics.MultiKueueClusterByStatus); got != len(metrics.ConditionStatusValues) {
		t.Errorf("expected only the cq2 series to remain, got %d series", got)
	}
	expectCQClusterStatusMetric(t, "cq2", "worker1", metav1.ConditionTrue)
}
