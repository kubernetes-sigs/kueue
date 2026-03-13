# KEP-7513: Quota check

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1: Ignore resources not declared in clusterQueue](#story-1-ignore-resources-not-declared-in-clusterqueue)
    - [Story 2: Block resource not declared in clusterQueue](#story-2-block-resource-not-declared-in-clusterqueue)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [API Changes](#api-changes)
  - [Feature Gate](#feature-gate)
  - [Implementation Details](#implementation-details)
  - [Interaction with excludeResourcePrefixes](#interaction-with-excluderesourceprefixes)
    - [When quotaCheckStrategy: BlockUndeclared (default)](#when-quotacheckstrategy-blockundeclared-default)
    - [When quotaCheckStrategy: IgnoreUndeclared](#when-quotacheckstrategy-ignoreundeclared)
  - [Test Plan](#test-plan)
    - [Integration Tests](#integration-tests)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
  - [Continue using excludeResourcePrefixes](#continue-using-excluderesourceprefixes)
  - [Add another API includeResourcePrefixes](#add-another-api-includeresourceprefixes)
<!-- /toc -->

## Summary

This KEP proposes to introduce a new configuration API which allows to restrict the
quota check to only resources which are declared in the `ClusterQueue`. Other resources
requested by the Pod are skipped from the quota checks.

## Motivation

Currently, Kueue counts all resources in pod requests/limits during admission,
except for those explicitly excluded via `excludeResourcePrefixes`. This works well when the
set of resources used in a cluster doesn't change much. However, in practice, many Kueue users
face operational challenges:

1. **Mutating webhooks add unexpected resources**: When a mutating webhook injects
    resources into pod specifications, these resources appear in pod requests/limits.

2. **Workloads become blocked**: When a new resource isn't ignored in the
   `excludeResourcePrefixes`, Kueue cannot admit workloads because the `ClusterQueue`
   doesn't have the quota configured.

3. **Reactive maintenance burden**: Platform operators must add entries to
   `excludeResourcePrefixes` whenever a new resource appears that is not on `ClusterQueue`.

4. **Most users care about a small set of resources**: In practice, most Kueue deployments
   are primarily concerned with managing a small, well-known set of resources (CPU, memory,
   and specific accelerators like GPUs), not all the resources that can appear in pod specs.

The current exclude-based approach assumes users want to manage all resources by default and
opt-out of specific ones. For many deployments, an include-based approach that assumes users
want to manage only specific resources would be more appropriate and simpler to operate.

### Goals

- Provide a single configuration option to specify how Kueue should manage the resources for quota purposes
- Reduce the operational burden for cluster administrators in environments with dynamic
  resource injection

### Non-Goals

- Modifying workload admission logic beyond resource filtering
- Impact on TAS calculation for resources placement

## Proposal

Add a new configuration option `resources.quotaCheckStrategy` to the Kueue Configuration API. When this field is
set with `IgnoreUndeclared` value, resources declared in the Workload's PodTemplate that are not declared
in the `ClusterQueue` are ignored for quota admission decisions. When the field has value `BlockUndeclared`,
resources declared in the Workload's PodTemplate that are not declared in the `ClusterQueue` are taken into
account for quota admission decisions.

### User Stories

#### Story 1: Ignore resources not declared in clusterQueue

As a training platform operator, I primarily care about managing GPU and Memory quota across teams.
While jobs request CPU and GPU, I want to focus on tracking and limiting GPU and Memory usage.

With `quotaCheckStrategy`, I can configure:
```yaml
apiVersion: config.x-k8s.io/v1beta2
kind: Configuration
...
resources:
  quotaCheckStrategy: "IgnoreUndeclared"
...
```

Then the `clusterQueue` could be configured like the following:
```yaml
apiVersion: kueue.x-k8s.io/v1beta2
kind: ClusterQueue
metadata:
  name: "cluster-queue"
spec:
  namespaceSelector: {} # match all.
  resourceGroups:
  - coveredResources: ["nvidia.com/gpu"]
    flavors:
    - name: "nvidia"
      resources:
      - name: "nvidia.com/gpu"
        nominalQuota: 10
     - name: "memory"
       nominalQuota: 36Gi
```

This allows me to manage only GPU and Memory resources without worrying about other resources that
might appear in job specifications. For example, the following job declares cpu and nvidia.com/gpu,
but only nvidia.com/gpu is taken into account for quota as is the only declared in the `ClusterQueue`.
The additional resource defined in the `ClusterQueue`, which is memory, won't have an impact on the quota
as is not being requested by the Workload.

```yaml
apiVersion: batch/v1
kind: Job
...
spec:
  template:
    spec:
      containers:
      - name: dummy-job
        ...
        resources:
          requests:
            cpu: "3"
            nvidia.com/gpu: "10"
```
#### Story 2: Block resource not declared in clusterQueue

I want for every workload resource quota to be managed by Kueue.

With `quotaCheckStrategy`, I can configure:
```yaml
apiVersion: config.x-k8s.io/v1beta2
kind: Configuration
...
resources:
  quotaCheckStrategy: "BlockUndeclared"
...
```

Then the clusterQueue could be configured like the following:
```yaml
apiVersion: kueue.x-k8s.io/v1beta2
kind: ClusterQueue
metadata:
  name: "cluster-queue"
spec:
  namespaceSelector: {} # match all.
  resourceGroups:
  - coveredResources: ["nvidia.com/gpu"]
    flavors:
    - name: "nvidia"
      resources:
      - name: "nvidia.com/gpu"
        nominalQuota: 10
```

The following job declares cpu, memory and nvidia.com/gpu, and all resources are taken into account for
quota, even the ones that are not declared in the `ClusterQueue`. This means that the workload will be
suspended, as there is no flavors defined for cpu and memory in the `ClusterQueue`.

```yaml
apiVersion: batch/v1
kind: Job
...
spec:
  template:
    spec:
      containers:
      - name: dummy-job
        ...
        resources:
          requests:
            cpu: "3"
            memory: "40Gi"
            nvidia.com/gpu: "10"
```

### Risks and Mitigations

**Risk**: Users might get confused on what is being managed and what is not.

**Mitigation**:
- Provide logging with resources that are being skipped.

## Design Details

### API Changes

Add a new type and field to the Kueue `Resources`:

```go
// QuotaCheckStrategy determines how Kueue checks resources against quota
// during admission.
type QuotaCheckStrategy string

const (
	// QuotaCheckBlockUndeclared means all resources defined in the workload are checked against quota,
	// except those matching ExcludeResourcePrefixes.
	QuotaCheckBlockUndeclared QuotaCheckStrategy = "BlockUndeclared"

	// QuotaCheckIgnoreUndeclared means only resources defined in the workload that are declared in the
  // ClusterQueue's coveredResources are checked against quota. Resources undeclared in the clusterQueue
  // are ignored. ExcludeResourcePrefixes is not allowed in combination with this strategy.
	QuotaCheckIgnoreUndeclared QuotaCheckStrategy = "IgnoreUndeclared"
)

type Resources struct {
    // ...
    // QuotaCheckStrategy determines which resources are considered during quota admission.
    // +optional
    QuotaCheckStrategy *QuotaCheckStrategy `json:"quotaCheckStrategy,omitempty"`
}
```

### Feature Gate

This feature is gated behind the `QuotaCheckStrategy` feature gate.

- **Alpha**: The feature gate is disabled by default. Users must explicitly enable
  it to use `quotaCheckStrategy: "IgnoreUndeclared"`. When the gate is disabled, the `quotaCheckStrategy`
  configuration field is ignored and the behavior is equivalent to `BlockUndeclared`.
- **Beta**: The feature gate is enabled by default.
- **GA**: The feature gate is locked to enabled and cannot be disabled.

### Implementation Details

The flavor assigner logic will be modified to:

1. If `quotaCheckStrategy` is set with value `IgnoreUndeclared`:
   - Only resources from Workload that match resources defined
     in the `ClusterQueue` are used for quota check.
   - Any Workload resource undeclared in the `ClusterQueue` will
     be ignored for flavor admission.
   - Any `ClusterQueue` resources that are not in the Workload will be
     skipped for quota check.

2. If `quotaCheckStrategy` is set with value `BlockUndeclared`, which would be the default:
    - All Workload resource will be used for quota calculations.
    - Any resource from Workload undeclared in `ClusterQueue` coveredResources are blocked.
    - Any resources defined in the `excludeResourcePrefixes` remain being excluded

### Interaction with excludeResourcePrefixes

The interaction between `quotaCheckStrategy` and `excludeResourcePrefixes` depends on the value
of `quotaCheckStrategy`:

#### When quotaCheckStrategy: BlockUndeclared (default)

This maintains **backward compatibility** with current Kueue behavior:

- **With `excludeResourcePrefixes`**: All Workload's resources are considered for quota EXCEPT
  those matching any prefix in `excludeResourcePrefixes`.
- **Without `excludeResourcePrefixes`**: All Workload's resources are considered for quota.

#### When quotaCheckStrategy: IgnoreUndeclared

The `excludeResourcePrefixes` field is **not allowed** and is blocked by validation.

### Test Plan

[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.

#### Integration Tests

- when `IgnoreUndeclared` is used check:
  - if workload has resources not declared in the `ClusterQueue`, it is admitted
  - if all resources are declaredin the `ClusterQueue`, normal quota checks applies
- when `BlockUndeclared` is used check:
  - if any workload resources are not declared in the `ClusterQueue`, suspend workload

### Graduation Criteria

This feature will follow the standard Kueue graduation criteria:

**Alpha**:
- API changes implemented behind the `QuotaCheckStrategy` feature gate, which is disabled by default
- Basic functionality working with unit tests
- Documentation covering the feature

**Beta**:
- Positive feedback from Alpha
- Feature gate enabled by default
- Integration tests added

**Stable**:
- Re-evaluate deprecation and dropping of the `excludeResourcePrefixes`
- Feature has been in beta for at least one release
- No major bugs reported

## Implementation History

- 2025-12-22: First draft of the KEP

## Drawbacks

- Adds another configuration option
- Users need to understand the difference between `quotaCheckStrategy` and `excludeResourcePrefixes` approaches

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

### Add another API includeResourcePrefixes

Users can use `includeResourcePrefixes` to specify an allow list of resource prefixes that
Kueue will manage for quota purposes. When configured, only resources matching the allow
list prefixes will be counted toward quota admission decisions.

Advantages:
- Closer to how existent `excludeResourcePrefixes` API works

Disadvantages:
- Confusion between what is enforced for quota check on `ClusterQueue` and
  `includeResourcePrefixes`. For example, if `includeResourcePrefixes` doesn't define
  cpu, but a Pod has cpu defined and the `ClusterQueue` has cpu, should the quota be
  evaluated or ignored for that resource?
