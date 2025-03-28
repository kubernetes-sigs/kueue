---
title: "Preemption"
date: 2024-05-28
weight: 7
description: >
  Preemption is the process of evicting one or more admitted Workloads to accommodate another Workload.
---

In a preemption, the following terms are relevant:
- **Preemptees**: The preempted Workloads.
- **Target ClusterQueues**: The ClusterQueues to which the preemptees belong.
- **Preemptor**: The Workload being accommodated.
- **Preempting ClusterQueue**: The ClusterQueue to which the preemptor belongs.

## Reasons for preemption

A Workload can preempt one or more Workloads if it is admitted in a [ClusterQueue with preemption enabled](/docs/concepts/cluster_queue/#preemption)
and any of the following events happen:
- The preemptee belongs to the same [ClusterQueue](/docs/concepts/cluster_queue) as the preemptor and the preemptee has a lower priority.
- The preemptee belongs to the same [cohort](/docs/concepts/cluster_queue#cohort) as the preemptor and the preemptee's ClusterQueue has a usage above
  the [nominal quota](/docs/concepts/cluster_queue#resources) for at least one resource that the preemptee and preemptor require.

The configured settings for preemption in the [Kueue Configuration](/docs/reference/kueue-config.v1beta1#FairSharing)
and in the [ClusterQueue](/docs/concepts/cluster_queue#preemption) can limit whether a Workload can preempt others, in addition
to the criteria above.

When preempting a Workload, Kueue adds entries in the `.status.conditions` field of the preempted Workload
that is similar to the following:

```yaml
status:
  conditions:
  - lastTransitionTime: "2024-05-31T18:42:33Z"
    message: 'Preempted to accommodate a workload (UID: 5515f7da-d2ea-4851-9e9c-6b8b3333734d)
      in the ClusterQueue'
    observedGeneration: 1
    reason: Preempted
    status: "True"
    type: Evicted
  - lastTransitionTime: "2024-05-31T18:42:33Z"
    message: 'Preempted to accommodate a workload (UID: 5515f7da-d2ea-4851-9e9c-6b8b3333734d)
      in the ClusterQueue'
    reason: InClusterQueue
    status: "True"
    type: Preempted
```

The `Evicted` condition indicates that the Workload was evicted with a reason `Preempted`,
whereas the `Preempted` condition gives more details about the preemption reason.

## Preemption algorithms

Kueue offers two preemption algorithms. The main difference between them is the criteria to allow
preemptions from a ClusterQueue to others in the Cohort, when the usage of the preempting ClusterQueue is
already above the nominal quota. The algorithms are:

- **[Classic Preemption](#classic-preemption)**: Preemption in the cohort can only happen when any of the following occurs:
  - The usage of the ClusterQueue for the incoming workload will be under the nominal quota after the ongoing admission process
  - Preemption while borrowing is enabled for the workload's ClusterQueue
  - All candidates for preemption belong to the same ClusterQueue as the preempting Workload

  In the above scenarios, a workload can only be considered for preemption, in favor a workload from another ClusterQueue, 
  if it belongs to a ClusterQueue which is running over its nominal quota. 
  ClusterQueues in a cohort borrow resources in a first-come first-served fashion.
  
  This algorithm is the most lightweight of the two.

- **[Fair sharing](#fair-sharing)**: ClusterQueues with pending Workloads can preempt other Workloads in their cohort
  until the preempting ClusterQueue obtains an equal or weighted share of the borrowable resources.
  The borrowable resources are the unused nominal quota of all the ClusterQueues in the cohort.

## Classic Preemption

An incoming Workload, which does not fit within the unused quota, is eligible
to issue preemptions when one of the following is true:
- the requests of the Workload are below the flavor's nominal quota, or
- `borrowWithinCohort` is enabled.

### Candidates

The list of preemption candidates is compiled from Workloads which either:
- belong to the same ClusterQueue as the preemptor Workload, and satisfying the `withinClusterQueue` policy of the preemptor's Cluster Queue
- belong to other ClusterQueues in the cohort, which are actively borrowing, and satisfying the `reclaimWithinCohort` and `borrowWithinCohort` policies of the preemptor's Cluster Queue.

The list of candidates is sorted based on the following preference checks for
tie-breaking:
- Workloads from borrowing queues in the cohort
- Workloads with the lowest priority
- Workloads which got admitted the most recently.

### Targets

The Classic Preemption algorithm qualifies the candidates as preemption targets using the heuristics
below:

1. If all candidates belong to the target queue, then Kueue greedily
qualifies candidates until the preemptor Workload can fit, allowing the usage of
the ClusterQueue to be above the nominal quota, up to the `borrowingLimit`.
This is referred as "borrowing" in the points below.

2. If `borrowWithinCohort` is enabled, then Kueue greedily qualifies
candidates (respecting the `borrowWithinCohort.maxPriorityThreshold` threshold),
until the preemptor Workload can fit, allowing for borrowing.

3. If the current usage of the target queue is below nominal quota, then
Kueue greedily qualifies the candidates, until the preemptor Workload can fit,
disallowing for borrowing.

4. If the Workload didn't fit by using the previous heuristics, Kueue greedily
qualifies only the candidates which belong to the preempting Cluster Queue,
until the preemptor Workload can fit, allowing for borrowing.

The last step of the algorithm is to minimize the set of targets. For this
purpose, Kueue greedily traverses the list of initial targets in reverse and
removes a Workload from the list of targets if the preemptor Workload still can be
admitted when accounting back the quota usage of the target Workload.

## Fair Sharing

Fair sharing introduces the concepts of ClusterQueue share values and preemption
strategies. These work together with the preemption policies set in
`withinClusterQueue` and `reclaimWithinCohort` to determine if a pending
Workload can preempt an admitted Workload. Fair sharing uses preemptions to
achieve an equal or weighted share of the borrowable resources between the
tenants of a cohort.

{{< feature-state state="stable" for_version="v0.7" >}}

To enable fair sharing, [use a Kueue Configuration](/docs/installation#install-a-custom-configured-release-version) similar to the following:

```yaml
apiVersion: config.kueue.x-k8s.io/v1beta1
kind: Configuration
fairSharing:
  enable: true
  preemptionStrategies: [LessThanOrEqualToFinalShare, LessThanInitialShare]
```

The attributes in this Kueue Configuration are described in the following sections.

### ClusterQueue share value

When you enable fair sharing, Kueue assigns a numeric share value to each ClusterQueue to summarize
the usage of borrowed resources in a ClusterQueue, in comparison to others in the same cohort.
The share value is weighted by the `.spec.fairSharing.weight` defined in a ClusterQueue.

During admission, Kueue prefers to admit Workloads from ClusterQueues that have the lowest share value first.
During preemption, Kueue prefers to preempt Workloads from ClusterQueues that have the highest share value first.

You can obtain the share value of a ClusterQueue in the `.status.fairSharing.weightedShare` field or querying
the [`kueue_cluster_queue_weighted_share` metric](/docs/reference/metrics#optional-metrics).

### Preemption strategies

The `preemptionStrategies` field in the Kueue Configuration indicates which constraints should a
preemption satisfy, with regards to the share values of the target and preempting ClusterQueues,
before and after preempting a particular Workload.

Different `preemptionStrategies` can lead to less or more preemptions under specific scenarios.
These are the factors you should consider when configuring `preemptionStrategies`:
- Tolerance to disruptions, in particular when single Workloads use a significant amount of the borrowable resources.
- Speed of convergence, in other words, how important is it to reach a steady fair state as soon as possible.
- Overall utilization, because certain strategies might reduce the utilization of the cluster in the pursue of
  fairness.

When you define multiple `preemptionStrategies`, the preemption algorithm will only use the next
strategy in the list if there aren't any more Workloads that are candidates for preemption that
satisfy the current strategy and the preemptor still doesn't fit.

The values you can put in the `preemptionStrategies` list are:
- `LessThanOrEqualToFinalShare`: Only preempt a Workload if the share of the preempting ClusterQueue
  with the preemptor Workload is less than or equal to the share of the target ClusterQueue
  without the preempted Workload.
  This strategy might favor preemption of smaller workloads in the target ClusterQueue,
  regardless of priority or start time, in an effort to keep the share of the ClusterQueue
  as high as possible.
- `LessThanInitialShare`: Only preempt a Workload if the share of the preempting ClusterQueue
  with the preemptor Workload is strictly less than the share of the target ClusterQueue.
  Note that this strategy doesn't depend on the share usage of the Workload being preempted.
  As a result, the strategy chooses to first preempt workloads with the lowest priority and
  newest start time within the target ClusterQueue.
The default strategy is `[LessThanOrEqualToFinalShare, LessThanInitialShare]`

### Algorithm overview

The initial step of the algorithm is to identify the [Workloads that are candidate for preemption](#candidates),
with the same criteria and ordering as the classic preemption, and grouped by ClusterQueue.

Next, the above candidates are qualified as preemption targets,
following an algorithm that can be summarized as follows:

```
FindFairPreemptionTargets(X ClusterQueue, W Workload)
  For each preemption strategy:
    While W does not fit and there are workloads that are preemption candidates:
      Find the ClusterQueue Y with the highest share value.
      For each admitted Workload U in ClusterQueue Y:
        If Workload U satisfies the preemption strategy:
          Add workload U to the list of targets
  In the reverse order of the list of targets:
    Attempt to remove a Workload from the targets, while W still fits.
```
