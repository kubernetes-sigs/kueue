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

package jobset

import (
	"context"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
	jobsetapi "sigs.k8s.io/jobset/api/jobset/v1alpha2"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobframework/webhook"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/queue"
	"sigs.k8s.io/kueue/pkg/util/podset"
)

var (
	replicatedJobsPath = field.NewPath("spec", "replicatedJobs")
)

type JobSetWebhook struct {
	client                       client.Client
	manageJobsWithoutQueueName   bool
	managedJobsNamespaceSelector labels.Selector
	queues                       *queue.Manager
	cache                        *cache.Cache
}

// SetupJobSetWebhook configures the webhook for kubeflow JobSet.
func SetupJobSetWebhook(mgr ctrl.Manager, opts ...jobframework.Option) error {
	options := jobframework.ProcessOptions(opts...)
	wh := &JobSetWebhook{
		client:                       mgr.GetClient(),
		manageJobsWithoutQueueName:   options.ManageJobsWithoutQueueName,
		managedJobsNamespaceSelector: options.ManagedJobsNamespaceSelector,
		queues:                       options.Queues,
		cache:                        options.Cache,
	}
	obj := &jobsetapi.JobSet{}
	return webhook.WebhookManagedBy(mgr).
		For(obj).
		WithMutationHandler(admission.WithCustomDefaulter(mgr.GetScheme(), obj, wh)).
		WithValidator(wh).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-jobset-x-k8s-io-v1alpha2-jobset,mutating=true,failurePolicy=fail,sideEffects=None,groups=jobset.x-k8s.io,resources=jobsets,verbs=create,versions=v1alpha2,name=mjobset.kb.io,admissionReviewVersions=v1

var _ admission.CustomDefaulter = &JobSetWebhook{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the type
func (w *JobSetWebhook) Default(ctx context.Context, obj runtime.Object) error {
	jobSet := fromObject(obj)
	log := ctrl.LoggerFrom(ctx).WithName("jobset-webhook")
	log.V(5).Info("Applying defaults")

	jobframework.ApplyDefaultLocalQueue(jobSet.Object(), w.queues.DefaultLocalQueueExist)
	if err := jobframework.ApplyDefaultForSuspend(ctx, jobSet, w.client, w.manageJobsWithoutQueueName, w.managedJobsNamespaceSelector); err != nil {
		return err
	}

	jobframework.ApplyDefaultForManagedBy(jobSet, w.queues, w.cache, log)

	return nil
}

// +kubebuilder:webhook:path=/validate-jobset-x-k8s-io-v1alpha2-jobset,mutating=false,failurePolicy=fail,sideEffects=None,groups=jobset.x-k8s.io,resources=jobsets,verbs=create;update,versions=v1alpha2,name=vjobset.kb.io,admissionReviewVersions=v1

var _ admission.CustomValidator = &JobSetWebhook{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *JobSetWebhook) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	jobSet := fromObject(obj)
	log := ctrl.LoggerFrom(ctx).WithName("jobset-webhook")
	log.Info("Validating create")
	validationErrs, err := w.validateCreate(jobSet)
	if err != nil {
		return nil, err
	}
	return nil, validationErrs.ToAggregate()
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *JobSetWebhook) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	oldJobSet := fromObject(oldObj)
	newJobSet := fromObject(newObj)
	log := ctrl.LoggerFrom(ctx).WithName("jobset-webhook")
	log.Info("Validating update")
	validationErrs, err := w.validateUpdate(oldJobSet, newJobSet)
	if err != nil {
		return nil, err
	}
	return nil, validationErrs.ToAggregate()
}

func (w *JobSetWebhook) validateUpdate(oldJob, newJob *JobSet) (field.ErrorList, error) {
	var allErrs field.ErrorList
	allErrs = append(allErrs, jobframework.ValidateJobOnUpdate(oldJob, newJob, w.queues.DefaultLocalQueueExist)...)
	validationErrs, err := w.validateCreate(newJob)
	if err != nil {
		return nil, err
	}
	allErrs = append(allErrs, validationErrs...)
	return allErrs, nil
}

func (w *JobSetWebhook) validateCreate(jobSet *JobSet) (field.ErrorList, error) {
	var allErrs field.ErrorList
	allErrs = append(allErrs, jobframework.ValidateJobOnCreate(jobSet)...)
	if features.Enabled(features.TopologyAwareScheduling) {
		validationErrs, err := w.validateTopologyRequest(jobSet)
		if err != nil {
			return nil, err
		}
		allErrs = append(allErrs, validationErrs...)
	}
	return allErrs, nil
}

func (w *JobSetWebhook) validateTopologyRequest(jobSet *JobSet) (field.ErrorList, error) {
	var allErrs field.ErrorList

	podSets, podSetsErr := jobSet.PodSets()

	for i, rj := range jobSet.Spec.ReplicatedJobs {
		replicaMetaPath := replicatedJobsPath.Index(i).Child("template", "metadata")
		allErrs = append(allErrs, jobframework.ValidateTASPodSetRequest(replicaMetaPath, &jobSet.Spec.ReplicatedJobs[i].Template.Spec.Template.ObjectMeta)...)

		if podSetsErr != nil {
			continue
		}

		podSet := podset.FindPodSetByName(podSets, kueue.NewPodSetReference(rj.Name))
		allErrs = append(allErrs, jobframework.ValidateSliceSizeAnnotationUpperBound(replicaMetaPath, &jobSet.Spec.ReplicatedJobs[i].Template.Spec.Template.ObjectMeta, podSet)...)
	}

	if len(allErrs) > 0 {
		return allErrs, nil
	}

	return nil, podSetsErr
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type
func (w *JobSetWebhook) ValidateDelete(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	return nil, nil
}
