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

	err := r.fetchAndFinalizePods(ctx, req)
	return ctrl.Result{}, err
}

func (r *Reconciler) fetchAndFinalizePods(ctx context.Context, req reconcile.Request) error {
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

	sts := &appsv1.StatefulSet{}
	err := r.client.Get(ctx, req.NamespacedName, sts)
	if client.IgnoreNotFound(err) != nil {
		return err
	}

	if err != nil {
		sts = nil
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
	return client.IgnoreNotFound(clientutil.Patch(ctx, r.client, pod, true, func() (bool, error) {
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

func NewReconciler(client client.Client, _ record.EventRecorder, opts ...jobframework.Option) jobframework.JobReconcilerInterface {
	options := jobframework.ProcessOptions(opts...)

	return &Reconciler{
		client:                       client,
		log:                          ctrl.Log.WithName("statefulset-reconciler"),
		manageJobsWithoutQueueName:   options.ManageJobsWithoutQueueName,
		managedJobsNamespaceSelector: options.ManagedJobsNamespaceSelector,
	}
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
