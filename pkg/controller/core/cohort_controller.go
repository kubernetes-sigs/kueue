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

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	config "sigs.k8s.io/kueue/apis/config/v1beta1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	"sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/queue"
)

type CohortReconcilerOptions struct {
	FairSharingEnabled bool
}

type CohortReconcilerOption func(*CohortReconcilerOptions)

func CohortReconcilerWithFairSharing(enabled bool) CohortReconcilerOption {
	return func(o *CohortReconcilerOptions) {
		o.FairSharingEnabled = enabled
	}
}

// CohortReconciler is responsible for synchronizing the in-memory
// representation of Cohorts in cache.Cache and queue.Manager with
// Cohort Kubernetes objects.
type CohortReconciler struct {
	client             client.Client
	log                logr.Logger
	cache              *cache.Cache
	qManager           *queue.Manager
	cqUpdateCh         chan event.GenericEvent
	fairSharingEnabled bool
}

func NewCohortReconciler(
	client client.Client,
	cache *cache.Cache,
	qManager *queue.Manager,
	opts ...CohortReconcilerOption,
) *CohortReconciler {
	options := CohortReconcilerOptions{}
	for _, opt := range opts {
		opt(&options)
	}

	return &CohortReconciler{
		client:             client,
		log:                ctrl.Log.WithName("cohort-reconciler"),
		cache:              cache,
		qManager:           qManager,
		cqUpdateCh:         make(chan event.GenericEvent, updateChBuffer),
		fairSharingEnabled: options.FairSharingEnabled,
	}
}

func (r *CohortReconciler) SetupWithManager(mgr ctrl.Manager, cfg *config.Configuration) error {
	cqHandler := &cohortCqHandler{
		cache: r.cache,
	}
	return ctrl.NewControllerManagedBy(mgr).
		Named("cohort_controller").
		WatchesRawSource(source.TypedKind(
			mgr.GetCache(),
			&kueue.Cohort{},
			&handler.TypedEnqueueRequestForObject[*kueue.Cohort]{},
			r,
		)).
		WithOptions(controller.Options{NeedLeaderElection: ptr.To(false)}).
		WatchesRawSource(source.Channel(r.cqUpdateCh, cqHandler)).
		Complete(WithLeadingManager(mgr, r, &kueue.Cohort{}, cfg))
}

func (r *CohortReconciler) Create(event.TypedCreateEvent[*kueue.Cohort]) bool {
	return true
}

func (r *CohortReconciler) Update(e event.TypedUpdateEvent[*kueue.Cohort]) bool {
	log := r.log.WithValues("cohort", klog.KObj(e.ObjectNew))
	if equality.Semantic.DeepEqual(e.ObjectOld.Spec, e.ObjectNew.Spec) {
		log.V(2).Info("Skip Cohort update event as Cohort unchanged")
		return false
	}
	log.V(2).Info("Processing Cohort update event")
	return true
}

func (r *CohortReconciler) Delete(event.TypedDeleteEvent[*kueue.Cohort]) bool {
	return true
}

func (r *CohortReconciler) Generic(event.TypedGenericEvent[*kueue.Cohort]) bool {
	return true
}

//+kubebuilder:rbac:groups=kueue.x-k8s.io,resources=cohorts,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=kueue.x-k8s.io,resources=cohorts/status,verbs=get;update;patch

func (r *CohortReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	log.V(2).Info("Reconcile Cohort")

	var cohort kueue.Cohort
	if err := r.client.Get(ctx, req.NamespacedName, &cohort); err != nil {
		if apierrors.IsNotFound(err) {
			log.V(2).Info("Cohort is being deleted")
			r.cache.DeleteCohort(v1beta1.CohortReference(req.NamespacedName.Name))
			r.qManager.DeleteCohort(v1beta1.CohortReference(req.NamespacedName.Name))
		}
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	log.V(2).Info("Cohort is being created or updated", "resources", cohort.Spec.ResourceGroups)
	if err := r.cache.AddOrUpdateCohort(&cohort); err != nil {
		log.V(2).Error(err, "Error adding or updating cohort in the cache")
	}
	r.qManager.AddOrUpdateCohort(ctx, &cohort)

	err := r.updateCohortStatusIfChanged(ctx, &cohort)
	return ctrl.Result{}, err
}

func (r *CohortReconciler) updateCohortStatusIfChanged(ctx context.Context, cohort *kueue.Cohort) error {
	log := ctrl.LoggerFrom(ctx)

	oldStatus := cohort.Status.DeepCopy()

	stats, err := r.cache.CohortStats(cohort)
	if err != nil {
		log.Error(err, "Failed getting cohort usage from cache")
		return err
	}

	if r.fairSharingEnabled {
		metrics.ReportCohortWeightedShare(cohort.Name, stats.WeightedShare)
		if cohort.Status.FairSharing == nil {
			cohort.Status.FairSharing = &v1beta1.FairSharingStatus{}
		}
		cohort.Status.FairSharing.WeightedShare = stats.WeightedShare
	} else {
		cohort.Status.FairSharing = nil
	}

	if !equality.Semantic.DeepEqual(cohort.Status, oldStatus) {
		return r.client.Status().Update(ctx, cohort)
	}

	return nil
}

func (r *CohortReconciler) NotifyClusterQueueUpdate(oldCQ, newCQ *v1beta1.ClusterQueue) {
	// if clusterQueue is nil, it's a delete event.
	if newCQ == nil {
		r.cqUpdateCh <- event.GenericEvent{Object: oldCQ}
		return
	}

	r.cqUpdateCh <- event.GenericEvent{Object: newCQ}
}

type cohortCqHandler struct {
	cache *cache.Cache
}

func (h *cohortCqHandler) Create(context.Context, event.CreateEvent, workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

func (h *cohortCqHandler) Update(context.Context, event.UpdateEvent, workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

func (h *cohortCqHandler) Delete(context.Context, event.DeleteEvent, workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

func (h *cohortCqHandler) Generic(ctx context.Context, e event.GenericEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	cq, isCQ := e.Object.(*v1beta1.ClusterQueue)
	if !isCQ {
		return
	}

	ancestors, err := h.cache.ClusterQueueAncestors(cq)
	if err != nil {
		log := ctrl.LoggerFrom(ctx)
		log.Error(err, "Failed getting ancestors for cohort", "cohort", cq.Spec.Cohort)
	}
	for _, ancestor := range ancestors {
		q.Add(reconcile.Request{NamespacedName: types.NamespacedName{Name: string(ancestor)}})
	}
}
