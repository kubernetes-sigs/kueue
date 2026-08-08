# KEP-4803: Workload-level WaitForPodsReady

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1](#story-1)
    - [Story 2](#story-2)
  - [Notes/Constraints/Caveats](#notesconstraintscaveats)
  - [Open Questions](#open-questions)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [API](#api)
    - [Workload Spec](#workload-spec)
    - [Job Annotations](#job-annotations)
  - [Controller](#controller)
    - [Workload](#workload)
    - [Jobs / Jobframework](#jobs--jobframework)
  - [Webhooks](#webhooks)
    - [Workload](#workload-1)
    - [Jobs](#jobs)
  - [Test Plan](#test-plan)
    - [Prerequisite testing updates](#prerequisite-testing-updates)
    - [Integration tests](#integration-tests)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
<!-- /toc -->

## Summary

This proposal introduces a mechanism to allow specification of `WaitForPodsReady` configuration per job.

## Motivation

Currently the `WaitForPodsReady` feature can only be configured at the cluster
level via the Kueue `ConfigMap`. Since different workloads may take different
amounts of time to reach a ready state, a single cluster-wide setting may often
not fit well all workloads.

### Goals

- Introduce per-workload `WaitForPodsReady.Timeout` field in `WorkloadSpec`.
- Define new Job annotations `kueue.x-k8s.io/pods-ready-timeout` that is
  propagated to the corresponding `WorkloadSpec` field at Workload creation time.
- When a workload carries per-workload timeouts, enforce them independently of
  the cluster-wide `WaitForPodsReady` configuration.
- Per-workload timeouts take precedence over the global configuration when
  both are present.

### Non-Goals

- Change the cluster-wide `WaitForPodsReady` behaviour.
- Change the requeue / backoff strategy; that remains cluster-wide only
- Introduce per-workload `BlockAdmission` semantics.

## Proposal

One new optional field is added to `WorkloadSpec`. The job object has the annotation
populated. The workload controller reads the annotation during reconciliation and uses it instead of the global config to determine whether to evict a workload that is taking too long
to become ready.

### User Stories

#### Story 1

As a batch platform administrator, I want to configure a tight pods-ready
timeout for data-preprocessing jobs (2 minutes) while allowing large ML
training jobs a longer timeout (30 minutes), without requiring two separate
Kueue deployments.

#### Story 2

As a job owner whose workload requires pulling a large container image that takes longer than the cluster-wide WaitForPodsReady timeout, I want to annotate my job with a longer per-job timeout so it is not evicted prematurely, without requiring the platform administrator to relax the global timeout for everyone.


### Notes/Constraints/Caveats

- Should we allow combining `WaitForPodsReady` disabled and enforcement of
  per-workload timeout? Maybe it should always be enabled to ensure the
  other `WaitForPodsReady` fields could be enforced.

### Risks and Mitigations

- **Conflicting values**: A user may set a per-workload timeout that conflicts
  with a cluster-wide one.  Mitigation: per-workload values always take
  precedence; a clear precedence rule is documented and enforced in the webhook.

## Design Details

### API

#### Workload Spec

One new optional field is added to `WorkloadSpec`:

```go
// WorkloadSpec defines the desired state of Workload
type WorkloadSpec struct {
    // ...existing fields...

    // podsReadyTimeout defines the maximum duration the workload may remain
    // admitted before all pods are in a Ready or Succeeded state.
    // When elapsed, the workload is evicted with reason PodsReadyTimeout.
    // If both this field and the cluster-wide WaitForPodsReady.Timeout are set,
    // this field takes precedence.
    // +optional
    PodsReadyTimeout *metav1.Duration `json:"podsReadyTimeout,omitempty"`
}
```

#### Job Annotations

One new annotation is defined in `pkg/controller/constants/constants.go`:

```go
// PodsReadyTimeoutAnnotation sets the per-job pods-ready timeout.
// Value must be a Go duration string parseable by time.ParseDuration (e.g. "10m").
PodsReadyTimeoutAnnotation = "kueue.x-k8s.io/pods-ready-timeout"
```

### Controller

#### Workload

`admittedNotReadyWorkload` in `pkg/controller/core/workload_controller.go` is
updated to resolve the timeout by merging the per-workload spec with
the global config.

`reconcileNotReadyTimeout` gains a short-circuit check: if neither the global
config is set nor the workload carries per-workload fields, the function
returns immediately (current behaviour preserved).

The check that decides to write the `WorkloadPodsReady` condition in the job reconciler
(`pkg/controller/jobframework/reconciler.go`) is updated to also check when the
workload carries per-workload timeout.

#### Jobs / Jobframework

`constructWorkload` (in `pkg/controller/jobframework/reconciler.go`) reads the
new annotations from the Job and populates the corresponding `WorkloadSpec`
field.

### Webhooks

#### Workload

- Validate that `podsReadyTimeout`, if set, is greater than zero.
- Field is immutable while the workload is admitted.

#### Jobs

- Validate that `kueue.x-k8s.io/pods-ready-timeout` is a valid duration
  string greater than zero.
- The annotation is immutable while the job is unsuspended.
- (open question) Should the workload be recreated if the annotation value changes
or there should be a webhook that doesn't allow the update?

### Test Plan

[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes
necessary to implement this enhancement.

#### Prerequisite testing updates

No regressions in the existing `WaitForPodsReady` tests under
`test/integration/singlecluster/scheduler/podsready/` and
`test/e2e/sequential/baseline/waitforpodsready_test.go`.

#### Integration tests

##### controller/core/workload

- "The per-workload podsReadyTimeout takes precedence over the cluster-wide
  timeout."

##### controller/jobs/job

- "A Job annotated with kueue.x-k8s.io/pods-ready-timeout has the timeout
  propagated to its Workload spec."

### Graduation Criteria

#### Alpha

- Feature gate `WorkloadLevelWaitForPodsReady` introduced, disabled by default.
- New `WorkloadSpec` fields and Job annotations implemented.
- Unit and integration tests covering the core controller and job framework
  propagation added.

#### Beta

- Feature gate enabled by default.
- E2E tests added.
- Documentation updated.

#### Stable

- No issues reported for two or more releases.
- Feature gate removed; behaviour always on.

## Implementation History

- 2026-07-03: KEP created as provisional.

## Drawbacks

- Adds optional field to `WorkloadSpec`, increasing API surface area.
