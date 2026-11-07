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
	"fmt"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingmetrics "sigs.k8s.io/kueue/pkg/util/testing/metrics"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

// The LocalQueue delete event clears the LocalQueue metrics, so a Workload event
// handled after it must not record them again. The ClusterQueue metric shows that
// the event reached the code that records both.
func TestWorkloadMetricsAfterLocalQueueDeletion(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.LocalQueueMetrics, true)
	now := time.Now().Truncate(time.Second)
	admitted := utiltestingapi.MakeWorkload("wl", "ns").Queue("lq").
		ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").Obj(), now).
		AdmittedAt(true, now)
	cases := map[string]struct {
		// When oldWorkload is set, the reconciler handles an update event from
		// oldWorkload to workload. Otherwise it reconciles workload.
		oldWorkload        *kueue.Workload
		workload           *kueue.Workload
		clusterQueueMetric *prometheus.CounterVec
		localQueueMetric   *prometheus.CounterVec
	}{
		"admitted": {
			workload: utiltestingapi.MakeWorkload("wl", "ns").Queue("lq").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").Obj(), now).
				Obj(),
			clusterQueueMetric: metrics.AdmittedWorkloadsTotal,
			localQueueMetric:   metrics.LocalQueueAdmittedWorkloadsTotal,
		},
		"finished": {
			oldWorkload:        admitted.Clone().Obj(),
			workload:           admitted.Clone().FinishedAt(now).Obj(),
			clusterQueueMetric: metrics.FinishedWorkloadsTotal,
			localQueueMetric:   metrics.LocalQueueFinishedWorkloadsTotal,
		},
		"deactivated": {
			workload:           admitted.Clone().Active(false).Obj(),
			clusterQueueMetric: metrics.EvictedWorkloadsTotal,
			localQueueMetric:   metrics.LocalQueueEvictedWorkloadsTotal,
		},
	}
	for name, tc := range cases {
		for _, deleteLocalQueue := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s, LocalQueue deleted=%t", name, deleteLocalQueue), func(t *testing.T) {
				ctx, _ := utiltesting.ContextWithLog(t)
				lqRef := metrics.LocalQueueReference{Name: "lq", Namespace: "ns"}
				metrics.ClearClusterQueueMetrics("cq")
				metrics.ClearLocalQueueMetrics(lqRef)
				t.Cleanup(func() {
					metrics.ClearClusterQueueMetrics("cq")
					metrics.ClearLocalQueueMetrics(lqRef)
				})

				wl := tc.workload.DeepCopy()
				cq := utiltestingapi.MakeClusterQueue("cq").Active(metav1.ConditionTrue).Obj()
				lq := utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj()
				cl := utiltesting.NewClientBuilder().
					WithObjects(cq, wl).
					WithStatusSubresource(wl).
					Build()
				cqCache := schdcache.New(cl)
				qManager := qcache.NewManagerForUnitTests(cl, cqCache)
				if err := cqCache.AddClusterQueue(ctx, cq); err != nil {
					t.Fatalf("Adding ClusterQueue to the scheduler cache: %v", err)
				}
				if err := qManager.AddClusterQueue(ctx, cq); err != nil {
					t.Fatalf("Adding ClusterQueue to the queue manager: %v", err)
				}
				if err := cl.Create(ctx, lq); err != nil {
					t.Fatalf("Creating LocalQueue: %v", err)
				}
				lqReconciler := NewLocalQueueReconciler(cl, qManager, cqCache)
				lqReconciler.Create(event.TypedCreateEvent[*kueue.LocalQueue]{Object: lq})
				wantLocalQueueSeries := 1
				if deleteLocalQueue {
					if err := cl.Delete(ctx, lq); err != nil {
						t.Fatalf("Deleting LocalQueue: %v", err)
					}
					lqReconciler.Delete(event.TypedDeleteEvent[*kueue.LocalQueue]{Object: lq})
					wantLocalQueueSeries = 0
				}

				reconciler := NewWorkloadReconciler(cl, qManager, cqCache, &utiltesting.EventRecorder{})
				if tc.oldWorkload != nil {
					reconciler.handleUpdate(ctx, event.TypedUpdateEvent[*kueue.Workload]{
						ObjectOld: tc.oldWorkload.DeepCopy(),
						ObjectNew: wl,
					})
				} else if _, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(wl)}); err != nil {
					t.Fatalf("Reconcile: %v", err)
				}

				if got := testingmetrics.CollectFilteredGaugeVec(tc.clusterQueueMetric, map[string]string{"cluster_queue": "cq"}); len(got) != 1 {
					t.Fatalf("ClusterQueue series: got %v, want one", got)
				}
				if got := testingmetrics.CollectFilteredGaugeVec(tc.localQueueMetric, map[string]string{"name": "lq", "namespace": "ns"}); len(got) != wantLocalQueueSeries {
					t.Errorf("LocalQueue series: got %v, want %d", got, wantLocalQueueSeries)
				}
			})
		}
	}
}
