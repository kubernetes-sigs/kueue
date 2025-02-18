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

package jobframework

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
	tools "k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	clientset "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	"k8s.io/apiextensions-apiserver/pkg/client/informers/externalversions"
)

const (
	baseBackoffWaitForIntegration = 1 * time.Second
	maxBackoffWaitForIntegration  = 2 * time.Minute
)

// SetupControllers setups all controllers and webhooks for integrations.
// When the platform developers implement a separate kueue-manager to manage the in-house custom jobs,
// they can easily setup controllers and webhooks for the in-house custom jobs.
//
// Note that the first argument, "mgr" must be initialized on the outside of this function.
// In addition, if the manager uses the kueue's internal cert management for the webhooks,
// this function needs to be called after the certs get ready because the controllers won't work
// until the webhooks are operating, and the webhook won't work until the
// certs are all in place.
func SetupControllers(ctx context.Context, mgr ctrl.Manager, log logr.Logger, opts ...Option) error {
	options := ProcessOptions(opts...)
	capacity := len(options.EnabledFrameworks)
	discoveredCRDs := make(chan schema.GroupVersionKind, capacity)

	enabledFrameworks := ProcessOptions(opts...)

	go watchCRDs(ctx, mgr, log, discoveredCRDs, enabledFrameworks)
	return manager.setupControllersFromDiscoveredCRDs(ctx, mgr, log, discoveredCRDs, opts...)
}

func (m *integrationManager) setupControllersFromDiscoveredCRDs(ctx context.Context, mgr ctrl.Manager, log logr.Logger, discoveredCRDs <-chan schema.GroupVersionKind, opts ...Option) error {
	options := ProcessOptions(opts...)

	if err := m.checkEnabledListDependencies(options.EnabledFrameworks); err != nil {
		return fmt.Errorf("check enabled frameworks list: %w", err)
	}

	for fwkName := range options.EnabledExternalFrameworks {
		if err := RegisterExternalJobType(fwkName); err != nil {
			return err
		}
	}

	setupChan := make(chan struct{}, 10)

	go func() {
		for gvk := range discoveredCRDs {
			setupChan <- struct{}{}
			go func(currentGVK schema.GroupVersionKind) {
				defer func() { <-setupChan }()

				var matchedName string
				var matchedCallbacks *IntegrationCallbacks
				m.mu.RLock()
				for name, cb := range m.integrations {
					if options.EnabledFrameworks.Has(name) {
						jobTypeStr := strings.ToLower(fmt.Sprintf("%T", cb.JobType))
						if name == fmt.Sprintf("%s/%s", currentGVK.Group, strings.ToLower(currentGVK.Kind)) ||
							strings.EqualFold(jobTypeStr, strings.ToLower(currentGVK.Kind)) {
							matchedName = name
							matchedCallbacks = &cb
							break
						}
					}
				}
				m.mu.RUnlock()

				if matchedCallbacks == nil {
					log.Info("No matching integration found for GVK", "gvk", currentGVK)
					return
				}

				waitForAPI(ctx, mgr, log, currentGVK, func() {
					log.Info("API available, initializing controller", "gvk", currentGVK)
					if err := m.setupControllerAndWebhook(
						mgr,
						matchedName,
						fmt.Sprintf("jobFrameworkName %q", currentGVK.Kind),
						*matchedCallbacks,
						options,
						opts...,
					); err != nil {
						log.Error(err, "Controller setup failed", "gvk", currentGVK)
					}
				})
			}(gvk)
		}
	}()

	return m.forEach(func(name string, cb IntegrationCallbacks) error {
		if !options.EnabledFrameworks.Has(name) {
			return nil
		}

		if cb.CanSupportIntegration != nil {
			if canSupport, err := cb.CanSupportIntegration(opts...); !canSupport || err != nil {
				return fmt.Errorf("jobFrameworkName %q: integration not supported: %w", name, err)
			}
		}

		return setupNoopWebhook(mgr, cb.JobType)
	})
}

func (m *integrationManager) setupControllerAndWebhook(mgr ctrl.Manager, name string, fwkNamePrefix string, cb IntegrationCallbacks, options Options, opts ...Option) error {
	if err := cb.NewReconciler(
		mgr.GetClient(),
		mgr.GetEventRecorderFor(fmt.Sprintf("%s-%s-controller", name, options.ManagerName)),
		opts...,
	).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("%s: %w", fwkNamePrefix, err)
	}
	for _, rec := range cb.NewAdditionalReconcilers {
		if err := rec(
			mgr.GetClient(),
			mgr.GetEventRecorderFor(fmt.Sprintf("%s-%s-controller", name, options.ManagerName)),
			opts...,
		).SetupWithManager(mgr); err != nil {
			return fmt.Errorf("%s: %w", fwkNamePrefix, err)
		}
	}
	if err := cb.SetupWebhook(mgr, opts...); err != nil {
		return fmt.Errorf("%s: unable to create webhook: %w", fwkNamePrefix, err)
	}
	m.enableIntegration(name)
	return nil
}

func waitForAPI(ctx context.Context, mgr ctrl.Manager, log logr.Logger, gvk schema.GroupVersionKind, action func()) {
	rateLimiter := workqueue.NewTypedItemExponentialFailureRateLimiter[string](baseBackoffWaitForIntegration, maxBackoffWaitForIntegration)
	item := gvk.String()
	for {
		err := restMappingExists(mgr, gvk)
		if err == nil {
			rateLimiter.Forget(item)
			action()
			return
		} else if !meta.IsNoMatchError(err) {
			log.Error(err, "Failed to get REST mapping for gvk", "gvk", gvk)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(rateLimiter.When(item)):
			continue
		}
	}
}

func restMappingExists(mgr ctrl.Manager, gvk schema.GroupVersionKind) error {
	_, err := mgr.GetRESTMapper().RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return fmt.Errorf("failed to get REST mapping for %v: %w", gvk, err)
	}
	return nil
}

// SetupIndexes setups the indexers for integrations.
// When the platform developers implement a separate kueue-manager to manage the in-house custom jobs,
// they can easily setup indexers for the in-house custom jobs.
//
// Note that the second argument, "indexer" needs to be the fieldIndexer obtained from the Manager.
func SetupIndexes(ctx context.Context, indexer client.FieldIndexer, opts ...Option) error {
	options := ProcessOptions(opts...)
	return ForEachIntegration(func(name string, cb IntegrationCallbacks) error {
		if options.EnabledFrameworks.Has(name) {
			if err := cb.SetupIndexes(ctx, indexer); err != nil {
				return fmt.Errorf("jobFrameworkName %q: %w", name, err)
			}
		}
		return nil
	})
}

func watchCRDs(ctx context.Context, mgr ctrl.Manager, log logr.Logger, discoveredCRDs chan schema.GroupVersionKind, opts Options) {
	crdClient, err := clientset.NewForConfig(mgr.GetConfig())
	if err != nil {
		log.Error(err, "Failed to create CRD client")
		close(discoveredCRDs)
		return
	}

	factory := externalversions.NewSharedInformerFactory(crdClient, 0)
	crdInformer := factory.Apiextensions().V1().CustomResourceDefinitions().Informer()

	crdInformer.AddEventHandler(tools.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			crd := obj.(*apiextensionsv1.CustomResourceDefinition)
			gvk := schema.GroupVersionKind{
				Group:   crd.Spec.Group,
				Version: crd.Spec.Versions[0].Name,
				Kind:    crd.Spec.Names.Kind,
			}

			for _, condition := range crd.Status.Conditions {
				if condition.Type == apiextensionsv1.Established &&
					condition.Status == apiextensionsv1.ConditionTrue {
					if opts.EnabledFrameworks.Has(fmt.Sprintf("%s/%s", gvk.Group, strings.ToLower(gvk.Kind))) {
						go waitForAPI(ctx, mgr, log, gvk, func() {
							log.Info("API now available, starting controller", "gvk", gvk)
							discoveredCRDs <- gvk
						})
						break
					}
				}
			}
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			newCRD := newObj.(*apiextensionsv1.CustomResourceDefinition)
			gvk := schema.GroupVersionKind{
				Group:   newCRD.Spec.Group,
				Version: newCRD.Spec.Versions[0].Name,
				Kind:    newCRD.Spec.Names.Kind,
			}

			for _, condition := range newCRD.Status.Conditions {
				if condition.Type == apiextensionsv1.Established &&
					condition.Status == apiextensionsv1.ConditionTrue {
					if opts.EnabledFrameworks.Has(fmt.Sprintf("%s/%s", gvk.Group, strings.ToLower(gvk.Kind))) {
						go waitForAPI(ctx, mgr, log, gvk, func() {
							log.Info("API now available, starting controller", "gvk", gvk)
							discoveredCRDs <- gvk
						})
						break
					}
				}
			}
		},
	})

	factory.Start(ctx.Done())
	factory.WaitForCacheSync(ctx.Done())
}
