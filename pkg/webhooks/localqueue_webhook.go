/*
Copyright 2022 The Kubernetes Authors.

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

package webhooks

import (
	"context"

	apivalidation "k8s.io/apimachinery/pkg/api/validation"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
)

type LocalQueueWebhook struct{}

func setupWebhookForLocalQueue(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(&kueue.LocalQueue{}).
		WithValidator(&LocalQueueWebhook{}).
		Complete()
}

// +kubebuilder:webhook:path=/validate-kueue-x-k8s-io-v1beta1-localqueue,mutating=false,failurePolicy=fail,sideEffects=None,groups=kueue.x-k8s.io,resources=localqueues,verbs=create;update,versions=v1beta1,name=vlocalqueue.kb.io,admissionReviewVersions=v1

var _ webhook.CustomValidator = &LocalQueueWebhook{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *LocalQueueWebhook) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	q := obj.(*kueue.LocalQueue)
	log := ctrl.LoggerFrom(ctx).WithName("localqueue-webhook")
	log.V(5).Info("Validating create", "localQueue", klog.KObj(q))
	return nil, ValidateLocalQueue(q).ToAggregate()
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *LocalQueueWebhook) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	newQ := newObj.(*kueue.LocalQueue)
	oldQ := oldObj.(*kueue.LocalQueue)
	log := ctrl.LoggerFrom(ctx).WithName("localqueue-webhook")
	log.V(5).Info("Validating update", "localQueue", klog.KObj(newQ))
	return nil, ValidateLocalQueueUpdate(newQ, oldQ).ToAggregate()
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type
func (w *LocalQueueWebhook) ValidateDelete(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

func ValidateLocalQueue(q *kueue.LocalQueue) field.ErrorList {
	var allErrs field.ErrorList
	clusterQueuePath := field.NewPath("spec", "clusterQueue")
	allErrs = append(allErrs, validateNameReference(string(q.Spec.ClusterQueue), clusterQueuePath)...)
	return allErrs
}

func ValidateLocalQueueUpdate(newObj, oldObj *kueue.LocalQueue) field.ErrorList {
	return apivalidation.ValidateImmutableField(newObj.Spec.ClusterQueue, oldObj.Spec.ClusterQueue, field.NewPath("spec", "clusterQueue"))
}
