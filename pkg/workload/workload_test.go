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
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/component-base/featuregate"
	testingclock "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	queueafs "sigs.k8s.io/kueue/pkg/cache/queue/afs"
	controllerconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
	utilqueue "sigs.k8s.io/kueue/pkg/util/queue"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

var (
	errTest = errors.New("test error")
)

func TestFromQuotaReservedOrAdmittedToPending(t *testing.T) {
	cases := map[string]struct {
		prev, new string
		want      bool
	}{
		"quotaReserved to pending":  {StatusQuotaReserved, StatusPending, true},
		"admitted to pending":       {StatusAdmitted, StatusPending, true},
		"pending to pending":        {StatusPending, StatusPending, false},
		"pending to quotaReserved":  {StatusPending, StatusQuotaReserved, false},
		"pending to admitted":       {StatusPending, StatusAdmitted, false},
		"quotaReserved to admitted": {StatusQuotaReserved, StatusAdmitted, false},
		"admitted to quotaReserved": {StatusAdmitted, StatusQuotaReserved, false},
		"finished to pending":       {StatusFinished, StatusPending, false},
		"quotaReserved to finished": {StatusQuotaReserved, StatusFinished, false},
		"admitted to finished":      {StatusAdmitted, StatusFinished, false},
		"same quotaReserved":        {StatusQuotaReserved, StatusQuotaReserved, false},
		"same admitted":             {StatusAdmitted, StatusAdmitted, false},
		"pending to finished":       {StatusPending, StatusFinished, false},
		"finished to quotaReserved": {StatusFinished, StatusQuotaReserved, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := FromQuotaReservedOrAdmittedToPending(tc.prev, tc.new); got != tc.want {
				t.Errorf("FromQuotaReservedOrAdmittedToPending(%q, %q) = %v, want %v", tc.prev, tc.new, got, tc.want)
			}
		})
	}
}

func TestIsOnHold(t *testing.T) {
	cases := map[string]struct {
		wl   kueue.Workload
		want bool
	}{
		"false without QuotaReserved condition": {
			wl:   *utiltestingapi.MakeWorkload("wl", "ns").Obj(),
			want: false,
		},
		"true with QuotaReserved=False and reason OnHold": {
			wl: *utiltestingapi.MakeWorkload("wl", "ns").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadOnHold,
					Message: "StatefulSet scaled to zero; workload on hold",
				}).
				Obj(),
			want: true,
		},
		"false when QuotaReserved=False with other reason": {
			wl: *utiltestingapi.MakeWorkload("wl", "ns").
				Condition(metav1.Condition{
					Type:   kueue.WorkloadQuotaReserved,
					Status: metav1.ConditionFalse,
					Reason: kueue.WorkloadInadmissible,
				}).
				Obj(),
			want: false,
		},
		"false when QuotaReserved=True": {
			wl: *utiltestingapi.MakeWorkload("wl", "ns").
				Condition(metav1.Condition{
					Type:   kueue.WorkloadQuotaReserved,
					Status: metav1.ConditionTrue,
					Reason: kueue.WorkloadOnHold,
				}).
				Obj(),
			want: false,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := IsOnHold(&tc.wl); got != tc.want {
				t.Fatalf("IsOnHold() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestNewInfo(t *testing.T) {
	now := time.Now().Truncate(time.Second)
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
						Requests: resources.MapRequests{
							corev1.ResourceCPU:    10,
							corev1.ResourceMemory: 512 * 1024,
						},
						Count: 1,
					},
				},
			},
		},
		"negative request floored to zero in total requests": {
			workload: *utiltestingapi.MakeWorkload("", "").
				PodSets(
					*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).
						Request(corev1.ResourceCPU, "-10m").
						Request(corev1.ResourceMemory, "512Ki").
						Obj(),
				).
				Obj(),
			wantInfo: Info{
				TotalRequests: []PodSetResources{
					{
						Name: kueue.DefaultPodSetName,
						Requests: resources.MapRequests{
							corev1.ResourceCPU:    0,
							corev1.ResourceMemory: 2 * 512 * 1024,
						},
						Count: 2,
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
						Requests: resources.MapRequests{
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
						Requests: resources.MapRequests{
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
		"prevent int overflow in total requests": {
			workload: *utiltestingapi.MakeWorkload("test-wl", "default").
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2147483647).
					Request(corev1.ResourceCPU, "4300000").
					Obj()).
				Obj(),
			wantInfo: Info{
				TotalRequests: []PodSetResources{
					{
						Name:  kueue.DefaultPodSetName,
						Count: 2147483647,
						Requests: resources.MapRequests{
							corev1.ResourceCPU: 9223372036854775807,
						},
					},
				},
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
				ReserveQuotaAt(utiltestingapi.MakeAdmission("foo").
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
					Obj(), now).
				Obj(),
			wantInfo: Info{
				ClusterQueue: "foo",
				TotalRequests: []PodSetResources{
					{
						Name: "driver",
						Requests: resources.MapRequests{
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
						Requests: resources.MapRequests{
							corev1.ResourceCPU:    15,
							corev1.ResourceMemory: 3 * 1024 * 1024,
							"ex.com/gpu":          3,
						},
						Count: 3,
					},
				},
			},
		},
		"admitted with TAS and transformed resources": {
			workload: *utiltestingapi.MakeWorkload("tas", "").
				PodSets(
					*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).
						// The admitted usage below transforms example.com/gpu to
						// example.com/logical-gpu and excludes networking.example.com/vpc.
						Request(corev1.ResourceCPU, "1").
						Request(corev1.ResourceMemory, "1Gi").
						Request("example.com/gpu", "1").
						Request("networking.example.com/vpc", "1").
						RequiredTopologyRequest(corev1.LabelHostname).
						Obj(),
				).
				ReserveQuotaAt(
					utiltestingapi.MakeAdmission("tas-cq").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "tas", "2").
							Assignment(corev1.ResourceMemory, "tas", "2Gi").
							Assignment("example.com/logical-gpu", "quota", "2").
							Count(2).
							TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(utiltestingapi.MakeDefaultOneLevelTopology("default"))).
								Domains(utiltestingapi.MakeTopologyDomainAssignment([]string{"node-a"}, 2).Obj()).
								Obj()).
							Obj()).
						Obj(), now,
				).
				Obj(),
			wantInfo: Info{
				ClusterQueue: "tas-cq",
				TotalRequests: []PodSetResources{
					{
						Name: kueue.DefaultPodSetName,
						Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
							corev1.ResourceCPU:        "tas",
							corev1.ResourceMemory:     "tas",
							"example.com/logical-gpu": "quota",
						},
						Requests: resources.MapRequests{
							corev1.ResourceCPU:        2000,
							corev1.ResourceMemory:     2 * 1024 * 1024 * 1024,
							"example.com/logical-gpu": 2,
						},
						Count: 2,
						TopologyRequest: &TopologyRequest{
							Levels: []string{corev1.LabelHostname},
							DomainRequests: []TopologyDomainRequests{{
								Values: []string{"node-a"},
								SinglePodRequests: resources.NewRequestsFromMap(resources.MapRequests{
									corev1.ResourceCPU:           1000,
									corev1.ResourceMemory:        1024 * 1024 * 1024,
									"example.com/gpu":            1,
									"networking.example.com/vpc": 1,
								}),
								Count: 2,
							}},
						},
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
				ReserveQuotaAt(
					utiltestingapi.MakeAdmission("").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "f1", "30m").
							Assignment(corev1.ResourceMemory, "f1", "30Ki").
							Count(3).
							Obj()).
						Obj(), now,
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
						Requests: resources.MapRequests{
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
				ReserveQuotaAt(
					utiltestingapi.MakeAdmission("").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "f1", "50m").
							Assignment(corev1.ResourceMemory, "f1", "50Ki").
							Count(5).
							Obj()).
						Obj(), now,
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
						Requests: resources.MapRequests{
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
				ReserveQuotaAt(
					utiltestingapi.MakeAdmission("").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "f1", "30m").
							Assignment(corev1.ResourceMemory, "f1", "30Ki").
							Count(3).
							Obj()).
						Obj(), now,
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
						Requests: resources.MapRequests{
							corev1.ResourceCPU:    2 * 10,
							corev1.ResourceMemory: 2 * 10 * 1024,
						},
						Count: 2,
					},
				},
			},
		},
		"admitted with stale reclaim exceeding scaled-down podSet count": {
			workload: *utiltestingapi.MakeWorkload("", "").
				PodSets(
					*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).
						Request(corev1.ResourceCPU, "10m").
						Request(corev1.ResourceMemory, "10Ki").
						Obj(),
				).
				ReserveQuotaAt(
					utiltestingapi.MakeAdmission("").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "f1", "50m").
							Assignment(corev1.ResourceMemory, "f1", "50Ki").
							Count(5).
							Obj()).
						Obj(), now,
				).
				ReclaimablePods(
					kueue.ReclaimablePod{
						Name:  kueue.DefaultPodSetName,
						Count: 5,
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
						Requests: resources.MapRequests{
							corev1.ResourceCPU:    0,
							corev1.ResourceMemory: 0,
						},
						Count: 0,
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
				ReserveQuotaAt(
					utiltestingapi.MakeAdmission("").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "f1", "30m").
							Assignment(corev1.ResourceMemory, "f1", "30Ki").
							Count(3).
							Obj()).
						Obj(), now,
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
						Requests: resources.MapRequests{
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
						Requests: resources.MapRequests{
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
					*utiltestingapi.MakePodSet("c", 1).
						Request("nvidia.com/vgpu", "2").
						Request("nvidia.com/vgpucores", "20").
						Request("nvidia.com/vgpumem", "1024").
						Obj(),
					*utiltestingapi.MakePodSet("d", 2).
						Request("nvidia.com/vgpu", "2").
						Request("nvidia.com/vgpucores", "30").
						Request("nvidia.com/vgpumem", "2048").
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
				{
					Input:      "nvidia.com/vgpucores",
					Strategy:   ptr.To(config.Replace),
					MultiplyBy: "nvidia.com/vgpu",
					Outputs: corev1.ResourceList{
						"nvidia.com/total-vgpucores": resource.MustParse("1"),
					},
				},
				{
					Input:      "nvidia.com/vgpumem",
					Strategy:   ptr.To(config.Replace),
					MultiplyBy: "nvidia.com/vgpu",
					Outputs: corev1.ResourceList{
						"nvidia.com/total-vgpumem": resource.MustParse("1"),
					},
				},
			})},
			wantInfo: Info{
				TotalRequests: []PodSetResources{
					{
						Name: "a",
						Requests: resources.MapRequests{
							corev1.ResourceCPU: 1000,
							corev1.ResourceName("example.com/accelerator-memory"): 20 * 1024,
							corev1.ResourceName("example.com/credits"):            35,
						},
						Count: 1,
					},
					{
						Name: "b",
						Requests: resources.MapRequests{
							corev1.ResourceCPU: 4 * 1000,
							corev1.ResourceName("example.com/accelerator-memory"): 80 * 1024,
							corev1.ResourceName("example.com/credits"):            200,
							corev1.ResourceName("nvidia.com/gpu"):                 2,
						},
						Count: 2,
					},
					{
						Name: "c",
						Requests: resources.MapRequests{
							corev1.ResourceName("nvidia.com/vgpu"):            2,
							corev1.ResourceName("nvidia.com/total-vgpucores"): 2 * 20,
							corev1.ResourceName("nvidia.com/total-vgpumem"):   2 * 1024,
						},
						Count: 1,
					},
					{
						Name: "d",
						Requests: resources.MapRequests{
							corev1.ResourceName("nvidia.com/vgpu"):            2 * 2,
							corev1.ResourceName("nvidia.com/total-vgpucores"): 2 * 2 * 30,
							corev1.ResourceName("nvidia.com/total-vgpumem"):   2 * 2 * 2048,
						},
						Count: 2,
					},
				},
			},
		},
		"transformMilliValues": {
			workload: *utiltestingapi.MakeWorkload("transform", "").
				PodSets(
					*utiltestingapi.MakePodSet("", 1).
						Request(corev1.ResourceCPU, "100m").
						Request(corev1.ResourceMemory, "100M").
						Obj(),
				).
				Obj(),
			infoOptions: []InfoOption{WithResourceTransformations([]config.ResourceTransformation{
				{
					Input:    corev1.ResourceCPU,
					Strategy: ptr.To(config.Replace),
					Outputs: corev1.ResourceList{
						"example.com/cpu-credits": resource.MustParse("3000"),
					},
				},
				{
					Input:    corev1.ResourceMemory,
					Strategy: ptr.To(config.Replace),
					Outputs: corev1.ResourceList{
						"example.com/memory-credits": resource.MustParse("3m"),
					},
				},
			})},
			wantInfo: Info{
				TotalRequests: []PodSetResources{
					{
						Name: "",
						Requests: resources.MapRequests{
							// 100m * 3000 = 300
							corev1.ResourceName("example.com/cpu-credits"): 300,
							// 100M * 3m = 300k
							corev1.ResourceName("example.com/memory-credits"): 300 * 1000,
						},
						Count: 1,
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
			if diff := cmp.Diff(info, &tc.wantInfo, cmpopts.IgnoreFields(Info{}, "Obj", "SchedulingHash"),
				cmp.Transformer("requestsToMap", resources.ToMapRequests)); diff != "" {
				t.Errorf("NewInfo(_) = (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestUpdateWithRebuild(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	cases := map[string]struct {
		initial        *kueue.Workload
		initialOptions []InfoOption
		updated        *kueue.Workload
		updateOptions  []InfoOption
		wantRequests   []PodSetResources
	}{
		"pending workload with changed requests": {
			initial: utiltestingapi.MakeWorkload("wl", "ns").
				Request(corev1.ResourceCPU, "100m").Obj(),
			updated: utiltestingapi.MakeWorkload("wl", "ns").
				Request(corev1.ResourceCPU, "200m").Obj(),
			wantRequests: []PodSetResources{{
				Name:     kueue.DefaultPodSetName,
				Requests: resources.MapRequests{corev1.ResourceCPU: 200},
				Count:    1,
			}},
		},
		"stale DRA TotalRequests cleared on rebuild": {
			initial: utiltestingapi.MakeWorkload("wl", "ns").
				Request("example.com/gpu", "1").Obj(),
			initialOptions: []InfoOption{
				WithPreprocessedDRAResources(
					map[kueue.PodSetReference]corev1.ResourceList{
						kueue.DefaultPodSetName: {
							"gpu": resource.MustParse("1"),
						},
					},
					map[kueue.PodSetReference]sets.Set[corev1.ResourceName]{
						kueue.DefaultPodSetName: sets.New[corev1.ResourceName]("example.com/gpu"),
					},
				),
			},
			updated: utiltestingapi.MakeWorkload("wl", "ns").
				Request("example.com/gpu", "1").Obj(),
			wantRequests: []PodSetResources{{
				Name:     kueue.DefaultPodSetName,
				Requests: resources.MapRequests{"example.com/gpu": 1},
				Count:    1,
			}},
		},
		"admitted workload recomputes from admission": {
			initial: utiltestingapi.MakeWorkload("wl", "ns").
				Request(corev1.ResourceCPU, "100m").Obj(),
			updated: utiltestingapi.MakeWorkload("wl", "ns").
				Request(corev1.ResourceCPU, "100m").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
					PodSets(kueue.PodSetAssignment{
						Name:          kueue.DefaultPodSetName,
						ResourceUsage: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("200m")},
						Count:         ptr.To[int32](1),
					}).Obj(), now).
				Obj(),
			wantRequests: []PodSetResources{{
				Name:     kueue.DefaultPodSetName,
				Requests: resources.MapRequests{corev1.ResourceCPU: 200},
				Count:    1,
			}},
		},
		"WithPreserveTotalRequests keeps DRA-translated requests": {
			initial: utiltestingapi.MakeWorkload("wl", "ns").
				Request("example.com/gpu", "1").Obj(),
			initialOptions: []InfoOption{
				WithPreprocessedDRAResources(
					map[kueue.PodSetReference]corev1.ResourceList{
						kueue.DefaultPodSetName: {"gpu": resource.MustParse("1")},
					},
					map[kueue.PodSetReference]sets.Set[corev1.ResourceName]{
						kueue.DefaultPodSetName: sets.New[corev1.ResourceName]("example.com/gpu"),
					},
				),
			},
			updated: utiltestingapi.MakeWorkload("wl", "ns").
				Request("example.com/gpu", "1").Obj(),
			updateOptions: []InfoOption{WithPreserveTotalRequests()},
			wantRequests: []PodSetResources{{
				Name:     kueue.DefaultPodSetName,
				Requests: resources.MapRequests{"gpu": 1},
				Count:    1,
			}},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			info := NewInfo(tc.initial, tc.initialOptions...)
			if len(tc.updateOptions) == 0 {
				if diff := cmp.Diff(tc.wantRequests, info.TotalRequests, cmpopts.IgnoreFields(PodSetResources{}, "Flavors")); diff == "" {
					t.Fatal("precondition failed: initial TotalRequests should differ from expected post-rebuild state")
				}
			}
			info.Update(logr.Discard(), tc.updated, tc.updateOptions...)
			if diff := cmp.Diff(tc.wantRequests, info.TotalRequests,
				cmpopts.IgnoreFields(PodSetResources{}, "Flavors")); diff != "" {
				t.Errorf("TotalRequests after Update (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestSetConditionAndUpdate(t *testing.T) {
	now := time.Now()
	fakeClock := testingclock.NewFakeClock(now)
	cases := map[string]struct {
		oldStatus  kueue.WorkloadStatus
		condType   string
		condStatus metav1.ConditionStatus
		reason     string
		message    string
		err        error
		wantStatus kueue.WorkloadStatus
		wantErr    error
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
		"initial empty with error": {
			condType:   kueue.WorkloadQuotaReserved,
			condStatus: metav1.ConditionFalse,
			reason:     "Pending",
			message:    "didn't fit",
			err:        errTest,
			wantStatus: kueue.WorkloadStatus{},
			wantErr:    errTest,
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
		for _, useMergePatch := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s with WorkloadRequestUseMergePatch enabled: %t", name, useMergePatch), func(t *testing.T) {
				features.SetFeatureGateDuringTest(t, features.WorkloadRequestUseMergePatch, useMergePatch)

				ctx, _ := utiltesting.ContextWithLog(t)

				workload := utiltestingapi.MakeWorkload("foo", "bar").Generation(1).Obj()
				workload.Status = tc.oldStatus

				cl := utiltesting.NewClientBuilder().
					WithObjects(workload).
					WithStatusSubresource(&kueue.Workload{}).
					WithInterceptorFuncs(interceptor.Funcs{
						SubResourcePatch: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
							if tc.err != nil {
								return tc.err
							}
							return utiltesting.TreatSSAAsStrategicMerge(ctx, c, subResourceName, obj, patch, opts...)
						},
					}).
					Build()

				err := SetConditionAndUpdate(ctx, cl, workload, tc.condType, tc.condStatus, tc.reason, tc.message, "manager-prefix", fakeClock)
				if diff := cmp.Diff(tc.wantErr, err, cmpopts.EquateErrors()); diff != "" {
					t.Errorf("Unexpected error (-want,+got):\n%s", diff)
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
}

func TestUpdateReclaimablePods(t *testing.T) {
	cases := map[string]struct {
		oldStatus       kueue.WorkloadStatus
		reclaimablePods []kueue.ReclaimablePod
		err             error
		wantStatus      kueue.WorkloadStatus
		wantErr         error
	}{
		"set reclaimable pods": {
			reclaimablePods: []kueue.ReclaimablePod{{Name: "ps1", Count: 1}},
			wantStatus: kueue.WorkloadStatus{
				ReclaimablePods: []kueue.ReclaimablePod{{Name: "ps1", Count: 1}},
			},
		},
		"set reclaimable pods with error": {
			err:             errTest,
			reclaimablePods: []kueue.ReclaimablePod{{Name: "ps1", Count: 1}},
			wantStatus:      kueue.WorkloadStatus{},
			wantErr:         errTest,
		},
		"update reclaimable pods": {
			oldStatus: kueue.WorkloadStatus{
				ReclaimablePods: []kueue.ReclaimablePod{{Name: "ps1", Count: 1}},
			},
			reclaimablePods: []kueue.ReclaimablePod{{Name: "ps1", Count: 2}},
			wantStatus: kueue.WorkloadStatus{
				ReclaimablePods: []kueue.ReclaimablePod{{Name: "ps1", Count: 2}},
			},
		},
	}
	for name, tc := range cases {
		for _, useMergePatch := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s with WorkloadRequestUseMergePatch enabled %t", name, useMergePatch), func(t *testing.T) {
				features.SetFeatureGateDuringTest(t, features.WorkloadRequestUseMergePatch, useMergePatch)

				ctx, _ := utiltesting.ContextWithLog(t)

				workload := utiltestingapi.MakeWorkload("foo", metav1.NamespaceDefault).Obj()
				workload.Status = tc.oldStatus

				cl := utiltesting.NewClientBuilder().
					WithObjects(workload).
					WithStatusSubresource(&kueue.Workload{}).
					WithInterceptorFuncs(interceptor.Funcs{
						SubResourcePatch: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
							if tc.err != nil {
								return tc.err
							}
							return utiltesting.TreatSSAAsStrategicMerge(ctx, c, subResourceName, obj, patch, opts...)
						},
					}).
					Build()

				err := UpdateReclaimablePods(ctx, cl, workload, tc.reclaimablePods)
				if diff := cmp.Diff(tc.wantErr, err, cmpopts.EquateErrors()); diff != "" {
					t.Errorf("Unexpected error (-want,+got):\n%s", diff)
				}

				var updatedWl kueue.Workload
				if err := cl.Get(ctx, client.ObjectKeyFromObject(workload), &updatedWl); err != nil {
					t.Fatalf("Failed obtaining updated object: %v", err)
				}

				if diff := cmp.Diff(tc.wantStatus, updatedWl.Status); diff != "" {
					t.Errorf("Unexpected status after updating (-want,+got):\n%s", diff)
				}
			})
		}
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

func TestLimitReclaimablePodsToPodSetSizes(t *testing.T) {
	wl := utiltestingapi.MakeWorkload("wl", "ns").
		PodSets(
			*utiltestingapi.MakePodSet("ps1", 3).Obj(),
			*utiltestingapi.MakePodSet("ps2", 5).Obj(),
		).
		Obj()
	cases := map[string]struct {
		reclaimablePods []kueue.ReclaimablePod
		want            []kueue.ReclaimablePod
	}{
		"empty": {},
		"within podSet sizes": {
			reclaimablePods: []kueue.ReclaimablePod{{Name: "ps1", Count: 3}, {Name: "ps2", Count: 1}},
			want:            []kueue.ReclaimablePod{{Name: "ps1", Count: 3}, {Name: "ps2", Count: 1}},
		},
		"count exceeding its podSet size is lowered": {
			reclaimablePods: []kueue.ReclaimablePod{{Name: "ps1", Count: 4}, {Name: "ps2", Count: 6}},
			want:            []kueue.ReclaimablePod{{Name: "ps1", Count: 3}, {Name: "ps2", Count: 5}},
		},
		"unknown podSet is left as is": {
			reclaimablePods: []kueue.ReclaimablePod{{Name: "ps3", Count: 10}},
			want:            []kueue.ReclaimablePod{{Name: "ps3", Count: 10}},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := LimitReclaimablePodsToPodSetSizes(wl, tc.reclaimablePods)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("Unexpected reclaimable pods (-want,+got):\n%s", diff)
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
					Requests: resources.MapRequests{
						corev1.ResourceCPU: 1_000,
						"example.com/gpu":  3,
					},
				}},
			},
			want: resources.FlavorResourceQuantities{
				{Flavor: "", Resource: "cpu"}:             resources.NewAmount(1_000),
				{Flavor: "", Resource: "example.com/gpu"}: resources.NewAmount(3),
			},
		},
		"one podset, multiple flavors": {
			info: &Info{
				TotalRequests: []PodSetResources{{
					Requests: resources.MapRequests{
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
				{Flavor: "default", Resource: "cpu"}:         resources.NewAmount(1_000),
				{Flavor: "gpu", Resource: "example.com/gpu"}: resources.NewAmount(3),
			},
		},
		"multiple podsets, multiple flavors": {
			info: &Info{
				TotalRequests: []PodSetResources{
					{
						Requests: resources.MapRequests{
							corev1.ResourceCPU: 1_000,
							"example.com/gpu":  3,
						},
						Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
							corev1.ResourceCPU: "default",
							"example.com/gpu":  "model_a",
						},
					},
					{
						Requests: resources.MapRequests{
							corev1.ResourceCPU:    2_000,
							corev1.ResourceMemory: 2 * utiltesting.Gi,
						},
						Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
							corev1.ResourceCPU:    "default",
							corev1.ResourceMemory: "default",
						},
					},
					{
						Requests: resources.MapRequests{
							"example.com/gpu": 1,
						},
						Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
							"example.com/gpu": "model_b",
						},
					},
				},
			},
			want: resources.FlavorResourceQuantities{
				{Flavor: "default", Resource: "cpu"}:             resources.NewAmount(3_000),
				{Flavor: "default", Resource: "memory"}:          resources.NewAmount(2 * utiltesting.Gi),
				{Flavor: "model_a", Resource: "example.com/gpu"}: resources.NewAmount(3),
				{Flavor: "model_b", Resource: "example.com/gpu"}: resources.NewAmount(1),
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

func TestFilterChecksForAdmission(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	cases := map[string]struct {
		cq                  *kueue.ClusterQueue
		wl                  *kueue.Workload
		wantAdmissionChecks sets.Set[kueue.AdmissionCheckReference]
	}{
		"AdmissionCheckStrategy with a flavor": {
			wl: utiltestingapi.MakeWorkload("wl", "ns").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment("cpu", "flavor1", "1").
						Obj()).
					Obj(), now).
				Obj(),
			cq: utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("flavor1").Obj(), *utiltestingapi.MakeFlavorQuotas("flavor2").Obj()).
				AdmissionCheckStrategy(*utiltestingapi.MakeAdmissionCheckStrategyRule("ac1", "flavor1").Obj()).
				Obj(),
			wantAdmissionChecks: sets.New[kueue.AdmissionCheckReference]("ac1"),
		},
		"AdmissionCheckStrategy with an unmatched flavor": {
			wl: utiltestingapi.MakeWorkload("wl", "ns").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment("cpu", "flavor1", "1").
						Obj()).
					Obj(), now).
				Obj(),
			cq: utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("flavor1").Obj(), *utiltestingapi.MakeFlavorQuotas("flavor2").Obj()).
				AdmissionCheckStrategy(*utiltestingapi.MakeAdmissionCheckStrategyRule("ac1", "unmatched-flavor").Obj()).
				Obj(),
			wantAdmissionChecks: nil,
		},
		"AdmissionCheckStrategy without a flavor": {
			wl: utiltestingapi.MakeWorkload("wl", "ns").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment("cpu", "flavor1", "1").
						Obj()).
					Obj(), now).
				Obj(),
			cq: utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("flavor1").Obj(), *utiltestingapi.MakeFlavorQuotas("flavor2").Obj()).
				AdmissionCheckStrategy(*utiltestingapi.MakeAdmissionCheckStrategyRule("ac1").Obj()).
				Obj(),
			wantAdmissionChecks: sets.New[kueue.AdmissionCheckReference]("ac1"),
		},
		"Two AdmissionCheckStrategies, one with flavor, one without flavor": {
			wl: utiltestingapi.MakeWorkload("wl", "ns").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment("cpu", "flavor1", "1").
						Obj()).
					Obj(), now).
				Obj(),
			cq: utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("flavor1").Obj(), *utiltestingapi.MakeFlavorQuotas("flavor2").Obj()).
				AdmissionCheckStrategy(
					*utiltestingapi.MakeAdmissionCheckStrategyRule("ac1", "flavor1").Obj(),
					*utiltestingapi.MakeAdmissionCheckStrategyRule("ac2").Obj()).
				Obj(),
			wantAdmissionChecks: sets.New[kueue.AdmissionCheckReference]("ac1", "ac2"),
		},
		"AdmissionCheckStrategy with only a non-existent flavor": {
			wl: utiltestingapi.MakeWorkload("wl", "ns").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment("cpu", "flavor1", "1").
						Obj()).
					Obj(), now).
				Obj(),
			cq: utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("flavor1").Obj()).
				AdmissionCheckStrategy(
					*utiltestingapi.MakeAdmissionCheckStrategyRule("ac1", "flavor-nonexistent").Obj()).
				Obj(),
			wantAdmissionChecks: sets.New[kueue.AdmissionCheckReference](),
		},
		"AdmissionCheckStrategy with an additional non-existent flavor": {
			wl: utiltestingapi.MakeWorkload("wl", "ns").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment("cpu", "flavor1", "1").
						Obj()).
					Obj(), now).
				Obj(),
			cq: utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("flavor1").Obj()).
				AdmissionCheckStrategy(
					*utiltestingapi.MakeAdmissionCheckStrategyRule("ac1", "flavor1", "flavor-nonexistent").Obj()).
				Obj(),
			wantAdmissionChecks: sets.New[kueue.AdmissionCheckReference]("ac1"),
		},
		"Two AdmissionCheckStrategies, one covering one flavor, one covering another": {
			wl: utiltestingapi.MakeWorkload("wl", "ns").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment("cpu", "flavor1", "1").
						Assignment("memory", "flavor2", "1").
						Obj()).
					Obj(), now).
				Obj(),
			cq: utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("flavor1").Obj(), *utiltestingapi.MakeFlavorQuotas("flavor2").Obj()).
				AdmissionCheckStrategy(
					*utiltestingapi.MakeAdmissionCheckStrategyRule("ac1", "flavor1").Obj(),
					*utiltestingapi.MakeAdmissionCheckStrategyRule("ac2", "flavor2").Obj(),
				).
				Obj(),
			wantAdmissionChecks: sets.New[kueue.AdmissionCheckReference]("ac1", "ac2"),
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, log := utiltesting.ContextWithLog(t)
			gotAdmissionChecks := admissionChecksForAdmission(log, admissioncheck.NewAdmissionChecks(tc.cq), *tc.wl.Status.Admission)
			if diff := cmp.Diff(tc.wantAdmissionChecks, gotAdmissionChecks); diff != "" {
				t.Errorf("Unexpected AdmissionChecks, (want-/got+):\n%s", diff)
			}
		})
	}
}

func TestAdmissionChecksForWorkload(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	cases := map[string]struct {
		wl                  *kueue.Workload
		wantAdmissionChecks sets.Set[kueue.AdmissionCheckReference]
	}{
		"Only relevant checks returned for an admitted workload": {
			wl: utiltestingapi.MakeWorkload("wl", "ns").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment("cpu", "flavor1", "1").
						Assignment("memory", "flavor2", "1").
						Obj()).
					Obj(), now).
				Obj(),
			wantAdmissionChecks: sets.New[kueue.AdmissionCheckReference]("ac1", "ac2", "ac3", "ac4", "ac6"),
		},
		"Only correct checks covering all relevant flavors returned for Workload without Quota Reserved ": {
			wl: utiltestingapi.MakeWorkload("wl", "ns").
				Obj(),
			wantAdmissionChecks: sets.New[kueue.AdmissionCheckReference]("ac3", "ac4", "ac6"),
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, log := utiltesting.ContextWithLog(t)
			cq := utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("flavor1").Obj(), *utiltestingapi.MakeFlavorQuotas("flavor2").Obj()).
				AdmissionCheckStrategy(
					*utiltestingapi.MakeAdmissionCheckStrategyRule("ac1", "flavor1").Obj(),
					*utiltestingapi.MakeAdmissionCheckStrategyRule("ac2", "flavor2").Obj(),
					*utiltestingapi.MakeAdmissionCheckStrategyRule("ac3", "flavor1", "flavor2").Obj(),
					*utiltestingapi.MakeAdmissionCheckStrategyRule("ac4", "flavor1", "flavor2", "non-existent-flavor").Obj(),
					*utiltestingapi.MakeAdmissionCheckStrategyRule("ac5", "non-existent-flavor").Obj(),
					*utiltestingapi.MakeAdmissionCheckStrategyRule("ac6").Obj(),
				).Obj()
			gotAdmissionChecks := AdmissionChecksForWorkload(log, tc.wl, cq)

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
					Requests: resources.MapRequests{
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
					Requests: resources.MapRequests{
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
					Requests: resources.MapRequests{
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
					Requests: resources.MapRequests{
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
					Requests: resources.MapRequests{
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
						Requests: resources.MapRequests{
							corev1.ResourceCPU:    10000,
							corev1.ResourceMemory: 10 * 1024 * 1024,
							"nvidia.com/gpu":      1,
						},
					},
					{
						Name: "ps2",
						Requests: resources.MapRequests{
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
			got := PropagateResourceRequests(tc.wl, tc.info, resources.NewResourceFormatter())
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("Unexpected PropagateResourceRequests() result (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestNeedsSecondPass(t *testing.T) {
	now := time.Now().Truncate(time.Second)
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
				ReserveQuotaAt(
					utiltestingapi.MakeAdmission("tas-main").
						PodSets(utiltestingapi.MakePodSetAssignment("one").
							Assignment(corev1.ResourceCPU, "tas-default", "1000m").
							TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
								Domains(utiltestingapi.MakeTopologyDomainAssignment([]string{"x0"}, 1).Obj()).
								Obj()).
							Obj()).
						Obj(), now,
				).
				AdmittedAt(true, now).
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
				ReserveQuotaAt(
					utiltestingapi.MakeAdmission("tas-main").
						PodSets(utiltestingapi.MakePodSetAssignment("one").
							Assignment(corev1.ResourceCPU, "tas-default", "1000m").
							TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
								Domains(utiltestingapi.MakeTopologyDomainAssignment([]string{"x0"}, 1).Obj()).
								Obj()).
							Obj()).
						Obj(), now,
				).
				AdmittedAt(true, now).
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
				ReserveQuotaAt(
					utiltestingapi.MakeAdmission("tas-main").
						PodSets(utiltestingapi.MakePodSetAssignment("one").
							Assignment(corev1.ResourceCPU, "tas-default", "1000m").
							TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
								Domains(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
								Obj()).
							Obj()).
						Obj(), now,
				).
				AdmittedAt(true, now).
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
				ReserveQuotaAt(
					utiltestingapi.MakeAdmission("tas-main").
						PodSets(utiltestingapi.MakePodSetAssignment("one").
							Assignment(corev1.ResourceCPU, "tas-default", "1000m").
							TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
								Domains(utiltestingapi.MakeTopologyDomainAssignment([]string{"x0"}, 1).Obj()).
								Obj()).
							Obj()).
						Obj(), now,
				).
				AdmittedAt(true, now).
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
				ReserveQuotaAt(
					utiltestingapi.MakeAdmission("tas-main").
						PodSets(utiltestingapi.MakePodSetAssignment("one").
							Assignment(corev1.ResourceCPU, "tas-default", "1000m").
							TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
								Domains(utiltestingapi.MakeTopologyDomainAssignment([]string{"x0"}, 1).Obj()).
								Obj()).
							Obj()).
						Obj(), now,
				).
				AdmittedAt(true, now).
				EvictedAt(now).
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
				ReserveQuotaAt(
					utiltestingapi.MakeAdmission("tas-main").
						PodSets(
							utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "1000m").
								DelayedTopologyRequest(kueue.DelayedTopologyRequestStatePending).
								Obj(),
						).
						Obj(), now,
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
				ReserveQuotaAt(
					utiltestingapi.MakeAdmission("tas-main").
						PodSets(
							utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "tas-default", "1000m").
								DelayedTopologyRequest(kueue.DelayedTopologyRequestStatePending).
								Obj(),
						).
						Obj(), now,
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
		"on-hold workload with UnhealthyNode": {
			wl: utiltestingapi.MakeWorkload("foo", "default").
				UnhealthyNodes("x0").
				Queue("tas-main").
				PodSets(*utiltestingapi.MakePodSet("one", 1).
					PreferredTopologyRequest(corev1.LabelHostname).
					Request(corev1.ResourceCPU, "1").
					Obj()).
				ReserveQuotaAt(
					utiltestingapi.MakeAdmission("tas-main").
						PodSets(utiltestingapi.MakePodSetAssignment("one").
							Assignment(corev1.ResourceCPU, "tas-default", "1000m").
							TopologyAssignment(utiltestingapi.MakeTopologyAssignment(utiltas.Levels(&defaultSingleLevelTopology)).
								Domains(utiltestingapi.MakeTopologyDomainAssignment([]string{"x0"}, 1).Obj()).
								Obj()).
							Obj()).
						Obj(), now,
				).
				AdmittedAt(true, now).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadOnHold,
					Message: "On hold",
				}).
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
	features.SetFeatureGateDuringTest(t, features.KueueDRAIntegration, true)

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
						Requests: resources.MapRequests{
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
						Requests: resources.MapRequests{
							corev1.ResourceCPU: 100,
							"gpus":             2,
						},
					},
					{
						Name:  "worker",
						Count: 2,
						Requests: resources.MapRequests{
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
						Requests: resources.MapRequests{
							corev1.ResourceCPU: 100,
							"gpus":             1,
						},
					},
					{
						Name:  "worker",
						Count: 1,
						Requests: resources.MapRequests{
							corev1.ResourceMemory: 512 * 1024 * 1024,
						},
					},
				},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			info := NewInfo(&tc.workload, WithPreprocessedDRAResources(tc.draResources, nil))

			if diff := cmp.Diff(tc.wantInfo.TotalRequests, info.TotalRequests); diff != "" {
				t.Errorf("Unexpected TotalRequests (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestWithPreprocessedDRAResourcesReplacesExtendedResources(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.KueueDRAIntegration, true)

	cases := map[string]struct {
		workload                  kueue.Workload
		draResources              map[kueue.PodSetReference]corev1.ResourceList
		replacedExtendedResources map[kueue.PodSetReference]sets.Set[corev1.ResourceName]
		wantInfo                  Info
	}{
		"extended resource replaced with DRA resource": {
			workload: *utiltestingapi.MakeWorkload("test-wl", "default").
				PodSets(*utiltestingapi.MakePodSet("main", 1).
					Request(corev1.ResourceCPU, "100m").
					Request("example.com/gpu", "1").
					Obj()).
				Obj(),
			draResources: map[kueue.PodSetReference]corev1.ResourceList{
				"main": {
					"gpu": resource.MustParse("1"),
				},
			},
			replacedExtendedResources: map[kueue.PodSetReference]sets.Set[corev1.ResourceName]{
				"main": sets.New[corev1.ResourceName]("example.com/gpu"),
			},
			wantInfo: Info{
				TotalRequests: []PodSetResources{
					{
						Name:  "main",
						Count: 1,
						Requests: resources.MapRequests{
							corev1.ResourceCPU: 100,
							"gpu":              1,
						},
					},
				},
			},
		},
		"multiple extended resources replaced": {
			workload: *utiltestingapi.MakeWorkload("test-wl", "default").
				PodSets(*utiltestingapi.MakePodSet("main", 2).
					Request(corev1.ResourceCPU, "100m").
					Request("example.com/gpu", "2").
					Request("example.com/tpu", "1").
					Obj()).
				Obj(),
			draResources: map[kueue.PodSetReference]corev1.ResourceList{
				"main": {
					"gpu": resource.MustParse("2"),
					"tpu": resource.MustParse("1"),
				},
			},
			replacedExtendedResources: map[kueue.PodSetReference]sets.Set[corev1.ResourceName]{
				"main": sets.New[corev1.ResourceName]("example.com/gpu", "example.com/tpu"),
			},
			wantInfo: Info{
				TotalRequests: []PodSetResources{
					{
						Name:  "main",
						Count: 2,
						Requests: resources.MapRequests{
							corev1.ResourceCPU: 200,
							"gpu":              4,
							"tpu":              2,
						},
					},
				},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			info := NewInfo(&tc.workload, WithPreprocessedDRAResources(tc.draResources, tc.replacedExtendedResources))

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
			wantRequeueAt:  new(metav1.NewTime(futureTime)),
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
			wantRequeueAt:  new(metav1.NewTime(futureTime)),
			wantCount:      ptr.To[int32](1),
		},
		"should update time when existing requeue time is earlier": {
			workload: &kueue.Workload{
				Status: kueue.WorkloadStatus{
					RequeueState: &kueue.RequeueState{
						RequeueAt: new(metav1.NewTime(pastTime)),
						Count:     ptr.To[int32](2),
					},
				},
			},
			waitUntil:      metav1.NewTime(futureTime),
			incrementCount: false,
			wantUpdated:    true,
			wantRequeueAt:  new(metav1.NewTime(futureTime)),
			wantCount:      ptr.To[int32](2),
		},
		"should not update time when existing requeue time is later": {
			workload: &kueue.Workload{
				Status: kueue.WorkloadStatus{
					RequeueState: &kueue.RequeueState{
						RequeueAt: new(metav1.NewTime(evenMoreFutureTime)),
						Count:     ptr.To[int32](3),
					},
				},
			},
			waitUntil:      metav1.NewTime(futureTime),
			incrementCount: false,
			wantUpdated:    false,
			wantRequeueAt:  new(metav1.NewTime(evenMoreFutureTime)),
			wantCount:      ptr.To[int32](3),
		},
		"should increment count but keep later requeue time": {
			workload: &kueue.Workload{
				Status: kueue.WorkloadStatus{
					RequeueState: &kueue.RequeueState{
						RequeueAt: new(metav1.NewTime(evenMoreFutureTime)),
						Count:     ptr.To[int32](3),
					},
				},
			},
			waitUntil:      metav1.NewTime(futureTime),
			incrementCount: true,
			wantUpdated:    true,
			wantRequeueAt:  new(metav1.NewTime(evenMoreFutureTime)),
			wantCount:      ptr.To[int32](4),
		},
		"should increment count when requeue time is same": {
			workload: &kueue.Workload{
				Status: kueue.WorkloadStatus{
					RequeueState: &kueue.RequeueState{
						RequeueAt: new(metav1.NewTime(futureTime)),
						Count:     ptr.To[int32](1),
					},
				},
			},
			waitUntil:      metav1.NewTime(futureTime),
			incrementCount: true,
			wantUpdated:    true,
			wantRequeueAt:  new(metav1.NewTime(futureTime)),
			wantCount:      ptr.To[int32](2),
		},
		"should increment from zero count": {
			workload: &kueue.Workload{
				Status: kueue.WorkloadStatus{
					RequeueState: &kueue.RequeueState{
						RequeueAt: new(metav1.NewTime(pastTime)),
						Count:     ptr.To[int32](0),
					},
				},
			},
			waitUntil:      metav1.NewTime(futureTime),
			incrementCount: true,
			wantUpdated:    true,
			wantRequeueAt:  new(metav1.NewTime(futureTime)),
			wantCount:      ptr.To[int32](1),
		},
		"should handle zero time in requeue state": {
			workload: &kueue.Workload{
				Status: kueue.WorkloadStatus{
					RequeueState: &kueue.RequeueState{
						RequeueAt: new(metav1.NewTime(time.Time{})),
						Count:     ptr.To[int32](0),
					},
				},
			},
			waitUntil:      metav1.NewTime(futureTime),
			incrementCount: true,
			wantUpdated:    true,
			wantRequeueAt:  new(metav1.NewTime(futureTime)),
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

// TestWorkloadPriorityClassChanged tests the workloadPriorityClassChanged function.
func TestWorkloadPriorityClassChanged(t *testing.T) {
	testCases := map[string]struct {
		oldWorkload *kueue.Workload
		newWorkload *kueue.Workload
		wantChanged bool
	}{
		"no priority class on either workload": {
			oldWorkload: utiltestingapi.MakeWorkload("wl", "ns").Obj(),
			newWorkload: utiltestingapi.MakeWorkload("wl", "ns").Obj(),
			wantChanged: false,
		},
		"same priority class on both workloads": {
			oldWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				WorkloadPriorityClassRef("priority-1").
				Obj(),
			newWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				WorkloadPriorityClassRef("priority-1").
				Obj(),
			wantChanged: false,
		},
		"priority class changed from one to another": {
			oldWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				WorkloadPriorityClassRef("priority-1").
				Obj(),
			newWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				WorkloadPriorityClassRef("priority-2").
				Obj(),
			wantChanged: true,
		},
		"priority class added (none -> some)": {
			oldWorkload: utiltestingapi.MakeWorkload("wl", "ns").Obj(),
			newWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				WorkloadPriorityClassRef("priority-1").
				Obj(),
			wantChanged: true,
		},
		"priority class removed (some -> none) - blocked by validation": {
			oldWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				WorkloadPriorityClassRef("priority-1").
				Obj(),
			newWorkload: utiltestingapi.MakeWorkload("wl", "ns").Obj(),
			wantChanged: false,
		},
		"PodPriorityClass (not WorkloadPriorityClass) changed - should not trigger": {
			oldWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				PodPriorityClassRef("pod-priority-1").
				Obj(),
			newWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				PodPriorityClassRef("pod-priority-2").
				Obj(),
			wantChanged: false,
		},
		"PodPriorityClass added (none -> some) - should not trigger": {
			oldWorkload: utiltestingapi.MakeWorkload("wl", "ns").Obj(),
			newWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				PodPriorityClassRef("pod-priority-1").
				Obj(),
			wantChanged: false,
		},
		"priority value decreased": {
			oldWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				Priority(500).
				Obj(),
			newWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				Priority(600).
				Obj(),
			wantChanged: false,
		},
		"priority value decreased with WPC": {
			oldWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				WorkloadPriorityClassRef("priority-1").
				Priority(500).
				Obj(),
			newWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				WorkloadPriorityClassRef("priority-1").
				Priority(100).
				Obj(),
			wantChanged: true,
		},
		"priority value decreased with PPC": {
			oldWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				PodPriorityClassRef("pod-priority").
				Priority(500).
				Obj(),
			newWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				PodPriorityClassRef("pod-priority").
				Priority(100).
				Obj(),
			wantChanged: false,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			_, log := utiltesting.ContextWithLog(t)
			gotChanged := PriorityChanged(log, tc.oldWorkload, tc.newWorkload)
			if gotChanged != tc.wantChanged {
				t.Errorf("workloadPriorityChanged() = %v, want %v", gotChanged, tc.wantChanged)
			}
		})
	}
}

func TestEvictionPendingLatency(t *testing.T) {
	evictTime := time.Date(2024, 3, 15, 12, 0, 0, 0, time.UTC)
	metricNow := evictTime.Add(30 * time.Second)

	evictedByPreemption := metav1.Condition{
		Type:               kueue.WorkloadEvicted,
		Status:             metav1.ConditionTrue,
		Reason:             kueue.WorkloadEvictedByPreemption,
		LastTransitionTime: metav1.NewTime(evictTime),
	}
	otherEvicted := metav1.Condition{
		Type:               kueue.WorkloadEvicted,
		Status:             metav1.ConditionTrue,
		Reason:             kueue.WorkloadEvictedByPodsReadyTimeout,
		LastTransitionTime: metav1.NewTime(evictTime),
	}
	evictedByAdmissionCheck := metav1.Condition{
		Type:               kueue.WorkloadEvicted,
		Status:             metav1.ConditionTrue,
		Reason:             kueue.WorkloadEvictedByAdmissionCheck,
		LastTransitionTime: metav1.NewTime(evictTime),
	}
	admittedTrue := metav1.Condition{
		Type:               kueue.WorkloadAdmitted,
		Status:             metav1.ConditionTrue,
		LastTransitionTime: metav1.NewTime(evictTime),
	}
	quotaReservedTrue := metav1.Condition{
		Type:               kueue.WorkloadQuotaReserved,
		Status:             metav1.ConditionTrue,
		LastTransitionTime: metav1.NewTime(evictTime),
	}

	cases := []struct {
		name        string
		oldWl       *kueue.Workload
		newWl       *kueue.Workload
		now         time.Time
		wantOK      bool
		wantCQ      kueue.ClusterQueueReference
		wantReason  string
		wantLatency time.Duration
	}{
		{
			name:   "nil old workload",
			oldWl:  nil,
			newWl:  &kueue.Workload{Status: kueue.WorkloadStatus{Conditions: []metav1.Condition{evictedByPreemption}}},
			now:    metricNow,
			wantOK: false,
		},
		{
			name: "admitted to pending due to preemption eviction (cq from old admission)",
			oldWl: &kueue.Workload{
				Status: kueue.WorkloadStatus{
					Admission:  &kueue.Admission{ClusterQueue: "cq-a"},
					Conditions: []metav1.Condition{admittedTrue},
				},
			},
			newWl: &kueue.Workload{
				Status: kueue.WorkloadStatus{
					Conditions: []metav1.Condition{evictedByPreemption},
				},
			},
			now:         metricNow,
			wantOK:      true,
			wantCQ:      "cq-a",
			wantReason:  kueue.WorkloadEvictedByPreemption,
			wantLatency: 30 * time.Second,
		},
		{
			name: "quota reserved to pending due to preemption eviction",
			oldWl: &kueue.Workload{
				Status: kueue.WorkloadStatus{
					Admission:  &kueue.Admission{ClusterQueue: "cq-b"},
					Conditions: []metav1.Condition{quotaReservedTrue},
				},
			},
			newWl: &kueue.Workload{
				Status: kueue.WorkloadStatus{
					Conditions: []metav1.Condition{evictedByPreemption},
				},
			},
			now:         metricNow,
			wantOK:      true,
			wantCQ:      "cq-b",
			wantReason:  kueue.WorkloadEvictedByPreemption,
			wantLatency: 30 * time.Second,
		},
		{
			name: "admitted to pending due to PodsReadyTimeout eviction",
			oldWl: &kueue.Workload{
				Status: kueue.WorkloadStatus{
					Admission:  &kueue.Admission{ClusterQueue: "cq-a"},
					Conditions: []metav1.Condition{admittedTrue},
				},
			},
			newWl: &kueue.Workload{
				Status: kueue.WorkloadStatus{
					Conditions: []metav1.Condition{otherEvicted},
				},
			},
			now:         metricNow,
			wantOK:      true,
			wantCQ:      "cq-a",
			wantReason:  kueue.WorkloadEvictedByPodsReadyTimeout,
			wantLatency: 30 * time.Second,
		},
		{
			name: "admitted to pending due to AdmissionCheck eviction",
			oldWl: &kueue.Workload{
				Status: kueue.WorkloadStatus{
					Admission:  &kueue.Admission{ClusterQueue: "cq-a"},
					Conditions: []metav1.Condition{admittedTrue},
				},
			},
			newWl: &kueue.Workload{
				Status: kueue.WorkloadStatus{
					Conditions: []metav1.Condition{evictedByAdmissionCheck},
				},
			},
			now:         metricNow,
			wantOK:      true,
			wantCQ:      "cq-a",
			wantReason:  kueue.WorkloadEvictedByAdmissionCheck,
			wantLatency: 30 * time.Second,
		},
		{
			name: "skip when old admission missing (no cluster queue for metric)",
			oldWl: &kueue.Workload{
				Status: kueue.WorkloadStatus{},
			},
			newWl: &kueue.Workload{
				Status: kueue.WorkloadStatus{
					Conditions: []metav1.Condition{evictedByPreemption},
				},
			},
			now:    metricNow,
			wantOK: false,
		},
		{
			name: "skip when cluster queue empty string",
			oldWl: &kueue.Workload{
				Status: kueue.WorkloadStatus{
					Admission:  &kueue.Admission{ClusterQueue: ""},
					Conditions: []metav1.Condition{admittedTrue},
				},
			},
			newWl: &kueue.Workload{
				Status: kueue.WorkloadStatus{
					Conditions: []metav1.Condition{evictedByPreemption},
				},
			},
			now:    metricNow,
			wantOK: false,
		},
		{
			name: "skip when new status not pending (still admitted)",
			oldWl: &kueue.Workload{
				Status: kueue.WorkloadStatus{
					Admission:  &kueue.Admission{ClusterQueue: "cq-a"},
					Conditions: []metav1.Condition{admittedTrue},
				},
			},
			newWl: &kueue.Workload{
				Status: kueue.WorkloadStatus{
					Admission:  &kueue.Admission{ClusterQueue: "cq-a"},
					Conditions: []metav1.Condition{admittedTrue, evictedByPreemption},
				},
			},
			now:    metricNow,
			wantOK: false,
		},
		{
			name: "skip when previous status already pending",
			oldWl: &kueue.Workload{
				Status: kueue.WorkloadStatus{
					Conditions: []metav1.Condition{evictedByPreemption},
				},
			},
			newWl: &kueue.Workload{
				Status: kueue.WorkloadStatus{
					Conditions: []metav1.Condition{evictedByPreemption},
				},
			},
			now:    metricNow,
			wantOK: false,
		},
		{
			name: "skip missing eviction condition",
			oldWl: &kueue.Workload{
				Status: kueue.WorkloadStatus{
					Admission:  &kueue.Admission{ClusterQueue: "cq-a"},
					Conditions: []metav1.Condition{admittedTrue},
				},
			},
			newWl:  &kueue.Workload{Status: kueue.WorkloadStatus{}},
			now:    metricNow,
			wantOK: false,
		},
		{
			name: "skip eviction condition not true",
			oldWl: &kueue.Workload{
				Status: kueue.WorkloadStatus{
					Admission:  &kueue.Admission{ClusterQueue: "cq-a"},
					Conditions: []metav1.Condition{admittedTrue},
				},
			},
			newWl: &kueue.Workload{
				Status: kueue.WorkloadStatus{
					Conditions: []metav1.Condition{
						{
							Type:               kueue.WorkloadEvicted,
							Status:             metav1.ConditionFalse,
							Reason:             kueue.WorkloadEvictedByPreemption,
							LastTransitionTime: metav1.NewTime(evictTime),
						},
					},
				},
			},
			now:    metricNow,
			wantOK: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotCQ, gotReason, gotLatency, gotOK := EvictionPendingLatency(tc.oldWl, tc.newWl, tc.now)
			if gotOK != tc.wantOK {
				t.Fatalf("ok: got %v want %v (cq=%q reason=%q latency=%v)", gotOK, tc.wantOK, gotCQ, gotReason, gotLatency)
			}
			if !tc.wantOK {
				return
			}
			if gotCQ != tc.wantCQ {
				t.Errorf("cluster queue: got %q want %q", gotCQ, tc.wantCQ)
			}
			if gotReason != tc.wantReason {
				t.Errorf("reason: got %q want %q", gotReason, tc.wantReason)
			}
			if gotLatency != tc.wantLatency {
				t.Errorf("latency: got %v want %v", gotLatency, tc.wantLatency)
			}
		})
	}
}

func TestSchedulingHash(t *testing.T) {
	cases := map[string]struct {
		wl1          *kueue.Workload
		wl2          *kueue.Workload
		wantSame     bool
		featureGates map[featuregate.Feature]bool
	}{
		"same spec different identity produces same hash": {
			wl1: utiltestingapi.MakeWorkload("wl1", "ns1").
				Request(corev1.ResourceCPU, "2").
				Request(corev1.ResourceMemory, "1Gi").Obj(),
			wl2: utiltestingapi.MakeWorkload("wl2", "ns2").
				Request(corev1.ResourceCPU, "2").
				Request(corev1.ResourceMemory, "1Gi").Obj(),
			wantSame:     true,
			featureGates: map[featuregate.Feature]bool{features.SchedulingEquivalenceHashing: true},
		},
		"different resource requests": {
			wl1: utiltestingapi.MakeWorkload("wl1", "ns").
				Request(corev1.ResourceCPU, "1").Obj(),
			wl2: utiltestingapi.MakeWorkload("wl2", "ns").
				Request(corev1.ResourceCPU, "2").Obj(),
			wantSame:     false,
			featureGates: map[featuregate.Feature]bool{features.SchedulingEquivalenceHashing: true},
		},
		"different pod counts": {
			wl1: utiltestingapi.MakeWorkload("wl1", "ns").
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 3).
					Request(corev1.ResourceCPU, "1").Obj()).Obj(),
			wl2: utiltestingapi.MakeWorkload("wl2", "ns").
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 5).
					Request(corev1.ResourceCPU, "1").Obj()).Obj(),
			wantSame:     false,
			featureGates: map[featuregate.Feature]bool{features.SchedulingEquivalenceHashing: true},
		},
		"different workload priorities": {
			wl1: utiltestingapi.MakeWorkload("wl1", "ns").
				Priority(100).
				Request(corev1.ResourceCPU, "1").Obj(),
			wl2: utiltestingapi.MakeWorkload("wl2", "ns").
				Priority(200).
				Request(corev1.ResourceCPU, "1").Obj(),
			wantSame:     false,
			featureGates: map[featuregate.Feature]bool{features.SchedulingEquivalenceHashing: true},
		},
		"same raw priority but different effective priority": {
			wl1: func() *kueue.Workload {
				wl := utiltestingapi.MakeWorkload("wl1", "ns").
					Priority(100).
					Request(corev1.ResourceCPU, "1").Obj()
				wl.Annotations = map[string]string{"kueue.x-k8s.io/priority-boost": "10"}
				return wl
			}(),
			wl2: utiltestingapi.MakeWorkload("wl2", "ns").
				Priority(100).
				Request(corev1.ResourceCPU, "1").Obj(),
			wantSame: false,
			featureGates: map[featuregate.Feature]bool{
				features.SchedulingEquivalenceHashing: true,
				features.PriorityBoost:                true,
			},
		},
		"same spec, different allowed flavors annotation, concurrent admission enabled produces different hash": {
			wl1: func() *kueue.Workload {
				wl := utiltestingapi.MakeWorkload("wl1", "ns").
					Request(corev1.ResourceCPU, "1").Obj()
				wl.Annotations = map[string]string{controllerconstants.WorkloadAllowedResourceFlavorAnnotation: "flavor1"}
				return wl
			}(),
			wl2: func() *kueue.Workload {
				wl := utiltestingapi.MakeWorkload("wl2", "ns").
					Request(corev1.ResourceCPU, "1").Obj()
				wl.Annotations = map[string]string{controllerconstants.WorkloadAllowedResourceFlavorAnnotation: "flavor2"}
				return wl
			}(),
			wantSame: false,
			featureGates: map[featuregate.Feature]bool{
				features.SchedulingEquivalenceHashing: true,
				features.ConcurrentAdmission:          true,
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, tc.featureGates)
			info1 := NewInfo(tc.wl1)
			info1.UpdateSchedulingHash(logr.Discard())
			info2 := NewInfo(tc.wl2)
			info2.UpdateSchedulingHash(logr.Discard())
			if info1.SchedulingHash == "" {
				t.Error("SchedulingHash should not be empty")
			}
			if tc.wantSame && info1.SchedulingHash != info2.SchedulingHash {
				t.Errorf("expected same hash, got %q and %q", info1.SchedulingHash, info2.SchedulingHash)
			}
			if !tc.wantSame && info1.SchedulingHash == info2.SchedulingHash {
				t.Errorf("expected different hashes, got same %q", info1.SchedulingHash)
			}
		})
	}

	t.Run("DRA translation producing different TotalRequests produces different hash", func(t *testing.T) {
		features.SetFeatureGatesDuringTest(t, map[featuregate.Feature]bool{
			features.SchedulingEquivalenceHashing: true,
		})
		wl := utiltestingapi.MakeWorkload("wl", "ns").
			Request("example.com/gpu", "1").Obj()
		before := NewInfo(wl)
		before.UpdateSchedulingHash(logr.Discard())

		after := NewInfo(wl, WithPreprocessedDRAResources(
			map[kueue.PodSetReference]corev1.ResourceList{
				kueue.DefaultPodSetName: {
					"gpu": resource.MustParse("1"),
				},
			},
			map[kueue.PodSetReference]sets.Set[corev1.ResourceName]{
				kueue.DefaultPodSetName: sets.New[corev1.ResourceName]("example.com/gpu"),
			},
		))
		after.UpdateSchedulingHash(logr.Discard())

		if diff := cmp.Diff(before.TotalRequests, after.TotalRequests); diff == "" {
			t.Fatal("precondition failed: TotalRequests should differ after DRA translation")
		}
		if before.SchedulingHash == after.SchedulingHash {
			t.Errorf("expected different hashes after DRA translation, got same %q", before.SchedulingHash)
		}
	})
}

func TestUsedNodes(t *testing.T) {
	cases := map[string]struct {
		wl   *kueue.Workload
		want []string
	}{
		"unadmitted workload": {
			wl: &kueue.Workload{
				Status: kueue.WorkloadStatus{
					Admission: nil,
				},
			},
			want: nil,
		},
		"tas admission present but lacking admitted condition": {
			wl: &kueue.Workload{
				Status: kueue.WorkloadStatus{
					Conditions: []metav1.Condition{{Type: kueue.WorkloadAdmitted, Status: metav1.ConditionFalse}},
					Admission: &kueue.Admission{
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								TopologyAssignment: &kueue.TopologyAssignment{
									Levels: []string{"zone", corev1.LabelHostname},
									Slices: []kueue.TopologyAssignmentSlice{
										{
											DomainCount: 1,
											ValuesPerLevel: []kueue.TopologyAssignmentSliceLevelValues{
												{Universal: new("zone-1")},
												{Individual: &kueue.TopologyAssignmentSliceLevelIndividualValues{
													Roots: []string{"node-1"},
												}},
											},
											PodCounts: kueue.TopologyAssignmentSlicePodCounts{
												Universal: ptr.To[int32](1),
											},
										},
									},
								},
							},
						},
					},
				},
			},
			want: nil,
		},
		"quota reserved but waiting for admission check": {
			wl: &kueue.Workload{
				Status: kueue.WorkloadStatus{
					Conditions: []metav1.Condition{{Type: kueue.WorkloadQuotaReserved, Status: metav1.ConditionTrue}},
					Admission: &kueue.Admission{
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								TopologyAssignment: &kueue.TopologyAssignment{
									Levels: []string{"zone", corev1.LabelHostname},
									Slices: []kueue.TopologyAssignmentSlice{
										{
											DomainCount: 1,
											ValuesPerLevel: []kueue.TopologyAssignmentSliceLevelValues{
												{Universal: new("zone-1")},
												{Individual: &kueue.TopologyAssignmentSliceLevelIndividualValues{
													Roots: []string{"node-1"},
												}},
											},
											PodCounts: kueue.TopologyAssignmentSlicePodCounts{
												Universal: ptr.To[int32](1),
											},
										},
									},
								},
							},
						},
					},
				},
			},
			want: nil,
		},
		"admitted without tas": {
			wl: &kueue.Workload{
				Status: kueue.WorkloadStatus{
					Conditions: []metav1.Condition{{Type: kueue.WorkloadAdmitted, Status: metav1.ConditionTrue}},
					Admission: &kueue.Admission{
						PodSetAssignments: []kueue.PodSetAssignment{{Name: "main"}},
					},
				},
			},
			want: nil,
		},
		"valid tas assignment": {
			wl: &kueue.Workload{
				Status: kueue.WorkloadStatus{
					Conditions: []metav1.Condition{{Type: kueue.WorkloadAdmitted, Status: metav1.ConditionTrue}},
					Admission: &kueue.Admission{
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								TopologyAssignment: &kueue.TopologyAssignment{
									Levels: []string{"zone", corev1.LabelHostname},
									Slices: []kueue.TopologyAssignmentSlice{
										{
											DomainCount: 2,
											ValuesPerLevel: []kueue.TopologyAssignmentSliceLevelValues{
												{Universal: new("zone-1")},
												{Individual: &kueue.TopologyAssignmentSliceLevelIndividualValues{
													Roots: []string{"node-1", "node-2"},
												}},
											},
											PodCounts: kueue.TopologyAssignmentSlicePodCounts{
												Universal: ptr.To[int32](1),
											},
										},
									},
								},
							},
						},
					},
				},
			},
			want: []string{"node-1", "node-2"},
		},
		"duplicate node assignments across pod sets": {
			wl: &kueue.Workload{
				Status: kueue.WorkloadStatus{
					Conditions: []metav1.Condition{{Type: kueue.WorkloadAdmitted, Status: metav1.ConditionTrue}},
					Admission: &kueue.Admission{
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								TopologyAssignment: &kueue.TopologyAssignment{
									Levels: []string{"zone", corev1.LabelHostname},
									Slices: []kueue.TopologyAssignmentSlice{
										{
											DomainCount: 1,
											ValuesPerLevel: []kueue.TopologyAssignmentSliceLevelValues{
												{Universal: new("zone-1")},
												{Individual: &kueue.TopologyAssignmentSliceLevelIndividualValues{
													Roots: []string{"node-1"},
												}},
											},
											PodCounts: kueue.TopologyAssignmentSlicePodCounts{
												Universal: ptr.To[int32](1),
											},
										},
									},
								},
							},
							{
								TopologyAssignment: &kueue.TopologyAssignment{
									Levels: []string{"zone", corev1.LabelHostname},
									Slices: []kueue.TopologyAssignmentSlice{
										{
											DomainCount: 2,
											ValuesPerLevel: []kueue.TopologyAssignmentSliceLevelValues{
												{Universal: new("zone-1")},
												{Individual: &kueue.TopologyAssignmentSliceLevelIndividualValues{
													Roots: []string{"node-1", "node-2"},
												}},
											},
											PodCounts: kueue.TopologyAssignmentSlicePodCounts{
												Universal: ptr.To[int32](1),
											},
										},
									},
								},
							},
						},
					},
				},
			},
			want: []string{"node-1", "node-2"},
		},
		"mixed tas and non-tas pod sets": {
			wl: &kueue.Workload{
				Status: kueue.WorkloadStatus{
					Conditions: []metav1.Condition{{Type: kueue.WorkloadAdmitted, Status: metav1.ConditionTrue}},
					Admission: &kueue.Admission{
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								TopologyAssignment: &kueue.TopologyAssignment{
									Levels: []string{"zone", corev1.LabelHostname},
									Slices: []kueue.TopologyAssignmentSlice{
										{
											DomainCount: 1,
											ValuesPerLevel: []kueue.TopologyAssignmentSliceLevelValues{
												{Universal: new("zone-1")},
												{Individual: &kueue.TopologyAssignmentSliceLevelIndividualValues{
													Roots: []string{"node-1"},
												}},
											},
											PodCounts: kueue.TopologyAssignmentSlicePodCounts{
												Universal: ptr.To[int32](1),
											},
										},
									},
								},
							},
							{
								Name: "non-tas-podset",
							},
						},
					},
				},
			},
			want: []string{"node-1"},
		},
		"non-hostname topology": {
			wl: &kueue.Workload{
				Status: kueue.WorkloadStatus{
					Conditions: []metav1.Condition{{Type: kueue.WorkloadAdmitted, Status: metav1.ConditionTrue}},
					Admission: &kueue.Admission{
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								TopologyAssignment: &kueue.TopologyAssignment{
									Levels: []string{"zone", "rack"},
									Slices: []kueue.TopologyAssignmentSlice{
										{
											DomainCount: 1,
											ValuesPerLevel: []kueue.TopologyAssignmentSliceLevelValues{
												{Universal: new("zone-1")},
												{Universal: new("rack-1")},
											},
											PodCounts: kueue.TopologyAssignmentSlicePodCounts{
												Universal: ptr.To[int32](1),
											},
										},
									},
								},
							},
						},
					},
				},
			},
			want: nil,
		},
	}

	sortStrings := cmpopts.SortSlices(func(a, b string) bool { return a < b })

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := TASAssignedNodeNames(tc.wl)
			if diff := cmp.Diff(tc.want, got, cmpopts.EquateEmpty(), sortStrings); diff != "" {
				t.Errorf("Unexpected nodes (-want +got):\n%s", diff)
			}
		})
	}
}

func TestIsExplicitlyRequestingTAS(t *testing.T) {
	cases := map[string]struct {
		podSets []kueue.PodSet
		want    bool
	}{
		"podset slice required topology constraints only": {
			podSets: []kueue.PodSet{
				{
					TopologyRequest: &kueue.PodSetTopologyRequest{
						PodsetSliceRequiredTopologyConstraints: []kueue.PodsetSliceRequiredTopologyConstraint{
							{
								Topology: "cloud.provider.com/topology-rack",
								Size:     4,
							},
						},
					},
				},
			},
			want: true,
		},
		"empty topology request": {
			podSets: []kueue.PodSet{
				{
					TopologyRequest: &kueue.PodSetTopologyRequest{},
				},
			},
			want: false,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := IsExplicitlyRequestingTAS(tc.podSets...)
			if got != tc.want {
				t.Errorf("IsExplicitlyRequestingTAS() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSumTotalRequestsWithDRAFromAdmission(t *testing.T) {
	fakeClock := testingclock.NewFakeClock(time.Now())
	now := fakeClock.Now()
	wl := utiltestingapi.MakeWorkload("test-wl", "default").
		PodSets(*utiltestingapi.MakePodSet("main", 1).
			Request(corev1.ResourceCPU, "1").
			Obj()).
		ReserveQuotaAt(
			utiltestingapi.MakeAdmission("cq").
				PodSets(
					utiltestingapi.MakePodSetAssignment("main").
						Flavor(corev1.ResourceCPU, "default").
						ResourceUsage(corev1.ResourceCPU, "1000m").
						Flavor("gpu-logical", "gpu-flavor").
						ResourceUsage("gpu-logical", "2").
						Count(1).
						Obj(),
				).Obj(), now,
		).Obj()

	info := NewInfo(wl)
	sumReqs := info.SumTotalRequests(resources.NewResourceFormatter())

	// Verify CPU is present
	cpuVal, hasCPU := sumReqs[corev1.ResourceCPU]
	if !hasCPU {
		t.Fatal("SumTotalRequests should include cpu")
	}
	if cpuVal.Cmp(resource.MustParse("1")) != 0 {
		t.Errorf("cpu = %v, want 1", cpuVal)
	}

	// Verify DRA logical resource is present from admission
	gpuVal, hasGPU := sumReqs["gpu-logical"]
	if !hasGPU {
		t.Fatal("SumTotalRequests should include DRA logical resource 'gpu-logical' from admission")
	}
	if gpuVal.Cmp(resource.MustParse("2")) != 0 {
		t.Errorf("gpu-logical = %v, want 2", gpuVal)
	}
}

func TestShouldSkipClusterNomination(t *testing.T) {
	cases := map[string]struct {
		acs       *kueue.AdmissionCheckState
		wl        *kueue.Workload
		isElastic bool
		want      bool
	}{
		"nil admission check state": {
			acs:  nil,
			wl:   &kueue.Workload{},
			want: true,
		},
		"admission check Pending, no ClusterName": {
			acs: &kueue.AdmissionCheckState{
				State: kueue.CheckStatePending,
			},
			wl:   &kueue.Workload{},
			want: false,
		},
		"admission check Retry": {
			acs: &kueue.AdmissionCheckState{
				State: kueue.CheckStateRetry,
			},
			wl:   &kueue.Workload{},
			want: true,
		},
		"admission check Ready": {
			acs: &kueue.AdmissionCheckState{
				State: kueue.CheckStateReady,
			},
			wl:   &kueue.Workload{},
			want: true,
		},
		"admission check Rejected": {
			acs: &kueue.AdmissionCheckState{
				State: kueue.CheckStateRejected,
			},
			wl:   &kueue.Workload{},
			want: true,
		},
		"admission check Pending, ClusterName set (eviction ongoing)": {
			acs: &kueue.AdmissionCheckState{
				State: kueue.CheckStatePending,
			},
			wl: &kueue.Workload{
				Status: kueue.WorkloadStatus{
					ClusterName: new("worker1"),
				},
			},
			want: true,
		},
		"admission check Pending, ClusterName set, elastic workload": {
			acs: &kueue.AdmissionCheckState{
				State: kueue.CheckStatePending,
			},
			wl: &kueue.Workload{
				Status: kueue.WorkloadStatus{
					ClusterName: new("worker1"),
				},
			},
			isElastic: true,
			want:      false,
		},
		"admission check Retry, ClusterName set (eviction ongoing)": {
			acs: &kueue.AdmissionCheckState{
				State: kueue.CheckStateRetry,
			},
			wl: &kueue.Workload{
				Status: kueue.WorkloadStatus{
					ClusterName: new("worker1"),
				},
			},
			want: true,
		},
		"admission check Retry, ClusterName set, elastic workload": {
			acs: &kueue.AdmissionCheckState{
				State: kueue.CheckStateRetry,
			},
			wl: &kueue.Workload{
				Status: kueue.WorkloadStatus{
					ClusterName: new("worker1"),
				},
			},
			isElastic: true,
			want:      true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := ShouldSkipClusterNomination(tc.acs, tc.wl, tc.isElastic)
			if got != tc.want {
				t.Errorf("ShouldSkipClusterNomination() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCalcLocalQueueFSUsage(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)
	errOther := errors.New("other error")
	cases := map[string]struct {
		err       error
		wantUsage float64
		wantErr   error
	}{
		"not found error": {
			err:       apierrors.NewNotFound(schema.GroupResource{Resource: "localqueues"}, "lq"),
			wantUsage: 50.0, // (10 cpu * 5 weight) / 1.0 default weight = 50.0
			wantErr:   nil,
		},
		"other error": {
			err:       errOther,
			wantUsage: 0,
			wantErr:   errOther,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			wl := utiltestingapi.MakeWorkload("wl", "ns").Queue("lq").Obj()
			cl := utiltesting.NewClientBuilder().
				WithInterceptorFuncs(interceptor.Funcs{
					Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
						return tc.err
					},
				}).
				Build()

			info := NewInfo(wl)

			resWeights := map[corev1.ResourceName]float64{corev1.ResourceCPU: 5.0}

			afsConsumed := queueafs.NewAfsConsumedResources()
			afsConsumed.Set(utilqueue.KeyFromWorkload(wl), corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("10"),
			}, time.Now())

			usage, err := info.CalcLocalQueueFSUsage(ctx, cl, resWeights, nil, afsConsumed)

			if diff := cmp.Diff(tc.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Unexpected error (-want,+got):\n%s", diff)
			}

			if usage != tc.wantUsage {
				t.Errorf("CalcLocalQueueFSUsage() = %v, want %v", usage, tc.wantUsage)
			}
		})
	}
}
