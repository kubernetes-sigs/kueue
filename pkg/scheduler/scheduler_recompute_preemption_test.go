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
	"fmt"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/component-base/featuregate"
	testingclock "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	utiltestingalpha "sigs.k8s.io/kueue/pkg/util/testing/v1alpha1"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
)

func defaultCohorts() []kueue.Cohort {
	// Cohort hierarchy for this test suite:
	//
	//                     root-cohort (nominal: 3005)
	//                   /              |              \
	//         parent-cohort-a   parent-cohort-b   parent-cohort-c
	//           (nominal: 0)     (nominal: 0)      (nominal: 0)
	//            /        \            |                 |
	//         cq-hero   cq-noisy    cq-tiny           cq-rest
	//       (nom: 3000) (nom: 0)   (nom: 50)         (nom: 0)
	return []kueue.Cohort{
		*utiltestingapi.MakeCohort("root-cohort").ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "3005").Obj(),
		).Obj(),
		*utiltestingapi.MakeCohort("parent-cohort-a").Parent("root-cohort").ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "0").Obj(),
		).Obj(),
		*utiltestingapi.MakeCohort("parent-cohort-b").Parent("root-cohort").Obj(),
		*utiltestingapi.MakeCohort("parent-cohort-c").Parent("root-cohort").Obj(),
	}
}

func TestScheduleRecomputePreemptionTargets(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	fakeClock := testingclock.NewFakeClock(now)

	resourceFlavors := []*kueue.ResourceFlavor{
		utiltestingapi.MakeResourceFlavor("default").Obj(),
		utiltestingapi.MakeResourceFlavor("on-demand").Obj(),
	}

	clusterQueues := []kueue.ClusterQueue{
		*utiltestingapi.MakeClusterQueue("cq-hero").
			Cohort("parent-cohort-a").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("default").
					Resource(corev1.ResourceCPU, "3000").Obj(),
			).
			Preemption(kueue.ClusterQueuePreemption{
				ReclaimWithinCohort: kueue.PreemptionPolicyAny,
				WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
			}).Obj(),
		*utiltestingapi.MakeClusterQueue("cq-noisy").
			Cohort("parent-cohort-a").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("default").
					Resource(corev1.ResourceCPU, "0").Obj(),
			).
			Preemption(kueue.ClusterQueuePreemption{
				ReclaimWithinCohort: kueue.PreemptionPolicyAny,
				WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
			}).Obj(),
		*utiltestingapi.MakeClusterQueue("cq-tiny").
			Cohort("parent-cohort-b").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("default").
					Resource(corev1.ResourceCPU, "50").Obj()).
			Preemption(kueue.ClusterQueuePreemption{
				ReclaimWithinCohort: kueue.PreemptionPolicyAny,
				WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
			}).Obj(),
		*utiltestingapi.MakeClusterQueue("cq-rest").
			Cohort("parent-cohort-c").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("default").
					Resource(corev1.ResourceCPU, "0").Obj()).
			Preemption(kueue.ClusterQueuePreemption{
				ReclaimWithinCohort: kueue.PreemptionPolicyAny,
				WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
			}).Obj(),
	}

	queues := []kueue.LocalQueue{
		*utiltestingapi.MakeLocalQueue("lq-hero", "eng-alpha").ClusterQueue("cq-hero").Obj(),
		*utiltestingapi.MakeLocalQueue("lq-noisy", "eng-alpha").ClusterQueue("cq-noisy").Obj(),
		*utiltestingapi.MakeLocalQueue("lq-tiny", "eng-alpha").ClusterQueue("cq-tiny").Obj(),
		*utiltestingapi.MakeLocalQueue("lq-rest", "eng-alpha").ClusterQueue("cq-rest").Obj(),
	}

	// In the workload slice cases "x" reclaims from the borrowing "v", and
	// "grow" replaces "old" and overlaps x's targets, so grow is deferred until
	// v is gone.
	sliceClusterQueues := []kueue.ClusterQueue{
		*utiltestingapi.MakeClusterQueue("cq-v").
			Cohort("slice-cohort").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "0").Obj()).
			Preemption(kueue.ClusterQueuePreemption{
				ReclaimWithinCohort: kueue.PreemptionPolicyAny,
			}).
			Obj(),
		*utiltestingapi.MakeClusterQueue("cq-x").
			Cohort("slice-cohort").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "4").Obj()).
			Preemption(kueue.ClusterQueuePreemption{
				ReclaimWithinCohort: kueue.PreemptionPolicyAny,
			}).
			Obj(),
		*utiltestingapi.MakeClusterQueue("cq-a").
			Cohort("slice-cohort").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "6").Obj()).
			Preemption(kueue.ClusterQueuePreemption{
				ReclaimWithinCohort: kueue.PreemptionPolicyAny,
			}).
			Obj(),
		*utiltestingapi.MakeClusterQueue("cq-l").
			Cohort("slice-cohort").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "0").Obj()).
			Preemption(kueue.ClusterQueuePreemption{
				ReclaimWithinCohort: kueue.PreemptionPolicyAny,
			}).
			Obj(),
	}
	sliceLocalQueues := []kueue.LocalQueue{
		*utiltestingapi.MakeLocalQueue("lq-v", "default").ClusterQueue("cq-v").Obj(),
		*utiltestingapi.MakeLocalQueue("lq-x", "default").ClusterQueue("cq-x").Obj(),
		*utiltestingapi.MakeLocalQueue("lq-a", "default").ClusterQueue("cq-a").Obj(),
		*utiltestingapi.MakeLocalQueue("lq-l", "default").ClusterQueue("cq-l").Obj(),
	}
	const sliceDeferredMsg = "Workload has overlapping preemption targets with another workload, but will fit after these preemptions complete"

	// Shared fixtures for PreemptionLimit cases: p1 (cq-p1, cfg-one) is older than p2 (cq-p2, cfg-two),
	// so it preempts first. Each config selects candidates with its "group" label from borrowing cq-v and cq-w.
	cqP1 := *utiltestingapi.MakeClusterQueue("cq-p1").
		Cohort("limits-cohort").
		ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "1").Obj()).
		Annotation(kueuealpha.PreemptionConfigNameAnnotation, "cfg-one").
		Obj()
	cqP2 := *utiltestingapi.MakeClusterQueue("cq-p2").
		Cohort("limits-cohort").
		ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "2").Obj()).
		Annotation(kueuealpha.PreemptionConfigNameAnnotation, "cfg-two").
		Obj()
	cqP2FallbackBase := utiltestingapi.MakeClusterQueue("cq-p2").
		Cohort("limits-cohort").
		ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "1").Obj(),
			*utiltestingapi.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "1").Obj(),
		).
		FlavorFungibility(kueue.FlavorFungibility{WhenCanBorrow: kueue.MayStopSearch, WhenCanPreempt: kueue.MayStopSearch}).
		Annotation(kueuealpha.PreemptionConfigNameAnnotation, "cfg-two")
	cqV := *utiltestingapi.MakeClusterQueue("cq-v").
		Cohort("limits-cohort").
		ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "0").Obj()).
		Obj()
	cqW := *utiltestingapi.MakeClusterQueue("cq-w").
		Cohort("limits-cohort").
		ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "0").Obj()).
		Obj()
	limitsClusterQueues := []kueue.ClusterQueue{cqP1, cqP2, cqV, cqW}
	limitsLocalQueues := []kueue.LocalQueue{
		*utiltestingapi.MakeLocalQueue("lq-p1", "default").ClusterQueue("cq-p1").Obj(),
		*utiltestingapi.MakeLocalQueue("lq-p2", "default").ClusterQueue("cq-p2").Obj(),
		*utiltestingapi.MakeLocalQueue("lq-v", "default").ClusterQueue("cq-v").Obj(),
		*utiltestingapi.MakeLocalQueue("lq-w", "default").ClusterQueue("cq-w").Obj(),
	}
	limitsFeatureGates := map[featuregate.Feature]bool{
		features.ConfigurablePreemptions:      true,
		features.ConfigurablePreemptionLimits: true,
	}
	cfgOne := utiltestingalpha.MakePreemptionConfig("cfg-one").
		Rule("rule-one", kueuealpha.Always,
			utiltestingalpha.MakeCandidateSelector(kueuealpha.AnyClusterQueue).
				LabelSelector(&metav1.LabelSelector{MatchLabels: map[string]string{"group": "one"}}).
				Obj(),
		).Obj()
	cfgTwo := utiltestingalpha.MakePreemptionConfig("cfg-two").
		Rule("rule-two", kueuealpha.Always,
			utiltestingalpha.MakeCandidateSelector(kueuealpha.AnyClusterQueue).
				LabelSelector(&metav1.LabelSelector{MatchLabels: map[string]string{"group": "two"}}).
				Obj(),
		).Obj()
	perPreemptedCQLimit := utiltestingalpha.MakePreemptionLimit("per-preempted-cq", kueuealpha.ByPreempteeCQLimitAggregation).
		UID("per-preempted-cq-uid").
		Limit(1).WindowSeconds(60)
	admCQV1 := utiltestingapi.MakeAdmission("cq-v").
		PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Assignment(corev1.ResourceCPU, "default", "1").Obj()).
		Obj()
	admCQV2 := utiltestingapi.MakeAdmission("cq-v").
		PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Assignment(corev1.ResourceCPU, "default", "2").Obj()).
		Obj()
	admCQW1 := utiltestingapi.MakeAdmission("cq-w").
		PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Assignment(corev1.ResourceCPU, "default", "1").Obj()).
		Obj()
	admCQW2 := utiltestingapi.MakeAdmission("cq-w").
		PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Assignment(corev1.ResourceCPU, "default", "2").Obj()).
		Obj()
	admOnDemand1 := utiltestingapi.MakeAdmission("cq-p2").
		PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Assignment(corev1.ResourceCPU, "on-demand", "1").Obj()).
		Obj()
	admOnDemand2 := utiltestingapi.MakeAdmission("cq-p2").
		PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Assignment(corev1.ResourceCPU, "on-demand", "2").Obj()).
		Obj()
	const (
		limitsInsufficientQuotaMsg = "couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor default, 1 more needed"
		limitsPendingPreemptionMsg = limitsInsufficientQuotaMsg + ". Pending the preemption of 1 workload(s)"
		limitsDeferredFitMsg       = "Workload's preemption targets exceed remaining preemption limits, but it will fit after earlier preemptions complete"
		p1PreemptMsg               = "Preempted by default/p1 because of preemption config cfg-one rule rule-one/0"
		p2PreemptMsg               = "Preempted by default/p2 because of preemption config cfg-two rule rule-two/0"
	)
	noReservationCond := metav1.Condition{
		Type:               kueue.WorkloadAdmitted,
		Status:             metav1.ConditionFalse,
		Reason:             kueue.WorkloadAdmittedReasonNoReservation,
		Message:            "The workload has no reservation",
		LastTransitionTime: metav1.NewTime(now),
	}
	cqP2QuotaReservedCond := metav1.Condition{
		Type:               kueue.WorkloadQuotaReserved,
		Status:             metav1.ConditionTrue,
		Reason:             "QuotaReserved",
		Message:            "Quota reserved in ClusterQueue cq-p2",
		LastTransitionTime: metav1.NewTime(now),
	}
	admittedCond := metav1.Condition{
		Type:               kueue.WorkloadAdmitted,
		Status:             metav1.ConditionTrue,
		Reason:             "Admitted",
		Message:            "The workload is admitted",
		LastTransitionTime: metav1.NewTime(now),
	}
	oneCPURequest := kueue.PodSetRequest{
		Name:      kueue.DefaultPodSetName,
		Resources: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")},
	}
	basePreemptorP1 := utiltestingapi.MakeWorkload("p1", "default").
		UID("p1").Queue("lq-p1").Request(corev1.ResourceCPU, "1").Creation(now.Add(-2 * time.Minute))
	wantP1Preempting := basePreemptorP1.Clone().
		Condition(metav1.Condition{
			Type:               kueue.WorkloadQuotaReserved,
			Status:             metav1.ConditionFalse,
			Reason:             kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads,
			Message:            limitsPendingPreemptionMsg,
			LastTransitionTime: metav1.NewTime(now),
		}).
		Condition(noReservationCond).
		ResourceRequests(oneCPURequest)
	basePreemptorP2 := utiltestingapi.MakeWorkload("p2", "default").
		UID("p2").Queue("lq-p2").Request(corev1.ResourceCPU, "1").Creation(now.Add(-time.Minute))
	baseV1 := utiltestingapi.MakeWorkload("v1", "default").
		UID("v1").Queue("lq-v").Label("group", "one").Request(corev1.ResourceCPU, "1").
		ReserveQuotaAt(admCQV1, now).AdmittedAt(true, now)
	wantV1Preempted := baseV1.Clone().
		Condition(metav1.Condition{
			Type:               kueue.WorkloadEvicted,
			Status:             metav1.ConditionTrue,
			Reason:             kueue.WorkloadEvictedByPreemption,
			Message:            p1PreemptMsg,
			LastTransitionTime: metav1.NewTime(now),
		}).
		Condition(metav1.Condition{
			Type:               kueue.WorkloadPreempted,
			Status:             metav1.ConditionTrue,
			Reason:             kueue.ConfigurablePreemptionReason,
			Message:            p1PreemptMsg,
			LastTransitionTime: metav1.NewTime(now),
		}).
		SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{
			Reason:          kueue.WorkloadEvictedByPreemption,
			UnderlyingCause: "cfg-one",
			Count:           1,
		})
	baseV2 := utiltestingapi.MakeWorkload("v2", "default").
		UID("v2").Queue("lq-v").Label("group", "two").Request(corev1.ResourceCPU, "1").
		ReserveQuotaAt(admCQV1, now).AdmittedAt(true, now)
	baseV3 := utiltestingapi.MakeWorkload("v3", "default").
		UID("v3").Queue("lq-w").Label("group", "two").Priority(1).Request(corev1.ResourceCPU, "1").
		ReserveQuotaAt(admCQW1, now).AdmittedAt(true, now)

	cases := map[string]scheduleTestCase{
		"with fair sharing: hierarchical nominal-first prefers non-borrowing leaf CQ": {
			// Admitted state:
			// - wl-noisy-admitted (cq-noisy): uses 3000 CPU (borrowed from cq-hero)
			// - wl-tiny-admitted (cq-tiny): uses 60 CPU (borrows 10 from root-cohort)
			// - wl-rest-admitted (cq-rest): uses 2995 CPU (borrowed from root-cohort)
			//
			// Pending:
			// - wl-hero (cq-hero): requests 2990 CPU (fits within its nominal 3000 CPU, so cq-hero is not borrowing)
			// - wl-tiny-pending (cq-tiny): requests 10 CPU (already borrowing, needs to borrow even more)
			//
			// wl-tiny-pending is considered first (Fair Sharing prioritization) and preempts
			// wl-rest-admitted. wl-hero has no nominal claim on wl-rest-admitted, because
			// parent-cohort-a (its AlmostLCA with cq-rest) is borrowing, so it reclaims
			// wl-noisy-admitted from cq-noisy without overlapping targets.
			enableFairSharing: true,
			cohorts:           defaultCohorts(),
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-noisy-admitted", "eng-alpha").
					UID("wl-noisy-admitted").
					Queue("lq-noisy").
					Request(corev1.ResourceCPU, "3000").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-noisy").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "3000").Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-tiny-admitted", "eng-alpha").
					UID("wl-tiny-admitted").
					Queue("lq-tiny").
					Request(corev1.ResourceCPU, "60").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-tiny").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "60").Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-rest-admitted", "eng-alpha").
					UID("wl-rest-admitted").
					Queue("lq-rest").
					Request(corev1.ResourceCPU, "2995").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-rest").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "2995").Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-hero", "eng-alpha").
					UID("wl-hero").
					JobUID("job-h-uid").
					Queue("lq-hero").
					Request(corev1.ResourceCPU, "2990").
					Creation(now.Add(time.Second)).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-tiny-pending", "eng-alpha").
					UID("wl-tiny-pending").
					JobUID("job-t-uid").
					Queue("lq-tiny").
					Request(corev1.ResourceCPU, "10").
					Creation(now).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-hero", "eng-alpha").
					UID("wl-hero").
					JobUID("job-h-uid").
					Queue("lq-hero").
					Request(corev1.ResourceCPU, "2990").
					Creation(now.Add(time.Second)).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads,
						Message:            "couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor default, 2990 more needed. Pending the preemption of 1 workload(s)",
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
							corev1.ResourceCPU: resource.MustParse("2990"),
						},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-noisy-admitted", "eng-alpha").
					UID("wl-noisy-admitted").
					Queue("lq-noisy").
					Request(corev1.ResourceCPU, "3000").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-noisy").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "3000").Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-hero, JobUID: job-h-uid) due to reclamation within the cohort; preemptor path: /root-cohort/parent-cohort-a/cq-hero; preemptee path: /root-cohort/parent-cohort-a/cq-noisy",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InCohortReclamation",
						Message:            "Preempted to accommodate a workload (UID: wl-hero, JobUID: job-h-uid) due to reclamation within the cohort; preemptor path: /root-cohort/parent-cohort-a/cq-hero; preemptee path: /root-cohort/parent-cohort-a/cq-noisy",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-rest-admitted", "eng-alpha").
					UID("wl-rest-admitted").
					Queue("lq-rest").
					Request(corev1.ResourceCPU, "2995").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-rest").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "2995").Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-tiny-pending, JobUID: job-t-uid) due to Fair Sharing within the cohort; preemptor path: /root-cohort/parent-cohort-b/cq-tiny; preemptee path: /root-cohort/parent-cohort-c/cq-rest",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InCohortFairSharing",
						Message:            "Preempted to accommodate a workload (UID: wl-tiny-pending, JobUID: job-t-uid) due to Fair Sharing within the cohort; preemptor path: /root-cohort/parent-cohort-b/cq-tiny; preemptee path: /root-cohort/parent-cohort-c/cq-rest",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-tiny-admitted", "eng-alpha").
					UID("wl-tiny-admitted").
					Queue("lq-tiny").
					Request(corev1.ResourceCPU, "60").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-tiny").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "60").Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-tiny-pending", "eng-alpha").
					UID("wl-tiny-pending").
					JobUID("job-t-uid").
					Queue("lq-tiny").
					Request(corev1.ResourceCPU, "10").
					Creation(now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads,
						Message:            "couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor default, 10 more needed. Pending the preemption of 1 workload(s)",
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
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/wl-noisy-admitted": *utiltestingapi.MakeAdmission("cq-noisy").PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Assignment(corev1.ResourceCPU, "default", "3000").Obj()).Obj(),
				"eng-alpha/wl-tiny-admitted":  *utiltestingapi.MakeAdmission("cq-tiny").PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Assignment(corev1.ResourceCPU, "default", "60").Obj()).Obj(),
				"eng-alpha/wl-rest-admitted":  *utiltestingapi.MakeAdmission("cq-rest").PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Assignment(corev1.ResourceCPU, "default", "2995").Obj()).Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"cq-hero": {"eng-alpha/wl-hero"},
				"cq-tiny": {"eng-alpha/wl-tiny-pending"},
			},
			wantPreemptionTargetRecomputations: map[string]map[string]int{
				"cq-hero": {"new_targets": 0},
			},
		},
		"with fair sharing: two workloads reclaim nominal quota; RecomputePreemptionTargetsUponOverlap enabled": {
			enableFairSharing: true,
			cohorts:           defaultCohorts(),
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-rest-admitted", "eng-alpha").
					UID("wl-rest-admitted").
					Queue("lq-rest").
					Request(corev1.ResourceCPU, "6055").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-rest").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "6055").Obj()).
						Obj(), now).
					Priority(0).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-hero", "eng-alpha").
					UID("wl-hero").
					JobUID("job-h-uid").
					Queue("lq-hero").
					Request(corev1.ResourceCPU, "2950").
					Creation(now.Add(time.Second)).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-tiny-pending", "eng-alpha").
					UID("wl-tiny-pending").
					JobUID("job-t-uid").
					Queue("lq-tiny").
					Request(corev1.ResourceCPU, "50").
					Creation(now).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-rest-pending", "eng-alpha").
					UID("wl-rest-pending").
					Queue("lq-rest").
					Request(corev1.ResourceCPU, "4000").
					Priority(10).
					Creation(now.Add(2 * time.Second)).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-hero", "eng-alpha").
					UID("wl-hero").
					JobUID("job-h-uid").
					Queue("lq-hero").
					Request(corev1.ResourceCPU, "2950").
					Creation(now.Add(time.Second)).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads,
						Message:            "Workload has overlapping preemption targets with another workload, but will fit after these preemptions complete",
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
							corev1.ResourceCPU: resource.MustParse("2950"),
						},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-rest-admitted", "eng-alpha").
					UID("wl-rest-admitted").
					Queue("lq-rest").
					Request(corev1.ResourceCPU, "6055").
					Priority(0).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-rest").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "6055").Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-tiny-pending, JobUID: job-t-uid) due to reclamation within the cohort; preemptor path: /root-cohort/parent-cohort-b/cq-tiny; preemptee path: /root-cohort/parent-cohort-c/cq-rest",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InCohortReclamation",
						Message:            "Preempted to accommodate a workload (UID: wl-tiny-pending, JobUID: job-t-uid) due to reclamation within the cohort; preemptor path: /root-cohort/parent-cohort-b/cq-tiny; preemptee path: /root-cohort/parent-cohort-c/cq-rest",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-rest-pending", "eng-alpha").
					UID("wl-rest-pending").
					Queue("lq-rest").
					Request(corev1.ResourceCPU, "4000").
					Creation(now.Add(2 * time.Second)).
					Priority(10).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonWaitingForQuota,
						Message:            "couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor default, 945 more needed",
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
							corev1.ResourceCPU: resource.MustParse("4000"),
						},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-tiny-pending", "eng-alpha").
					UID("wl-tiny-pending").
					JobUID("job-t-uid").
					Queue("lq-tiny").
					Request(corev1.ResourceCPU, "50").
					Creation(now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads,
						Message:            "couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor default, 50 more needed. Pending the preemption of 1 workload(s)",
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
							corev1.ResourceCPU: resource.MustParse("50"),
						},
					}).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/wl-rest-admitted": *utiltestingapi.MakeAdmission("cq-rest").PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Assignment(corev1.ResourceCPU, "default", "6055").Obj()).Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"cq-hero": {"eng-alpha/wl-hero"},
				"cq-tiny": {"eng-alpha/wl-tiny-pending"},
			},
			wantInadmissibleLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"cq-rest": {"eng-alpha/wl-rest-pending"},
			},
			// wl-hero overlapped with wl-tiny-pending on wl-rest-admitted, recomputed, but
			// can only fit after the earlier preemptions (from wl-tiny-pending) complete.
			wantPreemptionTargetRecomputations: map[string]map[string]int{
				"cq-hero": {"deferred_fit": 1},
			},
		},
		"two workloads reclaim nominal quota; RecomputePreemptionTargetsUponOverlap enabled": {
			enableFairSharing: false,
			cohorts:           defaultCohorts(),
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-rest-admitted", "eng-alpha").
					UID("wl-rest-admitted").
					Queue("lq-rest").
					Request(corev1.ResourceCPU, "6055").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-rest").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "6055").Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-hero", "eng-alpha").
					UID("wl-hero").
					JobUID("job-h-uid").
					Queue("lq-hero").
					Request(corev1.ResourceCPU, "2950").
					Creation(now.Add(time.Second)).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-tiny-pending", "eng-alpha").
					UID("wl-tiny-pending").
					JobUID("job-t-uid").
					Queue("lq-tiny").
					Request(corev1.ResourceCPU, "50").
					Creation(now).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-hero", "eng-alpha").
					UID("wl-hero").
					JobUID("job-h-uid").
					Queue("lq-hero").
					Request(corev1.ResourceCPU, "2950").
					Creation(now.Add(time.Second)).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads,
						Message:            "Workload has overlapping preemption targets with another workload, but will fit after these preemptions complete",
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
							corev1.ResourceCPU: resource.MustParse("2950"),
						},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-rest-admitted", "eng-alpha").
					UID("wl-rest-admitted").
					Queue("lq-rest").
					Request(corev1.ResourceCPU, "6055").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-rest").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "6055").Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-tiny-pending, JobUID: job-t-uid) due to reclamation within the cohort; preemptor path: /root-cohort/parent-cohort-b/cq-tiny; preemptee path: /root-cohort/parent-cohort-c/cq-rest",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InCohortReclamation",
						Message:            "Preempted to accommodate a workload (UID: wl-tiny-pending, JobUID: job-t-uid) due to reclamation within the cohort; preemptor path: /root-cohort/parent-cohort-b/cq-tiny; preemptee path: /root-cohort/parent-cohort-c/cq-rest",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-tiny-pending", "eng-alpha").
					UID("wl-tiny-pending").
					JobUID("job-t-uid").
					Queue("lq-tiny").
					Request(corev1.ResourceCPU, "50").
					Creation(now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads,
						Message:            "couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor default, 50 more needed. Pending the preemption of 1 workload(s)",
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
							corev1.ResourceCPU: resource.MustParse("50"),
						},
					}).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/wl-rest-admitted": *utiltestingapi.MakeAdmission("cq-rest").PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Assignment(corev1.ResourceCPU, "default", "6055").Obj()).Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"cq-hero": {"eng-alpha/wl-hero"},
				"cq-tiny": {"eng-alpha/wl-tiny-pending"},
			},
			// wl-hero overlapped with wl-tiny-pending on wl-rest-admitted, recomputed, but
			// can only fit after the earlier preemptions (from wl-tiny-pending) complete.
			wantPreemptionTargetRecomputations: map[string]map[string]int{
				"cq-hero": {"deferred_fit": 1},
			},
		},

		"flavor stickiness with RecomputePreemptionTargetsUponOverlap": {
			// Admitted state:
			// - wl-admitted-default (cq-1): uses 10 CPU on default flavor (borrowing 5)
			// - wl-admitted-on-demand (cq-1): uses 10 CPU on on-demand flavor (borrowing 5)
			//
			// Pending:
			// - wl-pending-high-prio-1 (cq-1): priority 11, requests 10 CPU (can fit by preempting its own admitted workloads)
			// - wl-pending-high-prio-2 (cq-2): priority 10, requests 10 CPU (needs to borrow, can fit by reclaiming from cq-1)
			//
			// Since cq-1 has much higher usage (20 CPU) than cq-2 (0 CPU), under Fair Sharing,
			// cq-2's pending workload (wl-pending-high-prio-2) is prioritized and processed first
			// despite having a lower priority than wl-pending-high-prio-1.
			//
			// wl-pending-high-prio-2 chooses to preempt wl-admitted-default on the default flavor.
			// When wl-pending-high-prio-1 is processed next, it is skipped due to overlapping targets
			// on the default flavor rather than violating flavor stickiness to switch to the on-demand flavor.
			enableFairSharing: true,
			cohorts: []kueue.Cohort{
				*utiltestingapi.MakeCohort("root").Obj(),
			},
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("cq-1").
					Cohort("root").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default").
							Resource(corev1.ResourceCPU, "5").Obj(),
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "5").Obj(),
					).
					Obj(),
				*utiltestingapi.MakeClusterQueue("cq-2").
					Cohort("root").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default").
							Resource(corev1.ResourceCPU, "5").Obj(),
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "5").Obj(),
					).
					Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltestingapi.MakeLocalQueue("lq-1", "default").ClusterQueue("cq-1").Obj(),
				*utiltestingapi.MakeLocalQueue("lq-2", "default").ClusterQueue("cq-2").Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-admitted-default", "default").
					UID("wl-admitted-default-uid").
					Queue("lq-1").
					Request(corev1.ResourceCPU, "10").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-1").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "10").Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-admitted-on-demand", "default").
					UID("wl-admitted-on-demand-uid").
					Queue("lq-1").
					Request(corev1.ResourceCPU, "10").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-1").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "on-demand", "10").Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-pending-high-prio-1", "default").
					UID("wl-pending-high-prio-1-uid").
					JobUID("job-high-prio-1-uid").
					Queue("lq-1").
					Priority(11).
					Request(corev1.ResourceCPU, "10").
					Creation(now).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-pending-high-prio-2", "default").
					UID("wl-pending-high-prio-2-uid").
					JobUID("job-high-prio-2-uid").
					Queue("lq-2").
					Priority(10).
					Request(corev1.ResourceCPU, "10").
					Creation(now.Add(time.Second)).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-admitted-default", "default").
					UID("wl-admitted-default-uid").
					Queue("lq-1").
					Request(corev1.ResourceCPU, "10").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-1").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "10").Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-pending-high-prio-2-uid, JobUID: job-high-prio-2-uid) due to Fair Sharing within the cohort; preemptor path: /root/cq-2; preemptee path: /root/cq-1",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InCohortFairSharing",
						Message:            "Preempted to accommodate a workload (UID: wl-pending-high-prio-2-uid, JobUID: job-high-prio-2-uid) due to Fair Sharing within the cohort; preemptor path: /root/cq-2; preemptee path: /root/cq-1",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-admitted-on-demand", "default").
					UID("wl-admitted-on-demand-uid").
					Queue("lq-1").
					Request(corev1.ResourceCPU, "10").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-1").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "on-demand", "10").Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-pending-high-prio-1", "default").
					UID("wl-pending-high-prio-1-uid").
					JobUID("job-high-prio-1-uid").
					Queue("lq-1").
					Priority(11).
					Request(corev1.ResourceCPU, "10").
					Creation(now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonWaitingForQuota,
						Message:            "couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor default, 10 more needed, skipping flavor on-demand as it is not found in the nomination mapping for resource cpu",
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
				*utiltestingapi.MakeWorkload("wl-pending-high-prio-2", "default").
					UID("wl-pending-high-prio-2-uid").
					JobUID("job-high-prio-2-uid").
					Queue("lq-2").
					Priority(10).
					Request(corev1.ResourceCPU, "10").
					Creation(now.Add(time.Second)).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads,
						Message:            "couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor default, 10 more needed, insufficient unused quota for cpu in flavor on-demand, 10 more needed. Pending the preemption of 1 workload(s)",
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
			wantAssignments: map[workload.Reference]kueue.Admission{
				"default/wl-admitted-default":   *utiltestingapi.MakeAdmission("cq-1").PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Assignment(corev1.ResourceCPU, "default", "10").Obj()).Obj(),
				"default/wl-admitted-on-demand": *utiltestingapi.MakeAdmission("cq-1").PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Assignment(corev1.ResourceCPU, "on-demand", "10").Obj()).Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"cq-2": {"default/wl-pending-high-prio-2"},
			},
			wantInadmissibleLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"cq-1": {"default/wl-pending-high-prio-1"},
			},
			// wl-pending-high-prio-1 overlapped on the default flavor with wl-pending-high-prio-2's
			// preemption target, recomputed with flavor stickiness enabled, but still couldn't
			// resolve the overlap — skipped.
			wantPreemptionTargetRecomputations: map[string]map[string]int{
				"cq-1": {"skipped": 1},
			},
		},

		"flavor stickiness with RecomputePreemptionTargetsUponOverlap and TAS disabled": {
			featureGates: map[featuregate.Feature]bool{
				features.RecomputeAssignmentUponPreemptionTargetsOverlap: true,
				features.TopologyAwareScheduling:                         false,
				features.TASRecomputeAssignmentWithinSchedulingCycle:     false,
			},
			// Admitted state:
			// - wl-admitted-default (cq-1): uses 10 CPU on default flavor (borrowing 5)
			// - wl-admitted-on-demand (cq-1): uses 10 CPU on on-demand flavor (borrowing 5)
			//
			// Pending:
			// - wl-pending-high-prio-1 (cq-1): priority 11, requests 10 CPU (can fit by preempting its own admitted workloads)
			// - wl-pending-high-prio-2 (cq-2): priority 10, requests 10 CPU (needs to borrow, can fit by reclaiming from cq-1)
			//
			// Since cq-1 has much higher usage (20 CPU) than cq-2 (0 CPU), under Fair Sharing,
			// cq-2's pending workload (wl-pending-high-prio-2) is prioritized and processed first
			// despite having a lower priority than wl-pending-high-prio-1.
			//
			// wl-pending-high-prio-2 chooses to preempt wl-admitted-default on the default flavor.
			// When wl-pending-high-prio-1 is processed next, it is skipped due to overlapping targets
			// on the default flavor rather than violating flavor stickiness to switch to the on-demand flavor.
			enableFairSharing: true,
			cohorts: []kueue.Cohort{
				*utiltestingapi.MakeCohort("root").Obj(),
			},
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("cq-1").
					Cohort("root").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default").
							Resource(corev1.ResourceCPU, "5").Obj(),
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "5").Obj(),
					).
					Obj(),
				*utiltestingapi.MakeClusterQueue("cq-2").
					Cohort("root").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default").
							Resource(corev1.ResourceCPU, "5").Obj(),
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "5").Obj(),
					).
					Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltestingapi.MakeLocalQueue("lq-1", "default").ClusterQueue("cq-1").Obj(),
				*utiltestingapi.MakeLocalQueue("lq-2", "default").ClusterQueue("cq-2").Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-admitted-default", "default").
					UID("wl-admitted-default-uid").
					Queue("lq-1").
					Request(corev1.ResourceCPU, "10").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-1").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "10").Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-admitted-on-demand", "default").
					UID("wl-admitted-on-demand-uid").
					Queue("lq-1").
					Request(corev1.ResourceCPU, "10").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-1").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "on-demand", "10").Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-pending-high-prio-1", "default").
					UID("wl-pending-high-prio-1-uid").
					JobUID("job-high-prio-1-uid").
					Queue("lq-1").
					Priority(11).
					Request(corev1.ResourceCPU, "10").
					Creation(now).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-pending-high-prio-2", "default").
					UID("wl-pending-high-prio-2-uid").
					JobUID("job-high-prio-2-uid").
					Queue("lq-2").
					Priority(10).
					Request(corev1.ResourceCPU, "10").
					Creation(now.Add(time.Second)).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-admitted-default", "default").
					UID("wl-admitted-default-uid").
					Queue("lq-1").
					Request(corev1.ResourceCPU, "10").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-1").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "10").Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-pending-high-prio-2-uid, JobUID: job-high-prio-2-uid) due to Fair Sharing within the cohort; preemptor path: /root/cq-2; preemptee path: /root/cq-1",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InCohortFairSharing",
						Message:            "Preempted to accommodate a workload (UID: wl-pending-high-prio-2-uid, JobUID: job-high-prio-2-uid) due to Fair Sharing within the cohort; preemptor path: /root/cq-2; preemptee path: /root/cq-1",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-admitted-on-demand", "default").
					UID("wl-admitted-on-demand-uid").
					Queue("lq-1").
					Request(corev1.ResourceCPU, "10").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-1").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "on-demand", "10").Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-pending-high-prio-1", "default").
					UID("wl-pending-high-prio-1-uid").
					JobUID("job-high-prio-1-uid").
					Queue("lq-1").
					Priority(11).
					Request(corev1.ResourceCPU, "10").
					Creation(now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonWaitingForQuota,
						Message:            "couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor default, 10 more needed, skipping flavor on-demand as it is not found in the nomination mapping for resource cpu",
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
				*utiltestingapi.MakeWorkload("wl-pending-high-prio-2", "default").
					UID("wl-pending-high-prio-2-uid").
					JobUID("job-high-prio-2-uid").
					Queue("lq-2").
					Priority(10).
					Request(corev1.ResourceCPU, "10").
					Creation(now.Add(time.Second)).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads,
						Message:            "couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor default, 10 more needed, insufficient unused quota for cpu in flavor on-demand, 10 more needed. Pending the preemption of 1 workload(s)",
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
			wantAssignments: map[workload.Reference]kueue.Admission{
				"default/wl-admitted-default":   *utiltestingapi.MakeAdmission("cq-1").PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Assignment(corev1.ResourceCPU, "default", "10").Obj()).Obj(),
				"default/wl-admitted-on-demand": *utiltestingapi.MakeAdmission("cq-1").PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Assignment(corev1.ResourceCPU, "on-demand", "10").Obj()).Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"cq-2": {"default/wl-pending-high-prio-2"},
			},
			wantInadmissibleLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"cq-1": {"default/wl-pending-high-prio-1"},
			},
		},

		"flavor stickiness with RecomputePreemptionTargetsUponOverlap and TAS recomputation disabled": {
			featureGates: map[featuregate.Feature]bool{
				features.RecomputeAssignmentUponPreemptionTargetsOverlap: true,
				features.TASRecomputeAssignmentWithinSchedulingCycle:     false,
			},
			enableFairSharing: true,
			cohorts: []kueue.Cohort{
				*utiltestingapi.MakeCohort("root").Obj(),
			},
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("cq-1").
					Cohort("root").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default").
							Resource(corev1.ResourceCPU, "5").Obj(),
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "5").Obj(),
					).
					Obj(),
				*utiltestingapi.MakeClusterQueue("cq-2").
					Cohort("root").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default").
							Resource(corev1.ResourceCPU, "5").Obj(),
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "5").Obj(),
					).
					Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltestingapi.MakeLocalQueue("lq-1", "default").ClusterQueue("cq-1").Obj(),
				*utiltestingapi.MakeLocalQueue("lq-2", "default").ClusterQueue("cq-2").Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-admitted-default", "default").
					UID("wl-admitted-default-uid").
					Queue("lq-1").
					Request(corev1.ResourceCPU, "10").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-1").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "10").Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-admitted-on-demand", "default").
					UID("wl-admitted-on-demand-uid").
					Queue("lq-1").
					Request(corev1.ResourceCPU, "10").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-1").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "on-demand", "10").Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-pending-high-prio-1", "default").
					UID("wl-pending-high-prio-1-uid").
					JobUID("job-high-prio-1-uid").
					Queue("lq-1").
					Priority(11).
					Request(corev1.ResourceCPU, "10").
					Creation(now).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-pending-high-prio-2", "default").
					UID("wl-pending-high-prio-2-uid").
					JobUID("job-high-prio-2-uid").
					Queue("lq-2").
					Priority(10).
					Request(corev1.ResourceCPU, "10").
					Creation(now.Add(time.Second)).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-admitted-default", "default").
					UID("wl-admitted-default-uid").
					Queue("lq-1").
					Request(corev1.ResourceCPU, "10").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-1").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "10").Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-pending-high-prio-2-uid, JobUID: job-high-prio-2-uid) due to Fair Sharing within the cohort; preemptor path: /root/cq-2; preemptee path: /root/cq-1",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InCohortFairSharing",
						Message:            "Preempted to accommodate a workload (UID: wl-pending-high-prio-2-uid, JobUID: job-high-prio-2-uid) due to Fair Sharing within the cohort; preemptor path: /root/cq-2; preemptee path: /root/cq-1",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-admitted-on-demand", "default").
					UID("wl-admitted-on-demand-uid").
					Queue("lq-1").
					Request(corev1.ResourceCPU, "10").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-1").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "on-demand", "10").Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-pending-high-prio-1", "default").
					UID("wl-pending-high-prio-1-uid").
					JobUID("job-high-prio-1-uid").
					Queue("lq-1").
					Priority(11).
					Request(corev1.ResourceCPU, "10").
					Creation(now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonWaitingForQuota,
						Message:            "couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor default, 10 more needed, skipping flavor on-demand as it is not found in the nomination mapping for resource cpu",
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
				*utiltestingapi.MakeWorkload("wl-pending-high-prio-2", "default").
					UID("wl-pending-high-prio-2-uid").
					JobUID("job-high-prio-2-uid").
					Queue("lq-2").
					Priority(10).
					Request(corev1.ResourceCPU, "10").
					Creation(now.Add(time.Second)).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads,
						Message:            "couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor default, 10 more needed, insufficient unused quota for cpu in flavor on-demand, 10 more needed. Pending the preemption of 1 workload(s)",
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
			wantAssignments: map[workload.Reference]kueue.Admission{
				"default/wl-admitted-default":   *utiltestingapi.MakeAdmission("cq-1").PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Assignment(corev1.ResourceCPU, "default", "10").Obj()).Obj(),
				"default/wl-admitted-on-demand": *utiltestingapi.MakeAdmission("cq-1").PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Assignment(corev1.ResourceCPU, "on-demand", "10").Obj()).Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"cq-2": {"default/wl-pending-high-prio-2"},
			},
			wantInadmissibleLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"cq-1": {"default/wl-pending-high-prio-1"},
			},
		},

		"legacy overlap skip with RecomputePreemptionTargetsUponOverlap disabled": {
			featureGates: map[featuregate.Feature]bool{
				features.RecomputeAssignmentUponPreemptionTargetsOverlap: false,
				features.TASRecomputeAssignmentWithinSchedulingCycle:     false,
			},
			enableFairSharing: true,
			cohorts: []kueue.Cohort{
				*utiltestingapi.MakeCohort("root").Obj(),
			},
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("cq-1").
					Cohort("root").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default").
							Resource(corev1.ResourceCPU, "5").Obj(),
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "5").Obj(),
					).
					Obj(),
				*utiltestingapi.MakeClusterQueue("cq-2").
					Cohort("root").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default").
							Resource(corev1.ResourceCPU, "5").Obj(),
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "5").Obj(),
					).
					Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltestingapi.MakeLocalQueue("lq-1", "default").ClusterQueue("cq-1").Obj(),
				*utiltestingapi.MakeLocalQueue("lq-2", "default").ClusterQueue("cq-2").Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-admitted-default", "default").
					UID("wl-admitted-default-uid").
					Queue("lq-1").
					Request(corev1.ResourceCPU, "10").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-1").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "10").Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-admitted-on-demand", "default").
					UID("wl-admitted-on-demand-uid").
					Queue("lq-1").
					Request(corev1.ResourceCPU, "10").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-1").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "on-demand", "10").Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-pending-high-prio-1", "default").
					UID("wl-pending-high-prio-1-uid").
					JobUID("job-high-prio-1-uid").
					Queue("lq-1").
					Priority(11).
					Request(corev1.ResourceCPU, "10").
					Creation(now).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-pending-high-prio-2", "default").
					UID("wl-pending-high-prio-2-uid").
					JobUID("job-high-prio-2-uid").
					Queue("lq-2").
					Priority(10).
					Request(corev1.ResourceCPU, "10").
					Creation(now.Add(time.Second)).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-admitted-default", "default").
					UID("wl-admitted-default-uid").
					Queue("lq-1").
					Request(corev1.ResourceCPU, "10").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-1").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "10").Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-pending-high-prio-2-uid, JobUID: job-high-prio-2-uid) due to Fair Sharing within the cohort; preemptor path: /root/cq-2; preemptee path: /root/cq-1",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InCohortFairSharing",
						Message:            "Preempted to accommodate a workload (UID: wl-pending-high-prio-2-uid, JobUID: job-high-prio-2-uid) due to Fair Sharing within the cohort; preemptor path: /root/cq-2; preemptee path: /root/cq-1",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-admitted-on-demand", "default").
					UID("wl-admitted-on-demand-uid").
					Queue("lq-1").
					Request(corev1.ResourceCPU, "10").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-1").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "on-demand", "10").Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-pending-high-prio-1", "default").
					UID("wl-pending-high-prio-1-uid").
					JobUID("job-high-prio-1-uid").
					Queue("lq-1").
					Priority(11).
					Request(corev1.ResourceCPU, "10").
					Creation(now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonWaitingForQuota,
						Message:            "Workload has overlapping preemption targets with another workload",
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
				*utiltestingapi.MakeWorkload("wl-pending-high-prio-2", "default").
					UID("wl-pending-high-prio-2-uid").
					JobUID("job-high-prio-2-uid").
					Queue("lq-2").
					Priority(10).
					Request(corev1.ResourceCPU, "10").
					Creation(now.Add(time.Second)).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads,
						Message:            "couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor default, 10 more needed, insufficient unused quota for cpu in flavor on-demand, 10 more needed. Pending the preemption of 1 workload(s)",
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
			wantAssignments: map[workload.Reference]kueue.Admission{
				"default/wl-admitted-default":   *utiltestingapi.MakeAdmission("cq-1").PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Assignment(corev1.ResourceCPU, "default", "10").Obj()).Obj(),
				"default/wl-admitted-on-demand": *utiltestingapi.MakeAdmission("cq-1").PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Assignment(corev1.ResourceCPU, "on-demand", "10").Obj()).Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"cq-1": {"default/wl-pending-high-prio-1"},
				"cq-2": {"default/wl-pending-high-prio-2"},
			},
			wantSkippedPreemptions: map[string]int{
				"cq-1": 1,
				"cq-2": 0,
			},
		},
		"sibling TAS flavor usage is updated for overlapping preemption targets": {
			featureGates: map[featuregate.Feature]bool{
				features.TASRecomputeAssignmentWithinSchedulingCycle:     true,
				features.RecomputeAssignmentUponPreemptionTargetsOverlap: true,
				features.TASCachingRemainingResources:                    true,
			},
			cohorts: []kueue.Cohort{
				*utiltestingapi.MakeCohort("tas-cohort").Obj(),
			},
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("preemptor-a").
					Cohort("tas-cohort").
					Preemption(kueue.ClusterQueuePreemption{ReclaimWithinCohort: kueue.PreemptionPolicyAny}).
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("flavor-a").
							Resource(corev1.ResourceCPU, "3").
							Obj(),
					).
					Obj(),
				*utiltestingapi.MakeClusterQueue("preemptor-b").
					Cohort("tas-cohort").
					Preemption(kueue.ClusterQueuePreemption{ReclaimWithinCohort: kueue.PreemptionPolicyAny}).
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("flavor-b").
							Resource(corev1.ResourceCPU, "3").
							Obj(),
					).
					Obj(),
				*utiltestingapi.MakeClusterQueue("overlapping").
					Cohort("tas-cohort").
					Preemption(kueue.ClusterQueuePreemption{ReclaimWithinCohort: kueue.PreemptionPolicyAny}).
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("flavor-b").
							Resource(corev1.ResourceCPU, "3").
							Obj(),
					).
					Obj(),
				*utiltestingapi.MakeClusterQueue("victim-a").
					Cohort("tas-cohort").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("flavor-a").
							ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("0").BorrowingLimit("3").Append().
							Obj(),
					).
					Obj(),
				*utiltestingapi.MakeClusterQueue("victim-b").
					Cohort("tas-cohort").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("flavor-b").
							ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("0").BorrowingLimit("5").Append().
							Obj(),
					).
					Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltestingapi.MakeLocalQueue("preemptor-a", "default").ClusterQueue("preemptor-a").Obj(),
				*utiltestingapi.MakeLocalQueue("preemptor-b", "default").ClusterQueue("preemptor-b").Obj(),
				*utiltestingapi.MakeLocalQueue("overlapping", "default").ClusterQueue("overlapping").Obj(),
				*utiltestingapi.MakeLocalQueue("victim-a", "default").ClusterQueue("victim-a").Obj(),
				*utiltestingapi.MakeLocalQueue("victim-b", "default").ClusterQueue("victim-b").Obj(),
			},
			additionalResourceFlavors: []kueue.ResourceFlavor{
				*utiltestingapi.MakeResourceFlavor("flavor-a").
					NodeLabel("flavor-a", "true").
					TopologyName("tas-single-level").
					Obj(),
				*utiltestingapi.MakeResourceFlavor("flavor-b").
					NodeLabel("flavor-b", "true").
					TopologyName("tas-single-level").
					Obj(),
			},
			topologies: []kueue.Topology{
				*utiltestingapi.MakeDefaultOneLevelTopology("tas-single-level"),
			},
			nodes: []corev1.Node{
				*testingnode.MakeNode("x1").
					Label("flavor-a", "true").
					Label("flavor-b", "true").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("5"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("y1").
					Label("flavor-b", "true").
					Label(corev1.LabelHostname, "y1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("5"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("z1").
					Label("flavor-a", "true").
					Label(corev1.LabelHostname, "z1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("5"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			// flavor-a selects x1/z1, flavor-b selects x1/y1, and x1 is shared.
			// The first two pending workloads preempt one victim in each flavor and
			// reserve z1 and y1. The third initially nominates y1 by targeting victim-b.
			// Once y1 is reserved, overlap recomputation temporarily removes both
			// victims and must observe victim-a's x1 removal through flavor-b.
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("victim-a", "default").
					UID("victim-a-uid").
					Queue("victim-a").
					Priority(1).
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						NodeSelector(map[string]string{corev1.LabelHostname: "x1"}).
						Request(corev1.ResourceCPU, "3").
						Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("victim-a").
							PodSets(utiltestingapi.MakePodSetAssignment("main").
								Assignment(corev1.ResourceCPU, "flavor-a", "3").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(),
						now,
					).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("victim-b", "default").
					UID("victim-b-uid").
					Queue("victim-b").
					Priority(1).
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						NodeSelector(map[string]string{corev1.LabelHostname: "y1"}).
						// Fills y1, leaving x1 as the only candidate for the overlapping Workload.
						Request(corev1.ResourceCPU, "5").
						Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("victim-b").
							PodSets(utiltestingapi.MakePodSetAssignment("main").
								Assignment(corev1.ResourceCPU, "flavor-b", "5").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"y1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(),
						now,
					).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("preemptor-a", "default").
					UID("preemptor-a-uid").
					JobUID("preemptor-a-job-uid").
					Queue("preemptor-a").
					Priority(30).
					Creation(now).
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						// Keeps preemptor-a off x1 so nothing but the overlapping Workload
						// can take the capacity victim-a frees there.
						NodeSelector(map[string]string{corev1.LabelHostname: "z1"}).
						Request(corev1.ResourceCPU, "3").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("preemptor-b", "default").
					UID("preemptor-b-uid").
					JobUID("preemptor-b-job-uid").
					Queue("preemptor-b").
					Priority(20).
					Creation(now.Add(time.Second)).
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						NodeSelector(map[string]string{corev1.LabelHostname: "y1"}).
						Request(corev1.ResourceCPU, "3").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("overlapping", "default").
					UID("overlapping-uid").
					JobUID("overlapping-job-uid").
					Queue("overlapping").
					Priority(10).
					Creation(now.Add(2 * time.Second)).
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "3").
						Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("overlapping", "default").
					UID("overlapping-uid").
					JobUID("overlapping-job-uid").
					Queue("overlapping").
					Priority(10).
					Creation(now.Add(2 * time.Second)).
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "3").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads,
						Message:            "Workload has overlapping preemption targets with another workload, but will fit after these preemptions complete",
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
							corev1.ResourceCPU: resource.MustParse("3"),
						},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("preemptor-a", "default").
					UID("preemptor-a-uid").
					JobUID("preemptor-a-job-uid").
					Queue("preemptor-a").
					Priority(30).
					Creation(now).
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						NodeSelector(map[string]string{corev1.LabelHostname: "z1"}).
						Request(corev1.ResourceCPU, "3").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads,
						Message:            "couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor flavor-a, 3 more needed. Pending the preemption of 1 workload(s)",
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
							corev1.ResourceCPU: resource.MustParse("3"),
						},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("preemptor-b", "default").
					UID("preemptor-b-uid").
					JobUID("preemptor-b-job-uid").
					Queue("preemptor-b").
					Priority(20).
					Creation(now.Add(time.Second)).
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						NodeSelector(map[string]string{corev1.LabelHostname: "y1"}).
						Request(corev1.ResourceCPU, "3").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads,
						Message:            "couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor flavor-b, 2 more needed. Pending the preemption of 1 workload(s)",
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
							corev1.ResourceCPU: resource.MustParse("3"),
						},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("victim-a", "default").
					UID("victim-a-uid").
					Queue("victim-a").
					Priority(1).
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						NodeSelector(map[string]string{corev1.LabelHostname: "x1"}).
						Request(corev1.ResourceCPU, "3").
						Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("victim-a").
							PodSets(utiltestingapi.MakePodSetAssignment("main").
								Assignment(corev1.ResourceCPU, "flavor-a", "3").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(),
						now,
					).
					AdmittedAt(true, now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: preemptor-a-uid, JobUID: preemptor-a-job-uid) due to reclamation within the cohort; preemptor path: /tas-cohort/preemptor-a; preemptee path: /tas-cohort/victim-a",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InCohortReclamation",
						Message:            "Preempted to accommodate a workload (UID: preemptor-a-uid, JobUID: preemptor-a-job-uid) due to reclamation within the cohort; preemptor path: /tas-cohort/preemptor-a; preemptee path: /tas-cohort/victim-a",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("victim-b", "default").
					UID("victim-b-uid").
					Queue("victim-b").
					Priority(1).
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						NodeSelector(map[string]string{corev1.LabelHostname: "y1"}).
						Request(corev1.ResourceCPU, "5").
						Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("victim-b").
							PodSets(utiltestingapi.MakePodSetAssignment("main").
								Assignment(corev1.ResourceCPU, "flavor-b", "5").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"y1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(),
						now,
					).
					AdmittedAt(true, now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: preemptor-b-uid, JobUID: preemptor-b-job-uid) due to reclamation within the cohort; preemptor path: /tas-cohort/preemptor-b; preemptee path: /tas-cohort/victim-b",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InCohortReclamation",
						Message:            "Preempted to accommodate a workload (UID: preemptor-b-uid, JobUID: preemptor-b-job-uid) due to reclamation within the cohort; preemptor path: /tas-cohort/preemptor-b; preemptee path: /tas-cohort/victim-b",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"default/victim-a": *utiltestingapi.MakeAdmission("victim-a").
					PodSets(utiltestingapi.MakePodSetAssignment("main").
						Assignment(corev1.ResourceCPU, "flavor-a", "3").
						TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
							Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
							Obj()).
						Obj()).
					Obj(),
				"default/victim-b": *utiltestingapi.MakeAdmission("victim-b").
					PodSets(utiltestingapi.MakePodSetAssignment("main").
						Assignment(corev1.ResourceCPU, "flavor-b", "5").
						TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
							Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"y1"}, 1).Obj()).
							Obj()).
						Obj()).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"overlapping": {"default/overlapping"},
				"preemptor-a": {"default/preemptor-a"},
				"preemptor-b": {"default/preemptor-b"},
			},
			// The recomputation only fits because preemptor-b's preemption of victim-b
			// frees y1, so the overlapping workload defers instead of picking new targets.
			wantPreemptionTargetRecomputations: map[string]map[string]int{
				"overlapping": {"deferred_fit": 1},
			},
		},
		// After the preemptions x takes 3 and grow 5, leaving room for l.
		"workload slice replacement deferred by overlapping targets: a later entry does not see the replaced slice twice": {
			featureGates: map[featuregate.Feature]bool{
				features.RecomputeAssignmentUponPreemptionTargetsOverlap: true,
			},
			additionalClusterQueues: sliceClusterQueues,
			additionalLocalQueues:   sliceLocalQueues,
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("v", "default").
					UID("v").
					JobUID("job-v").
					Queue("lq-v").
					Creation(now.Add(-10*time.Minute)).
					PodSets(*utiltestingapi.MakePodSet("one", 6).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-v").
						PodSets(utiltestingapi.MakePodSetAssignment("one").
							Assignment(corev1.ResourceCPU, "default", "6").
							Count(6).
							Obj()).
						Obj(), now.Add(-10*time.Minute)).
					Obj(),
				*utiltestingapi.MakeWorkload("old", "default").
					UID("old").
					JobUID("job-old").
					Queue("lq-a").
					Creation(now.Add(-9*time.Minute)).
					PodSets(*utiltestingapi.MakePodSet("one", 2).Request(corev1.ResourceCPU, "1").Obj()).
					Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-a").
						PodSets(utiltestingapi.MakePodSetAssignment("one").
							Assignment(corev1.ResourceCPU, "default", "2").
							Count(2).
							Obj()).
						Obj(), now.Add(-9*time.Minute)).
					Obj(),
				*utiltestingapi.MakeWorkload("x", "default").
					UID("x").
					JobUID("job-x").
					Queue("lq-x").
					Creation(now.Add(-3 * time.Minute)).
					PodSets(*utiltestingapi.MakePodSet("one", 3).Request(corev1.ResourceCPU, "1").Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("grow", "default").
					UID("grow").
					JobUID("job-grow").
					Queue("lq-a").
					Creation(now.Add(-2*time.Minute)).
					PodSets(*utiltestingapi.MakePodSet("one", 5).Request(corev1.ResourceCPU, "1").Obj()).
					Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					Annotation(workloadslicing.WorkloadSliceReplacementFor, "default/old").
					Obj(),
				*utiltestingapi.MakeWorkload("l", "default").
					UID("l").
					JobUID("job-l").
					Queue("lq-l").
					Creation(now.Add(-time.Minute)).
					PodSets(*utiltestingapi.MakePodSet("one", 2).Request(corev1.ResourceCPU, "1").Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("grow", "default").
					UID("grow").
					JobUID("job-grow").
					Queue("lq-a").
					Creation(now.Add(-2*time.Minute)).
					PodSets(*utiltestingapi.MakePodSet("one", 5).Request(corev1.ResourceCPU, "1").Obj()).
					Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					Annotation(workloadslicing.WorkloadSliceReplacementFor, "default/old").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads,
						Message:            sliceDeferredMsg,
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
						Name:      "one",
						Resources: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("5")},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("l", "default").
					UID("l").
					JobUID("job-l").
					Queue("lq-l").
					Creation(now.Add(-time.Minute)).
					PodSets(*utiltestingapi.MakePodSet("one", 2).Request(corev1.ResourceCPU, "1").Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "QuotaReserved",
						Message:            "Quota reserved in ClusterQueue cq-l",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionTrue,
						Reason:             "Admitted",
						Message:            "The workload is admitted",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Admission(utiltestingapi.MakeAdmission("cq-l").
						PodSets(utiltestingapi.MakePodSetAssignment("one").
							Assignment(corev1.ResourceCPU, "default", "2").
							Count(2).
							Obj()).
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("old", "default").
					UID("old").
					JobUID("job-old").
					Queue("lq-a").
					Creation(now.Add(-9*time.Minute)).
					PodSets(*utiltestingapi.MakePodSet("one", 2).Request(corev1.ResourceCPU, "1").Obj()).
					Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-a").
						PodSets(utiltestingapi.MakePodSetAssignment("one").
							Assignment(corev1.ResourceCPU, "default", "2").
							Count(2).
							Obj()).
						Obj(), now.Add(-9*time.Minute)).
					Obj(),
				*utiltestingapi.MakeWorkload("v", "default").
					UID("v").
					JobUID("job-v").
					Queue("lq-v").
					Creation(now.Add(-10*time.Minute)).
					PodSets(*utiltestingapi.MakePodSet("one", 6).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-v").
						PodSets(utiltestingapi.MakePodSetAssignment("one").
							Assignment(corev1.ResourceCPU, "default", "6").
							Count(6).
							Obj()).
						Obj(), now.Add(-10*time.Minute)).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: x, JobUID: job-x) due to reclamation within the cohort; preemptor path: /slice-cohort/cq-x; preemptee path: /slice-cohort/cq-v",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InCohortReclamation",
						Message:            "Preempted to accommodate a workload (UID: x, JobUID: job-x) due to reclamation within the cohort; preemptor path: /slice-cohort/cq-x; preemptee path: /slice-cohort/cq-v",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("x", "default").
					UID("x").
					JobUID("job-x").
					Queue("lq-x").
					Creation(now.Add(-3 * time.Minute)).
					PodSets(*utiltestingapi.MakePodSet("one", 3).Request(corev1.ResourceCPU, "1").Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads,
						Message:            "couldn't assign flavors to pod set one: insufficient unused quota for cpu in flavor default, 1 more needed. Pending the preemption of 1 workload(s)",
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
						Name:      "one",
						Resources: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("3")},
					}).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"default/v": *utiltestingapi.MakeAdmission("cq-v").
					PodSets(utiltestingapi.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "default", "6").
						Count(6).
						Obj()).
					Obj(),
				"default/old": *utiltestingapi.MakeAdmission("cq-a").
					PodSets(utiltestingapi.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "default", "2").
						Count(2).
						Obj()).
					Obj(),
				"default/l": *utiltestingapi.MakeAdmission("cq-l").
					PodSets(utiltestingapi.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "default", "2").
						Count(2).
						Obj()).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"cq-x": {"default/x"},
				"cq-a": {"default/grow"},
			},
			wantPreemptionTargetRecomputations: map[string]map[string]int{
				"cq-a": {"deferred_fit": 1},
			},
		},
		// After the preemptions x takes 4 and grow 5, leaving 1 for l.
		"workload slice replacement deferred by overlapping targets: a later entry sees the replacement's full quota": {
			featureGates: map[featuregate.Feature]bool{
				features.RecomputeAssignmentUponPreemptionTargetsOverlap: true,
			},
			additionalClusterQueues: sliceClusterQueues,
			additionalLocalQueues:   sliceLocalQueues,
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("v", "default").
					UID("v").
					JobUID("job-v").
					Queue("lq-v").
					Creation(now.Add(-10*time.Minute)).
					PodSets(*utiltestingapi.MakePodSet("one", 6).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-v").
						PodSets(utiltestingapi.MakePodSetAssignment("one").
							Assignment(corev1.ResourceCPU, "default", "6").
							Count(6).
							Obj()).
						Obj(), now.Add(-10*time.Minute)).
					Obj(),
				*utiltestingapi.MakeWorkload("old", "default").
					UID("old").
					JobUID("job-old").
					Queue("lq-a").
					Creation(now.Add(-9*time.Minute)).
					PodSets(*utiltestingapi.MakePodSet("one", 1).Request(corev1.ResourceCPU, "1").Obj()).
					Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-a").
						PodSets(utiltestingapi.MakePodSetAssignment("one").
							Assignment(corev1.ResourceCPU, "default", "1").
							Count(1).
							Obj()).
						Obj(), now.Add(-9*time.Minute)).
					Obj(),
				*utiltestingapi.MakeWorkload("x", "default").
					UID("x").
					JobUID("job-x").
					Queue("lq-x").
					Creation(now.Add(-3 * time.Minute)).
					PodSets(*utiltestingapi.MakePodSet("one", 4).Request(corev1.ResourceCPU, "1").Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("grow", "default").
					UID("grow").
					JobUID("job-grow").
					Queue("lq-a").
					Creation(now.Add(-2*time.Minute)).
					PodSets(*utiltestingapi.MakePodSet("one", 5).Request(corev1.ResourceCPU, "1").Obj()).
					Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					Annotation(workloadslicing.WorkloadSliceReplacementFor, "default/old").
					Obj(),
				*utiltestingapi.MakeWorkload("l", "default").
					UID("l").
					JobUID("job-l").
					Queue("lq-l").
					Creation(now.Add(-time.Minute)).
					PodSets(*utiltestingapi.MakePodSet("one", 2).Request(corev1.ResourceCPU, "1").Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("grow", "default").
					UID("grow").
					JobUID("job-grow").
					Queue("lq-a").
					Creation(now.Add(-2*time.Minute)).
					PodSets(*utiltestingapi.MakePodSet("one", 5).Request(corev1.ResourceCPU, "1").Obj()).
					Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					Annotation(workloadslicing.WorkloadSliceReplacementFor, "default/old").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads,
						Message:            sliceDeferredMsg,
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
						Name:      "one",
						Resources: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("5")},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("l", "default").
					UID("l").
					JobUID("job-l").
					Queue("lq-l").
					Creation(now.Add(-time.Minute)).
					PodSets(*utiltestingapi.MakePodSet("one", 2).Request(corev1.ResourceCPU, "1").Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonWaitingForQuota,
						Message:            "Workload no longer fits after processing another workload",
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
						Name:      "one",
						Resources: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("old", "default").
					UID("old").
					JobUID("job-old").
					Queue("lq-a").
					Creation(now.Add(-9*time.Minute)).
					PodSets(*utiltestingapi.MakePodSet("one", 1).Request(corev1.ResourceCPU, "1").Obj()).
					Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-a").
						PodSets(utiltestingapi.MakePodSetAssignment("one").
							Assignment(corev1.ResourceCPU, "default", "1").
							Count(1).
							Obj()).
						Obj(), now.Add(-9*time.Minute)).
					Obj(),
				*utiltestingapi.MakeWorkload("v", "default").
					UID("v").
					JobUID("job-v").
					Queue("lq-v").
					Creation(now.Add(-10*time.Minute)).
					PodSets(*utiltestingapi.MakePodSet("one", 6).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-v").
						PodSets(utiltestingapi.MakePodSetAssignment("one").
							Assignment(corev1.ResourceCPU, "default", "6").
							Count(6).
							Obj()).
						Obj(), now.Add(-10*time.Minute)).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: x, JobUID: job-x) due to reclamation within the cohort; preemptor path: /slice-cohort/cq-x; preemptee path: /slice-cohort/cq-v",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InCohortReclamation",
						Message:            "Preempted to accommodate a workload (UID: x, JobUID: job-x) due to reclamation within the cohort; preemptor path: /slice-cohort/cq-x; preemptee path: /slice-cohort/cq-v",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("x", "default").
					UID("x").
					JobUID("job-x").
					Queue("lq-x").
					Creation(now.Add(-3 * time.Minute)).
					PodSets(*utiltestingapi.MakePodSet("one", 4).Request(corev1.ResourceCPU, "1").Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads,
						Message:            "couldn't assign flavors to pod set one: insufficient unused quota for cpu in flavor default, 1 more needed. Pending the preemption of 1 workload(s)",
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
						Name:      "one",
						Resources: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4")},
					}).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"default/v": *utiltestingapi.MakeAdmission("cq-v").
					PodSets(utiltestingapi.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "default", "6").
						Count(6).
						Obj()).
					Obj(),
				"default/old": *utiltestingapi.MakeAdmission("cq-a").
					PodSets(utiltestingapi.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "default", "1").
						Count(1).
						Obj()).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"cq-x": {"default/x"},
				"cq-a": {"default/grow"},
				"cq-l": {"default/l"},
			},
			wantPreemptionTargetRecomputations: map[string]map[string]int{
				"cq-a": {"deferred_fit": 1},
			},
		},
		// p2 nominates v2 (lower priority than v3), but p1's preemption of v1 uses up cq-v's
		// limit, so p2 recomputes and preempts v3 in cq-w instead.
		"preemption limits: recompute selects a candidate in another ClusterQueue after an earlier preemption exhausts the limit": {
			featureGates:            limitsFeatureGates,
			additionalClusterQueues: limitsClusterQueues,
			additionalLocalQueues:   limitsLocalQueues,
			objects:                 []client.Object{cfgOne.DeepCopy(), cfgTwo.DeepCopy(), perPreemptedCQLimit.Clone().Obj()},
			workloads: []kueue.Workload{
				*basePreemptorP1.Clone().Obj(),
				*basePreemptorP2.Clone().Obj(),
				*baseV1.Clone().Obj(),
				*baseV2.Clone().Obj(),
				*baseV3.Clone().Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*wantP1Preempting.Clone().Obj(),
				*basePreemptorP2.Clone().
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads,
						Message:            limitsPendingPreemptionMsg,
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(noReservationCond).
					ResourceRequests(oneCPURequest).
					Obj(),
				*wantV1Preempted.Clone().Obj(),
				*baseV2.Clone().Obj(),
				*baseV3.Clone().
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             kueue.WorkloadEvictedByPreemption,
						Message:            p2PreemptMsg,
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             kueue.ConfigurablePreemptionReason,
						Message:            p2PreemptMsg,
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{
						Reason:          kueue.WorkloadEvictedByPreemption,
						UnderlyingCause: "cfg-two",
						Count:           1,
					}).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"default/v1": *admCQV1,
				"default/v2": *admCQV1,
				"default/v3": *admCQW1,
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"cq-p1": {"default/p1"},
				"cq-p2": {"default/p2"},
			},
			// Limits recompute does not report the overlap metric.
			wantPreemptionTargetRecomputations: map[string]map[string]int{
				"cq-p2": {"new_targets": 0},
			},
		},
		// Like the case above, but a global limit also blocks v3 in cq-w.
		"preemption limits: workload is requeued as inadmissible when an earlier preemption exhausts the limit for all candidates": {
			featureGates:            limitsFeatureGates,
			additionalClusterQueues: limitsClusterQueues,
			additionalLocalQueues:   limitsLocalQueues,
			objects: []client.Object{
				cfgOne.DeepCopy(), cfgTwo.DeepCopy(),
				utiltestingalpha.MakePreemptionLimit("global", kueuealpha.GlobalLimitAggregation).UID("global-uid").Limit(1).WindowSeconds(60).Obj(),
			},
			workloads: []kueue.Workload{
				*basePreemptorP1.Clone().Obj(),
				*basePreemptorP2.Clone().Obj(),
				*baseV1.Clone().Obj(),
				*baseV2.Clone().Obj(),
				*baseV3.Clone().Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*wantP1Preempting.Clone().Obj(),
				*basePreemptorP2.Clone().
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonWaitingForQuota,
						Message:            fmt.Sprintf("%s; preemptions blocked by PreemptionLimit global (aggregation Global) until %s", limitsInsufficientQuotaMsg, now.Add(60*time.Second).UTC().Format(time.RFC3339)),
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(noReservationCond).
					ResourceRequests(oneCPURequest).
					Obj(),
				*wantV1Preempted.Clone().Obj(),
				*baseV2.Clone().Obj(),
				*baseV3.Clone().Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"default/v1": *admCQV1,
				"default/v2": *admCQV1,
				"default/v3": *admCQW1,
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"cq-p1": {"default/p1"},
			},
			wantInadmissibleLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"cq-p2": {"default/p2"},
			},
			wantCohortRequeueDelays: map[kueue.CohortReference]time.Duration{
				"limits-cohort": 60 * time.Second,
			},
		},
		// p2 first settles on default, planning to preempt v2. After p1 uses up cq-v's
		// limit, p2's recompute considers every flavor again and fits on on-demand.
		"preemption limits: recompute falls back to another flavor when an earlier preemption exhausts the limit": {
			featureGates:            limitsFeatureGates,
			additionalClusterQueues: []kueue.ClusterQueue{cqP1, *cqP2FallbackBase.Clone().Obj(), cqV},
			additionalLocalQueues:   limitsLocalQueues,
			objects:                 []client.Object{cfgOne.DeepCopy(), cfgTwo.DeepCopy(), perPreemptedCQLimit.Clone().Obj()},
			workloads: []kueue.Workload{
				*basePreemptorP1.Clone().Obj(),
				*basePreemptorP2.Clone().Obj(),
				*baseV1.Clone().Obj(),
				*baseV2.Clone().Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*wantP1Preempting.Clone().Obj(),
				*basePreemptorP2.Clone().Condition(cqP2QuotaReservedCond).Condition(admittedCond).Admission(admOnDemand1).Obj(),
				*wantV1Preempted.Clone().Obj(),
				*baseV2.Clone().Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"default/p2": *admOnDemand1,
				"default/v1": *admCQV1,
				"default/v2": *admCQV1,
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"cq-p1": {"default/p1"},
			},
		},
		// v1 frees 2 CPUs while p1 needs 1. After p1 uses up cq-v's limit, p2 can't preempt v2,
		// so it waits to fit once v1 finishes terminating rather than switching to v3 in cq-w.
		"preemption limits: recompute defers a workload that fits only after earlier preemptions complete": {
			featureGates: limitsFeatureGates,
			additionalClusterQueues: []kueue.ClusterQueue{
				cqP1, cqV, cqW,
				*utiltestingapi.MakeClusterQueue("cq-p2").
					Cohort("limits-cohort").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "4").Obj()).
					Annotation(kueuealpha.PreemptionConfigNameAnnotation, "cfg-two").
					Obj(),
			},
			additionalLocalQueues: limitsLocalQueues,
			objects:               []client.Object{cfgOne.DeepCopy(), cfgTwo.DeepCopy(), perPreemptedCQLimit.Clone().Obj()},
			workloads: []kueue.Workload{
				*basePreemptorP1.Clone().Obj(),
				*basePreemptorP2.Clone().Obj(),
				*baseV1.Clone().Request(corev1.ResourceCPU, "2").Admission(admCQV2).Obj(),
				*baseV2.Clone().Obj(),
				*baseV3.Clone().Request(corev1.ResourceCPU, "2").Admission(admCQW2).Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*wantP1Preempting.Clone().Obj(),
				*basePreemptorP2.Clone().
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads,
						Message:            limitsDeferredFitMsg,
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(noReservationCond).
					ResourceRequests(oneCPURequest).
					Obj(),
				*wantV1Preempted.Clone().Request(corev1.ResourceCPU, "2").Admission(admCQV2).Obj(),
				*baseV2.Clone().Obj(),
				*baseV3.Clone().Request(corev1.ResourceCPU, "2").Admission(admCQW2).Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"default/v1": *admCQV2,
				"default/v2": *admCQV1,
				"default/v3": *admCQW2,
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"cq-p1": {"default/p1"},
				"cq-p2": {"default/p2"},
			},
		},
		// p1 and p2 share cfg-one, so both nominate v1. p2's overlap recompute stays on default,
		// where cq-v's used-up limit also blocks v2, and doesn't fall back to on-demand.
		"preemption limits: overlapping targets keep the nominated flavor even when an earlier preemption exhausts the limit": {
			featureGates:            limitsFeatureGates,
			additionalClusterQueues: []kueue.ClusterQueue{cqP1, *cqP2FallbackBase.Clone().Annotation(kueuealpha.PreemptionConfigNameAnnotation, "cfg-one").Obj(), cqV},
			additionalLocalQueues:   limitsLocalQueues,
			objects:                 []client.Object{cfgOne.DeepCopy(), perPreemptedCQLimit.Clone().Obj()},
			workloads: []kueue.Workload{
				*basePreemptorP1.Clone().Obj(),
				*basePreemptorP2.Clone().Obj(),
				*baseV1.Clone().Obj(),
				*baseV2.Clone().Label("group", "one").Priority(1).Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*wantP1Preempting.Clone().Obj(),
				*basePreemptorP2.Clone().
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonWaitingForQuota,
						Message:            fmt.Sprintf("%s, skipping flavor on-demand as it is not found in the nomination mapping for resource cpu; preemptions blocked by PreemptionLimit per-preempted-cq (aggregation ByPreempteeClusterQueue) until %s", limitsInsufficientQuotaMsg, now.Add(60*time.Second).UTC().Format(time.RFC3339)),
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(noReservationCond).
					ResourceRequests(oneCPURequest).
					Obj(),
				*wantV1Preempted.Clone().Obj(),
				*baseV2.Clone().Label("group", "one").Priority(1).Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"default/v1": *admCQV1,
				"default/v2": *admCQV1,
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"cq-p1": {"default/p1"},
			},
			wantInadmissibleLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"cq-p2": {"default/p2"},
			},
			wantCohortRequeueDelays: map[kueue.CohortReference]time.Duration{
				"limits-cohort": 60 * time.Second,
			},
			wantPreemptionTargetRecomputations: map[string]map[string]int{
				"cq-p2": {"skipped": 1},
			},
		},
		// e0 fits on on-demand, so refill brings in refilled, which plans to preempt v2 on default.
		// After p1 uses up cq-v's limit, refilled is recomputed and admitted on on-demand in this cycle.
		"preemption limits: refilled workload falls back to another flavor when an earlier preemption exhausts the limit": {
			enableFairSharing: true,
			featureGates: map[featuregate.Feature]bool{
				features.ConfigurablePreemptions:      true,
				features.ConfigurablePreemptionLimits: true,
				features.FairSharingRefill:            true,
			},
			additionalClusterQueues: []kueue.ClusterQueue{
				cqP1,
				*utiltestingapi.MakeClusterQueue("cq-p2").
					Cohort("limits-cohort").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "1").Obj(),
						*utiltestingapi.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "3").Obj(),
					).
					FlavorFungibility(kueue.FlavorFungibility{WhenCanBorrow: kueue.MayStopSearch, WhenCanPreempt: kueue.MayStopSearch}).
					Annotation(kueuealpha.PreemptionConfigNameAnnotation, "cfg-two").
					Obj(),
				cqV,
			},
			additionalLocalQueues: limitsLocalQueues,
			objects:               []client.Object{cfgOne.DeepCopy(), cfgTwo.DeepCopy(), perPreemptedCQLimit.Clone().Obj()},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("e0", "default").UID("e0").Queue("lq-p2").Request(corev1.ResourceCPU, "2").Creation(now.Add(-3 * time.Minute)).Obj(),
				*basePreemptorP1.Clone().Obj(),
				*utiltestingapi.MakeWorkload("refilled", "default").UID("refilled").Queue("lq-p2").Request(corev1.ResourceCPU, "1").Creation(now.Add(-time.Minute)).Obj(),
				*baseV1.Clone().Obj(),
				*baseV2.Clone().Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("e0", "default").UID("e0").Queue("lq-p2").Request(corev1.ResourceCPU, "2").Creation(now.Add(-3 * time.Minute)).
					Condition(cqP2QuotaReservedCond).Condition(admittedCond).Admission(admOnDemand2).Obj(),
				*wantP1Preempting.Clone().Obj(),
				*utiltestingapi.MakeWorkload("refilled", "default").UID("refilled").Queue("lq-p2").Request(corev1.ResourceCPU, "1").Creation(now.Add(-time.Minute)).
					Condition(cqP2QuotaReservedCond).Condition(admittedCond).Admission(admOnDemand1).Obj(),
				*wantV1Preempted.Clone().Obj(),
				*baseV2.Clone().Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"default/e0":       *admOnDemand2,
				"default/refilled": *admOnDemand1,
				"default/v1":       *admCQV1,
				"default/v2":       *admCQV1,
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"cq-p1": {"default/p1"},
			},
		},
	}

	runScheduleTestCases(t, scheduleTestConfig{
		queues:          queues,
		clusterQueues:   clusterQueues,
		resourceFlavors: resourceFlavors,
		fakeClock:       fakeClock,
	}, cases)
}
