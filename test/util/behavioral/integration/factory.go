// Copyright The Kubernetes Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package integration

import (
	"context"
	"time"

	"github.com/onsi/gomega"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/cache/scheduler/was"
	"sigs.k8s.io/kueue/pkg/features"
)

// NewManager is a factory for cache.queue.Manager for Integration Tests,
// which configures the Requeuer with a shorter timeout and starts it up.
func NewManager(ctx context.Context, client client.Client, checker qcache.StatusChecker, options ...qcache.Option) *qcache.Manager {
	return NewManagerWithBatchPeriod(ctx, client, checker, 100*time.Millisecond, options...)
}

func NewManagerWithBatchPeriod(ctx context.Context, client client.Client, checker qcache.StatusChecker, batchPeriod time.Duration, options ...qcache.Option) *qcache.Manager {
	requeuer := qcache.NewRequeuer(qcache.WithBatchPeriod(batchPeriod))
	go func() {
		// ignore error to make linter happy.
		_ = requeuer.Start(ctx)
	}()
	return qcache.NewManager(client, checker, requeuer, options...)
}

// SimulatorFactoryCacheOptions mirrors cmd/kueue/main.go: the scheduler cache is
// backed by the WAS simulator only when SchedulerLibraryIntegration is enabled.
func SimulatorFactoryCacheOptions(ctx context.Context, cfg *rest.Config) []schdcache.Option {
	if !features.Enabled(features.SchedulerLibraryIntegration) {
		return nil
	}
	simulatorFactory, err := was.NewWASSimulatorFactory(ctx, cfg)
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred(), "Failed to initialize WAS scheduling simulator")
	return []schdcache.Option{schdcache.WithSimulatorFactory(simulatorFactory)}
}
