/*
Copyright 2026 The Kubeflow Authors.

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

package statusserver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
	"k8s.io/client-go/rest"
)

type TokenAuthorizer interface {
	Init(ctx context.Context) error
	Authorize(ctx context.Context, rawIDToken, namespace, trainJobName string) (bool, error)
}

type projectedServiceAccountTokenAuthorizer struct {
	oidcProvider *oidc.Provider
	config       *rest.Config
}

var _ TokenAuthorizer = &projectedServiceAccountTokenAuthorizer{}

// projectedToken is the decoded JWT claims of a k8s projected service account token.
// Note: we only decode the subset of claims we actually need.
type projectedToken struct {
	Issuer     string `json:"iss"`
	Kubernetes struct {
		Namespace string `json:"namespace"`
	} `json:"kubernetes.io"`
}

// NewProjectedServiceAccountTokenAuthorizer creates a validator for checking a bearer token has permission to
// update the requested train job.
func NewProjectedServiceAccountTokenAuthorizer(config *rest.Config) TokenAuthorizer {
	return &projectedServiceAccountTokenAuthorizer{
		config: config,
	}
}

func (p *projectedServiceAccountTokenAuthorizer) Init(ctx context.Context) error {
	issuerURL, err := getClusterOIDCIssuerURL()
	if err != nil {
		return fmt.Errorf("failed to discover issuer URL: %w", err)
	}

	// Create an authenticated HTTP client using the provided rest config
	httpClient, err := rest.HTTPClientFor(p.config)
	if err != nil {
		return fmt.Errorf("failed to create HTTP client: %w", err)
	}

	// Create context with the authenticated HTTP client
	ctx = oidc.ClientContext(ctx, httpClient)

	provider, err := oidc.NewProvider(ctx, issuerURL)
	if err != nil {
		return fmt.Errorf("failed to create OIDC provider: %w", err)
	}
	p.oidcProvider = provider

	return nil
}

func (p *projectedServiceAccountTokenAuthorizer) Authorize(ctx context.Context, authHeader, namespace, trainJobName string) (bool, error) {
	if p.oidcProvider == nil {
		return false, fmt.Errorf("OIDC provider has not been initialized")
	}

	rawToken := extractRawToken(authHeader)

	// Create authorizer with TrainJob-specific audience
	expectedAudience := TokenAudience(namespace, trainJobName)
	verifier := p.oidcProvider.Verifier(&oidc.Config{
		ClientID: expectedAudience,
	})

	// Check token signature, expiry, and audience
	idToken, err := verifier.Verify(ctx, rawToken)
	if err != nil {
		return false, nil
	}

	// Check token is bound to a pod in the same namespace as the train job
	parsedToken := projectedToken{}
	err = idToken.Claims(&parsedToken)
	if err != nil {
		return false, nil
	}
	if parsedToken.Kubernetes.Namespace != namespace {
		return false, nil
	}

	return true, nil
}

func extractRawToken(authHeader string) string {
	parts := strings.Split(authHeader, " ")

	if len(parts) != 2 || parts[0] != "Bearer" {
		return ""
	}

	return parts[1]
}

// getClusterOIDCIssuerURL tries to look up the cluster token issuer from the in-cluster service account token
// Different clusters may use different issuers. This is a reliable way of discovering the issuer.
func getClusterOIDCIssuerURL() (string, error) {
	tokenBytes, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/token")
	if err != nil {
		return "", err
	}

	parts := strings.Split(strings.TrimSpace(string(tokenBytes)), ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("serviceaccount token is not a jwt")
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("serviceaccount token is not a jwt: %w", err)
	}

	var token projectedToken
	if err := json.Unmarshal(payload, &token); err != nil {
		return "", fmt.Errorf("serviceaccount token is not a jwt: %w", err)
	}

	if token.Issuer == "" {
		return "", fmt.Errorf("serviceaccount token missing issuer claim")
	}

	return token.Issuer, nil
}
