/*
Copyright 2024 The Kubernetes Authors.

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
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
	"sigs.k8s.io/controller-runtime/pkg/webhook/conversion"
)

// This code is copied from https://github.com/kubernetes-sigs/controller-runtime
// with some modifications to get full control of the construction of patches.

// Builder builds a Webhook.
type Builder struct {
	apiType         runtime.Object
	mutationHandler admission.Handler
	customValidator admission.CustomValidator
	gvk             schema.GroupVersionKind
	mgr             manager.Manager
	config          *rest.Config
	recoverPanic    bool
	logConstructor  func(base logr.Logger, req *admission.Request) logr.Logger
}

// ManagedBy returns a new webhook builder.
func ManagedBy(m manager.Manager) *Builder {
	return &Builder{mgr: m}
}

// For takes a runtime.Object which should be a CR.
func (blder *Builder) For(apiType runtime.Object) *Builder {
	blder.apiType = apiType
	return blder
}

// WithMutationHandler takes an admission.Handler inteface, a MutationgWebhook will be wired for this type.
func (blder *Builder) WithMutationHandler(handler admission.Handler) *Builder {
	blder.mutationHandler = handler
	return blder
}

// WithValidator takes a admission.CustomValidator interface, a ValidatingWebhook will be wired for this type.
func (blder *Builder) WithValidator(validator admission.CustomValidator) *Builder {
	blder.customValidator = validator
	return blder
}

// WithLogConstructor overrides the webhook's LogConstructor.
func (blder *Builder) WithLogConstructor(logConstructor func(base logr.Logger, req *admission.Request) logr.Logger) *Builder {
	blder.logConstructor = logConstructor
	return blder
}

// RecoverPanic indicates whether panics caused by the webhook should be recovered.
func (blder *Builder) RecoverPanic() *Builder {
	blder.recoverPanic = true
	return blder
}

// Complete builds the webhook.
func (blder *Builder) Complete() error {
	// Set the Config
	blder.loadRestConfig()

	// Configure the default LogConstructor
	blder.setLogConstructor()

	// Set the Webhook if needed
	return blder.registerWebhooks()
}

func (blder *Builder) loadRestConfig() {
	if blder.config == nil {
		blder.config = blder.mgr.GetConfig()
	}
}

func (blder *Builder) setLogConstructor() {
	if blder.logConstructor == nil {
		blder.logConstructor = func(base logr.Logger, req *admission.Request) logr.Logger {
			log := base.WithValues(
				"webhookGroup", blder.gvk.Group,
				"webhookKind", blder.gvk.Kind,
			)
			if req != nil {
				return log.WithValues(
					blder.gvk.Kind, klog.KRef(req.Namespace, req.Name),
					"namespace", req.Namespace, "name", req.Name,
					"resource", req.Resource, "user", req.UserInfo.Username,
					"requestID", req.UID,
				)
			}
			return log
		}
	}
}

func (blder *Builder) registerWebhooks() error {
	typ, err := blder.getType()
	if err != nil {
		return err
	}

	blder.gvk, err = apiutil.GVKForObject(typ, blder.mgr.GetScheme())
	if err != nil {
		return err
	}

	// Register webhook(s) for type
	blder.registerDefaultingWebhook()
	blder.registerValidatingWebhook()

	err = blder.registerConversionWebhook()
	if err != nil {
		return err
	}
	return nil
}

// registerDefaultingWebhook registers a defaulting webhook if necessary.
func (blder *Builder) registerDefaultingWebhook() {
	mwh := blder.getDefaultingWebhook()
	if mwh != nil {
		mwh.LogConstructor = blder.logConstructor
		path := generateMutatePath(blder.gvk)

		// Checking if the path is already registered.
		// If so, just skip it.
		if !blder.isAlreadyHandled(path) {
			blder.mgr.GetLogger().Info("Registering a mutating webhook",
				"GVK", blder.gvk,
				"path", path)
			blder.mgr.GetWebhookServer().Register(path, mwh)
		}
	}
}

func (blder *Builder) getDefaultingWebhook() *admission.Webhook {
	if handler := blder.mutationHandler; handler != nil {
		return (&admission.Webhook{Handler: handler}).WithRecoverPanic(blder.recoverPanic)
	}
	blder.mgr.GetLogger().Info(
		"skip registering a mutating webhook, WithMutationHandler wasn't called",
		"GVK", blder.gvk)
	return nil
}

// registerValidatingWebhook registers a validating webhook if necessary.
func (blder *Builder) registerValidatingWebhook() {
	vwh := blder.getValidatingWebhook()
	if vwh != nil {
		vwh.LogConstructor = blder.logConstructor
		path := generateValidatePath(blder.gvk)

		// Checking if the path is already registered.
		// If so, just skip it.
		if !blder.isAlreadyHandled(path) {
			blder.mgr.GetLogger().Info("Registering a validating webhook",
				"GVK", blder.gvk,
				"path", path)
			blder.mgr.GetWebhookServer().Register(path, vwh)
		}
	}
}

func (blder *Builder) getValidatingWebhook() *admission.Webhook {
	if validator := blder.customValidator; validator != nil {
		return admission.WithCustomValidator(blder.mgr.GetScheme(), blder.apiType, validator).WithRecoverPanic(blder.recoverPanic)
	}
	blder.mgr.GetLogger().Info(
		"skip registering a validating webhook, WithValidator wasn't called",
		"GVK", blder.gvk)
	return nil
}

func (blder *Builder) registerConversionWebhook() error {
	log := blder.mgr.GetLogger()
	ok, err := conversion.IsConvertible(blder.mgr.GetScheme(), blder.apiType)
	if err != nil {
		log.Error(err, "conversion check failed", "GVK", blder.gvk)
		return err
	}
	if ok {
		if !blder.isAlreadyHandled("/convert") {
			blder.mgr.GetWebhookServer().Register("/convert", conversion.NewWebhookHandler(blder.mgr.GetScheme()))
		}
		log.Info("Conversion webhook enabled", "GVK", blder.gvk)
	}

	return nil
}

func (blder *Builder) getType() (runtime.Object, error) {
	if blder.apiType != nil {
		return blder.apiType, nil
	}
	return nil, errors.New("For() must be called with a valid object")
}

func (blder *Builder) isAlreadyHandled(path string) bool {
	if blder.mgr.GetWebhookServer().WebhookMux() == nil {
		return false
	}
	h, p := blder.mgr.GetWebhookServer().WebhookMux().Handler(&http.Request{URL: &url.URL{Path: path}})
	if p == path && h != nil {
		return true
	}
	return false
}

func generateMutatePath(gvk schema.GroupVersionKind) string {
	return "/mutate-" + strings.ReplaceAll(gvk.Group, ".", "-") + "-" +
		gvk.Version + "-" + strings.ToLower(gvk.Kind)
}

func generateValidatePath(gvk schema.GroupVersionKind) string {
	return "/validate-" + strings.ReplaceAll(gvk.Group, ".", "-") + "-" +
		gvk.Version + "-" + strings.ToLower(gvk.Kind)
}
