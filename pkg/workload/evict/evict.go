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

package evict

import (
	"context"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
	"k8s.io/utils/clock"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/util/api"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	"sigs.k8s.io/kueue/pkg/workload/patching"
)

// workloadsWithPodsReadyToEvictedTime is the amount of time it takes a workload's pods running to getting evicted.
// This measures runtime of workloads that do not run to completion (ie are evicted).
func workloadsWithPodsReadyToEvictedTime(wl *kueue.Workload) *time.Duration {
	var podsReady *time.Time
	if c := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadPodsReady); c != nil && c.Status == metav1.ConditionTrue {
		podsReady = &c.LastTransitionTime.Time
	} else {
		return nil
	}

	var evicted *time.Time
	if c := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadEvicted); c != nil && c.Status == metav1.ConditionTrue {
		evicted = &c.LastTransitionTime.Time
	} else {
		return nil
	}

	return ptr.To(evicted.Sub(*podsReady))
}

func SetEvictedCondition(w *kueue.Workload, now time.Time, reason string, message string) bool {
	condition := metav1.Condition{
		Type:               kueue.WorkloadEvicted,
		Status:             metav1.ConditionTrue,
		LastTransitionTime: metav1.NewTime(now),
		Reason:             reason,
		Message:            api.TruncateConditionMessage(message),
		ObservedGeneration: w.Generation,
	}
	return apimeta.SetStatusCondition(&w.Status.Conditions, condition)
}

// IsEvictedByDeactivation returns true if the workload is evicted by deactivation.
func IsEvictedByDeactivation(w *kueue.Workload) bool {
	cond := apimeta.FindStatusCondition(w.Status.Conditions, kueue.WorkloadEvicted)
	return cond != nil && cond.Status == metav1.ConditionTrue && strings.HasPrefix(cond.Reason, kueue.WorkloadDeactivated)
}

// IsEvictedDueToDeactivationByKueue returns true if the workload is evicted by deactivation by kueue.
func IsEvictedDueToDeactivationByKueue(w *kueue.Workload) bool {
	cond := apimeta.FindStatusCondition(w.Status.Conditions, kueue.WorkloadEvicted)
	return cond != nil && cond.Status == metav1.ConditionTrue &&
		strings.HasPrefix(cond.Reason, patch.ReasonWithCause(kueue.WorkloadDeactivated, ""))
}

func IsEvictedByPodsReadyTimeout(w *kueue.Workload) (*metav1.Condition, bool) {
	cond := apimeta.FindStatusCondition(w.Status.Conditions, kueue.WorkloadEvicted)
	if cond == nil || cond.Status != metav1.ConditionTrue || cond.Reason != kueue.WorkloadEvictedByPodsReadyTimeout {
		return nil, false
	}
	return cond, true
}

func IsEvictedByAdmissionCheck(w *kueue.Workload) (*metav1.Condition, bool) {
	cond := apimeta.FindStatusCondition(w.Status.Conditions, kueue.WorkloadEvicted)
	if cond == nil || cond.Status != metav1.ConditionTrue || cond.Reason != kueue.WorkloadEvictedByAdmissionCheck {
		return nil, false
	}
	return cond, true
}

func IsEvicted(w *kueue.Workload) bool {
	return apimeta.IsStatusConditionTrue(w.Status.Conditions, kueue.WorkloadEvicted)
}

type EvictOption func(*EvictOptions)

type EvictOptions struct {
	CustomPrepare           func(wl *kueue.Workload)
	StrictApply             bool
	RetryOnConflictForPatch bool
	Clock                   clock.Clock
	RoleTracker             *roletracker.RoleTracker
}

func DefaultEvictOptions() *EvictOptions {
	return &EvictOptions{
		CustomPrepare: nil,
		StrictApply:   true,
		Clock:         clock.RealClock{},
	}
}

func WithCustomPrepare(customPrepare func(wl *kueue.Workload)) EvictOption {
	return func(o *EvictOptions) {
		if customPrepare != nil {
			o.CustomPrepare = customPrepare
		}
	}
}

func WithLooseOnApply() EvictOption {
	return func(o *EvictOptions) {
		o.StrictApply = false
	}
}

func WithRetryOnConflictForPatch() EvictOption {
	return func(o *EvictOptions) {
		o.RetryOnConflictForPatch = true
	}
}

func WithClock(clock clock.Clock) EvictOption {
	return func(o *EvictOptions) {
		o.Clock = clock
	}
}

func WithRoleTracker(tracker *roletracker.RoleTracker) EvictOption {
	return func(o *EvictOptions) {
		o.RoleTracker = tracker
	}
}

func Evict(ctx context.Context, c client.Client, recorder record.EventRecorder, wl *kueue.Workload, reason, msg string, underlyingCause kueue.EvictionUnderlyingCause, options ...EvictOption) error {
	opts := DefaultEvictOptions()
	for _, opt := range options {
		opt(opts)
	}

	var (
		hadAdmission              = wl.Status.Admission != nil
		reportWorkloadEvictedOnce bool
	)

	var patchOpts []patch.PatchStatusOption

	if !opts.StrictApply {
		patchOpts = append(patchOpts, patch.WithLooseOnApply())
	}

	if opts.RetryOnConflictForPatch {
		patchOpts = append(patchOpts, patch.WithRetryOnConflict())
	}

	if err := patch.PatchAdmissionStatus(ctx, c, wl, opts.Clock, func(wl *kueue.Workload) (bool, error) {
		if opts.CustomPrepare != nil {
			opts.CustomPrepare(wl)
		}

		evictionReason := reason
		if reason == kueue.WorkloadDeactivated && underlyingCause != "" {
			evictionReason = patch.ReasonWithCause(evictionReason, string(underlyingCause))
		}
		prepareForEviction(wl, opts.Clock.Now(), evictionReason, msg)
		reportWorkloadEvictedOnce = workloadEvictionStateInc(wl, reason, underlyingCause)
		return true, nil
	}, patchOpts...); err != nil {
		return err
	}
	if !hadAdmission {
		// This is an extra safeguard for access to `wl.Status.Admission`.
		// This function is expected to be called only for workload which have
		// Admission.
		log := log.FromContext(ctx)
		log.V(3).Info("WARNING: unexpected eviction of workload without status.Admission", "workload", klog.KObj(wl))
		return nil
	}
	reportEvictedWorkload(recorder, wl, wl.Status.Admission.ClusterQueue, reason, msg, underlyingCause, opts.RoleTracker)
	if reportWorkloadEvictedOnce {
		metrics.ReportEvictedWorkloadsOnce(wl.Status.Admission.ClusterQueue, reason, string(underlyingCause), patch.PriorityClassName(wl), opts.RoleTracker)
	}
	return nil
}

func prepareForEviction(w *kueue.Workload, now time.Time, reason, message string) {
	SetEvictedCondition(w, now, reason, message)
	resetClusterNomination(w)
	resetChecksOnEviction(w, now)
	resetUnhealthyNodes(w)
}

func resetClusterNomination(w *kueue.Workload) {
	w.Status.ClusterName = nil
	w.Status.NominatedClusterNames = nil
}

// resetChecksOnEviction sets all AdmissionChecks to Pending
func resetChecksOnEviction(w *kueue.Workload, now time.Time) {
	checks := w.Status.AdmissionChecks
	for i := range checks {
		if checks[i].State == kueue.CheckStatePending {
			continue
		}
		var retryCount *int32
		if checks[i].State == kueue.CheckStateRetry {
			tmpRetryCount := ptr.Deref(checks[i].RetryCount, 0) + 1
			retryCount = ptr.To(tmpRetryCount)
		}
		checks[i] = kueue.AdmissionCheckState{
			Name:                checks[i].Name,
			State:               kueue.CheckStatePending,
			Message:             "Reset to Pending after eviction. Previously: " + string(checks[i].State),
			LastTransitionTime:  metav1.NewTime(now),
			RequeueAfterSeconds: checks[i].RequeueAfterSeconds,
			RetryCount:          retryCount,
		}
	}
}

func resetUnhealthyNodes(w *kueue.Workload) {
	w.Status.UnhealthyNodes = nil
}

func reportEvictedWorkload(recorder record.EventRecorder, wl *kueue.Workload, cqName kueue.ClusterQueueReference, reason, message string, underlyingCause kueue.EvictionUnderlyingCause, tracker *roletracker.RoleTracker) {
	priorityClassName := patch.PriorityClassName(wl)
	metrics.ReportEvictedWorkloads(cqName, reason, string(underlyingCause), priorityClassName, tracker)
	if podsReadyToEvictionTime := workloadsWithPodsReadyToEvictedTime(wl); podsReadyToEvictionTime != nil {
		metrics.PodsReadyToEvictedTimeSeconds.WithLabelValues(string(cqName), reason, string(underlyingCause), roletracker.GetRole(tracker)).Observe(podsReadyToEvictionTime.Seconds())
	}
	if features.Enabled(features.LocalQueueMetrics) {
		metrics.ReportLocalQueueEvictedWorkloads(
			metrics.LQRefFromWorkload(wl),
			reason,
			string(underlyingCause),
			priorityClassName,
			tracker,
		)
	}
	eventReason := patch.ReasonWithCause(kueue.WorkloadEvicted, reason)
	if reason == kueue.WorkloadDeactivated && underlyingCause != "" {
		eventReason = patch.ReasonWithCause(eventReason, string(underlyingCause))
	}
	recorder.Event(wl, corev1.EventTypeNormal, eventReason, message)
}

func workloadEvictionStateInc(wl *kueue.Workload, reason string, underlyingCause kueue.EvictionUnderlyingCause) bool {
	evictionState := findSchedulingStatsEvictionByReason(wl, reason, underlyingCause)
	if evictionState == nil {
		evictionState = &kueue.WorkloadSchedulingStatsEviction{
			Reason:          reason,
			UnderlyingCause: underlyingCause,
		}
	}
	report := evictionState.Count == 0
	evictionState.Count++
	setSchedulingStatsEviction(wl, *evictionState)
	return report
}

func findSchedulingStatsEvictionByReason(wl *kueue.Workload, reason string, underlyingCause kueue.EvictionUnderlyingCause) *kueue.WorkloadSchedulingStatsEviction {
	if wl.Status.SchedulingStats != nil {
		for i := range wl.Status.SchedulingStats.Evictions {
			if wl.Status.SchedulingStats.Evictions[i].Reason == reason && wl.Status.SchedulingStats.Evictions[i].UnderlyingCause == underlyingCause {
				return &wl.Status.SchedulingStats.Evictions[i]
			}
		}
	}
	return nil
}

func setSchedulingStatsEviction(wl *kueue.Workload, newEvictionState kueue.WorkloadSchedulingStatsEviction) bool {
	if wl.Status.SchedulingStats == nil {
		wl.Status.SchedulingStats = &kueue.SchedulingStats{}
	}
	evictionState := findSchedulingStatsEvictionByReason(wl, newEvictionState.Reason, newEvictionState.UnderlyingCause)
	if evictionState == nil {
		wl.Status.SchedulingStats.Evictions = append(wl.Status.SchedulingStats.Evictions, newEvictionState)
		return true
	}
	if evictionState.Count != newEvictionState.Count {
		evictionState.Count = newEvictionState.Count
		return true
	}
	return false
}
