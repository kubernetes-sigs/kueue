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
    - [Metrics may become visible beyond their intended audience](#metrics-may-become-visible-beyond-their-intended-audience)
- [Design Details](#design-details)
  - [API](#api)
  - [Validation](#validation)
  - [AccessControl handling](#accesscontrol-handling)
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

Introduce a mechanism that lets an administrator disable metrics authentication
and authorization for a trusted scraper sharing the Kueue controller manager's
network namespace. Unauthenticated metrics are restricted to an explicit
loopback IP and port.

Authenticated HTTPS remains the default, and HTTPS remains required when
authentication is disabled. This proposal amends
[KEP-4377's requirement for secure metrics serving](../4377-metrics-tls/README.md)
by making authentication and authorization optional for loopback listeners,
while retaining its TLS requirement. HTTP and non-loopback unauthenticated
access are outside its scope.

## Motivation

Some Kubernetes platforms authenticate workload clients to the API server using
client certificates and do not provision an API-server-recognized bearer token
for every in-pod metrics scraper. The Kueue controller manager can reconcile
resources using its client certificate while its sidecar's request to `/metrics`
receives HTTP 401. The manager's API client identity and the scraper's incoming
request identity are separate concerns.

Currently, Kueue's metrics endpoint accepts only bearer tokens that pass
TokenReview and SubjectAccessReview, with no client-certificate option for
metrics requests.

### Why existing authentication cannot simply be configured

The sidecar has no token that TokenReview accepts. Client-certificate metrics
authentication is not supported today (see [Alternatives](#alternatives)).

### Goals

- Introduce a mechanism to opt out of metrics authentication and authorization,
  allowing a trusted pod-local scraper to collect metrics without a bearer token
  or metrics TokenReview/SubjectAccessReview calls.
- Retain authenticated HTTPS by default and reject unsafe opt-out addresses.
- Preserve verified HTTPS, serving-certificate rotation, and webhook behavior.

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

Allow administrators to disable metrics authentication and authorization and
control the metrics bind address for a trusted pod-local scraper. Opting out
requires an explicit loopback address; configurations that would expose
unauthenticated metrics beyond loopback are rejected before startup.
Authenticated HTTPS remains the default, and HTTPS is retained when opting out.

### User Stories

#### Story 1: A sidecar gets 401 because the platform issues no bearer tokens

An administrator trusts all containers in the manager pod, but the platform
provides no bearer token for the metrics sidecar. Scrapes are rejected with
HTTP 401 even though the manager can access the Kubernetes API. The administrator
wants the sidecar to collect metrics over verified HTTPS without provisioning a
scraper credential, while keeping the endpoint accessible only through loopback.
The deployment supplies external serving certificates so the scraper can verify
the server's identity.

### Risks and Mitigations

#### Metrics may become visible beyond their intended audience

Disabling authentication and authorization can expose information such as
pending workloads per namespace, queue names, and resource usage to additional
users. Some multi-tenant deployments intentionally make metrics visible across
tenants through shared managed Prometheus. Where the same metrics are already
available to the same audience, the opt-out may add no information exposure;
other deployments rely on restricting access to tenant metrics. The risk depends
on the deployment's intended audience and the information exposed.

Other containers in the manager pod can reach the listener; with
`hostNetwork: true`, other node-local processes can also reach it. A proxy or a
user with suitable execution or port-forward permissions can relay the endpoint.
These are ways the same disclosure can occur. Cluster access alone does not
grant access to loopback, and loopback does not isolate containers within a pod.

Authentication remains enabled by default. The mitigation is an explicit
administrator opt-out, enforced loopback binding, and a startup warning that
authentication is disabled and metrics may expose tenant information.
Administrators must assess who can reach or forward the listener and whether
sharing those metrics is acceptable. Deployments requiring a narrower audience
should retain authentication; the warning itself does not enforce isolation.

## Design Details

### API

Add `AccessControl` to `ControllerMetrics` in the
`config.kueue.x-k8s.io/v1beta2` Configuration API. Other existing fields are
omitted from this excerpt:

```go
// MetricsAccessControl specifies how access to the metrics listener is controlled.
// +kubebuilder:validation:Enum=Delegated;None
type MetricsAccessControl string

const (
	// MetricsAccessControlDelegated delegates authentication and authorization
	// to Kubernetes through controller-runtime.
	MetricsAccessControlDelegated MetricsAccessControl = "Delegated"

	// MetricsAccessControlNone disables authentication and authorization
	// for the metrics listener.
	MetricsAccessControlNone MetricsAccessControl = "None"
)

type ControllerMetrics struct {
	// AccessControl selects the authentication and authorization mode for
	// the metrics listener. Defaults to Delegated.
	// +optional
	AccessControl *MetricsAccessControl `json:"accessControl,omitempty"`
}
```

### Validation

Validate after configuration defaulting and feature-gate loading, before
initializing certificates or starting listeners:

- `metrics.accessControl` must be `Delegated` or `None`. An omitted or null field
  defaults to `Delegated`; explicit empty strings and unknown values are invalid.
- When metrics are enabled, `accessControl: None` requires the
  `MetricsAuthenticationOptOut` feature gate to be enabled.
- When metrics are enabled, `accessControl: None` requires a literal loopback IP
  and numeric port in `metrics.bindAddress`. Accept IPv4, IPv6, and IPv4-mapped
  loopback addresses, such as `127.0.0.1:8443`, `[::1]:8443`, and
  `[::ffff:127.0.0.1]:8443`. Ports must be in the range 0–65535; port zero permits
  an operating-system-assigned port. Reject wildcard and non-loopback addresses,
  hostnames (including `localhost`), URLs, missing ports, and malformed values.
- Kueue defaults an omitted or empty bind address to `:8443`; reject that wildcard
  address when `accessControl: None` is used for enabled metrics.
- `metrics.bindAddress: "0"` disables metrics. No listener is created, and the
  opt-out gate and loopback checks are skipped. The access-control value must
  still be valid.

Invalid configurations fail startup with an error naming the relevant field and
explaining how to correct it.

### AccessControl handling

Configure the controller-runtime metrics server according to `metrics.accessControl`:

- `Delegated`: use `FilterProvider: filters.WithAuthenticationAndAuthorization`.
- `None`: use `FilterProvider: nil`; metrics requests require no bearer token and
  make no TokenReview or SubjectAccessReview calls. This affects every path on
  the metrics listener, currently only `/metrics`.

Both modes retain `SecureServing: true` and existing certificate loading and
rotation. Defaults, webhook behavior, and upstream RBAC remain unchanged.
For an enabled listener using `None`, emit a startup warning with the bind
address stating that authentication and authorization are disabled and metrics
may expose tenant information to other processes sharing the network namespace.

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

- Omitted/null values defaulting to `Delegated`, explicit `Delegated` and `None`,
  and rejection of empty or unknown values.
- Both access-control modes with the feature gate enabled/disabled, including
  the disabled-metrics sentinel.
- IPv4/IPv6/mapped loopback, wildcard and non-loopback addresses, hostnames,
  missing/out-of-range ports, port zero, and malformed addresses.
- Omitted/empty bind addresses defaulting to `:8443` and being rejected when
  `accessControl: None` is used.
- The default and explicit `Delegated` authentication filter, gate-enabled `None`
  with no authentication filter, and HTTPS in every enabled mode.
- Actionable errors for unsafe addresses and a gate-disabled opt-out.

#### Integration tests

No new integration-framework tests are planned. Configuration unit tests cover
the gate, validation, and options; the cert-manager e2e test covers live serving
behavior.

#### e2e tests

Add one test to the cert-manager e2e suite with the alpha gate enabled,
`metrics.accessControl: None`, an explicit loopback address, and
`internalCertManagement.enable: false`. Supply external serving certificates and
add a sidecar configured with the trusted CA and a matching TLS server name.
Assert that the sidecar can scrape metrics over verified HTTPS without a bearer
token and that a direct connection to the metrics port on the manager pod IP is
refused. Keep existing authenticated HTTPS and certificate coverage unchanged.

### Graduation Criteria

#### Alpha

- `metrics.accessControl` with the `Delegated` default and the `None` opt-out.
- `MetricsAuthenticationOptOut` feature gate disabled by default.
- Unit tests for validation and the metrics server options, and an e2e test in
  the cert-manager suite where a sidecar scrapes over verified HTTPS without a
  token and the pod IP is refused.
- A task page under Observability covering the setup, the external certificate
  requirement, and the trust boundary: other containers in the pod,
  `hostNetwork`, and `kubectl port-forward`.

#### Beta

- Feature gate enabled by default.
- Positive feedback from users, and no open security issues against the opt-out.
- Behavior with a service mesh sidecar that forwards inbound traffic to
  localhost is verified and documented.
- Re-evaluate client-certificate authentication (Alternative 1).

#### GA

- All reported bugs are addressed.
- Feature gate locked to true.

## Implementation History

- 2026-10-01: Initial KEP draft.

## Drawbacks

This deliberately weakens metrics access controls for opted-in deployments and
adds configurations that must remain tested and documented. Loopback does not
protect against untrusted colocated processes, host networking, or forwarding.
It adds a configuration field and validation while retaining the existing
serving-certificate dependency.

## Alternatives

1. **Client-certificate metrics authentication.** Maintainers have prototyped
   this with controller-runtime and RBAC checks. It may fit platforms that can
   configure the scraper's client certificate, server client-CA trust, rotation,
   identity mapping, and authorization. This remains a viable authenticated
   alternative; the proposed opt-out serves administrators who intentionally
   choose the shared network namespace as their access boundary.
2. **Authentication proxy or local exporter.** Can centralize policy, but a
   proxy that simply forwards requests still needs an upstream credential or an
   accepted alternative authentication mode. It adds another component to run.
3. **Dedicated command-line flags.** The prototype used flags, but opt-out
   validity depends on `metrics.bindAddress`. Keep the related settings and their
   validation in the Configuration API rather than expose two configuration
   surfaces.
4. **Optional HTTP.** Would eliminate metrics certificate provisioning and
   client TLS wiring, but is unnecessary to remove the bearer-token dependency
   and would change KEP-4377's transport policy. Excluded from this proposal.
