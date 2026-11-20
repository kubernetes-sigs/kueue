# KEP-7029: ClusterQueue Maximum Execution Time

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1: forgotten interactive jobs run forever](#story-1-forgotten-interactive-jobs-run-forever)
    - [Story 2: a user wants a shorter limit than the queue](#story-2-a-user-wants-a-shorter-limit-than-the-queue)
    - [Story 3: a user asks for more than the queue allows](#story-3-a-user-asks-for-more-than-the-queue-allows)
  - [Notes/Constraints/Caveats](#notesconstraintscaveats)
  - [Risks and Mitigations](#risks-and-mitigations)
    - [Lowering the limit does not affect existing workloads](#lowering-the-limit-does-not-affect-existing-workloads)
- [Design Details](#design-details)
  - [API](#api)
  - [Validation](#validation)
  - [Handling](#handling)
  - [Test Plan](#test-plan)
    - [Prerequisite testing updates](#prerequisite-testing-updates)
    - [Unit Tests](#unit-tests)
    - [Integration tests](#integration-tests)
      - [controller/jobs/job](#controllerjobsjob)
      - [webhook/core/clusterqueue](#webhookcoreclusterqueue)
    - [e2e tests](#e2e-tests)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
- [Future Extensions](#future-extensions)
  - [LocalQueue-Level Defaults](#localqueue-level-defaults)
  - [Separate default and maximum](#separate-default-and-maximum)
<!-- /toc -->

## Summary

A ClusterQueue can set a maximum execution time.
It is the default for workloads without their own value, and an upper bound for workloads with one.
It builds on [KEP-3125](../3125-maximum-execution-time/README.md).

## Motivation

Administrators want time limits per queue.
Today every job must carry the `kueue.x-k8s.io/max-exec-time-seconds` label,
and an administrator cannot set a default or a limit.
Slurm enforces partition time limits as upper bounds in the same way
([Slurm Resource Limits](https://slurm.schedmd.com/resource_limits.html)).

Requested in:

- https://github.com/kubernetes-sigs/kueue/discussions/6684
- https://github.com/kubernetes-sigs/kueue/issues/6587#issuecomment-3342233971

### Goals

- Let administrators set a maximum execution time on a ClusterQueue.
- Apply it as the default to workloads that do not set their own.
- Do not admit workloads whose own value exceeds it.
- Leave behavior unchanged when the field is unset.

### Non-Goals

- Silently reducing a workload's own value to the ClusterQueue limit.
- Changing scheduling based on the maximum execution time.
- LocalQueue-level defaults (see [Future Extensions](#future-extensions)).

## Proposal

Add `workloadDefaults.maximumExecutionTimeSeconds` to `ClusterQueueSpec`.

- **Default**: a workload created without the label gets this value as `spec.maximumExecutionTimeSeconds`.
- **Upper bound**: a workload whose label value is larger is not admitted and stays pending, with a condition and an event that explain why.

Expiry is still handled by the existing maximum execution time enforcement.

### User Stories

#### Story 1: forgotten interactive jobs run forever

An administrator sets a 1-hour default so that runaway jobs are cleaned up without users setting the label.

```yaml
kind: ClusterQueue
spec:
  workloadDefaults:
    maximumExecutionTimeSeconds: 3600
```

#### Story 2: a user wants a shorter limit than the queue

A user on that queue sets `kueue.x-k8s.io/max-exec-time-seconds: "1800"`.
The job is cleaned up after 30 minutes.

#### Story 3: a user asks for more than the queue allows

A user sets `kueue.x-k8s.io/max-exec-time-seconds: "7200"` on a queue limited to 1 hour.
The workload stays pending, and its condition tells the user to lower the value.

### Notes/Constraints/Caveats

- The default is applied when the workload is created.
  Changing the ClusterQueue value, or moving the job to another LocalQueue, does not update existing workloads.
  A workload that is recreated picks up the current ClusterQueue value.
- Prebuilt workloads are not affected.

### Risks and Mitigations

#### Lowering the limit does not affect existing workloads

Admins may expect a lowered ClusterQueue value to apply to running workloads.
It applies only to new workloads, as described in the notes above; the docs will say so.

## Design Details

### API

```go
type ClusterQueueSpec struct {
    // ...existing fields...

    // workloadDefaults defines values applied to workloads submitted to
    // LocalQueues backed by this ClusterQueue when the workload does not set them.
    // +optional
    WorkloadDefaults *ClusterQueueWorkloadDefaults `json:"workloadDefaults,omitempty"`
}

type ClusterQueueWorkloadDefaults struct {
    // maximumExecutionTimeSeconds is the maximum execution time, in seconds,
    // for workloads submitted to LocalQueues backed by this ClusterQueue.
    // It is the default for workloads without the
    // kueue.x-k8s.io/max-exec-time-seconds label, and the upper bound for
    // workloads with it. When unset, the ClusterQueue sets no default and no limit.
    //
    // +optional
    // +kubebuilder:validation:Minimum=1
    MaximumExecutionTimeSeconds *int32 `json:"maximumExecutionTimeSeconds,omitempty"`
}
```

### Validation

- `maximumExecutionTimeSeconds` must be at least 1.
- `workloadDefaults` can be set only when the `ClusterQueueMaxExecutionTime` feature gate is enabled.
- Both fields are mutable.

### Handling

Precedence for a workload's `maximumExecutionTimeSeconds`:

| Job label | ClusterQueue value | Result |
|---|---|---|
| unset | unset | no limit |
| unset | set | ClusterQueue value |
| set, at most the ClusterQueue value | set | label value |
| set, above the ClusterQueue value | set | workload stays pending |
| set | unset | label value |

**Default.** When kueue-controller-manager creates or rebuilds a workload without a value, it copies the ClusterQueue's value.
If the LocalQueue or ClusterQueue cannot be read, it creates the workload without a default.

**Upper bound.** The check happens at admission, because the ClusterQueue may not be known when the workload is created.
The scheduler does not admit the workload and treats it like other structurally inadmissible workloads:
it is not retried every cycle, and it is re-evaluated when the ClusterQueue's `workloadDefaults` or the workload changes.
The user sees:

- `QuotaReserved=False` with a new reason, `MaximumExecutionTimeExceeded`.
  The existing `Inadmissible` reason means a missing or inactive queue, which is a different failure.
- A condition message and an event, for example
  "requested maximumExecutionTimeSeconds 7200 exceeds ClusterQueue limit of 3600".

To recover, the user lowers the label value.
The workload is then recreated and can be admitted.
Removing the label does not recover it, and the user must delete the workload or Job to get the default.

**Equivalency.** A workload's value may come from the ClusterQueue, so the job and workload values are compared only when the job has the label.

**Feature gate off.** The field is ignored: no default is applied and no limit is enforced.
Workloads that already received a value keep it.

### Test Plan

[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough before committing the changes
necessary to implement this enhancement.

#### Prerequisite testing updates

No regressions in the existing tests.

#### Unit Tests

- `pkg/controller/jobframework`: default lookup and the equivalency check.
- `pkg/controller/jobs/job`: default applied, label takes precedence, equivalency with a ClusterQueue-sourced value.
- `pkg/scheduler`: workload above the limit is not admitted and gets the new reason.

#### Integration tests

##### controller/jobs/job

- A job gets the ClusterQueue value when no label is set.
- A label within the limit takes precedence.
- A label above the limit keeps the workload pending.
- A job moved to another LocalQueue keeps its original value.
- An existing workload is not affected when the ClusterQueue value changes.

##### webhook/core/clusterqueue

- Validation of `maximumExecutionTimeSeconds` (at least 1) and of the feature gate.

#### e2e tests

Not planned. Integration tests cover the behavior, which does not depend on real pods.

### Graduation Criteria

**Alpha**: `ClusterQueueMaxExecutionTime` feature gate, disabled by default; unit and integration tests; documentation.

**Beta**: gate enabled by default; reported bugs fixed; review whether `DefaultTime` and `MaxTime` need separate fields, based on user feedback.

**Stable**: one release in Beta.

## Implementation History

- 2026-04-09: KEP created

## Drawbacks

- Workload creation reads the LocalQueue and ClusterQueue, which are cached.
- All LocalQueues of a ClusterQueue share one value.

## Alternatives

1. **Apply the default in a mutating webhook on Job.**
   Rejected: it modifies the user's Job, and not every integrated job type goes through Kueue's webhooks.
2. **Apply the default in a mutating webhook on Workload.**
   Rejected: there is no Workload mutating webhook, and one field does not justify adding it.
3. **LocalQueue-level default.**
   Rejected for now: most configuration lives on the ClusterQueue, and LocalQueues of one ClusterQueue rarely need different timeouts.
4. **Generic label/annotation defaulting on ClusterQueue.**
   Rejected for now: it needs its own design for precedence, validation and equivalency, with no second use case yet.
   The typed field is simple to validate and can inform a generic design later.
5. **Default only, no upper bound.**
   Rejected: Slurm partition limits cannot be exceeded by jobs, and administrators expect the same.
6. **Silently cap the job's value.**
   Rejected: it changes user intent and can terminate jobs unexpectedly.
   A pending workload with a clear message lets the user fix the request.

## Future Extensions

### LocalQueue-Level Defaults

A `LocalQueueWorkloadDefaults` struct on `LocalQueueSpec` could override the ClusterQueue value for one LocalQueue.
Precedence would be: job label, LocalQueue, ClusterQueue, none.
This needs its own KEP.

### Separate default and maximum

Slurm has [DefaultTime](https://slurm.schedmd.com/slurm.conf.html#OPT_DefaultTime) and [MaxTime](https://slurm.schedmd.com/slurm.conf.html#OPT_MaxTime).
This KEP uses one value for both.
A second field can be added if users ask for it.
