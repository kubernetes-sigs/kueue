---
title: "Admission"
date: 
weight: 
description: >
  Kueue's admission process determines when a Job should be started.
---

Kueue's admission process determines whether a Workload can begin execution.
Admission may also be subject to the [admission gate mechanism](/docs/reference/labels-and-annotations/#kueuex-k8sioadmission-gated-by) which is in beta and enabled by default.

It involves verifying:
- logical resource availability via quota reservation
- physical resource availability via Topology-Aware Scheduling, when used,
- optional AdmissionChecks for additional admission guards.

Kueue implements this through a two-phase admission cycle: 

1. **Quota Reservation:** When a user submits a Workload, it enters a LocalQueue first. This LocalQueue points to a ClusterQueue which is responsible for managing the available resources. The Kueue checks if the targeted ClusterQueue's available quota and resource flavors can accommodate requested resources (CPU, memory, GPUs, etc.). If the quota is available, the Kueue reserves resources for this Workload and prevents other Workloads from using the same resources. This phase also includes checking the availability of physical resources when 
    Topology-Aware Scheduling is enabled.

2. **Admission Checks:** Await for [AdmissionChecks](/docs/concepts/admission_check) configured in the ClusterQueue. Checks can be either built-in such as [MultiKueue](/docs/concepts/multikueue/) or [ProvisioningRequest](/docs/concepts/admission_check/provisioning_request/), or user-created plugins.

The Workload is admitted once all [AdmissionCheckStates](/docs/concepts/admission_check/#admissioncheckstates) are in the `Ready` state.

## Failure Handling:

- For temporary issues (e.g., cloud capacity shortages):
  - The system **releases** the reserved quota immediately.
  - It **requeues** the workload.
  - It triggers exponential backoff retries via [`retryStrategy`](/docs/concepts/admission_check/provisioning_request/#retry-strategy) in [ProvisioningRequestConfig](/docs/concepts/admission_check/provisioning_request/#provisioningrequestconfig).
  - Kueue creates new `ProvisioningRequest` with `-attempt<N>` suffix each retry.

- For permanent failures:
  - The system marks the AdmissionCheck as `Rejected`
  - It evicts the Workload.
  - It releases the reserved quota. 
  - It deactivates the Workload. To requeue it, the user needs to set the `.spec.active` field to `true`.

The following diagram shows the complete admission cycle for a Workload that uses the
[`ProvisioningRequest`](/docs/concepts/admission_check/provisioning_request/) AdmissionCheck,
including the retry loop:

<div class="diagram-surface p-3 rounded-3 border text-center">
  <img src="/images/prov-req-admission-cycle.svg" class="img-fluid"
       alt="Admission cycle for a Workload that uses a ProvisioningRequest, including the retry loop after a Failed or BookingExpired request">
</div>

Because the Workload is requeued between attempts, the quota reserved for the previous
attempt is released before the next one starts. Each retry creates a new
`ProvisioningRequest` with an `-attempt<N>` suffix, and the backoff is controlled by
[`retryStrategy`](/docs/concepts/admission_check/provisioning_request/#retry-strategy).

## What's Next?

  You can read the [Concepts](/docs/concepts) section to learn how [Admission Checks](/docs/concepts/admission_check/) influence admission.
