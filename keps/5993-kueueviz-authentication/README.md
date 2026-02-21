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

KueueViz currently exposes all Kueue resoruces without authentication.
This KEP adds optional bearer token authentication using the Kubernetes
TokenReview API, the same mechanism Kubernetes Dashboard uses.

Authentication is enabled by default, and can be disabled with
`auth.enabled: false`.

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
4. Enable authentication by default
5. Provide a login page with token input and validation flow
6. Add metrics for authentication attempts

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
| Breaking existing deployments | Users can set `auth.enabled: false` |
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
`?token=` query param, validates via TokenReview API, caches successful
authentications, and returns HTTP 401 with `WWW-Authenticate: Bearer` on
failure.

**Modified:** `cmd/kueueviz/backend/config/server.go`

Add fields to `ServerConfig`:

- `AuthEnabled bool`
- `AuthAudiences []string`
- `AuthCacheTTL time.Duration`
- `AuthNegativeCacheTTL time.Duration`

Load from environment variables:

- `KUEUEVIZ_AUTH_ENABLED`
- `KUEUEVIZ_AUTH_AUDIENCES`
- `KUEUEVIZ_AUTH_CACHE_TTL`
- `KUEUEVIZ_AUTH_NEGATIVE_CACHE_TTL`

**Modified:** `cmd/kueueviz/backend/main.go`

- Create `kubernetes.Clientset` for TokenReview calls
- Register auth middleware when `AuthEnabled` is true

### WebSocket Authentication

Tokens can be provided via:

1. `Authorization: Bearer <token>` header (preferred)
2. `?token=<token>` query parameter (browser fallback)

Authentication happens at the HTTP upgrade request. No per-message auth.

Future versions may use `Sec-WebSocket-Protocol` header instead of query
parameter to avoid URL logging exposure.

### Login Page

The frontend provides a login page at `/login` with:

1. A token input field
2. A "Sign In" action that stores the token for subsequent requests
3. Instructions for obtaining a token:

```text
kubectl create token <service-account> -n <namespace>
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
  verbs: ["create"] # TokenReview is a create-only API
```

### Helm Configuration

`charts/kueue/values.yaml`:

```yaml
kueueViz:
  backend:
    auth:
      enabled: true           # enabled by default
      audiences: ""           # optional, comma-separated
      cacheTTL: "60s"
      negativeCacheTTL: "5s"  # cache failed auth results
```

`charts/kueue/templates/kueueviz/backend-deployment.yaml`:

```yaml
{{- if .Values.kueueViz.backend.auth.enabled }}
- name: KUEUEVIZ_AUTH_ENABLED
  value: "true"
{{- if .Values.kueueViz.backend.auth.audiences }}
- name: KUEUEVIZ_AUTH_AUDIENCES
  value: {{ .Values.kueueViz.backend.auth.audiences | quote }}
{{- end }}
- name: KUEUEVIZ_AUTH_CACHE_TTL
  value: {{ .Values.kueueViz.backend.auth.cacheTTL | default "60s" | quote }}
{{- end }}
```

### Frontend Considerations

Initial release: out of scope. Users can use `kubectl proxy` or add headers
manually.

Future: may add login dialog with token input and localStorage persistence.

### Test Plan

[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.

Unit tests (`middleware/auth_test.go`):

- Token extraction from header and query param
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

Beta (v0.17):

- [ ] Middleware implemented with unit tests (>80% coverage)
- [ ] Integration tests passing
- [ ] E2E tests in CI
- [ ] Helm values documented
- [ ] INSTALL.md updated

Stable (v0.18):

- [ ] Auth metrics added
- [ ] Positive user feedback
- [ ] Production usage confirmed

## Implementation History

- 2026-02-04: Initial KEP draft

## Drawbacks

Adds middleware, configuration, and RBAC complexity. Users must obtain and
manage tokens themselves. Some users may expect OAuth login instead. Note
that revoked tokens remain valid until cache expires.

This is a breaking change for existing deployments that had no authentication.
Users upgrading must either obtain a token or set `auth.enabled: false` in
Helm values. Migration steps will be documented in release notes.

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
