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

package multikueue

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"golang.org/x/exp/slices"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	"k8s.io/utils/clock"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
	"sigs.k8s.io/kueue/pkg/util/api"
	clientutil "sigs.k8s.io/kueue/pkg/util/client"
	utilmaps "sigs.k8s.io/kueue/pkg/util/maps"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
)

var (
	realClock = clock.RealClock{}
)

type wlReconciler struct {
	client            client.Client
	helper            *admissioncheck.MultiKueueStoreHelper
	clusters          *clustersReconciler
	origin            string
	workerLostTimeout time.Duration
	deletedWlCache    *utilmaps.SyncMap[string, *kueue.Workload]
	eventsBatchPeriod time.Duration
	adapters          map[string]jobframework.MultiKueueAdapter
	recorder          record.EventRecorder
	clock             clock.Clock
	dispatcherName    string
}

var _ reconcile.Reconciler = (*wlReconciler)(nil)

type wlGroup struct {
	local         *kueue.Workload
	remotes       map[string]*kueue.Workload
	remoteClients map[string]*remoteClient
	acName        kueue.AdmissionCheckReference
	jobAdapter    jobframework.MultiKueueAdapter
	controllerKey types.NamespacedName
}

type Option func(reconciler *wlReconciler)

func WithClock(_ testing.TB, c clock.Clock) Option {
	return func(r *wlReconciler) {
		r.clock = c
	}
}

// IsFinished returns true if the local workload is finished.
func (g *wlGroup) IsFinished() bool {
	return apimeta.IsStatusConditionTrue(g.local.Status.Conditions, kueue.WorkloadFinished)
}

// IsElasticWorkload returns true if the workload is considered elastic,
// meaning the ElasticJobsViaWorkloadSlices feature is enabled and the
// workload has the corresponding annotation set.
func (g *wlGroup) IsElasticWorkload() bool {
	return workloadslicing.IsElasticWorkload(g.local)
}

// FirstReserving returns true if there is a workload reserving quota,
// the string identifies the remote cluster.
func (g *wlGroup) FirstReserving() (bool, string) {
	found := false
	bestMatch := ""
	var bestTime time.Time
	for remote, wl := range g.remotes {
		if wl == nil {
			continue
		}
		c := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadQuotaReserved)
		if c != nil && c.Status == metav1.ConditionTrue && (!found || bestTime.IsZero() || c.LastTransitionTime.Time.Before(bestTime)) {
			found = true
			bestMatch = remote
			bestTime = c.LastTransitionTime.Time
		}
	}
	return found, bestMatch
}

func (g *wlGroup) RemoteFinishedCondition() (*metav1.Condition, string) {
	var bestMatch *metav1.Condition
	bestMatchRemote := ""
	for remote, wl := range g.remotes {
		if wl == nil {
			continue
		}
		if c := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadFinished); c != nil && c.Status == metav1.ConditionTrue && (bestMatch == nil || c.LastTransitionTime.Before(&bestMatch.LastTransitionTime)) {
			bestMatch = c
			bestMatchRemote = remote
		}
	}
	return bestMatch, bestMatchRemote
}

func (g *wlGroup) RemoveRemoteObjects(ctx context.Context, cluster string) error {
	remWl := g.remotes[cluster]
	if remWl == nil {
		return nil
	}
	if err := g.jobAdapter.DeleteRemoteObject(ctx, g.remoteClients[cluster].client, g.controllerKey); err != nil {
		return fmt.Errorf("deleting remote controller object: %w", err)
	}

	if controllerutil.RemoveFinalizer(remWl, kueue.ResourceInUseFinalizerName) {
		if err := g.remoteClients[cluster].client.Update(ctx, remWl); err != nil {
			return fmt.Errorf("removing remote workloads finalizer: %w", err)
		}
	}

	err := g.remoteClients[cluster].client.Delete(ctx, remWl)
	if client.IgnoreNotFound(err) != nil {
		return fmt.Errorf("deleting remote workload: %w", err)
	}
	g.remotes[cluster] = nil
	return nil
}

func (w *wlReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	log.V(2).Info("Reconcile Workload")

	wl := &kueue.Workload{}
	isDeleted := false
	err := w.client.Get(ctx, req.NamespacedName, wl)
	switch {
	case client.IgnoreNotFound(err) != nil:
		return reconcile.Result{}, err
	case err != nil:
		oldWl, found := w.deletedWlCache.Get(req.String())
		if !found {
			return reconcile.Result{}, nil
		}
		wl = oldWl
		isDeleted = true

	default:
		isDeleted = !wl.DeletionTimestamp.IsZero()
	}

	mkAc, err := admissioncheck.GetMultiKueueAdmissionCheck(ctx, w.client, wl)
	if err != nil {
		return reconcile.Result{}, err
	}

	if mkAc == nil || mkAc.State == kueue.CheckStateRejected {
		log.V(2).Info("Skip Workload", "isDeleted", isDeleted)
		if isDeleted {
			// Delete the workload from the cache considering the following case:
			//    The workload is not admitted by MultiKueue and so there are
			//    no workloads on worker clusters created, we can safely drop it
			//    from the cache.
			//    TODO(#3840): Ideally, we would not add it to the cache in the
			//    first place.
			w.deletedWlCache.Delete(req.String())
		}
		return reconcile.Result{}, nil
	}

	adapter, owner := w.adapter(wl)
	if adapter == nil {
		// Reject the workload since there is no chance for it to run.
		var rejectionMessage string
		if owner != nil {
			rejectionMessage = fmt.Sprintf("No multikueue adapter found for owner kind %q", schema.FromAPIVersionAndKind(owner.APIVersion, owner.Kind).String())
		} else {
			rejectionMessage = "No multikueue adapter found"
		}
		return reconcile.Result{}, w.updateACS(ctx, wl, mkAc, kueue.CheckStateRejected, rejectionMessage)
	}

	// If the workload is deleted there is a chance that it's owner is also deleted. In that case
	// we skip calling `IsJobManagedByKueue` as its output would not be reliable.
	if !isDeleted {
		managed, unmanagedReason, err := adapter.IsJobManagedByKueue(ctx, w.client, types.NamespacedName{Name: owner.Name, Namespace: wl.Namespace})
		if err != nil {
			return reconcile.Result{}, err
		}

		if !managed {
			return reconcile.Result{}, w.updateACS(ctx, wl, mkAc, kueue.CheckStateRejected, fmt.Sprintf("The owner is not managed by Kueue: %s", unmanagedReason))
		}
	}

	grp, err := w.readGroup(ctx, wl, mkAc.Name, adapter, owner.Name)
	if err != nil {
		return reconcile.Result{}, err
	}

	if isDeleted {
		for cluster := range grp.remotes {
			err := grp.RemoveRemoteObjects(ctx, cluster)
			if err != nil {
				return reconcile.Result{}, err
			}
		}
		w.deletedWlCache.Delete(req.String())
		return reconcile.Result{}, nil
	}

	return w.reconcileGroup(ctx, grp)
}

func (w *wlReconciler) updateACS(ctx context.Context, wl *kueue.Workload, acs *kueue.AdmissionCheckState, status kueue.CheckState, message string) error {
	acs.State = status
	acs.Message = message
	acs.LastTransitionTime = metav1.NewTime(w.clock.Now())
	wlPatch := workload.BaseSSAWorkload(wl, true)
	workload.SetAdmissionCheckState(&wlPatch.Status.AdmissionChecks, *acs, w.clock)
	return w.client.Status().Patch(ctx, wlPatch, client.Apply, client.FieldOwner(kueue.MultiKueueControllerName), client.ForceOwnership)
}

func (w *wlReconciler) remoteClientsForAC(ctx context.Context, acName kueue.AdmissionCheckReference) (map[string]*remoteClient, error) {
	cfg, err := w.helper.ConfigForAdmissionCheck(ctx, acName)
	if err != nil {
		return nil, err
	}
	clients := make(map[string]*remoteClient, len(cfg.Spec.Clusters))
	for _, clusterName := range cfg.Spec.Clusters {
		if client, found := w.clusters.controllerFor(clusterName); found {
			// Skip the client if its reconnect is ongoing.
			if !client.connecting.Load() {
				clients[clusterName] = client
			}
		}
	}
	if len(clients) == 0 {
		return nil, admissioncheck.ErrNoActiveClusters
	}
	return clients, nil
}

func (w *wlReconciler) adapter(local *kueue.Workload) (jobframework.MultiKueueAdapter, *metav1.OwnerReference) {
	if controller := metav1.GetControllerOf(local); controller != nil {
		adapterKey := schema.FromAPIVersionAndKind(controller.APIVersion, controller.Kind).String()
		return w.adapters[adapterKey], controller
	} else if refs := local.GetOwnerReferences(); len(refs) > 0 {
		// For workloads without a controller but with owner references,
		// use the first owner reference to find the adapter. This supports composable workloads.
		adapterKey := schema.FromAPIVersionAndKind(refs[0].APIVersion, refs[0].Kind).String()
		return w.adapters[adapterKey], &refs[0]
	}
	return nil, nil
}

func (w *wlReconciler) readGroup(ctx context.Context, local *kueue.Workload, acName kueue.AdmissionCheckReference, adapter jobframework.MultiKueueAdapter, controllerName string) (*wlGroup, error) {
	rClients, err := w.remoteClientsForAC(ctx, acName)
	if err != nil {
		return nil, fmt.Errorf("admission check %q: %w", acName, err)
	}

	grp := wlGroup{
		local:         local,
		remotes:       make(map[string]*kueue.Workload, len(rClients)),
		remoteClients: rClients,
		acName:        acName,
		jobAdapter:    adapter,
		controllerKey: types.NamespacedName{Name: controllerName, Namespace: local.Namespace},
	}

	for remote, rClient := range rClients {
		wl := &kueue.Workload{}
		err := rClient.client.Get(ctx, client.ObjectKeyFromObject(local), wl)
		if client.IgnoreNotFound(err) != nil {
			return nil, err
		}
		if err != nil {
			wl = nil
		}
		grp.remotes[remote] = wl
	}
	return &grp, nil
}

func (w *wlReconciler) reconcileGroup(ctx context.Context, group *wlGroup) (reconcile.Result, error) {
	log := ctrl.LoggerFrom(ctx).WithValues("op", "reconcileGroup")
	log.V(3).Info("Reconcile Workload Group")

	acs := admissioncheck.FindAdmissionCheck(group.local.Status.AdmissionChecks, group.acName)

	// 0. Ignore Elastic workloads Finished when:
	// - Workload is "Finished" as a result workload slice replacement, OR
	// - Workload doesn't have quota reservation as a result of scale-up, i.e., scaling-up in progress.
	if group.IsElasticWorkload() &&
		((group.IsFinished() && workloadslicing.IsReplaced(group.local.Status)) ||
			(!workload.HasQuotaReservation(group.local) && workloadslicing.ScaledUp(group.local))) {
		return reconcile.Result{}, nil
	}

	// 1. delete all remote workloads when local workload is finished or has no quota reservation.
	if group.IsFinished() || !workload.HasQuotaReservation(group.local) {
		var errs []error
		for rem := range group.remotes {
			if err := group.RemoveRemoteObjects(ctx, rem); err != nil {
				errs = append(errs, err)
				log.V(2).Error(err, "Deleting remote workload", "workerCluster", rem)
			}
		}
		return reconcile.Result{}, errors.Join(errs...)
	}

	if remoteFinishedCond, remote := group.RemoteFinishedCondition(); remoteFinishedCond != nil {
		// NOTE: we can have a race condition setting the wl status here and it being updated by the job controller
		// it should not be problematic but the "From remote xxxx:" could be lost ....

		if group.jobAdapter != nil {
			if err := group.jobAdapter.SyncJob(ctx, w.client, group.remoteClients[remote].client, group.controllerKey, group.local.Name, w.origin); err != nil {
				log.V(2).Error(err, "copying remote controller status", "workerCluster", remote)
				// we should retry this
				return reconcile.Result{}, err
			}
		} else {
			log.V(3).Info("Group with no adapter, skip owner status copy", "workerCluster", remote)
		}

		// copy the status to the local one
		finishCond := metav1.Condition{
			Type:               kueue.WorkloadFinished,
			Status:             metav1.ConditionTrue,
			Reason:             remoteFinishedCond.Reason,
			Message:            remoteFinishedCond.Message,
			LastTransitionTime: metav1.NewTime(w.clock.Now()),
		}
		if features.Enabled(features.WorkloadRequestUseMergePatch) {
			return reconcile.Result{}, clientutil.PatchStatus(ctx, w.client, group.local, func() (client.Object, bool, error) {
				apimeta.SetStatusCondition(&group.local.Status.Conditions, finishCond)
				return group.local, true, nil
			})
		}

		wlPatch := workload.BaseSSAWorkload(group.local, false)
		apimeta.SetStatusCondition(&wlPatch.Status.Conditions, finishCond)
		return reconcile.Result{}, w.client.Status().Patch(ctx, wlPatch, client.Apply, client.FieldOwner(kueue.MultiKueueControllerName+"-finish"), client.ForceOwnership)
	}

	// 2. delete all workloads that are out of sync (other than scaled-down elastic workloads)
	// or are not in the chosen worker.
	for rem, remWl := range group.remotes {
		if remWl != nil && !equality.Semantic.DeepEqual(group.local.Spec, remWl.Spec) {
			// For elastic workloads detect a scale-down event and propagate changes to the remote.
			if group.IsElasticWorkload() && workloadslicing.ScaledDown(workload.ExtractPodSetCountsFromWorkload(remWl), workload.ExtractPodSetCountsFromWorkload(group.local)) {
				remWl.Spec = group.local.Spec
				if err := group.remoteClients[rem].client.Update(ctx, remWl); err != nil {
					return reconcile.Result{}, fmt.Errorf("failed to update remote workload slice: %w", err)
				}
				continue
			}

			if err := client.IgnoreNotFound(group.RemoveRemoteObjects(ctx, rem)); err != nil {
				log.V(2).Error(err, "Deleting out of sync remote objects", "remote", rem)
				return reconcile.Result{}, err
			}
			group.remotes[rem] = nil
		}
	}

	// 3. get the first reserving
	hasReserving, reservingRemote := group.FirstReserving()
	if hasReserving {
		// remove the non-reserving worker workloads
		for rem, remWl := range group.remotes {
			if remWl != nil && rem != reservingRemote {
				if err := client.IgnoreNotFound(group.RemoveRemoteObjects(ctx, rem)); err != nil {
					log.V(2).Error(err, "Deleting out of sync remote objects", "remote", rem)
					return reconcile.Result{}, err
				}
				group.remotes[rem] = nil
			}
		}

		acs := admissioncheck.FindAdmissionCheck(group.local.Status.AdmissionChecks, group.acName)
		if err := group.jobAdapter.SyncJob(ctx, w.client, group.remoteClients[reservingRemote].client, group.controllerKey, group.local.Name, w.origin); err != nil {
			log.V(2).Error(err, "creating remote controller object", "remote", reservingRemote)
			// We'll retry this in the next reconcile.
			return reconcile.Result{}, err
		}

		if err := w.syncReservingRemoteState(ctx, group, reservingRemote, acs); err != nil {
			return reconcile.Result{}, err
		}
		return reconcile.Result{RequeueAfter: w.workerLostTimeout}, nil
	} else if acs.State == kueue.CheckStateReady {
		// If there is no reserving and the AC is ready, the connection with the reserving remote might
		// be lost, keep the workload admitted for keepReadyTimeout and put it back in the queue after that.
		remainingWaitTime := w.workerLostTimeout - time.Since(acs.LastTransitionTime.Time)
		if remainingWaitTime > 0 {
			log.V(3).Info("Reserving remote lost, retry", "retryAfter", remainingWaitTime)
			return reconcile.Result{RequeueAfter: remainingWaitTime}, nil
		} else {
			acs.State = kueue.CheckStateRetry
			acs.Message = "Reserving remote lost"
			acs.LastTransitionTime = metav1.NewTime(w.clock.Now())
			wlPatch := workload.BaseSSAWorkload(group.local, true)
			workload.SetAdmissionCheckState(&wlPatch.Status.AdmissionChecks, *acs, w.clock)
			return reconcile.Result{}, w.client.Status().Patch(ctx, wlPatch, client.Apply, client.FieldOwner(kueue.MultiKueueControllerName), client.ForceOwnership)
		}
	}

	return w.nominateAndSynchronizeWorkers(ctx, group)
}

func (w *wlReconciler) nominateAndSynchronizeWorkers(ctx context.Context, group *wlGroup) (reconcile.Result, error) {
	log := ctrl.LoggerFrom(ctx).WithValues("op", "nominateAndSynchronizeWorkers")
	log.V(3).Info("Nominate and Synchronize Worker Clusters")
	var nominatedWorkers []string

	// For elastic workloads, retrieve the remote cluster where the original workload was scheduled.
	// For now, new workload slices will continue to be assigned to the same cluster.
	// In the future, we may introduce more nuanced remote workload propagation policies,
	// supporting preferred or required placement constraints.
	if clusterName := workload.ClusterName(group.local); group.IsElasticWorkload() && clusterName != "" {
		nominatedWorkers = []string{clusterName}
	} else if w.dispatcherName == config.MultiKueueDispatcherModeAllAtOnce {
		for workerName := range group.remotes {
			nominatedWorkers = append(nominatedWorkers, workerName)
		}
		if group.local.Status.ClusterName == nil && !equality.Semantic.DeepEqual(group.local.Status.NominatedClusterNames, nominatedWorkers) {
			if err := workload.PatchAdmissionStatus(ctx, w.client, group.local, w.clock, func() (*kueue.Workload, bool, error) {
				group.local.Status.NominatedClusterNames = nominatedWorkers
				return group.local, true, nil
			}); err != nil {
				log.V(2).Error(err, "Failed to patch nominated clusters", "workload", klog.KObj(group.local))
				return reconcile.Result{}, err
			}
		}
	} else {
		// Incremental dispatcher and External dispatcher path
		nominatedWorkers = group.local.Status.NominatedClusterNames
	}

	log.V(4).Info("Synchronize nominated worker clusters", "dispatcherName", w.dispatcherName, "nominatedWorkerClusterNames", nominatedWorkers)

	var errs []error
	for rem, remoteWl := range group.remotes {
		if slices.Contains(nominatedWorkers, rem) {
			if remoteWl == nil {
				clone := cloneForCreate(group.local, group.remoteClients[rem].origin)
				if err := group.remoteClients[rem].client.Create(ctx, clone); err != nil {
					log.V(2).Error(err, "creating remote object", "remote", rem)
					errs = append(errs, err)
				}
			}
		} else if remoteWl != nil {
			if err := client.IgnoreNotFound(group.RemoveRemoteObjects(ctx, rem)); err != nil {
				log.V(2).Error(err, "removing non-nominated remote object", "remote", rem)
				errs = append(errs, err)
			}
			group.remotes[rem] = nil
		}
	}
	return reconcile.Result{}, errors.Join(errs...)
}

func (w *wlReconciler) Create(_ event.CreateEvent) bool {
	return true
}

func (w *wlReconciler) Delete(de event.DeleteEvent) bool {
	if wl, isWl := de.Object.(*kueue.Workload); isWl && !de.DeleteStateUnknown {
		w.deletedWlCache.Add(client.ObjectKeyFromObject(wl).String(), wl)
	}
	return true
}

func (w *wlReconciler) Update(_ event.UpdateEvent) bool {
	return true
}

func (w *wlReconciler) Generic(_ event.GenericEvent) bool {
	return true
}

func newWlReconciler(c client.Client, helper *admissioncheck.MultiKueueStoreHelper, cRec *clustersReconciler, origin string,
	recorder record.EventRecorder, workerLostTimeout, eventsBatchPeriod time.Duration,
	adapters map[string]jobframework.MultiKueueAdapter, dispatcherName string,
	options ...Option,
) *wlReconciler {
	r := &wlReconciler{
		client:            c,
		helper:            helper,
		clusters:          cRec,
		origin:            origin,
		workerLostTimeout: workerLostTimeout,
		deletedWlCache:    utilmaps.NewSyncMap[string, *kueue.Workload](0),
		eventsBatchPeriod: eventsBatchPeriod,
		adapters:          adapters,
		recorder:          recorder,
		clock:             realClock,
		dispatcherName:    dispatcherName,
	}
	for _, option := range options {
		option(r)
	}
	return r
}

type configHandler struct {
	client            client.Client
	eventsBatchPeriod time.Duration
}

func (c *configHandler) Create(context.Context, event.CreateEvent, workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	// no-op as we don't need to react to new configs
}

func (c *configHandler) Update(ctx context.Context, e event.UpdateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	oldConfig, isOldConfig := e.ObjectOld.(*kueue.MultiKueueConfig)
	newConfig, isNewConfig := e.ObjectNew.(*kueue.MultiKueueConfig)
	if !isOldConfig || !isNewConfig {
		return
	}
	if equality.Semantic.DeepEqual(oldConfig.Spec.Clusters, newConfig.Spec.Clusters) {
		return
	}
	if err := c.queueWorkloadsForConfig(ctx, oldConfig.Name, q); err != nil {
		ctrl.LoggerFrom(ctx).V(2).Error(err, "Failed to queue workloads on config update", "multiKueueConfig", klog.KObj(oldConfig))
	}
}

func (c *configHandler) Delete(ctx context.Context, e event.DeleteEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	config, isConfig := e.Object.(*kueue.MultiKueueConfig)
	if !isConfig {
		return
	}
	if err := c.queueWorkloadsForConfig(ctx, config.Name, q); err != nil {
		ctrl.LoggerFrom(ctx).V(2).Error(err, "Failed to queue workloads on config deletion", "multiKueueConfig", klog.KObj(config))
	}
}

func (c *configHandler) Generic(context.Context, event.GenericEvent, workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	// no-op as we don't need to react to generic
}

func (c *configHandler) queueWorkloadsForConfig(ctx context.Context, configName string, q workqueue.TypedRateLimitingInterface[reconcile.Request]) error {
	admissionChecks := &kueue.AdmissionCheckList{}
	var errs []error

	if err := c.client.List(ctx, admissionChecks, client.MatchingFields{AdmissionCheckUsingConfigKey: configName}); err != nil {
		errs = append(errs, err)
		return errors.Join(errs...)
	}

	for _, admissionCheck := range admissionChecks.Items {
		workloads := &kueue.WorkloadList{}
		if err := c.client.List(ctx, workloads, client.MatchingFields{WorkloadsWithAdmissionCheckKey: admissionCheck.Name}); err != nil {
			errs = append(errs, err)
			continue
		}
		for _, workload := range workloads.Items {
			q.AddAfter(reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&workload)}, c.eventsBatchPeriod)
		}
	}
	return errors.Join(errs...)
}

func (w *wlReconciler) setupWithManager(mgr ctrl.Manager) error {
	syncHndl := handler.Funcs{
		GenericFunc: func(_ context.Context, e event.GenericEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			q.AddAfter(reconcile.Request{NamespacedName: types.NamespacedName{
				Namespace: e.Object.GetNamespace(),
				Name:      e.Object.GetName(),
			}}, w.eventsBatchPeriod)
		},
	}

	return ctrl.NewControllerManagedBy(mgr).
		Named("multikueue_workload").
		For(&kueue.Workload{}).
		WatchesRawSource(source.Channel(w.clusters.wlUpdateCh, syncHndl)).
		Watches(&kueue.MultiKueueConfig{}, &configHandler{client: w.client, eventsBatchPeriod: w.eventsBatchPeriod}).
		WithEventFilter(w).
		Complete(w)
}

func findPodSetAssignment(assignments []kueue.PodSetAssignment, name kueue.PodSetReference) *kueue.PodSetAssignment {
	for i := range assignments {
		if assignments[i].Name == name {
			return &assignments[i]
		}
	}
	return nil
}

func needsDelayedTopologyUpdate(local, remote *kueue.Workload) bool {
	if remote == nil || remote.Status.Admission == nil || local == nil || local.Status.Admission == nil {
		return false
	}

	for _, remotePSA := range remote.Status.Admission.PodSetAssignments {
		if remotePSA.TopologyAssignment == nil {
			continue
		}

		localPSA := findPodSetAssignment(local.Status.Admission.PodSetAssignments, remotePSA.Name)
		if localPSA == nil {
			continue
		}

		if localPSA.TopologyAssignment == nil {
			return true
		}

		if localPSA.DelayedTopologyRequest != nil &&
			*localPSA.DelayedTopologyRequest == kueue.DelayedTopologyRequestStatePending {
			return true
		}
	}
	return false
}

func (w *wlReconciler) syncReservingRemoteState(ctx context.Context, group *wlGroup, reservingRemote string, acs *kueue.AdmissionCheckState) error {
	log := ctrl.LoggerFrom(ctx)

	needsTopologyUpdate := needsDelayedTopologyUpdate(group.local, group.remotes[reservingRemote])
	needsACUpdate := acs.State != kueue.CheckStateRetry && acs.State != kueue.CheckStateRejected

	if !needsTopologyUpdate && !needsACUpdate {
		return nil
	}

	if err := workload.PatchAdmissionStatus(ctx, w.client, group.local, w.clock, func() (*kueue.Workload, bool, error) {
		if needsTopologyUpdate {
			updateDelayedTopologyRequest(group.local, group.remotes[reservingRemote])
		}

		if needsACUpdate {
			if group.jobAdapter.KeepAdmissionCheckPending() {
				acs.State = kueue.CheckStatePending
			} else {
				acs.State = kueue.CheckStateReady
			}
			// update the message
			acs.Message = fmt.Sprintf("The workload got reservation on %q", reservingRemote)
			// update the transition time since is used to detect the lost worker state.
			acs.LastTransitionTime = metav1.NewTime(w.clock.Now())

			workload.SetAdmissionCheckState(&group.local.Status.AdmissionChecks, *acs, w.clock)
			// Set the cluster name to the reserving remote and clear the nominated clusters.
			group.local.Status.ClusterName = &reservingRemote
			group.local.Status.NominatedClusterNames = nil
		}

		return group.local, true, nil
	}); err != nil {
		log.V(2).Error(err, "Failed to patch workload", "workload", klog.KObj(group.local))
		return err
	}

	if needsACUpdate {
		w.recorder.Eventf(group.local, corev1.EventTypeNormal, "MultiKueue", acs.Message)
	}

	return nil
}

func updateDelayedTopologyRequest(local, remote *kueue.Workload) {
	if remote == nil || remote.Status.Admission == nil || local == nil || local.Status.Admission == nil {
		return
	}

	for _, remotePSA := range remote.Status.Admission.PodSetAssignments {
		if remotePSA.TopologyAssignment == nil {
			continue
		}

		localPSA := findPodSetAssignment(local.Status.Admission.PodSetAssignments, remotePSA.Name)
		if localPSA == nil {
			continue
		}

		if localPSA.DelayedTopologyRequest != nil &&
			*localPSA.DelayedTopologyRequest == kueue.DelayedTopologyRequestStatePending {
			localPSA.DelayedTopologyRequest = ptr.To(kueue.DelayedTopologyRequestStateReady)
		}
	}
}

func cloneForCreate(orig *kueue.Workload, origin string) *kueue.Workload {
	remoteWl := &kueue.Workload{}
	remoteWl.ObjectMeta = api.CloneObjectMetaForCreation(&orig.ObjectMeta)
	if remoteWl.Labels == nil {
		remoteWl.Labels = make(map[string]string)
	}
	remoteWl.Labels[kueue.MultiKueueOriginLabel] = origin
	orig.Spec.DeepCopyInto(&remoteWl.Spec)
	return remoteWl
}
