---
title: "Admission"
date: 
weight: 
description: >
  Kueue's admission process determines when a Job should be started.
---

Kueue's admission process determines whether a Workload can begin execution. 

It involves verifying:
- logical resource availability via quota reservation
- physical resource availability via Topology-Aware Scheduling, when used,
- optional AdmissionChecks for additional admission guards.

Kueue implements this through a two-phase admission cycle: 

1. **Quota Reservation:** When a user submits a Workload, it enters a LocalQueue first. This LocalQueue points to a ClusterQueue which is responsible for managing the available resources. The Kueue checks if the targeted ClusterQueue's available quota and resource flavors can accomodate requested resources (CPU, memory, GPUs, etc.). If the quota is available, the Kueue reserves resources for this Workload and prevents other Workloads from using the same resources. This phase also includes checking the availability of physical resources when 
    Topology-Aware Scheduling is enabled.

2. **Admission Checks:** Await for [AdmissionChecks](/docs/concepts/admission_check) configured in the ClusterQueue. can be either built-in like [MultiKueue](/docs/concepts/multikueue/) or [ProvisioningRequest](/docs/admission-check-controllers/provisioning/), or are the pluggable
controllers that can perform validations such as policy checks, compliance, etc.
The Workload is admitted once all [AdmissionCheckStates](/docs/concepts/admission_check/#admissioncheckstates) are in the `Ready` state.

## Provisioning AdmissionCheck 

When AdmissionChecks or [TopologyAwareScheduling](docs/concepts/topology_aware_scheduling/) were not configured, Admissions were mainly based on quota checks - if sufficient quota existed, Kueue admitted the Workload. While quota reservation confirmed logical resource availability, it did't guarantee that physical resources existed to schedule all Pods successfully. The [ProvisioningRequest AdmissionCheck](/docs/admission-check-controllers/provisioning/) addresses this in cluster-autoscaler environments.

Kueue's enhanced admission requires two sequential checks:

1. **Quota Reservation:** Kueue validates the resource requests against ClusterQueue's available quota and resource flavors, reserves the required resources if available and locks the quota to prevent other Workloads from claiming it. This step verifies logical resource availability.
2. **Capacity Guarantee:** This step uses ProvisioningRequest and [Cluster Autoscaler](https://github.com/kubernetes/autoscaler/tree/master/cluster-autoscaler) (CA) to verify physical resource availability. 
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

- **Step 1** *(Quota Reservation)*: ClusterQueue has 32 GPU quota available. Kueue reserves 16 GPUs from this quota.

- **Step 2** *(Admission Check)*: Kueue creates a ProvisioningRequest requesting for 16 GPUs. 
  - Cluster Autoscaler checks cloud provider GPU inventory and initiates scaling of 4x GPU nodes (4 GPUs each). It sets `Provisioned=true` when nodes are ready.

  - Kueue sees the `Provisioned=true` proceeds to mark the AdmissionCheck `Ready` and admits workload.

Outcome:
*Job starts immediately with all 16 GPUs available.*

## Failure Handling:

- For temporary issues (e.g., cloud capacity shortages):
  - The system **releases** the reserved quota immediately.
  - It **requeues** the workload.
  - It triggers exponential backoff retries via [`retryStrategy`](docs/admission-check-controllers/provisioning/#retry-strategy) in ProvisioningRequestConfig.
  - Kueue creates new `ProvisioningRequest` with `-attempt<N>` suffix each retry.

- For permanent failures:
  - The system marks the AdmissionCheck as `Rejected`
  - It evicts the Workload.
  - It releases the reserved quota. 
  - It deactivates the Workload and to requeue it, the user needs to set the `.status.active` field to `true`.
