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
	"errors"
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
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
	"sigs.k8s.io/kueue/pkg/util/api"
	clientutil "sigs.k8s.io/kueue/pkg/util/client"
	"sigs.k8s.io/kueue/pkg/util/resourcegroups"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
)

var capacityProviderConfigGK = kueuealpha.SchemeGroupVersion.WithKind("MultiKueueCapacityProviderConfig").GroupKind()

// misconfiguredError is a configuration problem that the CapacityProvider reports
// with the Misconfigured reason, instead of returning it as a reconcile error.
type misconfiguredError struct {
	msg string
}

func (e *misconfiguredError) Error() string {
	return e.msg
}

func misconfigured(format string, args ...any) error {
	return &misconfiguredError{msg: fmt.Sprintf(format, args...)}
}

// cpReconciler reconciles the CapacityProviders whose controllerName is kueue.x-k8s.io/multikueue.
// It resolves the manager ClusterQueue through the MultiKueueCapacityProviderConfig referenced in
// spec.parameters, sums the quotas of its related worker ClusterQueues across connected workers,
// and publishes the result to status.capacity with the CapacitySynchronized condition.
type cpReconciler struct {
	client            client.Client
	helper            *admissioncheck.MultiKueueStoreHelper
	clusters          *clustersReconciler
	roleTracker       *roletracker.RoleTracker
	eventsBatchPeriod time.Duration
}

var _ reconcile.Reconciler = (*cpReconciler)(nil)

func newCPReconciler(c client.Client, helper *admissioncheck.MultiKueueStoreHelper, clusters *clustersReconciler, roleTracker *roletracker.RoleTracker, eventsBatchPeriod time.Duration) *cpReconciler {
	return &cpReconciler{
		client:            c,
		helper:            helper,
		clusters:          clusters,
		roleTracker:       roleTracker,
		eventsBatchPeriod: eventsBatchPeriod,
	}
}

func (r *cpReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	log := ctrl.LoggerFrom(ctx)

	cp := &kueuealpha.CapacityProvider{}
	if err := r.client.Get(ctx, req.NamespacedName, cp); err != nil {
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}
	if !cp.DeletionTimestamp.IsZero() {
		return reconcile.Result{}, nil
	}
	// The event filter checks controllerName too, but a CapacityProvider can be deleted
	// and created again for another controller after it was queued.
	if !isMultiKueueCapacityProvider(cp) {
		log.V(3).Info("Not a MultiKueue CapacityProvider, skipping reconcile")
		return reconcile.Result{}, nil
	}

	log.V(2).Info("Reconcile MultiKueue CapacityProvider")

	cq, mkConfig, err := r.resolveManagerClusterQueue(ctx, cp)
	if err != nil {
		if misconfiguredErr, ok := errors.AsType[*misconfiguredError](err); ok {
			return reconcile.Result{}, r.updateStatus(ctx, cp, nil, metav1.ConditionFalse, kueuealpha.CapacityProviderReasonMisconfigured, misconfiguredErr.msg)
		}
		return reconcile.Result{}, err
	}

	agg, err := r.aggregateWorkerQuotas(ctx, cq, mkConfig)
	if err != nil {
		return reconcile.Result{}, err
	}
	if agg.connectedWorkers == 0 {
		msg := "No MultiKueue worker clusters are connected"
		if len(agg.disconnectedWorkers) > 0 {
			msg = fmt.Sprintf("No MultiKueue worker clusters are connected; disconnected worker clusters: %v", agg.disconnectedWorkers)
		}
		return reconcile.Result{}, r.updateStatus(ctx, cp, nil, metav1.ConditionFalse, kueuealpha.CapacityProviderReasonSourceUnavailable, msg)
	}

	covered := sets.New(cq.Spec.ResourceGroups[0].CoveredResources...)
	remoteResources := sets.KeySet(agg.quotas)
	missingResources := remoteResources.Difference(covered)
	if missingResources.Len() > 0 {
		msg := fmt.Sprintf("manager-side coveredResources is missing resources configured on workers: %v", sets.List(missingResources))
		return reconcile.Result{}, r.updateStatus(ctx, cp, nil, metav1.ConditionFalse, kueuealpha.CapacityProviderReasonMisconfigured, msg)
	}

	resources := make(corev1.ResourceList, len(cq.Spec.ResourceGroups[0].CoveredResources))
	for _, resName := range cq.Spec.ResourceGroups[0].CoveredResources {
		if qty, ok := agg.quotas[resName]; ok {
			resources[resName] = qty
		} else {
			resources[resName] = *resource.NewQuantity(0, resource.DecimalSI)
		}
	}
	newCapacity := &kueuealpha.CapacityProviderNormalizedCapacity{
		Flavors: []kueuealpha.CapacityProviderNormalizedCapacityFlavor{
			{
				Name:      cp.Spec.OrchestratedFlavors[0].Name,
				Resources: resources,
			},
		},
	}

	msg := "Capacity synchronized from MultiKueue worker clusters"
	if len(agg.disconnectedWorkers) > 0 {
		msg = fmt.Sprintf("Capacity synchronized from connected MultiKueue worker clusters; disconnected worker clusters: %v", agg.disconnectedWorkers)
	}
	return reconcile.Result{}, r.updateStatus(ctx, cp, newCapacity, metav1.ConditionTrue, kueuealpha.CapacityProviderReasonSynchronized, msg)
}

// resolveManagerClusterQueue follows spec.parameters to the manager ClusterQueue and its MultiKueueConfig,
// and checks that they meet the Alpha constraints. It returns a *misconfiguredError when they do not.
func (r *cpReconciler) resolveManagerClusterQueue(ctx context.Context, cp *kueuealpha.CapacityProvider) (*kueue.ClusterQueue, *kueue.MultiKueueConfig, error) {
	if !isCapacityProviderConfigRef(cp.Spec.Parameters) {
		return nil, nil, misconfigured("spec.parameters must reference a %s in the %s API group", capacityProviderConfigGK.Kind, capacityProviderConfigGK.Group)
	}
	if len(cp.Spec.OrchestratedFlavors) != 1 {
		return nil, nil, misconfigured("spec.orchestratedFlavors must have exactly one ResourceFlavor, found %d", len(cp.Spec.OrchestratedFlavors))
	}

	providerConfig := &kueuealpha.MultiKueueCapacityProviderConfig{}
	if err := r.client.Get(ctx, types.NamespacedName{Name: cp.Spec.Parameters.Name}, providerConfig); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil, misconfigured("%s %q not found", capacityProviderConfigGK.Kind, cp.Spec.Parameters.Name)
		}
		return nil, nil, fmt.Errorf("getting %s: %w", capacityProviderConfigGK.Kind, err)
	}

	cqName := providerConfig.Spec.ManagerSubtreeRootRef.Name
	cq := &kueue.ClusterQueue{}
	if err := r.client.Get(ctx, types.NamespacedName{Name: cqName}, cq); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil, misconfigured("ClusterQueue %q not found", cqName)
		}
		return nil, nil, fmt.Errorf("getting manager ClusterQueue: %w", err)
	}

	ac, hasAC, err := r.getMultiKueueAdmissionCheck(ctx, cq)
	if err != nil {
		return nil, nil, err
	}
	if !hasAC {
		return nil, nil, misconfigured("ClusterQueue %q has no MultiKueue AdmissionCheck", cq.Name)
	}

	mkConfig, err := r.helper.ConfigFromRef(ctx, ac.Spec.Parameters)
	switch {
	case errors.Is(err, admissioncheck.ErrNilParametersRef), errors.Is(err, admissioncheck.ErrBadParametersRef):
		return nil, nil, misconfigured("AdmissionCheck %q has invalid parameters: %v", ac.Name, err)
	case apierrors.IsNotFound(err):
		return nil, nil, misconfigured("MultiKueueConfig %q not found", ac.Spec.Parameters.Name)
	case err != nil:
		return nil, nil, fmt.Errorf("getting MultiKueueConfig: %w", err)
	}

	if len(cq.Spec.ResourceGroups) != 1 || len(cq.Spec.ResourceGroups[0].Flavors) != 1 {
		return nil, nil, misconfigured("ClusterQueue %q must have exactly one ResourceFlavor", cq.Name)
	}
	cqFlavor := string(cq.Spec.ResourceGroups[0].Flavors[0].Name)
	if providerFlavor := string(cp.Spec.OrchestratedFlavors[0].Name); cqFlavor != providerFlavor {
		return nil, nil, misconfigured("ClusterQueue %q ResourceFlavor %q does not match the ResourceFlavor %q in spec.orchestratedFlavors", cq.Name, cqFlavor, providerFlavor)
	}

	return cq, mkConfig, nil
}

// getMultiKueueAdmissionCheck returns the first MultiKueue AdmissionCheck of the ClusterQueue.
func (r *cpReconciler) getMultiKueueAdmissionCheck(ctx context.Context, cq *kueue.ClusterQueue) (*kueue.AdmissionCheck, bool, error) {
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

type aggregatedWorkerQuotas struct {
	quotas              map[corev1.ResourceName]resource.Quantity
	connectedWorkers    int
	disconnectedWorkers []string
}

// aggregateWorkerQuotas sums the nominal quotas of all related worker ClusterQueues
// across connected clusters in the MultiKueueConfig, using each worker ClusterQueue's
// status.effectiveQuotas when set and spec.resourceGroups otherwise.
//
// Quotas are summed across all worker ResourceFlavors because manager flavor
// assignment is not propagated to workers.
func (r *cpReconciler) aggregateWorkerQuotas(ctx context.Context, cq *kueue.ClusterQueue, cfg *kueue.MultiKueueConfig) (aggregatedWorkerQuotas, error) {
	localLQs := &kueue.LocalQueueList{}
	if err := r.client.List(ctx, localLQs, client.MatchingFields{indexer.QueueClusterQueueKey: cq.Name}); err != nil {
		return aggregatedWorkerQuotas{}, fmt.Errorf("listing local LocalQueues: %w", err)
	}

	lqKeys := sets.New[types.NamespacedName]()
	for _, llq := range localLQs.Items {
		lqKeys.Insert(types.NamespacedName{Namespace: llq.Namespace, Name: llq.Name})
	}

	total := make(map[corev1.ResourceName]resource.Quantity)
	disconnected := sets.New[string]()
	connectedWorkers := 0

	for _, workerName := range cfg.Spec.Clusters {
		// The clusters reconciler removes the remote client of a deleted MultiKueueCluster
		// asynchronously, so check the object itself. Otherwise the reconcile queued by the
		// MultiKueueCluster delete event could still sum the quotas of the deleted worker.
		mkc := &kueue.MultiKueueCluster{}
		if err := r.client.Get(ctx, types.NamespacedName{Name: workerName}, mkc); err != nil {
			if !apierrors.IsNotFound(err) {
				return aggregatedWorkerQuotas{}, fmt.Errorf("getting MultiKueueCluster %q: %w", workerName, err)
			}
			ctrl.LoggerFrom(ctx).V(3).Info("Worker MultiKueueCluster not found, skipping it in quota aggregation", "workerCluster", workerName)
			disconnected.Insert(workerName)
			continue
		}
		if !mkc.DeletionTimestamp.IsZero() {
			ctrl.LoggerFrom(ctx).V(3).Info("Worker MultiKueueCluster is being deleted, skipping it in quota aggregation", "workerCluster", workerName)
			disconnected.Insert(workerName)
			continue
		}

		var rc *remoteClient
		var found bool
		if r.clusters != nil {
			rc, found = r.clusters.controllerFor(workerName)
		}
		if !found {
			ctrl.LoggerFrom(ctx).V(3).Info("Worker cluster client not found, skipping it in quota aggregation", "workerCluster", workerName)
			disconnected.Insert(workerName)
			continue
		}
		if !rc.connState.isConnected() {
			ctrl.LoggerFrom(ctx).V(3).Info("Worker cluster client not connected, skipping it in quota aggregation", "workerCluster", workerName)
			disconnected.Insert(workerName)
			continue
		}
		remoteCl := rc.getClient()
		if remoteCl == nil {
			ctrl.LoggerFrom(ctx).V(3).Info("Worker cluster client is nil, skipping it in quota aggregation", "workerCluster", workerName)
			disconnected.Insert(workerName)
			continue
		}
		connectedWorkers++

		remoteLQList := &kueue.LocalQueueList{}
		// This List is cached (by the selectivelyCachingClient).
		if err := remoteCl.List(ctx, remoteLQList); err != nil {
			return aggregatedWorkerQuotas{}, fmt.Errorf("listing LocalQueues on worker %q: %w", workerName, err)
		}
		remoteCQKeys := sets.New[types.NamespacedName]()
		for _, rlq := range remoteLQList.Items {
			if lqKeys.Has(types.NamespacedName{Namespace: rlq.Namespace, Name: rlq.Name}) {
				remoteCQKeys.Insert(types.NamespacedName{Name: string(rlq.Spec.ClusterQueue)})
			}
		}

		remoteCQList := &kueue.ClusterQueueList{}
		// This List is cached (by the selectivelyCachingClient).
		if err := remoteCl.List(ctx, remoteCQList); err != nil {
			return aggregatedWorkerQuotas{}, fmt.Errorf("listing ClusterQueues on worker %q: %w", workerName, err)
		}

		for _, rcq := range remoteCQList.Items {
			key := types.NamespacedName{Name: rcq.Name}
			if !remoteCQKeys.Has(key) {
				continue
			}
			for _, rg := range resourcegroups.EffectiveResourceGroups(&rcq) {
				for _, flavor := range rg.Flavors {
					for _, res := range flavor.Resources {
						if curr, exists := total[res.Name]; exists {
							curr.Add(res.NominalQuota)
							total[res.Name] = curr
						} else {
							total[res.Name] = res.NominalQuota.DeepCopy()
						}
					}
				}
			}
		}
	}

	return aggregatedWorkerQuotas{
		quotas:              total,
		connectedWorkers:    connectedWorkers,
		disconnectedWorkers: sets.List(disconnected),
	}, nil
}

// updateStatus sets the CapacitySynchronized condition for the current generation of the CapacityProvider,
// and updates status.capacity when newCapacity is non-nil. It patches the status only when either
// status.capacity or the condition changed (including a change of ObservedGeneration only, because DQO
// uses the capacity only when ObservedGeneration matches the generation).
func (r *cpReconciler) updateStatus(
	ctx context.Context,
	cp *kueuealpha.CapacityProvider,
	newCapacity *kueuealpha.CapacityProviderNormalizedCapacity,
	status metav1.ConditionStatus,
	reason, message string,
) error {
	err := clientutil.PatchStatus(ctx, r.client, cp, func() (bool, error) {
		changed := apimeta.SetStatusCondition(&cp.Status.Conditions, metav1.Condition{
			Type:               kueuealpha.CapacityProviderCapacitySynchronized,
			Status:             status,
			Reason:             reason,
			Message:            api.TruncateConditionMessage(message),
			ObservedGeneration: cp.Generation,
		})
		if newCapacity != nil && !equality.Semantic.DeepEqual(cp.Status.Capacity, newCapacity) {
			cp.Status.Capacity = newCapacity
			changed = true
		}
		return changed, nil
	})
	if client.IgnoreNotFound(err) != nil {
		return fmt.Errorf("updating CapacityProvider status: %w", err)
	}
	return nil
}

func isMultiKueueCapacityProvider(cp *kueuealpha.CapacityProvider) bool {
	return cp.Spec.ControllerName == kueue.MultiKueueControllerName
}

func isCapacityProviderConfigRef(ref *kueuealpha.CapacityProviderParametersReference) bool {
	return ref != nil && ref.APIGroup == capacityProviderConfigGK.Group && ref.Kind == capacityProviderConfigGK.Kind
}

func (r *cpReconciler) queueEventsForProviderConfig(ctx context.Context, configName string, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	cpList := &kueuealpha.CapacityProviderList{}
	if err := r.client.List(ctx, cpList, client.MatchingFields{CapacityProviderParametersKey: configName}); err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "Failed to list CapacityProviders using MultiKueueCapacityProviderConfig", "multiKueueCapacityProviderConfig", configName)
		return
	}

	for _, cp := range cpList.Items {
		q.AddAfter(reconcile.Request{Name: cp.Name}, r.eventsBatchPeriod)
	}
}

func (r *cpReconciler) queueEventsForManagerCQ(ctx context.Context, cqName string, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	configList := &kueuealpha.MultiKueueCapacityProviderConfigList{}
	if err := r.client.List(ctx, configList, client.MatchingFields{ProviderConfigManagerSubtreeRootKey: cqName}); err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "Failed to list MultiKueueCapacityProviderConfigs for ClusterQueue", "clusterQueue", cqName)
		return
	}

	for _, cfg := range configList.Items {
		r.queueEventsForProviderConfig(ctx, cfg.Name, q)
	}
}

func (r *cpReconciler) queueEventsForAC(ctx context.Context, acName string, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	cqList := &kueue.ClusterQueueList{}
	if err := r.client.List(ctx, cqList, client.MatchingFields{ClusterQueueAdmissionChecksKey: acName}); err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "Failed to list ClusterQueues for AdmissionCheck", "admissionCheck", acName)
		return
	}

	for _, cq := range cqList.Items {
		r.queueEventsForManagerCQ(ctx, cq.Name, q)
	}
}

func (r *cpReconciler) queueEventsForMKConfig(ctx context.Context, configName string, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	acList := &kueue.AdmissionCheckList{}
	if err := r.client.List(ctx, acList, client.MatchingFields{AdmissionCheckUsingConfigKey: configName}); err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "Failed to list AdmissionChecks using MultiKueueConfig", "multiKueueConfig", configName)
		return
	}

	for _, ac := range acList.Items {
		r.queueEventsForAC(ctx, ac.Name, q)
	}
}

func (r *cpReconciler) queueEventsForMKCluster(ctx context.Context, clusterName string, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	configList := &kueue.MultiKueueConfigList{}
	if err := r.client.List(ctx, configList, client.MatchingFields{UsingMultiKueueClusters: clusterName}); err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "Failed to list MultiKueueConfigs using MultiKueueCluster", "multiKueueCluster", clusterName)
		return
	}

	for _, cfg := range configList.Items {
		r.queueEventsForMKConfig(ctx, cfg.Name, q)
	}
}

// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=capacityproviders,verbs=get;list;watch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=capacityproviders/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=multikueuecapacityproviderconfigs,verbs=get;list;watch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=clusterqueues,verbs=get;list;watch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=localqueues,verbs=get;list;watch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=admissionchecks,verbs=get;list;watch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=multikueueconfigs,verbs=get;list;watch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=multikueueclusters,verbs=get;list;watch

func (r *cpReconciler) setupWithManager(mgr ctrl.Manager) error {
	cpEventFilter := predicate.NewPredicateFuncs(func(obj client.Object) bool {
		cp, ok := obj.(*kueuealpha.CapacityProvider)
		return ok && isMultiKueueCapacityProvider(cp)
	})

	remoteHandler := handler.TypedFuncs[kueue.ClusterQueueReference, reconcile.Request]{
		GenericFunc: func(ctx context.Context, e event.TypedGenericEvent[kueue.ClusterQueueReference], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			r.queueEventsForManagerCQ(ctx, string(e.Object), q)
		},
	}

	return ctrl.NewControllerManagedBy(mgr).
		Named("multikueue_capacityprovider").
		For(&kueuealpha.CapacityProvider{}, builder.WithPredicates(cpEventFilter, predicate.GenerationChangedPredicate{})).
		Watches(&kueuealpha.MultiKueueCapacityProviderConfig{}, &cpProviderConfigHandler{reconciler: r}).
		Watches(&kueue.ClusterQueue{}, &cpCQHandler{reconciler: r}).
		Watches(&kueue.LocalQueue{}, &cpLQHandler{reconciler: r}).
		Watches(&kueue.AdmissionCheck{}, &cpACHandler{reconciler: r}).
		Watches(&kueue.MultiKueueConfig{}, &cpMKConfigHandler{reconciler: r}).
		Watches(&kueue.MultiKueueCluster{}, &cpMKClusterHandler{reconciler: r}).
		WatchesRawSource(source.Channel(r.clusters.cqUpdateCh, remoteHandler)).
		WithOptions(controller.Options{
			LogConstructor: roletracker.NewLogConstructor(r.roleTracker, "multikueue-capacityprovider-reconciler"),
		}).
		Complete(r)
}

type cpProviderConfigHandler struct {
	reconciler *cpReconciler
}

var _ handler.EventHandler = (*cpProviderConfigHandler)(nil)

func (h *cpProviderConfigHandler) Create(ctx context.Context, event event.CreateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	h.reconciler.queueEventsForProviderConfig(ctx, event.Object.GetName(), q)
}

func (h *cpProviderConfigHandler) Update(ctx context.Context, event event.UpdateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	if event.ObjectOld.GetGeneration() != event.ObjectNew.GetGeneration() {
		h.reconciler.queueEventsForProviderConfig(ctx, event.ObjectNew.GetName(), q)
	}
}

func (h *cpProviderConfigHandler) Delete(ctx context.Context, event event.DeleteEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	h.reconciler.queueEventsForProviderConfig(ctx, event.Object.GetName(), q)
}

func (h *cpProviderConfigHandler) Generic(ctx context.Context, event event.GenericEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

// cpCQHandler ignores ClusterQueue status changes, so that DQO writing the manager
// ClusterQueue's status.effectiveQuotas does not trigger the provider.
type cpCQHandler struct {
	reconciler *cpReconciler
}

var _ handler.EventHandler = (*cpCQHandler)(nil)

func (h *cpCQHandler) Create(ctx context.Context, event event.CreateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	h.reconciler.queueEventsForManagerCQ(ctx, event.Object.GetName(), q)
}

func (h *cpCQHandler) Update(ctx context.Context, event event.UpdateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	if event.ObjectOld.GetGeneration() != event.ObjectNew.GetGeneration() {
		h.reconciler.queueEventsForManagerCQ(ctx, event.ObjectNew.GetName(), q)
	}
}

func (h *cpCQHandler) Delete(ctx context.Context, event event.DeleteEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	h.reconciler.queueEventsForManagerCQ(ctx, event.Object.GetName(), q)
}

func (h *cpCQHandler) Generic(ctx context.Context, event event.GenericEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

type cpLQHandler struct {
	reconciler *cpReconciler
}

var _ handler.EventHandler = (*cpLQHandler)(nil)

func (h *cpLQHandler) Create(ctx context.Context, event event.CreateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	if lq, ok := event.Object.(*kueue.LocalQueue); ok {
		h.reconciler.queueEventsForManagerCQ(ctx, string(lq.Spec.ClusterQueue), q)
	}
}

func (h *cpLQHandler) Update(ctx context.Context, event event.UpdateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	// No action needed, Spec.ClusterQueue is immutable on LocalQueues.
}

func (h *cpLQHandler) Delete(ctx context.Context, event event.DeleteEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	if lq, ok := event.Object.(*kueue.LocalQueue); ok {
		h.reconciler.queueEventsForManagerCQ(ctx, string(lq.Spec.ClusterQueue), q)
	}
}

func (h *cpLQHandler) Generic(ctx context.Context, event event.GenericEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

type cpACHandler struct {
	reconciler *cpReconciler
}

var _ handler.EventHandler = (*cpACHandler)(nil)

func (h *cpACHandler) Create(ctx context.Context, event event.CreateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	h.reconciler.queueEventsForAC(ctx, event.Object.GetName(), q)
}

func (h *cpACHandler) Update(ctx context.Context, event event.UpdateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	if event.ObjectNew.GetGeneration() != event.ObjectOld.GetGeneration() {
		h.reconciler.queueEventsForAC(ctx, event.ObjectNew.GetName(), q)
	}
}

func (h *cpACHandler) Delete(ctx context.Context, event event.DeleteEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	h.reconciler.queueEventsForAC(ctx, event.Object.GetName(), q)
}

func (h *cpACHandler) Generic(ctx context.Context, event event.GenericEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

type cpMKConfigHandler struct {
	reconciler *cpReconciler
}

var _ handler.EventHandler = (*cpMKConfigHandler)(nil)

func (h *cpMKConfigHandler) Create(ctx context.Context, event event.CreateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	h.reconciler.queueEventsForMKConfig(ctx, event.Object.GetName(), q)
}

func (h *cpMKConfigHandler) Update(ctx context.Context, event event.UpdateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	if event.ObjectOld.GetGeneration() != event.ObjectNew.GetGeneration() {
		h.reconciler.queueEventsForMKConfig(ctx, event.ObjectNew.GetName(), q)
	}
}

func (h *cpMKConfigHandler) Delete(ctx context.Context, event event.DeleteEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	h.reconciler.queueEventsForMKConfig(ctx, event.Object.GetName(), q)
}

func (h *cpMKConfigHandler) Generic(ctx context.Context, event event.GenericEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

type cpMKClusterHandler struct {
	reconciler *cpReconciler
}

var _ handler.EventHandler = (*cpMKClusterHandler)(nil)

func (h *cpMKClusterHandler) Create(ctx context.Context, event event.CreateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	h.reconciler.queueEventsForMKCluster(ctx, event.Object.GetName(), q)
}

func (h *cpMKClusterHandler) Update(ctx context.Context, event event.UpdateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	oldMKC, isOldMKC := event.ObjectOld.(*kueue.MultiKueueCluster)
	newMKC, isNewMKC := event.ObjectNew.(*kueue.MultiKueueCluster)
	if !isOldMKC || !isNewMKC {
		return
	}

	oldActive := apimeta.FindStatusCondition(oldMKC.Status.Conditions, kueue.MultiKueueClusterActive)
	newActive := apimeta.FindStatusCondition(newMKC.Status.Conditions, kueue.MultiKueueClusterActive)
	if !isConditionEqual(oldActive, newActive) {
		h.reconciler.queueEventsForMKCluster(ctx, newMKC.Name, q)
	}
}

func (h *cpMKClusterHandler) Delete(ctx context.Context, event event.DeleteEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	h.reconciler.queueEventsForMKCluster(ctx, event.Object.GetName(), q)
}

func (h *cpMKClusterHandler) Generic(ctx context.Context, event event.GenericEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}
