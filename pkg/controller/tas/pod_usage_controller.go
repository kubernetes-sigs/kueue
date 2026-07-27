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
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
)

func newPodUsageReconciler(k8sClient client.Client, cache *schdcache.Cache, roleTracker *roletracker.RoleTracker) *PodUsageReconciler {
	return &PodUsageReconciler{
		k8sClient:   k8sClient,
		cache:       cache,
		roleTracker: roleTracker,
	}
}

// PodUsageReconciler monitors all scheduled pods to update the TAS cache
// with non-TAS resource usage and to feed the scheduling simulator with
// pod state for feasibility checks.
type PodUsageReconciler struct {
	k8sClient   client.Client
	cache       *schdcache.Cache
	roleTracker *roletracker.RoleTracker
}

var _ reconcile.Reconciler = (*PodUsageReconciler)(nil)
var _ predicate.TypedPredicate[*corev1.Pod] = (*PodUsageReconciler)(nil)

//+kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch

func (r *PodUsageReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := klog.FromContext(ctx).WithValues("pod", req.NamespacedName)
	log.V(3).Info("Pod usage cache reconciling")
	var pod corev1.Pod
	err := r.k8sClient.Get(ctx, req.NamespacedName, &pod)
	if err != nil {
		if client.IgnoreNotFound(err) != nil {
			return ctrl.Result{}, err
		}
		log.V(5).Info("Idempotently deleting not found pod")
		r.cache.TASCache().DeleteNonTASUsageByKey(req.NamespacedName, log)
		r.cache.TASCache().UntrackPod(req.NamespacedName)
		return ctrl.Result{}, nil
	}

	if isScheduledAndRunning(&pod) {
		r.cache.TASCache().TrackPod(&pod)
	} else {
		r.cache.TASCache().UntrackPod(req.NamespacedName)
	}

	if belongsToNonTASCache(&pod) {
		r.cache.TASCache().UpdateNonTASUsage(&pod, log)
	} else {
		r.cache.TASCache().DeleteNonTASUsageByKey(req.NamespacedName, log)
	}
	return ctrl.Result{}, nil
}

func isScheduledAndRunning(pod *corev1.Pod) bool {
	return pod != nil && len(pod.Spec.NodeName) > 0 && !utilpod.IsTerminated(pod)
}

func belongsToNonTASCache(pod *corev1.Pod) bool {
	if pod == nil {
		return false
	}
	if utiltas.IsTAS(pod) {
		return false
	}
	if len(pod.Spec.NodeName) == 0 {
		return false
	}
	if utilpod.IsTerminated(pod) {
		return false
	}
	return true
}

func (r *PodUsageReconciler) Create(e event.TypedCreateEvent[*corev1.Pod]) bool {
	return isScheduledAndRunning(e.Object)
}

func (r *PodUsageReconciler) Update(e event.TypedUpdateEvent[*corev1.Pod]) bool {
	return isScheduledAndRunning(e.ObjectOld) != isScheduledAndRunning(e.ObjectNew) ||
		belongsToNonTASCache(e.ObjectOld) != belongsToNonTASCache(e.ObjectNew)
}

func (r *PodUsageReconciler) Delete(e event.TypedDeleteEvent[*corev1.Pod]) bool {
	return len(e.Object.Spec.NodeName) > 0
}

func (r *PodUsageReconciler) Generic(event.TypedGenericEvent[*corev1.Pod]) bool {
	return false
}

func (r *PodUsageReconciler) SetupWithManager(mgr ctrl.Manager) (string, error) {
	return TASPodUsageController, ctrl.NewControllerManagedBy(mgr).
		Named(TASPodUsageController).
		WatchesRawSource(source.TypedKind(
			mgr.GetCache(),
			&corev1.Pod{},
			&handler.TypedEnqueueRequestForObject[*corev1.Pod]{},
			r,
		)).
		WithOptions(controller.Options{
			NeedLeaderElection:      new(false),
			MaxConcurrentReconciles: mgr.GetControllerOptions().GroupKindConcurrency[corev1.SchemeGroupVersion.WithKind("Pod").GroupKind().String()],
		}).
		WithLogConstructor(roletracker.NewLogConstructor(r.roleTracker, TASPodUsageController)).
		Complete(r)
}
