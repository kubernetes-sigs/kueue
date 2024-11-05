/*
Copyright 2024 The Kubernetes Authors.

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

package controller

import (
	"context"
	"strconv"
	"sync"
	"time"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/performance/scheduler/runner/generator"
	"sigs.k8s.io/kueue/test/performance/scheduler/runner/recorder"
)

type reconciler struct {
	client        client.Client
	atLock        sync.RWMutex
	admissionTime map[types.UID]time.Time
	recorder      *recorder.Recorder
}

func (r *reconciler) getAdmittedTime(uid types.UID) (time.Time, bool) {
	r.atLock.RLock()
	defer r.atLock.RUnlock()
	t, ok := r.admissionTime[uid]
	return t, ok
}

func (r *reconciler) setAdmittedTime(uid types.UID, admitted bool) {
	_, found := r.getAdmittedTime(uid)
	if found != admitted {
		r.atLock.Lock()
		defer r.atLock.Unlock()
		if admitted {
			r.admissionTime[uid] = time.Now()
		} else {
			delete(r.admissionTime, uid)
		}
	}
}

var _ reconcile.Reconciler = (*reconciler)(nil)
var _ predicate.Predicate = (*reconciler)(nil)

func (r *reconciler) Create(ev event.CreateEvent) bool {
	wl, isWl := (ev.Object).(*kueue.Workload)
	if isWl {
		r.recorder.RecordWorkloadState(wl)
	}
	return !isWl
}

func (r *reconciler) Delete(_ event.DeleteEvent) bool {
	// ignore delete
	return false
}

func (r *reconciler) Update(ev event.UpdateEvent) bool {
	wl, isWl := (ev.ObjectNew).(*kueue.Workload)
	if !isWl {
		return true
	}
	admitted := apimeta.IsStatusConditionTrue(wl.Status.Conditions, kueue.WorkloadAdmitted)
	r.setAdmittedTime(wl.UID, admitted)

	r.recorder.RecordWorkloadState(wl)

	return admitted && !apimeta.IsStatusConditionTrue(wl.Status.Conditions, kueue.WorkloadFinished)
}

func (r *reconciler) Generic(_ event.GenericEvent) bool {
	// ignore generic
	return false
}

func (r *reconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	var wl kueue.Workload
	if err := r.client.Get(ctx, req.NamespacedName, &wl); err != nil {
		// we'll ignore not-found errors, since there is nothing to do.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	log := ctrl.LoggerFrom(ctx)
	// this should only:
	// 1. finish the workloads eviction
	if apimeta.IsStatusConditionTrue(wl.Status.Conditions, kueue.WorkloadEvicted) {
		_ = workload.UnsetQuotaReservationWithCondition(&wl, "Pending", "Evicted by the test runner", time.Now())
		err := workload.ApplyAdmissionStatus(ctx, r.client, &wl, true)
		if err == nil {
			log.V(5).Info("Finish eviction")
		}
		return reconcile.Result{}, err
	}

	// 2. finish the workload once it's time is up
	if cond := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadAdmitted); cond != nil && cond.Status == metav1.ConditionTrue {
		admittedTime, ok := r.getAdmittedTime(wl.UID)
		if !ok {
			admittedTime = cond.LastTransitionTime.Time
		}
		runningMsLabel := wl.Labels[generator.RunningTimeLabel]
		runningMs, err := strconv.Atoi(runningMsLabel)
		if err != nil {
			log.V(2).Error(err, "cannot parse running time", "using", runningMs)
		}
		runningDuration := time.Millisecond * time.Duration(runningMs)
		activeFor := time.Since(admittedTime)
		remaining := runningDuration - activeFor
		if remaining > 0 {
			return reconcile.Result{RequeueAfter: remaining}, nil
		} else {
			err := workload.UpdateStatus(ctx, r.client, &wl, kueue.WorkloadFinished, metav1.ConditionTrue, "ByTest", "By test runner", constants.JobControllerName)
			if err == nil {
				log.V(5).Info("Finish Workload")
			}
			return reconcile.Result{}, err
		}
	}
	return reconcile.Result{}, nil
}

func NewReconciler(c client.Client, r *recorder.Recorder) *reconciler {
	return &reconciler{
		client:        c,
		admissionTime: map[types.UID]time.Time{},
		recorder:      r,
	}
}

func (r *reconciler) SetupWithManager(mgr ctrl.Manager) error {
	cqHandler := handler.Funcs{
		CreateFunc: func(_ context.Context, ev event.CreateEvent, _ workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			if cq, isCq := ev.Object.(*kueue.ClusterQueue); isCq {
				r.recorder.RecordCQState(cq)
			}
		},
		UpdateFunc: func(_ context.Context, ev event.UpdateEvent, _ workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			if cq, isCq := ev.ObjectNew.(*kueue.ClusterQueue); isCq {
				r.recorder.RecordCQState(cq)
			}
		},
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&kueue.Workload{}).
		WithOptions(controller.Options{NeedLeaderElection: ptr.To(false)}).
		Watches(&kueue.ClusterQueue{}, cqHandler).
		WithEventFilter(r).
		Complete(r)
}
