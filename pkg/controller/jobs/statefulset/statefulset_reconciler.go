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

package statefulset

import (
	"context"
	"time"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	podcontroller "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	clientutil "sigs.k8s.io/kueue/pkg/util/client"
	"sigs.k8s.io/kueue/pkg/util/parallelize"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
)

const (
	podBatchPeriod = time.Second
)

// +kubebuilder:rbac:groups="apps",resources=statefulsets,verbs=get;list;watch

var (
	_ jobframework.JobReconcilerInterface = (*Reconciler)(nil)
)

type Reconciler struct {
	client                       client.Client
	log                          logr.Logger
	manageJobsWithoutQueueName   bool
	managedJobsNamespaceSelector labels.Selector
}

func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	log.V(2).Info("Reconcile StatefulSet")

	sts := &appsv1.StatefulSet{}
	if err := r.client.Get(ctx, req.NamespacedName, sts); err != nil {
		if client.IgnoreNotFound(err) != nil {
			return ctrl.Result{}, err
		}
		// StatefulSet was deleted, finalize orphaned pods.
		return ctrl.Result{}, r.fetchAndFinalizePods(ctx, req, nil)
	}

	// Ensure the StatefulSet is an owner of the workload to prevent
	// Kubernetes GC from deleting it when pods are replaced during rolling updates.
	// We log errors but don't block pod finalization, as ownership is best-effort.
	if err := r.ensureWorkloadOwnership(ctx, sts); err != nil {
		log.Error(err, "Failed to ensure workload ownership, will retry")
	}

	err := r.fetchAndFinalizePods(ctx, req, sts)
	return ctrl.Result{}, err
}

// ensureWorkloadOwnership ensures the StatefulSet is an owner of the workload.
// This prevents Kubernetes GC from deleting the workload when pods are replaced
// during rolling updates.
func (r *Reconciler) ensureWorkloadOwnership(ctx context.Context, sts *appsv1.StatefulSet) error {
	if sts == nil {
		return nil
	}

	log := ctrl.LoggerFrom(ctx)
	workloadName := GetWorkloadName(sts.Name)

	wl := &kueue.Workload{}
	if err := r.client.Get(ctx, types.NamespacedName{Name: workloadName, Namespace: sts.Namespace}, wl); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}

	// Check if StatefulSet is already an owner.
	for _, ref := range wl.GetOwnerReferences() {
		if ref.UID == sts.UID {
			return nil
		}
	}

	// Add StatefulSet as an owner.
	if err := controllerutil.SetOwnerReference(sts, wl, r.client.Scheme()); err != nil {
		return err
	}

	if err := r.client.Update(ctx, wl); err != nil {
		if apierrors.IsConflict(err) {
			// Conflict means the workload was modified concurrently; ownership may not be set.
			// Will retry on next reconciliation.
			log.V(4).Info("Conflict updating workload ownership, will retry on next reconciliation",
				"statefulset", klog.KObj(sts),
				"workload", klog.KObj(wl),
			)
			return nil
		}
		return err
	}
	log.V(3).Info("Added StatefulSet as owner of workload",
		"statefulset", klog.KObj(sts),
		"workload", klog.KObj(wl),
	)

	return nil
}

func (r *Reconciler) fetchAndFinalizePods(ctx context.Context, req reconcile.Request, sts *appsv1.StatefulSet) error {
	podList := &corev1.PodList{}
	if err := r.client.List(ctx, podList, client.InNamespace(req.Namespace), client.MatchingLabels{
		podcontroller.GroupNameLabel: GetWorkloadName(req.Name),
	}); err != nil {
		return err
	}

	// If no Pods are found, there's nothing to do.
	if len(podList.Items) == 0 {
		return nil
	}

	return r.finalizePods(ctx, sts, podList.Items)
}

func (r *Reconciler) finalizePods(ctx context.Context, sts *appsv1.StatefulSet, pods []corev1.Pod) error {
	return parallelize.Until(ctx, len(pods), func(i int) error {
		return r.finalizePod(ctx, sts, &pods[i])
	})
}

func (r *Reconciler) finalizePod(ctx context.Context, sts *appsv1.StatefulSet, pod *corev1.Pod) error {
	log := ctrl.LoggerFrom(ctx)
	return client.IgnoreNotFound(clientutil.Patch(ctx, r.client, pod, func() (bool, error) {
		if ungateAndFinalize(sts, pod) {
			log.V(3).Info(
				"Finalizing pod in group",
				"pod", klog.KObj(pod),
				"group", pod.Labels[podcontroller.GroupNameLabel],
			)
			return true, nil
		}
		return false, nil
	}))
}

func ungateAndFinalize(sts *appsv1.StatefulSet, pod *corev1.Pod) bool {
	var updated bool

	if shouldUngate(sts, pod) && utilpod.Ungate(pod, podcontroller.SchedulingGateName) {
		updated = true
	}

	if shouldFinalize(sts, pod) && controllerutil.RemoveFinalizer(pod, podcontroller.PodFinalizer) {
		updated = true
	}

	return updated
}

func shouldUngate(sts *appsv1.StatefulSet, pod *corev1.Pod) bool {
	return sts == nil || sts.Status.CurrentRevision != sts.Status.UpdateRevision &&
		sts.Status.CurrentRevision == pod.Labels[appsv1.ControllerRevisionHashLabelKey]
}

func shouldFinalize(sts *appsv1.StatefulSet, pod *corev1.Pod) bool {
	return shouldUngate(sts, pod) || utilpod.IsTerminated(pod)
}

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	ctrl.Log.V(3).Info("Setting up StatefulSet reconciler")
	return ctrl.NewControllerManagedBy(mgr).
		For(&appsv1.StatefulSet{}).
		WithEventFilter(r).
		Watches(&corev1.Pod{}, &podHandler{}).
		Complete(r)
}

func NewReconciler(_ context.Context, client client.Client, _ client.FieldIndexer, _ record.EventRecorder, opts ...jobframework.Option) (jobframework.JobReconcilerInterface, error) {
	options := jobframework.ProcessOptions(opts...)

	return &Reconciler{
		client:                       client,
		log:                          ctrl.Log.WithName("statefulset-reconciler"),
		manageJobsWithoutQueueName:   options.ManageJobsWithoutQueueName,
		managedJobsNamespaceSelector: options.ManagedJobsNamespaceSelector,
	}, nil
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

func (r *Reconciler) Delete(e event.DeleteEvent) bool {
	return r.handle(e.Object)
}

func (r *Reconciler) handle(obj client.Object) bool {
	sts, isSts := obj.(*appsv1.StatefulSet)
	if !isSts {
		return true
	}

	ctx := context.Background()
	log := r.log.WithValues("statefulset", klog.KObj(sts))
	ctrl.LoggerInto(ctx, log)

	// Handle only statefulset managed by kueue.
	suspend, err := jobframework.WorkloadShouldBeSuspended(ctx, sts, r.client, r.manageJobsWithoutQueueName, r.managedJobsNamespaceSelector)
	if err != nil {
		log.Error(err, "Failed to determine if the StatefulSet should be managed by Kueue")
	}

	return suspend
}

var _ handler.EventHandler = (*podHandler)(nil)

type podHandler struct{}

func (h *podHandler) Generic(context.Context, event.GenericEvent, workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

func (h *podHandler) Create(_ context.Context, e event.CreateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	// To watch for pod ADDED events.
	h.handle(e.Object, q)
}

func (h *podHandler) Update(_ context.Context, e event.UpdateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

func (h *podHandler) Delete(context.Context, event.DeleteEvent, workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

func (h *podHandler) handle(obj client.Object, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	pod, isPod := obj.(*corev1.Pod)
	if !isPod || pod.Annotations[podcontroller.SuspendedByParentAnnotation] != FrameworkName {
		return
	}
	if controllerRef := metav1.GetControllerOf(pod); controllerRef != nil {
		if controllerRef.Kind != gvk.Kind || controllerRef.APIVersion != gvk.GroupVersion().String() {
			// The pod is controlled by an owner that is not an apps/v1 StatefulSet.
			return
		}
		q.AddAfter(reconcile.Request{
			NamespacedName: types.NamespacedName{
				Namespace: pod.Namespace,
				Name:      controllerRef.Name,
			},
		}, podBatchPeriod)
	}
}
