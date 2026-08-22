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
    - [Story 3](#story-3)
  - [Notes/Constraints/Caveats](#notesconstraintscaveats)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [API](#api)
    - [Workload Spec](#workload-spec)
    - [Resource Annotations](#resource-annotations)
  - [Controller](#controller)
    - [Workload](#workload)
    - [Jobs / Jobframework](#jobs--jobframework)
  - [Webhooks](#webhooks)
    - [Managed resources (Jobs, Deployments, StatefulSets, etc.)](#managed-resources-jobs-deployments-statefulsets-etc)
  - [Test Plan](#test-plan)
    - [Prerequisite testing updates](#prerequisite-testing-updates)
    - [Webhook unit tests](#webhook-unit-tests)
      - [webhooks/job (controller/jobframework/validation)](#webhooksjob-controllerjobframeworkvalidation)
      - [webhooks/deployment](#webhooksdeployment)
    - [Integration tests](#integration-tests)
      - [controller/core/workload](#controllercoreworkload)
      - [controller/jobs/job](#controllerjobsjob)
      - [controller/jobs/deployment (Pod-template exception)](#controllerjobsdeployment-pod-template-exception)
  - [Graduation Criteria](#graduation-criteria)
    - [Alpha](#alpha)
    - [Beta](#beta)
    - [Stable](#stable)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
<!-- /toc -->

## Summary

This proposal introduces a mechanism to allow specification of `WaitForPodsReady` timeout per workload.

## Motivation

Currently the `WaitForPodsReady` feature can only be configured at the cluster
level via the Kueue `ConfigMap`. Since different workloads may take different
amounts of time to reach a ready state, a single cluster-wide setting may often
not fit well all workloads.

### Goals

- Introduce per-workload `WaitForPodsReady.podsReadyTimeout` field in `WorkloadSpec`.
- Define a new annotation `kueue.x-k8s.io/pods-ready-timeout` applicable to any
  Kueue-managed resource (Job, StatefulSet, etc.)
  that is propagated to the corresponding `WorkloadSpec` field at Workload creation time.
  For Deployment, the annotation needs to be defined at `spec.template.metadata.annotations`
  to be propagated to the Pods and then workloads.
- Per-workload timeouts take precedence over the global `WaitForPodsReady` timeout configuration when
  both are present.

### Non-Goals

- Change the cluster-wide `WaitForPodsReady` behaviour.
- Change the recovery / backoff strategy; that remains cluster-wide only
- Introduce per-workload `BlockAdmission` semantics.

## Proposal

One new optional field is added to `WorkloadSpec`. The
`kueue.x-k8s.io/pods-ready-timeout` annotation is parsed at Workload creation
and stored in `wl.Spec.WaitForPodsReady.PodsReadyTimeout`. The eviction deadline
is resolved exclusively from that stored field.

The managed-resource webhook (see [Webhooks](#webhooks)) rejects annotation
changes while the resource is unsuspended. This prevents `EquivalentToWorkload`
from detecting drift and triggering a delete-and-recreate cycle that would replace
the Workload with a new one carrying a different timeout.

### User Stories

#### Story 1

As a batch platform administrator, I want to configure a tight pods-ready
timeout for data-preprocessing jobs (2 minutes) while allowing large ML
training jobs a longer timeout (30 minutes), without requiring two separate
Kueue deployments.

#### Story 2

As a job owner whose workload requires pulling a large container image that takes longer than the cluster-wide WaitForPodsReady timeout, I want to annotate my job with a longer per-job timeout so it is not evicted prematurely, without requiring the platform administrator to relax the global timeout for everyone.

#### Story 3

As a platform administrator, I want to configure Kueue with `blockAdmission: true` and assign per-workload pods-ready timeouts so that each workload is evicted after its own appropriate timeout rather than a single cluster-wide timeout. This ensures that a workload whose pods never become ready is evicted promptly and unblock admission of workloads in other ClusterQueues.

### Notes/Constraints/Caveats

- When a per-workload timeout is enforced and `DisableWaitForPodsReady` feature gate is enabled
the workload is forbiden from being admitted.

### Risks and Mitigations

- **Conflicting values**: A user may set a per-workload timeout that conflicts
  with a cluster-wide one.  Mitigation: per-workload values always take
  precedence; a clear precedence rule is documented and enforced in the webhook.

## Design Details

### API

#### Workload Spec

The `WaitForPodsReady` struct and its `PodsReadyTimeout` field are added to
`WorkloadSpec` in **v1beta2 only**.

```go
// WorkloadSpec defines the desired state of Workload
type WorkloadSpec struct {
    // ...existing fields...

    WaitForPodsReady *WaitForPodsReady `json:"WaitForPodsReady,omitempty"`
}

```go
// +kubebuilder:validation:MinProperties=1
type WaitForPodsReady struct {
    // podsReadyTimeout defines the maximum duration the workload may remain
    // admitted before all pods are in a Ready or Succeeded state.
    // When elapsed, the workload is evicted with reason PodsReadyTimeout.
    // If both this field and the cluster-wide WaitForPodsReady.Timeout are set,
    // this field takes precedence.
    // +optional
    PodsReadyTimeout *metav1.Duration `json:"podsReadyTimeout,omitempty"`
}
```

**Conversion** (`apis/kueue/v1beta1/workload_conversion.go`):

No manual conversion function is needed. The generated `autoConvert_*` drops the
field when converting v1beta2→v1beta1.

#### Resource Annotations

One new annotation is defined in `pkg/controller/constants/constants.go`:

```go
// PodsReadyTimeoutAnnotation sets the per-workload pods-ready timeout.
// Applicable to any Kueue-managed resource (Job, Deployment, StatefulSet, etc.).
// Value must be a Go duration string parseable by time.ParseDuration (e.g. "10m").
PodsReadyTimeoutAnnotation = "kueue.x-k8s.io/pods-ready-timeout"
```

### Controller

#### Workload

`admittedNotReadyWorkload` in `pkg/controller/core/workload_controller.go` is
updated to resolve the effective timeout exclusively from `wl.Spec.WaitForPodsReady.PodsReadyTimeout`,
falling back to the cluster-wide config only when that field is nil.
The eviction deadline is resolved exclusively from `wl.Spec.WaitForPodsReady.PodsReadyTimeout`
and never from the live annotation. `prepareWorkload` does re-read the annotation
during reconciliation to keep the stored field in sync, but any attempt to
update `PodsReadyTimeout` while the Job is unsuspended is rejected by the
managed-resource webhook before the change can reach the API server.

`reconcileNotReadyTimeout` gains a short-circuit check: if neither the global
config is set nor `wl.Spec.WaitForPodsReady` is populated, the function
returns immediately (current behaviour preserved).

The check that decides to write the `WorkloadPodsReady` condition in the job reconciler
(`pkg/controller/jobframework/reconciler.go`) is updated to also trigger when
`wl.Spec.WaitForPodsReady` is non-nil.

#### Jobs / Jobframework

`ConstructWorkload` (in `pkg/controller/jobframework/reconciler.go`) reads the
`kueue.x-k8s.io/pods-ready-timeout` annotation from the managed resource object
and populates the corresponding `WorkloadSpec` field. This covers most integrations
(Job, StatefulSet, RayJob, PyTorchJob, JobSet, etc.).

The **Deployment** integration is an exception: it does not construct `Workload` objects directly. Kueue tracks Deployment-owned workloads via the Pod integration, so the annotation must be placed by the user on
the Pod template (`spec.template.metadata.annotations`). The Pod
integration then calls `ConstructWorkload` directly and reads the annotation from
the Pod object, so no additional propagation logic is needed in Kueue.

### Webhooks

#### Managed resources (Jobs, Deployments, StatefulSets, etc.)

- Validate that `kueue.x-k8s.io/pods-ready-timeout` is a valid duration
  string greater than zero.
- The annotation is immutable while the job is unsuspended. Changes are allowed
  while the job is suspended (i.e. between eviction cycles), which is the
  intended window for a user to adjust the timeout before re-admission.
  `EquivalentToWorkload` detects the drift and the Workload is recreated with
  the updated value. Note that recreation resets `wl.Status.RequeueState`,
  clearing any accumulated backoff; this is an accepted trade-off shared with
  other Kueue annotation changes that trigger Workload recreation.

### Test Plan

[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes
necessary to implement this enhancement.

#### Prerequisite testing updates

No regressions in the existing `WaitForPodsReady` tests under
`test/integration/singlecluster/scheduler/podsready/` and
`test/e2e/sequential/baseline/waitforpodsready_test.go`.

#### Webhook unit tests

##### webhooks/job (controller/jobframework/validation)

- "A Job annotated with kueue.x-k8s.io/pods-ready-timeout set to a valid
  duration is accepted."
- "A Job annotated with kueue.x-k8s.io/pods-ready-timeout set to a zero or
  negative duration is rejected at admission time."
- "A Job annotated with kueue.x-k8s.io/pods-ready-timeout set to a non-duration
  string is rejected at admission time."

##### webhooks/deployment

- "A Deployment whose Pod template carries kueue.x-k8s.io/pods-ready-timeout
  with a valid duration is accepted."
- "A Deployment whose Pod template carries kueue.x-k8s.io/pods-ready-timeout
  with an invalid value is rejected at admission time."

#### Integration tests

##### controller/core/workload

- "The per-workload podsReadyTimeout takes precedence over the cluster-wide
  timeout."
- "RecoveryTimeout uses only the cluster-wide value even when a per-workload
  podsReadyTimeout is set."

##### controller/jobs/job

- "A Job annotated with kueue.x-k8s.io/pods-ready-timeout has the timeout
  propagated to its Workload spec."

##### controller/jobs/deployment (Pod-template exception)

- "A Deployment whose Pod template carries kueue.x-k8s.io/pods-ready-timeout
  has the timeout propagated to the Workload created by the Pod integration."

### Graduation Criteria

#### Alpha

- Feature gate `WorkloadLevelWaitForPodsReady` introduced, disabled by default.
- New `WorkloadSpec` fields and resource annotations implemented.
- Unit tests for annotation parsing, webhook validation (duration, immutability),
  and the Deployment Pod-template exception added.
- Integration tests covering precedence, cluster-wide fallback, RecoveryTimeout
  behaviour, and Job/Deployment propagation added.

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
