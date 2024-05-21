# KEP-1282: Pods Ready Requeue Strategy

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories (Optional)](#user-stories-optional)
    - [Story 1](#story-1)
    - [Story 2](#story-2)
    - [Story 3](#story-3)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [API Changes](#api-changes)
    - [KueueConfig](#kueueconfig)
    - [Workload](#workload)
  - [Changes to Queue Sorting](#changes-to-queue-sorting)
    - [Existing Sorting](#existing-sorting)
    - [Proposed Sorting](#proposed-sorting)
  - [Exponential Backoff Mechanism](#exponential-backoff-mechanism)
    - [Evaluation](#evaluation)
  - [Test Plan](#test-plan)
      - [Prerequisite testing updates](#prerequisite-testing-updates)
    - [Unit Tests](#unit-tests)
    - [Integration tests](#integration-tests)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
  - [Create &quot;FrontOfQueue&quot; and &quot;BackOfQueue&quot;](#create-frontofqueue-and-backofqueue)
  - [Configure at the ClusterQueue level](#configure-at-the-clusterqueue-level)
  - [Make knob to be possible to set timeout until the workload is deactivated](#make-knob-to-be-possible-to-set-timeout-until-the-workload-is-deactivated)
    - [Evaluation](#evaluation-1)
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

#### Story 2

In the story 1 scenario, when we set `waitForPodsReady.requeuingStrategy.timestamp=Creation`, 
the workload endlessly or repeatedly can be put in front of the queue after eviction in the following eviction reasons:

1. The workload don't have the proper configurations like image pull credential and pvc name, etc.
2. The cluster can meet flavorQuotas, but each node doesn't have the resources that each podSet requests.  
3. If there are multiple resource flavors that match the workload (for example, flavors 1 & 2)
   and the workload was running on flavor 2, it's likely that the workload will be readmitted
   on the same flavor indefinitely.

Specifically, the second reason will often occur if the available quota is fragmented across multiple nodes,
such that the workload can't be scheduled in a node even though there is enough quota in the cluster.

For example, Given that the workload with a request of 2 gpus is submitted to the cluster that
has 2 worker nodes with 4 gpus, and 3 gpus are used (which means 1 gpu is free in each node),
the workload will be repeatedly evicted because of the lack of resources in each node even though the cluster has enough capacities.

In this case, to avoid rapid repetition of the admission and eviction cycle,
the administrator would like to use an exponential backoff mechanism and add a maximum number of retries.

#### Story 3

In the story 2 scenario, after the evicted workload reaches the maximum retry criterion
and the workload is never backoff, we want to easily requeue the workload to the queue without recreating the job.
This is possible if the Workload is deactivated (`.spec.active`=`false`) as opposed to deleting it.

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

#### KueueConfig

Add fields to the KueueConfig to allow administrators to specify what timestamp to consider during queue sorting (under the pre-existing waitForPodsReady block).

Possible settings:

* `Eviction` (Back of queue)
* `Creation` (Front of queue)

```go
type WaitForPodsReady struct {
	...
	// RequeuingStrategy defines the strategy for requeuing a Workload.
	// +optional
	RequeuingStrategy *RequeuingStrategy `json:"requeuingStrategy,omitempty"`
}

type RequeuingStrategy struct {
	// Timestamp defines the timestamp used for requeuing a Workload
	// that was evicted due to Pod readiness. The possible values are:
	//
	// - `Eviction` (default) indicates from Workload `Evicted` condition with `PodsReadyTimeout` reason.
	// - `Creation` indicates from Workload .metadata.creationTimestamp.
	//
	// +optional
	Timestamp *RequeuingTimestamp `json:"timestamp,omitempty"`

	// BackoffLimitCount defines the maximum number of re-queuing retries.
	// Once the number is reached, the workload is deactivated (`.spec.activate`=`false`).
	// When it is null, the workloads will repeatedly and endless re-queueing.
	//
	// Every backoff duration is about "60s*2^(n-1)+Rand" where:
	// - "n" represents the "workloadStatus.requeueState.count",
	// - "Rand" represents the random jitter.
	// During this time, the workload is taken as an inadmissible and
	// other workloads will have a chance to be admitted.
	// By default, the consecutive requeue delays are around: (60s, 120s, 240s, ...).
	//
	// Defaults to null.
	// +optional
	BackoffLimitCount *int32 `json:"backoffLimitCount,omitempty"`

	// BackoffBaseSeconds defines the base for the exponential backoff for
	// re-queuing an evicted workload.
	//
	// Defaults to 60.
	// +optional
	BackoffBaseSeconds *int32 `json:"backoffBaseSeconds,omitempty"`
}

type RequeuingTimestamp string

const (
	// CreationTimestamp timestamp (from Workload .metadata.creationTimestamp).
	CreationTimestamp RequeuingTimestamp = "Creation"
    
	// EvictionTimestamp timestamp (from Workload .status.conditions).
	EvictionTimestamp RequeuingTimestamp = "Eviction"
)
```

#### Workload

Add a new field, "requeueState", to the Workload to allow recording the following items: 

1. the number of times a workload is re-queued
2. when the workload was re-queued or will be re-queued

```go
type WorkloadStatus struct {
	...
	// requeueState holds the re-queue state
	// when a workload meets Eviction with PodsReadyTimeout reason.
	//
	// +optional	
	RequeueState *RequeueState `json:"requeueState,omitempty"`
}

type RequeueState struct {
	// count records the number of times a workload has been re-queued
	// When a deactivated (`.spec.activate`=`false`) workload is reactivated (`.spec.activate`=`true`),
	// this count would be reset to null.
	//
	// +optional
	// +kubebuilder:validation:Minimum=0
	Count *int32 `json:"count,omitempty"`
    
	// requeueAt records the time when a workload will be re-queued.
	// When a deactivated (`.spec.activate`=`false`) workload is reactivated (`.spec.activate`=`true`),
	// this time would be reset to null.
	//
	// +optional
	RequeueAt *metav1.Time `json:"requeueAt,omitempty"`
}
```

### Changes to Queue Sorting

#### Existing Sorting

Currently, workloads within a ClusterQueue are sorted based on 1. Priority and 2. Timestamp of eviction - if evicted, otherwise time of creation.

#### Proposed Sorting

The `pkg/workload` package could be modified to include a conditional (`if evictionReason == kueue.WorkloadEvictedByPodsReadyTimeout`) 
that controls which timestamp to return based on the configured ordering strategy.
The same sorting logic would also be used when sorting the heads of queues.

Update the `apis/config/<version>` package to include `Creation` and `Eviction` constants.

### Exponential Backoff Mechanism

When the kueueConfig `backoffLimitCount` is set and there are evicted workloads by waitForPodsReady,
the queueManager holds the evicted workloads as inadmissible workloads while exponential backoff duration.
Duration this time, other workloads will have a chance to be admitted.

The queueManager calculates an exponential backoff duration by [the Step function](https://pkg.go.dev/k8s.io/apimachinery/pkg/util/wait@v0.29.1#Backoff.Step)
according to the $b*2^{(n-1)}+Rand$ where:
- $b$ represents the base delay, configured by `backoffBaseSeconds`
- $n$ represents the `workloadStatus.requeueState.count`,
- $Rand$ represents the random jitter.

It will spend awaiting to be requeued after eviction:
$$\sum_{k=1}^{n}(b*2^{(k-1)} + Rand)$$

Assuming `backoffLimitCount` equals 10, and `backoffBaseSeconds` equals 60 (default) the workload is requeued 10 times
after failing to have all pods ready, then the total time awaiting for requeue
will take (neglecting the jitter): `60s+120s+240s +...+30720s=8h 32min`.
Also, considering `.waitForPodsReady.timeout=300s` (default),
the workload will spend `50min` total waiting for pods ready.

#### Evaluation

When a workload eviction is issued with the `PodsReadyTimeout` condition,
the workload controller increments the `.status.requeueState.count` by 1 each time and
sets the time to the `.status.requeueState.requeueAt` when a workload will be re-queued.

If a workload `.status.requeueState.count` reaches the kueueConfig `.waitForPodsReady.requeueingStrategy.backoffLimitCount`,
the workload controller doesn't modify `.status.requeueState` and deactivates a workload by setting false to `.spec.active`.

After that, the jobframework reconciler adds `Evicted` condition with `WorkloadInactive` reason to a workload.
Finally, the jobframework reconciler stops a job based in the next reconcile.

Additionally, when a deactivated workload by eviction is re-activated, the `requeueState` is reset to null. 
If a workload is deactivated by other ways such as user operations, the `requeueState` is not reset.

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
- Add an integration test to detect if flapping associated with preempted workloads being readmitted before the preemptor workload when `requeuingTimestamp: Creation` is set.

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

- Jan 18th: Implemented the re-queue strategy that workloads evicted due to pods-ready (story 1) [#1311](https://github.com/kubernetes-sigs/kueue/pulls/1311)
- Feb 12th: Implemented the re-queueing backoff mechanism triggered by eviction with PodsReadyTimeout reason (story 2 and 3) [#1709](https://github.com/kubernetes-sigs/kueue/pulls/1709)

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

* When used with `StrictFIFO`, the `requeuingStrategy.timestamp: Creation` (front of queue) policy could lead to a blocked queue. This was called out in the issue that set the hardcoded [back-of-queue behavior](https://github.com/kubernetes-sigs/kueue/issues/599). 
This could be mitigated by recommending administrators select `BestEffortFIFO` when using this setting.
* Pods that never become ready due to invalid images will constantly be requeued to the front of the queue when the creation timestamp is used. [See Kubernetes issue](https://github.com/kubernetes/kubernetes/issues/122300).

## Alternatives

### Create "FrontOfQueue" and "BackOfQueue"

The same concepts could be exposed to users based on `FrontOfQueue` or `BackOfQueue` settings instead of `Creation` and `Eviction` timestamps. 
These terms would imply that the workload would be prioritized over higher priority workloads in the queue.
This is probably not desired (would likely lead to rapid preemption upon admission when preemption based on priority is enabled). 

### Configure at the ClusterQueue level

These concepts could be configured in the ClusterQueue resource. This alternative would increase flexibility.
Without a clear need for this level of granularity, it might be better to set these options at the controller level where `waitForPodsReady` settings already exist.
Furthermore, configuring these settings at the ClusterQueue level introduces the question of what timestamp to use when sorting the heads of all ClusterQueues.

### Make knob to be possible to set timeout until the workload is deactivated

Another knob, `backoffCount` is difficult to estimate how many hours jobs will actually be retried (requeued).
So, it might be useful to make a knob to possible to set timeout until the workload is deactivated.
For the first iteration, we don't make this knob since only `backoffLimitCount` would be enough to current stories.  

```go
type RequeuingStrategy struct {
	...
	// backoffLimitTimeout defines the time for a workload that 
	// has once been admitted to reach the PodsReady=true condition.
	// When the time is reached, the workload is deactivated.
	// 	
	// Defaults to null.
	// +optional
	BackOffLimitTimeout *int32 `json:"backoffLimitTimeout,omitempty"`
}
```

#### Evaluation

When a workload's duration $currentTime - queueOrderingTimestamp$ reaches the kueueConfig `waitForPodsReady.requeueingStrategy.backoffLimitTimeout`,
the workload controller and the queueManager sets false to `.spec.active`.
After that, the jobframework reconciler deactivates a workload.

Before the jobframework reconciler deactivates a workload,
the workload controller sets false to `.spec.active` after the workload reconciler checks if a workload is finished.
In addition, when the kueue scheduler gets headWorkloads from clusterQueues,
if the queueManager finds the workloads exceeding `backoffLimitTimeout` and sets false to workload `.spec.active`.
