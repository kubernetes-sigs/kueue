/*
Copyright 2022 The Kubernetes Authors.

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
	"fmt"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	"k8s.io/utils/clock"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/source"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/queue"
	"sigs.k8s.io/kueue/pkg/util/limitrange"
	"sigs.k8s.io/kueue/pkg/util/resource"
	"sigs.k8s.io/kueue/pkg/workload"
)

const (
	// statuses for logging purposes
	pending  = "pending"
	admitted = "admitted"
	finished = "finished"
)

var (
	realClock = clock.RealClock{}
)

type options struct {
	watchers         []WorkloadUpdateWatcher
	podsReadyTimeout *time.Duration
}

// Option configures the reconciler.
type Option func(*options)

// WithPodsReadyTimeout indicates if the controller should interrupt startup
// of a workload if it exceeds the timeout to reach the PodsReady=True condition.
func WithPodsReadyTimeout(value *time.Duration) Option {
	return func(o *options) {
		o.podsReadyTimeout = value
	}
}

// WithWorkloadUpdateWatchers allows to specify the workload update watchers
func WithWorkloadUpdateWatchers(value ...WorkloadUpdateWatcher) Option {
	return func(o *options) {
		o.watchers = value
	}
}

var defaultOptions = options{}

type WorkloadUpdateWatcher interface {
	NotifyWorkloadUpdate(oldWl, newWl *kueue.Workload)
}

// WorkloadReconciler reconciles a Workload object
type WorkloadReconciler struct {
	log              logr.Logger
	queues           *queue.Manager
	cache            *cache.Cache
	client           client.Client
	watchers         []WorkloadUpdateWatcher
	podsReadyTimeout *time.Duration
}

func NewWorkloadReconciler(client client.Client, queues *queue.Manager, cache *cache.Cache, opts ...Option) *WorkloadReconciler {
	options := defaultOptions
	for _, opt := range opts {
		opt(&options)
	}

	return &WorkloadReconciler{
		log:              ctrl.Log.WithName("workload-reconciler"),
		client:           client,
		queues:           queues,
		cache:            cache,
		watchers:         options.watchers,
		podsReadyTimeout: options.podsReadyTimeout,
	}
}

//+kubebuilder:rbac:groups="",resources=events,verbs=create;watch;update;patch
//+kubebuilder:rbac:groups="",resources=limitranges,verbs=get;list;watch
//+kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/finalizers,verbs=update
//+kubebuilder:rbac:groups=node.k8s.io,resources=runtimeclasses,verbs=get;list;watch

func (r *WorkloadReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var wl kueue.Workload
	if err := r.client.Get(ctx, req.NamespacedName, &wl); err != nil {
		// we'll ignore not-found errors, since there is nothing to do.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	log := ctrl.LoggerFrom(ctx).WithValues("workload", klog.KObj(&wl))
	ctx = ctrl.LoggerInto(ctx, log)
	log.V(2).Info("Reconciling Workload")

	// if a pods ready timeout eviction is ongoing.
	if evictionCond := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadEvicted); evictionCond != nil && evictionCond.Status == metav1.ConditionTrue &&
		evictionCond.Reason == kueue.WorkloadEvictedByPodsReadyTimeout &&
		apimeta.IsStatusConditionTrue(wl.Status.Conditions, kueue.WorkloadAdmitted) {

		log.V(2).Info("Cancelling admission of the workload due to exceeding the PodsReady timeout")
		workload.UnsetAdmissionWithCondition(&wl, "Evicted", evictionCond.Message)
		err := workload.ApplyAdmissionStatus(ctx, r.client, &wl, true)
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if apimeta.IsStatusConditionTrue(wl.Status.Conditions, kueue.WorkloadFinished) {
		return ctrl.Result{}, nil
	}
	if workload.IsAdmitted(&wl) {
		return r.reconcileNotReadyTimeout(ctx, req, &wl)
	}

	if !r.queues.QueueForWorkloadExists(&wl) {
		log.V(3).Info("Workload is inadmissible because of missing LocalQueue", "localQueue", klog.KRef(wl.Namespace, wl.Spec.QueueName))
		workload.UnsetAdmissionWithCondition(&wl, "Inadmissible", fmt.Sprintf("LocalQueue %s doesn't exist", wl.Spec.QueueName))
		err := workload.ApplyAdmissionStatus(ctx, r.client, &wl, true)
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	cqName, cqOk := r.queues.ClusterQueueForWorkload(&wl)
	if !cqOk {
		log.V(3).Info("Workload is inadmissible because of missing ClusterQueue", "clusterQueue", klog.KRef("", cqName))
		workload.UnsetAdmissionWithCondition(&wl, "Inadmissible", fmt.Sprintf("ClusterQueue %s doesn't exist", cqName))
		err := workload.ApplyAdmissionStatus(ctx, r.client, &wl, true)
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !r.cache.ClusterQueueActive(cqName) {
		log.V(3).Info("Workload is inadmissible because ClusterQueue is inactive", "clusterQueue", klog.KRef("", cqName))
		workload.UnsetAdmissionWithCondition(&wl, "Inadmissible", fmt.Sprintf("ClusterQueue %s is inactive", cqName))
		err := workload.ApplyAdmissionStatus(ctx, r.client, &wl, true)
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	return ctrl.Result{}, nil
}

func (r *WorkloadReconciler) reconcileNotReadyTimeout(ctx context.Context, req ctrl.Request, wl *kueue.Workload) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	countingTowardsTimeout, recheckAfter := r.admittedNotReadyWorkload(wl, realClock)
	if !countingTowardsTimeout {
		return ctrl.Result{}, nil
	}
	if recheckAfter > 0 {
		log.V(4).Info("Workload not yet ready and did not exceed its timeout", "recheckAfter", recheckAfter)
		return ctrl.Result{RequeueAfter: recheckAfter}, nil
	} else {
		log.V(2).Info("Start the eviction of the workload due to exceeding the PodsReady timeout")
		workload.SetEvictedCondition(wl, kueue.WorkloadEvictedByPodsReadyTimeout, fmt.Sprintf("Exceeded the PodsReady timeout %s", req.NamespacedName.String()))
		err := workload.ApplyAdmissionStatus(ctx, r.client, wl, true)
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
}

func (r *WorkloadReconciler) Create(e event.CreateEvent) bool {
	wl, isWorkload := e.Object.(*kueue.Workload)
	if !isWorkload {
		// this event will be handled by the LimitRange/RuntimeClass handle
		return true
	}
	defer r.notifyWatchers(nil, wl)
	status := workloadStatus(wl)
	log := r.log.WithValues("workload", klog.KObj(wl), "queue", wl.Spec.QueueName, "status", status)
	log.V(2).Info("Workload create event")

	if status == finished {
		return true
	}

	wlCopy := wl.DeepCopy()
	r.adjustResources(log, wlCopy)

	if !workload.IsAdmitted(wl) {
		if !r.queues.AddOrUpdateWorkload(wlCopy) {
			log.V(2).Info("Queue for workload didn't exist; ignored for now")
		}
		return true
	}
	if !r.cache.AddOrUpdateWorkload(wlCopy) {
		log.V(2).Info("ClusterQueue for workload didn't exist; ignored for now")
	}

	return true
}

func (r *WorkloadReconciler) Delete(e event.DeleteEvent) bool {
	wl, isWorkload := e.Object.(*kueue.Workload)
	if !isWorkload {
		// this event will be handled by the LimitRange/RuntimeClass handle
		return true
	}
	defer r.notifyWatchers(wl, nil)
	status := "unknown"
	if !e.DeleteStateUnknown {
		status = workloadStatus(wl)
	}
	log := r.log.WithValues("workload", klog.KObj(wl), "queue", wl.Spec.QueueName, "status", status)
	log.V(2).Info("Workload delete event")
	ctx := ctrl.LoggerInto(context.Background(), log)

	// When assigning a clusterQueue to a workload, we assume it in the cache. If
	// the state is unknown, the workload could have been assumed and we need
	// to clear it from the cache.
	if workload.IsAdmitted(wl) || e.DeleteStateUnknown {
		// trigger the move of associated inadmissibleWorkloads if required.
		r.queues.QueueAssociatedInadmissibleWorkloadsAfter(ctx, wl, func() {
			// Delete the workload from cache while holding the queues lock
			// to guarantee that requeueued workloads are taken into account before
			// the next scheduling cycle.
			if err := r.cache.DeleteWorkload(wl); err != nil {
				if !e.DeleteStateUnknown {
					log.Error(err, "Failed to delete workload from cache")
				}
			}
		})
	}

	// Even if the state is unknown, the last cached state tells us whether the
	// workload was in the queues and should be cleared from them.
	if workload.IsAdmitted(wl) {
		r.queues.DeleteWorkload(wl)
	}
	return true
}

func (r *WorkloadReconciler) Update(e event.UpdateEvent) bool {
	oldWl, isWorkload := e.ObjectOld.(*kueue.Workload)
	if !isWorkload {
		// this event will be handled by the LimitRange/RuntimeClass handle
		return true
	}
	wl := e.ObjectNew.(*kueue.Workload)
	defer r.notifyWatchers(oldWl, wl)

	status := workloadStatus(wl)
	log := r.log.WithValues("workload", klog.KObj(wl), "queue", wl.Spec.QueueName, "status", status)
	ctx := ctrl.LoggerInto(context.Background(), log)

	prevQueue := oldWl.Spec.QueueName
	if prevQueue != wl.Spec.QueueName {
		log = log.WithValues("prevQueue", prevQueue)
	}
	prevStatus := workloadStatus(oldWl)
	if prevStatus != status {
		log = log.WithValues("prevStatus", prevStatus)
	}
	if workload.IsAdmitted(wl) {
		log = log.WithValues("clusterQueue", wl.Status.Admission.ClusterQueue)
	}
	if workload.IsAdmitted(oldWl) && (!workload.IsAdmitted(wl) || wl.Status.Admission.ClusterQueue != oldWl.Status.Admission.ClusterQueue) {
		log = log.WithValues("prevClusterQueue", oldWl.Status.Admission.ClusterQueue)
	}
	log.V(2).Info("Workload update event")

	wlCopy := wl.DeepCopy()
	// We do not handle old workload here as it will be deleted or replaced by new one anyway.
	r.adjustResources(log, wlCopy)

	switch {
	case status == finished:
		// The workload could have been in the queues if we missed an event.
		r.queues.DeleteWorkload(wl)

		// trigger the move of associated inadmissibleWorkloads, if there are any.
		r.queues.QueueAssociatedInadmissibleWorkloadsAfter(ctx, wl, func() {
			// Delete the workload from cache while holding the queues lock
			// to guarantee that requeueued workloads are taken into account before
			// the next scheduling cycle.
			if err := r.cache.DeleteWorkload(oldWl); err != nil && prevStatus == admitted {
				log.Error(err, "Failed to delete workload from cache")
			}
		})

	case prevStatus == pending && status == pending:
		if !r.queues.UpdateWorkload(oldWl, wlCopy) {
			log.V(2).Info("Queue for updated workload didn't exist; ignoring for now")
		}

	case prevStatus == pending && status == admitted:
		r.queues.DeleteWorkload(oldWl)
		if !r.cache.AddOrUpdateWorkload(wlCopy) {
			log.V(2).Info("ClusterQueue for workload didn't exist; ignored for now")
		}
	case prevStatus == admitted && status == pending:
		// trigger the move of associated inadmissibleWorkloads, if there are any.
		r.queues.QueueAssociatedInadmissibleWorkloadsAfter(ctx, wl, func() {
			// Delete the workload from cache while holding the queues lock
			// to guarantee that requeueued workloads are taken into account before
			// the next scheduling cycle.
			if err := r.cache.DeleteWorkload(wl); err != nil {
				log.Error(err, "Failed to delete workload from cache")
			}
		})
		if !r.queues.AddOrUpdateWorkload(wlCopy) {
			log.V(2).Info("Queue for workload didn't exist; ignored for now")
		}

	default:
		// Workload update in the cache is handled here; however, some fields are immutable
		// and are not supposed to actually change anything.
		if err := r.cache.UpdateWorkload(oldWl, wlCopy); err != nil {
			log.Error(err, "Updating workload in cache")
		}
	}

	return true
}

func (r *WorkloadReconciler) Generic(e event.GenericEvent) bool {
	r.log.V(3).Info("Ignore generic event", "obj", klog.KObj(e.Object), "kind", e.Object.GetObjectKind().GroupVersionKind())
	return false
}

func (r *WorkloadReconciler) notifyWatchers(oldWl, newWl *kueue.Workload) {
	for _, w := range r.watchers {
		w.NotifyWorkloadUpdate(oldWl, newWl)
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *WorkloadReconciler) SetupWithManager(mgr ctrl.Manager) error {
	ruh := &resourceUpdatesHandler{
		r: r,
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&kueue.Workload{}).
		Watches(&source.Kind{Type: &corev1.LimitRange{}}, ruh).
		Watches(&source.Kind{Type: &nodev1.RuntimeClass{}}, ruh).
		WithEventFilter(r).
		Complete(r)
}

// admittedNotReadyWorkload returns as pair of values. The first boolean determines
// if the workload is currently counting towards the timeout for PodsReady, i.e.
// it has the Admitted condition True and the PodsReady condition not equal
// True (False or not set). The second value is the remaining time to exceed the
// specified timeout counted since max of the LastTransitionTime's for the
// Admitted and PodsReady conditions.
func (r *WorkloadReconciler) admittedNotReadyWorkload(wl *kueue.Workload, clock clock.Clock) (bool, time.Duration) {
	if r.podsReadyTimeout == nil {
		// the timeout is not configured for the workload controller
		return false, 0
	}
	if !workload.IsAdmitted(wl) {
		// the workload is not admitted so there is no need to time it out
		return false, 0
	}

	podsReadyCond := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadPodsReady)
	if podsReadyCond != nil && podsReadyCond.Status == metav1.ConditionTrue {
		return false, 0
	}
	admittedCond := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadAdmitted)
	elapsedTime := clock.Since(admittedCond.LastTransitionTime.Time)
	if podsReadyCond != nil && podsReadyCond.Status == metav1.ConditionFalse && podsReadyCond.LastTransitionTime.After(admittedCond.LastTransitionTime.Time) {
		elapsedTime = clock.Since(podsReadyCond.LastTransitionTime.Time)
	}
	waitFor := *r.podsReadyTimeout - elapsedTime
	if waitFor < 0 {
		waitFor = 0
	}
	return true, waitFor
}

func workloadStatus(w *kueue.Workload) string {
	if apimeta.IsStatusConditionTrue(w.Status.Conditions, kueue.WorkloadFinished) {
		return finished
	}
	if workload.IsAdmitted(w) {
		return admitted
	}
	return pending
}

// We do not verify Pod's RuntimeClass legality here as this will be performed in admission controller.
// As a result, the pod's Overhead is not always correct. E.g. if we set a non-existent runtime class name to
// `pod.Spec.RuntimeClassName` and we also set the `pod.Spec.Overhead`, in real world, the pod creation will be
// rejected due to the mismatch with RuntimeClass. However, in the future we assume that they are correct.
func (r *WorkloadReconciler) handlePodOverhead(log logr.Logger, wl *kueue.Workload) {
	ctx := context.Background()

	for i := range wl.Spec.PodSets {
		podSpec := &wl.Spec.PodSets[i].Template.Spec
		if podSpec.RuntimeClassName != nil && len(podSpec.Overhead) == 0 {
			var runtimeClass nodev1.RuntimeClass
			if err := r.client.Get(ctx, types.NamespacedName{Name: *podSpec.RuntimeClassName}, &runtimeClass); err != nil {
				log.Error(err, "Could not get RuntimeClass")
				continue
			}
			if runtimeClass.Overhead != nil {
				podSpec.Overhead = runtimeClass.Overhead.PodFixed
			}
		}
	}
}

func (r *WorkloadReconciler) handlePodLimitRange(log logr.Logger, wl *kueue.Workload) {
	ctx := context.TODO()
	// get the list of limit ranges
	var list corev1.LimitRangeList
	if err := r.client.List(ctx, &list, &client.ListOptions{Namespace: wl.Namespace}, client.MatchingFields{indexer.LimitRangeHasContainerType: "true"}); err != nil {
		log.Error(err, "Could not list LimitRanges")
		return
	}

	if len(list.Items) == 0 {
		return
	}
	summary := limitrange.Summarize(list.Items...)
	containerLimits, found := summary[corev1.LimitTypeContainer]
	if !found {
		return
	}

	for pi := range wl.Spec.PodSets {
		pod := &wl.Spec.PodSets[pi].Template.Spec
		for ci := range pod.InitContainers {
			res := &pod.InitContainers[ci].Resources
			res.Limits = resource.MergeResourceListKeepFirst(res.Limits, containerLimits.Default)
			res.Requests = resource.MergeResourceListKeepFirst(res.Requests, containerLimits.DefaultRequest)
		}
		for ci := range pod.Containers {
			res := &pod.Containers[ci].Resources
			res.Limits = resource.MergeResourceListKeepFirst(res.Limits, containerLimits.Default)
			res.Requests = resource.MergeResourceListKeepFirst(res.Requests, containerLimits.DefaultRequest)
		}
	}
}

func (r *WorkloadReconciler) handleLimitsToRequests(wl *kueue.Workload) {
	for pi := range wl.Spec.PodSets {
		pod := &wl.Spec.PodSets[pi].Template.Spec
		for ci := range pod.InitContainers {
			res := &pod.InitContainers[ci].Resources
			res.Requests = resource.MergeResourceListKeepFirst(res.Requests, res.Limits)
		}
		for ci := range pod.Containers {
			res := &pod.Containers[ci].Resources
			res.Requests = resource.MergeResourceListKeepFirst(res.Requests, res.Limits)
		}
	}
}

func (r *WorkloadReconciler) adjustResources(log logr.Logger, wl *kueue.Workload) {
	r.handlePodOverhead(log, wl)
	r.handlePodLimitRange(log, wl)
	r.handleLimitsToRequests(wl)
}

type resourceUpdatesHandler struct {
	r *WorkloadReconciler
}

func (h *resourceUpdatesHandler) Create(e event.CreateEvent, q workqueue.RateLimitingInterface) {
	//TODO: the eventHandler should get a context soon, and this could be dropped
	// https://github.com/kubernetes-sigs/controller-runtime/blob/master/pkg/handler/eventhandler.go
	ctx := context.TODO()
	log := ctrl.LoggerFrom(ctx).WithValues("kind", e.Object.GetObjectKind())
	ctx = ctrl.LoggerInto(ctx, log)
	log.V(5).Info("Create event")
	h.handle(ctx, e.Object, q)
}

func (h *resourceUpdatesHandler) Update(e event.UpdateEvent, q workqueue.RateLimitingInterface) {
	ctx := context.TODO()
	log := ctrl.LoggerFrom(ctx).WithValues("kind", e.ObjectNew.GetObjectKind())
	ctx = ctrl.LoggerInto(ctx, log)
	log.V(5).Info("Update event")
	h.handle(ctx, e.ObjectNew, q)
}

func (h *resourceUpdatesHandler) Delete(e event.DeleteEvent, q workqueue.RateLimitingInterface) {
	ctx := context.TODO()
	log := ctrl.LoggerFrom(ctx).WithValues("kind", e.Object.GetObjectKind())
	ctx = ctrl.LoggerInto(ctx, log)
	log.V(5).Info("Delete event")
	h.handle(ctx, e.Object, q)
}

func (h *resourceUpdatesHandler) Generic(_ event.GenericEvent, _ workqueue.RateLimitingInterface) {
}

func (h *resourceUpdatesHandler) handle(ctx context.Context, obj client.Object, q workqueue.RateLimitingInterface) {
	switch v := obj.(type) {
	case *corev1.LimitRange:
		log := ctrl.LoggerFrom(ctx).WithValues("limitRange", klog.KObj(v))
		ctx = ctrl.LoggerInto(ctx, log)
		h.queueReconcileForPending(ctx, q, client.InNamespace(v.Namespace))
	case *nodev1.RuntimeClass:
		log := ctrl.LoggerFrom(ctx).WithValues("runtimeClass", klog.KObj(v))
		ctx = ctrl.LoggerInto(ctx, log)
		h.queueReconcileForPending(ctx, q, client.MatchingFields{indexer.WorkloadRuntimeClassKey: v.Name})
	default:
		panic(v)
	}
}

func (h *resourceUpdatesHandler) queueReconcileForPending(ctx context.Context, _ workqueue.RateLimitingInterface, opts ...client.ListOption) {
	log := ctrl.LoggerFrom(ctx)
	lst := kueue.WorkloadList{}
	opts = append(opts, client.MatchingFields{indexer.WorkloadAdmittedKey: string(metav1.ConditionFalse)})
	err := h.r.client.List(ctx, &lst, opts...)
	if err != nil {
		log.Error(err, "Could not list pending workloads")
	}
	log.V(4).Info("Updating pending workload requests", "count", len(lst.Items))
	for _, w := range lst.Items {
		wlCopy := w.DeepCopy()
		log := log.WithValues("workload", klog.KObj(wlCopy))
		log.V(5).Info("Queue reconcile for")
		h.r.adjustResources(log, wlCopy)
		if !h.r.queues.AddOrUpdateWorkload(wlCopy) {
			log.V(2).Info("Queue for workload didn't exist")
		}
	}
}
