---
title: "Scrape metrics from a sidecar"
description: Disable metrics authentication for a trusted scraper in the manager Pod.
weight: 5
---

Kueue serves metrics over HTTPS. By default, `metrics.accessControlMode` is
`Delegated`: requests need a bearer token accepted by Kubernetes TokenReview and
SubjectAccessReview. With the alpha `MetricsAccessControl` feature gate, you can
set it to `None` for a trusted sidecar without a bearer token.

## Configure the manager

Set these fields in the controller manager configuration, preserving your other
settings:

```yaml
featureGates:
  MetricsAccessControl: true
metrics:
  accessControlMode: None
  bindAddress: "127.0.0.1:8443"
internalCertManagement:
  enable: false
```

Supply external serving certificates. Mount the metrics certificate and key at
`/etc/kueue/metrics/certs/tls.crt` and `/etc/kueue/metrics/certs/tls.key`. They are
loaded at startup and watched for rotation. Also supply the webhook and any other
enabled servers' certificates as described in
[Configure Prometheus with TLS](/docs/tasks/manage/productization/prometheus).
The default internal certificate management uses a self-signed metrics certificate
regenerated at startup; use external certificates for verified sidecar HTTPS.

Restart the Deployment after updating its configuration and certificate mounts:

```shell
kubectl rollout restart deployment/kueue-controller-manager -n kueue-system
```

`None` removes authentication and authorization from every path on the metrics
listener, currently only `/metrics`, and makes no metrics TokenReview or
SubjectAccessReview calls. HTTPS remains required. Webhook behavior and upstream
RBAC remain unchanged.

An enabled unauthenticated listener requires a literal loopback IP and a numeric
port from 1 to 65535, such as `127.0.0.1:8443`, `[::1]:8443`, or
`[::ffff:127.0.0.1]:8443`. Wildcards, pod IPs, hostnames including `localhost`,
URLs, missing ports, and port zero fail startup. Omitted or empty bind addresses
default to `:8443`, which is rejected with `None`.

`metrics.bindAddress: "0"` disables metrics and skips the gate and loopback checks.
The access-control mode must still be `Delegated` or `None`. Omitted or null modes
default to `Delegated`; empty and unknown values are invalid.

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
pending workloads, and resource usage. Kueue logs a startup warning when `None`
is enabled. Assess who can reach or forward the listener before opting out; retain
`Delegated` when the audience needs tighter restrictions.

To restore authenticated metrics, set `metrics.accessControlMode: Delegated` and
restart the manager. The feature gate may then be disabled.
