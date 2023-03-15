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
	"k8s.io/klog/v2"
	"k8s.io/utils/clock"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/queue"
	"sigs.k8s.io/kueue/pkg/util/limitrange"
	"sigs.k8s.io/kueue/pkg/util/resource"
	"sigs.k8s.io/kueue/pkg/workload"
)

const (
	// statuses for logging purposes
	pending  = "pending"
	admitted = "admitted"
	// The cancellingAdmission status means that Admission=nil, but the workload
	// still has the Admitted=True condition.
	//
	// This is a transient state as the workload controller is about the set the
	// Admitted condition to False, transitioning the workload into the pending
	// status. Only once the workload reaches the pending status is requeued
	// so that scheduler can re-admit it.
	//
	// Cancellation of admission can be triggered in the following scenarios:
	// - exceeding the timeout to reach the PodsReady=True condition when the waitForPodsReady configuration is enabled
	// - workload preemption
	// - clearing of the Admission field manually via API
	cancellingAdmission = "cancellingAdmission"
	finished            = "finished"
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
	NotifyWorkloadUpdate(*kueue.Workload)
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

//+kubebuilder:rbac:groups="",resources=events,verbs=create;watch;update
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

	status := workloadStatus(&wl)
	switch status {
	case pending:
		if !r.queues.QueueForWorkloadExists(&wl) {
			err := workload.UpdateStatusIfChanged(ctx, r.client, &wl, kueue.WorkloadAdmitted, metav1.ConditionFalse,
				"Inadmissible", fmt.Sprintf("LocalQueue %s doesn't exist", wl.Spec.QueueName), constants.AdmissionName)
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}

		cqName, cqOk := r.queues.ClusterQueueForWorkload(&wl)
		if !cqOk {
			err := workload.UpdateStatusIfChanged(ctx, r.client, &wl, kueue.WorkloadAdmitted, metav1.ConditionFalse,
				"Inadmissible", fmt.Sprintf("ClusterQueue %s doesn't exist", cqName), constants.AdmissionName)
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}

		if !r.cache.ClusterQueueActive(cqName) {
			err := workload.UpdateStatusIfChanged(ctx, r.client, &wl, kueue.WorkloadAdmitted, metav1.ConditionFalse,
				"Inadmissible", fmt.Sprintf("ClusterQueue %s is inactive", cqName), constants.AdmissionName)
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
	case cancellingAdmission:
		err := workload.UpdateStatusIfChanged(ctx, r.client, &wl, kueue.WorkloadAdmitted, metav1.ConditionFalse,
			"AdmissionCancelled", "Admission cancelled", constants.AdmissionName)
		return ctrl.Result{}, client.IgnoreNotFound(err)
	case admitted:
		if apimeta.IsStatusConditionTrue(wl.Status.Conditions, kueue.WorkloadAdmitted) {
			return r.reconcileNotReadyTimeout(ctx, req, &wl)
		} else {
			msg := fmt.Sprintf("Admitted by ClusterQueue %s", wl.Status.Admission.ClusterQueue)
			err := workload.UpdateStatusIfChanged(ctx, r.client, &wl, kueue.WorkloadAdmitted, metav1.ConditionTrue, "AdmissionByKueue", msg, constants.AdmissionName)
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
	}

	return ctrl.Result{}, nil
}

func (r *WorkloadReconciler) reconcileNotReadyTimeout(ctx context.Context, req ctrl.Request, wl *kueue.Workload) (ctrl.Result, error) {
	countingTowardsTimeout, recheckAfter := r.admittedNotReadyWorkload(wl, realClock)
	if !countingTowardsTimeout {
		return ctrl.Result{}, nil
	}
	if recheckAfter > 0 {
		klog.V(4).InfoS("Workload not yet ready and did not exceed its timeout", "workload", req.NamespacedName.String(), "recheckAfter", recheckAfter)
		return ctrl.Result{RequeueAfter: recheckAfter}, nil
	} else {
		klog.V(2).InfoS("Cancelling admission of the workload due to exceeding the PodsReady timeout", "workload", req.NamespacedName.String())
		err := r.client.Status().Patch(ctx, workload.BaseSSAWorkload(wl), client.Apply, client.FieldOwner(constants.AdmissionName))
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
}

func (r *WorkloadReconciler) Create(e event.CreateEvent) bool {
	wl := e.Object.(*kueue.Workload)
	defer r.notifyWatchers(wl)
	status := workloadStatus(wl)
	log := r.log.WithValues("workload", klog.KObj(wl), "queue", wl.Spec.QueueName, "status", status)
	log.V(2).Info("Workload create event")

	if status == finished {
		return true
	}

	wlCopy := wl.DeepCopy()
	handlePodOverhead(r.log, wlCopy, r.client)
	r.handlePodLimitRange(log, wlCopy)

	if wl.Status.Admission == nil {
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
	wl := e.Object.(*kueue.Workload)
	defer r.notifyWatchers(wl)
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
	if wl.Status.Admission != nil || e.DeleteStateUnknown {
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
	if wl.Status.Admission == nil {
		r.queues.DeleteWorkload(wl)
	}
	return true
}

func (r *WorkloadReconciler) Update(e event.UpdateEvent) bool {
	oldWl := e.ObjectOld.(*kueue.Workload)
	wl := e.ObjectNew.(*kueue.Workload)
	defer r.notifyWatchers(oldWl)
	defer r.notifyWatchers(wl)

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
	if wl.Status.Admission != nil {
		log = log.WithValues("clusterQueue", wl.Status.Admission.ClusterQueue)
	}
	if oldWl.Status.Admission != nil && (wl.Status.Admission == nil || wl.Status.Admission.ClusterQueue != oldWl.Status.Admission.ClusterQueue) {
		log = log.WithValues("prevClusterQueue", oldWl.Status.Admission.ClusterQueue)
	}
	log.V(2).Info("Workload update event")

	wlCopy := wl.DeepCopy()
	// We do not handle old workload here as it will be deleted or replaced by new one anyway.
	handlePodOverhead(r.log, wlCopy, r.client)
	r.handlePodLimitRange(log, wlCopy)

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
	case prevStatus == admitted && status == cancellingAdmission:
		// The workload will be requeued when handling transitioning
		// from cancellingAdmission to pending.
		// When the workload is in the cancellingAdmission status, the only
		// transition possible, triggered by the workload controller, is to the
		// pending status. Scheduler is only able to re-admit the workload once
		// requeued after reaching the pending status.
	case prevStatus == cancellingAdmission && status == pending:
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

func (r *WorkloadReconciler) notifyWatchers(wl *kueue.Workload) {
	for _, w := range r.watchers {
		w.NotifyWorkloadUpdate(wl)
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *WorkloadReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kueue.Workload{}).
		WithEventFilter(r).
		Complete(r)
}

// admittedNotReadyWorkload returns as pair of values. The first boolean determines
// if the workload is currently counting towards the timeout for PodsReady, i.e.
// it has the Admitted condition True and the PodsReady condition not equal
// True (False or not set). The second value is the remaining time to exceed the
// specified timeout counted since max of the LastTransitionTime's for the
// Admitted and PodsReady conditions.
func (r *WorkloadReconciler) admittedNotReadyWorkload(workload *kueue.Workload, clock clock.Clock) (bool, time.Duration) {
	if r.podsReadyTimeout == nil {
		// the timeout is not configured for the workload controller
		return false, 0
	}
	if workload.Status.Admission == nil {
		// the workload is not admitted so there is no need to time it out
		return false, 0
	}
	admittedCond := apimeta.FindStatusCondition(workload.Status.Conditions, kueue.WorkloadAdmitted)
	if admittedCond == nil || admittedCond.Status != metav1.ConditionTrue {
		// workload does not yet have the condition indicating its admission time
		return false, 0
	}
	podsReadyCond := apimeta.FindStatusCondition(workload.Status.Conditions, kueue.WorkloadPodsReady)
	if podsReadyCond != nil && podsReadyCond.Status == metav1.ConditionTrue {
		return false, 0
	}
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
	if w.Status.Admission != nil {
		return admitted
	}
	if apimeta.IsStatusConditionTrue(w.Status.Conditions, kueue.WorkloadAdmitted) {
		return cancellingAdmission
	}
	return pending
}

// We do not verify Pod's RuntimeClass legality here as this will be performed in admission controller.
// As a result, the pod's Overhead is not always correct. E.g. if we set a non-existent runtime class name to
// `pod.Spec.RuntimeClassName` and we also set the `pod.Spec.Overhead`, in real world, the pod creation will be
// rejected due to the mismatch with RuntimeClass. However, in the future we assume that they are correct.
func handlePodOverhead(log logr.Logger, wl *kueue.Workload, c client.Client) {
	ctx := context.Background()

	for i := range wl.Spec.PodSets {
		podSpec := &wl.Spec.PodSets[i].Template.Spec
		if podSpec.RuntimeClassName != nil && len(podSpec.Overhead) == 0 {
			var runtimeClass nodev1.RuntimeClass
			if err := c.Get(ctx, types.NamespacedName{Name: *podSpec.RuntimeClassName}, &runtimeClass); err != nil {
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
	if err := r.client.List(ctx, &list, &client.ListOptions{Namespace: wl.Namespace}); err != nil {
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
