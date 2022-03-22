# Concepts

## APIs

### [Cluster Queue](cluster_queue.md)

A cluster-scoped resource that governs a pool of resources, defining usage
limits and fair sharing rules.

### [Queue](queue.md)

A namespaced resource that groups closely related workloads belonging to a
single tenant.

### [Queued Workload](queued_workload.md)

An application that will run to completion. It is the unit of _admission_ in
Kueue. Sometimes referred to as _job_.

### [Resource Flavor](cluster_queue.md#resource-flavors)

A kind or type of resource in a cluster. It could distinguish among different
characteristics of resources such as availability, pricing, architecture,
models, etc.

## Glossary

### Admission

The process of admitting a workload to start (pods to be created). A workload
is admitted by a ClusterQueue according to the available resources and gets
resource flavors assigned for each requested resource. Sometimes referred to
as _workload scheduling_ or _job scheduling_ (not to be confused with
[pod scheduling](https://kubernetes.io/docs/concepts/scheduling-eviction/assign-pod-node/)).

### [Cohort](cluster_queue.md#cohort)

A group of cluster queues that can borrow unused resources from each other.

### Queueing

The time between a workload is created until it is admitted by Cluster Queue.
Typically, the workload will compete with other workloads for available
resources based on the fair sharing rules for the Cluster Queue.

