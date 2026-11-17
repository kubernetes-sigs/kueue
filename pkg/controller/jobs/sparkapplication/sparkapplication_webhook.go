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
	"fmt"
	"math"
	"slices"
	"strconv"

	sparkv1beta2 "github.com/kubeflow/spark-operator/v2/api/v1beta2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	jobframeworktas "sigs.k8s.io/kueue/pkg/controller/jobframework/tas"
	"sigs.k8s.io/kueue/pkg/features"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
	"sigs.k8s.io/kueue/pkg/util/podset"
	"sigs.k8s.io/kueue/pkg/util/webhook"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
)

var (
	specPath                                        = field.NewPath("spec")
	dynamicAllocationEnabledPath                    = specPath.Child("dynamicAllocation").Child("enabled")
	driverSpecPath                                  = specPath.Child("driver")
	executorSpecPath                                = specPath.Child("executor")
	elasticJobAnnotationPath                        = field.NewPath("metadata", "annotations").Key(workloadslicing.EnabledAnnotationKey)
	partialPreemptionAnnotationPath                 = field.NewPath("metadata", "annotations").Key(constants.PartialPreemptionAnnotation)
	partialPreemptionMinRetainedCountAnnotationPath = executorSpecPath.Child("annotations").Key(constants.PodSetPartialPreemptionMinRetainedCountAnnotation)
)

type SparkApplicationWebhook struct {
	integrationManager           *jobframework.IntegrationManager
	client                       client.Client
	queues                       *qcache.Manager
	manageJobsWithoutQueueName   bool
	managedJobsNamespaceSelector labels.Selector
	cache                        *schdcache.Cache
	maxTimeoutOnWorkload         *metav1.Duration
}

func SetupWebhook(mgr ctrl.Manager, opts ...jobframework.Option) error {
	options := jobframework.ProcessOptions(opts...)
	wh := &SparkApplicationWebhook{
		integrationManager:           options.IntegrationManager,
		client:                       mgr.GetClient(),
		queues:                       options.Queues,
		manageJobsWithoutQueueName:   options.ManageJobsWithoutQueueName,
		managedJobsNamespaceSelector: options.ManagedJobsNamespaceSelector,
		cache:                        options.Cache,
		maxTimeoutOnWorkload:         options.MaxTimeoutOnWorkload,
	}
	obj := &sparkv1beta2.SparkApplication{}
	if options.NoopWebhook {
		return webhook.SetupNoopWebhook(mgr, obj)
	}
	return ctrl.NewWebhookManagedBy(mgr, obj).
		WithValidator(wh).
		WithDefaulter(wh).
		WithLogConstructor(jobframework.WebhookLogConstructor(fromObject(obj).GVK(), options.RoleTracker)).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-sparkoperator-k8s-io-v1beta2-sparkapplication,mutating=true,failurePolicy=fail,sideEffects=None,groups=sparkoperator.k8s.io,resources=sparkapplications,verbs=create,versions=v1beta2,name=msparkapplication.kb.io,admissionReviewVersions=v1

var _ admission.Defaulter[*sparkv1beta2.SparkApplication] = &SparkApplicationWebhook{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the type
func (w *SparkApplicationWebhook) Default(ctx context.Context, obj *sparkv1beta2.SparkApplication) error {
	job := fromObject(obj)
	log := ctrl.LoggerFrom(ctx).WithName("sparkapplication-webhook")
	log.V(5).Info("Applying defaults")

	if err := w.integrationManager.ApplyDefaultLocalQueue(ctx, w.client, job.Object(), w.queues.DefaultLocalQueueExist, w.managedJobsNamespaceSelector); err != nil {
		return err
	}
	if err := w.integrationManager.ApplyDefaultWorkloadPriorityClass(ctx, w.client, job.Object(), w.managedJobsNamespaceSelector); err != nil {
		return err
	}
	if err := w.integrationManager.ApplyDefaultForSuspend(ctx, job, w.client, w.manageJobsWithoutQueueName, w.managedJobsNamespaceSelector); err != nil {
		return err
	}
	if err := jobframework.ApplyDefaultForManagedBy(ctx, job, w.queues, w.cache, w.client, w.managedJobsNamespaceSelector, log); err != nil {
		return err
	}

	if isAnElasticJob(obj) {
		// The Spark driver creates executors on its own, so they must wait for
		// the workload slice that accounts for them to be admitted.
		if obj.Spec.Executor.Template == nil {
			obj.Spec.Executor.Template = emptyExecutorPodTemplateSpec.DeepCopy()
		}
		utilpod.GateTemplate(obj.Spec.Executor.Template, kueue.ElasticJobSchedulingGate)
	}

	return nil
}

// +kubebuilder:webhook:path=/validate-sparkoperator-k8s-io-v1beta2-sparkapplication,mutating=false,failurePolicy=fail,sideEffects=None,groups=sparkoperator.k8s.io,resources=sparkapplications,verbs=create;update,versions=v1beta2,name=vsparkapplication.kb.io,admissionReviewVersions=v1

var _ admission.Validator[*sparkv1beta2.SparkApplication] = &SparkApplicationWebhook{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *SparkApplicationWebhook) ValidateCreate(ctx context.Context, obj *sparkv1beta2.SparkApplication) (admission.Warnings, error) {
	log := ctrl.LoggerFrom(ctx).WithName("sparkapplication-webhook")
	log.V(5).Info("Validating create")
	validationErrs, err := w.validateCreate(ctx, obj, w.maxTimeoutOnWorkload, true)
	if err != nil {
		return nil, err
	}
	// This is checked on create only: disabling the gate rejects new elastic
	// SparkApplications, while the existing ones keep running as elastic until they
	// finish, as the elastic annotation is immutable.
	if workloadslicing.Enabled(obj) && !features.Enabled(features.ElasticJobsViaWorkloadSlicesForSparkApplication) {
		validationErrs = append(validationErrs, field.Forbidden(elasticJobAnnotationPath,
			"an elastic SparkApplication requires the ElasticJobsViaWorkloadSlicesForSparkApplication feature gate"))
	}
	// Validated only on create so that existing elastic SparkApplications with
	// required or preferred topology remain updatable (e.g. by Kueue) after an
	// upgrade or after enabling ElasticJobsViaWorkloadSlicesWithTAS.
	validationErrs = append(validationErrs, validateElasticJobTopologyRequest(fromObject(obj))...)
	if w.manageJobsWithoutQueueName || jobframework.QueueName(fromObject(obj)) != "" {
		validationErrs = append(validationErrs, validatePartialPreemptionMinRetainedCount(fromObject(obj))...)
	}
	return nil, validationErrs.ToAggregate()
}

// validateElasticJobTopologyRequest rejects required or preferred topology on the
// driver and executor of an elastic SparkApplication, see
// jobframeworktas.ValidateElasticJobTopologyRequest. The topology annotations are
// read from the merged Pod metadata, as for the PodSets.
func validateElasticJobTopologyRequest(sparkApp *SparkApplication) field.ErrorList {
	var allErrs field.ErrorList
	if driver, err := sparkApp.buildDriverPodTemplateSpec(); err == nil {
		allErrs = append(allErrs, jobframeworktas.ValidateElasticJobTopologyRequest(sparkApp, driverSpecPath, &driver.ObjectMeta)...)
	}
	if executor, err := sparkApp.buildExecutorPodTemplateSpec(); err == nil {
		allErrs = append(allErrs, jobframeworktas.ValidateElasticJobTopologyRequest(sparkApp, executorSpecPath, &executor.ObjectMeta)...)
	}
	return allErrs
}

// isAnElasticJob returns whether the SparkApplication is an elastic job. It
// follows ElasticJobsViaWorkloadSlices only, as the job reconciler does when it
// picks the workload-slice path, so a job created while
// ElasticJobsViaWorkloadSlicesForSparkApplication was on stays elastic.
func isAnElasticJob(sparkApp metav1.Object) bool {
	return workloadslicing.Enabled(sparkApp)
}

func (w *SparkApplicationWebhook) validateCreate(
	ctx context.Context,
	job *sparkv1beta2.SparkApplication,
	maxTimeoutOnWorkload *metav1.Duration,
	validateWaitForPodsReady bool,
) (field.ErrorList, error) {
	var allErrors field.ErrorList
	kueueJob := (*SparkApplication)(job)

	if w.manageJobsWithoutQueueName || jobframework.QueueName(kueueJob) != "" {
		spec := &job.Spec

		if spec.Mode != sparkv1beta2.DeployModeCluster {
			allErrors = append(allErrors, field.Invalid(specPath.Child("mode"), spec.Mode, "only Cluster mode is supported for a kueue managed job"))
		}

		if isAnElasticJob(job) {
			allErrors = append(allErrors, validateElasticJob(job)...)
		}

		allErrors = append(allErrors, validatePartialPreemptionCreate(job)...)

		if !isAnElasticJob(job) && ptr.Deref(spec.DynamicAllocation, sparkv1beta2.DynamicAllocation{}).Enabled {
			allErrors = append(allErrors,
				field.Invalid(dynamicAllocationEnabledPath,
					ptr.Deref(spec.DynamicAllocation, sparkv1beta2.DynamicAllocation{}).Enabled,
					fmt.Sprintf("a kueue managed job can use dynamicAllocation only as an elastic job: "+
						"enable the ElasticJobsViaWorkloadSlices and ElasticJobsViaWorkloadSlicesForSparkApplication feature gates and set the %q: %q annotation",
						workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue),
				),
			)
		}
	}

	if validateWaitForPodsReady {
		allErrors = append(allErrors, jobframework.ValidateJobOnCreate(kueueJob, maxTimeoutOnWorkload)...)
	} else {
		allErrors = append(allErrors, jobframework.ValidateJobOnCreateWithoutWaitForPodsReady(kueueJob)...)
	}
	if features.Enabled(features.TopologyAwareScheduling) {
		validationErrs, err := w.validateTopologyRequest(ctx, kueueJob)
		if err != nil {
			return nil, err
		}
		allErrors = append(allErrors, validationErrs...)
	}

	return allErrors, nil
}

// validatePartialPreemptionCreate validates a SparkApplication that opts into partial preemption
// via the kueue.x-k8s.io/partial-preemption="true" annotation. Partial preemption reclaims quota
// reactively: Kueue evicts the excess executor Pods and relies on workload slices to keep the
// replacements the Spark driver creates gated, so the app must be an elastic job. The check
// applies only when the PartialPreemption gate is on.
func validatePartialPreemptionCreate(job *sparkv1beta2.SparkApplication) field.ErrorList {
	if !features.Enabled(features.PartialPreemption) ||
		job.Annotations[constants.PartialPreemptionAnnotation] != "true" {
		return nil
	}
	if !isAnElasticJob(job) {
		return field.ErrorList{field.Invalid(partialPreemptionAnnotationPath, "true",
			fmt.Sprintf("partial preemption requires the elastic job annotation %s", constants.ElasticJobAnnotation))}
	}
	return nil
}

func validateElasticJob(job *sparkv1beta2.SparkApplication) field.ErrorList {
	allErrors := field.ErrorList{}

	workloadSliceSchedulingGate := corev1.PodSchedulingGate{
		Name: kueue.ElasticJobSchedulingGate,
	}

	var schedulingGates []corev1.PodSchedulingGate
	if job.Spec.Executor.Template != nil {
		schedulingGates = job.Spec.Executor.Template.Spec.SchedulingGates
	}
	if !slices.Contains(schedulingGates, workloadSliceSchedulingGate) {
		allErrors = append(
			allErrors,
			field.Invalid(
				executorSpecPath.Child("template").Child("spec").Child("schedulingGates"),
				schedulingGates,
				"an elastic job must have the ElasticJobSchedulingGate",
			),
		)
	}

	return allErrors
}

func (w *SparkApplicationWebhook) validateTopologyRequest(ctx context.Context, sparkApp *SparkApplication) (field.ErrorList, error) {
	var allErrs field.ErrorList

	podSets, podSetsErr := sparkApp.PodSets(ctx, nil)

	if podSetsErr == nil {
		driverPodSet := podset.FindPodSetByName(podSets, driverPodSetName)
		allErrs = append(allErrs, jobframeworktas.ValidateTASPodSetRequest(driverSpecPath, &driverPodSet.Template.ObjectMeta)...)
		allErrs = append(allErrs, jobframeworktas.ValidateSliceSizeAnnotationUpperBound(driverSpecPath, &driverPodSet.Template.ObjectMeta, driverPodSet)...)

		executorPodSet := podset.FindPodSetByName(podSets, executorPodSetName)
		allErrs = append(allErrs, jobframeworktas.ValidateTASPodSetRequest(executorSpecPath, &executorPodSet.Template.ObjectMeta)...)
		allErrs = append(allErrs, jobframeworktas.ValidateSliceSizeAnnotationUpperBound(executorSpecPath, &executorPodSet.Template.ObjectMeta, executorPodSet)...)
	}

	if len(allErrs) > 0 {
		return allErrs, nil
	}

	return nil, podSetsErr
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *SparkApplicationWebhook) ValidateUpdate(ctx context.Context, oldSparkApp, newSparkApp *sparkv1beta2.SparkApplication) (admission.Warnings, error) {
	if !jobframework.ShouldValidateRayOrSparkJobOnUpdate(fromObject(oldSparkApp), fromObject(newSparkApp), w.manageJobsWithoutQueueName) {
		return nil, nil
	}
	log := ctrl.LoggerFrom(ctx).WithName("sparkapplication-webhook")
	log.V(5).Info("Validating update")
	allErrors := jobframework.ValidateJobOnUpdate(fromObject(oldSparkApp), fromObject(newSparkApp), w.queues.DefaultLocalQueueExist, w.maxTimeoutOnWorkload)
	validationErrs, err := w.validateCreate(ctx, newSparkApp, nil, false)
	if err != nil {
		return nil, err
	}
	allErrors = append(allErrors, validationErrs...)
	oldApp, newApp := fromObject(oldSparkApp), fromObject(newSparkApp)
	if minRetainedCountAnnotation(newApp) != minRetainedCountAnnotation(oldApp) ||
		(oldApp.IsSuspended() && newApp.IsSuspended() && maxExecutorCount(newApp) < maxExecutorCount(oldApp)) {
		// Revalidate the floor when the annotation is created or changed, and when the
		// executor maximum drops while the app stays suspended, so it cannot end up at or
		// above that maximum. A running app may still scale its executors below the floor.
		allErrors = append(allErrors, validatePartialPreemptionMinRetainedCount(newApp)...)
	}
	return nil, allErrors.ToAggregate()
}

// validatePartialPreemptionMinRetainedCount validates the
// kueue.x-k8s.io/podset-partial-preemption-min-retained-count executor annotation: a
// non-negative integer below the most executors the app can run. A malformed or negative
// value is silently dropped by podSetPreemptionMinRetainedCount, and a floor at or above that
// maximum leaves nothing for partial preemption to shrink. Both are rejected so an invalid
// floor is reported instead of being ignored.
func validatePartialPreemptionMinRetainedCount(app *SparkApplication) field.ErrorList {
	if !features.Enabled(features.PartialPreemption) {
		return nil
	}
	strVal := minRetainedCountAnnotation(app)
	if strVal == "" {
		return nil
	}
	minCount, err := strconv.ParseInt(strVal, 10, 32)
	if err != nil {
		return field.ErrorList{field.Invalid(partialPreemptionMinRetainedCountAnnotationPath, strVal, "must be a non-negative integer")}
	}
	if minCount < 0 {
		return field.ErrorList{field.Invalid(partialPreemptionMinRetainedCountAnnotationPath, int(minCount), "must be a non-negative integer")}
	}
	if limit := maxExecutorCount(app); minCount >= int64(limit) {
		return field.ErrorList{field.Invalid(partialPreemptionMinRetainedCountAnnotationPath, int(minCount),
			fmt.Sprintf("should be less than %d", limit))}
	}
	return nil
}

// minRetainedCountAnnotation returns the partial-preemption floor annotation of the executor
// pods, as the executor PodSet reads it, or "" if it is not set.
func minRetainedCountAnnotation(app *SparkApplication) string {
	executor, err := app.buildExecutorPodTemplateSpec()
	if err != nil {
		return ""
	}
	return executor.Annotations[constants.PodSetPartialPreemptionMinRetainedCountAnnotation]
}

// maxExecutorCount returns the most executors the app can run: maxExecutors with dynamic
// allocation (unbounded if unset, as in Spark), otherwise the executor instances.
func maxExecutorCount(app *SparkApplication) int32 {
	if da := app.dynamicAllocation(); da.Enabled {
		return ptr.Deref(da.MaxExecutors, math.MaxInt32)
	}
	return app.numInitialExecutors()
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type
func (w *SparkApplicationWebhook) ValidateDelete(_ context.Context, _ *sparkv1beta2.SparkApplication) (admission.Warnings, error) {
	return nil, nil
}
