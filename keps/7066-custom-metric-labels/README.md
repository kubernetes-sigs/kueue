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
  - [In-memory label value caching for cleanup](#in-memory-label-value-caching-for-cleanup)
  - [Label Name Validation](#label-name-validation)
  - [Workload labels and annotations](#workload-labels-and-annotations)
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
  - [Tracking custom workload label value combinations](#tracking-custom-workload-label-value-combinations)
  - [Single Static Label](#single-static-label)
  - [Numbered Static Labels](#numbered-static-labels)
  - [Configurable with Override Name](#configurable-with-override-name)
  - [Soft Source Kind Priority](#soft-source-kind-priority)
  - [List of source kinds](#list-of-source-kinds)
    - [Append source kind to the metric label name](#append-source-kind-to-the-metric-label-name)
    - [Hard Source Kind Priority](#hard-source-kind-priority)
    - [Skip incompatible Metrics/Labels/Entries](#skip-incompatible-metricslabelsentries)
<!-- /toc -->

## Summary

Allow cluster admins to configure Kueue to promote specific Kubernetes
labels or annotations from ClusterQueue, LocalQueue, Cohort, and Workload objects
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
  LocalQueue, Cohort, and Workload (inherited from their corresponding GenericJobs) objects become Prometheus metric labels.
- Keep the metrics documentation auto-generation working.
- Validate configuration at startup.

### Non-Goals

- Runtime-dynamic label set changes (requires restart).

## Proposal

Add a `customLabels` list to the `metrics` config section. Each entry
has a `name` (used as the Prometheus label suffix after prepending the `custom_` prefix),
a `sourceKind` (default: `ClusterQueue`) denoting the kind of object the label's value will be sourced from,
and optionally a `sourceLabelKey` or `sourceAnnotationKey`
to specify where to read the value from.
Available kinds include: `Workload`, `ClusterQueue`, `LocalQueue`, and `Cohort`.

At startup, Kueue validates the configuration and initializes metric vectors
with the additional label dimensions.
When reporting metrics, the corresponding Kubernetes label or annotation
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

**Admitted Active Workloads grouping by workload type**

Custom labels configuration of:
```yaml
  customLabels:
    - name: "team"
    - name: "workload_type"
      sourceKind: "Workload"
      sourceLabelKey: "company.com/workload-type"
      trackedValues: ["inference", "training"]
```
resulting in the `kueue_admitted_active_workloads` metric
with a breakdown of workload counts by workload type.

Workloads spanning types: inference (10x), training (20x),
batch_inference (3x), each active and admitted to
ClusterQueue `name=cq-1` (with label `team=platform`) produce:

```
kueue_admitted_active_workloads{
  cluster_queue="cq-1",
  ...,
  custom_team="platform",
  custom_workload_type="inference",
} 10
kueue_admitted_active_workloads{
  cluster_queue="cq-1",
  ...,
  custom_team="platform",
  custom_workload_type="training",
} 20
kueue_admitted_active_workloads{
  cluster_queue="cq-1",
  ...,
  custom_team="platform",
  custom_workload_type="kueue.x-k8s.io/_UNTRACKED_VALUE_",
} 3
```

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
    - name: "workload_type"
      sourceLabelKey: "company.com/workload-type"         # reads from label "company.com/workload-type"
      sourceKind: "Workload"
      trackedValues: ["inference", "training"]            # workloads with any other value will be aggregated under "kueue.x-k8s.io/_UNTRACKED_VALUE_"
```

Resulting metric:

```
kueue_admitted_active_workloads{
  cluster_queue="cq-1", ...
  custom_team="platform", custom_env="prod",
  custom_cost_center="12345",
  custom_budget_code="ABC-123",
  custom_workload_type="inference"} 10
```

### Risks and Mitigations

**High metrics cardinality.** Introduction of custom metric labels naturally
increases the metric cardinality as it increases the granularity of the metrics.

For example, the metric `kueue_admitted_active_workloads` currently reports at most
one series per ClusterQueue. Defining custom labels for the `Workload` `SourceKind`
will cause existing series to be split into multiple composite ones.

Increased metric cardinality can cause downstream problems such as:
1. Increased memory usage (leading to OOMKill).
2. Increased storage (and consequently - cost).
3. Dashboard and UI performance degradation when handling large responses from Prometheus.

We add a documentation note to advise admins to define labels with limited cardinality counts (e.g.
`team`, `environment`, or `cost_center`) and be mindful of the impact of
adding custom labels on each connected metric, especially for metrics that are
aggregated over a large number of objects.

**High memory consumption due to internal structures.** In order to be able to
perform cleanup of the metrics when the labels are no longer used, Kueue tracks
the custom label values of recorded objects in-memory
(see [In-memory label value caching for cleanup](#in-memory-label-value-caching-for-cleanup)).

To counter the potential high memory usage caused by the custom labels, the number
of custom labels that can be defined for each `SourceKind` is limited:
- 2 for Workloads,
- 6 for other SourceKinds.

These limits are enforced by validation.

**Memory leak for no-longer-used custom label values.** For the `Workload` `SourceKind`,
a naive implementation could leak memory via gauges, when the last object
for a given series is deleted (or re-labeled). When the set of label
values is fixed, this issue is negligible. It could however be exploitable
for custom labels with unconstrained value space. For example, malicious
users could submit workloads with unique label values en masse, leading to
unbounded growth of the metrics series set.

To counter that, we require Workload custom labels to be defined with
the `trackedValues` list. This limits the amount of zero-value-series a gauge
can be polluted with to the `trackedValues` list size limit.
With currently defined constraints, the maximum amount of zero-value-series,
per unique combination of metric labels other than the defined
custom-workload-labels, is limited to `13^2 = 169`
(max 2 labels with max 12 tracked values + the untracked value).

Another mechanism to counter this issue is proposed in the alternatives section:
[Tracking custom workload label value combinations](#tracking-custom-workload-label-value-combinations)

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
type SourceKind string

const (
	SourceKindCohort       SourceKind = "Cohort"
	SourceKindLocalQueue   SourceKind = "LocalQueue"
	SourceKindClusterQueue SourceKind = "ClusterQueue"
	SourceKindWorkload     SourceKind = "Workload"
)

const UntrackedCustomLabelValue = "kueue.x-k8s.io/_UNTRACKED_VALUE_"

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

    // SourceKind is the object kind from which the label value should be sourced.
    // Up to 2 labels are allowed for Workloads and up to 6 for other source kinds.
    // Defaults to ClusterQueue.
    // The possible values are:
    // - Cohort
    // - LocalQueue
    // - ClusterQueue
    // - Workloads
    // +optional
    SourceKind *SourceKind `json:"sourceKind,omitempty"`

    // TrackedValues is a list of the label's allowed values.
    // When SourceKind is Workload, a closed list of 1-12 TrackedValues is required.
    // Non-workload source kinds can have 0-16 TrackedValues. If the list is empty, any value is allowed.
    // Label values not allowed by this field will be reported as "kueue.x-k8s.io/_UNTRACKED_VALUE_".
    // +optional
    TrackedValues []string `json:"trackedValues,omitempty"`
}

type ControllerMetrics struct {
    ...
    // CustomLabels is a list of entries whose values will be added as extra
    // Prometheus labels on supported metrics.
    // A maximum of 6 labels are allowed per SourceKind (2 for Workloads), with up to 20 labels defined in total.
    // +optional
    // +kubebuilder:validation:MaxItems=20
    CustomLabels []ControllerMetricsCustomLabel `json:"customLabels,omitempty"`
}
```

### In-memory label value caching for cleanup

In order to facilitate cleanup of metric series,
for example when a LocalQueue is deleted or when a custom label value changes,
Kueue tracks the last set of custom label values for each relevant object
with an in-memory cache scoped by `SourceKind`:

`SourceKind -> ObjectKey -> CustomLabelValues`

ObjectKey is the stable key of the object,
for example the workload name.
CustomLabelValues contains the last-reported custom label values for that object,
for example `{ workload_type: "Inference" }`.

Note that:
1. The size of this structure is proportional to the number of tracked objects
   multiplied by the number of custom metric labels.
2. Since the number of custom labels is bounded by validation, the cache scales
   linearly with the number of tracked objects.
3. The structure is kept in memory once (not per metric).

As a result the structure does not increase the asymptotic memory usage,
because all the object instances are already tracked by the
controller-runtime cache; it only adds bounded per-object overhead for the
cached label values.

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
are caught early. When `customLabels` are configured but the feature
gate is disabled, the controller logs a warning indicating that the
configuration will have no effect until the gate is enabled.

| Config `name`  | Source field                                            | Kubernetes source read                    | Prometheus label name  |
|----------------|---------------------------------------------------------|-------------------------------------------|------------------------|
| `team`         | *(omitted)*                                             | label `team`                              | `custom_team`          |
| `cost_center`  | `sourceLabelKey: "cost-center"`                         | label `cost-center`                       | `custom_cost_center`   |
| `app_name`     | `sourceLabelKey: "app.kubernetes.io/name"`              | label `app.kubernetes.io/name`            | `custom_app_name`      |
| `budget_code`  | `sourceAnnotationKey: "billing.company.com/budget"`     | annotation `billing.company.com/budget`   | `custom_budget_code`   |

### Workload labels and annotations

Upon creation, all labels and annotations specified
in the custom label config will be copied to the Workload
from its corresponding GenericJob.
These will be added alongside the labels specified in
`LabelKeysToCopy`.

Similar to copied labels, component jobs of a ComposableJob
(e.g. PodGroup) must match label and annotation values
marked as sources for custom metric labels.

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

Some metrics will support sourcing label values from multiple object kinds.
For example, `kueue_admitted_active_workloads` will provide a grouping
not only by ClusterQueue labels, but also by Workload labels,
if specified in the config with `sourceKind: "Workload"`.

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

   Metrics are initialized only with labels that match their `SourceKind`,
   as configured on a metric-by-metric basis.
   For example, metrics that do not support workload-sourced
   labels will ignore any label with `sourceKind: "Workload"`.

3. **Reporting**: All `Report*` functions currently use positional
   `WithLabelValues(...)` and accept primitive parameters (strings,
   ints), not queue objects. Custom label values must be threaded
   through these functions. The recommended approach is to add a
   `[]string` parameter to each `Report*` function for the custom
   label values. For each custom label entry, the source key (`Name`
   when neither source field is set, `SourceLabelKey`, or
   `SourceAnnotationKey`) determines where to read the value — from
   `obj.Labels[key]` or `obj.Annotations[key]` on the relevant object
   (ClusterQueue, LocalQueue, Cohort or Workload). For Cohort metrics
   (`CohortWeightedShare`), labels are read from the Cohort object.
   For `PreemptedWorkloadsTotal`, labels are read from the preempting
   ClusterQueue. Missing keys produce an empty string value.
   For metrics aggregating workloads (e.g., counting the number
   of active, admitted workloads), the gauges must be updated
   (both incremented and decremented) on individual workload-scoped
   events.
   
4. **Deletion cleanup**:
   For metrics that have series with a 1:N object-to-series correspondence with
   the deleted object (where each series corresponds to exactly one object),
   use `DeletePartialMatch` with the metric's label set (including custom
   labels) when an object is deleted.
   For Cohort, the `CohortWeightedShare` series must also be deleted
   when a Cohort is removed; the Cohort reconciler's delete path must
   call `DeletePartialMatch` for it.
   Depending on how the series of the metric correspond to objects of a
   given SourceKind:
   - if a series represents the deleted object (1:N object-to-series
     correspondence; the series cannot represent any other object of the
     given SourceKind during its lifetime), the series must be deleted.
     This applies to ClusterQueue, LocalQueue and Cohort objects
     for all metrics implemented as of the time of writing this KEP.
   - if a series represents multiple objects:
     - for gauges: the series is preserved and its value decremented
       accordingly,
     - for other metrics: the series should remain intact.

   If the label and tracked values constraints are relaxed in the future,
   a zero-value series cleanup mechanism may be added. For details:
   see [Tracking custom workload label value combinations](#tracking-custom-workload-label-value-combinations).

5. **Value-change cleanup**: The controller keeps an in-memory map
   from object name to last-reported custom label values. On every
   reconciliation of a ClusterQueue, LocalQueue, Cohort or Workload:
   1. Read the configured source keys (labels or annotations) from
      the object to get the new values.
   2. Look up the previous values for this object in the map.
   3. If any value differs: perform deletion cleanup of all old series
      from every metric vector scoped to that object type (following
      the rules outlined in pt. 4), then report with the new values.
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
- Metrics implementing Workload-level aggregations provide correct data breakdowns.

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
- Re-evaluate implementing the [Tracking custom workload label value combinations](#tracking-custom-workload-label-value-combinations).

**GA**

- Address all reported bugs.
- Gate locked on.

## Implementation History

- 2026-02-13: Initial KEP draft.
- 2026-07-02: Added source kind specification parameter to label definition (incompatible with previous alpha feature proposal). Expanded support to include Workload labels.

## Drawbacks

- Adds configuration complexity (mitigated: opt-in behind a feature gate,
  empty default).
- Adds extra label dimensions to queue metrics, slightly increasing
  per-series storage.

## Alternatives

### Tracking custom workload label value combinations

To address the issue of cleaning up zero-valued series in gauge metrics
tracking the `Workload` `SourceKind`, Kueue tracks the counts of observed custom
label value combinations across all recorded `Workload` objects.

`CustomLabelValues -> Int`

This structure is used to recognize when the last workload with a specific
combination of LabelValues has been deleted. When detected,
the metric series corresponding to that combination of values are deleted.

This approach could also be used for cumulative metrics, albeit with a caveat.
For cumulative metrics, deleting series aggregating data from multiple objects
of a single SourceKind between Prometheus scrapes can lead to data loss.

For example: imagine a metric that counts the number of workloads created for a
cluster queue. The metric uses the CQ.name as a core label and has the custom
workload label "workload_type" defined. In the following scenario:

1. Prometheus scrapes the metric, recording a series of
   `[cluster_queue="q1", workload_type="batch"] = 10`
2. 5 workloads of type "batch" are created for cluster queue "q1".
   `[cluster_queue="q1", workload_type="batch"] = 15`
3. All "batch" workloads are deleted. **We clear the series.**
4. Another 5 "batch" workloads appear. The series is recreated as
   `[cluster_queue="q1", workload_type="batch"] = 5`
5. The next scrape happens, the metric values are compared: old = 10, new = 5.
   Prometheus assumes a reset and calculates the increase of 5.
6. We end up with a reported increase of 5, instead of 10.

An example way to overcome this problem, is to introduce a timeout to the
deletion, deleting a series only if it remains at zero for a specific amount of
time (preferably the interval between consecutive Prometheus metric scrapes).

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

### Soft Source Kind Priority

All label values will be derived at Report time for all metrics.

If two distinct sources contribute the value for the same label, record the
value based on priority. This means that if a higher priority source does not
have a specific label, the value will be retrieved from the lower priority
source.

This variant allows us to simplify the API by removing the `sourceKinds` field,
but suffers the same downsides as the
[Hard Source Kind Priority](#hard-source-kind-priority) variant.

The order could be hard-coded or provided by the user as a separate config
parameter.

In addition, this variant complicates the update logic for label sets on objects.
Depending on the combination of sources for a metric, we could become unable to
use partial matching of label sets, as some values could have been overridden.

This makes it significantly harder to perform updates triggered by Lower Priority
Objects, if the exact set of labels of the related Higher Priority Objects is not
known. Conversely, it makes it significantly harder to create new metrics using custom
labels depending on the proposed ordering of the priorities list, especially if
it were made to be configurable.

Another issue is that this variant also abstracts away the label conflicts we
encounter, silencing them instead of vocalizing. Introducing any ways of
disambiguating what the conflicts are specifically would still require adding
the `sourceKinds` field, defeating the major advantage of this proposal.

### List of source kinds

`sourceKind` parameter is a list rather than a single value.

This variant would potentially reduce the size of the config,
in case users want to utilize the same labels on multiple different kinds.

The downside here is name collisions.
Any metrics that utilize multiple sources will have to
ensure a way to differentiate label instances based
on the source of their value.
Addressing the matter will incur the cost of either of:
* increased complexity of the internal logic,
leading to the solution being more error prone,
* polluting the metric label names with expanded names,
to include the distinction in label identity based on value source.

#### Append source kind to the metric label name
This is a clean way of resolving conflicts - but it makes metric label names inflated.

#### Hard Source Kind Priority

Establish the priority of the supported source kinds as:
Cohort > ClusterQueue > LocalQueue > Workload.
Alternatively, source kind priority could also be:
* a configurable parameter,
* defined as the explicit order in which the `sourceKinds` are listed
in the label definition (with the first entry having the highest priority).

In metrics where multiple source kinds could potentially contribute values,
the value from the highest priority source kind will be used.
For example, if a custom label is defined for both ClusterQueue and Workload,
then the metrics which gather label values from both CQ and WL,
will **always get the value from the CQ** (the value will be an empty string,
if the label is not defined on the CQ).

Allowing this makes it harder for the users to
introduce labels that have the same names and configs
on different source kinds, while being allowed to have
different values for said labels. To accomplish this, users
still have to provide additional labels with different names,
defeating one of the main advantages of using a list of source
kinds instead of a single value.

Moreover, this approach requires creating an internal representation of
metric labels identified by both name and source kind, to keep track of
which value came from which source.
Since source kind is not part of the metric series,
this complicates the internal logic and could lead
to internal cache bloat.

#### Skip incompatible Metrics/Labels/Entries

Metrics for which the `sourceKinds` list defines a set of sources introducing a label value source conflict:
* [A] can be omitted entirely,
* [B] can ignore labels that are misconfigured,
* [C] can ignore entries where multiple sources declare the value for the same label,
* [D] can set the value of a label with multiple sources as "CORRUPTED",
* [E] could be wiped out entirely when a conflict is detected.

The main downside of [A] is that when updating existing metrics to use multiple
sources, the change will make existing configs invalid (not backwards
compatible).

The main downside of [B] is the potential to cause confusion among the users
by removing some of the labels they explicitly defined.

Options [C] - [E] are more complex to implement as they assume that the value of
a label can have a different source depending on circumstances.

This would require adding independent tracking of the source of the value when
cached, to be able to determine how to handle it upon the deletion/update of one
or more entries (e.g., how to identify which entries match a given ClusterQueue
when some label values may have been sourced from a Workload).

In addition, these options would result in metrics missing data or providing
confusing results, which would make them harder to debug and use.
