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

	appsv1 "k8s.io/api/apps/v1"
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
	"sigs.k8s.io/kueue/pkg/util/parallelize"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
)

// +kubebuilder:rbac:groups="apps",resources=statefulsets,verbs=get;list;watch

var (
	_ jobframework.JobReconcilerInterface = (*Reconciler)(nil)
)

type Reconciler struct {
	client client.Client
}

func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	sts := &appsv1.StatefulSet{}
	err := r.client.Get(ctx, req.NamespacedName, sts)
	if err != nil {
		// we'll ignore not-found errors, since there is nothing to do.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	log := ctrl.LoggerFrom(ctx).WithValues("statefulset", klog.KObj(sts))
	ctx = ctrl.LoggerInto(ctx, log)
	log.V(2).Info("Reconciling StatefulSet")

	err = r.fetchAndFinalizePods(ctx, sts)
	if err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *Reconciler) fetchAndFinalizePods(ctx context.Context, sts *appsv1.StatefulSet) error {
	podList := &corev1.PodList{}
	if err := r.client.List(ctx, podList, client.InNamespace(sts.Namespace), client.MatchingLabels{
		podcontroller.GroupNameLabel: GetWorkloadName(sts.Name),
	}); err != nil {
		return err
	}
	return r.finalizePods(ctx, sts, podList.Items)
}

func (r *Reconciler) finalizePods(ctx context.Context, sts *appsv1.StatefulSet, pods []corev1.Pod) error {
	log := ctrl.LoggerFrom(ctx)
	return parallelize.Until(ctx, len(pods), func(i int) error {
		pod := &pods[i]
		return client.IgnoreNotFound(clientutil.Patch(ctx, r.client, pod, true, func() (bool, error) {
			if finalizePod(sts, pod) {
				log.V(3).Info(
					"Finalizing pod in group",
					"pod", klog.KObj(pod),
					"group", pod.Labels[podcontroller.GroupNameLabel],
				)
				return true, nil
			}
			return false, nil
		}))
	})
}

func finalizePod(sts *appsv1.StatefulSet, pod *corev1.Pod) bool {
	var updated bool

	if shouldUngate(sts, pod) && utilpod.Ungate(pod, podcontroller.SchedulingGateName) {
		updated = true
	}

	if shouldFinalize(sts, pod) && controllerutil.RemoveFinalizer(pod, podcontroller.PodFinalizer) {
		updated = true
	}

	return updated
}

func shouldUngate(sts *appsv1.StatefulSet, pod *corev1.Pod) bool {
	return sts.Status.CurrentRevision != sts.Status.UpdateRevision &&
		sts.Status.CurrentRevision == pod.Labels[appsv1.ControllerRevisionHashLabelKey]
}

func shouldFinalize(sts *appsv1.StatefulSet, pod *corev1.Pod) bool {
	return shouldUngate(sts, pod) || pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed
}

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	ctrl.Log.V(3).Info("Setting up StatefulSet reconciler")
	return ctrl.NewControllerManagedBy(mgr).
		For(&appsv1.StatefulSet{}).
		WithEventFilter(r).
		Complete(r)
}

func NewReconciler(client client.Client, _ record.EventRecorder, _ ...jobframework.Option) jobframework.JobReconcilerInterface {
	return &Reconciler{client: client}
}

var _ predicate.Predicate = (*Reconciler)(nil)

func (r *Reconciler) Generic(event.GenericEvent) bool {
	return false
}

func (r *Reconciler) Create(e event.CreateEvent) bool {
	return r.handle(e.Object)
}

func (r *Reconciler) Update(e event.UpdateEvent) bool {
	return r.handle(e.ObjectNew)
}

func (r *Reconciler) Delete(event.DeleteEvent) bool {
	return false
}

func (r *Reconciler) handle(obj client.Object) bool {
	sts, isSts := obj.(*appsv1.StatefulSet)
	if !isSts {
		return false
	}
	// Handle only statefulset managed by kueue.
	return jobframework.IsManagedByKueue(sts)
}
