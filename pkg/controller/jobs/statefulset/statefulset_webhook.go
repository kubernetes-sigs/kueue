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
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/pod"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
)

const (
	StatefulSetNameLabel = "kueue.x-k8s.io/statefulset-name"
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

// +kubebuilder:webhook:path=/mutate-apps-v1-statefulset,mutating=true,failurePolicy=fail,sideEffects=None,groups="apps",resources=statefulsets,verbs=create;update,versions=v1,name=mstatefulset.kb.io,admissionReviewVersions=v1

var _ webhook.CustomDefaulter = &Webhook{}

func (wh *Webhook) Default(ctx context.Context, obj runtime.Object) error {
	ss := fromObject(obj)
	log := ctrl.LoggerFrom(ctx).WithName("statefulset-webhook")
	log.V(5).Info("Applying defaults")

	queueName := jobframework.QueueNameForObject(ss.Object())
	if queueName == "" {
		return nil
	}

	if ss.Spec.Template.Labels == nil {
		ss.Spec.Template.Labels = make(map[string]string, 3)
	}
	ss.Spec.Template.Labels[StatefulSetNameLabel] = ss.Name
	ss.Spec.Template.Labels[constants.QueueLabel] = queueName
	groupName, err := GetWorkloadName(obj.(*appsv1.StatefulSet))
	if err != nil {
		return err
	}
	ss.Spec.Template.Labels[pod.GroupNameLabel] = groupName

	if ss.Spec.Template.Annotations == nil {
		ss.Spec.Template.Annotations = make(map[string]string, 4)
	}
	ss.Spec.Template.Annotations[pod.GroupTotalCountAnnotation] = fmt.Sprint(ptr.Deref(ss.Spec.Replicas, 1))
	ss.Spec.Template.Annotations[pod.GroupFastAdmissionAnnotation] = "true"
	ss.Spec.Template.Annotations[pod.GroupServingAnnotation] = "true"
	ss.Spec.Template.Annotations[kueuealpha.PodGroupPodIndexLabelAnnotation] = appsv1.PodIndexLabel

	return nil
}

// +kubebuilder:webhook:path=/validate-apps-v1-statefulset,mutating=false,failurePolicy=fail,sideEffects=None,groups="apps",resources=statefulsets,verbs=create;update,versions=v1,name=vstatefulset.kb.io,admissionReviewVersions=v1

var _ webhook.CustomValidator = &Webhook{}

func (wh *Webhook) ValidateCreate(ctx context.Context, obj runtime.Object) (warnings admission.Warnings, err error) {
	sts := fromObject(obj)

	log := ctrl.LoggerFrom(ctx).WithName("statefulset-webhook")
	log.V(5).Info("Validating create")

	allErrs := jobframework.ValidateQueueName(sts.Object())

	return nil, allErrs.ToAggregate()
}

var (
	labelsPath                = field.NewPath("metadata", "labels")
	queueNameLabelPath        = labelsPath.Key(constants.QueueLabel)
	replicasPath              = field.NewPath("spec", "replicas")
	groupNameLabelPath        = labelsPath.Key(pod.GroupNameLabel)
	podSpecLabelPath          = field.NewPath("spec", "template", "metadata", "labels")
	podSpecQueueNameLabelPath = podSpecLabelPath.Key(constants.QueueLabel)
)

func (wh *Webhook) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (warnings admission.Warnings, err error) {
	oldStatefulSet := fromObject(oldObj)
	newStatefulSet := fromObject(newObj)

	log := ctrl.LoggerFrom(ctx).WithName("statefulset-webhook")
	log.V(5).Info("Validating update")

	oldQueueName := jobframework.QueueNameForObject(oldStatefulSet.Object())
	newQueueName := jobframework.QueueNameForObject(newStatefulSet.Object())

	allErrs := jobframework.ValidateQueueName(newStatefulSet.Object())

	// Prevents updating the queue-name if at least one Pod is not suspended
	// or if the queue-name has been deleted.
	if oldStatefulSet.Status.ReadyReplicas > 0 || newQueueName == "" {
		allErrs = append(allErrs, apivalidation.ValidateImmutableField(oldQueueName, newQueueName, queueNameLabelPath)...)
	}

	return warnings, allErrs.ToAggregate()
}

func (wh *Webhook) ValidateDelete(context.Context, runtime.Object) (warnings admission.Warnings, err error) {
	return nil, nil
}

func GetWorkloadName(sts *appsv1.StatefulSet) (string, error) {
	shape, err := utilpod.GenerateShape(sts.Spec.Template.Spec)
	if err != nil {
		return "", err
	}
	ownerName := fmt.Sprintf("%s-%s", sts.Name, shape)
	// Passing empty UID as it is not available before object creation
	return jobframework.GetWorkloadNameForOwnerWithGVK(ownerName, "", gvk), nil
}
