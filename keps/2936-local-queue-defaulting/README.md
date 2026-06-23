# KEP-2936: LocalQueue defaulting

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1](#story-1)
  - [Notes/Constraints/Caveats (Optional)](#notesconstraintscaveats-optional)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [Test Plan](#test-plan)
- [Alternatives](#alternatives)
  - [Default field in LocalQueue spec](#default-field-in-localqueue-spec)
  - [Add default LocalQueue only to Workload object](#add-default-localqueue-only-to-workload-object)
<!-- /toc -->

## Summary

This KEP introduces a defaulting LocalQueue mechanism for the jobs that don't have
LocalQueue name label specified.

## Motivation

Simplify user experience with Kueue.

### Goals

- default LocalQueue for jobs that don't specify LocalQueue name label.

### Non-Goals

- create a default LocalQueue.

## Proposal

If the Job doesn't have label for LocalQueue in its Spec and the LocalQueue
with name `default` is present in Job's namespace and LocalQueueDefaulting feature gate
is enabled, the `kueue.x-k8s.io/queue-name:default` anotation will be added to the Job.

The `default` LocalQueue itself should be created manually by administrator.

### User Stories

#### Story 1

Administrator created a single `default` LocalQueue per namespace. Users doesn't need to specify
the LocalQueue label in order for workload to be scheduled.

### Notes/Constraints/Caveats (Optional)

manageJobsWithoutQueueName doesn't work with this feature, since Kueue add the LocalQueue
label.

### Risks and Mitigations

## Design Details

Update defautling webhook for all types of job to add `kueue.x-k8s.io/queue-name:default`
label if the label is not present and the feature gate is enabled.

### Test Plan

[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes
necessary to implement this enhancement.

## Alternatives

### Default field in LocalQueue spec

Add default field to LocalQueue spec, that could be set to true|false. There are two
scenarios possible:

- If only one LocalQueue is allowed to be set as default, then we should have
a validation webhook which validating the object based on other objects in
the cluster.
- If multiple queues could be set to default, we have to choose which one
is an actual default. Usually the creating timestamp is used for comparison.
However having multiple default LocalQueues with arbitrary names makes their management
more complex.

### Add default LocalQueue only to Workload object

This approach was rejected because:

- The approach is lacking visibility for the user.

- The approach depends on config.manageJobsWithoutQueueName value. If the value is set to false,
the job won't be managed by Kueue.
