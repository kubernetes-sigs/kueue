# KEP-15733: LeaderWorkerSet Pod Template Mutability

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
  - [Terminology](#terminology)
  - [Current behavior](#current-behavior)
  - [Phase 1: quota-neutral edits](#phase-1-quota-neutral-edits)
  - [Phase 2: the remaining edits, through successor Workloads](#phase-2-the-remaining-edits-through-successor-workloads)
    - [How re-admission works](#how-re-admission-works)
    - [Workload identity and naming](#workload-identity-and-naming)
    - [Pod binding](#pod-binding)
    - [Keeping and finishing the old Workload](#keeping-and-finishing-the-old-workload)
    - [Scheduling the successor](#scheduling-the-successor)
    - [Gates, TAS, and MultiKueue](#gates-tas-and-multikueue)
    - [Stalls and rolling strategies](#stalls-and-rolling-strategies)
  - [Interaction with other features](#interaction-with-other-features)
  - [Observability](#observability)
  - [Notes, Constraints, and Caveats](#notes-constraints-and-caveats)
    - [Pods stuck pending need manual deletion](#pods-stuck-pending-need-manual-deletion)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Test Plan](#test-plan)
  - [Unit tests](#unit-tests)
  - [Integration tests](#integration-tests)
  - [e2e tests](#e2e-tests)
- [Graduation Criteria](#graduation-criteria)
  - [Phase 1 gate: <code>LWSMutablePodTemplate</code>](#phase-1-gate-lwsmutablepodtemplate)
  - [Phase 2 gate: <code>LWSRollingReAdmission</code>](#phase-2-gate-lwsrollingreadmission)
  - [Open questions before beta](#open-questions-before-beta)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
<!-- /toc -->

## Summary

Kueue rejects any change to a fixed subset of a queue-managed LeaderWorkerSet (LWS) pod spec, such as resource requests, tolerations, and init containers.
It rejects even edits that cannot change quota, so adding a toleration means recreating the LWS.

This KEP makes that subset mutable in two phases.
Phase 1 (`LWSMutablePodTemplate`) accepts edits to tolerations, container ports, and init containers that leave the pods' effective resource request unchanged, without re-admission.
Phase 2 (`LWSRollingReAdmission`) accepts the remaining edits and re-admits each group through a successor Workload that asks only for the delta; the old reservation stays until the successor is admitted.
Both phases target alpha in v0.20.

## Motivation

How the webhook treats common edits to a queue-managed LWS:

| Edit | Today | Phase 1 | Phase 2 |
|---|---|---|---|
| Add a toleration | Rejected: `field is immutable` | Accepted, no re-admission | Accepted, no re-admission |
| Remove a toleration | Rejected | Accepted; may leave the group [unschedulable](#pods-stuck-pending-need-manual-deletion) | Same as Phase 1 |
| Add an init container within the pod's current request | Rejected | Accepted, no re-admission | Accepted, no re-admission |
| Raise the worker GPU request | Rejected | Rejected | Accepted; each group is re-admitted for the delta |
| Change `nodeSelector` | Rejected | Rejected | Accepted; groups are re-admitted if the selector still matches their flavor |
| Change the container image | Accepted | Accepted | Accepted |

Two operator incidents in [#15570](https://github.com/kubernetes-sigs/kueue/issues/15570), verified on a v0.19.3 deployment, motivated this work:

- A fleet-wide taint for a topology-aware-scheduling rollout needed a matching toleration, which the webhook rejected.
  Every serving LWS was recreated, a multi-hour disruption per fleet rollout.
- Pre-existing workloads became effectively un-restartable: replacement pods come up under a frozen pre-change PodSet, and the fix is rejected too.

[#15733](https://github.com/kubernetes-sigs/kueue/issues/15733) adds the quota-bearing case and proposed deleting each group's Workload once the roll drains it.
On a full ClusterQueue that fails, as a group growing from 8 to 10 GPUs shows:

| | Delete and recreate | Successor Workload (this KEP) |
|---|---|---|
| Transition | 0 held; the 8 GPUs return to the cohort | The old Workload still holds 8 |
| Quota needed to finish | 10 free GPUs | 2 free GPUs |
| If that quota never frees | 0 held; another ClusterQueue may take the 8 | 8 held |

With a successor, the reservation never drops below 8 GPUs, and only the 2-GPU delta has to fit.

### Goals

- Quota-neutral edits without recreating the LWS or re-admission.
- The remaining edits, re-admitted per group within the LWS rolling update budget.
- No release of a group's reservation before its successor is admitted, even on a full cluster, unless the group is evicted.
- The strict comparison as default until each gate graduates, then mutability by default.

### Non-Goals

- Changing Kueue's StatefulSet integration, which stays strict.
- A general API for mutating an admitted Workload's PodSets in place.
- Changes to flavor fungibility, TAS assignment, or preemption policy, beyond the [preemption-usage fix](#scheduling-the-successor).
- Pausing the LWS controller's rolling update: Kueue gates pods, and the roll stops only because gated pods count as unavailable.
- Group-size mutability: `LWSImmutableGroupSize` is unchanged.

## Proposal

### User Stories

**A fleet-wide taint rollout.**
A new node-pool taint needs a matching toleration on LeaderWorkerSets serving many thousands of replicas.
With `LWSMutablePodTemplate`, each LWS accepts the patch, groups roll at their configured pace, and no Workload changes.

**Adding an init container.**
A serving control plane already renders an init container whose requests match the app container's ([#15570 comment](https://github.com/kubernetes-sigs/kueue/issues/15570#issuecomment-5725964604)).
Adding another with no requests leaves the effective request unchanged, so it needs no re-admission, unless a LimitRange default raises that request.

**Growing per-group resources.**
An operator raises the worker request from 8 to 10 GPUs per group with `LWSRollingReAdmission`, and each group the roll reaches asks only for the 2 extra GPUs while keeping its 8.

### Terminology

- **Group**: one LWS replica, a leader and its workers, admitted as one Workload.
- **Effective pod request**: what Kueue charges per pod: `resourcehelpers.PodRequests` after LimitRange defaults, RuntimeClass overhead, and copying limits into missing requests.
  Native sidecars (`restartPolicy: Always`) add to a running sum ([kubelet rules](https://kubernetes.io/docs/concepts/workloads/pods/init-containers/#resources)).
- **Quota-neutral edit**: one that leaves the effective pod request unchanged and does not affect which flavor an admitted Workload holds.
- **Successor Workload**: a Workload built from the edited template for a group that already has an admitted one.
  It needs only the *delta* by which its effective request exceeds the group's reservation, zero or negative for a shrink.
  Unlike in [KEP-14596](../14596-fair-sharing-refill/README.md), "successor" always means a group's replacement Workload.

### Current behavior

The LWS webhook compares `utilpod.SpecShape` of the old and new leader and worker templates through `jobframework.ValidateImmutablePodGroupPodSpec`, shared with the StatefulSet integration.
The shape covers `initContainers` and `containers` as positional lists of `resources.requests` and `ports`, plus `nodeSelector`, `affinity`, `tolerations`, `runtimeClassName`, `priority`, `topologySpreadConstraints`, `overhead`, `resourceClaims`, and pod-level `resources` when set.
Images, limits, and other fields outside it are already mutable, so a limits-only edit can change the effective request without re-admission.
Phase 2 closes that gap for template edits, though not for a later LimitRange change.

The LWS reconciler builds one Workload per group, with static PodSet names (`main`, `leader`, `worker`) and PodSets pinned once admitted, and sets each pod's pod-group and prebuilt Workload names to that Workload's name.

### Phase 1: quota-neutral edits

With `LWSMutablePodTemplate`, the validation for queue-managed LWS relaxes three classes:

| Class | Accepted change | Why it cannot change quota |
|---|---|---|
| `tolerations` | add, remove, replace, reorder | Not requests; adding one only widens placement. |
| container `ports` | add, remove, change | Not requests; a `hostPort` conflict can still block scheduling. |
| `initContainers` | add, remove, reorder, replace | Only if the effective pod request is unchanged. |

For init containers, the comparator applies Kueue's Workload-usage adjustments (`pkg/workload/effective_resources.go`) to both specs and compares `resourcehelpers.PodRequests`, which covers limits-only init containers, LimitRange defaults, and sidecar order.
App containers stay positionally strict, and all other shape fields stay rejected, including `nodeSelector` and `affinity`, which decide the admitted flavor.

Accepted edits need no re-admission, because Workload usage derives from the unchanged effective requests.
The admitted `spec.podSets` stay frozen, though, and eviction clears `status.admission` without touching them, so a re-admission would check old tolerations against flavor and TAS taints.
The reconciler therefore copies Phase 1 fields into `spec.podSets` whenever the Workload holds no quota reservation, which Workload validation allows; under Phase 2, only into the current revision's Workload.
That fixes the second incident.

The comparator lives in `jobframework` behind the gate, for both `leaderTemplate` and `workerTemplate`, and with the gate off nothing changes.

### Phase 2: the remaining edits, through successor Workloads

With `LWSRollingReAdmission`, the webhook accepts every other shape edit, and correctness moves from rejection to per-group re-admission.
Adding or removing `leaderTemplate` stays rejected, because the delta accounting pairs old and new PodSets by position.

#### How re-admission works

1. The webhook accepts the edit, and the LWS controller rolls groups within its `maxUnavailable` and `maxSurge` budget.
2. When a group's pods return on the new revision, the reconciler maps that revision to a Workload by [shape key](#workload-identity-and-naming).
   With an unchanged key, the reconciler binds the pods to the current Workload with no re-admission; a changed key gets a successor.
3. The scheduler credits the group's reservation to the successor, so admission needs only the delta.
4. On admission, the scheduler finishes the old Workload with reason `WorkloadSliceReplaced` (`replaceWorkloadSlice` in `pkg/scheduler/scheduler.go`), and its usage moves to the successor.
   This is the WorkloadSlice aggregation of [KEP-77](../77-dynamically-sized-jobs/README.md).
5. The group's new pods are ungated under the successor.

#### Workload identity and naming

A Workload's *shape key* hashes each PodSet's effective pod request plus `nodeSelector`, `affinity`, `runtimeClassName`, `priority`, `topologySpreadConstraints`, `resourceClaims`, and its topology request.
It omits the Phase 1 classes and fields outside the shape, so image, toleration, and port edits create no successor, but a limits-only edit that changes the effective request does.

Each template edit gets a revision, the `leaderworkerset.sigs.k8s.io/template-revision-hash` label a group's leader and workers share.
The reconciler maps each revision to one unfinished Workload when it first reconciles a pod on it, and records the mapping there.
A revert to a revision whose Workload has finished is therefore mapped again, to a new Workload.
The revision's template comes from its `ControllerRevision` (`lws` library `GetRevision` and `ApplyRevision`), so Kueue needs read access to ControllerRevisions.
Mapping once means a later LimitRange change cannot split a group.
The key is defined over the PodSets Kueue builds, so a legacy Workload takes its key from its own PodSets and keeps the revisions its bound pods use.

A successor is named from the group's base name, its shape key, and the highest ControllerRevision number carrying its revision's hash.
LWS numbers every edit, a revert included, one higher, so names are never reused.
Because names are deterministic, a stale-cache retry fails with `AlreadyExists`, and normalization finishes any extra successor a stale ControllerRevision cache still produces.
The successor carries the `kueue.x-k8s.io/workload-slice-replacement-for` annotation that `ReplacementForKey` reads, naming the Workload it replaces.
Unsuffixed Workloads keep their names, so enabling the gate renames nothing, and surge groups get ordinary new Workloads.

#### Pod binding

At a pod's first reconcile, the reconciler binds it to the Workload mapped to its revision by setting its pod-group name and prebuilt Workload name.
The pod integration ungates a whole pod group under the Workload of one listed pod, so pods of different Workloads never share a pod group.
The `ShouldUngatePod` shortcut ungates a pod on the StatefulSet's `CurrentRevision` during a roll without consulting a Workload; under Phase 2 it applies only when that revision maps to the group's admitted Workload and that Workload's PodsReady reason is no longer `WaitForSuccessor`.

A further edit, including a revert, supersedes a pending successor: the reconciler deletes it and its gated pods, with a `resourceVersion` precondition from a fresh read showing no quota reservation.
If the successor was admitted meanwhile, it becomes the group's admitted Workload, and the next successor replaces it.
The deleted pods never ran; because a new revision resets the StatefulSet partition, they come back on its current revision under the old Workload, and the roll reaches them again later.

#### Keeping and finishing the old Workload

Per group index, the reconciler keeps every unfinished Workload, even with no pods bound, covering the drain window.
Each pass it normalizes the group: one admitted Workload, at most one pending successor replacing it, and an evicted Workload with a pending successor finished once none of its pods is active.
It deletes finished Workloads and those of removed group indexes; since nothing else deletes group Workloads, the finalizer dropped from an empty pod group never releases a reservation early.

Before creating a successor, the reconciler annotates the old Workload with the successor's name (`kueue.x-k8s.io/pending-successor`) and recomputes the annotation every pass.
The pod integration is its only reader.
While the named successor exists, is unfinished, and points back through `replacement-for`, the pod integration writes PodsReady False with a new reason, `WaitForSuccessor`, whether or not `waitForPodsReady` is enabled, including on its finalize path for an empty pod group.
Admission patches carry PodsReady but never compute the reason, and since the rest of Kueue keys on the reason, a stale annotation loses its effect at the pod integration's next pass.
`WaitForSuccessor` is an additive API constant, like the existing `WaitForStart` and `WaitForRecovery`.
While it is set, the old Workload is treated as having no pods:

- the cache leaves it out of the not-ready set that `waitForPodsReady.blockAdmission` waits on;
- no PodsReady timeout (`timeout`, `recoveryTimeout`, `unscheduledTimeout`) applies;
- on eviction the pod integration sets `Requeued=False`, as for single-pod Workloads, so it is not requeued before normalization finishes it.

When the exemption ends, the pod integration rewrites PodsReady as `WaitForRecovery` before ungating any rebound pod.
It sets `LastTransitionTime` directly, because `SetStatusCondition` keeps the old one for a False-to-False write, so `recoveryTimeout` runs from that write.

The scheduler finishes the old Workload in the same admission routine that admits the successor; until then the cache counts both, which over-counts quota instead of releasing it.
The LWS reconciler does not call `EnsureWorkloadSlices`, which repairs a failed finish for elastic jobs, so normalization does: once a successor is admitted, any older unfinished Workload of the group is finished.

#### Scheduling the successor

The fit path already subtracts the old Workload's requests per flavor and resource.
Preemption does not: `TotalRequestsFor` scales a replacement's requests by the change in pod count, which is zero when only per-pod requests change, so a successor would find no targets.
Phase 2 computes a replacement's preemption usage as new total minus old total per flavor and resource, clamped at zero.
Elastic jobs, whose per-pod requests do not change, get the same number.
A successor then preempts for the delta under the ClusterQueue's policy.

The slice path pins a replacement to the original's flavors (`pkg/scheduler/flavorassigner/flavorassigner.go`).
At alpha, a successor needing a different flavor, or a resource the old Workload never requested, therefore cannot fit; lifting the pin is an [open question](#open-questions-before-beta).

Successors carry the replacement reference but not the elastic-job annotation, which `ReplacedWorkloadSlice` does not need.
Elastic Workloads reuse their previous TAS assignment (`pkg/cache/scheduler/tas_elastic_workloads.go`) and reject `required` and `preferred` topology requests (`pkg/scheduler/flavorassigner/tas_flavorassigner.go`).
Successors are placed fresh, with both request types working, and `ElasticJobsViaWorkloadSlicesWithTAS` makes the scheduler subtract the old Workload's TAS usage.

#### Gates, TAS, and MultiKueue

`LWSRollingReAdmission` depends on `LWSMutablePodTemplate` and on `ElasticJobsViaWorkloadSlices`, Beta and on by default since v0.18.
With `TopologyAwareScheduling` and `LWSRollingReAdmission` on, Kueue refuses to start if `ElasticJobsViaWorkloadSlicesWithTAS` is off, like the checks in `pkg/config/validation.go`.
Without that gate the old TAS usage would be double-counted, and a declared dependency would force TAS on.

The MultiKueue adapter looks up group Workloads by unsuffixed name (`WorkloadKeysFor`), so Phase 2 does not apply there.
The webhook keeps rejecting quota-bearing edits when the ClusterQueue uses a MultiKueue admission check, or on a worker copy labeled `kueue.x-k8s.io/multikueue-origin`.
The reconciler creates no successor for such groups, so a MultiKueue check added mid-roll leaves new pods gated.
The adapter also never updates a remote LWS after creating it, so even Phase 1 edits do not reach workers; that predates this KEP.

Turning `LWSRollingReAdmission` off makes the webhook reject quota-bearing edits again, but the reconciler still applies Phase 2 to any revision that differs from its Workload in a field the webhook now rejects.
Only a gate-on edit can produce one, so an in-flight roll finishes safely, even for groups not yet reached.
No pod binds to a Workload of another shape, and limits-only edits behave as today.

#### Stalls and rolling strategies

| Situation | Roll and reservation | Recovery |
|---|---|---|
| Successor does not fit | New pods stay gated, and the old Workload keeps its reservation; at most `maxUnavailable + maxSurge` groups wait. | Requeue retries; the operator can free quota, extend flavors, or edit. |
| Needs a different flavor or a new resource | The flavor pin stops it at alpha; the reservation stays. | Revert the edit. |
| Template changed again, including a revert | The pending successor and its gated pods are deleted; pods return under the old Workload. | None. |
| Old Workload evicted while a successor is pending | Finished without a requeue once its pods stop; the successor needs the full shape. | Normal re-admission. |
| Ungated pods cannot be placed | Pods stay pending, and the roll stops. | Fix the template, then [delete the stuck pods](#pods-stuck-pending-need-manual-deletion). |

| `rolloutStrategy` | Behavior |
|---|---|
| `maxSurge: 0` (in-place) | A successor per group, as the roll reaches it. |
| `maxSurge > 0`, `maxUnavailable: 0` | The surge group, needing a full extra group of quota, is admitted before any old group drains; old groups then roll in place through successors. |
| `maxSurge > 0`, `maxUnavailable ≥ 1` | LWS drains up to `maxUnavailable` old groups while surge Workloads are pending. |

A saturated ClusterQueue lacks a spare group of quota, so in-place rolls are the main target.

### Interaction with other features

- **Fair sharing and cohorts**: the old usage moves to the successor without a release, so cohort usage changes once, by the delta.
- **Partial admission**: not applied, because a serving group needs all its pods.
- **TAS** ([KEP-2724](../2724-topology-aware-scheduling/README.md)), **preemption**, and **MultiKueue**: see [Phase 2](#phase-2-the-remaining-edits-through-successor-workloads).

### Observability

Events on both Workloads for creation, admission, and aggregation (`WorkloadSliceReplaced`); an event on a pending successor that holds up a roll; and metrics for successors created, admitted, and pending per ClusterQueue, following `ReportReplacedWorkloadSlices`.

### Notes, Constraints, and Caveats

**Restart window.**
Kueue cannot avoid the rolling group's restart: with `maxSurge: 0`, old pods terminate before new ones bind, and Phase 2 removes only the admission stall around that.
Operators who need no capacity loss use `maxSurge > 0` with `maxUnavailable: 0`.

**Idle reservation.**
A waiting group holds its reservation idle, with no timeout, and stays down until quota frees or an operator acts.

**No revert.**
Kueue never reverts an accepted edit, because the API server accepts it before any per-group admission.

#### Pods stuck pending need manual deletion

A StatefulSet does not re-apply a template to pods that are already pending.
If ungated pods cannot be placed because of the template, for example after a removed toleration, a `hostPort` conflict, or an unsatisfiable `nodeSelector`, fix the template and delete the stuck pods in reverse ordinal order ([prior investigation](https://github.com/kubernetes-sigs/kueue/issues/15733#issuecomment-5727143545)).

### Risks and Mitigations

**Comparator error.**
A wrong comparator would leave a reservation smaller than the running pods request.
Mitigation: it reuses Kueue's own adjustment and request functions, tests assert that Phase 1 edits leave Workload usage unchanged, and the strict path stays the default until graduation.

**Cross-controller reason.**
The pod integration writes `WaitForSuccessor`, and the cache and workload controller act on it, so a bug could hold a reservation too long or release it early.
Mitigation: one writer that checks the successor directly, and tests for each reader, including the `waitForPodsReady` cases.

## Test Plan

Shared test bodies assert after every case that:

- a Phase 1 edit leaves Workload usage (`workload.NewInfo`) unchanged;
- a group's reservation never drops below the smaller of its old and new requirement unless evicted;
- a pod is ungated only under the Workload mapped to its revision.

### Unit tests

- The comparator matrix per relaxed class, gate on and off: limits-only init containers, LimitRange defaults, sidecar order, and strict rejection elsewhere and for the StatefulSet integration.
- Webhook admission for each gate combination, and the `ElasticJobsViaWorkloadSlicesWithTAS` startup check.
- One test per Phase 2 rule across the reconciler, pod integration, cache, workload controller, and scheduler.
  These include image, toleration, and port edits creating no successor, a stale-cache `AlreadyExists` retry, `WaitForSuccessor` only for a valid successor, `Requeued=False`, the `WaitForRecovery` rewrite, preemption usage for equal pod counts with elastic scale-ups unchanged, and the flavor pin for a new resource.

### Integration tests

Envtest does not run the LWS controller, so these drive StatefulSet and pod state directly:

- each relaxed class accepted under the gate and rejected without it, and a toleration edit surviving eviction;
- a quota-bearing bump admitted for the delta, with preemption, and an inadmissible successor that stalls and resumes;
- a mid-roll edit and a revert while a successor is pending, including the supersede race;
- a stalled roll under `recoveryTimeout` and `blockAdmission` evicting nothing and blocking no other admission, and a revert after a longer stall not evicting the old Workload early;
- the old Workload keeping its reservation while it drains, and while its workers terminate after a preemption;
- MultiKueue rejection, and the gate turned off mid-roll.

### e2e tests

Only e2e runs the LWS controller:

- toleration, port, and image rolls with both gates on, creating no successor;
- a quota-bearing bump on a contended ClusterQueue with `maxSurge: 0`, where at most `maxUnavailable` groups wait and the old reservation never drops;
- a surge roll with `maxUnavailable: 0`, where the surge group is admitted before any old group drains;
- a TAS variant of the bump.

## Graduation Criteria

### Phase 1 gate: `LWSMutablePodTemplate`

- **Alpha (v0.20)**: off by default; comparator and webhook tests.
- **Beta (v0.21)**: on by default.
  The strict behavior [was introduced for implementation simplicity](https://github.com/kubernetes-sigs/kueue/issues/15733#issuecomment-5726044307), and a maintainer stated in the same comment that LWS mutability should be the default.
  Requires e2e coverage of taint and init-container rollouts, and user feedback.
- **GA**: gate removed.

### Phase 2 gate: `LWSRollingReAdmission`

- **Alpha (v0.20)**: off by default, with its gate dependencies; in-place rolls; unit and integration tests; no MultiKueue.
- **Beta (v0.21)**: on by default.
  Requires TAS and contended-ClusterQueue e2e coverage, stall metrics, user feedback, and answers to the open questions.
  Also requires `ElasticJobsViaWorkloadSlicesWithTAS` at Beta and on by default, so that a default configuration passes the startup check.
- **GA**: gate removed.

### Open questions before beta

The Phase 2 mechanism has not yet been reviewed by maintainers.

- **Flavor changes.** The pin keeps the scheduler change small, but an edit needing a different flavor or a new resource can never finish its roll.
  Lifting it needs a full-shape admission on the new flavor while the old reservation stays, briefly doubling what the group occupies.
- **Successor preemption.** Serving operators may want successors that only wait, so a template edit never evicts other admitted work.
- **WorkloadSlice reuse.** Reuse keeps the change small but ties LWS to a Beta mechanism; generalizing would decouple them at a higher cost and decide whether successor names become a documented format.

## Implementation History

- 2026-09-14: [#15570](https://github.com/kubernetes-sigs/kueue/issues/15570) requests relaxing the comparison for quota-neutral fields.
- 2026-09-17: [#15733](https://github.com/kubernetes-sigs/kueue/issues/15733) proposes the two mechanisms.
- 2026-09-18: author verification of the rejections ([#15570 comment](https://github.com/kubernetes-sigs/kueue/issues/15570#issuecomment-5725964604)), and of a deleted group Workload re-admitted in ~1–2s on an uncontended cluster ([#15733 comment](https://github.com/kubernetes-sigs/kueue/issues/15733#issuecomment-5725965029)).
- 2026-09-18: [maintainer feedback](https://github.com/kubernetes-sigs/kueue/issues/15733#issuecomment-5726044307) finds both ideas sound, suggests the Phase 1 behavior may be a bug, and asks for a graduation plan and research of prior `nodeSelector` work ([findings](https://github.com/kubernetes-sigs/kueue/issues/15733#issuecomment-5727143545)).
- 2026-09-22: KEP draft.

## Drawbacks

Two gates and two webhook behaviors add testing and diagnostic surface.
Accepting an edit whose roll may stall trades an early, explicit rejection for a later, visible stall, so operators must watch roll progress.

## Alternatives

- **Delete and recreate the group Workload**: the original #15733 proposal; [Motivation](#motivation) shows why it fails on a full cluster.
- **Producer-computed shape hash with `matchConditions`** (#15570's original opt-in): Kueue cannot verify the producer's hash, and every producer must adopt it; the in-tree comparator needs neither.
- **Webhook preflight rejection**: racy, because quota shifts as the roll advances, and it forces clients to retry, while a bounded stall resumes on its own.
- **Per-object opt-in annotation**: more API surface to graduate and remove, while the gates already limit exposure.
- **Recompute PodSets while unadmitted**: Phase 1 uses this for its own fields, but for quota-bearing edits it reaches only Workloads that already lost admission.
- **Make LWS an elastic-job framework**: the most reuse, but the jobframework reconciler assumes one Workload per job, while LWS creates one per group.
