package rayservice

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
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobframework/webhook"
	"sigs.k8s.io/kueue/pkg/queue"
)

var (
	headGroupSpecsPath   = field.NewPath("spec", "headGroupSpec")
	headGroupMetaPath    = headGroupSpecsPath.Child("template, metadata")
	workerGroupSpecsPath = field.NewPath("spec", "workerGroupSpecs")
)

type RayServiceWebhook struct {
	client                       client.Client
	queues                       *queue.Manager
	manageJobsWithoutQueueName   bool
	managedJobsNamespaceSelector labels.Selector
	cache                        *cache.Cache
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
	return webhook.WebhookManagedBy(mgr).
		For(obj).
		WithMutationHandler(admission.WithCustomDefaulter(mgr.GetScheme(), obj, wh)).
		WithValidator(wh).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-ray-io-v1-rayservice,mutating=true,failurePolicy=fail,sideEffects=None,groups=ray.io,resources=rayservices,verbs=create,versions=v1,name=mrayservice.kb.io,admissionReviewVersions=v1

var _ admission.CustomDefaulter = &RayServiceWebhook{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the type
func (w *RayServiceWebhook) Default(ctx context.Context, obj runtime.Object) error {
	job := fromObject(obj)
	log := ctrl.LoggerFrom(ctx).WithName("rayservice-webhook")
	log.V(10).Info("Applying defaults")
	jobframework.ApplyDefaultLocalQueue(job.Object(), w.queues.DefaultLocalQueueExist)
	if err := jobframework.ApplyDefaultForSuspend(ctx, job, w.client, w.manageJobsWithoutQueueName, w.managedJobsNamespaceSelector); err != nil {
		return err
	}
	jobframework.ApplyDefaultForManagedBy(job, w.queues, w.cache, log)
	return nil
}

// +kubebuilder:webhook:path=/validate-ray-io-v1-rayservice,mutating=false,failurePolicy=fail,sideEffects=None,groups=ray.io,resources=rayservices,verbs=create;update,versions=v1,name=vrayservice.kb.io,admissionReviewVersions=v1

var _ admission.CustomValidator = &RayServiceWebhook{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *RayServiceWebhook) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	job := obj.(*rayv1.RayService)
	log := ctrl.LoggerFrom(ctx).WithName("rayserivce-webhook")
	log.V(10).Info("Validating create")
	return nil, w.validateCreate(job).ToAggregate()
}

func (w *RayServiceWebhook) validateCreate(job *rayv1.RayService) field.ErrorList {
	var allErrors field.ErrorList
	kueueJob := (*RayService)(job)

	if w.manageJobsWithoutQueueName || jobframework.QueueName(kueueJob) != "" {
		spec := &job.Spec
		specPath := field.NewPath("spec")

		if ptr.Deref(spec.RayClusterSpec.EnableInTreeAutoscaling, false) {
			allErrors = append(allErrors, field.Invalid(specPath.Child("enableInTreeAutoscaling"), spec.RayClusterSpec.EnableInTreeAutoscaling, "a kueue managed job should not use autoscaling"))
		}

		// Should limit the worker count to 8 - 1 (max podSets num - cluster head)
		if len(spec.RayClusterSpec.WorkerGroupSpecs) > 7 {
			allErrors = append(allErrors, field.TooMany(specPath.Child("workerGroupSpecs"), len(spec.RayClusterSpec.WorkerGroupSpecs), 7))
		}

		// None of the workerGroups should be named "head"
		for i := range spec.RayClusterSpec.WorkerGroupSpecs {
			if spec.RayClusterSpec.WorkerGroupSpecs[i].GroupName == headGroupPodSetName {
				allErrors = append(allErrors, field.Forbidden(specPath.Child("workerGroupSpecs").Index(i).Child("groupName"), fmt.Sprintf("%q is reserved for the head group", headGroupPodSetName)))
			}
		}
	}

	allErrors = append(allErrors, jobframework.ValidateJobOnCreate(kueueJob)...)
	allErrors = append(allErrors, w.validateTopologyRequest(kueueJob)...)

	return allErrors
}

func (w *RayServiceWebhook) validateTopologyRequest(rayService *RayService) field.ErrorList {
	var allErrs field.ErrorList
	allErrs = append(allErrs, jobframework.ValidateTASPodSetRequest(headGroupMetaPath, &rayService.Spec.RayClusterSpec.HeadGroupSpec.Template.ObjectMeta)...)
	for i := range rayService.Spec.RayClusterSpec.WorkerGroupSpecs {
		workerGroupMetaPath := workerGroupSpecsPath.Index(i).Child("template", "metadata")
		allErrs = append(allErrs, jobframework.ValidateTASPodSetRequest(workerGroupMetaPath, &rayService.Spec.RayClusterSpec.WorkerGroupSpecs[i].Template.ObjectMeta)...)
	}
	return allErrs
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *RayServiceWebhook) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	oldJob := oldObj.(*rayv1.RayService)
	newJob := newObj.(*rayv1.RayService)
	log := ctrl.LoggerFrom(ctx).WithName("rayservice-webhook")
	if w.manageJobsWithoutQueueName || jobframework.QueueName((*RayService)(newJob)) != "" {
		log.Info("Validating update")
		allErrors := jobframework.ValidateJobOnUpdate((*RayService)(oldJob), (*RayService)(newJob), w.queues.DefaultLocalQueueExist)
		allErrors = append(allErrors, w.validateCreate(newJob)...)
		return nil, allErrors.ToAggregate()
	}
	return nil, nil
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type
func (w *RayServiceWebhook) ValidateDelete(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	return nil, nil
}
