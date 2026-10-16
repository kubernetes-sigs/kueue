# KEP-16925: Preemption Limits

<!--
This is the title of your KEP. Keep it short, simple, and descriptive. A good
title can help communicate what the KEP is and should be considered as part of
any review.
-->

<!--
A table of contents is helpful for quickly jumping to sections of a KEP and for
highlighting any additional information provided beyond the standard KEP
template.

Ensure the TOC is wrapped with
  <code>&lt;!-- toc --&rt;&lt;!-- /toc --&rt;</code>
tags, build the mdtoc tool with `make mdtoc` and then generate the TOC with `hack/tools/mdtoc/generate.sh`.
-->

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1 - Safe rollout of experimental PreemptionConfig (or new preemption rule in the future)](#story-1---safe-rollout-of-experimental-preemptionconfig-or-new-preemption-rule-in-the-future)
    - [Story 2 - Protecting a workload from too many disruptions](#story-2---protecting-a-workload-from-too-many-disruptions)
    - [Story 3 - Global Preemption Rate Limiting](#story-3---global-preemption-rate-limiting)
    - [Story 4 - Protecting a Mission-Critical ClusterQueue from Preemption](#story-4---protecting-a-mission-critical-clusterqueue-from-preemption)
  - [Caveats](#caveats)
  - [Risks and Mitigations](#risks-and-mitigations)
    - [Performance degradation](#performance-degradation)
    - [Security considerations](#security-considerations)
- [Design Details](#design-details)
  - [Proposed API](#proposed-api)
  - [Limit Enforcement](#limit-enforcement)
  - [In-Memory State Management](#in-memory-state-management)
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
  - [Rule-Level Rate Limiting via <code>ruleNames</code>](#rule-level-rate-limiting-via-rulenames)
    - [Multi-Rule Attribution Challenge and Alternatives Considered](#multi-rule-attribution-challenge-and-alternatives-considered)
    - [Proposed Approach: First Matching Rule Consumes the Limits](#proposed-approach-first-matching-rule-consumes-the-limits)
  - [Pod-Level Rate Limiting via <code>mode</code>](#pod-level-rate-limiting-via-mode)
  - [Reconstructing Approximate In-Memory State on Restart from Status](#reconstructing-approximate-in-memory-state-on-restart-from-status)
    - [Advantages](#advantages)
    - [Trade-offs](#trade-offs)
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

This KEP introduces **Preemption Limits** in Kueue through a new cluster-scoped `PreemptionLimit` Custom Resource Definition (CRD).
While declarative preemption systems (such as Configurable Preemptions via `PreemptionConfig`, introduced in [KEP-13396](/keps/13396-configurable-preemptions/README.md)) allow expressive candidate selection policies, cluster administrators need rate-limiting guardrails to safely roll out new configurations, control the blast radius of misconfigurations, avoid cascading evictions, and protect workloads and queues from disruptive eviction storms.

`PreemptionLimit` allows administrators to configure sliding-window rate limits across multiple scopes:
- **Global**: Limits the total number of preemption events across the entire cluster within the sliding window.
- **PreemptorClusterQueue**: Restricts the preemption rate triggered by workloads originating from specific ClusterQueues.
- **PreemptedClusterQueue**: Constrains the eviction rate targeting workloads running in specific ClusterQueues, including blocking preemptions entirely (`limit: 0`) to protect mission-critical "hero" queues.
- **PreemptedWorkload**: Limits how frequently an individual workload can be preempted within the window to prevent workload bullying and eviction thrashing loops.

Limits can target a specific subset of preemptions using label selectors (`configSelector` and `clusterQueueSelector`).
In Alpha, this capability is introduced specifically for Configurable Preemptions under the `ConfigurablePreemptionLimits` feature gate, with possible extensions to classical and fair-sharing preemption in future iterations.

## Motivation

While `PreemptionConfig` provides declarative candidate selection policies, cluster administrators also need rate-limiting guardrails to roll out new configurations safely, prevent cascading preemptions, eviction storms, and cluster instability during large-scale rescheduling.

### Goals

1. Safely introduce new `PreemptionConfig`s or rules within `PreemptionConfig`.
2. Improve cluster stability by rate-limiting preemptions.
3. Define the most common scopes and fields for limiting preemptions.

### Non-Goals

1. One advanced set of predefined `PreemptionLimit`s to fulfill every possible rate-limiting use case.
2. Defaults for rate limiting current classical/fair sharing preemptions (as those would be breaking).
3. Automatic addition of labels to existing Kueue resources (ClusterQueues, PreemptionConfigs).

## Proposal

This KEP proposes a new preemption rate-limiting mechanism — initially supported only in Configurable Preemptions, with the potential to extend to classical or fair-sharing preemptions in the future. The new mechanism is guarded by the `ConfigurablePreemptionLimits` feature gate, which requires the `ConfigurablePreemptions` feature gate to be enabled. When `ConfigurablePreemptionLimits` is disabled, Configurable Preemptions will not be limited according to the limits defined in the system.

Relevant capabilities include:

1. **Global rate-limiting**: Restrict the total number of preemption events across the entire cluster within a sliding time window.
2. **Preemptor ClusterQueue rate-limiting**: Throttle preemptions triggered by workloads originating from a specific ClusterQueue.
3. **Preempted ClusterQueue protection**: Limit or block preemptions targeting workloads belonging to a specific ClusterQueue (e.g., setting `limit: 0` to make mission-critical or hero queues non-preemptible).
4. **Preempted Workload churn limiting**: Restrict how many times an individual workload can be preempted within a given time window to avoid starvation, ping-pong eviction loops, or bullying of a particular workload.

### User Stories

#### Story 1 - Safe rollout of experimental PreemptionConfig (or new preemption rule in the future)

Mistakes in `PreemptionConfig` can lead to serious consequences, such as mass evictions or cluster instability. Introducing rate-limiting for preemptions associated with a new `PreemptionConfig` (or a specific rule within an existing config, deferred to future work) limits the blast radius of such misconfigurations. Cluster administrators can initially set a conservative limit on a new config and gradually increase it while observing cluster stability.

```yaml
spec:
  scope: "Global"
  limit: 1
  limitWindowSeconds: 3600
  configSelector:
    matchLabels:
      example.com/config-name: "experimental-preemption-config"
```

#### Story 2 - Protecting a workload from too many disruptions

Rate-limit preemptions of each workload in the cluster, so that it can be preempted only once per day to avoid bullying of a particular job by multiple preemptions.

```yaml
spec:
  scope: "PreemptedWorkload"
  limit: 1
  limitWindowSeconds: 86400
```

> [!WARNING]
> In Alpha, this will protect only from preemptions originating from configurable preemptions.

#### Story 3 - Global Preemption Rate Limiting

Rate-limit global preemptions to at most 10 evictions across the entire cluster in any 5-minute sliding window to avoid too many disruptions in the cluster:

```yaml
spec:
  scope: "Global"
  limit: 10
  limitWindowSeconds: 300
```

> [!WARNING]
> In Alpha, this will protect only from preemptions originating from configurable preemptions.

#### Story 4 - Protecting a Mission-Critical ClusterQueue from Preemption

Ensure that workloads running in the `hero-cq` ClusterQueue can never be preempted by setting `limit: 0` under the `PreemptedClusterQueue` scope:

```yaml
spec:
  scope: "PreemptedClusterQueue"
  clusterQueueSelector:
    matchLabels:
      example.com/cluster-queue-name: "hero-cq"
  limit: 0
  limitWindowSeconds: 3600
```

> [!WARNING]
> In Alpha, this will protect only from preemptions originating from configurable preemptions.

### Caveats

1. **User-managed resource labeling**: Because ClusterQueue names are not currently constrained to 63 characters and Kueue does not automatically inject labels onto cluster-scoped CRDs (as Kubernetes does for Namespaces), users must label ClusterQueues and PreemptionConfigs manually to match selectors. Improving this experience may be considered in future work, but is out of scope for this KEP.
2. **Reset on controller restarts**: Preemption events are tracked strictly in-memory within the controller cache and are not persisted in etcd. Consequently, if the Kueue controller restarts (e.g. due to pod restart, failover, or upgrade), preemption limits will be reset, temporarily allowing preemption events up to the configured limit immediately following the restart.
3. **Increasing time window**: Because expired preemption timestamps are periodically trimmed from the in-memory map based on the active sliding window, if a user updates a `PreemptionLimit` to increase `limitWindowSeconds`, events that occurred prior to the previously configured window will have already been pruned and will not be retroactively taken into account.
4. **Deleted and recreated objects**: Preemption events are tracked in-memory by entity key (`namespace/name` for Workloads, or name for ClusterQueues) rather than by UID. Consequently, objects in the same namespace and with the same name will be treated as the same object, and preemption limits for them will not be reset if an object is deleted and recreated within the active time window.
5. **Reset on spec changes to scope or configSelector**: If a `PreemptionLimit` is modified such that its `scope` or `configSelector` changes, its limits are reset by clearing all recorded in-memory preemption event timestamps for that limit.

### Risks and Mitigations

Because this feature is intended to de-risk Configurable Preemptions (currently in Alpha), it is initially introduced solely for Configurable Preemptions. It does not alter mature classical or fair-sharing preemption algorithms, keeping the blast radius small. The feature can be completely disabled at any time via the `ConfigurablePreemptionLimits` (or `ConfigurablePreemptions`) feature gate.

#### Performance degradation

Because validating preemption candidates against active limits introduces additional checks during preemption evaluation, performance monitoring is necessary to ensure preemption evaluation speed remains acceptable.

This risk is mitigated by:

- `PreemptionLimit`s are completely optional and can be disabled by removing the resources or disabling the `ConfigurablePreemptionLimits` feature gate,
- performance testing prior to graduating `ConfigurablePreemptionLimits` to Beta,
- short-circuit optimizations in the implementation (e.g., if a Global preemption limit has already been reached for a given `PreemptionConfig`, candidate evaluation for that config is skipped immediately),
- efficient in-memory timestamp tracking with periodic trimming of expired entries.

#### Security considerations

Because `PreemptionLimit` is a cluster-scoped resource modifiable only by cluster administrators, this feature introduces no security or multi-tenancy privilege-escalation risks. Administrators modifying them should nonetheless be aware that severe misconfigurations (such as setting an unintended `limit: 0`) could prevent necessary preemptions, leaving workloads pending.

## Design Details

### Proposed API
`PreemptionLimit` will be introduced as a cluster-scoped CRD:

```go
// PreemptionLimit is the Schema for the preemptionlimits API
type PreemptionLimit struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is the standard object metadata.
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec defines the desired state of the PreemptionLimit.
	// +optional
	Spec PreemptionLimitSpec `json:"spec"`

	// status defines the observed state of the PreemptionLimit.
	// +optional
	Status PreemptionLimitStatus `json:"status,omitempty"`
}


// PreemptionLimitScope specifies the entity boundary for a preemption limit.
// Possible values are:
// - "Global": restricts the total number of preemption events across the entire cluster within the sliding time window.
// - "PreemptorClusterQueue": restricts the number of preemption events triggered by workloads originating from a specific ClusterQueue within the sliding time window.
// - "PreemptedClusterQueue": restricts the number of preemption events targeting workloads belonging to a specific ClusterQueue within the sliding time window.
// - "PreemptedWorkload": restricts how many times an individual workload can be preempted within the sliding time window.
//
// +kubebuilder:validation:Enum=Global;PreemptorClusterQueue;PreemptedClusterQueue;PreemptedWorkload
type PreemptionLimitScope string

const (
	// GlobalPreemptionLimitScope restricts the total number of preemption events
	// across the entire cluster within the sliding time window.
	GlobalPreemptionLimitScope PreemptionLimitScope = "Global"

	// PreemptorCQLimitScope restricts the number of preemption events triggered by workloads
	// originating from a specific ClusterQueue within the sliding time window.
	PreemptorCQLimitScope PreemptionLimitScope = "PreemptorClusterQueue"

	// PreemptedCQLimitScope restricts the number of preemption events targeting workloads
	// belonging to a specific ClusterQueue within the sliding time window.
	PreemptedCQLimitScope PreemptionLimitScope = "PreemptedClusterQueue"

	// PreemptedWorkloadLimitScope restricts how many times an individual workload
	// can be preempted within the sliding time window.
	PreemptedWorkloadLimitScope PreemptionLimitScope = "PreemptedWorkload"
)

// PreemptionLimitSpec defines the desired state of PreemptionLimit
type PreemptionLimitSpec struct {
	// scope specifies the entity boundary for this preemption limit.
	//
	// +required
	Scope PreemptionLimitScope `json:"scope,omitempty"`

	// configSelector selects PreemptionConfigs to which this limit applies.
	// If not set, it applies to all PreemptionConfigs.
	//
	// +optional
	ConfigSelector *metav1.LabelSelector `json:"configSelector,omitempty"`

	// clusterQueueSelector selects ClusterQueues to which this limit applies.
	// If not set, it applies to all ClusterQueues under the configured scope.
	//
	// +optional
	ClusterQueueSelector *metav1.LabelSelector `json:"clusterQueueSelector,omitempty"`

	// limit defines how many preemption events can occur within the given time window.
	// An event is defined as a confirmed (preemptor, preemptee) eviction pair.
	// Setting limit to 0 blocks all preemptions under this limit's scope.
	//
	// +required
	// +kubebuilder:validation:Minimum=0
	Limit int32 `json:"limit"`

	// limitWindowSeconds specifies the sliding time window duration, in seconds.
	// Must be greater than or equal to 1 to prevent sub-second thrashing.
	//
	// +required
	// +kubebuilder:validation:Minimum=1
	LimitWindowSeconds int32 `json:"limitWindowSeconds,omitempty"`
}

// PreemptionLimitStatus defines the observed state of PreemptionLimit
type PreemptionLimitStatus struct {
	// lastUpdateTimestamp is the time when the status was last updated.
	//
	// +required
	LastUpdateTimestamp metav1.Time `json:"lastUpdateTimestamp"`

	// counts is periodically updated, for reference only.
	// Restricted to the top 1000 counts to fit within CRD size limits.
	//
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=1000
	Counts []PreemptionLimitCount `json:"counts,omitempty"`
}

// PreemptionLimitCount tracks the number of preemptions for a single scoped entity.
type PreemptionLimitCount struct {
	// name identifies the scoped entity.
	// For Global scope it is "Global".
	// For PreemptorClusterQueue and PreemptedClusterQueue scopes it is the ClusterQueue name.
	// For PreemptedWorkload scope it is "<namespace>/<workload-name>".
	//
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=317
	Name string `json:"name"`

	// value is the number of preemptions recorded within the sliding time window.
	//
	// +required
	// +kubebuilder:validation:Minimum=0
	Value int32 `json:"value"`
}
```

### Limit Enforcement

A `PreemptionLimit` limits the number of preemptions that can occur within its configured scope. During scheduling, the preemption evaluator checks proposed preemptions against all applicable limit objects, allowing an eviction to proceed only if sufficient preemption allowance remains. If a preemption falls within the scope of multiple limits, quota must be available in all of them. If the `ConfigurablePreemptionLimits` feature gate is disabled, Configurable Preemptions will not be constrained by any limits defined in the cluster.

### In-Memory State Management

To manage this data, Kueue stores a comprehensive preemption map in memory, isolated per `PreemptionLimit`. This map tracks all preemption event timestamps indexed by entity key (ClusterQueue or Workload), recording events that occurred within the designated `limitWindowSeconds`. Moreover, it records only events that match the scope and selectors of the specific limit; if a preemption does not match the configured `configSelector` or `clusterQueueSelector`, it is not tracked in that limit's preemption map. Expired timestamps are periodically trimmed from the map.

Because tracking is strictly maintained in-memory and periodically trimmed:
- **Reset on controller restarts**: Events are not persisted in etcd; restarting the Kueue controller resets the in-memory map, so preemption limit counters start fresh from zero.
- **Reset on spec changes to scope or configSelector**: If a `PreemptionLimit`'s `spec` is modified to change its `scope` or `configSelector`, the in-memory limit counters are reset by clearing all recorded preemption event timestamps for that limit.
- **Increasing time window**: If `LimitWindowSeconds` is increased, previous preemption events that occurred before the previously active window have already been discarded and will not be taken into account.
- **Deleted and recreated objects**: Entity keys in the in-memory tracking map are based on `namespace/name` (or name for ClusterQueues) rather than UID; objects in the same namespace with the same name will be treated as the same object, and their limits will not be reset upon deletion and recreation within the sliding window.

Furthermore, the status of the PreemptionLimit is refreshed periodically by a dedicated `PreemptionLimitReconciler` — approximately every minute — to update `lastUpdateTimestamp` and write the aggregated totals into the count map (restricted to the top 1000 counts to fit within CRD size limits).

`PreemptionLimitReconciler` will be responsible for updating an in-memory representation of limits — removing no longer needed recorded preemption limit timestamps from memory when a particular `PreemptionLimit` is deleted, and resetting the in-memory limits when a `PreemptionLimit`'s `spec.scope` or `spec.configSelector` changes.


### Observability

When preemption is throttled or blocked due to an exhausted `PreemptionLimit`, informational and debug log entries are recorded specifying the limit name, scope, and affected entities for operator troubleshooting.
Additionally, each preemption operation executed within the scope of active limits is recorded and reported in the periodically updated `PreemptionLimit.status.counts` and `PreemptionLimit.status.lastUpdateTimestamp`.

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


#### Unit tests

<!--
In principle every added code should have complete unit test coverage, so providing
the exact set of tests will not bring additional value.
However, if complete unit test coverage is not possible, explain the reason of it
together with explanation why this is acceptable.
-->

<!--
Additionally, try to enumerate the core package you will be touching
to implement this enhancement and provide the current unit coverage for those
in the form of:
- <package>: <date> - <current test coverage>

This can inform certain test coverage improvements that we want to do before
extending the production code to implement this enhancement.
-->
The majority of the new code will be introduced as a separate subpackage under `pkg/controller/preemption` called `limits`, alongside a dedicated controller handling periodic updates to `PreemptionLimit` status. All public methods across these components will be thoroughly covered by unit tests.

Apart from the new subpackage, the following existing components will receive additional unit tests covering the new behavior when the feature gate is enabled:
1. **Preemption Evaluator**:
   - Selects only candidates that do not violate any applicable limits.
   - Accurately reports which limits were consumed by the selected candidates.
2. **Preemptor.IssuePreemptions`**:
   - Records confirmed Configurable Preemption events in the in-memory limits state.
   - Records each event exactly once upon successful preemption issuance.
3. **Scheduler**:
   - Recomputes preemption candidates when concurrent workloads encounter conflicting limit constraints (analogous to overlapping preemption targets).


#### Integration tests

1. Preemption limits are enforced during Configurable Preemptions candidate selection when the feature gate is enabled.
2. A `PreemptionConfig` can be safely rolled out by progressively adjusting its limits.
3. Preemptions from Configurable Preemptions can be blocked entirely for a specific target ClusterQueue (e.g. `limit: 0`).


#### e2e tests

1. Cluster administrators can create, read, update, and delete `PreemptionLimit` resources.
2. Cluster administrators can observe the reported `status.counts` and `status.lastUpdateTimestamp` of `PreemptionLimit` resources.
3. Unprivileged batch users cannot create or modify `PreemptionLimit` resources (enforced via RBAC).
4. Extension of Configurable Preemptions e2e tests to validate end-to-end workload scheduling under active Preemption Limits.

### Graduation Criteria

#### Alpha

- `PreemptionLimit` CRD is implemented, allowing administrators to declare preemption rate limits for Configurable Preemptions.
- `PreemptionLimit` supports targeting specific `PreemptionConfig`s and `ClusterQueue`s via label selectors.
- Existing behavior for classical and fair-sharing preemptions remains unaffected - they are not limited by `PreemptionLimit` resources.
- Candidates from Configurable Preemptions are not selected if evicting them would violate active `PreemptionLimit`s.
- The feature is guarded by the `ConfigurablePreemptionLimits` feature gate (Alpha, disabled by default).

#### Beta

- Performance impact of enabling `ConfigurablePreemptionLimits` is evaluated and does not lead to significant degradation in candidate selection latency with up to 10 `PreemptionLimit`s in the cluster.
- User documentation explains Preemption Limits, how they are configured, and how they can be used to reduce risk during `PreemptionConfig` rollouts.
- `PreemptionLimit` supports limiting specific rules within a `PreemptionConfig` (rule-level rate limiting).
- Any bugs discovered during Alpha are resolved.

#### Stable

TBD

## Implementation History

1. v0.21: Implementation of Alpha version of `PreemptionLimit` CRD and limit enforcement:
   - CRD and RBAC definitions for `PreemptionLimit`.
   - `PreemptionEvaluator` updates to respect preemption limits.
   - In-memory tracking of preemption events.
   - Status reporting with `counts` and `lastUpdateTimestamp` via `PreemptionLimitReconciler`.


## Drawbacks

- **Increased PreemptionEvaluator complexity**: Evaluating limits introduces additional logic into the preemption path, partially mitigated by isolating limit-related logic in a dedicated subpackage with high test coverage.
- **In-memory state footprint**: Tracking timestamps for preemption events consumes memory, partially mitigated by periodic trimming of expired timestamps.
- **CRD operational overhead**: Introduces an additional custom resource (`PreemptionLimit`) for cluster administrators to manage.

## Alternatives

1. **Hardcoded or flag-based preemption limits for all Configurable Preemptions**:

   Rejected because cluster-wide flags do not allow granular scoping by ClusterQueue, Workload, or PreemptionConfig, nor do they support safe, progressive rollouts of individual configurations or rules.

2. **Specifying limits directly within the `PreemptionConfig` CRD**:

   Rejected because `PreemptionConfig` defines candidate selection rules for specific queues. Placing limits inside it would make global cluster-wide limits, cross-queue limits, or queue-level protections either impossible or unintuitive.

3. "Bucket"-based tracking for in-memory events similar to how QPS works.

   Track preemption allowances using a bucket counter periodically incremented up to the maximum limit, instead of tracking individual timestamps across a sliding window.
   - **Advantages**: Smaller in-memory footprint and simpler implementation.
   - **Drawbacks**: Can lead to scenarios where up to $2 \times N - 1$ preemptions occur within the defined preemption window (consuming a full bucket of $N$ allowances, followed by additional preemptions as the counter is periodically incremented over that window).

   Rejected because allowing the number of preemptions to reach up to $2\times$ the configured limit within the defined preemption window would be confusing for users and is not expected.

4. Using `limitWindowDuration` (`metav1.Duration`) instead of `limitWindowSeconds`.

   - **Advantages**: Better UX for manual configuration and manipulation in YAML manifests (e.g., writing `"1h"` or `"5m"` instead of `3600` or `300`).
   - **Drawbacks**: Discouraged by KAL (Kubernetes API Review guidelines) because of ambiguous values and lack of support for Go duration formats in client libraries across other programming languages.

   Rejected to comply with standard Kubernetes API conventions favoring explicit integer unit fields (such as `*Seconds`).

## Future Work Ideas

### Rule-Level Rate Limiting via `ruleNames`

To enable safe evolution of `PreemptionConfig`s, a `ruleNames` field can be added to `PreemptionLimitSpec`, allowing administrators to restrict a limit to a specific subset of rules within matching `PreemptionConfig`s.

This enables cluster administrators to define strict rate limits for a new or experimental rule to restrict its blast radius, and then relax or remove the limit once its stability in the cluster is verified.

The addition to `PreemptionLimitSpec` would look like the following:

```go
	// ruleNames restricts the limit to specific rule names within matching PreemptionConfigs.
	// If not set, it applies to all rules.
	//
	// +optional
	// +listType=set
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:items:MinLength=1
	// +kubebuilder:validation:items:MaxLength=63
	// +kubebuilder:validation:items:Pattern="^[a-z0-9]([-a-z0-9]*[a-z0-9])?$"
	RuleNames []string `json:"ruleNames,omitempty"`
```

#### Multi-Rule Attribution Challenge and Alternatives Considered

A key design challenge arises when a candidate workload matches multiple rules in a `PreemptionConfig`, especially when only some of the rules have associated preemption limits or remaining budget. Several alternative approaches were considered:

1. **Requiring available limits in all matching rules**:
   - *Drawback*: Would not allow for a stable, safe rollout of new rules. If an existing stable rule permits preemption without limits, adding a new experimental rule with a strict limit would cause preemptions otherwise allowed by the stable rule to be blocked whenever the experimental limit is reached.
2. **Consuming from only those rules that currently have limits available**:
   - *Drawback*: Leads to poor user experience because the number of consumed limits and the attribution behavior would non-deterministically differ depending on whether multiple limits or only one are available at the exact moment of preemption.

#### Proposed Approach: First Matching Rule Consumes the Limits

The proposed approach is to **always choose the first matching rule** from the `PreemptionConfig` (according to the order in `spec.rules`) to evaluate and consume the limits.

- **Deterministic behavior**: Using the first matching rule provides a predictable priority order for limit consumption based on the administrator's rule sequence.
- **Maintainable conditions**: While this approach requires an update to the condition reporting defined in Configurable Preemptions (which originally concatenated all matching rules in the preemption condition message), it leads to more maintainable code as always exactly one rule is unambiguously responsible for the preemption and for consuming the limit.

### Pod-Level Rate Limiting via `mode`

Currently, `PreemptionLimit` rate-limits preemption events strictly at the workload level. However, workloads can vary dramatically in size—from single-pod jobs to distributed training workloads spanning hundreds or thousands of pods. Preempting a large distributed workload produces significantly more node and scheduling churn than preempting a single-pod job.

To provide finer-grained guardrails against mass pod disruptions, a new field called `mode` can be introduced to `PreemptionLimitSpec`:

```go
// PreemptionLimitMode specifies the counting unit for preemption events.
// Possible values are:
// - "Workloads": limits the number of preempted workloads (default).
// - "Pods": limits the total number of pods belonging to preempted workloads.
//
// +kubebuilder:validation:Enum=Workloads;Pods
// +kubebuilder:default=Workloads
type PreemptionLimitMode string

const (
	// PreemptionLimitModeWorkloads limits the number of preempted workloads.
	PreemptionLimitModeWorkloads PreemptionLimitMode = "Workloads"

	// PreemptionLimitModePods limits the total number of pods across preempted workloads.
	PreemptionLimitModePods PreemptionLimitMode = "Pods"
)
```

The addition to `PreemptionLimitSpec` would look like the following:

```go
	// mode specifies the counting unit for preemption events.
	// Defaults to "Workloads".
	//
	// +optional
	// +kubebuilder:default=Workloads
	Mode PreemptionLimitMode `json:"mode,omitempty"`
```

- When set to `Workloads` (default), the number of preempted workloads will be limited.
- When changed to `Pods`, it can guardrail the total number of pods that can be preempted in a particular scope within the defined sliding window. The preemption evaluator will calculate the total number of pods across candidate workloads (summing pod counts across their pod sets) and prevent preemptions if evicting them would exceed the allowed pod limit.

### Reconstructing Approximate In-Memory State on Restart from Status

As described in [Caveats](#caveats) (Caveat 2), preemption events are tracked strictly in-memory and are reset whenever the Kueue controller restarts. This can temporarily allow a burst of preemption events up to the configured limit immediately following a restart (such as a controller pod upgrade, node drain, or leader election failover).

To mitigate this limitation without incurring the performance penalty of persisting every individual preemption event to etcd, an extension can reconstruct an approximate in-memory event state on startup from the previously persisted status saved in etcd (`PreemptionLimit.status`).

The reconstruction will work as follows:

1. **Status Update Timestamps**:
   `PreemptionLimitStatus` includes `lastUpdateTimestamp`, which is updated periodically by `PreemptionLimitReconciler` (e.g. approximately every minute) whenever aggregate counts are refreshed and persisted to etcd.

2. **Synthesizing Artificial In-Memory Events on Startup**:
   Instead of resetting limit counters entirely to zero upon controller startup:
   - The controller inspects each `PreemptionLimit`'s `status.counts` and `status.lastUpdateTimestamp`.
   - For each scoped entity entry present in `status.counts` (e.g. `"Global"`, ClusterQueue name, or `namespace/name`), the controller artificially creates in-memory preemption events matching the persisted count value.
   - Each synthesized event is assigned a timestamp equal to `lastUpdateTimestamp` from the status for the appropriate limit and connected scope values.

3. **Natural Expiration via Sliding Window**:
   - These synthesized events are inserted into the in-memory tracking map and immediately consume available quota under the limit.
   - The existing sliding window logic remains unchanged: as time advances past controller startup, these artificial events will naturally age out and be periodically trimmed once `time.Now() - lastUpdateTimestamp >= limitWindowSeconds`.
   - If the controller was offline longer than `limitWindowSeconds`, all synthesized events will already be expired upon startup and will be pruned immediately, cleanly granting a fresh budget.

#### Advantages

- **Zero evaluation overhead**: By relying on the periodic status reconciler rather than synchronous per-event etcd writes, this mechanism provides cross-restart resilience while maintaining zero latency overhead during the scheduling and preemption evaluation path.
- **Protection against post-restart eviction bursts**: Workloads and ClusterQueues are protected from sudden bursts of preemptions immediately following controller upgrades, node restarts, or failovers.

#### Trade-offs

- **Conservative approximation**: Because individual event timestamps are not persisted (only the periodic aggregate count and `lastUpdateTimestamp`), all pre-restart events are conservatively treated as having occurred at `lastUpdateTimestamp`. If some events actually occurred earlier within the window, this may slightly over-restrict preemptions for a short interval following startup. However, this prioritizes safety by preventing preemption spikes across controller restarts.
