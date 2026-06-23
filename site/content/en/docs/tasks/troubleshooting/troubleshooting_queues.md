---
title: "Troubleshooting Queues"
date: 2024-03-21
weight: 2
description: >
  Troubleshooting the status of a LocalQueue or ClusterQueue
---

## Why no workloads are admitted in the LocalQueue?

The status of the [LocalQueue](/docs/concepts/local_queue) includes details of any configuration problems
on the LocalQueue, as part of the `Active` condition.

Run the following command to see the status of the LocalQueue:

```bash
kubectl get localqueue -n my-namespace my-local-queue -o yaml
```

The status of the LocalQueue will be similar to the following:

```yaml
status:
  admittedWorkloads: 0
  conditions:
  - lastTransitionTime: "2024-05-03T18:57:32Z"
    message: Can't submit new workloads to clusterQueue
    reason: ClusterQueueIsInactive
    status: "False"
    type: Active
```

In the example above, the `Active` condition has status `False` because the ClusterQueue
is not active.

## Why no workloads are admitted in the ClusterQueue?

The status of the [ClusterQueue](/docs/concepts/cluster_queue) includes details of any configuration problems on
the ClusterQueue, as part of the `Active` condition.

Run the following command to see the status of the ClusterQueue:

```bash
kubectl get clusterqueue my-clusterqueue -o yaml
```

The status of the ClusterQueue will be similar to the following:

```yaml
status:
  admittedWorkloads: 0
  conditions:
  - lastTransitionTime: "2024-05-03T18:22:30Z"
    message: 'Can''t admit new workloads: FlavorNotFound'
    reason: FlavorNotFound
    status: "False"
    type: Active
```

In the example above, the `Active` condition has status `False` because the configured flavor
does not exist.
Read [Administer ClusterQueues](/docs/tasks/manage/administer_cluster_quotas) to learn how
to configure a ClusterQueue.

If the ClusterQueue is properly configured, the status will be similar to the following:

```yaml
status:
  admittedWorkloads: 1
  conditions:
  - lastTransitionTime: "2024-05-03T18:35:28Z"
    message: Can admit new workloads
    reason: Ready
    status: "True"
    type: Active
```

If the ClusterQueue has the `Active` condition with status `True`, and you still don't observe
workloads being admitted, then the problem is more likely to be in the individual workloads.
Read [Troubleshooting jobs](/docs/tasks/troubleshooting/troubleshooting_jobs) to learn why individual jobs cannot be admitted.
