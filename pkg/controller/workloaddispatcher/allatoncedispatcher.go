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

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/equality"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueueconfig "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/admissionchecks/multikueue"
	"sigs.k8s.io/kueue/pkg/controller/core"
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	workloadevict "sigs.k8s.io/kueue/pkg/workload/evict"
)

type AllAtOnceDispatcherReconciler struct {
	dispatcher
}

var _ reconcile.Reconciler = (*AllAtOnceDispatcherReconciler)(nil)

const AllAtOnceDispatcherControllerName = "multikueue_all_at_once_dispatcher"

// SetupWithManager registers the controller. The nominated clusters depend on the
// MultiKueueConfig of a Workload's AdmissionCheck, on the clusters that config lists
// and on which of them are Active, so a change to any of these requeues the affected
// Workloads. AdmissionCheck and MultiKueueConfig changes are handled as they are for
// the MultiKueue workload reconciler.
func (r *AllAtOnceDispatcherReconciler) SetupWithManager(mgr ctrl.Manager, cfg *kueueconfig.Configuration) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named(AllAtOnceDispatcherControllerName).
		For(&kueue.Workload{}).
		Watches(&kueue.AdmissionCheck{}, multikueue.NewWorkloadAdmissionCheckHandler(r.client, constants.UpdatesBatchPeriod)).
		Watches(&kueue.MultiKueueConfig{}, multikueue.NewWorkloadConfigHandler(r.client, constants.UpdatesBatchPeriod)).
		Watches(&kueue.MultiKueueCluster{}, &allAtOnceClusterHandler{client: r.client}).
		WithLogConstructor(roletracker.NewLogConstructor(r.roleTracker, AllAtOnceDispatcherControllerName)).
		Complete(core.WithLeadingManager(mgr, r, &kueue.Workload{}, cfg))
}

func NewAllAtOnceDispatcherReconciler(c client.Client, helper *admissioncheck.MultiKueueStoreHelper, roleTracker *roletracker.RoleTracker) *AllAtOnceDispatcherReconciler {
	return &AllAtOnceDispatcherReconciler{dispatcher: newDispatcher(c, helper, roleTracker)}
}

func (r *AllAtOnceDispatcherReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	wl, remoteClusters, err := r.workloadToNominate(ctx, req, nil)
	if wl == nil || err != nil {
		return reconcile.Result{}, err
	}

	// The workload is being evicted; let the core eviction flow complete (the Job
	// reconciler clears the quota reservation once the job is no longer active, and
	// the Workload controller requeues the workload) before re-nominating clusters.
	// Re-nominating during eviction races with the post-eviction cleanup and prevents
	// the workload from re-entering the queue.
	if workloadevict.IsEvicted(wl) {
		log.V(3).Info("Workload is being evicted, skip the reconciliation")
		return reconcile.Result{}, nil
	}

	activeClusters, err := r.filterActiveClusters(ctx, remoteClusters)
	if err != nil {
		log.Error(err, "Failed to filter active clusters")
		return reconcile.Result{}, err
	}

	log.V(3).Info("Nominate Worker Clusters with AllAtOnce Dispatcher")
	return r.nominateWorkers(ctx, wl, activeClusters, log)
}

// filterActiveClusters returns the subset of remoteClusters whose MultiKueueCluster
// exists and is Active, so that missing or inactive clusters are not nominated.
func (r *AllAtOnceDispatcherReconciler) filterActiveClusters(ctx context.Context, remoteClusters []string) (sets.Set[string], error) {
	active := sets.New[string]()
	for _, clusterName := range remoteClusters {
		cluster := &kueue.MultiKueueCluster{}
		if err := r.client.Get(ctx, types.NamespacedName{Name: clusterName}, cluster); err != nil {
			if client.IgnoreNotFound(err) != nil {
				return nil, err
			}
			continue
		}
		if isActive(cluster) {
			active.Insert(clusterName)
		}
	}
	return active, nil
}

func (r *AllAtOnceDispatcherReconciler) nominateWorkers(ctx context.Context, wl *kueue.Workload, remoteClusters sets.Set[string], log logr.Logger) (reconcile.Result, error) {
	nominatedWorkers := sets.List(remoteClusters)

	if equality.Semantic.DeepEqual(wl.Status.NominatedClusterNames, nominatedWorkers) {
		log.V(5).Info("Nominated cluster names already up to date, skip the reconciliation", "nominatedClusterNames", nominatedWorkers)
		return reconcile.Result{}, nil
	}

	log.V(5).Info("Nominating worker clusters", "nominatedClusterNames", nominatedWorkers)
	return reconcile.Result{}, r.nominate(ctx, wl, nominatedWorkers)
}

// allAtOnceClusterHandler requeues the Workloads of the MultiKueueConfigs that list
// a MultiKueueCluster when the cluster becomes Active or inactive, or is deleted.
// Creating a cluster needs no requeue: it cannot be Active before its first update.
type allAtOnceClusterHandler struct {
	client client.Client
}

var _ handler.EventHandler = (*allAtOnceClusterHandler)(nil)

func (h *allAtOnceClusterHandler) Create(context.Context, event.CreateEvent, workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

func (h *allAtOnceClusterHandler) Update(ctx context.Context, e event.UpdateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	oldCluster, isOld := e.ObjectOld.(*kueue.MultiKueueCluster)
	newCluster, isNew := e.ObjectNew.(*kueue.MultiKueueCluster)
	if !isOld || !isNew || isActive(oldCluster) == isActive(newCluster) {
		return
	}
	if err := h.queueWorkloads(ctx, newCluster.Name, q); err != nil {
		ctrl.LoggerFrom(ctx).V(2).Error(err, "Failed to queue workloads on cluster update", "multiKueueCluster", klog.KObj(newCluster))
	}
}

func (h *allAtOnceClusterHandler) Delete(ctx context.Context, e event.DeleteEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	cluster, isCluster := e.Object.(*kueue.MultiKueueCluster)
	if !isCluster {
		return
	}
	if err := h.queueWorkloads(ctx, cluster.Name, q); err != nil {
		ctrl.LoggerFrom(ctx).V(2).Error(err, "Failed to queue workloads on cluster delete", "multiKueueCluster", klog.KObj(cluster))
	}
}

func (h *allAtOnceClusterHandler) Generic(context.Context, event.GenericEvent, workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

func (h *allAtOnceClusterHandler) queueWorkloads(ctx context.Context, clusterName string, q workqueue.TypedRateLimitingInterface[reconcile.Request]) error {
	configs := &kueue.MultiKueueConfigList{}
	if err := h.client.List(ctx, configs, client.MatchingFields{multikueue.UsingMultiKueueClusters: clusterName}); err != nil {
		return err
	}
	var errs []error
	for _, config := range configs.Items {
		if err := multikueue.QueueWorkloadsForConfig(ctx, h.client, config.Name, constants.UpdatesBatchPeriod, q); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// isActive reports whether cluster can be nominated.
func isActive(cluster *kueue.MultiKueueCluster) bool {
	return apimeta.IsStatusConditionTrue(cluster.Status.Conditions, kueue.MultiKueueClusterActive)
}
