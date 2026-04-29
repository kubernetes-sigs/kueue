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
	"math/rand/v2"
	"slices"
	"time"

	"k8s.io/client-go/util/workqueue"
	"k8s.io/utils/clock"
	ctrl "sigs.k8s.io/controller-runtime"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/workload"
)

const (
	pendingWaitMetricsBatchPeriod = time.Second
	pendingWaitMetricsTickPeriod  = 15 * time.Second
)

// queuedWaitSamples accumulates individual queued-wait durations (in seconds)
// for a single bucket (active or inadmissible) within a ClusterQueue.
// After collection, the samples are sorted and quantiles are computed.
type queuedWaitSamples struct {
	samples []float64
}

// add records one workload's queued-wait duration from the given clock.
func (s *queuedWaitSamples) add(clk clock.Clock, wl *kueue.Workload) {
	sec := workload.QueuedWaitTime(wl, clk).Seconds()
	s.samples = append(s.samples, sec)
}

// quantiles computes p50, p95, p99 from the collected samples using the nearest-rank method.
// Returns zero values if no samples were collected.
func (s *queuedWaitSamples) quantiles() metrics.PendingWorkloadWaitQuantiles {
	n := len(s.samples)
	if n == 0 {
		return metrics.PendingWorkloadWaitQuantiles{}
	}
	slices.Sort(s.samples)
	return metrics.PendingWorkloadWaitQuantiles{
		P50: quantileFromSorted(s.samples, 0.50),
		P95: quantileFromSorted(s.samples, 0.95),
		P99: quantileFromSorted(s.samples, 0.99),
		N:   n,
	}
}

// quantileFromSorted returns the q-th quantile from a pre-sorted slice
// using the nearest-rank (ceiling) method.
func quantileFromSorted(sorted []float64, q float64) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	// Nearest-rank: rank = ceil(q * n) - 1 (0-indexed).
	rank := max(int(q*float64(n)+0.5)-1, 0)
	if rank >= n {
		rank = n - 1
	}
	return sorted[rank]
}

// pendingWaitQuantiles computes quantile-based queued wait times for active vs inadmissible pending workloads.
// When cqSchedulingActive is false (ClusterQueue inactive for scheduling), active workloads are folded into the
// inadmissible bucket — matching ReportPendingWorkloads counts semantics.
//
// Caller must NOT hold ClusterQueue locks; this method acquires RLock internally.
func (c *ClusterQueue) pendingWaitQuantiles(clk clock.Clock, cqSchedulingActive bool) (active, inadm metrics.PendingWorkloadWaitQuantiles) {
	c.rwm.RLock()
	defer c.rwm.RUnlock()

	var activeSamples, inadmSamples queuedWaitSamples
	for _, wi := range c.heap.List() {
		activeSamples.add(clk, wi.Obj)
	}
	if c.inflight != nil {
		activeSamples.add(clk, c.inflight.Obj)
	}
	for _, wi := range c.inadmissibleWorkloads {
		inadmSamples.add(clk, wi.Obj)
	}

	if !cqSchedulingActive {
		// Fold all samples into the inadmissible bucket.
		merged := queuedWaitSamples{
			samples: append(activeSamples.samples, inadmSamples.samples...),
		}
		return metrics.PendingWorkloadWaitQuantiles{}, merged.quantiles()
	}

	return activeSamples.quantiles(), inadmSamples.quantiles()
}

type pendingWaitMetricsOptions struct {
	batchPeriod time.Duration
	tickPeriod  time.Duration
}

// PendingWaitMetricsOption configures the pending wait metrics worker.
type PendingWaitMetricsOption func(*pendingWaitMetricsOptions)

// WithPendingWaitMetricsBatchPeriod sets the delay-queue batching window (default 1s).
func WithPendingWaitMetricsBatchPeriod(d time.Duration) PendingWaitMetricsOption {
	return func(o *pendingWaitMetricsOptions) {
		o.batchPeriod = d
	}
}

// WithPendingWaitMetricsTickPeriod sets how often all ClusterQueues are refreshed for monotonic wait time (default 15s).
func WithPendingWaitMetricsTickPeriod(d time.Duration) PendingWaitMetricsOption {
	return func(o *pendingWaitMetricsOptions) {
		o.tickPeriod = d
	}
}

// PendingWaitMetricsWorker batches ClusterQueue notifications and periodically refreshes all queues,
// mirroring workqueueRequeuer. Heavy aggregation runs only in this worker's goroutine.
type PendingWaitMetricsWorker struct {
	manager     *Manager
	queue       workqueue.TypedDelayingInterface[kueue.ClusterQueueReference]
	batchPeriod time.Duration
	tickPeriod  time.Duration
}

// NewPendingWaitMetricsWorker constructs a Runnable that updates pending wait time gauges.
func NewPendingWaitMetricsWorker(opts ...PendingWaitMetricsOption) *PendingWaitMetricsWorker {
	options := pendingWaitMetricsOptions{
		batchPeriod: pendingWaitMetricsBatchPeriod,
		tickPeriod:  pendingWaitMetricsTickPeriod,
	}
	for _, opt := range opts {
		opt(&options)
	}
	return &PendingWaitMetricsWorker{
		queue:       workqueue.NewTypedDelayingQueue[kueue.ClusterQueueReference](),
		batchPeriod: options.batchPeriod,
		tickPeriod:  options.tickPeriod,
	}
}

func (w *PendingWaitMetricsWorker) setManager(manager *Manager) {
	w.manager = manager
}

// notify schedules a single ClusterQueue for metrics re-evaluation after the batch period.
// Used on event-driven paths (workload add/remove); no jitter is applied here.
func (w *PendingWaitMetricsWorker) notify(cqName kueue.ClusterQueueReference) {
	w.queue.AddAfter(cqName, w.batchPeriod)
}

// Start implements manager.Runnable.
func (w *PendingWaitMetricsWorker) Start(ctx context.Context) error {
	log := ctrl.LoggerFrom(ctx).WithName("pending_wait_metrics_worker")
	ctx = ctrl.LoggerInto(ctx, log)
	go func() {
		<-ctx.Done()
		w.queue.ShutDown()
	}()

	w.enqueueAllClusterQueues()

	ticker := time.NewTicker(w.tickPeriod)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				w.enqueueAllClusterQueues()
			}
		}
	}()

	for {
		item, shutdown := w.queue.Get()
		if shutdown {
			return nil
		}
		w.reconcile(ctx, item)
		w.queue.Done(item)
	}
}

// enqueueAllClusterQueues schedules all known ClusterQueues for metrics re-evaluation.
// A random jitter of up to 50% of the tick period is added per CQ to spread reconcile
// work and avoid processing all queues at the same instant.
func (w *PendingWaitMetricsWorker) enqueueAllClusterQueues() {
	if w.manager == nil {
		return
	}
	maxJitter := int64(w.tickPeriod / 2)
	for _, name := range w.manager.GetClusterQueueNames() {
		jitter := time.Duration(rand.Int64N(maxJitter))
		w.queue.AddAfter(name, w.batchPeriod+jitter)
	}
}

func (w *PendingWaitMetricsWorker) reconcile(ctx context.Context, cqName kueue.ClusterQueueReference) {
	_ = ctx
	m := w.manager
	if m == nil {
		return
	}
	cq := m.getClusterQueue(cqName)
	if cq == nil {
		metrics.ClearPendingWorkloadWaitTimes(cqName)
		return
	}
	cqSchedulingActive := m.statusChecker == nil || m.statusChecker.ClusterQueueActive(cqName)
	active, inadm := cq.pendingWaitQuantiles(m.clock, cqSchedulingActive)
	metrics.ReportPendingWorkloadWaitTimes(cqName, active, inadm, m.customLabels.CQGet(cqName), m.roleTracker)
}
