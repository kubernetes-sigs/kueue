# KEP-3899: Remove finalizers using strict patch

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories (Optional)](#user-stories-optional)
  - [Notes/Constraints/Caveats (Optional)](#notesconstraintscaveats-optional)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [Test Plan](#test-plan)
    - [Integration tests](#integration-tests)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
<!-- /toc -->

## Summary

Remove finalizers using strict patch.
This would force every finalizer update to be handled individually, removing the possibility of
race conditions.

## Motivation

Updates to the finalizers [could be lost if they were made at the time when
Kueue removed its system finalizers](https://github.com/kubernetes-sigs/kueue/issues/3899). 
This could result in the unexpected and faulty behavior of the resources. 
To prevent this, we need to use strict mode that ensures the concurrently-safe finalizer handling.

### Goals

- Remove finalizers using a strict patch.


### Non-Goals

- Implement a feature to provide a knob if Kueue uses strict mode or not.

## Proposal

Make Kueue remove and update finalizers from resources using a strict patch to ensure
concurrently-safe finalizer handling.


### User Stories (Optional)


### Notes/Constraints/Caveats (Optional)

Removing finalizers using a strict patch will force the reconciler to requeue
every change to the finalizers individually, which could result in a performance
hit when used on a large scale.


### Risks and Mitigations

## Design Details

### Test Plan

[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.

#### Integration tests

- Test that verifies that no error happens when the strict mode is engaged.


### Graduation Criteria

GA/Stable:
* Positive feedback from users
* No critical performance hit
* There are no significant performance disadvantages in manual performance testings by real K8s cluster or KWOK.

## Implementation History

KEP: 2025-02-26

## Drawbacks


## Alternatives

