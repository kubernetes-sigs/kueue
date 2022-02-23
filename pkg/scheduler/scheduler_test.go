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
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kueue "sigs.k8s.io/kueue/api/v1alpha1"
	"sigs.k8s.io/kueue/pkg/capacity"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/queue"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/pkg/workload"
)

const (
	watchTimeout    = 2 * time.Second
	queueingTimeout = time.Second
)

var (
	filterAssignFields = cmp.Options{
		cmpopts.IgnoreFields(kueue.QueuedWorkloadSpec{}, "QueueName"),
		cmpopts.IgnoreFields(kueue.PodSet{}, "Count", "Spec"),
	}
)

func TestSchedule(t *testing.T) {
	capacities := []kueue.Capacity{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "sales"},
			Spec: kueue.CapacitySpec{
				RequestableResources: []kueue.Resource{
					{
						Name: corev1.ResourceCPU,
						Flavors: []kueue.ResourceFlavor{
							{
								Name: "default",
								Quota: kueue.Quota{
									Guaranteed: resource.MustParse("50"),
									Ceiling:    resource.MustParse("50"),
								},
							},
						},
					},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "eng-alpha"},
			Spec: kueue.CapacitySpec{
				Cohort: "eng",
				RequestableResources: []kueue.Resource{
					{
						Name: corev1.ResourceCPU,
						Flavors: []kueue.ResourceFlavor{
							{
								Name: "on-demand",
								Quota: kueue.Quota{
									Guaranteed: resource.MustParse("50"),
									Ceiling:    resource.MustParse("100"),
								},
							},
							{
								Name: "spot",
								Quota: kueue.Quota{
									Guaranteed: resource.MustParse("100"),
									Ceiling:    resource.MustParse("100"),
								},
							},
						},
					},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "eng-beta"},
			Spec: kueue.CapacitySpec{
				Cohort: "eng",
				RequestableResources: []kueue.Resource{
					{
						Name: corev1.ResourceCPU,
						Flavors: []kueue.ResourceFlavor{
							{
								Name: "on-demand",
								Quota: kueue.Quota{
									Guaranteed: resource.MustParse("50"),
									Ceiling:    resource.MustParse("60"),
								},
							},
							{
								Name: "spot",
								Quota: kueue.Quota{
									Guaranteed: resource.MustParse("0"),
									Ceiling:    resource.MustParse("100"),
								},
							},
						},
					},
					{
						Name: "example.com/gpu",
						Flavors: []kueue.ResourceFlavor{
							{
								Name: "model-a",
								Quota: kueue.Quota{
									Guaranteed: resource.MustParse("20"),
									Ceiling:    resource.MustParse("20"),
								},
							},
						},
					},
				},
			},
		},
	}
	queues := []kueue.Queue{
		{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "sales",
				Name:      "main",
			},
			Spec: kueue.QueueSpec{
				Capacity: "sales",
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "eng-alpha",
				Name:      "main",
			},
			Spec: kueue.QueueSpec{
				Capacity: "eng-alpha",
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "eng-beta",
				Name:      "main",
			},
			Spec: kueue.QueueSpec{
				Capacity: "eng-beta",
			},
		},
	}
	cases := map[string]struct {
		workloads []kueue.QueuedWorkload
		// wantAssignment is a summary of all the assignments in the cache after this cycle.
		wantAssigment map[string]kueue.QueuedWorkloadSpec
		// wantScheduled is the subset of assigments that got scheduled in this cycle.
		wantScheduled []string
		// wantLeft is the workload keys that are left in the queues after this cycle.
		wantLeft map[string]sets.String
	}{
		"workload fits in single capacity": {
			workloads: []kueue.QueuedWorkload{
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "sales",
						Name:      "foo",
					},
					Spec: kueue.QueuedWorkloadSpec{
						QueueName: "main",
						Pods: []kueue.PodSet{
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
			wantAssigment: map[string]kueue.QueuedWorkloadSpec{
				"sales/foo": {
					AssignedCapacity: "sales",
					Pods: []kueue.PodSet{
						{
							Name: "one",
							AssignedFlavors: map[corev1.ResourceName]string{
								corev1.ResourceCPU: "default",
							},
						},
					},
				},
			},
			wantScheduled: []string{"sales/foo"},
		},
		"single capacity full": {
			workloads: []kueue.QueuedWorkload{
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "sales",
						Name:      "new",
					},
					Spec: kueue.QueuedWorkloadSpec{
						QueueName: "main",
						Pods: []kueue.PodSet{
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
					Spec: kueue.QueuedWorkloadSpec{
						AssignedCapacity: "sales",
						Pods: []kueue.PodSet{
							{
								Name:  "one",
								Count: 40,
								Spec: utiltesting.PodSpecForRequest(map[corev1.ResourceName]string{
									corev1.ResourceCPU: "1",
								}),
								AssignedFlavors: map[corev1.ResourceName]string{
									corev1.ResourceCPU: "default",
								},
							},
						},
					},
				},
			},
			wantAssigment: map[string]kueue.QueuedWorkloadSpec{
				"sales/assigned": {
					AssignedCapacity: "sales",
					Pods: []kueue.PodSet{
						{
							Name: "one",
							AssignedFlavors: map[corev1.ResourceName]string{
								corev1.ResourceCPU: "default",
							},
						},
					},
				},
			},
			wantLeft: map[string]sets.String{
				"sales/main": sets.NewString("new"),
			},
		},
		"assign to different cohorts": {
			workloads: []kueue.QueuedWorkload{
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "sales",
						Name:      "new",
					},
					Spec: kueue.QueuedWorkloadSpec{
						QueueName: "main",
						Pods: []kueue.PodSet{
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
					Spec: kueue.QueuedWorkloadSpec{
						QueueName: "main",
						Pods: []kueue.PodSet{
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
			wantAssigment: map[string]kueue.QueuedWorkloadSpec{
				"sales/new": {
					AssignedCapacity: "sales",
					Pods: []kueue.PodSet{
						{
							Name: "one",
							AssignedFlavors: map[corev1.ResourceName]string{
								corev1.ResourceCPU: "default",
							},
						},
					},
				},
				"eng-alpha/new": {
					AssignedCapacity: "eng-alpha",
					Pods: []kueue.PodSet{
						{
							Name: "one",
							AssignedFlavors: map[corev1.ResourceName]string{
								corev1.ResourceCPU: "on-demand",
							},
						},
					},
				},
			},
			wantScheduled: []string{"sales/new", "eng-alpha/new"},
		},
		"assign to same cohort no borrowing": {
			workloads: []kueue.QueuedWorkload{
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "eng-alpha",
						Name:      "new",
					},
					Spec: kueue.QueuedWorkloadSpec{
						QueueName: "main",
						Pods: []kueue.PodSet{
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
					Spec: kueue.QueuedWorkloadSpec{
						QueueName: "main",
						Pods: []kueue.PodSet{
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
			wantAssigment: map[string]kueue.QueuedWorkloadSpec{
				"eng-alpha/new": {
					AssignedCapacity: "eng-alpha",
					Pods: []kueue.PodSet{
						{
							Name: "one",
							AssignedFlavors: map[corev1.ResourceName]string{
								corev1.ResourceCPU: "on-demand",
							},
						},
					},
				},
				"eng-beta/new": {
					AssignedCapacity: "eng-beta",
					Pods: []kueue.PodSet{
						{
							Name: "one",
							AssignedFlavors: map[corev1.ResourceName]string{
								corev1.ResourceCPU: "on-demand",
							},
						},
					},
				},
			},
			wantScheduled: []string{"eng-alpha/new", "eng-beta/new"},
		},
		"assign multiple resources and flavors": {
			workloads: []kueue.QueuedWorkload{
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "eng-beta",
						Name:      "new",
					},
					Spec: kueue.QueuedWorkloadSpec{
						QueueName: "main",
						Pods: []kueue.PodSet{
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
			wantAssigment: map[string]kueue.QueuedWorkloadSpec{
				"eng-beta/new": {
					AssignedCapacity: "eng-beta",
					Pods: []kueue.PodSet{
						{
							Name: "one",
							AssignedFlavors: map[corev1.ResourceName]string{
								corev1.ResourceCPU: "on-demand",
								"example.com/gpu":  "model-a",
							},
						},
						{
							Name: "two",
							AssignedFlavors: map[corev1.ResourceName]string{
								corev1.ResourceCPU: "spot",
							},
						},
					},
				},
			},
			wantScheduled: []string{"eng-beta/new"},
		},
		"cannot borrow if cohort was assigned": {
			workloads: []kueue.QueuedWorkload{
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "eng-alpha",
						Name:      "new",
					},
					Spec: kueue.QueuedWorkloadSpec{
						QueueName: "main",
						Pods: []kueue.PodSet{
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
					Spec: kueue.QueuedWorkloadSpec{
						QueueName: "main",
						Pods: []kueue.PodSet{
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
			wantAssigment: map[string]kueue.QueuedWorkloadSpec{
				"eng-alpha/new": {
					AssignedCapacity: "eng-alpha",
					Pods: []kueue.PodSet{
						{
							Name: "one",
							AssignedFlavors: map[corev1.ResourceName]string{
								corev1.ResourceCPU: "on-demand",
							},
						},
					},
				},
			},
			wantScheduled: []string{"eng-alpha/new"},
			wantLeft: map[string]sets.String{
				"eng-beta/main": sets.NewString("new"),
			},
		},
		"cannot borrow resource not listed in capacity": {
			workloads: []kueue.QueuedWorkload{
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "eng-alpha",
						Name:      "new",
					},
					Spec: kueue.QueuedWorkloadSpec{
						QueueName: "main",
						Pods: []kueue.PodSet{
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
				"eng-alpha/main": sets.NewString("new"),
			},
		},
		"not enough resources to borrow, fallback to next flavor": {
			workloads: []kueue.QueuedWorkload{
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "eng-alpha",
						Name:      "new",
					},
					Spec: kueue.QueuedWorkloadSpec{
						QueueName: "main",
						Pods: []kueue.PodSet{
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
					Spec: kueue.QueuedWorkloadSpec{
						AssignedCapacity: "eng-beta",
						Pods: []kueue.PodSet{
							{
								Name:  "one",
								Count: 45,
								Spec: utiltesting.PodSpecForRequest(map[corev1.ResourceName]string{
									corev1.ResourceCPU: "1",
								}),
								AssignedFlavors: map[corev1.ResourceName]string{
									corev1.ResourceCPU: "on-demand",
								},
							},
						},
					},
				},
			},
			wantAssigment: map[string]kueue.QueuedWorkloadSpec{
				"eng-alpha/new": {
					AssignedCapacity: "eng-alpha",
					Pods: []kueue.PodSet{
						{
							Name: "one",
							AssignedFlavors: map[corev1.ResourceName]string{
								corev1.ResourceCPU: "spot",
							},
						},
					},
				},
				"eng-beta/existing": {
					AssignedCapacity: "eng-beta",
					Pods: []kueue.PodSet{
						{
							Name: "one",
							AssignedFlavors: map[corev1.ResourceName]string{
								corev1.ResourceCPU: "on-demand",
							},
						},
					},
				},
			},
			wantScheduled: []string{"eng-alpha/new"},
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
			clientBuilder := fake.NewClientBuilder().WithScheme(scheme).WithLists(
				&kueue.QueuedWorkloadList{Items: tc.workloads})
			cl := clientBuilder.Build()
			broadcaster := record.NewBroadcaster()
			recorder := broadcaster.NewRecorder(scheme,
				corev1.EventSource{Component: constants.ManagerName})
			qManager := queue.NewManager(cl)
			capCache := capacity.NewCache(cl)
			// Workloads are loaded into queues or capacities as we add them.
			for _, q := range queues {
				if err := qManager.AddQueue(ctx, &q); err != nil {
					t.Fatalf("Inserting queue %s/%s in manager: %v", q.Namespace, q.Name, err)
				}
			}
			for _, c := range capacities {
				if err := capCache.AddCapacity(ctx, &c); err != nil {
					t.Fatalf("Inserting capacity %s in cache: %v", c.Name, err)
				}
			}
			workloadWatch, err := cl.Watch(ctx, &kueue.QueuedWorkloadList{})
			if err != nil {
				t.Fatalf("Failed setting up watch: %v", err)
			}
			scheduler := New(qManager, capCache, cl, recorder)

			ctx, cancel := context.WithTimeout(ctx, queueingTimeout)
			go qManager.CleanUpOnContext(ctx)
			defer cancel()
			scheduler.schedule(ctx)

			// Verify assignments in API.
			gotScheduled := make(map[string]kueue.QueuedWorkloadSpec)
			timedOut := false
			for !timedOut && len(gotScheduled) < len(tc.wantScheduled) {
				select {
				case evt := <-workloadWatch.ResultChan():
					w, ok := evt.Object.(*kueue.QueuedWorkload)
					if !ok {
						t.Fatalf("Received update for %T, want QueuedWorkload", evt.Object)
					}
					if w.Spec.AssignedCapacity != "" {
						gotScheduled[workload.Key(w)] = w.Spec
					}
				case <-time.After(watchTimeout):
					t.Errorf("Timed out waiting for QueuedWorkload updates")
					timedOut = true
				}
			}
			wantScheduled := make(map[string]kueue.QueuedWorkloadSpec)
			for _, key := range tc.wantScheduled {
				wantScheduled[key] = tc.wantAssigment[key]
			}
			if diff := cmp.Diff(wantScheduled, gotScheduled, filterAssignFields); diff != "" {
				t.Errorf("Unexpected scheduled workloads (-want,+got):\n%s", diff)
			}

			// Verify assignments in capacity cache.
			gotAssignments := make(map[string]kueue.QueuedWorkloadSpec)
			snapshot := capCache.Snapshot()
			for cName, c := range snapshot.Capacities {
				for name, w := range c.Workloads {
					if string(w.Obj.Spec.AssignedCapacity) != cName {
						t.Errorf("Workload %s is assigned to capacity %s, but it is found as member of capacity %s", name, w.Obj.Spec.AssignedCapacity, cName)
					}
					gotAssignments[name] = w.Obj.Spec
				}
			}
			if len(gotAssignments) == 0 {
				gotAssignments = nil
			}
			if diff := cmp.Diff(tc.wantAssigment, gotAssignments, filterAssignFields...); diff != "" {
				t.Errorf("Unexpected assigned capacities in cache (-want,+got):\n%s", diff)
			}

			qDump := qManager.Dump()
			if diff := cmp.Diff(tc.wantLeft, qDump); diff != "" {
				t.Errorf("Unexpected elements left in the queue (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestEntryAssignFlavors(t *testing.T) {
	cases := map[string]struct {
		wlPods      []kueue.PodSet
		capacity    capacity.Capacity
		wantFits    bool
		wantFlavors map[string]map[corev1.ResourceName]string
		wantBorrows capacity.Resources
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
			capacity: capacity.Capacity{
				RequestableResources: map[corev1.ResourceName][]capacity.FlavorQuota{
					corev1.ResourceCPU:    defaultFlavorNoBorrowing(1000),
					corev1.ResourceMemory: defaultFlavorNoBorrowing(2 * utiltesting.Mi),
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
			capacity: capacity.Capacity{
				RequestableResources: map[corev1.ResourceName][]capacity.FlavorQuota{
					corev1.ResourceCPU: defaultFlavorNoBorrowing(4000),
				},
				UsedResources: capacity.Resources{
					corev1.ResourceCPU: {
						"default": 3_000,
					},
				},
			},
		},
		"multiple flavors, fits": {
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
			capacity: capacity.Capacity{
				RequestableResources: map[corev1.ResourceName][]capacity.FlavorQuota{
					corev1.ResourceCPU: noBorrowing([]capacity.FlavorQuota{
						{Name: "one", Guaranteed: 2000},
						{Name: "two", Guaranteed: 4000},
					}),
					corev1.ResourceMemory: noBorrowing([]capacity.FlavorQuota{
						{Name: "one", Guaranteed: utiltesting.Gi},
						{Name: "two", Guaranteed: 5 * utiltesting.Mi},
					}),
				},
			},
			wantFits: true,
			wantFlavors: map[string]map[corev1.ResourceName]string{
				"main": {
					corev1.ResourceCPU:    "two",
					corev1.ResourceMemory: "one",
				},
			},
		},
		"multiple flavors, doesn't fit": {
			wlPods: []kueue.PodSet{
				{
					Count: 1,
					Name:  "main",
					Spec: utiltesting.PodSpecForRequest(map[corev1.ResourceName]string{
						corev1.ResourceCPU:    "4.1",
						corev1.ResourceMemory: "0.5Gi",
					}),
				},
			},
			capacity: capacity.Capacity{
				RequestableResources: map[corev1.ResourceName][]capacity.FlavorQuota{
					corev1.ResourceCPU: noBorrowing([]capacity.FlavorQuota{
						{Name: "one", Guaranteed: 2000},
						{Name: "two", Guaranteed: 4000},
					}),
					corev1.ResourceMemory: noBorrowing([]capacity.FlavorQuota{
						{Name: "one", Guaranteed: utiltesting.Gi},
						{Name: "two", Guaranteed: 5 * utiltesting.Mi},
					}),
				},
			},
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
			capacity: capacity.Capacity{
				RequestableResources: map[corev1.ResourceName][]capacity.FlavorQuota{
					corev1.ResourceCPU: noBorrowing([]capacity.FlavorQuota{
						{Name: "one", Guaranteed: 4000},
						{Name: "two", Guaranteed: 10_000},
					}),
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
			capacity: capacity.Capacity{
				RequestableResources: map[corev1.ResourceName][]capacity.FlavorQuota{
					corev1.ResourceCPU: {
						{
							Name:       "default",
							Guaranteed: 2000,
							Ceiling:    100_000,
						},
					},
					corev1.ResourceMemory: {
						{
							Name:       "default",
							Guaranteed: 2 * utiltesting.Gi,
							Ceiling:    100 * utiltesting.Gi,
						},
					},
				},
				Cohort: &capacity.Cohort{
					RequestableResources: capacity.Resources{
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
			wantBorrows: capacity.Resources{
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
			capacity: capacity.Capacity{
				RequestableResources: map[corev1.ResourceName][]capacity.FlavorQuota{
					corev1.ResourceCPU: {
						{
							Name:       "one",
							Guaranteed: 1000,
							Ceiling:    10_000,
						},
					},
				},
				Cohort: &capacity.Cohort{
					RequestableResources: capacity.Resources{
						corev1.ResourceCPU: {"one": 10_000},
					},
					UsedResources: capacity.Resources{
						corev1.ResourceCPU: {"one": 9_000},
					},
				},
			},
		},
		"past ceiling": {
			wlPods: []kueue.PodSet{
				{
					Count: 1,
					Name:  "main",
					Spec: utiltesting.PodSpecForRequest(map[corev1.ResourceName]string{
						corev1.ResourceCPU: "2",
					}),
				},
			},
			capacity: capacity.Capacity{
				RequestableResources: map[corev1.ResourceName][]capacity.FlavorQuota{
					corev1.ResourceCPU: {
						{
							Name:       "one",
							Guaranteed: 1000,
							Ceiling:    10_000,
						},
					},
				},
				UsedResources: capacity.Resources{
					corev1.ResourceCPU: {"one": 9_000},
				},
				Cohort: &capacity.Cohort{
					RequestableResources: capacity.Resources{
						corev1.ResourceCPU: {"one": 100_000},
					},
					UsedResources: capacity.Resources{
						corev1.ResourceCPU: {"one": 9_000},
					},
				},
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := entry{
				Info: *workload.NewInfo(&kueue.QueuedWorkload{
					Spec: kueue.QueuedWorkloadSpec{
						Pods: tc.wlPods,
					},
				}),
			}
			fits := e.assignFlavors(&tc.capacity)
			if fits != tc.wantFits {
				t.Errorf("e.assignFlavors(_)=%t, want %t", fits, tc.wantFits)
			}
			var flavors map[string]map[corev1.ResourceName]string
			if fits {
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
				Obj: &kueue.QueuedWorkload{ObjectMeta: metav1.ObjectMeta{
					Name:              "alpha",
					CreationTimestamp: metav1.NewTime(now),
				}},
			},
			borrows: capacity.Resources{
				corev1.ResourceCPU: {},
			},
		},
		{
			Info: workload.Info{
				Obj: &kueue.QueuedWorkload{ObjectMeta: metav1.ObjectMeta{
					Name:              "beta",
					CreationTimestamp: metav1.NewTime(now.Add(time.Second)),
				}},
			},
		},
		{
			Info: workload.Info{
				Obj: &kueue.QueuedWorkload{ObjectMeta: metav1.ObjectMeta{
					Name:              "gamma",
					CreationTimestamp: metav1.NewTime(now.Add(2 * time.Second)),
				}},
			},
		},
		{
			Info: workload.Info{
				Obj: &kueue.QueuedWorkload{ObjectMeta: metav1.ObjectMeta{
					Name:              "delta",
					CreationTimestamp: metav1.NewTime(now.Add(time.Second)),
				}},
			},
			borrows: capacity.Resources{
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

func defaultFlavorNoBorrowing(guaranteed int64) []capacity.FlavorQuota {
	return noBorrowing([]capacity.FlavorQuota{{Name: "default", Guaranteed: guaranteed}})
}

func noBorrowing(flavors []capacity.FlavorQuota) []capacity.FlavorQuota {
	for i := range flavors {
		flavors[i].Ceiling = flavors[i].Guaranteed
	}
	return flavors
}
