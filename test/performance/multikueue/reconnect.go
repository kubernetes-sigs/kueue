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
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configuration "sigs.k8s.io/kueue/test/performance/multikueue/config"
	"sigs.k8s.io/kueue/test/performance/multikueue/report"
)

const (
	probeNamePrefix = "probe-"

	// queueSampleInterval is how often the runner samples the controller metrics. It bounds the
	// precision of the reconnect windows.
	queueSampleInterval = 100 * time.Millisecond

	multiKueueWorkloadController = "multikueue_workload"
	multiKueueClusterController  = "multikueue_cluster"
	workqueueDepthMetric         = "workqueue_depth"
	reconcileTotalMetric         = "controller_runtime_reconcile_total"
)

// maxProbes is the number of probe workloads the runner reserves quota for.
func maxProbes(cfg configuration.Config) int {
	if cfg.Reconnect == nil {
		return 0
	}
	return cfg.Reconnect.MaxProbes(cfg.Timeout.Duration)
}

func isProbe(name string) bool {
	return strings.HasPrefix(name, probeNamePrefix)
}

// queueSample is one reading of the in-process controller metrics.
type queueSample struct {
	at time.Time
	// depth is the depth of the MultiKueue workload queue by priority.
	depth              map[string]int
	clusterReconciles  int
	workloadReconciles int
}

func (s queueSample) totalDepth() int {
	total := 0
	for _, depth := range s.depth {
		total += depth
	}
	return total
}

// gatherQueueSample reads the MultiKueue workload queue depth and the reconcile counts of the
// MultiKueue controllers. Every controller manager of the runner registers its metrics in the same
// registry, but only the manager cluster runs the MultiKueue controllers.
func gatherQueueSample(gatherer prometheus.Gatherer, at time.Time) (queueSample, error) {
	families, err := gatherer.Gather()
	if err != nil {
		return queueSample{}, fmt.Errorf("gather controller metrics: %w", err)
	}
	sample := queueSample{at: at, depth: make(map[string]int)}
	for _, family := range families {
		switch family.GetName() {
		case workqueueDepthMetric:
			for _, metric := range family.GetMetric() {
				if labelValue(metric, "name") == multiKueueWorkloadController {
					sample.depth[labelValue(metric, "priority")] += int(metric.GetGauge().GetValue())
				}
			}
		case reconcileTotalMetric:
			for _, metric := range family.GetMetric() {
				switch labelValue(metric, "controller") {
				case multiKueueClusterController:
					sample.clusterReconciles += int(metric.GetCounter().GetValue())
				case multiKueueWorkloadController:
					sample.workloadReconciles += int(metric.GetCounter().GetValue())
				}
			}
		}
	}
	return sample, nil
}

func labelValue(metric *dto.Metric, name string) string {
	for _, label := range metric.GetLabel() {
		if label.GetName() == name {
			return label.GetValue()
		}
	}
	return ""
}

type reconnectWindow struct {
	start              time.Time
	end                time.Time
	peakDepth          int
	workloadReconciles int
}

// reconnectTracker turns metric samples into reconnect windows. Once the MultiKueueClusters are
// connected, the MultiKueueCluster controller reconciles only when a worker watch ends and the
// manager reconnects to that worker, so a new MultiKueueCluster reconcile opens a window. The
// window closes once the workload queue is empty. The resync enqueues the workloads only after the
// events batch period, so a window lasts at least minDuration after the last reconnect in it.
type reconnectTracker struct {
	minDuration time.Duration

	previous      *queueSample
	open          *reconnectWindow
	lastReconnect time.Time
	reconcilesAt  int

	closed    []reconnectWindow
	peakDepth map[string]int
}

func newReconnectTracker(minDuration time.Duration) *reconnectTracker {
	return &reconnectTracker{minDuration: minDuration, peakDepth: make(map[string]int)}
}

func (t *reconnectTracker) add(sample queueSample) {
	for priority, depth := range sample.depth {
		t.peakDepth[priority] = max(t.peakDepth[priority], depth)
	}
	previous := t.previous
	t.previous = &sample
	if previous == nil {
		return
	}

	if sample.clusterReconciles > previous.clusterReconciles {
		// The reconnect happened after the previous sample. Starting the window there rather than
		// at this sample keeps a probe created in between from counting as unaffected, and
		// measuring minDuration from this sample keeps the window open long enough.
		t.lastReconnect = sample.at
		if t.open == nil {
			t.open = &reconnectWindow{start: previous.at}
			t.reconcilesAt = previous.workloadReconciles
		}
	}
	if t.open == nil {
		return
	}
	t.open.peakDepth = max(t.open.peakDepth, sample.totalDepth())
	if sample.totalDepth() == 0 && sample.at.Sub(t.lastReconnect) >= t.minDuration {
		t.open.end = sample.at
		t.open.workloadReconciles = sample.workloadReconciles - t.reconcilesAt
		t.closed = append(t.closed, *t.open)
		t.open = nil
	}
}

func (t *reconnectTracker) inWindow() bool {
	return t.open != nil
}

type probeTiming struct {
	createdAt  time.Time
	admittedAt time.Time
}

// classifyProbes splits probe admission latencies by when the probe was pending. A probe pending
// at any time in a reconnect window is affected by that reconnect, even when it was created
// shortly before the window started.
func classifyProbes(probes []probeTiming, windows []reconnectWindow) report.ProbeLatencies {
	var before, during, after []time.Duration
	for _, probe := range probes {
		latency := probe.admittedAt.Sub(probe.createdAt)
		switch {
		case slices.ContainsFunc(windows, func(w reconnectWindow) bool {
			return probe.createdAt.Before(w.end) && probe.admittedAt.After(w.start)
		}):
			during = append(during, latency)
		case len(windows) == 0 || probe.createdAt.Before(windows[0].start):
			before = append(before, latency)
		default:
			after = append(after, latency)
		}
	}
	return report.ProbeLatencies{
		BeforeReconnect: report.SummarizeDurations(before),
		DuringReconnect: report.SummarizeDurations(during),
		AfterReconnect:  report.SummarizeDurations(after),
	}
}

func summarizeReconnects(started time.Time, tracker *reconnectTracker, probes *observationCollector) (*report.ReconnectSummary, error) {
	timings := make([]probeTiming, 0, len(probes.workloads))
	for key, timing := range probes.workloads {
		if timing.createdAt.IsZero() || timing.admittedAt.IsZero() {
			return nil, fmt.Errorf("incomplete timing data for probe %s", key)
		}
		timings = append(timings, probeTiming{createdAt: timing.createdAt, admittedAt: timing.admittedAt})
	}
	windows := make([]report.ReconnectWindow, 0, len(tracker.closed))
	for _, window := range tracker.closed {
		windows = append(windows, report.ReconnectWindow{
			StartMs:            window.start.Sub(started).Milliseconds(),
			DrainMs:            window.end.Sub(window.start).Milliseconds(),
			PeakQueueDepth:     window.peakDepth,
			WorkloadReconciles: window.workloadReconciles,
		})
	}
	return &report.ReconnectSummary{
		Windows:          windows,
		ProbeAdmissionMs: classifyProbes(timings, tracker.closed),
		PeakQueueDepth:   tracker.peakDepth,
	}, nil
}

// queueTimeline is the sampled MultiKueue workload queue. Samples start before probing, while the
// runner waits for the queue to drain after the preloaded workloads are admitted.
type queueTimeline struct {
	probingStarted time.Time
	samples        []queueSample
}

// runReconnectPhase runs after the preloaded workloads are admitted. It waits until the
// MultiKueue workload queue stays empty, then creates a probe workload every probe interval while
// the worker API servers end the MultiKueue watches. It stops probing once the configured number
// of reconnects has drained and the configured time has passed, and then waits for every probe to
// be admitted and for any later reconnect to drain.
func runReconnectPhase(
	ctx context.Context,
	c client.Client,
	cfg configuration.Config,
	runID string,
	gatherer prometheus.Gatherer,
	observed <-chan timedWorkload,
	watchDone <-chan error,
	preload *observationCollector,
) (*report.ReconnectSummary, *queueTimeline, error) {
	phaseCtx, cancelPhase := context.WithCancel(ctx)
	var phaseWG sync.WaitGroup
	defer phaseWG.Wait()
	defer cancelPhase()

	samples := make(chan queueSample, 1)
	samplerDone := make(chan error, 1)
	phaseWG.Go(func() {
		samplerDone <- sampleQueue(phaseCtx, gatherer, samples)
	})

	// A reconnect resync enqueues workloads only after the events batch period, so an empty queue
	// shows that no reconnect is pending only after it has stayed empty that long.
	quietPeriod := cfg.EventsBatchPeriod.Duration + time.Second
	tracker := newReconnectTracker(quietPeriod)
	timeline := &queueTimeline{}
	var quietSince *queueSample
	probes := newObservationCollector()
	stopProbing := make(chan struct{})
	var probingDone chan probingResult
	var probingEndsAt time.Time
	created := -1
	for created < 0 || probes.admittedCount() < created || tracker.inWindow() {
		select {
		case sample := <-samples:
			timeline.samples = append(timeline.samples, sample)
			if timeline.probingStarted.IsZero() {
				switch {
				case sample.totalDepth() > 0:
					quietSince = nil
					continue
				case quietSince == nil || sample.clusterReconciles != quietSince.clusterReconciles:
					quietSince = &sample
					continue
				case sample.at.Sub(quietSince.at) < quietPeriod:
					continue
				}
				fmt.Println("MultiKueue workload queue drained; probing until", cfg.Reconnect.Reconnects, "worker reconnects have drained")
				timeline.probingStarted = sample.at
				tracker.add(sample)
				probingDone = make(chan probingResult, 1)
				phaseWG.Go(func() {
					created, err := generateProbes(phaseCtx, c, cfg, runID, stopProbing)
					probingDone <- probingResult{created: created, err: err}
				})
				continue
			}
			reconnects := len(tracker.closed)
			tracker.add(sample)
			if len(tracker.closed) > reconnects {
				window := tracker.closed[len(tracker.closed)-1]
				fmt.Printf("Reconnect %d drained after %s\n", len(tracker.closed), window.end.Sub(window.start).Round(time.Millisecond))
			}
			if probingEndsAt.IsZero() && len(tracker.closed) >= cfg.Reconnect.Reconnects {
				probingEndsAt = sample.at.Add(cfg.Reconnect.ProbesAfterReconnects.Duration)
			}
			if stopProbing != nil && !probingEndsAt.IsZero() && !sample.at.Before(probingEndsAt) {
				close(stopProbing)
				stopProbing = nil
			}
		case err := <-samplerDone:
			return nil, nil, err
		case result := <-probingDone:
			probingDone = nil
			if result.err != nil {
				return nil, nil, result.err
			}
			created = result.created
		case event, ok := <-observed:
			if !ok {
				if err := <-watchDone; err != nil {
					return nil, nil, err
				}
				return nil, nil, fmt.Errorf("workload watch ended after %d probe admissions", probes.admittedCount())
			}
			collector := preload
			if isProbe(event.workload.Name) {
				collector = probes
			}
			if err := collector.observe(event.workload, event.observedAt); err != nil {
				return nil, nil, err
			}
		case <-phaseCtx.Done():
			return nil, nil, fmt.Errorf(
				"wait for reconnects: %w (observed %d/%d reconnects, admitted %d probes)",
				phaseCtx.Err(),
				len(tracker.closed),
				cfg.Reconnect.Reconnects,
				probes.admittedCount(),
			)
		}
	}

	summary, err := summarizeReconnects(timeline.probingStarted, tracker, probes)
	if err != nil {
		return nil, nil, err
	}
	return summary, timeline, nil
}

func sampleQueue(ctx context.Context, gatherer prometheus.Gatherer, out chan<- queueSample) error {
	ticker := time.NewTicker(queueSampleInterval)
	defer ticker.Stop()
	for {
		sample, err := gatherQueueSample(gatherer, time.Now())
		if err != nil {
			return err
		}
		select {
		case out <- sample:
		case <-ctx.Done():
			return nil
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return nil
		}
	}
}

type probingResult struct {
	created int
	err     error
}

// generateProbes creates one probe workload per probe interval until stop is closed. It fails
// rather than exceed the quota reserved for probes.
func generateProbes(ctx context.Context, c client.Client, cfg configuration.Config, runID string, stop <-chan struct{}) (int, error) {
	ticker := time.NewTicker(cfg.Reconnect.ProbeInterval.Duration)
	defer ticker.Stop()
	limit := maxProbes(cfg)
	for created := 0; ; created++ {
		if created == limit {
			return created, fmt.Errorf("created %d probes without observing %d reconnects", created, cfg.Reconnect.Reconnects)
		}
		if err := createWorkload(ctx, c, cfg, runID, fmt.Sprintf("%s%06d", probeNamePrefix, created)); err != nil {
			return created, fmt.Errorf("create probe: %w", err)
		}
		select {
		case <-stop:
			return created + 1, nil
		case <-ctx.Done():
			return created + 1, ctx.Err()
		case <-ticker.C:
		}
	}
}

// writeQueueTimeline writes the sampled queue depth by priority and the reconcile counts, one row
// per sample, so the backlog can be plotted over time. Elapsed times are relative to the start of
// probing, like the reconnect windows in the summary, so samples taken before are negative.
func writeQueueTimeline(w io.Writer, timeline *queueTimeline) error {
	priorities := make(map[string]struct{})
	for _, sample := range timeline.samples {
		for priority := range sample.depth {
			priorities[priority] = struct{}{}
		}
	}
	sorted := slices.SortedFunc(maps.Keys(priorities), func(a, b string) int {
		ai, aErr := strconv.Atoi(a)
		bi, bErr := strconv.Atoi(b)
		if aErr != nil || bErr != nil {
			return strings.Compare(a, b)
		}
		return bi - ai
	})

	header := []string{"elapsedMs", "multikueueClusterReconciles", "multikueueWorkloadReconciles"}
	for _, priority := range sorted {
		header = append(header, "depthPriority"+priority)
	}
	if _, err := fmt.Fprintln(w, strings.Join(header, ",")); err != nil {
		return err
	}
	for _, sample := range timeline.samples {
		row := []string{
			strconv.FormatInt(sample.at.Sub(timeline.probingStarted).Milliseconds(), 10),
			strconv.Itoa(sample.clusterReconciles),
			strconv.Itoa(sample.workloadReconciles),
		}
		for _, priority := range sorted {
			row = append(row, strconv.Itoa(sample.depth[priority]))
		}
		if _, err := fmt.Fprintln(w, strings.Join(row, ",")); err != nil {
			return err
		}
	}
	return nil
}
