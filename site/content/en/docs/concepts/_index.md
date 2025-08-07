---
title: "Concepts"
linkTitle: "Concepts"
weight: 4
description: >
  Core Kueue Concepts
no_list: true
---

This section of the documentation helps you learn about the components, APIs and
abstractions that Kueue uses to represent your cluster and workloads.

## APIs

### [Resource Flavor](/docs/concepts/resource_flavor)

An object that you can define to describe what resources are available
in a cluster. Typically, a `ResourceFlavor` is associated with the characteristics
of a group of Nodes. It could distinguish among different characteristics of
resources such as availability, pricing, architecture, models, etc.

### [Cluster Queue](/docs/concepts/cluster_queue)

A cluster-scoped resource that governs a pool of resources, defining usage
limits and Fair Sharing rules.

### [Local Queue](/docs/concepts/local_queue)

A namespaced resource that groups closely related workloads belonging to a
single tenant.

### [Workload](/docs/concepts/workload)

An application that will run to completion. It is the unit of _admission_ in
Kueue. Sometimes referred to as _job_.

### [Workload Priority Class](/docs/concepts/workload_priority_class)

`WorkloadPriorityClass` defines a priority class for a workload,
independently from [pod priority](https://kubernetes.io/docs/concepts/scheduling-eviction/pod-priority-preemption/).
This priority value from a `WorkloadPriorityClass` is only used for managing the queueing and preemption of [Workloads](#workload).

### [Admission Check](/docs/concepts/admission_check)

A mechanism allowing internal or external components to influence the timing of workloads admission.

![Components](/images/queueing-components.svg)

### [Topology Aware Scheduling](/docs/concepts/topology_aware_scheduling)

A mechanism allowing to schedule Workloads optimizing Pod placement for
network throughput between the Pods.


## Glossary

### Quota Reservation

_Quota reservation_ is the process during through which the kueue scheduler locks the resources needed by a workload within the targeted
[ClusterQueues ResourceGroups](/docs/concepts/cluster_queue#resource-groups)

Quota reservation is sometimes referred to as _workload scheduling_ or _job scheduling_,
but it should not to be confused with [pod scheduling](https://kubernetes.io/docs/concepts/scheduling-eviction/assign-pod-node/).

### [Admission](docs/concepts/admission/)

_Admission_ is the process of allowing a Workload to start (Pods to be created). 

A Workload is admitted when:
- it has the `Quota Reservation` condition
- the physical Node capacity allows to run the Workload, when [Topology-Aware Scheduling](/docs/concepts/topology_aware_scheduling/) is used
- all(optional) its AdmissionCheckStates are in the`Ready` state. 

### [Cohort](/docs/concepts/cluster_queue#cohort)

A _cohort_ is a group of ClusterQueues that can borrow unused quota from each other.

### Queueing

_Queueing_ is the state of a Workload since the time it is created until Kueue admits it on a ClusterQueue.
Typically, the Workload will compete with other Workloads for available
quota based on the Fair Sharing rules of the ClusterQueue.

### [Preemption](/docs/concepts/preemption)

_Preemption_ is the process of evicting one or more admitted Workloads to accommodate another Workload.
The Workload being evicted might be of a lower priority or might be borrowing
resources that are now required by the owning ClusterQueue.

### [Fair Sharing](/docs/concepts/fair_sharing)

Mechanisms in Kueue to share quota between tenants fairly.

### [Elastic Workloads](/docs/concepts/elastic_workload)

Workload types that support dynamic scaling.

