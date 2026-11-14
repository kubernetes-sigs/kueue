# KEP-16925: Preemption Limits - Future Work

<!-- toc -->
- [Rule-Level Rate Limiting via <code>ruleNames</code>](#rule-level-rate-limiting-via-rulenames)
  - [Multi-Rule Attribution Challenge](#multi-rule-attribution-challenge)
  - [Proposed Approach: First Matching Rule Consumes the Limits](#proposed-approach-first-matching-rule-consumes-the-limits)
  - [Alternatives Considered](#alternatives-considered)
- [Pod-Level Rate Limiting via <code>mode</code>](#pod-level-rate-limiting-via-mode)
- [Reconstructing Approximate In-Memory State on Restart from Status](#reconstructing-approximate-in-memory-state-on-restart-from-status)
  - [Advantages](#advantages)
  - [Trade-offs](#trade-offs)
- [Better Candidate Limiting Heuristic](#better-candidate-limiting-heuristic)
  - [Combining Approaches via Two-Attempt Search](#combining-approaches-via-two-attempt-search)
  - [Advantages and Trade-offs](#advantages-and-trade-offs)
<!-- /toc -->

## Rule-Level Rate Limiting via `ruleNames`

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

### Multi-Rule Attribution Challenge

A key design challenge arises when a candidate workload matches multiple rules in a `PreemptionConfig`, especially when only some of the rules have associated preemption limits or remaining budget.

### Proposed Approach: First Matching Rule Consumes the Limits

The proposed approach is to **always choose the first matching rule** from the `PreemptionConfig` (according to the order in `spec.rules`) to evaluate and consume the limits.

- **Deterministic behavior**: Using the first matching rule provides a predictable priority order for limit consumption based on the administrator's rule sequence.
- **Maintainable conditions**: While this approach requires an update to the condition reporting defined in Configurable Preemptions (which originally concatenated all matching rules in the preemption condition message), it leads to more maintainable code as always exactly one rule is unambiguously responsible for the preemption and for consuming the limit.

### Alternatives Considered

Several alternative approaches for multi-rule attribution were considered:

1. **Requiring available limits in all matching rules**:
   - _Drawback_: Would not allow for a stable, safe rollout of new rules. If an existing stable rule permits preemption without limits, adding a new experimental rule with a strict limit would cause preemptions otherwise allowed by the stable rule to be blocked whenever the experimental limit is reached.
2. **Consuming from only those rules that currently have limits available**:
   - _Drawback_: Leads to poor user experience because the number of consumed limits and the attribution behavior would non-deterministically differ depending on whether multiple limits or only one are available at the exact moment of preemption.

## Pod-Level Rate Limiting via `mode`

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
- When changed to `Pods`, it can guardrail the total number of pods that can be preempted per unit of aggregation within the defined sliding window. The preemption evaluator will calculate the total number of pods across candidate workloads (summing pod counts across their pod sets) and prevent preemptions if evicting them would exceed the allowed pod limit.

## Reconstructing Approximate In-Memory State on Restart from Status

As described in [Caveats](README.md#caveats) (Caveat 2), preemption events are tracked strictly in-memory and are reset whenever the Kueue controller restarts. This can temporarily allow a burst of preemption events up to the configured limit immediately following a restart (such as a controller pod upgrade, node drain, or leader election failover).

To mitigate this limitation without incurring the performance penalty of persisting every individual preemption event to etcd, an extension can reconstruct an approximate in-memory event state on startup from the previously persisted status saved in etcd (`PreemptionLimit.status`).

The reconstruction will work as follows:

1. **Status Update Timestamps**:
   `PreemptionLimitStatus` includes `lastUpdateTime`, which is updated periodically by `PreemptionLimitReconciler` (e.g. approximately every minute) whenever aggregate counts are refreshed and persisted to etcd.

2. **Synthesizing Artificial In-Memory Events on Startup**:
   Instead of resetting limit counters entirely to zero upon controller startup:
   - The controller inspects each `PreemptionLimit`'s `status.topAggregatedCounts` and `status.lastUpdateTime`.
   - For each aggregation unit entry present in `status.topAggregatedCounts` (e.g. `"Global"`, ClusterQueue name, or `namespace/name`), the controller artificially creates in-memory preemption events matching the persisted count value.
   - Each synthesized event is assigned a timestamp equal to `lastUpdateTime` from the status for the appropriate limit and aggregation keys.

3. **Natural Expiration via Sliding Window**:
   - These synthesized events are inserted into the in-memory tracking map and immediately consume available quota under the limit.
   - The existing sliding window logic remains unchanged: as time advances past controller startup, these artificial events will naturally age out and be periodically trimmed once `time.Now() - lastUpdateTime >= windowSeconds`.
   - If the controller was offline longer than `windowSeconds`, all synthesized events will already be expired upon startup and will be pruned immediately, cleanly granting a fresh budget.

### Advantages

- **Zero evaluation overhead**: By relying on the periodic status reconciler rather than synchronous per-event etcd writes, this mechanism provides cross-restart resilience while maintaining zero latency overhead during the scheduling and preemption evaluation path.
- **Protection against post-restart eviction bursts**: Workloads and ClusterQueues are protected from sudden bursts of preemptions immediately following controller upgrades, node restarts, or failovers.

### Trade-offs

- **Conservative approximation**: Because individual event timestamps are not persisted (only the periodic aggregate count and `lastUpdateTime`), all pre-restart events are conservatively treated as having occurred at `lastUpdateTime`. If some events actually occurred earlier within the window, this may slightly over-restrict preemptions for a short interval following startup. However, this prioritizes safety by preventing preemption spikes across controller restarts.
- **Limited to top aggregation units**: Because only the top 100 counts are held in `status.topAggregatedCounts`, this will not avoid preemptions over the limit for the rest of the aggregation level values. To address this, other heuristics—such as creating artificial preemptions based on the ratio of controller downtime to the window duration (`downtime / windowSeconds`)—or a combination of heuristics may be considered.

## Better Candidate Limiting Heuristic

As analyzed in [Limit Enforcement](README.md#limit-enforcement) ([Interaction with Backfilling](README.md#interaction-with-backfilling)) and [Alternatives](README.md#alternatives), both candidate evaluation heuristics have distinct and complementary limitations.

### Combining Approaches via Two-Attempt Search

To achieve higher completeness without introducing combinatorial search complexity, a future enhancement can combine both heuristics by executing two preemption search attempts in sequence:

1. **First Attempt (Greedy Candidate Evaluation)**:
   The search first executes the greedy candidate evaluation algorithm, skipping candidates whose preemption would violate active limits. If this attempt successfully finds a target set that frees enough quota and satisfies all limits, preemption proceeds immediately.

2. **Second Attempt (Fallback with Post-Selection Verification)**:
   If the first attempt fails to find a fitting candidate set, Kueue executes a second attempt using unconstrained candidate accumulation followed by reverse-order backfilling. Once the candidate set is minimized, Kueue verifies whether the resulting target set satisfies all active `PreemptionLimit`s. If the minimized set satisfies the limits, it is accepted.

### Advantages and Trade-offs

- **Higher scheduling completeness**: Combining both approaches resolves the edge cases of each, allowing Kueue to successfully find preemptions in more scenarios.
- **Minimal performance impact**: The second attempt is evaluated strictly as a fallback only when the first attempt fails. Workloads that find valid candidates on the first attempt incur no additional evaluation latency.
