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

package elasticjobs

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	autoscaling "k8s.io/autoscaler/cluster-autoscaler/apis/provisioningrequest/autoscaling.x-k8s.io/v1"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	"k8s.io/utils/clock"
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
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	utilclient "sigs.k8s.io/kueue/pkg/util/client"
	"sigs.k8s.io/kueue/pkg/util/expectations"
	"sigs.k8s.io/kueue/pkg/util/parallelize"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	"sigs.k8s.io/kueue/pkg/workload"
	workloadevict "sigs.k8s.io/kueue/pkg/workload/evict"
	workloadfinish "sigs.k8s.io/kueue/pkg/workload/finish"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
)

const ControllerName = "ElasticJobUngater"

var errPendingUngateOps = errors.New("pending elastic ungate operations")

type elasticJobUngater struct {
	client            client.Client
	clock             clock.Clock
	expectationsStore *expectations.Store
	roleTracker       *roletracker.RoleTracker
}

var _ reconcile.Reconciler = (*elasticJobUngater)(nil)
var _ predicate.TypedPredicate[*kueue.Workload] = (*elasticJobUngater)(nil)

// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads,verbs=get;list;watch

func SetupWithManager(mgr ctrl.Manager, cfg *configapi.Configuration, roleTracker *roletracker.RoleTracker) (string, error) {
	r := &elasticJobUngater{
		client:            mgr.GetClient(),
		clock:             clock.RealClock{},
		expectationsStore: expectations.NewStore(ControllerName),
		roleTracker:       roleTracker,
	}
	podHandler := elasticPodHandler{
		client:            r.client,
		expectationsStore: r.expectationsStore,
	}
	// Reconcile by the chain's active slice, resolved from whichever slice fired
	// the event. Every slice maps to the single currently-admitted slice, whose
	// granted counts cap ungating; Reconcile then loads it directly (no lookup
	// through a possibly-deleted origin).
	sliceKeyHandler := handler.TypedEnqueueRequestsFromMapFunc(
		func(ctx context.Context, wl *kueue.Workload) []reconcile.Request {
			active, err := r.activeSlice(ctx, wl)
			if err != nil || active == nil {
				return nil
			}
			return []reconcile.Request{{NamespacedName: types.NamespacedName{
				Namespace: active.Namespace,
				Name:      active.Name,
			}}}
		},
	)
	return ControllerName, builder.TypedControllerManagedBy[reconcile.Request](mgr).
		Named("elastic_job_ungater").
		WatchesRawSource(source.TypedKind(
			mgr.GetCache(),
			&kueue.Workload{},
			sliceKeyHandler,
			r,
		)).
		Watches(&corev1.Pod{}, &podHandler).
		WithOptions(controller.Options{
			NeedLeaderElection:      new(false),
			MaxConcurrentReconciles: mgr.GetControllerOptions().GroupKindConcurrency[kueue.SchemeGroupVersion.WithKind("Workload").GroupKind().String()],
		}).
		WithLogConstructor(roletracker.NewLogConstructor(r.roleTracker, ControllerName)).
		Complete(core.WithLeadingManager(mgr, r, &kueue.Workload{}, cfg))
}

func (r *elasticJobUngater) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	log.V(2).Info("Reconcile ElasticJobUngater")

	// req.Name is whichever slice the enqueue handlers resolved as active when the
	// event fired. It is only a snapshot: a requeue (e.g. after a pod-patch
	// conflict) can re-run this against a slice that has since finished, so the
	// eligibility check below redirects to the current active slice rather than
	// trusting req.Name. If nothing in the chain is admitted anymore, there is
	// nothing to ungate.
	active := &kueue.Workload{}
	if err := r.client.Get(ctx, req.NamespacedName, active); err != nil {
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}
	// The handlers only enqueue active elastic slices, but req.Name is a snapshot:
	// by the time this runs (especially on a requeue after a pod-patch conflict),
	// the enqueued slice may have finished as part of a scale rollover. Bailing
	// here would strand any pending ungate until the next periodic resync, so
	// redirect to the chain's current active slice instead. Eviction is two
	// writes — the condition is set before the reservation is released — so an
	// evicted slice still reports a reservation while its capacity is on the way
	// out; treat it as ineligible too, matching workloadslicing.FindLatestActiveWorkload.
	if !shouldUngate(active) || workloadevict.IsEvicted(active) {
		redirected, err := r.activeSlice(ctx, active)
		if err != nil {
			return reconcile.Result{}, err
		}
		// No eligible active slice, or it is the same (still-ineligible) object we
		// just loaded: nothing new to ungate against.
		if redirected == nil || redirected.Name == active.Name {
			return reconcile.Result{}, nil
		}
		active = redirected
	}

	// Expectations are keyed by the stable chain key (the origin slice name shared
	// by every slice and pod in the chain), not by the rolling active-slice name,
	// so in-flight ungate expectations survive a scale rollover.
	sliceKey := types.NamespacedName{Namespace: active.Namespace, Name: workloadslicing.SliceName(active)}
	if !r.expectationsStore.Satisfied(log, sliceKey) {
		return reconcile.Result{}, errPendingUngateOps
	}

	pods, err := r.podsToUngate(ctx, active)
	if err != nil {
		return reconcile.Result{}, err
	}
	if len(pods) == 0 {
		return reconcile.Result{}, nil
	}

	log.V(2).Info("identified elastic pods to ungate", "count", len(pods))
	uids := make([]types.UID, len(pods))
	for i := range pods {
		uids[i] = pods[i].UID
	}
	r.expectationsStore.ExpectUIDs(log, sliceKey, uids)

	err = parallelize.Until(ctx, len(pods), func(i int) error {
		pod := pods[i]
		var ungated bool
		e := utilclient.Patch(ctx, r.client, pod, func() (bool, error) {
			changed, err := refreshPodAdmission(pod, active)
			if err != nil {
				return false, err
			}
			ungated = utilpod.Ungate(pod, kueue.ElasticJobSchedulingGate)
			if ungated {
				log.V(3).Info("ungating elastic pod", "pod", klog.KObj(pod))
			}
			return changed || ungated, nil
		})
		if e != nil {
			r.expectationsStore.ObservedUID(log, sliceKey, pod.UID)
			log.Error(e, "failed ungating elastic pod", "pod", klog.KObj(pod))
			return e
		}
		if !ungated {
			r.expectationsStore.ObservedUID(log, sliceKey, pod.UID)
		} else {
			utilpod.RecordPodSchedulingGateRemovalSeconds(r.clock, kueue.ElasticJobSchedulingGate, active, false, r.roleTracker)
		}
		return nil
	})
	return reconcile.Result{}, err
}

type podAdmissionUpdate struct {
	annotations  map[string]string
	nodeSelector map[string]string
}

func admissionUpdateForPodSet(wl *kueue.Workload, podSetName kueue.PodSetReference) (podAdmissionUpdate, error) {
	update := podAdmissionUpdate{
		annotations:  make(map[string]string),
		nodeSelector: make(map[string]string),
	}
	for _, check := range wl.Status.AdmissionChecks {
		for _, psUpdate := range check.PodSetUpdates {
			if psUpdate.Name != podSetName {
				continue
			}
			for _, key := range []string{
				autoscaling.ProvisioningRequestPodAnnotationKey,
				autoscaling.ProvisioningClassPodAnnotationKey,
			} {
				if value, found := psUpdate.Annotations[key]; found {
					if old, exists := update.annotations[key]; exists && old != value {
						return podAdmissionUpdate{}, fmt.Errorf("conflicting %q annotation updates for PodSet %q", key, podSetName)
					}
					update.annotations[key] = value
				}
			}
			for key, value := range psUpdate.NodeSelector {
				if old, exists := update.nodeSelector[key]; exists && old != value {
					return podAdmissionUpdate{}, fmt.Errorf("conflicting %q node selector updates for PodSet %q", key, podSetName)
				}
				update.nodeSelector[key] = value
			}
		}
	}
	return update, nil
}

func podAdmissionCompatible(pod *corev1.Pod, update podAdmissionUpdate) bool {
	for key, value := range update.annotations {
		if existing, found := pod.Annotations[key]; found && existing != value {
			return false
		}
	}
	for key, value := range update.nodeSelector {
		if existing, found := pod.Spec.NodeSelector[key]; found && existing != value {
			return false
		}
	}
	return true
}

func refreshPodAdmission(pod *corev1.Pod, wl *kueue.Workload) (bool, error) {
	update, err := admissionUpdateForPodSet(wl, kueue.PodSetReference(pod.Labels[constants.PodSetLabel]))
	if err != nil {
		return false, err
	}
	if !podAdmissionCompatible(pod, update) {
		return false, fmt.Errorf("pod %s/%s has immutable admission metadata from a different ProvisioningRequest", pod.Namespace, pod.Name)
	}
	if pod.Annotations == nil {
		pod.Annotations = make(map[string]string, len(update.annotations))
	}
	changed := false
	for key, value := range update.annotations {
		// PRQ consume/class identity is immutable after first assignment.
		if _, exists := pod.Annotations[key]; exists {
			continue
		}
		pod.Annotations[key] = value
		changed = true
	}
	if pod.Spec.NodeSelector == nil && len(update.nodeSelector) != 0 {
		pod.Spec.NodeSelector = make(map[string]string, len(update.nodeSelector))
	}
	for key, value := range update.nodeSelector {
		if pod.Spec.NodeSelector[key] != value {
			pod.Spec.NodeSelector[key] = value
			changed = true
		}
	}
	return changed, nil
}

func (r *elasticJobUngater) podsToUngate(ctx context.Context, wl *kueue.Workload) ([]*corev1.Pod, error) {
	// All pods in the slice chain share the same WorkloadSliceNameAnnotation,
	// so the index lookup returns every pod created on behalf of this job.
	// Although those pods may still carry an older slice's name in their
	// WorkloadAnnotation (the template is stamped at the slice's admission),
	// wl is the chain's active slice (resolved in Reconcile), so its granted
	// PodSet counts are the right cap for ungating any of them.
	sliceName := workloadslicing.SliceName(wl)
	var podList corev1.PodList
	if err := r.client.List(ctx, &podList,
		client.InNamespace(wl.Namespace),
		client.MatchingFields{indexer.WorkloadSliceNameKey: sliceName},
	); err != nil {
		return nil, fmt.Errorf("listing pods for workload slice: %w", err)
	}

	granted := workload.ExtractGrantedPodSetCounts(wl)
	gatedPerPodSet := make(map[kueue.PodSetReference][]*corev1.Pod)
	ungatedPerPodSet := make(map[kueue.PodSetReference]int32)
	admissionUpdates := make(map[kueue.PodSetReference]podAdmissionUpdate)
	for i := range podList.Items {
		p := &podList.Items[i]
		if utilpod.IsTerminated(p) {
			continue
		}
		ps := kueue.PodSetReference(p.Labels[constants.PodSetLabel])
		if utilpod.HasGate(p, kueue.ElasticJobSchedulingGate) {
			update, found := admissionUpdates[ps]
			if !found {
				var err error
				update, err = admissionUpdateForPodSet(wl, ps)
				if err != nil {
					return nil, err
				}
				admissionUpdates[ps] = update
			}
			if !podAdmissionCompatible(p, update) {
				ctrl.LoggerFrom(ctx).Info("leaving elastic pod gated because immutable admission metadata is stale; recycle the pod after its template refreshes",
					"pod", klog.KObj(p), "podSet", ps)
				continue
			}
			gatedPerPodSet[ps] = append(gatedPerPodSet[ps], p)
		} else {
			// Already-ungated pods consume quota too.
			ungatedPerPodSet[ps]++
		}
	}

	log := ctrl.LoggerFrom(ctx)
	var gated []*corev1.Pod
	for ps, candidates := range gatedPerPodSet {
		room := granted[ps] - ungatedPerPodSet[ps]
		var toUngate []*corev1.Pod
		if room > 0 {
			// Ungate the lowest-named pods first for deterministic behavior.
			slices.SortFunc(candidates, func(a, b *corev1.Pod) int { return strings.Compare(a.Name, b.Name) })
			toUngate = candidates
			if int32(len(candidates)) > room {
				toUngate = candidates[:room]
			}
		}
		log.V(4).Info("elastic ungating quota accounting for PodSet",
			"podSet", ps,
			"grantedCount", granted[ps],
			"alreadyUngatedCount", ungatedPerPodSet[ps],
			"gatedCount", len(candidates),
			"ungatingCount", len(toUngate),
		)
		gated = append(gated, toUngate...)
	}
	return gated, nil
}

// activeSlice resolves the admitted slice of the chain the workload anyWl belongs
// to, or nil if none is admitted.
func (r *elasticJobUngater) activeSlice(ctx context.Context, anyWl *kueue.Workload) (*kueue.Workload, error) {
	return workloadslicing.FindLatestAdmittedWorkloadForSlice(ctx, r.client, anyWl.Namespace, workloadslicing.SliceName(anyWl))
}

// Workload predicates

func (r *elasticJobUngater) Create(e event.TypedCreateEvent[*kueue.Workload]) bool {
	return shouldUngate(e.Object)
}

func (r *elasticJobUngater) Update(e event.TypedUpdateEvent[*kueue.Workload]) bool {
	return shouldUngate(e.ObjectNew)
}

func shouldUngate(wl *kueue.Workload) bool {
	return workloadslicing.IsElasticWorkload(wl) &&
		!workloadfinish.IsFinished(wl) &&
		workload.IsAdmitted(wl)
}

func (r *elasticJobUngater) Delete(event.TypedDeleteEvent[*kueue.Workload]) bool {
	return false
}

func (r *elasticJobUngater) Generic(event.TypedGenericEvent[*kueue.Workload]) bool {
	return false
}

// Pod event handler

var _ handler.EventHandler = (*elasticPodHandler)(nil)

type elasticPodHandler struct {
	client            client.Client
	expectationsStore *expectations.Store
}

func (h *elasticPodHandler) Create(ctx context.Context, e event.CreateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	h.queueReconcileForPod(ctx, e.Object, false, q)
}

func (h *elasticPodHandler) Update(ctx context.Context, e event.UpdateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	h.queueReconcileForPod(ctx, e.ObjectNew, false, q)
}

func (h *elasticPodHandler) Delete(ctx context.Context, e event.DeleteEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	h.queueReconcileForPod(ctx, e.Object, true, q)
}

func (h *elasticPodHandler) Generic(context.Context, event.GenericEvent, workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

func (h *elasticPodHandler) queueReconcileForPod(ctx context.Context, object client.Object, deleted bool, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	pod, isPod := object.(*corev1.Pod)
	if !isPod {
		return
	}
	// Expectations are keyed by the stable chain key (the origin slice name the
	// pod carries), so observations survive scale rollovers.
	sliceName := podSliceName(pod)
	if sliceName == "" {
		return
	}
	sliceKey := types.NamespacedName{Name: sliceName, Namespace: pod.Namespace}
	// Mark expectation as observed when the gate has been removed or the pod is deleted.
	if !utilpod.HasGate(pod, kueue.ElasticJobSchedulingGate) || deleted {
		log := ctrl.LoggerFrom(ctx).WithValues("pod", klog.KObj(pod), "workloadSlice", sliceKey.String())
		h.expectationsStore.ObservedUID(log, sliceKey, pod.UID)
	}
	active, err := workloadslicing.FindLatestAdmittedWorkloadForSlice(ctx, h.client, pod.Namespace, sliceName)
	if err != nil || active == nil {
		return
	}
	q.AddAfter(reconcile.Request{NamespacedName: types.NamespacedName{
		Namespace: active.Namespace,
		Name:      active.Name,
	}}, constants.UpdatesBatchPeriod)
}

// podSliceName returns the slice-chain key for a pod: the WorkloadSliceName
// annotation if present, otherwise the stamped Workload annotation. Mirrors
// indexer.IndexPodWorkloadSliceName so the key matches the pod index.
func podSliceName(pod *corev1.Pod) string {
	if v, found := pod.Annotations[kueue.WorkloadSliceNameAnnotation]; found {
		return v
	}
	return pod.Annotations[kueue.WorkloadAnnotation]
}
