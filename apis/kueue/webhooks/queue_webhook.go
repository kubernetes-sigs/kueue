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

	kueue "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
)

// log is for logging in this package.
var queueLog = ctrl.Log.WithName("queue-webhook")

type QueueWebhook struct{}

func setupWebhookForQueue(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(&kueue.Queue{}).
		WithValidator(&QueueWebhook{}).
		Complete()
}

// +kubebuilder:webhook:path=/validate-kueue-x-k8s-io-v1alpha1-queue,mutating=false,failurePolicy=fail,sideEffects=None,groups=kueue.x-k8s.io,resources=queues,verbs=create;update,versions=v1alpha1,name=vqueue.kb.io,admissionReviewVersions=v1

var _ webhook.CustomValidator = &QueueWebhook{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *QueueWebhook) ValidateCreate(ctx context.Context, obj runtime.Object) error {
	q := obj.(*kueue.Queue)
	queueLog.V(5).Info("Validating create", "queue", klog.KObj(q))
	return ValidateQueue(q).ToAggregate()
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *QueueWebhook) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) error {
	newQ := newObj.(*kueue.Queue)
	oldQ := oldObj.(*kueue.Queue)
	queueLog.V(5).Info("Validating update", "queue", klog.KObj(newQ))
	return ValidateQueueUpdate(newQ, oldQ).ToAggregate()
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type
func (w *QueueWebhook) ValidateDelete(ctx context.Context, obj runtime.Object) error {
	return nil
}

func ValidateQueue(q *kueue.Queue) field.ErrorList {
	var allErrs field.ErrorList
	clusterQueuePath := field.NewPath("spec", "clusterQueue")
	allErrs = append(allErrs, validateNameReference(string(q.Spec.ClusterQueue), clusterQueuePath)...)
	return allErrs
}

func ValidateQueueUpdate(newObj, oldObj *kueue.Queue) field.ErrorList {
	return apivalidation.ValidateImmutableField(newObj.Spec.ClusterQueue, oldObj.Spec.ClusterQueue, field.NewPath("spec", "clusterQueue"))
}
