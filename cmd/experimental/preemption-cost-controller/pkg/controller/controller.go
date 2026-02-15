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
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/cmd/experimental/preemption-cost-controller/pkg/constants"
)

type PreemptionCostReconciler struct {
	client                      client.Client
	log                         logr.Logger
	recorder                    record.EventRecorder
	enableRunningPhaseIncrement bool
	runningPhaseIncrement       int32
	maxCost                     int32
}

var _ reconcile.Reconciler = (*PreemptionCostReconciler)(nil)

type PreemptionCostReconcilerOptions struct {
	EnableRunningPhaseIncrement bool
	RunningPhaseIncrement       int32
	MaxCost                     int32
}

type PreemptionCostReconcilerOption func(*PreemptionCostReconcilerOptions)

func WithRunningPhaseIncrementEnabled(enabled bool) PreemptionCostReconcilerOption {
	return func(o *PreemptionCostReconcilerOptions) {
		o.EnableRunningPhaseIncrement = enabled
	}
}

func WithRunningPhaseIncrement(increment int32) PreemptionCostReconcilerOption {
	return func(o *PreemptionCostReconcilerOptions) {
		o.RunningPhaseIncrement = increment
	}
}

func WithMaxCost(maxCost int32) PreemptionCostReconcilerOption {
	return func(o *PreemptionCostReconcilerOptions) {
		o.MaxCost = maxCost
	}
}

var defaultOptions = PreemptionCostReconcilerOptions{
	EnableRunningPhaseIncrement: false,
	RunningPhaseIncrement:       1,
	MaxCost:                     100000,
}

func NewPreemptionCostReconciler(c client.Client, recorder record.EventRecorder, opts ...PreemptionCostReconcilerOption) *PreemptionCostReconciler {
	options := defaultOptions
	for _, opt := range opts {
		opt(&options)
	}
	return &PreemptionCostReconciler{
		client:                      c,
		log:                         ctrl.Log.WithName(constants.ControllerName),
		recorder:                    recorder,
		enableRunningPhaseIncrement: options.EnableRunningPhaseIncrement,
		runningPhaseIncrement:       options.RunningPhaseIncrement,
		maxCost:                     options.MaxCost,
	}
}

// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads,verbs=get;list;watch
func (r *PreemptionCostReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)

	var job batchv1.Job
	if err := r.client.Get(ctx, req.NamespacedName, &job); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !job.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	wl, err := r.findWorkloadForJob(ctx, &job)
	if err != nil {
		return ctrl.Result{}, err
	}

	newCost := r.computeCost(&job, wl)
	newCostValue := strconv.FormatInt(int64(newCost), 10)
	currentCostValue := job.Annotations[constants.PreemptionCostAnnotationKey]
	if currentCostValue == newCostValue {
		return ctrl.Result{}, nil
	}

	patch := client.MergeFrom(job.DeepCopy())
	if job.Annotations == nil {
		job.Annotations = make(map[string]string)
	}
	job.Annotations[constants.PreemptionCostAnnotationKey] = newCostValue
	if err := r.client.Patch(ctx, &job, patch); err != nil {
		return ctrl.Result{}, err
	}

	log.Info("Updated preemption cost annotation",
		"job", klog.KObj(&job),
		"annotationKey", constants.PreemptionCostAnnotationKey,
		"annotationValue", newCostValue)
	r.recorder.Eventf(&job, corev1.EventTypeNormal, "PreemptionCostUpdated",
		"Updated %s=%s", constants.PreemptionCostAnnotationKey, newCostValue)

	return ctrl.Result{}, nil
}

func (r *PreemptionCostReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return builder.TypedControllerManagedBy[reconcile.Request](mgr).
		For(&batchv1.Job{}).
		Watches(
			&kueue.Workload{},
			handler.EnqueueRequestsFromMapFunc(r.mapWorkloadToJob),
		).
		Complete(r)
}

func (r *PreemptionCostReconciler) mapWorkloadToJob(_ context.Context, obj client.Object) []reconcile.Request {
	wl, ok := obj.(*kueue.Workload)
	if !ok {
		return nil
	}
	owner := metav1.GetControllerOf(wl)
	if owner == nil || owner.Kind != "Job" {
		return nil
	}
	return []reconcile.Request{{
		NamespacedName: types.NamespacedName{
			Namespace: wl.Namespace,
			Name:      owner.Name,
		},
	}}
}

func (r *PreemptionCostReconciler) findWorkloadForJob(ctx context.Context, job *batchv1.Job) (*kueue.Workload, error) {
	var workloads kueue.WorkloadList
	if err := r.client.List(ctx, &workloads, client.InNamespace(job.Namespace)); err != nil {
		return nil, err
	}

	for i := range workloads.Items {
		wl := &workloads.Items[i]
		owner := metav1.GetControllerOf(wl)
		if owner == nil {
			continue
		}
		if owner.Kind == "Job" && owner.Name == job.Name {
			return wl, nil
		}
	}

	return nil, nil
}

func (r *PreemptionCostReconciler) computeCost(job *batchv1.Job, wl *kueue.Workload) int32 {
	var cost int32
	if wl != nil && wl.Status.SchedulingStats != nil {
		for _, eviction := range wl.Status.SchedulingStats.Evictions {
			if eviction.Reason == kueue.WorkloadEvictedByPreemption {
				cost += eviction.Count
			}
		}
	}
	if r.enableRunningPhaseIncrement && job.Status.Active > 0 {
		cost += r.runningPhaseIncrement
	}
	if r.maxCost > 0 {
		cost = min(cost, r.maxCost)
	}
	cost = max(cost, 0)
	return cost
}
