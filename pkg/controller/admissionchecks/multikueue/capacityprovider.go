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
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
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
	"sigs.k8s.io/kueue/pkg/util/roletracker"
)

// capacityProviderReconciler is the built-in MultiKueue CapacityProvider (KEP-17401).
// It reconciles the CapacityProviders with the MultiKueue controllerName. For each of them,
// it sums the quotas of the related worker ClusterQueues of the manager ClusterQueue that the
// referenced MultiKueueCapacityProviderConfig selects, and writes the sum to status.capacity.
// DQO then writes the manager quota to the ClusterQueue status.effectiveQuotas, so Kueue
// does not change the manager ClusterQueue spec.
type capacityProviderReconciler struct {
	client            client.Client
	helper            *admissioncheck.MultiKueueStoreHelper
	clusters          *clustersReconciler
	roleTracker       *roletracker.RoleTracker
	eventsBatchPeriod time.Duration
}

var _ reconcile.Reconciler = (*capacityProviderReconciler)(nil)

func newCapacityProviderReconciler(
	c client.Client,
	helper *admissioncheck.MultiKueueStoreHelper,
	clusters *clustersReconciler,
	roleTracker *roletracker.RoleTracker,
	eventsBatchPeriod time.Duration,
) *capacityProviderReconciler {
	return &capacityProviderReconciler{
		client:            c,
		helper:            helper,
		clusters:          clusters,
		roleTracker:       roleTracker,
		eventsBatchPeriod: eventsBatchPeriod,
	}
}

// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=capacityproviders,verbs=get;list;watch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=capacityproviders/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=multikueuecapacityproviderconfigs,verbs=get;list;watch

func (r *capacityProviderReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	cp := &kueuealpha.CapacityProvider{}
	if err := r.client.Get(ctx, req.NamespacedName, cp); err != nil {
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}
	if !isMultiKueueCapacityProvider(cp) || !cp.DeletionTimestamp.IsZero() {
		return reconcile.Result{}, nil
	}
	ctrl.LoggerFrom(ctx).V(2).Info("Reconciling MultiKueue CapacityProvider")

	capacity, reason, message, err := r.observeCapacity(ctx, cp)
	if err != nil {
		return reconcile.Result{}, err
	}
	return reconcile.Result{}, r.updateStatus(ctx, cp, capacity, reason, message)
}

// observeCapacity returns the reason and the message of the CapacitySynchronized condition,
// and the capacity to publish when the reason is Synchronized.
func (r *capacityProviderReconciler) observeCapacity(
	ctx context.Context,
	cp *kueuealpha.CapacityProvider,
) (capacity *kueuealpha.CapacityProviderNormalizedCapacity, reason, message string, err error) {
	managerCQ, mkConfig, misconfigured, err := r.resolveManager(ctx, cp)
	if err != nil {
		return nil, "", "", err
	}
	if misconfigured != "" {
		return nil, kueuealpha.CapacityProviderReasonMisconfigured, misconfigured, nil
	}

	quotas, disconnected, err := r.sumWorkerQuotas(ctx, managerCQ, mkConfig)
	if err != nil {
		return nil, "", "", err
	}
	connected := len(mkConfig.Spec.Clusters) - len(disconnected)
	if connected == 0 {
		return nil, kueuealpha.CapacityProviderReasonSourceUnavailable, "No worker cluster of the manager ClusterQueue is connected", nil
	}

	// KEP-9988 constraint: the manager flavor covers all resources of the related worker ClusterQueues.
	// TODO(#17401): DQO divides the capacity in proportion to the spec nominalQuota of the manager
	// ClusterQueue, so a covered resource with nominalQuota 0 gets no quota. Document it or report it here.
	covered := sets.New(managerCQ.Spec.ResourceGroups[0].CoveredResources...)
	if missing := sets.KeySet(quotas).Difference(covered); missing.Len() > 0 {
		return nil, kueuealpha.CapacityProviderReasonMisconfigured,
			fmt.Sprintf("ClusterQueue %q does not cover resources of the related worker ClusterQueues: %v", managerCQ.Name, sets.List(missing)), nil
	}

	message = fmt.Sprintf("Capacity synchronized with %d of %d worker clusters", connected, len(mkConfig.Spec.Clusters))
	if len(disconnected) > 0 {
		message += "; disconnected worker clusters: " + strings.Join(disconnected, ", ")
	}
	capacity = &kueuealpha.CapacityProviderNormalizedCapacity{
		Flavors: []kueuealpha.CapacityProviderNormalizedCapacityFlavor{{
			Name:      cp.Spec.OrchestratedFlavors[0].Name,
			Resources: quotas,
		}},
	}
	return capacity, kueuealpha.CapacityProviderReasonSynchronized, message, nil
}

// resolveManager follows spec.parameters to the manager ClusterQueue and to the MultiKueueConfig
// of its MultiKueue AdmissionCheck. It returns a misconfiguration message if the configuration
// is invalid or a referenced object does not exist.
func (r *capacityProviderReconciler) resolveManager(
	ctx context.Context,
	cp *kueuealpha.CapacityProvider,
) (managerCQ *kueue.ClusterQueue, mkConfig *kueue.MultiKueueConfig, misconfigured string, err error) {
	configName, ok := providerConfigName(cp)
	if !ok {
		return nil, nil, "spec.parameters must refer to a " + kueuealpha.MultiKueueCapacityProviderConfigKind, nil
	}
	// TODO(#17401): re-evaluate for Beta the mapping of the worker quotas into more than one manager flavor.
	if len(cp.Spec.OrchestratedFlavors) != 1 {
		return nil, nil, "spec.orchestratedFlavors must contain exactly one flavor", nil
	}

	// TODO(#17401): before Beta, review the configuration errors reported by Alpha users (for example,
	// the CapacityProvider and the DQO select different ClusterQueues) and evaluate more validation.
	providerConfig := &kueuealpha.MultiKueueCapacityProviderConfig{}
	if err := r.client.Get(ctx, types.NamespacedName{Name: configName}, providerConfig); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil, fmt.Sprintf("MultiKueueCapacityProviderConfig %q not found", configName), nil
		}
		return nil, nil, "", err
	}
	// The API validation allows only the ClusterQueue kind.
	// TODO(#17401): re-evaluate for Beta the Cohort scope.
	ref := providerConfig.Spec.ManagerSubtreeRootRef
	if ref.Kind != kueuealpha.ClusterQueueSubtreeRootRefKind {
		return nil, nil, fmt.Sprintf("managerSubtreeRootRef kind %q is not supported", ref.Kind), nil
	}

	managerCQ = &kueue.ClusterQueue{}
	if err := r.client.Get(ctx, types.NamespacedName{Name: ref.Name}, managerCQ); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil, fmt.Sprintf("ClusterQueue %q not found", ref.Name), nil
		}
		return nil, nil, "", err
	}
	// KEP-9988 constraint: the manager ClusterQueue has exactly one ResourceFlavor, which is the orchestrated flavor.
	flavor := string(cp.Spec.OrchestratedFlavors[0].Name)
	if rgs := managerCQ.Spec.ResourceGroups; len(rgs) != 1 || len(rgs[0].Flavors) != 1 || string(rgs[0].Flavors[0].Name) != flavor {
		return nil, nil, fmt.Sprintf("ClusterQueue %q must have exactly one ResourceFlavor, which is %q", managerCQ.Name, flavor), nil
	}

	ac, err := r.multiKueueAdmissionCheck(ctx, managerCQ)
	if err != nil {
		return nil, nil, "", err
	}
	if ac == nil {
		return nil, nil, fmt.Sprintf("ClusterQueue %q has no MultiKueue AdmissionCheck", managerCQ.Name), nil
	}
	mkConfig, err = r.helper.ConfigFromRef(ctx, ac.Spec.Parameters)
	if err != nil {
		if apierrors.IsNotFound(err) || errors.Is(err, admissioncheck.ErrNilParametersRef) || errors.Is(err, admissioncheck.ErrBadParametersRef) {
			return nil, nil, fmt.Sprintf("MultiKueueConfig of AdmissionCheck %q not found", ac.Name), nil
		}
		return nil, nil, "", err
	}
	return managerCQ, mkConfig, "", nil
}

// multiKueueAdmissionCheck returns the MultiKueue AdmissionCheck of the ClusterQueue, or nil if it has none.
func (r *capacityProviderReconciler) multiKueueAdmissionCheck(ctx context.Context, cq *kueue.ClusterQueue) (*kueue.AdmissionCheck, error) {
	if cq.Spec.AdmissionChecksStrategy == nil {
		return nil, nil
	}
	for _, rule := range cq.Spec.AdmissionChecksStrategy.AdmissionChecks {
		ac := &kueue.AdmissionCheck{}
		if err := r.client.Get(ctx, types.NamespacedName{Name: string(rule.Name)}, ac); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return nil, err
		}
		if ac.Spec.ControllerName == kueue.MultiKueueControllerName {
			return ac, nil
		}
	}
	return nil, nil
}

// sumWorkerQuotas finds the related worker ClusterQueues of the manager ClusterQueue as in
// KEP-9988 Alpha1, and sums their quotas over all worker flavors. It counts only the connected
// worker clusters of the MultiKueueConfig, and also returns the worker clusters it skipped.
func (r *capacityProviderReconciler) sumWorkerQuotas(ctx context.Context, managerCQ *kueue.ClusterQueue, mkConfig *kueue.MultiKueueConfig) (corev1.ResourceList, []string, error) {
	localLQs := &kueue.LocalQueueList{}
	if err := r.client.List(ctx, localLQs, client.MatchingFields{indexer.QueueClusterQueueKey: managerCQ.Name}); err != nil {
		return nil, nil, fmt.Errorf("listing local LocalQueues: %w", err)
	}
	lqKeys := sets.New[types.NamespacedName]()
	for i := range localLQs.Items {
		lqKeys.Insert(client.ObjectKeyFromObject(&localLQs.Items[i]))
	}

	total := corev1.ResourceList{}
	var disconnected []string
	for _, workerName := range mkConfig.Spec.Clusters {
		rc, found := r.clusters.controllerFor(workerName)
		var remoteClient client.Reader
		if found && rc.connState.isConnected() {
			remoteClient = rc.getClient()
		}
		if remoteClient == nil {
			// TODO(#17401): before Beta, review the outages reported by Alpha users
			// and reconsider the handling of temporarily unavailable workers.
			disconnected = append(disconnected, workerName)
			continue
		}

		// These Lists are cached (by the selectivelyCachingClient).
		remoteLQs := &kueue.LocalQueueList{}
		if err := remoteClient.List(ctx, remoteLQs); err != nil {
			return nil, nil, fmt.Errorf("listing LocalQueues in worker cluster %q: %w", workerName, err)
		}
		workerCQNames := sets.New[string]()
		for i := range remoteLQs.Items {
			if lqKeys.Has(client.ObjectKeyFromObject(&remoteLQs.Items[i])) {
				workerCQNames.Insert(string(remoteLQs.Items[i].Spec.ClusterQueue))
			}
		}
		remoteCQs := &kueue.ClusterQueueList{}
		if err := remoteClient.List(ctx, remoteCQs); err != nil {
			return nil, nil, fmt.Errorf("listing ClusterQueues in worker cluster %q: %w", workerName, err)
		}
		for i := range remoteCQs.Items {
			if !workerCQNames.Has(remoteCQs.Items[i].Name) {
				continue
			}
			for _, rg := range workerResourceGroups(&remoteCQs.Items[i]) {
				for _, flavor := range rg.Flavors {
					for _, res := range flavor.Resources {
						quota := total[res.Name]
						quota.Add(res.NominalQuota)
						total[res.Name] = quota
					}
				}
			}
		}
	}
	return total, disconnected, nil
}

// workerResourceGroups returns the quota of a worker ClusterQueue with the "Source of Truth for Quotas"
// rule of KEP-12382: status.effectiveQuotas when it is present, otherwise spec.resourceGroups.
// The manager cannot see the feature gates of a worker, so the DynamicQuotaOrchestration gate is not checked.
func workerResourceGroups(cq *kueue.ClusterQueue) []kueue.ResourceGroup {
	if cq.Status.EffectiveQuotas != nil {
		return cq.Status.EffectiveQuotas.ResourceGroups
	}
	return cq.Spec.ResourceGroups
}

// updateStatus sets the CapacitySynchronized condition for the current generation, as DQO requires,
// and writes the capacity if it is not nil. Otherwise status.capacity keeps its last value,
// which DQO does not use while the condition is not True.
func (r *capacityProviderReconciler) updateStatus(ctx context.Context, cp *kueuealpha.CapacityProvider, capacity *kueuealpha.CapacityProviderNormalizedCapacity, reason, message string) error {
	newCP := cp.DeepCopy()
	if capacity != nil {
		newCP.Status.Capacity = capacity
	}
	status := metav1.ConditionFalse
	if reason == kueuealpha.CapacityProviderReasonSynchronized {
		status = metav1.ConditionTrue
	}
	// TODO(#17401): before Beta, re-evaluate whether metrics or events are needed, in addition
	// to this condition, to surface misconfigurations and disconnected worker clusters.
	apimeta.SetStatusCondition(&newCP.Status.Conditions, metav1.Condition{
		Type:               kueuealpha.CapacityProviderCapacitySynchronized,
		Status:             status,
		Reason:             reason,
		Message:            api.TruncateConditionMessage(message),
		ObservedGeneration: cp.Generation,
	})
	if equality.Semantic.DeepEqual(cp.Status, newCP.Status) {
		return nil
	}
	return r.client.Status().Update(ctx, newCP)
}

func (r *capacityProviderReconciler) setupWithManager(mgr ctrl.Manager) error {
	isMultiKueue := predicate.NewPredicateFuncs(func(obj client.Object) bool {
		cp, ok := obj.(*kueuealpha.CapacityProvider)
		return ok && isMultiKueueCapacityProvider(cp)
	})
	// The ClusterQueue of a LocalQueue is immutable, so only creation and deletion matter.
	lqCreateOrDelete := predicate.Funcs{UpdateFunc: func(event.UpdateEvent) bool { return false }}
	// The Active condition changes when a worker cluster connects or disconnects.
	clusterActiveChanged := predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldCluster, okOld := e.ObjectOld.(*kueue.MultiKueueCluster)
			newCluster, okNew := e.ObjectNew.(*kueue.MultiKueueCluster)
			return okOld && okNew && !isConditionEqual(
				apimeta.FindStatusCondition(oldCluster.Status.Conditions, kueue.MultiKueueClusterActive),
				apimeta.FindStatusCondition(newCluster.Status.Conditions, kueue.MultiKueueClusterActive))
		},
	}
	// The remote clients send the manager ClusterQueues whose related worker ClusterQueues changed.
	remoteHandler := handler.TypedFuncs[kueue.ClusterQueueReference, reconcile.Request]{
		GenericFunc: func(ctx context.Context, e event.TypedGenericEvent[kueue.ClusterQueueReference], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			for _, req := range r.requestsForClusterQueue(ctx, string(e.Object)) {
				q.AddAfter(req, r.eventsBatchPeriod)
			}
		},
	}

	// TODO(#17401): map the AdmissionCheck, MultiKueueConfig and MultiKueueCluster events
	// to the affected CapacityProviders only, instead of all MultiKueue CapacityProviders.
	return ctrl.NewControllerManagedBy(mgr).
		Named("multikueue_capacityprovider").
		For(&kueuealpha.CapacityProvider{}, builder.WithPredicates(isMultiKueue, predicate.GenerationChangedPredicate{})).
		Watches(&kueuealpha.MultiKueueCapacityProviderConfig{}, handler.EnqueueRequestsFromMapFunc(r.mapProviderConfig)).
		Watches(&kueue.ClusterQueue{}, handler.EnqueueRequestsFromMapFunc(r.mapClusterQueue), builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&kueue.LocalQueue{}, handler.EnqueueRequestsFromMapFunc(r.mapLocalQueue), builder.WithPredicates(lqCreateOrDelete)).
		Watches(&kueue.AdmissionCheck{}, handler.EnqueueRequestsFromMapFunc(r.mapAll), builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&kueue.MultiKueueConfig{}, handler.EnqueueRequestsFromMapFunc(r.mapAll), builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&kueue.MultiKueueCluster{}, handler.EnqueueRequestsFromMapFunc(r.mapAll), builder.WithPredicates(clusterActiveChanged)).
		WatchesRawSource(source.Channel(r.clusters.cqUpdateCh, remoteHandler)).
		WithOptions(controller.Options{
			LogConstructor: roletracker.NewLogConstructor(r.roleTracker, "multikueue-capacityprovider-reconciler"),
		}).
		Complete(r)
}

func (r *capacityProviderReconciler) mapProviderConfig(ctx context.Context, obj client.Object) []reconcile.Request {
	return r.requestsFor(ctx, sets.New(obj.GetName()))
}

func (r *capacityProviderReconciler) mapClusterQueue(ctx context.Context, obj client.Object) []reconcile.Request {
	return r.requestsForClusterQueue(ctx, obj.GetName())
}

func (r *capacityProviderReconciler) mapLocalQueue(ctx context.Context, obj client.Object) []reconcile.Request {
	lq, ok := obj.(*kueue.LocalQueue)
	if !ok {
		return nil
	}
	return r.requestsForClusterQueue(ctx, string(lq.Spec.ClusterQueue))
}

func (r *capacityProviderReconciler) mapAll(ctx context.Context, _ client.Object) []reconcile.Request {
	return r.requestsFor(ctx, nil)
}

// requestsForClusterQueue returns the MultiKueue CapacityProviders whose
// MultiKueueCapacityProviderConfig selects the manager ClusterQueue.
func (r *capacityProviderReconciler) requestsForClusterQueue(ctx context.Context, cqName string) []reconcile.Request {
	configs := &kueuealpha.MultiKueueCapacityProviderConfigList{}
	if err := r.client.List(ctx, configs); err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "Failed to list MultiKueueCapacityProviderConfigs")
		return nil
	}
	configNames := sets.New[string]()
	for i := range configs.Items {
		ref := configs.Items[i].Spec.ManagerSubtreeRootRef
		if ref.Kind == kueuealpha.ClusterQueueSubtreeRootRefKind && ref.Name == cqName {
			configNames.Insert(configs.Items[i].Name)
		}
	}
	if configNames.Len() == 0 {
		return nil
	}
	return r.requestsFor(ctx, configNames)
}

// requestsFor returns the MultiKueue CapacityProviders that refer to one of the
// MultiKueueCapacityProviderConfigs, or all of them if configNames is nil.
// TODO(#17401): use indexes instead of listing all CapacityProviders and MultiKueueCapacityProviderConfigs.
func (r *capacityProviderReconciler) requestsFor(ctx context.Context, configNames sets.Set[string]) []reconcile.Request {
	cps := &kueuealpha.CapacityProviderList{}
	if err := r.client.List(ctx, cps); err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "Failed to list CapacityProviders")
		return nil
	}
	var requests []reconcile.Request
	for i := range cps.Items {
		cp := &cps.Items[i]
		if !isMultiKueueCapacityProvider(cp) {
			continue
		}
		if name, ok := providerConfigName(cp); configNames == nil || (ok && configNames.Has(name)) {
			requests = append(requests, reconcile.Request{Name: cp.Name})
		}
	}
	return requests
}

func isMultiKueueCapacityProvider(cp *kueuealpha.CapacityProvider) bool {
	return cp.Spec.ControllerName == kueuealpha.MultiKueueCapacityProviderControllerName
}

// providerConfigName returns the name of the MultiKueueCapacityProviderConfig that spec.parameters refers to.
func providerConfigName(cp *kueuealpha.CapacityProvider) (string, bool) {
	p := cp.Spec.Parameters
	if p == nil || p.APIGroup != kueuealpha.SchemeGroupVersion.Group || p.Kind != kueuealpha.MultiKueueCapacityProviderConfigKind {
		return "", false
	}
	return p.Name, true
}
