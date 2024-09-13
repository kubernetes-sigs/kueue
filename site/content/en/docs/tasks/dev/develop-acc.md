---
title: "Develop an AdmissionCheck Controller"
date: 2024-09-03
weight: 9
description: >
    Develop an AdmissionCheck Controller (ACC).
---

An Admission Check Controller, referred to as **ACC** in the rest of the doc, is a component that manages the AdmissionChecks associated with it (given by the corresponding value of `spec.controllerName`) and the workloads queued against ClusterQueues configured to use those AdmissionChecks.

Read [Admission Check](/docs/concepts/admission_check/) to learn more about the mechanism from a user perspective.


## Subcomponents

The ACC can be built-in into Kueue or run in a different kubernetes controller manager and should implement reconcilers for AdmissionChecks and Workloads.

### AdmissionCheck Reconciler

Monitors the AdmissionChecks in the cluster and maintains the `Active` condition of those that are associated with it (by `spec.controllerName`). 

Optionally, it can watch custom [parameters](/docs/reference/kueue.v1beta1/#kueue-x-k8s-io-v1beta1-AdmissionCheckParametersReference) objects.

The [Provisioning Admission Check Controller](/docs/admission-check-controllers/provisioning/) implements this in `pkg/controller/admissionchecks/provisioning/admissioncheck_reconciler.go`

### Workload Reconciler

It is in charge of maintaining the [AdmissionCheckStates](/docs/concepts/admission_check/#admissioncheckstates) of individual workloads
based on the ACC's custom logic. It can either allow the admission, requeue or fail the workload. 

The [Provisioning Admission Check Controller](/docs/admission-check-controllers/provisioning/) implements this in `pkg/controller/admissionchecks/provisioning/controller.go`

### Parameters Object Type
Optionally, you can define a cluster level object type to hold the Admission Check parameters specific to your implementation.
Users can reference instances of this object type in the AdmissionCheck definition in [spec.parameters](/docs/reference/kueue.v1beta1/#kueue-x-k8s-io-v1beta1-AdmissionCheckParametersReference).

For example, the [Provisioning Admission Check Controller](/docs/admission-check-controllers/provisioning/) uses [ProvisioningRequestConfig](/docs/reference/kueue.v1beta1/#kueue-x-k8s-io-v1beta1-ProvisioningRequestConfig) for this purpose.

## Utility Code

`pkg/util/admissioncheck` provides helper code that can be used to interact with AdmissionCheck's parameters and more.

## Examples

Currently there are two ACCs implemented into kueue:

- [Provisioning Admission Check Controller](/docs/admission-check-controllers/provisioning/) is implemented in `pkg/controller/admissionchecks/provisioning` and implements the Kueue's integration with [Kubernetes cluster-autoscaler](https://github.com/kubernetes/autoscaler/tree/master/cluster-autoscaler) [ProvisioningRequests](https://github.com/kubernetes/autoscaler/blob/4872bddce2bcc5b4a5f6a3d569111c11b8a2baf4/cluster-autoscaler/provisioningrequest/apis/autoscaling.x-k8s.io/v1beta1/types.go#L41).
- [MultiKueue](/docs/concepts/multikueue/), the Kueue's multi-cluster support is implemented as an AdmissionCheck Controller in `/pkg/controller/admissionchecks/multikueue`.

