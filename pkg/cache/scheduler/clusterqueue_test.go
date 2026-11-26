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

package scheduler

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/component-base/featuregate"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	tasindexer "sigs.k8s.io/kueue/pkg/controller/tas/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/resources"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingmetrics "sigs.k8s.io/kueue/pkg/util/testing/metrics"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestClusterQueueUpdateWithFlavors(t *testing.T) {
	rf := utiltestingapi.MakeResourceFlavor("x86").Obj()
	cq := utiltestingapi.MakeClusterQueue("cq").
		ResourceGroup(*utiltestingapi.MakeFlavorQuotas("x86").Resource("cpu", "5").Obj()).
		Obj()

	testCases := map[string]struct {
		curStatus  metrics.ClusterQueueStatus
		flavors    map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor
		wantStatus metrics.ClusterQueueStatus
	}{
		"Pending clusterQueue updated existent flavors": {
			curStatus: pending,
			flavors: map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor{
				kueue.ResourceFlavorReference(rf.Name): rf,
			},
			wantStatus: active,
		},
		"Active clusterQueue updated with not found flavors": {
			curStatus:  active,
			flavors:    map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor{},
			wantStatus: pending,
		},
		"Terminating clusterQueue updated with existent flavors": {
			curStatus: terminating,
			flavors: map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor{
				kueue.ResourceFlavorReference(rf.Name): rf,
			},
			wantStatus: terminating,
		},
		"Terminating clusterQueue updated with not found flavors": {
			curStatus:  terminating,
			wantStatus: terminating,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
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
		utiltestingapi.MakeResourceFlavor("on-demand").Obj(),
		utiltestingapi.MakeResourceFlavor("spot").Obj(),
	}
	clusterQueue :=
		*utiltestingapi.MakeClusterQueue("eng-alpha").
			QueueingStrategy(kueue.StrictFIFO).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
			}).
			FlavorFungibility(kueue.FlavorFungibility{
				WhenCanPreempt: kueue.MayStopSearch,
			}).
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("on-demand").
					Resource(corev1.ResourceCPU, "50", "50").Obj(),
				*utiltestingapi.MakeFlavorQuotas("spot").
					Resource(corev1.ResourceCPU, "100", "0").Obj(),
			).Obj()
	newClusterQueue :=
		utiltestingapi.MakeClusterQueue("eng-alpha").
			QueueingStrategy(kueue.StrictFIFO).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
			}).
			FlavorFungibility(kueue.FlavorFungibility{
				WhenCanPreempt: kueue.MayStopSearch,
			}).
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("on-demand").
					Resource(corev1.ResourceCPU, "100", "50").Obj(),
				*utiltestingapi.MakeFlavorQuotas("spot").
					Resource(corev1.ResourceCPU, "100", "0").Obj(),
			).Obj()
	cases := map[string]struct {
		cq                                *kueue.ClusterQueue
		newcq                             *kueue.ClusterQueue
		wantAllocatableResourceGeneration int64
	}{
		"RGs not change": {
			cq:                                &clusterQueue,
			newcq:                             clusterQueue.DeepCopy(),
			wantAllocatableResourceGeneration: 1,
		},
		"RGs changed": {
			cq:                                &clusterQueue,
			newcq:                             newClusterQueue,
			wantAllocatableResourceGeneration: 2,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
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
				tc.wantAllocatableResourceGeneration,
				snapshot.ClusterQueue("eng-alpha").AllocatableResourceGeneration); diff != "" {
				t.Errorf("Unexpected assigned clusterQueues in cache (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestClusterQueueUpdateWithAdmissionCheck(t *testing.T) {
	cqWithAC := utiltestingapi.MakeClusterQueue("cq").
		AdmissionChecks("check1", "check2", "check3").
		Obj()

	cqWithACStrategy := utiltestingapi.MakeClusterQueue("cq2").
		AdmissionCheckStrategy(
			*utiltestingapi.MakeAdmissionCheckStrategyRule("check1").Obj(),
			*utiltestingapi.MakeAdmissionCheckStrategyRule("check2").Obj(),
			*utiltestingapi.MakeAdmissionCheckStrategyRule("check3").Obj()).
		Obj()

	testCases := map[string]struct {
		cq              *kueue.ClusterQueue
		cqStatus        metrics.ClusterQueueStatus
		admissionChecks map[kueue.AdmissionCheckReference]AdmissionCheck
		wantStatus      metrics.ClusterQueueStatus
		wantReason      string
		wantMessage     string
	}{
		"Pending clusterQueue updated valid AC list": {
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
		"Pending clusterQueue with an AC strategy updated valid AC list": {
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
		"Active clusterQueue updated with not found AC": {
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
		"Active clusterQueue with an AC strategy updated with not found AC": {
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
		"Active clusterQueue updated with inactive AC": {
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
		"Active clusterQueue with an AC strategy updated with inactive AC": {
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
		"Active clusterQueue with an MultiKueue AC strategy updated with duplicate single instance AC Controller": {
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
		"Active clusterQueue with both MultiKueue and ProvisioningRequest ACs": {
			cq:       cqWithAC,
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
					Controller: kueue.ProvisioningRequestControllerName,
				},
			},
			wantStatus:  pending,
			wantReason:  kueue.ClusterQueueActiveReasonMultiKueueWithProvisioningRequest,
			wantMessage: `Can't admit new workloads: Cannot use both MultiKueue and ProvisioningRequest AdmissionChecks together, found: check1, check3.`,
		},
		"Active clusterQueue with both MultiKueue and CapacityBuffer ACs stays active": {
			cq:       cqWithAC,
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
					Controller: kueuealpha.CapacityBufferControllerName,
				},
			},
			wantStatus:  active,
			wantReason:  "Ready",
			wantMessage: "Can admit new workloads",
		},
		"Terminating clusterQueue updated with valid AC list": {
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
		"Terminating clusterQueue with an AC strategy updated with valid AC list": {
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
		"Terminating clusterQueue updated with not found AC": {
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
		"Terminating clusterQueue with an AC strategy updated with not found AC": {
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
		"Active clusterQueue with an AC strategy updated": {
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

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
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

func TestClusterQueueFlavorsWithProvReqAdmissionCheck(t *testing.T) {
	checks := map[kueue.AdmissionCheckReference]AdmissionCheck{
		"prov-check":   {Active: true, Controller: kueue.ProvisioningRequestControllerName},
		"buffer-check": {Active: true, Controller: kueuealpha.CapacityBufferControllerName},
		"custom-check": {Active: true, Controller: "custom-controller"},
	}
	resourceGroup := *utiltestingapi.MakeFlavorQuotas("tas").Resource(corev1.ResourceCPU, "10").Obj()
	otherFlavor := *utiltestingapi.MakeFlavorQuotas("other").Resource(corev1.ResourceCPU, "10").Obj()

	testCases := map[string]struct {
		rules []kueue.AdmissionCheckStrategyRule
		// disableCapacityBuffer turns off the CapacityBufferACC feature gate.
		disableCapacityBuffer bool
		// disableTAS turns off the TopologyAwareScheduling feature gate.
		disableTAS bool
		// disableCapacityBufferAfterUpdate turns off the CapacityBufferACC feature
		// gate after the ClusterQueue tracked its checks, so that only the gate
		// check of the topology delay can drop the CapacityBuffer flavors.
		disableCapacityBufferAfterUpdate bool
		// wantTracked are the CapacityBuffer checks the ClusterQueue tracks.
		wantTracked []kueue.AdmissionCheckReference
		want        sets.Set[kueue.ResourceFlavorReference]
	}{
		"CapacityBuffer check on all flavors": {
			rules: []kueue.AdmissionCheckStrategyRule{
				*utiltestingapi.MakeAdmissionCheckStrategyRule("buffer-check").Obj(),
			},
			wantTracked: []kueue.AdmissionCheckReference{"buffer-check"},
			want:        sets.New[kueue.ResourceFlavorReference]("tas", "other"),
		},
		"CapacityBuffer check on one flavor": {
			rules: []kueue.AdmissionCheckStrategyRule{
				*utiltestingapi.MakeAdmissionCheckStrategyRule("buffer-check", "tas").Obj(),
			},
			wantTracked: []kueue.AdmissionCheckReference{"buffer-check"},
			want:        sets.New[kueue.ResourceFlavorReference]("tas"),
		},
		"ProvisioningRequest and CapacityBuffer checks on different flavors": {
			rules: []kueue.AdmissionCheckStrategyRule{
				*utiltestingapi.MakeAdmissionCheckStrategyRule("prov-check", "other").Obj(),
				*utiltestingapi.MakeAdmissionCheckStrategyRule("buffer-check", "tas").Obj(),
			},
			wantTracked: []kueue.AdmissionCheckReference{"buffer-check"},
			want:        sets.New[kueue.ResourceFlavorReference]("tas", "other"),
		},
		"CapacityBuffer check does not delay the topology assignment when CapacityBufferACC is disabled": {
			rules: []kueue.AdmissionCheckStrategyRule{
				*utiltestingapi.MakeAdmissionCheckStrategyRule("buffer-check").Obj(),
			},
			disableCapacityBuffer: true,
			want:                  sets.New[kueue.ResourceFlavorReference](),
		},
		"ProvisioningRequest check delays the topology assignment when CapacityBufferACC is disabled": {
			rules: []kueue.AdmissionCheckStrategyRule{
				*utiltestingapi.MakeAdmissionCheckStrategyRule("prov-check", "other").Obj(),
				*utiltestingapi.MakeAdmissionCheckStrategyRule("buffer-check", "tas").Obj(),
			},
			disableCapacityBuffer: true,
			want:                  sets.New[kueue.ResourceFlavorReference]("other"),
		},
		"CapacityBuffer check does not delay the topology assignment when TopologyAwareScheduling is disabled": {
			rules: []kueue.AdmissionCheckStrategyRule{
				*utiltestingapi.MakeAdmissionCheckStrategyRule("prov-check", "other").Obj(),
				*utiltestingapi.MakeAdmissionCheckStrategyRule("buffer-check", "tas").Obj(),
			},
			disableTAS:  true,
			wantTracked: []kueue.AdmissionCheckReference{"buffer-check"},
			want:        sets.New[kueue.ResourceFlavorReference]("other"),
		},
		"CapacityBuffer check does not delay the topology assignment when CapacityBufferACC is disabled after the check was tracked": {
			rules: []kueue.AdmissionCheckStrategyRule{
				*utiltestingapi.MakeAdmissionCheckStrategyRule("buffer-check").Obj(),
			},
			disableCapacityBufferAfterUpdate: true,
			wantTracked:                      []kueue.AdmissionCheckReference{"buffer-check"},
			want:                             sets.New[kueue.ResourceFlavorReference](),
		},
		"custom check does not delay the topology assignment": {
			rules: []kueue.AdmissionCheckStrategyRule{
				*utiltestingapi.MakeAdmissionCheckStrategyRule("custom-check").Obj(),
			},
			want: sets.New[kueue.ResourceFlavorReference](),
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.CapacityBufferACC, !tc.disableCapacityBuffer)
			features.SetFeatureGateDuringTest(t, features.TopologyAwareScheduling, !tc.disableTAS)
			_, log := utiltesting.ContextWithLog(t)
			cache := New(utiltesting.NewFakeClient())
			cq, err := cache.newClusterQueue(log, utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(resourceGroup, otherFlavor).
				AdmissionCheckStrategy(tc.rules...).
				Obj())
			if err != nil {
				t.Fatalf("failed to create clusterQueue: %v", err)
			}
			cq.updateWithAdmissionChecks(log, checks)

			if diff := cmp.Diff(tc.wantTracked, cq.capacityBufferAdmissionChecks, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Unexpected tracked CapacityBuffer checks (-want,+got):\n%s", diff)
			}
			if tc.disableCapacityBufferAfterUpdate {
				features.SetFeatureGateDuringTest(t, features.CapacityBufferACC, false)
			}
			if diff := cmp.Diff(tc.want, cq.flavorsWithProvReqAdmissionCheck()); diff != "" {
				t.Errorf("Unexpected flavors (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestClusterQueueReadinessWithTAS(t *testing.T) {
	cases := map[string]struct {
		featureGates            map[featuregate.Feature]bool
		skipTopology            bool
		skipInitialTopologyName bool
		updatedFlavor           *kueue.ResourceFlavor
		cq                      *kueue.ClusterQueue
		updatedCq               *kueue.ClusterQueue
		wantStatus              metrics.ClusterQueueStatus
		wantReason              string
		wantMessage             string
	}{
		"TAS CQ goes active state": {
			cq: utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("tas-flavor").
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						Obj(),
				).Obj(),
			wantReason:  "Ready",
			wantMessage: "Can admit new workloads",
		},
		"TAS do not support Cohorts": {
			cq: utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("tas-flavor").
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						Obj(),
				).Obj(),
			updatedCq: utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("tas-flavor").
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						Obj(),
				).Cohort("some-cohort").Obj(),
			wantReason:  kueue.ClusterQueueActiveReasonReady,
			wantMessage: "Can admit new workloads",
		},
		"TAS do not support Preemption": {
			cq: utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("tas-flavor").
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						Obj(),
				).Obj(),
			updatedCq: utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("tas-flavor").
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						Obj(),
				).
				Preemption(kueue.ClusterQueuePreemption{
					WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
				}).
				FlavorFungibility(kueue.FlavorFungibility{
					WhenCanPreempt: kueue.MayStopSearch,
				}).
				Obj(),
			wantReason:  kueue.ClusterQueueActiveReasonReady,
			wantMessage: "Can admit new workloads",
		},
		"TAS supports MultiKueue AdmissionCheck": {
			cq: utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("tas-flavor").
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						Obj(),
				).Obj(),
			updatedCq: utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("tas-flavor").
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						Obj(),
				).AdmissionChecks("mk-check").Obj(),
			wantReason:  kueue.ClusterQueueActiveReasonReady,
			wantMessage: "Can admit new workloads",
		},
		"Referenced TAS flavor without topology": {
			skipTopology: true,
			cq: utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("tas-flavor").
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						Obj(),
				).Obj(),
			wantReason:  kueue.ClusterQueueActiveReasonTopologyNotFound,
			wantMessage: `Can't admit new workloads: there is no Topology "example-topology" for TAS flavor "tas-flavor".`,
		},
		"Multiple referenced TAS flavors without topology are listed by name": {
			skipTopology: true,
			cq: utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("tas-flavor-3").
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						Obj(),
					*utiltestingapi.MakeFlavorQuotas("tas-flavor-2").
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						Obj(),
					*utiltestingapi.MakeFlavorQuotas("tas-flavor").
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						Obj(),
				).Obj(),
			wantReason: kueue.ClusterQueueActiveReasonTopologyNotFound,
			wantMessage: `Can't admit new workloads: there is no Topology "example-topology" for TAS flavor "tas-flavor", ` +
				`there is no Topology "example-topology" for TAS flavor "tas-flavor-2", ` +
				`there is no Topology "example-topology" for TAS flavor "tas-flavor-3".`,
		},
		"Missing topology on TAS flavor is ignored when TAS is disabled": {
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: false},
			skipTopology: true,
			cq: utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("tas-flavor").
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						Obj(),
				).Obj(),
			wantReason:  kueue.ClusterQueueActiveReasonReady,
			wantMessage: "Can admit new workloads",
		},
		"Unsetting missing topologyName on TAS flavor restores Ready": {
			skipTopology:  true,
			updatedFlavor: utiltestingapi.MakeResourceFlavor("tas-flavor").Obj(),
			cq: utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("tas-flavor").
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						Obj(),
				).Obj(),
			wantReason:  kueue.ClusterQueueActiveReasonReady,
			wantMessage: "Can admit new workloads",
		},
		"Setting missing topologyName on non-TAS flavor marks CQ TopologyNotFound": {
			skipInitialTopologyName: true,
			skipTopology:            true,
			updatedFlavor:           utiltestingapi.MakeResourceFlavor("tas-flavor").TopologyName("example-topology").Obj(),
			cq: utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("tas-flavor").
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						Obj(),
				).Obj(),
			wantReason:  kueue.ClusterQueueActiveReasonTopologyNotFound,
			wantMessage: `Can't admit new workloads: there is no Topology "example-topology" for TAS flavor "tas-flavor".`,
		},
		"Changing flavor topologyName directly updates tracked topology": {
			updatedFlavor: utiltestingapi.MakeResourceFlavor("tas-flavor").TopologyName("missing-topology").Obj(),
			cq: utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("tas-flavor").
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						Obj(),
				).Obj(),
			wantReason:  kueue.ClusterQueueActiveReasonTopologyNotFound,
			wantMessage: `Can't admit new workloads: there is no Topology "missing-topology" for TAS flavor "tas-flavor".`,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, tc.featureGates)
			ctx, log := utiltesting.ContextWithLog(t)

			clientBuilder := utiltesting.NewClientBuilder()
			_ = tasindexer.SetupIndexes(ctx, utiltesting.AsIndexer(clientBuilder))
			client := clientBuilder.Build()

			cqCache := New(client)

			topology := utiltestingapi.MakeTopology("example-topology").Levels("tas-level-0").Obj()

			rfBuilder := utiltestingapi.MakeResourceFlavor("tas-flavor")
			if !tc.skipInitialTopologyName {
				rfBuilder = rfBuilder.TopologyName(topology.Name)
			}
			cqCache.AddOrUpdateResourceFlavor(log, rfBuilder.Obj())

			for _, name := range []string{"tas-flavor-2", "tas-flavor-3"} {
				cqCache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor(name).TopologyName(topology.Name).Obj())
			}

			if !tc.skipTopology {
				cqCache.AddOrUpdateTopology(log, topology)
			}

			mkAC := utiltestingapi.MakeAdmissionCheck("mk-check").ControllerName(kueue.MultiKueueControllerName).Active(metav1.ConditionTrue).Obj()
			cqCache.AddOrUpdateAdmissionCheck(log, mkAC)

			acWithPR := utiltestingapi.MakeAdmissionCheck("pr-check").ControllerName(kueue.ProvisioningRequestControllerName).Active(metav1.ConditionTrue).Obj()
			cqCache.AddOrUpdateAdmissionCheck(log, acWithPR)

			if err := cqCache.AddClusterQueue(ctx, tc.cq); err != nil {
				t.Fatalf("Inserting clusterQueue %s in cache: %v", tc.cq.Name, err)
			}

			if tc.updatedCq != nil {
				if err := cqCache.UpdateClusterQueue(log, tc.updatedCq); err != nil {
					t.Fatalf("Updating clusterQueue %s in cache: %v", tc.updatedCq.Name, err)
				}
			}

			if tc.updatedFlavor != nil {
				cqCache.AddOrUpdateResourceFlavor(log, tc.updatedFlavor)
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

func TestClusterQueueEffectiveQuotasUpdateAndFallback(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.DynamicQuotaOrchestration, true)
	ctx, log := utiltesting.ContextWithLog(t)
	cache := New(utiltesting.NewFakeClient())

	cqSpec := utiltestingapi.MakeClusterQueue("cq-eff").
		ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "5").Obj(),
		).Obj()

	if err := cache.AddClusterQueue(ctx, cqSpec); err != nil {
		t.Fatalf("failed to add CQ to cache: %v", err)
	}

	cqObj := cache.hm.ClusterQueue("cq-eff")
	if cqObj == nil {
		t.Fatalf("expected clusterqueue cq-eff in cache")
	}

	fr := resources.FlavorResource{Flavor: "default", Resource: corev1.ResourceCPU}
	if q := cqObj.resourceNode.Quotas[fr]; !q.Nominal.Equal(resources.NewAmount(5000)) {
		t.Errorf("expected nominal quota 5000 from spec, got %v", q.Nominal)
	}

	cqEffective := utiltestingapi.MakeClusterQueue("cq-eff").
		ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "5").Obj(),
		).
		EffectiveQuotas(
			*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "10").Obj(),
		).Obj()

	if err := cache.UpdateClusterQueue(log, cqEffective); err != nil {
		t.Fatalf("failed to update CQ in cache: %v", err)
	}

	cqObj = cache.hm.ClusterQueue("cq-eff")
	if q := cqObj.resourceNode.Quotas[fr]; !q.Nominal.Equal(resources.NewAmount(10000)) {
		t.Errorf("expected nominal quota 10000 from EffectiveQuotas, got %v", q.Nominal)
	}

	cqCleared := utiltestingapi.MakeClusterQueue("cq-eff").
		ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "5").Obj(),
		).Obj()

	if err := cache.UpdateClusterQueue(log, cqCleared); err != nil {
		t.Fatalf("failed to update CQ in cache: %v", err)
	}

	cqObj = cache.hm.ClusterQueue("cq-eff")
	if q := cqObj.resourceNode.Quotas[fr]; !q.Nominal.Equal(resources.NewAmount(5000)) {
		t.Errorf("expected nominal quota 5000 after clearing EffectiveQuotas, got %v", q.Nominal)
	}
}

// The cache ignores an effective quota marked EffectiveQuotaActive=False and uses
// it again once the condition is True. It never compares expirationTime with the clock.
func TestClusterQueueEffectiveQuotasInactive(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.DynamicQuotaOrchestration, true)
	metrics.ClearClusterQueueResourceMetrics("cq-expired")
	t.Cleanup(func() { metrics.ClearClusterQueueResourceMetrics("cq-expired") })
	ctx, log := utiltesting.ContextWithLog(t)
	cache := New(utiltesting.NewFakeClient())
	fr := resources.FlavorResource{Flavor: "default", Resource: corev1.ResourceCPU}

	base := utiltestingapi.MakeClusterQueue("cq-expired").
		ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "5").Obj(),
		).
		EffectiveQuotaStatus(utiltestingapi.MakeEffectiveQuotaStatus().
			Name("dqo").
			ResourceGroups(utiltestingapi.ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "10").Obj(),
			)).
			ExpirationTime(time.Now().Add(-time.Hour)).
			Obj())
	active := base.Clone().Condition(kueue.EffectiveQuotaActive, metav1.ConditionTrue, kueue.EffectiveQuotaActiveReasonIssued, "").Obj()
	expired := base.Clone().Condition(kueue.EffectiveQuotaActive, metav1.ConditionFalse, kueue.EffectiveQuotaActiveReasonExpired, "").Obj()

	if err := cache.AddClusterQueue(ctx, active); err != nil {
		t.Fatalf("failed to add CQ to cache: %v", err)
	}
	steps := []struct {
		name             string
		cq               *kueue.ClusterQueue
		wantNominal      int64
		wantOrchestrator string
	}{
		{name: "active", cq: active, wantNominal: 10_000, wantOrchestrator: "dqo"},
		{name: "expired", cq: expired, wantNominal: 5_000, wantOrchestrator: ""},
		{name: "active again", cq: active, wantNominal: 10_000, wantOrchestrator: "dqo"},
		{name: "expirationTime passed without the condition", cq: base.Clone().Obj(), wantNominal: 10_000, wantOrchestrator: "dqo"},
	}
	for _, step := range steps {
		if err := cache.UpdateClusterQueue(log, step.cq.DeepCopy()); err != nil {
			t.Fatalf("%s: failed to update CQ in cache: %v", step.name, err)
		}
		cqObj := cache.hm.ClusterQueue("cq-expired")
		if q := cqObj.resourceNode.Quotas[fr]; !q.Nominal.Equal(resources.NewAmount(step.wantNominal)) {
			t.Errorf("%s: nominal quota = %v, want %d", step.name, q.Nominal, step.wantNominal)
		}
		if got := string(cqObj.DynamicQuotaOrchestrator); got != step.wantOrchestrator {
			t.Errorf("%s: DynamicQuotaOrchestrator = %q, want %q", step.name, got, step.wantOrchestrator)
		}
		cache.RecordClusterQueueResourceMetrics(log, "cq-expired")
		dps := testingmetrics.CollectFilteredGaugeVec(metrics.ClusterQueueResourceNominalQuota, map[string]string{
			"cluster_queue": "cq-expired",
			"flavor":        "default",
			"resource":      string(corev1.ResourceCPU),
		})
		if len(dps) != 1 || dps[0].Value != float64(step.wantNominal)/1000 {
			t.Errorf("%s: nominal quota metric = %v, want one point of %v", step.name, dps, float64(step.wantNominal)/1000)
		}
	}
}

// An effective quota past int64 must arrive as the number it is, compare equal
// when rewritten, and fall back to the spec when cleared.
func TestClusterQueueEffectiveQuotasPastInt64(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.DynamicQuotaOrchestration, true)
	ctx, log := utiltesting.ContextWithLog(t)
	cache := New(utiltesting.NewFakeClient())

	cqSpec := utiltestingapi.MakeClusterQueue("cq-big").
		ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "5").Obj(),
		).Obj()
	cqEffective := utiltestingapi.MakeClusterQueue("cq-big").
		ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "5").Obj(),
		).
		EffectiveQuotas(
			*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "1E").Obj(),
		).Obj()

	if err := cache.AddClusterQueue(ctx, cqSpec); err != nil {
		t.Fatalf("AddClusterQueue() = %v", err)
	}
	fr := resources.FlavorResource{Flavor: "default", Resource: corev1.ResourceCPU}

	if err := cache.UpdateClusterQueue(log, cqEffective); err != nil {
		t.Fatalf("UpdateClusterQueue() = %v", err)
	}
	cqObj := cache.hm.ClusterQueue("cq-big")
	// 1E of CPU is 10^18 cores, which is 10^21 milliCPU and past int64.
	if got := cqObj.resourceNode.Quotas[fr].Nominal.String(); got != "1000000000000000000000" {
		t.Errorf("effective quota = %s, want 1000000000000000000000", got)
	}

	// Rewriting the same effective quota is not a change.
	genBefore := cqObj.AllocatableResourceGeneration
	if err := cache.UpdateClusterQueue(log, cqEffective); err != nil {
		t.Fatalf("UpdateClusterQueue() = %v", err)
	}
	cqObj = cache.hm.ClusterQueue("cq-big")
	if got := cqObj.AllocatableResourceGeneration; got != genBefore {
		t.Errorf("AllocatableResourceGeneration moved from %d to %d for an unchanged quota", genBefore, got)
	}

	if err := cache.UpdateClusterQueue(log, cqSpec); err != nil {
		t.Fatalf("UpdateClusterQueue() = %v", err)
	}
	cqObj = cache.hm.ClusterQueue("cq-big")
	if !cqObj.resourceNode.Quotas[fr].Nominal.Equal(resources.NewAmount(5000)) {
		t.Errorf("after clearing the effective quota = %s, want 5000",
			cqObj.resourceNode.Quotas[fr].Nominal)
	}
}

func TestClusterQueueLabels(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.ConfigurablePreemptions, true)
	const cqName = "cq"

	ctx, log := utiltesting.ContextWithLog(t)
	cq := utiltestingapi.MakeClusterQueue(cqName).
		Label("env", "prod").
		Label("tier", "batch").
		Obj()

	cache := New(utiltesting.NewFakeClient())
	if err := cache.AddClusterQueue(ctx, cq); err != nil {
		t.Fatalf("Failed to add cluster queue: %v", err)
	}

	snap, err := cache.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Failed to snapshot: %v", err)
	}
	if diff := cmp.Diff(map[string]string{"env": "prod", "tier": "batch"}, snap.ClusterQueue(cqName).Labels); diff != "" {
		t.Errorf("Unexpected snapshot labels (-want,+got):\n%s", diff)
	}

	// Verify mutating original object does not affect snapshot or cache
	cq.Labels["env"] = "mutated"
	snap2, err := cache.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Failed to snapshot: %v", err)
	}
	if diff := cmp.Diff(map[string]string{"env": "prod", "tier": "batch"}, snap2.ClusterQueue(cqName).Labels); diff != "" {
		t.Errorf("Snapshot was mutated by modifying original object (-want,+got):\n%s", diff)
	}

	// Verify UpdateClusterQueue updates labels and clones them
	updatedCQ := utiltestingapi.MakeClusterQueue(cqName).
		Label("env", "staging").
		Obj()
	if err := cache.UpdateClusterQueue(log, updatedCQ); err != nil {
		t.Fatalf("Failed to update cluster queue: %v", err)
	}
	snap3, err := cache.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Failed to snapshot: %v", err)
	}
	if diff := cmp.Diff(map[string]string{"env": "staging"}, snap3.ClusterQueue(cqName).Labels); diff != "" {
		t.Errorf("Unexpected updated snapshot labels (-want,+got):\n%s", diff)
	}

	// Verify mutating updated object does not affect subsequent snapshot
	updatedCQ.Labels["env"] = "mutated-again"
	snap4, err := cache.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Failed to snapshot: %v", err)
	}
	if diff := cmp.Diff(map[string]string{"env": "staging"}, snap4.ClusterQueue(cqName).Labels); diff != "" {
		t.Errorf("Snapshot was mutated by modifying updated object (-want,+got):\n%s", diff)
	}

	// Verify DeleteClusterQueue removes it from subsequent snapshots
	cache.DeleteClusterQueue(updatedCQ)
	snap5, err := cache.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Failed to snapshot: %v", err)
	}
	if snap5.ClusterQueue(cqName) != nil {
		t.Errorf("Expected ClusterQueue %q to be deleted from snapshot, but it was present", cqName)
	}

	t.Run("labels ignored when ConfigurablePreemptions feature gate is disabled", func(t *testing.T) {
		features.SetFeatureGateDuringTest(t, features.ConfigurablePreemptions, false)
		cacheDisabled := New(utiltesting.NewFakeClient())
		if err := cacheDisabled.AddClusterQueue(ctx, cq); err != nil {
			t.Fatalf("Failed to add cluster queue: %v", err)
		}
		snapDisabled, err := cacheDisabled.Snapshot(ctx)
		if err != nil {
			t.Fatalf("Failed to snapshot: %v", err)
		}
		if snapDisabled.ClusterQueue(cqName).Labels != nil {
			t.Errorf("Expected snapshot labels to be nil when feature gate is disabled, got %v", snapDisabled.ClusterQueue(cqName).Labels)
		}
	})
}

func TestClusterQueueAssignmentPlanner(t *testing.T) {
	const cqName = kueue.ClusterQueueReference("cq")

	t.Run("initial annotation values", func(t *testing.T) {
		cases := map[string]struct {
			deepIntegration bool
			annotation      *string
			wantPlanner     kueue.AssignmentPlannerName
		}{
			"gate disabled, unannotated defaults to KueueNative": {
				deepIntegration: false,
				wantPlanner:     kueue.KueueNative,
			},
			"gate disabled, KubeSchedulerDeepIntegration falls back to KueueNative": {
				deepIntegration: false,
				annotation:      new(string(kueue.KubeSchedulerDeepIntegration)),
				wantPlanner:     kueue.KueueNative,
			},
			"gate enabled, unannotated defaults to KueueNative": {
				deepIntegration: true,
				wantPlanner:     kueue.KueueNative,
			},
			"gate enabled, KueueNative": {
				deepIntegration: true,
				annotation:      new(string(kueue.KueueNative)),
				wantPlanner:     kueue.KueueNative,
			},
			"gate enabled, KubeSchedulerDeepIntegration": {
				deepIntegration: true,
				annotation:      new(string(kueue.KubeSchedulerDeepIntegration)),
				wantPlanner:     kueue.KubeSchedulerDeepIntegration,
			},
		}

		for name, tc := range cases {
			t.Run(name, func(t *testing.T) {
				features.SetFeatureGateDuringTest(t, features.SchedulerLibraryDeepIntegration, tc.deepIntegration)
				ctx, _ := utiltesting.ContextWithLog(t)
				cache := New(utiltesting.NewFakeClient())

				cqBuilder := utiltestingapi.MakeClusterQueue(string(cqName))
				if tc.annotation != nil {
					cqBuilder.Annotation(kueue.PreferredAssignmentPlannerAnnotation, *tc.annotation)
				}
				if err := cache.AddClusterQueue(ctx, cqBuilder.Obj()); err != nil {
					t.Fatalf("AddClusterQueue failed: %v", err)
				}

				snap, err := cache.Snapshot(ctx)
				if err != nil {
					t.Fatalf("Snapshot failed: %v", err)
				}
				if got := snap.ClusterQueue(cqName).AssignmentPlanner; got != tc.wantPlanner {
					t.Errorf("Snapshot AssignmentPlanner = %q, want %q", got, tc.wantPlanner)
				}
			})
		}
	})

	t.Run("switching annotation values updates cache and snapshot", func(t *testing.T) {
		features.SetFeatureGateDuringTest(t, features.SchedulerLibraryDeepIntegration, true)
		ctx, log := utiltesting.ContextWithLog(t)
		cache := New(utiltesting.NewFakeClient())

		cq := utiltestingapi.MakeClusterQueue(string(cqName)).Obj()
		if err := cache.AddClusterQueue(ctx, cq); err != nil {
			t.Fatalf("AddClusterQueue failed: %v", err)
		}

		steps := []struct {
			desc        string
			annotation  *string
			wantPlanner kueue.AssignmentPlannerName
		}{
			{
				desc:        "unannotated to KubeSchedulerDeepIntegration",
				annotation:  new(string(kueue.KubeSchedulerDeepIntegration)),
				wantPlanner: kueue.KubeSchedulerDeepIntegration,
			},
			{
				desc:        "KubeSchedulerDeepIntegration to KueueNative",
				annotation:  new(string(kueue.KueueNative)),
				wantPlanner: kueue.KueueNative,
			},
			{
				desc:        "KueueNative to KubeSchedulerDeepIntegration",
				annotation:  new(string(kueue.KubeSchedulerDeepIntegration)),
				wantPlanner: kueue.KubeSchedulerDeepIntegration,
			},
			{
				desc:        "KubeSchedulerDeepIntegration to unannotated",
				annotation:  nil,
				wantPlanner: kueue.KueueNative,
			},
		}

		for _, step := range steps {
			updated := utiltestingapi.MakeClusterQueue(string(cqName))
			if step.annotation != nil {
				updated.Annotation(kueue.PreferredAssignmentPlannerAnnotation, *step.annotation)
			}
			if err := cache.UpdateClusterQueue(log, updated.Obj()); err != nil {
				t.Fatalf("%s: UpdateClusterQueue failed: %v", step.desc, err)
			}
			snap, err := cache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("%s: Snapshot failed: %v", step.desc, err)
			}
			if got := snap.ClusterQueue(cqName).AssignmentPlanner; got != step.wantPlanner {
				t.Errorf("%s: Snapshot AssignmentPlanner = %q, want %q", step.desc, got, step.wantPlanner)
			}
		}
	})

	t.Run("switching annotation values while gate disabled keeps KueueNative in cache and snapshot", func(t *testing.T) {
		features.SetFeatureGateDuringTest(t, features.SchedulerLibraryDeepIntegration, false)
		ctx, log := utiltesting.ContextWithLog(t)
		cache := New(utiltesting.NewFakeClient())

		cq := utiltestingapi.MakeClusterQueue(string(cqName)).
			Annotation(kueue.PreferredAssignmentPlannerAnnotation, string(kueue.KueueNative)).
			Obj()
		if err := cache.AddClusterQueue(ctx, cq); err != nil {
			t.Fatalf("AddClusterQueue failed: %v", err)
		}

		updated := utiltestingapi.MakeClusterQueue(string(cqName)).
			Annotation(kueue.PreferredAssignmentPlannerAnnotation, string(kueue.KubeSchedulerDeepIntegration)).
			Obj()
		if err := cache.UpdateClusterQueue(log, updated); err != nil {
			t.Fatalf("UpdateClusterQueue failed: %v", err)
		}
		snap, err := cache.Snapshot(ctx)
		if err != nil {
			t.Fatalf("Snapshot failed: %v", err)
		}
		if got := snap.ClusterQueue(cqName).AssignmentPlanner; got != kueue.KueueNative {
			t.Errorf("Snapshot AssignmentPlanner = %q, want %q", got, kueue.KueueNative)
		}
	})
}
