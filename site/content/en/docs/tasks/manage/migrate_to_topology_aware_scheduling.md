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
- You can change the Kueue configuration and restart the Kueue controller
  manager, which a rollback through the feature gate requires.

## Step 1: Decide what cannot be changed later

Some TAS settings cannot be changed in place once applied. Settle them before
the first apply.

### ResourceFlavor `topologyName`

`.spec.topologyName` on a ResourceFlavor cannot be changed or unset once set.
You cannot turn TAS off for that flavor by editing it; see
[Step 5](#step-5-roll-back) for the rollback options.

### Topology levels

Choose levels whose labels every node behind the flavor has. A node that is
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
2. **Set `topologyName` on the ResourceFlavor.** On a TAS-only ClusterQueue,
   new workloads use implicit TAS from then on.
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

Reverting the change does not work: `topologyName` cannot be removed, and the
`kueue.x-k8s.io/resource-in-use` finalizer blocks deleting a ResourceFlavor
while a ClusterQueue uses it, and a Topology while a ResourceFlavor references
it. There are two ways back.

### Disable TAS through the feature gate

Set `TopologyAwareScheduling: false` and set every enabled feature gate that
depends on it to `false`; Kueue does not start while any of them is still
enabled, and the startup error lists each one. See
[Feature gates for alpha and beta features](/docs/installation/#feature-gates-for-alpha-and-beta-features).

This applies to the whole Kueue installation, not to one flavor, and restarts
the Kueue controller manager. After the restart:

- New workloads are admitted without TAS, and TAS annotations are ignored when
  scheduling.
- Running Pods of workloads admitted through TAS keep running.
- Jobs admitted through TAS keep the `kueue.x-k8s.io/topology` scheduling gate
  in their Pod template. A Pod created or recreated for such a Job while TAS is
  disabled stays `SchedulingGated` until the Workload is evicted, for example by
  `waitForPodsReady.recoveryTimeout`, and re-admitted without TAS. With
  `recoveryTimeout: 0s`, it stays gated indefinitely.

Re-enabling TAS reverses the change and needs another restart. Rehearse both
directions on a non-production cluster before you rely on them.

### Replace the ResourceFlavor

Create a new ResourceFlavor that selects the same nodes without `topologyName`,
then replace the old flavor with the new one in every ClusterQueue that lists
it. This works for one flavor at a time without restarting Kueue, with two
caveats:

- **Quota and node overlap**: Kueue tracks quota usage per flavor name. Workloads
  already admitted on the old flavor keep counting against the old flavor until
  they finish, while the new flavor starts at zero usage and (without
  `topologyName`) admits by quota alone. To avoid admitting new workloads onto
  nodes that workloads on the old flavor are still using, either drain the
  ClusterQueue before swapping the flavor or raise the new flavor's
  `nominalQuota` gradually as workloads on the old flavor finish.
- **Explicit TAS annotations**: remove any explicit TAS annotations from
  workloads that will use the new flavor; otherwise the new flavor rejects them
  with `Flavor "<name>" does not support TopologyAwareScheduling`.

Once no ClusterQueue lists the old flavor and all workloads admitted on it have
finished, you can delete the old flavor. Deleting it while workloads admitted on
it are still running causes Kueue to suspend those jobs on their next
reconcile.

## What's next

- Learn how [implicit TAS](/docs/concepts/topology_aware_scheduling/#implicit-tas-and-mixed-flavor-clusterqueues)
  interacts with mixed-flavor ClusterQueues.
- Review the [Topology mutability rules](/docs/concepts/topology/#referencing-a-topology-from-a-resourceflavor).
- Configure [waitForPodsReady](/docs/tasks/manage/setup_wait_for_pods_ready/).
- Read about [TAS drawbacks](/docs/concepts/topology_aware_scheduling/#drawbacks).
