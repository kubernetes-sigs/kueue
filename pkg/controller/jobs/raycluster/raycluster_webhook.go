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

package raycluster

import (
	"context"
	"fmt"
	"slices"

	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	kueuebeta "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobframework/webhook"
	"sigs.k8s.io/kueue/pkg/features"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
	"sigs.k8s.io/kueue/pkg/util/podset"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
)

var (
	headGroupSpecsPath   = field.NewPath("spec", "headGroupSpec")
	headGroupMetaPath    = headGroupSpecsPath.Child("template, metadata")
	workerGroupSpecsPath = field.NewPath("spec", "workerGroupSpecs")
)

type RayClusterWebhook struct {
	client                       client.Client
	queues                       *qcache.Manager
	manageJobsWithoutQueueName   bool
	managedJobsNamespaceSelector labels.Selector
	cache                        *schdcache.Cache
}

// SetupRayClusterWebhook configures the webhook for rayv1 RayCluster.
func SetupRayClusterWebhook(mgr ctrl.Manager, opts ...jobframework.Option) error {
	options := jobframework.ProcessOptions(opts...)
	for _, opt := range opts {
		opt(&options)
	}
	wh := &RayClusterWebhook{
		client:                       mgr.GetClient(),
		queues:                       options.Queues,
		manageJobsWithoutQueueName:   options.ManageJobsWithoutQueueName,
		managedJobsNamespaceSelector: options.ManagedJobsNamespaceSelector,
		cache:                        options.Cache,
	}
	obj := &rayv1.RayCluster{}
	return webhook.WebhookManagedBy(mgr).
		For(obj).
		WithMutationHandler(admission.WithCustomDefaulter(mgr.GetScheme(), obj, wh)).
		WithValidator(wh).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-ray-io-v1-raycluster,mutating=true,failurePolicy=fail,sideEffects=None,groups=ray.io,resources=rayclusters,verbs=create,versions=v1,name=mraycluster.kb.io,admissionReviewVersions=v1

var _ admission.CustomDefaulter = &RayClusterWebhook{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the type
func (w *RayClusterWebhook) Default(ctx context.Context, obj runtime.Object) error {
	job := fromObject(obj)
	log := ctrl.LoggerFrom(ctx).WithName("raycluster-webhook")
	log.V(10).Info("Applying defaults")
	jobframework.ApplyDefaultLocalQueue(job.Object(), w.queues.DefaultLocalQueueExist)
	if err := jobframework.ApplyDefaultForSuspend(ctx, job, w.client, w.manageJobsWithoutQueueName, w.managedJobsNamespaceSelector); err != nil {
		return err
	}
	jobframework.ApplyDefaultForManagedBy(job, w.queues, w.cache, log)

	rjob := obj.(*rayv1.RayCluster)

	if isAnElasticJob(rjob) {
		// Ensure that the PodSchedulingGate is present in the RayCluster's pod Templates for its Head and all its Workers
		utilpod.GateTemplate(&job.Spec.HeadGroupSpec.Template, kueuebeta.ElasticJobSchedulingGate)

		for index := range job.Spec.WorkerGroupSpecs {
			wgs := &job.Spec.WorkerGroupSpecs[index]

			utilpod.GateTemplate(&wgs.Template, kueuebeta.ElasticJobSchedulingGate)
		}
	}

	return nil
}

// +kubebuilder:webhook:path=/validate-ray-io-v1-raycluster,mutating=false,failurePolicy=fail,sideEffects=None,groups=ray.io,resources=rayclusters,verbs=create;update,versions=v1,name=vraycluster.kb.io,admissionReviewVersions=v1

var _ admission.CustomValidator = &RayClusterWebhook{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *RayClusterWebhook) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	job := obj.(*rayv1.RayCluster)
	log := ctrl.LoggerFrom(ctx).WithName("raycluster-webhook")
	log.V(10).Info("Validating create")
	validationErrs, err := w.validateCreate(job)
	if err != nil {
		return nil, err
	}
	return nil, validationErrs.ToAggregate()
}

// returns whether the RayCluster is an elastic job or not
func isAnElasticJob(job *rayv1.RayCluster) bool {
	return features.Enabled(features.ElasticJobsViaWorkloadSlices) && workloadslicing.Enabled(job.GetObjectMeta())
}

func (w *RayClusterWebhook) validateCreate(job *rayv1.RayCluster) (field.ErrorList, error) {
	var allErrors field.ErrorList
	kueueJob := (*RayCluster)(job)

	if w.manageJobsWithoutQueueName || jobframework.QueueName(kueueJob) != "" {
		spec := &job.Spec
		specPath := field.NewPath("spec")

		if isAnElasticJob(job) {
			allErrors = append(allErrors, validateElasticJob(job)...)
		} else if ptr.Deref(spec.EnableInTreeAutoscaling, false) {
			// Should not use auto scaler. Once the resources are reserved by queue the cluster should do its best to use them.
			allErrors = append(allErrors, field.Invalid(specPath.Child("enableInTreeAutoscaling"), spec.EnableInTreeAutoscaling, "a kueue managed job can use autoscaling only when the ElasticJobsViaWorkloadSlices feature gate is on and the job is an elastic job"))
		}

		// Should limit the worker count to 8 - 1 (max podSets num - cluster head)
		if len(spec.WorkerGroupSpecs) > 7 {
			allErrors = append(allErrors, field.TooMany(specPath.Child("workerGroupSpecs"), len(spec.WorkerGroupSpecs), 7))
		}

		// None of the workerGroups should be named "head"
		for i := range spec.WorkerGroupSpecs {
			if spec.WorkerGroupSpecs[i].GroupName == headGroupPodSetName {
				allErrors = append(allErrors, field.Forbidden(specPath.Child("workerGroupSpecs").Index(i).Child("groupName"), fmt.Sprintf("%q is reserved for the head group", headGroupPodSetName)))
			}
		}
	}

	allErrors = append(allErrors, jobframework.ValidateJobOnCreate(kueueJob)...)
	if features.Enabled(features.TopologyAwareScheduling) {
		validationErrs, err := w.validateTopologyRequest(kueueJob)
		if err != nil {
			return nil, err
		}
		allErrors = append(allErrors, validationErrs...)
	}

	return allErrors, nil
}

func validateElasticJob(job *rayv1.RayCluster) field.ErrorList {
	allErrors := field.ErrorList{}

	specPath := field.NewPath("spec")

	workloadSliceSchedulingGate := corev1.PodSchedulingGate{
		Name: kueuebeta.ElasticJobSchedulingGate,
	}

	for index := range job.Spec.WorkerGroupSpecs {
		wgs := &job.Spec.WorkerGroupSpecs[index]

		if !slices.Contains(wgs.Template.Spec.SchedulingGates, workloadSliceSchedulingGate) {
			allErrors = append(allErrors, field.Invalid(specPath.Child("workerGroupSpecs").Index(index).Child("template").Child("spec").Child("schedulingGates"), wgs.Template.Spec.SchedulingGates, "an elastic job must have the ElasticJobSchedulingGate"))
		}
	}

	if !slices.Contains(job.Spec.HeadGroupSpec.Template.Spec.SchedulingGates, workloadSliceSchedulingGate) {
		allErrors = append(allErrors, field.Invalid(specPath.Child("headGroupSpec").Child("template").Child("spec").Child("schedulingGates"), job.Spec.HeadGroupSpec.Template.Spec.SchedulingGates, "an elastic job must have the ElasticJobSchedulingGate"))
	}

	return allErrors
}

func (w *RayClusterWebhook) validateTopologyRequest(rayJob *RayCluster) (field.ErrorList, error) {
	var allErrs field.ErrorList

	podSets, podSetsErr := rayJob.PodSets()

	allErrs = append(allErrs, jobframework.ValidateTASPodSetRequest(headGroupMetaPath, &rayJob.Spec.HeadGroupSpec.Template.ObjectMeta)...)

	if podSetsErr == nil {
		headGroupPodSet := podset.FindPodSetByName(podSets, headGroupPodSetName)
		allErrs = append(allErrs, jobframework.ValidateSliceSizeAnnotationUpperBound(headGroupMetaPath, &rayJob.Spec.HeadGroupSpec.Template.ObjectMeta, headGroupPodSet)...)
	}

	for i, wgs := range rayJob.Spec.WorkerGroupSpecs {
		workerGroupMetaPath := workerGroupSpecsPath.Index(i).Child("template", "metadata")
		allErrs = append(allErrs, jobframework.ValidateTASPodSetRequest(workerGroupMetaPath, &rayJob.Spec.WorkerGroupSpecs[i].Template.ObjectMeta)...)

		if podSetsErr != nil {
			continue
		}

		podSet := podset.FindPodSetByName(podSets, kueuebeta.NewPodSetReference(wgs.GroupName))
		allErrs = append(allErrs, jobframework.ValidateSliceSizeAnnotationUpperBound(workerGroupMetaPath, &rayJob.Spec.WorkerGroupSpecs[i].Template.ObjectMeta, podSet)...)
	}

	if len(allErrs) > 0 {
		return allErrs, nil
	}

	return nil, podSetsErr
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *RayClusterWebhook) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	oldJob := oldObj.(*rayv1.RayCluster)
	newJob := newObj.(*rayv1.RayCluster)
	log := ctrl.LoggerFrom(ctx).WithName("raycluster-webhook")
	if w.manageJobsWithoutQueueName || jobframework.QueueName((*RayCluster)(newJob)) != "" {
		log.Info("Validating update")
		allErrors := jobframework.ValidateJobOnUpdate((*RayCluster)(oldJob), (*RayCluster)(newJob), w.queues.DefaultLocalQueueExist)
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
func (w *RayClusterWebhook) ValidateDelete(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	return nil, nil
}
