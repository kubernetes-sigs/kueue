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
	"slices"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/clock"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueueconfig "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/core"
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	"sigs.k8s.io/kueue/pkg/workload"
)

type AllAtOnceDispatcherReconciler struct {
	client      client.Client
	helper      *admissioncheck.MultiKueueStoreHelper
	clock       clock.Clock
	roleTracker *roletracker.RoleTracker
}

var _ reconcile.Reconciler = (*AllAtOnceDispatcherReconciler)(nil)

const AllAtOnceDispatcherControllerName = "multikueue_all_at_once_dispatcher"

func (r *AllAtOnceDispatcherReconciler) SetupWithManager(mgr ctrl.Manager, cfg *kueueconfig.Configuration) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named(AllAtOnceDispatcherControllerName).
		For(&kueue.Workload{}).
		WithLogConstructor(roletracker.NewLogConstructor(r.roleTracker, AllAtOnceDispatcherControllerName)).
		Complete(core.WithLeadingManager(mgr, r, &kueue.Workload{}, cfg))
}

func NewAllAtOnceDispatcherReconciler(c client.Client, helper *admissioncheck.MultiKueueStoreHelper, roleTracker *roletracker.RoleTracker) *AllAtOnceDispatcherReconciler {
	return &AllAtOnceDispatcherReconciler{
		client:      c,
		helper:      helper,
		clock:       realClock,
		roleTracker: roleTracker,
	}
}

func (r *AllAtOnceDispatcherReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	wl := &kueue.Workload{}
	if err := r.client.Get(ctx, req.NamespacedName, wl); err != nil {
		log.Error(err, "Failed to retrieve Workload, skip the reconciliation")
		return reconcile.Result{}, err
	}

	if !wl.DeletionTimestamp.IsZero() {
		log.V(3).Info("Workload is deleted, skip the reconciliation")
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
		return reconcile.Result{}, nil
	}

	log.V(3).Info("Nominate Worker Clusters with AllAtOnce Dispatcher")
	return r.nominateWorkers(ctx, wl, remoteClusters, log)
}

func (r *AllAtOnceDispatcherReconciler) nominateWorkers(ctx context.Context, wl *kueue.Workload, remoteClusters sets.Set[string], log logr.Logger) (reconcile.Result, error) {
	nominatedWorkers := remoteClusters.UnsortedList()
	slices.Sort(nominatedWorkers)

	if equality.Semantic.DeepEqual(wl.Status.NominatedClusterNames, nominatedWorkers) {
		log.V(5).Info("Nominated cluster names already up to date, skip the reconciliation", "nominatedClusterNames", nominatedWorkers)
		return reconcile.Result{}, nil
	}

	log.V(5).Info("Nominating worker clusters", "nominatedClusterNames", nominatedWorkers)
	if err := workload.PatchAdmissionStatus(ctx, r.client, wl, r.clock, func(wl *kueue.Workload) (bool, error) {
		wl.Status.NominatedClusterNames = nominatedWorkers
		return true, nil
	}); err != nil {
		log.V(2).Error(err, "Failed to patch nominated clusters")
		return reconcile.Result{}, err
	}

	return reconcile.Result{}, nil
}
