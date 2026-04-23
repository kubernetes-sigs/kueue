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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
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
	"sigs.k8s.io/kueue/pkg/workloadslicing"
)

const ControllerName = "ElasticJobUngater"

var errPendingUngateOps = errors.New("pending elastic ungate operations")

type elasticJobUngater struct {
	client            client.Client
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
		expectationsStore: expectations.NewStore(ControllerName),
		roleTracker:       roleTracker,
	}
	podHandler := elasticPodHandler{
		expectationsStore: r.expectationsStore,
	}
	return ControllerName, builder.TypedControllerManagedBy[reconcile.Request](mgr).
		Named("elastic_job_ungater").
		WatchesRawSource(source.TypedKind(
			mgr.GetCache(),
			&kueue.Workload{},
			&handler.TypedEnqueueRequestForObject[*kueue.Workload]{},
			r,
		)).
		Watches(&corev1.Pod{}, &podHandler).
		WithOptions(controller.Options{
			NeedLeaderElection:      new(false),
			MaxConcurrentReconciles: mgr.GetControllerOptions().GroupKindConcurrency[kueue.GroupVersion.WithKind("Workload").GroupKind().String()],
		}).
		WithLogConstructor(roletracker.NewLogConstructor(r.roleTracker, ControllerName)).
		Complete(core.WithLeadingManager(mgr, r, &kueue.Workload{}, cfg))
}

func (r *elasticJobUngater) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	log.V(2).Info("Reconcile ElasticJobUngater")

	wl := &kueue.Workload{}
	if err := r.client.Get(ctx, req.NamespacedName, wl); err != nil {
		if client.IgnoreNotFound(err) != nil {
			return reconcile.Result{}, err
		}
		return reconcile.Result{}, nil
	}
	if !r.expectationsStore.Satisfied(log, req.NamespacedName) {
		return reconcile.Result{}, errPendingUngateOps
	}
	// Ungate pods for workloads that were admitted (including finished ones
	// whose pods may still be gated after scale-up). Pending workloads that
	// were never admitted keep their pods gated.
	if !workloadslicing.IsElasticWorkload(wl) {
		return reconcile.Result{}, nil
	}
	if !workload.IsAdmitted(wl) && !workload.HasQuotaReservation(wl) {
		return reconcile.Result{}, nil
	}

	pods, err := r.listGatedPods(ctx, wl)
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
	r.expectationsStore.ExpectUIDs(log, req.NamespacedName, uids)

	err = parallelize.Until(ctx, len(pods), func(i int) error {
		pod := pods[i]
		var ungated bool
		e := utilclient.Patch(ctx, r.client, pod, func() (bool, error) {
			ungated = utilpod.Ungate(pod, kueue.ElasticJobSchedulingGate)
			if ungated {
				log.V(3).Info("ungating elastic pod", "pod", klog.KObj(pod))
			}
			return ungated, nil
		})
		if e != nil {
			r.expectationsStore.ObservedUID(log, req.NamespacedName, pod.UID)
			log.Error(e, "failed ungating elastic pod", "pod", klog.KObj(pod))
		}
		return e
	})
	return reconcile.Result{}, err
}

func (r *elasticJobUngater) listGatedPods(ctx context.Context, wl *kueue.Workload) ([]*corev1.Pod, error) {
	sliceName := workloadslicing.SliceName(wl)
	var podList corev1.PodList
	if err := r.client.List(ctx, &podList,
		client.InNamespace(wl.Namespace),
		client.MatchingFields{indexer.WorkloadSliceNameKey: sliceName},
	); err != nil {
		return nil, fmt.Errorf("listing pods for workload slice: %w", err)
	}

	var gated []*corev1.Pod
	for i := range podList.Items {
		if utilpod.HasGate(&podList.Items[i], kueue.ElasticJobSchedulingGate) &&
			podList.Items[i].Annotations[kueue.WorkloadAnnotation] == wl.Name {
			gated = append(gated, &podList.Items[i])
		}
	}
	return gated, nil
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
		(workload.IsAdmitted(wl) || workload.HasQuotaReservation(wl))
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
	wlName, found := pod.Annotations[kueue.WorkloadAnnotation]
	if !found {
		return
	}
	key := types.NamespacedName{
		Name:      wlName,
		Namespace: pod.Namespace,
	}
	// Mark expectation as observed when the gate has been removed or the pod is deleted.
	if !utilpod.HasGate(pod, kueue.ElasticJobSchedulingGate) || deleted {
		log := ctrl.LoggerFrom(ctx).WithValues("pod", klog.KObj(pod), "workload", key.String())
		h.expectationsStore.ObservedUID(log, key, pod.UID)
	}
	q.AddAfter(reconcile.Request{NamespacedName: key}, constants.UpdatesBatchPeriod)
}
