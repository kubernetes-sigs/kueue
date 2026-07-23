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

package concurrentadmission

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	"k8s.io/utils/clock"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	"sigs.k8s.io/kueue/pkg/constants"
	controllerconsts "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/pkg/workload/concurrentadmission"
	workloadevict "sigs.k8s.io/kueue/pkg/workload/evict"
	workloadfinish "sigs.k8s.io/kueue/pkg/workload/finish"
	workloadpatching "sigs.k8s.io/kueue/pkg/workload/patching"
)

const (
	ConcurrentAdmissionController  = "concurrent-admission-controller"
	ReasonCreatedVariant           = "CreatedVariant"
	ReasonDeletedVariant           = "DeletedVariant"
	ReasonActivatedVariant         = "ActivatedVariant"
	ReasonDeactivatedVariant       = "DeactivatedVariant"
	ReasonPreemptionUngatedVariant = "PreemptionUngatedVariant"

	preemptionTimeout = 5 * time.Minute
)

type variantReconciler struct {
	logName     string
	queues      *qcache.Manager
	client      client.Client
	recorder    events.EventRecorder
	roleTracker *roletracker.RoleTracker
	clock       clock.Clock
}

var _ reconcile.Reconciler = (*variantReconciler)(nil)
var _ predicate.TypedPredicate[*kueue.Workload] = (*variantReconciler)(nil)

func newVariantReconciler(c client.Client, queues *qcache.Manager, recorder events.EventRecorder, roleTracker *roletracker.RoleTracker) *variantReconciler {
	return &variantReconciler{
		logName:     ConcurrentAdmissionController,
		client:      c,
		queues:      queues,
		recorder:    recorder,
		roleTracker: roleTracker,
		clock:       clock.RealClock{},
	}
}

func SetupControllers(mgr ctrl.Manager, queues *qcache.Manager, cfg *configapi.Configuration, roleTracker *roletracker.RoleTracker) (string, error) {
	recorder := mgr.GetEventRecorder(ConcurrentAdmissionController)
	variantRec := newVariantReconciler(mgr.GetClient(), queues, recorder, roleTracker)
	if ctrlName, err := variantRec.setupWithManager(mgr, cfg); err != nil {
		return ctrlName, err
	}
	return "", nil
}

func (r *variantReconciler) setupWithManager(mgr ctrl.Manager, cfg *configapi.Configuration) (string, error) {
	return ConcurrentAdmissionController, builder.TypedControllerManagedBy[reconcile.Request](mgr).
		Named(ConcurrentAdmissionController).
		WatchesRawSource(source.TypedKind(
			mgr.GetCache(),
			&kueue.Workload{},
			handler.TypedEnqueueRequestsFromMapFunc(func(_ context.Context, obj *kueue.Workload) []reconcile.Request {
				if concurrentadmission.IsParent(obj) {
					return []reconcile.Request{{NamespacedName: client.ObjectKeyFromObject(obj)}}
				}
				if concurrentadmission.IsVariant(obj) {
					return []reconcile.Request{{NamespacedName: client.ObjectKey{Namespace: obj.Namespace, Name: concurrentadmission.GetParentWorkloadName(obj)}}}
				}
				return nil
			}),
			r,
		)).
		WatchesRawSource(source.TypedKind(
			mgr.GetCache(),
			&kueue.ClusterQueue{},
			handler.TypedFuncs[*kueue.ClusterQueue, reconcile.Request]{
				UpdateFunc: func(ctx context.Context, e event.TypedUpdateEvent[*kueue.ClusterQueue], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
					for _, req := range r.parentsForClusterQueue(ctx, e.ObjectNew) {
						q.AddAfter(req, constants.UpdatesBatchPeriod)
					}
				},
			},
			clusterQueueFlavorsChanged(),
		)).
		WithOptions(controller.Options{
			NeedLeaderElection:      new(false),
			MaxConcurrentReconciles: mgr.GetControllerOptions().GroupKindConcurrency[kueue.SchemeGroupVersion.WithKind("Workload").GroupKind().String()],
		}).
		WithLogConstructor(roletracker.NewLogConstructor(r.roleTracker, ConcurrentAdmissionController)).
		Complete(r)
}

func clusterQueueFlavorsChanged() predicate.TypedPredicate[*kueue.ClusterQueue] {
	return predicate.TypedFuncs[*kueue.ClusterQueue]{
		CreateFunc:  func(event.TypedCreateEvent[*kueue.ClusterQueue]) bool { return false },
		DeleteFunc:  func(event.TypedDeleteEvent[*kueue.ClusterQueue]) bool { return false },
		GenericFunc: func(event.TypedGenericEvent[*kueue.ClusterQueue]) bool { return false },
		UpdateFunc: func(e event.TypedUpdateEvent[*kueue.ClusterQueue]) bool {
			return !slices.EqualFunc(e.ObjectOld.Spec.ResourceGroups, e.ObjectNew.Spec.ResourceGroups,
				func(a, b kueue.ResourceGroup) bool {
					return slices.EqualFunc(a.Flavors, b.Flavors,
						func(x, y kueue.FlavorQuotas) bool {
							return x.Name == y.Name
						})
				})
		},
	}
}

func (r *variantReconciler) parentsForClusterQueue(ctx context.Context, cq *kueue.ClusterQueue) []reconcile.Request {
	log := ctrl.LoggerFrom(ctx).WithValues("clusterQueue", klog.KObj(cq))
	lqList := &kueue.LocalQueueList{}
	if err := r.client.List(ctx, lqList, client.MatchingFields{indexer.QueueClusterQueueKey: cq.Name}); err != nil {
		log.Error(err, "Failed to list LocalQueues for ClusterQueue")
		return nil
	}
	var requests []reconcile.Request
	for i := range lqList.Items {
		lq := &lqList.Items[i]
		wlList := &kueue.WorkloadList{}
		if err := r.client.List(ctx, wlList,
			client.InNamespace(lq.Namespace),
			client.MatchingFields{indexer.WorkloadQueueKey: lq.Name},
			client.MatchingLabels{controllerconsts.ConcurrentAdmissionParentLabelKey: "true"},
		); err != nil {
			log.Error(err, "Failed to list parent Workloads for LocalQueue", "localQueue", klog.KObj(lq))
			continue
		}
		for j := range wlList.Items {
			requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&wlList.Items[j])})
		}
	}
	return requests
}

// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/finalizers,verbs=update
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=clusterqueues,verbs=get;list;watch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=localqueues,verbs=get;list;watch

func (r *variantReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	log.V(2).Info("Reconcile Workload")

	parent, variants, cq, err := r.getFamilyAndClusterQueue(ctx, req)
	if err != nil {
		return ctrl.Result{}, err
	}
	if parent == nil {
		return ctrl.Result{}, nil
	}

	flavorOrder := make(map[kueue.ResourceFlavorReference]int, len(cq.Spec.ResourceGroups[0].Flavors))
	for i, flavor := range cq.Spec.ResourceGroups[0].Flavors {
		flavorOrder[flavor.Name] = i
	}
	variants = sortVariantsByFlavorOrder(variants, flavorOrder)

	log = log.WithValues("clusterQueue", cq.Name)
	log.V(2).Info("Found Workload family and ClusterQueue", "variants", klog.KObjSlice(variants))

	// TODO: If ConcurrentAdmission is no longer enabled for this CQ, delete parent and variants.

	log.V(3).Info("Reconciling variants against ClusterQueue flavors", "desired", len(flavorOrder), "actual", len(variants))
	if err := r.createVariants(ctx, parent, variants, cq.Spec.ResourceGroups[0].Flavors); err != nil {
		return ctrl.Result{}, err
	}
	variants, err = r.deleteStaleVariants(ctx, parent, variants, flavorOrder)
	if err != nil {
		log.Error(err, "Failed to delete stale variants")
		return ctrl.Result{}, err
	}

	log.V(3).Info("Syncing variants if needed")
	parentEvicted, err := r.syncVariantEvictionStatus(ctx, parent, variants)
	if err != nil {
		log.Error(err, "Failed to sync variant eviction status")
		return ctrl.Result{}, err
	}
	if parentEvicted {
		return ctrl.Result{}, nil
	}

	log.V(3).Info("Deactivating variants if needed")
	if err := r.deactivateVariants(ctx, parent, variants, cq, flavorOrder); err != nil {
		log.Error(err, "Failed to deactivate variants")
		return ctrl.Result{}, err
	}

	log.V(3).Info("Activating variants if needed")
	if err := r.activateVariants(ctx, parent, variants, cq, flavorOrder); err != nil {
		log.Error(err, "Failed to activate variants")
		return ctrl.Result{}, err
	}

	if err := r.syncAdmissionStatus(ctx, parent, variants); err != nil {
		log.Error(err, "Failed to sync admission status")
		return ctrl.Result{}, err
	}

	candidate, timeToWait := r.selectVariantToOpenPreemptionGate(ctx, variants)
	if candidate != nil {
		if err := r.openPreemptionGate(ctx, candidate); err != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{RequeueAfter: timeToWait}, nil
}

func (r *variantReconciler) getFamilyAndClusterQueue(ctx context.Context, req ctrl.Request) (*kueue.Workload, []kueue.Workload, *kueue.ClusterQueue, error) {
	log := ctrl.LoggerFrom(ctx)
	wl := &kueue.Workload{}
	if err := r.client.Get(ctx, req.NamespacedName, wl); err != nil {
		if apierrors.IsNotFound(err) {
			log.V(3).Info("Workload not found, might have been deleted", "workload", klog.KRef(req.Namespace, req.Name))
		}
		return nil, nil, nil, client.IgnoreNotFound(err)
	}

	if !concurrentadmission.IsParent(wl) {
		return nil, nil, nil, nil
	}

	variants, err := r.getVariantsForParent(ctx, wl)
	if err != nil {
		return nil, nil, nil, err
	}

	cq, err := r.getClusterQueue(ctx, wl)
	if err != nil {
		return nil, nil, nil, err
	}

	return wl, variants, cq, nil
}

func (r *variantReconciler) getVariantsForParent(ctx context.Context, parent *kueue.Workload) ([]kueue.Workload, error) {
	if !concurrentadmission.IsParent(parent) {
		return nil, fmt.Errorf("workload %s/%s is not a parent variant", parent.Namespace, parent.Name)
	}
	list := &kueue.WorkloadList{}
	if err := r.client.List(ctx, list, client.InNamespace(parent.Namespace), client.MatchingFields{indexer.OwnerReferenceUID: string(parent.UID)}); err != nil {
		return nil, err
	}
	return list.Items, nil
}

func (r *variantReconciler) getClusterQueue(ctx context.Context, wl *kueue.Workload) (*kueue.ClusterQueue, error) {
	cqName, ok := r.queues.ClusterQueueForWorkload(wl)
	if !ok {
		return nil, fmt.Errorf("could not find cluster queue for workload %s/%s", wl.Namespace, wl.Name)
	}
	cq := &kueue.ClusterQueue{}
	if err := r.client.Get(ctx, client.ObjectKey{Name: string(cqName)}, cq); err != nil {
		return nil, err
	}
	return cq, nil
}

func (r *variantReconciler) createVariants(ctx context.Context, parent *kueue.Workload, variants []kueue.Workload, resourceFlavors []kueue.FlavorQuotas) error {
	log := ctrl.LoggerFrom(ctx)
	for _, flavorQuota := range resourceFlavors {
		if r.hasVariantWithFlavor(ctx, variants, flavorQuota.Name) {
			continue
		}

		variant := generateVariant(parent, flavorQuota.Name)
		log.V(3).Info("Creating variant for flavor", "flavor", flavorQuota.Name, "variant", variant.Name)

		// Set the owner reference to the parent workload
		if err := ctrl.SetControllerReference(parent, variant, r.client.Scheme()); err != nil {
			log.V(3).Info("Failed to set owner reference for variant", "variant", klog.KObj(variant), "parent", klog.KObj(parent), "error", err)
			return err
		}
		if err := r.client.Create(ctx, variant); err != nil {
			if apierrors.IsAlreadyExists(err) {
				log.V(3).Info("Variant already exists", "variant", klog.KObj(variant))
				continue
			}
			log.V(3).Info("Failed to create variant", "variant", klog.KObj(variant), "error", err)
			return err
		}
		log.V(3).Info("Variant created", "variant", klog.KObj(variant), "flavor", flavorQuota.Name)
		r.recorder.Eventf(parent, nil, corev1.EventTypeNormal, ReasonCreatedVariant, ReasonCreatedVariant, "Variant Workload %q created", klog.KObj(variant))
	}
	return nil
}

func (r *variantReconciler) deleteStaleVariants(ctx context.Context, parent *kueue.Workload, variants []kueue.Workload, flavorOrder map[kueue.ResourceFlavorReference]int) ([]kueue.Workload, error) {
	log := ctrl.LoggerFrom(ctx)
	freshVariants := make([]kueue.Workload, 0, len(variants))
	for i := range variants {
		v := &variants[i]
		flavor := concurrentadmission.GetVariantFlavor(v)
		if _, ok := flavorOrder[flavor]; ok {
			freshVariants = append(freshVariants, *v)
			continue
		}
		log.V(2).Info("Deleting variant whose flavor is no longer in the ClusterQueue", "variant", klog.KObj(v), "flavor", flavor)
		if _, err := workload.Delete(ctx, r.client, v); err != nil && !apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("deleting stale variant %s: %w", klog.KObj(v), err)
		}
		r.recorder.Eventf(parent, nil, corev1.EventTypeNormal, ReasonDeletedVariant, ReasonDeletedVariant,
			"Variant Workload %q deleted; flavor %q no longer in ClusterQueue", klog.KObj(v), flavor)
	}
	return freshVariants, nil
}

func generateVariant(parent *kueue.Workload, flavor kueue.ResourceFlavorReference) *kueue.Workload {
	variant := &kueue.Workload{
		ObjectMeta: metav1.ObjectMeta{
			Name:          jobframework.GetWorkloadNameForVariant(parent.Name, parent.UID, parent.GroupVersionKind(), string(flavor)),
			Namespace:     parent.Namespace,
			Labels:        parent.Labels,
			Annotations:   parent.Annotations,
			ManagedFields: parent.ManagedFields,
		},
		Spec:   parent.Spec,
		Status: parent.Status,
	}
	variant.Spec.PreemptionGates = slices.Clone(variant.Spec.PreemptionGates)
	workload.EnsurePreemptionGateOnSpec(variant, controllerconsts.ConcurrentAdmissionPreemptionGate)
	delete(variant.Labels, controllerconsts.ConcurrentAdmissionParentLabelKey)
	metav1.SetMetaDataAnnotation(&variant.ObjectMeta, controllerconsts.WorkloadAllowedResourceFlavorAnnotation, string(flavor))
	return variant
}

func (r *variantReconciler) hasVariantWithFlavor(ctx context.Context, variants []kueue.Workload, flavor kueue.ResourceFlavorReference) bool {
	log := ctrl.LoggerFrom(ctx)
	log.V(3).Info("Checking if there is a variant with the flavor", "flavor", flavor)
	for _, v := range variants {
		if concurrentadmission.GetVariantFlavor(&v) == flavor {
			log.V(3).Info("Found a variant with the flavor", "variant", v.Name, "flavor", flavor)
			return true
		}
	}
	return false
}

func sortVariantsByFlavorOrder(variants []kueue.Workload, flavorOrder map[kueue.ResourceFlavorReference]int) []kueue.Workload {
	slices.SortFunc(variants, func(a, b kueue.Workload) int {
		aFlavor := concurrentadmission.GetVariantFlavor(&a) // we only support one flavor per variant for now
		bFlavor := concurrentadmission.GetVariantFlavor(&b)
		return flavorOrder[aFlavor] - flavorOrder[bFlavor]
	})
	return variants
}

func (r *variantReconciler) syncVariantEvictionStatus(ctx context.Context, parent *kueue.Workload, variants []kueue.Workload) (bool, error) {
	log := ctrl.LoggerFrom(ctx)
	log.V(3).Info("Syncing eviction status of variants", "parent", klog.KObj(parent))
	for i := range variants {
		v := &variants[i]
		evCond := apimeta.FindStatusCondition(v.Status.Conditions, kueue.WorkloadEvicted)
		if evCond != nil && evCond.Status == metav1.ConditionTrue && workload.HasQuotaReservation(v) {
			if workload.HasQuotaReservation(parent) {
				if !workloadevict.IsEvicted(parent) {
					log.V(2).Info("Evicting parent", "parent", klog.KObj(parent))
					err := workloadpatching.PatchAdmissionStatus(ctx, r.client, parent, r.clock, func(w *kueue.Workload) (bool, error) {
						return workloadevict.SetEvictedCondition(w, r.clock.Now(), "VariantEvicted", "Admitted variant was evicted"), nil
					})
					if err != nil {
						return false, fmt.Errorf("evicting parent: %w", err)
					}
				} else {
					log.V(2).Info("Parent already evicted, waiting for parent to lose quota reservation", "variant", klog.KObj(v), "parent", klog.KObj(parent))
				}
				return true, nil // Return to wait for parent to lose quota
			}

			log.V(2).Info("Parent has no quota, clearing variant admission", "variant", klog.KObj(v))
			if err := r.clearWorkloadAdmission(ctx, v, evCond); err != nil {
				return false, fmt.Errorf("clearing variant admission: %w", err)
			}
			return false, nil
		}
	}
	return false, nil
}

func (r *variantReconciler) clearWorkloadAdmission(ctx context.Context, wl *kueue.Workload, evCond *metav1.Condition) error {
	return workloadpatching.PatchAdmissionStatus(ctx, r.client, wl, r.clock, func(w *kueue.Workload) (bool, error) {
		setRequeued := (evCond.Reason == kueue.WorkloadEvictedByPreemption) ||
			(evCond.Reason == kueue.WorkloadEvictedDueToNodeFailures)
		updated := workload.SetRequeuedCondition(w, evCond.Reason, evCond.Message, setRequeued)
		reason := workload.UnadmittedWorkloadReasonWithFallback(
			kueue.WorkloadQuotaReservedReasonPendingEvaluation,
			kueue.WorkloadPending, //nolint:staticcheck // SA1019: fallback
		)
		if workload.UnsetQuotaReservationWithCondition(
			w,
			reason,
			evCond.Message,
			r.clock.Now(),
		) {
			updated = true
		}
		return updated, nil
	})
}

func (r *variantReconciler) deactivateVariant(ctx context.Context, v *kueue.Workload, message string) error {
	log := ctrl.LoggerFrom(ctx)
	if err := r.deactivateWl(ctx, v, message); err != nil {
		return err
	}
	// fetch the updated variant and unset quota
	if err := r.client.Get(ctx, client.ObjectKeyFromObject(v), v); err != nil {
		return err
	}
	if evCond := apimeta.FindStatusCondition(v.Status.Conditions, kueue.WorkloadEvicted); evCond != nil && evCond.Status == metav1.ConditionTrue {
		if workload.HasQuotaReservation(v) {
			log.V(3).Info("The variant is no longer active, clear the workloads admission")
			if err := r.clearWorkloadAdmission(ctx, v, evCond); err != nil {
				return fmt.Errorf("clearing admission: %w", err)
			}
		}
	}
	return nil
}

func (r *variantReconciler) deactivateMatchingVariants(ctx context.Context, variants []kueue.Workload, reason string, match func(*kueue.Workload) bool, logArgs ...any) error {
	log := ctrl.LoggerFrom(ctx)
	for i := range variants {
		v := &variants[i]
		if match != nil && !match(v) {
			continue
		}
		logFields := append([]any{"variant", klog.KObj(v), "flavor", concurrentadmission.GetVariantFlavor(v), "reason", reason}, logArgs...)
		log.V(2).Info("Deactivating variant", logFields...)
		if err := r.deactivateVariant(ctx, v, reason); err != nil {
			return err
		}
	}
	return nil
}

func (r *variantReconciler) deactivateVariants(
	ctx context.Context,
	parent *kueue.Workload,
	variants []kueue.Workload,
	cq *kueue.ClusterQueue,
	flavorOrder map[kueue.ResourceFlavorReference]int,
) error {
	log := ctrl.LoggerFrom(ctx)
	if !workload.IsActive(parent) {
		log.V(2).Info("Parent is not active, deactivating all variants", "parent", klog.KObj(parent))
		return r.deactivateMatchingVariants(
			ctx,
			variants,
			fmt.Sprintf("Parent Workload %q not active", klog.KObj(parent)),
			nil,
		)
	}

	admittedWl := getAdmittedVariant(variants)
	if admittedWl == nil {
		log.V(3).Info("No admitted variant, no need to deactivate any variant")
		return nil
	}
	switch migrationMode(cq) {
	case kueue.ConcurrentAdmissionRetainFirstAdmission:
		log.V(3).Info("RetainFirstAdmission mode, deactivating all variants except the admitted one", "admittedVariant", klog.KObj(admittedWl))
		return r.deactivateMatchingVariants(
			ctx,
			variants,
			fmt.Sprintf("RetainFirstAdmission: another Variant %q is admitted", klog.KObj(admittedWl)),
			func(v *kueue.Workload) bool { return v.Name != admittedWl.Name },
		)
	case kueue.ConcurrentAdmissionTryPreferredFlavors:
		// deactivate Variants below lastAcceptableFlavor if specified
		var lastAcceptableFlavor *kueue.ResourceFlavorReference
		if cq.Spec.ConcurrentAdmissionPolicy != nil && cq.Spec.ConcurrentAdmissionPolicy.Migration.Constraints != nil {
			lastAcceptableFlavor = cq.Spec.ConcurrentAdmissionPolicy.Migration.Constraints.LastAcceptableFlavorName
		}
		if lastAcceptableFlavor != nil {
			log.V(3).Info("Deactivating variants below lastAcceptableFlavor", "lastAcceptableFlavor", *lastAcceptableFlavor)
			err := r.deactivateMatchingVariants(
				ctx,
				variants,
				fmt.Sprintf("being below lastAcceptableFlavor: %q and another Variant admitted %q", *lastAcceptableFlavor, klog.KObj(admittedWl)),
				func(v *kueue.Workload) bool {
					return v.Name != admittedWl.Name &&
						flavorOrder[concurrentadmission.GetVariantFlavor(v)] > flavorOrder[*lastAcceptableFlavor]
				},
				"lastAcceptableFlavor", *lastAcceptableFlavor,
			)
			if err != nil {
				return err
			}
		}
		// also deactivate Variants below the admitted variant regardless of lastAcceptableFlavorName
		log.V(3).Info("Deactivating variants below the admitted variant", "admittedVariant", klog.KObj(admittedWl), "admittedFlavor", concurrentadmission.GetVariantFlavor(admittedWl))
		return r.deactivateMatchingVariants(
			ctx,
			variants,
			fmt.Sprintf("being lower priority than admitted Variant %q", klog.KObj(admittedWl)),
			func(v *kueue.Workload) bool {
				return flavorOrder[concurrentadmission.GetVariantFlavor(v)] > flavorOrder[concurrentadmission.GetVariantFlavor(admittedWl)]
			},
			"admittedFlavor", concurrentadmission.GetVariantFlavor(admittedWl),
		)
	default:
		return fmt.Errorf("unknown migration mode %q", migrationMode(cq))
	}
}

func (r *variantReconciler) activateVariants(ctx context.Context, parent *kueue.Workload, variants []kueue.Workload, cq *kueue.ClusterQueue, flavorOrder map[kueue.ResourceFlavorReference]int) error {
	log := ctrl.LoggerFrom(ctx)
	if !workload.IsActive(parent) {
		log.V(3).Info("Parent is not active, no needed to activate variants", "parent", klog.KObj(parent))
		return nil
	}
	admittedVariant := getAdmittedVariant(variants)
	if admittedVariant == nil {
		log.V(3).Info("No admitted variant, activating all variants")
		// no admitted variants so activate all variants if they are not active, case of preemption
		for i := range variants {
			v := &variants[i]
			if err := r.activateWl(ctx, v, "no other Variant being admitted"); err != nil {
				return err
			}
		}
		return nil
	}
	switch migrationMode(cq) {
	case kueue.ConcurrentAdmissionRetainFirstAdmission:
		log.V(3).Info("RetainFirstAdmission mode and a Variant is admitted, not activating any other variant", "admittedVariant", klog.KObj(admittedVariant))
		return nil
	case kueue.ConcurrentAdmissionTryPreferredFlavors:
		// activate all variants that are at least at the lastAcceptableFlavorName if specificed
		var lastAcceptableFlavor *kueue.ResourceFlavorReference
		if cq.Spec.ConcurrentAdmissionPolicy != nil && cq.Spec.ConcurrentAdmissionPolicy.Migration.Constraints != nil {
			lastAcceptableFlavor = cq.Spec.ConcurrentAdmissionPolicy.Migration.Constraints.LastAcceptableFlavorName
		}
		if lastAcceptableFlavor != nil {
			for i := range variants {
				v := &variants[i]
				if flavorOrder[concurrentadmission.GetVariantFlavor(v)] <= flavorOrder[*lastAcceptableFlavor] &&
					flavorOrder[concurrentadmission.GetVariantFlavor(v)] < flavorOrder[concurrentadmission.GetVariantFlavor(admittedVariant)] {
					// activate the variant, the smaller or equal the flavor order is to the lastAcceptableFlavor, the higher the priority is
					if err := r.activateWl(ctx, v, fmt.Sprintf("being at least lastAcceptableFlavor: %q and higher priority than admitted Variant %q",
						*lastAcceptableFlavor, klog.KObj(admittedVariant))); err != nil {
						return err
					}
				}
			}
			return nil
		}
		// no lastAcceptableFlavorName specified, so activate all variants that are below the admitted variant in the flavor order
		for i := range variants {
			v := &variants[i]
			if flavorOrder[concurrentadmission.GetVariantFlavor(v)] < flavorOrder[concurrentadmission.GetVariantFlavor(admittedVariant)] {
				// activate the variant, the smaller the flavor order is to the admitted variant, the higher the priority is
				if err := r.activateWl(ctx, v, fmt.Sprintf("being higher priority than admitted Variant %q", klog.KObj(admittedVariant))); err != nil {
					return err
				}
			}
		}
		return nil
	default:
		return fmt.Errorf("unknown migration mode %q", migrationMode(cq))
	}
}

func (r *variantReconciler) activateWl(ctx context.Context, wl *kueue.Workload, message string) error {
	if wl == nil || workload.IsActive(wl) {
		return nil
	}
	wl.Spec.Active = new(true)
	if err := r.client.Update(ctx, wl); err != nil {
		return err
	}
	r.recorder.Eventf(wl, nil, corev1.EventTypeNormal, ReasonActivatedVariant, ReasonActivatedVariant, "Variant Workload activated due to %s", message)
	return nil
}

func (r *variantReconciler) deactivateWl(ctx context.Context, wl *kueue.Workload, message string) error {
	if wl == nil || !workload.IsActive(wl) {
		return nil
	}
	wl.Spec.Active = new(false)
	if err := r.client.Update(ctx, wl); err != nil {
		return err
	}
	r.recorder.Eventf(wl, nil, corev1.EventTypeNormal, ReasonDeactivatedVariant, ReasonDeactivatedVariant, "Variant Workload deactivated due to %s", message)
	return nil
}

func (r *variantReconciler) syncFinished(ctx context.Context, parent *kueue.Workload, variants []kueue.Workload) error {
	finishCond := apimeta.FindStatusCondition(parent.Status.Conditions, kueue.WorkloadFinished)
	for i := range variants {
		v := &variants[i]
		if err := workloadfinish.Finish(ctx, r.client, v, finishCond.Reason, finishCond.Message, r.clock); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

func (r *variantReconciler) syncPodsReadyCond(parent, variant *kueue.Workload) bool {
	parentCond := apimeta.FindStatusCondition(parent.Status.Conditions, kueue.WorkloadPodsReady)
	if parentCond == nil {
		return false
	}
	return apimeta.SetStatusCondition(&variant.Status.Conditions, *parentCond)
}

func (r *variantReconciler) syncAdmissionStatus(ctx context.Context, parent *kueue.Workload, variants []kueue.Workload) error {
	log := ctrl.LoggerFrom(ctx)
	if workloadfinish.IsFinished(parent) {
		return r.syncFinished(ctx, parent, variants)
	}

	admittedVariant := getAdmittedVariant(variants)
	switch {
	case admittedVariant == nil && workload.IsAdmitted(parent):
		log.V(2).Info("Parent admitted and no Variant is admitted, evicting parent", "parent", klog.KObj(parent))
		err := workloadpatching.PatchAdmissionStatus(ctx, r.client, parent, r.clock, func(wl *kueue.Workload) (bool, error) {
			return workloadevict.SetEvictedCondition(wl, r.clock.Now(), "ConcurrentAdmission", "No variant is running"), nil
		})
		if err != nil {
			return fmt.Errorf("clearing admission: %w", err)
		}
	case admittedVariant != nil && !workload.IsAdmitted(parent):
		log.V(2).Info("Parent not admitted and Variant admitted, syncing their status", "parent", klog.KObj(parent), "Variant", klog.KObj(admittedVariant))
		log.V(3).Info("Syncing WaitForPodsReady condition")
		if err := workloadpatching.PatchAdmissionStatus(ctx, r.client, admittedVariant, r.clock, func(wl *kueue.Workload) (bool, error) {
			return r.syncPodsReadyCond(parent, wl), nil
		}); err != nil {
			return client.IgnoreNotFound(err)
		}
		if err := workloadpatching.PatchAdmissionStatus(ctx, r.client, parent, r.clock, func(wl *kueue.Workload) (bool, error) {
			workload.SetQuotaReservation(wl, admittedVariant.Status.Admission, r.clock)
			workload.SetAdmittedCondition(wl, r.clock.Now(), "Admitted", fmt.Sprintf("The variant %s is admitted", admittedVariant.Name))
			log.V(2).Info("Parent is not admitted but a variant is admitted, updating parent to admitted", "parent", klog.KObj(parent), "admittedVariant", klog.KObj(admittedVariant))
			return true, nil
		}); err != nil {
			return client.IgnoreNotFound(err)
		}
	case admittedVariant != nil && workload.IsAdmitted(parent):
		log.V(2).Info("Parent and Variant admitted, syncing their status", "parent", klog.KObj(parent), "Variant", klog.KObj(admittedVariant))
		log.V(3).Info("Syncing WaitForPodsReady condition")
		if err := workloadpatching.PatchAdmissionStatus(ctx, r.client, admittedVariant, r.clock, func(wl *kueue.Workload) (bool, error) {
			return r.syncPodsReadyCond(parent, wl), nil
		}); err != nil {
			return client.IgnoreNotFound(err)
		}

	case admittedVariant == nil && !workload.IsAdmitted(parent):
		log.V(2).Info("Parent and Variants are both not admitted, no sync needed", "parent", klog.KObj(parent))
	}
	return nil
}

func (r *variantReconciler) logger() logr.Logger {
	return roletracker.WithReplicaRole(ctrl.Log.WithName(r.logName), r.roleTracker)
}

func (r *variantReconciler) Generic(event.TypedGenericEvent[*kueue.Workload]) bool {
	return false
}

func (r *variantReconciler) Create(e event.TypedCreateEvent[*kueue.Workload]) bool {
	log := r.logger()
	log.V(2).Info("Create event for Workload", "workload", klog.KObj(e.Object))
	return r.shouldReconcile(e.Object)
}

func (r *variantReconciler) Update(e event.TypedUpdateEvent[*kueue.Workload]) bool {
	log := r.logger()
	log.V(2).Info("Update event for Workload", "workload", klog.KObj(e.ObjectNew))
	return r.shouldReconcile(e.ObjectNew)
}

func (r *variantReconciler) Delete(e event.TypedDeleteEvent[*kueue.Workload]) bool {
	log := r.logger()
	log.V(2).Info("Delete event for Workload", "workload", klog.KObj(e.Object))
	return r.shouldReconcile(e.Object)
}

func (r *variantReconciler) shouldReconcile(wl *kueue.Workload) bool {
	log := r.logger()
	if concurrentadmission.IsParent(wl) {
		log.V(3).Info("Workload is a parent variant, reconciling", "workload", klog.KObj(wl))
		return true
	}
	if concurrentadmission.IsVariant(wl) {
		log.V(3).Info("Workload is a variant, reconciling", "workload", klog.KObj(wl))
		return true
	}
	log.V(3).Info("Workload is neither a parent variant nor a variant, ignoring", "workload", klog.KObj(wl))
	return false
}

func (r *variantReconciler) selectVariantToOpenPreemptionGate(ctx context.Context, variants []kueue.Workload) (*kueue.Workload, time.Duration) {
	log := ctrl.LoggerFrom(ctx).WithValues("op", "selectVariantToOpenPreemptionGate")

	admissible := admissibleVariants(variants)
	candidate := firstCandidateVariant(log, admissible)
	if candidate == nil {
		log.V(4).Info("No candidate variant found for ungating preemptions")
		return nil, 0
	}

	lastUngateTime := latestOpenGateTime(log, admissible)
	if lastUngateTime == nil {
		log.V(4).Info("Found variant to ungate, no gate is currently open", "variantToUngate", klog.KObj(candidate))
		return candidate, preemptionTimeout
	}
	timeToWait := preemptionTimeout - r.clock.Since(lastUngateTime.Time)
	if timeToWait > 0 {
		log.V(4).Info("Single preemption timeout did not expire", "timeToWait", timeToWait, "nextVariantToUngate", klog.KObj(candidate))
		return nil, timeToWait
	}
	log.V(4).Info("Found variant to ungate", "variantToUngate", klog.KObj(candidate))
	return candidate, preemptionTimeout
}

func firstCandidateVariant(log logr.Logger, admissibleVariants []*kueue.Workload) *kueue.Workload {
	for _, wl := range admissibleVariants {
		if workload.HasOpenPreemptionGate(wl, controllerconsts.ConcurrentAdmissionPreemptionGate) {
			continue
		}
		wlLog := log.WithValues("candidateVariant", klog.KObj(wl), "flavor", concurrentadmission.GetVariantFlavor(wl))
		if workload.BlockedOnPreemptionGatesCondition(wl) == nil {
			wlLog.V(4).Info("Variant does not require preemption")
			continue
		}
		wlLog.V(4).Info("Variant is the candidate for ungating")
		return wl
	}
	return nil
}

func latestOpenGateTime(log logr.Logger, admissibleVariants []*kueue.Workload) *metav1.Time {
	var lastUngateTime *metav1.Time
	for _, wl := range admissibleVariants {
		openGate := workload.FindPreemptionGate(wl, controllerconsts.ConcurrentAdmissionPreemptionGate)
		if openGate == nil || openGate.Position != kueue.PreemptionGatePositionOpen {
			continue
		}
		if lastUngateTime == nil || openGate.LastTransitionTime.After(lastUngateTime.Time) {
			log.V(4).Info("Variant has the latest preemption ungating time", "openPreemptionGateVariant", klog.KObj(wl), "flavor", concurrentadmission.GetVariantFlavor(wl))
			lastUngateTime = &openGate.LastTransitionTime
		}
	}
	return lastUngateTime
}

func admissibleVariants(variants []kueue.Workload) []*kueue.Workload {
	var ret []*kueue.Workload
	for i := range variants {
		if workload.IsAdmissible(&variants[i]) {
			ret = append(ret, &variants[i])
		}
	}
	return ret
}

func (r *variantReconciler) openPreemptionGate(ctx context.Context, variant *kueue.Workload) error {
	log := ctrl.LoggerFrom(ctx)
	log.V(2).Info("Opening preemption gate for variant", "variant", klog.KObj(variant), "flavor", concurrentadmission.GetVariantFlavor(variant))
	var opened bool
	if err := workloadpatching.PatchAdmissionStatus(ctx, r.client, variant, r.clock, func(wl *kueue.Workload) (bool, error) {
		opened = workload.OpenPreemptionGate(wl, controllerconsts.ConcurrentAdmissionPreemptionGate, metav1.NewTime(r.clock.Now()))
		return opened, nil
	}, workloadpatching.WithRetryOnConflict()); err != nil {
		return fmt.Errorf("opening preemption gate on variant %s: %w", klog.KObj(variant), err)
	}
	if opened {
		r.recorder.Eventf(variant, nil, corev1.EventTypeNormal, ReasonPreemptionUngatedVariant, ReasonPreemptionUngatedVariant, "Opened preemption gate for variant workload %q", klog.KObj(variant))
	}
	return nil
}

func getAdmittedVariant(variants []kueue.Workload) *kueue.Workload {
	for i := range variants {
		v := &variants[i]
		if workload.IsAdmitted(v) {
			return v
		}
	}
	return nil
}

func migrationMode(cq *kueue.ClusterQueue) kueue.ConcurrentAdmissionMigrationMode {
	if cq.Spec.ConcurrentAdmissionPolicy == nil ||
		cq.Spec.ConcurrentAdmissionPolicy.Migration.Mode == "" {
		return kueue.ConcurrentAdmissionTryPreferredFlavors
	}
	return cq.Spec.ConcurrentAdmissionPolicy.Migration.Mode
}
