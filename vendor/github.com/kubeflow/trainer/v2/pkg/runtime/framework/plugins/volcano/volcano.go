/*
Copyright 2025 The Kubeflow Authors.

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

package volcano

import (
	"context"
	"fmt"
	"sync"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	schedulingv1 "k8s.io/api/scheduling/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apiruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation/field"
	metav1ac "k8s.io/client-go/applyconfigurations/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
	"sigs.k8s.io/jobset/client-go/applyconfiguration/jobset/v1alpha2"
	volcanov1beta1 "volcano.sh/apis/pkg/apis/scheduling/v1beta1"
	volcanov1beta1ac "volcano.sh/apis/pkg/client/applyconfiguration/scheduling/v1beta1"

	configapi "github.com/kubeflow/trainer/v2/pkg/apis/config/v1alpha1"
	trainer "github.com/kubeflow/trainer/v2/pkg/apis/trainer/v1alpha1"
	"github.com/kubeflow/trainer/v2/pkg/runtime"
	"github.com/kubeflow/trainer/v2/pkg/runtime/framework"
	"github.com/kubeflow/trainer/v2/pkg/util/trainjob"
)

type Volcano struct {
	client     client.Client
	restMapper meta.RESTMapper
	scheme     *apiruntime.Scheme
	logger     logr.Logger
	// podGroupCRDInstalled reports whether the Volcano PodGroup CRD is installed. It is
	// evaluated once, so the CRD must be installed before the controller starts, as for
	// the PodGroup watch in ReconcilerBuilders.
	podGroupCRDInstalled func() bool
}

var _ framework.EnforcePodGroupPolicyPlugin = (*Volcano)(nil)
var _ framework.ComponentBuilderPlugin = (*Volcano)(nil)
var _ framework.ComponentDeleterPlugin = (*Volcano)(nil)
var _ framework.WatchExtensionPlugin = (*Volcano)(nil)

const Name = "Volcano"

// +kubebuilder:rbac:groups=scheduling.volcano.sh,resources=podgroups,verbs=create;get;list;watch;update;patch;delete
// +kubebuilder:rbac:groups=scheduling.k8s.io,resources=priorityclasses,verbs=get;list;watch

func New(_ context.Context, client client.Client, _ client.FieldIndexer, _ *configapi.Configuration) (framework.Plugin, error) {
	v := &Volcano{
		client:     client,
		restMapper: client.RESTMapper(),
		scheme:     client.Scheme(),
	}
	v.podGroupCRDInstalled = sync.OnceValue(func() bool {
		_, err := v.restMapper.RESTMapping(
			schema.GroupKind{Group: volcanov1beta1.SchemeGroupVersion.Group, Kind: "PodGroup"},
			volcanov1beta1.SchemeGroupVersion.Version,
		)
		return err == nil
	})
	return v, nil
}

func (v *Volcano) Name() string {
	return Name
}

func (v *Volcano) Validate(ctx context.Context, info *runtime.Info, _, newObj *trainer.TrainJob) (admission.Warnings, field.ErrorList) {
	var allErrs field.ErrorList

	if info == nil || info.RuntimePolicy.PodGroupPolicy == nil || info.RuntimePolicy.PodGroupPolicy.Volcano == nil || newObj == nil {
		return nil, allErrs
	}

	specPath := field.NewPath("spec")

	// Validate queue (must not be empty if explicitly set)
	if queue, ok := info.Annotations[volcanov1beta1.QueueNameAnnotationKey]; ok {
		if queue == "" {
			allErrs = append(allErrs, field.Invalid(specPath.Child("annotations").Key(volcanov1beta1.QueueNameAnnotationKey),
				queue, "Volcano queue name must not be empty"))
		}
	}

	// Validate priorityClassName from the Pod template
	jobSetSpec, ok := runtime.TemplateSpecApply[v1alpha2.JobSetSpecApplyConfiguration](info)
	if ok && jobSetSpec != nil {
		for _, rj := range jobSetSpec.ReplicatedJobs {
			if rj.Template != nil && rj.Template.Spec != nil && rj.Template.Spec.Template != nil && rj.Template.Spec.Template.Spec != nil {
				priorityClassName := rj.Template.Spec.Template.Spec.PriorityClassName
				if priorityClassName != nil {
					pcName := *priorityClassName
					// Every name, including the built-in system-cluster-critical and
					// system-node-critical, must resolve to a PriorityClass object.
					var pc schedulingv1.PriorityClass
					if err := v.client.Get(ctx, types.NamespacedName{Name: pcName}, &pc); err != nil {
						allErrs = append(allErrs, field.Invalid(specPath.Child("templateSpec").Child("priorityClassName"),
							pcName, fmt.Sprintf("PriorityClass %q doesn't exist: %v", pcName, err)))
					}
				}
			}
		}
	}

	return nil, allErrs
}

func (v *Volcano) EnforcePodGroupPolicy(info *runtime.Info, trainJob *trainer.TrainJob) error {
	if info == nil || info.RuntimePolicy.PodGroupPolicy == nil || trainJob == nil || info.RuntimePolicy.PodGroupPolicy.Volcano == nil {
		return nil
	}
	if info.Scheduler.PodAnnotations == nil {
		info.Scheduler.PodAnnotations = map[string]string{}
	}
	info.Scheduler.PodAnnotations[volcanov1beta1.KubeGroupNameAnnotationKey] = trainJob.Name
	return nil
}

func (v *Volcano) Build(ctx context.Context, info *runtime.Info, trainJob *trainer.TrainJob) ([]apiruntime.ApplyConfiguration, error) {
	// Delete() removes the PodGroup while suspended, so do not recreate it here.
	if info == nil || info.RuntimePolicy.PodGroupPolicy == nil || info.RuntimePolicy.PodGroupPolicy.Volcano == nil || trainJob == nil || ptr.Deref(trainJob.Spec.Suspend, false) {
		return nil, nil
	}

	// Do not update an existing PodGroup. Changes made while suspended are picked up on resume,
	// since Delete() removes the PodGroup during suspension.
	oldPodGroup := &volcanov1beta1.PodGroup{}
	if err := v.client.Get(ctx, client.ObjectKeyFromObject(trainJob), oldPodGroup); err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, err
		}
		oldPodGroup = nil
	}
	if oldPodGroup != nil {
		return nil, nil
	}

	volcanoSpec := info.RuntimePolicy.PodGroupPolicy.Volcano

	// Aggregate pod resource requests
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
	pg := volcanov1beta1ac.PodGroup(trainJob.Name, trainJob.Namespace).
		WithSpec(volcanov1beta1ac.PodGroupSpec().
			WithMinMember(totalMembers).
			WithMinResources(totalResources))

	// Configure queue via annotations `scheduling.volcano.sh/queue-name`.
	// The field is initially set in TrainingRuntime, but can be overridden by the TrainJob.
	if queue, ok := info.Annotations[volcanov1beta1.QueueNameAnnotationKey]; ok {
		pg.Spec.WithQueue(queue)
	}

	// Configure priorityClassName from the Pod template
	jobSetSpec, ok := runtime.TemplateSpecApply[v1alpha2.JobSetSpecApplyConfiguration](info)
	if ok && jobSetSpec != nil {
		for _, rj := range jobSetSpec.ReplicatedJobs {
			if rj.Template != nil && rj.Template.Spec != nil && rj.Template.Spec.Template != nil && rj.Template.Spec.Template.Spec != nil {
				priorityClassName := rj.Template.Spec.Template.Spec.PriorityClassName
				if priorityClassName != nil {
					pg.Spec.WithPriorityClassName(*priorityClassName)
				}
			}
		}
	}

	if volcanoSpec.NetworkTopology != nil {
		pg.Spec.WithNetworkTopology(volcanov1beta1ac.NetworkTopologySpec().
			WithMode(volcanoSpec.NetworkTopology.Mode).
			WithHighestTierAllowed(*volcanoSpec.NetworkTopology.HighestTierAllowed))
	}

	pg.WithOwnerReferences(metav1ac.OwnerReference().
		WithAPIVersion(trainer.GroupVersion.String()).
		WithKind(trainer.TrainJobKind).
		WithName(trainJob.Name).
		WithUID(trainJob.UID).
		WithController(true).
		WithBlockOwnerDeletion(true))

	return []apiruntime.ApplyConfiguration{pg}, nil
}

// Delete returns the PodGroup that should now be removed for the TrainJob, once it is no
// longer running: either because it reached a Failed or Complete condition, or because it
// is currently suspended. A suspended TrainJob runs no Pods, so its PodGroup would
// otherwise keep reserving `minResources` capacity in the Volcano queue indefinitely.
func (v *Volcano) Delete(ctx context.Context, trainJob *trainer.TrainJob) ([]client.Object, error) {
	// Skip clusters without Volcano, so finished or suspended TrainJobs cost no API call there.
	if trainJob == nil || !shouldDeletePodGroup(trainJob) || !v.podGroupCRDInstalled() {
		return nil, nil
	}

	podGroup := &volcanov1beta1.PodGroup{}
	if err := v.client.Get(ctx, client.ObjectKeyFromObject(trainJob), podGroup); err != nil {
		// Tolerate a missing Volcano CRD, which fails client-side with NoMatch rather than NotFound.
		if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
			return nil, nil
		}
		return nil, err
	}
	if !metav1.IsControlledBy(podGroup, trainJob) {
		return nil, nil
	}
	return []client.Object{podGroup}, nil
}

// shouldDeletePodGroup reports whether a TrainJob's PodGroup should be removed: once the
// TrainJob is Failed, Complete, or suspended, no Pods are scheduled to run, so its
// PodGroup no longer needs to hold gang-scheduling capacity in the Volcano queue.
func shouldDeletePodGroup(trainJob *trainer.TrainJob) bool {
	return trainjob.IsTrainJobFinished(trainJob) || ptr.Deref(trainJob.Spec.Suspend, false)
}

// ReconcilerBuilders registers a watch on PodGroup so the TrainJob is requeued whenever
// its owned PodGroup changes.
func (v *Volcano) ReconcilerBuilders() []runtime.ReconcilerBuilder {
	if _, err := v.restMapper.RESTMapping(
		schema.GroupKind{Group: volcanov1beta1.SchemeGroupVersion.Group, Kind: "PodGroup"},
		volcanov1beta1.SchemeGroupVersion.Version,
	); err != nil {
		v.logger.Error(err, "PodGroup CRDs must be installed in advance")
		return nil
	}
	return []runtime.ReconcilerBuilder{
		func(b *builder.Builder, cl client.Client, cache cache.Cache) *builder.Builder {
			return b.Watches(
				&volcanov1beta1.PodGroup{},
				handler.EnqueueRequestForOwner(
					v.client.Scheme(), v.client.RESTMapper(), &trainer.TrainJob{}, handler.OnlyControllerOwner(),
				),
			)
		},
	}
}
