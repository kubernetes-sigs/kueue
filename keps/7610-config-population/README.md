# KEP-7610: Config Population

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [Implementation Overview](#implementation-overview)
  - [Risks and Mitigations](#risks-and-mitigations)
    - [Risk: Existing <code>LocalQueue</code>s](#risk-existing-localqueues)
- [Design Details](#design-details)
  - [Current Implementation (kueue-populator)](#current-implementation-kueue-populator)
    - [LocalQueue creation](#localqueue-creation)
    - [Topology-Aware scheduling configuration](#topology-aware-scheduling-configuration)
  - [Test Plan](#test-plan)
    - [Unit Tests](#unit-tests)
    - [Integration tests](#integration-tests)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
  - [Automatic Garbage Collection](#automatic-garbage-collection)
  - [Advanced Configuration (Templates)](#advanced-configuration-templates)
<!-- /toc -->

## Summary

This KEP proposes a mechanism to automatically create a default `LocalQueue` in
namespaces that match a `ClusterQueue`'s `namespaceSelector`. This avoids the
need for administrators to manually create a `LocalQueue` in each namespace.

> [!NOTE]
> This feature is implemented as a standalone experimental component named
> `kueue-populator`, rather than a core API change as originally proposed. See
> the [Design Pivot](#design-pivot) section for details. 

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
This is done for the ease of integrating and deploying Kueue and LWS - https://github.com/kubernetes-sigs/lws/issues/665

### Goals

- Automate the creation of a default `LocalQueue` within a namespace when its
  labels match a `ClusterQueue`'s `namespaceSelector`.
- Provide a standalone controller that can be deployed optionally to enable this behavior.
- Gracefully handle naming conflicts at runtime by ensuring the controller does
  not overwrite pre-existing `LocalQueues` and provides clear warning events when
  a conflict is detected.
- Ensure that automatically created `LocalQueue`s are clearly identifiable via
  the `kueue.x-k8s.io/auto-generated` label for easy discovery and management.
- Configure TAS with default `Topology`, `ResourceFlavor` and `ClusterQueue`.

### Non-Goals

- An automatic garbage collection mechanism to delete `LocalQueue`s from namespaces
  that no longer match the `namespaceSelector`. The initial implementation will
  leave these "orphaned" `LocalQueue`s in place to avoid disrupting active
  workloads. Cleanup will remain a manual administrative task.
- Supporting complex configurations for the auto-generated `LocalQueue` beyond
  its name.

## Proposal

### Implementation Overview

The `kueue-populator` is a standalone controller that:
1.  Watches `Namespace` and `ClusterQueue` resources.
2.  Is configured via CLI flags or a config file.
3.  Automatically creates a `LocalQueue` with a configured name (default:
    `default`) in namespaces that match a `ClusterQueue`'s `namespaceSelector`.

### Risks and Mitigations

#### Risk: Existing `LocalQueue`s

A `LocalQueue` with the configured name might already exist in a target
namespace, either created manually or by another process.

- **Mitigation:** The `kueue-populator`'s runtime logic is written
  defensively to handle cases where a `LocalQueue` already exists.

  1. **Verification:** Before taking any action, the controller checks if
     a `LocalQueue` with the configured name already exists in the target namespace.

  2. **No-Op on Conflict:** If the `LocalQueue` already exists, the controller
     does not attempt to create a new one or modify the existing one. This prevents
     overwriting a potentially customized, manually created `LocalQueue`.

  3. **Emit Warning Event:** To ensure administrators are aware of the situation,
     the controller emits a `Warning` event on the parent `ClusterQueue`. The
     event message clearly states that the creation of the default
     `LocalQueue` was skipped in a specific namespace because a `LocalQueue` with
     that name already exists.

## Design Details

### Current Implementation (kueue-populator)

The `kueue-populator` does not modify the `ClusterQueue` API. Instead, it is configured directly:

#### LocalQueue creation

-   `--local-queue-name`: The name of the LocalQueue to create (default: "default").
-   `--managed-jobs-namespace-selector`: A global selector to restrict which
    namespaces are considered (default: excludes `kube-system`).

**Reconciliation Logic:**

1.  **Trigger:** Watch events on `Namespace` and `ClusterQueue`.
2.  **Filter:**
    -   Ignore namespaces that don't match the global `managed-jobs-namespace-selector`.
    -   Identify `ClusterQueue`s whose `namespaceSelector` matches the namespace.
3.  **Action:**
    -   For each matching `ClusterQueue`, check if a `LocalQueue` with the configured name exists in the namespace.
    -   **If it exists:**
        -   Check if it points to the current `ClusterQueue`.
        -   If it points to a *different* `ClusterQueue`, emit a **Warning** event (conflict).
        -   If it points to the *same* `ClusterQueue`, do nothing (already correct).
    -   **If it does not exist:**
        -   Create the `LocalQueue` pointing to the `ClusterQueue`.
        -   Add label `kueue.x-k8s.io/auto-generated: "true"`.

**Conflict Resolution:**
If multiple `ClusterQueue`s match the same namespace, the first one reconciled
will successfully create the `LocalQueue`. Subsequent reconciliations for other
`ClusterQueue`s will fail to "claim" the `LocalQueue` and will emit a warning.

#### Topology-Aware scheduling configuration

Topology-Aware scheduling is configured by one default ClusterQueue, ResourceFlavor and Topology, which are configurable by Helm.
The `kueue-populator` Helm chart simplifies this setup by providing a `setup-hook` that automatically creates these resources.
The configuration for these resources can be customized via `values.yaml`.

### Test Plan

[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes
necessary to implement this enhancement.

#### Unit Tests

The `kueue-populator` controller logic is covered by unit tests, including:
- Creating LocalQueue when it doesn't exist.
- Skipping creation if it exists and matches.
- Emitting warning if it exists and conflicts.
- Respecting global namespace selector.

#### Integration tests

Integration tests cover the full controller flow using envtest, including:
- Verifying that watches on Namespace and ClusterQueue trigger reconciliation.
- End-to-end creation and updates of LocalQueues in a real API server environment.
- Handling of edge cases like deletions and updates.

### Graduation Criteria

Alpha:

- Initial release as an experimental component (`cmd/experimental/kueue-populator`).
- Basic conflict detection (existing LocalQueues).

Beta:

- Feedback gathered from experimental usage.
- Promotion from experimental to a standard supported component (for example
  `cmd/kueue-populator`), similar to `kueueviz`.

## Implementation History

- https://github.com/kubernetes-sigs/kueue/pull/7655

## Drawbacks

It introduces a "magical" behavior where resources are created automatically,
which might be surprising to users not familiar with the feature. Clear documentation
and events will be crucial.

## Alternatives

The original proposal suggested adding a `defaultLocalQueue` field to the
`ClusterQueue` API. However, during implementation, the decision was made to
move this functionality to a separate, standalone component named
`kueue-populator`.

**Reasoning:**
1.  **Separation of Concerns:** This avoids mixing cluster configuration
    management (creating resources) with the core Kueue responsibilities
    (scheduling and quota management).
2.  **API Bloat:** Keeping this logic external prevents adding "convenience"
    fields to the core API that might not be universally required.
3.  **Experimental Nature:** Implementing it as a separate tool allows for
    faster iteration and validation without affecting the stability of the core
    controller.

### Automatic Garbage Collection

Another considered approach was to automatically delete the auto-generated `LocalQueue`
if a namespace's labels change and it no longer matches the `namespaceSelector`.
This was ruled out for the initial version due to its complexity and potential for
destructive behavior. If there were active or pending workloads in the `LocalQueue`,
automatically deleting it could disrupt users. The current "do nothing" approach
is significantly safer, leaving the cleanup decision in the hands of an
administrator who can verify the queue is no longer in use.

### Advanced Configuration (Templates)

Currently, the `kueue-populator` only supports configuring the name of the `LocalQueue`.
In the future, if there is a need to support additional fields like `StopPolicy` or
`AdmissionFairSharing`, a `LocalQueueTemplate` could be introduced. This would function
similarly to a PodTemplate in a Deployment, allowing for a comprehensive blueprint
for auto-generated queues. This complexity was omitted from the initial experimental
version to prioritize simplicity.