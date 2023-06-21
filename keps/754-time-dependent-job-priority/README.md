# KEP-754: Time-dependent job priority

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
      - [Prerequisite testing updates](#prerequisite-testing-updates)
    - [Unit Tests](#unit-tests)
    - [Integration tests](#integration-tests)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
<!-- /toc -->

## Summary

This proposal allows job priorities to be dynamically adjusted based on time.
This functionality aims to prioritize the execution of jobs that have been waiting for an extended period, even if they have a lower initial priority.
By introducing time-dependent priority adjustments, the system can ensure that jobs that have been deferred receive the attention they require.
This KEP outlines the necessary changes and considerations to incorporate this time-based priority mechanism.

## Motivation

Lower-priority jobs sometimes get postponed for long time than expected.
To address situations where jobs remain unexecuted even after a certain period of time, we implementate this KEP.

### Goals

Provide a mechanism where the priority of a job increase after a certain period of time has elapsed.

### Non-Goals

The timed out job is going to the head of the ClusterQueue instead of changing the priority.

## Proposal

<!--
This is where we get down to the specifics of what the proposal actually is.
This should have enough detail that reviewers can understand exactly what
you're proposing, but should not include things like API designs or
implementation. What is the desired outcome and how do we measure success?.
The "Design Details" section below is for the real
nitty-gritty.
-->

### User Stories

#### Story 1

The job does not have a high priority but is intended for production, so it needs to start execution within three hours.


### Risks and Mitigations

The priority of job should be updated so that the running job isn't preempted.

## Design Details

<!--
This section should contain enough information that the specifics of your
change are understandable. This may include API specs (though not always
required) or even code snippets. If there's any ambiguity about HOW your
proposal will be implemented, this is the place to discuss them.
-->

### Test Plan

No regressions in the current test should be observed.

[X] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.

#### Unit Tests

This change should be covered by unit tests.

#### Integration tests

This change should be covered by integration tests.

### Graduation Criteria


## Implementation History


## Drawbacks


## Alternatives

