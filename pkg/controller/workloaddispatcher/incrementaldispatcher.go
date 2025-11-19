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

package dispatcher

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/clock"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueueconfig "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/core"
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
	utilmaps "sigs.k8s.io/kueue/pkg/util/maps"
	"sigs.k8s.io/kueue/pkg/workload"
)

const (
	incrementalDispatcherRoundTimeout = 5 * time.Minute
)

var ErrNoMoreWorkers = errors.New("no more workers to nominate")

type IncrementalDispatcherReconciler struct {
	client          client.Client
	helper          *admissioncheck.MultiKueueStoreHelper
	clock           clock.Clock
	roundStartTimes *utilmaps.SyncMap[types.NamespacedName, time.Time]
}

var realClock = clock.RealClock{}
var _ reconcile.Reconciler = (*IncrementalDispatcherReconciler)(nil)

func (r *IncrementalDispatcherReconciler) SetupWithManager(mgr ctrl.Manager, cfg *kueueconfig.Configuration) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("multikueue_incremental_dispatcher").
		For(&kueue.Workload{}).
		Complete(core.WithLeadingManager(mgr, r, &kueue.Workload{}, cfg))
}

func NewIncrementalDispatcherReconciler(c client.Client, helper *admissioncheck.MultiKueueStoreHelper) *IncrementalDispatcherReconciler {
	return &IncrementalDispatcherReconciler{
		client:          c,
		helper:          helper,
		clock:           realClock,
		roundStartTimes: utilmaps.NewSyncMap[types.NamespacedName, time.Time](0),
	}
}
func (r *IncrementalDispatcherReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	wl := &kueue.Workload{}
	err := r.client.Get(ctx, req.NamespacedName, wl)
	if err != nil {
		log.Error(err, "Failed to retrieve Workload, skip the reconciliation")
		if apierrors.IsNotFound(err) {
			r.clearRoundStartTime(req.NamespacedName)
		}
		return reconcile.Result{}, err
	}

	if !wl.DeletionTimestamp.IsZero() {
		log.V(3).Info("Workload is deleted, skip the reconciliation")
		r.clearRoundStartTime(req.NamespacedName)
		return reconcile.Result{}, nil
	}

	mkAc, err := admissioncheck.GetMultiKueueAdmissionCheck(ctx, r.client, wl)
	if err != nil {
		log.Error(err, "Can not get MultiKueue AdmissionCheckState")
		return reconcile.Result{}, err
	}

	if mkAc == nil || mkAc.State != kueue.CheckStatePending {
		log.V(3).Info("AdmissionCheckState is not in Pending, skip the reconciliation")
		return reconcile.Result{}, nil
	}

	// The workload is already assigned to a cluster, no need to nominate workers.
	if wl.Status.ClusterName != nil {
		log.V(3).Info("The workload is already assigned to a cluster, no need to nominate workers")
		return reconcile.Result{}, nil
	}

	remoteClusters, err := admissioncheck.GetRemoteClusters(ctx, r.helper, mkAc.Name)
	if err != nil {
		log.Error(err, "Can not get workload group")
		return reconcile.Result{}, err
	}

	if workload.IsFinished(wl) || !workload.HasQuotaReservation(wl) {
		log.V(3).Info("Workload is already finished or has no quota reserved, skip the reconciliation")
		r.clearRoundStartTime(req.NamespacedName)
		return reconcile.Result{}, nil
	}

	log.V(3).Info("Nominate Worker Clusters with Incremental Dispatcher")
	return r.nominateWorkers(ctx, wl, remoteClusters, log)
}

func (r *IncrementalDispatcherReconciler) nominateWorkers(ctx context.Context, wl *kueue.Workload, remoteClusters sets.Set[string], log logr.Logger) (reconcile.Result, error) {
	key := client.ObjectKeyFromObject(wl)
	roundStart, found := r.getRoundStartTime(key)
	now := r.clock.Now()
	log.V(5).Info("nominating worker clusters", "nominatedAt", roundStart, "revokedAt", roundStart.Add(incrementalDispatcherRoundTimeout))
	if found && now.Sub(roundStart) <= incrementalDispatcherRoundTimeout {
		remainingWaitTime := incrementalDispatcherRoundTimeout - now.Sub(roundStart)
		log.V(5).Info("Incremental Dispatcher nomination round still in progress", "remainingWaitTime", remainingWaitTime)
		return reconcile.Result{RequeueAfter: remainingWaitTime}, nil
	}

	nextNominatedWorkers, err := getNextNominatedWorkers(log, wl, remoteClusters)
	log.V(5).Info("revoke outdated nomination and nominate new worker clusters", "revokedWorkerClusters", wl.Status.NominatedClusterNames, "nominatedWorkerClusters", nextNominatedWorkers)
	if err != nil {
		log.Error(err, "Failed to nominate next worker clusters")
		return reconcile.Result{}, err
	}

	nominatedWorkers := append(wl.Status.NominatedClusterNames, nextNominatedWorkers...)
	if err = workload.PatchAdmissionStatus(ctx, r.client, wl, r.clock, func() (bool, error) {
		wl.Status.NominatedClusterNames = nominatedWorkers
		return true, nil
	}); err != nil {
		log.V(2).Error(err, "Failed to patch nominated clusters")
		return reconcile.Result{}, err
	}
	// only update the round start time if we successfully nominated workers
	r.setRoundStartTime(key, now)

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

	log.V(5).Info("proceeding worker clusters nomination", "alreadyNominatedClusterNames", alreadyNominated, "remainingClusterNames", workers)

	if len(workers) == 0 {
		return nil, ErrNoMoreWorkers
	}
	batchSize := 3
	if len(workers) < batchSize {
		return workers, nil
	}
	return workers[:batchSize], nil
}

func (r *IncrementalDispatcherReconciler) setRoundStartTime(key types.NamespacedName, t time.Time) {
	r.roundStartTimes.Add(key, t)
}

func (r *IncrementalDispatcherReconciler) getRoundStartTime(key types.NamespacedName) (time.Time, bool) {
	t, found := r.roundStartTimes.Get(key)
	return t, found
}

// clearRoundStartTime removes the round start time for the given workload key.
func (r *IncrementalDispatcherReconciler) clearRoundStartTime(key types.NamespacedName) {
	r.roundStartTimes.Delete(key)
}
