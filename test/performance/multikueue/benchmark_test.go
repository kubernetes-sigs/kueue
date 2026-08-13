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

package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestObservationCollectorUsesObservationTime(t *testing.T) {
	createdAt := time.Date(2026, time.July, 29, 12, 0, 0, 123, time.UTC)
	observedAt := createdAt.Add(1750 * time.Millisecond)
	workload := admittedWorkload(createdAt)

	collector := newObservationCollector()
	if err := collector.observe(workload, observedAt); err != nil {
		t.Fatalf("observe() unexpected error: %v", err)
	}

	got := collector.workloads[types.NamespacedName{Namespace: workload.Namespace, Name: workload.Name}]
	if got == nil {
		t.Fatal("workload was not observed")
	}
	if !got.createdAt.Equal(createdAt) {
		t.Errorf("createdAt = %s, want %s", got.createdAt, createdAt)
	}
	if !got.quotaReservedAt.Equal(observedAt) {
		t.Errorf("quotaReservedAt = %s, want %s", got.quotaReservedAt, observedAt)
	}
	if !got.dispatchReadyAt.Equal(observedAt) {
		t.Errorf("dispatchReadyAt = %s, want %s", got.dispatchReadyAt, observedAt)
	}
	if !got.admittedAt.Equal(observedAt) {
		t.Errorf("admittedAt = %s, want %s", got.admittedAt, observedAt)
	}
	if wantCluster := workerName(0); got.cluster != wantCluster {
		t.Errorf("cluster = %q, want %q", got.cluster, wantCluster)
	}
}

func TestObservationCollectorDeduplicatesAdmissions(t *testing.T) {
	createdAt := time.Now()
	observedAt := createdAt.Add(time.Second)
	workload := admittedWorkload(createdAt)
	collector := newObservationCollector()

	if err := collector.observe(workload, observedAt); err != nil {
		t.Fatalf("observe() unexpected error: %v", err)
	}
	if err := collector.observe(workload, observedAt.Add(time.Second)); err != nil {
		t.Fatalf("observe() unexpected error on duplicate observation: %v", err)
	}
	if collector.admittedCount() != 1 {
		t.Errorf("admittedCount() = %d, want 1 after duplicate observation", collector.admittedCount())
	}
}

func TestObservationCollectorSummarize(t *testing.T) {
	createdAt := time.Now()
	workload := admittedWorkload(createdAt)
	collector := newObservationCollector()
	if err := collector.observe(workload, createdAt.Add(time.Second)); err != nil {
		t.Fatalf("observe() unexpected error: %v", err)
	}

	summary, err := collector.summarize(benchmarkConfig{
		WorkloadCount:   1,
		WorkerClusters:  3,
		CreationWorkers: 1,
		CPURequest:      "1m",
	}, time.Millisecond, 0)
	if err != nil {
		t.Fatalf("summarize() unexpected error: %v", err)
	}
	for _, worker := range []string{"worker-1", "worker-2", "worker-3"} {
		if _, found := summary.WorkerDistribution[worker]; !found {
			t.Errorf("WorkerDistribution is missing %q", worker)
		}
	}
	if wantCluster := workerName(0); summary.WorkerDistribution[wantCluster] != 1 {
		t.Errorf("WorkerDistribution[%q] = %d, want 1", wantCluster, summary.WorkerDistribution[wantCluster])
	}
	if summary.Scenario.GCInterval != benchmarkGCInterval.String() {
		t.Errorf("Scenario.GCInterval = %q, want %q", summary.Scenario.GCInterval, benchmarkGCInterval.String())
	}
	if summary.Scenario.WorkerLostTimeout != benchmarkWorkerLostTimeout.String() {
		t.Errorf("Scenario.WorkerLostTimeout = %q, want %q", summary.Scenario.WorkerLostTimeout, benchmarkWorkerLostTimeout.String())
	}
	if summary.Scenario.EventsBatchPeriod != benchmarkEventsBatchPeriod.String() {
		t.Errorf("Scenario.EventsBatchPeriod = %q, want %q", summary.Scenario.EventsBatchPeriod, benchmarkEventsBatchPeriod.String())
	}
	if summary.Scenario.RemoteClientQPS != rest.DefaultQPS || summary.Scenario.RemoteClientBurst != rest.DefaultBurst {
		t.Errorf(
			"Scenario remote rate limits = %v, %v, want %v, %v",
			summary.Scenario.RemoteClientQPS,
			summary.Scenario.RemoteClientBurst,
			rest.DefaultQPS,
			rest.DefaultBurst,
		)
	}
	if summary.Scenario.WorkloadConcurrency != workloadConcurrency {
		t.Errorf("Scenario.WorkloadConcurrency = %d, want %d", summary.Scenario.WorkloadConcurrency, workloadConcurrency)
	}
}

func TestRunBenchmarkCancelsAndJoinsBackgroundWork(t *testing.T) {
	testCases := map[string]bool{
		"generation returns first": true,
		"watch returns first":      false,
	}
	for name, releaseGenerationFirst := range testCases {
		t.Run(name, func(t *testing.T) {
			testRunBenchmarkCancelsAndJoinsBackgroundWork(t, releaseGenerationFirst)
		})
	}
}

func testRunBenchmarkCancelsAndJoinsBackgroundWork(t *testing.T, releaseGenerationFirst bool) {
	watcher := watch.NewRaceFreeFake()
	malformedWorkload := utiltestingapi.MakeWorkload("workload", benchmarkNamespaceName).Obj()
	malformedWorkload.ResourceVersion = "1"
	watcher.Add(malformedWorkload)

	generationCanceled := make(chan struct{})
	generationReturned := make(chan struct{})
	releaseGeneration := make(chan struct{})
	watchCanceled := make(chan struct{})
	watchReturned := make(chan struct{})
	releaseWatch := make(chan struct{})
	generationReleased := false
	watchReleased := false
	defer func() {
		if !generationReleased {
			close(releaseGeneration)
		}
		if !watchReleased {
			close(releaseWatch)
		}
	}()

	benchmarkClient := interceptor.NewClient(utiltesting.NewClientBuilder().Build(), interceptor.Funcs{
		Create: func(ctx context.Context, _ client.WithWatch, _ client.Object, _ ...client.CreateOption) error {
			<-ctx.Done()
			close(generationCanceled)
			<-releaseGeneration
			close(generationReturned)
			return ctx.Err()
		},
		Watch: func(ctx context.Context, _ client.WithWatch, _ client.ObjectList, _ ...client.ListOption) (watch.Interface, error) {
			go func() {
				<-ctx.Done()
				close(watchCanceled)
				<-releaseWatch
				watcher.Stop()
				close(watchReturned)
			}()
			return watcher, nil
		},
	})

	result := make(chan error, 1)
	go func() {
		_, err := runBenchmark(t.Context(), &benchmarkCluster{client: benchmarkClient}, benchmarkConfig{
			WorkloadCount:   1,
			WorkerClusters:  1,
			CreationWorkers: 1,
			CPURequest:      "1m",
		})
		result <- err
	}()

	waitForSignal := func(signal <-chan struct{}, name string) {
		t.Helper()
		select {
		case <-signal:
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for %s", name)
		}
	}
	assertRunning := func(wait time.Duration) {
		t.Helper()
		select {
		case err := <-result:
			t.Fatalf("runBenchmark() returned before its background work stopped: %v", err)
		case <-time.After(wait):
		}
	}

	waitForSignal(generationCanceled, "workload generation cancellation")
	waitForSignal(watchCanceled, "workload watch cancellation")
	assertRunning(10 * time.Millisecond)

	if releaseGenerationFirst {
		generationReleased = true
		close(releaseGeneration)
		waitForSignal(generationReturned, "workload generation return")
		assertRunning(100 * time.Millisecond)

		watchReleased = true
		close(releaseWatch)
		waitForSignal(watchReturned, "workload watch return")
	} else {
		watchReleased = true
		close(releaseWatch)
		waitForSignal(watchReturned, "workload watch return")
		assertRunning(100 * time.Millisecond)

		generationReleased = true
		close(releaseGeneration)
		waitForSignal(generationReturned, "workload generation return")
	}

	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "has no "+benchmarkCreatedAtAnnotation+" annotation") {
			t.Fatalf("runBenchmark() error = %v, want malformed Workload error", err)
		}
		if errors.Is(err, context.Canceled) {
			t.Fatalf("runBenchmark() error = %v, want the primary collector error", err)
		}
	case <-time.After(time.Second):
		t.Fatal("runBenchmark() did not return after its background work stopped")
	}
}

func admittedWorkload(createdAt time.Time) *kueue.Workload {
	clusterName := workerName(0)
	workload := utiltestingapi.MakeWorkload("workload", "namespace").
		Annotation(benchmarkCreatedAtAnnotation, createdAt.Format(time.RFC3339Nano)).
		AdmissionChecks(kueue.AdmissionCheckState{
			Name:  multiKueueAdmissionCheck,
			State: kueue.CheckStateReady,
		}).
		Conditions(
			metav1.Condition{Type: kueue.WorkloadQuotaReserved, Status: metav1.ConditionTrue},
			metav1.Condition{Type: kueue.WorkloadAdmitted, Status: metav1.ConditionTrue},
		).
		Obj()
	workload.Status.ClusterName = &clusterName
	return workload
}

func TestObservationCollectorRejectsUnusableCreationTime(t *testing.T) {
	testCases := map[string]struct {
		annotation string
	}{
		"missing": {},
		"unparsable": {
			annotation: "not-a-timestamp",
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			workload := utiltestingapi.MakeWorkload("workload", "namespace").Obj()
			if tc.annotation != "" {
				workload.Annotations = map[string]string{benchmarkCreatedAtAnnotation: tc.annotation}
			}

			if err := newObservationCollector().observe(workload, time.Now()); err == nil {
				t.Error("observe() returned no error")
			}
		})
	}
}

func TestSummarizeRejectsIncompleteTiming(t *testing.T) {
	cfg := benchmarkConfig{WorkloadCount: 1, WorkerClusters: 1, CreationWorkers: 1, CPURequest: "1m"}

	testCases := map[string]struct {
		mutate func(*kueue.Workload)
	}{
		"missing dispatch-ready check": {
			mutate: func(wl *kueue.Workload) { wl.Status.AdmissionChecks = nil },
		},
		"missing worker assignment": {
			mutate: func(wl *kueue.Workload) { wl.Status.ClusterName = nil },
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			workload := admittedWorkload(time.Now())
			tc.mutate(workload)

			collector := newObservationCollector()
			if err := collector.observe(workload, time.Now()); err != nil {
				t.Fatalf("observe() unexpected error: %v", err)
			}
			if _, err := collector.summarize(cfg, time.Millisecond, 0); err == nil {
				t.Error("summarize() returned no error for incomplete timing data")
			}
		})
	}
}
