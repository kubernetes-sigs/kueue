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
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobframework/webhook"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/queue"
	"sigs.k8s.io/kueue/pkg/util/podset"

	kueue_constants "sigs.k8s.io/kueue/pkg/constants"
)

var (
	headGroupSpecsPath   = field.NewPath("spec", "headGroupSpec")
	headGroupMetaPath    = headGroupSpecsPath.Child("template, metadata")
	workerGroupSpecsPath = field.NewPath("spec", "workerGroupSpecs")
)

type RayClusterWebhook struct {
	client                       client.Client
	queues                       *queue.Manager
	manageJobsWithoutQueueName   bool
	managedJobsNamespaceSelector labels.Selector
	cache                        *cache.Cache
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

	// If the AutoScaler is off then there's nothing left to do here
	if ptr.Deref(job.Spec.EnableInTreeAutoscaling, false) == false {
		return nil
	}
	// However, if the AutoScaler is on, propagate the queue name of the RayCluster object to the WorkGroupSpecs which do not define one
	// First, find the queue name of the RayCluster object, if there's none bail out

	labels := job.GetLabels()

	if labels == nil || labels[constants.QueueLabel] == "" {
		return nil
	}

	w.applyDefaultQueueForWorkerGroup(job, labels[constants.QueueLabel])
	return nil
}

// Updates WorkerGroupSpecs that do not have a queue-name with a default one
func (w *RayClusterWebhook) applyDefaultQueueForWorkerGroup(job *RayCluster, queueName string) {
	for i := range job.Spec.WorkerGroupSpecs {
		wg := &job.Spec.WorkerGroupSpecs[i]
		labels := wg.Template.ObjectMeta.GetLabels()

		if labels == nil {
			labels = make(map[string]string)
		}

		if labels[constants.QueueLabel] == "" {
			labels[constants.QueueLabel] = queueName
		}

		// Setting this asks the reconciler to create a Workload object for this k8s pod. This is done via the Skip() method of the associated kueue Pod struct which returns False when the k8s pod has a correct managed label
		labels[kueue_constants.ManagedByKueueLabelKey] = kueue_constants.ManagedByKueueLabelValue

		wg.Template.ObjectMeta.SetLabels(labels)
	}
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

func (w *RayClusterWebhook) validateCreate(job *rayv1.RayCluster) (field.ErrorList, error) {
	var allErrors field.ErrorList
	kueueJob := (*RayCluster)(job)
	rayClusterQueueName := string(jobframework.QueueName(kueueJob))

	if w.manageJobsWithoutQueueName || rayClusterQueueName != "" {
		spec := &job.Spec
		specPath := field.NewPath("spec")

		// TODO revisit once Support dynamically sized (elastic) jobs #77 is implemented
		if ptr.Deref(spec.EnableInTreeAutoscaling, false) == false {
			// if the AutoScaler is disabled, then WorkerGroupSpecs must NOT point to a queue
			for i, wg := range kueueJob.Spec.WorkerGroupSpecs {
				if wg.Template.Labels[constants.QueueLabel] != "" {
					msg := fmt.Sprintf("a kueue managed raycluster without autoscaling should not set a %s label in its workers", constants.QueueLabel)
					allErrors = append(allErrors, field.Invalid(workerGroupSpecsPath.Index(i).Child("Template", "Labels", constants.QueueLabel), wg.Template.Labels[constants.QueueLabel], msg))
				}
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
		} else {
			// if the AutoScaler is enabled and Kueue does not manage pods without QueueLabels, then all Worker pods must have the same QueueLabel as the RayCluster object
			for i, wg := range kueueJob.Spec.WorkerGroupSpecs {
				if !w.manageJobsWithoutQueueName && wg.Template.Labels[constants.QueueLabel] != rayClusterQueueName {
					msg := fmt.Sprintf("a kueue managed raycluster with a queue and autoscaling should set the same %s label for its workers", constants.QueueLabel)
					allErrors = append(allErrors, field.Invalid(workerGroupSpecsPath.Index(i).Child("Template", "Labels", constants.QueueLabel), wg.Template.Labels[constants.QueueLabel], msg))
				}
			}
		}

		// The head node PodSet is accounted for in the RayCluster object regardless of whether AutoScaling is enabled or not.
		// Therefore, the pod representing the head node must NOT have a QueueLabel pointing to a LocalQueue at all
		if job.Spec.HeadGroupSpec.Template.Labels[constants.QueueLabel] != "" {
			msg := fmt.Sprintf("a kueue managed raycluster should not set a %s label in its head node", constants.QueueLabel)
			allErrors = append(allErrors, field.Invalid(headGroupSpecsPath.Child("Template", "Labels", constants.QueueLabel), job.Spec.HeadGroupSpec.Template.Labels[constants.QueueLabel], msg))
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
