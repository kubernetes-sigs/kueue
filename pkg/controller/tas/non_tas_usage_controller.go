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
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
)

func newNonTasUsageReconciler(client client.Client, cache *schdcache.Cache, qcache *qcache.Manager) *NonTasUsageReconciler {
	return &NonTasUsageReconciler{
		Client: client,
		Cache:  cache,
		QCache: qcache,
	}
}

// NonTasUsageReconciler monitors pods to update
// the TAS cache with non-TAS usage.
type NonTasUsageReconciler struct {
	client.Client
	Scheme      *runtime.Scheme
	Cache       *schdcache.Cache
	QCache      *qcache.Manager
	roleTracker *roletracker.RoleTracker
}

var _ reconcile.Reconciler = (*NonTasUsageReconciler)(nil)
var _ predicate.TypedPredicate[*corev1.Pod] = (*NonTasUsageReconciler)(nil)

//+kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch

func (r *NonTasUsageReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := klog.FromContext(ctx)
	log.V(3).Info("Non-TAS usage cache reconciling", "pod", req.NamespacedName)
	var pod corev1.Pod
	err := r.Get(ctx, req.NamespacedName, &pod)
	if err != nil {
		if client.IgnoreNotFound(err) != nil {
			return ctrl.Result{}, err
		}
		log.V(5).Info("Idempotently deleting not found pod", "pod", req.NamespacedName)
		r.Cache.TASCache().DeletePodByKey(req.NamespacedName)
		r.QCache.QueueAllInadmissible(ctx)
		return ctrl.Result{}, nil
	}

	r.Cache.TASCache().AddOrUpdatePod(pod, log)
	r.QCache.QueueAllInadmissible(ctx)
	return ctrl.Result{}, nil
}

func (r *NonTasUsageReconciler) Create(e event.TypedCreateEvent[*corev1.Pod]) bool {
	return true
}

func (r *NonTasUsageReconciler) Update(e event.TypedUpdateEvent[*corev1.Pod]) bool {
	return true
}

func (r *NonTasUsageReconciler) Delete(e event.TypedDeleteEvent[*corev1.Pod]) bool {
	return true
}

func (r *NonTasUsageReconciler) Generic(event.TypedGenericEvent[*corev1.Pod]) bool {
	return false
}

func (r *NonTasUsageReconciler) SetupWithManager(mgr ctrl.Manager) (string, error) {
	return TASNonTasUsageController, ctrl.NewControllerManagedBy(mgr).
		Named(TASNonTasUsageController).
		For(&corev1.Pod{}).
		WithOptions(controller.Options{
			NeedLeaderElection:      ptr.To(false),
			MaxConcurrentReconciles: mgr.GetControllerOptions().GroupKindConcurrency[kueue.GroupVersion.WithKind("Pod").GroupKind().String()],
		}).
		WithLogConstructor(roletracker.NewLogConstructor(r.roleTracker, TASNonTasUsageController)).
		Complete(r)
}
