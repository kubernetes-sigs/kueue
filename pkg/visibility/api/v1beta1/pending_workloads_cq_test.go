/*
Copyright 2023 The Kubernetes Authors.

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

package v1beta1

import (
	"context"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	visibility "sigs.k8s.io/kueue/apis/visibility/v1beta1"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/queue"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

func TestPendingWorkloadsInCQ(t *testing.T) {
	const (
		nsName   = "foo"
		cqNameA  = "cqA"
		cqNameB  = "cqB"
		lqNameA  = "lqA"
		lqNameB  = "lqB"
		lowPrio  = 50
		highPrio = 100
	)

	var (
		defaultQueryParams = &visibility.PendingWorkloadOptions{
			Offset: 0,
			Limit:  constants.DefaultPendingWorkloadsLimit,
		}
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
		queues        []*kueue.LocalQueue
		workloads     []*kueue.Workload
		req           *req
		wantResp      *resp
		wantErrMatch  func(error) bool
	}{
		"single ClusterQueue and single LocalQueue setup with two workloads and default query parameters": {
			clusterQueues: []*kueue.ClusterQueue{
				utiltesting.MakeClusterQueue(cqNameA).Obj(),
			},
			queues: []*kueue.LocalQueue{
				utiltesting.MakeLocalQueue(lqNameA, nsName).ClusterQueue(cqNameA).Obj(),
			},
			workloads: []*kueue.Workload{
				utiltesting.MakeWorkload("a", nsName).Queue(lqNameA).Priority(highPrio).Creation(now).Obj(),
				utiltesting.MakeWorkload("b", nsName).Queue(lqNameA).Priority(lowPrio).Creation(now).Obj(),
			},
			req: &req{
				queueName:   cqNameA,
				queryParams: defaultQueryParams,
			},
			wantResp: &resp{
				wantPendingWorkloads: []visibility.PendingWorkload{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:              "a",
							Namespace:         nsName,
							CreationTimestamp: metav1.NewTime(now),
						},
						LocalQueueName:         lqNameA,
						Priority:               highPrio,
						PositionInClusterQueue: 0,
						PositionInLocalQueue:   0,
					},
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:              "b",
							Namespace:         nsName,
							CreationTimestamp: metav1.NewTime(now),
						},
						LocalQueueName:         lqNameA,
						Priority:               lowPrio,
						PositionInClusterQueue: 1,
						PositionInLocalQueue:   1,
					}},
			},
		},
		"single ClusterQueue and two LocalQueue setup with four workloads and default query parameters": {
			clusterQueues: []*kueue.ClusterQueue{
				utiltesting.MakeClusterQueue(cqNameA).Obj(),
			},
			queues: []*kueue.LocalQueue{
				utiltesting.MakeLocalQueue(lqNameA, nsName).ClusterQueue(cqNameA).Obj(),
				utiltesting.MakeLocalQueue(lqNameB, nsName).ClusterQueue(cqNameA).Obj(),
			},
			workloads: []*kueue.Workload{
				utiltesting.MakeWorkload("lqA-high-prio", nsName).Queue(lqNameA).Priority(highPrio).Creation(now).Obj(),
				utiltesting.MakeWorkload("lqA-low-prio", nsName).Queue(lqNameA).Priority(lowPrio).Creation(now).Obj(),
				utiltesting.MakeWorkload("lqB-high-prio", nsName).Queue(lqNameB).Priority(highPrio).Creation(now.Add(time.Second)).Obj(),
				utiltesting.MakeWorkload("lqB-low-prio", nsName).Queue(lqNameB).Priority(lowPrio).Creation(now.Add(time.Second)).Obj(),
			},
			req: &req{
				queueName:   cqNameA,
				queryParams: defaultQueryParams,
			},
			wantResp: &resp{
				wantPendingWorkloads: []visibility.PendingWorkload{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:              "lqA-high-prio",
							Namespace:         nsName,
							CreationTimestamp: metav1.NewTime(now),
						},
						LocalQueueName:         lqNameA,
						Priority:               highPrio,
						PositionInClusterQueue: 0,
						PositionInLocalQueue:   0,
					},
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:              "lqB-high-prio",
							Namespace:         nsName,
							CreationTimestamp: metav1.NewTime(now.Add(time.Second)),
						},
						LocalQueueName:         lqNameB,
						Priority:               highPrio,
						PositionInClusterQueue: 1,
						PositionInLocalQueue:   0,
					},
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:              "lqA-low-prio",
							Namespace:         nsName,
							CreationTimestamp: metav1.NewTime(now),
						},
						LocalQueueName:         lqNameA,
						Priority:               lowPrio,
						PositionInClusterQueue: 2,
						PositionInLocalQueue:   1,
					},
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:              "lqB-low-prio",
							Namespace:         nsName,
							CreationTimestamp: metav1.NewTime(now.Add(time.Second)),
						},
						LocalQueueName:         lqNameB,
						Priority:               lowPrio,
						PositionInClusterQueue: 3,
						PositionInLocalQueue:   1,
					}},
			},
		},
		"limit query parameter set": {
			clusterQueues: []*kueue.ClusterQueue{
				utiltesting.MakeClusterQueue(cqNameA).Obj(),
			},
			queues: []*kueue.LocalQueue{
				utiltesting.MakeLocalQueue(lqNameA, nsName).ClusterQueue(cqNameA).Obj(),
			},
			workloads: []*kueue.Workload{
				utiltesting.MakeWorkload("a", nsName).Queue(lqNameA).Priority(highPrio).Creation(now).Obj(),
				utiltesting.MakeWorkload("b", nsName).Queue(lqNameA).Priority(highPrio).Creation(now.Add(time.Second)).Obj(),
				utiltesting.MakeWorkload("c", nsName).Queue(lqNameA).Priority(highPrio).Creation(now.Add(time.Second * 2)).Obj(),
			},
			req: &req{
				queueName: cqNameA,
				queryParams: &visibility.PendingWorkloadOptions{
					Limit: 2,
				},
			},
			wantResp: &resp{
				wantPendingWorkloads: []visibility.PendingWorkload{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:              "a",
							Namespace:         nsName,
							CreationTimestamp: metav1.NewTime(now),
						},
						LocalQueueName:         lqNameA,
						Priority:               highPrio,
						PositionInClusterQueue: 0,
						PositionInLocalQueue:   0,
					},
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:              "b",
							Namespace:         nsName,
							CreationTimestamp: metav1.NewTime(now.Add(time.Second)),
						},
						LocalQueueName:         lqNameA,
						Priority:               highPrio,
						PositionInClusterQueue: 1,
						PositionInLocalQueue:   1,
					}},
			},
		},
		"offset query parameter set": {
			clusterQueues: []*kueue.ClusterQueue{
				utiltesting.MakeClusterQueue(cqNameA).Obj(),
			},
			queues: []*kueue.LocalQueue{
				utiltesting.MakeLocalQueue(lqNameA, nsName).ClusterQueue(cqNameA).Obj(),
			},
			workloads: []*kueue.Workload{
				utiltesting.MakeWorkload("a", nsName).Queue(lqNameA).Priority(highPrio).Creation(now).Obj(),
				utiltesting.MakeWorkload("b", nsName).Queue(lqNameA).Priority(highPrio).Creation(now.Add(time.Second)).Obj(),
				utiltesting.MakeWorkload("c", nsName).Queue(lqNameA).Priority(highPrio).Creation(now.Add(time.Second * 2)).Obj(),
			},
			req: &req{
				queueName: cqNameA,
				queryParams: &visibility.PendingWorkloadOptions{
					Offset: 1,
					Limit:  constants.DefaultPendingWorkloadsLimit,
				},
			},
			wantResp: &resp{
				wantPendingWorkloads: []visibility.PendingWorkload{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:              "b",
							Namespace:         nsName,
							CreationTimestamp: metav1.NewTime(now.Add(time.Second)),
						},
						LocalQueueName:         lqNameA,
						Priority:               highPrio,
						PositionInClusterQueue: 1,
						PositionInLocalQueue:   1,
					},
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:              "c",
							Namespace:         nsName,
							CreationTimestamp: metav1.NewTime(now.Add(time.Second * 2)),
						},
						LocalQueueName:         lqNameA,
						Priority:               highPrio,
						PositionInClusterQueue: 2,
						PositionInLocalQueue:   2,
					}},
			},
		},
		"limit offset query parameters set": {
			clusterQueues: []*kueue.ClusterQueue{
				utiltesting.MakeClusterQueue(cqNameA).Obj(),
			},
			queues: []*kueue.LocalQueue{
				utiltesting.MakeLocalQueue(lqNameA, nsName).ClusterQueue(cqNameA).Obj(),
			},
			workloads: []*kueue.Workload{
				utiltesting.MakeWorkload("a", nsName).Queue(lqNameA).Priority(highPrio).Creation(now).Obj(),
				utiltesting.MakeWorkload("b", nsName).Queue(lqNameA).Priority(highPrio).Creation(now.Add(time.Second)).Obj(),
				utiltesting.MakeWorkload("c", nsName).Queue(lqNameA).Priority(highPrio).Creation(now.Add(time.Second * 2)).Obj(),
			},
			req: &req{
				queueName: cqNameA,
				queryParams: &visibility.PendingWorkloadOptions{
					Offset: 1,
					Limit:  1,
				},
			},
			wantResp: &resp{
				wantPendingWorkloads: []visibility.PendingWorkload{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:              "b",
							Namespace:         nsName,
							CreationTimestamp: metav1.NewTime(now.Add(time.Second)),
						},
						LocalQueueName:         lqNameA,
						Priority:               highPrio,
						PositionInClusterQueue: 1,
						PositionInLocalQueue:   1,
					}},
			},
		},
		"empty cluster queue": {
			clusterQueues: []*kueue.ClusterQueue{
				utiltesting.MakeClusterQueue(cqNameA).Obj(),
			},
			req: &req{
				queueName:   cqNameA,
				queryParams: defaultQueryParams,
			},
			wantResp: &resp{},
		},
		"nonexistent queue name": {
			req: &req{
				queueName:   "nonexistent-queue",
				queryParams: defaultQueryParams,
			},
			wantResp: &resp{
				wantErr: errors.NewNotFound(visibility.Resource("clusterqueue"), "nonexistent-queue"),
			},
			wantErrMatch: errors.IsNotFound,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			manager := queue.NewManager(utiltesting.NewFakeClient(), nil)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go manager.CleanUpOnContext(ctx)
			pendingWorkloadsInCqRest := NewPendingWorkloadsInCqREST(manager)
			for _, cq := range tc.clusterQueues {
				if err := manager.AddClusterQueue(ctx, cq); err != nil {
					t.Fatalf("Adding cluster queue %s: %v", cq.Name, err)
				}
			}
			for _, q := range tc.queues {
				if err := manager.AddLocalQueue(ctx, q); err != nil {
					t.Fatalf("Adding queue %q: %v", q.Name, err)
				}
			}
			for _, w := range tc.workloads {
				manager.AddOrUpdateWorkload(w)
			}

			info, err := pendingWorkloadsInCqRest.Get(ctx, tc.req.queueName, tc.req.queryParams)
			switch {
			case tc.wantErrMatch != nil:
				if !tc.wantErrMatch(err) {
					t.Errorf("Error differs: (-want,+got):\n%s", cmp.Diff(tc.wantResp.wantErr.Error(), err.Error()))
				}
			case err != nil:
				t.Error(err)
			default:
				pendingWorkloadsInfo := info.(*visibility.PendingWorkloadsSummary)
				if diff := cmp.Diff(tc.wantResp.wantPendingWorkloads, pendingWorkloadsInfo.Items, cmpopts.EquateEmpty()); diff != "" {
					t.Errorf("Pending workloads differ: (-want,+got):\n%s", diff)
				}
			}
		})
	}
}
