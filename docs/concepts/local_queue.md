# Local Queue

A `LocalQueue` is a namespaced object that groups closely related Workloads
that belong to a single namespace. A namespace is typically assigned to a tenant
(team or user) of the organization. A `LocalQueue` points to one [`ClusterQueue`](cluster_queue.md)
from which resources are allocated to run its Workloads.

A `LocalQueue` definition looks like the following:

```yaml
apiVersion: kueue.x-k8s.io/v1alpha2
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
kubectl get -n team-a localqueues
# Alternatively, use the alias 'queue' or 'queues'
kubectl get -n team-a queues
```

## What's next?

- Launch a [Workload](/docs/concepts/workload.md) through a local queue
