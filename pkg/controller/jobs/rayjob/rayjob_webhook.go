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

	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/raycluster"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/webhook"
)

var (
	headGroupSpecsPath   = field.NewPath("spec", "rayClusterSpec", "headGroupSpec")
	headGroupMetaPath    = headGroupSpecsPath.Child("template", "metadata")
	workerGroupSpecsPath = field.NewPath("spec", "rayClusterSpec", "workerGroupSpecs")
)

type RayJobWebhook struct {
	client                       client.Client
	queues                       *qcache.Manager
	manageJobsWithoutQueueName   bool
	managedJobsNamespaceSelector labels.Selector
	cache                        *schdcache.Cache
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
	if options.NoopWebhook {
		return webhook.SetupNoopWebhook(mgr, obj)
	}
	return ctrl.NewWebhookManagedBy(mgr, obj).
		WithDefaulter(wh).
		WithValidator(wh).
		WithLogConstructor(jobframework.WebhookLogConstructor(fromObject(obj).GVK(), options.RoleTracker)).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-ray-io-v1-rayjob,mutating=true,failurePolicy=fail,sideEffects=None,groups=ray.io,resources=rayjobs,verbs=create,versions=v1,name=mrayjob.kb.io,admissionReviewVersions=v1

var _ admission.Defaulter[*rayv1.RayJob] = &RayJobWebhook{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the type
func (w *RayJobWebhook) Default(ctx context.Context, obj *rayv1.RayJob) error {
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

var _ admission.Validator[*rayv1.RayJob] = &RayJobWebhook{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *RayJobWebhook) ValidateCreate(ctx context.Context, obj *rayv1.RayJob) (admission.Warnings, error) {
	log := ctrl.LoggerFrom(ctx).WithName("rayjob-webhook")
	log.Info("Validating create")
	validationErrs, err := w.validateCreate(ctx, obj)
	if err != nil {
		return nil, err
	}
	return nil, validationErrs.ToAggregate()
}

func (w *RayJobWebhook) validateCreate(ctx context.Context, job *rayv1.RayJob) (field.ErrorList, error) {
	var allErrors field.ErrorList
	kueueJob := (*RayJob)(job)

	if w.manageJobsWithoutQueueName || jobframework.QueueName(kueueJob) != "" {
		spec := &job.Spec
		specPath := field.NewPath("spec")
		hasClusterSelector := len(spec.ClusterSelector) > 0
		hasRayClusterSpec := spec.RayClusterSpec != nil

		// Validate the combination of clusterSelector and RayClusterSpec
		if hasClusterSelector && hasRayClusterSpec {
			// len(spec.ClusterSelector)>0 && spec.RayClusterSpec != nil -> validation error
			allErrors = append(allErrors, field.Invalid(specPath.Child("clusterSelector"), spec.ClusterSelector, "a kueue managed job should not use an existing cluster"))
			return allErrors, nil
		}

		if hasClusterSelector && !hasRayClusterSpec {
			// len(spec.ClusterSelector)>0 && spec.RayClusterSpec == nil -> valid (skip validation)
			// RayJobs using existing clusters are not managed by Kueue
			return allErrors, nil
		}

		if !hasClusterSelector && !hasRayClusterSpec {
			// len(spec.ClusterSelector)==0 && spec.RayClusterSpec == nil -> validation error
			allErrors = append(allErrors, field.Required(specPath.Child("rayClusterSpec"), "rayClusterSpec is required for Kueue-managed jobs that don't use clusterSelector"))
			return allErrors, nil
		}
		// len(spec.ClusterSelector)==0 && spec.RayClusterSpec != nil -> valid + perform additional validation

		// Should always delete the cluster after the job has ended, otherwise it will continue to the queue's resources.
		if !spec.ShutdownAfterJobFinishes {
			allErrors = append(allErrors, field.Invalid(specPath.Child("shutdownAfterJobFinishes"), spec.ShutdownAfterJobFinishes, "a kueue managed job should delete the cluster after finishing"))
		}

		clusterSpec := spec.RayClusterSpec
		clusterSpecPath := specPath.Child("rayClusterSpec")
		rayClusterSpecErrors := raycluster.ValidateCreate(job, clusterSpec, clusterSpecPath)
		allErrors = append(allErrors, rayClusterSpecErrors...)
	}

	allErrors = append(allErrors, jobframework.ValidateJobOnCreate(kueueJob)...)
	if features.Enabled(features.TopologyAwareScheduling) {
		validationErrs, err := w.validateTopologyRequest(ctx, job)
		if err != nil {
			return nil, err
		}
		allErrors = append(allErrors, validationErrs...)
	}

	return allErrors, nil
}

func (w *RayJobWebhook) validateTopologyRequest(ctx context.Context, rayJob *rayv1.RayJob) (field.ErrorList, error) {
	job := (*RayJob)(rayJob)
	return raycluster.ValidateTopologyRequest(ctx, job, rayJob.Spec.RayClusterSpec, headGroupMetaPath, workerGroupSpecsPath)
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *RayJobWebhook) ValidateUpdate(ctx context.Context, oldObj, newObj *rayv1.RayJob) (admission.Warnings, error) {
	oldJob := fromObject(oldObj)
	newJob := fromObject(newObj)
	log := ctrl.LoggerFrom(ctx).WithName("rayjob-webhook")
	if w.manageJobsWithoutQueueName || jobframework.QueueName(newJob) != "" {
		log.Info("Validating update")
		allErrors := jobframework.ValidateJobOnUpdate(oldJob, newJob, w.queues.DefaultLocalQueueExist)
		validationErrs, err := w.validateCreate(ctx, newObj)
		if err != nil {
			return nil, err
		}
		allErrors = append(allErrors, validationErrs...)
		return nil, allErrors.ToAggregate()
	}
	return nil, nil
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type
func (w *RayJobWebhook) ValidateDelete(_ context.Context, _ *rayv1.RayJob) (admission.Warnings, error) {
	return nil, nil
}
