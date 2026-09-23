---
title: "Setup Prometheus"
linkTitle: "Setup Prometheus"
date: 2026-02-04
weight: 1
description: >
  Enable Prometheus metrics scraping for Kueue
---

This page shows how to set up Prometheus to scrape Kueue metrics. For TLS-secured metrics endpoints, see [Configure Prometheus with TLS](/docs/tasks/manage/productization/prometheus).

The page is intended for a [batch administrator](/docs/tasks#batch-administrator).

## Before you begin

Make sure the following conditions are met:

- A Kubernetes cluster is running.
- The kubectl command-line tool has communication with your cluster.
- [Kueue is installed](/docs/installation).
- Prometheus Operator is [installed](https://prometheus-operator.dev/docs/getting-started/installation/).

## 1. Setup

Choose the setup method that matches your Kueue installation.

### Option A: Helm

If you installed Kueue using Helm, enable Prometheus scraping in your `values.yaml`:

```yaml
enablePrometheus: true
```

Then upgrade your Helm release:

```bash
helm upgrade kueue oci://registry.k8s.io/kueue/charts/kueue \
  --namespace kueue-system \
  -f values.yaml
```

### Option B: Manifests

If you installed Kueue using kubectl with the release manifests, apply the Prometheus ServiceMonitor:

```bash
VERSION={{< param "version" >}}
kubectl apply --server-side -f https://github.com/kubernetes-sigs/kueue/releases/download/${VERSION}/prometheus.yaml
```

## 2. Verify metrics

1. Check the ServiceMonitor is created:

   ```bash
   kubectl get servicemonitor -n kueue-system
   ```

   You should see `kueue-controller-manager-metrics-monitor` listed.

2. In the Prometheus UI, go to **Status > Target health** (or navigate to `/targets`) and verify that `kueue-system/kueue-controller-manager-metrics-monitor` shows as `UP`.

3. Run a test query in the Prometheus UI:

   ```promql
   kueue_admitted_workloads_total
   ```

   If Kueue has processed workloads, you should see data points for your ClusterQueues.

## 3. Enable optional metrics

By default, Kueue does not export resource-level metrics for ClusterQueues. To enable metrics like `kueue_cluster_queue_resource_usage` and `kueue_cluster_queue_nominal_quota`, set `enableClusterQueueResources: true` in the Kueue configuration.

Edit the `kueue-manager-config` ConfigMap:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: kueue-manager-config
  namespace: kueue-system
data:
  controller_manager_config.yaml: |
    apiVersion: config.kueue.x-k8s.io/v1beta2
    kind: Configuration
    metrics:
      bindAddress: :8443
      enableClusterQueueResources: true
    # ... other configuration
```

Restart the controller to apply the changes:

```bash
kubectl rollout restart deployment/kueue-controller-manager -n kueue-system
```

See [Prometheus Metrics](/docs/reference/metrics#optional-metrics) for the full list of optional metrics.

## Scraping from a sidecar over loopback

The manager flag `--metrics-authentication` defaults to `true`. By default,
metrics requests require bearer-token authentication through Kubernetes
TokenReview and authorization through SubjectAccessReview.

For a trusted scraper running in the same pod, you can explicitly disable both
checks while retaining HTTPS and metrics collection:

```sh
/manager --config=/etc/kueue/config/controller_manager_config.yaml \
  --metrics-authentication=false
```

Set the following in the manager configuration file:

```yaml
metrics:
  bindAddress: "127.0.0.1:8443"
```

When authentication is disabled, `metrics.bindAddress` must contain an explicit
loopback IP and a numeric port. IPv4 loopback addresses and IPv6 loopback
(`[::1]:8443`) are supported. Empty addresses, wildcard addresses such as `:8443`,
non-loopback IPs, hostnames (including `localhost`), and malformed addresses cause
startup to fail. Setting `metrics.bindAddress: "0"` continues to disable metrics
with either flag value.

All containers in the pod can access this endpoint without a bearer token.
Loopback is a **pod-level network boundary, not container isolation**. With
`hostNetwork: true`, loopback is shared with the node and other host-networked
processes. Use the opt-out only when that network namespace is trusted. An
external Prometheus server or ServiceMonitor cannot scrape a pod's loopback
endpoint through its Service or pod IP.

HTTPS and certificate verification are still required. Configure the sidecar
scraper to trust the serving CA and verify a hostname or IP present in the
certificate's subject alternative names. A certificate issued only for a
service-registry DNS name does **not** automatically validate against
`127.0.0.1`. If the scraper connects to loopback with such a certificate, configure
its TLS server name to match that DNS name. Do not disable TLS verification.

For example, a Prometheus sidecar could use:

```yaml
scrape_configs:
  - job_name: kueue
    scheme: https
    static_configs:
      - targets: ["127.0.0.1:8443"]
    tls_config:
      ca_file: /etc/scraper/certs/ca.crt
      server_name: kueue-metrics.example.com # Must match the serving certificate.
```

The flag also works with `internalCertManagement.enable: false`. In that mode,
the metrics certificate watcher continues to load and rotate
`/etc/kueue/metrics/certs/tls.crt` and `/etc/kueue/metrics/certs/tls.key`.
Webhook certificate configuration is separate and is unaffected.

Configure the flag in the manager container's arguments, including when using
a customized Helm deployment; it is not a field in the manager configuration
API. Upstream Helm and RBAC defaults remain authenticated. Sidecar TLS/server-name
wiring and removal of any metrics-auth RBAC that is no longer needed in a
downstream deployment are separate deployment changes. Keep that RBAC wherever
authenticated metrics or other users of the review APIs still require it.

## What's next

- See [Common Grafana Queries](/docs/tasks/manage/observability/common_grafana_queries) for PromQL queries to monitor Kueue in Grafana.
- See [Configure Prometheus with TLS](/docs/tasks/manage/productization/prometheus) for advanced TLS configuration using cert-manager.
- See [Setup Dev Monitoring](/docs/tasks/dev/setup_dev_monitoring) for a local development setup with Prometheus.
