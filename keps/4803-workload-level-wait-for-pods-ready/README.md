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
  - [Test Plan](#test-plan)
    - [Prerequisite testing updates](#prerequisite-testing-updates)
    - [Webhook unit tests](#webhook-unit-tests)
      - [webhooks/job (controller/jobframework/validation)](#webhooksjob-controllerjobframeworkvalidation)
    - [Integration](#integration)
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
  Kueue-managed resource (Job, StatefulSet, etc.).
- Per-workload timeouts take precedence over the global `WaitForPodsReady` timeouts configuration when
  both are present.
- Introduce `MaxTimeoutOnWorkload` field in the configuration.

### Non-Goals

- Change the cluster-wide `WaitForPodsReady` behavior.
- Change the backoff strategy; that remains cluster-wide only
- Introduce per-workload `BlockAdmission` semantics.
- Supporting MultiKueue in Alpha. Re-evaluate at Beta.

## Proposal

The `kueue.x-k8s.io/wait-for-pods-ready` annotation with the values for `Timeout`, `recoveryTimeout`
and `unscheduledTimeout` is read from the managed resource and stored in the workload spec.

A new optional configuration is added to the `WaitForPodsReady` that allows admins to control
the maximum timeout values they want to allow users to specify at the `kueue.x-k8s.io/wait-for-pods-ready`
annotation.

The managed-resource webhook (see [Webhooks](#webhooks)) rejects timeouts that exceed
the maximum timeout, are not larger than 0 or is represent in an invalid JSON.

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
  the timeout is ignored.
- Enabling `DisableWaitForPodsReady` and `WorkloadLevelWaitForPodsReady` feature gates is not allowed.

### Risks and Mitigations

- **Conflicting values**: A user may set per-workload timeouts that conflicts
  with a cluster-wide one.  Mitigation: per-workload values always take
  precedence; a clear precedence rule is documented and enforced in the webhook.

- **maxTimeoutOnWorkload is enforced at Job admission, not on the Workload**:
  `waitForPodsReady.maxTimeoutOnWorkload` is validated only by the Job admission webhook
  on the cluster where the Job is created. It is intentionally not enforced on the Workload
  object. MultiKueue copies the Workload spec and checking the cap on the Workload would
  break MultiKueue workers with a stricter cap.
  As a result, a user with write access to Workload objects can set
  `spec.waitForPodsReady.timeoutSeconds` above that cap. The workload controller uses the
  value as given, so the admitted workload keeps its quota until that deadline, which can
  delay other workloads. With Kueue's default roles the only end-user source of Workload
  write access is `workload-editor-role`, which aggregates into `batch-admin-role` but not
  `batch-user-role`, so this is limited to trusted actors and is accepted.

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

The `kueue.x-k8s.io/wait-for-pods-ready` annotation is parsed at Workload creation
and stored in the Workload spec fields `timeoutSeconds`, `recoveryTimeoutSeconds`
and `unscheduledTimeoutSeconds` instead of being carried as a workload annotation.

The `WaitForPodsReady` struct is added to `WorkloadSpec`.

```go
// WorkloadSpec defines the desired state of Workload
type WorkloadSpec struct {
    // ...existing fields...
    // WaitForPodsReady ensures the workload is ready within an specific timeout
    // +optional
    WaitForPodsReady *WaitForPodsReady `json:"waitForPodsReady,omitempty"`
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
    // are set, this field takes precedence.
    // +optional
    // +kubebuilder:validation:Minimum=1
    RecoveryTimeoutSeconds *int64 `json:"recoveryTimeoutSeconds,omitempty"`

    // unscheduledTimeoutSeconds defines a timeout, measured since the transition to the
    // Admitted=True condition, for all the Pods required by the admission to be
    // scheduled or to have succeeded.
    // If Kueue observes incomplete scheduling for this admission when the deadline
    // expires, it evicts the Workload with reason PodsReadyTimeout and requeues it
    // after the backoff delay.
    // If both this field and the cluster-wide WaitForPodsReady.UnscheduledTimeout
    // are set, this field takes precedence. If omitted, the cluster-wide value
    // applies. Set to 0 to disable scheduling tracking for this Workload.
    // Requires the WaitForPodsReadyUnscheduledTimeout feature gate.
    // +optional
    // +kubebuilder:validation:Minimum=0
    UnscheduledTimeoutSeconds *int64 `json:"unscheduledTimeoutSeconds,omitempty"`
}
```

#### Resource Annotations

One new annotation is defined in `pkg/controller/constants/constants.go`:

```go
	// WaitForPodsReadyAnnotation sets per-workload timeouts on Kueue-managed resources.
	// Its JSON value can contain timeoutSeconds, recoveryTimeoutSeconds, and
	// unscheduledTimeoutSeconds. Each value is in seconds and overrides the
	// corresponding cluster-wide timeout.
	// The annotation requires the WorkloadLevelWaitForPodsReady feature gate.
	// unscheduledTimeoutSeconds also requires the WaitForPodsReadyUnscheduledTimeout gate.
	WaitForPodsReadyAnnotation = "kueue.x-k8s.io/wait-for-pods-ready"
```

Here is an example of the annotation definition:

```yaml
annotations:
  kueue.x-k8s.io/wait-for-pods-ready: '{"timeoutSeconds": 30, "recoveryTimeoutSeconds": 40, "unscheduledTimeoutSeconds": 10}'
```

### Controller

#### Workload

The workload controller resolves the effective eviction deadline, recovery timeout,
and unscheduled timeout. Each per-workload value overrides the corresponding
cluster-wide setting; omitted values fall back to it. The startup and recovery
timeouts can also come from a legacy Workload annotation, while the unscheduled
timeout is read from the Workload spec.
The managed resource annotation is parsed into the Workload spec at construction
time and re-evaluated during job reconciliation to keep the values in sync.

#### Jobs / Jobframework

The job reconciler is updated so that the `WorkloadPodsReady` condition is also
written when the workload carries the `kueue.x-k8s.io/wait-for-pods-ready`
annotation, in addition to the existing cluster-wide trigger.

When a workload is constructed for a managed resource, the
`kueue.x-k8s.io/wait-for-pods-ready` annotation is read from that resource and
copied to the resulting workload's annotations. This covers most integrations
(Job, StatefulSet, RayJob, PyTorchJob, JobSet, etc.).

### Webhooks

#### Managed resources (Jobs, Deployments, StatefulSets, etc.)

- Validate that `timeoutSeconds` and `recoveryTimeoutSeconds` in
  `kueue.x-k8s.io/wait-for-pods-ready` are positive integers, and
  `unscheduledTimeoutSeconds` is a non-negative integer. All specified values
  must be no greater than `maxTimeoutOnWorkload` (2 hours by default).
  `unscheduledTimeoutSeconds` also requires the `WaitForPodsReadyUnscheduledTimeout`
  feature gate.
- A recoveryTimeout can be set without a timeout; the timeout then falls back to the cluster-wide value.
- For Kueue managed Deployment, the **Deployment** webhook copies the annotation
  from its metadata to the Template metadata to make sure it's propagated to the
  Pods and then to the workloads. It also removes the annotation from the template
  if it's only specified there and not on the Deployment annotation.
- To make the update behavior match all integrations, the annotation is always
  allowed to be updated given it's valid. The annotation is updated in place,
  no delete-and-recreate occurs. Note that for the Deployment, when the annotation
  is changed, the template annotation also changes and a rollout happens
  which is how the new value reaches the new workload.

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
- "A job with no annotation change has the webhook validation for the annotation
  skipped."

#### Integration

- "A job should have the annotation propagated to the workload during creation and update"
- "Elastic job should have the annotation propagated on update"
- "A job with a per-workload timeout shorter than the cluster wide timeout is evicted at
  per workload deadline"
- "A job with a per-workload recoveryTimeout shorter than the cluster wide timeout is evicted at
  per workload deadline"
- "When `blockadmission:true`, a workload with a short per-workload timeout that expires
  unblocks admission of other workloads sooner than the cluster wide timeout."

### Graduation Criteria

#### Alpha1 (0.20)

- Feature gate `WorkloadLevelWaitForPodsReady` introduced, disabled by default.
- New resource `kueue.x-k8s.io/wait-for-pods-ready` annotations implemented.
- Introduce `MaxTimeoutOnWorkload` field in the cluster wide configuration.
- Unit tests for annotation parsing, webhook validation.
- Integration tests added.

#### Alpha2 (0.21)

- Introduce per-workload `WaitForPodsReady.TimeoutSeconds` and `WaitForPodsReady.RecoveryTmeoutSeconds`
  fields in `WorkloadSpec`.
- Add `WaitForPodsReady.UnscheduledTimeoutSeconds` to the Workload spec and
  `unscheduledTimeoutSeconds` to the managed-resource annotation. Omission inherits
  the cluster value; `0` disables scheduling tracking for that Workload; a positive
  value overrides the cluster value and requires `WaitForPodsReadyUnscheduledTimeout`.

#### Beta

- Feature gate enabled by default.
- Remove support of reading the annotation on the workload and rely on the workload spec only.
- Documentation updated.

#### Stable

- No issues reported for two or more releases.
- Feature gate removed; behaviour always on.

## Implementation History

- 2026-07-03: KEP created as provisional.

## Drawbacks

- Increases the annotations surface on the workload.
