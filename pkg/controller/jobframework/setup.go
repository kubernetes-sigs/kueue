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

package jobframework

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	"k8s.io/apiextensions-apiserver/pkg/client/informers/externalversions"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	tools "k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
)

const (
	baseBackoffWaitForIntegration = 1 * time.Second
	maxBackoffWaitForIntegration  = 2 * time.Minute
	setupRetryDelay               = 5 * time.Second
	maxSetupRetries               = 5
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

	// Setup controllers for static/built-in types immediately if any
	if err := manager.setupStaticControllers(mgr, log, opts...); err != nil {
		return err
	}

	// Start watching CRDs for dynamic types
	go watchCRDs(ctx, mgr, log, chan<- schema.GroupVersionKind(discoveredCRDs), options)

	return manager.setupControllersFromDiscoveredCRDs(ctx, mgr, log, discoveredCRDs, opts...)
}

func (m *integrationManager) setupControllersFromDiscoveredCRDs(ctx context.Context, mgr ctrl.Manager, log logr.Logger, discoveredCRDs chan schema.GroupVersionKind, opts ...Option) error {
	options := ProcessOptions(opts...)

	if err := m.checkEnabledListDependencies(options.EnabledFrameworks); err != nil {
		return fmt.Errorf("check enabled frameworks list: %w", err)
	}

	for fwkName := range options.EnabledExternalFrameworks {
		if err := RegisterExternalJobType(fwkName); err != nil {
			return err
		}
	}

	var setupWg sync.WaitGroup

	go func() {
		for gvk := range discoveredCRDs {
			setupWg.Add(1)
			func(currentGVK schema.GroupVersionKind) {
				defer setupWg.Done()

				var matchedName string
				var matchedCallbacks *IntegrationCallbacks
				m.mu.RLock()
				for name, cb := range m.integrations {
					if options.EnabledFrameworks.Has(name) {
						jobGVK, err := apiutil.GVKForObject(cb.JobType, mgr.GetScheme())
						if err == nil && jobGVK == currentGVK {
							matchedName = name
							matchedCallbacks = &cb
							break
						}
					}
				}
				m.mu.RUnlock()

				if matchedCallbacks == nil {
					log.V(2).Info("No matching integration found for GVK", "gvk", currentGVK)
					return
				}

				waitForAPI(ctx, mgr, log.WithValues("integration", matchedName), currentGVK, func() {
					log.V(2).Info("API available, verifying readiness before initializing controller", "gvk", currentGVK, "integration", matchedName)

					err := verifyAPIReady(ctx, mgr, mgr.GetClient(), mgr.GetScheme(), currentGVK, log)
					if err != nil {
						log.V(2).Error(err, "API verification failed, will retry", "gvk", currentGVK)
						go func() {
							select {
							case <-ctx.Done():
								return
							case <-time.After(setupRetryDelay):
								waitForAPI(ctx, mgr, log, currentGVK, func() {
									setupControllerWithRetry(ctx, mgr, log, m, matchedName, currentGVK, *matchedCallbacks, options, opts...)
								})
							}
						}()
						return
					}

					log.V(2).Info("API verified, initializing controller", "gvk", currentGVK, "integration", matchedName)
					setupControllerWithRetry(ctx, mgr, log, m, matchedName, currentGVK, *matchedCallbacks, options, opts...)
				})
			}(gvk)
		}
		setupWg.Wait()
	}()

	setupWg.Wait()

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

// verifyAPIReady checks if the API server is ready to serve resources of the given GVK
func verifyAPIReady(ctx context.Context, mgr ctrl.Manager, c client.Client, scheme *runtime.Scheme, gvk schema.GroupVersionKind, log logr.Logger) error {
	log.V(2).Info("Checking if REST mapping exists for GVK", "gvk", gvk)
	if err := restMappingExists(mgr, gvk); err != nil {
		log.V(2).Error(err, "REST mapping not available", "gvk", gvk)
		return fmt.Errorf("REST mapping not available: %w", err)
	}

	listGVK := schema.GroupVersionKind{
		Group:   gvk.Group,
		Version: gvk.Version,
		Kind:    gvk.Kind + "List",
	}

	log.V(2).Info("Creating list object for GVK", "listGVK", listGVK)
	listObj, err := scheme.New(listGVK)
	if err != nil {
		log.V(2).Error(err, "Failed to create list object for GVK", "listGVK", listGVK)
		emptyObj, err := scheme.New(gvk)
		if err != nil {
			log.V(2).Error(err, "Failed to create object for GVK", "gvk", gvk)
			return fmt.Errorf("failed to create object for GVK %s: %w", gvk, err)
		}

		listType := reflect.TypeOf(emptyObj)
		if listType.Kind() == reflect.Ptr {
			listType = listType.Elem()
		}
		listObj = reflect.New(reflect.SliceOf(listType)).Interface().(runtime.Object)
	}

	log.V(2).Info("Listing resources for GVK", "gvk", gvk)
	err = c.List(ctx, listObj.(client.ObjectList), &client.ListOptions{Limit: 1})

	if err != nil && !apierrors.IsNotFound(err) && !meta.IsNoMatchError(err) {
		log.V(2).Error(err, "Failed to list resources for GVK", "gvk", gvk)
		return fmt.Errorf("failed to list resources for GVK %s: %w", gvk, err)
	}

	log.V(2).Info("API is ready for GVK", "gvk", gvk)
	return nil
}

func setupControllerWithRetry(ctx context.Context, mgr ctrl.Manager, log logr.Logger, m *integrationManager, name string, gvk schema.GroupVersionKind, cb IntegrationCallbacks, options Options, opts ...Option) error {
	fwkNamePrefix := fmt.Sprintf("jobFrameworkName %q", gvk.Kind)

	for retries := 0; retries < maxSetupRetries; retries++ {
		err := m.setupControllerAndWebhook(mgr, name, fwkNamePrefix, cb, options, opts...)
		if err == nil {
			log.V(2).Info("Controller setup successful", "gvk", gvk, "integration", name)
			return nil
		}

		log.V(2).Error(err, "Controller setup failed, will retry",
			"gvk", gvk,
			"integration", name,
			"attempt", retries+1,
			"requeuingCount", maxSetupRetries,
		)

		backoff := &wait.Backoff{
			Duration: baseBackoffWaitForIntegration,
			Factor:   2,
			Jitter:   0.0001,
			Steps:    maxSetupRetries - retries,
		}
		var waitDuration time.Duration
		for backoff.Steps > 0 {
			waitDuration = min(backoff.Step(), maxBackoffWaitForIntegration)
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("setup cancelled after %d retries: %w", retries+1, err)
		case <-time.After(waitDuration):
		}
	}
	return fmt.Errorf("failed to set up controller after %d retries", maxSetupRetries)
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
			log.V(2).Info("API ready, proceeding with action", "gvk", gvk)
			action()
			return
		} else if !meta.IsNoMatchError(err) {
			log.V(0).Error(err, "Failed to get REST mapping for gvk", "gvk", gvk)
		}
		select {
		case <-ctx.Done():
			log.V(1).Info("Context done, aborting API wait", "gvk", gvk)
			return
		case <-time.After(rateLimiter.When(item)):
			log.V(3).Info("API not ready, waiting", "gvk", gvk, "waitDuration", rateLimiter.When(item))
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

func watchCRDs(ctx context.Context, mgr ctrl.Manager, log logr.Logger, discoveredCRDs chan<- schema.GroupVersionKind, opts Options) {
	crdClient, err := clientset.NewForConfig(mgr.GetConfig())
	if err != nil {
		log.V(2).Error(err, "Failed to create CRD client")
		close(discoveredCRDs)
		return
	}

	factory := externalversions.NewSharedInformerFactory(crdClient, 0)
	crdInformer := factory.Apiextensions().V1().CustomResourceDefinitions().Informer()

	_, err = crdInformer.AddEventHandler(tools.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			crd := obj.(*apiextensionsv1.CustomResourceDefinition)
			gvk := schema.GroupVersionKind{
				Group:   crd.Spec.Group,
				Version: crd.Spec.Versions[0].Name,
				Kind:    crd.Spec.Names.Kind,
			}
			log.V(2).Info("CRD added", "gvk", gvk, "conditions", crd.Status.Conditions)
			if isCRDEstablished(crd) {
				frameworkKey := fmt.Sprintf("%s/%s", gvk.Group, strings.ToLower(gvk.Kind))
				if opts.EnabledFrameworks.Has(frameworkKey) {
					log.V(2).Info("Framework enabled, waiting for API", "gvk", gvk, "frameworkKey", frameworkKey)
					go waitForAPI(ctx, mgr, log, gvk, func() {
						log.V(2).Info("API now available, sending GVK", "gvk", gvk)
						discoveredCRDs <- gvk
					})
				}
			}
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			crd := newObj.(*apiextensionsv1.CustomResourceDefinition)
			gvk := schema.GroupVersionKind{
				Group:   crd.Spec.Group,
				Version: crd.Spec.Versions[0].Name,
				Kind:    crd.Spec.Names.Kind,
			}
			log.V(2).Info("CRD updated", "gvk", gvk, "conditions", crd.Status.Conditions)
			if isCRDEstablished(crd) {
				frameworkKey := fmt.Sprintf("%s/%s", gvk.Group, strings.ToLower(gvk.Kind))
				if opts.EnabledFrameworks.Has(frameworkKey) {
					log.V(2).Info("Framework enabled, waiting for API", "gvk", gvk, "frameworkKey", frameworkKey)
					go waitForAPI(ctx, mgr, log, gvk, func() {
						log.V(2).Info("API now available, sending GVK", "gvk", gvk)
						discoveredCRDs <- gvk
					})
				}
			}
		},
	})
	if err != nil {
		log.V(2).Error(err, "Failed to add event handler to CRD informer")
		close(discoveredCRDs)
		return
	}

	factory.Start(ctx.Done())
	if !tools.WaitForCacheSync(ctx.Done(), crdInformer.HasSynced) {
		log.V(2).Error(nil, "Failed to sync informer cache")
		close(discoveredCRDs)
		return
	}
	log.V(2).Info("CRD informer cache synced successfully")
}

// setupStaticControllers setups controllers for static/built-in types
func (m *integrationManager) setupStaticControllers(mgr ctrl.Manager, log logr.Logger, opts ...Option) error {
	options := ProcessOptions(opts...)
	builtInGroups := sets.NewString("", "apps", "batch")
	return m.forEach(func(name string, cb IntegrationCallbacks) error {
		if !options.EnabledFrameworks.Has(name) {
			return nil
		}
		fwkNamePrefix := fmt.Sprintf("jobFrameworkName %q", name)
		gvk, err := apiutil.GVKForObject(cb.JobType, mgr.GetScheme())
		if err != nil {
			return fmt.Errorf("%s: failed to get GVK: %w", fwkNamePrefix, err)
		}
		log.V(2).Info("Processing integration", "name", name, "gvk", gvk)
		if builtInGroups.Has(gvk.Group) {
			if err := restMappingExists(mgr, gvk); err == nil {
				log.V(2).Info("API exists, setting up static controller", "gvk", gvk)
				return m.setupControllerAndWebhook(mgr, name, fwkNamePrefix, cb, options, opts...)
			} else if !meta.IsNoMatchError(err) {
				return fmt.Errorf("%s: failed to verify API: %w", fwkNamePrefix, err)
			}
			log.V(2).Info("API not available for static type, skipping", "gvk", gvk)
		} else {
			log.V(2).Info("Skipping non-static integration", "name", name, "gvk", gvk)
		}
		return nil
	})
}

func isCRDEstablished(crd *apiextensionsv1.CustomResourceDefinition) bool {
	for _, condition := range crd.Status.Conditions {
		if condition.Type == apiextensionsv1.Established && condition.Status == apiextensionsv1.ConditionTrue {
			return true
		}
	}
	return false
}
