---
title: "Run LeaderWorkerSet in Multi-Cluster"
linkTitle: "LeaderWorkerSet"
weight: 10
date: 2026-01-16
description: >
  Run a MultiKueue scheduled LeaderWorkerSet.
---

## Before you begin

Check the [MultiKueue installation guide](/docs/tasks/manage/setup_multikueue) on how to properly setup MultiKueue clusters.

## How it works

LeaderWorkerSet creates one Workload per replica, which requires special handling in MultiKueue.

All workloads from a LeaderWorkerSet are dispatched to the same worker cluster atomically:

1. Each workload waits for all workload group members to exist on the manager cluster.
2. The primary workload (index 0) nominates and dispatches to the target worker cluster.
3. Once the remote workload is admitted on the worker, the primary's `ClusterName` is set.
4. All other workload group members see the `ClusterName` and follow to the same worker cluster.
5. The LeaderWorkerSet is created on the worker cluster after all workloads are synchronized.

This ensures that all replicas of a LeaderWorkerSet run together on the same worker cluster.

## Limitation

LeaderWorkerSet does not currently support the `managedBy` field.

As a limitation of this, to get the actual status of the LeaderWorkerSet, you need to access the worker cluster.

## Example

Here is a sample LeaderWorkerSet:

{{< include "examples/serving-workloads/sample-leaderworkerset.yaml" "yaml" >}}

You can create the LeaderWorkerSet using the following command:

```sh
kubectl create -f sample-leaderworkerset.yaml
```
