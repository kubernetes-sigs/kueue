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
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/features"
)

// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=dynamicresourceallocationconfigs,verbs=get;list;watch

// DynamicResourceAllocationConfigReconciler keeps the cache's DeviceClass→logical-resource
// mapping in sync with the singleton DynamicResourceAllocationConfig object.
// It is only registered when the DynamicResourceAllocation feature gate is enabled.
type DynamicResourceAllocationConfigReconciler struct {
	client client.Client
	cache  *cache.Cache
	log    logr.Logger
}

func NewDRAConfigReconciler(client client.Client, c *cache.Cache) *DynamicResourceAllocationConfigReconciler {
	return &DynamicResourceAllocationConfigReconciler{
		client: client,
		cache:  c,
		log:    ctrl.Log.WithName("dra-config-reconciler"),
	}
}

func (r *DynamicResourceAllocationConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	if !features.Enabled(features.DynamicResourceAllocation) {
		return ctrl.Result{}, nil
	}

	var cfg kueuealpha.DynamicResourceAllocationConfig
	if err := r.client.Get(ctx, req.NamespacedName, &cfg); err != nil {
		// TODO: this should never happen. But if a user deletes the singleton DRA by mistake, we should
		//  have a way to gracefully handle it. May be adding a finalizer to the singleton DRAConfig object?
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	r.log.V(2).Info("Reconciling DynamicResourceAllocationConfig", "name", req.NamespacedName)
	// we do not expect a lot of reconciles triggered here, therefore directly updating the cache should be okay
	r.cache.AddOrUpdateDynamicResourceAllocationConfig(r.log, &cfg)
	return ctrl.Result{}, nil
}

func (r *DynamicResourceAllocationConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("dynamicresourceallocationconfig_controller").
		For(&kueuealpha.DynamicResourceAllocationConfig{}).
		WithEventFilter(predicate.Funcs{
			// Only singleton named "default" matters (cluster-scoped resource).
			CreateFunc: func(e event.CreateEvent) bool {
				obj := e.Object.(*kueuealpha.DynamicResourceAllocationConfig)
				return obj.Name == "default"
			},
			UpdateFunc: func(e event.UpdateEvent) bool {
				obj := e.ObjectNew.(*kueuealpha.DynamicResourceAllocationConfig)
				return obj.Name == "default"
			},
			DeleteFunc:  func(event.DeleteEvent) bool { return false },
			GenericFunc: func(event.GenericEvent) bool { return false },
		}).
		Complete(r)
}
