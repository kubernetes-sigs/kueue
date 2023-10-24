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
positive signal to the Workload before it can be [Admitted](docs/concepts/#admission).

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
Similarly to `ResourceFlavors`, if an `AdmissionCheck` is not found or its controller has not mark it as `Active`, the cluster queue will be marked as Inactive.

### AdmissionCheckState

AdmissionCheckState is the way the state of an AdmissionCkeck for a specific Workload is tracked.

AdmissionCheckStates are listed in the Workload's `.status.admissionCheckStates` field.

The status of a Workload that has pending AdmissionChecks looks like the following:
```yaml
properties:
  lastTransitionTime:
    description: lastTransitionTime is the last time the condition
      transitioned from one status to another. This should be when
      the underlying condition changed.  If that is not known, then
      using the time when the API field changed is acceptable.
    format: date-time
    type: string
  message:
    description: message is a human readable message indicating
      details about the transition. This may be an empty string.
    maxLength: 32768
    type: string
  name:
    description: name identifies the admission check.
    maxLength: 316
    type: string
  podSetUpdates:
    items:
      description: PodSetUpdate contains a list of pod set modifications
        suggested by AdmissionChecks. The modifications should be
        additive only - modifications of already existing keys or
        having the same key provided by multiple AdmissionChecks
        is not allowed and will result in failure during workload
        admission.
  <...>
      type: object
    type: array
    x-kubernetes-list-type: atomic
  state:
    description: status of the condition, one of True, False, Unknown.
    enum:
    - Pending
    - Ready
    - Retry
    - Rejected
    - PreemptionRequired
    type: string
required:
- lastTransitionTime
- message
- name
- state
type: object
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

Is a component that monitors Workloads maintaining the content of its specific `AdmissionCheckStates` and the `Active` condition of the `AdmissionCheck`s  its  controlling.
The logic for how an `AdmissionCheck` changes states is not part of Kueue.
