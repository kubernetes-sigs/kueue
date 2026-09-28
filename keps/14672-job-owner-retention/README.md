# KEP-14672: Opt-in deletion of finished Job owners after a retention period

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1 - Configurable retention for finished Job owners](#story-1---configurable-retention-for-finished-job-owners)
  - [Notes/Constraints/Caveats](#notesconstraintscaveats)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [API](#api)
  - [Behavior](#behavior)
  - [Test Plan](#test-plan)
    - [Unit Tests](#unit-tests)
    - [Integration tests](#integration-tests)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
<!-- /toc -->

## Summary

This KEP extends the `objectRetentionPolicies` API introduced in
[KEP-1618](/keps/1618-optional-gc-of-workloads) — which today covers retention
of finished and deactivated Workloads — to also cover the Job-like objects
that own those Workloads (e.g. `batch/Job`, `JobSet`, or any other integration
managed by Kueue). KEP-1618's own Motivation section explicitly anticipated
this: "While the primary focus is on Workloads, the API section can be
extended to include other Kueue-authored objects in the future." This KEP is
that extension.

Today, Kueue's Workload retention policy can clean up the Workload object
itself, but the Job (or other owner) that the Workload represents is left
entirely to the user, or to whatever external tooling they run, to clean up.
For users running large numbers of short-lived batch Jobs, this means
Job objects accumulate indefinitely unless they build their own external
cleanup process — exactly the etcd-storage and memory-footprint problem
KEP-1618 solved for Workloads, just one level up the ownership chain.

## Motivation

Kueue already has a mechanism for this shape of problem for owners that were
deactivated by Kueue itself: `WorkloadRetentionPolicy.AfterDeactivatedByKueue`
deletes the parent owner after a Kueue-initiated deactivation. But this only
covers workloads Kueue itself decided to stop. It does not cover the common
case of a Job that ran to completion normally and finished successfully or
failed on its own — the much larger, more routine category of finished work
in most clusters.

### Goals

- Support opt-in deletion of a Job (or other Kueue-integration-managed owner)
  after its Workload has been finished for a configurable duration.
- Reuse the existing `objectRetentionPolicies` API surface and the two-phase
  evaluate-then-delete-or-requeue behavior established by KEP-1618, rather
  than introducing a parallel mechanism.
- Maintain backward compatibility: the feature must be explicitly enabled and
  configured, matching KEP-1618's own default-off behavior.

### Non-Goals

- Changing or replacing the existing `WorkloadRetentionPolicy` behavior for
  Workload objects themselves.
- Providing per-owner-type (Job vs. JobSet vs. ...) retention configuration in
  the initial version; this KEP proposes a single, integration-agnostic
  retention duration that applies to any Kueue-managed owner.
- Collecting owners that finished before the feature was enabled. The
  retention clock starts from the annotation, which is only written when a
  Workload's `WorkloadFinished` condition is set with reason `Succeeded` or
  `Failed`. An owner that finished earlier — particularly one whose Workload
  has already been deleted under `workloads.afterFinished` — carries no
  annotation and is never evaluated.

  Backfilling these is not proposed. It would require knowing when each
  owner finished, and `GenericJob` exposes only
  `Finished(ctx) (message, success, finished bool)` — whether, not when.
  Deriving the timestamp would mean either adding a completion-time method
  to `GenericJob` and implementing it across every integration, or reading
  each job type's own status from generic code. Both couple the framework to
  per-type details it deliberately abstracts.
  
## Proposal

Add a new field, `Jobs`, to `ObjectRetentionPolicies`, sibling to the existing
`Workloads` field, following the same `*metav1.Duration`-based shape already
established for `WorkloadRetentionPolicy.AfterFinished`.

### User Stories

#### Story 1 - Configurable retention for finished Job owners

As a Kueue administrator running large volumes of short-lived batch Jobs, I
want Kueue to automatically delete the Job object itself some time after its
Workload has finished, so that I don't need to run a separate cleanup
process or watch my cluster's Job count grow unbounded.

### Notes/Constraints/Caveats

The central design challenge for this KEP, not present in KEP-1618's
Workload-only scope, is an ordering problem: administrators will typically
want `jobs.afterFinished` to be *longer* than `workloads.afterFinished` (keep
the user-facing Job around for inspection/history longer than Kueue's own
internal bookkeeping object). But `handleWorkloadAfterDeactivatedPolicy` (and
its `AfterFinished` sibling), the existing mechanism this KEP extends, key
off the Workload's own `WorkloadEvicted`/`WorkloadFinished` condition
`LastTransitionTime` to decide when the *owner's* retention timer has
elapsed. If the Workload itself has already been deleted (per its own,
shorter retention policy) by the time the owner's longer retention window
is being evaluated, that timestamp is no longer available to check against.

The proposed mitigation: at Workload-finish time, before the Workload
becomes eligible for its own deletion, stamp the finish timestamp as an
annotation directly on the owner object. The owner's own retention check can
then read this annotation instead of depending on the Workload object's
continued existence. This is new plumbing — no existing mechanism in Kueue
currently stamps anything onto the owner at Workload-finish time
(`finalizeJob`, the closest existing owner-touching hook, today only invokes
an optional per-job-type `Finalize()` callback and does nothing generic to
the owner's metadata).

An alternative considered and rejected: simply require
`jobs.afterFinished <= workloads.afterFinished` as a validated configuration
constraint, avoiding the need for the stamping mechanism entirely. This was
rejected as too restrictive for the common case administrators actually
want (see [Alternatives](#alternatives)).

### Risks and Mitigations

- **R**: Stamping requires patch permission on each integration's owner type,
  and deletion requires delete. Some integrations have neither today —
  `apps/statefulsets` is declared `get;list;watch`.
  **M**: Markers are audited per integration during implementation; see
  [RBAC](#rbac). The grants are only exercised when the feature is enabled.
- **R**: In clusters with a large number of annotated finished owners,
  evaluating them against the retention policy during Kueue's initial
  reconciliation pass could be slow. The exposure is smaller than KEP-1618's,
  since only owners annotated since enablement are evaluated rather than every
  finished object in the cluster.
  **M**: Same approach as KEP-1618: no dedicated fix here; administrators can
  limit client QPS/burst while the feature is enabled.
- **R**: Deleting a Job (unlike deleting a Workload, which is an internal
  Kueue bookkeeping object) is a more consequential, user-visible operation
  — it can cascade-delete Pods and other objects the user may still want to
  inspect.
  **M**: The feature is opt-in and defaults to disabled, matching KEP-1618.
  A dedicated feature gate (`JobOwnerRetentionPolicy`) is proposed
  specifically because of this higher blast radius, separate from
  `ObjectRetentionPolicies`'s own (already-stable) gate.


## Design Details

### API
```go
// ObjectRetentionPolicies holds retention settings for different object types.
type ObjectRetentionPolicies struct {
    // Workloads configures retention for Workloads.
    // A nil value disables automatic deletion of Workloads.
    // +optional
    Workloads *WorkloadRetentionPolicy `json:"workloads,omitempty"`

    // Jobs configures retention for the Job-like objects (e.g. batch/Job,
    // JobSet) that own Kueue-managed Workloads.
    // A nil value disables automatic deletion of Job owners.
    // +optional
    Jobs *JobRetentionPolicy `json:"jobs,omitempty"`
}

// JobRetentionPolicy defines the policy for when a Job owner should be
// deleted after its Workload has finished.
type JobRetentionPolicy struct {
    // AfterFinished is the duration to wait after the owning Workload
    // finishes before deleting the Job.
    // A duration of 0 will delete immediately.
    // A nil value disables automatic deletion.
    // Represented using metav1.Duration (e.g. "10m", "1h30m").
    // +optional
    AfterFinished *metav1.Duration `json:"afterFinished,omitempty"`
}
```

### RBAC

Requirements differ per integration, and some will need new grants.

`batch/jobs` is declared
`verbs=get;list;watch;update;patch;delete`
(`pkg/controller/jobs/job/job_controller.go`), covering both the annotation
patch and the deletion — unsurprising, since
`handleWorkloadAfterDeactivatedPolicy` already deletes owners on the
deactivation path.

`apps/statefulsets` is declared `verbs=get;list;watch`
(`pkg/controller/jobs/statefulset/statefulset_reconciler.go`) and would need
`patch` and `delete` added. That is consistent with StatefulSet not being
suspendable: Kueue gates its pods rather than mutating the StatefulSet, so
it has never needed write access to the owner.

Each integration's markers therefore need auditing during implementation,
and the ones that gain `delete` are a meaningful permission increase given
the blast radius noted under Risks.

### Behavior

1. When a Workload's `WorkloadFinished` condition is set with reason
   `Succeeded` or `Failed`, Kueue stamps the finish timestamp as an
   annotation on the owner object. The stamp is written in `finalizeJob`
   (`pkg/controller/jobframework/reconciler.go`), which gains a
   `*kueue.Workload` parameter; both of its call sites in
   `ReconcileGenericJob` already have the Workload in scope.

   The annotation key is `kueue.x-k8s.io/workload-finished-at`, holding the
   value of `WorkloadFinished.LastTransitionTime` in RFC3339 as serialized by
   `metav1.Time`.

   The stamp is skipped when the owner has a `DeletionTimestamp` — retention
   is moot for an object already being deleted, and patching its metadata may
   fail.

   The reason check is required rather than incidental. `finalizeJob` is
   also reached from the `wl != nil && workloadfinish.IsFinished(wl)`
   branch, which fires for every finish reason — including several that do
   not mean the job ended:

   - `kueue.WorkloadSliceReplaced` — `normalizeActiveSlices` finishes a
     replaced slice to release quota it still holds, while the job
     continues on the surviving slice.
   - `kueue.WorkloadFinishedReasonOutOfSync` — set by
     `normalizeActiveSlices` and by `ensurePrebuiltWorkloadInSync`.
   - `FailedToStartFinishedReason` — the job never started.
   - `kueue.WorkloadFinishedReasonOwnerNotFound` — the owner is already
     gone.

   That branch is also how the motivating MultiKueue case arrives:
   `pkg/controller/admissionchecks/multikueue/workload.go` finishes the
   manager-side Workload directly, propagating the remote reason verbatim,
   and the job reconciler's `For(...).Owns(&kueue.Workload{})` watch maps
   that status write back to the owning job. No MultiKueue-specific
   handling is needed.

   The stamp is written only when the annotation is absent, so repeated
   reconciles do not move the timestamp forward.
2. During Kueue's reconciliation loop, an owner carrying this annotation is
   evaluated against `jobs.afterFinished`: if the retention period has
   elapsed, the owner is deleted (cascading to its dependent objects via
   normal Kubernetes garbage collection); otherwise it is requeued for
   reconciliation once the remaining duration has passed, mirroring the
   evaluate-then-delete-or-requeue pattern KEP-1618 established for
   Workloads.
3. During Kueue's initial reconciliation loop, owners carrying the annotation
   are evaluated the same way. Owners without it — those that finished before
   the feature was enabled — are skipped; see [Non-Goals](#non-goals).
4. `Finish` patches the Workload's status and the stamp is a separate write
   to a different object, so the two cannot be atomic. If the stamp write
   fails, the Workload is Finished and the owner unstamped. This retries on
   its own: while the Workload exists, any subsequent event on it or its job
   reaches `finalizeJob` again. A permanent leak therefore requires the
   write to fail continuously for the whole of `workloads.afterFinished`,
   and in that case the owner is simply never collected — today's behavior,
   not a regression.
5. Owner deletion uses `client.Preconditions{UID: ...}` so that a same-named
   owner recreated between the fetch and the delete is not deleted by
   mistake. Note the existing deactivation path does not do this today:
   `handleWorkloadAfterDeactivatedPolicy` calls `r.client.Delete` with only
   a propagation policy. Applying the precondition to both paths is
   proposed, but can be split into a separate change if preferred.

6. No integration or namespace filtering is applied in the retention path
   itself. `ReconcileGenericJob` enforces `managedJobsNamespaceSelector`
   and returns early for namespaces not opted in before any retention logic
   runs, and the reconciler is only registered for enabled integrations, so
   owner deletion applies only to jobs Kueue already manages. Behavior is
   unchanged when retention is unset: the path returns early when the new
   field is nil, mirroring `shouldHandleDeletionOfDeactivatedWorkload`.

### Test Plan

[X] I understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes
necessary to implement this enhancement.

#### Unit Tests

TBD — to be filled in during implementation, following the pattern
established in KEP-1618's own unit test plan (`pkg/controller/core/workload_controller_test.go`,
`apis/config/v1beta1/defaults_test.go`, `pkg/config/config_test.go`).

#### Integration tests

- the stamp firing at Workload-finish time for `Succeeded`/`Failed`, and not
  firing for `WorkloadSliceReplaced` or `OutOfSync`
- a MultiKueue manager-side owner: the manager Workload is finished by remote
  propagation, the Workload is deleted under a shorter
  `workloads.afterFinished`, and the owner is still deleted correctly when
  `jobs.afterFinished` later elapses
- default-disabled backward-compatible behavior

### Graduation Criteria

TBD

## Implementation History

- Issue filed: [#14672](https://github.com/kubernetes-sigs/kueue/issues/14672)

## Drawbacks

Same drawback noted in KEP-1618 applies here, amplified by the higher blast
radius of deleting user-visible Job objects rather than internal Workload
bookkeeping objects: garbage collection behavior can be surprising to users
not expecting it, even when explicitly opted in.

## Alternatives

- Require `jobs.afterFinished <= workloads.afterFinished` as a validated
  configuration constraint, avoiding the need for the annotation-stamping
  mechanism. Rejected: this forces an artificial floor on how long Workload
  objects must be retained purely to serve the owner's retention window,
  which is backwards from what most administrators actually want (a long Job
  retention with a much shorter, near-immediate Workload cleanup).
- Leave Job-owner cleanup entirely to external tooling / users, as is the
  status quo today. Rejected per the motivating issue and the precedent
  KEP-1618 already set for exactly this class of problem at the Workload
  level.