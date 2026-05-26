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

## What's next

- See [Common Grafana Queries](/docs/tasks/manage/observability/common_grafana_queries) for PromQL queries to monitor Kueue in Grafana.
- See [Configure Prometheus with TLS](/docs/tasks/manage/productization/prometheus) for advanced TLS configuration using cert-manager.
- See [Setup Dev Monitoring](/docs/tasks/dev/setup_dev_monitoring) for a local development setup with Prometheus.
