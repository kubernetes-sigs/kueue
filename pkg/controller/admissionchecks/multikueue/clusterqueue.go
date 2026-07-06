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
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
)

type cqReconciler struct {
	client            client.Client
	helper            *admissioncheck.MultiKueueStoreHelper
	clusters          *clustersReconciler
	roleTracker       *roletracker.RoleTracker
	eventsBatchPeriod time.Duration
}

var _ reconcile.Reconciler = (*cqReconciler)(nil)

func newCQReconciler(c client.Client, helper *admissioncheck.MultiKueueStoreHelper, clusters *clustersReconciler, roleTracker *roletracker.RoleTracker, eventsBatchPeriod time.Duration) *cqReconciler {
	return &cqReconciler{
		client:            c,
		helper:            helper,
		clusters:          clusters,
		roleTracker:       roleTracker,
		eventsBatchPeriod: eventsBatchPeriod,
	}
}

func (r *cqReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	log := ctrl.LoggerFrom(ctx).WithValues("clusterQueue", req.Name)
	log.V(3).Info("Reconcile ClusterQueue event received (in the MultiKueue controller)")

	cq := &kueue.ClusterQueue{}
	if err := r.client.Get(ctx, req.NamespacedName, cq); err != nil {
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}

	ac, hasAC, err := r.getMultiKueueAdmissionCheck(ctx, cq)
	if err != nil {
		return reconcile.Result{}, err
	}
	if !hasAC {
		log.V(3).Info("Not a MultiKueue manager ClusterQueue, skipping reconcile.")
		err := r.removeQuotaAutomationCondition(ctx, cq)
		return reconcile.Result{}, err
	}

	log.V(2).Info("Reconciling MultiKueue manager ClusterQueue")

	cfg, err := r.helper.ConfigFromRef(ctx, ac.Spec.Parameters)
	if err != nil {
		if apierrors.IsNotFound(err) {
			err = r.updateQuotaAutomationCondition(ctx, cq, metav1.ConditionFalse, "UnsupportedConfiguration", "The referenced MultiKueueConfig was not found.")
		}
		return reconcile.Result{}, err
	}

	if ptr.Deref(cfg.Spec.QuotaManagement, kueue.QuotaManagementManual) == kueue.QuotaManagementManual {
		err = r.updateQuotaAutomationCondition(ctx, cq, metav1.ConditionFalse, "NotRequested", "MultiKueue manager quota automation has not been requested.")
		return reconcile.Result{}, err
	}

	if len(cq.Spec.ResourceGroups) != 1 || len(cq.Spec.ResourceGroups[0].Flavors) != 1 {
		err = r.updateQuotaAutomationCondition(
			ctx,
			cq,
			metav1.ConditionFalse,
			"UnsupportedConfiguration",
			"Quota automation requires that the manager-side ClusterQueue has exactly one ResourceFlavor",
		)
		return reconcile.Result{}, err
	}
	singleFlavor := &cq.Spec.ResourceGroups[0].Flavors[0]

	aggregatedQuotas, err := r.aggregateWorkerQuotas(ctx, cq, cfg)
	if err != nil {
		return reconcile.Result{}, err
	}

	covered := sets.New(cq.Spec.ResourceGroups[0].CoveredResources...)
	remoteResources := sets.KeySet(aggregatedQuotas)
	missingResources := remoteResources.Difference(covered)
	if missingResources.Len() > 0 {
		errMsg := fmt.Sprintf("manager-side coveredResources is missing resources configured on workers: %v", sets.List(missingResources))
		err = r.updateQuotaAutomationCondition(ctx, cq, metav1.ConditionFalse, "UnsupportedConfiguration", errMsg)
		return reconcile.Result{}, err
	}

	var newResources []kueue.ResourceQuota
	for _, resName := range cq.Spec.ResourceGroups[0].CoveredResources {
		newResources = append(newResources, kueue.ResourceQuota{
			Name:         resName,
			NominalQuota: aggregatedQuotas[resName],
		})
	}

	if !equality.Semantic.DeepEqual(singleFlavor.Resources, newResources) {
		singleFlavor.Resources = newResources
		if err := r.client.Update(ctx, cq); err != nil {
			return reconcile.Result{}, fmt.Errorf("updating ClusterQueue nominal quotas: %w", err)
		}
	}

	err = r.updateQuotaAutomationCondition(ctx, cq, metav1.ConditionTrue, "QuotaAutomated", "ClusterQueue quota is automatically managed based on MultiKueue workers.")
	return reconcile.Result{}, err
}

func (r *cqReconciler) getMultiKueueAdmissionCheck(ctx context.Context, cq *kueue.ClusterQueue) (*kueue.AdmissionCheck, bool, error) {
	if cq.Spec.AdmissionChecksStrategy == nil {
		return nil, false, nil
	}

	acList := &kueue.AdmissionCheckList{}
	if err := r.client.List(ctx, acList, client.MatchingFields{AdmissionCheckControllerNameKey: kueue.MultiKueueControllerName}); err != nil {
		return nil, false, fmt.Errorf("listing local AdmissionChecks: %w", err)
	}

	cqACNames := sets.New[string]()
	for _, rule := range cq.Spec.AdmissionChecksStrategy.AdmissionChecks {
		cqACNames.Insert(string(rule.Name))
	}

	for _, ac := range acList.Items {
		if cqACNames.Has(ac.Name) {
			return &ac, true, nil
		}
	}

	return nil, false, nil
}

func (r *cqReconciler) aggregateWorkerQuotas(ctx context.Context, cq *kueue.ClusterQueue, cfg *kueue.MultiKueueConfig) (map[corev1.ResourceName]resource.Quantity, error) {
	localLQs := &kueue.LocalQueueList{}
	if err := r.client.List(ctx, localLQs, client.MatchingFields{indexer.QueueClusterQueueKey: cq.Name}); err != nil {
		return nil, fmt.Errorf("listing local LocalQueues: %w", err)
	}

	lqKeys := sets.New[types.NamespacedName]()
	for _, llq := range localLQs.Items {
		lqKeys.Insert(types.NamespacedName{Namespace: llq.Namespace, Name: llq.Name})
	}

	total := make(map[corev1.ResourceName]resource.Quantity)

	for _, workerName := range cfg.Spec.Clusters {
		rc, found := r.clusters.controllerFor(workerName)
		if !found {
			ctrl.LoggerFrom(ctx).V(3).Info("Worker cluster client not found, skipping it in quota aggregation", "workerCluster", workerName)
			continue
		}
		if !rc.connected.Load() {
			ctrl.LoggerFrom(ctx).V(3).Info("Worker cluster client not connected, skipping it in quota aggregation", "workerCluster", workerName)
			continue
		}
		remoteLQList := &kueue.LocalQueueList{}
		// This List is cached (by the selectivelyCachingClient).
		if err := rc.getClient().List(ctx, remoteLQList); err != nil {
			return nil, err
		}
		remoteCQKeys := sets.New[types.NamespacedName]()
		for _, rlq := range remoteLQList.Items {
			if lqKeys.Has(types.NamespacedName{Namespace: rlq.Namespace, Name: rlq.Name}) {
				remoteCQKeys.Insert(types.NamespacedName{Name: string(rlq.Spec.ClusterQueue)})
			}
		}

		remoteCQList := &kueue.ClusterQueueList{}
		// This List is cached (by the selectivelyCachingClient).
		if err := rc.getClient().List(ctx, remoteCQList); err != nil {
			return nil, err
		}

		for _, rcq := range remoteCQList.Items {
			key := types.NamespacedName{Name: rcq.Name}
			if !remoteCQKeys.Has(key) {
				continue
			}
			for _, rg := range rcq.Spec.ResourceGroups {
				for _, flavor := range rg.Flavors {
					for _, res := range flavor.Resources {
						curr := total[res.Name]
						curr.Add(res.NominalQuota)
						total[res.Name] = curr
					}
				}
			}
		}
	}

	return total, nil
}

func (r *cqReconciler) removeQuotaAutomationCondition(ctx context.Context, cq *kueue.ClusterQueue) error {
	if !apimeta.RemoveStatusCondition(&cq.Status.Conditions, kueue.MultiKueueManagerQuotaAutomation) {
		return nil
	}
	if err := r.client.Status().Update(ctx, cq); err != nil {
		return fmt.Errorf("removing ClusterQueue condition status: %w", err)
	}
	return nil
}

func (r *cqReconciler) updateQuotaAutomationCondition(ctx context.Context, cq *kueue.ClusterQueue, status metav1.ConditionStatus, reason, message string) error {
	newCondition := metav1.Condition{
		Type:               kueue.MultiKueueManagerQuotaAutomation,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: cq.Generation,
	}

	oldCondition := apimeta.FindStatusCondition(cq.Status.Conditions, kueue.MultiKueueManagerQuotaAutomation)
	if cmpConditionState(oldCondition, &newCondition) {
		return nil
	}

	apimeta.SetStatusCondition(&cq.Status.Conditions, newCondition)
	if err := r.client.Status().Update(ctx, cq); err != nil {
		return fmt.Errorf("updating ClusterQueue condition status: %w", err)
	}
	return nil
}

func (r *cqReconciler) queueEventsForAC(ctx context.Context, acName string, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	cqList := &kueue.ClusterQueueList{}
	if err := r.client.List(ctx, cqList, client.MatchingFields{ClusterQueueAdmissionChecksKey: acName}); err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "Failed to list ClusterQueues for AdmissionCheck", "admissionCheck", acName)
		return
	}

	for _, cq := range cqList.Items {
		q.AddAfter(reconcile.Request{NamespacedName: types.NamespacedName{Name: cq.Name}}, r.eventsBatchPeriod)
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

	remoteHandler := handler.TypedFuncs[kueue.ClusterQueueReference, reconcile.Request]{
		GenericFunc: func(_ context.Context, e event.TypedGenericEvent[kueue.ClusterQueueReference], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			q.AddAfter(reconcile.Request{NamespacedName: types.NamespacedName{Name: string(e.Object)}}, r.eventsBatchPeriod)
		},
	}

	return ctrl.NewControllerManagedBy(mgr).
		Named("multikueue_clusterqueue").
		For(&kueue.ClusterQueue{}, builder.WithPredicates(cqEventFilter)).
		Watches(&kueue.LocalQueue{}, &lqHandler{reconciler: r}).
		Watches(&kueue.AdmissionCheck{}, &acHandler{reconciler: r}).
		Watches(&kueue.MultiKueueConfig{}, &cqConfigHandler{reconciler: r}).
		Watches(&kueue.MultiKueueCluster{}, &cqClusterHandler{reconciler: r}).
		WatchesRawSource(source.Channel(r.clusters.cqUpdateCh, remoteHandler)).
		WithOptions(controller.Options{
			LogConstructor: roletracker.NewLogConstructor(r.roleTracker, "multikueue-clusterqueue"),
		}).
		Complete(r)
}

type lqHandler struct {
	reconciler *cqReconciler
}

var _ handler.EventHandler = (*lqHandler)(nil)

func (l *lqHandler) Create(ctx context.Context, event event.CreateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	if lq, ok := event.Object.(*kueue.LocalQueue); ok {
		q.AddAfter(reconcile.Request{NamespacedName: types.NamespacedName{Name: string(lq.Spec.ClusterQueue)}}, l.reconciler.eventsBatchPeriod)
	}
}

func (l *lqHandler) Update(ctx context.Context, event event.UpdateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	// No action needed, Spec.ClusterQueue is immutable on LocalQueues.
}

func (l *lqHandler) Delete(ctx context.Context, event event.DeleteEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	if lq, ok := event.Object.(*kueue.LocalQueue); ok {
		q.AddAfter(reconcile.Request{NamespacedName: types.NamespacedName{Name: string(lq.Spec.ClusterQueue)}}, l.reconciler.eventsBatchPeriod)
	}
}

func (l *lqHandler) Generic(ctx context.Context, event event.GenericEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
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
	if !cmpConditionState(oldActive, newActive) {
		c.reconciler.queueEventsForMKCluster(ctx, newMKC.Name, q)
	}
}

func (c *cqClusterHandler) Delete(ctx context.Context, event event.DeleteEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	c.reconciler.queueEventsForMKCluster(ctx, event.Object.GetName(), q)
}

func (c *cqClusterHandler) Generic(ctx context.Context, event event.GenericEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}
