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
	"maps"
	"reflect"
	goslices "slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/client-go/tools/record"
	"k8s.io/component-base/featuregate"
	"k8s.io/component-base/metrics/testutil"
	testingclock "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	config "sigs.k8s.io/kueue/apis/config/v1beta1"
	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/constants"
	tasindexer "sigs.k8s.io/kueue/pkg/controller/tas/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/queue"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/scheduler/flavorassigner"
	"sigs.k8s.io/kueue/pkg/util/limitrange"
	"sigs.k8s.io/kueue/pkg/util/routine"
	"sigs.k8s.io/kueue/pkg/util/slices"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
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
	now := time.Now()
	fakeClock := testingclock.NewFakeClock(now)
	ignoreEventMessageCmpOpts := cmp.Options{cmpopts.IgnoreFields(utiltesting.EventRecord{}, "Message")}

	resourceFlavors := []*kueue.ResourceFlavor{
		utiltesting.MakeResourceFlavor("default").Obj(),
		utiltesting.MakeResourceFlavor("on-demand").Obj(),
		utiltesting.MakeResourceFlavor("spot").Obj(),
		utiltesting.MakeResourceFlavor("model-a").Obj(),
	}
	clusterQueues := []kueue.ClusterQueue{
		*utiltesting.MakeClusterQueue("sales").
			NamespaceSelector(&metav1.LabelSelector{
				MatchExpressions: []metav1.LabelSelectorRequirement{{
					Key:      "dep",
					Operator: metav1.LabelSelectorOpIn,
					Values:   []string{"sales"},
				}},
			}).
			QueueingStrategy(kueue.StrictFIFO).
			ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "50", "0").Obj()).
			Obj(),
		*utiltesting.MakeClusterQueue("eng-alpha").
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
				*utiltesting.MakeFlavorQuotas("on-demand").
					Resource(corev1.ResourceCPU, "50", "50").Obj(),
				*utiltesting.MakeFlavorQuotas("spot").
					Resource(corev1.ResourceCPU, "100", "0").Obj(),
			).
			Obj(),
		*utiltesting.MakeClusterQueue("eng-beta").
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
				*utiltesting.MakeFlavorQuotas("on-demand").
					Resource(corev1.ResourceCPU, "50", "10").Obj(),
				*utiltesting.MakeFlavorQuotas("spot").
					Resource(corev1.ResourceCPU, "0", "100").Obj(),
			).
			ResourceGroup(
				*utiltesting.MakeFlavorQuotas("model-a").
					Resource("example.com/gpu", "20", "0").Obj(),
			).
			Obj(),
		*utiltesting.MakeClusterQueue("flavor-nonexistent-cq").
			QueueingStrategy(kueue.StrictFIFO).
			ResourceGroup(*utiltesting.MakeFlavorQuotas("nonexistent-flavor").
				Resource(corev1.ResourceCPU, "50").Obj()).
			Obj(),
		*utiltesting.MakeClusterQueue("lend-a").
			Cohort("lend").
			NamespaceSelector(&metav1.LabelSelector{
				MatchExpressions: []metav1.LabelSelectorRequirement{{
					Key:      "dep",
					Operator: metav1.LabelSelectorOpIn,
					Values:   []string{"lend"},
				}},
			}).
			ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "3", "", "2").Obj()).
			Obj(),
		*utiltesting.MakeClusterQueue("lend-b").
			Cohort("lend").
			NamespaceSelector(&metav1.LabelSelector{
				MatchExpressions: []metav1.LabelSelectorRequirement{{
					Key:      "dep",
					Operator: metav1.LabelSelectorOpIn,
					Values:   []string{"lend"},
				}},
			}).
			ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "2", "", "2").Obj()).
			Obj(),
	}
	queues := []kueue.LocalQueue{
		{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "sales",
				Name:      "main",
			},
			Spec: kueue.LocalQueueSpec{
				ClusterQueue: "sales",
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "sales",
				Name:      "blocked",
			},
			Spec: kueue.LocalQueueSpec{
				ClusterQueue: "eng-alpha",
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "eng-alpha",
				Name:      "main",
			},
			Spec: kueue.LocalQueueSpec{
				ClusterQueue: "eng-alpha",
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "eng-beta",
				Name:      "main",
			},
			Spec: kueue.LocalQueueSpec{
				ClusterQueue: "eng-beta",
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "sales",
				Name:      "flavor-nonexistent-queue",
			},
			Spec: kueue.LocalQueueSpec{
				ClusterQueue: "flavor-nonexistent-cq",
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "sales",
				Name:      "cq-nonexistent-queue",
			},
			Spec: kueue.LocalQueueSpec{
				ClusterQueue: "nonexistent-cq",
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "lend",
				Name:      "lend-a-queue",
			},
			Spec: kueue.LocalQueueSpec{
				ClusterQueue: "lend-a",
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "lend",
				Name:      "lend-b-queue",
			},
			Spec: kueue.LocalQueueSpec{
				ClusterQueue: "lend-b",
			},
		},
	}
	cases := map[string]struct {
		// Features
		disableLendingLimit               bool
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
		// wantScheduled is the subset of workloads that got scheduled/admitted in this cycle.
		wantScheduled []workload.Reference
		// workloadCmpOpts are the cmp options to compare workloads.
		workloadCmpOpts cmp.Options
		// wantWorkloads is the subset of workloads that got admitted in this cycle.
		wantWorkloads             []kueue.Workload
		wantWorkloadsWithStatuses []kueue.Workload
		// wantLeft is the workload keys that are left in the queues after this cycle.
		wantLeft map[kueue.ClusterQueueReference][]workload.Reference
		// wantInadmissibleLeft is the workload keys that are left in the inadmissible state after this cycle.
		wantInadmissibleLeft map[kueue.ClusterQueueReference][]workload.Reference
		// wantPreempted is the keys of the workloads that get preempted in the scheduling cycle.
		wantPreempted sets.Set[workload.Reference]
		// wantEvents ignored if empty, the Message is ignored (it contains the duration)
		wantEvents []utiltesting.EventRecord
		// eventCmpOpts are the cmp options to compare recorded events.
		eventCmpOpts cmp.Options

		wantSkippedPreemptions map[string]int

		// trackPatchWorkloads flag to collect workload patches and use patched workloads
		// for "wantWorkloads" assertion.
		trackPatchedWorkloads bool
	}{
		"use second flavor when the first has no preemption candidates; WhenCanPreempt: Preempt": {
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltesting.MakeClusterQueue("other-alpha").
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
					).Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltesting.MakeLocalQueue("other", "eng-alpha").ClusterQueue("other-alpha").Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("admitted", "eng-alpha").
					Queue("other").
					Request(corev1.ResourceCPU, "50").
					ReserveQuota(utiltesting.MakeAdmission("other-alpha").Assignment(corev1.ResourceCPU, "on-demand", "50").Obj()).
					Admitted(true).
					Obj(),
				*utiltesting.MakeWorkload("new", "eng-alpha").
					Queue("other").
					Request(corev1.ResourceCPU, "20").
					Obj(),
			},
			wantPreempted: sets.Set[workload.Reference]{},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/admitted": {
					ClusterQueue: "other-alpha",
					PodSetAssignments: []kueue.PodSetAssignment{
						{
							Name: "main",
							Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
								corev1.ResourceCPU: "on-demand",
							},
							ResourceUsage: corev1.ResourceList{
								corev1.ResourceCPU: resource.MustParse("50"),
							},
							Count: ptr.To[int32](1),
						},
					},
				},
				"eng-alpha/new": {
					ClusterQueue: "other-alpha",
					PodSetAssignments: []kueue.PodSetAssignment{
						{
							Name: "main",
							Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
								corev1.ResourceCPU: "spot",
							},
							ResourceUsage: corev1.ResourceList{
								corev1.ResourceCPU: resource.MustParse("20"),
							},
							Count: ptr.To[int32](1),
						},
					},
				},
			},
			wantScheduled: []workload.Reference{"eng-alpha/new"},
		},
		"workload fits in single clusterQueue, with check state ready": {
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "sales").
					Queue("main").
					PodSets(*utiltesting.MakePodSet("one", 10).
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
						{
							Name: "one",
							Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
								corev1.ResourceCPU: "default",
							},
							ResourceUsage: corev1.ResourceList{
								corev1.ResourceCPU: resource.MustParse("10000m"),
							},
							Count: ptr.To[int32](10),
						},
					},
				},
			},
			wantScheduled: []workload.Reference{"sales/foo"},
			workloadCmpOpts: cmp.Options{
				cmpopts.EquateEmpty(),
				cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime"),
				cmpopts.IgnoreFields(
					kueue.Workload{}, "TypeMeta", "ObjectMeta.Name", "ObjectMeta.ResourceVersion",
				),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "sales").
					Queue("main").
					PodSets(*utiltesting.MakePodSet("one", 10).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "check",
						State: kueue.CheckStateReady,
					}).
					Admission(
						utiltesting.MakeAdmission("sales", "one").
							Assignment(corev1.ResourceCPU, "default", "10000m").
							AssignmentPodCount(10).
							Obj(),
					).
					Generation(1).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             kueue.WorkloadQuotaReserved,
						Message:            "Quota reserved in ClusterQueue sales",
						ObservedGeneration: 1,
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionTrue,
						Reason:             kueue.WorkloadAdmitted,
						Message:            "The workload is admitted",
						ObservedGeneration: 1,
					}).
					Obj(),
			},
			eventCmpOpts: ignoreEventMessageCmpOpts,
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "sales", Name: "foo"},
					Reason:    "QuotaReserved",
					EventType: corev1.EventTypeNormal,
				},
				{
					Key:       types.NamespacedName{Namespace: "sales", Name: "foo"},
					Reason:    "Admitted",
					EventType: corev1.EventTypeNormal,
				},
			},
		},
		"skip workload with missing or deleted ClusterQueue (NoFit)": {
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("missing-cq-workload", "sales").
					Queue("non-existent-queue").
					PodSets(*utiltesting.MakePodSet("set", 1).
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
				*utiltesting.MakeWorkload("foo", "sales").
					Queue("main").
					PodSets(*utiltesting.MakePodSet("one", 10).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "check",
						State: kueue.CheckStatePending,
					}).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"sales/foo": {
					ClusterQueue: "sales",
					PodSetAssignments: []kueue.PodSetAssignment{
						{
							Name: "one",
							Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
								corev1.ResourceCPU: "default",
							},
							ResourceUsage: corev1.ResourceList{
								corev1.ResourceCPU: resource.MustParse("10000m"),
							},
							Count: ptr.To[int32](10),
						},
					},
				},
			},
			wantScheduled: []workload.Reference{"sales/foo"},
			eventCmpOpts:  ignoreEventMessageCmpOpts,
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "sales", Name: "foo"},
					Reason:    "QuotaReserved",
					EventType: corev1.EventTypeNormal,
				},
			},
		},
		"error during admission": {
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "sales").
					Queue("main").
					PodSets(*utiltesting.MakePodSet("one", 10).
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
				*utiltesting.MakeWorkload("new", "sales").
					Queue("main").
					PodSets(*utiltesting.MakePodSet("one", 11).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("assigned", "sales").
					PodSets(*utiltesting.MakePodSet("one", 40).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuota(utiltesting.MakeAdmission("sales", "one").Assignment(corev1.ResourceCPU, "default", "40000m").AssignmentPodCount(40).Obj()).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"sales/assigned": {
					ClusterQueue: "sales",
					PodSetAssignments: []kueue.PodSetAssignment{
						{
							Name: "one",
							Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
								corev1.ResourceCPU: "default",
							},
							ResourceUsage: corev1.ResourceList{
								corev1.ResourceCPU: resource.MustParse("40000m"),
							},
							Count: ptr.To[int32](40),
						},
					},
				},
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"sales": {"sales/new"},
			},
		},
		"failed to match clusterQueue selector": {
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("new", "sales").
					Queue("blocked").
					PodSets(*utiltesting.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantInadmissibleLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"eng-alpha": {"sales/new"},
			},
		},
		"admit in different cohorts": {
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("new", "sales").
					Queue("main").
					PodSets(*utiltesting.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("new", "eng-alpha").
					Queue("main").
					PodSets(*utiltesting.MakePodSet("one", 51 /* Will borrow */).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"sales/new": {
					ClusterQueue: "sales",
					PodSetAssignments: []kueue.PodSetAssignment{
						{
							Name: "one",
							Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
								corev1.ResourceCPU: "default",
							},
							ResourceUsage: corev1.ResourceList{
								corev1.ResourceCPU: resource.MustParse("1000m"),
							},
							Count: ptr.To[int32](1),
						},
					},
				},
				"eng-alpha/new": {
					ClusterQueue: "eng-alpha",
					PodSetAssignments: []kueue.PodSetAssignment{
						{
							Name: "one",
							Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
								corev1.ResourceCPU: "on-demand",
							},
							ResourceUsage: corev1.ResourceList{
								corev1.ResourceCPU: resource.MustParse("51000m"),
							},
							Count: ptr.To[int32](51),
						},
					},
				},
			},
			wantScheduled: []workload.Reference{"sales/new", "eng-alpha/new"},
		},
		"admit in same cohort with no borrowing": {
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("new", "eng-alpha").
					Queue("main").
					PodSets(*utiltesting.MakePodSet("one", 40).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("new", "eng-beta").
					Queue("main").
					PodSets(*utiltesting.MakePodSet("one", 40).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/new": {
					ClusterQueue: "eng-alpha",
					PodSetAssignments: []kueue.PodSetAssignment{
						{
							Name: "one",
							Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
								corev1.ResourceCPU: "on-demand",
							},
							ResourceUsage: corev1.ResourceList{
								corev1.ResourceCPU: resource.MustParse("40000m"),
							},
							Count: ptr.To[int32](40),
						},
					},
				},
				"eng-beta/new": {
					ClusterQueue: "eng-beta",
					PodSetAssignments: []kueue.PodSetAssignment{
						{
							Name: "one",
							Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
								corev1.ResourceCPU: "on-demand",
							},
							ResourceUsage: corev1.ResourceList{
								corev1.ResourceCPU: resource.MustParse("40000m"),
							},
							Count: ptr.To[int32](40),
						},
					},
				},
			},
			wantScheduled: []workload.Reference{"eng-alpha/new", "eng-beta/new"},
		},
		"assign multiple resources and flavors": {
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("new", "eng-beta").
					Queue("main").
					PodSets(
						*utiltesting.MakePodSet("one", 10).
							Request(corev1.ResourceCPU, "6").
							Request("example.com/gpu", "1").
							Obj(),
						*utiltesting.MakePodSet("two", 40).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-beta/new": {
					ClusterQueue: "eng-beta",
					PodSetAssignments: []kueue.PodSetAssignment{
						{
							Name: "one",
							Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
								corev1.ResourceCPU: "on-demand",
								"example.com/gpu":  "model-a",
							},
							ResourceUsage: corev1.ResourceList{
								corev1.ResourceCPU: resource.MustParse("60000m"),
								"example.com/gpu":  resource.MustParse("10"),
							},
							Count: ptr.To[int32](10),
						},
						{
							Name: "two",
							Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
								corev1.ResourceCPU: "spot",
							},
							ResourceUsage: corev1.ResourceList{
								corev1.ResourceCPU: resource.MustParse("40000m"),
							},
							Count: ptr.To[int32](40),
						},
					},
				},
			},
			wantScheduled: []workload.Reference{"eng-beta/new"},
		},
		"cannot borrow if cohort was assigned and would result in overadmission": {
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("new", "eng-alpha").
					Queue("main").
					PodSets(*utiltesting.MakePodSet("one", 45).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("new", "eng-beta").
					Queue("main").
					PodSets(*utiltesting.MakePodSet("one", 56).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/new": {
					ClusterQueue: "eng-alpha",
					PodSetAssignments: []kueue.PodSetAssignment{
						{
							Name: "one",
							Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
								corev1.ResourceCPU: "on-demand",
							},
							ResourceUsage: corev1.ResourceList{
								corev1.ResourceCPU: resource.MustParse("45000m"),
							},
							Count: ptr.To[int32](45),
						},
					},
				},
			},
			wantScheduled: []workload.Reference{"eng-alpha/new"},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"eng-beta": {"eng-beta/new"},
			},
		},
		"can borrow if cohort was assigned and will not result in overadmission": {
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("new", "eng-alpha").
					Queue("main").
					PodSets(*utiltesting.MakePodSet("one", 45).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("new", "eng-beta").
					Queue("main").
					PodSets(*utiltesting.MakePodSet("one", 55).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/new": {
					ClusterQueue: "eng-alpha",
					PodSetAssignments: []kueue.PodSetAssignment{
						{
							Name: "one",
							Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
								corev1.ResourceCPU: "on-demand",
							},
							ResourceUsage: corev1.ResourceList{
								corev1.ResourceCPU: resource.MustParse("45000m"),
							},
							Count: ptr.To[int32](45),
						},
					},
				},
				"eng-beta/new": {
					ClusterQueue: "eng-beta",
					PodSetAssignments: []kueue.PodSetAssignment{
						{
							Name: "one",
							Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
								corev1.ResourceCPU: "on-demand",
							},
							ResourceUsage: corev1.ResourceList{
								corev1.ResourceCPU: resource.MustParse("55000m"),
							},
							Count: ptr.To[int32](55),
						},
					},
				},
			},
			wantScheduled: []workload.Reference{"eng-alpha/new", "eng-beta/new"},
		},
		"can borrow if needs reclaim from cohort in different flavor": {
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("can-reclaim", "eng-alpha").
					Queue("main").
					Request(corev1.ResourceCPU, "100").
					Obj(),
				*utiltesting.MakeWorkload("needs-to-borrow", "eng-beta").
					Queue("main").
					Request(corev1.ResourceCPU, "1").
					Obj(),
				*utiltesting.MakeWorkload("user-on-demand", "eng-beta").
					Request(corev1.ResourceCPU, "50").
					ReserveQuota(utiltesting.MakeAdmission("eng-beta").Assignment(corev1.ResourceCPU, "on-demand", "50000m").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("user-spot", "eng-beta").
					Request(corev1.ResourceCPU, "1").
					ReserveQuota(utiltesting.MakeAdmission("eng-beta").Assignment(corev1.ResourceCPU, "spot", "1000m").Obj()).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"eng-alpha": {"eng-alpha/can-reclaim"},
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-beta/user-spot":       *utiltesting.MakeAdmission("eng-beta").Assignment(corev1.ResourceCPU, "spot", "1000m").Obj(),
				"eng-beta/user-on-demand":  *utiltesting.MakeAdmission("eng-beta").Assignment(corev1.ResourceCPU, "on-demand", "50000m").Obj(),
				"eng-beta/needs-to-borrow": *utiltesting.MakeAdmission("eng-beta").Assignment(corev1.ResourceCPU, "on-demand", "1000m").Obj(),
			},
			wantScheduled: []workload.Reference{
				"eng-beta/needs-to-borrow",
			},
		},
		"workload exceeds lending limit when borrow in cohort": {
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a", "lend").
					Request(corev1.ResourceCPU, "2").
					ReserveQuota(utiltesting.MakeAdmission("lend-b").Assignment(corev1.ResourceCPU, "default", "2000m").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("b", "lend").
					Queue("lend-b-queue").
					Request(corev1.ResourceCPU, "3").
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"lend/a": *utiltesting.MakeAdmission("lend-b").Assignment(corev1.ResourceCPU, "default", "2000m").Obj(),
			},
			wantInadmissibleLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"lend-b": {"lend/b"},
			},
		},
		"lendingLimit should not affect assignments when feature disabled": {
			disableLendingLimit: true,
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a", "lend").
					Request(corev1.ResourceCPU, "2").
					ReserveQuota(utiltesting.MakeAdmission("lend-b").Assignment(corev1.ResourceCPU, "default", "2000m").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("b", "lend").
					Queue("lend-b-queue").
					Request(corev1.ResourceCPU, "3").
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"lend/a": *utiltesting.MakeAdmission("lend-b").Assignment(corev1.ResourceCPU, "default", "2000m").Obj(),
				"lend/b": *utiltesting.MakeAdmission("lend-b").Assignment(corev1.ResourceCPU, "default", "3000m").Obj(),
			},
			wantScheduled: []workload.Reference{
				"lend/b",
			},
		},
		"preempt workloads in ClusterQueue and cohort": {
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("preemptor", "eng-beta").
					Queue("main").
					Request(corev1.ResourceCPU, "20").
					Obj(),
				*utiltesting.MakeWorkload("use-all-spot", "eng-alpha").
					Request(corev1.ResourceCPU, "100").
					ReserveQuota(utiltesting.MakeAdmission("eng-alpha").Assignment(corev1.ResourceCPU, "spot", "100000m").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("low-1", "eng-beta").
					Priority(-1).
					Request(corev1.ResourceCPU, "30").
					ReserveQuota(utiltesting.MakeAdmission("eng-beta").Assignment(corev1.ResourceCPU, "on-demand", "30000m").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("low-2", "eng-beta").
					Priority(-2).
					Request(corev1.ResourceCPU, "10").
					ReserveQuota(utiltesting.MakeAdmission("eng-beta").Assignment(corev1.ResourceCPU, "on-demand", "10000m").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("borrower", "eng-alpha").
					Request(corev1.ResourceCPU, "60").
					ReserveQuota(utiltesting.MakeAdmission("eng-alpha").Assignment(corev1.ResourceCPU, "on-demand", "60000m").Obj()).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				// Preemptor is not admitted in this cycle.
				"eng-beta": {"eng-beta/preemptor"},
			},
			wantPreempted: sets.New[workload.Reference]("eng-alpha/borrower", "eng-beta/low-2"),
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/use-all-spot": *utiltesting.MakeAdmission("eng-alpha").Assignment(corev1.ResourceCPU, "spot", "100").Obj(),
				"eng-beta/low-1":         *utiltesting.MakeAdmission("eng-beta").Assignment(corev1.ResourceCPU, "on-demand", "30").Obj(),
				// Removal from cache for the preempted workloads is deferred until we receive Workload updates
				"eng-beta/low-2":     *utiltesting.MakeAdmission("eng-beta").Assignment(corev1.ResourceCPU, "on-demand", "10").Obj(),
				"eng-alpha/borrower": *utiltesting.MakeAdmission("eng-alpha").Assignment(corev1.ResourceCPU, "on-demand", "60").Obj(),
			},
		},
		"multiple CQs need preemption": {
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltesting.MakeClusterQueue("other-alpha").
					Cohort("other").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "50", "50").Obj(),
					).
					Obj(),
				*utiltesting.MakeClusterQueue("other-beta").
					Cohort("other").
					Preemption(kueue.ClusterQueuePreemption{
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
					}).
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "50", "10").Obj(),
					).
					Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltesting.MakeLocalQueue("other", "eng-alpha").ClusterQueue("other-alpha").Obj(),
				*utiltesting.MakeLocalQueue("other", "eng-beta").ClusterQueue("other-beta").Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("preemptor", "eng-beta").
					Priority(-1).
					Queue("other").
					Request(corev1.ResourceCPU, "1").
					Obj(),
				*utiltesting.MakeWorkload("pending", "eng-alpha").
					Priority(1).
					Queue("other").
					Request(corev1.ResourceCPU, "1").
					Obj(),
				*utiltesting.MakeWorkload("use-all", "eng-alpha").
					Request(corev1.ResourceCPU, "100").
					ReserveQuota(utiltesting.MakeAdmission("other-alpha").Assignment(corev1.ResourceCPU, "on-demand", "100").Obj()).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				// Preemptor is not admitted in this cycle.
				"other-beta": {"eng-beta/preemptor"},
			},
			wantInadmissibleLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"other-alpha": {"eng-alpha/pending"},
			},
			wantPreempted: sets.New[workload.Reference]("eng-alpha/use-all"),
			wantAssignments: map[workload.Reference]kueue.Admission{
				// Removal from cache for the preempted workloads is deferred until we receive Workload updates
				"eng-alpha/use-all": *utiltesting.MakeAdmission("other-alpha").Assignment(corev1.ResourceCPU, "on-demand", "100").Obj(),
			},
		},
		"cannot borrow resource not listed in clusterQueue": {
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("new", "eng-alpha").
					Queue("main").
					Request("example.com/gpu", "1").
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"eng-alpha": {"eng-alpha/new"},
			},
		},
		"not enough resources to borrow, fallback to next flavor; WhenCanPreempt: TryNextFlavor": {
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("new", "eng-alpha").
					Queue("main").
					PodSets(*utiltesting.MakePodSet("one", 60).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("existing", "eng-beta").
					PodSets(*utiltesting.MakePodSet("one", 45).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuota(utiltesting.MakeAdmission("eng-beta", "one").Assignment(corev1.ResourceCPU, "on-demand", "45000m").AssignmentPodCount(45).Obj()).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/new": {
					ClusterQueue: "eng-alpha",
					PodSetAssignments: []kueue.PodSetAssignment{
						{
							Name: "one",
							Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
								corev1.ResourceCPU: "spot",
							},
							ResourceUsage: corev1.ResourceList{
								corev1.ResourceCPU: resource.MustParse("60000m"),
							},
							Count: ptr.To[int32](60),
						},
					},
				},
				"eng-beta/existing": {
					ClusterQueue: "eng-beta",
					PodSetAssignments: []kueue.PodSetAssignment{
						{
							Name: "one",
							Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
								corev1.ResourceCPU: "on-demand",
							},
							ResourceUsage: corev1.ResourceList{
								corev1.ResourceCPU: resource.MustParse("45000m"),
							},
							Count: ptr.To[int32](45),
						},
					},
				},
			},
			wantScheduled: []workload.Reference{"eng-alpha/new"},
		},
		"workload should not fit in nonexistent clusterQueue": {
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "sales").
					Queue("cq-nonexistent-queue").
					Request(corev1.ResourceCPU, "1").
					Obj(),
			},
		},
		"workload should not fit in clusterQueue with nonexistent flavor": {
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "sales").
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
				*utiltesting.MakeWorkload("new", "eng-beta").
					Queue("main").
					Creation(now.Add(-2 * time.Second)).
					PodSets(*utiltesting.MakePodSet("one", 50).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("new-alpha", "eng-alpha").
					Queue("main").
					Creation(now.Add(-time.Second)).
					PodSets(*utiltesting.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("new-gamma", "eng-gamma").
					Queue("main").
					Creation(now).
					PodSets(*utiltesting.MakePodSet("one", 50).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("existing", "eng-gamma").
					PodSets(
						*utiltesting.MakePodSet("borrow-on-demand", 51).
							Request(corev1.ResourceCPU, "1").
							Obj(),
						*utiltesting.MakePodSet("use-all-spot", 100).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					ReserveQuota(utiltesting.MakeAdmission("eng-gamma").
						PodSets(
							kueue.PodSetAssignment{
								Name: "borrow-on-demand",
								Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
									corev1.ResourceCPU: "on-demand",
								},
								ResourceUsage: corev1.ResourceList{
									corev1.ResourceCPU: resource.MustParse("51"),
								},
								Count: ptr.To[int32](51),
							},
							kueue.PodSetAssignment{
								Name: "use-all-spot",
								Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
									corev1.ResourceCPU: "spot",
								},
								ResourceUsage: corev1.ResourceList{
									corev1.ResourceCPU: resource.MustParse("100"),
								},
								Count: ptr.To[int32](100),
							},
						).
						Obj()).
					Obj(),
			},
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltesting.MakeClusterQueue("eng-gamma").
					Cohort("eng").
					Preemption(kueue.ClusterQueuePreemption{
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
					}).
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "50", "10").Obj(),
						*utiltesting.MakeFlavorQuotas("spot").
							Resource(corev1.ResourceCPU, "0", "100").Obj(),
					).
					Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "eng-gamma",
						Name:      "main",
					},
					Spec: kueue.LocalQueueSpec{
						ClusterQueue: "eng-gamma",
					},
				},
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-gamma/existing": *utiltesting.MakeAdmission("eng-gamma").
					PodSets(
						kueue.PodSetAssignment{
							Name: "borrow-on-demand",
							Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
								corev1.ResourceCPU: "on-demand",
							},
							ResourceUsage: corev1.ResourceList{
								corev1.ResourceCPU: resource.MustParse("51"),
							},
							Count: ptr.To[int32](51),
						},
						kueue.PodSetAssignment{
							Name: "use-all-spot",
							Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
								corev1.ResourceCPU: "spot",
							},
							ResourceUsage: corev1.ResourceList{
								corev1.ResourceCPU: resource.MustParse("100"),
							},
							Count: ptr.To[int32](100),
						},
					).Obj(),
				"eng-beta/new":        *utiltesting.MakeAdmission("eng-beta", "one").Assignment(corev1.ResourceCPU, "on-demand", "50").AssignmentPodCount(50).Obj(),
				"eng-alpha/new-alpha": *utiltesting.MakeAdmission("eng-alpha", "one").Assignment(corev1.ResourceCPU, "on-demand", "1").AssignmentPodCount(1).Obj(),
			},
			wantScheduled: []workload.Reference{"eng-beta/new", "eng-alpha/new-alpha"},
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
				*utiltesting.MakeWorkload("new", "sales").
					Queue("main").
					PodSets(*utiltesting.MakePodSet("one", 50).
						SetMinimumCount(20).
						Request(corev1.ResourceCPU, "2").
						Obj()).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"sales/new": {
					ClusterQueue: "sales",
					PodSetAssignments: []kueue.PodSetAssignment{
						{
							Name: "one",
							Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
								corev1.ResourceCPU: "default",
							},
							ResourceUsage: corev1.ResourceList{
								corev1.ResourceCPU: resource.MustParse("50000m"),
							},
							Count: ptr.To[int32](25),
						},
					},
				},
			},
			wantScheduled: []workload.Reference{"sales/new"},
		},
		"partial admission single variable pod set, preempt first": {
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("new", "eng-beta").
					Queue("main").
					Priority(4).
					PodSets(*utiltesting.MakePodSet("one", 20).
						SetMinimumCount(10).
						Request("example.com/gpu", "1").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("old", "eng-beta").
					Priority(-4).
					PodSets(*utiltesting.MakePodSet("one", 10).
						Request("example.com/gpu", "1").
						Obj()).
					ReserveQuota(utiltesting.MakeAdmission("eng-beta", "one").Assignment("example.com/gpu", "model-a", "10").AssignmentPodCount(10).Obj()).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-beta/old": {
					ClusterQueue: "eng-beta",
					PodSetAssignments: []kueue.PodSetAssignment{
						{
							Name: "one",
							Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
								"example.com/gpu": "model-a",
							},
							ResourceUsage: corev1.ResourceList{
								"example.com/gpu": resource.MustParse("10"),
							},
							Count: ptr.To[int32](10),
						},
					},
				},
			},
			wantPreempted: sets.New[workload.Reference]("eng-beta/old"),
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"eng-beta": {"eng-beta/new"},
			},
		},
		"partial admission single variable pod set, preempt with partial admission": {
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("new", "eng-beta").
					Queue("main").
					Priority(4).
					PodSets(*utiltesting.MakePodSet("one", 30).
						SetMinimumCount(10).
						Request("example.com/gpu", "1").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("old", "eng-beta").
					Priority(-4).
					PodSets(*utiltesting.MakePodSet("one", 10).
						Request("example.com/gpu", "1").
						Obj()).
					ReserveQuota(utiltesting.MakeAdmission("eng-beta", "one").Assignment("example.com/gpu", "model-a", "10").AssignmentPodCount(10).Obj()).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-beta/old": {
					ClusterQueue: "eng-beta",
					PodSetAssignments: []kueue.PodSetAssignment{
						{
							Name: "one",
							Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
								"example.com/gpu": "model-a",
							},
							ResourceUsage: corev1.ResourceList{
								"example.com/gpu": resource.MustParse("10"),
							},
							Count: ptr.To[int32](10),
						},
					},
				},
			},
			wantPreempted: sets.New[workload.Reference]("eng-beta/old"),
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"eng-beta": {"eng-beta/new"},
			},
		},
		"partial admission multiple variable pod sets": {
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("new", "sales").
					Queue("main").
					PodSets(
						*utiltesting.MakePodSet("one", 20).
							Request(corev1.ResourceCPU, "1").
							Obj(),
						*utiltesting.MakePodSet("two", 30).
							SetMinimumCount(10).
							Request(corev1.ResourceCPU, "1").
							Obj(),
						*utiltesting.MakePodSet("three", 15).
							SetMinimumCount(5).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"sales/new": {
					ClusterQueue: "sales",
					PodSetAssignments: []kueue.PodSetAssignment{
						{
							Name: "one",
							Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
								corev1.ResourceCPU: "default",
							},
							ResourceUsage: corev1.ResourceList{
								corev1.ResourceCPU: resource.MustParse("20000m"),
							},
							Count: ptr.To[int32](20),
						},
						{
							Name: "two",
							Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
								corev1.ResourceCPU: "default",
							},
							ResourceUsage: corev1.ResourceList{
								corev1.ResourceCPU: resource.MustParse("20000m"),
							},
							Count: ptr.To[int32](20),
						},
						{
							Name: "three",
							Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
								corev1.ResourceCPU: "default",
							},
							ResourceUsage: corev1.ResourceList{
								corev1.ResourceCPU: resource.MustParse("10000m"),
							},
							Count: ptr.To[int32](10),
						},
					},
				},
			},
			wantScheduled: []workload.Reference{"sales/new"},
		},
		"partial admission disabled, multiple variable pod sets": {
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("new", "sales").
					Queue("main").
					PodSets(
						*utiltesting.MakePodSet("one", 20).
							Request(corev1.ResourceCPU, "1").
							Obj(),
						*utiltesting.MakePodSet("two", 30).
							SetMinimumCount(10).
							Request(corev1.ResourceCPU, "1").
							Obj(),
						*utiltesting.MakePodSet("three", 15).
							SetMinimumCount(5).
							Request(corev1.ResourceCPU, "1").
							Obj(),
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
				rg := *utiltesting.MakeFlavorQuotas("default").Resource("r1", "10", "10").Resource("r2", "10", "10").Obj()
				cq1 := *utiltesting.MakeClusterQueue("cq1").Cohort("co").Preemption(preemption).ResourceGroup(rg).Obj()
				cq2 := *utiltesting.MakeClusterQueue("cq2").Cohort("co").Preemption(preemption).ResourceGroup(rg).Obj()
				cq3 := *utiltesting.MakeClusterQueue("cq3").Cohort("co").Preemption(preemption).ResourceGroup(rg).Obj()
				return []kueue.ClusterQueue{cq1, cq2, cq3}
			}(),
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltesting.MakeLocalQueue("lq1", "sales").ClusterQueue("cq1").Obj(),
				*utiltesting.MakeLocalQueue("lq2", "sales").ClusterQueue("cq2").Obj(),
				*utiltesting.MakeLocalQueue("lq3", "sales").ClusterQueue("cq3").Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("wl1", "sales").Queue("lq1").Priority(-1).PodSets(
					*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).Request("r1", "16").Obj(),
				).Obj(),
				*utiltesting.MakeWorkload("wl2", "sales").Queue("lq2").Priority(-2).PodSets(
					*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).Request("r2", "16").Obj(),
				).Obj(),
			},
			wantScheduled: []workload.Reference{"sales/wl1", "sales/wl2"},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"sales/wl1": *utiltesting.MakeAdmission("cq1", kueue.DefaultPodSetName).
					Assignment("r1", "default", "16").AssignmentPodCount(1).
					Obj(),
				"sales/wl2": *utiltesting.MakeAdmission("cq2", kueue.DefaultPodSetName).
					Assignment("r2", "default", "16").AssignmentPodCount(1).
					Obj(),
			},
		},
		"two workloads can borrow the same resources from the same flavor in the same cycle if fits in the cohort quota": {
			additionalClusterQueues: func() []kueue.ClusterQueue {
				preemption := kueue.ClusterQueuePreemption{
					ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
				}
				rg := *utiltesting.MakeFlavorQuotas("default").Resource("r1", "10", "10").Resource("r2", "10", "10").Obj()
				cq1 := *utiltesting.MakeClusterQueue("cq1").Cohort("co").Preemption(preemption).ResourceGroup(rg).Obj()
				cq2 := *utiltesting.MakeClusterQueue("cq2").Cohort("co").Preemption(preemption).ResourceGroup(rg).Obj()
				cq3 := *utiltesting.MakeClusterQueue("cq3").Cohort("co").Preemption(preemption).ResourceGroup(rg).Obj()
				return []kueue.ClusterQueue{cq1, cq2, cq3}
			}(),
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltesting.MakeLocalQueue("lq1", "sales").ClusterQueue("cq1").Obj(),
				*utiltesting.MakeLocalQueue("lq2", "sales").ClusterQueue("cq2").Obj(),
				*utiltesting.MakeLocalQueue("lq3", "sales").ClusterQueue("cq3").Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("wl1", "sales").Queue("lq1").Priority(-1).PodSets(
					*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).Request("r1", "16").Obj(),
				).Obj(),
				*utiltesting.MakeWorkload("wl2", "sales").Queue("lq2").Priority(-2).PodSets(
					*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).Request("r1", "14").Obj(),
				).Obj(),
			},
			wantScheduled: []workload.Reference{"sales/wl1", "sales/wl2"},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"sales/wl1": *utiltesting.MakeAdmission("cq1", kueue.DefaultPodSetName).
					Assignment("r1", "default", "16").AssignmentPodCount(1).
					Obj(),
				"sales/wl2": *utiltesting.MakeAdmission("cq2", kueue.DefaultPodSetName).
					Assignment("r1", "default", "14").AssignmentPodCount(1).
					Obj(),
			},
		},
		"only one workload can borrow one resources from the same flavor in the same cycle if cohort quota cannot fit": {
			additionalClusterQueues: func() []kueue.ClusterQueue {
				preemption := kueue.ClusterQueuePreemption{
					ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
				}
				rg := *utiltesting.MakeFlavorQuotas("default").Resource("r1", "10", "10").Resource("r2", "10", "10").Obj()
				cq1 := *utiltesting.MakeClusterQueue("cq1").Cohort("co").Preemption(preemption).ResourceGroup(rg).Obj()
				cq2 := *utiltesting.MakeClusterQueue("cq2").Cohort("co").Preemption(preemption).ResourceGroup(rg).Obj()
				cq3 := *utiltesting.MakeClusterQueue("cq3").Cohort("co").Preemption(preemption).ResourceGroup(rg).Obj()
				return []kueue.ClusterQueue{cq1, cq2, cq3}
			}(),
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltesting.MakeLocalQueue("lq1", "sales").ClusterQueue("cq1").Obj(),
				*utiltesting.MakeLocalQueue("lq2", "sales").ClusterQueue("cq2").Obj(),
				*utiltesting.MakeLocalQueue("lq3", "sales").ClusterQueue("cq3").Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("wl1", "sales").Queue("lq1").Priority(-1).PodSets(
					*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).Request("r1", "16").Obj(),
				).Obj(),
				*utiltesting.MakeWorkload("wl2", "sales").Queue("lq2").Priority(-2).PodSets(
					*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).Request("r1", "16").Obj(),
				).Obj(),
			},
			wantScheduled: []workload.Reference{"sales/wl1"},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"sales/wl1": *utiltesting.MakeAdmission("cq1", kueue.DefaultPodSetName).
					Assignment("r1", "default", "16").AssignmentPodCount(1).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"cq2": {"sales/wl2"},
			},
		},
		"preemption while borrowing, workload waiting for preemption should not block a borrowing workload in another CQ": {
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltesting.MakeClusterQueue("cq_shared").
					Cohort("preemption-while-borrowing").
					ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "4", "0").Obj()).
					Obj(),
				*utiltesting.MakeClusterQueue("cq_a").
					Cohort("preemption-while-borrowing").
					Preemption(kueue.ClusterQueuePreemption{
						ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
						BorrowWithinCohort: &kueue.BorrowWithinCohort{
							Policy: kueue.BorrowWithinCohortPolicyLowerPriority,
						},
					}).
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("default").
							Resource(corev1.ResourceCPU, "0", "3").Obj(),
					).
					Obj(),
				*utiltesting.MakeClusterQueue("cq_b").
					Cohort("preemption-while-borrowing").
					Preemption(kueue.ClusterQueuePreemption{
						ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
						BorrowWithinCohort: &kueue.BorrowWithinCohort{
							Policy: kueue.BorrowWithinCohortPolicyLowerPriority,
						},
					}).
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("default").
							Resource(corev1.ResourceCPU, "0").Obj(),
					).
					Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "eng-alpha",
						Name:      "lq_a",
					},
					Spec: kueue.LocalQueueSpec{
						ClusterQueue: "cq_a",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "eng-beta",
						Name:      "lq_b",
					},
					Spec: kueue.LocalQueueSpec{
						ClusterQueue: "cq_b",
					},
				},
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a", "eng-alpha").
					Queue("lq_a").
					Creation(now.Add(time.Second)).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).
						Request(corev1.ResourceCPU, "3").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("b", "eng-beta").
					Queue("lq_b").
					Creation(now.Add(2 * time.Second)).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("admitted_a", "eng-alpha").
					Queue("lq_a").
					PodSets(
						*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).
							Request(corev1.ResourceCPU, "2").
							Obj(),
					).
					ReserveQuota(utiltesting.MakeAdmission("cq_a").
						PodSets(
							kueue.PodSetAssignment{
								Name: kueue.DefaultPodSetName,
								Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
									corev1.ResourceCPU: "default",
								},
								ResourceUsage: corev1.ResourceList{
									corev1.ResourceCPU: resource.MustParse("2"),
								},
								Count: ptr.To[int32](1),
							},
						).
						Obj()).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/admitted_a": {
					ClusterQueue: "cq_a",
					PodSetAssignments: []kueue.PodSetAssignment{
						{
							Name: kueue.DefaultPodSetName,
							Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
								corev1.ResourceCPU: "default",
							},
							ResourceUsage: corev1.ResourceList{
								corev1.ResourceCPU: resource.MustParse("2"),
							},
							Count: ptr.To[int32](1),
						},
					},
				},
				"eng-beta/b": {
					ClusterQueue: "cq_b",
					PodSetAssignments: []kueue.PodSetAssignment{
						{
							Name: kueue.DefaultPodSetName,
							Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
								corev1.ResourceCPU: "default",
							},
							ResourceUsage: corev1.ResourceList{
								corev1.ResourceCPU: resource.MustParse("1"),
							},
							Count: ptr.To[int32](1),
						},
					},
				},
			},
			wantScheduled: []workload.Reference{
				"eng-beta/b",
			},
			wantInadmissibleLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"cq_a": {"eng-alpha/a"},
			},
		},
		"with fair sharing: schedule workload with lowest share first": {
			enableFairSharing: true,
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltesting.MakeClusterQueue("eng-shared").
					Cohort("eng").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "10", "0").Obj(),
					).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("all_nominal", "eng-alpha").
					Queue("main").
					PodSets(*utiltesting.MakePodSet("one", 50).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuota(utiltesting.MakeAdmission("eng-alpha", "one").Assignment(corev1.ResourceCPU, "on-demand", "50").AssignmentPodCount(50).Obj()).
					Obj(),
				*utiltesting.MakeWorkload("borrowing", "eng-beta").
					Queue("main").
					// Use half of shared quota.
					PodSets(*utiltesting.MakePodSet("one", 55).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuota(utiltesting.MakeAdmission("eng-beta", "one").Assignment(corev1.ResourceCPU, "on-demand", "55").AssignmentPodCount(55).Obj()).
					Obj(),
				*utiltesting.MakeWorkload("older_new", "eng-beta").
					Queue("main").
					Creation(now.Add(-time.Minute)).
					PodSets(*utiltesting.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("new", "eng-alpha").
					Queue("main").
					Creation(now).
					PodSets(*utiltesting.MakePodSet("one", 5).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/all_nominal": *utiltesting.MakeAdmission("eng-alpha", "one").Assignment(corev1.ResourceCPU, "on-demand", "50").AssignmentPodCount(50).Obj(),
				"eng-beta/borrowing":    *utiltesting.MakeAdmission("eng-beta", "one").Assignment(corev1.ResourceCPU, "on-demand", "55").AssignmentPodCount(55).Obj(),
				"eng-alpha/new":         *utiltesting.MakeAdmission("eng-alpha", "one").Assignment(corev1.ResourceCPU, "on-demand", "5").AssignmentPodCount(5).Obj(),
			},
			wantScheduled: []workload.Reference{"eng-alpha/new"},
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
				*utiltesting.MakeCohort("A").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "200").Obj(),
					).Obj(),
				*utiltesting.MakeCohort("B").Parent("A").Obj(),
				*utiltesting.MakeCohort("C").Parent("A").Obj(),
			},
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltesting.MakeClusterQueue("d").
					Cohort("B").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "0").Obj(),
					).Obj(),
				*utiltesting.MakeClusterQueue("e").
					Cohort("B").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "0").Obj(),
					).Obj(),
				*utiltesting.MakeClusterQueue("f").
					Cohort("C").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "0").Obj(),
					).Obj(),
				*utiltesting.MakeClusterQueue("g").
					Cohort("C").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "0").Obj(),
					).Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltesting.MakeLocalQueue("lq-d", "eng-alpha").ClusterQueue("d").Obj(),
				*utiltesting.MakeLocalQueue("lq-e", "eng-alpha").ClusterQueue("e").Obj(),
				*utiltesting.MakeLocalQueue("lq-f", "eng-alpha").ClusterQueue("f").Obj(),
				*utiltesting.MakeLocalQueue("lq-g", "eng-alpha").ClusterQueue("g").Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("d0", "eng-alpha").
					Queue("lq-d").
					PodSets(*utiltesting.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "10").
						Obj()).
					ReserveQuota(utiltesting.MakeAdmission("d", "one").Assignment(corev1.ResourceCPU, "on-demand", "10").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("e0", "eng-alpha").
					Queue("lq-e").
					PodSets(*utiltesting.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "20").
						Obj()).
					ReserveQuota(utiltesting.MakeAdmission("e", "one").Assignment(corev1.ResourceCPU, "on-demand", "20").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("g0", "eng-alpha").
					Queue("lq-g").
					PodSets(*utiltesting.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "100").
						Obj()).
					ReserveQuota(utiltesting.MakeAdmission("g", "one").Assignment(corev1.ResourceCPU, "on-demand", "100").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("d1", "eng-alpha").
					Queue("lq-d").
					PodSets(*utiltesting.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "70").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("e1", "eng-alpha").
					Queue("lq-e").
					PodSets(*utiltesting.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "61").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("f1", "eng-alpha").
					Queue("lq-f").
					PodSets(*utiltesting.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("g1", "eng-alpha").
					Queue("lq-g").
					PodSets(*utiltesting.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/d0": *utiltesting.MakeAdmission("d", "one").Assignment(corev1.ResourceCPU, "on-demand", "10").Obj(),
				"eng-alpha/e0": *utiltesting.MakeAdmission("e", "one").Assignment(corev1.ResourceCPU, "on-demand", "20").Obj(),
				"eng-alpha/g0": *utiltesting.MakeAdmission("g", "one").Assignment(corev1.ResourceCPU, "on-demand", "100").Obj(),
				"eng-alpha/d1": *utiltesting.MakeAdmission("d", "one").Assignment(corev1.ResourceCPU, "on-demand", "70").Obj(),
			},
			wantScheduled: []workload.Reference{"eng-alpha/d1"},
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
				*utiltesting.MakeCohort("A").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "100").Obj(),
					).Obj(),
			},
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltesting.MakeClusterQueue("b").
					Cohort("A").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "0").Obj(),
					).Obj(),
				*utiltesting.MakeClusterQueue("c").
					Cohort("A").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "0").Obj(),
					).Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltesting.MakeLocalQueue("lq-b", "eng-alpha").ClusterQueue("b").Obj(),
				*utiltesting.MakeLocalQueue("lq-c", "eng-alpha").ClusterQueue("c").Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("b0", "eng-alpha").
					Queue("lq-b").
					PodSets(*utiltesting.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "10").
						Obj()).
					ReserveQuota(utiltesting.MakeAdmission("b", "one").Assignment(corev1.ResourceCPU, "on-demand", "10").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("b1", "eng-alpha").
					Queue("lq-b").
					PodSets(*utiltesting.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "50").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("c1", "eng-alpha").
					Queue("lq-c").
					PodSets(*utiltesting.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "75").
						Obj()).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/b0": *utiltesting.MakeAdmission("b", "one").Assignment(corev1.ResourceCPU, "on-demand", "10").Obj(),
				"eng-alpha/b1": *utiltesting.MakeAdmission("b", "one").Assignment(corev1.ResourceCPU, "on-demand", "50").Obj(),
			},
			wantScheduled: []workload.Reference{"eng-alpha/b1"},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"c": {"eng-alpha/c1"},
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
				*utiltesting.MakeCohort("A").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "10").Obj(),
					).Obj(),
				*utiltesting.MakeCohort("B").Obj(),
			},
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltesting.MakeClusterQueue("a").
					Cohort("A").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "0").Obj(),
					).
					Obj(),
				*utiltesting.MakeClusterQueue("b").
					Cohort("B").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "10").Obj(),
					).
					Obj(),
				*utiltesting.MakeClusterQueue("c").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "10").Obj(),
					).
					Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltesting.MakeLocalQueue("lq-a", "eng-alpha").ClusterQueue("a").Obj(),
				*utiltesting.MakeLocalQueue("lq-b", "eng-alpha").ClusterQueue("b").Obj(),
				*utiltesting.MakeLocalQueue("lq-c", "eng-alpha").ClusterQueue("c").Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a1", "eng-alpha").
					Queue("lq-a").
					PodSets(*utiltesting.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "10").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("b1", "eng-alpha").
					Queue("lq-b").
					PodSets(*utiltesting.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "10").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("c1", "eng-alpha").
					Queue("lq-c").
					PodSets(*utiltesting.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "10").
						Obj()).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/a1": *utiltesting.MakeAdmission("a", "one").Assignment(corev1.ResourceCPU, "on-demand", "10").Obj(),
				"eng-alpha/b1": *utiltesting.MakeAdmission("b", "one").Assignment(corev1.ResourceCPU, "on-demand", "10").Obj(),
				"eng-alpha/c1": *utiltesting.MakeAdmission("c", "one").Assignment(corev1.ResourceCPU, "on-demand", "10").Obj(),
			},
			wantScheduled: []workload.Reference{"eng-alpha/a1", "eng-alpha/b1", "eng-alpha/c1"},
			wantLeft:      nil,
		},
		"fair sharing schedule highest priority first": {
			enableFairSharing: true,
			cohorts: []kueue.Cohort{
				*utiltesting.MakeCohort("A").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "10").Obj(),
					).Obj(),
				*utiltesting.MakeCohort("B").Obj(),
			},
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltesting.MakeClusterQueue("b").
					Cohort("A").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "0").Obj(),
					).Obj(),
				*utiltesting.MakeClusterQueue("c").
					Cohort("A").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "0").Obj(),
					).Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltesting.MakeLocalQueue("lq-b", "eng-alpha").ClusterQueue("b").Obj(),
				*utiltesting.MakeLocalQueue("lq-c", "eng-alpha").ClusterQueue("c").Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("b1", "eng-alpha").
					Queue("lq-b").
					Priority(99).
					PodSets(*utiltesting.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "10").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("c1", "eng-alpha").
					Queue("lq-c").
					Priority(101).
					PodSets(*utiltesting.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "10").
						Obj()).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/c1": *utiltesting.MakeAdmission("c", "one").Assignment(corev1.ResourceCPU, "on-demand", "10").Obj(),
			},
			wantScheduled: []workload.Reference{"eng-alpha/c1"},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"b": {"eng-alpha/b1"},
			},
		},
		"fair sharing schedule earliest timestamp first": {
			enableFairSharing: true,
			cohorts: []kueue.Cohort{
				*utiltesting.MakeCohort("A").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "10").Obj(),
					).Obj(),
				*utiltesting.MakeCohort("B").Obj(),
			},
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltesting.MakeClusterQueue("b").
					Cohort("A").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "0").Obj(),
					).Obj(),
				*utiltesting.MakeClusterQueue("c").
					Cohort("A").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "0").Obj(),
					).Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltesting.MakeLocalQueue("lq-b", "eng-alpha").ClusterQueue("b").Obj(),
				*utiltesting.MakeLocalQueue("lq-c", "eng-alpha").ClusterQueue("c").Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("b1", "eng-alpha").
					Creation(now.Add(time.Second)).
					Queue("lq-b").
					Priority(101).
					PodSets(*utiltesting.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "10").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("c1", "eng-alpha").
					Creation(now).
					Queue("lq-c").
					Priority(101).
					PodSets(*utiltesting.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "10").
						Obj()).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/c1": *utiltesting.MakeAdmission("c", "one").Assignment(corev1.ResourceCPU, "on-demand", "10").Obj(),
			},
			wantScheduled: []workload.Reference{"eng-alpha/c1"},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"b": {"eng-alpha/b1"},
			},
		},
		"minimal preemptions when target queue is exhausted": {
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltesting.MakeClusterQueue("other-alpha").
					Cohort("other").
					Preemption(kueue.ClusterQueuePreemption{
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
					}).
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "2").Obj(),
					).
					Obj(),
				*utiltesting.MakeClusterQueue("other-beta").
					Cohort("other").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "2").Obj(),
					).
					Obj(),
				*utiltesting.MakeClusterQueue("other-gamma").
					Cohort("other").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "2").Obj(),
					).
					Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltesting.MakeLocalQueue("other", "eng-alpha").ClusterQueue("other-alpha").Obj(),
				*utiltesting.MakeLocalQueue("other", "eng-beta").ClusterQueue("other-beta").Obj(),
				*utiltesting.MakeLocalQueue("other", "eng-gamma").ClusterQueue("other-gamma").Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a1", "eng-alpha").
					Priority(-2).
					Queue("other").
					Request(corev1.ResourceCPU, "1").
					ReserveQuota(utiltesting.MakeAdmission("other-alpha").Assignment(corev1.ResourceCPU, "on-demand", "1").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("a2", "eng-alpha").
					Priority(-2).
					Queue("other").
					Request(corev1.ResourceCPU, "1").
					ReserveQuota(utiltesting.MakeAdmission("other-alpha").Assignment(corev1.ResourceCPU, "on-demand", "1").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("a3", "eng-alpha").
					Priority(-1).
					Queue("other").
					Request(corev1.ResourceCPU, "1").
					ReserveQuota(utiltesting.MakeAdmission("other-alpha").Assignment(corev1.ResourceCPU, "on-demand", "1").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("b1", "eng-beta").
					Priority(0).
					Queue("other").
					Request(corev1.ResourceCPU, "1").
					ReserveQuota(utiltesting.MakeAdmission("other-beta").Assignment(corev1.ResourceCPU, "on-demand", "1").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("b2", "eng-beta").
					Priority(0).
					Queue("other").
					Request(corev1.ResourceCPU, "1").
					ReserveQuota(utiltesting.MakeAdmission("other-beta").Assignment(corev1.ResourceCPU, "on-demand", "1").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("b3", "eng-beta").
					Priority(0).
					Queue("other").
					Request(corev1.ResourceCPU, "1").
					ReserveQuota(utiltesting.MakeAdmission("other-beta").Assignment(corev1.ResourceCPU, "on-demand", "1").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("incoming", "eng-alpha").
					Priority(0).
					Queue("other").
					Request(corev1.ResourceCPU, "2").
					Obj(),
			},
			wantPreempted: sets.New[workload.Reference]("eng-alpha/a1", "eng-alpha/a2"),
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"other-alpha": {"eng-alpha/incoming"},
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/a1": *utiltesting.MakeAdmission("other-alpha").Assignment(corev1.ResourceCPU, "on-demand", "1").Obj(),
				"eng-alpha/a2": *utiltesting.MakeAdmission("other-alpha").Assignment(corev1.ResourceCPU, "on-demand", "1").Obj(),
				"eng-alpha/a3": *utiltesting.MakeAdmission("other-alpha").Assignment(corev1.ResourceCPU, "on-demand", "1").Obj(),
				"eng-beta/b1":  *utiltesting.MakeAdmission("other-beta").Assignment(corev1.ResourceCPU, "on-demand", "1").Obj(),
				"eng-beta/b2":  *utiltesting.MakeAdmission("other-beta").Assignment(corev1.ResourceCPU, "on-demand", "1").Obj(),
				"eng-beta/b3":  *utiltesting.MakeAdmission("other-beta").Assignment(corev1.ResourceCPU, "on-demand", "1").Obj(),
			},
		},
		"A workload is only eligible to do preemptions if it fits fully within nominal quota": {
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltesting.MakeClusterQueue("other-alpha").
					Cohort("other").
					Preemption(kueue.ClusterQueuePreemption{
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
					}).
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "2").Obj(),
					).
					Obj(),
				*utiltesting.MakeClusterQueue("other-beta").
					Cohort("other").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "2").Obj(),
					).
					Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltesting.MakeLocalQueue("other", "eng-alpha").ClusterQueue("other-alpha").Obj(),
				*utiltesting.MakeLocalQueue("other", "eng-beta").ClusterQueue("other-beta").Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a1", "eng-alpha").
					Priority(-1).
					Queue("other").
					Request(corev1.ResourceCPU, "1").
					ReserveQuota(utiltesting.MakeAdmission("other-alpha").Assignment(corev1.ResourceCPU, "on-demand", "1").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("b1", "eng-beta").
					Priority(-1).
					Queue("other").
					Request(corev1.ResourceCPU, "1").
					ReserveQuota(utiltesting.MakeAdmission("other-beta").Assignment(corev1.ResourceCPU, "on-demand", "1").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("incoming", "eng-alpha").
					Priority(1).
					Queue("other").
					Request(corev1.ResourceCPU, "3").
					Obj(),
			},
			wantInadmissibleLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"other-alpha": {"eng-alpha/incoming"},
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/a1": *utiltesting.MakeAdmission("other-alpha").Assignment(corev1.ResourceCPU, "on-demand", "1").Obj(),
				"eng-beta/b1":  *utiltesting.MakeAdmission("other-beta").Assignment(corev1.ResourceCPU, "on-demand", "1").Obj(),
			},
		},
		"with fair sharing: preempt workload from CQ with the highest share": {
			enableFairSharing: true,
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltesting.MakeClusterQueue("eng-gamma").
					Cohort("eng").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "50", "0").Obj(),
					).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("all_spot", "eng-alpha").
					Request(corev1.ResourceCPU, "100").
					SimpleReserveQuota("eng-alpha", "spot", now).Obj(),
				*utiltesting.MakeWorkload("alpha1", "eng-alpha").UID("alpha1").
					Request(corev1.ResourceCPU, "20").
					SimpleReserveQuota("eng-alpha", "on-demand", now).Obj(),
				*utiltesting.MakeWorkload("alpha2", "eng-alpha").UID("alpha2").
					Request(corev1.ResourceCPU, "20").
					SimpleReserveQuota("eng-alpha", "on-demand", now).Obj(),
				*utiltesting.MakeWorkload("alpha3", "eng-alpha").UID("alpha3").
					Request(corev1.ResourceCPU, "20").
					SimpleReserveQuota("eng-alpha", "on-demand", now).Obj(),
				*utiltesting.MakeWorkload("alpha4", "eng-alpha").UID("alpha4").
					Request(corev1.ResourceCPU, "20").
					SimpleReserveQuota("eng-alpha", "on-demand", now).Obj(),
				*utiltesting.MakeWorkload("gamma1", "eng-gamma").UID("gamma1").
					Request(corev1.ResourceCPU, "10").
					SimpleReserveQuota("eng-gamma", "on-demand", now).Obj(),
				*utiltesting.MakeWorkload("gamma2", "eng-gamma").UID("gamma2").
					Request(corev1.ResourceCPU, "20").
					SimpleReserveQuota("eng-gamma", "on-demand", now).Obj(),
				*utiltesting.MakeWorkload("gamma3", "eng-gamma").UID("gamma3").
					Request(corev1.ResourceCPU, "20").
					SimpleReserveQuota("eng-gamma", "on-demand", now).Obj(),
				*utiltesting.MakeWorkload("gamma4", "eng-gamma").UID("gamma4").
					Request(corev1.ResourceCPU, "20").
					SimpleReserveQuota("eng-gamma", "on-demand", now).Obj(),
				*utiltesting.MakeWorkload("preemptor", "eng-beta").
					Queue("main").
					Request(corev1.ResourceCPU, "30").Obj(),
			},
			wantPreempted: sets.New[workload.Reference]("eng-alpha/alpha1", "eng-gamma/gamma1"),
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				// Preemptor is not admitted in this cycle.
				"eng-beta": {"eng-beta/preemptor"},
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/all_spot": *utiltesting.MakeAdmission("eng-alpha").Assignment(corev1.ResourceCPU, "spot", "100").Obj(),
				"eng-alpha/alpha1":   *utiltesting.MakeAdmission("eng-alpha").Assignment(corev1.ResourceCPU, "on-demand", "20").Obj(),
				"eng-alpha/alpha2":   *utiltesting.MakeAdmission("eng-alpha").Assignment(corev1.ResourceCPU, "on-demand", "20").Obj(),
				"eng-alpha/alpha3":   *utiltesting.MakeAdmission("eng-alpha").Assignment(corev1.ResourceCPU, "on-demand", "20").Obj(),
				"eng-alpha/alpha4":   *utiltesting.MakeAdmission("eng-alpha").Assignment(corev1.ResourceCPU, "on-demand", "20").Obj(),
				"eng-gamma/gamma1":   *utiltesting.MakeAdmission("eng-gamma").Assignment(corev1.ResourceCPU, "on-demand", "10").Obj(),
				"eng-gamma/gamma2":   *utiltesting.MakeAdmission("eng-gamma").Assignment(corev1.ResourceCPU, "on-demand", "20").Obj(),
				"eng-gamma/gamma3":   *utiltesting.MakeAdmission("eng-gamma").Assignment(corev1.ResourceCPU, "on-demand", "20").Obj(),
				"eng-gamma/gamma4":   *utiltesting.MakeAdmission("eng-gamma").Assignment(corev1.ResourceCPU, "on-demand", "20").Obj(),
			},
		},
		"multiple preemptions without borrowing": {
			// While requiring the same shared FlavorResource (Default, cpu),
			// multiple workloads are able to issue preemptions on workloads within
			// their own CQs in a single scheduling cycle.
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltesting.MakeClusterQueue("other-alpha").
					Cohort("other").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
					}).
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("default").
							Resource(corev1.ResourceCPU, "2").Obj(),
					).
					Obj(),
				*utiltesting.MakeClusterQueue("other-beta").
					Cohort("other").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
					}).
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("default").
							Resource(corev1.ResourceCPU, "2").Obj(),
					).
					Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltesting.MakeLocalQueue("other", "eng-alpha").ClusterQueue("other-alpha").Obj(),
				*utiltesting.MakeLocalQueue("other", "eng-beta").ClusterQueue("other-beta").Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a1", "eng-alpha").
					Priority(0).
					Queue("other").
					Request(corev1.ResourceCPU, "2").
					ReserveQuota(utiltesting.MakeAdmission("other-alpha").Assignment(corev1.ResourceCPU, "default", "2").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("b1", "eng-beta").
					Priority(0).
					Queue("other").
					Request(corev1.ResourceCPU, "2").
					ReserveQuota(utiltesting.MakeAdmission("other-beta").Assignment(corev1.ResourceCPU, "default", "2").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("preemptor", "eng-alpha").
					Priority(100).
					Queue("other").
					Request(corev1.ResourceCPU, "2").
					Obj(),
				*utiltesting.MakeWorkload("preemptor", "eng-beta").
					Priority(100).
					Queue("other").
					Request(corev1.ResourceCPU, "2").
					Obj(),
			},
			wantPreempted: sets.New[workload.Reference]("eng-alpha/a1", "eng-beta/b1"),
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"other-alpha": {"eng-alpha/preemptor"},
				"other-beta":  {"eng-beta/preemptor"},
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/a1": *utiltesting.MakeAdmission("other-alpha").Assignment(corev1.ResourceCPU, "default", "2").Obj(),
				"eng-beta/b1":  *utiltesting.MakeAdmission("other-beta").Assignment(corev1.ResourceCPU, "default", "2").Obj(),
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
				*utiltesting.MakeClusterQueue("other-alpha").
					Cohort("other").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
					}).
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("default").
							Resource(corev1.ResourceCPU, "1").Obj(),
					).
					Obj(),
				*utiltesting.MakeClusterQueue("other-beta").
					Cohort("other").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
					}).
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("default").
							Resource(corev1.ResourceCPU, "2").Obj(),
					).
					Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltesting.MakeLocalQueue("other", "eng-alpha").ClusterQueue("other-alpha").Obj(),
				*utiltesting.MakeLocalQueue("other", "eng-beta").ClusterQueue("other-beta").Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("b1", "eng-beta").
					Priority(0).
					Queue("other").
					Request(corev1.ResourceCPU, "2").
					ReserveQuota(utiltesting.MakeAdmission("other-beta").Assignment(corev1.ResourceCPU, "default", "2").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("fit", "eng-alpha").
					Priority(100).
					Queue("other").
					Request(corev1.ResourceCPU, "1").
					Obj(),
				*utiltesting.MakeWorkload("preemptor", "eng-beta").
					Priority(99).
					Queue("other").
					Request(corev1.ResourceCPU, "2").
					Obj(),
			},
			wantPreempted: sets.New[workload.Reference]("eng-beta/b1"),
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"other-beta": {"eng-beta/preemptor"},
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/fit": *utiltesting.MakeAdmission("other-alpha").Assignment(corev1.ResourceCPU, "default", "1").Obj(),
				"eng-beta/b1":   *utiltesting.MakeAdmission("other-beta").Assignment(corev1.ResourceCPU, "default", "2").Obj(),
			},
			wantScheduled: []workload.Reference{"eng-alpha/fit"},
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
				*utiltesting.MakeClusterQueue("other-alpha").
					Cohort("other").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
						BorrowWithinCohort: &kueue.BorrowWithinCohort{
							Policy: kueue.BorrowWithinCohortPolicyLowerPriority,
						},
					}).
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("default").
							Resource(corev1.ResourceCPU, "2").Obj(),
					).
					Obj(),
				*utiltesting.MakeClusterQueue("other-beta").
					Cohort("other").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
						BorrowWithinCohort: &kueue.BorrowWithinCohort{
							Policy: kueue.BorrowWithinCohortPolicyLowerPriority,
						},
					}).
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("default").
							Resource(corev1.ResourceCPU, "2").Obj(),
					).
					Obj(),
				*utiltesting.MakeClusterQueue("resource-bank").
					Cohort("other").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("default").
							Resource(corev1.ResourceCPU, "1").Obj(),
					).
					Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltesting.MakeLocalQueue("other", "eng-alpha").ClusterQueue("other-alpha").Obj(),
				*utiltesting.MakeLocalQueue("other", "eng-beta").ClusterQueue("other-beta").Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a1", "eng-alpha").
					Priority(0).
					Queue("other").
					Request(corev1.ResourceCPU, "2").
					ReserveQuota(utiltesting.MakeAdmission("other-alpha").Assignment(corev1.ResourceCPU, "default", "2").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("b1", "eng-beta").
					Priority(0).
					Queue("other").
					Request(corev1.ResourceCPU, "2").
					ReserveQuota(utiltesting.MakeAdmission("other-beta").Assignment(corev1.ResourceCPU, "default", "2").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("preemptor", "eng-alpha").
					Priority(100).
					Queue("other").
					Request(corev1.ResourceCPU, "3").
					Obj(),
				*utiltesting.MakeWorkload("pretending-preemptor", "eng-beta").
					Priority(99).
					Queue("other").
					Request(corev1.ResourceCPU, "3").
					Obj(),
			},
			wantPreempted: sets.New[workload.Reference]("eng-alpha/a1"),
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"other-alpha": {"eng-alpha/preemptor"},
				"other-beta":  {"eng-beta/pretending-preemptor"},
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/a1": *utiltesting.MakeAdmission("other-alpha").Assignment(corev1.ResourceCPU, "default", "2").Obj(),
				"eng-beta/b1":  *utiltesting.MakeAdmission("other-beta").Assignment(corev1.ResourceCPU, "default", "2").Obj(),
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
				*utiltesting.MakeClusterQueue("other-alpha").
					Cohort("other").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
					}).
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("default").
							Resource(corev1.ResourceCPU, "2").
							Obj(),
					).
					Obj(),
				*utiltesting.MakeClusterQueue("other-beta").
					Cohort("other").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
					}).
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("default").
							Resource(corev1.ResourceCPU, "2").
							Obj(),
					).
					Obj(),
				*utiltesting.MakeClusterQueue("other-gamma").
					Cohort("other").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
					}).
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("default").
							Resource(corev1.ResourceCPU, "2").
							Obj(),
					).
					Obj(),
				*utiltesting.MakeClusterQueue("resource-bank").
					Cohort("other").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("default").
							Resource(corev1.ResourceCPU, "3").Obj(),
					).
					Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltesting.MakeLocalQueue("other", "eng-alpha").ClusterQueue("other-alpha").Obj(),
				*utiltesting.MakeLocalQueue("other", "eng-beta").ClusterQueue("other-beta").Obj(),
				*utiltesting.MakeLocalQueue("other", "eng-gamma").ClusterQueue("other-gamma").Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a1", "eng-alpha").
					Priority(0).
					Queue("other").
					Request(corev1.ResourceCPU, "3").
					ReserveQuota(utiltesting.MakeAdmission("other-alpha").
						Assignment(corev1.ResourceCPU, "default", "3").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("b1", "eng-beta").
					Priority(0).
					Queue("other").
					Request(corev1.ResourceCPU, "3").
					ReserveQuota(utiltesting.MakeAdmission("other-beta").
						Assignment(corev1.ResourceCPU, "default", "3").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("c1", "eng-gamma").
					Priority(0).
					Queue("other").
					Request(corev1.ResourceCPU, "3").
					ReserveQuota(utiltesting.MakeAdmission("other-gamma").
						Assignment(corev1.ResourceCPU, "default", "3").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("preemptor", "eng-alpha").
					Priority(100).
					Queue("other").
					Request(corev1.ResourceCPU, "3").
					Obj(),
				*utiltesting.MakeWorkload("preemptor", "eng-beta").
					Priority(100).
					Queue("other").
					Request(corev1.ResourceCPU, "3").
					Obj(),
				*utiltesting.MakeWorkload("preemptor", "eng-gamma").
					Priority(100).
					Queue("other").
					Request(corev1.ResourceCPU, "3").
					Obj(),
			},
			wantPreempted: sets.New[workload.Reference]("eng-alpha/a1", "eng-beta/b1", "eng-gamma/c1"),
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"other-alpha": {"eng-alpha/preemptor"},
				"other-beta":  {"eng-beta/preemptor"},
				"other-gamma": {"eng-gamma/preemptor"},
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/a1": *utiltesting.MakeAdmission("other-alpha").
					Assignment(corev1.ResourceCPU, "default", "3").Obj(),
				"eng-beta/b1": *utiltesting.MakeAdmission("other-beta").
					Assignment(corev1.ResourceCPU, "default", "3").Obj(),
				"eng-gamma/c1": *utiltesting.MakeAdmission("other-gamma").
					Assignment(corev1.ResourceCPU, "default", "3").Obj(),
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
				*utiltesting.MakeClusterQueue("other-alpha").
					Cohort("other").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
					}).
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("default").
							Resource(corev1.ResourceCPU, "0").
							Resource("alpha-resource", "1").Obj(),
					).
					Obj(),
				*utiltesting.MakeClusterQueue("other-beta").
					Cohort("other").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
					}).
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("default").
							Resource(corev1.ResourceCPU, "0").
							Resource("beta-resource", "1").Obj(),
					).
					Obj(),
				*utiltesting.MakeClusterQueue("other-gamma").
					Cohort("other").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
					}).
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("default").
							Resource(corev1.ResourceCPU, "0").
							Resource("gamma-resource", "1").Obj(),
					).
					Obj(),
				*utiltesting.MakeClusterQueue("resource-bank").
					Cohort("other").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("default").
							Resource(corev1.ResourceCPU, "9").Obj(),
					).
					Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltesting.MakeLocalQueue("other", "eng-alpha").ClusterQueue("other-alpha").Obj(),
				*utiltesting.MakeLocalQueue("other", "eng-beta").ClusterQueue("other-beta").Obj(),
				*utiltesting.MakeLocalQueue("other", "eng-gamma").ClusterQueue("other-gamma").Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a1", "eng-alpha").
					Priority(0).
					Queue("other").
					Request("alpha-resource", "1").
					ReserveQuota(utiltesting.MakeAdmission("other-alpha").
						Assignment("alpha-resource", "default", "1").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("b1", "eng-beta").
					Priority(0).
					Queue("other").
					Request("beta-resource", "1").
					ReserveQuota(utiltesting.MakeAdmission("other-beta").
						Assignment("beta-resource", "default", "1").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("c1", "eng-gamma").
					Priority(0).
					Queue("other").
					Request(corev1.ResourceCPU, "9").
					Request("gamma-resource", "1").
					ReserveQuota(utiltesting.MakeAdmission("other-gamma").
						Assignment(corev1.ResourceCPU, "default", "9").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("preemptor", "eng-alpha").
					Priority(100).
					Queue("other").
					Request(corev1.ResourceCPU, "3").
					Request("alpha-resource", "1").
					Obj(),
				*utiltesting.MakeWorkload("pretending-preemptor", "eng-beta").
					Priority(99).
					Queue("other").
					Request(corev1.ResourceCPU, "3").
					Request("beta-resource", "1").
					Obj(),
			},
			wantPreempted: sets.New[workload.Reference]("eng-alpha/a1", "eng-gamma/c1"),
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"other-alpha": {"eng-alpha/preemptor"},
				"other-beta":  {"eng-beta/pretending-preemptor"},
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/a1": *utiltesting.MakeAdmission("other-alpha").
					Assignment("alpha-resource", "default", "1").Obj(),
				"eng-beta/b1": *utiltesting.MakeAdmission("other-beta").
					Assignment("beta-resource", "default", "1").Obj(),
				"eng-gamma/c1": *utiltesting.MakeAdmission("other-gamma").
					Assignment(corev1.ResourceCPU, "default", "9").Obj(),
			},
			wantSkippedPreemptions: map[string]int{
				"other-alpha": 0,
				"other-beta":  1,
				"other-gamma": 0,
			},
		},
		"not enough resources": {
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("new", "sales").
					Queue("main").
					PodSets(*utiltesting.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "100").
						Obj()).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"sales": {"sales/new"},
			},
		},
		"container does not satisfy limitRange constraints": {
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("new", "sales").
					Queue("main").
					PodSets(*utiltesting.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "500m").
						Obj()).
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
				{
					Key:       types.NamespacedName{Namespace: "sales", Name: "new"},
					Reason:    "Pending",
					EventType: corev1.EventTypeWarning,
					Message: fmt.Sprintf("%s: %s",
						errLimitRangeConstraintsUnsatisfiedResources,
						field.Invalid(
							workload.PodSetsPath.Index(0).Child("template").Child("spec").Child("containers").Index(0),
							[]corev1.ResourceName{corev1.ResourceCPU},
							limitrange.RequestsMustNotBeAboveLimitRangeMaxMessage,
						).Error(),
					),
				},
			},
		},
		"container resource requests exceed limits": {
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("new", "sales").
					Queue("main").
					PodSets(*utiltesting.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "200m").
						Limit(corev1.ResourceCPU, "100m").
						Obj()).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"sales": {"sales/new"},
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "sales", Name: "new"},
					Reason:    "Pending",
					EventType: corev1.EventTypeWarning,
					Message: fmt.Sprintf("%s: %s",
						errInvalidWLResources,
						field.Invalid(
							workload.PodSetsPath.Index(0).Child("template").Child("spec").Child("containers").Index(0),
							[]corev1.ResourceName{corev1.ResourceCPU}, workload.RequestsMustNotExceedLimitMessage,
						).Error(),
					),
				},
			},
		},
		"not enough resources with fair sharing enabled": {
			enableFairSharing: true,
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("new", "sales").
					Queue("main").
					PodSets(*utiltesting.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "100").
						Obj()).
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
				*utiltesting.MakeClusterQueue("other-alpha").
					Cohort("other").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
					}).
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("on-demand").Resource("gpu", "10").Obj(),
						*utiltesting.MakeFlavorQuotas("spot").Resource("gpu", "10").Obj(),
					).
					Obj(),
				*utiltesting.MakeClusterQueue("other-beta").
					Cohort("other").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
					}).
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("on-demand").Resource("gpu", "0").Obj(),
						*utiltesting.MakeFlavorQuotas("spot").Resource("gpu", "0").Obj(),
					).
					Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltesting.MakeLocalQueue("other", "eng-alpha").ClusterQueue("other-alpha").Obj(),
				*utiltesting.MakeLocalQueue("other", "eng-beta").ClusterQueue("other-beta").Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a1", "eng-alpha").
					Priority(50).
					Queue("other").
					Request("gpu", "5").
					SimpleReserveQuota("other-alpha", "on-demand", now).
					Obj(),
				*utiltesting.MakeWorkload("b1", "eng-beta").
					Priority(50).
					Queue("other").
					Request("gpu", "5").
					SimpleReserveQuota("other-beta", "spot", now).
					Obj(),
				*utiltesting.MakeWorkload("preemptor", "eng-alpha").
					Priority(100).
					Queue("other").
					Request("gpu", "6").
					Obj(),
			},
			wantPreempted: sets.New[workload.Reference]("eng-beta/b1"),
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"other-alpha": {"eng-alpha/preemptor"},
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/a1": *utiltesting.MakeAdmission("other-alpha").Assignment("gpu", "on-demand", "5").Obj(),
				"eng-beta/b1":  *utiltesting.MakeAdmission("other-beta").Assignment("gpu", "spot", "5").Obj(),
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
				*utiltesting.MakeClusterQueue("other-alpha").
					Cohort("other").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
					}).
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("on-demand").Resource("gpu", "7").Obj(),
						*utiltesting.MakeFlavorQuotas("spot").Resource("gpu", "7").Obj(),
					).
					Obj(),
				*utiltesting.MakeClusterQueue("other-beta").
					Cohort("other").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("on-demand").Resource("gpu", "3").Obj(),
						*utiltesting.MakeFlavorQuotas("spot").Resource("gpu", "3").Obj(),
					).
					Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltesting.MakeLocalQueue("other", "eng-alpha").ClusterQueue("other-alpha").Obj(),
				*utiltesting.MakeLocalQueue("other", "eng-beta").ClusterQueue("other-beta").Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a1", "eng-alpha").
					Priority(50).
					Queue("other").
					Request("gpu", "5").
					SimpleReserveQuota("other-alpha", "on-demand", now).
					Obj(),
				*utiltesting.MakeWorkload("b1", "eng-beta").
					Priority(50).
					Queue("other").
					Request("gpu", "5").
					SimpleReserveQuota("other-beta", "spot", now).
					Obj(),
				*utiltesting.MakeWorkload("preemptor", "eng-alpha").
					Priority(100).
					Queue("other").
					Request("gpu", "8").
					Obj(),
			},
			wantPreempted: sets.New[workload.Reference]("eng-beta/b1"),
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"other-alpha": {"eng-alpha/preemptor"},
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/a1": *utiltesting.MakeAdmission("other-alpha").Assignment("gpu", "on-demand", "5").Obj(),
				"eng-beta/b1":  *utiltesting.MakeAdmission("other-beta").Assignment("gpu", "spot", "5").Obj(),
			},
		},
		"prefer first preemption flavor when second flavor requires both reclaim and cq priority preemption": {
			// Flavor 1, on-demand, requires preemption of workload in CQ.
			// Flavor 2, spot, requires preemption of workload in Cohort and CQ
			// Since Flavor 2 doesn't improve the assignment, we prefer Flavor 1.
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltesting.MakeClusterQueue("other-alpha").
					Cohort("other").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
					}).
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("on-demand").Resource("gpu", "10").Obj(),
						*utiltesting.MakeFlavorQuotas("spot").Resource("gpu", "10").Obj(),
					).
					Obj(),
				*utiltesting.MakeClusterQueue("other-beta").
					Cohort("other").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("on-demand").Resource("gpu", "0").Obj(),
						*utiltesting.MakeFlavorQuotas("spot").Resource("gpu", "0").Obj(),
					).
					Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltesting.MakeLocalQueue("other", "eng-alpha").ClusterQueue("other-alpha").Obj(),
				*utiltesting.MakeLocalQueue("other", "eng-beta").ClusterQueue("other-beta").Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a1", "eng-alpha").
					Priority(50).
					Queue("other").
					Request("gpu", "5").
					SimpleReserveQuota("other-alpha", "on-demand", now).
					Obj(),
				*utiltesting.MakeWorkload("a2", "eng-alpha").
					Priority(50).
					Queue("other").
					Request("gpu", "5").
					SimpleReserveQuota("other-alpha", "spot", now).
					Obj(),
				*utiltesting.MakeWorkload("b1", "eng-beta").
					Priority(50).
					Queue("other").
					Request("gpu", "5").
					SimpleReserveQuota("other-beta", "spot", now).
					Obj(),
				*utiltesting.MakeWorkload("preemptor", "eng-alpha").
					Priority(100).
					Queue("other").
					Request("gpu", "6").
					Obj(),
			},
			wantPreempted: sets.New[workload.Reference]("eng-alpha/a1"),
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"other-alpha": {"eng-alpha/preemptor"},
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/a1": *utiltesting.MakeAdmission("other-alpha").Assignment("gpu", "on-demand", "5").Obj(),
				"eng-alpha/a2": *utiltesting.MakeAdmission("other-alpha").Assignment("gpu", "spot", "5").Obj(),
				"eng-beta/b1":  *utiltesting.MakeAdmission("other-beta").Assignment("gpu", "spot", "5").Obj(),
			},
		},
		"prefer first preemption flavor when second flavor also requires cq preemption": {
			// Flavor 1, on-demand, requires preemption of workload in CQ
			// Flavor 2, spot, also requires preemption of workload in CQ,
			// since the borrowing workload in Cohort is too high priority.
			// Therefore, we choose Flavor 1.
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltesting.MakeClusterQueue("other-alpha").
					Cohort("other").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
					}).
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("on-demand").Resource("gpu", "10").Obj(),
						*utiltesting.MakeFlavorQuotas("spot").Resource("gpu", "10").Obj(),
					).
					Obj(),
				*utiltesting.MakeClusterQueue("other-beta").
					Cohort("other").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("on-demand").Resource("gpu", "0").Obj(),
						*utiltesting.MakeFlavorQuotas("spot").Resource("gpu", "0").Obj(),
					).
					Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltesting.MakeLocalQueue("other", "eng-alpha").ClusterQueue("other-alpha").Obj(),
				*utiltesting.MakeLocalQueue("other", "eng-beta").ClusterQueue("other-beta").Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a1", "eng-alpha").
					Priority(50).
					Queue("other").
					Request("gpu", "6").
					SimpleReserveQuota("other-alpha", "on-demand", now).
					Obj(),
				*utiltesting.MakeWorkload("a2", "eng-alpha").
					Priority(50).
					Queue("other").
					Request("gpu", "5").
					SimpleReserveQuota("other-alpha", "spot", now).
					Obj(),
				*utiltesting.MakeWorkload("b1", "eng-beta").
					// b1 is too high priority for preemptor.
					Priority(9001).
					Queue("other").
					Request("gpu", "5").
					SimpleReserveQuota("other-beta", "spot", now).
					Obj(),
				*utiltesting.MakeWorkload("preemptor", "eng-alpha").
					Priority(100).
					Queue("other").
					Request("gpu", "5").
					Obj(),
			},
			wantPreempted: sets.New[workload.Reference]("eng-alpha/a1"),
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"other-alpha": {"eng-alpha/preemptor"},
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/a1": *utiltesting.MakeAdmission("other-alpha").Assignment("gpu", "on-demand", "6").Obj(),
				"eng-alpha/a2": *utiltesting.MakeAdmission("other-alpha").Assignment("gpu", "spot", "5").Obj(),
				"eng-beta/b1":  *utiltesting.MakeAdmission("other-beta").Assignment("gpu", "spot", "5").Obj(),
			},
		},
		"workload requiring reclaimation prioritized over wl in another full cq": {
			// Also see #3405.
			//
			// CQ2 is lending out capacity to its
			// Cohort. It has a pending workload, WL2,
			// that fits within nominal capacity, and a
			// reclaim policy set to any.

			// CQ1 is using half of its capacity, and is
			// also lending out remaining capacity.

			// CQ3 has no capacity of its own, and is
			// borrowing 10 nominal capacity.

			// With a pending workloads WL1 and WL2 queued
			// in CQ1 and CQ2 respectively, we want to
			// make sure that the WL2 is processed first,
			// so that its preemption calculations are not
			// invalidated by CQ1's WL1, which won't fit
			// into its nominal capacity given the
			// admitted Admitted-Workload-1.

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
				*utiltesting.MakeClusterQueue("CQ1").
					Cohort("other").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("on-demand").Resource("gpu", "10").Obj(),
					).
					Obj(),
				*utiltesting.MakeClusterQueue("CQ2").
					Cohort("other").
					Preemption(kueue.ClusterQueuePreemption{
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("on-demand").Resource("gpu", "10").Obj(),
					).
					Obj(),
				*utiltesting.MakeClusterQueue("CQ3").
					Cohort("other").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("on-demand").Resource("gpu", "0").Obj(),
					).
					Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltesting.MakeLocalQueue("lq", "eng-alpha").ClusterQueue("CQ1").Obj(),
				*utiltesting.MakeLocalQueue("lq", "eng-beta").ClusterQueue("CQ2").Obj(),
				*utiltesting.MakeLocalQueue("lq", "eng-gamma").ClusterQueue("CQ3").Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("Admitted-Workload-1", "eng-alpha").
					Queue("lq").
					Request("gpu", "5").
					SimpleReserveQuota("CQ1", "on-demand", now).
					Obj(),
				*utiltesting.MakeWorkload("WL1", "eng-alpha").
					Creation(now).
					Queue("lq").
					Request("gpu", "10").
					Obj(),
				*utiltesting.MakeWorkload("WL2", "eng-beta").
					Creation(now.Add(time.Second)).
					Queue("lq").
					Request("gpu", "10").
					Obj(),
				*utiltesting.MakeWorkload("Admitted-Workload-2", "eng-gamma").
					Queue("lq").
					Priority(0).
					Request("gpu", "5").
					SimpleReserveQuota("CQ3", "on-demand", now).
					Obj(),
				*utiltesting.MakeWorkload("Admitted-Workload-3", "eng-gamma").
					Queue("lq").
					Priority(1).
					Request("gpu", "5").
					SimpleReserveQuota("CQ3", "on-demand", now).
					Obj(),
			},
			wantPreempted: sets.New[workload.Reference]("eng-gamma/Admitted-Workload-2"),
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"CQ2": {"eng-beta/WL2"},
			},
			wantInadmissibleLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"CQ1": {"eng-alpha/WL1"},
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/Admitted-Workload-1": *utiltesting.MakeAdmission("CQ1").Assignment("gpu", "on-demand", "5").Obj(),
				"eng-gamma/Admitted-Workload-2": *utiltesting.MakeAdmission("CQ3").Assignment("gpu", "on-demand", "5").Obj(),
				"eng-gamma/Admitted-Workload-3": *utiltesting.MakeAdmission("CQ3").Assignment("gpu", "on-demand", "5").Obj(),
			},
		},
		// ClusterQueueA has 2 capacity, 1 admitted workload (req=1), and 1 pending workload (req=2)
		// ClusterQueueB has 0 capacity, and 1 pending workload (req=1)
		// Since ClusterQueueA knows it can reclaim capacity, it lets ClusterQueueB borrow.
		"capacity not blocked when lending clusterqueue can reclaim (ReclaimWithinCohort=Any)": {
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltesting.MakeClusterQueue("ClusterQueueA").
					Cohort("root").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("on-demand").Resource("gpu", "2").Obj(),
					).
					Preemption(kueue.ClusterQueuePreemption{
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					Obj(),
				*utiltesting.MakeClusterQueue("ClusterQueueB").
					Cohort("root").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("on-demand").Resource("gpu", "0").Obj(),
					).
					Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltesting.MakeLocalQueue("lq", "eng-alpha").ClusterQueue("ClusterQueueA").Obj(),
				*utiltesting.MakeLocalQueue("lq", "eng-beta").ClusterQueue("ClusterQueueB").Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a1-admitted", "eng-alpha").
					Queue("lq").
					Request("gpu", "1").
					SimpleReserveQuota("ClusterQueueA", "on-demand", now).
					Obj(),
				*utiltesting.MakeWorkload("a2-pending", "eng-alpha").
					Queue("lq").
					Request("gpu", "2").
					Obj(),
				*utiltesting.MakeWorkload("b1-pending", "eng-beta").
					Creation(now).
					Queue("lq").
					Request("gpu", "1").
					Obj(),
			},
			wantLeft: nil,
			wantInadmissibleLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"ClusterQueueA": {"eng-alpha/a2-pending"},
			},
			wantScheduled: []workload.Reference{
				"eng-beta/b1-pending",
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/a1-admitted": *utiltesting.MakeAdmission("ClusterQueueA").Assignment("gpu", "on-demand", "1").Obj(),
				"eng-beta/b1-pending":   *utiltesting.MakeAdmission("ClusterQueueB").Assignment("gpu", "on-demand", "1").Obj(),
			},
		},
		// ClusterQueueA has 2 capacity, 1 admitted workload (req=1), and 1 pending workload (req=2)
		// ClusterQueueB has 0 capacity, and 1 pending workload (req=1)
		// Since ClusterQueueA is not sure that it can reclaim this capacity, it doesn't let ClusterQueueB borrow.
		"capacity blocked when lending clusterqueue not guaranteed to reclaim (ReclaimWithinCohort=LowerPriority)": {
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltesting.MakeClusterQueue("ClusterQueueA").
					Cohort("root").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("on-demand").Resource("gpu", "2").Obj(),
					).
					Preemption(kueue.ClusterQueuePreemption{
						ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
					}).
					Obj(),
				*utiltesting.MakeClusterQueue("ClusterQueueB").
					Cohort("root").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("on-demand").Resource("gpu", "0").Obj(),
					).
					Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltesting.MakeLocalQueue("lq", "eng-alpha").ClusterQueue("ClusterQueueA").Obj(),
				*utiltesting.MakeLocalQueue("lq", "eng-beta").ClusterQueue("ClusterQueueB").Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a1-admitted", "eng-alpha").
					Queue("lq").
					Request("gpu", "1").
					SimpleReserveQuota("ClusterQueueA", "on-demand", now).
					Obj(),
				*utiltesting.MakeWorkload("a2-pending", "eng-alpha").
					Queue("lq").
					Request("gpu", "2").
					Obj(),
				*utiltesting.MakeWorkload("b1-pending", "eng-beta").
					Creation(now).
					Queue("lq").
					Request("gpu", "1").
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"ClusterQueueB": {"eng-beta/b1-pending"},
			},
			wantInadmissibleLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"ClusterQueueA": {"eng-alpha/a2-pending"},
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/a1-admitted": *utiltesting.MakeAdmission("ClusterQueueA").Assignment("gpu", "on-demand", "1").Obj(),
			},
		},
		// ClusterQueueA has 2 capacity, 1 admitted workload (req=1), and 1 pending workload (req=2)
		// ClusterQueueB has 0 capacity, and 1 pending workload (req=1)
		// Since ClusterQueueA is not sure that it can reclaim this capacity, it doesn't let ClusterQueueB borrow.
		"capacity blocked when lending clusterqueue not guaranteed to reclaim (ReclaimWithinCohort=Never)": {
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltesting.MakeClusterQueue("ClusterQueueA").
					Cohort("root").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("on-demand").Resource("gpu", "2").Obj(),
					).
					Preemption(kueue.ClusterQueuePreemption{
						ReclaimWithinCohort: kueue.PreemptionPolicyNever,
					}).
					Obj(),
				*utiltesting.MakeClusterQueue("ClusterQueueB").
					Cohort("root").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("on-demand").Resource("gpu", "0").Obj(),
					).
					Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltesting.MakeLocalQueue("lq", "eng-alpha").ClusterQueue("ClusterQueueA").Obj(),
				*utiltesting.MakeLocalQueue("lq", "eng-beta").ClusterQueue("ClusterQueueB").Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a1-admitted", "eng-alpha").
					Queue("lq").
					Request("gpu", "1").
					SimpleReserveQuota("ClusterQueueA", "on-demand", now).
					Obj(),
				*utiltesting.MakeWorkload("a2-pending", "eng-alpha").
					Queue("lq").
					Request("gpu", "2").
					Obj(),
				*utiltesting.MakeWorkload("b1-pending", "eng-beta").
					Creation(now).
					Queue("lq").
					Request("gpu", "1").
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"ClusterQueueB": {"eng-beta/b1-pending"},
			},
			wantInadmissibleLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"ClusterQueueA": {"eng-alpha/a2-pending"},
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/a1-admitted": *utiltesting.MakeAdmission("ClusterQueueA").Assignment("gpu", "on-demand", "1").Obj(),
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
				*utiltesting.MakeCohort("root").Obj(),
				*utiltesting.MakeCohort("guaranteed").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("default").
							Resource(corev1.ResourceCPU, "4").Obj(),
					).Parent("root").Obj(),
			},
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltesting.MakeClusterQueue("guaranteed").
					Cohort("guaranteed").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("default").
							Resource(corev1.ResourceCPU, "0").Obj(),
					).Obj(),
				*utiltesting.MakeClusterQueue("best-effort").
					Cohort("root").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("default").
							Resource(corev1.ResourceCPU, "0").Obj(),
					).Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltesting.MakeLocalQueue("lq-guaranteed", "eng-alpha").ClusterQueue("guaranteed").Obj(),
				*utiltesting.MakeLocalQueue("lq-best-effort", "eng-alpha").ClusterQueue("best-effort").Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("guaranteed", "eng-alpha").
					Queue("lq-guaranteed").
					Priority(0).
					PodSets(*utiltesting.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "4").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("best-effort", "eng-alpha").
					Queue("lq-best-effort").
					Priority(3).
					PodSets(*utiltesting.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "4").
						Obj()).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/guaranteed": *utiltesting.MakeAdmission("guaranteed", "one").Assignment(corev1.ResourceCPU, "default", "4").Obj(),
			},
			wantScheduled: []workload.Reference{"eng-alpha/guaranteed"},
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
				*utiltesting.MakeClusterQueue("cq1").
					Cohort("cohort").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
						BorrowWithinCohort: &kueue.BorrowWithinCohort{
							Policy: kueue.BorrowWithinCohortPolicyLowerPriority,
						},
					}).
					FlavorFungibility(kueue.FlavorFungibility{
						WhenCanPreempt: kueue.Preempt,
						WhenCanBorrow:  kueue.Borrow,
					}).
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("on-demand").
							ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("0").BorrowingLimit("1").Append().
							Obj(),
						*utiltesting.MakeFlavorQuotas("spot").
							ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("0").BorrowingLimit("1").Append().
							Obj(),
					).Obj(),
				*utiltesting.MakeClusterQueue("cq2").
					Cohort("cohort").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("on-demand").
							ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("1").Append().
							Obj(),
						*utiltesting.MakeFlavorQuotas("spot").
							ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("1").Append().
							Obj(),
					).Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltesting.MakeLocalQueue("lq1", "eng-alpha").ClusterQueue("cq1").Obj(),
				*utiltesting.MakeLocalQueue("lq2", "eng-alpha").ClusterQueue("cq2").Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("admitted", "eng-alpha").
					Queue("lq2").
					Request(corev1.ResourceCPU, "1").
					SimpleReserveQuota("cq2", "on-demand", now.Add(-time.Second)).
					Admitted(true).
					Priority(0).
					Obj(),
				*utiltesting.MakeWorkload("new", "eng-alpha").
					Queue("lq1").
					Request(corev1.ResourceCPU, "1").
					Priority(100).
					Obj(),
			},
			wantScheduled: []workload.Reference{"eng-alpha/new"},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/new":      *utiltesting.MakeAdmission("cq1").Assignment(corev1.ResourceCPU, "spot", "1").Obj(),
				"eng-alpha/admitted": *utiltesting.MakeAdmission("cq2").Assignment(corev1.ResourceCPU, "on-demand", "1").Obj(),
			},
		},
		// Workload-slice scheduling test case.
		"workload-slice fits in single clusterQueue": {
			enableElasticJobsViaWorkloadSlice: true,
			trackPatchedWorkloads:             true,

			// workloads that will be returned by the fake.client.
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo-1", "sales").
					ResourceVersion("1").
					Queue("main").
					PodSets(*utiltesting.MakePodSet("one", 10).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Generation(1).
					ReserveQuota(utiltesting.MakeAdmission("sales", "one").Assignment(corev1.ResourceCPU, "default", "10000m").AssignmentPodCount(10).Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             kueue.WorkloadQuotaReserved,
						Message:            "Quota reserved in ClusterQueue sales",
						ObservedGeneration: 1,
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionTrue,
						Reason:             kueue.WorkloadAdmitted,
						Message:            "The workload is admitted",
						ObservedGeneration: 1,
					}).
					Obj(),
				*utiltesting.MakeWorkload("foo-2", "sales").
					ResourceVersion("1").
					Annotation(workloadslicing.WorkloadSliceReplacementFor, "sales/foo-1").
					Queue("main").
					PodSets(*utiltesting.MakePodSet("one", 15).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Generation(1).
					Obj(),
			},
			// wantAssignments is a map of workload name to the status assignments expected to be in the cache after the scheduling cycle.
			wantAssignments: map[workload.Reference]kueue.Admission{
				"sales/foo-1": *utiltesting.MakeAdmission("sales", "one").Assignment(corev1.ResourceCPU, "default", "10").AssignmentPodCount(10).Obj(),
				"sales/foo-2": *utiltesting.MakeAdmission("sales", "one").Assignment(corev1.ResourceCPU, "default", "15").AssignmentPodCount(15).Obj(),
			},
			// wantScheduled is a list of workloads admission status expected to be added to the cache after the scheduling cycle.
			wantScheduled: []workload.Reference{"sales/foo-2"},
			// wantWorkloads an authoritative list of workloads expected in K8s API after the scheduling cycle.
			// This may not be the same as previous "want*" values due to the stabbed apply status invocations in the test.
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo-1", "sales").
					ResourceVersion("1").
					Queue("main").
					PodSets(*utiltesting.MakePodSet("one", 10).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Admission(
						utiltesting.MakeAdmission("sales", "one").
							Assignment(corev1.ResourceCPU, "default", "10000m").
							AssignmentPodCount(10).
							Obj(),
					).
					Generation(1).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             kueue.WorkloadQuotaReserved,
						Message:            "Quota reserved in ClusterQueue sales",
						ObservedGeneration: 1,
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionTrue,
						Reason:             kueue.WorkloadAdmitted,
						Message:            "The workload is admitted",
						ObservedGeneration: 1,
					}).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadFinished,
						Status:  metav1.ConditionTrue,
						Reason:  kueue.WorkloadSliceReplaced,
						Message: "Replaced to accommodate a workload (UID: , JobUID: ) due to workload slice aggregation",
					}).
					Obj(),
				*utiltesting.MakeWorkload("foo-2", "sales").
					Annotation(workloadslicing.WorkloadSliceReplacementFor, "sales/foo-1").
					ResourceVersion("1").
					Queue("main").
					PodSets(*utiltesting.MakePodSet("one", 15).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Admission(
						utiltesting.MakeAdmission("sales", "one").
							Assignment(corev1.ResourceCPU, "default", "15000m").
							AssignmentPodCount(15).
							Obj(),
					).
					Generation(1).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             kueue.WorkloadQuotaReserved,
						Message:            "Quota reserved in ClusterQueue sales",
						ObservedGeneration: 1,
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionTrue,
						Reason:             kueue.WorkloadAdmitted,
						Message:            "The workload is admitted",
						ObservedGeneration: 1,
					}).
					Obj(),
			},
			workloadCmpOpts: cmp.Options{
				cmpopts.EquateEmpty(),
				cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime"),
				cmpopts.SortSlices(func(a, b kueue.Workload) bool { return workload.Key(&a) < workload.Key(&b) }),
			},
			eventCmpOpts: ignoreEventMessageCmpOpts,
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "sales", Name: "foo-1"},
					Reason:    kueue.WorkloadSliceReplaced,
					EventType: corev1.EventTypeNormal,
				},
				{
					Key:       types.NamespacedName{Namespace: "sales", Name: "foo-2"},
					Reason:    "QuotaReserved",
					EventType: corev1.EventTypeNormal,
				},
				{
					Key:       types.NamespacedName{Namespace: "sales", Name: "foo-2"},
					Reason:    "Admitted",
					EventType: corev1.EventTypeNormal,
				},
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			metrics.AdmissionCyclePreemptionSkips.Reset()
			if tc.disableLendingLimit {
				features.SetFeatureGateDuringTest(t, features.LendingLimit, false)
			}
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
						utiltesting.MakeNamespaceWrapper("eng-alpha").Label("dep", "eng").Obj(),
						utiltesting.MakeNamespaceWrapper("eng-beta").Label("dep", "eng").Obj(),
						utiltesting.MakeNamespaceWrapper("eng-gamma").Label("dep", "eng").Obj(),
						utiltesting.MakeNamespaceWrapper("sales").Label("dep", "sales").Obj(),
						utiltesting.MakeNamespaceWrapper("lend").Label("dep", "lend").Obj(),
					}, tc.objects...,
				)...)
			for i := range tc.workloads {
				clientBuilder.WithStatusSubresource(&tc.workloads[i])
			}

			// Patch workloads tracking by leveraging fake.Client sub-resource interceptor.
			patchedWorkloads := sets.New[*kueue.Workload]()
			if tc.trackPatchedWorkloads {
				clientBuilder.WithInterceptorFuncs(interceptor.Funcs{
					SubResourcePatch: func(ctx context.Context, clnt client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
						if subResourceName != "status" {
							// There should be only "status" subresource patches in the workload scheduler context.
							return fmt.Errorf("unexpected subresource in workload status patch: %s", subResourceName)
						}
						// There should be only *kueue.Workloads status patches.
						wl, ok := obj.(*kueue.Workload)
						if !ok {
							return fmt.Errorf("unexpected object in workload status patch: %T", obj)
						}
						// Bump up resource version.
						rv, _ := strconv.Atoi(wl.GetResourceVersion())
						wl.SetResourceVersion(strconv.Itoa(rv))
						patchedWorkloads.Insert(wl)
						return nil
					},
				})
			}
			cl := clientBuilder.Build()
			recorder := &utiltesting.EventRecorder{}
			cqCache := cache.New(cl)
			qManager := queue.NewManager(cl, cqCache)
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

			scheduler := New(qManager, cqCache, cl, recorder, WithFairSharing(&config.FairSharing{Enable: tc.enableFairSharing}), WithClock(t, fakeClock))
			wg := sync.WaitGroup{}
			scheduler.setAdmissionRoutineWrapper(routine.NewWrapper(
				func() { wg.Add(1) },
				func() { wg.Done() },
			))

			gotScheduled := make(map[workload.Reference]kueue.Admission)
			var mu sync.Mutex
			scheduler.applyAdmission = func(ctx context.Context, w *kueue.Workload) error {
				if tc.admissionError != nil {
					return tc.admissionError
				}
				mu.Lock()
				gotScheduled[workload.Key(w)] = *w.Status.Admission
				mu.Unlock()
				return nil
			}
			gotPreempted := sets.New[workload.Reference]()
			scheduler.preemptor.OverrideApply(func(_ context.Context, w *kueue.Workload, _, _ string) error {
				mu.Lock()
				gotPreempted.Insert(workload.Key(w))
				mu.Unlock()
				return nil
			})

			ctx, cancel := context.WithTimeout(ctx, queueingTimeout)
			go qManager.CleanUpOnContext(ctx)
			defer cancel()

			scheduler.schedule(ctx)
			wg.Wait()

			wantScheduled := make(map[workload.Reference]kueue.Admission)
			for _, key := range tc.wantScheduled {
				wantScheduled[key] = tc.wantAssignments[key]
			}
			if diff := cmp.Diff(wantScheduled, gotScheduled); diff != "" {
				t.Errorf("Unexpected scheduled workloads (-want,+got):\n%s", diff)
			}

			if diff := cmp.Diff(tc.wantPreempted, gotPreempted); diff != "" {
				t.Errorf("Unexpected preemptions (-want,+got):\n%s", diff)
			}

			// Verify assignments in cache.
			gotAssignments := make(map[workload.Reference]kueue.Admission)
			gotWorkloads := make(map[workload.Reference]kueue.Workload)
			snapshot, err := cqCache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("unexpected error while building snapshot: %v", err)
			}
			for cqName, c := range snapshot.ClusterQueues() {
				for name, w := range c.Workloads {
					gotWorkloads[name] = *w.Obj
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
			// If we track patched workloads, replace workload defined in queue with the tracked patch instance,
			// since workloads in the queue are "stale" in respect of status updates.
			// Note: if we don't track workload status patching - patchWorkloads will be empty, e.g., "noop".
			for wl := range patchedWorkloads {
				gotWorkloads[workload.Key(wl)] = *wl
			}

			if tc.wantWorkloads != nil {
				if diff := cmp.Diff(tc.wantWorkloads, goslices.Collect(maps.Values(gotWorkloads)), tc.workloadCmpOpts...); diff != "" {
					t.Errorf("Unexpected workloads in cache (-want,+got):\n%s", diff)
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
				val, err := testutil.GetGaugeMetricValue(metrics.AdmissionCyclePreemptionSkips.WithLabelValues(cqName))
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
			iter := makeIterator(t.Context(), tc.input, tc.workloadOrdering, false)
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
	now := time.Now()
	fakeClock := testingclock.NewFakeClock(now)

	resourceFlavors := []*kueue.ResourceFlavor{
		utiltesting.MakeResourceFlavor("on-demand").Obj(),
		utiltesting.MakeResourceFlavor("spot").Obj(),
	}
	clusterQueueCohort := []kueue.ClusterQueue{
		*utiltesting.MakeClusterQueue("eng-cohort-alpha").
			Cohort("cohort").
			QueueingStrategy(kueue.StrictFIFO).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue:  kueue.PreemptionPolicyNever,
				ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
			}).
			FlavorFungibility(kueue.FlavorFungibility{
				WhenCanPreempt: kueue.Preempt,
				WhenCanBorrow:  kueue.Borrow,
			}).
			ResourceGroup(
				*utiltesting.MakeFlavorQuotas("on-demand").
					Resource(corev1.ResourceCPU, "50", "50").Obj(),
				*utiltesting.MakeFlavorQuotas("spot").
					Resource(corev1.ResourceCPU, "100", "0").Obj(),
			).Obj(),
		*utiltesting.MakeClusterQueue("eng-cohort-beta").
			Cohort("cohort").
			QueueingStrategy(kueue.StrictFIFO).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue:  kueue.PreemptionPolicyNever,
				ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
			}).
			FlavorFungibility(kueue.FlavorFungibility{
				WhenCanPreempt: kueue.Preempt,
				WhenCanBorrow:  kueue.Borrow,
			}).
			ResourceGroup(
				*utiltesting.MakeFlavorQuotas("on-demand").
					Resource(corev1.ResourceCPU, "50", "50").Obj(),
				*utiltesting.MakeFlavorQuotas("spot").
					Resource(corev1.ResourceCPU, "100", "0").Obj(),
			).Obj(),
		*utiltesting.MakeClusterQueue("eng-cohort-theta").
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
				*utiltesting.MakeFlavorQuotas("on-demand").
					Resource(corev1.ResourceCPU, "50", "50").Obj(),
				*utiltesting.MakeFlavorQuotas("spot").
					Resource(corev1.ResourceCPU, "100", "0").Obj(),
			).Obj(),
	}

	queues := []kueue.LocalQueue{
		{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "default",
				Name:      "main",
			},
			Spec: kueue.LocalQueueSpec{
				ClusterQueue: "eng-alpha",
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "default",
				Name:      "main-alpha",
			},
			Spec: kueue.LocalQueueSpec{
				ClusterQueue: "eng-cohort-alpha",
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "default",
				Name:      "main-beta",
			},
			Spec: kueue.LocalQueueSpec{
				ClusterQueue: "eng-cohort-beta",
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "default",
				Name:      "main-theta",
			},
			Spec: kueue.LocalQueueSpec{
				ClusterQueue: "eng-cohort-theta",
			},
		},
	}
	cases := []struct {
		name                           string
		cqs                            []kueue.ClusterQueue
		admittedWorkloads              []kueue.Workload
		workloads                      []kueue.Workload
		deleteWorkloads                []kueue.Workload
		wantPreempted                  sets.Set[workload.Reference]
		wantAdmissionsOnFirstSchedule  map[workload.Reference]kueue.Admission
		wantAdmissionsOnSecondSchedule map[workload.Reference]kueue.Admission
	}{
		{
			name: "scheduling on the first flavor is unblocked after some workloads were deleted",
			// In this scenario we wait for the first flavor to be unblocked by
			// removal of workloads. The second flavor cannot be used because
			// it is noFit.
			cqs: []kueue.ClusterQueue{
				*utiltesting.MakeClusterQueue("eng-alpha").
					QueueingStrategy(kueue.BestEffortFIFO).
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
							Resource(corev1.ResourceCPU, "10", "0").Obj(),
					).Obj(),
			},
			admittedWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("low-1", "default").
					Queue("main").
					Request(corev1.ResourceCPU, "50").
					ReserveQuota(utiltesting.MakeAdmission("eng-alpha").Assignment(corev1.ResourceCPU, "on-demand", "50").Obj()).
					Admitted(true).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("preemptor", "default").
					Queue("main").
					Request(corev1.ResourceCPU, "20").
					Obj(),
			},
			deleteWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("low-1", "default").
					Queue("main").
					Request(corev1.ResourceCPU, "50").
					ReserveQuota(utiltesting.MakeAdmission("eng-alpha").Assignment(corev1.ResourceCPU, "on-demand", "50").Obj()).
					Admitted(true).
					Obj(),
			},
			wantPreempted:                 sets.Set[workload.Reference]{},
			wantAdmissionsOnFirstSchedule: map[workload.Reference]kueue.Admission{},
			wantAdmissionsOnSecondSchedule: map[workload.Reference]kueue.Admission{
				"default/preemptor": *utiltesting.MakeAdmission("eng-alpha").Assignment(corev1.ResourceCPU, "on-demand", "20").Obj(),
			},
		},
		{
			name: "borrow before next flavor",
			cqs:  clusterQueueCohort,
			admittedWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("placeholder", "default").
					Request(corev1.ResourceCPU, "50").
					ReserveQuota(utiltesting.MakeAdmission("eng-cohort-alpha").Assignment(corev1.ResourceCPU, "on-demand", "50").Obj()).
					Admitted(true).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("borrower", "default").
					Queue("main-alpha").
					Request(corev1.ResourceCPU, "20").
					Obj(),
				*utiltesting.MakeWorkload("workload1", "default").
					Queue("main-beta").
					Request(corev1.ResourceCPU, "20").
					Obj(),
			},
			deleteWorkloads: []kueue.Workload{},
			wantPreempted:   sets.Set[workload.Reference]{},
			wantAdmissionsOnFirstSchedule: map[workload.Reference]kueue.Admission{
				"default/workload1": *utiltesting.MakeAdmission("eng-cohort-beta").Assignment(corev1.ResourceCPU, "on-demand", "20").Obj(),
				"default/borrower":  *utiltesting.MakeAdmission("eng-cohort-alpha").Assignment(corev1.ResourceCPU, "on-demand", "20").Obj(),
			},
			wantAdmissionsOnSecondSchedule: map[workload.Reference]kueue.Admission{
				"default/placeholder": *utiltesting.MakeAdmission("eng-cohort-alpha").Assignment(corev1.ResourceCPU, "on-demand", "50").Obj(),
				"default/workload1":   *utiltesting.MakeAdmission("eng-cohort-beta").Assignment(corev1.ResourceCPU, "on-demand", "20").Obj(),
				"default/borrower":    *utiltesting.MakeAdmission("eng-cohort-alpha").Assignment(corev1.ResourceCPU, "on-demand", "20").Obj(),
			},
		},
		{
			name: "borrow after all flavors",
			cqs:  clusterQueueCohort,
			admittedWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("placeholder", "default").
					Request(corev1.ResourceCPU, "50").
					ReserveQuota(utiltesting.MakeAdmission("eng-cohort-alpha").Assignment(corev1.ResourceCPU, "on-demand", "50").Obj()).
					Admitted(true).
					Obj(),
				*utiltesting.MakeWorkload("placeholder1", "default").
					Request(corev1.ResourceCPU, "50").
					ReserveQuota(utiltesting.MakeAdmission("eng-cohort-theta").Assignment(corev1.ResourceCPU, "on-demand", "50").Obj()).
					Admitted(true).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("workload", "default").
					Queue("main-theta").
					Request(corev1.ResourceCPU, "20").
					Obj(),
			},
			deleteWorkloads: []kueue.Workload{},
			wantPreempted:   sets.Set[workload.Reference]{},
			wantAdmissionsOnFirstSchedule: map[workload.Reference]kueue.Admission{
				"default/workload": *utiltesting.MakeAdmission("eng-cohort-theta").Assignment(corev1.ResourceCPU, "spot", "20").Obj(),
			},
			wantAdmissionsOnSecondSchedule: map[workload.Reference]kueue.Admission{
				"default/placeholder":  *utiltesting.MakeAdmission("eng-cohort-alpha").Assignment(corev1.ResourceCPU, "on-demand", "50").Obj(),
				"default/placeholder1": *utiltesting.MakeAdmission("eng-cohort-theta").Assignment(corev1.ResourceCPU, "on-demand", "50").Obj(),
				"default/workload":     *utiltesting.MakeAdmission("eng-cohort-theta").Assignment(corev1.ResourceCPU, "spot", "20").Obj(),
			},
		},
		{
			name: "when the next flavor is full, but can borrow on first",
			cqs:  clusterQueueCohort,
			admittedWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("placeholder", "default").
					Request(corev1.ResourceCPU, "40").
					ReserveQuota(utiltesting.MakeAdmission("eng-cohort-alpha").Assignment(corev1.ResourceCPU, "on-demand", "40").Obj()).
					Admitted(true).
					Obj(),
				*utiltesting.MakeWorkload("placeholder1", "default").
					Request(corev1.ResourceCPU, "40").
					ReserveQuota(utiltesting.MakeAdmission("eng-cohort-theta").Assignment(corev1.ResourceCPU, "on-demand", "40").Obj()).
					Admitted(true).
					Obj(),
				*utiltesting.MakeWorkload("placeholder2", "default").
					Request(corev1.ResourceCPU, "100").
					ReserveQuota(utiltesting.MakeAdmission("eng-cohort-theta").Assignment(corev1.ResourceCPU, "spot", "100").Obj()).
					Admitted(true).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("workload", "default").
					Queue("main-theta").
					Request(corev1.ResourceCPU, "20").
					Obj(),
			},
			deleteWorkloads: []kueue.Workload{},
			wantPreempted:   sets.Set[workload.Reference]{},
			wantAdmissionsOnFirstSchedule: map[workload.Reference]kueue.Admission{
				"default/workload": *utiltesting.MakeAdmission("eng-cohort-theta").Assignment(corev1.ResourceCPU, "on-demand", "20").Obj(),
			},
			wantAdmissionsOnSecondSchedule: map[workload.Reference]kueue.Admission{
				"default/placeholder":  *utiltesting.MakeAdmission("eng-cohort-alpha").Assignment(corev1.ResourceCPU, "on-demand", "40").Obj(),
				"default/placeholder1": *utiltesting.MakeAdmission("eng-cohort-theta").Assignment(corev1.ResourceCPU, "on-demand", "40").Obj(),
				"default/placeholder2": *utiltesting.MakeAdmission("eng-cohort-theta").Assignment(corev1.ResourceCPU, "spot", "100").Obj(),
				"default/workload":     *utiltesting.MakeAdmission("eng-cohort-theta").Assignment(corev1.ResourceCPU, "on-demand", "20").Obj(),
			},
		},
		{
			name: "when the next flavor is full, but can preempt on first",
			cqs:  clusterQueueCohort,
			admittedWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("placeholder-alpha", "default").
					Priority(-1).
					Request(corev1.ResourceCPU, "150").
					ReserveQuota(utiltesting.MakeAdmission("eng-cohort-alpha").Assignment(corev1.ResourceCPU, "on-demand", "150").Obj()).
					Admitted(true).
					Obj(),
				*utiltesting.MakeWorkload("placeholder-theta-spot", "default").
					Request(corev1.ResourceCPU, "100").
					ReserveQuota(utiltesting.MakeAdmission("eng-cohort-theta").Assignment(corev1.ResourceCPU, "spot", "100").Obj()).
					Admitted(true).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("new", "default").
					Queue("main-theta").
					Request(corev1.ResourceCPU, "20").
					Obj(),
			},
			deleteWorkloads:               []kueue.Workload{*utiltesting.MakeWorkload("placeholder-alpha", "default").Obj()},
			wantPreempted:                 sets.New[workload.Reference]("default/placeholder-alpha"),
			wantAdmissionsOnFirstSchedule: map[workload.Reference]kueue.Admission{},
			wantAdmissionsOnSecondSchedule: map[workload.Reference]kueue.Admission{
				"default/placeholder-theta-spot": *utiltesting.MakeAdmission("eng-cohort-theta").Assignment(corev1.ResourceCPU, "spot", "100").Obj(),
				"default/new":                    *utiltesting.MakeAdmission("eng-cohort-theta").Assignment(corev1.ResourceCPU, "on-demand", "20").Obj(),
			},
		},
		{
			name: "TryNextFlavor, but second flavor is full and can preempt on first",
			cqs: []kueue.ClusterQueue{
				*utiltesting.MakeClusterQueue("eng-cohort-alpha").
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
						*utiltesting.MakeFlavorQuotas("on-demand").
							ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("0").BorrowingLimit("60").Append().
							Obj(),
						*utiltesting.MakeFlavorQuotas("spot").
							ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("30").BorrowingLimit("30").Append().
							Obj(),
					).Obj(),
				*utiltesting.MakeClusterQueue("eng-cohort-beta").
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
						*utiltesting.MakeFlavorQuotas("on-demand").
							ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("30").BorrowingLimit("30").Append().
							Obj(),
						*utiltesting.MakeFlavorQuotas("spot").
							ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("30").BorrowingLimit("30").Append().
							Obj(),
					).Obj(),
				*utiltesting.MakeClusterQueue("eng-cohort-shared").
					Cohort("cohort").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("on-demand").
							ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("30").Append().
							Obj(),
					).Obj(),
			},
			admittedWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("alpha1", "default").
					Request(corev1.ResourceCPU, "22").
					SimpleReserveQuota("eng-cohort-alpha", "on-demand", now.Add(-time.Second)).
					Admitted(true).
					Obj(),
				*utiltesting.MakeWorkload("alpha2", "default").
					Request(corev1.ResourceCPU, "22").
					SimpleReserveQuota("eng-cohort-alpha", "on-demand", now).
					Admitted(true).
					Obj(),
				*utiltesting.MakeWorkload("alpha3", "default").
					Request(corev1.ResourceCPU, "22").
					SimpleReserveQuota("eng-cohort-alpha", "spot", now).
					Admitted(true).
					Obj(),
				*utiltesting.MakeWorkload("beta1", "default").
					Request(corev1.ResourceCPU, "22").
					SimpleReserveQuota("eng-cohort-beta", "spot", now).
					Admitted(true).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("new", "default").
					Queue("main-beta").
					Request(corev1.ResourceCPU, "22").
					Obj(),
			},
			deleteWorkloads: []kueue.Workload{*utiltesting.MakeWorkload("alpha2", "default").Obj()},
			wantPreempted:   sets.New[workload.Reference]("default/alpha2"),
			wantAdmissionsOnSecondSchedule: map[workload.Reference]kueue.Admission{
				"default/alpha1": *utiltesting.MakeAdmission("eng-cohort-alpha").Assignment(corev1.ResourceCPU, "on-demand", "22").Obj(),
				"default/alpha3": *utiltesting.MakeAdmission("eng-cohort-alpha").Assignment(corev1.ResourceCPU, "spot", "22").Obj(),
				"default/beta1":  *utiltesting.MakeAdmission("eng-cohort-beta").Assignment(corev1.ResourceCPU, "spot", "22").Obj(),
				"default/new":    *utiltesting.MakeAdmission("eng-cohort-beta").Assignment(corev1.ResourceCPU, "on-demand", "22").Obj(),
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, log := utiltesting.ContextWithLog(t)
			scheme := runtime.NewScheme()

			clientBuilder := utiltesting.NewClientBuilder().
				WithLists(&kueue.WorkloadList{Items: tc.admittedWorkloads},
					&kueue.WorkloadList{Items: tc.workloads},
					&kueue.ClusterQueueList{Items: tc.cqs},
					&kueue.LocalQueueList{Items: queues}).
				WithObjects(
					utiltesting.MakeNamespace("default"),
				)
			cl := clientBuilder.Build()
			broadcaster := record.NewBroadcaster()
			recorder := broadcaster.NewRecorder(scheme,
				corev1.EventSource{Component: constants.AdmissionName})
			cqCache := cache.New(cl)
			qManager := queue.NewManager(cl, cqCache)
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
			gotScheduled := make(map[workload.Reference]kueue.Admission)
			var mu sync.Mutex
			scheduler.applyAdmission = func(ctx context.Context, w *kueue.Workload) error {
				mu.Lock()
				gotScheduled[workload.Key(w)] = *w.Status.Admission
				mu.Unlock()
				return nil
			}
			wg := sync.WaitGroup{}
			scheduler.setAdmissionRoutineWrapper(routine.NewWrapper(
				func() { wg.Add(1) },
				func() { wg.Done() },
			))
			gotPreempted := sets.New[workload.Reference]()
			scheduler.preemptor.OverrideApply(func(_ context.Context, w *kueue.Workload, _, _ string) error {
				mu.Lock()
				gotPreempted.Insert(workload.Key(w))
				mu.Unlock()
				return nil
			})

			ctx, cancel := context.WithTimeout(ctx, queueingTimeout)
			go qManager.CleanUpOnContext(ctx)
			defer cancel()

			scheduler.schedule(ctx)
			wg.Wait()

			if diff := cmp.Diff(tc.wantPreempted, gotPreempted); diff != "" {
				t.Errorf("Unexpected preemptions (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantAdmissionsOnFirstSchedule, gotScheduled, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Unexpected scheduled workloads (-want,+got):\n%s", diff)
			}

			for _, wl := range tc.deleteWorkloads {
				err := cl.Delete(ctx, &wl)
				if err != nil {
					t.Errorf("Delete workload failed: %v", err)
				}
				err = cqCache.DeleteWorkload(log, &wl)
				if err != nil {
					t.Errorf("Delete workload failed: %v", err)
				}
				qManager.QueueAssociatedInadmissibleWorkloadsAfter(ctx, &wl, nil)
			}

			scheduler.schedule(ctx)
			wg.Wait()

			if diff := cmp.Diff(tc.wantPreempted, gotPreempted); diff != "" {
				t.Errorf("Unexpected preemptions (-want,+got):\n%s", diff)
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

var ignoreConditionTimestamps = cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime")

func TestRequeueAndUpdate(t *testing.T) {
	cq := utiltesting.MakeClusterQueue("cq").Obj()
	q1 := utiltesting.MakeLocalQueue("q1", "ns1").ClusterQueue(cq.Name).Obj()
	w1 := utiltesting.MakeWorkload("w1", "ns1").Queue(kueue.LocalQueueName(q1.Name)).Obj()

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
			cqCache := cache.New(cl)
			qManager := queue.NewManager(cl, cqCache)
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
			if diff := cmp.Diff(tc.wantStatus, updatedWl.Status, ignoreConditionTimestamps); diff != "" {
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
	resourceFlavors := []*kueue.ResourceFlavor{
		utiltesting.MakeResourceFlavor("on-demand").Obj(),
		utiltesting.MakeResourceFlavor("spot").Obj(),
		utiltesting.MakeResourceFlavor("model-a").Obj(),
		utiltesting.MakeResourceFlavor("model-b").Obj(),
	}
	cq := utiltesting.MakeClusterQueue("cq").
		Cohort("eng").
		ResourceGroup(
			*utiltesting.MakeFlavorQuotas("on-demand").
				Resource(corev1.ResourceMemory, "100").Obj(),
			*utiltesting.MakeFlavorQuotas("spot").
				Resource(corev1.ResourceMemory, "0", "100").Obj(),
		).
		ResourceGroup(
			*utiltesting.MakeFlavorQuotas("model-a").
				Resource("gpu", "10", "0").Obj(),
			*utiltesting.MakeFlavorQuotas("model-b").
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
			cqCache := cache.New(cl)
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
				admission := utiltesting.MakeAdmission("cq").Assignment(fr.Resource, fr.Flavor, quantity.String())
				wl := utiltesting.MakeWorkload(fmt.Sprintf("workload-%d", i), "default-namespace").ReserveQuota(admission.Obj()).Obj()
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

func TestScheduleForTAS(t *testing.T) {
	const (
		tasBlockLabel = "cloud.com/topology-block"
		tasRackLabel  = "cloud.provider.com/rack"
	)
	defaultSingleNode := []corev1.Node{
		*testingnode.MakeNode("x1").
			Label("tas-node", "true").
			Label(corev1.LabelHostname, "x1").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("1"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
				corev1.ResourcePods:   resource.MustParse("10"),
			}).
			Ready().
			Obj(),
	}

	//        b1                   b2
	//     /      \             /      \
	//    r1       r2          r1       r2
	//  /  |  \     |           |        |
	// x2  x3  x4  x1          x5       x6
	defaultNodes := []corev1.Node{
		*testingnode.MakeNode("b1-r1-x1").
			Label("tas-node", "true").
			Label(tasBlockLabel, "b1").
			Label(tasRackLabel, "r1").
			Label(corev1.LabelHostname, "x1").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("1"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
				corev1.ResourcePods:   resource.MustParse("10"),
			}).
			Ready().
			Obj(),
		*testingnode.MakeNode("b1-r2-x2").
			Label("tas-node", "true").
			Label(tasBlockLabel, "b1").
			Label(tasRackLabel, "r2").
			Label(corev1.LabelHostname, "x2").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("1"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
				corev1.ResourcePods:   resource.MustParse("10"),
			}).
			Ready().
			Obj(),
		*testingnode.MakeNode("b1-r2-x3").
			Label("tas-node", "true").
			Label(tasBlockLabel, "b1").
			Label(tasRackLabel, "r2").
			Label(corev1.LabelHostname, "x3").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("1"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
				corev1.ResourcePods:   resource.MustParse("10"),
			}).
			Ready().
			Obj(),
		*testingnode.MakeNode("b1-r2-x4").
			Label("tas-node", "true").
			Label(tasBlockLabel, "b1").
			Label(tasRackLabel, "r2").
			Label(corev1.LabelHostname, "x4").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("1"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
				corev1.ResourcePods:   resource.MustParse("10"),
			}).
			Ready().
			Obj(),
		*testingnode.MakeNode("b2-r2-x5").
			Label("tas-node", "true").
			Label(tasBlockLabel, "b2").
			Label(tasRackLabel, "r1").
			Label(corev1.LabelHostname, "x5").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("1"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
				corev1.ResourcePods:   resource.MustParse("10"),
			}).
			Ready().
			Obj(),
		*testingnode.MakeNode("b2-r2-x6").
			Label("tas-node", "true").
			Label(tasBlockLabel, "b2").
			Label(tasRackLabel, "r2").
			Label(corev1.LabelHostname, "x6").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("2"),
				corev1.ResourceMemory: resource.MustParse("4Gi"),
				corev1.ResourcePods:   resource.MustParse("40"),
			}).
			Ready().
			Obj(),
	}

	defaultSingleLevelTopology := *utiltesting.MakeDefaultOneLevelTopology("tas-single-level")
	defaultTwoLevelTopology := *utiltesting.MakeTopology("tas-two-level").
		Levels(tasRackLabel, corev1.LabelHostname).
		Obj()
	defaultThreeLevelTopology := *utiltesting.MakeTopology("tas-three-level").
		Levels(tasBlockLabel, tasRackLabel, corev1.LabelHostname).
		Obj()
	defaultFlavor := *utiltesting.MakeResourceFlavor("default").Obj()
	defaultTASFlavor := *utiltesting.MakeResourceFlavor("tas-default").
		NodeLabel("tas-node", "true").
		TopologyName("tas-single-level").
		Obj()
	defaultTASTwoLevelFlavor := *utiltesting.MakeResourceFlavor("tas-default").
		NodeLabel("tas-node", "true").
		TopologyName("tas-two-level").
		Obj()
	defaultTASThreeLevelFlavor := *utiltesting.MakeResourceFlavor("tas-default").
		NodeLabel("tas-node", "true").
		TopologyName("tas-three-level").
		Obj()
	defaultClusterQueue := *utiltesting.MakeClusterQueue("tas-main").
		ResourceGroup(*utiltesting.MakeFlavorQuotas("tas-default").
			Resource(corev1.ResourceCPU, "50").
			Resource(corev1.ResourceMemory, "50Gi").Obj()).
		Obj()
	defaultProvCheck := *utiltesting.MakeAdmissionCheck("prov-check").
		ControllerName(kueue.ProvisioningRequestControllerName).
		Condition(metav1.Condition{
			Type:   kueue.AdmissionCheckActive,
			Status: metav1.ConditionTrue,
		}).
		Obj()
	defaultCustomCheck := *utiltesting.MakeAdmissionCheck("custom-check").
		ControllerName("custom-admission-check-controller").
		Condition(metav1.Condition{
			Type:   kueue.AdmissionCheckActive,
			Status: metav1.ConditionTrue,
		}).
		Obj()
	clusterQueueWithProvReq := *utiltesting.MakeClusterQueue("tas-main").
		ResourceGroup(*utiltesting.MakeFlavorQuotas("tas-default").
			Resource(corev1.ResourceCPU, "50").
			Resource(corev1.ResourceMemory, "50Gi").Obj()).
		AdmissionChecks(kueue.AdmissionCheckReference(defaultProvCheck.Name)).
		Obj()
	clusterQueueWithCustomCheck := *utiltesting.MakeClusterQueue("tas-main").
		ResourceGroup(*utiltesting.MakeFlavorQuotas("tas-default").
			Resource(corev1.ResourceCPU, "50").
			Resource(corev1.ResourceMemory, "50Gi").Obj()).
		AdmissionChecks(kueue.AdmissionCheckReference(defaultCustomCheck.Name)).
		Obj()
	queues := []kueue.LocalQueue{
		{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "default",
				Name:      "tas-main",
			},
			Spec: kueue.LocalQueueSpec{
				ClusterQueue: "tas-main",
			},
		},
	}
	eventIgnoreMessage := cmpopts.IgnoreFields(utiltesting.EventRecord{}, "Message")
	cases := map[string]struct {
		nodes           []corev1.Node
		pods            []corev1.Pod
		topologies      []kueuealpha.Topology
		admissionChecks []kueue.AdmissionCheck
		resourceFlavors []kueue.ResourceFlavor
		clusterQueues   []kueue.ClusterQueue
		workloads       []kueue.Workload

		// wantNewAssignments is a summary of all new admissions in the cache after this cycle.
		wantNewAssignments map[workload.Reference]kueue.Admission
		// wantLeft is the workload keys that are left in the queues after this cycle.
		wantLeft map[kueue.ClusterQueueReference][]workload.Reference
		// wantInadmissibleLeft is the workload keys that are left in the inadmissible state after this cycle.
		wantInadmissibleLeft map[kueue.ClusterQueueReference][]workload.Reference
		// wantEvents asserts on the events, the comparison options are passed by eventCmpOpts
		wantEvents []utiltesting.EventRecord
		// eventCmpOpts are the comparison options for the events
		eventCmpOpts cmp.Options

		featureGates []featuregate.Feature
	}{
		"workload with a PodSet of size zero": {
			nodes: []corev1.Node{
				*testingnode.MakeNode("x1").
					Label("tas-node", "true").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("3"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			topologies:      []kueuealpha.Topology{defaultTwoLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASTwoLevelFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(
						*utiltesting.MakePodSet("launcher", 1).
							PreferredTopologyRequest(corev1.LabelHostname).
							Request(corev1.ResourceCPU, "1").
							Obj(),
						*utiltesting.MakePodSet("worker", 0).
							PreferredTopologyRequest(corev1.LabelHostname).
							Request(corev1.ResourceCPU, "1").
							Obj()).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltesting.MakeAdmission("tas-main", "launcher", "worker").
					Assignment(corev1.ResourceCPU, "tas-default", "1000m").
					AssignmentPodCount(1).
					TopologyAssignment(&kueue.TopologyAssignment{
						Levels: utiltas.Levels(&defaultSingleLevelTopology),
						Domains: []kueue.TopologyDomainAssignment{
							{
								Count: 1,
								Values: []string{
									"x1",
								},
							},
						},
					}).
					AssignmentWithIndex(1, corev1.ResourceCPU, "tas-default", "0").
					AssignmentPodCountWithIndex(1, 0).
					TopologyAssignmentWithIndex(1, &kueue.TopologyAssignment{
						Levels: utiltas.Levels(&defaultSingleLevelTopology),
					}).
					Obj(),
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "foo"},
					Reason:    "QuotaReserved",
					EventType: corev1.EventTypeNormal,
				},
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "foo"},
					Reason:    "Admitted",
					EventType: corev1.EventTypeNormal,
				},
			},
		},
		"workload in CQ with ProvisioningRequest; second pass; baseline scenario": {
			nodes:           defaultSingleNode,
			admissionChecks: []kueue.AdmissionCheck{defaultProvCheck},
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{clusterQueueWithProvReq},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(*utiltesting.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuota(
						utiltesting.MakeAdmission("tas-main", "one").
							Assignment(corev1.ResourceCPU, "tas-default", "1000m").
							DelayedTopologyRequest(kueue.DelayedTopologyRequestStatePending).
							AssignmentPodCount(1).Obj(),
					).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "prov-check",
						State: kueue.CheckStateReady,
					}).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltesting.MakeAdmission("tas-main", "one").
					Assignment(corev1.ResourceCPU, "tas-default", "1000m").
					AssignmentPodCount(1).
					DelayedTopologyRequest(kueue.DelayedTopologyRequestStateReady).
					TopologyAssignment(&kueue.TopologyAssignment{
						Levels: utiltas.Levels(&defaultSingleLevelTopology),
						Domains: []kueue.TopologyDomainAssignment{
							{
								Count: 1,
								Values: []string{
									"x1",
								},
							},
						},
					}).Obj(),
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "foo"},
					Reason:    "Admitted",
					EventType: corev1.EventTypeNormal,
				},
			},
		},
		"workload in CQ with two TAS flavors, only the second is using Provisioning Admission Check": {
			nodes: []corev1.Node{
				*testingnode.MakeNode("x1").
					Label("tas-group", "reservation").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			admissionChecks: []kueue.AdmissionCheck{defaultProvCheck},
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{
				*utiltesting.MakeResourceFlavor("tas-reservation").
					NodeLabel("tas-group", "reservation").
					TopologyName("tas-single-level").
					Obj(),
				*utiltesting.MakeResourceFlavor("tas-provisioning").
					NodeLabel("tas-group", "provisioning").
					TopologyName("tas-single-level").
					Obj()},
			clusterQueues: []kueue.ClusterQueue{
				*utiltesting.MakeClusterQueue("tas-main").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("tas-reservation").
							Resource(corev1.ResourceCPU, "1").Obj(),
						*utiltesting.MakeFlavorQuotas("tas-provisioning").
							Resource(corev1.ResourceCPU, "1").Obj()).
					AdmissionCheckStrategy(kueue.AdmissionCheckStrategyRule{
						Name: "prov-check",
						OnFlavors: []kueue.ResourceFlavorReference{
							kueue.ResourceFlavorReference("tas-provisioning"),
						},
					}).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(*utiltesting.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltesting.MakeAdmission("tas-main", "one").
					Assignment(corev1.ResourceCPU, "tas-reservation", "1000m").
					AssignmentPodCount(1).
					TopologyAssignment(&kueue.TopologyAssignment{
						Levels: utiltas.Levels(&defaultSingleLevelTopology),
						Domains: []kueue.TopologyDomainAssignment{
							{
								Count: 1,
								Values: []string{
									"x1",
								},
							},
						},
					}).Obj(),
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "foo"},
					Reason:    "QuotaReserved",
					EventType: corev1.EventTypeNormal,
				},
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "foo"},
					Reason:    "Admitted",
					EventType: corev1.EventTypeNormal,
				},
			},
		},
		"workload with nodeToReplace annotation; second pass; baseline scenario": {
			nodes:           defaultNodes,
			admissionChecks: []kueue.AdmissionCheck{defaultProvCheck},
			topologies:      []kueuealpha.Topology{defaultThreeLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASThreeLevelFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Annotations(map[string]string{kueuealpha.NodeToReplaceAnnotation: "x0"}).
					Queue("tas-main").
					PodSets(*utiltesting.MakePodSet("one", 1).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuota(
						utiltesting.MakeAdmission("tas-main", "one").
							Assignment(corev1.ResourceCPU, "tas-default", "1000m").
							AssignmentPodCount(1).
							TopologyAssignment(&kueue.TopologyAssignment{
								Levels: utiltas.Levels(&defaultSingleLevelTopology),
								Domains: []kueue.TopologyDomainAssignment{
									{
										Count: 1,
										Values: []string{
											"x0",
										},
									},
								},
							}).
							Obj(),
					).
					Admitted(true).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltesting.MakeAdmission("tas-main", "one").
					Assignment(corev1.ResourceCPU, "tas-default", "1000m").
					AssignmentPodCount(1).
					TopologyAssignment(&kueue.TopologyAssignment{
						Levels: utiltas.Levels(&defaultSingleLevelTopology),
						Domains: []kueue.TopologyDomainAssignment{
							{
								Count: 1,
								Values: []string{
									"x1",
								},
							},
						},
					}).Obj(),
			},
		},
		"workload with nodeToReplace annotation; second pass; preferred; fit in different rack": {
			nodes:           defaultNodes,
			admissionChecks: []kueue.AdmissionCheck{defaultProvCheck},
			topologies:      []kueuealpha.Topology{defaultThreeLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASThreeLevelFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Annotations(map[string]string{kueuealpha.NodeToReplaceAnnotation: "x0"}).
					Queue("tas-main").
					PodSets(*utiltesting.MakePodSet("one", 2).
						PreferredTopologyRequest(tasRackLabel).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuota(
						utiltesting.MakeAdmission("tas-main", "one").
							Assignment(corev1.ResourceCPU, "tas-default", "1000m").
							AssignmentPodCount(1).
							TopologyAssignment(&kueue.TopologyAssignment{
								Levels: utiltas.Levels(&defaultSingleLevelTopology),
								Domains: []kueue.TopologyDomainAssignment{
									{
										Count: 1,
										Values: []string{
											"x0",
										},
									},
									{
										Count: 1,
										Values: []string{
											"x1",
										},
									},
								},
							}).
							Obj(),
					).
					Admitted(true).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltesting.MakeAdmission("tas-main", "one").
					Assignment(corev1.ResourceCPU, "tas-default", "1000m").
					AssignmentPodCount(1).
					TopologyAssignment(&kueue.TopologyAssignment{
						Levels: utiltas.Levels(&defaultSingleLevelTopology),
						Domains: []kueue.TopologyDomainAssignment{
							{
								Count: 1,
								Values: []string{
									"x1",
								},
							},
							{
								Count: 1,
								Values: []string{
									"x5",
								},
							},
						},
					}).Obj(),
			},
		},
		"workload with nodeToReplace annotation; second pass; preferred; no fit": {
			nodes:           defaultNodes,
			admissionChecks: []kueue.AdmissionCheck{defaultProvCheck},
			topologies:      []kueuealpha.Topology{defaultThreeLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASThreeLevelFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Annotations(map[string]string{kueuealpha.NodeToReplaceAnnotation: "x0"}).
					Queue("tas-main").
					PodSets(*utiltesting.MakePodSet("one", 1).
						PreferredTopologyRequest(tasRackLabel).
						Request(corev1.ResourceCPU, "3").
						Obj()).
					ReserveQuota(
						utiltesting.MakeAdmission("tas-main", "one").
							Assignment(corev1.ResourceCPU, "tas-default", "3000m").
							AssignmentPodCount(1).
							TopologyAssignment(&kueue.TopologyAssignment{
								Levels: utiltas.Levels(&defaultSingleLevelTopology),
								Domains: []kueue.TopologyDomainAssignment{
									{
										Count: 1,
										Values: []string{
											"x0",
										},
									},
								},
							}).
							Obj(),
					).
					Admitted(true).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltesting.MakeAdmission("tas-main", "one").
					Assignment(corev1.ResourceCPU, "tas-default", "3000m").
					AssignmentPodCount(1).
					TopologyAssignment(&kueue.TopologyAssignment{
						Levels: utiltas.Levels(&defaultSingleLevelTopology),
						Domains: []kueue.TopologyDomainAssignment{
							{
								Count: 1,
								Values: []string{
									"x0",
								},
							},
						},
					}).Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "foo"},
					EventType: corev1.EventTypeWarning,
					Reason:    "SecondPassFailed",
					Message:   "couldn't assign flavors to pod set one: topology \"tas-three-level\" doesn't allow to fit any of 1 pod(s)",
				},
			},
		},
		"workload with nodeToReplace annotation; second pass; preferred; no fit; FailFast": {
			nodes:           defaultNodes,
			admissionChecks: []kueue.AdmissionCheck{defaultProvCheck},
			topologies:      []kueuealpha.Topology{defaultThreeLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASThreeLevelFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Annotations(map[string]string{kueuealpha.NodeToReplaceAnnotation: "x0"}).
					Queue("tas-main").
					PodSets(*utiltesting.MakePodSet("one", 1).
						PreferredTopologyRequest(tasRackLabel).
						Request(corev1.ResourceCPU, "3").
						Obj()).
					ReserveQuota(
						utiltesting.MakeAdmission("tas-main", "one").
							Assignment(corev1.ResourceCPU, "tas-default", "3000m").
							AssignmentPodCount(1).
							TopologyAssignment(&kueue.TopologyAssignment{
								Levels: utiltas.Levels(&defaultSingleLevelTopology),
								Domains: []kueue.TopologyDomainAssignment{
									{
										Count: 1,
										Values: []string{
											"x0",
										},
									},
								},
							}).
							Obj(),
					).
					Admitted(true).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltesting.MakeAdmission("tas-main", "one").
					Assignment(corev1.ResourceCPU, "tas-default", "3000m").
					AssignmentPodCount(1).
					TopologyAssignment(&kueue.TopologyAssignment{
						Levels: utiltas.Levels(&defaultSingleLevelTopology),
						Domains: []kueue.TopologyDomainAssignment{
							{
								Count: 1,
								Values: []string{
									"x0",
								},
							},
						},
					}).Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "foo"},
					EventType: corev1.EventTypeNormal,
					Reason:    "EvictedDueToNodeFailures",
					Message:   "Workload was evicted as there was no replacement for a failed node: x0",
				},
			},
			featureGates: []featuregate.Feature{features.TASFailedNodeReplacementFailFast},
		},
		"workload with nodeToReplace annotation; second pass; required rack; fit": {
			nodes:           defaultNodes,
			admissionChecks: []kueue.AdmissionCheck{defaultProvCheck},
			topologies:      []kueuealpha.Topology{defaultThreeLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASThreeLevelFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Annotations(map[string]string{kueuealpha.NodeToReplaceAnnotation: "x0"}).
					Queue("tas-main").
					PodSets(*utiltesting.MakePodSet("one", 2).
						RequiredTopologyRequest(tasRackLabel).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuota(
						utiltesting.MakeAdmission("tas-main", "one").
							Assignment(corev1.ResourceCPU, "tas-default", "1000m").
							AssignmentPodCount(1).
							TopologyAssignment(&kueue.TopologyAssignment{
								Levels: utiltas.Levels(&defaultSingleLevelTopology),
								Domains: []kueue.TopologyDomainAssignment{
									{
										Count: 1,
										Values: []string{
											"x0",
										},
									},
									{
										Count: 1,
										Values: []string{
											"x2",
										},
									},
								},
							}).
							Obj(),
					).
					Admitted(true).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltesting.MakeAdmission("tas-main", "one").
					Assignment(corev1.ResourceCPU, "tas-default", "1000m").
					AssignmentPodCount(1).
					TopologyAssignment(&kueue.TopologyAssignment{
						Levels: utiltas.Levels(&defaultSingleLevelTopology),
						Domains: []kueue.TopologyDomainAssignment{
							{
								Count: 1,
								Values: []string{
									"x2",
								},
							},
							{
								Count: 1,
								Values: []string{
									"x3",
								},
							},
						},
					}).Obj(),
			},
		},
		"workload with nodeToReplace annotation; second pass; required rack for a single node; fit": {
			nodes:           defaultNodes,
			admissionChecks: []kueue.AdmissionCheck{defaultProvCheck},
			topologies:      []kueuealpha.Topology{defaultThreeLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASThreeLevelFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Annotations(map[string]string{kueuealpha.NodeToReplaceAnnotation: "x0"}).
					Queue("tas-main").
					PodSets(*utiltesting.MakePodSet("one", 1).
						RequiredTopologyRequest(tasRackLabel).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuota(
						utiltesting.MakeAdmission("tas-main", "one").
							Assignment(corev1.ResourceCPU, "tas-default", "1000m").
							AssignmentPodCount(1).
							TopologyAssignment(&kueue.TopologyAssignment{
								Levels: utiltas.Levels(&defaultSingleLevelTopology),
								Domains: []kueue.TopologyDomainAssignment{
									{
										Count: 1,
										Values: []string{
											"x0",
										},
									},
								},
							}).
							Obj(),
					).
					Admitted(true).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltesting.MakeAdmission("tas-main", "one").
					Assignment(corev1.ResourceCPU, "tas-default", "1000m").
					AssignmentPodCount(1).
					TopologyAssignment(&kueue.TopologyAssignment{
						Levels: utiltas.Levels(&defaultSingleLevelTopology),
						Domains: []kueue.TopologyDomainAssignment{
							{
								Count: 1,
								Values: []string{
									"x1",
								},
							},
						},
					}).Obj(),
			},
		},
		"workload with nodeToReplace annotation; second pass; required rack; no fit": {
			nodes:           defaultNodes,
			admissionChecks: []kueue.AdmissionCheck{defaultProvCheck},
			topologies:      []kueuealpha.Topology{defaultThreeLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASThreeLevelFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Annotations(map[string]string{kueuealpha.NodeToReplaceAnnotation: "x0"}).
					Queue("tas-main").
					PodSets(*utiltesting.MakePodSet("one", 2).
						RequiredTopologyRequest(tasRackLabel).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuota(
						utiltesting.MakeAdmission("tas-main", "one").
							Assignment(corev1.ResourceCPU, "tas-default", "1000m").
							AssignmentPodCount(1).
							TopologyAssignment(&kueue.TopologyAssignment{
								Levels: utiltas.Levels(&defaultSingleLevelTopology),
								Domains: []kueue.TopologyDomainAssignment{
									{
										Count: 1,
										Values: []string{
											"x0",
										},
									},
									{
										Count: 1,
										Values: []string{
											"x1",
										},
									},
								},
							}).
							Obj(),
					).
					Admitted(true).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltesting.MakeAdmission("tas-main", "one").
					Assignment(corev1.ResourceCPU, "tas-default", "1000m").
					AssignmentPodCount(1).
					TopologyAssignment(&kueue.TopologyAssignment{
						Levels: utiltas.Levels(&defaultSingleLevelTopology),
						Domains: []kueue.TopologyDomainAssignment{
							{
								Count: 1,
								Values: []string{
									"x0",
								},
							},
							{
								Count: 1,
								Values: []string{
									"x1",
								},
							},
						},
					}).Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "foo"},
					EventType: corev1.EventTypeWarning,
					Reason:    "SecondPassFailed",
					Message:   "couldn't assign flavors to pod set one: topology \"tas-three-level\" doesn't allow to fit any of 1 pod(s)",
				},
			},
		},
		"workload with nodeToReplace annotation; second pass; two podsets": {
			nodes:           defaultNodes,
			admissionChecks: []kueue.AdmissionCheck{defaultProvCheck},
			topologies:      []kueuealpha.Topology{defaultThreeLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASThreeLevelFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Annotations(map[string]string{kueuealpha.NodeToReplaceAnnotation: "x0"}).
					Queue("tas-main").
					PodSets(
						*utiltesting.MakePodSet("one", 1).
							PreferredTopologyRequest(corev1.LabelHostname).
							Request(corev1.ResourceCPU, "1").
							Obj(),
						*utiltesting.MakePodSet("two", 1).
							PreferredTopologyRequest(corev1.LabelHostname).
							Request(corev1.ResourceCPU, "1").
							Obj()).
					ReserveQuota(
						utiltesting.MakeAdmission("tas-main", "one", "two").
							Assignment(corev1.ResourceCPU, "tas-default", "1000m").
							AssignmentPodCount(1).
							TopologyAssignment(&kueue.TopologyAssignment{
								Levels: utiltas.Levels(&defaultSingleLevelTopology),
								Domains: []kueue.TopologyDomainAssignment{
									{
										Count: 1,
										Values: []string{
											"x0",
										},
									},
								},
							}).
							AssignmentWithIndex(1, corev1.ResourceCPU, "tas-default", "1000m").
							AssignmentPodCountWithIndex(1, 1).
							TopologyAssignmentWithIndex(1, &kueue.TopologyAssignment{
								Levels: utiltas.Levels(&defaultSingleLevelTopology),
								Domains: []kueue.TopologyDomainAssignment{
									{
										Count: 1,
										Values: []string{
											"x1",
										},
									},
								},
							}).
							Obj(),
					).
					Admitted(true).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltesting.MakeAdmission("tas-main", "one", "two").
					Assignment(corev1.ResourceCPU, "tas-default", "1000m").
					AssignmentPodCount(1).
					TopologyAssignment(&kueue.TopologyAssignment{
						Levels: utiltas.Levels(&defaultSingleLevelTopology),
						Domains: []kueue.TopologyDomainAssignment{
							{
								Count: 1,
								Values: []string{
									"x2",
								},
							},
						},
					}).
					AssignmentWithIndex(1, corev1.ResourceCPU, "tas-default", "1000m").
					AssignmentPodCountWithIndex(1, 1).
					TopologyAssignmentWithIndex(1, &kueue.TopologyAssignment{
						Levels: utiltas.Levels(&defaultSingleLevelTopology),
						Domains: []kueue.TopologyDomainAssignment{
							{
								Count: 1,
								Values: []string{
									"x1",
								},
							},
						},
					}).
					Obj(),
			},
		},
		"large workload in CQ with ProvisioningRequest; second pass": {
			// In this scenario we test a workload which is using 26 out of 50
			// available units of quota, to make sure we are not double counting
			// the quota in the second pass.
			nodes: []corev1.Node{
				*testingnode.MakeNode("x1").
					Label("tas-node", "true").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("50"),
						corev1.ResourceMemory: resource.MustParse("50Gi"),
						corev1.ResourcePods:   resource.MustParse("50"),
					}).
					Ready().
					Obj(),
			},
			admissionChecks: []kueue.AdmissionCheck{defaultProvCheck},
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{clusterQueueWithProvReq},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(*utiltesting.MakePodSet("one", 26).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuota(
						utiltesting.MakeAdmission("tas-main", "one").
							Assignment(corev1.ResourceCPU, "tas-default", "26").
							DelayedTopologyRequest(kueue.DelayedTopologyRequestStatePending).
							AssignmentPodCount(26).Obj(),
					).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "prov-check",
						State: kueue.CheckStateReady,
					}).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltesting.MakeAdmission("tas-main", "one").
					Assignment(corev1.ResourceCPU, "tas-default", "26").
					AssignmentPodCount(26).
					DelayedTopologyRequest(kueue.DelayedTopologyRequestStateReady).
					TopologyAssignment(&kueue.TopologyAssignment{
						Levels: utiltas.Levels(&defaultSingleLevelTopology),
						Domains: []kueue.TopologyDomainAssignment{
							{
								Count: 26,
								Values: []string{
									"x1",
								},
							},
						},
					}).Obj(),
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "foo"},
					Reason:    "Admitted",
					EventType: corev1.EventTypeNormal,
				},
			},
		},
		"workload in CQ with ProvisioningRequest when two TAS flavors; second pass": {
			nodes: []corev1.Node{
				*testingnode.MakeNode("x1").
					Label("tas-node", "true").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("y1").
					Label("tas-node-second", "true").
					Label(corev1.LabelHostname, "y1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			admissionChecks: []kueue.AdmissionCheck{defaultProvCheck},
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor,
				*utiltesting.MakeResourceFlavor("tas-second").
					NodeLabel("tas-node-second", "true").
					TopologyName("tas-single-level").
					Obj()},
			clusterQueues: []kueue.ClusterQueue{
				*utiltesting.MakeClusterQueue("tas-main").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("tas-default").
							Resource(corev1.ResourceCPU, "50").Obj(),
						*utiltesting.MakeFlavorQuotas("tas-second").
							Resource(corev1.ResourceCPU, "50").Obj()).
					AdmissionChecks(kueue.AdmissionCheckReference(defaultProvCheck.Name)).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(*utiltesting.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuota(
						utiltesting.MakeAdmission("tas-main", "one").
							Assignment(corev1.ResourceCPU, "tas-second", "1000m").
							DelayedTopologyRequest(kueue.DelayedTopologyRequestStatePending).
							AssignmentPodCount(1).Obj(),
					).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "prov-check",
						State: kueue.CheckStateReady,
					}).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltesting.MakeAdmission("tas-main", "one").
					Assignment(corev1.ResourceCPU, "tas-second", "1000m").
					AssignmentPodCount(1).
					DelayedTopologyRequest(kueue.DelayedTopologyRequestStateReady).
					TopologyAssignment(&kueue.TopologyAssignment{
						Levels: utiltas.Levels(&defaultSingleLevelTopology),
						Domains: []kueue.TopologyDomainAssignment{
							{
								Count: 1,
								Values: []string{
									"y1",
								},
							},
						},
					}).Obj(),
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "foo"},
					Reason:    "Admitted",
					EventType: corev1.EventTypeNormal,
				},
			},
		},
		"workload in CQ with two TAS flavors - one with ProvisioningRequest, one regular; second pass": {
			nodes: []corev1.Node{
				*testingnode.MakeNode("x1").
					Label("tas-node", "true").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("y1").
					Label("tas-node-second", "true").
					Label(corev1.LabelHostname, "y1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			admissionChecks: []kueue.AdmissionCheck{defaultProvCheck},
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor,
				*utiltesting.MakeResourceFlavor("tas-second").
					NodeLabel("tas-node-second", "true").
					TopologyName("tas-single-level").
					Obj()},
			clusterQueues: []kueue.ClusterQueue{
				*utiltesting.MakeClusterQueue("tas-main").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("tas-default").
							Resource(corev1.ResourceCPU, "50").Obj(),
						*utiltesting.MakeFlavorQuotas("tas-second").
							Resource(corev1.ResourceCPU, "50").Obj()).
					AdmissionCheckStrategy(kueue.AdmissionCheckStrategyRule{
						Name: kueue.AdmissionCheckReference(defaultProvCheck.Name),
						OnFlavors: []kueue.ResourceFlavorReference{
							"tas-second",
						},
					}).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(
						*utiltesting.MakePodSet("one", 1).
							RequiredTopologyRequest(corev1.LabelHostname).
							Request(corev1.ResourceCPU, "1").
							Obj(),
						*utiltesting.MakePodSet("two", 1).
							RequiredTopologyRequest(corev1.LabelHostname).
							Request(corev1.ResourceCPU, "1").
							Obj()).
					ReserveQuota(
						utiltesting.MakeAdmission("tas-main", "one", "two").
							PodSets(
								kueue.PodSetAssignment{
									Name: "one",
									Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
										corev1.ResourceCPU: "tas-default",
									},
									ResourceUsage: corev1.ResourceList{
										corev1.ResourceCPU: resource.MustParse("1"),
									},
									Count: ptr.To[int32](1),
									TopologyAssignment: &kueue.TopologyAssignment{
										Levels: utiltas.Levels(&defaultSingleLevelTopology),
										Domains: []kueue.TopologyDomainAssignment{
											{
												Count: 1,
												Values: []string{
													"x1",
												},
											},
										},
									},
								},
								kueue.PodSetAssignment{
									Name: "two",
									Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
										corev1.ResourceCPU: "tas-second",
									},
									ResourceUsage: corev1.ResourceList{
										corev1.ResourceCPU: resource.MustParse("1"),
									},
									Count:                  ptr.To[int32](1),
									DelayedTopologyRequest: ptr.To(kueue.DelayedTopologyRequestStatePending),
								},
							).Obj(),
					).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "prov-check",
						State: kueue.CheckStateReady,
					}).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltesting.MakeAdmission("tas-main", "one", "two").
					PodSets(
						kueue.PodSetAssignment{
							Name: "one",
							Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
								corev1.ResourceCPU: "tas-default",
							},
							ResourceUsage: corev1.ResourceList{
								corev1.ResourceCPU: resource.MustParse("1"),
							},
							Count: ptr.To[int32](1),
							TopologyAssignment: &kueue.TopologyAssignment{
								Levels: utiltas.Levels(&defaultSingleLevelTopology),
								Domains: []kueue.TopologyDomainAssignment{
									{
										Count: 1,
										Values: []string{
											"x1",
										},
									},
								},
							},
						},
						kueue.PodSetAssignment{
							Name: "two",
							Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
								corev1.ResourceCPU: "tas-second",
							},
							ResourceUsage: corev1.ResourceList{
								corev1.ResourceCPU: resource.MustParse("1"),
							},
							Count:                  ptr.To[int32](1),
							DelayedTopologyRequest: ptr.To(kueue.DelayedTopologyRequestStateReady),
							TopologyAssignment: &kueue.TopologyAssignment{
								Levels: utiltas.Levels(&defaultSingleLevelTopology),
								Domains: []kueue.TopologyDomainAssignment{
									{
										Count: 1,
										Values: []string{
											"y1",
										},
									},
								},
							},
						},
					).Obj(),
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "foo"},
					Reason:    "Admitted",
					EventType: corev1.EventTypeNormal,
				},
			},
		},
		"workload in CQ with ProvisioningRequest gets QuotaReserved only; implicit defaulting": {
			nodes:           []corev1.Node{},
			admissionChecks: []kueue.AdmissionCheck{defaultProvCheck},
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{clusterQueueWithProvReq},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(*utiltesting.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "prov-check",
						State: kueue.CheckStatePending,
					}).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltesting.MakeAdmission("tas-main", "one").
					Assignment(corev1.ResourceCPU, "tas-default", "1000m").
					DelayedTopologyRequest(kueue.DelayedTopologyRequestStatePending).
					AssignmentPodCount(1).Obj(),
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "foo"},
					Reason:    "QuotaReserved",
					EventType: corev1.EventTypeNormal,
				},
			},
		},
		"workload in CQ with ProvisioningRequest when two TAS flavors; second pass; implicit defaulting": {
			nodes: []corev1.Node{
				*testingnode.MakeNode("x1").
					Label("tas-node", "true").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("y1").
					Label("tas-node-second", "true").
					Label(corev1.LabelHostname, "y1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			admissionChecks: []kueue.AdmissionCheck{defaultProvCheck},
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor,
				*utiltesting.MakeResourceFlavor("tas-second").
					NodeLabel("tas-node-second", "true").
					TopologyName("tas-single-level").
					Obj()},
			clusterQueues: []kueue.ClusterQueue{
				*utiltesting.MakeClusterQueue("tas-main").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("tas-default").
							Resource(corev1.ResourceCPU, "50").Obj(),
						*utiltesting.MakeFlavorQuotas("tas-second").
							Resource(corev1.ResourceCPU, "50").Obj()).
					AdmissionChecks(kueue.AdmissionCheckReference(defaultProvCheck.Name)).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(*utiltesting.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuota(
						utiltesting.MakeAdmission("tas-main", "one").
							Assignment(corev1.ResourceCPU, "tas-second", "1000m").
							DelayedTopologyRequest(kueue.DelayedTopologyRequestStatePending).
							AssignmentPodCount(1).Obj(),
					).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "prov-check",
						State: kueue.CheckStateReady,
					}).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltesting.MakeAdmission("tas-main", "one").
					Assignment(corev1.ResourceCPU, "tas-second", "1000m").
					AssignmentPodCount(1).
					DelayedTopologyRequest(kueue.DelayedTopologyRequestStateReady).
					TopologyAssignment(&kueue.TopologyAssignment{
						Levels: utiltas.Levels(&defaultSingleLevelTopology),
						Domains: []kueue.TopologyDomainAssignment{
							{
								Count: 1,
								Values: []string{
									"y1",
								},
							},
						},
					}).Obj(),
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "foo"},
					Reason:    "Admitted",
					EventType: corev1.EventTypeNormal,
				},
			},
		},
		"workload in CQ with ProvisioningRequest gets QuotaReserved only": {
			nodes:           []corev1.Node{},
			admissionChecks: []kueue.AdmissionCheck{defaultProvCheck},
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{clusterQueueWithProvReq},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(*utiltesting.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "prov-check",
						State: kueue.CheckStatePending,
					}).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltesting.MakeAdmission("tas-main", "one").
					Assignment(corev1.ResourceCPU, "tas-default", "1000m").
					DelayedTopologyRequest(kueue.DelayedTopologyRequestStatePending).
					AssignmentPodCount(1).Obj(),
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "foo"},
					Reason:    "QuotaReserved",
					EventType: corev1.EventTypeNormal,
				},
			},
		},
		"workload with a custom AdmissionCheck gets TAS assigned": {
			nodes:           defaultSingleNode,
			admissionChecks: []kueue.AdmissionCheck{defaultCustomCheck},
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{clusterQueueWithCustomCheck},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(*utiltesting.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "custom-check",
						State: kueue.CheckStatePending,
					}).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltesting.MakeAdmission("tas-main", "one").
					Assignment(corev1.ResourceCPU, "tas-default", "1000m").
					AssignmentPodCount(1).
					TopologyAssignment(&kueue.TopologyAssignment{
						Levels: utiltas.Levels(&defaultSingleLevelTopology),
						Domains: []kueue.TopologyDomainAssignment{
							{
								Count: 1,
								Values: []string{
									"x1",
								},
							},
						},
					}).Obj(),
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "foo"},
					Reason:    "QuotaReserved",
					EventType: corev1.EventTypeNormal,
				},
			},
		},
		"workload which does not specify TAS annotation uses the only TAS flavor": {
			nodes:           defaultSingleNode,
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor, defaultFlavor},
			clusterQueues: []kueue.ClusterQueue{
				*utiltesting.MakeClusterQueue("tas-main").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("tas-default").
							Resource(corev1.ResourceCPU, "50").Obj()).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(*utiltesting.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltesting.MakeAdmission("tas-main", "one").
					Assignment(corev1.ResourceCPU, "tas-default", "1000m").
					AssignmentPodCount(1).
					TopologyAssignment(&kueue.TopologyAssignment{
						Levels: utiltas.Levels(&defaultSingleLevelTopology),
						Domains: []kueue.TopologyDomainAssignment{
							{
								Count: 1,
								Values: []string{
									"x1",
								},
							},
						},
					}).Obj(),
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "foo"},
					Reason:    "QuotaReserved",
					EventType: corev1.EventTypeNormal,
				},
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "foo"},
					Reason:    "Admitted",
					EventType: corev1.EventTypeNormal,
				},
			},
		},
		"workload requiring TAS skips the non-TAS flavor": {
			nodes:           defaultSingleNode,
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor, defaultFlavor},
			clusterQueues: []kueue.ClusterQueue{
				*utiltesting.MakeClusterQueue("tas-main").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("default").
							Resource(corev1.ResourceCPU, "50").Obj(),
						*utiltesting.MakeFlavorQuotas("tas-default").
							Resource(corev1.ResourceCPU, "50").Obj()).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(*utiltesting.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltesting.MakeAdmission("tas-main", "one").
					Assignment(corev1.ResourceCPU, "tas-default", "1000m").
					AssignmentPodCount(1).
					TopologyAssignment(&kueue.TopologyAssignment{
						Levels: utiltas.Levels(&defaultSingleLevelTopology),
						Domains: []kueue.TopologyDomainAssignment{
							{
								Count: 1,
								Values: []string{
									"x1",
								},
							},
						},
					}).Obj(),
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "foo"},
					Reason:    "QuotaReserved",
					EventType: corev1.EventTypeNormal,
				},
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "foo"},
					Reason:    "Admitted",
					EventType: corev1.EventTypeNormal,
				},
			},
		},
		"workload which does not need TAS skips the TAS flavor": {
			nodes:           defaultSingleNode,
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor, defaultFlavor},
			clusterQueues: []kueue.ClusterQueue{
				*utiltesting.MakeClusterQueue("tas-main").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("tas-default").
							Resource(corev1.ResourceCPU, "50").Obj(),
						*utiltesting.MakeFlavorQuotas("default").
							Resource(corev1.ResourceCPU, "50").Obj()).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(*utiltesting.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltesting.MakeAdmission("tas-main", "one").
					Assignment(corev1.ResourceCPU, "default", "1000m").
					AssignmentPodCount(1).
					Obj(),
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "foo"},
					Reason:    "QuotaReserved",
					EventType: corev1.EventTypeNormal,
				},
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "foo"},
					Reason:    "Admitted",
					EventType: corev1.EventTypeNormal,
				},
			},
		},
		"workload with mixed PodSets (requiring TAS and not)": {
			nodes:           defaultSingleNode,
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor, defaultFlavor},
			clusterQueues: []kueue.ClusterQueue{
				*utiltesting.MakeClusterQueue("tas-main").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("tas-default").
							Resource(corev1.ResourceCPU, "50").Obj(),
						*utiltesting.MakeFlavorQuotas("default").
							Resource(corev1.ResourceCPU, "50").Obj()).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(
						*utiltesting.MakePodSet("launcher", 1).
							Request(corev1.ResourceCPU, "500m").
							Obj(),
						*utiltesting.MakePodSet("worker", 1).
							RequiredTopologyRequest(corev1.LabelHostname).
							Request(corev1.ResourceCPU, "500m").
							Obj()).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltesting.MakeAdmission("tas-main", "one").
					PodSets(
						kueue.PodSetAssignment{
							Name: "launcher",
							Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
								corev1.ResourceCPU: "default",
							},
							ResourceUsage: corev1.ResourceList{
								corev1.ResourceCPU: resource.MustParse("500m"),
							},
							Count: ptr.To[int32](1),
						},
						kueue.PodSetAssignment{
							Name: "worker",
							Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
								corev1.ResourceCPU: "tas-default",
							},
							ResourceUsage: corev1.ResourceList{
								corev1.ResourceCPU: resource.MustParse("500m"),
							},
							Count: ptr.To[int32](1),
							TopologyAssignment: &kueue.TopologyAssignment{
								Levels: utiltas.Levels(&defaultSingleLevelTopology),
								Domains: []kueue.TopologyDomainAssignment{
									{
										Count: 1,
										Values: []string{
											"x1",
										},
									},
								},
							},
						},
					).
					Obj(),
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "foo"},
					Reason:    "QuotaReserved",
					EventType: corev1.EventTypeNormal,
				},
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "foo"},
					Reason:    "Admitted",
					EventType: corev1.EventTypeNormal,
				},
			},
		},
		"workload required TAS gets scheduled": {
			nodes:           defaultSingleNode,
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(*utiltesting.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltesting.MakeAdmission("tas-main", "one").
					Assignment(corev1.ResourceCPU, "tas-default", "1000m").
					AssignmentPodCount(1).
					TopologyAssignment(&kueue.TopologyAssignment{
						Levels: utiltas.Levels(&defaultSingleLevelTopology),
						Domains: []kueue.TopologyDomainAssignment{
							{
								Count: 1,
								Values: []string{
									"x1",
								},
							},
						},
					}).Obj(),
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "foo"},
					Reason:    "QuotaReserved",
					EventType: corev1.EventTypeNormal,
				},
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "foo"},
					Reason:    "Admitted",
					EventType: corev1.EventTypeNormal,
				},
			},
		},
		"workload requests topology level which is not present in topology": {
			nodes:           defaultSingleNode,
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(*utiltesting.MakePodSet("one", 1).
						RequiredTopologyRequest("cloud.com/non-existing").
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantInadmissibleLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"tas-main": {"default/foo"},
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "foo"},
					EventType: "Warning",
					Reason:    "Pending",
					Message:   `couldn't assign flavors to pod set one: Flavor "tas-default" does not contain the requested level`,
				},
			},
		},
		"workload requests topology level which is only present in second flavor": {
			nodes: []corev1.Node{
				*testingnode.MakeNode("x1").
					Label("tas-node", "true").
					Label("cloud.com/custom-level", "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			topologies: []kueuealpha.Topology{defaultSingleLevelTopology,
				*utiltesting.MakeTopology("tas-custom-topology").
					Levels("cloud.com/custom-level").
					Obj(),
			},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor,
				*utiltesting.MakeResourceFlavor("tas-custom-flavor").
					NodeLabel("tas-node", "true").
					TopologyName("tas-custom-topology").
					Obj(),
			},
			clusterQueues: []kueue.ClusterQueue{
				*utiltesting.MakeClusterQueue("tas-main").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("tas-default").
							Resource(corev1.ResourceCPU, "50").Obj(),
						*utiltesting.MakeFlavorQuotas("tas-custom-flavor").
							Resource(corev1.ResourceCPU, "50").Obj()).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(*utiltesting.MakePodSet("one", 1).
						RequiredTopologyRequest("cloud.com/custom-level").
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltesting.MakeAdmission("tas-main", "one").
					Assignment(corev1.ResourceCPU, "tas-custom-flavor", "1").
					AssignmentPodCount(1).
					TopologyAssignment(&kueue.TopologyAssignment{
						Levels: []string{"cloud.com/custom-level"},
						Domains: []kueue.TopologyDomainAssignment{
							{
								Count: 1,
								Values: []string{
									"x1",
								},
							},
						},
					}).Obj(),
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "foo"},
					Reason:    "QuotaReserved",
					EventType: corev1.EventTypeNormal,
				},
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "foo"},
					Reason:    "Admitted",
					EventType: corev1.EventTypeNormal,
				},
			},
		},
		"workload does not get scheduled as it does not fit within the node capacity": {
			nodes:           defaultSingleNode,
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(*utiltesting.MakePodSet("one", 2).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantInadmissibleLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"tas-main": {"default/foo"},
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "foo"},
					EventType: "Warning",
					Reason:    "Pending",
					Message:   `couldn't assign flavors to pod set one: topology "tas-single-level" allows to fit only 1 out of 2 pod(s)`,
				},
			},
		},
		"workload does not get scheduled as the node capacity is already used by another TAS workload": {
			nodes:           defaultSingleNode,
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(*utiltesting.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("bar-admitted", "default").
					Queue("tas-main").
					ReserveQuota(
						utiltesting.MakeAdmission("tas-main", "one").
							Assignment(corev1.ResourceCPU, "tas-default", "1000m").
							AssignmentPodCount(1).
							TopologyAssignment(&kueue.TopologyAssignment{
								Levels: utiltas.Levels(&defaultSingleLevelTopology),
								Domains: []kueue.TopologyDomainAssignment{
									{
										Count: 1,
										Values: []string{
											"x1",
										},
									},
								},
							}).Obj(),
					).
					Admitted(true).
					PodSets(*utiltesting.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantInadmissibleLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"tas-main": {"default/foo"},
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "foo"},
					EventType: "Warning",
					Reason:    "Pending",
					Message:   `couldn't assign flavors to pod set one: topology "tas-single-level" doesn't allow to fit any of 1 pod(s)`,
				},
			},
		},
		"workload does not get scheduled as the node capacity is already used by a non-TAS pod": {
			nodes: defaultSingleNode,
			pods: []corev1.Pod{
				*testingpod.MakePod("test-pending", "test-ns").NodeName("x1").
					StatusPhase(corev1.PodPending).
					Request(corev1.ResourceCPU, "600m").
					Obj(),
			},
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(*utiltesting.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantInadmissibleLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"tas-main": {"default/foo"},
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "foo"},
					EventType: "Warning",
					Reason:    "Pending",
					Message:   `couldn't assign flavors to pod set one: topology "tas-single-level" doesn't allow to fit any of 1 pod(s)`,
				},
			},
		},
		"workload gets scheduled as the usage of TAS pods and workloads is not double-counted": {
			nodes: defaultSingleNode,
			pods: []corev1.Pod{
				*testingpod.MakePod("test-running", "test-ns").NodeName("x1").
					StatusPhase(corev1.PodRunning).
					Request(corev1.ResourceCPU, "400m").
					NodeSelector(corev1.LabelHostname, "x1").
					Label(kueuealpha.TASLabel, "true").
					Obj(),
			},
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(*utiltesting.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "500m").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("bar-admitted", "default").
					Queue("tas-main").
					ReserveQuota(
						utiltesting.MakeAdmission("tas-main", "one").
							Assignment(corev1.ResourceCPU, "tas-default", "400m").
							AssignmentPodCount(1).
							TopologyAssignment(&kueue.TopologyAssignment{
								Levels: utiltas.Levels(&defaultSingleLevelTopology),
								Domains: []kueue.TopologyDomainAssignment{
									{
										Count: 1,
										Values: []string{
											"x1",
										},
									},
								},
							}).Obj(),
					).
					Admitted(true).
					PodSets(*utiltesting.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "400m").
						Obj()).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltesting.MakeAdmission("tas-main", "one").
					Assignment(corev1.ResourceCPU, "tas-default", "500m").
					AssignmentPodCount(1).
					TopologyAssignment(&kueue.TopologyAssignment{
						Levels: utiltas.Levels(&defaultSingleLevelTopology),
						Domains: []kueue.TopologyDomainAssignment{
							{
								Count: 1,
								Values: []string{
									"x1",
								},
							},
						},
					}).Obj(),
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "foo"},
					Reason:    "QuotaReserved",
					EventType: corev1.EventTypeNormal,
				},
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "foo"},
					Reason:    "Admitted",
					EventType: corev1.EventTypeNormal,
				},
			},
		},
		"workload gets admitted next to already admitted workload, multiple resources used": {
			nodes:           defaultSingleNode,
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(*utiltesting.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "500m").
						Request(corev1.ResourceMemory, "500Mi").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("bar-admitted", "default").
					Queue("tas-main").
					ReserveQuota(
						utiltesting.MakeAdmission("tas-main", "one").
							Assignment(corev1.ResourceCPU, "tas-default", "500m").
							Assignment(corev1.ResourceMemory, "tas-default", "500Mi").
							AssignmentPodCount(1).
							TopologyAssignment(&kueue.TopologyAssignment{
								Levels: utiltas.Levels(&defaultSingleLevelTopology),
								Domains: []kueue.TopologyDomainAssignment{
									{
										Count: 1,
										Values: []string{
											"x1",
										},
									},
								},
							}).Obj(),
					).
					Admitted(true).
					PodSets(*utiltesting.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "500m").
						Request(corev1.ResourceMemory, "500Mi").
						Obj()).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltesting.MakeAdmission("tas-main", "one").
					Assignment(corev1.ResourceCPU, "tas-default", "500m").
					Assignment(corev1.ResourceMemory, "tas-default", "500Mi").
					AssignmentPodCount(1).
					TopologyAssignment(&kueue.TopologyAssignment{
						Levels: utiltas.Levels(&defaultSingleLevelTopology),
						Domains: []kueue.TopologyDomainAssignment{
							{
								Count: 1,
								Values: []string{
									"x1",
								},
							},
						},
					}).Obj(),
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "foo"},
					Reason:    "QuotaReserved",
					EventType: corev1.EventTypeNormal,
				},
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "foo"},
					Reason:    "Admitted",
					EventType: corev1.EventTypeNormal,
				},
			},
		},
		"workload with multiple PodSets requesting the same TAS flavor; multiple levels": {
			nodes: []corev1.Node{
				*testingnode.MakeNode("x1").
					Label("tas-node", "true").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("3"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("y1").
					Label("tas-node", "true").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "y1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("3"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			topologies:      []kueuealpha.Topology{defaultTwoLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASTwoLevelFlavor},
			clusterQueues: []kueue.ClusterQueue{
				*utiltesting.MakeClusterQueue("tas-main").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("tas-default").
							Resource(corev1.ResourceCPU, "50").Obj()).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(
						*utiltesting.MakePodSet("launcher", 1).
							PreferredTopologyRequest(corev1.LabelHostname).
							Request(corev1.ResourceCPU, "1").
							Obj(),
						*utiltesting.MakePodSet("worker", 1).
							PreferredTopologyRequest(corev1.LabelHostname).
							Request(corev1.ResourceCPU, "7").
							Obj()).
					Obj(),
			},
			wantInadmissibleLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"tas-main": {"default/foo"},
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "foo"},
					EventType: "Warning",
					Reason:    "Pending",
					Message:   `couldn't assign flavors to pod set worker: topology "tas-two-level" doesn't allow to fit any of 1 pod(s)`,
				},
			},
		},
		"scheduling workload with multiple PodSets requesting TAS flavor and will succeed": {
			nodes: []corev1.Node{
				*testingnode.MakeNode("x1").
					Label("tas-node", "true").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("8"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("y1").
					Label("tas-node", "true").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "y1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("8"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			topologies:      []kueuealpha.Topology{defaultTwoLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASTwoLevelFlavor},
			clusterQueues: []kueue.ClusterQueue{
				*utiltesting.MakeClusterQueue("tas-main").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("tas-default").
							Resource(corev1.ResourceCPU, "16").Obj()).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(
						*utiltesting.MakePodSet("launcher", 1).
							RequiredTopologyRequest(corev1.LabelHostname).
							Request(corev1.ResourceCPU, "1").
							Obj(),
						*utiltesting.MakePodSet("worker", 15).
							PreferredTopologyRequest(corev1.LabelHostname).
							Request(corev1.ResourceCPU, "1").
							Obj()).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltesting.MakeAdmission("tas-main", "launcher", "worker").
					AssignmentWithIndex(0, corev1.ResourceCPU, "tas-default", "1000m").
					AssignmentPodCountWithIndex(0, 1).
					TopologyAssignmentWithIndex(0, &kueue.TopologyAssignment{
						Levels: []string{corev1.LabelHostname},
						Domains: []kueue.TopologyDomainAssignment{
							{
								Count: 1,
								Values: []string{
									"x1",
								},
							},
						},
					}).
					AssignmentWithIndex(1, corev1.ResourceCPU, "tas-default", "15000m").
					AssignmentPodCountWithIndex(1, 15).
					TopologyAssignmentWithIndex(1, &kueue.TopologyAssignment{
						Levels: []string{corev1.LabelHostname},
						Domains: []kueue.TopologyDomainAssignment{
							{
								Count: 7,
								Values: []string{
									"x1",
								},
							},
							{
								Count: 8,
								Values: []string{
									"y1",
								},
							},
						},
					}).
					Obj(),
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "foo"},
					Reason:    "QuotaReserved",
					EventType: corev1.EventTypeNormal,
				},
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "foo"},
					Reason:    "Admitted",
					EventType: corev1.EventTypeNormal,
				},
			},
		},
		"scheduling workload with multiple PodSets requesting higher level topology": {
			nodes: []corev1.Node{
				*testingnode.MakeNode("x1").
					Label("tas-node", "true").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("8"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("y1").
					Label("tas-node", "true").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "y1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("8"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			topologies:      []kueuealpha.Topology{defaultTwoLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASTwoLevelFlavor},
			clusterQueues: []kueue.ClusterQueue{
				*utiltesting.MakeClusterQueue("tas-main").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("tas-default").
							Resource(corev1.ResourceCPU, "16").Obj()).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(
						*utiltesting.MakePodSet("launcher", 1).
							RequiredTopologyRequest(tasRackLabel).
							Request(corev1.ResourceCPU, "1").
							Obj(),
						*utiltesting.MakePodSet("worker", 15).
							RequiredTopologyRequest(tasRackLabel).
							Request(corev1.ResourceCPU, "1").
							Obj()).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltesting.MakeAdmission("tas-main", "launcher", "worker").
					AssignmentWithIndex(0, corev1.ResourceCPU, "tas-default", "1000m").
					AssignmentPodCountWithIndex(0, 1).
					TopologyAssignmentWithIndex(0, &kueue.TopologyAssignment{
						Levels: []string{corev1.LabelHostname},
						Domains: []kueue.TopologyDomainAssignment{
							{
								Count: 1,
								Values: []string{
									"x1",
								},
							},
						},
					}).
					AssignmentWithIndex(1, corev1.ResourceCPU, "tas-default", "15000m").
					AssignmentPodCountWithIndex(1, 15).
					TopologyAssignmentWithIndex(1, &kueue.TopologyAssignment{
						Levels: []string{corev1.LabelHostname},
						Domains: []kueue.TopologyDomainAssignment{
							{
								Count: 7,
								Values: []string{
									"x1",
								},
							},
							{
								Count: 8,
								Values: []string{
									"y1",
								},
							},
						},
					}).
					Obj(),
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "foo"},
					Reason:    "QuotaReserved",
					EventType: corev1.EventTypeNormal,
				},
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "foo"},
					Reason:    "Admitted",
					EventType: corev1.EventTypeNormal,
				},
			},
		},
		"scheduling workload when the node for another admitted workload is deleted": {
			// Here we have the "bar-admitted" workload, which is admitted and
			// is using the "x1" node, which is deleted. Still, we have the y1
			// node which allows to schedule the "foo" workload.
			nodes: []corev1.Node{
				*testingnode.MakeNode("y1").
					Label("tas-node", "true").
					Label(corev1.LabelHostname, "y1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues: []kueue.ClusterQueue{
				*utiltesting.MakeClusterQueue("tas-main").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("tas-default").
							Resource(corev1.ResourceCPU, "50").Obj()).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(
						*utiltesting.MakePodSet("one", 1).
							PreferredTopologyRequest(corev1.LabelHostname).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Obj(),
				*utiltesting.MakeWorkload("bar-admitted", "default").
					Queue("tas-main").
					ReserveQuota(
						utiltesting.MakeAdmission("tas-main", "one").
							Assignment(corev1.ResourceCPU, "tas-default", "1000m").
							AssignmentPodCount(1).
							TopologyAssignment(&kueue.TopologyAssignment{
								Levels: utiltas.Levels(&defaultSingleLevelTopology),
								Domains: []kueue.TopologyDomainAssignment{
									{
										Count: 1,
										Values: []string{
											"x1",
										},
									},
								},
							}).Obj(),
					).
					Admitted(true).
					PodSets(*utiltesting.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltesting.MakeAdmission("tas-main", "one").
					Assignment(corev1.ResourceCPU, "tas-default", "1000m").
					AssignmentPodCount(1).
					TopologyAssignment(&kueue.TopologyAssignment{
						Levels: utiltas.Levels(&defaultSingleLevelTopology),
						Domains: []kueue.TopologyDomainAssignment{
							{
								Count: 1,
								Values: []string{
									"y1",
								},
							},
						},
					}).Obj(),
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "foo"},
					Reason:    "QuotaReserved",
					EventType: corev1.EventTypeNormal,
				},
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "foo"},
					Reason:    "Admitted",
					EventType: corev1.EventTypeNormal,
				},
			},
		},
		"scheduling workload on a tainted node when the toleration is on ResourceFlavor": {
			nodes: []corev1.Node{
				*testingnode.MakeNode("x1").
					Label("tas-node", "true").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Taints(corev1.Taint{
						Key:    "example.com/gpu",
						Value:  "present",
						Effect: corev1.TaintEffectNoSchedule,
					}).
					Ready().
					Obj(),
			},
			topologies: []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{{
				ObjectMeta: metav1.ObjectMeta{
					Name: "tas-default",
				},
				Spec: kueue.ResourceFlavorSpec{
					NodeLabels: map[string]string{
						"tas-node": "true",
					},
					Tolerations: []corev1.Toleration{
						{
							Key:      "example.com/gpu",
							Operator: corev1.TolerationOpExists,
						},
					},
					TopologyName: ptr.To[kueue.TopologyReference]("tas-single-level"),
				},
			}},
			clusterQueues: []kueue.ClusterQueue{
				*utiltesting.MakeClusterQueue("tas-main").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("tas-default").
							Resource(corev1.ResourceCPU, "50").Obj()).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(
						*utiltesting.MakePodSet("one", 1).
							PreferredTopologyRequest(corev1.LabelHostname).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltesting.MakeAdmission("tas-main", "one").
					Assignment(corev1.ResourceCPU, "tas-default", "1000m").
					AssignmentPodCount(1).
					TopologyAssignment(&kueue.TopologyAssignment{
						Levels: utiltas.Levels(&defaultSingleLevelTopology),
						Domains: []kueue.TopologyDomainAssignment{
							{
								Count: 1,
								Values: []string{
									"x1",
								},
							},
						},
					}).Obj(),
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "foo"},
					Reason:    "QuotaReserved",
					EventType: corev1.EventTypeNormal,
				},
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "foo"},
					Reason:    "Admitted",
					EventType: corev1.EventTypeNormal,
				},
			},
		},
		"TAS workload gets scheduled as trimmed by partial admission": {
			nodes:           defaultSingleNode,
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(*utiltesting.MakePodSet("one", 5).
						SetMinimumCount(1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltesting.MakeAdmission("tas-main", "one").
					Assignment(corev1.ResourceCPU, "tas-default", "1").
					AssignmentPodCount(1).
					TopologyAssignment(&kueue.TopologyAssignment{
						Levels: utiltas.Levels(&defaultSingleLevelTopology),
						Domains: []kueue.TopologyDomainAssignment{
							{
								Count: 1,
								Values: []string{
									"x1",
								},
							},
						},
					}).Obj(),
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "foo"},
					Reason:    "QuotaReserved",
					EventType: corev1.EventTypeNormal,
				},
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "foo"},
					Reason:    "Admitted",
					EventType: corev1.EventTypeNormal,
				},
			},
		},
		"workload does not get scheduled as the node capacity (.status.allocatable['pods']) is already used by non-TAS and TAS workloads": {
			nodes: []corev1.Node{
				*testingnode.MakeNode("x1").
					Label("tas-node", "true").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1000m"),
						corev1.ResourcePods: resource.MustParse("3"),
					}).
					Ready().
					Obj(),
			},
			pods: []corev1.Pod{
				*testingpod.MakePod("test-running", "test-ns").
					NodeName("x1").
					StatusPhase(corev1.PodRunning).
					Request(corev1.ResourceCPU, "300m").
					Obj(),
			},
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(*utiltesting.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "300m").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("bar-admitted", "default").
					Queue("tas-main").
					ReserveQuota(
						utiltesting.MakeAdmission("tas-main", "one").
							Assignment(corev1.ResourceCPU, "tas-default", "150m").
							AssignmentPodCount(2).
							TopologyAssignment(&kueue.TopologyAssignment{
								Levels: utiltas.Levels(&defaultSingleLevelTopology),
								Domains: []kueue.TopologyDomainAssignment{
									{
										Count: 2,
										Values: []string{
											"x1",
										},
									},
								},
							}).Obj(),
					).
					Admitted(true).
					PodSets(*utiltesting.MakePodSet("one", 2).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "150m").
						Obj()).
					Obj(),
			},
			wantInadmissibleLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"tas-main": {"default/foo"},
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "foo"},
					EventType: "Warning",
					Reason:    "Pending",
					Message:   `couldn't assign flavors to pod set one: topology "tas-single-level" doesn't allow to fit any of 1 pod(s)`,
				},
			},
		},
		"workload with zero value request gets scheduled": {
			nodes:           defaultSingleNode,
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueue},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Queue("tas-main").
					PodSets(*utiltesting.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "0").
						Request(corev1.ResourceMemory, "10Mi").
						Obj()).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/foo": *utiltesting.MakeAdmission("tas-main", "one").
					Assignment(corev1.ResourceCPU, "tas-default", "0").
					Assignment(corev1.ResourceMemory, "tas-default", "10Mi").
					AssignmentPodCount(1).
					TopologyAssignment(&kueue.TopologyAssignment{
						Levels: utiltas.Levels(&defaultSingleLevelTopology),
						Domains: []kueue.TopologyDomainAssignment{
							{
								Count: 1,
								Values: []string{
									"x1",
								},
							},
						},
					}).Obj(),
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "foo"},
					Reason:    "QuotaReserved",
					EventType: corev1.EventTypeNormal,
				},
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "foo"},
					Reason:    "Admitted",
					EventType: corev1.EventTypeNormal,
				},
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.TopologyAwareScheduling, true)
			for _, fg := range tc.featureGates {
				features.SetFeatureGateDuringTest(t, fg, true)
			}
			ctx, log := utiltesting.ContextWithLog(t)

			clientBuilder := utiltesting.NewClientBuilder().
				WithLists(
					&kueue.AdmissionCheckList{Items: tc.admissionChecks},
					&kueue.WorkloadList{Items: tc.workloads},
					&kueuealpha.TopologyList{Items: tc.topologies},
					&corev1.PodList{Items: tc.pods},
					&corev1.NodeList{Items: tc.nodes},
					&kueue.LocalQueueList{Items: queues}).
				WithObjects(utiltesting.MakeNamespace("default")).
				WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: utiltesting.TreatSSAAsStrategicMerge}).
				WithStatusSubresource(&kueue.Workload{}, &kueue.ClusterQueue{}, &kueue.LocalQueue{})

			for _, ac := range tc.admissionChecks {
				clientBuilder = clientBuilder.WithStatusSubresource(ac.DeepCopy())
			}
			_ = tasindexer.SetupIndexes(ctx, utiltesting.AsIndexer(clientBuilder))
			cl := clientBuilder.Build()
			recorder := &utiltesting.EventRecorder{}
			cqCache := cache.New(cl)
			now := time.Now()
			fakeClock := testingclock.NewFakeClock(now)
			qManager := queue.NewManager(cl, cqCache, queue.WithClock(fakeClock))
			topologyByName := slices.ToMap(tc.topologies, func(i int) (kueue.TopologyReference, kueuealpha.Topology) {
				return kueue.TopologyReference(tc.topologies[i].Name), tc.topologies[i]
			})
			for _, ac := range tc.admissionChecks {
				cqCache.AddOrUpdateAdmissionCheck(log, &ac)
			}
			for _, flavor := range tc.resourceFlavors {
				cqCache.AddOrUpdateResourceFlavor(log, &flavor)
				if flavor.Spec.TopologyName != nil {
					t := topologyByName[*flavor.Spec.TopologyName]
					cqCache.AddOrUpdateTopology(log, &t)
				}
			}
			for _, cq := range tc.clusterQueues {
				if err := cqCache.AddClusterQueue(ctx, &cq); err != nil {
					t.Fatalf("Inserting clusterQueue %s in cache: %v", cq.Name, err)
				}
				if err := qManager.AddClusterQueue(ctx, &cq); err != nil {
					t.Fatalf("Inserting clusterQueue %s in manager: %v", cq.Name, err)
				}
				if err := cl.Create(ctx, &cq); err != nil {
					t.Fatalf("couldn't create the cluster queue: %v", err)
				}
			}
			for _, q := range queues {
				if err := qManager.AddLocalQueue(ctx, &q); err != nil {
					t.Fatalf("Inserting queue %s/%s in manager: %v", q.Namespace, q.Name, err)
				}
			}
			initiallyAdmittedWorkloads := sets.New[workload.Reference]()
			for _, w := range tc.workloads {
				if workload.IsAdmitted(&w) && !workload.HasNodeToReplace(&w) {
					initiallyAdmittedWorkloads.Insert(workload.Key(&w))
				}
			}
			for _, w := range tc.workloads {
				if qManager.QueueSecondPassIfNeeded(ctx, &w) {
					fakeClock.Step(time.Second)
				}
			}
			scheduler := New(qManager, cqCache, cl, recorder)
			gotScheduled := make([]workload.Reference, 0)
			var mu sync.Mutex
			scheduler.applyAdmission = func(ctx context.Context, w *kueue.Workload) error {
				mu.Lock()
				gotScheduled = append(gotScheduled, workload.Key(w))
				mu.Unlock()
				return nil
			}
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
			snapshot, err := cqCache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("unexpected error while building snapshot: %v", err)
			}
			gotAssignments := make(map[workload.Reference]kueue.Admission)
			for cqName, c := range snapshot.ClusterQueues() {
				for name, w := range c.Workloads {
					if initiallyAdmittedWorkloads.Has(workload.Key(w.Obj)) {
						continue
					}
					switch {
					case !workload.HasQuotaReservation(w.Obj):
						t.Fatalf("Workload %s is not admitted by a clusterQueue, but it is found as member of clusterQueue %s in the cache", name, cqName)
					case w.Obj.Status.Admission.ClusterQueue != cqName:
						t.Fatalf("Workload %s is admitted by clusterQueue %s, but it is found as member of clusterQueue %s in the cache", name, w.Obj.Status.Admission.ClusterQueue, cqName)
					default:
						gotAssignments[name] = *w.Obj.Status.Admission
					}
				}
			}
			if diff := cmp.Diff(tc.wantNewAssignments, gotAssignments, cmpopts.EquateEmpty()); diff != "" {
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
			if diff := cmp.Diff(tc.wantEvents, recorder.RecordedEvents, tc.eventCmpOpts...); diff != "" {
				t.Errorf("unexpected events (-want/+got):\n%s", diff)
			}
		})
	}
}

func TestScheduleForTASPreemption(t *testing.T) {
	singleNode := testingnode.MakeNode("x1").
		Label("tas-node", "true").
		Label(corev1.LabelHostname, "x1").
		StatusAllocatable(corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("5"),
			corev1.ResourceMemory: resource.MustParse("5Gi"),
			corev1.ResourcePods:   resource.MustParse("10"),
		}).
		Ready()
	defaultSingleNode := []corev1.Node{
		*singleNode.Clone().Obj(),
	}
	defaultTwoNodes := []corev1.Node{
		*testingnode.MakeNode("x1").
			Label("tas-node", "true").
			Label(corev1.LabelHostname, "x1").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("5"),
				corev1.ResourceMemory: resource.MustParse("5Gi"),
				corev1.ResourcePods:   resource.MustParse("10"),
			}).
			Ready().
			Obj(),
		*testingnode.MakeNode("y1").
			Label("tas-node", "true").
			Label(corev1.LabelHostname, "y1").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("5"),
				corev1.ResourceMemory: resource.MustParse("5Gi"),
				corev1.ResourcePods:   resource.MustParse("10"),
			}).
			Ready().
			Obj(),
	}
	defaultSingleLevelTopology := *utiltesting.MakeDefaultOneLevelTopology("tas-single-level")
	defaultTASFlavor := *utiltesting.MakeResourceFlavor("tas-default").
		NodeLabel("tas-node", "true").
		TopologyName("tas-single-level").
		Obj()
	defaultClusterQueueWithPreemption := *utiltesting.MakeClusterQueue("tas-main").
		Preemption(kueue.ClusterQueuePreemption{WithinClusterQueue: kueue.PreemptionPolicyLowerPriority}).
		ResourceGroup(*utiltesting.MakeFlavorQuotas("tas-default").
			Resource(corev1.ResourceCPU, "50").
			Resource(corev1.ResourceMemory, "50Gi").Obj()).
		Obj()
	queues := []kueue.LocalQueue{
		{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "default",
				Name:      "tas-main",
			},
			Spec: kueue.LocalQueueSpec{
				ClusterQueue: "tas-main",
			},
		},
	}
	cases := map[string]struct {
		nodes           []corev1.Node
		pods            []corev1.Pod
		topologies      []kueuealpha.Topology
		resourceFlavors []kueue.ResourceFlavor
		clusterQueues   []kueue.ClusterQueue
		workloads       []kueue.Workload

		// wantNewAssignments is a summary of all new admissions in the cache after this cycle.
		wantNewAssignments map[workload.Reference]kueue.Admission
		// wantLeft is the workload keys that are left in the queues after this cycle.
		wantLeft map[kueue.ClusterQueueReference][]workload.Reference
		// wantInadmissibleLeft is the workload keys that are left in the inadmissible state after this cycle.
		wantInadmissibleLeft map[kueue.ClusterQueueReference][]workload.Reference
		// wantPreempted is the keys of the workloads that get preempted in the scheduling cycle.
		wantPreempted sets.Set[workload.Reference]
		// wantEvents asserts on the events, the comparison options are passed by eventCmpOpts
		wantEvents []utiltesting.EventRecord
		// eventCmpOpts are the comparison options for the events
		eventCmpOpts cmp.Options
	}{
		"workload preempted due to quota is using deleted Node": {
			// In this scenario the preemption target, based on quota, is a
			// using an already deleted node (z).
			nodes:           defaultTwoNodes,
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues: []kueue.ClusterQueue{
				*utiltesting.MakeClusterQueue("tas-main").
					Preemption(kueue.ClusterQueuePreemption{WithinClusterQueue: kueue.PreemptionPolicyLowerPriority}).
					ResourceGroup(*utiltesting.MakeFlavorQuotas("tas-default").
						Resource(corev1.ResourceCPU, "10").
						Resource(corev1.ResourceMemory, "10Gi").Obj()).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Queue("tas-main").
					Priority(3).
					PodSets(*utiltesting.MakePodSet("one", 10).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("low-priority-admitted", "default").
					Queue("tas-main").
					Priority(1).
					ReserveQuota(
						utiltesting.MakeAdmission("tas-main", "one").
							Assignment(corev1.ResourceCPU, "tas-default", "5").
							AssignmentPodCount(1).
							TopologyAssignment(&kueue.TopologyAssignment{
								Levels: utiltas.Levels(&defaultSingleLevelTopology),
								Domains: []kueue.TopologyDomainAssignment{
									{
										Count: 1,
										Values: []string{
											"z1",
										},
									},
								},
							}).Obj(),
					).
					Admitted(true).
					PodSets(*utiltesting.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "5").
						Obj()).
					Obj(),
			},
			wantPreempted: sets.New[workload.Reference]("default/low-priority-admitted"),
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"tas-main": {"default/foo"},
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "low-priority-admitted"},
					EventType: "Normal",
					Reason:    "Preempted",
					Message:   "Preempted to accommodate a workload (UID: UNKNOWN, JobUID: UNKNOWN) due to prioritization in the ClusterQueue",
				},
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "foo"},
					EventType: "Warning",
					Reason:    "Pending",
					Message:   `couldn't assign flavors to pod set one: insufficient unused quota for cpu in flavor tas-default, 5 more needed. Pending the preemption of 1 workload(s)`,
				},
			},
		},
		"only low priority workload is preempted": {
			// This test case demonstrates the baseline scenario where there
			// is only one low-priority workload and it gets preempted.
			nodes:           defaultSingleNode,
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueueWithPreemption},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Queue("tas-main").
					Priority(3).
					PodSets(*utiltesting.MakePodSet("one", 1).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "2").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("low-priority-admitted", "default").
					Queue("tas-main").
					Priority(1).
					ReserveQuota(
						utiltesting.MakeAdmission("tas-main", "one").
							Assignment(corev1.ResourceCPU, "tas-default", "5").
							AssignmentPodCount(1).
							TopologyAssignment(&kueue.TopologyAssignment{
								Levels: utiltas.Levels(&defaultSingleLevelTopology),
								Domains: []kueue.TopologyDomainAssignment{
									{
										Count: 1,
										Values: []string{
											"x1",
										},
									},
								},
							}).Obj(),
					).
					Admitted(true).
					PodSets(*utiltesting.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "5").
						Obj()).
					Obj(),
			},
			wantPreempted: sets.New[workload.Reference]("default/low-priority-admitted"),
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"tas-main": {"default/foo"},
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "low-priority-admitted"},
					EventType: "Normal",
					Reason:    "Preempted",
					Message:   "Preempted to accommodate a workload (UID: UNKNOWN, JobUID: UNKNOWN) due to prioritization in the ClusterQueue",
				},
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "foo"},
					EventType: "Warning",
					Reason:    "Pending",
					Message:   `couldn't assign flavors to pod set one: topology "tas-single-level" doesn't allow to fit any of 1 pod(s). Pending the preemption of 1 workload(s)`,
				},
			},
		},
		"With pods count usage pressure on nodes: only low priority workload is preempted": {
			// This test case demonstrates the baseline scenario where there
			// is only one low-priority workload and it gets preempted even if node has pods count usage pressure.
			nodes: []corev1.Node{
				*singleNode.Clone().
					StatusAllocatable(corev1.ResourceList{corev1.ResourcePods: resource.MustParse("1")}).
					Obj(),
			},
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueueWithPreemption},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("high-priority-waiting", "default").
					Queue("tas-main").
					Priority(3).
					PodSets(*utiltesting.MakePodSet("one", 1).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "2").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("low-priority-admitted", "default").
					Queue("tas-main").
					Priority(1).
					ReserveQuota(
						utiltesting.MakeAdmission("tas-main", "one").
							Assignment(corev1.ResourceCPU, "tas-default", "5").
							AssignmentPodCount(1).
							TopologyAssignment(&kueue.TopologyAssignment{
								Levels: utiltas.Levels(&defaultSingleLevelTopology),
								Domains: []kueue.TopologyDomainAssignment{
									{
										Count: 1,
										Values: []string{
											"x1",
										},
									},
								},
							}).Obj(),
					).
					Admitted(true).
					PodSets(*utiltesting.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "5").
						Obj()).
					Obj(),
			},
			wantPreempted: sets.New[workload.Reference]("default/low-priority-admitted"),
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"tas-main": {"default/high-priority-waiting"},
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "low-priority-admitted"},
					EventType: "Normal",
					Reason:    "Preempted",
					Message:   "Preempted to accommodate a workload (UID: UNKNOWN, JobUID: UNKNOWN) due to prioritization in the ClusterQueue",
				},
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "high-priority-waiting"},
					EventType: "Warning",
					Reason:    "Pending",
					Message:   `couldn't assign flavors to pod set one: topology "tas-single-level" doesn't allow to fit any of 1 pod(s). Pending the preemption of 1 workload(s)`,
				},
			},
		},
		"low priority workload is preempted, mid-priority workload survives": {
			// This test case demonstrates the targets are selected according
			// to priorities, similarly as for regular preemption.
			nodes:           defaultSingleNode,
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueueWithPreemption},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Queue("tas-main").
					Priority(3).
					PodSets(*utiltesting.MakePodSet("one", 1).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "2").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("mid-priority-admitted", "default").
					Queue("tas-main").
					Priority(2).
					ReserveQuota(
						utiltesting.MakeAdmission("tas-main", "one").
							Assignment(corev1.ResourceCPU, "tas-default", "2").
							AssignmentPodCount(1).
							TopologyAssignment(&kueue.TopologyAssignment{
								Levels: utiltas.Levels(&defaultSingleLevelTopology),
								Domains: []kueue.TopologyDomainAssignment{
									{
										Count: 1,
										Values: []string{
											"x1",
										},
									},
								},
							}).Obj(),
					).
					Admitted(true).
					PodSets(*utiltesting.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "2").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("low-priority-admitted", "default").
					Queue("tas-main").
					Priority(1).
					ReserveQuota(
						utiltesting.MakeAdmission("tas-main", "one").
							Assignment(corev1.ResourceCPU, "tas-default", "2").
							AssignmentPodCount(1).
							TopologyAssignment(&kueue.TopologyAssignment{
								Levels: utiltas.Levels(&defaultSingleLevelTopology),
								Domains: []kueue.TopologyDomainAssignment{
									{
										Count: 1,
										Values: []string{
											"x1",
										},
									},
								},
							}).Obj(),
					).
					Admitted(true).
					PodSets(*utiltesting.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "2").
						Obj()).
					Obj(),
			},
			wantPreempted: sets.New[workload.Reference]("default/low-priority-admitted"),
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"tas-main": {"default/foo"},
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "low-priority-admitted"},
					EventType: "Normal",
					Reason:    "Preempted",
					Message:   "Preempted to accommodate a workload (UID: UNKNOWN, JobUID: UNKNOWN) due to prioritization in the ClusterQueue",
				},
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "foo"},
					EventType: "Warning",
					Reason:    "Pending",
					Message:   `couldn't assign flavors to pod set one: topology "tas-single-level" doesn't allow to fit any of 1 pod(s). Pending the preemption of 1 workload(s)`,
				},
			},
		},
		"low priority workload is preempted even though there is enough capacity, but fragmented": {
			// In this test we fill in two nodes 4/5 which leaves 2 units empty.
			// It would be enough to schedule both pods of wl3, one pod per
			// node, but the workload requires to run on a single node, thus
			// the lower priority workload is chosen as target.
			nodes:           defaultTwoNodes,
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueueWithPreemption},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "default").
					Queue("tas-main").
					Priority(3).
					PodSets(*utiltesting.MakePodSet("one", 2).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("mid-priority-admitted", "default").
					Queue("tas-main").
					Priority(2).
					ReserveQuota(
						utiltesting.MakeAdmission("tas-main", "one").
							Assignment(corev1.ResourceCPU, "tas-default", "4").
							AssignmentPodCount(1).
							TopologyAssignment(&kueue.TopologyAssignment{
								Levels: utiltas.Levels(&defaultSingleLevelTopology),
								Domains: []kueue.TopologyDomainAssignment{
									{
										Count: 1,
										Values: []string{
											"x1",
										},
									},
								},
							}).Obj(),
					).
					Admitted(true).
					PodSets(*utiltesting.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "4").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("low-priority-admitted", "default").
					Queue("tas-main").
					Priority(1).
					ReserveQuota(
						utiltesting.MakeAdmission("tas-main", "one").
							Assignment(corev1.ResourceCPU, "tas-default", "4").
							AssignmentPodCount(1).
							TopologyAssignment(&kueue.TopologyAssignment{
								Levels: utiltas.Levels(&defaultSingleLevelTopology),
								Domains: []kueue.TopologyDomainAssignment{
									{
										Count: 1,
										Values: []string{
											"y1",
										},
									},
								},
							}).Obj(),
					).
					Admitted(true).
					PodSets(*utiltesting.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "4").
						Obj()).
					Obj(),
			},
			wantPreempted: sets.New[workload.Reference]("default/low-priority-admitted"),
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"tas-main": {"default/foo"},
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "low-priority-admitted"},
					EventType: "Normal",
					Reason:    "Preempted",
					Message:   "Preempted to accommodate a workload (UID: UNKNOWN, JobUID: UNKNOWN) due to prioritization in the ClusterQueue",
				},
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "foo"},
					EventType: "Warning",
					Reason:    "Pending",
					Message:   `couldn't assign flavors to pod set one: topology "tas-single-level" allows to fit only 1 out of 2 pod(s). Pending the preemption of 1 workload(s)`,
				},
			},
		},
		"workload with equal priority awaits for other workloads to complete": {
			// In this test case the waiting workload cannot preempt the running
			// workload as they are both with the same priority. Still, the
			// waiting workload does not let the second workload in the queue
			// to get in, as it is awaiting for the running workloads to
			// complete.
			nodes:           defaultSingleNode,
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueueWithPreemption},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("low-priority-which-would-fit", "default").
					Queue("tas-main").
					Priority(1).
					PodSets(*utiltesting.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("mid-priority-waiting", "default").
					Queue("tas-main").
					Priority(2).
					PodSets(*utiltesting.MakePodSet("one", 2).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("mid-priority-admitted", "default").
					Queue("tas-main").
					Priority(2).
					ReserveQuota(
						utiltesting.MakeAdmission("tas-main", "one").
							Assignment(corev1.ResourceCPU, "tas-default", "4").
							AssignmentPodCount(1).
							TopologyAssignment(&kueue.TopologyAssignment{
								Levels: utiltas.Levels(&defaultSingleLevelTopology),
								Domains: []kueue.TopologyDomainAssignment{
									{
										Count: 1,
										Values: []string{
											"x1",
										},
									},
								},
							}).Obj(),
					).
					Admitted(true).
					PodSets(*utiltesting.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "4").
						Obj()).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"tas-main": {"default/low-priority-which-would-fit"},
			},
			wantInadmissibleLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"tas-main": {"default/mid-priority-waiting"},
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "mid-priority-waiting"},
					EventType: "Warning",
					Reason:    "Pending",
					Message:   `couldn't assign flavors to pod set one: topology "tas-single-level" allows to fit only 1 out of 2 pod(s)`,
				},
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.TopologyAwareScheduling, true)
			ctx, log := utiltesting.ContextWithLog(t)

			clientBuilder := utiltesting.NewClientBuilder().
				WithLists(
					&kueue.WorkloadList{Items: tc.workloads},
					&kueuealpha.TopologyList{Items: tc.topologies},
					&corev1.PodList{Items: tc.pods},
					&corev1.NodeList{Items: tc.nodes},
					&kueue.LocalQueueList{Items: queues}).
				WithObjects(
					utiltesting.MakeNamespace("default"),
				)
			_ = tasindexer.SetupIndexes(ctx, utiltesting.AsIndexer(clientBuilder))
			cl := clientBuilder.Build()
			recorder := &utiltesting.EventRecorder{}
			cqCache := cache.New(cl)
			qManager := queue.NewManager(cl, cqCache)
			topologyByName := slices.ToMap(tc.topologies, func(i int) (kueue.TopologyReference, kueuealpha.Topology) {
				return kueue.TopologyReference(tc.topologies[i].Name), tc.topologies[i]
			})
			for _, flavor := range tc.resourceFlavors {
				cqCache.AddOrUpdateResourceFlavor(log, &flavor)
				if flavor.Spec.TopologyName != nil {
					t := topologyByName[*flavor.Spec.TopologyName]
					cqCache.AddOrUpdateTopology(log, &t)
				}
			}
			for _, cq := range tc.clusterQueues {
				if err := cqCache.AddClusterQueue(ctx, &cq); err != nil {
					t.Fatalf("Inserting clusterQueue %s in cache: %v", cq.Name, err)
				}
				if err := qManager.AddClusterQueue(ctx, &cq); err != nil {
					t.Fatalf("Inserting clusterQueue %s in manager: %v", cq.Name, err)
				}
				if err := cl.Create(ctx, &cq); err != nil {
					t.Fatalf("couldn't create the cluster queue: %v", err)
				}
			}
			for _, q := range queues {
				if err := qManager.AddLocalQueue(ctx, &q); err != nil {
					t.Fatalf("Inserting queue %s/%s in manager: %v", q.Namespace, q.Name, err)
				}
			}
			initiallyAdmittedWorkloads := sets.New[workload.Reference]()
			for _, w := range tc.workloads {
				if workload.IsAdmitted(&w) {
					initiallyAdmittedWorkloads.Insert(workload.Key(&w))
				}
			}
			scheduler := New(qManager, cqCache, cl, recorder)
			gotScheduled := make([]workload.Reference, 0)
			var mu sync.Mutex
			scheduler.applyAdmission = func(ctx context.Context, w *kueue.Workload) error {
				mu.Lock()
				gotScheduled = append(gotScheduled, workload.Key(w))
				mu.Unlock()
				return nil
			}
			wg := sync.WaitGroup{}
			scheduler.setAdmissionRoutineWrapper(routine.NewWrapper(
				func() { wg.Add(1) },
				func() { wg.Done() },
			))

			gotPreempted := sets.New[workload.Reference]()
			scheduler.preemptor.OverrideApply(func(_ context.Context, w *kueue.Workload, _, _ string) error {
				mu.Lock()
				gotPreempted.Insert(workload.Key(w))
				mu.Unlock()
				return nil
			})

			ctx, cancel := context.WithTimeout(ctx, queueingTimeout)
			go qManager.CleanUpOnContext(ctx)
			defer cancel()

			scheduler.schedule(ctx)
			wg.Wait()
			snapshot, err := cqCache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("unexpected error while building snapshot: %v", err)
			}
			if diff := cmp.Diff(tc.wantPreempted, gotPreempted); diff != "" {
				t.Errorf("Unexpected preemptions (-want,+got):\n%s", diff)
			}
			gotAssignments := make(map[workload.Reference]kueue.Admission)
			for cqName, c := range snapshot.ClusterQueues() {
				for name, w := range c.Workloads {
					if initiallyAdmittedWorkloads.Has(workload.Key(w.Obj)) {
						continue
					}
					switch {
					case !workload.HasQuotaReservation(w.Obj):
						t.Fatalf("Workload %s is not admitted by a clusterQueue, but it is found as member of clusterQueue %s in the cache", name, cqName)
					case w.Obj.Status.Admission.ClusterQueue != cqName:
						t.Fatalf("Workload %s is admitted by clusterQueue %s, but it is found as member of clusterQueue %s in the cache", name, w.Obj.Status.Admission.ClusterQueue, cqName)
					default:
						gotAssignments[name] = *w.Obj.Status.Admission
					}
				}
			}
			if diff := cmp.Diff(tc.wantNewAssignments, gotAssignments, cmpopts.EquateEmpty()); diff != "" {
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
			if diff := cmp.Diff(tc.wantEvents, recorder.RecordedEvents, tc.eventCmpOpts...); diff != "" {
				t.Errorf("unexpected events (-want/+got):\n%s", diff)
			}
		})
	}
}

func TestScheduleForTASCohorts(t *testing.T) {
	defaultNodeX1 := *testingnode.MakeNode("x1").
		Label("tas-node", "true").
		Label(corev1.LabelHostname, "x1").
		StatusAllocatable(corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("3"),
			corev1.ResourceMemory: resource.MustParse("5Gi"),
			corev1.ResourcePods:   resource.MustParse("10"),
		}).
		Ready().
		Obj()
	defaultNodeY1 := *testingnode.MakeNode("y1").
		Label("tas-node", "true").
		Label(corev1.LabelHostname, "y1").
		StatusAllocatable(corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("5"),
			corev1.ResourceMemory: resource.MustParse("3Gi"),
			corev1.ResourcePods:   resource.MustParse("10"),
		}).
		Ready().
		Obj()
	defaultTwoNodes := []corev1.Node{defaultNodeX1, defaultNodeY1}
	defaultSingleLevelTopology := *utiltesting.MakeDefaultOneLevelTopology("tas-single-level")
	defaultTASFlavor := *utiltesting.MakeResourceFlavor("tas-default").
		NodeLabel("tas-node", "true").
		TopologyName("tas-single-level").
		Obj()
	defaultClusterQueueA := *utiltesting.MakeClusterQueue("tas-cq-a").
		Cohort("tas-cohort-main").
		Preemption(kueue.ClusterQueuePreemption{
			WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
			ReclaimWithinCohort: kueue.PreemptionPolicyAny,
		}).
		ResourceGroup(*utiltesting.MakeFlavorQuotas("tas-default").
			Resource(corev1.ResourceCPU, "4").
			Resource(corev1.ResourceMemory, "4Gi").Obj()).
		Obj()
	defaultClusterQueueB := *utiltesting.MakeClusterQueue("tas-cq-b").
		Cohort("tas-cohort-main").
		Preemption(kueue.ClusterQueuePreemption{
			ReclaimWithinCohort: kueue.PreemptionPolicyAny,
		}).
		ResourceGroup(*utiltesting.MakeFlavorQuotas("tas-default").
			Resource(corev1.ResourceCPU, "4").
			Resource(corev1.ResourceMemory, "4Gi").Obj()).
		Obj()
	defaultClusterQueueC := *utiltesting.MakeClusterQueue("tas-cq-c").
		Cohort("tas-cohort-main").
		Preemption(kueue.ClusterQueuePreemption{
			ReclaimWithinCohort: kueue.PreemptionPolicyAny,
		}).
		ResourceGroup(*utiltesting.MakeFlavorQuotas("tas-default").
			Resource(corev1.ResourceCPU, "4").
			Resource(corev1.ResourceMemory, "4Gi").Obj()).
		Obj()
	queues := []kueue.LocalQueue{
		{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "default",
				Name:      "tas-lq-a",
			},
			Spec: kueue.LocalQueueSpec{
				ClusterQueue: "tas-cq-a",
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "default",
				Name:      "tas-lq-b",
			},
			Spec: kueue.LocalQueueSpec{
				ClusterQueue: "tas-cq-b",
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "default",
				Name:      "tas-lq-c",
			},
			Spec: kueue.LocalQueueSpec{
				ClusterQueue: "tas-cq-c",
			},
		},
	}
	eventIgnoreMessage := cmpopts.IgnoreFields(utiltesting.EventRecord{}, "Message")
	cases := map[string]struct {
		nodes           []corev1.Node
		pods            []corev1.Pod
		topologies      []kueuealpha.Topology
		resourceFlavors []kueue.ResourceFlavor
		clusterQueues   []kueue.ClusterQueue
		workloads       []kueue.Workload

		// wantNewAssignments is a summary of all new admissions in the cache after this cycle.
		wantNewAssignments map[workload.Reference]kueue.Admission
		// wantLeft is the workload keys that are left in the queues after this cycle.
		wantLeft map[kueue.ClusterQueueReference][]workload.Reference
		// wantInadmissibleLeft is the workload keys that are left in the inadmissible state after this cycle.
		wantInadmissibleLeft map[kueue.ClusterQueueReference][]workload.Reference
		// wantPreempted is the keys of the workloads that get preempted in the scheduling cycle.
		wantPreempted sets.Set[workload.Reference]
		// wantEvents asserts on the events, the comparison options are passed by eventCmpOpts
		wantEvents []utiltesting.EventRecord
		// eventCmpOpts are the comparison options for the events
		eventCmpOpts cmp.Options
	}{
		"workload which requires borrowing gets scheduled": {
			nodes:           defaultTwoNodes,
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueueA, defaultClusterQueueB},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a1", "default").
					Queue("tas-lq-a").
					PodSets(*utiltesting.MakePodSet("one", 6).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/a1": *utiltesting.MakeAdmission("tas-cq-a", "one").
					Assignment(corev1.ResourceCPU, "tas-default", "6").
					AssignmentPodCount(6).
					TopologyAssignment(&kueue.TopologyAssignment{
						Levels: utiltas.Levels(&defaultSingleLevelTopology),
						Domains: []kueue.TopologyDomainAssignment{
							{
								Count: 1,
								Values: []string{
									"x1",
								},
							},
							{
								Count: 5,
								Values: []string{
									"y1",
								},
							},
						},
					}).Obj(),
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "a1"},
					Reason:    "QuotaReserved",
					EventType: corev1.EventTypeNormal,
				},
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "a1"},
					Reason:    "Admitted",
					EventType: corev1.EventTypeNormal,
				},
			},
		},
		"reclaim within cohort; single borrowing workload gets preempted": {
			// This is a baseline scenario for reclamation within cohort where
			// a single borrowing workload gets preempted.
			nodes:           defaultTwoNodes,
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueueA, defaultClusterQueueB},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a1-admitted", "default").
					Queue("tas-lq-a").
					ReserveQuota(
						utiltesting.MakeAdmission("tas-cq-a", "one").
							Assignment(corev1.ResourceCPU, "tas-default", "5").
							AssignmentPodCount(5).
							TopologyAssignment(&kueue.TopologyAssignment{
								Levels: utiltas.Levels(&defaultSingleLevelTopology),
								Domains: []kueue.TopologyDomainAssignment{
									{
										Count: 5,
										Values: []string{
											"x1",
										},
									},
								},
							}).Obj(),
					).
					Admitted(true).
					PodSets(*utiltesting.MakePodSet("one", 5).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("b1", "default").
					Queue("tas-lq-b").
					PodSets(*utiltesting.MakePodSet("one", 4).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"tas-cq-b": {"default/b1"},
			},
			wantPreempted: sets.New[workload.Reference]("default/a1-admitted"),
			eventCmpOpts:  cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "b1"},
					Reason:    "Pending",
					EventType: corev1.EventTypeWarning,
				},
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "a1-admitted"},
					Reason:    "Preempted",
					EventType: corev1.EventTypeNormal,
				},
			},
		},
		"reclaim within cohort; single workload is preempted out three candidates": {
			// In this scenario the CQA is borrowing and has three candidates.
			// The test demonstrates the heuristic to select only a subset of
			// workloads that need to be preempted.
			nodes:           defaultTwoNodes,
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueueA, defaultClusterQueueB},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a1-admitted", "default").
					Queue("tas-lq-a").
					Priority(1).
					ReserveQuota(
						utiltesting.MakeAdmission("tas-cq-a", "one").
							Assignment(corev1.ResourceCPU, "tas-default", "5").
							AssignmentPodCount(5).
							TopologyAssignment(&kueue.TopologyAssignment{
								Levels: utiltas.Levels(&defaultSingleLevelTopology),
								Domains: []kueue.TopologyDomainAssignment{
									{
										Count: 5,
										Values: []string{
											"x1",
										},
									},
								},
							}).Obj(),
					).
					Admitted(true).
					PodSets(*utiltesting.MakePodSet("one", 5).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("a2-admitted", "default").
					Queue("tas-lq-a").
					Priority(2).
					ReserveQuota(
						utiltesting.MakeAdmission("tas-cq-a", "one").
							Assignment(corev1.ResourceCPU, "tas-default", "2").
							AssignmentPodCount(2).
							TopologyAssignment(&kueue.TopologyAssignment{
								Levels: utiltas.Levels(&defaultSingleLevelTopology),
								Domains: []kueue.TopologyDomainAssignment{
									{
										Count: 2,
										Values: []string{
											"y1",
										},
									},
								},
							}).Obj(),
					).
					Admitted(true).
					PodSets(*utiltesting.MakePodSet("one", 2).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("a3-admitted", "default").
					Queue("tas-lq-a").
					Priority(2).
					ReserveQuota(
						utiltesting.MakeAdmission("tas-cq-a", "one").
							Assignment(corev1.ResourceCPU, "tas-default", "1").
							AssignmentPodCount(1).
							TopologyAssignment(&kueue.TopologyAssignment{
								Levels: utiltas.Levels(&defaultSingleLevelTopology),
								Domains: []kueue.TopologyDomainAssignment{
									{
										Count: 1,
										Values: []string{
											"y1",
										},
									},
								},
							}).Obj(),
					).
					Admitted(true).
					PodSets(*utiltesting.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("b1", "default").
					Queue("tas-lq-b").
					Priority(3).
					PodSets(*utiltesting.MakePodSet("one", 3).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"tas-cq-b": {"default/b1"},
			},
			wantPreempted: sets.New[workload.Reference]("default/a1-admitted"),
			eventCmpOpts:  cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "a1-admitted"},
					Reason:    "Preempted",
					EventType: corev1.EventTypeNormal,
				},
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "b1"},
					Reason:    "Pending",
					EventType: corev1.EventTypeWarning,
				},
			},
		},
		"reclaim within cohort; preempting with partial admission": {
			// The new workload requires 4 units of CPU and memory on a single
			// node. There is no such node, so it gets schedule after partial
			// admission to 3 units of CPU and memory. It preempts one workload
			// to fit.
			nodes:           defaultTwoNodes,
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueueA, defaultClusterQueueB},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a1-admitted", "default").
					Queue("tas-lq-a").
					Priority(3).
					ReserveQuota(
						utiltesting.MakeAdmission("tas-cq-a", "one").
							Assignment(corev1.ResourceCPU, "tas-default", "5").
							AssignmentPodCount(5).
							TopologyAssignment(&kueue.TopologyAssignment{
								Levels: utiltas.Levels(&defaultSingleLevelTopology),
								Domains: []kueue.TopologyDomainAssignment{
									{
										Count: 5,
										Values: []string{
											"y1",
										},
									},
								},
							}).Obj(),
					).
					Admitted(true).
					PodSets(*utiltesting.MakePodSet("one", 4).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("a2-admitted", "default").
					Queue("tas-lq-a").
					Priority(2).
					ReserveQuota(
						utiltesting.MakeAdmission("tas-cq-a", "one").
							Assignment(corev1.ResourceCPU, "tas-default", "3").
							AssignmentPodCount(3).
							TopologyAssignment(&kueue.TopologyAssignment{
								Levels: utiltas.Levels(&defaultSingleLevelTopology),
								Domains: []kueue.TopologyDomainAssignment{
									{
										Count: 3,
										Values: []string{
											"x1",
										},
									},
								},
							}).Obj(),
					).
					Admitted(true).
					PodSets(*utiltesting.MakePodSet("one", 3).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("b1", "default").
					Queue("tas-lq-b").
					Priority(3).
					PodSets(*utiltesting.MakePodSet("one", 4).
						SetMinimumCount(3).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Request(corev1.ResourceMemory, "1").
						Obj()).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"tas-cq-b": {"default/b1"},
			},
			wantPreempted: sets.New[workload.Reference]("default/a2-admitted"),
			eventCmpOpts:  cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "a2-admitted"},
					Reason:    "Preempted",
					EventType: corev1.EventTypeNormal,
				},
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "b1"},
					Reason:    "Pending",
					EventType: corev1.EventTypeWarning,
				},
			},
		},
		"reclaim within cohort; capacity reserved by preempting workload does not allow to schedule last workload": {
			// The c1 workload is initially categorized as Fit, because there
			// was one CPU unit empty. However, the preempting workload b1 books
			// this capacity.
			nodes:           defaultTwoNodes,
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueueA, defaultClusterQueueB, defaultClusterQueueC},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a1-admitted", "default").
					Queue("tas-lq-a").
					Priority(2).
					ReserveQuota(
						utiltesting.MakeAdmission("tas-cq-a", "one").
							Assignment(corev1.ResourceCPU, "tas-default", "5").
							AssignmentPodCount(5).
							TopologyAssignment(&kueue.TopologyAssignment{
								Levels: utiltas.Levels(&defaultSingleLevelTopology),
								Domains: []kueue.TopologyDomainAssignment{
									{
										Count: 3,
										Values: []string{
											"x1",
										},
									},
									{
										Count: 2,
										Values: []string{
											"y1",
										},
									},
								},
							}).Obj(),
					).
					Admitted(true).
					PodSets(*utiltesting.MakePodSet("one", 5).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("a2-admitted", "default").
					Queue("tas-lq-a").
					Priority(1).
					ReserveQuota(
						utiltesting.MakeAdmission("tas-cq-a", "one").
							Assignment(corev1.ResourceCPU, "tas-default", "1").
							AssignmentPodCount(1).
							TopologyAssignment(&kueue.TopologyAssignment{
								Levels: utiltas.Levels(&defaultSingleLevelTopology),
								Domains: []kueue.TopologyDomainAssignment{
									{
										Count: 1,
										Values: []string{
											"y1",
										},
									},
								},
							}).Obj(),
					).
					Admitted(true).
					PodSets(*utiltesting.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("a3-admitted", "default").
					Queue("tas-lq-a").
					Priority(1).
					ReserveQuota(
						utiltesting.MakeAdmission("tas-cq-a", "one").
							Assignment(corev1.ResourceCPU, "tas-default", "1").
							AssignmentPodCount(1).
							TopologyAssignment(&kueue.TopologyAssignment{
								Levels: utiltas.Levels(&defaultSingleLevelTopology),
								Domains: []kueue.TopologyDomainAssignment{
									{
										Count: 1,
										Values: []string{
											"y1",
										},
									},
								},
							}).Obj(),
					).
					Admitted(true).
					PodSets(*utiltesting.MakePodSet("one", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("b1", "default").
					Queue("tas-lq-b").
					Priority(3).
					PodSets(*utiltesting.MakePodSet("one", 3).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("c1", "default").
					Queue("tas-lq-c").
					Priority(1).
					PodSets(*utiltesting.MakePodSet("one", 1).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"tas-cq-b": {"default/b1"},
				"tas-cq-c": {"default/c1"},
			},
			wantPreempted: sets.New[workload.Reference]("default/a2-admitted", "default/a3-admitted"),
			eventCmpOpts:  cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "b1"},
					Reason:    "Pending",
					EventType: corev1.EventTypeWarning,
				},
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "c1"},
					Reason:    "Pending",
					EventType: corev1.EventTypeWarning,
				},
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "a2-admitted"},
					Reason:    "Preempted",
					EventType: corev1.EventTypeNormal,
				},
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "a3-admitted"},
					Reason:    "Preempted",
					EventType: corev1.EventTypeNormal,
				},
			},
		},
		"two small workloads considered; both get scheduled on different nodes": {
			nodes:           defaultTwoNodes,
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueueA, defaultClusterQueueB},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a1", "default").
					Priority(2).
					Queue("tas-lq-a").
					PodSets(*utiltesting.MakePodSet("one", 1).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "5").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("b1", "default").
					Priority(1).
					Queue("tas-lq-b").
					PodSets(*utiltesting.MakePodSet("one", 1).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceMemory, "5").
						Obj()).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/a1": *utiltesting.MakeAdmission("tas-cq-a", "one").
					Assignment(corev1.ResourceCPU, "tas-default", "5").
					AssignmentPodCount(1).
					TopologyAssignment(&kueue.TopologyAssignment{
						Levels: utiltas.Levels(&defaultSingleLevelTopology),
						Domains: []kueue.TopologyDomainAssignment{
							{
								Count: 1,
								Values: []string{
									"y1",
								},
							},
						},
					}).Obj(),
				"default/b1": *utiltesting.MakeAdmission("tas-cq-b", "one").
					Assignment(corev1.ResourceMemory, "tas-default", "5").
					AssignmentPodCount(1).
					TopologyAssignment(&kueue.TopologyAssignment{
						Levels: utiltas.Levels(&defaultSingleLevelTopology),
						Domains: []kueue.TopologyDomainAssignment{
							{
								Count: 1,
								Values: []string{
									"x1",
								},
							},
						},
					}).Obj(),
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "b1"},
					Reason:    "QuotaReserved",
					EventType: corev1.EventTypeNormal,
				},
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "b1"},
					Reason:    "Admitted",
					EventType: corev1.EventTypeNormal,
				},
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "a1"},
					Reason:    "QuotaReserved",
					EventType: corev1.EventTypeNormal,
				},
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "a1"},
					Reason:    "Admitted",
					EventType: corev1.EventTypeNormal,
				},
			},
		},
		"two small workloads considered; both get scheduled on the same node": {
			nodes:           defaultTwoNodes,
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueueA, defaultClusterQueueB},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a1", "default").
					Priority(2).
					Queue("tas-lq-a").
					PodSets(*utiltesting.MakePodSet("one", 1).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("b1", "default").
					Priority(1).
					Queue("tas-lq-b").
					PodSets(*utiltesting.MakePodSet("one", 2).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/a1": *utiltesting.MakeAdmission("tas-cq-a", "one").
					Assignment(corev1.ResourceCPU, "tas-default", "1").
					AssignmentPodCount(1).
					TopologyAssignment(&kueue.TopologyAssignment{
						Levels: utiltas.Levels(&defaultSingleLevelTopology),
						Domains: []kueue.TopologyDomainAssignment{
							{
								Count: 1,
								Values: []string{
									"x1",
								},
							},
						},
					}).Obj(),
				"default/b1": *utiltesting.MakeAdmission("tas-cq-b", "one").
					Assignment(corev1.ResourceCPU, "tas-default", "2").
					AssignmentPodCount(2).
					TopologyAssignment(&kueue.TopologyAssignment{
						Levels: utiltas.Levels(&defaultSingleLevelTopology),
						Domains: []kueue.TopologyDomainAssignment{
							{
								Count: 2,
								Values: []string{
									"x1",
								},
							},
						},
					}).Obj(),
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "b1"},
					Reason:    "QuotaReserved",
					EventType: corev1.EventTypeNormal,
				},
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "b1"},
					Reason:    "Admitted",
					EventType: corev1.EventTypeNormal,
				},
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "a1"},
					Reason:    "QuotaReserved",
					EventType: corev1.EventTypeNormal,
				},
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "a1"},
					Reason:    "Admitted",
					EventType: corev1.EventTypeNormal,
				},
			},
		},
		"two small workloads considered; there is only space for one of them on the initial node": {
			nodes:           defaultTwoNodes,
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueueA, defaultClusterQueueB},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a1", "default").
					Priority(2).
					Queue("tas-lq-a").
					PodSets(*utiltesting.MakePodSet("one", 1).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("b1", "default").
					Priority(1).
					Queue("tas-lq-b").
					PodSets(*utiltesting.MakePodSet("one", 3).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/a1": *utiltesting.MakeAdmission("tas-cq-a", "one").
					Assignment(corev1.ResourceCPU, "tas-default", "1").
					AssignmentPodCount(1).
					TopologyAssignment(&kueue.TopologyAssignment{
						Levels: utiltas.Levels(&defaultSingleLevelTopology),
						Domains: []kueue.TopologyDomainAssignment{
							{
								Count: 1,
								Values: []string{
									"x1",
								},
							},
						},
					}).Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"tas-cq-b": {"default/b1"},
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "b1"},
					Reason:    "Pending",
					EventType: corev1.EventTypeWarning,
				},
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "a1"},
					Reason:    "QuotaReserved",
					EventType: corev1.EventTypeNormal,
				},
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "a1"},
					Reason:    "Admitted",
					EventType: corev1.EventTypeNormal,
				},
			},
		},
		"two workloads considered; there is enough space only for the first": {
			nodes:           defaultTwoNodes,
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueueA, defaultClusterQueueB},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a1", "default").
					Priority(2).
					Queue("tas-lq-a").
					PodSets(*utiltesting.MakePodSet("one", 5).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("b1", "default").
					Priority(1).
					Queue("tas-lq-b").
					PodSets(*utiltesting.MakePodSet("one", 5).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"tas-cq-b": {"default/b1"},
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/a1": *utiltesting.MakeAdmission("tas-cq-a", "one").
					Assignment(corev1.ResourceCPU, "tas-default", "5").
					AssignmentPodCount(5).
					TopologyAssignment(&kueue.TopologyAssignment{
						Levels: utiltas.Levels(&defaultSingleLevelTopology),
						Domains: []kueue.TopologyDomainAssignment{
							{
								Count: 5,
								Values: []string{
									"y1",
								},
							},
						},
					}).Obj(),
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "b1"},
					Reason:    "Pending",
					EventType: corev1.EventTypeWarning,
				},
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "a1"},
					Reason:    "QuotaReserved",
					EventType: corev1.EventTypeNormal,
				},
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "a1"},
					Reason:    "Admitted",
					EventType: corev1.EventTypeNormal,
				},
			},
		},
		"two workloads considered; both overlapping in the initial flavor assignment": {
			// Both of the workloads require 2 CPU units which will make them
			// both target the x1 node which is smallest in terms of CPU.
			nodes:           defaultTwoNodes,
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues:   []kueue.ClusterQueue{defaultClusterQueueA, defaultClusterQueueB},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a1", "default").
					Priority(2).
					Queue("tas-lq-a").
					PodSets(*utiltesting.MakePodSet("one", 1).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "2").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("b1", "default").
					Priority(1).
					Queue("tas-lq-b").
					PodSets(*utiltesting.MakePodSet("one", 1).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "2").
						Obj()).
					Obj(),
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/a1": *utiltesting.MakeAdmission("tas-cq-a", "one").
					Assignment(corev1.ResourceCPU, "tas-default", "2").
					AssignmentPodCount(1).
					TopologyAssignment(&kueue.TopologyAssignment{
						Levels: utiltas.Levels(&defaultSingleLevelTopology),
						Domains: []kueue.TopologyDomainAssignment{
							{
								Count: 1,
								Values: []string{
									"x1",
								},
							},
						},
					}).Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"tas-cq-b": {"default/b1"},
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "a1"},
					Reason:    "QuotaReserved",
					EventType: corev1.EventTypeNormal,
				},
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "a1"},
					Reason:    "Admitted",
					EventType: corev1.EventTypeNormal,
				},
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "b1"},
					Reason:    "Pending",
					EventType: corev1.EventTypeWarning,
				},
			},
		},
		"preempting workload with targets reserves capacity so that lower priority workload cannot use it": {
			nodes:           []corev1.Node{defaultNodeY1},
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues: []kueue.ClusterQueue{*utiltesting.MakeClusterQueue("tas-cq-a").
				Cohort("tas-cohort-main").
				Preemption(kueue.ClusterQueuePreemption{
					WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
					ReclaimWithinCohort: kueue.PreemptionPolicyAny,
				}).
				ResourceGroup(*utiltesting.MakeFlavorQuotas("tas-default").
					Resource(corev1.ResourceCPU, "8", "0").
					Resource(corev1.ResourceMemory, "4Gi", "0").Obj()).
				Obj(), defaultClusterQueueB},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a1-admitted", "default").
					Queue("tas-lq-a").
					Priority(1).
					ReserveQuota(
						utiltesting.MakeAdmission("tas-cq-a", "one").
							Assignment(corev1.ResourceCPU, "tas-default", "2").
							AssignmentPodCount(2).
							TopologyAssignment(&kueue.TopologyAssignment{
								Levels: utiltas.Levels(&defaultSingleLevelTopology),
								Domains: []kueue.TopologyDomainAssignment{
									{
										Count: 2,
										Values: []string{
											"y1",
										},
									},
								},
							}).Obj(),
					).
					Admitted(true).
					PodSets(*utiltesting.MakePodSet("one", 2).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("a2", "default").
					Queue("tas-lq-a").
					Priority(2).
					PodSets(*utiltesting.MakePodSet("one", 4).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("b1", "default").
					Queue("tas-lq-b").
					Priority(1).
					PodSets(*utiltesting.MakePodSet("one", 3).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"tas-cq-a": {"default/a2"},
				"tas-cq-b": {"default/b1"},
			},
			wantPreempted: sets.New[workload.Reference]("default/a1-admitted"),
			eventCmpOpts:  cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "a2"},
					Reason:    "Pending",
					EventType: corev1.EventTypeWarning,
				},
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "a1-admitted"},
					Reason:    "Preempted",
					EventType: corev1.EventTypeNormal,
				},
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "b1"},
					Reason:    "Pending",
					EventType: corev1.EventTypeWarning,
				},
			},
		},
		"preempting workload without targets reserves capacity so that lower priority workload cannot use it": {
			nodes:           []corev1.Node{defaultNodeY1},
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues: []kueue.ClusterQueue{*utiltesting.MakeClusterQueue("tas-cq-a").
				Cohort("tas-cohort-main").
				Preemption(kueue.ClusterQueuePreemption{
					WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
					ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
				}).
				ResourceGroup(*utiltesting.MakeFlavorQuotas("tas-default").
					Resource(corev1.ResourceCPU, "8", "0").
					Resource(corev1.ResourceMemory, "4Gi", "0").Obj()).
				Obj(), defaultClusterQueueB},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a1-admitted", "default").
					Queue("tas-lq-a").
					Priority(2).
					ReserveQuota(
						utiltesting.MakeAdmission("tas-cq-a", "one").
							Assignment(corev1.ResourceCPU, "tas-default", "2").
							AssignmentPodCount(2).
							TopologyAssignment(&kueue.TopologyAssignment{
								Levels: utiltas.Levels(&defaultSingleLevelTopology),
								Domains: []kueue.TopologyDomainAssignment{
									{
										Count: 2,
										Values: []string{
											"y1",
										},
									},
								},
							}).Obj(),
					).
					Admitted(true).
					PodSets(*utiltesting.MakePodSet("one", 2).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("a2", "default").
					Queue("tas-lq-a").
					Priority(2).
					PodSets(*utiltesting.MakePodSet("one", 4).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("b1", "default").
					Queue("tas-lq-b").
					Priority(1).
					PodSets(*utiltesting.MakePodSet("one", 3).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"tas-cq-b": {"default/b1"},
			},
			wantInadmissibleLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"tas-cq-a": {"default/a2"},
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "a2"},
					Reason:    "Pending",
					EventType: corev1.EventTypeWarning,
				},
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "b1"},
					Reason:    "Pending",
					EventType: corev1.EventTypeWarning,
				},
			},
		},
		"preempting workload without targets doesn't reserve capacity when it can always reclaim": {
			nodes:           []corev1.Node{defaultNodeY1},
			topologies:      []kueuealpha.Topology{defaultSingleLevelTopology},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor},
			clusterQueues: []kueue.ClusterQueue{*utiltesting.MakeClusterQueue("tas-cq-a").
				Cohort("tas-cohort-main").
				Preemption(kueue.ClusterQueuePreemption{
					WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
					ReclaimWithinCohort: kueue.PreemptionPolicyAny,
				}).
				ResourceGroup(*utiltesting.MakeFlavorQuotas("tas-default").
					Resource(corev1.ResourceCPU, "8", "0").
					Resource(corev1.ResourceMemory, "4Gi", "0").Obj()).
				Obj(), defaultClusterQueueB},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a1-admitted", "default").
					Queue("tas-lq-a").
					Priority(2).
					ReserveQuota(
						utiltesting.MakeAdmission("tas-cq-a", "one").
							Assignment(corev1.ResourceCPU, "tas-default", "2").
							AssignmentPodCount(2).
							TopologyAssignment(&kueue.TopologyAssignment{
								Levels: utiltas.Levels(&defaultSingleLevelTopology),
								Domains: []kueue.TopologyDomainAssignment{
									{
										Count: 2,
										Values: []string{
											"y1",
										},
									},
								},
							}).Obj(),
					).
					Admitted(true).
					PodSets(*utiltesting.MakePodSet("one", 2).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("a2", "default").
					Queue("tas-lq-a").
					Priority(2).
					PodSets(*utiltesting.MakePodSet("one", 4).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("b1", "default").
					Queue("tas-lq-b").
					Priority(1).
					PodSets(*utiltesting.MakePodSet("one", 3).
						PreferredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantInadmissibleLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"tas-cq-a": {"default/a2"},
			},
			eventCmpOpts: cmp.Options{eventIgnoreMessage},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "b1"},
					Reason:    "QuotaReserved",
					EventType: corev1.EventTypeNormal,
				},
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "b1"},
					Reason:    "Admitted",
					EventType: corev1.EventTypeNormal,
				},
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "a2"},
					EventType: "Warning",
					Reason:    "Pending",
					Message:   `couldn't assign flavors to pod set one: topology "tas-single-level" allows to fit only 3 out of 4 pod(s)`,
				},
			},
			wantNewAssignments: map[workload.Reference]kueue.Admission{
				"default/b1": *utiltesting.MakeAdmission("tas-cq-b", "one").
					Assignment(corev1.ResourceCPU, "tas-default", "3").
					AssignmentPodCount(3).
					TopologyAssignment(&kueue.TopologyAssignment{
						Levels: utiltas.Levels(&defaultSingleLevelTopology),
						Domains: []kueue.TopologyDomainAssignment{
							{
								Count: 3,
								Values: []string{
									"y1",
								},
							},
						},
					}).Obj(),
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.TopologyAwareScheduling, true)
			ctx, log := utiltesting.ContextWithLog(t)

			clientBuilder := utiltesting.NewClientBuilder().
				WithLists(
					&kueue.WorkloadList{Items: tc.workloads},
					&kueuealpha.TopologyList{Items: tc.topologies},
					&corev1.PodList{Items: tc.pods},
					&corev1.NodeList{Items: tc.nodes},
					&kueue.LocalQueueList{Items: queues}).
				WithObjects(
					utiltesting.MakeNamespace("default"),
				)
			_ = tasindexer.SetupIndexes(ctx, utiltesting.AsIndexer(clientBuilder))
			cl := clientBuilder.Build()
			recorder := &utiltesting.EventRecorder{}
			cqCache := cache.New(cl)
			qManager := queue.NewManager(cl, cqCache)
			topologyByName := slices.ToMap(tc.topologies, func(i int) (kueue.TopologyReference, kueuealpha.Topology) {
				return kueue.TopologyReference(tc.topologies[i].Name), tc.topologies[i]
			})
			for _, flavor := range tc.resourceFlavors {
				cqCache.AddOrUpdateResourceFlavor(log, &flavor)
				if flavor.Spec.TopologyName != nil {
					t := topologyByName[*flavor.Spec.TopologyName]
					cqCache.AddOrUpdateTopology(log, &t)
				}
			}
			for _, cq := range tc.clusterQueues {
				if err := cqCache.AddClusterQueue(ctx, &cq); err != nil {
					t.Fatalf("Inserting clusterQueue %s in cache: %v", cq.Name, err)
				}
				if err := qManager.AddClusterQueue(ctx, &cq); err != nil {
					t.Fatalf("Inserting clusterQueue %s in manager: %v", cq.Name, err)
				}
				if err := cl.Create(ctx, &cq); err != nil {
					t.Fatalf("couldn't create the cluster queue: %v", err)
				}
			}
			for _, q := range queues {
				if err := qManager.AddLocalQueue(ctx, &q); err != nil {
					t.Fatalf("Inserting queue %s/%s in manager: %v", q.Namespace, q.Name, err)
				}
			}
			initiallyAdmittedWorkloads := sets.New[workload.Reference]()
			for _, w := range tc.workloads {
				if workload.IsAdmitted(&w) {
					initiallyAdmittedWorkloads.Insert(workload.Key(&w))
				}
			}
			scheduler := New(qManager, cqCache, cl, recorder)
			gotScheduled := make([]workload.Reference, 0)
			var mu sync.Mutex
			scheduler.applyAdmission = func(ctx context.Context, w *kueue.Workload) error {
				mu.Lock()
				gotScheduled = append(gotScheduled, workload.Key(w))
				mu.Unlock()
				return nil
			}
			wg := sync.WaitGroup{}
			scheduler.setAdmissionRoutineWrapper(routine.NewWrapper(
				func() { wg.Add(1) },
				func() { wg.Done() },
			))

			gotPreempted := sets.New[workload.Reference]()
			scheduler.preemptor.OverrideApply(func(_ context.Context, w *kueue.Workload, _, _ string) error {
				mu.Lock()
				gotPreempted.Insert(workload.Key(w))
				mu.Unlock()
				return nil
			})

			ctx, cancel := context.WithTimeout(ctx, queueingTimeout)
			go qManager.CleanUpOnContext(ctx)
			defer cancel()

			scheduler.schedule(ctx)
			wg.Wait()
			snapshot, err := cqCache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("unexpected error while building snapshot: %v", err)
			}
			if diff := cmp.Diff(tc.wantPreempted, gotPreempted); diff != "" {
				t.Errorf("Unexpected preemptions (-want,+got):\n%s", diff)
			}
			gotAssignments := make(map[workload.Reference]kueue.Admission)
			for cqName, c := range snapshot.ClusterQueues() {
				for name, w := range c.Workloads {
					if initiallyAdmittedWorkloads.Has(workload.Key(w.Obj)) {
						continue
					}
					switch {
					case !workload.HasQuotaReservation(w.Obj):
						t.Fatalf("Workload %s is not admitted by a clusterQueue, but it is found as member of clusterQueue %s in the cache", name, cqName)
					case w.Obj.Status.Admission.ClusterQueue != cqName:
						t.Fatalf("Workload %s is admitted by clusterQueue %s, but it is found as member of clusterQueue %s in the cache", name, w.Obj.Status.Admission.ClusterQueue, cqName)
					default:
						gotAssignments[name] = *w.Obj.Status.Admission
					}
				}
			}
			if diff := cmp.Diff(tc.wantNewAssignments, gotAssignments, cmpopts.EquateEmpty()); diff != "" {
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
			if diff := cmp.Diff(tc.wantEvents, recorder.RecordedEvents, append(tc.eventCmpOpts, cmpopts.SortSlices(utiltesting.SortEvents))...); diff != "" {
				t.Errorf("unexpected events (-want/+got):\n%s", diff)
			}
		})
	}
}

func TestScheduleForAFS(t *testing.T) {
	afsConfig := &config.AdmissionFairSharing{
		UsageHalfLifeTime:     metav1.Duration{Duration: 10 * time.Second},
		UsageSamplingInterval: metav1.Duration{Duration: 1 * time.Second},
	}
	now := time.Now()
	fakeClock := testingclock.NewFakeClock(now)
	resourceFlavors := []*kueue.ResourceFlavor{
		utiltesting.MakeResourceFlavor("default").Obj(),
	}
	clusterQueues := []kueue.ClusterQueue{
		*utiltesting.MakeClusterQueue("cq1").
			ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "8").
				Resource(corev1.ResourceMemory, "8Gi").Obj()).
			AdmissionMode(kueue.UsageBasedAdmissionFairSharing).
			Obj(),
	}
	queues := []kueue.LocalQueue{
		*utiltesting.MakeLocalQueue("lq-a", "default").
			FairSharing(&kueue.FairSharing{Weight: ptr.To(resource.MustParse("1"))}).
			ClusterQueue("cq1").
			FairSharingStatus(&kueue.FairSharingStatus{
				WeightedShare: 1,
				AdmissionFairSharingStatus: &kueue.AdmissionFairSharingStatus{
					ConsumedResources: corev1.ResourceList{},
				},
			}).
			Obj(),
		*utiltesting.MakeLocalQueue("lq-b", "default").
			FairSharing(&kueue.FairSharing{Weight: ptr.To(resource.MustParse("1"))}).
			ClusterQueue("cq1").
			FairSharingStatus(&kueue.FairSharingStatus{
				WeightedShare: 1,
				AdmissionFairSharingStatus: &kueue.AdmissionFairSharingStatus{
					ConsumedResources: corev1.ResourceList{},
				},
			}).
			Obj(),
		*utiltesting.MakeLocalQueue("lq-c", "default").
			FairSharing(&kueue.FairSharing{Weight: ptr.To(resource.MustParse("1"))}).
			ClusterQueue("cq1").
			FairSharingStatus(&kueue.FairSharingStatus{
				WeightedShare: 1,
				AdmissionFairSharingStatus: &kueue.AdmissionFairSharingStatus{
					ConsumedResources: corev1.ResourceList{},
				},
			}).
			Obj(),
	}

	cases := map[string]struct {
		enableFairSharing bool
		initialUsage      map[string]corev1.ResourceList
		workloads         []kueue.Workload
		wantAdmissions    map[workload.Reference]kueue.Admission
		wantPending       []workload.Reference
	}{
		"admits workload from less active localqueue": {
			enableFairSharing: true,
			initialUsage: map[string]corev1.ResourceList{
				"lq-a": {corev1.ResourceCPU: resource.MustParse("8")},
				"lq-b": {corev1.ResourceCPU: resource.MustParse("2")},
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("wl-a1", "default").
					Queue("lq-a").
					PodSets(*utiltesting.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "8").
						Obj()).
					Creation(now).
					Obj(),
				*utiltesting.MakeWorkload("wl-b1", "default").
					Queue("lq-b").
					PodSets(*utiltesting.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "8").
						Obj()).
					Creation(now.Add(1 * time.Second)).
					Obj(),
			},
			wantAdmissions: map[workload.Reference]kueue.Admission{
				"default/wl-b1": *utiltesting.MakeAdmission("cq1", "one").Assignment(corev1.ResourceCPU, "default", "8").Obj(),
			},
			wantPending: []workload.Reference{
				"default/wl-a1",
			},
		},
		"without AFS: classic admission decision ignores queue usage": {
			enableFairSharing: false,
			initialUsage: map[string]corev1.ResourceList{
				"lq-a": {corev1.ResourceCPU: resource.MustParse("8")},
				"lq-b": {corev1.ResourceCPU: resource.MustParse("2")},
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("wl-a1", "default").
					Queue("lq-a").
					PodSets(*utiltesting.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "8").
						Obj()).
					Creation(now).
					Obj(),
				*utiltesting.MakeWorkload("wl-b1", "default").
					Queue("lq-b").
					PodSets(*utiltesting.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "8").
						Obj()).
					Creation(now.Add(1 * time.Second)).
					Obj(),
			},
			wantAdmissions: map[workload.Reference]kueue.Admission{
				"default/wl-a1": *utiltesting.MakeAdmission("cq1", "one").Assignment(corev1.ResourceCPU, "default", "8").Obj(),
			},
			wantPending: []workload.Reference{
				"default/wl-b1",
			},
		},
		"admits one workload from each localqueue when quota is limited": {
			enableFairSharing: true,
			initialUsage: map[string]corev1.ResourceList{
				"lq-a": {corev1.ResourceCPU: resource.MustParse("4")},
				"lq-b": {corev1.ResourceCPU: resource.MustParse("4")},
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("wl-a1", "default").
					Queue("lq-a").
					PodSets(*utiltesting.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "4").
						Obj()).
					Creation(now).
					Obj(),
				*utiltesting.MakeWorkload("wl-a2", "default").
					Queue("lq-a").
					PodSets(*utiltesting.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "4").
						Obj()).
					Creation(now.Add(1 * time.Second)).
					Obj(),
				*utiltesting.MakeWorkload("wl-b1", "default").
					Queue("lq-b").
					PodSets(*utiltesting.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "4").
						Obj()).
					Creation(now.Add(2 * time.Second)).
					Obj(),
				*utiltesting.MakeWorkload("wl-b2", "default").
					Queue("lq-b").
					PodSets(*utiltesting.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "4").
						Obj()).
					Creation(now.Add(3 * time.Second)).
					Obj(),
			},
			wantAdmissions: map[workload.Reference]kueue.Admission{
				"default/wl-a1": *utiltesting.MakeAdmission("cq1", "one").Assignment(corev1.ResourceCPU, "default", "4").Obj(),
				"default/wl-b1": *utiltesting.MakeAdmission("cq1", "one").Assignment(corev1.ResourceCPU, "default", "4").Obj(),
			},
			wantPending: []workload.Reference{
				"default/wl-a2",
				"default/wl-b2",
			},
		},
		"schedules normally when queues have equal usage": {
			enableFairSharing: true,
			initialUsage: map[string]corev1.ResourceList{
				"lq-a": {corev1.ResourceCPU: resource.MustParse("2")},
				"lq-b": {corev1.ResourceCPU: resource.MustParse("2")},
				"lq-c": {corev1.ResourceCPU: resource.MustParse("2")},
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("wl-a1", "default").
					Queue("lq-a").
					PodSets(*utiltesting.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "4").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("wl-b1", "default").
					Queue("lq-b").
					PodSets(*utiltesting.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "3").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("wl-c1", "default").
					Queue("lq-c").
					PodSets(*utiltesting.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantAdmissions: map[workload.Reference]kueue.Admission{
				"default/wl-a1": *utiltesting.MakeAdmission("cq1", "one").Assignment(corev1.ResourceCPU, "default", "4").Obj(),
				"default/wl-b1": *utiltesting.MakeAdmission("cq1", "one").Assignment(corev1.ResourceCPU, "default", "3").Obj(),
				"default/wl-c1": *utiltesting.MakeAdmission("cq1", "one").Assignment(corev1.ResourceCPU, "default", "1").Obj(),
			},
			wantPending: []workload.Reference{},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if tc.enableFairSharing {
				features.SetFeatureGateDuringTest(t, features.AdmissionFairSharing, true)
			}

			for i, q := range queues {
				if resList, found := tc.initialUsage[q.Name]; found {
					queues[i].Status.FairSharing.AdmissionFairSharingStatus.ConsumedResources = resList
				}
			}

			clientBuilder := utiltesting.NewClientBuilder().
				WithLists(
					&kueue.WorkloadList{Items: tc.workloads},
					&kueue.ClusterQueueList{Items: clusterQueues},
					&kueue.LocalQueueList{Items: queues}).
				WithObjects(
					utiltesting.MakeNamespace("default"),
				)
			cl := clientBuilder.Build()

			fairSharing := &config.FairSharing{
				Enable: tc.enableFairSharing,
			}
			cqCache := cache.New(cl, cache.WithFairSharing(fairSharing.Enable), cache.WithAdmissionFairSharing(afsConfig))
			qManager := queue.NewManager(cl, cqCache, queue.WithAdmissionFairSharing(afsConfig))

			ctx, log := utiltesting.ContextWithLog(t)
			for _, q := range queues {
				if err := qManager.AddLocalQueue(ctx, &q); err != nil {
					t.Fatalf("Inserting queue %s/%s in manager: %v", q.Namespace, q.Name, err)
				}
			}
			for _, rf := range resourceFlavors {
				cqCache.AddOrUpdateResourceFlavor(log, rf)
			}
			for _, cq := range clusterQueues {
				if err := cqCache.AddClusterQueue(ctx, &cq); err != nil {
					t.Fatalf("Inserting clusterQueue %s in cache: %v", cq.Name, err)
				}
				if err := qManager.AddClusterQueue(ctx, &cq); err != nil {
					t.Fatalf("Inserting clusterQueue %s in manager: %v", cq.Name, err)
				}
			}
			recorder := &utiltesting.EventRecorder{}
			scheduler := New(qManager, cqCache, cl, recorder,
				WithFairSharing(fairSharing),
				WithAdmissionFairSharing(afsConfig),
				WithClock(t, fakeClock))
			wg := sync.WaitGroup{}
			scheduler.setAdmissionRoutineWrapper(routine.NewWrapper(
				func() { wg.Add(1) },
				func() { wg.Done() },
			))

			gotScheduled := make(map[workload.Reference]kueue.Admission)
			var mu sync.Mutex
			scheduler.applyAdmission = func(ctx context.Context, w *kueue.Workload) error {
				mu.Lock()
				gotScheduled[workload.Key(w)] = *w.Status.Admission
				mu.Unlock()
				return nil
			}

			ctx, cancel := context.WithTimeout(ctx, queueingTimeout)
			go qManager.CleanUpOnContext(ctx)
			defer cancel()

			for range len(tc.workloads) {
				scheduler.schedule(ctx)
				wg.Wait()
			}

			if diff := cmp.Diff(tc.wantAdmissions, gotScheduled); diff != "" {
				t.Errorf("Unexpected scheduled workloads (-want,+got):\n%s", diff)
			}

			gotPending := make([]workload.Reference, 0)
			for _, wl := range tc.workloads {
				wlKey := workload.Key(&wl)
				if _, scheduled := gotScheduled[wlKey]; !scheduled {
					gotPending = append(gotPending, wlKey)
				}
			}
			if diff := cmp.Diff(tc.wantPending, gotPending); diff != "" {
				t.Errorf("Unexpected pending workloads (-want,+got):\n%s", diff)
			}
		})
	}
}
