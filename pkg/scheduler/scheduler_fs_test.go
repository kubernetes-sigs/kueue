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
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/component-base/metrics/testutil"
	testingclock "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	"sigs.k8s.io/kueue/pkg/util/routine"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestScheduleForFairSharing(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	fakeClock := testingclock.NewFakeClock(now)
	ignoreEventMessageCmpOpts := cmp.Options{cmpopts.IgnoreFields(utiltesting.EventRecord{}, "Message")}

	resourceFlavors := []*kueue.ResourceFlavor{
		utiltestingapi.MakeResourceFlavor("default").Obj(),
		utiltestingapi.MakeResourceFlavor("on-demand").Obj(),
		utiltestingapi.MakeResourceFlavor("spot").Obj(),
		utiltestingapi.MakeResourceFlavor("model-a").Obj(),
	}
	clusterQueues := []kueue.ClusterQueue{
		*utiltestingapi.MakeClusterQueue("sales").
			NamespaceSelector(&metav1.LabelSelector{
				MatchExpressions: []metav1.LabelSelectorRequirement{{
					Key:      "dep",
					Operator: metav1.LabelSelectorOpIn,
					Values:   []string{"sales"},
				}},
			}).
			QueueingStrategy(kueue.StrictFIFO).
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "50", "0").Obj()).
			Obj(),
		*utiltestingapi.MakeClusterQueue("eng-alpha").
			Cohort("eng").
			NamespaceSelector(&metav1.LabelSelector{
				MatchExpressions: []metav1.LabelSelectorRequirement{{
					Key:      "dep",
					Operator: metav1.LabelSelectorOpIn,
					Values:   []string{"eng"},
				}},
			}).
			QueueingStrategy(kueue.StrictFIFO).
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("on-demand").
					Resource(corev1.ResourceCPU, "50", "50").Obj(),
				*utiltestingapi.MakeFlavorQuotas("spot").
					Resource(corev1.ResourceCPU, "100", "0").Obj(),
			).
			Obj(),
		*utiltestingapi.MakeClusterQueue("eng-beta").
			Cohort("eng").
			NamespaceSelector(&metav1.LabelSelector{
				MatchExpressions: []metav1.LabelSelectorRequirement{{
					Key:      "dep",
					Operator: metav1.LabelSelectorOpIn,
					Values:   []string{"eng"},
				}},
			}).
			QueueingStrategy(kueue.StrictFIFO).
			Preemption(kueue.ClusterQueuePreemption{
				ReclaimWithinCohort: kueue.PreemptionPolicyAny,
				WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
			}).
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("on-demand").
					Resource(corev1.ResourceCPU, "50", "10").Obj(),
				*utiltestingapi.MakeFlavorQuotas("spot").
					Resource(corev1.ResourceCPU, "0", "100").Obj(),
			).
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("model-a").
					Resource("example.com/gpu", "20", "0").Obj(),
			).
			Obj(),
		*utiltestingapi.MakeClusterQueue("flavor-nonexistent-cq").
			QueueingStrategy(kueue.StrictFIFO).
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("nonexistent-flavor").
				Resource(corev1.ResourceCPU, "50").Obj()).
			Obj(),
		*utiltestingapi.MakeClusterQueue("lend-a").
			Cohort("lend").
			NamespaceSelector(&metav1.LabelSelector{
				MatchExpressions: []metav1.LabelSelectorRequirement{{
					Key:      "dep",
					Operator: metav1.LabelSelectorOpIn,
					Values:   []string{"lend"},
				}},
			}).
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "3", "", "2").Obj()).
			Obj(),
		*utiltestingapi.MakeClusterQueue("lend-b").
			Cohort("lend").
			NamespaceSelector(&metav1.LabelSelector{
				MatchExpressions: []metav1.LabelSelectorRequirement{{
					Key:      "dep",
					Operator: metav1.LabelSelectorOpIn,
					Values:   []string{"lend"},
				}},
			}).
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "2", "", "2").Obj()).
			Obj(),
	}
	queues := []kueue.LocalQueue{
		*utiltestingapi.MakeLocalQueue("main", "sales").ClusterQueue("sales").Obj(),
		*utiltestingapi.MakeLocalQueue("blocked", "sales").ClusterQueue("eng-alpha").Obj(),
		*utiltestingapi.MakeLocalQueue("main", "eng-alpha").ClusterQueue("eng-alpha").Obj(),
		*utiltestingapi.MakeLocalQueue("main", "eng-beta").ClusterQueue("eng-beta").Obj(),
		*utiltestingapi.MakeLocalQueue("flavor-nonexistent-queue", "sales").ClusterQueue("flavor-nonexistent-cq").Obj(),
		*utiltestingapi.MakeLocalQueue("cq-nonexistent-queue", "sales").ClusterQueue("nonexistent-cq").Obj(),
		*utiltestingapi.MakeLocalQueue("lend-a-queue", "lend").ClusterQueue("lend-a").Obj(),
		*utiltestingapi.MakeLocalQueue("lend-b-queue", "lend").ClusterQueue("lend-b").Obj(),
	}
	cases := map[string]struct {
		enableFairSharing bool

		workloads []kueue.Workload
		objects   []client.Object

		// additional*Queues can hold any extra queues needed by the tc
		additionalClusterQueues []kueue.ClusterQueue
		additionalLocalQueues   []kueue.LocalQueue

		cohorts []kueue.Cohort

		// wantAssignments is a summary of all the admissions in the cache after this cycle.
		wantAssignments map[workload.Reference]kueue.Admission
		// wantWorkloads is the subset of workloads that got admitted in this cycle.
		wantWorkloads []kueue.Workload
		// wantLeft is the workload keys that are left in the queues after this cycle.
		wantLeft map[kueue.ClusterQueueReference][]workload.Reference
		// wantInadmissibleLeft is the workload keys that are left in the inadmissible state after this cycle.
		wantInadmissibleLeft map[kueue.ClusterQueueReference][]workload.Reference
		// wantEvents ignored if empty, the Message is ignored (it contains the duration)
		wantEvents []utiltesting.EventRecord
		// eventCmpOpts are the cmp options to compare recorded events.
		eventCmpOpts cmp.Options

		wantSkippedPreemptions map[string]int
	}{
		"with fair sharing: schedule workload with lowest share first": {
			enableFairSharing: true,
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("eng-shared").
					Cohort("eng").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "10", "0").Obj(),
					).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("all_nominal", "eng-alpha").
					Queue("main").
					PodSets(*utiltestingapi.MakePodSet("one", 50).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("eng-alpha").PodSets(utiltestingapi.MakePodSetAssignment("one").Assignment(corev1.ResourceCPU, "on-demand", "50").Count(50).Obj()).Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("borrowing", "eng-beta").
					Queue("main").
					// Use half of shared quota.
					PodSets(*utiltestingapi.MakePodSet("one", 55).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("eng-beta").PodSets(utiltestingapi.MakePodSetAssignment("one").Assignment(corev1.ResourceCPU, "on-demand", "55").Count(55).Obj()).Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("older_new", "eng-beta").
					Queue("main").
					Creation(now.Add(-time.Minute)).
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("new", "eng-alpha").
					Queue("main").
					Creation(now).
					PodSets(*utiltestingapi.MakePodSet("one", 5).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("all_nominal", "eng-alpha").
					Queue("main").
					PodSets(*utiltestingapi.MakePodSet("one", 50).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("eng-alpha").PodSets(utiltestingapi.MakePodSetAssignment("one").Assignment(corev1.ResourceCPU, "on-demand", "50").Count(50).Obj()).Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("new", "eng-alpha").
					Queue("main").
					Creation(now).
					PodSets(*utiltestingapi.MakePodSet("one", 5).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "QuotaReserved",
						Message:            "Quota reserved in ClusterQueue eng-alpha",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionTrue,
						Reason:             "Admitted",
						Message:            "The workload is admitted",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Admission(
						utiltestingapi.MakeAdmission("eng-alpha").
							PodSets(
								utiltestingapi.MakePodSetAssignment("one").
									Assignment(corev1.ResourceCPU, "on-demand", "5").
									Count(5).
									Obj(),
							).
							Obj(),
					).
					Obj(),
				*utiltestingapi.MakeWorkload("borrowing", "eng-beta").
					Queue("main").
					// Use half of shared quota.
					PodSets(*utiltestingapi.MakePodSet("one", 55).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("eng-beta").PodSets(utiltestingapi.MakePodSetAssignment("one").Assignment(corev1.ResourceCPU, "on-demand", "55").Count(55).Obj()).Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("older_new", "eng-beta").
					Queue("main").
					Creation(now.Add(-time.Minute)).
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "Workload no longer fits after processing another workload",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "one",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("1"),
						},
					}).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/all_nominal": *utiltestingapi.MakeAdmission("eng-alpha").PodSets(utiltestingapi.MakePodSetAssignment("one").Assignment(corev1.ResourceCPU, "on-demand", "50").Count(50).Obj()).Obj(),
				"eng-beta/borrowing":    *utiltestingapi.MakeAdmission("eng-beta").PodSets(utiltestingapi.MakePodSetAssignment("one").Assignment(corev1.ResourceCPU, "on-demand", "55").Count(55).Obj()).Obj(),
				"eng-alpha/new":         *utiltestingapi.MakeAdmission("eng-alpha").PodSets(utiltestingapi.MakePodSetAssignment("one").Assignment(corev1.ResourceCPU, "on-demand", "5").Count(5).Obj()).Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"eng-beta": {"eng-beta/older_new"},
			},
		},
		"with fair sharing: nominal-first ordering admits workload fitting within nominal despite higher CQ DRS": {
			enableFairSharing: true,
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("fs-nom-a").
					Cohort("fs-nom").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "50", "0").Obj(),
					).
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("spot").
							Resource(corev1.ResourceCPU, "50", "50").Obj(),
					).
					Obj(),
				*utiltestingapi.MakeClusterQueue("fs-nom-b").
					Cohort("fs-nom").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "10", "50").Obj(),
					).
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("spot").
							Resource(corev1.ResourceCPU, "50", "0").Obj(),
					).
					Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltestingapi.MakeLocalQueue("fs-nom", "eng-alpha").ClusterQueue("fs-nom-a").Obj(),
				*utiltestingapi.MakeLocalQueue("fs-nom", "eng-beta").ClusterQueue("fs-nom-b").Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("spot-heavy", "eng-alpha").
					Queue("fs-nom").
					PodSets(*utiltestingapi.MakePodSet("one", 80).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("fs-nom-a").PodSets(
						utiltestingapi.MakePodSetAssignment("one").
							Assignment(corev1.ResourceCPU, "spot", "80").Count(80).Obj(),
					).Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("light-usage", "eng-beta").
					Queue("fs-nom").
					PodSets(*utiltestingapi.MakePodSet("one", 5).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("fs-nom-b").PodSets(
						utiltestingapi.MakePodSetAssignment("one").
							Assignment(corev1.ResourceCPU, "on-demand", "5").Count(5).Obj(),
					).Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("borrow-wl", "eng-beta").
					Queue("fs-nom").
					Creation(now.Add(-time.Minute)).
					PodSets(*utiltestingapi.MakePodSet("one", 15).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("nom-wl", "eng-alpha").
					Queue("fs-nom").
					Creation(now).
					PodSets(*utiltestingapi.MakePodSet("one", 45).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("nom-wl", "eng-alpha").
					Queue("fs-nom").
					Creation(now).
					PodSets(*utiltestingapi.MakePodSet("one", 45).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "QuotaReserved",
						Message:            "Quota reserved in ClusterQueue fs-nom-a",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionTrue,
						Reason:             "Admitted",
						Message:            "The workload is admitted",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Admission(
						utiltestingapi.MakeAdmission("fs-nom-a").
							PodSets(
								utiltestingapi.MakePodSetAssignment("one").
									Assignment(corev1.ResourceCPU, "on-demand", "45").
									Count(45).
									Obj(),
							).
							Obj(),
					).
					Obj(),
				*utiltestingapi.MakeWorkload("spot-heavy", "eng-alpha").
					Queue("fs-nom").
					PodSets(*utiltestingapi.MakePodSet("one", 80).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("fs-nom-a").PodSets(
						utiltestingapi.MakePodSetAssignment("one").
							Assignment(corev1.ResourceCPU, "spot", "80").Count(80).Obj(),
					).Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("borrow-wl", "eng-beta").
					Queue("fs-nom").
					Creation(now.Add(-time.Minute)).
					PodSets(*utiltestingapi.MakePodSet("one", 15).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "Workload no longer fits after processing another workload",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "one",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("15"),
						},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("light-usage", "eng-beta").
					Queue("fs-nom").
					PodSets(*utiltestingapi.MakePodSet("one", 5).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("fs-nom-b").PodSets(
						utiltestingapi.MakePodSetAssignment("one").
							Assignment(corev1.ResourceCPU, "on-demand", "5").Count(5).Obj(),
					).Obj(), now).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/spot-heavy": *utiltestingapi.MakeAdmission("fs-nom-a").PodSets(utiltestingapi.MakePodSetAssignment("one").Assignment(corev1.ResourceCPU, "spot", "80").Count(80).Obj()).Obj(),
				"eng-beta/light-usage": *utiltestingapi.MakeAdmission("fs-nom-b").PodSets(utiltestingapi.MakePodSetAssignment("one").Assignment(corev1.ResourceCPU, "on-demand", "5").Count(5).Obj()).Obj(),
				"eng-alpha/nom-wl":     *utiltestingapi.MakeAdmission("fs-nom-a").PodSets(utiltestingapi.MakePodSetAssignment("one").Assignment(corev1.ResourceCPU, "on-demand", "45").Count(45).Obj()).Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"fs-nom-b": {"eng-beta/borrow-wl"},
			},
		},
		//               ROOT
		//           /            \
		//   COHORT-A (0/10)    COHORT-B (10/5)
		//        |              /         \
		//    cq-a (0/0)    cq-b (0/5)  cq-c (10/0)
		//
		// Shown as (usage/subtreeQuota). After pending workloads:
		// wl-a (cq-a, 5 CPUs) -> COHORT-A  (5/10), not borrowing
		// wl-b (cq-b, 5 CPUs) -> COHORT-B (15/5),  borrowing
		//
		// At root, subtree-level borrowing prefers wl-a.
		"with fair sharing: hierarchical nominal-first prefers non-borrowing subtree": {
			enableFairSharing: true,
			cohorts: []kueue.Cohort{
				*utiltestingapi.MakeCohort("root").Obj(),
				*utiltestingapi.MakeCohort("Cohort-A").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "10").Obj(),
					).Parent("root").Obj(),
				*utiltestingapi.MakeCohort("Cohort-B").Parent("root").Obj(),
			},
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("cq-a").
					Cohort("Cohort-A").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "0").Obj(),
					).Obj(),
				*utiltestingapi.MakeClusterQueue("cq-b").
					Cohort("Cohort-B").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "5").Obj(),
					).Obj(),
				*utiltestingapi.MakeClusterQueue("cq-c").
					Cohort("Cohort-B").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "0").Obj(),
					).Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltestingapi.MakeLocalQueue("lq-a", "eng-alpha").ClusterQueue("cq-a").Obj(),
				*utiltestingapi.MakeLocalQueue("lq-b", "eng-alpha").ClusterQueue("cq-b").Obj(),
				*utiltestingapi.MakeLocalQueue("lq-c", "eng-alpha").ClusterQueue("cq-c").Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("c0", "eng-alpha").
					Queue("lq-c").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "10").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-c", "one").
						PodSets(utiltestingapi.MakePodSetAssignment("one").
							Assignment(corev1.ResourceCPU, "on-demand", "10").
							Obj()).
						Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-a", "eng-alpha").
					Queue("lq-a").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "5").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-b", "eng-alpha").
					Queue("lq-b").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "5").
						Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("c0", "eng-alpha").
					Queue("lq-c").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "10").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-c", "one").
						PodSets(utiltestingapi.MakePodSetAssignment("one").
							Assignment(corev1.ResourceCPU, "on-demand", "10").
							Obj()).
						Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-a", "eng-alpha").
					Queue("lq-a").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "5").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "QuotaReserved",
						Message:            "Quota reserved in ClusterQueue cq-a",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionTrue,
						Reason:             "Admitted",
						Message:            "The workload is admitted",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Admission(
						utiltestingapi.MakeAdmission("cq-a").
							PodSets(
								utiltestingapi.MakePodSetAssignment("one").
									Assignment(corev1.ResourceCPU, "on-demand", "5").
									Count(1).
									Obj(),
							).
							Obj(),
					).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-b", "eng-alpha").
					Queue("lq-b").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "5").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "Workload no longer fits after processing another workload",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "one",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("5"),
						},
					}).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/c0":   *utiltestingapi.MakeAdmission("cq-c").PodSets(utiltestingapi.MakePodSetAssignment("one").Assignment(corev1.ResourceCPU, "on-demand", "10").Obj()).Obj(),
				"eng-alpha/wl-a": *utiltestingapi.MakeAdmission("cq-a").PodSets(utiltestingapi.MakePodSetAssignment("one").Assignment(corev1.ResourceCPU, "on-demand", "5").Count(1).Obj()).Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"cq-b": {"eng-alpha/wl-b"},
			},
		},
		//                 ROOT
		//           /                \
		//   COHORT-A (0/10)      COHORT-B (10/5)
		//      |                  /         \
		//   SUB-A (0/10)      cq-b (0/5)  cq-c (10/0)
		//      |
		//   cq-a (0/0)
		//
		// Shown as (usage/subtreeQuota). After pending workloads:
		// wl-a (cq-a, 5 CPUs) -> SUB-A (5/10), COHORT-A  (5/10), not borrowing
		// wl-b (cq-b, 5 CPUs) -> COHORT-B (15/5),  borrowing
		//
		// At root, subtree-level borrowing prefers wl-a.
		"with fair sharing: hierarchical nominal-first with deeper nesting": {
			enableFairSharing: true,
			cohorts: []kueue.Cohort{
				*utiltestingapi.MakeCohort("root").Obj(),
				*utiltestingapi.MakeCohort("Cohort-A").Parent("root").Obj(),
				*utiltestingapi.MakeCohort("SUB-A").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "10").Obj(),
					).Parent("Cohort-A").Obj(),
				*utiltestingapi.MakeCohort("Cohort-B").Parent("root").Obj(),
			},
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("cq-a").
					Cohort("SUB-A").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "0").Obj(),
					).Obj(),
				*utiltestingapi.MakeClusterQueue("cq-b").
					Cohort("Cohort-B").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "5").Obj(),
					).Obj(),
				*utiltestingapi.MakeClusterQueue("cq-c").
					Cohort("Cohort-B").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "0").Obj(),
					).Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltestingapi.MakeLocalQueue("lq-a", "eng-alpha").ClusterQueue("cq-a").Obj(),
				*utiltestingapi.MakeLocalQueue("lq-b", "eng-alpha").ClusterQueue("cq-b").Obj(),
				*utiltestingapi.MakeLocalQueue("lq-c", "eng-alpha").ClusterQueue("cq-c").Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("c0", "eng-alpha").
					Queue("lq-c").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "10").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-c", "one").
						PodSets(utiltestingapi.MakePodSetAssignment("one").
							Assignment(corev1.ResourceCPU, "on-demand", "10").
							Obj()).
						Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-a", "eng-alpha").
					Queue("lq-a").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "5").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-b", "eng-alpha").
					Queue("lq-b").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "5").
						Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("c0", "eng-alpha").
					Queue("lq-c").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "10").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-c", "one").
						PodSets(utiltestingapi.MakePodSetAssignment("one").
							Assignment(corev1.ResourceCPU, "on-demand", "10").
							Obj()).
						Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-a", "eng-alpha").
					Queue("lq-a").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "5").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "QuotaReserved",
						Message:            "Quota reserved in ClusterQueue cq-a",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionTrue,
						Reason:             "Admitted",
						Message:            "The workload is admitted",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Admission(
						utiltestingapi.MakeAdmission("cq-a").
							PodSets(
								utiltestingapi.MakePodSetAssignment("one").
									Assignment(corev1.ResourceCPU, "on-demand", "5").
									Count(1).
									Obj(),
							).
							Obj(),
					).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-b", "eng-alpha").
					Queue("lq-b").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "5").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "Workload no longer fits after processing another workload",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "one",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("5"),
						},
					}).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/c0":   *utiltestingapi.MakeAdmission("cq-c").PodSets(utiltestingapi.MakePodSetAssignment("one").Assignment(corev1.ResourceCPU, "on-demand", "10").Obj()).Obj(),
				"eng-alpha/wl-a": *utiltestingapi.MakeAdmission("cq-a").PodSets(utiltestingapi.MakePodSetAssignment("one").Assignment(corev1.ResourceCPU, "on-demand", "5").Count(1).Obj()).Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"cq-b": {"eng-alpha/wl-b"},
			},
		},
		//                  ROOT
		//            /            \
		//   COHORT-A (6/18)      COHORT-B (20/18)
		//     /       \             /        \
		// cq-a1 (0/5) cq-a2 (6/3) cq-b (0/5) cq-c (20/3)
		//
		// Shown as (usage/subtreeQuota). SubtreeQuota: 10+5+3=18 each.
		// After pending workloads:
		// wl-a1 (cq-a1, 7 CPUs) -> COHORT-A (13/18), not borrowing
		// wl-b  (cq-b,  4 CPUs) -> COHORT-B (24/18), borrowing
		//
		// At root, subtree-level borrowing prefers wl-a1.
		"with fair sharing: hierarchical nominal-first with mixed CQ and cohort quotas": {
			enableFairSharing: true,
			cohorts: []kueue.Cohort{
				*utiltestingapi.MakeCohort("root").Obj(),
				*utiltestingapi.MakeCohort("Cohort-A").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "10").Obj(),
					).Parent("root").Obj(),
				*utiltestingapi.MakeCohort("Cohort-B").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "10").Obj(),
					).Parent("root").Obj(),
			},
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("cq-a1").
					Cohort("Cohort-A").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "5").Obj(),
					).Obj(),
				*utiltestingapi.MakeClusterQueue("cq-a2").
					Cohort("Cohort-A").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "3").Obj(),
					).Obj(),
				*utiltestingapi.MakeClusterQueue("cq-b").
					Cohort("Cohort-B").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "5").Obj(),
					).Obj(),
				*utiltestingapi.MakeClusterQueue("cq-c").
					Cohort("Cohort-B").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "3").Obj(),
					).Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltestingapi.MakeLocalQueue("lq-a1", "eng-alpha").ClusterQueue("cq-a1").Obj(),
				*utiltestingapi.MakeLocalQueue("lq-a2", "eng-alpha").ClusterQueue("cq-a2").Obj(),
				*utiltestingapi.MakeLocalQueue("lq-b", "eng-alpha").ClusterQueue("cq-b").Obj(),
				*utiltestingapi.MakeLocalQueue("lq-c", "eng-alpha").ClusterQueue("cq-c").Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a2-admitted", "eng-alpha").
					Queue("lq-a2").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "6").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-a2", "one").
						PodSets(utiltestingapi.MakePodSetAssignment("one").
							Assignment(corev1.ResourceCPU, "on-demand", "6").
							Obj()).
						Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("c-admitted", "eng-alpha").
					Queue("lq-c").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "20").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-c", "one").
						PodSets(utiltestingapi.MakePodSetAssignment("one").
							Assignment(corev1.ResourceCPU, "on-demand", "20").
							Obj()).
						Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-a1", "eng-alpha").
					Queue("lq-a1").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "7").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-b", "eng-alpha").
					Queue("lq-b").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "4").
						Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a2-admitted", "eng-alpha").
					Queue("lq-a2").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "6").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-a2", "one").
						PodSets(utiltestingapi.MakePodSetAssignment("one").
							Assignment(corev1.ResourceCPU, "on-demand", "6").
							Obj()).
						Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("c-admitted", "eng-alpha").
					Queue("lq-c").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "20").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-c", "one").
						PodSets(utiltestingapi.MakePodSetAssignment("one").
							Assignment(corev1.ResourceCPU, "on-demand", "20").
							Obj()).
						Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-a1", "eng-alpha").
					Queue("lq-a1").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "7").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "QuotaReserved",
						Message:            "Quota reserved in ClusterQueue cq-a1",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionTrue,
						Reason:             "Admitted",
						Message:            "The workload is admitted",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Admission(
						utiltestingapi.MakeAdmission("cq-a1").
							PodSets(
								utiltestingapi.MakePodSetAssignment("one").
									Assignment(corev1.ResourceCPU, "on-demand", "7").
									Count(1).
									Obj(),
							).
							Obj(),
					).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-b", "eng-alpha").
					Queue("lq-b").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "4").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "Workload no longer fits after processing another workload",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "one",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("4"),
						},
					}).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/a2-admitted": *utiltestingapi.MakeAdmission("cq-a2").PodSets(utiltestingapi.MakePodSetAssignment("one").Assignment(corev1.ResourceCPU, "on-demand", "6").Obj()).Obj(),
				"eng-alpha/c-admitted":  *utiltestingapi.MakeAdmission("cq-c").PodSets(utiltestingapi.MakePodSetAssignment("one").Assignment(corev1.ResourceCPU, "on-demand", "20").Obj()).Obj(),
				"eng-alpha/wl-a1":       *utiltestingapi.MakeAdmission("cq-a1").PodSets(utiltestingapi.MakePodSetAssignment("one").Assignment(corev1.ResourceCPU, "on-demand", "7").Count(1).Obj()).Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"cq-b": {"eng-alpha/wl-b"},
			},
		},
		//                     ROOT
		//            /                    \
		//     COHORT-A                   COHORT-B
		//     on-demand: (0/10)          on-demand: (8/5)
		//     spot:      (5/0)           spot:      (0/10)
		//         |                          |
		//       cq-a                       cq-b
		//       on-demand: (0/0)           on-demand: (8/5)
		//       spot:      (5/0)           spot:      (0/0)
		//       admitted: 5 spot           admitted: 8 on-demand
		//
		// Shown as (usage/subtreeQuota). Both cohorts borrow from
		// ROOT, but on different flavors.
		// After pending workloads (both request on-demand):
		// wl-a (cq-a, 4 on-demand) -> COHORT-A on-demand (4/10), not borrowing
		// wl-b (cq-b, 4 on-demand) -> COHORT-B on-demand (12/5), borrowing
		//
		// Per-flavor gate prefers wl-a: COHORT-A is not borrowing
		// on-demand despite borrowing spot. Only 7 on-demand remain,
		// so after wl-a is admitted wl-b cannot fit.
		"with fair sharing: hierarchical nominal-first per-flavor ignores cross-flavor borrowing": {
			enableFairSharing: true,
			cohorts: []kueue.Cohort{
				*utiltestingapi.MakeCohort("root").Obj(),
				*utiltestingapi.MakeCohort("Cohort-A").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "10").Obj(),
					).Parent("root").Obj(),
				*utiltestingapi.MakeCohort("Cohort-B").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("spot").
							Resource(corev1.ResourceCPU, "10").Obj(),
					).Parent("root").Obj(),
			},
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("cq-a").
					Cohort("Cohort-A").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "0").Obj(),
						*utiltestingapi.MakeFlavorQuotas("spot").
							Resource(corev1.ResourceCPU, "0").Obj(),
					).Obj(),
				*utiltestingapi.MakeClusterQueue("cq-b").
					Cohort("Cohort-B").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "5").Obj(),
						*utiltestingapi.MakeFlavorQuotas("spot").
							Resource(corev1.ResourceCPU, "0").Obj(),
					).Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltestingapi.MakeLocalQueue("lq-a", "eng-alpha").ClusterQueue("cq-a").Obj(),
				*utiltestingapi.MakeLocalQueue("lq-b", "eng-alpha").ClusterQueue("cq-b").Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("spot-admitted", "eng-alpha").
					Queue("lq-a").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "5").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-a", "one").
						PodSets(utiltestingapi.MakePodSetAssignment("one").
							Assignment(corev1.ResourceCPU, "spot", "5").
							Obj()).
						Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("od-admitted", "eng-alpha").
					Queue("lq-b").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "8").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-b", "one").
						PodSets(utiltestingapi.MakePodSetAssignment("one").
							Assignment(corev1.ResourceCPU, "on-demand", "8").
							Obj()).
						Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-a", "eng-alpha").
					Queue("lq-a").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "4").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-b", "eng-alpha").
					Queue("lq-b").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "4").
						Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("od-admitted", "eng-alpha").
					Queue("lq-b").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "8").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-b", "one").
						PodSets(utiltestingapi.MakePodSetAssignment("one").
							Assignment(corev1.ResourceCPU, "on-demand", "8").
							Obj()).
						Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("spot-admitted", "eng-alpha").
					Queue("lq-a").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "5").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-a", "one").
						PodSets(utiltestingapi.MakePodSetAssignment("one").
							Assignment(corev1.ResourceCPU, "spot", "5").
							Obj()).
						Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-a", "eng-alpha").
					Queue("lq-a").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "4").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "QuotaReserved",
						Message:            "Quota reserved in ClusterQueue cq-a",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionTrue,
						Reason:             "Admitted",
						Message:            "The workload is admitted",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Admission(
						utiltestingapi.MakeAdmission("cq-a").
							PodSets(
								utiltestingapi.MakePodSetAssignment("one").
									Assignment(corev1.ResourceCPU, "on-demand", "4").
									Count(1).
									Obj(),
							).
							Obj(),
					).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-b", "eng-alpha").
					Queue("lq-b").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "4").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "Workload no longer fits after processing another workload",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "one",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("4"),
						},
					}).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/spot-admitted": *utiltestingapi.MakeAdmission("cq-a").PodSets(utiltestingapi.MakePodSetAssignment("one").Assignment(corev1.ResourceCPU, "spot", "5").Obj()).Obj(),
				"eng-alpha/od-admitted":   *utiltestingapi.MakeAdmission("cq-b").PodSets(utiltestingapi.MakePodSetAssignment("one").Assignment(corev1.ResourceCPU, "on-demand", "8").Obj()).Obj(),
				"eng-alpha/wl-a":          *utiltestingapi.MakeAdmission("cq-a").PodSets(utiltestingapi.MakePodSetAssignment("one").Assignment(corev1.ResourceCPU, "on-demand", "4").Count(1).Obj()).Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"cq-b": {"eng-alpha/wl-b"},
			},
		},
		// Cohort A provides 200 capacity, with 70 remaining.
		// We denote Cohorts in UPPERCASE, and ClusterQueues
		// in lowercase.
		//
		//            A
		//        /       \
		//       /          \
		//     B(30)         C(100)
		//    /    \        /  \
		//   d(10) e(20)   f(0)  g(100)
		//
		// In (), we display current admissions.  These
		// numbers are proportional to DominantResourceShare,
		// which we call below pDRS.
		//
		// pending workloads -> resulting pDRS if admitted.
		// d1: 70 -> d(80) , B(100)
		// e1: 61 -> e(81) , B(91)
		// f1:  1 -> f(1)  , C(101)
		// g1:  1 -> g(101), C(101)
		//
		// We expect d1 to admit, since after its admission B
		// has lower pDRS (100) than C (101) after admission
		// of either f1 or g1.
		//
		// Though admission of e1 would result in an even
		// lower pDRS of B (91), d1 won the tournament at the
		// lower level, which we see by comparing d and e's
		// pDRSs, 80 and 81 respectively, after admission of
		// d1 and e1 respectively.
		"hierarchical fair sharing schedule workload which wins tournament": {
			enableFairSharing: true,
			cohorts: []kueue.Cohort{
				*utiltestingapi.MakeCohort("A").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "200").Obj(),
					).Obj(),
				*utiltestingapi.MakeCohort("B").Parent("A").Obj(),
				*utiltestingapi.MakeCohort("C").Parent("A").Obj(),
			},
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("d").
					Cohort("B").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "0").Obj(),
					).Obj(),
				*utiltestingapi.MakeClusterQueue("e").
					Cohort("B").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "0").Obj(),
					).Obj(),
				*utiltestingapi.MakeClusterQueue("f").
					Cohort("C").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "0").Obj(),
					).Obj(),
				*utiltestingapi.MakeClusterQueue("g").
					Cohort("C").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "0").Obj(),
					).Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltestingapi.MakeLocalQueue("lq-d", "eng-alpha").ClusterQueue("d").Obj(),
				*utiltestingapi.MakeLocalQueue("lq-e", "eng-alpha").ClusterQueue("e").Obj(),
				*utiltestingapi.MakeLocalQueue("lq-f", "eng-alpha").ClusterQueue("f").Obj(),
				*utiltestingapi.MakeLocalQueue("lq-g", "eng-alpha").ClusterQueue("g").Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("d0", "eng-alpha").
					Queue("lq-d").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "10").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("d", "one").
						PodSets(utiltestingapi.MakePodSetAssignment("one").
							Assignment(corev1.ResourceCPU, "on-demand", "10").
							Obj()).
						Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("e0", "eng-alpha").
					Queue("lq-e").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "20").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("e", "one").
						PodSets(utiltestingapi.MakePodSetAssignment("one").
							Assignment(corev1.ResourceCPU, "on-demand", "20").
							Obj()).
						Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("g0", "eng-alpha").
					Queue("lq-g").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "100").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("g", "one").
						PodSets(utiltestingapi.MakePodSetAssignment("one").
							Assignment(corev1.ResourceCPU, "on-demand", "100").
							Obj()).
						Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("d1", "eng-alpha").
					Queue("lq-d").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "70").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("e1", "eng-alpha").
					Queue("lq-e").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "61").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("f1", "eng-alpha").
					Queue("lq-f").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("g1", "eng-alpha").
					Queue("lq-g").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("d0", "eng-alpha").
					Queue("lq-d").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "10").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("d", "one").
						PodSets(utiltestingapi.MakePodSetAssignment("one").
							Assignment(corev1.ResourceCPU, "on-demand", "10").
							Obj()).
						Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("d1", "eng-alpha").
					Queue("lq-d").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "70").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "QuotaReserved",
						Message:            "Quota reserved in ClusterQueue d",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionTrue,
						Reason:             "Admitted",
						Message:            "The workload is admitted",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Admission(
						utiltestingapi.MakeAdmission("d").
							PodSets(
								utiltestingapi.MakePodSetAssignment("one").
									Assignment(corev1.ResourceCPU, "on-demand", "70").
									Count(1).
									Obj(),
							).
							Obj(),
					).
					Obj(),
				*utiltestingapi.MakeWorkload("e0", "eng-alpha").
					Queue("lq-e").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "20").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("e", "one").
						PodSets(utiltestingapi.MakePodSetAssignment("one").
							Assignment(corev1.ResourceCPU, "on-demand", "20").
							Obj()).
						Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("e1", "eng-alpha").
					Queue("lq-e").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "61").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "Workload no longer fits after processing another workload",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "one",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("61"),
						},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("f1", "eng-alpha").
					Queue("lq-f").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "Workload no longer fits after processing another workload",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "one",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("1"),
						},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("g0", "eng-alpha").
					Queue("lq-g").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "100").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("g", "one").
						PodSets(utiltestingapi.MakePodSetAssignment("one").
							Assignment(corev1.ResourceCPU, "on-demand", "100").
							Obj()).
						Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("g1", "eng-alpha").
					Queue("lq-g").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "Workload no longer fits after processing another workload",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "one",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("1"),
						},
					}).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/d0": *utiltestingapi.MakeAdmission("d", "one").
					PodSets(utiltestingapi.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "on-demand", "10").
						Obj()).
					Obj(),
				"eng-alpha/e0": *utiltestingapi.MakeAdmission("e", "one").
					PodSets(utiltestingapi.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "on-demand", "20").
						Obj()).
					Obj(),
				"eng-alpha/g0": *utiltestingapi.MakeAdmission("g", "one").
					PodSets(utiltestingapi.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "on-demand", "100").
						Obj()).
					Obj(),
				"eng-alpha/d1": *utiltestingapi.MakeAdmission("d", "one").
					PodSets(utiltestingapi.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "on-demand", "70").
						Obj()).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"e": {"eng-alpha/e1"},
				"g": {"eng-alpha/g1"},
				"f": {"eng-alpha/f1"},
			},
		},
		// b0 is already admitted, using 10 capacity.
		// b1 - 50 capacity, and c1 - 75 capacity are pending.
		//
		// we expect b1 to schedule, as b0 + b1 = 60, is less than
		// c1 = 75.
		"fair sharing schedule workload with lowest drf after admission": {
			enableFairSharing: true,
			cohorts: []kueue.Cohort{
				*utiltestingapi.MakeCohort("A").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "100").Obj(),
					).Obj(),
			},
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("b").
					Cohort("A").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "0").Obj(),
					).Obj(),
				*utiltestingapi.MakeClusterQueue("c").
					Cohort("A").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "0").Obj(),
					).Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltestingapi.MakeLocalQueue("lq-b", "eng-alpha").ClusterQueue("b").Obj(),
				*utiltestingapi.MakeLocalQueue("lq-c", "eng-alpha").ClusterQueue("c").Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("b0", "eng-alpha").
					Queue("lq-b").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "10").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("b", "one").
						PodSets(utiltestingapi.MakePodSetAssignment("one").
							Assignment(corev1.ResourceCPU, "on-demand", "10").
							Obj()).
						Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("b1", "eng-alpha").
					Queue("lq-b").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "50").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("c1", "eng-alpha").
					Queue("lq-c").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "75").
						Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("b0", "eng-alpha").
					Queue("lq-b").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "10").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("b", "one").
						PodSets(utiltestingapi.MakePodSetAssignment("one").
							Assignment(corev1.ResourceCPU, "on-demand", "10").
							Obj()).
						Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("b1", "eng-alpha").
					Queue("lq-b").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "50").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "QuotaReserved",
						Message:            "Quota reserved in ClusterQueue b",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionTrue,
						Reason:             "Admitted",
						Message:            "The workload is admitted",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Admission(
						utiltestingapi.MakeAdmission("b").
							PodSets(
								utiltestingapi.MakePodSetAssignment("one").
									Assignment(corev1.ResourceCPU, "on-demand", "50").
									Count(1).
									Obj(),
							).
							Obj(),
					).
					Obj(),
				*utiltestingapi.MakeWorkload("c1", "eng-alpha").
					Queue("lq-c").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "75").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "Workload no longer fits after processing another workload",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "one",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("75"),
						},
					}).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/b0": *utiltestingapi.MakeAdmission("b", "one").
					PodSets(utiltestingapi.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "on-demand", "10").
						Obj()).
					Obj(),
				"eng-alpha/b1": *utiltestingapi.MakeAdmission("b", "one").
					PodSets(utiltestingapi.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "on-demand", "50").
						Obj()).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"c": {"eng-alpha/c1"},
			},
		},
		// b0 is admitted, using 4 capacity.
		// b1 and c1 are pending.
		//
		// Even though b1 has a higher priority, c1 is admitted
		// as it is in a queue that is borrowing less than b1.
		"fair sharing two queues with weight 0 schedules workload which borrows less": {
			enableFairSharing: true,
			cohorts: []kueue.Cohort{
				*utiltestingapi.MakeCohort("A").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "8").Obj(),
					).Obj(),
			},
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("b").
					FairWeight(resource.MustParse("0")).
					Cohort("A").
					Preemption(kueue.ClusterQueuePreemption{
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
						WithinClusterQueue:  kueue.PreemptionPolicyNever,
					}).
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "0").Obj(),
					).Obj(),
				*utiltestingapi.MakeClusterQueue("c").
					FairWeight(resource.MustParse("0")).
					Cohort("A").
					Preemption(kueue.ClusterQueuePreemption{
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
						WithinClusterQueue:  kueue.PreemptionPolicyNever,
					}).
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "0").Obj(),
					).Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltestingapi.MakeLocalQueue("lq-b", "eng-alpha").ClusterQueue("b").Obj(),
				*utiltestingapi.MakeLocalQueue("lq-c", "eng-alpha").ClusterQueue("c").Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("b0", "eng-alpha").
					Queue("lq-b").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "4").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("b", "one").
						PodSets(utiltestingapi.MakePodSetAssignment("one").
							Assignment(corev1.ResourceCPU, "on-demand", "4").
							Obj()).
						Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("b1", "eng-alpha").
					Queue("lq-b").
					// high priority for tiebreak
					Priority(9001).
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "4").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("c1", "eng-alpha").
					Priority(0).
					Queue("lq-c").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "4").
						Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("b0", "eng-alpha").
					Queue("lq-b").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "4").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("b", "one").
						PodSets(utiltestingapi.MakePodSetAssignment("one").
							Assignment(corev1.ResourceCPU, "on-demand", "4").
							Obj()).
						Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("b1", "eng-alpha").
					Queue("lq-b").
					// high priority for tiebreak
					Priority(9001).
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "4").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "Workload no longer fits after processing another workload",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "one",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("4"),
						},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("c1", "eng-alpha").
					Priority(0).
					Queue("lq-c").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "4").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "QuotaReserved",
						Message:            "Quota reserved in ClusterQueue c",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionTrue,
						Reason:             "Admitted",
						Message:            "The workload is admitted",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Admission(
						utiltestingapi.MakeAdmission("c").
							PodSets(
								utiltestingapi.MakePodSetAssignment("one").
									Assignment(corev1.ResourceCPU, "on-demand", "4").
									Count(1).
									Obj(),
							).
							Obj(),
					).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/b0": *utiltestingapi.MakeAdmission("b", "one").
					PodSets(utiltestingapi.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "on-demand", "4").
						Obj()).
					Obj(),
				"eng-alpha/c1": *utiltestingapi.MakeAdmission("c", "one").
					PodSets(utiltestingapi.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "on-demand", "4").
						Obj()).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"b": {"eng-alpha/b1"},
			},
		},
		// b0 is admitted, using 4 capacity.
		// b1 and c1 are pending.
		//
		// Even though b1 has a higher priority, c1 is admitted
		// as it is in a queue that is borrowing less than b1.
		"fair sharing two queues with high weight schedules workload which borrows less": {
			enableFairSharing: true,
			cohorts: []kueue.Cohort{
				*utiltestingapi.MakeCohort("A").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "8").Obj(),
					).Obj(),
			},
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("b").
					FairWeight(resource.MustParse("123456789")).
					Cohort("A").
					Preemption(kueue.ClusterQueuePreemption{
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
						WithinClusterQueue:  kueue.PreemptionPolicyNever,
					}).
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "0").Obj(),
					).Obj(),
				*utiltestingapi.MakeClusterQueue("c").
					FairWeight(resource.MustParse("123456789")).
					Cohort("A").
					Preemption(kueue.ClusterQueuePreemption{
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
						WithinClusterQueue:  kueue.PreemptionPolicyNever,
					}).
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "0").Obj(),
					).Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltestingapi.MakeLocalQueue("lq-b", "eng-alpha").ClusterQueue("b").Obj(),
				*utiltestingapi.MakeLocalQueue("lq-c", "eng-alpha").ClusterQueue("c").Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("b0", "eng-alpha").
					Queue("lq-b").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "4").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("b", "one").
						PodSets(utiltestingapi.MakePodSetAssignment("one").
							Assignment(corev1.ResourceCPU, "on-demand", "4").
							Obj()).
						Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("b1", "eng-alpha").
					Queue("lq-b").
					Priority(9001).
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "4").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("c1", "eng-alpha").
					Priority(0).
					Queue("lq-c").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "4").
						Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("b0", "eng-alpha").
					Queue("lq-b").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "4").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("b", "one").
						PodSets(utiltestingapi.MakePodSetAssignment("one").
							Assignment(corev1.ResourceCPU, "on-demand", "4").
							Obj()).
						Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("b1", "eng-alpha").
					Queue("lq-b").
					Priority(9001).
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "4").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "Workload no longer fits after processing another workload",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "one",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("4"),
						},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("c1", "eng-alpha").
					Priority(0).
					Queue("lq-c").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "4").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "QuotaReserved",
						Message:            "Quota reserved in ClusterQueue c",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionTrue,
						Reason:             "Admitted",
						Message:            "The workload is admitted",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Admission(
						utiltestingapi.MakeAdmission("c").
							PodSets(
								utiltestingapi.MakePodSetAssignment("one").
									Assignment(corev1.ResourceCPU, "on-demand", "4").
									Count(1).
									Obj(),
							).
							Obj(),
					).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/b0": *utiltestingapi.MakeAdmission("b", "one").
					PodSets(utiltestingapi.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "on-demand", "4").
						Obj()).
					Obj(),
				"eng-alpha/c1": *utiltestingapi.MakeAdmission("c", "one").
					PodSets(utiltestingapi.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "on-demand", "4").
						Obj()).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"b": {"eng-alpha/b1"},
			},
		},
		// Cohort A has Clusterqueue a, and capacity is
		// provided by Cohort.
		//
		// Cohort B has Clusterqueue b, and capacity is
		// provided by ClusterQueue.
		//
		// Clusterqueue c has no Cohort, and provides its own
		// capacity.
		//
		// We ensure that all 3 pending workloads, one for each
		// cq, schedules.
		"fair sharing schedule singleton cqs and cq without cohort": {
			enableFairSharing: true,
			cohorts: []kueue.Cohort{
				*utiltestingapi.MakeCohort("A").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "10").Obj(),
					).Obj(),
				*utiltestingapi.MakeCohort("B").Obj(),
			},
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("a").
					Cohort("A").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "0").Obj(),
					).
					Obj(),
				*utiltestingapi.MakeClusterQueue("b").
					Cohort("B").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "10").Obj(),
					).
					Obj(),
				*utiltestingapi.MakeClusterQueue("c").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "10").Obj(),
					).
					Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltestingapi.MakeLocalQueue("lq-a", "eng-alpha").ClusterQueue("a").Obj(),
				*utiltestingapi.MakeLocalQueue("lq-b", "eng-alpha").ClusterQueue("b").Obj(),
				*utiltestingapi.MakeLocalQueue("lq-c", "eng-alpha").ClusterQueue("c").Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a1", "eng-alpha").
					Queue("lq-a").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "10").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("b1", "eng-alpha").
					Queue("lq-b").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "10").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("c1", "eng-alpha").
					Queue("lq-c").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "10").
						Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a1", "eng-alpha").
					Queue("lq-a").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "10").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "QuotaReserved",
						Message:            "Quota reserved in ClusterQueue a",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionTrue,
						Reason:             "Admitted",
						Message:            "The workload is admitted",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Admission(
						utiltestingapi.MakeAdmission("a").
							PodSets(
								utiltestingapi.MakePodSetAssignment("one").
									Assignment(corev1.ResourceCPU, "on-demand", "10").
									Count(1).
									Obj(),
							).
							Obj(),
					).
					Obj(),
				*utiltestingapi.MakeWorkload("b1", "eng-alpha").
					Queue("lq-b").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "10").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "QuotaReserved",
						Message:            "Quota reserved in ClusterQueue b",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionTrue,
						Reason:             "Admitted",
						Message:            "The workload is admitted",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Admission(
						utiltestingapi.MakeAdmission("b").
							PodSets(
								utiltestingapi.MakePodSetAssignment("one").
									Assignment(corev1.ResourceCPU, "on-demand", "10").
									Count(1).
									Obj(),
							).
							Obj(),
					).
					Obj(),
				*utiltestingapi.MakeWorkload("c1", "eng-alpha").
					Queue("lq-c").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "10").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "QuotaReserved",
						Message:            "Quota reserved in ClusterQueue c",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionTrue,
						Reason:             "Admitted",
						Message:            "The workload is admitted",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Admission(
						utiltestingapi.MakeAdmission("c").
							PodSets(
								utiltestingapi.MakePodSetAssignment("one").
									Assignment(corev1.ResourceCPU, "on-demand", "10").
									Count(1).
									Obj(),
							).
							Obj(),
					).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/a1": *utiltestingapi.MakeAdmission("a", "one").
					PodSets(utiltestingapi.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "on-demand", "10").
						Obj()).
					Obj(),
				"eng-alpha/b1": *utiltestingapi.MakeAdmission("b", "one").
					PodSets(utiltestingapi.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "on-demand", "10").
						Obj()).
					Obj(),
				"eng-alpha/c1": *utiltestingapi.MakeAdmission("c", "one").
					PodSets(utiltestingapi.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "on-demand", "10").
						Obj()).
					Obj(),
			},
			wantLeft: nil,
		},
		"fair sharing schedule highest priority first": {
			enableFairSharing: true,
			cohorts: []kueue.Cohort{
				*utiltestingapi.MakeCohort("A").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "10").Obj(),
					).Obj(),
				*utiltestingapi.MakeCohort("B").Obj(),
			},
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("b").
					Cohort("A").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "0").Obj(),
					).Obj(),
				*utiltestingapi.MakeClusterQueue("c").
					Cohort("A").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "0").Obj(),
					).Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltestingapi.MakeLocalQueue("lq-b", "eng-alpha").ClusterQueue("b").Obj(),
				*utiltestingapi.MakeLocalQueue("lq-c", "eng-alpha").ClusterQueue("c").Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("b1", "eng-alpha").
					Queue("lq-b").
					Priority(99).
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "10").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("c1", "eng-alpha").
					Queue("lq-c").
					Priority(101).
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "10").
						Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("b1", "eng-alpha").
					Queue("lq-b").
					Priority(99).
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "10").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "Workload no longer fits after processing another workload",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "one",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("10"),
						},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("c1", "eng-alpha").
					Queue("lq-c").
					Priority(101).
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "10").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "QuotaReserved",
						Message:            "Quota reserved in ClusterQueue c",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionTrue,
						Reason:             "Admitted",
						Message:            "The workload is admitted",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Admission(
						utiltestingapi.MakeAdmission("c").
							PodSets(
								utiltestingapi.MakePodSetAssignment("one").
									Assignment(corev1.ResourceCPU, "on-demand", "10").
									Count(1).
									Obj(),
							).
							Obj(),
					).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/c1": *utiltestingapi.MakeAdmission("c", "one").
					PodSets(utiltestingapi.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "on-demand", "10").
						Obj()).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"b": {"eng-alpha/b1"},
			},
		},
		"fair sharing schedule earliest timestamp first": {
			enableFairSharing: true,
			cohorts: []kueue.Cohort{
				*utiltestingapi.MakeCohort("A").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "10").Obj(),
					).Obj(),
				*utiltestingapi.MakeCohort("B").Obj(),
			},
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("b").
					Cohort("A").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "0").Obj(),
					).Obj(),
				*utiltestingapi.MakeClusterQueue("c").
					Cohort("A").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "0").Obj(),
					).Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltestingapi.MakeLocalQueue("lq-b", "eng-alpha").ClusterQueue("b").Obj(),
				*utiltestingapi.MakeLocalQueue("lq-c", "eng-alpha").ClusterQueue("c").Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("b1", "eng-alpha").
					Creation(now.Add(time.Second)).
					Queue("lq-b").
					Priority(101).
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "10").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("c1", "eng-alpha").
					Creation(now).
					Queue("lq-c").
					Priority(101).
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "10").
						Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("b1", "eng-alpha").
					Creation(now.Add(time.Second)).
					Queue("lq-b").
					Priority(101).
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "10").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "Workload no longer fits after processing another workload",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "one",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("10"),
						},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("c1", "eng-alpha").
					Creation(now).
					Queue("lq-c").
					Priority(101).
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "10").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "QuotaReserved",
						Message:            "Quota reserved in ClusterQueue c",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionTrue,
						Reason:             "Admitted",
						Message:            "The workload is admitted",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Admission(
						utiltestingapi.MakeAdmission("c").
							PodSets(
								utiltestingapi.MakePodSetAssignment("one").
									Assignment(corev1.ResourceCPU, "on-demand", "10").
									Count(1).
									Obj(),
							).
							Obj(),
					).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/c1": *utiltestingapi.MakeAdmission("c", "one").
					PodSets(utiltestingapi.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "on-demand", "10").
						Obj()).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"b": {"eng-alpha/b1"},
			},
		},
		"with fair sharing: preempt workload from CQ with the highest share": {
			enableFairSharing: true,
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("eng-gamma").
					Cohort("eng").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "50", "0").Obj(),
					).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("all_spot", "eng-alpha").
					Request(corev1.ResourceCPU, "100").
					SimpleReserveQuota("eng-alpha", "spot", now).Obj(),
				*utiltestingapi.MakeWorkload("alpha1", "eng-alpha").UID("alpha1").
					Request(corev1.ResourceCPU, "20").
					SimpleReserveQuota("eng-alpha", "on-demand", now).Obj(),
				*utiltestingapi.MakeWorkload("alpha2", "eng-alpha").UID("alpha2").
					Request(corev1.ResourceCPU, "20").
					SimpleReserveQuota("eng-alpha", "on-demand", now).Obj(),
				*utiltestingapi.MakeWorkload("alpha3", "eng-alpha").UID("alpha3").
					Request(corev1.ResourceCPU, "20").
					SimpleReserveQuota("eng-alpha", "on-demand", now).Obj(),
				*utiltestingapi.MakeWorkload("alpha4", "eng-alpha").UID("alpha4").
					Request(corev1.ResourceCPU, "20").
					SimpleReserveQuota("eng-alpha", "on-demand", now).Obj(),
				*utiltestingapi.MakeWorkload("gamma1", "eng-gamma").UID("gamma1").
					Request(corev1.ResourceCPU, "10").
					SimpleReserveQuota("eng-gamma", "on-demand", now).Obj(),
				*utiltestingapi.MakeWorkload("gamma2", "eng-gamma").UID("gamma2").
					Request(corev1.ResourceCPU, "20").
					SimpleReserveQuota("eng-gamma", "on-demand", now).Obj(),
				*utiltestingapi.MakeWorkload("gamma3", "eng-gamma").UID("gamma3").
					Request(corev1.ResourceCPU, "20").
					SimpleReserveQuota("eng-gamma", "on-demand", now).Obj(),
				*utiltestingapi.MakeWorkload("gamma4", "eng-gamma").UID("gamma4").
					Request(corev1.ResourceCPU, "20").
					SimpleReserveQuota("eng-gamma", "on-demand", now).Obj(),
				*utiltestingapi.MakeWorkload("preemptor", "eng-beta").
					UID("wl-preemptor").
					JobUID("job-preemptor").
					Queue("main").
					Request(corev1.ResourceCPU, "30").Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("all_spot", "eng-alpha").
					Request(corev1.ResourceCPU, "100").
					SimpleReserveQuota("eng-alpha", "spot", now).Obj(),
				*utiltestingapi.MakeWorkload("alpha1", "eng-alpha").UID("alpha1").
					Request(corev1.ResourceCPU, "20").
					SimpleReserveQuota("eng-alpha", "on-demand", now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-preemptor, JobUID: job-preemptor) due to reclamation within the cohort; preemptor path: /eng/eng-beta; preemptee path: /eng/eng-alpha",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InCohortReclamation",
						Message:            "Preempted to accommodate a workload (UID: wl-preemptor, JobUID: job-preemptor) due to reclamation within the cohort; preemptor path: /eng/eng-beta; preemptee path: /eng/eng-alpha",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("alpha2", "eng-alpha").UID("alpha2").
					Request(corev1.ResourceCPU, "20").
					SimpleReserveQuota("eng-alpha", "on-demand", now).Obj(),
				*utiltestingapi.MakeWorkload("alpha3", "eng-alpha").UID("alpha3").
					Request(corev1.ResourceCPU, "20").
					SimpleReserveQuota("eng-alpha", "on-demand", now).Obj(),
				*utiltestingapi.MakeWorkload("alpha4", "eng-alpha").UID("alpha4").
					Request(corev1.ResourceCPU, "20").
					SimpleReserveQuota("eng-alpha", "on-demand", now).Obj(),
				*utiltestingapi.MakeWorkload("preemptor", "eng-beta").
					UID("wl-preemptor").
					JobUID("job-preemptor").
					Queue("main").
					Request(corev1.ResourceCPU, "30").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor on-demand, 30 more needed, insufficient unused quota for cpu in flavor spot, 30 more needed. Pending the preemption of 2 workload(s)",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "main",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("30"),
						},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("gamma1", "eng-gamma").UID("gamma1").
					Request(corev1.ResourceCPU, "10").
					SimpleReserveQuota("eng-gamma", "on-demand", now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-preemptor, JobUID: job-preemptor) due to reclamation within the cohort; preemptor path: /eng/eng-beta; preemptee path: /eng/eng-gamma",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InCohortReclamation",
						Message:            "Preempted to accommodate a workload (UID: wl-preemptor, JobUID: job-preemptor) due to reclamation within the cohort; preemptor path: /eng/eng-beta; preemptee path: /eng/eng-gamma",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("gamma2", "eng-gamma").UID("gamma2").
					Request(corev1.ResourceCPU, "20").
					SimpleReserveQuota("eng-gamma", "on-demand", now).Obj(),
				*utiltestingapi.MakeWorkload("gamma3", "eng-gamma").UID("gamma3").
					Request(corev1.ResourceCPU, "20").
					SimpleReserveQuota("eng-gamma", "on-demand", now).Obj(),
				*utiltestingapi.MakeWorkload("gamma4", "eng-gamma").UID("gamma4").
					Request(corev1.ResourceCPU, "20").
					SimpleReserveQuota("eng-gamma", "on-demand", now).Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				// Preemptor is not admitted in this cycle.
				"eng-beta": {"eng-beta/preemptor"},
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/all_spot": *utiltestingapi.MakeAdmission("eng-alpha").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "spot", "100").
						Obj()).
					Obj(),
				"eng-alpha/alpha1": *utiltestingapi.MakeAdmission("eng-alpha").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "on-demand", "20").
						Obj()).
					Obj(),
				"eng-alpha/alpha2": *utiltestingapi.MakeAdmission("eng-alpha").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "on-demand", "20").
						Obj()).
					Obj(),
				"eng-alpha/alpha3": *utiltestingapi.MakeAdmission("eng-alpha").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "on-demand", "20").
						Obj()).
					Obj(),
				"eng-alpha/alpha4": *utiltestingapi.MakeAdmission("eng-alpha").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "on-demand", "20").
						Obj()).
					Obj(),
				"eng-gamma/gamma1": *utiltestingapi.MakeAdmission("eng-gamma").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "on-demand", "10").
						Obj()).
					Obj(),
				"eng-gamma/gamma2": *utiltestingapi.MakeAdmission("eng-gamma").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "on-demand", "20").
						Obj()).
					Obj(),
				"eng-gamma/gamma3": *utiltestingapi.MakeAdmission("eng-gamma").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "on-demand", "20").
						Obj()).
					Obj(),
				"eng-gamma/gamma4": *utiltestingapi.MakeAdmission("eng-gamma").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "on-demand", "20").
						Obj()).
					Obj(),
			},
		},
		"multiple preemptions within cq when fair sharing": {
			// Multiple CQs can preempt within their CQs, with
			// fair sharing enabled.
			enableFairSharing: true,
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("other-alpha").
					Cohort("other").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
					}).
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default").
							Resource(corev1.ResourceCPU, "2").
							Obj(),
					).
					Obj(),
				*utiltestingapi.MakeClusterQueue("other-beta").
					Cohort("other").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
					}).
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default").
							Resource(corev1.ResourceCPU, "2").
							Obj(),
					).
					Obj(),
				*utiltestingapi.MakeClusterQueue("other-gamma").
					Cohort("other").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
					}).
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default").
							Resource(corev1.ResourceCPU, "2").
							Obj(),
					).
					Obj(),
				*utiltestingapi.MakeClusterQueue("resource-bank").
					Cohort("other").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default").
							Resource(corev1.ResourceCPU, "3").Obj(),
					).
					Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltestingapi.MakeLocalQueue("other", "eng-alpha").ClusterQueue("other-alpha").Obj(),
				*utiltestingapi.MakeLocalQueue("other", "eng-beta").ClusterQueue("other-beta").Obj(),
				*utiltestingapi.MakeLocalQueue("other", "eng-gamma").ClusterQueue("other-gamma").Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a1", "eng-alpha").
					Priority(0).
					Queue("other").
					Request(corev1.ResourceCPU, "3").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("other-alpha").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "3").
							Obj()).Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("b1", "eng-beta").
					Priority(0).
					Queue("other").
					Request(corev1.ResourceCPU, "3").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("other-beta").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "3").Obj()).Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("c1", "eng-gamma").
					Priority(0).
					Queue("other").
					Request(corev1.ResourceCPU, "3").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("other-gamma").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "3").Obj()).Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("preemptor", "eng-alpha").
					UID("wl-preemptor").
					JobUID("job-preemptor").
					Priority(100).
					Queue("other").
					Request(corev1.ResourceCPU, "3").
					Obj(),
				*utiltestingapi.MakeWorkload("preemptor", "eng-beta").
					UID("wl-preemptor").
					JobUID("job-preemptor").
					Priority(100).
					Queue("other").
					Request(corev1.ResourceCPU, "3").
					Obj(),
				*utiltestingapi.MakeWorkload("preemptor", "eng-gamma").
					UID("wl-preemptor").
					JobUID("job-preemptor").
					Priority(100).
					Queue("other").
					Request(corev1.ResourceCPU, "3").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a1", "eng-alpha").
					Priority(0).
					Queue("other").
					Request(corev1.ResourceCPU, "3").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("other-alpha").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "3").
							Obj()).Obj(), now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-preemptor, JobUID: job-preemptor) due to prioritization in the ClusterQueue; preemptor path: /other/other-alpha; preemptee path: /other/other-alpha",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InClusterQueue",
						Message:            "Preempted to accommodate a workload (UID: wl-preemptor, JobUID: job-preemptor) due to prioritization in the ClusterQueue; preemptor path: /other/other-alpha; preemptee path: /other/other-alpha",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("preemptor", "eng-alpha").
					UID("wl-preemptor").
					JobUID("job-preemptor").
					Priority(100).
					Queue("other").
					Request(corev1.ResourceCPU, "3").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor default, 3 more needed. Pending the preemption of 1 workload(s)",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "main",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("3"),
						},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("b1", "eng-beta").
					Priority(0).
					Queue("other").
					Request(corev1.ResourceCPU, "3").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("other-beta").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "3").Obj()).Obj(), now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-preemptor, JobUID: job-preemptor) due to prioritization in the ClusterQueue; preemptor path: /other/other-beta; preemptee path: /other/other-beta",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InClusterQueue",
						Message:            "Preempted to accommodate a workload (UID: wl-preemptor, JobUID: job-preemptor) due to prioritization in the ClusterQueue; preemptor path: /other/other-beta; preemptee path: /other/other-beta",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("preemptor", "eng-beta").
					UID("wl-preemptor").
					JobUID("job-preemptor").
					Priority(100).
					Queue("other").
					Request(corev1.ResourceCPU, "3").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor default, 3 more needed. Pending the preemption of 1 workload(s)",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "main",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("3"),
						},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("c1", "eng-gamma").
					Priority(0).
					Queue("other").
					Request(corev1.ResourceCPU, "3").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("other-gamma").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "3").Obj()).Obj(), now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-preemptor, JobUID: job-preemptor) due to prioritization in the ClusterQueue; preemptor path: /other/other-gamma; preemptee path: /other/other-gamma",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InClusterQueue",
						Message:            "Preempted to accommodate a workload (UID: wl-preemptor, JobUID: job-preemptor) due to prioritization in the ClusterQueue; preemptor path: /other/other-gamma; preemptee path: /other/other-gamma",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("preemptor", "eng-gamma").
					UID("wl-preemptor").
					JobUID("job-preemptor").
					Priority(100).
					Queue("other").
					Request(corev1.ResourceCPU, "3").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor default, 3 more needed. Pending the preemption of 1 workload(s)",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "main",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("3"),
						},
					}).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"other-alpha": {"eng-alpha/preemptor"},
				"other-beta":  {"eng-beta/preemptor"},
				"other-gamma": {"eng-gamma/preemptor"},
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/a1": *utiltestingapi.MakeAdmission("other-alpha").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "default", "3").Obj()).Obj(),
				"eng-beta/b1": *utiltestingapi.MakeAdmission("other-beta").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "default", "3").Obj()).Obj(),
				"eng-gamma/c1": *utiltestingapi.MakeAdmission("other-gamma").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "default", "3").Obj()).Obj(),
			},
			wantSkippedPreemptions: map[string]int{
				"other-alpha": 0,
				"other-beta":  0,
				"other-gamma": 0,
			},
		},
		"multiple preemptions skip overlapping preemption targets": {
			// Gamma cq is using more than fair share of CPU.
			// alpha and beta need CPU to run incoming workload.
			//
			// Alpha workload is higher priority, so sorted first.
			//
			// We ensure only alpha (and not beta) workload is preempted,
			// as we disallow overlapping preemption targets
			// in the same cycle
			enableFairSharing: true,
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("other-alpha").
					Cohort("other").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default").
							Resource(corev1.ResourceCPU, "0").
							Resource("alpha-resource", "1").Obj(),
					).
					Obj(),
				*utiltestingapi.MakeClusterQueue("other-beta").
					Cohort("other").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default").
							Resource(corev1.ResourceCPU, "0").
							Resource("beta-resource", "1").Obj(),
					).
					Obj(),
				*utiltestingapi.MakeClusterQueue("other-gamma").
					Cohort("other").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default").
							Resource(corev1.ResourceCPU, "0").
							Resource("gamma-resource", "1").Obj(),
					).
					Obj(),
				*utiltestingapi.MakeClusterQueue("resource-bank").
					Cohort("other").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default").
							Resource(corev1.ResourceCPU, "9").Obj(),
					).
					Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltestingapi.MakeLocalQueue("other", "eng-alpha").ClusterQueue("other-alpha").Obj(),
				*utiltestingapi.MakeLocalQueue("other", "eng-beta").ClusterQueue("other-beta").Obj(),
				*utiltestingapi.MakeLocalQueue("other", "eng-gamma").ClusterQueue("other-gamma").Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a1", "eng-alpha").
					Priority(0).
					Queue("other").
					Request("alpha-resource", "1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("other-alpha").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment("alpha-resource", "default", "1").
							Obj()).Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("b1", "eng-beta").
					Priority(0).
					Queue("other").
					Request("beta-resource", "1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("other-beta").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment("beta-resource", "default", "1").Obj()).Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("c1", "eng-gamma").
					Priority(0).
					Queue("other").
					Request(corev1.ResourceCPU, "9").
					Request("gamma-resource", "1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("other-gamma").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "9").Obj()).Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("preemptor", "eng-alpha").
					UID("wl-preemptor").
					JobUID("job-preemptor").
					Priority(100).
					Queue("other").
					Request(corev1.ResourceCPU, "3").
					Request("alpha-resource", "1").
					Obj(),
				*utiltestingapi.MakeWorkload("pretending-preemptor", "eng-beta").
					Priority(99).
					Queue("other").
					Request(corev1.ResourceCPU, "3").
					Request("beta-resource", "1").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a1", "eng-alpha").
					Priority(0).
					Queue("other").
					Request("alpha-resource", "1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("other-alpha").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment("alpha-resource", "default", "1").
							Obj()).Obj(), now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-preemptor, JobUID: job-preemptor) due to prioritization in the ClusterQueue; preemptor path: /other/other-alpha; preemptee path: /other/other-alpha",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InClusterQueue",
						Message:            "Preempted to accommodate a workload (UID: wl-preemptor, JobUID: job-preemptor) due to prioritization in the ClusterQueue; preemptor path: /other/other-alpha; preemptee path: /other/other-alpha",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("preemptor", "eng-alpha").
					UID("wl-preemptor").
					JobUID("job-preemptor").
					Priority(100).
					Queue("other").
					Request(corev1.ResourceCPU, "3").
					Request("alpha-resource", "1").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "couldn't assign flavors to pod set main: insufficient unused quota for alpha-resource in flavor default, 1 more needed, insufficient unused quota for cpu in flavor default, 3 more needed. Pending the preemption of 2 workload(s)",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "main",
						Resources: corev1.ResourceList{
							"alpha-resource":   resource.MustParse("1"),
							corev1.ResourceCPU: resource.MustParse("3"),
						},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("b1", "eng-beta").
					Priority(0).
					Queue("other").
					Request("beta-resource", "1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("other-beta").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment("beta-resource", "default", "1").Obj()).Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("pretending-preemptor", "eng-beta").
					Priority(99).
					Queue("other").
					Request(corev1.ResourceCPU, "3").
					Request("beta-resource", "1").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "Workload has overlapping preemption targets with another workload",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "main",
						Resources: corev1.ResourceList{
							"beta-resource":    resource.MustParse("1"),
							corev1.ResourceCPU: resource.MustParse("3"),
						},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("c1", "eng-gamma").
					Priority(0).
					Queue("other").
					Request(corev1.ResourceCPU, "9").
					Request("gamma-resource", "1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("other-gamma").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "9").Obj()).Obj(), now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-preemptor, JobUID: job-preemptor) due to Fair Sharing within the cohort; preemptor path: /other/other-alpha; preemptee path: /other/other-gamma",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InCohortFairSharing",
						Message:            "Preempted to accommodate a workload (UID: wl-preemptor, JobUID: job-preemptor) due to Fair Sharing within the cohort; preemptor path: /other/other-alpha; preemptee path: /other/other-gamma",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"other-alpha": {"eng-alpha/preemptor"},
				"other-beta":  {"eng-beta/pretending-preemptor"},
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/a1": *utiltestingapi.MakeAdmission("other-alpha").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment("alpha-resource", "default", "1").Obj()).Obj(),
				"eng-beta/b1": *utiltestingapi.MakeAdmission("other-beta").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment("beta-resource", "default", "1").Obj()).Obj(),
				"eng-gamma/c1": *utiltestingapi.MakeAdmission("other-gamma").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "default", "9").Obj()).Obj(),
			},
			wantSkippedPreemptions: map[string]int{
				"other-alpha": 0,
				"other-beta":  1,
				"other-gamma": 0,
			},
		},
		"not enough resources with fair sharing enabled": {
			enableFairSharing: true,
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("new", "sales").
					Queue("main").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "100").
						Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("new", "sales").
					Queue("main").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "100").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "couldn't assign flavors to pod set one: insufficient quota for cpu in flavor default, previously considered podsets requests (0) + current podset request (100) > maximum capacity (50)",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "one",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("100"),
						},
					}).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"sales": {"sales/new"},
			},
		},
		"with fair sharing: prefer reclamation over cq priority based preemption; with preemption while borrowing": {
			// We enable fair sharing so that preemption while borrowing is enabled.
			// Flavor 1, on-demand, requires priority-based preemption in CQ.
			// Flavor 2, spot, requires reclaim in Cohort.
			// Flavor 2 is a better assignment, because reclaim is preferred over
			// priority-based preemption.
			enableFairSharing: true,
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("other-alpha").
					Cohort("other").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
					}).
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").Resource("gpu", "7").Obj(),
						*utiltestingapi.MakeFlavorQuotas("spot").Resource("gpu", "7").Obj(),
					).
					Obj(),
				*utiltestingapi.MakeClusterQueue("other-beta").
					Cohort("other").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").Resource("gpu", "3").Obj(),
						*utiltestingapi.MakeFlavorQuotas("spot").Resource("gpu", "3").Obj(),
					).
					Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltestingapi.MakeLocalQueue("other", "eng-alpha").ClusterQueue("other-alpha").Obj(),
				*utiltestingapi.MakeLocalQueue("other", "eng-beta").ClusterQueue("other-beta").Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a1", "eng-alpha").
					Priority(50).
					Queue("other").
					Request("gpu", "5").
					SimpleReserveQuota("other-alpha", "on-demand", now).
					Obj(),
				*utiltestingapi.MakeWorkload("b1", "eng-beta").
					Priority(50).
					Queue("other").
					Request("gpu", "5").
					SimpleReserveQuota("other-beta", "spot", now).
					Obj(),
				*utiltestingapi.MakeWorkload("preemptor", "eng-alpha").
					UID("wl-preemptor").
					JobUID("job-preemptor").
					Priority(100).
					Queue("other").
					Request("gpu", "8").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a1", "eng-alpha").
					Priority(50).
					Queue("other").
					Request("gpu", "5").
					SimpleReserveQuota("other-alpha", "on-demand", now).
					Obj(),
				*utiltestingapi.MakeWorkload("preemptor", "eng-alpha").
					UID("wl-preemptor").
					JobUID("job-preemptor").
					Priority(100).
					Queue("other").
					Request("gpu", "8").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "couldn't assign flavors to pod set main: insufficient unused quota for gpu in flavor on-demand, 3 more needed, insufficient unused quota for gpu in flavor spot, 3 more needed. Pending the preemption of 1 workload(s)",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "main",
						Resources: corev1.ResourceList{
							"gpu": resource.MustParse("8"),
						},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("b1", "eng-beta").
					Priority(50).
					Queue("other").
					Request("gpu", "5").
					SimpleReserveQuota("other-beta", "spot", now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-preemptor, JobUID: job-preemptor) due to Fair Sharing within the cohort; preemptor path: /other/other-alpha; preemptee path: /other/other-beta",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InCohortFairSharing",
						Message:            "Preempted to accommodate a workload (UID: wl-preemptor, JobUID: job-preemptor) due to Fair Sharing within the cohort; preemptor path: /other/other-alpha; preemptee path: /other/other-beta",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"other-alpha": {"eng-alpha/preemptor"},
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/a1": *utiltestingapi.MakeAdmission("other-alpha").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment("gpu", "on-demand", "5").
						Obj()).
					Obj(),
				"eng-beta/b1": *utiltestingapi.MakeAdmission("other-beta").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment("gpu", "spot", "5").
						Obj()).
					Obj(),
			},
		},
		"prefer flavor with most local capacity": {
			enableFairSharing: true,
			cohorts: []kueue.Cohort{
				*utiltestingapi.MakeCohort("root-cohort").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").Resource("gpu", "2").Obj(),
						*utiltestingapi.MakeFlavorQuotas("spot").Resource("gpu", "0").Obj(),
					).
					Obj(),
				*utiltestingapi.MakeCohort("child-cohort").
					Parent("root-cohort").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").Resource("gpu", "5").Obj(),
						*utiltestingapi.MakeFlavorQuotas("spot").Resource("gpu", "7").Obj(),
					).
					Obj(),
			},
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("queue1").
					Cohort("child-cohort").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					FlavorFungibility(kueue.FlavorFungibility{
						WhenCanPreempt: kueue.TryNextFlavor,
						WhenCanBorrow:  kueue.TryNextFlavor,
					}).
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").Resource("gpu", "3").Obj(),
						*utiltestingapi.MakeFlavorQuotas("spot").Resource("gpu", "3").Obj(),
					).
					Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltestingapi.MakeLocalQueue("queue1", "default").ClusterQueue("queue1").Obj(),
			},
			workloads: []kueue.Workload{
				// exhaust quota in on-demand in ParentCohort
				*utiltestingapi.MakeWorkload("a1", "default").
					Queue("queue1").
					Request("gpu", "8").
					SimpleReserveQuota("queue1", "on-demand", now).
					Obj(),
				// exhaust quota in spot in ClusterQueue
				*utiltestingapi.MakeWorkload("a2", "default").
					Queue("queue1").
					Request("gpu", "3").
					SimpleReserveQuota("queue1", "spot", now).
					Obj(),
				*utiltestingapi.MakeWorkload("a3", "default").
					Queue("queue1").
					Request("gpu", "1").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				// exhaust quota in on-demand in ParentCohort
				*utiltestingapi.MakeWorkload("a1", "default").
					Queue("queue1").
					Request("gpu", "8").
					SimpleReserveQuota("queue1", "on-demand", now).
					Obj(),
				// exhaust quota in spot in ClusterQueue
				*utiltestingapi.MakeWorkload("a2", "default").
					Queue("queue1").
					Request("gpu", "3").
					SimpleReserveQuota("queue1", "spot", now).
					Obj(),
				*utiltestingapi.MakeWorkload("a3", "default").
					Queue("queue1").
					Request("gpu", "1").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "QuotaReserved",
						Message:            "Quota reserved in ClusterQueue queue1",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionTrue,
						Reason:             "Admitted",
						Message:            "The workload is admitted",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Admission(
						utiltestingapi.MakeAdmission("queue1").
							PodSets(
								utiltestingapi.MakePodSetAssignment("main").
									Assignment("gpu", "spot", "1").
									Count(1).
									Obj(),
							).
							Obj(),
					).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"default/a1": *utiltestingapi.MakeAdmission("queue1").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment("gpu", "on-demand", "8").
						Obj()).
					Obj(),
				"default/a2": *utiltestingapi.MakeAdmission("queue1").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment("gpu", "spot", "3").
						Obj()).
					Obj(),
				"default/a3": *utiltestingapi.MakeAdmission("queue1").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment("gpu", "spot", "1").
						Obj()).
					Obj(),
			},
		},
		"prefer flavor with most local capacity (FS=false)": {
			enableFairSharing: false,
			cohorts: []kueue.Cohort{
				*utiltestingapi.MakeCohort("root-cohort").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").Resource("gpu", "2").Obj(),
						*utiltestingapi.MakeFlavorQuotas("spot").Resource("gpu", "0").Obj(),
					).
					Obj(),
				*utiltestingapi.MakeCohort("child-cohort").
					Parent("root-cohort").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").Resource("gpu", "5").Obj(),
						*utiltestingapi.MakeFlavorQuotas("spot").Resource("gpu", "7").Obj(),
					).
					Obj(),
			},
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("queue1").
					Cohort("child-cohort").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					FlavorFungibility(kueue.FlavorFungibility{
						WhenCanPreempt: kueue.TryNextFlavor,
						WhenCanBorrow:  kueue.TryNextFlavor,
					}).
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").Resource("gpu", "3").Obj(),
						*utiltestingapi.MakeFlavorQuotas("spot").Resource("gpu", "3").Obj(),
					).
					Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltestingapi.MakeLocalQueue("queue1", "default").ClusterQueue("queue1").Obj(),
			},
			workloads: []kueue.Workload{
				// exhaust quota in on-demand in ParentCohort
				*utiltestingapi.MakeWorkload("a1", "default").
					Queue("queue1").
					Request("gpu", "8").
					SimpleReserveQuota("queue1", "on-demand", now).
					Obj(),
				// exhaust quota in spot in ClusterQueue
				*utiltestingapi.MakeWorkload("a2", "default").
					Queue("queue1").
					Request("gpu", "3").
					SimpleReserveQuota("queue1", "spot", now).
					Obj(),
				*utiltestingapi.MakeWorkload("a3", "default").
					Queue("queue1").
					Request("gpu", "1").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				// exhaust quota in on-demand in ParentCohort
				*utiltestingapi.MakeWorkload("a1", "default").
					Queue("queue1").
					Request("gpu", "8").
					SimpleReserveQuota("queue1", "on-demand", now).
					Obj(),
				// exhaust quota in spot in ClusterQueue
				*utiltestingapi.MakeWorkload("a2", "default").
					Queue("queue1").
					Request("gpu", "3").
					SimpleReserveQuota("queue1", "spot", now).
					Obj(),
				*utiltestingapi.MakeWorkload("a3", "default").
					Queue("queue1").
					Request("gpu", "1").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "QuotaReserved",
						Message:            "Quota reserved in ClusterQueue queue1",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionTrue,
						Reason:             "Admitted",
						Message:            "The workload is admitted",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Admission(
						utiltestingapi.MakeAdmission("queue1").
							PodSets(
								utiltestingapi.MakePodSetAssignment("main").
									Assignment("gpu", "spot", "1").
									Count(1).
									Obj(),
							).
							Obj(),
					).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"default/a1": *utiltestingapi.MakeAdmission("queue1").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment("gpu", "on-demand", "8").
						Obj()).
					Obj(),
				"default/a2": *utiltestingapi.MakeAdmission("queue1").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment("gpu", "spot", "3").
						Obj()).
					Obj(),
				"default/a3": *utiltestingapi.MakeAdmission("queue1").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment("gpu", "spot", "1").
						Obj()).
					Obj(),
			},
		},
		"PreemptionOverBorrowing with fair sharing: preempt in first flavor instead of borrowing in second": {
			enableFairSharing: true,
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("fs-pob-cq").
					FairWeight(resource.MustParse("1")).
					Cohort("fs-pob-cohort").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
					}).
					FlavorFungibility(kueue.FlavorFungibility{
						WhenCanBorrow:  kueue.TryNextFlavor,
						WhenCanPreempt: kueue.TryNextFlavor,
						Preference:     ptr.To(kueue.PreemptionOverBorrowing),
					}).
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "5", "0").Obj(),
						*utiltestingapi.MakeFlavorQuotas("spot").
							Resource(corev1.ResourceCPU, "0", "5").Obj(),
					).Obj(),
				*utiltestingapi.MakeClusterQueue("fs-pob-lender").
					FairWeight(resource.MustParse("1")).
					Cohort("fs-pob-cohort").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "0").Obj(),
						*utiltestingapi.MakeFlavorQuotas("spot").
							Resource(corev1.ResourceCPU, "5").Obj(),
					).Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltestingapi.MakeLocalQueue("fs-pob-queue", "default").ClusterQueue("fs-pob-cq").Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("fs-low-pob", "default").
					Queue("fs-pob-queue").
					Priority(-1).
					Request(corev1.ResourceCPU, "5").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("fs-pob-cq").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "on-demand", "5000m").
							Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("fs-high-pob", "default").
					UID("wl-fs-high-pob").
					JobUID("job-fs-high-pob").
					Queue("fs-pob-queue").
					Priority(0).
					Request(corev1.ResourceCPU, "5").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("fs-high-pob", "default").
					UID("wl-fs-high-pob").
					JobUID("job-fs-high-pob").
					Queue("fs-pob-queue").
					Priority(0).
					Request(corev1.ResourceCPU, "5").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor on-demand, 5 more needed. Pending the preemption of 1 workload(s)",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: kueue.DefaultPodSetName,
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("5"),
						},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("fs-low-pob", "default").
					Queue("fs-pob-queue").
					Priority(-1).
					Request(corev1.ResourceCPU, "5").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("fs-pob-cq").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "on-demand", "5000m").
							Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-fs-high-pob, JobUID: job-fs-high-pob) due to prioritization in the ClusterQueue; preemptor path: /fs-pob-cohort/fs-pob-cq; preemptee path: /fs-pob-cohort/fs-pob-cq",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InClusterQueue",
						Message:            "Preempted to accommodate a workload (UID: wl-fs-high-pob, JobUID: job-fs-high-pob) due to prioritization in the ClusterQueue; preemptor path: /fs-pob-cohort/fs-pob-cq; preemptee path: /fs-pob-cohort/fs-pob-cq",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"fs-pob-cq": {"default/fs-high-pob"},
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"default/fs-low-pob": *utiltestingapi.MakeAdmission("fs-pob-cq").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "on-demand", "5000m").
						Obj()).
					Obj(),
			},
			eventCmpOpts: ignoreEventMessageCmpOpts,
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "fs-low-pob", "EvictedDueToPreempted", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "fs-low-pob", "Preempted", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("default", "fs-high-pob", "Pending", corev1.EventTypeWarning).Obj(),
			},
		},
	}
	for name, tc := range cases {
		for _, enabled := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s WorkloadRequestUseMergePatch enabled: %t", name, enabled), func(t *testing.T) {
				features.SetFeatureGateDuringTest(t, features.WorkloadRequestUseMergePatch, enabled)
				metrics.AdmissionCyclePreemptionSkips.Reset()

				ctx, log := utiltesting.ContextWithLog(t)

				allQueues := append(queues, tc.additionalLocalQueues...)
				allClusterQueues := append(clusterQueues, tc.additionalClusterQueues...)

				clientBuilder := utiltesting.NewClientBuilder().
					WithLists(&kueue.WorkloadList{Items: tc.workloads}, &kueue.LocalQueueList{Items: allQueues}).
					WithObjects(append(
						[]client.Object{
							utiltesting.MakeNamespaceWrapper("default").Obj(),
							utiltesting.MakeNamespaceWrapper("eng-alpha").Label("dep", "eng").Obj(),
							utiltesting.MakeNamespaceWrapper("eng-beta").Label("dep", "eng").Obj(),
							utiltesting.MakeNamespaceWrapper("eng-gamma").Label("dep", "eng").Obj(),
							utiltesting.MakeNamespaceWrapper("sales").Label("dep", "sales").Obj(),
							utiltesting.MakeNamespaceWrapper("lend").Label("dep", "lend").Obj(),
						}, tc.objects...,
					)...).
					WithStatusSubresource(&kueue.Workload{}).
					WithInterceptorFuncs(interceptor.Funcs{
						SubResourcePatch: utiltesting.TreatSSAAsStrategicMerge,
					})

				cl := clientBuilder.Build()
				recorder := &utiltesting.EventRecorder{}
				cqCache := schdcache.New(cl)
				qManager := qcache.NewManagerForUnitTests(cl, cqCache)
				// Workloads are loaded into queues or clusterQueues as we add them.
				for _, q := range allQueues {
					if err := qManager.AddLocalQueue(ctx, &q); err != nil {
						t.Fatalf("Inserting queue %s/%s in manager: %v", q.Namespace, q.Name, err)
					}
				}
				for i := range resourceFlavors {
					cqCache.AddOrUpdateResourceFlavor(log, resourceFlavors[i])
				}
				for _, cq := range allClusterQueues {
					if err := cqCache.AddClusterQueue(ctx, &cq); err != nil {
						t.Fatalf("Inserting clusterQueue %s in cache: %v", cq.Name, err)
					}
					if err := qManager.AddClusterQueue(ctx, &cq); err != nil {
						t.Fatalf("Inserting clusterQueue %s in manager: %v", cq.Name, err)
					}
					if err := cl.Create(ctx, &cq); err != nil {
						t.Errorf("couldn't create the cluster queue: %v", err)
					}
				}

				for _, cohort := range tc.cohorts {
					if err := cqCache.AddOrUpdateCohort(&cohort); err != nil {
						t.Fatalf("Inserting Cohort %s in cache: %v", cohort.Name, err)
					}
				}

				var fairSharing *config.FairSharing
				if tc.enableFairSharing {
					fairSharing = &config.FairSharing{}
				}
				scheduler := New(qManager, cqCache, cl, recorder,
					WithFairSharing(fairSharing), WithClock(t, fakeClock), WithPreemptionExpectations(preemptexpectations.New()))
				wg := sync.WaitGroup{}
				scheduler.setAdmissionRoutineWrapper(routine.NewWrapper(
					func() { wg.Add(1) },
					func() { wg.Done() },
				))

				ctx, cancel := context.WithTimeout(ctx, queueingTimeout)
				go qManager.CleanUpOnContext(ctx)
				defer cancel()

				scheduler.schedule(ctx)
				wg.Wait()

				// Verify assignments in cache.
				gotAssignments := make(map[workload.Reference]kueue.Admission)
				snapshot, err := cqCache.Snapshot(ctx)
				if err != nil {
					t.Fatalf("unexpected error while building snapshot: %v", err)
				}
				for cqName, c := range snapshot.ClusterQueues() {
					for name, w := range c.Workloads {
						switch {
						case !workload.HasQuotaReservation(w.Obj):
							t.Errorf("Workload %s is not admitted by a clusterQueue, but it is found as member of clusterQueue %s in the cache", name, cqName)
						case w.Obj.Status.Admission.ClusterQueue != cqName:
							t.Errorf("Workload %s is admitted by clusterQueue %s, but it is found as member of clusterQueue %s in the cache", name, w.Obj.Status.Admission.ClusterQueue, cqName)
						default:
							gotAssignments[name] = *w.Obj.Status.Admission
						}
					}
				}

				gotWorkloads := &kueue.WorkloadList{}
				err = cl.List(ctx, gotWorkloads)
				if err != nil {
					t.Fatalf("Unexpected list workloads error: %v", err)
				}

				defaultWorkloadCmpOpts := cmp.Options{
					cmpopts.EquateEmpty(),
					cmpopts.IgnoreFields(kueue.AdmissionCheckState{}, "LastTransitionTime"),
					cmpopts.IgnoreFields(kueue.Workload{}, "ObjectMeta.ResourceVersion", "ObjectMeta.CreationTimestamp"),
					cmpopts.SortSlices(func(a, b metav1.Condition) bool { return a.Type < b.Type }),
				}

				if diff := cmp.Diff(tc.wantWorkloads, gotWorkloads.Items, defaultWorkloadCmpOpts); diff != "" {
					t.Errorf("Unexpected workloads (-want,+got):\n%s", diff)
				}

				if len(gotAssignments) == 0 {
					gotAssignments = nil
				}
				if diff := cmp.Diff(tc.wantAssignments, gotAssignments); diff != "" {
					t.Errorf("Unexpected assigned clusterQueues in cache (-want,+got):\n%s", diff)
				}

				qDump := qManager.Dump()
				if diff := cmp.Diff(tc.wantLeft, qDump, cmpDump...); diff != "" {
					t.Errorf("Unexpected elements left in the queue (-want,+got):\n%s", diff)
				}
				qDumpInadmissible := qManager.DumpInadmissible()
				if diff := cmp.Diff(tc.wantInadmissibleLeft, qDumpInadmissible, cmpDump...); diff != "" {
					t.Errorf("Unexpected elements left in inadmissible workloads (-want,+got):\n%s", diff)
				}

				if len(tc.wantEvents) > 0 {
					if diff := cmp.Diff(tc.wantEvents, recorder.RecordedEvents, tc.eventCmpOpts...); diff != "" {
						t.Errorf("unexpected events (-want/+got):\n%s", diff)
					}
				}

				for cqName, want := range tc.wantSkippedPreemptions {
					lvs := []string{cqName, roletracker.RoleStandalone}
					val, err := testutil.GetGaugeMetricValue(metrics.AdmissionCyclePreemptionSkips.WithLabelValues(lvs...))
					if err != nil {
						t.Fatalf("Couldn't get value for metric admission_cycle_preemption_skips for %q: %v", cqName, err)
					}
					got := int(val)
					if want != got {
						t.Errorf("Counted %d skips for %q, want %d", got, cqName, want)
					}
				}
			})
		}
	}
}
