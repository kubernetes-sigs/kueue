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

## [Provisioning AdmissionCheck ](docs/concepts/admission_check/provisioning_request)

When neither AdmissionChecks nor [TopologyAwareScheduling](docs/concepts/topology_aware_scheduling/) were configured, Admissions were mainly based on quota checks. The [ProvisioningRequest AdmissionCheck](/docs/admission-check-controllers/provisioning/) addresses this in cluster-autoscaler environments through the following sequential checks:
- First reserving ClusterQue resources (**Quota Reservation**),
- Then confirming the physical capacity via ProvisioningRequest and Cluster Autoscaler(CA) (**Capacity Guarantee**)


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

  ## What's Next?

  You can read the [Concepts](/docs/concepts) section to learn how [Admission Checks](/docs/concepts/admission_check/) influence admission.
