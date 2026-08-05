# KEP-11520: Per-namespace LocalQueue Defaulting Configuration

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1](#story-1)
    - [Story 2](#story-2)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [API](#api)
  - [Implementation](#implementation)
  - [Test Plan](#test-plan)
    - [Unit tests](#unit-tests)
    - [e2e tests](#e2e-tests)
  - [Graduation Criteria](#graduation-criteria)
    - [Beta](#beta)
    - [Stable](#stable)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
  - [Reuse ManagedJobsNamespaceSelector](#reuse-managedjobsnamespaceselector)
  - [LocalQueue-level annotation](#localqueue-level-annotation)
<!-- /toc -->

## Summary

LocalQueue defaulting (KEP-2936) activates implicitly whenever a LocalQueue
named `default` exists in a managed namespace. `managedJobsNamespaceSelector` (KEP-3589)
gives administrators per-namespace control over whether Kueue manages jobs, but
there is no equivalent control for defaulting. These are orthogonal concerns.
An administrator may want Kueue to manage explicitly-labeled jobs in a
namespace but not auto-default unlabeled ones. This KEP adds a
`localQueueDefaultingNamespaceSelector` field to the Configuration API,
following the same pattern as `managedJobsNamespaceSelector`, to give
administrators explicit per-namespace control over LocalQueue defaulting.

## Motivation

Kueue provides two namespace-level behaviors today:

1. **Workload management**: whether Kueue manages workloads in a namespace,
   controlled by `managedJobsNamespaceSelector`.
2. **LocalQueue defaulting**: whether unlabeled workloads are implicitly
   assigned to a `default` LocalQueue. This has no per-namespace control and
   activates purely based on whether a LocalQueue named `default` exists in a
   managed namespace.

These concerns are independent. Multi-tenant clusters may need defaulting
enabled for some namespaces (e.g., data science teams submitting ad-hoc
workloads) but require explicit queue declaration in others (e.g., for
compliance, chargeback, or auditability). Platforms built on Kueue may
pre-provision LocalQueues including a `default` queue across namespaces but
want centralized control over which namespaces actually get implicit defaulting.

Today, the only way to prevent defaulting in a managed namespace is to ensure
no LocalQueue named `default` exists there. But an administrator may intentionally
create a LocalQueue named `default` for users to explicitly target with
`kueue.x-k8s.io/queue-name: default`, without wanting unlabeled workloads to be
automatically routed to it. There is no way to decouple the existence of the
queue from the defaulting behavior.

Operators adopting Kueue incrementally also need the ability to enable
defaulting only in namespaces that have been fully onboarded, without affecting
the rest of the cluster.

`managedJobsNamespaceSelector` cannot solve this. Opting a namespace out of
management entirely prevents Kueue from managing even workloads with explicit
queue-name labels. The two controls need to be independent.

### Goals

- Give administrators explicit per-namespace control over which namespaces
  participate in LocalQueue defaulting.
- Decouple the existence of a `default` LocalQueue from the activation of
  defaulting behavior.

### Non-Goals

- Change the defaulting mechanism itself (webhook-based label injection).
- Replace or deprecate the `default` LocalQueue naming convention.
- Per-workload or per-workload-type control over defaulting.

## Proposal

We add a `localQueueDefaultingNamespaceSelector` of type
`*metav1.LabelSelector` to the top level of the Kueue `Configuration` struct.

When the `LocalQueueDefaultingPerNamespace` feature gate is enabled:

1. If `localQueueDefaultingNamespaceSelector` is nil, defaulting is active in
   all managed namespaces where a `default` LocalQueue exists (current
   behavior, backward compatible).
2. If `localQueueDefaultingNamespaceSelector` is set, defaulting only activates
   in managed namespaces that have a `default` LocalQueue and match the
   selector.

When the feature gate is disabled, the selector has no effect and the current
behavior is preserved.

### User Stories

#### Story 1

Different tenants in a multi-tenant cluster have different requirements. All
tenant namespaces are managed by Kueue via `managedJobsNamespaceSelector`, but
some need defaulting for ease of use (e.g., data science teams submitting
ad-hoc workloads), while others require explicit queue assignment for
auditability, cost attribution, or compliance. Today there is no way to enforce
"defaulting must not happen in namespace X". If someone creates a LocalQueue
named `default` in a managed namespace, defaulting silently activates.

#### Story 2

An administrator wants a LocalQueue named `default` to exist as an explicit
routing target (users set `kueue.x-k8s.io/queue-name: default` on their
workloads), but does not want unlabeled workloads to be automatically routed
there. Today, the existence of the queue and the defaulting behavior are
inseparable.

### Risks and Mitigations

**Risk:** Users accustomed to the current behavior may be surprised if
defaulting stops working after the selector is configured.

**Mitigation:** When the selector is nil (the default), behavior is unchanged.
The selector only takes effect when explicitly set and the feature gate is
enabled. Documentation should clearly explain the interaction between the
selector and the `default` LocalQueue existence check.

**Risk:** The Pod webhook has separate inline defaulting logic. If the selector
check diverges from `ApplyDefaultLocalQueue`, workloads of different types
would see inconsistent defaulting behavior.

**Mitigation:** The implementation PR must verify both paths apply the selector
identically.

## Design Details

### API

The `Configuration` struct is extended to add
`LocalQueueDefaultingNamespaceSelector`:

```go
type Configuration struct {

  ...

  // LocalQueueDefaultingNamespaceSelector restricts which namespaces
  // participate in LocalQueue defaulting. When set and the
  // LocalQueueDefaultingPerNamespace feature gate is enabled, only
  // workloads in namespaces matching this selector will have the default
  // LocalQueue label injected if a LocalQueue named "default" exists in
  // the namespace.
  // When nil, defaulting is active in all managed namespaces where a
  // "default" LocalQueue exists (preserving current behavior).
  // +optional
  LocalQueueDefaultingNamespaceSelector *metav1.LabelSelector `json:"localQueueDefaultingNamespaceSelector,omitempty"`

  ...
}
```

Configuration example:

```yaml
localQueueDefaultingNamespaceSelector:
  matchLabels:
    local-queue-defaulting: "true"
```

### Implementation

The new selector is threaded through all webhook configurations following the
same pattern as `managedJobsNamespaceSelector`. It is passed to
`ApplyDefaultLocalQueue`, which checks it before injecting the default queue
label. The check is gated behind the `LocalQueueDefaultingPerNamespace` feature
gate.

The defaulting label is injected only when all of the following conditions are
met, evaluated in this order:

1. A LocalQueue named `default` exists in the namespace.
2. The workload has no queue-name label and its owner is not managed by Kueue.
3. The namespace matches `managedJobsNamespaceSelector`. Unmanaged namespaces
   are rejected here and never reach the next check.
4. The namespace matches `localQueueDefaultingNamespaceSelector` (only when
   the feature gate is enabled).

When the feature gate is disabled, step 4 is skipped and the selector has no
effect.

When the selector is nil, all managed namespaces participate in defaulting,
preserving current behavior.

The check applies to all integrations. Most integrations go through
`ApplyDefaultLocalQueue`, but the Pod webhook has its own inline defaulting
logic. The same selector check is applied there, evaluated after the existing
`managedJobsNamespaceSelector` check and before the default queue label is
injected.

Validation follows the same pattern as `managedJobsNamespaceSelector`, ensuring
the selector does not match prohibited namespaces (`kube-system`, kueue
namespace).

### Test Plan

[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes
necessary to implement this enhancement.

#### Unit tests

Unit tests for `ApplyDefaultLocalQueue` covering:
- Managed namespace with defaulting label gets default queue label
- Managed namespace without defaulting label does not get default queue label
- Unmanaged namespace with defaulting label does not get default queue label
- Feature gate disabled ignores the selector

Unit tests for configuration validation will be added.

Unit tests for the Pod webhook covering the same defaulting scenarios as
`ApplyDefaultLocalQueue` to verify both paths behave identically.

**Note:** Integration tests are not feasible for this feature because the
webhook test suite's `managerSetup` creates its own queue manager internally,
preventing pre-population with a `default` LocalQueue. The full defaulting
flow is covered by e2e tests instead.

#### e2e tests

E2e tests with `localQueueDefaultingNamespaceSelector` configured covering:
- Workload in a managed namespace with the defaulting label gets the default
  queue label injected
- Workload in a managed namespace without the defaulting label does not get
  the default queue label injected
- Workload in an unmanaged namespace does not get the default queue label
  injected
- Workload with the feature gate disabled and selector configured gets the
  default queue label injected, verifying backward compatibility

### Graduation Criteria

#### Beta

Alpha was skipped as the feature is well-scoped and low-risk. When the
selector is nil (default), behavior is unchanged for existing users.

- Feature gate `LocalQueueDefaultingPerNamespace` on by default.
- `LocalQueueDefaultingNamespaceSelector` field added to Configuration API.
- `ApplyDefaultLocalQueue` checks the selector when the gate is enabled.
- Validation rejects selectors matching prohibited namespaces.
- Unit, validation, and e2e tests covering core scenarios.

#### Stable

- Feature gate locked to default.
- At least one release at beta with no user-reported regressions.
- Comprehensive test coverage.

## Implementation History

- 2026-08-04: Introduced at Beta in v0.20, skipping Alpha.

## Drawbacks

This adds another namespace selector to the Configuration API alongside
`managedJobsNamespaceSelector`. Administrators must understand the distinction:
`managedJobsNamespaceSelector` controls whether Kueue manages workloads at all,
while `localQueueDefaultingNamespaceSelector` controls only the implicit
queue-name defaulting behavior.

## Alternatives

### Reuse ManagedJobsNamespaceSelector

Use the existing `managedJobsNamespaceSelector` to also control defaulting.

This was rejected because:
- `managedJobsNamespaceSelector` controls whether Kueue manages workloads at
  all in a namespace. Defaulting control is a more granular concern. An
  administrator may want Kueue to manage workloads with explicit queue names in
  a namespace but not auto-default unlabeled workloads.
- Conflating these two concerns would force administrators to choose between
  "Kueue manages nothing in this namespace" and "Kueue manages everything
  including implicit defaulting," with no middle ground.

### LocalQueue-level annotation

Add an annotation or field on the LocalQueue itself (e.g.,
`kueue.x-k8s.io/is-default: "true"`) to mark it as the default, instead of
relying on the name `default`.

This was rejected because:
- It does not solve the per-namespace control problem. It changes the trigger
  mechanism but does not give administrators a way to disable defaulting in
  specific namespaces.
- It could be a complementary feature but does not address the core use case.
