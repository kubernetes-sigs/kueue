# KEP-10765: WorkloadPriorityClass defaulting

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1](#story-1)
    - [Story 2](#story-2)
  - [Notes/Constraints/Caveats (Optional)](#notesconstraintscaveats-optional)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [Defaulting mechanism](#defaulting-mechanism)
  - [Test Plan](#test-plan)
    - [Unit Tests](#unit-tests)
    - [Integration Tests](#integration-tests)
  - [Graduation Criteria](#graduation-criteria)
- [Alternatives](#alternatives)
  - [Configuration-based default](#configuration-based-default)
  - [globalDefault field on WorkloadPriorityClass](#globaldefault-field-on-workloadpriorityclass)
<!-- /toc -->

## Summary

This KEP introduces a `WorkloadPriorityClassDefaulting` feature gate. When
enabled, workloads that do not specify a `WorkloadPriorityClass` via the
`kueue.x-k8s.io/priority-class` label automatically receive the priority value
from a `WorkloadPriorityClass` named `default`, if one exists.

## Motivation

In clusters where only a subset of workloads are managed by Kueue, admins need a
way to assign a default `WorkloadPriorityClass` to Kueue-managed workloads
without requiring every job author to set the `kueue.x-k8s.io/priority-class`
label.

Today, if a Kueue-managed job omits that label, `ExtractPriority()` falls
through to the Kubernetes `scheduling.k8s.io/v1` PriorityClass chain. This
conflates pod scheduling priority with workload queueing priority, which are
intentionally separate concerns in clusters that use `WorkloadPriorityClass`.

The only workarounds are requiring every job author to set the label (fragile and
hard to enforce across teams) or deploying a custom mutating webhook (operational
overhead for injecting a default that Kueue could handle natively).

### Goals

- When the `WorkloadPriorityClassDefaulting` feature gate is enabled, workloads
  without an explicit `WorkloadPriorityClass` label automatically use a
  `WorkloadPriorityClass` named `default` for their queueing priority, if it
  exists.

### Non-Goals

- Automatically creating a default `WorkloadPriorityClass`. The administrator
  must create it manually.
- Modifying the `WorkloadPriorityClass` API.

## Proposal

When the `WorkloadPriorityClassDefaulting` feature gate is enabled and a workload
does not specify a `WorkloadPriorityClass`, the priority resolution chain looks
for a `WorkloadPriorityClass` named `default` before falling through to the
Kubernetes PriorityClass chain. This follows the same convention-based pattern as
`LocalQueueDefaulting`, which looks for a LocalQueue named `default`.

The `default` WorkloadPriorityClass itself must be created manually by the
administrator.

### User Stories

#### Story 1

An organization runs a shared cluster where only batch workloads are managed by
Kueue. The admin creates a `WorkloadPriorityClass` named `default` with value
`1000`. Users submit batch Jobs without needing to know about
`WorkloadPriorityClass` — their workloads automatically receive priority `1000`
for queueing and preemption, keeping them in the `WorkloadPriorityClass` priority
domain.

#### Story 2

An admin wants to ensure that all Kueue-managed workloads have a baseline
queueing priority that is independent of their pod scheduling priority. By
creating a `default` WorkloadPriorityClass with a low value, new workloads that
forget to set a priority class get a well-defined baseline rather than inheriting
an unrelated value from the Kubernetes PriorityClass chain.

### Notes/Constraints/Caveats (Optional)

The name `default` is a convention, consistent with how `LocalQueueDefaulting`
works. Administrators who want a descriptive name can create a
`WorkloadPriorityClass` with their preferred name and instruct users to reference
it via label; this feature only covers the case where no label is set at all.

### Risks and Mitigations

Enabling this feature changes the priority of workloads that previously fell
through to the Kubernetes PriorityClass default. Admins should set the `default`
WorkloadPriorityClass value carefully before enabling the feature gate to avoid
unexpected changes in queueing and preemption behavior.

## Design Details

### Defaulting mechanism

When the `WorkloadPriorityClassDefaulting` feature gate is enabled and a job is
submitted without a `kueue.x-k8s.io/priority-class` label, the mutating webhook
checks whether a `WorkloadPriorityClass` named `default` exists. If it does, the
webhook sets the label `kueue.x-k8s.io/priority-class: default` on the job
object.

This follows the same pattern as `LocalQueueDefaulting`, which sets
`kueue.x-k8s.io/queue-name: default` via `ApplyDefaultLocalQueue` in the
webhook. The new `ApplyDefaultWorkloadPriorityClass` function is called from all
job-type webhooks (both `BaseWebhook` and custom webhooks) alongside
`ApplyDefaultLocalQueue`.

The label is set on the job at admission time, so the existing priority
resolution chain in `ExtractPriority()` picks it up naturally during workload
construction — no changes to the reconciler are needed.

The defaulting is skipped when:
- The feature gate is disabled.
- The job already has a `kueue.x-k8s.io/priority-class` label.
- The job's owner is already managed by Kueue (to avoid double-defaulting).
- No `WorkloadPriorityClass` named `default` exists.

When the `default` WorkloadPriorityClass does not exist, the webhook makes no
changes to the job and the existing priority resolution chain applies: the
workload priority falls through to the Kubernetes PodSpec `priorityClassName`,
then to the Kubernetes `globalDefault` PriorityClass, and finally to the
hardcoded value `0`. This is the same behavior as when the feature gate is
disabled.

### Test Plan

[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes
necessary to implement this enhancement.

#### Unit Tests

- `pkg/util/priority/priority_test.go`:
  - Feature gate disabled: `default` WPC is not consulted.
  - Feature gate enabled, `default` WPC exists: unlabeled workloads get its
    priority.
  - Feature gate enabled, `default` WPC does not exist: falls through to
    Kubernetes PriorityClass chain.
  - Explicit `kueue.x-k8s.io/priority-class` label takes precedence over the
    `default` WPC.

#### Integration Tests

- Webhook/controller integration tests verifying that workloads created from
  Jobs without a `WorkloadPriorityClass` label receive the `default` WPC
  priority when the feature gate is enabled.

### Graduation Criteria

**Alpha (v0.18):**
- Feature gate `WorkloadPriorityClassDefaulting` disabled by default.
- Unit and integration tests.

**Beta:**
- Feature gate enabled by default.
- Address feedback from Alpha usage.

**GA:**
- Feature gate locked to true.

## Alternatives

### globalDefault field on WorkloadPriorityClass

A `globalDefault` boolean field could be added to `WorkloadPriorityClass`,
mirroring Kubernetes' `PriorityClass.GlobalDefault`. This was rejected in favor
of the simpler convention-based approach for consistency with
`LocalQueueDefaulting`. The convention-based approach requires no API changes and
is simpler for both implementation and users to understand.
