# KEP-14477: Configurable Workloads Per ClusterQueue

## Summary

This proposal allows Kueue's scheduler to pop and process multiple workloads from a single ClusterQueue in a single scheduling cycle, removing the current hardcoded one-pop-per-cycle limit. The number of workloads popped is configured globally via the `workloadsPerClusterQueue` field in the Scheduler Configuration API.

## Motivation

Kueue's scheduler evaluates active ClusterQueues in a loop, currently popping exactly one Workload from each. While this is simple and ensures strict round-robin fairness when queues are shallow, it artificially throttles scheduling throughput for deep queues, particularly when there are fewer active ClusterQueues than the scheduler's potential evaluation capacity in a cycle.

### Goals
- Introduce a configurable setting for the maximum number of workloads popped per ClusterQueue in a scheduling cycle.
- Default the value to 1 to maintain backward compatibility.
- Safely manage multiple in-flight workloads per ClusterQueue without race conditions or memory leaks.

## Proposal

We add `workloadsPerClusterQueue` (int) to the `Scheduler` struct in `apis/config/v1beta1` and `v1beta2`.

```yaml
apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
scheduler:
  workloadsPerClusterQueue: 1
```

Internally, `PendingWorkloads` bookkeeping is refactored from tracking a single `inflight` pointer to tracking multiple in-flight workloads in a keyed map. The scheduler retrieves workloads using `PopN`, which allows iterating and assigning multiple workloads per queue.

## Design Details

- **PendingWorkloads Refactor**: Changed `inflight *workload.Info` to `inflight map[workload.Reference]*workload.Info`.
- **schedulingHashCounts Refactor**: Tracks multiple `inflight` hashes in a map to properly report metric lengths and support concurrent inflight hashes.
- **ClusterQueue PopN API**: `PopN(n int) []*workload.Info` pulls `n` elements from the active heap and tracks them as inflight. `Pop()` is left as a wrapper.
- **Scheduler Heads Generation**: `Manager.heads()` loops over popped items and queues them together.
