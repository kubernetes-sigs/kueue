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

package rayservice

import (
	"context"

	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/raycluster"
	"sigs.k8s.io/kueue/pkg/features"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
	"sigs.k8s.io/kueue/pkg/util/webhook"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
)

var (
	headGroupSpecsPath   = field.NewPath("spec", "rayClusterSpec", "headGroupSpec")
	headGroupMetaPath    = headGroupSpecsPath.Child("template", "metadata")
	workerGroupSpecsPath = field.NewPath("spec", "rayClusterSpec", "workerGroupSpecs")
)

type RayServiceWebhook struct {
	client                       client.Client
	queues                       *qcache.Manager
	manageJobsWithoutQueueName   bool
	managedJobsNamespaceSelector labels.Selector
	cache                        *schdcache.Cache
}

func fromObject(obj runtime.Object) *RayService {
	return (*RayService)(obj.(*rayv1.RayService))
}

// SetupRayServiceWebhook configures the webhook for rayv1 RayService.
func SetupRayServiceWebhook(mgr ctrl.Manager, opts ...jobframework.Option) error {
	options := jobframework.ProcessOptions(opts...)
	for _, opt := range opts {
		opt(&options)
	}
	wh := &RayServiceWebhook{
		client:                       mgr.GetClient(),
		queues:                       options.Queues,
		manageJobsWithoutQueueName:   options.ManageJobsWithoutQueueName,
		managedJobsNamespaceSelector: options.ManagedJobsNamespaceSelector,
		cache:                        options.Cache,
	}
	obj := &rayv1.RayService{}
	if options.NoopWebhook {
		return webhook.SetupNoopWebhook(mgr, obj)
	}
	return ctrl.NewWebhookManagedBy(mgr, obj).
		WithDefaulter(wh).
		WithValidator(wh).
		WithLogConstructor(jobframework.WebhookLogConstructor(fromObject(obj).GVK(), options.RoleTracker)).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-ray-io-v1-rayservice,mutating=true,failurePolicy=fail,sideEffects=None,groups=ray.io,resources=rayservices,verbs=create,versions=v1,name=mrayservice.kb.io,admissionReviewVersions=v1

var _ admission.Defaulter[*rayv1.RayService] = &RayServiceWebhook{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the type
func (w *RayServiceWebhook) Default(ctx context.Context, obj *rayv1.RayService) error {
	job := fromObject(obj)
	log := ctrl.LoggerFrom(ctx).WithName("rayservice-webhook")
	log.V(10).Info("Applying defaults")
	jobframework.ApplyDefaultLocalQueue(job.Object(), w.queues.DefaultLocalQueueExist)
	if err := jobframework.ApplyDefaultForSuspend(ctx, job, w.client, w.manageJobsWithoutQueueName, w.managedJobsNamespaceSelector); err != nil {
		return err
	}
	jobframework.ApplyDefaultForManagedBy(job, w.queues, w.cache, log)

	if isAnElasticJob(obj) {
		// Ensure that the PodSchedulingGate is present in the RayService's pod Templates for its Head and all its Workers
		utilpod.GateTemplate(&job.Spec.RayClusterSpec.HeadGroupSpec.Template, kueue.ElasticJobSchedulingGate)

		for index := range job.Spec.RayClusterSpec.WorkerGroupSpecs {
			wgs := &job.Spec.RayClusterSpec.WorkerGroupSpecs[index]

			utilpod.GateTemplate(&wgs.Template, kueue.ElasticJobSchedulingGate)
		}
	}

	return nil
}

// +kubebuilder:webhook:path=/validate-ray-io-v1-rayservice,mutating=false,failurePolicy=fail,sideEffects=None,groups=ray.io,resources=rayservices,verbs=create;update,versions=v1,name=vrayservice.kb.io,admissionReviewVersions=v1

var _ admission.Validator[*rayv1.RayService] = &RayServiceWebhook{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *RayServiceWebhook) ValidateCreate(ctx context.Context, obj *rayv1.RayService) (admission.Warnings, error) {
	log := ctrl.LoggerFrom(ctx).WithName("rayservice-webhook")
	log.V(10).Info("Validating create")
	validationErrs, err := w.validateCreate(ctx, obj)
	if err != nil {
		return nil, err
	}
	return nil, validationErrs.ToAggregate()
}

// returns whether the RayService is an elastic job or not
func isAnElasticJob(job *rayv1.RayService) bool {
	return features.Enabled(features.ElasticJobsViaWorkloadSlices) && workloadslicing.Enabled(job.GetObjectMeta())
}

func (w *RayServiceWebhook) validateCreate(ctx context.Context, job *rayv1.RayService) (field.ErrorList, error) {
	var allErrors field.ErrorList
	kueueJob := (*RayService)(job)

	if w.manageJobsWithoutQueueName || jobframework.QueueName(kueueJob) != "" {
		spec := &job.Spec
		specPath := field.NewPath("spec")

		clusterSpec := &spec.RayClusterSpec
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

func (w *RayServiceWebhook) validateTopologyRequest(ctx context.Context, rayService *rayv1.RayService) (field.ErrorList, error) {
	job := (*RayService)(rayService)
	return raycluster.ValidateTopologyRequest(ctx, job, &rayService.Spec.RayClusterSpec, headGroupMetaPath, workerGroupSpecsPath)
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *RayServiceWebhook) ValidateUpdate(ctx context.Context, oldObj, newObj *rayv1.RayService) (admission.Warnings, error) {
	oldJob := fromObject(oldObj)
	newJob := fromObject(newObj)
	log := ctrl.LoggerFrom(ctx).WithName("rayservice-webhook")
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
func (w *RayServiceWebhook) ValidateDelete(ctx context.Context, obj *rayv1.RayService) (admission.Warnings, error) {
	return nil, nil
}
