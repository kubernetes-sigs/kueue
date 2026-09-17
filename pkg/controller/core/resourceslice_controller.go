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

	corev1 "k8s.io/api/core/v1"
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
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
)

type ResourceSliceReconciler struct {
	qManager       *qcache.Manager
	cache          *schdcache.Cache
	drivers        sets.Set[string]
	quotaResources sets.Set[corev1.ResourceName]
	roleTracker    *roletracker.RoleTracker
}

var _ reconcile.Reconciler = (*ResourceSliceReconciler)(nil)

func NewResourceSliceReconciler(qManager *qcache.Manager, cache *schdcache.Cache, cfg *configapi.Configuration, roleTracker *roletracker.RoleTracker) *ResourceSliceReconciler {
	drivers := sets.New[string]()
	quotaResources := sets.New[corev1.ResourceName]()

	if cfg.Resources != nil {
		for _, m := range cfg.Resources.DeviceClassMappings {
			for _, s := range m.Sources {
				if driver := sourceDriver(s); driver != "" {
					drivers.Insert(driver)
					quotaResources.Insert(m.Name)
				}
			}
		}
	}
	return &ResourceSliceReconciler{
		qManager:       qManager,
		cache:          cache,
		drivers:        drivers,
		quotaResources: quotaResources,
		roleTracker:    roleTracker,
	}
}

func sourceDriver(s configapi.DeviceClassSourceConfig) string {
	if s.Counter != nil {
		return s.Counter.Driver
	}
	if s.Capacity != nil {
		return s.Capacity.Driver
	}
	return ""
}

func (r *ResourceSliceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	log.V(3).Info("Reconcile ResourceSlice", "resourceSlice", req.NamespacedName)
	if cqNames := r.cache.ClusterQueuesForResources(r.quotaResources); cqNames.Len() > 0 {
		qcache.NotifyRetryInadmissible(r.qManager, cqNames)
	}
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
