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
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
)

const (
	DefaultPodSetName = "main"
)

// log is for logging in this package.
var workloadlog = ctrl.Log.WithName("workload-webhook")

func (r *Workload) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(r).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-kueue-x-k8s-io-v1alpha1-workload,mutating=true,failurePolicy=fail,sideEffects=None,groups=kueue.x-k8s.io,resources=workloads,verbs=create;update,versions=v1alpha1,name=mworkload.kb.io,admissionReviewVersions=v1

var _ webhook.Defaulter = &Workload{}

// Default implements webhook.Defaulter so a webhook will be registered for the type
func (r *Workload) Default() {
	workloadlog.V(5).Info("defaulter", "workload", klog.KObj(r))

	for i := range r.Spec.PodSets {
		podSet := &r.Spec.PodSets[i]
		if len(podSet.Name) == 0 {
			podSet.Name = DefaultPodSetName
		}
	}
}

// +kubebuilder:webhook:path=/validate-kueue-x-k8s-io-v1alpha1-workload,mutating=false,failurePolicy=fail,sideEffects=None,groups=kueue.x-k8s.io,resources=workloads,verbs=create;update,versions=v1alpha1,name=vworkload.kb.io,admissionReviewVersions=v1

var _ webhook.Validator = &Workload{}

// ValidateCreate implements webhook.Validator so a webhook will be registered for the type
func (r *Workload) ValidateCreate() error {
	return nil
}

// ValidateUpdate implements webhook.Validator so a webhook will be registered for the type
func (r *Workload) ValidateUpdate(old runtime.Object) error {
	return nil
}

// ValidateDelete implements webhook.Validator so a webhook will be registered for the type
func (r *Workload) ValidateDelete() error {
	return nil
}
