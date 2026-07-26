# KEP-13243: MultiKueue Worker-Side Ray In-Tree Autoscaling for Elastic Jobs

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1: Online/offline inference](#story-1-onlineoffline-inference)
    - [Story 2: Colocated training and evaluation](#story-2-colocated-training-and-evaluation)
  - [Notes/Constraints/Caveats](#notesconstraintscaveats)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [Background: manager-driven forward sync](#background-manager-driven-forward-sync)
  - [Reverse elastic sync](#reverse-elastic-sync)
    - [RayCluster: spec reflection](#raycluster-spec-reflection)
    - [RayJob: runtime reflection](#rayjob-runtime-reflection)
  - [Worker-side resize tolerance](#worker-side-resize-tolerance)
  - [ElasticJobUngater: resolving a deleted slice-chain root](#elasticjobungater-resolving-a-deleted-slice-chain-root)
  - [Retaining enableInTreeAutoscaling on the remote copy](#retaining-enableintreeautoscaling-on-the-remote-copy)
  - [Test Plan](#test-plan)
    - [Prerequisite testing updates](#prerequisite-testing-updates)
    - [Unit tests](#unit-tests)
    - [Integration tests](#integration-tests)
    - [e2e tests](#e2e-tests)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
<!-- /toc -->

## Summary

Kueue can dispatch an elastic RayCluster or RayJob (`ElasticJobsViaWorkloadSlices`)
to a worker cluster through MultiKueue, and the manager-driven forward sync
([#12885](https://github.com/kubernetes-sigs/kueue/pull/12885)) propagates
manager-side resizes down to that worker copy. But when the workload is meant to
be resized by the **Ray in-tree autoscaler running on the worker cluster**, there
is no path for those worker-side resizes to travel back to the manager. As a
result, the manager's quota accounting drifts from reality and the manager can
overwrite the autoscaler's changes, tearing down the running job.

This KEP proposes the missing **worker→manager** direction — a *reverse elastic
sync* in the shared Ray adapter — so an autoscaler-driven resize on the worker
cluster is reflected onto the manager object, and the manager re-reserves quota
through its existing workload-slicing machinery. It also covers the two
supporting changes required to make the handover non-disruptive: a worker-side
resize tolerance in the jobframework, and a fix to the elastic-job ungater so it
does not strand pods when MultiKueue deletes the root of a slice chain.

## Motivation

Ray's in-tree autoscaler is the natural way to run elastic Ray workloads: it
grows and shrinks worker groups in response to the actual resource demands of the
application (pending tasks, actors, placement groups). Many real workloads depend
on it — mixed online/offline inference, and training colocated with evaluation in
a single long-lived RayCluster.

MultiKueue today forces such workloads into a *manager-driven* model: the
forward sync clears `enableInTreeAutoscaling` on the remote copy so the worker's
autoscaler cannot fight the manager, as documented in
[#12885](https://github.com/kubernetes-sigs/kueue/pull/12885):

> Because scaling is manager-driven, the remote copy must not run the in-tree Ray
> autoscaler (which would otherwise fight the manager by editing replicas on the
> worker cluster); the adapter clears `enableInTreeAutoscaling` on the remote copy
> of an elastic RayCluster.

That is correct for manager-driven resizing, but it blocks every use case that
relies on the worker's own autoscaler. To unblock them, the resize decision must
be allowed to *originate on the worker* and flow back to the manager, which
remains the quota authority.

### Goals

- Let the Ray in-tree autoscaler resize a MultiKueue-dispatched elastic
  `RayCluster` or `RayJob` on the worker cluster, and reflect that resize back
  onto the manager object.
- Keep the manager as the single quota authority: worker-originated resizes flow
  through the manager's workload-slicing admission (quota re-reservation), never
  bypass it.
- Make the resize handover non-disruptive to the running job: no false
  `OutOfSync` finish, no stranded pods, no quota under-reservation.
- Bound what the worker autoscaler is allowed to request to the manager-declared
  `[minReplicas, maxReplicas]` range.

### Non-Goals

- A brand-new autoscaling algorithm — this reuses Ray's in-tree autoscaler and
  Kueue's existing workload slicing.
- Extending reverse elastic sync to non-Ray integrations in this KEP (the adapter
  hooks are designed to generalize, but only Ray is implemented here).
- A webhook-level authz guard preventing tenants from hand-setting the
  runtime replica-size annotation on their own RayJob (tracked as a follow-up;
  value validation is present).
- Manager-driven and worker-driven resizing of the *same* object at the same
  time; a given elastic object is resized by one side.

## Proposal

Add a **reverse elastic sync** to the shared Ray adapter that detects an
autoscaler-driven resize of the remote (worker) copy and reflects it onto the
manager object, where the existing workload-slicing machinery re-reserves quota.
Two type-specific mechanisms are used, chosen by where the worker replica counts
actually live, and grouped as mutually-exclusive hook structs validated at wiring
time:

- **RayCluster** — replicas live on the CR spec, so the worker resize is written
  back onto the manager RayCluster spec.
- **RayJob** — replicas live on the child RayCluster that KubeRay creates on the
  worker, so the child's per-group counts are reflected onto the manager RayJob
  as annotations that feed the manager's PodSets derivation.

Two supporting changes make the handover safe: a worker-scoped resize tolerance
in the jobframework, and an ungater fix for deleted slice-chain roots.

### User Stories

#### Story 1: Online/offline inference

A team runs a long-lived RayCluster serving online inference, with the in-tree
autoscaler adding worker replicas as offline batch inference demand spikes and
removing them when it subsides. They want Kueue/MultiKueue to place this cluster
on a worker cluster with spare quota and keep the manager's quota books accurate
as the autoscaler resizes it — without the manager fighting the autoscaler.

#### Story 2: Colocated training and evaluation

A team colocates a training job and periodic evaluation actors in the same
RayCluster. Evaluation bursts cause the autoscaler to grow the cluster
temporarily. They want those autoscaler-driven resizes admitted against their
ClusterQueue quota (through slice replacement) rather than blocked or reverted.

### Notes/Constraints/Caveats

- The feature applies only to elastic (`ElasticJobsViaWorkloadSlices`) Ray objects
  dispatched through MultiKueue with `enableInTreeAutoscaling` set.
- Only `Replicas` moves in the worker→manager direction. Structural changes
  (adding/removing worker groups, resource shapes) remain manager-owned.
- Counts outside the manager-declared `[minReplicas, maxReplicas]` cannot
  legitimately come from the autoscaler and are ignored.

### Risks and Mitigations

- **The manager overwrites an autoscaler resize (fight loop).** In autoscaling
  mode the forward sync never pushes replicas; its only remaining duty is
  repointing the remote's prebuilt-workload marker at the current slice
  (idempotent). Replicas flow one way — worker→manager — while autoscaling is on.
- **A tenant forges replica counts via the runtime annotation.** The reflected
  value is validated (parseable, within `[min,max]`); a malformed annotation
  falls back to the spec counts. An authz-level guard is a documented follow-up.
- **The handover finishes the workload `OutOfSync` and tears the job down.** A
  worker-scoped resize tolerance absorbs the transient job/slice count mismatch
  during handover (see below).
- **Pods stranded behind scheduling gates after slice replacement.** The ungater
  fix resolves the slice chain through any surviving member when its root has been
  deleted by MultiKueue.

## Design Details

### Background: manager-driven forward sync

[#12885](https://github.com/kubernetes-sigs/kueue/pull/12885) established the
manager→worker direction for elastic RayClusters: a resize on the manager is
propagated to the remote copy, and the remote copy runs with
`enableInTreeAutoscaling` cleared so it cannot self-resize. This KEP adds the
opposite direction and, for autoscaling objects, retains
`enableInTreeAutoscaling` on the remote copy.

### Reverse elastic sync

The shared Ray adapter gains reverse-sync hooks, grouped as two mutually
exclusive structs and validated when the adapter is wired:

- `Spec{Push, Reflect, Counts}` — used when worker replicas live on the CR.
- `Runtime{Fetch, Apply}` — used when worker replicas live on a child object.

#### RayCluster: spec reflection

For an elastic RayCluster, the worker replicas live directly on the remote CR.
When the worker autoscaler resizes the remote copy, the adapter's `Reflect` hook
writes the new `Replicas` back onto the manager RayCluster spec. The manager's
workload-slicing machinery observes the spec change and re-reserves quota (slice
replacement on scale-up; in-place slice update on scale-down). Only `Replicas`
is reflected, and only within the manager-declared `[minReplicas, maxReplicas]`.

#### RayJob: runtime reflection

For an elastic RayJob, the worker replicas live on the child RayCluster that
KubeRay creates on the worker cluster, not on the RayJob spec. The adapter
reflects the child's per-group counts, plus a `childUID-generation` revision, onto
the manager RayJob as annotations. Those annotations feed the manager's PodSets
derivation (gated on `spec.managedBy`) and workload-slice naming. Count-neutral
child updates (same per-group counts) do not mint a replacement slice; the
revision distinguishes a genuine resize from an unrelated child update.

### Worker-side resize tolerance

During a resize handover, the job's observed count on the worker can transiently
differ from the admitted slice's count. Without tolerance, that mismatch finishes
the workload `OutOfSync` and tears the job down. This KEP adds a resize tolerance
in the jobframework, scoped to MultiKueue-dispatched copies via the origin label,
so a transient mismatch during handover is not treated as divergence:

- On scale-up, extra pods stay behind scheduling gates and are never admitted
  past quota.
- On scale-down, the workload briefly over-reserves and is never under-reserved.

### ElasticJobUngater: resolving a deleted slice-chain root

The worker-side `ElasticJobUngater` resolves a slice chain by loading its root
workload. On a MultiKueue worker, the manager deletes the remote copy of a
replaced slice, so the chain's root can be gone while newer slices and gated pods
that name the chain key still exist. The ungater previously dead-ended there,
leaving pods `SchedulingGated` forever. It now falls back to resolving the chain
through any surviving member (a workload whose `workloadslicing.SliceName`
matches the chain key).

### Retaining enableInTreeAutoscaling on the remote copy

In autoscaling mode the remote copy keeps `enableInTreeAutoscaling`, so the worker
runs the autoscaler sidecar (accounted for in the head PodSet). The forward sync
never pushes replicas in this mode; its only duty is repointing the remote's
prebuilt-workload marker at the current slice, which is idempotent.

### Test Plan

[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes
necessary to implement this enhancement.

#### Prerequisite testing updates

None beyond the coverage described below.

#### Unit tests

- Reverse-sync hooks for RayCluster (spec reflection) and RayJob (runtime
  reflection), including the `[min,max]` clamp, the `childUID-generation`
  revision, count-neutral updates that must not mint a slice, and the
  malformed-annotation fallback to spec counts.
- Hook-pairing validation (`Spec`/`Runtime` mutually exclusive) at wiring time.
- Jobframework worker-side resize tolerance, gated on the origin label.
- ElasticJobUngater chain resolution when the root is deleted (fail-without,
  pass-with).

#### Integration tests

- MultiKueue elastic RayCluster/RayJob resize handover: worker-originated resize
  reflected onto the manager; slice replacement on scale-up; in-place update on
  scale-down; no `OutOfSync` churn; quota reservation exact.

#### e2e tests

- Real-autoscaler e2e on two kind clusters (KubeRay operator on the worker,
  scaling driven via `ray.autoscaler.sdk.request_resources`): RayCluster 1→3→1
  and RayJob child 1→3→1 reflected onto the manager; scale-up replaces the slice
  (admitted by the real scheduler, prebuilt marker repointed), scale-down updates
  the slice in place; post-handover pods reach `Running`; ClusterQueue
  reservation exact including the autoscaler sidecar; MultiKueue GC clean after
  deletion.

### Graduation Criteria

Alpha:

- Reverse elastic sync implemented for RayCluster and RayJob behind the existing
  `ElasticJobsViaWorkloadSlices` gate + MultiKueue.
- Unit, integration, and real-autoscaler e2e coverage as above.

Beta (tentative):

- Address the documented follow-ups: authz guard for the runtime replica-size
  annotation, and an optional time bound on the resize tolerance.
- Metrics/observability for reverse-sync resizes.
- Broader soak/e2e coverage in CI (fullray periodic jobs).

## Implementation History

- 2026-07-26: KEP drafted.
- Prototype and verification in
  [#13435](https://github.com/kubernetes-sigs/kueue/pull/13435) (reverse elastic
  sync, worker-side resize tolerance, ungater chain-root fix; verified with unit,
  integration, and real-autoscaler e2e).

## Drawbacks

- Adds a second sync direction to the Ray adapter, increasing the surface area of
  MultiKueue's elastic handling and the number of interleavings to reason about.
- Retaining `enableInTreeAutoscaling` on the remote copy makes the worker a
  source of truth for replica counts, which must be carefully bounded to the
  manager-declared range to keep the manager as the quota authority.

## Alternatives

- **Manager-driven only (status quo).** Simple and already shipped, but blocks
  every use case that needs the worker's in-tree autoscaler.
- **Reject `enableInTreeAutoscaling` for MultiKueue elastic Ray objects.**
  [#13244](https://github.com/kubernetes-sigs/kueue/pull/13244) takes this stance
  as an interim guard until this support exists; it prevents silent misbehavior
  but does not deliver worker-side autoscaling. Once this KEP lands, that
  rejection is lifted.
- **Run the autoscaler on the manager against the remote cluster's metrics.**
  Requires the manager to reach worker-cluster application metrics and duplicates
  the autoscaler's placement logic across the MultiKueue boundary; rejected as
  more complex and less faithful to how Ray autoscaling works.
