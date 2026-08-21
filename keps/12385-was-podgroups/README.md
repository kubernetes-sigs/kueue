# KEP-12385: WAS PodGroups for Plain Pod Groups

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
  - [User Stories](#user-stories)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [Implementation](#implementation)
  - [Test Plan](#test-plan)
    - [Integration tests](#integration-tests)
    - [E2E tests](#e2e-tests)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
<!-- /toc -->

## Summary

Kueue admits plain Pod groups as a single Workload, but `kube-scheduler` places each Pod independently after ungating. This creates a gap where partially-admitted groups can have some Pods scheduled and others pending, weakening gang-scheduling semantics.

This KEP enables Kueue to create a native `scheduling.k8s.io/v1beta1` `PodGroup` object for opted-in plain Pod groups on Kubernetes 1.37+. The `PodGroup`'s `Gang.MinCount` enforces all-or-nothing placement at the scheduler level, and Kueue's priority is mapped to the native object for consistent preemption ordering.

**Note**: This feature requires Kubernetes 1.37 or later. Future Kubernetes releases will graduate these APIs to `v1`, at which point Kueue will create `v1` objects instead of `v1beta1`.

## Motivation

Kueue reserves quota for an entire plain Pod group before admission, but after ungating the scheduler can place a subset of Pods while others remain pending. This weakens gang-scheduling semantics and wastes reserved capacity.

Native `PodGroup` support closes this gap by enforcing atomic placement at the scheduler level.

### Goals

- Behind the `WASPodGroups` feature gate, create a `scheduling.k8s.io/v1beta1` `PodGroup` object for plain Pod groups with the `kueue.x-k8s.io/was-podgroup: "true"` annotation on Kubernetes 1.37+.
- Set `Gang.MinCount` to the group's total count for all-or-nothing placement.
- Map Kueue Workload priority to the native `PodGroup` object for consistent preemption.
- Own the created `PodGroup` from the Kueue Workload for garbage collection.
- Gracefully no-op when the native API is unavailable (Kubernetes < 1.37 or API disabled).
- Support future migration to `v1` APIs when Kubernetes graduates the API.

### Non-Goals

- Changing behavior for groups without the opt-in annotation.
- Creating native objects for non-Pod integrations (Job, JobSet, Ray, etc.).
- Full Workload-Aware Preemption (KEP-5710) integration beyond numeric priority.
- ResourceClaims for PodGroups will not be supported with this feature.

### User Stories

**As a batch user**, I want all Pods in my group placed together or not at all, preventing partial placement that wastes nodes.

**As a cluster operator**, I want opt-in per-workload control and graceful fallback when the native API is unavailable.

### Risks and Mitigations

- **Kubernetes version requirement**: Feature requires Kubernetes 1.37+ with the upstream `GenericWorkload` feature gate enabled on `kube-apiserver` (and `kube-scheduler`, for gang enforcement). Kueue detects API availability via REST mapper and no-ops if unavailable (Kubernetes < 1.37, `GenericWorkload` disabled, or API otherwise disabled), logging the reason.
- **API version migration**: When Kubernetes graduates to `v1`, Kueue will need to detect the available version and create objects accordingly. Implementation uses REST mapper discovery to handle version detection.
- **Alpha feature**: Ships with `WASPodGroups` gate off by default.

## Design Details

### Implementation

**Kubernetes version requirement**: Requires Kubernetes 1.37 or later for `scheduling.k8s.io/v1beta1` API availability. This API version is `v1beta1`-gated upstream: the `GenericWorkload` feature gate must be enabled on `kube-apiserver` to serve the `PodGroup` API, and on `kube-scheduler` for `Gang.MinCount` to be enforced at placement time. Both prerequisites are outside Kueue's control; Kueue only detects and reacts to their availability.

**Opt-in**: Annotation `kueue.x-k8s.io/was-podgroup: "true"` enables the feature per Pod group.

**Availability detection**: Enabled only when `WASPodGroups` gate is on and REST mapper resolves `scheduling.k8s.io/v1beta1` `PodGroup`. This implicitly requires the upstream `GenericWorkload` gate to be on, since the API is not served otherwise; Kueue does not probe the gate directly and instead relies on API discovery. Future Kueue versions will detect and prefer `v1` when available.

**Webhook defaulting**: Sets `pod.spec.schedulingGroup.podGroupName` to the Kueue pod-group name for opted-in groups.

**Object creation**: Before ungating, `Pod.Run` ensures a native `PodGroup` (v1beta1) exists with an inline `schedulingPolicy.gang.minCount` set to the group size — no native `Workload`/`PodGroupTemplate` is created, since `kube-scheduler` does not consume it.

The `PodGroup` uses a controller reference to the Kueue Workload for garbage collection. A pre-existing non-Kueue `PodGroup` is reused (emitting an event); an ownership conflict returns an error.

**Priority mapping**: Kueue Workload's numeric priority (`.spec.priority`) maps to `PodGroup.spec.priority`. Only numeric values are projected.

**Validation**: Rejects groups with inconsistent `schedulingGroup.podGroupName` values across Pods.

This validation enforces that users are not setting `schedulingGroup` themselves.
This would fall under the case of the user bringing their own podgroup which is not supported for this feature.

### Test Plan

- **Unit**: Availability detection, create/reuse/conflict paths, priority mapping, validation, API version handling (`pkg/controller/jobs/pod/was_test.go`, `pod_controller_test.go`, `pod_webhook` tests).

#### Integration tests

Integration tests run against `envtest`, so the upstream `scheduling.k8s.io/v1beta1`
`PodGroup` type must be registered with the test API server's scheme and
CRDs/aggregated API installed for the `WASPodGroups`-gate-on scenarios below; the
gate-off and API-unavailable scenarios are what exercise the no-native-API path.

- **Webhook defaulting**:
  - A Pod group with `kueue.x-k8s.io/was-podgroup: "true"` gets `pod.spec.schedulingGroup.podGroupName` defaulted to the Kueue pod-group name on every Pod in the group.
  - A Pod group without the annotation is left untouched — no `schedulingGroup` set.
  - Defaulting is skipped entirely when `WASPodGroups` is disabled, even if the annotation is present.
- **Validation**:
  - Pods in the same group with inconsistent `schedulingGroup.podGroupName` values are rejected by the webhook.
  - A user manually setting `schedulingGroup.podGroupName` to a value inconsistent with the Kueue-assigned group name is rejected ("bring your own PodGroup" is unsupported).
- **Object creation on admission** (gate on, opted-in group):
  - Before ungating, a native `PodGroup` is created with an inline `schedulingPolicy.gang.minCount` == group size.
  - The `PodGroup` carries a controller `ownerReference` back to the Kueue Workload.
  - `PodGroup.spec.priority` equals the Kueue Workload's `.spec.priority`.
  - A non-numeric/unset priority source results in no priority being projected (rather than erroring).
- **Reuse and conflict paths**:
  - A pre-existing native `PodGroup` not owned by Kueue is reused, and an Event is emitted noting the reuse.
  - A pre-existing native `PodGroup` owned by a different controller's `ownerReference` is treated as a conflict and surfaces an error/condition on the Kueue Workload rather than being silently adopted or overwritten.
- **Garbage collection**:
  - Deleting the Kueue Workload cascades deletion of the owned native `PodGroup` via `ownerReference` (verified through `envtest`'s garbage collector or by asserting deletion timestamps/foreground-deletion behavior, since GC itself is a cluster-level component).
- **Gate-off / non-opted-in behavior**:
  - `WASPodGroups` disabled: no native objects are created and no webhook defaulting occurs, regardless of the annotation.
  - `WASPodGroups` enabled but the group lacks the opt-in annotation: behavior is unchanged from today (no native objects, no defaulting) — regression coverage for the [Non-Goal](#non-goals) of not affecting non-opted-in groups.
- **API availability detection**:
  - REST mapper resolves `scheduling.k8s.io/v1beta1` `PodGroup`: feature is enabled end-to-end.
  - REST mapper cannot resolve the type (simulating Kubernetes < 1.37 or the upstream `GenericWorkload` gate being off): Kueue no-ops gracefully — no native object is created, no error surfaced on the Workload, and the reason is logged.

#### E2E tests

Run on a Kubernetes 1.37+ `kind` cluster built from `main` with the upstream
`GenericWorkload` feature gate enabled (see the `was-cluster` skill), covering
behavior that only a real `kube-scheduler` and garbage collector can validate
(`test/e2e/singlecluster/was/was_pod_group_test.go`):

- **Gang admission — happy path**: An opted-in Pod group where the cluster has enough capacity for all Pods is admitted and every Pod is scheduled together; the native `PodGroup` object exists with the expected `Gang.MinCount` and `ownerReference` back to the Kueue Workload.
- **Gang admission — partial capacity**: An opted-in Pod group is admitted by Kueue (quota reserved) but the cluster only has capacity for a subset of Pods. Assert `kube-scheduler` does *not* place any Pod in the group (no partial placement) until capacity for the full group becomes available, at which point all Pods schedule together. This is the core regression test for the gap described in [Motivation](#motivation).
- **Priority-ordered preemption**: Two opted-in Pod groups at different Kueue priorities compete for the same limited capacity. Verify the native objects' mapped priority causes `kube-scheduler` to prefer the higher-priority group, consistent with Kueue's own admission ordering.
- **Non-opted-in groups unaffected**: A Pod group without the annotation, run alongside opted-in groups on the same cluster, retains today's behavior (independent per-Pod placement, no native objects) — confirms the two code paths don't interfere.
- **Garbage collection**: Deleting the parent Job (or the Kueue Workload directly) results in the native `PodGroup` object being garbage-collected by the cluster within a bounded timeout.

### Graduation Criteria

**Alpha**:
- Feature behind `WASPodGroups` gate (default: false).
- Native `v1beta1` object creation with priority mapping on Kubernetes 1.37+.
- Unit, integration, and e2e coverage.
- Graceful fallback when the native API is unavailable (Kubernetes < 1.37, or `GenericWorkload`/the API disabled).

**Beta** (future):
- Support for both `v1beta1` and `v1` APIs with automatic version detection.
- Full KEP-5710 integration (`WorkloadPriorityClass` → native `priorityClassName`).
- Default-on based on upstream API graduation to `v1`.

## Implementation History

- 2026-07-17: Initial KEP and alpha implementation (`WASPodGroups` gate, v0.20).
