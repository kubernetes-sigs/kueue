# KEP-1136: ProvisioningRequest support

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories (Optional)](#user-stories-optional)
    - [Story 1](#story-1)
    - [Story 2](#story-2)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [Test Plan](#test-plan)
      - [Prerequisite testing updates](#prerequisite-testing-updates)
    - [Unit Tests](#unit-tests)
    - [Integration tests](#integration-tests)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Alternatives](#alternatives)
<!-- /toc -->

## Summary

Introduce an (AdmissionCheck)[https://github.com/kubernetes-sigs/kueue/tree/main/keps/993-two-phase-admission]
that will use (`ProvisioningRequest`)[https://github.com/kubernetes/autoscaler/blob/master/cluster-autoscaler/proposals/provisioning-request.md]
to ensure that there is enough capacity in the cluster before 
admitting a workload.

## Motivation

Currently Kueue admits workloads based on the quota check alone. 
This works reasonably well in most cases, but doesn't provide 
guarantee that an admitted workload will actually schedule 
in full in the cluster. With `ProvisioningRequest`, SIG-Autoscaling owned
(ClusterAutoscaler)[https://github.com/kubernetes/autoscaler/tree/master/cluster-autoscaler]
opens a way for stronger (but still not hard-guaranteed) all-or-nothing
scheduling in an autoscaled cloud environment. 

Before admission, CA will check whether there is enough resources and
provide them if their number is not sufficient (details
depend on the exact engine used with `ProvisioningRequest)`.

### Goals

* Provide Kueue integration with `ProvisioningRequest`.
* Define how users can configure what Kueue puts into `ProvisioningRequest`.

### Non-Goals

* Define how Cluster Autoscaler handles ProvisioningRequest.
* Define underlying cloud-specific behavior.

## Proposal

* Introduce a new controller in Kueue that will act as AdmissionCheck based on
  the status of created `ProvisioningRequest`.

* Introduce a new cluster-scoped CRD to configure how `ProvisioningRequest` should be used.


### User Stories (Optional)

#### Story 1

I want to admit workloads only after ClusterAutoscaler running on my cloud provider
expands a dedicated node group on which the workload will be run.

#### Story 2

I want to admit workloads only after a CheckCapacity request to ClusterAutoscaler
succeeds.

### Risks and Mitigations

There doesn't seem to be much risks or mitigations.
(Two phase admission process)[https://github.com/kubernetes-sigs/kueue/tree/main/keps/993-two-phase-admission]
was added specifically for use cases like this.

## Design Details

The new ProvisioningRequest controller will:

* Watch for all workloads that require an `AdmissionCheck` with controller
name set to `"ProvisioningRequestController"`. For that it will also need to
to watch all `AdmissionCheck` definitions to understand whether the particular
check is in fact `ProvisioningRequest` or not.

* For each of such workloads create a `ProvisioningRequest` (and accompanying
PodTemplates) requesting capacity for all podsets from the workload. 
The `ProvisioningRequest` should have the owner reference set to the workload.
To understand what details should it put into `ProvisioningRequest` the controller
will also need to watch `ProvisioningRequestConfigs`.

* Watch all changes CA makes to `ProvisioningRequests`. If the `Provisioned`
or `CapacityAvailable` condition is set to `True` then finish the `AdmissionCheck`
with success (and propagate the information about `ProvisioningRequest` name to
workload pods - KEP #1145 under `"cluster-autoscaler.kubernetes.io/consume-provisioning-request"`.
If the `ProvisioningRequest` fails, fail the `AdmissionCheck`.

* Watch the admission of the workload - if it is again suspended or finished, 
the provisioning request should also be deleted (the last one can be achieved via
OwnerReference).

The definition of `ProvisioningRequestConfig` is relatively simple and is based on
what can be set in `ProvisioningRequest`.

```
type ProvisioningRequestConfig struct {
    metav1.TypeMeta   `json:",inline"`
    metav1.ObjectMeta `json:"metadata,omitempty"`
    
    ProvisioningClass string
    Parameters map[string]string
} 
```

`AdmissionCheck` will point to this configuration:

```
kind: AdmissionCheck:
    Name: "SuperProvider"
    Spec:
	ControllerName: “ProvisioningRequestController”
    Parameters: 
	    ApiGroup: “kueue.x-k8s.io/v1beta1”
	    Kind: “ProvisioningRequestConfig”
	    Name: “SuperProviderConfig”

kind: ProvsioningRequestConfig:
    Name: "SuperProviderConfig"
    ProvisioningClass: "SuperSpot"
    Parameters: 
	"Priority": "TopTier"

```

### Test Plan

[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.

##### Prerequisite testing updates

None.

#### Unit Tests

Regular unit tests covering the new controller should suffice.

#### Integration tests

Integration tests should be done without actual Cluster Autoscaler running
(but with integration tests flipping the `ProvisioningRequest` state)
to cover possible error scenarios.

The tests should start with a job going to a queue with ProvisioningRequestController-based `AdmissionCheck`.
The appropriate `ProvisioningRequest` should be created, with the right `ProvisioningClass` set (taken from `ProvisioningRequestConfig`).
The following scenarios should be tested:

* `ProvisioningRequest` is completed successfully. Then:
    * Workload completes till success.
    * Workload is preempted and goes back to suspend.
    * Workload is deleted.
* `ProvisioningRequest` is failed.
*  Workload is deleted.
*  Workload is suspended.
*  Queue definition changes and doesn't require any `AdmissionChecks` anymore.
*  `ProvisioningRequestConfig` changes.
*  `ProvisioningRequestConfig` is removed.

### Graduation Criteria

User feedback is positive.

## Implementation History

2023-09-21: KEP 

## Alternatives

Not do `ProvisioningRequest` integration.