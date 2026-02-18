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
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	controllerconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	clientutil "sigs.k8s.io/kueue/pkg/util/client"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
)

type PodReconciler struct {
	client                     client.Client
	manageJobsWithoutQueueName bool
	roleTracker                *roletracker.RoleTracker
}

const podControllerName = "statefulset_pod"

func NewPodReconciler(_ context.Context, client client.Client, _ client.FieldIndexer, _ record.EventRecorder, opts ...jobframework.Option) (jobframework.JobReconcilerInterface, error) {
	options := jobframework.ProcessOptions(opts...)
	return &PodReconciler{
		client:                     client,
		manageJobsWithoutQueueName: options.ManageJobsWithoutQueueName,
		roleTracker:                options.RoleTracker,
	}, nil
}

var _ jobframework.JobReconcilerInterface = (*PodReconciler)(nil)

func (r *PodReconciler) SetupWithManager(mgr ctrl.Manager) error {
	ctrl.Log.V(3).Info("Setting up Pod reconciler for StatefulSet")
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Pod{}).
		Named(podControllerName).
		WithEventFilter(r).
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

	if utilpod.IsTerminated(pod) || pod.DeletionTimestamp != nil {
		err = client.IgnoreNotFound(clientutil.Patch(ctx, r.client, pod, func() (bool, error) {
			removed := controllerutil.RemoveFinalizer(pod, podconstants.PodFinalizer)
			if removed {
				log.V(3).Info(
					"Finalizing statefulset pod in group",
					"pod", klog.KObj(pod),
					"group", pod.Labels[podconstants.GroupNameLabel],
				)
			}
			return removed, nil
		}))
	} else {
		err = client.IgnoreNotFound(clientutil.Patch(ctx, r.client, pod, func() (bool, error) {
			updated, err := r.setDefault(ctx, pod)
			if err != nil {
				return false, err
			}
			if updated {
				log.V(3).Info("Updating pod in group", "pod", klog.KObj(pod), "group", pod.Labels[podconstants.GroupNameLabel])
			}
			return updated, nil
		}))
	}

	return ctrl.Result{}, err
}

func (r *PodReconciler) setDefault(ctx context.Context, pod *corev1.Pod) (bool, error) {
	controllerRef := metav1.GetControllerOf(pod)
	if controllerRef == nil {
		return false, nil
	}
	if controllerRef.Kind != gvk.Kind || controllerRef.APIVersion != gvk.GroupVersion().String() {
		return false, nil
	}

	sts := &appsv1.StatefulSet{}
	err := r.client.Get(ctx, client.ObjectKey{Namespace: pod.Namespace, Name: controllerRef.Name}, sts)
	if err != nil {
		return false, client.IgnoreNotFound(err)
	}

	queueName := jobframework.QueueNameForObject(sts)
	wlName := GetWorkloadName(sts.Name)

	if pod.Labels[podconstants.GroupNameLabel] == wlName {
		if queueName != "" && pod.Labels[controllerconstants.QueueLabel] != string(queueName) {
			pod.Labels[controllerconstants.QueueLabel] = string(queueName)
			return true, nil
		}
		return false, nil
	}

	if queueName == "" && !r.manageJobsWithoutQueueName {
		return false, nil
	}

	if pod.Labels == nil {
		pod.Labels = make(map[string]string)
	}
	pod.Labels[constants.ManagedByKueueLabelKey] = constants.ManagedByKueueLabelValue
	pod.Labels[podconstants.GroupNameLabel] = wlName
	pod.Labels[controllerconstants.PrebuiltWorkloadLabel] = wlName
	if queueName != "" {
		pod.Labels[controllerconstants.QueueLabel] = string(queueName)
	}

	if priorityClass := jobframework.WorkloadPriorityClassName(sts); priorityClass != "" {
		pod.Labels[controllerconstants.WorkloadPriorityClassLabel] = priorityClass
	}

	pod.Annotations[podconstants.GroupTotalCountAnnotation] = fmt.Sprint(ptr.Deref(sts.Spec.Replicas, 1))
	pod.Annotations[podconstants.GroupFastAdmissionAnnotationKey] = podconstants.GroupFastAdmissionAnnotationValue
	pod.Annotations[podconstants.GroupServingAnnotationKey] = podconstants.GroupServingAnnotationValue
	pod.Annotations[kueue.PodGroupPodIndexLabelAnnotation] = appsv1.PodIndexLabel
	pod.Annotations[podconstants.RoleHashAnnotation] = string(kueue.DefaultPodSetName)

	return true, nil
}

var _ predicate.Predicate = (*PodReconciler)(nil)

func (r *PodReconciler) Generic(event.GenericEvent) bool {
	return false
}

func (r *PodReconciler) Create(e event.CreateEvent) bool {
	return r.handle(e.Object)
}

func (r *PodReconciler) Update(e event.UpdateEvent) bool {
	return r.handle(e.ObjectNew)
}

func (r *PodReconciler) Delete(event.DeleteEvent) bool {
	return false
}

func (r *PodReconciler) handle(obj client.Object) bool {
	pod, isPod := obj.(*corev1.Pod)
	if !isPod {
		return false
	}
	return pod.Annotations[podconstants.SuspendedByParentAnnotation] == FrameworkName
}
