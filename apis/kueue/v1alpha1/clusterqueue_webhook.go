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

package v1alpha1

import (
	"fmt"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/validation"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	utilvalidation "k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
)

var (
	// log is for logging in this package.
	clusterQueueLog = ctrl.Log.WithName("cluster-queue-webhook")

	queueingStrategies = sets.NewString(string(StrictFIFO), string(BestEffortFIFO))
)

const isNegativeErrorMsg string = `must be greater than or equal to 0`

func (cq *ClusterQueue) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(cq).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-kueue-x-k8s-io-v1alpha1-clusterqueue,mutating=true,failurePolicy=fail,sideEffects=None,groups=kueue.x-k8s.io,resources=clusterqueues,verbs=create,versions=v1alpha1,name=mclusterqueue.kb.io,admissionReviewVersions=v1

var _ webhook.Defaulter = &ClusterQueue{}

// Default implements webhook.Defaulter so a webhook will be registered for the type
func (cq *ClusterQueue) Default() {
	clusterQueueLog.Info("defaulter", "clusterQueue", klog.KObj(cq))
	if !controllerutil.ContainsFinalizer(cq, ResourceInUseFinalizerName) {
		controllerutil.AddFinalizer(cq, ResourceInUseFinalizerName)
	}
}

// +kubebuilder:webhook:path=/validate-kueue-x-k8s-io-v1alpha1-clusterqueue,mutating=false,failurePolicy=fail,sideEffects=None,groups=kueue.x-k8s.io,resources=clusterqueues,verbs=create;update,versions=v1alpha1,name=vclusterqueue.kb.io,admissionReviewVersions=v1

var _ webhook.Validator = &ClusterQueue{}

// ValidateCreate implements webhook.Validator so a webhook will be registered for the type
func (cq *ClusterQueue) ValidateCreate() error {
	allErrs := ValidateClusterQueue(cq)
	return allErrs.ToAggregate()
}

// ValidateUpdate implements webhook.Validator so a webhook will be registered for the type
func (cq *ClusterQueue) ValidateUpdate(old runtime.Object) error {
	allErrs := ValidateClusterQueue(cq)
	return allErrs.ToAggregate()
}

// ValidateDelete implements webhook.Validator so a webhook will be registered for the type
func (cq *ClusterQueue) ValidateDelete() error {
	return nil
}

func ValidateClusterQueue(cq *ClusterQueue) field.ErrorList {
	path := field.NewPath("spec")

	allErrs := field.ErrorList{}
	allErrs = append(allErrs, ValidateResources(cq, path.Child("resources"))...)
	allErrs = append(allErrs, validateQueueingStrategy(string(cq.Spec.QueueingStrategy), path.Child("queueingStrategy"))...)
	allErrs = append(allErrs, validateNamespaceSelector(cq.Spec.NamespaceSelector, path.Child("namespaceSelector"))...)

	return allErrs
}

func ValidateResources(cq *ClusterQueue, path *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}

	for i, resource := range cq.Spec.Resources {
		allErrs = append(allErrs, validateResourceName(string(resource.Name), path.Index(i).Child("name"))...)

		flavorPath := path.Index(i).Child("flavors")
		for j, flavor := range resource.Flavors {
			allErrs = append(allErrs, validateFlavorName(string(flavor.Name), flavorPath.Index(j).Child("name"))...)
			allErrs = append(allErrs, validateFlavorQuota(flavor, flavorPath.Index(j).Child("quota"))...)
		}
	}
	return allErrs
}

func validateResourceName(name string, fldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}
	for _, msg := range utilvalidation.IsQualifiedName(name) {
		allErrs = append(allErrs, field.Invalid(fldPath, name, msg))
	}
	return allErrs
}

func validateFlavorName(name string, path *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}
	if msgs := utilvalidation.IsDNS1123Subdomain(name); len(msgs) > 0 {
		for _, msg := range msgs {
			allErrs = append(allErrs, field.Invalid(path, name, msg))
		}
	}
	return allErrs
}

func validateFlavorQuota(flavor Flavor, path *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}
	allErrs = append(allErrs, validateResourceQuantity(flavor.Quota.Min, path.Child("min"))...)

	if flavor.Quota.Max != nil {
		allErrs = append(allErrs, validateResourceQuantity(*flavor.Quota.Max, path.Child("max"))...)
		if flavor.Quota.Min.Cmp(*flavor.Quota.Max) > 0 {
			allErrs = append(allErrs, field.Invalid(path.Child("min"), flavor.Quota.Min.String(), fmt.Sprintf("must be less than or equal to %s max", flavor.Name)))
		}
	}
	return allErrs
}

// validateResourceQuantity enforces that specified quantity is valid for specified resource
func validateResourceQuantity(value resource.Quantity, fldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}
	if value.Cmp(resource.Quantity{}) < 0 {
		allErrs = append(allErrs, field.Invalid(fldPath, value.String(), isNegativeErrorMsg))
	}
	return allErrs
}

func validateQueueingStrategy(strategy string, path *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}

	if len(strategy) > 0 && !queueingStrategies.Has(strategy) {
		allErrs = append(allErrs, field.Invalid(path, strategy, fmt.Sprintf("queueing strategy %s is not supported, available strategies are %v", strategy, queueingStrategies.List())))
	}

	return allErrs
}

func validateNamespaceSelector(selector *metav1.LabelSelector, path *field.Path) field.ErrorList {
	allErrs := validation.ValidateLabelSelector(selector, path)
	return allErrs
}
