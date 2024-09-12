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
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/utils/ptr"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/queue"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingmetrics "sigs.k8s.io/kueue/pkg/util/testing/metrics"
)

func TestUpdateCqStatusIfChanged(t *testing.T) {
	cqName := "test-cq"
	lqName := "test-lq"
	defaultWls := &kueue.WorkloadList{
		Items: []kueue.Workload{
			*utiltesting.MakeWorkload("alpha", "").Queue(lqName).Obj(),
			*utiltesting.MakeWorkload("beta", "").Queue(lqName).Obj(),
		},
	}

	testCases := map[string]struct {
		insertCqIntoCache   bool
		insertCqIntoManager bool
		cqStatus            kueue.ClusterQueueStatus
		newConditionStatus  metav1.ConditionStatus
		newReason           string
		newMessage          string
		newWl               *kueue.Workload
		wantCqStatus        kueue.ClusterQueueStatus
		wantError           error
	}{
		"empty ClusterQueueStatus": {
			insertCqIntoCache:   true,
			insertCqIntoManager: true,
			cqStatus:            kueue.ClusterQueueStatus{},
			newConditionStatus:  metav1.ConditionFalse,
			newReason:           "FlavorNotFound",
			newMessage:          "Can't admit new workloads; some flavors are not found",
			wantCqStatus: kueue.ClusterQueueStatus{
				PendingWorkloads: int32(len(defaultWls.Items)),
				Conditions: []metav1.Condition{{
					Type:               kueue.ClusterQueueActive,
					Status:             metav1.ConditionFalse,
					Reason:             "FlavorNotFound",
					Message:            "Can't admit new workloads; some flavors are not found",
					ObservedGeneration: 1,
				}},
			},
		},
		"same condition status": {
			insertCqIntoCache:   true,
			insertCqIntoManager: true,
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
					Type:               kueue.ClusterQueueActive,
					Status:             metav1.ConditionTrue,
					Reason:             "Ready",
					Message:            "Can admit new workloads",
					ObservedGeneration: 1,
				}},
			},
		},
		"same condition status with different reason and message": {
			insertCqIntoCache:   true,
			insertCqIntoManager: true,
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
					Type:               kueue.ClusterQueueActive,
					Status:             metav1.ConditionFalse,
					Reason:             "Terminating",
					Message:            "Can't admit new workloads; clusterQueue is terminating",
					ObservedGeneration: 1,
				}},
			},
		},
		"different condition status": {
			insertCqIntoCache:   true,
			insertCqIntoManager: true,
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
					Type:               kueue.ClusterQueueActive,
					Status:             metav1.ConditionTrue,
					Reason:             "Ready",
					Message:            "Can admit new workloads",
					ObservedGeneration: 1,
				}},
			},
		},
		"different pendingWorkloads with same condition status": {
			insertCqIntoCache:   true,
			insertCqIntoManager: true,
			cqStatus: kueue.ClusterQueueStatus{
				PendingWorkloads: int32(len(defaultWls.Items)),
				Conditions: []metav1.Condition{{
					Type:    kueue.ClusterQueueActive,
					Status:  metav1.ConditionTrue,
					Reason:  "Ready",
					Message: "Can admit new workloads",
				}},
			},
			newWl:              utiltesting.MakeWorkload("gamma", "").Queue(lqName).Obj(),
			newConditionStatus: metav1.ConditionTrue,
			newReason:          "Ready",
			newMessage:         "Can admit new workloads",
			wantCqStatus: kueue.ClusterQueueStatus{
				PendingWorkloads: int32(len(defaultWls.Items) + 1),
				Conditions: []metav1.Condition{{
					Type:               kueue.ClusterQueueActive,
					Status:             metav1.ConditionTrue,
					Reason:             "Ready",
					Message:            "Can admit new workloads",
					ObservedGeneration: 1,
				}},
			},
		},
		"cluster queue does not exist on manager": {
			wantError: queue.ErrClusterQueueDoesNotExist,
		},
		"cluster queue does not exist on cache": {
			insertCqIntoManager: true,
			wantError:           cache.ErrCqNotFound,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			cq := utiltesting.MakeClusterQueue(cqName).
				QueueingStrategy(kueue.StrictFIFO).
				Generation(1).
				Obj()
			cq.Status = tc.cqStatus
			lq := utiltesting.MakeLocalQueue(lqName, "").
				ClusterQueue(cqName).Obj()
			ctx, log := utiltesting.ContextWithLog(t)

			cl := utiltesting.NewClientBuilder().WithLists(defaultWls).WithObjects(lq, cq).WithStatusSubresource(lq, cq).
				Build()
			cqCache := cache.New(cl)
			qManager := queue.NewManager(cl, cqCache)
			if tc.insertCqIntoCache {
				if err := cqCache.AddClusterQueue(ctx, cq); err != nil {
					t.Fatalf("Inserting clusterQueue in cache: %v", err)
				}
			}
			if tc.insertCqIntoManager {
				if err := qManager.AddClusterQueue(ctx, cq); err != nil {
					t.Fatalf("Inserting clusterQueue in manager: %v", err)
				}
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
			gotError := r.updateCqStatusIfChanged(ctx, cq, tc.newConditionStatus, tc.newReason, tc.newMessage)
			if diff := cmp.Diff(tc.wantError, gotError, cmpopts.EquateErrors()); len(diff) != 0 {
				t.Errorf("Unexpected error (-want/+got):\n%s", diff)
			}
			configCmpOpts := []cmp.Option{
				cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime"),
				cmpopts.IgnoreFields(kueue.ClusterQueuePendingWorkloadsStatus{}, "LastChangeTime"),
				cmpopts.EquateEmpty(),
			}
			if diff := cmp.Diff(tc.wantCqStatus, cq.Status, configCmpOpts...); len(diff) != 0 {
				t.Errorf("unexpected ClusterQueueStatus (-want,+got):\n%s", diff)
			}
		})
	}
}

type cqMetrics struct {
	NominalDPs   []testingmetrics.MetricDataPoint
	BorrowingDPs []testingmetrics.MetricDataPoint
	UsageDPs     []testingmetrics.MetricDataPoint
}

func allMetricsForQueue(name string) cqMetrics {
	return cqMetrics{
		NominalDPs:   testingmetrics.CollectFilteredGaugeVec(metrics.ClusterQueueResourceNominalQuota, map[string]string{"cluster_queue": name}),
		BorrowingDPs: testingmetrics.CollectFilteredGaugeVec(metrics.ClusterQueueResourceBorrowingLimit, map[string]string{"cluster_queue": name}),
		UsageDPs:     testingmetrics.CollectFilteredGaugeVec(metrics.ClusterQueueResourceReservations, map[string]string{"cluster_queue": name}),
	}
}

func resourceDataPoint(cohort, name, flavor, res string, v float64) testingmetrics.MetricDataPoint {
	return testingmetrics.MetricDataPoint{
		Labels: map[string]string{
			"cohort":        cohort,
			"cluster_queue": name,
			"flavor":        flavor,
			"resource":      res,
		},
		Value: v,
	}
}

func TestRecordResourceMetrics(t *testing.T) {
	baseQueue := &kueue.ClusterQueue{
		ObjectMeta: metav1.ObjectMeta{
			Name: "name",
		},
		Spec: kueue.ClusterQueueSpec{
			Cohort: "cohort",
			ResourceGroups: []kueue.ResourceGroup{
				{
					CoveredResources: []corev1.ResourceName{corev1.ResourceCPU},
					Flavors: []kueue.FlavorQuotas{
						{
							Name: "flavor",
							Resources: []kueue.ResourceQuota{
								{
									Name:           corev1.ResourceCPU,
									NominalQuota:   resource.MustParse("1"),
									BorrowingLimit: ptr.To(resource.MustParse("2")),
								},
							},
						},
					},
				},
			},
		},
		Status: kueue.ClusterQueueStatus{
			FlavorsReservation: []kueue.FlavorUsage{
				{
					Name: "flavor",
					Resources: []kueue.ResourceUsage{
						{
							Name:     corev1.ResourceCPU,
							Total:    resource.MustParse("2"),
							Borrowed: resource.MustParse("1"),
						},
					},
				},
			},
		},
	}

	testCases := map[string]struct {
		queue              *kueue.ClusterQueue
		wantMetrics        cqMetrics
		updatedQueue       *kueue.ClusterQueue
		wantUpdatedMetrics cqMetrics
	}{
		"no change": {
			queue: baseQueue.DeepCopy(),
			wantMetrics: cqMetrics{
				NominalDPs: []testingmetrics.MetricDataPoint{
					resourceDataPoint("cohort", "name", "flavor", string(corev1.ResourceCPU), 1),
				},
				BorrowingDPs: []testingmetrics.MetricDataPoint{
					resourceDataPoint("cohort", "name", "flavor", string(corev1.ResourceCPU), 2),
				},
				UsageDPs: []testingmetrics.MetricDataPoint{
					resourceDataPoint("cohort", "name", "flavor", string(corev1.ResourceCPU), 2),
				},
			},
		},
		"update-in-place": {
			queue: baseQueue.DeepCopy(),
			wantMetrics: cqMetrics{
				NominalDPs: []testingmetrics.MetricDataPoint{
					resourceDataPoint("cohort", "name", "flavor", string(corev1.ResourceCPU), 1),
				},
				BorrowingDPs: []testingmetrics.MetricDataPoint{
					resourceDataPoint("cohort", "name", "flavor", string(corev1.ResourceCPU), 2),
				},
				UsageDPs: []testingmetrics.MetricDataPoint{
					resourceDataPoint("cohort", "name", "flavor", string(corev1.ResourceCPU), 2),
				},
			},
			updatedQueue: func() *kueue.ClusterQueue {
				ret := baseQueue.DeepCopy()
				ret.Spec.ResourceGroups[0].Flavors[0].Resources[0].NominalQuota = resource.MustParse("2")
				ret.Spec.ResourceGroups[0].Flavors[0].Resources[0].BorrowingLimit = ptr.To(resource.MustParse("1"))
				ret.Status.FlavorsReservation[0].Resources[0].Total = resource.MustParse("3")
				return ret
			}(),
			wantUpdatedMetrics: cqMetrics{
				NominalDPs: []testingmetrics.MetricDataPoint{
					resourceDataPoint("cohort", "name", "flavor", string(corev1.ResourceCPU), 2),
				},
				BorrowingDPs: []testingmetrics.MetricDataPoint{
					resourceDataPoint("cohort", "name", "flavor", string(corev1.ResourceCPU), 1),
				},
				UsageDPs: []testingmetrics.MetricDataPoint{
					resourceDataPoint("cohort", "name", "flavor", string(corev1.ResourceCPU), 3),
				},
			},
		},
		"change-cohort": {
			queue: baseQueue.DeepCopy(),
			wantMetrics: cqMetrics{
				NominalDPs: []testingmetrics.MetricDataPoint{
					resourceDataPoint("cohort", "name", "flavor", string(corev1.ResourceCPU), 1),
				},
				BorrowingDPs: []testingmetrics.MetricDataPoint{
					resourceDataPoint("cohort", "name", "flavor", string(corev1.ResourceCPU), 2),
				},
				UsageDPs: []testingmetrics.MetricDataPoint{
					resourceDataPoint("cohort", "name", "flavor", string(corev1.ResourceCPU), 2),
				},
			},
			updatedQueue: func() *kueue.ClusterQueue {
				ret := baseQueue.DeepCopy()
				ret.Spec.Cohort = "cohort2"
				return ret
			}(),
			wantUpdatedMetrics: cqMetrics{
				NominalDPs: []testingmetrics.MetricDataPoint{
					resourceDataPoint("cohort2", "name", "flavor", string(corev1.ResourceCPU), 1),
				},
				BorrowingDPs: []testingmetrics.MetricDataPoint{
					resourceDataPoint("cohort2", "name", "flavor", string(corev1.ResourceCPU), 2),
				},
				UsageDPs: []testingmetrics.MetricDataPoint{
					resourceDataPoint("cohort2", "name", "flavor", string(corev1.ResourceCPU), 2),
				},
			},
		},
		"add-rm-flavor": {
			queue: baseQueue.DeepCopy(),
			wantMetrics: cqMetrics{
				NominalDPs: []testingmetrics.MetricDataPoint{
					resourceDataPoint("cohort", "name", "flavor", string(corev1.ResourceCPU), 1),
				},
				BorrowingDPs: []testingmetrics.MetricDataPoint{
					resourceDataPoint("cohort", "name", "flavor", string(corev1.ResourceCPU), 2),
				},
				UsageDPs: []testingmetrics.MetricDataPoint{
					resourceDataPoint("cohort", "name", "flavor", string(corev1.ResourceCPU), 2),
				},
			},
			updatedQueue: func() *kueue.ClusterQueue {
				ret := baseQueue.DeepCopy()
				ret.Spec.ResourceGroups[0].Flavors[0].Name = "flavor2"
				ret.Status.FlavorsReservation[0].Name = "flavor2"
				return ret
			}(),
			wantUpdatedMetrics: cqMetrics{
				NominalDPs: []testingmetrics.MetricDataPoint{
					resourceDataPoint("cohort", "name", "flavor2", string(corev1.ResourceCPU), 1),
				},
				BorrowingDPs: []testingmetrics.MetricDataPoint{
					resourceDataPoint("cohort", "name", "flavor2", string(corev1.ResourceCPU), 2),
				},
				UsageDPs: []testingmetrics.MetricDataPoint{
					resourceDataPoint("cohort", "name", "flavor2", string(corev1.ResourceCPU), 2),
				},
			},
		},
		"add-rm-resource": {
			queue: baseQueue.DeepCopy(),
			wantMetrics: cqMetrics{
				NominalDPs: []testingmetrics.MetricDataPoint{
					resourceDataPoint("cohort", "name", "flavor", string(corev1.ResourceCPU), 1),
				},
				BorrowingDPs: []testingmetrics.MetricDataPoint{
					resourceDataPoint("cohort", "name", "flavor", string(corev1.ResourceCPU), 2),
				},
				UsageDPs: []testingmetrics.MetricDataPoint{
					resourceDataPoint("cohort", "name", "flavor", string(corev1.ResourceCPU), 2),
				},
			},
			updatedQueue: func() *kueue.ClusterQueue {
				ret := baseQueue.DeepCopy()
				ret.Spec.ResourceGroups[0].Flavors[0].Resources[0].Name = corev1.ResourceMemory
				ret.Status.FlavorsReservation[0].Resources[0].Name = corev1.ResourceMemory
				return ret
			}(),
			wantUpdatedMetrics: cqMetrics{
				NominalDPs: []testingmetrics.MetricDataPoint{
					resourceDataPoint("cohort", "name", "flavor", string(corev1.ResourceMemory), 1),
				},
				BorrowingDPs: []testingmetrics.MetricDataPoint{
					resourceDataPoint("cohort", "name", "flavor", string(corev1.ResourceMemory), 2),
				},
				UsageDPs: []testingmetrics.MetricDataPoint{
					resourceDataPoint("cohort", "name", "flavor", string(corev1.ResourceMemory), 2),
				},
			},
		},
		"drop-usage": {
			queue: baseQueue.DeepCopy(),
			wantMetrics: cqMetrics{
				NominalDPs: []testingmetrics.MetricDataPoint{
					resourceDataPoint("cohort", "name", "flavor", string(corev1.ResourceCPU), 1),
				},
				BorrowingDPs: []testingmetrics.MetricDataPoint{
					resourceDataPoint("cohort", "name", "flavor", string(corev1.ResourceCPU), 2),
				},
				UsageDPs: []testingmetrics.MetricDataPoint{
					resourceDataPoint("cohort", "name", "flavor", string(corev1.ResourceCPU), 2),
				},
			},
			updatedQueue: func() *kueue.ClusterQueue {
				ret := baseQueue.DeepCopy()
				ret.Status.FlavorsReservation = nil
				return ret
			}(),
			wantUpdatedMetrics: cqMetrics{
				NominalDPs: []testingmetrics.MetricDataPoint{
					resourceDataPoint("cohort", "name", "flavor", string(corev1.ResourceCPU), 1),
				},
				BorrowingDPs: []testingmetrics.MetricDataPoint{
					resourceDataPoint("cohort", "name", "flavor", string(corev1.ResourceCPU), 2),
				},
			},
		},
	}

	opts := []cmp.Option{
		cmpopts.SortSlices(func(a, b testingmetrics.MetricDataPoint) bool { return a.Less(&b) }),
		cmpopts.EquateEmpty(),
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			recordResourceMetrics(tc.queue)
			gotMetrics := allMetricsForQueue(tc.queue.Name)
			if diff := cmp.Diff(tc.wantMetrics, gotMetrics, opts...); len(diff) != 0 {
				t.Errorf("Unexpected metrics (-want,+got):\n%s", diff)
			}

			if tc.updatedQueue != nil {
				updateResourceMetrics(tc.queue, tc.updatedQueue)
				gotMetricsAfterUpdate := allMetricsForQueue(tc.queue.Name)
				if diff := cmp.Diff(tc.wantUpdatedMetrics, gotMetricsAfterUpdate, opts...); len(diff) != 0 {
					t.Errorf("Unexpected metrics (-want,+got):\n%s", diff)
				}
			}

			metrics.ClearClusterQueueResourceMetrics(tc.queue.Name)
			endMetrics := allMetricsForQueue(tc.queue.Name)
			if len(endMetrics.NominalDPs) != 0 || len(endMetrics.BorrowingDPs) != 0 || len(endMetrics.UsageDPs) != 0 {
				t.Errorf("Unexpected metrics after cleanup:\n%v", endMetrics)
			}
		})
	}
}

func TestClusterQueuePendingWorkloadsStatus(t *testing.T) {
	cqName := "test-cq"
	lqName := "test-lq"
	const lowPrio, highPrio = 0, 100
	defaultWls := &kueue.WorkloadList{
		Items: []kueue.Workload{
			*utiltesting.MakeWorkload("one", "").Queue(lqName).Priority(highPrio).Obj(),
			*utiltesting.MakeWorkload("two", "").Queue(lqName).Priority(lowPrio).Obj(),
		},
	}
	testCases := map[string]struct {
		queueVisibilityUpdateInterval        time.Duration
		queueVisibilityClusterQueuesMaxCount int32
		wantPendingWorkloadsStatus           *kueue.ClusterQueuePendingWorkloadsStatus
		enableQueueVisibility                bool
	}{
		"queue visibility is disabled": {},
		"queue visibility is disabled but maxcount is provided": {
			queueVisibilityClusterQueuesMaxCount: 2,
		},
		"queue visibility is enabled": {
			queueVisibilityClusterQueuesMaxCount: 2,
			queueVisibilityUpdateInterval:        10 * time.Millisecond,
			enableQueueVisibility:                true,
			wantPendingWorkloadsStatus: &kueue.ClusterQueuePendingWorkloadsStatus{
				Head: []kueue.ClusterQueuePendingWorkload{
					{Name: "one"}, {Name: "two"},
				},
			},
		},
		"verify the head of pending workloads when the number of pending workloads exceeds MaxCount": {
			queueVisibilityClusterQueuesMaxCount: 1,
			queueVisibilityUpdateInterval:        10 * time.Millisecond,
			enableQueueVisibility:                true,
			wantPendingWorkloadsStatus: &kueue.ClusterQueuePendingWorkloadsStatus{
				Head: []kueue.ClusterQueuePendingWorkload{
					{Name: "one"},
				},
			},
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.QueueVisibility, tc.enableQueueVisibility)

			cq := utiltesting.MakeClusterQueue(cqName).
				QueueingStrategy(kueue.StrictFIFO).Obj()
			lq := utiltesting.MakeLocalQueue(lqName, "").
				ClusterQueue(cqName).Obj()
			ctx := context.Background()

			cl := utiltesting.NewClientBuilder().WithLists(defaultWls).WithObjects(lq, cq).WithStatusSubresource(lq, cq).
				Build()
			cCache := cache.New(cl)
			qManager := queue.NewManager(cl, cCache)
			if err := qManager.AddClusterQueue(ctx, cq); err != nil {
				t.Fatalf("Inserting clusterQueue in manager: %v", err)
			}
			if err := qManager.AddLocalQueue(ctx, lq); err != nil {
				t.Fatalf("Inserting localQueue in manager: %v", err)
			}

			r := NewClusterQueueReconciler(
				cl,
				qManager,
				cCache,
				WithQueueVisibilityUpdateInterval(tc.queueVisibilityUpdateInterval),
				WithQueueVisibilityClusterQueuesMaxCount(tc.queueVisibilityClusterQueuesMaxCount),
			)

			go func() {
				if err := r.Start(ctx); err != nil {
					t.Errorf("error starting the cluster queue reconciler: %v", err)
				}
			}()

			diff := ""
			if err := wait.PollUntilContextTimeout(ctx, time.Second, 10*time.Second, false, func(ctx context.Context) (done bool, err error) {
				diff = cmp.Diff(tc.wantPendingWorkloadsStatus, r.getWorkloadsStatus(cq), cmpopts.IgnoreFields(kueue.ClusterQueuePendingWorkloadsStatus{}, "LastChangeTime"))
				return diff == "", nil
			}); err != nil {
				t.Fatalf("Failed to get the expected pending workloads status, last diff=%s", diff)
			}
		})
	}
}
