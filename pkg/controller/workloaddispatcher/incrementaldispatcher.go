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

package workloaddispatcher

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueueconfig "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/core"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
	utilmaps "sigs.k8s.io/kueue/pkg/util/maps"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
)

const (
	IncrementalDispatcherControllerName = "multikueue_incremental_dispatcher"
	incrementalDispatcherRoundTimeout   = 5 * time.Minute
	DefaultStepSize                     = 3
)

var ErrNoMoreWorkers = errors.New("no more workers to nominate")

type IncrementalDispatcherReconciler struct {
	dispatcher
	roundStartTimes *utilmaps.SyncMap[types.NamespacedName, time.Time]
	cfg             *kueueconfig.IncrementalDispatcherConfig
}

var _ reconcile.Reconciler = (*IncrementalDispatcherReconciler)(nil)

func (r *IncrementalDispatcherReconciler) SetupWithManager(mgr ctrl.Manager, cfg *kueueconfig.Configuration) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named(IncrementalDispatcherControllerName).
		For(&kueue.Workload{}).
		WithLogConstructor(roletracker.NewLogConstructor(r.roleTracker, IncrementalDispatcherControllerName)).
		Complete(core.WithLeadingManager(mgr, r, &kueue.Workload{}, cfg))
}

func NewIncrementalDispatcherReconciler(
	c client.Client,
	helper *admissioncheck.MultiKueueStoreHelper,
	roleTracker *roletracker.RoleTracker,
	cfg *kueueconfig.IncrementalDispatcherConfig,
) *IncrementalDispatcherReconciler {
	return &IncrementalDispatcherReconciler{
		dispatcher:      newDispatcher(c, helper, roleTracker),
		roundStartTimes: utilmaps.NewSyncMap[types.NamespacedName, time.Time](0),
		cfg:             cfg,
	}
}

func (r *IncrementalDispatcherReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	wl, remoteClusters, err := r.workloadToNominate(ctx, req, r.clearRoundStartTime)
	if wl == nil || err != nil {
		return reconcile.Result{}, err
	}

	log.V(3).Info("Nominate Worker Clusters with Incremental Dispatcher")
	return r.nominateWorkers(ctx, wl, remoteClusters, log)
}

func (r *IncrementalDispatcherReconciler) nominateWorkers(ctx context.Context, wl *kueue.Workload, remoteClusters []string, log logr.Logger) (reconcile.Result, error) {
	key := client.ObjectKeyFromObject(wl)
	roundStart, found := r.getRoundStartTime(key)
	now := r.clock.Now()
	log.V(5).Info("nominating worker clusters", "nominatedAt", roundStart, "revokedAt", roundStart.Add(incrementalDispatcherRoundTimeout))
	if found && now.Sub(roundStart) <= incrementalDispatcherRoundTimeout {
		remainingWaitTime := incrementalDispatcherRoundTimeout - now.Sub(roundStart)
		log.V(5).Info("Incremental Dispatcher nomination round still in progress", "remainingWaitTime", remainingWaitTime)
		return reconcile.Result{RequeueAfter: remainingWaitTime}, nil
	}

	nextNominatedWorkers, err := getNextNominatedWorkers(log, wl, remoteClusters, r.stepSize())
	log.V(5).Info("revoke outdated nomination and nominate new worker clusters", "revokedWorkerClusters", wl.Status.NominatedClusterNames, "nominatedWorkerClusters", nextNominatedWorkers)
	if err != nil {
		log.Error(err, "Failed to nominate next worker clusters")
		return reconcile.Result{}, err
	}

	nominatedWorkers := append(wl.Status.NominatedClusterNames, nextNominatedWorkers...)
	if err := r.nominate(ctx, wl, nominatedWorkers); err != nil {
		return reconcile.Result{}, err
	}
	// only update the round start time if we successfully nominated workers
	r.setRoundStartTime(key, now)

	return reconcile.Result{}, nil
}

func getNextNominatedWorkers(log logr.Logger, wl *kueue.Workload, remoteClusters []string, batchSize int) ([]string, error) {
	alreadyNominated := sets.New(wl.Status.NominatedClusterNames...)

	workers := make([]string, 0, len(remoteClusters))
	for _, remoteWorker := range remoteClusters {
		if !alreadyNominated.Has(remoteWorker) {
			workers = append(workers, remoteWorker)
		}
	}
	// Sorts the local copy, never remoteClusters, which aliases the cached MultiKueueConfig.
	if !features.Enabled(features.MultiKueueIncrementalDispatcherRespectConfigOrder) {
		slices.Sort(workers)
	}

	log.V(5).Info("proceeding worker clusters nomination", "alreadyNominatedClusterNames", alreadyNominated, "remainingClusterNames", workers)

	if len(workers) == 0 {
		return nil, ErrNoMoreWorkers
	}
	if len(workers) < batchSize {
		return workers, nil
	}
	return workers[:batchSize], nil
}

func (r *IncrementalDispatcherReconciler) stepSize() int {
	if utilfeature.DefaultFeatureGate.Enabled(features.MultiKueueIncrementalDispatcherConfig) &&
		r.cfg != nil && r.cfg.StepSize != nil {
		return int(*r.cfg.StepSize)
	}

	return DefaultStepSize
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
