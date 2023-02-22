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

	"github.com/go-logr/logr/testr"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1alpha2"
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/queue"
	"sigs.k8s.io/kueue/pkg/scheduler/flavorassigner"
	"sigs.k8s.io/kueue/pkg/util/pointer"
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
		{
			ObjectMeta: metav1.ObjectMeta{Name: "sales"},
			Spec: kueue.ClusterQueueSpec{
				NamespaceSelector: &metav1.LabelSelector{
					MatchExpressions: []metav1.LabelSelectorRequirement{
						{
							Key:      "dep",
							Operator: metav1.LabelSelectorOpIn,
							Values:   []string{"sales"},
						},
					},
				},
				QueueingStrategy: kueue.StrictFIFO,
				Resources: []kueue.Resource{
					{
						Name: corev1.ResourceCPU,
						Flavors: []kueue.Flavor{
							{
								Name: "default",
								Quota: kueue.Quota{
									Min: resource.MustParse("50"),
									Max: pointer.Quantity(resource.MustParse("50")),
								},
							},
						},
					},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "eng-alpha"},
			Spec: kueue.ClusterQueueSpec{
				Cohort: "eng",
				NamespaceSelector: &metav1.LabelSelector{
					MatchExpressions: []metav1.LabelSelectorRequirement{
						{
							Key:      "dep",
							Operator: metav1.LabelSelectorOpIn,
							Values:   []string{"eng"},
						},
					},
				},
				QueueingStrategy: kueue.StrictFIFO,
				Resources: []kueue.Resource{
					{
						Name: corev1.ResourceCPU,
						Flavors: []kueue.Flavor{
							{
								Name: "on-demand",
								Quota: kueue.Quota{
									Min: resource.MustParse("50"),
									Max: pointer.Quantity(resource.MustParse("100")),
								},
							},
							{
								Name: "spot",
								Quota: kueue.Quota{
									Min: resource.MustParse("100"),
									Max: pointer.Quantity(resource.MustParse("100")),
								},
							},
						},
					},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "eng-beta"},
			Spec: kueue.ClusterQueueSpec{
				Cohort: "eng",
				NamespaceSelector: &metav1.LabelSelector{
					MatchExpressions: []metav1.LabelSelectorRequirement{
						{
							Key:      "dep",
							Operator: metav1.LabelSelectorOpIn,
							Values:   []string{"eng"},
						},
					},
				},
				Preemption: &kueue.ClusterQueuePreemption{
					ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
				},
				QueueingStrategy: kueue.StrictFIFO,
				Resources: []kueue.Resource{
					{
						Name: corev1.ResourceCPU,
						Flavors: []kueue.Flavor{
							{
								Name: "on-demand",
								Quota: kueue.Quota{
									Min: resource.MustParse("50"),
									Max: pointer.Quantity(resource.MustParse("60")),
								},
							},
							{
								Name: "spot",
								Quota: kueue.Quota{
									Min: resource.MustParse("0"),
									Max: pointer.Quantity(resource.MustParse("100")),
								},
							},
						},
					},
					{
						Name: "example.com/gpu",
						Flavors: []kueue.Flavor{
							{
								Name: "model-a",
								Quota: kueue.Quota{
									Min: resource.MustParse("20"),
									Max: pointer.Quantity(resource.MustParse("20")),
								},
							},
						},
					},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "flavor-nonexistent-cq"},
			Spec: kueue.ClusterQueueSpec{
				QueueingStrategy: kueue.StrictFIFO,
				Resources: []kueue.Resource{
					{
						Name: corev1.ResourceCPU,
						Flavors: []kueue.Flavor{
							{
								Name: "nonexistent-flavor",
								Quota: kueue.Quota{
									Min: resource.MustParse("50"),
								},
							},
						},
					},
				},
			},
		},
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
	}{
		"workload fits in single clusterQueue": {
			workloads: []kueue.Workload{
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "sales",
						Name:      "foo",
					},
					Spec: kueue.WorkloadSpec{
						QueueName: "main",
						PodSets: []kueue.PodSet{
							{
								Name:  "one",
								Count: 10,
								Spec: utiltesting.PodSpecForRequest(map[corev1.ResourceName]string{
									corev1.ResourceCPU: "1",
								}),
							},
						},
					},
				},
			},
			wantAssignments: map[string]kueue.Admission{
				"sales/foo": {
					ClusterQueue: "sales",
					PodSetFlavors: []kueue.PodSetFlavors{
						{
							Name: "one",
							Flavors: map[corev1.ResourceName]string{
								corev1.ResourceCPU: "default",
							},
						},
					},
				},
			},
			wantScheduled: []string{"sales/foo"},
		},
		"error during admission": {
			workloads: []kueue.Workload{
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "sales",
						Name:      "foo",
					},
					Spec: kueue.WorkloadSpec{
						QueueName: "main",
						PodSets: []kueue.PodSet{
							{
								Name:  "one",
								Count: 10,
								Spec: utiltesting.PodSpecForRequest(map[corev1.ResourceName]string{
									corev1.ResourceCPU: "1",
								}),
							},
						},
					},
				},
			},
			admissionError: errors.New("admission"),
			wantLeft: map[string]sets.Set[string]{
				"sales": sets.New("sales/foo"),
			},
		},
		"single clusterQueue full": {
			workloads: []kueue.Workload{
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "sales",
						Name:      "new",
					},
					Spec: kueue.WorkloadSpec{
						QueueName: "main",
						PodSets: []kueue.PodSet{
							{
								Name:  "one",
								Count: 11,
								Spec: utiltesting.PodSpecForRequest(map[corev1.ResourceName]string{
									corev1.ResourceCPU: "1",
								}),
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "sales",
						Name:      "assigned",
					},
					Spec: kueue.WorkloadSpec{
						PodSets: []kueue.PodSet{
							{
								Name:  "one",
								Count: 40,
								Spec: utiltesting.PodSpecForRequest(map[corev1.ResourceName]string{
									corev1.ResourceCPU: "1",
								}),
							},
						},
						Admission: &kueue.Admission{
							ClusterQueue: "sales",
							PodSetFlavors: []kueue.PodSetFlavors{
								{
									Name: "one",
									Flavors: map[corev1.ResourceName]string{
										corev1.ResourceCPU: "default",
									},
								},
							},
						},
					},
				},
			},
			wantAssignments: map[string]kueue.Admission{
				"sales/assigned": {
					ClusterQueue: "sales",
					PodSetFlavors: []kueue.PodSetFlavors{
						{
							Name: "one",
							Flavors: map[corev1.ResourceName]string{
								corev1.ResourceCPU: "default",
							},
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
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "sales",
						Name:      "new",
					},
					Spec: kueue.WorkloadSpec{
						QueueName: "blocked",
						PodSets: []kueue.PodSet{
							{
								Name:  "one",
								Count: 1,
								Spec: utiltesting.PodSpecForRequest(map[corev1.ResourceName]string{
									corev1.ResourceCPU: "1",
								}),
							},
						},
					},
				},
			},
			wantInadmissibleLeft: map[string]sets.Set[string]{
				"eng-alpha": sets.New("sales/new"),
			},
		},
		"assign to different cohorts": {
			workloads: []kueue.Workload{
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "sales",
						Name:      "new",
					},
					Spec: kueue.WorkloadSpec{
						QueueName: "main",
						PodSets: []kueue.PodSet{
							{
								Name:  "one",
								Count: 1,
								Spec: utiltesting.PodSpecForRequest(map[corev1.ResourceName]string{
									corev1.ResourceCPU: "1",
								}),
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "eng-alpha",
						Name:      "new",
					},
					Spec: kueue.WorkloadSpec{
						QueueName: "main",
						PodSets: []kueue.PodSet{
							{
								Name:  "one",
								Count: 51, // will borrow.
								Spec: utiltesting.PodSpecForRequest(map[corev1.ResourceName]string{
									corev1.ResourceCPU: "1",
								}),
							},
						},
					},
				},
			},
			wantAssignments: map[string]kueue.Admission{
				"sales/new": {
					ClusterQueue: "sales",
					PodSetFlavors: []kueue.PodSetFlavors{
						{
							Name: "one",
							Flavors: map[corev1.ResourceName]string{
								corev1.ResourceCPU: "default",
							},
						},
					},
				},
				"eng-alpha/new": {
					ClusterQueue: "eng-alpha",
					PodSetFlavors: []kueue.PodSetFlavors{
						{
							Name: "one",
							Flavors: map[corev1.ResourceName]string{
								corev1.ResourceCPU: "on-demand",
							},
						},
					},
				},
			},
			wantScheduled: []string{"sales/new", "eng-alpha/new"},
		},
		"assign to same cohort no borrowing": {
			workloads: []kueue.Workload{
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "eng-alpha",
						Name:      "new",
					},
					Spec: kueue.WorkloadSpec{
						QueueName: "main",
						PodSets: []kueue.PodSet{
							{
								Name:  "one",
								Count: 40,
								Spec: utiltesting.PodSpecForRequest(map[corev1.ResourceName]string{
									corev1.ResourceCPU: "1",
								}),
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "eng-beta",
						Name:      "new",
					},
					Spec: kueue.WorkloadSpec{
						QueueName: "main",
						PodSets: []kueue.PodSet{
							{
								Name:  "one",
								Count: 40,
								Spec: utiltesting.PodSpecForRequest(map[corev1.ResourceName]string{
									corev1.ResourceCPU: "1",
								}),
							},
						},
					},
				},
			},
			wantAssignments: map[string]kueue.Admission{
				"eng-alpha/new": {
					ClusterQueue: "eng-alpha",
					PodSetFlavors: []kueue.PodSetFlavors{
						{
							Name: "one",
							Flavors: map[corev1.ResourceName]string{
								corev1.ResourceCPU: "on-demand",
							},
						},
					},
				},
				"eng-beta/new": {
					ClusterQueue: "eng-beta",
					PodSetFlavors: []kueue.PodSetFlavors{
						{
							Name: "one",
							Flavors: map[corev1.ResourceName]string{
								corev1.ResourceCPU: "on-demand",
							},
						},
					},
				},
			},
			wantScheduled: []string{"eng-alpha/new", "eng-beta/new"},
		},
		"assign multiple resources and flavors": {
			workloads: []kueue.Workload{
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "eng-beta",
						Name:      "new",
					},
					Spec: kueue.WorkloadSpec{
						QueueName: "main",
						PodSets: []kueue.PodSet{
							{
								Name:  "one",
								Count: 10,
								Spec: utiltesting.PodSpecForRequest(map[corev1.ResourceName]string{
									corev1.ResourceCPU: "6", // Needs to borrow.
									"example.com/gpu":  "1",
								}),
							},
							{
								Name:  "two",
								Count: 40,
								Spec: utiltesting.PodSpecForRequest(map[corev1.ResourceName]string{
									corev1.ResourceCPU: "1",
								}),
							},
						},
					},
				},
			},
			wantAssignments: map[string]kueue.Admission{
				"eng-beta/new": {
					ClusterQueue: "eng-beta",
					PodSetFlavors: []kueue.PodSetFlavors{
						{
							Name: "one",
							Flavors: map[corev1.ResourceName]string{
								corev1.ResourceCPU: "on-demand",
								"example.com/gpu":  "model-a",
							},
						},
						{
							Name: "two",
							Flavors: map[corev1.ResourceName]string{
								corev1.ResourceCPU: "spot",
							},
						},
					},
				},
			},
			wantScheduled: []string{"eng-beta/new"},
		},
		"cannot borrow if cohort was assigned": {
			workloads: []kueue.Workload{
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "eng-alpha",
						Name:      "new",
					},
					Spec: kueue.WorkloadSpec{
						QueueName: "main",
						PodSets: []kueue.PodSet{
							{
								Name:  "one",
								Count: 40,
								Spec: utiltesting.PodSpecForRequest(map[corev1.ResourceName]string{
									corev1.ResourceCPU: "1",
								}),
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "eng-beta",
						Name:      "new",
					},
					Spec: kueue.WorkloadSpec{
						QueueName: "main",
						PodSets: []kueue.PodSet{
							{
								Name:  "one",
								Count: 51,
								Spec: utiltesting.PodSpecForRequest(map[corev1.ResourceName]string{
									corev1.ResourceCPU: "1",
								}),
							},
						},
					},
				},
			},
			wantAssignments: map[string]kueue.Admission{
				"eng-alpha/new": {
					ClusterQueue: "eng-alpha",
					PodSetFlavors: []kueue.PodSetFlavors{
						{
							Name: "one",
							Flavors: map[corev1.ResourceName]string{
								corev1.ResourceCPU: "on-demand",
							},
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
					Admit(utiltesting.MakeAdmission("eng-beta").Flavor(corev1.ResourceCPU, "on-demand").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("user-spot", "eng-beta").
					Request(corev1.ResourceCPU, "1").
					Admit(utiltesting.MakeAdmission("eng-beta").Flavor(corev1.ResourceCPU, "spot").Obj()).
					Obj(),
			},
			wantLeft: map[string]sets.Set[string]{
				"eng-alpha": sets.New("eng-alpha/can-reclaim"),
				"eng-beta":  sets.New("eng-beta/needs-to-borrow"),
			},
			wantAssignments: map[string]kueue.Admission{
				"eng-beta/user-spot":      *utiltesting.MakeAdmission("eng-beta").Flavor(corev1.ResourceCPU, "spot").Obj(),
				"eng-beta/user-on-demand": *utiltesting.MakeAdmission("eng-beta").Flavor(corev1.ResourceCPU, "on-demand").Obj(),
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
					Admit(utiltesting.MakeAdmission("eng-alpha").Flavor(corev1.ResourceCPU, "spot").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("low-1", "eng-beta").
					Priority(-1).
					Request(corev1.ResourceCPU, "30").
					Admit(utiltesting.MakeAdmission("eng-beta").Flavor(corev1.ResourceCPU, "on-demand").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("low-2", "eng-beta").
					Priority(-2).
					Request(corev1.ResourceCPU, "10").
					Admit(utiltesting.MakeAdmission("eng-beta").Flavor(corev1.ResourceCPU, "on-demand").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("borrower", "eng-alpha").
					Request(corev1.ResourceCPU, "60").
					Admit(utiltesting.MakeAdmission("eng-alpha").Flavor(corev1.ResourceCPU, "on-demand").Obj()).
					Obj(),
			},
			wantLeft: map[string]sets.Set[string]{
				// Preemptor is not admitted in this cycle.
				"eng-beta": sets.New("eng-beta/preemptor"),
			},
			wantPreempted: sets.New("eng-alpha/borrower", "eng-beta/low-2"),
			wantAssignments: map[string]kueue.Admission{
				"eng-alpha/use-all-spot": *utiltesting.MakeAdmission("eng-alpha").Flavor(corev1.ResourceCPU, "spot").Obj(),
				"eng-beta/low-1":         *utiltesting.MakeAdmission("eng-beta").Flavor(corev1.ResourceCPU, "on-demand").Obj(),
				// Removal from cache for the preempted workloads is deferred until we receive Workload updates
				"eng-beta/low-2":     *utiltesting.MakeAdmission("eng-beta").Flavor(corev1.ResourceCPU, "on-demand").Obj(),
				"eng-alpha/borrower": *utiltesting.MakeAdmission("eng-alpha").Flavor(corev1.ResourceCPU, "on-demand").Obj(),
			},
		},
		"cannot borrow resource not listed in clusterQueue": {
			workloads: []kueue.Workload{
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "eng-alpha",
						Name:      "new",
					},
					Spec: kueue.WorkloadSpec{
						QueueName: "main",
						PodSets: []kueue.PodSet{
							{
								Name:  "one",
								Count: 1,
								Spec: utiltesting.PodSpecForRequest(map[corev1.ResourceName]string{
									"example.com/gpu": "1",
								}),
							},
						},
					},
				},
			},
			wantLeft: map[string]sets.Set[string]{
				"eng-alpha": sets.New("eng-alpha/new"),
			},
		},
		"not enough resources to borrow, fallback to next flavor": {
			workloads: []kueue.Workload{
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "eng-alpha",
						Name:      "new",
					},
					Spec: kueue.WorkloadSpec{
						QueueName: "main",
						PodSets: []kueue.PodSet{
							{
								Name:  "one",
								Count: 60,
								Spec: utiltesting.PodSpecForRequest(map[corev1.ResourceName]string{
									corev1.ResourceCPU: "1",
								}),
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "eng-beta",
						Name:      "existing",
					},
					Spec: kueue.WorkloadSpec{
						PodSets: []kueue.PodSet{
							{
								Name:  "one",
								Count: 45,
								Spec: utiltesting.PodSpecForRequest(map[corev1.ResourceName]string{
									corev1.ResourceCPU: "1",
								}),
							},
						},
						Admission: &kueue.Admission{
							ClusterQueue: "eng-beta",
							PodSetFlavors: []kueue.PodSetFlavors{
								{
									Name: "one",
									Flavors: map[corev1.ResourceName]string{
										corev1.ResourceCPU: "on-demand",
									},
								},
							},
						},
					},
				},
			},
			wantAssignments: map[string]kueue.Admission{
				"eng-alpha/new": {
					ClusterQueue: "eng-alpha",
					PodSetFlavors: []kueue.PodSetFlavors{
						{
							Name: "one",
							Flavors: map[corev1.ResourceName]string{
								corev1.ResourceCPU: "spot",
							},
						},
					},
				},
				"eng-beta/existing": {
					ClusterQueue: "eng-beta",
					PodSetFlavors: []kueue.PodSetFlavors{
						{
							Name: "one",
							Flavors: map[corev1.ResourceName]string{
								corev1.ResourceCPU: "on-demand",
							},
						},
					},
				},
			},
			wantScheduled: []string{"eng-alpha/new"},
		},
		"workload should not fit in nonexistent clusterQueue": {
			workloads: []kueue.Workload{
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "sales",
						Name:      "foo",
					},
					Spec: kueue.WorkloadSpec{
						QueueName: "cq-nonexistent-queue",
						PodSets: []kueue.PodSet{
							{
								Name:  "one",
								Count: 10,
								Spec: utiltesting.PodSpecForRequest(map[corev1.ResourceName]string{
									corev1.ResourceCPU: "1",
								}),
							},
						},
					},
				},
			},
		},
		"workload should not fit in flavor nonexistent clusterQueue": {
			workloads: []kueue.Workload{
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "sales",
						Name:      "foo",
					},
					Spec: kueue.WorkloadSpec{
						QueueName: "flavor-nonexistent-queue",
						PodSets: []kueue.PodSet{
							{
								Name:  "one",
								Count: 10,
								Spec: utiltesting.PodSpecForRequest(map[corev1.ResourceName]string{
									corev1.ResourceCPU: "1",
								}),
							},
						},
					},
				},
			},
			wantLeft: map[string]sets.Set[string]{
				"flavor-nonexistent-cq": sets.New("sales/foo"),
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			log := testr.NewWithOptions(t, testr.Options{
				Verbosity: 2,
			})
			ctx := ctrl.LoggerInto(context.Background(), log)
			scheme := runtime.NewScheme()
			clientBuilder := utiltesting.NewClientBuilder().
				WithLists(&kueue.WorkloadList{Items: tc.workloads}, &kueue.LocalQueueList{Items: queues}).
				WithObjects(
					&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "eng-alpha", Labels: map[string]string{"dep": "eng"}}},
					&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "eng-beta", Labels: map[string]string{"dep": "eng"}}},
					&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "sales", Labels: map[string]string{"dep": "sales"}}},
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
			for _, cq := range clusterQueues {
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
				gotScheduled[workload.Key(w)] = *w.Spec.Admission
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
					if w.Obj.Spec.Admission == nil {
						t.Errorf("Workload %s is not admitted by a clusterQueue, but it is found as member of clusterQueue %s in the cache", name, cqName)
					} else if string(w.Obj.Spec.Admission.ClusterQueue) != cqName {
						t.Errorf("Workload %s is admitted by clusterQueue %s, but it is found as member of clusterQueue %s in the cache", name, w.Obj.Spec.Admission.ClusterQueue, cqName)
					}
					gotAssignments[name] = *w.Obj.Spec.Admission
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
					Name:              "alpha",
					CreationTimestamp: metav1.NewTime(now),
				}},
			},
			assignment: flavorassigner.Assignment{
				TotalBorrow: cache.ResourceQuantities{
					corev1.ResourceCPU: {},
				},
			},
		},
		{
			Info: workload.Info{
				Obj: &kueue.Workload{ObjectMeta: metav1.ObjectMeta{
					Name:              "beta",
					CreationTimestamp: metav1.NewTime(now.Add(time.Second)),
				}},
			},
		},
		{
			Info: workload.Info{
				Obj: &kueue.Workload{ObjectMeta: metav1.ObjectMeta{
					Name:              "gamma",
					CreationTimestamp: metav1.NewTime(now.Add(2 * time.Second)),
				}},
			},
		},
		{
			Info: workload.Info{
				Obj: &kueue.Workload{ObjectMeta: metav1.ObjectMeta{
					Name:              "delta",
					CreationTimestamp: metav1.NewTime(now.Add(time.Second)),
				}},
			},
			assignment: flavorassigner.Assignment{
				TotalBorrow: cache.ResourceQuantities{
					corev1.ResourceCPU: {},
				},
			},
		},
	}
	sort.Sort(entryOrdering(input))
	order := make([]string, len(input))
	for i, e := range input {
		order[i] = e.Obj.Name
	}
	wantOrder := []string{"beta", "gamma", "alpha", "delta"}
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
			log := testr.NewWithOptions(t, testr.Options{
				Verbosity: 2,
			})
			ctx := ctrl.LoggerInto(context.Background(), log)
			scheme := runtime.NewScheme()

			clientBuilder := utiltesting.NewClientBuilder().WithObjects(w1, q1, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ns1"}})
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
