---
title: "Setup Dev Monitoring"
linkTitle: "Setup Dev Monitoring"
date: 2026-02-04
weight: 5
description: >
  Set up Prometheus for development and debugging
---

This page shows how to set up Prometheus for development, debugging, and testing Kueue metrics.

The page is intended for a [platform developer](/docs/tasks#platform-developer).

## Before you begin

Make sure the following conditions are met:

- A Kubernetes cluster is running.
- [Kueue is installed](/docs/installation).

## 1. Install kube-prometheus

From a scratch directory outside the Kueue repository, install [kube-prometheus](https://github.com/prometheus-operator/kube-prometheus):

```bash
git clone https://github.com/prometheus-operator/kube-prometheus.git
cd kube-prometheus
kubectl apply --server-side -f manifests/setup
kubectl wait --for condition=Established --all CustomResourceDefinition --namespace=monitoring
kubectl apply -f manifests/
kubectl wait --for=condition=Ready pods --all -n monitoring --timeout=300s
```

## 2. Enable Kueue metrics scraping

Apply the Kueue ServiceMonitor:

```bash
VERSION={{< param "version" >}}
kubectl apply --server-side -f https://github.com/kubernetes-sigs/kueue/releases/download/${VERSION}/prometheus.yaml
```

Alternatively, if you're working from a Kueue source checkout, use:

```bash
make prometheus
```

## 3. Generate test data

Create a ClusterQueue and LocalQueue:

```bash
kubectl apply -f https://kueue.sigs.k8s.io/examples/admin/single-clusterqueue-setup.yaml
```

Submit test jobs:

```bash
for i in {1..5}; do
  kubectl create -f https://kueue.sigs.k8s.io/examples/jobs/sample-job.yaml
done
```

## 4. Verify metrics

Port-forward to the Prometheus service:

```bash
kubectl -n monitoring port-forward svc/prometheus-k8s 9090:9090
```

Check that Prometheus is scraping Kueue:

```bash
curl -s 'http://localhost:9090/api/v1/targets' | jq '.data.activeTargets[] | select(.labels.job | contains("kueue"))'
```

You should see output like:

```json
{
  "labels": {
    "job": "kueue-controller-manager-metrics-service",
    ...
  },
  "health": "up",
  ...
}
```

Open http://localhost:9090 in your browser and try a query:

```promql
kueue_admitted_workloads_total
```

For Grafana access, see the [kube-prometheus documentation](https://github.com/prometheus-operator/kube-prometheus#access-the-dashboards).

## 5. Enable optional metrics

To enable resource-level metrics like `kueue_cluster_queue_resource_usage`, edit the Kueue configuration:

```bash
kubectl edit configmap kueue-manager-config -n kueue-system
```

Add `enableClusterQueueResources: true` under the `metrics` section:

```yaml
metrics:
  bindAddress: :8443
  enableClusterQueueResources: true
```

Restart Kueue:

```bash
kubectl rollout restart deployment/kueue-controller-manager -n kueue-system
```

Verify the optional metrics are available:

```promql
kueue_cluster_queue_nominal_quota
```

See [Prometheus Metrics](/docs/reference/metrics#optional-metrics) for the full list of optional metrics.

## What's next

- See [Prometheus Metrics](/docs/reference/metrics) for a complete list of available metrics.
- See [Setup Prometheus](/docs/tasks/manage/observability/setup_prometheus) for production setup instructions.
