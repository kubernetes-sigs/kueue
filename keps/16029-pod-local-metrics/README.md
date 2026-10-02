# KEP-16029: Metrics access for pod-local scrapers

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Why existing authentication cannot simply be configured](#why-existing-authentication-cannot-simply-be-configured)
  - [Separate motivation for HTTP](#separate-motivation-for-http)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [Options and startup validation](#options-and-startup-validation)
  - [Certificates and deployment](#certificates-and-deployment)
  - [Compatibility and rollback](#compatibility-and-rollback)
  - [Test Plan](#test-plan)
    - [Prerequisite testing updates](#prerequisite-testing-updates)
    - [Unit tests](#unit-tests)
    - [Integration tests](#integration-tests)
    - [e2e tests](#e2e-tests)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
<!-- /toc -->

## Summary

Allow an administrator to explicitly disable Kubernetes API-mediated metrics
authentication and authorization for a trusted scraper sharing the manager's
network namespace. Authenticated HTTPS remains the default. An unauthenticated
endpoint must bind to an explicit loopback IP and port, validated before startup.

This proposal also presents an independent, default-off HTTP option for the same
loopback-only use case. Whether to include HTTP is an open design decision:
authentication opt-out works with HTTPS and can be accepted on its own. HTTP
would deliberately introduce a narrow exception to
[KEP-4377's requirement for secure metrics serving](../4377-metrics-tls/README.md).

The proposal is provisional. [Issue #16029] tracks the authentication requirement;
[issue #4841] records the earlier HTTP and alternative authentication discussion.
[PR #16035] is an implementation prototype, not evidence of design acceptance.

## Motivation

Some Kubernetes platforms authenticate workload clients to the API server using
client certificates and do not provision an API-server-recognized bearer token
for every in-pod metrics scraper. The controller can reconcile resources using
its certificate while its sidecar's request to `/metrics` receives HTTP 401.
The controller's API client identity and the scraper's incoming request identity
are separate concerns.

In [v0.19.5's manager setup], Kueue unconditionally configures
`SecureServing: true` and
`FilterProvider: filters.WithAuthenticationAndAuthorization`. The filter disables
anonymous access and uses TokenReview and SubjectAccessReview. It does not
configure client-certificate authentication for incoming metrics requests.

### Why existing authentication cannot simply be configured

The motivating deployment has no supported bearer-token provisioning path for
the sidecar. Any bearer token recognized by the cluster's TokenReview API could
work; the limitation is not that Kubernetes only accepts ServiceAccount tokens.
Providing controller client certificates, configuring a serving certificate, or
granting review-API RBAC does not give the scraper an accepted bearer token.
Kueue currently exposes no option to use those certificates to authenticate the
metrics client instead of the token filter.

Provisioning and rotating a suitable token would resolve this requirement where
the platform supports it. Introducing a new token lifecycle or an authentication
proxy solely for a pod-local scrape adds dependencies that these deployments do
not otherwise need. This proposal permits administrators to deliberately trust
the shared network namespace instead, with narrower exposure than an anonymous
endpoint on a pod IP or wildcard address.

### Separate motivation for HTTP

With `internalCertManagement.enable: false`, the manager loads metrics serving
certificates from `/etc/kueue/metrics/certs/tls.crt` and `tls.key`; missing or
invalid files prevent startup. A platform using external webhook certificates
therefore also has to supply metrics serving certificates, even for a scraper
sharing pod loopback.

A centrally issued certificate may cover only a service DNS identity, without
`localhost` or loopback IP SANs. That does **not** make verified HTTPS impossible:
a scraper can dial `127.0.0.1` while verifying a DNS name present in the serving
certificate, using a trusted CA and its TLS server-name setting. Scraper support
for that setting and deployment wiring must be checked first. Disabling TLS
verification is not a proposed workaround.

The independent HTTP request is to avoid metrics certificate provisioning,
mounting, rotation, and client TLS configuration for trusted processes sharing a
network namespace. It is not needed to solve the missing bearer-token problem,
and no performance or startup-time improvement is claimed without measurements.

### Goals

- Support metrics collection by a trusted pod-local scraper without a bearer
  token or metrics TokenReview/SubjectAccessReview calls.
- Retain authenticated HTTPS by default and reject unsafe opt-out addresses.
- Preserve verified HTTPS, serving-certificate rotation, and webhook behavior.
- Decide explicitly whether eliminating the metrics certificate dependency
  justifies optional loopback HTTP.

### Non-Goals

- Anonymous or plaintext metrics on wildcard, pod, Service, or external IPs.
- Container-level isolation within a pod, or a guarantee that host loopback is a
  pod-only boundary.
- Changes to controller-to-API-server authentication, ServiceAccount lifecycle,
  webhooks, job integrations, or metrics contents.
- Adding mTLS, custom authentication providers, or a proxy implementation.
- Automatically removing upstream RBAC or reconfiguring external scrapers.

## Proposal

Add a boolean manager flag, `--metrics-authentication`, defaulting to `true`.
Setting it to `false` omits the metrics authentication and authorization filter.
When metrics are enabled, the bind address must contain a literal loopback IP and
numeric port. Other addresses cause an actionable startup error.

Separately propose `--metrics-secure`, also defaulting to `true`. Setting it to
`false` requests HTTP and additionally requires authentication to be disabled.
If HTTP is excluded during review, only the authentication flag is introduced
and metrics continue to use HTTPS in every enabled mode.

| Secure | Authentication | Enabled metrics behavior |
| --- | --- | --- |
| `true` | `true` | Existing authenticated HTTPS; existing address behavior |
| `true` | `false` | Unauthenticated HTTPS on an explicit loopback IP and port |
| `false` | `false` | Proposed optional HTTP on an explicit loopback IP and port |
| `false` | `true` | Startup error; no bearer-token authentication over HTTP |

The existing `metrics.bindAddress: "0"` sentinel continues to disable metrics;
serving-mode and loopback validation does not reject a disabled endpoint.

### User Stories

1. An administrator whose platform provides client certificates but no scraper
   bearer token can collect metrics over verified HTTPS from a trusted sidecar.
2. Subject to acceptance of HTTP, an administrator can use external webhook
   certificates without also provisioning metrics certificates for a loopback
   scraper in the same trusted network namespace.

### Risks and Mitigations

- **Broader access within the network namespace:** every container, including
  an ephemeral debug container, can reach loopback. HTTPS without authentication
  does not identify the scraper. The opt-out is suitable only when all processes
  sharing that namespace are trusted. Metrics can reveal workload names, queue
  names, and resource usage; do not treat them as public data.
- **Host networking:** a `hostNetwork: true` pod shares node loopback with other
  node-local and host-networked processes. Address validation cannot establish
  pod isolation or detect this from the bind address. Document this limitation;
  administrators must not opt out when that wider namespace is untrusted.
- **Exposure through another process:** a compromised or deliberately configured
  sidecar, proxy, or port-forward can relay a loopback endpoint. The restriction
  prevents direct off-namespace binding, not forwarding or access by actors with
  pod execution/debug privileges. NetworkPolicy does not provide isolation
  between containers sharing loopback.
- **Unintended downgrade:** both flags default to `true`, unsafe addresses fail
  startup, and certificate errors never trigger fallback to HTTP. Emit a single
  startup warning when authentication is disabled, identifying the bind address,
  transport, and shared-network-namespace exposure without logging credentials.
- **Plaintext exposure:** optional HTTP provides neither encryption nor server
  identity verification. It has the same loopback restriction, cannot be combined
  with the authentication filter, and must be documented as an explicit security
  tradeoff. Clients must not attach bearer tokens to this unauthenticated HTTP
  endpoint.

Kueue maintainers should review the security boundary and the HTTP exception
before marking this KEP implementable. Reviewers and approvers are not yet
assigned; this document does not imply their endorsement.

## Design Details

### Options and startup validation

Register the flags in the manager command and build controller-runtime metrics
options after loading/defaulting configuration, before starting any listener.

- Authentication enabled: retain
  `filters.WithAuthenticationAndAuthorization`.
- Authentication disabled: leave `FilterProvider` unset. No TokenReview or
  SubjectAccessReview is needed for metrics requests; controller operations
  still require their normal Kubernetes API access.
- If HTTP is accepted, set `SecureServing` from `--metrics-secure`; otherwise
  keep it unconditionally `true`.

For enabled unauthenticated metrics, parse `metrics.bindAddress` using
`net/netip.ParseAddrPort` and require `Addr.IsLoopback()`. Accept IPv4 loopback,
IPv6 loopback, and IPv4-mapped loopback addresses, for example `127.0.0.1:8443`,
`[::1]:8443`, and `[::ffff:127.0.0.1]:8443`. Require a numeric port in the valid
range; port zero retains the usual operating-system-assigned port behavior.
Do not resolve hostnames. Reject empty hosts, wildcards, non-loopback IPs,
`localhost`, other DNS names, URLs, missing ports, and malformed addresses.
Validate before controller-runtime can default an empty host to a wildcard.

Errors should name the relevant flags and `metrics.bindAddress`, give valid
IPv4/IPv6 examples, and explain that non-loopback serving requires authenticated
HTTPS. Flags are process-start configuration; there is no dynamic mode switch.

### Certificates and deployment

HTTPS keeps its existing TLS settings and certificate-loading/rotation paths,
including external metrics certificates when internal certificate management is
disabled. Webhook certificate handling and shared TLS policy parsing remain
unchanged. The `"0"` sentinel still creates no metrics server; changing the
existing HTTPS certificate-initialization behavior for that sentinel is outside
this proposal.

If HTTP is accepted, skip metrics TLS options and the metrics certificate watcher
entirely in HTTP mode. This removes only the metrics certificate dependency;
webhook TLS requirements still apply when internal certificate management is
disabled. Do not reuse webhook certificates implicitly or add a TLS-verification
bypass.

Example for unauthenticated HTTPS:

```sh
/manager --config=/etc/kueue/config/controller_manager_config.yaml \
  --metrics-authentication=false
```

```yaml
metrics:
  bindAddress: "127.0.0.1:8443"
```

If HTTP is accepted, add `--metrics-secure=false` and scrape
`http://127.0.0.1:8443/metrics`. The port does not select the protocol.

For HTTPS, configure the scraper's CA trust and a certificate-matching TLS server
name. A certificate issued only for a service-registry DNS name does not
automatically validate against `127.0.0.1`. The Helm
`metrics.serviceMonitor.tlsConfig` value controls Prometheus's TLS client; it
does not change the manager's transport or authentication filter and does not
configure another sidecar's client.

Keep upstream Helm, ServiceMonitor, and RBAC defaults unchanged. Initially,
customized deployments supply the flags through manager arguments. A standard
external ServiceMonitor cannot reach a loopback listener through its Service or
pod IP. Scraper scheme/TLS wiring and removing downstream metrics-auth RBAC are
separate administrator actions; retain review permissions wherever authenticated
metrics or another component still needs them.

### Compatibility and rollback

Existing installations receive no behavior change unless they opt out. Returning
to authenticated HTTPS requires configuring the scraper with an accepted token,
CA/server name, and the usual metrics-read authorization, retaining/restoring the
controller's review-API permissions, and supplying serving certificates before
restarting with both flags enabled. Remove new flags before rolling back to a
binary that does not recognize them. No stored API objects need migration.

### Test Plan

- [x] The owners of the involved components may require updates to existing tests
  before implementation can be accepted.

#### Prerequisite testing updates

Establish tests for the existing authenticated HTTPS path before adding opt-outs.
Use recording review-API handlers and real HTTP/TLS listeners; do not infer
authentication behavior solely from non-nil filter options. No package coverage
percentage is asserted by this proposal.

#### Unit tests

In `cmd/kueue`, cover flag defaults, explicit values, all serving-mode
combinations, disabled metrics, IPv4/IPv6/mapped loopback, wildcard and
non-loopback addresses, hostnames,
and malformed addresses. Verify startup errors are actionable and occur before
listeners or certificate initialization. Check the opt-out startup warning.

#### Integration tests

Run real HTTPS scrapes with no token, invalid tokens, allowed and denied tokens.
Verify the default filter still authenticates and authorizes requests, and
unauthenticated scrapes produce zero TokenReview/SubjectAccessReview calls.
Verify CA and hostname failures remain failures, initial certificate failures
are surfaced, and updated certificates are served without restart.

If HTTP is accepted, test real IPv4/IPv6 HTTP scrapes with no metrics certificate
files or review calls, including external certificate-management configuration.
Ensure HTTP does not initialize a metrics certificate watcher and cannot be
configured with authentication enabled.

#### e2e tests

Before release, start a manager with a sidecar scraper and external webhook
certificates. Verify readiness, metrics collection without a bearer token, normal
workload admission, and webhook TLS. Verify direct access through the pod IP fails
for loopback binding and that a second container in the pod can scrape, making
the actual trust boundary explicit. Retain authenticated HTTPS/cert-manager
coverage. Include an HTTP variant only if that option is accepted.

### Graduation Criteria

Propose stable command-line options without an additional feature gate: the
explicit flags already control opt-in and introduce no persisted API schema.
This maturity choice and the command-line versus configuration-API shape require
maintainer agreement. No release milestone or backport is committed here.

Before implementation is accepted, resolve HTTP scope, obtain security review,
complete the agreed tests and documentation, and verify unchanged defaults.
Implementation merges to `main` first; release-branch eligibility is a separate
maintainer decision.

## Implementation History

- September 2026: [issue #16029] and [PR #16035] describe the authentication
  requirement and a prototype, subsequently extended with optional HTTP.
- October 2026: [maintainer feedback] requests a KEP covering the deployment
  problem, existing-authentication alternatives, and opt-out design, and suggests
  retaining HTTPS. This provisional proposal records that unresolved scope.

## Drawbacks

This deliberately weakens metrics access controls for opted-in deployments and
adds configurations that must remain tested and documented. Loopback does not
protect against untrusted colocated processes, host networking, or forwarding.
HTTP further removes transport protection and revisits an earlier explicit
non-goal. The benefit must justify those support and security costs.

## Alternatives

1. **Provision a bearer token and retain authenticated HTTPS.** Preferred where
   supported. It does not fit platforms without an acceptable token lifecycle
   for the scraper; controller client certificates cannot substitute for it.
2. **Authentication opt-out with HTTPS only.** Meets the original requirement
   and avoids changing KEP-4377's transport policy. It retains metrics certificate
   provisioning. This is an independently viable outcome of this KEP and the
   maintainer's current recommendation.
3. **Configure CA trust and TLS server name, or issue a suitable certificate.**
   Resolves certificate trust/name mismatches without disabling verification.
   It does not remove bearer-token authentication or certificate provisioning.
4. **mTLS or pluggable metrics authentication.** Could use existing workload
   certificates while preserving caller identity. It needs a client-CA/trust
   model, rotation, identity mapping, and an authorization policy. It is a larger
   design and remains a possible alternative if maintainers reject anonymous
   loopback access.
5. **Authentication proxy or local exporter.** Can centralize policy, but a
   proxy that simply forwards requests still needs an upstream credential or an
   accepted alternative authentication mode. It adds another component to run.
6. **Configuration API fields instead of flags.** Could fit existing Helm
   configuration more naturally, but requires API defaulting/versioning and
   documentation. Decide this during review rather than exposing both surfaces.
7. **Unrestricted anonymous serving or disabling metrics.** The former exposes
   metrics outside the intended namespace; the latter loses required
   observability. Neither meets this proposal's goals.

[Issue #16029]: https://github.com/kubernetes-sigs/kueue/issues/16029
[issue #4841]: https://github.com/kubernetes-sigs/kueue/issues/4841
[PR #16035]: https://github.com/kubernetes-sigs/kueue/pull/16035
[v0.19.5's manager setup]: https://github.com/kubernetes-sigs/kueue/blob/v0.19.5/cmd/kueue/main.go#L188
[maintainer feedback]: https://github.com/kubernetes-sigs/kueue/pull/16035#issuecomment-5944565191
