/*
Copyright 2023 The Kubernetes Authors.

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
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	config "sigs.k8s.io/kueue/apis/config/v1beta1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/queue"
	"sigs.k8s.io/kueue/pkg/util/slices"
)

type AdmissionCheckUpdateWatcher interface {
	NotifyAdmissionCheckUpdate(oldAc, newAc *kueue.AdmissionCheck)
}

// AdmissionCheckReconciler reconciles a AdmissionCheck object
type AdmissionCheckReconciler struct {
	log        logr.Logger
	qManager   *queue.Manager
	client     client.Client
	cache      *cache.Cache
	cqUpdateCh chan event.GenericEvent
	watchers   []AdmissionCheckUpdateWatcher
}

func NewAdmissionCheckReconciler(
	client client.Client,
	qMgr *queue.Manager,
	cache *cache.Cache,
) *AdmissionCheckReconciler {
	return &AdmissionCheckReconciler{
		log:        ctrl.Log.WithName("admissioncheck-reconciler"),
		qManager:   qMgr,
		client:     client,
		cache:      cache,
		cqUpdateCh: make(chan event.GenericEvent, updateChBuffer),
	}
}

// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=admissionchecks,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=admissionchecks/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=admissionchecks/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *AdmissionCheckReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ac := &kueue.AdmissionCheck{}

	if err := r.client.Get(ctx, req.NamespacedName, ac); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	log := log.FromContext(ctx).WithValues("admissionCheck", klog.KObj(ac))
	if ac.DeletionTimestamp.IsZero() {
		if controllerutil.AddFinalizer(ac, kueue.ResourceInUseFinalizerName) {
			if err := r.client.Update(ctx, ac); err != nil {
				return ctrl.Result{}, err
			}
			log.V(5).Info("Added finalizer")
		}
	} else {
		if controllerutil.ContainsFinalizer(ac, kueue.ResourceInUseFinalizerName) {
			if cqs := r.cache.ClusterQueuesUsingAdmissionCheck(ac.Name); len(cqs) != 0 {
				log.V(3).Info("admissionCheck is still in use", "ClusterQueues", cqs)
				// We avoid to return error here to prevent backoff requeue, which is passive and wasteful.
				// Instead, we drive the removal of finalizer by ClusterQueue Update/Delete events
				// when the admissionCheck is no longer in use.
				return ctrl.Result{}, nil
			}
			controllerutil.RemoveFinalizer(ac, kueue.ResourceInUseFinalizerName)
			if err := r.client.Update(ctx, ac); err != nil {
				return ctrl.Result{}, err
			}
			log.V(5).Info("Removed finalizer")
		}
	}
	return ctrl.Result{}, nil
}
func (r *AdmissionCheckReconciler) notifyWatchers(oldAc, newAc *kueue.AdmissionCheck) {
	for _, w := range r.watchers {
		w.NotifyAdmissionCheckUpdate(oldAc, newAc)
	}
}

func (r *AdmissionCheckReconciler) AddUpdateWatchers(watchers ...AdmissionCheckUpdateWatcher) {
	r.watchers = append(r.watchers, watchers...)
}

func (r *AdmissionCheckReconciler) Create(e event.CreateEvent) bool {
	ac, isAc := e.Object.(*kueue.AdmissionCheck)
	if !isAc {
		return false
	}
	defer r.notifyWatchers(nil, ac)
	r.log.WithValues("admissionCheck", klog.KObj(ac)).V(5).Info("Create event")
	if cqNames := r.cache.AddOrUpdateAdmissionCheck(ac); len(cqNames) > 0 {
		r.qManager.QueueInadmissibleWorkloads(context.Background(), cqNames)
	}
	return true
}

func (r *AdmissionCheckReconciler) Update(e event.UpdateEvent) bool {
	newAc, isAc := e.ObjectNew.(*kueue.AdmissionCheck)
	if !isAc {
		return false
	}
	oldAc, isAc := e.ObjectNew.(*kueue.AdmissionCheck)
	if !isAc {
		return false
	}
	defer r.notifyWatchers(oldAc, newAc)
	r.log.WithValues("admissionCheck", klog.KObj(newAc)).V(5).Info("Update event")
	if !newAc.DeletionTimestamp.IsZero() {
		return true
	}
	if cqNames := r.cache.AddOrUpdateAdmissionCheck(newAc); len(cqNames) > 0 {
		r.qManager.QueueInadmissibleWorkloads(context.Background(), cqNames)
	}
	return false
}

func (r *AdmissionCheckReconciler) Delete(e event.DeleteEvent) bool {
	ac, isAc := e.Object.(*kueue.AdmissionCheck)
	if !isAc {
		return false
	}
	defer r.notifyWatchers(ac, nil)
	r.log.WithValues("admissionCheck", klog.KObj(ac)).V(5).Info("Delete event")

	if cqNames := r.cache.DeleteAdmissionCheck(ac); len(cqNames) > 0 {
		r.qManager.QueueInadmissibleWorkloads(context.Background(), cqNames)
	}
	return true
}

func (r *AdmissionCheckReconciler) Generic(e event.GenericEvent) bool {
	r.log.WithValues("object", klog.KObj(e.Object), "kind", e.Object.GetObjectKind().GroupVersionKind()).V(5).Info("Generic event")
	return true
}

func (r *AdmissionCheckReconciler) NotifyClusterQueueUpdate(oldCq *kueue.ClusterQueue, newCq *kueue.ClusterQueue) {
	log := r.log.WithValues("oldClusterQueue", klog.KObj(oldCq), "newClusterQueue", klog.KObj(newCq))
	log.V(5).Info("Cluster queue notification")
	noChange := newCq != nil && oldCq != nil && slices.CmpNoOrder(oldCq.Spec.AdmissionChecks, newCq.Spec.AdmissionChecks)
	if noChange {
		return
	}

	if oldCq != nil {
		r.cqUpdateCh <- event.GenericEvent{Object: oldCq}
	}

	if newCq != nil {
		r.cqUpdateCh <- event.GenericEvent{Object: newCq}
	}
}

type acCqHandler struct {
	cache *cache.Cache
}

func (h *acCqHandler) Create(context.Context, event.CreateEvent, workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

func (h *acCqHandler) Update(context.Context, event.UpdateEvent, workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

func (h *acCqHandler) Delete(context.Context, event.DeleteEvent, workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

func (h *acCqHandler) Generic(ctx context.Context, e event.GenericEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	cq := e.Object.(*kueue.ClusterQueue)
	log := log.FromContext(ctx).WithValues("clusterQueue", klog.KObj(cq))
	log.V(6).Info("Cluster queue generic event")

	for _, ac := range cq.Spec.AdmissionChecks {
		if cqs := h.cache.ClusterQueuesUsingAdmissionCheck(ac); len(cqs) == 0 {
			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name: ac,
				},
			}
			q.Add(req)
		}
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *AdmissionCheckReconciler) SetupWithManager(mgr ctrl.Manager, cfg *config.Configuration) error {
	handler := acCqHandler{
		cache: r.cache,
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&kueue.AdmissionCheck{}).
		WithOptions(controller.Options{NeedLeaderElection: ptr.To(false)}).
		WatchesRawSource(source.Channel(r.cqUpdateCh, &handler)).
		WithEventFilter(r).
		Complete(WithLeadingManager(mgr, r, &kueue.AdmissionCheck{}, cfg))
}
