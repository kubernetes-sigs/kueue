/*
Copyright 2024 The Kubernetes Authors.

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

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
)

type CohortWebhook struct{}

func setupWebhookForCohort(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(&kueuealpha.Cohort{}).
		WithValidator(&CohortWebhook{}).
		Complete()
}

func (w *CohortWebhook) Default(ctx context.Context, obj runtime.Object) error {
	return nil
}

//+kubebuilder:webhook:path=/validate-kueue-x-k8s-io-v1alpha1-cohort,mutating=false,failurePolicy=fail,sideEffects=None,groups=kueue.x-k8s.io,resources=cohorts,verbs=create;update,versions=v1alpha1,name=vcohort.kb.io,admissionReviewVersions=v1

var _ webhook.CustomValidator = &CohortWebhook{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *CohortWebhook) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	cohort := obj.(*kueuealpha.Cohort)
	log := ctrl.LoggerFrom(ctx).WithName("cohort-webhook")
	log.V(5).Info("Validating Cohort create")
	return nil, validateCohort(cohort).ToAggregate()
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *CohortWebhook) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	cohort := newObj.(*kueuealpha.Cohort)
	log := ctrl.LoggerFrom(ctx).WithName("cohort-webhook")
	log.V(5).Info("Validating Cohort update")
	return nil, validateCohort(cohort).ToAggregate()
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type
func (w *CohortWebhook) ValidateDelete(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

func validateCohort(cohort *kueuealpha.Cohort) field.ErrorList {
	path := field.NewPath("spec")
	config := validationConfig{
		hasParent:                        cohort.Spec.Parent != "",
		enforceNominalGreaterThanLending: false,
	}
	return validateResourceGroups(cohort.Spec.ResourceGroups, config, path.Child("resourceGroups"))
}
