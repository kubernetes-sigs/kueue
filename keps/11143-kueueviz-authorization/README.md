# KEP-11143: KueueViz Authorization

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
  - [Authorization Semantics](#authorization-semantics)
  - [Endpoint Authorization Policy](#endpoint-authorization-policy)
  - [Notes/Constraints/Caveats](#notesconstraintscaveats)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [Authentication Context](#authentication-context)
  - [SubjectAccessReview Authorizer](#subjectaccessreview-authorizer)
  - [Request Handling](#request-handling)
  - [WebSocket Handling](#websocket-handling)
  - [Partial Results for Aggregated Views](#partial-results-for-aggregated-views)
  - [Caching](#caching)
  - [RBAC Updates](#rbac-updates)
  - [Helm Configuration](#helm-configuration)
  - [Frontend Considerations](#frontend-considerations)
  - [Test Plan](#test-plan)
    - [Prerequisite testing updates](#prerequisite-testing-updates)
    - [Unit tests](#unit-tests)
    - [Integration tests](#integration-tests)
    - [e2e tests](#e2e-tests)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
  - [Only authorize Pod YAML requests](#only-authorize-pod-yaml-requests)
  - [Use per-user Kubernetes clients](#use-per-user-kubernetes-clients)
  - [Use impersonation instead of SubjectAccessReview](#use-impersonation-instead-of-subjectaccessreview)
  - [Rely on frontend filtering](#rely-on-frontend-filtering)
  - [Keep authorization optional](#keep-authorization-optional)
<!-- /toc -->

## Summary

KueueViz authentication, introduced by
[KEP-5993](../5993-kueueviz-authentication/README.md), validates that an
incoming bearer token belongs to a Kubernetes user. It does not validate that
the authenticated user is allowed to read the Kubernetes objects returned by the
KueueViz backend.

This KEP adds Kubernetes RBAC authorization to KueueViz when authentication is
enabled. KueueViz will use `SubjectAccessReview` to decide whether the
authenticated user can read each resource represented by a REST or WebSocket
request. The backend may continue using its service account and shared informer
cache to read cluster state, but it must filter or reject responses according
to the requesting user's Kubernetes RBAC permissions before data leaves the
backend.

## Motivation

From [Issue #11143](https://github.com/kubernetes-sigs/kueue/issues/11143):

> Currently the kueueviz backend only authenticate incoming requests and doesn't
> authorize them. It leads to a vulnerability where a user with limited access
> to some namespaces, can send GET requests and retrieve information about Pods
> in other namespaces.

The current `TokenReview` flow prevents unauthenticated access, but it still
creates a confused deputy: users authenticate as themselves, then receive data
read by the KueueViz backend service account. If that service account has
cluster-wide read permissions, a user with access to only one namespace may be
able to read Pods, Events, Workloads, LocalQueues, or other resources in other
namespaces through KueueViz.

KueueViz must enforce the same authorization boundary users would encounter
when using `kubectl get` directly against the Kubernetes API server.

### Goals

1. Enforce Kubernetes RBAC authorization for KueueViz API and WebSocket
   responses when `auth.mode` is `TokenReview`.
2. Use Kubernetes `SubjectAccessReview` with the authenticated user's identity
   from `TokenReview`.
3. Deny direct object requests when the user lacks permission to read the
   requested resource.
4. Filter aggregated views so users only receive objects and related details
   they are authorized to read.
5. Keep the existing shared informer/cache architecture for KueueViz backend.
6. Fail closed when authorization cannot be determined.
7. Add tests that demonstrate a user with access to one namespace cannot read
   resources from another namespace through KueueViz.

### Non-Goals

1. Replacing the KueueViz authentication mechanism from KEP-5993.
2. Adding OAuth2, OIDC, mTLS, or server-side sessions.
3. Creating one Kubernetes client or informer cache per end user.
4. Changing Kubernetes RBAC semantics or introducing KueueViz-specific roles.
5. Redesigning the KueueViz UI or API schemas beyond necessary authorization
   failures and filtered aggregate results.
6. Per-message authentication or token refresh for existing WebSocket
   connections.
7. Supporting authorization when `auth.mode` is `Disabled`. Without an
   authenticated user identity, KueueViz cannot evaluate user RBAC.

## Proposal

KueueViz will add an authorization layer after authentication and before any
handler returns Kubernetes object data to the client.

At request time:

1. The authentication middleware validates the token using `TokenReview`.
2. The middleware stores the full authenticated `UserInfo` in the request
   context.
3. Handlers build one or more Kubernetes resource access checks from the
   endpoint semantics.
4. The authorizer evaluates those checks with `SubjectAccessReview`.
5. The handler either returns the requested data, returns HTTP 403, closes the
   WebSocket, or filters unauthorized objects from an aggregate response.

The KueueViz backend service account remains responsible for reading data from
the Kubernetes API server and maintaining informers. The new rule is that the
backend must not disclose data unless the requesting user is authorized to read
the corresponding Kubernetes resource.

### User Stories

**Namespace-scoped user**: A user can list workloads, local queues, events, and
pods in a namespace where they have read permissions. The same user cannot use
KueueViz to read pods or workload details in a namespace where they do not have
RBAC access.

**Cluster admin**: A cluster admin can continue using KueueViz to inspect
cluster-scoped resources such as ClusterQueues, Cohorts, ResourceFlavors, and
Nodes when their RBAC grants those permissions.

**Multi-tenant operator**: An operator can expose KueueViz through Ingress with
`auth.mode: TokenReview` and rely on Kubernetes RBAC to keep tenant data
separated.

### Authorization Semantics

KueueViz will treat Kubernetes RBAC as the source of truth for all protected
resources. An authenticated user can see a resource through KueueViz only if a
Kubernetes `SubjectAccessReview` for that user allows the equivalent `get`,
`list`, or `watch` operation.

For REST endpoints that return a single object, the relevant verb is `get`.
For WebSocket endpoints, KueueViz sends an initial snapshot and then sends new
snapshots after informer-triggered changes. These endpoints must require the
equivalent `watch` authorization in addition to the read authorization for the
initial data. List-style streams require `list` and `watch`; detail streams
require `get` for the named object and `watch` for the streamed resource in the
same scope. If an endpoint is implemented as a one-shot snapshot instead of a
stream, it may omit the `watch` check.

Cluster-scoped resources use an empty namespace in the access review.
Namespace-scoped resources must include a namespace. Requests for a namespaced
resource without a namespace are rejected with HTTP 400 instead of being
interpreted as all namespaces.

For `SubjectAccessReview` resource attributes, `name` is set only for named
object checks such as `get`. The `name` field must be empty for `list` and
`watch` checks so the authorization request matches Kubernetes API server
semantics.

### Endpoint Authorization Policy

| Endpoint | Required access | Response when denied |
|----------|-----------------|----------------------|
| `/api/workload/{name}?namespace={ns}&output=yaml` | `get` `workloads.kueue.x-k8s.io` in `{ns}` | 403 |
| `/api/localqueue/{name}?namespace={ns}&output=yaml` | `get` `localqueues.kueue.x-k8s.io` in `{ns}` | 403 |
| `/api/pod/{name}?namespace={ns}&output=yaml` | `get` `pods` in `{ns}` | 403 |
| `/api/event/{name}?namespace={ns}&output=yaml` | `get` `events` in `{ns}` | 403 |
| `/api/clusterqueue/{name}?output=yaml` | `get` `clusterqueues.kueue.x-k8s.io` | 403 |
| `/api/cohort/{name}?output=yaml` | `get` `cohorts.kueue.x-k8s.io` | 403 |
| `/api/resourceflavor/{name}?output=yaml` | `get` `resourceflavors.kueue.x-k8s.io` | 403 |
| `/api/node/{name}?output=yaml` | `get` `nodes` | 403 |
| `/ws/workloads?namespace={ns}` | `list` and `watch` `workloads.kueue.x-k8s.io` in `{ns}` | 403 before upgrade |
| `/ws/workloads` | `list` and `watch` `workloads.kueue.x-k8s.io` per namespace | filtered namespaces |
| `/ws/workload/{ns}/{name}` | `get` `workloads.kueue.x-k8s.io` named `{name}` in `{ns}`; `watch` `workloads.kueue.x-k8s.io` in `{ns}` | 403 before upgrade |
| `/ws/workload/{ns}/{name}/events` | `get` `workloads.kueue.x-k8s.io` named `{name}` in `{ns}`; `list` and `watch` `events` in `{ns}` | 403 before upgrade |
| `/ws/local-queues` | `list` and `watch` `localqueues.kueue.x-k8s.io` per namespace | filtered namespaces |
| `/ws/local-queue/{ns}/{name}` | `get` `localqueues.kueue.x-k8s.io` named `{name}` in `{ns}`; `watch` `localqueues.kueue.x-k8s.io` in `{ns}` | 403 before upgrade |
| `/ws/local-queue/{ns}/{name}/workloads` | `list` and `watch` `workloads.kueue.x-k8s.io` in `{ns}` | 403 before upgrade |
| `/ws/namespaces` | `list` and `watch` `localqueues.kueue.x-k8s.io` per namespace | filtered namespaces |
| `/ws/cluster-queues` | `list` and `watch` `clusterqueues.kueue.x-k8s.io` | 403 before upgrade |
| `/ws/cluster-queue/{name}` | `get` `clusterqueues.kueue.x-k8s.io` named `{name}`; `watch` `clusterqueues.kueue.x-k8s.io`; related LocalQueues require per-namespace `list` and `watch` `localqueues` | 403 before upgrade for primary checks; return authorized related queues only |
| `/ws/cohorts` | `list` and `watch` `cohorts.kueue.x-k8s.io`; related ClusterQueues require `list` and `watch` `clusterqueues` | 403 before upgrade for primary checks; omit unauthorized related data |
| `/ws/cohort/{name}` | `get` `cohorts.kueue.x-k8s.io` named `{name}`; `watch` `cohorts.kueue.x-k8s.io`; related ClusterQueues require `list` and `watch` `clusterqueues` | 403 before upgrade for primary checks; omit unauthorized related data |
| `/ws/resource-flavors` | `list` and `watch` `resourceflavors.kueue.x-k8s.io` | 403 before upgrade |
| `/ws/resource-flavor/{name}` | `get` `resourceflavors.kueue.x-k8s.io` named `{name}`; `watch` `resourceflavors.kueue.x-k8s.io`; related Nodes require `list` and `watch` `nodes`; related ClusterQueues require `list` and `watch` `clusterqueues` | 403 before upgrade for primary checks; omit unauthorized related data |
| `/ws/workloads/dashboard?namespace={ns}` | non-empty `{ns}`; `list` and `watch` Workloads in `{ns}`; Pods require `list` and `watch` pods in `{ns}`; cluster-scoped panels require their own `list` and `watch` checks | 400 for missing or empty namespace; 403 before upgrade for primary checks; omit unauthorized panels/details |

### Notes/Constraints/Caveats

KueueViz currently has endpoints that combine several resource types in one
view. Authorization for these views must be resource-specific. A user allowed
to read a Workload is not automatically allowed to read the Pods owned by that
Workload, the LocalQueue it references, or the ClusterQueue backing that
LocalQueue.

The first implementation should prefer a simple, explicit mapping from
endpoint to resource checks. It should not introduce a generic policy language
or a KueueViz-specific authorization API.

KueueViz should continue to use the backend service account for informer
watches. Users are authorized for returned data, not for the backend's internal
watch operation.

The current Cohort WebSocket handlers fetch Cohort objects but only register
ClusterQueue informers. The alpha implementation must add Cohort informer
coverage for endpoints that require `watch` on `cohorts.kueue.x-k8s.io`, or it
must not claim Cohort watch semantics.

The `/ws/workloads/dashboard` endpoint is namespace-scoped for alpha. An empty
or missing `namespace` query parameter is rejected with HTTP 400 before the
WebSocket upgrade. A cluster-wide dashboard would require all-namespace
filtering across Workloads, Pods, LocalQueues, ClusterQueues, and
ResourceFlavors, and is out of scope for the first implementation.

The frontend currently initializes the dashboard with an empty namespace
selection. The alpha implementation must avoid opening
`/ws/workloads/dashboard?namespace=` by selecting an authorized namespace first
or by showing an empty-state prompt until the user chooses one.

### Risks and Mitigations

| Risk | Mitigation |
|------|------------|
| Authorization gaps in aggregate endpoints | Define endpoint-to-resource policy in one backend package and cover each route with tests |
| API server load from repeated SubjectAccessReview calls | Add bounded TTL cache keyed by user identity and resource attributes |
| Stale authorization after RBAC changes | Keep authorization cache TTL short and document the delay |
| Confusing partial dashboard data | Return only authorized panels/details and expose generic forbidden messages without leaking object names |
| Regression for unauthenticated deployments | Keep `auth.mode: Disabled` behavior unchanged, but document that authorization requires TokenReview |
| Security-sensitive error messages | Do not reveal whether a denied object exists; return 403 for denied and 404 only after an allowed get fails |

## Design Details

### Authentication Context

The authentication middleware will store full Kubernetes user information from
`TokenReview.status.user` in Gin context:

- `username`
- `uid`
- `groups`
- `extra`

The cache entry for successful TokenReview results will retain the same user
information. Authorization must not reconstruct identity from only the username
because Kubernetes authorization can depend on groups and user extras.

This requires updating the authentication implementation introduced by
KEP-5993. The alpha implementation must store the full `UserInfo` in both the
Gin context and the TokenReview cache before any SubjectAccessReview-based
authorizer is wired into request handling.

### SubjectAccessReview Authorizer

KueueViz will add a backend authorizer that creates
`authorization.k8s.io/v1 SubjectAccessReview` objects using the backend
service account. Each review uses:

- `spec.user` from TokenReview user info
- `spec.uid` from TokenReview user info
- `spec.groups` from TokenReview user info
- `spec.extra` from TokenReview user info
- `spec.resourceAttributes` for the requested Kubernetes resource

The authorizer returns:

- allowed when `status.allowed` is true
- denied when `status.allowed` is false
- error when the SubjectAccessReview API call fails

Handlers map denied access to HTTP 403 or a WebSocket forbidden response.
Errors evaluating authorization map to HTTP 503 or a WebSocket error response,
because the backend cannot safely decide access.

### Request Handling

For HTTP requests, authorization runs before the backend fetches the target
object. This prevents using response differences to probe object existence.

The response rules are:

- malformed request, such as a namespaced resource without namespace: 400
- authenticated but denied: 403
- authorization API unavailable: 503
- authorized but object not found: 404

### WebSocket Handling

WebSocket requests are authorized before the upgrade sends initial data. If the
user is not authorized for the primary resource, including the required `watch`
permission for streamed data, the request fails with HTTP 403 before the
connection is established.

Namespaced WebSocket endpoints must validate namespace path parameters and query
parameters before authorization. Missing or empty namespaces for namespace-scoped
endpoints are malformed requests and fail with HTTP 400 before the WebSocket
upgrade.

For endpoints that can stream partial aggregate data, each snapshot is filtered
before it is written to the WebSocket. The same filtering is applied on the
initial snapshot and on every informer-triggered update.

KueueViz will not re-authenticate the token for every WebSocket message in this
KEP. Token lifetime and refresh remain part of the authentication KEP's
limitations.

### Partial Results for Aggregated Views

Aggregate views should avoid failing the entire page when some secondary data
is not authorized. Instead, they should omit unauthorized secondary resources.

Examples:

- Dashboard data may include Workloads for a namespace but omit Pod details if
  the user lacks `list pods` or `watch pods` in that namespace.
- ClusterQueue detail may include the ClusterQueue but omit LocalQueues in
  namespaces where the user lacks `list localqueues` or `watch localqueues`.
- ResourceFlavor detail may include flavor details but omit matching Nodes if
  the user lacks `list nodes` or `watch nodes`.

The backend should not include object names, counts, or status information for
unauthorized resources.

### Caching

SubjectAccessReview results may be cached to reduce API server load. The cache
key must include:

- authenticated user identity: username, uid, groups, and extras
- verb
- group
- version
- resource
- subresource
- namespace
- name

The initial alpha defaults should be conservative:

- successful authorization cache TTL: 30 seconds
- denied authorization cache TTL: 5 seconds

The cache must be bounded so a malicious client cannot create unbounded memory
growth by varying resource names. Evicting the oldest entries is acceptable.

### RBAC Updates

When KueueViz authentication is enabled, the backend service account needs
permission to create SubjectAccessReviews:

```yaml
- apiGroups: ["authorization.k8s.io"]
  resources: ["subjectaccessreviews"]
  verbs: ["create"]
```

This is added only to the KueueViz backend ClusterRole. It does not grant end
users any new permission to read Kubernetes objects.

KueueViz can be installed through the generated `kueueviz.yaml` manifest or
through the Helm chart. The implementation must update both
`config/components/kueueviz/clusterrole.yaml` for the generated manifest and
`charts/kueue/templates/kueueviz/clusterrole.yaml` for Helm. The release
artifact generated by `make artifacts` must contain the same
`subjectaccessreviews/create` permission as the Helm-rendered ClusterRole.

### Helm Configuration

The first implementation will enable authorization whenever
`kueueViz.backend.auth.mode` is `TokenReview`.

No separate Helm value is proposed for disabling authorization in authenticated
mode. Authentication without authorization is the vulnerable behavior this KEP
is fixing, so adding a bypass would preserve the unsafe configuration.

Future KEP updates may add cache tuning values if alpha users report that the
defaults are too aggressive or too expensive.

### Frontend Considerations

The frontend should treat HTTP 403 and WebSocket forbidden errors differently
from 401:

- 401 means the token is missing, invalid, or expired; redirect to login.
- 403 means the user is authenticated but not authorized; show a permission
  error without clearing the token.

The frontend should not attempt client-side filtering as a security boundary.
All filtering must happen in the backend.

For WebSocket authorization failures before upgrade, browsers may expose only a
generic connection failure to JavaScript. The frontend should not clear the
token for these failures. HTTP 403 responses must remain distinct from 401 and
must not trigger logout.

### Test Plan

[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.

#### Prerequisite testing updates

Add route-level backend tests that exercise all KueueViz protected endpoints
with an authenticated user context. These tests should run without a live
cluster by using fake Kubernetes clients and fake authorizer implementations.

#### Unit tests

Unit tests should cover:

- TokenReview cache stores and restores full user info.
- SubjectAccessReview requests include username, uid, groups, extras, and
  resource attributes.
- SubjectAccessReview requests leave `resourceAttributes.name` empty for
  `list` and `watch` checks.
- HTTP API handlers return 403 before object lookup when authorization is
  denied.
- Namespaced REST resource requests without namespace return 400.
- Dashboard WebSocket requests with missing or empty namespace return 400 before
  upgrade.
- The dashboard frontend does not open the dashboard WebSocket with an empty
  `namespace` query parameter.
- WebSocket handlers return 403 before upgrade when the user has `list` or
  `get` permission but lacks the required `watch` permission.
- Workload events WebSocket requests require both `get workload` for the named
  Workload and `list/watch events` in the namespace.
- Aggregated dashboard data omits Pod details when `list pods` or `watch pods`
  is denied.
- ClusterQueue detail omits LocalQueues when `list localqueues` or
  `watch localqueues` is denied in a namespace.
- ResourceFlavor detail omits Nodes when `list nodes` or `watch nodes` is
  denied.
- Cohort endpoints register Cohort informer coverage when they require
  `watch cohorts`.
- Namespaces WebSocket responses include only namespaces where the user is
  authorized to `list` and `watch` LocalQueues.
- Authorization cache differentiates users, groups, namespaces, resource names,
  and verbs.

Packages expected to be touched:

- `cmd/kueueviz/backend/middleware`
- `cmd/kueueviz/backend/handlers`
- `cmd/kueueviz/backend/config`
- `cmd/kueueviz/frontend/src`
- `config/components/kueueviz`
- `charts/kueue`
- `cmd/kueueviz/INSTALL.md`
- `site/content/en/docs/tasks/manage/enable_kueueviz.md`
- `test/e2e/kueueviz`

Current unit coverage for Go packages expected to be touched, measured on
2026-06-17 with `go test ./... -cover` from `cmd/kueueviz/backend`:

- `kueueviz/middleware`: 54.4%
- `kueueviz/handlers`: 24.5%
- `kueueviz/config`: no Go test files
- non-Go Helm, manifest, frontend, docs, and e2e paths: not applicable to Go
  unit coverage

#### Integration tests

Integration tests should run the backend against a real or envtest Kubernetes
API server and verify SubjectAccessReview behavior with RBAC:

1. Create namespace `team-a` and namespace `team-b`.
2. Create a user or service account token that can read Pods and Workloads only
   in `team-a`.
3. Verify KueueViz allows requests for `team-a`.
4. Verify KueueViz returns 403 or filtered data for `team-b`.

#### e2e tests

KueueViz e2e tests should include a TokenReview-enabled deployment and verify:

- unauthenticated request returns 401
- authenticated request for an allowed namespace succeeds
- authenticated request for a denied namespace returns 403 or omits data,
  depending on endpoint policy
- dashboard does not show Pods from namespaces denied by RBAC

### Graduation Criteria

Alpha (v0.19):

- [ ] SubjectAccessReview-based authorizer implemented.
- [ ] All protected REST and WebSocket endpoints mapped to resource checks.
- [ ] Backend unit tests cover deny, allow, and partial-result behavior.
- [ ] Helm chart and generated KueueViz manifest grant
  `subjectaccessreviews/create` when auth is enabled.
- [ ] KueueViz docs explain 401 versus 403 behavior.

Beta (TBD):

- [ ] E2E tests run in CI for a multi-namespace RBAC scenario.
- [ ] Authorization cache defaults validated against API server load.
- [ ] User feedback confirms partial aggregate views are understandable.
- [ ] Metrics expose authorization allow, deny, error, and latency counts.

Stable (TBD):

- [ ] KueueViz authentication and authorization are enabled by default, or the
  project explicitly documents why KueueViz remains opt-in.
- [ ] Production usage confirms RBAC filtering semantics are stable.
- [ ] No known authorization bypasses in protected KueueViz routes.

## Implementation History

- 2026-05-12: Initial KEP draft based on issue #11143.

## Drawbacks

This KEP adds backend complexity and extra API server calls. Aggregated views
become more complex because each piece of data can have different
authorization requirements. Some users may see partial dashboards rather than a
single complete cluster view.

These costs are necessary for safe multi-tenant use. Without authorization,
KueueViz cannot safely be exposed to authenticated users with different
namespace permissions.

## Alternatives

### Only authorize Pod YAML requests

Issue #11143 specifically mentions Pod data, but limiting the fix to
`/api/pod` would leave other endpoints vulnerable. Dashboard data, workload
events, LocalQueues, ClusterQueue details, ResourceFlavor node matching, and
other aggregate views can also disclose information from unauthorized
namespaces or cluster-scoped resources.

### Use per-user Kubernetes clients

KueueViz could build Kubernetes clients from each user's bearer token and use
the API server to enforce RBAC directly. This has clean semantics for direct
reads, but it does not fit the current shared informer architecture. Creating
per-user informers would increase watch connections, memory usage, and API
server load with the number of active users.

### Use impersonation instead of SubjectAccessReview

The backend could impersonate the authenticated user when making Kubernetes API
requests. This would require granting the backend service account impersonation
permissions for users and groups, which is broader and riskier than permission
to create SubjectAccessReviews. SubjectAccessReview is the narrower API for
asking the API server whether a user may perform a specific action.

### Rely on frontend filtering

Frontend filtering cannot be a security boundary. Users can call backend APIs
directly and bypass the UI. Authorization must happen in the backend before
data is returned.

### Keep authorization optional

Adding a Helm value to disable authorization while keeping authentication would
preserve the vulnerable confused-deputy behavior. For alpha, KueueViz already
supports `auth.mode: Disabled` for deployments that intentionally run without
auth. Once `TokenReview` is enabled, authorization should be mandatory.
