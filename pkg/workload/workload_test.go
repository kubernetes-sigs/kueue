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
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/component-base/featuregate"
	testingclock "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
	qutil "sigs.k8s.io/kueue/pkg/util/queue"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestNewInfo(t *testing.T) {
	cases := map[string]struct {
		workload     kueue.Workload
		infoOptions  []InfoOption
		wantInfo     Info
		featureGates map[featuregate.Feature]bool
	}{
		"pending": {
			workload: *utiltestingapi.MakeWorkload("", "").
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
		"pending with reclaim; reclaimablePods on": {
			workload: *utiltestingapi.MakeWorkload("", "").
				PodSets(
					*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 5).
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
		"pending with reclaim; reclaimablePods off": {
			workload: *utiltestingapi.MakeWorkload("", "").
				PodSets(
					*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 5).
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
							corev1.ResourceCPU:    5 * 10,
							corev1.ResourceMemory: 5 * 512 * 1024,
						},
						Count: 5,
					},
				},
			},
			featureGates: map[featuregate.Feature]bool{
				features.ReclaimablePods: false,
			},
		},
		"admitted": {
			workload: *utiltestingapi.MakeWorkload("", "").
				PodSets(
					*utiltestingapi.MakePodSet("driver", 1).
						Request(corev1.ResourceCPU, "10m").
						Request(corev1.ResourceMemory, "512Ki").
						Obj(),
					*utiltestingapi.MakePodSet("workers", 3).
						Request(corev1.ResourceCPU, "5m").
						Request(corev1.ResourceMemory, "1Mi").
						Request("ex.com/gpu", "1").
						Obj(),
				).
				ReserveQuota(utiltestingapi.MakeAdmission("foo").
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
		"admitted with reclaim; reclaimablePods on": {
			workload: *utiltestingapi.MakeWorkload("", "").
				PodSets(
					*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 5).
						Request(corev1.ResourceCPU, "10m").
						Request(corev1.ResourceMemory, "10Ki").
						Obj(),
				).
				ReserveQuota(
					utiltestingapi.MakeAdmission("").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "f1", "30m").
							Assignment(corev1.ResourceMemory, "f1", "30Ki").
							Count(3).
							Obj()).
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
		"admitted with reclaim; reclaimablePods off": {
			workload: *utiltestingapi.MakeWorkload("", "").
				PodSets(
					*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 5).
						Request(corev1.ResourceCPU, "10m").
						Request(corev1.ResourceMemory, "10Ki").
						Obj(),
				).
				ReserveQuota(
					utiltestingapi.MakeAdmission("").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "f1", "50m").
							Assignment(corev1.ResourceMemory, "f1", "50Ki").
							Count(5).
							Obj()).
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
							corev1.ResourceCPU:    5 * 10,
							corev1.ResourceMemory: 5 * 10 * 1024,
						},
						Count: 5,
					},
				},
			},
			featureGates: map[featuregate.Feature]bool{
				features.ReclaimablePods: false,
			}},
		"admitted with reclaim and increased reclaim": {
			workload: *utiltestingapi.MakeWorkload("", "").
				PodSets(
					*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 5).
						Request(corev1.ResourceCPU, "10m").
						Request(corev1.ResourceMemory, "10Ki").
						Obj(),
				).
				ReserveQuota(
					utiltestingapi.MakeAdmission("").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "f1", "30m").
							Assignment(corev1.ResourceMemory, "f1", "30Ki").
							Count(3).
							Obj()).
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
			workload: *utiltestingapi.MakeWorkload("", "").
				PodSets(
					*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 5).
						Request(corev1.ResourceCPU, "10m").
						Request(corev1.ResourceMemory, "10Ki").
						Obj(),
				).
				ReserveQuota(
					utiltestingapi.MakeAdmission("").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "f1", "30m").
							Assignment(corev1.ResourceMemory, "f1", "30Ki").
							Count(3).
							Obj()).
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
			workload: *utiltestingapi.MakeWorkload("", "").
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
			workload: *utiltestingapi.MakeWorkload("transform", "").
				PodSets(
					*utiltestingapi.MakePodSet("a", 1).
						Request("nvidia.com/mig-1g.5gb", "2").
						Request("nvidia.com/mig-2g.10gb", "1").
						Request(corev1.ResourceCPU, "1").
						Obj(),
					*utiltestingapi.MakePodSet("b", 2).
						Request("nvidia.com/gpu", "1").
						Request(corev1.ResourceCPU, "2").
						Obj(),
				).
				Obj(),
			infoOptions: []InfoOption{WithResourceTransformations([]config.ResourceTransformation{
				{
					Input:    "nvidia.com/mig-1g.5gb",
					Strategy: ptr.To(config.Replace),
					Outputs: corev1.ResourceList{
						"example.com/accelerator-memory": resource.MustParse("5Ki"),
						"example.com/credits":            resource.MustParse("10"),
					},
				},
				{
					Input:    "nvidia.com/mig-2g.10gb",
					Strategy: ptr.To(config.Replace),
					Outputs: corev1.ResourceList{
						"example.com/accelerator-memory": resource.MustParse("10Ki"),
						"example.com/credits":            resource.MustParse("15"),
					},
				},
				{
					Input:    "nvidia.com/gpu",
					Strategy: ptr.To(config.Retain),
					Outputs: corev1.ResourceList{
						"example.com/accelerator-memory": resource.MustParse("40Ki"),
						"example.com/credits":            resource.MustParse("100"),
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
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			for fg, enabled := range tc.featureGates {
				features.SetFeatureGateDuringTest(t, fg, enabled)
			}
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
			ctx, _ := utiltesting.ContextWithLog(t)
			workload := utiltestingapi.MakeWorkload("foo", "bar").Generation(1).Obj()
			workload.Status = tc.oldStatus
			cl := utiltesting.NewFakeClientSSAAsSM(workload)
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
			wl: utiltestingapi.MakeWorkload("name", "ns").
				Creation(creationTime.Time).
				Obj(),
			want: map[Ordering]metav1.Time{
				evictionOrdering: creationTime,
				creationOrdering: creationTime,
			},
		},
		"evicted by preemption": {
			wl: utiltestingapi.MakeWorkload("name", "ns").
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
			wl: utiltestingapi.MakeWorkload("name", "ns").
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
			wl: utiltestingapi.MakeWorkload("name", "ns").
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
			workload: utiltestingapi.MakeWorkload("test", "test").Obj(),
		},
		"evicted condition with false status": {
			workload: utiltestingapi.MakeWorkload("test", "test").
				Condition(metav1.Condition{
					Type:   kueue.WorkloadEvicted,
					Reason: kueue.WorkloadDeactivated,
					Status: metav1.ConditionFalse,
				}).
				Obj(),
		},
		"evicted condition with PodsReadyTimeout reason": {
			workload: utiltestingapi.MakeWorkload("test", "test").
				Condition(metav1.Condition{
					Type:   kueue.WorkloadEvicted,
					Reason: kueue.WorkloadEvictedByPodsReadyTimeout,
					Status: metav1.ConditionTrue,
				}).
				Obj(),
		},
		"evicted condition with Deactivated reason": {
			workload: utiltestingapi.MakeWorkload("test", "test").
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
			workload: utiltestingapi.MakeWorkload("test", "test").Obj(),
		},
		"evicted condition with false status": {
			workload: utiltestingapi.MakeWorkload("test", "test").
				Condition(metav1.Condition{
					Type:   kueue.WorkloadEvicted,
					Reason: kueue.WorkloadEvictedByPodsReadyTimeout,
					Status: metav1.ConditionFalse,
				}).
				Obj(),
		},
		"evicted condition with Preempted reason": {
			workload: utiltestingapi.MakeWorkload("test", "test").
				Condition(metav1.Condition{
					Type:   kueue.WorkloadEvicted,
					Reason: kueue.WorkloadEvictedByPreemption,
					Status: metav1.ConditionTrue,
				}).
				Obj(),
		},
		"evicted condition with PodsReadyTimeout reason": {
			workload: utiltestingapi.MakeWorkload("test", "test").
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
			wl: utiltestingapi.MakeWorkload("wl", "ns").
				ReserveQuota(utiltestingapi.MakeAdmission("cq").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment("cpu", "flavor1", "1").
						Obj()).
					Obj()).
				Obj(),
			cq: utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("flavor1").Obj(), *utiltestingapi.MakeFlavorQuotas("flavor2").Obj()).
				AdmissionCheckStrategy(*utiltestingapi.MakeAdmissionCheckStrategyRule("ac1", "flavor1").Obj()).
				Obj(),
			wantAdmissionChecks: sets.New[kueue.AdmissionCheckReference]("ac1"),
		},
		"AdmissionCheckStrategy with an unmatched flavor": {
			wl: utiltestingapi.MakeWorkload("wl", "ns").
				ReserveQuota(utiltestingapi.MakeAdmission("cq").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment("cpu", "flavor1", "1").
						Obj()).
					Obj()).
				Obj(),
			cq: utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("flavor1").Obj(), *utiltestingapi.MakeFlavorQuotas("flavor2").Obj()).
				AdmissionCheckStrategy(*utiltestingapi.MakeAdmissionCheckStrategyRule("ac1", "unmatched-flavor").Obj()).
				Obj(),
			wantAdmissionChecks: nil,
		},
		"AdmissionCheckStrategy without a flavor": {
			wl: utiltestingapi.MakeWorkload("wl", "ns").
				ReserveQuota(utiltestingapi.MakeAdmission("cq").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment("cpu", "flavor1", "1").
						Obj()).
					Obj()).
				Obj(),
			cq: utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("flavor1").Obj(), *utiltestingapi.MakeFlavorQuotas("flavor2").Obj()).
				AdmissionCheckStrategy(*utiltestingapi.MakeAdmissionCheckStrategyRule("ac1").Obj()).
				Obj(),
			wantAdmissionChecks: sets.New[kueue.AdmissionCheckReference]("ac1"),
		},
		"Two AdmissionCheckStrategies, one with flavor, one without flavor": {
			wl: utiltestingapi.MakeWorkload("wl", "ns").
				ReserveQuota(utiltestingapi.MakeAdmission("cq").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment("cpu", "flavor1", "1").
						Obj()).
					Obj()).
				Obj(),
			cq: utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("flavor1").Obj(), *utiltestingapi.MakeFlavorQuotas("flavor2").Obj()).
				AdmissionCheckStrategy(
					*utiltestingapi.MakeAdmissionCheckStrategyRule("ac1", "flavor1").Obj(),
					*utiltestingapi.MakeAdmissionCheckStrategyRule("ac2").Obj()).
				Obj(),
			wantAdmissionChecks: sets.New[kueue.AdmissionCheckReference]("ac1", "ac2"),
		},
		"AdmissionCheckStrategy with a non-existent flavor": {
			wl: utiltestingapi.MakeWorkload("wl", "ns").
				ReserveQuota(utiltestingapi.MakeAdmission("cq").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment("cpu", "flavor1", "1").
						Obj()).
					Obj()).
				Obj(),
			cq: utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("flavor1").Obj()).
				AdmissionCheckStrategy(
					*utiltestingapi.MakeAdmissionCheckStrategyRule("ac1", "flavor-nonexistent").Obj()).
				Obj(),
			wantAdmissionChecks: sets.New[kueue.AdmissionCheckReference](),
		},
		"Workload has no QuotaReserved": {
			wl: utiltestingapi.MakeWorkload("wl", "ns").
				Obj(),
			cq: utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("flavor1").Obj(), *utiltestingapi.MakeFlavorQuotas("flavor2").Obj()).
				AdmissionCheckStrategy(
					*utiltestingapi.MakeAdmissionCheckStrategyRule("ac1", "flavor1").Obj(),
					*utiltestingapi.MakeAdmissionCheckStrategyRule("ac2").Obj()).
				Obj(),
			wantAdmissionChecks: nil,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, log := utiltesting.ContextWithLog(t)
			gotAdmissionChecks := AdmissionChecksForWorkload(log, tc.wl, admissioncheck.NewAdmissionChecks(tc.cq), qutil.AllFlavors(tc.cq.Spec.ResourceGroups))

			if diff := cmp.Diff(tc.wantAdmissionChecks, gotAdmissionChecks); diff != "" {
				t.Errorf("Unexpected AdmissionChecks, (want-/got+):\n%s", diff)
			}
		})
	}
}

func TestPropagateResourceRequests(t *testing.T) {
	cases := map[string]struct {
		wl   *kueue.Workload
		info *Info
		want bool
	}{
		"one podset, no diff": {
			wl: &kueue.Workload{
				Status: kueue.WorkloadStatus{
					ResourceRequests: []kueue.PodSetRequest{
						{
							Name: "ps1",
							Resources: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("10"),
								corev1.ResourceMemory: resource.MustParse("10Mi"),
								"nvidia.com/gpu":      resource.MustParse("1"),
							},
						},
					},
				},
			},
			info: &Info{
				TotalRequests: []PodSetResources{{
					Name: "ps1",
					Requests: resources.Requests{
						corev1.ResourceCPU:    10000,
						corev1.ResourceMemory: 10 * 1024 * 1024,
						"nvidia.com/gpu":      1,
					},
				}},
			},
			want: false,
		},
		"one podset, memory missing diff": {
			wl: &kueue.Workload{
				Status: kueue.WorkloadStatus{
					ResourceRequests: []kueue.PodSetRequest{
						{
							Name: "ps1",
							Resources: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("10"),
								corev1.ResourceMemory: resource.MustParse("10Mi"),
								"nvidia.com/gpu":      resource.MustParse("1"),
							},
						},
					},
				},
			},
			info: &Info{
				TotalRequests: []PodSetResources{{
					Name: "ps1",
					Requests: resources.Requests{
						corev1.ResourceCPU: 5000,
						"nvidia.com/gpu":   1,
					},
				}},
			},
			want: true,
		},
		"one podset, cpu diff": {
			wl: &kueue.Workload{
				Status: kueue.WorkloadStatus{
					ResourceRequests: []kueue.PodSetRequest{
						{
							Name: "ps1",
							Resources: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("10"),
								corev1.ResourceMemory: resource.MustParse("10Mi"),
								"nvidia.com/gpu":      resource.MustParse("1"),
							},
						},
					},
				},
			},
			info: &Info{
				TotalRequests: []PodSetResources{{
					Name: "ps1",
					Requests: resources.Requests{
						corev1.ResourceCPU:    5000,
						corev1.ResourceMemory: 10 * 1024 * 1024,
						"nvidia.com/gpu":      1,
					},
				}},
			},
			want: true,
		},
		"one podset, memory diff": {
			wl: &kueue.Workload{
				Status: kueue.WorkloadStatus{
					ResourceRequests: []kueue.PodSetRequest{
						{
							Name: "ps1",
							Resources: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("10"),
								corev1.ResourceMemory: resource.MustParse("10Gi"),
								"nvidia.com/gpu":      resource.MustParse("1"),
							},
						},
					},
				},
			},
			info: &Info{
				TotalRequests: []PodSetResources{{
					Name: "ps1",
					Requests: resources.Requests{
						corev1.ResourceCPU:    10000,
						corev1.ResourceMemory: 10 * 1024 * 1024,
						"nvidia.com/gpu":      1,
					},
				}},
			},
			want: true,
		},
		"one podset, gpu (extended resource) diff": {
			wl: &kueue.Workload{
				Status: kueue.WorkloadStatus{
					ResourceRequests: []kueue.PodSetRequest{
						{
							Name: "ps1",
							Resources: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("10"),
								corev1.ResourceMemory: resource.MustParse("10Mi"),
								"nvidia.com/gpu":      resource.MustParse("1"),
							},
						},
					},
				},
			},
			info: &Info{
				TotalRequests: []PodSetResources{{
					Name: "ps1",
					Requests: resources.Requests{
						corev1.ResourceCPU:    10000,
						corev1.ResourceMemory: 10 * 1024 * 1024,
						"nvidia.com/gpu":      2,
					},
				}},
			},
			want: true,
		},
		"two podset, no diff ": {
			wl: &kueue.Workload{
				Status: kueue.WorkloadStatus{
					ResourceRequests: []kueue.PodSetRequest{
						{
							Name: "ps1",
							Resources: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("10"),
								corev1.ResourceMemory: resource.MustParse("10Mi"),
								"nvidia.com/gpu":      resource.MustParse("1"),
							},
						},
						{
							Name: "ps2",
							Resources: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("20"),
								corev1.ResourceMemory: resource.MustParse("20Mi"),
								"nvidia.com/gpu":      resource.MustParse("2"),
							},
						},
					},
				},
			},
			info: &Info{
				TotalRequests: []PodSetResources{
					{
						Name: "ps1",
						Requests: resources.Requests{
							corev1.ResourceCPU:    10000,
							corev1.ResourceMemory: 10 * 1024 * 1024,
							"nvidia.com/gpu":      1,
						},
					},
					{
						Name: "ps2",
						Requests: resources.Requests{
							corev1.ResourceCPU:    20000,
							corev1.ResourceMemory: 20 * 1024 * 1024,
							"nvidia.com/gpu":      2,
						},
					},
				},
			},
			want: false,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := PropagateResourceRequests(tc.wl, tc.info)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("Unexpected PropagateResourceRequests() result (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestNeedsSecondPass(t *testing.T) {
	defaultSingleLevelTopology := *utiltestingapi.MakeDefaultOneLevelTopology("tas-single-level")
	cases := map[string]struct {
		wl   *kueue.Workload
		want bool
	}{
		"admitted workload with UnhealthyNode": {
			wl: utiltestingapi.MakeWorkload("foo", "default").
				UnhealthyNodes("x0").
				Queue("tas-main").
				PodSets(*utiltestingapi.MakePodSet("one", 1).
					PreferredTopologyRequest(corev1.LabelHostname).
					Request(corev1.ResourceCPU, "1").
					Obj()).
				ReserveQuota(
					utiltestingapi.MakeAdmission("tas-main").
						PodSets(utiltestingapi.MakePodSetAssignment("one").
							Assignment(corev1.ResourceCPU, "tas-default", "1000m").
							TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
								Domains(utiltestingapi.MakeTopologyDomainAssignment([]string{"x0"}, 1).Obj()).
								Obj()).
							Obj()).
						Obj(),
				).
				Admitted(true).
				Obj(),
			want: true,
		},
		"admitted workload without UnhealthyNode": {
			wl: utiltestingapi.MakeWorkload("foo", "default").
				Queue("tas-main").
				PodSets(*utiltestingapi.MakePodSet("one", 1).
					PreferredTopologyRequest(corev1.LabelHostname).
					Request(corev1.ResourceCPU, "1").
					Obj()).
				ReserveQuota(
					utiltestingapi.MakeAdmission("tas-main").
						PodSets(utiltestingapi.MakePodSetAssignment("one").
							Assignment(corev1.ResourceCPU, "tas-default", "1000m").
							TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
								Domains(utiltestingapi.MakeTopologyDomainAssignment([]string{"x0"}, 1).Obj()).
								Obj()).
							Obj()).
						Obj(),
				).
				Admitted(true).
				Obj(),
			want: false,
		},
		"admitted workload with UnhealthyNode, but no node in the assignment": {
			wl: utiltestingapi.MakeWorkload("foo", "default").
				UnhealthyNodes("x0").
				Queue("tas-main").
				PodSets(*utiltestingapi.MakePodSet("one", 1).
					PreferredTopologyRequest(corev1.LabelHostname).
					Request(corev1.ResourceCPU, "1").
					Obj()).
				ReserveQuota(
					utiltestingapi.MakeAdmission("tas-main").
						PodSets(utiltestingapi.MakePodSetAssignment("one").
							Assignment(corev1.ResourceCPU, "tas-default", "1000m").
							TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
								Domains(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
								Obj()).
							Obj()).
						Obj(),
				).
				Admitted(true).
				Obj(),
			want: false,
		},
		"finished workload with UnhealthyNode": {
			wl: utiltestingapi.MakeWorkload("foo", "default").
				UnhealthyNodes("x0").
				Queue("tas-main").
				PodSets(*utiltestingapi.MakePodSet("one", 1).
					PreferredTopologyRequest(corev1.LabelHostname).
					Request(corev1.ResourceCPU, "1").
					Obj()).
				ReserveQuota(
					utiltestingapi.MakeAdmission("tas-main").
						PodSets(utiltestingapi.MakePodSetAssignment("one").
							Assignment(corev1.ResourceCPU, "tas-default", "1000m").
							TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
								Domains(utiltestingapi.MakeTopologyDomainAssignment([]string{"x0"}, 1).Obj()).
								Obj()).
							Obj()).
						Obj(),
				).
				Admitted(true).
				Finished().
				Obj(),
			want: false,
		},
		"evicted workload with UnhealthyNode": {
			wl: utiltestingapi.MakeWorkload("foo", "default").
				UnhealthyNodes("x0").
				Queue("tas-main").
				PodSets(*utiltestingapi.MakePodSet("one", 1).
					PreferredTopologyRequest(corev1.LabelHostname).
					Request(corev1.ResourceCPU, "1").
					Obj()).
				ReserveQuota(
					utiltestingapi.MakeAdmission("tas-main").
						PodSets(utiltestingapi.MakePodSetAssignment("one").
							Assignment(corev1.ResourceCPU, "tas-default", "1000m").
							TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
								Domains(utiltestingapi.MakeTopologyDomainAssignment([]string{"x0"}, 1).Obj()).
								Obj()).
							Obj()).
						Obj(),
				).
				Admitted(true).
				Evicted().
				Obj(),
			want: false,
		},
		"quotaReserved and admission checks Ready when workload delayedTopologyRequest=Pending": {
			wl: utiltestingapi.MakeWorkload("foo", "default").
				Queue("tas-main").
				PodSets(*utiltestingapi.MakePodSet("one", 1).
					RequiredTopologyRequest(corev1.LabelHostname).
					Request(corev1.ResourceCPU, "1").
					Obj()).
				ReserveQuota(
					utiltestingapi.MakeAdmission("tas-main").
						PodSets(
							utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "1000m").
								DelayedTopologyRequest(kueue.DelayedTopologyRequestStatePending).
								Obj(),
						).
						Obj(),
				).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:  "prov-check",
					State: kueue.CheckStateReady,
				}).
				Obj(),
			want: true,
		},
		"quotaReserved and admission checks Pending": {
			wl: utiltestingapi.MakeWorkload("foo", "default").
				Queue("tas-main").
				PodSets(*utiltestingapi.MakePodSet("one", 1).
					RequiredTopologyRequest(corev1.LabelHostname).
					Request(corev1.ResourceCPU, "1").
					Obj()).
				ReserveQuota(
					utiltestingapi.MakeAdmission("tas-main").
						PodSets(
							utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "1000m").
								DelayedTopologyRequest(kueue.DelayedTopologyRequestStatePending).
								Obj(),
						).
						Obj(),
				).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:  "prov-check",
					State: kueue.CheckStatePending,
				}).
				Obj(),
			want: false,
		},
		"workload without quota": {
			wl: utiltestingapi.MakeWorkload("foo", "default").
				Queue("tas-main").
				PodSets(*utiltestingapi.MakePodSet("one", 1).
					RequiredTopologyRequest(corev1.LabelHostname).
					Request(corev1.ResourceCPU, "1").
					Obj()).
				Obj(),
			want: false,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := NeedsSecondPass(tc.wl)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("Unexpected NeedsSecondPass() result (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestWithPreprocessedDRAResources(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.DynamicResourceAllocation, true)

	cases := map[string]struct {
		workload     kueue.Workload
		draResources map[kueue.PodSetReference]corev1.ResourceList
		wantInfo     Info
	}{
		"single podset with DRA resources": {
			workload: *utiltestingapi.MakeWorkload("test-wl", "default").
				PodSets(*utiltestingapi.MakePodSet("main", 1).
					Request(corev1.ResourceCPU, "100m").
					Obj()).
				Obj(),
			draResources: map[kueue.PodSetReference]corev1.ResourceList{
				"main": {
					"gpus": resource.MustParse("2"),
				},
			},
			wantInfo: Info{
				TotalRequests: []PodSetResources{
					{
						Name:  "main",
						Count: 1,
						Requests: resources.Requests{
							corev1.ResourceCPU: 100,
							"gpus":             2,
						},
					},
				},
			},
		},
		"multiple podsets with different DRA resources": {
			workload: *utiltestingapi.MakeWorkload("test-wl", "default").
				PodSets(
					*utiltestingapi.MakePodSet("main", 1).
						Request(corev1.ResourceCPU, "100m").
						Obj(),
					*utiltestingapi.MakePodSet("worker", 2).
						Request(corev1.ResourceMemory, "1Gi").
						Obj(),
				).
				Obj(),
			draResources: map[kueue.PodSetReference]corev1.ResourceList{
				"main": {
					"gpus": resource.MustParse("2"),
				},
				"worker": {
					"foo-accelerator": resource.MustParse("1"),
				},
			},
			wantInfo: Info{
				TotalRequests: []PodSetResources{
					{
						Name:  "main",
						Count: 1,
						Requests: resources.Requests{
							corev1.ResourceCPU: 100,
							"gpus":             2,
						},
					},
					{
						Name:  "worker",
						Count: 2,
						Requests: resources.Requests{
							corev1.ResourceMemory: 2 * 1024 * 1024 * 1024,
							"foo-accelerator":     2,
						},
					},
				},
			},
		},
		"no DRA resources for podset": {
			workload: *utiltestingapi.MakeWorkload("test-wl", "default").
				PodSets(
					*utiltestingapi.MakePodSet("main", 1).
						Request(corev1.ResourceCPU, "100m").
						Obj(),
					*utiltestingapi.MakePodSet("worker", 1).
						Request(corev1.ResourceMemory, "512Mi").
						Obj(),
				).
				Obj(),
			draResources: map[kueue.PodSetReference]corev1.ResourceList{
				"main": {
					"gpus": resource.MustParse("1"),
				},
			},
			wantInfo: Info{
				TotalRequests: []PodSetResources{
					{
						Name:  "main",
						Count: 1,
						Requests: resources.Requests{
							corev1.ResourceCPU: 100,
							"gpus":             1,
						},
					},
					{
						Name:  "worker",
						Count: 1,
						Requests: resources.Requests{
							corev1.ResourceMemory: 512 * 1024 * 1024,
						},
					},
				},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			info := NewInfo(&tc.workload, WithPreprocessedDRAResources(tc.draResources))

			if diff := cmp.Diff(tc.wantInfo.TotalRequests, info.TotalRequests); diff != "" {
				t.Errorf("Unexpected TotalRequests (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestSetQuotaReservation(t *testing.T) {
	// test clock and time "constants" uses in conditions.
	testClock := testingclock.NewFakeClock(time.Now())
	now := testClock.Now()
	fiveMinutesAgo := now.Add(-5 * time.Minute)

	// admission "constants" values used in all test cases.
	admission := utiltestingapi.MakeAdmission("test-queue").Obj()
	quotaReservedReason := "QuotaReserved"
	quotaReservedMessage := fmt.Sprintf("Quota reserved in ClusterQueue %s", admission.ClusterQueue)

	// newWorkload wrapper to reduce boilerplate in test cases.
	newWorkload := func() *utiltestingapi.WorkloadWrapper {
		return utiltestingapi.MakeWorkload("test", "default").Generation(1)
	}

	// newCondition helper.
	newCondition := func(condition string, status metav1.ConditionStatus, reason, message string, ltt time.Time) metav1.Condition {
		return metav1.Condition{
			Type:               condition,
			Status:             status,
			Reason:             reason,
			Message:            message,
			ObservedGeneration: 1,
			LastTransitionTime: metav1.NewTime(ltt),
		}
	}

	// test cases.
	type args struct {
		workload  *kueue.Workload
		admission *kueue.Admission
	}
	tests := map[string]struct {
		args args
		want *kueue.Workload
	}{
		"WorkloadWithoutConditions": {
			args: args{
				workload:  newWorkload().Obj(),
				admission: admission,
			},
			want: newWorkload().
				Admission(admission).
				Condition(newCondition(kueue.WorkloadQuotaReserved, metav1.ConditionTrue, quotaReservedReason, quotaReservedMessage, now)).
				Obj(),
		},
		"WorkloadWithActiveConditions": {
			args: args{
				workload: newWorkload().
					Conditions(
						newCondition(kueue.WorkloadEvicted, metav1.ConditionTrue, "TestEvictedReason", "test evicted message", fiveMinutesAgo),
						newCondition(kueue.WorkloadPreempted, metav1.ConditionTrue, "TestPreemptedReason", "test preempted message", fiveMinutesAgo),
						newCondition(kueue.WorkloadQuotaReserved, metav1.ConditionFalse, "TestReason", "test message", fiveMinutesAgo),
					).Obj(),
				admission: admission,
			},
			want: newWorkload().
				Admission(admission).
				Conditions(
					newCondition(kueue.WorkloadEvicted, metav1.ConditionFalse, quotaReservedReason, "Previously: test evicted message", now),
					newCondition(kueue.WorkloadPreempted, metav1.ConditionFalse, quotaReservedReason, "Previously: test preempted message", now),
					newCondition(kueue.WorkloadQuotaReserved, metav1.ConditionTrue, quotaReservedReason, quotaReservedMessage, now),
				).
				Obj(),
		},
		"WorkloadWithInactiveConditions": {
			args: args{
				workload: newWorkload().
					Conditions(
						newCondition(kueue.WorkloadEvicted, metav1.ConditionFalse, quotaReservedReason, "Previously: test evicted message", fiveMinutesAgo),
						newCondition(kueue.WorkloadPreempted, metav1.ConditionFalse, quotaReservedReason, "Previously: test preempted message", fiveMinutesAgo),
						newCondition(kueue.WorkloadQuotaReserved, metav1.ConditionFalse, quotaReservedReason, quotaReservedMessage, fiveMinutesAgo),
					).Obj(),
				admission: admission,
			},
			want: newWorkload().
				Admission(admission).
				Conditions(
					newCondition(kueue.WorkloadEvicted, metav1.ConditionFalse, quotaReservedReason, "Previously: test evicted message", fiveMinutesAgo),
					newCondition(kueue.WorkloadPreempted, metav1.ConditionFalse, quotaReservedReason, "Previously: test preempted message", fiveMinutesAgo),
					newCondition(kueue.WorkloadQuotaReserved, metav1.ConditionTrue, quotaReservedReason, quotaReservedMessage, now),
				).
				Obj(),
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			SetQuotaReservation(tt.args.workload, tt.args.admission, testClock)
			if diff := cmp.Diff(tt.want, tt.args.workload); diff != "" {
				t.Errorf("SetQuotaReservation() (-want +got):\n%s", diff)
			}
		})
	}
}

func TestPatchAdmissionStatus(t *testing.T) {
	now := time.Now()
	fakeClock := testingclock.NewFakeClock(now)
	type patchCall struct {
		updated bool
		err     error
	}

	errUpdate := errors.New("update error")

	tests := map[string]struct {
		patchCall patchCall
		wantErr   error
	}{
		"update returns true": {
			patchCall: patchCall{updated: true, err: nil},
			wantErr:   nil,
		},
		"update returns false": {
			patchCall: patchCall{updated: false, err: nil},
			wantErr:   nil,
		},
		"update returns error": {
			patchCall: patchCall{updated: false, err: errUpdate},
			wantErr:   errUpdate,
		},
	}
	for name, tc := range tests {
		for _, featureEnabled := range []bool{true, false} {
			t.Run(name, func(t *testing.T) {
				features.SetFeatureGateDuringTest(t, features.WorkloadRequestUseMergePatch, featureEnabled)
				ctx, _ := utiltesting.ContextWithLog(t)
				wl := utiltestingapi.MakeWorkload("foo", "default").Obj()
				var cl client.Client
				if !featureEnabled {
					cl = utiltesting.NewFakeClientSSAAsSM(wl)
				} else {
					cl = utiltesting.NewFakeClient(wl)
				}
				called := false
				gotErr := PatchAdmissionStatus(ctx, cl, wl, fakeClock, func() (bool, error) {
					called = true
					return tc.patchCall.updated, tc.patchCall.err
				})
				if diff := cmp.Diff(tc.wantErr, gotErr, cmpopts.EquateErrors()); diff != "" {
					t.Errorf("Unexpected error (-want/+got)\n%s", diff)
				}
				if featureEnabled && !called {
					t.Errorf("expected update func to be called when feature enabled")
				}
				if !featureEnabled && tc.patchCall.updated && !called {
					t.Errorf("expected update func to be called when feature disabled and update true")
				}
			})
		}
	}
}

func TestSetRequeueState(t *testing.T) {
	baseTime := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	futureTime := baseTime.Add(5 * time.Minute)
	evenMoreFutureTime := baseTime.Add(10 * time.Minute)
	pastTime := baseTime.Add(-5 * time.Minute)

	cases := map[string]struct {
		workload       *kueue.Workload
		waitUntil      metav1.Time
		incrementCount bool
		wantUpdated    bool
		wantRequeueAt  *metav1.Time
		wantCount      *int32
	}{
		"should initialize and set time when requeue state is nil without increment": {
			workload: &kueue.Workload{
				Status: kueue.WorkloadStatus{
					RequeueState: nil,
				},
			},
			waitUntil:      metav1.NewTime(futureTime),
			incrementCount: false,
			wantUpdated:    true,
			wantRequeueAt:  ptr.To(metav1.NewTime(futureTime)),
			wantCount:      nil,
		},
		"should initialize and set time and count when requeue state is nil with increment": {
			workload: &kueue.Workload{
				Status: kueue.WorkloadStatus{
					RequeueState: nil,
				},
			},
			waitUntil:      metav1.NewTime(futureTime),
			incrementCount: true,
			wantUpdated:    true,
			wantRequeueAt:  ptr.To(metav1.NewTime(futureTime)),
			wantCount:      ptr.To[int32](1),
		},
		"should update time when existing requeue time is earlier": {
			workload: &kueue.Workload{
				Status: kueue.WorkloadStatus{
					RequeueState: &kueue.RequeueState{
						RequeueAt: ptr.To(metav1.NewTime(pastTime)),
						Count:     ptr.To[int32](2),
					},
				},
			},
			waitUntil:      metav1.NewTime(futureTime),
			incrementCount: false,
			wantUpdated:    true,
			wantRequeueAt:  ptr.To(metav1.NewTime(futureTime)),
			wantCount:      ptr.To[int32](2),
		},
		"should not update time when existing requeue time is later": {
			workload: &kueue.Workload{
				Status: kueue.WorkloadStatus{
					RequeueState: &kueue.RequeueState{
						RequeueAt: ptr.To(metav1.NewTime(evenMoreFutureTime)),
						Count:     ptr.To[int32](3),
					},
				},
			},
			waitUntil:      metav1.NewTime(futureTime),
			incrementCount: false,
			wantUpdated:    false,
			wantRequeueAt:  ptr.To(metav1.NewTime(evenMoreFutureTime)),
			wantCount:      ptr.To[int32](3),
		},
		"should increment count but keep later requeue time": {
			workload: &kueue.Workload{
				Status: kueue.WorkloadStatus{
					RequeueState: &kueue.RequeueState{
						RequeueAt: ptr.To(metav1.NewTime(evenMoreFutureTime)),
						Count:     ptr.To[int32](3),
					},
				},
			},
			waitUntil:      metav1.NewTime(futureTime),
			incrementCount: true,
			wantUpdated:    true,
			wantRequeueAt:  ptr.To(metav1.NewTime(evenMoreFutureTime)),
			wantCount:      ptr.To[int32](4),
		},
		"should increment count when requeue time is same": {
			workload: &kueue.Workload{
				Status: kueue.WorkloadStatus{
					RequeueState: &kueue.RequeueState{
						RequeueAt: ptr.To(metav1.NewTime(futureTime)),
						Count:     ptr.To[int32](1),
					},
				},
			},
			waitUntil:      metav1.NewTime(futureTime),
			incrementCount: true,
			wantUpdated:    true,
			wantRequeueAt:  ptr.To(metav1.NewTime(futureTime)),
			wantCount:      ptr.To[int32](2),
		},
		"should increment from zero count": {
			workload: &kueue.Workload{
				Status: kueue.WorkloadStatus{
					RequeueState: &kueue.RequeueState{
						RequeueAt: ptr.To(metav1.NewTime(pastTime)),
						Count:     ptr.To[int32](0),
					},
				},
			},
			waitUntil:      metav1.NewTime(futureTime),
			incrementCount: true,
			wantUpdated:    true,
			wantRequeueAt:  ptr.To(metav1.NewTime(futureTime)),
			wantCount:      ptr.To[int32](1),
		},
		"should handle zero time in requeue state": {
			workload: &kueue.Workload{
				Status: kueue.WorkloadStatus{
					RequeueState: &kueue.RequeueState{
						RequeueAt: ptr.To(metav1.NewTime(time.Time{})),
						Count:     ptr.To[int32](0),
					},
				},
			},
			waitUntil:      metav1.NewTime(futureTime),
			incrementCount: true,
			wantUpdated:    true,
			wantRequeueAt:  ptr.To(metav1.NewTime(futureTime)),
			wantCount:      ptr.To[int32](1),
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			gotUpdated := SetRequeueState(tc.workload, tc.waitUntil, tc.incrementCount)

			if gotUpdated != tc.wantUpdated {
				t.Errorf("SetRequeueState() returned %v, want %v", gotUpdated, tc.wantUpdated)
			}

			if tc.workload.Status.RequeueState == nil {
				t.Fatal("RequeueState should not be nil after SetRequeueState")
			}

			if diff := cmp.Diff(tc.wantRequeueAt, tc.workload.Status.RequeueState.RequeueAt); diff != "" {
				t.Errorf("Unexpected RequeueAt (-want +got):\n%s", diff)
			}

			if diff := cmp.Diff(tc.wantCount, tc.workload.Status.RequeueState.Count); diff != "" {
				t.Errorf("Unexpected Count (-want +got):\n%s", diff)
			}
		})
	}
}
