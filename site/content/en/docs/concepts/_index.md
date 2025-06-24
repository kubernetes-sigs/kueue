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

### Admission

_Admission_ is the process of allowing a Workload to start (Pods to be created). Kueue uses a two-step admission cycle for Workload scheduling: 

- Quota Reservation : When a Workload is submitted, it enters a LocalQueue first. This LocalQueue points to a ClusterQueue which is responsible for managing the available resources. The Kueue scheduler checks if the resources (CPU, memory, GPUs, etc.) requested by the Workload can be satisfied using the targeted ClusterQueue's available quota and resource flavors. If the quota is available, the resources are reserved for this Workload and other Workloads are prevented from using the same resources. 

- Admission Checks: After the quota is reserved, Kueue executes all [AdmissionChecks](/docs/concepts/admission_check) configured in the ClusterQueue concurrently. These are pluggable controllers that can perform validations such as policy checks, compliance, etc.
These checks can be external or internal and determine if additional criteria are met before the Workload is admitted. The Workload is admitted once all its [AdmissionCheckStates](/docs/concepts/admission_check/#admissioncheckstates) are marked `Ready`.

<h4> Example: Provisioning AdmissionCheck </h4>

Without AdmissionChecks or [TopologyAwareScheduling](docs/concepts/topology_aware_scheduling/), Kueue admissions were mainly based on quota checks - if sufficient quota existed, the Workload was admitted. While quota reservation ensures logical resource availability, it doesn't guarantee physical resources exist to schedule all Pods successfully. The [ProvisioningRequest AdmissionCheck](/docs/admission-check-controllers/provisioning/) addresses this in cloud environments.

Kueue's enhanced admission requires two sequential checks:

- Quota Reservation: Kueue validates the resource requests against ClusterQueue's available quota and resource flavors, reserves the required resources if available and locks the quota to prevent other Workloads from claiming it. This step verifies logical resource availability. <br>
- Capacity Guarantee: This step uses ProvisioningRequest and [Cluster Autoscaler](https://github.com/kubernetes/autoscaler/tree/master/cluster-autoscaler) (CA) to verify physical resource availability. 
  - The Kueue controller creates a ProvisioningRequest object by attaching the Workload's PodTemplates(optionally merged via [PodSetMergePolicy](/docs/admission-check-controllers/provisioning/#podset-merge-policy)) , applying [ProvisioningRequestConfig](/docs/admission-check-controllers/provisioning/#provisioningrequest-configuration) settings, and setting owner reference to Workload.
  - Cluster Autoscaler receives ProvisioningRequest, checks actual cluster capacity, triggers scaling if needed and updates ProvisioningRequest status with this possible states: 
    - `Provisioned=true`: CA provisioned the capacity and it's ready to use
    - `Provisioned=false`: Provisioning in progress
    - `Failed=true`:  CA couldn't provision the capacity
    - `BookingExpired=true`: CA stopped booking the capacity, it will scale down if there are no Pods running on it  
    - `CapacityRevoked=true`: CA revokes the capacity, if a Workload is running on it, it will be evicted
  
    These conditions only affect non-admitted Workloads. Once admitted, they are ignored.

Let's understand this with a real-world usage - GPU Workload:

Scenario: *AI training job requiring 16 GPUs :*

- Step 1 (Quota Reservation): ClusterQueue has 32 GPU quota available. Kueue reserves 16 GPUs from this quota.

- Step 2 (Admission Check): Kueue creates a ProvisioningRequest requesting for 16 GPUs. 
  - Cluster Autoscaler checks cloud provider GPU inventory and initiates scaling of 4x GPU nodes (4 GPUs each). It sets `Provisioned=true` when nodes are ready.

  - Kueue sees the `Provisioned=true` proceeds to mark the AdmissionCheck `Ready` and admits workload.

Outcome:
*Job starts immediately with all 16 GPUs available.*

<h4> Failure Handling: </h4> 

- If the admission check fails due to temporary issues (e.g., cloud capacity shortages), the system releases the reserved quota immediately, requeues the workload, and triggers exponential backoff retries via [retryStrategy](docs/admission-check-controllers/provisioning/#retry-strategy) in ProvisioningRequestConfig.
Kueue creates new ProvisioningRequest with `-attempt<N>` suffix each retry.

- For permanent failures the AdmissionCheck is marked `Rejected`, the Workload is evicted and the quota it reserved is released. The Workload gets deactivated and to requeue it, a user needs to set `.status.active` field to `true`.

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