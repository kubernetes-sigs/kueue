/*
Copyright 2023 The Kubernetes Authors.

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

package cache

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestClusterQueueUpdateWithFlavors(t *testing.T) {
	rf := utiltesting.MakeResourceFlavor("x86").Obj()
	cq := utiltesting.MakeClusterQueue("cq").
		ResourceGroup(*utiltesting.MakeFlavorQuotas("x86").Resource("cpu", "5").Obj()).
		Obj()

	testcases := []struct {
		name       string
		curStatus  metrics.ClusterQueueStatus
		flavors    map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor
		wantStatus metrics.ClusterQueueStatus
	}{
		{
			name:      "Pending clusterQueue updated existent flavors",
			curStatus: pending,
			flavors: map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor{
				kueue.ResourceFlavorReference(rf.Name): rf,
			},
			wantStatus: active,
		},
		{
			name:       "Active clusterQueue updated with not found flavors",
			curStatus:  active,
			flavors:    map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor{},
			wantStatus: pending,
		},
		{
			name:      "Terminating clusterQueue updated with existent flavors",
			curStatus: terminating,
			flavors: map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor{
				kueue.ResourceFlavorReference(rf.Name): rf,
			},
			wantStatus: terminating,
		},
		{
			name:       "Terminating clusterQueue updated with not found flavors",
			curStatus:  terminating,
			wantStatus: terminating,
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			cache := New(utiltesting.NewFakeClient())
			cq, err := cache.newClusterQueue(cq)
			if err != nil {
				t.Fatalf("failed to new clusterQueue %v", err)
			}

			cq.Status = tc.curStatus
			cq.UpdateWithFlavors(tc.flavors)

			if cq.Status != tc.wantStatus {
				t.Fatalf("got different status, want: %v, got: %v", tc.wantStatus, cq.Status)
			}
		})
	}
}

func TestFitInCohort(t *testing.T) {
	cases := map[string]struct {
		request            FlavorResourceQuantities
		wantFit            bool
		cq                 *ClusterQueue
		enableLendingLimit bool
	}{
		"full cohort, empty request": {
			request: FlavorResourceQuantities{},
			wantFit: true,
			cq: &ClusterQueue{
				Name: "CQ",
				Cohort: &Cohort{
					Name: "C",
					RequestableResources: FlavorResourceQuantities{
						"f1": map[corev1.ResourceName]int64{
							corev1.ResourceCPU:    5,
							corev1.ResourceMemory: 5,
						},
						"f2": map[corev1.ResourceName]int64{
							corev1.ResourceCPU:    5,
							corev1.ResourceMemory: 5,
						},
					},
					Usage: FlavorResourceQuantities{
						"f1": map[corev1.ResourceName]int64{
							corev1.ResourceCPU:    5,
							corev1.ResourceMemory: 5,
						},
						"f2": map[corev1.ResourceName]int64{
							corev1.ResourceCPU:    5,
							corev1.ResourceMemory: 5,
						},
					},
				},
				ResourceGroups: nil,
			},
		},
		"can fit": {
			request: FlavorResourceQuantities{
				"f2": map[corev1.ResourceName]int64{
					corev1.ResourceCPU:    1,
					corev1.ResourceMemory: 1,
				},
			},
			wantFit: true,
			cq: &ClusterQueue{
				Name: "CQ",
				Cohort: &Cohort{
					Name: "C",
					RequestableResources: FlavorResourceQuantities{
						"f1": map[corev1.ResourceName]int64{
							corev1.ResourceCPU:    5,
							corev1.ResourceMemory: 5,
						},
						"f2": map[corev1.ResourceName]int64{
							corev1.ResourceCPU:    5,
							corev1.ResourceMemory: 5,
						},
					},
					Usage: FlavorResourceQuantities{
						"f1": map[corev1.ResourceName]int64{
							corev1.ResourceCPU:    5,
							corev1.ResourceMemory: 5,
						},
						"f2": map[corev1.ResourceName]int64{
							corev1.ResourceCPU:    4,
							corev1.ResourceMemory: 4,
						},
					},
				},
				ResourceGroups: nil,
			},
		},
		"full cohort, none fit": {
			request: FlavorResourceQuantities{
				"f1": map[corev1.ResourceName]int64{
					corev1.ResourceCPU:    1,
					corev1.ResourceMemory: 1,
				},
				"f2": map[corev1.ResourceName]int64{
					corev1.ResourceCPU:    1,
					corev1.ResourceMemory: 1,
				},
			},
			wantFit: false,
			cq: &ClusterQueue{
				Name: "CQ",
				Cohort: &Cohort{
					Name: "C",
					RequestableResources: FlavorResourceQuantities{
						"f1": map[corev1.ResourceName]int64{
							corev1.ResourceCPU:    5,
							corev1.ResourceMemory: 5,
						},
						"f2": map[corev1.ResourceName]int64{
							corev1.ResourceCPU:    5,
							corev1.ResourceMemory: 5,
						},
					},
					Usage: FlavorResourceQuantities{
						"f1": map[corev1.ResourceName]int64{
							corev1.ResourceCPU:    5,
							corev1.ResourceMemory: 5,
						},
						"f2": map[corev1.ResourceName]int64{
							corev1.ResourceCPU:    5,
							corev1.ResourceMemory: 5,
						},
					},
				},
				ResourceGroups: nil,
			},
		},
		"one cannot fit": {
			request: FlavorResourceQuantities{
				"f1": map[corev1.ResourceName]int64{
					corev1.ResourceCPU:    1,
					corev1.ResourceMemory: 1,
				},
				"f2": map[corev1.ResourceName]int64{
					corev1.ResourceCPU:    2,
					corev1.ResourceMemory: 1,
				},
			},
			wantFit: false,
			cq: &ClusterQueue{
				Name: "CQ",
				Cohort: &Cohort{
					Name: "C",
					RequestableResources: FlavorResourceQuantities{
						"f1": map[corev1.ResourceName]int64{
							corev1.ResourceCPU:    5,
							corev1.ResourceMemory: 5,
						},
						"f2": map[corev1.ResourceName]int64{
							corev1.ResourceCPU:    5,
							corev1.ResourceMemory: 5,
						},
					},
					Usage: FlavorResourceQuantities{
						"f1": map[corev1.ResourceName]int64{
							corev1.ResourceCPU:    4,
							corev1.ResourceMemory: 4,
						},
						"f2": map[corev1.ResourceName]int64{
							corev1.ResourceCPU:    4,
							corev1.ResourceMemory: 4,
						},
					},
				},
				ResourceGroups: nil,
			},
		},
		"missing flavor": {
			request: FlavorResourceQuantities{
				"f2": map[corev1.ResourceName]int64{
					corev1.ResourceCPU:    1,
					corev1.ResourceMemory: 1,
				},
			},
			wantFit: false,
			cq: &ClusterQueue{
				Name: "CQ",
				Cohort: &Cohort{
					Name: "C",
					RequestableResources: FlavorResourceQuantities{
						"f1": map[corev1.ResourceName]int64{
							corev1.ResourceCPU:    5,
							corev1.ResourceMemory: 5,
						},
					},
					Usage: FlavorResourceQuantities{
						"f1": map[corev1.ResourceName]int64{
							corev1.ResourceCPU:    5,
							corev1.ResourceMemory: 5,
						},
					},
				},
				ResourceGroups: nil,
			},
		},
		"missing resource": {
			request: FlavorResourceQuantities{
				"f1": map[corev1.ResourceName]int64{
					corev1.ResourceCPU:    1,
					corev1.ResourceMemory: 1,
				},
			},
			wantFit: false,
			cq: &ClusterQueue{
				Name: "CQ",
				Cohort: &Cohort{
					Name: "C",
					RequestableResources: FlavorResourceQuantities{
						"f1": map[corev1.ResourceName]int64{
							corev1.ResourceCPU: 5,
						},
					},
					Usage: FlavorResourceQuantities{
						"f1": map[corev1.ResourceName]int64{
							corev1.ResourceCPU: 3,
						},
					},
				},
				ResourceGroups: nil,
			},
		},
		"lendingLimit enabled can't fit": {
			request: FlavorResourceQuantities{
				"f1": map[corev1.ResourceName]int64{
					corev1.ResourceCPU: 3,
				},
			},
			wantFit: false,
			cq: &ClusterQueue{
				Name: "CQ-A",
				Cohort: &Cohort{
					Name: "C",
					RequestableResources: FlavorResourceQuantities{
						"f1": map[corev1.ResourceName]int64{
							// CQ-A has 2 nominal cpu, CQ-B has 3 nominal cpu and 2 lendingLimit,
							// so when lendingLimit enabled, the cohort's RequestableResources is 4 cpu.
							corev1.ResourceCPU: 4,
						},
					},
					Usage: FlavorResourceQuantities{
						"f1": map[corev1.ResourceName]int64{
							corev1.ResourceCPU: 2,
						},
					},
				},
				GuaranteedQuota: FlavorResourceQuantities{
					"f1": {
						corev1.ResourceCPU: 0,
					},
				},
			},
			enableLendingLimit: true,
		},
		"lendingLimit enabled can fit": {
			request: FlavorResourceQuantities{
				"f1": map[corev1.ResourceName]int64{
					corev1.ResourceCPU: 3,
				},
			},
			wantFit: true,
			cq: &ClusterQueue{
				Name: "CQ-A",
				Cohort: &Cohort{
					Name: "C",
					RequestableResources: FlavorResourceQuantities{
						"f1": map[corev1.ResourceName]int64{
							// CQ-A has 2 nominal cpu, CQ-B has 3 nominal cpu and 2 lendingLimit,
							// so when lendingLimit enabled, the cohort's RequestableResources is 4 cpu.
							corev1.ResourceCPU: 4,
						},
					},
					Usage: FlavorResourceQuantities{
						"f1": map[corev1.ResourceName]int64{
							// CQ-B has admitted a workload with 2 cpus, but with 1 GuaranteedQuota,
							// so when lendingLimit enabled, Cohort.Usage should be 2 - 1 = 1.
							corev1.ResourceCPU: 1,
						},
					},
				},
				GuaranteedQuota: FlavorResourceQuantities{
					"f1": {
						corev1.ResourceCPU: 2,
					},
				},
			},
			enableLendingLimit: true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			defer features.SetFeatureGateDuringTest(t, features.LendingLimit, tc.enableLendingLimit)()
			got := tc.cq.FitInCohort(tc.request)
			if got != tc.wantFit {
				t.Errorf("Unexpected result, %v", got)
			}

		})
	}

}

func TestClusterQueueUpdate(t *testing.T) {
	resourceFlavors := []*kueue.ResourceFlavor{
		{ObjectMeta: metav1.ObjectMeta{Name: "on-demand"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "spot"}},
	}
	clusterQueue :=
		*utiltesting.MakeClusterQueue("eng-alpha").
			QueueingStrategy(kueue.StrictFIFO).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
			}).
			FlavorFungibility(kueue.FlavorFungibility{
				WhenCanPreempt: kueue.Preempt,
			}).
			ResourceGroup(
				*utiltesting.MakeFlavorQuotas("on-demand").
					Resource(corev1.ResourceCPU, "50", "50").Obj(),
				*utiltesting.MakeFlavorQuotas("spot").
					Resource(corev1.ResourceCPU, "100", "0").Obj(),
			).Obj()
	newClusterQueue :=
		*utiltesting.MakeClusterQueue("eng-alpha").
			QueueingStrategy(kueue.StrictFIFO).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
			}).
			FlavorFungibility(kueue.FlavorFungibility{
				WhenCanPreempt: kueue.Preempt,
			}).
			ResourceGroup(
				*utiltesting.MakeFlavorQuotas("on-demand").
					Resource(corev1.ResourceCPU, "100", "50").Obj(),
				*utiltesting.MakeFlavorQuotas("spot").
					Resource(corev1.ResourceCPU, "100", "0").Obj(),
			).Obj()
	cases := []struct {
		name                         string
		cq                           *kueue.ClusterQueue
		newcq                        *kueue.ClusterQueue
		wantLastAssignmentGeneration int64
	}{
		{
			name:                         "RGs not change",
			cq:                           &clusterQueue,
			newcq:                        clusterQueue.DeepCopy(),
			wantLastAssignmentGeneration: 1,
		},
		{
			name:                         "RGs changed",
			cq:                           &clusterQueue,
			newcq:                        &newClusterQueue,
			wantLastAssignmentGeneration: 2,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			clientBuilder := utiltesting.NewClientBuilder().
				WithObjects(
					&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}},
					tc.cq,
				)
			cl := clientBuilder.Build()
			cqCache := New(cl)
			// Workloads are loaded into queues or clusterQueues as we add them.
			for _, rf := range resourceFlavors {
				cqCache.AddOrUpdateResourceFlavor(rf)
			}
			if err := cqCache.AddClusterQueue(ctx, tc.cq); err != nil {
				t.Fatalf("Inserting clusterQueue %s in cache: %v", tc.cq.Name, err)
			}
			if err := cqCache.UpdateClusterQueue(tc.newcq); err != nil {
				t.Fatalf("Updating clusterQueue %s in cache: %v", tc.newcq.Name, err)
			}
			snapshot := cqCache.Snapshot()
			if diff := cmp.Diff(
				tc.wantLastAssignmentGeneration,
				snapshot.ClusterQueues["eng-alpha"].AllocatableResourceGeneration); diff != "" {
				t.Errorf("Unexpected assigned clusterQueues in cache (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestClusterQueueUpdateWithAdmissionCheck(t *testing.T) {
	cqWithAC := utiltesting.MakeClusterQueue("cq").
		AdmissionChecks("check1", "check2", "check3").
		Obj()

	cqWithACStrategy := utiltesting.MakeClusterQueue("cq2").
		AdmissionCheckStrategy(
			*utiltesting.MakeAdmissionCheckStrategyRule("check1").Obj(),
			*utiltesting.MakeAdmissionCheckStrategyRule("check2").Obj(),
			*utiltesting.MakeAdmissionCheckStrategyRule("check3").Obj()).
		Obj()

	cqWithACPerFlavor := utiltesting.MakeClusterQueue("cq3").
		AdmissionCheckStrategy(
			*utiltesting.MakeAdmissionCheckStrategyRule("check1", "flavor1", "flavor2", "flavor3").Obj(),
		).
		Obj()

	testcases := []struct {
		name            string
		cq              *kueue.ClusterQueue
		cqStatus        metrics.ClusterQueueStatus
		admissionChecks map[string]AdmissionCheck
		wantStatus      metrics.ClusterQueueStatus
		wantReason      string
	}{
		{
			name:     "Pending clusterQueue updated valid AC list",
			cq:       cqWithAC,
			cqStatus: pending,
			admissionChecks: map[string]AdmissionCheck{
				"check1": {
					Active:     true,
					Controller: "controller1",
				},
				"check2": {
					Active:     true,
					Controller: "controller2",
				},
				"check3": {
					Active:     true,
					Controller: "controller3",
				},
			},
			wantStatus: active,
			wantReason: "Ready",
		},
		{
			name:     "Pending clusterQueue with an AC strategy updated valid AC list",
			cq:       cqWithACStrategy,
			cqStatus: pending,
			admissionChecks: map[string]AdmissionCheck{
				"check1": {
					Active:     true,
					Controller: "controller1",
				},
				"check2": {
					Active:     true,
					Controller: "controller2",
				},
				"check3": {
					Active:     true,
					Controller: "controller3",
				},
			},
			wantStatus: active,
			wantReason: "Ready",
		},
		{
			name:     "Active clusterQueue updated with not found AC",
			cq:       cqWithAC,
			cqStatus: active,
			admissionChecks: map[string]AdmissionCheck{
				"check1": {
					Active:     true,
					Controller: "controller1",
				},
				"check2": {
					Active:     true,
					Controller: "controller2",
				},
			},
			wantStatus: pending,
			wantReason: "CheckNotFoundOrInactive",
		},
		{
			name:     "Active clusterQueue with an AC strategy updated with not found AC",
			cq:       cqWithACStrategy,
			cqStatus: active,
			admissionChecks: map[string]AdmissionCheck{
				"check1": {
					Active:     true,
					Controller: "controller1",
				},
				"check2": {
					Active:     true,
					Controller: "controller2",
				},
			},
			wantStatus: pending,
			wantReason: "CheckNotFoundOrInactive",
		},
		{
			name:     "Active clusterQueue updated with inactive AC",
			cq:       cqWithAC,
			cqStatus: active,
			admissionChecks: map[string]AdmissionCheck{
				"check1": {
					Active:     true,
					Controller: "controller1",
				},
				"check2": {
					Active:     true,
					Controller: "controller2",
				},
				"check3": {
					Active:     false,
					Controller: "controller3",
				},
			},
			wantStatus: pending,
			wantReason: "CheckNotFoundOrInactive",
		},
		{
			name:     "Active clusterQueue with an AC strategy updated with inactive AC",
			cq:       cqWithACStrategy,
			cqStatus: active,
			admissionChecks: map[string]AdmissionCheck{
				"check1": {
					Active:     true,
					Controller: "controller1",
				},
				"check2": {
					Active:     true,
					Controller: "controller2",
				},
				"check3": {
					Active:     false,
					Controller: "controller3",
				},
			},
			wantStatus: pending,
			wantReason: "CheckNotFoundOrInactive",
		},
		{
			name:     "Active clusterQueue updated with duplicate single instance AC Controller",
			cq:       cqWithAC,
			cqStatus: active,
			admissionChecks: map[string]AdmissionCheck{
				"check1": {
					Active:                       true,
					Controller:                   "controller1",
					SingleInstanceInClusterQueue: true,
				},
				"check2": {
					Active:     true,
					Controller: "controller2",
				},
				"check3": {
					Active:                       true,
					Controller:                   "controller2",
					SingleInstanceInClusterQueue: true,
				},
			},
			wantStatus: pending,
			wantReason: "MultipleSingleInstanceControllerChecks",
		},
		{
			name:     "Active clusterQueue with an AC strategy updated with duplicate single instance AC Controller",
			cq:       cqWithACStrategy,
			cqStatus: active,
			admissionChecks: map[string]AdmissionCheck{
				"check1": {
					Active:                       true,
					Controller:                   "controller1",
					SingleInstanceInClusterQueue: true,
				},
				"check2": {
					Active:     true,
					Controller: "controller2",
				},
				"check3": {
					Active:                       true,
					Controller:                   "controller2",
					SingleInstanceInClusterQueue: true,
				},
			},
			wantStatus: pending,
			wantReason: "MultipleSingleInstanceControllerChecks",
		},
		{
			name:     "Active clusterQueue with a FlavorIndependent AC applied per ResourceFlavor",
			cq:       cqWithACPerFlavor,
			cqStatus: pending,
			admissionChecks: map[string]AdmissionCheck{
				"check1": {
					Active:            true,
					Controller:        "controller1",
					FlavorIndependent: true,
				},
			},
			wantStatus: pending,
			wantReason: "FlavorIndependentAdmissionCheckAppliedPerFlavor",
		},
		{
			name:     "Terminating clusterQueue updated with valid AC list",
			cq:       cqWithAC,
			cqStatus: terminating,
			admissionChecks: map[string]AdmissionCheck{
				"check1": {
					Active:     true,
					Controller: "controller1",
				},
				"check2": {
					Active:     true,
					Controller: "controller2",
				},
				"check3": {
					Active:     true,
					Controller: "controller3",
				},
			},
			wantStatus: terminating,
			wantReason: "Terminating",
		},
		{
			name:     "Terminating clusterQueue with an AC strategy updated with valid AC list",
			cq:       cqWithACStrategy,
			cqStatus: terminating,
			admissionChecks: map[string]AdmissionCheck{
				"check1": {
					Active:     true,
					Controller: "controller1",
				},
				"check2": {
					Active:     true,
					Controller: "controller2",
				},
				"check3": {
					Active:     true,
					Controller: "controller3",
				},
			},
			wantStatus: terminating,
			wantReason: "Terminating",
		},
		{
			name:     "Terminating clusterQueue updated with not found AC",
			cq:       cqWithAC,
			cqStatus: terminating,
			admissionChecks: map[string]AdmissionCheck{
				"check1": {
					Active:     true,
					Controller: "controller1",
				},
				"check2": {
					Active:     true,
					Controller: "controller2",
				},
			},
			wantStatus: terminating,
			wantReason: "Terminating",
		},
		{
			name:     "Terminating clusterQueue with an AC strategy updated with not found AC",
			cq:       cqWithACStrategy,
			cqStatus: terminating,
			admissionChecks: map[string]AdmissionCheck{
				"check1": {
					Active:     true,
					Controller: "controller1",
				},
				"check2": {
					Active:     true,
					Controller: "controller2",
				},
			},
			wantStatus: terminating,
			wantReason: "Terminating",
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			cache := New(utiltesting.NewFakeClient())
			cq, err := cache.newClusterQueue(tc.cq)
			if err != nil {
				t.Fatalf("failed to new clusterQueue %v", err)
			}

			cq.Status = tc.cqStatus

			// Align the admission check related internals to the desired Status.
			if tc.cqStatus == active {
				cq.hasMultipleSingleInstanceControllersChecks = false
				cq.hasMissingOrInactiveAdmissionChecks = false
				cq.hasFlavorIndependentAdmissionCheckAppliedAtFlavorLevel = false
			} else {
				cq.hasMultipleSingleInstanceControllersChecks = true
				cq.hasMissingOrInactiveAdmissionChecks = true
				cq.hasFlavorIndependentAdmissionCheckAppliedAtFlavorLevel = true
			}
			cq.updateWithAdmissionChecks(tc.admissionChecks)

			if cq.Status != tc.wantStatus {
				t.Errorf("got different status, want: %v, got: %v", tc.wantStatus, cq.Status)
			}

			gotReason, _ := cq.inactiveReason()
			if diff := cmp.Diff(tc.wantReason, gotReason); diff != "" {
				t.Errorf("Unexpected inactiveReason (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestDominantResourceShare(t *testing.T) {
	cases := map[string]struct {
		cq          ClusterQueue
		workload    *workload.Info
		wantDRValue int
		wantDRName  corev1.ResourceName
	}{
		"no cohort": {
			cq: ClusterQueue{
				ResourceStats: ResourceStats{
					corev1.ResourceCPU: {
						Nominal:  2_000,
						Lendable: 2_000,
						Usage:    1_000,
					},
					"example.com/gpu": {
						Nominal:  5,
						Lendable: 5,
						Usage:    2_000,
					},
				},
			},
		},
		"usage below nominal": {
			cq: ClusterQueue{
				ResourceStats: ResourceStats{
					corev1.ResourceCPU: {
						Nominal:  2_000,
						Lendable: 2_000,
						Usage:    1_000,
					},
					"example.com/gpu": {
						Nominal:  5,
						Lendable: 5,
						Usage:    2,
					},
				},
				Cohort: &Cohort{
					ResourceStats: ResourceStats{
						corev1.ResourceCPU: {
							Nominal:  10_000,
							Lendable: 10_000,
							Usage:    2_000,
						},
						"example.com/gpu": {
							Nominal:  10,
							Lendable: 10,
							Usage:    6,
						},
					},
				},
			},
			wantDRName: corev1.ResourceCPU, // due to alphabetical order.
		},
		"usage above nominal": {
			cq: ClusterQueue{
				ResourceStats: ResourceStats{
					corev1.ResourceCPU: {
						Nominal:  2_000,
						Lendable: 2_000,
						Usage:    3_000,
					},
					"example.com/gpu": {
						Nominal:  5,
						Lendable: 5,
						Usage:    7,
					},
				},
				Cohort: &Cohort{
					ResourceStats: ResourceStats{
						corev1.ResourceCPU: {
							Nominal:  10_000,
							Lendable: 10_000,
							Usage:    10_000,
						},
						"example.com/gpu": {
							Nominal:  10,
							Lendable: 10,
							Usage:    10,
						},
					},
				},
			},
			wantDRName:  "example.com/gpu",
			wantDRValue: 20, // (7-5)/10
		},
		"one resource above nominal": {
			cq: ClusterQueue{
				ResourceStats: ResourceStats{
					corev1.ResourceCPU: {
						Nominal:  2_000,
						Lendable: 2_000,
						Usage:    3_000,
					},
					"example.com/gpu": {
						Nominal:  5,
						Lendable: 5,
						Usage:    3,
					},
				},
				Cohort: &Cohort{
					ResourceStats: ResourceStats{
						corev1.ResourceCPU: {
							Nominal:  10_000,
							Lendable: 10_000,
							Usage:    10_000,
						},
						"example.com/gpu": {
							Nominal:  10,
							Lendable: 10,
							Usage:    10,
						},
					},
				},
			},
			wantDRName:  corev1.ResourceCPU,
			wantDRValue: 10, // (3-2)/10
		},
		"usage with workload above nominal": {
			cq: ClusterQueue{
				ResourceStats: ResourceStats{
					corev1.ResourceCPU: {
						Nominal:  2_000,
						Lendable: 2_000,
						Usage:    1_000,
					},
					"example.com/gpu": {
						Nominal:  5,
						Lendable: 5,
						Usage:    2,
					},
				},
				Cohort: &Cohort{
					ResourceStats: ResourceStats{
						corev1.ResourceCPU: {
							Nominal:  10_000,
							Lendable: 10_000,
							Usage:    2_000,
						},
						"example.com/gpu": {
							Nominal:  10,
							Lendable: 10,
							Usage:    6,
						},
					},
				},
			},
			workload: &workload.Info{
				TotalRequests: []workload.PodSetResources{{
					Requests: workload.Requests{
						corev1.ResourceCPU: 4_000,
						"example.com/gpu":  4,
					},
				}},
			},
			wantDRName:  corev1.ResourceCPU,
			wantDRValue: 30, // (1+4-2)/10
		},
		"A resource with zero lendable": {
			cq: ClusterQueue{
				ResourceStats: ResourceStats{
					corev1.ResourceCPU: {
						Nominal:  2_000,
						Lendable: 2_000,
						Usage:    1_000,
					},
					"example.com/gpu": {
						Nominal: 2_000,
						Usage:   1_000,
					},
				},
				Cohort: &Cohort{
					ResourceStats: ResourceStats{
						corev1.ResourceCPU: {
							Nominal:  10_000,
							Lendable: 10_000,
							Usage:    2_000,
						},
						"example.com/gpu": {
							Nominal: 10_000,
							Usage:   5_000,
						},
					},
				},
			},
			workload: &workload.Info{
				TotalRequests: []workload.PodSetResources{{
					Requests: workload.Requests{
						corev1.ResourceCPU: 4_000,
						"example.com/gpu":  4,
					},
				}},
			},
			wantDRName:  corev1.ResourceCPU,
			wantDRValue: 30, // (1+4-2)/10
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			drValue, drName := tc.cq.DominantResourceShareWith(tc.workload)
			if drValue != tc.wantDRValue {
				t.Errorf("DominantResourceShare(_) returned value %d, want %d", drValue, tc.wantDRValue)
			}
			if drName != tc.wantDRName {
				t.Errorf("DominantResourceShare(_) returned resource %s, want %s", drName, tc.wantDRName)
			}
		})
	}
}
