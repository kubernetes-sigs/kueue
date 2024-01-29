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
	apivalidation "k8s.io/apimachinery/pkg/api/validation"
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
)

const (
	isNegativeErrorMsg     string = `must be greater than or equal to 0`
	borrowingLimitErrorMsg string = `must be nil when cohort is empty`
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
	if cq.Spec.Preemption == nil {
		cq.Spec.Preemption = &kueue.ClusterQueuePreemption{
			WithinClusterQueue:  kueue.PreemptionPolicyNever,
			ReclaimWithinCohort: kueue.PreemptionPolicyNever,
		}
	}
	if cq.Spec.Preemption.BorrowWithinCohort == nil {
		cq.Spec.Preemption.BorrowWithinCohort = &kueue.BorrowWithinCohort{
			Policy: kueue.BorrowWithinCohortPolicyNever,
		}
	}
	if cq.Spec.FlavorFungibility == nil {
		cq.Spec.FlavorFungibility = &kueue.FlavorFungibility{
			WhenCanBorrow:  kueue.Borrow,
			WhenCanPreempt: kueue.TryNextFlavor,
		}
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
	if len(cq.Spec.Cohort) != 0 {
		allErrs = append(allErrs, validateNameReference(cq.Spec.Cohort, path.Child("cohort"))...)
	}
	allErrs = append(allErrs, validateResourceGroups(cq.Spec.ResourceGroups, cq.Spec.Cohort, path.Child("resourceGroups"))...)
	allErrs = append(allErrs,
		validation.ValidateLabelSelector(cq.Spec.NamespaceSelector, validation.LabelSelectorValidationOptions{}, path.Child("namespaceSelector"))...)
	if cq.Spec.Preemption != nil {
		allErrs = append(allErrs, validatePreemption(cq.Spec.Preemption, path.Child("preemption"))...)
	}
	return allErrs
}

// Since Kubernetes 1.25, we can use CEL validation rules to implement
// a few common immutability patterns directly in the manifest for a CRD.
// ref: https://kubernetes.io/blog/2022/09/29/enforce-immutability-using-cel/
// We need to validate the spec.queueingStrategy immutable manually before Kubernetes 1.25.
func ValidateClusterQueueUpdate(newObj, oldObj *kueue.ClusterQueue) field.ErrorList {
	var allErrs field.ErrorList
	allErrs = append(allErrs, ValidateClusterQueue(newObj)...)
	allErrs = append(allErrs, apivalidation.ValidateImmutableField(newObj.Spec.QueueingStrategy, oldObj.Spec.QueueingStrategy, field.NewPath("spec", "queueingStrategy"))...)
	return allErrs
}

func validatePreemption(preemption *kueue.ClusterQueuePreemption, path *field.Path) field.ErrorList {
	var allErrs field.ErrorList
	if preemption.ReclaimWithinCohort == kueue.PreemptionPolicyNever &&
		preemption.BorrowWithinCohort != nil &&
		preemption.BorrowWithinCohort.Policy != kueue.BorrowWithinCohortPolicyNever {
		allErrs = append(allErrs, field.Invalid(path, preemption, "reclaimWithinCohort=Never and borrowWithinCohort.Policy!=Never"))
	}
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
	allErrs := validateNameReference(string(flavorQuotas.Name), path.Child("name"))
	if len(flavorQuotas.Resources) != len(coveredResources) {
		allErrs = append(allErrs, field.Invalid(path.Child("resources"), field.OmitValueType{}, "must have the same number of resources as the coveredResources"))
	}

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
			allErrs = append(allErrs, validateBorrowingLimit(*rq.BorrowingLimit, cohort, borrowingLimitPath)...)
		}
	}
	return allErrs
}

// validateResourceQuantity enforces that specified quantity is valid for specified resource
func validateResourceQuantity(value resource.Quantity, fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList
	if value.Cmp(resource.Quantity{}) < 0 {
		allErrs = append(allErrs, field.Invalid(fldPath, value.String(), isNegativeErrorMsg))
	}
	return allErrs
}

// validateBorrowingLimit enforces that BorrowingLimit must be nil when cohort is empty
func validateBorrowingLimit(borrow resource.Quantity, cohort string, fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList
	if len(cohort) == 0 {
		allErrs = append(allErrs, field.Invalid(fldPath, borrow.String(), borrowingLimitErrorMsg))
	}
	return allErrs
}
