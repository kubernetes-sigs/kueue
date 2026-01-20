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
	"golang.org/x/sync/errgroup"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	controllerconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	podcontroller "sigs.k8s.io/kueue/pkg/controller/jobs/pod"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	clientutil "sigs.k8s.io/kueue/pkg/util/client"
	"sigs.k8s.io/kueue/pkg/util/parallelize"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
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
	roleTracker                  *roletracker.RoleTracker
}

const controllerName = "statefulset"

func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	log.V(2).Info("Reconcile StatefulSet")

	sts := &appsv1.StatefulSet{}
	err := r.client.Get(ctx, req.NamespacedName, sts)
	if client.IgnoreNotFound(err) != nil {
		return ctrl.Result{}, err
	}

	if err != nil {
		sts = nil
	}

	var workloadName string
	if sts != nil {
		workloadName = GetWorkloadName(sts.UID, sts.Name)
	}

	podList := &corev1.PodList{}
	if err := r.client.List(ctx, podList, client.InNamespace(req.Namespace), client.MatchingLabels{
		constants.ManagedByKueueLabelKey: constants.ManagedByKueueLabelValue,
	}); err != nil {
		return ctrl.Result{}, err
	}

	var stsPods []corev1.Pod
	for _, p := range podList.Items {
		if owner := metav1.GetControllerOf(&p); owner != nil && owner.APIVersion == gvk.GroupVersion().String() &&
			owner.Kind == gvk.Kind && owner.Name == req.Name {
			stsPods = append(stsPods, p)
		}
	}

	if sts != nil {
		if err := r.ensurePodsLabels(ctx, workloadName, stsPods); err != nil {
			return ctrl.Result{}, err
		}
	}

	eg, ctx := errgroup.WithContext(ctx)

	eg.Go(func() error {
		return r.finalizePods(ctx, sts, stsPods)
	})

	eg.Go(func() error {
		return r.reconcileWorkload(ctx, sts)
	})

	err = eg.Wait()
	return ctrl.Result{}, err
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
				"group", pod.Labels[podconstants.GroupNameLabel],
			)
			return true, nil
		}
		return false, nil
	}))
}

func ungateAndFinalize(sts *appsv1.StatefulSet, pod *corev1.Pod) bool {
	var updated bool

	if shouldUngate(sts, pod) && utilpod.Ungate(pod, podconstants.SchedulingGateName) {
		updated = true
	}

	// TODO (#8571): As discussed in https://github.com/kubernetes-sigs/kueue/issues/8571,
	// this check should be removed in v0.20.
	if shouldFinalize(sts, pod) && controllerutil.RemoveFinalizer(pod, podconstants.PodFinalizer) {
		updated = true
	}

	return updated
}

func shouldUngate(sts *appsv1.StatefulSet, pod *corev1.Pod) bool {
	return sts == nil || sts.Status.CurrentRevision != sts.Status.UpdateRevision &&
		sts.Status.CurrentRevision == pod.Labels[appsv1.ControllerRevisionHashLabelKey]
}

func shouldFinalize(sts *appsv1.StatefulSet, pod *corev1.Pod) bool {
	return shouldUngate(sts, pod) || utilpod.IsTerminated(pod) || pod.DeletionTimestamp != nil
}

func (r *Reconciler) reconcileWorkload(ctx context.Context, sts *appsv1.StatefulSet) error {
	if sts == nil {
		return nil
	}

	workloadName := GetWorkloadName(sts.UID, sts.Name)
	wl := &kueue.Workload{}
	err := r.client.Get(ctx, client.ObjectKey{Namespace: sts.Namespace, Name: workloadName}, wl)
	if err != nil {
		if client.IgnoreNotFound(err) != nil {
			return err
		}
		return r.createPrebuiltWorkload(ctx, sts, workloadName)
	}

	hasOwnerReference, err := controllerutil.HasOwnerReference(wl.OwnerReferences, sts, r.client.Scheme())
	if err != nil {
		return err
	}

	var (
		shouldUpdate = false
		replicas     = ptr.Deref(sts.Spec.Replicas, 1)
	)

	switch {
	case hasOwnerReference && replicas == 0:
		shouldUpdate = true
		err = controllerutil.RemoveOwnerReference(sts, wl, r.client.Scheme())
	case !hasOwnerReference && replicas > 0:
		shouldUpdate = true
		err = controllerutil.SetOwnerReference(sts, wl, r.client.Scheme())
	}
	if err != nil || !shouldUpdate {
		return err
	}

	err = r.client.Update(ctx, wl)
	return err
}

func (r *Reconciler) createPrebuiltWorkload(ctx context.Context, sts *appsv1.StatefulSet, workloadName string) error {
	createdWorkload, err := r.constructWorkload(sts, workloadName)
	if err != nil {
		return err
	}

	err = jobframework.PrepareWorkloadPriority(ctx, r.client, sts, createdWorkload, nil)
	if err != nil {
		return err
	}

	err = r.client.Create(ctx, createdWorkload)
	if err != nil {
		return err
	}

	return nil
}

func (r *Reconciler) constructWorkload(sts *appsv1.StatefulSet, workloadName string) (*kueue.Workload, error) {
	podSets := []kueue.PodSet{
		{
			Name:  kueue.DefaultPodSetName,
			Count: ptr.Deref(sts.Spec.Replicas, 1),
			Template: corev1.PodTemplateSpec{
				Spec: *sts.Spec.Template.Spec.DeepCopy(),
			},
		},
	}
	for i := range podSets {
		jobframework.SanitizePodSet(&podSets[i])
	}
	createdWorkload := podcontroller.NewGroupWorkload(workloadName, sts, podSets, nil)
	if err := controllerutil.SetOwnerReference(sts, createdWorkload, r.client.Scheme()); err != nil {
		return nil, err
	}
	return createdWorkload, nil
}

func (r *Reconciler) ensurePodsLabels(ctx context.Context, wlName string, pods []corev1.Pod) error {
	return parallelize.Until(ctx, len(pods), func(i int) error {
		pod := &pods[i]
		return client.IgnoreNotFound(clientutil.Patch(ctx, r.client, pod, func() (bool, error) {
			updated := false
			if pod.Labels == nil {
				pod.Labels = make(map[string]string)
			}
			if pod.Labels[podconstants.GroupNameLabel] != wlName {
				pod.Labels[podconstants.GroupNameLabel] = wlName
				updated = true
			}
			if pod.Labels[controllerconstants.PrebuiltWorkloadLabel] != wlName {
				pod.Labels[controllerconstants.PrebuiltWorkloadLabel] = wlName
				updated = true
			}
			return updated, nil
		}))
	})
}

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	ctrl.Log.V(3).Info("Setting up StatefulSet reconciler")
	return ctrl.NewControllerManagedBy(mgr).
		For(&appsv1.StatefulSet{}).
		WithEventFilter(r).
		Watches(&corev1.Pod{}, &podHandler{}).
		WithOptions(controller.Options{
			LogConstructor: roletracker.NewLogConstructor(r.roleTracker, controllerName),
		}).
		Complete(r)
}

func NewReconciler(_ context.Context, client client.Client, _ client.FieldIndexer, _ record.EventRecorder, opts ...jobframework.Option) (jobframework.JobReconcilerInterface, error) {
	options := jobframework.ProcessOptions(opts...)

	return &Reconciler{
		client:                       client,
		log:                          roletracker.WithReplicaRole(ctrl.Log.WithName("statefulset-reconciler"), options.RoleTracker),
		manageJobsWithoutQueueName:   options.ManageJobsWithoutQueueName,
		managedJobsNamespaceSelector: options.ManagedJobsNamespaceSelector,
		roleTracker:                  options.RoleTracker,
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
	if !isPod || pod.Annotations[podconstants.SuspendedByParentAnnotation] != FrameworkName {
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
