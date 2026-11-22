---
title: "Migrate an Existing Cluster to Topology-Aware Scheduling"
date: 2026-10-28
weight: 7
description: >
  Plan, roll out, and roll back Topology-Aware Scheduling on a cluster that already runs workloads.
---

This page shows how to enable
[Topology-Aware Scheduling (TAS)](/docs/concepts/topology_aware_scheduling) on
a cluster that already runs workloads through Kueue, and how to roll it back.
[Setup Topology-Aware Scheduling](/docs/tasks/manage/setup_topology_aware_scheduling/)
starts from an empty cluster; this page covers what changes when ClusterQueues,
ResourceFlavors and running workloads already exist.

The intended audience for this page are [batch administrators](/docs/tasks#batch-administrator).

## Before you begin

Make sure the following conditions are met:

- You are familiar with [Topology](/docs/concepts/topology/) objects and the
  [TAS concepts](/docs/concepts/topology_aware_scheduling).
- The `TopologyAwareScheduling` feature gate is enabled (beta, on by default).
  Steps 1 to 4 run with this gate enabled; the gate is touched only if you roll
  back through the feature gate in [Step 5](#step-5-roll-back).
- If Pods request extended resources only in `limits`, you run Kueue v0.19.10
  or later. Earlier versions can count those resources as zero after a
  controller restart and double-book nodes.

## Step 1: Decide what cannot be changed later

Some TAS settings cannot be changed in place once applied. Settle them before
the first apply.

### Choose a rollout route

`.spec.topologyName` on a ResourceFlavor cannot be changed or unset once set, so
you cannot disable TAS on a flavor by editing it in place. Choose between two
rollout routes:

- **Swap in a new ResourceFlavor**: create a new ResourceFlavor that selects the
  same nodes and sets `topologyName`, then replace the old flavor in every
  ClusterQueue that lists it. Quota is tracked per flavor name, so every
  ClusterQueue in a cohort must swap together. Workloads already admitted on the
  old flavor keep counting against it until they finish; TAS still subtracts
  their unannotated Pods' physical usage from node capacity as non-TAS usage, so
  the new flavor cannot double-book their nodes. Rollback is swapping the
  ClusterQueues back to the old flavor without touching the feature gate, with
  the caveats under [Replace the ResourceFlavor](#replace-the-resourceflavor).
  Workloads with explicit TAS annotations are refused by the non-TAS flavor, so
  this route suits ClusterQueues that rely on implicit TAS.
- **Update the existing ResourceFlavor in place**: set `topologyName` on the
  existing flavor. You create fewer objects and keep existing flavor names, while
  rolling back requires either creating a new non-TAS flavor later or disabling
  the feature gate ([Step 5](#step-5-roll-back)).

Choose the **flavor swap** when your workloads use implicit TAS and you want a
rollback path that needs no controller restart and continues to work after
`TopologyAwareScheduling` graduates to GA. Choose the **in-place update** when
keeping existing flavor names across ClusterQueues and cohorts outweighs
maintaining a second ResourceFlavor during transitions.

### Topology levels

Use only levels that every node in the flavor is labeled with. A node that is
missing the label for any level is excluded from the TAS flavor, together with
its capacity. Check the labels before you create the Topology, for example:

```bash
kubectl get nodes -l <flavor-node-label>=<value> -L <level-label-1>,<level-label-2>
```

Since Kueue v0.20, levels can be changed while workloads are admitted, as long
as `kubernetes.io/hostname` is the lowest level before and after the change; see
[Changing the levels of a Topology in use](/docs/concepts/topology/#changing-the-levels-of-a-topology-in-use).
In earlier versions the levels are immutable, so choose them before the first
apply.

### ClusterQueue flavors

Whether workloads need any change depends on the flavors each ClusterQueue
lists:

- **TAS-only ClusterQueue**: every ResourceFlavor across all
  `spec.resourceGroups` has `topologyName`. Workloads without a TAS annotation
  use [implicit TAS](/docs/concepts/topology_aware_scheduling/#implicit-tas-and-mixed-flavor-clusterqueues),
  so no workload change is needed.
- **Mixed ClusterQueue**: at least one listed flavor has no `topologyName`,
  even one with `0` quota. Workloads without a TAS annotation are refused the
  TAS flavor (`Flavor "<name>" supports only TopologyAwareScheduling`), so any
  workload that needs it must set an
  [explicit TAS annotation](/docs/concepts/topology_aware_scheduling/#user-facing-apis).

If you can, make the ClusterQueues that will use TAS flavors TAS-only, for
example by moving non-TAS flavors to a ClusterQueue of their own. This avoids
coordinating a change to every workload with the rollout.

A ClusterQueue that is TAS-only today becomes mixed the moment someone adds a
non-TAS flavor to it, and from then on unannotated workloads are no longer
admitted to the TAS flavor. If you manage ClusterQueues as code, add a check
that fails when a ClusterQueue lists both TAS and non-TAS flavors.

## Step 2: Check the cluster prerequisites

### Node autoscaling

Without TAS, Kueue admits a Workload against static `ClusterQueue` quota and
unsuspends the Job, creating unscheduled Pods that trigger the Cluster
Autoscaler. With TAS, Kueue requires existing schedulable nodes in its TAS cache
before admitting a Workload (while Jobs remain suspended and plain Pods keep the
`kueue.x-k8s.io/topology` scheduling gate), so unscheduled Pods never reach the
Cluster Autoscaler to trigger a scale-up. Either use fixed-size node pools for
TAS flavors, or provision capacity through the
[Provisioning AdmissionCheck](/docs/concepts/topology_aware_scheduling/#clusterautoscaler-support).

### Recovery from node failures

Configure [waitForPodsReady](/docs/tasks/manage/setup_wait_for_pods_ready/)
with a finite `recoveryTimeout`. A TAS workload's Pods are bound to the nodes in
its topology assignment. When one of those nodes fails and Kueue cannot replace
it (see [Hot swap support](/docs/concepts/topology_aware_scheduling/#hot-swap-support)),
eviction after `recoveryTimeout` is how the workload leaves it. It is also the recovery
path after a rollback through the feature gate ([Step 5](#step-5-roll-back)).
`recoveryTimeout: 0s` disables recovery eviction.

### Scheduler capacity

TAS adds work to each scheduling cycle. On a cluster where the Kueue scheduler
is already busy, measure how much of the time it spends in admission attempts
before you enable TAS, using `kueue_admission_attempt_duration_seconds`, and
compare it after the rollout.

## Step 3: Roll out in order

Apply the changes in this order:

1. **Create the Topology.** If a ResourceFlavor references a Topology that does
   not exist, the ClusterQueues that use the flavor become inactive with
   `TopologyNotFound` until it does.
2. **Enable TAS on the ResourceFlavor**, following the route from
   [Step 1](#choose-a-rollout-route):
   - **Flavor swap**: create the new ResourceFlavor with `topologyName` and at
     least one entry in `.spec.nodeLabels`, replace the old flavor in every
     ClusterQueue in the cohort, and delete the old flavor only after all
     workloads admitted on it have finished and you no longer want it kept for
     rollback.
   - **In-place update**: set `topologyName` on the existing ResourceFlavor. The
     flavor must have at least one entry in `.spec.nodeLabels` (which you can
     add in the same update if it is empty), or the API rejects the update.

   On a TAS-only ClusterQueue, new workloads use implicit TAS from then on.
3. **Add TAS annotations to workloads**, if you need them. Do this together with
   step 2 or after it, never before. A workload with an explicit TAS annotation
   is refused by a flavor without `topologyName`
   (`Flavor "<name>" does not support TopologyAwareScheduling`), and stays
   `Pending` until the flavor is changed. Nothing fails: the only sign is that
   message in the Workload's conditions.

Start with one cluster, and if possible with a flavor that is the only flavor
in its ClusterQueues.

## Step 4: Monitor the transition

### Workloads admitted before TAS

Workloads admitted before the change keep running where they are, and their
Pods are not bound to TAS assignments. TAS counts the usage of these Pods only
while they are scheduled and not terminated. When such a Pod terminates and is
replaced by a new Pod (for example, in a Job with `restartPolicy: Never`), TAS
treats its node as free until the replacement is scheduled, and can assign that
node to a TAS workload. kube-scheduler does not know about TAS assignments, so
it can place the replacement Pod on a node that TAS has already assigned to
another workload. The Pods of that TAS workload then stay `Pending` until the
node frees up or `waitForPodsReady` evicts the workload.

Preemption also cannot free nodes occupied by workloads admitted before TAS. On
an in-place update they remain preemption candidates, yet simulating their
removal releases no TAS node capacity because they have no `topologyAssignment`
(and on a flavor swap they count against the old flavor and are not candidates
at all), so preemptions that need their nodes fail until they finish or are
drained.

This lasts until every workload admitted before TAS has finished or been
re-admitted. To shorten it, re-admit workloads whose Pods are failing and being
recreated at the time of the change, for example by deactivating and
reactivating them with
[`kueuectl stop workload`](/docs/reference/kubectl-kueue/commands/kueuectl_stop/kueuectl_stop_workload/)
and [`kueuectl resume workload`](/docs/reference/kubectl-kueue/commands/kueuectl_resume/kueuectl_resume_workload/).

### What to watch

- Workloads that stay pending with `Flavor "<name>" supports only TopologyAwareScheduling`
  or `Flavor "<name>" does not support TopologyAwareScheduling`. These indicate
  a mixed ClusterQueue or annotations applied in the wrong order; see
  [Troubleshooting](/docs/tasks/run/topology_aware_scheduling/#troubleshooting).
- `kueue_pending_workloads` and, with the `UnadmittedWorkloadsObservability`
  feature gate, `kueue_unadmitted_workloads` by `reason`.
- `kueue_admission_wait_time_seconds` per ClusterQueue.
- `kueue_admission_attempt_duration_seconds`, against the baseline from
  [Step 2](#scheduler-capacity).
- The topology assignments themselves, in
  `.status.admission.podSetAssignments[*].topologyAssignment` on the Workload.

## Step 5: Roll back

Reverting the change in place does not work: `topologyName` cannot be removed,
and the `kueue.x-k8s.io/resource-in-use` finalizer blocks deleting a
ResourceFlavor while a ClusterQueue uses it, and a Topology while a
ResourceFlavor references it. There are two ways back.

### Replace the ResourceFlavor

If you rolled out by swapping flavors, recreate the original non-TAS
ResourceFlavor if you already deleted it, then switch every ClusterQueue in the
cohort back to it. If you updated the flavor in place, create a new
ResourceFlavor that selects the same nodes without `topologyName`, and replace
the TAS flavor in every ClusterQueue that lists it.

This works for one flavor (or cohort) at a time without restarting Kueue, keeps
`kueue.x-k8s.io/topology` ungating active for workloads already admitted through
TAS, and continues to work after `TopologyAwareScheduling` graduates to GA. Two
caveats apply:

- **Quota and node overlap**: Kueue tracks quota usage per flavor name, so every
  ClusterQueue in a cohort must swap together. Workloads already admitted on the
  TAS flavor keep counting against it until they finish, while the non-TAS
  flavor starts at zero usage (or resumes from any workloads still running on
  it) and admits by quota alone. To avoid over-admitting the node pool, either
  drain the ClusterQueues before swapping or set the non-TAS flavor's
  `nominalQuota` to a reduced value in the same update that swaps the flavor,
  then raise it gradually as TAS workloads finish. Ramping `nominalQuota` does
  not steer kube-scheduler away from a specific node, so a newly admitted
  non-TAS Pod can still take a node assigned to an unscheduled or recreated TAS
  Pod; drain before swapping if you need to avoid that race.
- **Explicit TAS annotations**: remove any explicit TAS annotations from
  workloads that will use the non-TAS flavor; otherwise the flavor rejects them
  with `Flavor "<name>" does not support TopologyAwareScheduling`.

Once no ClusterQueue lists the TAS flavor and all workloads admitted on it have
finished, you can delete that flavor (and then its Topology). Deleting a
ResourceFlavor while workloads admitted on it are still running causes Kueue to
suspend those jobs on their next reconcile.

### Disable TAS through the feature gate

This option is available only while the `TopologyAwareScheduling` feature gate
can still be disabled, and requires changing the Kueue configuration and
restarting the controller manager. Set `TopologyAwareScheduling: false` and set
every enabled feature gate that depends on it (including
`TASResidualPodAccounting`, enabled by default from v0.21) to `false`; Kueue
does not start while any of them is still enabled, and the startup error lists
each one. See
[Feature gates for alpha and beta features](/docs/installation/#feature-gates-for-alpha-and-beta-features).

This turns TAS off cluster-wide, not for a single flavor. After the restart:

- New workloads are admitted without TAS, and TAS annotations are ignored when
  scheduling.
- Running Pods of workloads admitted through TAS keep running.
- Jobs admitted through TAS keep the `kueue.x-k8s.io/topology` scheduling gate
  in their Pod template. A Pod created or recreated for such a Job while TAS is
  disabled stays `SchedulingGated` until the Workload is evicted, for example by
  `waitForPodsReady.recoveryTimeout`, and re-admitted without TAS. With
  `recoveryTimeout: 0s`, it stays gated indefinitely.

#### Re-enabling TAS after a feature-gate rollback

Re-enabling TAS requires another restart. Workloads admitted while the gate was
off have no topology assignment, even when their Pods carry TAS annotations:

- In Kueue v0.19.8 to v0.19.12 and v0.20.0 to v0.20.5, Jobs with TAS
  annotations that were admitted while the gate was off are stopped and requeued
  when TAS is enabled again. Use v0.19.13, v0.20.6, or later.
- From those versions, such Jobs keep running, yet without residual Pod
  accounting TAS does not count their Pods: they are neither non-TAS Pods nor
  covered by a topology assignment, so TAS can assign their nodes to other
  workloads. Enable `TASResidualPodAccounting` together with
  `TopologyAwareScheduling` (enabled by default from v0.21; see
  [Capacity calculation](/docs/concepts/topology_aware_scheduling/#capacity-calculation)).
- Pending Pods managed through the
  [Pod integration](/docs/tasks/run/plain_pods/) that were created while the
  gate was off have only the `kueue.x-k8s.io/admission` scheduling gate, and
  Kubernetes rejects adding `kueue.x-k8s.io/topology` when TAS later admits
  them, leaving them `SchedulingGated`. Delete pending Pods before re-enabling
  TAS so they can be recreated with both gates.

Rehearse both directions on a non-production cluster before you rely on them.

## What's next

- Learn how [implicit TAS](/docs/concepts/topology_aware_scheduling/#implicit-tas-and-mixed-flavor-clusterqueues)
  interacts with mixed-flavor ClusterQueues.
- Review the [Topology mutability rules](/docs/concepts/topology/#referencing-a-topology-from-a-resourceflavor).
- Configure [waitForPodsReady](/docs/tasks/manage/setup_wait_for_pods_ready/).
- Read about [TAS drawbacks](/docs/concepts/topology_aware_scheduling/#drawbacks).
