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

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
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
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	"sigs.k8s.io/kueue/pkg/workload"
)

// workloadPriorityClassRefReconciler gives a Workload that starts to reference a
// WorkloadPriorityClass the class value. The class reconciler acts on class
// events only, so it misses a Workload that references the class after it ran.
type workloadPriorityClassRefReconciler struct {
	client      client.Client
	roleTracker *roletracker.RoleTracker
}

var _ reconcile.Reconciler = (*workloadPriorityClassRefReconciler)(nil)
var _ predicate.TypedPredicate[*kueue.Workload] = (*workloadPriorityClassRefReconciler)(nil)

func newWorkloadPriorityClassRefReconciler(client client.Client, roleTracker *roletracker.RoleTracker) *workloadPriorityClassRefReconciler {
	return &workloadPriorityClassRefReconciler{
		client:      client,
		roleTracker: roleTracker,
	}
}

// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloadpriorityclasses,verbs=get;list;watch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads,verbs=get;list;watch;patch

func (r *workloadPriorityClassRefReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var wl kueue.Workload
	if err := r.client.Get(ctx, req.NamespacedName, &wl); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !workload.IsWorkloadPriorityClass(&wl) {
		return ctrl.Result{}, nil
	}
	if _, isMultiKueueRemote := wl.Labels[kueue.MultiKueueOriginLabel]; isMultiKueueRemote {
		return ctrl.Result{}, nil
	}

	key := types.NamespacedName{Name: wl.Spec.PriorityClassRef.Name}
	var wpc kueue.WorkloadPriorityClass
	if err := r.client.Get(ctx, key, &wpc); err != nil {
		// A class created later is reconciled with the Workloads that reference it.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	// The class can change while the Workload is written, and the class reconcile
	// skips a Workload that still holds the new value. So the class is read again
	// after each write, and the write is repeated only if the class changed.
	for !ptr.Equal(wl.Spec.Priority, &wpc.Value) {
		written := wpc.Value
		patch := client.MergeFrom(wl.DeepCopy())
		wl.Spec.Priority = new(written)
		if err := r.client.Patch(ctx, &wl, patch); err != nil {
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
		ctrl.LoggerFrom(ctx).V(3).Info("Updated workload priority", "workloadPriorityClass", klog.KObj(&wpc), "newPriority", written)
		if err := r.client.Get(ctx, key, &wpc); err != nil || wpc.Value == written {
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
	}
	return ctrl.Result{}, nil
}

func (r *workloadPriorityClassRefReconciler) Create(e event.TypedCreateEvent[*kueue.Workload]) bool {
	return workload.IsWorkloadPriorityClass(e.Object)
}

// Update reacts only to a change of class, so that a priority edited by hand
// under the same class is kept. A relist reports a Workload recreated under
// the same name as an update, hence the UID check.
func (r *workloadPriorityClassRefReconciler) Update(e event.TypedUpdateEvent[*kueue.Workload]) bool {
	return workload.IsWorkloadPriorityClass(e.ObjectNew) &&
		(e.ObjectOld.UID != e.ObjectNew.UID ||
			!ptr.Equal(e.ObjectOld.Spec.PriorityClassRef, e.ObjectNew.Spec.PriorityClassRef))
}

func (r *workloadPriorityClassRefReconciler) Delete(event.TypedDeleteEvent[*kueue.Workload]) bool {
	return false
}

func (r *workloadPriorityClassRefReconciler) Generic(event.TypedGenericEvent[*kueue.Workload]) bool {
	return false
}

func (r *workloadPriorityClassRefReconciler) SetupWithManager(mgr ctrl.Manager, cfg *config.Configuration) error {
	return builder.TypedControllerManagedBy[reconcile.Request](mgr).
		Named("workloadpriorityclassref_controller").
		WatchesRawSource(source.TypedKind(
			mgr.GetCache(),
			&kueue.Workload{},
			&handler.TypedEnqueueRequestForObject[*kueue.Workload]{},
			r,
		)).
		WithOptions(controller.Options{
			NeedLeaderElection:      new(false),
			MaxConcurrentReconciles: mgr.GetControllerOptions().GroupKindConcurrency[kueue.SchemeGroupVersion.WithKind("Workload").GroupKind().String()],
			LogConstructor:          roletracker.NewLogConstructor(r.roleTracker, "workloadpriorityclassref-reconciler"),
		}).
		Complete(WithLeadingManager(mgr, r, &kueue.Workload{}, cfg))
}
