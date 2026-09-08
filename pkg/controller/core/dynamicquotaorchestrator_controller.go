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

package core

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/go-logr/logr"
	"gopkg.in/inf.v0"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	utilresource "sigs.k8s.io/kueue/pkg/util/resource"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
)

const (
	dqoControllerName            = "dynamicquotaorchestrator-reconciler"
	dynamicQuotaOrchestratorKind = "DynamicQuotaOrchestrator"
)

type DynamicQuotaOrchestratorReconciler struct {
	client      client.Client
	roleTracker *roletracker.RoleTracker
	logName     string
}

type DynamicQuotaOrchestratorReconcilerOption func(*DynamicQuotaOrchestratorReconciler)

// WithDynamicQuotaOrchestratorRoleTracker configures the RoleTracker for the reconciler.
func WithDynamicQuotaOrchestratorRoleTracker(rt *roletracker.RoleTracker) DynamicQuotaOrchestratorReconcilerOption {
	return func(r *DynamicQuotaOrchestratorReconciler) {
		r.roleTracker = rt
	}
}

// NewDynamicQuotaOrchestratorReconciler instantiates a new DynamicQuotaOrchestrator reconciler.
func NewDynamicQuotaOrchestratorReconciler(client client.Client, opts ...DynamicQuotaOrchestratorReconcilerOption) *DynamicQuotaOrchestratorReconciler {
	r := &DynamicQuotaOrchestratorReconciler{
		client:  client,
		logName: dqoControllerName,
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

func (r *DynamicQuotaOrchestratorReconciler) logger() logr.Logger {
	return roletracker.WithReplicaRole(ctrl.Log.WithName(r.logName), r.roleTracker)
}

// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=dynamicquotaorchestrators,verbs=get;list;watch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=dynamicquotaorchestrators/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=capacityproviders,verbs=get;list;watch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=clusterqueues,verbs=get;list;watch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=clusterqueues/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=cohorts,verbs=get;list;watch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=cohorts/status,verbs=get;update;patch

// SetupWithManager registers the DynamicQuotaOrchestrator controller and its watches with the manager.
func (r *DynamicQuotaOrchestratorReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kueuealpha.DynamicQuotaOrchestrator{}).
		Watches(
			&kueuealpha.CapacityProvider{},
			handler.EnqueueRequestsFromMapFunc(r.mapCapacityProviderToDQOs),
		).
		Complete(r)
}

// mapCapacityProviderToDQOs maps a CapacityProvider event to reconcile requests for all DynamicQuotaOrchestrators referencing it.
func (r *DynamicQuotaOrchestratorReconciler) mapCapacityProviderToDQOs(ctx context.Context, obj client.Object) []ctrl.Request {
	capacityProvider, ok := obj.(*kueuealpha.CapacityProvider)
	if !ok || capacityProvider == nil {
		return nil
	}
	var orchestratorList kueuealpha.DynamicQuotaOrchestratorList
	if err := r.client.List(ctx, &orchestratorList, client.MatchingFields{
		indexer.DynamicQuotaOrchestratorCapacityProviderKey: capacityProvider.Name,
	}); err != nil {
		r.logger().Error(err, "Failed to list DynamicQuotaOrchestrators for CapacityProvider", "capacityProvider", capacityProvider.Name)
		return nil
	}
	requests := make([]ctrl.Request, 0, len(orchestratorList.Items))
	for _, orchestrator := range orchestratorList.Items {
		requests = append(requests, ctrl.Request{
			NamespacedName: types.NamespacedName{Name: orchestrator.Name},
		})
	}
	return requests
}

// Reconcile coordinates capacity discovery and quota distribution for a DynamicQuotaOrchestrator.
func (r *DynamicQuotaOrchestratorReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	if !features.Enabled(features.DynamicQuotaOrchestration) {
		return ctrl.Result{}, nil
	}

	log := ctrl.LoggerFrom(ctx)
	log.V(2).Info("Reconcile DynamicQuotaOrchestrator")

	var orchestrator kueuealpha.DynamicQuotaOrchestrator
	if err := r.client.Get(ctx, req.NamespacedName, &orchestrator); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !orchestrator.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	oldStatus := orchestrator.Status.DeepCopy()

	// Phase 1: Capacity Discovery
	if discoveryErr := r.reconcileDiscovery(ctx, &orchestrator); discoveryErr != nil {
		err := r.updateStatus(ctx, &orchestrator, oldStatus)
		return ctrl.Result{}, errors.Join(discoveryErr, err)
	}

	// Phase 2: Quota Distribution
	var distributionErr error
	switch {
	case orchestrator.Spec.CapacityDistribution == nil:
		apimeta.RemoveStatusCondition(&orchestrator.Status.Conditions, kueuealpha.DynamicQuotaOrchestratorDistributed)
	case orchestrator.Status.EffectiveCapacity == nil:
		apimeta.SetStatusCondition(&orchestrator.Status.Conditions, metav1.Condition{
			Type:               kueuealpha.DynamicQuotaOrchestratorDistributed,
			Status:             metav1.ConditionFalse,
			ObservedGeneration: orchestrator.Generation,
			Reason:             kueuealpha.DynamicQuotaOrchestratorReasonEffectiveCapacityNotComputed,
			Message:            "Capacity discovery not ready",
		})
	default:
		if err := r.reconcileDistribution(ctx, &orchestrator, orchestrator.Status.EffectiveCapacity); err != nil {
			log.Error(err, "Failed to distribute quotas")
			distributionErr = err
		}
	}

	if err := r.updateStatus(ctx, &orchestrator, oldStatus); err != nil {
		return ctrl.Result{}, err
	}
	if distributionErr != nil {
		return ctrl.Result{}, distributionErr
	}
	return ctrl.Result{}, nil
}

// reconcileDiscovery performs Phase 1 reconciliation: aggregates normalized capacities across referenced CapacityProviders.
func (r *DynamicQuotaOrchestratorReconciler) reconcileDiscovery(ctx context.Context, orchestrator *kueuealpha.DynamicQuotaOrchestrator) error {
	aggregatedCapacity := make(map[kueuealpha.ResourceFlavorReference]corev1.ResourceList)

	for _, providerContribution := range orchestrator.Spec.CapacityDiscovery.Providers {
		var capacityProvider kueuealpha.CapacityProvider
		if err := r.client.Get(ctx, types.NamespacedName{Name: string(providerContribution.Name)}, &capacityProvider); err != nil {
			if apierrors.IsNotFound(err) {
				r.setDiscoveryCondition(
					orchestrator,
					metav1.ConditionFalse,
					kueuealpha.DynamicQuotaOrchestratorReasonMisconfigured,
					fmt.Sprintf("CapacityProvider %q not found", providerContribution.Name),
				)
				orchestrator.Status.EffectiveCapacity = nil
				return nil
			}
			return err
		}

		if !apimeta.IsStatusConditionTrue(capacityProvider.Status.Conditions, kueuealpha.CapacityProviderCapacitySynchronized) {
			r.setDiscoveryCondition(
				orchestrator,
				metav1.ConditionFalse,
				kueuealpha.DynamicQuotaOrchestratorReasonProviderNotReady,
				fmt.Sprintf("CapacityProvider %q is not synchronized", providerContribution.Name),
			)
			orchestrator.Status.EffectiveCapacity = nil
			return nil
		}

		aggregateProviderCapacity(capacityProvider.Status.Capacity, capacityProvider.Spec.OrchestratedFlavors, providerContribution.EffectiveCapacityMultiplier, aggregatedCapacity)
	}

	effectiveCapacityFlavors := make([]kueuealpha.EffectiveCapacityFlavor, 0, len(aggregatedCapacity))
	for _, flavorName := range slices.Sorted(maps.Keys(aggregatedCapacity)) {
		effectiveCapacityFlavors = append(effectiveCapacityFlavors, kueuealpha.EffectiveCapacityFlavor{
			Name:      flavorName,
			Resources: aggregatedCapacity[flavorName],
		})
	}

	orchestrator.Status.EffectiveCapacity = &kueuealpha.EffectiveCapacity{
		Flavors: effectiveCapacityFlavors,
	}
	r.setDiscoveryCondition(orchestrator, metav1.ConditionTrue, kueuealpha.DynamicQuotaOrchestratorReasonComputed, "Aggregated capacity successfully computed")
	return nil
}

// aggregateProviderCapacity scales and adds flavor resource quantities from a single CapacityProvider into the running aggregated total,
// filtering exclusively by the flavors declared in the CapacityProvider's spec.orchestratedFlavors.
func aggregateProviderCapacity(
	capacity *kueuealpha.CapacityProviderNormalizedCapacity,
	orchestratedFlavors []kueuealpha.CapacityProviderOrchestratedFlavor,
	multiplier *resource.Quantity,
	aggregatedCapacity map[kueuealpha.ResourceFlavorReference]corev1.ResourceList,
) {
	if capacity == nil {
		return
	}
	allowedFlavors := sets.New[kueuealpha.ResourceFlavorReference]()
	for _, f := range orchestratedFlavors {
		allowedFlavors.Insert(f.Name)
	}
	for _, flavor := range capacity.Flavors {
		if !allowedFlavors.Has(flavor.Name) {
			continue
		}
		res := flavor.Resources
		if len(res) == 0 {
			continue
		}
		if multiplier != nil {
			res = make(corev1.ResourceList, len(flavor.Resources))
			for k, v := range flavor.Resources {
				res[k] = utilresource.MultiplyQuantity(v, *multiplier)
			}
		}
		aggregatedCapacity[flavor.Name] = utilresource.MergeResourceListKeepSum(aggregatedCapacity[flavor.Name], res)
	}
}

// reconcileDistribution performs Phase 2 reconciliation: validates subtree conflicts, resolves the target hierarchy, and distributes effective quotas.
func (r *DynamicQuotaOrchestratorReconciler) reconcileDistribution(ctx context.Context, orchestrator *kueuealpha.DynamicQuotaOrchestrator, effectiveCapacity *kueuealpha.EffectiveCapacity) error {
	rootRef := orchestrator.Spec.CapacityDistribution.SubtreeRootQuotaRef
	if rootRef.Kind != kueuealpha.CohortSubtreeRootRefKind && rootRef.Kind != kueuealpha.ClusterQueueSubtreeRootRefKind {
		r.setDistributionCondition(orchestrator, metav1.ConditionFalse, kueuealpha.DynamicQuotaOrchestratorReasonMisconfigured, fmt.Sprintf("unsupported subtree root kind %q", rootRef.Kind))
		return nil
	}

	otherOrchestrators, err := r.listDistributingDQOs(ctx)
	if err != nil {
		return err
	}

	conflictMsg, err := r.findConflictingDistributingDQO(ctx, orchestrator, otherOrchestrators)
	if err != nil {
		return err
	}
	if conflictMsg != "" {
		r.setDistributionCondition(orchestrator, metav1.ConditionFalse, kueuealpha.DynamicQuotaOrchestratorReasonConflictingDynamicQuotaOrchestrator, conflictMsg)
		return nil
	}

	targetClusterQueues, targetCohorts, err := r.resolveSubtree(ctx, rootRef)
	if err != nil {
		if apierrors.IsNotFound(err) {
			r.setDistributionCondition(orchestrator, metav1.ConditionFalse, kueuealpha.DynamicQuotaOrchestratorReasonMisconfigured, fmt.Sprintf("%s %q not found", rootRef.Kind, rootRef.Name))
			return nil
		}
		return err
	}

	if err := r.findOwnershipConflict(ctx, orchestrator, targetClusterQueues, targetCohorts, otherOrchestrators); err != nil {
		r.setDistributionCondition(orchestrator, metav1.ConditionFalse, kueuealpha.DynamicQuotaOrchestratorReasonEffectiveQuotasConflict, err.Error())
		return nil
	}

	allocatedQuantities := calculateAllocations(effectiveCapacity, targetCohorts, targetClusterQueues)
	if err := r.applyEffectiveQuotas(ctx, orchestrator.Name, targetClusterQueues, targetCohorts, allocatedQuantities); err != nil {
		return err
	}

	r.setDistributionCondition(orchestrator, metav1.ConditionTrue, kueuealpha.DynamicQuotaOrchestratorReasonQuotasDistributed, "Quotas successfully distributed")
	return nil
}

// listDistributingDQOs returns all DynamicQuotaOrchestrators currently configured for distribution.
func (r *DynamicQuotaOrchestratorReconciler) listDistributingDQOs(ctx context.Context) ([]kueuealpha.DynamicQuotaOrchestrator, error) {
	var list kueuealpha.DynamicQuotaOrchestratorList
	if err := r.client.List(ctx, &list, client.MatchingFields{
		indexer.DynamicQuotaOrchestratorIsDistributingKey: "true",
	}); err != nil {
		return nil, err
	}
	return list.Items, nil
}

// findConflictingDistributingDQO checks if another distributing DQO conflicts by being an ancestor or an older instance on the same root.
// It returns a non-empty conflict message string if a conflict is found, or an empty string if no conflict exists.
func (r *DynamicQuotaOrchestratorReconciler) findConflictingDistributingDQO(
	ctx context.Context,
	orchestrator *kueuealpha.DynamicQuotaOrchestrator,
	otherOrchestrators []kueuealpha.DynamicQuotaOrchestrator,
) (string, error) {
	currentRoot := orchestrator.Spec.CapacityDistribution.SubtreeRootQuotaRef
	for _, otherOrchestrator := range otherOrchestrators {
		if otherOrchestrator.Name == orchestrator.Name || !otherOrchestrator.DeletionTimestamp.IsZero() {
			continue
		}

		otherRoot := otherOrchestrator.Spec.CapacityDistribution.SubtreeRootQuotaRef
		isAncestor, err := r.isStrictAncestor(ctx, otherRoot, currentRoot)
		if err != nil {
			return "", err
		}
		if isAncestor {
			return fmt.Sprintf("Conflicts with ancestor DynamicQuotaOrchestrator %q", otherOrchestrator.Name), nil
		}

		if otherRoot == currentRoot && hasPrecedence(&otherOrchestrator, orchestrator) {
			return fmt.Sprintf("Conflicts with older DynamicQuotaOrchestrator %q", otherOrchestrator.Name), nil
		}
	}
	return "", nil
}

// findOwnershipConflict checks whether any target ClusterQueue or Cohort is currently managed by another active distributing orchestrator.
// If the target object is managed by a descendant orchestrator, the current ancestor orchestrator has higher precedence and is allowed to take over.
func (r *DynamicQuotaOrchestratorReconciler) findOwnershipConflict(
	ctx context.Context,
	orchestrator *kueuealpha.DynamicQuotaOrchestrator,
	targetClusterQueues []kueue.ClusterQueue,
	targetCohorts []kueue.Cohort,
	otherOrchestrators []kueuealpha.DynamicQuotaOrchestrator,
) error {
	currentRoot := orchestrator.Spec.CapacityDistribution.SubtreeRootQuotaRef
	for _, clusterQueue := range targetClusterQueues {
		if err := r.checkManagedConflict(
			ctx,
			orchestrator,
			currentRoot,
			kueuealpha.ClusterQueueSubtreeRootRefKind,
			clusterQueue.Name,
			clusterQueue.Status.EffectiveQuotas,
			otherOrchestrators,
		); err != nil {
			return err
		}
	}
	for _, cohort := range targetCohorts {
		if err := r.checkManagedConflict(ctx, orchestrator, currentRoot, kueuealpha.CohortSubtreeRootRefKind, cohort.Name, cohort.Status.EffectiveQuotas, otherOrchestrators); err != nil {
			return err
		}
	}
	return nil
}

// checkManagedConflict verifies whether an individual object's EffectiveQuotas is owned by another active distributing orchestrator.
// It returns an error if owned by another active orchestrator that is not a descendant or older instance on the same root,
// or nil if no conflict exists or if the reconciling orchestrator has precedence.
func (r *DynamicQuotaOrchestratorReconciler) checkManagedConflict(
	ctx context.Context,
	orchestrator *kueuealpha.DynamicQuotaOrchestrator,
	currentRoot kueuealpha.CapacityDistributionSubtreeRootRef,
	kind kueuealpha.SubtreeRootRefKind,
	name string,
	quotas *kueue.EffectiveQuotaStatus,
	otherOrchestrators []kueuealpha.DynamicQuotaOrchestrator,
) error {
	if quotas == nil {
		return nil
	}
	ref := quotas.OrchestratorRef
	if ref.Name == orchestrator.Name {
		return nil
	}
	if ref.Kind != dynamicQuotaOrchestratorKind || (ref.APIGroup != "" && ref.APIGroup != kueuealpha.SchemeGroupVersion.Group) {
		return fmt.Errorf("%s %q already managed by %s/%s", kind, name, ref.Kind, ref.Name)
	}
	idx := slices.IndexFunc(otherOrchestrators, func(o kueuealpha.DynamicQuotaOrchestrator) bool {
		return o.Name == ref.Name
	})
	if idx == -1 {
		return nil
	}

	other := &otherOrchestrators[idx]
	if !other.DeletionTimestamp.IsZero() || other.Spec.CapacityDistribution == nil {
		return nil
	}

	distCond := apimeta.FindStatusCondition(other.Status.Conditions, kueuealpha.DynamicQuotaOrchestratorDistributed)
	if distCond != nil && distCond.Status == metav1.ConditionFalse {
		// The other orchestrator is deactivated; its effective quota can be taken over.
		return nil
	}
	isAncestor, err := r.isStrictAncestor(ctx, currentRoot, other.Spec.CapacityDistribution.SubtreeRootQuotaRef)
	if err != nil {
		return err
	}
	if isAncestor {
		// Current orchestrator is a strict ancestor of the managing orchestrator, so it has precedence.
		return nil
	}
	if other.Spec.CapacityDistribution.SubtreeRootQuotaRef == currentRoot && hasPrecedence(orchestrator, other) {
		// Current orchestrator has the same root and has precedence (older or UID tie-break).
		return nil
	}
	return fmt.Errorf("%s %q already managed by %s/%s", kind, name, ref.Kind, ref.Name)
}

// calculateAllocations distributes total capacity across all flavors and resources to participant nodes in the subtree.
func calculateAllocations(
	effectiveCapacity *kueuealpha.EffectiveCapacity,
	cohorts []kueue.Cohort,
	clusterQueues []kueue.ClusterQueue,
) map[quotaKey]map[string]resource.Quantity {
	participantsByKey := indexParticipantsByQuotaKey(cohorts, clusterQueues)
	allocatedQuantities := make(map[quotaKey]map[string]resource.Quantity)
	for _, flavor := range effectiveCapacity.Flavors {
		for resourceName, totalCapacity := range flavor.Resources {
			key := quotaKey{flavor: flavor.Name, resource: resourceName}
			allocatedQuantities[key] = distributeCapacityProportionally(resourceName, totalCapacity, participantsByKey[key])
		}
	}
	return allocatedQuantities
}

// applyEffectiveQuotas updates status.effectiveQuotas on all target ClusterQueues and Cohorts in the subtree.
func (r *DynamicQuotaOrchestratorReconciler) applyEffectiveQuotas(
	ctx context.Context,
	orchestratorName string,
	targetClusterQueues []kueue.ClusterQueue,
	targetCohorts []kueue.Cohort,
	allocatedQuantities map[quotaKey]map[string]resource.Quantity,
) error {
	var errs []error

	// Step 1: Apply calculated effective quotas to all target ClusterQueues in the subtree.
	for i := range targetClusterQueues {
		clusterQueue := &targetClusterQueues[i]
		newEffectiveQuotas := buildEffectiveQuotas(
			orchestratorName,
			kueuealpha.ClusterQueueSubtreeRootRefKind,
			clusterQueue.Spec.ResourceGroups,
			allocatedQuantities,
			getParticipantID(kueuealpha.ClusterQueueSubtreeRootRefKind, clusterQueue.Name),
		)
		if !equality.Semantic.DeepEqual(clusterQueue.Status.EffectiveQuotas, newEffectiveQuotas) {
			clusterQueue.Status.EffectiveQuotas = newEffectiveQuotas
			if err := r.client.Status().Update(ctx, clusterQueue); err != nil {
				errs = append(errs, fmt.Errorf("updating ClusterQueue %q effectiveQuotas: %w", clusterQueue.Name, err))
			}
		}
	}

	// Step 2: Apply calculated effective quotas to all target Cohorts in the subtree.
	for i := range targetCohorts {
		cohort := &targetCohorts[i]
		newEffectiveQuotas := buildEffectiveQuotas(
			orchestratorName,
			kueuealpha.CohortSubtreeRootRefKind,
			cohort.Spec.ResourceGroups,
			allocatedQuantities,
			getParticipantID(kueuealpha.CohortSubtreeRootRefKind, cohort.Name),
		)
		if !equality.Semantic.DeepEqual(cohort.Status.EffectiveQuotas, newEffectiveQuotas) {
			cohort.Status.EffectiveQuotas = newEffectiveQuotas
			if err := r.client.Status().Update(ctx, cohort); err != nil {
				errs = append(errs, fmt.Errorf("updating Cohort %q effectiveQuotas: %w", cohort.Name, err))
			}
		}
	}

	return errors.Join(errs...)
}

type quotaKey struct {
	flavor   kueuealpha.ResourceFlavorReference
	resource corev1.ResourceName
}

type quotaParticipant struct {
	kind             kueuealpha.SubtreeRootRefKind
	name             string
	uid              types.UID
	specNominalQuota resource.Quantity
}

// getParticipantID returns a canonical identifier string for a participant Cohort or ClusterQueue.
func getParticipantID(kind kueuealpha.SubtreeRootRefKind, name string) string {
	return string(kind) + "/" + name
}

// indexParticipantsByQuotaKey indexes Cohorts and ClusterQueues by the (flavor, resource) pairs they declare in spec.resourceGroups.
func indexParticipantsByQuotaKey(cohorts []kueue.Cohort, clusterQueues []kueue.ClusterQueue) map[quotaKey][]quotaParticipant {
	index := make(map[quotaKey][]quotaParticipant)

	addParticipants := func(kind kueuealpha.SubtreeRootRefKind, name string, uid types.UID, rgs []kueue.ResourceGroup) {
		for _, rg := range rgs {
			for _, f := range rg.Flavors {
				for _, r := range f.Resources {
					key := quotaKey{flavor: kueuealpha.ResourceFlavorReference(f.Name), resource: r.Name}
					index[key] = append(index[key], quotaParticipant{
						kind:             kind,
						name:             name,
						uid:              uid,
						specNominalQuota: r.NominalQuota,
					})
				}
			}
		}
	}

	for _, cohort := range cohorts {
		addParticipants(kueuealpha.CohortSubtreeRootRefKind, cohort.Name, cohort.UID, cohort.Spec.ResourceGroups)
	}
	for _, cq := range clusterQueues {
		addParticipants(kueuealpha.ClusterQueueSubtreeRootRefKind, cq.Name, cq.UID, cq.Spec.ResourceGroups)
	}

	return index
}

type remainderEntry struct {
	participant quotaParticipant
	floor       *inf.Dec
	remainder   *inf.Dec
}

// distributeCapacityProportionally distributes total capacity among participants using the largest-remainder method
// for deterministic proportional allocation per KEP-12382:
//  1. Determines the resource scale and unit: milliCPU (1m, scale 3) for CPU; integer (scale 0) for other resources.
//  2. Calculates each participant's floor_i and exact remainder_i = (capacity * specNominal_i) - (floor_i * sumSpecNominal).
//  3. Calculates the unallocated surplus capacity: diff = capacity - sum(floor_i).
//  4. Sorts participants by remainder descending. Ties are broken deterministically by object UID (lexicographical).
//  5. Assigns final allocations, adding +1 capacity unit to each of the top N participants (where N = diff / unit), and records the result.
func distributeCapacityProportionally(
	resourceName corev1.ResourceName,
	capacity resource.Quantity,
	participants []quotaParticipant,
) map[string]resource.Quantity {
	result := make(map[string]resource.Quantity, len(participants))
	if len(participants) == 0 {
		return result
	}

	sumSpecNominalQuota := new(inf.Dec)
	for _, p := range participants {
		sumSpecNominalQuota.Add(sumSpecNominalQuota, p.specNominalQuota.AsDec())
	}

	if sumSpecNominalQuota.Sign() <= 0 {
		for _, p := range participants {
			result[getParticipantID(p.kind, p.name)] = *resource.NewQuantity(0, capacity.Format)
		}
		return result
	}

	// Step 1: Determine the resource scale and unit.
	// Per KEP-12382, CPU is distributed in milliCPUs (1m, scale 3) because container CPU requests in Kubernetes
	// are fractional down to millicores. Other resources (e.g. memory in bytes, GPUs, or scalar items) use integer units (scale 0).
	var scale inf.Scale
	if resourceName == corev1.ResourceCPU {
		scale = 3
	}
	unitDec := inf.NewDec(1, scale)

	capacityAtScale := new(inf.Dec).Round(capacity.AsDec(), scale, inf.RoundDown)

	// Step 2: Calculate floor_i and exact remainder_i = (capacity * specNominal_i) - (floor_i * sumSpecNominal).
	entries := make([]remainderEntry, len(participants))
	sumFloors := new(inf.Dec)

	for i, p := range participants {
		idealNumerator := new(inf.Dec).Mul(capacityAtScale, p.specNominalQuota.AsDec())
		floor := new(inf.Dec).QuoRound(idealNumerator, sumSpecNominalQuota, scale, inf.RoundDown)

		floorTimesSum := new(inf.Dec).Mul(floor, sumSpecNominalQuota)
		remainder := new(inf.Dec).Sub(idealNumerator, floorTimesSum)

		entries[i] = remainderEntry{participant: p, floor: floor, remainder: remainder}
		sumFloors.Add(sumFloors, floor)
	}

	// Step 3: Calculate unallocated surplus capacity (diff) and number of surplus units (surplusUnits).
	// Mathematically, 0 <= surplusUnits < len(entries) is guaranteed by the largest-remainder method.
	diff := new(inf.Dec).Sub(capacityAtScale, sumFloors)
	surplusUnits := int(diff.UnscaledBig().Int64())

	// Step 4: Sort participants by remainder descending, breaking ties deterministically by object UID per KEP-12382.
	slices.SortFunc(entries, func(a, b remainderEntry) int {
		if cmp := b.remainder.Cmp(a.remainder); cmp != 0 {
			return cmp
		}
		return strings.Compare(string(a.participant.uid), string(b.participant.uid))
	})

	// Step 5: Assign final allocations, adding +1 capacity unit to each of the top N (surplusUnits) participants, and record them in result.
	for i, entry := range entries {
		allocated := entry.floor
		if i < surplusUnits {
			allocated = new(inf.Dec).Add(allocated, unitDec)
		}
		result[getParticipantID(entry.participant.kind, entry.participant.name)] = *resource.NewDecimalQuantity(*allocated, capacity.Format)
	}

	return result
}

// buildEffectiveQuotas creates an EffectiveQuotaStatus for a participant with its allocated nominal quotas.
// For ClusterQueues, any non-null lendingLimit is capped at the effective nominalQuota per KEP-12382.
func buildEffectiveQuotas(
	orchestratorName string,
	kind kueuealpha.SubtreeRootRefKind,
	specResourceGroups []kueue.ResourceGroup,
	allocatedQuantities map[quotaKey]map[string]resource.Quantity,
	participantID string,
) *kueue.EffectiveQuotaStatus {
	if len(specResourceGroups) == 0 {
		return &kueue.EffectiveQuotaStatus{
			OrchestratorRef: kueue.EffectiveQuotaStatusOrchestratorRef{
				APIGroup: kueuealpha.SchemeGroupVersion.Group,
				Kind:     dynamicQuotaOrchestratorKind,
				Name:     orchestratorName,
			},
			ResourceGroups: []kueue.ResourceGroup{},
		}
	}
	effectiveResourceGroups := make([]kueue.ResourceGroup, len(specResourceGroups))
	for i, resourceGroup := range specResourceGroups {
		effectiveResourceGroups[i] = *resourceGroup.DeepCopy()
		for j, flavorQuotas := range effectiveResourceGroups[i].Flavors {
			for k, resourceQuota := range flavorQuotas.Resources {
				key := quotaKey{
					flavor:   kueuealpha.ResourceFlavorReference(flavorQuotas.Name),
					resource: resourceQuota.Name,
				}
				if participantAllocations, found := allocatedQuantities[key]; found {
					if allocated, ok := participantAllocations[participantID]; ok {
						effectiveResourceGroups[i].Flavors[j].Resources[k].NominalQuota = allocated
						if kind == kueuealpha.ClusterQueueSubtreeRootRefKind && resourceQuota.LendingLimit != nil {
							if resourceQuota.LendingLimit.Cmp(allocated) > 0 {
								cappedLimit := allocated.DeepCopy()
								effectiveResourceGroups[i].Flavors[j].Resources[k].LendingLimit = &cappedLimit
							}
						}
					}
				}
			}
		}
	}

	return &kueue.EffectiveQuotaStatus{
		OrchestratorRef: kueue.EffectiveQuotaStatusOrchestratorRef{
			APIGroup: kueuealpha.SchemeGroupVersion.Group,
			Kind:     dynamicQuotaOrchestratorKind,
			Name:     orchestratorName,
		},
		ResourceGroups: effectiveResourceGroups,
	}
}

// resolveSubtree traverses the quota hierarchy downwards from the specified root reference to find all member ClusterQueues and Cohorts.
func (r *DynamicQuotaOrchestratorReconciler) resolveSubtree(
	ctx context.Context,
	rootRef kueuealpha.CapacityDistributionSubtreeRootRef,
) ([]kueue.ClusterQueue, []kueue.Cohort, error) {
	if rootRef.Kind == kueuealpha.ClusterQueueSubtreeRootRefKind {
		var clusterQueue kueue.ClusterQueue
		if err := r.client.Get(ctx, types.NamespacedName{Name: rootRef.Name}, &clusterQueue); err != nil {
			return nil, nil, err
		}
		return []kueue.ClusterQueue{clusterQueue}, nil, nil
	}

	if rootRef.Kind == kueuealpha.CohortSubtreeRootRefKind {
		var rootCohort kueue.Cohort
		if err := r.client.Get(ctx, types.NamespacedName{Name: rootRef.Name}, &rootCohort); err != nil {
			return nil, nil, err
		}

		targetCohorts := []kueue.Cohort{rootCohort}
		var targetClusterQueues []kueue.ClusterQueue
		cohortQueue := []string{rootCohort.Name}
		visitedCohorts := sets.New(rootCohort.Name)

		for len(cohortQueue) > 0 {
			currentCohortName := cohortQueue[0]
			cohortQueue = cohortQueue[1:]

			// Find direct ClusterQueues under this cohort using indexer
			var cqList kueue.ClusterQueueList
			if err := r.client.List(ctx, &cqList, client.MatchingFields{
				indexer.ClusterQueueCohortKey: currentCohortName,
			}); err != nil {
				return nil, nil, err
			}
			targetClusterQueues = append(targetClusterQueues, cqList.Items...)

			// Find direct child Cohorts under this cohort using indexer
			var childCohortList kueue.CohortList
			if err := r.client.List(ctx, &childCohortList, client.MatchingFields{
				indexer.CohortParentKey: currentCohortName,
			}); err != nil {
				return nil, nil, err
			}
			for _, child := range childCohortList.Items {
				if !visitedCohorts.Has(child.Name) {
					visitedCohorts.Insert(child.Name)
					targetCohorts = append(targetCohorts, child)
					cohortQueue = append(cohortQueue, child.Name)
				}
			}
		}

		return targetClusterQueues, targetCohorts, nil
	}

	return nil, nil, fmt.Errorf("unsupported subtree root kind %q", rootRef.Kind)
}

// isStrictAncestor returns true if candidate is a strict ancestor of target in the cohort hierarchy.
func (r *DynamicQuotaOrchestratorReconciler) isStrictAncestor(
	ctx context.Context,
	candidate kueuealpha.CapacityDistributionSubtreeRootRef,
	target kueuealpha.CapacityDistributionSubtreeRootRef,
) (bool, error) {
	if candidate.Kind != kueuealpha.CohortSubtreeRootRefKind || candidate == target {
		return false, nil
	}

	currentName := target.Name
	if target.Kind == kueuealpha.ClusterQueueSubtreeRootRefKind {
		var cq kueue.ClusterQueue
		if err := r.client.Get(ctx, types.NamespacedName{Name: target.Name}, &cq); err != nil {
			return false, client.IgnoreNotFound(err)
		}
		if cq.Spec.CohortName == "" {
			return false, nil
		}
		if string(cq.Spec.CohortName) == candidate.Name {
			return true, nil
		}
		currentName = string(cq.Spec.CohortName)
	}

	visited := sets.New(currentName)
	for currentName != "" {
		var cohort kueue.Cohort
		if err := r.client.Get(ctx, types.NamespacedName{Name: currentName}, &cohort); err != nil {
			return false, client.IgnoreNotFound(err)
		}
		parentName := string(cohort.Spec.ParentName)
		if parentName == "" || visited.Has(parentName) {
			return false, nil
		}
		if parentName == candidate.Name {
			return true, nil
		}
		visited.Insert(parentName)
		currentName = parentName
	}

	return false, nil
}

// hasPrecedence returns true if a has precedence over b (i.e. created earlier, breaking ties with UID).
func hasPrecedence(a, b *kueuealpha.DynamicQuotaOrchestrator) bool {
	if a.CreationTimestamp.Before(&b.CreationTimestamp) {
		return true
	}
	if b.CreationTimestamp.Before(&a.CreationTimestamp) {
		return false
	}
	return a.UID < b.UID
}

// setDiscoveryCondition sets the EffectiveCapacityComputed condition on the orchestrator status.
func (r *DynamicQuotaOrchestratorReconciler) setDiscoveryCondition(orchestrator *kueuealpha.DynamicQuotaOrchestrator, status metav1.ConditionStatus, reason, message string) {
	apimeta.SetStatusCondition(&orchestrator.Status.Conditions, metav1.Condition{
		Type:               kueuealpha.DynamicQuotaOrchestratorEffectiveCapacityComputed,
		Status:             status,
		ObservedGeneration: orchestrator.Generation,
		Reason:             reason,
		Message:            message,
	})
}

// setDistributionCondition sets the Distributed condition on the orchestrator status.
func (r *DynamicQuotaOrchestratorReconciler) setDistributionCondition(orchestrator *kueuealpha.DynamicQuotaOrchestrator, status metav1.ConditionStatus, reason, message string) {
	apimeta.SetStatusCondition(&orchestrator.Status.Conditions, metav1.Condition{
		Type:               kueuealpha.DynamicQuotaOrchestratorDistributed,
		Status:             status,
		ObservedGeneration: orchestrator.Generation,
		Reason:             reason,
		Message:            message,
	})
}
func (r *DynamicQuotaOrchestratorReconciler) updateStatus(ctx context.Context, orchestrator *kueuealpha.DynamicQuotaOrchestrator, oldStatus *kueuealpha.DynamicQuotaOrchestratorStatus) error {
	if equality.Semantic.DeepEqual(oldStatus, &orchestrator.Status) {
		return nil
	}
	return r.client.Status().Update(ctx, orchestrator)
}
