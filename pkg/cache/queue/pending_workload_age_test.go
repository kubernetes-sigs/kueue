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

package queue

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	testingclock "k8s.io/utils/clock/testing"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingmetrics "sigs.k8s.io/kueue/pkg/util/testing/metrics"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestOldestPendingWorkloadAge(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	base := utiltestingapi.MakeWorkload("wl", "ns").Queue("lq").Creation(now.Add(-6 * time.Hour))
	cases := map[string]struct {
		wl            *kueue.Workload
		extraWorkload *kueue.Workload
		wantAge       time.Duration
	}{
		"oldest workload is not the scheduling head": {
			wl:            base.Clone().Priority(1).Obj(),
			extraWorkload: utiltestingapi.MakeWorkload("young", "ns").Queue("lq").Priority(10).Creation(now.Add(-time.Hour)).Obj(),
			wantAge:       6 * time.Hour,
		},
		"empty queue":                           {},
		"restored pending workload retains age": {wl: base.Clone().Obj(), wantAge: 6 * time.Hour},
		"future timestamp is clamped":           {wl: base.Clone().Creation(now.Add(time.Hour)).Obj()},
		"requeued workload starts a new wait": {
			wl:      base.Clone().Condition(metav1.Condition{Type: kueue.WorkloadRequeued, Status: metav1.ConditionTrue, LastTransitionTime: metav1.NewTime(now.Add(-time.Hour))}).Obj(),
			wantAge: time.Hour,
		},
		"preempted workload starts at quota release": {
			wl: base.Clone().Condition(metav1.Condition{Type: kueue.WorkloadEvicted, Status: metav1.ConditionTrue, LastTransitionTime: metav1.NewTime(now.Add(-2 * time.Hour))}).
				Condition(metav1.Condition{Type: kueue.WorkloadQuotaReserved, Status: metav1.ConditionFalse, LastTransitionTime: metav1.NewTime(now.Add(-time.Hour))}).Obj(),
			wantAge: time.Hour,
		},
		"failed scheduling does not reset age": {
			wl:      base.Clone().Condition(metav1.Condition{Type: kueue.WorkloadQuotaReserved, Status: metav1.ConditionFalse, LastTransitionTime: metav1.NewTime(now.Add(-time.Hour))}).Obj(),
			wantAge: 6 * time.Hour,
		},
		"inactive workload is excluded":       {wl: base.Clone().Active(false).Obj()},
		"finished workload is excluded":       {wl: base.Clone().Condition(metav1.Condition{Type: kueue.WorkloadFinished, Status: metav1.ConditionTrue}).Obj()},
		"quota reserved workload is excluded": {wl: base.Clone().SimpleReserveQuota("cq", "rf", now).Obj()},
		"deleting workload is excluded":       {wl: base.Clone().Finalizers("test").DeletionTimestamp(now).Obj()},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			metrics.InitMetricVectors(nil)
			t.Cleanup(func() { metrics.InitMetricVectors(nil) })
			ctx, _ := utiltesting.ContextWithLog(t)
			builder := utiltesting.NewClientBuilder().WithObjects(utiltesting.MakeNamespace("ns")).
				WithIndex(&corev1.LimitRange{}, indexer.LimitRangeHasContainerOrPodType, indexer.IndexLimitRangeHasContainerOrPodType)
			if tc.wl != nil {
				builder = builder.WithObjects(tc.wl)
			}
			if tc.extraWorkload != nil {
				builder = builder.WithObjects(tc.extraWorkload)
			}
			m := NewManagerForUnitTests(builder.Build(), nil, WithClock(testingclock.NewFakeClock(now)))
			cq := utiltestingapi.MakeClusterQueue("cq").Obj()
			if err := m.AddClusterQueue(ctx, cq); err != nil {
				t.Fatal(err)
			}
			if err := m.AddLocalQueue(ctx, utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj()); err != nil {
				t.Fatal(err)
			}
			m.ResyncGaugeMetrics()
			got := testingmetrics.CollectFilteredGaugeVec(metrics.OldestPendingWorkloadAge, map[string]string{"cluster_queue": "cq"})
			if len(got) != 1 || got[0].Value != tc.wantAge.Seconds() {
				t.Fatalf("age metrics = %v, want one series with value %v", got, tc.wantAge.Seconds())
			}
		})
	}
}

func TestPendingWorkloadAgeReporting(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		features.SetFeatureGateDuringTest(t, features.CustomMetricLabels, true)
		t.Cleanup(func() { metrics.InitMetricVectors(nil) })
		ctx, log := utiltesting.ContextWithLog(t)
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		clock := testingclock.NewFakeClock(time.Now().Truncate(time.Second))
		labels := metrics.NewCustomLabels([]config.ControllerMetricsCustomLabel{
			utiltestingapi.MakeCustomLabel("team").SourceLabelKey("team").SourceKind(config.SourceKindClusterQueue).Obj(),
			utiltestingapi.MakeCustomLabel("kind").SourceLabelKey("kind").SourceKind(config.SourceKindWorkload).Obj(),
		})
		cq := utiltestingapi.MakeClusterQueue("cq").Label("team", "ml").Obj()
		labels.CQStore("cq", cq.Labels, nil)
		lq := utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj()
		old := utiltestingapi.MakeWorkload("old", "ns").Queue("lq").Creation(clock.Now().Add(-4 * time.Hour)).Obj()
		cl := utiltesting.NewClientBuilder().WithObjects(utiltesting.MakeNamespace("ns"), old).
			WithIndex(&corev1.LimitRange{}, indexer.LimitRangeHasContainerOrPodType, indexer.IndexLimitRangeHasContainerOrPodType).Build()
		checker := &pendingWorkloadsStatusChecker{active: true}
		elected := make(chan struct{})
		tracker := roletracker.NewRoleTracker(elected)
		m, requeuer := NewManagerForUnitTestsWithRequeuer(cl, checker, WithClock(clock), WithCustomLabels(labels), WithRoleTracker(tracker), WithPreemptionExpectations(preemptexpectations.New()))
		if err := m.AddClusterQueue(ctx, cq); err != nil {
			t.Fatal(err)
		}
		if err := m.AddLocalQueue(ctx, lq); err != nil {
			t.Fatal(err)
		}
		requeuer.ProcessRequeues(ctx)
		if m.NeedLeaderElection() {
			t.Fatal("queue metrics must run on followers")
		}
		go func() {
			if err := m.Start(ctx); err != nil {
				t.Error(err)
			}
		}()
		synctest.Wait()
		checkAge := func(want time.Duration, role string) {
			t.Helper()
			got := testingmetrics.CollectFilteredGaugeVec(metrics.OldestPendingWorkloadAge, map[string]string{"cluster_queue": "cq"})
			if len(got) != 1 || got[0].Value != want.Seconds() {
				t.Fatalf("age metrics = %v, want one series with value %v", got, want.Seconds())
			}
			wantLabels := map[string]string{"cluster_queue": "cq", "replica_role": role, "custom_team": "ml"}
			if diff := cmp.Diff(wantLabels, got[0].Labels); diff != "" {
				t.Fatalf("labels mismatch (-want,+got): %s", diff)
			}
		}
		checkAge(4*time.Hour, roletracker.RoleFollower)
		// No events: the timer alone must make the four-hour alert fire.
		clock.Step(pendingWorkloadAgeReportingInterval)
		synctest.Wait()
		checkAge(4*time.Hour+pendingWorkloadAgeReportingInterval, roletracker.RoleFollower)
		// A checkout remains pending until quota is reserved or it is abandoned.
		heads := m.Heads(ctx)
		if len(heads) != 1 {
			t.Fatalf("heads = %d, want 1", len(heads))
		}
		m.ResyncGaugeMetrics()
		checkAge(4*time.Hour+pendingWorkloadAgeReportingInterval, roletracker.RoleFollower)
		if !m.RequeueWorkload(ctx, &heads[0].Info, RequeueReasonGeneric, "") {
			t.Fatal("failed to requeue workload")
		}
		// Admit a younger workload while the old workload remains inadmissible.
		young := utiltestingapi.MakeWorkload("young", "ns").Queue("lq").Creation(clock.Now()).Obj()
		if err := cl.Create(ctx, young); err != nil {
			t.Fatal(err)
		}
		if err := m.AddOrUpdateWorkload(ctx, log, young); err != nil {
			t.Fatal(err)
		}
		m.DeleteWorkload(log, workload.Key(young))
		clock.Step(pendingWorkloadAgeReportingInterval)
		synctest.Wait()
		checkAge(4*time.Hour+2*pendingWorkloadAgeReportingInterval, roletracker.RoleFollower)
		// A stopped ClusterQueue still has waiting workloads.
		checker.active = false
		m.ResyncGaugeMetrics()
		checkAge(4*time.Hour+2*pendingWorkloadAgeReportingInterval, roletracker.RoleFollower)
		tracker.OnElected(func() { metrics.ClearGaugeMetricsForRole(roletracker.RoleFollower); m.ResyncGaugeMetrics() })
		close(elected)
		tracker.Start(ctx, log)
		checkAge(4*time.Hour+2*pendingWorkloadAgeReportingInterval, roletracker.RoleLeader)
		// Removing the oldest workload clears the alert on the next refresh.
		m.DeleteWorkload(log, workload.Key(old))
		clock.Step(pendingWorkloadAgeReportingInterval)
		synctest.Wait()
		checkAge(0, roletracker.RoleLeader)
		m.DeleteClusterQueue(log, cq)
		clock.Step(pendingWorkloadAgeReportingInterval)
		synctest.Wait()
		if got := testingmetrics.CollectFilteredGaugeVec(metrics.OldestPendingWorkloadAge, nil); len(got) != 0 {
			t.Fatalf("metrics after queue deletion = %v, want none", got)
		}
		cancel()
		synctest.Wait()
	})
}
