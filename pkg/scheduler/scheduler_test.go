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
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	logrtesting "github.com/go-logr/logr/testing"
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
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1alpha2"
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/queue"
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
		workloads []kueue.Workload
		// wantAssignments is a summary of all the admissions in the cache after this cycle.
		wantAssignments map[string]kueue.Admission
		// wantScheduled is the subset of workloads that got scheduled/admitted in this cycle.
		wantScheduled []string
		// wantLeft is the workload keys that are left in the queues after this cycle.
		wantLeft map[string]sets.String
		// wantInadmissibleLeft is the workload keys that are left in the inadmissible state after this cycle.
		wantInadmissibleLeft map[string]sets.String
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
			wantLeft: map[string]sets.String{
				"sales": sets.NewString("new"),
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
			wantInadmissibleLeft: map[string]sets.String{
				"eng-alpha": sets.NewString("new"),
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
			wantLeft: map[string]sets.String{
				"eng-beta": sets.NewString("new"),
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
			wantLeft: map[string]sets.String{
				"eng-alpha": sets.NewString("new"),
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
			wantLeft: map[string]sets.String{
				"flavor-nonexistent-cq": sets.NewString("foo"),
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			log := logrtesting.NewTestLoggerWithOptions(t, logrtesting.Options{
				Verbosity: 2,
			})
			ctx := ctrl.LoggerInto(context.Background(), log)
			scheme := runtime.NewScheme()
			if err := kueue.AddToScheme(scheme); err != nil {
				t.Fatalf("Failed adding kueue scheme: %v", err)
			}
			if err := corev1.AddToScheme(scheme); err != nil {
				t.Fatalf("Failed adding kueue scheme: %v", err)
			}
			clientBuilder := fake.NewClientBuilder().WithScheme(scheme).
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

func TestEntryAssignFlavors(t *testing.T) {
	resourceFlavors := map[string]*kueue.ResourceFlavor{
		"default": {
			ObjectMeta: metav1.ObjectMeta{Name: "default"},
		},
		"one": {
			ObjectMeta: metav1.ObjectMeta{
				Name:   "one",
				Labels: map[string]string{"type": "one"},
			},
		},
		"two": {
			ObjectMeta: metav1.ObjectMeta{
				Name:   "two",
				Labels: map[string]string{"type": "two"},
			},
		},
		"b_one": {
			ObjectMeta: metav1.ObjectMeta{
				Name:   "b_one",
				Labels: map[string]string{"b_type": "one"},
			},
		},
		"b_two": {
			ObjectMeta: metav1.ObjectMeta{
				Name:   "b_two",
				Labels: map[string]string{"b_type": "two"},
			},
		},
		"tainted": {
			ObjectMeta: metav1.ObjectMeta{Name: "tainted"},
			Taints: []corev1.Taint{{
				Key:    "instance",
				Value:  "spot",
				Effect: corev1.TaintEffectNoSchedule,
			}},
		},
	}

	cases := map[string]struct {
		wlPods       []kueue.PodSet
		clusterQueue cache.ClusterQueue
		wantFits     bool
		wantFlavors  map[string]map[corev1.ResourceName]string
		wantBorrows  cache.ResourceQuantities
		wantMsg      string
	}{
		"single flavor, fits": {
			wlPods: []kueue.PodSet{
				{
					Count: 1,
					Name:  "main",
					Spec: utiltesting.PodSpecForRequest(map[corev1.ResourceName]string{
						corev1.ResourceCPU:    "1",
						corev1.ResourceMemory: "1Mi",
					}),
				},
			},
			clusterQueue: cache.ClusterQueue{
				RequestableResources: map[corev1.ResourceName]*cache.Resource{
					corev1.ResourceCPU:    {Flavors: []cache.FlavorLimits{{Name: "default", Min: 1000}}},
					corev1.ResourceMemory: {Flavors: []cache.FlavorLimits{{Name: "default", Min: 2 * utiltesting.Mi}}},
				},
			},
			wantFits: true,
			wantFlavors: map[string]map[corev1.ResourceName]string{
				"main": {
					corev1.ResourceCPU:    "default",
					corev1.ResourceMemory: "default",
				},
			},
		},
		"single flavor, fits tainted flavor": {
			wlPods: []kueue.PodSet{
				{
					Count: 1,
					Name:  "main",
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Resources: corev1.ResourceRequirements{
									Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")},
								},
							},
						},
						Tolerations: []corev1.Toleration{{
							Key:      "instance",
							Operator: corev1.TolerationOpEqual,
							Value:    "spot",
							Effect:   corev1.TaintEffectNoSchedule,
						}},
					},
				},
			},
			clusterQueue: cache.ClusterQueue{
				RequestableResources: map[corev1.ResourceName]*cache.Resource{
					corev1.ResourceCPU: {
						Flavors: []cache.FlavorLimits{{Name: "tainted", Min: 4000}},
					},
				},
			},
			wantFits: true,
			wantFlavors: map[string]map[corev1.ResourceName]string{
				"main": {
					corev1.ResourceCPU: "tainted",
				},
			},
		},
		"single flavor, used resources, doesn't fit": {
			wlPods: []kueue.PodSet{
				{
					Count: 1,
					Name:  "main",
					Spec: utiltesting.PodSpecForRequest(map[corev1.ResourceName]string{
						corev1.ResourceCPU: "2",
					}),
				},
			},
			clusterQueue: cache.ClusterQueue{
				RequestableResources: map[corev1.ResourceName]*cache.Resource{
					corev1.ResourceCPU: {Flavors: []cache.FlavorLimits{{Name: "default", Min: 4000}}},
				},
				UsedResources: cache.ResourceQuantities{
					corev1.ResourceCPU: {
						"default": 3_000,
					},
				},
			},
			wantMsg: "insufficient quota for cpu flavor default, 1 more needed",
		},
		"multiple independent flavors, fits": {
			wlPods: []kueue.PodSet{
				{
					Count: 1,
					Name:  "main",
					Spec: utiltesting.PodSpecForRequest(map[corev1.ResourceName]string{
						corev1.ResourceCPU:    "3",
						corev1.ResourceMemory: "10Mi",
					}),
				},
			},
			clusterQueue: cache.ClusterQueue{
				RequestableResources: map[corev1.ResourceName]*cache.Resource{
					corev1.ResourceCPU: {
						Flavors: []cache.FlavorLimits{
							{Name: "one", Min: 2000},
							{Name: "two", Min: 4000},
						},
					},
					corev1.ResourceMemory: {
						Flavors: []cache.FlavorLimits{
							{Name: "b_one", Min: utiltesting.Gi},
							{Name: "b_two", Min: 5 * utiltesting.Mi},
						},
					},
				},
			},
			wantFits: true,
			wantFlavors: map[string]map[corev1.ResourceName]string{
				"main": {
					corev1.ResourceCPU:    "two",
					corev1.ResourceMemory: "b_one",
				},
			},
		},
		"some codependent flavors, fits": {
			wlPods: []kueue.PodSet{
				{
					Count: 1,
					Name:  "main",
					Spec: utiltesting.PodSpecForRequest(map[corev1.ResourceName]string{
						corev1.ResourceCPU:    "3",
						corev1.ResourceMemory: "10Mi",
						"example.com/gpu":     "3",
					}),
				},
			},
			clusterQueue: cache.ClusterQueue{
				RequestableResources: map[corev1.ResourceName]*cache.Resource{
					corev1.ResourceCPU: {
						Flavors: []cache.FlavorLimits{
							{Name: "one", Min: 2000},
							{Name: "two", Min: 4000},
						},
					},
					corev1.ResourceMemory: {
						Flavors: []cache.FlavorLimits{
							{Name: "one", Min: utiltesting.Gi},
							{Name: "two", Min: 15 * utiltesting.Mi},
						},
					},
					"example.com/gpu": {
						Flavors: []cache.FlavorLimits{
							{Name: "b_one", Min: 4},
							{Name: "b_two", Min: 2},
						},
					},
				},
			},
			wantFits: true,
			wantFlavors: map[string]map[corev1.ResourceName]string{
				"main": {
					corev1.ResourceCPU:    "two",
					corev1.ResourceMemory: "two",
					"example.com/gpu":     "b_one",
				},
			},
		},
		"codependent flavors, doesn't fit": {
			wlPods: []kueue.PodSet{
				{
					Count: 1,
					Name:  "main",
					Spec: utiltesting.PodSpecForRequest(map[corev1.ResourceName]string{
						corev1.ResourceCPU:    "3",
						corev1.ResourceMemory: "10Mi",
					}),
				},
			},
			clusterQueue: cache.ClusterQueue{
				RequestableResources: map[corev1.ResourceName]*cache.Resource{
					corev1.ResourceCPU: {
						Flavors: []cache.FlavorLimits{
							{Name: "one", Min: 2000},
							{Name: "two", Min: 4000},
						},
					},
					corev1.ResourceMemory: {
						Flavors: []cache.FlavorLimits{
							{Name: "one", Min: utiltesting.Gi},
							{Name: "two", Min: 5 * utiltesting.Mi},
						},
					},
				},
			},
			wantMsg: "insufficient quota for cpu flavor one, 1 more needed; insufficient quota for memory flavor two, 5Mi more needed",
		},
		"multiple flavors, fits while skipping tainted flavor": {
			wlPods: []kueue.PodSet{
				{
					Count: 1,
					Name:  "main",
					Spec: utiltesting.PodSpecForRequest(map[corev1.ResourceName]string{
						corev1.ResourceCPU: "3",
					}),
				},
			},
			clusterQueue: cache.ClusterQueue{
				RequestableResources: map[corev1.ResourceName]*cache.Resource{
					corev1.ResourceCPU: {
						Flavors: []cache.FlavorLimits{
							{Name: "tainted", Min: 4000},
							{Name: "two", Min: 4000},
						},
					},
				},
			},
			wantFits: true,
			wantFlavors: map[string]map[corev1.ResourceName]string{
				"main": {
					corev1.ResourceCPU: "two",
				},
			},
		},
		"multiple flavors, skip missing ResourceFlavor": {
			wlPods: []kueue.PodSet{
				{
					Count: 1,
					Name:  "main",
					Spec: utiltesting.PodSpecForRequest(map[corev1.ResourceName]string{
						corev1.ResourceCPU: "3",
					}),
				},
			},
			clusterQueue: cache.ClusterQueue{
				RequestableResources: map[corev1.ResourceName]*cache.Resource{
					corev1.ResourceCPU: {
						Flavors: []cache.FlavorLimits{
							{Name: "non-existent", Min: 4000},
							{Name: "two", Min: 4000},
						},
					},
				},
			},
			wantFits: true,
			wantFlavors: map[string]map[corev1.ResourceName]string{
				"main": {
					corev1.ResourceCPU: "two",
				},
			},
		},
		"multiple flavors, fits a node selector": {
			wlPods: []kueue.PodSet{
				{
					Count: 1,
					Name:  "main",
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Resources: corev1.ResourceRequirements{
									Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")},
								},
							},
						},
						// ignored:foo should get ignored
						NodeSelector: map[string]string{"type": "two", "ignored1": "foo"},
						Affinity: &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
								NodeSelectorTerms: []corev1.NodeSelectorTerm{
									{
										MatchExpressions: []corev1.NodeSelectorRequirement{
											{
												// this expression should get ignored
												Key:      "ignored2",
												Operator: corev1.NodeSelectorOpIn,
												Values:   []string{"bar"},
											},
										},
									},
								},
							}},
						},
					},
				},
			},
			clusterQueue: cache.ClusterQueue{
				RequestableResources: map[corev1.ResourceName]*cache.Resource{
					corev1.ResourceCPU: {
						Flavors: []cache.FlavorLimits{
							{Name: "non-existent", Min: 4000},
							{Name: "one", Min: 4000},
							{Name: "two", Min: 4000},
						},
					},
				},
				LabelKeys: map[corev1.ResourceName]sets.String{corev1.ResourceCPU: sets.NewString("cpuType")},
			},
			wantFits: true,
			wantFlavors: map[string]map[corev1.ResourceName]string{
				"main": {
					corev1.ResourceCPU: "two",
				},
			},
		},
		"multiple flavors, fits with node affinity": {
			wlPods: []kueue.PodSet{
				{
					Count: 1,
					Name:  "main",
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Resources: corev1.ResourceRequirements{
									Requests: corev1.ResourceList{
										corev1.ResourceCPU:    resource.MustParse("1"),
										corev1.ResourceMemory: resource.MustParse("1Mi"),
									},
								},
							},
						},
						NodeSelector: map[string]string{"ignored1": "foo"},
						Affinity: &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
								NodeSelectorTerms: []corev1.NodeSelectorTerm{
									{
										MatchExpressions: []corev1.NodeSelectorRequirement{
											{
												Key:      "type",
												Operator: corev1.NodeSelectorOpIn,
												Values:   []string{"two"},
											},
										},
									},
								},
							}},
						},
					},
				},
			},
			clusterQueue: cache.ClusterQueue{
				RequestableResources: map[corev1.ResourceName]*cache.Resource{
					corev1.ResourceCPU: {
						Flavors: []cache.FlavorLimits{
							{Name: "one", Min: 4000},
							{Name: "two", Min: 4000},
						},
					},
					corev1.ResourceMemory: {
						Flavors: []cache.FlavorLimits{
							{Name: "one", Min: utiltesting.Gi},
							{Name: "two", Min: utiltesting.Gi},
						},
					},
				},
			},
			wantFits: true,
			wantFlavors: map[string]map[corev1.ResourceName]string{
				"main": {
					corev1.ResourceCPU:    "two",
					corev1.ResourceMemory: "two",
				},
			},
		},
		"multiple flavors, node affinity fits any flavor": {
			wlPods: []kueue.PodSet{
				{
					Count: 1,
					Name:  "main",
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Resources: corev1.ResourceRequirements{
									Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")},
								},
							},
						},
						Affinity: &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
								NodeSelectorTerms: []corev1.NodeSelectorTerm{
									{
										MatchExpressions: []corev1.NodeSelectorRequirement{
											{
												Key:      "ignored2",
												Operator: corev1.NodeSelectorOpIn,
												Values:   []string{"bar"},
											},
										},
									},
									{
										MatchExpressions: []corev1.NodeSelectorRequirement{
											{
												// although this terms selects two
												// the first term practically matches
												// any flavor; and since the terms
												// are ORed, any flavor can be selected.
												Key:      "cpuType",
												Operator: corev1.NodeSelectorOpIn,
												Values:   []string{"two"},
											},
										},
									},
								},
							}},
						},
					},
				},
			},
			clusterQueue: cache.ClusterQueue{
				RequestableResources: map[corev1.ResourceName]*cache.Resource{
					corev1.ResourceCPU: {
						Flavors: []cache.FlavorLimits{
							{Name: "one", Min: 4000},
							{Name: "two", Min: 4000},
						},
					},
				},
			},
			wantFits: true,
			wantFlavors: map[string]map[corev1.ResourceName]string{
				"main": {
					corev1.ResourceCPU: "one",
				},
			},
		},
		"multiple flavor, doesn't fit node affinity": {
			wlPods: []kueue.PodSet{
				{
					Count: 1,
					Name:  "main",
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Resources: corev1.ResourceRequirements{
									Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")},
								},
							},
						},
						Affinity: &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
								NodeSelectorTerms: []corev1.NodeSelectorTerm{
									{
										MatchExpressions: []corev1.NodeSelectorRequirement{
											{
												Key:      "type",
												Operator: corev1.NodeSelectorOpIn,
												Values:   []string{"three"},
											},
										},
									},
								},
							}},
						},
					},
				},
			},
			clusterQueue: cache.ClusterQueue{
				RequestableResources: map[corev1.ResourceName]*cache.Resource{
					corev1.ResourceCPU: {
						Flavors: []cache.FlavorLimits{
							{Name: "one", Min: 4000},
							{Name: "two", Min: 4000},
						},
					},
				},
				LabelKeys: map[corev1.ResourceName]sets.String{corev1.ResourceCPU: sets.NewString("cpuType")},
			},
			wantFits: false,
			wantMsg:  "flavor one doesn't match with node affinity",
		},
		"multiple specs, fit different flavors": {
			wlPods: []kueue.PodSet{
				{
					Count: 1,
					Name:  "driver",
					Spec: utiltesting.PodSpecForRequest(map[corev1.ResourceName]string{
						corev1.ResourceCPU: "5",
					}),
				},
				{
					Count: 1,
					Name:  "worker",
					Spec: utiltesting.PodSpecForRequest(map[corev1.ResourceName]string{
						corev1.ResourceCPU: "3",
					}),
				},
			},
			clusterQueue: cache.ClusterQueue{
				RequestableResources: map[corev1.ResourceName]*cache.Resource{
					corev1.ResourceCPU: {
						Flavors: []cache.FlavorLimits{
							{Name: "one", Min: 4000},
							{Name: "two", Min: 10_000},
						},
					},
				},
			},
			wantFits: true,
			wantFlavors: map[string]map[corev1.ResourceName]string{
				"driver": {
					corev1.ResourceCPU: "two",
				},
				"worker": {
					corev1.ResourceCPU: "one",
				},
			},
		},
		"multiple specs, fits borrowing": {
			wlPods: []kueue.PodSet{
				{
					Count: 1,
					Name:  "driver",
					Spec: utiltesting.PodSpecForRequest(map[corev1.ResourceName]string{
						corev1.ResourceCPU:    "4",
						corev1.ResourceMemory: "1Gi",
					}),
				},
				{
					Count: 1,
					Name:  "worker",
					Spec: utiltesting.PodSpecForRequest(map[corev1.ResourceName]string{
						corev1.ResourceCPU:    "6",
						corev1.ResourceMemory: "4Gi",
					}),
				},
			},
			clusterQueue: cache.ClusterQueue{
				RequestableResources: map[corev1.ResourceName]*cache.Resource{
					corev1.ResourceCPU: {
						Flavors: []cache.FlavorLimits{
							{
								Name: "default",
								Min:  2000,
								Max:  pointer.Int64(100_000),
							},
						},
					},
					corev1.ResourceMemory: {
						Flavors: []cache.FlavorLimits{
							{
								Name: "default",
								Min:  2 * utiltesting.Gi,
								// No max.
							},
						},
					},
				},
				Cohort: &cache.Cohort{
					RequestableResources: cache.ResourceQuantities{
						corev1.ResourceCPU: {
							"default": 200_000,
						},
						corev1.ResourceMemory: {
							"default": 200 * utiltesting.Gi,
						},
					},
				},
			},
			wantFits: true,
			wantFlavors: map[string]map[corev1.ResourceName]string{
				"driver": {
					corev1.ResourceCPU:    "default",
					corev1.ResourceMemory: "default",
				},
				"worker": {
					corev1.ResourceCPU:    "default",
					corev1.ResourceMemory: "default",
				},
			},
			wantBorrows: cache.ResourceQuantities{
				corev1.ResourceCPU: {
					"default": 8_000,
				},
				corev1.ResourceMemory: {
					"default": 3 * utiltesting.Gi,
				},
			},
		},
		"not enough space to borrow": {
			wlPods: []kueue.PodSet{
				{
					Count: 1,
					Name:  "main",
					Spec: utiltesting.PodSpecForRequest(map[corev1.ResourceName]string{
						corev1.ResourceCPU: "2",
					}),
				},
			},
			clusterQueue: cache.ClusterQueue{
				RequestableResources: map[corev1.ResourceName]*cache.Resource{
					corev1.ResourceCPU: {
						Flavors: []cache.FlavorLimits{
							{
								Name: "one",
								Min:  1000,
								// No max.
							},
						},
					},
				},
				Cohort: &cache.Cohort{
					RequestableResources: cache.ResourceQuantities{
						corev1.ResourceCPU: {"one": 10_000},
					},
					UsedResources: cache.ResourceQuantities{
						corev1.ResourceCPU: {"one": 9_000},
					},
				},
			},
			wantMsg: "insufficient quota for cpu flavor one, 1 more needed after borrowing",
		},
		"past max": {
			wlPods: []kueue.PodSet{
				{
					Count: 1,
					Name:  "main",
					Spec: utiltesting.PodSpecForRequest(map[corev1.ResourceName]string{
						corev1.ResourceCPU: "2",
					}),
				},
			},
			clusterQueue: cache.ClusterQueue{
				RequestableResources: map[corev1.ResourceName]*cache.Resource{
					corev1.ResourceCPU: {
						Flavors: []cache.FlavorLimits{
							{
								Name: "one",
								Min:  1000,
								Max:  pointer.Int64(10_000),
							},
						},
					},
				},
				UsedResources: cache.ResourceQuantities{
					corev1.ResourceCPU: {"one": 9_000},
				},
				Cohort: &cache.Cohort{
					RequestableResources: cache.ResourceQuantities{
						corev1.ResourceCPU: {"one": 100_000},
					},
					UsedResources: cache.ResourceQuantities{
						corev1.ResourceCPU: {"one": 9_000},
					},
				},
			},
			wantMsg: "borrowing limit for cpu flavor one exceeded",
		},
		"resource not listed in clusterQueue": {
			wlPods: []kueue.PodSet{
				{
					Count: 1,
					Name:  "main",
					Spec: utiltesting.PodSpecForRequest(map[corev1.ResourceName]string{
						"example.com/gpu": "1",
					}),
				},
			},
			clusterQueue: cache.ClusterQueue{
				RequestableResources: map[corev1.ResourceName]*cache.Resource{
					corev1.ResourceCPU: {
						Flavors: []cache.FlavorLimits{
							{Name: "one", Min: 4000},
						},
					},
				},
			},
			wantFits: false,
			wantMsg:  "resource example.com/gpu unavailable in ClusterQueue",
		},
		"resource not found": {
			wlPods: []kueue.PodSet{
				{
					Count: 1,
					Name:  "main",
					Spec: utiltesting.PodSpecForRequest(map[corev1.ResourceName]string{
						"unknown_resource": "1",
					}),
				},
			},
			clusterQueue: cache.ClusterQueue{
				RequestableResources: map[corev1.ResourceName]*cache.Resource{
					corev1.ResourceCPU: {Flavors: []cache.FlavorLimits{{Name: "one", Min: 1000}}},
				},
			},
			wantMsg: "resource unknown_resource unavailable in ClusterQueue",
		},
		"flavor not found": {
			wlPods: []kueue.PodSet{
				{
					Count: 1,
					Name:  "main",
					Spec: utiltesting.PodSpecForRequest(map[corev1.ResourceName]string{
						corev1.ResourceCPU: "1",
					}),
				},
			},
			clusterQueue: cache.ClusterQueue{
				RequestableResources: map[corev1.ResourceName]*cache.Resource{
					corev1.ResourceCPU: {Flavors: []cache.FlavorLimits{{Name: "nonexistent-flavor", Min: 1000}}},
				},
			},
			wantMsg: "flavor nonexistent-flavor not found",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			log := logrtesting.NewTestLoggerWithOptions(t, logrtesting.Options{
				Verbosity: 2,
			})
			tc.clusterQueue.UpdateCodependentResources()
			e := entry{
				Info: *workload.NewInfo(&kueue.Workload{
					Spec: kueue.WorkloadSpec{
						PodSets: tc.wlPods,
					},
				}),
			}
			tc.clusterQueue.UpdateWithFlavors(resourceFlavors)
			status := e.assignFlavors(log, resourceFlavors, &tc.clusterQueue)
			if status.IsSuccess() != tc.wantFits {
				t.Errorf("e.assignFlavors(_)=%t, want %t", status.IsSuccess(), tc.wantFits)
			}
			if !tc.wantFits {
				if len(tc.wantMsg) == 0 || !strings.Contains(status.Message(), tc.wantMsg) {
					t.Errorf("got msg:\n%s\nwant msg containing:\n%s", status.Message(), tc.wantMsg)
				}
			}
			var flavors map[string]map[corev1.ResourceName]string
			if status.IsSuccess() {
				flavors = make(map[string]map[corev1.ResourceName]string)
				for _, podSet := range e.TotalRequests {
					flavors[podSet.Name] = podSet.Flavors
				}
			}
			if diff := cmp.Diff(tc.wantFlavors, flavors); diff != "" {
				t.Errorf("Assigned unexpected flavors (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantBorrows, e.borrows); diff != "" {
				t.Errorf("Calculated unexpected borrowing (-want,+got):\n%s", diff)
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
			borrows: cache.ResourceQuantities{
				corev1.ResourceCPU: {},
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
			borrows: cache.ResourceQuantities{
				corev1.ResourceCPU: {},
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
		name          string
		e             entry
		wantWorkloads map[string]sets.String
		wantStatus    kueue.WorkloadStatus
	}{
		{
			name: "workload didn't fit",
			e: entry{
				status:          "",
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
		},
		{
			name: "assumed",
			e: entry{
				status:          assumed,
				inadmissibleMsg: "",
			},
			wantWorkloads: map[string]sets.String{
				"cq": sets.NewString(w1.Name),
			},
		},
		{
			name: "nominated",
			e: entry{
				status:          nominated,
				inadmissibleMsg: "failed to admit workload",
			},
			wantWorkloads: map[string]sets.String{
				"cq": sets.NewString(w1.Name),
			},
		},
		{
			name: "skipped",
			e: entry{
				status:          skipped,
				inadmissibleMsg: "cohort used in this cycle",
			},
			wantWorkloads: map[string]sets.String{
				"cq": sets.NewString(w1.Name),
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			log := logrtesting.NewTestLoggerWithOptions(t, logrtesting.Options{
				Verbosity: 2,
			})
			ctx := ctrl.LoggerInto(context.Background(), log)
			scheme := runtime.NewScheme()
			if err := kueue.AddToScheme(scheme); err != nil {
				t.Fatalf("Failed adding kueue scheme: %v", err)
			}
			if err := corev1.AddToScheme(scheme); err != nil {
				t.Fatalf("Failed adding kueue scheme: %v", err)
			}

			clientBuilder := fake.NewClientBuilder().WithScheme(scheme).WithObjects(w1, q1, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ns1"}})
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
