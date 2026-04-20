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
	"math"
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

type queuedWaitAgg struct {
	n   int
	sum float64
	max float64
}

func (a *queuedWaitAgg) add(clk clock.Clock, wl *kueue.Workload) {
	sec := workload.QueuedWaitTime(wl, clk).Seconds()
	a.n++
	a.sum += sec
	if a.n == 1 || sec > a.max {
		a.max = sec
	}
}

func (a queuedWaitAgg) finalize() (max, mean float64) {
	if a.n == 0 {
		return 0, 0
	}
	return a.max, a.sum / float64(a.n)
}

func maxQueuedWaitAcross(a, b queuedWaitAgg) float64 {
	switch {
	case a.n == 0:
		return b.max
	case b.n == 0:
		return a.max
	default:
		return math.Max(a.max, b.max)
	}
}

// pendingWaitAggregates computes max/mean queued wait times for active vs inadmissible pending workloads.
// When cqSchedulingActive is false (ClusterQueue inactive for scheduling), active workloads are folded into the
// inadmissible bucket — matching ReportPendingWorkloads counts semantics.
//
// Caller must NOT hold ClusterQueue locks; this method acquires RLock internally.
func (c *ClusterQueue) pendingWaitAggregates(clk clock.Clock, cqSchedulingActive bool) (activeMax, activeMean float64, activeN int, inadmMax, inadmMean float64, inadmN int) {
	c.rwm.RLock()
	defer c.rwm.RUnlock()

	var active, inadm queuedWaitAgg
	for _, wi := range c.heap.List() {
		active.add(clk, wi.Obj)
	}
	if c.inflight != nil {
		active.add(clk, c.inflight.Obj)
	}
	for _, wi := range c.inadmissibleWorkloads {
		inadm.add(clk, wi.Obj)
	}

	if !cqSchedulingActive {
		totalN := active.n + inadm.n
		if totalN == 0 {
			return 0, 0, 0, 0, 0, 0
		}
		sum := active.sum + inadm.sum
		inadmMean = sum / float64(totalN)
		inadmMax = maxQueuedWaitAcross(active, inadm)
		return 0, 0, 0, inadmMax, inadmMean, totalN
	}

	amx, amn := active.finalize()
	imx, imn := inadm.finalize()
	return amx, amn, active.n, imx, imn, inadm.n
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

func (w *PendingWaitMetricsWorker) enqueueAllClusterQueues() {
	if w.manager == nil {
		return
	}
	for _, name := range w.manager.GetClusterQueueNames() {
		w.notify(name)
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
	activeMax, activeMean, activeN, inadmMax, inadmMean, inadmN := cq.pendingWaitAggregates(m.clock, cqSchedulingActive)
	metrics.ReportPendingWorkloadWaitTimes(cqName, activeMax, activeMean, activeN, inadmMax, inadmMean, inadmN, m.customLabels.CQGet(cq.name), m.roleTracker)
}
