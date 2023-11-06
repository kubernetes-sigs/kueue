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
    - [Preemption/Preemptor Flapping](#preemptionpreemptor-flapping)
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

Introduce new options that allow administrators to configure how Workloads are placed back in the queue after being after being evicted.

## Motivation

### Goals

Allow administrators to configure requeuing behavior based on the reason the Workload is being requeued.

### Non-Goals

## Proposal

Make queue placement after preemption configurable at the level of Kueue configuration.

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

Add additional fields to the Kueue ConfigMap to allow administrators to specify what timestamp to consider during queue sorting based on the reason for eviction.

Possible settings:

* `UseEvictionTimestamp` (Back of queue)
* `UseCreationTimestamp` (Front of queue)

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
    requeueStrategy:
      priorityPreemption: UseEvictionTimestamp
      podsReadyTimeout: UseCreationTimestamp
```

### Changes to Queue Sorting

#### Existing Sorting

Currently, workloads within a ClusterQueue are sorted based on 1. Priority and 2. Timestamp of Creation/Eviction.

```go
package queue

// ...

func queueOrdering(a, b interface{}) bool {
	objA := a.(*workload.Info)
	objB := b.(*workload.Info)
	p1 := utilpriority.Priority(objA.Obj)
	p2 := utilpriority.Priority(objB.Obj)

	if p1 != p2 {
		return p1 > p2
	}

	tA := workload.GetQueueOrderTimestamp(objA.Obj)
	tB := workload.GetQueueOrderTimestamp(objB.Obj)
	return !tB.Before(tA)
}
```

```go
package workload

// ...

func GetQueueOrderTimestamp(w *kueue.Workload) *metav1.Time {
	if c := apimeta.FindStatusCondition(w.Status.Conditions, kueue.WorkloadEvicted); c != nil && c.Status == metav1.ConditionTrue && c.Reason == kueue.WorkloadEvictedByPodsReadyTimeout {
		return &c.LastTransitionTime
	}
	return &w.CreationTimestamp
}
```

#### Proposed Sorting

Add types that contain information about queue ordering strategy to the `apis/config/<version>` package. For example:

```go
package v1beta1

// ...

type QueueOrderingCriteria string

const (
  OrderOnEvictionTimestamp = QueueOrderingCriteria("UseEvictionTimestamp")
  OrderOnCreationTimestamp = QueueOrderingCriteria("UseCreationTimestamp")
)

type QueueOrderingStrategy struct{
  RequeueOnPriorityPreemption QueueOrderingCriteria
  RequeueOnPodsReadyTimeout   QueueOrderingCriteria
}
```

The `pkg/workload` package could have the `GetQueueOrderTimestamp` function modified to include conditionals that control which timestamp to return based on the configured ordering strategy.

```go
func GetQueueOrderTimestamp(w *kueue.Workload, s QueueOrderingStrategy) *metav1.Time {
  switch getEvictionReason(w) {
    case kueue.WorkloadEvictedByPodsReadyTimeout:
      if s.RequeueOnPodsReadyTimeout == OrderOnEvictionTimestamp {
        return &c.LastTransitionTime
      } else {
        return &c.CreationTimestamp
      }
    case kueue.WorkloadEvictedByPreemption:
      if s.RequeueOnPriorityPreemption == OrderOnEvictionTimestamp {
        return &c.LastTransitionTime
      } else {
        return &c.CreationTimestamp
      }
    default:
      return &w.CreationTimestamp
  }
}

// ...

func getEvictionReason(w *kueue.Workload) string {
	if c := apimeta.FindStatusCondition(w.Status.Conditions, kueue.WorkloadEvicted); c != nil && c.Status == metav1.ConditionTrue {
    return c.Reason
  }
  return ""
}
```

#### Preemption/Preemptor Flapping

To address the [following concern](https://github.com/kubernetes-sigs/kueue/issues/1282#issue-1966106166):

> The case of preemption might be more tricky: we want the preempted jobs to be readmitted as soon as possible. But if a job is waiting for more than one job to be preempted and the queueing strategy is BestEffortFIFO, we don't want the preempted pods to take the head of the queue.
> Maybe we need to hold them until the preemptor job is admitted, and then they should use the regular priority.

The `pkg/queue` package could have the existing `queueOrderingFunc()` modified to add sorting based on who is the preemptor (might need to add a condition to the Workload to track this).


```go
package queue 

// ...

func queueOrderingFunc(s kueue.QueueOrderingStrategy) func(interface{}, interface{}) bool {
  return func(a, b interface{}) bool {
    objA := a.(*workload.Info)
    objB := b.(*workload.Info)
    p1 := utilpriority.Priority(objA.Obj)
    p2 := utilpriority.Priority(objB.Obj)
    
    if p1 != p2 {
      return p1 > p2
    }
    
    // Make sure a workload does not jump ahead of a preempting workload.
    pre1 := workload.IsPreemptor(objA.Obj)
    pre2 := workload.IsPreemptor(objB.Obj)
    if pre1 != pre2 {
      return pre1
    }
    
    tA := workload.GetQueueOrderTimestamp(objA.Obj, s)
    tB := workload.GetQueueOrderTimestamp(objB.Obj, s)
    return !tB.Before(tA)
  }
}
```

```go
package workload

// ...

func IsPreemptor(w *kueue.Workload, s QueueOrderingStrategy) *metav1.Time {
  c := apimeta.FindStatusCondition(w.Status.Conditions, /* TODO: Condition */)
  return c != nil && c.Status == metav1.ConditionTrue && c.Reason == /* TODO: Reason */
}
```

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

Most of the test covarage should probably live inside of `pkd/queue`. Additional test cases should be added that test different requeuing configurations.

- `pkg/queue`: `Nov 2 2023` - `33.9%`

#### Integration tests

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

When used with `StrictFIFO`, the `podsReadyTimeout: UseCreationTimestamp` (front of queue) policy could lead to a blocked queue. This was called out in the issue that set the hardcoded [back-of-queue behavior](https://github.com/kubernetes-sigs/kueue/issues/599). This could be mitigated by recommending administrators select `BestEffortFIFO` when using this setting.

## Alternatives

The same concepts could be exposed to users based on `FrontOfQueue` or `BackOfQueue` settings instead of `UseCreationTimestamp` and `UseEvictionTimestamp`. These terms would imply that the workload would be prioritized over higher priority workloads in the queue. This is probably not desired (would likely lead to rapid preemption upon admission when preemption based on priority is enabled).

These concepts could be configured in the ClusterQueue resource. This alternative would increase flexibility. Without a clear need for this level of granularity, it might be better to set these options at the controller level as proposed here to increase the grokability of the overall system.
