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

package v1alpha1

import (
	apivalidation "k8s.io/apimachinery/pkg/api/validation"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
)

// log is for logging in this package.
var queueLog = ctrl.Log.WithName("queue-webhook")

func (q *Queue) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(q).
		Complete()
}

// +kubebuilder:webhook:path=/validate-kueue-x-k8s-io-v1alpha1-queue,mutating=false,failurePolicy=fail,sideEffects=None,groups=kueue.x-k8s.io,resources=queues,verbs=create;update,versions=v1alpha1,name=vqueue.kb.io,admissionReviewVersions=v1

var _ webhook.Validator = &Queue{}

// ValidateCreate implements webhook.Validator so a webhook will be registered for the type
func (q *Queue) ValidateCreate() error {
	queueLog.V(5).Info("Validating create", "queue", klog.KObj(q))
	return ValidateQueueCreate(q).ToAggregate()
}

// ValidateUpdate implements webhook.Validator so a webhook will be registered for the type
func (q *Queue) ValidateUpdate(old runtime.Object) error {
	queueLog.V(5).Info("Validating update", "queue", klog.KObj(q))
	return ValidateQueueUpdate(q, old.(*Queue)).ToAggregate()
}

// ValidateDelete implements webhook.Validator so a webhook will be registered for the type
func (q *Queue) ValidateDelete() error {
	return nil
}

func ValidateQueueCreate(q *Queue) field.ErrorList {
	var allErrs field.ErrorList
	clusterQueuePath := field.NewPath("spec", "clusterQueue")
	for _, msg := range apivalidation.NameIsDNSSubdomain(string(q.Spec.ClusterQueue), false) {
		allErrs = append(allErrs, field.Invalid(clusterQueuePath, q.Spec.ClusterQueue, msg))
	}
	return allErrs
}

func ValidateQueueUpdate(newObj, oldObj *Queue) field.ErrorList {
	return apivalidation.ValidateImmutableField(newObj.Spec.ClusterQueue, oldObj.Spec.ClusterQueue, field.NewPath("spec", "clusterQueue"))
}
