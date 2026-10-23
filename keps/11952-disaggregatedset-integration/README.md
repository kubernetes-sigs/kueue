# KEP-11952: DisaggregatedSet Integration

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1: Atomic admission of a disaggregated model server](#story-1-atomic-admission-of-a-disaggregated-model-server)
    - [Story 2: Preempting a whole inference workload](#story-2-preempting-a-whole-inference-workload)
    - [Story 3: Scaling out by slices](#story-3-scaling-out-by-slices)
    - [Story 4: Rolling out a new model server image](#story-4-rolling-out-a-new-model-server-image)
  - [Notes/Constraints/Caveats](#notesconstraintscaveats)
    - [Required LWS version](#required-lws-version)
    - [Only constant values are stamped on templates](#only-constant-values-are-stamped-on-templates)
    - [Handling of child objects](#handling-of-child-objects)
    - [Size and topology constraints](#size-and-topology-constraints)
    - [Supported and unsupported features](#supported-and-unsupported-features)
  - [Risks and Mitigations](#risks-and-mitigations)
    - [The DisaggregatedSet API changes quickly](#the-disaggregatedset-api-changes-quickly)
    - [Pod adoption](#pod-adoption)
    - [Large DisaggregatedSets create many Pods and Workloads](#large-disaggregatedsets-create-many-pods-and-workloads)
    - [Rollout churn](#rollout-churn)
    - [Interaction with the planner's unschedulable fallback](#interaction-with-the-planners-unschedulable-fallback)
    - [Rollouts stall when a group is unready](#rollouts-stall-when-a-group-is-unready)
    - [Changes to the stamped annotations start a rollout](#changes-to-the-stamped-annotations-start-a-rollout)
    - [Scale-down releases quota before Pods terminate](#scale-down-releases-quota-before-pods-terminate)
- [Design Details](#design-details)
  - [Feature gate and registration](#feature-gate-and-registration)
  - [Disabling the integration](#disabling-the-integration)
  - [Webhook](#webhook)
    - [Defaulting](#defaulting)
    - [Validation on create](#validation-on-create)
    - [Validation on update](#validation-on-update)
  - [Workload construction](#workload-construction)
    - [Identity and ownership](#identity-and-ownership)
    - [PodSets](#podsets)
    - [Creation, deletion and replacement](#creation-deletion-and-replacement)
    - [Queue and priority updates](#queue-and-priority-updates)
  - [Workload PodSet limit](#workload-podset-limit)
  - [Placement policy and Topology Aware Scheduling](#placement-policy-and-topology-aware-scheduling)
  - [TAS rank ordering](#tas-rank-ordering)
  - [Pod reconciliation](#pod-reconciliation)
  - [Shared helpers](#shared-helpers)
  - [Rollout and deletion](#rollout-and-deletion)
  - [Interactions with Kueue features](#interactions-with-kueue-features)
  - [Observability](#observability)
  - [Configuration, manifests and documentation](#configuration-manifests-and-documentation)
  - [Test Plan](#test-plan)
    - [Prerequisite testing updates](#prerequisite-testing-updates)
    - [Unit tests](#unit-tests)
    - [Integration tests](#integration-tests)
      - [Webhook tests](#webhook-tests)
      - [Controller tests](#controller-tests)
    - [e2e tests](#e2e-tests)
  - [Graduation Criteria](#graduation-criteria)
    - [Alpha (v0.21)](#alpha-v021)
    - [Beta](#beta)
    - [GA](#ga)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
  - [One Workload per DisaggregatedSet](#one-workload-per-disaggregatedset)
  - [One Workload per role, or keeping per-replica LWS Workloads](#one-workload-per-role-or-keeping-per-replica-lws-workloads)
  - [External framework integration](#external-framework-integration)
  - [Adding <code>suspend</code> to DisaggregatedSet](#adding-suspend-to-disaggregatedset)
  - [Evicting by scaling roles to zero](#evicting-by-scaling-roles-to-zero)
- [Appendix: DisaggregatedSet behavior](#appendix-disaggregatedset-behavior)
<!-- /toc -->

## Summary

This KEP adds atomic admission and preemption for replicated serving groups that
contain several resource roles. The first supported integration is
[DisaggregatedSet](https://github.com/kubernetes-sigs/lws/tree/main/keps/766-DisaggregatedSet)
(`disaggregatedset.x-k8s.io/v1`), the LeaderWorkerSet (LWS) API that runs several
LeaderWorkerSets, called roles, as one logical unit for disaggregated LLM inference.
A typical example is a `prefill` role and a `decode` role with different resource shapes.

## Motivation

Disaggregated serving stacks such as [llm-d](https://github.com/llm-d/llm-d) deploy
prefill and decode as separate LeaderWorkerSets. Each role needs different hardware,
for example 8 prefill replicas at 1 GPU each and 2 decode replicas at 4 GPUs each.
The service only works when every role is running.

Today Kueue can only manage the child LeaderWorkerSets individually. A queue-name
label on a role's metadata, a namespace default LocalQueue, or
`manageJobsWithoutQueueName` makes each child LWS a managed LWS. Kueue then creates
one Workload per LWS replica, independently for each role. Under quota pressure this
causes two problems:

1. **Partial admission.** Kueue can admit the prefill groups but not the decode
   groups, or the reverse. The admitted role holds accelerators without serving any
   traffic until the other role is admitted too.
2. **Fragmented preemption.** When reclaiming accelerators for higher-priority work,
   Kueue selects victims per LWS replica. It may evict only one role and leave a
   broken deployment that still holds quota.

DisaggregatedSet already groups the roles into one object. An integration that
understands it can give atomic admission and preemption using existing Kueue
primitives.

### Goals

- Admit and preempt a replicated serving group with several resource roles as one
  unit, with DisaggregatedSet as the first integration.
- Create one Workload per DisaggregatedSet slice. The Workload covers every role in
  the slice, so the slice is admitted and preempted atomically.
- Keep the user's DisaggregatedSet spec untouched by Kueue after creation. Kueue
  must not trigger a DisaggregatedSet rollout by itself, except when a
  DisaggregatedSet becomes managed or the set of stamped annotations changes, as
  listed in [Changes to the stamped annotations start a rollout](#changes-to-the-stamped-annotations-start-a-rollout).
- Support the following operations on a managed DisaggregatedSet:
  - template updates that keep the Pod shape, such as a new image;
  - changing `spec.slices`;
  - scaling every role to zero and back.
- Support Topology Aware Scheduling (TAS) on the role pod templates.
- Document which DisaggregatedSet features are supported, which are rejected, and
  the plan for each rejected feature.
- Define the criteria to graduate the integration from alpha to beta.

### Non-Goals

- Changing the DisaggregatedSet API. The design works with the API as released in
  LWS v1.0.0.
- Supporting roles autoscaled through `DisaggregatedSetRoleScaler`
  (`scaling.mode: External`) in alpha.
- Supporting `subRoles`, `groupIdentity: Hash`, LWS native gang scheduling, or
  rollouts with `maxSurge > 0` in alpha.
- Managing the child LWS objects of a managed DisaggregatedSet on their own. Kueue
  labels in role metadata and templates are rejected, as described in
  [Handling of child objects](#handling-of-child-objects).
- Partial admission inside a slice, or resizing one role of an admitted slice.
- MultiKueue support in alpha.
- Translating the DisaggregatedSet `placementPolicy` into TAS constraints in alpha.

## Proposal

Kueue creates one Workload for each slice of a DisaggregatedSet. A slice is one
complete copy of all roles. The Workload contains PodSets for every role in the
slice, so a slice is admitted as a whole and preempted as a whole.

The integration adds the Kueue DisaggregatedSet webhook and the Kueue
DisaggregatedSet reconciler. It reuses the
pod-group machinery that the LeaderWorkerSet and StatefulSet integrations already
rely on. In this KEP, "DisaggregatedSet controller" means the LWS controller that
manages DisaggregatedSets. The "DisaggregatedSet planner" is the part of that
controller that decides how many groups of each revision exist during a rollout.

```
DisaggregatedSet "llm" (slices: 2)
├── slice 0
│   ├── prefill: child LWS, StatefulSets, Pods ─┐
│   └── decode:  child LWS, StatefulSets, Pods ─┴─ Workload llm-0
│                                                  PodSets: prefill-leader, prefill-worker,
│                                                           decode-leader,  decode-worker
└── slice 1
    ├── prefill: child LWS, StatefulSets, Pods ─┐
    └── decode:  child LWS, StatefulSets, Pods ─┴─ Workload llm-1
                                                   PodSets: same as llm-0
```

1. The user sets `kueue.x-k8s.io/queue-name` on the DisaggregatedSet metadata.
2. When Kueue manages the DisaggregatedSet, the Kueue DisaggregatedSet webhook
   stamps two constant annotations on every role's leader and worker pod templates.
   The annotations are
   `kueue.x-k8s.io/pod-suspending-parent: disaggregatedset.x-k8s.io/disaggregatedset`
   and `kueue.x-k8s.io/pod-group-serving: "true"`.
3. The DisaggregatedSet controller copies the templates into child LWS objects. The
   LWS controller copies them into StatefulSets, which create Pods. The existing pod
   webhook sees the parent annotation and adds the Kueue scheduling gate to each Pod.
4. The Kueue DisaggregatedSet reconciler creates one Workload per slice. It labels
   each Pod with the pod-group name, the prebuilt Workload name, the group size and
   the PodSet it belongs to.
5. When the Workload is admitted, the pod-group reconciler ungates every Pod of the
   slice. When the Workload is evicted, it deletes them. The StatefulSets recreate the
   Pods, which are gated again until the slice is re-admitted.

The granularity of one Workload per slice follows the same rule as the LWS
integration. LWS creates one Workload per LWS replica because a replica is the
smallest unit that can serve traffic by itself. In a DisaggregatedSet, a single LWS
replica of one role cannot serve without the other roles, so the smallest useful
unit is a slice.

### User Stories

All stories assume that an admin enabled the `DisaggregatedSetIntegration` feature
gate and added `disaggregatedset.x-k8s.io/disaggregatedset` to
`integrations.frameworks`. Both are off by default.

#### Story 1: Atomic admission of a disaggregated model server

As a platform engineer, I run llm-d inference services next to training jobs on a
shared GPU pool. I want each inference deployment to start only when quota is
available for both its prefill and decode roles. I do not want GPUs held by a prefill
role that cannot serve traffic.

I add `kueue.x-k8s.io/queue-name: inference` to the DisaggregatedSet. Kueue admits the
whole slice once quota for every role is available.

```yaml
apiVersion: disaggregatedset.x-k8s.io/v1
kind: DisaggregatedSet
metadata:
  name: llm
  labels:
    kueue.x-k8s.io/queue-name: inference
spec:
  slices: 1
  roles:
  - name: prefill
    spec:
      replicas: 8
      leaderWorkerTemplate:
        size: 1
        workerTemplate:
          spec:
            containers:
            - name: vllm
              image: <model-server-image>
              resources:
                limits:
                  nvidia.com/gpu: 1
  - name: decode
    spec:
      replicas: 2
      leaderWorkerTemplate:
        size: 1
        workerTemplate:
          spec:
            containers:
            - name: vllm
              image: <model-server-image>
              resources:
                limits:
                  nvidia.com/gpu: 4
```

#### Story 2: Preempting a whole inference workload

As a cluster administrator, I give training a higher WorkloadPriorityClass than
low-priority inference, and I enable preemption within the ClusterQueue, which is
off by default. When a training job needs the GPUs, I want Kueue to evict a
complete inference slice. A half-evicted inference workload would keep holding quota while
serving nothing.

```yaml
apiVersion: kueue.x-k8s.io/v1beta2
kind: ClusterQueue
spec:
  preemption:
    withinClusterQueue: LowerPriority
---
apiVersion: disaggregatedset.x-k8s.io/v1
kind: DisaggregatedSet
metadata:
  labels:
    kueue.x-k8s.io/queue-name: inference
    kueue.x-k8s.io/priority-class: low-priority
```

#### Story 3: Scaling out by slices

As a service owner, I serve more traffic by increasing `spec.slices` from 2 to 3. The
new slice waits in the queue until quota is available. The existing slices keep
serving without interruption.

```yaml
spec:
  slices: 3
```

#### Story 4: Rolling out a new model server image

As a service owner, I update the vLLM image of the decode role. The
DisaggregatedSet controller replaces groups one at a time. Kueue admits the
replacement Pods against the quota already reserved for the slice, so the rollout
needs no extra quota.

```yaml
spec:
  roles:
  - name: decode
    spec:
      leaderWorkerTemplate:
        workerTemplate:
          spec:
            containers:
            - name: vllm
              image: <new-model-server-image>
```

### Notes/Constraints/Caveats

#### Required LWS version

The integration requires LWS v1.0.0 or later, both for the vendored API types and
for the LWS controller in the cluster:

- The vendored types must include every field that the Kueue DisaggregatedSet
  webhook rejects. Kueue decodes the object into those types, so a field they lack,
  such as `subRoles` or `scalingPolicy` before v1.0.0, is silently dropped and
  cannot be rejected.
- The integration reads `status.roleStatuses[]`. An older LWS controller does not
  populate it.

The DisaggregatedSet behavior that this design relies on is summarized in
[Appendix: DisaggregatedSet behavior](#appendix-disaggregatedset-behavior).

#### Only constant values are stamped on templates

Pod templates are part of the DisaggregatedSet revision hash. The Kueue
DisaggregatedSet webhook therefore only stamps values that never change: the parent
annotation and the serving annotation. The queue name and priority class are not
written to templates. The LWS integration stamps the queue name on its pod templates
and patches Pods only to backfill objects created before that. A DisaggregatedSet
cannot carry the queue name on its templates without a rollout, so the Kueue
DisaggregatedSet reconciler patches it onto every Pod. The priority comes from the
Workload, which reads it from the DisaggregatedSet. As a result, changing the queue
name or priority class never triggers a rollout.

The mutating webhook also runs on update and re-applies the same constant values.
A client that re-applies the original manifest without the stamped annotations, as
GitOps tools do, therefore produces no template change.

#### Handling of child objects

No changes are needed in the LWS, StatefulSet or Pod integrations. Each child kind
already defers to a managed ancestor:

- **LeaderWorkerSet.** Kueue walks the owner chain of the child LWS, finds the
  managed DisaggregatedSet, and does not manage the child LWS on its own. The LWS
  webhook stamps nothing, and the LWS reconciler creates no per-replica Workloads.
- **StatefulSet.** The StatefulSet integration skips StatefulSets whose template
  carries a parent annotation of another framework.
- **Pod.** The pod webhook gates every Pod that carries the parent annotation.

This requires the DisaggregatedSet integration to be enabled.

A child LWS defers to its parent only while the DisaggregatedSet is managed.
The DisaggregatedSet controller copies role metadata, `spec.roles[].metadata`, to
the child LWS, and the leader and worker templates to the Pods.
On an unmanaged DisaggregatedSet, a `kueue.x-k8s.io/queue-name` label in those places
keeps today's behavior, described in [Motivation](#motivation): the LWS integration
manages each child LWS on its own, or the pod integration manages the Pods.
Kueue applies the feature table and label checks only to managed DisaggregatedSets,
so enabling the integration does not change this.

On a managed DisaggregatedSet, these labels have no effect, because the queue name
and priority class come from the DisaggregatedSet metadata.
Changing them still triggers a rollout, because role metadata and templates are part
of the revision hash.
This is why [Validation on create](#validation-on-create) rejects them on a managed
DisaggregatedSet.

#### Size and topology constraints

- A slice Workload is limited to 18 PodSets, 1,000 Pods of maximum membership and
  512 KiB of role templates. See [Workload PodSet limit](#workload-podset-limit).
- TAS is supported on role templates. A role template with TAS annotations cannot
  be combined with a `placementPolicy.type` other than `None`, and
  `kueue.x-k8s.io/podset-group-name` works only for a role with a leader template,
  `size > 1` and `replicas: 1`. See
  [Placement policy and Topology Aware Scheduling](#placement-policy-and-topology-aware-scheduling).
- TAS rank ordering is not supported in alpha. See
  [TAS rank ordering](#tas-rank-ordering).

#### Supported and unsupported features

| DisaggregatedSet feature | Alpha | Plan |
| :--- | :--- | :--- |
| Static roles, 1 to 10 | Supported | — |
| Leader and worker templates, `size` | Supported | `size` and leader-template presence are immutable while managed. |
| `spec.slices` | Supported | One Workload per slice. Scaling slices creates or deletes Workloads. |
| Scaling all roles to zero and back | Supported | Workloads are deleted at zero and recreated on scale-up. |
| Changing one role's `replicas` while running | Rejected | Beta candidate. Requires resizing an admitted Workload. |
| Template updates that keep the Pod shape | Supported | Requires `maxSurge: 0`. |
| Template updates that change the Pod shape | Rejected | Same rule as the LWS integration. |
| Rollouts with `maxSurge > 0` | Rejected | Beta candidate. Requires quota for surge groups. The LWS default is `maxSurge: 0`, so only roles that opt into surge are rejected. |
| `spec.scalingPolicy.scaleDownDuringRollout` | Supported | Affects only scale-to-zero during a rollout: `Deferred` keeps old groups until they drain, while their quota is already released. See [Scale-down releases quota before Pods terminate](#scale-down-releases-quota-before-pods-terminate). |
| `startupPolicy: LeaderCreated` and `LeaderReady` | Supported | Same handling as LWS. |
| `scaling.mode: External` with `DisaggregatedSetRoleScaler` | Rejected | Beta candidate. LWS v1.0.0 reports the scaler target in `status.roleStatuses[].desiredReplicas`. See [Graduation Criteria](#graduation-criteria). |
| `subRoles` | Rejected | Re-evaluated for beta. Sub-roles relabel live Pods and change counts outside the spec. |
| LWS native gang scheduling, role `spec.scheduling` | Rejected | Re-evaluated for beta. LWS adds its own PodGroups through the Kubernetes Workload API, and its own scheduling gates, which are untested together with Kueue gating. |
| `leaderWorkerTemplate.maxGroupRestarts` and `restartBackoff` | Supported | A group that exhausts its restart budget keeps its Pods terminal. The slice Workload stays admitted, reports `WaitingForReplacementPods`, and holds quota until the group is recovered. |
| `podTerminationPolicy` | Supported | No Kueue-specific handling. |
| `groupIdentity: Hash` | Rejected | Re-evaluated together with Hash support in the LWS integration. |
| `placementPolicy` without TAS | Supported | Affinity is enforced by kube-scheduler only. |
| `placementPolicy` with TAS | Rejected | Beta candidate: slice co-location through TAS with `placementPolicy: None`. Spread across slices needs a new TAS capability. |

### Risks and Mitigations

#### The DisaggregatedSet API changes quickly

The DisaggregatedSet API (`disaggregatedset.x-k8s.io/v1`) still gains fields between
LWS releases. LWS v1.0.0 added `subRoles`, `scalingPolicy`, `maxSurge`,
`maxUnavailable` and `status.roleStatuses[].desiredReplicas`, and new LWS template
fields. A future LWS release can add a field that the vendored types do not know.
Kueue cannot reject such a field until it vendors that release.

Mitigations:
- The integration is alpha and disabled by default.
- Supported LWS versions are documented.
- Kueue vendors the LWS version that it validates against, starting with v1.0.0.
- Every `sigs.k8s.io/lws` bump re-runs the DisaggregatedSet e2e suite and re-checks
  the [feature table](#supported-and-unsupported-features).

#### Pod adoption

The Kueue DisaggregatedSet reconciler selects Pods by the
`disaggregatedset.x-k8s.io/name` label, which any user who can create Pods in the
namespace can set. A Pod must not join an admitted slice and get ungated by carrying
that label alone. The Kueue DisaggregatedSet reconciler only adopts a Pod whose
controller owner chain reaches the DisaggregatedSet through one of its child LWS
objects, with every link verified by UID. Leader Pods are owned by the leader
StatefulSet, which is owned by the LWS. Worker Pods are owned by a worker
StatefulSet, which is owned by the leader Pod.

An owner reference does not prove where a Pod came from: a user can create a Pod whose
owner reference points to a real StatefulSet. The Kueue DisaggregatedSet reconciler
therefore also checks that:
- the Pod name is `<statefulset-name>-<ordinal>`, with an ordinal below the
  StatefulSet replicas;
- the slice, role and revision labels match the child LWS.

A Pod that fails a check is not adopted and stays gated. The Kueue DisaggregatedSet
reconciler emits a `PodNotAdopted` event on the Pod that names the failed check.

The quota of a slice is charged from the role templates. The Kueue
DisaggregatedSet reconciler does not compare an adopted Pod's requests with them.
The pod-group reconciler does, under the `PodIntegrationVerifyRoleRequests`
feature gate, which is beta and enabled by default in v0.21. Before it ungates a
Pod, it checks the Pod's requests against the effective reservation of the PodSet
that the Pod's role hash names. A Pod that requests more stays gated, and the
pod-group reconciler emits a `PodExceedsRoleRequests` warning event on it. This
covers both a forged Pod and a Pod that a mutating webhook, such as a sidecar
injector, made larger than the template. Such a Pod blocks only its own slot: the
slice Workload stays admitted and its other Pods run. Users who inject sidecars
add the sidecar requests to the role templates. The LWS and StatefulSet
integrations behave the same way.

When an admin disables `PodIntegrationVerifyRoleRequests`, the pod-group reconciler
ungates these Pods, and they run beyond the quota recorded on the Workload. Use
`ResourceQuota` to bound total namespace usage; `LimitRange` can bound individual
Pods or containers.

The adoption checks are not proof of origin. A user who can create LWS objects in the
namespace can create a child LWS owned by a real DisaggregatedSet, and its Pods pass
every check. Kueue cannot prevent this without help from LWS or an admission-time
identity mechanism. The integration therefore assumes that users who share a
namespace are inside the same administrative boundary. Within a namespace, one user
can take or disrupt the admitted slots of another user's DisaggregatedSet, and can
run Pods in those slots that request more than the slice reserved. The LWS and
StatefulSet integrations make the same assumption.
For the same reason, a user can create a Pod that carries the DisaggregatedSet
labels and parent annotation, with an owner reference to an object that does not
exist. Kueue treats it like a Pod whose owner is gone and ungates it, so it runs
without quota. With `manageJobsWithoutQueueName`, this lets a user in the namespace
bypass the policy that every Pod goes through Kueue. The LWS integration has the same
behavior.

#### Large DisaggregatedSets create many Pods and Workloads

The design allows at most 100 slices of 1,000 Pods, so 100,000 Pods for one
DisaggregatedSet. There is no lower total limit, because it would be a policy
without scale data to support it. Admins limit the Pod count of a namespace with a
`ResourceQuota`. A lower total limit is revisited if scale tests show a controller
problem.

#### Rollout churn

During a rollout, a new-revision Pod can appear while the old Pod it replaces is
still terminating. The pod group counts the terminating Pod as active, so it deletes
the newest Pod as excess, and the StatefulSet recreates it. The LWS integration
already behaves this way when a group is replaced. The cost is extra Pod churn and
`ExcessPodDeleted` events, not extra quota. The e2e tests cover a full image rollout.

#### Interaction with the planner's unschedulable fallback

The DisaggregatedSet planner releases old capacity when a target Pod has been
`PodScheduled=False/Unschedulable` for one minute. Kueue-gated Pods report the reason
`SchedulingGated`, not `Unschedulable`, so gating should not trigger the fallback. An
e2e test covers a rollout that waits for re-admission.

#### Rollouts stall when a group is unready

When the unavailability budget is used, for example by an old group that exhausted
`maxGroupRestarts`, the DisaggregatedSet planner creates one extra new-revision group
per role, even with `maxSurge: 0`. It then waits for that group to become Ready
before it drains more old groups. With Kueue, the extra group exceeds the role's
PodSet count, and Kueue deletes the newest gated Pods as excess. The extra group
never becomes Ready, and the rollout stays blocked with `BootstrapSurge` and
`RevisionDrainBlocked` events.

Mitigations:
- The documentation describes the symptom and asks users to make the unready group
  Ready again, for example with the `leaderworkerset.sigs.k8s.io/recover`
  annotation.
- If the old group cannot recover, for example when the rollout fixes a crashing
  version, users raise the role's `maxUnavailable` by the number of old groups
  that cannot recover.
  `rolloutStrategy` is not part of the revision hash, so this does not start
  another rollout.
  This trades availability for progress: the role can briefly have fewer Ready
  groups.
- An e2e test shows the stall and that the rollout continues after the group
  recovers.
- Quota for surge groups, a beta candidate, also covers the extra group.

#### Changes to the stamped annotations start a rollout

The Kueue DisaggregatedSet webhook re-applies the constant annotations on every
update. If the set of stamped
annotations changes, the next update of a DisaggregatedSet starts a rollout, even
if the update changes nothing else. This happens in these cases:
- a Kueue upgrade changes the stamped annotations;
- a user adds the queue name to an unmanaged DisaggregatedSet, which makes it
  managed, as described in [Webhook](#webhook);
- a namespace gains a default LocalQueue. The next update of an unmanaged
  DisaggregatedSet with no Ready replicas makes it managed and stamps its
  templates. An unmanaged DisaggregatedSet with Ready replicas stays unmanaged and
  can still be updated, as described in [Defaulting](#defaulting).

Mitigations:
- The documentation lists the stamped annotations and these triggers.
- The documentation explains that a running unmanaged DisaggregatedSet becomes
  managed by a namespace default LocalQueue only after its roles are scaled to
  zero.
- A Kueue release that changes the stamped annotations calls it out as
  `ACTION REQUIRED` in its release notes.

#### Scale-down releases quota before Pods terminate

When `spec.slices` decreases, or every role is scaled to zero, the Kueue
DisaggregatedSet reconciler deletes the slice Workloads while the DisaggregatedSet
controller deletes their LWS objects. Deleting a Workload releases its quota at once,
before the slice Pods terminate. During the termination grace period, Pods of the
removed slices and Pods of newly admitted Workloads can run at the same time, beyond
the ClusterQueue quota. The LWS integration behaves the same way on scale-down, as
[KEP-10076](/keps/10076-quota-release-strategy/README.md) documents.

Mitigations:
- The documentation describes the overcommit window.
- An e2e test shows that quota is released on scale-down and that the Pods of the
  removed slices terminate.
- Releasing quota only after the slice Pods terminate, following KEP-10076, is a
  beta criterion.

## Design Details

### Feature gate and registration

- Feature gate `DisaggregatedSetIntegration`: alpha in v0.21, default `false`.
- Framework name `disaggregatedset.x-k8s.io/disaggregatedset`, set in
  `integrations.frameworks`. It is listed commented-out in the default configuration
  and Helm values, like the integrations whose CRDs are optional.
- Enabling the integration also enables the pod integration, as for LWS.
- The integration has its own reconciler instead of the generic suspend-based job
  reconciler, like LWS, because DisaggregatedSet has no `suspend` field.
- If the DisaggregatedSet CRD is not installed, Kueue waits for it before it starts
  the Kueue DisaggregatedSet reconciler, like other integrations.

### Disabling the integration

A new check in the Configuration validation makes Kueue fail to start when
`DisaggregatedSetIntegration` is disabled while the framework is listed in
`integrations.frameworks`. No existing integration ties its framework entry to a
feature gate.
An admin who turns off the gate, or downgrades to a Kueue version without the
integration, also removes the framework from the list.

After that, the Kueue DisaggregatedSet reconciler no longer runs.
Existing Workloads stay and keep their quota.
Pods that are already labeled remain members of their pod groups, so the pod-group
reconciler still ungates and evicts them against those Workloads.
Replacement Pods cannot join their groups, because nothing labels them any more.
They still carry the parent annotation from the stamped templates.
If the pod integration is still enabled, they stay gated.
Otherwise, they run outside of Kueue quota.

Admins therefore delete each managed DisaggregatedSet before they disable the
integration, and recreate it afterwards.
Removing the queue name is rejected, and the stamped annotations would stay on the
templates anyway.
Because recovery requires deleting user objects, `kep.yaml` sets `disable-supported`
to `false` for alpha. A safe path to disable the integration is a beta criterion.

### Webhook

#### Defaulting

The mutating webhook runs on create and update:

1. It applies the namespace default LocalQueue and the default
   WorkloadPriorityClass to the DisaggregatedSet, as for other integrations.
   On update, it applies each default only while every role reports zero Ready
   replicas in the `status.roleStatuses[]` of the old object. This is the rule that
   [Validation on update](#validation-on-update) enforces for adding the queue name
   and the priority class. Without it, the default would be added to every update
   of a running unmanaged DisaggregatedSet and then rejected, including an update
   that scales every role to zero. A running unmanaged DisaggregatedSet therefore
   stays unmanaged after its namespace gains a default LocalQueue, until its roles
   are scaled to zero. The LWS webhook applies this guard to the default
   WorkloadPriorityClass but not to the default LocalQueue.
2. If Kueue manages the DisaggregatedSet, it stamps every role's leader and
   worker templates with the `pod-suspending-parent` and `pod-group-serving`
   annotations. Unlike the LWS webhook, it does not set
   `kueue.x-k8s.io/pod-index-offset`. TAS uses the offset only together with a
   Pod index label, which alpha leaves unset, so the annotation would have no
   effect. It is added with the per-group index in
   [TAS rank ordering](#tas-rank-ordering).

#### Validation on create

On create, the validating webhook checks only managed DisaggregatedSets. It runs
these checks:

- the queue name;
- the `kueue.x-k8s.io/queue-name` and `kueue.x-k8s.io/priority-class` labels are
  forbidden in role metadata and in the leader and worker templates, for the reason
  given in [Handling of child objects](#handling-of-child-objects);
- each PodSet count fits in int32, and the maximum membership of a slice and the
  size of the role templates are within the limits in
  [Workload PodSet limit](#workload-podset-limit);
- TAS annotations on each role template, using the existing TAS validation;
- the TAS group rules on the PodSets that Kueue builds, with the `role-<role>` group
  names, as in the LWS integration. Both PodSets of a group must use the same
  required or preferred topology and agree on topology spreading;
- `kueue.x-k8s.io/podset-group-name` is used only as described in
  [Placement policy and Topology Aware Scheduling](#placement-policy-and-topology-aware-scheduling);
- the 18-PodSet limit and the length of the generated PodSet names;
- the generated PodSet names are unique. For example, a role `foo` with a leader
  template and a role `foo-leader` without one would both produce `foo-leader`;
- the elastic-job annotation is forbidden;
- the unsupported features in the [feature table](#supported-and-unsupported-features)
  are rejected.

#### Validation on update

On every update, managed or not, the validating webhook applies the same queue-name
rules as the LWS integration:

- removing the queue name is rejected;
- adding or changing the queue name is allowed only while every role reports zero
  Ready replicas in the `status.roleStatuses[]` of the old object.

Adding a queue name to an unmanaged DisaggregatedSet makes it managed, and the
mutating webhook stamps its templates, which starts a rollout of every role. No Pod
is Ready at that point, so no serving capacity is lost. Pods that are running but
not yet Ready, for example while loading a model, use resources outside quota until
the rollout replaces them.

On update of a managed DisaggregatedSet, it also runs these checks:

- the same unsupported-feature, label and Pod count checks as on create;
- the same priority-class rules as the LWS integration: adding a priority class is
  rejected while any role has Ready replicas, removing it is always rejected, and
  changing it to another class is allowed;
- the set of roles is immutable, so roles cannot be added, removed or renamed;
- per role, `leaderWorkerTemplate.size` and the presence of a leader template are
  immutable;
- per role, the leader and worker Pod shapes are immutable, with the same rule as
  the LWS integration. The image, environment, command and arguments
  can change;
- per role, the TAS annotations on the leader and worker templates are immutable.
  The shape rule covers only the Pod spec, and an existing slice Workload never
  refreshes its `TopologyRequest`, so a changed annotation would leave replacement
  Pods on the old placement;
- role `replicas` may change only when the old spec or the new spec has every static
  role at zero;
- `spec.slices` may change.

### Workload construction

For each slice index `i` in `[0, slices)`, the Kueue DisaggregatedSet reconciler
ensures one Workload with the properties below.

#### Identity and ownership

- **Name.** Built from `<ds-name>-<i>` and the DisaggregatedSet UID, with the same
  naming scheme as other integrations.
- **Ownership.** An owner reference to the DisaggregatedSet that is not a
  controller reference, as in the LWS integration, so Workloads are
  garbage-collected with it. The pod-group reconciler skips a prebuilt Workload that
  another object controls. Like LWS, no finalizer is put on the
  DisaggregatedSet.
- **Labels and annotations.** The job UID label, the job-owner annotations, and
  `kueue.x-k8s.io/component-workload-index: "<i>"`. The queue name and priority come
  from the DisaggregatedSet.
- **Workload-level annotations.** The Kueue DisaggregatedSet reconciler copies the
  annotations listed in
  [Interactions with Kueue features](#interactions-with-kueue-features) to every
  slice Workload on create and update, as the LWS integration does.

#### PodSets

For each role, in spec order, the Workload has these PodSets:

- With a leader template: `<role>-leader` with count `replicas`. If `size > 1`,
  also `<role>-worker` with count `replicas × (size - 1)`. Unlike the LWS
  integration, a worker PodSet with count zero is omitted, because all roles share
  the 18-PodSet limit of one Workload.
- Without a leader template: `<role>` with count `replicas × size`.
- Pod templates are taken from the role spec. Labels and annotations prefixed with
  `leaderworkerset.sigs.k8s.io` and `disaggregatedset.x-k8s.io` are dropped, as the
  LWS integration does.
- With TAS, `TopologyRequest` is built from the role template annotations.

#### Creation, deletion and replacement

The Kueue DisaggregatedSet reconciler creates missing Workloads and deletes
Workloads for slice indexes greater than or equal to `slices`. It deletes all
Workloads when every role is scaled to zero.

The Kueue DisaggregatedSet reconciler also compares each existing slice Workload
with the PodSets it would build now: the PodSet names and counts, the Pod shape
under the rule in [Validation on update](#validation-on-update), and the
`TopologyRequest`. Permitted template changes, such as a new image or environment
variable, are ignored, so a rollout never replaces a Workload. Shape and TAS
annotations are immutable, so in practice only the counts can differ. This happens
on a quick scale-through-zero, for example `replicas` set from N to 0 and then to M
before the Kueue DisaggregatedSet reconciler observes the zero state. Without a
replacement, the Workload would keep the counts for N, and the pod-group reconciler
would delete the Pods above those counts as excess, so the scale-up could never
complete.

When the counts differ, the Kueue DisaggregatedSet reconciler annotates the old
Workload with `kueue.x-k8s.io/podset-replacement: "true"` and sets `spec.active`
to `false`. The Workload is evicted with the reason `Deactivated`, and the
pod-group reconciler deletes the slice's Pods. Once no ungated Pod of the slice
remains, the Kueue DisaggregatedSet reconciler deletes the Workload, creates the replacement
from the current spec, and emits a `WorkloadReplaced` event on the
DisaggregatedSet. The StatefulSets recreate the Pods gated, and they join the new
Workload and wait for admission.

The annotation, not the current counts, drives a replacement once it has started.
A deactivated Workload cannot be admitted again, so a spec that reverts to the old
counts during the replacement would otherwise leave the slice inactive forever.
The replacement therefore always completes: the old Workload is deleted, and a new
one is created from whatever the spec says at that point. If every role is at zero
at that point, nothing is created. A slice Workload that a user deactivated, without
the annotation, is left alone. A user who sets the annotation and deactivates a
slice Workload triggers the same replacement, which has the effect of an eviction.

#### Queue and priority updates

The Kueue DisaggregatedSet reconciler updates the queue name of existing Workloads.
A Workload that has reserved quota keeps its queue, because it is immutable while
quota is reserved.
Priority follows the shared priority-update rule of the job reconciler:

- a change of the priority-class label to another WorkloadPriorityClass is applied
  to every slice Workload, including those with reserved quota;
- a Workload that reserved quota without a priority class is left alone, because a
  class can no longer be added;
- the numeric value of an unchanged class is not re-resolved on ordinary
  reconciles. It is refreshed only together with a class change of a sibling
  Workload.

After a queue change, slices of one DisaggregatedSet can therefore use different
queues until the older slices are evicted or recreated. The LWS integration
behaves the same way across its per-group Workloads.

### Workload PodSet limit

A Workload can have at most 18 PodSets. A role with a leader template and
`size > 1` uses two PodSets, and any other role uses one. The Kueue DisaggregatedSet
webhook rejects a DisaggregatedSet whose slice would need more than 18 PodSets.
The DisaggregatedSet API allows up to 10 roles. Ten roles fit when at most eight of
them use two PodSets: eight such roles and two others need exactly 18.

LWS caps role `replicas` at 1,000,000 but does not cap `size`, so `replicas × size`
can overflow the int32 PodSet count.
The limit comes from the size of the slice Workload. The pod-group reconciler adds
every Pod of the slice as an owner of the Workload, before it deletes excess Pods.
During a rollout, a slice can therefore have more Pods than its PodSet counts: per
role, up to `maxUnavailable` groups that are terminating, the groups that replace
them, and one extra group from the planner, described in
[Rollouts stall when a group is unready](#rollouts-stall-when-a-group-is-unready).
The maximum membership of a slice is:

```
sum over roles of (replicas + maxUnavailable + 1) × size
```

The Kueue DisaggregatedSet webhook computes this sum in int64, with `maxUnavailable`
resolved to a group count, and rejects a DisaggregatedSet whose maximum membership
is more than 1,000 Pods. It runs the check on create and on every update, including
a change of `maxUnavailable`.

The Workload also copies the role templates, which the Pod limit does not bound.
The Kueue DisaggregatedSet webhook therefore limits the role templates, serialized
as Kueue copies them into PodSets, to 512 KiB in total. It also checks the projected
size of the slice Workload:

```
templateBytes + maxMembership × (ownerBytes + tasBytesPerPod) + headroom
  <= safeWorkloadBytes
```

- `templateBytes` is the serialized size of the role templates, at most 512 KiB.
- `maxMembership` is the maximum membership of the slice, at most 1,000 Pods.
- `ownerBytes` is one owner reference with its managed-fields entry, about 250 bytes
  for a 63-character Pod name.
- `tasBytesPerPod` is zero when no role template has a TAS annotation. Otherwise it
  is the worst-case TAS assignment of one Pod in its own domain at every level of a
  16-level Topology, the most that the Topology API allows, with 63-character
  values. This is about 1.4 KB. Kueue cannot know which Topology the slice is
  admitted to, so it assumes the worst case.
- `headroom` is fixed room for the rest of the metadata and status.
- `safeWorkloadBytes` is a size safely below the 1.5 MiB object limit.

The size test calibrates `ownerBytes`, `tasBytesPerPod`, `headroom` and
`safeWorkloadBytes`. Without TAS, a slice with 512 KiB of templates fits the
1,000-Pod limit. With TAS, the formula gives a lower Pod limit: a few hundred Pods
with 512 KiB of templates, and more with smaller templates.

Raising the limit needs a Workload that does not list every Pod as an owner, which
is a beta candidate.

### Placement policy and Topology Aware Scheduling

`spec.placementPolicy` injects pod affinity and anti-affinity rules into the child
LWS templates. The rules co-locate the roles of a slice in one topology domain and
spread slices across domains. TAS does not see these rules. If both are used,
Kueue may admit a slice into a domain that the injected affinity cannot satisfy, and
the Pods stay pending while holding quota.

In alpha, the Kueue DisaggregatedSet webhook rejects a managed DisaggregatedSet that combines TAS
annotations on a role template with a `placementPolicy.type` other than `None`.
Users choose one of two options:

- **Kueue TAS.** Set `kueue.x-k8s.io/podset-required-topology` or
  `kueue.x-k8s.io/podset-preferred-topology` on the role templates. TAS places each
  PodSet on its own.
  With `podset-required-topology`, TAS places all leaders of a role in one domain
  and all its workers in one domain. `podset-preferred-topology` prefers one domain
  but can spread a PodSet over several.
  A TAS PodSet group needs one PodSet with count 1. In a DisaggregatedSet, the
  leader PodSet has count `replicas`, so `kueue.x-k8s.io/podset-group-name` works
  only for a role with a leader template, `size > 1` and `replicas: 1`, which is
  the same shape as one LWS Workload. Both templates of the role must carry the same
  group name. The Kueue DisaggregatedSet webhook rejects any other use.
  All roles share one Workload, so the same group name on two roles would join their
  PodSets. After it checks that both templates carry the same group name, Kueue
  therefore replaces it in the Workload with `role-<role>`. Role names are unique
  and at most 63 characters, so these group names cannot collide and stay within the
  256-character limit of the Workload field. Placing each leader with its own workers needs a
  per-group index, which is the beta candidate in
  [TAS rank ordering](#tas-rank-ordering).
- **DisaggregatedSet placement policy.** Kueue only enforces quota, and the
  kube-scheduler enforces the injected affinity.

`ExclusiveSlice` has two parts, and TAS can only express the first one today:

- **Co-location inside a slice.** All roles of a slice share one domain. This maps
  to one TAS PodSet group that spans all PodSets of the slice Workload, at
  `placementPolicy.topology`. It requires lifting the TAS limit of exactly two
  PodSets per group.
- **Spread across slices.** Different slices of the same DisaggregatedSet use
  different domains. Each slice is its own Workload, and TAS has no rule that keeps
  one Workload out of the domains used by another. The DisaggregatedSet controller
  would still inject the spread anti-affinity. TAS could then pin a slice to a
  domain that the anti-affinity forbids, and the slice would stay pending while
  holding quota.

The beta plan therefore offers slice co-location through TAS with
`placementPolicy: None`. Combining TAS with `ExclusiveSlice` stays rejected until
TAS can avoid the domains used by sibling slices, which needs a separate TAS
proposal. `ExclusiveTopology` remains affinity-only, because TAS cannot express
exclusivity between Workloads.

### TAS rank ordering

In alpha, TAS is supported on role templates without rank-based ordering.
Inside a slice Workload, a role's worker PodSet contains several LWS groups. The
natural mapping is `subGroupIndexLabel: leaderworkerset.sigs.k8s.io/group-index` and
`podIndexLabel: leaderworkerset.sigs.k8s.io/worker-index`, as JobSet does with job
indexes. During a rollout, old and new revisions are separate LWS objects whose group
indexes overlap, so Kueue leaves the index labels unset.

The beta candidate is a slot index that Kueue assigns when it patches a Pod.
With `maxSurge: 0`, a new group always replaces a drained one, so it takes the freed
slot, and `podIndexLabel` points to the Kueue label.

### Pod reconciliation

The Kueue DisaggregatedSet reconciler watches Pods labeled
`disaggregatedset.x-k8s.io/name` that carry the DisaggregatedSet parent annotation.
For each Pod that passes the ownership check in [Pod adoption](#pod-adoption), it
applies one patch:

- `kueue.x-k8s.io/managed: "true"`;
- the pod-group name and the prebuilt Workload name, both set to the Workload of the
  Pod's slice, taken from the `disaggregatedset.x-k8s.io/slice` label;
- `kueue.x-k8s.io/pod-group-total-count`, set to the total Pod count of the slice;
- the role-hash annotation, set to the PodSet name. The leader is the Pod with
  `leaderworkerset.sigs.k8s.io/worker-index: "0"` when the role has a leader
  template;
- the `kueue.x-k8s.io/queue-name` label, only while the Pod still carries the
  Kueue scheduling gate. The pod webhook makes the label immutable once the Pod is
  ungated, so a patch on a running Pod would be rejected, as the LWS integration
  guards against. The Workload, not the Pod label, decides which queue a slice uses.

Ungating, eviction, excess Pod deletion and finalizer handling are done by the
existing pod-group reconciler. If the DisaggregatedSet, the child LWS or the
StatefulSet is gone, for example after a delete with `--cascade=orphan`, Kueue no
longer manages the Pods. The Kueue DisaggregatedSet reconciler then removes the Kueue
scheduling gates, so the Pods do not stay gated forever, as the LWS integration does.
This differs from a Pod that fails the
[Pod adoption](#pod-adoption) checks while its owners exist, which stays gated.

### Shared helpers

The parts of the design that do not depend on DisaggregatedSet live in the shared
job framework, so that another API with several resource roles per replica can
reuse them:

- building the PodSets of one pod-group Workload from a list of named role
  templates, with the generated-name and PodSet-limit checks of
  [Validation on create](#validation-on-create);
- naming and indexing one Workload per component of a parent object;
- the Pod patch for pod-group membership in [Pod reconciliation](#pod-reconciliation).

The DisaggregatedSet integration is their only user in alpha. The LWS integration
keeps its own code until the helpers are reviewed against a second user.

### Rollout and deletion

- **Shape-preserving rollout.** With `maxSurge: 0`, the DisaggregatedSet controller
  drains a group of the old revision before it creates a group of the new revision.
  New Pods have the same slice and role labels and the same shape, so they join the
  existing pod group and are ungated against the admitted Workload. Pods that arrive
  early are handled as described in [Rollout churn](#rollout-churn).
- **Deletion.** Workloads are garbage-collected through the owner reference.

### Interactions with Kueue features

| Kueue feature | Behavior with DisaggregatedSet |
| :--- | :--- |
| Topology Aware Scheduling | Supported on role templates, without rank-based ordering in alpha. See [Placement policy and Topology Aware Scheduling](#placement-policy-and-topology-aware-scheduling) and [TAS rank ordering](#tas-rank-ordering). |
| `waitForPodsReady` | A slice Workload is ready when every Pod of the slice is Ready, so model loading counts toward `timeout`. Rollouts and group restarts make it not ready again. `recoveryTimeout`, which defaults to `timeout`, then evicts the whole slice, including a slice whose group exhausted `maxGroupRestarts`. One unready Pod in any role evicts every role and group of the slice, where the LWS integration evicts only one group. Users set both timeouts above the model loading time. |
| `WorkloadLevelWaitForPodsReady` | With the feature gate enabled and `DisableWaitForPodsReady` disabled, the Kueue DisaggregatedSet webhook validates the workload-level `waitForPodsReady` annotation, and the Kueue DisaggregatedSet reconciler copies it to every slice Workload. Otherwise, Kueue ignores the annotation. |
| `AdmissionGatedBy` | With the feature gate enabled, the Kueue DisaggregatedSet webhook validates the admission-gate annotation, the Kueue DisaggregatedSet reconciler copies it to every slice Workload, and a slice is not admitted while the annotation is set. With the gate disabled, Kueue ignores the annotation. |
| Admission checks, such as ProvisioningRequest | Apply to the slice Workload. Their PodSet updates are applied to each Pod when it is ungated, as for other pod groups. |
| WorkloadPriorityClass and preemption | Supported. The priority class is read from the DisaggregatedSet metadata, with the same update rules as LWS. Preemption evicts the whole slice. |
| Fair sharing | No special handling. Slice Workloads are ordinary Workloads. |
| `manageJobsWithoutQueueName` | A DisaggregatedSet without a queue name in a namespace that `managedJobsNamespaceSelector` selects is managed, as for other integrations. |
| Partial admission | Not supported. A slice is admitted as a whole. |
| Elastic jobs, `kueue.x-k8s.io/elastic-job` | Rejected, as for LWS. |
| MultiKueue | Not supported in alpha. Required for beta. |
| LeaderWorkerSet, StatefulSet and Pod integrations | No changes. See [Handling of child objects](#handling-of-child-objects). |

### Observability

There are no new metrics in alpha. Admission, eviction and preemption are visible
through the standard Workload metrics and conditions. The Kueue DisaggregatedSet
reconciler emits events on the DisaggregatedSet when a Workload is created, deleted
or replaced, and when the workload-level annotations are updated. It emits a
`PodNotAdopted` event on a Pod that fails the [Pod adoption](#pod-adoption) checks.
The existing `PodExceedsRoleRequests` event of the pod-group reconciler marks an
adopted Pod that stays gated because it requests more than its PodSet reserves.
The Kueue DisaggregatedSet webhook rejects invalid DisaggregatedSets, such as one
over the PodSet limit or with an unsupported feature, so those never reach the
Kueue DisaggregatedSet reconciler.

### Configuration, manifests and documentation

- The framework name is added to the Configuration API documentation, the default
  configuration and Helm values, commented-out.
- RBAC: get, list and watch on `disaggregatedsets`. Kueue never writes the
  DisaggregatedSet or its status. The Kueue DisaggregatedSet reconciler also reads
  `leaderworkersets` and `statefulsets` for the ownership check, and patches Pods
  through the existing pod integration permissions. Editor and viewer ClusterRoles
  are added for DisaggregatedSet, as for LWS.
- Mutating and validating webhook manifests are added for kustomize and Helm.
- A documentation page under Tasks, Run covers queue selection, the feature table,
  TAS and placement policy guidance, and troubleshooting.
- The LWS documentation example that puts the queue name on the DisaggregatedSet
  metadata becomes correct with this integration. A follow-up PR in the LWS repository
  will link to the Kueue page.

### Test Plan

[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.

#### Prerequisite testing updates

- Kueue vendors `sigs.k8s.io/lws` v1.0.0.
- The e2e environment installs LWS v1.0.0, which includes the DisaggregatedSet CRD
  and controller. The e2e scripts load, install and wait for LWS when the label
  filter contains `feature:leaderworkerset`. `feature:disaggregatedset` is added to
  those three checks.
- The `feature:disaggregatedset` label is added to the label filters of
  `test-e2e-extended-shard-1` and `test-tas-e2e-extended-shard-1`, so presubmit
  runs both the regular and the TAS e2e tests.
- The shared extended e2e configuration, used by the extended and TAS extended
  suites, enables the `DisaggregatedSetIntegration` feature gate and lists
  `disaggregatedset.x-k8s.io/disaggregatedset` in `integrations.frameworks`.
  Installing LWS and extending the label filters alone leaves the integration
  disabled.
- `make dep-crds` copies the DisaggregatedSet CRD for integration tests.

#### Unit tests

- `pkg/controller/jobs/disaggregatedset`: new package. Tests cover PodSet
  construction, including a leader role with `size: 1` that produces no worker
  PodSet, per-slice Workload naming, webhook defaulting and validation,
  ownership-checked Pod adoption, and Pod labeling.
- `pkg/controller/jobframework`: tests for the [shared helpers](#shared-helpers).
- `pkg/config`: validation rejects a configuration that lists the framework while
  the `DisaggregatedSetIntegration` feature gate is disabled, and listing the
  framework enables the pod integration.

#### Integration tests

Integration tests run without the LWS controllers, so the tests create child LWS
objects, StatefulSets and Pods directly.

##### Webhook tests

In `test/integration/singlecluster/webhook/jobs`:

- defaulting stamps only the constant annotations;
- unsupported features are rejected on a managed DisaggregatedSet and accepted on
  an unmanaged one;
- a queue-name or priority-class label in role metadata or in a leader or worker
  template is rejected on a managed DisaggregatedSet and accepted on an unmanaged
  one;
- a slice whose maximum membership is more than 1,000 Pods, or with a PodSet
  count that overflows int32, is rejected, also when an update raises
  `maxUnavailable`;
- role templates larger than 512 KiB in total are rejected;
- a DisaggregatedSet whose projected slice Workload size is over the limit is
  rejected, with and without TAS annotations;
- roles whose generated PodSet names collide are rejected;
- `podset-group-name` is rejected unless the role has a leader template,
  `size > 1`, `replicas: 1` and the same group name on both templates;
- two roles that use the same group name get separate TAS groups, named
  `role-<role>`, also when a role name with a dash could otherwise form the same
  name, and a 63-character role name produces a valid group name;
- a TAS group whose two templates use different topology annotations, such as
  required on one and preferred on the other, or different topology spreading,
  is rejected;
- `pod-index-offset` is never stamped;
- adding a priority class is rejected while any role has Ready replicas, removing
  it is always rejected, and changing it is accepted;
- immutability rules for size, shape, TAS annotations, roles and replicas;
- removing the queue name is rejected, and adding or changing it is accepted only
  while every role has zero Ready replicas, on managed and unmanaged
  DisaggregatedSets;
- re-applying a manifest without the stamped annotations leaves templates unchanged;
- the workload-level `waitForPodsReady` and admission-gate annotations are
  validated on create and update when their feature gates are enabled, and
  ignored when they are disabled.

##### Controller tests

In `test/integration/singlecluster/controller/jobs/disaggregatedset`:

- one Workload per slice with the expected PodSets;
- a slice Workload at the projected size limit can still be updated with TAS
  assignments in its status. The test uses 18 PodSets with 512 KiB of templates,
  63-character Pod names, owner Pods including the planner's extra group, and a
  16-level TAS assignment with one domain per Pod. It calibrates the constants of
  the size formula, with and without TAS;
- scaling slices up and down;
- scaling to zero and back;
- a slice Workload whose PodSet counts no longer match the spec is replaced,
  and its Pods are deleted before the replacement is created. The test sets
  `replicas` from N to 0 and then to M, and reconciles only after the final
  update. A permitted template change, such as a new image, does not replace
  the Workload;
- a replacement completes when the spec reverts to the old counts while the old
  Workload is deactivated: the old Workload is deleted and an active replacement
  with the reverted counts is created. A slice Workload that is deactivated
  without the replacement annotation is left alone;
- an adopted Pod whose requests exceed the role template, for example with an
  injected container, stays gated and gets a `PodExceedsRoleRequests` event while
  the other Pods of the slice are ungated. With `PodIntegrationVerifyRoleRequests`
  disabled, the same Pod is ungated;
- the workload-level `waitForPodsReady` and admission-gate annotations reach every
  slice Workload, also after an update, when their feature gates are enabled, and
  do not reach them when the gates are disabled;
- a DisaggregatedSet becomes managed through a namespace default LocalQueue and
  through `manageJobsWithoutQueueName`, and stays unmanaged in a namespace that
  `managedJobsNamespaceSelector` does not select;
- after its namespace gains a default LocalQueue, a running unmanaged
  DisaggregatedSet accepts updates, including scaling every role to zero, and
  becomes managed on the first update after it has no Ready replicas. The same
  holds for a default WorkloadPriorityClass;
- queue name updates reach Workloads without reserved quota, and Workloads
  with reserved quota keep their queue. A priority-class change reaches every
  slice Workload, including those with reserved quota, and a Workload that
  reserved quota without a priority class does not get one;
- the queue-name label is patched onto gated Pods only, and a queue change while
  Pods are running but not Ready does not produce a Pod patch error;
- Pods with a forged `disaggregatedset.x-k8s.io/name` label are not adopted;
- Pods owned by a real StatefulSet but with an unexpected name or labels are not
  adopted, and get a `PodNotAdopted` event;
- with `waitForPodsReady`, a slice becomes ready only when all its Pods are Ready,
  and losing readiness for longer than `recoveryTimeout` evicts the whole slice;
- PodSet updates from an admission check reach the matching leader and worker
  Pods;
- gated Pods are ungated when the DisaggregatedSet, the child LWS or the
  StatefulSet is gone, one test for each, while a Pod that fails the adoption
  checks with all owners present stays gated.

#### e2e tests

`test/e2e/singlecluster/extended/disaggregatedset_test.go`:

- With quota for one slice, two DisaggregatedSets are created. One is fully running
  and the other is fully gated. No role of the second set runs.
- A higher-priority workload preempts a slice. Every Pod of every role is evicted,
  and the slice is re-admitted when quota frees up.
- Scaling `spec.slices` up and down. On scale-down, quota of the removed slices is
  released and their Pods terminate.
- Scaling all roles to zero releases quota, and scaling up re-admits.
- An image rollout completes without exceeding the admitted quota.
- An image rollout with an unready old group stalls, and continues after the group
  recovers.
- An image rollout with an old group that never becomes Ready completes after the
  role's `maxUnavailable` is raised during the stall.
- `startupPolicy: LeaderCreated` and `LeaderReady`.
- Deleting the DisaggregatedSet removes its Workloads.

`test/e2e/singlecluster/tasapi/extended/disaggregatedset_test.go`:

- A role with `replicas: 2`, `size: 3` and `podset-required-topology` on both
  templates: both leaders are in one domain, and all four workers are in one
  domain.
- A role with `replicas: 1` and `podset-group-name` on both templates: the leader
  and its workers are in the same domain.

### Graduation Criteria

#### Alpha (v0.21)

- The `DisaggregatedSetIntegration` feature gate, disabled by default.
- The scope in the [feature table](#supported-and-unsupported-features), with webhook
  rejection of every unsupported feature.
- Unit, integration and e2e tests as described in the [Test Plan](#test-plan).
- A user documentation page with the feature table.

#### Beta

- The feature gate is enabled by default.
- Feedback from at least one adopter running disaggregated inference, for example
  through llm-d.
- A MultiKueue adapter for an object with several Workloads, one per slice.
- A path to disable the integration without deleting DisaggregatedSets, so that
  `disable-supported` becomes `true`.
- A slice Workload keeps only the DisaggregatedSet as its owner instead of every
  Pod, so that the 1,000-Pod limit per slice can be raised.
- Quota of a removed slice is released only after its Pods terminate, following
  [KEP-10076](/keps/10076-quota-release-strategy/README.md).
- A resolution for each alpha-rejected feature. Each one is either implemented, or
  kept rejected with the reason documented. Candidates:
  - **External role scaling.** Read the role target from
    `status.roleStatuses[].desiredReplicas`, which LWS v1.0.0 reports for every role
    and sub-role, including the scaler target for External roles. Resize the slice
    Workload by generalizing WorkloadSlices (KEP-77) to pod-group integrations. The
    same work enables changing one role's `replicas` while running.
  - **Surge rollouts.** Reserve quota for surge groups, for example with a temporary
    Workload slice. This also covers the planner's extra group described in
    [Rollouts stall when a group is unready](#rollouts-stall-when-a-group-is-unready).
  - **`placementPolicy` with TAS.** Slice co-location through TAS with
    `placementPolicy: None`, as described in
    [Placement policy and Topology Aware Scheduling](#placement-policy-and-topology-aware-scheduling).
  - **`subRoles` and `groupIdentity: Hash`.**
- A decision on TAS rank ordering, described in [TAS rank ordering](#tas-rank-ordering).
- The [shared helpers](#shared-helpers) are reviewed against a second user, the
  LWS integration or another multi-role API.
- All known e2e flakes are fixed.

#### GA

- At least two releases at beta without significant bug reports.
- The DisaggregatedSet fields that the integration supports are stable across LWS
  releases, or the supported LWS versions are pinned and documented.
- The feature gate is locked to enabled and later removed, following the Kueue
  deprecation policy.

## Implementation History

- 2026-10-20: KEP proposed.

## Drawbacks

- Kueue maintains another integration against a fast-moving alpha API. This is
  mitigated by the alpha feature gate, the narrow scope, and an e2e run on every LWS
  version bump.
- Slice-level atomicity can still admit some slices of a multi-slice
  DisaggregatedSet and not others. Each admitted slice is fully functional, so no
  accelerator is held by an incomplete deployment.

## Alternatives

### One Workload per DisaggregatedSet

One Workload would cover every slice, so the whole DisaggregatedSet would be admitted
or rejected at once. This was rejected for these reasons:

- Slices are independent serving replicas. Requiring all of them at once delays
  serving.
- Scaling `spec.slices` would require resizing an admitted Workload.
- Preemption would always evict every slice.

### One Workload per role, or keeping per-replica LWS Workloads

These alternatives keep the roles independent, which is the problem this KEP solves.

### External framework integration

Maintainers agreed on
[#11952](https://github.com/kubernetes-sigs/kueue/issues/11952) to keep it in-tree,
behind an alpha gate, because DisaggregatedSet is part of LWS, which Kueue already
vendors and integrates.

### Adding `suspend` to DisaggregatedSet

A `spec.suspend` field would let the integration use the generic suspend-based job
reconciler. It requires an LWS
API change, and LWS itself has no suspend. The child LWS objects would still need the
pod-group mechanism. This may be revisited if LWS adds suspend semantics.

### Evicting by scaling roles to zero

Kueue could evict by writing zero into every role's `replicas` and restoring the
values on re-admission. This was rejected for these reasons:

- Replica counts apply to every slice, so one slice cannot be evicted alone.
- Kueue would rewrite the user's spec, which conflicts with GitOps tools.
- External roles keep their replica counts in a separate scaler object.

## Appendix: DisaggregatedSet behavior

This appendix summarizes the DisaggregatedSet behavior, as of LWS v1.0.0, that the
design relies on. None of it is changed by this KEP.

- Each role is backed by one child LWS per slice and per revision. The LWS name is
  `<ds>-<slice>-<revision>-<role>`. Child LWS objects and their Pods carry the labels
  `disaggregatedset.x-k8s.io/name`, `disaggregatedset.x-k8s.io/role`,
  `disaggregatedset.x-k8s.io/slice` and `disaggregatedset.x-k8s.io/revision`.
- `roles[].spec.replicas` is a per-slice count. The number of Pods of a role in one
  slice is `replicas × leaderWorkerTemplate.size`.
- DisaggregatedSet has no `suspend` field and no `/scale` subresource. The CRD
  requires that every static role has a non-zero replica count, or that every static
  role is zero.
- The DisaggregatedSet controller does not copy the DisaggregatedSet's own metadata
  labels or annotations to child objects. Kueue therefore reads the queue name and
  priority class from the DisaggregatedSet object and writes what Pods need itself.
- Every change to a role template creates new child LWS objects at a new revision.
  The old revision is drained while the new one grows, bounded per role by
  `rolloutStrategy.rollingUpdateConfiguration.maxSurge` and `maxUnavailable`. Since
  LWS v1.0.0, the CRD writes the defaults of 0 and 1 onto every role.
- The revision hash covers the pod templates. For DisaggregatedSets created with LWS
  v1.0.0 or later, it also covers role labels and annotations. Any Kueue mutation of
  them after creation would trigger a rollout.
