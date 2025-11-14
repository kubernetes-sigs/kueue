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

package core

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/onsi/gomega"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"

	"sigs.k8s.io/controller-runtime/pkg/event"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingmetrics "sigs.k8s.io/kueue/pkg/util/testing/metrics"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestUpdateCqStatusIfChanged(t *testing.T) {
	cqName := "test-cq"
	lqName := "test-lq"
	defaultWls := &kueue.WorkloadList{
		Items: []kueue.Workload{
			*utiltestingapi.MakeWorkload("alpha", "").Queue(kueue.LocalQueueName(lqName)).Obj(),
			*utiltestingapi.MakeWorkload("beta", "").Queue(kueue.LocalQueueName(lqName)).Obj(),
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
			newWl:              utiltestingapi.MakeWorkload("gamma", "").Queue(kueue.LocalQueueName(lqName)).Obj(),
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
			wantError: qcache.ErrClusterQueueDoesNotExist,
		},
		"cluster queue does not exist on cache": {
			insertCqIntoManager: true,
			wantError:           schdcache.ErrCqNotFound,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			cq := utiltestingapi.MakeClusterQueue(cqName).
				QueueingStrategy(kueue.StrictFIFO).
				Generation(1).
				Obj()
			cq.Status = tc.cqStatus
			lq := utiltestingapi.MakeLocalQueue(lqName, "").
				ClusterQueue(cqName).Obj()
			ctx, log := utiltesting.ContextWithLog(t)

			cl := utiltesting.NewClientBuilder().WithLists(defaultWls).WithObjects(lq, cq).WithStatusSubresource(lq, cq).
				Build()
			cqCache := schdcache.New(cl)
			qManager := qcache.NewManager(cl, cqCache)
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
				cqCache.AddOrUpdateWorkload(log, &wl)
			}
			r := &ClusterQueueReconciler{
				client:   cl,
				log:      log,
				cache:    cqCache,
				qManager: qManager,
			}
			if tc.newWl != nil {
				if err := r.qManager.AddOrUpdateWorkload(tc.newWl); err != nil {
					t.Fatalf("Failed to add or update workload : %v", err)
				}
			}
			gotError := r.updateCqStatusIfChanged(ctx, cq, tc.newConditionStatus, tc.newReason, tc.newMessage)
			if diff := cmp.Diff(tc.wantError, gotError, cmpopts.EquateErrors()); len(diff) != 0 {
				t.Errorf("Unexpected error (-want/+got):\n%s", diff)
			}
			configCmpOpts := cmp.Options{
				cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime"),
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
			CohortName: "cohort",
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
				ret.Spec.CohortName = "cohort2"
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

	opts := cmp.Options{
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

func TestCQNamespaceHandlerUpdate(t *testing.T) {
	cqName := "test-cq"
	autoLqName := "auto-lq"
	nsName := "test-namespace"

	baseCQ := utiltestingapi.MakeClusterQueue(cqName).
		NamespaceSelector(&metav1.LabelSelector{
			MatchLabels: map[string]string{"dep": "eng"},
		}).
		DefaultLocalQueue(&kueue.DefaultLocalQueue{Name: autoLqName}).
		Obj()

	testcases := map[string]struct {
		cq                  *kueue.ClusterQueue
		oldNs               *corev1.Namespace
		newNs               *corev1.Namespace
		featureGateEnabled  bool
		wantLocalQueue      bool
		wantInadmissibleWls bool
	}{
		"no matching change; noop": {
			cq: baseCQ.DeepCopy(),
			oldNs: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{Name: nsName},
			},
			newNs: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{Name: nsName},
			},
			featureGateEnabled: true,
		},
		"gate disabled; noop": {
			cq: baseCQ.DeepCopy(),
			oldNs: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{Name: nsName},
			},
			newNs: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name:   nsName,
					Labels: map[string]string{"dep": "eng"},
				},
			},
		},
		"namespace matches; localqueue is created": {
			cq: baseCQ.DeepCopy(),
			oldNs: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{Name: nsName},
			},
			newNs: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name:   nsName,
					Labels: map[string]string{"dep": "eng"},
				},
			},
			featureGateEnabled:  true,
			wantLocalQueue:      true,
			wantInadmissibleWls: false,
		},
		"namespace matches but autolq is nil; localqueue is not created": {
			cq: utiltestingapi.MakeClusterQueue(cqName).
				NamespaceSelector(&metav1.LabelSelector{
					MatchLabels: map[string]string{"dep": "eng"},
				}).Obj(),
			oldNs: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{Name: nsName},
			},
			newNs: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name:   nsName,
					Labels: map[string]string{"dep": "eng"},
				},
			},
			featureGateEnabled:  true,
			wantInadmissibleWls: false,
		},
	}
	for name, tc := range testcases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.DefaultLocalQueue, tc.featureGateEnabled)

			ctx, _ := utiltesting.ContextWithLog(t)
			client := utiltesting.NewClientBuilder().WithObjects(tc.cq, tc.newNs).Build()
			cqCache := schdcache.New(client)
			g.Expect(cqCache.AddClusterQueue(ctx, tc.cq)).To(gomega.Succeed())
			qManager := qcache.NewManager(client, cqCache)

			recorder := record.NewFakeRecorder(1)
			handler := cqNamespaceHandler{
				client:   client,
				qManager: qManager,
				cache:    cqCache,
				recorder: recorder,
			}

			event := event.UpdateEvent{
				ObjectOld: tc.oldNs,
				ObjectNew: tc.newNs,
			}

			handler.Update(ctx, event, nil)

			var lq kueue.LocalQueue
			err := client.Get(ctx, types.NamespacedName{Name: autoLqName, Namespace: nsName}, &lq)
			if tc.wantLocalQueue {
				if err != nil {
					t.Fatalf("Failed to get LocalQueue: %v", err)
				}
				if lq.Spec.ClusterQueue != kueue.ClusterQueueReference(cqName) {
					t.Errorf("Wrong ClusterQueue reference, want %q, got %q", cqName, lq.Spec.ClusterQueue)
				}
			} else if !errors.IsNotFound(err) {
				t.Fatalf("Unexpected error getting LocalQueue: %v", err)
			}

			inadmissibleWorkloadsMetric := testingmetrics.CollectFilteredGaugeVec(metrics.PendingWorkloads, map[string]string{"cluster_queue": cqName, "status": "inadmissible"})
			hasInadmissibleWorkloads := len(inadmissibleWorkloadsMetric) > 0 && inadmissibleWorkloadsMetric[0].Value > 0
			if tc.wantInadmissibleWls != hasInadmissibleWorkloads {
				t.Errorf("Unexpected inadmissible workloads status, want %t, got %t", tc.wantInadmissibleWls, hasInadmissibleWorkloads)
			}
		})
	}
}

func TestCQNamespaceHandlerCreate(t *testing.T) {
	cqName := "test-cq"
	autoLqName := "auto-lq"
	nsName := "test-namespace"

	baseCQ := utiltestingapi.MakeClusterQueue(cqName).
		NamespaceSelector(&metav1.LabelSelector{
			MatchLabels: map[string]string{"dep": "eng"},
		}).
		DefaultLocalQueue(&kueue.DefaultLocalQueue{Name: autoLqName}).
		Obj()

	testcases := map[string]struct {
		cq                 *kueue.ClusterQueue
		ns                 *corev1.Namespace
		featureGateEnabled bool
		existingLq         *kueue.LocalQueue
		wantLocalQueue     bool
	}{
		"gate disabled; noop": {
			cq: baseCQ.DeepCopy(),
			ns: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name:   nsName,
					Labels: map[string]string{"dep": "eng"},
				},
			},
		},
		"namespace matches; localqueue is created": {
			cq: baseCQ.DeepCopy(),
			ns: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name:   nsName,
					Labels: map[string]string{"dep": "eng"},
				},
			},
			featureGateEnabled: true,
			wantLocalQueue:     true,
		},
		"namespace doesn't match; localqueue is not created": {
			cq: baseCQ.DeepCopy(),
			ns: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name:   nsName,
					Labels: map[string]string{"dep": "sales"},
				},
			},
			featureGateEnabled: true,
		},
		"namespace matches but autolq is nil; localqueue is not created": {
			cq: utiltestingapi.MakeClusterQueue(cqName).
				NamespaceSelector(&metav1.LabelSelector{
					MatchLabels: map[string]string{"dep": "eng"},
				}).Obj(),
			ns: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name:   nsName,
					Labels: map[string]string{"dep": "eng"},
				},
			},
			featureGateEnabled: true,
		},
		"localqueue already exists; noop": {
			cq: baseCQ.DeepCopy(),
			ns: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name:   nsName,
					Labels: map[string]string{"dep": "eng"},
				},
			},
			existingLq: utiltestingapi.MakeLocalQueue(autoLqName, nsName).
				ClusterQueue(cqName).
				Obj(),
			featureGateEnabled: true,
			wantLocalQueue:     true,
		},
	}
	for name, tc := range testcases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.DefaultLocalQueue, tc.featureGateEnabled)

			ctx, _ := utiltesting.ContextWithLog(t)
			clientBuilder := utiltesting.NewClientBuilder().WithObjects(tc.cq, tc.ns)
			if tc.existingLq != nil {
				clientBuilder.WithObjects(tc.existingLq)
			}
			recorder := record.NewFakeRecorder(1)
			client := clientBuilder.Build()
			cqCache := schdcache.New(client)
			g.Expect(cqCache.AddClusterQueue(ctx, tc.cq)).To(gomega.Succeed())
			qManager := qcache.NewManager(client, cqCache)

			handler := cqNamespaceHandler{
				client:   client,
				qManager: qManager,
				cache:    cqCache,
				recorder: recorder,
			}
			event := event.CreateEvent{
				Object: tc.ns,
			}

			handler.Create(ctx, event, nil)

			var lq kueue.LocalQueue
			err := client.Get(ctx, types.NamespacedName{Name: autoLqName, Namespace: nsName}, &lq)
			if tc.wantLocalQueue {
				if err != nil {
					t.Fatalf("Failed to get LocalQueue: %v", err)
				}
				if lq.Spec.ClusterQueue != kueue.ClusterQueueReference(cqName) {
					t.Errorf("Wrong ClusterQueue reference, want %q, got %q", cqName, lq.Spec.ClusterQueue)
				}
			} else if !errors.IsNotFound(err) {
				t.Fatalf("Unexpected error getting LocalQueue: %v", err)
			}
		})
	}
}

func TestClusterQueueReconcilerCreate(t *testing.T) {
	cqName := "test-cq"
	autoLqName := "auto-lq"
	nsMatchingName := "test-namespace-matching"
	nsNonMatchingName := "test-namespace-non-matching"
	nsExistingLqName := "test-namespace-existing-lq"

	baseCQ := utiltestingapi.MakeClusterQueue(cqName).
		NamespaceSelector(&metav1.LabelSelector{
			MatchLabels: map[string]string{"dep": "eng"},
		}).
		DefaultLocalQueue(&kueue.DefaultLocalQueue{Name: autoLqName}).
		Obj()

	matchingNs := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   nsMatchingName,
			Labels: map[string]string{"dep": "eng"},
		},
	}
	nonMatchingNs := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   nsNonMatchingName,
			Labels: map[string]string{"dep": "sales"},
		},
	}
	existingLqNs := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   nsExistingLqName,
			Labels: map[string]string{"dep": "eng"},
		},
	}
	existingLq := utiltestingapi.MakeLocalQueue(autoLqName, nsExistingLqName).
		ClusterQueue("some-other-cq").
		Obj()

	testcases := map[string]struct {
		cq                 *kueue.ClusterQueue
		namespaces         []*corev1.Namespace
		existingLqs        []*kueue.LocalQueue
		featureGateEnabled bool
		managerNsSelector  *metav1.LabelSelector
		wantLqInNs         map[string]bool // map of namespace name to wantLocalQueue
		wantEvent          *string
	}{
		"gate disabled; noop": {
			cq:         baseCQ.DeepCopy(),
			namespaces: []*corev1.Namespace{matchingNs},
			wantLqInNs: map[string]bool{
				nsMatchingName: false,
			},
		},
		"matching namespace; localqueue is created": {
			cq:                 baseCQ.DeepCopy(),
			namespaces:         []*corev1.Namespace{matchingNs},
			featureGateEnabled: true,
			wantLqInNs: map[string]bool{
				nsMatchingName: true,
			},
		},
		"non-matching namespace; localqueue is not created": {
			cq:                 baseCQ.DeepCopy(),
			namespaces:         []*corev1.Namespace{nonMatchingNs},
			featureGateEnabled: true,
			wantLqInNs: map[string]bool{
				nsNonMatchingName: false,
			},
		},
		"multiple namespaces; localqueue is created only in matching": {
			cq:                 baseCQ.DeepCopy(),
			namespaces:         []*corev1.Namespace{matchingNs, nonMatchingNs},
			featureGateEnabled: true,
			wantLqInNs: map[string]bool{
				nsMatchingName:    true,
				nsNonMatchingName: false,
			},
		},
		"autolq is nil; localqueue is not created": {
			cq: utiltestingapi.MakeClusterQueue(cqName).
				NamespaceSelector(&metav1.LabelSelector{
					MatchLabels: map[string]string{"dep": "eng"},
				}).Obj(),
			namespaces:         []*corev1.Namespace{matchingNs},
			featureGateEnabled: true,
			wantLqInNs: map[string]bool{
				nsMatchingName: false,
			},
		},
		"localqueue already exists; emit event": {
			cq:                 baseCQ.DeepCopy(),
			namespaces:         []*corev1.Namespace{existingLqNs},
			existingLqs:        []*kueue.LocalQueue{existingLq},
			featureGateEnabled: true,
			wantLqInNs: map[string]bool{
				nsExistingLqName: true, // it should exist, but not be created by the reconciler
			},
			wantEvent: ptr.To("Warning DefaultLocalQueueExists Skipping default LocalQueue creation in namespace test-namespace-existing-lq, a LocalQueue with the name auto-lq already exists"),
		},
		"with namespace selector on manager; localqueue is created only in matching": {
			cq: utiltestingapi.MakeClusterQueue(cqName).
				NamespaceSelector(&metav1.LabelSelector{}).
				DefaultLocalQueue(&kueue.DefaultLocalQueue{Name: autoLqName}).
				Obj(),
			namespaces:         []*corev1.Namespace{matchingNs, nonMatchingNs},
			featureGateEnabled: true,
			managerNsSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"dep": "eng"},
			},
			wantLqInNs: map[string]bool{
				nsMatchingName:    true,
				nsNonMatchingName: false,
			},
		},
	}
	for name, tc := range testcases {
		t.Run(name, func(t *testing.T) {
			g := NewGomegaWithT(t)
			features.SetFeatureGateDuringTest(t, features.DefaultLocalQueue, tc.featureGateEnabled)

			ctx, log := utiltesting.ContextWithLog(t)
			clientBuilder := utiltesting.NewClientBuilder()
			if tc.cq != nil {
				clientBuilder.WithObjects(tc.cq)
			}
			for _, ns := range tc.namespaces {
				clientBuilder.WithObjects(ns)
			}
			for _, lq := range tc.existingLqs {
				clientBuilder.WithObjects(lq)
			}
			client := clientBuilder.Build()
			cqCache := schdcache.New(client)
			qManager := qcache.NewManager(client, cqCache)
			recorder := record.NewFakeRecorder(1)

			reconciler := &ClusterQueueReconciler{
				client:   client,
				log:      log,
				cache:    cqCache,
				qManager: qManager,
				recorder: recorder,
			}
			if tc.managerNsSelector != nil {
				var err error
				reconciler.namespaceSelector, err = metav1.LabelSelectorAsSelector(tc.managerNsSelector)
				if err != nil {
					t.Fatalf("Couldn't parse managerNsSelector: %v", err)
				}
			}

			event := event.TypedCreateEvent[*kueue.ClusterQueue]{
				Object: tc.cq,
			}

			reconciler.Create(event)

			if tc.wantEvent != nil {
				g.Expect(recorder.Events).Should(Receive(ContainSubstring(*tc.wantEvent)))
			}

			for nsName, wantLq := range tc.wantLqInNs {
				var lq kueue.LocalQueue
				err := client.Get(ctx, types.NamespacedName{Name: autoLqName, Namespace: nsName}, &lq)
				if wantLq {
					if err != nil {
						t.Fatalf("Failed to get LocalQueue in namespace %s: %v", nsName, err)
					}
					// If it's the pre-existing LQ, check it's not modified.
					if nsName == nsExistingLqName {
						if lq.Spec.ClusterQueue != "some-other-cq" {
							t.Errorf("Existing LocalQueue was modified, want clusterQueue %q, got %q", "some-other-cq", lq.Spec.ClusterQueue)
						}
					} else {
						if lq.Spec.ClusterQueue != kueue.ClusterQueueReference(tc.cq.Name) {
							t.Errorf("Wrong ClusterQueue reference in namespace %s, want %q, got %q", nsName, tc.cq.Name, lq.Spec.ClusterQueue)
						}
					}
				} else if !errors.IsNotFound(err) {
					t.Fatalf("Unexpected error getting LocalQueue in namespace %s: %v", nsName, err)
				}
			}
		})
	}
}
