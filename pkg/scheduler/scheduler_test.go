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
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/client-go/tools/record"
	"k8s.io/component-base/metrics/testutil"
	testingclock "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/scheduler/flavorassigner"
	"sigs.k8s.io/kueue/pkg/util/limitrange"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	"sigs.k8s.io/kueue/pkg/util/routine"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
)

const (
	queueingTimeout = time.Second
)

var cmpDump = cmp.Options{
	cmpopts.SortSlices(func(a, b string) bool { return a < b }),
}

func TestSchedule(t *testing.T) {
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
		// Features
		disablePartialAdmission           bool
		enableFairSharing                 bool
		enableElasticJobsViaWorkloadSlice bool

		workloads      []kueue.Workload
		objects        []client.Object
		admissionError error

		// additional*Queues can hold any extra queues needed by the tc
		additionalClusterQueues []kueue.ClusterQueue
		additionalLocalQueues   []kueue.LocalQueue

		cohorts []kueue.Cohort

		// wantAssignments is a summary of all the admissions in the cache after this cycle.
		wantAssignments map[workload.Reference]kueue.Admission
		// wantWorkloads is the subset of workloads that got admitted in this cycle.
		wantWorkloads []kueue.Workload
		// workload version to compensate for the difference between use of Apply and Merge patch in FakeClient
		wantWorkloadUseMergePatch []kueue.Workload
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
		"use second flavor when the first has no preemption candidates; WhenCanPreempt: MayStopSearch": {
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("other-alpha").
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
					).Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltestingapi.MakeLocalQueue("other", "eng-alpha").ClusterQueue("other-alpha").Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("admitted", "eng-alpha").
					Queue("other").
					Request(corev1.ResourceCPU, "50").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("other-alpha").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "on-demand", "50").
							Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("new", "eng-alpha").
					Queue("other").
					Request(corev1.ResourceCPU, "20").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("admitted", "eng-alpha").
					Queue("other").
					Request(corev1.ResourceCPU, "50").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("other-alpha").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "on-demand", "50").
							Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("new", "eng-alpha").
					Queue("other").
					Request(corev1.ResourceCPU, "20").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "QuotaReserved",
						Message:            "Quota reserved in ClusterQueue other-alpha",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionTrue,
						Reason:             "Admitted",
						Message:            "The workload is admitted",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Admission(utiltestingapi.MakeAdmission("other-alpha").
						PodSets(
							utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "spot", "20").
								Obj()).
						Obj()).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/admitted": {
					ClusterQueue: "other-alpha",
					PodSetAssignments: []kueue.PodSetAssignment{
						utiltestingapi.MakePodSetAssignment("main").
							Assignment(corev1.ResourceCPU, "on-demand", "50").
							Obj(),
					},
				},
				"eng-alpha/new": {
					ClusterQueue: "other-alpha",
					PodSetAssignments: []kueue.PodSetAssignment{
						utiltestingapi.MakePodSetAssignment("main").Assignment(corev1.ResourceCPU, "spot", "20").Obj(),
					},
				},
			},
		},
		"workload fits in single clusterQueue, with check state ready": {
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("foo", "sales").
					Queue("main").
					PodSets(*utiltestingapi.MakePodSet("one", 10).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "check",
						State: kueue.CheckStateReady,
					}).
					Generation(1).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"sales/foo": {
					ClusterQueue: "sales",
					PodSetAssignments: []kueue.PodSetAssignment{
						utiltestingapi.MakePodSetAssignment("one").
							Assignment(corev1.ResourceCPU, "default", "10000m").
							Count(10).
							Obj(),
					},
				},
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("foo", "sales").
					Queue("main").
					PodSets(*utiltestingapi.MakePodSet("one", 10).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "check",
						State: kueue.CheckStateReady,
					}).
					Admission(
						utiltestingapi.MakeAdmission("sales").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "default", "10000m").
								Count(10).
								Obj()).
							Obj(),
					).
					Generation(1).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             kueue.WorkloadQuotaReserved,
						Message:            "Quota reserved in ClusterQueue sales",
						ObservedGeneration: 1,
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionTrue,
						Reason:             kueue.WorkloadAdmitted,
						Message:            "The workload is admitted",
						ObservedGeneration: 1,
						LastTransitionTime: metav1.NewTime(now),
					}).
					Obj(),
			},
			eventCmpOpts: ignoreEventMessageCmpOpts,
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("sales", "foo", "QuotaReserved", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("sales", "foo", "Admitted", corev1.EventTypeNormal).Obj(),
			},
		},
		"skip workload with missing or deleted ClusterQueue (NoFit)": {
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("missing-cq-workload", "sales").
					Queue("non-existent-queue").
					PodSets(*utiltestingapi.MakePodSet("set", 1).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Generation(1).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("missing-cq-workload", "sales").
					Queue("non-existent-queue").
					PodSets(*utiltestingapi.MakePodSet("set", 1).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Generation(1).
					Obj(),
			},
			// Expect no panics and workload skipped.
			wantLeft:     nil,
			wantEvents:   nil,
			eventCmpOpts: ignoreEventMessageCmpOpts,
		},
		"workload fits in single clusterQueue, with check state pending": {
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("foo", "sales").
					Queue("main").
					PodSets(*utiltestingapi.MakePodSet("one", 10).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "check",
						State: kueue.CheckStatePending,
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("foo", "sales").
					Queue("main").
					PodSets(*utiltestingapi.MakePodSet("one", 10).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "check",
						State: kueue.CheckStatePending,
					}).
					Admission(
						utiltestingapi.MakeAdmission("sales").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "default", "10000m").
								Count(10).
								Obj()).
							Obj(),
					).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             kueue.WorkloadQuotaReserved,
						Message:            "Quota reserved in ClusterQueue sales",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"sales/foo": {
					ClusterQueue: "sales",
					PodSetAssignments: []kueue.PodSetAssignment{
						utiltestingapi.MakePodSetAssignment("one").
							Assignment(corev1.ResourceCPU, "default", "10000m").
							Count(10).
							Obj(),
					},
				},
			},
			eventCmpOpts: ignoreEventMessageCmpOpts,
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("sales", "foo", "QuotaReserved", corev1.EventTypeNormal).Obj(),
			},
		},
		"error during admission": {
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("foo", "sales").
					Queue("main").
					PodSets(*utiltestingapi.MakePodSet("one", 10).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("foo", "sales").
					Queue("main").
					PodSets(*utiltestingapi.MakePodSet("one", 10).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			admissionError: errors.New("admission"),
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"sales": {"sales/foo"},
			},
		},
		"single clusterQueue full": {
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("new", "sales").
					Queue("main").
					PodSets(*utiltestingapi.MakePodSet("one", 11).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("assigned", "sales").
					PodSets(*utiltestingapi.MakePodSet("one", 40).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("sales").PodSets(utiltestingapi.MakePodSetAssignment("one").Assignment(corev1.ResourceCPU, "default", "40000m").Count(40).Obj()).Obj(), now).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("assigned", "sales").
					PodSets(*utiltestingapi.MakePodSet("one", 40).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "AdmittedByTest",
						Message:            "Admitted by ClusterQueue sales",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Admission(
						utiltestingapi.MakeAdmission("sales").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "default", "40").
								Count(40).
								Obj()).
							Obj(),
					).
					Obj(),
				*utiltestingapi.MakeWorkload("new", "sales").
					Queue("main").
					PodSets(*utiltestingapi.MakePodSet("one", 11).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "couldn't assign flavors to pod set one: insufficient unused quota for cpu in flavor default, 1 more needed",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "one",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("11"),
						},
					}).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"sales/assigned": {
					ClusterQueue: "sales",
					PodSetAssignments: []kueue.PodSetAssignment{
						utiltestingapi.MakePodSetAssignment("one").
							Assignment(corev1.ResourceCPU, "default", "40000m").
							Count(40).
							Obj(),
					},
				},
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"sales": {"sales/new"},
			},
		},
		"failed to match clusterQueue selector": {
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("new", "sales").
					Queue("blocked").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("new", "sales").
					Queue("blocked").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "Workload namespace doesn't match ClusterQueue selector",
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
			wantInadmissibleLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"eng-alpha": {"sales/new"},
			},
		},
		"admit in different cohorts": {
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("new", "sales").
					Queue("main").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("new", "eng-alpha").
					Queue("main").
					PodSets(*utiltestingapi.MakePodSet("one", 51 /* Will borrow */).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("new", "eng-alpha").
					Queue("main").
					PodSets(*utiltestingapi.MakePodSet("one", 51 /* Will borrow */).
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
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "on-demand", "51").
								Count(51).
								Obj()).
							Obj(),
					).
					Obj(),
				*utiltestingapi.MakeWorkload("new", "sales").
					Queue("main").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "QuotaReserved",
						Message:            "Quota reserved in ClusterQueue sales",
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
						utiltestingapi.MakeAdmission("sales").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "default", "1").
								Count(1).
								Obj()).
							Obj(),
					).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"sales/new": {
					ClusterQueue: "sales",
					PodSetAssignments: []kueue.PodSetAssignment{
						utiltestingapi.MakePodSetAssignment("one").
							Assignment(corev1.ResourceCPU, "default", "1000m").
							Obj(),
					},
				},
				"eng-alpha/new": {
					ClusterQueue: "eng-alpha",
					PodSetAssignments: []kueue.PodSetAssignment{
						utiltestingapi.MakePodSetAssignment("one").
							Assignment(corev1.ResourceCPU, "on-demand", "51000m").
							Count(51).
							Obj(),
					},
				},
			},
		},
		"admit in same cohort with no borrowing": {
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("new", "eng-alpha").
					Queue("main").
					PodSets(*utiltestingapi.MakePodSet("one", 40).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("new", "eng-beta").
					Queue("main").
					PodSets(*utiltestingapi.MakePodSet("one", 40).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("new", "eng-alpha").
					Queue("main").
					PodSets(*utiltestingapi.MakePodSet("one", 40).
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
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "on-demand", "40").
								Count(40).
								Obj()).
							Obj(),
					).
					Obj(),
				*utiltestingapi.MakeWorkload("new", "eng-beta").
					Queue("main").
					PodSets(*utiltestingapi.MakePodSet("one", 40).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "QuotaReserved",
						Message:            "Quota reserved in ClusterQueue eng-beta",
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
						utiltestingapi.MakeAdmission("eng-beta").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "on-demand", "40").
								Count(40).
								Obj()).
							Obj(),
					).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/new": {
					ClusterQueue: "eng-alpha",
					PodSetAssignments: []kueue.PodSetAssignment{
						utiltestingapi.MakePodSetAssignment("one").
							Assignment(corev1.ResourceCPU, "on-demand", "40000m").
							Count(40).
							Obj(),
					},
				},
				"eng-beta/new": {
					ClusterQueue: "eng-beta",
					PodSetAssignments: []kueue.PodSetAssignment{
						utiltestingapi.MakePodSetAssignment("one").
							Assignment(corev1.ResourceCPU, "on-demand", "40000m").
							Count(40).
							Obj(),
					},
				},
			},
		},
		"assign multiple resources and flavors": {
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("new", "eng-beta").
					Queue("main").
					PodSets(
						*utiltestingapi.MakePodSet("one", 10).
							Request(corev1.ResourceCPU, "6").
							Request("example.com/gpu", "1").
							Obj(),
						*utiltestingapi.MakePodSet("two", 40).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("new", "eng-beta").
					Queue("main").
					PodSets(
						*utiltestingapi.MakePodSet("one", 10).
							Request(corev1.ResourceCPU, "6").
							Request("example.com/gpu", "1").
							Obj(),
						*utiltestingapi.MakePodSet("two", 40).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "QuotaReserved",
						Message:            "Quota reserved in ClusterQueue eng-beta",
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
						utiltestingapi.MakeAdmission("eng-beta").
							PodSets(
								utiltestingapi.MakePodSetAssignment("one").
									Assignment(corev1.ResourceCPU, "on-demand", "60000m").
									Assignment("example.com/gpu", "model-a", "10").
									Count(10).
									Obj(),
								utiltestingapi.MakePodSetAssignment("two").
									Assignment(corev1.ResourceCPU, "spot", "40000m").
									Count(40).
									Obj(),
							).
							Obj(),
					).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-beta/new": {
					ClusterQueue: "eng-beta",
					PodSetAssignments: []kueue.PodSetAssignment{
						utiltestingapi.MakePodSetAssignment("one").
							Assignment(corev1.ResourceCPU, "on-demand", "60000m").
							Assignment("example.com/gpu", "model-a", "10").
							Count(10).
							Obj(),
						utiltestingapi.MakePodSetAssignment("two").
							Assignment(corev1.ResourceCPU, "spot", "40000m").
							Count(40).
							Obj(),
					},
				},
			},
		},
		"cannot borrow if cohort was assigned and would result in overadmission": {
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("new", "eng-alpha").
					Queue("main").
					PodSets(*utiltestingapi.MakePodSet("one", 45).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("new", "eng-beta").
					Queue("main").
					PodSets(*utiltestingapi.MakePodSet("one", 56).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("new", "eng-alpha").
					Queue("main").
					PodSets(*utiltestingapi.MakePodSet("one", 45).
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
									Assignment(corev1.ResourceCPU, "on-demand", "45").
									Count(45).
									Obj(),
							).
							Obj(),
					).
					Obj(),
				*utiltestingapi.MakeWorkload("new", "eng-beta").
					Queue("main").
					PodSets(*utiltestingapi.MakePodSet("one", 56).
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
							corev1.ResourceCPU: resource.MustParse("56"),
						},
					}).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/new": {
					ClusterQueue: "eng-alpha",
					PodSetAssignments: []kueue.PodSetAssignment{
						utiltestingapi.MakePodSetAssignment("one").
							Assignment(corev1.ResourceCPU, "on-demand", "45000m").
							Count(45).
							Obj(),
					},
				},
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"eng-beta": {"eng-beta/new"},
			},
		},
		"can borrow if cohort was assigned and will not result in overadmission": {
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("new", "eng-alpha").
					Queue("main").
					PodSets(*utiltestingapi.MakePodSet("one", 45).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("new", "eng-beta").
					Queue("main").
					PodSets(*utiltestingapi.MakePodSet("one", 55).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("new", "eng-alpha").
					Queue("main").
					PodSets(*utiltestingapi.MakePodSet("one", 45).
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
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "on-demand", "45").
								Count(45).
								Obj()).
							Obj(),
					).
					Obj(),
				*utiltestingapi.MakeWorkload("new", "eng-beta").
					Queue("main").
					PodSets(*utiltestingapi.MakePodSet("one", 55).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "QuotaReserved",
						Message:            "Quota reserved in ClusterQueue eng-beta",
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
						utiltestingapi.MakeAdmission("eng-beta").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "on-demand", "55").
								Count(55).
								Obj()).
							Obj(),
					).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/new": {
					ClusterQueue: "eng-alpha",
					PodSetAssignments: []kueue.PodSetAssignment{
						utiltestingapi.MakePodSetAssignment("one").
							Assignment(corev1.ResourceCPU, "on-demand", "45000m").
							Count(45).
							Obj(),
					},
				},
				"eng-beta/new": {
					ClusterQueue: "eng-beta",
					PodSetAssignments: []kueue.PodSetAssignment{
						utiltestingapi.MakePodSetAssignment("one").
							Assignment(corev1.ResourceCPU, "on-demand", "55000m").
							Count(55).
							Obj(),
					},
				},
			},
		},
		"can borrow if needs reclaim from cohort in different flavor": {
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("can-reclaim", "eng-alpha").
					Queue("main").
					Request(corev1.ResourceCPU, "100").
					Obj(),
				*utiltestingapi.MakeWorkload("needs-to-borrow", "eng-beta").
					Queue("main").
					Request(corev1.ResourceCPU, "1").
					Obj(),
				*utiltestingapi.MakeWorkload("user-on-demand", "eng-beta").
					Request(corev1.ResourceCPU, "50").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("eng-beta").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "on-demand", "50000m").
							Obj()).
						Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("user-spot", "eng-beta").
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("eng-beta").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "spot", "1000m").
							Obj()).
						Obj(), now).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("can-reclaim", "eng-alpha").
					Queue("main").
					Request(corev1.ResourceCPU, "100").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor on-demand, 50 more needed, insufficient unused quota for cpu in flavor spot, 1 more needed",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "main",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("100"),
						},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("needs-to-borrow", "eng-beta").
					Queue("main").
					Request(corev1.ResourceCPU, "1").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "QuotaReserved",
						Message:            "Quota reserved in ClusterQueue eng-beta",
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
						utiltestingapi.MakeAdmission("eng-beta").
							PodSets(utiltestingapi.MakePodSetAssignment("main").
								Assignment(corev1.ResourceCPU, "on-demand", "1").
								Count(1).
								Obj()).
							Obj(),
					).
					Obj(),
				*utiltestingapi.MakeWorkload("user-on-demand", "eng-beta").
					Request(corev1.ResourceCPU, "50").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("eng-beta").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "on-demand", "50000m").
							Obj()).
						Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("user-spot", "eng-beta").
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("eng-beta").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "spot", "1000m").
							Obj()).
						Obj(), now).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"eng-alpha": {"eng-alpha/can-reclaim"},
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-beta/user-spot": *utiltestingapi.MakeAdmission("eng-beta").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "spot", "1000m").
						Obj()).
					Obj(),
				"eng-beta/user-on-demand": *utiltestingapi.MakeAdmission("eng-beta").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "on-demand", "50000m").
						Obj()).
					Obj(),
				"eng-beta/needs-to-borrow": *utiltestingapi.MakeAdmission("eng-beta").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "on-demand", "1000m").
						Obj()).
					Obj(),
			},
		},
		"workload exceeds lending limit when borrow in cohort": {
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a", "lend").
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("lend-b").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "2000m").
							Obj()).
						Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("b", "lend").
					Queue("lend-b-queue").
					Request(corev1.ResourceCPU, "3").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a", "lend").
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("lend-b").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "2000m").
							Obj()).
						Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("b", "lend").
					Queue("lend-b-queue").
					Request(corev1.ResourceCPU, "3").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor default, 1 more needed",
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
			wantAssignments: map[workload.Reference]kueue.Admission{
				"lend/a": *utiltestingapi.MakeAdmission("lend-b").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "default", "2000m").
						Obj()).
					Obj(),
			},
			wantInadmissibleLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"lend-b": {"lend/b"},
			},
		},
		// Cohorts in UPPERCASE, ClusterQueues in lowercase.
		//
		//        ROOT
		//         |
		//       CHILD
		//      /     \
		// lender    borrower
		//
		// In (), we display nominal quota.
		// lender(10):   lendingLimit=3
		// borrower(5):  borrowingLimit=10, usage=5
		//
		// lender reserves 10-3=7 CPU and lends at most 3
		// to the cohort. borrower has used all 5 of its
		// nominal quota, so it must borrow to admit more.
		//
		// wl-pending requests 4 CPU, but only 3 are
		// lendable. We expect it to be inadmissible.
		"hierarchical cohort respects lending limit when borrowing": {
			cohorts: []kueue.Cohort{
				*utiltestingapi.MakeCohort("root").Obj(),
				*utiltestingapi.MakeCohort("child").Parent("root").Obj(),
			},
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("cq-lender").
					Cohort("child").
					NamespaceSelector(&metav1.LabelSelector{
						MatchExpressions: []metav1.LabelSelectorRequirement{{
							Key:      "dep",
							Operator: metav1.LabelSelectorOpIn,
							Values:   []string{"eng"},
						}},
					}).
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("10").LendingLimit("3").Append().
							Obj(),
					).Obj(),
				*utiltestingapi.MakeClusterQueue("cq-borrower").
					Cohort("child").
					NamespaceSelector(&metav1.LabelSelector{
						MatchExpressions: []metav1.LabelSelectorRequirement{{
							Key:      "dep",
							Operator: metav1.LabelSelectorOpIn,
							Values:   []string{"eng"},
						}},
					}).
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("5").BorrowingLimit("10").Append().
							Obj(),
					).Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltestingapi.MakeLocalQueue("lq-lender", "eng-alpha").ClusterQueue("cq-lender").Obj(),
				*utiltestingapi.MakeLocalQueue("lq-borrower", "eng-alpha").ClusterQueue("cq-borrower").Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-existing", "eng-alpha").
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						Request(corev1.ResourceCPU, "5").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-borrower", "main").
						PodSets(utiltestingapi.MakePodSetAssignment("main").
							Assignment(corev1.ResourceCPU, "on-demand", "5").
							Obj()).
						Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-pending", "eng-alpha").
					Queue("lq-borrower").
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						Request(corev1.ResourceCPU, "4").
						Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-existing", "eng-alpha").
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						Request(corev1.ResourceCPU, "5").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-borrower", "main").
						PodSets(utiltestingapi.MakePodSetAssignment("main").
							Assignment(corev1.ResourceCPU, "on-demand", "5").
							Obj()).
						Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-pending", "eng-alpha").
					Queue("lq-borrower").
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						Request(corev1.ResourceCPU, "4").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor on-demand, 1 more needed",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "main",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("4"),
						},
					}).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/wl-existing": *utiltestingapi.MakeAdmission("cq-borrower", "main").
					PodSets(utiltestingapi.MakePodSetAssignment("main").
						Assignment(corev1.ResourceCPU, "on-demand", "5").
						Obj()).
					Obj(),
			},
			wantInadmissibleLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"cq-borrower": {"eng-alpha/wl-pending"},
			},
		},
		// Cohorts in UPPERCASE, ClusterQueues in lowercase.
		//
		//        ROOT
		//         |
		//       CHILD
		//      /     \
		// lender    borrower
		//
		// In (), we display nominal quota.
		// lender(10):   lendingLimit=5
		// borrower(5):  borrowingLimit=10, usage=5
		//
		// lender reserves 10-5=5 CPU and lends at most 5
		// to the cohort. borrower has used all 5 of its
		// nominal quota, so it must borrow to admit more.
		//
		// wl-borrowing requests 5 CPU, which exactly
		// matches the lendable amount. We expect it to
		// be admitted.
		"hierarchical cohort allows borrowing up to lending limit": {
			cohorts: []kueue.Cohort{
				*utiltestingapi.MakeCohort("root").Obj(),
				*utiltestingapi.MakeCohort("child").Parent("root").Obj(),
			},
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("cq-lender").
					Cohort("child").
					NamespaceSelector(&metav1.LabelSelector{
						MatchExpressions: []metav1.LabelSelectorRequirement{{
							Key:      "dep",
							Operator: metav1.LabelSelectorOpIn,
							Values:   []string{"eng"},
						}},
					}).
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("10").LendingLimit("5").Append().
							Obj(),
					).Obj(),
				*utiltestingapi.MakeClusterQueue("cq-borrower").
					Cohort("child").
					NamespaceSelector(&metav1.LabelSelector{
						MatchExpressions: []metav1.LabelSelectorRequirement{{
							Key:      "dep",
							Operator: metav1.LabelSelectorOpIn,
							Values:   []string{"eng"},
						}},
					}).
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("5").BorrowingLimit("10").Append().
							Obj(),
					).Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltestingapi.MakeLocalQueue("lq-lender", "eng-alpha").ClusterQueue("cq-lender").Obj(),
				*utiltestingapi.MakeLocalQueue("lq-borrower", "eng-alpha").ClusterQueue("cq-borrower").Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-existing", "eng-alpha").
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						Request(corev1.ResourceCPU, "5").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-borrower", "main").
						PodSets(utiltestingapi.MakePodSetAssignment("main").
							Assignment(corev1.ResourceCPU, "on-demand", "5").
							Obj()).
						Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-borrowing", "eng-alpha").
					Queue("lq-borrower").
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						Request(corev1.ResourceCPU, "5").
						Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-borrowing", "eng-alpha").
					Queue("lq-borrower").
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						Request(corev1.ResourceCPU, "5").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "QuotaReserved",
						Message:            "Quota reserved in ClusterQueue cq-borrower",
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
						utiltestingapi.MakeAdmission("cq-borrower").
							PodSets(
								utiltestingapi.MakePodSetAssignment("main").
									Assignment(corev1.ResourceCPU, "on-demand", "5").
									Count(1).
									Obj(),
							).
							Obj(),
					).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-existing", "eng-alpha").
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						Request(corev1.ResourceCPU, "5").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-borrower", "main").
						PodSets(utiltestingapi.MakePodSetAssignment("main").
							Assignment(corev1.ResourceCPU, "on-demand", "5").
							Obj()).
						Obj(), now).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/wl-existing": *utiltestingapi.MakeAdmission("cq-borrower", "main").
					PodSets(utiltestingapi.MakePodSetAssignment("main").
						Assignment(corev1.ResourceCPU, "on-demand", "5").
						Obj()).
					Obj(),
				"eng-alpha/wl-borrowing": *utiltestingapi.MakeAdmission("cq-borrower", "main").
					PodSets(utiltestingapi.MakePodSetAssignment("main").
						Assignment(corev1.ResourceCPU, "on-demand", "5").
						Obj()).
					Obj(),
			},
		},
		"preempt workloads in ClusterQueue and cohort": {
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("preemptor", "eng-beta").
					UID("wl-preemptor").
					JobUID("job-preemptor").
					Queue("main").
					Request(corev1.ResourceCPU, "20").
					Obj(),
				*utiltestingapi.MakeWorkload("use-all-spot", "eng-alpha").
					Request(corev1.ResourceCPU, "100").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("eng-alpha").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "spot", "100000m").
							Obj()).
						Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("low-1", "eng-beta").
					Priority(-1).
					Request(corev1.ResourceCPU, "30").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("eng-beta").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "on-demand", "30000m").
							Obj()).
						Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("low-2", "eng-beta").
					Priority(-2).
					Request(corev1.ResourceCPU, "10").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("eng-beta").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "on-demand", "10000m").
							Obj()).
						Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("borrower", "eng-alpha").
					Request(corev1.ResourceCPU, "60").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("eng-alpha").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "on-demand", "60000m").
							Obj()).
						Obj(), now).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("borrower", "eng-alpha").
					Request(corev1.ResourceCPU, "60").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("eng-alpha").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "on-demand", "60000m").
							Obj()).
						Obj(), now).
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
				*utiltestingapi.MakeWorkload("use-all-spot", "eng-alpha").
					Request(corev1.ResourceCPU, "100").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("eng-alpha").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "spot", "100000m").
							Obj()).
						Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("low-1", "eng-beta").
					Priority(-1).
					Request(corev1.ResourceCPU, "30").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("eng-beta").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "on-demand", "30000m").
							Obj()).
						Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("low-2", "eng-beta").
					Priority(-2).
					Request(corev1.ResourceCPU, "10").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("eng-beta").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "on-demand", "10000m").
							Obj()).
						Obj(), now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-preemptor, JobUID: job-preemptor) due to prioritization in the ClusterQueue; preemptor path: /eng/eng-beta; preemptee path: /eng/eng-beta",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InClusterQueue",
						Message:            "Preempted to accommodate a workload (UID: wl-preemptor, JobUID: job-preemptor) due to prioritization in the ClusterQueue; preemptor path: /eng/eng-beta; preemptee path: /eng/eng-beta",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("preemptor", "eng-beta").
					UID("wl-preemptor").
					JobUID("job-preemptor").
					Queue("main").
					Request(corev1.ResourceCPU, "20").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor on-demand, 20 more needed, insufficient unused quota for cpu in flavor spot, 20 more needed. Pending the preemption of 2 workload(s)",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "main",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("20"),
						},
					}).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				// Preemptor is not admitted in this cycle.
				"eng-beta": {"eng-beta/preemptor"},
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/use-all-spot": *utiltestingapi.MakeAdmission("eng-alpha").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "spot", "100").
						Obj()).
					Obj(),
				"eng-beta/low-1": *utiltestingapi.MakeAdmission("eng-beta").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "on-demand", "30").
						Obj()).
					Obj(),
				// Removal from cache for the preempted workloads is deferred until we receive Workload updates
				"eng-beta/low-2": *utiltestingapi.MakeAdmission("eng-beta").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "on-demand", "10").
						Obj()).
					Obj(),
				"eng-alpha/borrower": *utiltestingapi.MakeAdmission("eng-alpha").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "on-demand", "60").
						Obj()).
					Obj(),
			},
		},
		"multiple CQs need preemption": {
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("other-alpha").
					Cohort("other").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "50", "50").Obj(),
					).
					Obj(),
				*utiltestingapi.MakeClusterQueue("other-beta").
					Cohort("other").
					Preemption(kueue.ClusterQueuePreemption{
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
					}).
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "50", "10").Obj(),
					).
					Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltestingapi.MakeLocalQueue("other", "eng-alpha").ClusterQueue("other-alpha").Obj(),
				*utiltestingapi.MakeLocalQueue("other", "eng-beta").ClusterQueue("other-beta").Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("preemptor", "eng-beta").
					UID("wl-preemptor").
					JobUID("job-preemptor").
					Priority(-1).
					Queue("other").
					Request(corev1.ResourceCPU, "1").
					Obj(),
				*utiltestingapi.MakeWorkload("pending", "eng-alpha").
					Priority(1).
					Queue("other").
					Request(corev1.ResourceCPU, "1").
					Obj(),
				*utiltestingapi.MakeWorkload("use-all", "eng-alpha").
					Request(corev1.ResourceCPU, "100").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("other-alpha").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "on-demand", "100").
							Obj()).
						Obj(), now).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("pending", "eng-alpha").
					Priority(1).
					Queue("other").
					Request(corev1.ResourceCPU, "1").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor on-demand, 1 more needed",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "main",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("1"),
						},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("use-all", "eng-alpha").
					Request(corev1.ResourceCPU, "100").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("other-alpha").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "on-demand", "100").
							Obj()).
						Obj(), now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-preemptor, JobUID: job-preemptor) due to reclamation within the cohort; preemptor path: /other/other-beta; preemptee path: /other/other-alpha",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InCohortReclamation",
						Message:            "Preempted to accommodate a workload (UID: wl-preemptor, JobUID: job-preemptor) due to reclamation within the cohort; preemptor path: /other/other-beta; preemptee path: /other/other-alpha",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("preemptor", "eng-beta").
					UID("wl-preemptor").
					JobUID("job-preemptor").
					Priority(-1).
					Queue("other").
					Request(corev1.ResourceCPU, "1").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor on-demand, 1 more needed. Pending the preemption of 1 workload(s)",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "main",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("1"),
						},
					}).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				// Preemptor is not admitted in this cycle.
				"other-beta": {"eng-beta/preemptor"},
			},
			wantInadmissibleLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"other-alpha": {"eng-alpha/pending"},
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				// Removal from cache for the preempted workloads is deferred until we receive Workload updates
				"eng-alpha/use-all": *utiltestingapi.MakeAdmission("other-alpha").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "on-demand", "100").
						Obj()).
					Obj(),
			},
		},
		"cannot borrow resource not listed in clusterQueue": {
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("new", "eng-alpha").
					Queue("main").
					Request("example.com/gpu", "1").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("new", "eng-alpha").
					Queue("main").
					Request("example.com/gpu", "1").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "couldn't assign flavors to pod set main: resource example.com/gpu unavailable in ClusterQueue",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "main",
						Resources: corev1.ResourceList{
							"example.com/gpu": resource.MustParse("1"),
						},
					}).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"eng-alpha": {"eng-alpha/new"},
			},
		},
		"not enough resources to borrow, fallback to next flavor; WhenCanPreempt: TryNextFlavor": {
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("new", "eng-alpha").
					Queue("main").
					PodSets(*utiltestingapi.MakePodSet("one", 60).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("existing", "eng-beta").
					PodSets(*utiltestingapi.MakePodSet("one", 45).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("eng-beta").PodSets(utiltestingapi.MakePodSetAssignment("one").Assignment(corev1.ResourceCPU, "on-demand", "45000m").Count(45).Obj()).Obj(), now).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("new", "eng-alpha").
					Queue("main").
					PodSets(*utiltestingapi.MakePodSet("one", 60).
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
									Assignment(corev1.ResourceCPU, "spot", "60").
									Count(60).
									Obj(),
							).
							Obj(),
					).
					Obj(),
				*utiltestingapi.MakeWorkload("existing", "eng-beta").
					PodSets(*utiltestingapi.MakePodSet("one", 45).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("eng-beta").PodSets(utiltestingapi.MakePodSetAssignment("one").Assignment(corev1.ResourceCPU, "on-demand", "45000m").Count(45).Obj()).Obj(), now).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/new": {
					ClusterQueue: "eng-alpha",
					PodSetAssignments: []kueue.PodSetAssignment{
						utiltestingapi.MakePodSetAssignment("one").
							Assignment(corev1.ResourceCPU, "spot", "60000m").
							Count(60).
							Obj(),
					},
				},
				"eng-beta/existing": {
					ClusterQueue: "eng-beta",
					PodSetAssignments: []kueue.PodSetAssignment{
						utiltestingapi.MakePodSetAssignment("one").
							Assignment(corev1.ResourceCPU, "on-demand", "45000m").
							Count(45).
							Obj(),
					},
				},
			},
		},
		"workload should not fit in nonexistent clusterQueue": {
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("foo", "sales").
					Queue("cq-nonexistent-queue").
					Request(corev1.ResourceCPU, "1").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("foo", "sales").
					Queue("cq-nonexistent-queue").
					Request(corev1.ResourceCPU, "1").
					Obj(),
			},
		},
		"workload should not fit in clusterQueue with nonexistent flavor": {
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("foo", "sales").
					Queue("flavor-nonexistent-queue").
					Request(corev1.ResourceCPU, "1").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("foo", "sales").
					Queue("flavor-nonexistent-queue").
					Request(corev1.ResourceCPU, "1").
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"flavor-nonexistent-cq": {"sales/foo"},
			},
		},
		"no overadmission while borrowing": {
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("new", "eng-beta").
					Queue("main").
					Creation(now.Add(-2 * time.Second)).
					PodSets(*utiltestingapi.MakePodSet("one", 50).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("new-alpha", "eng-alpha").
					Queue("main").
					Creation(now.Add(-time.Second)).
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("new-gamma", "eng-gamma").
					Queue("main").
					Creation(now).
					PodSets(*utiltestingapi.MakePodSet("one", 50).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("existing", "eng-gamma").
					PodSets(
						*utiltestingapi.MakePodSet("borrow-on-demand", 51).
							Request(corev1.ResourceCPU, "1").
							Obj(),
						*utiltestingapi.MakePodSet("use-all-spot", 100).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("eng-gamma").
						PodSets(
							utiltestingapi.MakePodSetAssignment("borrow-on-demand").
								Assignment(corev1.ResourceCPU, "on-demand", "51").
								Count(51).
								Obj(),
							utiltestingapi.MakePodSetAssignment("use-all-spot").
								Assignment(corev1.ResourceCPU, "spot", "100").
								Count(100).
								Obj(),
						).
						Obj(), now).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("new-alpha", "eng-alpha").
					Queue("main").
					Creation(now.Add(-time.Second)).
					PodSets(*utiltestingapi.MakePodSet("one", 1).
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
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "on-demand", "1").
								Count(1).
								Obj()).
							Obj(),
					).
					Obj(),
				*utiltestingapi.MakeWorkload("new", "eng-beta").
					Queue("main").
					Creation(now.Add(-2 * time.Second)).
					PodSets(*utiltestingapi.MakePodSet("one", 50).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "QuotaReserved",
						Message:            "Quota reserved in ClusterQueue eng-beta",
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
						utiltestingapi.MakeAdmission("eng-beta").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "on-demand", "50").
								Count(50).
								Obj()).
							Obj(),
					).
					Obj(),
				*utiltestingapi.MakeWorkload("existing", "eng-gamma").
					PodSets(
						*utiltestingapi.MakePodSet("borrow-on-demand", 51).
							Request(corev1.ResourceCPU, "1").
							Obj(),
						*utiltestingapi.MakePodSet("use-all-spot", 100).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("eng-gamma").
						PodSets(
							utiltestingapi.MakePodSetAssignment("borrow-on-demand").
								Assignment(corev1.ResourceCPU, "on-demand", "51").
								Count(51).
								Obj(),
							utiltestingapi.MakePodSetAssignment("use-all-spot").
								Assignment(corev1.ResourceCPU, "spot", "100").
								Count(100).
								Obj(),
						).
						Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("new-gamma", "eng-gamma").
					Queue("main").
					Creation(now).
					PodSets(*utiltestingapi.MakePodSet("one", 50).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "couldn't assign flavors to pod set one: insufficient unused quota for cpu in flavor on-demand, 41 more needed, insufficient unused quota for cpu in flavor spot, 50 more needed",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "one",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("50"),
						},
					}).
					Obj(),
			},
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("eng-gamma").
					Cohort("eng").
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
					Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltestingapi.MakeLocalQueue("main", "eng-gamma").ClusterQueue("eng-gamma").Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-gamma/existing": *utiltestingapi.MakeAdmission("eng-gamma").
					PodSets(
						utiltestingapi.MakePodSetAssignment("borrow-on-demand").
							Assignment(corev1.ResourceCPU, "on-demand", "51").
							Count(51).
							Obj(),
						utiltestingapi.MakePodSetAssignment("use-all-spot").
							Assignment(corev1.ResourceCPU, "spot", "100").
							Count(100).
							Obj(),
					).Obj(),
				"eng-beta/new":        *utiltestingapi.MakeAdmission("eng-beta").PodSets(utiltestingapi.MakePodSetAssignment("one").Assignment(corev1.ResourceCPU, "on-demand", "50").Count(50).Obj()).Obj(),
				"eng-alpha/new-alpha": *utiltestingapi.MakeAdmission("eng-alpha").PodSets(utiltestingapi.MakePodSetAssignment("one").Assignment(corev1.ResourceCPU, "on-demand", "1").Obj()).Obj(),
			},
			wantInadmissibleLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"eng-gamma": {"eng-gamma/new-gamma"},
			},
			wantSkippedPreemptions: map[string]int{
				"eng-alpha": 0,
				"eng-beta":  0,
				"eng-gamma": 0,
			},
		},
		"partial admission single variable pod set": {
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("new", "sales").
					Queue("main").
					PodSets(*utiltestingapi.MakePodSet("one", 50).
						SetMinimumCount(20).
						Request(corev1.ResourceCPU, "2").
						Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("new", "sales").
					Queue("main").
					PodSets(*utiltestingapi.MakePodSet("one", 50).
						SetMinimumCount(20).
						Request(corev1.ResourceCPU, "2").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "QuotaReserved",
						Message:            "Quota reserved in ClusterQueue sales",
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
						utiltestingapi.MakeAdmission("sales").
							PodSets(
								utiltestingapi.MakePodSetAssignment("one").
									Assignment(corev1.ResourceCPU, "default", "50").
									Count(25).
									Obj(),
							).
							Obj(),
					).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"sales/new": {
					ClusterQueue: "sales",
					PodSetAssignments: []kueue.PodSetAssignment{
						utiltestingapi.MakePodSetAssignment("one").
							Assignment(corev1.ResourceCPU, "default", "50000m").
							Count(25).
							Obj(),
					},
				},
			},
		},
		"partial admission single variable pod set, preempt first": {
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("new", "eng-beta").
					UID("wl-new").
					JobUID("job-new").
					Queue("main").
					Priority(4).
					PodSets(*utiltestingapi.MakePodSet("one", 20).
						SetMinimumCount(10).
						Request("example.com/gpu", "1").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("old", "eng-beta").
					Priority(-4).
					PodSets(*utiltestingapi.MakePodSet("one", 10).
						Request("example.com/gpu", "1").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("eng-beta").PodSets(utiltestingapi.MakePodSetAssignment("one").Assignment("example.com/gpu", "model-a", "10").Count(10).Obj()).Obj(), now).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("new", "eng-beta").
					UID("wl-new").
					JobUID("job-new").
					Queue("main").
					Priority(4).
					PodSets(*utiltestingapi.MakePodSet("one", 20).
						SetMinimumCount(10).
						Request("example.com/gpu", "1").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "couldn't assign flavors to pod set one: insufficient unused quota for example.com/gpu in flavor model-a, 10 more needed. Pending the preemption of 1 workload(s)",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "one",
						Resources: corev1.ResourceList{
							"example.com/gpu": resource.MustParse("20"),
						},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("old", "eng-beta").
					Priority(-4).
					PodSets(*utiltestingapi.MakePodSet("one", 10).
						Request("example.com/gpu", "1").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("eng-beta").PodSets(utiltestingapi.MakePodSetAssignment("one").Assignment("example.com/gpu", "model-a", "10").Count(10).Obj()).Obj(), now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-new, JobUID: job-new) due to prioritization in the ClusterQueue; preemptor path: /eng/eng-beta; preemptee path: /eng/eng-beta",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InClusterQueue",
						Message:            "Preempted to accommodate a workload (UID: wl-new, JobUID: job-new) due to prioritization in the ClusterQueue; preemptor path: /eng/eng-beta; preemptee path: /eng/eng-beta",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-beta/old": {
					ClusterQueue: "eng-beta",
					PodSetAssignments: []kueue.PodSetAssignment{
						utiltestingapi.MakePodSetAssignment("one").
							Assignment("example.com/gpu", "model-a", "10").
							Count(10).
							Obj(),
					},
				},
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"eng-beta": {"eng-beta/new"},
			},
		},
		"partial admission single variable pod set, preempt with partial admission": {
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("new", "eng-beta").
					UID("wl-new").
					JobUID("job-new").
					Queue("main").
					Priority(4).
					PodSets(*utiltestingapi.MakePodSet("one", 30).
						SetMinimumCount(10).
						Request("example.com/gpu", "1").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("old", "eng-beta").
					Priority(-4).
					PodSets(*utiltestingapi.MakePodSet("one", 10).
						Request("example.com/gpu", "1").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("eng-beta").PodSets(utiltestingapi.MakePodSetAssignment("one").Assignment("example.com/gpu", "model-a", "10").Count(10).Obj()).Obj(), now).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("new", "eng-beta").
					UID("wl-new").
					JobUID("job-new").
					Queue("main").
					Priority(4).
					PodSets(*utiltestingapi.MakePodSet("one", 30).
						SetMinimumCount(10).
						Request("example.com/gpu", "1").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "couldn't assign flavors to pod set one: insufficient unused quota for example.com/gpu in flavor model-a, 10 more needed. Pending the preemption of 1 workload(s)",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "one",
						Resources: corev1.ResourceList{
							"example.com/gpu": resource.MustParse("30"),
						},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("old", "eng-beta").
					Priority(-4).
					PodSets(*utiltestingapi.MakePodSet("one", 10).
						Request("example.com/gpu", "1").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("eng-beta").PodSets(utiltestingapi.MakePodSetAssignment("one").Assignment("example.com/gpu", "model-a", "10").Count(10).Obj()).Obj(), now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-new, JobUID: job-new) due to prioritization in the ClusterQueue; preemptor path: /eng/eng-beta; preemptee path: /eng/eng-beta",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InClusterQueue",
						Message:            "Preempted to accommodate a workload (UID: wl-new, JobUID: job-new) due to prioritization in the ClusterQueue; preemptor path: /eng/eng-beta; preemptee path: /eng/eng-beta",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-beta/old": {
					ClusterQueue: "eng-beta",
					PodSetAssignments: []kueue.PodSetAssignment{
						utiltestingapi.MakePodSetAssignment("one").
							Assignment("example.com/gpu", "model-a", "10").
							Count(10).
							Obj(),
					},
				},
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"eng-beta": {"eng-beta/new"},
			},
		},
		"partial admission multiple variable pod sets": {
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("new", "sales").
					Queue("main").
					PodSets(
						*utiltestingapi.MakePodSet("one", 20).
							Request(corev1.ResourceCPU, "1").
							Obj(),
						*utiltestingapi.MakePodSet("two", 30).
							SetMinimumCount(10).
							Request(corev1.ResourceCPU, "1").
							Obj(),
						*utiltestingapi.MakePodSet("three", 15).
							SetMinimumCount(5).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("new", "sales").
					Queue("main").
					PodSets(
						*utiltestingapi.MakePodSet("one", 20).
							Request(corev1.ResourceCPU, "1").
							Obj(),
						*utiltestingapi.MakePodSet("two", 30).
							SetMinimumCount(10).
							Request(corev1.ResourceCPU, "1").
							Obj(),
						*utiltestingapi.MakePodSet("three", 15).
							SetMinimumCount(5).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "QuotaReserved",
						Message:            "Quota reserved in ClusterQueue sales",
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
						utiltestingapi.MakeAdmission("sales").
							PodSets(
								utiltestingapi.MakePodSetAssignment("one").
									Assignment(corev1.ResourceCPU, "default", "20").
									Count(20).
									Obj(),
								utiltestingapi.MakePodSetAssignment("two").
									Assignment(corev1.ResourceCPU, "default", "20").
									Count(20).
									Obj(),
								utiltestingapi.MakePodSetAssignment("three").
									Assignment(corev1.ResourceCPU, "default", "10").
									Count(10).
									Obj(),
							).
							Obj(),
					).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"sales/new": {
					ClusterQueue: "sales",
					PodSetAssignments: []kueue.PodSetAssignment{
						utiltestingapi.MakePodSetAssignment("one").
							Assignment(corev1.ResourceCPU, "default", "20000m").
							Count(20).
							Obj(),
						utiltestingapi.MakePodSetAssignment("two").
							Assignment(corev1.ResourceCPU, "default", "20000m").
							Count(20).
							Obj(),
						utiltestingapi.MakePodSetAssignment("three").
							Assignment(corev1.ResourceCPU, "default", "10000m").
							Count(10).
							Obj(),
					},
				},
			},
		},
		"partial admission disabled, multiple variable pod sets": {
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("new", "sales").
					Queue("main").
					PodSets(
						*utiltestingapi.MakePodSet("one", 20).
							Request(corev1.ResourceCPU, "1").
							Obj(),
						*utiltestingapi.MakePodSet("two", 30).
							SetMinimumCount(10).
							Request(corev1.ResourceCPU, "1").
							Obj(),
						*utiltestingapi.MakePodSet("three", 15).
							SetMinimumCount(5).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("new", "sales").
					Queue("main").
					PodSets(
						*utiltestingapi.MakePodSet("one", 20).
							Request(corev1.ResourceCPU, "1").
							Obj(),
						*utiltestingapi.MakePodSet("two", 30).
							SetMinimumCount(10).
							Request(corev1.ResourceCPU, "1").
							Obj(),
						*utiltestingapi.MakePodSet("three", 15).
							SetMinimumCount(5).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "couldn't assign flavors to pod set three: insufficient quota for cpu in flavor default, previously considered podsets requests (50) + current podset request (15) > maximum capacity (50)",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(
						kueue.PodSetRequest{
							Name: "one",
							Resources: corev1.ResourceList{
								corev1.ResourceCPU: resource.MustParse("20"),
							},
						},
						kueue.PodSetRequest{
							Name: "two",
							Resources: corev1.ResourceList{
								corev1.ResourceCPU: resource.MustParse("30"),
							},
						},
						kueue.PodSetRequest{
							Name: "three",
							Resources: corev1.ResourceList{
								corev1.ResourceCPU: resource.MustParse("15"),
							},
						},
					).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"sales": {"sales/new"},
			},
			disablePartialAdmission: true,
		},
		"two workloads can borrow different resources from the same flavor in the same cycle": {
			additionalClusterQueues: func() []kueue.ClusterQueue {
				preemption := kueue.ClusterQueuePreemption{
					ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
				}
				rg := *utiltestingapi.MakeFlavorQuotas("default").Resource("r1", "10", "10").Resource("r2", "10", "10").Obj()
				cq1 := *utiltestingapi.MakeClusterQueue("cq1").Cohort("co").Preemption(preemption).ResourceGroup(rg).Obj()
				cq2 := *utiltestingapi.MakeClusterQueue("cq2").Cohort("co").Preemption(preemption).ResourceGroup(rg).Obj()
				cq3 := *utiltestingapi.MakeClusterQueue("cq3").Cohort("co").Preemption(preemption).ResourceGroup(rg).Obj()
				return []kueue.ClusterQueue{cq1, cq2, cq3}
			}(),
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltestingapi.MakeLocalQueue("lq1", "sales").ClusterQueue("cq1").Obj(),
				*utiltestingapi.MakeLocalQueue("lq2", "sales").ClusterQueue("cq2").Obj(),
				*utiltestingapi.MakeLocalQueue("lq3", "sales").ClusterQueue("cq3").Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl1", "sales").Queue("lq1").Priority(-1).PodSets(
					*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request("r1", "16").Obj(),
				).Obj(),
				*utiltestingapi.MakeWorkload("wl2", "sales").Queue("lq2").Priority(-2).PodSets(
					*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request("r2", "16").Obj(),
				).Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl1", "sales").Queue("lq1").Priority(-1).PodSets(
					*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request("r1", "16").Obj(),
				).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "QuotaReserved",
						Message:            "Quota reserved in ClusterQueue cq1",
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
						utiltestingapi.MakeAdmission("cq1").
							PodSets(
								utiltestingapi.MakePodSetAssignment("main").
									Assignment("r1", "default", "16").
									Count(1).
									Obj(),
							).
							Obj(),
					).
					Obj(),
				*utiltestingapi.MakeWorkload("wl2", "sales").Queue("lq2").Priority(-2).PodSets(
					*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request("r2", "16").Obj(),
				).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "QuotaReserved",
						Message:            "Quota reserved in ClusterQueue cq2",
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
						utiltestingapi.MakeAdmission("cq2").
							PodSets(
								utiltestingapi.MakePodSetAssignment("main").
									Assignment("r2", "default", "16").
									Count(1).
									Obj(),
							).
							Obj(),
					).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"sales/wl1": *utiltestingapi.MakeAdmission("cq1").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment("r1", "default", "16").
						Obj()).
					Obj(),
				"sales/wl2": *utiltestingapi.MakeAdmission("cq2").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment("r2", "default", "16").
						Obj()).
					Obj(),
			},
		},
		"two workloads can borrow the same resources from the same flavor in the same cycle if fits in the cohort quota": {
			additionalClusterQueues: func() []kueue.ClusterQueue {
				preemption := kueue.ClusterQueuePreemption{
					ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
				}
				rg := *utiltestingapi.MakeFlavorQuotas("default").Resource("r1", "10", "10").Resource("r2", "10", "10").Obj()
				cq1 := *utiltestingapi.MakeClusterQueue("cq1").Cohort("co").Preemption(preemption).ResourceGroup(rg).Obj()
				cq2 := *utiltestingapi.MakeClusterQueue("cq2").Cohort("co").Preemption(preemption).ResourceGroup(rg).Obj()
				cq3 := *utiltestingapi.MakeClusterQueue("cq3").Cohort("co").Preemption(preemption).ResourceGroup(rg).Obj()
				return []kueue.ClusterQueue{cq1, cq2, cq3}
			}(),
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltestingapi.MakeLocalQueue("lq1", "sales").ClusterQueue("cq1").Obj(),
				*utiltestingapi.MakeLocalQueue("lq2", "sales").ClusterQueue("cq2").Obj(),
				*utiltestingapi.MakeLocalQueue("lq3", "sales").ClusterQueue("cq3").Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl1", "sales").Queue("lq1").Priority(-1).PodSets(
					*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request("r1", "16").Obj(),
				).Obj(),
				*utiltestingapi.MakeWorkload("wl2", "sales").Queue("lq2").Priority(-2).PodSets(
					*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request("r1", "14").Obj(),
				).Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl1", "sales").Queue("lq1").Priority(-1).PodSets(
					*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request("r1", "16").Obj(),
				).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "QuotaReserved",
						Message:            "Quota reserved in ClusterQueue cq1",
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
						utiltestingapi.MakeAdmission("cq1").
							PodSets(
								utiltestingapi.MakePodSetAssignment("main").
									Assignment("r1", "default", "16").
									Count(1).
									Obj(),
							).
							Obj(),
					).
					Obj(),
				*utiltestingapi.MakeWorkload("wl2", "sales").Queue("lq2").Priority(-2).PodSets(
					*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request("r1", "14").Obj(),
				).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "QuotaReserved",
						Message:            "Quota reserved in ClusterQueue cq2",
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
						utiltestingapi.MakeAdmission("cq2").
							PodSets(
								utiltestingapi.MakePodSetAssignment("main").
									Assignment("r1", "default", "14").
									Count(1).
									Obj(),
							).
							Obj(),
					).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"sales/wl1": *utiltestingapi.MakeAdmission("cq1").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment("r1", "default", "16").
						Obj()).
					Obj(),
				"sales/wl2": *utiltestingapi.MakeAdmission("cq2").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment("r1", "default", "14").
						Obj()).
					Obj(),
			},
		},
		"only one workload can borrow one resources from the same flavor in the same cycle if cohort quota cannot fit": {
			additionalClusterQueues: func() []kueue.ClusterQueue {
				preemption := kueue.ClusterQueuePreemption{
					ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
				}
				rg := *utiltestingapi.MakeFlavorQuotas("default").Resource("r1", "10", "10").Resource("r2", "10", "10").Obj()
				cq1 := *utiltestingapi.MakeClusterQueue("cq1").Cohort("co").Preemption(preemption).ResourceGroup(rg).Obj()
				cq2 := *utiltestingapi.MakeClusterQueue("cq2").Cohort("co").Preemption(preemption).ResourceGroup(rg).Obj()
				cq3 := *utiltestingapi.MakeClusterQueue("cq3").Cohort("co").Preemption(preemption).ResourceGroup(rg).Obj()
				return []kueue.ClusterQueue{cq1, cq2, cq3}
			}(),
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltestingapi.MakeLocalQueue("lq1", "sales").ClusterQueue("cq1").Obj(),
				*utiltestingapi.MakeLocalQueue("lq2", "sales").ClusterQueue("cq2").Obj(),
				*utiltestingapi.MakeLocalQueue("lq3", "sales").ClusterQueue("cq3").Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl1", "sales").Queue("lq1").Priority(-1).PodSets(
					*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request("r1", "16").Obj(),
				).Obj(),
				*utiltestingapi.MakeWorkload("wl2", "sales").Queue("lq2").Priority(-2).PodSets(
					*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request("r1", "16").Obj(),
				).Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl1", "sales").Queue("lq1").Priority(-1).PodSets(
					*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request("r1", "16").Obj(),
				).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "QuotaReserved",
						Message:            "Quota reserved in ClusterQueue cq1",
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
						utiltestingapi.MakeAdmission("cq1").
							PodSets(
								utiltestingapi.MakePodSetAssignment("main").
									Assignment("r1", "default", "16").
									Count(1).
									Obj(),
							).
							Obj(),
					).
					Obj(),
				*utiltestingapi.MakeWorkload("wl2", "sales").Queue("lq2").Priority(-2).PodSets(
					*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request("r1", "16").Obj(),
				).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "Workload no longer fits after processing another workload",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "main",
						Resources: corev1.ResourceList{
							"r1": resource.MustParse("16"),
						},
					}).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"sales/wl1": *utiltestingapi.MakeAdmission("cq1").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment("r1", "default", "16").
						Obj()).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"cq2": {"sales/wl2"},
			},
		},
		"preemption while borrowing, workload waiting for preemption should not block a borrowing workload in another CQ": {
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("cq_shared").
					Cohort("preemption-while-borrowing").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "4", "0").Obj()).
					Obj(),
				*utiltestingapi.MakeClusterQueue("cq_a").
					Cohort("preemption-while-borrowing").
					Preemption(kueue.ClusterQueuePreemption{
						ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
						BorrowWithinCohort: &kueue.BorrowWithinCohort{
							Policy: kueue.BorrowWithinCohortPolicyLowerPriority,
						},
					}).
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default").
							Resource(corev1.ResourceCPU, "0", "3").Obj(),
					).
					Obj(),
				*utiltestingapi.MakeClusterQueue("cq_b").
					Cohort("preemption-while-borrowing").
					Preemption(kueue.ClusterQueuePreemption{
						ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
						BorrowWithinCohort: &kueue.BorrowWithinCohort{
							Policy: kueue.BorrowWithinCohortPolicyLowerPriority,
						},
					}).
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default").
							Resource(corev1.ResourceCPU, "0").Obj(),
					).
					Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltestingapi.MakeLocalQueue("lq_a", "eng-alpha").ClusterQueue("cq_a").Obj(),
				*utiltestingapi.MakeLocalQueue("lq_b", "eng-beta").ClusterQueue("cq_b").Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a", "eng-alpha").
					Queue("lq_a").
					Creation(now.Add(time.Second)).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
						Request(corev1.ResourceCPU, "3").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("b", "eng-beta").
					Queue("lq_b").
					Creation(now.Add(2 * time.Second)).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("admitted_a", "eng-alpha").
					Queue("lq_a").
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							Request(corev1.ResourceCPU, "2").
							Obj(),
					).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq_a").
						PodSets(
							utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "2").
								Obj(),
						).
						Obj(), now).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a", "eng-alpha").
					Queue("lq_a").
					Creation(now.Add(time.Second)).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
						Request(corev1.ResourceCPU, "3").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor default, 2 more needed",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "main",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("3"),
						},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("admitted_a", "eng-alpha").
					Queue("lq_a").
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							Request(corev1.ResourceCPU, "2").
							Obj(),
					).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq_a").
						PodSets(
							utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "2").
								Obj(),
						).
						Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("b", "eng-beta").
					Queue("lq_b").
					Creation(now.Add(2 * time.Second)).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "QuotaReserved",
						Message:            "Quota reserved in ClusterQueue cq_b",
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
						utiltestingapi.MakeAdmission("cq_b").
							PodSets(
								utiltestingapi.MakePodSetAssignment("main").
									Assignment(corev1.ResourceCPU, "default", "1").
									Count(1).
									Obj(),
							).
							Obj(),
					).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/admitted_a": {
					ClusterQueue: "cq_a",
					PodSetAssignments: []kueue.PodSetAssignment{
						utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "2").
							Obj(),
					},
				},
				"eng-beta/b": {
					ClusterQueue: "cq_b",
					PodSetAssignments: []kueue.PodSetAssignment{
						utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "1").
							Obj(),
					},
				},
			},
			wantInadmissibleLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"cq_a": {"eng-alpha/a"},
			},
		},
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
			wantInadmissibleLeft: nil,
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
			wantInadmissibleLeft: nil,
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
		"minimal preemptions when target queue is exhausted": {
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("other-alpha").
					Cohort("other").
					Preemption(kueue.ClusterQueuePreemption{
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
					}).
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "2").Obj(),
					).
					Obj(),
				*utiltestingapi.MakeClusterQueue("other-beta").
					Cohort("other").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "2").Obj(),
					).
					Obj(),
				*utiltestingapi.MakeClusterQueue("other-gamma").
					Cohort("other").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "2").Obj(),
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
					Priority(-2).
					Queue("other").
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("other-alpha").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "on-demand", "1").
							Obj()).
						Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("a2", "eng-alpha").
					Priority(-2).
					Queue("other").
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("other-alpha").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "on-demand", "1").
							Obj()).
						Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("a3", "eng-alpha").
					Priority(-1).
					Queue("other").
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("other-alpha").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "on-demand", "1").
							Obj()).
						Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("b1", "eng-beta").
					Priority(0).
					Queue("other").
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("other-beta").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "on-demand", "1").
							Obj()).
						Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("b2", "eng-beta").
					Priority(0).
					Queue("other").
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("other-beta").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "on-demand", "1").
							Obj()).
						Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("b3", "eng-beta").
					Priority(0).
					Queue("other").
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("other-beta").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "on-demand", "1").
							Obj()).
						Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("incoming", "eng-alpha").
					UID("wl-incoming").
					JobUID("job-incoming").
					Priority(0).
					Queue("other").
					Request(corev1.ResourceCPU, "2").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a1", "eng-alpha").
					Priority(-2).
					Queue("other").
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("other-alpha").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "on-demand", "1").
							Obj()).
						Obj(), now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-incoming, JobUID: job-incoming) due to prioritization in the ClusterQueue; preemptor path: /other/other-alpha; preemptee path: /other/other-alpha",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InClusterQueue",
						Message:            "Preempted to accommodate a workload (UID: wl-incoming, JobUID: job-incoming) due to prioritization in the ClusterQueue; preemptor path: /other/other-alpha; preemptee path: /other/other-alpha",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("a2", "eng-alpha").
					Priority(-2).
					Queue("other").
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("other-alpha").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "on-demand", "1").
							Obj()).
						Obj(), now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-incoming, JobUID: job-incoming) due to prioritization in the ClusterQueue; preemptor path: /other/other-alpha; preemptee path: /other/other-alpha",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InClusterQueue",
						Message:            "Preempted to accommodate a workload (UID: wl-incoming, JobUID: job-incoming) due to prioritization in the ClusterQueue; preemptor path: /other/other-alpha; preemptee path: /other/other-alpha",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("a3", "eng-alpha").
					Priority(-1).
					Queue("other").
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("other-alpha").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "on-demand", "1").
							Obj()).
						Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("incoming", "eng-alpha").
					UID("wl-incoming").
					JobUID("job-incoming").
					Priority(0).
					Queue("other").
					Request(corev1.ResourceCPU, "2").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor on-demand, 2 more needed. Pending the preemption of 2 workload(s)",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "main",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("2"),
						},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("b1", "eng-beta").
					Priority(0).
					Queue("other").
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("other-beta").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "on-demand", "1").
							Obj()).
						Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("b2", "eng-beta").
					Priority(0).
					Queue("other").
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("other-beta").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "on-demand", "1").
							Obj()).
						Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("b3", "eng-beta").
					Priority(0).
					Queue("other").
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("other-beta").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "on-demand", "1").
							Obj()).
						Obj(), now).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"other-alpha": {"eng-alpha/incoming"},
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/a1": *utiltestingapi.MakeAdmission("other-alpha").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "on-demand", "1").
						Obj()).
					Obj(),
				"eng-alpha/a2": *utiltestingapi.MakeAdmission("other-alpha").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "on-demand", "1").
						Obj()).
					Obj(),
				"eng-alpha/a3": *utiltestingapi.MakeAdmission("other-alpha").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "on-demand", "1").
						Obj()).
					Obj(),
				"eng-beta/b1": *utiltestingapi.MakeAdmission("other-beta").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "on-demand", "1").
						Obj()).
					Obj(),
				"eng-beta/b2": *utiltestingapi.MakeAdmission("other-beta").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "on-demand", "1").
						Obj()).
					Obj(),
				"eng-beta/b3": *utiltestingapi.MakeAdmission("other-beta").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "on-demand", "1").
						Obj()).
					Obj(),
			},
		},
		"A workload is only eligible to do preemptions if it fits fully within nominal quota": {
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("other-alpha").
					Cohort("other").
					Preemption(kueue.ClusterQueuePreemption{
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
					}).
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "2").Obj(),
					).
					Obj(),
				*utiltestingapi.MakeClusterQueue("other-beta").
					Cohort("other").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "2").Obj(),
					).
					Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltestingapi.MakeLocalQueue("other", "eng-alpha").ClusterQueue("other-alpha").Obj(),
				*utiltestingapi.MakeLocalQueue("other", "eng-beta").ClusterQueue("other-beta").Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a1", "eng-alpha").
					Priority(-1).
					Queue("other").
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("other-alpha").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "on-demand", "1").
							Obj()).
						Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("b1", "eng-beta").
					Priority(-1).
					Queue("other").
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("other-beta").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "on-demand", "1").
							Obj()).
						Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("incoming", "eng-alpha").
					Priority(1).
					Queue("other").
					Request(corev1.ResourceCPU, "3").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a1", "eng-alpha").
					Priority(-1).
					Queue("other").
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("other-alpha").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "on-demand", "1").
							Obj()).
						Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("incoming", "eng-alpha").
					Priority(1).
					Queue("other").
					Request(corev1.ResourceCPU, "3").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor on-demand, 1 more needed",
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
					Priority(-1).
					Queue("other").
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("other-beta").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "on-demand", "1").
							Obj()).
						Obj(), now).
					Obj(),
			},
			wantInadmissibleLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"other-alpha": {"eng-alpha/incoming"},
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/a1": *utiltestingapi.MakeAdmission("other-alpha").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "on-demand", "1").
						Obj()).
					Obj(),
				"eng-beta/b1": *utiltestingapi.MakeAdmission("other-beta").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "on-demand", "1").
						Obj()).
					Obj(),
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
						Message:            "Preempted to accommodate a workload (UID: wl-preemptor, JobUID: job-preemptor) due to Fair Sharing within the cohort; preemptor path: /eng/eng-beta; preemptee path: /eng/eng-alpha",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InCohortFairSharing",
						Message:            "Preempted to accommodate a workload (UID: wl-preemptor, JobUID: job-preemptor) due to Fair Sharing within the cohort; preemptor path: /eng/eng-beta; preemptee path: /eng/eng-alpha",
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
						Message:            "Preempted to accommodate a workload (UID: wl-preemptor, JobUID: job-preemptor) due to Fair Sharing within the cohort; preemptor path: /eng/eng-beta; preemptee path: /eng/eng-gamma",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InCohortFairSharing",
						Message:            "Preempted to accommodate a workload (UID: wl-preemptor, JobUID: job-preemptor) due to Fair Sharing within the cohort; preemptor path: /eng/eng-beta; preemptee path: /eng/eng-gamma",
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
		"multiple preemptions without borrowing": {
			// While requiring the same shared FlavorResource (Default, cpu),
			// multiple workloads are able to issue preemptions on workloads within
			// their own CQs in a single scheduling cycle.
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("other-alpha").
					Cohort("other").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
					}).
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default").
							Resource(corev1.ResourceCPU, "2").Obj(),
					).
					Obj(),
				*utiltestingapi.MakeClusterQueue("other-beta").
					Cohort("other").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
					}).
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default").
							Resource(corev1.ResourceCPU, "2").Obj(),
					).
					Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltestingapi.MakeLocalQueue("other", "eng-alpha").ClusterQueue("other-alpha").Obj(),
				*utiltestingapi.MakeLocalQueue("other", "eng-beta").ClusterQueue("other-beta").Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a1", "eng-alpha").
					Priority(0).
					Queue("other").
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("other-alpha").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "2").
							Obj()).
						Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("b1", "eng-beta").
					Priority(0).
					Queue("other").
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("other-beta").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "2").
							Obj()).
						Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("preemptor", "eng-alpha").
					UID("wl-preemptor").
					JobUID("job-preemptor").
					Priority(100).
					Queue("other").
					Request(corev1.ResourceCPU, "2").
					Obj(),
				*utiltestingapi.MakeWorkload("preemptor", "eng-beta").
					UID("wl-preemptor").
					JobUID("job-preemptor").
					Priority(100).
					Queue("other").
					Request(corev1.ResourceCPU, "2").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a1", "eng-alpha").
					Priority(0).
					Queue("other").
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("other-alpha").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "2").
							Obj()).
						Obj(), now).
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
					Request(corev1.ResourceCPU, "2").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor default, 2 more needed. Pending the preemption of 1 workload(s)",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "main",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("2"),
						},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("b1", "eng-beta").
					Priority(0).
					Queue("other").
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("other-beta").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "2").
							Obj()).
						Obj(), now).
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
					Request(corev1.ResourceCPU, "2").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor default, 2 more needed. Pending the preemption of 1 workload(s)",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "main",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("2"),
						},
					}).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"other-alpha": {"eng-alpha/preemptor"},
				"other-beta":  {"eng-beta/preemptor"},
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/a1": *utiltestingapi.MakeAdmission("other-alpha").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "default", "2").
						Obj()).
					Obj(),
				"eng-beta/b1": *utiltestingapi.MakeAdmission("other-beta").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "default", "2").
						Obj()).
					Obj(),
			},
			wantSkippedPreemptions: map[string]int{
				"other-alpha": 0,
				"other-beta":  0,
			},
		},
		"multiple preemptions preemption possible after earlier workload fits": {
			// When one workload is assigned Fit,
			// and another Preempt, the Fit workload doesn't block
			// the preempting workload in the same cycle.
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("other-alpha").
					Cohort("other").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
					}).
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default").
							Resource(corev1.ResourceCPU, "1").Obj(),
					).
					Obj(),
				*utiltestingapi.MakeClusterQueue("other-beta").
					Cohort("other").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
					}).
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default").
							Resource(corev1.ResourceCPU, "2").Obj(),
					).
					Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltestingapi.MakeLocalQueue("other", "eng-alpha").ClusterQueue("other-alpha").Obj(),
				*utiltestingapi.MakeLocalQueue("other", "eng-beta").ClusterQueue("other-beta").Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("b1", "eng-beta").
					Priority(0).
					Queue("other").
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("other-beta").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "2").
							Obj()).
						Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("fit", "eng-alpha").
					Priority(100).
					Queue("other").
					Request(corev1.ResourceCPU, "1").
					Obj(),
				*utiltestingapi.MakeWorkload("preemptor", "eng-beta").
					UID("wl-preemptor").
					JobUID("job-preemptor").
					Priority(99).
					Queue("other").
					Request(corev1.ResourceCPU, "2").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("fit", "eng-alpha").
					Priority(100).
					Queue("other").
					Request(corev1.ResourceCPU, "1").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "QuotaReserved",
						Message:            "Quota reserved in ClusterQueue other-alpha",
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
						utiltestingapi.MakeAdmission("other-alpha").
							PodSets(
								utiltestingapi.MakePodSetAssignment("main").
									Assignment(corev1.ResourceCPU, "default", "1").
									Count(1).
									Obj(),
							).
							Obj(),
					).
					Obj(),
				*utiltestingapi.MakeWorkload("b1", "eng-beta").
					Priority(0).
					Queue("other").
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("other-beta").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "2").
							Obj()).
						Obj(), now).
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
					Priority(99).
					Queue("other").
					Request(corev1.ResourceCPU, "2").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor default, 1 more needed. Pending the preemption of 1 workload(s)",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "main",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("2"),
						},
					}).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"other-beta": {"eng-beta/preemptor"},
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/fit": *utiltestingapi.MakeAdmission("other-alpha").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "default", "1").
						Obj()).
					Obj(),
				"eng-beta/b1": *utiltestingapi.MakeAdmission("other-beta").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "default", "2").
						Obj()).
					Obj(),
			},
			wantSkippedPreemptions: map[string]int{
				"other-alpha": 0,
				"other-beta":  0,
			},
		},
		"multiple preemptions skip preemption when shared limited resource": {
			// The two preempting workloads, each requesting 3 CPU,
			// require capacity in the Cohort in addition to preemption. We make sure
			// that we don't do a wasteful preemption, as only one of the
			// workloads can fit even after two preemptions.
			//
			// Evicted workloads: request 4
			// Preempting workloads: request 6
			// Cohort has capacity 5
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("other-alpha").
					Cohort("other").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
						BorrowWithinCohort: &kueue.BorrowWithinCohort{
							Policy: kueue.BorrowWithinCohortPolicyLowerPriority,
						},
					}).
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default").
							Resource(corev1.ResourceCPU, "2").Obj(),
					).
					Obj(),
				*utiltestingapi.MakeClusterQueue("other-beta").
					Cohort("other").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
						BorrowWithinCohort: &kueue.BorrowWithinCohort{
							Policy: kueue.BorrowWithinCohortPolicyLowerPriority,
						},
					}).
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default").
							Resource(corev1.ResourceCPU, "2").Obj(),
					).
					Obj(),
				*utiltestingapi.MakeClusterQueue("resource-bank").
					Cohort("other").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default").
							Resource(corev1.ResourceCPU, "1").Obj(),
					).
					Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltestingapi.MakeLocalQueue("other", "eng-alpha").ClusterQueue("other-alpha").Obj(),
				*utiltestingapi.MakeLocalQueue("other", "eng-beta").ClusterQueue("other-beta").Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a1", "eng-alpha").
					Priority(0).
					Queue("other").
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("other-alpha").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "2").
							Obj()).
						Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("b1", "eng-beta").
					Priority(0).
					Queue("other").
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("other-beta").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "2").
							Obj()).
						Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("preemptor", "eng-alpha").
					UID("wl-preemptor").
					JobUID("job-preemptor").
					Priority(100).
					Queue("other").
					Request(corev1.ResourceCPU, "3").
					Obj(),
				*utiltestingapi.MakeWorkload("pretending-preemptor", "eng-beta").
					Priority(99).
					Queue("other").
					Request(corev1.ResourceCPU, "3").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a1", "eng-alpha").
					Priority(0).
					Queue("other").
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("other-alpha").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "2").
							Obj()).
						Obj(), now).
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
						Message:            "couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor default, 2 more needed. Pending the preemption of 1 workload(s)",
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
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("other-beta").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "2").
							Obj()).
						Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("pretending-preemptor", "eng-beta").
					Priority(99).
					Queue("other").
					Request(corev1.ResourceCPU, "3").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "Workload no longer fits after processing another workload",
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
				"other-beta":  {"eng-beta/pretending-preemptor"},
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/a1": *utiltestingapi.MakeAdmission("other-alpha").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "default", "2").
						Obj()).
					Obj(),
				"eng-beta/b1": *utiltestingapi.MakeAdmission("other-beta").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "default", "2").
						Obj()).
					Obj(),
			},
			wantSkippedPreemptions: map[string]int{
				"other-alpha": 0,
				"other-beta":  1,
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
		"not enough resources": {
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
		"container does not satisfy limitRange constraints": {
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("new", "sales").
					Queue("main").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "500m").
						Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("new", "sales").
					Queue("main").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "500m").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "resources didn't satisfy LimitRange constraints: spec.podSets[0].template.spec.containers[0]: Invalid value: [\"cpu\"]: requests must not be above the limitRange max",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "one",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("500m"),
						},
					}).
					Obj(),
			},
			objects: []client.Object{
				utiltesting.MakeLimitRange("alpha", "sales").
					WithType(corev1.LimitTypeContainer).
					WithValue("Max", corev1.ResourceCPU, "300m").
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"sales": {"sales/new"},
			},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("sales", "new", "Pending", corev1.EventTypeWarning).
					Message(fmt.Sprintf("%s: %s",
						errLimitRangeConstraintsUnsatisfiedResources,
						field.Invalid(
							workload.PodSetsPath.Index(0).Child("template").Child("spec").Child("containers").Index(0),
							[]corev1.ResourceName{corev1.ResourceCPU},
							limitrange.RequestsMustNotBeAboveLimitRangeMaxMessage,
						).Error(),
					)).
					Obj(),
			},
		},
		"container resource requests exceed limits": {
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("new", "sales").
					Queue("main").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "200m").
						Limit(corev1.ResourceCPU, "100m").
						Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("new", "sales").
					Queue("main").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "200m").
						Limit(corev1.ResourceCPU, "100m").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "resources validation failed: spec.podSets[0].template.spec.containers[0]: Invalid value: [\"cpu\"]: requests must not exceed its limits",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "one",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("200m"),
						},
					}).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"sales": {"sales/new"},
			},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("sales", "new", "Pending", corev1.EventTypeWarning).
					Message(fmt.Sprintf("%s: %s",
						errInvalidWLResources,
						field.Invalid(
							workload.PodSetsPath.Index(0).Child("template").Child("spec").Child("containers").Index(0),
							[]corev1.ResourceName{corev1.ResourceCPU}, workload.RequestsMustNotExceedLimitMessage,
						).Error(),
					)).
					Obj(),
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
		"prefer reclamation over cq priority based preemption": {
			// Flavor 1, on-demand, requires preemption of workload in CQ.
			// Flavor 2, spot, requires preemption of workload in Cohort which
			// is borrowing from CQ.
			// Flavor 2 is a better assignment, so we preempt in it.
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("other-alpha").
					Cohort("other").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
					}).
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").Resource("gpu", "10").Obj(),
						*utiltestingapi.MakeFlavorQuotas("spot").Resource("gpu", "10").Obj(),
					).
					Obj(),
				*utiltestingapi.MakeClusterQueue("other-beta").
					Cohort("other").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
					}).
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").Resource("gpu", "0").Obj(),
						*utiltestingapi.MakeFlavorQuotas("spot").Resource("gpu", "0").Obj(),
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
					Request("gpu", "6").
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
					Request("gpu", "6").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "couldn't assign flavors to pod set main: insufficient unused quota for gpu in flavor on-demand, 1 more needed, insufficient unused quota for gpu in flavor spot, 1 more needed. Pending the preemption of 1 workload(s)",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "main",
						Resources: corev1.ResourceList{
							"gpu": resource.MustParse("6"),
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
						Message:            "Preempted to accommodate a workload (UID: wl-preemptor, JobUID: job-preemptor) due to reclamation within the cohort; preemptor path: /other/other-alpha; preemptee path: /other/other-beta",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InCohortReclamation",
						Message:            "Preempted to accommodate a workload (UID: wl-preemptor, JobUID: job-preemptor) due to reclamation within the cohort; preemptor path: /other/other-alpha; preemptee path: /other/other-beta",
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
		"prefer first preemption flavor when second flavor requires both reclaim and cq priority preemption": {
			// Flavor 1, on-demand, requires preemption of workload in CQ.
			// Flavor 2, spot, requires preemption of workload in Cohort and CQ
			// Since Flavor 2 doesn't improve the assignment, we prefer Flavor 1.
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("other-alpha").
					Cohort("other").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
					}).
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").Resource("gpu", "10").Obj(),
						*utiltestingapi.MakeFlavorQuotas("spot").Resource("gpu", "10").Obj(),
					).
					Obj(),
				*utiltestingapi.MakeClusterQueue("other-beta").
					Cohort("other").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").Resource("gpu", "0").Obj(),
						*utiltestingapi.MakeFlavorQuotas("spot").Resource("gpu", "0").Obj(),
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
				*utiltestingapi.MakeWorkload("a2", "eng-alpha").
					Priority(50).
					Queue("other").
					Request("gpu", "5").
					SimpleReserveQuota("other-alpha", "spot", now).
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
					Request("gpu", "6").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a1", "eng-alpha").
					Priority(50).
					Queue("other").
					Request("gpu", "5").
					SimpleReserveQuota("other-alpha", "on-demand", now).
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
				*utiltestingapi.MakeWorkload("a2", "eng-alpha").
					Priority(50).
					Queue("other").
					Request("gpu", "5").
					SimpleReserveQuota("other-alpha", "spot", now).
					Obj(),
				*utiltestingapi.MakeWorkload("preemptor", "eng-alpha").
					UID("wl-preemptor").
					JobUID("job-preemptor").
					Priority(100).
					Queue("other").
					Request("gpu", "6").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "couldn't assign flavors to pod set main: insufficient unused quota for gpu in flavor on-demand, 1 more needed, insufficient unused quota for gpu in flavor spot, 6 more needed. Pending the preemption of 1 workload(s)",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "main",
						Resources: corev1.ResourceList{
							"gpu": resource.MustParse("6"),
						},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("b1", "eng-beta").
					Priority(50).
					Queue("other").
					Request("gpu", "5").
					SimpleReserveQuota("other-beta", "spot", now).
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
				"eng-alpha/a2": *utiltestingapi.MakeAdmission("other-alpha").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment("gpu", "spot", "5").
						Obj()).
					Obj(),
				"eng-beta/b1": *utiltestingapi.MakeAdmission("other-beta").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment("gpu", "spot", "5").
						Obj()).
					Obj(),
			},
		},
		"prefer first preemption flavor when second flavor also requires cq preemption": {
			// Flavor 1, on-demand, requires preemption of workload in CQ
			// Flavor 2, spot, also requires preemption of workload in CQ,
			// since the borrowing workload in Cohort is too high priority.
			// Therefore, we choose Flavor 1.
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("other-alpha").
					Cohort("other").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
					}).
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").Resource("gpu", "10").Obj(),
						*utiltestingapi.MakeFlavorQuotas("spot").Resource("gpu", "10").Obj(),
					).
					Obj(),
				*utiltestingapi.MakeClusterQueue("other-beta").
					Cohort("other").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").Resource("gpu", "0").Obj(),
						*utiltestingapi.MakeFlavorQuotas("spot").Resource("gpu", "0").Obj(),
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
					Request("gpu", "6").
					SimpleReserveQuota("other-alpha", "on-demand", now).
					Obj(),
				*utiltestingapi.MakeWorkload("a2", "eng-alpha").
					Priority(50).
					Queue("other").
					Request("gpu", "5").
					SimpleReserveQuota("other-alpha", "spot", now).
					Obj(),
				*utiltestingapi.MakeWorkload("b1", "eng-beta").
					// b1 is too high priority for preemptor.
					Priority(9001).
					Queue("other").
					Request("gpu", "5").
					SimpleReserveQuota("other-beta", "spot", now).
					Obj(),
				*utiltestingapi.MakeWorkload("preemptor", "eng-alpha").
					UID("wl-preemptor").
					JobUID("job-preemptor").
					Priority(100).
					Queue("other").
					Request("gpu", "5").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a1", "eng-alpha").
					Priority(50).
					Queue("other").
					Request("gpu", "6").
					SimpleReserveQuota("other-alpha", "on-demand", now).
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
				*utiltestingapi.MakeWorkload("a2", "eng-alpha").
					Priority(50).
					Queue("other").
					Request("gpu", "5").
					SimpleReserveQuota("other-alpha", "spot", now).
					Obj(),
				*utiltestingapi.MakeWorkload("preemptor", "eng-alpha").
					UID("wl-preemptor").
					JobUID("job-preemptor").
					Priority(100).
					Queue("other").
					Request("gpu", "5").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "couldn't assign flavors to pod set main: insufficient unused quota for gpu in flavor on-demand, 1 more needed, insufficient unused quota for gpu in flavor spot, 5 more needed. Pending the preemption of 1 workload(s)",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "main",
						Resources: corev1.ResourceList{
							"gpu": resource.MustParse("5"),
						},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("b1", "eng-beta").
					// b1 is too high priority for preemptor.
					Priority(9001).
					Queue("other").
					Request("gpu", "5").
					SimpleReserveQuota("other-beta", "spot", now).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"other-alpha": {"eng-alpha/preemptor"},
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/a1": *utiltestingapi.MakeAdmission("other-alpha").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment("gpu", "on-demand", "6").
						Obj()).
					Obj(),
				"eng-alpha/a2": *utiltestingapi.MakeAdmission("other-alpha").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment("gpu", "spot", "5").
						Obj()).
					Obj(),
				"eng-beta/b1": *utiltestingapi.MakeAdmission("other-beta").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment("gpu", "spot", "5").
						Obj()).
					Obj(),
			},
		},
		"workload requiring reclaimation prioritized over wl in another full cq": {
			// Also see #3405.
			//
			// CQ2 is lending out capacity to its
			// Cohort. It has a pending workload, WL2,
			// that fits within nominal capacity, and a
			// reclaim policy set to any.
			//
			// CQ1 is using half of its capacity, and is
			// also lending out remaining capacity.
			//
			// CQ3 has no capacity of its own, and is
			// borrowing 10 nominal capacity.
			//
			// With a pending workloads WL1 and WL2 queued
			// in CQ1 and CQ2 respectively, we want to
			// make sure that the WL2 is processed first,
			// so that its preemption calculations are not
			// invalidated by CQ1's WL1, which won't fit
			// into its nominal capacity given the
			// admitted Admitted-Workload-1.
			//
			// As WL1 has an earlier creation timestamp
			// than WL2, there was a bug where it would
			// process first, reserving capacity which
			// invalidated WL2's preemption calculations,
			// blocking it indefinitely from reclaiming
			// its nominal capacity.
			//
			// We don't test legacy mode as it classifies
			// inadmissible/left different, and we will
			// delete that logic shortly.
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("CQ1").
					Cohort("other").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").Resource("gpu", "10").Obj(),
					).
					Obj(),
				*utiltestingapi.MakeClusterQueue("CQ2").
					Cohort("other").
					Preemption(kueue.ClusterQueuePreemption{
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").Resource("gpu", "10").Obj(),
					).
					Obj(),
				*utiltestingapi.MakeClusterQueue("CQ3").
					Cohort("other").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").Resource("gpu", "0").Obj(),
					).
					Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltestingapi.MakeLocalQueue("lq", "eng-alpha").ClusterQueue("CQ1").Obj(),
				*utiltestingapi.MakeLocalQueue("lq", "eng-beta").ClusterQueue("CQ2").Obj(),
				*utiltestingapi.MakeLocalQueue("lq", "eng-gamma").ClusterQueue("CQ3").Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("Admitted-Workload-1", "eng-alpha").
					Queue("lq").
					Request("gpu", "5").
					SimpleReserveQuota("CQ1", "on-demand", now).
					Obj(),
				*utiltestingapi.MakeWorkload("WL1", "eng-alpha").
					Creation(now).
					Queue("lq").
					Request("gpu", "10").
					Obj(),
				*utiltestingapi.MakeWorkload("WL2", "eng-beta").
					UID("wl-WL2").
					JobUID("job-WL2").
					Creation(now.Add(time.Second)).
					Queue("lq").
					Request("gpu", "10").
					Obj(),
				*utiltestingapi.MakeWorkload("Admitted-Workload-2", "eng-gamma").
					Queue("lq").
					Priority(0).
					Request("gpu", "5").
					SimpleReserveQuota("CQ3", "on-demand", now).
					Obj(),
				*utiltestingapi.MakeWorkload("Admitted-Workload-3", "eng-gamma").
					Queue("lq").
					Priority(1).
					Request("gpu", "5").
					SimpleReserveQuota("CQ3", "on-demand", now).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("Admitted-Workload-1", "eng-alpha").
					Queue("lq").
					Request("gpu", "5").
					SimpleReserveQuota("CQ1", "on-demand", now).
					Obj(),
				*utiltestingapi.MakeWorkload("WL1", "eng-alpha").
					Creation(now).
					Queue("lq").
					Request("gpu", "10").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "couldn't assign flavors to pod set main: insufficient unused quota for gpu in flavor on-demand, 5 more needed",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "main",
						Resources: corev1.ResourceList{
							"gpu": resource.MustParse("10"),
						},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("WL2", "eng-beta").
					UID("wl-WL2").
					JobUID("job-WL2").
					Creation(now.Add(time.Second)).
					Queue("lq").
					Request("gpu", "10").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "couldn't assign flavors to pod set main: insufficient unused quota for gpu in flavor on-demand, 5 more needed. Pending the preemption of 1 workload(s)",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "main",
						Resources: corev1.ResourceList{
							"gpu": resource.MustParse("10"),
						},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("Admitted-Workload-2", "eng-gamma").
					Queue("lq").
					Priority(0).
					Request("gpu", "5").
					SimpleReserveQuota("CQ3", "on-demand", now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-WL2, JobUID: job-WL2) due to reclamation within the cohort; preemptor path: /other/CQ2; preemptee path: /other/CQ3",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InCohortReclamation",
						Message:            "Preempted to accommodate a workload (UID: wl-WL2, JobUID: job-WL2) due to reclamation within the cohort; preemptor path: /other/CQ2; preemptee path: /other/CQ3",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("Admitted-Workload-3", "eng-gamma").
					Queue("lq").
					Priority(1).
					Request("gpu", "5").
					SimpleReserveQuota("CQ3", "on-demand", now).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"CQ2": {"eng-beta/WL2"},
			},
			wantInadmissibleLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"CQ1": {"eng-alpha/WL1"},
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/Admitted-Workload-1": *utiltestingapi.MakeAdmission("CQ1").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment("gpu", "on-demand", "5").
						Obj()).
					Obj(),
				"eng-gamma/Admitted-Workload-2": *utiltestingapi.MakeAdmission("CQ3").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment("gpu", "on-demand", "5").
						Obj()).
					Obj(),
				"eng-gamma/Admitted-Workload-3": *utiltestingapi.MakeAdmission("CQ3").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment("gpu", "on-demand", "5").
						Obj()).
					Obj(),
			},
		},
		// ClusterQueueA has 2 capacity, 1 admitted workload (req=1), and 1 pending workload (req=2)
		// ClusterQueueB has 0 capacity, and 1 pending workload (req=1)
		// Since ClusterQueueA knows it can reclaim capacity, it lets ClusterQueueB borrow.
		"capacity not blocked when lending clusterqueue can reclaim (ReclaimWithinCohort=Any)": {
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("ClusterQueueA").
					Cohort("root").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").Resource("gpu", "2").Obj(),
					).
					Preemption(kueue.ClusterQueuePreemption{
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					Obj(),
				*utiltestingapi.MakeClusterQueue("ClusterQueueB").
					Cohort("root").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").Resource("gpu", "0").Obj(),
					).
					Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltestingapi.MakeLocalQueue("lq", "eng-alpha").ClusterQueue("ClusterQueueA").Obj(),
				*utiltestingapi.MakeLocalQueue("lq", "eng-beta").ClusterQueue("ClusterQueueB").Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a1-admitted", "eng-alpha").
					Queue("lq").
					Request("gpu", "1").
					SimpleReserveQuota("ClusterQueueA", "on-demand", now).
					Obj(),
				*utiltestingapi.MakeWorkload("a2-pending", "eng-alpha").
					Queue("lq").
					Request("gpu", "2").
					Obj(),
				*utiltestingapi.MakeWorkload("b1-pending", "eng-beta").
					Creation(now).
					Queue("lq").
					Request("gpu", "1").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a1-admitted", "eng-alpha").
					Queue("lq").
					Request("gpu", "1").
					SimpleReserveQuota("ClusterQueueA", "on-demand", now).
					Obj(),
				*utiltestingapi.MakeWorkload("a2-pending", "eng-alpha").
					Queue("lq").
					Request("gpu", "2").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "couldn't assign flavors to pod set main: insufficient unused quota for gpu in flavor on-demand, 1 more needed",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "main",
						Resources: corev1.ResourceList{
							"gpu": resource.MustParse("2"),
						},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("b1-pending", "eng-beta").
					Creation(now).
					Queue("lq").
					Request("gpu", "1").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "QuotaReserved",
						Message:            "Quota reserved in ClusterQueue ClusterQueueB",
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
						utiltestingapi.MakeAdmission("ClusterQueueB").
							PodSets(
								utiltestingapi.MakePodSetAssignment("main").
									Assignment("gpu", "on-demand", "1").
									Count(1).
									Obj(),
							).
							Obj(),
					).
					Obj(),
			},
			wantLeft: nil,
			wantInadmissibleLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"ClusterQueueA": {"eng-alpha/a2-pending"},
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/a1-admitted": *utiltestingapi.MakeAdmission("ClusterQueueA").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment("gpu", "on-demand", "1").
						Obj()).
					Obj(),
				"eng-beta/b1-pending": *utiltestingapi.MakeAdmission("ClusterQueueB").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment("gpu", "on-demand", "1").
						Obj()).
					Obj(),
			},
		},
		// ClusterQueueA has 2 capacity, 1 admitted workload (req=1), and 1 pending workload (req=2)
		// ClusterQueueB has 0 capacity, and 1 pending workload (req=1)
		// Since ClusterQueueA is not sure that it can reclaim this capacity, it doesn't let ClusterQueueB borrow.
		"capacity blocked when lending clusterqueue not guaranteed to reclaim (ReclaimWithinCohort=LowerPriority)": {
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("ClusterQueueA").
					Cohort("root").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").Resource("gpu", "2").Obj(),
					).
					Preemption(kueue.ClusterQueuePreemption{
						ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
					}).
					Obj(),
				*utiltestingapi.MakeClusterQueue("ClusterQueueB").
					Cohort("root").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").Resource("gpu", "0").Obj(),
					).
					Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltestingapi.MakeLocalQueue("lq", "eng-alpha").ClusterQueue("ClusterQueueA").Obj(),
				*utiltestingapi.MakeLocalQueue("lq", "eng-beta").ClusterQueue("ClusterQueueB").Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a1-admitted", "eng-alpha").
					Queue("lq").
					Request("gpu", "1").
					SimpleReserveQuota("ClusterQueueA", "on-demand", now).
					Obj(),
				*utiltestingapi.MakeWorkload("a2-pending", "eng-alpha").
					Queue("lq").
					Request("gpu", "2").
					Obj(),
				*utiltestingapi.MakeWorkload("b1-pending", "eng-beta").
					Creation(now).
					Queue("lq").
					Request("gpu", "1").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a1-admitted", "eng-alpha").
					Queue("lq").
					Request("gpu", "1").
					SimpleReserveQuota("ClusterQueueA", "on-demand", now).
					Obj(),
				*utiltestingapi.MakeWorkload("a2-pending", "eng-alpha").
					Queue("lq").
					Request("gpu", "2").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "couldn't assign flavors to pod set main: insufficient unused quota for gpu in flavor on-demand, 1 more needed",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "main",
						Resources: corev1.ResourceList{
							"gpu": resource.MustParse("2"),
						},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("b1-pending", "eng-beta").
					Creation(now).
					Queue("lq").
					Request("gpu", "1").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "Workload no longer fits after processing another workload",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "main",
						Resources: corev1.ResourceList{
							"gpu": resource.MustParse("1"),
						},
					}).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"ClusterQueueB": {"eng-beta/b1-pending"},
			},
			wantInadmissibleLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"ClusterQueueA": {"eng-alpha/a2-pending"},
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/a1-admitted": *utiltestingapi.MakeAdmission("ClusterQueueA").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment("gpu", "on-demand", "1").
						Obj()).
					Obj(),
			},
		},
		// ClusterQueueA has 2 capacity, 1 admitted workload (req=1), and 1 pending workload (req=2)
		// ClusterQueueB has 0 capacity, and 1 pending workload (req=1)
		// Since ClusterQueueA is not sure that it can reclaim this capacity, it doesn't let ClusterQueueB borrow.
		"capacity blocked when lending clusterqueue not guaranteed to reclaim (ReclaimWithinCohort=Never)": {
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("ClusterQueueA").
					Cohort("root").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").Resource("gpu", "2").Obj(),
					).
					Preemption(kueue.ClusterQueuePreemption{
						ReclaimWithinCohort: kueue.PreemptionPolicyNever,
					}).
					Obj(),
				*utiltestingapi.MakeClusterQueue("ClusterQueueB").
					Cohort("root").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").Resource("gpu", "0").Obj(),
					).
					Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltestingapi.MakeLocalQueue("lq", "eng-alpha").ClusterQueue("ClusterQueueA").Obj(),
				*utiltestingapi.MakeLocalQueue("lq", "eng-beta").ClusterQueue("ClusterQueueB").Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a1-admitted", "eng-alpha").
					Queue("lq").
					Request("gpu", "1").
					SimpleReserveQuota("ClusterQueueA", "on-demand", now).
					Obj(),
				*utiltestingapi.MakeWorkload("a2-pending", "eng-alpha").
					Queue("lq").
					Request("gpu", "2").
					Obj(),
				*utiltestingapi.MakeWorkload("b1-pending", "eng-beta").
					Creation(now).
					Queue("lq").
					Request("gpu", "1").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a1-admitted", "eng-alpha").
					Queue("lq").
					Request("gpu", "1").
					SimpleReserveQuota("ClusterQueueA", "on-demand", now).
					Obj(),
				*utiltestingapi.MakeWorkload("a2-pending", "eng-alpha").
					Queue("lq").
					Request("gpu", "2").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "couldn't assign flavors to pod set main: insufficient unused quota for gpu in flavor on-demand, 1 more needed",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "main",
						Resources: corev1.ResourceList{
							"gpu": resource.MustParse("2"),
						},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("b1-pending", "eng-beta").
					Creation(now).
					Queue("lq").
					Request("gpu", "1").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "Workload no longer fits after processing another workload",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "main",
						Resources: corev1.ResourceList{
							"gpu": resource.MustParse("1"),
						},
					}).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"ClusterQueueB": {"eng-beta/b1-pending"},
			},
			wantInadmissibleLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"ClusterQueueA": {"eng-alpha/a2-pending"},
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/a1-admitted": *utiltestingapi.MakeAdmission("ClusterQueueA").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment("gpu", "on-demand", "1").
						Obj()).
					Obj(),
			},
		},
		//
		// Denote Cohorts in UPPERCASE, and ClusterQueues in lowercase.
		// quota is at GUARANTEED cohort
		//
		//             ROOT
		//          /        \
		//    GUARANTEED      best-effort
		//   /
		// guaranteed
		//
		"in a hierarchical cohort, workload borrowing less is scheduled first": {
			cohorts: []kueue.Cohort{
				*utiltestingapi.MakeCohort("root").Obj(),
				*utiltestingapi.MakeCohort("guaranteed").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default").
							Resource(corev1.ResourceCPU, "4").Obj(),
					).Parent("root").Obj(),
			},
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("guaranteed").
					Cohort("guaranteed").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default").
							Resource(corev1.ResourceCPU, "0").Obj(),
					).Obj(),
				*utiltestingapi.MakeClusterQueue("best-effort").
					Cohort("root").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default").
							Resource(corev1.ResourceCPU, "0").Obj(),
					).Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltestingapi.MakeLocalQueue("lq-guaranteed", "eng-alpha").ClusterQueue("guaranteed").Obj(),
				*utiltestingapi.MakeLocalQueue("lq-best-effort", "eng-alpha").ClusterQueue("best-effort").Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("guaranteed", "eng-alpha").
					Queue("lq-guaranteed").
					Priority(0).
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "4").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("best-effort", "eng-alpha").
					Queue("lq-best-effort").
					Priority(3).
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "4").
						Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("best-effort", "eng-alpha").
					Queue("lq-best-effort").
					Priority(3).
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
				*utiltestingapi.MakeWorkload("guaranteed", "eng-alpha").
					Queue("lq-guaranteed").
					Priority(0).
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "4").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "QuotaReserved",
						Message:            "Quota reserved in ClusterQueue guaranteed",
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
						utiltestingapi.MakeAdmission("guaranteed").
							PodSets(
								utiltestingapi.MakePodSetAssignment("one").
									Assignment(corev1.ResourceCPU, "default", "4").
									Count(1).
									Obj(),
							).
							Obj(),
					).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/guaranteed": *utiltestingapi.MakeAdmission("guaranteed", "one").
					PodSets(utiltestingapi.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "default", "4").
						Obj()).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"best-effort": {"eng-alpha/best-effort"},
			},
		},
		// In this test, the workload `new` cannot be assigned the first flavor
		// `on-demand` because the other workload `admitted` is using only the
		// nominal quota hence cannot be preempted. The flavorassigner detects
		// this and assigns the other flavor `spot` to the workload `new`.
		"don't assign flavor if there are no candidates for preemption": {
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("cq1").
					Cohort("cohort").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
						BorrowWithinCohort: &kueue.BorrowWithinCohort{
							Policy: kueue.BorrowWithinCohortPolicyLowerPriority,
						},
					}).
					FlavorFungibility(kueue.FlavorFungibility{
						WhenCanPreempt: kueue.MayStopSearch,
						WhenCanBorrow:  kueue.MayStopSearch,
					}).
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("0").BorrowingLimit("1").Append().
							Obj(),
						*utiltestingapi.MakeFlavorQuotas("spot").
							ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("0").BorrowingLimit("1").Append().
							Obj(),
					).Obj(),
				*utiltestingapi.MakeClusterQueue("cq2").
					Cohort("cohort").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("1").Append().
							Obj(),
						*utiltestingapi.MakeFlavorQuotas("spot").
							ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("1").Append().
							Obj(),
					).Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltestingapi.MakeLocalQueue("lq1", "eng-alpha").ClusterQueue("cq1").Obj(),
				*utiltestingapi.MakeLocalQueue("lq2", "eng-alpha").ClusterQueue("cq2").Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("admitted", "eng-alpha").
					Queue("lq2").
					Request(corev1.ResourceCPU, "1").
					SimpleReserveQuota("cq2", "on-demand", now.Add(-time.Second)).
					AdmittedAt(true, now).
					Priority(0).
					Obj(),
				*utiltestingapi.MakeWorkload("new", "eng-alpha").
					Queue("lq1").
					Request(corev1.ResourceCPU, "1").
					Priority(100).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("admitted", "eng-alpha").
					Queue("lq2").
					Request(corev1.ResourceCPU, "1").
					SimpleReserveQuota("cq2", "on-demand", now.Add(-time.Second)).
					AdmittedAt(true, now).
					Priority(0).
					Obj(),
				*utiltestingapi.MakeWorkload("new", "eng-alpha").
					Queue("lq1").
					Request(corev1.ResourceCPU, "1").
					Priority(100).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "QuotaReserved",
						Message:            "Quota reserved in ClusterQueue cq1",
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
						utiltestingapi.MakeAdmission("cq1").
							PodSets(
								utiltestingapi.MakePodSetAssignment("main").
									Assignment(corev1.ResourceCPU, "spot", "1").
									Count(1).
									Obj(),
							).
							Obj(),
					).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/new": *utiltestingapi.MakeAdmission("cq1").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "spot", "1").
						Obj()).
					Obj(),
				"eng-alpha/admitted": *utiltestingapi.MakeAdmission("cq2").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "on-demand", "1").
						Obj()).
					Obj(),
			},
		},
		// Workload-slice scheduling test case.
		"workload-slice fits in single clusterQueue": {
			enableElasticJobsViaWorkloadSlice: true,
			// workloads that will be returned by the fake.client.
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("foo-1", "sales").
					ResourceVersion("1").
					Queue("main").
					PodSets(*utiltestingapi.MakePodSet("one", 10).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Generation(1).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("sales").PodSets(utiltestingapi.MakePodSetAssignment("one").Assignment(corev1.ResourceCPU, "default", "10000m").Count(10).Obj()).Obj(), now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             kueue.WorkloadQuotaReserved,
						Message:            "Quota reserved in ClusterQueue sales",
						ObservedGeneration: 1,
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionTrue,
						Reason:             kueue.WorkloadAdmitted,
						Message:            "The workload is admitted",
						ObservedGeneration: 1,
						LastTransitionTime: metav1.NewTime(now),
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("foo-2", "sales").
					ResourceVersion("1").
					Annotation(workloadslicing.WorkloadSliceReplacementFor, "sales/foo-1").
					Queue("main").
					PodSets(*utiltestingapi.MakePodSet("one", 15).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Generation(1).
					Obj(),
			},
			// wantAssignments is a map of workload name to the status assignments expected to be in the cache after the scheduling cycle.
			wantAssignments: map[workload.Reference]kueue.Admission{
				"sales/foo-1": *utiltestingapi.MakeAdmission("sales").PodSets(utiltestingapi.MakePodSetAssignment("one").Assignment(corev1.ResourceCPU, "default", "10").Count(10).Obj()).Obj(),
				"sales/foo-2": *utiltestingapi.MakeAdmission("sales").PodSets(utiltestingapi.MakePodSetAssignment("one").Assignment(corev1.ResourceCPU, "default", "15").Count(15).Obj()).Obj(),
			},
			// wantWorkloads an authoritative list of workloads expected in K8s API after the scheduling cycle.
			// This may not be the same as previous "want*" values due to the stabbed apply status invocations in the test.
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("foo-1", "sales").
					ResourceVersion("2").
					Queue("main").
					PodSets(*utiltestingapi.MakePodSet("one", 10).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Admission(
						utiltestingapi.MakeAdmission("sales").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "default", "10000m").
								Count(10).
								Obj()).
							Obj(),
					).
					Generation(1).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             kueue.WorkloadQuotaReserved,
						Message:            "Quota reserved in ClusterQueue sales",
						ObservedGeneration: 1,
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionTrue,
						Reason:             kueue.WorkloadAdmitted,
						Message:            "The workload is admitted",
						ObservedGeneration: 1,
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadFinished,
						Status:             metav1.ConditionTrue,
						Reason:             kueue.WorkloadSliceReplaced,
						Message:            "Replaced to accommodate a workload (UID: , JobUID: ) due to workload slice aggregation",
						ObservedGeneration: 1,
						LastTransitionTime: metav1.NewTime(now),
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("foo-2", "sales").
					Annotation(workloadslicing.WorkloadSliceReplacementFor, "sales/foo-1").
					ResourceVersion("2").
					Queue("main").
					PodSets(*utiltestingapi.MakePodSet("one", 15).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Admission(
						utiltestingapi.MakeAdmission("sales").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "default", "15000m").
								Count(15).
								Obj()).
							Obj(),
					).
					Generation(1).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             kueue.WorkloadQuotaReserved,
						Message:            "Quota reserved in ClusterQueue sales",
						ObservedGeneration: 1,
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionTrue,
						Reason:             kueue.WorkloadAdmitted,
						Message:            "The workload is admitted",
						ObservedGeneration: 1,
						LastTransitionTime: metav1.NewTime(now),
					}).
					Obj(),
			},
			eventCmpOpts: ignoreEventMessageCmpOpts,
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("sales", "foo-1", kueue.WorkloadSliceReplaced, corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("sales", "foo-2", "QuotaReserved", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("sales", "foo-2", "Admitted", corev1.EventTypeNormal).Obj(),
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
		"pending admission check with nofit and fit flavors": {
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("pending-check", "eng-beta").
					Queue("main").
					Request(corev1.ResourceCPU, "80").
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "check",
						State: kueue.CheckStatePending,
					}).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-beta/pending-check": {
					ClusterQueue: "eng-beta",
					PodSetAssignments: []kueue.PodSetAssignment{
						utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "spot", "80").
							Obj(),
					},
				},
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("pending-check", "eng-beta").
					Queue("main").
					Request(corev1.ResourceCPU, "80").
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "check",
						State: kueue.CheckStatePending,
					}).
					Admission(
						utiltestingapi.MakeAdmission("eng-beta").
							PodSets(
								utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
									Assignment(corev1.ResourceCPU, "spot", "80").
									Obj(),
							).
							Obj(),
					).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             kueue.WorkloadQuotaReserved,
						Message:            "Quota reserved in ClusterQueue eng-beta",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("eng-beta", "pending-check", "QuotaReserved", corev1.EventTypeNormal).
					Message("Quota reserved in ClusterQueue eng-beta, wait time since queued was 9223372037s; " +
						"Flavors considered: main: on-demand(NoFit;insufficient quota for cpu in flavor on-demand, " +
						"previously considered podsets requests (0) + current podset request (80) > maximum capacity (60)), spot(Fit;borrow=1)").Obj(),
			},
		},
		"admit to second flavor when first needs preemption; WhenCanPreempt: TryNextFlavor": {
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("preempt-attempts-cq").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
					}).
					FlavorFungibility(kueue.FlavorFungibility{
						WhenCanPreempt: kueue.TryNextFlavor,
					}).
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "1").Obj(),
						*utiltestingapi.MakeFlavorQuotas("spot").
							Resource(corev1.ResourceCPU, "1").Obj(),
					).Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltestingapi.MakeLocalQueue("preempt-attempts-lq", "eng-alpha").ClusterQueue("preempt-attempts-cq").Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("blocker", "eng-alpha").
					Queue("preempt-attempts-lq").
					Request(corev1.ResourceCPU, "1").
					Priority(50).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("preempt-attempts-cq").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "on-demand", "1").
							Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("test-wl", "eng-alpha").
					Queue("preempt-attempts-lq").
					Request(corev1.ResourceCPU, "1").
					Priority(100).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("blocker", "eng-alpha").
					Queue("preempt-attempts-lq").
					Request(corev1.ResourceCPU, "1").
					Priority(50).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("preempt-attempts-cq").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "on-demand", "1").
							Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("test-wl", "eng-alpha").
					Queue("preempt-attempts-lq").
					Request(corev1.ResourceCPU, "1").
					Priority(100).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "QuotaReserved",
						Message:            "Quota reserved in ClusterQueue preempt-attempts-cq",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionTrue,
						Reason:             "Admitted",
						Message:            "The workload is admitted",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Admission(utiltestingapi.MakeAdmission("preempt-attempts-cq").
						PodSets(
							utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "spot", "1").
								Obj()).
						Obj()).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/test-wl": {
					ClusterQueue: "preempt-attempts-cq",
					PodSetAssignments: []kueue.PodSetAssignment{
						utiltestingapi.MakePodSetAssignment("main").Assignment(corev1.ResourceCPU, "spot", "1").Obj(),
					},
				},
				"eng-alpha/blocker": {
					ClusterQueue: "preempt-attempts-cq",
					PodSetAssignments: []kueue.PodSetAssignment{
						utiltestingapi.MakePodSetAssignment("main").Assignment(corev1.ResourceCPU, "on-demand", "1").Obj(),
					},
				},
			},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("eng-alpha", "test-wl", "QuotaReserved", corev1.EventTypeNormal).
					Message("Quota reserved in ClusterQueue preempt-attempts-cq, wait time since queued was 9223372037s; Flavors considered: main: on-demand(preemptionMode=Preempt;insufficient unused quota for cpu in flavor on-demand, 1 more needed)").
					Obj(),
				utiltesting.MakeEventRecord("eng-alpha", "test-wl", "Admitted", corev1.EventTypeNormal).
					Message("Admitted by ClusterQueue preempt-attempts-cq, wait time since reservation was 0s").
					Obj(),
			},
		},
		"admit workload with zero-quantity request for resource not in ClusterQueue": {
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("zero-resource-wl", "sales").
					Queue("main").
					Request(corev1.ResourceCPU, "1").
					Request("example.com/gpu", "0").
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"sales/zero-resource-wl": {
					ClusterQueue: "sales",
					PodSetAssignments: []kueue.PodSetAssignment{
						utiltestingapi.MakePodSetAssignment("main").
							Assignment(corev1.ResourceCPU, "default", "1000m").
							Obj(),
					},
				},
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("zero-resource-wl", "sales").
					Queue("main").
					Request(corev1.ResourceCPU, "1").
					Request("example.com/gpu", "0").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "QuotaReserved",
						Message:            "Quota reserved in ClusterQueue sales",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionTrue,
						Reason:             "Admitted",
						Message:            "The workload is admitted",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Admission(utiltestingapi.MakeAdmission("sales").
						PodSets(
							utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1000m").
								Obj()).
						Obj()).
					Obj(),
			},
			eventCmpOpts: ignoreEventMessageCmpOpts,
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("sales", "zero-resource-wl", "QuotaReserved", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("sales", "zero-resource-wl", "Admitted", corev1.EventTypeNormal).Obj(),
			},
		},
		"preempt when workload requests zero of a resource not defined in ClusterQueue": {
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("preempt-zero-gpu-cq").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
					}).
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default").
							Resource(corev1.ResourceCPU, "4").Obj(),
					).Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltestingapi.MakeLocalQueue("preempt-zero-gpu-lq", "sales").ClusterQueue("preempt-zero-gpu-cq").Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("preemptor", "sales").
					UID("wl-preemptor").
					JobUID("job-preemptor").
					Queue("preempt-zero-gpu-lq").
					Request(corev1.ResourceCPU, "2").
					Request("example.com/gpu", "0").
					Obj(),
				*utiltestingapi.MakeWorkload("low-priority", "sales").
					Priority(-1).
					Queue("preempt-zero-gpu-lq").
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("preempt-zero-gpu-cq").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "4000m").
							Obj()).
						Obj(), now).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("low-priority", "sales").
					Priority(-1).
					Queue("preempt-zero-gpu-lq").
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("preempt-zero-gpu-cq").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "4000m").
							Obj()).
						Obj(), now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-preemptor, JobUID: job-preemptor) due to prioritization in the ClusterQueue; preemptor path: /preempt-zero-gpu-cq; preemptee path: /preempt-zero-gpu-cq",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InClusterQueue",
						Message:            "Preempted to accommodate a workload (UID: wl-preemptor, JobUID: job-preemptor) due to prioritization in the ClusterQueue; preemptor path: /preempt-zero-gpu-cq; preemptee path: /preempt-zero-gpu-cq",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("preemptor", "sales").
					UID("wl-preemptor").
					JobUID("job-preemptor").
					Queue("preempt-zero-gpu-lq").
					Request(corev1.ResourceCPU, "2").
					Request("example.com/gpu", "0").
					ResourceRequests(kueue.PodSetRequest{
						Name: kueue.DefaultPodSetName,
						Resources: corev1.ResourceList{
							corev1.ResourceCPU:                     resource.MustParse("2"),
							corev1.ResourceName("example.com/gpu"): resource.MustParse("0"),
						},
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor default, 2 more needed. Pending the preemption of 1 workload(s)",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"sales/low-priority": {
					ClusterQueue: "preempt-zero-gpu-cq",
					PodSetAssignments: []kueue.PodSetAssignment{
						utiltestingapi.MakePodSetAssignment("main").
							Assignment(corev1.ResourceCPU, "default", "4000m").
							Obj(),
					},
				},
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"preempt-zero-gpu-cq": {"sales/preemptor"},
			},
			eventCmpOpts: ignoreEventMessageCmpOpts,
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("sales", "low-priority", "EvictedDueToPreempted", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("sales", "low-priority", "Preempted", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord("sales", "preemptor", "Pending", corev1.EventTypeWarning).Obj(),
			},
		},
	}
	for name, tc := range cases {
		for _, enabled := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s WorkloadRequestUseMergePatch enabled: %t", name, enabled), func(t *testing.T) {
				features.SetFeatureGateDuringTest(t, features.WorkloadRequestUseMergePatch, enabled)
				metrics.AdmissionCyclePreemptionSkips.Reset()
				if tc.disablePartialAdmission {
					features.SetFeatureGateDuringTest(t, features.PartialAdmission, false)
				}
				features.SetFeatureGateDuringTest(t, features.ElasticJobsViaWorkloadSlices, tc.enableElasticJobsViaWorkloadSlice)

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
						SubResourcePatch: func(ctx context.Context, client client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
							if _, ok := obj.(*kueue.Workload); ok && subResourceName == "status" && tc.admissionError != nil {
								return tc.admissionError
							}
							return utiltesting.TreatSSAAsStrategicMerge(ctx, client, subResourceName, obj, patch, opts...)
						},
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
					WithFairSharing(fairSharing), WithClock(t, fakeClock))
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

				if features.Enabled(features.WorkloadRequestUseMergePatch) && tc.wantWorkloadUseMergePatch != nil {
					if diff := cmp.Diff(tc.wantWorkloadUseMergePatch, gotWorkloads.Items, defaultWorkloadCmpOpts); diff != "" {
						t.Errorf("Unexpected workloads (-want,+got):\n%s", diff)
					}
				} else {
					if diff := cmp.Diff(tc.wantWorkloads, gotWorkloads.Items, defaultWorkloadCmpOpts); diff != "" {
						t.Errorf("Unexpected workloads (-want,+got):\n%s", diff)
					}
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
					val, err := testutil.GetGaugeMetricValue(metrics.AdmissionCyclePreemptionSkips.WithLabelValues(cqName, roletracker.RoleStandalone))
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

func TestEntryOrdering(t *testing.T) {
	now := time.Now()
	input := []entry{
		{
			Info: workload.Info{
				Obj: &kueue.Workload{ObjectMeta: metav1.ObjectMeta{
					Name:              "old_borrowing",
					CreationTimestamp: metav1.NewTime(now),
				}},
			},
			assignment: flavorassigner.Assignment{
				Borrowing: 1,
			},
		},
		{
			Info: workload.Info{
				Obj: &kueue.Workload{ObjectMeta: metav1.ObjectMeta{
					Name:              "old",
					CreationTimestamp: metav1.NewTime(now.Add(time.Second)),
				}},
			},
		},
		{
			Info: workload.Info{
				Obj: &kueue.Workload{ObjectMeta: metav1.ObjectMeta{
					Name:              "new",
					CreationTimestamp: metav1.NewTime(now.Add(3 * time.Second)),
				}},
			},
		},
		{
			Info: workload.Info{
				Obj: &kueue.Workload{ObjectMeta: metav1.ObjectMeta{
					Name:              "high_pri_borrowing",
					CreationTimestamp: metav1.NewTime(now.Add(3 * time.Second)),
				}, Spec: kueue.WorkloadSpec{
					Priority: ptr.To[int32](1),
				}},
			},
			assignment: flavorassigner.Assignment{
				Borrowing: 1,
			},
		},
		{
			Info: workload.Info{
				Obj: &kueue.Workload{ObjectMeta: metav1.ObjectMeta{
					Name:              "new_high_pri",
					CreationTimestamp: metav1.NewTime(now.Add(4 * time.Second)),
				}, Spec: kueue.WorkloadSpec{
					Priority: ptr.To[int32](1),
				}},
			},
		},
		{
			Info: workload.Info{
				Obj: &kueue.Workload{ObjectMeta: metav1.ObjectMeta{
					Name:              "new_borrowing",
					CreationTimestamp: metav1.NewTime(now.Add(3 * time.Second)),
				}},
			},
			assignment: flavorassigner.Assignment{
				Borrowing: 1,
			},
		},
		{
			Info: workload.Info{
				Obj: &kueue.Workload{
					ObjectMeta: metav1.ObjectMeta{
						Name:              "evicted_borrowing",
						CreationTimestamp: metav1.NewTime(now.Add(time.Second)),
					},
					Status: kueue.WorkloadStatus{
						Conditions: []metav1.Condition{
							{
								Type:               kueue.WorkloadEvicted,
								Status:             metav1.ConditionTrue,
								LastTransitionTime: metav1.NewTime(now.Add(2 * time.Second)),
								Reason:             kueue.WorkloadEvictedByPodsReadyTimeout,
							},
						},
					},
				},
			},
			assignment: flavorassigner.Assignment{
				Borrowing: 1,
			},
		},
		{
			Info: workload.Info{
				Obj: &kueue.Workload{
					ObjectMeta: metav1.ObjectMeta{
						Name:              "recently_evicted",
						CreationTimestamp: metav1.NewTime(now),
					},
					Status: kueue.WorkloadStatus{
						Conditions: []metav1.Condition{
							{
								Type:               kueue.WorkloadEvicted,
								Status:             metav1.ConditionTrue,
								LastTransitionTime: metav1.NewTime(now.Add(2 * time.Second)),
								Reason:             kueue.WorkloadEvictedByPodsReadyTimeout,
							},
						},
					},
				},
			},
		},
		{
			Info: workload.Info{
				Obj: &kueue.Workload{ObjectMeta: metav1.ObjectMeta{
					Name:              "high_pri_borrowing_more",
					CreationTimestamp: metav1.NewTime(now.Add(3 * time.Second)),
				}, Spec: kueue.WorkloadSpec{
					Priority: ptr.To[int32](1),
				}},
			},
			assignment: flavorassigner.Assignment{
				Borrowing: 2,
			},
		},
	}
	inputForOrderingPreemptedWorkloads := []entry{
		{
			Info: workload.Info{
				Obj: &kueue.Workload{ObjectMeta: metav1.ObjectMeta{
					Name:              "old-mid-recently-preempted-in-queue",
					CreationTimestamp: metav1.NewTime(now),
				}, Spec: kueue.WorkloadSpec{
					Priority: ptr.To[int32](1),
				}, Status: kueue.WorkloadStatus{
					Conditions: []metav1.Condition{
						{
							Type:               kueue.WorkloadPreempted,
							Status:             metav1.ConditionTrue,
							Reason:             kueue.InClusterQueueReason,
							LastTransitionTime: metav1.NewTime(now.Add(5 * time.Second)),
						},
					},
				}},
			},
		},
		{
			Info: workload.Info{
				Obj: &kueue.Workload{ObjectMeta: metav1.ObjectMeta{
					Name:              "old-mid-recently-reclaimed-while-borrowing",
					CreationTimestamp: metav1.NewTime(now),
				}, Spec: kueue.WorkloadSpec{
					Priority: ptr.To[int32](1),
				}, Status: kueue.WorkloadStatus{
					Conditions: []metav1.Condition{
						{
							Type:               kueue.WorkloadPreempted,
							Status:             metav1.ConditionTrue,
							Reason:             kueue.InCohortReclaimWhileBorrowingReason,
							LastTransitionTime: metav1.NewTime(now.Add(6 * time.Second)),
						},
					},
				}},
			},
		},
		{
			Info: workload.Info{
				Obj: &kueue.Workload{ObjectMeta: metav1.ObjectMeta{
					Name:              "old-mid-more-recently-reclaimed-while-borrowing",
					CreationTimestamp: metav1.NewTime(now),
				}, Spec: kueue.WorkloadSpec{
					Priority: ptr.To[int32](1),
				}, Status: kueue.WorkloadStatus{
					Conditions: []metav1.Condition{
						{
							Type:               kueue.WorkloadPreempted,
							Status:             metav1.ConditionTrue,
							Reason:             kueue.InCohortReclaimWhileBorrowingReason,
							LastTransitionTime: metav1.NewTime(now.Add(7 * time.Second)),
						},
					},
				}},
			},
		},
		{
			Info: workload.Info{
				Obj: &kueue.Workload{ObjectMeta: metav1.ObjectMeta{
					Name:              "old-mid-not-preempted-yet",
					CreationTimestamp: metav1.NewTime(now.Add(time.Second)),
				}, Spec: kueue.WorkloadSpec{
					Priority: ptr.To[int32](1),
				}},
			},
		},
		{
			Info: workload.Info{
				Obj: &kueue.Workload{ObjectMeta: metav1.ObjectMeta{
					Name:              "preemptor",
					CreationTimestamp: metav1.NewTime(now.Add(7 * time.Second)),
				}, Spec: kueue.WorkloadSpec{
					Priority: ptr.To[int32](2),
				}},
			},
		},
	}
	for _, tc := range []struct {
		name             string
		input            []entry
		prioritySorting  bool
		workloadOrdering workload.Ordering
		wantOrder        []string
	}{
		{
			name:             "Priority sorting is enabled (default) using pods-ready Eviction timestamp (default)",
			input:            input,
			prioritySorting:  true,
			workloadOrdering: workload.Ordering{PodsReadyRequeuingTimestamp: config.EvictionTimestamp},
			wantOrder:        []string{"new_high_pri", "old", "recently_evicted", "new", "high_pri_borrowing", "old_borrowing", "evicted_borrowing", "new_borrowing", "high_pri_borrowing_more"},
		},
		{
			name:             "Priority sorting is enabled (default) using pods-ready Creation timestamp",
			input:            input,
			prioritySorting:  true,
			workloadOrdering: workload.Ordering{PodsReadyRequeuingTimestamp: config.CreationTimestamp},
			wantOrder:        []string{"new_high_pri", "recently_evicted", "old", "new", "high_pri_borrowing", "old_borrowing", "evicted_borrowing", "new_borrowing", "high_pri_borrowing_more"},
		},
		{
			name:             "Priority sorting is disabled using pods-ready Eviction timestamp",
			input:            input,
			prioritySorting:  false,
			workloadOrdering: workload.Ordering{PodsReadyRequeuingTimestamp: config.EvictionTimestamp},
			wantOrder:        []string{"old", "recently_evicted", "new", "new_high_pri", "old_borrowing", "evicted_borrowing", "high_pri_borrowing", "new_borrowing", "high_pri_borrowing_more"},
		},
		{
			name:             "Priority sorting is disabled using pods-ready Creation timestamp",
			input:            input,
			prioritySorting:  false,
			workloadOrdering: workload.Ordering{PodsReadyRequeuingTimestamp: config.CreationTimestamp},
			wantOrder:        []string{"recently_evicted", "old", "new", "new_high_pri", "old_borrowing", "evicted_borrowing", "high_pri_borrowing", "new_borrowing", "high_pri_borrowing_more"},
		},
		{
			name:            "Some workloads are preempted; Priority sorting is disabled",
			input:           inputForOrderingPreemptedWorkloads,
			prioritySorting: false,
			wantOrder: []string{
				"old-mid-recently-preempted-in-queue",
				"old-mid-not-preempted-yet",
				"old-mid-recently-reclaimed-while-borrowing",
				"preemptor",
				"old-mid-more-recently-reclaimed-while-borrowing",
			},
		},
		{
			name:            "Some workloads are preempted; Priority sorting is enabled",
			input:           inputForOrderingPreemptedWorkloads,
			prioritySorting: true,
			wantOrder: []string{
				"preemptor",
				"old-mid-recently-preempted-in-queue",
				"old-mid-recently-reclaimed-while-borrowing",
				"old-mid-more-recently-reclaimed-while-borrowing",
				"old-mid-not-preempted-yet",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.PrioritySortingWithinCohort, tc.prioritySorting)
			ctx, _ := utiltesting.ContextWithLog(t)
			iter := makeIterator(ctx, tc.input, tc.workloadOrdering, false)
			order := make([]string, len(tc.input))
			for i := range tc.input {
				order[i] = iter.pop().Obj.Name
			}
			if iter.hasNext() {
				t.Error("Expected iterator to be exhausted")
			}
			if diff := cmp.Diff(tc.wantOrder, order); diff != "" {
				t.Errorf("%s: Unexpected order (-want,+got):\n%s", tc.name, diff)
			}
		})
	}
}

func TestLastSchedulingContext(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	fakeClock := testingclock.NewFakeClock(now)

	resourceFlavors := []*kueue.ResourceFlavor{
		utiltestingapi.MakeResourceFlavor("on-demand").Obj(),
		utiltestingapi.MakeResourceFlavor("spot").Obj(),
	}
	clusterQueueCohort := []kueue.ClusterQueue{
		*utiltestingapi.MakeClusterQueue("eng-cohort-alpha").
			Cohort("cohort").
			QueueingStrategy(kueue.StrictFIFO).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue:  kueue.PreemptionPolicyNever,
				ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
			}).
			FlavorFungibility(kueue.FlavorFungibility{
				WhenCanPreempt: kueue.MayStopSearch,
				WhenCanBorrow:  kueue.MayStopSearch,
			}).
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("on-demand").
					Resource(corev1.ResourceCPU, "50", "50").Obj(),
				*utiltestingapi.MakeFlavorQuotas("spot").
					Resource(corev1.ResourceCPU, "100", "0").Obj(),
			).Obj(),
		*utiltestingapi.MakeClusterQueue("eng-cohort-beta").
			Cohort("cohort").
			QueueingStrategy(kueue.StrictFIFO).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue:  kueue.PreemptionPolicyNever,
				ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
			}).
			FlavorFungibility(kueue.FlavorFungibility{
				WhenCanPreempt: kueue.MayStopSearch,
				WhenCanBorrow:  kueue.MayStopSearch,
			}).
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("on-demand").
					Resource(corev1.ResourceCPU, "50", "50").Obj(),
				*utiltestingapi.MakeFlavorQuotas("spot").
					Resource(corev1.ResourceCPU, "100", "0").Obj(),
			).Obj(),
		*utiltestingapi.MakeClusterQueue("eng-cohort-theta").
			Cohort("cohort").
			QueueingStrategy(kueue.StrictFIFO).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue:  kueue.PreemptionPolicyNever,
				ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
			}).
			FlavorFungibility(kueue.FlavorFungibility{
				WhenCanPreempt: kueue.TryNextFlavor,
				WhenCanBorrow:  kueue.TryNextFlavor,
			}).
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("on-demand").
					Resource(corev1.ResourceCPU, "50", "50").Obj(),
				*utiltestingapi.MakeFlavorQuotas("spot").
					Resource(corev1.ResourceCPU, "100", "0").Obj(),
			).Obj(),
	}

	queues := []kueue.LocalQueue{
		*utiltestingapi.MakeLocalQueue("main", "default").ClusterQueue("eng-alpha").Obj(),
		*utiltestingapi.MakeLocalQueue("main-alpha", "default").ClusterQueue("eng-cohort-alpha").Obj(),
		*utiltestingapi.MakeLocalQueue("main-beta", "default").ClusterQueue("eng-cohort-beta").Obj(),
		*utiltestingapi.MakeLocalQueue("main-theta", "default").ClusterQueue("eng-cohort-theta").Obj(),
	}
	cases := []struct {
		name                           string
		cqs                            []kueue.ClusterQueue
		workloads                      []kueue.Workload
		deleteWorkloads                []client.ObjectKey
		wantWorkloads                  []kueue.Workload
		wantAdmissionsOnSecondSchedule map[workload.Reference]kueue.Admission
	}{
		{
			name: "scheduling on the first flavor is unblocked after some workloads were deleted",
			// In this scenario we wait for the first flavor to be unblocked by
			// removal of workloads. The second flavor cannot be used because
			// it is noFit.
			cqs: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("eng-alpha").
					QueueingStrategy(kueue.BestEffortFIFO).
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
							Resource(corev1.ResourceCPU, "10", "0").Obj(),
					).Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("low-1", "default").
					Queue("main").
					Request(corev1.ResourceCPU, "50").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("eng-alpha").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "on-demand", "50").
							Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("preemptor", "default").
					Queue("main").
					Request(corev1.ResourceCPU, "20").
					Obj(),
			},
			deleteWorkloads: []client.ObjectKey{{
				Namespace: metav1.NamespaceDefault,
				Name:      "low-1",
			}},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("low-1", "default").
					Queue("main").
					Request(corev1.ResourceCPU, "50").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("eng-alpha").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "on-demand", "50").
							Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("preemptor", "default").
					Queue("main").
					Request(corev1.ResourceCPU, "20").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "couldn't assign flavors to pod set main: insufficient quota for cpu in flavor spot, previously considered podsets requests (0) + current podset request (20) > maximum capacity (10), insufficient unused quota for cpu in flavor on-demand, 20 more needed",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "main",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("20"),
						},
					}).
					Obj(),
			},
			wantAdmissionsOnSecondSchedule: map[workload.Reference]kueue.Admission{
				"default/preemptor": *utiltestingapi.MakeAdmission("eng-alpha").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "on-demand", "20").
						Obj()).
					Obj(),
			},
		},
		{
			name: "borrow before next flavor",
			cqs:  clusterQueueCohort,
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("placeholder", "default").
					Request(corev1.ResourceCPU, "50").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("eng-cohort-alpha").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "on-demand", "50").
							Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("borrower", "default").
					Queue("main-alpha").
					Request(corev1.ResourceCPU, "20").
					Obj(),
				*utiltestingapi.MakeWorkload("workload1", "default").
					Queue("main-beta").
					Request(corev1.ResourceCPU, "20").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("borrower", "default").
					Queue("main-alpha").
					Request(corev1.ResourceCPU, "20").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "QuotaReserved",
						Message:            "Quota reserved in ClusterQueue eng-cohort-alpha",
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
						utiltestingapi.MakeAdmission("eng-cohort-alpha").
							PodSets(
								utiltestingapi.MakePodSetAssignment("main").
									Assignment(corev1.ResourceCPU, "on-demand", "20").
									Count(1).
									Obj(),
							).
							Obj(),
					).
					Obj(),
				*utiltestingapi.MakeWorkload("placeholder", "default").
					Request(corev1.ResourceCPU, "50").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("eng-cohort-alpha").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "on-demand", "50").
							Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("workload1", "default").
					Queue("main-beta").
					Request(corev1.ResourceCPU, "20").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "QuotaReserved",
						Message:            "Quota reserved in ClusterQueue eng-cohort-beta",
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
						utiltestingapi.MakeAdmission("eng-cohort-beta").
							PodSets(
								utiltestingapi.MakePodSetAssignment("main").
									Assignment(corev1.ResourceCPU, "on-demand", "20").
									Count(1).
									Obj(),
							).
							Obj(),
					).
					Obj(),
			},
			wantAdmissionsOnSecondSchedule: map[workload.Reference]kueue.Admission{
				"default/placeholder": *utiltestingapi.MakeAdmission("eng-cohort-alpha").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "on-demand", "50").
						Obj()).
					Obj(),
				"default/workload1": *utiltestingapi.MakeAdmission("eng-cohort-beta").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "on-demand", "20").
						Obj()).
					Obj(),
				"default/borrower": *utiltestingapi.MakeAdmission("eng-cohort-alpha").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "on-demand", "20").
						Obj()).
					Obj(),
			},
		},
		{
			name: "borrow after all flavors",
			cqs:  clusterQueueCohort,
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("placeholder", "default").
					Request(corev1.ResourceCPU, "50").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("eng-cohort-alpha").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "on-demand", "50").
							Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("placeholder1", "default").
					Request(corev1.ResourceCPU, "50").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("eng-cohort-theta").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "on-demand", "50").
							Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("workload", "default").
					Queue("main-theta").
					Request(corev1.ResourceCPU, "20").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("placeholder", "default").
					Request(corev1.ResourceCPU, "50").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("eng-cohort-alpha").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "on-demand", "50").
							Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("placeholder1", "default").
					Request(corev1.ResourceCPU, "50").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("eng-cohort-theta").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "on-demand", "50").
							Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("workload", "default").
					Queue("main-theta").
					Request(corev1.ResourceCPU, "20").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "QuotaReserved",
						Message:            "Quota reserved in ClusterQueue eng-cohort-theta",
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
						utiltestingapi.MakeAdmission("eng-cohort-theta").
							PodSets(
								utiltestingapi.MakePodSetAssignment("main").
									Assignment(corev1.ResourceCPU, "spot", "20").
									Count(1).
									Obj(),
							).
							Obj(),
					).
					Obj(),
			},
			wantAdmissionsOnSecondSchedule: map[workload.Reference]kueue.Admission{
				"default/placeholder": *utiltestingapi.MakeAdmission("eng-cohort-alpha").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "on-demand", "50").
						Obj()).
					Obj(),
				"default/placeholder1": *utiltestingapi.MakeAdmission("eng-cohort-theta").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "on-demand", "50").
						Obj()).
					Obj(),
				"default/workload": *utiltestingapi.MakeAdmission("eng-cohort-theta").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "spot", "20").
						Obj()).
					Obj(),
			},
		},
		{
			name: "when the next flavor is full, but can borrow on first",
			cqs:  clusterQueueCohort,
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("placeholder", "default").
					Request(corev1.ResourceCPU, "40").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("eng-cohort-alpha").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "on-demand", "40").
							Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("placeholder1", "default").
					Request(corev1.ResourceCPU, "40").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("eng-cohort-theta").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "on-demand", "40").
							Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("placeholder2", "default").
					Request(corev1.ResourceCPU, "100").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("eng-cohort-theta").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "spot", "100").
							Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("workload", "default").
					Queue("main-theta").
					Request(corev1.ResourceCPU, "20").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("placeholder", "default").
					Request(corev1.ResourceCPU, "40").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("eng-cohort-alpha").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "on-demand", "40").
							Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("placeholder1", "default").
					Request(corev1.ResourceCPU, "40").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("eng-cohort-theta").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "on-demand", "40").
							Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("placeholder2", "default").
					Request(corev1.ResourceCPU, "100").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("eng-cohort-theta").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "spot", "100").
							Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("workload", "default").
					Queue("main-theta").
					Request(corev1.ResourceCPU, "20").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "QuotaReserved",
						Message:            "Quota reserved in ClusterQueue eng-cohort-theta",
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
						utiltestingapi.MakeAdmission("eng-cohort-theta").
							PodSets(
								utiltestingapi.MakePodSetAssignment("main").
									Assignment(corev1.ResourceCPU, "on-demand", "20").
									Count(1).
									Obj(),
							).
							Obj(),
					).
					Obj(),
			},
			wantAdmissionsOnSecondSchedule: map[workload.Reference]kueue.Admission{
				"default/placeholder": *utiltestingapi.MakeAdmission("eng-cohort-alpha").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "on-demand", "40").
						Obj()).
					Obj(),
				"default/placeholder1": *utiltestingapi.MakeAdmission("eng-cohort-theta").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "on-demand", "40").
						Obj()).
					Obj(),
				"default/placeholder2": *utiltestingapi.MakeAdmission("eng-cohort-theta").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "spot", "100").
						Obj()).
					Obj(),
				"default/workload": *utiltestingapi.MakeAdmission("eng-cohort-theta").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "on-demand", "20").
						Obj()).
					Obj(),
			},
		},
		{
			name: "when the next flavor is full, but can preempt on first",
			cqs:  clusterQueueCohort,
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("placeholder-alpha", "default").
					Priority(-1).
					Request(corev1.ResourceCPU, "150").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("eng-cohort-alpha").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "on-demand", "150").
							Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("placeholder-theta-spot", "default").
					Request(corev1.ResourceCPU, "100").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("eng-cohort-theta").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "spot", "100").
							Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("new", "default").
					UID("wl-new").
					JobUID("job-new").
					Queue("main-theta").
					Request(corev1.ResourceCPU, "20").
					Obj(),
			},
			deleteWorkloads: []client.ObjectKey{{
				Namespace: metav1.NamespaceDefault,
				Name:      "placeholder-alpha",
			}},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("new", "default").
					UID("wl-new").
					JobUID("job-new").
					Queue("main-theta").
					Request(corev1.ResourceCPU, "20").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor on-demand, 20 more needed, insufficient unused quota for cpu in flavor spot, 20 more needed. Pending the preemption of 1 workload(s)",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "main",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("20"),
						},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("placeholder-alpha", "default").
					Priority(-1).
					Request(corev1.ResourceCPU, "150").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("eng-cohort-alpha").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "on-demand", "150").
							Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-new, JobUID: job-new) due to reclamation within the cohort; preemptor path: /cohort/eng-cohort-theta; preemptee path: /cohort/eng-cohort-alpha",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InCohortReclamation",
						Message:            "Preempted to accommodate a workload (UID: wl-new, JobUID: job-new) due to reclamation within the cohort; preemptor path: /cohort/eng-cohort-theta; preemptee path: /cohort/eng-cohort-alpha",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("placeholder-theta-spot", "default").
					Request(corev1.ResourceCPU, "100").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("eng-cohort-theta").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "spot", "100").
							Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Obj(),
			},
			wantAdmissionsOnSecondSchedule: map[workload.Reference]kueue.Admission{
				"default/placeholder-theta-spot": *utiltestingapi.MakeAdmission("eng-cohort-theta").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "spot", "100").
						Obj()).
					Obj(),
				"default/new": *utiltestingapi.MakeAdmission("eng-cohort-theta").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "on-demand", "20").
						Obj()).
					Obj(),
			},
		},
		{
			name: "TryNextFlavor, but second flavor is full and can preempt on first",
			cqs: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("eng-cohort-alpha").
					Cohort("cohort").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					FlavorFungibility(kueue.FlavorFungibility{
						WhenCanPreempt: kueue.TryNextFlavor,
						WhenCanBorrow:  kueue.TryNextFlavor,
					}).
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("0").BorrowingLimit("60").Append().
							Obj(),
						*utiltestingapi.MakeFlavorQuotas("spot").
							ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("30").BorrowingLimit("30").Append().
							Obj(),
					).Obj(),
				*utiltestingapi.MakeClusterQueue("eng-cohort-beta").
					Cohort("cohort").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					FlavorFungibility(kueue.FlavorFungibility{
						WhenCanPreempt: kueue.TryNextFlavor,
						WhenCanBorrow:  kueue.TryNextFlavor,
					}).
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("30").BorrowingLimit("30").Append().
							Obj(),
						*utiltestingapi.MakeFlavorQuotas("spot").
							ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("30").BorrowingLimit("30").Append().
							Obj(),
					).Obj(),
				*utiltestingapi.MakeClusterQueue("eng-cohort-shared").
					Cohort("cohort").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("30").Append().
							Obj(),
					).Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("alpha1", "default").
					Request(corev1.ResourceCPU, "22").
					SimpleReserveQuota("eng-cohort-alpha", "on-demand", now.Add(-time.Second)).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("alpha2", "default").
					Request(corev1.ResourceCPU, "22").
					SimpleReserveQuota("eng-cohort-alpha", "on-demand", now).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("alpha3", "default").
					Request(corev1.ResourceCPU, "22").
					SimpleReserveQuota("eng-cohort-alpha", "spot", now).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("beta1", "default").
					Request(corev1.ResourceCPU, "22").
					SimpleReserveQuota("eng-cohort-beta", "spot", now).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("new", "default").
					UID("wl-new").
					JobUID("job-new").
					Queue("main-beta").
					Request(corev1.ResourceCPU, "22").
					Obj(),
			},
			deleteWorkloads: []client.ObjectKey{{
				Namespace: metav1.NamespaceDefault,
				Name:      "alpha2",
			}},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("alpha1", "default").
					Request(corev1.ResourceCPU, "22").
					SimpleReserveQuota("eng-cohort-alpha", "on-demand", now.Add(-time.Second)).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("alpha2", "default").
					Request(corev1.ResourceCPU, "22").
					SimpleReserveQuota("eng-cohort-alpha", "on-demand", now).
					AdmittedAt(true, now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-new, JobUID: job-new) due to reclamation within the cohort; preemptor path: /cohort/eng-cohort-beta; preemptee path: /cohort/eng-cohort-alpha",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InCohortReclamation",
						Message:            "Preempted to accommodate a workload (UID: wl-new, JobUID: job-new) due to reclamation within the cohort; preemptor path: /cohort/eng-cohort-beta; preemptee path: /cohort/eng-cohort-alpha",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("alpha3", "default").
					Request(corev1.ResourceCPU, "22").
					SimpleReserveQuota("eng-cohort-alpha", "spot", now).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("beta1", "default").
					Request(corev1.ResourceCPU, "22").
					SimpleReserveQuota("eng-cohort-beta", "spot", now).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("new", "default").
					UID("wl-new").
					JobUID("job-new").
					Queue("main-beta").
					Request(corev1.ResourceCPU, "22").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor on-demand, 6 more needed, insufficient unused quota for cpu in flavor spot, 6 more needed. Pending the preemption of 1 workload(s)",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "main",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("22"),
						},
					}).
					Obj(),
			},
			wantAdmissionsOnSecondSchedule: map[workload.Reference]kueue.Admission{
				"default/alpha1": *utiltestingapi.MakeAdmission("eng-cohort-alpha").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "on-demand", "22").
						Obj()).
					Obj(),
				"default/alpha3": *utiltestingapi.MakeAdmission("eng-cohort-alpha").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "spot", "22").
						Obj()).
					Obj(),
				"default/beta1": *utiltestingapi.MakeAdmission("eng-cohort-beta").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "spot", "22").
						Obj()).
					Obj(),
				"default/new": *utiltestingapi.MakeAdmission("eng-cohort-beta").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "on-demand", "22").
						Obj()).
					Obj(),
			},
		},
	}

	for _, tc := range cases {
		for _, enabled := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s WorkloadRequestUseMergePatch enabled: %t", tc.name, enabled), func(t *testing.T) {
				features.SetFeatureGateDuringTest(t, features.WorkloadRequestUseMergePatch, enabled)
				ctx, log := utiltesting.ContextWithLog(t)
				scheme := runtime.NewScheme()

				testWls := make([]kueue.Workload, 0, len(tc.workloads))
				for _, wl := range tc.workloads {
					testWls = append(testWls, *wl.DeepCopy())
				}
				clientBuilder := utiltesting.NewClientBuilder().
					WithLists(
						&kueue.WorkloadList{Items: testWls},
						&kueue.ClusterQueueList{Items: tc.cqs},
						&kueue.LocalQueueList{Items: queues},
					).
					WithObjects(
						utiltesting.MakeNamespace("default"),
					).
					WithStatusSubresource(&kueue.Workload{}).
					WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: utiltesting.TreatSSAAsStrategicMerge})

				cl := clientBuilder.Build()
				broadcaster := record.NewBroadcaster()
				recorder := broadcaster.NewRecorder(scheme,
					corev1.EventSource{Component: constants.AdmissionName})
				cqCache := schdcache.New(cl)
				qManager := qcache.NewManagerForUnitTests(cl, cqCache)
				// Workloads are loaded into queues or clusterQueues as we add them.
				for _, q := range queues {
					if err := qManager.AddLocalQueue(ctx, &q); err != nil {
						t.Fatalf("Inserting queue %s/%s in manager: %v", q.Namespace, q.Name, err)
					}
				}
				for i := range resourceFlavors {
					cqCache.AddOrUpdateResourceFlavor(log, resourceFlavors[i])
				}
				for _, cq := range tc.cqs {
					if err := cqCache.AddClusterQueue(ctx, &cq); err != nil {
						t.Fatalf("Inserting clusterQueue %s in cache: %v", cq.Name, err)
					}
					if err := qManager.AddClusterQueue(ctx, &cq); err != nil {
						t.Fatalf("Inserting clusterQueue %s in manager: %v", cq.Name, err)
					}
				}
				scheduler := New(qManager, cqCache, cl, recorder, WithClock(t, fakeClock))

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

				gotWorkloads := &kueue.WorkloadList{}
				err := cl.List(ctx, gotWorkloads)
				if err != nil {
					t.Fatalf("Unexpected list workloads error: %v", err)
				}

				defaultWorkloadCmpOpts := cmp.Options{
					cmpopts.EquateEmpty(),
					cmpopts.IgnoreFields(kueue.Workload{}, "ObjectMeta.ResourceVersion"),
					cmpopts.SortSlices(func(a, b metav1.Condition) bool { return a.Type < b.Type }),
				}

				if diff := cmp.Diff(tc.wantWorkloads, gotWorkloads.Items, defaultWorkloadCmpOpts); diff != "" {
					t.Errorf("Unexpected scheduled workloads (-want,+got):\n%s", diff)
				}

				for _, workloadReference := range tc.deleteWorkloads {
					var wl kueue.Workload
					err := cl.Get(ctx, workloadReference, &wl)
					if err != nil {
						t.Errorf("Unable to get workload: %v", err)
					}
					err = cl.Delete(ctx, &wl)
					if err != nil {
						t.Errorf("Delete workload failed: %v", err)
					}
					err = cqCache.DeleteWorkload(log, workload.Key(&wl))
					if err != nil {
						t.Errorf("Delete workload failed: %v", err)
					}
					qManager.QueueAssociatedInadmissibleWorkloadsAfter(ctx, workload.Key(&wl), nil)
				}

				scheduler.schedule(ctx)
				wg.Wait()

				if features.Enabled(features.WorkloadRequestUseMergePatch) {
					// Schedule again to ensure all workloads are admitted, as with MergePatch we enforce stricter patching.
					scheduler.schedule(ctx)
					wg.Wait()
				}

				// Verify assignments in cache.
				gotAssignments := make(map[workload.Reference]kueue.Admission)
				snapshot, err := cqCache.Snapshot(ctx)
				if err != nil {
					t.Fatalf("unexpected error while building snapshot: %v", err)
				}
				for cqName, c := range snapshot.ClusterQueues() {
					for name, w := range c.Workloads {
						switch {
						case !workload.IsAdmitted(w.Obj):
							t.Errorf("Workload %s is not admitted by a clusterQueue, but it is found as member of clusterQueue %s in the cache", name, cqName)
						case w.Obj.Status.Admission.ClusterQueue != cqName:
							t.Errorf("Workload %s is admitted by clusterQueue %s, but it is found as member of clusterQueue %s in the cache", name, w.Obj.Status.Admission.ClusterQueue, cqName)
						default:
							gotAssignments[name] = *w.Obj.Status.Admission
						}
					}
				}
				if diff := cmp.Diff(tc.wantAdmissionsOnSecondSchedule, gotAssignments); diff != "" {
					t.Errorf("Unexpected assigned clusterQueues in cache (-want,+got):\n%s", diff)
				}
			})
		}
	}
}

func TestRequeueAndUpdate(t *testing.T) {
	cq := utiltestingapi.MakeClusterQueue("cq").Obj()
	q1 := utiltestingapi.MakeLocalQueue("q1", "ns1").ClusterQueue(cq.Name).Obj()
	w1 := utiltestingapi.MakeWorkload("w1", "ns1").Queue(kueue.LocalQueueName(q1.Name)).Obj()

	cases := []struct {
		name              string
		e                 entry
		wantWorkloads     map[kueue.ClusterQueueReference][]workload.Reference
		wantInadmissible  map[kueue.ClusterQueueReference][]workload.Reference
		wantStatus        kueue.WorkloadStatus
		wantStatusUpdates int
	}{
		{
			name: "workload didn't fit",
			e: entry{
				inadmissibleMsg: "didn't fit",
			},
			wantStatus: kueue.WorkloadStatus{
				Conditions: []metav1.Condition{
					{
						Type:    kueue.WorkloadQuotaReserved,
						Status:  metav1.ConditionFalse,
						Reason:  "Pending",
						Message: "didn't fit",
					},
				},
				ResourceRequests: []kueue.PodSetRequest{{Name: kueue.DefaultPodSetName}},
			},
			wantInadmissible: map[kueue.ClusterQueueReference][]workload.Reference{
				"cq": {workload.Key(w1)},
			},
			wantStatusUpdates: 1,
		},
		{
			name: "assumed",
			e: entry{
				status:          assumed,
				inadmissibleMsg: "",
			},
			wantWorkloads: map[kueue.ClusterQueueReference][]workload.Reference{
				"cq": {workload.Key(w1)},
			},
		},
		{
			name: "nominated",
			e: entry{
				status:          nominated,
				inadmissibleMsg: "failed to admit workload",
			},
			wantWorkloads: map[kueue.ClusterQueueReference][]workload.Reference{
				"cq": {workload.Key(w1)},
			},
		},
		{
			name: "skipped with summary",
			e: entry{
				status:          skipped,
				inadmissibleMsg: "cohort used in this cycle",
			},
			wantStatus: kueue.WorkloadStatus{
				Conditions: []metav1.Condition{
					{
						Type:    kueue.WorkloadQuotaReserved,
						Status:  metav1.ConditionFalse,
						Reason:  "Pending",
						Message: "cohort used in this cycle",
					},
				},
				ResourceRequests: []kueue.PodSetRequest{{Name: kueue.DefaultPodSetName}},
			},
			wantWorkloads: map[kueue.ClusterQueueReference][]workload.Reference{
				"cq": {workload.Key(w1)},
			},
			wantStatusUpdates: 1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			scheme := runtime.NewScheme()

			updates := 0
			objs := []client.Object{w1, q1, utiltesting.MakeNamespace("ns1")}
			cl := utiltesting.NewClientBuilder().WithInterceptorFuncs(interceptor.Funcs{
				SubResourcePatch: func(ctx context.Context, client client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
					updates++
					return utiltesting.TreatSSAAsStrategicMerge(ctx, client, subResourceName, obj, patch, opts...)
				},
			}).WithObjects(objs...).WithStatusSubresource(objs...).Build()
			broadcaster := record.NewBroadcaster()
			recorder := broadcaster.NewRecorder(scheme, corev1.EventSource{Component: constants.AdmissionName})
			cqCache := schdcache.New(cl)
			qManager := qcache.NewManagerForUnitTests(cl, cqCache)
			scheduler := New(qManager, cqCache, cl, recorder)
			if err := qManager.AddLocalQueue(ctx, q1); err != nil {
				t.Fatalf("Inserting queue %s/%s in manager: %v", q1.Namespace, q1.Name, err)
			}
			if err := qManager.AddClusterQueue(ctx, cq); err != nil {
				t.Fatalf("Inserting clusterQueue %s in manager: %v", cq.Name, err)
			}
			if err := cqCache.AddClusterQueue(ctx, cq); err != nil {
				t.Fatalf("Inserting clusterQueue %s to cache: %v", cq.Name, err)
			}
			if !cqCache.ClusterQueueActive(kueue.ClusterQueueReference(cq.Name)) {
				t.Fatalf("Status of ClusterQueue %s should be active", cq.Name)
			}

			wInfos := qManager.Heads(ctx)
			if len(wInfos) != 1 {
				t.Fatalf("Failed getting heads in cluster queue")
			}
			tc.e.Info = wInfos[0]
			scheduler.requeueAndUpdate(ctx, tc.e)

			qDump := qManager.Dump()
			if diff := cmp.Diff(tc.wantWorkloads, qDump, cmpDump...); diff != "" {
				t.Errorf("Unexpected elements in the cluster queue (-want,+got):\n%s", diff)
			}

			inadmissibleDump := qManager.DumpInadmissible()
			if diff := cmp.Diff(tc.wantInadmissible, inadmissibleDump, cmpDump...); diff != "" {
				t.Errorf("Unexpected elements in the inadmissible stage of the cluster queue (-want,+got):\n%s", diff)
			}

			var updatedWl kueue.Workload
			if err := cl.Get(ctx, client.ObjectKeyFromObject(w1), &updatedWl); err != nil {
				t.Fatalf("Failed obtaining updated object: %v", err)
			}
			if diff := cmp.Diff(tc.wantStatus, updatedWl.Status,
				cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime"),
				cmpopts.SortSlices(func(a, b metav1.Condition) bool {
					return a.Type < b.Type
				})); diff != "" {
				t.Errorf("Unexpected status after updating (-want,+got):\n%s", diff)
			}
			// Make sure a second call doesn't make unnecessary updates.
			scheduler.requeueAndUpdate(ctx, tc.e)
			if updates != tc.wantStatusUpdates {
				t.Errorf("Observed %d status updates, want %d", updates, tc.wantStatusUpdates)
			}
		})
	}
}

func TestResourcesToReserve(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	resourceFlavors := []*kueue.ResourceFlavor{
		utiltestingapi.MakeResourceFlavor("on-demand").Obj(),
		utiltestingapi.MakeResourceFlavor("spot").Obj(),
		utiltestingapi.MakeResourceFlavor("model-a").Obj(),
		utiltestingapi.MakeResourceFlavor("model-b").Obj(),
	}
	cq := utiltestingapi.MakeClusterQueue("cq").
		Cohort("eng").
		ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas("on-demand").
				Resource(corev1.ResourceMemory, "100").Obj(),
			*utiltestingapi.MakeFlavorQuotas("spot").
				Resource(corev1.ResourceMemory, "0", "100").Obj(),
		).
		ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas("model-a").
				Resource("gpu", "10", "0").Obj(),
			*utiltestingapi.MakeFlavorQuotas("model-b").
				Resource("gpu", "10", "5").Obj(),
		).
		QueueingStrategy(kueue.StrictFIFO).
		Obj()

	cases := []struct {
		name            string
		assignmentMode  flavorassigner.FlavorAssignmentMode
		borrowing       int
		assignmentUsage resources.FlavorResourceQuantities
		cqUsage         resources.FlavorResourceQuantities
		wantReserved    resources.FlavorResourceQuantities
	}{
		{
			name:           "Reserved memory and gpu less than assignment usage, assignment preempts",
			assignmentMode: flavorassigner.Preempt,
			assignmentUsage: resources.FlavorResourceQuantities{
				{Flavor: kueue.ResourceFlavorReference("on-demand"), Resource: corev1.ResourceMemory}: 50,
				{Flavor: kueue.ResourceFlavorReference("model-a"), Resource: "gpu"}:                   6,
			},
			cqUsage: resources.FlavorResourceQuantities{
				{Flavor: kueue.ResourceFlavorReference("on-demand"), Resource: corev1.ResourceMemory}: 60,
				{Flavor: kueue.ResourceFlavorReference("spot"), Resource: corev1.ResourceMemory}:      50,
				{Flavor: kueue.ResourceFlavorReference("model-a"), Resource: "gpu"}:                   6,
				{Flavor: kueue.ResourceFlavorReference("model-b"), Resource: "gpu"}:                   2,
			},
			wantReserved: resources.FlavorResourceQuantities{
				{Flavor: kueue.ResourceFlavorReference("on-demand"), Resource: corev1.ResourceMemory}: 40,
				{Flavor: kueue.ResourceFlavorReference("model-a"), Resource: "gpu"}:                   4,
			},
		},
		{
			name:           "Reserved memory equal assignment usage, assignment preempts",
			assignmentMode: flavorassigner.Preempt,
			assignmentUsage: resources.FlavorResourceQuantities{
				{Flavor: kueue.ResourceFlavorReference("on-demand"), Resource: corev1.ResourceMemory}: 30,
				{Flavor: kueue.ResourceFlavorReference("model-a"), Resource: "gpu"}:                   2,
			},
			cqUsage: resources.FlavorResourceQuantities{
				{Flavor: kueue.ResourceFlavorReference("on-demand"), Resource: corev1.ResourceMemory}: 60,
				{Flavor: kueue.ResourceFlavorReference("spot"), Resource: corev1.ResourceMemory}:      50,
				{Flavor: kueue.ResourceFlavorReference("model-a"), Resource: "gpu"}:                   2,
				{Flavor: kueue.ResourceFlavorReference("model-b"), Resource: "gpu"}:                   2,
			},
			wantReserved: resources.FlavorResourceQuantities{
				{Flavor: kueue.ResourceFlavorReference("on-demand"), Resource: corev1.ResourceMemory}: 30,
				{Flavor: kueue.ResourceFlavorReference("model-a"), Resource: "gpu"}:                   2,
			},
		},
		{
			name:           "Reserved memory equal assignment usage, assignment fits",
			assignmentMode: flavorassigner.Fit,
			assignmentUsage: resources.FlavorResourceQuantities{
				{Flavor: kueue.ResourceFlavorReference("on-demand"), Resource: corev1.ResourceMemory}: 50,
				{Flavor: kueue.ResourceFlavorReference("model-a"), Resource: "gpu"}:                   2,
			},
			cqUsage: resources.FlavorResourceQuantities{
				{Flavor: kueue.ResourceFlavorReference("on-demand"), Resource: corev1.ResourceMemory}: 60,
				{Flavor: kueue.ResourceFlavorReference("spot"), Resource: corev1.ResourceMemory}:      50,
				{Flavor: kueue.ResourceFlavorReference("model-a"), Resource: "gpu"}:                   2,
				{Flavor: kueue.ResourceFlavorReference("model-b"), Resource: "gpu"}:                   2,
			},
			wantReserved: resources.FlavorResourceQuantities{
				{Flavor: kueue.ResourceFlavorReference("on-demand"), Resource: corev1.ResourceMemory}: 50,
				{Flavor: kueue.ResourceFlavorReference("model-a"), Resource: "gpu"}:                   2,
			},
		},
		{
			name:           "Reserved memory is 0, CQ is borrowing, assignment preempts without borrowing",
			assignmentMode: flavorassigner.Preempt,
			assignmentUsage: resources.FlavorResourceQuantities{
				{Flavor: kueue.ResourceFlavorReference("spot"), Resource: corev1.ResourceMemory}: 50,
				{Flavor: kueue.ResourceFlavorReference("model-b"), Resource: "gpu"}:              2,
			},
			cqUsage: resources.FlavorResourceQuantities{
				{Flavor: kueue.ResourceFlavorReference("on-demand"), Resource: corev1.ResourceMemory}: 60,
				{Flavor: kueue.ResourceFlavorReference("spot"), Resource: corev1.ResourceMemory}:      60,
				{Flavor: kueue.ResourceFlavorReference("model-a"), Resource: "gpu"}:                   2,
				{Flavor: kueue.ResourceFlavorReference("model-b"), Resource: "gpu"}:                   10,
			},
			wantReserved: resources.FlavorResourceQuantities{
				{Flavor: kueue.ResourceFlavorReference("spot"), Resource: corev1.ResourceMemory}: 0,
				{Flavor: kueue.ResourceFlavorReference("model-b"), Resource: "gpu"}:              0,
			},
		},
		{
			name:           "Reserved memory cut by nominal+borrowing quota, assignment preempts and borrows",
			assignmentMode: flavorassigner.Preempt,
			borrowing:      1,
			assignmentUsage: resources.FlavorResourceQuantities{
				{Flavor: kueue.ResourceFlavorReference("spot"), Resource: corev1.ResourceMemory}: 50,
				{Flavor: kueue.ResourceFlavorReference("model-b"), Resource: "gpu"}:              2,
			},
			cqUsage: resources.FlavorResourceQuantities{
				{Flavor: kueue.ResourceFlavorReference("on-demand"), Resource: corev1.ResourceMemory}: 60,
				{Flavor: kueue.ResourceFlavorReference("spot"), Resource: corev1.ResourceMemory}:      60,
				{Flavor: kueue.ResourceFlavorReference("model-a"), Resource: "gpu"}:                   2,
				{Flavor: kueue.ResourceFlavorReference("model-b"), Resource: "gpu"}:                   10,
			},
			wantReserved: resources.FlavorResourceQuantities{
				{Flavor: kueue.ResourceFlavorReference("spot"), Resource: corev1.ResourceMemory}: 40,
				{Flavor: kueue.ResourceFlavorReference("model-b"), Resource: "gpu"}:              2,
			},
		},
		{
			name:           "Reserved memory equal assignment usage, CQ borrowing limit is nil",
			assignmentMode: flavorassigner.Preempt,
			borrowing:      1,
			assignmentUsage: resources.FlavorResourceQuantities{
				{Flavor: kueue.ResourceFlavorReference("on-demand"), Resource: corev1.ResourceMemory}: 50,
				{Flavor: kueue.ResourceFlavorReference("model-b"), Resource: "gpu"}:                   2,
			},
			cqUsage: resources.FlavorResourceQuantities{
				{Flavor: kueue.ResourceFlavorReference("on-demand"), Resource: corev1.ResourceMemory}: 60,
				{Flavor: kueue.ResourceFlavorReference("spot"), Resource: corev1.ResourceMemory}:      60,
				{Flavor: kueue.ResourceFlavorReference("model-a"), Resource: "gpu"}:                   2,
				{Flavor: kueue.ResourceFlavorReference("model-b"), Resource: "gpu"}:                   10,
			},
			wantReserved: resources.FlavorResourceQuantities{
				{Flavor: kueue.ResourceFlavorReference("on-demand"), Resource: corev1.ResourceMemory}: 50,
				{Flavor: kueue.ResourceFlavorReference("model-b"), Resource: "gpu"}:                   2,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, log := utiltesting.ContextWithLog(t)
			assignment := flavorassigner.Assignment{
				PodSets: []flavorassigner.PodSetAssignment{{
					Name:    "memory",
					Status:  *flavorassigner.NewStatus("needs to preempt"),
					Flavors: flavorassigner.ResourceAssignment{corev1.ResourceMemory: &flavorassigner.FlavorAssignment{Mode: tc.assignmentMode}},
				},
					{
						Name:    "gpu",
						Status:  *flavorassigner.NewStatus("needs to preempt"),
						Flavors: flavorassigner.ResourceAssignment{"gpu": &flavorassigner.FlavorAssignment{Mode: tc.assignmentMode}},
					},
				},
				Borrowing: tc.borrowing,
				Usage:     workload.Usage{Quota: tc.assignmentUsage},
			}
			e := &entry{assignment: assignment, Info: *workload.NewInfo(
				&kueue.Workload{},
			)}
			cl := utiltesting.NewClientBuilder().
				WithLists(&kueue.ClusterQueueList{Items: []kueue.ClusterQueue{*cq}}).
				Build()
			cqCache := schdcache.New(cl)
			for _, flavor := range resourceFlavors {
				cqCache.AddOrUpdateResourceFlavor(log, flavor)
			}
			err := cqCache.AddClusterQueue(ctx, cq)
			if err != nil {
				t.Errorf("Error when adding ClusterQueue to the cache: %v", err)
			}

			i := 0
			for fr, v := range tc.cqUsage {
				quantity := resources.ResourceQuantity(fr.Resource, v)
				admission := utiltestingapi.MakeAdmission("cq").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(fr.Resource, fr.Flavor, quantity.String()).
						Obj()).
					Obj()
				wl := utiltestingapi.MakeWorkload(fmt.Sprintf("workload-%d", i), "default-namespace").ReserveQuotaAt(admission, now).Obj()
				cqCache.AddOrUpdateWorkload(log, wl)
				i++
			}
			snapshot, err := cqCache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("unexpected error while building snapshot: %v", err)
			}
			cqSnapshot := snapshot.ClusterQueue("cq")

			got := resourcesToReserve(e, cqSnapshot)
			if !reflect.DeepEqual(tc.wantReserved, got.Quota) {
				t.Errorf("%s failed\n: Want reservedMem: %v, got: %v", tc.name, tc.wantReserved, got)
			}
		})
	}
}

func TestSchedulerWhenWorkloadModifiedConcurrently(t *testing.T) {
	now := time.Now().Truncate(time.Second)

	ns := utiltesting.MakeNamespaceWrapper("default").Obj()
	rf := utiltestingapi.MakeResourceFlavor("rf").Obj()
	cq := utiltestingapi.MakeClusterQueue("cq").
		ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas(rf.Name).
				Resource(corev1.ResourceCPU, "1").
				Obj(),
		).Obj()
	lq := utiltestingapi.MakeLocalQueue("lq", metav1.NamespaceDefault).ClusterQueue(cq.Name).Obj()

	testCases := map[string]struct {
		workload      *kueue.Workload
		wantWorkloads []kueue.Workload
		wantEvents    []utiltesting.EventRecord
	}{
		"use patch in admit": {
			workload: utiltestingapi.MakeWorkload("wl", metav1.NamespaceDefault).
				ResourceVersion("1").
				Queue(kueue.LocalQueueName(lq.Name)).
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					Request(corev1.ResourceCPU, "1").
					Obj()).
				Obj(),
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl", metav1.NamespaceDefault).
					ResourceVersion("1").
					Queue(kueue.LocalQueueName(lq.Name)).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ResourceVersion("3").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             kueue.WorkloadQuotaReserved,
						Message:            "Quota reserved in ClusterQueue cq",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Admission(
						utiltestingapi.MakeAdmission(cq.Name).
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, kueue.ResourceFlavorReference(rf.Name), "1").
								Obj()).
							Obj(),
					).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionTrue,
						Reason:             kueue.WorkloadAdmitted,
						Message:            "The workload is admitted",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord(metav1.NamespaceDefault, "wl", "QuotaReserved", corev1.EventTypeNormal).Obj(),
				utiltesting.MakeEventRecord(metav1.NamespaceDefault, "wl", "Admitted", corev1.EventTypeNormal).Obj(),
			},
		},
		"use patch in requeueAndUpdate": {
			workload: utiltestingapi.MakeWorkload("wl", metav1.NamespaceDefault).
				ResourceVersion("1").
				Queue(kueue.LocalQueueName(lq.Name)).
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).
					Request(corev1.ResourceCPU, "1").
					Obj()).
				Obj(),
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl", metav1.NamespaceDefault).
					ResourceVersion("1").
					Queue(kueue.LocalQueueName(lq.Name)).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ResourceVersion("3").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "couldn't assign flavors to pod set main: insufficient quota for cpu in flavor rf, previously considered podsets requests (0) + current podset request (2) > maximum capacity (1)",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: kueue.DefaultPodSetName,
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("2"),
						},
					}).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord(metav1.NamespaceDefault, "wl", "Pending", corev1.EventTypeWarning).Obj(),
			},
		},
	}
	for name, tc := range testCases {
		for _, enabled := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s when the WorkloadRequestUseMergePatch feature is %t", name, enabled), func(t *testing.T) {
				features.SetFeatureGateDuringTest(t, features.WorkloadRequestUseMergePatch, enabled)
				ctx, log := utiltesting.ContextWithLog(t)
				var patched bool
				clientBuilder := utiltesting.NewClientBuilder().
					WithObjects(ns.DeepCopy(), rf.DeepCopy(), cq.DeepCopy(), lq.DeepCopy(), tc.workload).
					WithStatusSubresource(&kueue.Workload{}).
					WithInterceptorFuncs(interceptor.Funcs{
						SubResourcePatch: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
							if _, ok := obj.(*kueue.Workload); ok && subResourceName == "status" && !patched {
								patched = true
								// Simulate concurrent modification by another controller
								wlCopy := tc.workload.DeepCopy()
								if wlCopy.Labels == nil {
									wlCopy.Labels = make(map[string]string, 1)
								}
								wlCopy.Labels["test.kueue.x-k8s.io/timestamp"] = time.Now().String()
								if err := c.Update(ctx, wlCopy); err != nil {
									return err
								}
							}
							return utiltesting.TreatSSAAsStrategicMerge(ctx, c, subResourceName, obj, patch, opts...)
						},
					})
				cl := clientBuilder.Build()
				recorder := &utiltesting.EventRecorder{}

				cqCache := schdcache.New(cl)
				qManager := qcache.NewManagerForUnitTests(cl, cqCache)

				cqCache.AddOrUpdateResourceFlavor(log, rf.DeepCopy())
				if err := cqCache.AddClusterQueue(ctx, cq.DeepCopy()); err != nil {
					t.Fatalf("Inserting clusterQueue %s in cache: %v", cq.Name, err)
				}
				if err := qManager.AddClusterQueue(ctx, cq.DeepCopy()); err != nil {
					t.Fatalf("Inserting clusterQueue %s in manager: %v", cq.Name, err)
				}
				if err := qManager.AddLocalQueue(ctx, lq.DeepCopy()); err != nil {
					t.Fatalf("Inserting queue %s/%s in manager: %v", lq.Namespace, lq.Name, err)
				}

				scheduler := New(qManager, cqCache, cl, recorder, WithClock(t, testingclock.NewFakeClock(now)))

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

				gotWorkloads := &kueue.WorkloadList{}
				err := cl.List(ctx, gotWorkloads)
				if err != nil {
					t.Fatalf("Unexpected list workloads error: %v", err)
				}

				opts := cmp.Options{
					cmpopts.EquateEmpty(),
					cmpopts.IgnoreFields(metav1.ObjectMeta{}, "Labels"),
				}

				if diff := cmp.Diff(tc.wantWorkloads, gotWorkloads.Items, opts); diff != "" {
					t.Errorf("Unexpected scheduled workloads (-want,+got):\n%s", diff)
				}

				if diff := cmp.Diff(tc.wantEvents, recorder.RecordedEvents,
					cmpopts.IgnoreFields(utiltesting.EventRecord{}, "Message"),
				); diff != "" {
					t.Errorf("Unexpected events (-want/+got):\n%s", diff)
				}
			})
		}
	}
}
