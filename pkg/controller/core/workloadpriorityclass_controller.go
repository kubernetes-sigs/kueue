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

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
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
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
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

	var updateErrors []error

	// Update each workload's priority field
	for i := range workloads.Items {
		wl := &workloads.Items[i]
		wlLog := log.WithValues("workload", klog.KObj(wl))

		// Skip if priority is already up to date
		if wl.Spec.Priority != nil && *wl.Spec.Priority == wpc.Value {
			wlLog.V(3).Info("Workload priority already up to date")
			continue
		}

		wl.Spec.Priority = new(wpc.Value)

		if err := r.client.Update(ctx, wl); err != nil {
			if !apierrors.IsNotFound(err) {
				wlLog.Error(err, "Failed to update workload priority")
				updateErrors = append(updateErrors, err)
			}
			continue
		}

		wlLog.V(2).Info("Updated workload priority", "newPriority", wpc.Value)
	}
	return ctrl.Result{}, errors.Join(updateErrors...)
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

// workloadPriorityClassRequest returns the class the Workload references, when
// that reference is to a WorkloadPriorityClass. The name is the same key
// Reconcile lists by, so a request made here finds the Workload that produced it.
func workloadPriorityClassRequest(wl *kueue.Workload) (reconcile.Request, bool) {
	if !workload.IsWorkloadPriorityClass(wl) {
		return reconcile.Request{}, false
	}
	return reconcile.Request{
		NamespacedName: types.NamespacedName{Name: wl.Spec.PriorityClassRef.Name},
	}, true
}

// enqueueReferencedWorkloadPriorityClass enqueues the class a Workload now
// points at. Written as an event handler rather than a map function because
// that one enqueues the old object as well, and the class a Workload left has
// nothing to repair.
func enqueueReferencedWorkloadPriorityClass() handler.TypedEventHandler[*kueue.Workload, reconcile.Request] {
	enqueue := func(wl *kueue.Workload, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
		if req, isClass := workloadPriorityClassRequest(wl); isClass {
			q.Add(req)
		}
	}
	return handler.TypedFuncs[*kueue.Workload, reconcile.Request]{
		CreateFunc: func(_ context.Context, e event.TypedCreateEvent[*kueue.Workload], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			enqueue(e.Object, q)
		},
		UpdateFunc: func(_ context.Context, e event.TypedUpdateEvent[*kueue.Workload], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			enqueue(e.ObjectNew, q)
		},
	}
}

func workloadPriorityClassRefChanged() predicate.TypedPredicate[*kueue.Workload] {
	return predicate.TypedFuncs[*kueue.Workload]{
		CreateFunc: func(e event.TypedCreateEvent[*kueue.Workload]) bool {
			return workload.IsWorkloadPriorityClass(e.Object)
		},
		UpdateFunc: func(e event.TypedUpdateEvent[*kueue.Workload]) bool {
			if !workload.IsWorkloadPriorityClass(e.ObjectNew) {
				return false
			}
			// Compared as a whole reference, and against the reference rather
			// than the value: Reconcile writes spec.priority and leaves the
			// reference alone, so this does not fire on its own writes, and a
			// move between two groups sharing a name is still a move.
			old := e.ObjectOld.Spec.PriorityClassRef
			return old == nil || *old != *e.ObjectNew.Spec.PriorityClassRef
		},
		DeleteFunc:  func(event.TypedDeleteEvent[*kueue.Workload]) bool { return false },
		GenericFunc: func(event.TypedGenericEvent[*kueue.Workload]) bool { return false },
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *WorkloadPriorityClassReconciler) SetupWithManager(mgr ctrl.Manager, cfg *config.Configuration) error {
	return builder.TypedControllerManagedBy[reconcile.Request](mgr).
		Named("workloadpriorityclass_controller").
		WatchesRawSource(source.TypedKind(
			mgr.GetCache(),
			&kueue.WorkloadPriorityClass{},
			&handler.TypedEnqueueRequestForObject[*kueue.WorkloadPriorityClass]{},
			r,
		)).
		// A class update reconciled before a Workload starts referencing that
		// class finds nothing to update and is consumed. The Workload arrives
		// with whatever value its own lookup returned, so the reference is what
		// brings the class back for another pass.
		WatchesRawSource(source.TypedKind(
			mgr.GetCache(),
			&kueue.Workload{},
			enqueueReferencedWorkloadPriorityClass(),
			workloadPriorityClassRefChanged(),
		)).
		WithOptions(controller.Options{
			NeedLeaderElection:      new(false),
			MaxConcurrentReconciles: mgr.GetControllerOptions().GroupKindConcurrency[kueue.SchemeGroupVersion.WithKind("WorkloadPriorityClass").GroupKind().String()],
			LogConstructor:          roletracker.NewLogConstructor(r.roleTracker, "workloadpriorityclass-reconciler"),
		}).
		Complete(WithLeadingManager(mgr, r, &kueue.WorkloadPriorityClass{}, cfg))
}
