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

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/core"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/controller/tas"
	tasindexer "sigs.k8s.io/kueue/pkg/controller/tas/indexer"
	"sigs.k8s.io/kueue/pkg/dra"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/scheduler"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
	utildra "sigs.k8s.io/kueue/pkg/util/dra"
)

// The DRA objects the performance tests generate, and the quota resource they map to.
const (
	DRADriverName      = "gpu.perf.example.com"
	DRADeviceClassName = "gpu.perf.example.com"
	DRAResourceName    = corev1.ResourceName("perf.example.com/gpu")
)

// DRADeviceClassMappings maps the generated DeviceClass to DRAResourceName.
func DRADeviceClassMappings() []configapi.DeviceClassMapping {
	return []configapi.DeviceClassMapping{{
		Name:             DRAResourceName,
		DeviceClassNames: []corev1.ResourceName{DRADeviceClassName},
	}}
}

// Setup registers the core indexers, controllers and scheduler, optionally including TAS.
// DRA is wired as cmd/kueue/main.go wires it, but only when the configuration has
// deviceClassMappings, so the configurations without DRA measure what they did before.
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

	var draMapper *dra.ResourceMapper
	var draBackedResources *dra.ExtendedResourceCache
	var resourceSliceAPIAvailable bool
	var cacheOptions []schdcache.Option
	var queueOptions []qcache.Option
	if features.Enabled(features.KueueDRAIntegration) && cfg.Resources != nil && len(cfg.Resources.DeviceClassMappings) > 0 {
		draMapper = dra.NewResourceMapper()
		if err := draMapper.PopulateFromConfiguration(cfg.Resources.DeviceClassMappings); err != nil {
			return fmt.Errorf("populate DRA mapper: %w", err)
		}
		draBackedResources = dra.NewExtendedResourceCache()
		cacheOptions = append(cacheOptions, schdcache.WithDRABackedResources(draBackedResources))
		queueOptions = append(queueOptions, qcache.WithDRABackedResources(draBackedResources))
		resourceSliceAPIAvailable = utildra.CheckResourceSliceAPIAvailable(mgr)
		if resourceSliceAPIAvailable {
			if err := core.SetupResourceSliceIndexer(ctx, mgr.GetFieldIndexer()); err != nil {
				return fmt.Errorf("setup ResourceSlice indexer: %w", err)
			}
		}
	}

	schedulerCache := schdcache.New(mgr.GetClient(), cacheOptions...)
	requeuer := qcache.NewRequeuer()
	if err := mgr.Add(requeuer); err != nil {
		return fmt.Errorf("add workload requeuer: %w", err)
	}

	preemptionExpectations := preemptexpectations.New()
	queueOptions = append(queueOptions, qcache.WithPreemptionExpectations(preemptionExpectations))
	queues := qcache.NewManager(
		mgr.GetClient(),
		schedulerCache,
		requeuer,
		queueOptions...,
	)
	go queues.CleanUpOnContext(ctx)
	go schedulerCache.CleanUpOnContext(ctx)

	if failedController, err := core.SetupControllers(
		mgr,
		queues,
		schedulerCache,
		cfg,
		core.SetupControllersOpts{
			PreemptionExpectations:    preemptionExpectations,
			DRAMapper:                 draMapper,
			DRABackedResources:        draBackedResources,
			ResourceSliceAPIAvailable: resourceSliceAPIAvailable,
		},
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
	)
	if err := mgr.Add(sched); err != nil {
		return fmt.Errorf("add scheduler: %w", err)
	}
	return nil
}
