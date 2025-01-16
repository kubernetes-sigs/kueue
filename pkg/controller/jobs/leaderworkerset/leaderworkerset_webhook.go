/*
Copyright 2025 The Kubernetes Authors.

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
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
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
	labelsPath         = field.NewPath("metadata", "labels")
	queueNameLabelPath = labelsPath.Key(constants.QueueLabel)
	startupPolicyPath  = field.NewPath("spec", "startupPolicy")
)

func (wh *Webhook) ValidateCreate(ctx context.Context, obj runtime.Object) (warnings admission.Warnings, err error) {
	lws := fromObject(obj)

	log := ctrl.LoggerFrom(ctx).WithName("leaderworkerset-webhook")
	log.V(5).Info("Validating create")

	allErrs := jobframework.ValidateQueueName(lws.Object())
	allErrs = append(allErrs, validateStartupPolicy(lws)...)

	return nil, allErrs.ToAggregate()
}

func (wh *Webhook) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (warnings admission.Warnings, err error) {
	oldLeaderWorkerSet := fromObject(oldObj)
	newLeaderWorkerSet := fromObject(newObj)

	log := ctrl.LoggerFrom(ctx).WithName("leaderworkerset-webhook")
	log.V(5).Info("Validating update")

	allErrs := apivalidation.ValidateImmutableField(
		jobframework.QueueNameForObject(newLeaderWorkerSet.Object()),
		jobframework.QueueNameForObject(oldLeaderWorkerSet.Object()),
		queueNameLabelPath,
	)
	allErrs = append(allErrs, validateStartupPolicy(newLeaderWorkerSet)...)

	return warnings, allErrs.ToAggregate()
}

func (wh *Webhook) ValidateDelete(context.Context, runtime.Object) (warnings admission.Warnings, err error) {
	return nil, nil
}

func GetWorkloadName(lws *leaderworkersetv1.LeaderWorkerSet, groupIndex string) (string, error) {
	ownerName := lws.Name
	if lws.Spec.LeaderWorkerTemplate.LeaderTemplate != nil {
		leaderHash, err := utilpod.GenerateShape(lws.Spec.LeaderWorkerTemplate.LeaderTemplate.Spec)
		if err != nil {
			return "", err
		}
		ownerName = fmt.Sprintf("%s-%s", ownerName, leaderHash)
	}
	workerHash, err := utilpod.GenerateShape(lws.Spec.LeaderWorkerTemplate.WorkerTemplate.Spec)
	if err != nil {
		return "", err
	}
	ownerName = fmt.Sprintf("%s-%s", ownerName, workerHash)

	// Workload name should be unique for leader shape, worker shape and group index.
	return jobframework.GetWorkloadNameForOwnerWithGVK(fmt.Sprintf("%s-%s", ownerName, groupIndex), lws.UID, gvk), nil
}

func validateStartupPolicy(lws *LeaderWorkerSet) field.ErrorList {
	allErrors := field.ErrorList{}
	// TODO(#3232): Support LeaderReady StartupPolicy
	if jobframework.QueueNameForObject(lws.Object()) != "" && lws.Spec.StartupPolicy == leaderworkersetv1.LeaderReadyStartupPolicy {
		allErrors = append(allErrors,
			field.Invalid(startupPolicyPath, lws.Spec.StartupPolicy, "only the LeaderCreated startup policy is allowed when using the kueue.x-k8s.io/queue-name label or annotation"),
		)
	}
	return allErrors
}
