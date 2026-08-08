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
	"time"

	"github.com/go-logr/logr"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads,verbs=get;list;update

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

func workloadPriorityClassRefChanged() predicate.TypedPredicate[*kueue.Workload] {
	return predicate.TypedFuncs[*kueue.Workload]{
		CreateFunc: func(e event.TypedCreateEvent[*kueue.Workload]) bool {
			return ownsPriority(e.Object)
		},
		UpdateFunc: func(e event.TypedUpdateEvent[*kueue.Workload]) bool {
			if !ownsPriority(e.ObjectNew) {
				return false
			}
			// The reference can stay put while the answer stops being someone
			// else's. A Workload that loses the MultiKueue origin label carries a
			// value resolved on the manager, from a class this cluster's own is
			// under no obligation to agree with.
			if !ownsPriority(e.ObjectOld) {
				return true
			}
			// Compared as a whole reference, and against the reference rather
			// than the value: Reconcile writes spec.priority and leaves the
			// reference alone, so this does not fire on its own writes, and a
			// move between two groups sharing a name is still a move.
			return !apiequality.Semantic.DeepEqual(e.ObjectOld.Spec.PriorityClassRef, e.ObjectNew.Spec.PriorityClassRef)
		},
		DeleteFunc:  func(event.TypedDeleteEvent[*kueue.Workload]) bool { return false },
		GenericFunc: func(event.TypedGenericEvent[*kueue.Workload]) bool { return false },
	}
}

// classMovedRequeue is how long the reconcile waits before resolving again when
// the class changed under it. Short, since the value it just wrote is known to
// be the wrong one, and not zero, so a class being edited repeatedly does not
// spin.
const classMovedRequeue = time.Second

// ownsPriority reports whether this cluster decides the Workload's priority.
// A Workload MultiKueue created here carries the manager's resolution, and a
// class of the same name on this cluster is not the one it was resolved from.
func ownsPriority(wl *kueue.Workload) bool {
	_, isMultiKueueRemote := wl.Labels[kueue.MultiKueueOriginLabel]
	return !isMultiKueueRemote && workload.IsWorkloadPriorityClass(wl)
}

// WorkloadPriorityClassReferenceReconciler keeps one Workload's priority in step
// with the class it references. It is keyed on the Workload rather than the
// class, so a Workload arriving at a class does not cost a pass over every other
// Workload already using it.
type WorkloadPriorityClassReferenceReconciler struct {
	logName string
	client  client.Client
	// Both objects are read straight from the API server, since neither cached
	// answer is ordered against what this reconcile is deciding from. The class
	// read can be one this replica took before the class moved, and the pass that
	// would have repaired it has already decided there was nothing to do. The
	// workload read can be one from before this reconcile's own write, which on a
	// requeue still holds the value the class has since moved to, so the pass
	// that came back to repair it reports nothing left to do instead.
	apiReader   client.Reader
	roleTracker *roletracker.RoleTracker
}

var _ reconcile.Reconciler = (*WorkloadPriorityClassReferenceReconciler)(nil)

func NewWorkloadPriorityClassReferenceReconciler(
	client client.Client,
	apiReader client.Reader,
	roleTracker *roletracker.RoleTracker,
) *WorkloadPriorityClassReferenceReconciler {
	return &WorkloadPriorityClassReferenceReconciler{
		logName:     "workloadpriorityclassreference-reconciler",
		client:      client,
		apiReader:   apiReader,
		roleTracker: roleTracker,
	}
}

// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads,verbs=get;list;watch;update

func (r *WorkloadPriorityClassReferenceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var wl kueue.Workload
	if err := r.apiReader.Get(ctx, req.NamespacedName, &wl); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	// Checked again here, not only in the predicate: the label can arrive
	// after the request was queued.
	if !ownsPriority(&wl) {
		return ctrl.Result{}, nil
	}

	log := ctrl.LoggerFrom(ctx).WithValues("workload", klog.KObj(&wl))
	log.V(2).Info("Reconcile Workload priority class reference")

	var wpc kueue.WorkloadPriorityClass
	if err := r.apiReader.Get(ctx, client.ObjectKey{Name: wl.Spec.PriorityClassRef.Name}, &wpc); err != nil {
		// A class that does not exist yet sweeps the workloads referencing it
		// when it is created.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if wl.Spec.Priority != nil && *wl.Spec.Priority == wpc.Value {
		log.V(3).Info("Workload priority already up to date")
		return ctrl.Result{}, nil
	}

	wl.Spec.Priority = new(wpc.Value)
	if err := r.client.Update(ctx, &wl); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	// The class can move between the read above and this write, and the pass that
	// change starts can find the workload already holding the new value and leave
	// it alone, which leaves nothing to stop this write landing on top of a
	// correct one. Reading the class again closes the window this write opened,
	// and the requeue resolves from the API server rather than from a cache that
	// has not caught up with the write above.
	var after kueue.WorkloadPriorityClass
	if err := r.apiReader.Get(ctx, client.ObjectKey{Name: wl.Spec.PriorityClassRef.Name}, &after); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if after.Value != wpc.Value {
		log.V(2).Info("Class moved while the workload was being written", "wrote", wpc.Value, "now", after.Value)
		return ctrl.Result{RequeueAfter: classMovedRequeue}, nil
	}
	log.V(2).Info("Updated workload priority", "newPriority", wpc.Value)
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *WorkloadPriorityClassReferenceReconciler) SetupWithManager(mgr ctrl.Manager, cfg *config.Configuration) error {
	return builder.TypedControllerManagedBy[reconcile.Request](mgr).
		Named("workloadpriorityclassreference_controller").
		WatchesRawSource(source.TypedKind(
			mgr.GetCache(),
			&kueue.Workload{},
			&handler.TypedEnqueueRequestForObject[*kueue.Workload]{},
			workloadPriorityClassRefChanged(),
		)).
		WithOptions(controller.Options{
			NeedLeaderElection:      new(false),
			MaxConcurrentReconciles: mgr.GetControllerOptions().GroupKindConcurrency[kueue.SchemeGroupVersion.WithKind("Workload").GroupKind().String()],
			LogConstructor:          roletracker.NewLogConstructor(r.roleTracker, "workloadpriorityclassreference-reconciler"),
		}).
		Complete(WithLeadingManager(mgr, r, &kueue.Workload{}, cfg))
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
		WithOptions(controller.Options{
			NeedLeaderElection:      new(false),
			MaxConcurrentReconciles: mgr.GetControllerOptions().GroupKindConcurrency[kueue.SchemeGroupVersion.WithKind("WorkloadPriorityClass").GroupKind().String()],
			LogConstructor:          roletracker.NewLogConstructor(r.roleTracker, "workloadpriorityclass-reconciler"),
		}).
		Complete(WithLeadingManager(mgr, r, &kueue.WorkloadPriorityClass{}, cfg))
}
