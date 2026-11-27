---
title: "Configurable Preemptions"
date: 2026-11-23
weight: 8
aliases:
  - "/docs/concepts/configurable_preemption/"
description: >
  Declarative, rule-based preemption policies and sliding-window preemption limits for custom candidate selection.
---

{{< feature-state state="alpha" for_version="v0.20" >}}

{{% alert title="Cascading Preemption Risk" color="warning" %}}
Configurable Preemptions is an advanced capability intended for power users and cluster administrators. Custom preemption rules carry inherent operational risks: if rules are misconfigured or symmetric (for example, jobs can mutually preempt one another), they can trigger cascading preemptions and continuous job disruptions across the cluster. Use [Preemption Limits](#preemption-limits) (`PreemptionLimit`) to cap preemption rates and limit blast radius when rolling out new configurations.
{{% /alert %}}

Configurable Preemptions introduces a declarative mechanism to define when preemption should occur and which workloads are eligible for eviction. It complements Kueue's existing [Classic Preemption](/docs/concepts/preemption/#classic-preemption) and [Fair Sharing](/docs/concepts/preemption/#fair-sharing) algorithms by enabling policies for complex operational scenarios, such as:

- **Topology defragmentation**: Allowing distributed workloads requiring specific physical topology domains (such as multi-node GPU or TPU training jobs under [Topology-Aware Scheduling](/docs/concepts/topology_aware_scheduling)) to preempt smaller workloads that fragment the cluster, even when all workloads are within their nominal quotas.
- **Mission-critical "hero" workloads**: Allowing dedicated, access-restricted queues with elevated preemption privileges to evict workloads across queues even when those workloads are within nominal quota (quota borrowing applies within the same cohort tree and remains subject to configured borrowing and lending limits; evicting workloads outside the cohort tree frees physical node capacity under TAS rather than quota). When combined with the `PrioritizePreemptorWorkloads` feature gate, hero jobs can lock quota and gain admission without extra cluster-wide modifications.
- **Granular priority, label, and time-based rules**: Evaluating candidates by priority, priority class, custom labels, cumulative execution duration, or workload age.
- **Preemption rate limiting**: Bounding the rate of configurable preemptions with `PreemptionLimit` across global, queue, or individual workload scopes.

To use Configurable Preemptions, install the Alpha CRDs (`PreemptionConfig` and `PreemptionLimit`) and enable the `ConfigurablePreemptions` [feature gate](/docs/getting-started/installation/#change-the-feature-gates-configuration). To enforce `PreemptionLimit` resources, also enable the `ConfigurablePreemptionLimits` feature gate.

## Architecture and API overview

Configurable Preemptions is driven by the cluster-scoped **`PreemptionConfig`** custom resource (`kueue.x-k8s.io/v1alpha1`, short name `preempcfg`; see the [v1alpha1 API Reference](/docs/reference/kueue.v1alpha1/#kueue-x-k8s-io-v1alpha1-PreemptionConfig)). It contains a list of up to 64 named rules, each defining:

- **Activation policy (`activationPolicy`)**: The trigger that activates the rule during a scheduling cycle.
- **Candidate selectors (`candidateSelectors`)**: Up to 32 criteria that identify which running workloads may be considered for preemption. Leaving `candidateSelectors` empty yields no candidates and disallows preemptions for that rule.
- **Preemptor selectors (`preemptorSelector` and `preemptorPriorityClassSelector`)** *(Optional)*: Criteria that restrict which incoming workloads can activate the rule.

Here is a complete example of a `PreemptionConfig` resource:

```yaml
apiVersion: kueue.x-k8s.io/v1alpha1
kind: PreemptionConfig
metadata:
  name: "topology-defragmentation"
spec:
  rules:
  - name: "evict-smaller-jobs-for-large-topology"
    activationPolicy:
      trigger: "QuotaFeasibleAndInsufficientTopology"
    candidateSelectors:
    - scope: "AnyClusterQueue"
      priority:
        mode: "Base"
        comparison: "LessThanOrEqual"
      numericLabels:
      - key: "example.com/node-count"
        comparison: "LessThan"
        fallbackValue: 0
      clusterQueueSelector:
        matchExpressions:
        - key: "example.com/protection-tier"
          operator: "NotIn"
          values: ["infrastructure"]
```

## Activation triggers

The `activationPolicy.trigger` field determines when a preemption rule becomes active during admission evaluation. Rules are evaluated incrementally in tier order: `Always` first, then `InsufficientQuota`, and finally `QuotaFeasibleAndInsufficientTopology`.

| Trigger | Description |
| :--- | :--- |
| `Always` | Matching candidates are contributed unconditionally during configurable preemption evaluation. |
| `InsufficientQuota` | Matching candidates are contributed only if preempting classical or Fair Sharing candidates and `Always` candidates does not yield sufficient quota to admit the incoming workload. |
| `QuotaFeasibleAndInsufficientTopology` | Matching candidates are contributed only if quota is already feasible for the preemptor under at least one eligible flavor assignment (after classical or Fair Sharing preemption, `Always` rules, and applicable `InsufficientQuota` rules), but the workload cannot be admitted because no placement satisfies its physical topology requirements (see [Topology-Aware Scheduling](/docs/concepts/topology_aware_scheduling)). |

{{% alert title="Overlapping Topology-Aware Scheduling Flavors" color="info" %}}
Normally, a candidate must consume at least one flavor resource that needs preemption. When both the preemptor's needed flavor and a candidate's Topology-Aware Scheduling flavor use `kubernetes.io/hostname` as their lowest topology level and share physical nodes, candidate matching widens to include workloads holding TAS capacity on those shared nodes, regardless of which flavor or resource they consume (while still applying the rule's `candidateSelectors`). Preempting those workloads frees physical node capacity in the preemptor's topology, and this applies across all triggers.
{{% /alert %}}

## Candidate selectors

Each rule specifies `candidateSelectors` to filter eligible preemption victims. Candidates resulting from multiple selectors within a rule (or across rules in the same trigger tier) are combined into a single deduplicated set. Within a single selector, all configured constraints are combined using logical AND (a candidate must satisfy every specified constraint).

### Relational scope (`scope`)

**Required.** The `scope` defines the relational boundary between the preemptor and candidate workloads:

- `WithinLocalQueue`: Candidate must belong to the exact same `LocalQueue` as the preemptor (matching name and namespace).
- `WithinClusterQueue`: Candidate must belong to the exact same `ClusterQueue` as the preemptor.
- `WithinParentCohort`: Candidate belongs to a `ClusterQueue` sharing the immediate parent `Cohort`, or to the preemptor's own `ClusterQueue` (even if standalone without a parent `Cohort`).
- `WithinCohortTree`: Candidate belongs to any `ClusterQueue` within the same root `Cohort` hierarchy, or to the preemptor's own `ClusterQueue` (even if standalone without a parent `Cohort`).
- `AnyClusterQueue`: No relationship constraint; candidates can be selected from any `ClusterQueue` in the cluster.

### Priority constraints (`priority`)

**Optional.** Defines priority comparison criteria against the incoming preemptor workload. If specified, both `mode` and `comparison` are required:

- **`mode`** (Required):
  - `Base`: Compares raw priority values assigned in `Workload.spec.priority`.
  - `Boosted`: Compares effective priority values adjusted by priority boosting (see `priorityBoost` in the [Workload spec](/docs/reference/kueue.v1beta2/#kueue-x-k8s-io-v1beta2-WorkloadSpec)).
- **`comparison`** (Required):
  - `LessThan`: Candidate priority < Preemptor priority.
  - `LessThanOrEqual`: Candidate priority <= Preemptor priority.
  - `GreaterThan`: Candidate priority > Preemptor priority.
  - `GreaterThanOrEqual`: Candidate priority >= Preemptor priority.

### Priority class selector (`priorityClassSelector`)

{{< feature-state state="alpha" for_version="v0.21" >}}

**Optional.** Filters candidate workloads by their priority class (`Workload.spec.priorityClassRef`, populated by Kueue for both `WorkloadPriorityClass` and Pod `PriorityClass`). When both `sourceRef` and `nameExpressions` are specified, a candidate must satisfy both:

- **`sourceRef`** (Optional): Filters workloads by the API `group` and `kind` of their priority class:
  - `group: kueue.x-k8s.io` and `kind: WorkloadPriorityClass`: Matches workloads using a Kueue `WorkloadPriorityClass`.
  - `group: scheduling.k8s.io` and `kind: PriorityClass`: Matches workloads using a Kubernetes Pod `PriorityClass`.
  - Workloads without a `priorityClassRef` do not match when `sourceRef` is set. If omitted, priority classes of either source match.
- **`nameExpressions`** (Optional): A non-empty list of selector requirements (up to 16 items) matched against `Workload.spec.priorityClassRef.name` (all requirements must be satisfied):
  - **`operator`** (Required):
    - `In`: Candidate's `spec.priorityClassRef.name` must match one of the listed `values` (workloads without a `priorityClassRef` do not match).
    - `NotIn`: Candidate's `spec.priorityClassRef.name` must not match any of the listed `values` (workloads without a `priorityClassRef` always match).
  - **`values`** (Required): A non-empty set of priority class names (up to 16 items).

When `WorkloadPriorityClassDefaulting` is enabled and a `"default"` `WorkloadPriorityClass` exists, or when a `globalDefault` Pod `PriorityClass` is configured, its name (for example, `"default"`) can be used in `nameExpressions` to match or exclude workloads that do not explicitly specify a priority class.

### Time constraints (`timeConstraints`)

{{< feature-state state="alpha" for_version="v0.21" >}}

**Optional.** Filters candidate workloads based on elapsed execution duration or workload age (see [`PreemptionConfigTimeConstraints`](/docs/reference/kueue.v1alpha1/#kueue-x-k8s-io-v1alpha1-PreemptionConfigTimeConstraints)). When specified, at least one of `execution` or `sinceCreation` must be set; if both are set, a candidate must satisfy both (logical AND):

- **`execution`** (Optional): Filters candidates by cumulative execution duration, measured as total time spent in the `Admitted` state across admission cycles (the sum of `Workload.status.accumulatedPastExecutionTimeSeconds` and elapsed time since the current `Admitted` transition, if currently admitted).
- **`sinceCreation`** (Optional): Filters candidates by workload age, measured from `Workload.metadata.creationTimestamp` until preemption evaluation.

Each constraint (`execution` or `sinceCreation`) accepts the following fields (at least one must be set; multiple fields are ANDed, and `minSeconds` must be less than or equal to `maxSeconds` when both are specified):

- **`minSeconds`** (Optional, integer >= 1): Minimum elapsed duration in seconds (inclusive) a candidate must have accumulated. Useful for protecting newly admitted workloads from immediate preemption.
- **`maxSeconds`** (Optional, integer >= 1): Maximum elapsed duration in seconds (inclusive) a candidate may have accumulated. Useful for protecting older or long-running workloads nearing completion or SLA deadlines.
- **`comparison`** (Optional): Compares the candidate's duration against the preemptor's duration for the same metric (`LessThan`, `LessThanOrEqual`, `GreaterThan`, `GreaterThanOrEqual`). For example, setting `sinceCreation.comparison: LessThan` selects only candidates created more recently than the preemptor.

### Custom numeric labels (`numericLabels`)

**Optional.** A list of up to 32 numeric label constraints allowing candidate filtering based on integer workload labels (such as number of GPUs or TPUs, slice index, or node count). Multiple numeric label constraints in a selector are joined using an AND rule (all constraints must be satisfied):

- **`key`** (Required): The workload label key containing strings that can be parsed as integers.
- **`comparison`** (Optional): How the candidate's label value compares to the preemptor's label value (`LessThan`, `LessThanOrEqual`, `GreaterThan`, `GreaterThanOrEqual`).
- **`fallbackValue`** (Optional): Integer value assumed if a workload does not have the label or the value cannot be parsed as an integer. Because Kubernetes label values cannot start with `-`, a negative `fallbackValue` makes unlabeled workloads compare smaller than any labeled workload. If omitted, workloads lacking the label are treated as incomparable and excluded.
- **`minValue` / `maxValue`** (Optional, integer >= 0): Inclusive lower and upper boundaries for the candidate's label value (`minValue <= maxValue`). If none of `comparison`, `minValue`, or `maxValue` is set, the constraint checks only that the candidate has the label key with a valid integer value.

{{% alert title="Important" color="warning" %}}
Custom labels from high-level jobs (such as Job, JobSet, or RayCluster) are not automatically copied to the Kueue `Workload` resource unless their keys are listed in `integrations.labelKeysToCopy` in your [Kueue Configuration](/docs/reference/kueue-config.v1beta2). Ensure your custom numeric label keys are configured for copying.
{{% /alert %}}

### Label selectors (`labelSelector` and `clusterQueueSelector`)

**Optional.** Standard Kubernetes label selectors:

- **`labelSelector`**: Filters candidate `Workload` metadata.
- **`clusterQueueSelector`**: Filters target `ClusterQueue` metadata.

## Preemptor selectors

**Optional.** A rule can restrict which incoming (preempting) workloads are allowed to activate it. When both selectors are specified, an incoming workload must satisfy both (logical AND). If omitted, any incoming workload in the queue can trigger the rule:

- **`preemptorSelector`**: A standard Kubernetes label selector filtering incoming `Workload` metadata.
- **`preemptorPriorityClassSelector`**: Filters incoming workloads by `Workload.spec.priorityClassRef` using `sourceRef` and `nameExpressions` (with the same semantics as [`priorityClassSelector`](#priority-class-selector-priorityclassselector) in candidate selectors).

## Referencing PreemptionConfig on a ClusterQueue

Attach a `PreemptionConfig` to a `ClusterQueue` using the `kueue.x-k8s.io/preemption-config-name` annotation:

```yaml
apiVersion: kueue.x-k8s.io/v1beta2
kind: ClusterQueue
metadata:
  name: "distributed-training-cq"
  annotations:
    kueue.x-k8s.io/preemption-config-name: "topology-defragmentation"
spec:
  preemption:
    reclaimWithinCohort: LowerPriority
    withinClusterQueue: LowerPriority
  # ... remaining ClusterQueue fields ...
```

{{% alert title="Interaction with Existing Preemption" color="info" %}}
In Alpha, `PreemptionConfig` works alongside `ClusterQueue.spec.preemption`. Candidates selected by `PreemptionConfig` rules are appended after candidates selected by classical or Fair Sharing preemption.
{{% /alert %}}

When `ConfigurablePreemptions` is enabled, Kueue watches `PreemptionConfig` resources. Creating, deleting, or updating the spec of a `PreemptionConfig` automatically retries inadmissible workloads in the `ClusterQueue`s that reference it so they are re-evaluated against the new rules.

## Candidate evaluation order and classical preemption interaction

During preemption evaluation in the scheduler, candidates from `ClusterQueue.spec.preemption` and the referenced `PreemptionConfig` are evaluated in the following order:

1. **Classical or Fair Sharing candidates first**: The globally configured classical or Fair Sharing preemption algorithm evaluates `ClusterQueue.spec.preemption` and yields its candidates in its usual order, unchanged.
2. **Configurable candidates appended tier by tier**: If classical or Fair Sharing candidates do not make the preemptor fit, Kueue appends candidates from `PreemptionConfig` rules in trigger order: `Always`, then `InsufficientQuota` (if quota still does not fit), then `QuotaFeasibleAndInsufficientTopology` (if quota fits, but topology placement fails). Within each trigger tier, candidates are deduplicated by UID and sorted using the default preemption ordering heuristics (prioritizing workloads already marked for eviction, workloads from other `ClusterQueue`s before the preemptor's own `ClusterQueue`, higher `LocalQueue` usage when Admission Fair Sharing applies, lower effective priority, more recent `QuotaReserved` transition time, and UID tie-breaking).
3. **No duplicate selection**: Each yielded candidate is removed from the scheduling snapshot, so a workload already selected by classical or Fair Sharing preemption is not selected again by `PreemptionConfig` rules. A configurable candidate is never preferred ahead of a classical or Fair Sharing candidate.
4. **Per-attempt behavior and minimal victim set**: For classical preemption (which may run separate attempts with and without borrowing), configurable candidates are appended at the end of each attempt. For Fair Sharing, they are appended after all configured Fair Sharing strategies are tried. Once the preemptor fits, reverse-order backfilling runs across the selected candidates to keep the evicted set minimal.
5. **Selective control**:
   - To use **only** `PreemptionConfig` rules and disable classical or Fair Sharing preemption candidates, set `spec.preemption.reclaimWithinCohort: Never`, `spec.preemption.withinClusterQueue: Never`, and (if configured) `spec.preemption.borrowWithinCohort.policy: Never` on the `ClusterQueue`.
   - To use **only** classical or Fair Sharing preemption, omit the `kueue.x-k8s.io/preemption-config-name` annotation.

{{% alert title="Note on Beta Evolution" color="info" %}}
In Beta+, `PreemptionConfig` will achieve full feature parity with classical and Fair Sharing preemption. The strategies will become mutually exclusive via a formal API field on `ClusterQueueSpec`, and the Alpha annotation will be retired.
{{% /alert %}}

## Timed workload requeuing

When a workload cannot be admitted because time-based constraints (such as `timeConstraints.execution.minSeconds`) or exhausted `PreemptionLimit` windows currently block preemption candidates, the scheduler computes the earliest timestamp across all evaluated flavors and skipped candidates at which at least one time constraint or limit window can allow further evaluation.

When Kueue requeues that workload as inadmissible, it schedules a delayed retry for that timestamp and moves the workload back to the active queue once that time elapses.

- This delayed retry is an earliest-progress heuristic so scheduling can reconsider additional candidates as soon as a constraint or limit expires; it does not guarantee that the workload will fit on that exact retry.
- Waiting cannot unblock a `PreemptionLimit` configured with `limit: 0`, or a limit exhausted solely by candidates selected within the current search pass (with no prior recorded events to expire). When such a limit blocks all remaining candidates (and no other candidate is waiting on a time constraint), no timed retry is scheduled; if it only blocks a subset of candidates, retry times from other limits or time constraints on other candidates are still used.

## Preemption limits

{{< feature-state state="alpha" for_version="v0.21" >}}

**Preemption Limits** introduce sliding-window rate-limiting guardrails for Configurable Preemptions via the cluster-scoped **`PreemptionLimit`** custom resource (`kueue.x-k8s.io/v1alpha1`, short name `preemplim`; see the [v1alpha1 API Reference](/docs/reference/kueue.v1alpha1/#kueue-x-k8s-io-v1alpha1-PreemptionLimit)). Administrators can use `PreemptionLimit` to roll out new `PreemptionConfig` rules incrementally, bound cluster-wide disruption rates, protect sensitive `ClusterQueue`s, and prevent individual workloads from repeated evictions.

To enforce `PreemptionLimit` resources, install the Alpha CRDs and enable both the `ConfigurablePreemptions` and `ConfigurablePreemptionLimits` feature gates. When `ConfigurablePreemptionLimits` is disabled, `PreemptionLimit` resources are not enforced and Configurable Preemptions are not rate-limited.

{{% alert title="Scope in Alpha" color="info" %}}
In Alpha, `PreemptionLimit` applies only to preemptions triggered by `PreemptionConfig` rules. Classical and Fair Sharing preemptions configured through `ClusterQueue.spec.preemption` are not constrained by `PreemptionLimit`.
{{% /alert %}}

Here is an example `PreemptionLimit` that allows at most 10 configurable preemptions across the cluster in any 5-minute sliding window:

```yaml
apiVersion: kueue.x-k8s.io/v1alpha1
kind: PreemptionLimit
metadata:
  name: "global-preemption-rate-limit"
spec:
  aggregation: "Global"
  configSelector: {}
  limit: 10
  windowSeconds: 300
```

### Aggregation levels (`aggregation`)

**Required.** The `aggregation` field sets the grouping key used to count confirmed `(preemptor, preemptee)` preemption events within the sliding window:

| Aggregation | Description | Status count key |
| :--- | :--- | :--- |
| `Global` | Restricts the total number of matching configurable preemption events across the entire cluster. | `"Global"` |
| `ByPreemptorClusterQueue` | Restricts the number of matching preemption events initiated by workloads from each preemptor `ClusterQueue`. | `<ClusterQueue name>` |
| `ByPreempteeClusterQueue` | Restricts the number of matching preemption events targeting workloads running in each preemptee `ClusterQueue`. Setting `limit: 0` blocks all matching configurable preemptions against those queues. | `<ClusterQueue name>` |
| `ByPreempteeWorkload` | Restricts how many times an individual `Workload` can be preempted within the window. | `<namespace>/<workload-name>` |

### Selectors and sliding window

Each `PreemptionLimit` scopes which configurable preemptions it counts and constrains using these fields:

- **`configSelector`** (Required): Standard Kubernetes `LabelSelector` selecting `PreemptionConfig` objects by label. An empty selector (`{}`) matches all `PreemptionConfig` objects.
- **`preemptorClusterQueueSelector`** (Optional): Standard Kubernetes `LabelSelector` selecting the preempting workload's `ClusterQueue`. If omitted, applies to preemptors from all `ClusterQueue`s.
- **`preempteeClusterQueueSelector`** (Optional): Standard Kubernetes `LabelSelector` selecting the target workload's `ClusterQueue`. If omitted, applies to preemptees in all `ClusterQueue`s.
- **`limit`** (Required, integer >= 0): Maximum number of confirmed `(preemptor, preemptee)` preemption events permitted per aggregation key within the sliding window. Setting `limit: 0` blocks all matching configurable preemptions.
- **`windowSeconds`** (Required, integer `1` to `604800`): Duration of the sliding time window in seconds (from 1 second up to 7 days).

Kueue does not automatically add labels to `ClusterQueue` or `PreemptionConfig` resources. Administrators must label them manually to match `PreemptionLimit` selectors.

### Enforcement behavior and operational caveats

- **Multiple matching limits**: If a candidate preemption matches multiple `PreemptionLimit` resources, budget must be available in all of them for the candidate to be selected.
- **Already-evicted candidates**: Candidates already marked as evicted (or with an eviction already in progress) do not consume limit budget and remain selectable even when an applicable limit is exhausted or set to `0`.
- **Greedy candidate evaluation**: Limits are checked greedily as candidates are scanned in order before reverse-order backfilling runs. Earlier candidates can consume remaining budget and cause later, larger candidates to be skipped even if backfilling would have kept only the larger candidate.
- **In-cycle overlap recomputation**: If multiple workloads evaluated in the same scheduling cycle contend for the same limit budget, Kueue detects the overconsumption when issuing preemptions and recomputes the conflicting workload's targets against the remaining budget, or defers and requeues the workload if no valid targets remain.
- **In-memory state resets on restart**: Preemption event timestamps are tracked in memory only (not persisted in etcd). Controller restarts, upgrades, or leader failovers reset the counters, temporarily allowing up to the configured `limit` again.
- **Increasing `windowSeconds`**: Expired timestamps are periodically pruned based on the active window. Increasing `windowSeconds` does not restore events that were already pruned under the shorter window.
- **Entity tracking by name**: Events are keyed by `ClusterQueue` name or `<namespace>/<workload-name>` rather than UID. Deleting and recreating an object with the same name within the active window retains its recorded events.
- **Spec changes and workload retries**: Modifying a `PreemptionLimit`'s `aggregation` or any selector (`configSelector`, `preemptorClusterQueueSelector`, or `preempteeClusterQueueSelector`) resets its recorded in-memory timestamps. Deleting a `PreemptionLimit`, or updating its spec in a way that resets or relaxes the limit (such as increasing `limit` or shortening `windowSeconds`), retries inadmissible workloads so workloads previously blocked by limits are re-evaluated promptly.

## Observability

To provide visibility into why a workload was preempted or blocked when custom rules and limits are used, Kueue surfaces configurable preemption details across workload status and `PreemptionLimit` status:

### Eviction scheduling stats

Kueue records detailed eviction information in `Workload.status.schedulingStats.evictions`:

- **`reason`**: Set to `Preempted`.
- **`underlyingCause`**: Identifies the `PreemptionConfig` name that caused the preemption.

### Workload status conditions

When a workload is preempted by a `PreemptionConfig` rule, Kueue sets two conditions in `Workload.status.conditions`:

```yaml
status:
  conditions:
  - type: Evicted
    status: "True"
    reason: Preempted
    message: "Preempted by default/hero-job-xyz because of preemption config hero-workloads-preemption-config rule hero-preempt-lower-priority/0"
  - type: Preempted
    status: "True"
    reason: ConfigurablePreemption
    message: "Preempted by default/hero-job-xyz because of preemption config hero-workloads-preemption-config rule hero-preempt-lower-priority/0"
```

When a pending workload cannot be admitted because candidate selection is blocked by an active `PreemptionLimit`, its `QuotaReserved` condition (`status: "False"`) message includes the blocking limit name, aggregation, and (when waiting for recorded events to expire can unblock it) the UTC timestamp when the limit window elapses (for example, `preemptions blocked by PreemptionLimit <name> (aggregation <aggregation>) until <timestamp>`, or without `until <timestamp>` for non-expiring blocks such as `limit: 0`).

### PreemptionLimit status

Kueue periodically refreshes `PreemptionLimit.status.window` (approximately once per minute) with informational counts of recorded preemptions within the active sliding window:

```yaml
status:
  window:
    lastUpdateTime: "2026-11-23T12:00:00Z"
    topAggregatedCounts:
    - key: "team-a-cq"
      value: 3
```

`topAggregatedCounts` is capped at the top 100 aggregation keys to stay within CRD object size limits and is intended for observability only; scheduling enforces limits directly from the live in-memory state.

## Preventing preemption flapping

When authoring preemption rules across queues (especially with `AnyClusterQueue` or `WithinCohortTree`), ensure that rules are **strictly asymmetric**: if Workload A can preempt Workload B, Workload B must not be able to preempt Workload A in return.

You can reduce flapping and bound disruption by:

- Requiring `priority.comparison: LessThan`.
- Enforcing `numericLabels` with `comparison: LessThan` on job sizes.
- Protecting newly admitted workloads with `timeConstraints.execution.minSeconds`.
- Restricting preemption rights to dedicated high-priority queues.
- Adding `PreemptionLimit` guardrails (such as `Global`, `ByPreempteeWorkload`, or conservative rollout limits on new `PreemptionConfig` resources).

## What's next

- Follow the [Use Custom Preemption Configurations](/docs/tasks/manage/setup_configurable_preemption) guide for hands-on configuration steps and practical scenarios.
- Read [Preemption](/docs/concepts/preemption) to understand Classic Preemption and Fair Sharing algorithms.
- Read [Topology-Aware Scheduling](/docs/concepts/topology_aware_scheduling) to see how physical network topology and defragmentation interact.
- Learn about [Workload Priority Class](/docs/concepts/workload_priority_class) to configure workload priorities.
- Inspect the full schemas for [`PreemptionConfig`](/docs/reference/kueue.v1alpha1/#kueue-x-k8s-io-v1alpha1-PreemptionConfig) and [`PreemptionLimit`](/docs/reference/kueue.v1alpha1/#kueue-x-k8s-io-v1alpha1-PreemptionLimit) in the v1alpha1 API Reference.
