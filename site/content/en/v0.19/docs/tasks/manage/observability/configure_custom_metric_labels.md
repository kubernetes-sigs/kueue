---
title: "Configure Custom Metric Labels"
linkTitle: "Custom Metric Labels"
date: 2026-03-10
weight: 4
description: >
  Add custom Prometheus labels to Kueue ClusterQueue, LocalQueue, Cohort, and Workload metrics from Kubernetes metadata.
---

This page shows you how to configure custom metric labels that promote Kubernetes
labels or annotations on ClusterQueues, LocalQueues, Cohorts, and Workloads into
additional Prometheus metric label dimensions.

The intended audience for this page are [batch administrators](/v0.19/docs/tasks#batch-administrator).

## Before you begin

Make sure the following conditions are met:

- A Kubernetes cluster is running.
- The kubectl command-line tool has communication with your cluster.
- [Kueue is installed](/v0.19/docs/installation).
- Prometheus is [set up](/v0.19/docs/tasks/manage/observability/setup_prometheus) to scrape Kueue metrics.

## Configure custom labels

Custom metric labels require the `CustomMetricLabels` feature gate, which is
alpha and disabled by default. See
[Installation](/v0.19/docs/installation/#change-the-feature-gates-configuration)
for details on how to enable feature gates.

Add a `metrics.customLabels` section to the Kueue controller configuration.
Each entry defines one extra Prometheus label dimension. The `sourceKind` field
selects the object the value is read from: `ClusterQueue` (the default),
`LocalQueue`, `Cohort`, or `Workload`.

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
    - name: workload_type
      sourceKind: Workload
      sourceLabelKey: "company.com/workload-type"
      trackedValues: ["inference", "training"]
```

### Field reference

| Field                | Description |
|----------------------|-------------|
| `name`               | Suffix for the Prometheus label. Kueue prepends `custom_` automatically (e.g. `name: team` → label `custom_team`). Must follow Prometheus label naming conventions: `[a-zA-Z][a-zA-Z0-9_]*`, and be unique across entries. |
| `sourceKind`         | Object the value is read from: `ClusterQueue` (default), `LocalQueue`, `Cohort`, or `Workload`. |
| `sourceLabelKey`     | Kubernetes label key to read the value from. Must be a valid Kubernetes qualified name. Mutually exclusive with `sourceAnnotationKey`. Defaults to `name` if neither is specified. |
| `sourceAnnotationKey`| Kubernetes annotation key to read the value from. Must be a valid Kubernetes qualified name. Mutually exclusive with `sourceLabelKey`. |
| `trackedValues`      | List of allowed values for the label. Required for `Workload` labels (1 to 12 values); optional for other kinds (up to 16), where an empty list allows any value. A value not in the list is reported as `kueue.x-k8s.io/_UNTRACKED_VALUE_`. |

The number of custom labels is limited to keep metric cardinality bounded: up to
**2** labels with `sourceKind: Workload`, up to **6** for each other kind, and
**20** in total. The configuration is validated at startup, so an invalid entry
prevents the controller from starting.

### How values are resolved

For each object, Kueue reads the value of the configured Kubernetes label or
annotation. If the object does not have it, an empty string is used.

For `ClusterQueue`, `LocalQueue`, and `Cohort` labels the value is read from the
object itself. For `Workload` labels the value is read from the Workload; Kueue
copies the configured source key from the underlying job (Job, JobSet, and other
supported kinds) onto the Workload it creates, so you set the label or annotation
on the job.

A `Workload` label must declare its allowed values in `trackedValues`. Workloads
are numerous and can carry arbitrary values, so any value outside the list is
reported under `kueue.x-k8s.io/_UNTRACKED_VALUE_`. This keeps the number of time
series bounded regardless of how many distinct values workloads use.

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

## Example: break down workloads by type

`Workload` labels split a workload count metric by a label or annotation carried
by individual workloads instead of by a queue. Currently they apply to the
`kueue_admitted_active_workloads` metric.

Using the `workload_type` entry from the configuration above:

1. Submit jobs carrying the `company.com/workload-type` label to a ClusterQueue
   `cq-1` labeled `team=platform`.

2. `kueue_admitted_active_workloads` carries both `custom_team` (from the
   ClusterQueue) and `custom_workload_type` (from each workload). With 10 active
   `inference` workloads, 20 `training`, and 3 with some other value:

    ```
    kueue_admitted_active_workloads{cluster_queue="cq-1", ..., custom_team="platform", custom_workload_type="inference"} 10
    kueue_admitted_active_workloads{cluster_queue="cq-1", ..., custom_team="platform", custom_workload_type="training"} 20
    kueue_admitted_active_workloads{cluster_queue="cq-1", ..., custom_team="platform", custom_workload_type="kueue.x-k8s.io/_UNTRACKED_VALUE_"} 3
    ```

3. Aggregate by workload type:

    ```promql
    sum by (custom_workload_type) (kueue_admitted_active_workloads)
    ```

{{% alert title="Cardinality considerations" color="warning" %}}
Each unique combination of label values creates a separate time series in
Prometheus. Keep the number of distinct values low (e.g. team names, cost
centers, a small set of workload types) to avoid excessive cardinality. Avoid
using high-cardinality values such as user IDs or timestamps. For `Workload`
labels, list only the values you need in `trackedValues`; everything else is
grouped under `kueue.x-k8s.io/_UNTRACKED_VALUE_`.
{{% /alert %}}

## What's next

- See [Prometheus Metrics](/v0.19/docs/reference/metrics) for the full list of Kueue metrics.
- See [Common Grafana Queries](/v0.19/docs/tasks/manage/observability/common_grafana_queries) for PromQL queries to monitor Kueue in Grafana.
