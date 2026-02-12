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

package replicaset

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"sigs.k8s.io/kueue/pkg/cache/queue"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/webhook"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
)

type Webhook struct {
	client                       client.Client
	manageJobsWithoutQueueName   bool
	managedJobsNamespaceSelector labels.Selector
	queues                       *queue.Manager
}

// SetupWebhook registers the mutating and validating webhook for ReplicaSet
// objects with the given controller-runtime Manager. Additional options may
// be passed to customize queue integration or other behavior.
func SetupWebhook(mgr ctrl.Manager, opts ...jobframework.Option) error {
	options := jobframework.ProcessOptions(opts...)
	wh := &Webhook{
		client:                       mgr.GetClient(),
		manageJobsWithoutQueueName:   options.ManageJobsWithoutQueueName,
		managedJobsNamespaceSelector: options.ManagedJobsNamespaceSelector,
		queues:                       options.Queues,
	}
	obj := &appsv1.ReplicaSet{}
	if options.NoopWebhook {
		return webhook.SetupNoopWebhook(mgr, obj)
	}
	return ctrl.NewWebhookManagedBy(mgr, obj).
		WithDefaulter(wh).
		WithValidator(wh).
		WithLogConstructor(jobframework.WebhookLogConstructor(GroupVersionKind, options.RoleTracker)).
		Complete()
}

// logger returns a logr.Logger preconfigured with the "rs-webhook" name,
// scoped to the provided context. Used for consistent structured logging
// within webhook admission handlers.
func logger(ctx context.Context) logr.Logger {
	return ctrl.LoggerFrom(ctx).WithName("rs-webhook")
}

// +kubebuilder:webhook:path=/mutate-apps-v1-replicaset,mutating=true,failurePolicy=fail,sideEffects=None,groups="apps",resources=replicasets,verbs=create;update,versions=v1,name=mreplicaset.kb.io,admissionReviewVersions=v1

var _ admission.Defaulter[*appsv1.ReplicaSet] = &Webhook{}

// toReplicaSet asserts that obj is an *appsv1.ReplicaSet and returns it.
// Returns an error if the type does not match.
func toReplicaSet(obj runtime.Object) (*appsv1.ReplicaSet, error) {
	rs, ok := obj.(*appsv1.ReplicaSet)
	if !ok {
		return nil, fmt.Errorf("expected a ReplicaSet, but got %T", obj)
	}
	return rs, nil
}

// Default applies default values to the provided ReplicaSet.
// This includes applying the default local queue (if configured) and,
// when the ElasticJobsViaWorkloadSlices feature gate is enabled and the
// ReplicaSet has the elastic-job annotation, adding the workload slice
// scheduling gate to its Pod template.
func (w *Webhook) Default(ctx context.Context, rs *appsv1.ReplicaSet) error {
	logger(ctx).V(5).Info("Applying defaults")

	jobframework.ApplyDefaultLocalQueue(rs, w.queues.DefaultLocalQueueExist)

	// Check if ElasticJobsViaWorkloadSlices is enabled and this is an ElasticJob enabled instance.
	if features.Enabled(features.ElasticJobsViaWorkloadSlices) && workloadslicing.Enabled(rs) {
		workloadslicing.ApplyWorkloadSliceSchedulingGate(&rs.Spec.Template)
		return nil
	}

	return nil
}

// +kubebuilder:webhook:path=/validate-apps-v1-replicaset,mutating=false,failurePolicy=fail,sideEffects=None,groups=apps,resources=replicasets,verbs=create;update,versions=v1,name=vreplicaset.kb.io,admissionReviewVersions=v1

var _ admission.Validator[*appsv1.ReplicaSet] = &Webhook{}

// ValidateCreate validates a ReplicaSet object on creation.
// It runs generic job creation validations from the jobframework.
// Returns an aggregated error if any validation fails.
func (w *Webhook) ValidateCreate(ctx context.Context, rs *appsv1.ReplicaSet) (admission.Warnings, error) {
	logger(ctx).V(5).Info("Validating create")
	return nil, jobframework.ValidateJobOnCreate(&ReplicaSet{ReplicaSet: rs}).ToAggregate()
}

// ValidateUpdate validates a ReplicaSet object on update.
// It runs generic job update validations, optional topology-aware scheduling
// validations, and feature-specific rules for elastic jobs.
// Returns an aggregated error if any validation fails.
func (w *Webhook) ValidateUpdate(ctx context.Context, oldRs, newRs *appsv1.ReplicaSet) (admission.Warnings, error) {
	logger(ctx).V(5).Info("Validating update")

	validationErrs, err := w.validateUpdate(ctx, &ReplicaSet{ReplicaSet: oldRs}, &ReplicaSet{ReplicaSet: newRs})
	if err != nil {
		return nil, err
	}
	return nil, validationErrs.ToAggregate()
}

// validateUpdate executes the set of validation rules for a ReplicaSet update.
// This includes:
//   - generic job creation and update validation,
//   - topology-aware scheduling request validation (when enabled),
//   - preventing removal of the elastic job annotation from an existing elastic ReplicaSet.
func (w *Webhook) validateUpdate(ctx context.Context, oldRs, newRs *ReplicaSet) (field.ErrorList, error) {
	allErrs := jobframework.ValidateJobOnCreate(newRs)
	allErrs = append(allErrs, jobframework.ValidateJobOnUpdate(oldRs, newRs, w.queues.DefaultLocalQueueExist)...)

	if features.Enabled(features.TopologyAwareScheduling) {
		validationErrs, err := w.validateTopologyRequest(ctx, newRs)
		if err != nil {
			return nil, err
		}
		allErrs = append(allErrs, validationErrs...)
	}

	if features.Enabled(features.ElasticJobsViaWorkloadSlices) &&
		workloadslicing.Enabled(oldRs) && !workloadslicing.Enabled(newRs) {
		allErrs = append(allErrs, field.Invalid(field.NewPath("metadata", "annotations"), newRs.Annotations,
			fmt.Sprintf("should contain: %s=%s", workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue)))
	}

	return allErrs, nil
}

var replicaMetaPath = field.NewPath("spec", "template", "metadata")

// validateTopologyRequest validates topology-aware scheduling (TAS) fields
// for the given ReplicaSet, when the TopologyAwareScheduling feature is enabled.
// It checks the pod template metadata for TAS requests and validates that any
// elastic slice size annotations do not exceed the PodSet's size.
func (w *Webhook) validateTopologyRequest(ctx context.Context, rs *ReplicaSet) (field.ErrorList, error) {
	validationErrs := jobframework.ValidateTASPodSetRequest(replicaMetaPath, &rs.Spec.Template.ObjectMeta)
	if validationErrs != nil {
		return validationErrs, nil
	}

	podSets, err := rs.PodSets(ctx)
	if err != nil {
		return nil, err
	}

	if len(podSets) == 0 {
		return nil, nil
	}

	return jobframework.ValidateSliceSizeAnnotationUpperBound(replicaMetaPath, &rs.Spec.Template.ObjectMeta, &podSets[0]), nil
}

// ValidateDelete validates the ReplicaSet object on delete.
// Currently, we don't require ReplicaSet validation on deletion.
func (w *Webhook) ValidateDelete(_ context.Context, _ *appsv1.ReplicaSet) (admission.Warnings, error) {
	return nil, nil
}
