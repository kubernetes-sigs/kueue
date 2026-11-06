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

package config

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apiserver/pkg/apis/apiserver"
	"k8s.io/apiserver/pkg/authentication/authenticator"
	"k8s.io/apiserver/pkg/authentication/authenticatorfactory"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	"k8s.io/apiserver/pkg/authorization/authorizerfactory"
	"k8s.io/apiserver/pkg/server/dynamiccertificates"
	authenticationv1 "k8s.io/client-go/kubernetes/typed/authentication/v1"
	authorizationv1 "k8s.io/client-go/kubernetes/typed/authorization/v1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

type metricsClientCAWatcher struct {
	ca *dynamiccertificates.DynamicFileCAContent
}

var _ manager.Runnable = (*metricsClientCAWatcher)(nil)
var _ manager.LeaderElectionRunnable = (*metricsClientCAWatcher)(nil)

func (w *metricsClientCAWatcher) Start(ctx context.Context) error {
	w.ca.Run(ctx, 1)
	return nil
}

func (w *metricsClientCAWatcher) NeedLeaderElection() bool {
	return false
}

// Snapshot roots for each handshake; request authentication separately rechecks
// the current bundle, so removing an issuer also affects reused TLS connections.
func metricsClientTLSOption(ca dynamiccertificates.CAContentProvider) func(*tls.Config) {
	return func(config *tls.Config) {
		previous := config.GetConfigForClient
		config.ClientAuth = tls.VerifyClientCertIfGiven
		config.GetConfigForClient = func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
			base := config
			if previous != nil {
				selected, err := previous(hello)
				if err != nil {
					return nil, err
				}
				if selected != nil {
					base = selected
				}
			}
			roots, ok := ca.VerifyOptions()
			if !ok {
				return nil, errors.New("metrics client CA has no valid verification roots")
			}
			snapshot := base.Clone()
			snapshot.GetConfigForClient = nil
			snapshot.ClientAuth = tls.VerifyClientCertIfGiven
			snapshot.ClientCAs = roots.Roots
			return snapshot, nil
		}
	}
}

type metricsRequestAccess struct {
	certificate authenticator.Request
	bearer      authenticator.Request
	authorizer  authorizer.Authorizer
}

func metricsCertificateOrBearerFilter(ca dynamiccertificates.CAContentProvider) func(*rest.Config, *http.Client) (metricsserver.Filter, error) {
	return func(config *rest.Config, client *http.Client) (metricsserver.Filter, error) {
		tokenClient, err := authenticationv1.NewForConfigAndClient(config, client)
		if err != nil {
			return nil, fmt.Errorf("create metrics TokenReview client: %w", err)
		}
		sarClient, err := authorizationv1.NewForConfigAndClient(config, client)
		if err != nil {
			return nil, fmt.Errorf("create metrics SubjectAccessReview client: %w", err)
		}
		certificate, _, err := (authenticatorfactory.DelegatingAuthenticatorConfig{
			Anonymous:                          &apiserver.AnonymousAuthConfig{Enabled: false},
			ClientCertificateCAContentProvider: ca,
		}).New()
		if err != nil {
			return nil, fmt.Errorf("create metrics certificate authenticator: %w", err)
		}
		bearer, _, err := (authenticatorfactory.DelegatingAuthenticatorConfig{
			Anonymous: &apiserver.AnonymousAuthConfig{Enabled: false},
			CacheTTL:  time.Minute, TokenAccessReviewClient: tokenClient,
			TokenAccessReviewTimeout: 10 * time.Second, WebhookRetryBackoff: metricsAuthRetryBackoff(),
		}).New()
		if err != nil {
			return nil, fmt.Errorf("create metrics bearer authenticator: %w", err)
		}
		access, err := (authorizerfactory.DelegatingAuthorizerConfig{
			SubjectAccessReviewClient: sarClient,
			AllowCacheTTL:             5 * time.Minute, DenyCacheTTL: 30 * time.Second,
			WebhookRetryBackoff: metricsAuthRetryBackoff(),
		}).New()
		if err != nil {
			return nil, fmt.Errorf("create metrics authorizer: %w", err)
		}
		return (&metricsRequestAccess{certificate: certificate, bearer: bearer, authorizer: access}).filter, nil
	}
}

func (a *metricsRequestAccess) filter(log logr.Logger, next http.Handler) (http.Handler, error) {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		selected := a.bearer
		certificatePresented := request.TLS != nil && len(request.TLS.PeerCertificates) != 0
		if certificatePresented {
			selected = a.certificate
		}
		identity, ok, err := selected.AuthenticateRequest(request)
		if err != nil {
			log.Error(err, "Metrics authentication failed")
			status := http.StatusInternalServerError
			if certificatePresented {
				status = http.StatusUnauthorized
			}
			http.Error(w, "Authentication failed", status)
			return
		}
		if !ok {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		attributes := authorizer.AttributesRecord{
			User: identity.User, Verb: strings.ToLower(request.Method), Path: request.URL.Path,
		}
		decision, _, err := a.authorizer.Authorize(request.Context(), attributes)
		if err != nil {
			log.Error(err, "Metrics authorization failed")
			http.Error(w, "Authorization failed", http.StatusInternalServerError)
			return
		}
		if decision != authorizer.DecisionAllow {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, request)
	}), nil
}

// Match controller-runtime's delegated metrics authentication retry policy.
func metricsAuthRetryBackoff() *wait.Backoff {
	return &wait.Backoff{Duration: 500 * time.Millisecond, Factor: 1.5, Jitter: 0.2, Steps: 5}
}
