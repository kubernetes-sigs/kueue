# KEP-9992: TAS Priority-Aware Non-TAS Pod Capacity

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
  - [Notes/Constraints/Caveats](#notesconstraintscaveats)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [Configuration API](#configuration-api)
  - [Behavior](#behavior)
  - [Test Plan](#test-plan)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
  - [Feature Gate with Hardcoded Negative Priority](#feature-gate-with-hardcoded-negative-priority)
  - [Per-ClusterQueue Priority Threshold](#per-clusterqueue-priority-threshold)
  - [Full kube-scheduler preemptionPolicy Awareness](#full-kube-scheduler-preemptionpolicy-awareness)
<!-- /toc -->

## Summary

When TAS computes available capacity on a node, it subtracts the resource
usage of all non-TAS pods regardless of their Kubernetes pod priority. This
means low-priority pods that kube-scheduler would preempt for a higher-priority workload are still
counted as unavailable. TAS then rejects admission even though the
workload would successfully schedule with preemption.

This KEP introduces a configurable priority threshold in the Kueue
`Configuration` API. When set, non-TAS pods with priority strictly below
the threshold are excluded from TAS capacity calculations, allowing
admission decisions to account for capacity available via kube-scheduler preemption.

## Motivation

It is difficult to run TAS workloads alongside non-trivial amounts of preemptable work.

Known cases:

- **CoreWeave HPC verification pods** (priority -1) on GPU nodes
  ([#9992](https://github.com/kubernetes-sigs/kueue/issues/9992))

### Goals

- Allow TAS to exclude low-priority non-TAS pods from capacity
  calculations via a configurable priority threshold.

- Preserve existing behavior when the threshold is not configured. 

### Non-Goals

- Per-ClusterQueue or per-ResourceFlavor priority thresholds.
- Awareness of the incoming workload's `preemptionPolicy`.
- Kueue-driven preemption of non-TAS pods. Actual preemption remains the responsibility of kube-scheduler.

## Proposal

Add a `topologyAwareScheduling` section to the Kueue `Configuration`
API with a `nonTASPodsPriorityThreshold` field. When the
`TASPriorityAwareNonTASUsage` feature gate is enabled and the threshold is set, non-TAS pods whose `pod.Spec.Priority` is strictly less than
the configured value are excluded from TAS capacity calculations.

### User Stories

**CoreWeave HPC verification.** CoreWeave runs HPC verification pods
at priority -1. With `nonTASPodsPriorityThreshold: 0`, TAS excludes
these pods. Any workload at priority >= 0 sees full node capacity.

**DaemonSet health checks on GPU nodes.** GPU health check DaemonSets
running at very low priority with the expectation of being preempted.
Without this feature, TAS sees insufficient memory on every node and
blocks admission for workloads that would fit after preemption.

### Notes/Constraints/Caveats

- The threshold uses strict less-than: pods with `priority < threshold`
  are excluded, pods with `priority >= threshold` are always counted.
- If the threshold is nil (default), all non-TAS pods are counted,
  preserving current behavior.
- This feature does not guarantee kube-scheduler will actually preempt
  the excluded pods. If the incoming workload has
  `preemptionPolicy: Never` or lower priority than the excluded pods,
  the workload may be admitted but fail to schedule. This is a known
  limitation addressed in [Alternatives](#alternatives).

### Risks and Mitigations

**Risk: Misalignment between TAS threshold and TAS workload**
The workload could either have `preemptionPolicy: Never` or have a Priority value less than the threshold AND the workloads we are ignoring.
This would lead to the workload being admitted and then failing to schedule

*Mitigation:* Kueue's `waitForPodsReady` detects workloads whose pods
do not reach Ready within the configured timeout, evicts them, and
requeues with exponential backoff.

## Design Details

### Configuration API

Add to the Kueue `Configuration` type:

```go
type Configuration struct {
    // ...existing fields...

    // TopologyAwareScheduling holds configuration for TAS behavior.
    // +optional
    TopologyAwareScheduling *TopologyAwareScheduling `json:"topologyAwareScheduling,omitempty"`
}

type TopologyAwareScheduling struct {
    // NonTASPodsPriorityThreshold, when set, excludes non-TAS pods with
    // pod.Spec.Priority strictly less than this value from TAS capacity
    // calculations. This allows TAS to account for capacity that would be
    // reclaimed by kube-scheduler preemption.
    //
    // For example, setting this to 0 excludes only negative-priority pods.
    // Setting this to 1000 excludes pods with priority < 1000.
    //
    // When nil (default), all non-TAS pods are counted toward capacity,
    // preserving existing behavior.
    // +optional
    NonTASPodsPriorityThreshold *int32 `json:"nonTASPodsPriorityThreshold,omitempty"`
}
```

Example configuration:

```yaml
apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
topologyAwareScheduling:
  nonTASPodsPriorityThreshold: 1000
```

### Behavior

When the feature is enabled and the Priority threshold is configured, pods under the threshold are not counted in the non-TAS usage data, allowing TAS pods to be admitted.

### Test Plan

[x] I/we understand the owners of the involved components may require
updates to existing tests to make this code solid enough prior to
committing the changes necessary to implement this enhancement.

- Pods below threshold are excluded from capacity calculations.
- Pods at or above threshold are included.
- Enabled flag + Nil threshold preserves existing behavior (all pods counted).
- Disabled flag preserves existing behavior (all pods counted).
- TAS admits a workload when low-priority non-TAS pods (below
  threshold) occupy capacity on all candidate nodes.
- TAS rejects a workload when non-TAS pods at or above the threshold
  occupy capacity.

### Graduation Criteria

**Alpha (v0.18):**
- Feature gate `TASPriorityAwareNonTASUsage` disabled by default.
- Configuration API field added.
- Unit and integration tests passing.
- Documentation updated.

**Beta:**
- Feature gate enabled by default (nil threshold preserves existing behavior).
- No significant bugs reported during alpha.

**GA:**
- Feature gate locked to enabled.
- At least two releases at beta with no issues.

## Implementation History

- 2026-04-28: KEP created based on discussion in
  [#9992](https://github.com/kubernetes-sigs/kueue/issues/9992).
- Prior work: [Changes by @Huang-Wei](https://github.com/Huang-Wei/kueue/commit/bb27afed0383f3db21c406ff07fd0fe631e6eba6)
  based on v0.16.2 demonstrating this approach.

## Drawbacks

- A global threshold is very blunt. All TAS-enabled
  ClusterQueues see the same filtered capacity, even queues whose
  workloads cannot actually preempt the excluded pods.
- Kueue users must reason about the interaction between the
  threshold and their PriorityClass hierarchy. Misconfiguration can
  lead to workloads being admitted but failing to schedule.

## Alternatives

### Full kube-scheduler preemptionPolicy Awareness

Rather than a configured threshold, TAS would check the incoming
workload's `preemptionPolicy` and `priority` from its PodTemplate and
dynamically exclude non-TAS pods that would be preempted. This is the
cleaner than the current approach as it mirrors what kube-scheduler would actually
do, but is much more complicated. 

This would also allow for much more nuanced deployments than our one-size-fits-all threshold. 
