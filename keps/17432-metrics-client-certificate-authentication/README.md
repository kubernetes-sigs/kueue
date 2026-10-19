# KEP-17432: Client-certificate authentication for metrics

Provisional proposal for [enhancement 17432](https://github.com/kubernetes-sigs/kueue/issues/17432).
The release target is unassigned.
The configuration below is proposed, not supported by current Kueue releases.

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [Risks and Mitigations](#risks-and-mitigations)
    - [A broad client CA may authenticate unintended users](#a-broad-client-ca-may-authenticate-unintended-users)
    - [Multiple credentials can select an unexpected identity](#multiple-credentials-can-select-an-unexpected-identity)
    - [Client authentication can be mistaken for server authentication](#client-authentication-can-be-mistaken-for-server-authentication)
- [Design Details](#design-details)
  - [API](#api)
    - [Story: reuse a platform-issued scraper certificate](#story-reuse-a-platform-issued-scraper-certificate)
  - [Validation](#validation)
  - [Handling](#handling)
  - [Interactions](#interactions)
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

Allow metrics scrapers to authenticate with an existing X.509 client certificate,
without a bearer token, while retaining Kubernetes authorization for `/metrics`.

## Motivation

Currently, Kueue accepts metrics requests only with bearer tokens validated by
TokenReview and SubjectAccessReview.
Some platforms already issue and rotate client certificates for their scrapers.
Those credentials cannot authenticate to Kueue's metrics listener today.

Trusting the certificate issuer on the metrics server would let these platforms
reuse their existing scraper identity instead of distributing another secret.
An API-server client CA can be used if it signs that certificate; an API-server
serving CA is not necessarily the same authority.

### Goals

- Reuse existing client certificates for remote authenticated HTTPS scraping.
- Preserve `/metrics` authorization and compatibility with bearer-only scrapers.
- Support client-CA and client-leaf rotation without new certificate issuance in
  Kueue or the monitoring controller.

### Non-Goals

- Anonymous metrics, HTTP metrics, or bypassing Kubernetes authorization.
- Issuing certificates or automatically trusting a platform's API-server CA.
- Changing serving-certificate management, webhook TLS, or controller API access.
- Guaranteeing server identity when a scraper disables server verification.

## Proposal

Administrators may provide a trusted client-CA bundle for the metrics listener.
Kueue then accepts either a certificate-backed scraper identity or an existing
bearer-token identity, and authorizes the selected identity before serving data.
Certificate holders are not automatically granted metrics access.

### Risks and Mitigations

#### A broad client CA may authenticate unintended users

The bundle defines who can authenticate, not who can read metrics.
Retain authorization and bind only intended identities to the metrics-reader role.
Protect configuration and CA-bundle write permissions as authentication-policy
administration; do not automatically grant access to all authenticated users.

#### Multiple credentials can select an unexpected identity

Certificate identity takes precedence when a certificate is presented.
Invalid or unauthorized certificates cannot fall back to a supplied bearer token.
Use only verified TLS peer certificates, never identity headers from a scraper.

#### Client authentication can be mistaken for server authentication

The scraper's private key is not transmitted, but trusting its issuer does not
make Kueue's serving certificate trusted by the scraper.
Server CA/SAN verification remains a separate deployment responsibility.
This feature does not certify skip-verification configurations as secure.

## Design Details

### API

Add one field to `ControllerMetrics` in Configuration API `v1beta2`:

```go
type ControllerMetrics struct {
    // ClientCAFile identifies a PEM CA bundle used to authenticate metrics clients.
    // Unset or null preserves bearer-token-only authentication.
    // When set, the listener also accepts trusted client certificates and still
    // authorizes every authenticated identity for GET /metrics.
    // The path is read at startup; bundle contents are reloaded on change.
    // +optional
    ClientCAFile *string `json:"clientCAFile,omitempty"`
}
```

The configuration field is startup-only; changing its path requires a restart.
Its file contents may rotate without restarting kueue-controller-manager.
Existing `v1beta1` configurations retain their current behavior; the new option
is configured through `v1beta2` and must not be silently lost in conversion.

#### Story: reuse a platform-issued scraper certificate

The administrator trusts an existing client-certificate issuer and enables the
proposed alpha feature:

```yaml
apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
featureGates:
  MetricsClientCertificateAuthentication: true
metrics:
  bindAddress: ":8443"
  clientCAFile: /etc/kueue/metrics-client-ca/ca.crt
```

Mount only the issuer's public bundle into the manager.
Configure the scraper with its existing client certificate and private key,
without a bearer token, and grant its certificate identity the existing
metrics-reader role.
For example, for a certificate whose Kubernetes username is `metrics-scraper`:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: kueue-metrics-reader-client-cert
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: kueue-metrics-reader
subjects:
- apiGroup: rbac.authorization.k8s.io
  kind: User
  name: metrics-scraper
```

The role name follows the installation's naming convention.
The client CA must match the signer of the actual client certificate.
Scraper configuration of the CA and server name for Kueue's serving certificate
is independent of the public client-CA bundle mounted into the manager.

### Validation

- Introduce alpha gate `MetricsClientCertificateAuthentication`, off by default.
  Reject a configured `clientCAFile` if the gate is off, including after rollback.
- Reject explicit empty, whitespace-only, or non-absolute paths.
  When metrics are enabled, fail startup on an unreadable, empty, or malformed
  CA bundle; never continue with anonymous access or trust-all behavior.
- Preserve disabled metrics (`bindAddress: "0"`): validate field/gate consistency,
  but do not read the bundle or start a client-CA watcher for an absent listener.

### Handling

When the field is unset, keep existing authentication behavior.
When configured, keep HTTPS and request an optional client certificate,
verifying it against the configured bundle if present.
A client without a certificate can still use bearer authentication.

For certificate requests, validate the chain, validity, and clientAuth usage,
and use Kubernetes X.509 username/group mapping before SubjectAccessReview.
A trusted certificate without a usable username is rejected, not reinterpreted
as a bearer-token request.
Do not infer permission from TLS success or special-case an issuer or username.

| Request credentials | Authentication | Authorization / result |
|---|---|---|
| None | Unauthenticated | HTTP 401 |
| Bearer token only | Existing TokenReview | Existing SubjectAccessReview |
| Trusted client certificate, no token | Local X.509 verification | SubjectAccessReview; 200 if allowed, 403 if denied |
| Trusted certificate and token | Certificate identity only | No retry using token identity after denial |
| Expired, untrusted, or wrong-usage certificate | TLS rejection | No metrics response or bearer fallback |

Certificate authentication avoids TokenReview, not SubjectAccessReview.
The API-server dependency and existing authorization-cache behavior remain.
Normal client-leaf rotation needs no manager configuration change.

Reload the public client-CA bundle for both TLS and request authentication.
New valid bundles affect subsequent authentication decisions, including CA
removal on reused TLS connections; use overlapping bundles for CA migration.
A malformed runtime update retains the last valid bundle and logs a warning.
It never disables verification; administrators must repair the update to change
trust, and existing established connections are not forcibly terminated.

### Interactions

- **KEP-4377 / serving certificates:** independent trust direction; retain its
  HTTPS, serving-certificate loading/rotation, and webhook requirements.
- **KEP-16029 / access control:** extend the described `Delegated` mode to allow
  certificate-backed identities with SubjectAccessReview when configured.
  If its `None` mode is implemented, reject combining it with `clientCAFile`.
  This proposal does not implement or broaden anonymous loopback scraping.
- **TLS security profiles:** preserve configured protocol/cipher settings while
  adding client authentication; do not replace serving-certificate callbacks.

### Test Plan

- [x] Component owners may require additional tests before implementation.

#### Prerequisite testing updates

Make metrics authentication options testable independently of manager startup.
Exercise a real TLS listener so tests do not merely inject a synthetic identity.

#### Unit tests

Cover API defaulting/conversion, gate-off rollback, invalid paths and CA bundles,
disabled metrics, incompatible access-control modes, identity selection, and
last-valid-bundle handling.

#### Integration tests

Use real TLS plus controlled authentication/authorization APIs to prove every
credential/result row above, including denied certificates with a valid token.
Assert certificate requests make no TokenReview calls and SubjectAccessReview
receives the certificate-derived identity and non-resource `GET /metrics`.
Test leaf renewal, overlapping/removed CAs, reused connections, malformed bundle
updates, and unchanged bearer-only and serving-certificate behavior.

#### e2e tests

Extend the existing certificate-management E2E job with a mounted client CA,
client leaf, and metrics-reader binding.
Scrape successfully without a token, deny an unauthorized certificate, rotate
the client leaf and CA, and verify a bearer-only scraper still works.
Restore the manager configuration, bindings, and credentials after the test.

### Graduation Criteria

- **Alpha:** gate disabled by default; configuration, examples, and the planned
  TLS/authorization/rotation tests are present and selected by CI.
- **Beta:** gate enabled by default; issuer interoperability, CA rollover and
  rollback, credential precedence, and interaction with KEP-16029 are validated.
- **GA:** supported deployments have no unresolved authentication/authorization
  security issues; API and compatibility behavior are accepted and documented.

## Implementation History

- 2026-10-19: Enhancement issue opened and initial KEP proposed for discussion.
  No implementation, release target, or maintainer approval is claimed.

## Drawbacks

Adds client-CA lifecycle and identity-policy configuration for administrators.
A broad API-client CA may be undesirable; platforms still own certificate
issuance, least-privilege bindings, and scraper reloads.
Authorization still depends on Kubernetes availability.

## Alternatives

- **Bearer tokens:** supported today; keep this default for existing deployments.
- **Loopback opt-out (KEP-16029):** serves a different audience that trusts the
  pod's network namespace, not remote certificate-authenticated scrapers.
- **Authorize every CA-signed client:** smaller implementation but changes the
  authorization boundary for all clients of a shared CA; not proposed.
- **Issue new serving certificates:** addresses server identity, not missing
  client-certificate authentication; can be combined but is not required here.
- **Authentication proxy:** adds another component and policy/lifecycle surface;
  does not provide native certificate authentication for this listener.
