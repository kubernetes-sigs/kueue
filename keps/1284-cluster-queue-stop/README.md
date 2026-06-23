# KEP-1284: Add a mechanism to stop a ClusterQueue.
<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1](#story-1)
  - [Notes/Constraints/Caveats](#notesconstraintscaveats)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [API/ClusterQueue](#apiclusterqueue)
  - [Controllers](#controllers)
    - [ClusterQueue](#clusterqueue)
    - [Workload](#workload)
  - [Test Plan](#test-plan)
      - [Prerequisite testing updates](#prerequisite-testing-updates)
    - [Unit Tests](#unit-tests)
    - [Integration tests](#integration-tests)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
<!-- /toc -->

## Summary
Add setting in a ClusterQueue that an administrator is able to use in order to pause new admissions and have the option to cancel current QuotaReservations and Evict admitted workloads.

## Motivation

This is a common admin journey to control usage from a user.

### Goals

Add a setting in a ClusterQueue that an administrator is able to use in order to to pause new admissions and have the option to cancel current QuotaReservations and Evict admitted workloads.

### Non-Goals

Manage the QuotaReservation and Admission of workloads from the same cohort that might borrow resources from the ClusterQueue in question.

## Proposal

Add a new member in the ClusterQueue implementation `stopPolicy` the presence of which will mark the ClusterQueue as Inactive and it's value will control how the `Admitted` or `Reserving` workloads are affected.

### User Stories
#### Story 1

As a cluster administrator I want to be able to stop the new admissions in a specific ClusterQueue with the option of Evicting currently admitted Workloads or canceling QuotaReservations.

### Notes/Constraints/Caveats
Managing the Reservation canceling and Eviction of workloads in other queues from the same cohort that
are potentially borrowing resources from the stopped queue adds a considerable amount of complexity
while having a limited added value, therefore these cases are not covered in this first iteration. 

### Risks and Mitigations

## Design Details

### API/ClusterQueue

```go
type ClusterQueueSpec struct {
	// ....

	// stopPolicy - if set the ClusterQueue is considered Inactive, no new reservation being
	// made. 
	//
	// Depending on its value, its associated workloads will:
	//
	// - None - Workloads are admitted
	// - HoldAndDrain - Admitted workloads are evicted and Reserving workloads will cancel the reservation.
	// - Hold - Admitted workloads will run to completion and Reserving workloads will cancel the reservation.
	//
	// +kubebuilder:validation:Enum=None;Hold;HoldAndDrain
	// +kubebuilder:default="None"
	StopPolicy StopPolicy `json:"stopPolicy,omitempty"`
}

type StopPolicy string

const (
	None         StopPolicy = "None"
	Hold         StopPolicy = "Hold"
	HoldAndDrain StopPolicy = "HoldAndDrain"
)


```
### Controllers
#### ClusterQueue

Once the `stopPolicy` is set the cluster queue is marked as inactive with a relevant status message.

#### Workload

If the cluster queue associated to a workload has the `stopPolicy` changed depending on the policy value and state of the
workload it should Evict or cancel the reservation of the workload.

### Test Plan


[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.

##### Prerequisite testing updates


#### Unit Tests

To be added depending on the added code complexity.

#### Integration tests

The `controllers/core` suite should check:

1. ClusterQueue - Once the `stopPolicy` is set a ClusterQueue becomes Inactive.
2. Workload - Once its ClusterQueue `stopPolicy` is set, depending on the value:
- The Reserving workloads are canceling the reservation.
- The Admitted workloads get Evicted and the Reserving ones cancel their reservation.
- New workload is not admitted when cluster queue is inactive

### Graduation Criteria


## Implementation History


## Drawbacks


## Alternatives

