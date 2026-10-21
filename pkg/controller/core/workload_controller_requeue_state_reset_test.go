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
	"errors"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/component-base/featuregate"
	testingclock "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	controllerconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestReconcileRequeueStateReset(t *testing.T) {
	errStatus := errors.New("status write failed")
	// the clock is primarily used with second rounded times
	// use the current time trimmed.
	now := time.Now().Truncate(time.Second)
	fakeClock := testingclock.NewFakeClock(now)

	timeoutConfig := &waitForPodsReadyConfig{
		timeout:                     5 * time.Minute,
		requeuingBackoffLimitCount:  new(int32(100)),
		requeuingBackoffBaseSeconds: 10,
		requeuingBackoffJitter:      0,
		requeuingBackoffMaxDuration: time.Hour,
	}

	// readyWl returns an admitted workload which is PodsReady for the
	// given duration and carries a requeueState left by earlier evictions.
	readyWl := func(readyFor time.Duration) *utiltestingapi.WorkloadWrapper {
		return utiltestingapi.MakeWorkload("wl", "ns").
			ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "ownername", "owneruid").
			ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
			AdmittedAt(true, now).
			Condition(metav1.Condition{
				Type:               kueue.WorkloadPodsReady,
				Status:             metav1.ConditionTrue,
				Reason:             "PodsReady",
				Message:            "All pods are ready or succeeded",
				LastTransitionTime: metav1.NewTime(now.Add(-readyFor)),
			}).
			RequeueState(new(int32(3)), nil)
	}

	// notReadyWl is an admitted workload which waits for its pods to start and
	// carries a requeueState left by earlier evictions.
	notReadyWl := readyWl(0).Condition(metav1.Condition{
		Type:               kueue.WorkloadPodsReady,
		Status:             metav1.ConditionFalse,
		Reason:             kueue.WorkloadWaitForStart,
		Message:            "Not all pods are ready or succeeded",
		LastTransitionTime: metav1.NewTime(now.Add(-time.Minute)),
	})

	// evictedWl has been preempted but is still admitted while it drains, and
	// its PodsReady condition is left over from before the eviction.
	evictedWl := readyWl(6 * time.Minute).Condition(metav1.Condition{
		Type:               kueue.WorkloadEvicted,
		Status:             metav1.ConditionTrue,
		Reason:             kueue.WorkloadEvictedByPreemption,
		Message:            "Preempted to accommodate a workload",
		LastTransitionTime: metav1.NewTime(now.Add(-time.Minute)),
	})

	// checksPendingWl has its quota reserved but is not admitted, because the
	// AdmissionCheck of its ClusterQueue is Pending. Its PodsReady condition
	// is left over from an earlier admission.
	checksPendingWl := utiltestingapi.MakeWorkload("wl", "ns").
		Queue("lq").
		ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "ownername", "owneruid").
		ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
			PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Assignment(corev1.ResourceCPU, "flavor1", "1").Obj()).
			Obj(), now).
		AdmissionCheck(kueue.AdmissionCheckState{
			Name:               "ac1",
			State:              kueue.CheckStatePending,
			LastTransitionTime: metav1.NewTime(now),
		}).
		Condition(metav1.Condition{
			Type:               kueue.WorkloadPodsReady,
			Status:             metav1.ConditionTrue,
			Reason:             "PodsReady",
			Message:            "All pods are ready or succeeded",
			LastTransitionTime: metav1.NewTime(now.Add(-10 * time.Minute)),
		}).
		RequeueState(new(int32(3)), nil)

	cases := map[string]reconcileTestCase{
		"should keep the requeueState after sustained readiness when the gate is disabled": {
			featureGates: map[featuregate.Feature]bool{
				features.ResetRequeueStateOnPodsReady: false,
			},
			reconcilerOpts: []Option{WithWaitForPodsReady(timeoutConfig)},
			workload:       readyWl(10 * time.Minute).Obj(),
			wantWorkload:   readyWl(10 * time.Minute).Obj(),
			wantResult:     reconcile.Result{},
		},
		"should keep the requeueState and requeue when the readiness is sustained shorter than the timeout": {
			featureGates: map[featuregate.Feature]bool{
				features.ResetRequeueStateOnPodsReady: true,
			},
			reconcilerOpts: []Option{WithWaitForPodsReady(timeoutConfig)},
			workload:       readyWl(2 * time.Minute).Obj(),
			wantWorkload:   readyWl(2 * time.Minute).Obj(),
			wantResult:     reconcile.Result{RequeueAfter: 3 * time.Minute},
		},
		"should clear the requeueState after readiness sustained for at least the timeout": {
			featureGates: map[featuregate.Feature]bool{
				features.ResetRequeueStateOnPodsReady: true,
			},
			reconcilerOpts: []Option{WithWaitForPodsReady(timeoutConfig)},
			workload:       readyWl(5 * time.Minute).Obj(),
			wantWorkload:   readyWl(5*time.Minute).RequeueState(nil, nil).Obj(),
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "wl", Namespace: "ns"},
					EventType: corev1.EventTypeNormal,
					Reason:    "RequeueStateReset",
					Message:   "Reset the requeue count (was 3) after PodsReady was True for at least 5m0s",
				},
			},
			wantResult: reconcile.Result{},
		},
		"should keep the requeueState when PodsReady is False": {
			featureGates: map[featuregate.Feature]bool{
				features.ResetRequeueStateOnPodsReady: true,
			},
			reconcilerOpts: []Option{WithWaitForPodsReady(timeoutConfig)},
			workload:       notReadyWl.Clone().Obj(),
			wantWorkload:   notReadyWl.Clone().Obj(),
			wantResult:     reconcile.Result{RequeueAfter: 5 * time.Minute},
		},
		"should not reset the requeueState when no waitForPodsReady timeout is configured": {
			featureGates: map[featuregate.Feature]bool{
				features.ResetRequeueStateOnPodsReady: true,
			},
			workload:     readyWl(10 * time.Minute).Obj(),
			wantWorkload: readyWl(10 * time.Minute).Obj(),
			wantResult:   reconcile.Result{},
		},
		"should do nothing when the requeueState is already cleared": {
			featureGates: map[featuregate.Feature]bool{
				features.ResetRequeueStateOnPodsReady: true,
			},
			reconcilerOpts: []Option{WithWaitForPodsReady(timeoutConfig)},
			workload:       readyWl(10*time.Minute).RequeueState(nil, nil).Obj(),
			wantWorkload:   readyWl(10*time.Minute).RequeueState(nil, nil).Obj(),
			wantResult:     reconcile.Result{},
		},
		"should return the error when clearing the requeueState fails": {
			featureGates: map[featuregate.Feature]bool{
				features.ResetRequeueStateOnPodsReady: true,
			},
			reconcilerOpts: []Option{WithWaitForPodsReady(timeoutConfig)},
			patchErr:       errStatus,
			workload:       readyWl(5 * time.Minute).Obj(),
			wantWorkload:   readyWl(5 * time.Minute).Obj(),
			wantError:      errStatus,
		},
		"should requeue after the earlier of the PodsReady and maximumExecutionTime timers when the gate is disabled": {
			featureGates: map[featuregate.Feature]bool{
				features.ResetRequeueStateOnPodsReady: false,
			},
			reconcilerOpts: []Option{WithWaitForPodsReady(timeoutConfig)},
			workload:       notReadyWl.Clone().MaximumExecutionTimeSeconds(60).Obj(),
			wantWorkload:   notReadyWl.Clone().MaximumExecutionTimeSeconds(60).Obj(),
			wantResult:     reconcile.Result{RequeueAfter: time.Minute},
		},
		"should requeue at the maximumExecutionTime when it is due before the requeueState reset": {
			featureGates: map[featuregate.Feature]bool{
				features.ResetRequeueStateOnPodsReady: true,
			},
			reconcilerOpts: []Option{WithWaitForPodsReady(timeoutConfig)},
			workload:       readyWl(2 * time.Minute).MaximumExecutionTimeSeconds(60).Obj(),
			wantWorkload:   readyWl(2 * time.Minute).MaximumExecutionTimeSeconds(60).Obj(),
			wantResult:     reconcile.Result{RequeueAfter: time.Minute},
		},
		"should requeue at the requeueState reset when it is due before the maximumExecutionTime": {
			featureGates: map[featuregate.Feature]bool{
				features.ResetRequeueStateOnPodsReady: true,
			},
			reconcilerOpts: []Option{WithWaitForPodsReady(timeoutConfig)},
			workload:       readyWl(4*time.Minute + 30*time.Second).MaximumExecutionTimeSeconds(120).Obj(),
			wantWorkload:   readyWl(4*time.Minute + 30*time.Second).MaximumExecutionTimeSeconds(120).Obj(),
			wantResult:     reconcile.Result{RequeueAfter: 30 * time.Second},
		},
		"should not reset the requeueState of an evicted workload which is still admitted": {
			featureGates: map[featuregate.Feature]bool{
				features.ResetRequeueStateOnPodsReady: true,
			},
			reconcilerOpts: []Option{WithWaitForPodsReady(timeoutConfig)},
			workload:       evictedWl.Clone().Obj(),
			wantWorkload:   evictedWl.Clone().Obj(),
			wantResult:     reconcile.Result{},
		},
		"should not reset the requeueState when the quota is reserved but the workload is not admitted": {
			featureGates: map[featuregate.Feature]bool{
				features.ResetRequeueStateOnPodsReady: true,
			},
			reconcilerOpts: []Option{WithWaitForPodsReady(timeoutConfig)},
			cq: utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("flavor1").Obj()).
				AdmissionChecks("ac1").
				Obj(),
			lq:       utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj(),
			workload: checksPendingWl.Clone().Obj(),
			wantWorkload: checksPendingWl.Clone().
				Condition(metav1.Condition{
					Type:    kueue.WorkloadAdmitted,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadAdmittedReasonUnsatisfiedAdmissionChecks,
					Message: "The workload has not all checks ready",
				}).
				Obj(),
			wantResult: reconcile.Result{},
		},
		"should reset the requeueState after the per-workload timeout when it is shorter than the cluster-level one": {
			featureGates: map[featuregate.Feature]bool{
				features.ResetRequeueStateOnPodsReady:  true,
				features.WorkloadLevelWaitForPodsReady: true,
			},
			reconcilerOpts: []Option{WithWaitForPodsReady(timeoutConfig)},
			workload: readyWl(2*time.Minute).
				Annotation(controllerconstants.WaitForPodsReadyAnnotation, `{"timeoutSeconds":60}`).
				Obj(),
			wantWorkload: readyWl(2*time.Minute).
				Annotation(controllerconstants.WaitForPodsReadyAnnotation, `{"timeoutSeconds":60}`).
				RequeueState(nil, nil).
				Obj(),
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "wl", Namespace: "ns"},
					EventType: corev1.EventTypeNormal,
					Reason:    "RequeueStateReset",
					Message:   "Reset the requeue count (was 3) after PodsReady was True for at least 1m0s",
				},
			},
			wantResult: reconcile.Result{},
		},
	}
	runReconcileTestCases(t, cases, fakeClock)
}
