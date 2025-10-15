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

package sparkapplication

import (
	"context"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobframework/webhook"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/podset"
	"sigs.k8s.io/kueue/pkg/workloadslicing"

	sparkv1beta2 "github.com/kubeflow/spark-operator/v2/api/v1beta2"
)

var (
	specPath                       = field.NewPath("spec")
	dynamicAllocationPath          = specPath.Child("dynamicAllocation")
	driverSpecPath                 = specPath.Child("driver")
	driverSpecTemplatePath         = driverSpecPath.Child("template")
	driverSpecSchedulingGatePath   = driverSpecPath.Child("template").Child("spec").Child("schedulingGates")
	executorSpecPath               = specPath.Child("executor")
	executorSpecTemplatePath       = executorSpecPath.Child("template")
	executorSpecSchedulingGatePath = executorSpecPath.Child("template").Child("spec").Child("schedulingGates")
	elasticJobEnabledPath          = field.NewPath("metadata", "annotations").Key(workloadslicing.EnabledAnnotationKey)
)

type SparkApplicationWebhook struct {
	client                       client.Client
	queues                       *qcache.Manager
	manageJobsWithoutQueueName   bool
	managedJobsNamespaceSelector labels.Selector
	cache                        *schdcache.Cache
}

func setupSparkApplicationWebhook(mgr ctrl.Manager, opts ...jobframework.Option) error {
	options := jobframework.ProcessOptions(opts...)
	for _, opt := range opts {
		opt(&options)
	}
	wh := &SparkApplicationWebhook{
		client:                       mgr.GetClient(),
		queues:                       options.Queues,
		manageJobsWithoutQueueName:   options.ManageJobsWithoutQueueName,
		managedJobsNamespaceSelector: options.ManagedJobsNamespaceSelector,
		cache:                        options.Cache,
	}
	obj := &sparkv1beta2.SparkApplication{}
	return webhook.WebhookManagedBy(mgr).
		For(obj).
		WithMutationHandler(admission.WithCustomDefaulter(mgr.GetScheme(), obj, wh)).
		WithValidator(wh).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-sparkoperator-k8s-io-v1beta2-sparkapplication,mutating=true,failurePolicy=fail,sideEffects=None,groups=sparkoperator.k8s.io,resources=sparkapplications,verbs=create,versions=v1beta2,name=msparkapplication.kb.io,admissionReviewVersions=v1

var _ admission.CustomDefaulter = &SparkApplicationWebhook{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the type
func (w *SparkApplicationWebhook) Default(ctx context.Context, obj runtime.Object) error {
	job := FromObject(obj)
	log := ctrl.LoggerFrom(ctx).WithName("sparkapplication-webhook")
	log.V(10).Info("Applying defaults")

	jobframework.ApplyDefaultLocalQueue(job.Object(), w.queues.DefaultLocalQueueExist)
	if err := jobframework.ApplyDefaultForSuspend(ctx, job, w.client, w.manageJobsWithoutQueueName, w.managedJobsNamespaceSelector); err != nil {
		return err
	}

	if w.manageJobsWithoutQueueName || jobframework.QueueName(job) != "" {
		// All the labels in SparkApplication will be copied into spark pods by default.
		// So, we need to exclude the queue label from being copied to the spaark pods.
		// NOTE: Spark operator puts OwnerReference to the spark driver pod IN WEBHOOK
		//       Thus, Kueue's plain pod webhook might see the spark driver pod with queue label
		//       but no SparkApplication OwnerReference. This will cause to activate plain pod
		//       integration.
		noMutateQueueLabelToSparkPods := sparkv1beta2.MutatingLabelKeyMatchCondition{
			Fixed: ptr.To(constants.QueueLabel), Invert: true,
		}

		labelsMutationForDriverPod := job.Spec.Driver.SparkApplicationLabelsMutation
		if labelsMutationForDriverPod == nil {
			job.Spec.Driver.SparkApplicationLabelsMutation = &sparkv1beta2.SparkApplicationLabelsMutationSpec{
				LabelKeyMatches: []sparkv1beta2.MutatingLabelKeyMatchCondition{},
			}
		}
		job.Spec.Driver.SparkApplicationLabelsMutation.LabelKeyMatches = append(
			job.Spec.Driver.SparkApplicationLabelsMutation.LabelKeyMatches,
			noMutateQueueLabelToSparkPods,
		)

		labelsMutationForExecutorPod := job.Spec.Executor.SparkApplicationLabelsMutation
		if labelsMutationForExecutorPod == nil {
			job.Spec.Executor.SparkApplicationLabelsMutation = &sparkv1beta2.SparkApplicationLabelsMutationSpec{
				LabelKeyMatches: []sparkv1beta2.MutatingLabelKeyMatchCondition{},
			}
		}
		job.Spec.Executor.SparkApplicationLabelsMutation.LabelKeyMatches = append(
			job.Spec.Executor.SparkApplicationLabelsMutation.LabelKeyMatches,
			noMutateQueueLabelToSparkPods,
		)
	}

	return nil
}

// +kubebuilder:webhook:path=/validate-sparkoperator-k8s-io-v1beta2-sparkapplication,mutating=false,failurePolicy=fail,sideEffects=None,groups=sparkoperator.k8s.io,resources=sparkapplications,verbs=create;update,versions=v1beta2,name=vsparkapplication.kb.io,admissionReviewVersions=v1

var _ admission.CustomValidator = &SparkApplicationWebhook{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *SparkApplicationWebhook) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	job := obj.(*sparkv1beta2.SparkApplication)
	log := ctrl.LoggerFrom(ctx).WithName("sparkapplication-webhook")
	log.V(10).Info("Validating create")
	validationErrs, err := w.validateCreate(ctx, job)
	if err != nil {
		return nil, err
	}
	return nil, validationErrs.ToAggregate()
}

// returns whether the SparkApplication is an elastic job or not
func isAnElasticJob(sparkApp *sparkv1beta2.SparkApplication) bool {
	return workloadslicing.Enabled(sparkApp)
}

func (w *SparkApplicationWebhook) validateCreate(ctx context.Context, job *sparkv1beta2.SparkApplication) (field.ErrorList, error) {
	var allErrors field.ErrorList
	kueueJob := (*SparkApplication)(job)

	if w.manageJobsWithoutQueueName || jobframework.QueueName(kueueJob) != "" {
		spec := &job.Spec

		if spec.Mode != sparkv1beta2.DeployModeCluster {
			allErrors = append(allErrors, field.Invalid(specPath.Child("mode"), spec.Mode, "only Cluster mode is supported for a kueue managed job"))
		}

		if isAnElasticJob(job) {
			allErrors = append(allErrors,
				field.Invalid(elasticJobEnabledPath, workloadslicing.EnabledAnnotationValue, "elastic job is not supported in SparkApplication"),
			)
		} else if ptr.Deref(spec.DynamicAllocation, sparkv1beta2.DynamicAllocation{}).Enabled {
			allErrors = append(allErrors,
				field.Invalid(dynamicAllocationPath,
					ptr.Deref(spec.DynamicAllocation, sparkv1beta2.DynamicAllocation{}).Enabled,
					"a kueue managed job can use dynamicAllocation only when the ElasticJobsViaWorkloadSlices feature gate is on and the job is an elastic job",
				),
			)
		}
	}

	allErrors = append(allErrors, jobframework.ValidateJobOnCreate(kueueJob)...)
	if features.Enabled(features.TopologyAwareScheduling) {
		validationErrs, err := w.validateTopologyRequest(ctx, kueueJob)
		if err != nil {
			return nil, err
		}
		allErrors = append(allErrors, validationErrs...)
	}

	return allErrors, nil
}

func (w *SparkApplicationWebhook) validateTopologyRequest(ctx context.Context, sparkApp *SparkApplication) (field.ErrorList, error) {
	var allErrs field.ErrorList

	podSets, podSetsErr := sparkApp.PodSets(ctx)

	if podSetsErr == nil {
		driverPodSet := podset.FindPodSetByName(podSets, driverPodSetName)
		allErrs = append(allErrs, jobframework.ValidateTASPodSetRequest(driverSpecPath, &driverPodSet.Template.ObjectMeta)...)
		allErrs = append(allErrs, jobframework.ValidateSliceSizeAnnotationUpperBound(driverSpecPath, &driverPodSet.Template.ObjectMeta, driverPodSet)...)

		executorPodSet := podset.FindPodSetByName(podSets, executorPodSetName)
		allErrs = append(allErrs, jobframework.ValidateTASPodSetRequest(executorSpecPath, &executorPodSet.Template.ObjectMeta)...)
		allErrs = append(allErrs, jobframework.ValidateSliceSizeAnnotationUpperBound(executorSpecPath, &executorPodSet.Template.ObjectMeta, executorPodSet)...)
	}

	if len(allErrs) > 0 {
		return allErrs, nil
	}

	return nil, podSetsErr
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *SparkApplicationWebhook) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	oldSparkApp := oldObj.(*sparkv1beta2.SparkApplication)
	newSparkApp := newObj.(*sparkv1beta2.SparkApplication)
	log := ctrl.LoggerFrom(ctx).WithName("sparkapplication-webhook")
	if w.manageJobsWithoutQueueName || jobframework.QueueName(FromObject(newSparkApp)) != "" {
		log.Info("Validating update")
		allErrors := jobframework.ValidateJobOnUpdate(FromObject(oldSparkApp), FromObject(newSparkApp), w.queues.DefaultLocalQueueExist)
		validationErrs, err := w.validateCreate(ctx, newSparkApp)
		if err != nil {
			return nil, err
		}
		allErrors = append(allErrors, validationErrs...)
		return nil, allErrors.ToAggregate()
	}
	return nil, nil
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type
func (w *SparkApplicationWebhook) ValidateDelete(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	return nil, nil
}
