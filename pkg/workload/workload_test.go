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

package workload

import (
	"context"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

func TestNewInfo(t *testing.T) {
	cases := map[string]struct {
		workload     kueue.Workload
		podTemplates []corev1.PodTemplate
		wantInfo     Info
	}{
		"pending": {
			workload: *utiltesting.MakeWorkload("", "").
				PodSets(*utiltesting.MakePodSet("main", 1).
					SetPodTemplateName("main").
					Obj(),
				).
				Obj(),
			podTemplates: []corev1.PodTemplate{
				*utiltesting.MakePodTemplate("main", "").
					Request(corev1.ResourceCPU, "10m").
					Request(corev1.ResourceMemory, "512Ki").
					Obj(),
			},
			wantInfo: Info{
				TotalRequests: []PodSetResources{
					{
						Name: "main",
						Requests: Requests{
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
					*utiltesting.MakePodSet("main", 5).
						SetPodTemplateName("main").
						Obj(),
				).
				ReclaimablePods(
					kueue.ReclaimablePod{
						Name:  "main",
						Count: 2,
					},
				).
				Obj(),
			podTemplates: []corev1.PodTemplate{
				*utiltesting.MakePodTemplate("main", "").
					Request(corev1.ResourceCPU, "10m").
					Request(corev1.ResourceMemory, "512Ki").
					Obj(),
			},
			wantInfo: Info{
				TotalRequests: []PodSetResources{
					{
						Name: "main",
						Requests: Requests{
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
			podTemplates: []corev1.PodTemplate{
				*utiltesting.MakePodTemplate("driver", "").
					Request(corev1.ResourceCPU, "10m").
					Request(corev1.ResourceMemory, "512Ki").
					Obj(),
				*utiltesting.MakePodTemplate("workers", "").
					Request(corev1.ResourceCPU, "5m").
					Request(corev1.ResourceMemory, "1Mi").
					Request("ex.com/gpu", "1").
					Obj(),
			},
			wantInfo: Info{
				ClusterQueue: "foo",
				TotalRequests: []PodSetResources{
					{
						Name: "driver",
						Requests: Requests{
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
						Requests: Requests{
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
					*utiltesting.MakePodSet("main", 5).
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
						Name:  "main",
						Count: 2,
					},
				).
				Obj(),
			podTemplates: []corev1.PodTemplate{
				*utiltesting.MakePodTemplate("main", "").
					Request(corev1.ResourceCPU, "10m").
					Request(corev1.ResourceMemory, "10Ki").
					Obj(),
			},
			wantInfo: Info{
				TotalRequests: []PodSetResources{
					{
						Name: "main",
						Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
							corev1.ResourceCPU:    "f1",
							corev1.ResourceMemory: "f1",
						},
						Requests: Requests{
							corev1.ResourceCPU:    3 * 10,
							corev1.ResourceMemory: 3 * 10 * 1024,
						},
						Count: 3,
					},
				},
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			info := NewInfo(&tc.workload, tc.podTemplates)
			if diff := cmp.Diff(info, &tc.wantInfo, cmpopts.IgnoreFields(Info{}, "Obj", "PodTemplates")); diff != "" {
				t.Errorf("NewInfo(_) = (-want,+got):\n%s", diff)
			}
		})
	}
}

var ignoreConditionTimestamps = cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime")

func TestUpdateWorkloadStatus(t *testing.T) {
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
						Type:    kueue.WorkloadQuotaReserved,
						Status:  metav1.ConditionFalse,
						Reason:  "Pending",
						Message: "didn't fit",
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
						Type:   kueue.WorkloadQuotaReserved,
						Status: metav1.ConditionTrue,
						Reason: "Admitted",
					},
				},
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			workload := utiltesting.MakeWorkload("foo", "bar").Obj()
			workload.Status = tc.oldStatus
			cl := utiltesting.NewFakeClient(workload)
			ctx := context.Background()
			err := UpdateStatus(ctx, cl, workload, tc.condType, tc.condStatus, tc.reason, tc.message, "manager-perfix")
			if err != nil {
				t.Fatalf("Failed updating status: %v", err)
			}
			var updatedWl kueue.Workload
			if err := cl.Get(ctx, client.ObjectKeyFromObject(workload), &updatedWl); err != nil {
				t.Fatalf("Failed obtaining updated object: %v", err)
			}
			if diff := cmp.Diff(tc.wantStatus, updatedWl.Status, ignoreConditionTimestamps); diff != "" {
				t.Errorf("Unexpected status after updating (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestGetQueueOrderTimestamp(t *testing.T) {
	creationTime := metav1.Now()
	conditionTime := metav1.NewTime(time.Now().Add(time.Hour))
	cases := map[string]struct {
		wl   *kueue.Workload
		want metav1.Time
	}{
		"no condition": {
			wl: utiltesting.MakeWorkload("name", "ns").
				Creation(creationTime.Time).
				Obj(),
			want: creationTime,
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
			want: creationTime,
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
			want: conditionTime,
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
			want: creationTime,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			gotTime := GetQueueOrderTimestamp(tc.wl)
			if diff := cmp.Diff(*gotTime, tc.want); diff != "" {
				t.Errorf("Unexpected time (-want,+got):\n%s", diff)
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
		"one value missmatch": {
			a:          []kueue.ReclaimablePod{{Name: "rp1", Count: 1}, {Name: "rp2", Count: 2}},
			b:          []kueue.ReclaimablePod{{Name: "rp2", Count: 1}, {Name: "rp1", Count: 1}},
			wantResult: false,
		},
		"one name missmatch": {
			a:          []kueue.ReclaimablePod{{Name: "rp1", Count: 1}, {Name: "rp2", Count: 2}},
			b:          []kueue.ReclaimablePod{{Name: "rp3", Count: 3}, {Name: "rp1", Count: 1}},
			wantResult: false,
		},
		"length missmatch": {
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
