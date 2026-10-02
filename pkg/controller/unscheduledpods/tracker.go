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

package unscheduledpods

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	"k8s.io/utils/clock"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/core"
	"sigs.k8s.io/kueue/pkg/features"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	"sigs.k8s.io/kueue/pkg/util/waitforpodsready"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/pkg/workload/concurrentadmission"
	workloadevict "sigs.k8s.io/kueue/pkg/workload/evict"
	workloadfinish "sigs.k8s.io/kueue/pkg/workload/finish"
	workloadpatching "sigs.k8s.io/kueue/pkg/workload/patching"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
)

const (
	controllerName = "UnscheduledPodsTracker"

	fieldOwner = constants.KueueName + "-unscheduled-pods-tracker"

	// Wait one second so LastTransitionTime is strictly after the second-granularity admission time.
	admissionObservationDelay = time.Second

	unscheduledPodsMessage = "At least one required pod is not scheduled"

	allPodsScheduledMessage = "All required pods were scheduled or succeeded"
)

type option func(*Tracker)

func withClock(c clock.Clock) option {
	return func(o *Tracker) {
		o.clock = c
	}
}

type Tracker struct {
	client           client.Client
	clock            clock.Clock
	roleTracker      *roletracker.RoleTracker
	waitForPodsReady *configapi.WaitForPodsReady
}

var _ reconcile.Reconciler = (*Tracker)(nil)
var _ predicate.TypedPredicate[*kueue.Workload] = (*Tracker)(nil)

// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads,verbs=get;list;watch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/status,verbs=get;update;patch

func NewTracker(c client.Client, roleTracker *roletracker.RoleTracker, cfg *configapi.WaitForPodsReady, opts ...option) *Tracker {
	t := &Tracker{
		client:           c,
		clock:            clock.RealClock{},
		roleTracker:      roleTracker,
		waitForPodsReady: cfg,
	}
	for _, opt := range opts {
		opt(t)
	}
	return t
}

func (t *Tracker) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	if !waitforpodsready.PodsScheduledTrackingEnabled(t.waitForPodsReady) {
		return reconcile.Result{}, nil
	}
	log := ctrl.LoggerFrom(ctx)
	log.V(4).Info("Reconcile UnscheduledPodsTracker")

	wl, err := workloadslicing.FindActiveWorkload(ctx, t.client, req.NamespacedName, true)
	if err != nil || wl == nil {
		return reconcile.Result{}, err
	}
	if workloadfinish.IsFinished(wl) || (features.Enabled(features.ConcurrentAdmission) && concurrentadmission.IsVariant(wl)) {
		return reconcile.Result{}, nil
	}
	if !workload.IsAdmitted(wl) && apimeta.IsStatusConditionTrue(wl.Status.Conditions, kueue.WorkloadPodsScheduled) {
		resetPodsScheduledCond := metav1.Condition{
			Type:               kueue.WorkloadPodsScheduled,
			Status:             metav1.ConditionFalse,
			Reason:             kueue.WorkloadWaitForStart,
			Message:            workload.PodsNotReadyMessage,
			ObservedGeneration: wl.Generation,
			LastTransitionTime: metav1.NewTime(t.clock.Now()),
		}
		return reconcile.Result{}, workloadpatching.PatchStatus(ctx, t.client, wl, client.FieldOwner(fieldOwner), func(wl *kueue.Workload) (bool, error) {
			return apimeta.SetStatusCondition(&wl.Status.Conditions, resetPodsScheduledCond), nil
		})
	}
	if !shouldTrack(wl) {
		return reconcile.Result{}, nil
	}

	// shouldTrack guarantees that the Admitted=True condition exists.
	admittedAt := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadAdmitted).LastTransitionTime.Time
	now := t.clock.Now()
	if observeAfter := admittedAt.Add(admissionObservationDelay); now.Before(observeAfter) {
		return reconcile.Result{RequeueAfter: observeAfter.Sub(now)}, nil
	}

	current := workload.CurrentPodsScheduledCondition(wl, admittedAt)
	if current != nil && current.Status == metav1.ConditionTrue {
		return reconcile.Result{}, nil
	}
	pods, err := workloadslicing.ListPodsForWorkloadSlice(ctx, t.client, wl.Namespace, workloadslicing.SliceName(wl))
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("listing pods for workload %s: %w", klog.KObj(wl), err)
	}

	return reconcile.Result{}, t.updateWorkloadStatusIfNeeded(ctx, wl, pods, current, now)
}

func (t *Tracker) updateWorkloadStatusIfNeeded(ctx context.Context, wl *kueue.Workload, pods []*corev1.Pod, current *metav1.Condition, now time.Time) error {
	log := ctrl.LoggerFrom(ctx)
	nonTerminal, activeScheduled, succeeded := countsPodsbyStatus(pods)
	var reclaimable map[kueue.PodSetReference]int32
	if features.Enabled(features.ReclaimablePods) {
		reclaimable = workload.ReclaimableCounts(wl)
	}
	var required, scheduled, succeededCapped int64
	specCounts := workload.ExtractPodSetCounts(wl.Spec.PodSets)
	for _, assignment := range wl.Status.Admission.PodSetAssignments {
		psName := assignment.Name
		// Spec counts are only a fallback for older Workloads, not a cap after scale-down.
		count := int64(ptr.Deref(assignment.Count, specCounts[psName]))
		required += count
		scheduled += min(count, activeScheduled[psName]+max(succeeded[psName], int64(reclaimable[psName])))
		succeededCapped += min(count, succeeded[psName])
	}
	succeededFillsGrant := required > 0 && succeededCapped == required
	if current == nil && nonTerminal == 0 && !succeededFillsGrant {
		log.V(4).Info("No live pods observed for the workload. Leaving the PodsScheduled condition unset")
		return nil
	}
	allScheduled := scheduled >= required
	if current != nil && !allScheduled {
		log.V(5).Info("PodsScheduled condition is up-to-date")
		return nil
	}

	condition := metav1.Condition{
		Type:               kueue.WorkloadPodsScheduled,
		Status:             metav1.ConditionFalse,
		Reason:             kueue.WorkloadWaitForScheduling,
		Message:            unscheduledPodsMessage,
		ObservedGeneration: wl.Generation,
		LastTransitionTime: metav1.NewTime(now),
	}
	if allScheduled {
		condition.Status = metav1.ConditionTrue
		condition.Reason = kueue.WorkloadAllRequiredPodsScheduled
		condition.Message = allPodsScheduledMessage
	}
	log.V(3).Info("Updating the PodsScheduled condition", "status", condition.Status, "reason", condition.Reason)
	return workloadpatching.PatchStatus(ctx, t.client, wl, client.FieldOwner(fieldOwner), func(wl *kueue.Workload) (bool, error) {
		apimeta.RemoveStatusCondition(&wl.Status.Conditions, condition.Type)
		apimeta.SetStatusCondition(&wl.Status.Conditions, condition)
		return true, nil
	})
}

func countsPodsbyStatus(pods []*corev1.Pod) (nonTerminal int64, activeScheduled, succeeded map[kueue.PodSetReference]int64) {
	activeScheduled = make(map[kueue.PodSetReference]int64)
	succeeded = make(map[kueue.PodSetReference]int64)
	for _, pod := range pods {
		podSetName := kueue.PodSetReference(pod.Labels[constants.PodSetLabel])
		switch {
		case pod.Status.Phase == corev1.PodSucceeded:
			succeeded[podSetName]++
		case pod.Status.Phase == corev1.PodFailed, !pod.DeletionTimestamp.IsZero():
			// No action: failed or deleting Pods do not satisfy an admitted slot.
		default:
			nonTerminal++
			if utilpod.IsScheduled(pod) {
				activeScheduled[podSetName]++
			}
		}
	}
	return nonTerminal, activeScheduled, succeeded
}

func shouldTrack(wl *kueue.Workload) bool {
	if features.Enabled(features.ConcurrentAdmission) && concurrentadmission.IsVariant(wl) {
		return false
	}
	return wl.Status.Admission != nil && workload.IsAdmitted(wl) && !workloadfinish.IsFinished(wl) && !workloadevict.IsEvicted(wl)
}

func (t *Tracker) Create(e event.TypedCreateEvent[*kueue.Workload]) bool {
	return shouldTrack(e.Object) || workload.HasPodsScheduledCondition(e.Object)
}

func (t *Tracker) Update(e event.TypedUpdateEvent[*kueue.Workload]) bool {
	return shouldTrack(e.ObjectNew) || workload.HasPodsScheduledCondition(e.ObjectNew)
}

func (t *Tracker) Delete(event.TypedDeleteEvent[*kueue.Workload]) bool {
	return false
}

func (t *Tracker) Generic(event.TypedGenericEvent[*kueue.Workload]) bool {
	return false
}

var _ handler.TypedEventHandler[*corev1.Pod, reconcile.Request] = (*podHandler)(nil)

type podHandler struct{}

func (h *podHandler) Create(ctx context.Context, e event.TypedCreateEvent[*corev1.Pod], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	h.queueReconcileForPod(ctx, e.Object, q)
}

func (h *podHandler) Update(ctx context.Context, e event.TypedUpdateEvent[*corev1.Pod], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	oldPod, newPod := e.ObjectOld, e.ObjectNew
	if !schedulingChanged(oldPod, newPod) {
		return
	}
	oldKey := workloadslicing.KeyForPod(oldPod)
	newKey := workloadslicing.KeyForPod(newPod)
	if oldKey != nil && !ptr.Equal(oldKey, newKey) {
		queueReconcile(ctx, oldPod, *oldKey, q)
	}
	if newKey != nil {
		queueReconcile(ctx, newPod, *newKey, q)
	}
}

func (h *podHandler) Delete(ctx context.Context, e event.TypedDeleteEvent[*corev1.Pod], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	h.queueReconcileForPod(ctx, e.Object, q)
}

func (h *podHandler) Generic(context.Context, event.TypedGenericEvent[*corev1.Pod], workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

func (h *podHandler) queueReconcileForPod(ctx context.Context, pod *corev1.Pod, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	if key := workloadslicing.KeyForPod(pod); key != nil {
		queueReconcile(ctx, pod, *key, q)
	}
}

func queueReconcile(ctx context.Context, pod *corev1.Pod, key types.NamespacedName, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	ctrl.LoggerFrom(ctx).V(5).Info("Queueing reconcile for workload", "pod", klog.KObj(pod), "workload", key.String())
	q.AddAfter(reconcile.Request{NamespacedName: key}, constants.UpdatesBatchPeriod)
}

func schedulingChanged(oldPod, newPod *corev1.Pod) bool {
	return utilpod.IsScheduled(oldPod) != utilpod.IsScheduled(newPod) ||
		oldPod.Status.Phase != newPod.Status.Phase ||
		oldPod.DeletionTimestamp.IsZero() != newPod.DeletionTimestamp.IsZero() ||
		oldPod.Annotations[kueue.WorkloadAnnotation] != newPod.Annotations[kueue.WorkloadAnnotation] ||
		oldPod.Annotations[kueue.WorkloadSliceNameAnnotation] != newPod.Annotations[kueue.WorkloadSliceNameAnnotation] ||
		oldPod.Labels[constants.PodSetLabel] != newPod.Labels[constants.PodSetLabel]
}

func (t *Tracker) SetupWithManager(mgr ctrl.Manager, cfg *configapi.Configuration) (string, error) {
	return controllerName, builder.TypedControllerManagedBy[reconcile.Request](mgr).
		Named("unscheduled_pods_tracker").
		WatchesRawSource(source.TypedKind(
			mgr.GetCache(),
			&kueue.Workload{},
			&handler.TypedEnqueueRequestForObject[*kueue.Workload]{},
			t,
		)).
		WatchesRawSource(source.TypedKind(mgr.GetCache(), &corev1.Pod{}, &podHandler{})).
		WithOptions(controller.Options{
			NeedLeaderElection:      new(false),
			MaxConcurrentReconciles: mgr.GetControllerOptions().GroupKindConcurrency[kueue.SchemeGroupVersion.WithKind("Workload").GroupKind().String()],
		}).
		WithLogConstructor(roletracker.NewLogConstructor(t.roleTracker, controllerName)).
		Complete(core.WithLeadingManager(mgr, t, &kueue.Workload{}, cfg))
}
