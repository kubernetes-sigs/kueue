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

package multikueue

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/utils/clock"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
	"sigs.k8s.io/kueue/pkg/util/api"
	utilmaps "sigs.k8s.io/kueue/pkg/util/maps"
	"sigs.k8s.io/kueue/pkg/workload"
)

var (
	realClock           = clock.RealClock{}
	errNoActiveClusters = errors.New("no active clusters")
)

type wlReconciler struct {
	client            client.Client
	helper            *multiKueueStoreHelper
	clusters          *clustersReconciler
	origin            string
	workerLostTimeout time.Duration
	deletedWlCache    *utilmaps.SyncMap[string, *kueue.Workload]
	eventsBatchPeriod time.Duration
	adapters          map[string]jobframework.MultiKueueAdapter
	clock             clock.Clock
}

var _ reconcile.Reconciler = (*wlReconciler)(nil)

type wlGroup struct {
	local         *kueue.Workload
	remotes       map[string]*kueue.Workload
	remoteClients map[string]*remoteClient
	acName        string
	jobAdapter    jobframework.MultiKueueAdapter
	controllerKey types.NamespacedName
}

type options struct {
	clock clock.Clock
}

type Option func(*options)

var defaultOptions = options{
	clock: realClock,
}

func WithClock(_ testing.TB, c clock.Clock) Option {
	return func(o *options) {
		o.clock = c
	}
}

// IsFinished returns true if the local workload is finished.
func (g *wlGroup) IsFinished() bool {
	return apimeta.IsStatusConditionTrue(g.local.Status.Conditions, kueue.WorkloadFinished)
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

	mkAc, err := w.multikueueAC(ctx, wl)
	if err != nil {
		return reconcile.Result{}, err
	}

	if mkAc == nil || mkAc.State == kueue.CheckStateRejected {
		log.V(2).Info("Skip Workload")
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
	wlPatch := workload.BaseSSAWorkload(wl)
	workload.SetAdmissionCheckState(&wlPatch.Status.AdmissionChecks, *acs)
	return w.client.Status().Patch(ctx, wlPatch, client.Apply, client.FieldOwner(kueue.MultiKueueControllerName), client.ForceOwnership)
}

func (w *wlReconciler) remoteClientsForAC(ctx context.Context, acName string) (map[string]*remoteClient, error) {
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
		return nil, errNoActiveClusters
	}
	return clients, nil
}

func (w *wlReconciler) multikueueAC(ctx context.Context, local *kueue.Workload) (*kueue.AdmissionCheckState, error) {
	relevantChecks, err := admissioncheck.FilterForController(ctx, w.client, local.Status.AdmissionChecks, kueue.MultiKueueControllerName)
	if err != nil {
		return nil, err
	}

	if len(relevantChecks) == 0 {
		return nil, nil
	}
	return workload.FindAdmissionCheck(local.Status.AdmissionChecks, relevantChecks[0]), nil
}

func (w *wlReconciler) adapter(local *kueue.Workload) (jobframework.MultiKueueAdapter, *metav1.OwnerReference) {
	if controller := metav1.GetControllerOf(local); controller != nil {
		adapterKey := schema.FromAPIVersionAndKind(controller.APIVersion, controller.Kind).String()
		return w.adapters[adapterKey], controller
	}
	return nil, nil
}

func (w *wlReconciler) readGroup(ctx context.Context, local *kueue.Workload, acName string, adapter jobframework.MultiKueueAdapter, controllerName string) (*wlGroup, error) {
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

	acs := workload.FindAdmissionCheck(group.local.Status.AdmissionChecks, group.acName)

	// 1. delete all remote workloads when finished or the local wl has no reservation
	if group.IsFinished() || !workload.HasQuotaReservation(group.local) {
		errs := []error{}
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
		wlPatch := workload.BaseSSAWorkload(group.local)
		apimeta.SetStatusCondition(&wlPatch.Status.Conditions, metav1.Condition{
			Type:    kueue.WorkloadFinished,
			Status:  metav1.ConditionTrue,
			Reason:  remoteFinishedCond.Reason,
			Message: remoteFinishedCond.Message,
		})
		return reconcile.Result{}, w.client.Status().Patch(ctx, wlPatch, client.Apply, client.FieldOwner(kueue.MultiKueueControllerName+"-finish"), client.ForceOwnership)
	}

	// 2. delete all workloads that are out of sync or are not in the chosen worker
	for rem, remWl := range group.remotes {
		if remWl != nil && !equality.Semantic.DeepEqual(group.local.Spec, remWl.Spec) {
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

		acs := workload.FindAdmissionCheck(group.local.Status.AdmissionChecks, group.acName)
		if err := group.jobAdapter.SyncJob(ctx, w.client, group.remoteClients[reservingRemote].client, group.controllerKey, group.local.Name, w.origin); err != nil {
			log.V(2).Error(err, "creating remote controller object", "remote", reservingRemote)
			// We'll retry this in the next reconcile.
			return reconcile.Result{}, err
		}

		if acs.State != kueue.CheckStateRetry && acs.State != kueue.CheckStateRejected {
			if group.jobAdapter.KeepAdmissionCheckPending() {
				acs.State = kueue.CheckStatePending
			} else {
				acs.State = kueue.CheckStateReady
			}
			// update the message
			acs.Message = fmt.Sprintf("The workload got reservation on %q", reservingRemote)
			// update the transition time since is used to detect the lost worker state.
			acs.LastTransitionTime = metav1.NewTime(w.clock.Now())

			wlPatch := workload.BaseSSAWorkload(group.local)
			workload.SetAdmissionCheckState(&wlPatch.Status.AdmissionChecks, *acs)
			err := w.client.Status().Patch(ctx, wlPatch, client.Apply, client.FieldOwner(kueue.MultiKueueControllerName), client.ForceOwnership)
			if err != nil {
				return reconcile.Result{}, err
			}
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
			wlPatch := workload.BaseSSAWorkload(group.local)
			workload.SetAdmissionCheckState(&wlPatch.Status.AdmissionChecks, *acs)
			return reconcile.Result{}, w.client.Status().Patch(ctx, wlPatch, client.Apply, client.FieldOwner(kueue.MultiKueueControllerName), client.ForceOwnership)
		}
	}

	// finally - create missing workloads
	var errs []error
	for rem, remWl := range group.remotes {
		if remWl == nil {
			clone := cloneForCreate(group.local, group.remoteClients[rem].origin)
			err := group.remoteClients[rem].client.Create(ctx, clone)
			if err != nil {
				// just log the error for a single remote
				log.V(2).Error(err, "creating remote object", "remote", rem)
				errs = append(errs, err)
			}
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

func newWlReconciler(c client.Client, helper *multiKueueStoreHelper, cRec *clustersReconciler, origin string, workerLostTimeout, eventsBatchPeriod time.Duration, adapters map[string]jobframework.MultiKueueAdapter, opts ...Option) *wlReconciler {
	options := defaultOptions

	for _, opt := range opts {
		opt(&options)
	}

	return &wlReconciler{
		client:            c,
		helper:            helper,
		clusters:          cRec,
		origin:            origin,
		workerLostTimeout: workerLostTimeout,
		deletedWlCache:    utilmaps.NewSyncMap[string, *kueue.Workload](0),
		eventsBatchPeriod: eventsBatchPeriod,
		adapters:          adapters,
		clock:             options.clock,
	}
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
		Named("multikueue-workload").
		For(&kueue.Workload{}).
		WatchesRawSource(source.Channel(w.clusters.wlUpdateCh, syncHndl)).
		WithEventFilter(w).
		Complete(w)
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
