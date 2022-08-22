# Concepts

This section of the documentation helps you learn about the components, APIs and
abstractions that Kueue uses to represent your cluster and workloads.

## APIs

### [Cluster Queue](cluster_queue.md)

A cluster-scoped resource that governs a pool of resources, defining usage
limits and fair sharing rules.

### [Queue](queue.md)

A namespaced resource that groups closely related workloads belonging to a
single tenant.

### [Workload](workload.md)

An application that will run to completion. It is the unit of _admission_ in
Kueue. Sometimes referred to as _job_.

### [Resource Flavor](cluster_queue.md#resourceflavor-object)

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

A group of ClusterQueues that can borrow unused quota from each other.

### Queueing

The time between a workload is created until it is admitted by a ClusterQueue.
Typically, the workload will compete with other workloads for available
quota based on the fair sharing rules of the ClusterQueue.
