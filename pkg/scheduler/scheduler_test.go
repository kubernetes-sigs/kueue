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
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/pointer"
	"sigs.k8s.io/controller-runtime/pkg/client"

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

func TestSchedule(t *testing.T) {
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
	}
	cases := map[string]struct {
		workloads      []kueue.Workload
		admissionError error
		// wantAssignments is a summary of all the admissions in the cache after this cycle.
		wantAssignments map[string]kueue.Admission
		// wantScheduled is the subset of workloads that got scheduled/admitted in this cycle.
		wantScheduled []string
		// wantLeft is the workload keys that are left in the queues after this cycle.
		wantLeft map[string]sets.Set[string]
		// wantInadmissibleLeft is the workload keys that are left in the inadmissible state after this cycle.
		wantInadmissibleLeft map[string]sets.Set[string]
		// wantPreempted is the keys of the workloads that get preempted in the scheduling cycle.
		wantPreempted sets.Set[string]

		// additional*Queues can hold any extra queues needed by the tc
		additionalClusterQueues []kueue.ClusterQueue
		additionalLocalQueues   []kueue.LocalQueue

		// enable partial admission
		enablePartialAdmission bool
	}{
		"workload fits in single clusterQueue": {
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "sales").
					Queue("main").
					PodSets(*utiltesting.MakePodSet("one", 10).
						Request(corev1.ResourceCPU, "1").
						Obj()).
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
							Count: pointer.Int32(10),
						},
					},
				},
			},
			wantScheduled: []string{"sales/foo"},
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
			wantLeft: map[string]sets.Set[string]{
				"sales": sets.New("sales/foo"),
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
					Admit(utiltesting.MakeAdmission("sales", "one").Assignment(corev1.ResourceCPU, "default", "40000m").AssignmentPodCount(40).Obj()).
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
							Count: pointer.Int32(40),
						},
					},
				},
			},
			wantLeft: map[string]sets.Set[string]{
				"sales": sets.New("sales/new"),
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
			wantInadmissibleLeft: map[string]sets.Set[string]{
				"eng-alpha": sets.New("sales/new"),
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
							Count: pointer.Int32(1),
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
							Count: pointer.Int32(51),
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
							Count: pointer.Int32(40),
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
							Count: pointer.Int32(40),
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
							Count: pointer.Int32(10),
						},
						{
							Name: "two",
							Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
								corev1.ResourceCPU: "spot",
							},
							ResourceUsage: corev1.ResourceList{
								corev1.ResourceCPU: resource.MustParse("40000m"),
							},
							Count: pointer.Int32(40),
						},
					},
				},
			},
			wantScheduled: []string{"eng-beta/new"},
		},
		"cannot borrow if cohort was assigned": {
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("new", "eng-alpha").
					Queue("main").
					PodSets(*utiltesting.MakePodSet("one", 40).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("new", "eng-beta").
					Queue("main").
					PodSets(*utiltesting.MakePodSet("one", 51).
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
							Count: pointer.Int32(40),
						},
					},
				},
			},
			wantScheduled: []string{"eng-alpha/new"},
			wantLeft: map[string]sets.Set[string]{
				"eng-beta": sets.New("eng-beta/new"),
			},
		},
		"cannot borrow if needs reclaim from cohort": {
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
					Admit(utiltesting.MakeAdmission("eng-beta").Assignment(corev1.ResourceCPU, "on-demand", "50000m").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("user-spot", "eng-beta").
					Request(corev1.ResourceCPU, "1").
					Admit(utiltesting.MakeAdmission("eng-beta").Assignment(corev1.ResourceCPU, "spot", "1000m").Obj()).
					Obj(),
			},
			wantLeft: map[string]sets.Set[string]{
				"eng-alpha": sets.New("eng-alpha/can-reclaim"),
				"eng-beta":  sets.New("eng-beta/needs-to-borrow"),
			},
			wantAssignments: map[string]kueue.Admission{
				"eng-beta/user-spot":      *utiltesting.MakeAdmission("eng-beta").Assignment(corev1.ResourceCPU, "spot", "1000m").Obj(),
				"eng-beta/user-on-demand": *utiltesting.MakeAdmission("eng-beta").Assignment(corev1.ResourceCPU, "on-demand", "50000m").Obj(),
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
					Admit(utiltesting.MakeAdmission("eng-alpha").Assignment(corev1.ResourceCPU, "spot", "100000m").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("low-1", "eng-beta").
					Priority(-1).
					Request(corev1.ResourceCPU, "30").
					Admit(utiltesting.MakeAdmission("eng-beta").Assignment(corev1.ResourceCPU, "on-demand", "30000m").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("low-2", "eng-beta").
					Priority(-2).
					Request(corev1.ResourceCPU, "10").
					Admit(utiltesting.MakeAdmission("eng-beta").Assignment(corev1.ResourceCPU, "on-demand", "10000m").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("borrower", "eng-alpha").
					Request(corev1.ResourceCPU, "60").
					Admit(utiltesting.MakeAdmission("eng-alpha").Assignment(corev1.ResourceCPU, "on-demand", "60000m").Obj()).
					Obj(),
			},
			wantLeft: map[string]sets.Set[string]{
				// Preemptor is not admitted in this cycle.
				"eng-beta": sets.New("eng-beta/preemptor"),
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
		"cannot borrow resource not listed in clusterQueue": {
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("new", "eng-alpha").
					Queue("main").
					Request("example.com/gpu", "1").
					Obj(),
			},
			wantLeft: map[string]sets.Set[string]{
				"eng-alpha": sets.New("eng-alpha/new"),
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
					Admit(utiltesting.MakeAdmission("eng-beta", "one").Assignment(corev1.ResourceCPU, "on-demand", "45000m").AssignmentPodCount(45).Obj()).
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
							Count: pointer.Int32(60),
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
							Count: pointer.Int32(45),
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
			wantLeft: map[string]sets.Set[string]{
				"flavor-nonexistent-cq": sets.New("sales/foo"),
			},
		},
		"only one workload is admitted in a cohort while borrowing": {
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("new", "eng-beta").
					Queue("main").
					Creation(time.Now().Add(-2 * time.Second)).
					PodSets(*utiltesting.MakePodSet("one", 50).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("new-alpha", "eng-alpha").
					Queue("main").
					Creation(time.Now().Add(-time.Second)).
					PodSets(*utiltesting.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("new-gamma", "eng-gamma").
					Queue("main").
					Creation(time.Now()).
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
					Admit(utiltesting.MakeAdmission("eng-gamma").
						PodSets(
							kueue.PodSetAssignment{
								Name: "borrow-on-demand",
								Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
									corev1.ResourceCPU: "on-demand",
								},
								ResourceUsage: corev1.ResourceList{
									corev1.ResourceCPU: resource.MustParse("51"),
								},
								Count: pointer.Int32(51),
							},
							kueue.PodSetAssignment{
								Name: "use-all-spot",
								Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
									corev1.ResourceCPU: "spot",
								},
								ResourceUsage: corev1.ResourceList{
									corev1.ResourceCPU: resource.MustParse("100"),
								},
								Count: pointer.Int32(100),
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
							Count: pointer.Int32(51),
						},
						kueue.PodSetAssignment{
							Name: "use-all-spot",
							Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
								corev1.ResourceCPU: "spot",
							},
							ResourceUsage: corev1.ResourceList{
								corev1.ResourceCPU: resource.MustParse("100"),
							},
							Count: pointer.Int32(100),
						},
					).Obj(),
				"eng-beta/new": *utiltesting.MakeAdmission("eng-beta", "one").Assignment(corev1.ResourceCPU, "on-demand", "50").AssignmentPodCount(50).Obj(),
			},
			wantScheduled: []string{"eng-beta/new"},
			wantLeft: map[string]sets.Set[string]{
				"eng-alpha": sets.New("eng-alpha/new-alpha"),
				"eng-gamma": sets.New("eng-gamma/new-gamma"),
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
							Count: pointer.Int32(25),
						},
					},
				},
			},
			wantScheduled:          []string{"sales/new"},
			enablePartialAdmission: true,
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
					Admit(utiltesting.MakeAdmission("eng-beta", "one").Assignment("example.com/gpu", "model-a", "10").AssignmentPodCount(10).Obj()).
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
							Count: pointer.Int32(10),
						},
					},
				},
			},
			wantPreempted: sets.New("eng-beta/old"),
			wantLeft: map[string]sets.Set[string]{
				"eng-beta": sets.New("eng-beta/new"),
			},
			enablePartialAdmission: true,
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
							Count: pointer.Int32(20),
						},
						{
							Name: "two",
							Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
								corev1.ResourceCPU: "default",
							},
							ResourceUsage: corev1.ResourceList{
								corev1.ResourceCPU: resource.MustParse("20000m"),
							},
							Count: pointer.Int32(20),
						},
						{
							Name: "three",
							Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
								corev1.ResourceCPU: "default",
							},
							ResourceUsage: corev1.ResourceList{
								corev1.ResourceCPU: resource.MustParse("10000m"),
							},
							Count: pointer.Int32(10),
						},
					},
				},
			},
			wantScheduled:          []string{"sales/new"},
			enablePartialAdmission: true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if tc.enablePartialAdmission {
				defer features.SetFeatureGateDuringTest(t, features.PartialAdmission, true)()
			}
			ctx, _ := utiltesting.ContextWithLog(t)
			scheme := runtime.NewScheme()

			allQueues := append(queues, tc.additionalLocalQueues...)
			allClusterQueues := append(clusterQueues, tc.additionalClusterQueues...)

			clientBuilder := utiltesting.NewClientBuilder().
				WithLists(&kueue.WorkloadList{Items: tc.workloads}, &kueue.LocalQueueList{Items: allQueues}).
				WithObjects(
					&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "eng-alpha", Labels: map[string]string{"dep": "eng"}}},
					&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "eng-beta", Labels: map[string]string{"dep": "eng"}}},
					&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "eng-gamma", Labels: map[string]string{"dep": "eng"}}},
					&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "sales", Labels: map[string]string{"dep": "sales"}}},
				)
			cl := clientBuilder.Build()
			broadcaster := record.NewBroadcaster()
			recorder := broadcaster.NewRecorder(scheme,
				corev1.EventSource{Component: constants.AdmissionName})
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
					if !workload.IsAdmitted(w.Obj) {
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
			if diff := cmp.Diff(tc.wantLeft, qDump); diff != "" {
				t.Errorf("Unexpected elements left in the queue (-want,+got):\n%s", diff)
			}
			qDumpInadmissible := qManager.DumpInadmissible()
			if diff := cmp.Diff(tc.wantInadmissibleLeft, qDumpInadmissible); diff != "" {
				t.Errorf("Unexpected elements left in inadmissible workloads (-want,+got):\n%s", diff)
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
				TotalBorrow: cache.FlavorResourceQuantities{
					"flavor": {},
				},
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
					Priority: pointer.Int32(1),
				}},
			},
			assignment: flavorassigner.Assignment{
				TotalBorrow: cache.FlavorResourceQuantities{
					"flavor": {},
				},
			},
		},
		{
			Info: workload.Info{
				Obj: &kueue.Workload{ObjectMeta: metav1.ObjectMeta{
					Name:              "new_high_pri",
					CreationTimestamp: metav1.NewTime(now.Add(3 * time.Second)),
				}, Spec: kueue.WorkloadSpec{
					Priority: pointer.Int32(1),
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
				TotalBorrow: cache.FlavorResourceQuantities{
					"flavor": {},
				},
			},
		},
		{
			Info: workload.Info{
				Obj: &kueue.Workload{
					ObjectMeta: metav1.ObjectMeta{
						Name:              "evicted_borrowing",
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
			assignment: flavorassigner.Assignment{
				TotalBorrow: cache.FlavorResourceQuantities{
					"flavor": {},
				},
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
	sort.Sort(entryOrdering(input))
	order := make([]string, len(input))
	for i, e := range input {
		order[i] = e.Obj.Name
	}
	wantOrder := []string{"new_high_pri", "old", "recently_evicted", "new", "high_pri_borrowing", "old_borrowing", "evicted_borrowing", "new_borrowing"}
	if diff := cmp.Diff(wantOrder, order); diff != "" {
		t.Errorf("Unexpected order (-want,+got):\n%s", diff)
	}
}

var ignoreConditionTimestamps = cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime")

func TestRequeueAndUpdate(t *testing.T) {
	cq := utiltesting.MakeClusterQueue("cq").Obj()
	q1 := utiltesting.MakeLocalQueue("q1", "ns1").ClusterQueue(cq.Name).Obj()
	w1 := utiltesting.MakeWorkload("w1", "ns1").Queue(q1.Name).Obj()

	cases := []struct {
		name             string
		e                entry
		wantWorkloads    map[string]sets.Set[string]
		wantInadmissible map[string]sets.Set[string]
		wantStatus       kueue.WorkloadStatus
	}{
		{
			name: "workload didn't fit",
			e: entry{
				inadmissibleMsg: "didn't fit",
			},
			wantStatus: kueue.WorkloadStatus{
				Conditions: []metav1.Condition{
					{
						Type:    kueue.WorkloadAdmitted,
						Status:  metav1.ConditionFalse,
						Reason:  "Pending",
						Message: "didn't fit",
					},
				},
			},
			wantInadmissible: map[string]sets.Set[string]{
				"cq": sets.New(workload.Key(w1)),
			},
		},
		{
			name: "assumed",
			e: entry{
				status:          assumed,
				inadmissibleMsg: "",
			},
			wantWorkloads: map[string]sets.Set[string]{
				"cq": sets.New(workload.Key(w1)),
			},
		},
		{
			name: "nominated",
			e: entry{
				status:          nominated,
				inadmissibleMsg: "failed to admit workload",
			},
			wantWorkloads: map[string]sets.Set[string]{
				"cq": sets.New(workload.Key(w1)),
			},
		},
		{
			name: "skipped",
			e: entry{
				status:          skipped,
				inadmissibleMsg: "cohort used in this cycle",
			},
			wantWorkloads: map[string]sets.Set[string]{
				"cq": sets.New(workload.Key(w1)),
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, log := utiltesting.ContextWithLog(t)
			scheme := runtime.NewScheme()

			objs := []client.Object{w1, q1, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ns1"}}}
			clientBuilder := utiltesting.NewClientBuilder().WithObjects(objs...).WithStatusSubresource(objs...)
			cl := clientBuilder.Build()
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
			if diff := cmp.Diff(tc.wantWorkloads, qDump); diff != "" {
				t.Errorf("Unexpected elements in the cluster queue (-want,+got):\n%s", diff)
			}

			inadmissibleDump := qManager.DumpInadmissible()
			if diff := cmp.Diff(tc.wantInadmissible, inadmissibleDump); diff != "" {
				t.Errorf("Unexpected elements in the inadmissible stage of the cluster queue (-want,+got):\n%s", diff)
			}

			var updatedWl kueue.Workload
			if err := cl.Get(ctx, client.ObjectKeyFromObject(w1), &updatedWl); err != nil {
				t.Fatalf("Failed obtaining updated object: %v", err)
			}
			if diff := cmp.Diff(tc.wantStatus, updatedWl.Status, ignoreConditionTimestamps); diff != "" {
				t.Errorf("Unexpected status after updating (-want,+got):\n%s", diff)
			}
		})
	}
}
