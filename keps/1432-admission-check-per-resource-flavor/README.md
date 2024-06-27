# KEP-1432: AdmissionChecks per ResourceFlavor

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories (Optional)](#user-stories-optional)
    - [Story 1](#story-1)
  - [Notes/Constraints/Caveats (Optional)](#notesconstraintscaveats-optional)
- [Design Details](#design-details)
  - [Test Plan](#test-plan)
    - [Unit Tests](#unit-tests)
    - [Integration tests](#integration-tests)
- [Alternatives](#alternatives)
  - [AdmissionCheck API change](#admissioncheck-api-change)
  - [FlavorQuota API change](#flavorquota-api-change)
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

Add a new field to ClusterQueue API, so it contains a list of AdmissionChecks with associated ResourceFlavors.
That way a user can define what AdmissionChecks should run for a particular ResourceFlavor.

### User Stories (Optional)

#### Story 1
As a user who has reserved machines at my cloud provider I would like to use them first. If they are not obtainable then
switch to spot machines, using ProvisioningRequest. It only makes sense to run Provisioning AdmissionCheck when the ResourceFlavor is Spot.

### Notes/Constraints/Caveats (Optional)
User cannot define AdmissionChecks using both `.spec.AdmissionCheck` and `.spec.AdmissionCheckStrategy` fields. This will be validated by a webhook.

## Design Details
We propose adding a new field to `ClusterQueue` API called `AdmissionChecksStrategy` in following manner:

```
type ClusterQueue struct {
[...]

  // admissionCheckStrategy is the list of AdmissionChecks that should run when a particular ResourceFlavor is used.
  AdmissionCheckStrategy []AdmissionCheckStrategy
}

type AdmissionCheckStrategy struct {

  	// name is an AdmissionCheck's metav1.Name
    Name string

    // forFlavors is a list of ResourceFlavors' metav1.Names that this AdmissionCheck should run for.
    // if empty the AdmissionCheck will run for all workloads submitted to the ClusterQueue.
    OnFlavors []string
}
```

At the same time, we want to preserve the existing `AdmissionChecks` field in `ClusterQueue` API, with the constraints mentioned above for backward compatibility.

### Test Plan

[X] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.

#### Unit Tests
- Test Workload's Controller's function that assigns AdmissionChecks to a Workload
- Test the webhook that validates ClusterQueue if user defines AdmissionChecks using `.spec.AdmissionCheck` and `.spec.AdmissionCheckStrategy`



#### Integration tests
- Create 2 AdmissionCheckStrategies each with a different AdmissionCheck and test if a Workload contains the AdmissionCheck associated with one of the ResourceFlavors;
- Create 2 ResourceFlavors, "reservation" and "spot" with an AdmissionCheck, and Workload that require "spot" Flavor. Check if the Workload contains the AdmissionCheck associated with "spot" Flavor.
- Create AdmissionCheckStrategy with an empty `OnFlavors` list and test if a Workload contains the AdmissionCheck.


## Alternatives
### AdmissionCheck API change
Change `AdmissionCheck` API so it contains a selector with a list of `ResourceFlavors` to which
it should be assigned.

**Cons:**
- Decreases the flexibility of the mechanism, as it would force users to use the same AdmissionChecks for ResourceFlavor regardless of the ClusterQueue where the Workload is submitted.

### FlavorQuota API change

Add a new field to `FlavorQuota` API, so it contains a list of AdmissionChecks that should run when a Workload
is admitted for this ResourceFlavor.

```
type FlavorQuotas struct {
[...]

  // admissionChecks is the list of AdmissionChecks that should run when the ResourceFlavor is used.
  // The list consist of metav1.Names of AdmissionChecks.
  AdmissionChecks []string `json:"admissionChecks"`
}
```

**Cons:**
- Introduces AdmissionChecks fragmentation
- Not as future proof as proposal
