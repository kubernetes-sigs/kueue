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
    - [Story 1 - Safe rollout of experimental PreemptionConfig or new preemption rule](#story-1---safe-rollout-of-experimental-preemptionconfig-or-new-preemption-rule)
    - [Story 2 - Protecting a workload from too many disruptions](#story-2---protecting-a-workload-from-too-many-disruptions)
    - [Story 3 - Global Preemption Rate Limiting](#story-3---global-preemption-rate-limiting)
    - [Story 4 - Protecting a Mission-Critical ClusterQueue from Preemption](#story-4---protecting-a-mission-critical-clusterqueue-from-preemption)
  - [Caveats](#caveats)
  - [Risks and Mitigations](#risks-and-mitigations)
    - [Performance degradation](#performance-degradation)
    - [Security considerations](#security-considerations)
- [Design Details](#design-details)
  - [Observability When Reaching Preemption Limits](#observability-when-reaching-preemption-limits)
  - [Test Plan](#test-plan)
    - [Prerequisite testing updates](#prerequisite-testing-updates)
    - [Unit tests](#unit-tests)
    - [Integration tests](#integration-tests)
    - [e2e tests](#e2e-tests)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
- [Future Work Ideas](#future-work-ideas)
  - [Rule-Level Rate Limiting via `ruleNames`](#rule-level-rate-limiting-via-rulenames)
  - [Pod-Level Rate Limiting via `mode`](#pod-level-rate-limiting-via-mode)
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

## Motivation

While `PreemptionConfig` provides declarative candidate selection policies, cluster administrators also need rate-limiting guardrails to rollout new configurations safely, prevent cascading preemptions, eviction storms, and cluster instability during large-scale rescheduling.

### Goals

1. Safe introduction of new `PreemptionConfig`s or rules within `PreemptionConfig`.
2. Increase of cluster stability by rate-limiting of preemptions.
3. Definition of most common fields that can be used to limit preemptions.

### Non-Goals

1. One advanced set of predefined `PreemptionLimit`s to fullfil every possible rate limiting use case.
2. Defaults for rate limiting current classical/fair sharing preemptions. (As those would be breaking)
3. Automatic addition of labels to existing Kueue resources (ClusterQueues, PreemptionConfigs).

## Proposal

This KEP proposes a new preemption rate limiting mechanism - for now supported only in Configurable Preemptions, but it can be extened also to the classic or fair sharing preemptions in the future.

Relevant capabilities include:

1. **Global rate-limiting**: Restrict the total number of preemption events across the entire cluster within a sliding time window.
2. **Preemptor ClusterQueue rate-limiting**: Throttle preemptions triggered by workloads originating from a specific ClusterQueue.
3. **Preempted ClusterQueue protection**: Limit or block preemptions targeting workloads belonging to a specific ClusterQueue (e.g., setting `limit: 0` to make mission-critical or hero queues non-preemptible).
4. **Preempted Workload churn limiting**: Restrict how many times an individual workload can be preempted within a given time window to avoid starvation, ping-pong eviction loops or bullying of particular workload.

### User Stories

#### Story 1 - Safe rollout of experimental PreemptionConfig or new preemption rule

Mistakes in `PreemptionConfigs` can lead to serious consequences like mass evictions or cluster instability. By introducing rate-limiting of preemptions connected to a new `PreemptionConfig` or a new rule within existing config, can limit the blast radius of such misconfigurations. Cluster admins can at first set a small limit to a new rule/config and slowly increase it to the desired level, while observing cluster stability.

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

#### Story 3 - Global Preemption Rate Limiting

Rate-limit global preemptions to at most 10 evictions across the entire cluster in any 5-minute sliding window to avoid too many disruptions in the cluster:

```yaml
spec:
  scope: "Global"
  limit: 10
  limitWindowSeconds: 300
```

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

### Caveats

1. **User-managed resource labeling**: As ClusterQueues right now do not have a constrained resource name to 63 characters and Kueue currently does not auto add labels to the Cluster Scope CRDs (K8s does this for Namespaces), this is up to the users to label cluster queues and appropriate preemption configurations themselves. This might be changed in the future to make the user experience better, but remains out of the scope of this KEP.
2. **Reset on controller restarts**: Preemption events are tracked strictly in-memory within the controller cache and are not persisted in etcd. Consequently, if the Kueue controller restarts (e.g. due to pod restart, failover, or upgrade), preemption limits will be reset, temporarily allowing preemption events up to the configured limit immediately following the restart.
3. **Increasing time window**: Because expired preemption timestamps are dynamically trimmed from the in-memory map based on the active sliding window, if a user updates a `PreemptionLimit` to increase `limitWindowSeconds`, events that occurred prior to the previously configured window will have already been pruned and will not be retroactively taken into account.

### Risks and Mitigations

As this feature is targeted to derisk Configurable Preemptions usage which is currently in Alpha it will be at first introduced only for the Configurable Preemptions. Thereby, as it will not in any way interfere with well matured preemption algorithms, the risks have small blast radius, and can be always mitigated by disabling Configurable Preemptions or Configurable Preemption Limits (via the `ConfigurablePreemptions` and `ConfigurablePreemptionLimits` feature gates).

#### Performance degradation

Since this will increase complexity of configurable preemptions algorithm by checking if preemptions are allowed according to the limits, preemption performance monitoring is required to maintain appropriate level of preemption performance.

The risk will be mitigated by:

- `PreemptionLimit`s are completely optional and can be disabled by removing them or disabling the `ConfigurablePreemptionLimits` feature gate,
- performance testing before `ConfigurablePreemptionLimits` is graduated to Beta,
- short circuts in the implementation (e.g. if Global preemption limit is already reached for the particular PreemptionConfig, we do not need to check candidates for it at all),
- efficient implamentation of in memory tracking of past preemptions.

#### Security considerations

As preemption limits will be modifiable only by cluster administrators, there are no security risks introduced by this feature. Administrators modifying them should be aware of the risks and consequences of misconfiguration in Kueue, which can lead to workloads not being scheduled and effectively blocked or cascading preemptions.

## Design Details

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
PreemptionLimitSpec defines the desired state of PreemptionLimit
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
	// counts is periodically updated, for reference only.
	// Restricted to the top 1000 counts to fit within CRD size limits.
	//
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=1000
	Counts []PreemptionLimitCount `json:"counts,omitempty"`
}
```

PreemptionLimit limits the number of preemptions that happen for the specified set of rules. The preemption evaluator evaluates proposed preemptions against defined limit objects, allowing them to proceed only if adequate preemption quota remains. If a preemption is in the scope of multiple limits, quota must exist in all of them.
To track this, a list of preemption rule names responsible for selecting each candidate must be maintained.

To manage this data, Kueue will store a comprehensive preemption map in memory, isolated per PreemptionLimit. This map tracks all preemption event timestamps under a specific CQ/workload key, capturing events that occurred within the designated `LimitWindowSeconds`. Moreover, it tracks only events that are in the scope of the specific limit; if a preemption does not match the defined config or rules selector, it will not be tracked in that particular instance of the preemption map. This list is dynamically trimmed upon each retrieval to filter out expired timestamps.

Because tracking is strictly maintained in-memory and dynamically trimmed:
- **Reset on controller restarts**: Events are not persisted in etcd; restarting the Kueue controller resets the in-memory map, so preemption limit counters start fresh from zero.
- **Increasing time window**: If `LimitWindowSeconds` is increased, previous preemption events that occurred before the previously active window have already been discarded and will not be taken into account.

Furthermore, the status of the PreemptionLimit is refreshed periodically — approximately every minute — to write the aggregated totals into the count map (restricted to the top 1000 counts to fit within CRD size limits).

### Observability When Reaching Preemption Limits

When preemption is throttled or blocked due to an exhausted `PreemptionLimit`:

1. **Workload Condition**: A condition with type `PreemptionBlockedByLimit` (reason `PreemptionLimitExceeded`) is assigned to the preemptor Workload, with an informative message indicating which limit blocked admission (e.g. `"Preemption was blocked by PreemptionLimit <limit-name>"`).
2. **Kubernetes Events**: A Kubernetes `Event` with reason `PreemptionThrottled` is emitted on both the preemptor Workload and its ClusterQueue.
3. **Structured Audit Logging**: Informational/debug log entries are recorded specifying the limit name, scope, and affected entities for operator troubleshooting.

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
Majority of the new code will be added as a separate subpackage in preemptions called "limits",
and in the new controller handling the preemption limits periodical updates.
These two components will have all of the public methods covered with unit tests.

Appart from the new subpackage the following existing components will receive additional unit tests for new behavior only if the FG is enabled:
1. Preemption Evaluator:
	- Selects only candidates that do not violate any of the limits.
	- Returns information which limits where consumed by the selected candidates.
2. `Preemptor.IssuePreemptions`:
	- Records the configurable preemption events for tracking preemption limits in memory.
	- Records preemption events only once if preemption is issued successfully.
3. Scheduler:
  - Recomputes preemption candidates in case of conflicting limits from many workloads (same as for overlapping preemption targets).


#### Integration tests

1. New preemption limits are respected by configurable preemptions candidate selection if FG is enabled.
2. Preemption Configuration can be safely rolled out, by changing its limits.
3. Preemptions from Confgiurable Preemptions can be blocked for specific candidate cluster queue. 


#### e2e tests

1. Cluster admin can create/update/get/delete a `PreemptionLimit` CRD.
2. Cluster admin can view the status of the `PreemptionLimit` CRDs.
3. Batch users cannot manage `PreemptionLimit` CRDs out of the box.
4. Extension of Configurable Preemptions e2e tests to cover a basic Preemption Limits scenario.

### Graduation Criteria

#### Alpha

- `PreemptionLimit` CRD is implemented allowing to specify the desired limits for configurable preemptions.
- `PreemptionLimit` allows narrowing down limits to specific `PreemptionConfig`s.
- Existing behavior of not limiting classic and fairs sharing preemptions is maintained.
- Candidates from configurable preemptions are not selected if this would violate `PreemptionLimit`s in the cluster.
- The feature is guarded by the `ConfigurablePreemptionLimits` feature gate (Alpha, disabled by default).

#### Beta

- Performance of enabling `ConfigurablePreemptionLimits` is evaluated and does not lead to significant degradation of preemption candidates selection speed for up to 10 `PreemptionLimit`s in the cluster.
- Public documentation explains Preemption Limits, how can they be configured, and how they can be used to decrease risk of new `PreemptionConfig` rollouts.
- `PreemptionLimit`s support limiting only a subset of rules in the `PreemptionConfig`.
- Any bugs discovered during Alpha are fixed.

#### Stable

TBD

## Implementation History

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

Increase of complexity of the PreemptionEvaluator - can be partially mitigated by the maintaining high quality of the code.

Increased memory footprint by tracking of preemption events for each limit.

Additional CRD to manage by the cluster administrator.

## Alternatives

1. Hardcoded preemption limits or flag based preemption limits for whole Configurable Preemptions.

   Rejected as this does not allow slow rule roll-out.

2. Specifing limits in the `PreemptionConfig` CRD.

   Rejected as this will make global limits either impossible or really unintuitive.

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

To allow safe evolution of `PreemptionConfig`s, it would be best to add a `ruleNames` field to the `PreemptionLimitSpec`, allowing an administrator to restrict a limit to a specific subset of rules within matching `PreemptionConfig`s.

This enables cluster administrators to define strict rate limits for a new or experimental rule, restricting its blast radius while observing its behavior, and then lift or relax the limit once the rule's stability in the cluster is confirmed.

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
