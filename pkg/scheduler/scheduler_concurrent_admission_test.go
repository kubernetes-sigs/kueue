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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/component-base/featuregate"
	testingclock "k8s.io/utils/clock/testing"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestScheduleConcurrentAdmission(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	fakeClock := testingclock.NewFakeClock(now)

	resourceFlavors := []*kueue.ResourceFlavor{
		utiltestingapi.MakeResourceFlavor("reservation").Obj(),
		utiltestingapi.MakeResourceFlavor("on-demand").Obj(),
		utiltestingapi.MakeResourceFlavor("spot").Obj(),
	}
	clusterQueues := []kueue.ClusterQueue{
		*utiltestingapi.MakeClusterQueue("concurrent-cq").
			NamespaceSelector(&metav1.LabelSelector{
				MatchExpressions: []metav1.LabelSelectorRequirement{{
					Key:      "dep",
					Operator: metav1.LabelSelectorOpIn,
					Values:   []string{"eng"},
				}},
			}).
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("on-demand").
					Resource(corev1.ResourceCPU, "10").Obj(),
				*utiltestingapi.MakeFlavorQuotas("spot").
					Resource(corev1.ResourceCPU, "10").Obj(),
			).
			Obj(),
	}
	queues := []kueue.LocalQueue{
		*utiltestingapi.MakeLocalQueue("concurrent-queue", "eng-alpha").ClusterQueue("concurrent-cq").Obj(),
	}

	cases := map[string]scheduleTestCase{
		"concurrent admission: more favorable variant evicts less favorable sibling": {
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("sibling-less-favorable", "eng-alpha").
					UID("sibling-uid").
					Queue("concurrent-queue").
					Request(corev1.ResourceCPU, "10").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("concurrent-cq").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "spot", "10").
							Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "parent-workload", "parent-uid").
					AllowedFlavors("spot").
					Obj(),
				*utiltestingapi.MakeWorkload("candidate-more-favorable", "eng-alpha").
					UID("candidate-uid").
					Queue("concurrent-queue").
					Request(corev1.ResourceCPU, "10").
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "parent-workload", "parent-uid").
					AllowedFlavors("on-demand").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("candidate-more-favorable", "eng-alpha").
					UID("candidate-uid").
					Queue("concurrent-queue").
					Request(corev1.ResourceCPU, "10").
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "parent-workload", "parent-uid").
					AllowedFlavors("on-demand").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads,
						Message:            ". Pending the migration of 1 workload(s)",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadAdmittedReasonNoReservation,
						Message:            "The workload has no reservation",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "main",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("10"),
						},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("sibling-less-favorable", "eng-alpha").
					UID("sibling-uid").
					Queue("concurrent-queue").
					Request(corev1.ResourceCPU, "10").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("concurrent-cq").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "spot", "10").
							Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "parent-workload", "parent-uid").
					AllowedFlavors("spot").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "FlavorMigration",
						Message:            "Evicted to accommodate a workload (UID: candidate-uid) due to migration to more favorable resource flavor",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "FlavorMigration", Count: 1}).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"concurrent-cq": {"eng-alpha/candidate-more-favorable"},
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/sibling-less-favorable": *utiltestingapi.MakeAdmission("concurrent-cq").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "spot", "10").
						Obj()).
					Obj(),
			},
			featureGates: map[featuregate.Feature]bool{features.ConcurrentAdmission: true},
		},
		"concurrent admission: less favorable variant is skipped when more favorable sibling is admitted": {
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("sibling-more-favorable", "eng-alpha").
					UID("sibling-uid").
					Queue("concurrent-queue").
					Request(corev1.ResourceCPU, "10").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("concurrent-cq").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "on-demand", "10").
							Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "parent-workload", "parent-uid").
					AllowedFlavors("on-demand").
					Obj(),
				*utiltestingapi.MakeWorkload("candidate-less-favorable", "eng-alpha").
					UID("candidate-uid").
					Queue("concurrent-queue").
					Request(corev1.ResourceCPU, "10").
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "parent-workload", "parent-uid").
					AllowedFlavors("spot").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("sibling-more-favorable", "eng-alpha").
					UID("sibling-uid").
					Queue("concurrent-queue").
					Request(corev1.ResourceCPU, "10").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("concurrent-cq").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "on-demand", "10").
							Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "parent-workload", "parent-uid").
					AllowedFlavors("on-demand").
					Obj(),
				*utiltestingapi.MakeWorkload("candidate-less-favorable", "eng-alpha").
					UID("candidate-uid").
					Queue("concurrent-queue").
					Request(corev1.ResourceCPU, "10").
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "parent-workload", "parent-uid").
					AllowedFlavors("spot").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonPendingEvaluation,
						Message:            "A more favorable variant is already admitted",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadAdmittedReasonNoReservation,
						Message:            "The workload has no reservation",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "main",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("10"),
						},
					}).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"concurrent-cq": {"eng-alpha/candidate-less-favorable"},
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/sibling-more-favorable": *utiltestingapi.MakeAdmission("concurrent-cq").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "on-demand", "10").
						Obj()).
					Obj(),
			},
			featureGates: map[featuregate.Feature]bool{features.ConcurrentAdmission: true},
		},
		"concurrent admission: migration is stopped when target flavor is below LastAcceptableFlavorName": {
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("sibling-less-favorable", "eng-alpha").
					UID("sibling-uid").
					Queue("concurrent-queue-new").
					Request(corev1.ResourceCPU, "10").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("concurrent-cq-with-min-pref").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "spot", "10").
							Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "parent-workload", "parent-uid").
					AllowedFlavors("spot").
					Obj(),
				*utiltestingapi.MakeWorkload("candidate-more-favorable", "eng-alpha").
					UID("candidate-uid").
					Queue("concurrent-queue-new").
					Request(corev1.ResourceCPU, "10").
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "parent-workload", "parent-uid").
					AllowedFlavors("on-demand").
					Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltestingapi.MakeLocalQueue("concurrent-queue-new", "eng-alpha").ClusterQueue("concurrent-cq-with-min-pref").Obj(),
			},
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("concurrent-cq-with-min-pref").
					NamespaceSelector(&metav1.LabelSelector{
						MatchExpressions: []metav1.LabelSelectorRequirement{{
							Key:      "dep",
							Operator: metav1.LabelSelectorOpIn,
							Values:   []string{"eng"},
						}},
					}).
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("reservation").
							Resource(corev1.ResourceCPU, "10").Obj(),
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "10").Obj(),
						*utiltestingapi.MakeFlavorQuotas("spot").
							Resource(corev1.ResourceCPU, "10").Obj(),
					).
					LastAcceptableFlavorName("reservation").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("sibling-less-favorable", "eng-alpha").
					UID("sibling-uid").
					Queue("concurrent-queue-new").
					Request(corev1.ResourceCPU, "10").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("concurrent-cq-with-min-pref").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "spot", "10").
							Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "parent-workload", "parent-uid").
					AllowedFlavors("spot").
					Obj(),
				*utiltestingapi.MakeWorkload("candidate-more-favorable", "eng-alpha").
					UID("candidate-uid").
					Queue("concurrent-queue-new").
					Request(corev1.ResourceCPU, "10").
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "parent-workload", "parent-uid").
					AllowedFlavors("on-demand").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonPendingEvaluation,
						Message:            "Target flavor is below LastAcceptableFlavorName",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadAdmittedReasonNoReservation,
						Message:            "The workload has no reservation",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "main",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("10"),
						},
					}).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"concurrent-cq-with-min-pref": {"eng-alpha/candidate-more-favorable"},
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/sibling-less-favorable": *utiltestingapi.MakeAdmission("concurrent-cq-with-min-pref").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "spot", "10").
						Obj()).
					Obj(),
			},
			featureGates: map[featuregate.Feature]bool{features.ConcurrentAdmission: true},
		},
	}

	runScheduleTestCases(t, scheduleTestConfig{
		queues:             queues,
		clusterQueues:      clusterQueues,
		resourceFlavors:    resourceFlavors,
		fakeClock:          fakeClock,
		schedulingTimeout:  10 * time.Second,
		unorderedWorkloads: true,
	}, cases)
}
