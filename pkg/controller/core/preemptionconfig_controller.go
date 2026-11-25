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

	"k8s.io/apimachinery/pkg/util/sets"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
)

// PreemptionConfigReconciler retries the inadmissible Workloads of the
// ClusterQueues that reference a PreemptionConfig when that PreemptionConfig
// is created, deleted, or its rules change.
type PreemptionConfigReconciler struct {
	qManager    *qcache.Manager
	cache       *schdcache.Cache
	roleTracker *roletracker.RoleTracker
}

var _ reconcile.Reconciler = (*PreemptionConfigReconciler)(nil)
var _ predicate.TypedPredicate[*kueuealpha.PreemptionConfig] = (*PreemptionConfigReconciler)(nil)

func NewPreemptionConfigReconciler(qManager *qcache.Manager, cache *schdcache.Cache, roleTracker *roletracker.RoleTracker) *PreemptionConfigReconciler {
	return &PreemptionConfigReconciler{
		qManager:    qManager,
		cache:       cache,
		roleTracker: roleTracker,
	}
}

func (r *PreemptionConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	log.V(3).Info("Reconcile PreemptionConfig", "preemptionConfig", req.Name)
	if cqNames := r.cache.ClusterQueuesUsingPreemptionConfig(req.Name); len(cqNames) > 0 {
		qcache.NotifyRetryInadmissible(r.qManager, sets.New(cqNames...))
	}
	return ctrl.Result{}, nil
}

func (r *PreemptionConfigReconciler) Create(event.TypedCreateEvent[*kueuealpha.PreemptionConfig]) bool {
	return true
}

// Update only lets through spec changes. PreemptionConfig has no status
// subresource, so the generation changes only when the rules change.
func (r *PreemptionConfigReconciler) Update(e event.TypedUpdateEvent[*kueuealpha.PreemptionConfig]) bool {
	return e.ObjectOld.Generation != e.ObjectNew.Generation
}

func (r *PreemptionConfigReconciler) Delete(event.TypedDeleteEvent[*kueuealpha.PreemptionConfig]) bool {
	return true
}

func (r *PreemptionConfigReconciler) Generic(event.TypedGenericEvent[*kueuealpha.PreemptionConfig]) bool {
	return false
}

func (r *PreemptionConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return builder.TypedControllerManagedBy[reconcile.Request](mgr).
		Named("preemptionconfig_controller").
		WatchesRawSource(source.TypedKind(
			mgr.GetCache(),
			&kueuealpha.PreemptionConfig{},
			&handler.TypedEnqueueRequestForObject[*kueuealpha.PreemptionConfig]{},
			r,
		)).
		WithOptions(controller.Options{
			NeedLeaderElection: new(false),
			LogConstructor:     roletracker.NewLogConstructor(r.roleTracker, "preemptionconfig-reconciler"),
		}).
		Complete(r)
}
