# KEP-10587: Configurable Node Label Prefix Filtering for TAS Cache

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1 – Large-scale Azure/GCP clusters](#story-1--large-scale-azuregcp-clusters)
    - [Story 2 – Multi-cloud operator standardization](#story-2--multi-cloud-operator-standardization)
  - [Notes/Constraints/Caveats](#notesconstraintscaveats)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [API Changes](#api-changes)
  - [Defaults](#defaults)
  - [Filtering Implementation](#filtering-implementation)
  - [Test Plan](#test-plan)
    - [Prerequisite testing updates](#prerequisite-testing-updates)
    - [Unit tests](#unit-tests)
    - [Integration tests](#integration-tests)
    - [e2e tests](#e2e-tests)
  - [Graduation Criteria](#graduation-criteria)
    - [Alpha](#alpha)
    - [Beta](#beta)
    - [GA](#ga)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
  - [Allowlist instead of denylist](#allowlist-instead-of-denylist)
  - [Regex-based filtering](#regex-based-filtering)
  - [Per-ClusterQueue configuration](#per-clusterqueue-configuration)
<!-- /toc -->

## Summary

This KEP adds a `Resources.ExcludeNodeLabelPrefixes` configuration field to
Kueue's `Configuration` API (`v1beta2`). When Topology Aware Scheduling (TAS)
caches node objects, labels whose keys match any of the configured prefixes are
stripped before storage. This reduces per-node memory in the TAS cache without
affecting scheduling correctness, because the excluded labels are infrastructure
metadata that Kueue never uses for topology levels, flavor node selectors, or
workload scheduling decisions.

## Motivation

In large clusters (hundreds to thousands of nodes), each node can carry 30–80+
labels injected by cloud providers and infrastructure controllers. Examples
include `kubectl.kubernetes.io/`, `cloud.google.com/`, `eks.amazonaws.com/`,
and `node.cluster.x-k8s.io/` prefixes. The TAS node cache stores all labels on
every node, even though Kueue only inspects a small subset for topology,
flavor, and workload scheduling purposes.

At scale, these unnecessary labels become a measurable memory overhead. In
Singularity staging clusters running ~3,600 Workloads and ~100 nodes, the
Kueue controller-manager RSS was ~202 MB at baseline. Combined with a
complementary Workload cache optimization (stripping non-scheduling PodTemplateSpec
fields), excluding infrastructure node labels reduced RSS to ~179 MB—a 9.3%
reduction.

This is analogous to the existing `Resources.ExcludeResourcePrefixes` field,
which strips irrelevant resource types from quota calculations. The same
pattern—a denylist of key prefixes with sensible defaults—is applied here to
node labels.

### Goals

* Provide a `Resources.ExcludeNodeLabelPrefixes` configuration field that
  controls which node label prefixes are stripped from the TAS node cache.
* Ship a default set of common infrastructure label prefixes so that
  operators benefit out of the box without configuration.
* Reduce per-node memory usage in the TAS cache proportionally to the
  number of excluded labels.
* Maintain full backward compatibility: an empty or nil value falls back
  to the default prefix list; an explicit empty list (`[]`) disables
  filtering entirely.

### Non-Goals

* Filtering labels from node objects outside the TAS cache (e.g., in the
  Kubernetes API server or other Kueue caches).
* Allowlist-based filtering (only keep certain prefixes). This could be a
  future enhancement if needed.
* Filtering annotations or taints from cached nodes.
* Dynamically reloading the prefix list without restarting the controller.

## Proposal

Add a new field `ExcludeNodeLabelPrefixes` to the `Resources` struct in
`apis/config/v1beta2/configuration_types.go`. When set (or defaulted), the
TAS node cache strips matching labels at ingestion time—inside the
`newNodeInfo()` constructor that converts a `*corev1.Node` to the internal
`nodeInfo` representation.

### User Stories

#### Story 1 – Large-scale Azure/GCP clusters

An operator runs Kueue with TAS enabled on a 500-node AKS cluster. Each node
has ~60 labels, of which only 5–8 are used for topology levels and flavors.
With the default `ExcludeNodeLabelPrefixes`, approximately 20 labels per node
are stripped, saving ~40 KB of string data across the cluster in the TAS cache.
No configuration is required—the defaults cover common Azure and GCP
infrastructure labels.

#### Story 2 – Multi-cloud operator standardization

An operator manages Kueue across AWS EKS, GCP GKE, and on-prem clusters. Each
environment injects different infrastructure labels. The operator customizes
`ExcludeNodeLabelPrefixes` per environment to strip cloud-specific labels while
retaining topology labels used for their scheduling policies. On the on-prem
cluster where there are no cloud labels to strip, they set `[]` to disable
filtering.

### Notes/Constraints/Caveats

* **Ordering**: Prefix matching uses `strings.HasPrefix` for each label key
  against each prefix in the list. The list is unordered; the first match wins.
  For most practical configurations (<20 prefixes), linear scan is efficient.
* **Interaction with topology levels**: Operators must ensure they do not
  exclude label prefixes used in their `TopologySpec.levels[].nodeLabel`
  definitions or flavor `nodeLabels`. Kueue will not validate this
  cross-reference automatically in the alpha stage.
* **Immutability during runtime**: The prefix list is read at controller
  startup. Changing it requires a restart. Existing cached nodes are not
  retroactively re-filtered.

### Risks and Mitigations

| Risk | Mitigation |
|------|------------|
| Operator accidentally excludes a prefix used for topology levels, causing TAS scheduling failures | Document clearly that topology-relevant labels must not be excluded. In beta, add a startup validation warning that cross-references configured topology levels against excluded prefixes. |
| Default prefix list is too aggressive for some environments | Defaults are limited to well-known infrastructure prefixes (`kubectl.kubernetes.io/`, cloud-provider-specific). Operators can override with `[]` to disable. |
| Performance regression from prefix scanning on hot path | `newNodeInfo()` is called once per node watch event, not per scheduling cycle. Linear prefix scan on <20 prefixes is negligible. |

## Design Details

### API Changes

Add the following field to the `Resources` struct in
`apis/config/v1beta2/configuration_types.go`:

```go
// ExcludeNodeLabelPrefixes lists label key prefixes that should be
// stripped from cached node objects in the Topology Aware Scheduling
// (TAS) node cache to reduce memory usage. Any node label whose key
// starts with one of these prefixes is dropped when the node is
// stored in the TAS cache. Labels needed for topology levels, flavor
// node selectors, workload node selectors, and node affinity should
// NOT be listed here.
// Defaults to a set of common infrastructure labels that are not
// relevant for scheduling decisions (see defaults.go).
// +optional
ExcludeNodeLabelPrefixes []string `json:"excludeNodeLabelPrefixes,omitempty"`
```

### Defaults

In `apis/config/v1beta2/defaults.go`, define:

```go
var DefaultExcludeNodeLabelPrefixes = []string{
    "kubectl.kubernetes.io/",
    "node.kubernetes.io/exclude-from-external-load-balancers",
    "node-role.kubernetes.io/",
    "cloud.google.com/",
    "eks.amazonaws.com/",
    "topology.ebs.csi.aws.com/",
    "node.cluster.x-k8s.io/",
    "container.googleapis.com/",
}
```

The defaulting logic in `SetDefaults_Configuration` applies this list when
`cfg.Resources.ExcludeNodeLabelPrefixes` is `nil`. An explicit empty slice
(`[]`) disables filtering.

### Filtering Implementation

In `pkg/cache/scheduler/tas_flavor.go`, modify `newNodeInfo()` to accept
the prefix list and filter labels before caching:

```go
func newNodeInfo(node *corev1.Node, excludePrefixes []string) *nodeInfo {
    labels := node.Labels
    if len(excludePrefixes) > 0 && len(labels) > 0 {
        labels = filterLabelsByPrefix(labels, excludePrefixes)
    }
    return &nodeInfo{
        Name:        node.Name,
        Labels:      labels,
        Taints:      node.Spec.Taints,
        Allocatable: node.Status.Allocatable,
    }
}

func filterLabelsByPrefix(src map[string]string, prefixes []string) map[string]string {
    result := make(map[string]string, len(src))
    for k, v := range src {
        if !hasAnyPrefix(k, prefixes) {
            result[k] = v
        }
    }
    return result
}

func hasAnyPrefix(s string, prefixes []string) bool {
    for _, p := range prefixes {
        if strings.HasPrefix(s, p) {
            return true
        }
    }
    return false
}
```

The `excludePrefixes` value is threaded from the `Configuration` object through
the TAS cache constructor at startup.

### Test Plan

[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes
necessary to implement this enhancement.

#### Prerequisite testing updates

Existing TAS cache unit tests cover `newNodeInfo()` and node label propagation.
These must be verified to pass before and after the change.

#### Unit tests

* `pkg/cache/scheduler`: Test `filterLabelsByPrefix` with empty prefixes,
  matching prefixes, non-matching prefixes, empty labels map.
* `pkg/cache/scheduler`: Test `newNodeInfo` with and without prefix filtering,
  verifying that topology-relevant labels are preserved.
* `apis/config/v1beta2`: Test `SetDefaults_Configuration` applies defaults when
  `ExcludeNodeLabelPrefixes` is nil and preserves explicit empty slice.

Core packages and current coverage:
- `pkg/cache/scheduler`: 2026-04-17 - TBD
- `apis/config/v1beta2`: 2026-04-17 - TBD

#### Integration tests

* TAS scheduling integration test with `ExcludeNodeLabelPrefixes` set to
  exclude labels that are *not* used for topology, verifying scheduling
  still works correctly.
* TAS scheduling integration test with default prefixes on nodes carrying
  infrastructure labels, verifying those labels are absent from the internal
  cache but scheduling succeeds.

#### e2e tests

Not required at alpha. At beta, add an e2e test verifying that a TAS-enabled
cluster with the default prefix list can schedule workloads to nodes carrying
infrastructure labels.

### Graduation Criteria

#### Alpha

* Feature gate `ExcludeNodeLabelPrefixes` defaults to disabled.
* `Resources.ExcludeNodeLabelPrefixes` field added to `Configuration` API.
* Default prefix list defined.
* Filtering implemented in TAS node cache.
* Unit tests covering filtering logic and defaulting.

#### Beta

* Feature gate defaults to enabled.
* Startup validation warning when excluded prefixes overlap with configured
  topology level `nodeLabel` values.
* Integration tests demonstrating correct TAS scheduling with filtering.
* Documentation of the field in the Kueue configuration reference.

#### GA

* Feature gate locked to enabled and removed.
* e2e test coverage.
* At least two releases with no reported issues from beta users.

## Implementation History

* 2026-04-17: KEP drafted
* 2026-04-17: PR [#10587](https://github.com/kubernetes-sigs/kueue/pull/10587)
  submitted with initial implementation (combined with Workload cache stripping)

## Drawbacks

* Adds a new configuration field, increasing the Configuration API surface.
  However, this follows the established pattern of `ExcludeResourcePrefixes`
  and is a natural extension of the same concept.
* Operators could misconfigure the list and break TAS scheduling. The beta
  graduation criterion addresses this with cross-reference validation.

## Alternatives

### Allowlist instead of denylist

Instead of excluding prefixes, an allowlist approach would only cache labels
matching specified prefixes. This is more aggressive and safer against new
unknown labels, but harder to configure correctly—operators would need to
enumerate all topology, flavor, and workload-relevant label prefixes. The
denylist approach is simpler for most users since the defaults cover common
infrastructure prefixes.

### Regex-based filtering

Using regular expressions instead of prefix strings provides more flexibility
but adds complexity and potential for misconfiguration. Prefix matching covers
the vast majority of infrastructure labels (which share common prefixes by
convention) and is simpler to reason about.

### Per-ClusterQueue configuration

Making the exclusion list per-ClusterQueue instead of global would allow
different queues to have different caching behavior. However, the TAS node
cache is shared, so per-queue filtering would complicate the cache
architecture significantly for minimal practical benefit.
