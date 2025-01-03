/*
Copyright 2024 The Kubernetes Authors.

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

package statefulset

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	podcontroller "sigs.k8s.io/kueue/pkg/controller/jobs/pod"
	clientutil "sigs.k8s.io/kueue/pkg/util/client"
)

type PodReconciler struct {
	client client.Client
}

func NewPodReconciler(client client.Client, _ record.EventRecorder, _ ...jobframework.Option) jobframework.JobReconcilerInterface {
	return &PodReconciler{client: client}
}

var _ jobframework.JobReconcilerInterface = (*PodReconciler)(nil)

func (r *PodReconciler) SetupWithManager(mgr ctrl.Manager) error {
	ctrl.Log.V(3).Info("Setting up Pod reconciler for StatefulSet")
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Pod{}).
		Named("statefulset-pod").
		WithEventFilter(r).
		Complete(r)
}

func (r *PodReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	pod := &corev1.Pod{}
	err := r.client.Get(ctx, req.NamespacedName, pod)
	if err != nil {
		// we'll ignore not-found errors, since there is nothing to do.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	log := ctrl.LoggerFrom(ctx).WithValues("pod", klog.KObj(pod))
	ctx = ctrl.LoggerInto(ctx, log)
	log.V(2).Info("Reconciling StatefulSet Pod")

	if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
		err = client.IgnoreNotFound(clientutil.Patch(ctx, r.client, pod, true, func() (bool, error) {
			removed := controllerutil.RemoveFinalizer(pod, podcontroller.PodFinalizer)
			if removed {
				log.V(3).Info(
					"Finalizing statefulset pod in group",
					"pod", klog.KObj(pod),
					"group", pod.Labels[podcontroller.GroupNameLabel],
				)
			}
			return removed, nil
		}))
	}

	return ctrl.Result{}, err
}

var _ predicate.Predicate = (*PodReconciler)(nil)

func (r *PodReconciler) Generic(event.GenericEvent) bool {
	return false
}

func (r *PodReconciler) Create(e event.CreateEvent) bool {
	return r.handle(e.Object)
}

func (r *PodReconciler) Update(e event.UpdateEvent) bool {
	return r.handle(e.ObjectNew)
}

func (r *PodReconciler) Delete(event.DeleteEvent) bool {
	return false
}

func (r *PodReconciler) handle(obj client.Object) bool {
	pod, isPod := obj.(*corev1.Pod)
	if !isPod {
		return false
	}
	// Handle only statefulset pods.
	return pod.Annotations[podcontroller.SuspendedByParentAnnotation] == FrameworkName
}
