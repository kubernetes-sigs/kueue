# KEP-7066: Custom Metadata Labels for Kueue Metrics

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [Configuration API](#configuration-api)
  - [Label Sanitization](#label-sanitization)
  - [Affected Metrics](#affected-metrics)
  - [Implementation Approach](#implementation-approach)
  - [Metrics Documentation Generator](#metrics-documentation-generator)
  - [Validation](#validation)
  - [Test Plan](#test-plan)
    - [Unit Tests](#unit-tests)
    - [Integration Tests](#integration-tests)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
  - [Single Static Label](#single-static-label)
  - [Numbered Static Labels](#numbered-static-labels)
  - [Configurable with Override Name](#configurable-with-override-name)
<!-- /toc -->

## Summary

Allow cluster admins to configure Kueue to promote specific Kubernetes
labels from ClusterQueue and LocalQueue objects into Prometheus metric
labels (with a mandatory `custom_` prefix), enabling filtering and
aggregation by organizational metadata.

## Motivation

Kueue metrics have a fixed label set. Grouping metrics by categories
like team, environment, or cost center requires joining Kueue metrics
with external data sources in PromQL. Allowing users to add selected
Kubernetes labels as Prometheus metric labels would make it easier to
build dashboards and filter or aggregate metrics.

### Goals

- Configure which Kubernetes labels on ClusterQueue and LocalQueue objects
  become Prometheus metric labels.
- Enforce a `custom_` prefix on all user-defined Prometheus labels to
  prevent collisions with built-in names.
- Keep the metrics documentation auto-generation working.
- Validate configuration at startup.

### Non-Goals

- Runtime-dynamic label set changes (requires restart).
- Annotation-to-metric mapping.
- Custom labels on non-queue metrics (workload, cohort). Metrics that
  are not keyed by a specific queue (e.g., `AdmissionAttemptsTotal`,
  `PreemptedWorkloadsTotal`, `CohortWeightedShare`) are excluded.
- User-chosen Prometheus label names (generated automatically from the
  Kubernetes label key).

## Proposal

Add a `customLabels` string list to the `metrics` config section. At
startup, Kueue sanitizes each key, adds a `custom_` prefix, and
initializes ClusterQueue and LocalQueue metric vectors with the additional
label dimensions. When reporting metrics, the corresponding Kubernetes
label values are included in the label set.

### User Stories

**Team aggregation.** A ClusterQueue labeled `team=platform` produces
`custom_team="platform"` in metrics, enabling PromQL like
`sum by (custom_team) (kueue_cluster_queue_resource_usage)`.

**Environment filtering.** A ClusterQueue labeled `env=prod` produces
`custom_env="prod"`, allowing Grafana dashboards filtered by environment.

**Cost center grouping.** A ClusterQueue labeled `cost-center=12345`
produces `custom_cost_center="12345"`, enabling aggregation by cost
center.

### Risks and Mitigations

**Metric series growth.** Each custom label adds a dimension to existing
queue metric series. However, since custom label values are read from
the queue object itself, the total number of series stays the same
since no new combinations are introduced. Documentation will advise keeping the
list focused and avoiding labels whose values change frequently.

**Name collisions.** The mandatory `custom_` prefix prevents collisions
with current or future built-in labels.

**Stale metrics.** When a queue's label value changes, the old time series
persists until explicitly deleted. The controller must track previous
custom label values per queue and delete the old series when a change
is detected, before reporting with the new values.

## Design Details

### Configuration API

Extend `ControllerMetrics` in
`apis/config/v1beta2/configuration_types.go`:

```go
type ControllerMetrics struct {
    ...
    EnableClusterQueueResources bool     `json:"enableClusterQueueResources,omitempty"`

    // CustomLabels is a list of Kubernetes label keys whose values will be
    // added as extra Prometheus labels on ClusterQueue and LocalQueue metrics.
    // Each key is sanitized and prefixed with "custom_" to form the
    // Prometheus label name (e.g., "team" becomes "custom_team").
    // +optional
    CustomLabels []string `json:"customLabels,omitempty"`
}
```

Example configuration:

```yaml
metrics:
  enableClusterQueueResources: true
  customLabels:
    - "team"
    - "env"
    - "cost-center"
```

Resulting metric (existing labels truncated with `...`):

```
kueue_cluster_queue_resource_usage{
  cluster_queue="cq-1", cohort="c", ...,
  custom_team="platform", custom_env="prod",
  custom_cost_center="12345"} 4.5
```

### Label Sanitization

Hyphens, dots, and slashes in Kubernetes label keys are replaced with
underscores, then the `custom_` prefix is prepended:

| Kubernetes label key           | Prometheus label name                  |
|--------------------------------|----------------------------------------|
| `team`                         | `custom_team`                          |
| `cost-center`                  | `custom_cost_center`                   |
| `app.kubernetes.io/name`       | `custom_app_kubernetes_io_name`        |

### Affected Metrics

Custom labels are appended to metrics that report about a specific
queue. The selection criteria is: ClusterQueue metrics whose label set
includes `cluster_queue`, and LocalQueue metrics whose label set
includes `name`/`namespace`.

Metrics that are not keyed by a specific queue are excluded:
`AdmissionAttemptsTotal`, `PreemptedWorkloadsTotal`, and
`CohortWeightedShare`.

All ClusterQueue and LocalQueue metrics matching the criteria above
are affected. The full set is determined by the metric definitions in
`pkg/metrics/metrics.go` at the time of implementation.

### Implementation Approach

1. **Startup**: Read `customLabels`, validate, compute Prometheus names.
2. **Metric vector initialization**: Metric vectors are currently
   declared as package-level variables with `prometheus.NewXxxVec(...)`.
   Since the label set must include custom labels, the vectors need to
   be built after the configuration is loaded. The static declarations
   should remain in source so the metricsdoc generator can parse them,
   but the vectors must be reassigned with the extended label set before
   registration in `Register()`.
3. **Reporting**: All `Report*` functions currently use positional
   `WithLabelValues(...)` and accept primitive parameters (strings,
   ints), not queue objects. Custom label values must be threaded
   through these functions. The recommended approach is to pass a
   `[]string` of custom label values as an additional parameter,
   following the pattern used for `*roletracker.RoleTracker`. Missing
   labels on the queue object produce an empty string value.
4. **Deletion cleanup**: Existing `Clear*Metrics` functions use
   `DeletePartialMatch` and will match custom label dimensions
   automatically when a queue is deleted.
5. **Value-change cleanup**: When a queue's custom label value changes,
   the controller must delete the old series before reporting with the
   new values. This requires tracking previous custom label values per
   queue and detecting changes during reconciliation.

### Metrics Documentation Generator

The metricsdoc generator (`hack/tools/metricsdoc/main.go`) parses Go
source to find static `prometheus.NewXxxVec(...)` calls and extract their
label lists. Since custom labels are added at runtime, the generator
cannot see them.

The static `prometheus.NewXxxVec(...)` declarations with built-in labels
remain in source code so the generator can parse them. At startup, the
vectors are reassigned with the extended label set before registration.
The generator continues to work as-is for built-in labels. A
documentation note will explain that additional labels can appear when
`customLabels` is configured.

### Validation

At startup, each `customLabels` entry must:

1. Be a valid [Kubernetes label key][k8s-labels].
2. Produce a valid Prometheus label name after sanitization.
3. Not produce a duplicate derived name (e.g., `cost-center` and
   `cost.center` both map to `custom_cost_center`).

The `custom_` prefix ensures that any Kubernetes label key can be
safely used without conflicting with built-in metric labels.

Validation runs regardless of whether the `CustomMetricLabels` feature
gate is enabled, so configuration errors are caught early.

[k8s-labels]: https://kubernetes.io/docs/concepts/overview/working-with-objects/labels/#syntax-and-character-set

### Test Plan

[x] I/we understand the owners of the involved components may require
updates to existing tests to make this code solid enough prior to
committing the changes necessary to implement this enhancement.

#### Unit Tests

- Config validation (invalid keys, duplicates after sanitization).
- Label sanitization edge cases.
- Metric reporting with custom labels; missing labels produce empty values.
- Feature gate disabled: `customLabels` config is ignored, metrics have
  only built-in labels.

#### Integration Tests

- ClusterQueue and LocalQueue with matching labels: custom labels appear
  at `/metrics`.
- Queue without any of the configured custom labels: empty string values
  appear in metric series.
- Label value change on a queue: stale series cleaned up, new series
  appears.
- Empty `customLabels`: only built-in labels (backward compatibility).
- `CustomMetricLabels` enabled with `LocalQueueMetrics` disabled:
  ClusterQueue metrics get custom labels, LocalQueue metrics are not
  registered.

### Graduation Criteria

**Alpha (v0.17)**: Feature gate `CustomMetricLabels` (disabled by default),
config field, ClusterQueue and LocalQueue metrics support, validation,
tests, docs.

**Beta**: Positive feedback, gate defaults to enabled.

**GA**: No open bugs, gate locked on.

## Implementation History

- 2026-02-13: Initial KEP draft.

## Drawbacks

- Adds configuration complexity (mitigated: opt-in behind a feature gate,
  empty default).
- Adds extra label dimensions to queue metrics, slightly increasing
  per-series storage.

## Alternatives

### Single Static Label

Use a single well-known label (`kueue.x-k8s.io/metrics-category-label`)
and promote its value as a `category` Prometheus label. Too limited when
multiple custom categories are needed.

### Numbered Static Labels

Add pre-defined labels like `kueue.x-k8s.io/metrics-category2-label`,
`kueue.x-k8s.io/metrics-category3-label`, etc. Label names carry no
semantic meaning, and the fixed count may not match actual needs.

### Configurable with Override Name

Use a structured config with explicit Kubernetes and Prometheus label
fields, allowing users to rename labels:

```yaml
customMetricTags:
  clusterQueue:
    - resourceTag: team
    - resourceTag: gpu_type
      overrideMetricTag: my_gpu_type
```

Allowing arbitrary Prometheus names risks collisions with built-in labels.
Generating the `custom_` prefix automatically from the Kubernetes label
key is simpler and prevents collisions by construction.
