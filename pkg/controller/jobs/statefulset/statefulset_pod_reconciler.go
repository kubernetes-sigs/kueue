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
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
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

	"sigs.k8s.io/kueue/pkg/constants"
	controllerconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	clientutil "sigs.k8s.io/kueue/pkg/util/client"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
)

// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;patch

type PodReconciler struct {
	client      client.Client
	roleTracker *roletracker.RoleTracker
}

const podControllerName = "statefulset_pod"

func NewPodReconciler(_ context.Context, client client.Client, _ client.FieldIndexer, _ record.EventRecorder, opts ...jobframework.Option) (jobframework.JobReconcilerInterface, error) {
	options := jobframework.ProcessOptions(opts...)
	return &PodReconciler{
		client:      client,
		roleTracker: options.RoleTracker,
	}, nil
}

var _ jobframework.JobReconcilerInterface = (*PodReconciler)(nil)

func (r *PodReconciler) SetupWithManager(mgr ctrl.Manager) error {
	ctrl.Log.V(3).Info("Setting up Pod reconciler for StatefulSet")
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Pod{}, builder.WithPredicates(r)).
		Named(podControllerName).
		Watches(&appsv1.StatefulSet{}, handler.EnqueueRequestsFromMapFunc(r.enqueueManagedPods)).
		WithOptions(controller.Options{
			LogConstructor: roletracker.NewLogConstructor(r.roleTracker, podControllerName),
		}).
		Complete(r)
}

func (r *PodReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	pod := &corev1.Pod{}
	err := r.client.Get(ctx, req.NamespacedName, pod)
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	log := ctrl.LoggerFrom(ctx)
	log.V(2).Info("Reconcile StatefulSet Pod")

	sts, err := r.getOwnerStatefulSet(ctx, pod)
	if err != nil {
		return ctrl.Result{}, err
	}

	if utilpod.IsTerminated(pod) || pod.DeletionTimestamp != nil {
		err = client.IgnoreNotFound(clientutil.Patch(ctx, r.client, pod, func() (bool, error) {
			updated := ungateAndFinalize(sts, pod)
			if updated {
				log.V(3).Info(
					"Finalizing pod in group",
					"pod", klog.KObj(pod),
					"group", pod.Labels[podconstants.GroupNameLabel],
				)
			}
			return updated, nil
		}))
	} else {
		err = client.IgnoreNotFound(clientutil.Patch(ctx, r.client, pod, func() (bool, error) {
			setDefaultUpdated := r.setDefault(pod, sts)
			ungateUpdated := ungateAndFinalize(sts, pod)
			updated := setDefaultUpdated || ungateUpdated
			if updated {
				log.V(3).Info("Updating pod in group", "pod", klog.KObj(pod), "group", pod.Labels[podconstants.GroupNameLabel])
			}
			return updated, nil
		}))
	}

	return ctrl.Result{}, err
}

func (r *PodReconciler) setDefault(pod *corev1.Pod, sts *appsv1.StatefulSet) bool {
	if sts == nil {
		return false
	}

	var updated bool

	workloadName := GetWorkloadName(GetOwnerUID(sts), sts.Name)
	queueName := string(jobframework.QueueNameForObject(sts))
	groupTotalCount := fmt.Sprint(ptr.Deref(sts.Spec.Replicas, 1))

	if pod.Labels == nil {
		pod.Labels = make(map[string]string, 4)
	}
	if pod.Annotations == nil {
		pod.Annotations = make(map[string]string, 1)
	}

	if pod.Labels[constants.ManagedByKueueLabelKey] != constants.ManagedByKueueLabelValue {
		pod.Labels[constants.ManagedByKueueLabelKey] = constants.ManagedByKueueLabelValue
		updated = true
	}

	if pod.Labels[podconstants.GroupNameLabel] != workloadName {
		pod.Labels[podconstants.GroupNameLabel] = workloadName
		updated = true
	}

	if queueName != "" && pod.Labels[controllerconstants.QueueLabel] != queueName {
		pod.Labels[controllerconstants.QueueLabel] = queueName
		updated = true
	}

	if pod.Annotations[podconstants.GroupTotalCountAnnotation] != groupTotalCount {
		pod.Annotations[podconstants.GroupTotalCountAnnotation] = groupTotalCount
		updated = true
	}

	return updated
}

func (r *PodReconciler) getOwnerStatefulSet(ctx context.Context, pod *corev1.Pod) (*appsv1.StatefulSet, error) {
	controllerRef := metav1.GetControllerOf(pod)
	if controllerRef == nil {
		return nil, nil
	}
	if controllerRef.Kind != gvk.Kind || controllerRef.APIVersion != gvk.GroupVersion().String() {
		return nil, nil
	}

	sts := &appsv1.StatefulSet{}
	err := r.client.Get(ctx, client.ObjectKey{Namespace: pod.Namespace, Name: controllerRef.Name}, sts)
	if err != nil {
		return nil, client.IgnoreNotFound(err)
	}
	return sts, nil
}

func (r *PodReconciler) enqueueManagedPods(ctx context.Context, obj client.Object) []reconcile.Request {
	sts, ok := obj.(*appsv1.StatefulSet)
	if !ok {
		return nil
	}

	if sts.Spec.Template.Annotations[podconstants.SuspendedByParentAnnotation] != FrameworkName {
		return nil
	}

	podList := &corev1.PodList{}
	if err := r.client.List(ctx, podList,
		client.InNamespace(sts.Namespace),
		client.MatchingLabels(sts.Spec.Selector.MatchLabels),
	); err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "Failed to list pods for StatefulSet", "statefulset", klog.KObj(sts))
		return nil
	}

	var requests []reconcile.Request
	for i := range podList.Items {
		pod := &podList.Items[i]
		if pod.Annotations[podconstants.SuspendedByParentAnnotation] != FrameworkName {
			continue
		}
		requests = append(requests, reconcile.Request{
			NamespacedName: client.ObjectKeyFromObject(pod),
		})
	}

	return requests
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

var _ predicate.Predicate = (*PodReconciler)(nil)

func (r *PodReconciler) Generic(event.GenericEvent) bool {
	return false
}

func (r *PodReconciler) Create(e event.CreateEvent) bool {
	return r.handlePod(e.Object)
}

func (r *PodReconciler) Update(e event.UpdateEvent) bool {
	return r.handlePod(e.ObjectNew)
}

func (r *PodReconciler) Delete(e event.DeleteEvent) bool {
	return r.handlePod(e.Object)
}

func (r *PodReconciler) handlePod(obj client.Object) bool {
	pod, isPod := obj.(*corev1.Pod)
	if !isPod {
		return false
	}
	return pod.Annotations[podconstants.SuspendedByParentAnnotation] == FrameworkName
}
