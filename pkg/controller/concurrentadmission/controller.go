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

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
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
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/pkg/workload/concurrentadmission"
)

const (
	ConcurrentAdmissionController = "concurrent-admission-controller"
	ReasonCreatedVariant          = "CreatedVariant"
	ReasonActivatedVariant        = "ActivatedVariant"
	ReasonDeactivatedVariant      = "DeactivatedVariant"
)

type variantReconciler struct {
	logName     string
	queues      *qcache.Manager
	client      client.Client
	recorder    record.EventRecorder
	roleTracker *roletracker.RoleTracker
	clock       clock.Clock
}

var _ reconcile.Reconciler = (*variantReconciler)(nil)
var _ predicate.TypedPredicate[*kueue.Workload] = (*variantReconciler)(nil)

func newVariantReconciler(c client.Client, queues *qcache.Manager, recorder record.EventRecorder, roleTracker *roletracker.RoleTracker) *variantReconciler {
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
	recorder := mgr.GetEventRecorderFor(ConcurrentAdmissionController)
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
		WithOptions(controller.Options{
			NeedLeaderElection:      new(false),
			MaxConcurrentReconciles: mgr.GetControllerOptions().GroupKindConcurrency[kueue.GroupVersion.WithKind("Workload").GroupKind().String()],
		}).
		WithLogConstructor(roletracker.NewLogConstructor(r.roleTracker, ConcurrentAdmissionController)).
		Complete(r)
}

// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/finalizers,verbs=update

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

	if len(variants) < len(flavorOrder) {
		log.V(3).Info("Too few variants, creating new ones", "desired", len(flavorOrder), "actual", len(variants))
		if err := r.createVariants(ctx, parent, variants, flavorOrder); err != nil {
			return ctrl.Result{}, err
		}
	}
	if len(variants) == len(flavorOrder) {
		log.V(3).Info("Desired number of variants, no action needed", "desired", len(flavorOrder), "actual", len(variants))
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
	return ctrl.Result{}, nil
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

func (r *variantReconciler) createVariants(ctx context.Context, parent *kueue.Workload, variants []kueue.Workload, resourceFlavors map[kueue.ResourceFlavorReference]int) error {
	log := ctrl.LoggerFrom(ctx)
	for flavor := range resourceFlavors {
		if r.hasVariantWithFlavor(ctx, variants, flavor) {
			continue
		}

		variant := generateVariant(parent, flavor)
		log.V(3).Info("Creating variant for flavor", "flavor", flavor, "variant", variant.Name)

		// Set the owner reference to the parent workload
		if err := ctrl.SetControllerReference(parent, variant, r.client.Scheme()); err != nil {
			log.V(3).Info("Failed to set owner reference for variant", "variant", klog.KObj(variant), "parent", klog.KObj(parent), "error", err)
			return err
		}
		if err := r.client.Create(ctx, variant); err != nil {
			log.V(3).Info("Failed to create variant", "variant", klog.KObj(variant), "error", err)
			return err
		}
		log.V(3).Info("Variant created", "variant", klog.KObj(variant), "flavor", flavor)
		r.recorder.Eventf(parent, corev1.EventTypeNormal, ReasonCreatedVariant, "Variant Workload %q created", klog.KObj(variant))
	}
	return nil
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
	delete(variant.Labels, constants.ConcurrentAdmissionParentLabelKey)
	metav1.SetMetaDataAnnotation(&variant.ObjectMeta, constants.WorkloadAllowedResourceFlavorAnnotation, string(flavor))
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
				if !workload.IsEvicted(parent) {
					log.V(2).Info("Evicting parent", "parent", klog.KObj(parent))
					err := workload.PatchAdmissionStatus(ctx, r.client, parent, r.clock, func(w *kueue.Workload) (bool, error) {
						return workload.SetEvictedCondition(w, r.clock.Now(), "VariantEvicted", "Admitted variant was evicted"), nil
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
	return workload.PatchAdmissionStatus(ctx, r.client, wl, r.clock, func(w *kueue.Workload) (bool, error) {
		setRequeued := (evCond.Reason == kueue.WorkloadEvictedByPreemption) ||
			(evCond.Reason == kueue.WorkloadEvictedDueToNodeFailures)
		updated := workload.SetRequeuedCondition(w, evCond.Reason, evCond.Message, setRequeued)
		if workload.UnsetQuotaReservationWithCondition(w, "Pending", evCond.Message, r.clock.Now()) {
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
		for i := range variants {
			v := &variants[i]
			if err := r.deactivateVariant(ctx, v, fmt.Sprintf("Parent Workload %q not active", klog.KObj(parent))); err != nil {
				return err
			}
		}
		return nil
	}

	admittedWl := getAdmittedVariant(variants)
	if admittedWl == nil {
		log.V(3).Info("No admitted variant, no need to deactivate any variant")
		return nil
	}
	// deactivate Variants below minPreferredFlavor if specified
	var minPreferredFlavor *kueue.ResourceFlavorReference
	if cq.Spec.ConcurrentAdmissionPolicy != nil && cq.Spec.ConcurrentAdmissionPolicy.Migration.Constraints != nil {
		minPreferredFlavor = cq.Spec.ConcurrentAdmissionPolicy.Migration.Constraints.MinPreferredFlavorName
	}
	if minPreferredFlavor != nil {
		log.V(3).Info("Deactivating variants below minPreferredFlavor", "minPreferredFlavor", *minPreferredFlavor)
		for i := range variants {
			v := &variants[i]
			if v.Name == admittedWl.Name {
				continue
			}
			if flavorOrder[concurrentadmission.GetVariantFlavor(v)] > flavorOrder[*minPreferredFlavor] {
				log.V(2).
					Info("Deactivating variant because it is below the minPreferredFlavor", "variant", klog.KObj(v), "flavor", concurrentadmission.GetVariantFlavor(v), "minPreferredFlavor", *minPreferredFlavor)
				if err := r.deactivateVariant(ctx, v, fmt.Sprintf("being below minPreferredFlavor: %q and another Variant admitted %q", *minPreferredFlavor, klog.KObj(admittedWl))); err != nil {
					return err
				}
			}
		}
	}
	// also deactivate Variants below the admitted variant regardless of minPreferredFlavor
	log.V(3).Info("Deactivating variants below the admitted variant", "admittedVariant", klog.KObj(admittedWl), "admittedFlavor", concurrentadmission.GetVariantFlavor(admittedWl))
	for i := range variants {
		v := &variants[i]
		if flavorOrder[concurrentadmission.GetVariantFlavor(v)] > flavorOrder[concurrentadmission.GetVariantFlavor(admittedWl)] {
			log.V(2).
				Info("Deactivating variant because it is below the admitted variant", "variant", klog.KObj(v), "flavor", concurrentadmission.GetVariantFlavor(v), "admittedFlavor", concurrentadmission.GetVariantFlavor(admittedWl))
			if err := r.deactivateVariant(ctx, v, fmt.Sprintf("being lower priority than admitted Variant %q", klog.KObj(admittedWl))); err != nil {
				return err
			}
		}
	}
	return nil
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
	// activate all variants that are at least at the minPreferredFlavor if specificed
	var minPreferredFlavor *kueue.ResourceFlavorReference
	if cq.Spec.ConcurrentAdmissionPolicy != nil && cq.Spec.ConcurrentAdmissionPolicy.Migration.Constraints != nil {
		minPreferredFlavor = cq.Spec.ConcurrentAdmissionPolicy.Migration.Constraints.MinPreferredFlavorName
	}
	if minPreferredFlavor != nil {
		for i := range variants {
			v := &variants[i]
			if flavorOrder[concurrentadmission.GetVariantFlavor(v)] <= flavorOrder[*minPreferredFlavor] &&
				flavorOrder[concurrentadmission.GetVariantFlavor(v)] < flavorOrder[concurrentadmission.GetVariantFlavor(admittedVariant)] {
				// activate the variant, the smaller or equal the flavor order is to the minPreferredFlavor, the higher the priority is
				if err := r.activateWl(ctx, v, fmt.Sprintf("being at least minPreferredFlavor: %q and higher priority than admitted Variant %q",
					*minPreferredFlavor, klog.KObj(admittedVariant))); err != nil {
					return err
				}
			}
		}
		return nil
	}
	// no minPreferredFlavor specified, so activate all variants that are below the admitted variant in the flavor order
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
}

func (r *variantReconciler) activateWl(ctx context.Context, wl *kueue.Workload, message string) error {
	if wl == nil || workload.IsActive(wl) {
		return nil
	}
	wl.Spec.Active = new(true)
	if err := r.client.Update(ctx, wl); err != nil {
		return err
	}
	r.recorder.Eventf(wl, corev1.EventTypeNormal, ReasonActivatedVariant, "Variant Workload activated due to %s", message)
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
	r.recorder.Eventf(wl, corev1.EventTypeNormal, ReasonDeactivatedVariant, "Variant Workload deactivated due to %s", message)
	return nil
}

func (r *variantReconciler) syncFinished(ctx context.Context, parent *kueue.Workload, variants []kueue.Workload) error {
	finishCond := apimeta.FindStatusCondition(parent.Status.Conditions, kueue.WorkloadFinished)
	for i := range variants {
		v := &variants[i]
		if err := workload.Finish(ctx, r.client, v, finishCond.Reason, finishCond.Message, r.clock); err != nil && !apierrors.IsNotFound(err) {
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
	if workload.IsFinished(parent) {
		return r.syncFinished(ctx, parent, variants)
	}

	admittedVariant := getAdmittedVariant(variants)
	switch {
	case admittedVariant == nil && workload.IsAdmitted(parent):
		log.V(2).Info("Parent admitted and no Variant is admitted, evicting parent", "parent", klog.KObj(parent))
		err := workload.PatchAdmissionStatus(ctx, r.client, parent, r.clock, func(wl *kueue.Workload) (bool, error) {
			return workload.SetEvictedCondition(wl, r.clock.Now(), "ConcurrentAdmission", "No variant is running"), nil
		})
		if err != nil {
			return fmt.Errorf("clearing admission: %w", err)
		}
	case admittedVariant != nil && !workload.IsAdmitted(parent):
		log.V(2).Info("Parent not admitted and Variant admitted, syncing their status", "parent", klog.KObj(parent), "Variant", klog.KObj(admittedVariant))
		log.V(3).Info("Syncing WaitForPodsReady condition")
		if err := workload.PatchAdmissionStatus(ctx, r.client, admittedVariant, r.clock, func(wl *kueue.Workload) (bool, error) {
			return r.syncPodsReadyCond(parent, wl), nil
		}); err != nil {
			return client.IgnoreNotFound(err)
		}
		if err := workload.PatchAdmissionStatus(ctx, r.client, parent, r.clock, func(wl *kueue.Workload) (bool, error) {
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
		if err := workload.PatchAdmissionStatus(ctx, r.client, admittedVariant, r.clock, func(wl *kueue.Workload) (bool, error) {
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

func getAdmittedVariant(variants []kueue.Workload) *kueue.Workload {
	for i := range variants {
		v := &variants[i]
		if workload.IsAdmitted(v) {
			return v
		}
	}
	return nil
}
