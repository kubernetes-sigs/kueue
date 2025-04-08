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

package workload

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	testingclock "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	config "sigs.k8s.io/kueue/apis/config/v1beta1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

func TestNewInfo(t *testing.T) {
	cases := map[string]struct {
		workload                            kueue.Workload
		infoOptions                         []InfoOption
		wantInfo                            Info
		configurableResourceTransformations bool
	}{
		"pending": {
			workload: *utiltesting.MakeWorkload("", "").
				Request(corev1.ResourceCPU, "10m").
				Request(corev1.ResourceMemory, "512Ki").
				Obj(),
			wantInfo: Info{
				TotalRequests: []PodSetResources{
					{
						Name: kueue.DefaultPodSetName,
						Requests: resources.Requests{
							corev1.ResourceCPU:    10,
							corev1.ResourceMemory: 512 * 1024,
						},
						Count: 1,
					},
				},
			},
		},
		"pending with reclaim": {
			workload: *utiltesting.MakeWorkload("", "").
				PodSets(
					*utiltesting.MakePodSet(kueue.DefaultPodSetName, 5).
						Request(corev1.ResourceCPU, "10m").
						Request(corev1.ResourceMemory, "512Ki").
						Obj(),
				).
				ReclaimablePods(
					kueue.ReclaimablePod{
						Name:  kueue.DefaultPodSetName,
						Count: 2,
					},
				).
				Obj(),
			wantInfo: Info{
				TotalRequests: []PodSetResources{
					{
						Name: kueue.DefaultPodSetName,
						Requests: resources.Requests{
							corev1.ResourceCPU:    3 * 10,
							corev1.ResourceMemory: 3 * 512 * 1024,
						},
						Count: 3,
					},
				},
			},
		},
		"admitted": {
			workload: *utiltesting.MakeWorkload("", "").
				PodSets(
					*utiltesting.MakePodSet("driver", 1).
						Request(corev1.ResourceCPU, "10m").
						Request(corev1.ResourceMemory, "512Ki").
						Obj(),
					*utiltesting.MakePodSet("workers", 3).
						Request(corev1.ResourceCPU, "5m").
						Request(corev1.ResourceMemory, "1Mi").
						Request("ex.com/gpu", "1").
						Obj(),
				).
				ReserveQuota(utiltesting.MakeAdmission("foo").
					PodSets(
						kueue.PodSetAssignment{
							Name: "driver",
							Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
								corev1.ResourceCPU: "on-demand",
							},
							ResourceUsage: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("10m"),
								corev1.ResourceMemory: resource.MustParse("512Ki"),
							},
							Count: ptr.To[int32](1),
						},
						kueue.PodSetAssignment{
							Name: "workers",
							ResourceUsage: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("15m"),
								corev1.ResourceMemory: resource.MustParse("3Mi"),
								"ex.com/gpu":          resource.MustParse("3"),
							},
							Count: ptr.To[int32](3),
						},
					).
					Obj()).
				Obj(),
			wantInfo: Info{
				ClusterQueue: "foo",
				TotalRequests: []PodSetResources{
					{
						Name: "driver",
						Requests: resources.Requests{
							corev1.ResourceCPU:    10,
							corev1.ResourceMemory: 512 * 1024,
						},
						Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
							corev1.ResourceCPU: "on-demand",
						},
						Count: 1,
					},
					{
						Name: "workers",
						Requests: resources.Requests{
							corev1.ResourceCPU:    15,
							corev1.ResourceMemory: 3 * 1024 * 1024,
							"ex.com/gpu":          3,
						},
						Count: 3,
					},
				},
			},
		},
		"admitted with reclaim": {
			workload: *utiltesting.MakeWorkload("", "").
				PodSets(
					*utiltesting.MakePodSet(kueue.DefaultPodSetName, 5).
						Request(corev1.ResourceCPU, "10m").
						Request(corev1.ResourceMemory, "10Ki").
						Obj(),
				).
				ReserveQuota(
					utiltesting.MakeAdmission("").
						Assignment(corev1.ResourceCPU, "f1", "30m").
						Assignment(corev1.ResourceMemory, "f1", "30Ki").
						AssignmentPodCount(3).
						Obj(),
				).
				ReclaimablePods(
					kueue.ReclaimablePod{
						Name:  kueue.DefaultPodSetName,
						Count: 2,
					},
				).
				Obj(),
			wantInfo: Info{
				TotalRequests: []PodSetResources{
					{
						Name: kueue.DefaultPodSetName,
						Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
							corev1.ResourceCPU:    "f1",
							corev1.ResourceMemory: "f1",
						},
						Requests: resources.Requests{
							corev1.ResourceCPU:    3 * 10,
							corev1.ResourceMemory: 3 * 10 * 1024,
						},
						Count: 3,
					},
				},
			},
		},
		"admitted with reclaim and increased reclaim": {
			workload: *utiltesting.MakeWorkload("", "").
				PodSets(
					*utiltesting.MakePodSet(kueue.DefaultPodSetName, 5).
						Request(corev1.ResourceCPU, "10m").
						Request(corev1.ResourceMemory, "10Ki").
						Obj(),
				).
				ReserveQuota(
					utiltesting.MakeAdmission("").
						Assignment(corev1.ResourceCPU, "f1", "30m").
						Assignment(corev1.ResourceMemory, "f1", "30Ki").
						AssignmentPodCount(3).
						Obj(),
				).
				ReclaimablePods(
					kueue.ReclaimablePod{
						Name:  kueue.DefaultPodSetName,
						Count: 3,
					},
				).
				Obj(),
			wantInfo: Info{
				TotalRequests: []PodSetResources{
					{
						Name: kueue.DefaultPodSetName,
						Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
							corev1.ResourceCPU:    "f1",
							corev1.ResourceMemory: "f1",
						},
						Requests: resources.Requests{
							corev1.ResourceCPU:    2 * 10,
							corev1.ResourceMemory: 2 * 10 * 1024,
						},
						Count: 2,
					},
				},
			},
		},
		"partially admitted": {
			workload: *utiltesting.MakeWorkload("", "").
				PodSets(
					*utiltesting.MakePodSet(kueue.DefaultPodSetName, 5).
						Request(corev1.ResourceCPU, "10m").
						Request(corev1.ResourceMemory, "10Ki").
						Obj(),
				).
				ReserveQuota(
					utiltesting.MakeAdmission("").
						Assignment(corev1.ResourceCPU, "f1", "30m").
						Assignment(corev1.ResourceMemory, "f1", "30Ki").
						AssignmentPodCount(3).
						Obj(),
				).
				Obj(),
			wantInfo: Info{
				TotalRequests: []PodSetResources{
					{
						Name: kueue.DefaultPodSetName,
						Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
							corev1.ResourceCPU:    "f1",
							corev1.ResourceMemory: "f1",
						},
						Requests: resources.Requests{
							corev1.ResourceCPU:    3 * 10,
							corev1.ResourceMemory: 3 * 10 * 1024,
						},
						Count: 3,
					},
				},
			},
		},
		"filterResources": {
			workload: *utiltesting.MakeWorkload("", "").
				Request(corev1.ResourceCPU, "10m").
				Request(corev1.ResourceMemory, "512Ki").
				Request("networking.example.com/vpc1", "1").
				Obj(),
			infoOptions: []InfoOption{WithExcludedResourcePrefixes([]string{"dummyPrefix", "networking.example.com/"})},
			wantInfo: Info{
				TotalRequests: []PodSetResources{
					{
						Name: kueue.DefaultPodSetName,
						Requests: resources.Requests{
							corev1.ResourceCPU:    10,
							corev1.ResourceMemory: 512 * 1024,
						},
						Count: 1,
					},
				},
			},
		},
		"transformResources": {
			workload: *utiltesting.MakeWorkload("transform", "").
				PodSets(
					*utiltesting.MakePodSet("a", 1).
						Request("nvidia.com/mig-1g.5gb", "2").
						Request("nvidia.com/mig-2g.10gb", "1").
						Request(corev1.ResourceCPU, "1").
						Obj(),
					*utiltesting.MakePodSet("b", 2).
						Request("nvidia.com/gpu", "1").
						Request(corev1.ResourceCPU, "2").
						Obj(),
				).
				Obj(),
			infoOptions: []InfoOption{WithResourceTransformations([]config.ResourceTransformation{
				{
					Input:    corev1.ResourceName("nvidia.com/mig-1g.5gb"),
					Strategy: ptr.To(config.Replace),
					Outputs: corev1.ResourceList{
						corev1.ResourceName("example.com/accelerator-memory"): resource.MustParse("5Ki"),
						corev1.ResourceName("example.com/credits"):            resource.MustParse("10"),
					},
				},
				{
					Input:    corev1.ResourceName("nvidia.com/mig-2g.10gb"),
					Strategy: ptr.To(config.Replace),
					Outputs: corev1.ResourceList{
						corev1.ResourceName("example.com/accelerator-memory"): resource.MustParse("10Ki"),
						corev1.ResourceName("example.com/credits"):            resource.MustParse("15"),
					},
				},
				{
					Input:    corev1.ResourceName("nvidia.com/gpu"),
					Strategy: ptr.To(config.Retain),
					Outputs: corev1.ResourceList{
						corev1.ResourceName("example.com/accelerator-memory"): resource.MustParse("40Ki"),
						corev1.ResourceName("example.com/credits"):            resource.MustParse("100"),
					},
				},
			})},
			wantInfo: Info{
				TotalRequests: []PodSetResources{
					{
						Name: "a",
						Requests: resources.Requests{
							corev1.ResourceCPU: 1000,
							corev1.ResourceName("example.com/accelerator-memory"): 20 * 1024,
							corev1.ResourceName("example.com/credits"):            35,
						},
						Count: 1,
					},
					{
						Name: "b",
						Requests: resources.Requests{
							corev1.ResourceCPU: 4 * 1000,
							corev1.ResourceName("example.com/accelerator-memory"): 80 * 1024,
							corev1.ResourceName("example.com/credits"):            200,
							corev1.ResourceName("nvidia.com/gpu"):                 2,
						},
						Count: 2,
					},
				},
			},
			configurableResourceTransformations: true,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.ConfigurableResourceTransformations, tc.configurableResourceTransformations)
			info := NewInfo(&tc.workload, tc.infoOptions...)
			if diff := cmp.Diff(info, &tc.wantInfo, cmpopts.IgnoreFields(Info{}, "Obj")); diff != "" {
				t.Errorf("NewInfo(_) = (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestUpdateWorkloadStatus(t *testing.T) {
	now := time.Now()
	fakeClock := testingclock.NewFakeClock(now)
	cases := map[string]struct {
		oldStatus  kueue.WorkloadStatus
		condType   string
		condStatus metav1.ConditionStatus
		reason     string
		message    string
		wantStatus kueue.WorkloadStatus
	}{
		"initial empty": {
			condType:   kueue.WorkloadQuotaReserved,
			condStatus: metav1.ConditionFalse,
			reason:     "Pending",
			message:    "didn't fit",
			wantStatus: kueue.WorkloadStatus{
				Conditions: []metav1.Condition{
					{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "didn't fit",
						ObservedGeneration: 1,
					},
				},
			},
		},
		"same condition type": {
			oldStatus: kueue.WorkloadStatus{
				Conditions: []metav1.Condition{
					{
						Type:    kueue.WorkloadQuotaReserved,
						Status:  metav1.ConditionFalse,
						Reason:  "Pending",
						Message: "didn't fit",
					},
				},
			},
			condType:   kueue.WorkloadQuotaReserved,
			condStatus: metav1.ConditionTrue,
			reason:     "Admitted",
			wantStatus: kueue.WorkloadStatus{
				Conditions: []metav1.Condition{
					{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "Admitted",
						ObservedGeneration: 1,
					},
				},
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			workload := utiltesting.MakeWorkload("foo", "bar").Generation(1).Obj()
			workload.Status = tc.oldStatus
			cl := utiltesting.NewFakeClientSSAAsSM(workload)
			ctx := t.Context()
			err := UpdateStatus(ctx, cl, workload, tc.condType, tc.condStatus, tc.reason, tc.message, "manager-prefix", fakeClock)
			if err != nil {
				t.Fatalf("Failed updating status: %v", err)
			}
			var updatedWl kueue.Workload
			if err := cl.Get(ctx, client.ObjectKeyFromObject(workload), &updatedWl); err != nil {
				t.Fatalf("Failed obtaining updated object: %v", err)
			}
			if diff := cmp.Diff(
				tc.wantStatus,
				updatedWl.Status,
				cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime"),
			); diff != "" {
				t.Errorf("Unexpected status after updating (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestGetQueueOrderTimestamp(t *testing.T) {
	var (
		evictionOrdering = Ordering{PodsReadyRequeuingTimestamp: config.EvictionTimestamp}
		creationOrdering = Ordering{PodsReadyRequeuingTimestamp: config.CreationTimestamp}
	)

	creationTime := metav1.Now()
	conditionTime := metav1.NewTime(creationTime.Add(time.Hour))

	cases := map[string]struct {
		wl   *kueue.Workload
		want map[Ordering]metav1.Time
	}{
		"no condition": {
			wl: utiltesting.MakeWorkload("name", "ns").
				Creation(creationTime.Time).
				Obj(),
			want: map[Ordering]metav1.Time{
				evictionOrdering: creationTime,
				creationOrdering: creationTime,
			},
		},
		"evicted by preemption": {
			wl: utiltesting.MakeWorkload("name", "ns").
				Creation(creationTime.Time).
				Condition(metav1.Condition{
					Type:               kueue.WorkloadEvicted,
					Status:             metav1.ConditionTrue,
					LastTransitionTime: conditionTime,
					Reason:             kueue.WorkloadEvictedByPreemption,
				}).
				Obj(),
			want: map[Ordering]metav1.Time{
				evictionOrdering: creationTime,
				creationOrdering: creationTime,
			},
		},
		"evicted by PodsReady timeout": {
			wl: utiltesting.MakeWorkload("name", "ns").
				Creation(creationTime.Time).
				Condition(metav1.Condition{
					Type:               kueue.WorkloadEvicted,
					Status:             metav1.ConditionTrue,
					LastTransitionTime: conditionTime,
					Reason:             kueue.WorkloadEvictedByPodsReadyTimeout,
				}).
				Obj(),
			want: map[Ordering]metav1.Time{
				evictionOrdering: conditionTime,
				creationOrdering: creationTime,
			},
		},
		"after eviction": {
			wl: utiltesting.MakeWorkload("name", "ns").
				Creation(creationTime.Time).
				Condition(metav1.Condition{
					Type:               kueue.WorkloadEvicted,
					Status:             metav1.ConditionFalse,
					LastTransitionTime: conditionTime,
					Reason:             kueue.WorkloadEvictedByPodsReadyTimeout,
				}).
				Obj(),
			want: map[Ordering]metav1.Time{
				evictionOrdering: creationTime,
				creationOrdering: creationTime,
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			for ordering, want := range tc.want {
				gotTime := ordering.GetQueueOrderTimestamp(tc.wl)
				if diff := cmp.Diff(*gotTime, want); diff != "" {
					t.Errorf("Unexpected time (-want,+got):\n%s", diff)
				}
			}
		})
	}
}

func TestReclaimablePodsAreEqual(t *testing.T) {
	cases := map[string]struct {
		a, b       []kueue.ReclaimablePod
		wantResult bool
	}{
		"both empty": {
			b:          []kueue.ReclaimablePod{},
			wantResult: true,
		},
		"one empty": {
			b:          []kueue.ReclaimablePod{{Name: "rp1", Count: 1}},
			wantResult: false,
		},
		"one value mismatch": {
			a:          []kueue.ReclaimablePod{{Name: "rp1", Count: 1}, {Name: "rp2", Count: 2}},
			b:          []kueue.ReclaimablePod{{Name: "rp2", Count: 1}, {Name: "rp1", Count: 1}},
			wantResult: false,
		},
		"one name mismatch": {
			a:          []kueue.ReclaimablePod{{Name: "rp1", Count: 1}, {Name: "rp2", Count: 2}},
			b:          []kueue.ReclaimablePod{{Name: "rp3", Count: 3}, {Name: "rp1", Count: 1}},
			wantResult: false,
		},
		"length mismatch": {
			a:          []kueue.ReclaimablePod{{Name: "rp1", Count: 1}, {Name: "rp2", Count: 2}},
			b:          []kueue.ReclaimablePod{{Name: "rp1", Count: 1}},
			wantResult: false,
		},
		"equal": {
			a:          []kueue.ReclaimablePod{{Name: "rp1", Count: 1}, {Name: "rp2", Count: 2}},
			b:          []kueue.ReclaimablePod{{Name: "rp2", Count: 2}, {Name: "rp1", Count: 1}},
			wantResult: true,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			result := ReclaimablePodsAreEqual(tc.a, tc.b)
			if diff := cmp.Diff(result, tc.wantResult); diff != "" {
				t.Errorf("Unexpected time (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestAssignmentClusterQueueState(t *testing.T) {
	cases := map[string]struct {
		state              *AssignmentClusterQueueState
		wantPendingFlavors bool
	}{
		"no info": {
			wantPendingFlavors: false,
		},
		"all done": {
			state: &AssignmentClusterQueueState{
				LastTriedFlavorIdx: []map[corev1.ResourceName]int{
					{
						corev1.ResourceCPU:    -1,
						corev1.ResourceMemory: -1,
					},
					{
						corev1.ResourceMemory: -1,
					},
				},
			},
			wantPendingFlavors: false,
		},
		"some pending": {
			state: &AssignmentClusterQueueState{
				LastTriedFlavorIdx: []map[corev1.ResourceName]int{
					{
						corev1.ResourceCPU:    0,
						corev1.ResourceMemory: -1,
					},
					{
						corev1.ResourceMemory: 1,
					},
				},
			},
			wantPendingFlavors: true,
		},
		"all pending": {
			state: &AssignmentClusterQueueState{
				LastTriedFlavorIdx: []map[corev1.ResourceName]int{
					{
						corev1.ResourceCPU:    1,
						corev1.ResourceMemory: 0,
					},
					{
						corev1.ResourceMemory: 1,
					},
				},
			},
			wantPendingFlavors: true,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := tc.state.PendingFlavors()
			if got != tc.wantPendingFlavors {
				t.Errorf("state.PendingFlavors() = %t, want %t", got, tc.wantPendingFlavors)
			}
		})
	}
}

func TestIsEvictedByDeactivation(t *testing.T) {
	cases := map[string]struct {
		workload *kueue.Workload
		want     bool
	}{
		"evicted condition doesn't exist": {
			workload: utiltesting.MakeWorkload("test", "test").Obj(),
		},
		"evicted condition with false status": {
			workload: utiltesting.MakeWorkload("test", "test").
				Condition(metav1.Condition{
					Type:   kueue.WorkloadEvicted,
					Reason: kueue.WorkloadDeactivated,
					Status: metav1.ConditionFalse,
				}).
				Obj(),
		},
		"evicted condition with PodsReadyTimeout reason": {
			workload: utiltesting.MakeWorkload("test", "test").
				Condition(metav1.Condition{
					Type:   kueue.WorkloadEvicted,
					Reason: kueue.WorkloadEvictedByPodsReadyTimeout,
					Status: metav1.ConditionTrue,
				}).
				Obj(),
		},
		"evicted condition with Deactivated reason": {
			workload: utiltesting.MakeWorkload("test", "test").
				Condition(metav1.Condition{
					Type:   kueue.WorkloadEvicted,
					Reason: kueue.WorkloadDeactivated,
					Status: metav1.ConditionTrue,
				}).
				Obj(),
			want: true,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := IsEvictedByDeactivation(tc.workload)
			if tc.want != got {
				t.Errorf("Unexpected result from IsEvictedByDeactivation\nwant:%v\ngot:%v\n", tc.want, got)
			}
		})
	}
}

func TestIsEvictedByPodsReadyTimeout(t *testing.T) {
	cases := map[string]struct {
		workload             *kueue.Workload
		wantEvictedByTimeout bool
		wantCondition        *metav1.Condition
	}{
		"evicted condition doesn't exist": {
			workload: utiltesting.MakeWorkload("test", "test").Obj(),
		},
		"evicted condition with false status": {
			workload: utiltesting.MakeWorkload("test", "test").
				Condition(metav1.Condition{
					Type:   kueue.WorkloadEvicted,
					Reason: kueue.WorkloadEvictedByPodsReadyTimeout,
					Status: metav1.ConditionFalse,
				}).
				Obj(),
		},
		"evicted condition with Preempted reason": {
			workload: utiltesting.MakeWorkload("test", "test").
				Condition(metav1.Condition{
					Type:   kueue.WorkloadEvicted,
					Reason: kueue.WorkloadEvictedByPreemption,
					Status: metav1.ConditionTrue,
				}).
				Obj(),
		},
		"evicted condition with PodsReadyTimeout reason": {
			workload: utiltesting.MakeWorkload("test", "test").
				Condition(metav1.Condition{
					Type:   kueue.WorkloadEvicted,
					Reason: kueue.WorkloadEvictedByPodsReadyTimeout,
					Status: metav1.ConditionTrue,
				}).
				Obj(),
			wantEvictedByTimeout: true,
			wantCondition: &metav1.Condition{
				Type:   kueue.WorkloadEvicted,
				Reason: kueue.WorkloadEvictedByPodsReadyTimeout,
				Status: metav1.ConditionTrue,
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			gotCondition, gotEvictedByTimeout := IsEvictedByPodsReadyTimeout(tc.workload)
			if tc.wantEvictedByTimeout != gotEvictedByTimeout {
				t.Errorf("Unexpected evictedByTimeout from IsEvictedByPodsReadyTimeout\nwant:%v\ngot:%v\n",
					tc.wantEvictedByTimeout, gotEvictedByTimeout)
			}
			if diff := cmp.Diff(tc.wantCondition, gotCondition,
				cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime")); len(diff) != 0 {
				t.Errorf("Unexpected condition from IsEvictedByPodsReadyTimeout: (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestFlavorResourceUsage(t *testing.T) {
	cases := map[string]struct {
		info *Info
		want resources.FlavorResourceQuantities
	}{
		"nil": {
			want: resources.FlavorResourceQuantities{},
		},
		"one podset, no flavors": {
			info: &Info{
				TotalRequests: []PodSetResources{{
					Requests: resources.Requests{
						corev1.ResourceCPU: 1_000,
						"example.com/gpu":  3,
					},
				}},
			},
			want: resources.FlavorResourceQuantities{
				{Flavor: "", Resource: "cpu"}:             1_000,
				{Flavor: "", Resource: "example.com/gpu"}: 3,
			},
		},
		"one podset, multiple flavors": {
			info: &Info{
				TotalRequests: []PodSetResources{{
					Requests: resources.Requests{
						corev1.ResourceCPU: 1_000,
						"example.com/gpu":  3,
					},
					Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
						corev1.ResourceCPU: "default",
						"example.com/gpu":  "gpu",
					},
				}},
			},
			want: resources.FlavorResourceQuantities{
				{Flavor: "default", Resource: "cpu"}:         1_000,
				{Flavor: "gpu", Resource: "example.com/gpu"}: 3,
			},
		},
		"multiple podsets, multiple flavors": {
			info: &Info{
				TotalRequests: []PodSetResources{
					{
						Requests: resources.Requests{
							corev1.ResourceCPU: 1_000,
							"example.com/gpu":  3,
						},
						Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
							corev1.ResourceCPU: "default",
							"example.com/gpu":  "model_a",
						},
					},
					{
						Requests: resources.Requests{
							corev1.ResourceCPU:    2_000,
							corev1.ResourceMemory: 2 * utiltesting.Gi,
						},
						Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
							corev1.ResourceCPU:    "default",
							corev1.ResourceMemory: "default",
						},
					},
					{
						Requests: resources.Requests{
							"example.com/gpu": 1,
						},
						Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
							"example.com/gpu": "model_b",
						},
					},
				},
			},
			want: resources.FlavorResourceQuantities{
				{Flavor: "default", Resource: "cpu"}:             3_000,
				{Flavor: "default", Resource: "memory"}:          2 * utiltesting.Gi,
				{Flavor: "model_a", Resource: "example.com/gpu"}: 3,
				{Flavor: "model_b", Resource: "example.com/gpu"}: 1,
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := tc.info.FlavorResourceUsage()
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("info.ResourceUsage() returned (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestAdmissionCheckStrategy(t *testing.T) {
	cases := map[string]struct {
		cq                  *kueue.ClusterQueue
		wl                  *kueue.Workload
		wantAdmissionChecks sets.Set[kueue.AdmissionCheckReference]
	}{
		"AdmissionCheckStrategy with a flavor": {
			wl: utiltesting.MakeWorkload("wl", "ns").
				ReserveQuota(utiltesting.MakeAdmission("cq").Assignment("cpu", "flavor1", "1").Obj()).
				Obj(),
			cq: utiltesting.MakeClusterQueue("cq").
				AdmissionCheckStrategy(*utiltesting.MakeAdmissionCheckStrategyRule("ac1", "flavor1").Obj()).
				Obj(),
			wantAdmissionChecks: sets.New[kueue.AdmissionCheckReference]("ac1"),
		},
		"AdmissionCheckStrategy with an unmatched flavor": {
			wl: utiltesting.MakeWorkload("wl", "ns").
				ReserveQuota(utiltesting.MakeAdmission("cq").Assignment("cpu", "flavor1", "1").Obj()).
				Obj(),
			cq: utiltesting.MakeClusterQueue("cq").
				AdmissionCheckStrategy(*utiltesting.MakeAdmissionCheckStrategyRule("ac1", "unmatched-flavor").Obj()).
				Obj(),
			wantAdmissionChecks: nil,
		},
		"AdmissionCheckStrategy without a flavor": {
			wl: utiltesting.MakeWorkload("wl", "ns").
				ReserveQuota(utiltesting.MakeAdmission("cq").Assignment("cpu", "flavor1", "1").Obj()).
				Obj(),
			cq: utiltesting.MakeClusterQueue("cq").
				AdmissionCheckStrategy(*utiltesting.MakeAdmissionCheckStrategyRule("ac1").Obj()).
				Obj(),
			wantAdmissionChecks: sets.New[kueue.AdmissionCheckReference]("ac1"),
		},
		"Two AdmissionCheckStrategies, one with flavor, one without flavor": {
			wl: utiltesting.MakeWorkload("wl", "ns").
				ReserveQuota(utiltesting.MakeAdmission("cq").Assignment("cpu", "flavor1", "1").Obj()).
				Obj(),
			cq: utiltesting.MakeClusterQueue("cq").
				AdmissionCheckStrategy(
					*utiltesting.MakeAdmissionCheckStrategyRule("ac1", "flavor1").Obj(),
					*utiltesting.MakeAdmissionCheckStrategyRule("ac2").Obj()).
				Obj(),
			wantAdmissionChecks: sets.New[kueue.AdmissionCheckReference]("ac1", "ac2"),
		},
		"Workload has no QuotaReserved": {
			wl: utiltesting.MakeWorkload("wl", "ns").
				Obj(),
			cq: utiltesting.MakeClusterQueue("cq").
				AdmissionCheckStrategy(
					*utiltesting.MakeAdmissionCheckStrategyRule("ac1", "flavor1").Obj(),
					*utiltesting.MakeAdmissionCheckStrategyRule("ac2").Obj()).
				Obj(),
			wantAdmissionChecks: nil,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, log := utiltesting.ContextWithLog(t)
			gotAdmissionChecks := AdmissionChecksForWorkload(log, tc.wl, admissioncheck.NewAdmissionChecks(tc.cq))

			if diff := cmp.Diff(tc.wantAdmissionChecks, gotAdmissionChecks); diff != "" {
				t.Errorf("Unexpected AdmissionChecks, (want-/got+):\n%s", diff)
			}
		})
	}
}
