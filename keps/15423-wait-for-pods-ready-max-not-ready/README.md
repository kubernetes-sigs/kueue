# KEP-15423: WaitForPodsReady Maximum Not-Ready Pods

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

This KEP introduces the `kueue.x-k8s.io/pod-group-max-not-ready-count` Pod annotation,
guarded by the alpha `WaitForPodsReadyMaxNotReady` feature gate (disabled by default).
It sets the maximum number of not-ready Pods tolerated for a Pod group to satisfy the
Workload `PodsReady` condition, relaxing `waitForPodsReady.timeout` and
`waitForPodsReady.recoveryTimeout` while keeping admission and quota reservation at full
`kueue.x-k8s.io/pod-group-total-count` size.

## Motivation

`waitForPodsReady` ([KEP-349](../349-all-or-nothing/README.md)) requires all
`kueue.x-k8s.io/pod-group-total-count` Pods of a Pod group to become ready within
`timeout` (and recover within `recoveryTimeout`), otherwise evicting and requeuing the
entire Workload.

For large workloads that tolerate a few not-ready Pods, evicting the entire Workload is
far more disruptive than running slightly below capacity while those Pods recover.

Expressing this tolerance as a maximum number of not-ready Pods mirrors the
`maxUnavailable` pattern of standard Kubernetes disruption and rollout primitives
(`PodDisruptionBudget`, `StatefulSet`, `Deployment`) and remains valid when a
`StatefulSet` or `LeaderWorkerSet` is scaled (provided the total Pod count stays above the
configured limit) without modifying the Pod template.

Unlike partial admission (`podSets[].minCount`,
[KEP-420](../420-partial-admission/README.md)), which shrinks a Workload at admission
time, this feature admits the Workload at full size and only relaxes the `PodsReady`
threshold.

### Goals

- Allow configuring, per Pod group, the maximum number of not-ready Pods tolerated when
  satisfying the Workload `PodsReady` condition for both `timeout` and `recoveryTimeout`.
- Support plain Pod groups ([KEP-976](../976-plain-pods/README.md)).
- Preserve all-or-nothing behavior (`0` not-ready Pods tolerated) when the feature gate
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

Introduce the `kueue.x-k8s.io/pod-group-max-not-ready-count` annotation on the Pods of a
Pod group, honored when the `WaitForPodsReadyMaxNotReady` feature gate is enabled.

A Pod group's **not-ready Pods** are the `pod-group-total-count` slots of the group that
are not filled by a Pod counted as ready for `PodsReady` purposes, i.e.
`notReady = pod-group-total-count - readyCount`. This covers Pods whose `Ready` condition
is not `True` (unless they succeeded and succeeded Pods count as ready, see
[Notes/Constraints/Caveats](#notesconstraintscaveats-optional)) as well as Pods that were
not yet created or were deleted. The term matches the existing `PodsReady` condition and
its `Not all pods are ready or succeeded` message.

When every Pod in the group carries a valid integer `M` in `[0, pod-group-total-count - 1]`,
the Pod group tolerates up to `M` not-ready Pods (satisfying `PodsReady` once at least
`pod-group-total-count - M` of its Pods are ready). Otherwise, `0` not-ready Pods are
tolerated and all `pod-group-total-count` Pods must be ready.

The existing `waitForPodsReady` machinery (`timeout`, `recoveryTimeout`, `blockAdmission`,
`requeuingStrategy`, and per-Workload timeouts from
[KEP-4803](../4803-workload-level-wait-for-pods-ready/README.md)) applies unchanged on top
of the resulting `PodsReady` condition.

### User Stories (Optional)

#### Story 1

As a user running a 200-replica `StatefulSet` (or `LeaderWorkerSet`) backed by Kueue's Pod
group integration, I want the Workload to tolerate up to 5 not-ready Pods when evaluating
`PodsReady` (requiring at least 195 ready Pods), so that a few delayed or replacement Pods
do not trigger `timeout` or `recoveryTimeout` eviction. I set the maximum not-ready count
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
        kueue.x-k8s.io/pod-group-max-not-ready-count: "5"
    spec:
      # ...
```

#### Story 2

As a user or custom controller managing a plain Pod group directly (without `StatefulSet`
or `LeaderWorkerSet`), I want the Workload to tolerate up to 5 not-ready Pods out of 200
when evaluating `PodsReady`, so that a few delayed or replacement Pods do not trigger
`timeout` or `recoveryTimeout` eviction. I set
`kueue.x-k8s.io/pod-group-max-not-ready-count` alongside
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
    kueue.x-k8s.io/pod-group-max-not-ready-count: "5"
spec:
  # ...
```

### Notes/Constraints/Caveats (Optional)

- **Single-Pod readiness is unchanged.** A Pod counts as ready when its `Ready` condition
  is `True` or, for non-serving Pod groups with `PodIntegrationCountSucceededPodsAsReady`
  enabled, when it has succeeded.
- **Flat count & strictest value.** Not-ready Pods are counted across the full group size
  (`pod-group-total-count - readyCount`) and governed by the lowest (strictest)
  `pod-group-max-not-ready-count` value among its Pods; any Pod with a missing or invalid
  annotation (not an integer in `[0, pod-group-total-count - 1]`) defaults to `0`.
- **StatefulSet and LeaderWorkerSet.** Because these integrations use Pod groups under the
  hood, the annotation works when set in their Pod template(s) (for `LeaderWorkerSet`, in
  both the leader and worker templates). Kueue does not propagate it from the parent object
  in Alpha.
- **Role-agnostic budget.** The budget applies to the Pod group as a whole, not per role
  or Pod template. Leader and worker Pods of a `LeaderWorkerSet` group are counted together,
  and a not-ready leader consumes the budget exactly like a not-ready worker. If the
  templates carry different values, the strictest one applies to the whole group. For
  example, with the following `LeaderWorkerSet`, each group of 10 Pods tolerates at most
  `1` not-ready Pod (leader or worker), not `1` leader plus `5` workers:

  ```yaml
  apiVersion: leaderworkerset.x-k8s.io/v1
  kind: LeaderWorkerSet
  spec:
    replicas: 2
    leaderWorkerTemplate:
      size: 10
      leaderTemplate:
        metadata:
          annotations:
            kueue.x-k8s.io/pod-group-max-not-ready-count: "1"
      workerTemplate:
        metadata:
          annotations:
            kueue.x-k8s.io/pod-group-max-not-ready-count: "5"
  ```

  Per-role budgets are out of scope for Alpha (see [Non-Goals](#non-goals)) and are
  tracked as future work under `evictionCriteria` (see
  [Future work ideas](#future-work-ideas)).
- **Annotation updates on running Pods.** Pod annotations are mutable, so
  `pod-group-max-not-ready-count` can be changed on running Pods (for example with
  `kubectl annotate pods ... --overwrite`). In Alpha, Kueue accepts such updates without
  validation and re-evaluates `PodsReady` using the strictest value in the group (see
  [Pod Controller](#pod-controller)). Validating updates is deferred (see
  [Validation](#validation)).

### Risks and Mitigations

- **Weakened all-or-nothing guarantee:** Not-ready Pods within the allowed budget still
  hold quota while `PodsReady=True` unblocks subsequent admissions and disarms timeouts.
  *Mitigation:* Opt-in per Pod group; cluster admins can keep the
  `WaitForPodsReadyMaxNotReady` feature gate disabled or restrict the annotation via a
  ValidatingAdmissionPolicy.
- **Silent fallback in Alpha:** Invalid values (non-integers, `M < 0`, or
  `M >= pod-group-total-count`) fall back to `0` (requiring all `pod-group-total-count`
  Pods to be ready), and the annotation is ignored on unsupported integrations.
  *Mitigation:* Documented in the annotation reference; admission-time validation/warnings
  are a Beta graduation criterion (see [Validation](#validation)).
- **Stuck replacements for serving Pod groups:** In a serving Pod group
  (`kueue.x-k8s.io/pod-group-serving: "true"`), Kueue keeps the finalizer on a failed Pod,
  and today `recoveryTimeout` eviction is what releases it. When the failed Pods stay
  within the budget, `PodsReady` stays `True` and no eviction happens, so the finalizers
  stay in place. An external controller that creates replacement Pods under the same name
  (for example a `StatefulSet`, or a custom controller that manages the Pod group
  directly) then cannot create the replacements. The group keeps running below capacity
  until enough Pods fail to exceed the budget, which then triggers `recoveryTimeout`
  eviction. This limitation is accepted for Alpha.

## Design Details

### API

A new Pod annotation constant:

```go
GroupMaxNotReadyCountAnnotation = "kueue.x-k8s.io/pod-group-max-not-ready-count"
```

The annotation value is valid when it is an integer `M` with
`0 <= M < pod-group-total-count` (requiring at least one ready Pod in the group).

### Pod Controller

The Pod integration computes `PodsReady` for a Pod group as follows:

```text
maxNotReady(pod)    = M  if the pod's annotation is a valid integer M in [0, totalCount - 1]
                    = 0  otherwise (annotation missing or invalid)

allowedNotReady     = 0                                     if the feature gate is disabled
                    = min(maxNotReady(pod) for pod in group) otherwise

notReady            = totalCount - count(pod in group : readyOrSucceeded(pod))
PodsReady           = notReady <= allowedNotReady
```

Taking the minimum across the group makes evaluation deterministic during in-place
annotation updates (`kubectl annotate pods ... --overwrite`) and fail-safe if values
diverge: relaxing the budget (raising `pod-group-max-not-ready-count`) takes effect once
all Pods carry the new value, whereas tightening it (lowering
`pod-group-max-not-ready-count`) takes effect as soon as any Pod is updated. Measuring
`notReady` against `totalCount` also ensures that uncreated or deleted Pods count as
not ready.

Disabling the feature gate causes Kueue to ignore the annotation and allow `0` not-ready
Pods (requiring all `totalCount` Pods to be ready).

### Validation

In Alpha, no webhook validation is added; invalid values safely fall back to `0`
not-ready Pods tolerated.

Pod annotations are mutable, so `pod-group-max-not-ready-count` can also be changed after
Pods are created. In Alpha, such updates are accepted as-is. Evaluating the group minimum
keeps the behavior deterministic and fail-safe while the update rolls out across the
group's Pods.

For a follow-up Alpha iteration or Beta, admission-time validation or warnings can be
added for:
- non-integer values or values outside `[0, pod-group-total-count - 1]` on Pod group Pods,
- annotations placed on unsupported job integrations or their Pod templates,
- updates to the annotation on existing Pods (for example, validating the new value, or
  warning when Pods in the same group carry different values).

### Future work ideas

- **Consolidating under `kueue.x-k8s.io/wait-for-pods-ready`
  ([KEP-4803](../4803-workload-level-wait-for-pods-ready/README.md)):** Move the
  not-ready budget configuration into the per-workload `kueue.x-k8s.io/wait-for-pods-ready`
  JSON annotation (and eventually `Workload.spec.waitForPodsReady`) alongside
  `timeoutSeconds` and `recoveryTimeoutSeconds`, unifying per-workload `WaitForPodsReady`
  settings in one place.
- **Generalizing to `evictionCriteria`:** Generalize the single integer count into
  structured `evictionCriteria` where `maxNotReady` can be specified per PodSet/role
  within a PodGroup or Workload - for example, tolerating `0` not-ready `leader` Pods
  while tolerating `1` not-ready `worker` Pod.
- **Parent-object propagation:** Propagate the annotation from `StatefulSet` and
  `LeaderWorkerSet` objects to their Pods so the budget can be updated without a Pod
  template rollout.
- **Other integrations:** Support non-Pod-group integrations once ready Pod tracking is
  available ([kubernetes-sigs/kueue#15404](https://github.com/kubernetes-sigs/kueue/issues/15404)).
- **Further improvements:** Based on user feedback, future versions may consider surfacing
  ready and not-ready Pod counts in Workload status and MultiKueue support.

### Test Plan

[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.

#### Prerequisite testing updates

None.

#### Unit tests

- `pkg/controller/jobs/pod`: Test `PodsReady` with `WaitForPodsReadyMaxNotReady` enabled
  and disabled, covering valid budgets, missing or invalid annotations (non-integers,
  `M < 0`, and `M >= totalCount`), divergent values across Pods in a group, and
  `PodIntegrationCountSucceededPodsAsReady`.

#### Integration tests

- Verify Pod group `PodsReady` transitions and `recoveryTimeout` eviction when
  `kueue.x-k8s.io/pod-group-max-not-ready-count` is configured and updated.
- Verify interaction with per-Workload `kueue.x-k8s.io/wait-for-pods-ready`
  ([KEP-4803](../4803-workload-level-wait-for-pods-ready/README.md)).

#### e2e tests

- Verify that a Pod group with `kueue.x-k8s.io/pod-group-max-not-ready-count` stays ready
  when not-ready Pods remain within the allowed budget and is evicted on
  `recoveryTimeout` when not-ready Pods exceed the budget.

### Graduation Criteria

#### Alpha

- `WaitForPodsReadyMaxNotReady` feature gate introduced (disabled by default).
- `kueue.x-k8s.io/pod-group-max-not-ready-count` honored for plain Pod groups.
- Unit, integration, and e2e tests added; annotation reference updated.
- `PodsReady=False` reuses the existing generic message (`Not all pods are ready or
  succeeded`); no new condition messages are introduced.

#### Beta

- Feature gate enabled by default.
- Webhook validation or warnings for invalid values, unsupported integrations, and
  annotation updates on existing Pods.
- Resolve stuck same-name replacements for serving Pod groups whose failed Pods stay
  within the budget (see [Risks and Mitigations](#risks-and-mitigations)).
- Make the `PodsReady=False` condition message explain why the condition is not satisfied
  (for example, the not-ready Pod count versus the allowed
  `pod-group-max-not-ready-count` budget). This may land in an intermediate Alpha
  iteration.
- Honor `kueue.x-k8s.io/pod-group-max-not-ready-count` for
  `waitForPodsReady.unscheduledTimeout` ([KEP-13502](../13502-unscheduled-pods-timeout/README.md))
  so up to `pod-group-max-not-ready-count` unscheduled Pods are tolerated.
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
  not-ready:** While equivalent for a fixed group size
  (`minReady = totalCount - maxNotReady`), a minimum ready count couples the Pod
  template annotation to the total replica count. Scaling a `StatefulSet` or
  `LeaderWorkerSet` would either invalidate the threshold or require modifying the Pod
  template (triggering a Pod rollout). Expressing the budget as
  `pod-group-max-not-ready-count` remains valid across replica scaling (as long as
  `totalCount > maxNotReady`) and is symmetric with `pod-group-total-count`.
- **Clamping `M >= pod-group-total-count` to `pod-group-total-count - 1` instead of
  falling back to `0`:** Clamping values `>= pod-group-total-count` would mark a group
  `PodsReady=True` as soon as a single Pod is ready (for example if a user accidentally
  sets `pod-group-max-not-ready-count` equal to `pod-group-total-count`). Treating
  `M >= pod-group-total-count` as invalid and falling back to `0` fails closed to
  all-or-nothing behavior.
- **Reusing `podSets[].minCount` ([KEP-420](../420-partial-admission/README.md)):**
  `minCount` resizes a Workload and reduces its quota at admission time, whereas this
  feature keeps the full admission size and only relaxes `PodsReady`.
- **A Workload API field:** A per-PodSet field (for example
  `spec.podSets[].maxNotReady`) requires all integrations to report ready Pods per
  PodSet
  ([kubernetes-sigs/kueue#15404](https://github.com/kubernetes-sigs/kueue/issues/15404)).
  Starting with a Pod group annotation keeps the Alpha API footprint small.
- **Cluster-level configuration:** Tolerating not-ready Pods is workload-specific; a
  global ratio would weaken all-or-nothing guarantees for workloads that require every Pod.
- **Reading the annotation from a single Pod:** Inspecting only the reconciling Pod would
  make readiness order-dependent during updates and could mark an incomplete group ready if
  partially applied. Taking the group minimum is deterministic and fail-safe.
