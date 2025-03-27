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

package leaderworkerset

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	leaderworkersetv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/constants"
	controllerconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	clientutil "sigs.k8s.io/kueue/pkg/util/client"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
)

type PodReconciler struct {
	client client.Client
}

func NewPodReconciler(client client.Client, _ record.EventRecorder, _ ...jobframework.Option) jobframework.JobReconcilerInterface {
	return &PodReconciler{client: client}
}

var _ jobframework.JobReconcilerInterface = (*PodReconciler)(nil)

func (r *PodReconciler) SetupWithManager(mgr ctrl.Manager) error {
	ctrl.Log.V(3).Info("Setting up Pod reconciler for LeaderWorkerSet")
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Pod{}).
		Named("leaderworkerset_pod").
		WithEventFilter(r).
		Complete(r)
}

func (r *PodReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	pod := &corev1.Pod{}
	err := r.client.Get(ctx, req.NamespacedName, pod)
	if err != nil {
		// we'll ignore not-found errors, since there is nothing to do.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	log := ctrl.LoggerFrom(ctx)
	log.V(2).Info("Reconcile LeaderWorkerSet Pod")

	if utilpod.IsTerminated(pod) {
		err = client.IgnoreNotFound(clientutil.Patch(ctx, r.client, pod, true, func() (bool, error) {
			removed := controllerutil.RemoveFinalizer(pod, podconstants.PodFinalizer)
			if removed {
				log.V(3).Info(
					"Finalizing leaderworkerset pod in group",
					"leaderworkerset", pod.Labels[leaderworkersetv1.SetNameLabelKey],
					"pod", klog.KObj(pod),
					"group", pod.Labels[podconstants.GroupNameLabel],
				)
			}
			return removed, nil
		}))
	} else {
		err = client.IgnoreNotFound(clientutil.Patch(ctx, r.client, pod, true, func() (bool, error) {
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
	// If queue label already exist nothing to update.
	if _, ok := pod.Labels[controllerconstants.QueueLabel]; ok {
		return false, nil
	}

	// We should wait for GroupIndexLabelKey.
	if _, ok := pod.Labels[leaderworkersetv1.GroupIndexLabelKey]; !ok {
		return false, nil
	}

	lws := &leaderworkersetv1.LeaderWorkerSet{}
	err := r.client.Get(ctx, client.ObjectKey{Namespace: pod.Namespace, Name: pod.Labels[leaderworkersetv1.SetNameLabelKey]}, lws)
	if err != nil {
		return false, client.IgnoreNotFound(err)
	}

	queueName := jobframework.QueueNameForObject(lws)
	// Ignore LeaderWorkerSet without queue name.
	if queueName == "" {
		return false, nil
	}

	wlName := GetWorkloadName(lws.UID, lws.Name, pod.Labels[leaderworkersetv1.GroupIndexLabelKey])

	pod.Labels[constants.ManagedByKueueLabelKey] = constants.ManagedByKueueLabelValue
	pod.Labels[controllerconstants.QueueLabel] = queueName
	pod.Labels[podconstants.GroupNameLabel] = wlName
	pod.Labels[controllerconstants.PrebuiltWorkloadLabel] = wlName
	pod.Annotations[podconstants.GroupTotalCountAnnotation] = fmt.Sprint(ptr.Deref(lws.Spec.LeaderWorkerTemplate.Size, 1))
	pod.Annotations[podconstants.RoleHashAnnotation] = string(kueue.DefaultPodSetName)

	if lws.Spec.LeaderWorkerTemplate.LeaderTemplate != nil {
		if _, ok := pod.Annotations[leaderworkersetv1.LeaderPodNameAnnotationKey]; !ok {
			pod.Annotations[podconstants.RoleHashAnnotation] = leaderPodSetName
		} else {
			pod.Annotations[podconstants.RoleHashAnnotation] = workerPodSetName
		}
	}

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
	// Handle only leaderworkerset pods.
	return pod.Annotations[podconstants.SuspendedByParentAnnotation] == FrameworkName
}
