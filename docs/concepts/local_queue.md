# Local Queue

A `LocalQueue` is a namespaced object that groups closely related workloads
belonging to a single tenant. A `LocalQueue` points to one [`ClusterQueue`](cluster_queue.md)
from which resources are allocated to run its workloads.

```yaml
apiVersion: kueue.x-k8s.io/v1alpha2
kind: LocalQueue
metadata:
  namespace: team-a 
  name: team-a-lq
spec:
  clusterQueue: cluster-queue 
```

Users submit jobs to a `LocalQueue`, instead of to a `ClusterQueue` directly.
Tenants can discover which queues they can submit jobs to by listing the
local queues in their namespace. The command is similar to the following:

```sh
kubectl get -n my-namespace localqueues
```

## What's next?

- Launch a [workload](/docs/concepts/workload.md) against a local queue
