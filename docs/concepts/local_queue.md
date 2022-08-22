# Local Queue

A `LocalQueue` is a namespaced object that groups closely related workloads
belonging to a single tenant. A `LocalQueue` points to one [`ClusterQueue`](cluster_queue.md)
from which resources are allocated to run its workloads.

Users submit jobs to a `LocalQueue`, instead of directly to a `ClusterQueue`.
Tenants can discover which queues they can submit jobs to by listing the
local queues in their namespace. The command looks similar to the following:

```sh
kubectl get -n my-namespace localqueues
# Alternatively, use the alias `queue` or `queues`
kubectl get -n my-namespace queues
```

`queue` and `queues` are aliases for `localqueue`.
