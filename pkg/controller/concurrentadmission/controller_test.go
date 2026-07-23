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

package concurrentadmission

import (
	"fmt"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/component-base/featuregate"
	testingclock "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/features"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/pkg/workload/concurrentadmission"
)

var (
	workloadCmpOpts = cmp.Options{
		cmpopts.EquateEmpty(),
		cmpopts.IgnoreFields(
			kueue.Workload{}, "TypeMeta", "ObjectMeta.ResourceVersion", "ObjectMeta.UID", "Status.AccumulatedPastExecutionTimeSeconds", "Status.SchedulingStats",
		),
		cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime"),
		cmpopts.IgnoreFields(kueue.PreemptionGateState{}, "LastTransitionTime"),
		cmpopts.SortSlices(func(a, b kueue.Workload) bool { return a.Name < b.Name }),
		cmpopts.SortSlices(func(a, b metav1.Condition) bool { return a.Type < b.Type }),
	}
)

// blockedOnPreemptionCondition returns a WorkloadBlockedOnPreemptionGates=True
// condition with the given signal time. The scheduler stamps this condition when
// a Variant needs preemption but its gate is still closed.
func blockedOnPreemptionCondition(t time.Time) metav1.Condition {
	return metav1.Condition{
		Type:               kueue.WorkloadBlockedOnPreemptionGates,
		Status:             metav1.ConditionTrue,
		Reason:             kueue.PreemptionGated,
		Message:            "Workload requires preemption, but it's gated",
		LastTransitionTime: metav1.NewTime(t),
	}
}

func caGate() kueue.PreemptionGate {
	return kueue.PreemptionGate{Name: constants.ConcurrentAdmissionPreemptionGate}
}

func caGateState(position kueue.PreemptionGatePosition, t time.Time) kueue.PreemptionGateState {
	return kueue.PreemptionGateState{
		Name:               constants.ConcurrentAdmissionPreemptionGate,
		Position:           position,
		LastTransitionTime: metav1.NewTime(t),
	}
}

func TestReconcile(t *testing.T) {
	// fakeNow seeds the reconciler's fake clock below. Gate timestamps that feed
	// the preemption-timeout computation are expressed relative to it so that
	// clock.Since(gateTime) is exact. It is truncated to whole seconds because
	// gate LastTransitionTimes are stored at second granularity (metav1.Time
	// round-trips through RFC3339), and the fake clock's now must match that to
	// avoid a sub-second remainder.
	fakeNow := metav1.Now().Rfc3339Copy().Time

	// otherGate is a non-ConcurrentAdmission preemption gate used to verify that
	// generated variants inherit the parent's gates alongside the CA gate.
	otherGate := kueue.PreemptionGate{Name: "other-gate"}

	defaultCQ := utiltestingapi.MakeClusterQueue("cq").
		ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas("on-demand").Obj(),
			*utiltestingapi.MakeFlavorQuotas("spot").Obj(),
		).
		ConcurrentAdmissionPolicy(kueue.ConcurrentAdmissionTryPreferredFlavors).
		Obj()
	defaultLQ := utiltestingapi.MakeLocalQueue("lq", "default").ClusterQueue("cq").Obj()

	migrationCQ := utiltestingapi.MakeClusterQueue("cq-migration").
		ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas("reservation").Obj(),
			*utiltestingapi.MakeFlavorQuotas("on-demand").Obj(),
			*utiltestingapi.MakeFlavorQuotas("spot").Obj(),
		).
		LastAcceptableFlavorName("reservation").
		Obj()
	migrationLQ := utiltestingapi.MakeLocalQueue("lq-migration", "default").ClusterQueue("cq-migration").Obj()

	migrationCQNoConstraint := utiltestingapi.MakeClusterQueue("cq-migration-no-constraint").
		ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas("reservation").Obj(),
			*utiltestingapi.MakeFlavorQuotas("on-demand").Obj(),
			*utiltestingapi.MakeFlavorQuotas("spot").Obj(),
		).
		ConcurrentAdmissionPolicy(kueue.ConcurrentAdmissionTryPreferredFlavors).
		Obj()
	migrationLQNoConstraint := utiltestingapi.MakeLocalQueue("lq-migration-no-constraint", "default").ClusterQueue("cq-migration-no-constraint").Obj()

	retainFirstAdmissionCQ := utiltestingapi.MakeClusterQueue("cq-hold").
		ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas("reservation").Obj(),
			*utiltestingapi.MakeFlavorQuotas("on-demand").Obj(),
			*utiltestingapi.MakeFlavorQuotas("spot").Obj(),
		).
		ConcurrentAdmissionPolicy(kueue.ConcurrentAdmissionRetainFirstAdmission).
		Obj()
	retainFirstAdmissionLQ := utiltestingapi.MakeLocalQueue("lq-hold", "default").ClusterQueue("cq-hold").Obj()

	spotOnlyCQ := utiltestingapi.MakeClusterQueue("cq-spot-only").
		ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas("spot").Obj(),
		).
		ConcurrentAdmissionPolicy(kueue.ConcurrentAdmissionTryPreferredFlavors).
		Obj()
	spotOnlyLQ := utiltestingapi.MakeLocalQueue("lq-spot-only", "default").ClusterQueue("cq-spot-only").Obj()

	testCases := map[string]struct {
		parentWorkload       *kueue.Workload
		variantWorkloads     []kueue.Workload
		wantParentWorkload   *kueue.Workload
		wantVariantWorkloads []kueue.Workload
		wantEvents           []utiltesting.EventRecord
		wantResult           reconcile.Result
		req                  reconcile.Request
	}{
		"workload not found": {
			req: reconcile.Request{
				NamespacedName: types.NamespacedName{
					Namespace: "default",
					Name:      "non-existing",
				},
			},
		},
		"workload is not parent or variant": {
			parentWorkload:     utiltestingapi.MakeWorkload("wl", "default").Obj(),
			wantParentWorkload: utiltestingapi.MakeWorkload("wl", "default").Obj(),
		},
		"parent workload without variants creates them": {
			parentWorkload: utiltestingapi.MakeWorkload("wl-12345", "default").
				Queue("lq").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Obj(),
			wantParentWorkload: utiltestingapi.MakeWorkload("wl-12345", "default").
				Queue("lq").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Obj(),
			wantVariantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-variant-spot-a2342", "default").
					Queue("lq").
					AllowedFlavors("spot").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", ""). // UID is ignored in cmp
					Obj(),
				*utiltestingapi.MakeWorkload("wl-variant-on-demand-480a3", "default").
					Queue("lq").
					AllowedFlavors("on-demand").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "wl-12345"},
					EventType: corev1.EventTypeNormal,
					Reason:    ReasonCreatedVariant,
					Message:   "Variant Workload \"default/wl-variant-spot-a2342\" created",
				},
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "wl-12345"},
					EventType: corev1.EventTypeNormal,
					Reason:    ReasonCreatedVariant,
					Message:   "Variant Workload \"default/wl-variant-on-demand-480a3\" created",
				},
			},
		},
		"parent workload with missing variants; creates missing": {
			parentWorkload: utiltestingapi.MakeWorkload("wl-12345", "default").
				Queue("lq").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Obj(),
			variantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-variant-spot", "default").
					Queue("lq").
					AllowedFlavors("spot").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", ""). // UID is ignored in cmp
					Obj(),
			},
			wantParentWorkload: utiltestingapi.MakeWorkload("wl-12345", "default").
				Queue("lq").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Obj(),
			wantVariantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-variant-spot", "default").
					Queue("lq").
					AllowedFlavors("spot").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", ""). // UID is ignored in cmp
					Obj(),
				*utiltestingapi.MakeWorkload("wl-variant-on-demand-480a3", "default").
					Queue("lq").
					AllowedFlavors("on-demand").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "wl-12345"},
					EventType: corev1.EventTypeNormal,
					Reason:    ReasonCreatedVariant,
					Message:   "Variant Workload \"default/wl-variant-on-demand-480a3\" created",
				},
			},
		},
		"flavor removed from ClusterQueue; variant is deleted": {
			parentWorkload: utiltestingapi.MakeWorkload("wl-12345", "default").
				Queue("lq").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Obj(),
			variantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-variant-on-demand", "default").
					Queue("lq").
					AllowedFlavors("on-demand").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Obj(),
				*utiltestingapi.MakeWorkload("wl-variant-spot", "default").
					Queue("lq").
					AllowedFlavors("spot").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Obj(),
				// "stale" is not part of the "cq" ClusterQueue's resource group anymore.
				*utiltestingapi.MakeWorkload("wl-variant-stale", "default").
					Queue("lq").
					AllowedFlavors("stale").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Obj(),
			},
			wantParentWorkload: utiltestingapi.MakeWorkload("wl-12345", "default").
				Queue("lq").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Obj(),
			wantVariantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-variant-on-demand", "default").
					Queue("lq").
					AllowedFlavors("on-demand").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Obj(),
				*utiltestingapi.MakeWorkload("wl-variant-spot", "default").
					Queue("lq").
					AllowedFlavors("spot").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "wl-12345"},
					EventType: corev1.EventTypeNormal,
					Reason:    ReasonDeletedVariant,
					Message:   "Variant Workload \"default/wl-variant-stale\" deleted; flavor \"stale\" no longer in ClusterQueue",
				},
			},
		},
		"admitted variant's flavor removed; variant deleted and parent evicted": {
			parentWorkload: utiltestingapi.MakeWorkload("wl-12345", "default").
				Queue("lq-spot-only").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Request(corev1.ResourceCPU, "1").
				SimpleReserveQuota("cq-spot-only", "on-demand", metav1.Now().Time).
				AdmittedAt(true, metav1.Now().Time).
				Obj(),
			variantWorkloads: []kueue.Workload{
				// The admitted variant runs on "on-demand", which cq-spot-only no longer offers.
				*utiltestingapi.MakeWorkload("wl-variant-on-demand", "default").
					Queue("lq-spot-only").
					AllowedFlavors("on-demand").
					Request(corev1.ResourceCPU, "1").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					SimpleReserveQuota("cq-spot-only", "on-demand", metav1.Now().Time).
					AdmittedAt(true, metav1.Now().Time).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-variant-spot", "default").
					Queue("lq-spot-only").
					AllowedFlavors("spot").
					Request(corev1.ResourceCPU, "1").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Obj(),
			},
			wantParentWorkload: utiltestingapi.MakeWorkload("wl-12345", "default").
				Queue("lq-spot-only").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Request(corev1.ResourceCPU, "1").
				SimpleReserveQuota("cq-spot-only", "on-demand", metav1.Now().Time).
				AdmittedAt(true, metav1.Now().Time).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadEvicted,
					Status:  metav1.ConditionTrue,
					Reason:  "ConcurrentAdmission",
					Message: "No variant is running",
				}).
				Obj(),
			wantVariantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-variant-spot", "default").
					Queue("lq-spot-only").
					AllowedFlavors("spot").
					Request(corev1.ResourceCPU, "1").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "wl-12345"},
					EventType: corev1.EventTypeNormal,
					Reason:    ReasonDeletedVariant,
					Message:   "Variant Workload \"default/wl-variant-on-demand\" deleted; flavor \"on-demand\" no longer in ClusterQueue",
				},
			},
		},
		"deleting the variant holding the open preemption gate opens the next one's gate": {
			parentWorkload: utiltestingapi.MakeWorkload("wl-12345", "default").
				Queue("lq-spot-only").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Obj(),
			variantWorkloads: []kueue.Workload{
				// on-demand holds an open gate whose preemption timeout has NOT elapsed
				// (opened at fakeNow), so the spot gate would normally stay closed.
				// cq-spot-only no longer offers on-demand, so this variant is deleted.
				*utiltestingapi.MakeWorkload("wl-variant-on-demand", "default").
					Queue("lq-spot-only").
					AllowedFlavors("on-demand").
					Request(corev1.ResourceCPU, "1").
					PreemptionGates(caGate()).
					PreemptionGateStates(caGateState(kueue.PreemptionGatePositionOpen, fakeNow)).
					Condition(blockedOnPreemptionCondition(fakeNow)).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Obj(),
				*utiltestingapi.MakeWorkload("wl-variant-spot", "default").
					Queue("lq-spot-only").
					AllowedFlavors("spot").
					Request(corev1.ResourceCPU, "1").
					PreemptionGates(caGate()).
					Condition(blockedOnPreemptionCondition(fakeNow)).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Obj(),
			},
			wantParentWorkload: utiltestingapi.MakeWorkload("wl-12345", "default").
				Queue("lq-spot-only").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Obj(),
			wantVariantWorkloads: []kueue.Workload{
				// on-demand deleted; spot's gate is opened even though on-demand's gate
				// had not yet timed out, because the gate holder is gone.
				*utiltestingapi.MakeWorkload("wl-variant-spot", "default").
					Queue("lq-spot-only").
					AllowedFlavors("spot").
					Request(corev1.ResourceCPU, "1").
					PreemptionGates(caGate()).
					PreemptionGateStates(caGateState(kueue.PreemptionGatePositionOpen, fakeNow)).
					Condition(blockedOnPreemptionCondition(fakeNow)).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "wl-12345"},
					EventType: corev1.EventTypeNormal,
					Reason:    ReasonDeletedVariant,
					Message:   "Variant Workload \"default/wl-variant-on-demand\" deleted; flavor \"on-demand\" no longer in ClusterQueue",
				},
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "wl-variant-spot"},
					EventType: corev1.EventTypeNormal,
					Reason:    ReasonPreemptionUngatedVariant,
					Message:   "Opened preemption gate for variant workload \"default/wl-variant-spot\"",
				},
			},
			wantResult: reconcile.Result{RequeueAfter: preemptionTimeout},
		},
		"ungates the next-preferred variant once the open gate's preemption timeout elapses": {
			parentWorkload: utiltestingapi.MakeWorkload("wl-12345", "default").
				Queue("lq").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Obj(),
			variantWorkloads: []kueue.Workload{
				// on-demand is the most-preferred flavor and already holds an open
				// gate, but its preemption timeout has elapsed (opened > preemptionTimeout ago).
				*utiltestingapi.MakeWorkload("wl-variant-on-demand", "default").
					Queue("lq").
					AllowedFlavors("on-demand").
					Request(corev1.ResourceCPU, "1").
					PreemptionGates(caGate()).
					PreemptionGateStates(caGateState(kueue.PreemptionGatePositionOpen, fakeNow.Add(-preemptionTimeout))).
					Condition(blockedOnPreemptionCondition(metav1.Now().Time)).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Obj(),
				*utiltestingapi.MakeWorkload("wl-variant-spot", "default").
					Queue("lq").
					AllowedFlavors("spot").
					Request(corev1.ResourceCPU, "1").
					PreemptionGates(caGate()).
					Condition(blockedOnPreemptionCondition(metav1.Now().Time)).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Obj(),
			},
			wantParentWorkload: utiltestingapi.MakeWorkload("wl-12345", "default").
				Queue("lq").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Obj(),
			wantVariantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-variant-on-demand", "default").
					Queue("lq").
					AllowedFlavors("on-demand").
					Request(corev1.ResourceCPU, "1").
					PreemptionGates(caGate()).
					PreemptionGateStates(caGateState(kueue.PreemptionGatePositionOpen, fakeNow.Add(-preemptionTimeout))).
					Condition(blockedOnPreemptionCondition(metav1.Now().Time)).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Obj(),
				*utiltestingapi.MakeWorkload("wl-variant-spot", "default").
					Queue("lq").
					AllowedFlavors("spot").
					Request(corev1.ResourceCPU, "1").
					PreemptionGates(caGate()).
					PreemptionGateStates(caGateState(kueue.PreemptionGatePositionOpen, metav1.Now().Time)).
					Condition(blockedOnPreemptionCondition(metav1.Now().Time)).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "wl-variant-spot"},
					EventType: corev1.EventTypeNormal,
					Reason:    ReasonPreemptionUngatedVariant,
					Message:   "Opened preemption gate for variant workload \"default/wl-variant-spot\"",
				},
			},
			wantResult: reconcile.Result{RequeueAfter: preemptionTimeout},
		},
		"blocked variant has its preemption gate opened; non-blocked variant stays closed": {
			parentWorkload: utiltestingapi.MakeWorkload("wl-12345", "default").
				Queue("lq").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Obj(),
			variantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-variant-on-demand", "default").
					Queue("lq").
					AllowedFlavors("on-demand").
					Request(corev1.ResourceCPU, "1").
					PreemptionGates(caGate()).
					Condition(blockedOnPreemptionCondition(fakeNow)).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Obj(),
				*utiltestingapi.MakeWorkload("wl-variant-spot", "default").
					Queue("lq").
					AllowedFlavors("spot").
					Request(corev1.ResourceCPU, "1").
					PreemptionGates(caGate()).
					PreemptionGateStates(caGateState(kueue.PreemptionGatePositionClosed, fakeNow)).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Obj(),
			},
			wantParentWorkload: utiltestingapi.MakeWorkload("wl-12345", "default").
				Queue("lq").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Obj(),
			wantVariantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-variant-on-demand", "default").
					Queue("lq").
					AllowedFlavors("on-demand").
					Request(corev1.ResourceCPU, "1").
					PreemptionGates(caGate()).
					PreemptionGateStates(caGateState(kueue.PreemptionGatePositionOpen, fakeNow)).
					Condition(blockedOnPreemptionCondition(fakeNow)).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Obj(),
				*utiltestingapi.MakeWorkload("wl-variant-spot", "default").
					Queue("lq").
					AllowedFlavors("spot").
					Request(corev1.ResourceCPU, "1").
					PreemptionGates(caGate()).
					PreemptionGateStates(caGateState(kueue.PreemptionGatePositionClosed, fakeNow)).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "wl-variant-on-demand"},
					EventType: corev1.EventTypeNormal,
					Reason:    ReasonPreemptionUngatedVariant,
					Message:   "Opened preemption gate for variant workload \"default/wl-variant-on-demand\"",
				},
			},
			wantResult: reconcile.Result{RequeueAfter: preemptionTimeout},
		},
		"no variant requires preemption; no gate is opened": {
			parentWorkload: utiltestingapi.MakeWorkload("wl-12345", "default").
				Queue("lq").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Obj(),
			variantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-variant-on-demand", "default").
					Queue("lq").
					AllowedFlavors("on-demand").
					Request(corev1.ResourceCPU, "1").
					PreemptionGates(caGate()).
					PreemptionGateStates(caGateState(kueue.PreemptionGatePositionClosed, fakeNow)).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Obj(),
				*utiltestingapi.MakeWorkload("wl-variant-spot", "default").
					Queue("lq").
					AllowedFlavors("spot").
					Request(corev1.ResourceCPU, "1").
					PreemptionGates(caGate()).
					PreemptionGateStates(caGateState(kueue.PreemptionGatePositionClosed, fakeNow)).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Obj(),
			},
			wantParentWorkload: utiltestingapi.MakeWorkload("wl-12345", "default").
				Queue("lq").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Obj(),
			wantVariantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-variant-on-demand", "default").
					Queue("lq").
					AllowedFlavors("on-demand").
					Request(corev1.ResourceCPU, "1").
					PreemptionGates(caGate()).
					PreemptionGateStates(caGateState(kueue.PreemptionGatePositionClosed, fakeNow)).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Obj(),
				*utiltestingapi.MakeWorkload("wl-variant-spot", "default").
					Queue("lq").
					AllowedFlavors("spot").
					Request(corev1.ResourceCPU, "1").
					PreemptionGates(caGate()).
					PreemptionGateStates(caGateState(kueue.PreemptionGatePositionClosed, fakeNow)).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Obj(),
			},
		},
		"the most-preferred blocked variant is selected for ungating": {
			parentWorkload: utiltestingapi.MakeWorkload("wl-12345", "default").
				Queue("lq").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Obj(),
			variantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-variant-on-demand", "default").
					Queue("lq").
					AllowedFlavors("on-demand").
					Request(corev1.ResourceCPU, "1").
					PreemptionGates(caGate()).
					Condition(blockedOnPreemptionCondition(fakeNow)).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Obj(),
				*utiltestingapi.MakeWorkload("wl-variant-spot", "default").
					Queue("lq").
					AllowedFlavors("spot").
					Request(corev1.ResourceCPU, "1").
					PreemptionGates(caGate()).
					Condition(blockedOnPreemptionCondition(fakeNow)).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Obj(),
			},
			wantParentWorkload: utiltestingapi.MakeWorkload("wl-12345", "default").
				Queue("lq").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Obj(),
			wantVariantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-variant-on-demand", "default").
					Queue("lq").
					AllowedFlavors("on-demand").
					Request(corev1.ResourceCPU, "1").
					PreemptionGates(caGate()).
					PreemptionGateStates(caGateState(kueue.PreemptionGatePositionOpen, fakeNow)).
					Condition(blockedOnPreemptionCondition(fakeNow)).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Obj(),
				*utiltestingapi.MakeWorkload("wl-variant-spot", "default").
					Queue("lq").
					AllowedFlavors("spot").
					Request(corev1.ResourceCPU, "1").
					PreemptionGates(caGate()).
					Condition(blockedOnPreemptionCondition(fakeNow)).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "wl-variant-on-demand"},
					EventType: corev1.EventTypeNormal,
					Reason:    ReasonPreemptionUngatedVariant,
					Message:   "Opened preemption gate for variant workload \"default/wl-variant-on-demand\"",
				},
			},
			wantResult: reconcile.Result{RequeueAfter: preemptionTimeout},
		},
		"a recently opened gate defers ungating the next variant until the timeout": {
			parentWorkload: utiltestingapi.MakeWorkload("wl-12345", "default").
				Queue("lq").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Obj(),
			variantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-variant-on-demand", "default").
					Queue("lq").
					AllowedFlavors("on-demand").
					Request(corev1.ResourceCPU, "1").
					PreemptionGates(caGate()).
					PreemptionGateStates(caGateState(kueue.PreemptionGatePositionOpen, fakeNow.Add(-time.Minute))).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Obj(),
				*utiltestingapi.MakeWorkload("wl-variant-spot", "default").
					Queue("lq").
					AllowedFlavors("spot").
					Request(corev1.ResourceCPU, "1").
					PreemptionGates(caGate()).
					Condition(blockedOnPreemptionCondition(fakeNow)).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Obj(),
			},
			wantParentWorkload: utiltestingapi.MakeWorkload("wl-12345", "default").
				Queue("lq").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Obj(),
			wantVariantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-variant-on-demand", "default").
					Queue("lq").
					AllowedFlavors("on-demand").
					Request(corev1.ResourceCPU, "1").
					PreemptionGates(caGate()).
					PreemptionGateStates(caGateState(kueue.PreemptionGatePositionOpen, fakeNow.Add(-time.Minute))).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Obj(),
				*utiltestingapi.MakeWorkload("wl-variant-spot", "default").
					Queue("lq").
					AllowedFlavors("spot").
					Request(corev1.ResourceCPU, "1").
					PreemptionGates(caGate()).
					Condition(blockedOnPreemptionCondition(fakeNow)).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Obj(),
			},
			wantResult: reconcile.Result{RequeueAfter: preemptionTimeout - time.Minute},
		},
		"the latest open-gate time among variants determines the wait": {
			parentWorkload: utiltestingapi.MakeWorkload("wl-12345", "default").
				Queue("lq-migration-no-constraint").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Obj(),
			variantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-variant-reservation", "default").
					Queue("lq-migration-no-constraint").
					AllowedFlavors("reservation").
					Request(corev1.ResourceCPU, "1").
					PreemptionGates(caGate()).
					PreemptionGateStates(caGateState(kueue.PreemptionGatePositionOpen, fakeNow.Add(-4*time.Minute))).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Obj(),
				*utiltestingapi.MakeWorkload("wl-variant-on-demand", "default").
					Queue("lq-migration-no-constraint").
					AllowedFlavors("on-demand").
					Request(corev1.ResourceCPU, "1").
					PreemptionGates(caGate()).
					PreemptionGateStates(caGateState(kueue.PreemptionGatePositionOpen, fakeNow.Add(-time.Minute))).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Obj(),
				*utiltestingapi.MakeWorkload("wl-variant-spot", "default").
					Queue("lq-migration-no-constraint").
					AllowedFlavors("spot").
					Request(corev1.ResourceCPU, "1").
					PreemptionGates(caGate()).
					Condition(blockedOnPreemptionCondition(fakeNow)).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Obj(),
			},
			wantParentWorkload: utiltestingapi.MakeWorkload("wl-12345", "default").
				Queue("lq-migration-no-constraint").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Obj(),
			wantVariantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-variant-reservation", "default").
					Queue("lq-migration-no-constraint").
					AllowedFlavors("reservation").
					Request(corev1.ResourceCPU, "1").
					PreemptionGates(caGate()).
					PreemptionGateStates(caGateState(kueue.PreemptionGatePositionOpen, fakeNow.Add(-4*time.Minute))).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Obj(),
				*utiltestingapi.MakeWorkload("wl-variant-on-demand", "default").
					Queue("lq-migration-no-constraint").
					AllowedFlavors("on-demand").
					Request(corev1.ResourceCPU, "1").
					PreemptionGates(caGate()).
					PreemptionGateStates(caGateState(kueue.PreemptionGatePositionOpen, fakeNow.Add(-time.Minute))).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Obj(),
				*utiltestingapi.MakeWorkload("wl-variant-spot", "default").
					Queue("lq-migration-no-constraint").
					AllowedFlavors("spot").
					Request(corev1.ResourceCPU, "1").
					PreemptionGates(caGate()).
					Condition(blockedOnPreemptionCondition(fakeNow)).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Obj(),
			},
			wantResult: reconcile.Result{RequeueAfter: preemptionTimeout - time.Minute},
		},
		"parent with a pre-existing preemption gate; created variants inherit it plus the ConcurrentAdmission gate": {
			parentWorkload: utiltestingapi.MakeWorkload("wl-12345", "default").
				Queue("lq").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				PreemptionGates(otherGate).
				Obj(),
			wantParentWorkload: utiltestingapi.MakeWorkload("wl-12345", "default").
				Queue("lq").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				PreemptionGates(otherGate).
				Obj(),
			wantVariantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-variant-spot-a2342", "default").
					Queue("lq").
					AllowedFlavors("spot").
					PreemptionGates(otherGate, caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Obj(),
				*utiltestingapi.MakeWorkload("wl-variant-on-demand-480a3", "default").
					Queue("lq").
					AllowedFlavors("on-demand").
					PreemptionGates(otherGate, caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "wl-12345"},
					EventType: corev1.EventTypeNormal,
					Reason:    ReasonCreatedVariant,
					Message:   "Variant Workload \"default/wl-variant-spot-a2342\" created",
				},
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "wl-12345"},
					EventType: corev1.EventTypeNormal,
					Reason:    ReasonCreatedVariant,
					Message:   "Variant Workload \"default/wl-variant-on-demand-480a3\" created",
				},
			},
		},
		"admitted variant syncs admission to parent": {
			parentWorkload: utiltestingapi.MakeWorkload("wl-12345", "default").
				Queue("lq").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Obj(),
			variantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-variant-spot", "default").
					Queue("lq").
					AllowedFlavors("spot").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Request(corev1.ResourceCPU, "1").
					SimpleReserveQuota("cq", "spot", metav1.Now().Time).
					AdmittedAt(true, metav1.Now().Time).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-variant-on-demand", "default").
					Queue("lq").
					AllowedFlavors("on-demand").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Obj(),
			},
			wantParentWorkload: utiltestingapi.MakeWorkload("wl-12345", "default").
				Queue("lq").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Admission(utiltestingapi.MakeAdmission("cq", "main").
					PodSets(kueue.PodSetAssignment{
						Name: "main",
						Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
							corev1.ResourceCPU: "spot",
						},
						Count:         ptr.To[int32](1),
						ResourceUsage: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")},
					}).Obj()).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadAdmitted,
					Status:  metav1.ConditionTrue,
					Reason:  "Admitted",
					Message: "The variant wl-variant-spot is admitted",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionTrue,
					Reason:  "QuotaReserved",
					Message: "Quota reserved in ClusterQueue cq",
				}).
				Obj(),
			wantVariantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-variant-spot", "default").
					Queue("lq").
					AllowedFlavors("spot").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Request(corev1.ResourceCPU, "1").
					SimpleReserveQuota("cq", "spot", metav1.Now().Time).
					AdmittedAt(true, metav1.Now().Time).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-variant-on-demand", "default").
					Queue("lq").
					AllowedFlavors("on-demand").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Obj(),
			},
		},
		"no variant admitted; parent admitted; evict parent": {
			parentWorkload: utiltestingapi.MakeWorkload("wl-12345", "default").
				Queue("lq").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Request(corev1.ResourceCPU, "1").
				SimpleReserveQuota("cq", "spot", metav1.Now().Time).
				AdmittedAt(true, metav1.Now().Time).
				Obj(),
			variantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-variant-spot", "default").
					Queue("lq").
					AllowedFlavors("spot").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Request(corev1.ResourceCPU, "1").
					Obj(),
				*utiltestingapi.MakeWorkload("wl-variant-on-demand", "default").
					Queue("lq").
					AllowedFlavors("on-demand").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Request(corev1.ResourceCPU, "1").
					Obj(),
			},
			wantParentWorkload: utiltestingapi.MakeWorkload("wl-12345", "default").
				Queue("lq").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Request(corev1.ResourceCPU, "1").
				SimpleReserveQuota("cq", "spot", metav1.Now().Time).
				AdmittedAt(true, metav1.Now().Time).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadEvicted,
					Status:  metav1.ConditionTrue,
					Reason:  "ConcurrentAdmission",
					Message: "No variant is running",
				}).
				Obj(),
			wantVariantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-variant-spot", "default").
					Queue("lq").
					AllowedFlavors("spot").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Request(corev1.ResourceCPU, "1").
					Obj(),
				*utiltestingapi.MakeWorkload("wl-variant-on-demand", "default").
					Queue("lq").
					AllowedFlavors("on-demand").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Request(corev1.ResourceCPU, "1").
					Obj(),
			},
		},
		"admitted variant evicted; parent has quota; evict parent and wait": {
			parentWorkload: utiltestingapi.MakeWorkload("wl-12345", "default").
				Queue("lq").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Request(corev1.ResourceCPU, "1").
				SimpleReserveQuota("cq", "spot", metav1.Now().Time).
				AdmittedAt(true, metav1.Now().Time).
				Obj(),
			variantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-variant-spot", "default").
					Queue("lq").
					AllowedFlavors("spot").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Request(corev1.ResourceCPU, "1").
					SimpleReserveQuota("cq", "spot", metav1.Now().Time).
					AdmittedAt(true, metav1.Now().Time).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadEvicted,
						Status:  metav1.ConditionTrue,
						Reason:  kueue.WorkloadEvictedByPreemption,
						Message: "Evicted by preemption",
					}).
					Active(true).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-variant-on-demand", "default").
					Queue("lq").
					AllowedFlavors("on-demand").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Request(corev1.ResourceCPU, "1").
					Active(false).
					Obj(),
			},
			wantParentWorkload: utiltestingapi.MakeWorkload("wl-12345", "default").
				Queue("lq").
				Request(corev1.ResourceCPU, "1").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				SimpleReserveQuota("cq", "spot", metav1.Now().Time).
				AdmittedAt(true, metav1.Now().Time).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadEvicted,
					Status:  metav1.ConditionTrue,
					Reason:  "VariantEvicted",
					Message: "Admitted variant was evicted",
				}).
				Obj(),
			wantVariantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-variant-spot", "default").
					Queue("lq").
					AllowedFlavors("spot").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Request(corev1.ResourceCPU, "1").
					SimpleReserveQuota("cq", "spot", metav1.Now().Time).
					AdmittedAt(true, metav1.Now().Time).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadEvicted,
						Status:  metav1.ConditionTrue,
						Reason:  kueue.WorkloadEvictedByPreemption,
						Message: "Evicted by preemption",
					}).
					Active(true).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-variant-on-demand", "default").
					Queue("lq").
					AllowedFlavors("on-demand").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Request(corev1.ResourceCPU, "1").
					Active(false).
					Obj(),
			},
		},
		"admitted variant evicted; parent has no quota reservation; clear variant reservation": {
			parentWorkload: utiltestingapi.MakeWorkload("wl-12345", "default").
				Queue("lq").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Request(corev1.ResourceCPU, "1").
				Obj(),
			variantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-variant-spot", "default").
					Queue("lq").
					AllowedFlavors("spot").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Request(corev1.ResourceCPU, "1").
					SimpleReserveQuota("cq", "spot", metav1.Now().Time).
					AdmittedAt(true, metav1.Now().Time).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadEvicted,
						Status:  metav1.ConditionTrue,
						Reason:  kueue.WorkloadEvictedByPreemption,
						Message: "Evicted by preemption",
					}).
					Active(true).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-variant-on-demand", "default").
					Queue("lq").
					AllowedFlavors("on-demand").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Request(corev1.ResourceCPU, "1").
					Active(false).
					Obj(),
			},
			wantParentWorkload: utiltestingapi.MakeWorkload("wl-12345", "default").
				Queue("lq").
				Request(corev1.ResourceCPU, "1").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Obj(),
			wantVariantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-variant-spot", "default").
					Queue("lq").
					AllowedFlavors("spot").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Request(corev1.ResourceCPU, "1").
					SimpleReserveQuota("cq", "spot", metav1.Now().Time).
					AdmittedAt(true, metav1.Now().Time).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadQuotaReserved,
						Status:  metav1.ConditionFalse,
						Reason:  "Pending",
						Message: "Evicted by preemption",
					}).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadAdmitted,
						Status:  metav1.ConditionFalse,
						Reason:  "NoReservation",
						Message: "The workload has no reservation",
					}).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadEvicted,
						Status:  metav1.ConditionTrue,
						Reason:  kueue.WorkloadEvictedByPreemption,
						Message: "Evicted by preemption",
					}).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadRequeued,
						Status:  metav1.ConditionTrue,
						Reason:  "Preempted",
						Message: "Evicted by preemption",
					}).
					Active(true).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-variant-on-demand", "default").
					Queue("lq").
					AllowedFlavors("on-demand").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Request(corev1.ResourceCPU, "1").
					Active(true).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "wl-variant-on-demand"},
					EventType: corev1.EventTypeNormal,
					Reason:    ReasonActivatedVariant,
					Message:   "Variant Workload activated due to no other Variant being admitted",
				},
			},
		},
		"with minTargetFlavor=reservation, variant admitted on spot, deactivate on-demand": {
			parentWorkload: utiltestingapi.MakeWorkload("wl-12345", "default").
				Queue("lq-migration").
				Request(corev1.ResourceCPU, "1").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Obj(),
			variantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-variant-reservation", "default").
					Queue("lq-migration").
					AllowedFlavors("reservation").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Request(corev1.ResourceCPU, "1").
					Obj(),
				*utiltestingapi.MakeWorkload("wl-variant-on-demand", "default").
					Queue("lq-migration").
					AllowedFlavors("on-demand").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Request(corev1.ResourceCPU, "1").
					Obj(),
				*utiltestingapi.MakeWorkload("wl-variant-spot", "default").
					Queue("lq-migration").
					AllowedFlavors("spot").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Request(corev1.ResourceCPU, "1").
					SimpleReserveQuota("cq-migration", "spot", metav1.Now().Time).
					AdmittedAt(true, metav1.Now().Time).
					Obj(),
			},
			wantParentWorkload: utiltestingapi.MakeWorkload("wl-12345", "default").
				Queue("lq-migration").
				Request(corev1.ResourceCPU, "1").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Admission(utiltestingapi.MakeAdmission("cq-migration", "main").
					PodSets(kueue.PodSetAssignment{
						Name: "main",
						Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
							corev1.ResourceCPU: "spot",
						},
						Count:         ptr.To[int32](1),
						ResourceUsage: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")},
					}).Obj()).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadAdmitted,
					Status:  metav1.ConditionTrue,
					Reason:  "Admitted",
					Message: "The variant wl-variant-spot is admitted",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionTrue,
					Reason:  "QuotaReserved",
					Message: "Quota reserved in ClusterQueue cq-migration",
				}).
				Obj(),
			wantVariantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-variant-reservation", "default").
					Queue("lq-migration").
					AllowedFlavors("reservation").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Request(corev1.ResourceCPU, "1").
					Obj(),
				*utiltestingapi.MakeWorkload("wl-variant-on-demand", "default").
					Queue("lq-migration").
					AllowedFlavors("on-demand").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Request(corev1.ResourceCPU, "1").
					Active(false).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-variant-spot", "default").
					Queue("lq-migration").
					AllowedFlavors("spot").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Request(corev1.ResourceCPU, "1").
					SimpleReserveQuota("cq-migration", "spot", metav1.Now().Time).
					AdmittedAt(true, metav1.Now().Time).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "wl-variant-on-demand"},
					EventType: corev1.EventTypeNormal,
					Reason:    ReasonDeactivatedVariant,
					Message:   "Variant Workload deactivated due to being below lastAcceptableFlavor: \"reservation\" and another Variant admitted \"default/wl-variant-spot\"",
				},
			},
		},
		"with minTargetFlavor=reservation, variant admitted on on-demand, deactivate spot": {
			parentWorkload: utiltestingapi.MakeWorkload("wl-12345", "default").
				Queue("lq-migration").
				Request(corev1.ResourceCPU, "1").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Obj(),
			variantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-variant-reservation", "default").
					Queue("lq-migration").
					AllowedFlavors("reservation").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Request(corev1.ResourceCPU, "1").
					Obj(),
				*utiltestingapi.MakeWorkload("wl-variant-on-demand", "default").
					Queue("lq-migration").
					AllowedFlavors("on-demand").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Request(corev1.ResourceCPU, "1").
					SimpleReserveQuota("cq-migration", "on-demand", metav1.Now().Time).
					AdmittedAt(true, metav1.Now().Time).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-variant-spot", "default").
					Queue("lq-migration").
					AllowedFlavors("spot").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Request(corev1.ResourceCPU, "1").
					Obj(),
			},
			wantParentWorkload: utiltestingapi.MakeWorkload("wl-12345", "default").
				Queue("lq-migration").
				Request(corev1.ResourceCPU, "1").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Admission(utiltestingapi.MakeAdmission("cq-migration", "main").
					PodSets(kueue.PodSetAssignment{
						Name: "main",
						Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
							corev1.ResourceCPU: "on-demand",
						},
						Count:         ptr.To[int32](1),
						ResourceUsage: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")},
					}).Obj()).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadAdmitted,
					Status:  metav1.ConditionTrue,
					Reason:  "Admitted",
					Message: "The variant wl-variant-on-demand is admitted",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionTrue,
					Reason:  "QuotaReserved",
					Message: "Quota reserved in ClusterQueue cq-migration",
				}).
				Obj(),
			wantVariantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-variant-reservation", "default").
					Queue("lq-migration").
					AllowedFlavors("reservation").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Request(corev1.ResourceCPU, "1").
					Obj(),
				*utiltestingapi.MakeWorkload("wl-variant-on-demand", "default").
					Queue("lq-migration").
					AllowedFlavors("on-demand").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Request(corev1.ResourceCPU, "1").
					SimpleReserveQuota("cq-migration", "on-demand", metav1.Now().Time).
					AdmittedAt(true, metav1.Now().Time).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-variant-spot", "default").
					Queue("lq-migration").
					AllowedFlavors("spot").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Request(corev1.ResourceCPU, "1").
					Active(false).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "wl-variant-spot"},
					EventType: corev1.EventTypeNormal,
					Reason:    ReasonDeactivatedVariant,
					Message:   "Variant Workload deactivated due to being below lastAcceptableFlavor: \"reservation\" and another Variant admitted \"default/wl-variant-on-demand\"",
				},
			},
		},
		"with minTargetFlavor=reservation, variant admitted on reservation, deactivate spot and on-demand": {
			parentWorkload: utiltestingapi.MakeWorkload("wl-12345", "default").
				Queue("lq-migration").
				Request(corev1.ResourceCPU, "1").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Obj(),
			variantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-variant-reservation", "default").
					Queue("lq-migration").
					AllowedFlavors("reservation").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Request(corev1.ResourceCPU, "1").
					SimpleReserveQuota("cq-migration", "reservation", metav1.Now().Time).
					AdmittedAt(true, metav1.Now().Time).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-variant-on-demand", "default").
					Queue("lq-migration").
					AllowedFlavors("on-demand").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Request(corev1.ResourceCPU, "1").
					Obj(),
				*utiltestingapi.MakeWorkload("wl-variant-spot", "default").
					Queue("lq-migration").
					AllowedFlavors("spot").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Request(corev1.ResourceCPU, "1").
					Obj(),
			},
			wantParentWorkload: utiltestingapi.MakeWorkload("wl-12345", "default").
				Queue("lq-migration").
				Request(corev1.ResourceCPU, "1").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Admission(utiltestingapi.MakeAdmission("cq-migration", "main").
					PodSets(kueue.PodSetAssignment{
						Name: "main",
						Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
							corev1.ResourceCPU: "reservation",
						},
						Count:         ptr.To[int32](1),
						ResourceUsage: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")},
					}).Obj()).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadAdmitted,
					Status:  metav1.ConditionTrue,
					Reason:  "Admitted",
					Message: "The variant wl-variant-reservation is admitted",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionTrue,
					Reason:  "QuotaReserved",
					Message: "Quota reserved in ClusterQueue cq-migration",
				}).
				Obj(),
			wantVariantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-variant-reservation", "default").
					Queue("lq-migration").
					AllowedFlavors("reservation").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Request(corev1.ResourceCPU, "1").
					SimpleReserveQuota("cq-migration", "reservation", metav1.Now().Time).
					AdmittedAt(true, metav1.Now().Time).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-variant-on-demand", "default").
					Queue("lq-migration").
					AllowedFlavors("on-demand").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Request(corev1.ResourceCPU, "1").
					Active(false).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-variant-spot", "default").
					Queue("lq-migration").
					AllowedFlavors("spot").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Request(corev1.ResourceCPU, "1").
					Active(false).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "wl-variant-on-demand"},
					EventType: corev1.EventTypeNormal,
					Reason:    ReasonDeactivatedVariant,
					Message:   "Variant Workload deactivated due to being below lastAcceptableFlavor: \"reservation\" and another Variant admitted \"default/wl-variant-reservation\"",
				},
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "wl-variant-spot"},
					EventType: corev1.EventTypeNormal,
					Reason:    ReasonDeactivatedVariant,
					Message:   "Variant Workload deactivated due to being below lastAcceptableFlavor: \"reservation\" and another Variant admitted \"default/wl-variant-reservation\"",
				},
			},
		},
		"without minTargetFlavor, variant admitted on spot, nothing deactivated": {
			parentWorkload: utiltestingapi.MakeWorkload("wl-12345", "default").
				Queue("lq-migration-no-constraint").
				Request(corev1.ResourceCPU, "1").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Obj(),
			variantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-variant-reservation", "default").
					Queue("lq-migration-no-constraint").
					AllowedFlavors("reservation").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Request(corev1.ResourceCPU, "1").
					Obj(),
				*utiltestingapi.MakeWorkload("wl-variant-on-demand", "default").
					Queue("lq-migration-no-constraint").
					AllowedFlavors("on-demand").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Request(corev1.ResourceCPU, "1").
					Obj(),
				*utiltestingapi.MakeWorkload("wl-variant-spot", "default").
					Queue("lq-migration-no-constraint").
					AllowedFlavors("spot").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Request(corev1.ResourceCPU, "1").
					SimpleReserveQuota("cq-migration-no-constraint", "spot", metav1.Now().Time).
					AdmittedAt(true, metav1.Now().Time).
					Obj(),
			},
			wantParentWorkload: utiltestingapi.MakeWorkload("wl-12345", "default").
				Queue("lq-migration-no-constraint").
				Request(corev1.ResourceCPU, "1").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Admission(utiltestingapi.MakeAdmission("cq-migration-no-constraint", "main").
					PodSets(kueue.PodSetAssignment{
						Name: "main",
						Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
							corev1.ResourceCPU: "spot",
						},
						Count:         ptr.To[int32](1),
						ResourceUsage: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")},
					}).Obj()).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadAdmitted,
					Status:  metav1.ConditionTrue,
					Reason:  "Admitted",
					Message: "The variant wl-variant-spot is admitted",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionTrue,
					Reason:  "QuotaReserved",
					Message: "Quota reserved in ClusterQueue cq-migration-no-constraint",
				}).
				Obj(),
			wantVariantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-variant-reservation", "default").
					Queue("lq-migration-no-constraint").
					AllowedFlavors("reservation").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Request(corev1.ResourceCPU, "1").
					Obj(),
				*utiltestingapi.MakeWorkload("wl-variant-on-demand", "default").
					Queue("lq-migration-no-constraint").
					AllowedFlavors("on-demand").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Request(corev1.ResourceCPU, "1").
					Obj(),
				*utiltestingapi.MakeWorkload("wl-variant-spot", "default").
					Queue("lq-migration-no-constraint").
					AllowedFlavors("spot").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Request(corev1.ResourceCPU, "1").
					SimpleReserveQuota("cq-migration-no-constraint", "spot", metav1.Now().Time).
					AdmittedAt(true, metav1.Now().Time).
					Obj(),
			},
		},
		"without minTargetFlavor, variant admitted on on-demand, deactivate spot": {
			parentWorkload: utiltestingapi.MakeWorkload("wl-12345", "default").
				Queue("lq-migration-no-constraint").
				Request(corev1.ResourceCPU, "1").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Obj(),
			variantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-variant-reservation", "default").
					Queue("lq-migration-no-constraint").
					AllowedFlavors("reservation").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Request(corev1.ResourceCPU, "1").
					Obj(),
				*utiltestingapi.MakeWorkload("wl-variant-on-demand", "default").
					Queue("lq-migration-no-constraint").
					AllowedFlavors("on-demand").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Request(corev1.ResourceCPU, "1").
					SimpleReserveQuota("cq-migration-no-constraint", "on-demand", metav1.Now().Time).
					AdmittedAt(true, metav1.Now().Time).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-variant-spot", "default").
					Queue("lq-migration-no-constraint").
					AllowedFlavors("spot").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Request(corev1.ResourceCPU, "1").
					Obj(),
			},
			wantParentWorkload: utiltestingapi.MakeWorkload("wl-12345", "default").
				Queue("lq-migration-no-constraint").
				Request(corev1.ResourceCPU, "1").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Admission(utiltestingapi.MakeAdmission("cq-migration-no-constraint", "main").
					PodSets(kueue.PodSetAssignment{
						Name: "main",
						Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
							corev1.ResourceCPU: "on-demand",
						},
						Count:         ptr.To[int32](1),
						ResourceUsage: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")},
					}).Obj()).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadAdmitted,
					Status:  metav1.ConditionTrue,
					Reason:  "Admitted",
					Message: "The variant wl-variant-on-demand is admitted",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionTrue,
					Reason:  "QuotaReserved",
					Message: "Quota reserved in ClusterQueue cq-migration-no-constraint",
				}).
				Obj(),
			wantVariantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-variant-reservation", "default").
					Queue("lq-migration-no-constraint").
					AllowedFlavors("reservation").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Request(corev1.ResourceCPU, "1").
					Obj(),
				*utiltestingapi.MakeWorkload("wl-variant-on-demand", "default").
					Queue("lq-migration-no-constraint").
					AllowedFlavors("on-demand").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Request(corev1.ResourceCPU, "1").
					SimpleReserveQuota("cq-migration-no-constraint", "on-demand", metav1.Now().Time).
					AdmittedAt(true, metav1.Now().Time).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-variant-spot", "default").
					Queue("lq-migration-no-constraint").
					AllowedFlavors("spot").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Request(corev1.ResourceCPU, "1").
					Active(false).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "wl-variant-spot"},
					EventType: corev1.EventTypeNormal,
					Reason:    ReasonDeactivatedVariant,
					Message:   "Variant Workload deactivated due to being lower priority than admitted Variant \"default/wl-variant-on-demand\"",
				},
			},
		},
		"without minTargetFlavor, variant admitted on reservation, deactivate spot and on-demand": {
			parentWorkload: utiltestingapi.MakeWorkload("wl-12345", "default").
				Queue("lq-migration-no-constraint").
				Request(corev1.ResourceCPU, "1").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Obj(),
			variantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-variant-reservation", "default").
					Queue("lq-migration-no-constraint").
					AllowedFlavors("reservation").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Request(corev1.ResourceCPU, "1").
					SimpleReserveQuota("cq-migration-no-constraint", "reservation", metav1.Now().Time).
					AdmittedAt(true, metav1.Now().Time).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-variant-on-demand", "default").
					Queue("lq-migration-no-constraint").
					AllowedFlavors("on-demand").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Request(corev1.ResourceCPU, "1").
					Obj(),
				*utiltestingapi.MakeWorkload("wl-variant-spot", "default").
					Queue("lq-migration-no-constraint").
					AllowedFlavors("spot").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Request(corev1.ResourceCPU, "1").
					Obj(),
			},
			wantParentWorkload: utiltestingapi.MakeWorkload("wl-12345", "default").
				Queue("lq-migration-no-constraint").
				Request(corev1.ResourceCPU, "1").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Admission(utiltestingapi.MakeAdmission("cq-migration-no-constraint", "main").
					PodSets(kueue.PodSetAssignment{
						Name: "main",
						Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
							corev1.ResourceCPU: "reservation",
						},
						Count:         ptr.To[int32](1),
						ResourceUsage: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")},
					}).Obj()).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadAdmitted,
					Status:  metav1.ConditionTrue,
					Reason:  "Admitted",
					Message: "The variant wl-variant-reservation is admitted",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionTrue,
					Reason:  "QuotaReserved",
					Message: "Quota reserved in ClusterQueue cq-migration-no-constraint",
				}).
				Obj(),
			wantVariantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-variant-reservation", "default").
					Queue("lq-migration-no-constraint").
					AllowedFlavors("reservation").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Request(corev1.ResourceCPU, "1").
					SimpleReserveQuota("cq-migration-no-constraint", "reservation", metav1.Now().Time).
					AdmittedAt(true, metav1.Now().Time).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-variant-on-demand", "default").
					Queue("lq-migration-no-constraint").
					AllowedFlavors("on-demand").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Request(corev1.ResourceCPU, "1").
					Active(false).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-variant-spot", "default").
					Queue("lq-migration-no-constraint").
					AllowedFlavors("spot").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Request(corev1.ResourceCPU, "1").
					Active(false).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "wl-variant-on-demand"},
					EventType: corev1.EventTypeNormal,
					Reason:    ReasonDeactivatedVariant,
					Message:   "Variant Workload deactivated due to being lower priority than admitted Variant \"default/wl-variant-reservation\"",
				},
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "wl-variant-spot"},
					EventType: corev1.EventTypeNormal,
					Reason:    ReasonDeactivatedVariant,
					Message:   "Variant Workload deactivated due to being lower priority than admitted Variant \"default/wl-variant-reservation\"",
				},
			},
		},
		"RetainFirstAdmission, variant admitted on spot, deactivate reservation and on-demand": {
			parentWorkload: utiltestingapi.MakeWorkload("wl-12345", "default").
				Queue("lq-hold").
				Request(corev1.ResourceCPU, "1").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Obj(),
			variantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-variant-reservation", "default").
					Queue("lq-hold").
					AllowedFlavors("reservation").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Request(corev1.ResourceCPU, "1").
					Obj(),
				*utiltestingapi.MakeWorkload("wl-variant-on-demand", "default").
					Queue("lq-hold").
					AllowedFlavors("on-demand").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Request(corev1.ResourceCPU, "1").
					Obj(),
				*utiltestingapi.MakeWorkload("wl-variant-spot", "default").
					Queue("lq-hold").
					AllowedFlavors("spot").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Request(corev1.ResourceCPU, "1").
					SimpleReserveQuota("cq-hold", "spot", metav1.Now().Time).
					AdmittedAt(true, metav1.Now().Time).
					Obj(),
			},
			wantParentWorkload: utiltestingapi.MakeWorkload("wl-12345", "default").
				Queue("lq-hold").
				Request(corev1.ResourceCPU, "1").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Admission(utiltestingapi.MakeAdmission("cq-hold", "main").
					PodSets(kueue.PodSetAssignment{
						Name: "main",
						Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
							corev1.ResourceCPU: "spot",
						},
						Count:         ptr.To[int32](1),
						ResourceUsage: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")},
					}).Obj()).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadAdmitted,
					Status:  metav1.ConditionTrue,
					Reason:  "Admitted",
					Message: "The variant wl-variant-spot is admitted",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionTrue,
					Reason:  "QuotaReserved",
					Message: "Quota reserved in ClusterQueue cq-hold",
				}).
				Obj(),
			wantVariantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-variant-reservation", "default").
					Queue("lq-hold").
					AllowedFlavors("reservation").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Request(corev1.ResourceCPU, "1").
					Active(false).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-variant-on-demand", "default").
					Queue("lq-hold").
					AllowedFlavors("on-demand").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Request(corev1.ResourceCPU, "1").
					Active(false).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-variant-spot", "default").
					Queue("lq-hold").
					AllowedFlavors("spot").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Request(corev1.ResourceCPU, "1").
					SimpleReserveQuota("cq-hold", "spot", metav1.Now().Time).
					AdmittedAt(true, metav1.Now().Time).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "wl-variant-reservation"},
					EventType: corev1.EventTypeNormal,
					Reason:    ReasonDeactivatedVariant,
					Message:   "Variant Workload deactivated due to RetainFirstAdmission: another Variant \"default/wl-variant-spot\" is admitted",
				},
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "wl-variant-on-demand"},
					EventType: corev1.EventTypeNormal,
					Reason:    ReasonDeactivatedVariant,
					Message:   "Variant Workload deactivated due to RetainFirstAdmission: another Variant \"default/wl-variant-spot\" is admitted",
				},
			},
		},
		"parent marked finished, mark all variants finished": {
			parentWorkload: utiltestingapi.MakeWorkload("wl-12345", "default").
				Queue("lq").
				Request(corev1.ResourceCPU, "1").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadFinished,
					Status:  metav1.ConditionTrue,
					Reason:  "Succeeded",
					Message: "Job finished successfully",
				}).
				Obj(),
			variantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-variant-on-demand", "default").
					Queue("lq").
					AllowedFlavors("on-demand").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Request(corev1.ResourceCPU, "1").
					Obj(),
				*utiltestingapi.MakeWorkload("wl-variant-spot", "default").
					Queue("lq").
					AllowedFlavors("spot").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Request(corev1.ResourceCPU, "1").
					Obj(),
			},
			wantParentWorkload: utiltestingapi.MakeWorkload("wl-12345", "default").
				Queue("lq").
				Request(corev1.ResourceCPU, "1").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadFinished,
					Status:  metav1.ConditionTrue,
					Reason:  "Succeeded",
					Message: "Job finished successfully",
				}).
				Obj(),
			wantVariantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-variant-on-demand", "default").
					Queue("lq").
					AllowedFlavors("on-demand").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Request(corev1.ResourceCPU, "1").
					Condition(metav1.Condition{
						Type:    kueue.WorkloadFinished,
						Status:  metav1.ConditionTrue,
						Reason:  "Succeeded",
						Message: "Job finished successfully",
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-variant-spot", "default").
					Queue("lq").
					AllowedFlavors("spot").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Request(corev1.ResourceCPU, "1").
					Condition(metav1.Condition{
						Type:    kueue.WorkloadFinished,
						Status:  metav1.ConditionTrue,
						Reason:  "Succeeded",
						Message: "Job finished successfully",
					}).
					Obj(),
			},
		},
		"parent is not active, propagating deactivation to all variants": {
			parentWorkload: utiltestingapi.MakeWorkload("wl-12345", "default").
				Queue("lq").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Request(corev1.ResourceCPU, "1").
				Active(false).
				Obj(),
			variantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-variant-spot", "default").
					Queue("lq").
					AllowedFlavors("spot").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Request(corev1.ResourceCPU, "1").
					Obj(),
				*utiltestingapi.MakeWorkload("wl-variant-on-demand", "default").
					Queue("lq").
					AllowedFlavors("on-demand").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Request(corev1.ResourceCPU, "1").
					Obj(),
			},
			wantParentWorkload: utiltestingapi.MakeWorkload("wl-12345", "default").
				Queue("lq").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Request(corev1.ResourceCPU, "1").
				Active(false).
				Obj(),
			wantVariantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-variant-spot", "default").
					Queue("lq").
					AllowedFlavors("spot").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Request(corev1.ResourceCPU, "1").
					Active(false).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-variant-on-demand", "default").
					Queue("lq").
					AllowedFlavors("on-demand").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Request(corev1.ResourceCPU, "1").
					Active(false).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "wl-variant-spot"},
					EventType: corev1.EventTypeNormal,
					Reason:    ReasonDeactivatedVariant,
					Message:   "Variant Workload deactivated due to Parent Workload \"default/wl-12345\" not active",
				},
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "wl-variant-on-demand"},
					EventType: corev1.EventTypeNormal,
					Reason:    ReasonDeactivatedVariant,
					Message:   "Variant Workload deactivated due to Parent Workload \"default/wl-12345\" not active",
				},
			},
		},
		"parent changes WaitForPodsReady from False to True, syncs to admitted variant": {
			parentWorkload: utiltestingapi.MakeWorkload("wl-12345", "default").
				Queue("lq").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Request(corev1.ResourceCPU, "1").
				SimpleReserveQuota("cq", "spot", metav1.Now().Time).
				AdmittedAt(true, metav1.Now().Time).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadPodsReady,
					Status:  metav1.ConditionTrue,
					Reason:  "PodsReady",
					Message: "Pods are ready",
				}).
				Obj(),
			variantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-variant-spot", "default").
					Queue("lq").
					AllowedFlavors("spot").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Request(corev1.ResourceCPU, "1").
					SimpleReserveQuota("cq", "spot", metav1.Now().Time).
					AdmittedAt(true, metav1.Now().Time).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadPodsReady,
						Status:  metav1.ConditionFalse,
						Reason:  "PodsNotReady",
						Message: "Pods are not ready",
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-variant-on-demand", "default").
					Queue("lq").
					AllowedFlavors("on-demand").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Request(corev1.ResourceCPU, "1").
					Obj(),
			},
			wantParentWorkload: utiltestingapi.MakeWorkload("wl-12345", "default").
				Queue("lq").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Request(corev1.ResourceCPU, "1").
				SimpleReserveQuota("cq", "spot", metav1.Now().Time).
				AdmittedAt(true, metav1.Now().Time).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadPodsReady,
					Status:  metav1.ConditionTrue,
					Reason:  "PodsReady",
					Message: "Pods are ready",
				}).
				Obj(),
			wantVariantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-variant-spot", "default").
					Queue("lq").
					AllowedFlavors("spot").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Request(corev1.ResourceCPU, "1").
					SimpleReserveQuota("cq", "spot", metav1.Now().Time).
					AdmittedAt(true, metav1.Now().Time).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadPodsReady,
						Status:  metav1.ConditionTrue,
						Reason:  "PodsReady",
						Message: "Pods are ready",
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-variant-on-demand", "default").
					Queue("lq").
					AllowedFlavors("on-demand").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Request(corev1.ResourceCPU, "1").
					Obj(),
			},
		},
		"parent changes WaitForPodsReady from True to False, syncs to admitted variant": {
			parentWorkload: utiltestingapi.MakeWorkload("wl-12345", "default").
				Queue("lq").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Request(corev1.ResourceCPU, "1").
				SimpleReserveQuota("cq", "spot", metav1.Now().Time).
				AdmittedAt(true, metav1.Now().Time).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadPodsReady,
					Status:  metav1.ConditionFalse,
					Reason:  "PodsNotReady",
					Message: "Pods are not ready",
				}).
				Obj(),
			variantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-variant-spot", "default").
					Queue("lq").
					AllowedFlavors("spot").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Request(corev1.ResourceCPU, "1").
					SimpleReserveQuota("cq", "spot", metav1.Now().Time).
					AdmittedAt(true, metav1.Now().Time).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadPodsReady,
						Status:  metav1.ConditionTrue,
						Reason:  "PodsReady",
						Message: "Pods are ready",
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-variant-on-demand", "default").
					Queue("lq").
					AllowedFlavors("on-demand").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Request(corev1.ResourceCPU, "1").
					Obj(),
			},
			wantParentWorkload: utiltestingapi.MakeWorkload("wl-12345", "default").
				Queue("lq").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Request(corev1.ResourceCPU, "1").
				SimpleReserveQuota("cq", "spot", metav1.Now().Time).
				AdmittedAt(true, metav1.Now().Time).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadPodsReady,
					Status:  metav1.ConditionFalse,
					Reason:  "PodsNotReady",
					Message: "Pods are not ready",
				}).
				Obj(),
			wantVariantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-variant-spot", "default").
					Queue("lq").
					AllowedFlavors("spot").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Request(corev1.ResourceCPU, "1").
					SimpleReserveQuota("cq", "spot", metav1.Now().Time).
					AdmittedAt(true, metav1.Now().Time).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadPodsReady,
						Status:  metav1.ConditionFalse,
						Reason:  "PodsNotReady",
						Message: "Pods are not ready",
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-variant-on-demand", "default").
					Queue("lq").
					AllowedFlavors("on-demand").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Request(corev1.ResourceCPU, "1").
					Obj(),
			},
		},
		"parent not admitted, variant admitted, parent changes WaitForPodsReady from True to False, syncs to variant and admits parent": {
			parentWorkload: utiltestingapi.MakeWorkload("wl-12345", "default").
				Queue("lq").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Request(corev1.ResourceCPU, "1").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadPodsReady,
					Status:  metav1.ConditionFalse,
					Reason:  "PodsNotReady",
					Message: "Pods are not ready",
				}).
				Obj(),
			variantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-variant-spot", "default").
					Queue("lq").
					AllowedFlavors("spot").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Request(corev1.ResourceCPU, "1").
					SimpleReserveQuota("cq", "spot", metav1.Now().Time).
					AdmittedAt(true, metav1.Now().Time).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadPodsReady,
						Status:  metav1.ConditionTrue,
						Reason:  "PodsReady",
						Message: "Pods are ready",
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-variant-on-demand", "default").
					Queue("lq").
					AllowedFlavors("on-demand").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Request(corev1.ResourceCPU, "1").
					Obj(),
			},
			wantParentWorkload: utiltestingapi.MakeWorkload("wl-12345", "default").
				Queue("lq").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Request(corev1.ResourceCPU, "1").
				Admission(utiltestingapi.MakeAdmission("cq", "main").
					PodSets(kueue.PodSetAssignment{
						Name: "main",
						Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
							corev1.ResourceCPU: "spot",
						},
						Count:         ptr.To[int32](1),
						ResourceUsage: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")},
					}).Obj()).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadAdmitted,
					Status:  metav1.ConditionTrue,
					Reason:  "Admitted",
					Message: "The variant wl-variant-spot is admitted",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionTrue,
					Reason:  "QuotaReserved",
					Message: "Quota reserved in ClusterQueue cq",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadPodsReady,
					Status:  metav1.ConditionFalse,
					Reason:  "PodsNotReady",
					Message: "Pods are not ready",
				}).
				Obj(),
			wantVariantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-variant-spot", "default").
					Queue("lq").
					AllowedFlavors("spot").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Request(corev1.ResourceCPU, "1").
					SimpleReserveQuota("cq", "spot", metav1.Now().Time).
					AdmittedAt(true, metav1.Now().Time).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadPodsReady,
						Status:  metav1.ConditionFalse,
						Reason:  "PodsNotReady",
						Message: "Pods are not ready",
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-variant-on-demand", "default").
					Queue("lq").
					AllowedFlavors("on-demand").
					PreemptionGates(caGate()).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "wl-12345", "").
					Request(corev1.ResourceCPU, "1").
					Obj(),
			},
		},
	}

	scenarios := []map[featuregate.Feature]bool{
		{
			features.UnadmittedWorkloadsObservability: false,
		},
		{
			features.UnadmittedWorkloadsObservability: true,
		},
	}

	for name, tc := range testCases {
		for _, scenario := range scenarios {
			t.Run(fmt.Sprintf("%s UnadmittedWorkloadsObservability enabled: %t", name, scenario[features.UnadmittedWorkloadsObservability]), func(t *testing.T) {
				features.SetFeatureGateDuringTest(t, features.ConcurrentAdmission, true)
				features.SetFeatureGatesDuringTest(t, scenario)
				var objects []client.Object
				if tc.parentWorkload != nil {
					objects = append(objects, tc.parentWorkload)
				}
				for i := range tc.variantWorkloads {
					objects = append(objects, &tc.variantWorkloads[i])
				}
				cl := utiltesting.NewClientBuilder().
					WithObjects(objects...).
					WithStatusSubresource(objects...).
					Build()
				preemptionExpectations := preemptexpectations.New()
				qManager := qcache.NewManagerForUnitTests(cl, nil, qcache.WithPreemptionExpectations(preemptionExpectations))
				roleTracker := roletracker.NewFakeRoleTracker(roletracker.RoleLeader)

				cqs := []*kueue.ClusterQueue{defaultCQ.DeepCopy(), migrationCQ.DeepCopy(), migrationCQNoConstraint.DeepCopy(), retainFirstAdmissionCQ.DeepCopy(), spotOnlyCQ.DeepCopy()}
				lqs := []*kueue.LocalQueue{defaultLQ.DeepCopy(), migrationLQ.DeepCopy(), migrationLQNoConstraint.DeepCopy(), retainFirstAdmissionLQ.DeepCopy(), spotOnlyLQ.DeepCopy()}

				for _, cq := range cqs {
					if err := cl.Create(t.Context(), cq); err != nil {
						t.Fatal(err)
					}
					if err := qManager.AddClusterQueue(t.Context(), cq); err != nil {
						t.Fatal(err)
					}
				}

				for _, lq := range lqs {
					if err := cl.Create(t.Context(), lq); err != nil {
						t.Fatal(err)
					}
					if err := qManager.AddLocalQueue(t.Context(), lq); err != nil {
						t.Fatal(err)
					}
				}

				for i := range tc.variantWorkloads {
					if workload.IsAdmissible(&tc.variantWorkloads[i]) {
						if err := qManager.AddOrUpdateWorkload(ctrl.Log, tc.variantWorkloads[i].DeepCopy()); err != nil {
							t.Fatalf("Failed to add workload to qManager: %v", err)
						}
					}
				}

				r := &variantReconciler{
					logName:     ConcurrentAdmissionController,
					client:      cl,
					queues:      qManager,
					roleTracker: roleTracker,
					clock:       testingclock.NewFakeClock(fakeNow),
					recorder:    &utiltesting.EventRecorder{},
				}

				req := tc.req
				if req.Name == "" && tc.parentWorkload != nil {
					req = reconcile.Request{
						NamespacedName: types.NamespacedName{
							Namespace: tc.parentWorkload.Namespace,
							Name:      tc.parentWorkload.Name,
						},
					}
				}

				got, err := r.Reconcile(t.Context(), req)
				if err != nil {
					t.Fatalf("Reconcile() unexpected error: %v", err)
				}
				if diff := cmp.Diff(tc.wantResult, got); diff != "" {
					t.Errorf("Reconcile() unexpected result (-want +got):\n%s", diff)
				}

				if tc.wantParentWorkload != nil {
					var gotParent kueue.Workload
					err := cl.Get(t.Context(), types.NamespacedName{Namespace: tc.wantParentWorkload.Namespace, Name: tc.wantParentWorkload.Name}, &gotParent)
					if err != nil {
						t.Fatal(err)
					}
					if diff := cmp.Diff(tc.wantParentWorkload, &gotParent, workloadCmpOpts); diff != "" {
						t.Errorf("Unexpected parent workload (-want +got):\n%s", diff)
					}
				}

				var allWorkloads kueue.WorkloadList
				if err := cl.List(t.Context(), &allWorkloads, client.InNamespace(tc.req.Namespace)); err != nil {
					t.Fatal(err)
				}

				var gotVariants []kueue.Workload
				for _, wl := range allWorkloads.Items {
					if concurrentadmission.IsVariant(&wl) {
						gotVariants = append(gotVariants, wl)
					}
				}

				wantVariantWorkloads := make([]kueue.Workload, len(tc.wantVariantWorkloads))
				for i := range tc.wantVariantWorkloads {
					wantVariantWorkloads[i] = *tc.wantVariantWorkloads[i].DeepCopy()
					if scenario[features.UnadmittedWorkloadsObservability] {
						cond := apimeta.FindStatusCondition(wantVariantWorkloads[i].Status.Conditions, kueue.WorkloadQuotaReserved)
						if cond != nil && cond.Status == metav1.ConditionFalse && cond.Reason == kueue.WorkloadPending { //nolint:staticcheck // SA1019: fallback
							cond.Reason = kueue.WorkloadQuotaReservedReasonPendingEvaluation
						}
					}
				}

				if diff := cmp.Diff(wantVariantWorkloads, gotVariants, workloadCmpOpts); diff != "" {
					t.Errorf("Unexpected variant workloads (-want +got):\n%s", diff)
				}

				gotEvents := r.recorder.(*utiltesting.EventRecorder).RecordedEvents
				if diff := cmp.Diff(tc.wantEvents, gotEvents, cmpopts.SortSlices(utiltesting.SortEvents)); diff != "" {
					t.Errorf("Unexpected events (-want +got):\n%s", diff)
				}
			})
		}
	}
}

func TestClusterQueueFlavorsChanged(t *testing.T) {
	base := utiltestingapi.MakeClusterQueue("cq").
		ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "5").Obj(),
			*utiltestingapi.MakeFlavorQuotas("spot").Resource(corev1.ResourceCPU, "5").Obj(),
		).Obj()

	flavorAdded := base.DeepCopy()
	flavorAdded.Spec.ResourceGroups[0].Flavors = append(flavorAdded.Spec.ResourceGroups[0].Flavors,
		*utiltestingapi.MakeFlavorQuotas("reservation").Obj())

	flavorRemoved := base.DeepCopy()
	flavorRemoved.Spec.ResourceGroups[0].Flavors = flavorRemoved.Spec.ResourceGroups[0].Flavors[:1]

	flavorsReordered := base.DeepCopy()
	rg := &flavorsReordered.Spec.ResourceGroups[0]
	rg.Flavors[0], rg.Flavors[1] = rg.Flavors[1], rg.Flavors[0]

	quotaOnly := base.DeepCopy()
	quotaOnly.Spec.ResourceGroups[0].Flavors[0].Resources[0].NominalQuota = resource.MustParse("99")

	statusOnly := base.DeepCopy()
	statusOnly.Status.PendingWorkloads = 5

	pred := clusterQueueFlavorsChanged()

	updateCases := map[string]struct {
		old  *kueue.ClusterQueue
		new  *kueue.ClusterQueue
		want bool
	}{
		"flavor added":        {old: base, new: flavorAdded, want: true},
		"flavor removed":      {old: base, new: flavorRemoved, want: true},
		"flavors reordered":   {old: base, new: flavorsReordered, want: true},
		"flavors unchanged":   {old: base, new: base.DeepCopy(), want: false},
		"only quota changed":  {old: base, new: quotaOnly, want: false},
		"only status changed": {old: base, new: statusOnly, want: false},
	}
	for name, tc := range updateCases {
		t.Run(name, func(t *testing.T) {
			got := pred.Update(event.TypedUpdateEvent[*kueue.ClusterQueue]{ObjectOld: tc.old, ObjectNew: tc.new})
			if got != tc.want {
				t.Errorf("Update() = %v, want %v", got, tc.want)
			}
		})
	}

	ignoredEventCases := map[string]bool{
		"Create":  pred.Create(event.TypedCreateEvent[*kueue.ClusterQueue]{Object: base}),
		"Delete":  pred.Delete(event.TypedDeleteEvent[*kueue.ClusterQueue]{Object: base}),
		"Generic": pred.Generic(event.TypedGenericEvent[*kueue.ClusterQueue]{Object: base}),
	}
	for name, got := range ignoredEventCases {
		t.Run(name, func(t *testing.T) {
			if got {
				t.Errorf("%s() = true, want false", name)
			}
		})
	}
}

func TestParentsForClusterQueue(t *testing.T) {
	parentWl := func(name, ns, lq string) *kueue.Workload {
		return utiltestingapi.MakeWorkload(name, ns).
			Queue(kueue.LocalQueueName(lq)).
			Label(constants.ConcurrentAdmissionParentLabelKey, "true").
			Obj()
	}
	variantWl := func(name, ns, lq, flavor string) *kueue.Workload {
		return utiltestingapi.MakeWorkload(name, ns).
			Queue(kueue.LocalQueueName(lq)).
			AllowedFlavors(kueue.ResourceFlavorReference(flavor)).
			ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "parent", "").
			Obj()
	}
	req := func(name, ns string) reconcile.Request {
		return reconcile.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: name}}
	}

	testCases := map[string]struct {
		clusterQueue string
		localQueues  []*kueue.LocalQueue
		workloads    []*kueue.Workload
		want         []reconcile.Request
	}{
		"multiple parents under one ClusterQueue": {
			clusterQueue: "cq",
			localQueues:  []*kueue.LocalQueue{utiltestingapi.MakeLocalQueue("lq", "default").ClusterQueue("cq").Obj()},
			workloads: []*kueue.Workload{
				parentWl("parent-a", "default", "lq"),
				parentWl("parent-b", "default", "lq"),
			},
			want: []reconcile.Request{req("parent-a", "default"), req("parent-b", "default")},
		},
		"parents across namespaces feeding the same ClusterQueue": {
			clusterQueue: "cq",
			localQueues: []*kueue.LocalQueue{
				utiltestingapi.MakeLocalQueue("lq", "ns-a").ClusterQueue("cq").Obj(),
				utiltestingapi.MakeLocalQueue("lq", "ns-b").ClusterQueue("cq").Obj(),
			},
			workloads: []*kueue.Workload{
				parentWl("parent-a", "ns-a", "lq"),
				parentWl("parent-b", "ns-b", "lq"),
			},
			want: []reconcile.Request{req("parent-a", "ns-a"), req("parent-b", "ns-b")},
		},
		"non-parent workloads are excluded": {
			clusterQueue: "cq",
			localQueues:  []*kueue.LocalQueue{utiltestingapi.MakeLocalQueue("lq", "default").ClusterQueue("cq").Obj()},
			workloads: []*kueue.Workload{
				parentWl("parent", "default", "lq"),
				variantWl("variant", "default", "lq", "spot"),
				utiltestingapi.MakeWorkload("plain", "default").Queue("lq").Obj(),
			},
			want: []reconcile.Request{req("parent", "default")},
		},
		"workloads of other ClusterQueues are excluded": {
			clusterQueue: "cq",
			localQueues: []*kueue.LocalQueue{
				utiltestingapi.MakeLocalQueue("lq", "default").ClusterQueue("cq").Obj(),
				utiltestingapi.MakeLocalQueue("lq-other", "default").ClusterQueue("cq-other").Obj(),
			},
			workloads: []*kueue.Workload{
				parentWl("parent", "default", "lq"),
				parentWl("parent-other", "default", "lq-other"),
			},
			want: []reconcile.Request{req("parent", "default")},
		},
		"no parents returns nothing": {
			clusterQueue: "cq",
			localQueues:  []*kueue.LocalQueue{utiltestingapi.MakeLocalQueue("lq", "default").ClusterQueue("cq").Obj()},
			workloads:    []*kueue.Workload{utiltestingapi.MakeWorkload("plain", "default").Queue("lq").Obj()},
			want:         nil,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			var objects []client.Object
			for _, lq := range tc.localQueues {
				objects = append(objects, lq)
			}
			for _, wl := range tc.workloads {
				objects = append(objects, wl)
			}
			cl := utiltesting.NewClientBuilder().WithObjects(objects...).Build()
			r := &variantReconciler{client: cl}

			got := r.parentsForClusterQueue(t.Context(), utiltestingapi.MakeClusterQueue(tc.clusterQueue).Obj())
			if diff := cmp.Diff(tc.want, got, cmpopts.SortSlices(func(a, b reconcile.Request) bool {
				return a.String() < b.String()
			})); diff != "" {
				t.Errorf("parentsForClusterQueue() unexpected requests (-want +got):\n%s", diff)
			}
		})
	}
}

// TestCreateVariantsAlreadyExists checks that createVariants treats an
// already-existing Variant as a no-op instead of a reconcile error.
func TestCreateVariantsAlreadyExists(t *testing.T) {
	parent := utiltestingapi.MakeWorkload("parent-wl", "default").
		Queue("lq").
		Request(corev1.ResourceCPU, "1").
		Label(constants.ConcurrentAdmissionParentLabelKey, "true").
		Obj()
	existing := generateVariant(parent, "spot")
	cl := utiltesting.NewClientBuilder().WithObjects(parent, existing).Build()
	r := &variantReconciler{client: cl, recorder: &utiltesting.EventRecorder{}}

	// Empty variants slice so hasVariantWithFlavor is false and Create is attempted.
	err := r.createVariants(t.Context(), parent, nil, []kueue.FlavorQuotas{*utiltestingapi.MakeFlavorQuotas("spot").Obj()})
	if err != nil {
		t.Fatalf("createVariants() with a pre-existing variant should be a no-op, got error: %v", err)
	}

	if err := cl.Get(t.Context(), client.ObjectKeyFromObject(existing), &kueue.Workload{}); err != nil {
		t.Fatalf("pre-existing variant should still exist: %v", err)
	}
}
