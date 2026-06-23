---
title: "Configure Custom Metric Labels"
linkTitle: "Custom Metric Labels"
date: 2026-03-10
weight: 4
description: >
  Add custom Prometheus labels to Kueue ClusterQueue, LocalQueue, and Cohort metrics from Kubernetes metadata.
---

This page shows you how to configure custom metric labels that promote Kubernetes
labels or annotations on ClusterQueues, LocalQueues, and Cohorts into additional
Prometheus metric label dimensions.

The intended audience for this page are [batch administrators](/docs/tasks#batch-administrator).

## Before you begin

Make sure the following conditions are met:

- A Kubernetes cluster is running.
- The kubectl command-line tool has communication with your cluster.
- [Kueue is installed](/docs/installation).
- Prometheus is [set up](/docs/tasks/manage/observability/setup_prometheus) to scrape Kueue metrics.

## Configure custom labels

Custom metric labels require the `CustomMetricLabels` feature gate, which is
alpha and disabled by default. See
[Installation](/docs/installation/#change-the-feature-gates-configuration)
for details on how to enable feature gates.

Add a `metrics.customLabels` section to the Kueue controller configuration.
Each entry defines one extra Prometheus label dimension on ClusterQueue,
LocalQueue, and Cohort metrics.

```yaml
apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
featureGates:
  CustomMetricLabels: true
metrics:
  customLabels:
    - name: team
    - name: cost_center
      sourceLabelKey: "billing/cost-center"
    - name: budget
      sourceAnnotationKey: "billing.example.com/budget"
```

### Field reference

| Field                | Description |
|----------------------|-------------|
| `name`               | Suffix for the Prometheus label. Kueue prepends `custom_` automatically (e.g. `name: team` → label `custom_team`). Must follow Prometheus label naming conventions: `[a-zA-Z_][a-zA-Z0-9_]*`. |
| `sourceLabelKey`     | Kubernetes label key to read the value from. Must be a valid Kubernetes qualified name. Mutually exclusive with `sourceAnnotationKey`. Defaults to `name` if neither is specified. |
| `sourceAnnotationKey`| Kubernetes annotation key to read the value from. Must be a valid Kubernetes qualified name. Mutually exclusive with `sourceLabelKey`. |

A maximum of **8** custom labels can be configured.

### How values are resolved

For each ClusterQueue, LocalQueue, or Cohort, Kueue reads the value of the
specified Kubernetes label or annotation. If the object does not have the
label/annotation, an empty string is used as the value.

## Example: label by team

Using the configuration above with `name: team`:

1. Create a ClusterQueue with a `team` label:

    ```yaml
    apiVersion: kueue.x-k8s.io/v1beta2
    kind: ClusterQueue
    metadata:
      name: engineering-cq
      labels:
        team: platform
    spec:
      resourceGroups:
        - coveredResources: ["cpu"]
          flavors:
            - name: default
              resources:
                - name: cpu
                  nominalQuota: 10
    ```

2. Query metrics filtered by team:

    ```promql
    kueue_pending_workloads{custom_team="platform"}
    kueue_admitted_workloads_total{custom_team="platform"}
    ```

{{% alert title="Cardinality considerations" color="warning" %}}
Each unique combination of label values creates a separate time series in
Prometheus. Keep the number of distinct values low (e.g. team names, cost
centers) to avoid excessive cardinality. Avoid using high-cardinality values
such as user IDs or timestamps.
{{% /alert %}}

## What's next

- See [Prometheus Metrics](/docs/reference/metrics) for the full list of Kueue metrics.
- See [Common Grafana Queries](/docs/tasks/manage/observability/common_grafana_queries) for PromQL queries to monitor Kueue in Grafana.
