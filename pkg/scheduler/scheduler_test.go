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
	"k8s.io/utils/ptr"
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
		podTemplates   []corev1.PodTemplate
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
						SetPodTemplateName("one").
						Obj()).
					Obj(),
			},
			podTemplates: []corev1.PodTemplate{
				*utiltesting.MakePodTemplate("one", "sales").
					Labels(map[string]string{constants.WorkloadNameLabel: "foo"}).
					Request(corev1.ResourceCPU, "1").
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
		},
		"error during admission": {
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "sales").
					Queue("main").
					PodSets(*utiltesting.MakePodSet("one", 10).
						Obj()).
					Obj(),
			},
			podTemplates: []corev1.PodTemplate{
				*utiltesting.MakePodTemplate("one", "").
					Labels(map[string]string{constants.WorkloadNameLabel: "foo"}).
					Request(corev1.ResourceCPU, "1").
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
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("assigned", "sales").
					PodSets(*utiltesting.MakePodSet("one", 40).
						Obj()).
					ReserveQuota(utiltesting.MakeAdmission("sales", "one").Assignment(corev1.ResourceCPU, "default", "40000m").AssignmentPodCount(40).Obj()).
					Obj(),
			},
			podTemplates: []corev1.PodTemplate{
				*utiltesting.MakePodTemplate("new-one", "").
					Labels(map[string]string{constants.WorkloadNameLabel: "new"}).
					Request(corev1.ResourceCPU, "1").
					Obj(),
				*utiltesting.MakePodTemplate("assigned-one", "").
					Labels(map[string]string{constants.WorkloadNameLabel: "assigned"}).
					Request(corev1.ResourceCPU, "1").
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
			wantLeft: map[string]sets.Set[string]{
				"sales": sets.New("sales/new"),
			},
		},
		"failed to match clusterQueue selector": {
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("new", "sales").
					Queue("blocked").
					PodSets(*utiltesting.MakePodSet("one", 1).
						Obj()).
					Obj(),
			},
			podTemplates: []corev1.PodTemplate{
				*utiltesting.MakePodTemplate("new-one", "").
					Labels(map[string]string{constants.WorkloadNameLabel: "new"}).
					Request(corev1.ResourceCPU, "1").
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
						SetPodTemplateName("new-one").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("new", "eng-alpha").
					Queue("main").
					PodSets(*utiltesting.MakePodSet("one", 51 /* Will borrow */).
						SetPodTemplateName("new-one").
						Obj()).
					Obj(),
			},
			podTemplates: []corev1.PodTemplate{
				*utiltesting.MakePodTemplate("new-one", "sales").
					Labels(map[string]string{constants.WorkloadNameLabel: "new"}).
					Request(corev1.ResourceCPU, "1").
					Obj(),
				*utiltesting.MakePodTemplate("new-one", "eng-alpha").
					Labels(map[string]string{constants.WorkloadNameLabel: "new"}).
					Request(corev1.ResourceCPU, "1").
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
						SetPodTemplateName("new-one").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("new", "eng-beta").
					Queue("main").
					PodSets(*utiltesting.MakePodSet("one", 40).
						SetPodTemplateName("new-one").
						Obj()).
					Obj(),
			},
			podTemplates: []corev1.PodTemplate{
				*utiltesting.MakePodTemplate("new-one", "eng-alpha").
					Labels(map[string]string{constants.WorkloadNameLabel: "new"}).
					Request(corev1.ResourceCPU, "1").
					Obj(),
				*utiltesting.MakePodTemplate("new-one", "eng-beta").
					Labels(map[string]string{constants.WorkloadNameLabel: "new"}).
					Request(corev1.ResourceCPU, "1").
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
							SetPodTemplateName("new-one").
							Obj(),
						*utiltesting.MakePodSet("two", 40).
							SetPodTemplateName("new-two").
							Obj(),
					).
					Obj(),
			},
			podTemplates: []corev1.PodTemplate{
				*utiltesting.MakePodTemplate("new-one", "eng-beta").
					Labels(map[string]string{constants.WorkloadNameLabel: "new"}).
					Request(corev1.ResourceCPU, "6").
					Request("example.com/gpu", "1").
					Obj(),
				*utiltesting.MakePodTemplate("new-two", "eng-beta").
					Labels(map[string]string{constants.WorkloadNameLabel: "new"}).
					Request(corev1.ResourceCPU, "1").
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
						SetPodTemplateName("new-one").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("new", "eng-beta").
					Queue("main").
					PodSets(*utiltesting.MakePodSet("one", 56).
						SetPodTemplateName("new-one").
						Obj()).
					Obj(),
			},
			podTemplates: []corev1.PodTemplate{
				*utiltesting.MakePodTemplate("new-one", "eng-alpha").
					Labels(map[string]string{constants.WorkloadNameLabel: "new"}).
					Request(corev1.ResourceCPU, "1").
					Obj(),
				*utiltesting.MakePodTemplate("new-one", "eng-beta").
					Labels(map[string]string{constants.WorkloadNameLabel: "new"}).
					Request(corev1.ResourceCPU, "1").
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
			wantLeft: map[string]sets.Set[string]{
				"eng-beta": sets.New("eng-beta/new"),
			},
		},
		"can borrow if cohort was assigned and will not result in overadmission": {
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("new", "eng-alpha").
					Queue("main").
					PodSets(*utiltesting.MakePodSet("one", 45).
						SetPodTemplateName("new-one").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("new", "eng-beta").
					Queue("main").
					PodSets(*utiltesting.MakePodSet("one", 55).
						SetPodTemplateName("new-one").
						Obj()).
					Obj(),
			},
			podTemplates: []corev1.PodTemplate{
				*utiltesting.MakePodTemplate("new-one", "eng-alpha").
					Labels(map[string]string{constants.WorkloadNameLabel: "new"}).
					Request(corev1.ResourceCPU, "1").
					Obj(),
				*utiltesting.MakePodTemplate("new-one", "eng-beta").
					Labels(map[string]string{constants.WorkloadNameLabel: "new"}).
					Request(corev1.ResourceCPU, "1").
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
					SetPodTemplateName("can-reclaim-main").
					Obj(),
				*utiltesting.MakeWorkload("needs-to-borrow", "eng-beta").
					Queue("main").
					SetPodTemplateName("needs-to-borrow-main").
					Obj(),
				*utiltesting.MakeWorkload("user-on-demand", "eng-beta").
					SetPodTemplateName("user-on-demand-main").
					ReserveQuota(utiltesting.MakeAdmission("eng-beta").Assignment(corev1.ResourceCPU, "on-demand", "50000m").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("user-spot", "eng-beta").
					SetPodTemplateName("user-spot-main").
					ReserveQuota(utiltesting.MakeAdmission("eng-beta").Assignment(corev1.ResourceCPU, "spot", "1000m").Obj()).
					Obj(),
			},
			podTemplates: []corev1.PodTemplate{
				*utiltesting.MakePodTemplate("can-reclaim-main", "eng-alpha").
					Labels(map[string]string{constants.WorkloadNameLabel: "can-reclaim"}).
					Request(corev1.ResourceCPU, "100").
					Obj(),
				*utiltesting.MakePodTemplate("needs-to-borrow-main", "eng-beta").
					Labels(map[string]string{constants.WorkloadNameLabel: "needs-to-borrow"}).
					Request(corev1.ResourceCPU, "1").
					Obj(),
				*utiltesting.MakePodTemplate("user-on-demand-main", "eng-beta").
					Labels(map[string]string{constants.WorkloadNameLabel: "user-on-demand"}).
					Request(corev1.ResourceCPU, "50").
					Obj(),
				*utiltesting.MakePodTemplate("user-spot-main", "eng-beta").
					Labels(map[string]string{constants.WorkloadNameLabel: "user-spot"}).
					Request(corev1.ResourceCPU, "1").
					Obj(),
			},
			wantLeft: map[string]sets.Set[string]{
				"eng-alpha": sets.New("eng-alpha/can-reclaim"),
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
		"preempt workloads in ClusterQueue and cohort": {
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("preemptor", "eng-beta").
					Queue("main").
					SetPodTemplateName("preemptor-main").
					Obj(),
				*utiltesting.MakeWorkload("use-all-spot", "eng-alpha").
					ReserveQuota(utiltesting.MakeAdmission("eng-alpha").Assignment(corev1.ResourceCPU, "spot", "100000m").Obj()).
					SetPodTemplateName("use-all-spot-main").
					Obj(),
				*utiltesting.MakeWorkload("low-1", "eng-beta").
					Priority(-1).
					SetPodTemplateName("low-1-main").
					ReserveQuota(utiltesting.MakeAdmission("eng-beta").Assignment(corev1.ResourceCPU, "on-demand", "30000m").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("low-2", "eng-beta").
					Priority(-2).
					SetPodTemplateName("low-2-main").
					ReserveQuota(utiltesting.MakeAdmission("eng-beta").Assignment(corev1.ResourceCPU, "on-demand", "10000m").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("borrower", "eng-alpha").
					SetPodTemplateName("borrower-main").
					ReserveQuota(utiltesting.MakeAdmission("eng-alpha").Assignment(corev1.ResourceCPU, "on-demand", "60000m").Obj()).
					Obj(),
			},
			podTemplates: []corev1.PodTemplate{
				*utiltesting.MakePodTemplate("preemptor-main", "eng-beta").
					Labels(map[string]string{constants.WorkloadNameLabel: "preemptor"}).
					Request(corev1.ResourceCPU, "20").
					Obj(),
				*utiltesting.MakePodTemplate("use-all-spot-main", "eng-alpha").
					Labels(map[string]string{constants.WorkloadNameLabel: "use-all-spot"}).
					Request(corev1.ResourceCPU, "100").
					Obj(),
				*utiltesting.MakePodTemplate("low-1-main", "eng-beta").
					Labels(map[string]string{constants.WorkloadNameLabel: "low-1"}).
					Request(corev1.ResourceCPU, "30").
					Obj(),
				*utiltesting.MakePodTemplate("low-2-main", "eng-beta").
					Labels(map[string]string{constants.WorkloadNameLabel: "low-2"}).
					Request(corev1.ResourceCPU, "10").
					Obj(),
				*utiltesting.MakePodTemplate("borrower-main", "eng-alpha").
					Labels(map[string]string{constants.WorkloadNameLabel: "borrower"}).
					Request(corev1.ResourceCPU, "60").
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
					Obj(),
			},
			podTemplates: []corev1.PodTemplate{
				*utiltesting.MakePodTemplate("main", "").
					Labels(map[string]string{constants.WorkloadNameLabel: "new"}).
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
						SetPodTemplateName("new-main").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("existing", "eng-beta").
					PodSets(*utiltesting.MakePodSet("one", 45).
						SetPodTemplateName("new-main").
						Obj()).
					ReserveQuota(utiltesting.MakeAdmission("eng-beta", "one").Assignment(corev1.ResourceCPU, "on-demand", "45000m").AssignmentPodCount(45).Obj()).
					Obj(),
			},
			podTemplates: []corev1.PodTemplate{
				*utiltesting.MakePodTemplate("new-main", "eng-alpha").
					Labels(map[string]string{constants.WorkloadNameLabel: "new"}).
					Request(corev1.ResourceCPU, "1").
					Obj(),
				*utiltesting.MakePodTemplate("new-main", "eng-beta").
					Labels(map[string]string{constants.WorkloadNameLabel: "existing"}).
					Request(corev1.ResourceCPU, "1").
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
					Obj(),
			},
			podTemplates: []corev1.PodTemplate{
				*utiltesting.MakePodTemplate("foo-main", "sales").
					Labels(map[string]string{constants.WorkloadNameLabel: "foo"}).
					Request(corev1.ResourceCPU, "1").
					Obj(),
			},
		},
		"workload should not fit in clusterQueue with nonexistent flavor": {
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("foo", "sales").
					Queue("flavor-nonexistent-queue").
					Obj(),
			},
			podTemplates: []corev1.PodTemplate{
				*utiltesting.MakePodTemplate("foo-main", "sales").
					Labels(map[string]string{constants.WorkloadNameLabel: "foo"}).
					Request(corev1.ResourceCPU, "1").
					Obj(),
			},
			wantLeft: map[string]sets.Set[string]{
				"flavor-nonexistent-cq": sets.New("sales/foo"),
			},
		},
		"no overadmission while borrowing": {
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("new", "eng-beta").
					Queue("main").
					Creation(time.Now().Add(-2 * time.Second)).
					PodSets(*utiltesting.MakePodSet("one", 50).
						SetPodTemplateName("new-one").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("new-alpha", "eng-alpha").
					Queue("main").
					Creation(time.Now().Add(-time.Second)).
					PodSets(*utiltesting.MakePodSet("one", 1).
						SetPodTemplateName("new-alpha-one").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("new-gamma", "eng-gamma").
					Queue("main").
					Creation(time.Now()).
					PodSets(*utiltesting.MakePodSet("one", 50).
						SetPodTemplateName("new-gamma-one").
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("existing", "eng-gamma").
					PodSets(
						*utiltesting.MakePodSet("borrow-on-demand", 51).
							SetPodTemplateName("existing-borrow-on-demand").
							Obj(),
						*utiltesting.MakePodSet("use-all-spot", 100).
							SetPodTemplateName("existing-use-all-spot").
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
			podTemplates: []corev1.PodTemplate{
				*utiltesting.MakePodTemplate("new-one", "eng-beta").
					Labels(map[string]string{constants.WorkloadNameLabel: "new"}).
					Request(corev1.ResourceCPU, "1").
					Obj(),
				*utiltesting.MakePodTemplate("new-alpha-one", "eng-alpha").
					Labels(map[string]string{constants.WorkloadNameLabel: "new-alpha"}).
					Request(corev1.ResourceCPU, "1").
					Obj(),
				*utiltesting.MakePodTemplate("new-gamma-one", "eng-gamma").
					Labels(map[string]string{constants.WorkloadNameLabel: "new-gamma"}).
					Request(corev1.ResourceCPU, "1").
					Obj(),
				*utiltesting.MakePodTemplate("existing-borrow-on-demand", "eng-gamma").
					Labels(map[string]string{constants.WorkloadNameLabel: "existing"}).
					Request(corev1.ResourceCPU, "1").
					Obj(),
				*utiltesting.MakePodTemplate("existing-use-all-spot", "eng-gamma").
					Labels(map[string]string{constants.WorkloadNameLabel: "existing"}).
					Request(corev1.ResourceCPU, "1").
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
			wantLeft: map[string]sets.Set[string]{
				"eng-gamma": sets.New("eng-gamma/new-gamma"),
			},
		},
		"partial admission single variable pod set": {
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("new", "sales").
					Queue("main").
					PodSets(*utiltesting.MakePodSet("one", 50).
						SetMinimumCount(20).
						SetPodTemplateName("new-one").
						Obj()).
					Obj(),
			},
			podTemplates: []corev1.PodTemplate{
				*utiltesting.MakePodTemplate("new-one", "sales").
					Labels(map[string]string{constants.WorkloadNameLabel: "new"}).
					Request(corev1.ResourceCPU, "2").
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
			wantScheduled:          []string{"sales/new"},
			enablePartialAdmission: true,
		},
		"partial admission single variable pod set, preempt first": {
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("new", "eng-beta").
					Queue("main").
					Priority(4).
					PodSets(*utiltesting.MakePodSet("one", 20).
						SetPodTemplateName("new-one").
						SetMinimumCount(10).
						Obj()).
					Obj(),
				*utiltesting.MakeWorkload("old", "eng-beta").
					Priority(-4).
					PodSets(*utiltesting.MakePodSet("one", 10).
						SetPodTemplateName("old-one").
						Obj()).
					ReserveQuota(utiltesting.MakeAdmission("eng-beta", "one").Assignment("example.com/gpu", "model-a", "10").AssignmentPodCount(10).Obj()).
					Obj(),
			},
			podTemplates: []corev1.PodTemplate{
				*utiltesting.MakePodTemplate("new-one", "eng-beta").
					Labels(map[string]string{constants.WorkloadNameLabel: "new"}).
					Request("example.com/gpu", "1").
					Obj(),
				*utiltesting.MakePodTemplate("old-one", "eng-beta").
					Labels(map[string]string{constants.WorkloadNameLabel: "old"}).
					Request("example.com/gpu", "1").
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
							SetPodTemplateName("new-one").
							Obj(),
						*utiltesting.MakePodSet("two", 30).
							SetPodTemplateName("new-two").
							SetMinimumCount(10).
							Obj(),
						*utiltesting.MakePodSet("three", 15).
							SetPodTemplateName("new-three").
							SetMinimumCount(5).
							Obj(),
					).
					Obj(),
			},
			podTemplates: []corev1.PodTemplate{
				*utiltesting.MakePodTemplate("new-one", "sales").
					Labels(map[string]string{constants.WorkloadNameLabel: "new"}).
					Request(corev1.ResourceCPU, "1").
					Obj(),
				*utiltesting.MakePodTemplate("new-two", "sales").
					Labels(map[string]string{constants.WorkloadNameLabel: "new"}).
					Request(corev1.ResourceCPU, "1").
					Obj(),
				*utiltesting.MakePodTemplate("new-three", "sales").
					Labels(map[string]string{constants.WorkloadNameLabel: "new"}).
					Request(corev1.ResourceCPU, "1").
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
			wantScheduled:          []string{"sales/new"},
			enablePartialAdmission: true,
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
				*utiltesting.MakeWorkload("wl1", "sales").Queue("lq1").Priority(-1).SetPodTemplateName("wl1-main").Obj(),
				*utiltesting.MakeWorkload("wl2", "sales").Queue("lq2").Priority(-2).SetPodTemplateName("wl2-main").Obj(),
			},
			podTemplates: []corev1.PodTemplate{
				*utiltesting.MakePodTemplate("wl1-main", "sales").
					Labels(map[string]string{constants.WorkloadNameLabel: "wl1"}).
					Request("r1", "16").
					Obj(),
				*utiltesting.MakePodTemplate("wl2-main", "sales").
					Labels(map[string]string{constants.WorkloadNameLabel: "wl2"}).
					Request("r2", "16").
					Obj(),
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
				*utiltesting.MakeWorkload("wl1", "sales").Queue("lq1").Priority(-1).SetPodTemplateName("wl1-main").Obj(),
				*utiltesting.MakeWorkload("wl2", "sales").Queue("lq2").Priority(-2).SetPodTemplateName("wl2-main").Obj(),
			},
			podTemplates: []corev1.PodTemplate{
				*utiltesting.MakePodTemplate("wl1-main", "sales").
					Labels(map[string]string{constants.WorkloadNameLabel: "wl1"}).
					Request("r1", "16").
					Obj(),
				*utiltesting.MakePodTemplate("wl2-main", "sales").
					Labels(map[string]string{constants.WorkloadNameLabel: "wl2"}).
					Request("r1", "14").
					Obj(),
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
				*utiltesting.MakeWorkload("wl1", "sales").Queue("lq1").Priority(-1).SetPodTemplateName("wl1-main").Obj(),
				*utiltesting.MakeWorkload("wl2", "sales").Queue("lq2").Priority(-2).SetPodTemplateName("wl2-main").Obj(),
			},
			podTemplates: []corev1.PodTemplate{
				*utiltesting.MakePodTemplate("wl1-main", "sales").
					Labels(map[string]string{constants.WorkloadNameLabel: "wl1"}).
					Request("r1", "16").
					Obj(),
				*utiltesting.MakePodTemplate("wl2-main", "sales").
					Labels(map[string]string{constants.WorkloadNameLabel: "wl2"}).
					Request("r1", "16").
					Obj(),
			},
			wantScheduled: []string{"sales/wl1"},
			wantAssignments: map[string]kueue.Admission{
				"sales/wl1": *utiltesting.MakeAdmission("cq1", "main").
					Assignment("r1", "default", "16").AssignmentPodCount(1).
					Obj(),
			},
			wantLeft: map[string]sets.Set[string]{
				"cq2": sets.New("sales/wl2"),
			},
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
				WithLists(&kueue.WorkloadList{Items: tc.workloads}, &kueue.LocalQueueList{Items: allQueues}, &corev1.PodTemplateList{Items: tc.podTemplates}).
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
					Priority: ptr.To[int32](1),
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

func TestLastSchedulingContext(t *testing.T) {
	resourceFlavors := []*kueue.ResourceFlavor{
		{ObjectMeta: metav1.ObjectMeta{Name: "on-demand"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "spot"}},
	}
	clusterQueue := []kueue.ClusterQueue{
		*utiltesting.MakeClusterQueue("eng-alpha").
			QueueingStrategy(kueue.StrictFIFO).
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
		Request(corev1.ResourceCPU, "50").
		ReserveQuota(utiltesting.MakeAdmission("eng-alpha").Assignment(corev1.ResourceCPU, "on-demand", "50").Obj()).
		Admitted(true).
		Obj()
	cases := []struct {
		name                           string
		cqs                            []kueue.ClusterQueue
		admittedWorkloads              []kueue.Workload
		workloads                      []kueue.Workload
		deletedWorkloads               []kueue.Workload
		wantPreempted                  sets.Set[string]
		wantAdmissionsOnFirstSchedule  map[string]kueue.Admission
		wantAdmissionsOnSecondSchedule map[string]kueue.Admission
	}{
		{
			name: "scheduling context not changed",
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
			deletedWorkloads:              []kueue.Workload{},
			wantPreempted:                 sets.Set[string]{},
			wantAdmissionsOnFirstSchedule: map[string]kueue.Admission{},
			wantAdmissionsOnSecondSchedule: map[string]kueue.Admission{
				"default/preemptor": *utiltesting.MakeAdmission("eng-alpha").Assignment(corev1.ResourceCPU, "spot", "20").Obj(),
				"default/low-1":     *utiltesting.MakeAdmission("eng-alpha").Assignment(corev1.ResourceCPU, "on-demand", "50").Obj(),
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
			deletedWorkloads: []kueue.Workload{
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
			deletedWorkloads: []kueue.Workload{},
			wantPreempted:    sets.Set[string]{},
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
			deletedWorkloads: []kueue.Workload{},
			wantPreempted:    sets.Set[string]{},
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
			name: "when the next flavor is full",
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
			deletedWorkloads: []kueue.Workload{},
			wantPreempted:    sets.Set[string]{},
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

			for _, wl := range tc.deletedWorkloads {
				err := cl.Delete(ctx, &wl)
				if err != nil {
					t.Errorf("Delete workload failed: %v", err)
				}
				err = cqCache.DeleteWorkload(&wl)
				if err != nil {
					t.Errorf("Delete workload failed: %v", err)
				}
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
						Type:    kueue.WorkloadQuotaReserved,
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
