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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/clock"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
)

// NewManager is a factory for cache.queue.Manager for Integration Tests,
// which configures the Requeuer with a shorter timeout and starts it up.
func NewManager(ctx context.Context, client client.Client, checker qcache.StatusChecker, options ...qcache.Option) *qcache.Manager {
	return NewManagerWithRequeuerOption(ctx, client, checker, qcache.WithInadmissibleWorkloadRequeuerConfig(&configapi.InadmissibleWorkloadRequeuer{
		BatchPeriod: &metav1.Duration{Duration: 100 * time.Millisecond},
	}), options...)
}

// NewManagerWithRequeuerOption is like NewManager, but the caller chooses how
// the Requeuer is configured.
func NewManagerWithRequeuerOption(ctx context.Context, client client.Client, checker qcache.StatusChecker, requeuerOption qcache.RequeuerOption, options ...qcache.Option) *qcache.Manager {
	requeuer := qcache.NewRequeuer(requeuerOption)
	go func() {
		// ignore error to make linter happy.
		_ = requeuer.Start(ctx)
	}()
	return qcache.NewManager(client, checker, requeuer, options...)
}

// NewManagerWithClock configures both the Manager and its inadmissible Requeuer
// with the given batchPeriod and clk.
func NewManagerWithClock(
	ctx context.Context,
	client client.Client,
	checker qcache.StatusChecker,
	batchPeriod time.Duration,
	clk clock.WithTickerAndDelayedExecution,
	options ...qcache.Option,
) *qcache.Manager {
	requeuer := qcache.NewRequeuer(
		qcache.WithInadmissibleWorkloadRequeuerConfig(&configapi.InadmissibleWorkloadRequeuer{
			BatchPeriod: &metav1.Duration{Duration: batchPeriod},
		}),
		qcache.WithRequeuerClock(clk),
	)
	go func() {
		// ignore error to make linter happy.
		_ = requeuer.Start(ctx)
	}()
	options = append([]qcache.Option{qcache.WithClock(clk)}, options...)
	return qcache.NewManager(client, checker, requeuer, options...)
}
