/*
Copyright 2024 The Kubeflow Authors.

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

package coscheduling

import (
	"context"
	"slices"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	apiruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	metav1ac "k8s.io/client-go/applyconfigurations/meta/v1"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
	schedulerpluginsv1alpha1 "sigs.k8s.io/scheduler-plugins/apis/scheduling/v1alpha1"
	schedulerpluginsv1alpha1ac "sigs.k8s.io/scheduler-plugins/pkg/generated/applyconfiguration/scheduling/v1alpha1"

	trainer "github.com/kubeflow/trainer/v2/pkg/apis/trainer/v1alpha1"
	"github.com/kubeflow/trainer/v2/pkg/runtime"
	"github.com/kubeflow/trainer/v2/pkg/runtime/framework"
	index "github.com/kubeflow/trainer/v2/pkg/runtime/indexer"
	runtimeindexer "github.com/kubeflow/trainer/v2/pkg/runtime/indexer"
)

type CoScheduling struct {
	client     client.Client
	restMapper meta.RESTMapper
	scheme     *apiruntime.Scheme
	logger     logr.Logger
}

var _ framework.EnforcePodGroupPolicyPlugin = (*CoScheduling)(nil)
var _ framework.WatchExtensionPlugin = (*CoScheduling)(nil)
var _ framework.ComponentBuilderPlugin = (*CoScheduling)(nil)

const Name = "CoScheduling"

// +kubebuilder:rbac:groups=scheduling.x-k8s.io,resources=podgroups,verbs=create;get;list;watch;update;patch
// +kubebuilder:rbac:groups=node.k8s.io,resources=runtimeclasses,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=limitranges,verbs=get;list;watch

func New(_ context.Context, client client.Client, _ client.FieldIndexer) (framework.Plugin, error) {
	return &CoScheduling{
		client:     client,
		restMapper: client.RESTMapper(),
		scheme:     client.Scheme(),
	}, nil
}

func (c *CoScheduling) Name() string {
	return Name
}

func (c *CoScheduling) EnforcePodGroupPolicy(info *runtime.Info, trainJob *trainer.TrainJob) error {
	if info == nil || info.RuntimePolicy.PodGroupPolicy == nil || info.RuntimePolicy.PodGroupPolicy.Coscheduling == nil || trainJob == nil {
		return nil
	}

	if info.Scheduler.PodLabels == nil {
		info.Scheduler.PodLabels = make(map[string]string, 1)
	}
	info.Scheduler.PodLabels[schedulerpluginsv1alpha1.PodGroupLabel] = trainJob.Name
	return nil
}

func (c *CoScheduling) Build(ctx context.Context, info *runtime.Info, trainJob *trainer.TrainJob) ([]apiruntime.ApplyConfiguration, error) {
	if info == nil || info.RuntimePolicy.PodGroupPolicy == nil || info.RuntimePolicy.PodGroupPolicy.Coscheduling == nil || trainJob == nil {
		return nil, nil
	}

	// Do not update the PodGroup if it already exists and the TrainJob is not suspended
	oldPodGroup := &schedulerpluginsv1alpha1.PodGroup{}
	if err := c.client.Get(ctx, client.ObjectKeyFromObject(trainJob), oldPodGroup); err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, err
		}
		oldPodGroup = nil
	}
	if oldPodGroup != nil && !ptr.Deref(trainJob.Spec.Suspend, false) {
		return nil, nil
	}

	var totalMembers int32
	totalResources := make(corev1.ResourceList)
	for _, ps := range info.TemplateSpec.PodSets {
		count := *ps.Count
		totalMembers += count
		for resName, quantity := range ps.SinglePodRequests {
			quantity.Mul(int64(count))
			current := totalResources[resName]
			current.Add(quantity)
			totalResources[resName] = current
		}
	}

	podGroup := schedulerpluginsv1alpha1ac.PodGroup(trainJob.Name, trainJob.Namespace)

	podGroup.WithSpec(schedulerpluginsv1alpha1ac.PodGroupSpec().
		WithMinMember(totalMembers).
		WithMinResources(totalResources).
		WithScheduleTimeoutSeconds(*info.RuntimePolicy.PodGroupPolicy.Coscheduling.ScheduleTimeoutSeconds))

	podGroup.WithOwnerReferences(metav1ac.OwnerReference().
		WithAPIVersion(trainer.GroupVersion.String()).
		WithKind(trainer.TrainJobKind).
		WithName(trainJob.Name).
		WithUID(trainJob.UID).
		WithController(true).
		WithBlockOwnerDeletion(true))

	return []apiruntime.ApplyConfiguration{podGroup}, nil
}

type PodGroupRuntimeClassHandler struct {
	client client.Client
}

var _ handler.TypedEventHandler[*nodev1.RuntimeClass, reconcile.Request] = (*PodGroupRuntimeClassHandler)(nil)

func (h *PodGroupRuntimeClassHandler) Create(ctx context.Context, e event.TypedCreateEvent[*nodev1.RuntimeClass], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	containerRuntimeClass := e.Object
	log := ctrl.LoggerFrom(ctx).WithValues("runtimeClass", klog.KObj(containerRuntimeClass))
	if err := h.queueSuspendedTrainJobs(ctx, containerRuntimeClass, q); err != nil {
		log.Error(err, "could not queue suspended TrainJob to reconcile queue")
	}
}

func (h *PodGroupRuntimeClassHandler) Update(ctx context.Context, e event.TypedUpdateEvent[*nodev1.RuntimeClass], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	newContainerRuntimeClass := e.ObjectNew
	log := ctrl.LoggerFrom(ctx).WithValues("runtimeClass", klog.KObj(newContainerRuntimeClass))
	if err := h.queueSuspendedTrainJobs(ctx, newContainerRuntimeClass, q); err != nil {
		log.Error(err, "could not queue suspended TrainJob to reconcile queue")
	}
}

func (h *PodGroupRuntimeClassHandler) Delete(ctx context.Context, e event.TypedDeleteEvent[*nodev1.RuntimeClass], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	containerRuntimeClass := e.Object
	log := ctrl.LoggerFrom(ctx).WithValues("runtimeClass", klog.KObj(containerRuntimeClass))
	if err := h.queueSuspendedTrainJobs(ctx, containerRuntimeClass, q); err != nil {
		log.Error(err, "could not queue suspended TrainJob to reconcile queue")
	}
}

func (h *PodGroupRuntimeClassHandler) Generic(context.Context, event.TypedGenericEvent[*nodev1.RuntimeClass], workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

func (h *PodGroupRuntimeClassHandler) queueSuspendedTrainJobs(ctx context.Context, runtimeClass *nodev1.RuntimeClass, q workqueue.TypedRateLimitingInterface[reconcile.Request]) error {
	var trainingRuntimes trainer.TrainingRuntimeList
	if err := h.client.List(ctx, &trainingRuntimes, client.MatchingFields{index.TrainingRuntimeContainerRuntimeClassKey: runtimeClass.Name}); err != nil {
		return err
	}
	var clusterTrainingRuntimes trainer.ClusterTrainingRuntimeList
	if err := h.client.List(ctx, &clusterTrainingRuntimes, client.MatchingFields{index.ClusterTrainingRuntimeContainerRuntimeClassKey: runtimeClass.Name}); err != nil {
		return err
	}

	var trainJobs []trainer.TrainJob
	for _, trainingRuntime := range trainingRuntimes.Items {
		var trainJobsWithTrainingRuntime trainer.TrainJobList
		err := h.client.List(ctx, &trainJobsWithTrainingRuntime, client.MatchingFields{runtimeindexer.TrainJobRuntimeRefKey: trainingRuntime.Name})
		if err != nil {
			return err
		}
		trainJobs = append(trainJobs, trainJobsWithTrainingRuntime.Items...)
	}
	for _, clusterTrainingRuntime := range clusterTrainingRuntimes.Items {
		var trainJobsWithClTrainingRuntime trainer.TrainJobList
		err := h.client.List(ctx, &trainJobsWithClTrainingRuntime, client.MatchingFields{runtimeindexer.TrainJobClusterRuntimeRefKey: clusterTrainingRuntime.Name})
		if err != nil {
			return err
		}
		trainJobs = append(trainJobs, trainJobsWithClTrainingRuntime.Items...)
	}
	trainJobs = slices.CompactFunc(trainJobs, func(a, b trainer.TrainJob) bool {
		return a.Name == b.Name
	})
	for _, trainJob := range trainJobs {
		if ptr.Deref(trainJob.Spec.Suspend, false) {
			q.Add(reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&trainJob)})
		}
	}
	return nil
}

type PodGroupLimitRangeHandler struct {
	client client.Client
}

var _ handler.TypedEventHandler[*corev1.LimitRange, reconcile.Request] = (*PodGroupLimitRangeHandler)(nil)

func (h *PodGroupLimitRangeHandler) Create(ctx context.Context, e event.TypedCreateEvent[*corev1.LimitRange], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	limitRange := e.Object
	log := ctrl.LoggerFrom(ctx).WithValues("limitRange", klog.KObj(limitRange))
	if err := h.queueSuspendedTrainJob(ctx, limitRange.Namespace, q); err != nil {
		log.Error(err, "could not queue suspended TrainJob to reconcile queue")
	}
}

func (h *PodGroupLimitRangeHandler) Update(ctx context.Context, e event.TypedUpdateEvent[*corev1.LimitRange], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	newLimitRange := e.ObjectNew
	log := ctrl.LoggerFrom(ctx).WithValues("limitRange", klog.KObj(newLimitRange))
	if err := h.queueSuspendedTrainJob(ctx, newLimitRange.Namespace, q); err != nil {
		log.Error(err, "could not queue suspended TrainJob to reconcile queue")
	}
}

func (h *PodGroupLimitRangeHandler) Delete(ctx context.Context, e event.TypedDeleteEvent[*corev1.LimitRange], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	limitRange := e.Object
	log := ctrl.LoggerFrom(ctx).WithValues("limitRange", klog.KObj(limitRange))
	if err := h.queueSuspendedTrainJob(ctx, limitRange.Namespace, q); err != nil {
		log.Error(err, "could not queue suspended TrainJob to reconcile queue")
	}
}

func (h *PodGroupLimitRangeHandler) Generic(context.Context, event.TypedGenericEvent[*corev1.LimitRange], workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

func (h *PodGroupLimitRangeHandler) queueSuspendedTrainJob(ctx context.Context, ns string, q workqueue.TypedRateLimitingInterface[reconcile.Request]) error {
	var trainJobs trainer.TrainJobList
	if err := h.client.List(ctx, &trainJobs, client.InNamespace(ns)); err != nil {
		return err
	}
	for _, trainJob := range trainJobs.Items {
		if ptr.Deref(trainJob.Spec.Suspend, false) {
			q.Add(reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&trainJob)})
		}
	}
	return nil
}

func (c *CoScheduling) ReconcilerBuilders() []runtime.ReconcilerBuilder {
	if _, err := c.restMapper.RESTMapping(
		schema.GroupKind{Group: schedulerpluginsv1alpha1.SchemeGroupVersion.Group, Kind: "PodGroup"},
		schedulerpluginsv1alpha1.SchemeGroupVersion.Version,
	); err != nil {
		c.logger.Error(err, "PodGroup CRDs must be installed in advance")
		return nil
	}
	return []runtime.ReconcilerBuilder{
		func(b *builder.Builder, cl client.Client, cache cache.Cache) *builder.Builder {
			return b.Watches(
				&schedulerpluginsv1alpha1.PodGroup{},
				handler.EnqueueRequestForOwner(
					c.client.Scheme(), c.client.RESTMapper(), &trainer.TrainJob{}, handler.OnlyControllerOwner(),
				),
			)
		},
		func(b *builder.Builder, cl client.Client, cache cache.Cache) *builder.Builder {
			return b.WatchesRawSource(source.TypedKind[*corev1.LimitRange, reconcile.Request](cache, &corev1.LimitRange{}, &PodGroupLimitRangeHandler{
				client: cl,
			}))
		},
		func(b *builder.Builder, cl client.Client, cache cache.Cache) *builder.Builder {
			return b.WatchesRawSource(source.TypedKind[*nodev1.RuntimeClass, reconcile.Request](cache, &nodev1.RuntimeClass{}, &PodGroupRuntimeClassHandler{
				client: cl,
			}))
		},
	}
}
