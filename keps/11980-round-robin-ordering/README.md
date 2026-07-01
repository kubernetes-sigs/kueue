# KEP-11980: Support Round-Robin Ordering for Pending Workloads in ClusterQueue

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories (Optional)](#user-stories-optional)
    - [Story 1 (Optional)](#story-1-optional)
    - [Story 2 (Optional)](#story-2-optional)
  - [Notes/Constraints/Caveats (Optional)](#notesconstraintscaveats-optional)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [Test Plan](#test-plan)
    - [Prerequisite testing updates](#prerequisite-testing-updates)
    - [Unit tests](#unit-tests)
    - [Integration tests](#integration-tests)
    - [e2e tests](#e2e-tests)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
<!-- /toc -->

## Summary

This KEP adds optional `ClusterQueue.spec.ordering` to configure how pending
workloads with the same effective priority are ordered within a ClusterQueue.
The default remains FIFO by `creationTimestamp`, while Cluster administrators
can opt into `RoundRobin` ordering to rotate admission opportunities among
same-priority pending workloads.

- `FIFO` (default): order by `creationTimestamp` (current behavior).
- `RoundRobin`: order by the latest `lastTransitionTime` among
  `Workload.status.conditions`. Workloads with a more recent condition
  transition are placed behind those with an older latest transition.

When configured, `roundRobinWindow` limits how long `RoundRobin` applies after
workload creation; older workloads fall back to FIFO by `creationTimestamp`.

## Motivation

By default, Kueue orders pending workloads within a ClusterQueue by
`creationTimestamp`. This gives the earliest-created workload priority and
helps it finish sooner under steady load.

However, when same-priority workloads are repeatedly preempted and requeued,
the workload with the earliest `creationTimestamp` can keep winning admission
while others wait.

```plaintext
1. `job-a` (priority=10) is admitted.
2. `job-b` (priority=10) is queued.
3. `job-c` (priority=100) arrives and preempts `job-a`.
4. `job-a` is requeued ahead of `job-b`.
5. `job-c` completes.
6. `job-a` is admitted again.
7. Another high-priority workload arrives and repeats the cycle.
```

From a FIFO perspective this is reasonable: the oldest workload resumes first.
From a time-sharing perspective, admission opportunities concentrate on one
workload, which may be undesirable for Cluster administrators who want
time-sharing fairness among same-priority pending workloads. This KEP proposes
optional `RoundRobin` ordering within a ClusterQueue.

### Goals

- Add `ClusterQueue.spec.ordering` to configure ordering of same-priority
  pending workloads within a ClusterQueue.
- Support `spec.ordering.mode`: `FIFO` (default, current behavior) or
  `RoundRobin`.
- Support optional `spec.ordering.roundRobinWindow` to apply `RoundRobin` only
  for a bounded period after workload creation.
- When `roundRobinWindow` is set, workloads older than
  `creationTimestamp + roundRobinWindow` fall back to FIFO ordering by
  `creationTimestamp`.

### Non-Goals

- Does not affect ordering across different ClusterQueues.
- Does not affect admission ordering between workloads with different effective
  priorities.
- Does not affect fair sharing or Dominant Resource Share (DRS) calculations.
- Does not affect preemption victim or candidate workload selection.
- Does not change preemption policy evaluation (`LowerPriority`,
  `LowerOrNewerEqualPriority`, etc.).

## Proposal

Cluster administrators set `spec.ordering` on a ClusterQueue to choose FIFO or
`RoundRobin` ordering for same-priority pending workloads.

When set, `roundRobinWindow` bounds how long `RoundRobin` applies after
workload creation. Workloads older than the window are ordered by
`creationTimestamp` (FIFO).

Ordering is applied in the per-ClusterQueue pending heap. The comparator runs
on heap insert/update, which may change relative positions via sift up/down.

### User Stories (Optional)

#### Story 1 (Optional): `RoundRobin` without `roundRobinWindow`

```plaintext
1. `job-a` (priority=10) is admitted.
2. `job-b` (priority=10) is queued.
3. `job-c` (priority=100) arrives and preempts `job-a`.
4. `job-a` is requeued behind `job-b`.
5. `job-c` completes.
6. `job-b` is admitted.
7. `job-d` (priority=100) arrives and preempts `job-b`.
8. `job-b` is requeued behind `job-a`.
9. `job-d` completes.
10. `job-a` is admitted.
11. Repeat steps 3-10.
```

#### Story 2 (Optional): `RoundRobin` with `roundRobinWindow` (30m)

```plaintext
1.  0m - `job-a` (priority=10) is admitted.
2. 10m - `job-b` (priority=10) is queued.
3. 35m - `job-d` (priority=100) arrives and preempts `job-a`.
4. `job-a` is requeued ahead of `job-b` (outside the 30m window, FIFO applies).
5. `job-d` completes.
6. `job-a` is admitted.
```

### Notes/Constraints/Caveats (Optional)

- `RoundRobin` applies only to workloads with the same effective priority.
- `RoundRobin` applies only within a single ClusterQueue pending heap.
- When `roundRobinWindow` is set, workloads outside the window use FIFO
  ordering.
- Default behavior is unchanged (`FIFO`) when `spec.ordering` is unset.
- Uses **Workload** `status.conditions` only (not Job conditions). If no
  conditions exist, `creationTimestamp` is used.
- When `roundRobinWindow` is unset, `RoundRobin` applies indefinitely.

### Risks and Mitigations

- **Risk**: With `RoundRobin` enabled, continuously arriving same-priority
  workloads can reduce admission opportunities for workloads that have been
  pending longer.
  - **Mitigation**: When the current time is past
    `creationTimestamp + roundRobinWindow`, ordering falls back to FIFO so
    older workloads are admitted first. If `roundRobinWindow` is unset,
    `RoundRobin` applies indefinitely as an accepted trade-off.
- **Risk**: A workload already in the pending heap cannot move to the head when
  a configured `roundRobinWindow` expires; heap positions change only on sift
  up/down during heap updates.
  - **Mitigation**: This matches existing Kueue heap behavior. Ordering updates
    on the next insert, requeue, or workload status change that triggers a heap
    fix.

## Design Details

`ClusterQueueSpec` is extended as follows:

```yaml
apiVersion: kueue.x-k8s.io/v1beta2
kind: ClusterQueue
metadata:
  name: team-a-cq
spec:
  ordering:
    mode: FIFO          # default; or RoundRobin
    roundRobinWindow: 30m   # optional; only when mode=RoundRobin
```

`spec.ordering` is optional. When unset, or when `mode` is omitted, the
ClusterQueue uses `FIFO` (current behavior).

### Heap ordering

Implemented in `pkg/cache/queue/cluster_queue.go` (`baseCompareFunc`), with a
helper in `pkg/workload` for `max(lastTransitionTime)` across
`status.conditions`. Not added to global `GetQueueOrderTimestamp`.

```plaintext
1. Sticky status
2. Effective priority (higher first)
3. Ordering timestamp (earlier first):
   - FIFO: creationTimestamp
   - RoundRobin:
     - if roundRobinWindow set and now > creationTimestamp + roundRobinWindow:
       creationTimestamp
     - else: max(lastTransitionTime) across status.conditions, or
       creationTimestamp if none
4. UID
```

### Test Plan

[ ] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes
necessary to implement this enhancement.

#### Prerequisite testing updates

- Lock the current `baseCompareFunc` ordering contract in
  `pkg/cache/queue/cluster_queue_test.go`.
- Add API defaulting/validation tests for `ClusterQueue.spec.ordering`.

#### Unit tests

- `pkg/cache/queue`: `baseCompareFunc` for `FIFO` and `RoundRobin`; window
  fallback; sticky/priority/UID unchanged.
- `pkg/workload`: helper for latest condition `lastTransitionTime`.
- `apis/kueue/v1beta2`: default `mode=FIFO`; validation for `mode` and
  `roundRobinWindow`.

Core packages to extend:

- `pkg/cache/queue`: baseline — existing `cluster_queue_test.go`
- `pkg/workload`: baseline — existing `workload_test.go`
- `apis/kueue/v1beta2`: baseline — API defaulting/validation tests

#### Integration tests

1. **Story 1**: preemption + alternating admission between two same-priority
   workloads.
2. **Story 2**: with a 30m window, workload outside the window is ordered ahead
   of younger same-priority workloads after requeue.
3. **FIFO default unchanged** without `spec.ordering`.

After the implementation PR is merged, add the concrete test function names
here.

#### e2e tests

For Alpha, integration tests are sufficient. For Beta, add one e2e scenario
mirroring Story 1.

### Graduation Criteria

**Alpha:**
- `spec.ordering` API and `RoundRobin` heap comparator.
- Unit and integration tests pass.

**Beta:**
- User feedback from Alpha.
- Optional e2e test.

**GA:**
- Documented in API reference.
- No major issues from Beta.

## Implementation History

- 2026-06-30: Initial KEP drafted (issue #11980).

## Drawbacks

- Cluster administrators must choose `FIFO` vs `RoundRobin` per ClusterQueue.
- Different admission patterns in preemption-heavy environments compared to
  creation-time FIFO.

## Alternatives

- **Global `GetQueueOrderTimestamp` change**: This was rejected because the
  helper is shared by cross-CQ scheduler paths and preemption policy;
  `RoundRobin` belongs in per-CQ `baseCompareFunc` only.
- **`Requeued` condition only**: This was rejected because it does not cover
  pending workloads with only `QuotaReserved/Pending`; latest condition
  `lastTransitionTime` unifies newly queued and requeued workloads.
