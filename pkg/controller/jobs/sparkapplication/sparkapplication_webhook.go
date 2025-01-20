/*
Copyright 2025 The Kubernetes Authors.

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

	apivalidation "k8s.io/apimachinery/pkg/api/validation"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobframework/webhook"
	podcontroller "sigs.k8s.io/kueue/pkg/controller/jobs/pod"
	"sigs.k8s.io/kueue/pkg/queue"

	kfsparkapi "github.com/kubeflow/spark-operator/api/v1beta2"
)

type SparkApplicationWebhook struct {
	client                       client.Client
	manageJobsWithoutQueueName   bool
	managedJobsNamespaceSelector labels.Selector
	queues                       *queue.Manager
}

// SetupSparkApplicationWebhook configures the webhook for SparkApplication.
func SetupSparkApplicationWebhook(mgr ctrl.Manager, opts ...jobframework.Option) error {
	options := jobframework.ProcessOptions(opts...)
	wh := &SparkApplicationWebhook{
		client:                       mgr.GetClient(),
		manageJobsWithoutQueueName:   options.ManageJobsWithoutQueueName,
		managedJobsNamespaceSelector: options.ManagedJobsNamespaceSelector,
		queues:                       options.Queues,
	}
	obj := &kfsparkapi.SparkApplication{}
	return webhook.WebhookManagedBy(mgr).
		For(obj).
		WithMutationHandler(webhook.WithLosslessDefaulter(mgr.GetScheme(), obj, wh)).
		WithValidator(wh).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-sparkoperator-k8s-io-v1beta2-sparkapplication,mutating=true,failurePolicy=fail,reinvocationPolicy=IfNeeded,sideEffects=None,groups=sparkoperator.k8s.io,resources=sparkapplications,verbs=create,versions=v1beta2,name=msparkapplication.kb.io,admissionReviewVersions=v1

var _ admission.CustomDefaulter = &SparkApplicationWebhook{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the type
func (wh *SparkApplicationWebhook) Default(ctx context.Context, obj runtime.Object) error {
	sparkApp := fromObject(obj)
	log := ctrl.LoggerFrom(ctx).WithName("sparkapplication-webhook").WithValues("sparkApplication", klog.KObj(sparkApp.Object()))

	jobframework.ApplyDefaultLocalQueue(sparkApp.Object(), wh.queues.DefaultLocalQueueExist)
	suspend, err := jobframework.WorkloadShouldBeSuspended(
		ctx, sparkApp.Object(), wh.client, wh.manageJobsWithoutQueueName, wh.managedJobsNamespaceSelector,
	)
	if err != nil {
		return err
	}

	if !suspend {
		return nil
	}

	log.V(5).Info("Applying defaults")
	ctrl.LoggerInto(ctx, log)

	sparkApp.Suspend()
	wh.setDefaultsToDriverSpec(ctx, sparkApp)
	wh.setDefaultsToExecutorSpec(ctx, sparkApp)

	return nil
}

func (wh *SparkApplicationWebhook) setDefaultsToDriverSpec(ctx context.Context, sparkApp *SparkApplication) {
	log := ctrl.LoggerFrom(ctx)

	defaultAnnotations := map[string]string{
		podcontroller.SkipWebhookAnnotationKey: podcontroller.SkipWebhookAnnotationValue,
	}

	log.V(5).Info("Setting defaults to spec.driver.annotations", "defaults", defaultAnnotations)
	if len(defaultAnnotations) > 0 && sparkApp.Spec.Driver.Annotations == nil {
		sparkApp.Spec.Driver.Annotations = make(map[string]string, len(defaultAnnotations))
	}

	for k, v := range defaultAnnotations {
		sparkApp.Spec.Driver.Annotations[k] = v
	}
}

func (wh *SparkApplicationWebhook) setDefaultsToExecutorSpec(ctx context.Context, sparkApp *SparkApplication) {
	log := ctrl.LoggerFrom(ctx)

	setDefaults := func(
		defaultLabels map[string]string,
		defaultAnnotations map[string]string,
	) {
		if len(defaultLabels) > 0 {
			log.V(5).Info("Setting defaults to spec.executor.labels", "defaults", defaultAnnotations)
			if sparkApp.Spec.Executor.Labels == nil {
				sparkApp.Spec.Executor.Labels = make(map[string]string, len(defaultLabels))
			}
			for k, v := range defaultLabels {
				sparkApp.Spec.Executor.Labels[k] = v
			}
		}
		if len(defaultAnnotations) > 0 {
			log.V(5).Info("Setting defaults to spec.executor.annotations", "defaults", defaultAnnotations)
			if sparkApp.Spec.Executor.Annotations == nil {
				sparkApp.Spec.Executor.Annotations = make(map[string]string, len(defaultAnnotations))
			}
			for k, v := range defaultAnnotations {
				sparkApp.Spec.Executor.Annotations[k] = v
			}
		}
	}

	switch mode, _ := sparkApp.integrationMode(); mode {
	case integrationModeAggregated:
		setDefaults(
			nil,
			map[string]string{
				podcontroller.SkipWebhookAnnotationKey: podcontroller.SkipWebhookAnnotationValue,
			},
		)

	case integrationModeExecutorDetached:
		setDefaults(
			map[string]string{
				constants.QueueLabel: jobframework.QueueNameForObject(sparkApp.Object()),
			},
			map[string]string{
				podcontroller.SuspendedByParentAnnotation: FrameworkName,
			},
		)
	}
}

// +kubebuilder:webhook:path=/validate-sparkoperator-k8s-io-v1beta2-sparkapplication,mutating=false,failurePolicy=fail,sideEffects=None,groups=sparkoperator.k8s.io,resources=sparkapplications,verbs=create;update,versions=v1beta2,name=vsparkapplication.kb.io,admissionReviewVersions=v1

var _ admission.CustomValidator = &SparkApplicationWebhook{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type
func (wh *SparkApplicationWebhook) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	sparkApp := fromObject(obj)
	log := ctrl.LoggerFrom(ctx).
		WithName("sparkapplication-webhook").
		WithValues("sparkApplication", klog.KObj(sparkApp.Object()))
	ctx = ctrl.LoggerInto(ctx, log)
	log.Info("Validating create")

	suspend, err := jobframework.WorkloadShouldBeSuspended(
		ctx, sparkApp.Object(), wh.client, wh.manageJobsWithoutQueueName, wh.managedJobsNamespaceSelector,
	)
	if err != nil {
		return nil, err
	}

	if !suspend {
		return nil, nil
	}

	return nil, wh.validateCreate(ctx, sparkApp).ToAggregate()
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type
func (wh *SparkApplicationWebhook) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	oldSparkApp := fromObject(oldObj)
	newSparkApp := fromObject(newObj)
	log := ctrl.LoggerFrom(ctx).WithName("sparkapplication-webhook").
		WithValues("sparkApplication",
			klog.KObj(newSparkApp.Object()),
		)
	ctx = ctrl.LoggerInto(ctx, log)

	newSuspend, err := jobframework.WorkloadShouldBeSuspended(
		ctx, newSparkApp.Object(), wh.client, wh.manageJobsWithoutQueueName, wh.managedJobsNamespaceSelector,
	)
	if err != nil {
		return nil, err
	}
	if !newSuspend {
		return nil, nil
	}

	log.Info("Validating update")
	var allErrs field.ErrorList
	allErrs = append(allErrs, wh.validateUpdate(ctx, oldSparkApp, newSparkApp)...)
	allErrs = append(allErrs, wh.validateCreate(ctx, newSparkApp)...)
	return nil, allErrs.ToAggregate()
}

func (wh *SparkApplicationWebhook) validateUpdate(
	_ context.Context, oldSparkApp, newSparkApp *SparkApplication,
) field.ErrorList {
	allErrs := jobframework.ValidateJobOnUpdate(oldSparkApp, newSparkApp)

	// unknown integration error will be captured in validateCreate()
	newMode, _ := newSparkApp.integrationMode()

	// suspended, integrationMode is Aggregated
	// --> spec is locked execept for spec
	if !oldSparkApp.IsSuspended() && !newSparkApp.IsSuspended() && newMode == integrationModeAggregated {
		allErrs = append(allErrs, apivalidation.ValidateImmutableField(
			newSparkApp.Spec.Executor.Instances,
			oldSparkApp.Spec.Executor.Instances,
			field.NewPath("spec", "executor", "instances"),
		)...)
		// // immutable exclude Spec.Suspend when integrationModeAggregated
		// // compare with making both suspended
		// newSparkApp.Suspend()
		// oldSparkApp.Suspend()
		// allErrs = append(allErrs, apivalidation.ValidateImmutableField(
		// 	newSparkApp.Spec, oldSparkApp.Spec,
		// 	field.NewPath("spec"),
		// )...)
	}

	return allErrs
}

func (wh *SparkApplicationWebhook) validateCreate(_ context.Context, sparkApp *SparkApplication) field.ErrorList {
	allErrs := jobframework.ValidateJobOnCreate(sparkApp)

	allErrs = append(allErrs,
		expectStringValue(
			sparkApp.Spec.Driver.Annotations[podcontroller.SkipWebhookAnnotationKey],
			podcontroller.SkipWebhookAnnotationValue,
			field.NewPath("spec", "driver", "annotations").Key(podcontroller.SkipWebhookAnnotationKey),
		)...,
	)
	allErrs = append(allErrs,
		expectKeyNotExists(
			sparkApp.Spec.Driver.Annotations,
			podcontroller.SuspendedByParentAnnotation,
			field.NewPath("spec", "driver", "annotations").Key(podcontroller.SuspendedByParentAnnotation),
		)...,
	)
	mode, err := sparkApp.integrationMode()
	if err != nil {
		allErrs = append(allErrs, err)
	}
	switch mode {
	case integrationModeAggregated:
		allErrs = append(allErrs,
			expectStringValue(
				sparkApp.Spec.Executor.Annotations[podcontroller.SkipWebhookAnnotationKey],
				podcontroller.SkipWebhookAnnotationValue,
				field.NewPath("spec", "executor", "annotations").Key(podcontroller.SkipWebhookAnnotationKey),
			)...,
		)
		allErrs = append(allErrs,
			expectKeyNotExists(
				sparkApp.Spec.Executor.Annotations,
				podcontroller.SuspendedByParentAnnotation,
				field.NewPath("spec", "executor", "annotations").Key(podcontroller.SuspendedByParentAnnotation),
			)...,
		)
		if sparkApp.Spec.Executor.Instances == nil {
			allErrs = append(allErrs,
				field.Required(
					field.NewPath("spec", "executor", "instances"),
					"must not be nil",
				),
			)
		}
	case integrationModeExecutorDetached:
		allErrs = append(allErrs,
			expectKeyNotExists(
				sparkApp.Spec.Executor.Annotations,
				podcontroller.SkipWebhookAnnotationKey,
				field.NewPath("spec", "executor", "annotations").Key(podcontroller.SkipWebhookAnnotationKey),
			)...,
		)
		allErrs = append(allErrs,
			expectStringValue(
				sparkApp.Spec.Executor.Annotations[podcontroller.SuspendedByParentAnnotation],
				FrameworkName,
				field.NewPath("spec", "executor", "annotations").Key(podcontroller.SuspendedByParentAnnotation),
			)...,
		)
		if sparkApp.Spec.DynamicAllocation.MaxExecutors == nil {
			allErrs = append(allErrs,
				field.Required(
					field.NewPath("spec", "dynamicAllocation", "maxExecutors"),
					"must not be nil",
				),
			)
		}

	}

	return allErrs
}

func expectStringValue(actual, expected string, path *field.Path) field.ErrorList {
	var allErrs field.ErrorList
	if actual != expected {
		allErrs = append(allErrs, field.Invalid(path, actual, fmt.Sprintf("must be %s", expected)))
	}
	return allErrs
}

func expectKeyNotExists(targetMap map[string]string, key string, path *field.Path) field.ErrorList {
	var allErrs field.ErrorList

	if v, ok := targetMap[key]; ok {
		allErrs = append(allErrs,
			field.Invalid(path, v, "must not exist"),
		)
	}

	return allErrs
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type
func (wh *SparkApplicationWebhook) ValidateDelete(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	return nil, nil
}
