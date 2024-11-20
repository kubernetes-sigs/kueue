# KEP-3589: Uniformly filter manageJobsWithoutQueueName by namespace

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories (Optional)](#user-stories-optional)
    - [Story 1](#story-1)
- [Design Details](#design-details)
  - [API](#api)
  - [Implementation](#implementation)
  - [Test Plan](#test-plan)
    - [Unit Tests](#unit-tests)
    - [Integration tests](#integration-tests)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
<!-- /toc -->

## Summary

This KEP adds an optional field to the Kueue `Configuration` struct that enables
the effect of `manageJobsWithoutQueueName` to be controlled on a per-namespace
basis for all Kueue integrations.

-->

## Motivation

Kueue already allows per-namespace control of `manageJobsWithoutQueueName`
for the Pod integration via `integrations.podOptions.namespaceSelector`.
The effect of `manageJobsWithoutQueueName` on the `Deployment` and `StatefulSet`
integrations in Kueue 0.9 are also indirectly controlled by the same `namespaceSelector`
because these integrations are built on top of the `Pod` integration.  Just like the
`Pod` integration, Kueue requires some mechanism for modulating `manageJobsWithoutQueueName`
for these fundamental types for system namespaces to avoid interrupting normal cluster operations.

No other integrations beyond these three consider the `namespaceSelector` when deciding whether or not to
suspend jobs that do not have a queue name.  This results in an irregular API and makes it
harder to configure Kueue such that quotas will be enforced (cannot be bypassed by users).
The irregularity between `batchv1/Job` and `Deployment` is likely to be especially confusing
to users and cluster admins who would view both of these types as "built-in" to Kubernetes and
would expect them to be configured in the same way.

### Goals

Provide a simple and uniform API to enable cluster admins to configure Kueue
such that quotas will be enforced for specific namespaces.

### Non-Goals

Consider any scope for filtering `manageJobsWithoutQueueName` that is not based on namespaces.

## Proposal

We add a `manageAllJobsNamespaceSelector` of type `*metav1.LabelSelector`
to the top level of the Kueue `Configuration` struct (same level as `manageJobsWithoutQueueName`).
(The name of `manageJobsWithoutQueueNameNamespaceSelector` reads poorly, so I propose something else)

We use this new configuration for all integrations in the same way that the current
`namespaceSelector` is used by the Pod integration.

We migrate away from using `podOptions.namespaceSelector` over the course of several releases.
Ideally we remove it from the `Configuration` struct as part of going to the `v1` API level.

### User Stories (Optional)

#### Story 1

Cluster admins will be able to use `manageAllJobsNamespaceSelector` to easily configure Kueue
to enable quota enforcement on a per namespace basis.

## Design Details

### API

The `Configuration` struct is extended to add ManageAllJobsNamespaceSelector
```go
type Configuration struct {

  ...

	// ManageJobsWithoutQueueName controls whether or not Kueue reconciles
	// jobs that don't set the label kueue.x-k8s.io/queue-name.
	// If set to true, then those jobs will be suspended and never started unless
	// they are assigned a queue and eventually admitted. This also applies to
	// jobs created before starting the kueue controller.
	// Defaults to false; therefore, those jobs are not managed and if they are created
	// unsuspended, they will start immediately.
	ManageJobsWithoutQueueName bool `json:"manageJobsWithoutQueueName"`

  // ManageAllJobsNamespaceSelector can be used to omit some namespaces from ManageJobsWithoutQueueName
	ManageAllJobsNamespaceSelector *metav1.LabelSelector `json:"manageAllJobsNamespaceSelector,omitempty"`

  ...
}
```

### Implementation

The configuration of all Job webhooks are extended to propagate the label selector and it is
used to filter manageJobsWithoutQueueNames as is currently done in the Pod integration.

Configuration parsing during startup is extended to give a deprecation notice if `podOptions.namespaceSelector` is set.
Setting both `podOptions.namespaceSelector` and `manageAllJobsNamespaceSelector` will be flagged as a configuration error.

### Test Plan

[ X ] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.

#### Unit Tests

Unit tests for configuration parsing will be added.

All unit tests for the current Pod integration namespaceSelector will be extended across all integrations.

#### Integration tests

All integration tests for the current Pod integration namespaceSelector will be extended across all integrations.

### Graduation Criteria

The feature is implemented as described for all Job integrations.

## Implementation History

## Drawbacks

<!--
Why should this KEP _not_ be implemented?
-->

## Alternatives

We could do nothing and simply document that despite its historical placement in
a subfield of the `Configuration` struct, `podOptions.namespaceSelector` also applies
to the `StatefulSet` and `Deployment` integrations.

We could phase out `manageJobsWithoutQueueNames` and instead recommend the
use of ValidatingAdmissionPolicies to ensure that Jobs created in "user" namespaces
all have a queue name label (and thus will be managed by Kueue). However, for this to
work the VAP must be able to correctly identify child Jobs that are owned directly
or indirectly by Kueue-managed Jobs and are thus exempt from the policy.  Since there
can be multiple levels of such parent-child links, the VAP becomes complex.
