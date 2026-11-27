---
title: "Run DisaggregatedSet"
linkTitle: "DisaggregatedSet"
date: 2026-10-27
weight: 6
description: >
   Run a DisaggregatedSet as a Kueue-managed workload.
---

This page shows how to run a [DisaggregatedSet](https://github.com/kubernetes-sigs/lws)
with Kueue. A DisaggregatedSet runs several roles of one model server, such as
prefill and decode, as copies called slices. Kueue admits each slice as a whole:
every role of a slice runs, or none of them does.

This integration is based on the [Plain Pod Group](/docs/tasks/run/plain_pods/) integration.

This guide is for [serving users](/docs/tasks#serving-user) that have a basic understanding of Kueue.
For more information, see [Kueue's overview](/docs/overview).

{{% alert title="Alpha" color="warning" %}}
The DisaggregatedSet integration is alpha and disabled by default.
{{% /alert %}}

## Before you begin

1. Install [LeaderWorkerSet](https://lws.sigs.k8s.io/docs/installation/) v1.0.0 or later,
   which serves the `disaggregatedset.x-k8s.io/v1` API. The integration is tested with v1.0.0.

2. Enable the `DisaggregatedSetIntegration`
   [feature gate](/docs/getting-started/installation/#feature-gates-for-alpha-and-beta-features),
   and add the framework to the
   [manager configuration](/docs/installation/#install-a-custom-configured-released-version):
   ```yaml
   apiVersion: config.kueue.x-k8s.io/v1beta2
   kind: Configuration
   integrations:
     frameworks:
     - "disaggregatedset.x-k8s.io/disaggregatedset"
   ```
   Kueue does not start when the framework is listed while the feature gate is disabled.
   Listing the framework also enables the `pod` integration.

3. Check [Administer cluster quotas](/docs/tasks/manage/administer_cluster_quotas) for details on the initial Kueue setup.

{{% alert title="Note" color="primary" %}}
Before you enable the integration, list the DisaggregatedSets that already have
the `kueue.x-k8s.io/queue-name` label:

```sh
kubectl get disaggregatedsets --all-namespaces -l kueue.x-k8s.io/queue-name
```

Kueue manages them as soon as the integration is enabled. The next update of
such a DisaggregatedSet rolls out every role, also while it is running, and the
new Pods stay gated until their slice Workload is admitted.
{{% /alert %}}

## Running a DisaggregatedSet admitted by Kueue

### a. Queue selection

Set the target [local queue](/docs/concepts/local_queue) in the `metadata.labels`
section of the DisaggregatedSet:

```yaml
metadata:
  labels:
    kueue.x-k8s.io/queue-name: user-queue
```

Set the queue name and the `kueue.x-k8s.io/priority-class` label only on the
DisaggregatedSet itself. Kueue rejects them in role metadata and in the leader
and worker templates, because a change there starts a rollout.

Kueue also rejects the `kueue.x-k8s.io/elastic-job`,
`kueue.x-k8s.io/elastic-job-scale-up-strategy`, `kueue.x-k8s.io/admission-gated-by`
and `kueue.x-k8s.io/wait-for-pods-ready` annotations in role metadata. The
DisaggregatedSet controller copies role metadata to each child LeaderWorkerSet,
where Kueue could reject these annotations and block the DisaggregatedSet. Set the
admission-gate and `waitForPodsReady` annotations on the DisaggregatedSet itself.

### b. Resource needs

Kueue creates one Workload per slice. The Workload has one PodSet per role, or
two for a role with a leader template and `size > 1`: `<role>-leader` and
`<role>-worker`. The resource needs come from the role templates.

### c. Scaling

- Changing `spec.slices` creates or deletes slice Workloads. A new slice waits,
  with its Pods gated, until its Workload is admitted. The other slices keep running.
- Changing the `replicas` of a role is accepted only when every role is scaled
  to zero, or from zero. Scaling every role to zero deletes the slice Workloads
  and releases their quota.

When a slice is deleted, its quota is released at once, before its Pods
terminate. For a short time, Pods of removed slices and Pods of newly admitted
Workloads can run together beyond the ClusterQueue quota.

After you scale every role to zero, wait until the slice Workloads are deleted
before you scale up again. Kueue does not replace an existing slice Workload, so
a Workload that survives a quick scale through zero keeps its old Pod counts. See
[A scale-up through zero does not complete](#a-scale-up-through-zero-does-not-complete).

### d. Rollouts

Changing any role template starts a new revision of the whole DisaggregatedSet.
Every role of every slice rolls out at the same time. Kueue admits the
replacement Pods against the quota of each slice, so a rollout needs no extra
quota and keeps the slice Workloads. A new Pod waits, gated, until the Pod it
replaces is gone.

Kueue therefore rejects rollouts with `maxSurge` above zero, and changes to the
Pod shape, such as the resources or the number of containers. The image,
environment, command and arguments can change. A container that sets a limit
without a request for a resource uses the limit as its request, so Kueue also
rejects a change of that limit.

Since Kueue v0.21, Kueue also rejects turning an init container into a sidecar
container with `restartPolicy: Always` when that changes the effective resource
requests of the Pods. Kueue counts the defaults of the LimitRanges in the
namespace, so an init container without requests can still change them. A
limit change that keeps the effective requests is accepted, for example raising
a container limit that has no request while it stays at or below the Pod-level
limit. To turn this off, disable the `ValidateEffectiveRequestsOnPodTemplateUpdate`
[feature gate](/docs/getting-started/installation/#feature-gates-for-alpha-and-beta-features).
Kueue then rejects every change of a limit that is used as a request.

Kueue stamps two annotations on every role template of a DisaggregatedSet that
it manages: `kueue.x-k8s.io/pod-suspending-parent` and
`kueue.x-k8s.io/pod-group-serving`. A tool that re-applies the manifest without
them does not change the templates. The first update that stamps the templates
starts a rollout of every role. This happens when:

- you add the queue name to a DisaggregatedSet that Kueue did not manage;
- the namespace gains a default LocalQueue and no role of the DisaggregatedSet
  has Ready replicas;
- an admin enables the integration while the DisaggregatedSet already has a
  queue name.

### e. Preemption

Kueue preempts a whole slice: it evicts every role of the slice. Other slices of
the same DisaggregatedSet keep running.

## Example

Here is a sample DisaggregatedSet with two slices of a prefill role and a decode
role with a leader and a worker:

{{< include "examples/serving-workloads/sample-disaggregatedset.yaml" "yaml" >}}

Each slice requests 2 × 200m CPU for prefill and 100m + 200m CPU for decode.
Create it with:

```sh
kubectl create -f sample-disaggregatedset.yaml
```

## Limits and supported features

- A slice can have at most 18 PodSets.
- A slice can have at most 1,000 Pods, counting the Pods that can exist during a
  rollout. With the `TopologyAwareScheduling` feature gate enabled, which is the
  default, the limit is lower, about 750 Pods with small templates and about 435
  Pods with 512 KiB of templates, because the slice Workload must have room for
  topology assignments.
- The role templates of a DisaggregatedSet can use at most 512 KiB in total.

Kueue applies the following rules to a DisaggregatedSet that it manages:

| DisaggregatedSet feature | Support | Notes |
| :--- | :--- | :--- |
| Roles with leader and worker templates, and `size` | Supported | `size` and the presence of a leader template cannot change. |
| `spec.slices` | Supported | One Workload per slice. |
| Scaling every role to zero and back | Supported | The slice Workloads are deleted at zero. |
| Changing the `replicas` of a role while it runs | Rejected | |
| Template updates that keep the Pod shape | Supported | Requires `maxSurge: 0`, which is the default. |
| Template updates that change the Pod shape | Rejected | |
| `maxSurge` above zero | Rejected | |
| `spec.scalingPolicy.scaleDownDuringRollout` | Accepted, no effect | At scale to zero, Kueue deletes the slice Workloads and their Pods, so `Deferred` cannot keep old groups running. |
| `startupPolicy: LeaderCreated` and `LeaderReady` | Supported | |
| `scaling.mode: External` | Rejected | |
| `subRoles` | Rejected | |
| Role `spec.scheduling` | Rejected | |
| `leaderWorkerTemplate.maxGroupRestarts` and `restartBackoff` | Rejected | LWS counts the Pods that Kueue deletes on eviction as group failures. |
| `podTerminationPolicy` | Supported | |
| `groupIdentity: Hash` | Rejected | |
| `placementPolicy` | Supported without TAS | See [Configure Topology Aware Scheduling](#configure-topology-aware-scheduling). |
| `leaderworkerset.sigs.k8s.io/exclusive-topology` and `leaderworkerset.sigs.k8s.io/subgroup-exclusive-topology` annotations | Supported without TAS | Rejected on a role whose templates have TAS annotations. |
| `kueue.x-k8s.io/pod-group-fast-admission` annotation in a role template | Rejected | |

MultiKueue is not supported for DisaggregatedSets. Kueue does not reject a
DisaggregatedSet whose ClusterQueue has a MultiKueue admission check, and the
behavior of its slices is not defined.

## Configure Topology Aware Scheduling

Add the `kueue.x-k8s.io/podset-required-topology` or
`kueue.x-k8s.io/podset-preferred-topology` annotation to the role templates.
Kueue places each PodSet on its own: all leaders of a role in one domain, and
all its workers in one domain.

To place a leader with its workers, add the same `kueue.x-k8s.io/podset-group-name`
annotation to both templates of a role. This works only for a role with a leader
template, `size > 1` and `replicas: 1`.

TAS does not see the affinity rules that the DisaggregatedSet and LeaderWorkerSet
controllers add to the Pods. Kueue therefore rejects:

- a role template with TAS annotations combined with a `placementPolicy.type`
  other than `None`;
- the `leaderworkerset.sigs.k8s.io/exclusive-topology` and
  `leaderworkerset.sigs.k8s.io/subgroup-exclusive-topology` annotations, in role
  metadata or in a role template, on a role whose templates have TAS annotations.

Both checks look only at the TAS annotations of the role templates. A
ClusterQueue that has only TAS flavors assigns a topology to every Pod. In such
a ClusterQueue, a DisaggregatedSet with a placement policy or an exclusive
placement annotation is accepted, and its Pods can stay pending while the slice
holds quota.

Kueue assigns the Pods of a PodSet to the admitted domains in no fixed order.
With `kueue.x-k8s.io/podset-slice-required-topology`, the Pods that share a
domain can therefore come from different groups of the role.

The `kueue.x-k8s.io/podset-slice-size` annotation of a template can be at most
the number of Pods that the template has in one group of the role: `size` for a
role without a leader template, and `size - 1` for the workers of a role with
one. Each child LeaderWorkerSet is validated with this limit, even though Kueue
places the Pods of all groups of the role in one PodSet.

## Troubleshooting

### A running DisaggregatedSet is not managed

Kueue applies the namespace default LocalQueue and the default
WorkloadPriorityClass only while no role reports Ready replicas. A running
DisaggregatedSet becomes managed only after its roles are scaled to zero.

While the integration is enabled, Kueue does not apply the default LocalQueue to
the child LeaderWorkerSets of a DisaggregatedSet, also when Kueue does not manage
the DisaggregatedSet. Their Pods run outside Kueue unless the role metadata sets
a queue name.

### A rollout does not progress

When an old group is not Ready, the DisaggregatedSet controller creates an extra
group of the new revision and waits for it to become Ready. The extra group does
not fit the quota of the slice, so it stays gated, and the DisaggregatedSet
reports `BootstrapSurge` and `RevisionDrainBlocked` events. The rollout continues
once the old group is Ready again. If the old group cannot become Ready, for
example because the rollout fixes a crashing version, raise the role's
`maxUnavailable` by the number of old groups that cannot recover. This does not
start another rollout.

### A slice is evicted during a rollout

With [`waitForPodsReady`](/docs/tasks/manage/setup_wait_for_pods_ready), a slice
is ready only when every Pod of every role is Ready. One unready Pod evicts every
role of the slice after `recoveryTimeout`. A rollout replaces groups in every
slice at the same time, so a rollout that takes longer than `recoveryTimeout` can
evict every slice of the DisaggregatedSet at once. Set `recoveryTimeout` above
the time that a rollout of the DisaggregatedSet takes. The workload-level
`waitForPodsReady` annotation accepts at most
`waitForPodsReady.maxTimeoutOnWorkload`, which defaults to 2h.

With the `WaitForPodsReadyMaxNotReady` feature gate, you can instead keep the
slice ready while a rollout replaces groups. Set the
`kueue.x-k8s.io/pod-group-max-not-ready-count` annotation to the same value in
every leader and worker template, for example the sum over roles of
`maxUnavailable × size`. The value counts not-ready Pods across all roles of the
slice, so it also keeps the slice ready while every Pod of a small role is down.
A template without the annotation makes the slice require every Pod to be Ready.

### A slice restarts with a `WorkloadRecreated` event

In a rare race during a rollout, Kueue can count a terminating Pod and its
replacement as two members of a role and finish the slice Workload with reason
`OutOfSync`. Kueue then deletes the Pods of the slice and creates a new Workload,
which waits for admission. The slice runs again once it is admitted.

### A slice stays inactive

Kueue deactivates a slice Workload when it exceeds the `backoffLimitCount` of
`waitForPodsReady`, or when an admission check rejects it. The Workload then has
`spec.active: false`, and the Pods of the slice stay gated while the other slices
keep running. A rollout does not bring the slice back. To admit the slice again,
set `spec.active` to `true` on its Workload:

```sh
kubectl patch workloads.kueue.x-k8s.io <workload-name> -n <namespace> --type merge -p '{"spec":{"active":true}}'
```

### A scale-up through zero does not complete

When you scale every role to zero and up again before Kueue deletes the slice
Workloads, the Workloads keep the Pod counts of the old `replicas`. The Pods
above those counts are not adopted and stay gated. Scale every role to zero
again, wait until the slice Workloads are deleted, and then scale up.

### A Pod stays gated with a `PodExceedsRoleRequests` event

The Pod requests more than its role template, for example because a mutating
webhook injected a sidecar container. The other Pods of the slice are not
affected. Add the requests of the injected containers to the role templates.

### A Pod stays gated with a `PodNotAdopted` event

Kueue adopts a Pod only when its owners lead to the DisaggregatedSet: its
StatefulSet, its LeaderWorkerSet, and its DisaggregatedSet. The event names the
check that failed.

### Disabling the integration

Delete every DisaggregatedSet that Kueue manages before you disable the feature
gate or remove the framework, and recreate them afterwards.
