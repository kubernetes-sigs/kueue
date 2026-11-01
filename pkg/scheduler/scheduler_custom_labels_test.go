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

package scheduler

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/component-base/metrics/testutil"
	testingclock "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	"sigs.k8s.io/kueue/pkg/util/routine"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

// TestAdmissionReportAfterWorkloadEvent changes the cache inside the
// admission write, as a Workload event handled in that window would, and
// checks that the admission report that follows does not undo it.
func TestAdmissionReportAfterWorkloadEvent(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.CustomMetricLabels, true)
	features.SetFeatureGateDuringTest(t, features.LocalQueueMetrics, true)

	cases := map[string]struct {
		// relabelTo relabels the Workload after the write; empty deletes it.
		relabelTo string
		// wantStored is the Workload's stored values after the admission report.
		wantStored []string
		// wantActive is the admitted active gauge per "kind" then, on both queues.
		wantActive map[string]float64
	}{
		"Workload deleted": {
			wantActive: map[string]float64{"training": 0, "inference": 0},
		},
		"Workload relabeled": {
			relabelTo:  "inference",
			wantStored: []string{"inference"},
			wantActive: map[string]float64{"training": 0, "inference": 1},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, log := utiltesting.ContextWithLog(t)
			customLabels := metrics.NewCustomLabels([]config.ControllerMetricsCustomLabel{
				utiltestingapi.MakeCustomLabel("wl_kind").SourceLabelKey("kind").
					SourceKind(config.SourceKindWorkload).TrackedValues("training", "inference").Obj(),
			})
			t.Cleanup(func() { metrics.InitMetricVectors(nil) })

			ns := utiltesting.MakeNamespaceWrapper(metav1.NamespaceDefault).Obj()
			rf := utiltestingapi.MakeResourceFlavor("rf").Obj()
			cq := utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas(rf.Name).Resource(corev1.ResourceCPU, "1").Obj()).
				Obj()
			lq := utiltestingapi.MakeLocalQueue("lq", metav1.NamespaceDefault).ClusterQueue(cq.Name).Obj()
			wl := utiltestingapi.MakeWorkload("wl", metav1.NamespaceDefault).
				ResourceVersion("1").
				Label("kind", "training").
				Queue(kueue.LocalQueueName(lq.Name)).
				Request(corev1.ResourceCPU, "1").
				Obj()
			key := workload.Key(wl)

			var cqCache *schdcache.Cache
			var written bool
			cl := utiltesting.NewClientBuilder().
				WithObjects(ns, rf, cq, lq, wl).
				WithStatusSubresource(&kueue.Workload{}).
				WithInterceptorFuncs(interceptor.Funcs{
					SubResourceApply: func(ctx context.Context, c client.Client, subResourceName string, applyConf runtime.ApplyConfiguration, opts ...client.SubResourceApplyOption) error {
						if err := utiltesting.TreatSSAAsStrategicMergeForApplyConfiguration(ctx, c, subResourceName, applyConf, opts...); err != nil {
							return err
						}
						written = true
						if diff := cmp.Diff([]string{"training"}, customLabels.Get(config.SourceKindWorkload, string(key))); diff != "" {
							t.Errorf("Unexpected stored values before the event (-want,+got):\n%s", diff)
						}
						if tc.relabelTo == "" {
							if err := cqCache.DeleteWorkload(log, key); err != nil {
								t.Errorf("Deleting the workload from the cache: %v", err)
							}
							return nil
						}
						var relabeled kueue.Workload
						if err := c.Get(ctx, client.ObjectKeyFromObject(wl), &relabeled); err != nil {
							t.Errorf("Getting the admitted workload: %v", err)
							return nil
						}
						relabeled.Labels["kind"] = tc.relabelTo
						if !cqCache.AddOrUpdateWorkload(ctx, log, &relabeled) {
							t.Error("Expected the cache to take the relabeled workload")
						}
						return nil
					},
				}).
				Build()

			lqMetrics := &metrics.LocalQueueMetricsConfig{Enabled: true, QueueSelector: labels.Everything()}
			cqCache = schdcache.New(cl, schdcache.WithCustomLabels(customLabels), schdcache.WithLocalQueueMetrics(lqMetrics))
			qManager := qcache.NewManagerForUnitTests(cl, cqCache, qcache.WithCustomLabels(customLabels), qcache.WithLocalQueueMetrics(lqMetrics))
			cqCache.AddOrUpdateResourceFlavor(log, rf)
			if err := cqCache.AddClusterQueue(ctx, cq); err != nil {
				t.Fatalf("Inserting clusterQueue %s in cache: %v", cq.Name, err)
			}
			if err := qManager.AddClusterQueue(ctx, cq); err != nil {
				t.Fatalf("Inserting clusterQueue %s in manager: %v", cq.Name, err)
			}
			if err := qManager.AddLocalQueue(ctx, lq); err != nil {
				t.Fatalf("Inserting queue %s/%s in manager: %v", lq.Namespace, lq.Name, err)
			}

			scheduler := New(qManager, cqCache, cl, &utiltesting.EventRecorder{},
				WithClock(t, testingclock.NewFakeClock(time.Now().Truncate(time.Second))),
				WithPreemptionExpectations(preemptexpectations.New()),
				WithCustomLabels(customLabels))
			wg := sync.WaitGroup{}
			scheduler.setAdmissionRoutineWrapper(routine.NewWrapper(
				func() { wg.Add(1) },
				func() { wg.Done() },
			))

			ctx, cancel := context.WithTimeout(ctx, queueingTimeout)
			go qManager.CleanUpOnContext(ctx)
			defer cancel()

			scheduler.schedule(ctx)
			wg.Wait()
			if !written {
				t.Fatal("Expected the scheduler to write the admission")
			}

			var stored []string
			if slices.Contains(customLabels.StoredRefsForTest(config.SourceKindWorkload), string(key)) {
				stored = customLabels.Get(config.SourceKindWorkload, string(key))
			}
			if diff := cmp.Diff(tc.wantStored, stored); diff != "" {
				t.Errorf("Unexpected stored values after the admission report (-want,+got):\n%s", diff)
			}
			role := roletracker.RoleStandalone
			for kind, want := range tc.wantActive {
				if v, err := testutil.GetGaugeMetricValue(metrics.AdmittedActiveWorkloads.WithLabelValues(cq.Name, role, kind)); err != nil || v != want {
					t.Errorf("admitted_active_workloads{custom_wl_kind=%q} before the delete = %v (err %v), want %v", kind, v, err, want)
				}
				if v, err := testutil.GetGaugeMetricValue(metrics.LocalQueueAdmittedActiveWorkloads.WithLabelValues(lq.Name, lq.Namespace, role, kind)); err != nil || v != want {
					t.Errorf("local_queue_admitted_active_workloads{custom_wl_kind=%q} before the delete = %v (err %v), want %v", kind, v, err, want)
				}
			}

			if err := cqCache.DeleteWorkload(log, key); err != nil {
				t.Fatalf("Deleting the workload from the cache: %v", err)
			}
			if got := customLabels.StoredRefsForTest(config.SourceKindWorkload); len(got) != 0 {
				t.Errorf("Workload label values left after the delete: %v", got)
			}
			for kind := range tc.wantActive {
				if v, err := testutil.GetGaugeMetricValue(metrics.AdmittedActiveWorkloads.WithLabelValues(cq.Name, role, kind)); err != nil || v != 0 {
					t.Errorf("admitted_active_workloads{custom_wl_kind=%q} = %v (err %v), want 0", kind, v, err)
				}
				if v, err := testutil.GetGaugeMetricValue(metrics.LocalQueueAdmittedActiveWorkloads.WithLabelValues(lq.Name, lq.Namespace, role, kind)); err != nil || v != 0 {
					t.Errorf("local_queue_admitted_active_workloads{custom_wl_kind=%q} = %v (err %v), want 0", kind, v, err)
				}
			}
			if v, err := testutil.GetCounterMetricValue(metrics.LocalQueueAdmittedWorkloadsTotal.WithLabelValues(lq.Name, lq.Namespace, "", role, "training")); err != nil || v != 1 {
				t.Errorf("local_queue_admitted_workloads_total{custom_wl_kind=\"training\"} = %v (err %v), want 1", v, err)
			}
		})
	}
}
