# KEP-16029: Loopback metrics authentication opt-out

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Why existing authentication cannot simply be configured](#why-existing-authentication-cannot-simply-be-configured)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1: A sidecar gets 401 because the platform issues no bearer tokens](#story-1-a-sidecar-gets-401-because-the-platform-issues-no-bearer-tokens)
  - [Risks and Mitigations](#risks-and-mitigations)
    - [Any container in the manager pod can read metrics](#any-container-in-the-manager-pod-can-read-metrics)
    - [Host networking exposes metrics to other node-local processes](#host-networking-exposes-metrics-to-other-node-local-processes)
    - [Another process can forward the loopback endpoint](#another-process-can-forward-the-loopback-endpoint)
- [Design Details](#design-details)
  - [Configuration API and feature gate](#configuration-api-and-feature-gate)
  - [Startup validation and serving behavior](#startup-validation-and-serving-behavior)
  - [Certificates and deployment](#certificates-and-deployment)
  - [Compatibility and rollback](#compatibility-and-rollback)
  - [Test Plan](#test-plan)
    - [Prerequisite testing updates](#prerequisite-testing-updates)
    - [Unit tests](#unit-tests)
    - [Integration tests](#integration-tests)
    - [e2e tests](#e2e-tests)
  - [Graduation Criteria](#graduation-criteria)
    - [Alpha](#alpha)
    - [Beta](#beta)
    - [GA](#ga)
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
authentication is disabled. This proposal amends
[KEP-4377's requirement for secure metrics serving](../4377-metrics-tls/README.md)
by making authentication and authorization optional for loopback listeners,
while retaining its TLS requirement. HTTP and non-loopback unauthenticated
access are outside its scope.

## Motivation

Some Kubernetes platforms authenticate workload clients to the API server using
client certificates and do not provision an API-server-recognized bearer token
for every in-pod metrics scraper. The controller can reconcile resources using
its certificate while its sidecar's request to `/metrics` receives HTTP 401.
The controller's API client identity and the scraper's incoming request identity
are separate concerns.

Kueue accepts only bearer tokens that pass TokenReview and SubjectAccessReview,
with no client-certificate option for metrics requests.

### Why existing authentication cannot simply be configured

The sidecar has no token that TokenReview accepts. Client-certificate metrics
authentication is not supported today (see [Alternatives](#alternatives)).
This KEP is for administrators who trust every process in the pod's network namespace.

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
disables metrics authentication and authorization. Enabled metrics must
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

#### Story 1: A sidecar gets 401 because the platform issues no bearer tokens

An administrator trusts all containers in the manager pod, but the platform
provides no bearer token for the metrics sidecar. The administrator enables the
alpha gate and disables metrics authentication on loopback. Verified HTTPS
requires externally supplied serving certificates and a scraper configured to
trust their CA and verify a certificate-matching server name.

```yaml
apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
featureGates:
  MetricsAuthenticationOptOut: true
internalCertManagement:
  enable: false
metrics:
  bindAddress: "127.0.0.1:8443"
  authentication: false
```

This example shows the relevant configuration fields. Supply the required
external certificates as described in [Certificates and deployment](#certificates-and-deployment),
then start the manager with the configuration file:

```sh
/manager --config=/etc/kueue/config/controller_manager_config.yaml
```

The sidecar scrapes `https://127.0.0.1:8443/metrics` without a bearer token,
with certificate verification enabled.

### Risks and Mitigations

#### Any container in the manager pod can read metrics

Every container, including an ephemeral debug container, can reach loopback.
HTTPS without authentication does not identify the scraper. The opt-out is
suitable only when all processes sharing that namespace are trusted. Metrics
can reveal workload names, queue names, and resource usage; do not treat them
as public data.

#### Host networking exposes metrics to other node-local processes

A `hostNetwork: true` pod shares node loopback with other node-local and
host-networked processes. Address validation cannot establish pod isolation or
detect this from the bind address. Administrators must not opt out when that
wider namespace is untrusted.

#### Another process can forward the loopback endpoint

A compromised or deliberately configured sidecar, proxy, or `kubectl port-forward`
can relay a loopback endpoint. The restriction prevents direct off-namespace
binding, not forwarding or access by actors with pod execution/debug privileges.
NetworkPolicy does not provide isolation between containers sharing loopback.

## Design Details

### Configuration API and feature gate

Add `Authentication *bool` with JSON name `authentication` and `+optional` to
`ControllerMetrics` in the `config.kueue.x-k8s.io/v1beta2` Configuration API
accepted by the manager on `main`. Default an omitted or null field to `true`;
retain an explicit `false` through defaulting and serialization. Update generated
code and the configuration reference during implementation. The field controls
both authentication and authorization; it does not control TLS.

Register `MetricsAuthenticationOptOut` as an alpha, default-off feature gate in
`pkg/features`. Enabling the gate alone does not change serving behavior. Use
the existing `featureGates` configuration map or the existing feature-gate CLI
mechanism; their existing mutual-exclusion rules remain unchanged. Introduce no
dedicated metrics flags.

### Startup validation and serving behavior

After loading/defaulting configuration and applying feature gates, validate the
new field together with `metrics.bindAddress` in `pkg/config`, before certificate
initialization or starting any listener. For enabled metrics with authentication
disabled, require the gate to be enabled and the address to be loopback. Reject a
gate-disabled opt-out rather than silently ignoring the requested setting.

With authentication enabled, metrics requests retain the existing TokenReview
authentication and SubjectAccessReview authorization checks. With authentication
disabled, every path on the metrics listener is served without authentication
or authorization; today that is only `/metrics`. Metrics requests make no
TokenReview or SubjectAccessReview calls in this mode. Controller operations
still require their normal Kubernetes API access. HTTPS and the existing TLS
settings apply in both modes; certificate errors never cause fallback to HTTP.

For enabled unauthenticated metrics, the address must be a literal loopback IP
with a port. Accept IPv4 loopback, IPv6 loopback, and IPv4-mapped loopback
addresses, for example `127.0.0.1:8443`, `[::1]:8443`, and
`[::ffff:127.0.0.1]:8443`. Require a numeric port in the valid
range; port zero retains the usual operating-system-assigned port behavior.
Do not resolve hostnames. Reject empty hosts, wildcards, non-loopback IPs,
`localhost`, other DNS names, URLs, missing ports, and malformed addresses.
Kueue defaults an omitted or empty `metrics.bindAddress` to `:8443` before
validation. The opt-out check rejects this defaulted wildcard address before
any listener is started.

Errors should name `metrics.authentication`, `MetricsAuthenticationOptOut`, or
`metrics.bindAddress` as appropriate, give valid IPv4/IPv6 examples, and explain
that non-loopback serving requires authenticated HTTPS. These settings are
process-start configuration; there is no dynamic authentication or bind-address
switch.

When an unauthenticated metrics listener is enabled, emit a single startup
warning identifying its bind address, HTTPS transport, and access by all
processes sharing the network namespace. Do not log credentials.

### Certificates and deployment

Metrics keep their existing TLS settings and certificate-loading/rotation paths.
With the default `internalCertManagement.enable: true`, the metrics listener
uses a self-signed certificate generated on each start, without a stable trust
anchor for the scraper. Verified HTTPS in the user story therefore requires
`internalCertManagement.enable: false` and externally supplied certificates.

With `internalCertManagement.enable: false`, supplying the required webhook,
metrics, and visibility certificates is intentional. In particular, the manager
continues to load external metrics certificates from
`/etc/kueue/metrics/certs/tls.crt` and `/etc/kueue/metrics/certs/tls.key`; missing
or invalid files still prevent startup. Disabling metrics authentication does
not remove this requirement. Webhook and visibility certificate handling and
shared TLS policy parsing remain unchanged.

The `"0"` sentinel still creates no metrics server; changing the existing HTTPS
certificate-initialization behavior for that sentinel is outside this proposal.

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
configuration field and the `MetricsAuthenticationOptOut` gate entry. Older
binaries reject both the unknown field and the unknown gate at startup. No
stored workload API objects need migration. All replicas should use a consistent
configuration during rollout; scrapers must accommodate authenticated replicas
until the transition is complete.

### Test Plan

- [x] The owners of the involved components may require updates to existing tests
  before implementation can be accepted.

#### Prerequisite testing updates

Extend the existing `pkg/config` table tests and the cert-manager e2e suite.
Make metrics-options construction a testable helper in `pkg/config`, called
after feature gates are applied and configuration is validated. The current
integration harness disables metrics by default, so use the existing
cert-manager e2e infrastructure for live scrape checks.

#### Unit tests

Add table-driven tests in `pkg/config` for configuration validation and the
resulting metrics server options:

- Omitted/null/default-true and explicit true/false authentication values.
- Every gate/field combination in the behavior table and the disabled-metrics
  sentinel with each combination.
- IPv4/IPv6/mapped loopback, wildcard and non-loopback addresses, hostnames,
  missing/out-of-range ports, port zero, and malformed addresses.
- Omitted/empty bind addresses defaulting to `:8443` and being rejected when
  authentication is disabled.
- The default and explicit-true authentication filter, a gate-enabled opt-out
  with no authentication filter, and HTTPS in every enabled mode.
- Actionable errors for unsafe addresses and a gate-disabled opt-out.

#### Integration tests

No new integration-framework tests are planned. Configuration unit tests cover
the gate, validation, and options; the cert-manager e2e test covers live serving
behavior.

#### e2e tests

Add one test to the cert-manager e2e suite with the alpha gate enabled,
`metrics.authentication: false`, an explicit loopback address, and
`internalCertManagement.enable: false`. Supply external serving certificates and
add a sidecar configured with the trusted CA and a matching TLS server name.
Assert that the sidecar can scrape metrics over verified HTTPS without a bearer
token and that a direct connection to the metrics port on the manager pod IP is
refused. Keep existing authenticated HTTPS and certificate coverage unchanged.

### Graduation Criteria

#### Alpha

- `metrics.authentication` defaults to `true`; the `MetricsAuthenticationOptOut`
  feature gate is disabled by default.
- Unit tests for validation and the metrics server options, and an e2e test in
  the cert-manager suite where a sidecar scrapes over verified HTTPS without a
  token and the pod IP is refused.
- A task page under Observability covering the setup, the external certificate
  requirement, and the trust boundary: other containers in the pod,
  `hostNetwork`, and `kubectl port-forward`.

#### Beta

- Feature gate enabled by default. `metrics.authentication` still defaults to
  `true`.
- Positive feedback from users, and no open security issues against the opt-out.
- Behavior with a service mesh sidecar that forwards inbound traffic to
  localhost is verified and documented.
- Re-evaluate client-certificate authentication (Alternative 3).

#### GA

- All reported bugs are addressed.
- Feature gate locked to true.

## Implementation History

- 2026-10-01: Initial KEP draft.

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
