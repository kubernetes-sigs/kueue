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

package disaggregatedset

import (
	"context"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
	disaggregatedsetv1 "sigs.k8s.io/lws/api/disaggregatedset/v1"

	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	"sigs.k8s.io/kueue/pkg/util/webhook"
)

// TODO(#11952): implement defaulting and validation as described in
// keps/11952-disaggregatedset-integration.
type Webhook struct{}

func SetupWebhook(mgr ctrl.Manager, opts ...jobframework.Option) error {
	options := jobframework.ProcessOptions(opts...)
	wh := &Webhook{}
	obj := &disaggregatedsetv1.DisaggregatedSet{}
	if options.NoopWebhook {
		return webhook.SetupNoopWebhook(mgr, obj)
	}
	return ctrl.NewWebhookManagedBy(mgr, obj).
		WithDefaulter(wh).
		WithValidator(wh).
		WithLogConstructor(roletracker.WebhookLogConstructor(options.RoleTracker)).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-disaggregatedset-x-k8s-io-v1-disaggregatedset,mutating=true,failurePolicy=fail,sideEffects=None,groups="disaggregatedset.x-k8s.io",resources=disaggregatedsets,verbs=create;update,versions=v1,name=mdisaggregatedset.kb.io,admissionReviewVersions=v1

var _ admission.Defaulter[*disaggregatedsetv1.DisaggregatedSet] = &Webhook{}

func (wh *Webhook) Default(context.Context, *disaggregatedsetv1.DisaggregatedSet) error {
	return nil
}

// +kubebuilder:webhook:path=/validate-disaggregatedset-x-k8s-io-v1-disaggregatedset,mutating=false,failurePolicy=fail,sideEffects=None,groups="disaggregatedset.x-k8s.io",resources=disaggregatedsets,verbs=create;update,versions=v1,name=vdisaggregatedset.kb.io,admissionReviewVersions=v1

var _ admission.Validator[*disaggregatedsetv1.DisaggregatedSet] = &Webhook{}

func (wh *Webhook) ValidateCreate(context.Context, *disaggregatedsetv1.DisaggregatedSet) (admission.Warnings, error) {
	return nil, nil
}

func (wh *Webhook) ValidateUpdate(context.Context, *disaggregatedsetv1.DisaggregatedSet, *disaggregatedsetv1.DisaggregatedSet) (admission.Warnings, error) {
	return nil, nil
}

func (wh *Webhook) ValidateDelete(context.Context, *disaggregatedsetv1.DisaggregatedSet) (admission.Warnings, error) {
	return nil, nil
}
