# KEP-15423: WaitForPodsReady Maximum Unavailable Pods

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories (Optional)](#user-stories-optional)
    - [Story 1](#story-1)
    - [Story 2](#story-2)
  - [Notes/Constraints/Caveats (Optional)](#notesconstraintscaveats-optional)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [API](#api)
  - [Pod Controller](#pod-controller)
  - [Validation](#validation)
  - [Future work ideas](#future-work-ideas)
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

This KEP introduces the `kueue.x-k8s.io/pod-group-max-unavailable-count` Pod annotation,
guarded by the alpha `WaitForPodsReadyMaxUnavailable` feature gate (disabled by default).
It sets the maximum number of unavailable Pods tolerated for a Pod group to satisfy the
Workload `PodsReady` condition, relaxing `waitForPodsReady.timeout` and
`waitForPodsReady.recoveryTimeout` while keeping admission and quota reservation at full
`kueue.x-k8s.io/pod-group-total-count` size.

## Motivation

`waitForPodsReady` ([KEP-349](../349-all-or-nothing/README.md)) requires all
`kueue.x-k8s.io/pod-group-total-count` Pods of a Pod group to become ready within
`timeout` (and recover within `recoveryTimeout`), otherwise evicting and requeuing the
entire Workload.

For large workloads that tolerate a few unavailable Pods, evicting the entire Workload is
far more disruptive than running slightly below capacity while those Pods recover.

Expressing this tolerance as a maximum number of unavailable Pods aligns with standard
Kubernetes disruption and rollout primitives (`PodDisruptionBudget`, `StatefulSet`,
`Deployment`) and remains valid when a `StatefulSet` or `LeaderWorkerSet` is scaled
(provided the total Pod count stays above the configured limit) without modifying the Pod
template.

Unlike partial admission (`podSets[].minCount`,
[KEP-420](../420-partial-admission/README.md)), which shrinks a Workload at admission
time, this feature admits the Workload at full size and only relaxes the `PodsReady`
threshold.

### Goals

- Allow configuring, per Pod group, the maximum number of unavailable Pods tolerated when
  satisfying the Workload `PodsReady` condition for both `timeout` and `recoveryTimeout`.
- Support plain Pod groups ([KEP-976](../976-plain-pods/README.md)).
- Preserve all-or-nothing behavior (`0` unavailable Pods tolerated) when the feature gate
  is disabled or the annotation is absent or invalid.

### Non-Goals

- Changing admission, quota reservation, or partial admission (`podSets[].minCount`).
- Role-aware thresholds (for example requiring a specific leader Pod).
- Propagating the annotation from `StatefulSet` or `LeaderWorkerSet` objects to their Pods
  in Alpha (see [Future work ideas](#future-work-ideas)).
- Supporting non-Pod-group integrations (`batch/v1` Job, JobSet, Kubeflow Jobs, RayJob) or
  MultiKueue in Alpha.
- Introducing a Workload API field in Alpha (see [Alternatives](#alternatives)).

## Proposal

Introduce the `kueue.x-k8s.io/pod-group-max-unavailable-count` annotation on the Pods of a
Pod group, honored when the `WaitForPodsReadyMaxUnavailable` feature gate is enabled.

When every Pod in the group carries a valid integer `M` in `[0, pod-group-total-count - 1]`,
the Pod group tolerates up to `M` unavailable Pods (satisfying `PodsReady` once at least
`pod-group-total-count - M` of its Pods are ready). Otherwise, `0` unavailable Pods are
tolerated and all `pod-group-total-count` Pods must be ready.

The existing `waitForPodsReady` machinery (`timeout`, `recoveryTimeout`, `blockAdmission`,
`requeuingStrategy`, and per-Workload timeouts from
[KEP-4803](../4803-workload-level-wait-for-pods-ready/README.md)) applies unchanged on top
of the resulting `PodsReady` condition.

### User Stories (Optional)

#### Story 1

As a user running a 200-replica `StatefulSet` (or `LeaderWorkerSet`) backed by Kueue's Pod
group integration, I want the Workload to tolerate up to 5 unavailable Pods when evaluating
`PodsReady` (requiring at least 195 ready Pods), so that a few delayed or replacement Pods
do not trigger `timeout` or `recoveryTimeout` eviction. I set the maximum unavailable count
directly in the Pod template, which remains valid if `replicas` is scaled (with
`replicas > 5`) without editing the Pod template:

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
        kueue.x-k8s.io/pod-group-max-unavailable-count: "5"
    spec:
      # ...
```

#### Story 2

As a user or custom controller managing a plain Pod group directly (without `StatefulSet`
or `LeaderWorkerSet`), I want the Workload to tolerate up to 5 unavailable Pods out of 200
when evaluating `PodsReady`, so that a few delayed or replacement Pods do not trigger
`timeout` or `recoveryTimeout` eviction. I set
`kueue.x-k8s.io/pod-group-max-unavailable-count` alongside
`kueue.x-k8s.io/pod-group-total-count` on each Pod in the group:

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: trainer-0
  labels:
    kueue.x-k8s.io/queue-name: user-queue
    kueue.x-k8s.io/pod-group-name: trainer
  annotations:
    kueue.x-k8s.io/pod-group-total-count: "200"
    kueue.x-k8s.io/pod-group-max-unavailable-count: "5"
spec:
  # ...
```

### Notes/Constraints/Caveats (Optional)

- **Single-Pod readiness is unchanged.** A Pod counts as ready when its `Ready` condition
  is `True` or, for non-serving Pod groups with `PodIntegrationCountSucceededPodsAsReady`
  enabled, when it has succeeded.
- **Flat count & strictest value.** Unavailability is evaluated across the full group size
  (`pod-group-total-count - readyCount`) and governed by the lowest (strictest)
  `pod-group-max-unavailable-count` value among its Pods; any Pod with a missing or invalid
  annotation (not an integer in `[0, pod-group-total-count - 1]`) defaults to `0`.
- **StatefulSet and LeaderWorkerSet.** Because these integrations use Pod groups under the
  hood, the annotation works when set in their Pod template(s) (for `LeaderWorkerSet`, in
  both the leader and worker templates). Kueue does not propagate it from the parent object
  in Alpha.

### Risks and Mitigations

- **Weakened all-or-nothing guarantee:** Unavailable Pods within the allowed budget still
  hold quota while `PodsReady=True` unblocks subsequent admissions and disarms timeouts.
  *Mitigation:* Opt-in per Pod group; cluster admins can keep the
  `WaitForPodsReadyMaxUnavailable` feature gate disabled or restrict the annotation via a
  ValidatingAdmissionPolicy.
- **Silent fallback in Alpha:** Invalid values (non-integers, `M < 0`, or
  `M >= pod-group-total-count`) fall back to `0` (requiring all `pod-group-total-count`
  Pods to be ready), and the annotation is ignored on unsupported integrations.
  *Mitigation:* Documented in the annotation reference; admission-time validation/warnings
  are a Beta graduation criterion (see [Validation](#validation)).

## Design Details

### API

A new alpha feature gate (disabled by default in v0.20):

```go
WaitForPodsReadyMaxUnavailable featuregate.Feature = "WaitForPodsReadyMaxUnavailable"
```

And a new Pod annotation constant:

```go
GroupMaxUnavailableCountAnnotation = "kueue.x-k8s.io/pod-group-max-unavailable-count"
```

The annotation value is valid when it is an integer `M` with
`0 <= M < pod-group-total-count` (requiring at least one ready Pod in the group).

### Pod Controller

The Pod integration computes `PodsReady` for a Pod group as follows:

```text
maxUnavailable(pod) = M  if the pod's annotation is a valid integer M in [0, totalCount - 1]
                    = 0  otherwise (annotation missing or invalid)

allowedUnavailable  = 0                                        if the feature gate is disabled
                    = min(maxUnavailable(pod) for pod in group) otherwise

unavailable         = totalCount - count(pod in group : readyOrSucceeded(pod))
PodsReady           = unavailable <= allowedUnavailable
```

Taking the minimum across the group makes evaluation deterministic during in-place
annotation updates (`kubectl annotate pods ... --overwrite`) and fail-safe if values
diverge: relaxing the budget (raising `pod-group-max-unavailable-count`) takes effect once
all Pods carry the new value, whereas tightening it (lowering
`pod-group-max-unavailable-count`) takes effect as soon as any Pod is updated. Measuring
`unavailable` against `totalCount` also ensures that uncreated or deleted Pods count as
unavailable.

Disabling the feature gate causes Kueue to ignore the annotation and allow `0` unavailable
Pods (requiring all `totalCount` Pods to be ready).

### Validation

In Alpha, no webhook validation is added; invalid values safely fall back to `0`
unavailable Pods tolerated.

For Beta, admission-time validation or warnings can be added for:
- non-integer values or values outside `[0, pod-group-total-count - 1]` on Pod group Pods,
- annotations placed on unsupported job integrations or their Pod templates.

### Future work ideas

- **Consolidating under `kueue.x-k8s.io/wait-for-pods-ready`
  ([KEP-4803](../4803-workload-level-wait-for-pods-ready/README.md)):** Move the
  unavailability configuration into the per-workload `kueue.x-k8s.io/wait-for-pods-ready`
  JSON annotation (and eventually `Workload.spec.waitForPodsReady`) alongside
  `timeoutSeconds` and `recoveryTimeoutSeconds`, unifying per-workload `WaitForPodsReady`
  settings in one place.
- **Generalizing to `evictionCriteria`:** Generalize the single integer count into
  structured `evictionCriteria` where `maxUnavailable` can be specified per PodSet/role
  within a PodGroup or Workload - for example, tolerating `0` unavailable `leader` Pods
  while tolerating `1` unavailable `worker` Pod.
- **Parent-object propagation:** Propagate the annotation from `StatefulSet` and
  `LeaderWorkerSet` objects to their Pods so the budget can be updated without a Pod
  template rollout.
- **Other integrations:** Support non-Pod-group integrations once ready Pod tracking is
  available ([kubernetes-sigs/kueue#15404](https://github.com/kubernetes-sigs/kueue/issues/15404)).
- **Further improvements:** Based on user feedback, future versions may consider surfacing
  ready and unavailable Pod counts in Workload status and MultiKueue support.

### Test Plan

[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.

#### Prerequisite testing updates

None.

#### Unit tests

- `pkg/controller/jobs/pod`: Test `PodsReady` with `WaitForPodsReadyMaxUnavailable` enabled
  and disabled, covering valid budgets, missing or invalid annotations (non-integers,
  `M < 0`, and `M >= totalCount`), divergent values across Pods in a group, and
  `PodIntegrationCountSucceededPodsAsReady`.

#### Integration tests

- Verify Pod group `PodsReady` transitions and `recoveryTimeout` eviction when
  `kueue.x-k8s.io/pod-group-max-unavailable-count` is configured and updated.
- Verify interaction with per-Workload `kueue.x-k8s.io/wait-for-pods-ready`
  ([KEP-4803](../4803-workload-level-wait-for-pods-ready/README.md)).

#### e2e tests

- Verify that a Pod group with `kueue.x-k8s.io/pod-group-max-unavailable-count` stays ready
  when unavailable Pods remain within the allowed budget and is evicted on
  `recoveryTimeout` when unavailable Pods exceed the budget.

### Graduation Criteria

#### Alpha

- `WaitForPodsReadyMaxUnavailable` feature gate introduced (disabled by default).
- `kueue.x-k8s.io/pod-group-max-unavailable-count` honored for plain Pod groups.
- Unit, integration, and e2e tests added; annotation reference updated.

#### Beta

- Feature gate enabled by default.
- Admission-time validation or warnings for invalid values and unsupported integrations.
- Re-evaluate replacing the annotation with a Workload API field [KEP-4803](../4803-workload-level-wait-for-pods-ready/README.md).

#### Stable

- Feature gate locked to enabled.

## Implementation History

- 2026-09-23: Initially proposed as an extension of
  [KEP-349](../349-all-or-nothing/README.md) in
  [kubernetes-sigs/kueue#16059](https://github.com/kubernetes-sigs/kueue/pull/16059).
- 2026-09-25: Moved to a dedicated KEP and migrated from minimum ready count to maximum
  unavailable Pods.

## Drawbacks

- Weakens the all-or-nothing guarantee of `waitForPodsReady` for Pod groups that opt in.
- Adds an annotation to the Pod group API surface, limited to Pod-group-based integrations
  in Alpha.

## Alternatives

- **Specifying minimum ready Pods (`pod-group-min-ready-count`) instead of maximum
  unavailable:** While equivalent for a fixed group size
  (`minReady = totalCount - maxUnavailable`), a minimum ready count couples the Pod
  template annotation to the total replica count. Scaling a `StatefulSet` or
  `LeaderWorkerSet` would either invalidate the threshold or require modifying the Pod
  template (triggering a Pod rollout). Expressing the budget as
  `pod-group-max-unavailable-count` remains valid across replica scaling (as long as
  `totalCount > maxUnavailable`) and is symmetric with `pod-group-total-count`.
- **Clamping `M >= pod-group-total-count` to `pod-group-total-count - 1` instead of
  falling back to `0`:** Clamping values `>= pod-group-total-count` would mark a group
  `PodsReady=True` as soon as a single Pod is ready (for example if a user accidentally
  sets `pod-group-max-unavailable-count` equal to `pod-group-total-count`). Treating
  `M >= pod-group-total-count` as invalid and falling back to `0` fails closed to
  all-or-nothing behavior.
- **Reusing `podSets[].minCount` ([KEP-420](../420-partial-admission/README.md)):**
  `minCount` resizes a Workload and reduces its quota at admission time, whereas this
  feature keeps the full admission size and only relaxes `PodsReady`.
- **A Workload API field:** A per-PodSet field (for example
  `spec.podSets[].maxUnavailable`) requires all integrations to report ready Pods per
  PodSet
  ([kubernetes-sigs/kueue#15404](https://github.com/kubernetes-sigs/kueue/issues/15404)).
  Starting with a Pod group annotation keeps the Alpha API footprint small.
- **Cluster-level configuration:** Tolerating unavailable Pods is workload-specific; a
  global ratio would weaken all-or-nothing guarantees for workloads that require every Pod.
- **Reading the annotation from a single Pod:** Inspecting only the reconciling Pod would
  make readiness order-dependent during updates and could mark an incomplete group ready if
  partially applied. Taking the group minimum is deterministic and fail-safe.
