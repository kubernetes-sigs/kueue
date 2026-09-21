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

package controllers

import (
	"context"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/manager"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/core"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/controller/tas"
	tasindexer "sigs.k8s.io/kueue/pkg/controller/tas/indexer"
	"sigs.k8s.io/kueue/pkg/scheduler"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption/fairsharing"
)

// Setup registers the core indexers, controllers and scheduler, optionally including TAS.
// The caller supplies the configuration and owns the manager's startup and context lifetime.
func Setup(ctx context.Context, mgr manager.Manager, cfg *configapi.Configuration, enableTAS bool) error {
	if err := indexer.Setup(ctx, mgr.GetFieldIndexer()); err != nil {
		return fmt.Errorf("setup core indexers: %w", err)
	}
	if enableTAS {
		if err := tasindexer.SetupIndexes(ctx, mgr.GetFieldIndexer()); err != nil {
			return fmt.Errorf("setup TAS indexers: %w", err)
		}
	}

	var cacheOptions []schdcache.Option
	if cfg.FairSharing != nil {
		cacheOptions = append(cacheOptions, schdcache.WithFairSharing(fairsharing.Enabled(cfg.FairSharing)))
	}
	schedulerCache := schdcache.New(mgr.GetClient(), cacheOptions...)
	requeuer := qcache.NewRequeuer()
	if err := mgr.Add(requeuer); err != nil {
		return fmt.Errorf("add workload requeuer: %w", err)
	}

	preemptionExpectations := preemptexpectations.New()
	queues := qcache.NewManager(
		mgr.GetClient(),
		schedulerCache,
		requeuer,
		qcache.WithPreemptionExpectations(preemptionExpectations),
	)
	go queues.CleanUpOnContext(ctx)
	go schedulerCache.CleanUpOnContext(ctx)

	if failedController, err := core.SetupControllers(
		mgr,
		queues,
		schedulerCache,
		cfg,
		core.SetupControllersOpts{PreemptionExpectations: preemptionExpectations},
	); err != nil {
		return fmt.Errorf("setup core controller %s: %w", failedController, err)
	}
	if enableTAS {
		if failedController, err := tas.SetupControllers(mgr, queues, schedulerCache, cfg, nil); err != nil {
			return fmt.Errorf("setup TAS controller %s: %w", failedController, err)
		}
	}

	sched := scheduler.New(
		queues,
		schedulerCache,
		mgr.GetClient(),
		mgr.GetEventRecorder(constants.AdmissionName),
		scheduler.WithPreemptionExpectations(preemptionExpectations),
		scheduler.WithFairSharing(cfg.FairSharing),
	)
	if err := mgr.Add(sched); err != nil {
		return fmt.Errorf("add scheduler: %w", err)
	}
	return nil
}
