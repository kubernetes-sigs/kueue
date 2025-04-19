# KEP-2560: ResourceFlavor fallbacks

<!--
This is the title of your KEP. Keep it short, simple, and descriptive. A good
title can help communicate what the KEP is and should be considered as part of
any review.
-->

<!--
A table of contents is helpful for quickly jumping to sections of a KEP and for
highlighting any additional information provided beyond the standard KEP
template.

Ensure the TOC is wrapped with
  <code>&lt;!-- toc --&rt;&lt;!-- /toc --&rt;</code>
tags, and then generate with `hack/update-toc.sh`.
-->

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1](#story-1)
    - [Story 2](#story-2)
- [Design Details](#design-details)
    - [ClusterQueue API changes - FlavorFallbackStrategy](#clusterqueue-api-changes---flavorfallbackstrategy)
      - [Usage:](#usage)
    - [How will Kueue assign ResourceFlavors](#how-will-kueue-assign-resourceflavors)
    - [Timeout based strategy](#timeout-based-strategy)
      - [What does the timeout cover](#what-does-the-timeout-cover)
    - [Workload API changes - flavorAssignmentHistory](#workload-api-changes---flavorassignmenthistory)
  - [Notes/Constraints/Caveats](#notesconstraintscaveats)
    - [WaitForPodsReady](#waitforpodsready)
    - [Multiple ResourceFlavors for one Workload](#multiple-resourceflavors-for-one-workload)
  - [Risks and Mitigations](#risks-and-mitigations)
    - [Non-trivial interleavings for timeout based strategy](#non-trivial-interleavings-for-timeout-based-strategy)
  - [Test Plan](#test-plan)
    - [Unit Tests](#unit-tests)
    - [Integration tests](#integration-tests)
- [Implementation Plan](#implementation-plan)
  - [Timeout based strategy](#timeout-based-strategy-1)
  - [AdmissionCheck based strategy](#admissioncheck-based-strategy)
- [Drawbacks](#drawbacks)
  - [Wasteful resource allocation](#wasteful-resource-allocation)
- [Alternatives](#alternatives)
  - [Add timeout to the FlavorQuota API](#add-timeout-to-the-flavorquota-api)
  - [Use separate timeouts for AdmissionChecks and WaitForPodReady.](#use-separate-timeouts-for-admissionchecks-and-waitforpodready)
<!-- /toc -->

## Summary

We propose adding a mechanism to Kueue, that allows users to fallback to a different consumption model (ResourceFlavor). The mechanism will have two modes of operation and will be introduced in two phases:
Fallback based on configurable timeout (this is the focus of this KEP)
Fallback based on ProvisioningRequest response

The feature will involve adding new APIs to the ClusterQueue and Workload objects.


## Motivation

Currently, we provide the fallback mechanism only within a single scheduling cycle by flavorFungibility. 
It means that if there is a free capacity in Kueue, but there are stockouts on the cloud provider side, or fragmented resource consumption happening in the cluster, Kueue will assign the same flavor over and over to a given Workload throughout the requeuing cycles. This results in wasteful assignments VMs to a Workload that will not start (e.g. Workload will repeatedly get 5 VMs, when it needs 10 of them to start)
Users would like to be able to configure Kueue in a way, so that in case there are stockouts or fragmented node resource consumption, Kueue will try a different flavor (e.g. Spot vs On-demand).

Additionally, as of now users do not have the ability to express how much time they are willing to wait for an AdmissionCheck (in particular ProvisioningRequest) to succeed.

### Goals

- Provide a mechanism that solves above-mentioned problems and accounts for two modes of operation
- Describe API changes
- Describe design details on the timeout based mode of operation

### Non-Goals

- Describe flavor assignment mechanisms
- Describe design details on the ProvisioningRequest’s response based mode of operation

## Proposal

- Add a new API field to the ClusterQueue that defines fallback rules. It accounts for both timeout based and Provisioning Requests response based modes of operation.
- Add a new API field to the Workload’s Status that tracks how much time Workload has a flavor assigned for

### User Stories

#### Story 1

As a batch admin I configured my ClusterQueue to have 3 flavors: Reservation, Spot, On-demand.
Reservation has a fixed quota, so if there is free capacity in Kueue, there is also a free capacity on the cloud provider side. Spot is set to have an "infinite" quota - stockouts on the cloud provider side may appear. The desired behavior is that, when Reservation is saturated, and there are stockouts with Spot, Kueue requeues the job. In the next iteration, the job can be retried on the Reservation (if there is now capacity) or On-demand, but not on Spot.

#### Story 2

As a batch admin I configured my ClusterQueue to have 3 flavors: FlavorA, FlavorB, FlavorC. I want my users to use run their jobs on FlavorA first. On fallback I want them to try to run theirs jobs on FlavorB but if there is no capacity in the region, use FlavorC instead

## Design Details

#### ClusterQueue API changes - FlavorFallbackStrategy

We propose to introduce a new API to the ClusterQueue object, similarly to AdmissionCheckStrategy. The new field called FlavorFallbackStrategy falls under FlavorFungibility configuration. The order of rules does not affect the order in which Flavors are assigned. This field should be treated more as a mapping between a ResourceFlavor and the timeout, and a list of policies applied to all flavors. 

```golang
type FlavorFungibility struct {
	[...]
	// fallbackStrategy defines a list of strategies to determine which how much time a user is willing to spend on trying a ResourceFlavor.
	// +optional
	FallbackStrategy *FlavorFallbackStrategy
}

type FlavorFallbackStrategy struct {
	// failurePolicy is an enum describing whether Kueue should evict a Workload after trying all flavors, or reset flavors assigning attempts
	// Values: [DeactivateWorkload, RetryAllFlavors]
	FailurePolicy string

	// rules is a list of strategies for ResourceFlavor fallbacks.
	// +optional
	// +listType=map
	Rules []FlavorFallbackStrategyRule
}

// FlavorFallbackStrategyRule defines rules for a single ResourceFlavor
type FlavorFallbackStrategyRule struct {
	// name is a ResourceFlavor's name.
	// '*' means that the rule applies to every ResourceFlavor
	Name ResourceFlavorReference

	// trigger is an enum describing whether the fallback is AdmissionCheck based, or timeout based
	// Values: [TimeoutForPodsReadyExceeded, AdmissionCheckRejected]
	Trigger string

	TimeoutMinutes *int
}
```

##### Usage:
An example ClusterQueue’s configuration::

```yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: ClusterQueue
metadata:
  name: "cluster-queue"
spec:
  [...]
  flavorFungibility:
    whenCanBorrow: TryNextFlavor
    flavorFallbackStrategy:
failurePolicy: DeactivateWorkload
strategy: TimeoutBased
rules:
  - name: "reservation"
    timeout: 10m  # we want to wait at most 10 mins for a job to be started
  - name: "spot"
    timeout: 15m  # we want to wait at most 15 mins for a job to be started
  - name: "on-demand" # no timeout since there is no flavor to fallback to
```

#### How will Kueue assign ResourceFlavors

Currently, in Kueue we have a lot of features related to assigning a ResourceFlavor to a Workload such as FlavorFungibility, or Preemption. We want to minimize interference with already complicated algorithms, so the only way this feature will impact assigning flavors would be preventing assigning a ResourceFlavor that should be excluded with respect to the flavorFallbackStrategy.

#### Timeout based strategy
##### What does the timeout cover

The countdown starts as soon as Kueue assigns a flavor to a Workload so it covers the time between assigning a flavor and starting a job, in particular:
- Time for AdmissionChecks
- Kueue operation time (sending and responding to requests)
- Time for pods to get ready

It means the timeout does not cover time spent in a queue

#### Workload API changes - flavorAssignmentHistory

We introduce an additional field called flavorAssignmentHistory to the Workload’s Status API that will map a ResourceFlavor to the duration already used for a given ResourceFlavor.
E.g:

```golang
type WorkloadStatus struct {
	[...]
	// flavorAssignmentHistory is a list containing history of flavor assignments for this workload
	// +listType=map
	// +listMapKey=resourceFlavor
	FlavorAssignmentHistory []FlavorAssignmentHistory
}

type FlavorAssignmentHistory struct {
	// resourceFlavor is the name of the flavor the history is for
	ResourceFlavor ResourceFlavorReference

	// assignmentTime is the time the flavor was assigned for the last time
	AssignmentTime metav1.Time
}
```

Kueue checks if current time is greater than assignment time + the defined timeout, and based on that allows or prevents from assigning the flavor to a Workload

Exemplary Workload’s status would like following:


```yaml
---
apiVersion: kueue.x-k8s.io/v1beta1
kind: Workload
[...]
status:
  [...]
  flavorAssignmentHistory:
    Resource Flavor: spot
    Assignment Time: 2024-06-17T10:14:40Z
    Resource Flavor: on-demand
    Assignment Time: 2024-06-17T10:00:40Z
    Resource Flavor: reservation # Kueue hasn't tried this flavor for the Workload yet
    Assignment Time:
```


### Notes/Constraints/Caveats

#### WaitForPodsReady

Designing this feature we are aware of the WaitForPodsReady feature that may serve as an alternative for finishing Workloads when there are stock-outs. However it comes with limitations that prevent us from relying on it.
Primarily, it does not prevent Kueue from assigning the same flavor repeatedly in the future.
It does not provide different timeouts for different ResourceFlavors/ClusterQueues.
It does not take into account possible AdmissionChecks that Kueue runs between ResourceFlavor’s assignment and Workload’s admission.

Hence, we treat WaitForPodsReady as a completely separate feature and the mechanism does take it into account

#### Multiple ResourceFlavors for one Workload

Kueue can assign a flavor to the Workload for each resourceGroup. Hence we may end up in the situation where a single Workload has multiple ResourceFlavors assigned. It does not affect the correctness of the fallback mechanism but we need to keep that fact in mind.

### Risks and Mitigations

#### Non-trivial interleavings for timeout based strategy
We have to keep in mind that there can be some non-trivial interleaving regarding assigning flavors. Assume we have 2 flavors: Flavor-A and Flavor-B. Let’s say Flavor-A has no free capacity, so Kueue tries to run a job with Flavor-B. However it failed to run a job on Flavor-B, and in the meantime Flavor-A released some capacity. Kueue tries to run a job with Flavor-A now, but it also fails. We have to be aware of such scenarios and strictly define behavior of timeouts - e.g. how they should be counted down, what should happen on timeout.


### Test Plan

[X] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.

#### Unit Tests

Relevant unit tests will be added

#### Integration tests

This feature will be covered by integration tests. Especially, we want to test scenarios with 3 flavors.
- Flavor #1 is saturated, so Kueue will try starting a job on flavor #2. When Flavor #2 fails, then Kueue should try scheduling Flavor #1, and then Flavor #3 if there is Flavor #1 has no capacity.
- If `FailurePolicy=DeactivateWorkload` Kueue should deactivate Workload if all flavors fail to start a job
- If `FailurePolicy=RetryAllFlavors` Kueue should reset all `Assignment Time` fields in `flavorAssignmentHistory` field in Workload’s status if all flavors fail to start a job, and start flavor assignment cycle over.

## Implementation Plan

The implementation of the mechanism will be divided into two phases that aligns with possible fallback strategies.
### Timeout based strategy
- Add flavorFallbackStrategy field to the ClusterQueue API
- Add flavorAssignmentHistory field to the Workload API
- Add timeout based fallback mechanism

### AdmissionCheck based strategy
- Add Trigger field to the flavorFallbackStrategy field
- Add AdmissionCheck based fallback mechanism

## Drawbacks

### Wasteful resource allocation
When using timeout based strategy there may be a scenario when we are left with only 1 minute of timeout after previous failures. In that case we allocate resources in Kueue, possibly create a ProvisioningRequest or provision nodes without it, with very low chances of starting a job. After the 1 minute resources will be freed, but for that short period of time we waste it.


## Alternatives

### Add timeout to the FlavorQuota API

As an alternative to introducing a new field in the ClusterQueue object we could extend the FlavorQuotas (definitions of flavors in ClusterQueue’s spec) API to contain timeout.
*Pros:*
- Less changes to the ClusterQueue’s API
*Cons:*
- No consistency with the AdmissionCheckStrategy field
- Does not extend well

### Use separate timeouts for AdmissionChecks and WaitForPodReady.

Another alternative would be to introduce two separate timeouts - one for AdmissionChecks phase, and the other actually starting a Workload once it's admitted.
*Pros:*
- Represents well the natural distinction between AdmissionChecks phase and WaitForPodsReady phase - those two phases may significantly vary in needed time
*Cons:*
- More complexity
- Harder to maintain

Having this in mind we also may extend the proposed mechanism by an additional timeout in the future.
