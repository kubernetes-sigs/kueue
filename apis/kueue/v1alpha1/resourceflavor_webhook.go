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
	"fmt"
	"reflect"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
)

// log is for logging in this package.
var resourceflavorlog = logf.Log.WithName("resourceflavor-resource")

func (r *ResourceFlavor) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(r).
		Complete()
}

//+kubebuilder:webhook:path=/mutate-kueue-x-k8s-io-v1alpha1-resourceflavor,mutating=true,failurePolicy=fail,sideEffects=None,groups=kueue.x-k8s.io,resources=resourceflavors,verbs=create;update,versions=v1alpha1,name=mresourceflavor.kb.io,admissionReviewVersions=v1

var _ webhook.Defaulter = &ResourceFlavor{}

// Default implements webhook.Defaulter so a webhook will be registered for the type
func (r *ResourceFlavor) Default() {
	resourceflavorlog.Info("default", "name", r.Name)
}

//+kubebuilder:webhook:path=/validate-kueue-x-k8s-io-v1alpha1-resourceflavor,mutating=false,failurePolicy=fail,sideEffects=None,groups=kueue.x-k8s.io,resources=resourceflavors,verbs=create;update;delete,versions=v1alpha1,name=vresourceflavor.kb.io,admissionReviewVersions=v1

var _ webhook.Validator = &ResourceFlavor{}

// ValidateCreate implements webhook.Validator so a webhook will be registered for the type
func (r *ResourceFlavor) ValidateCreate() error {
	resourceflavorlog.Info("validate create", "name", r.Name)
	return nil
}

// ValidateUpdate implements webhook.Validator so a webhook will be registered for the type
func (r *ResourceFlavor) ValidateUpdate(old runtime.Object) error {
	resourceflavorlog.Info("validate update", "name", r.Name)
	oldRf, match := old.(*ResourceFlavor)
	if !match {
		return fmt.Errorf("the object is not resource flavor")
	}
	if len(oldRf.ClusterQueues) > 0 && !reflect.DeepEqual(r.Labels, oldRf.Labels) {
		return fmt.Errorf("labels updates is prohibited with references")
	}

	return nil
}

// ValidateDelete implements webhook.Validator so a webhook will be registered for the type
func (r *ResourceFlavor) ValidateDelete() error {
	resourceflavorlog.Info("validate delete", "name", r.Name)
	if len(r.ClusterQueues) > 0 {
		return fmt.Errorf("deletion is prohibited with references")
	}

	return nil
}
