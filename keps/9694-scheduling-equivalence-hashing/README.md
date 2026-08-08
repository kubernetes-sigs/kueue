# KEP-9694: Scheduling Equivalence Hashing

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [Terminology](#terminology)
  - [User Stories](#user-stories)
    - [Deep mixed-resource queue](#deep-mixed-resource-queue)
  - [Overview](#overview)
  - [Notes, Constraints, and Caveats](#notes-constraints-and-caveats)
    - [Queueing strategy](#queueing-strategy)
    - [ClusterQueue scope](#clusterqueue-scope)
    - [Disabled Feature gate](#disabled-feature-gate)
    - [Concurrent Admission](#concurrent-admission)
  - [Risks and Mitigations](#risks-and-mitigations)
    - [Incomplete scheduling shape](#incomplete-scheduling-shape)
      - [ElasticJobsViaWorkloadSlices](#elasticjobsviaworkloadslices)
      - [UsageBasedAdmissionFairSharing](#usagebasedadmissionfairsharing)
    - [Overly broad failure classification](#overly-broad-failure-classification)
    - [Stale failed-class records](#stale-failed-class-records)
    - [Identifier collision](#identifier-collision)
    - [Latency for very large Workloads](#latency-for-very-large-workloads)
    - [Reduced diagnostics for bypassed Workloads](#reduced-diagnostics-for-bypassed-workloads)
    - [Additional memory and queue work](#additional-memory-and-queue-work)
- [Design Details](#design-details)
  - [Equivalence Class Construction](#equivalence-class-construction)
  - [Recording and Bulk Movement](#recording-and-bulk-movement)
  - [Observability](#observability)
  - [Notable Changes by Version](#notable-changes-by-version)
    - [v0.15.6, v0.15.7, v0.16.3, v0.16.4, and v0.17.0](#v0156-v0157-v0163-v0164-and-v0170)
    - [v0.18.0](#v0180)
    - [v0.19.0](#v0190)
    - [v0.20.0](#v0200)
  - [Test Plan](#test-plan)
    - [Unit Tests](#unit-tests)
    - [Integration Tests](#integration-tests)
    - [End-to-End Tests](#end-to-end-tests)
  - [Graduation Criteria](#graduation-criteria)
    - [Beta](#beta)
    - [Stable](#stable)
- [Implementation History](#implementation-history)
<!-- /toc -->

## Summary

Scheduling Equivalence Hashing reduces repeated scheduling work in deep
BestEffortFIFO ClusterQueues. Kueue hashes selected scheduling-relevant Workload
properties to form equivalence classes. After one representative Workload is
fully evaluated and cannot be admitted for an allowlisted failure reason, the
other Workloads in the same equivalence class can be moved to the inadmissible
pool without repeating the same expensive evaluation.

The optimization allows the scheduler to reach Workloads deeper in the queue
when many earlier Workloads have identical requirements. It is most effective
at large queue depths when resource requirements and scheduling constraints
recur across many Workloads. Repeated Topology Aware Scheduling evaluations can
increase the savings because placement evaluation is more expensive.

## Motivation

A BestEffortFIFO ClusterQueue may contain many Workloads that request the same
resources and have the same placement constraints. If one of those Workloads
cannot fit, evaluating every equivalent Workload often produces the same result while
consuming scheduler cycles. Periodic or event-driven requeues can repeatedly
return the same large group to the active heap before the scheduler reaches a
different Workload that could use available capacity.

This behavior is particularly harmful in deep queues. A sequence of large GPU
Workloads can repeatedly fail while smaller CPU Workloads that would fit remain
farther down the queue. Repeated evaluations also require repeated scheduler
snapshot construction. For Topology Aware Scheduling, the snapshot and
placement work can be more expensive than the quota decision itself.

The number of distinct scheduling shapes is commonly much smaller than the
number of pending Workloads. Treating one fully evaluated Workload as the
representative of its equivalence class changes repeated scheduling work from
being proportional to the number of Workloads toward being proportional to the
number of distinct scheduling shapes.

### Goals

- Avoid repeated full evaluations of Workloads that are equivalent for the
  relevant scheduling decision.
- Allow BestEffortFIFO queues to make progress past large groups of equivalent
  inadmissible Workloads.
- Preserve queue ordering, sticky preemption behavior, and the distinction
  between BestEffortFIFO and StrictFIFO.
- Account for effective resource requests and key placement constraints in the
  scheduling shape.
- Restrict grouped deferral to an explicit allowlist of failure reasons intended
  for class-wide handling.

### Non-Goals

- Introduce or change a user-facing API.
- Introduce a dedicated failure reason.
- Treat every form of inadmissibility as a reason to defer equivalent Workloads.
- Add per-Workload events as part of the current design.

## Proposal

### Terminology

- Equivalence class: A set of Workloads in the same ClusterQueue whose
  scheduling-relevant shapes produce the same deterministic identifier.
- Representative: A Workload that is evaluated normally and whose outcome may
  be used to defer equivalent Workloads.
- Failed-class record: Volatile, ClusterQueue-scoped state indicating that a
  representative of an equivalence class recently failed for an allowlisted
  reason. The record also carries a user-facing reason when one is available.
- Bulk move: The transition of matching Workloads from the active heap to the
  inadmissible pool after their representative fails.
- Bypassed Workload: A Workload placed in the inadmissible pool because its
  equivalence class has a failed-class record, without a full scheduling
  evaluation of that Workload.

### User Stories

#### Deep mixed-resource queue

As a cluster administrator, I want a BestEffortFIFO queue containing thousands
of identical unschedulable accelerator Workloads to avoid blocking smaller
Workloads that can use other available resources.

### Overview

Kueue leverages a deterministic identifier for each pending Workload to group
Workloads by selected inputs used by flavor assignment and placement.

In BestEffortFIFO, a NoFit or PreemptionNoCandidates result for one
representative can defer redundant evaluations of the other Workloads in the
group. When relevant scheduling conditions change, the previous conclusion is
invalidated so affected Workloads can be considered again.

### Notes, Constraints, and Caveats

#### Queueing strategy

The optimization applies only to BestEffortFIFO ClusterQueues.

StrictFIFO intentionally retains its normal head-of-line behavior. Skipping a
StrictFIFO head based on another Workload's result could weaken strict ordering
and alter when later Workloads become eligible.

Workloads kept sticky while preemption or migration is in progress remain on
the existing sticky path. The class-wide shortcut is not applied to
PendingPreemption or PendingMigration, so it does not bypass a Workload with a
viable assignment, preemption plan, or migration plan.

#### ClusterQueue scope

The equivalence cache is scoped to a ClusterQueue. Workloads in different
ClusterQueues are not compared, even if their scheduling shapes are identical.
This avoids sharing conclusions across different quota, flavor, cohort, and
policy contexts.

#### Disabled Feature gate

Disabling the feature gate causes Workloads to use the unknown class and makes
all equivalence optimizations no-ops after the controller restarts with the new
setting.

#### Concurrent Admission

When Concurrent Admission is enabled, allowed Resource Flavor restrictions are
included in the scheduling shape. Workloads restricted to different flavor sets
therefore remain in separate equivalence classes.

### Risks and Mitigations

#### Incomplete scheduling shape

The identifier is only as complete as the scheduling inputs included in its
shape. If an input used by flavor assignment or placement is omitted, Workloads
with different scheduling outcomes can share a class. An allowlisted failure
from the representative can then defer a schedulable member without evaluating
it, and retries can repeat the same incorrect deferral. Including an irrelevant
input has the less severe effect of splitting equivalent Workloads and reducing
optimization opportunities.

The current scheduling shape includes the Pod placement shape and effective
resource requests, but it does not model every input used by all scheduling
paths. The following independent cases depend on additional inputs.

##### ElasticJobsViaWorkloadSlices

When a Workload is a scale-up replacement, the scheduler evaluates it relative
to the admitted Workload Slice it replaces. The additional quota needed is the
difference between the new and replaced slices, and the replacement must retain
the ResourceFlavor assignments of the replaced slice.

The scheduling shape includes the new Workload's effective requests, but not
the replacement target or its state. For example, suppose a ClusterQueue has
2 CPUs available and two new Workloads each request a total of 10 CPUs. A
Workload replacing a slice that already reserves 8 CPUs needs 2 additional CPUs
and can fit. A Workload replacing a slice that reserves 4 CPUs needs 6 additional
CPUs and cannot fit. The two new Workloads can have the same identifier even
though only one can be admitted. A NoFit result from the latter can therefore
defer the former without evaluating it.

##### UsageBasedAdmissionFairSharing

When UsageBasedAdmissionFairSharing is combined with
LowerOrNewerEqualPriority, queue selection and preemption eligibility use
different ordering rules. LowerOrNewerEqualPriority permits an equal-priority
Workload to be preempted only when it is newer than the pending Workload. The
scheduling shape includes the effective priority, but not the queue-order
timestamp used by this policy.

UsageBasedAdmissionFairSharing orders Workloads by LocalQueue usage before
their timestamps. It can therefore evaluate a newer member of an equivalence
class before an older member. For example, consider an older pending Workload A,
an equal-priority admitted Workload V created after A, and a newer pending
Workload B created after V. If B belongs to a lower-usage LocalQueue, B can be
evaluated first. B cannot preempt the older V and can receive
PreemptionNoCandidates, while A can preempt V because V is newer than A. If B is
the representative, the same identifier can cause A to be deferred without
evaluating it.

Until these inputs are modeled or the affected paths are excluded from
class-wide decisions, a matching identifier is not proof that all members have
the same outcome.

Every new scheduling input must undergo the same review: represent it in the
scheduling shape or exclude the affected outcome from class-wide handling.

#### Overly broad failure classification

Some failures depend on namespace, admission-check state, transient preemption
state, or a single Workload's lifecycle. Applying those outcomes to a whole
class can incorrectly defer valid Workloads.

Queue handling therefore receives an explicit requeue reason, and only NoFit
and PreemptionNoCandidates create failed-class records. NamespaceMismatch,
PreemptionGated, Generic, FailedAfterNomination, and other reasons retain their
normal individual handling. In particular, a preemption-gated Workload must be
processed individually so the scheduler can maintain its gate condition. This
includes gates used by ConcurrentAdmission and MultiKueueOrchestratedPreemption.

Encoding gate state or a per-Workload identifier in the scheduling shape was
considered during Beta graduation. Excluding unsafe outcomes by requeue reason
was chosen instead, so new scheduler outcomes must explicitly opt in to
class-wide handling.

#### Stale failed-class records

Stale cached Workload information can affect scheduling even when equivalence
hashing is disabled, so that risk is not specific to this feature. Equivalence
hashing adds a failed-class record, which can become stale after quota, flavor,
topology, node, admission-check, or Workload state changes.

Failed-class records are discarded whenever the affected inadmissible pool is
retried. A process restart also discards them. Workload changes refresh the
cached information and recompute that Workload's class membership.

#### Identifier collision

The internal identifier is the first 16 hexadecimal characters, or 64 bits, of
a SHA-256 digest. Assuming digest prefixes are uniformly distributed, the
birthday-bound probability of at least one accidental collision among `n`
distinct scheduling shapes in one ClusterQueue is approximately
`n(n-1)/(2 * 2^64)`. The relevant count is the number of distinct shapes, not
the number of pending Workloads. At 50,000 distinct shapes, the probability is
about 6.8e-11, or 1 in 15 billion. Even at 1 million distinct shapes, it is about
2.7e-8, or 1 in 37 million. These estimates cover accidental collisions rather
than a deliberate search for colliding inputs.

A collision cannot cause an incorrect admission because the optimization never
grants quota or reuses an assignment. It can, however, cause repeated delay or
starvation: retry invalidation clears the failed-class record but does not
separate two shapes with the same deterministic identifier. The same
representative can fail again and defer the other shape on every retry.

#### Latency for very large Workloads

Bulk movement exchanges repeated scheduling cost for queue scanning and batched
retry latency. When many equivalent Workloads each consume nearly all
ClusterQueue capacity, bulk movement can make them wait for the batched
inadmissible retry. Their individual admission latency can increase even while
total queue throughput improves.

The scheduler performance runs shared during v0.18 graduation illustrated this
tradeoff:

**Baseline (15,000 Workloads)**

| Metric | Hash off | Hash on | Delta |
| --- | ---: | ---: | ---: |
| `wallMs` | 368,568 | 354,086 | -3.9% |
| `large` | 11,918 | 15,615 | +31% |
| `medium` | 83,107 | 67,461 | -19% |
| `small` | 233,131 | 217,133 | -6.9% |

**Large-scale (50,000 Workloads)**

| Metric | Hash off | Hash on | Delta |
| --- | ---: | ---: | ---: |
| `wallMs` | 1,148,840 | 1,137,344 | -1.0% |
| `large` | 75,245 | 78,353 | +4.1% |
| `medium` | 233,600 | 231,851 | -0.7% |
| `small` | 684,797 | 676,737 | -1.2% |

Source: Performance tables created by
[@sohankunkerkar](https://github.com/sohankunkerkar) and shared in the
[v0.18 graduation discussion][performance-results].

Enabling the feature reduced total wall-clock time by 3.9% in the
15,000-Workload run and by 1.0% in the 50,000-Workload run, while admission
latency for the largest Workloads increased by 31% and 4.1%, respectively.
Those Workloads consumed nearly all ClusterQueue capacity and waited for the
one-second batched retry. These results are workload-shape dependent rather
than performance guarantees.

[performance-results]: https://github.com/kubernetes-sigs/kueue/pull/11097#discussion_r3226876641

#### Reduced diagnostics for bypassed Workloads

A bypassed Workload does not run a scheduling attempt and therefore cannot
produce the same detailed resource or placement diagnosis as an individually
evaluated Workload. The `kueue_pending_scheduling_hashes` metric reports
aggregate class counts and does not provide a per-Workload diagnosis.

The failed-class record retains the representative's high-level reason. The
controller applies that reason to a bypassed Workload only when it has no more
specific active scheduler diagnosis, so an existing detailed message is
preserved.

#### Additional memory and queue work

Each pending Workload retains an equivalence identifier, and each recently
failed class retains a small record. Bulk movement scans active-heap entries
to find matching members.

The scheduler and queue manager gain another form of shared in-memory state and
must keep it synchronized with queue membership, Workload updates, retry
notifications, and observability.

The failed-class state is bounded by the number of distinct classes observed
between retries and is discarded with the retry cycle. The queue scan replaces
multiple substantially more expensive scheduling evaluations.

## Design Details

### Equivalence Class Construction

For a class-wide conclusion to be sound, the scheduling shape must include every
Workload property used by the relevant flavor-assignment and placement decision,
while excluding metadata that does not affect the decision. Including irrelevant
identity fields would split otherwise equivalent Workloads and reduce the
benefit without improving correctness. The known gaps in the current shape are
listed under Risks and Mitigations.

The scheduling shape includes the effective Workload priority and an ordered
description of every PodSet. Each PodSet description includes:

- `name` and effective `count`
- `minCount`
- effective `requests` after Kueue-side processing
- scheduling-relevant fields from `template.spec`, including
  `initContainers[].resources.requests`, `initContainers[].ports`,
  `containers[].resources.requests`, `containers[].ports`, `resources.requests`,
  `nodeSelector`, `affinity`, `tolerations`, `runtimeClassName`, `priority`,
  `topologySpreadConstraints`, `overhead`, and `resourceClaims`
- `Workload.spec.podSets[].topologyRequest`, read directly from the Workload
  spec rather than Job annotations or Workload status

The scheduling-relevant fields in `template.spec` come from the shared Pod spec
shape. The Pod group integration uses the same shape when calculating the
`kueue.x-k8s.io/role-hash` annotation. Integrations that implicitly enable the
Pod integration, such as StatefulSet and LeaderWorkerSet, also use it when
validating `PodTemplateSpec` identity.

PodSet name is included conservatively even though the current flavor-assignment
path does not use it. This can split otherwise equivalent Workloads into
different classes, reducing optimization opportunities, but it cannot merge
different scheduling shapes incorrectly.

Effective resource requests are included separately from the original Pod
shape. They can differ after reclaimable Pod accounting, resource
transformations, Dynamic Resource Allocation preprocessing, defaulting, or
other Kueue-side calculations. The class must reflect the values actually used
for admission decisions.

Kueue serializes the scheduling shape as JSON. The encoder sorts map keys while
PodSets and other slices retain their order. Kueue computes SHA-256 over that
JSON, encodes the digest in hexadecimal, and uses the first 16 characters, or
64 bits, as the identifier. If serialization fails, Kueue uses the unknown
class, which is evaluated individually and never participates in bulk movement.

Kueue computes the identifier when it creates cached scheduling information for
a Workload and recomputes it whenever a relevant Workload update refreshes that
information.

Before a ClusterQueue retries its inadmissible Workloads, Kueue clears that
ClusterQueue's failed-class records.

### Recording and Bulk Movement

Equivalence hashing adds a ClusterQueue-scoped in-memory record containing the
failed class identifier and high-level reason. Creating the record bulk-moves
matching active Workloads to the inadmissible pool. Matching Workloads added or
updated while the record exists are placed there directly.

The record is created when the representative is requeued, rather than waiting
for each equivalent Workload to be popped. Because the scheduler normally
nominates one head Workload per ClusterQueue in a cycle, the record remains
useful across cycles and avoids later heap pops and scheduling snapshot
construction for the rest of the class.

### Observability

The representative can receive the full diagnostic result of its scheduling
attempt. Bypassed Workloads do not run that attempt and initially had no way to
explain why they entered the inadmissible pool.

The controller applies the bypass reason only when the Workload has no more
specific active scheduler diagnosis. A previously evaluated Workload with a
detailed resource or placement message retains that message.

When SchedulingEquivalenceHashing is enabled, Kueue exposes the
`kueue_pending_scheduling_hashes` gauge. It reports the number of unique pending
equivalence identifiers for each `cluster_queue` and `status` (`active` or
`inadmissible`), with `replica_role` as an additional label. The series is not
reported when the feature gate is disabled.

Operators should use the pending equivalence-class count, rather than the
pending Workload count, when estimating whether the scheduler can reach the end
of a queue before the next inadmissible retry. One representative failure can
move an entire class without evaluating its remaining Workloads.

### Notable Changes by Version

#### v0.15.6, v0.15.7, v0.16.3, v0.16.4, and v0.17.0

Scheduling Equivalence Hashing was introduced in v0.15.6 and v0.16.3. In
BestEffortFIFO ClusterQueues, a NoFit result recorded the failed equivalence
class and bulk-moved matching Workloads to the inadmissible pool.

PreemptionNoCandidates began participating in class-wide handling in v0.15.7,
v0.16.4, and v0.17.0. At that point, failed-class recording was driven by the
broader non-immediate requeue signal rather than explicit outcomes. In v0.17.0,
the priority input in the scheduling shape changed from the raw Workload
priority to the effective priority, including priority adjustments when that
behavior is enabled.

#### v0.18.0

Failed-class recording changed from the broad non-immediate requeue signal to
an explicit allowlist containing NoFit and PreemptionNoCandidates. This retained
class-wide handling for those two outcomes while excluding other requeue paths.

Allowed Resource Flavor restrictions and effective resource requests were added
to the scheduling shape.

The Beta feature gate became enabled by default.

#### v0.19.0

Failed-class records began retaining the representative's high-level reason so
bypassed Workloads could expose it without replacing an existing detailed
diagnosis.

The equivalence identifier was corrected to include Pod-level resource requests
when the Pod-level resources field is set.

#### v0.20.0

Observability was extended with the `kueue_pending_scheduling_hashes` gauge,
which reports unique active and inadmissible equivalence classes per
ClusterQueue when the feature is enabled.

### Test Plan

[x] The owners of the involved components understand that existing tests may
need updates as scheduling inputs and feature interactions evolve.

This retrospective KEP adds no product code. Existing tests cover the core hash,
queue transitions, failure-reason allowlist, retry invalidation, deep-queue
progress, and bypass observability. The known input combinations described in
Risks and Mitigations are not fully automated today. Future changes must
preserve and extend the following coverage.

The PreemptionNoCandidates integration scenario validates the scheduling end
state. Unit coverage of the requeue reasons validates whether the equivalence
optimization is triggered.

#### Unit Tests

Unit coverage must exercise these scenarios:

- Workloads that should share a scheduling outcome form one class, while a
  scheduling-relevant difference keeps them separate.
- An eligible representative failure defers equivalent Workloads only in
  BestEffortFIFO, while other failures and StrictFIFO retain normal
  per-Workload behavior.
- Workload updates and retry-triggering cluster changes refresh class
  membership and invalidate prior failed-class conclusions.
- Known unmodeled inputs are covered when resolved, whether the resolution adds
  them to the shape or excludes their outcomes from class-wide handling.
- A bypass reason can be propagated without replacing a more specific existing
  diagnosis.

#### Integration Tests

Integration coverage must exercise a deep BestEffortFIFO queue in which a large
group of equivalent, unschedulable Workloads precedes a schedulable Workload.
It must also verify that relevant cluster-state changes retry the deferred group
and that namespace, preemption, and observability boundaries retain their
existing behavior. Before stable, it must exercise the resolved behavior for
Workload-slice replacement and LowerOrNewerEqualPriority under usage-based
admission fair sharing.

#### End-to-End Tests

Dedicated end-to-end coverage is not required because the feature has no API or
external component boundary. Unit and integration tests exercise the internal
class construction, queue transitions, retry events, and selected feature
interactions directly.

### Graduation Criteria

#### Beta

Beta requires:

- explicit restriction to NoFit and PreemptionNoCandidates
- safe fallback for unknown identifiers
- BestEffortFIFO-only bulk movement
- retry-time invalidation of all failed-class records
- effective request and flavor-restriction coverage in the class
- unit and integration coverage for the main success and exclusion paths
- a supported feature-gate rollback.

#### Stable

Graduation to stable requires:

- reevaluation of class-wide outcomes for ElasticJobsViaWorkloadSlices and a
  decision to either include the replacement target and relevant state in the
  scheduling shape or exclude affected Workloads from class-wide handling
- reevaluation of PreemptionNoCandidates class-wide handling when
  UsageBasedAdmissionFairSharing is combined with LowerOrNewerEqualPriority,
  and a decision to either include the queue-order timestamp in the scheduling
  shape or exclude this combination from class-wide handling
- validated retry triggers for quota, cohort, flavor, topology, admission
  checks, and relevant Pod-capacity changes
- an explicit decision on whether the current digest size is sufficient for
  expected scale
- sufficient observability to distinguish individual evaluation from an
  equivalence bypass.

## Implementation History

- 2026-03-06: Initial implementation was merged for development toward v0.17.0.
  - [#9698: Initial implementation](https://github.com/kubernetes-sigs/kueue/pull/9698)
- 2026-03-09: The initial behavior was backported to the v0.15 and v0.16 patch
  releases.
  - [#9769: v0.16 backport](https://github.com/kubernetes-sigs/kueue/pull/9769)
  - [#9771: v0.15 backport](https://github.com/kubernetes-sigs/kueue/pull/9771)
- 2026-03-17 through 2026-03-19: Scheduler integration and queue handling were
  hardened.
  - [#9120: Effective priority](https://github.com/kubernetes-sigs/kueue/pull/9120)
  - [#10001: Queue integration fix](https://github.com/kubernetes-sigs/kueue/pull/10001)
- 2026-05-04 through 2026-05-22: Beta hardening, graduation, and follow-up
  coverage were completed.
  - [#10910: Concurrent Admission](https://github.com/kubernetes-sigs/kueue/pull/10910)
  - [#11097: Beta graduation](https://github.com/kubernetes-sigs/kueue/pull/11097)
  - [#11399: Effective requests](https://github.com/kubernetes-sigs/kueue/pull/11399)
- 2026-07-02 through 2026-07-08: Correctness, diagnostics, and internal
  improvement.
  - [#12334: Pod-level resources](https://github.com/kubernetes-sigs/kueue/pull/12334)
  - [#12821: Bypass observability](https://github.com/kubernetes-sigs/kueue/pull/12821)
  - [#12871: Strongly typed identifiers](https://github.com/kubernetes-sigs/kueue/pull/12871)
- 2026-08-07: Metrics and the KEP were added.
  - [#12520: Pending scheduling hashes metric](https://github.com/kubernetes-sigs/kueue/pull/12520)
  - [#13973: KEP](https://github.com/kubernetes-sigs/kueue/pull/13973)
