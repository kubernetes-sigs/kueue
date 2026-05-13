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
  - [Notes/Constraints/Caveats](#notesconstraintscaveats)
    - [Naming convention](#naming-convention)
    - [Interaction with <code>managedJobsNamespaceSelector</code>](#interaction-with-managedjobsnamespaceselector)
    - [Compatibility with <code>manageJobsWithoutQueueName</code>](#compatibility-with-managejobswithoutqueuename)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [Defaulting mechanism](#defaulting-mechanism)
  - [Test Plan](#test-plan)
    - [Unit Tests](#unit-tests)
    - [Integration Tests](#integration-tests)
  - [Graduation Criteria](#graduation-criteria)
- [Alternatives](#alternatives)
  - [globalDefault field on WorkloadPriorityClass](#globaldefault-field-on-workloadpriorityclass)
  - [Defaulting in the Workload rather than in the Job webhook](#defaulting-in-the-workload-rather-than-in-the-job-webhook)
  - [Per-ClusterQueue default WorkloadPriorityClass](#per-clusterqueue-default-workloadpriorityclass)
  - [Soft enforcement when a Pod PriorityClass is set](#soft-enforcement-when-a-pod-priorityclass-is-set)
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

### Notes/Constraints/Caveats

#### Naming convention

The name `default` is a convention, consistent with how `LocalQueueDefaulting`
works. Administrators who want a descriptive name can create a
`WorkloadPriorityClass` with their preferred name and instruct users to reference
it via label; this feature only covers the case where no label is set at all.

#### Interaction with `managedJobsNamespaceSelector`

When the `default` WorkloadPriorityClass exists, the webhook sets the
`kueue.x-k8s.io/priority-class: default` label on every Job that passes
through it, including Jobs in namespaces excluded by
`managedJobsNamespaceSelector`. This mirrors how `LocalQueueDefaulting` sets
the `kueue.x-k8s.io/queue-name` label, since both run before the
`ApplyDefaultForSuspend` check where the namespace-selector filter is
applied. The label is noop on excluded-namespace Jobs: Kueue does not
reconcile those Jobs, so no `Workload` is created and the label is never
read.

#### Compatibility with `manageJobsWithoutQueueName`

Defaulting does not depend on the `kueue.x-k8s.io/queue-name` label, so Jobs
admitted under `manageJobsWithoutQueueName=true` — including those without a
LocalQueue reference — receive the default `WorkloadPriorityClass` on the same
terms as any other Kueue-managed Job.

### Risks and Mitigations

Enabling this feature only changes semantics in environments that already
contain a `WorkloadPriorityClass` named `default`: workloads submitted without a
`kueue.x-k8s.io/priority-class` label, which previously fell through to the
Kubernetes PriorityClass chain, will instead inherit the priority of the
`default` WorkloadPriorityClass. Environments without a `default`
WorkloadPriorityClass are not affected.

To mitigate the risk for affected environments:

- The `WorkloadPriorityClassDefaulting` feature gate allows admins to disable
  the mechanism while in Beta in case their environments are negatively
  affected.
- The behavior change will be called out in the release notes so that admins
  upgrading Kueue can review their existing `WorkloadPriorityClass` resources
  and decide whether to rename the `default` one or adjust its value before
  enabling the feature gate.

## Design Details

### Defaulting mechanism

When the `WorkloadPriorityClassDefaulting` feature gate is enabled and a job is
submitted without a `kueue.x-k8s.io/priority-class` label, the mutating webhook
checks whether a `WorkloadPriorityClass` named `default` exists. If it does, the
webhook sets the label `kueue.x-k8s.io/priority-class: default` on the job
object.

This follows the same pattern as `LocalQueueDefaulting`, which sets
`kueue.x-k8s.io/queue-name: default` via `ApplyDefaultLocalQueue` in the
webhook. The new `ApplyDefaultWorkloadPriorityClass` function is called
alongside `ApplyDefaultLocalQueue` from every job-type webhook:

- Integrations that embed the shared `BaseWebhook` (e.g. `AppWrapper`,
  Kubeflow's `JAXJob`/`PaddleJob`/`PyTorchJob`/`TFJob`/`XGBoostJob`)
  inherit the call from `BaseWebhook.Default`.
- Integrations with a custom webhook (`Pod`, `Deployment`, `Job`, `JobSet`,
  `LeaderWorkerSet`, `MPIJob`, `RayCluster`, `RayJob`, `RayService`,
  `SparkApplication`, `StatefulSet`, `TrainJob`) invoke it explicitly from
  their own `Default` method.

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

- `pkg/util/priority/priority_test.go` — `DefaultWorkloadPriorityClassExist`:
  - `default` WorkloadPriorityClass exists: returns `true`.
  - `default` WorkloadPriorityClass does not exist (different name): returns
    `false`.
  - No WorkloadPriorityClasses exist: returns `false`.

- `pkg/controller/jobframework/defaults_test.go` —
  `ApplyDefaultWorkloadPriorityClass`:
  - Feature gate enabled, no label, `default` WPC exists: label
    `kueue.x-k8s.io/priority-class: default` is set on the job.
  - Feature gate disabled: label is not set.
  - Label already set: existing label is preserved unchanged.
  - `default` WPC does not exist: label is not set.
  - Job's owner is managed by Kueue: label is not set.

#### Integration Tests

- Webhook/controller integration tests verifying that workloads created from
  Jobs without a `kueue.x-k8s.io/priority-class` label receive the `default`
  WPC priority when the feature gate is enabled.

### Graduation Criteria

**Alpha (v0.18):**
- Feature gate `WorkloadPriorityClassDefaulting` disabled by default.
- Unit and integration tests.

**Beta:**
- Feature gate enabled by default.
- Address feedback from Alpha usage.
- Re-evaluate whether defaulting should move from the Job webhook to the
  Workload level based on user feedback and integration complexity.

**GA:**
- Feature gate locked to true.

## Alternatives

### globalDefault field on WorkloadPriorityClass

A `globalDefault` boolean field could be added to `WorkloadPriorityClass`,
mirroring Kubernetes' `PriorityClass.GlobalDefault`. This was rejected in favor
of the simpler convention-based approach for consistency with
`LocalQueueDefaulting`. The convention-based approach requires no API changes and
is simpler for both implementation and users to understand.

### Defaulting in the Workload rather than in the Job webhook

Instead of defaulting the `kueue.x-k8s.io/priority-class` label in the Job
mutating webhook, the default `WorkloadPriorityClass` could be resolved at the
time of Workload creation in the reconciler.

This approach has some advantages: it would be implemented in a single place,
agnostic of the actual Job type, avoiding code duplication across all job-type
webhooks. It also avoids the webhook hot path and makes it straightforward to
emit an event for debuggability when the default is applied.

However, the webhook approach was chosen for Alpha to maintain consistency with
`LocalQueueDefaulting`, which already sets the `kueue.x-k8s.io/queue-name`
label via `ApplyDefaultLocalQueue` in the webhook. The webhook implementation is
simple, already exercised by the existing `LocalQueueDefaulting` pattern, and
provides immediate visibility to the user through the label on the Job object.

This decision will be re-evaluated before Beta based on user feedback and the
integration complexity observed during Alpha.

### Per-ClusterQueue default WorkloadPriorityClass

Instead of resolving defaulting through a cluster-scoped `WorkloadPriorityClass`
named `default`, the default could be configured per-`ClusterQueue` via a new
optional field on `ClusterQueueSpec`:

```yaml
apiVersion: kueue.x-k8s.io/v1beta2
kind: ClusterQueue
spec:
  defaultWorkloadPriorityClass: gold
```

Resolution would still happen in the Job mutating webhook, so the defaulted
priority remains visible on the Job object. Given the `kueue.x-k8s.io/queue-name`
label (set by the user or by `ApplyDefaultLocalQueue`), the webhook would:

1. Look up the `LocalQueue` and its bound `ClusterQueue` via the queue cache
   (`Manager.ClusterQueueFromLocalQueue`, already used by
   `ApplyDefaultForManagedBy`).
2. Read `ClusterQueue.Spec.DefaultWorkloadPriorityClass`.
3. If set and the Job has no `kueue.x-k8s.io/priority-class` label, set the
   label to that value.

**Advantages over the cluster-wide `default` WPC:**

- Different `ClusterQueue`s can have different defaults (e.g., `interactive`
  vs `batch`), aligning with how admins typically configure per-partition QoS
  in systems like Slurm.
- Operators can change the default `WorkloadPriorityClass` one `ClusterQueue`
  at a time, so Jobs already labeled with the old default keep resolving
  successfully while the new default is rolled out. With the cluster-wide
  `default` WPC convention, deleting it leaves every already-labeled Job
  unable to produce a Workload until the old WPC is restored or the label is
  stripped.
- Defaulting is opt-in per `ClusterQueue`. Operators can enable WPC defaulting
  on some queues while other queues continue to fall through to
  `PodSpec.priorityClassName` and the Kubernetes `PriorityClass` chain.

**Disadvantages:**

- Admins who want a single default for every `ClusterQueue` must set the
  field on each one (or use their templating / policy tooling).

### Soft enforcement when a Pod PriorityClass is set

As proposed, defaulting sets `kueue.x-k8s.io/priority-class: default` on every
Job that does not already carry the label, regardless of whether the Job's
`PodTemplateSpec` sets a Kubernetes `priorityClassName` ("strong" enforcement).

A "soft" enforcement mode could instead apply the default `WorkloadPriorityClass`
only when the Job's `PodTemplateSpec` has no `priorityClassName`, leaving Jobs
that set a Pod `PriorityClass` to fall through to the Kubernetes PriorityClass
chain for their Kueue queueing priority.

In clusters with a mix of batch and non-batch workloads, Pod `PriorityClass` is
typically used cluster-wide to drive quota and scheduling priority for
non-batch workloads handled by the default Kubernetes scheduler, while
`WorkloadPriorityClass` expresses a separate, tenant-scoped queueing and
preemption priority for Kueue-managed batch Jobs. Batch Jobs frequently still
set a Pod `PriorityClass` to participate in the cluster-wide quota and
scheduling system, and those are exactly the Jobs where decoupling Kueue
queueing priority from scheduler priority matters most. Strong enforcement
keeps WPC defaulting effective for those Jobs and preserves the clean
separation between scheduler priority and Kueue queueing priority that this
KEP aims to establish.

This decision will be re-evaluated before Beta based on user feedback from
Alpha.
