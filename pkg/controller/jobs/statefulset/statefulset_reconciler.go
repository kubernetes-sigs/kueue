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
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/pod"
	clientutil "sigs.k8s.io/kueue/pkg/util/client"
	"sigs.k8s.io/kueue/pkg/util/parallelize"
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

	// For now, handle only scaling down to zero.
	if ptr.Deref(sts.Spec.Replicas, 1) != 0 {
		return ctrl.Result{}, nil
	}

	err = r.fetchAndFinalizePods(ctx, req.Namespace, req.Name)
	if err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *Reconciler) fetchAndFinalizePods(ctx context.Context, namespace, statefulSetName string) error {
	podList := &corev1.PodList{}
	if err := r.client.List(ctx, podList, client.InNamespace(namespace), client.MatchingLabels{
		pod.GroupNameLabel: GetWorkloadName(statefulSetName),
	}); err != nil {
		return err
	}
	return r.finalizePods(ctx, podList.Items)
}

func (r *Reconciler) finalizePods(ctx context.Context, pods []corev1.Pod) error {
	log := ctrl.LoggerFrom(ctx)
	return parallelize.Until(ctx, len(pods), func(i int) error {
		p := &pods[i]
		err := clientutil.Patch(ctx, r.client, p, true, func() (bool, error) {
			removed := controllerutil.RemoveFinalizer(p, pod.PodFinalizer)
			if removed {
				log.V(3).Info("Finalizing pod in group", "pod", klog.KObj(p), "group", p.Labels[pod.GroupNameLabel])
			}
			return removed, nil
		})
		return client.IgnoreNotFound(err)
	})
}

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	ctrl.Log.V(3).Info("Setting up StatefulSet reconciler")
	return ctrl.NewControllerManagedBy(mgr).For(&appsv1.StatefulSet{}).Complete(r)
}

func NewReconciler(client client.Client, _ record.EventRecorder, _ ...jobframework.Option) jobframework.JobReconcilerInterface {
	return &Reconciler{client: client}
}
