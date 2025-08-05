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

package cache

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	tasindexer "sigs.k8s.io/kueue/pkg/controller/tas/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
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
			_, log := utiltesting.ContextWithLog(t)
			cache := New(utiltesting.NewFakeClient())
			cq, err := cache.newClusterQueue(log, cq)
			if err != nil {
				t.Fatalf("failed to new clusterQueue %v", err)
			}

			cq.Status = tc.curStatus
			cq.UpdateWithFlavors(log, tc.flavors)

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
			ctx, log := utiltesting.ContextWithLog(t)
			clientBuilder := utiltesting.NewClientBuilder().
				WithObjects(
					utiltesting.MakeNamespace("default"),
					tc.cq,
				)
			cl := clientBuilder.Build()
			cqCache := New(cl)
			// Workloads are loaded into queues or clusterQueues as we add them.
			for _, rf := range resourceFlavors {
				cqCache.AddOrUpdateResourceFlavor(log, rf)
			}
			if err := cqCache.AddClusterQueue(ctx, tc.cq); err != nil {
				t.Fatalf("Inserting clusterQueue %s in cache: %v", tc.cq.Name, err)
			}
			if err := cqCache.UpdateClusterQueue(log, tc.newcq); err != nil {
				t.Fatalf("Updating clusterQueue %s in cache: %v", tc.newcq.Name, err)
			}
			snapshot, err := cqCache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("unexpected error while building snapshot: %v", err)
			}
			if diff := cmp.Diff(
				tc.wantLastAssignmentGeneration,
				snapshot.ClusterQueue("eng-alpha").AllocatableResourceGeneration); diff != "" {
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

	testcases := []struct {
		name            string
		cq              *kueue.ClusterQueue
		cqStatus        metrics.ClusterQueueStatus
		admissionChecks map[kueue.AdmissionCheckReference]AdmissionCheck
		wantStatus      metrics.ClusterQueueStatus
		wantReason      string
		wantMessage     string
	}{
		{
			name:     "Pending clusterQueue updated valid AC list",
			cq:       cqWithAC,
			cqStatus: pending,
			admissionChecks: map[kueue.AdmissionCheckReference]AdmissionCheck{
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
			name:     "Pending clusterQueue with an AC strategy updated valid AC list",
			cq:       cqWithACStrategy,
			cqStatus: pending,
			admissionChecks: map[kueue.AdmissionCheckReference]AdmissionCheck{
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
			admissionChecks: map[kueue.AdmissionCheckReference]AdmissionCheck{
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
			wantMessage: "Can't admit new workloads: references missing AdmissionCheck(s): check3.",
		},
		{
			name:     "Active clusterQueue with an AC strategy updated with not found AC",
			cq:       cqWithACStrategy,
			cqStatus: active,
			admissionChecks: map[kueue.AdmissionCheckReference]AdmissionCheck{
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
			wantMessage: "Can't admit new workloads: references missing AdmissionCheck(s): check3.",
		},
		{
			name:     "Active clusterQueue updated with inactive AC",
			cq:       cqWithAC,
			cqStatus: active,
			admissionChecks: map[kueue.AdmissionCheckReference]AdmissionCheck{
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
			wantMessage: "Can't admit new workloads: references inactive AdmissionCheck(s): check3.",
		},
		{
			name:     "Active clusterQueue with an AC strategy updated with inactive AC",
			cq:       cqWithACStrategy,
			cqStatus: active,
			admissionChecks: map[kueue.AdmissionCheckReference]AdmissionCheck{
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
			wantMessage: "Can't admit new workloads: references inactive AdmissionCheck(s): check3.",
		},
		{
			name:     "Active clusterQueue with an MultiKueue AC strategy updated with duplicate single instance AC Controller",
			cq:       cqWithACStrategy,
			cqStatus: active,
			admissionChecks: map[kueue.AdmissionCheckReference]AdmissionCheck{
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
			name:     "Terminating clusterQueue updated with valid AC list",
			cq:       cqWithAC,
			cqStatus: terminating,
			admissionChecks: map[kueue.AdmissionCheckReference]AdmissionCheck{
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
			admissionChecks: map[kueue.AdmissionCheckReference]AdmissionCheck{
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
			admissionChecks: map[kueue.AdmissionCheckReference]AdmissionCheck{
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
			admissionChecks: map[kueue.AdmissionCheckReference]AdmissionCheck{
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
			name:     "Active clusterQueue with an AC strategy updated",
			cq:       cqWithACStrategy,
			cqStatus: active,
			admissionChecks: map[kueue.AdmissionCheckReference]AdmissionCheck{
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
					Controller: "controller2",
				},
			},
			wantStatus:  active,
			wantReason:  "Ready",
			wantMessage: "Can admit new workloads",
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			_, log := utiltesting.ContextWithLog(t)
			cache := New(utiltesting.NewFakeClient())
			cq, err := cache.newClusterQueue(log, tc.cq)
			if err != nil {
				t.Fatalf("failed to new clusterQueue %v", err)
			}

			cq.Status = tc.cqStatus

			// Align the admission check related internals to the desired Status.
			if tc.cqStatus == active {
				cq.missingAdmissionChecks = nil
				cq.inactiveAdmissionChecks = nil
			} else {
				cq.missingAdmissionChecks = []kueue.AdmissionCheckReference{"missing-ac"}
				cq.inactiveAdmissionChecks = []kueue.AdmissionCheckReference{"inactive-ac"}
			}
			cq.updateWithAdmissionChecks(log, tc.admissionChecks)

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

func TestClusterQueueReadinessWithTAS(t *testing.T) {
	cases := []struct {
		name         string
		skipTopology bool
		cq           *kueue.ClusterQueue
		updatedCq    *kueue.ClusterQueue
		wantStatus   metrics.ClusterQueueStatus
		wantReason   string
		wantMessage  string
	}{
		{
			name: "TAS CQ goes active state",
			cq: utiltesting.MakeClusterQueue("cq").
				ResourceGroup(
					*utiltesting.MakeFlavorQuotas("tas-flavor").
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						Obj(),
				).Obj(),
			wantReason:  "Ready",
			wantMessage: "Can admit new workloads",
		},
		{
			name: "TAS do not support Cohorts",
			cq: utiltesting.MakeClusterQueue("cq").
				ResourceGroup(
					*utiltesting.MakeFlavorQuotas("tas-flavor").
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						Obj(),
				).Obj(),
			updatedCq: utiltesting.MakeClusterQueue("cq").
				ResourceGroup(
					*utiltesting.MakeFlavorQuotas("tas-flavor").
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						Obj(),
				).Cohort("some-cohort").Obj(),
			wantReason:  kueue.ClusterQueueActiveReasonReady,
			wantMessage: "Can admit new workloads",
		},
		{
			name: "TAS do not support Preemption",
			cq: utiltesting.MakeClusterQueue("cq").
				ResourceGroup(
					*utiltesting.MakeFlavorQuotas("tas-flavor").
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						Obj(),
				).Obj(),
			updatedCq: utiltesting.MakeClusterQueue("cq").
				ResourceGroup(
					*utiltesting.MakeFlavorQuotas("tas-flavor").
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						Obj(),
				).
				Preemption(kueue.ClusterQueuePreemption{
					WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
				}).
				FlavorFungibility(kueue.FlavorFungibility{
					WhenCanPreempt: kueue.Preempt,
				}).
				Obj(),
			wantReason:  kueue.ClusterQueueActiveReasonReady,
			wantMessage: "Can admit new workloads",
		},
		{
			name: "TAS do not support MultiKueue AdmissionCheck",
			cq: utiltesting.MakeClusterQueue("cq").
				ResourceGroup(
					*utiltesting.MakeFlavorQuotas("tas-flavor").
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						Obj(),
				).Obj(),
			updatedCq: utiltesting.MakeClusterQueue("cq").
				ResourceGroup(
					*utiltesting.MakeFlavorQuotas("tas-flavor").
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						Obj(),
				).AdmissionChecks("mk-check").Obj(),
			wantReason:  kueue.ClusterQueueActiveReasonNotSupportedWithTopologyAwareScheduling,
			wantMessage: "Can't admit new workloads: TAS is not supported with MultiKueue admission check.",
		},
		{
			name:         "Referenced TAS flavor without topology",
			skipTopology: true,
			cq: utiltesting.MakeClusterQueue("cq").
				ResourceGroup(
					*utiltesting.MakeFlavorQuotas("tas-flavor").
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						Obj(),
				).Obj(),
			wantReason:  kueue.ClusterQueueActiveReasonTopologyNotFound,
			wantMessage: `Can't admit new workloads: there is no Topology "example-topology" for TAS flavor "tas-flavor".`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.TopologyAwareScheduling, true)

			ctx, log := utiltesting.ContextWithLog(t)

			clientBuilder := utiltesting.NewClientBuilder()
			_ = tasindexer.SetupIndexes(ctx, utiltesting.AsIndexer(clientBuilder))
			client := clientBuilder.Build()

			cqCache := New(client)

			topology := utiltesting.MakeTopology("example-topology").Levels("tas-level-0").Obj()

			rf := utiltesting.MakeResourceFlavor("tas-flavor").TopologyName(topology.Name).Obj()
			cqCache.AddOrUpdateResourceFlavor(log, rf)

			if !tc.skipTopology {
				cqCache.AddOrUpdateTopology(log, topology)
			}

			mkAC := utiltesting.MakeAdmissionCheck("mk-check").ControllerName(kueue.MultiKueueControllerName).Active(metav1.ConditionTrue).Obj()
			cqCache.AddOrUpdateAdmissionCheck(log, mkAC)

			acWithPR := utiltesting.MakeAdmissionCheck("pr-check").ControllerName(kueue.ProvisioningRequestControllerName).Active(metav1.ConditionTrue).Obj()
			cqCache.AddOrUpdateAdmissionCheck(log, acWithPR)

			if err := cqCache.AddClusterQueue(ctx, tc.cq); err != nil {
				t.Fatalf("Inserting clusterQueue %s in cache: %v", tc.cq.Name, err)
			}

			if tc.updatedCq != nil {
				if err := cqCache.UpdateClusterQueue(log, tc.updatedCq); err != nil {
					t.Fatalf("Updating clusterQueue %s in cache: %v", tc.updatedCq.Name, err)
				}
			}

			_, err := cqCache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("unexpected error while building snapshot: %v", err)
			}

			_, gotReason, gotMessage := cqCache.ClusterQueueReadiness(kueue.ClusterQueueReference(tc.cq.Name))
			if diff := cmp.Diff(tc.wantReason, gotReason); diff != "" {
				t.Errorf("Unexpected inactiveReason (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantMessage, gotMessage); diff != "" {
				t.Errorf("Unexpected inactiveMessage (-want,+got):\n%s", diff)
			}
		})
	}
}
