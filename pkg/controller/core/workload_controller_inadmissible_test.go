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

package core

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/component-base/featuregate"
	testingclock "k8s.io/utils/clock/testing"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestReconcileInadmissible(t *testing.T) {
	// the clock is primarily used with second rounded times
	// use the current time trimmed.
	now := time.Now().Truncate(time.Second)
	fakeClock := testingclock.NewFakeClock(now)

	cases := map[string]reconcileTestCase{
		"should set the Misconfigured reason on QuotaReservation condition when the LocalQueue was deleted": {
			cq: utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("flavor1").Obj()).
				AdmissionChecks("check").Obj(),
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(true).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "flavor1", "1").
						Obj()).
					Obj(), now).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:  "check",
					State: kueue.CheckStatePending,
				}).
				Queue("lq").
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(true).
				Admission(utiltestingapi.MakeAdmission("cq").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "flavor1", "1").
						Obj()).
					Obj()).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:  "check",
					State: kueue.CheckStatePending,
				}).
				Queue("lq").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadQuotaReservedReasonMisconfigured,
					Message: "LocalQueue lq is terminating or missing",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadAdmitted,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadAdmittedReasonNoReservation,
					Message: "The workload has no reservation",
				}).
				Obj(),
			wantWorkloadUseMergePatch: utiltestingapi.MakeWorkload("wl", "ns").
				Active(true).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:  "check",
					State: kueue.CheckStatePending,
				}).
				Queue("lq").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadQuotaReservedReasonMisconfigured,
					Message: "LocalQueue lq is terminating or missing",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadAdmitted,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadAdmittedReasonNoReservation,
					Message: "The workload has no reservation",
				}).
				Obj(),
		},
		"should set the Misconfigured reason on QuotaReservation condition when the LocalQueue is terminating": {
			cq: utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("flavor1").Obj()).
				AdmissionChecks("check").Obj(),
			lq:             utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj(),
			shouldDeleteLQ: true, // The setup helper will attach a finalizer and delete the object, which automatically populates its DeletionTimestamp in the fake client.
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(true).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "flavor1", "1").
						Obj()).
					Obj(), now).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:  "check",
					State: kueue.CheckStatePending,
				}).
				Queue("lq").
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(true).
				Admission(utiltestingapi.MakeAdmission("cq").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "flavor1", "1").
						Obj()).
					Obj()).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:  "check",
					State: kueue.CheckStatePending,
				}).
				Queue("lq").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadQuotaReservedReasonMisconfigured,
					Message: "LocalQueue lq is terminating or missing",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadAdmitted,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadAdmittedReasonNoReservation,
					Message: "The workload has no reservation",
				}).
				Obj(),
			wantWorkloadUseMergePatch: utiltestingapi.MakeWorkload("wl", "ns").
				Active(true).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:  "check",
					State: kueue.CheckStatePending,
				}).
				Queue("lq").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadQuotaReservedReasonMisconfigured,
					Message: "LocalQueue lq is terminating or missing",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadAdmitted,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadAdmittedReasonNoReservation,
					Message: "The workload has no reservation",
				}).
				Obj(),
		},
		"should set the Suspended reason on QuotaReservation condition when the LocalQueue was Hold": {
			cq: utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("flavor1").Obj()).
				AdmissionChecks("check").Obj(),
			lq: utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").StopPolicy(kueue.Hold).Obj(),
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(true).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "flavor1", "1").
						Obj()).
					Obj(), now).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:  "check",
					State: kueue.CheckStatePending,
				}).
				Queue("lq").
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(true).
				Admission(utiltestingapi.MakeAdmission("cq").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "flavor1", "1").
						Obj()).
					Obj()).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:  "check",
					State: kueue.CheckStatePending,
				}).
				Queue("lq").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadQuotaReservedReasonSuspended,
					Message: "LocalQueue lq is stopped",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadAdmitted,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadAdmittedReasonNoReservation,
					Message: "The workload has no reservation",
				}).
				Obj(),
			wantWorkloadUseMergePatch: utiltestingapi.MakeWorkload("wl", "ns").
				Active(true).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:  "check",
					State: kueue.CheckStatePending,
				}).
				Queue("lq").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadQuotaReservedReasonSuspended,
					Message: "LocalQueue lq is stopped",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadAdmitted,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadAdmittedReasonNoReservation,
					Message: "The workload has no reservation",
				}).
				Obj(),
		},
		"should set the Misconfigured reason on QuotaReservation condition when the ClusterQueue was deleted": {
			lq: utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj(),
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(true).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "flavor1", "1").
						Obj()).
					Obj(), now).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:  "check",
					State: kueue.CheckStatePending,
				}).
				Queue("lq").
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(true).
				Admission(utiltestingapi.MakeAdmission("cq").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "flavor1", "1").
						Obj()).
					Obj()).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:  "check",
					State: kueue.CheckStatePending,
				}).
				Queue("lq").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadQuotaReservedReasonMisconfigured,
					Message: "ClusterQueue cq is terminating or missing",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadAdmitted,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadAdmittedReasonNoReservation,
					Message: "The workload has no reservation",
				}).
				Obj(),
			wantWorkloadUseMergePatch: utiltestingapi.MakeWorkload("wl", "ns").
				Active(true).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:  "check",
					State: kueue.CheckStatePending,
				}).
				Queue("lq").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadQuotaReservedReasonMisconfigured,
					Message: "ClusterQueue cq is terminating or missing",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadAdmitted,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadAdmittedReasonNoReservation,
					Message: "The workload has no reservation",
				}).
				Obj(),
		},
		"should set the Misconfigured reason on QuotaReservation condition when the ClusterQueue is terminating": {
			cq: utiltestingapi.MakeClusterQueue("cq").
				Cohort("cohort").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("flavor1").Obj()).
				AdmissionChecks("check").
				Obj(),
			lq:             utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj(),
			shouldDeleteCQ: true, // The setup helper will attach a finalizer and delete the object, which automatically populates its DeletionTimestamp in the fake client.
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(true).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "flavor1", "1").
						Obj()).
					Obj(), now).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:  "check",
					State: kueue.CheckStatePending,
				}).
				Queue("lq").
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(true).
				Admission(utiltestingapi.MakeAdmission("cq").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "flavor1", "1").
						Obj()).
					Obj()).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:  "check",
					State: kueue.CheckStatePending,
				}).
				Queue("lq").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadQuotaReservedReasonMisconfigured,
					Message: "ClusterQueue cq is terminating or missing",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadAdmitted,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadAdmittedReasonNoReservation,
					Message: "The workload has no reservation",
				}).
				Obj(),
			wantWorkloadUseMergePatch: utiltestingapi.MakeWorkload("wl", "ns").
				Active(true).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:  "check",
					State: kueue.CheckStatePending,
				}).
				Queue("lq").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadQuotaReservedReasonMisconfigured,
					Message: "ClusterQueue cq is terminating or missing",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadAdmitted,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadAdmittedReasonNoReservation,
					Message: "The workload has no reservation",
				}).
				Obj(),
		},
		"should set the Suspended reason on QuotaReservation condition when the ClusterQueue was Hold": {
			cq: utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("flavor1").Obj()).
				AdmissionChecks("check").StopPolicy(kueue.Hold).Obj(),
			lq: utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj(),
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(true).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "flavor1", "1").
						Obj()).
					Obj(), now).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:  "check",
					State: kueue.CheckStatePending,
				}).
				Queue("lq").
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(true).
				Admission(utiltestingapi.MakeAdmission("cq").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "flavor1", "1").
						Obj()).
					Obj()).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:  "check",
					State: kueue.CheckStatePending,
				}).
				Queue("lq").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadQuotaReservedReasonSuspended,
					Message: "ClusterQueue cq is stopped",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadAdmitted,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadAdmittedReasonNoReservation,
					Message: "The workload has no reservation",
				}).
				Obj(),
			wantWorkloadUseMergePatch: utiltestingapi.MakeWorkload("wl", "ns").
				Active(true).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:  "check",
					State: kueue.CheckStatePending,
				}).
				Queue("lq").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadQuotaReservedReasonSuspended,
					Message: "ClusterQueue cq is stopped",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadAdmitted,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadAdmittedReasonNoReservation,
					Message: "The workload has no reservation",
				}).
				Obj(),
		},
		"should set status QuotaReserved conditions to False with reason Misconfigured if quota not reserved LocalQueue is not created": {
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(true).
				Queue("lq").
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(true).
				Queue("lq").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadQuotaReservedReasonMisconfigured,
					Message: "LocalQueue lq doesn't exist",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadAdmitted,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadAdmittedReasonNoReservation,
					Message: "The workload has no reservation",
				}).
				Obj(),
		},
		"should set status QuotaReserved conditions to False with reason Deactivated if workload is deactivated even if LocalQueue doesn't exist": {
			featureGates: map[featuregate.Feature]bool{
				features.UnadmittedWorkloadsObservability: true,
			},
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(false).
				Queue("lq").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadEvicted,
					Status:  metav1.ConditionTrue,
					Reason:  kueue.WorkloadDeactivated,
					Message: "The workload is deactivated",
				}).
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(false).
				Queue("lq").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadEvicted,
					Status:  metav1.ConditionTrue,
					Reason:  kueue.WorkloadDeactivated,
					Message: "The workload is deactivated",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadDeactivated,
					Message: "The workload is deactivated",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadAdmitted,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadAdmittedReasonNoReservation,
					Message: "The workload has no reservation",
				}).
				Obj(),
		},
		"should preserve NoMatchingFlavor reason": {
			featureGates: map[featuregate.Feature]bool{
				features.UnadmittedWorkloadsObservability: true,
			},
			cq: utiltestingapi.MakeClusterQueue("cq").Active(metav1.ConditionTrue).Obj(),
			lq: utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj(),
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(true).
				Queue("lq").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadQuotaReservedReasonNoMatchingFlavor,
					Message: "No matching flavor",
				}).
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(true).
				Queue("lq").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadQuotaReservedReasonNoMatchingFlavor,
					Message: "No matching flavor",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadAdmitted,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadAdmittedReasonNoReservation,
					Message: "The workload has no reservation",
				}).
				Obj(),
		},
		"should preserve WaitingForPodsReady reason": {
			featureGates: map[featuregate.Feature]bool{
				features.UnadmittedWorkloadsObservability: true,
			},
			cq: utiltestingapi.MakeClusterQueue("cq").Active(metav1.ConditionTrue).Obj(),
			lq: utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj(),
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(true).
				Queue("lq").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadQuotaReservedReasonWaitingForPodsReady,
					Message: "Waiting for pods ready",
				}).
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(true).
				Queue("lq").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadQuotaReservedReasonWaitingForPodsReady,
					Message: "Waiting for pods ready",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadAdmitted,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadAdmittedReasonNoReservation,
					Message: "The workload has no reservation",
				}).
				Obj(),
		},
		"should preserve ExceedsMaxQuota reason": {
			featureGates: map[featuregate.Feature]bool{
				features.UnadmittedWorkloadsObservability: true,
			},
			cq: utiltestingapi.MakeClusterQueue("cq").Active(metav1.ConditionTrue).Obj(),
			lq: utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj(),
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(true).
				Queue("lq").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadQuotaReservedReasonExceedsMaxQuota,
					Message: "Exceeds max quota",
				}).
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(true).
				Queue("lq").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadQuotaReservedReasonExceedsMaxQuota,
					Message: "Exceeds max quota",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadAdmitted,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadAdmittedReasonNoReservation,
					Message: "The workload has no reservation",
				}).
				Obj(),
		},
		"should write AdmissionGated reason when admission gate is active": {
			featureGates: map[featuregate.Feature]bool{
				features.UnadmittedWorkloadsObservability: true,
				features.AdmissionGatedBy:                 true,
			},
			cq: utiltestingapi.MakeClusterQueue("cq").Active(metav1.ConditionTrue).Obj(),
			lq: utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj(),
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(true).
				Queue("lq").
				Annotation("kueue.x-k8s.io/admission-gated-by", "foo-gate").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadAdmissionGated,
					Message: "Admission is gated by: foo-gate",
				}).
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(true).
				Queue("lq").
				Annotation("kueue.x-k8s.io/admission-gated-by", "foo-gate").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadAdmissionGated,
					Message: "Admission is gated by: foo-gate",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadAdmitted,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadAdmittedReasonNoReservation,
					Message: "The workload has no reservation",
				}).
				Obj(),
		},
		"should transition to PendingEvaluation when all blockages are resolved": {
			featureGates: map[featuregate.Feature]bool{
				features.UnadmittedWorkloadsObservability: true,
			},
			cq: utiltestingapi.MakeClusterQueue("cq").Active(metav1.ConditionTrue).Obj(),
			lq: utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj(),
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(true).
				Queue("lq").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadQuotaReservedReasonSuspended,
					Message: "LocalQueue lq is inactive",
				}).
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(true).
				Queue("lq").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadQuotaReservedReasonPendingEvaluation,
					Message: "Workload is pending evaluation in the scheduling queue",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadAdmitted,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadAdmittedReasonNoReservation,
					Message: "The workload has no reservation",
				}).
				Obj(),
		},
		"should transition to PendingEvaluation when misconfiguration (missing LocalQueue) is resolved": {
			featureGates: map[featuregate.Feature]bool{
				features.UnadmittedWorkloadsObservability: true,
			},
			cq: utiltestingapi.MakeClusterQueue("cq").Active(metav1.ConditionTrue).Obj(),
			lq: utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj(),
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(true).
				Queue("lq").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadQuotaReservedReasonMisconfigured,
					Message: "LocalQueue lq doesn't exist",
				}).
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(true).
				Queue("lq").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadQuotaReservedReasonPendingEvaluation,
					Message: "Workload is pending evaluation in the scheduling queue",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadAdmitted,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadAdmittedReasonNoReservation,
					Message: "The workload has no reservation",
				}).
				Obj(),
		},
		"should not write QuotaReserved condition on newly created workload": {
			featureGates: map[featuregate.Feature]bool{
				features.UnadmittedWorkloadsObservability: true,
			},
			cq: utiltestingapi.MakeClusterQueue("cq").Active(metav1.ConditionTrue).Obj(),
			lq: utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj(),
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(true).
				Queue("lq").
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(true).
				Queue("lq").
				Obj(),
		},
		"should set status QuotaReserved conditions to False with reason Suspended if quota not reserved LocalQueue StopPolicy=Hold": {
			lq: utiltestingapi.MakeLocalQueue("lq", "ns").StopPolicy(kueue.Hold).Obj(),
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(true).
				Queue("lq").
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(true).
				Queue("lq").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadQuotaReservedReasonSuspended,
					Message: "LocalQueue lq is inactive",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadAdmitted,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadAdmittedReasonNoReservation,
					Message: "The workload has no reservation",
				}).
				Obj(),
		},
		"should set status QuotaReserved conditions to False with reason Suspended if quota not reserved LocalQueue StopPolicy=HoldAndDrain": {
			lq: utiltestingapi.MakeLocalQueue("lq", "ns").StopPolicy(kueue.HoldAndDrain).Obj(),
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(true).
				Queue("lq").
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(true).
				Queue("lq").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadQuotaReservedReasonSuspended,
					Message: "LocalQueue lq is inactive",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadAdmitted,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadAdmittedReasonNoReservation,
					Message: "The workload has no reservation",
				}).
				Obj(),
		},
		"should set status QuotaReserved conditions to False with reason Misconfigured if quota not reserved ClusterQueue is not created": {
			lq: utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").StopPolicy(kueue.None).Obj(),
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(true).
				Queue("lq").
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(true).
				Queue("lq").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadQuotaReservedReasonMisconfigured,
					Message: "ClusterQueue cq doesn't exist",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadAdmitted,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadAdmittedReasonNoReservation,
					Message: "The workload has no reservation",
				}).
				Obj(),
		},
		"should set status QuotaReserved conditions to False with reason Suspended if quota not reserved ClusterQueue StopPolicy=Hold": {
			lq: utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").StopPolicy(kueue.None).Obj(),
			cq: utiltestingapi.MakeClusterQueue("cq").StopPolicy(kueue.Hold).Obj(),
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(true).
				Queue("lq").
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(true).
				Queue("lq").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadQuotaReservedReasonSuspended,
					Message: "ClusterQueue cq is inactive",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadAdmitted,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadAdmittedReasonNoReservation,
					Message: "The workload has no reservation",
				}).
				Obj(),
		},
	}
	runReconcileTestCases(t, cases, fakeClock)
}
