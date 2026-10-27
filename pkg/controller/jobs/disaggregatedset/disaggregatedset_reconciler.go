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
	"maps"
	"slices"
	"strconv"

	"github.com/go-logr/logr"
	"golang.org/x/sync/errgroup"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
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

	// podSetReplacementAnnotation marks a slice Workload that is deactivated to
	// be replaced by one built from the current spec. Once set, it drives the
	// replacement to completion, because a deactivated Workload is never
	// admitted again.
	podSetReplacementAnnotation = "kueue.x-k8s.io/podset-replacement"

	ReasonWorkloadReplaced = "WorkloadReplaced"
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
	}, nil
}

func (r *Reconciler) logger() logr.Logger {
	return roletracker.WithReplicaRole(ctrl.Log.WithName(r.logName), r.roleTracker)
}

var _ jobframework.JobReconcilerInterface = (*Reconciler)(nil)

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	ctrl.Log.V(3).Info("Setting up DisaggregatedSet reconciler")

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
	log.V(4).Info("Reconcile DisaggregatedSet")

	ds := &disaggregatedsetv1.DisaggregatedSet{}
	if err := r.client.Get(ctx, req.NamespacedName, ds); err != nil {
		if client.IgnoreNotFound(err) != nil {
			return reconcile.Result{}, err
		}
		// The Workloads of a deleted DisaggregatedSet are garbage-collected
		// through their owner reference. Kueue no longer manages its Pods.
		return reconcile.Result{}, r.releasePods(ctx, req.Namespace, req.Name)
	}
	// Workload and Pod events bypass the predicate.
	if !ds.DeletionTimestamp.IsZero() || !r.handle(ds) {
		return reconcile.Result{}, nil
	}
	if err := r.reconcileWorkloads(ctx, ds); err != nil {
		return reconcile.Result{}, err
	}
	// Pods are reconciled after the Workloads, so that they join existing ones.
	return reconcile.Result{}, r.reconcilePods(ctx, ds)
}

func (r *Reconciler) reconcileWorkloads(ctx context.Context, ds *disaggregatedsetv1.DisaggregatedSet) error {
	log := ctrl.LoggerFrom(ctx)

	wlList := &kueue.WorkloadList{}
	if err := r.client.List(ctx, wlList, client.InNamespace(ds.Namespace),
		client.MatchingFields{indexer.OwnerReferenceUID: string(ds.UID)},
	); err != nil {
		log.Error(err, "Failed to fetch Workloads")
		return err
	}

	desired, err := podSets(ds)
	if err != nil {
		return err
	}
	toCreate, toUpdate, toReplace, toFinishReplacement, toDelete := filterWorkloads(ds, desired, wlList.Items)

	// The branches hold disjoint sets of Workloads, so one failing is no reason
	// to abandon the others, as in the LeaderWorkerSet reconciler.
	var eg errgroup.Group
	eg.Go(func() error {
		return parallelize.Until(ctx, len(toCreate), func(i int) error {
			return r.createWorkload(ctx, ds, toCreate[i])
		})
	})
	eg.Go(func() error {
		return r.updateWorkloads(ctx, ds, toUpdate)
	})
	eg.Go(func() error {
		return parallelize.Until(ctx, len(toReplace), func(i int) error {
			return r.startReplacement(ctx, toReplace[i])
		})
	})
	eg.Go(func() error {
		return parallelize.Until(ctx, len(toFinishReplacement), func(i int) error {
			return r.finishReplacement(ctx, ds, toFinishReplacement[i])
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
// to update in place, the Workloads whose PodSets no longer match the spec,
// the Workloads whose replacement is in progress, and the Workloads of slices
// that no longer exist.
func filterWorkloads(ds *disaggregatedsetv1.DisaggregatedSet, desired []kueue.PodSet, existing []kueue.Workload) (
	toCreate []int32, toUpdate, toReplace []*kueue.Workload, toFinishReplacement []sliceWorkload, toDelete []*kueue.Workload,
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
		case isReplacing(wl):
			toFinishReplacement = append(toFinishReplacement, sliceWorkload{slice: slice, wl: wl})
		case workload.IsActive(wl) && podSetCountsChanged(wl.Spec.PodSets, desired):
			toReplace = append(toReplace, wl)
		default:
			toUpdate = append(toUpdate, wl)
		}
	}
	return toCreate, toUpdate, toReplace, toFinishReplacement, slices.Collect(maps.Values(remaining))
}

// isReplacing reports whether a slice Workload was deactivated to be replaced.
// A Workload that is deactivated without the annotation is left alone.
func isReplacing(wl *kueue.Workload) bool {
	return wl.Annotations[podSetReplacementAnnotation] == "true" && !workload.IsActive(wl)
}

// podSetCountsChanged reports whether a slice Workload has other PodSets than
// the current spec builds. The webhook keeps the Pod shape and the TAS
// annotations immutable, so only the counts can change, after the roles are
// scaled through zero faster than the reconciler observes it.
func podSetCountsChanged(got, want []kueue.PodSet) bool {
	return !slices.EqualFunc(got, want, func(a, b kueue.PodSet) bool {
		return a.Name == b.Name && a.Count == b.Count
	})
}

func (r *Reconciler) createWorkload(ctx context.Context, ds *disaggregatedsetv1.DisaggregatedSet, slice int32) error {
	name := GetWorkloadName(ds.UID, ds.Name, slice)
	log := ctrl.LoggerFrom(ctx).WithValues(
		"workload", klog.ObjectRef{Name: name, Namespace: ds.Namespace},
		"slice", slice,
	)
	log.V(3).Info("Create DisaggregatedSet Workload")

	wl, err := r.constructWorkload(ds, name, slice)
	if err != nil {
		log.Error(err, "Failed to construct Workload")
		return err
	}
	if err := jobframework.PrepareWorkloadPriority(ctx, r.client, r.record, ds, wl, nil); err != nil {
		log.Error(err, "Failed to prepare Workload priority")
		return err
	}
	if err := r.client.Create(ctx, wl); err != nil {
		log.Error(err, "Failed to create Workload")
		return err
	}
	r.record.Eventf(ds, nil, corev1.EventTypeNormal, jobframework.ReasonCreatedWorkload, "CreatedWorkload",
		"Created Workload: %v", workload.Key(wl))
	jobframework.RecordWorkloadCreationLatency(ctx, ds, gvk.Kind, wl, r.customLabels, r.roleTracker)
	return nil
}

func (r *Reconciler) constructWorkload(ds *disaggregatedsetv1.DisaggregatedSet, name string, slice int32) (*kueue.Workload, error) {
	podSets, err := podSets(ds)
	if err != nil {
		return nil, err
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
		ctrl.LoggerFrom(ctx).Error(err, "Failed to update Workload priority")
		return err
	}
	return nil
}

func (r *Reconciler) updateWorkload(ctx context.Context, ds *disaggregatedsetv1.DisaggregatedSet, wl *kueue.Workload) error {
	log := ctrl.LoggerFrom(ctx).WithValues("workload", klog.KObj(wl))

	var shouldUpdate bool
	// The queue name of a Workload is immutable while it has reserved quota.
	if queueName := jobframework.QueueNameForObject(ds); wl.Spec.QueueName != queueName && !workload.HasQuotaReservation(wl) {
		log.V(2).Info("DisaggregatedSet changed queue, updating Workload")
		wl.Spec.QueueName = queueName
		shouldUpdate = true
	}

	var admissionGatedByUpdated bool
	if features.Enabled(features.AdmissionGatedBy) {
		admissionGatedByUpdated = jobframework.PropagateAdmissionGatedByAnnotation(ds, wl)
		shouldUpdate = shouldUpdate || admissionGatedByUpdated
	}

	var waitForPodsReadyUpdated bool
	if waitforpodsready.WorkloadLevelWaitForPodsReadyEnabled() {
		var err error
		waitForPodsReadyUpdated, err = jobframework.PropagateWaitForPodsReadyAnnotation(ds, wl)
		if err != nil {
			return err
		}
		shouldUpdate = shouldUpdate || waitForPodsReadyUpdated
	}

	if !shouldUpdate {
		return nil
	}
	log.V(3).Info("Update DisaggregatedSet Workload")
	if err := r.client.Update(ctx, wl); err != nil {
		log.Error(err, "Failed to update Workload")
		return err
	}
	if admissionGatedByUpdated {
		jobframework.RecordAdmissionGatedByUpdateEvent(r.record, ds)
	}
	if waitForPodsReadyUpdated {
		jobframework.RecordWaitForPodsReadyUpdateEvent(r.record, ds)
	}
	return nil
}

// startReplacement deactivates a slice Workload whose PodSet counts no longer
// match the spec. Kueue evicts it, and the pod-group reconciler deletes the
// Pods of the slice.
func (r *Reconciler) startReplacement(ctx context.Context, wl *kueue.Workload) error {
	log := ctrl.LoggerFrom(ctx).WithValues("workload", klog.KObj(wl))
	log.V(2).Info("Deactivating DisaggregatedSet Workload to replace its PodSets")

	if wl.Annotations == nil {
		wl.Annotations = make(map[string]string, 1)
	}
	wl.Annotations[podSetReplacementAnnotation] = "true"
	wl.Spec.Active = new(false)
	if err := r.client.Update(ctx, wl); err != nil {
		log.Error(err, "Failed to deactivate Workload")
		return err
	}
	return nil
}

// finishReplacement deletes a deactivated slice Workload once none of the
// slice Pods runs. Deleting the Workload requeues the DisaggregatedSet, which
// then creates the replacement from the current spec.
func (r *Reconciler) finishReplacement(ctx context.Context, ds *disaggregatedsetv1.DisaggregatedSet, sw sliceWorkload) error {
	log := ctrl.LoggerFrom(ctx).WithValues("workload", klog.KObj(sw.wl), "slice", sw.slice)

	running, err := r.hasRunningPods(ctx, ds, sw.slice)
	if err != nil {
		return err
	}
	if running {
		log.V(3).Info("Waiting for the slice Pods to stop before replacing the Workload")
		return nil
	}
	deleted, err := workload.Delete(ctx, r.client, sw.wl)
	if err != nil {
		log.Error(err, "Failed to delete Workload")
		return client.IgnoreNotFound(err)
	}
	if deleted {
		r.record.Eventf(ds, nil, corev1.EventTypeNormal, ReasonWorkloadReplaced, "ReplacedWorkload",
			"Replacing Workload %v, its PodSets no longer match the spec", workload.Key(sw.wl))
	}
	return nil
}

// hasRunningPods reports whether a Pod of the slice is ungated and not yet
// terminated.
func (r *Reconciler) hasRunningPods(ctx context.Context, ds *disaggregatedsetv1.DisaggregatedSet, slice int32) (bool, error) {
	pods := &corev1.PodList{}
	if err := r.client.List(ctx, pods, client.InNamespace(ds.Namespace), client.MatchingLabels{
		disaggregatedsetv1.SetNameLabelKey: ds.Name,
		disaggregatedsetv1.SliceLabelKey:   strconv.Itoa(int(slice)),
	}); err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "Failed to list Pods")
		return false, err
	}
	return slices.ContainsFunc(pods.Items, func(pod corev1.Pod) bool {
		return pod.Annotations[podconstants.SuspendedByParentAnnotation] == FrameworkName &&
			!utilpod.HasGate(&pod, podconstants.SchedulingGateName) && !utilpod.IsTerminated(&pod)
	}), nil
}

func (r *Reconciler) deleteWorkload(ctx context.Context, ds *disaggregatedsetv1.DisaggregatedSet, wl *kueue.Workload) error {
	log := ctrl.LoggerFrom(ctx).WithValues("workload", klog.KObj(wl))
	log.V(3).Info("Delete DisaggregatedSet Workload")

	deleted, err := workload.Delete(ctx, r.client, wl)
	if err != nil {
		log.Error(err, "Failed to delete Workload")
		return client.IgnoreNotFound(err)
	}
	if deleted {
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

// handle reports whether Kueue manages the DisaggregatedSet.
func (r *Reconciler) handle(obj client.Object) bool {
	ds, isDS := obj.(*disaggregatedsetv1.DisaggregatedSet)
	if !isDS {
		return false
	}

	log := r.logger().WithValues("disaggregatedset", klog.KObj(ds))
	ctx := ctrl.LoggerInto(context.Background(), log)

	managed, err := r.integrationManager.WorkloadShouldBeSuspended(ctx, ds, r.client, r.manageJobsWithoutQueueName, r.managedJobsNamespaceSelector)
	if err != nil {
		log.Error(err, "Failed to determine if the DisaggregatedSet should be managed by Kueue")
	}
	return managed
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
				"disaggregatedset", klog.ObjectRef{Namespace: wl.Namespace, Name: ownerRef.Name},
			)
			q.AddAfter(reconcile.Request{Namespace: wl.Namespace, Name: ownerRef.Name},
				constants.UpdatesBatchPeriod)
			return
		}
	}
}

// podHandler requeues the DisaggregatedSet of a slice Pod, so that a
// Workload replacement continues once the Pods of the slice stop.
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
		"disaggregatedset", klog.ObjectRef{Namespace: pod.Namespace, Name: dsName},
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
		"disaggregatedset", klog.ObjectRef{Namespace: e.Object.GetNamespace(), Name: dsName},
	)
	q.AddAfter(reconcile.Request{Namespace: e.Object.GetNamespace(), Name: dsName},
		constants.UpdatesBatchPeriod)
}

func (h *childDeletionHandler) Generic(context.Context, event.GenericEvent, workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}
