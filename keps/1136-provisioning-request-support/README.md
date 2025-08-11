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
    - [BookingExpired condition](#bookingexpired-condition)
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

Introduce an [AdmissionCheck](https://github.com/kubernetes-sigs/kueue/tree/main/keps/993-two-phase-admission)
that will use [`ProvisioningRequest`](https://github.com/kubernetes/autoscaler/blob/master/cluster-autoscaler/proposals/provisioning-request.md)
to ensure that there is enough capacity in the cluster before
admitting a workload.

## Motivation

Currently Kueue admits workloads based on the quota check alone.
This works reasonably well in most cases, but doesn't provide
guarantee that an admitted workload will actually schedule
in full in the cluster. With `ProvisioningRequest`, SIG-Autoscaling owned
[ClusterAutoscaler](https://github.com/kubernetes/autoscaler/tree/master/cluster-autoscaler)
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

#### BookingExpired condition

Kueue's support for the BookingExpired condition in ProvisioningRequest poses a risk. The Cluster Autoscaler may set `BookingExpired=true`,
potentially ceasing to guarantee the capacity before all pods are scheduled. This can occur in two scenarios:

- **Other AdmissionChecks**: If other AdmissionChecks are used, they might delay pod creation, causing the Cluster Autoscaler to expire the booking.
- **Massive jobs**: When a very large job is created, the controller responsible for pod creation might not be able to
  keep pace, again leading to the booking expiring before all pods are scheduled.

This could result in the scheduling of only a subset of pods. To mitigate the first scenario, users can utilize the
[`WaitForPodsReady`](https://github.com/kubernetes-sigs/kueue/tree/main/keps/349-all-or-nothing) field. This ensures
a Workload is evicted if not all of its pods are scheduled after a specified timeout. For the second scenario, cluster
administrators should ensure their control plane is adequately provisioned with sufficient resources - larger VMs and/or
higher qps for the pod-creating controller) to handle large jobs efficiently.

## Design Details

The new ProvisioningRequest controller will:

* Watch for all workloads that require an `AdmissionCheck` with controller
name set to `"kueue.x-k8s.io/provisioning-request"`. For that it will also need to
to watch all `AdmissionCheck` definitions to understand whether the particular
check is in fact `ProvisioningRequest` or not.

* For each of such workloads create a `ProvisioningRequest` (and accompanying
PodTemplates) requesting capacity for the podsets of interest from the workload.
A podset is considered "of interest" if it requires at least one resource listed
in the `ProvisioningRequestConfig` `managedResources` field or `managedResources`
is empty. If the workload has no podsets of interest it is considered `Ready`.
The `ProvisioningRequest` should have the owner reference set to the workload.
To understand what details should it put into `ProvisioningRequest` the controller
will also need to watch `ProvisioningRequestConfigs`.

* Watch all changes CA makes to `ProvisioningRequests`. If the `ProvisioningRequest's` conditions are set to:
  - `Provisioned=false` controller should surface information about ProvisioningRequest's ETA. It should emit an event regarding that and for every ETA change.
  - `Provisioned=true` controller should mark the AdmissionCheck as `Ready` and propagate the information about `ProvisioningRequest` name to
workload pods - [KEP #1145](https://github.com/kubernetes-sigs/kueue/blob/main/keps/1145-additional-labels/kep.yaml) under `"cluster-autoscaler.kubernetes.io/consume-provisioning-request"`.
  - `Failed=true` controller should retry AdmissionCheck with respect to the `RetryStrategy` configuration, or mark the AdmissionCheck as `Rejected`
  - `BookingExpired=true` if a Workload is not `Admitted`, the controller should act the same as for `Failed=true`.
  - `CapacityRevoked=true` if a Workload is not `Finished`, the controller should mark it as `Inactive`, which will evict it.
    Additionally, an event should be emitted to signalize this happening. This can happen only if the job
    allows for retries, for example, in the case of `batch.v1/Job`, when the user
    sets `.spec.backOffLimit > 0`.

* Watch the admission of the workload - if it is again suspended or finished,
the provisioning request should also be deleted (the last one can be achieved via
OwnerReference).

* Retry ProvisioningRequests with respect to the `RetryStrategy` configuration in
the `ProvisioningRequestConfig`. For each attempt a new provisioning request is
created with the suffix indicating the attempt number. The corresponding AdmissionCheck will change Workload's `.status.requeueState` accordingly, and change its status to `Retry` state until the Workload is requeued. After the Workload is requeued the workload controller should change the AdmissionCheck's status to `Pending`.
Unless a user configures otherwise configuration is as follows:
  - The max number of retries is 3,
  - The interval between attempts grows exponentially, starting
from 1min (1, 2, 4 min),
  - Workloads gets requeued (and the allocated quota is released) after each failure of ProvisioningRequest.

Workload gets requeued in a similar way to [WaitForPodsReady](https://github.com/kubernetes-sigs/kueue/tree/main/keps/349-all-or-nothing) mechanism.
When AdmissionCheck is in `Retry` state, the workload controller evicts the Workload with `WorkloadRequeued=False` condition with `AdmissionCheck` as a reason.
After the backoff period, the Workload is requeued, enters the queue again, and begins a new admission cycle, and `.status.requeueState.requeueAt` field is reset.

Additionally, to enable the previous behavior of keeping the quota, in 0.9.0 we introduce the feature gate `KeepQuotaForProvReqRetry`. If the feature gate is enabled the Workload with failed ProvisioningRequest
will keep the allocated quota and won't be requeued. Instead it will be in the AdmissionCheck phase of admission, and will recreate ProvisioningRequest after backoff time.
The feature gate is deprecated and will be removed in 0.10 unless we get feedback from users indicating that the old behavior is needed.

The PodSetMergePolicy feature gives the ability to merge similar or identical PodSets into a single PodTemplate within the ProvisioningRequestConfig. This functionality is designed to overcome limitations in certain cloud providers that only allow provisioning one PodTemplate at a time. By merging PodSets, workloads such as PyTorch (which often use similar resources for leader and worker pods) can be efficiently provisioned, reducing overhead and optimizing scaling decisions.

The definition of `ProvisioningRequestConfig` is relatively simple and is based on
what can be set in `ProvisioningRequest`.

```go
// ProvisioningRequestConfig is the Schema for the provisioningrequestconfig API
type ProvisioningRequestConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec ProvisioningRequestConfigSpec `json:"spec,omitempty"`
}

type ProvisioningRequestConfigSpec struct {
	// ProvisioningClassName describes the different modes of provisioning the resources.
	// Check autoscaling.x-k8s.io ProvisioningRequestSpec.ProvisioningClassName for details.
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`
	// +kubebuilder:validation:MaxLength=253
	ProvisioningClassName string `json:"provisioningClassName"`

	// Parameters contains all other parameters classes may require.
	//
	// +optional
	// +kubebuilder:validation:MaxProperties=100
	Parameters map[string]Parameter `json:"parameters,omitempty"`

	// managedResources contains the list of resources managed by the autoscaling.
	//
	// If empty, all resources are considered managed.
	//
	// If not empty, the ProvisioningRequest will contain only the podsets that are
	// requesting at least one of them.
	//
	// If none of the workloads podsets is requesting at least a managed resource,
	// the workload is considered ready.
	//
	// +optional
	// +listType=set
	// +kubebuilder:validation:MaxItems=100
	ManagedResources []corev1.ResourceName `json:"managedResources,omitempty"`

	// retryStrategy defines strategy for retrying ProvisioningRequest.
	// If null, then the default configuration is applied with the following parameter values:
	// backoffLimitCount:  3
	// backoffBaseSeconds: 60 - 1 min
	// backoffMaxSeconds:  1800 - 30 mins
	//
	// To switch off retry mechanism
	// set retryStrategy.backoffLimitCount to 0.
	//
	// +optional
	// +kubebuilder:default={backoffLimitCount:3,backoffBaseSeconds:60,backoffMaxSeconds:1800}
	RetryStrategy *ProvisioningRequestRetryStrategy `json:"retryStrategy,omitempty"`

	// podSetUpdates specifies the update of the workload's PodSetUpdates which
	// are used to target the provisioned nodes.
	//
	// +optional
	PodSetUpdates *ProvisioningRequestPodSetUpdates `json:"podSetUpdates,omitempty"`

	// podSetMergePolicy specifies the policy for merging PodSets before being passed
	// to the cluster autoscaler.
	//
	// +optional
	// +kubebuilder:validation:Enum=IdenticalPodTemplates;IdenticalWorkloadSchedulingRequirements
	PodSetMergePolicy *ProvisioningRequestConfigPodSetMergePolicy `json:"podSetMergePolicy,omitempty"`
}

type ProvisioningRequestPodSetUpdates struct {
	// nodeSelector specifies the list of updates for the NodeSelector.
	//
	// +optional
	NodeSelector []ProvisioningRequestPodSetUpdatesNodeSelector `json:"nodeSelector,omitempty"`
}

type ProvisioningRequestPodSetUpdatesNodeSelector struct {
	// key specifies the key for the NodeSelector.
	//
	//  +required
	Key string `json:"key"`

	// ValueFromProvisioningClassDetail specifies the key of the
	// ProvisioningRequest.status.provisioningClassDetails from which the value
	// is used for the update.
	//
	// +required
	ValueFromProvisioningClassDetail string `json:"ValueFromProvisioningClassDetail"`
}

type ProvisioningRequestRetryStrategy struct {
	// BackoffLimitCount defines the maximum number of re-queuing retries.
	// Once the number is reached, the workload is deactivated (`.spec.activate`=`false`).
	//
	// Every backoff duration is about "b*2^(n-1)+Rand" where:
	// - "b" represents the base set by "BackoffBaseSeconds" parameter,
	// - "n" represents the "workloadStatus.requeueState.count",
	// - "Rand" represents the random jitter.
	// During this time, the workload is taken as an inadmissible and
	// other workloads will have a chance to be admitted.
	// By default, the consecutive requeue delays are around: (60s, 120s, 240s, ...).
	//
	// Defaults to 3.
	// +optional
	// +kubebuilder:default=3
	BackoffLimitCount *int32 `json:"backoffLimitCount,omitempty"`

	// BackoffBaseSeconds defines the base for the exponential backoff for
	// re-queuing an evicted workload.
	//
	// Defaults to 60.
	// +optional
	// +kubebuilder:default=60
	BackoffBaseSeconds *int32 `json:"backoffBaseSeconds,omitempty"`

	// BackoffMaxSeconds defines the maximum backoff time to re-queue an evicted workload.
	//
	// Defaults to 1800.
	// +optional
	// +kubebuilder:default=1800
	BackoffMaxSeconds *int32 `json:"backoffMaxSeconds,omitempty"`
}
```

`AdmissionCheck` will point to this configuration:

```yaml
kind: AdmissionCheck:
name: "SuperProvider"
spec:
  controllerName: “kueue.x-k8s.io/provisioning-request”
  parameters:
    apiGroup: “kueue.x-k8s.io/v1beta1”
    kind: “ProvisioningRequestConfig”
    name: “SuperProviderConfig”
---
kind: ProvisioningRequestConfig:
name: "SuperProviderConfig"
spec:
  provisioningClass: "SuperSpot"
  parameters:
    "Priority": "TopTier"
  managedResources:
  - cpu

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

The tests should start with a job going to a queue with `kueue.x-k8s.io/provisioning-request` based `AdmissionCheck`.
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

* `ProvisioningRequest` is using V1 APIs
* `ProvisioningRequest` deprecated annotations are dropped by code.
* `ProvisioningRequest` is beta without known issues.

## Implementation History

2023-09-21: KEP
2025-08-07: Promoted to GA.

## Alternatives

Not do `ProvisioningRequest` integration.
