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

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/equality"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/utils/clock"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueueconfig "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/admissionchecks/multikueue"
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
		Watches(&kueue.MultiKueueConfig{}, &allAtOnceConfigHandler{client: r.client}).
		Watches(&kueue.MultiKueueCluster{}, &allAtOnceClusterHandler{client: r.client}).
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

	// The workload is being evicted; let the core eviction flow complete (the Job
	// reconciler will UnsetQuotaReservation once the job is no longer active, and
	// the scheduler will requeue it) before re-nominating clusters. Re-nominating
	// during eviction races with the post-eviction cleanup and prevents the
	// workload from re-entering the queue.
	if workload.IsEvicted(wl) {
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
// has the MultiKueueClusterActive condition set to True. Clusters that are missing
// or not active are excluded so they are not nominated for workload placement.
func (r *AllAtOnceDispatcherReconciler) filterActiveClusters(ctx context.Context, remoteClusters sets.Set[string]) (sets.Set[string], error) {
	active := sets.New[string]()
	for clusterName := range remoteClusters {
		cluster := &kueue.MultiKueueCluster{}
		if err := r.client.Get(ctx, types.NamespacedName{Name: clusterName}, cluster); err != nil {
			if client.IgnoreNotFound(err) != nil {
				return nil, err
			}
			// Missing cluster: skip.
			continue
		}
		if apimeta.IsStatusConditionTrue(cluster.Status.Conditions, kueue.MultiKueueClusterActive) {
			active.Insert(clusterName)
		}
	}
	return active, nil
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

// Inline version had access to config and cluster handlers for free, we need to add them here.
// allAtOnceConfigHandler enqueues all Workloads referencing AdmissionChecks that
// use a given MultiKueueConfig whenever the config is created, updated or deleted.
type allAtOnceConfigHandler struct {
	client client.Client
}

var _ handler.EventHandler = (*allAtOnceConfigHandler)(nil)

func (h *allAtOnceConfigHandler) Create(ctx context.Context, e event.CreateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	cfg, ok := e.Object.(*kueue.MultiKueueConfig)
	if !ok {
		return
	}
	if err := queueWorkloadsForConfig(ctx, h.client, cfg.Name, q); err != nil {
		ctrl.LoggerFrom(ctx).V(2).Error(err, "Failed to queue workloads on config create", "multiKueueConfig", cfg.Name)
	}
}

func (h *allAtOnceConfigHandler) Update(ctx context.Context, e event.UpdateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	oldCfg, isOld := e.ObjectOld.(*kueue.MultiKueueConfig)
	newCfg, isNew := e.ObjectNew.(*kueue.MultiKueueConfig)
	if !isOld || !isNew {
		return
	}
	if equality.Semantic.DeepEqual(oldCfg.Spec.Clusters, newCfg.Spec.Clusters) {
		return
	}
	if err := queueWorkloadsForConfig(ctx, h.client, newCfg.Name, q); err != nil {
		ctrl.LoggerFrom(ctx).V(2).Error(err, "Failed to queue workloads on config update", "multiKueueConfig", newCfg.Name)
	}
}

func (h *allAtOnceConfigHandler) Delete(ctx context.Context, e event.DeleteEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	cfg, ok := e.Object.(*kueue.MultiKueueConfig)
	if !ok {
		return
	}
	if err := queueWorkloadsForConfig(ctx, h.client, cfg.Name, q); err != nil {
		ctrl.LoggerFrom(ctx).V(2).Error(err, "Failed to queue workloads on config delete", "multiKueueConfig", cfg.Name)
	}
}

func (h *allAtOnceConfigHandler) Generic(context.Context, event.GenericEvent, workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

// allAtOnceClusterHandler enqueues all Workloads referencing AdmissionChecks
// whose MultiKueueConfig contains the cluster, but only when the cluster's
// activity status (MultiKueueClusterActive) actually transitions.
type allAtOnceClusterHandler struct {
	client client.Client
}

var _ handler.EventHandler = (*allAtOnceClusterHandler)(nil)

func (h *allAtOnceClusterHandler) Create(ctx context.Context, e event.CreateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	cluster, ok := e.Object.(*kueue.MultiKueueCluster)
	if !ok {
		return
	}
	if err := h.queueForCluster(ctx, cluster.Name, q); err != nil {
		ctrl.LoggerFrom(ctx).V(2).Error(err, "Failed to queue workloads on cluster create", "multiKueueCluster", cluster.Name)
	}
}

func (h *allAtOnceClusterHandler) Update(ctx context.Context, e event.UpdateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	oldCluster, isOld := e.ObjectOld.(*kueue.MultiKueueCluster)
	newCluster, isNew := e.ObjectNew.(*kueue.MultiKueueCluster)
	if !isOld || !isNew {
		return
	}
	oldActive := apimeta.FindStatusCondition(oldCluster.Status.Conditions, kueue.MultiKueueClusterActive)
	newActive := apimeta.FindStatusCondition(newCluster.Status.Conditions, kueue.MultiKueueClusterActive)
	if conditionStatusEqual(oldActive, newActive) {
		return
	}
	if err := h.queueForCluster(ctx, newCluster.Name, q); err != nil {
		ctrl.LoggerFrom(ctx).V(2).Error(err, "Failed to queue workloads on cluster update", "multiKueueCluster", newCluster.Name)
	}
}

func (h *allAtOnceClusterHandler) Delete(ctx context.Context, e event.DeleteEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	cluster, ok := e.Object.(*kueue.MultiKueueCluster)
	if !ok {
		return
	}
	if err := h.queueForCluster(ctx, cluster.Name, q); err != nil {
		ctrl.LoggerFrom(ctx).V(2).Error(err, "Failed to queue workloads on cluster delete", "multiKueueCluster", cluster.Name)
	}
}

func (h *allAtOnceClusterHandler) Generic(context.Context, event.GenericEvent, workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

func (h *allAtOnceClusterHandler) queueForCluster(ctx context.Context, clusterName string, q workqueue.TypedRateLimitingInterface[reconcile.Request]) error {
	configs := &kueue.MultiKueueConfigList{}
	if err := h.client.List(ctx, configs, client.MatchingFields{multikueue.UsingMultiKueueClusters: clusterName}); err != nil {
		return err
	}
	var errs []error
	for _, cfg := range configs.Items {
		if err := queueWorkloadsForConfig(ctx, h.client, cfg.Name, q); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// queueWorkloadsForConfig enqueues every workload that has a pending MultiKueue
// admission check whose MultiKueueConfig parameter matches the given config name.
func queueWorkloadsForConfig(ctx context.Context, c client.Client, configName string, q workqueue.TypedRateLimitingInterface[reconcile.Request]) error {
	admissionChecks := &kueue.AdmissionCheckList{}
	if err := c.List(ctx, admissionChecks, client.MatchingFields{multikueue.AdmissionCheckUsingConfigKey: configName}); err != nil {
		return err
	}
	var errs []error
	for _, ac := range admissionChecks.Items {
		workloads := &kueue.WorkloadList{}
		if err := c.List(ctx, workloads, client.MatchingFields{multikueue.WorkloadsWithAdmissionCheckKey: ac.Name}); err != nil {
			errs = append(errs, err)
			continue
		}
		for _, wl := range workloads.Items {
			q.Add(reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&wl)})
		}
	}
	return errors.Join(errs...)
}

func conditionStatusEqual(a, b *metav1.Condition) bool {
	if a == b {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return a.Status == b.Status && a.Reason == b.Reason
}
