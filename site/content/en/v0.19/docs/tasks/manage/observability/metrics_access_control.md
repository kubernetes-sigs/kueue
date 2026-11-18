---
title: "Scrape metrics from a sidecar"
description: Disable metrics authentication for a trusted scraper in the manager Pod.
weight: 5
---

Kueue serves metrics over HTTPS. By default, requests need a bearer token accepted
by Kubernetes TokenReview and SubjectAccessReview. The alpha
`DisableMetricsAccessControl` feature gate (disabled by default) allows trusted
sidecars to scrape metrics without a bearer token. This is a release-0.19 backport
of [the metrics access-control feature](https://github.com/kubernetes-sigs/kueue/pull/16035).

## Configure the manager

Set these fields in the controller manager configuration, preserving your other
settings:

```yaml
featureGates:
  DisableMetricsAccessControl: true
metrics:
  bindAddress: "127.0.0.1:8443"
internalCertManagement:
  enable: false
```

Supply external serving certificates. Mount the metrics certificate and key at
`/etc/kueue/metrics/certs/tls.crt` and `/etc/kueue/metrics/certs/tls.key`. They are
loaded at startup and watched for rotation. Also supply the webhook and any other
enabled servers' certificates as described in
[Configure Prometheus with TLS](/v0.19/docs/tasks/manage/productization/prometheus).
The default internal certificate management uses a self-signed metrics certificate
regenerated at startup; use external certificates for verified sidecar HTTPS.

Restart the Deployment after updating its configuration and certificate mounts:

```shell
kubectl rollout restart deployment/kueue-controller-manager -n kueue-system
```

Enabling `DisableMetricsAccessControl` removes authentication and authorization
from every path on the metrics listener, currently only `/metrics`, and makes no
metrics TokenReview or
SubjectAccessReview calls. HTTPS remains required. Webhook behavior and upstream
RBAC remain unchanged.

An enabled unauthenticated listener requires a literal loopback IP and a numeric
port from 1 to 65535, such as `127.0.0.1:8443`, `[::1]:8443`, or
`[::ffff:127.0.0.1]:8443`. Wildcards, pod IPs, hostnames including `localhost`,
URLs, missing ports, and port zero fail startup. Omitted or empty bind addresses
default to `:8443`, which is rejected when the gate is enabled.

With the gate enabled, `metrics.bindAddress: "0"` disables metrics and skips the
loopback checks. With the gate disabled, delegated authentication is retained.

## Configure the sidecar

Mount the serving CA into the scraper and configure a TLS server name matching
the serving certificate. A certificate for a Service DNS name does not also
validate against a loopback IP. For example, a Prometheus sidecar could use:

```yaml
scrape_configs:
  - job_name: kueue
    scheme: https
    static_configs:
      - targets: ["127.0.0.1:8443"]
    tls_config:
      ca_file: /etc/scraper/certs/ca.crt
      server_name: kueue-controller-manager-metrics-service.kueue-system.svc
```

Use the actual certificate's DNS name and CA path. Keep TLS verification enabled.
External Prometheus servers and ServiceMonitors cannot scrape a pod's loopback
listener through its Service or pod IP.

## Assess the trust boundary

All containers sharing the manager Pod's network namespace can read these
metrics. With `hostNetwork: true`, node-local processes share that loopback
interface too. A proxy or a user with suitable execution or `kubectl port-forward`
permissions can forward the endpoint to other users. Service-mesh forwarding
behavior has not yet been verified.

Metrics can expose tenant information, including namespace and queue names,
pending workloads, and resource usage. Kueue logs a startup warning when
unauthenticated metrics are enabled. Assess who can reach or forward the listener
before opting out; retain authentication when the audience needs tighter restrictions.

To restore authenticated metrics, set `DisableMetricsAccessControl: false` and
restart the manager.

## Upgrade to v0.21

This backport gate is specific to the release-0.19 branch. When upgrading to v0.21,
remove `DisableMetricsAccessControl` and use the configuration API introduced there:

```yaml
featureGates:
  MetricsAccessControl: true
metrics:
  accessControlMode: None
  bindAddress: "127.0.0.1:8443"
```

Remove the old gate before starting v0.21; an unrecognized feature gate prevents
startup. HTTPS and loopback requirements still apply.
