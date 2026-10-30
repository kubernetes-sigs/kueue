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

### d. Rollouts

Changing a role template starts a rollout. Kueue admits the replacement Pods
against the quota of the slice, so a rollout needs no extra quota. A new Pod
waits, gated, until the Pod it replaces is gone. Kueue therefore rejects
rollouts with `maxSurge` above zero, and changes to the Pod shape, such as the
resources or the number of containers. The image, environment, command and
arguments can change.

Kueue stamps two annotations on every role template:
`kueue.x-k8s.io/pod-suspending-parent` and `kueue.x-k8s.io/pod-group-serving`.
A tool that re-applies the manifest without them does not change the templates.

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

## Limits and unsupported features

- A slice can have at most 18 PodSets.
- A slice can have at most 1,000 Pods, counting the Pods that can exist during a
  rollout. With the `TopologyAwareScheduling` feature gate enabled, which is the
  default, the limit is lower, about 750 Pods with small templates and about 435
  Pods with 512 KiB of templates, because the slice Workload must have room for
  topology assignments.
- Kueue rejects these features on a DisaggregatedSet that it manages:
  `scaling.mode: External`, `subRoles`, role `spec.scheduling`,
  `groupIdentity: Hash`, `maxSurge` above zero, `leaderWorkerTemplate.maxGroupRestarts`
  and `leaderWorkerTemplate.restartBackoff`.

## Configure Topology Aware Scheduling

Add the `kueue.x-k8s.io/podset-required-topology` or
`kueue.x-k8s.io/podset-preferred-topology` annotation to the role templates.
Kueue places each PodSet on its own: all leaders of a role in one domain, and
all its workers in one domain.

To place a leader with its workers, add the same `kueue.x-k8s.io/podset-group-name`
annotation to both templates of a role. This works only for a role with a leader
template, `size > 1` and `replicas: 1`.

A role template with TAS annotations cannot be combined with a
`placementPolicy.type` other than `None`.

## Troubleshooting

### A running DisaggregatedSet is not managed

Kueue applies the namespace default LocalQueue and the default
WorkloadPriorityClass only while no role reports Ready replicas. A running
DisaggregatedSet becomes managed only after its roles are scaled to zero.

### A rollout does not progress

When an old group is not Ready, the DisaggregatedSet controller creates an extra
group of the new revision and waits for it to become Ready. The extra group does
not fit the quota of the slice, so it stays gated. Make the old group Ready
again, for example with the `leaderworkerset.sigs.k8s.io/recover` annotation, or
raise the role's `maxUnavailable` by the number of old groups that cannot recover.

### A slice is evicted during a rollout

With [`waitForPodsReady`](/docs/tasks/manage/setup_wait_for_pods_ready), a slice
is ready only when every Pod of every role is Ready. One unready Pod evicts every
role of the slice after `recoveryTimeout`. Set `recoveryTimeout` above the time
that a rollout of one slice takes.

### A Pod stays gated with a `PodNotAdopted` event

Kueue adopts a Pod only when its owners lead to the DisaggregatedSet: its
StatefulSet, its LeaderWorkerSet, and its DisaggregatedSet. The event names the
check that failed.

### Disabling the integration

Delete every DisaggregatedSet that Kueue manages before you disable the feature
gate or remove the framework, and recreate them afterwards.
