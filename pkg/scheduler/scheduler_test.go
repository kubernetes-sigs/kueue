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
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	config "sigs.k8s.io/kueue/apis/config/v1beta1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/queue"
	"sigs.k8s.io/kueue/pkg/scheduler/flavorassigner"
	"sigs.k8s.io/kueue/pkg/util/routine"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/pkg/workload"
)

const (
	queueingTimeout = time.Second
)

var cmpDump = []cmp.Option{
	cmpopts.SortSlices(func(a, b string) bool { return a < b }),
}

func TestSchedule(t *testing.T) {
	now := time.Now()
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
		workloads      []kueue.Workload
		admissionError error
		// wantAssignments is a summary of all the admissions in the cache after this cycle.
		wantAssignments map[string]kueue.Admission
		// wantScheduled is the subset of workloads that got scheduled/admitted in this cycle.
		wantScheduled []string
		// wantLeft is the workload keys that are left in the queues after this cycle.
		wantLeft map[string][]string
		// wantInadmissibleLeft is the workload keys that are left in the inadmissible state after this cycle.
		wantInadmissibleLeft map[string][]string
		// wantPreempted is the keys of the workloads that get preempted in the scheduling cycle.
		wantPreempted sets.Set[string]

		// additional*Queues can hold any extra queues needed by the tc
		additionalClusterQueues []kueue.ClusterQueue
		additionalLocalQueues   []kueue.LocalQueue

		// disable partial admission
		disablePartialAdmission bool

		// enable lending limit
		enableLendingLimit bool

		// ignored if empty, the Message is ignored (it contains the duration)
		wantEvents []utiltesting.EventRecord
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
			enableLendingLimit: true,
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
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if tc.enableLendingLimit {
				defer features.SetFeatureGateDuringTest(t, features.LendingLimit, true)()
			}
			if tc.disablePartialAdmission {
				defer features.SetFeatureGateDuringTest(t, features.PartialAdmission, false)()
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
			}
			scheduler := New(qManager, cqCache, cl, recorder)
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
			scheduler.preemptor.OverrideApply(func(_ context.Context, w *kueue.Workload) error {
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
			snapshot := cqCache.Snapshot()
			for cqName, c := range snapshot.ClusterQueues {
				for name, w := range c.Workloads {
					if !workload.HasQuotaReservation(w.Obj) {
						t.Errorf("Workload %s is not admitted by a clusterQueue, but it is found as member of clusterQueue %s in the cache", name, cqName)
					} else if string(w.Obj.Status.Admission.ClusterQueue) != cqName {
						t.Errorf("Workload %s is admitted by clusterQueue %s, but it is found as member of clusterQueue %s in the cache", name, w.Obj.Status.Admission.ClusterQueue, cqName)
					} else {
						gotAssignments[name] = *w.Obj.Status.Admission
					}
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
	for _, tc := range []struct {
		name             string
		prioritySorting  bool
		workloadOrdering workload.Ordering
		wantOrder        []string
	}{
		{
			name:             "Priority sorting is enabled (default) using pods-ready Eviction timestamp (default)",
			prioritySorting:  true,
			workloadOrdering: workload.Ordering{PodsReadyRequeuingTimestamp: config.EvictionTimestamp},
			wantOrder:        []string{"new_high_pri", "old", "recently_evicted", "new", "high_pri_borrowing", "old_borrowing", "evicted_borrowing", "new_borrowing"},
		},
		{
			name:             "Priority sorting is enabled (default) using pods-ready Creation timestamp",
			prioritySorting:  true,
			workloadOrdering: workload.Ordering{PodsReadyRequeuingTimestamp: config.CreationTimestamp},
			wantOrder:        []string{"new_high_pri", "recently_evicted", "old", "new", "high_pri_borrowing", "old_borrowing", "evicted_borrowing", "new_borrowing"},
		},
		{
			name:             "Priority sorting is disabled using pods-ready Eviction timestamp",
			prioritySorting:  false,
			workloadOrdering: workload.Ordering{PodsReadyRequeuingTimestamp: config.EvictionTimestamp},
			wantOrder:        []string{"old", "recently_evicted", "new", "new_high_pri", "old_borrowing", "evicted_borrowing", "high_pri_borrowing", "new_borrowing"},
		},
		{
			name:             "Priority sorting is disabled using pods-ready Creation timestamp",
			prioritySorting:  false,
			workloadOrdering: workload.Ordering{PodsReadyRequeuingTimestamp: config.CreationTimestamp},
			wantOrder:        []string{"recently_evicted", "old", "new", "new_high_pri", "old_borrowing", "evicted_borrowing", "high_pri_borrowing", "new_borrowing"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Cleanup(features.SetFeatureGateDuringTest(t, features.PrioritySortingWithinCohort, tc.prioritySorting))
			sort.Sort(entryOrdering{
				entries:          input,
				workloadOrdering: tc.workloadOrdering},
			)
			order := make([]string, len(input))
			for i, e := range input {
				order[i] = e.Obj.Name
			}
			if diff := cmp.Diff(tc.wantOrder, order); diff != "" {
				t.Errorf("%s: Unexpected order (-want,+got):\n%s", tc.name, diff)
			}
		})
	}
}

func TestLastSchedulingContext(t *testing.T) {
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
	clusterQueue_cohort := []kueue.ClusterQueue{
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
			cqs:  clusterQueue_cohort,
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
			cqs:  clusterQueue_cohort,
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
			cqs:  clusterQueue_cohort,
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
			cqs:  clusterQueue_cohort,
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
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
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
			scheduler := New(qManager, cqCache, cl, recorder)
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
			scheduler.preemptor.OverrideApply(func(_ context.Context, w *kueue.Workload) error {
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
			if diff := cmp.Diff(tc.wantAdmissionsOnFirstSchedule, gotScheduled); diff != "" {
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
			snapshot := cqCache.Snapshot()
			for cqName, c := range snapshot.ClusterQueues {
				for name, w := range c.Workloads {
					if !workload.IsAdmitted(w.Obj) {
						t.Errorf("Workload %s is not admitted by a clusterQueue, but it is found as member of clusterQueue %s in the cache", name, cqName)
					} else if string(w.Obj.Status.Admission.ClusterQueue) != cqName {
						t.Errorf("Workload %s is admitted by clusterQueue %s, but it is found as member of clusterQueue %s in the cache", name, w.Obj.Status.Admission.ClusterQueue, cqName)
					} else {
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
		t.Run(tc.name, func(t *testing.T) {
			ctx, log := utiltesting.ContextWithLog(t)
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
			scheduler.requeueAndUpdate(log, ctx, tc.e)

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
			scheduler.requeueAndUpdate(log, ctx, tc.e)
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
		assignmentUsage cache.FlavorResourceQuantities
		cqUsage         cache.FlavorResourceQuantities
		wantReserved    cache.FlavorResourceQuantities
	}{
		{
			name:           "Reserved memory and gpu less than assignment usage, assignment preempts",
			assignmentMode: flavorassigner.Preempt,
			assignmentUsage: cache.FlavorResourceQuantities{
				kueue.ResourceFlavorReference("on-demand"): {corev1.ResourceMemory: 50},
				kueue.ResourceFlavorReference("model-a"):   {"gpu": 6},
			},
			cqUsage: cache.FlavorResourceQuantities{
				kueue.ResourceFlavorReference("on-demand"): {corev1.ResourceMemory: 60},
				kueue.ResourceFlavorReference("spot"):      {corev1.ResourceMemory: 50},
				kueue.ResourceFlavorReference("model-a"):   {"gpu": 6},
				kueue.ResourceFlavorReference("model-b"):   {"gpu": 2},
			},
			wantReserved: cache.FlavorResourceQuantities{
				kueue.ResourceFlavorReference("on-demand"): {corev1.ResourceMemory: 40},
				kueue.ResourceFlavorReference("model-a"):   {"gpu": 4},
			},
		},
		{
			name:           "Reserved memory equal assignment usage, assignment preempts",
			assignmentMode: flavorassigner.Preempt,
			assignmentUsage: cache.FlavorResourceQuantities{
				kueue.ResourceFlavorReference("on-demand"): {corev1.ResourceMemory: 30},
				kueue.ResourceFlavorReference("model-a"):   {"gpu": 2},
			},
			cqUsage: cache.FlavorResourceQuantities{
				kueue.ResourceFlavorReference("on-demand"): {corev1.ResourceMemory: 60},
				kueue.ResourceFlavorReference("spot"):      {corev1.ResourceMemory: 50},
				kueue.ResourceFlavorReference("model-a"):   {"gpu": 2},
				kueue.ResourceFlavorReference("model-b"):   {"gpu": 2},
			},
			wantReserved: cache.FlavorResourceQuantities{
				kueue.ResourceFlavorReference("on-demand"): {corev1.ResourceMemory: 30},
				kueue.ResourceFlavorReference("model-a"):   {"gpu": 2},
			},
		},
		{
			name:           "Reserved memory equal assignment usage, assigmnent fits",
			assignmentMode: flavorassigner.Fit,
			assignmentUsage: cache.FlavorResourceQuantities{
				kueue.ResourceFlavorReference("on-demand"): {corev1.ResourceMemory: 50},
				kueue.ResourceFlavorReference("model-a"):   {"gpu": 2},
			},
			cqUsage: cache.FlavorResourceQuantities{
				kueue.ResourceFlavorReference("on-demand"): {corev1.ResourceMemory: 60},
				kueue.ResourceFlavorReference("spot"):      {corev1.ResourceMemory: 50},
				kueue.ResourceFlavorReference("model-a"):   {"gpu": 2},
				kueue.ResourceFlavorReference("model-b"):   {"gpu": 2},
			},
			wantReserved: cache.FlavorResourceQuantities{
				kueue.ResourceFlavorReference("on-demand"): {corev1.ResourceMemory: 50},
				kueue.ResourceFlavorReference("model-a"):   {"gpu": 2},
			},
		},
		{
			name:           "Reserved memory is 0, CQ is borrowing, assignment preempts without borrowing",
			assignmentMode: flavorassigner.Preempt,
			assignmentUsage: cache.FlavorResourceQuantities{
				kueue.ResourceFlavorReference("spot"):    {corev1.ResourceMemory: 50},
				kueue.ResourceFlavorReference("model-b"): {"gpu": 2},
			},
			cqUsage: cache.FlavorResourceQuantities{
				kueue.ResourceFlavorReference("on-demand"): {corev1.ResourceMemory: 60},
				kueue.ResourceFlavorReference("spot"):      {corev1.ResourceMemory: 60},
				kueue.ResourceFlavorReference("model-a"):   {"gpu": 2},
				kueue.ResourceFlavorReference("model-b"):   {"gpu": 10},
			},
			wantReserved: cache.FlavorResourceQuantities{
				kueue.ResourceFlavorReference("spot"):    {corev1.ResourceMemory: 0},
				kueue.ResourceFlavorReference("model-b"): {"gpu": 0},
			},
		},
		{
			name:           "Reserved memory cut by nominal+borrowing quota, assignment preempts and borrows",
			assignmentMode: flavorassigner.Preempt,
			borrowing:      true,
			assignmentUsage: cache.FlavorResourceQuantities{
				kueue.ResourceFlavorReference("spot"):    {corev1.ResourceMemory: 50},
				kueue.ResourceFlavorReference("model-b"): {"gpu": 2},
			},
			cqUsage: cache.FlavorResourceQuantities{
				kueue.ResourceFlavorReference("on-demand"): {corev1.ResourceMemory: 60},
				kueue.ResourceFlavorReference("spot"):      {corev1.ResourceMemory: 60},
				kueue.ResourceFlavorReference("model-a"):   {"gpu": 2},
				kueue.ResourceFlavorReference("model-b"):   {"gpu": 10},
			},
			wantReserved: cache.FlavorResourceQuantities{
				kueue.ResourceFlavorReference("spot"):    {corev1.ResourceMemory: 40},
				kueue.ResourceFlavorReference("model-b"): {"gpu": 2},
			},
		},
		{
			name:           "Reserved memory equal assignment usage, CQ borrowing limit is nil",
			assignmentMode: flavorassigner.Preempt,
			borrowing:      true,
			assignmentUsage: cache.FlavorResourceQuantities{
				kueue.ResourceFlavorReference("on-demand"): {corev1.ResourceMemory: 50},
				kueue.ResourceFlavorReference("model-b"):   {"gpu": 2},
			},
			cqUsage: cache.FlavorResourceQuantities{
				kueue.ResourceFlavorReference("on-demand"): {corev1.ResourceMemory: 60},
				kueue.ResourceFlavorReference("spot"):      {corev1.ResourceMemory: 60},
				kueue.ResourceFlavorReference("model-a"):   {"gpu": 2},
				kueue.ResourceFlavorReference("model-b"):   {"gpu": 10},
			},
			wantReserved: cache.FlavorResourceQuantities{
				kueue.ResourceFlavorReference("on-demand"): {corev1.ResourceMemory: 50},
				kueue.ResourceFlavorReference("model-b"):   {"gpu": 2},
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
			cachedCQ := cqCache.Snapshot().ClusterQueues["cq"]
			cachedCQ.Usage = tc.cqUsage

			got := resourcesToReserve(e, cachedCQ)
			if !reflect.DeepEqual(tc.wantReserved, got) {
				t.Errorf("%s failed\n: Want reservedMem: %v, got: %v", tc.name, tc.wantReserved, got)
			}
		})
	}
}
