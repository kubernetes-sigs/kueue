# KEP-8692: TAS Node Metrics

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1](#story-1)
    - [Story 2](#story-2)
    - [Story 3](#story-3)
    - [Story 4](#story-4)
  - [Notes/Constraints/Caveats](#notesconstraintscaveats)
    - [Cardinality at Scale](#cardinality-at-scale)
    - [Non-TAS Pod Accounting](#non-tas-pod-accounting)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [Feature Gate](#feature-gate)
  - [Metric: kueue_tas_domain_usage](#metric-kueue_tas_domain_usage)
    - [Label Design](#label-design)
    - [TAS Workload Usage](#tas-workload-usage)
    - [Non-TAS Pod Usage](#non-tas-pod-usage)
    - [Topology Level Value Recovery](#topology-level-value-recovery)
    - [Reclaim Correctness Fix](#reclaim-correctness-fix)
  - [API Changes](#api-changes)
  - [Test Plan](#test-plan)
    - [Unit Tests](#unit-tests)
    - [Integration Tests](#integration-tests)
    - [E2E Tests](#e2e-tests)
  - [Graduation Criteria](#graduation-criteria)
    - [Alpha](#alpha)
    - [Beta](#beta)
    - [Stable](#stable)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
<!-- /toc -->

## Summary

This KEP introduces the `kueue_tas_domain_usage` Prometheus metric, which exposes
resource usage per Topology-Aware Scheduling (TAS) topology domain at each level
of the topology hierarchy. The metric covers both admitted TAS workloads and
non-TAS pods co-scheduled onto TAS-flavor nodes. It is gated behind the
`TASNodeMetrics` feature gate (alpha, off by default) and includes a configuration
option to exclude high-cardinality topology levels from the exported time series.

## Motivation

Topology-Aware Scheduling places workloads onto topology domains (e.g. node,
rack, block) to maximize locality and minimize cross-domain traffic. Operators
today have no Prometheus-native view into how much capacity each domain is
consuming. Without this visibility, it is difficult to:

- **Provide an accurate view of reservations.** Kueue reserves quota
  at admission time, before the operator has created any pods. Because pod creation
  can take time, node-level pod metrics undercount real consumption during this
  window. `kueue_tas_domain_usage` reflects Kueue's reservation view, making it a
  more accurate signal of domain utilization than pod-derived metrics alone (ex: from kube state metrics).
  Operators can compute the difference between pending workload resource requests
  and unallocated nominal quota: if that gap is non-zero and persists over time
  (i.e. not a transient spike), it indicates a scheduling issue worth
  investigating—fragmentation, scheduling gates not initiated by Kueue, a bug, or
  another pathological condition.
- Detect topology fragmentation—situations where small scattered allocations
  prevent large locality-sensitive workloads from being admitted.
- Build Grafana time-series dashboards showing utilization trends per block,
  rack, or node over time.
- Understand why TAS scheduling decisions are being made by correlating
  scheduler logs with actual domain utilization.

### Goals

1. Expose a `kueue_tas_domain_usage` Gauge metric reporting resource usage per
   `flavor × topology level × domain value × resource` for admitted TAS workloads.
2. Include non-TAS pod resource usage on nodes belonging to TAS flavor topologies,
   so the metric reflects total observed consumption rather than just TAS-admitted
   usage.
3. Provide a configuration knob (`ExcludedTopologyLevels`) to omit high-cardinality
   topology levels (e.g. `kubernetes.io/hostname`) when cluster size makes
   per-node series prohibitively expensive.
4. Introduce the `TASNodeMetrics` feature gate so operators can opt in explicitly
   after evaluating cardinality for their topology.

### Non-Goals

1. Expose TAS capacity (allocatable resources per domain)
2. Alert rule definitions or Grafana dashboard templates.
3. RBAC changes to restrict metric visibility per tenant.
4. Support for topologies without the `kubernetes.io/hostname` leaf level; the
   hostname level is the anchor used to recover ancestor topology values for
   optimized assignments.

## Proposal

### User Stories

#### Story 1

As a researcher, I want to see how much of each TAS flavor's resources are
currently in use so that I can right-size my workload to fit into available
topology domains. The Kubernetes scheduler's view of node allocations is not
accurate enough for this purpose because Kueue admitted workloads may not yet
have pods running. The operator takes time to create them. So scheduler visible
capacity overstates what is actually free. Which is confusing for a non-infra engineer.

#### Story 2

As a cluster administrator, I want to see per-rack and per-block resource usage
for each TAS flavor so that I can identify fragmented topology domains and
proactively defragment or resize them before large multi-node workloads fail
admission.

#### Story 3

As a platform engineer, I want to observe GPU utilization per topology domain
over time so that I can identify usage trends, spot persistently underutilized
domains, make informed capacity planning decisions, and retroactively debug
topology scheduling issues by reviewing historical domain utilization at the time
a problem occurred.

#### Story 4

As an operator of a large cluster, I want to exclude the `kubernetes.io/hostname`
topology level from metrics because per-node series cardinality would exceed my
Prometheus storage budget, while still retaining rack and block level visibility.

### Notes/Constraints/Caveats

#### Cardinality at Scale

`kueue_tas_domain_usage` emits one time series per unique combination of
`(flavor, topology level label key, domain value, resource)`. At the node/hostname level this grows linearly with cluster size and can
be significant. The `ExcludedTopologyLevels` configuration is specifically designed
to allow operators to opt out of any domain but in particular node-level series while retaining higher-level
aggregate series.

#### Non-TAS Pod Accounting

Nodes belonging to TAS flavors host both TAS-admitted workloads and ordinary
(non-TAS) pods such as DaemonSets, system pods, and workloads from other queues
that happen to land on the same nodes. TAS scheduling decisions are made against
the _actual_ free capacity on nodes, which includes these non-TAS pods. The metric
must account for this usage to give an accurate picture of domain utilization.

The implementation stores pre-computed metric coordinates (flavor, topology level
keys, and domain values) on each non-TAS pod entry at add time. This avoids a live
node-cache lookup at subtract time, ensuring the metric remains correct even when
a node has subsequently left the node cache (e.g. after being marked unschedulable).

### Risks and Mitigations

**Risk**: High cardinality at the node level causes Prometheus OOM or scrape
timeouts in large clusters.

**Mitigation**: The feature gate is off by default. Documentation prominently warns
about cardinality and provides the `ExcludedTopologyLevels` configuration option.
The cardinality calculation is included in user-facing docs so operators can
estimate impact before enabling.

> **Author's note**: The cardinality concern is real at hyperscale (100K+ nodes),
> but the node-level granularity is precisely what makes this metric valuable for
> smaller clusters—seeing per-node usage is the primary use case for researchers
> right-sizing workloads (Story 1). Designing purely around the 100K-node case
> would strip the benefit from the clusters that need it most. The
> `ExcludedTopologyLevels` escape hatch is intentionally opt-in so that large
> clusters can suppress node-level series, while smaller clusters keep full
> granularity by default.

## Design Details

### Feature Gate

A new feature gate `TASNodeMetrics` is added at Alpha stage, defaulting to `false`
as of v0.18:

```go
// Enable Prometheus metrics exposing TAS resource usage and capacity per topology
// domain, enabling Grafana time-series dashboards for topology fragmentation analysis.
// Opt-in due to high label cardinality at scale (one series per flavor × topology level
// × domain × resource; dominated by the node/leaf level).
TASNodeMetrics featuregate.Feature = "TASNodeMetrics"
```

Metrics registration (`RegisterTASMetrics`) is skipped entirely when the gate is
disabled, so there is zero overhead for clusters that do not opt in.

### Metric: kueue_tas_domain_usage

```
# TYPE kueue_tas_domain_usage gauge
kueue_tas_domain_usage{flavor="gpu-flavor",domain="kubernetes.io/hostname",domain_id="node-1",resource="nvidia.com/gpu"} 8
kueue_tas_domain_usage{flavor="gpu-flavor",domain="topology.kubernetes.io/rack",domain_id="rack-a",resource="nvidia.com/gpu"} 16
kueue_tas_domain_usage{flavor="gpu-flavor",domain="topology.kubernetes.io/block",domain_id="block-1",resource="nvidia.com/gpu"} 32
```

#### Label Design

| Label | Value |
|---|---|
| `flavor` | ResourceFlavor name (e.g. `gpu-flavor`) |
| `domain` | Topology level label key (e.g. `kubernetes.io/hostname`) |
| `domain_id` | Value of that label on the matched domain |
| `resource` | Kubernetes resource name (e.g. `nvidia.com/gpu`, `cpu`, `memory`) |

`corev1.ResourcePods` is excluded because it is an internal Kueue tracking resource
and has no meaningful utilization semantics for operators.

#### TAS Workload Usage

When an admitted workload uses TAS, its `TopologyAssignment` stores domain values
at the lowest topology level stored by the assignment (see the optimization noted
in [Topology Level Value Recovery](#topology-level-value-recovery)). Usage is
emitted via `AddTASDomainUsage` / `SubTASDomainUsage` at every topology level for
each domain in the assignment, so a single admitted pod contributes to all ancestor
levels (node, rack, block, etc.).

The call sites are in `clusterqueue.go`:

- `addTASFlavorUsage`: called when a workload is admitted, records usage in the
  flavor cache and emits metric increments.
- `removeTASFlavorUsage`: called when a workload is evicted or finishes, removes
  usage from the flavor cache and emits metric decrements.

Both functions delegate metric emission to `emitTASDomainUsage`, which is a no-op
when the feature gate is disabled.

#### Non-TAS Pod Usage

Non-TAS pods on TAS-flavor nodes are tracked in `nonTasUsageCache`. When a pod is
added to the cache, the node's labels are resolved against all flavor caches to
compute `podMetricCoord` entries (one per matching flavor):

```go
type podMetricCoord struct {
    flavorName kueue.ResourceFlavorReference
    levels     []string  // topology level label keys
    values     []string  // domain values at pod add time
}
```

These coordinates are stored in `podUsageValue` and are preserved across pod
updates (re-adds to the cache). At subtract time (pod termination or deletion),
the stored coordinates are used directly without re-reading the node cache. This
ensures correctness even after the node has departed the cache.

#### Topology Level Value Recovery

TAS assignment optimization: when `kubernetes.io/hostname` is the lowest level,
`buildAssignment` stores only the hostname in `TopologyAssignment.DomainRequests`,
omitting ancestor levels (block, rack). This keeps the assignment compact but means
the full value set for ancestor levels must be recovered before emitting metrics.

`tasDomainFullLevelValues` handles this:

1. If `len(values) == len(allLevels)`, all levels are already present — return unchanged.
2. Otherwise, find `kubernetes.io/hostname` in the stored (suffix) levels to get a
   hostname value, look up that node in the live node cache, and return
   `LevelValues(allLevels, node.Labels)` — the complete label values for all levels.
3. If the hostname is not among the stored levels (topology has no hostname level),
   return unchanged.

### API Changes

A new `TASMetrics` struct is added to `ControllerMetrics` in the v1beta2
configuration API:

```go
// ControllerMetrics defines the metrics configs.
type ControllerMetrics struct {
    // ... existing fields ...

    // TASMetrics configures the TAS domain usage metrics.
    // Only takes effect when the TASNodeMetrics feature gate is enabled.
    // +optional
    TASMetrics *TASMetrics `json:"tasMetrics,omitempty"`
}

// TASMetrics defines configuration options for TAS domain usage metrics.
type TASMetrics struct {
    // ExcludedTopologyLevels lists topology level label keys to exclude from
    // kueue_tas_domain_usage metrics (e.g. "kubernetes.io/hostname" to avoid
    // high-cardinality node-level series in large clusters).
    // +optional
    // +listType=set
    ExcludedTopologyLevels []string `json:"excludedTopologyLevels,omitempty"`
}
```

Example configuration to suppress node-level series:

```yaml
metrics:
  tasMetrics:
    excludedTopologyLevels:
    - kubernetes.io/hostname
```

`InitTASMetricsConfig` must be called before `Register()` to apply this
configuration. The conversion webhook propagates the new field from v1beta1 to
v1beta2 with a zero value (no exclusions) when upgrading from older configurations.

### Test Plan

[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes
necessary to implement this enhancement.

#### Unit Tests

- `pkg/metrics/`: `tas_metrics.go` covered by `metrics_test.go` — verifies metric
  registration, `AddTASDomainUsage`/`SubTASDomainUsage` increment/decrement
  behavior, `ClearTASFlavorUsage` cleanup, and `ExcludedTopologyLevels` filtering.
- `pkg/cache/scheduler/`: `tas_cache_test.go` — verifies that `Update` and
  `DeletePodByKey` emit the correct metric deltas for non-TAS pods, including the
  case where the node has left the cache at subtract time.
- `pkg/cache/scheduler/`: `clusterqueue_test.go` — verifies `emitTASDomainUsage`
  for TAS workload add/remove including ancestor level recovery via
  `tasDomainFullLevelValues`.

#### Integration Tests

`test/integration/singlecluster/tas/tas_test.go` covers:

1. After admitting a TAS workload, `kueue_tas_domain_usage` reflects the correct
   per-domain, per-resource quantities at all topology levels.
2. After the workload finishes, the metric returns to zero for all its domains.
3. Non-TAS pods on a TAS-flavor node are included in the domain usage total.
4. `ExcludedTopologyLevels` suppresses the specified level's series while leaving
   other levels intact.
5. Deleting a ResourceFlavor clears all metric series for that flavor.

#### E2E Tests

`test/e2e/singlecluster/tas_test.go` verifies end-to-end that:

1. The metric is absent when `TASNodeMetrics` is disabled.
2. After enabling the feature gate and admitting a TAS workload, the metric appears
   with expected label values.
3. After the workload completes, usage drops to zero.

### Graduation Criteria

#### Alpha

- Feature gate `TASNodeMetrics` introduced at Alpha (v0.18), off by default.
- `kueue_tas_domain_usage` metric implemented for TAS workload usage and non-TAS
  pod usage.
- `ExcludedTopologyLevels` configuration option available.
- Unit, integration, and e2e test coverage.
- Documentation in `site/content/en/docs/reference/metrics.md` with cardinality
  warning and configuration example.

#### Beta

- `TASMetrics` configuration API stabilized — no further breaking changes to
  `ExcludedTopologyLevels` or the `tasMetrics` config stanza.
- Cardinality impact validated on representative clusters across a range of sizes;
  scrape latency and series count documented as a reference for operators.
- Correctness validated across multiple Kueue release cycles with no open bugs
  against the metric values (including reclaim, eviction, and flavor deletion paths).
- Feedback from alpha adopters incorporated (additional exclusion options, label
  naming, interaction with other TAS features, etc.).
- Feature gate remains opt-in; enabling by default is not appropriate given that
  cardinality impact is inherently cluster-size-dependent and operators must
  evaluate the cost for their topology before enabling.

#### Stable

- No known correctness issues or performance regressions across multiple beta
  release cycles.
- `TASMetrics` API frozen.
- Feature gate remains opt-in permanently. Unlike most feature gates, this one
  controls a resource cost (Prometheus series) that scales with cluster topology
  and cannot be safely defaulted to enabled for all cluster sizes.

## Implementation History

- 2026-04-27: Initial implementation — `TASNodeMetrics` feature gate, `kueue_tas_domain_usage` metric for TAS workloads, node-level metrics infrastructure.
- 2026-05-01: Added `ExcludedTopologyLevels` configuration, updated metrics documentation, added `TASMetrics` API type and conversion.
- 2026-05-01: Added non-TAS pod usage tracking in `nonTasUsageCache` with pre-computed metric coordinates.

## Drawbacks

- **Cardinality**: At the node level, the number of time series scales with
  cluster size. This is an inherent tradeoff of per-domain visibility; the
  `ExcludedTopologyLevels` option and opt-in feature gate are the primary
  mitigations.
- **Non-TAS pod coordinate storage**: Storing `podMetricCoord` on every cached
  pod entry increases memory usage proportional to the number of non-TAS pods
  on TAS-flavor nodes. For clusters with large numbers of DaemonSet or system
  pods, this overhead can be non-trivial.
