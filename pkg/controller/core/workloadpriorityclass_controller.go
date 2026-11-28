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
	"context"
	"errors"
	"fmt"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	utillogging "sigs.k8s.io/kueue/pkg/util/logging"
	"sigs.k8s.io/kueue/pkg/util/parallelize"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	"sigs.k8s.io/kueue/pkg/workload"
)

// WorkloadPriorityClassReconciler reconciles a WorkloadPriorityClass object
type WorkloadPriorityClassReconciler struct {
	logName     string
	client      client.Client
	roleTracker *roletracker.RoleTracker
}

var _ reconcile.Reconciler = (*WorkloadPriorityClassReconciler)(nil)
var _ predicate.TypedPredicate[*kueue.WorkloadPriorityClass] = (*WorkloadPriorityClassReconciler)(nil)

func NewWorkloadPriorityClassReconciler(
	client client.Client,
	roleTracker *roletracker.RoleTracker,
) *WorkloadPriorityClassReconciler {
	return &WorkloadPriorityClassReconciler{
		logName:     "workloadpriorityclass-reconciler",
		client:      client,
		roleTracker: roleTracker,
	}
}

func (r *WorkloadPriorityClassReconciler) logger() logr.Logger {
	return roletracker.WithReplicaRole(ctrl.Log.WithName(r.logName), r.roleTracker)
}

// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloadpriorityclasses,verbs=get;list;watch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads,verbs=get;list;watch;update

func (r *WorkloadPriorityClassReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var wpc kueue.WorkloadPriorityClass
	if err := r.client.Get(ctx, req.NamespacedName, &wpc); err != nil {
		// we'll ignore not-found errors, since there is nothing to do.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	log := ctrl.LoggerFrom(ctx)
	log.V(3).Info("Reconcile WorkloadPriorityClass")

	// List all workloads using this WorkloadPriorityClass
	var workloads kueue.WorkloadList
	if err := r.client.List(ctx, &workloads,
		client.MatchingFields{indexer.WorkloadPriorityClassKey: wpc.Name}); err != nil {
		return ctrl.Result{}, fmt.Errorf("listing workloads for WorkloadPriorityClass: %w", err)
	}
	if len(workloads.Items) == 0 {
		log.V(5).Info("No workloads using this WorkloadPriorityClass")
		return ctrl.Result{}, nil
	}

	// Update each workload's priority field, bounded and cancellable like the
	// rest of the tree, rather than serially in this single reconcile.
	errs := make([]error, len(workloads.Items))
	_ = parallelize.Until(ctx, len(workloads.Items), func(i int) error {
		wl := &workloads.Items[i]

		if _, isMultiKueueRemote := wl.Labels[kueue.MultiKueueOriginLabel]; isMultiKueueRemote {
			if logV := log.V(5); logV.Enabled() {
				logV.Info("Skipping MultiKueue remote workload", "workload", klog.KObj(wl))
			}
			return nil
		}

		// Skip if priority is already up to date
		if wl.Spec.Priority != nil && *wl.Spec.Priority == wpc.Value {
			if logV := log.V(5); logV.Enabled() {
				logV.Info("Workload priority already up to date", "workload", klog.KObj(wl))
			}
			return nil
		}

		wl.Spec.Priority = new(wpc.Value)

		if err := r.client.Update(ctx, wl); err != nil {
			if apierrors.IsNotFound(err) {
				return nil
			}
			err = fmt.Errorf("updating workload %s priority: %w", klog.KObj(wl), err)
			errs[i] = utillogging.ErrorWithValues(err, "workload", klog.KObj(wl))
			return nil
		}

		log.V(3).Info("Updated workload priority", "workload", klog.KObj(wl), "priority", wpc.Value)
		return nil
	})
	return ctrl.Result{}, errors.Join(errs...)
}

func (r *WorkloadPriorityClassReconciler) Create(e event.TypedCreateEvent[*kueue.WorkloadPriorityClass]) bool {
	if logV := r.logger().V(3); logV.Enabled() {
		logV.Info("WorkloadPriorityClass create event", "workloadPriorityClass", klog.KObj(e.Object))
	}

	// Events from the informer's initial list don't indicate a change of the priority value,
	// so reconciling them would overwrite the priorities set directly on the Workloads
	// every time the controller starts.
	if e.IsInInitialList {
		log.V(3).Info("Event from the initial list, skipping reconciliation")
		return false
	}

	// Covering the case when the WorkloadPriorityClass was re-created with a different priority,
	// but the Workload is still referencing it.
	return true
}

func (r *WorkloadPriorityClassReconciler) Delete(e event.TypedDeleteEvent[*kueue.WorkloadPriorityClass]) bool {
	return false
}

func (r *WorkloadPriorityClassReconciler) Update(e event.TypedUpdateEvent[*kueue.WorkloadPriorityClass]) bool {
	if logV := r.logger().V(3); logV.Enabled() {
		logV.Info("WorkloadPriorityClass update event", "workloadPriorityClass", klog.KObj(e.ObjectNew))
	}

	// Only reconcile if the priority value changed
	if e.ObjectOld.Value == e.ObjectNew.Value {
		if logV := r.logger().V(5); logV.Enabled() {
			logV.Info("Priority value unchanged, skipping reconciliation", "workloadPriorityClass", klog.KObj(e.ObjectNew))
		}
		return false
	}

	if logV := r.logger().V(4); logV.Enabled() {
		logV.Info("Priority value changed, triggering reconciliation",
			"workloadPriorityClass", klog.KObj(e.ObjectNew),
			"previousValue", e.ObjectOld.Value,
			"value", e.ObjectNew.Value)
	}
	return true
}

func (r *WorkloadPriorityClassReconciler) Generic(e event.TypedGenericEvent[*kueue.WorkloadPriorityClass]) bool {
	return false
}

// SetupWithManager sets up the controller with the Manager.
func (r *WorkloadPriorityClassReconciler) SetupWithManager(mgr ctrl.Manager, cfg *config.Configuration) error {
	b := builder.TypedControllerManagedBy[reconcile.Request](mgr).
		Named("workloadpriorityclass_controller").
		WatchesRawSource(source.TypedKind(
			mgr.GetCache(),
			&kueue.WorkloadPriorityClass{},
			&handler.TypedEnqueueRequestForObject[*kueue.WorkloadPriorityClass]{},
			r,
		))
	if features.Enabled(features.WorkloadPriorityClassWatchesWorkloads) {
		b = b.WatchesRawSource(source.TypedKind(
			mgr.GetCache(),
			&kueue.Workload{},
			&wpcWorkloadHandler{r: r},
		))
	}
	return b.
		WithOptions(controller.Options{
			NeedLeaderElection:      new(false),
			MaxConcurrentReconciles: mgr.GetControllerOptions().GroupKindConcurrency[kueue.SchemeGroupVersion.WithKind("WorkloadPriorityClass").GroupKind().String()],
			LogConstructor:          roletracker.NewLogConstructor(r.roleTracker, "workloadpriorityclass-reconciler"),
		}).
		Complete(WithLeadingManager(mgr, r, &kueue.WorkloadPriorityClass{}, cfg))
}

// wpcWorkloadHandler requeues the WorkloadPriorityClass of a Workload whose
// priority differs from the class value. Class events alone miss a Workload that
// the sweep skipped on a stale cache, or that got another value after the sweep.
type wpcWorkloadHandler struct {
	r *WorkloadPriorityClassReconciler
}

var _ handler.TypedEventHandler[*kueue.Workload, reconcile.Request] = (*wpcWorkloadHandler)(nil)

func (h *wpcWorkloadHandler) Create(ctx context.Context, e event.TypedCreateEvent[*kueue.Workload], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	h.queueClassIfDiverged(ctx, e.Object, q)
}

func (h *wpcWorkloadHandler) Update(ctx context.Context, e event.TypedUpdateEvent[*kueue.Workload], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	h.queueClassIfDiverged(ctx, e.ObjectNew, q)
}

func (h *wpcWorkloadHandler) Delete(context.Context, event.TypedDeleteEvent[*kueue.Workload], workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

func (h *wpcWorkloadHandler) Generic(context.Context, event.TypedGenericEvent[*kueue.Workload], workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

func (h *wpcWorkloadHandler) queueClassIfDiverged(ctx context.Context, wl *kueue.Workload, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	if !workload.IsWorkloadPriorityClass(wl) {
		return
	}
	if _, isMultiKueueRemote := wl.Labels[kueue.MultiKueueOriginLabel]; isMultiKueueRemote {
		return
	}
	var wpc kueue.WorkloadPriorityClass
	if err := h.r.client.Get(ctx, client.ObjectKey{Name: wl.Spec.PriorityClassRef.Name}, &wpc); err != nil {
		if !apierrors.IsNotFound(err) {
			h.r.logger().Error(err, "Failed to get the WorkloadPriorityClass of a workload",
				"workload", klog.KObj(wl), "workloadPriorityClass", klog.KRef("", wl.Spec.PriorityClassRef.Name))
		}
		return
	}
	if wl.Spec.Priority != nil && *wl.Spec.Priority == wpc.Value {
		return
	}
	q.AddAfter(reconcile.Request{Name: wpc.Name}, constants.UpdatesBatchPeriod)
}
