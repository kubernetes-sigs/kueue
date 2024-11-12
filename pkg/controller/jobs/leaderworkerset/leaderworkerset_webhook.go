/*
Copyright 2024 The Kubernetes Authors.

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

	apivalidation "k8s.io/apimachinery/pkg/api/validation"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
	leaderworkersetv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/pod"
)

type Webhook struct {
	client client.Client
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

// +kubebuilder:webhook:path=/mutate-leaderworkerset-x-k8s-io-v1-leaderworkerset,mutating=true,failurePolicy=fail,sideEffects=None,groups="leaderworkerset.x-k8s.io",resources=leaderworkersets,verbs=create,versions=v1,name=mleaderworkerset.kb.io,admissionReviewVersions=v1

var _ webhook.CustomDefaulter = &Webhook{}

func (wh *Webhook) Default(ctx context.Context, obj runtime.Object) error {
	fmt.Printf("Defaulting LeaderWorkerSet\n")
	lws := fromObject(obj)
	log := ctrl.LoggerFrom(ctx).WithName("leaderworkerset-webhook")
	log.V(5).Info("Applying defaults")

	queueName := jobframework.QueueNameForObject(lws.Object())
	if queueName == "" {
		return nil
	}

	groupSize := ptr.Deref(lws.Spec.LeaderWorkerTemplate.Size, 1) * ptr.Deref(lws.Spec.Replicas, 1)
	// LeaderTemplate defaults to the WorkerTemplate if not specified
	if lws.Spec.LeaderWorkerTemplate.LeaderTemplate != nil {
		if lws.Spec.LeaderWorkerTemplate.LeaderTemplate.Labels == nil {
			lws.Spec.LeaderWorkerTemplate.LeaderTemplate.Labels = make(map[string]string, 2)
		}
		lws.Spec.LeaderWorkerTemplate.LeaderTemplate.Labels[constants.QueueLabel] = queueName
		lws.Spec.LeaderWorkerTemplate.LeaderTemplate.Labels[pod.GroupNameLabel] = GetWorkloadName(lws.Name)

		if lws.Spec.LeaderWorkerTemplate.LeaderTemplate.Annotations == nil {
			lws.Spec.LeaderWorkerTemplate.LeaderTemplate.Annotations = make(map[string]string, 2)
		}
		lws.Spec.LeaderWorkerTemplate.LeaderTemplate.Annotations[pod.GroupTotalCountAnnotation] = fmt.Sprint(groupSize)
		lws.Spec.LeaderWorkerTemplate.LeaderTemplate.Annotations[pod.GroupFastAdmissionAnnotation] = "true"
	}

	if lws.Spec.LeaderWorkerTemplate.WorkerTemplate.Labels == nil {
		lws.Spec.LeaderWorkerTemplate.WorkerTemplate.Labels = make(map[string]string, 2)
	}
	lws.Spec.LeaderWorkerTemplate.WorkerTemplate.Labels[constants.QueueLabel] = queueName
	lws.Spec.LeaderWorkerTemplate.WorkerTemplate.Labels[pod.GroupNameLabel] = GetWorkloadName(lws.Name)

	if lws.Spec.LeaderWorkerTemplate.WorkerTemplate.Annotations == nil {
		lws.Spec.LeaderWorkerTemplate.WorkerTemplate.Annotations = make(map[string]string, 2)
	}
	lws.Spec.LeaderWorkerTemplate.WorkerTemplate.Annotations[pod.GroupTotalCountAnnotation] = fmt.Sprint(groupSize)
	lws.Spec.LeaderWorkerTemplate.WorkerTemplate.Annotations[pod.GroupFastAdmissionAnnotation] = "true"

	return nil
}

// +kubebuilder:webhook:path=/validate-leaderworkerset-x-k8s-io-v1-leaderworkerset,mutating=false,failurePolicy=fail,sideEffects=None,groups="leaderworkerset.x-k8s.io",resources=leaderworkersets,verbs=create;update,versions=v1,name=vleaderworkerset.kb.io,admissionReviewVersions=v1

var _ webhook.CustomValidator = &Webhook{}

func (wh *Webhook) ValidateCreate(ctx context.Context, obj runtime.Object) (warnings admission.Warnings, err error) {
	lws := fromObject(obj)

	log := ctrl.LoggerFrom(ctx).WithName("leaderworkerset-webhook")
	log.V(5).Info("Validating create")

	allErrs := jobframework.ValidateQueueName(lws.Object())

	return nil, allErrs.ToAggregate()
}

var (
	leaderTemplatePath               = field.NewPath("spec", "leaderWorkerTemplate", "leaderTemplate")
	leaderTemplateLabelsPath         = leaderTemplatePath.Key("labels")
	leaderTemplateQueueNameLabelPath = leaderTemplateLabelsPath.Key(constants.QueueLabel)
	leaderTemplateGroupNameLabelPath = leaderTemplateLabelsPath.Key(pod.GroupNameLabel)
	workerTemplatePath               = field.NewPath("spec", "leaderWorkerTemplate", "workerTemplate")
	workerTemplateLabelsPath         = workerTemplatePath.Key("labels")
	workerTemplateQueueNameLabelPath = workerTemplateLabelsPath.Key(constants.QueueLabel)
	workerTemplateGroupNameLabelPath = workerTemplateLabelsPath.Key(pod.GroupNameLabel)
	sizePath                         = field.NewPath("spec", "leaderWorkerTemplate", "size")
)

func (wh *Webhook) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (warnings admission.Warnings, err error) {
	oldLeaderWorkerSet := fromObject(oldObj)
	newLeaderWorkerSet := fromObject(newObj)

	log := ctrl.LoggerFrom(ctx).WithName("leaderworkerset-webhook")
	log.V(5).Info("Validating update")
	// TODO: check for annotation
	allErrs := apivalidation.ValidateImmutableField(
		newLeaderWorkerSet.Spec.LeaderWorkerTemplate.LeaderTemplate.GetLabels()[constants.QueueLabel],
		oldLeaderWorkerSet.Spec.LeaderWorkerTemplate.LeaderTemplate.GetLabels()[constants.QueueLabel],
		leaderTemplateQueueNameLabelPath,
	)
	allErrs = append(allErrs, apivalidation.ValidateImmutableField(
		newLeaderWorkerSet.Spec.LeaderWorkerTemplate.LeaderTemplate.GetLabels()[pod.GroupNameLabel],
		oldLeaderWorkerSet.Spec.LeaderWorkerTemplate.LeaderTemplate.GetLabels()[pod.GroupNameLabel],
		leaderTemplateGroupNameLabelPath,
	)...)
	allErrs = append(allErrs, apivalidation.ValidateImmutableField(
		newLeaderWorkerSet.Spec.LeaderWorkerTemplate.WorkerTemplate.GetLabels()[constants.QueueLabel],
		oldLeaderWorkerSet.Spec.LeaderWorkerTemplate.WorkerTemplate.GetLabels()[constants.QueueLabel],
		workerTemplateQueueNameLabelPath,
	)...)
	allErrs = append(allErrs, apivalidation.ValidateImmutableField(
		newLeaderWorkerSet.Spec.LeaderWorkerTemplate.WorkerTemplate.GetLabels()[pod.GroupNameLabel],
		oldLeaderWorkerSet.Spec.LeaderWorkerTemplate.WorkerTemplate.GetLabels()[pod.GroupNameLabel],
		workerTemplateGroupNameLabelPath,
	)...)

	// TODO(#...): support resizes later
	allErrs = append(allErrs, apivalidation.ValidateImmutableField(
		newLeaderWorkerSet.Spec.LeaderWorkerTemplate.Size,
		oldLeaderWorkerSet.Spec.LeaderWorkerTemplate.Size,
		sizePath,
	)...)

	// TODO(#...): support mutation of leader/worker templates later
	allErrs = append(allErrs, apivalidation.ValidateImmutableField(
		newLeaderWorkerSet.Spec.LeaderWorkerTemplate.LeaderTemplate,
		oldLeaderWorkerSet.Spec.LeaderWorkerTemplate.LeaderTemplate,
		leaderTemplatePath,
	)...)
	allErrs = append(allErrs, apivalidation.ValidateImmutableField(
		newLeaderWorkerSet.Spec.LeaderWorkerTemplate.WorkerTemplate,
		oldLeaderWorkerSet.Spec.LeaderWorkerTemplate.WorkerTemplate,
		workerTemplatePath,
	)...)

	return warnings, nil
}

func (wh *Webhook) ValidateDelete(context.Context, runtime.Object) (warnings admission.Warnings, err error) {
	return nil, nil
}

func GetWorkloadName(lwsName string) string {
	// Passing empty UID as it is not available before object creation
	return jobframework.GetWorkloadNameForOwnerWithGVK(lwsName, "", gvk)
}
