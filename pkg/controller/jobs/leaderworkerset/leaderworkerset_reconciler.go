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
	"strconv"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	leaderworkersetv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	podcontroller "sigs.k8s.io/kueue/pkg/controller/jobs/pod"
	"sigs.k8s.io/kueue/pkg/workload"
)

const (
	leaderPodSetName = "leader"
	workerPodSetName = "worker"
)

type Reconciler struct {
	client          client.Client
	record          record.EventRecorder
	labelKeysToCopy []string
}

func NewReconciler(client client.Client, eventRecorder record.EventRecorder, opts ...jobframework.Option) jobframework.JobReconcilerInterface {
	options := jobframework.ProcessOptions(opts...)

	return &Reconciler{
		client:          client,
		record:          eventRecorder,
		labelKeysToCopy: options.LabelKeysToCopy,
	}
}

var _ jobframework.JobReconcilerInterface = (*Reconciler)(nil)

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	ctrl.Log.V(3).Info("Setting up LeaderWorkerSet reconciler")

	return ctrl.NewControllerManagedBy(mgr).
		For(&leaderworkersetv1.LeaderWorkerSet{}).
		Named("leaderworkerset").
		WithEventFilter(r).
		Complete(r)
}

// +kubebuilder:rbac:groups=leaderworkerset.x-k8s.io,resources=leaderworkersets,verbs=get;list;watch

func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	lws := &leaderworkersetv1.LeaderWorkerSet{}
	err := r.client.Get(ctx, req.NamespacedName, lws)
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	log := ctrl.LoggerFrom(ctx)
	log.V(2).Info("Reconcile LeaderWorkerSet")

	err = r.createPrebuiltWorkloadsIfNotExist(ctx, lws)
	if err != nil {
		return ctrl.Result{}, err
	}

	replicas := ptr.Deref(lws.Spec.Replicas, 1)
	err = r.cleanupExcessWorkloads(ctx, lws, replicas)
	if err != nil {
		return ctrl.Result{}, err
	}

	if lws.DeletionTimestamp != nil {
		var workloads kueue.WorkloadList
		if err := r.client.List(ctx, &workloads, client.InNamespace(lws.Namespace), client.MatchingLabels{leaderworkersetv1.SetNameLabelKey: lws.Name}); err != nil {
			return ctrl.Result{}, err
		}
		for _, wl := range workloads.Items {
			if wl.DeletionTimestamp != nil {
				continue
			}
			if err := r.client.Delete(ctx, &wl); err != nil && client.IgnoreNotFound(err) != nil {
				return ctrl.Result{}, err
			}
			r.record.Eventf(lws, corev1.EventTypeNormal, "DeletedWorkload", "Deleted Workload: %v", workload.Key(&wl))
		}
		return ctrl.Result{}, nil
	}

	return ctrl.Result{}, nil
}

// cleanupExcessWorkloads deletes excess Workloads (replicas > replicas) to scale down.
func (r *Reconciler) cleanupExcessWorkloads(ctx context.Context, lws *leaderworkersetv1.LeaderWorkerSet, replicas int32) error {
	var workloads kueue.WorkloadList
	if err := r.client.List(ctx, &workloads, client.InNamespace(lws.Namespace), client.MatchingLabels{"leaderworkerset/leader-name": lws.Name}); err != nil {
		return err
	}

	for _, wl := range workloads.Items {
		indexStr, ok := wl.Labels["leaderworkerset/group-index"]
		if !ok {
			continue
		}
		index, err := strconv.Atoi(indexStr)
		if err != nil {
			continue
		}
		if int32(index) >= replicas {
			if err := r.client.Delete(ctx, &wl); err != nil && client.IgnoreNotFound(err) != nil {
				return err
			}
		}
	}
	return nil
}

func (r *Reconciler) createPrebuiltWorkloadsIfNotExist(ctx context.Context, lws *leaderworkersetv1.LeaderWorkerSet) error {
	replicas := ptr.Deref(lws.Spec.Replicas, 1)
	for i := int32(0); i < replicas; i++ {
		if err := r.createPrebuiltWorkloadIfNotExist(ctx, lws, i); err != nil {
			return err
		}
	}
	return nil
}

func (r *Reconciler) createPrebuiltWorkloadIfNotExist(ctx context.Context, lws *leaderworkersetv1.LeaderWorkerSet, index int32) error {
	wl := &kueue.Workload{}
	err := r.client.Get(ctx, client.ObjectKey{Name: GetWorkloadName(lws.UID, lws.Name, fmt.Sprint(index)), Namespace: lws.Namespace}, wl)
	// Ignore if the Workload already exists or an error occurs.
	if err == nil || client.IgnoreNotFound(err) != nil {
		return err
	}
	return r.createPrebuiltWorkload(ctx, lws, index)
}

func (r *Reconciler) createPrebuiltWorkload(ctx context.Context, lws *leaderworkersetv1.LeaderWorkerSet, index int32) error {
	createdWorkload := r.constructWorkload(lws, index)

	priorityClassName, source, p, err := jobframework.ExtractPriority(ctx, r.client, lws, createdWorkload.Spec.PodSets, nil)
	if err != nil {
		return err
	}

	createdWorkload.Spec.PriorityClassName = priorityClassName
	createdWorkload.Spec.Priority = &p
	createdWorkload.Spec.PriorityClassSource = source

	err = r.client.Create(ctx, createdWorkload)
	if err != nil {
		return err
	}
	r.record.Eventf(
		lws, corev1.EventTypeNormal, jobframework.ReasonCreatedWorkload,
		"Created Workload: %v", workload.Key(createdWorkload),
	)
	return nil
}

func (r *Reconciler) constructWorkload(lws *leaderworkersetv1.LeaderWorkerSet, index int32) *kueue.Workload {
	wl := podcontroller.NewGroupWorkload(GetWorkloadName(lws.UID, lws.Name, fmt.Sprint(index)), lws, r.podSets(lws), r.labelKeysToCopy)

	if wl.Labels == nil {
		wl.Labels = make(map[string]string)
	}

	wl.Labels["leaderworkerset/leader-name"] = lws.Name
	wl.Labels["leaderworkerset/group-index"] = fmt.Sprint(index)

	// Set owner reference for garbage collection else it wont be deleted when the LeaderWorkerSet is deleted. causing e2e tests to fail.
	wl.OwnerReferences = []metav1.OwnerReference{
		{
			APIVersion: gvk.GroupVersion().String(),
			Kind:       gvk.Kind,
			Name:       lws.Name,
			UID:        lws.UID,
			Controller: ptr.To(true),
		},
	}
	return wl
}

func (r *Reconciler) podSets(lws *leaderworkersetv1.LeaderWorkerSet) []kueue.PodSet {
	podSets := make([]kueue.PodSet, 0, 2)

	if lws.Spec.LeaderWorkerTemplate.LeaderTemplate != nil {
		podSets = append(podSets, kueue.PodSet{
			Name:  leaderPodSetName,
			Count: 1,
			Template: corev1.PodTemplateSpec{
				Spec: *lws.Spec.LeaderWorkerTemplate.LeaderTemplate.Spec.DeepCopy(),
			},
			TopologyRequest: jobframework.PodSetTopologyRequest(
				&lws.Spec.LeaderWorkerTemplate.LeaderTemplate.ObjectMeta,
				nil,
				nil,
				nil,
			),
		})
	}

	defaultPodSetName := kueue.DefaultPodSetName
	if len(podSets) > 0 {
		defaultPodSetName = workerPodSetName
	}

	defaultPodSetCount := ptr.Deref(lws.Spec.LeaderWorkerTemplate.Size, 1)
	if len(podSets) > 0 {
		defaultPodSetCount--
	}

	podSets = append(podSets, kueue.PodSet{
		Name:  defaultPodSetName,
		Count: defaultPodSetCount,
		Template: corev1.PodTemplateSpec{
			Spec: *lws.Spec.LeaderWorkerTemplate.WorkerTemplate.Spec.DeepCopy(),
		},
		TopologyRequest: jobframework.PodSetTopologyRequest(
			&lws.Spec.LeaderWorkerTemplate.WorkerTemplate.ObjectMeta,
			ptr.To(leaderworkersetv1.WorkerIndexLabelKey),
			nil,
			nil,
		),
	})

	return podSets
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
	lws, isLws := obj.(*leaderworkersetv1.LeaderWorkerSet)
	if !isLws {
		return false
	}
	// Handle only leaderworkerset managed by kueue.
	return jobframework.IsManagedByKueue(lws)
}
