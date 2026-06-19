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
	"strconv"
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
	"sigs.k8s.io/kueue/cmd/experimental/kueue-priority-booster/pkg/constants"
)

// PriorityBoostReconciler watches Workload objects and, once a workload has
// been admitted for at least timeSharingInterval, sets the
// kueue.x-k8s.io/priority-boost annotation to a negative value. The negative
// boost lowers the workload's effective priority so same-base-priority pending
// workloads can preempt it under withinClusterQueue: LowerPriority. No
// annotation is set during the time-sharing interval.
type PriorityBoostReconciler struct {
	client              client.Client
	log                 logr.Logger
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
		log:                 ctrl.Log.WithName(constants.ControllerName),
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
		return ctrl.Result{}, r.clearBoostAnnotationIfPresent(ctx, &wl, log)
	}

	newBoost, requeueAfter := r.computeBoost(&wl)
	// Skip Patch only when the annotation already matches computeBoost's target. Do not use
	// requeueAfter != 0 alone: while admitted inside timeSharingInterval, newBoost is 0 and
	// requeueAfter is positive, but a stale non-empty annotation must still be cleared.
	needAnnotationPatch := !r.hasDesiredNegativeBoostValue(&wl, newBoost)
	if !needAnnotationPatch {
		return ctrl.Result{RequeueAfter: requeueAfter}, nil
	}

	patch := client.MergeFrom(wl.DeepCopy())
	if newBoost != 0 {
		r.setBoostAnnotation(&wl, newBoost, requeueAfter, log)
	} else {
		r.clearBoostAnnotation(&wl, log)
	}

	if err := r.client.Patch(ctx, &wl, patch); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

// hasDesiredNegativeBoostValue reports whether the workload's current annotation already
// matches the boost value the controller would set (empty string when newBoost
// is 0).
func (r *PriorityBoostReconciler) hasDesiredNegativeBoostValue(wl *kueue.Workload, newBoost int32) bool {
	var desired string
	if newBoost != 0 {
		desired = strconv.FormatInt(int64(newBoost), 10)
	}
	return wl.Annotations[constants.PriorityBoostAnnotationKey] == desired
}

func (r *PriorityBoostReconciler) setBoostAnnotation(wl *kueue.Workload, newBoost int32, requeueAfter time.Duration, log logr.Logger) {
	desiredBoostStr := strconv.FormatInt(int64(newBoost), 10)
	if wl.Annotations == nil {
		wl.Annotations = make(map[string]string)
	}
	wl.Annotations[constants.PriorityBoostAnnotationKey] = desiredBoostStr
	log.Info("Setting priority-boost annotation after time-sharing window",
		"workload", klog.KObj(wl),
		"annotationKey", constants.PriorityBoostAnnotationKey,
		"annotationValue", desiredBoostStr,
		"requeueAfter", requeueAfter)
	r.recorder.Eventf(wl, nil, corev1.EventTypeNormal, "PriorityBoostSet", "PriorityBoostSet",
		"Set %s=%s after time-sharing window",
		constants.PriorityBoostAnnotationKey, desiredBoostStr)
}

func (r *PriorityBoostReconciler) clearBoostAnnotation(wl *kueue.Workload, log logr.Logger) {
	delete(wl.Annotations, constants.PriorityBoostAnnotationKey)
	log.Info("Removing priority-boost annotation",
		"workload", klog.KObj(wl),
		"annotationKey", constants.PriorityBoostAnnotationKey)
	r.recorder.Eventf(wl, nil, corev1.EventTypeNormal, "PriorityBoostCleared", "PriorityBoostCleared",
		"Cleared %s (workload not in scope, not admitted, or feature disabled)",
		constants.PriorityBoostAnnotationKey)
}

// clearBoostAnnotationIfPresent patches the workload to remove the
// priority-boost annotation only when it holds a controller-managed (negative)
// value. Manually-set annotations (zero or positive) are left untouched.
func (r *PriorityBoostReconciler) clearBoostAnnotationIfPresent(ctx context.Context, wl *kueue.Workload, log logr.Logger) error {
	val := wl.Annotations[constants.PriorityBoostAnnotationKey]
	if val == "" {
		return nil
	}
	// Non-negative or unparseable values are treated as manually-set and left alone;
	n, _ := strconv.ParseInt(val, 10, 64)
	if n >= 0 {
		return nil
	}
	patch := client.MergeFrom(wl.DeepCopy())
	r.clearBoostAnnotation(wl, log)
	return r.client.Patch(ctx, wl, patch)
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
// our annotation (so it can be cleared).
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
	return wl.Annotations[constants.PriorityBoostAnnotationKey] != ""
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
