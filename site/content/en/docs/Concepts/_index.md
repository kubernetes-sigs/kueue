---
title: "Concepts"
linkTitle: "Concepts"
weight: 4
description: >
  Core Kueue Concepts
---

This section of the documentation helps you learn about the components, APIs and
abstractions that Kueue uses to represent your cluster and workloads.

## Glossary

### Admission

The process of admitting a Workload to start (pods to be created). A Workload
is admitted by a ClusterQueue according to the available resources and gets
resource flavors assigned for each requested resource.

Sometimes referred to as _workload scheduling_ or _job scheduling_
(not to be confused with [pod scheduling](https://kubernetes.io/docs/concepts/scheduling-eviction/assign-pod-node/)).

### [Cohort](/docs/concepts/cluster_queue#cohort)

A group of ClusterQueues that can borrow unused quota from each other.

### Queueing

The time between a workload is created until it is admitted by a ClusterQueue.
Typically, the workload will compete with other workloads for available
quota based on the fair sharing rules of the ClusterQueue.

### [Resource Flavor](/docs/concepts/cluster_queue#resourceflavor-object)

A kind or type of resource in a cluster. It could distinguish among different characteristics of resources such as availability, pricing, architecture, models, etc.
