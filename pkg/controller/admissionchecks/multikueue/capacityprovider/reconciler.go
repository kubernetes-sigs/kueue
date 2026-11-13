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

// Package capacityprovider implements the MultiKueue CapacityProvider controller
// (controllerName kueue.x-k8s.io/multikueue). It publishes the summed quotas of the
// worker ClusterQueues related to a manager ClusterQueue, so that Dynamic Quota
// Orchestration can derive the manager's quota from its MultiKueue workers.
package capacityprovider

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
	"sigs.k8s.io/kueue/pkg/constants"
	mkindexer "sigs.k8s.io/kueue/pkg/controller/admissionchecks/multikueue/indexer"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
	"sigs.k8s.io/kueue/pkg/util/api"
	clientutil "sigs.k8s.io/kueue/pkg/util/client"
	"sigs.k8s.io/kueue/pkg/util/resourcegroups"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
)

var capacityProviderConfigGK = kueuealpha.SchemeGroupVersion.WithKind("MultiKueueCapacityProviderConfig").GroupKind()

// WorkerClients gives access to the clients of the MultiKueue worker clusters.
type WorkerClients interface {
	// ConnectedClient returns a reader for the worker cluster, and false when the worker
	// cluster has no client or its client is not connected. The reader serves LocalQueue
	// and ClusterQueue lists from a cache of the worker cluster.
	ConnectedClient(ctx context.Context, clusterName string) (client.Reader, bool)
}

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

// Reconciler reconciles the CapacityProviders whose controllerName is kueue.x-k8s.io/multikueue.
// It resolves the manager ClusterQueue through the MultiKueueCapacityProviderConfig referenced in
// spec.parameters, sums the quotas of its related worker ClusterQueues across connected workers,
// and publishes the result to status.capacity with the CapacitySynchronized condition.
type Reconciler struct {
	client            client.Client
	helper            *admissioncheck.MultiKueueStoreHelper
	workers           WorkerClients
	cqUpdateCh        <-chan event.TypedGenericEvent[kueue.ClusterQueueReference]
	roleTracker       *roletracker.RoleTracker
	eventsBatchPeriod time.Duration
}

var _ reconcile.Reconciler = (*Reconciler)(nil)

type Option func(*Reconciler)

// WithRoleTracker sets the role tracker for HA logging.
func WithRoleTracker(rt *roletracker.RoleTracker) Option {
	return func(r *Reconciler) {
		r.roleTracker = rt
	}
}

// WithEventsBatchPeriod sets the delay used when queueing CapacityProviders after events.
func WithEventsBatchPeriod(d time.Duration) Option {
	return func(r *Reconciler) {
		r.eventsBatchPeriod = d
	}
}

// NewReconciler creates the MultiKueue CapacityProvider reconciler. cqUpdateCh carries the names of
// the manager ClusterQueues whose related worker ClusterQueues or LocalQueues changed. The reconciler
// must be its only consumer: the remote watchers block once its buffer fills, and a second consumer
// would take some of the events.
func NewReconciler(
	c client.Client,
	helper *admissioncheck.MultiKueueStoreHelper,
	workers WorkerClients,
	cqUpdateCh <-chan event.TypedGenericEvent[kueue.ClusterQueueReference],
	opts ...Option,
) *Reconciler {
	r := &Reconciler{
		client:            c,
		helper:            helper,
		workers:           workers,
		cqUpdateCh:        cqUpdateCh,
		eventsBatchPeriod: constants.UpdatesBatchPeriod,
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
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

	log.V(3).Info("Reconcile MultiKueue CapacityProvider")

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

	newCapacity, err := normalizedCapacity(cp, cq, agg.quotas)
	if err != nil {
		return reconcile.Result{}, r.updateStatus(ctx, cp, nil, metav1.ConditionFalse, kueuealpha.CapacityProviderReasonMisconfigured, err.Error())
	}

	msg := "Capacity synchronized from MultiKueue worker clusters"
	if len(agg.disconnectedWorkers) > 0 {
		msg = fmt.Sprintf("Capacity synchronized from connected MultiKueue worker clusters; disconnected worker clusters: %v", agg.disconnectedWorkers)
	}
	return reconcile.Result{}, r.updateStatus(ctx, cp, newCapacity, metav1.ConditionTrue, kueuealpha.CapacityProviderReasonSynchronized, msg)
}

// resolveManagerClusterQueue follows spec.parameters to the manager ClusterQueue and its MultiKueueConfig,
// and checks that they meet the Alpha constraints. It returns a *misconfiguredError when they do not.
func (r *Reconciler) resolveManagerClusterQueue(ctx context.Context, cp *kueuealpha.CapacityProvider) (*kueue.ClusterQueue, *kueue.MultiKueueConfig, error) {
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
func (r *Reconciler) getMultiKueueAdmissionCheck(ctx context.Context, cq *kueue.ClusterQueue) (*kueue.AdmissionCheck, bool, error) {
	if cq.Spec.AdmissionChecksStrategy == nil {
		return nil, false, nil
	}

	acList := &kueue.AdmissionCheckList{}
	if err := r.client.List(ctx, acList, client.MatchingFields{mkindexer.AdmissionCheckControllerNameKey: kueue.MultiKueueControllerName}); err != nil {
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
func (r *Reconciler) aggregateWorkerQuotas(ctx context.Context, cq *kueue.ClusterQueue, cfg *kueue.MultiKueueConfig) (aggregatedWorkerQuotas, error) {
	managerLQs, err := r.managerLocalQueues(ctx, cq)
	if err != nil {
		return aggregatedWorkerQuotas{}, err
	}

	total := make(map[corev1.ResourceName]resource.Quantity)
	disconnected := sets.New[string]()
	connectedWorkers := 0
	for _, workerName := range cfg.Spec.Clusters {
		workerClient, connected, err := r.connectedWorkerClient(ctx, workerName)
		if err != nil {
			return aggregatedWorkerQuotas{}, err
		}
		if !connected {
			disconnected.Insert(workerName)
			continue
		}
		connectedWorkers++

		if err := addWorkerQuotas(ctx, total, workerClient, workerName, managerLQs); err != nil {
			return aggregatedWorkerQuotas{}, err
		}
	}

	return aggregatedWorkerQuotas{
		quotas:              total,
		connectedWorkers:    connectedWorkers,
		disconnectedWorkers: sets.List(disconnected),
	}, nil
}

func (r *Reconciler) managerLocalQueues(ctx context.Context, cq *kueue.ClusterQueue) (sets.Set[types.NamespacedName], error) {
	lqList := &kueue.LocalQueueList{}
	if err := r.client.List(ctx, lqList, client.MatchingFields{indexer.QueueClusterQueueKey: cq.Name}); err != nil {
		return nil, fmt.Errorf("listing local LocalQueues: %w", err)
	}

	keys := sets.New[types.NamespacedName]()
	for i := range lqList.Items {
		keys.Insert(client.ObjectKeyFromObject(&lqList.Items[i]))
	}
	return keys, nil
}

func (r *Reconciler) connectedWorkerClient(ctx context.Context, workerName string) (client.Reader, bool, error) {
	log := ctrl.LoggerFrom(ctx).WithValues("workerCluster", workerName)

	// The clusters reconciler removes the remote client of a deleted MultiKueueCluster
	// asynchronously, so check the object itself. Otherwise the reconcile queued by the
	// MultiKueueCluster delete event could still sum the quotas of the deleted worker.
	mkc := &kueue.MultiKueueCluster{}
	if err := r.client.Get(ctx, types.NamespacedName{Name: workerName}, mkc); err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, false, fmt.Errorf("getting MultiKueueCluster %q: %w", workerName, err)
		}
		log.V(3).Info("Worker MultiKueueCluster not found, skipping it in quota aggregation")
		return nil, false, nil
	}
	if !mkc.DeletionTimestamp.IsZero() {
		log.V(3).Info("Worker MultiKueueCluster is being deleted, skipping it in quota aggregation")
		return nil, false, nil
	}

	workerClient, connected := r.workers.ConnectedClient(ctx, workerName)
	return workerClient, connected, nil
}

// addWorkerQuotas adds to total the nominal quotas of the worker ClusterQueues referenced by
// worker LocalQueues that share a namespace/name with managerLQs.
func addWorkerQuotas(
	ctx context.Context,
	total map[corev1.ResourceName]resource.Quantity,
	workerClient client.Reader,
	workerName string,
	managerLQs sets.Set[types.NamespacedName],
) error {
	// This List is cached (see WorkerClients.ConnectedClient).
	workerLQList := &kueue.LocalQueueList{}
	if err := workerClient.List(ctx, workerLQList); err != nil {
		return fmt.Errorf("listing LocalQueues on worker %q: %w", workerName, err)
	}
	relatedCQs := sets.New[string]()
	for i := range workerLQList.Items {
		if managerLQs.Has(client.ObjectKeyFromObject(&workerLQList.Items[i])) {
			relatedCQs.Insert(string(workerLQList.Items[i].Spec.ClusterQueue))
		}
	}

	// This List is cached (see WorkerClients.ConnectedClient).
	workerCQList := &kueue.ClusterQueueList{}
	if err := workerClient.List(ctx, workerCQList); err != nil {
		return fmt.Errorf("listing ClusterQueues on worker %q: %w", workerName, err)
	}
	for i := range workerCQList.Items {
		if relatedCQs.Has(workerCQList.Items[i].Name) {
			addNominalQuotas(total, &workerCQList.Items[i])
		}
	}
	return nil
}

func addNominalQuotas(total map[corev1.ResourceName]resource.Quantity, cq *kueue.ClusterQueue) {
	for _, rg := range resourcegroups.EffectiveResourceGroups(cq) {
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

// normalizedCapacity returns the capacity of the orchestrated ResourceFlavor: the summed worker quota
// for each coveredResource of the manager ClusterQueue, or zero when no worker sets that resource.
// It returns a *misconfiguredError when the workers set quota for a resource that the manager
// ClusterQueue does not cover.
func normalizedCapacity(cp *kueuealpha.CapacityProvider, cq *kueue.ClusterQueue, workerQuotas map[corev1.ResourceName]resource.Quantity) (*kueuealpha.CapacityProviderNormalizedCapacity, error) {
	coveredResources := cq.Spec.ResourceGroups[0].CoveredResources
	if missing := sets.KeySet(workerQuotas).Difference(sets.New(coveredResources...)); missing.Len() > 0 {
		return nil, misconfigured("manager-side coveredResources is missing resources configured on workers: %v", sets.List(missing))
	}

	resources := make(corev1.ResourceList, len(coveredResources))
	for _, resName := range coveredResources {
		if qty, ok := workerQuotas[resName]; ok {
			resources[resName] = qty
		} else {
			resources[resName] = *resource.NewQuantity(0, resource.DecimalSI)
		}
	}
	return &kueuealpha.CapacityProviderNormalizedCapacity{
		Flavors: []kueuealpha.CapacityProviderNormalizedCapacityFlavor{
			{
				Name:      cp.Spec.OrchestratedFlavors[0].Name,
				Resources: resources,
			},
		},
	}, nil
}

// updateStatus sets the CapacitySynchronized condition for the current generation of the CapacityProvider,
// and updates status.capacity when newCapacity is non-nil. It patches the status only when either
// status.capacity or the condition changed (including a change of ObservedGeneration only, because DQO
// uses the capacity only when ObservedGeneration matches the generation).
func (r *Reconciler) updateStatus(
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

func (r *Reconciler) queueEventsForProviderConfig(ctx context.Context, configName string, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	cpList := &kueuealpha.CapacityProviderList{}
	if err := r.client.List(ctx, cpList, client.MatchingFields{mkindexer.CapacityProviderParametersKey: configName}); err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "Failed to list CapacityProviders using MultiKueueCapacityProviderConfig", "multiKueueCapacityProviderConfig", configName)
		return
	}

	for _, cp := range cpList.Items {
		q.AddAfter(reconcile.Request{Name: cp.Name}, r.eventsBatchPeriod)
	}
}

func (r *Reconciler) queueEventsForManagerCQ(ctx context.Context, cqName string, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	configList := &kueuealpha.MultiKueueCapacityProviderConfigList{}
	if err := r.client.List(ctx, configList, client.MatchingFields{mkindexer.ProviderConfigManagerSubtreeRootKey: cqName}); err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "Failed to list MultiKueueCapacityProviderConfigs for ClusterQueue", "clusterQueue", cqName)
		return
	}

	for _, cfg := range configList.Items {
		r.queueEventsForProviderConfig(ctx, cfg.Name, q)
	}
}

func (r *Reconciler) queueEventsForAC(ctx context.Context, acName string, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	cqList := &kueue.ClusterQueueList{}
	if err := r.client.List(ctx, cqList, client.MatchingFields{mkindexer.ClusterQueueAdmissionChecksKey: acName}); err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "Failed to list ClusterQueues for AdmissionCheck", "admissionCheck", acName)
		return
	}

	for _, cq := range cqList.Items {
		r.queueEventsForManagerCQ(ctx, cq.Name, q)
	}
}

func (r *Reconciler) queueEventsForMKConfig(ctx context.Context, configName string, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	acList := &kueue.AdmissionCheckList{}
	if err := r.client.List(ctx, acList, client.MatchingFields{mkindexer.AdmissionCheckUsingConfigKey: configName}); err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "Failed to list AdmissionChecks using MultiKueueConfig", "multiKueueConfig", configName)
		return
	}

	for _, ac := range acList.Items {
		r.queueEventsForAC(ctx, ac.Name, q)
	}
}

func (r *Reconciler) queueEventsForMKCluster(ctx context.Context, clusterName string, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	configList := &kueue.MultiKueueConfigList{}
	if err := r.client.List(ctx, configList, client.MatchingFields{mkindexer.UsingMultiKueueClusters: clusterName}); err != nil {
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

// SetupWithManager registers the controller and its watches with the manager.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
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
		Watches(&kueuealpha.MultiKueueCapacityProviderConfig{}, r.providerConfigHandler()).
		Watches(&kueue.ClusterQueue{}, r.clusterQueueHandler()).
		Watches(&kueue.LocalQueue{}, r.localQueueHandler()).
		Watches(&kueue.AdmissionCheck{}, r.admissionCheckHandler()).
		Watches(&kueue.MultiKueueConfig{}, r.multiKueueConfigHandler()).
		Watches(&kueue.MultiKueueCluster{}, r.multiKueueClusterHandler()).
		WatchesRawSource(source.Channel(r.cqUpdateCh, remoteHandler)).
		WithOptions(controller.Options{
			LogConstructor: roletracker.NewLogConstructor(r.roleTracker, "multikueue-capacityprovider-reconciler"),
		}).
		Complete(r)
}

type enqueueFunc func(ctx context.Context, name string, q workqueue.TypedRateLimitingInterface[reconcile.Request])

// newEventHandler returns an event handler that calls enqueue with the name of the
// watched object. Update events are queued only when updated returns true.
func newEventHandler(enqueue enqueueFunc, updated func(oldObj, newObj client.Object) bool) handler.EventHandler {
	return handler.Funcs{
		CreateFunc: func(ctx context.Context, e event.CreateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			if e.Object != nil {
				enqueue(ctx, e.Object.GetName(), q)
			}
		},
		UpdateFunc: func(ctx context.Context, e event.UpdateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			if e.ObjectOld != nil && e.ObjectNew != nil && updated(e.ObjectOld, e.ObjectNew) {
				enqueue(ctx, e.ObjectNew.GetName(), q)
			}
		},
		DeleteFunc: func(ctx context.Context, e event.DeleteEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			if e.Object != nil {
				enqueue(ctx, e.Object.GetName(), q)
			}
		},
	}
}

func generationChanged(oldObj, newObj client.Object) bool {
	return oldObj.GetGeneration() != newObj.GetGeneration()
}

func (r *Reconciler) providerConfigHandler() handler.EventHandler {
	return newEventHandler(r.queueEventsForProviderConfig, generationChanged)
}

// clusterQueueHandler ignores ClusterQueue status changes, so that DQO writing the manager
// ClusterQueue's status.effectiveQuotas does not trigger the provider.
func (r *Reconciler) clusterQueueHandler() handler.EventHandler {
	return newEventHandler(r.queueEventsForManagerCQ, generationChanged)
}

// localQueueHandler ignores Update events, because Spec.ClusterQueue is immutable on LocalQueues.
func (r *Reconciler) localQueueHandler() handler.EventHandler {
	enqueue := func(ctx context.Context, obj client.Object, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
		if lq, ok := obj.(*kueue.LocalQueue); ok && lq != nil && lq.Spec.ClusterQueue != "" {
			r.queueEventsForManagerCQ(ctx, string(lq.Spec.ClusterQueue), q)
		}
	}
	return handler.Funcs{
		CreateFunc: func(ctx context.Context, e event.CreateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			enqueue(ctx, e.Object, q)
		},
		DeleteFunc: func(ctx context.Context, e event.DeleteEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			enqueue(ctx, e.Object, q)
		},
	}
}

func (r *Reconciler) admissionCheckHandler() handler.EventHandler {
	return newEventHandler(r.queueEventsForAC, generationChanged)
}

func (r *Reconciler) multiKueueConfigHandler() handler.EventHandler {
	return newEventHandler(r.queueEventsForMKConfig, generationChanged)
}

// multiKueueClusterHandler queues on Update events only when the MultiKueueCluster starts
// being deleted or its Active condition changes. The clusters reconciler updates Active after
// connecting or disconnecting the worker, but stops a deleted worker without updating Active.
func (r *Reconciler) multiKueueClusterHandler() handler.EventHandler {
	return newEventHandler(r.queueEventsForMKCluster, multiKueueClusterUpdated)
}

func multiKueueClusterUpdated(oldObj, newObj client.Object) bool {
	oldMKC, isOldMKC := oldObj.(*kueue.MultiKueueCluster)
	newMKC, isNewMKC := newObj.(*kueue.MultiKueueCluster)
	if !isOldMKC || !isNewMKC || oldMKC == nil || newMKC == nil {
		return false
	}
	if oldMKC.DeletionTimestamp.IsZero() != newMKC.DeletionTimestamp.IsZero() {
		return true
	}

	oldActive := apimeta.FindStatusCondition(oldMKC.Status.Conditions, kueue.MultiKueueClusterActive)
	newActive := apimeta.FindStatusCondition(newMKC.Status.Conditions, kueue.MultiKueueClusterActive)
	return activeConditionChanged(oldActive, newActive)
}

// activeConditionChanged compares ObservedGeneration too, because a reconnect after a
// MultiKueueCluster spec change only updates the ObservedGeneration of Active.
func activeConditionChanged(oldCond, newCond *metav1.Condition) bool {
	if oldCond == nil || newCond == nil {
		return oldCond != newCond
	}
	return oldCond.Status != newCond.Status ||
		oldCond.Reason != newCond.Reason ||
		oldCond.Message != newCond.Message ||
		oldCond.ObservedGeneration != newCond.ObservedGeneration
}
