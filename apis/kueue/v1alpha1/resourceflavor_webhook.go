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
	"k8s.io/apimachinery/pkg/apis/meta/v1/validation"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
)

// log is for logging in this package.
var resourceFlavorLog = ctrl.Log.WithName("resource-flavor-webhook")

func (rf *ResourceFlavor) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(rf).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-kueue-x-k8s-io-v1alpha1-resourceflavor,mutating=true,failurePolicy=fail,sideEffects=None,groups=kueue.x-k8s.io,resources=resourceflavors,verbs=create,versions=v1alpha1,name=mresourceflavor.kb.io,admissionReviewVersions=v1

var _ webhook.Defaulter = &ResourceFlavor{}

// Default implements webhook.Defaulter so a webhook will be registered for the type
func (rf *ResourceFlavor) Default() {
	resourceFlavorLog.Info("defaulter", "resourceFlavor", klog.KObj(rf))
	if !controllerutil.ContainsFinalizer(rf, ResourceInUseFinalizerName) {
		controllerutil.AddFinalizer(rf, ResourceInUseFinalizerName)
	}
}

// +kubebuilder:webhook:path=/validate-kueue-x-k8s-io-v1alpha1-resourceflavor,mutating=false,failurePolicy=fail,sideEffects=None,groups=kueue.x-k8s.io,resources=resourceflavors,verbs=create;update,versions=v1alpha1,name=vresourceflavor.kb.io,admissionReviewVersions=v1

var _ webhook.Validator = &ResourceFlavor{}

// ValidateCreate implements webhook.Validator so a webhook will be registered for the type
func (rf *ResourceFlavor) ValidateCreate() error {
	return ValidateResourceFlavorLabels(rf).ToAggregate()
}

// ValidateUpdate implements webhook.Validator so a webhook will be registered for the type
func (rf *ResourceFlavor) ValidateUpdate(old runtime.Object) error {
	return ValidateResourceFlavorLabels(rf).ToAggregate()
}

// ValidateDelete implements webhook.Validator so a webhook will be registered for the type
func (rf *ResourceFlavor) ValidateDelete() error {
	return nil
}

func ValidateResourceFlavorLabels(rf *ResourceFlavor) field.ErrorList {
	field := field.NewPath("labels")
	return validation.ValidateLabels(rf.Labels, field)

}
