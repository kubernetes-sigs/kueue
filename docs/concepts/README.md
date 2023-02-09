# Concepts

This section of the documentation helps you learn about the components, APIs and
abstractions that Kueue uses to represent your cluster and workloads.

## APIs

### [Resource Flavor](resource_flavor.md)

A kind or type of resource that you can define in a cluster. Typically,
this resource is associated with the characteristics of a group of Nodes. 
It could distinguish among different characteristics of resources such as 
availability, pricing, architecture, models, etc.

### [Cluster Queue](cluster_queue.md)

A cluster-scoped resource that governs a pool of resources, defining usage
limits and fair sharing rules.

### [Local Queue](local_queue.md)

A namespaced resource that groups closely related Workloads belonging to a
single tenant.

### [Workload](workload.md)

An application that will run to completion. It is the unit of _admission_ in
Kueue. Sometimes referred to as _job_.

## Glossary

### Admission

The process of admitting a Workload to start (Pods to be created). A Workload
is admitted by a ClusterQueue according to the available resources and gets
resource flavors assigned for each requested resource.

Sometimes referred to as _workload scheduling_ or _job scheduling_
(not to be confused with [pod scheduling](https://kubernetes.io/docs/concepts/scheduling-eviction/assign-pod-node/)).

### [Cohort](cluster_queue.md#cohort)

A group of ClusterQueues that can borrow unused quota from each other.

### Queueing

The time between a Workload is created until it is admitted by a ClusterQueue.
Typically, the Workload will compete with other Workloads for available
quota based on the fair sharing rules of the ClusterQueue.
