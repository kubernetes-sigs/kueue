# KEP-7513: Include resource prefixes

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1: Focus on GPU management](#story-1-focus-on-gpu-management)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [API Changes](#api-changes)
  - [Implementation Details](#implementation-details)
  - [Relation with ClusterQueue](#relation-with-clusterqueue)
  - [Interaction with excludeResourcePrefixes](#interaction-with-excluderesourceprefixes)
  - [Test Plan](#test-plan)
    - [Unit Tests](#unit-tests)
    - [Integration tests](#integration-tests)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
  - [Continue using excludeResourcePrefixes](#continue-using-excluderesourceprefixes)
<!-- /toc -->

## Summary

This KEP proposes to introduce a new configuration field `includeResourcePrefixes`
that acts as an allow list of resources that Kueue will manage. Any resource not added
to this list is exempt from quota management. This addresses the operational challenge
of Kueue managing workloads in environments where mutating webhooks or controllers add
unexpected resources to pod specifications.

## Motivation

Currently, Kueue counts all resources in pod requests/limits during admission,
except for those explicitly excluded via `excludeResourcePrefixes`. This works well when the
set of resources used in a cluster doesn't change much. However, in practice, many Kueue users
face operational challenges:

1. **Mutating webhooks add unexpected resources**: When a mutating webhooks injects
    resources into pod specifications these resources appear in pod requests/limits.

2. **Workloads become blocked**: When a new resource isn't ignored in the
   `excludeResourcePrefixes`, Kueue cannot admit workloads because the `ClusterQueue` doesn't have
   the Quota configured.

3. **Reactive maintenance burden**: Platform operators must add entries to
   `excludeResourcePrefixes` whenever a new resource appears that is not on `ClusterQueue`.

4. **Most users care about a small set of resources**: In practice, most Kueue deployments
   are primarily concerned with managing a small, well-known set of resources (CPU, memory,
   and specific accelerators like GPUs), not all the resources that can appear in pod specs.

The current exclude-based approach assumes users want to manage all resources by default and
opt-out of specific ones. For many deployments, an include-based approach that assumes users
want to manage only specific resources would be more appropriate and simpler to operate.

### Goals

- Provide a configuration option to specify an allow list of resource prefixes that Kueue
  will manage for quota purposes
- When configured, only resources matching the allow list prefixes will be counted toward
  quota admission decisions
- Maintain support for the existing `excludeResourcePrefixes` configuration
- Reduce the operational burden for cluster administrators in environments with dynamic
  resource injection

### Non-Goals

- Removing or deprecating the existing `excludeResourcePrefixes` field
- Automatically detecting which resources should be managed
- Modifying workload admission logic beyond resource filtering

## Proposal

Add a new configuration field `includeResourcePrefixes` to the Kueue Configuration API.
When this field is set, only resources whose names match one of the specified prefixes
will be counted when making quota admission decisions. Resources not matching any prefix
in the allow list will be ignored for quota purposes.

### User Stories

#### Story 1: Focus on GPU management

As a training platform operator, I primarily care about managing GPU quota across teams.
While pods request CPU and memory, I want to focus on tracking and limiting GPU usage.

With `includeResourcePrefixes`, I can configure:
```yaml
includeResourcePrefixes:
  - nvidia.com/
```

This allows me to manage only GPU resources without worrying about other resources that
might appear in pod specifications.

### Risks and Mitigations

**Risk**: Users might misconfigure `includeResourcePrefixes` and accidentally not include
important resources from quota management.

**Mitigation**:
- Clear documentation with examples of common configurations
- Validation error when both `includeResourcePrefixes` and `excludeResourcePrefixes`
  are set

## Design Details

### API Changes

Add a new field to the Kueue `Configuration`:

```go
type Configuration struct {
    // ...
    // ExcludeResourcePrefixes defines resource prefixes that will be excluded
    // from quota management. Resources matching any of these prefixes won't
    // be counted toward quota.
    // Cannot be used together with IncludeResourcePrefixes.
    ExcludeResourcePrefixes []string `json:"excludeResourcePrefixes,omitempty"`

    // IncludeResourcePrefixes defines resource prefixes that will be included
    // in quota management. When set, only resources matching one of these
    // prefixes will be counted toward quota. Resources not matching any prefix
    // will be ignored.
    // Cannot be used together with ExcludeResourcePrefixes.
    // +optional
    IncludeResourcePrefixes []string `json:"includeResourcePrefixes,omitempty"`
}
```

### Implementation Details

The resource filtering logic will be modified to:

1. If `includeResourcePrefixes` is set and non-empty:
   - For each resource in the pod's requests/limits, check if the resource name has a
     prefix matching any entry in `includeResourcePrefixes`
   - Only include the resource in quota calculations if it matches
   - Pod resources not matching any prefix are ignored from quota calculations

2. If `includeResourcePrefixes` is set and empty:
    - Pod resources are ignored from quota calculations

3. If `includeResourcePrefixes` and `excludeResourcePrefixes` are set:
   - Reject the configuration during validation (mutually exclusive)

4. If neither is set:
   - All Pod resources are managed (existing default behavior)

### Relation with ClusterQueue

1. If Resource prefix is set in `includeResourcePrefixes` and its quota is defined in ClusterQueue:
    - Workload quota is evaluated and workload is admitted
2. If Resource prefix is set in `includeResourcePrefixes` and its quota is not defined in ClusterQueue:
    - Mandatory Resource not included in ClusterQueue. Workload is suspended
3. If Resource prefix is not set in `includeResourcePrefixes` and Resource quota is not defined in ClusterQueue:
    - Kueue does not care about the resource quota and it should be admitted
4. If Resource prefix is not set in `includeResourcePrefixes` and Resource quota is defined in ClusterQueue:
    - Kueue does not evaluate the resource quota.

### Interaction with excludeResourcePrefixes

To maintain simplicity and avoid confusion, we can make `includeResourcePrefixes`
and `excludeResourcePrefixes` mutually exclusive by making the configuration validation
 reject configurations where both fields are set.

### Test Plan

[X] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.

#### Unit Tests

- In the test validation for configuration:
  - Valid configurations with `includeResourcePrefixes` set
  - Rejection when both `includeResourcePrefixes` and `excludeResourcePrefixes` are set
  - Empty `includeResourcePrefixes` is treated as any Pod resource are exempt from quota management

- In the workload test resource filtering logic:
  - Resources matching include prefixes are counted
  - Resources not matching include prefixes are ignored
  - Edge cases: empty resource names, malformed resource names

#### Integration tests

- Scheduler tests with `includeResourcePrefixes` configured:
  - Workloads requesting included resources with quota defined in the `ClusterQueue` are admitted normally
  - Workloads requesting included resources without quota defined in the `ClusterQueue` are suspended
  - Workloads requesting both included and non-included resources have only included resources counted
  - Workloads requesting non-included resources without quota defined in the `ClusterQueue` moves to running as Kueue doesn't care about it

### Graduation Criteria

This feature will follow the standard Kueue graduation criteria:

**Alpha**:
- API changes implemented behind a feature gate, which is disabled by default
- Basic functionality working with unit tests
- Documentation covering the feature

**Beta**:
- Positive feedback from Alpha
- Feature gate enabled by default
- Integration tests added

**Stable**:
- Feature has been in beta for at least one release
- No major bugs reported

## Implementation History

- 2025-12-22: First draft of the KEP

## Drawbacks

- Adds another configuration option
- Users need to understand the difference between include and exclude approaches
- Users who want to manage "all except a few" resources will find exclude approach
  simpler than include approach
- Requires documentation and potential migration for users who want to switch approaches

## Alternatives

### Continue using excludeResourcePrefixes

Users can continue using the existing `excludeResourcePrefixes` approach. However, this
requires maintaining an up-to-date list of excluded resources and reactively adding to it
whenever new resources appear.

Advantages:
- No API changes needed
- Works for users who want to manage most resources

Disadvantages:
- Reactive maintenance burden
- Workloads get blocked when new resources appear
- Doesn't solve the core problem described in the motivation