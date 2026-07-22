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

	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
)

type ResourceSliceReconciler struct {
	qManager    *qcache.Manager
	drivers     sets.Set[string]
	roleTracker *roletracker.RoleTracker
}

var _ reconcile.Reconciler = (*ResourceSliceReconciler)(nil)

func NewResourceSliceReconciler(qManager *qcache.Manager, cfg *configapi.Configuration, roleTracker *roletracker.RoleTracker) *ResourceSliceReconciler {
	drivers := sets.New[string]()
	if cfg.Resources != nil {
		for _, m := range cfg.Resources.DeviceClassMappings {
			for _, s := range m.Sources {
				if s.Counter != nil {
					drivers.Insert(s.Counter.Driver)
				}
				if s.Capacity != nil {
					drivers.Insert(s.Capacity.Driver)
				}
			}
		}
	}
	return &ResourceSliceReconciler{
		qManager:    qManager,
		drivers:     drivers,
		roleTracker: roleTracker,
	}
}

func (r *ResourceSliceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	log.V(3).Info("Reconcile ResourceSlice", "resourceSlice", req.NamespacedName)
	cqNames := sets.New(r.qManager.GetClusterQueueNames()...)
	qcache.NotifyRetryInadmissible(r.qManager, cqNames)
	return ctrl.Result{}, nil
}

func (r *ResourceSliceReconciler) Create(e event.TypedCreateEvent[*resourcev1.ResourceSlice]) bool {
	return r.drivers.Has(e.Object.Spec.Driver)
}

func (r *ResourceSliceReconciler) Update(e event.TypedUpdateEvent[*resourcev1.ResourceSlice]) bool {
	return r.drivers.Has(e.ObjectNew.Spec.Driver)
}

func (r *ResourceSliceReconciler) Delete(e event.TypedDeleteEvent[*resourcev1.ResourceSlice]) bool {
	return r.drivers.Has(e.Object.Spec.Driver)
}

func (r *ResourceSliceReconciler) Generic(event.TypedGenericEvent[*resourcev1.ResourceSlice]) bool {
	return false
}

func SetupResourceSliceIndexer(ctx context.Context, indexer client.FieldIndexer) error {
	return indexer.IndexField(ctx, &resourcev1.ResourceSlice{},
		"spec.driver", func(obj client.Object) []string {
			slice := obj.(*resourcev1.ResourceSlice)
			return []string{slice.Spec.Driver}
		})
}

// +kubebuilder:rbac:groups=resource.k8s.io,resources=resourceslices,verbs=get;list;watch

func (r *ResourceSliceReconciler) SetupWithManager(mgr ctrl.Manager, cfg *configapi.Configuration) error {
	return builder.TypedControllerManagedBy[reconcile.Request](mgr).
		Named("resourceslice_controller").
		WatchesRawSource(source.TypedKind(
			mgr.GetCache(),
			&resourcev1.ResourceSlice{},
			&handler.TypedEnqueueRequestForObject[*resourcev1.ResourceSlice]{},
			r,
		)).
		WithOptions(controller.Options{
			NeedLeaderElection: new(false),
			LogConstructor:     roletracker.NewLogConstructor(r.roleTracker, "resourceslice-reconciler"),
		}).
		Complete(r)
}
