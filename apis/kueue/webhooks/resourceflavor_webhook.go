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

	"k8s.io/apimachinery/pkg/apis/meta/v1/validation"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
)

// log is for logging in this package.
var resourceFlavorLog = ctrl.Log.WithName("resource-flavor-webhook")

type ResourceFlavorWebhook struct{}

func setupWebhookForResourceFlavor(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(&kueue.ResourceFlavor{}).
		WithDefaulter(&ResourceFlavorWebhook{}).
		WithValidator(&ResourceFlavorWebhook{}).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-kueue-x-k8s-io-v1alpha1-resourceflavor,mutating=true,failurePolicy=fail,sideEffects=None,groups=kueue.x-k8s.io,resources=resourceflavors,verbs=create,versions=v1alpha1,name=mresourceflavor.kb.io,admissionReviewVersions=v1

var _ webhook.CustomDefaulter = &ResourceFlavorWebhook{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the type
func (w *ResourceFlavorWebhook) Default(ctx context.Context, obj runtime.Object) error {
	rf := obj.(*kueue.ResourceFlavor)
	resourceFlavorLog.V(5).Info("Setting defaulter", "resourceFlavor", klog.KObj(rf))

	if !controllerutil.ContainsFinalizer(rf, kueue.ResourceInUseFinalizerName) {
		controllerutil.AddFinalizer(rf, kueue.ResourceInUseFinalizerName)
	}
	return nil
}

// +kubebuilder:webhook:path=/validate-kueue-x-k8s-io-v1alpha1-resourceflavor,mutating=false,failurePolicy=fail,sideEffects=None,groups=kueue.x-k8s.io,resources=resourceflavors,verbs=create;update,versions=v1alpha1,name=vresourceflavor.kb.io,admissionReviewVersions=v1

var _ webhook.CustomValidator = &ResourceFlavorWebhook{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *ResourceFlavorWebhook) ValidateCreate(ctx context.Context, obj runtime.Object) error {
	rf := obj.(*kueue.ResourceFlavor)
	resourceFlavorLog.V(5).Info("Validating create", "resourceFlavor", klog.KObj(rf))
	return ValidateResourceFlavorLabels(rf).ToAggregate()
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *ResourceFlavorWebhook) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) error {
	newRF := newObj.(*kueue.ResourceFlavor)
	resourceFlavorLog.V(5).Info("Validating update", "resourceFlavor", klog.KObj(newRF))
	return ValidateResourceFlavorLabels(newRF).ToAggregate()
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type
func (w *ResourceFlavorWebhook) ValidateDelete(ctx context.Context, obj runtime.Object) error {
	return nil
}

func ValidateResourceFlavorLabels(rf *kueue.ResourceFlavor) field.ErrorList {
	field := field.NewPath("labels")
	return validation.ValidateLabels(rf.Labels, field)

}
