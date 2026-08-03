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
    - [RayCluster](#raycluster)
    - [RayJob](#rayjob)
    - [Workload-slice naming under annotation reflection](#workload-slice-naming-under-annotation-reflection)
  - [Worker-side resize tolerance](#worker-side-resize-tolerance)
  - [Handover safety under frequent slice replacement](#handover-safety-under-frequent-slice-replacement)
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
be resized by the [Ray Autoscaler](https://docs.ray.io/en/latest/cluster/kubernetes/user-guides/configuring-autoscaling.html), there
is no path for those worker-side resizes to travel back to the manager.

This KEP proposes the missing **worker→manager** direction — a *reverse elastic
sync* in the shared Ray adapter — so an autoscaler-driven resize on the worker
cluster is reflected onto the manager object, and the manager re-reserves quota
through its existing workload-slicing machinery.

## Motivation

MultiKueue today supports only a *manager-driven* resize model for elastic Ray
workloads, built on the forward sync from
[#12885](https://github.com/kubernetes-sigs/kueue/pull/12885):

- Step 1: a user (or an external controller) edits the manager RayCluster's
  replicas.
- Step 2: MultiKueue forward-syncs the new count to the worker copy.
- Step 3: the worker's KubeRay adjusts the worker pods to match.

But the [Ray Autoscaler](https://docs.ray.io/en/latest/cluster/kubernetes/user-guides/configuring-autoscaling.html)
is the natural way to run these workloads — it grows and shrinks worker groups in
response to the actual resource demands of the application:

- Step 1: the user runs a Ray application (submitting tasks, actors, or placement
  groups) on the RayCluster.
- Step 2: the Ray Autoscaler observes that demand and decides to scale, editing
  the RayCluster CR — raising a worker group's `replicas` to add workers, or
  listing specific pods in `scaleStrategy.workersToDelete` to remove them.
- Step 3: KubeRay reconciles the updated CR and creates or deletes the worker
  pods.

Many real workloads depend on it — mixed online/offline inference, and training
colocated with evaluation in a single long-lived RayCluster. The manager-driven-only
model shuts out every such use case. To unblock them, the resize decision must be
allowed to *originate on the worker* and *flow back to the manager*, which remains
the quota authority.

### Goals

- Let the Ray Autoscaler resize a MultiKueue-dispatched elastic
  `RayCluster` or `RayJob` on the worker cluster, and reflect that resize back
  onto the manager object.
- Keep the manager as the single quota authority: worker-originated resizes flow
  through the manager's workload-slicing admission (quota re-reservation), never
  bypass it.
- Make the resize handover non-disruptive to the running job: no false
  `OutOfSync` finish, no stranded pods, no quota under-reservation.
- Keep the reflected worker count bounded to the RayCluster's own
  `[minReplicas, maxReplicas]`, which the Ray autoscaler already enforces on the
  worker cluster.

### Non-Goals

- A brand-new autoscaling algorithm — this reuses the Ray Autoscaler and
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
autoscaler-driven resize on the worker cluster and reflects it onto the manager
object **as annotations, leaving the manager spec untouched**, where the existing
workload-slicing machinery re-reserves quota. A single `Runtime{Fetch, Apply}`
hook drives it for both types; only *where the live worker replicas are read*
differs:

- **RayCluster** — the replicas live on the remote RayCluster copy itself.
- **RayJob** — the replicas live on the child RayCluster that KubeRay creates on
  the worker (the child never exists on the manager), so its per-group counts are
  read from there.

In both cases `Apply` records the counts (and a revision) on the manager copy as
the `raycluster-podset-replica-sizes` and `raycluster-generation` annotations,
which feed the manager's PodSets derivation and the workload-slice name.

The handover is made safe by a worker-scoped resize tolerance in the jobframework
(this KEP), together with the already-merged
[#13489](https://github.com/kubernetes-sigs/kueue/pull/13489), which finishes a
replaced slice as `WorkloadSliceReplaced` so MultiKueue does not delete a slice
mid-handover and strand its pods.

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
- Only per-worker-group replica counts move in the worker→manager direction, and
  only as annotations — the manager spec is never rewritten. Structural changes
  (adding/removing worker groups, resource shapes) remain manager-owned.
- The reflected count is whatever the worker autoscaler settled on, which the
  RayCluster's own `[minReplicas, maxReplicas]` already bounds on the worker side.

### Risks and Mitigations

- **The manager overwrites an autoscaler resize (fight loop).** In autoscaling
  mode the forward sync never pushes replicas; its only remaining duty is
  repointing the remote's prebuilt-workload marker at the current slice
  (idempotent). Replicas flow one way — worker→manager — while autoscaling is on.
- **A tenant forges replica counts via the runtime annotation.** The reflected
  value is parsed and a malformed annotation is ignored (the manager falls back to
  the spec counts). An authz-level guard against a tenant hand-setting the
  annotation is a documented follow-up.
- **The handover finishes the workload `OutOfSync` and tears the job down.** A
  worker-scoped resize tolerance absorbs the transient job/slice count mismatch
  during handover (see below).
- **Pods stranded behind scheduling gates after slice replacement.** Fixed by the
  merged prerequisite [#13489](https://github.com/kubernetes-sigs/kueue/pull/13489):
  a replaced slice is finished as `WorkloadSliceReplaced`, so MultiKueue's elastic
  guard no longer deletes the slice (and its chain root) mid-handover.

## Design Details

### Background: manager-driven forward sync

[#12885](https://github.com/kubernetes-sigs/kueue/pull/12885) established the
manager→worker direction for elastic RayClusters: a resize on the manager is
propagated to the remote copy. To keep scaling strictly manager-driven, in-tree
autoscaling on a MultiKueue-managed elastic RayCluster is currently rejected at
admission ([#13244](https://github.com/kubernetes-sigs/kueue/pull/13244)). This
KEP adds the opposite direction and, for autoscaling objects, retains
`enableInTreeAutoscaling` on the remote copy, lifting that rejection.

### Reverse elastic sync

The shared Ray adapter gains a single reverse-sync hook, `Runtime{Fetch, Apply}`,
guarded by an `AutoscalingEnabled` predicate that turns the reverse direction on
only when the object runs the worker autoscaler. Wiring is validated when the
adapter is built: if `AutoscalingEnabled` is set, `Runtime` (with both `Fetch` and
`Apply`) is required. Both RayCluster and RayJob use the same hook — the reflection
is **annotation-based for both**, leaving the manager spec untouched. Each
reconcile of an autoscaling object:

1. `Fetch(remoteClient, remoteJob)` reads the effective per-worker-group pod counts
   from the worker cluster, plus a `UID-generation` **revision** of the object that
   holds those counts. A suspended remote is skipped (its counts were restored by
   the worker's Kueue while stopping the job, not set by the autoscaler).
2. `Apply(localJob, counts, revision)` records them on the **manager** copy as two
   annotations — `raycluster-podset-replica-sizes` (the counts) and
   `raycluster-generation` (the revision) — leaving the manager spec untouched.
   Equality is decided on the counts alone, so a count-neutral revision bump does
   not re-annotate or mint a replacement slice.

The manager's PodSets derivation reads `raycluster-podset-replica-sizes` (gated on
`spec.managedBy`), so its admitted PodSet counts follow the worker autoscaler, and
the workload-slicing machinery re-reserves quota — slice replacement on scale-up,
in-place slice update on scale-down.

The only per-type difference is **where `Fetch` reads the live worker replicas**:

#### RayCluster

The worker replicas live on the remote RayCluster copy itself, so `Fetch` reads
that copy directly; the revision is the remote RayCluster's `UID-generation`.

#### RayJob

The worker replicas live on the **child RayCluster** that KubeRay creates on the
worker cluster (the child never exists on the manager), so `Fetch` reads the child
by `status.rayClusterName`; the revision is the child's `UID-generation`.

#### Workload-slice naming under annotation reflection

Because the reverse sync reflects onto the manager as **annotations**, it never
bumps the manager object's `metadata.generation`. The elastic workload-slice name
must still change on each reflected scale-up so the manager mints a fresh
replacement slice — a same-named slice would fail to create ("already exists") and
leave the scaled worker pods gated. Both the RayJob and RayCluster jobs therefore
fold the `raycluster-generation` revision into the slice name
(`GetWorkloadNameExtraPart`): the name keys on the manager `generation` **and** the
reflected worker `UID-generation`, which advances on every worker-side resize. The
UID component keeps the name unique across a remote recreation, whose generation
restarts from 1.

### Worker-side resize tolerance

During a resize handover, the job's observed count on the worker can transiently
differ from the admitted slice's count. Without tolerance, that mismatch finishes
the workload `OutOfSync` and tears the job down. This KEP adds a resize tolerance
in the jobframework, scoped to MultiKueue-dispatched copies via the origin label,
so a transient mismatch during handover is not treated as divergence:

- On scale-up, extra pods stay behind scheduling gates and are never admitted
  past quota.
- On scale-down, the workload briefly over-reserves and is never under-reserved.

### Handover safety under frequent slice replacement

Worker-driven resizing replaces workload slices frequently. When a slice is
replaced, the manager's MultiKueue reconciler recognizes the old slice only when
it is finished as `WorkloadSliceReplaced`; a slice mistakenly finished as
`OutOfSync` slips past that guard and its remote objects are deleted mid-handover,
which can strand the scaled pods behind scheduling gates. This feature therefore
depends on the merged
[#13489](https://github.com/kubernetes-sigs/kueue/pull/13489), which finishes a
replaced slice as `WorkloadSliceReplaced` and closes that race.

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

- Reverse-sync `Runtime{Fetch, Apply}` for RayCluster (reads its own remote copy)
  and RayJob (reads the child RayCluster), including the `UID-generation` revision,
  count-neutral updates that must not re-annotate or mint a slice, the
  suspended-remote skip, and the malformed-annotation fallback to spec counts.
- Workload-slice naming folds in the reflected revision
  (`GetWorkloadNameExtraPart`): a fresh reflected count yields a new slice name.
- Adapter-wiring validation: `Runtime` (with `Fetch` and `Apply`) required when
  `AutoscalingEnabled` is set.
- Jobframework worker-side resize tolerance, gated on the origin label.

#### Integration tests

- MultiKueue elastic RayCluster/RayJob resize handover: worker-originated resize
  reflected onto the manager; slice replacement on scale-up; in-place update on
  scale-down; no `OutOfSync` churn; quota reservation exact.

#### e2e tests

- Real-autoscaler e2e (extended MultiKueue suite: manager + worker clusters,
  KubeRay operator on the workers), scaling driven by detached Ray actors that each
  require a per-worker resource so the actual Ray autoscaler resizes the group. A
  standalone RayCluster and a RayJob's child are each driven **up (2) → down (0) →
  up (1)**, and every resize is reflected onto the manager. Assertions cover the
  manager runtime annotation, the worker `DesiredWorkerReplicas` and Running
  worker-pod count, the ClusterQueue admitted count, and — on **both** the manager
  and the worker cluster — exactly one live, admitted workload slice reserving the
  reflected count. The slice lifecycle is checked by slice **name** (in-place on
  scale-down, fresh replacement on scale-up), which keeps the assertions robust to
  the autoscaler transiently overshooting the requested count. Worker RayCluster
  not torn down mid-handover; MultiKueue GC clean after deletion.

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
  sync, worker-side resize tolerance; verified with unit, integration, and
  real-autoscaler e2e — including on real GPU clusters).
- 2026-08-01: reverse sync unified onto a single annotation-based
  `Runtime{Fetch, Apply}` for both RayCluster and RayJob — RayCluster no longer
  writes back to the manager spec; the reflected size reuses the existing
  `raycluster-podset-replica-sizes` annotation; the elastic workload-slice name
  folds in the reflected revision so annotation-only reflection still mints fresh
  slices; e2e specs hardened to up/down/up with name-based, overshoot-tolerant
  slice assertions.
- The initially-prototyped ElasticJobUngater chain-root workaround was dropped in
  favor of the merged root-cause fix
  [#13489](https://github.com/kubernetes-sigs/kueue/pull/13489) (finish a replaced
  slice as `WorkloadSliceReplaced`).

## Drawbacks

- Adds a second sync direction to the Ray adapter, increasing the surface area of
  MultiKueue's elastic handling and the number of interleavings to reason about.
- Retaining `enableInTreeAutoscaling` on the remote copy makes the worker a
  source of truth for replica counts, which must be carefully bounded to the
  manager-declared range to keep the manager as the quota authority.

## Alternatives

- **Manager-driven only (status quo).** Simple and already shipped, but blocks
  every use case that needs the worker's Ray Autoscaler.
- **Reject `enableInTreeAutoscaling` for MultiKueue elastic Ray objects.**
  [#13244](https://github.com/kubernetes-sigs/kueue/pull/13244) takes this stance
  as an interim guard until this support exists; it prevents silent misbehavior
  but does not deliver worker-side autoscaling. Once this KEP lands, that
  rejection is lifted.
- **Run the autoscaler on the manager against the remote cluster's metrics.**
  Requires the manager to reach worker-cluster application metrics and duplicates
  the autoscaler's placement logic across the MultiKueue boundary; rejected as
  more complex and less faithful to how Ray autoscaling works.
