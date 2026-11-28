---
title: "Scrape metrics with a client certificate"
description: Authenticate a metrics scraper with X.509 and authorize it through Kubernetes RBAC.
weight: 6
---

The alpha `ClientCertificateOrBearer` metrics access-control mode accepts a
trusted X.509 client certificate, or a bearer token when no certificate is
presented. Both identities still require Kubernetes SubjectAccessReview
permission for non-resource `GET /metrics`. Trusting the issuer alone does not
grant metrics access. `Delegated` remains the default mode.

## Configure the manager

Enable the `MetricsAccessControl` feature gate and configure the public client-CA
bundle in the controller manager configuration:

```yaml
featureGates:
  MetricsAccessControl: true
metrics:
  bindAddress: ":8443"
  accessControlMode: ClientCertificateOrBearer
  clientCAFile: /etc/kueue/metrics/client-ca/ca.crt
```

Mount the bundle read-only, for example from a ConfigMap. `clientCAFile` must be
an absolute, nonempty path to a readable PEM bundle. An absent, unreadable,
empty, or malformed bundle fails startup. **Do not mount the CA private key**:
Kueue verifies client certificates; it does not issue or renew them. Prefer a
dedicated client CA, or assess every identity that the chosen CA can issue.

The bundle is independent of the metrics serving certificate. Continue to
configure and verify the server's certificate as described in
[Configure Prometheus with TLS](/docs/tasks/manage/productization/prometheus).
Do not use the serving certificate's private key as a client credential.

## Authorize the scraper identity

The certificate's Common Name becomes the Kubernetes username; Organization
fields become groups. Kubernetes also adds `system:authenticated`. Certificates
must be valid for client authentication, have a nonempty Common Name, and chain
to the configured client CA.

For a certificate with Common Name `metrics-reader`, grant only metrics access:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: certificate-metrics-reader
rules:
  - nonResourceURLs: ["/metrics"]
    verbs: ["get"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: certificate-metrics-reader
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: certificate-metrics-reader
subjects:
  - apiGroup: rbac.authorization.k8s.io
    kind: User
    name: metrics-reader
```

The manager's existing SubjectAccessReview and TokenReview permissions are still
required. Certificate-only scrapes do not make TokenReview calls, but they do
use SubjectAccessReview, including the normal delegated authorization cache.

## Configure the scraper

Configure the serving CA separately from the scraper's client certificate and
private key. For example:

```yaml
scrape_configs:
  - job_name: kueue
    scheme: https
    static_configs:
      - targets:
          - kueue-controller-manager-metrics-service.kueue-system.svc:8443
    tls_config:
      ca_file: /etc/scraper/server-ca/ca.crt
      server_name: kueue-controller-manager-metrics-service.kueue-system.svc
      cert_file: /etc/scraper/client/tls.crt
      key_file: /etc/scraper/client/tls.key
```

No bearer token is needed for this certificate-authenticated target. Keep TLS
server verification enabled and use the actual server certificate's DNS name.

If both a certificate and a bearer token are presented, the certificate identity
wins. An invalid or unauthorized certificate must not fall back to the token.
A request with no certificate can still use the existing bearer-token flow;
anonymous access is not enabled. Clients must actually present the certificate
for this precedence rule to apply.

## Rotate credentials and roll back

Kueue reloads valid changes to the mounted client-CA bundle without restarting,
including atomic file replacement. Reload runs on every manager replica, not
only the elected leader. Failed updates are logged and retain the last valid
bundle; an empty or malformed update never enables anonymous access.

For CA rotation, publish an overlap bundle containing both issuers, rotate client
certificates, then remove the old issuer. New TLS handshakes and certificate
request authentication use the current bundle. Removed issuers are rejected on
subsequent requests even over existing connections. Client leaf-certificate
renewal under a still-trusted issuer needs no manager restart. Certificate
issuance, private-key rotation, and CRL/OCSP revocation are not provided by Kueue.

`metrics.bindAddress: "0"` disables the listener and skips loading the CA file,
but does not bypass the certificate mode's feature-gate and path validation.

To roll back, set `metrics.accessControlMode: Delegated`, remove `clientCAFile`,
and restart the manager. Before disabling the feature gate, also remove
`accessControlMode`; both fields require the gate even when metrics are disabled.
With both fields unset, delegated authentication remains in effect. `clientCAFile`
is forbidden in `Delegated` and `None` modes so it cannot silently alter their
existing contracts.
