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

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobframework/webhook"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/podset"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
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
	return webhook.WebhookManagedBy(mgr).
		For(obj).
		WithMutationHandler(admission.WithCustomDefaulter(mgr.GetScheme(), obj, wh)).
		WithValidator(wh).
		WithRoleTracker(options.RoleTracker).
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
	validationErrs, err := w.validateCreate(ctx, job)
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

		// Should not use auto scaler. Once the resources are reserved by queue the cluster should do its best to use them.
		if ptr.Deref(clusterSpec.EnableInTreeAutoscaling, false) && !workloadslicing.Enabled(job) {
			allErrors = append(allErrors, field.Invalid(clusterSpecPath.Child("enableInTreeAutoscaling"), clusterSpec.EnableInTreeAutoscaling, "a kueue managed job should only use autoscaling when workload slicing is enabled"))
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
		validationErrs, err := w.validateTopologyRequest(ctx, job)
		if err != nil {
			return nil, err
		}
		allErrors = append(allErrors, validationErrs...)
	}

	return allErrors, nil
}

func (w *RayJobWebhook) validateTopologyRequest(ctx context.Context, rayJob *rayv1.RayJob) (field.ErrorList, error) {
	var allErrs field.ErrorList
	if rayJob.Spec.RayClusterSpec == nil {
		return allErrs, nil
	}

	podSets, podSetsErr := jobframework.JobPodSets(ctx, (*RayJob)(rayJob))

	allErrs = append(allErrs, jobframework.ValidateTASPodSetRequest(headGroupMetaPath, &rayJob.Spec.RayClusterSpec.HeadGroupSpec.Template.ObjectMeta)...)

	if podSetsErr == nil {
		headGroupPodSetName := podset.FindPodSetByName(podSets, headGroupPodSetName)
		allErrs = append(allErrs, jobframework.ValidateSliceSizeAnnotationUpperBound(headGroupMetaPath, &rayJob.Spec.RayClusterSpec.HeadGroupSpec.Template.ObjectMeta, headGroupPodSetName)...)
		allErrs = append(allErrs, jobframework.ValidatePodSetGroupingTopology(podSets, buildPodSetAnnotationsPathByNameMap(rayJob))...)
	}

	for i, wgs := range rayJob.Spec.RayClusterSpec.WorkerGroupSpecs {
		workerGroupMetaPath := workerGroupSpecsPath.Index(i).Child("template", "metadata")
		allErrs = append(allErrs, jobframework.ValidateTASPodSetRequest(workerGroupMetaPath, &rayJob.Spec.RayClusterSpec.WorkerGroupSpecs[i].Template.ObjectMeta)...)

		if podSetsErr != nil {
			continue
		}

		workerPodSetName := podset.FindPodSetByName(podSets, kueue.NewPodSetReference(wgs.GroupName))
		allErrs = append(allErrs, jobframework.ValidateSliceSizeAnnotationUpperBound(workerGroupMetaPath, &rayJob.Spec.RayClusterSpec.WorkerGroupSpecs[i].Template.ObjectMeta, workerPodSetName)...)
	}

	if len(allErrs) > 0 {
		return allErrs, nil
	}

	return nil, podSetsErr
}

func buildPodSetAnnotationsPathByNameMap(rayJob *rayv1.RayJob) map[kueue.PodSetReference]*field.Path {
	podSetAnnotationsPathByName := make(map[kueue.PodSetReference]*field.Path)
	podSetAnnotationsPathByName[headGroupPodSetName] = headGroupMetaPath.Child("annotations")
	podSetAnnotationsPathByName[submitterJobPodSetName] = field.NewPath("spec", "submitterPodTemplate", "metadata", "annotations")
	for i, wgs := range rayJob.Spec.RayClusterSpec.WorkerGroupSpecs {
		podSetAnnotationsPathByName[kueue.PodSetReference(wgs.GroupName)] = workerGroupSpecsPath.Index(i).Child("template", "metadata", "annotations")
	}
	return podSetAnnotationsPathByName
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *RayJobWebhook) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	oldJob := oldObj.(*rayv1.RayJob)
	newJob := newObj.(*rayv1.RayJob)
	log := ctrl.LoggerFrom(ctx).WithName("rayjob-webhook")
	if w.manageJobsWithoutQueueName || jobframework.QueueName((*RayJob)(newJob)) != "" {
		log.Info("Validating update")
		allErrors := jobframework.ValidateJobOnUpdate((*RayJob)(oldJob), (*RayJob)(newJob), w.queues.DefaultLocalQueueExist)
		validationErrs, err := w.validateCreate(ctx, newJob)
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
