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

package controller

import (
	"context"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/tools/events"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

// PriorityBoostReconciler watches Workload objects and, once a workload has
// been admitted for at least timeSharingInterval, sets the
// Workload spec.priorityBoost to a negative value. The negative
// boost lowers the workload's effective priority so same-base-priority pending
// workloads can preempt it under withinClusterQueue: LowerPriority. No boost
// is set during the time-sharing interval.
type PriorityBoostReconciler struct {
	client              client.Client
	recorder            events.EventRecorder
	timeSharingInterval time.Duration
	negativeBoostValue  int32
	workloadSelector    labels.Selector
	maxWorkloadPriority *int32
}

var _ reconcile.Reconciler = (*PriorityBoostReconciler)(nil)

type PriorityBoostReconcilerOptions struct {
	TimeSharingInterval time.Duration
	NegativeBoostValue  int32
	WorkloadSelector    labels.Selector
	MaxWorkloadPriority *int32
}

type PriorityBoostReconcilerOption func(*PriorityBoostReconcilerOptions)

func WithTimeSharingInterval(d time.Duration) PriorityBoostReconcilerOption {
	return func(o *PriorityBoostReconcilerOptions) {
		o.TimeSharingInterval = d
	}
}

func WithNegativeBoostValue(v int32) PriorityBoostReconcilerOption {
	return func(o *PriorityBoostReconcilerOptions) {
		o.NegativeBoostValue = v
	}
}

func WithWorkloadSelector(s labels.Selector) PriorityBoostReconcilerOption {
	return func(o *PriorityBoostReconcilerOptions) {
		o.WorkloadSelector = s
	}
}

func WithMaxWorkloadPriority(p *int32) PriorityBoostReconcilerOption {
	return func(o *PriorityBoostReconcilerOptions) {
		o.MaxWorkloadPriority = p
	}
}

var defaultOptions = PriorityBoostReconcilerOptions{
	TimeSharingInterval: 0,
	NegativeBoostValue:  100000,
}

func NewPriorityBoostReconciler(c client.Client, recorder events.EventRecorder, opts ...PriorityBoostReconcilerOption) *PriorityBoostReconciler {
	options := defaultOptions
	for _, opt := range opts {
		opt(&options)
	}
	return &PriorityBoostReconciler{
		client:              c,
		recorder:            recorder,
		timeSharingInterval: options.TimeSharingInterval,
		negativeBoostValue:  options.NegativeBoostValue,
		workloadSelector:    options.WorkloadSelector,
		maxWorkloadPriority: options.MaxWorkloadPriority,
	}
}

// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups="events.k8s.io",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads,verbs=get;list;watch;patch

func (r *PriorityBoostReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)

	var wl kueue.Workload
	if err := r.client.Get(ctx, req.NamespacedName, &wl); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !r.workloadInScope(&wl) {
		return ctrl.Result{}, r.clearBoostIfPresent(ctx, &wl, log)
	}

	newBoost, requeueAfter := r.computeBoost(&wl)
	// Skip Patch only when the field already matches computeBoost's target. Do not use
	// requeueAfter != 0 alone: while admitted inside timeSharingInterval, newBoost is 0 and
	// requeueAfter is positive, but a stale boost must still be cleared.
	needBoostPatch := !r.hasDesiredBoost(&wl, newBoost)
	if !needBoostPatch {
		return ctrl.Result{RequeueAfter: requeueAfter}, nil
	}

	patch := client.MergeFrom(wl.DeepCopy())
	if newBoost != 0 {
		r.setBoost(&wl, newBoost)
	} else {
		r.clearBoost(&wl)
	}

	if err := r.client.Patch(ctx, &wl, patch); err != nil {
		return ctrl.Result{}, err
	}
	if newBoost != 0 {
		log.V(3).Info("Set spec.priorityBoost after time-sharing window",
			"workload", klog.KObj(&wl),
			"priorityBoost", newBoost,
			"requeueAfter", requeueAfter)
	} else {
		log.V(3).Info("Removed spec.priorityBoost",
			"workload", klog.KObj(&wl))
	}

	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

// hasDesiredBoost reports whether the workload's spec.priorityBoost already
// matches the boost value the controller would set (unset when newBoost is 0).
func (r *PriorityBoostReconciler) hasDesiredBoost(wl *kueue.Workload, newBoost int32) bool {
	if newBoost == 0 {
		return wl.Spec.PriorityBoost == nil
	}
	return wl.Spec.PriorityBoost != nil && *wl.Spec.PriorityBoost == newBoost
}

func (r *PriorityBoostReconciler) setBoost(wl *kueue.Workload, newBoost int32) {
	wl.Spec.PriorityBoost = &newBoost
	r.recorder.Eventf(wl, nil, corev1.EventTypeNormal, "PriorityBoostSet", "PriorityBoostSet",
		"Set spec.priorityBoost=%d after time-sharing window", newBoost)
}

func (r *PriorityBoostReconciler) clearBoost(wl *kueue.Workload) {
	wl.Spec.PriorityBoost = nil
	r.recorder.Eventf(wl, nil, corev1.EventTypeNormal, "PriorityBoostCleared", "PriorityBoostCleared",
		"Cleared spec.priorityBoost (workload not in scope, not admitted, or feature disabled)")
}

// clearBoostIfPresent patches the workload to remove spec.priorityBoost only
// when it holds a controller-managed (negative) value. Manually-set values
// (zero or positive) are left untouched.
func (r *PriorityBoostReconciler) clearBoostIfPresent(ctx context.Context, wl *kueue.Workload, log logr.Logger) error {
	if wl.Spec.PriorityBoost == nil || *wl.Spec.PriorityBoost >= 0 {
		return nil
	}
	patch := client.MergeFrom(wl.DeepCopy())
	r.clearBoost(wl)
	if err := r.client.Patch(ctx, wl, patch); err != nil {
		return err
	}
	log.V(3).Info("Removed spec.priorityBoost",
		"workload", klog.KObj(wl))
	return nil
}

func (r *PriorityBoostReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return builder.TypedControllerManagedBy[reconcile.Request](mgr).
		For(&kueue.Workload{}).
		WithEventFilter(predicate.Funcs{
			CreateFunc: func(e event.CreateEvent) bool {
				return r.shouldEnqueueWorkload(e.Object)
			},
			UpdateFunc: func(e event.UpdateEvent) bool {
				return r.shouldEnqueueWorkload(e.ObjectOld) || r.shouldEnqueueWorkload(e.ObjectNew)
			},
			DeleteFunc: func(e event.DeleteEvent) bool {
				return false
			},
			GenericFunc: func(e event.GenericEvent) bool {
				return false
			},
		}).
		Complete(r)
}

// shouldEnqueueWorkload limits the Workload informer to objects this controller
// may change: in-scope workloads, or out-of-scope workloads that still carry
// a boost (so it can be cleared).
func (r *PriorityBoostReconciler) shouldEnqueueWorkload(o client.Object) bool {
	wl, ok := o.(*kueue.Workload)
	if !ok || wl == nil {
		return false
	}
	if !wl.DeletionTimestamp.IsZero() {
		return false
	}
	if r.workloadInScope(wl) {
		return true
	}
	return wl.Spec.PriorityBoost != nil
}

func (r *PriorityBoostReconciler) workloadInScope(wl *kueue.Workload) bool {
	if r.workloadSelector != nil && !r.workloadSelector.Matches(labels.Set(wl.Labels)) {
		return false
	}
	if r.maxWorkloadPriority != nil {
		var p int32
		if wl.Spec.Priority != nil {
			p = *wl.Spec.Priority
		}
		if p > *r.maxWorkloadPriority {
			return false
		}
	}
	return true
}

// computeBoost returns the signed boost to apply and how long until the
// controller should re-evaluate.
//
// With timeSharingInterval > 0: while admitted and elapsed < timeSharingInterval,
// returns (0, remaining). While admitted after the interval, returns
// (-negativeBoostValue, 0). Otherwise returns (0, 0).
func (r *PriorityBoostReconciler) computeBoost(wl *kueue.Workload) (boost int32, requeueAfter time.Duration) {
	if r.timeSharingInterval <= 0 {
		return 0, 0
	}

	admittedCond := meta.FindStatusCondition(wl.Status.Conditions, string(kueue.WorkloadAdmitted))
	if admittedCond == nil || admittedCond.Status != metav1.ConditionTrue {
		return 0, 0
	}

	elapsed := time.Since(admittedCond.LastTransitionTime.Time)
	if elapsed < r.timeSharingInterval {
		remaining := r.timeSharingInterval - elapsed
		return 0, remaining
	}

	return -r.negativeBoostValue, 0
}
