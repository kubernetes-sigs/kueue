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
limits and fair sharing rules.

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

## Glossary

### Quota Reservation

Sometimes referred to as _workload scheduling_ or _job scheduling_
(not to be confused with [pod scheduling](https://kubernetes.io/docs/concepts/scheduling-eviction/assign-pod-node/)).
Is the process during which the kueue scheduler locks the resources needed by a workload within the targeted [ClusterQueues ResourceGroups](/docs/concepts/cluster_queue/#resource-groups)

### Admission

The process of admitting a Workload to start (Pods to be created). A Workload
is admitted when it has a Quota Reservation and all its [AdmissionCheckStates](/docs/concepts/admission_check)
are `Ready`.

### [Cohort](/docs/concepts/cluster_queue#cohort)

A group of ClusterQueues that can borrow unused quota from each other.

### Queueing

The time between a Workload is created until it is admitted by a ClusterQueue.
Typically, the Workload will compete with other Workloads for available
quota based on the fair sharing rules of the ClusterQueue.
