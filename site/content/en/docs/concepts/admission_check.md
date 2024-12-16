---
title: "Admission Check"
date: 2024-06-13
weight: 6
description: >
  Mechanism allowing internal or external components to influence the workload's admission.
---

AdmissionChecks are a mechanism that allows Kueue to consider additional criteria before admitting a Workload.
After Kueue has reserved quota for a Workload, Kueue runs all the admission checks configured
in the ClusterQueue concurrently.
Kueue can only admit a Workload when all of the AdmissionChecks have provided a positive signal for the Workload.

### API

AdmissionCheck is a non-namespaced API object used to define details about an admission check:

- `controllerName` - identifies the controller that processes the AdmissionCheck, not necessarily a Kubernetes Pod or Deployment name. Cannot be empty.
- `retryDelayMinutes` (deprecated) - specifies how long to keep the workload suspended after a failed check (after it transitioned to False). After that the check state goes to "Unknown". The default is 15 min.
- `parameters` - identifies a configuration with additional parameters for the check.

An AdmissionCheck object looks like the following:
```yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: AdmissionCheck
metadata:
  name: prov-test
spec:
  controllerName: kueue.x-k8s.io/provisioning-request
  parameters:
    apiGroup: kueue.x-k8s.io
    kind: ProvisioningRequestConfig
    name: prov-test-config
```

### Usage

Once defined, an AdmissionCheck can be referenced in the [ClusterQueue's spec](/docs/concepts/cluster_queue). All Workloads associated with the queue need to be evaluated by the AdmissionCheck's controller before being admitted.
Similarly to `ResourceFlavors`, if an `AdmissionCheck` is not found or its controller has not marked it as `Active`, the ClusterQueue will be marked as Inactive.

There are two ways of referencing AdmissionChecks in the ClusterQueue's spec:

- `.spec.admissionChecks` - is the list of AdmissionChecks that will be run for all Workloads submitted to the ClusterQueue
- `.spec.admissionCheckStrategy` - wraps the list of `admissionCheckStrategyRules` that give you more flexibility. It allows you to both run an AdmissionCheck for all Workloads or to associate an AdmissionCheck
with a specific ResourceFlavor. To specify ResourceFlavors that an AdmissionCheck should run for use the `admissionCheckStrategyRule.onFlavors` field, and if you want to run AdmissionCheck for all Workloads, simply leave the field empty.

Only one of the above-mentioned fields can be specified at the time.

#### Examples

##### Using `.spec.admissionChecks`

```yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: ClusterQueue
metadata:
  name: "cluster-queue"
spec:
<...>
  admissionChecks:
  - sample-prov
```

##### Using `.spec.admissionCheckStrategy`

```yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: ClusterQueue
metadata:
  name: "cluster-queue"
spec:
<...>
  admissionChecksStrategy:
    admissionChecks:
    - name: "sample-prov"           # Name of the AdmissionCheck to be run
      onFlavors: ["default-flavor"] # This AdmissionCheck will only run for Workloads that use default-flavor
    - name: "sample-prov-2"         # This AdmissionCheck will run for all Workloads regardless of a used ResourceFlavor
```


### AdmissionCheckStates

An AdmissionCheckState is the representation of the AdmissionCheck's state for a specific Workload.
AdmissionCheckStates are listed in the Workload's `.status.admissionCheckStates` field.

AdmissionCheck can be in one of the following states:
- `Pending` - the check still hasn't been performed/hasn't finished
- `Ready` - the check has passed
- `Retry` - the check cannot pass at the moment, it will back off (possibly allowing other to try, unblock quota) and retry.
- `Rejected` - the check will not pass in the near future. It is not worth to retry.

The status of a Workload that has `Pending` AdmissionChecks is similar to the following:
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
    state: Pending
  <...>
```

Kueue ensures that the list of the Workload's AdmissionCheckStates is in sync with the list of the Workload's ClusterQueue.
When a user adds a new AdmissionCheck, Kueue adds it to the Workload's AdmissionCheckStates with the `Pending` state.
If a Workload is admitted, adding a new AdmissionCheck does not evict the Workload.

### Admitting Workload with AdmissionChecks

Once a Workload has `QuotaReservation` condition set to `True`, and all of its AdmissionChecks are in `Ready` state the Workload will become `Admitted`.

If any of the Workload's AdmissionCheck is in the `Retry` state:
  - If `Admitted` the Workload is evicted - Workload will have an `Evicted` condition in `workload.Status.Condition` with `AdmissionCheck` as a `Reason`
  - If the Workload has `QuotaReservation` it will be released.
  - Event `EvictedDueToAdmissionCheck` is emitted

If any of the Workload's AdmissionCheck is in the `Rejected` state:
  - Workload is deactivated - [`workload.Spec.Active`](docs/concepts/workload/#active) is set to `False`
  - If `Admitted` the Workload is evicted - Workload has an `Evicted` condition in `workload.Status.Condition` with `Deactivated` as a `Reason`
  - If the Workload has `QuotaReservation` it will be released.
  - Event `AdmissionCheckRejected` is emitted

## What's next?

- Read the [API reference](/docs/reference/kueue.v1beta1/#kueue-x-k8s-io-v1beta1-AdmissionCheck) for `AdmissionCheck`
