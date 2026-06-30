# KEP-11980: Round-Robin Order for Priority Booster Time Sharing

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [Terminology](#terminology)
  - [User Stories](#user-stories)
    - [Story 1: only the oldest workloads take turns](#story-1-only-the-oldest-workloads-take-turns)
    - [Story 2: an urgent job cuts a turn short](#story-2-an-urgent-job-cuts-a-turn-short)
  - [Notes/Constraints/Caveats](#notesconstraintscaveats)
    - [Round robin can increase the mean completion time](#round-robin-can-increase-the-mean-completion-time)
    - [Workloads of different sizes](#workloads-of-different-sizes)
    - [The booster misses a yield when Kueue reserves quota first](#the-booster-misses-a-yield-when-kueue-reserves-quota-first)
  - [Risks and Mitigations](#risks-and-mitigations)
    - [The switches are split between Kueue and the booster](#the-switches-are-split-between-kueue-and-the-booster)
    - [A user who can write Workloads can change the order](#a-user-who-can-write-workloads-can-change-the-order)
- [Design Details](#design-details)
  - [Kueue](#kueue)
    - [API](#api)
    - [Validation](#validation)
    - [Handling](#handling)
  - [Kueue Priority Booster](#kueue-priority-booster)
    - [API](#api-1)
    - [Validation](#validation-1)
    - [Handling](#handling-1)
  - [Interaction with other features](#interaction-with-other-features)
  - [Test Plan](#test-plan)
    - [Unit tests](#unit-tests)
    - [Integration tests](#integration-tests)
    - [e2e tests](#e2e-tests)
  - [Graduation Criteria](#graduation-criteria)
    - [Alpha](#alpha)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
  - [Evict through a separate AdmissionCheck controller](#evict-through-a-separate-admissioncheck-controller)
  - [Raise the boost of the next workload](#raise-the-boost-of-the-next-workload)
- [Future Work Ideas](#future-work-ideas)
<!-- /toc -->

## Summary

This KEP makes the time sharing of `kueue-priority-booster` (the booster), the reference controller of [KEP-7990](../7990-priority-boost/README.md), take turns in round-robin order.
Kueue adds an optional Workload field that records the time when the Workload yielded its turn, and uses this time to order Workloads within the queue.
In its round-robin mode, the booster writes the requeue time to this field of a Workload that was preempted after its interval.
So that Workload waits behind the Workloads that were already pending, and all Workloads take turns in the order they entered the queue.
This KEP builds on the booster and changes only the order among Workloads of the same priority.

## Motivation

The booster's time sharing sets a negative boost on a Workload that has been admitted for longer than `timeSharing.interval`, so that pending Workloads of the same priority preempt it.
In practice, however, only the few oldest Workloads take turns until they finish, and later Workloads wait until then.
For example, if a, b, c, d and e enter a ClusterQueue that runs one Workload at a time, only a and b take turns: `a b a b …`.

Except in a few cases, Kueue orders pending Workloads of the same priority by creation time, so a preempted Workload that returns to the queue waits ahead of Workloads created after it.
The booster can change only the boost, and the boost changes the priority itself, so it cannot change just the order within the same priority.

When Workloads of the same priority with unknown run times share resources, they need to take turns in round robin, in the order they entered the queue, to share the run time evenly.
In the example above, the order should be `a b c d e a b …`.
This KEP adds round robin to the booster's time sharing.

### Goals

- Round robin works only when an administrator enables it, and otherwise Kueue and the booster behave as they do today.
- Within the same priority, newly submitted Workloads and Workloads that return to the queue after their interval take turns in the order they entered the queue.
- A Workload preempted before its interval ends keeps its place.
- Workloads ordered by a time other than creation time (an eviction or preemption time) are still ordered by that time.
- The order survives restarts of Kueue and the booster.

### Non-Goals

- How Kueue orders Workloads without `spec.yieldTime`, and other Kueue mechanisms (admission, preemption, effective priority).
- The existing behavior of the booster.
- Fairness by Workload size or accumulated run time.
- Time sharing across ClusterQueues.
- Guarantee graceful termination or checkpointing.

## Proposal

Add the Workload field `spec.yieldTime` to Kueue, and add `timeSharing.ordering` to the booster.

`timeSharing.ordering` takes the following values.
- `DefaultOrder` (default): the current behavior of the booster.
- `RoundRobin`: the order this KEP adds.

With `RoundRobin`, time sharing works as follows.
Steps 1 and 2 are the current behavior.
Writing `spec.yieldTime` in step 3 and the ordering in step 4 are what this KEP adds.

1. The booster sets a negative boost on a Workload that has been admitted for longer than the interval.
2. A pending Workload of the same priority preempts it, and the preempted Workload returns to the queue.
3. When the booster removes the negative boost, it writes the time when the Workload returned to the queue (the requeue time) to `spec.yieldTime`.
4. Kueue orders a Workload with `spec.yieldTime` by that time, so the Workload waits behind the Workloads that were already pending.

As a result, Workloads of the same priority take turns in the order they entered the queue.

### Terminology

- **Booster**: `kueue-priority-booster`, the reference controller of [KEP-7990](../7990-priority-boost/README.md) and an experimental controller in the Kueue repository.
- **Yield time**: the time when a Workload preempted after its interval returned to the queue (the requeue time).
- **Round robin**: Workloads of the same priority take turns in the order they entered the queue.
- **Time sharing**: the booster feature that makes Workloads of the same priority take turns using the resources, one interval at a time.
- **Interval**: the time an admitted Workload runs without being preempted by pending Workloads of the same priority.
- **Negative boost**: a negative priority boost that lowers the effective priority of a Workload.

### User Stories

#### Story 1: only the oldest workloads take turns

Four team members each run an experiment, j1, j2, j3 and j4, on shared resources that can run two experiments at a time.
All experiments have the same priority, and nobody knows when they will finish.
The experiments save checkpoints and resume from the last checkpoint when they run again.
The administrator installed the booster so that the experiments take turns every 30 minutes, but only j1, j2 and j3 take turns.
j4, submitted last, waits until one of the earlier experiments finishes, and keeps waiting if none of them finishes.

```yaml
# kueue-controller-manager Configuration
featureGates:
  PriorityBoost: true
  YieldTime: true      # new
---
# kueue-priority-booster configuration
timeSharing:
  interval: "30m"
  negativeBoost: 1
  ordering: RoundRobin # new
---
# ClusterQueue spec
preemption:
  withinClusterQueue: LowerPriority
```

Now the four experiments take turns, 30 minutes each.
The team member who submitted last also continues the experiment on each turn and can check intermediate results regularly.
An experiment submitted later waits behind the experiments that are pending at that time.
In the table below, running lists the running experiments in the order they were admitted, and pending lists the pending experiments in queue order.

| Event | Current | This KEP |
|---|---|---|
| Start | running `j1 j2`<br>pending `j3 j4` | running `j1 j2`<br>pending `j3 j4` |
| j1's interval passes | running `j2 j3`<br>pending `j1 j4` | running `j2 j3`<br>pending `j4 j1` |
| A new experiment j5 is submitted | running `j2 j3`<br>pending `j1 j4 j5` | running `j2 j3`<br>pending `j4 j1 j5` |
| j2's interval passes | running `j3 j1`<br>pending `j2 j4 j5` | running `j3 j4`<br>pending `j1 j5 j2` |
| A new experiment j6 is submitted | running `j3 j1`<br>pending `j2 j4 j5 j6` | running `j3 j4`<br>pending `j1 j5 j2 j6` |
| Later | Only j1, j2 and j3 take turns, and j4, j5 and j6 wait until one of them finishes | All experiments take turns in the order they entered the queue |

#### Story 2: an urgent job cuts a turn short

Kueue, the booster and the ClusterQueue are configured as in Story 1, but the ClusterQueue runs one job at a time.
A regular job l1 is running, and five regular jobs, l2 to l6, are pending.
By policy, the team accepts that an urgent job with a higher priority preempts regular jobs.
About halfway through l1's interval, an urgent job h arrives and preempts l1.

l1 was only pushed out by h before it used up its turn; it did not hand its turn to another regular job.
If `spec.yieldTime` were written on every eviction, l1 would move to the end of the queue, and the queue would be `l2 l3 l4 l5 l6 l1`.
Although l1 ran for only half of its interval, it would have to wait after h finishes until all five jobs finish their turns.

So the booster writes `spec.yieldTime` only for a Workload that used up its interval and has a negative boost.
l1 has no negative boost, so its value does not change.

```yaml
apiVersion: kueue.x-k8s.io/v1beta2
kind: Workload
metadata:
  name: l1
spec:
  yieldTime: "2026-11-20T06:30:05Z" # written by the booster when the previous turn ended; unchanged after h preempts l1
```

The queue is `l1 l2 l3 l4 l5 l6`, and l1 runs again as soon as h finishes.

### Notes/Constraints/Caveats

#### Round robin can increase the mean completion time

For finite Workloads of similar size, round robin increases the mean completion time.

For example, the interval is 30 minutes, the ClusterQueue runs one Workload at a time, and Workloads a, b, c, d and e of the same priority, each taking 1 hour 30 minutes, enter at the same time.
Times are counted from when the Workloads entered.

| | Finish time of a / b / c / d / e | Mean | First run of a / b / c / d / e |
|---|---|---|---|
| Current | 2h30m / 3h / 5h30m / 6h / 7h30m | 4h54m | 0 / 30m / 3h / 3h30m / 6h |
| This KEP | 5h30m / 6h / 6h30m / 7h / 7h30m | 6h30m | 0 / 30m / 1h / 1h30m / 2h |

#### Workloads of different sizes

Round robin only sets the queue order.
It does not change how Kueue admits Workloads.
With BestEffortFIFO, if the head Workload does not fit in the resources freed by one Workload whose interval passed, a smaller Workload behind it is admitted first, as today.
So when Workloads of different sizes are mixed, a large Workload can keep missing its turn.

#### The booster misses a yield when Kueue reserves quota first

If Kueue reserves quota again for a preempted Workload before the booster removes its negative boost, `Evicted` changes to the reason `QuotaReserved` and the requeue time is gone, so the booster only removes the negative boost and does not write `spec.yieldTime`.
If that Workload is preempted again before its new interval ends, it waits by its earlier value and can be ahead of Workloads that were already pending when it yielded.

### Risks and Mitigations

#### The switches are split between Kueue and the booster

The round-robin order applies only when all of the following are in place.
- Kueue: a version with `spec.yieldTime` (including the CRD), with the `YieldTime` gate enabled.
- Booster: a version with `timeSharing.ordering`, set to `RoundRobin`.

So when an administrator enables or disables round robin, the actual order can differ from what they expect.
- Enabling: if any of these is missing, the order stays as today and no error occurs.
  The booster cannot see the Kueue version or feature gates.
- Disabling: if only the booster is set back to `DefaultOrder`, the `spec.yieldTime` values already written remain, and those Workloads are still ordered by those times.

Mitigation: the booster README states that enabling or disabling round robin requires changing both the booster's `ordering` and Kueue's `YieldTime` feature gate.

#### A user who can write Workloads can change the order

`spec.yieldTime` is meant to be written by an external controller such as the booster, but when the `YieldTime` gate is enabled, a user with write access to Workloads can also use this field to change the order within the same priority.
- Writing a later value moves the Workload back.
  The user can also move other users' Workloads back.
- Clearing the value of their own Workload undoes the move by the booster, and the Workload goes back ahead of the Workloads that were already pending.
- Writing a value earlier than the creation time can move the Workload forward.
- On each change, kueue-controller-manager re-sorts that one pending Workload in its queue, as for any other spec change, and evaluates it again if it is inadmissible.

Mitigation: this KEP adds no new permissions.
kueue-controller-manager and the booster work with the permissions they have today, and the users who can write this field are the users who can write Workloads today.
A Job cannot set this field, so users who submit Jobs cannot change it.

## Design Details

Round robin builds on the current time sharing of the booster.
So it needs what the booster requires: the `PriorityBoost` feature gate and `withinClusterQueue: LowerPriority` in the ClusterQueue.
It also needs the `YieldTime` gate and `ordering: RoundRobin`.

### Kueue

#### API

Add the `YieldTime` feature gate and the Workload field `spec.yieldTime` to Kueue.

```go
type WorkloadSpec struct {
	// ...

	// yieldTime is the time when the Workload yielded its turn to other pending Workloads of the same priority.
	// An external controller sets this field, and Kueue does not set or clear it.
	// If this field is set, Kueue uses it in place of metadata.creationTimestamp to order the Workload among pending Workloads of the same priority in its ClusterQueue.
	// Kueue does not use it for a Workload evicted by PodsReadyTimeout.
	// If this field is unset, Kueue orders the Workload by the other rules.
	//
	// This is an alpha field and requires enabling the YieldTime feature gate.
	// +optional
	YieldTime *metav1.Time `json:"yieldTime,omitempty"`
}
```

- The field is in spec because it is desired state that another component sets after creation.
  The name means the time when the Workload yielded its turn, and follows the API convention of naming time fields `somethingTime`.

#### Validation

- Values that are not times are rejected.
  There is no minimum or maximum, because a check against the current time would depend on the clock.
- The field is mutable, because the booster writes it again on each turn.
  Controllers and administrators with write access to Workloads can change it at any time, whether or not the Workload is admitted, and users who submit Jobs cannot change it.
- The API server accepts the field and stores it in the Workload spec even when the gate is disabled.

#### Handling

When the gate is enabled, Kueue orders pending Workloads of the same priority within a ClusterQueue by the first time that applies below.
Rule 4 is new, and the others are the current rules.

1. A Workload evicted by `PodsReadyTimeout`: EvictionTimestamp (when `waitForPodsReady.requeuingStrategy.timestamp` is `Eviction`)
2. A Workload evicted by an admission check: EvictionTimestamp
3. A Workload preempted with `InCohortReclaimWhileBorrowing` when `PrioritySortingWithinCohort` is disabled: the preemption time plus 1 ms
4. A Workload not evicted by `PodsReadyTimeout`: `spec.yieldTime` (when set) (new)
5. CreationTimestamp

- Times have a precision of one second.
  When two times are equal, Kueue orders the Workloads by UID, as today, so the order against a Workload that entered the queue in the same second as the requeue is not defined.
- When the gate is disabled, Kueue does not use the value.
  The value stays on the Workload and is used again when the gate is enabled again.
- When Kueue rebuilds the Workload spec from a changed Job, it keeps `spec.yieldTime`.
- When Kueue deletes a Workload and creates a new one, the new Workload has no `spec.yieldTime`.
  For example, Kueue deletes a single-Pod Workload with its Pod on preemption.
- There is no new condition, event or metric.
  Users can check the order through the pending positions that the visibility API reports.

### Kueue Priority Booster

#### API

Add the `ordering` field to `timeSharing` in the booster configuration.

```go
type TimeSharing struct {
	// Interval and NegativeBoost are unchanged.

	// Ordering selects where a workload waits after it used its Interval and was preempted.
	// - DefaultOrder: when a preempted workload returns to the queue, the booster removes the negative boost.
	// - RoundRobin: in addition to DefaultOrder, the booster sets spec.yieldTime to the time when
	//   the workload's QuotaReserved condition became False, in the same patch that removes the negative boost.
	//   It needs the YieldTime feature gate of kueue-controller-manager.
	// Defaults to DefaultOrder.
	Ordering TimeSharingOrdering `json:"ordering,omitempty"`
}
```

- The default, `DefaultOrder`, is the current behavior of the booster, and `RoundRobin` is the value this KEP adds.
  Without a configuration change, the booster behaves as it does today.

#### Validation

- If `ordering` is set to a value other than `DefaultOrder` or `RoundRobin`, the booster fails to start.

#### Handling

For the Workloads it manages, the booster works as follows.

1. When a Workload is admitted, the booster sets no negative boost until the interval has passed since admission.
2. After the interval, the booster sets the negative boost, and Kueue can then preempt the Workload.
3. When a Workload with the negative boost returns to the queue with the `Evicted` reason `Preempted` (`Admitted` becomes `False`), the booster takes the time when the `QuotaReserved` condition became `False` (the requeue time) as the value of `spec.yieldTime`. (new - RoundRobin)
4. The booster patches the Workload that returned to the queue to clear `spec.priorityBoost`.
5. In the same patch as step 4, the booster writes the value from step 3 to the Workload's `spec.yieldTime` and records a `YieldTimeSet` event. (new - RoundRobin)

- The booster does not clear `spec.yieldTime`, even for Workloads it does not manage.
- Like the current rules, the `RoundRobin` rules look only at one Workload and keep no state in memory.
- The booster cannot see Kueue's `YieldTime` gate, so with `RoundRobin` it writes `spec.yieldTime` even when the gate is disabled.

### Interaction with other features

This KEP changes only the Workload ordering rules, and they apply to the Workloads of all integrations in the same way.

| Feature | Effect |
|---|---|
| Cohort | Round robin does not affect the order of Workloads across ClusterQueues. |
| `withinClusterQueue` | Round robin does not support policies other than `LowerPriority`. |
| BestEffortFIFO | Round robin does not affect keeping a Workload whose preemptions are in progress at the head, or how Kueue admits Workloads ([Workloads of different sizes](#workloads-of-different-sizes)). |
| Admission Fair Sharing | Round robin does not affect the comparison of LocalQueue usage, and applies only to the order among Workloads with equal usage. |
| `waitForPodsReady` | Round robin does not affect the order of a Workload evicted by `PodsReadyTimeout`. |

### Test Plan

[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.

#### Unit tests

- `pkg/workload`: `2026-11-25` - `68.6%`
  - The precedence of `spec.yieldTime` in the ordering, and the gate check.
- `pkg/cache/queue`: `2026-11-25` - `89.8%`
  - Whether the order within a ClusterQueue uses `spec.yieldTime`.
- `pkg/controller/jobframework`: `2026-11-25` - `72.1%`
  - Keeping `spec.yieldTime` when Kueue rebuilds the Workload spec.
- `cmd/experimental/kueue-priority-booster/pkg/controller`: `2026-11-25` - `90.6%`
  - Writing `spec.yieldTime` with `RoundRobin`.
- `cmd/experimental/kueue-priority-booster/pkg/config`: `2026-11-25` - `0.0%`
  - The default and validation of `ordering`.

#### Integration tests

Integration tests check the following behaviors.

- Place after the interval: a Workload preempted after its interval returns to the queue behind the Workloads of the same priority that were already pending.
- New and returning Workloads: newly submitted Workloads and Workloads that return after their interval wait in the order they entered the queue.
- Preemption during the interval: a Workload preempted by a higher-priority Workload during its interval keeps its `spec.yieldTime`, so it returns to its place before the preemption.
- Gate disabled: the API server stores `spec.yieldTime` written by the booster, and Kueue orders the Workload as if the field were not set.
- Workload spec rebuild: when a change of a suspended Job makes Kueue rebuild the Workload spec, `spec.yieldTime` does not change.
- Quota reserved again before the booster: when Kueue reserves quota again before the booster removes the negative boost, the booster removes the negative boost and keeps `spec.yieldTime`.

#### e2e tests

None.
The feature does not depend on Pods, the kubelet or kube-controller-manager, so integration tests cover it.

### Graduation Criteria

#### Alpha

- The feature is behind the `YieldTime` feature gate of Kueue, which is disabled by default.
- The default of the booster's `timeSharing.ordering` is `DefaultOrder`, the current behavior.
- The unit and integration tests in the Test Plan are in place.
- The booster README describes the round-robin configuration and states that enabling or disabling round robin requires changing both the booster's `ordering` and Kueue's `YieldTime` gate.

## Implementation History

- 2026-06-30: Initial KEP ([#12653](https://github.com/kubernetes-sigs/kueue/pull/12653)) proposed `ClusterQueue.spec.ordering`.
- 2026-11-23: KEP rewritten to move the policy to the booster and add the Workload field `spec.yieldTime` to Kueue.

## Drawbacks

- It works against the goal of finishing earlier Workloads first.
  Round robin gives every Workload a turn, so an earlier Workload also waits while other Workloads take their turns and finishes later.
  In that case, use `DefaultOrder`.
- It adds a field to the Kueue API, and the only writer today is the booster, an experimental controller.

## Alternatives

### Evict through a separate AdmissionCheck controller

Implement a controller for an admission check attached to the ClusterQueue.
The controller sets the check of a Workload with reserved quota to `Ready`.
When the interval has passed and Workloads are pending, it sets the check of the longest-running Workload to `Retry` to evict it.
Kueue already orders a Workload evicted by an admission check by its eviction time, so this gives the same order without changing Kueue.

Ruled out because:

- There would be two controllers that do time sharing.
  The booster's time sharing must be disabled for the same ClusterQueue, and the settings are managed separately in two places.
- The controller is in the admission path of every Workload, so when it stops, no new Workload can start.

### Raise the boost of the next workload

The booster sets a positive boost on the Workload whose turn is next, so that this Workload preempts.

Ruled out because:

- It changes the existing behavior of the booster, and it needs memory or a field to remember whose turn is next.
- A positive boost also affects the behavior of Workloads of other priorities.

## Future Work Ideas

- The booster counts the run time of a turn across admissions, instead of counting the interval from each admission.
  Today, if Workloads of a higher priority keep preempting the same Workload more often than once per interval, that Workload never uses up its interval and does not yield its turn.
  This needs changes only in the booster.
- A time boundary that applies round robin only to Workloads created within a recent period.
  This needs changes only in the booster.
- A value of `waitForPodsReady.requeuingStrategy.timestamp` that uses `spec.yieldTime`, so that a Workload evicted by `PodsReadyTimeout` also keeps its round-robin place.
