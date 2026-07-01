# KEP-7029: ClusterQueue Maximum Execution Time

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1](#story-1)
    - [Story 2](#story-2)
    - [Story 3](#story-3)
  - [Notes/Constraints/Caveats](#notesconstraintscaveats)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [API](#api)
    - [ClusterQueue Spec](#clusterqueue-spec)
  - [Controller](#controller)
    - [Workload Creation](#workload-creation)
    - [Admission Enforcement](#admission-enforcement)
    - [Workload Equivalency Check](#workload-equivalency-check)
  - [Precedence Rules](#precedence-rules)
  - [Why Workload, Not Job](#why-workload-not-job)
  - [Specific Field vs. Generic Defaulting](#specific-field-vs-generic-defaulting)
  - [Test Plan](#test-plan)
    - [Prerequisite testing updates](#prerequisite-testing-updates)
    - [Unit Tests](#unit-tests)
    - [Integration tests](#integration-tests)
      - [controller/jobs/job](#controllerjobsjob)
      - [webhook/core/clusterqueue](#webhookcoreclusterqueue)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
- [Future Extensions](#future-extensions)
  - [LocalQueue-Level Defaults](#localqueue-level-defaults)
<!-- /toc -->

## Summary

Add the ability to configure a maximum execution time on a ClusterQueue that
serves as both a **default** and an **upper bound** for workloads submitted to
LocalQueues backed by that ClusterQueue.

When a job is submitted without a `kueue.x-k8s.io/max-exec-time-seconds` label,
the ClusterQueue's value is applied as the workload's
`maximumExecutionTimeSeconds`. When a job *does* specify its own value via the
label, the ClusterQueue's value acts as a ceiling: if the job's value exceeds
the ClusterQueue's limit, the workload is kept in a pending state and is not
admitted.

This is consistent with how Slurm enforces partition-level time limits as upper
bounds that cannot be exceeded by individual jobs ([Slurm Resource Limits](https://slurm.schedmd.com/resource_limits.html)).

This builds on the existing maximum execution time feature introduced in
[KEP-3125](../3125-maximum-execution-time/README.md).

## Motivation

Cluster administrators often want to enforce time limits on jobs submitted to
specific queues. Today, setting the maximum execution time requires each job to
carry the `kueue.x-k8s.io/max-exec-time-seconds` label. This places the burden
on individual users to remember to set the label, and there is no way for an
administrator to enforce a default or maximum timeout at the queue level.

Most Kueue configuration lives at the ClusterQueue level, and administrators
typically manage policies there. Adding the maximum execution time to the
ClusterQueue is consistent with this pattern and addresses two common cases:
ensuring all jobs have a timeout (default), and preventing users from requesting
excessive runtimes (upper bound).

This has been requested by the community in multiple discussions:

- https://github.com/kubernetes-sigs/kueue/discussions/6684
- https://github.com/kubernetes-sigs/kueue/issues/6587#issuecomment-3342233971

### Goals

- Allow cluster administrators to configure a maximum execution time on a
  ClusterQueue via a new field in `ClusterQueueSpec`.
- Automatically apply this value as a default to workloads created for jobs
  submitted to any LocalQueue backed by the ClusterQueue, when the job does not
  specify its own maximum execution time.
- Enforce this value as an upper bound: workloads whose job-specified maximum
  execution time exceeds the ClusterQueue's limit are not admitted and remain
  pending.
- Preserve backward compatibility: when the ClusterQueue does not set this field,
  behavior is unchanged.

### Non-Goals

- Overriding (silently reducing) a job-level timeout to match the ClusterQueue
  limit. When the job exceeds the limit, the workload is kept pending rather
  than having its timeout silently modified.
- Changing the scheduling behavior based on the maximum execution time.
- Adding the default timeout at the LocalQueue level. This may be considered in
  a future enhancement (see [Future Extensions](#future-extensions)).

## Proposal

Add a new optional field `maximumExecutionTimeSeconds` inside a
`workloadDefaults` struct on `ClusterQueueSpec`. This value serves two purposes:

1. **Default**: When a workload is created for a job that does not have the
   `kueue.x-k8s.io/max-exec-time-seconds` label, the workload's
   `spec.maximumExecutionTimeSeconds` is populated with the ClusterQueue's value.

2. **Upper bound**: When a job specifies a maximum execution time via the label
   that exceeds the ClusterQueue's value, the workload is not admitted and
   remains pending. An event and condition are set on the workload to inform the
   user that the requested execution time exceeds the ClusterQueue's limit.

The existing enforcement mechanism in the workload controller
(`reconcileMaxExecutionTime`) handles timeout expiry — no changes are needed
there.

### User Stories

#### Story 1

As a cluster administrator, I want to set a default maximum execution time of
1 hour on a ClusterQueue used for interactive workloads, so that forgotten or
runaway jobs are automatically cleaned up without requiring each user to set
the timeout label on their jobs.

#### Story 2

As a batch user submitting jobs to a queue backed by a ClusterQueue with a
maximum execution time of 1 hour, I want to set
`kueue.x-k8s.io/max-exec-time-seconds` to 1800 (30 minutes) on my job, so that
my job is cleaned up sooner than the queue default.

#### Story 3

As a cluster administrator, I need to enforce a maximum execution time limit on jobs. When a user specifies`kueue.x-k8s.io/max-exec-time-seconds` (e.g., 7200 for 2 hours) on a job submitted to a queue limited to 1 hour, the workload must remain pending, accompanied by a clear message guiding the user to correct their request.

### Notes/Constraints/Caveats

- The default is applied at workload creation time. Existing workloads are not
  retroactively updated in the following scenarios:

  - **ClusterQueue's timeout field is modified**: If an administrator changes the
    `maximumExecutionTimeSeconds` value on a ClusterQueue, existing workloads keep
    their original timeout. Only newly created workloads pick up the new value.

  - **Job moves to a different LocalQueue**: If a user changes the
    `kueue.x-k8s.io/queue-name` label on a Job to point to a different
    LocalQueue (potentially backed by a different ClusterQueue), the workload's
    `QueueName` is updated in-place, but its `MaximumExecutionTimeSeconds` is not
    recalculated from the new ClusterQueue. This is intentional: the timeout was
    a default applied at creation, and changing it mid-flight could be disruptive
    (e.g., a workload admitted with a 1-hour timeout should not suddenly receive
    a 15-minute timeout from the new queue). This is consistent with existing
    behavior where the queue change path only updates the `QueueName` field on
    the workload. If the workload is recreated (e.g., due to job spec changes
    that trigger non-equivalency), the new ClusterQueue's default would apply.

  - **Readmission**: Changing the queue label does not trigger readmission today.
    This is existing behavior, and this KEP does not change it. The timeout
    inherited at creation time persists through queue changes.

- Prebuilt workloads are not affected. They are expected to be fully specified
  externally.

### Risks and Mitigations

- **Risk**: An API call to look up the ClusterQueue during workload
  creation adds latency to the reconcile loop.
  - **Mitigation**: The lookup is a single GET call that only happens once per
    workload creation. The ClusterQueue is likely cached by the
    controller-runtime client cache. The performance impact is negligible.

## Design Details

### API

#### ClusterQueue Spec

Add a new optional `workloadDefaults` field to `ClusterQueueSpec` in both
`v1beta2` and `v1beta1`. This struct groups all default values that a
ClusterQueue can apply to workloads, making it straightforward to add future
defaults (e.g., priority class, TAS configuration) without cluttering the
top-level spec:

```go
// ClusterQueueSpec defines the desired state of ClusterQueue
type ClusterQueueSpec struct {
    // ...existing fields...

    // workloadDefaults defines default values that are applied to workloads
    // submitted to LocalQueues backed by this ClusterQueue when the workload
    // does not already specify them.
    // +optional
    WorkloadDefaults *ClusterQueueWorkloadDefaults `json:"workloadDefaults,omitempty"`
}

// ClusterQueueWorkloadDefaults defines default values that are applied to
// workloads submitted to LocalQueues backed by this ClusterQueue when the
// workload does not already specify them.
type ClusterQueueWorkloadDefaults struct {
    // maximumExecutionTimeSeconds if provided, determines the default maximum
    // time, in seconds, for workloads submitted to LocalQueues backed by this
    // ClusterQueue.
    // This value is used when the job does not already specify a maximum execution
    // time via the kueue.x-k8s.io/max-exec-time-seconds label.
    //
    // +optional
    // +kubebuilder:validation:Minimum=1
    MaximumExecutionTimeSeconds *int32 `json:"maximumExecutionTimeSeconds,omitempty"`
}
```

Since the struct and field names are identical in both API versions, auto-generated
conversion functions handle the `v1beta1 <-> v1beta2` conversion without manual
code.

### Controller

#### Workload Creation

In the job framework reconciler (`pkg/controller/jobframework/reconciler.go`),
after constructing the workload and before creating it, the reconciler checks if
the `ClusterQueueMaxExecutionTime` feature gate is enabled and
`wl.Spec.MaximumExecutionTimeSeconds` is nil. If so, it looks up the
ClusterQueue (resolving the LocalQueue's `clusterQueue` reference) and applies
its `WorkloadDefaults.MaximumExecutionTimeSeconds` value if set.

This logic is applied in two code paths:

1. **`handleJobWithNoWorkload`** — when a new workload is created for a job.
2. **`updateWorkloadToMatchJob`** — when an existing workload is reconstructed to
   match a modified job.

A new helper method on `JobReconciler` performs a GET on the LocalQueue to
resolve the ClusterQueue name, then a GET on the ClusterQueue to retrieve its
`WorkloadDefaults`. The `MaximumExecutionTimeSeconds` is copied into the
workload spec if set.

If either lookup fails (e.g., the queue or ClusterQueue does not exist yet), the
error is logged but workload creation proceeds without the default. This is
consistent with existing behavior where a job can reference a non-existent queue.

#### Admission Enforcement

During the admission cycle, the scheduler checks whether the workload's
`maximumExecutionTimeSeconds` exceeds the ClusterQueue's
`workloadDefaults.maximumExecutionTimeSeconds`. If it does, the workload is not
admitted. Instead:

1. The workload remains in the queue in a pending state.
2. A condition of type `Inadmissible` is set on the workload with a message
   indicating that the requested execution time exceeds the ClusterQueue's limit
   (e.g., "requested maximumExecutionTimeSeconds 7200 exceeds ClusterQueue limit
   of 3600").
3. An event is emitted on the workload to surface the issue to the user.

This check cannot be enforced at workload creation time because the ClusterQueue
assignment may not yet be resolved. The scheduler is the appropriate place
because it already has the ClusterQueue context and performs admission checks.

If the user corrects the job (e.g., by reducing or removing the label), the
workload is recreated through the equivalency check mechanism, and the new
workload can be admitted.

#### Workload Equivalency Check

The existing `EquivalentToWorkload` function compares the workload's
`MaximumExecutionTimeSeconds` against the job's label value. When the timeout
originates from the ClusterQueue (and the job has no label), this comparison
would incorrectly flag a mismatch.

The fix: only compare `MaximumExecutionTimeSeconds` when the job explicitly has
the `kueue.x-k8s.io/max-exec-time-seconds` label. If the label is absent, the
workload's value may have been inherited from the ClusterQueue, and no mismatch
should be reported. The check first looks for the presence of the label on the
job object, and only performs the value comparison if the label exists.

### Precedence Rules

The precedence for `maximumExecutionTimeSeconds` on a workload is:

1. **Job label** (`kueue.x-k8s.io/max-exec-time-seconds`) — highest priority.
   If the job has this label, its value is used, subject to the upper bound.
2. **ClusterQueue default** (`spec.workloadDefaults.maximumExecutionTimeSeconds`)
   — used only if the job does not have the label.
3. **None** — if neither the job nor the ClusterQueue specifies a value, no
   maximum execution time is set on the workload.

The ClusterQueue's value also acts as an **upper bound**:

- If the job label value **≤** ClusterQueue value: the job label value is used.
- If the job label value **>** ClusterQueue value: the workload is not admitted
  and remains pending with an informative condition.
- If no job label is set: the ClusterQueue value is used as the default.

### Why Workload, Not Job

The default is applied to the Workload object, not the user-created Job. This is
a deliberate design decision:

- **No surprise mutations**: Mutating the Job to inject a
  `kueue.x-k8s.io/max-exec-time-seconds` label the user never set would be
  surprising and could interfere with user tooling that reads Job labels.
- **Universal coverage**: Not all job types go through Kueue's mutating admission
  webhooks. Applying the default at the workload level in the reconciler ensures
  it works for every integrated job type without requiring webhook changes.
- **Consistent layering**: The Workload is Kueue's internal representation of a
  job. Applying Kueue-internal defaults there is consistent with how other
  workload fields (e.g., pod set specs, queue name) are populated from the job
  framework reconciler rather than mutated on the original object.

See also the [Alternatives](#alternatives) section for the rejected approaches of
using a mutating webhook on Jobs or Workloads.

### Specific Field vs. Generic Defaulting

This KEP adds a specific `maximumExecutionTimeSeconds` field within
`ClusterQueueSpec.workloadDefaults` rather than a generic label/annotation
defaulting mechanism. The `workloadDefaults` struct provides a natural grouping
for future default fields while keeping each field explicitly typed. The reasons:

- **Specific validation and precedence**: `maximumExecutionTimeSeconds` has
  specific precedence rules (job label > ClusterQueue default > none) and
  interacts with the workload equivalency check. A generic mechanism would need
  its own design to handle precedence, validation, and equivalency for arbitrary
  labels/annotations — adding complexity without a clear second use case.
- **Simpler to validate**: A typed field with `+kubebuilder:validation:Minimum=1`
  is simpler to validate, document, and reason about than a generic string map.
- **Future extensibility**: If more ClusterQueue-level defaults emerge (e.g., from
  [discussion #7129](https://github.com/kubernetes-sigs/kueue/discussions/7129)),
  the pattern established here — specific field with clear precedence rules — can
  inform the design of a generic mechanism. Refactoring from specific to generic
  is straightforward once multiple use cases exist.

See also the [Alternatives](#alternatives) section for the generic defaulting
alternative.

### Test Plan

[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough before committing the changes
necessary to implement this enhancement.

#### Prerequisite testing updates

No regressions in the existing tests.

#### Unit Tests

As needed to provide coverage for the new code.

- `pkg/controller/jobframework`: coverage for the ClusterQueue default lookup
  and the updated `EquivalentToWorkload` logic.
- `pkg/controller/jobs/job`: test cases covering ClusterQueue default applied,
  job label taking precedence, and equivalency passing with ClusterQueue-sourced
  timeout.

#### Integration tests

##### controller/jobs/job

Add "A job gets the ClusterQueue default maximum execution time when no label is set"

Add "A job label maximum execution time takes precedence over ClusterQueue default when within limit"

Add "A job label maximum execution time exceeding ClusterQueue limit keeps workload pending"

Add "A job moving to a different LocalQueue keeps the original timeout"

Add "An existing workload is not affected when the ClusterQueue's timeout is modified"

##### webhook/core/clusterqueue

Add validation tests for the new `maximumExecutionTimeSeconds` field (must be >= 1).

### Graduation Criteria

This feature is gated behind the `ClusterQueueMaxExecutionTime` feature gate.

## Implementation History

- 2026-04-09: KEP created

## Drawbacks

- Adds an additional API call (ClusterQueue GET) during workload creation.
  However, this is a single cached read and the cost is negligible.
- All LocalQueues backed by a ClusterQueue share the same default. More granular
  per-LocalQueue defaults are not included in this KEP but could be added later
  (see [Future Extensions](#future-extensions)).

## Alternatives

1. **Mutating webhook on Workload**: Instead of applying the default in the
   reconciler, a mutating admission webhook could intercept Workload creation and
   set the default. This was rejected because Workloads do not currently have a
   custom mutating webhook, and adding one for this single field introduces
   unnecessary architectural complexity.

2. **Mutating webhook on Jobs**: Apply the `kueue.x-k8s.io/max-exec-time-seconds`
   label to the Job itself during admission. This was rejected because it modifies
   the user's Job object, which may be unexpected, and it does not work for job
   types that are not managed by Kueue's webhooks.

3. **LocalQueue-level default**: Instead of the ClusterQueue, set the default at
   the LocalQueue level. This provides more granular per-queue control but is
   inconsistent with the existing pattern where most configuration lives at the
   ClusterQueue level and the LocalQueue acts primarily as a pointer. Multiple
   LocalQueues backed by the same ClusterQueue rarely need different timeout
   policies. If per-LocalQueue overrides are needed in the future, they can be
   added as a follow-up (see [Future Extensions](#future-extensions)).

4. **Generic label/annotation defaulting on ClusterQueue**: Instead of a specific
   `maximumExecutionTimeSeconds` field, add a generic mechanism for ClusterQueues
   to default arbitrary labels or annotations onto workloads. This was considered
   but rejected for now because it introduces significant design complexity
   (precedence rules, validation, equivalency interactions) without a clear second
   use case. If additional ClusterQueue-level defaults are needed in the future,
   the pattern established by this KEP can inform a more generic design. See
   [Specific Field vs. Generic Defaulting](#specific-field-vs-generic-defaulting)
   for the full rationale.

5. **Default only (no upper bound)**: The ClusterQueue value could serve purely
   as a default, allowing jobs to specify any value (including longer timeouts)
   without restriction. This was rejected after reviewing how Slurm handles
   partition-level time limits: Slurm enforces the partition limit as an upper
   bound that cannot be exceeded, even by jobs with QOS or association limits.
   This prevents runaway resource consumption and is the expected behavior for
   administrators setting queue-level time limits.

6. **Silently cap (reduce) the job's timeout**: Instead of rejecting workloads
   that exceed the limit, the ClusterQueue's value could be silently applied as
   the effective timeout. This was rejected because silently changing the user's
   intent could lead to unexpected job terminations. Keeping the workload pending
   with a clear message is more transparent and gives the user the opportunity to
   correct their request.

## Future Extensions

### LocalQueue-Level Defaults

The `ClusterQueueWorkloadDefaults` pattern established in this KEP can be
extended to LocalQueues to allow more granular per-queue defaults. A
`LocalQueueWorkloadDefaults` struct with the same shape could be added to
`LocalQueueSpec`, introducing a three-level precedence chain:

1. **Job label** — highest priority.
2. **LocalQueue default** — overrides the ClusterQueue default for a specific queue.
3. **ClusterQueue default** — applies to all LocalQueues backed by the ClusterQueue.
4. **None** — no default.

This would address use cases where different LocalQueues backed by the same
ClusterQueue need different timeout policies (e.g., an interactive queue with a
short timeout vs. a batch queue with a longer timeout). This extension would
require its own KEP to define the precedence semantics and interaction with the
ClusterQueue-level defaults.
