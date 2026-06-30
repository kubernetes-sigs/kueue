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
	"k8s.io/client-go/tools/events"
	"k8s.io/klog/v2"
	"k8s.io/utils/clock"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/util/api"
	utilqueue "sigs.k8s.io/kueue/pkg/util/queue"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	"sigs.k8s.io/kueue/pkg/workload/patching"
)

// IsEvictedByDeactivation returns true if the workload is evicted by deactivation.
func IsEvictedByDeactivation(w *kueue.Workload) bool {
	cond := apimeta.FindStatusCondition(w.Status.Conditions, kueue.WorkloadEvicted)
	return cond != nil && cond.Status == metav1.ConditionTrue && strings.HasPrefix(cond.Reason, kueue.WorkloadDeactivated)
}

// IsEvictedDueToDeactivationByKueue returns true if the workload is evicted by deactivation by kueue.
func IsEvictedDueToDeactivationByKueue(w *kueue.Workload) bool {
	cond := apimeta.FindStatusCondition(w.Status.Conditions, kueue.WorkloadEvicted)
	return cond != nil && cond.Status == metav1.ConditionTrue &&
		strings.HasPrefix(cond.Reason, patching.ReasonWithCause(kueue.WorkloadDeactivated, ""))
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

	return new(evicted.Sub(*podsReady))
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

type Option func(*Options)

type Options struct {
	CustomPrepare           func(wl *kueue.Workload)
	StrictApply             bool
	RetryOnConflictForPatch bool
}

func defaultOptions() *Options {
	return &Options{
		CustomPrepare: nil,
		StrictApply:   true,
	}
}

func WithCustomPrepare(customPrepare func(wl *kueue.Workload)) Option {
	return func(o *Options) {
		if customPrepare != nil {
			o.CustomPrepare = customPrepare
		}
	}
}

func WithLooseOnApply() Option {
	return func(o *Options) {
		o.StrictApply = false
	}
}

func WithRetryOnConflict() Option {
	return func(o *Options) {
		o.RetryOnConflictForPatch = true
	}
}

func Evict(
	ctx context.Context,
	c client.Client,
	recorder events.EventRecorder,
	wl *kueue.Workload,
	reason, msg string,
	underlyingCause kueue.EvictionUnderlyingCause,
	clock clock.Clock,
	exposeLqMetrics bool,
	tracker *roletracker.RoleTracker,
	cl *metrics.CustomLabels,
	options ...Option,
) error {
	opts := defaultOptions()
	for _, opt := range options {
		opt(opts)
	}

	var (
		hadAdmission              = wl.Status.Admission != nil
		reportWorkloadEvictedOnce bool
	)

	var patchOpts []patching.PatchStatusOption

	if !opts.StrictApply {
		patchOpts = append(patchOpts, patching.WithLooseOnApply())
	}

	if opts.RetryOnConflictForPatch {
		patchOpts = append(patchOpts, patching.WithRetryOnConflict())
	}

	if err := patching.PatchAdmissionStatus(ctx, c, wl, clock, func(wl *kueue.Workload) (bool, error) {
		if opts.CustomPrepare != nil {
			opts.CustomPrepare(wl)
		}

		evictionReason := reason
		if reason == kueue.WorkloadDeactivated && underlyingCause != "" {
			evictionReason = patching.ReasonWithCause(evictionReason, string(underlyingCause))
		}
		prepareForEviction(wl, clock.Now(), evictionReason, msg)
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
	reportEvictedWorkload(recorder, wl, wl.Status.Admission.ClusterQueue, reason, msg, underlyingCause, exposeLqMetrics, tracker, cl)
	if reportWorkloadEvictedOnce {
		metrics.ReportEvictedWorkloadsOnce(wl.Status.Admission.ClusterQueue, reason, string(underlyingCause), patching.PriorityClassName(wl), cl.CQGet(wl.Status.Admission.ClusterQueue), tracker)
	}
	return nil
}

func prepareForEviction(w *kueue.Workload, now time.Time, reason, message string) {
	SetEvictedCondition(w, now, reason, message)
	resetClusterNomination(w)
	resetChecksOnEviction(w, now)
	resetUnhealthyNodes(w)
	unsetBlockedOnPreemptionGatesCondition(w, now, reason, message)
	closeAllPreemptionGates(w, now)
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
			retryCount = new(tmpRetryCount)
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

func unsetBlockedOnPreemptionGatesCondition(w *kueue.Workload, now time.Time, reason, message string) {
	preemptionSignalCond := apimeta.FindStatusCondition(w.Status.Conditions, kueue.WorkloadBlockedOnPreemptionGates)
	if preemptionSignalCond == nil || preemptionSignalCond.Status != metav1.ConditionTrue {
		return
	}

	condition := metav1.Condition{
		Type:               kueue.WorkloadBlockedOnPreemptionGates,
		Status:             metav1.ConditionFalse,
		LastTransitionTime: metav1.NewTime(now),
		Reason:             reason,
		Message:            api.TruncateConditionMessage(message),
		ObservedGeneration: w.Generation,
	}
	apimeta.SetStatusCondition(&w.Status.Conditions, condition)
}

func closeAllPreemptionGates(w *kueue.Workload, now time.Time) {
	for i := range w.Status.PreemptionGates {
		w.Status.PreemptionGates[i].Position = kueue.PreemptionGatePositionClosed
		w.Status.PreemptionGates[i].LastTransitionTime = metav1.NewTime(now)
	}
}

func reportEvictedWorkload(recorder events.EventRecorder, wl *kueue.Workload, cqName kueue.ClusterQueueReference,
	reason, message string, underlyingCause kueue.EvictionUnderlyingCause, exposeLqMetrics bool,
	tracker *roletracker.RoleTracker, cl *metrics.CustomLabels,
) {
	priorityClassName := patching.PriorityClassName(wl)
	cqCustomLabels := cl.CQGet(cqName)
	metrics.ReportEvictedWorkloads(cqName, reason, string(underlyingCause), priorityClassName, cqCustomLabels, tracker)
	if podsReadyToEvictionTime := workloadsWithPodsReadyToEvictedTime(wl); podsReadyToEvictionTime != nil {
		metrics.ReportPodsReadyToEvictedTimeSeconds(cqName, reason, string(underlyingCause), *podsReadyToEvictionTime, cqCustomLabels, tracker)
	}
	if exposeLqMetrics {
		lqRef := metrics.LQRefFromWorkload(wl)
		metrics.ReportLocalQueueEvictedWorkloads(
			lqRef,
			reason,
			string(underlyingCause),
			priorityClassName,
			cl.LQGet(utilqueue.KeyFromWorkload(wl)),
			tracker,
		)
	}
	eventReason := patching.ReasonWithCause(kueue.WorkloadEvicted, reason)
	if reason == kueue.WorkloadDeactivated && underlyingCause != "" {
		eventReason = patching.ReasonWithCause(eventReason, string(underlyingCause))
	}
	recorder.Eventf(wl, nil, corev1.EventTypeNormal, eventReason, eventReason, api.TruncateEventMessage(message))
}

func ReportPreemption(preemptingCqName kueue.ClusterQueueReference, preemptingReason string, targetCqName kueue.ClusterQueueReference, tracker *roletracker.RoleTracker, cl *metrics.CustomLabels) {
	metrics.ReportPreemption(preemptingCqName, preemptingReason, targetCqName, cl.CQGet(preemptingCqName), tracker)
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
