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
	"errors"
	"fmt"
	"os"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/meta"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"

	"sigs.k8s.io/kueue/pkg/controller/jobs/noop"
	"sigs.k8s.io/kueue/pkg/util/cert"
)

var (
	errFailedMappingResource = errors.New("restMapper failed mapping resource")
)

func SetupControllers(mgr ctrl.Manager, log logr.Logger, certsReady chan struct{}, opts ...Option) error {
	// The controllers won't work until the webhooks are operating, and the webhook won't work until the
	// certs are all in place.
	cert.WaitForCertsReady(log, certsReady)
	options := ProcessOptions(opts...)

	return ForEachIntegration(func(name string, cb IntegrationCallbacks) error {
		logger := log.WithValues("jobFrameworkName", name)
		fwkNamePrefix := fmt.Sprintf("jobFrameworkName %q", name)

		if options.EnabledFrameworks.Has(name) {
			if cb.CanSupportIntegration != nil {
				if canSupport, err := cb.CanSupportIntegration(opts...); !canSupport || err != nil {
					log.Error(err, "Failed to configure reconcilers")
					os.Exit(1)
				}
			}
			gvk, err := apiutil.GVKForObject(cb.JobType, mgr.GetScheme())
			if err != nil {
				return fmt.Errorf("%s: %w: %w", fwkNamePrefix, errFailedMappingResource, err)
			}
			if _, err = mgr.GetRESTMapper().RESTMapping(gvk.GroupKind(), gvk.Version); err != nil {
				if !meta.IsNoMatchError(err) {
					return fmt.Errorf("%s: %w", fwkNamePrefix, err)
				}
				logger.Info("No matching API in the server for job framework, skipped setup of controller and webhook")
			} else {
				if err = cb.NewReconciler(
					mgr.GetClient(),
					mgr.GetEventRecorderFor(fmt.Sprintf("%s-%s-controller", name, options.ManagerName)),
					opts...,
				).SetupWithManager(mgr); err != nil {
					return fmt.Errorf("%s: %w", fwkNamePrefix, err)
				}
				if err = cb.SetupWebhook(mgr, opts...); err != nil {
					return fmt.Errorf("%s: unable to create webhook: %w", fwkNamePrefix, err)
				}
				logger.Info("Set up controller and webhook for job framework")
				return nil
			}
		}
		if err := noop.SetupWebhook(mgr, cb.JobType); err != nil {
			return fmt.Errorf("%s: unable to create noop webhook: %w", fwkNamePrefix, err)
		}
		return nil
	})
}

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
