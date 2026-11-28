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
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/prometheus/client_golang/prometheus"

	"sigs.k8s.io/kueue/test/performance/multikueue/report"
)

func TestGatherQueueSample(t *testing.T) {
	registry := prometheus.NewRegistry()
	depth := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: workqueueDepthMetric}, []string{"name", "controller", "priority"})
	reconciles := prometheus.NewCounterVec(prometheus.CounterOpts{Name: reconcileTotalMetric}, []string{"controller", "result"})
	registry.MustRegister(depth, reconciles)

	depth.WithLabelValues(multiKueueWorkloadController, multiKueueWorkloadController, "0").Set(2)
	depth.WithLabelValues(multiKueueWorkloadController, multiKueueWorkloadController, "-100").Set(300)
	depth.WithLabelValues("workload", "workload", "0").Set(50)
	reconciles.WithLabelValues(multiKueueClusterController, "success").Add(4)
	reconciles.WithLabelValues(multiKueueClusterController, "requeue_after").Add(1)
	reconciles.WithLabelValues(multiKueueWorkloadController, "success").Add(700)
	reconciles.WithLabelValues(multiKueueWorkloadController, "error").Add(3)
	reconciles.WithLabelValues("workload", "success").Add(9)

	at := time.Now()
	got, err := gatherQueueSample(registry, at)
	if err != nil {
		t.Fatalf("gatherQueueSample() unexpected error: %v", err)
	}
	want := queueSample{
		at:                 at,
		depth:              map[string]int{"0": 2, "-100": 300},
		clusterReconciles:  5,
		workloadReconciles: 703,
	}
	if diff := cmp.Diff(want, got, cmp.AllowUnexported(queueSample{})); diff != "" {
		t.Errorf("gatherQueueSample() (-want,+got):\n%s", diff)
	}
}

func TestReconnectTracker(t *testing.T) {
	origin := time.Date(2026, time.November, 17, 12, 0, 0, 0, time.UTC)
	at := func(ms int) time.Time { return origin.Add(time.Duration(ms) * time.Millisecond) }
	sample := func(ms, clusterReconciles, workloadReconciles int, depth map[string]int) queueSample {
		return queueSample{at: at(ms), depth: depth, clusterReconciles: clusterReconciles, workloadReconciles: workloadReconciles}
	}

	testCases := map[string]struct {
		samples      []queueSample
		wantWindows  []reconnectWindow
		wantOpen     bool
		wantPeakByPr map[string]int
	}{
		"reconnects before the first sample are ignored": {
			samples: []queueSample{
				sample(0, 7, 100, map[string]int{"-100": 20}),
				sample(100, 7, 110, map[string]int{"-100": 0}),
			},
			wantPeakByPr: map[string]int{"-100": 20},
		},
		"window waits for the batched resync and closes when the queue is empty": {
			samples: []queueSample{
				sample(0, 3, 100, map[string]int{"0": 0}),
				sample(100, 4, 100, map[string]int{"0": 0}),
				// The queue is still empty before the events batch period elapses.
				sample(500, 4, 101, map[string]int{"0": 0}),
				sample(1200, 4, 120, map[string]int{"0": 1, "-100": 300}),
				sample(2500, 4, 400, map[string]int{"0": 0, "-100": 0}),
			},
			wantWindows: []reconnectWindow{{
				start:              at(0),
				end:                at(2500),
				peakDepth:          301,
				workloadReconciles: 300,
			}},
			wantPeakByPr: map[string]int{"0": 1, "-100": 300},
		},
		"empty queue after a reconnect without backlog": {
			samples: []queueSample{
				sample(0, 3, 100, nil),
				sample(100, 4, 100, nil),
				sample(2000, 4, 101, nil),
				sample(2100, 4, 102, nil),
			},
			wantWindows:  []reconnectWindow{{start: at(0), end: at(2100), workloadReconciles: 2}},
			wantPeakByPr: map[string]int{},
		},
		"overlapping reconnects merge and extend the window": {
			samples: []queueSample{
				sample(0, 3, 0, nil),
				sample(100, 4, 0, map[string]int{"-100": 0}),
				sample(1500, 5, 50, map[string]int{"-100": 200}),
				sample(2600, 5, 250, map[string]int{"-100": 0}),
				sample(3500, 5, 260, map[string]int{"-100": 0}),
			},
			wantWindows:  []reconnectWindow{{start: at(0), end: at(3500), peakDepth: 200, workloadReconciles: 260}},
			wantPeakByPr: map[string]int{"-100": 200},
		},
		"window is still open while the queue drains": {
			samples: []queueSample{
				sample(0, 3, 0, nil),
				sample(100, 4, 0, nil),
				sample(5000, 4, 10, map[string]int{"0": 900}),
			},
			wantOpen:     true,
			wantPeakByPr: map[string]int{"0": 900},
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			tracker := newReconnectTracker(2 * time.Second)
			for _, s := range tc.samples {
				tracker.add(s)
			}
			if diff := cmp.Diff(tc.wantWindows, tracker.closed, cmp.AllowUnexported(reconnectWindow{})); diff != "" {
				t.Errorf("closed windows (-want,+got):\n%s", diff)
			}
			if got := tracker.inWindow(); got != tc.wantOpen {
				t.Errorf("inWindow() = %t, want %t", got, tc.wantOpen)
			}
			if diff := cmp.Diff(tc.wantPeakByPr, tracker.peakDepth); diff != "" {
				t.Errorf("peak depth (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestClassifyProbes(t *testing.T) {
	origin := time.Date(2026, time.November, 17, 12, 0, 0, 0, time.UTC)
	at := func(s float64) time.Time { return origin.Add(time.Duration(s * float64(time.Second))) }
	probe := func(created, admitted float64) probeTiming {
		return probeTiming{createdAt: at(created), admittedAt: at(admitted)}
	}
	windows := []reconnectWindow{
		{start: at(10), end: at(15)},
		{start: at(40), end: at(42)},
	}

	got := classifyProbes([]probeTiming{
		probe(1, 2),
		probe(5, 6),
		// Created before the window, but still pending when it starts.
		probe(9, 11),
		probe(12, 20),
		probe(20, 21),
		probe(41, 47),
		probe(50, 51.5),
	}, windows)
	want := report.ProbeLatencies{
		BeforeReconnect: report.SummarizeDurations([]time.Duration{time.Second, time.Second}),
		DuringReconnect: report.SummarizeDurations([]time.Duration{2 * time.Second, 8 * time.Second, 6 * time.Second}),
		AfterReconnect:  report.SummarizeDurations([]time.Duration{time.Second, 1500 * time.Millisecond}),
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("classifyProbes() (-want,+got):\n%s", diff)
	}
}

func TestClassifyProbesWithoutReconnects(t *testing.T) {
	createdAt := time.Now()
	got := classifyProbes([]probeTiming{{createdAt: createdAt, admittedAt: createdAt.Add(time.Second)}}, nil)
	if got.BeforeReconnect.Count != 1 || got.DuringReconnect.Count != 0 || got.AfterReconnect.Count != 0 {
		t.Errorf("classifyProbes() = %+v, want the probe before any reconnect", got)
	}
}

func TestWriteQueueTimeline(t *testing.T) {
	origin := time.Date(2026, time.November, 17, 12, 0, 0, 0, time.UTC)
	var out strings.Builder
	err := writeQueueTimeline(&out, &queueTimeline{
		probingStarted: origin.Add(100 * time.Millisecond),
		samples: []queueSample{
			{at: origin, depth: map[string]int{"0": 1}, clusterReconciles: 3, workloadReconciles: 10},
			{at: origin.Add(250 * time.Millisecond), depth: map[string]int{"0": 0, "-100": 250}, clusterReconciles: 4, workloadReconciles: 12},
		},
	})
	if err != nil {
		t.Fatalf("writeQueueTimeline() unexpected error: %v", err)
	}
	want := `elapsedMs,multikueueClusterReconciles,multikueueWorkloadReconciles,depthPriority0,depthPriority-100
-100,3,10,1,0
150,4,12,0,250
`
	if diff := cmp.Diff(want, out.String()); diff != "" {
		t.Errorf("writeQueueTimeline() (-want,+got):\n%s", diff)
	}
}
