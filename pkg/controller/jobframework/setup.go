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
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
)

const (
	baseBackoffWaitForIntegration = 1 * time.Second
	maxBackoffWaitForIntegration  = 2 * time.Minute
)

var (
	errFailedMappingResource = errors.New("restMapper failed mapping resource")
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
	return manager.setupControllers(ctx, mgr, log, opts...)
}

func (m *integrationManager) setupControllers(ctx context.Context, mgr ctrl.Manager, log logr.Logger, opts ...Option) error {
	options := ProcessOptions(opts...)

	if err := m.checkEnabledListDependencies(options.EnabledFrameworks); err != nil {
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

		if options.EnabledFrameworks.Has(name) {
			if cb.CanSupportIntegration != nil {
				if canSupport, err := cb.CanSupportIntegration(opts...); !canSupport || err != nil {
					return fmt.Errorf("failed to configure reconcilers: %w", err)
				}
			}
			gvk, err := apiutil.GVKForObject(cb.JobType, mgr.GetScheme())
			if err != nil {
				return fmt.Errorf("%s: %w: %w", fwkNamePrefix, errFailedMappingResource, err)
			}
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
				go waitForAPI(ctx, mgr, log, gvk, func() {
					log.Info("API now available, starting controller", "gvk", gvk)
					if err := m.setupControllerAndWebhook(mgr, name, fwkNamePrefix, cb, options, opts...); err != nil {
						log.Error(err, "Failed to setup controller for job framework")
					}
				})
			} else {
				if err := m.setupControllerAndWebhook(mgr, name, fwkNamePrefix, cb, options, opts...); err != nil {
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
