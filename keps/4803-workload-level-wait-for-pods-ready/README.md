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
    - [Resource Annotations](#resource-annotations)
  - [Controller](#controller)
    - [Workload](#workload)
    - [Jobs / Jobframework](#jobs--jobframework)
  - [Webhooks](#webhooks)
    - [Managed resources (Jobs, Deployments, StatefulSets, etc.)](#managed-resources-jobs-deployments-statefulsets-etc)
  - [Future Work](#future-work)
  - [Test Plan](#test-plan)
    - [Prerequisite testing updates](#prerequisite-testing-updates)
    - [Webhook unit tests](#webhook-unit-tests)
      - [webhooks/job (controller/jobframework/validation)](#webhooksjob-controllerjobframeworkvalidation)
    - [e2e](#e2e)
  - [Graduation Criteria](#graduation-criteria)
    - [Alpha1 (0.20)](#alpha1-020)
    - [Alpha2 (0.21)](#alpha2-021)
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

- Introduce the JSON-based annotation: `kueue.x-k8s.io/wait-for-pods-ready` applicable to any
  Kueue-managed resource (Job, StatefulSet, etc.). For Deployment, the annotation needs to be
  defined at `spec.template.metadata.annotations` to be propagated to the Pods and then workloads.
- Per-workload timeouts take precedence over the global `WaitForPodsReady` timeouts configuration when
  both are present.
- Introduce `MaxTimeoutOnWorkload` field in the configuration.

### Non-Goals

- Change the cluster-wide `WaitForPodsReady` behavior.
- Change the backoff strategy; that remains cluster-wide only
- Introduce per-workload `BlockAdmission` semantics.
- Supporting MultiKueue in Alpha. Re-evaluate at Beta.

## Proposal

The `kueue.x-k8s.io/wait-for-pods-ready` annotation with the values for `Timeout` and `recoveryTimeout`
is read from the managed resource and stored in the workload annotation.

A new optional configuration is added to the `WaitForPodsReady` that allows admins to control
the maximum timeout values they want to allow users to specify at the `kueue.x-k8s.io/wait-for-pods-ready`
annotation.

The managed-resource webhook (see [Webhooks](#webhooks)) rejects annotation
changes while the resource is unsuspended. When the job is suspended and the
annotation is changed, the mismatch is detected and the Workload spec is reconstructed.
The webhooks also rejects timeouts that exceed the maximum timeout or are not valid integers.

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
the timeout is ignored and a log warns about incompatible combination.

### Risks and Mitigations

- **Conflicting values**: A user may set per-workload timeouts that conflicts
  with a cluster-wide one.  Mitigation: per-workload values always take
  precedence; a clear precedence rule is documented and enforced in the webhook.

## Design Details

### API

The `WaitForPodsReady` struct has a new field added `MaxTimeoutOnWorkload`:

```go
type WaitForPodsReady struct {
  ...
	// MaxTimeoutOnWorkload defines the upper bound allowed for a per-workload
	// PodsReady timeout and recoveryTimeout override (set via the `kueue.x-k8s.io/wait-for-pods-ready`
	// annotation). If a workload requests a timeout or recoveryTimeout greater than
	// MaxTimeoutOnWorkload, the job is rejected by the admission webhook.
	// When unset, the default maximum of 2 hours is enforced.
	// It has no effect on workloads that don't set a per-workload override.
	// +optional
	MaxTimeoutOnWorkload *metav1.Duration `json:"maxTimeoutOnWorkload,omitempty"` 
}
```

#### Resource Annotations

One new annotation is defined in `pkg/controller/constants/constants.go`:

```go
  // WaitForPodsReadyAnnotation is the annotation key on any Kueue-managed resource that sets
	// per-workload timeout and recoveryTimeout, overriding those values at cluster-wide WaitForPodsReady.
  // The value is a JSON containing timeout in seconds and RecoveryTimeout in seconds.
	// This annotation is alpha-level enabled by the WorkloadLevelWaitForPodsReady.
	WaitForPodsReadyAnnotation = "kueue.x-k8s.io/wait-for-pods-ready"
```

Here is an example of the annotation definition:

```yaml
annotations:
  kueue.x-k8s.io/wait-for-pods-ready: '{"timeoutSeconds": 30, "recoveryTimeoutSeconds": 40}'
```

### Controller

#### Workload

The workload controller is updated to resolve both the effective eviction deadline
and the recovery timeout from the per-workload values carried in the workload's
own annotations, falling back to the cluster-wide configuration for each field
independently when no per-workload value is present. This means a workload may override 
the timeout, the recovery timeout, or both. When only the timeout is specified,
it is also used as the default for the recovery timeout. When neither field is
specified, the cluster-wide values are used as the fallback for each.
The annotation is propagated from the managed resource to the workload at
construction time and re-evaluated on every reconciliation to keep the workload
annotation in sync with any changes made while the job is suspended. Any attempt
to change the annotation on the managed resource while the job is unsuspended is
rejected by the managed-resource webhook before it can take effect.

#### Jobs / Jobframework

The job reconciler is updated so that the `WorkloadPodsReady` condition is also
written when the workload carries the `kueue.x-k8s.io/wait-for-pods-ready`
annotation, in addition to the existing cluster-wide trigger.

When a workload is constructed for a managed resource, the
`kueue.x-k8s.io/wait-for-pods-ready` annotation is read from that resource and
copied to the resulting workload's annotations. This covers most integrations
(Job, StatefulSet, RayJob, PyTorchJob, JobSet, etc.).

The **Deployment** integration is an exception: Kueue tracks Deployment-owned
workloads via the Pod integration rather than directly from the Deployment object.
Users must therefore place the annotation on the Pod template
(`spec.template.metadata.annotations`), from where it is read when the workload
is constructed for the Pod — no additional propagation logic is needed in Kueue.

### Webhooks

#### Managed resources (Jobs, Deployments, StatefulSets, etc.)

- Validate that the timeouts values in `kueue.x-k8s.io/wait-for-pods-ready` are
  positive integers and does not exceed the maximum value set by the admin in the
  cluster configuration.
- Setting a recoveryTimeout without timeout set is not supported.
- The annotation is immutable while the job is unsuspended. Changes are allowed
  while the job is suspended (i.e. between eviction cycles), which is the
  intended window for a user to adjust the timeout before re-admission. When a
  change is detected during reconciliation, the workload's own annotation is
  updated in place — no delete-and-recreate occurs.

### Future Work

The `kueue.x-k8s.io/wait-for-pods-ready` annotation is parsed at Workload creation
and stored in the Workload spec fields `timeoutSeconds` and `recoveryTimeoutSeconds`
instead of being carried as a workload annotation.

The following APIs are considered for future releases and will be evaluated.

The `WaitForPodsReady` struct and its `TimeoutSeconds` field are added to `WorkloadSpec`.

```go
// WorkloadSpec defines the desired state of Workload
type WorkloadSpec struct {
    // ...existing fields...
    // WaitForPodsReady ensures the workload is ready within an specific timeout
    // +optional
    WaitForPodsReady *WaitForPodsReady `json:"WaitForPodsReady,omitempty"`
}

// +kubebuilder:validation:MinProperties=1
type WaitForPodsReady struct {
    // timeoutSeconds defines the maximum time the workload may remain
    // admitted before all pods are in a Ready or Succeeded state.
    // When elapsed, the workload is evicted with reason PodsReadyTimeout.
    // If both this field and the cluster-wide WaitForPodsReady.Timeout are set,
    // this field takes precedence.
    // +optional
    // +kubebuilder:validation:Minimum=1
    TimeoutSeconds *int64 `json:"timeoutSeconds,omitempty"`

    // RecoveryTimeoutSeconds defines a timeout, measured since the workload
    // loses readiness after a Workload is Admitted and running.
    // After exceeding the timeout the corresponding job gets suspended again
    // and requeued after the backoff delay.
    // If both this field and the cluster-wide WaitForPodsReady.RecoveryTimeout
    // are set, this field takes precedence. Defaults to timeoutSeconds when
    // timeoutSeconds is set and this field is not. Setting 0 disables it.
    // +optional
    // +kubebuilder:validation:Minimum=0
    RecoveryTimeoutSeconds *int64 `json:"recoveryTimeoutSeconds,omitempty"`
}
```

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

- "A Job annotated with `kueue.x-k8s.io/wait-for-pods-ready` set to a valid
  JSON with a positive timeout in seconds is accepted."
- "A Job annotated with `kueue.x-k8s.io/wait-for-pods-ready` set to a valid
  JSON with both timeout and recoveryTimeout in seconds is accepted."
- "A Job annotated with `kueue.x-k8s.io/wait-for-pods-ready` set to a invalid
  JSON with only recoveryTimeout is not accepted."
- "A Job annotated with `kueue.x-k8s.io/wait-for-pods-ready` with a zero or
  negative timeout is rejected at admission time."
- "A Job annotated with `kueue.x-k8s.io/wait-for-pods-ready` with a timeout
  exceeding `MaxTimeoutOnWorkload` is rejected at admission time."
- "A Job annotated with `kueue.x-k8s.io/wait-for-pods-ready` set to malformed
  JSON is rejected at admission time."
- "Changing the annotation on an unsuspended Job is rejected at admission time."

#### e2e

- "A job with a per-workload timeout shorter than the cluster wide timeout is evicted at
  per workload deadline"
- "A job with a per-workload timeout longer than the cluster wide timeout is evicted at
  per workload deadline"
- "Updating the annotation while the job is suspended causes the workload annotation
  to be updated on the next reconciliation without delete-and-recreate."
- "When `blockadmission:true`, a workload with a short per-workload timeout that expires
  unblocks admission of other workloads sooner than the cluster wide timeout."

### Graduation Criteria

#### Alpha1 (0.20)

- Feature gate `WorkloadLevelWaitForPodsReady` introduced, disabled by default.
- New resource `kueue.x-k8s.io/wait-for-pods-ready` annotations implemented.
- Introduce `MaxTimeoutOnWorkload` field in the cluster wide configuration.
- Unit tests for annotation parsing, webhook validation (duration, immutability).
- E2E tests added.

#### Alpha2 (0.21)

- Introduce per-workload `WaitForPodsReady.TimeoutSeconds` and `WaitForPodsReady.RecoveryTmeoutSeconds`
  fields in `WorkloadSpec`.
- Extend the support for `.unscheduledTimeout`.

#### Beta

- Feature gate enabled by default.
- Documentation updated.

#### Stable

- No issues reported for two or more releases.
- Feature gate removed; behaviour always on.

## Implementation History

- 2026-07-03: KEP created as provisional.

## Drawbacks

- Increases the annotations surface on the workload.
