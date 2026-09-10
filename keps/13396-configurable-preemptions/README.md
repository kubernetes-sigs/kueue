# KEP-13396: Configurable Preemptions

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [1. Defragmentation](#1-defragmentation)
  - [2. Hero workloads](#2-hero-workloads)
  - [3. Desired behavior of preemptions is business driven](#3-desired-behavior-of-preemptions-is-business-driven)
  - [Other related issues](#other-related-issues)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
    - [Referencing PreemptionConfig and Strategy Interaction](#referencing-preemptionconfig-and-strategy-interaction)
  - [User Stories](#user-stories)
    - [Story 1 - Defragmentation](#story-1---defragmentation)
    - [Story 2 - Hero job](#story-2---hero-job)
    - [Story 3 - Business driven preemption rules](#story-3---business-driven-preemption-rules)
  - [Notes](#notes)
  - [Constraints](#constraints)
  - [Caveats](#caveats)
  - [Risks and Mitigations](#risks-and-mitigations)
    - [Cascading preemptions due to misconfiguration](#cascading-preemptions-due-to-misconfiguration)
    - [Performance degradation](#performance-degradation)
    - [Security considerations](#security-considerations)
- [Design Details](#design-details)
  - [Proposed API PreemptionConfig](#proposed-api-preemptionconfig)
    - [Default Candidate Ordering](#default-candidate-ordering)
  - [Preemption evaluation flow in scheduler](#preemption-evaluation-flow-in-scheduler)
    - [Step-by-Step Breakdown](#step-by-step-breakdown)
  - [Candidate Organization: Per-Selector, Per-CQ Priority Queues](#candidate-organization-per-selector-per-cq-priority-queues)
    - [Architectural Groundwork for Configurable Candidate Ordering](#architectural-groundwork-for-configurable-candidate-ordering)
  - [Observability](#observability)
  - [Test Plan](#test-plan)
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
- [Future Work Ideas](#future-work-ideas)
  - [Configurable Candidate Ordering](#configurable-candidate-ordering)
    - [Proposed API for Custom Ordering](#proposed-api-for-custom-ordering)
    - [Examples with Custom Ordering](#examples-with-custom-ordering)
      - [Story 1 - Defragmentation with Explicit Priority Ordering](#story-1---defragmentation-with-explicit-priority-ordering)
      - [Story 2 - Hero Workload with Explicit Priority Ordering](#story-2---hero-workload-with-explicit-priority-ordering)
    - [Efficient Iteration Through Candidates in Preemption Order](#efficient-iteration-through-candidates-in-preemption-order)
      - [Problem Statement](#problem-statement)
      - [Naive Solutions and Complexity Bottlenecks](#naive-solutions-and-complexity-bottlenecks)
      - [Proposed Approach: Multi-Queue Dynamic Iteration](#proposed-approach-multi-queue-dynamic-iteration)
      - [Example Walkthrough](#example-walkthrough)
      - [Implementation Caveats and Selector Isolation](#implementation-caveats-and-selector-isolation)
      - [Complexity Analysis](#complexity-analysis)
      - [Complexity Comparison](#complexity-comparison)
      - [Open Challenges](#open-challenges)
  - [Time-Based Candidate Selectors (Execution and Creation Duration)](#time-based-candidate-selectors-execution-and-creation-duration)
    - [Proposed API for Time-Based Candidate Selectors](#proposed-api-for-time-based-candidate-selectors)
    - [Examples with Time-Based Candidate Selectors](#examples-with-time-based-candidate-selectors)
      - [Story 1 - Minimal Execution Duration Before Preemption](#story-1---minimal-execution-duration-before-preemption)
      - [Story 2 - SLA Protection Based on Workload Creation Time](#story-2---sla-protection-based-on-workload-creation-time)
  - [Workload Priority Class Selectors](#workload-priority-class-selectors)
    - [Proposed API for Workload Priority Class Selectors](#proposed-api-for-workload-priority-class-selectors)
    - [Examples with Workload Priority Class Selectors](#examples-with-workload-priority-class-selectors)
      - [Story 1 - Priority Threshold for Within-ClusterQueue Preemptions](#story-1---priority-threshold-for-within-clusterqueue-preemptions)
      - [Story 2 - Priority Threshold for Reclaim Within Cohort](#story-2---priority-threshold-for-reclaim-within-cohort)
  - [PreemptionLimit (Rate-Limiting Guardrails)](#preemptionlimit-rate-limiting-guardrails)
    - [Proposed API for PreemptionLimit](#proposed-api-for-preemptionlimit)
    - [Observability When Reaching Preemption Limits](#observability-when-reaching-preemption-limits)
    - [Examples with PreemptionLimit](#examples-with-preemptionlimit)
      - [Story 1 - Global Preemption Rate Limiting](#story-1---global-preemption-rate-limiting)
      - [Story 2 - Protecting a Mission-Critical ClusterQueue from Preemption](#story-2---protecting-a-mission-critical-clusterqueue-from-preemption)
  - [Minimum Trigger Duration (MinTriggerRequiredDuration)](#minimum-trigger-duration-mintriggerrequiredduration)
    - [Proposed API for Minimum Trigger Duration](#proposed-api-for-minimum-trigger-duration)
    - [Examples with Minimum Trigger Duration](#examples-with-minimum-trigger-duration)
      - [Story 1 - Grace Period for Topology Defragmentation](#story-1---grace-period-for-topology-defragmentation)
<!-- /toc -->

## Summary

<!--
This section is incredibly important for producing high-quality, user-focused
documentation such as release notes or a development roadmap. It should be
possible to collect this information before implementation begins, in order to
avoid requiring implementors to split their attention between writing release
notes and implementing the feature itself. KEP editors and SIG Docs
should help to ensure that the tone and content of the `Summary` section is
useful for a wide audience.

A good summary is probably at least a paragraph in length.

Both in this section and below, follow the guidelines of the [documentation
style guide]. In particular, wrap lines to a reasonable length, to make it
easier for reviewers to cite specific portions, and to minimize diff churn on
updates.

[documentation style guide]: https://github.com/kubernetes/community/blob/master/contributors/guide/style-guide.md
-->

This KEP introduces **Configurable Preemptions** in Kueue through the `PreemptionConfig` cluster-scoped CRD (with rate-limiting guardrails via `PreemptionLimit` deferred to future work).
This enables declarative preemption policies for scenarios unsupported by existing heuristics, including topology defragmentation, mission-critical "hero" workloads, and business SLA constraints.
With `PreemptionConfig`, administrators can configure explicit triggers (quota or topology constraints) and candidate selectors (such as priority relations, queue relations, and custom numeric labels, with minimal trigger duration, time-based candidate duration selectors, priority class selectors, custom ordering, and `PreemptionLimit` deferred to future work). In the initial iteration, candidate evaluation reuses the default ordering rules from classical preemption and fair sharing. In Alpha, `PreemptionConfig` is referenced via an explicit Alpha annotation on the `ClusterQueue` (`kueue.x-k8s.io/alpha-preemption-config`), keeping the defaulting of `spec.preemption` intact and merging the candidate outputs of both classical and configurable preemption strategies. For Beta+, as `PreemptionConfig` achieves full feature parity with classical preemption, both strategies will become mutually exclusive via a formal API field, and the Alpha annotation will be retired.

## Motivation

### 1. Defragmentation

Kueue does not support inter-ClusterQueue topology-based preemptions when workloads are within their cluster queue's nominal quota. Because of this, small workloads can sometimes block large topology domains.
In some clusters, this may be desired, as disruptions of critical workloads should be avoided as much as possible.

In other setups, better cluster utilization or the ability to schedule higher-priority jobs that are blocked due to cluster fragmentation is more important. Therefore, additional defragmentation mechanisms are needed to allow higher-priority workloads to move smaller workloads between topology domains. The expected behavior in this case can be seen in the following example:

Let us consider a cluster with 2 racks where each rack has 4 nodes.
For simplicity, we will equate resources with nodes, and assume there is only one resource flavor with a natural 2-level topology: rack, hostname.

And 3 cluster queues:

- Queue A with quota 1.
- Queue B with quota 1.
- Queue C with quota 4.

The total cluster capacity is 8 nodes, which is strictly larger than the sum of the queues' nominal quotas (6 nodes across all queues).

At **Timestamp 1**, two workloads are running:
Workload A from Queue A and Workload B from Queue B, each consuming 1 node (their respective queue's nominal quota).

```mermaid
block-beta
columns 3
block-beta
  columns 1
  t1["Timestamp 1"]
  block:rack1
    columns 2
    rack1A[" "]
    rack1B["Workload A"]
    rack1C[" "]
    rack1D[" "]
  end
  space
  block:rack2
    columns 2
    rack2A[" "]
    rack2B[" "]
    rack2C["Workload B"]
    rack2D[" "]
  end
  space
end

style t1 fill:none,stroke:none,font-weight:bold
style rack1B fill:#969,stroke:#333,stroke-width:4px
style rack2C fill:#369,stroke:#333,stroke-width:4px

```

Next, Workload C arrives requiring 4 nodes in a single rack. Although sufficient quota is available, neither rack can accommodate it because Workload A and Workload B occupy one node in each rack, fragmenting both topology domains.

To schedule Workload C, one of the running workloads must be preempted and relocated to the other rack. However, Kueue currently does not support this because all running workloads are within their cluster queues' nominal quotas.

```mermaid
block-beta
columns 3
block-beta
  columns 1
  t1["Timestamp 1"]
  block:rack1
    columns 2
    rack1A[" "]
    rack1B["Workload A"]
    rack1C[" "]
    rack1D[" "]
  end
  space
  block:rack2
    columns 2
    rack2A[" "]
    rack2B[" "]
    rack2C["Workload B"]
    rack2D[" "]
  end
  space
end

arrow1<["Workload B preemption"]>(right)

block-beta
  columns 1
  t2["Timestamp 2"]
  block:rack1after
    columns 2
    rack1afterA["Workload B"]
    rack1afterB["Workload A"]
    rack1afterC[" "]
    rack1afterD[" "]
  end
  space
  block:rack2after
    columns 2
    rack2afterA["Workload C"]
    rack2afterB["Workload C"]
    rack2afterC["Workload C"]
    rack2afterD["Workload C"]
  end
  space
end

style t1 fill:none,stroke:none,font-weight:bold
style t2 fill:none,stroke:none,font-weight:bold
style rack1B fill:#969,stroke:#333,stroke-width:4px
style rack2C fill:#369,stroke:#333,stroke-width:4px
style rack1afterA fill:#369,stroke:#333,stroke-width:4px
style rack1afterB fill:#969,stroke:#333,stroke-width:4px
style rack2afterA fill:#f84,stroke:#333,stroke-width:4px
style rack2afterB fill:#f84,stroke:#333,stroke-width:4px
style rack2afterC fill:#f84,stroke:#333,stroke-width:4px
style rack2afterD fill:#f84,stroke:#333,stroke-width:4px
```

### 2. Hero workloads

Related [issue](https://github.com/kubernetes-sigs/kueue/issues/8826).

Hero workloads are often high-priority and require the majority of the cluster quota.
Currently, Kueue does not natively support their needs as it has no notion of elevated preemption privileges to overrule standard quota and topology limitations when needed. In particular, Kueue does not allow for preemption of jobs that are within cluster queues' guaranteed quotas from other cluster queues.
Current workarounds like "temporary" overrides of
quotas assigned to all cluster queues are bad from the user experience perspective as they require manual handling.
Moreover, they lead to wasted resources if a hero workload fails for
some reason and quotas are not brought back to their previous state.

### 3. Desired behavior of preemptions is business driven

Many companies have specific requirements for when a workload should or should not be preempted,
depending on their business needs.
For example, [issue #9596](https://github.com/kubernetes-sigs/kueue/issues/9596) asks for adding a parameter for minimal execution time before preemption.

Yet another example comes from the ETL world. Some businesses have SLAs for the freshness of the provided information. Therefore,
failure to run a workload in time (as dictated by the SLA) can lead to significant financial penalties. On the other hand, those workloads might not be super high-priority — they should not preempt other workloads, and the fact that they will run in the next "X hours" is enough to satisfy the SLA.

Other businesses might need workloads that are not preemptible at all.

### Other related issues

- maxPriorityThreshold for withinClusterQueue preemptions [#12001](https://github.com/kubernetes-sigs/kueue/issues/12001)
- maxPriorityThreshold for reclaimWithinCohort [#12046](https://github.com/kubernetes-sigs/kueue/issues/12046)

<!--
This section is for explicitly listing the motivation, goals, and non-goals of
this KEP.  Describe why the change is important and the benefits to users. The
motivation section can optionally provide links to [experience reports] to
demonstrate the interest in a KEP within the wider Kubernetes community.

[experience reports]: https://github.com/golang/go/wiki/ExperienceReports
-->

### Goals

1. Preemptions triggered by lack of sufficient topology domains to run the workload.
2. Inter-ClusterQueue preemptions.
3. Configurability of preemptions to satisfy various business requirements.
4. Definition of the most common fields that can be used to build preemption configs.
5. Definition of "golden" configs for common preemption scenarios.

### Non-Goals

1. Full defragmentation of the cluster.
2. One advanced preemption config to fulfill all preemption needs.
3. Support for preemptions using arbitrary Workload fields — this KEP aims to
   create a good baseline for configurations that can be extended in the future; it does not aim to be comprehensive for every possible scenario.
4. Complete replacement of current preemption strategies.

## Proposal

Introduce a new CRD **PreemptionConfig** that will be used to define:

- triggers for when preemption should occur (e.g. insufficient topology to schedule the workload),
- rules defining which workloads should be considered for preemption.

In the initial iteration, candidate workloads are evaluated and ordered using the default ordering rules from classical preemption and fair sharing (reusing the existing preemption ordering logic in `pkg/scheduler/preemption/common/ordering.go`). Configurable candidate ordering is deferred to [Future Work Ideas](#future-work-ideas).

The **PreemptionConfig** object is a cluster-wide resource that can be referenced by multiple cluster queues.

#### Referencing PreemptionConfig and Strategy Interaction

In Kueue, `ClusterQueue.spec.preemption` has declarative kubebuilder defaulting (`+kubebuilder:default={}`). Setting `preemption` to `null` or removing its declarative defaulting cannot be done without a breaking change for existing clients, manifests, and stored objects.

Furthermore, introducing a formal field on `ClusterQueueSpec` (e.g. `preemptionConfigName`) in Alpha that allows merging with `spec.preemption`, and subsequently changing the field in Beta to be mutually exclusive, would constitute an incompatible breaking semantic change for that field.

Therefore, the integration is designed with a two-phase evolution:

1. **Alpha: Reference via Annotation & Merged Candidate Outputs**
   - **No new field in `ClusterQueueSpec`**: To avoid introducing field-level semantics that would break when transitioning to mutual exclusivity in Beta, `PreemptionConfig` is referenced in Alpha using a dedicated ClusterQueue annotation:
     ```yaml
     metadata:
       annotations:
         kueue.x-k8s.io/alpha-preemption-config: "<preemption-config-name>"
     ```
     This annotation is explicitly marked as Alpha and designated to be retired when moving to Beta.
   - **Preserve `spec.preemption` defaulting**: `ClusterQueue.spec.preemption` remains fully intact, retaining its standard kubebuilder defaulting (`+kubebuilder:default={}`) and allowing any value as currently.
   - **Merge outputs of both strategies**: During preemption evaluation in the scheduler, if the annotation is set, the candidate outputs of **both** mechanisms are merged:
     - Candidates selected by classical preemption rules (configured via `spec.preemption`, such as borrowing reclaim and within-ClusterQueue preemption).
     - Candidates selected by `PreemptionConfig` rules (such as topology defragmentation or custom label constraints).
   - **Maximum flexibility and backwards compatibility**: This merged approach allows existing preemption behavior to function uninterrupted while layering new capabilities (like defragmentation). Furthermore, users can fully stop candidates from either mechanism if desired:
     - To stop classical preemption candidates, set `spec.preemption` policies to `Never` (for example, `reclaimWithinCohort: Never` and `withinClusterQueue: Never`).
     - To stop configurable preemption candidates, omit the annotation or specify rules with empty candidate selectors.
   - Candidates from both mechanisms are merged, deduplicated, and ordered using the default ordering rules to satisfy preemptor quota and topology requirements.

2. **Beta+: Mutual Exclusivity & Feature Parity via Formal API Field**
   - In Beta+, `PreemptionConfig` and classical preemption will become **mutually exclusive**, with `PreemptionConfig` providing full **feature parity** with classical preemption (including borrowing reclaim, within-ClusterQueue preemption, and fair sharing).
   - Because `PreemptionConfig` will have full feature parity, running or merging both strategies will no longer be necessary.
   - A formal field will be introduced on `ClusterQueueSpec` (or within a unified preemption configuration section) with validation enforcing that only one strategy is active.
   - The Alpha annotation will be deprecated and removed.

An example of attaching a `PreemptionConfig` to a `ClusterQueue` in Alpha:

```yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: ClusterQueue
metadata:
  name: "cluster-queue-a"
  annotations:
    kueue.x-k8s.io/alpha-preemption-config: "defrag-and-hero-preemption-config"
spec:
  # spec.preemption continues to be defaulted or explicitly configured as today.
  # If desired, classical preemption can be disabled by setting policies to Never.
  preemption:
    reclaimWithinCohort: Any
    withinClusterQueue: LowerPriority
  # ... other ClusterQueue fields ...
```

Rate-limiting guardrails via a separate **PreemptionLimit** cluster-scoped CRD across global, queue, and workload scopes are deferred to [Future Work Ideas](#future-work-ideas) to focus the initial iteration on `PreemptionConfig`.

Success criteria:

1. Cluster administrators are able to configure preemptions in the cluster in a way that satisfies their organization's needs.
2. Workloads are preempted only if allowed by the appropriate preemption config and/or classical `preemption` field (whose candidate outputs are merged in Alpha).
3. Most popular setups are possible, tested, and covered by documentation:
   - Defragmentation
   - Hero jobs

### User Stories

Each of the user stories mentioned in the motivation section can be fulfilled by an appropriate config. Configs for each of them can be found below in the appropriate subsections.

#### Story 1 - Defragmentation

A user can define a config with an `InsufficientTopology` trigger that will allow preemption of workloads blocking specific topologies when scheduling a workload from the associated cluster queue requires it. To avoid "flappy" preemption issues, the rules should be limited in a way that guarantees asymmetry: if A can preempt B, B shouldn't be able to preempt A. This can be done in various ways, for example:

- Only allow preemption of workloads with strictly lower priority.
- Only allow preemption of workloads that require smaller topologies (e.g. using a custom numeric label).
- Only allow preemption of workloads that should be preemptible according to FairSharing rules.

An example config based on priority and number of TPUs can look like this:

```yaml
spec:
  rules:
    - name: defrag-smaller-tpu-workloads
      trigger: "InsufficientTopology"
      candidateSelectors:
        - relativeWorkloadPriority: "LowerOrEqual"
          relationRequirement: "AnyClusterQueue"
          numericLabels:
            - key: "tpus-count"
              relation: "Lower"
              defaultValue: 0
```

As it has an `AnyClusterQueue` relation, it can preempt workloads even if they are not related in any way to the preemptor cluster queue. In combination with a custom numeric label selector using strict `Lower`, this guarantees asymmetry: a larger-topology workload can preempt smaller workloads blocking the required topology domain, but smaller or equal-sized workloads cannot preempt the larger workload in return, preventing mutual preemption loops. Effectively, when the smaller workloads are re-admitted, they can be placed in smaller fragmented domains (where the larger workload cannot fit), thereby defragmenting the cluster.

#### Story 2 - Hero job

This example shows how a hero job's preemption config can be set up. It proposes an exemplary separate preemption config for the hero job's cluster queue, but in practical deployments it should be tailored to the user's needs.

Assumptions:

- The hero job has a higher priority than regular workloads in the cluster,
- The hero job should have elevated privileges to preempt other workloads,
- The hero job is a mission-critical job and should be scheduled as soon as possible,
- The hero job should not be preemptible by any other workload.

This can be achieved by a separate preemption config for the hero job. The config should be referenced by the hero job's cluster queue. The config will have two rules, allowing it to preempt any lower-priority workload across any ClusterQueue for either quota or topology reasons:

```yaml
spec:
  rules:
    - name: hero-reclaim-topology
      trigger: "InsufficientTopology"
      candidateSelectors:
        - relativeWorkloadPriority: "Lower"
          relationRequirement: "AnyClusterQueue"
    - name: hero-reclaim-quota
      trigger: "InsufficientQuota"
      candidateSelectors:
        - relativeWorkloadPriority: "Lower"
          relationRequirement: "AnyClusterQueue"
```

And then to make sure that the hero job is never preempted, one may:

1. Make the hero job's priority higher than any other workload's priority and do not allow preemption of workloads with higher or equal priority.
2. Define in candidate selectors subfield `ClusterQueueSelector` of other preemption configs that they cannot preempt from the hero job's CQ.
3. In future milestones, use a `PreemptionLimit` with 0 allowed preemptions from the hero job's CQ (see [Future Work Ideas](#future-work-ideas)).

Thanks to the elevated preemption privileges, the hero job will be able to preempt any workload and borrow quota from other CQs in the cohort tree (this job will still be affected by lending limits — so they have to be set appropriately to allow for gathering quota). It will also effectively lock this quota, as no other workload will be able to preempt it.

#### Story 3 - Business driven preemption rules

Requested functionalities from the community can be satisfied with the following configurations (with workload priority class selectors in stories 3 & 4 and time-based duration selectors in stories 5 & 6 deferred to [Future Work Ideas](#future-work-ideas)):

1. **Resource requests/limits based preemption (filtering by resource size):**
   Protect large, long-running batch workloads from preemption by ensuring only "small" workloads (e.g. workloads requesting at most 8 GPUs or 32 CPU cores) are eligible as preemption candidates using custom numeric labels with `maxValue`:

   ```yaml
   spec:
     rules:
       - name: preempt-small-resource-workloads
         trigger: "InsufficientQuota"
         candidateSelectors:
           - relationRequirement: "SameClusterQueue"
             relativeWorkloadPriority: "Lower"
             numericLabels:
               - key: "requested-gpus"
                 maxValue: 8
   ```

2. **Advanced topology comparison (matching podset required levels):**
   Ensure preemption only targets workloads that match or fall within specific topology domains or required podset levels (e.g., only preempt workloads constrained to the same `rack` domain) using `workloadSelector` or numeric label relations:

   ```yaml
   spec:
     rules:
       - name: preempt-same-topology-level-workloads
         trigger: "InsufficientTopology"
         candidateSelectors:
           - relationRequirement: "SameCohort"
             relativeWorkloadPriority: "LowerOrEqual"
             workloadSelector:
               matchLabels:
                 kueue.x-k8s.io/topology-level: "rack"
   ```

3. **Priority threshold for within-ClusterQueue preemptions ([Issue #12001](https://github.com/kubernetes-sigs/kueue/issues/12001)):** _(Deferred to [Future Work Ideas](#workload-priority-class-selectors))_
   Restricted preemption within the same ClusterQueue targeting only candidates matching a specific priority class:

   ```yaml
   spec:
     rules:
       - name: preempt-same-cq-low-priority
         trigger: "InsufficientQuota"
         candidateSelectors:
           - relationRequirement: "SameClusterQueue"
             candidateWorkloadPrioritySelector:
               matchLabels:
                 kueue.x-k8s.io/priority-class: "batch-low"
   ```

4. **Priority threshold for reclaim within Cohort ([Issue #12046](https://github.com/kubernetes-sigs/kueue/issues/12046)):** _(Deferred to [Future Work Ideas](#workload-priority-class-selectors))_
   Reclaim borrowed capacity within the cohort only from candidates matching a specific priority class:

   ```yaml
   spec:
     rules:
       - name: reclaim-cohort-quota-from-low-priority
         trigger: "QuotaReclaimRequired"
         candidateSelectors:
           - relationRequirement: "SameCohort"
             quota: "BorrowingCapacityFromPreemptor"
             candidateWorkloadPrioritySelector:
               matchLabels:
                 kueue.x-k8s.io/priority-class: "batch-low"
   ```

5. **Minimal execution duration before preemption ([Issue #9596](https://github.com/kubernetes-sigs/kueue/issues/9596)):** _(Deferred to [Future Work Ideas](#time-based-candidate-selectors-execution-and-creation-duration))_
   Avoid preempting workloads that just started by requiring candidates to have run for a minimum duration (e.g. at least 15 minutes):

   ```yaml
   spec:
     rules:
       - name: preempt-only-after-min-exec-time
         trigger: "InsufficientQuota"
         candidateSelectors:
           - relationRequirement: "SameClusterQueue"
             relativeWorkloadPriority: "Lower"
             minExecutionDuration: "15m"
   ```

6. **SLA protection based on workload creation time:** _(Deferred to [Future Work Ideas](#time-based-candidate-selectors-execution-and-creation-duration))_
   Model SLA requirements by only preempting recently created workloads (e.g., created less than 1 hour ago) to avoid preemption of older workloads nearing SLA completion deadlines:
   ```yaml
   spec:
     rules:
       - name: preempt-recent-workloads-only
         trigger: "InsufficientQuota"
         candidateSelectors:
           - relationRequirement: "SameClusterQueue"
             relativeWorkloadPriority: "Lower"
             maxTimeFromCreationDuration: "1h"
   ```

### Notes

There are many possible extensions of the proposed selectors in the rules. For now, we propose to support only those that seem most common and natural, but the design allows for extensibility.

### Constraints

- **Backward Compatibility & Strategy Merging (Alpha):** `ClusterQueue.spec.preemption` remains fully backward-compatible, retaining its declarative kubebuilder defaulting (`+kubebuilder:default={}`). No new field is added to `ClusterQueueSpec` in Alpha; instead, `PreemptionConfig` is referenced via the `kueue.x-k8s.io/alpha-preemption-config` annotation. The scheduler merges candidate outputs from both classical preemption and `PreemptionConfig`. For Beta+, the two strategies will become mutually exclusive via a formal API field once `PreemptionConfig` provides full feature parity with classical preemption.
- **Deterministic Scheduling:** Candidate selection, victim evaluation, and tie-breaking must remain strictly deterministic across scheduling cycles (guaranteed by multi-key comparison chains and Workload UID tie-breaking).
- **Non-mutating Evaluation:** Preemption evaluation operates strictly on cluster snapshot state and simulated usage without mutating workload specs or priorities during preemption simulation.
- **Resource Scope:** `PreemptionConfig` is a cluster-scoped CRD subject to standard Kubernetes RBAC and controller-runtime caching mechanisms (`PreemptionLimit` is deferred to future work).

### Caveats

Given the extensive nature of **PreemptionConfigs** defined below, the API introduces several complexities where users might inadvertently misconfigure their setup. To maximize user success, the following mitigations will be implemented:

- Deliver concrete examples demonstrating successful configurations.
- Offer detailed scenarios illustrating invalid configurations or potential flapping issues.
- Communicate explicitly that custom rule creation carries inherent risks and is intended for power users — the OSS Kueue community might not be able to support troubleshooting every custom scenario.

### Risks and Mitigations

#### Cascading preemptions due to misconfiguration

One inherent risk is users deploying ill-defined preemption configs that could lead to cluster instability (e.g. cascading preemptions). The design includes the following mitigations:

1. **Restrictive default preemption config** — By default, an empty config does not lead to any preemptions as candidate selection rules will be empty.
2. **Documentation** — Comprehensive documentation will be provided to help users understand the risks and benefits of each configuration option, including examples of common preemption scenarios and how to configure them.
3. **Rate-limiting guardrails** — Cluster administrators can define preemption limits to roll out new configs or rules gradually (deferred to future work as `PreemptionLimit`).

#### Performance degradation

Another risk is preemption performance degradation due to the generic nature of new rules and a potentially large number of selectors. This risk will be mitigated by the implementation of a performance-focused test suite for preemptions.

The test suite will be used to benchmark the new implementation against the existing one to ensure that there is no significant performance degradation for already defined "high-level" policies.
The test suite will also be used to identify performance optimization opportunities for the newly introduced code.

The documentation will also clearly indicate that the creation of a large number of complex preemption rules may have performance implications for overall scheduling, and that it is recommended to benchmark your configs before rolling out to production.

#### Security considerations

As preemption configs will be modifiable only by cluster administrators, there are no additional security risks. Administrators modifying them should be aware of the risks and consequences of misconfiguration in Kueue, which can effectively lead to no workloads being scheduled.

## Design Details

### Proposed API PreemptionConfig

```go
const (
  // AlphaPreemptionConfigAnnotation is the annotation key used on ClusterQueue to reference
  // a PreemptionConfig during Alpha.
  // This annotation is explicitly alpha-level and designated to go away when moving to Beta.
  AlphaPreemptionConfigAnnotation = "kueue.x-k8s.io/alpha-preemption-config"
)

// PreemptionConfigReference is the name of the PreemptionConfig.
// In Alpha, it is specified via the AlphaPreemptionConfigAnnotation on ClusterQueue.
// In Beta+, it will be introduced as a formal field on ClusterQueueSpec.
//
// Validation of a PreemptionConfig name is equivalent to that of object names:
// subdomain in DNS (RFC 1123).
// +kubebuilder:validation:MaxLength=253
// +kubebuilder:validation:Pattern="^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$"
type PreemptionConfigReference string

type PreemptionConfig struct {
  metav1.TypeMeta `json:",inline"`
  metav1.ObjectMeta `json:"metadata,omitempty"`
  Spec PreemptionConfigSpec `json:"spec,omitempty"`
}

type PreemptionConfigSpec struct {
  // Rules to select preemption candidates.
  //
  // +listType=map
  // +listMapKey=name
  Rules []PreemptionRule `json:"rules"`
}

// +kubebuilder:validation:Enum=InsufficientQuota;QuotaReclaimRequired;InsufficientTopology
type PreemptionRuleTrigger string

const (
  // InsufficientQuota means that there was an attempt to admit the workload,
  // but there was not enough unused quota in the ClusterQueue or its Cohort to accommodate the Workload.
  InsufficientQuota PreemptionRuleTrigger = "InsufficientQuota"

  // QuotaReclaimRequired means that there was an attempt to admit the workload
  // and workload should be admissible according to nominal quota of the ClusterQueue,
  // but it cannot as quota was borrowed. Thereby, quota will have to be reclaimed before this workload is scheduled.
  QuotaReclaimRequired PreemptionRuleTrigger = "QuotaReclaimRequired"

  // InsufficientTopology means that there was an attempt to admit the workload,
  // quota was available, but no topology domain satisfied its requirements.
  // Unlike quota-related conditions, this condition is only reset on admission, as it is checked only after quota is available for the workload.
  InsufficientTopology PreemptionRuleTrigger = "InsufficientTopology"
)

// PreemptionRule defines a single rule under which preemptions can be triggered
// and the candidate workloads eligible for preemption.
type PreemptionRule struct {
  // Name is the identifier of the preemption rule.
  //
  // +kubebuilder:validation:Required
  // +kubebuilder:validation:MaxLength=63
  // +kubebuilder:validation:Pattern="^[a-z0-9]([-a-z0-9]*[a-z0-9])?$"
  Name string `json:"name"`

  // MatchingPreemptorWorkloads is a label selector indicating which workloads can trigger preemptions
  // using this rule. Accepts all workloads if not set.
  //
  // +optional
  MatchingPreemptorWorkloads *metav1.LabelSelector `json:"matchingPreemptorWorkloads,omitempty"`

  // Trigger specifies the condition (InsufficientQuota, QuotaReclaimRequired, or InsufficientTopology)
  // that must be observed on the preemptor workload for this rule to apply.
  //
  // +kubebuilder:validation:Required
  Trigger PreemptionRuleTrigger `json:"trigger"`

  // CandidateSelectors specifies the selection rules for workloads that are candidates for preemption.
  // Candidates resulting from multiple selectors are summed into one set.
  // No selectors result in an empty candidate set, thereby disallowing any preemptions with this rule.
  //
  // +optional
  CandidateSelectors []PreemptionCandidateSelector `json:"candidateSelectors,omitempty"`
}
```

The trigger state and the first observation timestamp when a specific trigger occurred are maintained in-memory within the queue management and scheduling cache (rather than being patched as status conditions on the Workload API object). The in-memory trigger state is cleared upon successful admission of the workload, when the workload is deleted or evicted, or when the trigger condition is no longer true (for instance, when enough quota becomes freed to admit the workload directly without preemption).

The supported in-memory trigger types are:
- `InsufficientQuota`: The ClusterQueue or Cohort does not have enough unused quota to admit the workload directly.
- `QuotaReclaimRequired`: The workload cannot be scheduled because nominal quota was borrowed by cohort members; reclaiming this quota from borrowers is required.
- `InsufficientTopology`: Quota is available, but no topology domain satisfies the workload's topology requirements (TAS).

By maintaining triggers in-memory, the scheduler avoids etcd write amplification, eliminates informer watch propagation latency between scheduling cycles, and prevents duplicate API patch conflicts, while providing the foundation to support deferred duration-based rules (`MinTriggerRequiredDuration`) in future iterations.

```go

// PreemptionRelationConstraint specifies the relational boundary between
// the preempting workload's queue and candidate workloads' queues.
// Possible values are:
// - "SameLocalQueue": restricts preemption candidates to workloads submitted to the exact same LocalQueue (matching name and namespace).
// - "SameClusterQueue": restricts preemption candidates to workloads submitted to the same ClusterQueue as the preemptor.
// - "SameCohort": restricts preemption candidates to workloads in ClusterQueues that share the exact same immediate direct Cohort, as well as workloads in the preemptor's own ClusterQueue (even if standalone).
// - "SameCohortTree": restricts preemption candidates to workloads in ClusterQueues that belong to the same Cohort Tree (sharing the same root ancestor Cohort), as well as workloads in the preemptor's own ClusterQueue (even if standalone).
// - "AnyClusterQueue": places no relationship restrictions on preemption candidates.
//
// +kubebuilder:validation:Enum=SameLocalQueue;SameClusterQueue;SameCohort;SameCohortTree;AnyClusterQueue
type PreemptionRelationConstraint string

const (
  // SameLocalQueue restricts preemption candidates to workloads submitted
  // to the exact same LocalQueue (matching name and namespace).
  SameLocalQueue PreemptionRelationConstraint = "SameLocalQueue"

  // SameClusterQueue restricts preemption candidates to workloads submitted
  // to the same ClusterQueue as the preemptor.
  SameClusterQueue PreemptionRelationConstraint = "SameClusterQueue"

  // SameCohort restricts preemption candidates to workloads in ClusterQueues
  // that share the exact same immediate direct Cohort, as well as workloads in the
  // preemptor's own ClusterQueue (even if standalone and lacking a parent cohort).
  SameCohort PreemptionRelationConstraint = "SameCohort"

  // SameCohortTree restricts preemption candidates to workloads in ClusterQueues
  // that belong to the same Cohort Tree (sharing the same root ancestor Cohort),
  // as well as workloads in the preemptor's own ClusterQueue (even if standalone and lacking a parent cohort).
  SameCohortTree PreemptionRelationConstraint = "SameCohortTree"

  // AnyClusterQueue places no relationship restrictions on preemption candidates.
  AnyClusterQueue PreemptionRelationConstraint = "AnyClusterQueue"
)


// +kubebuilder:validation:Enum=BorrowingCapacityFromPreemptor;DRSLessThanOrEqualToFinalShare;DRSLessThanInitialShare;DRSAllStrategies
type QuotaConstraint string

const (
  BorrowingCapacityFromPreemptor QuotaConstraint = "BorrowingCapacityFromPreemptor"
  DRSLessThanOrEqualToFinalShare QuotaConstraint = "DRSLessThanOrEqualToFinalShare"
  DRSLessThanInitialShare QuotaConstraint = "DRSLessThanInitialShare"
  DRSAllStrategies QuotaConstraint = "DRSAllStrategies"
)


// PreemptionCandidateSelector defines the selection criteria for workloads that are candidates for preemption.
type PreemptionCandidateSelector struct {
  // RelationRequirement specifies the queue or cohort relation boundary to the preemptor workload.
  //
  // +kubebuilder:validation:Required
  RelationRequirement PreemptionRelationConstraint `json:"relationRequirement"`

  // Quota specifies quota-based preemption constraints (e.g., borrowing capacity or fair sharing share).
  // Cannot be set if RelationRequirement is SameLocalQueue or SameClusterQueue.
  // Accepts all if not set.
  //
  // +optional
  Quota *QuotaConstraint `json:"quota,omitempty"`

  // NumericLabels defines rules for filtering candidates using custom numeric labels on the Workload resource.
  // Multiple numeric labels are joined using AND-rule (all have to be satisfied).
  // Accepts all if not set.
  //
  // +optional
  NumericLabels []NumericLabelConstraint `json:"numericLabels,omitempty"`

  // ClusterQueueSelector defines label selector constraints on candidate ClusterQueues.
  // Accepts all if not set.
  //
  // +optional
  ClusterQueueSelector *metav1.LabelSelector `json:"clusterQueueSelector,omitempty"`

  // WorkloadSelector defines label selector constraints on candidate Workloads.
  // Accepts all if not set.
  //
  // +optional
  WorkloadSelector *metav1.LabelSelector `json:"workloadSelector,omitempty"`

  // RelativeWorkloadPriority defines how the candidate's priority compares to the preemptor's priority.
  // For example "Lower" means that only workloads with lower
  // priority will be allowed as preemption candidates.
  // The comparison is made using effective priority (accounting for priority boost if enabled).
  // If nil, no relative priority check is enforced.
  //
  // +optional
  RelativeWorkloadPriority *RelativeConstraint `json:"relativeWorkloadPriority,omitempty"`
}



// NumericLabelConstraint describes the rule for filtering a custom numerical label.
// For example, this can be used to filter candidates based on the label describing the
// required topology domain size, such as the "number of TPUs".
// If a user has a label "number-of-tpus" that describes the number of TPUs required in a single cube,
// it can be used to create a rule that selects only workloads requiring smaller cube slices
// by defining relation: "Lower". Such a configuration would allow preemption of "smaller" workloads,
// to achieve better cluster utilization and decrease fragmentation.
// Please note that those labels are not copied out of the box from job-like objects.
// You should remember to append the designated labels to the list of labels
// copied to the workload via the Kueue main configuration
// if you wish to use a custom label.
type NumericLabelConstraint struct {
  // Key is the label key that stores the integer value in the workload that will
  // be used for candidate selection.
  //
  // +kubebuilder:validation:Required
  // +kubebuilder:validation:MaxLength=316
  Key string `json:"key"`

  // DefaultValue is used when a workload does not have the label key
  // or the value under the key cannot be parsed as an integer.
  // If not specified, workloads without the label or
  // with a label value not parsable as int are treated as incomparable,
  // and therefore excluded from preemption candidates.
  // +optional
  DefaultValue *int32 `json:"defaultValue,omitempty"`

  // Relation defines how the candidate's label value compares to the preemptor's.
  // +optional
  Relation *RelativeConstraint `json:"relation,omitempty"`

  // MinValue specifies the lowest label value a candidate workload can have to be considered for preemption.
  // +optional
  MinValue *int32 `json:"minValue,omitempty"`

  // MaxValue specifies the highest label value a candidate workload can have to be considered for preemption.
  // +optional
  MaxValue *int32 `json:"maxValue,omitempty"`
}

// RelativeConstraint defines how a specified numeric property (e.g., effective priority) of the candidate compares to the same property of the preemptor.
// Possible values are:
// - "Lower": permits preemption if candidate field value < preemptor field value
// - "Greater": permits preemption if candidate field value > preemptor field value
// - "LowerOrEqual": permits preemption if candidate field value <= preemptor field value
// - "GreaterOrEqual": permits preemption if candidate field value >= preemptor field value
// +kubebuilder:validation:Enum=Lower;Greater;LowerOrEqual;GreaterOrEqual
type RelativeConstraint string

const (
  // Lower permits preemption if candidate field value < preemptor field value
  Lower RelativeConstraint = "Lower"
  // Greater permits preemption if candidate field value > preemptor field value
  Greater RelativeConstraint = "Greater"
  // LowerOrEqual permits preemption if candidate field value <= preemptor field value
  LowerOrEqual RelativeConstraint = "LowerOrEqual"
  // GreaterOrEqual permits preemption if candidate field value >= preemptor field value
  GreaterOrEqual RelativeConstraint = "GreaterOrEqual"
)

// Kueue uses full, descriptive identifiers ("Lower", "Greater", "LowerOrEqual", "GreaterOrEqual").
// This maintains consistency with equality comparisons, enhances YAML readability, and provides
// clear, intuitive semantics for cluster administrators.

```

#### Default Candidate Ordering

In the initial iteration, candidate workloads are evaluated and ordered using the default ordering rules for classical preemption and fair sharing (reusing the logic from [`pkg/scheduler/preemption/common/ordering.go`](../../pkg/scheduler/preemption/common/ordering.go#L34-L41)):

0. Workloads already marked for preemption/eviction first (`isEvicted`).
1. Workloads from other ClusterQueues in the cohort before the ones in the same ClusterQueue as the preemptor.
2. (AdmissionFairSharing only) Workloads with lower LocalQueue's usage first.
3. Workloads with lower priority first (accounting for effective priority and priority boost if enabled).
4. Workloads admitted more recently first (protecting long-running workloads, matching classical Kueue).
5. Workload UID as tie-breaker for deterministic sorting.

Configurable candidate ordering via an `Ordering` field is deferred to [Future Work Ideas](#future-work-ideas).

### Preemption evaluation flow in scheduler

The preemption evaluation flow integrates trigger condition tracking, upper-bound feasibility checks, ordered candidate evaluation (until quota and topology conditions are satisfied), and reverse-order victim backfilling across scheduling cycles:

```mermaid
flowchart TD
    subgraph Cycle1 ["1. Initial Cycle: Nomination & In-Memory Trigger Tracking"]
        A["Queue Heads Retrieved<br/>(queues.Heads)"] --> B["Nominate Workloads<br/>(nominate)"]
        B --> C["Order Entries into Iterator"]
        C --> D["Process Entry<br/>(processEntry)"]
        D --> E{"Workload Fits Directly?"}
        E -->|Yes| F["Admit Workload<br/>(admit)"]
        E -->|No| G["Record Trigger State & Observation Timestamp<br/>in Queue Memory Cache<br/>(InsufficientQuota / QuotaReclaimRequired / InsufficientTopology)"]
        G --> H["Requeue Workload<br/>(Immediate requeue to active heap)"]
    end

    subgraph CycleN ["2. Subsequent Cycles: Preemption Evaluation in getInitialAssignments"]
        H -.->|Next Scheduling Cycle| I["Consider Workload in Subsequent Cycle<br/>(nominate -> getInitialAssignments)"]
        I --> J["Evaluate In-Memory Triggers<br/>(PreemptionEvaluator)"]
        J --> K{"Is Any Trigger Satisfied?<br/>(Matching trigger observed in memory)"}
        K -->|No| L["Preemption Bypassed<br/>(No matching trigger)"]
        K -->|Yes| M["Upper-Bound Feasibility Check<br/>(CandidatesQuotaAndTopologyUpperLimit)"]
        M --> N{"Preemptor Fits if ALL<br/>Candidates Preempted?"}
        N -->|No| O["Preemption Infeasible<br/>(Preemptor cannot fit even with all candidates)"]
        N -->|Yes| P["Merge Candidate Outputs<br/>(Classical spec.preemption + PreemptionConfig)<br/>& Deduplicate"]
        P --> P2["Order Candidates<br/>(Sort per default preemption ordering)"]

        P2 --> Q["Candidate Selection Loop"]
        Q --> R["Take Next Candidate in Order"]
        R --> S["Add Candidate to Preemption Targets<br/>& Update Simulated Resources"]
        S --> T{"Preemptor Quota &<br/>Topology Needs Satisfied?"}
        T -->|No| U{"More Candidates?"}
        U -->|Yes| R
        U -->|No| V["Preemption Incomplete<br/>(Cannot satisfy requirements)"]

        T -->|Yes| W["Victim Backfilling<br/>(Test selected targets in REVERSED order)"]
        W --> X["For each victim in reverse order:<br/>Can preemptor fit WITHOUT preempting this victim?"]
        X --> Y{"Preemptor Still Fits?"}
        Y -->|Yes| Z["Remove victim from preemption targets<br/>(Backfill / preserve workload)"]
        Y -->|No| AA["Retain victim in preemption targets"]
        Z --> AB{"More victims to test?"}
        AA --> AB
        AB -->|Yes| X
        AB -->|No| AC["Final Preemption Targets Determined"]
    end

    subgraph Execution ["3. Preemption Execution in processEntry"]
        AC --> AD["Process Entry<br/>(processEntry in Preempt mode)"]
        AD --> AE{"Targets Overlapping or<br/>Workload No Longer Fits?"}
        AE -->|Yes| AF["Mark Skipped / Requeue"]
        AE -->|No| AG["Issue Preemptions<br/>(issuePreemptions)"]
        AG --> AH["Requeue Preemptor<br/>(Wait for victims to terminate)"]
    end

    subgraph Admission ["4. Admitting Cycle: Workload Admission in Subsequent Cycle"]
        AH -.->|Victims terminate & capacity freed| AI["Re-evaluate Preemptor in Next Cycle<br/>(nominate -> processEntry)"]
        AI --> AJ{"Preemptor Fits Directly?"}
        AJ -->|Yes| AK["Admit Preemptor Workload<br/>(admit)"]
        AJ -->|No| AL["Requeue / Re-evaluate Preemption"]
    end

    style Cycle1 fill:#f8f9fa,stroke:#6c757d,stroke-width:2px
    style CycleN fill:#eef6fc,stroke:#0d6efd,stroke-width:2px
    style Execution fill:#fff3cd,stroke:#ffc107,stroke-width:2px
    style Admission fill:#e8f5e9,stroke:#198754,stroke-width:2px
    style E fill:#fff3cd,stroke:#ffc107
    style K fill:#fff3cd,stroke:#ffc107
    style N fill:#fff3cd,stroke:#ffc107
    style T fill:#fff3cd,stroke:#ffc107
    style U fill:#fff3cd,stroke:#ffc107
    style Y fill:#fff3cd,stroke:#ffc107
    style AB fill:#fff3cd,stroke:#ffc107
    style AE fill:#fff3cd,stroke:#ffc107
    style AJ fill:#fff3cd,stroke:#ffc107
    style AK fill:#d1e7dd,stroke:#0f5132,stroke-width:2px
    style F fill:#d1e7dd,stroke:#0f5132,stroke-width:2px
```

#### Step-by-Step Breakdown

1. **Nomination & In-Memory Trigger Tracking (Cycle 1)**:
   - In `nominate()`, initial resource flavor requirements are calculated for all active queue heads.
   - In `processEntry()`, each entry is processed:
     - If the workload fits directly, it proceeds to admission (`admit()`).
     - If the workload cannot fit directly (e.g. requires preemption or lacks resources/topology), `processEntry()` detects the active triggers (`InsufficientQuota`, `QuotaReclaimRequired`, or `InsufficientTopology`) and records their initial observation in memory within the queue manager / scheduler cache.
     - The workload is requeued immediately to the active heap (`immediate = true`), allowing it to be evaluated for preemption on the very next scheduling pass without waiting for API patches or watch delivery.

2. **Trigger & Preemption Evaluation (`PreemptionEvaluator`)**:
   - In subsequent scheduling cycles (immediately on the next tick), `getInitialAssignments()` queries `PreemptionEvaluator` to check whether the active trigger condition matches any applicable preemption rule.
   - If no trigger is satisfied, preemption is bypassed for this cycle, allowing the workload to continue waiting or be requeued.
   - If triggers are satisfied but no preemption candidates exist in the cluster (e.g. all running workloads have higher priority), the workload is moved to `inadmissibleWorkloads` to prevent infinite busy-looping.

3. **Upper-Bound Feasibility Check (`CandidatesQuotaAndTopologyUpperLimit`)**:
   - If an applicable trigger is met, the scheduler performs an upper-bound check using `CandidatesQuotaAndTopologyUpperLimit` by simulating the removal of all matching candidate workloads.
   - If the preemptor cannot fit even when all candidates are preempted, the evaluation terminates early.

4. **Candidate Gathering & Strategy Merging (Alpha)**:
   - In Alpha, candidates are gathered by evaluating both preemption mechanisms:
     - **Classical Preemption**: Evaluates candidates according to `cq.Spec.Preemption` policies (e.g. workloads borrowing from the preemptor's ClusterQueue, or lower-priority workloads in the same CQ or cohort).
     - **Configurable Preemption**: Evaluates candidates matching the rules and candidate selectors of the `PreemptionConfig` referenced by the `kueue.x-k8s.io/alpha-preemption-config` annotation (subject to matching triggers).
   - The candidate outputs of both strategies are **merged and deduplicated** into a single candidate set ($C_{\text{merged}} = C_{\text{classical}} \cup C_{\text{config}}$).
   - This provides maximum flexibility: users can run both strategies concurrently, or fully stop candidates from either mechanism (e.g., setting `reclaimWithinCohort: Never` and `withinClusterQueue: Never` disables classical candidates, while omitting the annotation disables configurable preemption candidates).

5. **Ordered Candidate Iteration (Quota & Topology Satisfaction)**:
   - Candidates in the merged set are sorted based on the default preemption ordering rules (reusing classical preemption and fair sharing ordering logic).
   - The scheduler iterates through candidate workloads in order, adding victims until the preemptor's resource quota and topology domain requirements are fully satisfied.

6. **Reverse-Order Victim Backfilling**:
   - Once a viable candidate set `[V_1, V_2, ..., V_k]` is assembled, the scheduler attempts backfilling by checking victims in reverse order, from `V_k` down to `V_1`.
   - For each victim, the scheduler evaluates whether the preemptor can still fit without evicting that victim. If the preemptor still fits, the victim is removed from the preemption target list, minimizing unnecessary workload disruptions.

7. **Execution (`issuePreemptions`)**:
   - In `processEntry()`, after checking for target overlap with earlier cycle decisions, `issuePreemptions()` issues evictions for the final victim set and requeues the preemptor workload, setting status conditions indicating preemption is pending.

8. **Admission in Follow-up Cycle (`admit`)**:
   - Preemption is asynchronous: the preemptor cannot be admitted immediately while victim pods are terminating.
   - Once all evicted victim workloads complete termination and release their quota and topology allocations, the preemptor is evaluated in a subsequent scheduling cycle. In this cycle, the preemptor fits directly within available capacity and proceeds to admission (`admit()`).

`CandidatesQuotaAndTopologyUpperLimit` by design is just an approximation to allow for short-circuiting when the preemptor obviously will not be admitted anyway. It will just use the initial state of the `PreemptionEvaluator` and does not attempt to simulate changes in DRS or borrowing during iteration over candidates. However, the returned values should always be greater than or equal to what can be preempted at this moment, so it is reasonable to avoid heavy simulation if the result is smaller than the requested amount.

### Candidate Organization: Per-Selector, Per-CQ Priority Queues

In the initial iteration of `PreemptionConfig`, candidate workloads are evaluated according to the default preemption ordering rules (reusing the established ordering logic from classical preemption and fair sharing: workloads marked for eviction first, workloads from other ClusterQueues before the preemptor's ClusterQueue, fair-sharing usage, priority, admission timestamp, and UID tiebreaker).

Rather than pooling all candidate workloads across the cluster into a single unstructured list, the evaluator organizes candidate workloads into **separate priority queues partitioned by `(CandidateSelector, ClusterQueue)`**.

#### Architectural Groundwork for Configurable Candidate Ordering

The sophisticated dynamic candidate iteration algorithm described in [Efficient Iteration Through Candidates in Preemption Order](#efficient-iteration-through-candidates-in-preemption-order) is only strictly relevant when introducing [Configurable Candidate Ordering](#configurable-candidate-ordering) (deferred to future work), where distinct comparator chains and dynamic multi-queue iteration come into play. However, it sets the ground as to why we should already adopt **Per-Selector, Per-CQ Priority Queues** in the first iteration:

1. **Enabling Future Integration**: When configurable candidate ordering is introduced in future iterations, candidate workloads will need to be evaluated and compared across distinct queues dynamically according to user-defined comparator chains. Establishing per-selector, per-CQ queue data structures in the initial release ensures that configurable ordering can be integrated seamlessly without re-architecting Kueue's candidate selection pipeline.
2. **Static Intra-Queue Ordering (Sort Once)**: Within any given ClusterQueue, relative candidate ordering under the default rules is static and unaffected by dynamic cluster state. Sorting each queue independently once at the start of preemption evaluation ($O(\frac{n}{c} \log \frac{n}{c})$ per queue) avoids expensive full-array re-sorting during candidate iteration.
3. **Fast CQ-Level Pruning**: Dynamic cluster properties—such as current borrowed quota—can be tracked at the queue level. When a ClusterQueue exhausts its borrowing capacity, its entire priority queue under that borrowing selector is immediately pruned from consideration.
4. **Selector Isolation**: Maintaining distinct queues per selector ensures that dropping an ineligible queue under a borrowing selector does not inadvertently discard candidates from the same ClusterQueue that remain eligible under static selectors (such as priority-only preemption within the same CQ).
5. **Deduplication & Preemption Justification**: Workloads matching multiple selectors (across one or more rules) reside at the heads of multiple queues (held via shared references) and are popped simultaneously when selected. This multi-queue membership directly identifies all matching candidate selectors and rules, providing precise metadata for preemption justification in workload status conditions and audit logs (see [Observability](#observability)).


### Observability

As new preemptions may be far more complex than the existing classical model, it may be non-trivial to judge why a workload was preempted just by looking at the cluster queue resource. Therefore, we need to add more visibility into preemption reasons. To satisfy this need, details about the eviction will be written to the `WorkloadSchedulingStatsEviction` structure in the `Workload` status.
Reason will be set to `ConfigurablePreemption` to indicate that the new mechanism was used for preemption. The `UnderlyingCause` will be filled with the following information up to the maximum characters:

- preemptor workload reference,
- preemption config name, rule name, and selector indices which resulted in choosing this workload as a candidate.

Example message:
`Preempted by <preemptor> because of preemption config <preemptionConfig> rule <ruleName>/<selectorIndex>`

In case of multiple selectors which are triggered within one rule, they will be concatenated with ",".

Example message:
`Preempted by <preemptor> because of preemption config <preemptionConfig> rule <ruleName_1>/<selectorIndex_1>,<selectorIndex_2>,...,<selectorIndex_n>; <ruleName_2>/<selectorIndex_1>,<selectorIndex_2>,...,<selectorIndex_n>; ...`

New preemptions will overwrite the previous underlying cause but increase the eviction count for this reason.

### Test Plan

<!--
**Note:** *Not required until targeted at a release.*
The goal is to ensure that we don't accept enhancements with inadequate testing.

All code is expected to have adequate tests (eventually with coverage
expectations). Please adhere to the [Kubernetes testing guidelines][testing-guidelines]
when drafting this test plan.

[testing-guidelines]: https://git.k8s.io/community/contributors/devel/sig-testing/testing.md
-->

[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.

The test plan is focused on `PreemptionConfig` (`PreemptionLimit` is deferred to future work).

#### Unit tests

1. Trigger tracking — trigger states and observation timestamps are correctly recorded in memory when a workload cannot be admitted for a particular reason, preserved across requeues, and cleared upon admission or when resources become available.
2. Preemption Evaluator:
   - Uses only rules that are applicable according to the trigger.
   - Orders candidates according to default preemption ordering rules (reusing classical preemption and fair sharing ordering logic).
   - Collects candidates from multiple rules and deduplicates.
   - Updates DRS and borrowing information dynamically — filtering out candidates that
     should no longer be selected according to DRS/Borrowing selectors.
   - Tests for each candidate selector.
3. New preemptions are only considered when the feature gate is enabled.

The majority of the code will be in the `scheduler/preemption` package; a new subpackage with configurable preemptions will be created there.

Small parts of the implementation like in-memory trigger tracking or integration with the scheduler itself will be done in other packages and accompanied with appropriate unit tests.

#### Integration tests

1. New configurable preemptions are used when the `ConfigurablePreemptions` feature gate is enabled and a preemption config is specified for a cluster queue (old preemptions are covered by existing tests).
2. Pre-made config tests satisfying the main user stories — defrag and hero jobs.
3. Dedicated preemption performance test suite — to compare the performance of the new implementation with the existing implementation for identical configurations.

#### e2e tests

1. Cluster admin can create a preemption config and use it to preempt workloads.
2. Batch users cannot modify the preemption config, but their workloads follow rules defined in configs attached to the CQ.
3. Different CQs can use different preemption configs.
4. Preexisting classical/fair sharing preemptions can be mixed with new preemption configs.

### Graduation Criteria

#### Alpha

- `PreemptionConfig` CRD is implemented with preemption rules.
- `ClusterQueue` references `PreemptionConfig` via the `kueue.x-k8s.io/alpha-preemption-config` annotation, without introducing a new field to `ClusterQueueSpec`.
- `ClusterQueue.spec.preemption` declarative defaulting (`+kubebuilder:default={}`) is preserved intact.
- Preemption evaluator merges candidate outputs from classical preemption (`spec.preemption`) and configurable preemption (`PreemptionConfig`), allowing users to combine or selectively stop candidates from either mechanism.
- Workloads can be preempted according to rules defined in the preemption config.
- Workloads that are preempted have the rule that triggered the preemption added in the eviction condition.
- Lazy defragmentation use case is covered by available configuration rules.

#### Beta

- Feature parity: `PreemptionConfig` covers all existing classical and fair sharing preemption use cases (reclaim within cohort, within cluster queue, borrowing preemption, fair sharing), alongside defragmentation and hero jobs.
- Mutual exclusivity: `PreemptionConfig` and classical `preemption` become mutually exclusive; candidate merging is removed in favor of exclusive strategy execution.
- API promotion: `PreemptionConfig` reference is promoted to a formal field in `ClusterQueueSpec`, and the Alpha annotation is deprecated and designated for removal.
- No significant performance regression for existing preemptions translated to new preemption configs.
- All of the [Open Challenges](#open-challenges) regarding dynamic DRS state re-evaluation and backfilling fairness are addressed.
- Public documentation explains configurable preemptions and documents the rules and triggers. Examples of recommended preemption configs are available for users. Common pitfalls are documented, and the documentation includes suitable warnings that this is an advanced topic and can lead to continuous preemptions if used inappropriately.

#### Stable

TBD

<!--

Clearly define what it means for the feature to be implemented and
considered stable.

If the feature you are introducing has high complexity, consider adding graduation
milestones with these graduation criteria:
- [Maturity levels (`alpha`, `beta`, `stable`)][maturity-levels]
- [Feature gate][feature gate] lifecycle
- [Deprecation policy][deprecation-policy]

[feature gate]: https://git.k8s.io/community/contributors/devel/sig-architecture/feature-gates.md
[maturity-levels]: https://git.k8s.io/community/contributors/devel/sig-architecture/api_changes.md#alpha-beta-and-stable-versions
[deprecation-policy]: https://kubernetes.io/docs/reference/using-api/deprecation-policy/
-->

## Implementation History

Proposed implementation approach:

**Step 1.** `PreemptionConfig` foundations and Defrag use case:

Implementation of the foundations of PreemptionConfig:

- candidate ordering reusing classical preemption ordering logic
- triggers
- candidate organization into per-selector, per-CQ priority queues using default preemption ordering
- ClusterQueue integration via `kueue.x-k8s.io/alpha-preemption-config` annotation
- preemption evaluator support for merging candidate outputs from classical preemption (`spec.preemption`) and `PreemptionConfig`

Implementation of the following candidate selector fields and constraints to have an MVP of defrag:

- `NumericLabels` (`NumericLabelConstraint`)
- `RelativeWorkloadPriority` (`RelativeConstraint`)
- `RelationRequirement` (`PreemptionRelationConstraint`)

Expose the implementation under feature gate "ConfigurablePreemptions", integration should not change in any way the existing preemption logic.

**Step 2.** Implement fair sharing and borrowing based rules, integrating with fair sharing candidate ordering.

Create performance test suite for preemptions to validate current implementation.

**Step 3.** Reimplement existing classical and fair sharing rules using the new API to achieve full feature parity, enforce mutual exclusivity between strategies, introduce a formal API field on `ClusterQueueSpec` for Beta, and retire the Alpha annotation.

**Step 4.** Implement additional candidate selectors (time-based execution and creation duration selectors, workload priority class selectors) and minimum trigger duration (`minTriggerRequiredDuration`).

**Step 5.** Implement configurable candidate ordering (`ordering:` comparator chains and dynamic multi-queue candidate iteration).

**Step 6.** Future design and implementation of preemption rate limiting (`PreemptionLimit`).

<!--
Major milestones in the lifecycle of a KEP should be tracked in this section.
Major milestones might include:
- the `Summary` and `Motivation` sections being merged, signaling SIG acceptance
- the `Proposal` section being merged, signaling agreement on a proposed design
- the date implementation started
- the first Kubernetes release where an initial version of the KEP was available
- the version of Kubernetes where the KEP graduated to general availability
- when the KEP was retired or superseded
-->

## Drawbacks

**Complexity of the solution.**

As proposed configurations are targeting various use cases, the API and the implementation will be much more complex than just adding simple fields targeting specific use cases, e.g. "canPreemptAll". However, providing a single centrally designed and consistent API will make the implementation and configuration more flexible, composable, and easier to maintain than creating multiple specialized solutions.

<!--
Why should this KEP _not_ be implemented?
-->

## Alternatives

1. Periodic defragmentation process that moves workloads to make larger topological domains available.
   Ruled out because:
   - It will not satisfy other user needs like hero jobs and SLA-aware preemptions.
   - It can lead to unnecessary preemptions and therefore wasted cluster resources if the defragmentation process "moves" the workload to a more suitable spot, but a workload in need of the freed topology domain does not arrive before the workload finishes.

2. Uber cluster queues as a separate CRD with elevated permissions to preempt any workload and without quota limits.
   Ruled out because:
   - It would bring excessive complexity to the system and would not fulfill other user needs.
   - It would be harder to maintain as it would require "dual" handling of Kueue preemption and quota computation logic.
   - Depending on the exact API, it can be harder for users to migrate to, as they probably already have some form of "uber" cluster queues if they really need them.

3. Additional preemption related fields in **ClusterQueueSpec** like selector of queues from which it can preempt, preempted workload execution duration, etc.
   Ruled out because:
   - It will lead to inconsistencies between cluster queues.
   - It will make preemption rules maintenance harder.
   - It will not allow defining fine-grained global preemption limits.

4. Consolidation of **PreemptionConfig** and **PreemptionLimit** into a single CRD.
   Ruled out because:
   - It will not allow limiting preemptions globally across cluster queues.
   - It will make configurations like "this cluster queue should never be preempted" unintuitive.
   - It will make limits across different configs harder to maintain or infeasible at all.

5. Adding a `preemptionConfigName` field to `ClusterQueueSpec` in Alpha and requiring `spec.preemption: null` (or merged semantics).
   Ruled out because:
   - `ClusterQueue.spec.preemption` has declarative defaulting (`+kubebuilder:default={}`). Setting it to `null` or altering declarative defaulting in a mutating webhook is a breaking change for existing clients and manifests.
   - If a formal field `spec.preemptionConfigName` were added in Alpha with merged behavior alongside `spec.preemption`, changing it to mutually exclusive in Beta would be a breaking change to the field's semantics.
   - Using an explicit Alpha annotation (`kueue.x-k8s.io/alpha-preemption-config`) avoids creating a premature field contract while allowing the outputs of both strategies to be merged cleanly for Alpha. When `PreemptionConfig` reaches full feature parity in Beta, both strategies can be made mutually exclusive via a formal API field without breaking backward compatibility.

6. Persisting trigger conditions directly on the Workload API object via status condition patches (`Workload.Status.Conditions`).
   Ruled out because:
   - **Informer Watch Latency & Desync**: Writing a condition to etcd via `PatchAdmissionStatus()` and waiting for the informer watch event to update the scheduler's local cache introduces significant latency (tens of milliseconds) compared to the sub-millisecond scheduling cycle. If a workload is requeued immediately for evaluation in the next cycle, the scheduler pops the stale, unpatched object from cache, leading to scheduling failures, high latency, or race conditions.
   - **Duplicate API Patches & Conflicts**: Because informer watch delivery is asynchronous, re-queuing the workload immediately while the watch event is in flight causes the scheduler to re-evaluate the workload repeatedly against stale cache state, generating duplicate status patch requests and triggering API server conflict errors (`409 Conflict`).
   - **Inadmissible Trapping vs. Infinite Busy-Loops**: If workloads requiring preemption were marked inadmissible after setting the condition, they would become stuck in `inadmissibleWorkloads` indefinitely because informer condition updates only update inadmissible workloads in place without re-queuing them to the active heap (unless an unrelated cluster event triggers `QueueInadmissibleWorkloads`). Conversely, keeping them in the active queue without conditions causes infinite busy-loops when preemption candidates do not exist in the cluster.
   - **etcd Churn & Scalability**: Updating status conditions in etcd on every unadmitted scheduling pass creates severe write amplification and API server pressure, particularly in busy clusters with high workload arrival rates and short scheduling intervals.
   - **Conclusion**: Maintaining triggers and observation timestamps in-memory within the queue management and scheduler cache eliminates informer watch latency, avoids etcd write churn and duplicate API patches, and allows immediate requeuing to the active heap (while laying the groundwork for timer-based requeuing for deferred `MinTriggerRequiredDuration` rules in future iterations).

## Future Work Ideas

### Configurable Candidate Ordering

In the initial iteration of `PreemptionConfig`, candidate workloads are ordered strictly by reusing the default ordering rules from classical preemption and fair sharing (as defined in `pkg/scheduler/preemption/common/ordering.go`):

0. Workloads already marked for preemption first (`isEvicted`).
1. Workloads from other ClusterQueues in the cohort before the ones in the same ClusterQueue as the preemptor.
2. (AdmissionFairSharing only) Workloads with lower LocalQueue fair sharing usage first.
3. Workloads with lower priority first (effective priority).
4. Workloads admitted more recently first (protecting long-running workloads).
5. Workload UID as tie-breaker for deterministic sorting.

In future iterations, we plan to reintroduce the `Ordering` field in `PreemptionConfigSpec` to allow cluster administrators to configure custom multi-key ordering comparator chains.

#### Proposed API for Custom Ordering

```go
type PreemptionConfigSpec struct {
  // Rules to select preemption candidates.
  Rules []PreemptionRule

  // Ordering of preemption candidates evaluated sequentially as a multi-key comparator chain.
  // Workloads already marked for eviction (`isEvicted`) are always prioritized first implicitly,
  // so this criterion is omitted from the configurable ordering list.
  // The order is always deterministic, as the Workload UID is used as the final tie-breaker.
  // If not set, candidates will be ordered by default like this:
  // 1. Priority (Ascending: lowest priority first)
  // 2. AdmissionTimestamp (Descending: most recently admitted first, protecting long-running workloads)
  // 3. UID (Ascending: deterministic tie-breaker)
  // +optional
  Ordering []Order `json:"ordering,omitempty"`
}

// OrderingField specifies the criterion used to sort candidate workloads during preemption evaluation.
// Note: OrderingField is a predefined enum of sorting keys, not arbitrary fields of the Workload struct.
// Supported values are:
// - "Priority": orders workloads by effective priority (accounting for priority boost if enabled).
//   - Ascending (default): lowest priority first.
//   - Descending: highest priority first.
//
// - "AdmissionTimestamp": orders workloads by the timestamp when quota was reserved (admitted).
//   - Ascending (default): oldest admitted workloads first (FIFO preemption).
//   - Descending: most recently admitted workloads first (LIFO preemption, protecting long-running workloads, matching classical Kueue).
//
// - "ClusterQueueDRS": orders workloads based on their ClusterQueue's Dominant Resource Share.
//   - Ascending (default): workloads from ClusterQueues with lower Dominant Resource Share first.
//   - Descending: workloads from ClusterQueues with higher Dominant Resource Share first (preempting heavy borrowers first).
//
// - "IsOtherCQ": orders workloads based on whether they belong to a different ClusterQueue than the preemptor.
//   - Ascending (default): workloads from the same ClusterQueue first, followed by other ClusterQueues.
//   - Descending: workloads from other ClusterQueues first, followed by the same ClusterQueue.
//
// - "IsOtherCohort": orders workloads based on whether they belong to a different Cohort than the preemptor.
//   - Ascending (default): workloads from the same direct Cohort first, followed by other Cohorts.
//   - Descending: workloads from other Cohorts first, followed by the same Cohort.
//
// - "IsDRSLessThanInitialShare": orders workloads based on whether preemption of the workload is fair according to the DRSLessThanInitialShare strategy.
//   - Ascending (default): workloads from ClusterQueues exceeding their initial share first (prioritizing preemption of borrowing workloads).
//   - Descending: workloads from ClusterQueues within their initial share first.
//
// - "IsDRSLessThanOrEqualToFinalShare": orders workloads based on whether preemption of the workload is fair according to the DRSLessThanOrEqualToFinalShare strategy.
//   - Ascending (default): workloads from ClusterQueues exceeding their final share first (protecting workloads within fair share).
//   - Descending: workloads from ClusterQueues within or equal to their final share first.
//
// +kubebuilder:validation:Enum=Priority;AdmissionTimestamp;ClusterQueueDRS;IsOtherCQ;IsOtherCohort;IsDRSLessThanInitialShare;IsDRSLessThanOrEqualToFinalShare
type OrderingField string

const (
  // Priority orders candidates by effective priority (accounting for priority boost if enabled).
  // Ascending order places lowest priority candidates first.
  Priority OrderingField = "Priority"

  // AdmissionTimestamp orders candidates by the time quota was reserved.
  // Ascending order places oldest admitted candidates first and most recently admitted last.
  AdmissionTimestamp OrderingField = "AdmissionTimestamp"

  // ClusterQueueDRS orders candidates based on their ClusterQueue's Dominant Resource Share.
  // Ascending order places candidates from ClusterQueues with lower Dominant Resource Share first.
  ClusterQueueDRS OrderingField = "ClusterQueueDRS"

  // IsOtherCQ orders candidates based on whether their ClusterQueue differs from the preemptor.
  // Ascending order places workloads from the same ClusterQueue first.
  IsOtherCQ OrderingField = "IsOtherCQ"

  // IsOtherCohort orders candidates based on whether their direct Cohort differs from the preemptor.
  // Ascending order places workloads from the same Cohort first.
  IsOtherCohort OrderingField = "IsOtherCohort"

  // IsDRSLessThanInitialShare orders candidates based on whether preemption is fair according to DRSLessThanInitialShare.
  // Ascending order places workloads whose ClusterQueue exceeds initial share first.
  IsDRSLessThanInitialShare OrderingField = "IsDRSLessThanInitialShare"

  // IsDRSLessThanOrEqualToFinalShare orders candidates based on whether preemption is fair according to DRSLessThanOrEqualToFinalShare.
  // Ascending order places workloads whose ClusterQueue exceeds final share first.
  IsDRSLessThanOrEqualToFinalShare OrderingField = "IsDRSLessThanOrEqualToFinalShare"
)

// OrderingDirection specifies the sort direction for a candidate ordering criterion.
// Possible values are:
// - "Ascending": sort in natural ascending order (default).
// - "Descending": sort in reverse/descending order.
//
// +kubebuilder:validation:Enum=Ascending;Descending
type OrderingDirection string

const (
  // Ascending sorts candidate workloads in natural order (e.g., lowest priority first, oldest admission first, or same CQ/Cohort first).
  Ascending OrderingDirection = "Ascending"

  // Descending sorts candidate workloads in reverse order (e.g., highest priority first, newest admission first, or other CQ/Cohort first).
  Descending OrderingDirection = "Descending"
)

// Order specifies a single sorting criterion and direction for ordering preemption candidates.
// Multiple Order criteria are evaluated sequentially as a multi-key comparator chain,
// with ties broken by Workload UID for deterministic ordering.
type Order struct {
  // OrderingField specifies the field to sort preemption candidates by.
  //
  // +kubebuilder:validation:Required
  OrderingField OrderingField `json:"orderingField"`

  // Direction specifies whether to sort preemption candidates in ascending or descending order.
  // Defaults to "Ascending" if not specified.
  //
  // +kubebuilder:default=Ascending
  // +optional
  Direction OrderingDirection `json:"direction,omitempty"`
}
```

As defined by [current ordering](https://github.com/kubernetes-sigs/kueue/blob/24f6f99135979076a8d56ca7fc407990b98c66af/pkg/scheduler/preemption/common/ordering.go#L34-L41),
the order is currently based on:

0. Workloads already marked for preemption first.
1. Workloads from other ClusterQueues in the cohort before the ones in the same ClusterQueue as the preemptor.
2. (AdmissionFairSharing only) Workloads with lower LocalQueue's usage first.
3. Workloads with lower priority first.
4. Workloads admitted more recently first.

Therefore, the proposed custom ordering fields were designed to cover and generalize this well.

#### Examples with Custom Ordering

In future work, users would be able to configure explicit candidate ordering in `PreemptionConfig` manifests:

##### Story 1 - Defragmentation with Explicit Priority Ordering

```yaml
spec:
  rules:
    - name: defrag-smaller-tpu-workloads
      trigger: "InsufficientTopology"
      candidateSelectors:
        - relativeWorkloadPriority: "LowerOrEqual"
          relationRequirement: "AnyClusterQueue"
          numericLabels:
            - key: "tpus-count"
              relation: "Lower"
              defaultValue: 0
  ordering:
    - orderingField: "Priority"
      direction: "Ascending"
```

##### Story 2 - Hero Workload with Explicit Priority Ordering

```yaml
spec:
  rules:
    - name: hero-reclaim-topology
      trigger: "InsufficientTopology"
      candidateSelectors:
        - relativeWorkloadPriority: "Lower"
          relationRequirement: "AnyClusterQueue"
    - name: hero-reclaim-quota
      trigger: "InsufficientQuota"
      candidateSelectors:
        - relativeWorkloadPriority: "Lower"
          relationRequirement: "AnyClusterQueue"
  ordering:
    - orderingField: "Priority"
      direction: "Ascending"
```

#### Efficient Iteration Through Candidates in Preemption Order

As established in [Candidate Organization: Per-Selector, Per-CQ Priority Queues](#candidate-organization-per-selector-per-cq-priority-queues), the first iteration already organizes candidate workloads into per-selector, per-CQ priority queues to set the architectural ground for configurable candidate ordering. When configurable candidate ordering is introduced with custom comparator chains and dynamic ordering metrics (such as DRS), preemption evaluation requires an efficient iteration algorithm across these queues to avoid prohibitive performance degradation.

##### Problem Statement

Certain preemption candidate rules—such as those based on `BorrowingCapacityFromPreemptor` or Dominant Resource Share (DRS) fair-sharing strategies—depend on dynamic cluster state that changes as candidate workloads are simulated for preemption during evaluation.

For example, consider cluster queues A and B, each with a nominal quota of 5. Suppose CQ B is currently borrowing 1 unit of quota from CQ A. If a workload in CQ A triggers preemption under a rule targeting only borrowing workloads, and each candidate workload in CQ B consumes 1 unit of quota, the evaluator should only preempt a single workload from CQ B. Once that first workload is selected, CQ B is no longer borrowing quota from CQ A, so remaining workloads in CQ B must immediately become ineligible for that borrowing rule.

Furthermore, dynamic cluster metrics (such as DRS in fair-sharing cohorts) mean that preemption eligibility and relative candidate ordering across cluster queues can shift after every candidate selection step.

##### Naive Solutions and Complexity Bottlenecks

Let:

- $n$: total number of candidate workloads across all cluster queues in the cohort.
- $c$: number of cluster queues in the cohort, with $c \ll n$.
- $s$: number of candidate selectors configured in `PreemptionConfig` rules, with $s \le 5$.
- $m$: number of victim workloads required to satisfy the preemptor, with $m \le n$.

Under dynamic state changes:

- **Naive Linear Filtering per Selection** (`O(m · n)` to `O(n²)`): Dynamically filtering the candidate set and linearly scanning for the minimum at each of the $m$ preemption steps requires $O(n)$ work per step, yielding $O(m \cdot n)$ time (up to $O(n^2)$ in the worst case where $m \approx n$).
- **Naive Dynamic Re-sorting** (`O(m · n log n)` to `O(n² log n)`): Naively re-sorting the candidate array whenever CQ borrowing or DRS metrics change introduces an $O(n \log n)$ sorting step per eviction, leading to $O(m \cdot n \log n)$ time and severe scheduler throughput degradation.

##### Proposed Approach: Multi-Queue Dynamic Iteration

Leveraging the **Per-Selector, Per-CQ Priority Queues** established in the first iteration, the evaluator achieves optimal scheduling performance without repetitive full-array scans or re-sorting:

1. **Static Intra-Queue Ordering (Sort Once):**
   Within any given cluster queue, relative candidate ordering (e.g., by Priority, `AdmissionTimestamp`, Workload UID) is static and unaffected by dynamic quota borrowing or DRS changes. Therefore, candidate workloads within each `(Selector, CQ)` queue need to be sorted only once at the start of evaluation.

2. **CQ-Level State Tracking & Fast Pruning:**
   Dynamic state—such as current borrowed quota and cluster queue DRS—is tracked via lightweight counters attached to each CQ queue. When a CQ property no longer satisfies the selector's criteria (e.g., borrowed quota reaches zero for borrowing selectors), the entire priority queue for that CQ under that selector is pruned from consideration.

3. **Handling Workload-Specific Constraints (`DRSLessThanOrEqualToFinalShare`):**
   For selectors requiring workload-level evaluation (such as `DRSLessThanOrEqualToFinalShare`), the entire queue cannot simply be dropped at the CQ level because eligibility depends on the individual workload's DRS value. For these selectors, candidates are evaluated at extraction time when inspected at the queue head. If a candidate violates the fair-sharing constraint under current simulated state, it is popped and discarded for that selector.

4. **Multi-Queue Head Selection:**
   At each preemption step, the evaluator inspects the heads of all active priority queues and selects the globally minimal candidate according to the configured ordering comparator chain.

5. **Deduplication & Multi-Queue Popping:**
   A single workload can match multiple candidate selectors (across one or more preemption rules) and thus reside in multiple priority queues. Because the ordering comparator is consistent across queues, the selected minimal workload will always be at the head of all its corresponding queues. When chosen, it is popped from all matching queue heads simultaneously. Workloads are stored as shared pointers/references across queues to eliminate data duplication.

6. **Simulated State Updates:**
   After popping a candidate, the evaluator updates simulated state (reclaimed quota, updated DRS counters) and drops any newly ineligible CQ priority queues before the next selection step.

##### Example Walkthrough

Consider three cluster queues (CQ A, CQ B, and CQ C) in a flat cohort, each with 2 admitted workloads:

- **Workloads & Priorities**:
  - Preemptor: Workload A3 in ClusterQueue A, Priority = 40.
  - Candidates in CQ A: A1 (Priority = 20), A2 (Priority = 50).
  - Candidates in CQ B: B1 (Priority = 5), B2 (Priority = 10).
  - Candidates in CQ C: C1 (Priority = 30), C2 (Priority = 60).
- **Rules & Candidate Ordering**:
  - Candidates are evaluated according to the configured ordering comparator chain (e.g. lower priority workloads preempted first).
  - _Rule 1 (Priority-based, intra-CQ)_: Preempt workloads within the same CQ (CQ A) with strictly lower priority than the preemptor (priority < 40). Candidate matching: Workload A1 (Priority 20).
  - _Rule 2 (Fair Sharing, inter-CQ)_: Preempt workloads from any ClusterQueue whose DRS exceeds its fair share.

Now, workload A3 arrives in ClusterQueue A and requires preemption to be admitted:

1. **Queue Initialization (2 selectors × 3 ClusterQueues = 6 priority queues)**:
   - For the priority selector (Rule 1), DRS is ignored; these queues only contain workloads passing the static priority filter and intra-CQ constraint (Workload A1).
   - For the fair sharing selector (Rule 2), cohort DRS is evaluated dynamically for each CQ:
     - ClusterQueue B is borrowing and heavily exceeds fair share $\implies$ Workloads B1 and B2 are eligible.
     - ClusterQueue C is currently within its fair share $\implies$ Workloads C1 and C2 are **ineligible** under Rule 2, and do not match Rule 1 (different CQ). Thus, queues for CQ C are initially inactive/empty.

2. **Candidate Selection (Workloads B1, B2, A1)**:
   - The evaluator inspects the heads of all active priority queues and selects the candidate with the lowest priority.
   - First, it selects candidate **B1** (Priority 5), then candidate **B2** (Priority 10). As the cohort structure is flat, evicting B1 and B2 does not alter CQ C's fair-share status.
   - Next, the evaluator selects candidate **A1** (Priority 20). Because preemption within the same ClusterQueue is also considered fair under fair-sharing rules, A1 matches both Rule 1 and Rule 2, and is popped simultaneously from both queues representing ClusterQueue A.

3. **Dynamic State Recomputation & Selection of Workload C1**:
   - Simulating the preemption of A1 reduces ClusterQueue A's resource usage, which shifts the cohort fair-share baseline. Under the updated DRS values, ClusterQueue C now exceeds its fair share!
   - Consequently, the priority queue for ClusterQueue C under Rule 2 becomes active, making C1 (Priority 30) eligible for preemption.
   - The evaluator inspects active queue heads (C1 at 30 vs A2 at 50, C2 at 60) and selects candidate **C1** (Priority 30) as the lowest-priority eligible candidate.
   - _(Note: Without dynamic state recomputation, C1 would have been prematurely excluded or would have required a full scan of all cluster workloads.)_

4. **Termination**:
   - Workload A3 resource requirements can now be satisfied after selecting {B1, B2, A1, C1}. Candidate iteration terminates, and the scheduler proceeds to reverse-order backfilling.

##### Implementation Caveats and Selector Isolation

Maintaining separate priority queues per candidate selector is essential. If queues were pooled across selectors (either within a rule or across rules), dropping an ineligible CQ queue due to exhausted borrowing or DRS thresholds would inadvertently discard candidates that matched other non-borrowing, static selectors (such as priority-only preemption within the same CQ). Distinct per-selector queues permit aggressive filtering using static constraints up front while isolating dynamic state invalidation.

##### Complexity Analysis

To evaluate algorithmic efficiency under realistic cluster conditions:

- $n$: total number of candidate workloads across all cluster queues in the cohort.
- $c$: number of cluster queues in the cohort, with $c \ll n$.
- $s$: number of candidate selectors configured in the `PreemptionConfig`, with $s \le 5$.
- $m$: number of victim workloads required to admit the preemptor, with $m \le n$.

Assuming workloads are roughly evenly distributed across cluster queues (approximately $n/c$ workloads per queue):

1. **Queue Initialization & Sorting**:
   - The algorithm instantiates at most $s \times c$ priority queues.
   - Sorting each queue of size $n/c$ takes $O(\frac{n}{c} \log \frac{n}{c})$. Across all $s \times c$ queues:

     $$\sum_{i=1}^{s \times c} O\left(\frac{n}{c} \log \frac{n}{c}\right) = s \cdot c \cdot O\left(\frac{n}{c} \log \frac{n}{c}\right) = O\left(s \cdot n \log\left(\frac{n}{c}\right)\right)$$

   - Since $\log(n/c) \le \log n$, this is bounded by standard $O(s \cdot n \log n)$.

2. **Victim Selection & Dynamic Updates**:
   - At each selection step, finding the globally minimal candidate takes $O(c \cdot s)$ time to inspect the heads of all active queues.
   - Popping $m$ victim workloads requires $O(m \cdot c \cdot s)$ comparisons.
   - Updating simulated resource allocations and DRS values per victim takes $O(1)$ on a flat cohort structure.

3. **Overall Time Complexity**:

   $$T = O(s \cdot n \log n + m \cdot c \cdot s)$$

   Treating the number of selectors $s$ as a small constant, with $s = O(1)$, the overall complexity simplifies to:

   $$O(n \log n + m \cdot c)$$

##### Complexity Comparison

| Algorithm                              | Per-Step Selection Time     | Total Selection Time (for $m$ victims) | Overall Algorithm Time   | Scalability Bottleneck                                                        |
| -------------------------------------- | --------------------------- | -------------------------------------- | ------------------------ | ----------------------------------------------------------------------------- |
| **Naive Linear Filtering**             | $O(n)$                      | $O(m \cdot n)$                         | $O(m \cdot n)$           | High per-step scan overhead when $n$ is large.                                |
| **Naive Dynamic Re-sorting**           | $O(n \log n)$               | $O(m \cdot n \log n)$                  | $O(m \cdot n \log n)$    | Severe throughput degradation on frequent evictions.                          |
| **Proposed Per-(Selector, CQ) Queues** | $O(c \cdot s) \approx O(c)$ | $O(m \cdot c)$                         | **`O(n log n + m · c)`** | Scales with number of ClusterQueues $c$, independent of $n$ during selection. |

Because in real clusters the number of ClusterQueues is much smaller than the total number of workloads (where $c \ll n$, e.g. dozens of queues vs. thousands of workloads), where $m \cdot c \ll m \cdot n$. The multi-queue approach eliminates repetitive scans and re-sorting, ensuring scalable preemption evaluation.

##### Open Challenges

**Challenge 1** — how to handle the situation where workloads are preempted from the preemptor CQ, which makes previously removed workloads viable again — [issue #14122](https://github.com/kubernetes-sigs/kueue/issues/14122).

**Vague implementation idea** — keep track of workloads that are dropped because of DRS in the appropriate order and re-evaluate them (when a whole CQ is dropped because of DRS, save all of the workloads from it).

**Challenge 2** — how to make sure that preemptions are fair even if we backfill some workloads. The algorithm described above is fair if no backfilling is happening, but if we preempt and then backfill it can lead to issues as described in [issue #14543](https://github.com/kubernetes-sigs/kueue/issues/14543).

**Vague implementation idea** — when backfilling, hold the required values (attached to the CQs or in a cohort-tree-like struct) to make preemption of suffix workloads still fair according to the DRS rules. If backfilling changes the DRS in a way that makes the "fairness" rule no longer true for suffix workloads, then do not reintroduce them.
As stricter backfilling can lead to lower cluster utilization (a trade-off with fairness), this should probably be introduced as an additional preemption config parameter (boolean flag).
There are some additional caveats that should be addressed — for example, what if suffix candidate preemption is still possible because other non-DRS rules allow it? Then we should probably allow backfilling of the workloads, but this may lead to a change in the ordering of the candidates. For simplicity, it may be worth documenting as a known limitation that candidates are only ordered once according to the original plan and not reordered during backfilling.

### Time-Based Candidate Selectors (Execution and Creation Duration)

Filtering candidates based on workload execution duration or creation age addresses valid operational and SLA requirements, but is not deemed a must-have in the first iteration of `PreemptionConfig`. These fields are deferred to future work.

Relevant use cases include:

1. **Minimal execution duration before preemption ([Issue #9596](https://github.com/kubernetes-sigs/kueue/issues/9596)):**
   Avoid thrashing workloads that have just started by requiring candidates to have run for a minimum duration (e.g., at least 15 minutes) before being eligible for preemption.
2. **SLA protection based on workload creation time:**
   Prevent preemption of older workloads nearing completion or SLA deadlines by selecting only recently created workloads (e.g., created less than 1 hour ago) as preemption candidates.
3. **Relative execution time comparison:**
   Compare the candidate workload's runtime or creation time against the preemptor workload (e.g., only preempt workloads that have been running for shorter duration than the preemptor).

#### Proposed API for Time-Based Candidate Selectors

In a future iteration, `PreemptionCandidateSelector` can be extended with the following duration and time-relation fields:

```go
type PreemptionCandidateSelector struct {
  // ... baseline candidate selector fields ...

  // Accepts any execution times if not set.
  // MinExecutionDuration specifies the minimum runtime a candidate workload must have completed.
  MinExecutionDuration *metav1.Duration `json:"minExecutionDuration,omitempty"`

  // MaxExecutionDuration specifies the maximum runtime a candidate workload can have completed.
  MaxExecutionDuration *metav1.Duration `json:"maxExecutionDuration,omitempty"`

  // ExecutionTimeRelation defines how the candidate's execution time compares to the preemptor's.
  ExecutionTimeRelation *RelativeConstraint `json:"executionTimeRelation,omitempty"`

  // Accepts any time from creation if not set.
  // MinTimeFromCreationDuration specifies the minimum age of the workload from creation timestamp.
  MinTimeFromCreationDuration *metav1.Duration `json:"minTimeFromCreationDuration,omitempty"`

  // MaxTimeFromCreationDuration specifies the maximum age of the workload from creation timestamp.
  MaxTimeFromCreationDuration *metav1.Duration `json:"maxTimeFromCreationDuration,omitempty"`

  // TimeFromCreationRelation defines how the candidate's creation time compares to the preemptor's.
  TimeFromCreationRelation *RelativeConstraint `json:"timeFromCreationRelation,omitempty"`
}
```

#### Examples with Time-Based Candidate Selectors

##### Story 1 - Minimal Execution Duration Before Preemption

```yaml
spec:
  rules:
    - name: preempt-only-after-min-exec-time
      trigger: "InsufficientQuota"
      candidateSelectors:
        - relationRequirement: "SameClusterQueue"
          relativeWorkloadPriority: "Lower"
          minExecutionDuration: "15m"
```

##### Story 2 - SLA Protection Based on Workload Creation Time

```yaml
spec:
  rules:
    - name: preempt-recent-workloads-only
      trigger: "InsufficientQuota"
      candidateSelectors:
        - relationRequirement: "SameClusterQueue"
          relativeWorkloadPriority: "Lower"
          maxTimeFromCreationDuration: "1h"
```

### Workload Priority Class Selectors

Selecting preemption candidates based on workload priority class label selectors allows targeting specific priority classes (e.g. preempting only `batch-low` workloads within the same ClusterQueue or when reclaiming borrowed cohort capacity). While these use cases are well-identified, configuring priority-class label selectors is deferred to future work. Preemptor workloads are qualified at the rule level via `matchingPreemptorWorkloads`.

Relevant use cases include:

1. **Priority threshold for within-ClusterQueue preemptions ([Issue #12001](https://github.com/kubernetes-sigs/kueue/issues/12001)):**
   Restricted preemption within the same ClusterQueue targeting only candidates matching a specific priority class.
2. **Priority threshold for reclaim within Cohort ([Issue #12046](https://github.com/kubernetes-sigs/kueue/issues/12046)):**
   Reclaim borrowed capacity within the cohort only from candidates matching a specific priority class.

#### Proposed API for Workload Priority Class Selectors

In a future iteration, `PreemptionCandidateSelector` will be extended with the following label selector field:

```go
type PreemptionCandidateSelector struct {
  // ... baseline candidate selector fields ...

  // CandidateWorkloadPrioritySelector defines label selector constraints on candidate WorkloadPriorityClasses.
  // Matches all workload priority classes if not set.
  //
  // +optional
  CandidateWorkloadPrioritySelector *metav1.LabelSelector `json:"candidateWorkloadPrioritySelector,omitempty"`
}
```

#### Examples with Workload Priority Class Selectors

##### Story 1 - Priority Threshold for Within-ClusterQueue Preemptions

```yaml
spec:
  rules:
    - name: preempt-same-cq-low-priority
      trigger: "InsufficientQuota"
      candidateSelectors:
        - relationRequirement: "SameClusterQueue"
          candidateWorkloadPrioritySelector:
            matchLabels:
              kueue.x-k8s.io/priority-class: "batch-low"
```

##### Story 2 - Priority Threshold for Reclaim Within Cohort

```yaml
spec:
  rules:
    - name: reclaim-cohort-quota-from-low-priority
      trigger: "QuotaReclaimRequired"
      candidateSelectors:
        - relationRequirement: "SameCohort"
          quota: "BorrowingCapacityFromPreemptor"
          candidateWorkloadPrioritySelector:
            matchLabels:
              kueue.x-k8s.io/priority-class: "batch-low"
```

### PreemptionLimit (Rate-Limiting Guardrails)

While `PreemptionConfig` provides declarative candidate selection policies, cluster administrators also need rate-limiting guardrails to prevent cascading preemptions, eviction storms, and cluster instability during large-scale rescheduling. To maintain focus on core `PreemptionConfig` mechanics for Alpha, the `PreemptionLimit` cluster-scoped CRD is deferred to future work.

Relevant capabilities include:

1. **Global rate-limiting**: Restrict the total number of preemption events across the entire cluster within a sliding time window.
2. **Preempting ClusterQueue rate-limiting**: Throttle preemptions triggered by workloads originating from a specific ClusterQueue.
3. **Preempted ClusterQueue protection**: Limit or block preemptions targeting workloads belonging to a specific ClusterQueue (e.g., setting `limit: 0` to make mission-critical or hero queues non-preemptible).
4. **Preempted Workload churn limiting**: Restrict how many times an individual workload can be preempted within a given time window to avoid starvation or ping-pong eviction loops.

#### Proposed API for PreemptionLimit

In a future iteration, `PreemptionLimit` will be introduced as a cluster-scoped CRD:

```go
type PreemptionLimit struct {
  metav1.TypeMeta `json:",inline"`
  metav1.ObjectMeta `json:"metadata,omitempty"`
  Spec PreemptionLimitSpec `json:"spec,omitempty"`
  Status PreemptionLimitStatus `json:"status,omitempty"`
}

// +kubebuilder:validation:Enum=Global;PreemptingClusterQueue;PreemptedClusterQueue;PreemptedWorkload
type PreemptionLimitScope string
const (
  GlobalPreemptionLimitScope PreemptionLimitScope = "Global"
  PreemptingCQLimitScope PreemptionLimitScope = "PreemptingClusterQueue"
  PreemptedCQLimitScope PreemptionLimitScope = "PreemptedClusterQueue"
  PreemptedWorkloadLimitScope PreemptionLimitScope = "PreemptedWorkload"
)

type PreemptionLimitSpec struct {
  // Scope specifies the entity boundary for this preemption limit.
  //
  // +kubebuilder:validation:Required
  Scope PreemptionLimitScope `json:"scope"`

  // ConfigSelector selects PreemptionConfigs to which this limit applies.
  // If not set, it applies to all PreemptionConfigs.
  //
  // +optional
  ConfigSelector *metav1.LabelSelector `json:"configSelector,omitempty"`

  // ClusterQueueSelector selects ClusterQueues to which this limit applies.
  // If not set, it applies to all ClusterQueues under the configured scope.
  //
  // +optional
  ClusterQueueSelector *metav1.LabelSelector `json:"clusterQueueSelector,omitempty"`

  // RuleNames restricts the limit to specific rule names within matching PreemptionConfigs.
  // If not set, it applies to all rules.
  //
  // +optional
  RuleNames []string `json:"ruleNames,omitempty"`

  // Limit defines how many preemption events can occur within the given time window.
  // An event is defined as a confirmed (preemptor, preemptee) eviction pair.
  // Setting Limit to 0 blocks all preemptions under this limit's scope.
  //
  // +kubebuilder:validation:Required
  // +kubebuilder:validation:Minimum=0
  Limit int32 `json:"limit"`

  // LimitWindowDuration specifies the sliding time window duration.
  // Must be greater than or equal to 1s to prevent sub-second thrashing.
  //
  // +kubebuilder:validation:Required
  // +kubebuilder:validation:XValidation:rule="self >= duration('1s')",message="must be at least 1s"
  LimitWindowDuration metav1.Duration `json:"limitWindowDuration"`
}

type PreemptionLimitStatus struct {
  Conditions []metav1.Condition `json:"conditions,omitempty"`

  // Periodically updated, for reference only.
  // Map key depends on the scope. For Global it is just Global.
  // For CQ it is cluster queue name.
  // For Workload it is namespace + "/" + workload name.
  // Restricted to the top 1000 counts to fit within CRD size limits.
  Count map[string]int32 `json:"count,omitempty"`
}
```

PreemptionLimit limits the number of preemptions that happen for the specified set of rules. The preemption evaluator evaluates proposed preemptions against defined limit objects, allowing them to proceed only if adequate preemption quota remains. If a preemption is in the scope of multiple limits, quota must exist in all of them.
To track this, a list of preemption rule names responsible for selecting each candidate must be maintained.

To manage this data, Kueue will store a comprehensive preemption map in memory, isolated per PreemptionLimit. This map tracks all preemption event timestamps under a specific CQ/workload key, capturing events that occurred within the designated `LimitWindowDuration`. Moreover, it tracks only events that are in the scope of the specific limit; if a preemption does not match the defined config or rules selector, it will not be tracked in that particular instance of the preemption map. This list is dynamically trimmed upon each retrieval to filter out expired timestamps.

Furthermore, the status of the PreemptionLimit is refreshed periodically — approximately every minute — to write the aggregated totals into the count map (restricted to the top 1000 counts to fit within CRD size limits).

#### Observability When Reaching Preemption Limits

When preemption is throttled or blocked due to an exhausted `PreemptionLimit`:

1. **Workload Condition**: A condition with type `PreemptionBlockedByLimit` (reason `PreemptionLimitExceeded`) is assigned to the preemptor Workload, with an informative message indicating which limit blocked admission (e.g. `"Preemption was blocked by PreemptionLimit <limit-name>"`).
2. **Kubernetes Events**: A Kubernetes `Event` with reason `PreemptionThrottled` is emitted on both the preemptor Workload and its ClusterQueue.
3. **Structured Audit Logging**: Informational/debug log entries are recorded specifying the limit name, scope, and affected entities for operator troubleshooting.

#### Examples with PreemptionLimit

##### Story 1 - Global Preemption Rate Limiting

Rate-limit global preemptions to at most 10 evictions across the entire cluster in any 5-minute sliding window:

```yaml
spec:
  scope: "Global"
  limit: 10
  limitWindowDuration: "5m"
```

##### Story 2 - Protecting a Mission-Critical ClusterQueue from Preemption

Ensure that workloads running in the `hero-cq` ClusterQueue can never be preempted by setting `limit: 0` under the `PreemptedClusterQueue` scope:

```yaml
spec:
  scope: "PreemptedClusterQueue"
  clusterQueueSelector:
    matchLabels:
      kueue.x-k8s.io/queue-name: "hero-cq"
  limit: 0
  limitWindowDuration: "1h"
```

### Minimum Trigger Duration (MinTriggerRequiredDuration)

In many production environments, administrators want to avoid premature or "flapping" preemptions caused by transient quota shortages or temporary topology fragmentation that might resolve naturally within a short window (e.g., as short jobs complete or as autoscaling nodes join). By requiring that a trigger condition (such as `InsufficientTopology`, `InsufficientQuota`, or `QuotaReclaimRequired`) persists for a minimum duration before evaluating candidate preemptions, clusters can grant a grace window for normal placement or natural workload completions before resorting to disruptive evictions.

In the initial Alpha release, preemption evaluation triggers immediately upon observing the trigger condition in memory without timer-based requeueing, keeping the execution flow synchronous with scheduling passes and avoiding timer management complexity. In future iterations, `PreemptionRule` will be extended with `minTriggerRequiredDuration`.

#### Proposed API for Minimum Trigger Duration

```go
type PreemptionRule struct {
  // Name of the preemption rule.
  Name string `json:"name"`

  // MatchingPreemptorWorkloads specifies an optional label selector to limit which preemptor workloads can activate this rule.
  //
  // +optional
  MatchingPreemptorWorkloads *metav1.LabelSelector `json:"matchingPreemptorWorkloads,omitempty"`

  // Trigger specifies the condition (InsufficientQuota, QuotaReclaimRequired, or InsufficientTopology)
  // that must be observed on the preemptor workload for this rule to apply.
  Trigger PreemptionRuleTrigger `json:"trigger"`

  // MinTriggerRequiredDuration specifies how long the trigger condition must be observed before
  // preempting workloads specified by candidateSelectors. 0s indicates that preemptions can be started immediately.
  // Defaults to 0s.
  //
  // +optional
  // +kubebuilder:default="0s"
  MinTriggerRequiredDuration metav1.Duration `json:"minTriggerRequiredDuration,omitempty"`

  // CandidateSelectors specifies the selection rules for workloads that are candidates for preemption.
  CandidateSelectors []PreemptionCandidateSelector `json:"candidateSelectors,omitempty"`
}
```

When `minTriggerRequiredDuration` is configured with a duration greater than `0s`:
- When an active trigger is first observed on an unadmitted workload, its observation timestamp is stored in the in-memory cache, and the workload is moved to `inadmissibleWorkloads`.
- An in-memory timer is scheduled to requeue the workload back to the active queue once the required duration elapses.
- During scheduling cycles, `PreemptionEvaluator` validates whether `now - inMemoryObserved >= minTriggerRequiredDuration` before considering the rule eligible for candidate selection.

#### Examples with Minimum Trigger Duration

##### Story 1 - Grace Period for Topology Defragmentation

Delay defragmentation preemption by 30 seconds to give running workloads time to finish or allow the cluster autoscaler to provision a suitable topology domain before evicting smaller workloads:

```yaml
spec:
  rules:
    - name: defrag-smaller-tpu-workloads
      trigger: "InsufficientTopology"
      minTriggerRequiredDuration: "30s"
      candidateSelectors:
        - relativeWorkloadPriority: "LowerOrEqual"
          relationRequirement: "AnyClusterQueue"
          numericLabels:
            - key: "tpus-count"
              relation: "Lower"
              defaultValue: 0
```

