# KEP-12385: Native Workloads and PodGroups for Plain Pod Groups (Workload-Aware Scheduling)

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
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [Opt-in annotation](#opt-in-annotation)
  - [Availability detection](#availability-detection)
  - [Webhook defaulting](#webhook-defaulting)
  - [Workload and PodGroup creation on Run](#workload-and-podgroup-creation-on-run)
  - [Priority mapping](#priority-mapping)
  - [Validation](#validation)
  - [Events](#events)
  - [Test Plan](#test-plan)
    - [Unit tests](#unit-tests)
    - [Integration tests](#integration-tests)
    - [e2e tests](#e2e-tests)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
<!-- /toc -->

## Summary

Kueue admits plain Pod groups (multiple Pods sharing the
`kueue.x-k8s.io/pod-group-name` label) as a single Workload, but once the Pods
are ungated the `kube-scheduler` places each Pod independently. There is no
gang-scheduling guarantee at the scheduler level, so a partially-admitted group
can end up with some Pods scheduled and others pending.

Kubernetes is introducing native `scheduling.k8s.io` (`v1alpha3`) objects for
workload-aware scheduling: a `Workload`, which describes a workload as a set of
`PodGroupTemplates` (each with a scheduling policy and priority), and a
`PodGroup`, the runtime instance the scheduler acts on. A `PodGroup` links back
to the template it was created from via `spec.podGroupTemplateRef.workload`, and
Pods join a `PodGroup` through `pod.spec.schedulingGroup.podGroupName`.

This KEP proposes that Kueue, behind the alpha `WASPodGroups` feature gate,
project an opted-in plain Pod group onto a native `Workload` holding a single
`PodGroupTemplate`, create the corresponding `PodGroup` from that template, and
default `pod.spec.schedulingGroup.podGroupName` on the group's Pods. The
`PodGroup`'s `Gang.MinCount` mirrors Kueue's admitted group size so the scheduler
enforces all-or-nothing placement, and the Kueue Workload's resolved priority is
mapped onto the `PodGroupTemplate` and `PodGroup` so the scheduler shares Kueue's
view of relative importance.

## Motivation

Kueue already computes the total size of a plain Pod group and reserves quota
for the whole group before admitting it. However, after ungating, nothing
prevents the scheduler from placing a subset of the group's Pods while the rest
stay `Pending` (for example, waiting on autoscaling). This weakens the
all-or-nothing semantics users expect from gang-scheduled batch workloads and
can waste reserved capacity.

Native `PodGroup` support in the scheduler closes this gap. By projecting the
Kueue group's total count onto a `PodGroup` with a matching `Gang.MinCount`,
Kueue can hand the scheduler the information it needs to keep the group's
placement atomic, without Kueue having to re-implement gang scheduling itself.

### Goals

- Behind the `WASPodGroups` feature gate, create a native
  `scheduling.k8s.io/v1alpha3` `Workload` (holding one `PodGroupTemplate`) and a
  corresponding `PodGroup` for plain Pod groups that opt in via the
  `kueue.x-k8s.io/was-podgroup: "true"` annotation.
- Default `pod.spec.schedulingGroup.podGroupName` on opted-in group Pods to the
  Kueue pod-group name so the scheduler associates them with the `PodGroup`.
- Set the `PodGroupTemplate`'s and `PodGroup`'s `Gang.MinCount` to the group's
  total count so the scheduler enforces gang placement, and link the `PodGroup`
  back to the `Workload` via `spec.podGroupTemplateRef.workload`.
- Map the Kueue `Workload`'s resolved priority onto the `PodGroupTemplate` and
  the `PodGroup` so the scheduler's preemption ordering matches Kueue's.
- Own both created objects from the Kueue `Workload` so they are
  garbage-collected with it.
- Gracefully no-op when the `scheduling.k8s.io/v1alpha3` `Workload`/`PodGroup`
  API is not served by the cluster, even if the gate is on.

### Non-Goals

- Changing the behavior of plain Pod groups that do not opt in — existing
  behavior is unchanged when the annotation is absent or the gate is off.
- Creating native `Workload`s/`PodGroup`s for non-Pod integrations (Job, JobSet,
  Ray, etc.).
- Fully wiring up upstream Workload-Aware Preemption (KEP-5710) beyond projecting
  the numeric priority; mapping Kueue's `WorkloadPriorityClass` onto a native
  `priorityClassName` is out of scope (only the resolved numeric value is
  projected).
- Managing a `Workload` or `PodGroup` that already exists and is not owned by
  Kueue — such an object is reused as-is, not mutated.

## Proposal

When a plain Pod group opts in via annotation and the cluster serves the native
`PodGroup` API, Kueue:

1. Gates the group's Pods (existing behavior) until the Workload is admitted.
2. Defaults `pod.spec.schedulingGroup.podGroupName` on each Pod at admission
   webhook time.
3. Creates a `scheduling.k8s.io/v1alpha3` `Workload` named after the pod-group
   name, holding one `PodGroupTemplate` with `Gang.MinCount` equal to the group
   total count and the mapped priority, owned by the Kueue Workload, just before
   ungating the Pods.
4. Creates the `PodGroup` (also named after the pod-group name) from that
   template — copying the scheduling policy and priority and setting
   `spec.podGroupTemplateRef.workload` — owned by the Kueue Workload.
5. Lets `kube-scheduler` use that `PodGroup` to enforce gang placement.

### User Stories

#### Story 1

As a batch user submitting a group of plain Pods, I want the scheduler to place
all of my Pods together or none at all, so a partially-placed group does not sit
holding nodes while the rest of the group waits.

#### Story 2

As a cluster operator, I want this behavior to be opt-in per workload and gated,
so I can roll it out only on clusters that serve the native `PodGroup` API and
only for workloads that request it.

### Notes/Constraints/Caveats

- The feature requires the cluster to serve the `scheduling.k8s.io/v1alpha3`
  `Workload` and `PodGroup` kinds. Kueue detects both at controller and webhook
  setup via the REST mapper; if either is unavailable, the feature is a no-op
  regardless of the gate.
- All Pods in a group must resolve to the same
  `schedulingGroup.podGroupName`. Kueue defaults this consistently, but the group
  validation also rejects a group whose members disagree.

### Risks and Mitigations

- **API not present at runtime.** If the gate is on but the API is not served,
  Kueue logs the reason and disables the behavior rather than failing Pod
  admission.
- **Conflicting pre-existing `Workload`/`PodGroup`.** If an object with the
  target name already exists and is not Kueue-managed, Kueue reuses it and emits
  an event rather than overwriting it. If it is Kueue-managed but owned by a
  different Workload, Kueue returns an error rather than hijacking it. This
  applies independently to the native `Workload` and the `PodGroup`.
- **Alpha, off by default.** The gate ships `Alpha` and `Default: false`, so the
  new code path is inert unless explicitly enabled.

## Design Details

### Opt-in annotation

A new constant `WASPodGroupAnnotation = "kueue.x-k8s.io/was-podgroup"` gates the
behavior per Pod group. Kueue only acts when the annotation value is `"true"`.

### Availability detection

`nativePodGroupsAvailability(restMapper)` returns enabled only when the
`WASPodGroups` gate is on and the REST mapper can resolve both the
`scheduling.k8s.io/v1alpha3` `Workload` and `PodGroup` kinds. Both the Pod
webhook and the Pod reconciler compute this once at setup and store the result.

### Webhook defaulting

In `PodWebhook.Default`, for an opted-in group Pod, Kueue sets
`pod.spec.schedulingGroup.podGroupName` to the Kueue pod-group name
(`utilpod.GetPodGroupName`) when it is not already set.

### Workload and PodGroup creation on Run

In `Pod.Run`, before ungating, `ensureNativePodGroup` no-ops unless the feature
is enabled, the Pod is part of a group, the group opted in, and
`schedulingGroup.podGroupName` is set. When it proceeds it ensures two objects,
in order:

1. `ensureNativeWorkload` gets the target `Workload`; if present, it reuses a
   non-Kueue-managed one (event `NativeWorkloadReused`), verifies ownership of a
   Kueue-managed one, else returns an error. Otherwise it creates a `Workload`
   with the Kueue managed-by label, a single `PodGroupTemplate` named `pods`
   (`Gang.MinCount = groupTotalCount`, mapped priority), and a controller
   reference to the Kueue Workload (event `NativeWorkloadCreated`).
2. The `PodGroup` step behaves the same way (events `NativePodGroupReused` /
   `NativePodGroupCreated`), creating a `PodGroup` with
   `Gang.MinCount = groupTotalCount`, the mapped priority, and
   `spec.podGroupTemplateRef.workload` pointing at the native `Workload` and its
   `pods` template.

Both objects are named after the pod-group name and carry a controller reference
to the Kueue `Workload`, so they are garbage-collected together. The controller
reference (rather than a plain owner reference) also lets the reuse path
correctly recognize objects it created earlier via `IsControlledBy`.

### Priority mapping

`nativeWASPriority(wl)` maps the Kueue `Workload`'s resolved numeric priority
(`.spec.priority`) onto the native `PodGroupTemplate.priority` and
`PodGroup.spec.priority`. When the Kueue Workload has no priority set, both are
left unset (priority `0`). Only the numeric value is projected; Kueue
`WorkloadPriorityClass` names are not translated into a native
`priorityClassName`.

### Validation

`validatePodGroupMetadata` additionally rejects a group whose members carry
inconsistent `schedulingGroup.podGroupName` values, as an unretryable error.

### Events

Four Normal events are recorded on the Kueue Workload:
`NativeWorkloadCreated`, `NativeWorkloadReused`, `NativePodGroupCreated`, and
`NativePodGroupReused`.

### Test Plan

#### Unit tests

- `pkg/controller/jobs/pod/was_test.go` covers availability detection (now
  requiring both the `Workload` and `PodGroup` kinds), opt-in/defaulting
  predicates, `ensureNativeWorkload`/`ensureNativePodGroup` create/reuse/conflict
  paths, the `Workload`↔`PodGroup` linkage, and priority mapping.
- `pkg/controller/jobs/pod/pod_controller_test.go` and `pod_webhook` tests cover
  defaulting and group validation.

#### Integration tests

- Pod controller integration tests covering defaulting and gate-off behavior for
  opted-in and non-opted-in groups.

#### e2e tests

- `test/e2e/was/baseline/was_pod_group_test.go` exercises the end-to-end flow on
  a kind cluster built from Kubernetes `main` with the native `Workload`/
  `PodGroup` API and the `WASPodGroups` gate enabled (see the `was-cluster`
  skill), including verifying the native `Workload`, the `PodGroup`'s
  `podGroupTemplateRef` link, and priority mapping from a
  `WorkloadPriorityClass`.

### Graduation Criteria

Alpha:

- Feature implemented behind `WASPodGroups`, `Default: false`.
- Native `Workload` + `PodGroup` creation with priority mapping.
- Unit, integration, and e2e coverage as above.
- No-op fallback when the native API is unavailable.

Beta (future):

- Full Workload-Aware Preemption (KEP-5710) integration, including
  `WorkloadPriorityClass` → native `priorityClassName` translation.
- Extend beyond plain Pods if warranted.
- Default-on decision based on upstream API graduation.

## Implementation History

- 2026-07-17: Initial KEP drafted alongside the alpha implementation
  (`WASPodGroups` gate, v0.20).
- 2026-07-17: Extended to also create a native `Workload` (with a
  `PodGroupTemplate` the `PodGroup` references) and to map the Kueue Workload's
  priority onto the template and `PodGroup`.

## Drawbacks

The feature depends on an alpha upstream API (`scheduling.k8s.io/v1alpha3`) that
is not yet broadly available, so it is only useful on clusters that opt into
that API. It also currently applies only to plain Pod groups, which limits its
immediate reach.

## Alternatives

- **Do nothing / rely on Kueue admission only.** Keeps the current gap where the
  scheduler can partially place a group after ungating.
- **Reimplement gang scheduling in Kueue.** Duplicates scheduler responsibility
  and is far more complex than projecting the group onto a native `PodGroup`.
- **Create only a `PodGroup`, no `Workload`.** Simpler, but the `Workload` is the
  object upstream WAS uses to carry per-template scheduling policy and priority;
  creating it lets the `PodGroup` reference a real template via
  `podGroupTemplateRef` and gives priority a natural home for future
  preemption work (KEP-5710).
- **Always create the objects (no opt-in).** Rejected for alpha to keep the
  blast radius small and let users adopt per workload.
