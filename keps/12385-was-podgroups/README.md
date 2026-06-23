# KEP-12385: Integrate Kubernetes WAS PodGroups with Kueue plain Pod groups

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [Terminology](#terminology)
  - [Activation model](#activation-model)
  - [API changes](#api-changes)
  - [Object model and invariants](#object-model-and-invariants)
  - [Capability detection](#capability-detection)
  - [Pod defaulting](#pod-defaulting)
  - [PodGroup lifecycle and ownership](#podgroup-lifecycle-and-ownership)
  - [Admission and scheduling flow](#admission-and-scheduling-flow)
  - [External ownership](#external-ownership)
  - [Compatibility](#compatibility)
  - [Observability](#observability)
  - [Test Plan](#test-plan)
    - [Unit tests](#unit-tests)
    - [Integration tests](#integration-tests)
    - [e2e tests](#e2e-tests)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
- [Future Work](#future-work)
  - [Controller-managed workloads (LWS, StatefulSet, JobSet)](#controller-managed-workloads-lws-statefulset-jobset)
<!-- /toc -->

## Summary

Kueue currently supports gang admission for plain Pods through Kueue-specific
labels, annotations, scheduling gates, and PodSet composition logic.
Kubernetes Workload Aware Scheduling (WAS) introduces native runtime gang
primitives — `scheduling.k8s.io` `PodGroup` objects and
`pod.spec.schedulingGroup`.

This KEP adds an optional integration for **plain Pod groups only**.
Under a new feature gate (`WASPodGroups`), when the cluster serves the required
WAS APIs, Kueue will create a native `PodGroup` and set
`pod.spec.schedulingGroup.podGroupName` while continuing to compose its own
`kueue.x-k8s.io` `Workload` for queueing and quota admission.

This preserves Kueue as the outer queueing and quota-admission controller, and
delegates runtime all-or-nothing placement to kube-scheduler.

## Motivation

Kueue's plain-Pod gang support predates upstream WAS and uses Kueue-owned
metadata (`kueue.x-k8s.io/pod-group-name`, `pod-group-total-count`,
`role-hash`) plus custom PodSet composition logic for workload construction,
quota accounting, and replacement behavior.

Native Kubernetes WAS support is now available for runtime grouping and gang
placement. Kueue should integrate with WAS where it improves interoperability,
but Kueue's multi-PodSet workload model is not directly equivalent to a single
WAS `PodGroup`. Phase 1 therefore chooses **integration without replacement**.

### Goals

- Plain Pod groups can use native `PodGroup` objects for runtime gang scheduling.
- Kueue remains the outer queueing and quota-admission system.
- Existing Kueue pod-group metadata and workload composition are preserved.
- Clusters without WAS support fall back safely.
- Externally managed WAS state is not overwritten.

### Non-Goals

- Creating upstream `scheduling.k8s.io/Workload` objects.
- Replacing Kueue `Workload.spec.podSets`, `role-hash`, or PodSet construction.
- Deprecating current Kueue pod-group labels/annotations.
- Solving controller-native WAS integrations (LWS, JobSet, etc.).
- Making scheduler `PodGroup` status a prerequisite for Kueue admission.

## Proposal

When the `WASPodGroups` feature gate is enabled and the cluster supports native
`PodGroup` resources, Kueue will — for Pods that carry the opt-in annotation
`kueue.x-k8s.io/was-podgroup: "true"`:

1. Compose a Kueue `Workload` using the current plain-Pod logic.
2. Create one native `PodGroup` per plain Pod group.
3. Set `pod.spec.schedulingGroup.podGroupName` on admitted Pods at creation.
4. Use Kueue's scheduling gate as the outer quota-admission gate.
5. Remove the gate only after Kueue workload admission.
6. Let kube-scheduler enforce native gang placement.

Pods without the annotation retain existing behavior. If any other prerequisite
is unmet, Kueue falls back to existing behavior.

### User Stories

1. **Native gang enforcement**: A user submits a Kueue-managed plain Pod group.
   Kueue decides when the workload may start (queueing/quota); kube-scheduler
   enforces gang placement natively.

2. **Safe fallback**: On clusters without WAS support, the feature gate has no
   effect — existing label-based behavior is preserved.

3. **Controller coexistence**: Controllers that manage WAS state directly are
   not overwritten — Kueue backs off when `schedulingGroup` is already set.

### Risks and Mitigations

| Risk | Mitigation |
|------|-----------|
| Cluster API/version skew | Feature-gated + runtime capability detection; fallback on unsupported clusters |
| Immutable `schedulingGroup` field | Only set during Pod creation when capability is confirmed |
| Two-level admission confusion | Events and logs distinguish Kueue admission from scheduler gang placement |
| Ownership conflicts | Pre-existing `schedulingGroup` or non-Kueue-owned PodGroups are left untouched |

## Design Details

### Terminology

- **Kueue Workload**: `kueue.x-k8s.io/Workload` — Kueue's CRD for queueing, quota, admission.
- **Native PodGroup**: `scheduling.k8s.io/PodGroup` — upstream runtime grouping for kube-scheduler.
- **Plain Pod group**: Kueue's plain-Pod construct identified by pod-group metadata.

### Activation model

The feature is **active** only when all of:

1. `WASPodGroups` feature gate is enabled,
2. capability detection confirms native `PodGroup` API is served,
3. the Pod is managed by the plain-Pod integration path,
4. the Pod carries the opt-in annotation `kueue.x-k8s.io/was-podgroup: "true"`,
5. the Pod does not already declare externally managed `schedulingGroup`.

### API changes

One new feature gate: `WASPodGroups` (alpha, disabled by default).

One new annotation: `kueue.x-k8s.io/was-podgroup`. When set to `"true"` on a
Pod that belongs to a plain Pod group, Kueue will create a native
`scheduling.k8s.io/v1alpha2` `PodGroup` and default
`pod.spec.schedulingGroup.podGroupName`. This annotation is the opt-in
mechanism — Pods without it retain existing behavior even when the feature gate
is enabled.

No CRD schema changes, no dependency on `scheduling.k8s.io/Workload`.

### Object model and invariants

Each eligible plain Pod group maps to exactly one Kueue Workload (as today)
plus exactly one native PodGroup (with `minCount = pod-group-total-count`).

The implementation satisfies these invariants:

- **Quota admission owned by Kueue.** Kueue Workload admission gates Pod scheduling.
- **Immutable scheduler grouping.** `pod.spec.schedulingGroup.podGroupName` is
  set at Pod creation and never mutated.
- **One runtime gang per group.** All Pods in a plain Pod group reference the
  same native PodGroup.
- **No partial mode.** All Pods in a group use native PodGroup mode, or none do.
- **External ownership wins.** Pre-existing WAS state is never overwritten.
- **Fallback preserves current semantics.** When inactive, behavior is unchanged.

### Capability detection

At startup, both the webhook and controller determine whether the cluster serves
the `scheduling.k8s.io/v1alpha2` `PodGroup` resource via REST mapper discovery.
If detection fails, the feature falls back silently.

### Pod defaulting

When active, the mutating webhook sets `pod.spec.schedulingGroup.podGroupName`
(derived from the Kueue pod-group name) during Pod creation, preserving all
other existing defaults (managed label, scheduling gate, role hash, finalizer).

### PodGroup lifecycle and ownership

Kueue creates the native PodGroup during the `Run` phase (workload start),
owned by the Kueue Workload via owner reference. The PodGroup is labeled with
`app.kubernetes.io/managed-by: kueue` to distinguish Kueue-owned from external
PodGroups. Cleanup happens via owner reference garbage collection.

If a PodGroup already exists and is not Kueue-managed, Kueue reuses it without
taking ownership.

### Admission and scheduling flow

1. User creates Pods with Kueue pod-group metadata and the
   `kueue.x-k8s.io/was-podgroup: "true"` annotation.
2. Webhook defaults the Pods; if active and the annotation is present, sets
   `schedulingGroup.podGroupName`.
3. Kueue composes a Kueue Workload and queues it.
4. Kueue admits the Workload based on quota policy.
5. Kueue creates the native PodGroup and removes its scheduling gate.
6. kube-scheduler applies native gang placement.

Kueue admission and scheduler gang placement are separate stages. A workload
may be admitted by Kueue while kube-scheduler determines the gang cannot yet
be placed. Phase 1 keeps these stages independent and observable.

### External ownership

- If a Pod has `schedulingGroup` set before Kueue defaulting, Kueue preserves
  it unchanged.
- If a native PodGroup exists without the Kueue-managed label, Kueue reuses it
  but does not modify or take ownership.

### Compatibility

Phase 1 is purely additive. Kueue continues to use and require current
pod-group metadata. No deprecation is included. Future deprecation requires a
separate design with migration path.

### Observability

- Events: `NativePodGroupCreated`, `NativePodGroupReused`.
- Structured logs at V(3) for PodGroup creation and fallback decisions.
- Metrics and richer status propagation can follow in later phases.

### Test Plan

#### Unit tests

- Native PodGroup name derivation and object creation.
- Defaulting behavior for `schedulingGroup.podGroupName`.
- Opt-in annotation gating: PodGroup creation skipped when annotation is absent.
- External ownership detection and backoff.
- Feature gate registration.

#### Integration tests

- Native PodGroup created when feature is active and annotation is present.
- Pods receive `schedulingGroup.podGroupName` only with annotation.
- Pods without annotation do not get `schedulingGroup` defaulted.
- Scheduling gate retained until Kueue admission.
- Fallback when capability detection reports unsupported.
- Pre-set `schedulingGroup` is preserved.

#### e2e tests

- Plain Pod gang scheduling with native PodGroup mode.
- Fallback on clusters without native capability.
- Coexistence with externally managed scheduler grouping.

### Graduation Criteria

**Alpha**: Feature gate disabled by default. Creates native PodGroups for
eligible groups. Fallback works. Test coverage exists.

**Beta**: Capability detection stable across version skew. Observability
sufficient to debug two-level admission. Coexistence validated.

**GA**: Stable across supported Kubernetes skew matrix. Ownership model proven
robust.

## Implementation History

- 2026-06-23: Initial KEP outline drafted from issue #12385.

## Drawbacks

- Phase 1 adds complexity without simplifying Kueue's internal PodSet model.
- Users must reason about two admission layers (Kueue quota + scheduler gang).
- Capability detection adds cluster-environment dependencies to the plain-Pod path.

## Alternatives

The following alternatives were considered and rejected for phase 1:

- **Immediately replace Kueue pod-group metadata**: Too risky — Kueue's PodSet
  composition and quota accounting depend on current metadata.
- **Create upstream `scheduling.k8s.io/Workload` objects**: Introduces ambiguity
  with Kueue's own `Workload` CRD without solving multi-PodSet mapping.
- **Replace role-hash and PodSet construction**: Kueue still needs PodSet-based
  accounting for quota, reclaim, and replacement.
- **Make scheduler PodGroup status part of Kueue admission**: Couples Kueue to
  scheduler runtime state before observability and lifecycle are mature enough.

## Future Work

### Controller-managed workloads (LWS, StatefulSet, JobSet)

Phase 1 supports WAS PodGroups only for plain Pod groups. Extending support to
controller-managed workloads (LeaderWorkerSet, StatefulSet, JobSet) introduces
several open questions:

1. **Pod creation is indirect.** For LWS the ownership chain is
   LWS → StatefulSet → Pod. Kueue does not create the pods — the StatefulSet
   controller does. If `pod.spec.schedulingGroup` is immutable after creation,
   the field must be set in the pod template *before* the child controller
   creates the pod. Possible injection points include the parent webhook
   (LWS webhook) or the StatefulSet pod template at admission time, but each
   has trade-offs (PodGroup name may not be deterministic at webhook time;
   patching a StatefulSet pod template triggers a rolling update).

2. **PodGroup naming and cardinality.** A single LWS can have multiple replica
   groups, each with its own Kueue Workload. Each group would need its own
   native PodGroup with an appropriate `minCount`. The PodGroup name would need
   to be derived deterministically from the group index, matching the Workload
   name.

3. **PodGroup lifecycle.** LWS uses `JobReconcilerInterface` rather than
   `ComposableJob`, so it does not have a `Run()` method. PodGroup creation
   would need to happen in the controller's reconciler when a Workload is
   admitted, rather than in the generic job framework.

4. **Annotation propagation.** The opt-in annotation
   (`kueue.x-k8s.io/was-podgroup`) would need to be placed on the parent object
   (e.g., the LWS) and propagated to the pod template or recognised by the
   controller's reconciler. The per-pod annotation model used in Phase 1 does
   not naturally extend to controller-managed pods.

5. **Immutability of `schedulingGroup`.** Whether `pod.spec.schedulingGroup` is
   truly immutable after pod creation needs to be confirmed. If it is mutable,
   the LWS reconciler's `setDefault` path (which patches pods after creation)
   could be extended. If it is immutable, injection must happen at the pod
   template level before the child controller creates pods.

These questions are deferred to a future phase. The current opt-in annotation is
silently ignored on controller-managed pods because the pod webhook returns
early for pods with a Kueue-managed ancestor, and no controller-specific
reconciler checks for the annotation.
