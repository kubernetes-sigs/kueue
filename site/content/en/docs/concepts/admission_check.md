---
title: "Admission Check"
date: 2023-10-05
weight: 6
description: >
  A mechanism allowing internal or external components to influence the timing of
  workloads admission.
---

AdmissionChecks are a mechanism that allows Kueue to consider additional criteria before admitting a Workload.
When a ClusterQueue has AdmissionChecks configured, each of the checks has to provide a
positive signal to the Workload before it can be [Admitted](https://kueue.sigs.k8s.io/docs/concepts#admission).

## Components

You can configure the mechanism using the following components:

### Admission Check

Admission check is a non-namespaced API object used to define details about an `AdmissionCheck` like:

- **controllerName** - It's an identifier for the controller that processes this AdmissionCheck, not necessarily a Kubernetes Pod or Deployment name. Cannot be empty.
- **retryDelayMinutes** - Specifies how long to keep the workload suspended after a failed check (after it transitioned to False). After that the check state goes to "Unknown". The default is 15 min.
- **parameters** - Identifies an additional resource providing additional parameters for the check.

An AdmissionCheck object looks like the following:
```yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: AdmissionCheck
metadata:
  name: prov-test
spec:
  controllerName: kueue.x-k8s.io/provisioning-request
  retryDelayMinutes: 15
  parameters:
    apiGroup: kueue.x-k8s.io
    kind: ProvisioningRequestConfig
    name: prov-test-config
```

### ClusterQueue admissionChecks

Once defined, an AdmissionCheck can be referenced in the ClusterQueues' spec. All Workloads associated with the queue need to be evaluated by the AdmissionCheck's controller before being admitted.
Similarly to `ResourceFlavors`, if an `AdmissionCheck` is not found or its controller has not marked it as `Active`, the ClusterQueue will be marked as Inactive.

### AdmissionCheckState

AdmissionCheckState is the way the state of an AdmissionCheck for a specific Workload is tracked.

AdmissionCheckStates are listed in the Workload's `.status.admissionCheckStates` field.

The status of a Workload that has pending AdmissionChecks looks like the following:
```yaml
status:
  admission:
    <...>
  admissionChecks:
  - lastTransitionTime: "2023-10-20T06:40:14Z"
    message: ""
    name: sample-prov
    podSetUpdates:
    - annotations:
        cluster-autoscaler.kubernetes.io/consume-provisioning-request: job-prov-job-9815b-sample-prov
      name: main
    state: Ready
  <...>
```

A list of states being maintained in the Status of all the monitored Workloads.

Kueue ensures that the list of the Workloads AdmissionCheckStates is in sync with the list of its associated ClusterQueue, Kueue adds new checks with the `Pending` state.

- Once a workload has QuotaReservation and all its AdmissionChecks are in "Ready" state it will become Admitted.
- If at least one of the Workloads AdmissionCheck is in the `Retry` state.
  - If `Admitted` the workload is evicted.
  - If the workload has `QuotaReservation` it will be release released.
- If at least one of the Workloads AdmissionCheck is in the `Rejected`:
  - If `Admitted` the workload is evicted.
  - If the workload has `QuotaReservation` it will be release released.
  - The workload is marked as 'Finished' with a relevant failure message.

### Admission Check Controller

Is a component that monitors Workloads maintaining the content of its specific `admissionCheckStates` and the `Active` condition of the AdmissionChecks it's  controlling.
The logic for how an AdmissionCheck changes states is not part of Kueue.

## What's next?

- Read the [API reference](/docs/reference/kueue.v1beta1/#kueue-x-k8s-io-v1beta1-AdmissionCheck) for `AdmissionCheck`
