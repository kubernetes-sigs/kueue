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

	log := ctrl.LoggerFrom(ctx).WithValues("workloadPriorityClass", klog.KObj(&wpc))
	log.V(2).Info("Reconcile WorkloadPriorityClass")

	// List all workloads using this WorkloadPriorityClass
	var workloads kueue.WorkloadList
	if err := r.client.List(ctx, &workloads,
		client.MatchingFields{indexer.WorkloadPriorityClassKey: wpc.Name}); err != nil {
		log.Error(err, "Failed to list workloads for WorkloadPriorityClass")
		return ctrl.Result{}, err
	}
	if len(workloads.Items) == 0 {
		log.V(2).Info("No workloads using this WorkloadPriorityClass")
		return ctrl.Result{}, nil
	}

	// Update each workload's priority field, bounded and cancellable like the
	// rest of the tree, rather than serially in this single reconcile.
	err := parallelize.Until(ctx, len(workloads.Items), func(i int) error {
		wl := &workloads.Items[i]
		wlLog := log.WithValues("workload", klog.KObj(wl))

		if _, isMultiKueueRemote := wl.Labels[kueue.MultiKueueOriginLabel]; isMultiKueueRemote {
			wlLog.V(3).Info("Skipping MultiKueue remote workload")
			return nil
		}

		// Skip if priority is already up to date
		if wl.Spec.Priority != nil && *wl.Spec.Priority == wpc.Value {
			wlLog.V(3).Info("Workload priority already up to date")
			return nil
		}

		wl.Spec.Priority = new(wpc.Value)

		if err := r.client.Update(ctx, wl); err != nil {
			if apierrors.IsNotFound(err) {
				return nil
			}
			wlLog.Error(err, "Failed to update workload priority")
			return err
		}

		wlLog.V(2).Info("Updated workload priority", "newPriority", wpc.Value)
		return nil
	})
	return ctrl.Result{}, err
}

func (r *WorkloadPriorityClassReconciler) Create(e event.TypedCreateEvent[*kueue.WorkloadPriorityClass]) bool {
	log := r.logger().WithValues("workloadPriorityClass", klog.KObj(e.Object))
	log.V(2).Info("WorkloadPriorityClass create event")

	// Covering the case when the WorkloadPriorityClass was re-created with a different priority,
	// but the Workload is still referencing it.
	return true
}

func (r *WorkloadPriorityClassReconciler) Delete(e event.TypedDeleteEvent[*kueue.WorkloadPriorityClass]) bool {
	return false
}

func (r *WorkloadPriorityClassReconciler) Update(e event.TypedUpdateEvent[*kueue.WorkloadPriorityClass]) bool {
	log := r.logger().WithValues("workloadPriorityClass", klog.KObj(e.ObjectNew))
	log.V(2).Info("WorkloadPriorityClass update event")

	// Only reconcile if the priority value changed
	if e.ObjectOld.Value == e.ObjectNew.Value {
		log.V(3).Info("Priority value unchanged, skipping reconciliation")
		return false
	}

	log.V(2).Info("Priority value changed, triggering reconciliation", "oldValue", e.ObjectOld.Value, "newValue", e.ObjectNew.Value)
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
				"workload", klog.KObj(wl), "workloadPriorityClass", wl.Spec.PriorityClassRef.Name)
		}
		return
	}
	if wl.Spec.Priority != nil && *wl.Spec.Priority == wpc.Value {
		return
	}
	q.AddAfter(reconcile.Request{NamespacedName: client.ObjectKey{Name: wpc.Name}}, constants.UpdatesBatchPeriod)
}
