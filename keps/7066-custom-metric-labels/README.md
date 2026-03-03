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
  - [Label Name Validation](#label-name-validation)
  - [Affected Metrics](#affected-metrics)
  - [Implementation Approach](#implementation-approach)
  - [Metrics Documentation Generator](#metrics-documentation-generator)
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
labels or annotations from ClusterQueue, LocalQueue, and Cohort objects
into Prometheus metric labels (with a mandatory `custom_` prefix),
enabling filtering and aggregation by organizational metadata.

## Motivation

Kueue metrics have a fixed label set. Grouping metrics by categories
like team, environment, or cost center requires joining Kueue metrics
with external data sources in PromQL. Allowing users to add selected
Kubernetes labels or annotations as Prometheus metric labels would make
it easier to build dashboards and filter or aggregate metrics.

### Goals

- Configure which Kubernetes labels or annotations on ClusterQueue,
  LocalQueue, and Cohort objects become Prometheus metric labels.
- Keep the metrics documentation auto-generation working.
- Validate configuration at startup.

### Non-Goals

- Runtime-dynamic label set changes (requires restart).

## Proposal

Add a `customLabels` list to the `metrics` config section. Each entry
has a `name` (used as the Prometheus label suffix after prepending
`custom_`), and optionally a `sourceLabelKey` or `sourceAnnotationKey`
to specify where to read the value from. If neither source field is
set, `name` is used as the Kubernetes label key. At startup, Kueue
validates the configuration and initializes ClusterQueue, LocalQueue,
and Cohort metric vectors with the additional label dimensions. When
reporting metrics, the corresponding Kubernetes label or annotation
values are included in the label set.

### User Stories

**Team aggregation.** A ClusterQueue labeled `team=platform` produces
`custom_team="platform"` in metrics, enabling PromQL like
`sum by (custom_team) (kueue_cluster_queue_resource_usage)`.

**Environment filtering.** A ClusterQueue labeled `env=prod` produces
`custom_env="prod"`, allowing Grafana dashboards filtered by environment.

**Cost center grouping.** A ClusterQueue labeled `cost-center=12345`
is configured with `name: "cost_center"` and
`sourceLabelKey: "cost-center"`, producing
`custom_cost_center="12345"` in metrics.

**Annotation-based billing.** A ClusterQueue annotated with
`billing.company.com/budget=ABC-123` is configured with
`name: "budget_code"` and
`sourceAnnotationKey: "billing.company.com/budget"`, producing
`custom_budget_code="ABC-123"` in metrics.

**Example configuration:**

```yaml
metrics:
  enableClusterQueueResources: true
  customLabels:
    - name: "team"                                        # reads from label "team"
    - name: "env"                                         # reads from label "env"
    - name: "cost_center"
      sourceLabelKey: "cost-center"                       # reads from label "cost-center"
    - name: "budget_code"
      sourceAnnotationKey: "billing.company.com/budget"   # reads from annotation
```

Resulting metric:

```
kueue_cluster_queue_resource_usage{
  cluster_queue="cq-1", cohort="c", ...,
  custom_team="platform", custom_env="prod",
  custom_cost_center="12345",
  custom_budget_code="ABC-123"} 4.5
```

### Risks and Mitigations

**Metric series growth.** Custom labels do not increase active series
count in steady state — each queue still produces one set of label
values per metric. However, changing a custom label or annotation value
creates a new series; the old one must be explicitly deleted (see
[Value-change cleanup](#implementation-approach)). Admins should prefer
stable metadata (team, environment, cost center) and avoid values that
change frequently.

**Name collisions.** The mandatory `custom_` prefix prevents collisions
with current or future built-in labels.

**Stale metrics.** When a queue's label or annotation value changes,
the old series persists until explicitly deleted. See
[Value-change cleanup](#implementation-approach) for details.

## Design Details

### Configuration API

Extend `ControllerMetrics` in
`apis/config/v1beta2/configuration_types.go`:

```go
type ControllerMetricsCustomLabel struct {
    // Name is the Prometheus metric label name suffix.
    // Prepended with "custom_" to form the full Prometheus label name
    // (e.g., "team" becomes "custom_team").
    // Must contain only [a-zA-Z0-9_] characters and start with a letter.
    Name string `json:"name"`

    // SourceLabelKey is the Kubernetes label key to read the value from.
    // Mutually exclusive with SourceAnnotationKey.
    // If neither is specified, defaults to Name.
    // +optional
    SourceLabelKey string `json:"sourceLabelKey,omitempty"`

    // SourceAnnotationKey is the Kubernetes annotation key to read the value from.
    // Mutually exclusive with SourceLabelKey.
    // +optional
    SourceAnnotationKey string `json:"sourceAnnotationKey,omitempty"`
}

type ControllerMetrics struct {
    ...
    // CustomLabels is a list of entries whose values will be added as extra
    // Prometheus labels on ClusterQueue, LocalQueue, and Cohort metrics.
    // +optional
    // +kubebuilder:validation:MaxItems=8
    CustomLabels []ControllerMetricsCustomLabel `json:"customLabels,omitempty"`
}
```

### Label Name Validation

Each `customLabels` entry is validated at startup:

1. `Name` must match `[a-zA-Z][a-zA-Z0-9_]*` (a valid Prometheus label
   suffix). After prepending `custom_`, the result matches the
   Prometheus label-name regex `[a-zA-Z_][a-zA-Z0-9_]*` (guaranteed by
   construction).
2. `SourceLabelKey` and `SourceAnnotationKey` are mutually exclusive;
   setting both is a **fatal startup error**.
3. `SourceLabelKey` (if set) must be a valid [Kubernetes label
   key][k8s-labels].
4. `SourceAnnotationKey` (if set) must be a valid Kubernetes annotation
   key.
5. If neither source field is set, `Name` must also be a valid
   Kubernetes label key (since it doubles as the source key).
6. No two entries may share the same `Name` (**fatal startup error**
   with a descriptive message listing the duplicates).

Validation runs at startup regardless of whether the
`CustomMetricLabels` feature gate is enabled, so configuration errors
are caught early. When `customLabels` is configured but the feature
gate is disabled, the controller logs a warning indicating that the
configuration will have no effect until the gate is enabled.

| Config `name`  | Source field                                            | Kubernetes source read                    | Prometheus label name  |
|----------------|---------------------------------------------------------|-------------------------------------------|------------------------|
| `team`         | *(omitted)*                                             | label `team`                              | `custom_team`          |
| `cost_center`  | `sourceLabelKey: "cost-center"`                         | label `cost-center`                       | `custom_cost_center`   |
| `app_name`     | `sourceLabelKey: "app.kubernetes.io/name"`              | label `app.kubernetes.io/name`            | `custom_app_name`      |
| `budget_code`  | `sourceAnnotationKey: "billing.company.com/budget"`     | annotation `billing.company.com/budget`   | `custom_budget_code`   |

### Affected Metrics

Custom labels are appended to every metric that is keyed by a specific
Kubernetes object — ClusterQueue, LocalQueue, or Cohort. The selection
criteria:

- **ClusterQueue metrics**: label set includes `cluster_queue`.
  `PreemptedWorkloadsTotal` is also included — its
  `preempting_cluster_queue` label identifies a ClusterQueue, so custom
  label values are read from that ClusterQueue object.
- **LocalQueue metrics**: label set includes `name`/`namespace`.
- **Cohort metrics**: `CohortWeightedShare` (keyed by `cohort`). Custom
  label values are read from the Cohort object's labels or annotations.

Truly global metrics that have no object key are excluded:
`AdmissionAttemptsTotal`, `admissionAttemptDuration`, and `buildInfo`.

The full set of affected metrics is determined by the metric definitions
in `pkg/metrics/metrics.go` at the time of implementation.

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
   through these functions. The recommended approach is to add a
   `[]string` parameter to each `Report*` function for the custom
   label values. For each custom label entry, the source key (`Name`
   when neither source field is set, `SourceLabelKey`, or
   `SourceAnnotationKey`) determines where to read the value — from
   `obj.Labels[key]` or `obj.Annotations[key]` on the relevant object
   (ClusterQueue, LocalQueue, or Cohort). For Cohort metrics
   (`CohortWeightedShare`), labels are read from the Cohort object.
   For `PreemptedWorkloadsTotal`, labels are read from the preempting
   ClusterQueue. Missing keys produce an empty string value.
4. **Deletion cleanup**: Existing `Clear*Metrics` functions use
   `DeletePartialMatch` and will match custom label dimensions
   automatically when a queue is deleted. For Cohort, the
   `CohortWeightedShare` series must also be deleted when a Cohort is
   removed; the Cohort reconciler's delete path must call
   `DeletePartialMatch` for it.
5. **Value-change cleanup**: The controller keeps an in-memory map
   from object name to last-reported custom label values. On every
   reconciliation of a ClusterQueue, LocalQueue, or Cohort:
   1. Read the configured source keys (labels or annotations) from
      the object to get the new values.
   2. Look up the previous values for this object in the map.
   3. If any value differs: remove the old series from every metric
      vector scoped to that object type, then report with the new
      values.
   4. Update the map entry with the new values.

   For Cohort specifically, the Cohort reconciler must perform this
   check before calling `ReportCohortWeightedShare`.

   For cumulative metrics (counters and histograms), removing and
   re-creating a series resets it to zero. This is expected; `rate()`
   and `increase()` handle counter resets correctly.

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

[k8s-labels]: https://kubernetes.io/docs/concepts/overview/working-with-objects/labels/#syntax-and-character-set

### Test Plan

[x] I/we understand the owners of the involved components may require
updates to existing tests to make this code solid enough prior to
committing the changes necessary to implement this enhancement.

#### Unit Tests

- Config validation: invalid `Name` (special characters, leading digit),
  duplicate `Name` values, both `sourceLabelKey` and
  `sourceAnnotationKey` set, invalid `sourceLabelKey`, invalid
  `sourceAnnotationKey`.
- Config with `sourceLabelKey` reads from the correct Kubernetes label.
- Config with `sourceAnnotationKey` reads from the correct annotation.
- Omitting both source fields uses `Name` as the label key source.
- Metric reporting with custom labels; missing labels/annotations
  produce empty values.
- Feature gate disabled: `customLabels` config is ignored, metrics have
  only built-in labels, warning is logged.

#### Integration Tests

- ClusterQueue and LocalQueue with matching labels: custom labels appear
  at `/metrics`.
- Config with `sourceLabelKey`: value is read from the specified
  Kubernetes label.
- Config with `sourceAnnotationKey`: value is read from the specified
  annotation.
- Queue without any of the configured custom labels or annotations:
  empty string values appear in metric series.
- Label value change on a queue: stale series cleaned up, new series
  appears.
- Cohort with custom labels: `CohortWeightedShare` carries the custom
  label values read from the Cohort object.
- `PreemptedWorkloadsTotal` with custom labels: custom label values are
  read from the preempting ClusterQueue.
- Empty `customLabels`: only built-in labels (backward compatibility).
- `CustomMetricLabels` enabled with `LocalQueueMetrics` disabled:
  ClusterQueue metrics get custom labels, LocalQueue metrics are not
  registered.

### Graduation Criteria

**Alpha**

- Feature gate `CustomMetricLabels` (disabled by default).
- Configuration field `customLabels` under `metrics`.
- Custom labels applied to ClusterQueue, LocalQueue, and Cohort metrics.
- LocalQueue custom labels require the `LocalQueueMetrics` gate to also
  be enabled (still alpha); otherwise only ClusterQueue and Cohort
  metrics carry custom labels.
- Startup validation and tests.
- Documentation.

**Beta**

- Positive feedback from users.
- Gate defaults to enabled.
- Re-evaluate whether to support a `SourceKinds` parameter (e.g.,
  `["ClusterQueue", "LocalQueue", "Cohort", "Workload"]`) to let admins
  select which object types carry custom labels.

**GA**

- Address all reported bugs.
- Gate locked on.

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
