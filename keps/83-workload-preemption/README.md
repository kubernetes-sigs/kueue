# KEP-83: Workload preemption

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories (Optional)](#user-stories-optional)
    - [Story 1](#story-1)
  - [Notes/Constraints/Caveats (Optional)](#notesconstraintscaveats-optional)
    - [Why no control to opt-out a ClusterQueue from preemption](#why-no-control-to-opt-out-a-clusterqueue-from-preemption)
  - [Reassigning flavors after preemption](#reassigning-flavors-after-preemption)
  - [Risks and Mitigations](#risks-and-mitigations)
    - [Workload preemption doesn't imply immediate Pod termination](#workload-preemption-doesnt-imply-immediate-pod-termination)
  - [Increased admission latency](#increased-admission-latency)
- [Design Details](#design-details)
  - [ClusterQueue API changes](#clusterqueue-api-changes)
  - [Changes in scheduling algorithm](#changes-in-scheduling-algorithm)
    - [Detecting Workloads that might benefit from preemption](#detecting-workloads-that-might-benefit-from-preemption)
    - [Sorting Workloads that are heads of ClusterQueues](#sorting-workloads-that-are-heads-of-clusterqueues)
    - [Admission](#admission)
    - [Preemption](#preemption)
  - [Test Plan](#test-plan)
      - [Prerequisite testing updates](#prerequisite-testing-updates)
    - [Unit Tests](#unit-tests)
    - [Integration tests](#integration-tests)
    - [E2E tests](#e2e-tests)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
  - [Allow high priority jobs to borrow quota while preempting](#allow-high-priority-jobs-to-borrow-quota-while-preempting)
  - [Inform how costly is to interrupt a Workload](#inform-how-costly-is-to-interrupt-a-workload)
  - [Penalizing long running workloads](#penalizing-long-running-workloads)
  - [Terminating Workloads on preemption](#terminating-workloads-on-preemption)
  - [Extra knobs in ClusterQueue preemption policy](#extra-knobs-in-clusterqueue-preemption-policy)
<!-- /toc -->

## Summary

This enhancement introduces workload preemption, a mechanism to suspend
workloads when:
- ClusterQueues under their minimum quota need the resources that are currently
  borrowed by other ClusterQueues in the cohort. Alternatively, we say that the
  ClusterQueue needs to _reclaim_ its quota.
- Within a ClusterQueue, there are running Workloads with lower priority than
  a pending Workload.

API fields in the ClusterQueue spec determine preemption policies.

## Motivation

When ClusterQueues under their minimum quota lend resources, they should
be able to recover those resources fast, to be able to admit Workloads
when there are sudden spikes. Similarly, the ClusterQueue should be able to
recover quota from low priority workloads that are currently running.

Currently, the only mechanism to recover those resources is to wait for
Workloads to finish, which is generally unbounded.

### Goals

- Preempt Workloads from ClusterQueues borrowing resources when other
  ClusterQueues in the cohort, under their minimum quota, need the resources.
- Preempt Workloads within a ClusterQueue when a high priority Workload doesn't
  fit in the available quota, independently of borrowed quota.
- Introduce API fields in ClusterQueue to control when preemption occurs.

### Non-Goals

- Graceful termination of Workloads is left to the workload pods to implement.
- Tracking usage by workloads that take arbitrary time to be suspended. See
  [Workload preemption doesn't imply immediate Pod termination](#workload-preemption-doesnt-imply-immediate-pod-termination) to learn more.
  For example, the integration with Job uses the [suspend field](https://kubernetes.io/docs/concepts/workloads/controllers/job/#suspending-a-job).
- Partial workload preemption is not supported.
- Terminate workloads on preemption.
- Penalize workloads with the same priority that have been running for a long
  time.

## Proposal

This enhancement proposes the introduction of a field in the ClusterQueue to
determine the preemption policy for two scenarios:
- Reclaiming quota: a pending workload fits in the quota that is currently
  borrowed by other ClusterQueues in the cohort.
- Pending high priority pod: the ClusterQueue is out of quota, but there are
  low priority active Workloads.

The enhancement also includes an algorithm for selecting a set of Workloads to be
preempted from the ClusterQueue or the cohort (to reclaim borrowed Quota).

### User Stories (Optional)

#### Story 1

As a cluster administrator, I want to control preemption of active Workloads
within the ClusterQueue and/or Cohort to accommodate for a pending workload.

A possible configuration looks like the following:

```yaml
apiVersion: kueue.x-k8s.io/v1alpha2
kind: ClusterQueue
metadata:
  name: cluster-total
spec:
  preemption:
    withinCohort: Always
    withinClusterQueue: LowerPriorityOnly
```

### Notes/Constraints/Caveats (Optional)

#### Why no control to opt-out a ClusterQueue from preemption

In a cohort, some ClusterQueue could have high priority Workloads running, so
it might be desired not to disturb them.

However, this could be achieved by two means:
- Configuring the ClusterQueue with high priority Workloads to never borrow
  (through `.quota.max`), while owning a big part or all of the quota for the
  cohort.
- Configuring other ClusterQueues to not preempt workloads in the cohort when
  reclaiming or only do so for incoming workload that have higher priority than
  the running workloads. In other words, the control is on the ClusterQueue that
  is lending the resources, rather than the borrower.

### Reassigning flavors after preemption

When a Job is first admitted, kueue's job controller modifies it's pod template
to inject a node selector coming from the ResourceFlavor.

On preemption, the job controller resets the template back to the original
nodeSelector, stored in the Workload spec (implementation)[https://github.com/kubernetes-sigs/kueue/blob/f24c63accaad461dfe582b21819dbf3a5d75dd60/pkg/controller/workload/job/job_controller.go#L246-251].

### Risks and Mitigations

#### Workload preemption doesn't imply immediate Pod termination

When Kueue issues a Workload preemption, the workload API integration controller
is expected to start removing Pods.
In the case of Kubernetes' batch.v1/Job, the following steps happen:
1. Kueue's job controller sets the
[`.spec.suspend`](https://kubernetes.io/docs/concepts/workloads/controllers/job/#suspending-a-job)
field to true.
2. The Kubernetes' job controller deletes the Job's Pods.
3. The Kubelets send SIGTERM signals to the Pod's containers, which can have a
   graceful termination logic.

This implies the following:
- Pods of a workload could implement checkpointing as part of their graceful
  termination.
- The resources from these Pods are not immediately available and they could
  be arbitrarily delayed.
- While Pods are terminating, a ClusterQueue's quota could be oversubscribed.

The kubernetes Job status includes the number of Pending/Running Pods that are
not terminating (don't have a `.metadata.deletionTimestamp`). We could use
this information and write the old admission spec into an annotation to keep
track of usage from non-terminating Pods. But this will be left for future work.

### Increased admission latency

Calculating and executing preemption is expensive. Potentially, every
workload might benefit from preemption of running Workloads.

To mitigate this, we will keep track of the minimum priority among the running
Workloads in a ClusterQueue. If the minimum priorities are higher than or equal
to the incoming Workload, we will skip preemption for it altogether.

The assumption is that workloads with low priority are more common than
workloads with higher priority and that Workloads are sent to ClusterQueues
where most Workloads have the same priority.

Additionally, the preemption algorithm is mostly a linear pass over the running
workloads (plus sorting), so it doesn't add a significant complexity overhead
over building the scheduling snapshot every cycle.

The API updates from preemption will be executed in parallel.

## Design Details

The proposal consists of new API fields and a preemption algorithm.

### ClusterQueue API changes

The new API fields in ClusterQueue describe how to influence the selection
of Workloads to preempt.

```golang
type ClusterQueueSpec struct {
  ...
  // preemption describes policies to preempt Workloads from this ClusterQueue
  // or the ClusterQueue's cohort.
  //
  // Preemption can happen in two scenarios:
  //
  // - When a Workload fits within the min quota of the ClusterQueue, but the
  //   quota is currently borrowed by other ClusterQueues in the cohort.
  //   Preempting Workloads in other ClusterQueues allows this ClusterQueue to
  //   reclaim its min quota.
  // - When a Workload doesn't fit within the min quota of the ClusterQueue
  //   and there are active Workloads with lower priority.
  //
  // The preemption algorithm tries to find a minimal set of Workloads to
  // preempt to accommodate the pending Workload, preempting Workloads with
  // lower priority first.
  preemption ClusterQueuePreemption
}

type PreemptionPolicy string

const (
  PreemptionPolicyNever                    = "Never"
  PreemptionPolicyReclaimFromLowerPriority = "ReclaimFromLowerPriority"
  PreemptionPolicyReclaimFromAny           = "ReclaimFromAny"
  PreemptionPolicyLowerPriority            = "LowerPriority"
)

type ClusterQueuePreemption struct {
  // withinCohort determines whether a pending Workload can preempt Workloads
  // from other ClusterQueues in the cohort that are using more than their min
  // quota.
  // Possible values are:
  // - `Never` (default): do not preempt workloads in the cohort.
  // - `ReclaimFromLowerPriority`: if the pending workload fits within the min
  //   quota of its ClusterQueue, only preempt workloads in the cohort that have
  //   lower priority than the pending Workload.
  // - `ReclaimAny`: if the pending workload fits within the min quota of its
  //   ClusterQueue, preempt any workload in the cohort.
  WithinCohort PreemptionPolicy

  // withinClusterQueue determines whether a pending workload that doesn't fit
  // within the min quota for its ClusterQueue, can preempt active Workloads in
  // the ClusterQueue.
  // Possible values are:
  // - `Never` (default): do not preempt workloads in the ClusterQueue.
  // - `LowerPriority`: only preempt workloads in the ClusterQueue that have
  //   lower priority than the pending Workload.
  WithinClusterQueue PreemptionPolicy
}
```

### Changes in scheduling algorithm

The following changes in the scheduling algorithm are required to implement
preemption.

#### Detecting Workloads that might benefit from preemption

The first stage during scheduling is to assign flavors to each resource of
a workload.

The algorithm is like follows:

    For each resource (or set of resources with the same flavors), evaluate
    flavors in the order established in the ClusterQueue*:

    0. Find a flavor that still has quota in the cohort (borrowing allowed),
       but doesn't surpass the max quota for the CQ. Keep track of whether
       borrowing was needed.
    1. [New step] if no flavor was found, find a flavor that is able to contain
       the request within the min quota of the ClusterQueue. This flavor
       assignment could be satisfied with preemption.

Some highlights:
- A Workload could get flavor assignments at different steps for different
  resources.
- Assignments that require preemption implicitly do not borrow quota.

A flavor assignment from step 1 means that we need to preempt or wait for other
workloads in the cohort and/or ClusterQueue to finish to accommodate this
workload.

[#312](https://github.com/kubernetes-sigs/kueue/issues/312) discusses different
strategies to select a flavor.

#### Sorting Workloads that are heads of ClusterQueues

Sorting uses the following criteria:

1. Flavors that don't borrow first.
2. [New criteria] Highest priority first.
3. Older creation timestamp first.

Note that these criteria might put Workloads that require preemption ahead,
because preemption doesn't require borrowing more resources. This is desired,
because preemption to recover quota or admit high priority Workloads takes
preference over borrowing.

#### Admission

When iterating over workloads to be admitted, in the order given by the previous
section, we disallow borrowing in the cohort in the current cycle after
evaluating a Workload that doesn't require borrowing. This is the same behavior
that we have today, but note that this criteria now includes Workloads that need
preemption, because there is no preemption with borrowing quota.

This guarantees that, in future cycles, we can admit Workloads that were not
heads in their ClusterQueues in this cycle, but could fit without borrowing in
the next cycle, before lending quota to other ClusterQueues.

In the past, we only disallowed borrowing in the cohort if we were able to
admit the Workload, because we only kept track of flavor assignments of type 0.
This caused ClusterQueues in the cohort to continue
borrowing quota, even if there were pending Workloads that would fit under the
min Quota for their ClusterQueues.

It is actually possible to limit borrowing within the cohort only for the
flavors used by the evaluated Workloads, instead of restricting borrowing for
all the flavors in the cohort. But we will leave this as a future possible
optimization to improve throughput.

#### Preemption

For each Workload that got flavor assignments where preemption could help,
we run the following algorithm:

1. Check whether preemption is allowed and could help.

  We skip preemption if `.preemption.withinCohort=Never` and
  `.preemption.withinClusterQueue=Never`.

2. Obtain a list of candidate Workloads to be preempted.

  1. In the cohort, we only consider ClusterQueues that are currently borrowing
     quota. We restrict the list to Workloads with lower priority than the
     pending Workload if `.preemption.withinCohort=ReclaimFromLowerPriority`
  2. In the ClusterQueue, we only select Workloads with lower
     priority than the pending Workload.
     
  To quickly list workloads with priority lower than the incoming workload,
  we can keep a priority queue with the priorities of active
  Workloads in the ClusterQueue.

  When going over these sets, we filter out the Workloads that are not using the
  flavors that were selected for the incoming Workload.
  
  If the list of candidates is empty, skip the rest of the preemption algorithm.

3. Sort the Workloads using the following criteria:
   1. Workloads from other ClusterQueues in the cohort first.
   2. Lower priority first.
   3. Shortest running time first.

4. Remove Workloads from the snapshot in the order of the list. Stop removing
   Workloads if the incoming Workload fits within the quota. Skip removing more
   Workloads from a ClusterQueue if its usage is already below its `min` quota
   for all the involved flavors.

   The set of removed Workloads is a maximal set of Workloads that need to be
   preempted.

5. In the reverse order of the Workloads that were removed, add Workloads back
   as long as the incoming Workload still fits. This gives us a minimal set
   of Workloads to preempt.

6. Preempt the Workloads by clearing `.spec.admission`.
   The Workload will be requeued by the Workload event handler.

The incoming Workload wouldn't be admitted in this cycle. It is requeued and
it will be admitted once the changes in the victim Workloads are observed and
updated in the cache.

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

- Need to improve coverage of `pkg/queue` up to at least 80%.

#### Unit Tests

<!--
In principle every added code should have complete unit test coverage, so providing
the exact set of tests will not bring additional value.
However, if complete unit test coverage is not possible, explain the reason of it
together with explanation why this is acceptable.
-->

<!--
Additionally, try to enumerate the core package you will be touching
to implement this enhancement and provide the current unit coverage for those
in the form of:
- <package>: <date> - <current test coverage>

This can inform certain test coverage improvements that we want to do before
extending the production code to implement this enhancement.
-->

- `apis/kueue/webhooks`: `2022-11-17` - `72%`
- `pkg/cache`: `2022-11-17` - `83%`
- `pkg/scheduler`: `2022-11-17` - `91%`
- `pkg/queue`: `2022-11-17` - `62%`

#### Integration tests

- No new workloads in the cohort can borrow when workloads in a ClusterQueue
  fit within their min quota (StrictFIFO and BestEffortFIFO), but there are
  running workloads.
- Preemption within a ClusterQueue based on priority.
- Preemption within a Cohort to reclaim min quota.

#### E2E tests

- Preemption within a ClusterQueue based on priority.

<!--
Describe what tests will be added to ensure proper quality of the enhancement.

After the implementation PR is merged, add the names of the tests here.
-->

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

N/A

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

1. 2022-09-19: First draft, included multiple knobs.
2. 2022-11-17: Complete proposal with minimal API.

## Drawbacks

<!--
Why should this KEP _not_ be implemented?
-->

Preemption is costly to calculate. However, it's a highly demanded feature.
The API allows preemption to be opt-in.

## Alternatives

The following APIs were initially proposed to enhance the control over
preemption, but they were left out of this KEP for lack of strong use cases.

We might add them back in the future, based on feedback.


### Allow high priority jobs to borrow quota while preempting

The proposed policies for preemption within cohort require that the Workload
fits within the min quota of the ClusterQueue. In other words, we don't try to
borrow quota when preempting.

It might be desired for higher priority workloads to preempt lower priority
workloads that are borrowing resources, even if it makes the ClusterQueue
borrow resources. This could be added as `.preemption.withinCohort=LowerPriority`.

The implementation could be like the following:

For each ClusterQueue, we consider the usage as the maximum of the min quota and
the actual used quota. Then, we select flavors for the pending workload based on
this simulated usage and run the preemption algorithm.

**Reasons for discarding/deferring**

It's unclear whether this behavior is useful and it adds complexity.

### Inform how costly is to interrupt a Workload

A workload might have a known cost of interruption that varies over time.
For example:
Early in its execution, the Workload hasn't made much progress, so it can be
preempted. Later, the Workload is on the path of doing significant progress, so
it's best not to disturb it. Lastly, the Workload is expected to have made
some checkpoints, so it's ok to disturb it.

This could be expressed with the following configuration:

```yaml
apiVersion: kueue.x-k8s.io/v1alpha2
kind: Workload
metadata:
  name: my-workload
spec:
  preemption:
    disruptionCostMilestones:
    - seconds: 60
      cost: 100
    - seconds: 600
      cost: 0
```

The cost is a linear interpolation of the configuration above. A graphical
representation of the cost looks like the following (not to scale):

```
cost

100      __
        /  \___
       /       \___
      /            \___
0    /                 \_
    0    60             600  time
```

As a cluster administrator, I can configure default `disruptionCostMilestones`
for certain workloads using webhooks or setting them for all Workloads in a
LocalQueue.

**Reasons for discarding/deferring**

- Users could be incentivized to increase their cost.
- Administrators might not be able to set a default that fits all users.
- The use case in [#83](https://github.com/kubernetes-sigs/kueue/issues/83#issuecomment-1224602577)
  is mostly covered by `ClusterQueue.spec.waitBeforePreemptionSeconds`.

A better approach would be that the workload actively publishes the cost of
interrupting it, but this is an ongoing discussion upstream https://issues.k8s.io/107598

### Penalizing long running workloads

A variant of the concept of cost to interrupt a workload is a penalty to
Workloads that have been running for a long time. For example, by allowing them
to be preempted by pending Workloads of the same priority after some time.

One way this could be implemented is by introducing a concept of dynamic
priority: the priority of a Workload could increase when they stay pending for
a long time, or it could be reduced as the Workload keeps running.

**Reasons for discarding/deferring**

This can be implemented separately from the preemption APIs and algorithm, with
specialized APIs to control priority. So it can be left for a different KEP.

### Terminating Workloads on preemption

For some Workloads, it's not desired to restart them after preemption without
some manual intervention or verification (for example, interactive jobs).

This behavior could be configured like this:

```yaml
apiVersion: kueue.x-k8s.io/v1alpha2
kind: Workload
metadata:
  name: my-workload
spec:
  onPreemption: Terminate # OR Requeue (default)
```

**Reasons for discarding/deferring**

There is no clean mechanism to terminate a Job and all its running Pods.
There are two means to terminate all running Pods of a Job, but they have
some problems:

1. Delete the Job. The pods will be deleted (gracefully) on cascade.

  This could mean loss of information for the end-user, unless they have a
  finalizer on the Job. In a sense, it violates
   [`ttlSecondsAfterFinish`](https://kubernetes.io/docs/concepts/workloads/controllers/ttlafterfinished/)

2. Just suspend the Job.
  
   This option leaves a Job that is not finished, then it wouldn't be
   cleaned after `ttlSecondsAfterFinish`
   couldn't clean it up.

   Simply adding a `Failed` condition after suspending the Job could leave its
   Pods running indefinitely if the kubernetes job controller doesn't have a
   chance to delete all the Pods based on the `.spec.suspend` field.

One possibility is to insert the `FailureTarget` condition in the Job status,
introduced by [KEP#3329](https://github.com/kubernetes/enhancements/tree/master/keps/sig-apps/3329-retriable-and-non-retriable-failures)
for a different purpose.

Perhaps we should have an explicit API for this behavior, but it needs to be
done upstream.

Similar work needs to be done for workload CRDs.
We should have an explicit API for this behavior

### Extra knobs in ClusterQueue preemption policy

These extra knobs could enhance the control over preemption:

```golang
type ClusterQueuePreemption struct {
  // triggerAfterWorkloadWaitingSeconds is the time in seconds that Workloads
  // in this ClusterQueue will wait before triggering preemptions of active
  // workloads in this ClusterQueue or its cohort (when reclaiming quota).
  //
  // The time is measured from the first time the workload was attempted for
  // admission. This value is present as the `transitionTimestamp` of the
  // Admitted condition, with status=False.
  TriggerAfterWorkloadWaitingSeconds int64

  // workloadSorting determines how Workloads from the cohort that are
  // candidates for preemption are sorted.
  // Sorting happens at the time when a Workload in this ClusterQueue is
  // evaluated for admission. All the Workloads in the cohort are sorted based
  // on the criteria defined in the preempting ClusterQueue.
  // workloadOrder is a list of comparison criteria between two Workloads that
  // are evaluated in order.
  // Possible criteria are:
  // - ByLowestPriority: Prefer to preempt the Workload with lower priority.
  // - ByLowestRuntime: Prefer to preempt the Workload that started more
  //   recently.
  // - ByLongestRuntime: Prefer to preempt the Workload that started earlier.
  //
  // If empty, the behavior is equivalent to
  // [ByLowestPriority, ByLowestRuntime].
  WorkloadSorting []WorkloadComparison

  type WorkloadSortingCriteria string

  const (
    ComparisonByLowestPriority = "ByLowestPriority"
    ComparisonByLowestRuntime = "ByLowestRuntime"
  )
}
```

The proposed field `ClusterQueue.spec.preemption.triggerAfterWorkloadWaitingSeconds`
can be interpreted in two ways:
1. **How long jobs are willing to wait**.
   This shouldn't be problematic. The field can be configured based purely on
   the importance of the Workloads served by the preempting ClusterQueue.
2. **The characteristics of the workloads in the cohort**; for example, how long
   they take to finish or how often they perform checkpointing, on average.
   This implies that all workloads in the cohort have similar characteristics
   and all the ClusterQueues in the cohort should have the same wait period.

This caveat should be part of the documentation as a best practice for how to
setup the field.


**Reasons for discarding/deferring**

The usefulness of the field `triggerAfterWorkloadWaitingSeconds` is somewhat
questionable when the ClusterQueue is saturated (all the workloads require
preemption). If the ClusterQueue is in `BestEffortFIFO` mode, it's possible
that all the elements will trigger preemption once the deadline for at least
one Workload is satisfied.

For simplicity of the API, we will start with implicit sorting rules.
