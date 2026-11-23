---
title: "Use Custom Preemption Configurations"
date: 2026-11-23
weight: 7
description: >
  Set up and verify declarative preemption configurations, time-based candidate filters, and sliding-window preemption limits using PreemptionConfig and PreemptionLimit.
---

{{< feature-state state="alpha" for_version="v0.20" >}}

This guide demonstrates how to configure and verify [Configurable Preemptions](/docs/concepts/preemption/configurable_preemption) in Kueue. You will learn how to:

1. Install the Alpha CRDs and enable the `ConfigurablePreemptions` and `ConfigurablePreemptionLimits` feature gates.
2. Configure a dedicated **Hero Workload Preemption** policy for an access-restricted `ClusterQueue`.
3. Configure a **Topology Defragmentation** policy for general-purpose workloads.
4. Configure **Priority Class** and **Time-Based** candidate filters to protect intermediate tiers, newly admitted jobs, or aging workloads.
5. Configure **Preemption Limits** (`PreemptionLimit`) to safely roll out new configurations, limit per-workload churn, and protect critical queues.
6. Verify preemption outcomes and limit status via workload conditions, eviction stats, `PreemptionLimit` status, and metrics.

## Before you begin

Make sure the following conditions are met:

- A Kubernetes cluster is running.
- The `kubectl` command-line tool has communication with your cluster.
- [Kueue is installed](/docs/getting-started/installation).
- You have permissions to install CRDs and manage cluster-scoped resources (`PreemptionConfig`, `PreemptionLimit`, and `ClusterQueue`).

## Install the alpha APIs

`PreemptionConfig` and `PreemptionLimit` are Alpha APIs (`kueue.x-k8s.io/v1alpha1`). Kueue does not install their CRDs by default. Install them before enabling the feature gates.

If you installed Kueue with `kubectl`, apply the Alpha CRDs manifest:

```shell
kubectl apply --server-side -f https://github.com/kubernetes-sigs/kueue/releases/download/{{< param "version" >}}/alpha-crds.yaml
```

If you installed Kueue with Helm, set `enableAlphaAPIs` to `true`:

```shell
helm upgrade kueue https://github.com/kubernetes-sigs/kueue/releases/download/{{< param "version" >}}/kueue-{{< param "chart_version" >}}.tgz \
  --namespace kueue-system \
  --reuse-values \
  --set enableAlphaAPIs=true
```

## Enable the feature gates

Enable `ConfigurablePreemptions` in your `kueue-manager-config` (see [Change the feature gates configuration](/docs/getting-started/installation/#change-the-feature-gates-configuration)). Enable `ConfigurablePreemptionLimits` as well if you plan to use `PreemptionLimit` resources (it requires `ConfigurablePreemptions`). For hero workload scenarios, enabling `PrioritizePreemptorWorkloads` is also recommended:

```yaml
apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
featureGates:
  ConfigurablePreemptions: true
  ConfigurablePreemptionLimits: true
  PrioritizePreemptorWorkloads: true
```

Restart `kueue-controller-manager` after updating `kueue-manager-config` so the new feature gates take effect. If you installed Kueue with Helm, set these flags in `controllerManager.featureGates` in your Helm values instead so future upgrades retain them.

## Configuration scenarios

Rather than combining multiple concerns into a single configuration, define distinct `PreemptionConfig` resources tailored to specific queue purposes and operational privileges, and pair them with `PreemptionLimit` guardrails to control disruption rates.

### Scenario: Hero workload preemption (restricted queue)

In this scenario, a dedicated, access-restricted `ClusterQueue` is established for top-priority distributed workloads ("hero workloads"). When hero workloads lack quota or contiguous topology, they are permitted to preempt lower-priority workloads across the cluster while respecting protection labels on mission-critical queues.

#### Define the PreemptionConfig

```yaml
apiVersion: kueue.x-k8s.io/v1alpha1
kind: PreemptionConfig
metadata:
  name: "hero-workloads-preemption-config"
spec:
  rules:
  - name: "hero-preempt-lower-priority"
    activationPolicy:
      trigger: "Always"
    candidateSelectors:
    - scope: "AnyClusterQueue"
      priority:
        mode: "Base"
        comparison: "LessThan"
      clusterQueueSelector:
        matchExpressions:
        - key: "example.com/protection-tier"
          operator: "NotIn"
          values: ["mission-critical"]
```

**How it works:**

- **Trigger**: `Always` evaluates candidates unconditionally whenever configurable preemption runs. This allows the hero workload to preempt lower-priority workloads both to reclaim quota and to clear physical topology constraints under Topology-Aware Scheduling.
- **Scope**: `AnyClusterQueue` searches across all queues in the cluster so physical topology domains (such as racks or blocks) can be unblocked cluster-wide.
- **Elevated privileges**: Workloads in this queue can evict lower-priority workloads across queues even if those target workloads are running within their nominal quota (quota borrowing applies within the same cohort tree and respects configured borrowing and lending limits; evictions outside the cohort tree free physical node capacity under Topology-Aware Scheduling rather than quota).
- **Protection guardrail**: `clusterQueueSelector` ensures that `ClusterQueue`s labeled `example.com/protection-tier: mission-critical` are never selected for preemption.

#### Attach to the restricted ClusterQueue

Attach the `PreemptionConfig` to your dedicated hero `ClusterQueue`:

```yaml
apiVersion: kueue.x-k8s.io/v1beta2
kind: ClusterQueue
metadata:
  name: "hero-jobs-cq"
  labels:
    example.com/hero-queue: "true"
  annotations:
    kueue.x-k8s.io/preemption-config-name: "hero-workloads-preemption-config"
spec:
  preemption:
    reclaimWithinCohort: Never
    withinClusterQueue: Never
  # ... resource groups, flavors, and quotas ...
```

{{% alert title="Important: Disabling Classical Preemption on the Preempting Queue" color="warning" %}}
In Alpha, candidates selected by `PreemptionConfig` are appended after candidates selected by `spec.preemption`. If you leave `spec.preemption.reclaimWithinCohort: LowerPriority` enabled on `hero-jobs-cq`, classical preemption evaluates cohort candidates first **without** checking the `clusterQueueSelector` in your `PreemptionConfig`. To ensure protection labels are strictly honored, set `reclaimWithinCohort: Never`, `withinClusterQueue: Never`, and (if configured) `borrowWithinCohort.policy: Never` on the preempting `ClusterQueue`.
{{% /alert %}}

### Scenario: Topology defragmentation (general queue)

In this scenario, large distributed jobs require contiguous physical topology (such as full host blocks or racks under [Topology-Aware Scheduling](/docs/concepts/topology_aware_scheduling)). A large workload may have feasible quota, but cannot be scheduled because smaller workloads are fragmenting the physical topology.

#### Define the PreemptionConfig

```yaml
apiVersion: kueue.x-k8s.io/v1alpha1
kind: PreemptionConfig
metadata:
  name: "topology-defrag-preemption-config"
  labels:
    example.com/rollout-stage: "experimental"
spec:
  rules:
  - name: "preempt-lower-priority-within-cohort"
    activationPolicy:
      trigger: "InsufficientQuota"
    candidateSelectors:
    - scope: "WithinCohortTree"
      priority:
        mode: "Base"
        comparison: "LessThan"
      labelSelector:
        matchExpressions:
        - key: "example.com/workload-tier"
          operator: "NotIn"
          values: ["mission-critical"]
  - name: "evict-smaller-jobs-for-topology"
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
      labelSelector:
        matchExpressions:
        - key: "example.com/workload-tier"
          operator: "NotIn"
          values: ["mission-critical"]
```

**How it works:**

- **Rule `preempt-lower-priority-within-cohort`**:
  - **Trigger**: `InsufficientQuota` activates when the incoming workload lacks sufficient quota to be admitted.
  - **Scope**: `WithinCohortTree` restricts quota reclamation to the cohort hierarchy (since borrowing outside the cohort tree is not permitted).
  - **Protection guardrail**: `labelSelector` prevents evicting lower-priority workloads labeled `example.com/workload-tier: mission-critical`.
- **Rule `evict-smaller-jobs-for-topology`**:
  - **Trigger**: `QuotaFeasibleAndInsufficientTopology` activates only when quota is already feasible for the incoming job under at least one eligible flavor assignment (after classical or Fair Sharing preemption, `Always` rules, and any applicable `InsufficientQuota` rules), but placement is blocked by physical topology constraints.
  {{% alert title="Note" color="info" %}}
  `QuotaFeasibleAndInsufficientTopology` does **not** acquire missing quota; it only resolves topology fragmentation once quota feasibility has been satisfied. Combining this trigger with an `InsufficientQuota` rule ensures workloads can first reclaim quota and then defragment topology.
  {{% /alert %}}
  - **Scope**: `AnyClusterQueue` searches across all `ClusterQueue`s in the cluster so topology can be unblocked across physical nodes regardless of cohort relationship.
  - **Asymmetric defragmentation**: `numericLabels` with `comparison: LessThan` ensures that a larger workload (for example, `example.com/node-count: "32"`) can preempt smaller workloads (for example, `example.com/node-count: "4"`), but a 4-node workload cannot preempt a 32-node workload in return. Omitting `fallbackValue` ensures unlabeled workloads are treated as incomparable and protected from eviction.
  - **Protecting mission-critical workloads**: Without explicit exclusion, defragmentation rules could evict smaller mission-critical workloads. The `labelSelector` prevents evicting workloads labeled `example.com/workload-tier: mission-critical`.

#### Attach to the ClusterQueue

```yaml
apiVersion: kueue.x-k8s.io/v1beta2
kind: ClusterQueue
metadata:
  name: "general-training-cq"
  annotations:
    kueue.x-k8s.io/preemption-config-name: "topology-defrag-preemption-config"
spec:
  preemption:
    reclaimWithinCohort: Never
    withinClusterQueue: Never
  # ... resource groups, flavors, and quotas ...
```

{{% alert title="Disabling Classical Preemption for Label Protection" color="info" %}}
Because `PreemptionConfig` manages both quota acquisition and topology defragmentation while enforcing protection for `example.com/workload-tier: mission-critical`, set `spec.preemption.reclaimWithinCohort: Never`, `spec.preemption.withinClusterQueue: Never`, and (if configured) `spec.preemption.borrowWithinCohort.policy: Never`. Leaving classical preemption enabled as `LowerPriority` would evaluate cohort candidates first without checking `labelSelector`, potentially evicting lower-priority mission-critical workloads.
{{% /alert %}}

### Scenario: Priority threshold for within-ClusterQueue preemption

{{< feature-state state="alpha" for_version="v0.21" >}}

In a shared `ClusterQueue` with multiple priority tiers (such as `production-high`, `production-standard`, `batch-low`, and `dev-preemptible`), you can restrict within-queue preemption so higher-priority workloads only preempt specific low-priority classes while protecting intermediate tiers such as `production-standard`:

```yaml
apiVersion: kueue.x-k8s.io/v1alpha1
kind: PreemptionConfig
metadata:
  name: "preempt-same-cq-low-priority"
spec:
  rules:
  - name: "preempt-same-cq-low-priority"
    activationPolicy:
      trigger: "InsufficientQuota"
    candidateSelectors:
    - scope: "WithinClusterQueue"
      priority:
        mode: "Base"
        comparison: "LessThan"
      priorityClassSelector:
        sourceRef:
          group: "kueue.x-k8s.io"
          kind: "WorkloadPriorityClass"
        nameExpressions:
        - operator: "In"
          values:
          - "batch-low"
          - "dev-preemptible"
```

**How it works:**

- **Priority class allowlist (`priorityClassSelector`)**: Combined with `priority` (`mode: Base` and `comparison: LessThan`), only candidates in the same `ClusterQueue` (`WithinClusterQueue`) that have strictly lower base priority **and** reference a `WorkloadPriorityClass` (`sourceRef`) named `batch-low` or `dev-preemptible` (`nameExpressions` with `operator: In`) are eligible for preemption. Running `production-standard` workloads are protected even when a `production-high` workload arrives.
- **ClusterQueue setup**: Attach this config via `kueue.x-k8s.io/preemption-config-name: "preempt-same-cq-low-priority"` and set `spec.preemption.withinClusterQueue: Never` on the `ClusterQueue` so classical preemption does not bypass `priorityClassSelector`.

### Scenario: Time-based workload protection

{{< feature-state state="alpha" for_version="v0.21" >}}

You can use `timeConstraints` to protect older workloads (for example, workloads older than 15 minutes that are further along or nearing completion) from eviction, while still giving newly admitted workloads a brief grace period (for example, 2 minutes) before they can be preempted:

```yaml
apiVersion: kueue.x-k8s.io/v1alpha1
kind: PreemptionConfig
metadata:
  name: "time-protected-preemption-config"
spec:
  rules:
  - name: "preempt-eligible-age-window"
    activationPolicy:
      trigger: "InsufficientQuota"
    candidateSelectors:
    - scope: "WithinClusterQueue"
      priority:
        mode: "Boosted"
        comparison: "LessThan"
      timeConstraints:
        execution:
          minSeconds: 120
        sinceCreation:
          maxSeconds: 900
```

**How it works:**

- **Protect older workloads (`sinceCreation.maxSeconds: 900`)**: Workloads created more than 15 minutes (900 seconds) ago are excluded from preemption candidates, protecting long-running or aging jobs against eviction. (You can also set `maxSeconds` under `execution` to protect workloads based on accumulated runtime instead of creation age.)
- **Short startup grace period (`execution.minSeconds: 120`)**: A candidate must have accumulated at least 2 minutes (120 seconds) of execution time in the `Admitted` state before it becomes eligible for preemption. If the workload cannot fit because candidates have not yet met this threshold, Kueue automatically schedules a timed requeue once the earliest skipped candidate can reach 120 seconds.
- **Relative age comparison**: To preempt only workloads created more recently than the incoming preemptor, replace `maxSeconds` with `comparison: "LessThan"` under `sinceCreation`.
- **ClusterQueue setup**: Attach this config via `kueue.x-k8s.io/preemption-config-name: "time-protected-preemption-config"` and set `spec.preemption.withinClusterQueue: Never` on the `ClusterQueue` so classical preemption does not bypass `timeConstraints` (if the queue belongs to a cohort and peer queues should not evict protected workloads via classical or Fair Sharing preemption, set `reclaimWithinCohort: Never` and, if configured, `borrowWithinCohort.policy: Never` on those peer `ClusterQueue`s as well).

### Scenario: Rate-limiting configurable preemptions with PreemptionLimit

{{< feature-state state="alpha" for_version="v0.21" >}}

`PreemptionLimit` lets you place sliding-window rate limits on configurable preemptions. Ensure the `ConfigurablePreemptionLimits` feature gate is enabled (alongside `ConfigurablePreemptions`) before creating `PreemptionLimit` resources.

#### Safely roll out a new PreemptionConfig

Label the new `PreemptionConfig` (as shown with `example.com/rollout-stage: "experimental"` in `topology-defrag-preemption-config` above) and cap it to at most 1 preemption per hour while you observe cluster behavior:

```yaml
apiVersion: kueue.x-k8s.io/v1alpha1
kind: PreemptionLimit
metadata:
  name: "experimental-config-rollout-limit"
spec:
  aggregation: "Global"
  configSelector:
    matchLabels:
      example.com/rollout-stage: "experimental"
  limit: 1
  windowSeconds: 3600
```

As confidence grows, increase `spec.limit`, shorten `spec.windowSeconds`, or remove the rollout label from the `PreemptionConfig`. Kueue automatically retries inadmissible workloads when a `PreemptionLimit` is deleted or when its spec is updated in a way that resets or relaxes the limit (such as increasing `spec.limit` or shortening `spec.windowSeconds`). Updating only metadata labels on a `PreemptionConfig` does not trigger an immediate retry; affected workloads observe the updated labels on their next scheduling cycle.

#### Prevent repeated preemptions of the same workload

Limit each workload to at most 1 configurable preemption per day across all `PreemptionConfig` resources (`configSelector: {}`), while exempting hero queues labeled `example.com/hero-queue: "true"`:

```yaml
apiVersion: kueue.x-k8s.io/v1alpha1
kind: PreemptionLimit
metadata:
  name: "limit-per-workload-churn"
spec:
  aggregation: "ByPreempteeWorkload"
  configSelector: {}
  preemptorClusterQueueSelector:
    matchExpressions:
    - key: "example.com/hero-queue"
      operator: "NotIn"
      values: ["true"]
  limit: 1
  windowSeconds: 86400
```

#### Block configurable preemptions targeting a mission-critical ClusterQueue

To prevent any `PreemptionConfig` from evicting workloads running in `hero-jobs-cq`, label the `ClusterQueue` (for example, `example.com/hero-queue: "true"`) and create a `PreemptionLimit` with `limit: 0`:

```yaml
apiVersion: kueue.x-k8s.io/v1alpha1
kind: PreemptionLimit
metadata:
  name: "protect-hero-queue"
spec:
  aggregation: "ByPreempteeClusterQueue"
  configSelector: {}
  preempteeClusterQueueSelector:
    matchLabels:
      example.com/hero-queue: "true"
  limit: 0
  windowSeconds: 3600
```

To give each tenant queue an independent preemption budget in a shared cohort (for example, at most 10 preemptions per hour per preempting queue), use `aggregation: "ByPreemptorClusterQueue"` with `preemptorClusterQueueSelector`.

## Common pitfalls

- **Classical preemption bypassing custom guardrails**: In Alpha, `spec.preemption` candidates are evaluated before `PreemptionConfig` rules (whose candidates are appended afterward). If your custom rules protect specific workloads using `priorityClassSelector`, `timeConstraints`, `labelSelector`, `clusterQueueSelector`, or `PreemptionLimit`, classical or Fair Sharing preemption in `spec.preemption` will still evaluate and evict those workloads unless you set `spec.preemption.reclaimWithinCohort: Never`, `spec.preemption.withinClusterQueue: Never`, and (if configured) `spec.preemption.borrowWithinCohort.policy: Never`.
- **Preemption flapping from symmetric rules**: When defining rules across queues (with `WithinCohortTree` or `AnyClusterQueue`), ensure rules are strictly asymmetric (for example, using `priority.comparison: LessThan` or `numericLabels.comparison: LessThan`) to avoid cascading preemptions where workloads repeatedly evict each other.
- **Label propagation**: Custom numeric labels or tier labels on Jobs are not copied to Kueue `Workload` resources unless added to `integrations.labelKeysToCopy` in your [Kueue Configuration](/docs/reference/kueue-config.v1beta2).
- **Manual labeling for `PreemptionLimit` selectors**: Kueue does not automatically inject name or metadata labels onto `ClusterQueue` or `PreemptionConfig` objects. Label those resources manually so `configSelector`, `preemptorClusterQueueSelector`, and `preempteeClusterQueueSelector` match as intended.
- **Topology defragmentation requires quota feasibility**: A workload blocked by both quota exhaustion and topology fragmentation cannot activate `QuotaFeasibleAndInsufficientTopology` until its quota requirement is satisfied by classical or Fair Sharing preemption, `Always` rules, or an `InsufficientQuota` rule.
- **Controller restarts reset `PreemptionLimit` counters**: In Alpha, `PreemptionLimit` event timestamps are kept strictly in memory. Restarting or failing over the Kueue controller resets active counts until new events occur.

## Verification and observability

To provide visibility into why a workload was preempted or throttled when custom rules and limits are used, Kueue tracks configurable preemption outcomes across workload status, `PreemptionLimit` status, and Prometheus metrics.

### Eviction scheduling stats

Kueue records detailed eviction information in `Workload.status.schedulingStats.evictions`, populating `underlyingCause` with the name of the `PreemptionConfig` that triggered the preemption:

```yaml
status:
  schedulingStats:
    evictions:
    - count: 1
      reason: Preempted
      underlyingCause: "hero-workloads-preemption-config"
```

### Status conditions

When a workload is preempted by a `PreemptionConfig` rule, Kueue sets two conditions in `Workload.status.conditions`. Both conditions share the same diagnostic message identifying the preemptor workload, the `PreemptionConfig` name, the rule name, and the selector index:

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

If multiple selectors within a rule triggered the candidate's preemption, their indices are concatenated with commas (for example, `rule hero-preempt-lower-priority/0,1`). If multiple rules contributed, they are separated with semicolons.

When a pending workload cannot be admitted because an active `PreemptionLimit` blocked preemption candidates, inspect its `QuotaReserved` condition (`status: "False"`). The message identifies the blocking `PreemptionLimit` name, its `aggregation`, and (when waiting for recorded events to expire can unblock it) the timestamp when the limit window elapses.

### PreemptionLimit status

Inspect active sliding-window preemption counts reported on a `PreemptionLimit` (refreshed approximately once per minute):

```shell
kubectl get preemptionlimit experimental-config-rollout-limit -o yaml
```

The status section is similar to the following:

```yaml
status:
  window:
    lastUpdateTime: "2026-11-23T12:00:00Z"
    topAggregatedCounts:
    - key: "Global"
      value: 1
```

### Metrics

Kueue exports Prometheus metrics for monitoring configurable preemptions and scheduling cycles:

- `kueue_preempted_workloads_total{reason="ConfigurablePreemption"}`: Counts workloads preempted by `PreemptionConfig` rules, labeled by `preempting_cluster_queue` and `reason`.
- `kueue_evicted_workloads_total{reason="Preempted", underlying_cause="<PreemptionConfig name>"}`: Counts evicted workloads per target `cluster_queue`, `reason`, and `underlying_cause` (populated with the `PreemptionConfig` name).
- `kueue_admission_attempts_total{result="inadmissible"}`: Counts scheduling cycles where no workload was admitted (labeled by `result` and `replica_role`, not by queue or reason). Inspect `Workload.status.conditions` to determine whether a specific pending workload was blocked by quota, topology, or an active `PreemptionLimit`.
