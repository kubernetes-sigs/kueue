# KEP-5993: KueueViz Authentication

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [API Changes](#api-changes)
  - [Authentication Flow](#authentication-flow)
  - [Backend Implementation](#backend-implementation)
  - [WebSocket Authentication](#websocket-authentication)
  - [Login Page](#login-page)
  - [RBAC Updates](#rbac-updates)
  - [Helm Configuration](#helm-configuration)
  - [Frontend Considerations](#frontend-considerations)
  - [Test Plan](#test-plan)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
  - [OAuth2/OIDC Proxy](#oauth2oidc-proxy)
  - [mTLS Client Certificates](#mtls-client-certificates)
  - [Basic Authentication](#basic-authentication)
<!-- /toc -->

## Summary

KueueViz currently exposes all Kueue resources without authentication.
This KEP adds optional bearer token authentication using the Kubernetes
TokenReview API, the same mechanism Kubernetes Dashboard uses.

Authentication is configured through Helm values. In Alpha, the default mode
is `Disabled`, and users opt in via `auth.mode: TokenReview`.

## Motivation

From [Issue #5993](https://github.com/kubernetes-sigs/kueue/issues/5993):

> "The current KueueViz has no authentication, which makes it risky to expose externally.
> We would need auth to be added before we can safely use it."
> — david-gang, Issue #8969

Without authentication, KueueViz is limited to `kubectl port-foward`.
There is no way to safely expose it via Ingress, and no audit trail.

### Goals

1. Add optional authentication using Kubernetes TokenReview API
2. Support bearer token via `Authorization: Bearer <token>` header
3. Authenticate WebSocket connections at upgrade time
4. Provide a login page with token input and validation flow
5. Add metrics for authentication attempts
6. Minimize manual steps for users when authenticating

### Non-Goals

1. Authorization (SubjectAccessReview), may be added in a future KEP
2. Server-side sessions
3. OAuth2/OIDC integration, users can deploy oauth2-proxy separately
4. Token refresh, client resposibility

## Proposal

Add authentication middleware to KueueViz backend:

1. Extract bearer token from request
2. Validate via Kubernetes TokenReview API
3. Return HTTP 401 for invalid or missing tokens
4. Store user info in request context

### User Stories

**Cluster Admin**: Enable authentication via Helm so only users with valid
Kubernetes tokens can access KueueViz.

**Multi-Tenant Operator**: Expose KueueViz via Ingress to multiple teams,
with per-user authentication and audit logging.

**Developer**: Access KueueViz using existing Kubernetes credentials
(ServiceAccount token or kubeconfig token).

### Risks and Mitigations

| Risk | Mitigation |
|------|------------|
| Breaking existing deployments | Disabled by default in Alpha; opt-in via `auth.mode: TokenReview` |
| Token exposure in logs | Never log tokens |
| TokenReview latency | Cache with configurable TTL |
| API server load from invalid tokens | Negative caching (5s TTL) for failed auth results |
| WebSocket complexity | Authenticate at connection time only |
| Expired tokens on long connections | Document token lifetime |

## Design Details

### API Changes

None. Changes limited to environment variables and Helm values.

### Authentication Flow

```text
Client                    KueueViz Backend         K8s API Server
  │                             │                        │
  │ GET /ws/workloads           │                        │
  │ Authorization: Bearer <t>   │                        │
  │────────────────────────────>│                        │
  │                             │ POST tokenreviews      │
  │                             │───────────────────────>│
  │                             │ authenticated: true    │
  │                             │<───────────────────────│
  │ 200 OK / WS Upgrade         │                        │
  │<────────────────────────────│                        │
```

### Backend Implementation

**New file:** `cmd/kueueviz/backend/middleware/auth.go`

Gin middleware that extracts bearer token from `Authorization` header or
`Sec-WebSocket-Protocol` subprotocol, validates via TokenReview API, and returns HTTP 401
with `WWW-Authenticate: Bearer` on failure.

Caching protects the API server from excessive TokenReview calls:
- Successful auth results are cached with configurable TTL (default 60s).
- Failed auth results are cached with a short TTL (default 5s) so the same
  invalid token does not trigger repeated calls.
- The API server's own API Priority and Fairness (`--max-requests-inflight`)
  provides additional protection; application-layer caching is a second line.

**Modified:** `cmd/kueueviz/backend/config/server.go`

Add fields to `ServerConfig`:

- `AuthMode string` — authentication mode; one of `Disabled` or `TokenReview`
- `AuthTokenReviewAudiences []string` — optional token audiences to validate against; empty means any audience is accepted
- `AuthTokenReviewCacheTTL time.Duration` — TTL for caching successful TokenReview results (default 60s)
- `AuthTokenReviewNegativeCacheTTL time.Duration` — TTL for caching failed TokenReview results (default 5s)

Load from environment variables:

- `KUEUEVIZ_AUTH_MODE`
- `KUEUEVIZ_AUTH_TOKEN_REVIEW_AUDIENCES`
- `KUEUEVIZ_AUTH_TOKEN_REVIEW_CACHE_TTL`
- `KUEUEVIZ_AUTH_TOKEN_REVIEW_NEGATIVE_CACHE_TTL`

**Modified:** `cmd/kueueviz/backend/main.go`

- Create `kubernetes.Clientset` for TokenReview calls
- Register auth middleware when `AuthMode` is `TokenReview`

### WebSocket Authentication

Tokens can be provided via:

1. `Authorization: Bearer <token>` header (preferred)
2. `Sec-WebSocket-Protocol: kueueviz.auth.<base64-token>` subprotocol (browser fallback)

Authentication happens at the HTTP upgrade request. No per-message auth.

### Login Page

The frontend provides a login page at `/login` with:

1. A token input field
2. A "Sign In" action that stores the token for subsequent requests
3. Instructions for obtaining a token:

```text
kubectl create token <service-account> -n <namespace> --duration=168h
```

Authentication flow on the frontend:

1. Access protected API endpoints with bearer token when available.
2. On HTTP 401, clear token and redirect to `/login`.
3. User provides a token and signs in.
4. Retry protected requests with bearer token for HTTP APIs and WebSockets.
5. On later HTTP 401, clear token and redirect back to `/login`.

### RBAC Updates

Add to `charts/kueue/templates/kueueviz/clusterrole.yaml`:

```yaml
- apiGroups: ["authentication.k8s.io"]
  resources: ["tokenreviews"]
  # TokenReview is a create-only API in Kubernetes (no get/list/watch/delete).
  # The client POSTs a token and receives an authentication result.
  verbs: ["create"]
```

### Helm Configuration

`charts/kueue/values.yaml`:

```yaml
kueueViz:
  backend:
    auth:
      mode: "Disabled"          # Disabled | TokenReview
      tokenReviewConfig:
        audiences: ""           # optional, comma-separated; empty means any audience
        cacheTTL: "60s"         # how long a successful auth result is cached
        negativeCacheTTL: "5s"  # how long a failed auth result is cached to prevent API server abuse
```

`charts/kueue/templates/kueueviz/backend-deployment.yaml`:

```yaml
{{- if eq .Values.kueueViz.backend.auth.mode "TokenReview" }}
- name: KUEUEVIZ_AUTH_MODE
  value: "TokenReview"
{{- if .Values.kueueViz.backend.auth.tokenReviewConfig.audiences }}
- name: KUEUEVIZ_AUTH_TOKEN_REVIEW_AUDIENCES
  value: {{ .Values.kueueViz.backend.auth.tokenReviewConfig.audiences | quote }}
{{- end }}
- name: KUEUEVIZ_AUTH_TOKEN_REVIEW_CACHE_TTL
  value: {{ .Values.kueueViz.backend.auth.tokenReviewConfig.cacheTTL | default "60s" | quote }}
- name: KUEUEVIZ_AUTH_TOKEN_REVIEW_NEGATIVE_CACHE_TTL
  value: {{ .Values.kueueViz.backend.auth.tokenReviewConfig.negativeCacheTTL | default "5s" | quote }}
{{- end }}
```

### Frontend Considerations

The frontend handles authentication with minimal user friction:

1. When auth is enabled, the frontend detects HTTP 401 and redirects to `/login`.
2. The login page provides a token input field and a helper command:
   `kubectl create token <service-account> -n <namespace> --duration=168h`.
3. Once entered, the token is stored in sessionStorage (scoped to the browser
   tab, cleared on tab close) and attached to all subsequent HTTP and WebSocket
   requests automatically.
4. On token expiry (HTTP 401), the user is prompted to re-authenticate.

Future: automatic token acquisition via `kubectl proxy` or browser-based
OIDC, removing the need to copy-paste tokens.

### Test Plan

[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.

Unit tests (`middleware/auth_test.go`):

- Token extraction from header and WebSocket subprotocol
- Cache hit/miss
- Expired cache entries
- Disabled auth passthrough

Coverage target: >80%

Integration tests:

- Valid token → 200
- Invalid token → 401
- Missing token → 401
- Disabled auth → pass all

E2E tests (`test/e2e/kueueviz/`):

- Deploy with auth enabled
- Test with ServiceAccount token
- Test rejection of unauthenticated requests

### Graduation Criteria

Alpha (v0.17, disabled by default):

- [ ] Middleware implemented with unit tests (>80% coverage)
- [ ] Integration tests passing
- [ ] E2E tests in CI
- [ ] Helm values documented
- [ ] Negative caching for failed auth results
- [ ] INSTALL.md updated

Beta (v0.18, re-evaluate enabling by default based on user feedback):

- [ ] Positive user feedback from Alpha adopters
- [ ] Auth metrics added (success/failure counters, latency histogram)
- [ ] Frontend login flow fully integrated
- [ ] DDoS mitigation validated (negative cache + rate limiting)
- [ ] Documentation for migration from unauthenticated deployments

Stable (v0.19):

- [ ] Production usage confirmed
- [ ] Enabled by default
- [ ] Secure by default: evaluate automatic token acquisition

## Implementation History

- 2026-02-04: Initial KEP draft

## Drawbacks

Adds middleware, configuration, and RBAC complexity. Users must obtain and
manage tokens themselves. Some users may expect OAuth login instead. Note
that revoked tokens remain valid until cache expires.

Starting with Alpha disabled by default avoids breaking existing deployments.
When graduating to Beta/Stable with auth enabled by default, users upgrading
must either obtain a token or set `auth.mode: Disabled`. Migration steps
will be documented in release notes.

## Alternatives

### OAuth2/OIDC Proxy

Deploy oauth2-proxy in front of KueueViz. Provides full OIDC support but
requires external IdP and adds deployment complexity.
Users can still choose this approach.

### mTLS Client Certificates

Strong security, but complex certificate management and poor browser support.
Too complex for general use.

### Basic Authentication

Simple but requires password storage and has no Kubernetes RBAC integration.
Security concerns outweigh simplicity.
