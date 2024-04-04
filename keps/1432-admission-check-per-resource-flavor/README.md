# KEP-1432: AdmissionChecks per ResouceFlavor

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories (Optional)](#user-stories-optional)
    - [Story 1](#story-1)
  - [Story 2](#story-2)
  - [Notes/Constraints/Caveats (Optional)](#notesconstraintscaveats-optional)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [Test Plan](#test-plan)
    - [Unit Tests](#unit-tests)
    - [Integration tests](#integration-tests)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
<!-- /toc -->

## Summary

This KEP introduces assigning AdmissionChecks to a ResourceFlavor.

## Motivation

Currently, AdmissionChecks are only assigned to a ClusterQueue. This results in running `AdmissionChecks` regardless of
the ResourceFlavor used by a Workload, even when it is unnecessary. We would like to have the ability to assign AdmissionChecks
to a specific ResourceFlavor, so the whole mechanism is more flexible and expressive.

### Goals
- Support running AdmissionChecks only when a particular ResourceFlavor is assigned to the Workload.
- Running an AdmissionCheck with different settings depending on the flavor

### Non-Goals

## Proposal

Add new field to FlavorQuota API, so it contains a list of AdmissionChecks that  should run when a Workload
is admitted for this ResourceFlavor.

### User Stories (Optional)

#### Story 1
As a user who has reserved machines at my cloud provider I would like to use them first. If they are not obtainable then
switch to spot machines, using ProvisioningRequest. It only makes sense to run Provisioning AdmissionCheck when the ResourceFlavor is Spot.

### Story 2
I would like to follow a similar approach as in an above story, but additionally I want to limit spending on a given
ClusterQueue. I have created an in-house AdmissionCheck that checks if the budget is not exceeded. This check should run
for all Workloads submitted to the ClusterQueue.

### Notes/Constraints/Caveats (Optional)
All `AdmissionChecks` assigned to a Workload must have different controllers.
This is because in case of having two AdmissionChecks with Provisioning controller, it would result in creating
two ProvisioningRequests for the same Workload, which is wasteful.

### Risks and Mitigations

## Design Details
We propose adding a new field to `FlavorQuota` API called `AdmissionChecks` in following manner:

```
type FlavorQuotas struct {
[...]

  // admissionChecks is the list of AdmissionChecks that should run when the ResourceFlavor is used.
  // The list consist of metav1.Names of AdmissionChecks.
  // No two AdmissionChecks should have the same controller.
  // +listType=set
  AdmissionChecks []string `json:"admissionchecks"`
}
```

At the same time, we want to preserve the existing `AdmissionChecks` field in `ClusterQueue` API. A Workload may have
assigned `AdmissionChecks` both from `ClusterQueue` and `FlavorQuota` APIs.

This enables having default `AdmissionChecks` that apply to all Workloads submitted to a `ClusterQueue`, and more
specific ones that apply only to Workloads using a specific `ResourceFlavor`.

### Test Plan

[X] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.

#### Unit Tests

Test Workload's Controller's function that assigns AdmissionChecks to a Workload

#### Integration tests
- Create 2 ResourceFlavors each with a different AdmissionCheck and test if a Workload contains the AdmissionCheck associated with one of the ResourceFlavors;
- Create 2 AdmissionChecks with different controllers, one associated with ResourceFlavor and the other with ClusterQueue. Test if a Workload contains both;

## Drawbacks


## Alternatives
Alternatively we could change `AdmissionChecks` API so it contained selector with a list of `ResourceFlavors` to which
it should be assigned. There are no strong pros or cons for either approach. We chose the current approach because we
believe it is more intuitive for users.
