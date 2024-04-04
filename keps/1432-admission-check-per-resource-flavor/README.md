# KEP-1432: AdmissionChecks per ResouceFlavor

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories (Optional)](#user-stories-optional)
    - [Story 1](#story-1)
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

Add a new field to FlavorQuota API, so it contains a list of AdmissionChecks that  should run when a Workload
is admitted for this ResourceFlavor.

### User Stories (Optional)

#### Story 1
As a user who has reserved machines at my cloud provider I would like to use them first. If they are not obtainable then
switch to spot machines, using ProvisioningRequest. It only makes sense to run Provisioning AdmissionCheck when the ResourceFlavor is Spot.

### Notes/Constraints/Caveats (Optional)
User cannot define AdmissionChecks both at ClusterQueue and ResourceFlavor level. This will be validated by
a ClusterQueue webhook.

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

At the same time, we want to preserve the existing `AdmissionChecks` field in `ClusterQueue` API, with the constraints mentioned above.

### Test Plan

[X] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.

#### Unit Tests

- Test Workload's Controller's function that assigns AdmissionChecks to a Workload

- Test the webhook that validates ClusterQueue when user defines AdmissionChecks at both ClusterQueue and ResourceFlavor
level.


#### Integration tests
- Create 2 ResourceFlavors each with a different AdmissionCheck and test if a Workload contains the AdmissionCheck associated with one of the ResourceFlavors;
- Create 2 ResourceFlavors, "reservation" and "spot" with an AdmissionCheck, and Workload that require "spot" Flavor. Check if the Workload contains the AdmissionCheck associated with "spot" Flavor.

## Drawbacks

## Alternatives
Alternatively we could change `AdmissionChecks` API so it contained selector with a list of `ResourceFlavors` to which
it should be assigned. However, this decreases the flexibility of the mechanism, as it would force users to use the same
AdmissionChecks for ResourceFlavor regardless of the ClusterQueue where the Workload is submitted.
