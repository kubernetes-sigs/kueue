---
title: "Local Queue"
date: 2022-03-14
weight: 4
description: >
  A namespaced resource that groups closely related workloads belonging to a single tenant.
---

A `LocalQueue` is a namespaced object that groups closely related Workloads
that belong to a single namespace. A namespace is typically assigned to a tenant
(team or user) of the organization. A `LocalQueue` points to one [`ClusterQueue`](/docs/concepts/cluster_queue)
from which resources are allocated to run its Workloads.

A `LocalQueue` definition looks like the following:

```yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: LocalQueue
metadata:
  namespace: team-a 
  name: team-a-queue
spec:
  clusterQueue: cluster-queue 
```

Users submit jobs to a `LocalQueue`, instead of to a `ClusterQueue` directly.
Tenants can discover which queues they can submit jobs to by listing the
local queues in their namespace. The command is similar to the following:

```sh
kubectl get -n my-namespace localqueues
# Alternatively, use the alias `queue` or `queues`
kubectl get -n my-namespace queues
```

`queue` and `queues` are aliases for `localqueue`.

## What's next?

- Launch a [Workload](/docs/concepts/workload) through a local queue
- Read the [API reference](/docs/reference/kueue.v1beta1/#kueue-x-k8s-io-v1beta1-LocalQueue) for `LocalQueue`
