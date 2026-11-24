# KEP-16450: Configurable Inadmissible Workload Requeue Batch Period

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [Terminology](#terminology)
  - [User Stories](#user-stories)
    - [Story 1: Workloads behind the head of a large BestEffortFIFO ClusterQueue wait for hours](#story-1-workloads-behind-the-head-of-a-large-besteffortfifo-clusterqueue-wait-for-hours)
  - [Risks and Mitigations](#risks-and-mitigations)
    - [Admins may set a long batch period and delay admission after quota frees up](#admins-may-set-a-long-batch-period-and-delay-admission-after-quota-frees-up)
- [Design Details](#design-details)
  - [API](#api)
  - [Validation](#validation)
  - [Handling](#handling)
  - [Interactions](#interactions)
  - [Test Plan](#test-plan)
    - [Prerequisite testing updates](#prerequisite-testing-updates)
    - [Unit tests](#unit-tests)
    - [Integration tests](#integration-tests)
    - [e2e tests](#e2e-tests)
  - [Graduation Criteria](#graduation-criteria)
    - [Beta](#beta)
    - [Stable](#stable)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
  - [Reintroduce the SchedulerLongRequeueInterval feature gate](#reintroduce-the-schedulerlongrequeueinterval-feature-gate)
<!-- /toc -->

## Summary

This KEP adds the `inadmissibleWorkloadRequeuer.batchPeriod` field to the Kueue Configuration so that admins of large clusters can set how long kueue-controller-manager waits before it moves inadmissible workloads back to the queue.

## Motivation

Currently, after a [requeue trigger](#terminology) such as a workload finishing, kueue-controller-manager waits 1s and then runs an [inadmissible workload requeue](#terminology) for the affected ClusterQueue.
For a ClusterQueue in a Cohort, the inadmissible workload requeue covers every ClusterQueue in the Cohort tree.
Requeue triggers that arrive during the wait join the same inadmissible workload requeue.

Each inadmissible workload requeue moves every inadmissible workload that is not in backoff back to the queue.
Priority and creation time put the same workloads back at the head, so the scheduler evaluates them again in the next scheduling cycle.
Also, if an inadmissible workload requeue runs while the scheduler evaluates a workload, that workload returns to the queue when its evaluation fails, instead of waiting as inadmissible.
Each inadmissible workload requeue also clears the failed-class records of [KEP-9694](../9694-scheduling-equivalence-hashing/README.md), so the scheduler evaluates equivalent workloads again.

On a large or high-churn cluster with a stream of requeue triggers, an inadmissible workload requeue runs about every second.
In a `BestEffortFIFO` ClusterQueue, the scheduler keeps evaluating the same workloads at the head and does not reach the workloads behind them.

Kueue v0.17 added the Alpha feature gate `SchedulerLongRequeueInterval`, which fixed the batch period at 10s. It was also removed in Kueue v0.20.
However, one fixed value does not fit every cluster, so this KEP makes the value configurable.

### Goals

- Let admins set the cluster-wide batch period in the Kueue Configuration.
- Reject values that remove batching (`0s` or less).

### Non-Goals

- Change the default of 1s.
- Add a periodic resync.
- Set the batch period per ClusterQueue or per Cohort.
- Change which inadmissible workloads an inadmissible workload requeue moves, or the order in which they return to the queue.
  The field makes inadmissible workload requeues less frequent.
  It does not change the fact that each one puts the same workloads back at the head.

## Proposal

Add an optional `inadmissibleWorkloadRequeuer` section with one field, `batchPeriod`, to the Kueue Configuration.
The value is how long Kueue waits from the first requeue trigger to the inadmissible workload requeue.

### Terminology

- **inadmissible workload requeue**: It is the step in which kueue-controller-manager moves inadmissible workloads back to the queue so that the scheduler evaluates them again.
  This KEP uses the qualified term everywhere to tell it apart from the requeue in `waitForPodsReady.requeuingStrategy`, which evicts an admitted workload and puts it back with a backoff.
- **requeue trigger**: It is a change that can make inadmissible workloads admissible (e.g., Workload deletion, ClusterQueue changes).
- **batch period**: It is the wait from the first requeue trigger to the inadmissible workload requeue.
  It is not a resync period.

### User Stories

#### Story 1: Workloads behind the head of a large BestEffortFIFO ClusterQueue wait for hours

A batch admin runs a large cluster where workloads finish every few seconds.
In a `BestEffortFIFO` ClusterQueue with thousands of pending workloads, the scheduler keeps evaluating the same workloads at the head, and the workloads behind them wait for hours.
The admin sets `batchPeriod` to 30s.
Between two inadmissible workload requeues, failed workloads stay inadmissible, so the scheduler moves on to the workloads behind them.

```yaml
apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
inadmissibleWorkloadRequeuer:
  batchPeriod: 30s
```

### Risks and Mitigations

#### Admins may set a long batch period and delay admission after quota frees up

After quota frees up, an inadmissible workload can wait up to `batchPeriod` (at most 10m) before the scheduler evaluates it again.
A newly created workload enters the queue without this wait, unless it has the same Scheduling Equivalence Hash as a workload that failed after the last inadmissible workload requeue.
An inadmissible workload that is updated to a different Scheduling Equivalence Hash also returns to the queue without this wait.
As a result, in a `BestEffortFIFO` ClusterQueue a new workload is more likely to be admitted before an older inadmissible workload.

## Design Details

### API

`Configuration` gets one new field.

```go
type Configuration struct {
	// ...

	// InadmissibleWorkloadRequeuer configures how Kueue requeues inadmissible workloads.
	// +optional
	InadmissibleWorkloadRequeuer *InadmissibleWorkloadRequeuer `json:"inadmissibleWorkloadRequeuer,omitempty"`
}
```

```go
// InadmissibleWorkloadRequeuer defines how Kueue requeues inadmissible workloads.
type InadmissibleWorkloadRequeuer struct {
	// BatchPeriod is how long Kueue waits after an event that may make
	// inadmissible workloads admissible, such as a workload finishing or a
	// ClusterQueue, Cohort, or ResourceFlavor changing, before it requeues the
	// inadmissible workloads of the affected ClusterQueue.
	// Events that arrive during the wait join the same requeue.
	//
	// A longer batch period lets the scheduler reach workloads deeper in large
	// BestEffortFIFO ClusterQueues, where frequent events otherwise put the
	// same inadmissible workloads back at the head in every scheduling cycle.
	//
	// Must be greater than 0s and must not exceed 10m.
	// Defaults to 1s.
	// Requires the ConfigurableInadmissibleWorkloadRequeuer feature gate.
	// +optional
	BatchPeriod *metav1.Duration `json:"batchPeriod,omitempty"`
}
```

### Validation

| `ConfigurableInadmissibleWorkloadRequeuer` | `inadmissibleWorkloadRequeuer` | Result |
| --- | --- | --- |
| false | No section | Accepted, batch period 1s |
| false | Empty section | Accepted, batch period 1s |
| false | `batchPeriod` set to any value | Forbidden, "requires the ConfigurableInadmissibleWorkloadRequeuer feature gate" |
| true | Empty section | Accepted, batch period 1s |
| true | `batchPeriod` greater than `0s` and at most `10m` | Accepted |
| true | `batchPeriod` of `0s` or less | Invalid, "must be greater than 0" |
| true | `batchPeriod` greater than `10m` | Invalid, "must not exceed 10m" |

### Handling

- If the field is unset, the batch period is 1s.
- The wait starts at the first requeue trigger and lasts at least the batch period.
  Requeue triggers that arrive during the wait join the same inadmissible workload requeue.
  Without a requeue trigger, no inadmissible workload requeue happens.
- The existing `kueue_admission_wait_time_seconds` metric shows the effect.

### Interactions

- `SchedulingEquivalenceHashing` ([KEP-9694](../9694-scheduling-equivalence-hashing/README.md)).
  Failed-class records last until the next inadmissible workload requeue.
  A longer batch period keeps them longer, so Kueue skips the evaluation of equivalent workloads for longer.
  This applies only to `BestEffortFIFO` ClusterQueues.
- `StrictFIFO` ClusterQueues.
  A failed workload returns to the queue immediately, except on a namespace mismatch or during backoff, so the field has little effect.

### Test Plan

[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.

#### Prerequisite testing updates

None.

#### Unit tests

Unit tests cover three behaviors.

1. Validation, with one case for each row of the [Validation](#validation) table.
2. Kueue loads a Configuration file that contains `batchPeriod: 30s`.
3. The batch period that Kueue uses is 1s with no section, 1s with an empty section, and 30s with `30s`.

#### Integration tests

The inadmissible workload suite checks that requeue triggers join one inadmissible workload requeue with a 2s batch period.
It will also set the batch period through the new section.

#### e2e tests

Beta adds one e2e test, listed in [Beta](#beta), for kueue-controller-manager reading the value from the Configuration at startup.
The rest of the new behavior is only a change in timing, and integration tests cover it.

### Graduation Criteria

#### Beta

This KEP starts at Beta because an unset field keeps the 1s batch period, so the feature gate changes nothing until an admin sets the field.

- The `ConfigurableInadmissibleWorkloadRequeuer` feature gate is enabled by default.
  An unset field keeps the 1s batch period.
- Validation rejects the values listed in [Validation](#validation).
- An e2e test sets `batchPeriod` and restarts kueue-controller-manager.
  It checks that Kueue starts and admits a Job.

#### Stable

- Reported bugs and user feedback are addressed.

## Implementation History

- 2026-11-23: Initial KEP.

## Drawbacks

None.

## Alternatives

### Reintroduce the SchedulerLongRequeueInterval feature gate

Kueue could reintroduce the `SchedulerLongRequeueInterval` feature gate, which sets the batch period to 10s instead of 1s.
This KEP does not choose it because one fixed value does not fit every cluster.
