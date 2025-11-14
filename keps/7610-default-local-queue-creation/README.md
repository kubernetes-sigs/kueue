# KEP-7610: Create Default LocalQueues Based on NamespaceSelector

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [API Proposal](#api-proposal)
  - [Controller Logic](#controller-logic)
  - [Test Plan](#test-plan)
      - [Prerequisite testing updates](#prerequisite-testing-updates)
    - [Unit Tests](#unit-tests)
    - [Integration tests](#integration-tests)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
  - [Improvements for future versions](#improvements-for-future-versions)
<!-- /toc -->

## Summary

This KEP proposes a change to the `ClusterQueue` API to introduce an opt-in
feature that automatically creates a default `LocalQueue` in namespaces that
match a `ClusterQueue`'s `namespaceSelector`. This avoids the need for administrators
to manually create a `LocalQueue` in each namespace. 

## Motivation

Kueue `ClusterQueue` can select which namespaces are allowed to submit workloads
via a `namespaceSelector`. However, this selection only serves as an admission
rule. For users to actually submit workloads, an administrator must still
manually create a `LocalQueue` resource in that namespace and point it to the
appropriate `ClusterQueue`. This manual step is repetitive and could be
automated.

Automating the creation of a default `LocalQueue` makes the administrator's
experience much smoother. When a namespace is created or labeled to match a
`ClusterQueue`'s selector, the corresponding `LocalQueue` will be provisioned
automatically, making the namespace immediately ready for workload submission.

### Goals

- Automate the creation of a default `LocalQueue` within a namespace when its
  labels match a `ClusterQueue`'s `namespaceSelector`.
- Provide a clear, opt-in mechanism on the `ClusterQueue` to enable and
  configure this behavior.
- Prevent name conflicts and undefined behavior by validating that no two
  `ClusterQueue`s with this feature enabled have overlapping `namespaceSelector`s.
- Ensure that automatically created `LocalQueue`s are clearly identifiable via
  labels and annotations for easy discovery and management.

### Non-Goals

- Automatically deleting the `LocalQueue` if a namespace no longer matches the
  selector. The initial implementation will leave the `LocalQueue` in place to
  avoid disrupting active workloads. Cleanup will remain a manual administrative
  task.
- A "garbage collection" mechanism for orphaned, auto-generated `LocalQueue`s.
  This could be considered in a future iteration.
- Supporting complex configurations for the auto-generated `LocalQueue` beyond
  its name.

## Proposal

This proposal introduces a new, optional field `defaultLocalQueue` to the
`ClusterQueue` API specification. When this field is present, it signals the
controller to enable the automatic creation of `LocalQueue`s. This field will be
an object containing the configuration for the `LocalQueue` to be created,
starting with a required `name` field.

The existing `clusterqueue-controller` will be extended to manage this logic. It
will watch `Namespace`s in addition to `ClusterQueue`s. When a namespace is
created or updated to match the `namespaceSelector` of a `ClusterQueue` with
this feature enabled, the controller will create a `LocalQueue` with the
specified name in that namespace.

To prevent conflicts where two `ClusterQueue`s might try to create a `LocalQueue`
with the same name in the same namespace, new logic to the existing `ClusterQueue`
admission webhook will be added. This webhook will reject the creation or update
of a `ClusterQueue` if its `namespaceSelector` (with `defaultLocalQueue` enabled)
overlaps with an existing `ClusterQueue` that also has the feature enabled.



### Risks and Mitigations

Risk: Race Conditions and Existing `LocalQueue`s

A `LocalQueue` with the configured name might already exist in a target
namespace, either created manually or by another process.

- Mitigation 1: The admission webhook will reject a `ClusterQueue` if a
  `LocalQueue` with the target name already exists in a namespace matched by the
  selector at the time of `ClusterQueue` creation/update.

- Mitigation 2: The `clusterqueue-controller`'s runtime logic will be written
  defensively to handle cases where a `LocalQueue` already exists. If the
  controller identifies a namespace that should receive a default `LocalQueue`
  named `<lq-default>`, its reconciliation process will be as follows:

  1. Verification: Before taking any action, the controller will first check if
     a `LocalQueue` named `<lq-default>` already exists in the target namespace.

  2. No-Op on Conflict: If the `LocalQueue` already exists, the controller will
     not attempt to create a new one or modify the existing one. This prevents
     the controller from overwriting a potentially customized, manually created
     `LocalQueue`.

  3. Emit Warning Event: To ensure administrators are aware of the situation,
     the controller will emit a `Warning` event on the parent `ClusterQueue`. The
     event message will clearly state that the creation of the default
     `LocalQueue` was skipped in a specific namespace because a `LocalQueue` with
     that name already exists.

  4. Continue Reconciliation: The controller will then proceed with its
     reconciliation loop without being blocked, handling other namespaces or
     tasks as needed. The `ClusterQueue` itself remains fully operational.

## Design Details

### API Proposal

This proposal adds a new `defaultLocalQueue` field to the `ClusterQueueSpec`.

```go
// ClusterQueueSpec defines the desired state of ClusterQueue
type ClusterQueueSpec struct {
    // ... existing fields

	// defaultLocalQueue specifies the configuration for automatically creating
	// LocalQueues in namespaces that match the ClusterQueue's namespaceSelector.
	// This feature is controlled by the `DefaultLocalQueue` feature gate.
	// If this field is set, a LocalQueue with the specified name will be created
	// in each matching namespace. The LocalQueue will reference this ClusterQueue.
	// +optional
	DefaultLocalQueue *DefaultLocalQueue `json:"defaultLocalQueue,omitempty"`
}

// DefaultLocalQueue defines the configuration for automatically created LocalQueues.
type DefaultLocalQueue struct {
	// name is the name of the LocalQueue to be created in matching namespaces.
	// This name must be a valid DNS subdomain name.
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern="^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$"
	Name string `json:"name"`
}
```

The auto-generated `LocalQueue` will also be given identifying labels and
annotations:

- Label: `kueue.x-k8s.io/auto-generated: "true"`

- Annotation: `kueue.x-k8s.io/created-by-clusterqueue: "<cluster-queue-name>"`

### Controller Logic

The existing `clusterqueue-controller` will be extended with the following
reconciliation logic:

1. The controller will watch for events on both `ClusterQueue` and `Namespace`
   resources.
2. On a change, it will iterate through `ClusterQueue`s that have
   `spec.defaultLocalQueue` enabled.
3. For each such `ClusterQueue`, it will list all `Namespace`s that match its
   `spec.namespaceSelector`.
4. For each matching `Namespace`, it will check if a `LocalQueue` with the name
   from `spec.defaultLocalQueue.name` already exists.
5. If the `LocalQueue` does not exist, the controller will create it. The new
   `LocalQueue` will reference the current `ClusterQueue` (via
   `spec.clusterQueue` field) and will include the identifying labels and
   annotations.
6. If a `LocalQueue` already exists, do nothing and emit a warning event.


### Test Plan

[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes
necessary to implement this enhancement.

##### Prerequisite testing updates

#### Unit Tests

#### Integration tests

### Graduation Criteria

Alpha:

- feature disabled by default
- creation of the `LocalQueue` which matches the `namespaceSelector`

Beta:

- feature enabled by default
- re-evaluate the strategies for conflict prevention

## Implementation History

## Drawbacks

The primary drawback is the added complexity to the existing
`clusterqueue-controller` and webhook logic. A misconfiguration, though
mitigated, could still have unintended consequences. It also introduces a
"magical" behavior where resources are created automatically, which might be
surprising to users not familiar with the feature. Clear documentation and
events will be crucial.

## Alternatives

### Improvements for future versions

Add new validation admission logic to the existing `ClusterQueue` webhook to
prevent selector overlap.

1. The webhook triggers on `CREATE` and `UPDATE` of `ClusterQueue` resources.
2. If `spec.defaultLocalQueue` is not set, the validation is skipped.
3. If set, the webhook lists all other `ClusterQueue`s in the cluster.
4. It compares the `namespaceSelector` of the incoming `ClusterQueue` with every
   other `ClusterQueue` that also has `defaultLocalQueue` enabled.
5. If a selector overlap is detected and the `defaultLocalQueue.name` is the same,
   the request is rejected with an error detailing the conflict. This prevents
   two `ClusterQueues` from attempting to manage the same `LocalQueue` resource.
