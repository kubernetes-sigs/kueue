# KEP-16029: Loopback metrics authentication opt-out

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Why existing authentication cannot simply be configured](#why-existing-authentication-cannot-simply-be-configured)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [Configuration API and feature gate](#configuration-api-and-feature-gate)
  - [Startup validation and server options](#startup-validation-and-server-options)
  - [Certificates and deployment](#certificates-and-deployment)
  - [Compatibility and rollback](#compatibility-and-rollback)
  - [Test Plan](#test-plan)
    - [Prerequisite testing updates](#prerequisite-testing-updates)
    - [Unit tests](#unit-tests)
    - [Integration tests](#integration-tests)
    - [e2e tests](#e2e-tests)
  - [Graduation Criteria](#graduation-criteria)
    - [Alpha](#alpha)
    - [Beta and stable](#beta-and-stable)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
<!-- /toc -->

## Summary

Introduce an alpha Configuration API field, `metrics.authentication`, that lets
an administrator explicitly disable Kubernetes API-mediated metrics
authentication and authorization for a trusted scraper sharing the manager's
network namespace. The field defaults to `true`. Opting out requires the
default-off `MetricsAuthenticationOptOut` feature gate and an explicit loopback
IP and port, validated before startup.

Authenticated HTTPS remains the default, and HTTPS remains required when
authentication is disabled. This proposal preserves
[KEP-4377's requirement for secure metrics serving](../4377-metrics-tls/README.md).
HTTP and non-loopback unauthenticated access are outside its scope.

The proposal is provisional. [Issue #16029] tracks the authentication requirement;
[issue #4841] records the earlier HTTP and alternative authentication discussion.
[PR #16035] is an earlier flag-based prototype; it must be aligned with this
narrower configuration-based design before implementation can be accepted.

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
the platform supports it. [Maintainer feedback on client certificates] also
reports a prototype using client-certificate authentication with RBAC checks.
That is a viable alternative to investigate for platforms with workload
certificates; this proposal does not claim that authenticated scraping is
impossible in those environments.

The opt-out addresses deployments that deliberately trust all processes in the
shared network namespace and do not need a separate metrics caller identity.
It avoids adding a scraper credential lifecycle or authentication proxy solely
for pod-local metrics, with narrower exposure than an anonymous endpoint on a
pod IP or wildcard address. Serving certificates and client TLS verification
remain required independently of scraper authentication.

### Goals

- Support metrics collection by a trusted pod-local scraper without a bearer
  token or metrics TokenReview/SubjectAccessReview calls.
- Retain authenticated HTTPS by default and reject unsafe opt-out addresses.
- Preserve verified HTTPS, serving-certificate rotation, and webhook behavior.
- Introduce the opt-out through the Configuration API behind a default-off alpha
  feature gate, with validation alongside `metrics.bindAddress`.

### Non-Goals

- HTTP metrics on any address, or removing metrics certificate requirements.
- Unauthenticated metrics on wildcard, pod, Service, or external IPs.
- New metrics authentication or transport command-line flags.
- Container-level isolation within a pod, or a guarantee that host loopback is a
  pod-only boundary.
- Changes to controller-to-API-server authentication, ServiceAccount lifecycle,
  webhooks, job integrations, or metrics contents.
- Adding mTLS, custom authentication providers, or a proxy implementation.
- Automatically removing upstream RBAC or reconfiguring external scrapers.

## Proposal

Add `metrics.authentication`, an optional boolean defaulting to `true`, alongside
`metrics.bindAddress` in the manager's Configuration API. With the alpha
`MetricsAuthenticationOptOut` feature gate enabled, setting the field to `false`
omits the metrics authentication and authorization filter. Enabled metrics must
then bind to a literal loopback IP and numeric port. Other addresses cause an
actionable startup error. HTTPS is used in every enabled mode.

| Feature gate | `metrics.authentication` | Enabled metrics behavior |
| --- | --- | --- |
| Disabled (default) | Omitted or `true` | Existing authenticated HTTPS; existing address behavior |
| Disabled | `false` | Startup error; opt-out requires the alpha gate |
| Enabled | Omitted or `true` | Existing authenticated HTTPS; existing address behavior |
| Enabled | `false` | Unauthenticated HTTPS on an explicit loopback IP and port |

The existing `metrics.bindAddress: "0"` sentinel continues to disable metrics.
For this sentinel, the authentication field has no effect and the new gate and
loopback checks are skipped; no metrics listener is created. Re-enabling metrics
requires satisfying both checks. Existing feature-gate loading rules still
apply, including rejection of unknown feature gates.

### User Stories

An administrator who trusts all containers in the manager pod can enable the
alpha gate and collect metrics over verified HTTPS from a sidecar without
provisioning a bearer token for that scraper.

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
- **Unintended downgrade:** authentication defaults to `true` and the alpha gate
  defaults to `false`. Unsafe addresses or a gate-disabled opt-out fail startup
  when metrics are enabled. Certificate errors never trigger fallback to HTTP.
  Emit a single startup warning when authentication is disabled, identifying the
  bind address,
  transport, and shared-network-namespace exposure without logging credentials.

Kueue maintainers should review the security boundary and configuration behavior
before marking this KEP implementable. Listing reviewers and approvers in the
metadata does not imply their endorsement.

## Design Details

### Configuration API and feature gate

Add `Authentication *bool` with JSON name `authentication` and `+optional` to
`ControllerMetrics` in the `config.kueue.x-k8s.io/v1beta2` Configuration API
accepted by the manager on `main`. Default an omitted or null field to `true`;
retain an explicit `false` through defaulting and serialization. Update generated
code and the configuration reference during
implementation. The field controls both authentication and authorization; it
does not control TLS.

Register `MetricsAuthenticationOptOut` as an alpha, default-off feature gate in
`pkg/features`. Enabling the gate alone does not change serving behavior. Use
the existing `featureGates` configuration map or the existing feature-gate CLI
mechanism; their existing mutual-exclusion rules remain unchanged. Introduce no
dedicated metrics flags.

### Startup validation and server options

After loading/defaulting configuration and applying feature gates, validate the
new field together with `metrics.bindAddress` in `pkg/config`, before certificate
initialization or starting any listener. For enabled metrics with authentication
disabled, require the gate to be enabled and the address to be loopback. Reject a
gate-disabled opt-out rather than silently ignoring the requested setting.
Build controller-runtime metrics options only after this validation succeeds.

- Authentication enabled: retain
  `filters.WithAuthenticationAndAuthorization`.
- Authentication disabled: leave `FilterProvider` unset. No TokenReview or
  SubjectAccessReview is needed for metrics requests; controller operations
  still require their normal Kubernetes API access.
- Always retain `SecureServing: true` and the existing TLS options.

For enabled unauthenticated metrics, parse `metrics.bindAddress` using
`net/netip.ParseAddrPort` and require `Addr.IsLoopback()`. Accept IPv4 loopback,
IPv6 loopback, and IPv4-mapped loopback addresses, for example `127.0.0.1:8443`,
`[::1]:8443`, and `[::ffff:127.0.0.1]:8443`. Require a numeric port in the valid
range; port zero retains the usual operating-system-assigned port behavior.
Do not resolve hostnames. Reject empty hosts, wildcards, non-loopback IPs,
`localhost`, other DNS names, URLs, missing ports, and malformed addresses.
Validate before controller-runtime can default an empty host to a wildcard.

Errors should name `metrics.authentication`, `MetricsAuthenticationOptOut`, or
`metrics.bindAddress` as appropriate, give valid IPv4/IPv6 examples, and explain
that non-loopback serving requires authenticated
HTTPS. These settings are process-start configuration; there is no dynamic
authentication or bind-address switch.

### Certificates and deployment

Metrics keep their existing TLS settings and certificate-loading/rotation paths.
With `internalCertManagement.enable: false`, supplying the required webhook,
metrics, and visibility certificates is intentional. In particular, the manager
continues to load external metrics certificates from
`/etc/kueue/metrics/certs/tls.crt` and `/etc/kueue/metrics/certs/tls.key`; missing
or invalid files still prevent startup. Disabling metrics authentication does
not remove this requirement. Webhook and visibility certificate handling and
shared TLS policy parsing remain unchanged.

The Helm chart's missing external metrics certificate mount and its example
values were identified in [maintainer scope feedback]. Correcting those chart
defects is separate work, not a reason to remove TLS in this KEP.

The `"0"` sentinel still creates no metrics server; changing the existing HTTPS
certificate-initialization behavior for that sentinel is outside this proposal.

Example for unauthenticated HTTPS:

```sh
/manager --config=/etc/kueue/config/controller_manager_config.yaml
```

```yaml
apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
featureGates:
  MetricsAuthenticationOptOut: true
metrics:
  bindAddress: "127.0.0.1:8443"
  authentication: false
```

This example shows the relevant configuration fields. Scrape
`https://127.0.0.1:8443/metrics` with a client configured to verify the serving
certificate, without a bearer token.

For HTTPS, configure the scraper's CA trust and a certificate-matching TLS server
name. A certificate issued only for a service-registry DNS name does not
automatically validate against `127.0.0.1`. The Helm
`metrics.serviceMonitor.tlsConfig` value controls Prometheus's TLS client; it
does not change the manager's transport or authentication filter and does not
configure another sidecar's client.
Do not add a TLS-verification bypass or implicitly reuse webhook certificates.

Keep upstream Helm, ServiceMonitor, and RBAC defaults unchanged. Customized
deployments supply the opt-out through the manager configuration file. A standard
external ServiceMonitor cannot reach a loopback listener through its Service or
pod IP. Scraper scheme/TLS wiring and removing downstream metrics-auth RBAC are
separate administrator actions; retain review permissions wherever authenticated
metrics or another component still needs them.

### Compatibility and rollback

Existing installations receive no behavior change unless they enable the alpha
gate and explicitly set `metrics.authentication: false`. Turning on the gate
alone retains authenticated HTTPS.

To return to authenticated HTTPS, configure the scraper with an accepted token,
CA/server name, and the usual metrics-read authorization, retain or restore the
controller's review-API permissions, and set `metrics.authentication: true` or
remove the field before restarting. Serving certificates remain required
throughout. The feature gate can then be disabled, or disabled in the same
configuration update. Disabling only the gate while leaving an enabled endpoint
configured with `authentication: false` fails startup.

Before rolling back to a binary that predates the feature, remove both the new
configuration field and the `MetricsAuthenticationOptOut` gate entry. Validate
the configuration with the target binary; older versions may reject unknown
fields or gates. No stored workload API objects need migration. All replicas
should use a consistent configuration during rollout; scrapers must accommodate
authenticated replicas until the transition is complete.

### Test Plan

- [x] The owners of the involved components may require updates to existing tests
  before implementation can be accepted.

#### Prerequisite testing updates

Establish tests for the existing authenticated HTTPS path before adding the
opt-out.
Use recording review-API handlers and real HTTP/TLS listeners; do not infer
authentication behavior solely from non-nil filter options. No package coverage
percentage is asserted by this proposal.

#### Unit tests

- In the `v1beta2` Configuration API package, cover omitted/null/default-true and
  explicit true/false values, deep copies, and serialization round trips.
  Explicit `false` must not become the default `true`.
- In `pkg/config`, cover every gate/field combination in the behavior table and
  the disabled-metrics sentinel with each combination. Cover IPv4/IPv6/mapped
  loopback, wildcard and non-loopback addresses, hostnames, missing/out-of-range
  ports, port zero, and malformed addresses. An omitted bind address must not
  become an unauthenticated wildcard listener through defaulting.
- In `cmd/kueue`, verify the default and explicit-true filter, gate-enabled
  opt-out, unconditional secure serving, and opt-out startup warning. Verify
  validation errors are actionable and occur before listeners or certificate
  initialization. Disabling the feature gate must not leave an active
  unauthenticated path.

#### Integration tests

Run real HTTPS scrapes with no token, invalid tokens, allowed and denied tokens.
Verify the default filter still authenticates and authorizes requests, and
unauthenticated scrapes produce zero TokenReview/SubjectAccessReview calls.
Run the opt-out on IPv4 and IPv6 loopback with the alpha gate enabled, and verify
that enabling the gate alone leaves authentication enforced. Verify unsafe
addresses and a gate-disabled opt-out fail startup. Verify CA and hostname
failures remain failures, plaintext requests are not served metrics, initial
certificate failures are surfaced, and updated certificates are served without
restart. Cover `internalCertManagement.enable: false`, unchanged webhook
certificate handling, and the existing disabled-metrics behavior.

#### e2e tests

Before alpha release, start a manager with the gate enabled, a sidecar scraper,
and external serving certificates. Verify readiness, metrics collection over
verified HTTPS without a bearer token, normal workload admission, and webhook
TLS. Verify direct access through the pod IP fails for loopback binding and that
a second container in the pod can scrape, making
the actual trust boundary explicit. Retain authenticated HTTPS/cert-manager
coverage. Verify rollback to authenticated HTTPS with a token-bearing scraper.

### Graduation Criteria

#### Alpha

- Introduce `metrics.authentication` and the default-off
  `MetricsAuthenticationOptOut` gate with the behavior and validation above.
- Obtain maintainer security/API review; complete unit, integration, and e2e
  coverage for opt-in, gate-off rejection, unchanged defaults, and rollback.
- Document verified HTTPS setup, the shared network namespace boundary, external
  certificate requirements, and actionable startup errors.

#### Beta and stable

Further graduation requires adoption feedback from independent deployments,
stable automated coverage, and no unresolved security or compatibility issues.
Review the API and gate lifecycle before promotion. Authentication must remain
enabled by default even if the feature gate becomes enabled by default at a
later stage. HTTP and non-loopback access require separate proposals; neither
is a graduation criterion for this feature.

This KEP remains provisional, with no release milestone or backport promised.
Implementation merges to `main` first; release-branch eligibility is a separate
maintainer decision.

## Implementation History

- September 2026: [issue #16029] and [PR #16035] describe the authentication
  requirement and a prototype, subsequently extended with optional HTTP.
- October 1, 2026: [maintainer feedback] requests a KEP covering the deployment
  problem, existing-authentication alternatives, and opt-out design, and suggests
  retaining HTTPS.
- October 5, 2026: following [maintainer scope feedback], narrow the proposal to
  an alpha, feature-gated Configuration API opt-out for loopback HTTPS. Remove
  HTTP and dedicated metrics flags from the proposed scope.

## Drawbacks

This deliberately weakens metrics access controls for opted-in deployments and
adds configurations that must remain tested and documented. Loopback does not
protect against untrusted colocated processes, host networking, or forwarding.
It adds an alpha configuration field, feature-gate validation, and rollback
requirements while retaining the existing serving-certificate dependency.

## Alternatives

1. **Provision a bearer token and retain authenticated HTTPS.** Preferred where
   supported. It does not fit platforms without an acceptable token lifecycle
   for the scraper; controller client certificates cannot substitute for it.
2. **Configure CA trust and TLS server name, or issue a suitable certificate.**
   Resolves certificate trust/name mismatches without disabling verification.
   It does not remove bearer-token authentication or certificate provisioning.
3. **Client-certificate metrics authentication.** Maintainers have prototyped
   this with controller-runtime and RBAC checks. It may fit platforms that can
   configure the scraper's client certificate, server client-CA trust, rotation,
   identity mapping, and authorization. This remains a viable authenticated
   alternative; the proposed opt-out serves administrators who intentionally
   choose the shared network namespace as their access boundary.
4. **Authentication proxy or local exporter.** Can centralize policy, but a
   proxy that simply forwards requests still needs an upstream credential or an
   accepted alternative authentication mode. It adds another component to run.
5. **Dedicated command-line flags.** The prototype used flags, but opt-out
   validity depends on `metrics.bindAddress`. Keep the related settings and their
   validation in the Configuration API rather than expose two configuration
   surfaces.
6. **Optional HTTP.** Would eliminate metrics certificate provisioning and
   client TLS wiring, but is unnecessary to remove the bearer-token dependency
   and would change KEP-4377's transport policy. Excluded from this proposal.
7. **Unrestricted anonymous serving or disabling metrics.** The former exposes
   metrics outside the intended namespace; the latter loses required
   observability. Neither meets this proposal's goals.

[Issue #16029]: https://github.com/kubernetes-sigs/kueue/issues/16029
[issue #4841]: https://github.com/kubernetes-sigs/kueue/issues/4841
[PR #16035]: https://github.com/kubernetes-sigs/kueue/pull/16035
[v0.19.5's manager setup]: https://github.com/kubernetes-sigs/kueue/blob/v0.19.5/cmd/kueue/main.go#L188
[maintainer feedback]: https://github.com/kubernetes-sigs/kueue/pull/16035#issuecomment-5944565191
[maintainer feedback on client certificates]: https://github.com/kubernetes-sigs/kueue/pull/16535#issuecomment-5955636750
[maintainer scope feedback]: https://github.com/kubernetes-sigs/kueue/pull/16535#issuecomment-6006762757
