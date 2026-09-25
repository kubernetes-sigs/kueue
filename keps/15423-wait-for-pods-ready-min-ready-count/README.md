# KEP-15423: WaitForPodsReady Minimum Ready Count

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories (Optional)](#user-stories-optional)
    - [Story 1](#story-1)
  - [Notes/Constraints/Caveats (Optional)](#notesconstraintscaveats-optional)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [API](#api)
  - [Pod Controller](#pod-controller)
  - [Validation](#validation)
  - [Future Work](#future-work)
  - [Test Plan](#test-plan)
    - [Prerequisite testing updates](#prerequisite-testing-updates)
    - [Unit tests](#unit-tests)
    - [Integration tests](#integration-tests)
    - [e2e tests](#e2e-tests)
  - [Graduation Criteria](#graduation-criteria)
    - [Alpha](#alpha)
    - [Beta](#beta)
    - [Stable](#stable)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
<!-- /toc -->

## Summary

This KEP introduces the `kueue.x-k8s.io/pod-group-min-ready-count` Pod annotation, guarded
by the alpha `WaitForPodsReadyMinReadyCount` feature gate (disabled by default). It sets
the minimum number of ready Pods required for a Pod group to satisfy the Workload
`PodsReady` condition, relaxing `waitForPodsReady.timeout` and
`waitForPodsReady.recoveryTimeout` while keeping admission and quota reservation at full
`kueue.x-k8s.io/pod-group-total-count` size.

## Motivation

`waitForPodsReady` ([KEP-349](../349-all-or-nothing/README.md)) requires all
`kueue.x-k8s.io/pod-group-total-count` Pods of a Pod group to become ready within
`timeout` (and recover within `recoveryTimeout`), otherwise evicting and requeuing the
entire Workload.

For large workloads that tolerate a few unavailable Pods, evicting the entire Workload is
far more disruptive than running slightly below capacity while those Pods recover.

Unlike partial admission (`podSets[].minCount`,
[KEP-420](../420-partial-admission/README.md)), which shrinks a Workload at admission
time, this feature admits the Workload at full size and only relaxes the `PodsReady`
threshold.

### Goals

- Allow configuring, per Pod group, the minimum number of ready Pods required to satisfy
  the Workload `PodsReady` condition for both `timeout` and `recoveryTimeout`.
- Support plain Pod groups ([KEP-976](../976-plain-pods/README.md)).
- Preserve all-or-nothing behavior when the feature gate is disabled or the annotation is
  absent or invalid.

### Non-Goals

- Changing admission, quota reservation, or partial admission (`podSets[].minCount`).
- Role-aware thresholds (for example requiring a specific leader Pod).
- Propagating the annotation from `StatefulSet` or `LeaderWorkerSet` objects to their Pods
  in Alpha (see [Future Work](#future-work)).
- Supporting non-Pod-group integrations (`batch/v1` Job, JobSet, Kubeflow Jobs, RayJob) or
  MultiKueue in Alpha.
- Introducing a Workload API field in Alpha (see [Alternatives](#alternatives)).

## Proposal

Introduce the `kueue.x-k8s.io/pod-group-min-ready-count` annotation on the Pods of a Pod
group, honored when the `WaitForPodsReadyMinReadyCount` feature gate is enabled.

When every Pod in the group carries a valid integer `N` in `[1, pod-group-total-count]`,
the Pod group satisfies `PodsReady` once at least `N` of its Pods are ready. Otherwise, all
`pod-group-total-count` Pods must be ready.

The existing `waitForPodsReady` machinery (`timeout`, `recoveryTimeout`, `blockAdmission`,
`requeuingStrategy`, and per-Workload timeouts from
[KEP-4803](../4803-workload-level-wait-for-pods-ready/README.md)) applies unchanged on top
of the resulting `PodsReady` condition.

### User Stories (Optional)

#### Story 1

As a user running a 200-replica `StatefulSet` (or `LeaderWorkerSet`) backed by Kueue's Pod
group integration, I want the Workload to satisfy `PodsReady` once at least 195 Pods are
ready, so that a few delayed or replacement Pods do not trigger `timeout` or
`recoveryTimeout` eviction. I set the minimum ready count directly in the Pod template:

```yaml
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: trainer
  labels:
    kueue.x-k8s.io/queue-name: user-queue
spec:
  replicas: 200
  template:
    metadata:
      annotations:
        kueue.x-k8s.io/pod-group-min-ready-count: "195"
    spec:
      # ...
```

### Notes/Constraints/Caveats (Optional)

- **Single-Pod readiness is unchanged.** A Pod counts as ready when its `Ready` condition
  is `True` or, for non-serving Pod groups with `PodIntegrationCountSucceededPodsAsReady`
  enabled, when it has succeeded.
- **Flat count & strictest value.** Readiness is a flat count across the group, governed by
  the highest threshold among its Pods; any Pod with a missing or invalid annotation
  defaults to `pod-group-total-count`.
- **StatefulSet and LeaderWorkerSet.** Because these integrations use Pod groups under the
  hood, the annotation works when set in their Pod template(s) (for `LeaderWorkerSet`, in
  both the leader and worker templates). Kueue does not propagate it from the parent object
  in Alpha.

### Risks and Mitigations

- **Weakened all-or-nothing guarantee:** Unready Pods above the threshold still hold quota
  while `PodsReady=True` unblocks subsequent admissions and disarms timeouts.
  *Mitigation:* Opt-in per Pod group; cluster admins can keep the
  `WaitForPodsReadyMinReadyCount` feature gate disabled or restrict the annotation via a
  ValidatingAdmissionPolicy.
- **Silent fallback in Alpha:** Invalid values fall back to `pod-group-total-count`, and
  the annotation is ignored on unsupported integrations.
  *Mitigation:* Documented in the annotation reference; admission-time validation/warnings
  are a Beta graduation criterion (see [Validation](#validation)).

## Design Details

### API

A new alpha feature gate (disabled by default in v0.20):

```go
WaitForPodsReadyMinReadyCount featuregate.Feature = "WaitForPodsReadyMinReadyCount"
```

And a new Pod annotation constant:

```go
GroupPodsReadyMinCountAnnotation = "kueue.x-k8s.io/pod-group-min-ready-count"
```

The annotation value is valid when it is an integer `N` with
`1 <= N <= pod-group-total-count`.

### Pod Controller

The Pod integration computes `PodsReady` for a Pod group as follows:

```text
threshold(pod) = N           if the pod's annotation is a valid value N
               = totalCount  otherwise (annotation missing or invalid)

required       = totalCount                           if the feature gate is disabled
               = max(threshold(pod) for pod in group) otherwise

PodsReady      = count(pod in group : readyOrSucceeded(pod)) >= required
```

Taking the maximum across the group makes evaluation deterministic during in-place
annotation updates (`kubectl annotate pods ... --overwrite`) and fail-safe if values
diverge: lowering the threshold takes effect once all Pods carry the new value, whereas
raising it takes effect as soon as any Pod is updated.

Disabling the feature gate causes Kueue to ignore the annotation and require `totalCount`
ready Pods.

### Validation

In Alpha, no webhook validation is added; invalid values safely fall back to
`pod-group-total-count`.

For Beta, admission-time validation or warnings can be added for:
- malformed or out-of-range values on Pod group Pods,
- annotations placed on unsupported job integrations or their Pod templates.

### Future Work

- **Parent-object propagation:** Propagate the annotation from `StatefulSet` and
  `LeaderWorkerSet` objects to their Pods so the threshold can be updated without a Pod
  template rollout.
- **Other integrations:** Support non-Pod-group integrations once ready Pod tracking is
  available ([kubernetes-sigs/kueue#15404](https://github.com/kubernetes-sigs/kueue/issues/15404)).
- **Further improvements:** Based on user feedback, future versions may consider percentage
  thresholds (similar to `PodDisruptionBudget` `minAvailable`), role-aware thresholds,
  surfacing ready and required Pod counts in Workload status, and MultiKueue support.

### Test Plan

[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.

#### Prerequisite testing updates

None.

#### Unit tests

- `pkg/controller/jobs/pod`: Test `PodsReady` with `WaitForPodsReadyMinReadyCount` enabled
  and disabled, covering valid thresholds, missing or invalid annotations, divergent values
  across Pods in a group, and `PodIntegrationCountSucceededPodsAsReady`.

#### Integration tests

- Verify Pod group `PodsReady` transitions and `recoveryTimeout` eviction when
  `kueue.x-k8s.io/pod-group-min-ready-count` is configured and updated.
- Verify interaction with per-Workload `kueue.x-k8s.io/wait-for-pods-ready`
  ([KEP-4803](../4803-workload-level-wait-for-pods-ready/README.md)).

#### e2e tests

- Verify that a Pod group with `kueue.x-k8s.io/pod-group-min-ready-count` stays ready when
  a Pod is deleted above the threshold and is evicted on `recoveryTimeout` when ready Pods
  drop below the threshold.

### Graduation Criteria

#### Alpha

- `WaitForPodsReadyMinReadyCount` feature gate introduced (disabled by default).
- `kueue.x-k8s.io/pod-group-min-ready-count` honored for plain Pod groups.
- Unit, integration, and e2e tests added; annotation reference updated.

#### Beta

- Feature gate enabled by default.
- Admission-time validation or warnings for invalid values and unsupported integrations.
- Re-evaluate replacing the annotation with a Workload API field.

#### Stable

- Feature gate locked to enabled.

## Implementation History

- 2026-09-23: Initially proposed as an extension of
  [KEP-349](../349-all-or-nothing/README.md) in
  [kubernetes-sigs/kueue#16059](https://github.com/kubernetes-sigs/kueue/pull/16059).
- 2026-09-25: Moved to a dedicated KEP.

## Drawbacks

- Weakens the all-or-nothing guarantee of `waitForPodsReady` for Pod groups that opt in.
- Adds an annotation to the Pod group API surface, limited to Pod-group-based integrations
  in Alpha.

## Alternatives

- **Reusing `podSets[].minCount` ([KEP-420](../420-partial-admission/README.md)):**
  `minCount` resizes a Workload and reduces its quota at admission time, whereas this
  feature keeps the full admission size and only relaxes `PodsReady`. The annotation is
  named `min-ready-count` (rather than `min-count` or `threshold`) to avoid ambiguity.
- **A Workload API field:** A per-PodSet field (for example `spec.podSets[].minReadyCount`)
  requires all integrations to report ready Pods per PodSet
  ([kubernetes-sigs/kueue#15404](https://github.com/kubernetes-sigs/kueue/issues/15404)).
  Starting with a Pod group annotation keeps the Alpha API footprint small.
- **Cluster-level configuration:** Tolerating missing Pods is workload-specific; a global
  ratio would weaken all-or-nothing guarantees for workloads that require every Pod.
- **Reading the annotation from a single Pod:** Inspecting only the reconciling Pod would
  make readiness order-dependent during updates and could mark an incomplete group ready if
  partially applied. Taking the group maximum is deterministic and fail-safe.
