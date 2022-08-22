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
	"fmt"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/validation"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
)

var (
	// log is for logging in this package.
	clusterQueueLog = ctrl.Log.WithName("clusterqueue-webhook")

	queueingStrategies = sets.NewString(string(kueue.StrictFIFO), string(kueue.BestEffortFIFO))
)

const (
	isNegativeErrorMsg string = `must be greater than or equal to 0`
)

type ClusterQueueWebhook struct{}

func setupWebhookForClusterQueue(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(&kueue.ClusterQueue{}).
		WithDefaulter(&ClusterQueueWebhook{}).
		WithValidator(&ClusterQueueWebhook{}).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-kueue-x-k8s-io-v1alpha1-clusterqueue,mutating=true,failurePolicy=fail,sideEffects=None,groups=kueue.x-k8s.io,resources=clusterqueues,verbs=create,versions=v1alpha1,name=mclusterqueue.kb.io,admissionReviewVersions=v1

var _ webhook.CustomDefaulter = &ClusterQueueWebhook{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the type
func (w *ClusterQueueWebhook) Default(ctx context.Context, obj runtime.Object) error {
	cq := obj.(*kueue.ClusterQueue)

	clusterQueueLog.V(5).Info("Applying defaults", "clusterQueue", klog.KObj(cq))
	if !controllerutil.ContainsFinalizer(cq, kueue.ResourceInUseFinalizerName) {
		controllerutil.AddFinalizer(cq, kueue.ResourceInUseFinalizerName)
	}
	return nil
}

// +kubebuilder:webhook:path=/validate-kueue-x-k8s-io-v1alpha1-clusterqueue,mutating=false,failurePolicy=fail,sideEffects=None,groups=kueue.x-k8s.io,resources=clusterqueues,verbs=create;update,versions=v1alpha1,name=vclusterqueue.kb.io,admissionReviewVersions=v1

var _ webhook.CustomValidator = &ClusterQueueWebhook{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *ClusterQueueWebhook) ValidateCreate(ctx context.Context, obj runtime.Object) error {
	cq := obj.(*kueue.ClusterQueue)
	clusterQueueLog.V(5).Info("Validating create", "clusterQueue", klog.KObj(cq))
	allErrs := ValidateClusterQueue(cq)
	return allErrs.ToAggregate()
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *ClusterQueueWebhook) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) error {
	newCQ := newObj.(*kueue.ClusterQueue)
	clusterQueueLog.V(5).Info("Validating update", "clusterQueue", klog.KObj(newCQ))
	allErrs := ValidateClusterQueue(newCQ)
	return allErrs.ToAggregate()
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type
func (w *ClusterQueueWebhook) ValidateDelete(ctx context.Context, obj runtime.Object) error {
	return nil
}

func ValidateClusterQueue(cq *kueue.ClusterQueue) field.ErrorList {
	path := field.NewPath("spec")

	var allErrs field.ErrorList
	if len(cq.Spec.Cohort) != 0 {
		allErrs = append(allErrs, validateNameReference(cq.Spec.Cohort, path.Child("cohort"))...)
	}
	allErrs = append(allErrs, validateResources(cq.Spec.Resources, path.Child("resources"))...)
	allErrs = append(allErrs, validateQueueingStrategy(string(cq.Spec.QueueingStrategy), path.Child("queueingStrategy"))...)
	allErrs = append(allErrs, validateNamespaceSelector(cq.Spec.NamespaceSelector, path.Child("namespaceSelector"))...)

	return allErrs
}

func validateResources(resources []kueue.Resource, path *field.Path) field.ErrorList {
	var allErrs field.ErrorList
	flavorsPerRes := make([]sets.String, len(resources))

	if len(resources) > 16 {
		allErrs = append(allErrs, field.TooMany(path, len(resources), 16))
	}

	for i, resource := range resources {
		path := path.Index(i)
		allErrs = append(allErrs, validateResourceName(resource.Name, path.Child("name"))...)
		if len(resource.Flavors) > 16 {
			allErrs = append(allErrs, field.TooMany(path.Child("flavors"), len(resource.Flavors), 16))
		}

		flavorsPerRes[i] = make(sets.String, len(resource.Flavors))
		for j, flavor := range resource.Flavors {
			path := path.Child("flavors").Index(j)
			allErrs = append(allErrs, validateNameReference(string(flavor.Name), path.Child("name"))...)
			allErrs = append(allErrs, validateFlavorQuota(flavor, path.Child("quota"))...)
			flavorsPerRes[i].Insert(string(flavor.Name))
		}
		for j := 0; j < i; j++ {
			if !flavorsPerRes[i].HasAny(flavorsPerRes[j].UnsortedList()...) || matchesFlavorsInOrder(resource.Flavors, resources[j].Flavors) {
				continue
			}
			err := field.Invalid(path.Child("flavors"), resource.Flavors, fmt.Sprintf("has flavors present in resource %s; all flavors must be different or they all must be present in the same order", resources[j].Name))
			allErrs = append(allErrs, err)
		}
	}
	return allErrs
}

func validateFlavorQuota(flavor kueue.Flavor, path *field.Path) field.ErrorList {
	var allErrs field.ErrorList
	allErrs = append(allErrs, validateResourceQuantity(flavor.Quota.Min, path.Child("min"))...)

	if flavor.Quota.Max != nil {
		allErrs = append(allErrs, validateResourceQuantity(*flavor.Quota.Max, path.Child("max"))...)
		if flavor.Quota.Min.Cmp(*flavor.Quota.Max) > 0 {
			allErrs = append(allErrs, field.Invalid(path.Child("min"), flavor.Quota.Min.String(), fmt.Sprintf("must be less than or equal to %s max", flavor.Name)))
		}
	}
	return allErrs
}

func matchesFlavorsInOrder(f1, f2 []kueue.Flavor) bool {
	if len(f1) != len(f2) {
		return false
	}
	for i := range f1 {
		if f1[i].Name != f2[i].Name {
			return false
		}
	}
	return true
}

// validateResourceQuantity enforces that specified quantity is valid for specified resource
func validateResourceQuantity(value resource.Quantity, fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList
	if value.Cmp(resource.Quantity{}) < 0 {
		allErrs = append(allErrs, field.Invalid(fldPath, value.String(), isNegativeErrorMsg))
	}
	return allErrs
}

func validateQueueingStrategy(strategy string, path *field.Path) field.ErrorList {
	var allErrs field.ErrorList
	if len(strategy) > 0 && !queueingStrategies.Has(strategy) {
		allErrs = append(allErrs, field.Invalid(path, strategy, fmt.Sprintf("queueing strategy %s is not supported, available strategies are %v", strategy, queueingStrategies.List())))
	}
	return allErrs
}

func validateNamespaceSelector(selector *metav1.LabelSelector, path *field.Path) field.ErrorList {
	allErrs := validation.ValidateLabelSelector(selector, path)
	return allErrs
}
