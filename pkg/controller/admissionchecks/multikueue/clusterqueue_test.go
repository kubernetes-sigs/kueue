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

	"github.com/google/go-cmp/cmp"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
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

		wantQuotaAutomated      bool
		wantNominalQuotas       map[string]string // Ignored if wantQuotaAutomated == false
		wantAutomationCondition bool
		wantReason              string
		wantMessage             string
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
					Parameters(kueue.GroupVersion.Group, "MultiKueueConfig", "config1").
					Obj(),
			},
			configs: []*kueue.MultiKueueConfig{
				utiltestingapi.MakeMultiKueueConfig("config1").Clusters("worker1", "worker2").Obj(),
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
			wantQuotaAutomated:      true,
			wantNominalQuotas: map[string]string{
				"cpu":    "15",
				"memory": "30Gi",
			},
			wantAutomationCondition: true,
			wantReason:              "QuotaAutomated",
			wantMessage:             "ClusterQueue quota is automatically managed based on MultiKueue workers.",
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
					Parameters(kueue.GroupVersion.Group, "MultiKueueConfig", "config1").
					Obj(),
			},
			configs: []*kueue.MultiKueueConfig{
				utiltestingapi.MakeMultiKueueConfig("config1").Clusters("worker1", "worker2").Obj(),
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
			wantQuotaAutomated:      true,
			wantNominalQuotas: map[string]string{
				"cpu": "10",
			},
			wantAutomationCondition: true,
			wantReason:              "QuotaAutomated",
			wantMessage:             "ClusterQueue quota is automatically managed based on MultiKueue workers.",
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
					Parameters(kueue.GroupVersion.Group, "MultiKueueConfig", "config1").
					Obj(),
			},
			configs: []*kueue.MultiKueueConfig{
				utiltestingapi.MakeMultiKueueConfig("config1").Clusters("worker1", "worker2").Obj(),
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
			wantQuotaAutomated:      true,
			wantNominalQuotas: map[string]string{
				"cpu": "23", // 10 + 5 + 8; 10 should be counted only once
			},
			wantAutomationCondition: true,
			wantReason:              "QuotaAutomated",
			wantMessage:             "ClusterQueue quota is automatically managed based on MultiKueue workers.",
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
					Parameters(kueue.GroupVersion.Group, "MultiKueueConfig", "config1").
					Obj(),
			},
			configs: []*kueue.MultiKueueConfig{
				utiltestingapi.MakeMultiKueueConfig("config1").Clusters("worker1", "worker2").Obj(),
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
			wantQuotaAutomated:      true,
			wantNominalQuotas: map[string]string{
				"cpu": "19",
				"gpu": "4",
			},
			wantAutomationCondition: true,
			wantReason:              "QuotaAutomated",
			wantMessage:             "ClusterQueue quota is automatically managed based on MultiKueue workers.",
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
					Parameters(kueue.GroupVersion.Group, "MultiKueueConfig", "config1").
					Obj(),
			},
			configs: []*kueue.MultiKueueConfig{
				utiltestingapi.MakeMultiKueueConfig("config1").Clusters("worker1", "worker2").Obj(),
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
			wantQuotaAutomated:      true,
			wantNominalQuotas: map[string]string{
				"cpu":    "15",
				"memory": "20Gi",
				"gpu":    "0",
			},
			wantAutomationCondition: true,
			wantReason:              "QuotaAutomated",
			wantMessage:             "ClusterQueue quota is automatically managed based on MultiKueue workers.",
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
					Parameters(kueue.GroupVersion.Group, "MultiKueueConfig", "config1").
					Obj(),
			},
			configs: []*kueue.MultiKueueConfig{
				func() *kueue.MultiKueueConfig {
					cfg := utiltestingapi.MakeMultiKueueConfig("config1").Clusters("worker1").Obj()
					cfg.Spec.QuotaAutomation = &kueue.QuotaAutomation{Mode: kueue.QuotaAutomationManual}
					return cfg
				}(),
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
			wantQuotaAutomated:      false,
			wantAutomationCondition: true,
			wantReason:              "NotRequested",
			wantMessage:             "MultiKueue manager quota automation has not been requested.",
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
					Parameters(kueue.GroupVersion.Group, "MultiKueueConfig", "config1").
					Obj(),
			},
			configs: []*kueue.MultiKueueConfig{
				utiltestingapi.MakeMultiKueueConfig("config1").Clusters("worker1").Obj(),
			},
			wantQuotaAutomated:      false,
			wantAutomationCondition: true,
			wantReason:              "UnsupportedConfiguration",
			wantMessage:             "manager-side ClusterQueue must have exactly one ResourceFlavor",
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
					Parameters(kueue.GroupVersion.Group, "MultiKueueConfig", "config1").
					Obj(),
			},
			configs: []*kueue.MultiKueueConfig{
				utiltestingapi.MakeMultiKueueConfig("config1").Clusters("worker1").Obj(),
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
			wantQuotaAutomated:      false,
			wantAutomationCondition: true,
			wantReason:              "UnsupportedConfiguration",
			wantMessage:             "manager-side coveredResources is missing resources configured on workers: [gpu memory]",
		},
		"not a MultiKueue manager ClusterQueue": {
			cq: utiltestingapi.MakeClusterQueue("cq1").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource("cpu", "100").Obj()).
				Condition(string(kueue.MultiKueueManagerQuotaAutomation), metav1.ConditionTrue, "QuotaAutomated", "ClusterQueue quota is automatically managed based on MultiKueue workers.").
				Obj(),
			lqs: []*kueue.LocalQueue{
				utiltestingapi.MakeLocalQueue("lq1", TestNamespace).ClusterQueue("cq1").Obj(),
			},
			wantQuotaAutomated:      false,
			wantAutomationCondition: false,
		},
		"referenced MultiKueueConfig not found": {
			cq: utiltestingapi.MakeClusterQueue("cq1").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource("cpu", "100").Obj()).
				AdmissionChecks("ac1").
				Obj(),
			acs: []*kueue.AdmissionCheck{
				utiltestingapi.MakeAdmissionCheck("ac1").
					ControllerName(kueue.MultiKueueControllerName).
					Parameters(kueue.GroupVersion.Group, "MultiKueueConfig", "config-not-found").
					Obj(),
			},
			wantQuotaAutomated:      false,
			wantAutomationCondition: true,
			wantReason:              "UnsupportedConfiguration",
			wantMessage:             "The referenced MultiKueueConfig was not found.",
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

			adapters, _ := jobframework.GetMultiKueueAdapters(sets.New("batch/job"))
			cRec := newClustersReconciler(c, TestNamespace, 0, defaultOrigin, nil, adapters, nil, nil)
			cRec.rootContext = ctx

			for worker, wState := range tc.workers {
				workerClient := utiltesting.NewClientBuilder().
					WithObjects(asObjs(wState.cqs)...).
					WithObjects(asObjs(wState.lqs)...).
					Build()
				rc := newRemoteClient(c, nil, nil, nil, defaultOrigin, worker, adapters)
				rc.client = workerClient
				rc.connecting.Store(wState.inactive)
				cRec.remoteClients[worker] = rc
			}

			helper, _ := admissioncheck.NewMultiKueueStoreHelper(c)
			reconciler := newCQReconciler(c, helper, cRec, nil)

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
			cond := apimeta.FindStatusCondition(gotCQ.Status.Conditions, string(kueue.MultiKueueManagerQuotaAutomation))
			if tc.wantAutomationCondition {
				if cond == nil {
					t.Error("expected status condition to be defined, got nil")
				} else {
					expectedStatus := metav1.ConditionFalse
					if tc.wantQuotaAutomated {
						expectedStatus = metav1.ConditionTrue
					}
					if cond.Status != expectedStatus {
						t.Errorf("expected status: %s, got: %s", expectedStatus, cond.Status)
					}
					if cond.Reason != tc.wantReason {
						t.Errorf("expected reason: %s, got: %s", tc.wantReason, cond.Reason)
					}
					if cond.Message != tc.wantMessage {
						t.Errorf("expected message: %q, got: %q", tc.wantMessage, cond.Message)
					}
				}
			} else {
				if cond != nil {
					t.Errorf("expected status condition to be absent, got: %v", cond)
				}
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
