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

package core

import (
	"context"
	"testing"

	"github.com/go-logr/logr/testr"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/queue"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

func TestUpdateCqStatusIfChanged(t *testing.T) {
	cqName := "test-cq"
	lqName := "test-lq"
	defaultNamespace := "default"
	defaultWls := &kueue.WorkloadList{
		Items: []kueue.Workload{
			*utiltesting.MakeWorkload("alpha", defaultNamespace).Queue(lqName).Obj(),
			*utiltesting.MakeWorkload("beta", defaultNamespace).Queue(lqName).Obj(),
		},
	}

	testCases := map[string]struct {
		cqStatus           kueue.ClusterQueueStatus
		newConditionStatus metav1.ConditionStatus
		newReason          string
		newMessage         string
		newWl              *kueue.Workload
		wantCqStatus       kueue.ClusterQueueStatus
	}{
		"empty ClusterQueueStatus": {
			cqStatus:           kueue.ClusterQueueStatus{},
			newConditionStatus: metav1.ConditionFalse,
			newReason:          "FlavorNotFound",
			newMessage:         "Can't admit new workloads; some flavors are not found",
			wantCqStatus: kueue.ClusterQueueStatus{
				PendingWorkloads: int32(len(defaultWls.Items)),
				Conditions: []metav1.Condition{{
					Type:    kueue.ClusterQueueActive,
					Status:  metav1.ConditionFalse,
					Reason:  "FlavorNotFound",
					Message: "Can't admit new workloads; some flavors are not found",
				}},
			},
		},
		"same condition status": {
			cqStatus: kueue.ClusterQueueStatus{
				PendingWorkloads: int32(len(defaultWls.Items)),
				Conditions: []metav1.Condition{{
					Type:    kueue.ClusterQueueActive,
					Status:  metav1.ConditionTrue,
					Reason:  "Ready",
					Message: "Can admit new workloads",
				}},
			},
			newConditionStatus: metav1.ConditionTrue,
			newReason:          "Ready",
			newMessage:         "Can admit new workloads",
			wantCqStatus: kueue.ClusterQueueStatus{
				PendingWorkloads: int32(len(defaultWls.Items)),
				Conditions: []metav1.Condition{{
					Type:    kueue.ClusterQueueActive,
					Status:  metav1.ConditionTrue,
					Reason:  "Ready",
					Message: "Can admit new workloads",
				}},
			},
		},
		"same condition status with different reason and message": {
			cqStatus: kueue.ClusterQueueStatus{
				PendingWorkloads: int32(len(defaultWls.Items)),
				Conditions: []metav1.Condition{{
					Type:    kueue.ClusterQueueActive,
					Status:  metav1.ConditionFalse,
					Reason:  "FlavorNotFound",
					Message: "Can't admit new workloads; Can't admit new workloads; some flavors are not found",
				}},
			},
			newConditionStatus: metav1.ConditionFalse,
			newReason:          "Terminating",
			newMessage:         "Can't admit new workloads; clusterQueue is terminating",
			wantCqStatus: kueue.ClusterQueueStatus{
				PendingWorkloads: int32(len(defaultWls.Items)),
				Conditions: []metav1.Condition{{
					Type:    kueue.ClusterQueueActive,
					Status:  metav1.ConditionFalse,
					Reason:  "Terminating",
					Message: "Can't admit new workloads; clusterQueue is terminating",
				}},
			},
		},
		"different condition status": {
			cqStatus: kueue.ClusterQueueStatus{
				PendingWorkloads: int32(len(defaultWls.Items)),
				Conditions: []metav1.Condition{{
					Type:    kueue.ClusterQueueActive,
					Status:  metav1.ConditionFalse,
					Reason:  "FlavorNotFound",
					Message: "Can't admit new workloads; some flavors are not found",
				}},
			},
			newConditionStatus: metav1.ConditionTrue,
			newReason:          "Ready",
			newMessage:         "Can admit new workloads",
			wantCqStatus: kueue.ClusterQueueStatus{
				PendingWorkloads: int32(len(defaultWls.Items)),
				Conditions: []metav1.Condition{{
					Type:    kueue.ClusterQueueActive,
					Status:  metav1.ConditionTrue,
					Reason:  "Ready",
					Message: "Can admit new workloads",
				}},
			},
		},
		"different pendingWorkloads with same condition status": {
			cqStatus: kueue.ClusterQueueStatus{
				PendingWorkloads: int32(len(defaultWls.Items)),
				Conditions: []metav1.Condition{{
					Type:    kueue.ClusterQueueActive,
					Status:  metav1.ConditionTrue,
					Reason:  "Ready",
					Message: "Can admit new workloads",
				}},
			},
			newWl:              utiltesting.MakeWorkload("gamma", defaultNamespace).Queue(lqName).Obj(),
			newConditionStatus: metav1.ConditionTrue,
			newReason:          "Ready",
			newMessage:         "Can admit new workloads",
			wantCqStatus: kueue.ClusterQueueStatus{
				PendingWorkloads: int32(len(defaultWls.Items) + 1),
				Conditions: []metav1.Condition{{
					Type:    kueue.ClusterQueueActive,
					Status:  metav1.ConditionTrue,
					Reason:  "Ready",
					Message: "Can admit new workloads",
				}},
			},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			cq := utiltesting.MakeClusterQueue(cqName).
				QueueingStrategy(kueue.StrictFIFO).Obj()
			cq.Status = tc.cqStatus
			lq := utiltesting.MakeLocalQueue(lqName, defaultNamespace).
				ClusterQueue(cqName).Obj()
			log := testr.NewWithOptions(t, testr.Options{
				Verbosity: 2,
			})
			ctx := ctrl.LoggerInto(context.Background(), log)

			cl := utiltesting.NewClientBuilder().WithLists(defaultWls).WithObjects(lq, cq).
				WithStatusSubresource(lq, cq).Build()
			cqCache := cache.New(cl)
			qManager := queue.NewManager(cl, cqCache)
			if err := cqCache.AddClusterQueue(ctx, cq); err != nil {
				t.Fatalf("Inserting clusterQueue in cache: %v", err)
			}
			if err := qManager.AddClusterQueue(ctx, cq); err != nil {
				t.Fatalf("Inserting clusterQueue in manager: %v", err)
			}
			if err := qManager.AddLocalQueue(ctx, lq); err != nil {
				t.Fatalf("Inserting localQueue in manager: %v", err)
			}
			for _, wl := range defaultWls.Items {
				cqCache.AddOrUpdateWorkload(&wl)
			}
			r := &ClusterQueueReconciler{
				client:   cl,
				log:      log,
				cache:    cqCache,
				qManager: qManager,
			}
			if tc.newWl != nil {
				r.qManager.AddOrUpdateWorkload(tc.newWl)
			}
			err := r.updateCqStatusIfChanged(ctx, cq, tc.newConditionStatus, tc.newReason, tc.newMessage)
			if err != nil {
				t.Errorf("Updating ClusterQueueStatus: %v", err)
			}
			if diff := cmp.Diff(tc.wantCqStatus, cq.Status,
				cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime"),
				cmpopts.EquateEmpty()); len(diff) != 0 {
				t.Errorf("unexpected ClusterQueueStatus (-want,+got):\n%s", diff)
			}
		})
	}
}
