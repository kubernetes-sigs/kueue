# Queue

A `Queue` is a namespaced resource that groups closely related workloads
belonging to a single tenant. A `Queue` points to one [`ClusterQueue`](cluster_queue.md)
from which resources are allocated to run its workloads.

Users submit jobs to a `Queue`, instead of directly to a `ClusterQueue`. This
allows tenants to discover which queues they can submit jobs to by listing the
queues in their namespace.
