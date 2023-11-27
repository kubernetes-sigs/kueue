// Copyright 2023 The Kubernetes Authors.
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
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/endpoints/request"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	visibility "sigs.k8s.io/kueue/apis/visibility/v1alpha1"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/queue"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

type req struct {
	nsName      string
	queueName   string
	queryParams *visibility.PendingWorkloadOptions
}

type resp struct {
	wantErr              error
	wantPendingWorkloads []visibility.PendingWorkload
}

func TestPendingWorkloadsInLQ(t *testing.T) {
	const (
		nsNameA  = "nsA"
		nsNameB  = "nsB"
		cqNameA  = "cqA"
		cqNameB  = "cqB"
		lqNameA  = "lqA"
		lqNameB  = "lqB"
		lowPrio  = 50
		highPrio = 100
	)

	defaultQueryParams := &visibility.PendingWorkloadOptions{
		Offset: 0,
		Limit:  constants.DefaultPendingWorkloadsLimit,
	}

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
		wantResponse  map[req]*resp
	}{
		"single ClusterQueue and single LocalQueue setup with two workloads and default query parameters": {
			clusterQueues: []*kueue.ClusterQueue{
				utiltesting.MakeClusterQueue(cqNameA).Obj(),
			},
			queues: []*kueue.LocalQueue{
				utiltesting.MakeLocalQueue(lqNameA, nsNameA).ClusterQueue(cqNameA).Obj(),
			},
			workloads: []*kueue.Workload{
				utiltesting.MakeWorkload("a", nsNameA).Queue(lqNameA).Priority(highPrio).Creation(now).Obj(),
				utiltesting.MakeWorkload("b", nsNameA).Queue(lqNameA).Priority(lowPrio).Obj(),
			},
			wantResponse: map[req]*resp{
				{
					nsName:      nsNameA,
					queueName:   lqNameA,
					queryParams: defaultQueryParams,
				}: {
					wantPendingWorkloads: []visibility.PendingWorkload{
						{
							ObjectMeta: v1.ObjectMeta{
								Name:      "a",
								Namespace: nsNameA,
							},
							LocalQueueName:         lqNameA,
							Priority:               highPrio,
							PositionInClusterQueue: 0,
							PositionInLocalQueue:   0,
						},
						{
							ObjectMeta: v1.ObjectMeta{
								Name:      "b",
								Namespace: nsNameA,
							},
							LocalQueueName:         lqNameA,
							Priority:               lowPrio,
							PositionInClusterQueue: 1,
							PositionInLocalQueue:   1,
						}},
				},
			},
		},
		"single ClusterQueue and two LocalQueue setup with four workloads and default query parameters": {
			clusterQueues: []*kueue.ClusterQueue{
				utiltesting.MakeClusterQueue(cqNameA).Obj(),
			},
			queues: []*kueue.LocalQueue{
				utiltesting.MakeLocalQueue(lqNameA, nsNameA).ClusterQueue(cqNameA).Obj(),
				utiltesting.MakeLocalQueue(lqNameB, nsNameA).ClusterQueue(cqNameA).Obj(),
			},
			workloads: []*kueue.Workload{
				utiltesting.MakeWorkload("lqA-high-prio", nsNameA).Queue(lqNameA).Priority(highPrio).Creation(now).Obj(),
				utiltesting.MakeWorkload("lqA-low-prio", nsNameA).Queue(lqNameA).Priority(lowPrio).Creation(now).Obj(),
				utiltesting.MakeWorkload("lqB-high-prio", nsNameA).Queue(lqNameB).Priority(highPrio).Creation(now.Add(time.Second)).Obj(),
				utiltesting.MakeWorkload("lqB-low-prio", nsNameA).Queue(lqNameB).Priority(lowPrio).Creation(now.Add(time.Second)).Obj(),
			},
			wantResponse: map[req]*resp{
				{
					nsName:      nsNameA,
					queueName:   lqNameA,
					queryParams: defaultQueryParams,
				}: {
					wantPendingWorkloads: []visibility.PendingWorkload{
						{
							ObjectMeta: v1.ObjectMeta{
								Name:      "lqA-high-prio",
								Namespace: nsNameA,
							},
							LocalQueueName:         lqNameA,
							Priority:               highPrio,
							PositionInClusterQueue: 0,
							PositionInLocalQueue:   0,
						},
						{
							ObjectMeta: v1.ObjectMeta{
								Name:      "lqA-low-prio",
								Namespace: nsNameA,
							},
							LocalQueueName:         lqNameA,
							Priority:               lowPrio,
							PositionInClusterQueue: 2,
							PositionInLocalQueue:   1,
						}},
				},
				{
					nsName:      nsNameA,
					queueName:   lqNameB,
					queryParams: defaultQueryParams,
				}: {
					wantPendingWorkloads: []visibility.PendingWorkload{
						{
							ObjectMeta: v1.ObjectMeta{
								Name:      "lqB-high-prio",
								Namespace: nsNameA,
							},
							LocalQueueName:         lqNameB,
							Priority:               highPrio,
							PositionInClusterQueue: 1,
							PositionInLocalQueue:   0,
						},
						{
							ObjectMeta: v1.ObjectMeta{
								Name:      "lqB-low-prio",
								Namespace: nsNameA,
							},
							LocalQueueName:         lqNameB,
							Priority:               lowPrio,
							PositionInClusterQueue: 3,
							PositionInLocalQueue:   1,
						}},
				},
			},
		},
		"two Namespaces, two ClusterQueue and two LocalQueue setup with four workloads and default query parameters": {
			clusterQueues: []*kueue.ClusterQueue{
				utiltesting.MakeClusterQueue(cqNameA).Obj(),
				utiltesting.MakeClusterQueue(cqNameB).Obj(),
			},
			queues: []*kueue.LocalQueue{
				utiltesting.MakeLocalQueue(lqNameA, nsNameA).ClusterQueue(cqNameA).Obj(),
				utiltesting.MakeLocalQueue(lqNameB, nsNameB).ClusterQueue(cqNameB).Obj(),
			},
			workloads: []*kueue.Workload{
				utiltesting.MakeWorkload("lqA-high-prio", nsNameA).Queue(lqNameA).Priority(highPrio).Creation(now).Obj(),
				utiltesting.MakeWorkload("lqA-low-prio", nsNameA).Queue(lqNameA).Priority(lowPrio).Creation(now).Obj(),
				utiltesting.MakeWorkload("lqB-high-prio", nsNameB).Queue(lqNameB).Priority(highPrio).Creation(now.Add(time.Second)).Obj(),
				utiltesting.MakeWorkload("lqB-low-prio", nsNameB).Queue(lqNameB).Priority(lowPrio).Creation(now.Add(time.Second)).Obj(),
			},
			wantResponse: map[req]*resp{
				{
					nsName:      nsNameA,
					queueName:   lqNameA,
					queryParams: defaultQueryParams,
				}: {
					wantPendingWorkloads: []visibility.PendingWorkload{
						{
							ObjectMeta: v1.ObjectMeta{
								Name:      "lqA-high-prio",
								Namespace: nsNameA,
							},
							LocalQueueName:         lqNameA,
							Priority:               highPrio,
							PositionInClusterQueue: 0,
							PositionInLocalQueue:   0,
						},
						{
							ObjectMeta: v1.ObjectMeta{
								Name:      "lqA-low-prio",
								Namespace: nsNameA,
							},
							LocalQueueName:         lqNameA,
							Priority:               lowPrio,
							PositionInClusterQueue: 1,
							PositionInLocalQueue:   1,
						}},
				},
				{
					nsName:      nsNameB,
					queueName:   lqNameB,
					queryParams: defaultQueryParams,
				}: {
					wantPendingWorkloads: []visibility.PendingWorkload{
						{
							ObjectMeta: v1.ObjectMeta{
								Name:      "lqB-high-prio",
								Namespace: nsNameB,
							},
							LocalQueueName:         lqNameB,
							Priority:               highPrio,
							PositionInClusterQueue: 0,
							PositionInLocalQueue:   0,
						},
						{
							ObjectMeta: v1.ObjectMeta{
								Name:      "lqB-low-prio",
								Namespace: nsNameB,
							},
							LocalQueueName:         lqNameB,
							Priority:               lowPrio,
							PositionInClusterQueue: 1,
							PositionInLocalQueue:   1,
						}},
				},
			},
		},
		"valid query parameters set": {
			clusterQueues: []*kueue.ClusterQueue{
				utiltesting.MakeClusterQueue(cqNameA).Obj(),
			},
			queues: []*kueue.LocalQueue{
				utiltesting.MakeLocalQueue(lqNameA, nsNameA).ClusterQueue(cqNameA).Obj(),
			},
			workloads: []*kueue.Workload{
				utiltesting.MakeWorkload("a", nsNameA).Queue(lqNameA).Priority(highPrio).Creation(now).Obj(),
				utiltesting.MakeWorkload("b", nsNameA).Queue(lqNameA).Priority(highPrio).Creation(now.Add(time.Second)).Obj(),
				utiltesting.MakeWorkload("c", nsNameA).Queue(lqNameA).Priority(highPrio).Creation(now.Add(time.Second * 2)).Obj(),
			},
			wantResponse: map[req]*resp{
				// Only limit is set
				{
					nsName:    nsNameA,
					queueName: lqNameA,
					queryParams: &visibility.PendingWorkloadOptions{
						Limit: 2,
					},
				}: {
					wantPendingWorkloads: []visibility.PendingWorkload{
						{
							ObjectMeta: v1.ObjectMeta{
								Name:      "a",
								Namespace: nsNameA,
							},
							LocalQueueName:         lqNameA,
							Priority:               highPrio,
							PositionInClusterQueue: 0,
							PositionInLocalQueue:   0,
						},
						{
							ObjectMeta: v1.ObjectMeta{
								Name:      "b",
								Namespace: nsNameA,
							},
							LocalQueueName:         lqNameA,
							Priority:               highPrio,
							PositionInClusterQueue: 1,
							PositionInLocalQueue:   1,
						}},
				},
				// Only offset is set
				{
					nsName:    nsNameA,
					queueName: lqNameA,
					queryParams: &visibility.PendingWorkloadOptions{
						Offset: 1,
						Limit:  constants.DefaultPendingWorkloadsLimit,
					},
				}: {
					wantPendingWorkloads: []visibility.PendingWorkload{
						{
							ObjectMeta: v1.ObjectMeta{
								Name:      "b",
								Namespace: nsNameA,
							},
							LocalQueueName:         lqNameA,
							Priority:               highPrio,
							PositionInClusterQueue: 1,
							PositionInLocalQueue:   1,
						},
						{
							ObjectMeta: v1.ObjectMeta{
								Name:      "c",
								Namespace: nsNameA,
							},
							LocalQueueName:         lqNameA,
							Priority:               highPrio,
							PositionInClusterQueue: 2,
							PositionInLocalQueue:   2,
						}},
				},
				// Both limit and offset are set
				{
					nsName:    nsNameA,
					queueName: lqNameA,
					queryParams: &visibility.PendingWorkloadOptions{
						Offset: 1,
						Limit:  1,
					},
				}: {
					wantPendingWorkloads: []visibility.PendingWorkload{
						{
							ObjectMeta: v1.ObjectMeta{
								Name:      "b",
								Namespace: nsNameA,
							},
							LocalQueueName:         lqNameA,
							Priority:               highPrio,
							PositionInClusterQueue: 1,
							PositionInLocalQueue:   1,
						}},
				},
			},
		},
		"invalid queue name": {
			wantResponse: map[req]*resp{
				{
					queueName:   "invalid",
					queryParams: defaultQueryParams,
				}: {
					wantErr: errQueueDoesNotExist,
				},
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			manager := queue.NewManager(utiltesting.NewFakeClient(), nil)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go manager.CleanUpOnContext(ctx)
			pendingWorkloadsInLqRest := NewPendingWorkloadsInLqREST(manager)
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

			for req, resp := range tc.wantResponse {
				ctx := request.WithNamespace(ctx, req.nsName)
				info, err := pendingWorkloadsInLqRest.Get(ctx, req.queueName, req.queryParams)
				if err != nil {
					if diff := cmp.Diff(resp.wantErr, err, cmpopts.EquateErrors()); diff != "" {
						t.Errorf("Error differs: (-want,+got):\n%s", diff)
					}
				} else {
					pendingWorkloadsInfo := info.(*visibility.PendingWorkloadsSummary)
					if diff := cmp.Diff(resp.wantPendingWorkloads, pendingWorkloadsInfo.Items, cmpopts.IgnoreTypes(metav1.TypeMeta{})); diff != "" {
						t.Errorf("Pending workloads differ: (-want,+got):\n%s", diff)
					}
				}
			}
		})
	}
}
