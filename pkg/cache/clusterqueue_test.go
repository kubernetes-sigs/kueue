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
	"context"
	"fmt"
	"math"
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/resources"
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

func TestClusterQueueUpdate(t *testing.T) {
	resourceFlavors := []*kueue.ResourceFlavor{
		utiltesting.MakeResourceFlavor("on-demand").Obj(),
		utiltesting.MakeResourceFlavor("spot").Obj(),
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
			snapshot, err := cqCache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("unexpected error while building snapshot: %v", err)
			}
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
		name                     string
		cq                       *kueue.ClusterQueue
		cqStatus                 metrics.ClusterQueueStatus
		admissionChecks          map[string]AdmissionCheck
		wantStatus               metrics.ClusterQueueStatus
		wantReason               string
		wantMessage              string
		acValidationRulesEnabled bool
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
			wantStatus:  active,
			wantReason:  "Ready",
			wantMessage: "Can admit new workloads",
		},
		{
			name:     "Pending clusterQueue updated valid AC list - AdmissionCheckValidationRules enabled",
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
			wantStatus:               active,
			wantReason:               "Ready",
			wantMessage:              "Can admit new workloads",
			acValidationRulesEnabled: true,
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
			wantStatus:  active,
			wantReason:  "Ready",
			wantMessage: "Can admit new workloads",
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
			wantStatus:  pending,
			wantReason:  "AdmissionCheckNotFound",
			wantMessage: "Can't admit new workloads: references missing AdmissionCheck(s): [check3].",
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
			wantStatus:  pending,
			wantReason:  "AdmissionCheckNotFound",
			wantMessage: "Can't admit new workloads: references missing AdmissionCheck(s): [check3].",
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
			wantStatus:  pending,
			wantReason:  "AdmissionCheckInactive",
			wantMessage: "Can't admit new workloads: references inactive AdmissionCheck(s): [check3].",
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
			wantStatus:  pending,
			wantReason:  "AdmissionCheckInactive",
			wantMessage: "Can't admit new workloads: references inactive AdmissionCheck(s): [check3].",
		},
		{
			name:     "Active clusterQueue updated with duplicate single instance AC Controller - AdmissionCheckValidationRules enabled",
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
			wantStatus:               pending,
			wantReason:               "MultipleSingleInstanceControllerAdmissionChecks",
			wantMessage:              `Can't admit new workloads: only one AdmissionCheck of [check2 check3] can be referenced for controller "controller2".`,
			acValidationRulesEnabled: true,
		},
		{
			name:     "Active clusterQueue with an AC strategy updated with duplicate single instance AC Controller - AdmissionCheckValidationRules enabled",
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
			wantStatus:               pending,
			wantReason:               "MultipleSingleInstanceControllerAdmissionChecks",
			wantMessage:              `Can't admit new workloads: only one AdmissionCheck of [check2 check3] can be referenced for controller "controller2".`,
			acValidationRulesEnabled: true,
		},
		{
			name:     "Active clusterQueue with an MultiKueue AC strategy updated with duplicate single instance AC Controller",
			cq:       cqWithACStrategy,
			cqStatus: active,
			admissionChecks: map[string]AdmissionCheck{
				"check1": {
					Active:     true,
					Controller: kueue.MultiKueueControllerName,
				},
				"check2": {
					Active:     true,
					Controller: "controller2",
				},
				"check3": {
					Active:     true,
					Controller: kueue.MultiKueueControllerName,
				},
			},
			wantStatus:  pending,
			wantReason:  kueue.ClusterQueueActiveReasonMultipleMultiKueueAdmissionChecks,
			wantMessage: `Can't admit new workloads: Cannot use multiple MultiKueue AdmissionChecks on the same ClusterQueue, found: check1,check3.`,
		},
		{
			name:     "Pending clusterQueue with a FlavorIndependent AC applied per ResourceFlavor",
			cq:       cqWithACPerFlavor,
			cqStatus: pending,
			admissionChecks: map[string]AdmissionCheck{
				"check1": {
					Active:            true,
					Controller:        "controller1",
					FlavorIndependent: true,
				},
			},
			wantStatus:  active,
			wantReason:  "Ready",
			wantMessage: "Can admit new workloads",
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
			wantStatus:  terminating,
			wantReason:  "Terminating",
			wantMessage: "Can't admit new workloads; clusterQueue is terminating",
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
			wantStatus:  terminating,
			wantReason:  "Terminating",
			wantMessage: "Can't admit new workloads; clusterQueue is terminating",
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
			wantStatus:  terminating,
			wantReason:  "Terminating",
			wantMessage: "Can't admit new workloads; clusterQueue is terminating",
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
			wantStatus:  terminating,
			wantReason:  "Terminating",
			wantMessage: "Can't admit new workloads; clusterQueue is terminating",
		},
		{
			name:     "Active clusterQueue with an AC strategy updated with AdmissionCheckValidationRules disabled and no MultiKueue",
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
			wantStatus:  active,
			wantReason:  "Ready",
			wantMessage: "Can admit new workloads",
		},
		{
			name:     "Active clusterQueue with a FlavorIndependent AC applied per ResourceFlavor - AdmissionCheckValidationRules enabled",
			cq:       cqWithACPerFlavor,
			cqStatus: pending,
			admissionChecks: map[string]AdmissionCheck{
				"check1": {
					Active:            true,
					Controller:        "controller1",
					FlavorIndependent: true,
				},
			},
			wantStatus:               pending,
			wantReason:               "FlavorIndependentAdmissionCheckAppliedPerFlavor",
			wantMessage:              "Can't admit new workloads: AdmissionCheck(s): [check1] cannot be set at flavor level.",
			acValidationRulesEnabled: true,
		},
		{
			name:     "Active clusterQueue with a FlavorIndependent MultiKueue AC applied per ResourceFlavor",
			cq:       cqWithACPerFlavor,
			cqStatus: pending,
			admissionChecks: map[string]AdmissionCheck{
				"check1": {
					Active:            true,
					Controller:        kueue.MultiKueueControllerName,
					FlavorIndependent: true,
				},
			},
			wantStatus:  pending,
			wantReason:  "MultiKueueAdmissionCheckAppliedPerFlavor",
			wantMessage: `Can't admit new workloads: Cannot specify MultiKueue AdmissionCheck per flavor, found: check1.`,
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.acValidationRulesEnabled {
				features.SetFeatureGateDuringTest(t, features.AdmissionCheckValidationRules, true)
			}
			cache := New(utiltesting.NewFakeClient())
			cq, err := cache.newClusterQueue(tc.cq)
			if err != nil {
				t.Fatalf("failed to new clusterQueue %v", err)
			}

			cq.Status = tc.cqStatus

			// Align the admission check related internals to the desired Status.
			if tc.cqStatus == active {
				cq.multipleSingleInstanceControllersChecks = nil
				cq.missingAdmissionChecks = nil
				cq.inactiveAdmissionChecks = nil
				cq.flavorIndependentAdmissionCheckAppliedPerFlavor = nil
			} else {
				cq.missingAdmissionChecks = []string{"missing-ac"}
				cq.inactiveAdmissionChecks = []string{"inactive-ac"}
				// can only be cleaned up when feature gate is enabled
				if tc.acValidationRulesEnabled {
					cq.multipleSingleInstanceControllersChecks = map[string][]string{"c1": {"ac1", "ac2"}}
					cq.flavorIndependentAdmissionCheckAppliedPerFlavor = []string{"not-on-flavor"}
				}
			}
			cq.updateWithAdmissionChecks(tc.admissionChecks)

			if cq.Status != tc.wantStatus {
				t.Errorf("got different status, want: %v, got: %v", tc.wantStatus, cq.Status)
			}

			gotReason, gotMessage := cq.inactiveReason()
			if diff := cmp.Diff(tc.wantReason, gotReason); diff != "" {
				t.Errorf("Unexpected inactiveReason (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantMessage, gotMessage); diff != "" {
				t.Errorf("Unexpected inactiveMessage (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestDominantResourceShare(t *testing.T) {
	cases := map[string]struct {
		usage               resources.FlavorResourceQuantities
		clusterQueue        *kueue.ClusterQueue
		lendingClusterQueue *kueue.ClusterQueue
		flvResQ             resources.FlavorResourceQuantities
		wantDRValue         int
		wantDRName          corev1.ResourceName
	}{
		"no cohort": {
			usage: resources.FlavorResourceQuantities{
				{Flavor: "default", Resource: corev1.ResourceCPU}: 1_000,
				{Flavor: "default", Resource: "example.com/gpu"}:  2,
			},
			clusterQueue: utiltesting.MakeClusterQueue("cq").
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("cpu").NominalQuota("2000").Append().
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						FlavorQuotas,
				).Obj(),
		},
		"usage below nominal": {
			usage: resources.FlavorResourceQuantities{
				{Flavor: "default", Resource: corev1.ResourceCPU}: 1_000,
				{Flavor: "default", Resource: "example.com/gpu"}:  2,
			},
			clusterQueue: utiltesting.MakeClusterQueue("cq").
				Cohort("test-cohort").
				FairWeight(oneQuantity).
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("cpu").NominalQuota("2").Append().
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						FlavorQuotas,
				).Obj(),
			lendingClusterQueue: utiltesting.MakeClusterQueue("lending-cq").
				Cohort("test-cohort").
				FairWeight(oneQuantity).
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("cpu").NominalQuota("8").Append().
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						FlavorQuotas,
				).Obj(),
		},
		"usage above nominal": {
			usage: resources.FlavorResourceQuantities{
				{Flavor: "default", Resource: corev1.ResourceCPU}: 3_000,
				{Flavor: "default", Resource: "example.com/gpu"}:  7,
			},
			clusterQueue: utiltesting.MakeClusterQueue("cq").
				Cohort("test-cohort").
				FairWeight(oneQuantity).
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("cpu").NominalQuota("2").Append().
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						FlavorQuotas,
				).Obj(),
			lendingClusterQueue: utiltesting.MakeClusterQueue("lending-cq").
				Cohort("test-cohort").
				FairWeight(oneQuantity).
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("cpu").NominalQuota("8").Append().
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						FlavorQuotas,
				).Obj(),
			wantDRName:  "example.com/gpu",
			wantDRValue: 200, // (7-5)*1000/10
		},
		"one resource above nominal": {
			usage: resources.FlavorResourceQuantities{
				{Flavor: "default", Resource: corev1.ResourceCPU}: 3_000,
				{Flavor: "default", Resource: "example.com/gpu"}:  3,
			},
			clusterQueue: utiltesting.MakeClusterQueue("cq").
				Cohort("test-cohort").
				FairWeight(oneQuantity).
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("cpu").NominalQuota("2").Append().
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						FlavorQuotas,
				).Obj(),
			lendingClusterQueue: utiltesting.MakeClusterQueue("lending-cq").
				Cohort("test-cohort").
				FairWeight(oneQuantity).
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("cpu").NominalQuota("8").Append().
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						FlavorQuotas,
				).Obj(),
			wantDRName:  corev1.ResourceCPU,
			wantDRValue: 100, // (3-2)*1000/10
		},
		"usage with workload above nominal": {
			usage: resources.FlavorResourceQuantities{
				{Flavor: "default", Resource: corev1.ResourceCPU}: 1_000,
				{Flavor: "default", Resource: "example.com/gpu"}:  2,
			},
			clusterQueue: utiltesting.MakeClusterQueue("cq").
				Cohort("test-cohort").
				FairWeight(oneQuantity).
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("cpu").NominalQuota("2").Append().
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						FlavorQuotas,
				).Obj(),
			lendingClusterQueue: utiltesting.MakeClusterQueue("lending-cq").
				Cohort("test-cohort").
				FairWeight(oneQuantity).
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("cpu").NominalQuota("8").Append().
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						FlavorQuotas,
				).Obj(),
			flvResQ: resources.FlavorResourceQuantities{
				{Flavor: "default", Resource: corev1.ResourceCPU}: 4_000,
				{Flavor: "default", Resource: "example.com/gpu"}:  4,
			},
			wantDRName:  corev1.ResourceCPU,
			wantDRValue: 300, // (1+4-2)*1000/10
		},
		"A resource with zero lendable": {
			usage: resources.FlavorResourceQuantities{
				{Flavor: "default", Resource: corev1.ResourceCPU}: 1_000,
				{Flavor: "default", Resource: "example.com/gpu"}:  1,
			},
			clusterQueue: utiltesting.MakeClusterQueue("cq").
				Cohort("test-cohort").
				FairWeight(oneQuantity).
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("cpu").NominalQuota("2").Append().
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("2").LendingLimit("0").Append().
						FlavorQuotas,
				).Obj(),
			lendingClusterQueue: utiltesting.MakeClusterQueue("lending-cq").
				Cohort("test-cohort").
				FairWeight(oneQuantity).
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("cpu").NominalQuota("8").Append().
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("64").LendingLimit("0").Append().
						FlavorQuotas,
				).Obj(),
			flvResQ: resources.FlavorResourceQuantities{
				{Flavor: "default", Resource: corev1.ResourceCPU}: 4_000,
				{Flavor: "default", Resource: "example.com/gpu"}:  4,
			},
			wantDRName:  corev1.ResourceCPU,
			wantDRValue: 300, // (1+4-2)*1000/10
		},
		"multiple flavors": {
			usage: resources.FlavorResourceQuantities{
				{Flavor: "on-demand", Resource: corev1.ResourceCPU}: 15_000,
				{Flavor: "spot", Resource: corev1.ResourceCPU}:      5_000,
			},
			clusterQueue: utiltesting.MakeClusterQueue("cq").
				Cohort("test-cohort").
				FairWeight(oneQuantity).
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("on-demand").
						ResourceQuotaWrapper("cpu").NominalQuota("20").Append().
						FlavorQuotas,
					utiltesting.MakeFlavorQuotas("spot").
						ResourceQuotaWrapper("cpu").NominalQuota("80").Append().
						FlavorQuotas,
				).Obj(),
			lendingClusterQueue: utiltesting.MakeClusterQueue("lending-cq").
				Cohort("test-cohort").
				FairWeight(oneQuantity).
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("cpu").NominalQuota("100").Append().
						FlavorQuotas,
				).Obj(),
			flvResQ: resources.FlavorResourceQuantities{
				{Flavor: "on-demand", Resource: corev1.ResourceCPU}: 10_000,
			},
			wantDRName:  corev1.ResourceCPU,
			wantDRValue: 25, // ((15+10-20)+0)*1000/200 (spot under nominal)
		},
		"above nominal with integer weight": {
			usage: resources.FlavorResourceQuantities{
				{Flavor: "default", Resource: "example.com/gpu"}: 7,
			},
			clusterQueue: utiltesting.MakeClusterQueue("cq").
				Cohort("test-cohort").
				FairWeight(resource.MustParse("2")).
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						FlavorQuotas,
				).Obj(),
			lendingClusterQueue: utiltesting.MakeClusterQueue("lending-cq").
				Cohort("test-cohort").
				FairWeight(oneQuantity).
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						FlavorQuotas,
				).Obj(),
			wantDRName:  "example.com/gpu",
			wantDRValue: 100, // ((7-5)*1000/10)/2
		},
		"above nominal with decimal weight": {
			usage: resources.FlavorResourceQuantities{
				{Flavor: "default", Resource: "example.com/gpu"}: 7,
			},
			clusterQueue: utiltesting.MakeClusterQueue("cq").
				Cohort("test-cohort").
				FairWeight(resource.MustParse("0.5")).
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						FlavorQuotas,
				).Obj(),
			lendingClusterQueue: utiltesting.MakeClusterQueue("lending-cq").
				Cohort("test-cohort").
				FairWeight(oneQuantity).
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						FlavorQuotas,
				).Obj(),
			wantDRName:  "example.com/gpu",
			wantDRValue: 400, // ((7-5)*1000/10)/(1/2)
		},
		"above nominal with zero weight": {
			usage: resources.FlavorResourceQuantities{
				{Flavor: "default", Resource: "example.com/gpu"}: 7,
			},
			clusterQueue: utiltesting.MakeClusterQueue("cq").
				Cohort("test-cohort").
				FairWeight(oneQuantity).
				FairWeight(resource.MustParse("0")).
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						FlavorQuotas,
				).Obj(),
			lendingClusterQueue: utiltesting.MakeClusterQueue("lending-cq").
				Cohort("test-cohort").
				FairWeight(oneQuantity).
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("10").Append().
						FlavorQuotas,
				).Obj(),
			wantDRValue: math.MaxInt,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			cache := New(utiltesting.NewFakeClient())
			cache.AddOrUpdateResourceFlavor(utiltesting.MakeResourceFlavor("default").Obj())
			cache.AddOrUpdateResourceFlavor(utiltesting.MakeResourceFlavor("on-demand").Obj())
			cache.AddOrUpdateResourceFlavor(utiltesting.MakeResourceFlavor("spot").Obj())

			_ = cache.AddClusterQueue(ctx, tc.clusterQueue)

			if tc.lendingClusterQueue != nil {
				// we create a second cluster queue to add lendable capacity to the cohort.
				_ = cache.AddClusterQueue(ctx, tc.lendingClusterQueue)
			}

			snapshot, err := cache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("unexpected error while building snapshot: %v", err)
			}
			i := 0
			for fr, v := range tc.usage {
				admission := utiltesting.MakeAdmission("cq")
				quantity := resources.ResourceQuantity(fr.Resource, v)
				admission.Assignment(fr.Resource, fr.Flavor, quantity.String())

				wl := utiltesting.MakeWorkload(fmt.Sprintf("workload-%d", i), "default-namespace").ReserveQuota(admission.Obj()).Obj()

				cache.AddOrUpdateWorkload(wl)
				snapshot.AddWorkload(workload.NewInfo(wl))
				i += 1
			}

			drVal, drNameCache := dominantResourceShare(cache.hm.ClusterQueues["cq"], tc.flvResQ, 1)
			if drVal != tc.wantDRValue {
				t.Errorf("cache.DominantResourceShare(_) returned value %d, want %d", drVal, tc.wantDRValue)
			}
			if drNameCache != tc.wantDRName {
				t.Errorf("cache.DominantResourceShare(_) returned resource %s, want %s", drNameCache, tc.wantDRName)
			}

			drValSnap, drNameSnap := snapshot.ClusterQueues["cq"].DominantResourceShareWith(tc.flvResQ)
			if drValSnap != tc.wantDRValue {
				t.Errorf("snapshot.DominantResourceShare(_) returned value %d, want %d", drValSnap, tc.wantDRValue)
			}
			if drNameSnap != tc.wantDRName {
				t.Errorf("snapshot.DominantResourceShare(_) returned resource %s, want %s", drNameSnap, tc.wantDRName)
			}
		})
	}
}

func TestCohortLendable(t *testing.T) {
	cache := New(utiltesting.NewFakeClient())

	cq1 := utiltesting.MakeClusterQueue("cq1").
		ResourceGroup(
			utiltesting.MakeFlavorQuotas("default").
				ResourceQuotaWrapper("cpu").NominalQuota("8").LendingLimit("8").Append().
				ResourceQuotaWrapper("example.com/gpu").NominalQuota("3").LendingLimit("3").Append().
				FlavorQuotas,
		).Cohort("test-cohort").
		ClusterQueue

	cq2 := utiltesting.MakeClusterQueue("cq2").
		ResourceGroup(
			utiltesting.MakeFlavorQuotas("default").
				ResourceQuotaWrapper("cpu").NominalQuota("2").LendingLimit("2").Append().
				FlavorQuotas,
		).Cohort("test-cohort").
		ClusterQueue

	if err := cache.AddClusterQueue(context.Background(), &cq1); err != nil {
		t.Fatal("Failed to add CQ to cache", err)
	}
	if err := cache.AddClusterQueue(context.Background(), &cq2); err != nil {
		t.Fatal("Failed to add CQ to cache", err)
	}

	wantLendable := map[corev1.ResourceName]int64{
		corev1.ResourceCPU: 10_000,
		"example.com/gpu":  3,
	}

	lendable := cache.hm.Cohorts["test-cohort"].resourceNode.calculateLendable()
	if diff := cmp.Diff(wantLendable, lendable); diff != "" {
		t.Errorf("Unexpected cohort lendable (-want,+got):\n%s", diff)
	}
}

func TestClusterQueueReadinessWithTAS(t *testing.T) {
	cases := []struct {
		name        string
		cq          *kueue.ClusterQueue
		updatedCq   *kueue.ClusterQueue
		wantStatus  metrics.ClusterQueueStatus
		wantReason  string
		wantMessage string
	}{
		{
			name: "TAS CQ goes active state",
			cq: utiltesting.MakeClusterQueue("cq").
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("tas-flavor").
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						FlavorQuotas,
				).Obj(),
			wantReason:  "Ready",
			wantMessage: "Can admit new workloads",
		},
		{
			name: "TAS do not support Cohorts",
			cq: utiltesting.MakeClusterQueue("cq").
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("tas-flavor").
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						FlavorQuotas,
				).Obj(),
			updatedCq: utiltesting.MakeClusterQueue("cq").
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("tas-flavor").
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						FlavorQuotas,
				).Cohort("some-cohort").Obj(),
			wantReason:  kueue.ClusterQueueActiveReasonNotSupportedWithTopologyAwareScheduling,
			wantMessage: "Can't admit new workloads: TAS is not supported for cohorts.",
		},
		{
			name: "TAS do not support Preemption",
			cq: utiltesting.MakeClusterQueue("cq").
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("tas-flavor").
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						FlavorQuotas,
				).Obj(),
			updatedCq: utiltesting.MakeClusterQueue("cq").
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("tas-flavor").
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						FlavorQuotas,
				).
				Preemption(kueue.ClusterQueuePreemption{
					WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
				}).
				FlavorFungibility(kueue.FlavorFungibility{
					WhenCanPreempt: kueue.Preempt,
				}).
				Obj(),
			wantReason:  kueue.ClusterQueueActiveReasonNotSupportedWithTopologyAwareScheduling,
			wantMessage: "Can't admit new workloads: TAS is not supported for preemption within cluster queue.",
		},
		{
			name: "TAS do not support MultiKueue AdmissionCheck",
			cq: utiltesting.MakeClusterQueue("cq").
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("tas-flavor").
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						FlavorQuotas,
				).Obj(),
			updatedCq: utiltesting.MakeClusterQueue("cq").
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("tas-flavor").
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						FlavorQuotas,
				).AdmissionChecks("mk-check").Obj(),
			wantReason:  kueue.ClusterQueueActiveReasonNotSupportedWithTopologyAwareScheduling,
			wantMessage: "Can't admit new workloads: TAS is not supported with MultiKueue admission check.",
		},
		{
			name: "TAS do not support ProvisioningRequest AdmissionCheck",
			cq: utiltesting.MakeClusterQueue("cq").
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("tas-flavor").
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						FlavorQuotas,
				).Obj(),
			updatedCq: utiltesting.MakeClusterQueue("cq").
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("tas-flavor").
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						FlavorQuotas,
				).AdmissionChecks("pr-check").Obj(),
			wantReason:  kueue.ClusterQueueActiveReasonNotSupportedWithTopologyAwareScheduling,
			wantMessage: "Can't admit new workloads: TAS is not supported with ProvisioningRequest admission check.",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.TopologyAwareScheduling, true)

			ctx, _ := utiltesting.ContextWithLog(t)
			cqCache := New(utiltesting.NewFakeClient())

			topology := utiltesting.MakeTopology("example-topology").Levels([]string{"tas-level-0"}).Obj()

			rf := utiltesting.MakeResourceFlavor("tas-flavor").TopologyName(topology.Name).Obj()
			cqCache.AddOrUpdateResourceFlavor(rf)

			mkAC := utiltesting.MakeAdmissionCheck("mk-check").ControllerName(kueue.MultiKueueControllerName).Active(metav1.ConditionTrue).Obj()
			cqCache.AddOrUpdateAdmissionCheck(mkAC)

			acWithPR := utiltesting.MakeAdmissionCheck("pr-check").ControllerName(kueue.ProvisioningRequestControllerName).Active(metav1.ConditionTrue).Obj()
			cqCache.AddOrUpdateAdmissionCheck(acWithPR)

			if err := cqCache.AddClusterQueue(ctx, tc.cq); err != nil {
				t.Fatalf("Inserting clusterQueue %s in cache: %v", tc.cq.Name, err)
			}

			if tc.updatedCq != nil {
				if err := cqCache.UpdateClusterQueue(tc.updatedCq); err != nil {
					t.Fatalf("Updating clusterQueue %s in cache: %v", tc.updatedCq.Name, err)
				}
			}

			_, err := cqCache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("unexpected error while building snapshot: %v", err)
			}

			_, gotReason, gotMessage := cqCache.ClusterQueueReadiness(tc.cq.Name)
			if diff := cmp.Diff(tc.wantReason, gotReason); diff != "" {
				t.Errorf("Unexpected inactiveReason (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantMessage, gotMessage); diff != "" {
				t.Errorf("Unexpected inactiveMessage (-want,+got):\n%s", diff)
			}
		})
	}
}
