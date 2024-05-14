// Copyright 2024 The Kubernetes Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package rest

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	visibility "sigs.k8s.io/kueue/apis/visibility/v1alpha1"
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/constants"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

func TestRunningWorkloadsInCQ(t *testing.T) {
	const (
		nsName   = "foo"
		cqNameA  = "cqA"
		lqNameA  = "lqA"
		lqNameB  = "lqB"
		lowPrio  = 50
		highPrio = 100
	)

	var (
		defaultQueryParams = &visibility.RunningWorkloadOptions{
			Offset: 0,
			Limit:  constants.DefaultRunningWorkloadsLimit,
		}
	)

	var (
		q1 = utiltesting.MakeFlavorQuotas("flavor1").Resource("cpu", "10").Obj()
	)

	podSets := []kueue.PodSet{
		*utiltesting.MakePodSet("driver", 1).
			Request(corev1.ResourceCPU, "1").
			Obj(),
	}
	podSetFlavors := []kueue.PodSetAssignment{
		{
			Name: "driver",
			Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
				corev1.ResourceCPU: "flavor1",
			},
			ResourceUsage: corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("1"),
			},
		},
	}

	var (
		adA = utiltesting.MakeAdmission(cqNameA).PodSets(podSetFlavors...).Obj()
	)

	scheme := runtime.NewScheme()
	if err := kueue.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed adding kueue scheme: %s", err)
	}
	if err := visibility.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed adding kueue scheme: %s", err)
	}

	now := time.Now()
	cases := map[string]struct {
		clusterQueues []*kueue.ClusterQueue
		workloads     []*kueue.Workload
		req           *runningReq
		wantResp      *runningResp
		wantErrMatch  func(error) bool
	}{
		"single ClusterQueue and single LocalQueue setup with two workloads and default query parameters": {
			clusterQueues: []*kueue.ClusterQueue{
				utiltesting.MakeClusterQueue(cqNameA).ResourceGroup(*q1).Obj(),
			},
			workloads: []*kueue.Workload{
				utiltesting.MakeWorkload("a", nsName).PodSets(podSets...).ReserveQuota(adA).Queue(lqNameA).Priority(highPrio).Creation(now).Admitted(true).Obj(),
				utiltesting.MakeWorkload("b", nsName).PodSets(podSets...).ReserveQuota(adA).Queue(lqNameA).Priority(lowPrio).Creation(now).Admitted(true).Obj(),
			},
			req: &runningReq{
				queueName:   cqNameA,
				queryParams: defaultQueryParams,
			},
			wantResp: &runningResp{
				wantRunningWorkloads: []visibility.RunningWorkload{
					{
						ObjectMeta: v1.ObjectMeta{
							Name:              "a",
							Namespace:         nsName,
							CreationTimestamp: v1.NewTime(now),
						},
						Priority: highPrio,
					},
					{
						ObjectMeta: v1.ObjectMeta{
							Name:              "b",
							Namespace:         nsName,
							CreationTimestamp: v1.NewTime(now),
						},
						Priority: lowPrio,
					}},
			},
		},
		"single ClusterQueue and two LocalQueue setup with four workloads and default query parameters": {
			clusterQueues: []*kueue.ClusterQueue{
				utiltesting.MakeClusterQueue(cqNameA).ResourceGroup(*q1).Obj(),
			},
			workloads: []*kueue.Workload{
				utiltesting.MakeWorkload("lqA-high-prio", nsName).PodSets(podSets...).ReserveQuota(adA).Queue(lqNameA).Priority(highPrio).Creation(now).Admitted(true).Obj(),
				utiltesting.MakeWorkload("lqA-low-prio", nsName).PodSets(podSets...).ReserveQuota(adA).Queue(lqNameA).Priority(lowPrio).Creation(now).Admitted(true).Obj(),
				utiltesting.MakeWorkload("lqB-high-prio", nsName).PodSets(podSets...).ReserveQuota(adA).Queue(lqNameB).Priority(highPrio).Creation(now.Add(time.Second)).Admitted(true).Obj(),
				utiltesting.MakeWorkload("lqB-low-prio", nsName).PodSets(podSets...).ReserveQuota(adA).Queue(lqNameB).Priority(lowPrio).Creation(now.Add(time.Second)).Admitted(true).Obj(),
			},
			req: &runningReq{
				queueName:   cqNameA,
				queryParams: defaultQueryParams,
			},
			wantResp: &runningResp{
				wantRunningWorkloads: []visibility.RunningWorkload{
					{
						ObjectMeta: v1.ObjectMeta{
							Name:              "lqA-high-prio",
							Namespace:         nsName,
							CreationTimestamp: v1.NewTime(now),
						},
						Priority: highPrio,
					},
					{
						ObjectMeta: v1.ObjectMeta{
							Name:              "lqB-high-prio",
							Namespace:         nsName,
							CreationTimestamp: v1.NewTime(now.Add(time.Second)),
						},
						Priority: highPrio,
					},
					{
						ObjectMeta: v1.ObjectMeta{
							Name:              "lqA-low-prio",
							Namespace:         nsName,
							CreationTimestamp: v1.NewTime(now),
						},
						Priority: lowPrio,
					},
					{
						ObjectMeta: v1.ObjectMeta{
							Name:              "lqB-low-prio",
							Namespace:         nsName,
							CreationTimestamp: v1.NewTime(now.Add(time.Second)),
						},
						Priority: lowPrio,
					}},
			},
		},
		"limit query parameter set": {
			clusterQueues: []*kueue.ClusterQueue{
				utiltesting.MakeClusterQueue(cqNameA).ResourceGroup(*q1).Obj(),
			},
			workloads: []*kueue.Workload{
				utiltesting.MakeWorkload("a", nsName).PodSets(podSets...).ReserveQuota(adA).Queue(lqNameA).Priority(highPrio).Admitted(true).Creation(now).Obj(),
				utiltesting.MakeWorkload("b", nsName).PodSets(podSets...).ReserveQuota(adA).Queue(lqNameA).Priority(highPrio).Admitted(true).Creation(now.Add(time.Second)).Obj(),
				utiltesting.MakeWorkload("c", nsName).PodSets(podSets...).ReserveQuota(adA).Queue(lqNameA).Priority(highPrio).Admitted(true).Creation(now.Add(time.Second * 2)).Obj(),
			},
			req: &runningReq{
				queueName: cqNameA,
				queryParams: &visibility.RunningWorkloadOptions{
					Limit: 2,
				},
			},
			wantResp: &runningResp{
				wantRunningWorkloads: []visibility.RunningWorkload{
					{
						ObjectMeta: v1.ObjectMeta{
							Name:              "a",
							Namespace:         nsName,
							CreationTimestamp: v1.NewTime(now),
						},
						Priority: highPrio,
					},
					{
						ObjectMeta: v1.ObjectMeta{
							Name:              "b",
							Namespace:         nsName,
							CreationTimestamp: v1.NewTime(now.Add(time.Second)),
						},
						Priority: highPrio,
					}},
			},
		},
		"offset query parameter set": {
			clusterQueues: []*kueue.ClusterQueue{
				utiltesting.MakeClusterQueue(cqNameA).ResourceGroup(*q1).Obj(),
			},
			workloads: []*kueue.Workload{
				utiltesting.MakeWorkload("a", nsName).PodSets(podSets...).ReserveQuota(adA).Queue(lqNameA).Priority(highPrio).Admitted(true).Creation(now).Obj(),
				utiltesting.MakeWorkload("b", nsName).PodSets(podSets...).ReserveQuota(adA).Queue(lqNameA).Priority(highPrio).Admitted(true).Creation(now.Add(time.Second)).Obj(),
				utiltesting.MakeWorkload("c", nsName).PodSets(podSets...).ReserveQuota(adA).Queue(lqNameA).Priority(highPrio).Admitted(true).Creation(now.Add(time.Second * 2)).Obj(),
			},
			req: &runningReq{
				queueName: cqNameA,
				queryParams: &visibility.RunningWorkloadOptions{
					Offset: 1,
					Limit:  constants.DefaultRunningWorkloadsLimit,
				},
			},
			wantResp: &runningResp{
				wantRunningWorkloads: []visibility.RunningWorkload{
					{
						ObjectMeta: v1.ObjectMeta{
							Name:              "b",
							Namespace:         nsName,
							CreationTimestamp: v1.NewTime(now.Add(time.Second)),
						},
						Priority: highPrio,
					},
					{
						ObjectMeta: v1.ObjectMeta{
							Name:              "c",
							Namespace:         nsName,
							CreationTimestamp: v1.NewTime(now.Add(time.Second * 2)),
						},
						Priority: highPrio,
					}},
			},
		},
		"limit offset query parameters set": {
			clusterQueues: []*kueue.ClusterQueue{
				utiltesting.MakeClusterQueue(cqNameA).ResourceGroup(*q1).Obj(),
			},
			workloads: []*kueue.Workload{
				utiltesting.MakeWorkload("a", nsName).ReserveQuota(adA).Queue(lqNameA).Priority(highPrio).Creation(now).Admitted(true).Obj(),
				utiltesting.MakeWorkload("b", nsName).ReserveQuota(adA).Queue(lqNameA).Priority(highPrio).Creation(now.Add(time.Second)).Admitted(true).Obj(),
				utiltesting.MakeWorkload("c", nsName).ReserveQuota(adA).Queue(lqNameA).Priority(highPrio).Creation(now.Add(time.Second * 2)).Admitted(true).Obj(),
			},
			req: &runningReq{
				queueName: cqNameA,
				queryParams: &visibility.RunningWorkloadOptions{
					Offset: 1,
					Limit:  1,
				},
			},
			wantResp: &runningResp{
				wantRunningWorkloads: []visibility.RunningWorkload{
					{
						ObjectMeta: v1.ObjectMeta{
							Name:              "b",
							Namespace:         nsName,
							CreationTimestamp: v1.NewTime(now.Add(time.Second)),
						},
						Priority: highPrio,
					}},
			},
		},
		"empty cluster queue": {
			clusterQueues: []*kueue.ClusterQueue{
				utiltesting.MakeClusterQueue(cqNameA).ResourceGroup(*q1).Obj(),
			},
			req: &runningReq{
				queueName:   cqNameA,
				queryParams: defaultQueryParams,
			},
			wantResp: &runningResp{},
		},
		"nonexistent queue name": {
			req: &runningReq{
				queueName:   "nonexistent-queue",
				queryParams: defaultQueryParams,
			},
			wantResp: &runningResp{
				wantErr: errors.NewNotFound(visibility.Resource("clusterqueue"), "nonexistent-queue"),
			},
			wantErrMatch: errors.IsNotFound,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			client := utiltesting.NewFakeClient()
			cCache := cache.New(client, cache.WithPodsReadyTracking(false))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			runningWorkloadsInCqREST := NewRunningWorkloadsInCqREST(cCache)
			for _, cq := range tc.clusterQueues {
				if err := cCache.AddClusterQueue(ctx, cq); err != nil {
					t.Fatalf("Adding cluster queue %s: %v", cq.Name, err)
				}
			}
			for _, w := range tc.workloads {
				cCache.AddOrUpdateWorkload(w)
			}

			info, err := runningWorkloadsInCqREST.Get(ctx, tc.req.queueName, tc.req.queryParams)
			if tc.wantErrMatch != nil {
				if !tc.wantErrMatch(err) {
					t.Errorf("Error differs: (-want,+got):\n%s", cmp.Diff(tc.wantResp.wantErr.Error(), err.Error()))
				}
			} else if err != nil {
				t.Error(err)
			} else {
				runningWorkloadsInfo := info.(*visibility.RunningWorkloadsSummary)
				less := func(a, b visibility.RunningWorkload) bool {
					p1 := a.Priority
					p2 := b.Priority

					if p1 != p2 {
						return p1 > p2
					}

					return a.CreationTimestamp.Before(&b.CreationTimestamp)
				}
				sort.Slice(tc.wantResp.wantRunningWorkloads, func(i, j int) bool {
					return less(tc.wantResp.wantRunningWorkloads[i], tc.wantResp.wantRunningWorkloads[j])
				})
				if diff := cmp.Diff(tc.wantResp.wantRunningWorkloads, runningWorkloadsInfo.Items, cmpopts.EquateEmpty(), cmpopts.IgnoreFields(visibility.RunningWorkload{}, "AdmissionTime")); diff != "" {
					t.Errorf("Running workloads differ: (-want,+got):\n%s", diff)
				}
			}
		})
	}
}
