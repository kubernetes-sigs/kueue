# KEP-7513: Quota check

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
  - [Feature Gate](#feature-gate)
  - [Implementation Details](#implementation-details)
  - [Interaction with excludeResourcePrefixes](#interaction-with-excluderesourceprefixes)
    - [When quotaCheck: All (default)](#when-quotacheck-all-default)
    - [When quotaCheck: OnlyDeclared](#when-quotacheck-onlydeclared)
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

This KEP proposes to introduce a new configuration field `quotaCheck`
that acts as a way to control which resources should be managed by Kueue. The `quotaCheck`
can have the value of `OnlyDeclared`, which means only resources declared in the `ClusterQueue`
can have an impact in the admission of the Pod. It can also have the value of `All`, which
means all the resources defined on the Pod requests will have an impact in the admission
of the workload.

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
- Deprecate existing `excludeResourcePrefixes` API configuration
- Reduce the operational burden for cluster administrators in environments with dynamic
  resource injection

### Non-Goals

- Modifying workload admission logic beyond resource filtering
- Impact on TAS calculation for resources placement

## Proposal

Add a new configuration option `resources.quotaCheck` to the Kueue Configuration API.
When this field is set with `OnlyDeclared` value, only resources declared in the `ClusterQueue` are taken into account for quota admission decisions, any resources not listed in the `ClusterQueue` are ignored. When the field has value `All`, which would be the default, all the resources defined in the Pod are taken into account for quota admission decisions.

### User Stories

#### Story 1: Focus on GPU management

As a training platform operator, I primarily care about managing GPU quota across teams.
While pods request CPU and memory, I want to focus on tracking and limiting GPU usage.

With `quotaCheck`, I can configure:
```yaml
resources:
  quotaCheck: "OnlyDeclared"
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

This allows me to manage only GPU resources without worrying about other resources that
might appear in pod specifications.

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
// +kubebuilder:validation:Enum=All;OnlyDeclared
type QuotaCheckStrategy string

const (
	// QuotaCheckAll means all resources in Pod requests are checked against quota,
	// except those matching ExcludeResourcePrefixes.
	QuotaCheckAll QuotaCheckStrategy = "All"

	// QuotaCheckOnlyDeclared means only resources declared in the ClusterQueue's
	// coveredResources are checked against quota. Undeclared resources are ignored.
	QuotaCheckOnlyDeclared QuotaCheckStrategy = "OnlyDeclared"
)

type Resources struct {
    // ...
    // QuotaCheck determines which resources are considered during quota admission.
    // +kubebuilder:default="All"
    // +optional
    QuotaCheck QuotaCheckStrategy `json:"quotaCheck,omitempty"`
}
```

### Feature Gate

This feature is gated behind the `QuotaCheck` feature gate.

- **Alpha**: The feature gate is disabled by default. Users must explicitly enable
  it to use `quotaCheck: "OnlyDeclared"`. When the gate is disabled, the `quotaCheck`
  configuration field is ignored and the behavior is equivalent to `All`.
- **Beta**: The feature gate is enabled by default.
- **GA**: The feature gate is locked to enabled and cannot be disabled.

### Implementation Details

The flavor assigner logic will be modified in the [`findFlavorForPodSets`](https://github.com/kubernetes-sigs/kueue/blob/fe64c96e32130b15e9e5898ffaa1e145761d49d8/pkg/scheduler/flavorassigner/flavorassigner.go#L751) method to:

1. If `quotaCheck` is set with value `OnlyDeclared`:
   - Only resources that match resources defined in the `ClusterQueue` are used for
     quota check.
   - Any resources defined in the Pod that are not defined in the `ClusterQueue` will
     be skipped for flavor admission.
   - Any resources defined in the `ClusterQueue` that are not in the Pod will be
     skipped for quota check.

2. If `quotaCheck` is set with value `All`, which would be the default:
    - No change is necessary in the `findFlavorForPodSets` method.
    - All resources defined in the Pod will be used for quota calculations.
    - Any resources defined in the `excludeResourcePrefixes` remain being excluded
      in the [`totalRequestsFromPodSets`](https://github.com/kubernetes-sigs/kueue/blob/fe64c96e32130b15e9e5898ffaa1e145761d49d8/pkg/workload/workload.go#L517).

### Interaction with excludeResourcePrefixes

The interaction between `quotaCheck` and `excludeResourcePrefixes` depends on the value
of `quotaCheck`:

#### When quotaCheck: All (default)

This maintains **backward compatibility** with current Kueue behavior:

- **With `excludeResourcePrefixes`**: All pod resources are considered for quota EXCEPT
  those matching any prefix in `excludeResourcePrefixes`.
- **Without `excludeResourcePrefixes`**: All pod resources are considered for quota.

#### When quotaCheck: OnlyDeclared

The `excludeResourcePrefixes` field is **ignored** and has no effect:

- Only resources explicitly listed in ClusterQueue's `coveredResources` are considered.
- If both are set, a **warning** is logged but configuration is accepted.

### Test Plan

[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.

#### Integration Tests

- when onlyDeclared is used check:
  - if workload with undeclared resources on ClusterQueue is admitted
  - if all resources are declared, normal quota checks applies
- when All is used, if the workload with undeclared resources is rejected

### Graduation Criteria

This feature will follow the standard Kueue graduation criteria:

**Alpha**:
- API changes implemented behind the `QuotaCheck` feature gate, which is disabled by default
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
- Users need to understand the difference between `quotaCheck` and exclude approaches

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
  cpu, but a Pod has cpu defined and the ClusterQueue has cpu, should the quota be
  evaluated or ignored for that resource?
