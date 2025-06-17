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
[ClusterQueues ResourceGroups](/site/content/en/docs/concepts/cluster_queue.md#resource-groups)

Quota reservation is sometimes referred to as _workload scheduling_ or _job scheduling_,
but it should not to be confused with [pod scheduling](https://kubernetes.io/docs/concepts/scheduling-eviction/assign-pod-node/).

### Admission

_Admission_ is the process of allowing a Workload to start (Pods to be created). Kueue uses a two-step admission cycle for Workload scheduling: 

- Quota Reservation : When a Workload is submitted, it enters a LocalQueue first. This LocalQueue points to a ClusterQueue which is responsible for managing the available resources. The Kueue scheduler checks if the resources (CPU, memory, GPUs, etc.) requested by the Workload can be satisfied using the targeted ClusterQueue's available quota and resource flavors. If the quota is available, the resources are reserved for this workload and other Workloads are prevented from using the same resources. 

- Admission Checks: After the quota is reserved, Kueue executes all AdmissionChecks configured in the ClusterQueue concurrently. These are pluggable AdmissionChecks controllers that can perform certain validations such as policy checks, compliance, etc.
These checks can be external or internal and determine if additional criteria are met before the Workload is admitted. The Workload is admitted once all its [AdmissionCheckStates](site/content/en/docs/concepts/admission_check.md) are mark `Ready`.

<h4> Example: Provisioning AdmissionCheck </h4>

Earlier, Kueue admissions were mainly based on quota checks- if sufficient quota existed, the workload is admitted. However, just because the quota (logical resource) is available, doesn't mean there are enough actual(physical) compute resources in the cluster to schedule all pods successfully. This is critical in cloud environments where autoscaling provides nodes on-demand. 

[ProvisioningRequest AdmissionCheck](keps/1136-provisioning-request-support/README.md) handles this issue by ensuring that the physical resources can be provisioned before admitting Workloads.
Kueue's enhanced admission process now consists of two sequential checks before admitting a workload:
- Quota Validation: It uses Kueue's existing quota management system to check if the workload fits within available cluster quotas. It verifies "logical" resource availability. <br>
- Capacity Guarantee: It uses Provisioning Request and [Cluster Autoscaler](https://github.com/kubernetes/autoscaler/tree/master/cluster-autoscaler) to ensure actual resources will exist. It verifies "physical" resource availability. <br> The Kueue controller creates a ProvisioningRequest object, attaches PodTemplates from the Workload, applies configuration from ProvisioningRequestConfig and sets owner reference to Workload. <br> Cluster Autoscaler receives ProvisioningRequest, checks actual cluster capacity, triggers scaling if needed and updates ProvisioningRequest status as `Provisioned=true` if capacity guaranteed, `Provisioned=false` if scaling is in progress, and `Failed=true` if cannot provision.

Lets understand this with a real-world usage - GPU Workload:

Scenario: *AI training job needing 16 GPUs*

- Step 1 (Quota Check): ClusterQueue has 32 GPU quota available, since requested is quota available, 16 GPUs are reserved.

- Step 2 (Capacity Check): A ProvisioningRequest is created for 16 GPUs. Cluster Autoscaler checks cloud provider GPU inventory and initiates scaling of 4x GPU nodes (4 GPUs each). It sets `Provisioned=true` when nodes ready.

Kueue sees the `Provisioned=true` proceeds to mark the AdmissionCheck `Ready` and admits workload

Outcome:
*Job starts immediately with all 16 GPUs available.*

<h4>Failure Handling: </h4>
If the capacity check fails, incase of temporary issues like cloud capacity shortages, exponential backoff retries is triggered while maintaining the workload's queue position and reserved quota. For permanent failures, the AdmissionCheck is marked Rejected, quota is released, and the workload is evicted, requiring user resubmission.

<!-- A Workload is admitted when it has a Quota Reservation and all its [AdmissionCheckStates](/docs/concepts/admission_check)
are `Ready`. -->

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