# KEP-16029: Metrics access control

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Why existing authentication cannot simply be configured](#why-existing-authentication-cannot-simply-be-configured)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1: Scraping metrics from a sidecar container without a bearer token](#story-1-scraping-metrics-from-a-sidecar-container-without-a-bearer-token)
    - [Story 2: A remote scraper already has a client certificate](#story-2-a-remote-scraper-already-has-a-client-certificate)
  - [Risks and Mitigations](#risks-and-mitigations)
    - [Metrics may become visible beyond their intended audience](#metrics-may-become-visible-beyond-their-intended-audience)
    - [A shared client CA can authenticate unintended identities](#a-shared-client-ca-can-authenticate-unintended-identities)
- [Design Details](#design-details)
  - [API](#api)
  - [Validation](#validation)
  - [AccessControlMode handling](#accesscontrolmode-handling)
  - [Proposed follow-up: certificate-authenticated mode](#proposed-follow-up-certificate-authenticated-mode)
    - [API](#api-1)
    - [Validation](#validation-1)
    - [Handling](#handling)
  - [Test Plan](#test-plan)
    - [Prerequisite testing updates](#prerequisite-testing-updates)
    - [Unit tests](#unit-tests)
    - [Integration tests](#integration-tests)
    - [e2e tests](#e2e-tests)
    - [Proposed follow-up tests](#proposed-follow-up-tests)
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

A proposed authenticated follow-up supports two alternative authentication
methods: the existing bearer token or a trusted client certificate. A scraper
using a client certificate does not need a bearer token. Both methods remain
subject to Kubernetes RBAC authorization for `GET /metrics`. Delivery and staging
are separate from the initial loopback-only implementation.

## Motivation

There are two independent use cases:

- **Loopback-only unauthenticated scraping.** Some platforms add a metrics scraper
  sidecar to the Kueue controller manager Pod. The administrator trusts the shared
  network namespace and wants a loopback-only endpoint without authentication.
- **Remote client-certificate scraping.** Remote collectors cannot reach the
  pod-local loopback listener. Some already have platform-issued client
  certificates and want to authenticate with those identities instead of
  provisioning another bearer credential. They still require Kubernetes RBAC
  permission to read metrics; this use case does not require the loopback opt-out.

For authenticated metrics requests, Kueue currently accepts bearer tokens that
pass TokenReview and SubjectAccessReview, with no client-certificate option.

### Why existing authentication cannot simply be configured

Some platforms authenticate the controller to the API server with a client
certificate and do not provision a bearer token for the sidecar that TokenReview
accepts. Client-certificate metrics authentication is not supported today (see
[Alternatives](#alternatives)).

### Goals

- Introduce a mechanism to opt out of metrics authentication and authorization,
  allowing a trusted pod-local scraper to collect metrics without a bearer token
  or metrics TokenReview/SubjectAccessReview calls.
- Retain authenticated HTTPS by default and reject unsafe opt-out addresses.
- Preserve verified HTTPS, serving-certificate rotation, and webhook behavior.
- Define an authenticated follow-up for remote scrapers supporting either the
  existing bearer-token method or a new client-certificate method. A scraper
  using a client certificate does not need a bearer token. Kubernetes RBAC
  authorization for `GET /metrics` applies to both methods.

### Non-Goals

- HTTP metrics on any address, or removing metrics certificate requirements.
- Unauthenticated metrics on wildcard, pod, Service, or external IPs.
- New metrics authentication or transport command-line flags.
- Container-level isolation within a pod, or a guarantee that host loopback is a
  pod-only boundary.
- Changes to controller-to-API-server authentication, ServiceAccount lifecycle,
  webhooks, job integrations, or metrics contents.
- New certificate issuance, custom authentication providers, or a proxy.
- Automatically removing upstream RBAC or reconfiguring external scrapers.

## Proposal

Allow administrators to disable metrics authentication and authorization and
control the metrics bind address for a trusted pod-local scraper. Opting out
requires an explicit loopback address; configurations that would expose
unauthenticated metrics beyond loopback are rejected before startup.
Authenticated HTTPS remains the default, and HTTPS is retained when opting out.

### User Stories

#### Story 1: Scraping metrics from a sidecar container without a bearer token

An administrator adds a metrics scraper sidecar to the Kueue controller manager
Pod and trusts all processes sharing its network namespace. They want the scraper
to collect metrics over verified HTTPS without provisioning a bearer token, while
keeping the metrics listener bound to loopback. The deployment supplies external
serving certificates, and the scraper trusts their CA and verifies a
certificate-matching server name.

#### Story 2: A remote scraper already has a client certificate

An administrator trusts the issuer of a remote collector's client certificate.
The collector cannot use pod loopback and should not receive an extra bearer
secret. The proposed authenticated mode accepts that certificate identity and
checks its metrics-reader permission. Other bearer-only collectors remain usable.
The configuration example is in the follow-up design below.

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

Add a task page under Observability covering the setup, the external certificate
requirement, and the trust boundary: other containers in the pod, `hostNetwork`,
and `kubectl port-forward`. Document service-mesh forwarding behavior as it is
verified.

#### A shared client CA can authenticate unintended identities

Client-CA trust establishes identity, not permission. Retain metrics authorization
and bind only intended identities to the metrics-reader role. Protect CA-bundle
write access as authentication-policy administration. The API-server serving CA
is not necessarily the CA that signed the scraper certificate.

Certificate identity takes precedence when both credentials are presented;
invalid or denied certificates must not fall back to a more privileged token.
Client authentication does not make Kueue's serving certificate trusted by the
scraper; serving CA/SAN verification remains a separate responsibility.

## Design Details

### API

Add `AccessControlMode` to `ControllerMetrics` in the
`config.kueue.x-k8s.io/v1beta2` Configuration API. Other existing fields are
omitted from this excerpt:

```go
// MetricsAccessControlMode specifies how access to the metrics listener is controlled.
type MetricsAccessControlMode string

const (
	// MetricsAccessControlModeDelegated delegates authentication and authorization
	// to Kubernetes through controller-runtime.
	MetricsAccessControlModeDelegated MetricsAccessControlMode = "Delegated"

	// MetricsAccessControlModeNone disables authentication and authorization
	// for the metrics listener.
	MetricsAccessControlModeNone MetricsAccessControlMode = "None"
)

type ControllerMetrics struct {
	// AccessControlMode selects how the metrics listener authenticates and authorizes requests.
	// Possible values are:
	// - Delegated: each request needs a bearer token that passes TokenReview and SubjectAccessReview.
	// - None: no authentication or authorization. For enabled metrics, requires the
	//   MetricsAccessControl feature gate and a loopback IP and port in bindAddress.
	// Defaults to Delegated.
	// +optional
	AccessControlMode *MetricsAccessControlMode `json:"accessControlMode,omitempty"`
}
```

### Validation

Validate after configuration defaulting and feature-gate loading, before
initializing certificates or starting listeners:

- `metrics.accessControlMode` must be `Delegated` or `None`. An omitted or null
  field defaults to `Delegated`; explicit empty strings and unknown values are
  invalid.
- When metrics are enabled, `accessControlMode: None` requires the
  `MetricsAccessControl` feature gate to be enabled.
- When metrics are enabled, `accessControlMode: None` requires a literal loopback
  IP and numeric port in `metrics.bindAddress`. Accept IPv4, IPv6, and IPv4-mapped
  loopback addresses, such as `127.0.0.1:8443`, `[::1]:8443`, and
  `[::ffff:127.0.0.1]:8443`. Ports must be in the range 1–65535; reject port zero
  because the sidecar has no mechanism to discover an operating-system-assigned
  port. Reject wildcard and non-loopback addresses, hostnames (including
  `localhost`), URLs, missing ports, and malformed values.
- Kueue defaults an omitted or empty bind address to `:8443`; reject that wildcard
  address when `accessControlMode: None` is used for enabled metrics.
- `metrics.bindAddress: "0"` disables metrics. No listener is created, and the
  opt-out gate and loopback checks are skipped. The access-control value must
  still be valid.

Invalid configurations fail startup with an error naming the relevant field and
explaining how to correct it.

### AccessControlMode handling

Configure the controller-runtime metrics server according to
`metrics.accessControlMode`:

- `Delegated`: use `FilterProvider: filters.WithAuthenticationAndAuthorization`.
- `None`: use `FilterProvider: nil`; metrics requests require no bearer token and
  make no TokenReview or SubjectAccessReview calls. This affects every path on
  the metrics listener, currently only `/metrics`.

Both modes retain `SecureServing: true` and existing certificate loading and
rotation. Defaults, webhook behavior, and upstream RBAC remain unchanged.
For an enabled listener using `None`, the metrics options helper logs a startup
warning with the bind address stating that authentication and authorization are
disabled and metrics may expose tenant information to other processes sharing
the network namespace.

### Proposed follow-up: certificate-authenticated mode

This extension is proposed for maintainer discussion, not part of the initial
`Delegated`/`None` Alpha or a requirement for its implementation. The mode name,
feature-gate staging, and delivery milestone must be agreed separately.

#### API

Extend the existing mode enum, rather than introduce a separate access-control
API. Proposed additions to the Configuration API `v1beta2`:

```go
// MetricsAccessControlModeClientCertificateOrBearer uses a trusted client
// certificate, or a bearer token when no certificate is presented, and retains
// Kubernetes authorization. Proposed follow-up, not initial Alpha.
const MetricsAccessControlModeClientCertificateOrBearer MetricsAccessControlMode = "ClientCertificateOrBearer"

type ControllerMetrics struct {
    // ClientCAFile identifies a public PEM bundle for metrics client certificates.
    // Required only in ClientCertificateOrBearer; unset/null preserves the existing
    // mode contract. The path is startup-only; valid contents reload on change.
    // +optional
    ClientCAFile *string `json:"clientCAFile,omitempty"`
}
```

Example using externally managed serving certificates, after that extension is
implemented and its gate is enabled:

```yaml
internalCertManagement:
  enable: false
metrics:
  accessControlMode: ClientCertificateOrBearer
  bindAddress: ":8443"
  clientCAFile: /etc/kueue/metrics-client-ca/ca.crt
```

The default controller-runtime fallback is a self-signed serving certificate for
`localhost` and `127.0.0.1`, not a certificate a remote scraper can verify against
the metrics Service's DNS name out of the box. For this example, supply the
serving certificate and private key at `/etc/kueue/metrics/certs/tls.crt` and
`/etc/kueue/metrics/certs/tls.key`. The serving certificate must cover the name
used by the scraper; configure the scraper to trust its issuing CA and verify
that name without skipping TLS verification.

`clientCAFile` is a separate trust setting: it contains the public CA bundle used
by Kueue to verify the scraper's client certificate. It does not configure the
scraper's trust of Kueue's serving certificate, and must not contain a CA private
key.

Disabling internal certificate management also requires externally managed
webhook and visibility serving certificates and their existing CA wiring. Follow
[Configure Prometheus with TLS](../../site/content/en/docs/tasks/manage/productization/prometheus.md)
for the deployment setup; setting the flag alone does not provision certificates.
This example uses the existing external-certificate path, not a new requirement
or certificate-management change imposed by the authentication mode.

#### Validation

- Initial Alpha rejects the new mode/field. The follow-up requires agreed
  feature-gate staging; gate-off with the mode configured must fail startup,
  not fall back to weaker access controls.
- Require `clientCAFile` in this mode and reject it in `Delegated` or `None`.
  Reject empty, whitespace-only, or non-absolute paths. Enabled metrics fail
  startup for unreadable, empty, or malformed bundles. Disabled metrics perform
  no bundle I/O or watcher setup.
- This authenticated mode does not require loopback. The existing `None` address
  restrictions remain unchanged.

#### Handling

Keep HTTPS and request an optional client certificate. Verify its chain,
validity, and clientAuth usage against the supplied bundle. Authenticate the TLS
peer using Kubernetes X.509 username/group mapping, then authorize non-resource
`GET /metrics` through SubjectAccessReview. Do not trust identity headers, infer
permission from certificate validity, or issue new certificates.

| Presented credentials | Authentication and authorization |
|---|---|
| None | HTTP 401 |
| Bearer only | Existing TokenReview and SubjectAccessReview |
| Trusted certificate | Certificate identity; SubjectAccessReview allows 200 or denies 403 |
| Trusted certificate and token | Certificate identity; no token retry after denial |
| Invalid/expired/wrong-usage certificate | Rejected; no metrics or token fallback |

Reject unusable certificate identities rather than reinterpreting them as bearer
requests. Certificate requests avoid TokenReview, not SubjectAccessReview.
Changing the CA path requires a restart; reload valid bundle contents for TLS
and request authentication, including CA removal on reused connections. Malformed
updates retain the last valid bundle and warn, never enabling trust-all or
anonymous access. Use overlapping bundles for CA migration. Existing serving
certificate loading/rotation, TLS profiles, and scraper reload responsibilities
remain independent.

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
- IPv4/IPv6/mapped loopback with ports 1–65535 accepted; wildcard and non-loopback
  addresses, hostnames, missing/out-of-range ports, port zero, and malformed
  addresses rejected when `accessControlMode: None` is used for enabled metrics.
- Omitted/empty bind addresses defaulting to `:8443` and being rejected when
  `accessControlMode: None` is used.
- The default and explicit `Delegated` authentication filter, gate-enabled `None`
  with no authentication filter, and HTTPS in every enabled mode.
- Actionable errors for unsafe addresses and a gate-disabled opt-out.
- The helper emits the startup warning, including the bind address and tenant
  information risk, for an enabled listener using `None`. It emits no opt-out
  warning for `Delegated` or disabled metrics.

#### Integration tests

No new integration-framework tests are planned. Configuration unit tests cover
the gate, validation, and options; the cert-manager e2e test covers live serving
behavior.

#### e2e tests

Add one test to the cert-manager e2e suite, run in a separate sequential phase
after the existing metrics tests so no other test observes the temporary shared
configuration. Before modifying shared resources, register cleanup to restore
the original configuration and Deployment even if the test fails. Wait for the
restored manager to become available and confirm authenticated HTTPS scraping
through the Service works again.

The test uses the alpha gate, `metrics.accessControlMode: None`, an explicit
loopback address, and `internalCertManagement.enable: false`. Supply external
serving certificates and add a sidecar configured with the trusted CA and a
matching TLS server name. Assert that the sidecar can scrape metrics over verified HTTPS without a bearer
token and that a direct connection to the metrics port on the manager pod IP is
refused. Keep existing authenticated HTTPS and certificate coverage unchanged.

#### Proposed follow-up tests

Extend configuration tests for the mode/CA field, gate-off rollback, invalid
bundles, disabled metrics, and cross-mode rejection. Real TLS tests with controlled
review APIs must cover the credential table, prove certificate identity reaches
SubjectAccessReview with no TokenReview call, and test leaf/CA rotation, reused
connections, and malformed bundle updates. Extend the certificate-management E2E
job with remote certificate/no-token scraping, denied identities, rotation, and
bearer-only compatibility. Require verification of the serving CA and matching
server name; an untrusted serving CA or name mismatch must fail even with a valid
client certificate. Restore shared configuration afterward. These are follow-up
tests, not prerequisites for initial `None`.

### Graduation Criteria

The stages below describe the initial loopback opt-out. Delivery and maturity of
the authenticated follow-up must be agreed separately.


#### Alpha

- `metrics.accessControlMode` with the `Delegated` default and the `None` opt-out.
- `MetricsAccessControl` feature gate disabled by default.
- Unit tests for validation and the metrics server options, and an e2e test in
  the cert-manager suite where a sidecar scrapes over verified HTTPS without a
  token and the pod IP is refused.

#### Beta

- Feature gate enabled by default.
- Positive feedback from users, and no open security issues against the opt-out.
- Behavior with a service mesh sidecar that forwards inbound traffic to
  localhost is verified.
- Review the certificate-authenticated follow-up's API, credential precedence,
  CA rollover, gate staging, and delivery target before treating it as supported.
- Re-evaluate an Event or a metric for the access-control mode based on user
  feedback.

#### GA

- All reported bugs are addressed.
- Feature gate locked to true.

## Implementation History

- 2026-10-01: Initial KEP draft.
- 2026-10-19: Proposed client-certificate-authenticated follow-up after maintainer
  feedback to align enhancement 17432 with this KEP; not implemented or approved.

## Drawbacks

This deliberately weakens metrics access controls for opted-in deployments and
adds configurations that must remain tested and documented. Loopback does not
protect against untrusted colocated processes, host networking, or forwarding.
It adds a configuration field and validation while retaining the existing
serving-certificate dependency.

## Alternatives

1. **Client-certificate metrics authentication.** Maintainers have prototyped
   certificate identity with RBAC checks. It is now described as a proposed
   authenticated follow-up mode above, not a replacement for the loopback opt-out:
   administrators who intentionally use the shared network namespace as their
   access boundary still have the separate `None` use case.
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
