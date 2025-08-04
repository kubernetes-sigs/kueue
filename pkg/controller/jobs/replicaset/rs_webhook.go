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
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobframework/webhook"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/queue"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
)

// Webhook definition for ReplicaSet objects.
type Webhook struct {
	client client.Client
	queues *queue.Manager
}

// logger helper returns "rs-webhook" named logger.
func logger(ctx context.Context) logr.Logger {
	return ctrl.LoggerFrom(ctx).WithName("rs-webhook")
}

// +kubebuilder:webhook:path=/mutate-apps-v1-replicaset,mutating=true,failurePolicy=fail,sideEffects=None,groups=apps,resources=replicasets,verbs=create,versions=v1,name=mreplicaset.kb.io,admissionReviewVersions=v1

var _ admission.CustomDefaulter = &Webhook{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the type
func (w *Webhook) Default(ctx context.Context, obj runtime.Object) error {
	rs, ok := obj.(*appsv1.ReplicaSet)
	if !ok {
		return fmt.Errorf("expected a ReplicaSet, but got %T", obj)
	}
	logger(ctx).V(5).Info("Applying defaults")

	jobframework.ApplyDefaultLocalQueue(rs, w.queues.DefaultLocalQueueExist)

	workloadslicing.ApplyWorkloadSliceSchedulingGate(rs, &rs.Spec.Template)

	return nil
}

// +kubebuilder:webhook:path=/validate-apps-v1-replicaset,mutating=false,failurePolicy=fail,sideEffects=None,groups=apps,resources=replicasets,verbs=create;update,versions=v1,name=vreplicaset.kb.io,admissionReviewVersions=v1

var _ admission.CustomValidator = &Webhook{}

// ValidateCreate validates the ReplicaSet object on create.
func (w *Webhook) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	rs, ok := obj.(*appsv1.ReplicaSet)
	if !ok {
		return nil, fmt.Errorf("expected a ReplicaSet, but got %T", obj)
	}
	logger(ctx).V(5).Info("Validating create")
	return nil, jobframework.ValidateJobOnCreate((*ReplicaSet)(rs)).ToAggregate()
}

// ValidateUpdate validates the ReplicaSet object on update.
func (w *Webhook) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	newRs, ok := newObj.(*appsv1.ReplicaSet)
	if !ok {
		return nil, fmt.Errorf("expected a ReplicaSet, but got %T", newObj)
	}
	oldRs, ok := oldObj.(*appsv1.ReplicaSet)
	if !ok {
		return nil, fmt.Errorf("expected a ReplicaSet, but got %T", newObj)
	}
	logger(ctx).V(5).Info("Validating update")

	validationErrs, err := w.validateUpdate((*ReplicaSet)(oldRs), (*ReplicaSet)(newRs))
	if err != nil {
		return nil, err
	}
	return nil, validationErrs.ToAggregate()
}

func (w *Webhook) validateUpdate(oldRs, newRs *ReplicaSet) (field.ErrorList, error) {
	allErrs := jobframework.ValidateJobOnCreate(newRs)
	allErrs = append(allErrs, jobframework.ValidateJobOnUpdate(oldRs, newRs, w.queues.DefaultLocalQueueExist)...)

	if features.Enabled(features.TopologyAwareScheduling) {
		validationErrs, err := w.validateTopologyRequest(newRs)
		if err != nil {
			return nil, err
		}
		allErrs = append(allErrs, validationErrs...)
	}

	if !workloadslicing.HasWorkloadSliceSchedulingGate(newRs.Spec.Template.Spec.SchedulingGates) {
		allErrs = append(allErrs, field.Invalid(field.NewPath("spec", "tempalte", "spec", "schedulingGates"), newRs.Spec.Template.Spec.SchedulingGates, "should contain: "+kueue.ElasticJobSchedulingGate))
	}

	return allErrs, nil
}

var replicaMetaPath = field.NewPath("spec", "template", "metadata")

func (w *Webhook) validateTopologyRequest(rs *ReplicaSet) (field.ErrorList, error) {
	validationErrs := jobframework.ValidateTASPodSetRequest(replicaMetaPath, &rs.Spec.Template.ObjectMeta)
	if validationErrs != nil {
		return validationErrs, nil
	}

	podSets, err := rs.PodSets()
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
func (w *Webhook) ValidateDelete(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

// SetupWebhook configures the webhook for ReplicaSet objects.
func SetupWebhook(mgr ctrl.Manager, opts ...jobframework.Option) error {
	options := jobframework.ProcessOptions(opts...)
	wh := &Webhook{
		client: mgr.GetClient(),
		queues: options.Queues,
	}
	obj := &appsv1.ReplicaSet{}
	return webhook.WebhookManagedBy(mgr).
		For(obj).
		WithMutationHandler(admission.WithCustomDefaulter(mgr.GetScheme(), obj, wh)).
		WithValidator(wh).
		Complete()
}
