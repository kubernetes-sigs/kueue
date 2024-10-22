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

package statefulset

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	apivalidation "k8s.io/apimachinery/pkg/api/validation"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/pod"
)

type Webhook struct {
	client                     client.Client
	manageJobsWithoutQueueName bool
}

func SetupWebhook(mgr ctrl.Manager, opts ...jobframework.Option) error {
	options := jobframework.ProcessOptions(opts...)
	wh := &Webhook{
		client:                     mgr.GetClient(),
		manageJobsWithoutQueueName: options.ManageJobsWithoutQueueName,
	}
	return ctrl.NewWebhookManagedBy(mgr).
		For(&appsv1.StatefulSet{}).
		WithDefaulter(wh).
		WithValidator(wh).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-apps-v1-statefulset,mutating=true,failurePolicy=fail,sideEffects=None,groups="apps",resources=statefulsets,verbs=create,versions=v1,name=mstatefulset.kb.io,admissionReviewVersions=v1

var _ webhook.CustomDefaulter = &Webhook{}

func (wh *Webhook) Default(ctx context.Context, obj runtime.Object) error {
	ss := fromObject(obj)
	log := ctrl.LoggerFrom(ctx).WithName("statefulset-webhook")
	log.V(5).Info("Applying defaults")

	cqLabel, ok := ss.Labels[constants.QueueLabel]
	if ok {
		if ss.Spec.Template.Labels == nil {
			ss.Spec.Template.Labels = make(map[string]string, 1)
		}
		ss.Spec.Template.Labels[constants.QueueLabel] = cqLabel
	}

	podGroupName := GetWorkloadName(ss.Name, ss.UID)
	var podGroupReplicas int32 = 1
	if ss.Spec.Replicas != nil {
		podGroupReplicas = *ss.Spec.Replicas
	}
	if ss.Spec.Template.Labels == nil {
		ss.Spec.Template.Labels = make(map[string]string, 1)
	}
	ss.Spec.Template.Labels[pod.GroupNameLabel] = podGroupName
	if ss.Spec.Template.Annotations == nil {
		ss.Spec.Template.Annotations = make(map[string]string, 2)
	}
	ss.Spec.Template.Annotations[pod.GroupTotalCountAnnotation] = fmt.Sprint(podGroupReplicas)
	ss.Spec.Template.Annotations[pod.GroupFastAdmissionAnnotation] = "true"

	return nil
}

// +kubebuilder:webhook:path=/validate-apps-v1-statefulset,mutating=false,failurePolicy=fail,sideEffects=None,groups="apps",resources=statefulsets,verbs=create;update,versions=v1,name=vstatefulset.kb.io,admissionReviewVersions=v1

var _ webhook.CustomValidator = &Webhook{}

func (wh *Webhook) ValidateCreate(context.Context, runtime.Object) (warnings admission.Warnings, err error) {
	return nil, nil
}

var (
	statefulsetLabelsPath         = field.NewPath("metadata", "labels")
	statefulsetQueueNameLabelPath = statefulsetLabelsPath.Key(constants.QueueLabel)
	statefulsetReplicasPath       = field.NewPath("spec", "replicas")
	statefulsetGroupNameLabelPath = statefulsetLabelsPath.Key(pod.GroupNameLabel)

	podSpecQueueNameLabelPath = field.NewPath("spec", "template", "metadata", "labels").
					Key(constants.QueueLabel)
)

func (wh *Webhook) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (warnings admission.Warnings, err error) {
	oldStatefulSet := fromObject(oldObj)
	newStatefulSet := fromObject(newObj)

	log := ctrl.LoggerFrom(ctx).WithName("statefulset-webhook")
	log.V(5).Info("Validating update")
	allErrs := apivalidation.ValidateImmutableField(
		newStatefulSet.GetLabels()[constants.QueueLabel],
		oldStatefulSet.GetLabels()[constants.QueueLabel],
		statefulsetQueueNameLabelPath,
	)
	allErrs = append(allErrs, apivalidation.ValidateImmutableField(
		newStatefulSet.Spec.Template.GetLabels()[constants.QueueLabel],
		oldStatefulSet.Spec.Template.GetLabels()[constants.QueueLabel],
		podSpecQueueNameLabelPath,
	)...)
	allErrs = append(allErrs, apivalidation.ValidateImmutableField(
		newStatefulSet.GetLabels()[pod.GroupNameLabel],
		oldStatefulSet.GetLabels()[pod.GroupNameLabel],
		statefulsetGroupNameLabelPath,
	)...)

	// TODO(#3279): support resizes later
	allErrs = append(allErrs, apivalidation.ValidateImmutableField(
		newStatefulSet.Spec.Replicas,
		oldStatefulSet.Spec.Replicas,
		statefulsetReplicasPath,
	)...)

	return warnings, allErrs.ToAggregate()
}

func (wh *Webhook) ValidateDelete(context.Context, runtime.Object) (warnings admission.Warnings, err error) {
	return nil, nil
}

func GetWorkloadName(statefulSetName string, statefulSetUID types.UID) string {
	return jobframework.GetWorkloadNameForOwnerWithGVK(statefulSetName, statefulSetUID, gvk)
}
