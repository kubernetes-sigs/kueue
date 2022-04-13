/*
Copyright 2022 The Kubernetes Authors.

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
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/event"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	"sigs.k8s.io/kueue/pkg/cache"
)

// ResourceFlavorReconciler reconciles a ResourceFlavor object
type ResourceFlavorReconciler struct {
	log   logr.Logger
	cache *cache.Cache
}

func NewResourceFlavorReconciler(cache *cache.Cache) *ResourceFlavorReconciler {
	return &ResourceFlavorReconciler{
		log:   ctrl.Log.WithName("resourceflavor-reconciler"),
		cache: cache,
	}
}

//+kubebuilder:rbac:groups=kueue.x-k8s.io,resources=resourceflavors,verbs=get;list;watch

func (r *ResourceFlavorReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	// Nothing to do here.
	return ctrl.Result{}, nil
}

func (r *ResourceFlavorReconciler) Create(e event.CreateEvent) bool {
	flv, match := e.Object.(*kueue.ResourceFlavor)
	if !match {
		return false
	}
	log := r.log.WithValues("resourceFlavor", klog.KObj(flv))
	log.V(2).Info("ResourceFlavor create event")
	r.cache.AddOrUpdateResourceFlavor(flv.DeepCopy())
	return false
}

func (r *ResourceFlavorReconciler) Delete(e event.DeleteEvent) bool {
	flv, match := e.Object.(*kueue.ResourceFlavor)
	if !match {
		return false
	}
	log := r.log.WithValues("resourceFlavor", klog.KObj(flv))
	log.V(2).Info("ResourceFlavor delete event")
	r.cache.DeleteResourceFlavor(flv)
	return false
}

func (r *ResourceFlavorReconciler) Update(e event.UpdateEvent) bool {
	flv, match := e.ObjectNew.(*kueue.ResourceFlavor)
	if !match {
		return false
	}
	log := r.log.WithValues("resourceFlavor", klog.KObj(flv))
	log.V(2).Info("ResourceFlavor update event")
	r.cache.AddOrUpdateResourceFlavor(flv.DeepCopy())
	return false
}

func (r *ResourceFlavorReconciler) Generic(e event.GenericEvent) bool {
	r.log.V(3).Info("Ignore generic event", "obj", klog.KObj(e.Object), "kind", e.Object.GetObjectKind().GroupVersionKind())
	return false
}

// SetupWithManager sets up the controller with the Manager.
func (r *ResourceFlavorReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kueue.ResourceFlavor{}).
		WithEventFilter(r).
		Complete(r)
}
