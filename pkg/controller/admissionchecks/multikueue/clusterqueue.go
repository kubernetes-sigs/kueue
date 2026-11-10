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

package multikueue

import (
	"context"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
)

// cqReconciler reports worker cluster status metrics for manager ClusterQueues.
type cqReconciler struct {
	client            client.Client
	helper            *admissioncheck.MultiKueueStoreHelper
	roleTracker       *roletracker.RoleTracker
	eventsBatchPeriod time.Duration
}

var _ reconcile.Reconciler = (*cqReconciler)(nil)

func newCQReconciler(c client.Client, helper *admissioncheck.MultiKueueStoreHelper, roleTracker *roletracker.RoleTracker, eventsBatchPeriod time.Duration) *cqReconciler {
	return &cqReconciler{
		client:            c,
		helper:            helper,
		roleTracker:       roleTracker,
		eventsBatchPeriod: eventsBatchPeriod,
	}
}

func (r *cqReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	log := ctrl.LoggerFrom(ctx).WithValues("clusterQueue", req.Name)
	log.V(4).Info("Reconcile ClusterQueue event received (in the MultiKueue controller)")

	cq := &kueue.ClusterQueue{}
	if err := r.client.Get(ctx, req.NamespacedName, cq); err != nil {
		if apierrors.IsNotFound(err) {
			// The ClusterQueue is gone, so its worker status series must go too.
			metrics.ClearMultiKueueClusterQueueMetrics(kueue.ClusterQueueReference(req.Name))
		}
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}

	ac, hasAC, err := getMultiKueueAdmissionCheck(ctx, r.client, cq)
	if err != nil {
		return reconcile.Result{}, err
	}
	if !hasAC {
		log.V(4).Info("Not a MultiKueue manager ClusterQueue, skipping reconcile.")
		metrics.ClearMultiKueueClusterQueueMetrics(kueue.ClusterQueueReference(cq.Name))
		return reconcile.Result{}, nil
	}

	log.V(4).Info("Reconciling MultiKueue manager ClusterQueue")

	return reconcile.Result{}, r.reportWorkerClusterStatuses(ctx, cq, kueue.AdmissionCheckReference(ac.Name))
}

func (r *cqReconciler) queueEventsForAC(ctx context.Context, acName string, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	cqList := &kueue.ClusterQueueList{}
	if err := r.client.List(ctx, cqList, client.MatchingFields{ClusterQueueAdmissionChecksKey: acName}); err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "Failed to list ClusterQueues for AdmissionCheck", "admissionCheck", acName)
		return
	}

	for _, cq := range cqList.Items {
		q.AddAfter(reconcile.Request{Name: cq.Name}, r.eventsBatchPeriod)
	}
}

func (r *cqReconciler) queueEventsForMKConfig(ctx context.Context, configName string, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	acList := &kueue.AdmissionCheckList{}
	if err := r.client.List(ctx, acList, client.MatchingFields{AdmissionCheckUsingConfigKey: configName}); err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "Failed to list AdmissionChecks using MultiKueueConfig", "multiKueueConfig", configName)
		return
	}

	for _, ac := range acList.Items {
		r.queueEventsForAC(ctx, ac.Name, q)
	}
}

func (r *cqReconciler) queueEventsForMKCluster(ctx context.Context, clusterName string, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	configList := &kueue.MultiKueueConfigList{}
	if err := r.client.List(ctx, configList, client.MatchingFields{UsingMultiKueueClusters: clusterName}); err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "Failed to list MultiKueueConfigs using MultiKueueCluster", "multiKueueCluster", clusterName)
		return
	}

	for _, cfg := range configList.Items {
		r.queueEventsForMKConfig(ctx, cfg.Name, q)
	}
}

func (r *cqReconciler) setupWithManager(mgr ctrl.Manager) error {
	cqEventFilter := predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldCQ, okOld := e.ObjectOld.(*kueue.ClusterQueue)
			newCQ, okNew := e.ObjectNew.(*kueue.ClusterQueue)
			if !okOld || !okNew {
				return true
			}

			return oldCQ.Generation != newCQ.Generation
		},
	}

	return ctrl.NewControllerManagedBy(mgr).
		Named("multikueue_clusterqueue").
		For(&kueue.ClusterQueue{}, builder.WithPredicates(cqEventFilter)).
		Watches(&kueue.AdmissionCheck{}, &acHandler{reconciler: r}).
		Watches(&kueue.MultiKueueConfig{}, &cqConfigHandler{reconciler: r}).
		Watches(&kueue.MultiKueueCluster{}, &cqClusterHandler{reconciler: r}).
		WithOptions(controller.Options{
			LogConstructor: roletracker.NewLogConstructor(r.roleTracker, "multikueue-clusterqueue-reconciler"),
		}).
		Complete(r)
}

type acHandler struct {
	reconciler *cqReconciler
}

var _ handler.EventHandler = (*acHandler)(nil)

func (a *acHandler) Create(ctx context.Context, event event.CreateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	a.reconciler.queueEventsForAC(ctx, event.Object.GetName(), q)
}

func (a *acHandler) Update(ctx context.Context, event event.UpdateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	if event.ObjectNew.GetGeneration() != event.ObjectOld.GetGeneration() {
		a.reconciler.queueEventsForAC(ctx, event.ObjectNew.GetName(), q)
	}
}

func (a *acHandler) Delete(ctx context.Context, event event.DeleteEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	a.reconciler.queueEventsForAC(ctx, event.Object.GetName(), q)
}

func (a *acHandler) Generic(ctx context.Context, event event.GenericEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

type cqConfigHandler struct {
	reconciler *cqReconciler
}

var _ handler.EventHandler = (*cqConfigHandler)(nil)

func (c *cqConfigHandler) Create(ctx context.Context, event event.CreateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	c.reconciler.queueEventsForMKConfig(ctx, event.Object.GetName(), q)
}

func (c *cqConfigHandler) Update(ctx context.Context, event event.UpdateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	if event.ObjectOld.GetGeneration() != event.ObjectNew.GetGeneration() {
		c.reconciler.queueEventsForMKConfig(ctx, event.ObjectNew.GetName(), q)
	}
}

func (c *cqConfigHandler) Delete(ctx context.Context, event event.DeleteEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	c.reconciler.queueEventsForMKConfig(ctx, event.Object.GetName(), q)
}

func (c *cqConfigHandler) Generic(ctx context.Context, event event.GenericEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

type cqClusterHandler struct {
	reconciler *cqReconciler
}

var _ handler.EventHandler = (*cqClusterHandler)(nil)

func (c *cqClusterHandler) Create(ctx context.Context, event event.CreateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	c.reconciler.queueEventsForMKCluster(ctx, event.Object.GetName(), q)
}

func (c *cqClusterHandler) Update(ctx context.Context, event event.UpdateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	oldMKC, isOldMKC := event.ObjectOld.(*kueue.MultiKueueCluster)
	newMKC, isNewMKC := event.ObjectNew.(*kueue.MultiKueueCluster)
	if !isOldMKC || !isNewMKC {
		return
	}

	oldActive := apimeta.FindStatusCondition(oldMKC.Status.Conditions, kueue.MultiKueueClusterActive)
	newActive := apimeta.FindStatusCondition(newMKC.Status.Conditions, kueue.MultiKueueClusterActive)
	if !isConditionEqual(oldActive, newActive) {
		c.reconciler.queueEventsForMKCluster(ctx, newMKC.Name, q)
	}
}

func (c *cqClusterHandler) Delete(ctx context.Context, event event.DeleteEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	c.reconciler.queueEventsForMKCluster(ctx, event.Object.GetName(), q)
}

func (c *cqClusterHandler) Generic(ctx context.Context, event event.GenericEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}
