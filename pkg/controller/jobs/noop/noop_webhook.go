/*
Copyright 2023 The Kubernetes Authors.

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

package noop

import (
	"context"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
)

type webhook struct {
}

// SetupWebhook configures the webhook for batchJob.
func SetupWebhook(mgr ctrl.Manager, apiType runtime.Object) error {
	wh := &webhook{}
	return ctrl.NewWebhookManagedBy(mgr).
		For(apiType).
		WithDefaulter(wh).
		WithValidator(wh).
		Complete()
}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the type
func (w *webhook) Default(ctx context.Context, obj runtime.Object) error {
	return nil
}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *webhook) ValidateCreate(ctx context.Context, obj runtime.Object) error {
	return nil
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *webhook) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) error {
	return nil
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type
func (w *webhook) ValidateDelete(ctx context.Context, obj runtime.Object) error {
	return nil
}
