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

package disaggregatedset

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"

	"github.com/go-logr/logr"
	"golang.org/x/sync/errgroup"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/events"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	disaggregatedsetv1 "sigs.k8s.io/lws/api/disaggregatedset/v1"
	leaderworkersetv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	"sigs.k8s.io/kueue/pkg/constants"
	controllerconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	podcontroller "sigs.k8s.io/kueue/pkg/controller/jobs/pod"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/util/parallelize"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	utilslices "sigs.k8s.io/kueue/pkg/util/slices"
	"sigs.k8s.io/kueue/pkg/util/waitforpodsready"
	"sigs.k8s.io/kueue/pkg/workload"
)

const (
	controllerName = "disaggregatedset"

	// defaultSlices mirrors the DisaggregatedSet API default for spec.slices.
	defaultSlices = 1

	ReasonWorkloadRecreated = "WorkloadRecreated"
)

// sliceWorkload is the Workload of one slice index.
type sliceWorkload struct {
	slice int32
	wl    *kueue.Workload
}

type Reconciler struct {
	integrationManager           *jobframework.IntegrationManager
	client                       client.Client
	logName                      string
	record                       events.EventRecorder
	labelKeysToCopy              sets.Set[string]
	annotationsToCopy            sets.Set[string]
	manageJobsWithoutQueueName   bool
	managedJobsNamespaceSelector labels.Selector
	roleTracker                  *roletracker.RoleTracker
	customLabels                 *metrics.CustomLabels
	queues                       *qcache.Manager
}

func NewReconciler(_ context.Context, client client.Client, _ client.FieldIndexer, eventRecorder events.EventRecorder, opts ...jobframework.Option) (jobframework.JobReconcilerInterface, error) {
	options := jobframework.ProcessOptions(opts...)

	return &Reconciler{
		integrationManager:           options.IntegrationManager,
		client:                       client,
		logName:                      "disaggregatedset-reconciler",
		record:                       eventRecorder,
		labelKeysToCopy:              options.LabelKeysToCopy,
		annotationsToCopy:            options.AnnotationsToCopy,
		manageJobsWithoutQueueName:   options.ManageJobsWithoutQueueName,
		managedJobsNamespaceSelector: options.ManagedJobsNamespaceSelector,
		roleTracker:                  options.RoleTracker,
		customLabels:                 options.CustomLabels,
		queues:                       options.Queues,
	}, nil
}

func (r *Reconciler) logger() logr.Logger {
	return roletracker.WithReplicaRole(ctrl.Log.WithName(r.logName), r.roleTracker)
}

var _ jobframework.JobReconcilerInterface = (*Reconciler)(nil)

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	mgr.GetLogger().WithName(r.logName).V(1).Info("Setting up DisaggregatedSet reconciler")

	return ctrl.NewControllerManagedBy(mgr).
		For(&disaggregatedsetv1.DisaggregatedSet{}, builder.WithPredicates(r)).
		Named(controllerName).
		Watches(&kueue.Workload{}, &workloadHandler{}).
		Watches(&corev1.Pod{}, &podHandler{}).
		Watches(&leaderworkersetv1.LeaderWorkerSet{}, &childDeletionHandler{}).
		Watches(&appsv1.StatefulSet{}, &childDeletionHandler{}).
		WithOptions(controller.Options{
			LogConstructor: roletracker.NewLogConstructor(r.roleTracker, "disaggregatedset-reconciler"),
		}).
		Complete(r)
}

// +kubebuilder:rbac:groups=disaggregatedset.x-k8s.io,resources=disaggregatedsets,verbs=get;list;watch

func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	log.V(3).Info("Reconcile DisaggregatedSet")

	ds := &disaggregatedsetv1.DisaggregatedSet{}
	if err := r.client.Get(ctx, req.NamespacedName, ds); err != nil {
		if client.IgnoreNotFound(err) != nil {
			return reconcile.Result{}, err
		}
		// The Workloads of a deleted DisaggregatedSet are garbage-collected
		// through their owner reference. Kueue no longer manages its Pods.
		return reconcile.Result{}, r.releasePods(ctx, req.Namespace, req.Name)
	}
	if !ds.DeletionTimestamp.IsZero() {
		return reconcile.Result{}, nil
	}
	// Workload and Pod events bypass the predicate.
	if managed, err := r.isManaged(ctx, ds); err != nil || !managed {
		return reconcile.Result{}, err
	}
	desired, err := podSets(ds)
	if err != nil {
		return reconcile.Result{}, err
	}
	// Pods are reconciled after the Workloads, so that they join existing ones.
	// A Workload that fails for one slice does not hold up the Pods of the
	// others. A Pod of a slice without a Workload stays gated.
	wlErr := r.reconcileWorkloads(ctx, ds, desired)
	return reconcile.Result{}, errors.Join(wlErr, r.reconcilePods(ctx, ds, desired))
}

func (r *Reconciler) reconcileWorkloads(ctx context.Context, ds *disaggregatedsetv1.DisaggregatedSet, desired []kueue.PodSet) error {
	wlList := &kueue.WorkloadList{}
	if err := r.client.List(ctx, wlList, client.InNamespace(ds.Namespace),
		client.MatchingFields{indexer.OwnerReferenceUID: string(ds.UID)},
	); err != nil {
		return fmt.Errorf("listing Workloads: %w", err)
	}

	toCreate, toUpdate, toRecover, toDelete := filterWorkloads(ds, wlList.Items)

	// The branches hold disjoint sets of Workloads, so one failing is no reason
	// to abandon the others, as in the LeaderWorkerSet reconciler.
	var eg errgroup.Group
	eg.Go(func() error {
		return parallelize.Until(ctx, len(toCreate), func(i int) error {
			return r.createWorkload(ctx, ds, desired, toCreate[i])
		})
	})
	eg.Go(func() error {
		return r.updateWorkloads(ctx, ds, toUpdate)
	})
	eg.Go(func() error {
		return parallelize.Until(ctx, len(toRecover), func(i int) error {
			return r.recoverWorkload(ctx, ds, toRecover[i])
		})
	})
	eg.Go(func() error {
		return parallelize.Until(ctx, len(toDelete), func(i int) error {
			return r.deleteWorkload(ctx, ds, toDelete[i])
		})
	})
	return eg.Wait()
}

// filterWorkloads compares the slices of a DisaggregatedSet with its existing
// Workloads. It returns the slice indexes that need a Workload, the Workloads
// to update in place, the Workloads to recover after they finished as out of
// sync, and the Workloads of slices that no longer exist.
//
// An existing Workload is never replaced when the spec changes. The webhook
// keeps the Pod shape and the TAS annotations immutable, and role replicas
// change only through zero, where the Workloads are deleted. A Workload that
// Kueue deactivated is updated like any other, and stays inactive until a user
// activates it again.
func filterWorkloads(ds *disaggregatedsetv1.DisaggregatedSet, existing []kueue.Workload) (
	toCreate []int32, toUpdate []*kueue.Workload, toRecover []sliceWorkload, toDelete []*kueue.Workload,
) {
	remaining := utilslices.ToRefMap(existing, func(wl *kueue.Workload) string {
		return wl.Name
	})

	var sliceCount int32
	if !allRolesScaledToZero(ds) {
		sliceCount = ptr.Deref(ds.Spec.Slices, defaultSlices)
	}
	for slice := range sliceCount {
		name := GetWorkloadName(ds.UID, ds.Name, slice)
		wl, found := remaining[name]
		delete(remaining, name)
		switch {
		case !found:
			toCreate = append(toCreate, slice)
		case isOutOfSync(wl):
			toRecover = append(toRecover, sliceWorkload{slice: slice, wl: wl})
		default:
			toUpdate = append(toUpdate, wl)
		}
	}
	return toCreate, toUpdate, toRecover, slices.Collect(maps.Values(remaining))
}

// isOutOfSync reports whether the pod-group reconciler finished a slice
// Workload because a role had more members than its PodSet count, which a
// race between Pod adoption and a terminating Pod can cause during a rollout.
// A finished Workload is never admitted again, so the slice would stay gated.
func isOutOfSync(wl *kueue.Workload) bool {
	cond := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadFinished)
	return cond != nil && cond.Status == metav1.ConditionTrue && cond.Reason == kueue.WorkloadFinishedReasonOutOfSync
}

func (r *Reconciler) createWorkload(ctx context.Context, ds *disaggregatedsetv1.DisaggregatedSet, desired []kueue.PodSet, slice int32) error {
	name := GetWorkloadName(ds.UID, ds.Name, slice)
	wlRef := klog.KRef(ds.Namespace, name)
	log := ctrl.LoggerFrom(ctx).WithValues("workload", wlRef, "slice", slice)

	wl, err := r.constructWorkload(ds, desired, name, slice)
	if err != nil {
		return fmt.Errorf("constructing Workload %s of slice %d: %w", wlRef, slice, err)
	}
	if err := jobframework.PrepareWorkloadPriority(ctx, r.client, r.record, ds, wl, nil); err != nil {
		return fmt.Errorf("preparing the priority of Workload %s of slice %d: %w", wlRef, slice, err)
	}
	if err := r.client.Create(ctx, wl); err != nil {
		return fmt.Errorf("creating Workload %s of slice %d: %w", wlRef, slice, err)
	}
	log.V(3).Info("Created DisaggregatedSet Workload")
	r.record.Eventf(ds, nil, corev1.EventTypeNormal, jobframework.ReasonCreatedWorkload, "CreatedWorkload",
		"Created Workload: %v", workload.Key(wl))
	jobframework.RecordWorkloadCreationLatency(ctx, ds, gvk.Kind, wl, r.queues, r.customLabels, r.roleTracker)
	return nil
}

// constructWorkload builds the Workload of one slice. Each Workload gets its
// own copy of the PodSets, because Workloads of several slices are created
// concurrently and Create decodes the response into the Workload.
func (r *Reconciler) constructWorkload(ds *disaggregatedsetv1.DisaggregatedSet, desired []kueue.PodSet, name string, slice int32) (*kueue.Workload, error) {
	podSets := make([]kueue.PodSet, len(desired))
	for i := range desired {
		desired[i].DeepCopyInto(&podSets[i])
	}
	wl := podcontroller.NewGroupWorkload(name, ds, podSets, r.labelKeysToCopy, r.annotationsToCopy)
	if wl.Labels == nil {
		wl.Labels = make(map[string]string, 1)
	}
	wl.Labels[controllerconstants.JobUIDLabel] = string(ds.UID)
	wl.Annotations[controllerconstants.JobOwnerGVKAnnotation] = gvk.String()
	wl.Annotations[controllerconstants.JobOwnerNameAnnotation] = ds.Name
	wl.Annotations[controllerconstants.ComponentWorkloadIndexAnnotation] = strconv.Itoa(int(slice))
	if err := controllerutil.SetOwnerReference(ds, wl, r.client.Scheme()); err != nil {
		return nil, err
	}
	return wl, nil
}

// updateWorkloads applies the queue name and the workload-level annotations
// of the DisaggregatedSet to its slice Workloads, and then their priority.
func (r *Reconciler) updateWorkloads(ctx context.Context, ds *disaggregatedsetv1.DisaggregatedSet, wls []*kueue.Workload) error {
	if err := parallelize.Until(ctx, len(wls), func(i int) error {
		return r.updateWorkload(ctx, ds, wls[i])
	}); err != nil {
		return err
	}
	// All slice Workloads are passed together, so a class change of one of them
	// also refreshes the priority value of the others.
	if err := jobframework.UpdateWorkloadPriority(ctx, r.client, r.record, ds, nil, wls...); err != nil {
		return fmt.Errorf("updating Workload priority: %w", err)
	}
	return nil
}

func (r *Reconciler) updateWorkload(ctx context.Context, ds *disaggregatedsetv1.DisaggregatedSet, wl *kueue.Workload) error {
	log := ctrl.LoggerFrom(ctx).WithValues("workload", klog.KObj(wl))

	var shouldUpdate bool
	var queueChanged bool
	previousQueueName := wl.Spec.QueueName
	// The queue name of a Workload is immutable while it has reserved quota.
	if queueName := jobframework.QueueNameForObject(ds); wl.Spec.QueueName != queueName && !workload.HasQuotaReservation(wl) {
		wl.Spec.QueueName = queueName
		queueChanged = true
		shouldUpdate = true
	}

	var admissionGatedByUpdated bool
	if features.Enabled(features.AdmissionGatedBy) {
		admissionGatedByUpdated = jobframework.PropagateAdmissionGatedByAnnotation(ds, wl)
		shouldUpdate = shouldUpdate || admissionGatedByUpdated
	}

	var waitForPodsReadyEvent jobframework.WaitForPodsReadyEvent
	if waitforpodsready.WorkloadLevelWaitForPodsReadyEnabled() {
		var waitForPodsReadyUpdated bool
		waitForPodsReadyUpdated, waitForPodsReadyEvent = jobframework.SyncWaitForPodsReady(ctx, ds, wl)
		shouldUpdate = shouldUpdate || waitForPodsReadyUpdated
	}

	if !shouldUpdate {
		return nil
	}
	if err := r.client.Update(ctx, wl); err != nil {
		return fmt.Errorf("updating Workload %s: %w", klog.KObj(wl), err)
	}
	if queueChanged {
		log.V(3).Info("Updated Workload queue name",
			"previousLocalQueue", klog.KRef(wl.Namespace, string(previousQueueName)),
			"localQueue", klog.KRef(wl.Namespace, string(wl.Spec.QueueName)))
	} else {
		log.V(3).Info("Updated DisaggregatedSet Workload")
	}
	if admissionGatedByUpdated {
		jobframework.RecordAdmissionGatedByUpdateEvent(r.record, ds)
	}
	if waitForPodsReadyEvent != jobframework.WaitForPodsReadyEventNone {
		jobframework.RecordWaitForPodsReadyUpdateEvent(r.record, ds, wl, waitForPodsReadyEvent)
	}
	return nil
}

// runningPods returns the Pods of the slice that are ungated, can still run,
// and were created by the DisaggregatedSet. A deleting Pod that was never
// scheduled cannot run, and keeps its finalizer until the Workload is deleted.
// A Pod with other owners only has the labels of the slice, so it does not
// hold up the recovery.
func (r *Reconciler) runningPods(ctx context.Context, ds *disaggregatedsetv1.DisaggregatedSet, slice int32) ([]*corev1.Pod, error) {
	pods := &corev1.PodList{}
	if err := r.client.List(ctx, pods, client.InNamespace(ds.Namespace), client.MatchingLabels{
		disaggregatedsetv1.SetNameLabelKey: ds.Name,
		disaggregatedsetv1.SliceLabelKey:   strconv.Itoa(int(slice)),
	}); err != nil {
		return nil, fmt.Errorf("listing Pods of slice %d: %w", slice, err)
	}
	children, err := r.listChildObjects(ctx, ds, pods.Items)
	if err != nil {
		return nil, err
	}
	var running []*corev1.Pod
	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.Annotations[podconstants.SuspendedByParentAnnotation] != FrameworkName ||
			utilpod.HasGate(pod, podconstants.SchedulingGateName) || utilpod.IsTerminated(pod) ||
			!pod.DeletionTimestamp.IsZero() && pod.Spec.NodeName == "" {
			continue
		}
		owners, err := r.checkOwnership(ctx, ds, children, pod)
		if err != nil {
			return nil, err
		}
		if owners.created() {
			running = append(running, pod)
		}
	}
	return running, nil
}

// recoverWorkload recreates a slice Workload that finished as out of sync. A
// finished Workload has released its quota and is never admitted again. The
// pod-group reconciler deletes the ungated Pods of the slice in the reconcile
// that finishes the Workload. Any that it left behind, for example when that
// reconcile failed part-way, are deleted here, because nothing else stops
// them. Once none of the slice Pods runs, the Workload is deleted. The
// StatefulSets recreate the Pods gated, and the Workload created next
// requeues the slice for admission.
func (r *Reconciler) recoverWorkload(ctx context.Context, ds *disaggregatedsetv1.DisaggregatedSet, sw sliceWorkload) error {
	log := ctrl.LoggerFrom(ctx).WithValues("workload", klog.KObj(sw.wl), "slice", sw.slice)

	running, err := r.runningPods(ctx, ds, sw.slice)
	if err != nil {
		return err
	}
	if len(running) > 0 {
		log.V(3).Info("Waiting for the slice Pods to stop before recreating the out of sync Workload")
		return parallelize.Until(ctx, len(running), func(i int) error {
			pod := running[i]
			if !pod.DeletionTimestamp.IsZero() {
				return nil
			}
			if err := r.client.Delete(ctx, pod); client.IgnoreNotFound(err) != nil {
				return fmt.Errorf("deleting Pod %s of slice %d: %w", klog.KObj(pod), sw.slice, err)
			}
			return nil
		})
	}
	deleted, err := workload.Delete(ctx, r.client, sw.wl)
	if client.IgnoreNotFound(err) != nil {
		return fmt.Errorf("deleting the out of sync Workload %s of slice %d: %w", klog.KObj(sw.wl), sw.slice, err)
	}
	if deleted {
		r.record.Eventf(ds, nil, corev1.EventTypeNormal, ReasonWorkloadRecreated, "RecreatedWorkload",
			"Recreating Workload %v, it finished out of sync with its Pods", workload.Key(sw.wl))
	}
	return nil
}

func (r *Reconciler) deleteWorkload(ctx context.Context, ds *disaggregatedsetv1.DisaggregatedSet, wl *kueue.Workload) error {
	log := ctrl.LoggerFrom(ctx).WithValues("workload", klog.KObj(wl))

	deleted, err := workload.Delete(ctx, r.client, wl)
	if client.IgnoreNotFound(err) != nil {
		return fmt.Errorf("deleting Workload %s: %w", klog.KObj(wl), err)
	}
	if deleted {
		log.V(3).Info("Deleted DisaggregatedSet Workload")
		r.record.Eventf(ds, nil, corev1.EventTypeNormal, jobframework.ReasonDeletedWorkload, "DeletedWorkload",
			"Deleted Workload: %v", workload.Key(wl))
	}
	return nil
}

var _ predicate.Predicate = (*Reconciler)(nil)

func (r *Reconciler) Generic(event.GenericEvent) bool {
	return false
}

func (r *Reconciler) Create(e event.CreateEvent) bool {
	return r.handle(e.Object)
}

func (r *Reconciler) Update(e event.UpdateEvent) bool {
	return r.handle(e.ObjectNew)
}

// Delete requeues a deleted DisaggregatedSet, so that the Pods it leaves
// behind, for example after a delete with --cascade=orphan, are released.
func (r *Reconciler) Delete(event.DeleteEvent) bool {
	return true
}

// handle reports whether Kueue manages the DisaggregatedSet of an event. The
// predicate has no context, so it starts one.
func (r *Reconciler) handle(obj client.Object) bool {
	ds, isDS := obj.(*disaggregatedsetv1.DisaggregatedSet)
	if !isDS {
		return false
	}

	log := r.logger().WithValues("disaggregatedSet", klog.KObj(ds))
	managed, err := r.isManaged(ctrl.LoggerInto(context.Background(), log), ds)
	if err != nil {
		log.Error(err, "Failed to determine if the DisaggregatedSet should be managed by Kueue")
	}
	return managed
}

// isManaged reports whether Kueue manages the DisaggregatedSet.
func (r *Reconciler) isManaged(ctx context.Context, ds *disaggregatedsetv1.DisaggregatedSet) (bool, error) {
	return r.integrationManager.WorkloadShouldBeSuspended(ctx, ds, r.client, r.manageJobsWithoutQueueName, r.managedJobsNamespaceSelector)
}

// workloadHandler requeues the DisaggregatedSet that owns a Workload, so that
// a deleted slice Workload is recreated.
type workloadHandler struct{}

var _ handler.EventHandler = (*workloadHandler)(nil)

func (h *workloadHandler) Create(ctx context.Context, e event.CreateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	h.enqueue(ctx, e.Object, q)
}

func (h *workloadHandler) Update(ctx context.Context, e event.UpdateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	h.enqueue(ctx, e.ObjectNew, q)
}

func (h *workloadHandler) Delete(ctx context.Context, e event.DeleteEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	h.enqueue(ctx, e.Object, q)
}

func (h *workloadHandler) Generic(context.Context, event.GenericEvent, workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

func (h *workloadHandler) enqueue(ctx context.Context, obj client.Object, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	wl, ok := obj.(*kueue.Workload)
	if !ok {
		return
	}
	for _, ownerRef := range wl.OwnerReferences {
		if ownerRef.APIVersion == gvk.GroupVersion().String() && ownerRef.Kind == gvk.Kind {
			ctrl.LoggerFrom(ctx).V(3).Info("Queueing reconcile for owning DisaggregatedSet",
				"workload", klog.KObj(wl),
				"disaggregatedSet", klog.KRef(wl.Namespace, ownerRef.Name),
			)
			q.AddAfter(reconcile.Request{Namespace: wl.Namespace, Name: ownerRef.Name},
				constants.UpdatesBatchPeriod)
			return
		}
	}
}

// podHandler requeues the DisaggregatedSet of a slice Pod, so that the Pod is
// adopted or released, and the recovery of an out of sync Workload continues
// once the Pods of the slice stop.
type podHandler struct{}

var _ handler.EventHandler = (*podHandler)(nil)

func (h *podHandler) Create(ctx context.Context, e event.CreateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	h.enqueue(ctx, e.Object, q)
}

func (h *podHandler) Update(ctx context.Context, e event.UpdateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	h.enqueue(ctx, e.ObjectNew, q)
}

func (h *podHandler) Delete(ctx context.Context, e event.DeleteEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	h.enqueue(ctx, e.Object, q)
}

func (h *podHandler) Generic(context.Context, event.GenericEvent, workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

func (h *podHandler) enqueue(ctx context.Context, obj client.Object, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	pod, ok := obj.(*corev1.Pod)
	if !ok || pod.Annotations[podconstants.SuspendedByParentAnnotation] != FrameworkName {
		return
	}
	dsName, ok := pod.Labels[disaggregatedsetv1.SetNameLabelKey]
	if !ok {
		return
	}
	ctrl.LoggerFrom(ctx).V(3).Info("Queueing reconcile for owning DisaggregatedSet",
		"pod", klog.KObj(pod),
		"disaggregatedSet", klog.KRef(pod.Namespace, dsName),
	)
	q.AddAfter(reconcile.Request{Namespace: pod.Namespace, Name: dsName},
		constants.UpdatesBatchPeriod)
}

// childDeletionHandler requeues the DisaggregatedSet of a deleted child
// LeaderWorkerSet or StatefulSet. After a delete with --cascade=orphan, no Pod
// event follows, and the Pods of the deleted object must still be released.
type childDeletionHandler struct{}

var _ handler.EventHandler = (*childDeletionHandler)(nil)

func (h *childDeletionHandler) Create(context.Context, event.CreateEvent, workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

func (h *childDeletionHandler) Update(context.Context, event.UpdateEvent, workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

func (h *childDeletionHandler) Delete(ctx context.Context, e event.DeleteEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	dsName, ok := e.Object.GetLabels()[disaggregatedsetv1.SetNameLabelKey]
	if !ok {
		return
	}
	ctrl.LoggerFrom(ctx).V(3).Info("Queueing reconcile for the DisaggregatedSet of a deleted child object",
		"object", klog.KObj(e.Object),
		"disaggregatedSet", klog.KRef(e.Object.GetNamespace(), dsName),
	)
	q.AddAfter(reconcile.Request{Namespace: e.Object.GetNamespace(), Name: dsName},
		constants.UpdatesBatchPeriod)
}

func (h *childDeletionHandler) Generic(context.Context, event.GenericEvent, workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}
