/*
Copyright 2022 The Kubernetes Authors.

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
	"sort"
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
	"k8s.io/client-go/tools/record"
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
	"sigs.k8s.io/kueue/pkg/util/routine"
	"sigs.k8s.io/kueue/pkg/util/slices"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/pkg/workload"
)

const (
	queueingTimeout = time.Second
)

var cmpDump = []cmp.Option{
	cmpopts.SortSlices(func(a, b string) bool { return a < b }),
}

// Indicates under which configuration of the MultiplePreemptions flag the test passes.
type multiplePreemptionsCompatibility int

func (mode multiplePreemptionsCompatibility) runTest(baseName string, t *testing.T, f func(t *testing.T)) {
	if mode == Both || mode == Legacy {
		features.SetFeatureGateDuringTest(t, features.MultiplePreemptions, false)
		t.Run(baseName+" LegacyMode", f)
	}
	if mode == Both || mode == MultiplePremptions {
		features.SetFeatureGateDuringTest(t, features.MultiplePreemptions, true)
		t.Run(baseName+" MultiplePreemptionsMode", f)
	}
}

const (
	// Default. Run test with both Legacy and MultiplePreemptions code.
	Both multiplePreemptionsCompatibility = iota
	// Run test only with Legacy code.
	Legacy
	// Run test only with MultiplePreemptions code.
	MultiplePremptions
)

func TestSchedule(t *testing.T) {
	now := time.Now()
	fakeClock := testingclock.NewFakeClock(now)

	resourceFlavors := []*kueue.ResourceFlavor{
		{ObjectMeta: metav1.ObjectMeta{Name: "default"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "on-demand"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "spot"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "model-a"}},
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
		disableLendingLimit     bool
		disablePartialAdmission bool
		enableFairSharing       bool

		multiplePreemptions multiplePreemptionsCompatibility

		workloads      []kueue.Workload
		admissionError error

		// additional*Queues can hold any extra queues needed by the tc
		additionalClusterQueues []kueue.ClusterQueue
		additionalLocalQueues   []kueue.LocalQueue

		// wantAssignments is a summary of all the admissions in the cache after this cycle.
		wantAssignments map[string]kueue.Admission
		// wantScheduled is the subset of workloads that got scheduled/admitted in this cycle.
		wantScheduled []string
		// workloadCmpOpts are the cmp options to compare workloads.
		workloadCmpOpts []cmp.Option
		// wantWorkloads is the subset of workloads that got admitted in this cycle.
		wantWorkloads []kueue.Workload
		// wantLeft is the workload keys that are left in the queues after this cycle.
		wantLeft map[string][]string
		// wantInadmissibleLeft is the workload keys that are left in the inadmissible state after this cycle.
		wantInadmissibleLeft map[string][]string
		// wantPreempted is the keys of the workloads that get preempted in the scheduling cycle.
		wantPreempted sets.Set[string]
		// wantEvents ignored if empty, the Message is ignored (it contains the duration)
		wantEvents []utiltesting.EventRecord

		wantSkippedPreemptions map[string]int
	}{
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
			wantAssignments: map[string]kueue.Admission{
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
			wantScheduled: []string{"sales/foo"},
			workloadCmpOpts: []cmp.Option{
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
			wantAssignments: map[string]kueue.Admission{
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
			wantScheduled: []string{"sales/foo"},
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
			wantLeft: map[string][]string{
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
			wantAssignments: map[string]kueue.Admission{
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
			wantLeft: map[string][]string{
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
			wantInadmissibleLeft: map[string][]string{
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
			wantAssignments: map[string]kueue.Admission{
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
			wantScheduled: []string{"sales/new", "eng-alpha/new"},
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
			wantAssignments: map[string]kueue.Admission{
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
			wantScheduled: []string{"eng-alpha/new", "eng-beta/new"},
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
			wantAssignments: map[string]kueue.Admission{
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
			wantScheduled: []string{"eng-beta/new"},
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
			wantAssignments: map[string]kueue.Admission{
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
			wantScheduled: []string{"eng-alpha/new"},
			wantLeft: map[string][]string{
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
			wantAssignments: map[string]kueue.Admission{
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
			wantScheduled: []string{"eng-alpha/new", "eng-beta/new"},
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
			wantLeft: map[string][]string{
				"eng-alpha": {"eng-alpha/can-reclaim"},
			},
			wantAssignments: map[string]kueue.Admission{
				"eng-beta/user-spot":       *utiltesting.MakeAdmission("eng-beta").Assignment(corev1.ResourceCPU, "spot", "1000m").Obj(),
				"eng-beta/user-on-demand":  *utiltesting.MakeAdmission("eng-beta").Assignment(corev1.ResourceCPU, "on-demand", "50000m").Obj(),
				"eng-beta/needs-to-borrow": *utiltesting.MakeAdmission("eng-beta").Assignment(corev1.ResourceCPU, "on-demand", "1000m").Obj(),
			},
			wantScheduled: []string{
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
			wantAssignments: map[string]kueue.Admission{
				"lend/a": *utiltesting.MakeAdmission("lend-b").Assignment(corev1.ResourceCPU, "default", "2000m").Obj(),
			},
			wantInadmissibleLeft: map[string][]string{
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
			wantAssignments: map[string]kueue.Admission{
				"lend/a": *utiltesting.MakeAdmission("lend-b").Assignment(corev1.ResourceCPU, "default", "2000m").Obj(),
				"lend/b": *utiltesting.MakeAdmission("lend-b").Assignment(corev1.ResourceCPU, "default", "3000m").Obj(),
			},
			wantScheduled: []string{
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
			wantLeft: map[string][]string{
				// Preemptor is not admitted in this cycle.
				"eng-beta": {"eng-beta/preemptor"},
			},
			wantPreempted: sets.New("eng-alpha/borrower", "eng-beta/low-2"),
			wantAssignments: map[string]kueue.Admission{
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
			wantLeft: map[string][]string{
				// Preemptor is not admitted in this cycle.
				"other-beta": {"eng-beta/preemptor"},
			},
			wantInadmissibleLeft: map[string][]string{
				"other-alpha": {"eng-alpha/pending"},
			},
			wantPreempted: sets.New("eng-alpha/use-all"),
			wantAssignments: map[string]kueue.Admission{
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
			wantLeft: map[string][]string{
				"eng-alpha": {"eng-alpha/new"},
			},
		},
		"not enough resources to borrow, fallback to next flavor": {
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
			wantAssignments: map[string]kueue.Admission{
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
			wantScheduled: []string{"eng-alpha/new"},
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
			wantLeft: map[string][]string{
				"flavor-nonexistent-cq": {"sales/foo"},
			},
		},
		"no overadmission while borrowing": {
			// we disable this test for MultiplePreemption
			// logic, as the new logic considers this
			// unschedulable, while the old logic considers
			// it skipped. Duplicate test for MultiplePreemptions
			// directly below.
			multiplePreemptions: Legacy,
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
			wantAssignments: map[string]kueue.Admission{
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
			wantScheduled: []string{"eng-beta/new", "eng-alpha/new-alpha"},
			wantLeft: map[string][]string{
				"eng-gamma": {"eng-gamma/new-gamma"},
			},
			wantSkippedPreemptions: map[string]int{
				"eng-alpha": 0,
				"eng-beta":  0,
				"eng-gamma": 1,
			},
		},
		"no overadmission while borrowing (duplicated from above)": {
			// duplicate of test case above, as new logic
			// considers workload unschedulable, while old
			// logic considers it skipped.
			multiplePreemptions: MultiplePremptions,
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
			wantAssignments: map[string]kueue.Admission{
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
			wantScheduled: []string{"eng-beta/new", "eng-alpha/new-alpha"},
			wantInadmissibleLeft: map[string][]string{
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
			wantAssignments: map[string]kueue.Admission{
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
			wantScheduled: []string{"sales/new"},
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
			wantAssignments: map[string]kueue.Admission{
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
			wantPreempted: sets.New("eng-beta/old"),
			wantLeft: map[string][]string{
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
			wantAssignments: map[string]kueue.Admission{
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
			wantPreempted: sets.New("eng-beta/old"),
			wantLeft: map[string][]string{
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
			wantAssignments: map[string]kueue.Admission{
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
			wantScheduled: []string{"sales/new"},
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
			wantLeft: map[string][]string{
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
					*utiltesting.MakePodSet("main", 1).Request("r1", "16").Obj(),
				).Obj(),
				*utiltesting.MakeWorkload("wl2", "sales").Queue("lq2").Priority(-2).PodSets(
					*utiltesting.MakePodSet("main", 1).Request("r2", "16").Obj(),
				).Obj(),
			},
			wantScheduled: []string{"sales/wl1", "sales/wl2"},
			wantAssignments: map[string]kueue.Admission{
				"sales/wl1": *utiltesting.MakeAdmission("cq1", "main").
					Assignment("r1", "default", "16").AssignmentPodCount(1).
					Obj(),
				"sales/wl2": *utiltesting.MakeAdmission("cq2", "main").
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
					*utiltesting.MakePodSet("main", 1).Request("r1", "16").Obj(),
				).Obj(),
				*utiltesting.MakeWorkload("wl2", "sales").Queue("lq2").Priority(-2).PodSets(
					*utiltesting.MakePodSet("main", 1).Request("r1", "14").Obj(),
				).Obj(),
			},
			wantScheduled: []string{"sales/wl1", "sales/wl2"},
			wantAssignments: map[string]kueue.Admission{
				"sales/wl1": *utiltesting.MakeAdmission("cq1", "main").
					Assignment("r1", "default", "16").AssignmentPodCount(1).
					Obj(),
				"sales/wl2": *utiltesting.MakeAdmission("cq2", "main").
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
					*utiltesting.MakePodSet("main", 1).Request("r1", "16").Obj(),
				).Obj(),
				*utiltesting.MakeWorkload("wl2", "sales").Queue("lq2").Priority(-2).PodSets(
					*utiltesting.MakePodSet("main", 1).Request("r1", "16").Obj(),
				).Obj(),
			},
			wantScheduled: []string{"sales/wl1"},
			wantAssignments: map[string]kueue.Admission{
				"sales/wl1": *utiltesting.MakeAdmission("cq1", "main").
					Assignment("r1", "default", "16").AssignmentPodCount(1).
					Obj(),
			},
			wantLeft: map[string][]string{
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
					PodSets(*utiltesting.MakePodSet("main", 1).
						Request(corev1.ResourceCPU, "3").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("b", "eng-beta").
					Queue("lq_b").
					Creation(now.Add(2 * time.Second)).
					PodSets(*utiltesting.MakePodSet("main", 1).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("admitted_a", "eng-alpha").
					Queue("lq_a").
					PodSets(
						*utiltesting.MakePodSet("main", 1).
							Request(corev1.ResourceCPU, "2").
							Obj(),
					).
					ReserveQuota(utiltesting.MakeAdmission("cq_a").
						PodSets(
							kueue.PodSetAssignment{
								Name: "main",
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
			wantAssignments: map[string]kueue.Admission{
				"eng-alpha/admitted_a": {
					ClusterQueue: "cq_a",
					PodSetAssignments: []kueue.PodSetAssignment{
						{
							Name: "main",
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
							Name: "main",
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
			wantScheduled: []string{
				"eng-beta/b",
			},
			wantInadmissibleLeft: map[string][]string{
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
			wantAssignments: map[string]kueue.Admission{
				"eng-alpha/all_nominal": *utiltesting.MakeAdmission("eng-alpha", "one").Assignment(corev1.ResourceCPU, "on-demand", "50").AssignmentPodCount(50).Obj(),
				"eng-beta/borrowing":    *utiltesting.MakeAdmission("eng-beta", "one").Assignment(corev1.ResourceCPU, "on-demand", "55").AssignmentPodCount(55).Obj(),
				"eng-alpha/new":         *utiltesting.MakeAdmission("eng-alpha", "one").Assignment(corev1.ResourceCPU, "on-demand", "5").AssignmentPodCount(5).Obj(),
			},
			wantScheduled: []string{"eng-alpha/new"},
			wantLeft: map[string][]string{
				"eng-beta": {"eng-beta/older_new"},
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
			wantPreempted: sets.New("eng-alpha/a1", "eng-alpha/a2"),
			wantLeft: map[string][]string{
				"other-alpha": {"eng-alpha/incoming"},
			},
			wantAssignments: map[string]kueue.Admission{
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
			wantInadmissibleLeft: map[string][]string{
				"other-alpha": {"eng-alpha/incoming"},
			},
			wantAssignments: map[string]kueue.Admission{
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
			wantPreempted: sets.New("eng-alpha/alpha1", "eng-gamma/gamma1"),
			wantLeft: map[string][]string{
				// Preemptor is not admitted in this cycle.
				"eng-beta": {"eng-beta/preemptor"},
			},
			wantAssignments: map[string]kueue.Admission{
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
			multiplePreemptions: MultiplePremptions,
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
			wantPreempted: sets.New("eng-alpha/a1", "eng-beta/b1"),
			wantLeft: map[string][]string{
				"other-alpha": {"eng-alpha/preemptor"},
				"other-beta":  {"eng-beta/preemptor"},
			},
			wantAssignments: map[string]kueue.Admission{
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
			multiplePreemptions: MultiplePremptions,
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
			wantPreempted: sets.New("eng-beta/b1"),
			wantLeft: map[string][]string{
				"other-beta": {"eng-beta/preemptor"},
			},
			wantAssignments: map[string]kueue.Admission{
				"eng-alpha/fit": *utiltesting.MakeAdmission("other-alpha").Assignment(corev1.ResourceCPU, "default", "1").Obj(),
				"eng-beta/b1":   *utiltesting.MakeAdmission("other-beta").Assignment(corev1.ResourceCPU, "default", "2").Obj(),
			},
			wantScheduled: []string{"eng-alpha/fit"},
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
			multiplePreemptions: MultiplePremptions,
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
			wantPreempted: sets.New("eng-alpha/a1"),
			wantLeft: map[string][]string{
				"other-alpha": {"eng-alpha/preemptor"},
				"other-beta":  {"eng-beta/pretending-preemptor"},
			},
			wantAssignments: map[string]kueue.Admission{
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
			multiplePreemptions: MultiplePremptions,
			enableFairSharing:   true,
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
			wantPreempted: sets.New("eng-alpha/a1", "eng-beta/b1", "eng-gamma/c1"),
			wantLeft: map[string][]string{
				"other-alpha": {"eng-alpha/preemptor"},
				"other-beta":  {"eng-beta/preemptor"},
				"other-gamma": {"eng-gamma/preemptor"},
			},
			wantAssignments: map[string]kueue.Admission{
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
			multiplePreemptions: MultiplePremptions,
			enableFairSharing:   true,
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
			wantPreempted: sets.New("eng-alpha/a1", "eng-gamma/c1"),
			wantLeft: map[string][]string{
				"other-alpha": {"eng-alpha/preemptor"},
				"other-beta":  {"eng-beta/pretending-preemptor"},
			},
			wantAssignments: map[string]kueue.Admission{
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
			wantLeft: map[string][]string{
				"sales": {"sales/new"},
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
			wantLeft: map[string][]string{
				"sales": {"sales/new"},
			},
		},
		"multiple preemptions blocked when overlapping FlavorResource in cohort": {
			multiplePreemptions: Legacy,
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
					SimpleReserveQuota("other-alpha", "default", now).
					Obj(),
				*utiltesting.MakeWorkload("b1", "eng-beta").
					Priority(0).
					Queue("other").
					Request(corev1.ResourceCPU, "2").
					SimpleReserveQuota("other-beta", "default", now).
					Obj(),
				*utiltesting.MakeWorkload("preemptor", "eng-alpha").
					Priority(100).
					Queue("other").
					Request(corev1.ResourceCPU, "2").
					Obj(),
				*utiltesting.MakeWorkload("pretending-preemptor", "eng-beta").
					Priority(99).
					Queue("other").
					Request(corev1.ResourceCPU, "2").
					Obj(),
			},
			wantPreempted: sets.New("eng-alpha/a1"),
			wantLeft: map[string][]string{
				"other-alpha": {"eng-alpha/preemptor"},
				"other-beta":  {"eng-beta/pretending-preemptor"},
			},
			wantAssignments: map[string]kueue.Admission{
				"eng-alpha/a1": *utiltesting.MakeAdmission("other-alpha").Assignment(corev1.ResourceCPU, "default", "2").Obj(),
				"eng-beta/b1":  *utiltesting.MakeAdmission("other-beta").Assignment(corev1.ResourceCPU, "default", "2").Obj(),
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
						utiltesting.MakeFlavorQuotas("on-demand").Resource("gpu", "10").FlavorQuotas,
						utiltesting.MakeFlavorQuotas("spot").Resource("gpu", "10").FlavorQuotas,
					).
					Obj(),
				*utiltesting.MakeClusterQueue("other-beta").
					Cohort("other").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
					}).
					ResourceGroup(
						utiltesting.MakeFlavorQuotas("on-demand").Resource("gpu", "0").FlavorQuotas,
						utiltesting.MakeFlavorQuotas("spot").Resource("gpu", "0").FlavorQuotas,
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
			wantPreempted: sets.New("eng-beta/b1"),
			wantLeft: map[string][]string{
				"other-alpha": {"eng-alpha/preemptor"},
			},
			wantAssignments: map[string]kueue.Admission{
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
						utiltesting.MakeFlavorQuotas("on-demand").Resource("gpu", "10").FlavorQuotas,
						utiltesting.MakeFlavorQuotas("spot").Resource("gpu", "10").FlavorQuotas,
					).
					Obj(),
				*utiltesting.MakeClusterQueue("other-beta").
					Cohort("other").
					ResourceGroup(
						utiltesting.MakeFlavorQuotas("on-demand").Resource("gpu", "0").FlavorQuotas,
						utiltesting.MakeFlavorQuotas("spot").Resource("gpu", "0").FlavorQuotas,
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
			wantPreempted: sets.New("eng-alpha/a1"),
			wantLeft: map[string][]string{
				"other-alpha": {"eng-alpha/preemptor"},
			},
			wantAssignments: map[string]kueue.Admission{
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
						utiltesting.MakeFlavorQuotas("on-demand").Resource("gpu", "10").FlavorQuotas,
						utiltesting.MakeFlavorQuotas("spot").Resource("gpu", "10").FlavorQuotas,
					).
					Obj(),
				*utiltesting.MakeClusterQueue("other-beta").
					Cohort("other").
					ResourceGroup(
						utiltesting.MakeFlavorQuotas("on-demand").Resource("gpu", "0").FlavorQuotas,
						utiltesting.MakeFlavorQuotas("spot").Resource("gpu", "0").FlavorQuotas,
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
			wantPreempted: sets.New("eng-alpha/a1"),
			wantLeft: map[string][]string{
				"other-alpha": {"eng-alpha/preemptor"},
			},
			wantAssignments: map[string]kueue.Admission{
				"eng-alpha/a1": *utiltesting.MakeAdmission("other-alpha").Assignment("gpu", "on-demand", "6").Obj(),
				"eng-alpha/a2": *utiltesting.MakeAdmission("other-alpha").Assignment("gpu", "spot", "5").Obj(),
				"eng-beta/b1":  *utiltesting.MakeAdmission("other-beta").Assignment("gpu", "spot", "5").Obj(),
			},
		},
	}

	for name, tc := range cases {
		tc.multiplePreemptions.runTest(name, t, func(t *testing.T) {
			metrics.AdmissionCyclePreemptionSkips.Reset()
			if tc.disableLendingLimit {
				features.SetFeatureGateDuringTest(t, features.LendingLimit, false)
			}
			if tc.disablePartialAdmission {
				features.SetFeatureGateDuringTest(t, features.PartialAdmission, false)
			}
			ctx, _ := utiltesting.ContextWithLog(t)

			allQueues := append(queues, tc.additionalLocalQueues...)
			allClusterQueues := append(clusterQueues, tc.additionalClusterQueues...)

			clientBuilder := utiltesting.NewClientBuilder().
				WithLists(&kueue.WorkloadList{Items: tc.workloads}, &kueue.LocalQueueList{Items: allQueues}).
				WithObjects(
					&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "eng-alpha", Labels: map[string]string{"dep": "eng"}}},
					&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "eng-beta", Labels: map[string]string{"dep": "eng"}}},
					&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "eng-gamma", Labels: map[string]string{"dep": "eng"}}},
					&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "sales", Labels: map[string]string{"dep": "sales"}}},
					&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "lend", Labels: map[string]string{"dep": "lend"}}},
				)
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
				cqCache.AddOrUpdateResourceFlavor(resourceFlavors[i])
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
			scheduler := New(qManager, cqCache, cl, recorder, WithFairSharing(&config.FairSharing{Enable: tc.enableFairSharing}), WithClock(t, fakeClock))
			gotScheduled := make(map[string]kueue.Admission)
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
			wg := sync.WaitGroup{}
			scheduler.setAdmissionRoutineWrapper(routine.NewWrapper(
				func() { wg.Add(1) },
				func() { wg.Done() },
			))
			gotPreempted := sets.New[string]()
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

			wantScheduled := make(map[string]kueue.Admission)
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
			gotAssignments := make(map[string]kueue.Admission)
			var gotWorkloads []kueue.Workload
			snapshot, err := cqCache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("unexpected error while building snapshot: %v", err)
			}
			for cqName, c := range snapshot.ClusterQueues {
				for name, w := range c.Workloads {
					gotWorkloads = append(gotWorkloads, *w.Obj)
					switch {
					case !workload.HasQuotaReservation(w.Obj):
						t.Errorf("Workload %s is not admitted by a clusterQueue, but it is found as member of clusterQueue %s in the cache", name, cqName)
					case string(w.Obj.Status.Admission.ClusterQueue) != cqName:
						t.Errorf("Workload %s is admitted by clusterQueue %s, but it is found as member of clusterQueue %s in the cache", name, w.Obj.Status.Admission.ClusterQueue, cqName)
					default:
						gotAssignments[name] = *w.Obj.Status.Admission
					}
				}
			}

			if tc.wantWorkloads != nil {
				if diff := cmp.Diff(tc.wantWorkloads, gotWorkloads, tc.workloadCmpOpts...); diff != "" {
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
				if diff := cmp.Diff(tc.wantEvents, recorder.RecordedEvents, cmpopts.IgnoreFields(utiltesting.EventRecord{}, "Message")); diff != "" {
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
				Borrowing: true,
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
				Borrowing: true,
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
				Borrowing: true,
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
				Borrowing: true,
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
			wantOrder:        []string{"new_high_pri", "old", "recently_evicted", "new", "high_pri_borrowing", "old_borrowing", "evicted_borrowing", "new_borrowing"},
		},
		{
			name:             "Priority sorting is enabled (default) using pods-ready Creation timestamp",
			input:            input,
			prioritySorting:  true,
			workloadOrdering: workload.Ordering{PodsReadyRequeuingTimestamp: config.CreationTimestamp},
			wantOrder:        []string{"new_high_pri", "recently_evicted", "old", "new", "high_pri_borrowing", "old_borrowing", "evicted_borrowing", "new_borrowing"},
		},
		{
			name:             "Priority sorting is disabled using pods-ready Eviction timestamp",
			input:            input,
			prioritySorting:  false,
			workloadOrdering: workload.Ordering{PodsReadyRequeuingTimestamp: config.EvictionTimestamp},
			wantOrder:        []string{"old", "recently_evicted", "new", "new_high_pri", "old_borrowing", "evicted_borrowing", "high_pri_borrowing", "new_borrowing"},
		},
		{
			name:             "Priority sorting is disabled using pods-ready Creation timestamp",
			input:            input,
			prioritySorting:  false,
			workloadOrdering: workload.Ordering{PodsReadyRequeuingTimestamp: config.CreationTimestamp},
			wantOrder:        []string{"recently_evicted", "old", "new", "new_high_pri", "old_borrowing", "evicted_borrowing", "high_pri_borrowing", "new_borrowing"},
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
			sort.Sort(entryOrdering{
				entries:          tc.input,
				workloadOrdering: tc.workloadOrdering},
			)
			order := make([]string, len(tc.input))
			for i, e := range tc.input {
				order[i] = e.Obj.Name
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
		{ObjectMeta: metav1.ObjectMeta{Name: "on-demand"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "spot"}},
	}
	clusterQueue := []kueue.ClusterQueue{
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
					Resource(corev1.ResourceCPU, "100", "0").Obj(),
			).Obj(),
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
	wl := utiltesting.MakeWorkload("low-1", "default").
		Queue("main").
		Request(corev1.ResourceCPU, "50").
		ReserveQuota(utiltesting.MakeAdmission("eng-alpha").Assignment(corev1.ResourceCPU, "on-demand", "50").Obj()).
		Admitted(true).
		Obj()
	cases := []struct {
		name                           string
		cqs                            []kueue.ClusterQueue
		admittedWorkloads              []kueue.Workload
		workloads                      []kueue.Workload
		deleteWorkloads                []kueue.Workload
		wantPreempted                  sets.Set[string]
		wantAdmissionsOnFirstSchedule  map[string]kueue.Admission
		wantAdmissionsOnSecondSchedule map[string]kueue.Admission
	}{
		{
			name: "scheduling context not changed: use next flavor if can't preempt",
			cqs:  clusterQueue,
			admittedWorkloads: []kueue.Workload{
				*wl,
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("new", "default").
					Queue("main").
					Request(corev1.ResourceCPU, "20").
					Obj(),
			},
			deleteWorkloads:               []kueue.Workload{},
			wantPreempted:                 sets.Set[string]{},
			wantAdmissionsOnFirstSchedule: map[string]kueue.Admission{},
			wantAdmissionsOnSecondSchedule: map[string]kueue.Admission{
				"default/new":   *utiltesting.MakeAdmission("eng-alpha").Assignment(corev1.ResourceCPU, "spot", "20").Obj(),
				"default/low-1": *utiltesting.MakeAdmission("eng-alpha").Assignment(corev1.ResourceCPU, "on-demand", "50").Obj(),
			},
		},
		{
			name: "some workloads were deleted",
			cqs:  clusterQueue,
			admittedWorkloads: []kueue.Workload{
				*wl,
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("preemptor", "default").
					Queue("main").
					Request(corev1.ResourceCPU, "20").
					Obj(),
			},
			deleteWorkloads: []kueue.Workload{
				*wl,
			},
			wantPreempted:                 sets.Set[string]{},
			wantAdmissionsOnFirstSchedule: map[string]kueue.Admission{},
			wantAdmissionsOnSecondSchedule: map[string]kueue.Admission{
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
			wantPreempted:   sets.Set[string]{},
			wantAdmissionsOnFirstSchedule: map[string]kueue.Admission{
				"default/workload1": *utiltesting.MakeAdmission("eng-cohort-beta").Assignment(corev1.ResourceCPU, "on-demand", "20").Obj(),
				"default/borrower":  *utiltesting.MakeAdmission("eng-cohort-alpha").Assignment(corev1.ResourceCPU, "on-demand", "20").Obj(),
			},
			wantAdmissionsOnSecondSchedule: map[string]kueue.Admission{
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
			wantPreempted:   sets.Set[string]{},
			wantAdmissionsOnFirstSchedule: map[string]kueue.Admission{
				"default/workload": *utiltesting.MakeAdmission("eng-cohort-theta").Assignment(corev1.ResourceCPU, "spot", "20").Obj(),
			},
			wantAdmissionsOnSecondSchedule: map[string]kueue.Admission{
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
			wantPreempted:   sets.Set[string]{},
			wantAdmissionsOnFirstSchedule: map[string]kueue.Admission{
				"default/workload": *utiltesting.MakeAdmission("eng-cohort-theta").Assignment(corev1.ResourceCPU, "on-demand", "20").Obj(),
			},
			wantAdmissionsOnSecondSchedule: map[string]kueue.Admission{
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
			wantPreempted:                 sets.New("default/placeholder-alpha"),
			wantAdmissionsOnFirstSchedule: map[string]kueue.Admission{},
			wantAdmissionsOnSecondSchedule: map[string]kueue.Admission{
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
			wantPreempted:   sets.New("default/alpha2"),
			wantAdmissionsOnSecondSchedule: map[string]kueue.Admission{
				"default/alpha1": *utiltesting.MakeAdmission("eng-cohort-alpha").Assignment(corev1.ResourceCPU, "on-demand", "22").Obj(),
				"default/alpha3": *utiltesting.MakeAdmission("eng-cohort-alpha").Assignment(corev1.ResourceCPU, "spot", "22").Obj(),
				"default/beta1":  *utiltesting.MakeAdmission("eng-cohort-beta").Assignment(corev1.ResourceCPU, "spot", "22").Obj(),
				"default/new":    *utiltesting.MakeAdmission("eng-cohort-beta").Assignment(corev1.ResourceCPU, "on-demand", "22").Obj(),
			},
		},
	}

	for _, tc := range cases {
		Both.runTest(tc.name, t, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			scheme := runtime.NewScheme()

			clientBuilder := utiltesting.NewClientBuilder().
				WithLists(&kueue.WorkloadList{Items: tc.admittedWorkloads},
					&kueue.WorkloadList{Items: tc.workloads},
					&kueue.ClusterQueueList{Items: tc.cqs},
					&kueue.LocalQueueList{Items: queues}).
				WithObjects(
					&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}},
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
				cqCache.AddOrUpdateResourceFlavor(resourceFlavors[i])
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
			gotScheduled := make(map[string]kueue.Admission)
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
			gotPreempted := sets.New[string]()
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
				err = cqCache.DeleteWorkload(&wl)
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
			gotAssignments := make(map[string]kueue.Admission)
			snapshot, err := cqCache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("unexpected error while building snapshot: %v", err)
			}
			for cqName, c := range snapshot.ClusterQueues {
				for name, w := range c.Workloads {
					switch {
					case !workload.IsAdmitted(w.Obj):
						t.Errorf("Workload %s is not admitted by a clusterQueue, but it is found as member of clusterQueue %s in the cache", name, cqName)
					case string(w.Obj.Status.Admission.ClusterQueue) != cqName:
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
	w1 := utiltesting.MakeWorkload("w1", "ns1").Queue(q1.Name).Obj()

	cases := []struct {
		name              string
		e                 entry
		wantWorkloads     map[string][]string
		wantInadmissible  map[string][]string
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
			},
			wantInadmissible: map[string][]string{
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
			wantWorkloads: map[string][]string{
				"cq": {workload.Key(w1)},
			},
		},
		{
			name: "nominated",
			e: entry{
				status:          nominated,
				inadmissibleMsg: "failed to admit workload",
			},
			wantWorkloads: map[string][]string{
				"cq": {workload.Key(w1)},
			},
		},
		{
			name: "skipped",
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
			},
			wantWorkloads: map[string][]string{
				"cq": {workload.Key(w1)},
			},
			wantStatusUpdates: 1,
		},
	}

	for _, tc := range cases {
		Both.runTest(tc.name, t, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			scheme := runtime.NewScheme()

			updates := 0
			objs := []client.Object{w1, q1, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ns1"}}}
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
			if !cqCache.ClusterQueueActive(cq.Name) {
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
		{ObjectMeta: metav1.ObjectMeta{Name: "on-demand"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "spot"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "model-a"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "model-b"}},
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
		borrowing       bool
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
			borrowing:      true,
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
			borrowing:      true,
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
			ctx, _ := utiltesting.ContextWithLog(t)
			assignment := flavorassigner.Assignment{
				PodSets: []flavorassigner.PodSetAssignment{{
					Name:    "memory",
					Status:  &flavorassigner.Status{},
					Flavors: flavorassigner.ResourceAssignment{corev1.ResourceMemory: &flavorassigner.FlavorAssignment{Mode: tc.assignmentMode}},
				},
					{
						Name:    "gpu",
						Status:  &flavorassigner.Status{},
						Flavors: flavorassigner.ResourceAssignment{"gpu": &flavorassigner.FlavorAssignment{Mode: tc.assignmentMode}},
					},
				},
				Borrowing: tc.borrowing,
				Usage:     tc.assignmentUsage,
			}
			e := &entry{assignment: assignment}
			cl := utiltesting.NewClientBuilder().
				WithLists(&kueue.ClusterQueueList{Items: []kueue.ClusterQueue{*cq}}).
				Build()
			cqCache := cache.New(cl)
			for _, flavor := range resourceFlavors {
				cqCache.AddOrUpdateResourceFlavor(flavor)
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
				cqCache.AddOrUpdateWorkload(wl)
				i += 1
			}
			snapshot, err := cqCache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("unexpected error while building snapshot: %v", err)
			}
			cqSnapshot := snapshot.ClusterQueues["cq"]

			got := resourcesToReserve(e, cqSnapshot)
			if !reflect.DeepEqual(tc.wantReserved, got) {
				t.Errorf("%s failed\n: Want reservedMem: %v, got: %v", tc.name, tc.wantReserved, got)
			}
		})
	}
}

func TestScheduleForTAS(t *testing.T) {
	const (
		tasHostLabel = "kubernetes.io/hostname"
	)
	defaultSingleNode := []corev1.Node{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "x1",
				Labels: map[string]string{
					"tas-node":   "true",
					tasHostLabel: "x1",
				},
			},
			Status: corev1.NodeStatus{
				Allocatable: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("1"),
					corev1.ResourceMemory: resource.MustParse("1Gi"),
				},
				Conditions: []corev1.NodeCondition{
					{
						Type:   corev1.NodeReady,
						Status: corev1.ConditionTrue,
					},
				},
			},
		},
	}
	defaultSingleLevelTopology := kueuealpha.Topology{
		ObjectMeta: metav1.ObjectMeta{
			Name: "tas-single-level",
		},
		Spec: kueuealpha.TopologySpec{
			Levels: []kueuealpha.TopologyLevel{
				{
					NodeLabel: tasHostLabel,
				},
			},
		},
	}
	defaultFlavor := kueue.ResourceFlavor{
		ObjectMeta: metav1.ObjectMeta{
			Name: "default",
		},
		Spec: kueue.ResourceFlavorSpec{},
	}
	defaultTASFlavor := kueue.ResourceFlavor{
		ObjectMeta: metav1.ObjectMeta{
			Name: "tas-default",
		},
		Spec: kueue.ResourceFlavorSpec{
			NodeLabels: map[string]string{
				"tas-node": "true",
			},
			TopologyName: ptr.To[kueue.TopologyReference]("tas-single-level"),
		},
	}
	defaultClusterQueue := *utiltesting.MakeClusterQueue("tas-main").
		ResourceGroup(*utiltesting.MakeFlavorQuotas("tas-default").
			Resource(corev1.ResourceCPU, "50").Obj()).
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
		resourceFlavors []kueue.ResourceFlavor
		clusterQueues   []kueue.ClusterQueue
		workloads       []kueue.Workload

		// wantNewAssignments is a summary of all new admissions in the cache after this cycle.
		wantNewAssignments map[string]kueue.Admission
		// wantLeft is the workload keys that are left in the queues after this cycle.
		wantLeft map[string][]string
		// wantInadmissibleLeft is the workload keys that are left in the inadmissible state after this cycle.
		wantInadmissibleLeft map[string][]string
		// wantEvents asserts on the events, the comparison options are passed by eventCmpOpts
		wantEvents []utiltesting.EventRecord
		// eventCmpOpts are the comparison options for the events
		eventCmpOpts []cmp.Option
	}{
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
						RequiredTopologyRequest(tasHostLabel).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantNewAssignments: map[string]kueue.Admission{
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
			eventCmpOpts: []cmp.Option{eventIgnoreMessage},
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
			wantNewAssignments: map[string]kueue.Admission{
				"default/foo": *utiltesting.MakeAdmission("tas-main", "one").
					Assignment(corev1.ResourceCPU, "default", "1000m").
					AssignmentPodCount(1).
					Obj(),
			},
			eventCmpOpts: []cmp.Option{eventIgnoreMessage},
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
							RequiredTopologyRequest(tasHostLabel).
							Request(corev1.ResourceCPU, "500m").
							Obj()).
					Obj(),
			},
			wantNewAssignments: map[string]kueue.Admission{
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
			eventCmpOpts: []cmp.Option{eventIgnoreMessage},
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
						RequiredTopologyRequest(tasHostLabel).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantNewAssignments: map[string]kueue.Admission{
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
			eventCmpOpts: []cmp.Option{eventIgnoreMessage},
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
			wantInadmissibleLeft: map[string][]string{
				"tas-main": {"default/foo"},
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "foo"},
					EventType: "Warning",
					Reason:    "Pending",
					Message:   "failed to assign flavors to pod set one: no flavor assigned",
				},
			},
		},
		"workload requests topology level which is only present in second flavor": {
			nodes: []corev1.Node{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "x1",
						Labels: map[string]string{
							"tas-node":               "true",
							"cloud.com/custom-level": "x1",
						},
					},
					Status: corev1.NodeStatus{
						Allocatable: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("1"),
						},
						Conditions: []corev1.NodeCondition{
							{
								Type:   corev1.NodeReady,
								Status: corev1.ConditionTrue,
							},
						},
					},
				},
			},
			topologies: []kueuealpha.Topology{defaultSingleLevelTopology,
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "tas-custom-topology",
					},
					Spec: kueuealpha.TopologySpec{
						Levels: []kueuealpha.TopologyLevel{
							{
								NodeLabel: "cloud.com/custom-level",
							},
						},
					},
				},
			},
			resourceFlavors: []kueue.ResourceFlavor{defaultTASFlavor,
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "tas-custom-flavor",
					},
					Spec: kueue.ResourceFlavorSpec{
						NodeLabels: map[string]string{
							"tas-node": "true",
						},
						TopologyName: ptr.To[kueue.TopologyReference]("tas-custom-topology"),
					},
				},
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
			wantNewAssignments: map[string]kueue.Admission{
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
			eventCmpOpts: []cmp.Option{eventIgnoreMessage},
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
						RequiredTopologyRequest(tasHostLabel).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantInadmissibleLeft: map[string][]string{
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
						RequiredTopologyRequest(tasHostLabel).
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
						RequiredTopologyRequest(tasHostLabel).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantInadmissibleLeft: map[string][]string{
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
						RequiredTopologyRequest(tasHostLabel).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantInadmissibleLeft: map[string][]string{
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
					NodeSelector(tasHostLabel, "x1").
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
						RequiredTopologyRequest(tasHostLabel).
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
						RequiredTopologyRequest(tasHostLabel).
						Request(corev1.ResourceCPU, "400m").
						Obj()).
					Obj(),
			},
			wantNewAssignments: map[string]kueue.Admission{
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
			eventCmpOpts: []cmp.Option{eventIgnoreMessage},
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
			ctx, _ := utiltesting.ContextWithLog(t)

			clientBuilder := utiltesting.NewClientBuilder().
				WithLists(
					&kueue.WorkloadList{Items: tc.workloads},
					&kueuealpha.TopologyList{Items: tc.topologies},
					&corev1.PodList{Items: tc.pods},
					&corev1.NodeList{Items: tc.nodes},
					&kueue.LocalQueueList{Items: queues}).
				WithObjects(
					&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}},
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
				cqCache.AddOrUpdateResourceFlavor(&flavor)
				if flavor.Spec.TopologyName != nil {
					t := topologyByName[*flavor.Spec.TopologyName]
					tasCache := cqCache.TASCache()
					levels := utiltas.Levels(&t)
					tasFlavorCache := tasCache.NewTASFlavorCache(*flavor.Spec.TopologyName, levels, flavor.Spec.NodeLabels)
					tasCache.Set(kueue.ResourceFlavorReference(flavor.Name), tasFlavorCache)
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
			initiallyAdmittedWorkloads := sets.New[string]()
			for _, w := range tc.workloads {
				if workload.IsAdmitted(&w) {
					initiallyAdmittedWorkloads.Insert(workload.Key(&w))
				}
			}
			scheduler := New(qManager, cqCache, cl, recorder)
			gotScheduled := make([]string, 0)
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
			gotAssignments := make(map[string]kueue.Admission)
			for cqName, c := range snapshot.ClusterQueues {
				for name, w := range c.Workloads {
					if initiallyAdmittedWorkloads.Has(workload.Key(w.Obj)) {
						continue
					}
					switch {
					case !workload.HasQuotaReservation(w.Obj):
						t.Fatalf("Workload %s is not admitted by a clusterQueue, but it is found as member of clusterQueue %s in the cache", name, cqName)
					case string(w.Obj.Status.Admission.ClusterQueue) != cqName:
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
