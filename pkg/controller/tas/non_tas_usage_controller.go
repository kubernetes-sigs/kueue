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

package tas

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
)

func newNonTasUsageReconciler(k8sClient client.Client, cache *schdcache.Cache, roleTracker *roletracker.RoleTracker) *NonTasUsageReconciler {
	return &NonTasUsageReconciler{
		k8sClient:   k8sClient,
		cache:       cache,
		roleTracker: roleTracker,
	}
}

// NonTasUsageReconciler monitors pods to update
// the TAS cache with non-TAS usage.
type NonTasUsageReconciler struct {
	k8sClient   client.Client
	cache       *schdcache.Cache
	roleTracker *roletracker.RoleTracker
}

var _ reconcile.Reconciler = (*NonTasUsageReconciler)(nil)
var _ predicate.TypedPredicate[*corev1.Pod] = (*NonTasUsageReconciler)(nil)

//+kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch

func (r *NonTasUsageReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := klog.FromContext(ctx).WithValues("pod", req.NamespacedName)
	log.V(3).Info("Non-TAS usage cache reconciling")
	var pod corev1.Pod
	err := r.k8sClient.Get(ctx, req.NamespacedName, &pod)
	if err != nil {
		if client.IgnoreNotFound(err) != nil {
			return ctrl.Result{}, err
		}
		log.V(5).Info("Idempotently deleting not found pod")
		r.cache.TASCache().DeletePodByKey(req.NamespacedName)
		return ctrl.Result{}, nil
	}

	r.cache.TASCache().Update(&pod, log)
	return ctrl.Result{}, nil
}

func filterPod(pod *corev1.Pod) bool {
	if utiltas.IsTAS(pod) {
		return false
	} else if len(pod.Spec.NodeName) == 0 {
		// skip unscheduled pods as they don't use any capacity.
		return false
	}
	return true
}

func (r *NonTasUsageReconciler) Create(e event.TypedCreateEvent[*corev1.Pod]) bool {
	return filterPod(e.Object)
}

func (r *NonTasUsageReconciler) Update(e event.TypedUpdateEvent[*corev1.Pod]) bool {
	return filterPod(e.ObjectNew)
}

func (r *NonTasUsageReconciler) Delete(e event.TypedDeleteEvent[*corev1.Pod]) bool {
	return filterPod(e.Object)
}

func (r *NonTasUsageReconciler) Generic(event.TypedGenericEvent[*corev1.Pod]) bool {
	return false
}

func (r *NonTasUsageReconciler) SetupWithManager(mgr ctrl.Manager) (string, error) {
	return TASNonTasUsageController, ctrl.NewControllerManagedBy(mgr).
		Named(TASNonTasUsageController).
		WatchesRawSource(source.TypedKind(
			mgr.GetCache(),
			&corev1.Pod{},
			&handler.TypedEnqueueRequestForObject[*corev1.Pod]{},
			r,
		)).
		WithOptions(controller.Options{
			NeedLeaderElection:      ptr.To(false),
			MaxConcurrentReconciles: mgr.GetControllerOptions().GroupKindConcurrency[corev1.SchemeGroupVersion.WithKind("Pod").GroupKind().String()],
		}).
		WithLogConstructor(roletracker.NewLogConstructor(r.roleTracker, TASNonTasUsageController)).
		Complete(r)
}
