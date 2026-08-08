# KEP-13746: Topology Spreading

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1: Zone-level HA for inference services](#story-1-zone-level-ha-for-inference-services)
    - [Story 2: Mixed training/inference workloads](#story-2-mixed-traininginference-workloads)
    - [Story 3: Soft spreading with limited capacity](#story-3-soft-spreading-with-limited-capacity)
  - [Notes/Constraints/Caveats](#notesconstraintscaveats)
    - [Relationship to Kubernetes Pod Topology Spread Constraints](#relationship-to-kubernetes-pod-topology-spread-constraints)
    - [Spreading is cross-workload, not intra-workload](#spreading-is-cross-workload-not-intra-workload)
    - [Spreading only applies to topology-enabled Resource Flavors](#spreading-only-applies-to-topology-enabled-resource-flavors)
    - [Spreading is enforced only at scheduling time](#spreading-is-enforced-only-at-scheduling-time)
    - [Cold-start behavior](#cold-start-behavior)
  - [Risks and Mitigations](#risks-and-mitigations)
    - [Annotation parsing errors](#annotation-parsing-errors)
    - [Performance impact of counting workloads per domain](#performance-impact-of-counting-workloads-per-domain)
    - [Stale data after preemptions](#stale-data-after-preemptions)
- [Design Details](#design-details)
  - [API](#api)
    - [Annotation format](#annotation-format)
    - [Field definitions](#field-definitions)
    - [Admission constraint formula](#admission-constraint-formula)
    - [Annotation propagation from Job to Workload](#annotation-propagation-from-job-to-workload)
    - [Validation and error handling](#validation-and-error-handling)
  - [Scheduler integration](#scheduler-integration)
    - [Computing per-domain workload counts](#computing-per-domain-workload-counts)
    - [Banned and penalized domains](#banned-and-penalized-domains)
    - [Scoring](#scoring)
    - [Interaction with preemption](#interaction-with-preemption)
    - [Caching](#caching)
  - [Visibility](#visibility)
  - [Feature gate](#feature-gate)
- [Test Plan](#test-plan)
  - [Prerequisite testing updates](#prerequisite-testing-updates)
  - [Unit tests](#unit-tests)
  - [Integration tests](#integration-tests)
  - [e2e tests](#e2e-tests)
  - [Graduation Criteria](#graduation-criteria)
    - [Alpha](#alpha)
    - [Beta](#beta)
    - [Stable](#stable)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
  - [Use maxSkew instead of max-domain-percentage](#use-maxskew-instead-of-max-domain-percentage)
  - [First-class API field instead of annotation](#first-class-api-field-instead-of-annotation)
  - [Enforce spreading via kube-scheduler Pod Topology Spread Constraints](#enforce-spreading-via-kube-scheduler-pod-topology-spread-constraints)
<!-- /toc -->

## Summary

Topology Spreading is a Kueue feature that distributes admitted workloads across
topology domains (e.g., availability zones, racks, superblocks) to improve service
availability. It complements Topology Aware Scheduling (KEP-2724), which optimizes
for dense co-location of pods within a workload for low-latency communication.
While dense co-location is desirable for batch ML training, user-facing inference
services benefit from spreading replicas across failure domains so that a localized
infrastructure outage does not take down the entire service.

The feature introduces a new annotation,
`[alpha].kueue.x-k8s.io/topology-spreading`, on Kueue Workloads. The annotation
carries a JSON object specifying a label selector that identifies the set of
workloads to spread, and one or more spreading rules. Each rule names a topology
domain key (matching a level defined on a topology-enabled ResourceFlavor) and
a `max-domain-percentage` cap — the maximum fraction of the matching workload
population that may reside in any single domain. Rules may be `Required` (hard
constraint, blocks admission) or `Preferred` (soft constraint, penalizes
over-crowded domains during scoring while still admitting the workload).

## Motivation

Kueue's Topology Aware Scheduling focuses on maximizing workload density to
improve communication throughput and minimize latency. This is the right default
for batch ML training. However, Kueue is increasingly used for mixed
training/inference scenarios where the same cluster hosts long-running inference
services alongside batch jobs. For inference services, availability is a primary
concern: if all replicas are placed in a single zone or rack, a single failure can
take down the entire service.

Core Kubernetes provides
[Pod Topology Spread Constraints](https://kubernetes.io/docs/concepts/scheduling-eviction/topology-spread-constraints/)
for intra-workload pod distribution, but this mechanism operates at the level of
individual pods and cannot express cross-workload spreading (i.e., distributing
separate Kueue workload objects across domains). Kueue must be aware of the
full placement picture — including which topology domains are already heavily used
by other workloads — to make good admission decisions.

### Goals

- Allow users to express availability requirements for groups of workloads as a
  maximum-per-domain percentage cap.
- Support hard (`Required`) and soft (`Preferred`) enforcement modes.
- Support up to two levels of topology spreading for the alpha milestone (e.g.,
  zone and rack).
- Integrate with topology-enabled ResourceFlavors (as defined by KEP-2724). The
  feature has no effect on non-topology ResourceFlavors.
- Surface scheduling failures due to spreading constraints in workload conditions
  and logs.
- Be compatible with all or most (LWS, Deployment, plain Pods, and Pod Groups)
  supported CRDs.
- Limit the scope of label-selector matching to the same namespace for security
  and performance reasons.
- Apply spreading only at admission time; do not evict already-admitted workloads
  if the constraint is later violated.

### Non-Goals

- Intra-workload pod spreading (use Kubernetes Pod Topology Spread Constraints
  for that).
- Spreading across non-topology ResourceFlavors.
- Automatic cross-namespace spreading.
- More than two topology levels in the alpha milestone.
- Continuous rebalancing of already-admitted workloads.
- Changes to the core Kubernetes scheduler.

## Proposal

The feature introduces a new per-workload annotation that carries spreading
configuration as a JSON object. At admission time, the Kueue scheduler reads the
annotation, counts how many already-admitted workloads (from the same namespace,
matching the label selector) reside in each topology domain, and then decides
whether the candidate domain is admissible (`Required`) or how heavily to penalize
it (`Preferred`).

### User Stories

#### Story 1: Zone-level HA for inference services

A platform team runs an inference service consisting of many single-replica
Kueue workloads managed through a Deployment. Each workload is a separate GPU pod.
They want to ensure that no more than 45% of the inference pods end up in any
single availability zone. They annotate the workload template:

```yaml
"[alpha].kueue.x-k8s.io/topology-spreading": |
  {
    "workload-label-selector": "app=inference-service",
    "rules": [
      {"key": "topology.kubernetes.io/zone", "max-domain-percentage": "45"}
    ]
  }
```

Kueue then refuses to admit a new replica into a zone that already hosts ≥ 45%
of the total matching workloads, holding it pending until another zone has
capacity or the imbalance resolves through other means.

#### Story 2: Mixed training/inference workloads

An ML platform deploys both training jobs (which want dense packing via TAS) and
inference replicas (which need zone spreading). Inference workloads carry the
topology-spreading annotation. Training workloads do not carry the annotation and
are unaffected. The two features coexist in the same cluster and even the same
ClusterQueue.

#### Story 3: Soft spreading with limited capacity

An operator prefers zone spreading but recognizes that capacity constraints may
prevent strict enforcement. They use `"type": "Preferred"`:

```yaml
"[alpha].kueue.x-k8s.io/topology-spreading": |
  {
    "workload-label-selector": "app=inference-service",
    "rules": [
      {
        "key": "topology.kubernetes.io/zone",
        "max-domain-percentage": "45",
        "type": "Preferred"
      }
    ]
  }
```

The scheduler will admit the workload into any available domain, but will heavily
penalize over-crowded zones so that, all else being equal, workloads land in the
least-loaded zone.

### Notes/Constraints/Caveats

#### Relationship to Kubernetes Pod Topology Spread Constraints

Kubernetes Pod Topology Spread Constraints operate at the pod level and are
evaluated by kube-scheduler independently of Kueue. They can be used alongside
this feature for intra-workload pod distribution. Kueue Topology Spreading
operates at the workload level, across separate Kueue `Workload` objects, and is
evaluated during the Kueue admission cycle.

#### Spreading is cross-workload, not intra-workload

Each Kueue `Workload` object represents one logical unit (e.g., one LWS
leader+workers group, one Deployment replica managed by Kueue). Spreading
distributes these units across domains. Pod-level distribution within a single
workload is outside the scope of this KEP.

#### Spreading only applies to topology-enabled Resource Flavors

Topology Spreading is active for a workload only when **both** of the following
conditions hold:

1. The workload carries the `[alpha].kueue.x-k8s.io/topology-spreading`
   annotation with a valid configuration. Workloads without this annotation are
   unaffected — the feature is entirely opt-in.
2. The selected `ResourceFlavor` has a `Topology` field that declares the
   topology levels used as spreading keys. Flavors without a `Topology` field do
   not participate in domain-based scheduling.

If either condition is not met, all spreading rules are ignored for that
workload/flavor combination. Specifically: if a workload is matched to a
ResourceFlavor that does not declare the topology key referenced in a `Required`
rule, that flavor is skipped during flavor selection. For `Preferred` rules, a
non-topology flavor (or a topology flavor that does not declare the spreading key)
remains eligible for selection; the spreading preference is simply not applied for
that rule.

#### Spreading is enforced only at scheduling time

Once a workload is admitted, Kueue does not re-evaluate spreading constraints.
If the domain balance changes after admission (e.g., some workloads finish), the
remaining workloads are not migrated. This matches the Kubernetes behavior where
`topologySpreadConstraints` are evaluated at scheduling time only.

#### Cold-start behavior

The `max-domain-percentage` formula is designed to allow cold-start:

```
domain_count < max_domain_percentage * total_count + 1
```

When only a small number of workloads exist, the percentage may temporarily exceed
`max-domain-percentage` because the rounding effect of "+1" dominates. This is
intentional: a single replica should always be admissible even when only one
domain has capacity.

### Risks and Mitigations

#### Annotation parsing errors

Malformed JSON or invalid field values will cause the workload to be marked with
a `TopologySpreadInvalid` condition and excluded from scheduling. This prevents
silent misconfiguration. The exact validation rules are described in
[Validation and error handling](#validation-and-error-handling).

#### Performance impact of counting workloads per domain

Counting matching workloads across the namespace on every scheduling cycle could
be expensive at high workload counts. The implementation will maintain a cached
map of per-domain counts (keyed by `(namespace, label-selector, domain-key)`)
that is invalidated when a workload matching the selector is admitted or
evicted. See [Caching](#caching).

#### Stale data after preemptions

During preemption, candidate victim workloads are tentatively removed from the
count before the preemption is committed. If the preemption is retracted, the
count must be restored. The implementation will account for this by tracking
preempted workloads separately and subtracting them from the cached totals during
the admission cycle.

## Design Details

### API

#### Annotation format

Spreading configuration is expressed as a JSON-encoded string in a single
annotation on the `Workload` object:

```
[alpha].kueue.x-k8s.io/topology-spreading
```

The annotation value is a JSON object with the following top-level fields:

| Field | Type | Required | Description |
|---|---|---|---|
| `workload-label-selector` | string | yes | A Kubernetes label selector string identifying the set of workloads in the same namespace to count when evaluating spreading constraints. |
| `rules` | array | yes | One or more spreading rules. At least one rule must be present. |

Each element of `rules` is a JSON object:

| Field | Type | Required | Default | Description |
|---|---|---|---|---|
| `key` | string | yes | — | The topology domain key. Must correspond to a label key defined in the `Topology` of the ResourceFlavor being evaluated. |
| `max-domain-percentage` | string | yes | — | Integer percentage (1–99) expressed as a string. Caps the fraction of matching workloads that may reside in any single domain. |
| `type` | string | no | `"Required"` | Enforcement mode: `"Required"` blocks admission into over-crowded domains; `"Preferred"` penalizes them but still allows admission. |

Example:

```json
{
  "workload-label-selector": "app=inference-service,tier=gpu",
  "rules": [
    {
      "key": "topology.kubernetes.io/zone",
      "max-domain-percentage": "45",
      "type": "Required"
    },
    {
      "key": "cloud.google.com/gke-tpu-partition-4x4x4-id",
      "max-domain-percentage": "22",
      "type": "Preferred"
    }
  ]
}
```

#### Field definitions

**`workload-label-selector`**

A standard Kubernetes label selector string, as defined in the
[Kubernetes label selector documentation](https://kubernetes.io/docs/concepts/overview/working-with-objects/labels/).
The selector is matched against `Workload` objects in the **same namespace** as
the annotated workload. Cross-namespace matching is not supported.

**`max-domain-percentage`**

An integer value in the range [1, 99], expressed as a decimal string (without a
`%` suffix). It controls how many workloads may be scheduled into a single
topology domain, expressed as a percentage of the total number of matching
workloads currently admitted.

**`type`**

- `Required` (default): Kueue will not admit the workload into a domain where the
  constraint would be violated.
- `Preferred`: Kueue will admit the workload into any available domain, but will
  assign a large score penalty to domains that exceed `max-domain-percentage`.

#### Admission constraint formula

For a given domain `D`, let:

- `count(D)` = number of admitted matching workloads currently placed in domain `D`
- `total` = total number of admitted matching workloads across all domains

A domain `D` is admissible for a new workload if and only if:

```
count(D) < max_domain_percentage * total + 1
                       (integer division is NOT used; this is floating-point)
```

Equivalently, domains with:

```
count(D) / total < max_domain_percentage
```

may receive one additional workload (which may temporarily push the fraction above
the configured percentage). This formulation ensures that:

1. **Cold start**: When `total` is 0 or very small, the "+1" term always allows at
   least one domain to accept the workload.
2. **Steady state**: As the number of workloads grows, the constraint converges to
   an even distribution across domains.

The following example illustrates the formula for 3 domains and
`max-domain-percentage = 34`:

| Workload # | Domain A (count → %) | Domain B (count → %) | Domain C (count → %) | Placed in |
|---|---|---|----------------------|---|
| 1 | 0 → 0% ✓ | 0 → 0% | 0 → 0%               | A |
| 2 | 1 → 100% ✗ | 0 → 0% ✓ | 0 → 0%               | B |
| 3 | 1 → 50% ✗ | 1 → 50% ✗ | 0 → 0% ✓             | C |
| 4 | 1 → 33% ✓ | 1 → 33% | 1 → 33%              | A |
| 5 | 2 → 50% ✗ | 1 → 25% ✓ | 1 → 25%              | B |
| 6 | 2 → 40% ✗ | 2 → 40% ✗ | 1 → 20% ✓            | C |

#### Annotation propagation from Job to Workload

Users will typically annotate their Job (or other supported object such as a
LeaderWorkerSet or Deployment) rather than the controller-created `Workload` object.
Kueue propagates selected annotations from the Job to the Workload during workload
construction in `NewWorkload` (`pkg/controller/jobframework/utils.go`).

#### Validation and error handling

The following validation is performed when a workload carrying the annotation is
first seen by the Kueue controller:

1. The annotation value must be valid JSON.
2. `workload-label-selector` must be a syntactically valid Kubernetes label
   selector string.
3. `rules` must be a non-empty array of at most 2 entries (the alpha-milestone level limit; see [Goals](#goals)).
4. Each rule must contain `key` (non-empty string) and `max-domain-percentage`
   (parseable integer, 1–99 inclusive).
5. `type`, if present, must be `"Required"` or `"Preferred"`.

If any validation fails, the workload is marked with the condition:

```yaml
type: TopologySpreadInvalid
status: "True"
reason: InvalidConfiguration
message: "<human-readable description of the error>"
```

Workloads with this condition are excluded from all scheduling attempts until the
annotation is corrected and the workload is updated.

### Scheduler integration

The spreading logic is applied inside `findTopologyAssignment` (or its equivalent
entry point for non-TAS flavors), during the per-flavor evaluation phase of the
Kueue scheduler.

#### Computing per-domain workload counts

Before evaluating a candidate workload, the scheduler computes, for each
`(namespace, label-selector, topology-key)` triple referenced by the workload's
spreading rules, a map of `domainID → count`. The count includes all workloads in
the namespace that:

1. Match the label selector.
2. Are currently in the `Admitted` phase (i.e., have a valid topology assignment).
3. Have a placement recorded for the given topology key.

Workloads that are tentatively being preempted in the current scheduling cycle are
subtracted from the count before the candidate workload is evaluated.

#### Banned and penalized domains

From the per-domain counts, two derived sets are computed for each rule:

- **Banned domains** (applies to `Required` rules): domains where
  `count(D) >= max_domain_percentage * total + 1`. No node within a banned domain
  will be considered for admission.
- **Penalized domains** (applies to `Preferred` rules): domains where
  `count(D) >= max_domain_percentage * total + 1`. Nodes within penalized domains
  receive a large scheduling score penalty.

The penalty is applied once per domain, regardless of how many pods of the
candidate workload would land there.

#### Scoring

When a domain is penalized (soft constraint violated), a configurable penalty
value is subtracted from the candidate assignment's score. The penalty is designed
to be larger than any bin-packing score difference, so that the scheduler always
prefers a non-penalized domain when one is available. When all reachable domains
are penalized, the scheduler selects the least-penalized one, falling back to
standard bin-packing tiebreaking.

Once spreading constraints are satisfied, standard bin-packing scoring applies
within the admissible set of domains, ensuring that the cluster is not fragmented
beyond what is strictly necessary for HA goals.

#### Interaction with preemption

A high-priority workload with `type: Required` rules can trigger preemptions to
achieve the required spreading. Kueue's standard priority-based preemption
applies, with the additional constraint that freed capacity must land in a
non-banned domain.

A high-priority workload with `type: Preferred` rules does not trigger
preemptions to improve spreading. The scheduler minimizes skew across available
domains through scoring, but some preemptions may still occur to find capacity
anywhere in the cluster.

#### Caching

Counting matching workloads is O(n) in the number of admitted workloads per
namespace. To avoid repeating this scan on every scheduling cycle, the scheduler
maintains a cached structure:

```
cache[(namespace, selector-key, topology-key)] → map[domainID]int
```

The cache is invalidated (and re-computed lazily) whenever:

- A workload matching the selector is admitted.
- A workload matching the selector has its admission revoked (eviction or
  preemption).

The `selector-key` is the normalized string representation of the parsed label
selector, to allow sharing across workloads with identical selectors.

### Visibility

When a workload cannot be admitted because a `Required` spreading rule cannot be
satisfied (all eligible domains are banned), the following surfaces are updated:

- **Workload condition**: a `QuotaReserved: False` or `Admitted: False` condition
  with `reason: TopologySpreadConstraintNotMet` and a message describing which
  rule and domains are blocked.
- **Logs**: a structured log entry at `V(3)` naming the workload, the violated
  rule key, and the domain counts.

### Feature gate

The entire feature is gated behind a feature gate named `TopologySpreading`
(disabled by default in alpha). When the gate is off, the annotation is ignored
and all workloads are scheduled as if it were absent.

## Test Plan

[ ] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes
necessary to implement this enhancement.

### Prerequisite testing updates

- Ensure that the existing TAS integration tests cover the topology-key/label
  model that this feature relies on, to avoid regressions when the spreading
  logic interacts with TAS assignments.

### Unit tests

The following unit tests will be added or extended:

- `pkg/scheduler` (spreading rule parsing and validation): `<date>` - target ≥ 80%
- `pkg/scheduler` (banned/penalized domain computation): `<date>` - target ≥ 80%
- `pkg/scheduler` (scoring with penalized domains): `<date>` - target ≥ 80%
- `pkg/cache` (cache invalidation on admit/evict): `<date>` - target ≥ 80%

Concrete test cases:

1. Workloads with invalid annotation JSON are marked `TopologySpreadInvalid` and
   skipped.
2. Workloads with a missing required field (`key`, `max-domain-percentage`) are
   marked invalid.
3. Workloads with `max-domain-percentage` outside [1, 99] are marked invalid.
4. Workloads with an invalid label selector are marked invalid.
5. Workloads targeting a topology key not present on the ResourceFlavor skip that
   flavor (Required) or ignore the rule (Preferred).
6. Label-selector matching is restricted to the same namespace.
7. Correct per-domain count computation for:
    - Multiple workloads in multiple domains.
    - Workloads with multiple pods on a single node (counted once per workload).
    - Workloads that are candidates for preemption (excluded from count).
    - Workloads that were preemption candidates but are no longer (count restored).
8. Banned domain set correctly derived for Required rules.
9. Penalized domain set correctly derived for Preferred rules.
10. Multiple rules (different keys) all applied simultaneously.
11. Cache invalidation triggered on admit and on eviction.

### Integration tests

1. End-to-end admission: a new workload is denied admission into a banned domain
   and held pending until another domain becomes available (Required rule).
2. Preemption-driven spreading: a high-priority workload triggers preemption of
   a lower-priority workload to free a less-loaded domain.
3. Preferred spreading: a workload is admitted into a penalized domain when no
   unpenalized domain is available, and the workload condition reports the
   best-effort outcome.
4. Multiple rules at two topology levels (e.g., zone and rack).
5. Feature gate disabled: annotation is silently ignored; workloads are scheduled
   normally.
6. Integration with LWS, Deployment, plain Pods, Pod Groups: annotation is read
   from the respective Kueue Workload object created for each integration.

### e2e tests

E2e tests verifying zone-level spreading on a multi-zone kind cluster
with topology-labeled nodes, covering both Required and Preferred modes.

### Graduation Criteria

#### Alpha

- Feature gate `TopologySpreading` introduced, disabled by default.
- Annotation `[alpha].kueue.x-k8s.io/topology-spreading` documented as
  experimental.
- Support for up to two topology levels.
- `Required` and `Preferred` rule types implemented.
- `TopologySpreadInvalid` condition surfaced for misconfigured workloads.
- Unit and integration tests covering all cases listed in [Test Plan](#test-plan).
- e2e tests on a multi-zone cluster.
- No performance regression in existing TAS scheduling benchmarks (< 5% overhead
  for clusters with ≤ 1000 concurrent workloads).

#### Beta

- Feature gate enabled by default.
- Annotation promoted to `kueue.x-k8s.io/topology-spreading` (alpha prefix
  dropped); backward compatibility shim for old annotation key with a deferred
  removal comment targeting the release after stable graduation.
- More than two topology levels supported (following user feedback on alpha
  usage).
- Metrics added to track workloads blocked by spreading constraints.
- Documentation updated.
- No known correctness issues from alpha usage.

#### Stable

- Feature gate removed; feature unconditionally enabled.
- Annotation moved to a typed API field on `Workload.spec` (design to be finalized
  during beta based on experience with the annotation-based API).
- Full coverage in the periodic e2e suite with no flakes for two consecutive
  releases.

## Implementation History

- 2026-07-28: KEP created based on design document authored by Marcin Wielgus.

## Drawbacks

- The annotation-based API is less discoverable than a typed field and harder to
  validate with admission webhooks alone. This is mitigated by the
  `TopologySpreadInvalid` condition and by the plan to graduate to a typed field
  at stable.
- The per-domain counting adds scheduler complexity and a cache invalidation
  surface. This is mitigated by the cache design and by the constraint that
  matching is namespace-scoped.
- Spreading is not enforced after admission. In dynamic environments where
  workloads frequently complete and new ones are admitted, the actual distribution
  may drift from the target. This is a deliberate trade-off to avoid continuous
  eviction storms.

## Alternatives

### Use maxSkew instead of max-domain-percentage

Kubernetes `topologySpreadConstraints` uses `maxSkew` — a maximum allowed
difference in pod counts between any two domains. This is intuitive for small
homogeneous clusters but becomes awkward when:

- Domains vary significantly in size or capacity (a skew of 1 between a 1-pod
  domain and a 100-pod domain is very different from a skew of 1 between two
  50-pod domains).
- Capacity is located in a single zone or superblock that provides many smaller
  domains (e.g., TPU cubes, GB200 racks), where fragmenting across all domains
  is not recommended (R6-a).

`max-domain-percentage` expresses the constraint as a fraction of the total,
which naturally accommodates growth and heterogeneous domain sizes without
requiring reconfiguration as the workload population changes.

### First-class API field instead of annotation

Adding a typed field to `Workload.spec` would provide better validation and
discoverability. However, it requires CRD changes and would complicate the
integration-level API (users configure spreading on the integration CRD, not
directly on the Workload). Using an annotation during alpha allows rapid
iteration without committing to a field shape. The plan is to graduate to a typed
field at stable (see [Graduation Criteria](#graduation-criteria)).

### Enforce spreading via kube-scheduler Pod Topology Spread Constraints

One could translate Kueue spreading rules into pod-level
`topologySpreadConstraints` at admission time. This approach has several problems:

1. It operates at pod granularity, not workload granularity — a single workload
   with many pods would trivially satisfy a zone-level spread even if all pods
   land in the same zone.
2. kube-scheduler evaluates spreading using the live pod count, which may change
   between Kueue admission and pod scheduling, leading to races.
3. kube-scheduler does not understand Kueue workload identity, so the
   `labelSelector` would need to target pods rather than workloads, making it
   impossible to express "one workload per domain" semantics.