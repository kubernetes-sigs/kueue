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
	"time"

	batchv1 "k8s.io/api/batch/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
	jobset "sigs.k8s.io/jobset/api/jobset/v1alpha2"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
	"sigs.k8s.io/kueue/pkg/workload"
)

var (
	adapters = map[string]jobAdapter{
		batchv1.SchemeGroupVersion.WithKind("Job").String():   &batchJobAdapter{},
		jobset.SchemeGroupVersion.WithKind("JobSet").String(): &jobsetAdapter{},
	}

	errNoActiveClusters = errors.New("no active clusters")
)

type wlReconciler struct {
	client            client.Client
	helper            *multiKueueStoreHelper
	clusters          *clustersReconciler
	origin            string
	workerLostTimeout time.Duration
}

var _ reconcile.Reconciler = (*wlReconciler)(nil)

type jobAdapter interface {
	// Creates the Job object in the worker cluster using remote client, if not already created.
	// Copy the status from the remote job if already exists.
	SyncJob(ctx context.Context, localClient client.Client, remoteClient client.Client, key types.NamespacedName, workloadName, origin string) error
	// Deletes the Job in the worker cluster.
	DeleteRemoteObject(ctx context.Context, remoteClient client.Client, key types.NamespacedName) error
	// KeepAdmissionCheckPending returns true if the state of the multikueue admission check should be
	// kept Pending while the job runs in a worker. This might be needed to keep the managers job
	// suspended and not start the execution locally.
	KeepAdmissionCheckPending() bool
	// IsJobManagedByKueue returns:
	// - a bool indicating if the job object identified by key is managed by kueue and can be delegated.
	// - a reason indicating why the job is not managed by Kueue
	// - any API error encountered during the check
	IsJobManagedByKueue(ctx context.Context, localClient client.Client, key types.NamespacedName) (bool, string, error)
}

type wlGroup struct {
	local         *kueue.Workload
	remotes       map[string]*kueue.Workload
	remoteClients map[string]*remoteClient
	acName        string
	jobAdapter    jobAdapter
	controllerKey types.NamespacedName
}

// the local wl is finished
func (g *wlGroup) IsFinished() bool {
	return apimeta.IsStatusConditionTrue(g.local.Status.Conditions, kueue.WorkloadFinished)
}

// returns true if there is a wl reserving quota
// the string identifies the remote, ("" - local)
func (g *wlGroup) FirstReserving() (bool, string) {
	found := false
	bestMatch := ""
	bestTime := time.Now()
	for remote, wl := range g.remotes {
		if wl == nil {
			continue
		}
		c := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadQuotaReserved)
		if c != nil && c.Status == metav1.ConditionTrue && (!found || c.LastTransitionTime.Time.Before(bestTime)) {
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

func (a *wlReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	log.V(2).Info("Reconcile Workload")
	wl := &kueue.Workload{}
	if err := a.client.Get(ctx, req.NamespacedName, wl); err != nil {
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}
	// NOTE: the not found needs to be treated and should result in the deletion of all the remote workloads.
	// since the list of remotes can only be taken from its list of admission check stats we need to either
	// 1. use a finalizer
	// 2. try to trigger the remote deletion from an event filter.

	mkAc, err := a.multikueueAC(ctx, wl)
	if err != nil {
		return reconcile.Result{}, err
	}

	if mkAc == nil || mkAc.State == kueue.CheckStateRejected {
		log.V(2).Info("Skip Workload")
		return reconcile.Result{}, nil
	}

	adapter, owner := a.adapter(wl)
	if adapter == nil {
		// Reject the workload since there is no chance for it to run.
		var rejectionMessage string
		if owner != nil {
			rejectionMessage = fmt.Sprintf("No multikueue adapter found for owner kind %q", schema.FromAPIVersionAndKind(owner.APIVersion, owner.Kind).String())
		} else {
			rejectionMessage = "No multikueue adapter found"
		}
		return reconcile.Result{}, a.updateACS(ctx, wl, mkAc, kueue.CheckStateRejected, rejectionMessage)
	}

	managed, unmanagedReason, err := adapter.IsJobManagedByKueue(ctx, a.client, types.NamespacedName{Name: owner.Name, Namespace: wl.Namespace})
	if err != nil {
		return reconcile.Result{}, err
	}

	if !managed {
		return reconcile.Result{}, a.updateACS(ctx, wl, mkAc, kueue.CheckStateRejected, fmt.Sprintf("The owner is not managed by Kueue: %s", unmanagedReason))
	}

	grp, err := a.readGroup(ctx, wl, mkAc.Name, adapter, owner.Name)
	if err != nil {
		return reconcile.Result{}, err
	}

	return a.reconcileGroup(ctx, grp)
}

func (w *wlReconciler) updateACS(ctx context.Context, wl *kueue.Workload, acs *kueue.AdmissionCheckState, status kueue.CheckState, message string) error {
	acs.State = status
	acs.Message = message
	acs.LastTransitionTime = metav1.NewTime(time.Now())
	wlPatch := workload.BaseSSAWorkload(wl)
	workload.SetAdmissionCheckState(&wlPatch.Status.AdmissionChecks, *acs)
	return w.client.Status().Patch(ctx, wlPatch, client.Apply, client.FieldOwner(ControllerName), client.ForceOwnership)
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
			if !client.pendingReconnect.Load() {
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
	relevantChecks, err := admissioncheck.FilterForController(ctx, w.client, local.Status.AdmissionChecks, ControllerName)
	if err != nil {
		return nil, err
	}

	if len(relevantChecks) == 0 {
		return nil, nil
	}
	return workload.FindAdmissionCheck(local.Status.AdmissionChecks, relevantChecks[0]), nil
}

func (w *wlReconciler) adapter(local *kueue.Workload) (jobAdapter, *metav1.OwnerReference) {
	if controller := metav1.GetControllerOf(local); controller != nil {
		adapterKey := schema.FromAPIVersionAndKind(controller.APIVersion, controller.Kind).String()
		return adapters[adapterKey], controller
	}
	return nil, nil
}

func (a *wlReconciler) readGroup(ctx context.Context, local *kueue.Workload, acName string, adapter jobAdapter, controllerName string) (*wlGroup, error) {
	rClients, err := a.remoteClientsForAC(ctx, acName)
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

func (a *wlReconciler) reconcileGroup(ctx context.Context, group *wlGroup) (reconcile.Result, error) {
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

		if !workload.HasQuotaReservation(group.local) && acs.State == kueue.CheckStateRetry {
			errs = append(errs, a.updateACS(ctx, group.local, acs, kueue.CheckStatePending, "Requeued"))
		}

		return reconcile.Result{}, errors.Join(errs...)
	}

	if remoteFinishedCond, remote := group.RemoteFinishedCondition(); remoteFinishedCond != nil {
		// NOTE: we can have a race condition setting the wl status here and it being updated by the job controller
		// it should not be problematic but the "From remote xxxx:" could be lost ....

		if group.jobAdapter != nil {
			if err := group.jobAdapter.SyncJob(ctx, a.client, group.remoteClients[remote].client, group.controllerKey, group.local.Name, a.origin); err != nil {
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
		return reconcile.Result{}, a.client.Status().Patch(ctx, wlPatch, client.Apply, client.FieldOwner(ControllerName+"-finish"), client.ForceOwnership)
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
		if err := group.jobAdapter.SyncJob(ctx, a.client, group.remoteClients[reservingRemote].client, group.controllerKey, group.local.Name, a.origin); err != nil {
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
			acs.LastTransitionTime = metav1.NewTime(time.Now())

			wlPatch := workload.BaseSSAWorkload(group.local)
			workload.SetAdmissionCheckState(&wlPatch.Status.AdmissionChecks, *acs)
			err := a.client.Status().Patch(ctx, wlPatch, client.Apply, client.FieldOwner(ControllerName), client.ForceOwnership)
			if err != nil {
				return reconcile.Result{}, err
			}
		}
		return reconcile.Result{RequeueAfter: a.workerLostTimeout}, nil
	} else if acs.State == kueue.CheckStateReady {
		// If there is no reserving and the AC is ready, the connection with the reserving remote might
		// be lost, keep the workload admitted for keepReadyTimeout and put it back in the queue after that.
		remainingWaitTime := a.workerLostTimeout - time.Since(acs.LastTransitionTime.Time)
		if remainingWaitTime > 0 {
			log.V(3).Info("Reserving remote lost, retry", "retryAfter", remainingWaitTime)
			return reconcile.Result{RequeueAfter: remainingWaitTime}, nil
		} else {
			acs.State = kueue.CheckStateRetry
			acs.Message = "Reserving remote lost"
			acs.LastTransitionTime = metav1.NewTime(time.Now())
			wlPatch := workload.BaseSSAWorkload(group.local)
			workload.SetAdmissionCheckState(&wlPatch.Status.AdmissionChecks, *acs)
			return reconcile.Result{}, a.client.Status().Patch(ctx, wlPatch, client.Apply, client.FieldOwner(ControllerName), client.ForceOwnership)
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

func newWlReconciler(c client.Client, helper *multiKueueStoreHelper, cRec *clustersReconciler, origin string, workerLostTimeout time.Duration) *wlReconciler {
	return &wlReconciler{
		client:            c,
		helper:            helper,
		clusters:          cRec,
		origin:            origin,
		workerLostTimeout: workerLostTimeout,
	}
}

func (w *wlReconciler) setupWithManager(mgr ctrl.Manager) error {
	syncHndl := handler.Funcs{
		GenericFunc: func(_ context.Context, e event.GenericEvent, q workqueue.RateLimitingInterface) {
			q.Add(reconcile.Request{NamespacedName: types.NamespacedName{
				Namespace: e.Object.GetNamespace(),
				Name:      e.Object.GetName(),
			}})
		},
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&kueue.Workload{}).
		WatchesRawSource(&source.Channel{Source: w.clusters.wlUpdateCh}, syncHndl).
		Complete(w)
}

func cleanObjectMeta(orig *metav1.ObjectMeta) metav1.ObjectMeta {
	// to clone the labels and annotations
	clone := orig.DeepCopy()
	return metav1.ObjectMeta{
		Name:        clone.Name,
		Namespace:   clone.Namespace,
		Labels:      clone.Labels,
		Annotations: clone.Annotations,
	}
}

func cloneForCreate(orig *kueue.Workload, origin string) *kueue.Workload {
	remoteWl := &kueue.Workload{}
	remoteWl.ObjectMeta = cleanObjectMeta(&orig.ObjectMeta)
	if remoteWl.Labels == nil {
		remoteWl.Labels = make(map[string]string)
	}
	remoteWl.Labels[kueuealpha.MultiKueueOriginLabel] = origin
	orig.Spec.DeepCopyInto(&remoteWl.Spec)
	return remoteWl
}
