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

package incrementaldispatcher

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"
	"k8s.io/utils/clock"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueueconfig "sigs.k8s.io/kueue/apis/config/v1beta1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/workloaddispatcher/dispatcher"
	"sigs.k8s.io/kueue/pkg/util/multikueuehelper"
	"sigs.k8s.io/kueue/pkg/workload"
)

const (
	incrementalDispatcherRoundTimeout = 5 * time.Minute
)

type IncrementalDispatcherReconciler struct {
	client          client.Client
	helper          *multikueuehelper.MultiKueueStoreHelper
	clock           clock.Clock
	dispatcherName  string
	roundStartTimes map[types.NamespacedName]time.Time
}

var realClock = clock.RealClock{}
var _ reconcile.Reconciler = (*IncrementalDispatcherReconciler)(nil)

func NewIncrementalDispatcherReconciler(c client.Client, helper *multikueuehelper.MultiKueueStoreHelper, dispatcherName string) *IncrementalDispatcherReconciler {
	return &IncrementalDispatcherReconciler{
		client:          c,
		helper:          helper,
		clock:           realClock,
		dispatcherName:  dispatcherName,
		roundStartTimes: make(map[types.NamespacedName]time.Time),
	}
}
func (r *IncrementalDispatcherReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx).WithValues("op", "nominateWorkers")
	log.V(3).Info("Nominate Worker Clusters with Incremental Dispatcher")

	if r.dispatcherName != kueueconfig.MultiKueueDispatcherModeIncremental {
		log.V(3).Info("Not a Incremental Dispatcher, skip the reconciliation", "dispatcherName", r.dispatcherName)
		return reconcile.Result{}, nil
	}

	wl := &kueue.Workload{}
	err := r.client.Get(ctx, req.NamespacedName, wl)
	if err != nil {
		log.Error(err, "Failed to retrieve Workload, skip the reconciliation", "namespacedName", req.NamespacedName)
		if apierrors.IsNotFound(err) {
			r.deleteRoundStartTime(req.NamespacedName)
		}
		return reconcile.Result{}, err
	}

	if !wl.DeletionTimestamp.IsZero() {
		log.V(3).Info("Workload is deleted, skip the reconciliation", "workload", klog.KObj(wl))
		r.deleteRoundStartTime(req.NamespacedName)
		return reconcile.Result{}, nil
	}

	mkAc, err := dispatcher.GetMultiKueueAdmissionCheck(ctx, r.client, wl)
	if err != nil {
		log.Error(err, "Can not get MultiKueue AdmissionCheckState", "workload", klog.KObj(wl))
		return reconcile.Result{}, err
	}

	if mkAc == nil || mkAc.State != kueue.CheckStatePending {
		log.V(3).Info("AdmissionCheckState is not in Pending, skip the reconciliation", "workload", klog.KObj(wl))
		return reconcile.Result{}, nil
	}

	// The workload is already assigned to a cluster, no need to nominate workers.
	if wl.Status.ClusterName != nil {
		log.V(3).Info("The workload is already assigned to a cluster, no need to nominate workers", "workload", klog.KObj(wl))
		return reconcile.Result{}, nil
	}

	remoteClusters, err := dispatcher.GetRemoteClusters(ctx, r.client, r.helper, wl, mkAc.Name)
	if err != nil {
		log.Error(err, "Can not get workload group", "workload", klog.KObj(wl))
		return reconcile.Result{}, err
	}

	if workload.IsFinished(wl) || !workload.HasQuotaReservation(wl) {
		log.V(3).Info("Workload is already finished or has no quota reserved, skip the reconciliation", "workload", klog.KObj(wl))
		r.deleteRoundStartTime(req.NamespacedName)
		return reconcile.Result{}, nil
	}

	return r.nominateWorkers(ctx, wl, remoteClusters, log)
}

func (r *IncrementalDispatcherReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kueue.Workload{}).
		Complete(r)
}

func (r *IncrementalDispatcherReconciler) nominateWorkers(ctx context.Context, wl *kueue.Workload, remoteClusters sets.Set[string], log logr.Logger) (reconcile.Result, error) {
	log.V(3).Info("NominateWorkers called", "workload", klog.KObj(wl))
	key := client.ObjectKeyFromObject(wl)
	roundStart, found := r.roundStartTimes[key]
	now := r.clock.Now()
	log.V(3).Info("NominateWorkers called", "workload", klog.KObj(wl), "found", found, "roundStart", roundStart, "still in round", now.Sub(roundStart) <= incrementalDispatcherRoundTimeout)
	if found && now.Sub(roundStart) <= incrementalDispatcherRoundTimeout {
		remainingWaitTime := incrementalDispatcherRoundTimeout - now.Sub(roundStart)
		log.V(4).Info("Incremental Dispatcher nomination round still in progress", "remainingWaitTime", remainingWaitTime)
		return reconcile.Result{RequeueAfter: remainingWaitTime}, nil
	}

	nextNominatedWorkers, err := getNextNominatedWorkers(log, wl, remoteClusters)
	log.V(3).Info("NominateWorkers called", "workload", klog.KObj(wl), "nextNominatedWorkers", nextNominatedWorkers)
	if err != nil {
		log.Error(err, "Failed to nominate next worker clusters")
		return reconcile.Result{}, err
	}

	nominatedWorkers := append(wl.Status.NominatedClusterNames, nextNominatedWorkers...)
	wl.Status.NominatedClusterNames = nominatedWorkers
	if err := workload.ApplyAdmissionStatus(ctx, r.client, wl, true, r.clock); err != nil {
		log.V(2).Error(err, "Failed to patch nominated clusters", "workload", klog.KObj(wl))
		return reconcile.Result{}, err
	}
	// only update the round start time if we successfully nominated workers
	r.roundStartTimes[key] = now

	return reconcile.Result{}, nil
}

// getNextNominatedWorkers returns the next set of nominated workers for incremental dispatching.
// It nominates up to 3 remotes that have not yet been nominated, in sorted order.
func getNextNominatedWorkers(log logr.Logger, wl *kueue.Workload, remoteClusters sets.Set[string]) ([]string, error) {
	alreadyNominated := sets.New(wl.Status.NominatedClusterNames...)

	workers := make([]string, 0, len(remoteClusters))
	for remoteWorker := range remoteClusters {
		if !alreadyNominated.Has(remoteWorker) {
			workers = append(workers, remoteWorker)
		}
	}
	slices.Sort(workers)

	log.V(5).Info("getNextNominatedWorkers (incremental)", "alreadyNominatedClusterNames", alreadyNominated, "remainingClusterNames", workers)

	if len(workers) == 0 {
		return nil, errors.New("no more workers to nominate")
	}
	batchSize := 3
	if len(workers) < batchSize {
		return workers, nil
	}
	return workers[:batchSize], nil
}

// Deletes the map entry when the workload has been evicted, re-created or finished.
func (r *IncrementalDispatcherReconciler) deleteRoundStartTime(wlKey types.NamespacedName) {
	delete(r.roundStartTimes, wlKey)
}
