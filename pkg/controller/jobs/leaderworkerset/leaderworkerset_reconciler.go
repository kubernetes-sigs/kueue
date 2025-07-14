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
	"maps"
	"slices"

	"github.com/go-logr/logr"
	"golang.org/x/sync/errgroup"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
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
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	podcontroller "sigs.k8s.io/kueue/pkg/controller/jobs/pod"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/parallelize"
	utilslices "sigs.k8s.io/kueue/pkg/util/slices"
	"sigs.k8s.io/kueue/pkg/workload"
)

const (
	leaderPodSetName = "leader"
	workerPodSetName = "worker"
)

type Reconciler struct {
	client                       client.Client
	log                          logr.Logger
	record                       record.EventRecorder
	labelKeysToCopy              []string
	manageJobsWithoutQueueName   bool
	managedJobsNamespaceSelector labels.Selector
}

func NewReconciler(client client.Client, eventRecorder record.EventRecorder, opts ...jobframework.Option) jobframework.JobReconcilerInterface {
	options := jobframework.ProcessOptions(opts...)

	return &Reconciler{
		client:                       client,
		log:                          ctrl.Log.WithName("leaderworkerset-reconciler"),
		record:                       eventRecorder,
		labelKeysToCopy:              options.LabelKeysToCopy,
		manageJobsWithoutQueueName:   options.ManageJobsWithoutQueueName,
		managedJobsNamespaceSelector: options.ManagedJobsNamespaceSelector,
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
		// we'll ignore not-found errors, since there is nothing to do.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	log := ctrl.LoggerFrom(ctx)
	log.V(2).Info("Reconcile LeaderWorkerSet")

	wlList := &kueue.WorkloadList{}
	if err := r.client.List(ctx, wlList, client.InNamespace(lws.GetNamespace()),
		client.MatchingFields{indexer.OwnerReferenceUID: string(lws.GetUID())},
	); err != nil {
		return ctrl.Result{}, err
	}

	toCreate, toFinalize := r.filterWorkloads(lws, wlList.Items)

	eg, ctx := errgroup.WithContext(ctx)

	eg.Go(func() error {
		return parallelize.Until(ctx, len(toCreate), func(i int) error {
			return r.createPrebuiltWorkload(ctx, lws, toCreate[i])
		})
	})

	eg.Go(func() error {
		return parallelize.Until(ctx, len(toFinalize), func(i int) error {
			return r.removeOwnerReference(ctx, lws, toFinalize[i])
		})
	})

	err = eg.Wait()
	if err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// filterWorkloads compares the desired state in a LeaderWorkerSet with existing workloads,
// identifying workloads to create and those to finalize.
//
// It takes a LeaderWorkerSet and a slice of existing Workload objects as input and returns:
// 1. A slice of workload names that need to be created
// 2. A slice of Workload pointers that need to be finalized
func (r *Reconciler) filterWorkloads(lws *leaderworkersetv1.LeaderWorkerSet, existingWorkloads []kueue.Workload) ([]string, []*kueue.Workload) {
	var (
		toCreate   []string
		toFinalize = utilslices.ToRefMap(existingWorkloads, func(e *kueue.Workload) string {
			return e.Name
		})
		replicas = ptr.Deref(lws.Spec.Replicas, 1)
	)

	for i := range replicas {
		workloadName := GetWorkloadName(lws.UID, lws.Name, fmt.Sprint(i))
		if _, ok := toFinalize[workloadName]; ok {
			delete(toFinalize, workloadName)
		} else {
			toCreate = append(toCreate, workloadName)
		}
	}

	return toCreate, slices.Collect(maps.Values(toFinalize))
}

func (r *Reconciler) createPrebuiltWorkload(ctx context.Context, lws *leaderworkersetv1.LeaderWorkerSet, workloadName string) error {
	createdWorkload, err := r.constructWorkload(lws, workloadName)
	if err != nil {
		return err
	}

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

func (r *Reconciler) constructWorkload(lws *leaderworkersetv1.LeaderWorkerSet, workloadName string) (*kueue.Workload, error) {
	podSets, err := podSets(lws)
	if err != nil {
		return nil, err
	}
	createdWorkload := podcontroller.NewGroupWorkload(workloadName, lws, podSets, r.labelKeysToCopy)
	if err := controllerutil.SetOwnerReference(lws, createdWorkload, r.client.Scheme()); err != nil {
		return nil, err
	}
	return createdWorkload, nil
}

func podSets(lws *leaderworkersetv1.LeaderWorkerSet) ([]kueue.PodSet, error) {
	podSets := make([]kueue.PodSet, 0, 2)

	if lws.Spec.LeaderWorkerTemplate.LeaderTemplate != nil {
		podSet := kueue.PodSet{
			Name:  leaderPodSetName,
			Count: 1,
			Template: corev1.PodTemplateSpec{
				Spec: *lws.Spec.LeaderWorkerTemplate.LeaderTemplate.Spec.DeepCopy(),
			},
		}
		if features.Enabled(features.TopologyAwareScheduling) {
			topologyRequest, err := jobframework.NewPodSetTopologyRequest(
				&lws.Spec.LeaderWorkerTemplate.LeaderTemplate.ObjectMeta).Build()
			if err != nil {
				return nil, err
			}
			podSet.TopologyRequest = topologyRequest
		}
		podSets = append(podSets, podSet)
	}

	defaultPodSetName := kueue.DefaultPodSetName
	if len(podSets) > 0 {
		defaultPodSetName = workerPodSetName
	}

	defaultPodSetCount := ptr.Deref(lws.Spec.LeaderWorkerTemplate.Size, 1)
	if len(podSets) > 0 {
		defaultPodSetCount--
	}

	podSet := kueue.PodSet{
		Name:  defaultPodSetName,
		Count: defaultPodSetCount,
		Template: corev1.PodTemplateSpec{
			Spec: *lws.Spec.LeaderWorkerTemplate.WorkerTemplate.Spec.DeepCopy(),
		},
	}

	if features.Enabled(features.TopologyAwareScheduling) {
		topologyRequest, err := jobframework.NewPodSetTopologyRequest(
			&lws.Spec.LeaderWorkerTemplate.WorkerTemplate.ObjectMeta).PodIndexLabel(
			ptr.To(leaderworkersetv1.WorkerIndexLabelKey)).Build()
		if err != nil {
			return nil, err
		}
		podSet.TopologyRequest = topologyRequest
	}

	podSets = append(podSets, podSet)

	return podSets, nil
}

func (r *Reconciler) removeOwnerReference(ctx context.Context, lws *leaderworkersetv1.LeaderWorkerSet, wl *kueue.Workload) error {
	err := controllerutil.RemoveOwnerReference(lws, wl, r.client.Scheme())
	if err != nil {
		return err
	}
	return r.client.Update(ctx, wl)
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

func (r *Reconciler) Delete(event.DeleteEvent) bool {
	return false
}

func (r *Reconciler) handle(obj client.Object) bool {
	lws, isLws := obj.(*leaderworkersetv1.LeaderWorkerSet)
	if !isLws {
		return false
	}

	ctx := context.Background()
	log := r.log.WithValues("leaderworkerset", klog.KObj(lws))
	ctrl.LoggerInto(ctx, log)

	// Handle only leaderworkerset managed by kueue.
	suspend, err := jobframework.WorkloadShouldBeSuspended(ctx, lws, r.client, r.manageJobsWithoutQueueName, r.managedJobsNamespaceSelector)
	if err != nil {
		log.Error(err, "Failed to determine if the LeaderWorkerSet should be managed by Kueue")
	}

	return suspend
}
