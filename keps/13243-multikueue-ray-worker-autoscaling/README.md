# KEP-13243: MultiKueue Worker-Side Ray In-Tree Autoscaling for Elastic Jobs

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1: Online and offline inference](#story-1-online-and-offline-inference)
    - [Story 2: Colocated training and evaluation](#story-2-colocated-training-and-evaluation)
  - [Notes/Constraints/Caveats](#notesconstraintscaveats)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [Reverse elastic sync](#reverse-elastic-sync)
    - [RayCluster](#raycluster)
    - [RayJob](#rayjob)
    - [Workload-slice naming under annotation reflection](#workload-slice-naming-under-annotation-reflection)
  - [Manager-side replicas pinning](#manager-side-replicas-pinning)
  - [Worker-side resize tolerance](#worker-side-resize-tolerance)
  - [Triggering the reverse sync](#triggering-the-reverse-sync)
  - [Test Plan](#test-plan)
    - [Prerequisite testing updates](#prerequisite-testing-updates)
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
allowed to **originate on the worker** and **flow back to the manager**, which
remains the quota authority.

### Goals

- Let the Ray Autoscaler resize a MultiKueue-dispatched elastic
  `RayCluster` or `RayJob` on the worker cluster, and reflect that resize back
  onto the manager object — and extend the same mechanism to `RayService` once its
  zero-downtime / incremental upgrade support is complete.
- Keep the manager as the single quota authority: worker-originated resizes flow
  through the manager's workload-slicing admission (quota re-reservation), never
  bypass it.

### Non-Goals

- A brand-new autoscaling algorithm — this reuses the Ray Autoscaler and
  Kueue's existing workload slicing.
- Extending reverse elastic sync to non-Ray integrations in this KEP (the adapter
  hooks are designed to generalize, but only Ray is implemented here).
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

### User Stories

#### Story 1: Online and offline inference

The Ray autoscaler sizes each worker group independently to match its demand:

- Online inference is served by a long-lived `RayService`; its worker groups scale
  up as request load rises and back down when it subsides.
- Offline / batch inference runs as a `RayCluster` or `RayJob` over multi-modal
  data whose stages need different resources — for example CPU workers for image
  decoding and GPU workers for captioning — so the autoscaler grows and shrinks
  each worker group as the batch moves through the pipeline.

#### Story 2: Colocated training and evaluation

A team colocates a training job and periodic evaluation actors in the same
RayCluster. Evaluation bursts cause the autoscaler to grow the cluster
temporarily.

### Notes/Constraints/Caveats

- The feature is gated by the new `MultiKueueRayInTreeAutoscaling` feature gate
  (alpha, off by default) and applies only to elastic
  (`ElasticJobsViaWorkloadSlices`) Ray objects dispatched through MultiKueue with
  `enableInTreeAutoscaling` set.
- Only per-worker-group replica counts move in the worker→manager direction, and
  only as annotations — the manager spec is never rewritten. Structural changes
  (adding/removing worker groups, resource shapes) remain manager-owned.

### Risks and Mitigations

- **The owner of the worker group's `replicas` differs with and without
  autoscaling.** Without autoscaling the manager owns them; with autoscaling on, the
  worker autoscaler does, and letting both write the same fields would fight. While
  autoscaling is on, a validating webhook on the manager rejects manager-side
  `replicas` edits (see
  [Manager-side replicas pinning](#manager-side-replicas-pinning)).
- **The handover finishes the workload `OutOfSync` and tears the job down.** A
  worker-scoped resize tolerance absorbs the transient job/slice count mismatch
  during handover (see below).

## Design Details

The end-to-end flow of a worker-side autoscaler resize:

1. An elastic, autoscaling `RayCluster` (or `RayJob`) is dispatched through
   MultiKueue and runs on a **worker** cluster with `enableInTreeAutoscaling` on;
   the **manager** holds the admitted workload slice (the quota).
2. A Ray application on the worker creates pending tasks, actors, or placement
   groups.
3. The **Ray Autoscaler** grows or shrinks the worker group to meet that demand,
   editing the worker RayCluster's replicas; KubeRay then adds or removes worker
   pods.
4. On the next manager reconcile, the adapter's reverse-sync **`Fetch`** reads the
   effective per-worker-group counts from the worker-side RayCluster's **spec**
   plus a revision, and **`Apply`**
   records them on the manager copy as annotations — the manager spec is never
   touched.
5. The manager's PodSets follow the reflected counts, so the workload-slicing
   machinery **re-reserves quota**: a freshly named replacement slice on scale-up,
   or an in-place update on scale-down.
6. On scale-up the admitted slice changes name, so the forward sync **repoints**
   the worker job's prebuilt-workload marker to the new slice, keeping the
   already-running worker job linked to the current slice.
7. When the manager **suspends** the job (eviction, preemption, requeue), the
   reflected annotations are **cleared** alongside the existing PodSets restore, so
   re-admission reserves at the spec baseline rather than the last autoscaled size.

The subsections below detail each step.

### Reverse elastic sync

The shared Ray adapter gains a single reverse-sync hook, `Runtime{Fetch, Apply}`,
guarded by an `AutoscalingEnabled` predicate that turns the reverse direction on
only when the object runs the worker autoscaler. Both RayCluster and RayJob use
the same hook — the reflection is **annotation-based for both**, leaving the
manager spec untouched.

```go
type RuntimeReplicaSync[PtrT any] struct {
	// Fetch reads the effective per-worker-group pod counts from the
	// worker-side RayCluster's spec, plus a revision identifying the observed
	// runtime state.
	Fetch func(ctx context.Context, remoteClient client.Client, remoteJob PtrT) (
		counts map[kueue.PodSetReference]int32, revision string, found bool, err error)
	// Apply records them onto the manager copy (as annotations), returning
	// whether anything changed.
	Apply func(localJob client.Object, counts map[kueue.PodSetReference]int32, revision string) bool
}
```

Each reconcile of an autoscaling object:

1. `Fetch(remoteClient, remoteJob)` reads the effective per-worker-group pod counts
   from the worker-side RayCluster's **spec** (the object's own remote copy, or a
   RayJob's child cluster), plus a `UID-generation` **revision** of the object that
   holds those counts. The revision's role is to give each reflected
   scale-up a distinct workload-slice name (details under [Workload-slice
   naming](#workload-slice-naming-under-annotation-reflection)).
2. `Apply(localJob, counts, revision)` records them on the **manager** copy as two
   annotations — `raycluster-podset-replica-sizes` (the counts) and
   `raycluster-generation` (the revision) — leaving the manager spec untouched.
   Equality is decided on the counts alone, so a count-neutral revision bump does
   not re-annotate or mint a replacement slice.

The manager's PodSets derivation reads `raycluster-podset-replica-sizes` (gated on
`spec.managedBy`), so its admitted PodSet counts follow the worker autoscaler, and
the workload-slicing machinery re-reserves quota — slice replacement on scale-up,
in-place slice update on scale-down.

The only per-type difference is **where `Fetch` reads the live worker replicas**,
which splits the job types into two kinds:

1. The worker replica counts can be read **directly from the object's own CR** —
   the object itself carries them (e.g. a standalone `RayCluster`).
2. The counts are **not** on the object's CR and must be read from a **child
   resource** the framework creates on the worker (e.g. `RayJob`, whose child
   `RayCluster` is created by KubeRay).

The two `Fetch` implementations map onto these:

#### RayCluster

The worker replicas live on the remote RayCluster copy's spec, so `Fetch` reads
that spec directly; the revision is the remote RayCluster's `UID-generation`.

```go
// RayCluster: Fetch reads the remote RayCluster copy itself.
revision := fmt.Sprintf("%s-%d", remoteCluster.UID, remoteCluster.Generation)
```

#### RayJob

The worker replicas live on the spec of the **child RayCluster** that KubeRay
creates on the worker cluster (the child never exists on the manager), so `Fetch`
resolves the child by `status.rayClusterName` and reads its spec; the revision is
the child's `UID-generation`.

```go
// RayJob: Fetch reads the child RayCluster (status.rayClusterName) on the worker.
child := getRemoteChild(remoteJob.Status.RayClusterName)
revision := fmt.Sprintf("%s-%d", child.UID, child.Generation)
```

#### Workload-slice naming under annotation reflection

The `raycluster-generation` annotation predates this KEP:
[#9960](https://github.com/kubernetes-sigs/kueue/pull/9960) introduced it for
RayJob and RayService with the child RayCluster's bare `generation`, skipping
standalone RayClusters. Here the reflected value is `UID-generation` instead.

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

```go
func GetWorkloadNameExtraPart(obj metav1.Object) string {
	extra := strconv.FormatInt(obj.GetGeneration(), 10) // manager generation (frozen under annotation reflection)
	if rev := obj.GetAnnotations()[RayClusterGenerationAnnotation]; rev != "" {
		extra += "_" + rev // reflected worker UID-generation, advances on every resize
	}
	return extra
}

// slice name = <kind>-<name>-sha1(Kind "\n" Group "\n" name "\n" UID "\n" extra)[:5]
```

### Manager-side replicas pinning

While the worker autoscaler owns the replicas, a **validating webhook** on the
manager cluster rejects manager-side edits to the pinned fields. Without it, the
forward sync ([#12885](https://github.com/kubernetes-sigs/kueue/pull/12885))
propagates a manager-side edit down to the worker copy, where it lands on the same
fields the autoscaler writes: two writers, each overwriting the other's value in an
endless fight loop. The webhook keeps the autoscaler as the single writer.

Pinned field paths:

- `RayCluster`: `spec.workerGroupSpecs[*].replicas`

### Worker-side resize tolerance

During a resize handover, the job's observed count **on the worker cluster** can
transiently differ from its admitted slice's count. Without tolerance, the **worker
cluster's** Kueue would finish that workload `OutOfSync` and tear the job down.
This KEP adds a resize tolerance in the jobframework that applies **on the worker
cluster** — scoped to the MultiKueue-dispatched copy via the origin label — so a
transient mismatch during handover is not treated as divergence.

**Scale-up.** KubeRay creates the new worker pods immediately, but each lands
behind the `kueue.x-k8s.io/elastic-job` scheduling gate; the worker's ungater
releases them — up to the granted count — only once the manager has admitted the
replacement slice. Until then the job's desired count exceeds the admitted
slice's: the tolerance absorbs that gap, and the gates keep it from becoming
scheduled pods beyond quota.

**Scale-down.** The two sides proceed independently: KubeRay makes sure the pods
the autoscaler lists in `workersToDelete` are eventually deleted, without
waiting for the workload's in-place update, and the reverse sync shrinks the
slice without waiting for those pods to terminate. If that eventual guarantee
proves insufficient, a future refinement could defer the workload update until
KubeRay has cleared `workersToDelete` — that is, until the listed pods are
actually gone.

### Triggering the reverse sync

A resize must wake the manager's workload reconcile. MultiKueue watches each
worker object and maps it back to a workload through its prebuilt-workload marker.

- **RayCluster** — the autoscaler edits the watched RayCluster itself, so the
  reconcile wakes directly.
- **RayJob** — the autoscaler edits the child RayCluster. The wake-up relies on
  KubeRay mirroring the child's status into `RayJob.status`.

As a possible follow-up, the child RayCluster event could wake the RayJob
reconcile directly, removing the dependency on KubeRay's status mirroring. This
is not a priority, since KubeRay always performs that status mirroring.

### Test Plan

[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes
necessary to implement this enhancement.

#### Prerequisite testing updates

None beyond the coverage described below.

#### e2e tests

- **Real-autoscaler e2e** (extended MultiKueue suite: manager + worker clusters,
  KubeRay operator on the workers). Two scenarios run on this same setup:

  - *Single-resize lifecycle*: a standalone RayCluster and a RayJob's child are each
    driven **up (2) → down (0) → up (1)**; in-place slice update on scale-down, fresh
    replacement on scale-up; worker RayCluster not torn down mid-handover; MultiKueue
    GC clean after deletion.
  - *Sequential scale-ups*: two consecutive scale-ups on the standalone RayCluster,
    each adding one worker pod (**0 → 1 → 2**). Ensures that while the manager has
    admitted only the first scale-up's slice, the worker schedules exactly one extra
    worker pod, not two — the second pod stays gated until the manager admits the
    second scale-up's slice.

### Graduation Criteria

The feature follows the standard Kueue maturity progression on its own
`MultiKueueRayInTreeAutoscaling` feature gate; it additionally requires
`ElasticJobsViaWorkloadSlices` and MultiKueue.

**Alpha**: Reverse elastic sync is implemented for RayCluster and RayJob behind the
`MultiKueueRayInTreeAutoscaling` gate (off by default), with basic functionality
covered by tests and accompanying documentation. While the gate is off, the
validating webhook keeps rejecting `enableInTreeAutoscaling` on a
MultiKueue-managed elastic Ray object, exactly as it does today.

**Beta**: Positive feedback from Alpha, broader test coverage, and any documented
follow-ups addressed.

**Stable (GA)**: The feature has spent at least one release cycle in beta with no
major outstanding bugs.

## Implementation History

- 2026-07-26: KEP drafted.
- 2026-07: Prototyped in
  [#13435](https://github.com/kubernetes-sigs/kueue/pull/13435) — reverse elastic
  sync and worker-side resize tolerance for RayCluster and RayJob.

## Drawbacks

- Adds a second sync direction to the Ray adapter, increasing the surface area of
  MultiKueue's elastic handling and the number of interleavings to reason about.
- Retaining `enableInTreeAutoscaling` on the remote copy makes the worker, rather
  than the manager, the source of truth for the worker replica count.

## Alternatives

- **Manager-driven only (status quo).** Simple and already shipped, but blocks
  every use case that needs the worker's Ray Autoscaler.
- **Reject `enableInTreeAutoscaling` for MultiKueue elastic Ray objects.**
  [#13244](https://github.com/kubernetes-sigs/kueue/pull/13244) takes this stance
  as an interim guard until this support exists; it prevents silent misbehavior
  but does not deliver worker-side autoscaling. Once this KEP lands, that
  rejection is lifted.
