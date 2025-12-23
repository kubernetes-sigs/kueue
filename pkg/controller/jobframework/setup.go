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
	"errors"
	"fmt"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	"k8s.io/apiextensions-apiserver/pkg/client/informers/externalversions"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
	tools "k8s.io/client-go/tools/cache"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

var errFailedMappingResource = errors.New("restMapper failed mapping resource")

// SetupControllers setups all controllers and webhooks for integrations.
// When the platform developers implement a separate kueue-manager to manage the in-house custom jobs,
// they can easily setup controllers and webhooks for the in-house custom jobs.
//
// Note that the first argument, "mgr" must be initialized on the outside of this function.
// In addition, if the manager uses the kueue's internal cert management for the webhooks,
// this function needs to be called after the certs get ready because the controllers won't work
// until the webhooks are operating, and the webhook won't work until the
// certs are all in place.
func SetupControllers(ctx context.Context, mgr ctrl.Manager, opts ...Option) error {
	go manager.startCRDInformer(ctx, mgr)
	return manager.setupControllers(ctx, mgr, opts...)
}

func (m *integrationManager) setupControllers(ctx context.Context, mgr ctrl.Manager, opts ...Option) error {
	log := log.FromContext(ctx)
	options := ProcessOptions(opts...)

	implicitlyEnabledIntegrations := m.collectImplicitlyEnabledIntegrations(options.EnabledFrameworks)
	m.setImplicitlyEnabledIntegrations(implicitlyEnabledIntegrations)
	allEnabledIntegrations := options.EnabledFrameworks.Union(implicitlyEnabledIntegrations)

	if err := m.checkEnabledListDependencies(allEnabledIntegrations); err != nil {
		return fmt.Errorf("check enabled frameworks list: %w", err)
	}

	for fwkName := range options.EnabledExternalFrameworks {
		if err := RegisterExternalJobType(fwkName); err != nil {
			return err
		}
	}
	return m.forEach(func(name string, cb IntegrationCallbacks) error {
		logger := log.WithValues("jobFrameworkName", name)
		fwkNamePrefix := fmt.Sprintf("jobFrameworkName %q", name)

		if allEnabledIntegrations.Has(name) {
			if cb.CanSupportIntegration != nil {
				if canSupport, err := cb.CanSupportIntegration(opts...); !canSupport || err != nil {
					return fmt.Errorf("failed to configure reconcilers: %w", err)
				}
			}
			gvk, err := apiutil.GVKForObject(cb.JobType, mgr.GetScheme())
			if err != nil {
				return fmt.Errorf("%s: %w: %w", fwkNamePrefix, errFailedMappingResource, err)
			}
			m.ensureCRDNotifierCh(gvk)
			if err := restMappingExists(mgr, gvk); err != nil {
				if !meta.IsNoMatchError(err) {
					return fmt.Errorf("%s: %w", fwkNamePrefix, err)
				}
				// Webhook must be registered now; controller can be registered later.
				// The issue is that the controller-runtime silently ignores attempts to update the webhook
				// for an endpoint that already has one and we don't want the NoopWebhook to be installed.
				if err := cb.SetupWebhook(mgr, opts...); err != nil {
					return fmt.Errorf("%s: unable to create webhook: %w", fwkNamePrefix, err)
				}
				logger.Info("No matching API in the server for job framework, deferring setting up controller")
				go m.waitForAPI(ctx, mgr, gvk, func() {
					log.Info("API now available, starting controller", "gvk", gvk)
					if err := m.setupControllerAndWebhook(ctx, mgr, name, fwkNamePrefix, cb, options, opts...); err != nil {
						log.Error(err, "Failed to setup controller for job framework")
					}
				})
			} else {
				if err := m.setupControllerAndWebhook(ctx, mgr, name, fwkNamePrefix, cb, options, opts...); err != nil {
					return err
				}
			}
		}
		if err := setupNoopWebhook(mgr, cb.JobType); err != nil {
			return fmt.Errorf("%s: unable to create noop webhook: %w", fwkNamePrefix, err)
		}
		return nil
	})
}

func (m *integrationManager) setupControllerAndWebhook(ctx context.Context, mgr ctrl.Manager, name string, fwkNamePrefix string, cb IntegrationCallbacks, options Options, opts ...Option) error {
	if r, err := cb.NewReconciler(
		ctx,
		mgr.GetClient(),
		mgr.GetFieldIndexer(),
		mgr.GetEventRecorderFor(fmt.Sprintf("%s-%s-controller", name, options.ManagerName)),
		opts...,
	); err != nil {
		return fmt.Errorf("%s: %w", fwkNamePrefix, err)
	} else if err := r.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("%s: %w", fwkNamePrefix, err)
	}

	for _, rec := range cb.NewAdditionalReconcilers {
		if r, err := rec(
			ctx,
			mgr.GetClient(),
			mgr.GetFieldIndexer(),
			mgr.GetEventRecorderFor(fmt.Sprintf("%s-%s-controller", name, options.ManagerName)),
			opts...,
		); err != nil {
			return fmt.Errorf("%s: %w", fwkNamePrefix, err)
		} else if err := r.SetupWithManager(mgr); err != nil {
			return fmt.Errorf("%s: %w", fwkNamePrefix, err)
		}
	}
	if err := cb.SetupWebhook(mgr, opts...); err != nil {
		return fmt.Errorf("%s: unable to create webhook: %w", fwkNamePrefix, err)
	}
	m.enableIntegration(name)
	return nil
}

func (m *integrationManager) waitForAPI(ctx context.Context, mgr ctrl.Manager, gvk schema.GroupVersionKind, action func()) {
	log := log.FromContext(ctx)
	crdNotifyCh := m.getCRDNotifierCh(gvk)
	if crdNotifyCh == nil {
		log.V(2).Info("Channel not found for gvk", "gvk", gvk)
		return
	}
	for {
		err := restMappingExists(mgr, gvk)
		if err == nil {
			action()
			return
		} else if !meta.IsNoMatchError(err) {
			log.Error(err, "Failed to get REST mapping for gvk", "gvk", gvk)
		}
		select {
		case <-ctx.Done():
			return
		case <-crdNotifyCh:
			log.V(2).Info("Received CRD notification, checking API availability", "gvk", gvk)
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

	allEnabledIntegrations := options.EnabledFrameworks.Union(manager.collectImplicitlyEnabledIntegrations(options.EnabledFrameworks))
	return ForEachIntegration(func(name string, cb IntegrationCallbacks) error {
		if allEnabledIntegrations.Has(name) {
			if err := cb.SetupIndexes(ctx, indexer); err != nil {
				return fmt.Errorf("jobFrameworkName %q: %w", name, err)
			}
		}
		return nil
	})
}

// startCRDInformer watches for CRD additions/updates and notifies waitForAPI immediately
func (m *integrationManager) startCRDInformer(ctx context.Context, mgr ctrl.Manager) {
	log := log.FromContext(ctx)
	crdClient, err := clientset.NewForConfig(mgr.GetConfig())
	if err != nil {
		log.V(2).Info("Failed to create CRD client for informer, falling back to polling", "error", err)
		return
	}

	factory := externalversions.NewSharedInformerFactory(crdClient, 0)
	crdInformer := factory.Apiextensions().V1().CustomResourceDefinitions().Informer()

	informerCtx, informerCancel := context.WithCancel(ctx)

	handler := tools.ResourceEventHandlerFuncs{
		AddFunc: func(obj any) {
			crd := obj.(*apiextensionsv1.CustomResourceDefinition)
			if isCRDEstablished(crd) {
				m.notifyCRDAvailable(ctx, crd)
				m.cancelInformerIfAllCRDsRegistered(informerCancel)
			}
		},
		UpdateFunc: func(oldObj, newObj any) {
			crd := newObj.(*apiextensionsv1.CustomResourceDefinition)
			if isCRDEstablished(crd) {
				m.notifyCRDAvailable(ctx, crd)
				m.cancelInformerIfAllCRDsRegistered(informerCancel)
			}
		},
	}

	if _, err := crdInformer.AddEventHandler(handler); err != nil {
		log.V(2).Info("Failed to add CRD informer handler, falling back to polling", "error", err)
		return
	}

	factory.Start(ctx.Done())
	if !tools.WaitForCacheSync(ctx.Done(), crdInformer.HasSynced) {
		log.V(2).Info("CRD informer cache failed to sync, falling back to polling")
		return
	}

	log.V(2).Info("CRD informer started successfully")

	defer informerCancel()

	select {
	case <-ctx.Done():
		log.V(2).Info("CRD informer context done, stopping informer")
	case <-informerCtx.Done():
		factory.Shutdown()
		log.V(2).Info("All CRDs registered, stopping informer")
	}
}

func (m *integrationManager) cancelInformerIfAllCRDsRegistered(cancel context.CancelFunc) {
	defer m.crdNotifiersMu.Unlock()
	m.crdNotifiersMu.Lock()
	if len(m.crdNotifiers) == 0 {
		cancel()
	}
}

// notifyCRDAvailable notifies all waiters for this CRD's GVK
func (m *integrationManager) notifyCRDAvailable(ctx context.Context, crd *apiextensionsv1.CustomResourceDefinition) {
	log := log.FromContext(ctx)
	version := getCrdVersion(crd.Spec.Versions)
	gvk := schema.GroupVersionKind{
		Group:   crd.Spec.Group,
		Version: version,
		Kind:    crd.Spec.Names.Kind,
	}

	m.crdNotifiersMu.Lock()
	defer m.crdNotifiersMu.Unlock()

	if notifier, exists := m.crdNotifiers[gvk]; exists {
		log.V(2).Info("CRD established, notifying waiters", "gvk", gvk)
		close(notifier)
		delete(m.crdNotifiers, gvk)
	}
}

// getCrdVersion returns the current CRD version
func getCrdVersion(versions []apiextensionsv1.CustomResourceDefinitionVersion) string {
	var version string
	for _, v := range versions {
		if v.Storage {
			version = v.Name
			break
		}
	}
	return version
}

// getCRDNotifierCh returns the pre-created channel for the given GVK so waitForAPI can watch
func (m *integrationManager) getCRDNotifierCh(gvk schema.GroupVersionKind) chan struct{} {
	return m.crdNotifiers[gvk]
}

// ensureCRDNotifierCh creates a CRD notifier channel for the given GVK if it doesn't exist
func (m *integrationManager) ensureCRDNotifierCh(gvk schema.GroupVersionKind) {
	m.crdNotifiersMu.Lock()
	defer m.crdNotifiersMu.Unlock()
	if _, exists := m.crdNotifiers[gvk]; !exists {
		m.crdNotifiers[gvk] = make(chan struct{})
	}
}

// isCRDEstablished checks if a CRD has the Established condition
func isCRDEstablished(crd *apiextensionsv1.CustomResourceDefinition) bool {
	for _, condition := range crd.Status.Conditions {
		if condition.Type == apiextensionsv1.Established && condition.Status == apiextensionsv1.ConditionTrue {
			return true
		}
	}
	return false
}
