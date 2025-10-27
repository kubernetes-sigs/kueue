# KEP-5800: ClusterQueue-level Resource Exclusion

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
  - [API Changes](#api-changes)
  - [Behavior](#behavior)
  - [Interaction with Global Configuration](#interaction-with-global-configuration)
  - [Test Plan](#test-plan)
      - [Prerequisite testing updates](#prerequisite-testing-updates)
    - [Unit Tests](#unit-tests)
    - [Integration tests](#integration-tests)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
  - [Alternative 1: LocalQueue-level exclusions](#alternative-1-localqueue-level-exclusions)
  - [Alternative 2: Allow ClusterQueue exclusions to override global exclusions](#alternative-2-allow-clusterqueue-exclusions-to-override-global-exclusions)
  - [Alternative 3: Include/exclude lists](#alternative-3-includeexclude-lists)
  - [Alternative 5: Keep only global configuration](#alternative-5-keep-only-global-configuration)
<!-- /toc -->

## Summary

This proposal adds support for specifying resource exclusions at the ClusterQueue level,
allowing administrators to configure which resources should be excluded from quota management
on a per-ClusterQueue basis, rather than only at the global configuration level.

Currently, the `excludeResourcePrefixes` field is only available in the global Kueue Configuration
(config.kueue.x-k8s.io/v1beta2), which applies to all ClusterQueues. This enhancement adds an
`excludeResourcePrefixes` field to the ClusterQueue spec, giving administrators fine-grained
control over resource exclusion policies.

## Motivation

The current implementation of resource exclusion in Kueue only supports a global configuration
through the `excludeResourcePrefixes` field in the Kueue Configuration API. This creates several
challenges for administrators:

1. **All-or-nothing approach**: Administrators must apply resource exclusions globally to all
   ClusterQueues, even when different ClusterQueues may have different resource management requirements.

2. **Operational complexity**: When users need to use resources like huge pages or ephemeral-storage
   that should be excluded from quota management, administrators must modify the global configuration,
   which requires a restart of the Kueue controller.

3. **Limited flexibility**: Different teams or tenants may have different requirements for which
   resources should be managed by Kueue. The global configuration prevents administrators from
   tailoring resource exclusion policies to specific ClusterQueues.

4. **Discovery challenges**: Administrators must anticipate all possible resources that users
   might request across all workloads, making it difficult to maintain an accurate global
   exclusion list.

By moving resource exclusion configuration to the ClusterQueue level, administrators can:
- Apply different resource exclusion policies to different ClusterQueues
- Update exclusion policies without requiring controller restarts
- Simplify resource management by aligning exclusions with ClusterQueue ownership

### Goals

- Add an `excludeResourcePrefixes` field to the ClusterQueue API specification
- Allow ClusterQueue-specific resource exclusions to work independently from global exclusions
- Maintain backward compatibility with existing global `excludeResourcePrefixes` configuration
- Enable dynamic updates to resource exclusions without requiring controller restarts
- Support the combination of both global and ClusterQueue-level exclusions (union of both sets)

### Non-Goals

- Remove or deprecate the global `excludeResourcePrefixes` configuration field
- Automatically migrate existing global exclusion configurations to ClusterQueue-level configurations
- Add resource exclusion capabilities at the LocalQueue level
- Support wildcards or complex pattern matching beyond prefix matching (maintaining consistency
  with existing global behavior)

## Proposal

Add an `excludeResourcePrefixes` field to the ClusterQueue specification that works in conjunction
with the global configuration. Resources matching either the global exclusion prefixes or the
ClusterQueue-specific exclusion prefixes will be excluded from quota management for that ClusterQueue.

### User Stories (Optional)

#### Story 1

As a cluster administrator managing multiple tenants with different resource requirements, I want
to configure different resource exclusion policies for each ClusterQueue so that I can:
- Allow the data science team's ClusterQueue to exclude GPU-related resources from quota management
- Allow the web services team's ClusterQueue to exclude ephemeral-storage from quota management
- Prevent these exclusions from affecting other teams' ClusterQueues

Currently, I must either:
- Use a global configuration that affects all ClusterQueues
- Manually manage these resources in quota calculations for all teams

With ClusterQueue-level exclusions, I can configure each ClusterQueue independently according to
each team's needs.

#### Story 2

As a platform engineer, when a user reports that their workload using huge pages cannot be admitted,
I want to quickly add huge page resources to the exclusion list for their specific ClusterQueue
without:
- Restarting the Kueue controller
- Affecting other ClusterQueues that may want to manage huge pages
- Coordinating a global configuration change that impacts all tenants

With ClusterQueue-level exclusions, I can simply update the ClusterQueue spec to add the exclusion,
and the change takes effect immediately.

### Notes/Constraints/Caveats (Optional)

1. **Union behavior**: When both global and ClusterQueue-level exclusions are defined, the effective
   exclusion list is the union of both sets. A resource excluded by either configuration will be
   excluded from quota management.

2. **Prefix matching semantics**: The ClusterQueue-level exclusions use the same prefix matching
   semantics as the global configuration. For example, `example.com` would match `example.com/gpu`,
   `example.com/memory`, etc.

3. **No resource recovery**: Adding a resource to the exclusion list does not automatically reclaim
   quota for workloads already admitted using that resource. Administrators should plan changes
   carefully.

4. **Validation complexity**: The validation logic must be updated to handle excluded resources
   at the ClusterQueue level in addition to the global level.

### Risks and Mitigations

**Risk**: Administrators might accidentally exclude critical resources (e.g., CPU, memory) from
quota management, breaking workload admission.

**Mitigation**:
- Add validation warnings for common critical resources
- Document best practices and common exclusion patterns
- Provide clear error messages when workloads cannot be admitted due to exclusions

**Risk**: Confusion about the interaction between global and ClusterQueue-level exclusions.

**Mitigation**:
- Clearly document the union behavior
- Add observability (events, logs) that indicate which resources are excluded and why
- Provide examples showing both configurations working together

**Risk**: Performance impact of checking exclusions on a per-ClusterQueue basis during
workload admission.

**Mitigation**:
- Implement efficient prefix matching
- Cache compiled exclusion lists per ClusterQueue
- Monitor performance during testing to ensure no regression

## Design Details

### API Changes

Add the `excludeResourcePrefixes` field to the ClusterQueue spec in `apis/kueue/v1beta2/clusterqueue_types.go`:

```go
type ClusterQueueSpec struct {
	// ... existing fields ...

	// excludeResourcePrefixes defines which resources should be ignored by
	// Kueue for quota management in this ClusterQueue. Resources matching any
	// of the prefixes will be excluded from quota calculations.
	// When specified, this list is combined (union) with the global
	// excludeResourcePrefixes from the Kueue Configuration.
	// The prefix matching follows the same semantics as the global configuration:
	// - An exact match of the resource name
	// - A prefix match followed by a slash (e.g., "example.com" matches "example.com/gpu")
	//
	// Example: ["ephemeral-storage", "hugepages-", "example.com"]
	//
	// +optional
	ExcludeResourcePrefixes []string `json:"excludeResourcePrefixes,omitempty"`
}
```

### Behavior

1. **Resource exclusion logic**:
   - During workload creation and quota calculation, Kueue checks if a resource should be excluded
   - A resource is excluded if it matches any prefix in:
     - The global `Configuration.resources.excludeResourcePrefixes` list, OR
     - The `ClusterQueue.spec.excludeResourcePrefixes` list
   - Excluded resources are not included in quota calculations or admission checks

2. **Update behavior**:
   - Changes to `ClusterQueue.spec.excludeResourcePrefixes` take effect immediately without
     requiring controller restart
   - Existing admitted workloads are not affected by changes to exclusion lists
   - New workload admissions will use the updated exclusion configuration

3. **Validation**:
   - Validate that prefix strings are non-empty
   - Warn if common critical resources (cpu, memory, nvidia.com/gpu) are being excluded
   - Ensure the list doesn't exceed reasonable size limits (e.g., 64 prefixes)

### Interaction with Global Configuration

The ClusterQueue-level exclusions work additively with the global configuration:

```
Effective Exclusions = Global Exclusions ∪ ClusterQueue Exclusions
```

Example:
- Global configuration: `excludeResourcePrefixes: ["example.com"]`
- ClusterQueue A: `excludeResourcePrefixes: ["hugepages-"]`
- ClusterQueue B: `excludeResourcePrefixes: []`

Result:
- ClusterQueue A excludes: resources with prefixes `example.com` or `hugepages-`
- ClusterQueue B excludes: resources with prefix `example.com`

### Test Plan

[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.

##### Prerequisite testing updates

None.

#### Unit Tests

The following packages will need unit test coverage:

- `pkg/cache`: Test ClusterQueue cache correctly handles excluded resources at CQ level
- `pkg/workload`: Test workload resource calculation with ClusterQueue-level exclusions
- `pkg/controller/core/`: Test workload admission with mixed global and CQ exclusions
- `pkg/scheduler/`: Test quota calculations with excluded resources

Coverage targets:
- `pkg/cache`: >85%
- `pkg/workload`: >85%
- `pkg/scheduler/`: >80%

Test scenarios:
- Resource excluded by global config only
- Resource excluded by ClusterQueue config only
- Resource excluded by both (union behavior)
- Resource not excluded by either
- Edge cases (empty strings, special characters, etc.)

#### Integration tests

The following integration test scenarios will be added:

1. **Basic functionality**:
   - Create ClusterQueue with `excludeResourcePrefixes: ["ephemeral-storage"]`
   - Submit workload requesting CPU, memory, and ephemeral-storage
   - Verify only CPU and memory are checked against quota
   - Verify workload is admitted even if ephemeral-storage exceeds nominal quota

2. **Union behavior with global config**:
   - Set global `excludeResourcePrefixes: ["example.com"]`
   - Create ClusterQueue with `excludeResourcePrefixes: ["hugepages-"]`
   - Submit workload requesting resources with both prefixes
   - Verify both are excluded from quota management

3. **Dynamic updates**:
   - Create ClusterQueue with no exclusions
   - Submit workload requesting ephemeral-storage
   - Verify ephemeral-storage is checked against quota
   - Update ClusterQueue to exclude ephemeral-storage
   - Submit new workload requesting ephemeral-storage
   - Verify new workload excludes ephemeral-storage from quota checks

4. **Multiple ClusterQueues with different exclusions**:
   - Create ClusterQueue A excluding `["hugepages-"]`
   - Create ClusterQueue B excluding `["ephemeral-storage"]`
   - Submit workloads to both queues with different resource requirements
   - Verify each queue applies its own exclusion policy correctly

5. **Prefix matching behavior**:
   - Create ClusterQueue with `excludeResourcePrefixes: ["example.com"]`
   - Submit workloads requesting:
     - `example.com/gpu`
     - `example.com/memory`
     - `other.com/resource`
   - Verify only resources with `example.com` prefix are excluded

### Graduation Criteria

This feature will follow the standard Kueue graduation process:

**Alpha (v0.X)**:
- Feature implemented behind a feature gate (default off)
- API design reviewed and approved
- Basic unit and integration tests passing
- Documentation draft available

**Beta (v0.Y)**:
- Feature gate enabled by default
- Comprehensive test coverage (>80% for affected packages)
- Real-world usage feedback from at least 2 organizations
- Performance benchmarks show no regression
- Complete documentation with examples
- Validation logic hardened based on alpha feedback

**GA (v1.0 or later)**:
- Feature gate removed (always enabled)
- At least 2 releases in beta with no major issues
- Multiple production deployments using the feature
- Full API stability guarantees
- Comprehensive troubleshooting guide

## Implementation History

- 2025-10-27: KEP created and submitted for review
- TBD: Alpha implementation
- TBD: Beta graduation
- TBD: GA graduation

## Drawbacks

1. **Increased API complexity**: Adding another configuration field increases the cognitive load
   for administrators who must understand the interaction between global and ClusterQueue-level
   exclusions.

2. **Potential for misconfiguration**: With more configuration options, there's increased risk
   of administrators accidentally excluding critical resources or creating confusing
   configurations.

3. **Testing burden**: The combination of global and ClusterQueue-level exclusions creates
   additional test scenarios that must be maintained.

4. **Documentation overhead**: Clear documentation is needed to explain when to use global vs.
   ClusterQueue-level exclusions and how they interact.

## Alternatives

### Alternative 1: LocalQueue-level exclusions

Instead of ClusterQueue-level exclusions, support exclusions at the LocalQueue level.

**Pros**:
- Even more fine-grained control
- Users could manage their own exclusions

**Cons**:
- Violates the principle that resource management is an admin concern
- Significantly more complex to implement and reason about
- Could lead to inconsistent behavior across LocalQueues in the same ClusterQueue
- Increases security concerns (users shouldn't control quota management)

**Decision**: Rejected. ClusterQueue is the appropriate level for quota management configuration.

### Alternative 2: Allow ClusterQueue exclusions to override global exclusions

Instead of union behavior, let ClusterQueue exclusions completely replace global exclusions.

**Pros**:
- More flexible for ClusterQueues that want different exclusion policies
- Simpler mental model (no union logic)

**Cons**:
- Administrators must duplicate global exclusions in each ClusterQueue that needs them
- More error-prone (forgetting to include a global exclusion)
- Breaks the principle of global configuration providing baseline policies

**Decision**: Rejected. Union behavior is safer and follows the principle of least surprise.

### Alternative 3: Include/exclude lists

Instead of just exclusion lists, support both inclusion and exclusion lists for resources.

**Pros**:
- More expressive configuration
- Could explicitly allow only certain resources

**Cons**:
- Significantly more complex to implement and document
- Interaction between include and exclude lists is confusing
- Not aligned with current global configuration design
- Higher risk of misconfiguration

**Decision**: Rejected. Keep it simple with exclusion-only, matching the current global design.

### Alternative 5: Keep only global configuration

Improve documentation and tooling around the global configuration instead of adding
ClusterQueue-level support.

**Pros**:
- No API changes needed
- Simpler implementation

**Cons**:
- Doesn't solve the fundamental problem of different ClusterQueues needing different policies
- Still requires controller restarts
- Doesn't address the operational complexity for administrators

**Decision**: Rejected. This doesn't address the core use case.
