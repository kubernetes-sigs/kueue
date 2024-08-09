/*
Copyright 2022 The Kubernetes Authors.

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

package core

import (
	"time"

	ctrl "sigs.k8s.io/controller-runtime"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta1"
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/queue"
)

const (
	updateChBuffer = 10
)

// SetupControllers sets up the core controllers. It returns the name of the
// controller that failed to create and an error, if any.
func SetupControllers(mgr ctrl.Manager, qManager *queue.Manager, cc *cache.Cache, cfg *configapi.Configuration) (string, error) {
	rfRec := NewResourceFlavorReconciler(mgr.GetClient(), qManager, cc)
	if err := rfRec.SetupWithManager(mgr, cfg); err != nil {
		return "ResourceFlavor", err
	}
	acRec := NewAdmissionCheckReconciler(mgr.GetClient(), qManager, cc)
	if err := acRec.SetupWithManager(mgr, cfg); err != nil {
		return "AdmissionCheck", err
	}
	qRec := NewLocalQueueReconciler(mgr.GetClient(), qManager, cc)
	if err := qRec.SetupWithManager(mgr, cfg); err != nil {
		return "LocalQueue", err
	}

	var fairSharingEnabled bool
	if cfg.FairSharing != nil {
		fairSharingEnabled = cfg.FairSharing.Enable
	}

	cqRec := NewClusterQueueReconciler(
		mgr.GetClient(),
		qManager,
		cc,
		WithQueueVisibilityUpdateInterval(queueVisibilityUpdateInterval(cfg)),
		WithQueueVisibilityClusterQueuesMaxCount(queueVisibilityClusterQueuesMaxCount(cfg)),
		WithReportResourceMetrics(cfg.Metrics.EnableClusterQueueResources),
		WithFairSharing(fairSharingEnabled),
		WithWatchers(rfRec, acRec),
	)
	if err := mgr.Add(cqRec); err != nil {
		return "Unable to add ClusterQueue to manager", err
	}
	rfRec.AddUpdateWatcher(cqRec)
	acRec.AddUpdateWatchers(cqRec)
	if err := cqRec.SetupWithManager(mgr, cfg); err != nil {
		return "ClusterQueue", err
	}

	if err := NewWorkloadReconciler(mgr.GetClient(), qManager, cc,
		mgr.GetEventRecorderFor(constants.WorkloadControllerName),
		WithWorkloadUpdateWatchers(qRec, cqRec),
		WithWaitForPodsReady(waitForPodsReady(cfg.WaitForPodsReady)),
		WithWorkloadObjectRetention(cfg.ObjectRetentionPolicies.FinishedWorkloadRetention),
	).SetupWithManager(mgr, cfg); err != nil {
		return "Workload", err
	}
	return "", nil
}

func waitForPodsReady(cfg *configapi.WaitForPodsReady) *waitForPodsReadyConfig {
	if cfg == nil || !cfg.Enable {
		return nil
	}
	result := waitForPodsReadyConfig{
		timeout: cfg.Timeout.Duration,
	}
	if cfg.RequeuingStrategy != nil {
		result.requeuingBackoffBaseSeconds = *cfg.RequeuingStrategy.BackoffBaseSeconds
		result.requeuingBackoffLimitCount = cfg.RequeuingStrategy.BackoffLimitCount
		result.requeuingBackoffMaxDuration = time.Duration(*cfg.RequeuingStrategy.BackoffMaxSeconds) * time.Second
		result.requeuingBackoffJitter = 0.0001
	}
	return &result
}

func queueVisibilityUpdateInterval(cfg *configapi.Configuration) time.Duration {
	if cfg.QueueVisibility != nil {
		return time.Duration(cfg.QueueVisibility.UpdateIntervalSeconds) * time.Second
	}
	return 0
}

func queueVisibilityClusterQueuesMaxCount(cfg *configapi.Configuration) int32 {
	if cfg.QueueVisibility != nil && cfg.QueueVisibility.ClusterQueues != nil {
		return cfg.QueueVisibility.ClusterQueues.MaxCount
	}
	return 0
}
