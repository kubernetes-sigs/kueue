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

	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	podcontroller "sigs.k8s.io/kueue/pkg/controller/jobs/pod"
	"sigs.k8s.io/kueue/pkg/queue"
)

type Webhook struct {
	client                       client.Client
	manageJobsWithoutQueueName   bool
	managedJobsNamespaceSelector labels.Selector
	queues                       *queue.Manager
}

func SetupWebhook(mgr ctrl.Manager, _ ...jobframework.Option) error {
	wh := &Webhook{
		client: mgr.GetClient(),
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
			wh.podTemplateSpecDefault(lws.Spec.LeaderWorkerTemplate.LeaderTemplate)
		}
		wh.podTemplateSpecDefault(&lws.Spec.LeaderWorkerTemplate.WorkerTemplate)
	}

	return nil
}

func (wh *Webhook) podTemplateSpecDefault(podTemplateSpec *corev1.PodTemplateSpec) {
	if podTemplateSpec.Annotations == nil {
		podTemplateSpec.Annotations = make(map[string]string, 1)
	}
	podTemplateSpec.Annotations[podcontroller.SuspendedByParentAnnotation] = FrameworkName
	podTemplateSpec.Annotations[podcontroller.GroupServingAnnotation] = "true"
}

// +kubebuilder:webhook:path=/validate-leaderworkerset-x-k8s-io-v1-leaderworkerset,mutating=false,failurePolicy=fail,sideEffects=None,groups="leaderworkerset.x-k8s.io",resources=leaderworkersets,verbs=create;update,versions=v1,name=vleaderworkerset.kb.io,admissionReviewVersions=v1

var _ webhook.CustomValidator = &Webhook{}

var (
	labelsPath               = field.NewPath("metadata", "labels")
	queueNameLabelPath       = labelsPath.Key(constants.QueueLabel)
	priorityClassNamePath    = labelsPath.Key(constants.WorkloadPriorityClassLabel)
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

	allErrs := validateCreate(lws)

	return nil, allErrs.ToAggregate()
}

func (wh *Webhook) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (warnings admission.Warnings, err error) {
	oldLeaderWorkerSet := fromObject(oldObj)
	newLeaderWorkerSet := fromObject(newObj)

	log := ctrl.LoggerFrom(ctx).WithName("leaderworkerset-webhook")
	log.V(5).Info("Validating update")

	allErrs := validateCreate(newLeaderWorkerSet)

	allErrs = append(allErrs, apivalidation.ValidateImmutableField(
		jobframework.QueueNameForObject(newLeaderWorkerSet.Object()),
		jobframework.QueueNameForObject(oldLeaderWorkerSet.Object()),
		queueNameLabelPath,
	)...)
	allErrs = append(allErrs, jobframework.ValidateUpdateForWorkloadPriorityClassName(
		newLeaderWorkerSet.Object(),
		oldLeaderWorkerSet.Object(),
	)...)

	if jobframework.IsManagedByKueue(newLeaderWorkerSet.Object()) {
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

func validateCreate(lws *LeaderWorkerSet) field.ErrorList {
	var allErrs field.ErrorList
	allErrs = append(allErrs, jobframework.ValidateQueueName(lws.Object())...)
	allErrs = append(allErrs, validateTopologyRequest(lws)...)
	return allErrs
}

func validateTopologyRequest(lws *LeaderWorkerSet) field.ErrorList {
	var allErrs field.ErrorList
	if lws.Spec.LeaderWorkerTemplate.LeaderTemplate != nil {
		allErrs = append(allErrs, jobframework.ValidateTASPodSetRequest(leaderTemplateMetaPath, &lws.Spec.LeaderWorkerTemplate.LeaderTemplate.ObjectMeta)...)
	}
	allErrs = append(allErrs, jobframework.ValidateTASPodSetRequest(workerTemplateMetaPath, &lws.Spec.LeaderWorkerTemplate.WorkerTemplate.ObjectMeta)...)
	return allErrs
}

func validateImmutablePodTemplateSpec(newPodTemplateSpec *corev1.PodTemplateSpec, oldPodTemplateSpec *corev1.PodTemplateSpec, fieldPath *field.Path) field.ErrorList {
	allErrors := field.ErrorList{}
	if newPodTemplateSpec == nil || oldPodTemplateSpec == nil {
		allErrors = append(allErrors, apivalidation.ValidateImmutableField(newPodTemplateSpec, oldPodTemplateSpec, fieldPath)...)
	} else {
		allErrors = append(allErrors, jobframework.ValidateImmutablePodGroupPodSpec(&newPodTemplateSpec.Spec, &oldPodTemplateSpec.Spec, fieldPath.Child("spec"))...)
	}
	return allErrors
}
