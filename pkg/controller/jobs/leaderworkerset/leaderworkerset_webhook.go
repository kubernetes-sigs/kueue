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
	apivalidation "k8s.io/apimachinery/pkg/api/validation"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
	leaderworkersetv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	kueuebeta "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/queue"
	"sigs.k8s.io/kueue/pkg/util/podset"
)

type Webhook struct {
	client                       client.Client
	manageJobsWithoutQueueName   bool
	managedJobsNamespaceSelector labels.Selector
	queues                       *queue.Manager
}

func SetupWebhook(mgr ctrl.Manager, opts ...jobframework.Option) error {
	options := jobframework.ProcessOptions(opts...)
	wh := &Webhook{
		client:                       mgr.GetClient(),
		manageJobsWithoutQueueName:   options.ManageJobsWithoutQueueName,
		managedJobsNamespaceSelector: options.ManagedJobsNamespaceSelector,
		queues:                       options.Queues,
	}
	return ctrl.NewWebhookManagedBy(mgr).
		For(&leaderworkersetv1.LeaderWorkerSet{}).
		WithDefaulter(wh).
		WithValidator(wh).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-leaderworkerset-x-k8s-io-v1-leaderworkerset,mutating=true,failurePolicy=fail,sideEffects=None,groups="leaderworkerset.x-k8s.io",resources=leaderworkersets,verbs=create;update,versions=v1,name=mleaderworkerset.kb.io,admissionReviewVersions=v1

var _ webhook.CustomDefaulter = &Webhook{}

func (wh *Webhook) Default(ctx context.Context, obj runtime.Object) error {
	lws := fromObject(obj)
	log := ctrl.LoggerFrom(ctx).WithName("leaderworkerset-webhook")
	log.V(5).Info("Applying defaults")

	jobframework.ApplyDefaultLocalQueue(lws.Object(), wh.queues.DefaultLocalQueueExist)
	suspend, err := jobframework.WorkloadShouldBeSuspended(ctx, lws.Object(), wh.client, wh.manageJobsWithoutQueueName, wh.managedJobsNamespaceSelector)
	if err != nil {
		return err
	}
	if suspend {
		if lws.Spec.LeaderWorkerTemplate.LeaderTemplate != nil {
			wh.podTemplateSpecDefault(lws, lws.Spec.LeaderWorkerTemplate.LeaderTemplate)
		}
		wh.podTemplateSpecDefault(lws, &lws.Spec.LeaderWorkerTemplate.WorkerTemplate)
	}

	return nil
}

func (wh *Webhook) podTemplateSpecDefault(lws *LeaderWorkerSet, podTemplateSpec *corev1.PodTemplateSpec) {
	if priorityClass := jobframework.WorkloadPriorityClassName(lws.Object()); priorityClass != "" {
		if podTemplateSpec.Labels == nil {
			podTemplateSpec.Labels = make(map[string]string, 1)
		}
		podTemplateSpec.Labels[constants.WorkloadPriorityClassLabel] = priorityClass
	}

	if podTemplateSpec.Annotations == nil {
		podTemplateSpec.Annotations = make(map[string]string, 2)
	}
	podTemplateSpec.Annotations[podconstants.SuspendedByParentAnnotation] = FrameworkName
	podTemplateSpec.Annotations[podconstants.GroupServingAnnotationKey] = podconstants.GroupServingAnnotationValue
}

// +kubebuilder:webhook:path=/validate-leaderworkerset-x-k8s-io-v1-leaderworkerset,mutating=false,failurePolicy=fail,sideEffects=None,groups="leaderworkerset.x-k8s.io",resources=leaderworkersets,verbs=create;update,versions=v1,name=vleaderworkerset.kb.io,admissionReviewVersions=v1

var _ webhook.CustomValidator = &Webhook{}

var (
	labelsPath               = field.NewPath("metadata", "labels")
	queueNameLabelPath       = labelsPath.Key(constants.QueueLabel)
	specPath                 = field.NewPath("spec")
	leaderWorkerTemplatePath = specPath.Child("leaderWorkerTemplate")
	leaderTemplatePath       = leaderWorkerTemplatePath.Child("leaderTemplate")
	leaderTemplateMetaPath   = leaderTemplatePath.Child("metadata")
	workerTemplatePath       = leaderWorkerTemplatePath.Child("workerTemplate")
	workerTemplateMetaPath   = workerTemplatePath.Child("metadata")
)

func (wh *Webhook) ValidateCreate(ctx context.Context, obj runtime.Object) (warnings admission.Warnings, err error) {
	lws := fromObject(obj)

	log := ctrl.LoggerFrom(ctx).WithName("leaderworkerset-webhook")
	log.V(5).Info("Validating create")

	validationErrs, err := validateCreate(lws)
	if err != nil {
		return nil, err
	}

	return nil, validationErrs.ToAggregate()
}

func (wh *Webhook) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (warnings admission.Warnings, err error) {
	oldLeaderWorkerSet := fromObject(oldObj)
	newLeaderWorkerSet := fromObject(newObj)

	log := ctrl.LoggerFrom(ctx).WithName("leaderworkerset-webhook")
	log.V(5).Info("Validating update")

	allErrs, err := validateCreate(newLeaderWorkerSet)
	if err != nil {
		return nil, err
	}

	allErrs = append(allErrs, apivalidation.ValidateImmutableField(
		jobframework.QueueNameForObject(newLeaderWorkerSet.Object()),
		jobframework.QueueNameForObject(oldLeaderWorkerSet.Object()),
		queueNameLabelPath,
	)...)

	isSuspended := oldLeaderWorkerSet.Status.ReadyReplicas == 0
	if !isSuspended || jobframework.IsWorkloadPriorityClassNameEmpty(newLeaderWorkerSet.Object()) {
		allErrs = append(allErrs, jobframework.ValidateUpdateForWorkloadPriorityClassName(
			newLeaderWorkerSet.Object(),
			oldLeaderWorkerSet.Object(),
		)...)
	}

	suspend, err := jobframework.WorkloadShouldBeSuspended(ctx, newLeaderWorkerSet.Object(), wh.client, wh.manageJobsWithoutQueueName, wh.managedJobsNamespaceSelector)
	if err != nil {
		return nil, err
	}
	if suspend {
		allErrs = append(allErrs, validateImmutablePodTemplateSpec(
			newLeaderWorkerSet.Spec.LeaderWorkerTemplate.LeaderTemplate,
			oldLeaderWorkerSet.Spec.LeaderWorkerTemplate.LeaderTemplate,
			leaderTemplatePath,
		)...)
		allErrs = append(allErrs, validateImmutablePodTemplateSpec(
			&newLeaderWorkerSet.Spec.LeaderWorkerTemplate.WorkerTemplate,
			&oldLeaderWorkerSet.Spec.LeaderWorkerTemplate.WorkerTemplate,
			workerTemplatePath,
		)...)
	}

	return warnings, allErrs.ToAggregate()
}

func (wh *Webhook) ValidateDelete(context.Context, runtime.Object) (warnings admission.Warnings, err error) {
	return nil, nil
}

func GetWorkloadName(uid types.UID, name string, groupIndex string) string {
	// Workload name should be unique by group index.
	return jobframework.GetWorkloadNameForOwnerWithGVK(fmt.Sprintf("%s-%s", name, groupIndex), uid, gvk)
}

func validateCreate(lws *LeaderWorkerSet) (field.ErrorList, error) {
	var allErrs field.ErrorList
	allErrs = append(allErrs, jobframework.ValidateQueueName(lws.Object())...)
	if features.Enabled(features.TopologyAwareScheduling) {
		validationErrs, err := validateTopologyRequest(lws)
		if err != nil {
			return nil, err
		}
		allErrs = append(allErrs, validationErrs...)
	}
	return allErrs, nil
}

func validateTopologyRequest(lws *LeaderWorkerSet) (field.ErrorList, error) {
	var allErrs field.ErrorList

	lwsv1 := leaderworkersetv1.LeaderWorkerSet(*lws)
	podSets, podSetsErr := podSets(&lwsv1)

	defaultPodSetName := kueuebeta.DefaultPodSetName

	if lws.Spec.LeaderWorkerTemplate.LeaderTemplate != nil {
		defaultPodSetName = workerPodSetName

		allErrs = append(allErrs, jobframework.ValidateTASPodSetRequest(leaderTemplateMetaPath, &lws.Spec.LeaderWorkerTemplate.LeaderTemplate.ObjectMeta)...)

		if podSetsErr == nil {
			leaderPodSet := podset.FindPodSetByName(podSets, leaderPodSetName)
			allErrs = append(allErrs, jobframework.ValidateSliceSizeAnnotationUpperBound(leaderTemplateMetaPath,
				&lws.Spec.LeaderWorkerTemplate.LeaderTemplate.ObjectMeta, leaderPodSet)...)
		}
	}

	allErrs = append(allErrs, jobframework.ValidateTASPodSetRequest(workerTemplateMetaPath, &lws.Spec.LeaderWorkerTemplate.WorkerTemplate.ObjectMeta)...)

	if podSetsErr == nil {
		workerPodSet := podset.FindPodSetByName(podSets, defaultPodSetName)
		allErrs = append(allErrs, jobframework.ValidateSliceSizeAnnotationUpperBound(workerTemplateMetaPath,
			&lws.Spec.LeaderWorkerTemplate.WorkerTemplate.ObjectMeta, workerPodSet)...)
	}

	if len(allErrs) > 0 {
		return allErrs, nil
	}

	return nil, podSetsErr
}

func validateImmutablePodTemplateSpec(newPodTemplateSpec *corev1.PodTemplateSpec, oldPodTemplateSpec *corev1.PodTemplateSpec, fieldPath *field.Path) field.ErrorList {
	var allErrors field.ErrorList
	if newPodTemplateSpec == nil || oldPodTemplateSpec == nil {
		allErrors = append(allErrors, apivalidation.ValidateImmutableField(newPodTemplateSpec, oldPodTemplateSpec, fieldPath)...)
	} else {
		allErrors = append(allErrors, jobframework.ValidateImmutablePodGroupPodSpec(&newPodTemplateSpec.Spec, &oldPodTemplateSpec.Spec, fieldPath.Child("spec"))...)
	}
	return allErrors
}
