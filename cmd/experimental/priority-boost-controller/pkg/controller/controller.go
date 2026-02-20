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

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/cmd/experimental/priority-boost-controller/pkg/constants"
)

type PriorityBoostReconciler struct {
	client   client.Client
	log      logr.Logger
	recorder record.EventRecorder
	maxBoost int32
}

var _ reconcile.Reconciler = (*PriorityBoostReconciler)(nil)

type PriorityBoostReconcilerOptions struct {
	MaxBoost int32
}

type PriorityBoostReconcilerOption func(*PriorityBoostReconcilerOptions)

func WithMaxBoost(maxBoost int32) PriorityBoostReconcilerOption {
	return func(o *PriorityBoostReconcilerOptions) {
		o.MaxBoost = maxBoost
	}
}

var defaultOptions = PriorityBoostReconcilerOptions{
	MaxBoost: 100000,
}

func NewPriorityBoostReconciler(c client.Client, recorder record.EventRecorder, opts ...PriorityBoostReconcilerOption) *PriorityBoostReconciler {
	options := defaultOptions
	for _, opt := range opts {
		opt(&options)
	}
	return &PriorityBoostReconciler{
		client:   c,
		log:      ctrl.Log.WithName(constants.ControllerName),
		recorder: recorder,
		maxBoost: options.MaxBoost,
	}
}

// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads,verbs=get;list;watch;patch
func (r *PriorityBoostReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)

	var wl kueue.Workload
	if err := r.client.Get(ctx, req.NamespacedName, &wl); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !wl.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	newBoost := r.computeBoost(&wl)
	newBoostValue := strconv.FormatInt(int64(newBoost), 10)
	currentBoostValue := wl.Annotations[constants.PriorityBoostAnnotationKey]
	if currentBoostValue == newBoostValue {
		return ctrl.Result{}, nil
	}

	patch := client.MergeFrom(wl.DeepCopy())
	if wl.Annotations == nil {
		wl.Annotations = make(map[string]string)
	}
	wl.Annotations[constants.PriorityBoostAnnotationKey] = newBoostValue
	if err := r.client.Patch(ctx, &wl, patch); err != nil {
		return ctrl.Result{}, err
	}

	log.Info("Updated priority-boost annotation",
		"workload", klog.KObj(&wl),
		"annotationKey", constants.PriorityBoostAnnotationKey,
		"annotationValue", newBoostValue)
	r.recorder.Eventf(&wl, corev1.EventTypeNormal, "PriorityBoostUpdated",
		"Updated %s=%s", constants.PriorityBoostAnnotationKey, newBoostValue)

	return ctrl.Result{}, nil
}

func (r *PriorityBoostReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return builder.TypedControllerManagedBy[reconcile.Request](mgr).
		For(&kueue.Workload{}).
		Complete(r)
}

// computeBoost calculates a priority boost value for the workload based on
// the number of prior preemption evictions recorded in schedulingStats.
// To limit preemption flapping, the boost increments only on every 2nd
// preemption (threshold-based approach).
func (r *PriorityBoostReconciler) computeBoost(wl *kueue.Workload) int32 {
	var preemptionCount int32
	if wl.Status.SchedulingStats != nil {
		for _, eviction := range wl.Status.SchedulingStats.Evictions {
			if eviction.Reason == kueue.WorkloadEvictedByPreemption {
				preemptionCount += eviction.Count
			}
		}
	}
	boost := preemptionCount / 2
	if r.maxBoost > 0 {
		boost = min(boost, r.maxBoost)
	}
	boost = max(boost, 0)
	return boost
}
