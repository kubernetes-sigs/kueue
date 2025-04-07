# KEP-3122: Expose Flavors in LocalQueue Status

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
  - [API](#api)
  - [Implementation overview](#implementation-overview)
  - [Future works](#future-works)
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

This KEP introduces a new status field in LocalQueue, allowing users to see
all currently available ResourceFlavors for the LocalQueue.

## Motivation

Currently, users without RBAC access to ResourceFlavors cannot view the list
of available flavors. Depending on the RBAC rules, users might also lack read
access to ClusterQueues. Providing users with information about the available
flavors is useful, as it gives them an idea of the capabilities provided by
a LocalQueue (e.g., a flavor might include newer GPUs).

### Goals

- Provide a possibility to see all currently available ResourceFlavors in 
  the LocalQueue.

### Non-Goals

- Verify that the ResourceFlavors exist and show only the existing flavors.
- The stopPolicies are never considered in the LocalQueue flavors status field.

## Proposal

Introduce a new status field `flavors` in LocalQueue 
that will be updated when ClusterQueue flavors are modified.

### User Stories (Optional)

#### Story 1

As a user I want to see the list of all ResourceFlavors available in each LocalQueue 
due to the RBAC configuration for ClusterQueue in my cluster I cannot inspect the 
ClusterQueue objects directly, only LocalQueues.

### Notes/Constraints/Caveats (Optional)

### Risks and Mitigations

Risk: Increased size of the status object due to adding 16 resource flavors 
to the LocalQueue.

Mitigation: The number of available flavor names is limited to 16, so this 
additional field does not significantly impact performance.

## Design Details

### API

Create `LocalQueueFlavorStatus` API object:

```go
type LocalQueueFlavorStatus struct {
  // name of the flavor.
  // +required
  // +kubebuilder:validation:Required
  Name ResourceFlavorReference `json:"name"`
  
  // resources used in the flavor.
  // +listType=set
  // +kubebuilder:validation:MaxItems=16
  // +optional
  Resources []corev1.ResourceName `json:"resources,omitempty"`

  // nodeLabels are labels that associate the ResourceFlavor with Nodes that
  // have the same labels.
  // +mapType=atomic
  // +kubebuilder:validation:MaxProperties=8
  // +optional
  NodeLabels map[string]string `json:"nodeLabels,omitempty"`

  // nodeTaints are taints that the nodes associated with this ResourceFlavor
  // have.
  // +listType=atomic
  // +kubebuilder:validation:MaxItems=8
  // +optional
  NodeTaints []corev1.Taint `json:"nodeTaints,omitempty"`

  // topology is the topology that associated with this ResourceFlavor.
  //
  // This is an alpha field and requires enabling the TopologyAwareScheduling
  // feature gate.
  //
  // +optional
  Topology *TopologyInfo `json:"topology,omitempty"`
}
```

Add `TopologyInfo` API object:

```go
type TopologyInfo struct {
  // name is the name of the topology.
  //
  // +required
  // +kubebuilder:validation:Required
  Name TopologyReference `json:"name"`

  // levels define the levels of topology.
  //
  // +required
  // +listType=atomic
  // +kubebuilder:validation:Required
  // +kubebuilder:validation:MinItems=1
  // +kubebuilder:validation:MaxItems=8
  Levels []string `json:"levels"`
}
```

Modify `LocalQueueStatus` API object:

```go
// LocalQueueStatus defines the observed state of LocalQueue
type LocalQueueStatus struct {
	...
	// flavors lists all currently available ResourceFlavors in specified ClusterQueue.
	//
	// +listType=map
	// +listMapKey=name
  // +kubebuilder:validation:MaxItems=16
  // +optional
	Flavors []LocalQueueFlavorStatus `json:"flavors,omitempty"`
}
```

### Implementation overview

Modify `LocalQueueUsageStats` object:

```go
type LocalQueueUsageStats struct {
  ...
	Flavors []kueue.ResourceFlavorReference
}
```

Get available `Flavors` from `cqImpl.ResourceGroups` in `cache.LocalQueueUsage(...)` 
method and update `Flavors` field on `UpdateStatusIfChanged(...)`
on each LocalQueue reconcile when it was updated.

### Future works

In the future, we can also add the `availableCoveredResources` field. This field allows 
batch users to understand which resources may be available for the LocalQueue.


### Test Plan

<!--
**Note:** *Not required until targeted at a release.*
The goal is to ensure that we don't accept enhancements with inadequate testing.

All code is expected to have adequate tests (eventually with coverage
expectations). Please adhere to the [Kubernetes testing guidelines][testing-guidelines]
when drafting this test plan.

[testing-guidelines]: https://git.k8s.io/community/contributors/devel/sig-testing/testing.md
-->

[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.

##### Prerequisite testing updates

<!--
Based on reviewers feedback describe what additional tests need to be added prior
implementing this enhancement to ensure the enhancements have also solid foundations.
-->

None.

#### Unit Tests

Existing unit tests should be updated to tests whether the new data are correctly
passed and applied on CRD.

#### Integration tests

Existing integration tests should be updated to tests whether the new data are correctly
passed and applied on CRD.

Additionally, add an integration test case to check that Flavors are updated after 
forcefully removing the ClusterQueue while ignoring validations.

### Graduation Criteria

We will graduate this feature to stable together with the whole LocalQueue API.

## Implementation History

2024-10-02 KEP

## Drawbacks

## Alternatives