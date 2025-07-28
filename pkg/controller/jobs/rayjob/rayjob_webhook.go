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

package rayjob

import (
	"context"
	"fmt"

	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	kueuebeta "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobframework/webhook"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/queue"
	"sigs.k8s.io/kueue/pkg/util/podset"
)

var (
	headGroupSpecsPath   = field.NewPath("spec", "rayClusterSpec", "headGroupSpec")
	headGroupMetaPath    = headGroupSpecsPath.Child("template, metadata")
	workerGroupSpecsPath = field.NewPath("spec", "rayClusterSpec", "workerGroupSpecs")
)

type RayJobWebhook struct {
	client                       client.Client
	queues                       *queue.Manager
	manageJobsWithoutQueueName   bool
	managedJobsNamespaceSelector labels.Selector
	cache                        *cache.Cache
}

// SetupRayJobWebhook configures the webhook for RayJob.
func SetupRayJobWebhook(mgr ctrl.Manager, opts ...jobframework.Option) error {
	options := jobframework.ProcessOptions(opts...)
	wh := &RayJobWebhook{
		client:                       mgr.GetClient(),
		queues:                       options.Queues,
		manageJobsWithoutQueueName:   options.ManageJobsWithoutQueueName,
		managedJobsNamespaceSelector: options.ManagedJobsNamespaceSelector,
		cache:                        options.Cache,
	}
	obj := &rayv1.RayJob{}
	return webhook.WebhookManagedBy(mgr).
		For(obj).
		WithMutationHandler(admission.WithCustomDefaulter(mgr.GetScheme(), obj, wh)).
		WithValidator(wh).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-ray-io-v1-rayjob,mutating=true,failurePolicy=fail,sideEffects=None,groups=ray.io,resources=rayjobs,verbs=create,versions=v1,name=mrayjob.kb.io,admissionReviewVersions=v1

var _ admission.CustomDefaulter = &RayJobWebhook{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the type
func (w *RayJobWebhook) Default(ctx context.Context, obj runtime.Object) error {
	job := fromObject(obj)
	log := ctrl.LoggerFrom(ctx).WithName("rayjob-webhook")
	log.V(5).Info("Applying defaults")
	jobframework.ApplyDefaultLocalQueue(job.Object(), w.queues.DefaultLocalQueueExist)
	if err := jobframework.ApplyDefaultForSuspend(ctx, job, w.client, w.manageJobsWithoutQueueName, w.managedJobsNamespaceSelector); err != nil {
		return err
	}
	jobframework.ApplyDefaultForManagedBy(job, w.queues, w.cache, log)
	return nil
}

// +kubebuilder:webhook:path=/validate-ray-io-v1-rayjob,mutating=false,failurePolicy=fail,sideEffects=None,groups=ray.io,resources=rayjobs,verbs=create;update,versions=v1,name=vrayjob.kb.io,admissionReviewVersions=v1

var _ admission.CustomValidator = &RayJobWebhook{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *RayJobWebhook) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	job := obj.(*rayv1.RayJob)
	log := ctrl.LoggerFrom(ctx).WithName("rayjob-webhook")
	log.Info("Validating create")
	validationErrs, err := w.validateCreate(job)
	if err != nil {
		return nil, err
	}
	return nil, validationErrs.ToAggregate()
}

func (w *RayJobWebhook) validateCreate(job *rayv1.RayJob) (field.ErrorList, error) {
	var allErrors field.ErrorList
	kueueJob := (*RayJob)(job)

	if w.manageJobsWithoutQueueName || jobframework.QueueName(kueueJob) != "" {
		spec := &job.Spec
		specPath := field.NewPath("spec")

		// Should always delete the cluster after the job has ended, otherwise it will continue to the queue's resources.
		if !spec.ShutdownAfterJobFinishes {
			allErrors = append(allErrors, field.Invalid(specPath.Child("shutdownAfterJobFinishes"), spec.ShutdownAfterJobFinishes, "a kueue managed job should delete the cluster after finishing"))
		}

		// Should not want existing cluster. Kueue (workload) should be able to control the admission of the actual work, not only the trigger.
		if len(spec.ClusterSelector) > 0 {
			allErrors = append(allErrors, field.Invalid(specPath.Child("clusterSelector"), spec.ClusterSelector, "a kueue managed job should not use an existing cluster"))
		}

		clusterSpec := spec.RayClusterSpec
		clusterSpecPath := specPath.Child("rayClusterSpec")

		// Should not use auto scaler. Once the resources are reserved by queue the cluster should do it's best to use them.
		if ptr.Deref(clusterSpec.EnableInTreeAutoscaling, false) {
			allErrors = append(allErrors, field.Invalid(clusterSpecPath.Child("enableInTreeAutoscaling"), clusterSpec.EnableInTreeAutoscaling, "a kueue managed job should not use autoscaling"))
		}

		// Should limit the worker count to 8 - 1 (max podSets num - cluster head)
		if len(clusterSpec.WorkerGroupSpecs) > 7 {
			allErrors = append(allErrors, field.TooMany(clusterSpecPath.Child("workerGroupSpecs"), len(clusterSpec.WorkerGroupSpecs), 7))
		}

		// None of the workerGroups should be named "head"
		for i := range clusterSpec.WorkerGroupSpecs {
			if clusterSpec.WorkerGroupSpecs[i].GroupName == headGroupPodSetName {
				allErrors = append(allErrors, field.Forbidden(clusterSpecPath.Child("workerGroupSpecs").Index(i).Child("groupName"), fmt.Sprintf("%q is reserved for the head group", headGroupPodSetName)))
			}
		}
	}

	allErrors = append(allErrors, jobframework.ValidateJobOnCreate(kueueJob)...)
	if features.Enabled(features.TopologyAwareScheduling) {
		validationErrs, err := w.validateTopologyRequest(job)
		if err != nil {
			return nil, err
		}
		allErrors = append(allErrors, validationErrs...)
	}

	return allErrors, nil
}

func (w *RayJobWebhook) validateTopologyRequest(rayJob *rayv1.RayJob) (field.ErrorList, error) {
	var allErrs field.ErrorList
	if rayJob.Spec.RayClusterSpec == nil {
		return allErrs, nil
	}

	podSets, podSetsErr := (*RayJob)(rayJob).PodSets()

	allErrs = append(allErrs, jobframework.ValidateTASPodSetRequest(headGroupMetaPath, &rayJob.Spec.RayClusterSpec.HeadGroupSpec.Template.ObjectMeta)...)

	if podSetsErr == nil {
		headGroupPodSetName := podset.FindPodSetByName(podSets, headGroupPodSetName)
		allErrs = append(allErrs, jobframework.ValidateSliceSizeAnnotationUpperBound(headGroupMetaPath, &rayJob.Spec.RayClusterSpec.HeadGroupSpec.Template.ObjectMeta, headGroupPodSetName)...)
	}

	for i, wgs := range rayJob.Spec.RayClusterSpec.WorkerGroupSpecs {
		workerGroupMetaPath := workerGroupSpecsPath.Index(i).Child("template", "metadata")
		allErrs = append(allErrs, jobframework.ValidateTASPodSetRequest(workerGroupMetaPath, &rayJob.Spec.RayClusterSpec.WorkerGroupSpecs[i].Template.ObjectMeta)...)

		if podSetsErr != nil {
			continue
		}

		workerPodSetName := podset.FindPodSetByName(podSets, kueuebeta.NewPodSetReference(wgs.GroupName))
		allErrs = append(allErrs, jobframework.ValidateSliceSizeAnnotationUpperBound(workerGroupMetaPath, &rayJob.Spec.RayClusterSpec.WorkerGroupSpecs[i].Template.ObjectMeta, workerPodSetName)...)
	}

	if len(allErrs) > 0 {
		return allErrs, nil
	}

	return nil, podSetsErr
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *RayJobWebhook) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	oldJob := oldObj.(*rayv1.RayJob)
	newJob := newObj.(*rayv1.RayJob)
	log := ctrl.LoggerFrom(ctx).WithName("rayjob-webhook")
	if w.manageJobsWithoutQueueName || jobframework.QueueName((*RayJob)(newJob)) != "" {
		log.Info("Validating update")
		allErrors := jobframework.ValidateJobOnUpdate((*RayJob)(oldJob), (*RayJob)(newJob), w.queues.DefaultLocalQueueExist)
		validationErrs, err := w.validateCreate(newJob)
		if err != nil {
			return nil, err
		}
		allErrors = append(allErrors, validationErrs...)
		return nil, allErrors.ToAggregate()
	}
	return nil, nil
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type
func (w *RayJobWebhook) ValidateDelete(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	return nil, nil
}
