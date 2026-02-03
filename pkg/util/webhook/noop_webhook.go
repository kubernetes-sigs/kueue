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

package webhook

import (
	"context"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

type noopWebhook[T runtime.Object] struct{}

func SetupNoopWebhook[T runtime.Object](mgr ctrl.Manager, apiType T) error {
	wh := &noopWebhook[T]{}
	return ctrl.NewWebhookManagedBy(mgr, apiType).
		WithDefaulter(wh).
		WithValidator(wh).
		Complete()
}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the type
func (w *noopWebhook[T]) Default(ctx context.Context, obj T) error {
	return nil
}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *noopWebhook[T]) ValidateCreate(ctx context.Context, obj T) (admission.Warnings, error) {
	return nil, nil
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type
func (w *noopWebhook[T]) ValidateUpdate(ctx context.Context, oldObj, newObj T) (admission.Warnings, error) {
	return nil, nil
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type
func (w *noopWebhook[T]) ValidateDelete(ctx context.Context, obj T) (admission.Warnings, error) {
	return nil, nil
}
