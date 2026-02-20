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

package webhooks

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	apimachineryvalidation "k8s.io/apimachinery/pkg/api/validation"
	"k8s.io/apimachinery/pkg/apis/meta/v1/validation"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
)

const (
	limitIsEmptyErrorMsgTemplate string = `must be nil when %s is empty`
	lendingLimitErrorMsg         string = `must be less than or equal to the nominalQuota`
)

type ClusterQueueWebhook struct{}

func setupWebhookForClusterQueue(mgr ctrl.Manager, roleTracker *roletracker.RoleTracker) error {
	return ctrl.NewWebhookManagedBy(mgr, &kueue.ClusterQueue{}).
		WithDefaulter(&ClusterQueueWebhook{}).
		WithValidator(&ClusterQueueWebhook{}).
		WithLogConstructor(roletracker.WebhookLogConstructor(roleTracker)).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-kueue-x-k8s-io-v1beta2-clusterqueue,mutating=true,failurePolicy=fail,sideEffects=None,groups=kueue.x-k8s.io,resources=clusterqueues,verbs=create,versions=v1beta2,name=mclusterqueue.kb.io,admissionReviewVersions=v1

var _ admission.Defaulter[*kueue.ClusterQueue] = &ClusterQueueWebhook{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the type
func (w *ClusterQueueWebhook) Default(ctx context.Context, cq *kueue.ClusterQueue) error {
	log := ctrl.LoggerFrom(ctx).WithName("clusterqueue-webhook")
	log.V(5).Info("Applying defaults")
	if !controllerutil.ContainsFinalizer(cq, kueue.ResourceInUseFinalizerName) {
		controllerutil.AddFinalizer(cq, kueue.ResourceInUseFinalizerName)
	}
	return nil
}

// +kubebuilder:webhook:path=/validate-kueue-x-k8s-io-v1beta2-clusterqueue,mutating=false,failurePolicy=fail,sideEffects=None,groups=kueue.x-k8s.io,resources=clusterqueues,verbs=create;update,versions=v1beta2,name=vclusterqueue.kb.io,admissionReviewVersions=v1

var _ admission.Validator[*kueue.ClusterQueue] = &ClusterQueueWebhook{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *ClusterQueueWebhook) ValidateCreate(ctx context.Context, cq *kueue.ClusterQueue) (admission.Warnings, error) {
	log := ctrl.LoggerFrom(ctx).WithName("clusterqueue-webhook")
	log.V(5).Info("Validating create")
	allErrs := ValidateClusterQueue(cq)
	return nil, allErrs.ToAggregate()
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *ClusterQueueWebhook) ValidateUpdate(ctx context.Context, _, newCQ *kueue.ClusterQueue) (admission.Warnings, error) {
	log := ctrl.LoggerFrom(ctx).WithName("clusterqueue-webhook")
	log.V(5).Info("Validating update")
	allErrs := ValidateClusterQueueUpdate(newCQ)
	return nil, allErrs.ToAggregate()
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type
func (w *ClusterQueueWebhook) ValidateDelete(_ context.Context, _ *kueue.ClusterQueue) (admission.Warnings, error) {
	return nil, nil
}

func ValidateClusterQueue(cq *kueue.ClusterQueue) field.ErrorList {
	path := field.NewPath("spec")

	var allErrs field.ErrorList
	config := validationConfig{
		hasParent:                        cq.Spec.CohortName != "",
		enforceNominalGreaterThanLending: true,
	}
	allErrs = append(allErrs, validateResourceGroups(cq.Spec.ResourceGroups, config, path.Child("resourceGroups"), false)...)
	allErrs = append(allErrs,
		validation.ValidateLabelSelector(cq.Spec.NamespaceSelector, validation.LabelSelectorValidationOptions{}, path.Child("namespaceSelector"))...)
	if cq.Spec.Preemption != nil {
		allErrs = append(allErrs, validatePreemption(cq.Spec.Preemption, path.Child("preemption"))...)
	}
	if cq.Spec.FlavorFungibility != nil {
		allErrs = append(allErrs, validateFlavorFungibility(cq.Spec.FlavorFungibility, path.Child("flavorFungibility"))...)
	}
	allErrs = append(allErrs, validateFairSharing(cq.Spec.FairSharing, path.Child("fairSharing"))...)
	allErrs = append(allErrs, validateTotalFlavors(cq.Spec.ResourceGroups, path.Child("resourceGroups"))...)
	allErrs = append(allErrs, validateTotalCoveredResources(cq.Spec.ResourceGroups, path.Child("resourceGroups"))...)
	allErrs = append(allErrs, validateFlavorResourceCombinations(cq.Spec.ResourceGroups, path.Child("resourceGroups"))...)
	return allErrs
}

func ValidateClusterQueueUpdate(newObj *kueue.ClusterQueue) field.ErrorList {
	return ValidateClusterQueue(newObj)
}

func validateTotalFlavors(resourceGroups []kueue.ResourceGroup, path *field.Path) field.ErrorList {
	var allErrs field.ErrorList
	total := 0
	for _, rg := range resourceGroups {
		total += len(rg.Flavors)
	}
	if total > 256 {
		allErrs = append(allErrs, field.Invalid(path, total,
			fmt.Sprintf("total number of flavors across all resourceGroups must be ≤ 256, got %d", total)))
	}
	return allErrs
}

func validateTotalCoveredResources(resourceGroups []kueue.ResourceGroup, path *field.Path) field.ErrorList {
	var allErrs field.ErrorList
	total := 0
	for _, rg := range resourceGroups {
		total += len(rg.CoveredResources)
	}
	if total > 256 {
		allErrs = append(allErrs, field.Invalid(path, total,
			fmt.Sprintf("total number of covered resources across all resourceGroups must be ≤ 256, got %d", total)))
	}
	return allErrs
}

func validateFlavorResourceCombinations(resourceGroups []kueue.ResourceGroup, path *field.Path) field.ErrorList {
	var allErrs field.ErrorList
	for i, rg := range resourceGroups {
		total := 0
		for _, fqs := range rg.Flavors {
			total += len(fqs.Resources)
		}
		if total > 512 {
			allErrs = append(allErrs, field.Invalid(
				path.Index(i),
				total,
				fmt.Sprintf("number of flavor-resource combinations in a resourceGroup must be ≤ 512, got %d", total),
			))
		}
	}
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

func validateFlavorFungibility(fungibility *kueue.FlavorFungibility, path *field.Path) field.ErrorList {
	var allErrs field.ErrorList
	if fungibility.Preference != nil {
		if fungibility.WhenCanBorrow != kueue.TryNextFlavor || fungibility.WhenCanPreempt != kueue.TryNextFlavor {
			allErrs = append(allErrs, field.Invalid(
				path.Child("preference"),
				*fungibility.Preference,
				fmt.Sprintf("preference %q requires both whenCanBorrow and whenCanPreempt to be TryNextFlavor", *fungibility.Preference),
			))
		}
	}
	return allErrs
}

func validateResourceGroups(resourceGroups []kueue.ResourceGroup, config validationConfig, path *field.Path, isCohort bool) field.ErrorList {
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
			allErrs = append(allErrs, validateFlavorQuotas(fqs, rg.CoveredResources, config, path, isCohort)...)
			if seenFlavors.Has(fqs.Name) {
				allErrs = append(allErrs, field.Duplicate(path.Child("name"), fqs.Name))
			} else {
				seenFlavors.Insert(fqs.Name)
			}
		}
	}
	return allErrs
}

func validateFlavorQuotas(flavorQuotas kueue.FlavorQuotas, coveredResources []corev1.ResourceName, config validationConfig, path *field.Path, isCohort bool) field.ErrorList {
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
			allErrs = append(allErrs, validateLimit(*rq.BorrowingLimit, config, borrowingLimitPath, isCohort)...)
			allErrs = append(allErrs, validateResourceQuantity(*rq.BorrowingLimit, borrowingLimitPath)...)
		}
		if rq.LendingLimit != nil {
			lendingLimitPath := path.Child("lendingLimit")
			allErrs = append(allErrs, validateResourceQuantity(*rq.LendingLimit, lendingLimitPath)...)
			allErrs = append(allErrs, validateLimit(*rq.LendingLimit, config, lendingLimitPath, isCohort)...)
			allErrs = append(allErrs, validateLendingLimit(*rq.LendingLimit, rq.NominalQuota, config, lendingLimitPath)...)
		}
	}
	return allErrs
}

// validateResourceQuantity enforces that specified quantity is valid for specified resource
func validateResourceQuantity(value resource.Quantity, fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList
	if value.Cmp(resource.Quantity{}) < 0 {
		allErrs = append(allErrs, field.Invalid(fldPath, value.String(), apimachineryvalidation.IsNegativeErrorMsg))
	}
	return allErrs
}

// validateLimit enforces that BorrowingLimit or LendingLimit must be nil when cohort (or parent cohort) is empty
func validateLimit(limit resource.Quantity, config validationConfig, fldPath *field.Path, isCohort bool) field.ErrorList {
	var allErrs field.ErrorList
	if !config.hasParent {
		var limitIsEmptyErrorMsg string
		if isCohort {
			limitIsEmptyErrorMsg = fmt.Sprintf(limitIsEmptyErrorMsgTemplate, "parent")
		} else {
			limitIsEmptyErrorMsg = fmt.Sprintf(limitIsEmptyErrorMsgTemplate, "cohort")
		}
		allErrs = append(allErrs, field.Invalid(fldPath, limit.String(), limitIsEmptyErrorMsg))
	}
	return allErrs
}

// validateLendingLimit enforces that LendingLimit is not greater than NominalQuota
func validateLendingLimit(lend, nominal resource.Quantity, config validationConfig, fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList
	if config.enforceNominalGreaterThanLending && lend.Cmp(nominal) > 0 {
		allErrs = append(allErrs, field.Invalid(fldPath, lend.String(), lendingLimitErrorMsg))
	}
	return allErrs
}
