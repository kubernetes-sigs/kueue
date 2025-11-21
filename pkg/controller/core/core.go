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

package core

import (
	"time"

	ctrl "sigs.k8s.io/controller-runtime"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/failurerecovery"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption/fairsharing"
	"sigs.k8s.io/kueue/pkg/util/waitforpodsready"
)

const (
	updateChBuffer = 10
)

// SetupControllers sets up the core controllers. It returns the name of the
// controller that failed to create and an error, if any.
func SetupControllers(mgr ctrl.Manager, qManager *qcache.Manager, cc *schdcache.Cache, cfg *configapi.Configuration) (string, error) {
	rfRec := NewResourceFlavorReconciler(mgr.GetClient(), qManager, cc)
	if err := rfRec.SetupWithManager(mgr, cfg); err != nil {
		return "ResourceFlavor", err
	}
	acRec := NewAdmissionCheckReconciler(mgr.GetClient(), qManager, cc)
	if err := acRec.SetupWithManager(mgr, cfg); err != nil {
		return "AdmissionCheck", err
	}
	qRec := NewLocalQueueReconciler(mgr.GetClient(), qManager, cc,
		WithAdmissionFairSharingConfig(cfg.AdmissionFairSharing))
	if err := qRec.SetupWithManager(mgr, cfg); err != nil {
		return "LocalQueue", err
	}

	fairSharingEnabled := fairsharing.Enabled(cfg.FairSharing)
	watchers := []ClusterQueueUpdateWatcher{rfRec, acRec}
	if features.Enabled(features.HierarchicalCohorts) {
		cohortRec := NewCohortReconciler(mgr.GetClient(), cc, qManager,
			CohortReconcilerWithFairSharing(fairSharingEnabled))
		if err := cohortRec.SetupWithManager(mgr, cfg); err != nil {
			return "Cohort", err
		}
		watchers = append(watchers, cohortRec)
	}

	if features.Enabled(features.FailureRecoveryPolicy) {
		tpRec, err := failurerecovery.NewTerminatingPodReconciler(
			mgr.GetClient(),
			mgr.GetEventRecorderFor(constants.PodTerminationControllerName),
		)
		if err != nil {
			return "FailureRecoveryPolicy", err
		}

		if err := tpRec.SetupWithManager(mgr); err != nil {
			return "FailureRecoveryPolicy", err
		}
	}

	cqRec := NewClusterQueueReconciler(
		mgr.GetClient(),
		qManager,
		cc,
		WithReportResourceMetrics(cfg.Metrics.EnableClusterQueueResources),
		WithFairSharing(fairSharingEnabled),
		WithWatchers(watchers...),
	)
	rfRec.AddUpdateWatcher(cqRec)
	acRec.AddUpdateWatchers(cqRec)
	if err := cqRec.SetupWithManager(mgr, cfg); err != nil {
		return "ClusterQueue", err
	}

	workloadRec := NewWorkloadReconciler(mgr.GetClient(), qManager, cc,
		mgr.GetEventRecorderFor(constants.WorkloadControllerName),
		WithWorkloadUpdateWatchers(qRec, cqRec),
		WithWaitForPodsReady(waitForPodsReady(cfg.WaitForPodsReady)),
		WithWorkloadRetention(workloadRetention(cfg.ObjectRetentionPolicies)),
	)
	if features.Enabled(features.DynamicResourceAllocation) {
		qManager.SetDRAReconcileChannel(workloadRec.GetDRAReconcileChannel())
	}

	if err := workloadRec.SetupWithManager(mgr, cfg); err != nil {
		return "Workload", err
	}
	qManager.AddTopologyUpdateWatcher(cqRec)
	qManager.AddWorkloadUpdateWatcher(qRec)
	return "", nil
}

func waitForPodsReady(cfg *configapi.WaitForPodsReady) *waitForPodsReadyConfig {
	if !waitforpodsready.Enabled(cfg) {
		return nil
	}
	result := waitForPodsReadyConfig{
		timeout: cfg.Timeout.Duration,
	}
	if cfg.RecoveryTimeout != nil {
		result.recoveryTimeout = &cfg.RecoveryTimeout.Duration
	}
	if cfg.RequeuingStrategy != nil {
		result.requeuingBackoffBaseSeconds = *cfg.RequeuingStrategy.BackoffBaseSeconds
		result.requeuingBackoffLimitCount = cfg.RequeuingStrategy.BackoffLimitCount
		result.requeuingBackoffMaxDuration = time.Duration(*cfg.RequeuingStrategy.BackoffMaxSeconds) * time.Second
		result.requeuingBackoffJitter = 0.0001
	}
	return &result
}

func workloadRetention(cfg *configapi.ObjectRetentionPolicies) *workloadRetentionConfig {
	if cfg == nil || cfg.Workloads == nil || cfg.Workloads.AfterFinished == nil {
		return nil
	}

	return &workloadRetentionConfig{
		afterFinished: &cfg.Workloads.AfterFinished.Duration,
	}
}
