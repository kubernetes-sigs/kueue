# KEP-1282: Requeue Strategy

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories (Optional)](#user-stories-optional)
    - [Story 1](#story-1)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [API Changes](#api-changes)
  - [Changes to Queue Sorting](#changes-to-queue-sorting)
    - [Existing Sorting](#existing-sorting)
    - [Proposed Sorting](#proposed-sorting)
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

Introduce new options that allow administrators to configure how Workloads are placed back in the queue after being after being evicted due to readiness checks.

## Motivation

### Goals

* Allowing administrators to configure requeuing behavior to ensure fair resource sharing after workloads fail to start running after they have been admitted.

### Non-Goals

* Providing options for how to sort requeued workloads after priority-based evictions (no user stories).

## Proposal

Make queue placement after pod-readiness eviction configurable at the level of Kueue configuration.

### User Stories (Optional)

#### Story 1

Consider the following scenario:

* A ClusterQueue has 2 ResourceFlavors.
* Kueue has admitted a workload on ResourceFlavor #2.
* There is a stock-out on the machine type needed to schedule this workload and the cluster autoscaler is unable to provision the necessary Nodes.
* The workload gets evicted by Kueue because an administrator has configured the `waitForPodsReady` setting.
* While the workload was pending capacity freed up on ResourceFlavor #1.

In this case, the administrator would like the evicted workload to be requeued as soon as possible on the newly available capacity.

### Risks and Mitigations

<!--
What are the risks of this proposal, and how do we mitigate? Think broadly.
For example, consider both security and how this will impact the larger
Kubernetes ecosystem.

How will security be reviewed, and by whom?

How will UX be reviewed, and by whom?

Consider including folks who also work outside the SIG or subproject.
-->


## Design Details

### API Changes

Add an additional field to the Kueue ConfigMap to allow administrators to specify what timestamp to consider during queue sorting (under the pre-existing waitForPodsReady block).

Possible settings:

* `Eviction` (Back of queue)
* `Creation` (Front of queue)

```yaml
kind: ConfigMap
metadata:
  name: kueue-manager-config
  namespace: kueue-system
data:
  controller_manager_config.yaml: |
    apiVersion: config.kueue.x-k8s.io/v1beta1
    kind: Configuration
    # ...
    waitForPodsReady:
      requeuingTimestamp: Creation | Eviction # <-- New field
```

### Changes to Queue Sorting

#### Existing Sorting

Currently, workloads within a ClusterQueue are sorted based on 1. Priority and 2. Timestamp of eviction - if evicted, otherwise time of creation.

#### Proposed Sorting

The `pkg/workload` package could be modified to include a conditional (`if evictionReason == kueue.WorkloadEvictedByPodsReadyTimeout`) that controls which timestamp to return based on the configured ordering strategy. The same sorting logic would also be used when sorting the heads of queues.

Update the `apis/config/<version>` package to include `Creation` and `Eviction` constants.

### Test Plan

[X] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.

##### Prerequisite testing updates

<!--
Based on reviewers feedback describe what additional tests need to be added prior
implementing this enhancement to ensure the enhancements have also solid foundations.
-->

#### Unit Tests

Most of the test coverage should probably live inside of `pkd/queue`. Additional test cases should be added that test different requeuing configurations.

- `pkg/queue`: `Nov 2 2023` - `33.9%`

#### Integration tests

- Add integration test that matches user story 1.
- Add an integration test to detect if flapping associated with preempted workloads being readmitted before the preemptor workload when `priorityPreemption: UseEvictionTimestamp` is set.

### Graduation Criteria

<!--

Clearly define what it means for the feature to be implemented and
considered stable.

If the feature you are introducing has high complexity, consider adding graduation
milestones with these graduation criteria:
- [Maturity levels (`alpha`, `beta`, `stable`)][maturity-levels]
- [Feature gate][feature gate] lifecycle
- [Deprecation policy][deprecation-policy]

[feature gate]: https://git.k8s.io/community/contributors/devel/sig-architecture/feature-gates.md
[maturity-levels]: https://git.k8s.io/community/contributors/devel/sig-architecture/api_changes.md#alpha-beta-and-stable-versions
[deprecation-policy]: https://kubernetes.io/docs/reference/using-api/deprecation-policy/
-->

## Implementation History

<!--
Major milestones in the lifecycle of a KEP should be tracked in this section.
Major milestones might include:
- the `Summary` and `Motivation` sections being merged, signaling SIG acceptance
- the `Proposal` section being merged, signaling agreement on a proposed design
- the date implementation started
- the first Kubernetes release where an initial version of the KEP was available
- the version of Kubernetes where the KEP graduated to general availability
- when the KEP was retired or superseded
-->

## Drawbacks

When used with `StrictFIFO`, the `requeuingTimestamp: Creation` (front of queue) policy could lead to a blocked queue. This was called out in the issue that set the hardcoded [back-of-queue behavior](https://github.com/kubernetes-sigs/kueue/issues/599). This could be mitigated by recommending administrators select `BestEffortFIFO` when using this setting.

## Alternatives

The same concepts could be exposed to users based on `FrontOfQueue` or `BackOfQueue` settings instead of `Creation` and `Eviction` timestamps. These terms would imply that the workload would be prioritized over higher priority workloads in the queue. This is probably not desired (would likely lead to rapid preemption upon admission when preemption based on priority is enabled).

These concepts could be configured in the ClusterQueue resource. This alternative would increase flexibility. Without a clear need for this level of granularity, it might be better to set these options at the controller level where `waitForPodsReady` settings already exist. Furthermore, configuring these settings at the ClusterQueue level introduces the question of what timestamp to use when sorting the heads of all ClusterQueues.
