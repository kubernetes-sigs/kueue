---
title: "Admission Check"
linkTitle: "Admission Check"
date: 2024-06-13
weight: 6
description: >
  Mechanism allowing internal or external components to influence the workload's admission.
no_list: true
---

AdmissionChecks are a mechanism that allows Kueue to consider additional criteria before admitting a Workload.
After Kueue reserves quota for a Workload, the applicable admission checks run concurrently.
Kueue can admit the Workload only after all of those checks pass.

### API

AdmissionCheck is a non-namespaced API object used to define details about an admission check:

- `controllerName` - identifies the controller that processes the AdmissionCheck, not necessarily a Kubernetes Pod or Deployment name. Cannot be empty.
- `parameters` - identifies a configuration with additional parameters for the check.

An AdmissionCheck object looks like the following:
```yaml
apiVersion: kueue.x-k8s.io/v1beta2
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

Once defined, an AdmissionCheck can be referenced in the [ClusterQueue's spec](/docs/concepts/cluster_queue).
Similarly to `ResourceFlavors`, if an `AdmissionCheck` is not found or its controller has not marked it as `Active`, the ClusterQueue will be marked as Inactive.

In `v1beta2`, list the checks under `.spec.admissionChecksStrategy.admissionChecks`.
Set `onFlavors` on an entry to run the check only for Workloads assigned one of those
ResourceFlavors. Each flavor in `onFlavors` must also be listed in the ClusterQueue's
`resourceGroups`. Omit `onFlavors` or leave it empty to configure a queue-wide
check. Kueue can currently admit a Workload with no assigned ResourceFlavor
without waiting for a queue-wide check to pass.

#### Example

```yaml
apiVersion: kueue.x-k8s.io/v1beta2
kind: ClusterQueue
metadata:
  name: "cluster-queue"
spec:
  namespaceSelector: {}
  resourceGroups:
  - coveredResources: ["cpu"]
    flavors:
    - name: "default-flavor"
      resources:
      - name: "cpu"
        nominalQuota: 10
  admissionChecksStrategy:
    admissionChecks:
    - name: "sample-prov"           # Name of the AdmissionCheck to be run
      onFlavors: ["default-flavor"] # This AdmissionCheck will only run for Workloads that use default-flavor
    - name: "sample-prov-2"         # Queue-wide check
```


### AdmissionCheckStates

An AdmissionCheckState is the representation of the AdmissionCheck's state for a specific Workload.
AdmissionCheckStates are listed in the Workload's `.status.admissionChecks` field.

AdmissionCheck can be in one of the following states:
- `Pending` - the check still hasn't been performed/hasn't finished
- `Ready` - the check has passed
- `Retry` - the check cannot pass at the moment, it will back off (possibly allowing other to try, unblock quota) and retry.
- `Rejected` - the check will not pass in the near future. It is not worth to retry.

The status of a Workload with a `Ready` ProvisioningRequest admission check can look like this:
```yaml
status:
  admissionChecks:
  - lastTransitionTime: "2023-10-20T06:40:14Z"
    message: ""
    name: sample-prov
    podSetUpdates:
    - annotations:
        cluster-autoscaler.kubernetes.io/consume-provisioning-request: job-prov-job-9815b-sample-prov
      name: main
    state: Ready
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

- Read the [API reference](/docs/reference/kueue.v1beta2/#kueue-x-k8s-io-v1beta2-AdmissionCheck) for `AdmissionCheck`
- Learn more from the built-in [Provisioning Admission Check Controller](/docs/concepts/admission_check/provisioning_request)
