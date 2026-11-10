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
	"fmt"
	"slices"
	"strings"

	authenticationv1 "k8s.io/api/authentication/v1"
	"k8s.io/apiserver/pkg/authentication/serviceaccount"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type TokenAuthorizer interface {
	Init(ctx context.Context) error
	Authorize(ctx context.Context, rawIDToken, namespace, trainJobName string) (bool, error)
}

// +kubebuilder:rbac:groups=authentication.k8s.io,resources=tokenreviews,verbs=create

type tokenReviewAuthorizer struct {
	client client.Client
}

var _ TokenAuthorizer = &tokenReviewAuthorizer{}

// NewTokenReviewAuthorizer creates a validator for checking a bearer token has permission to
// update the requested train job. Tokens are validated by the Kubernetes API server using the
// TokenReview API, so it works with any service account token issuer configured in the cluster.
func NewTokenReviewAuthorizer(c client.Client) TokenAuthorizer {
	return &tokenReviewAuthorizer{
		client: c,
	}
}

// Init is a no-op: tokens are only reviewed when status updates are received, so the authorizer
// does not depend on any external endpoint at startup.
func (t *tokenReviewAuthorizer) Init(_ context.Context) error {
	return nil
}

func (t *tokenReviewAuthorizer) Authorize(ctx context.Context, authHeader, namespace, trainJobName string) (bool, error) {
	log := ctrl.LoggerFrom(ctx).WithValues("namespace", namespace, "trainJobName", trainJobName)

	rawToken := extractRawToken(authHeader)
	if rawToken == "" {
		log.V(5).Info("Missing or malformed bearer token")
		return false, nil
	}

	// Ask the API server to check the token signature, expiry, bound pod and the TrainJob-specific audience.
	expectedAudience := TokenAudience(namespace, trainJobName)
	review := &authenticationv1.TokenReview{
		Spec: authenticationv1.TokenReviewSpec{
			Token:     rawToken,
			Audiences: []string{expectedAudience},
		},
	}
	if err := t.client.Create(ctx, review); err != nil {
		return false, fmt.Errorf("failed to review token: %w", err)
	}
	if !review.Status.Authenticated || !slices.Contains(review.Status.Audiences, expectedAudience) {
		log.V(5).Info("Token review rejected", "authenticated", review.Status.Authenticated,
			"audiences", review.Status.Audiences, "error", review.Status.Error)
		return false, nil
	}

	// Check token is issued to a service account in the same namespace as the train job
	tokenNamespace, _, err := serviceaccount.SplitUsername(review.Status.User.Username)
	if err != nil || tokenNamespace != namespace {
		log.V(5).Info("Token is not issued to a service account in the train job namespace",
			"username", review.Status.User.Username)
		return false, nil
	}

	return true, nil
}

// extractRawToken returns the credentials of an RFC 7235 "Bearer" Authorization header.
// The scheme is matched case-insensitively, and any amount of surrounding or separating
// whitespace is tolerated. An empty string is returned when the header does not carry
// exactly one bearer credential.
func extractRawToken(authHeader string) string {
	parts := strings.Fields(authHeader)

	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}

	return parts[1]
}
