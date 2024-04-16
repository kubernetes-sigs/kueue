/*
Copyright 2021 The Kubernetes Authors.

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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/validation"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/features"
)

const (
	limitIsEmptyErrorMsg string = `must be nil when cohort is empty`
	lendingLimitErrorMsg string = `must be less than or equal to the nominalQuota`
)

type ClusterQueueWebhook struct{}

func setupWebhookForClusterQueue(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(&kueue.ClusterQueue{}).
		WithDefaulter(&ClusterQueueWebhook{}).
		WithValidator(&ClusterQueueWebhook{}).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-kueue-x-k8s-io-v1beta1-clusterqueue,mutating=true,failurePolicy=fail,sideEffects=None,groups=kueue.x-k8s.io,resources=clusterqueues,verbs=create,versions=v1beta1,name=mclusterqueue.kb.io,admissionReviewVersions=v1

var _ webhook.CustomDefaulter = &ClusterQueueWebhook{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the type
func (w *ClusterQueueWebhook) Default(ctx context.Context, obj runtime.Object) error {
	cq := obj.(*kueue.ClusterQueue)
	log := ctrl.LoggerFrom(ctx).WithName("clusterqueue-webhook")
	log.V(5).Info("Applying defaults", "clusterQueue", klog.KObj(cq))
	if !controllerutil.ContainsFinalizer(cq, kueue.ResourceInUseFinalizerName) {
		controllerutil.AddFinalizer(cq, kueue.ResourceInUseFinalizerName)
	}
	return nil
}

// +kubebuilder:webhook:path=/validate-kueue-x-k8s-io-v1beta1-clusterqueue,mutating=false,failurePolicy=fail,sideEffects=None,groups=kueue.x-k8s.io,resources=clusterqueues,verbs=create;update,versions=v1beta1,name=vclusterqueue.kb.io,admissionReviewVersions=v1

var _ webhook.CustomValidator = &ClusterQueueWebhook{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *ClusterQueueWebhook) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	cq := obj.(*kueue.ClusterQueue)
	log := ctrl.LoggerFrom(ctx).WithName("clusterqueue-webhook")
	log.V(5).Info("Validating create", "clusterQueue", klog.KObj(cq))
	allErrs := ValidateClusterQueue(cq)
	return nil, allErrs.ToAggregate()
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *ClusterQueueWebhook) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	newCQ := newObj.(*kueue.ClusterQueue)
	oldCQ := oldObj.(*kueue.ClusterQueue)

	log := ctrl.LoggerFrom(ctx).WithName("clusterqueue-webhook")
	log.V(5).Info("Validating update", "clusterQueue", klog.KObj(newCQ))
	allErrs := ValidateClusterQueueUpdate(newCQ, oldCQ)
	return nil, allErrs.ToAggregate()
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type
func (w *ClusterQueueWebhook) ValidateDelete(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

func ValidateClusterQueue(cq *kueue.ClusterQueue) field.ErrorList {
	path := field.NewPath("spec")

	var allErrs field.ErrorList
	allErrs = append(allErrs, validateResourceGroups(cq.Spec.ResourceGroups, cq.Spec.Cohort, path.Child("resourceGroups"))...)
	allErrs = append(allErrs,
		validation.ValidateLabelSelector(cq.Spec.NamespaceSelector, validation.LabelSelectorValidationOptions{}, path.Child("namespaceSelector"))...)
	return allErrs
}

func ValidateClusterQueueUpdate(newObj, oldObj *kueue.ClusterQueue) field.ErrorList {
	var allErrs field.ErrorList
	allErrs = append(allErrs, ValidateClusterQueue(newObj)...)
	return allErrs
}

func validateResourceGroups(resourceGroups []kueue.ResourceGroup, cohort string, path *field.Path) field.ErrorList {
	var allErrs field.ErrorList
	seenResources := sets.New[corev1.ResourceName]()
	seenFlavors := sets.New[kueue.ResourceFlavorReference]()

	for i, rg := range resourceGroups {
		path := path.Index(i)
		for j, name := range rg.CoveredResources {
			path := path.Child("coveredResources").Index(j)
			allErrs = append(allErrs, validateResourceName(name, path)...)
			if seenResources.Has(name) {
				allErrs = append(allErrs, field.Duplicate(path, name))
			} else {
				seenResources.Insert(name)
			}
		}
		for j, fqs := range rg.Flavors {
			path := path.Child("flavors").Index(j)
			allErrs = append(allErrs, validateFlavorQuotas(fqs, rg.CoveredResources, cohort, path)...)
			if seenFlavors.Has(fqs.Name) {
				allErrs = append(allErrs, field.Duplicate(path.Child("name"), fqs.Name))
			} else {
				seenFlavors.Insert(fqs.Name)
			}
		}
	}
	return allErrs
}

func validateFlavorQuotas(flavorQuotas kueue.FlavorQuotas, coveredResources []corev1.ResourceName, cohort string, path *field.Path) field.ErrorList {
	var allErrs field.ErrorList

	for i, rq := range flavorQuotas.Resources {
		if i >= len(coveredResources) {
			break
		}
		path := path.Child("resources").Index(i)
		if rq.Name != coveredResources[i] {
			allErrs = append(allErrs, field.Invalid(path.Child("name"), rq.Name, "must match the name in coveredResources"))
		}
		allErrs = append(allErrs, validateResourceQuantity(rq.NominalQuota, path.Child("nominalQuota"))...)
		if rq.BorrowingLimit != nil {
			borrowingLimitPath := path.Child("borrowingLimit")
			allErrs = append(allErrs, validateResourceQuantity(*rq.BorrowingLimit, borrowingLimitPath)...)
		}
		if features.Enabled(features.LendingLimit) && rq.LendingLimit != nil {
			lendingLimitPath := path.Child("lendingLimit")
			allErrs = append(allErrs, validateResourceQuantity(*rq.LendingLimit, lendingLimitPath)...)
			allErrs = append(allErrs, validateLimit(*rq.LendingLimit, cohort, lendingLimitPath)...)
			allErrs = append(allErrs, validateLendingLimit(*rq.LendingLimit, rq.NominalQuota, lendingLimitPath)...)
		}
	}
	return allErrs
}

// validateResourceQuantity enforces that specified quantity is valid for specified resource
func validateResourceQuantity(value resource.Quantity, fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList
	if value.Cmp(resource.Quantity{}) < 0 {
		allErrs = append(allErrs, field.Invalid(fldPath, value.String(), constants.IsNegativeErrorMsg))
	}
	return allErrs
}

// validateLimit enforces that BorrowingLimit or LendingLimit must be nil when cohort is empty
func validateLimit(limit resource.Quantity, cohort string, fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList
	if len(cohort) == 0 {
		allErrs = append(allErrs, field.Invalid(fldPath, limit.String(), limitIsEmptyErrorMsg))
	}
	return allErrs
}

// validateLendingLimit enforces that LendingLimit is not greater than NominalQuota
func validateLendingLimit(lend, nominal resource.Quantity, fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList
	if lend.Cmp(nominal) > 0 {
		allErrs = append(allErrs, field.Invalid(fldPath, lend.String(), lendingLimitErrorMsg))
	}
	return allErrs
}
