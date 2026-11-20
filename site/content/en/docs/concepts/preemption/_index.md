---
title: "Preemption"
date: 2024-05-28
weight: 7
description: >
  Preemption is the process of evicting one or more admitted Workloads to accommodate another Workload.
no_list: true
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

The configured settings for preemption in the [Kueue Configuration](/docs/reference/kueue-config.v1beta2/#config-kueue-x-k8s-io-v1beta2-FairSharing)
and in the [ClusterQueue](/docs/concepts/cluster_queue#preemption) can limit whether a Workload can preempt others, in addition
to the criteria above.

When preempting a Workload, Kueue adds entries in the `.status.conditions` field of the preempted Workload
that is similar to the following:

```yaml
status:
  - lastTransitionTime: "2025-03-07T21:19:54Z"
    message: 'Preempted to accommodate a workload (UID: 5c023c28-8533-4927-b266-56bca5e310c1,
      JobUID: 4548c8bd-c399-4027-bb02-6114f3a8cdeb) due to prioritization in the ClusterQueue'
    observedGeneration: 1
    reason: Preempted
    status: "True"
    type: Evicted
  - lastTransitionTime: "2025-03-07T21:19:54Z"
    message: 'Preempted to accommodate a workload (UID: 5c023c28-8533-4927-b266-56bca5e310c1,
      JobUID: 4548c8bd-c399-4027-bb02-6114f3a8cdeb) due to prioritization in the ClusterQueue'
    reason: InClusterQueue
    status: "True"
    type: Preempted
```

The `Evicted` condition indicates that the Workload was evicted with a reason `Preempted`,
whereas the `Preempted` condition gives more details about the preemption reason.

The preempting workload can be found by running `kubectl get workloads.kueue.x-k8s.io --selector=kueue.x-k8s.io/job-uid=<JobUID> --all-namespaces`.

## Preemption algorithms

Kueue offers two built-in preemption algorithms, alongside a declarative configurable preemption mechanism:

- **[Classic Preemption](#classic-preemption)**: Preemption in the cohort can only happen when any of the following occurs:
  - The usage of the ClusterQueue for the incoming workload will be under the nominal quota after the ongoing admission process
  - Preemption while borrowing is enabled for the workload's ClusterQueue
  - All candidates for preemption belong to the same ClusterQueue as the preempting Workload

  In the above scenarios, a workload can only be considered for preemption, in favor of a workload from another ClusterQueue, 
  if it belongs to a ClusterQueue which is running over its nominal quota. 
  ClusterQueues in a cohort borrow resources in a first-come first-served fashion.
  
  This algorithm is the most lightweight of the algorithms.

- **[Fair Sharing](#fair-sharing)**: ClusterQueues with pending Workloads can preempt other Workloads in their cohort
  until the preempting ClusterQueue obtains an equal or weighted share of the borrowable resources.
  The borrowable resources are the unused nominal quota of all the ClusterQueues in the cohort.

- **[Configurable Preemptions](/docs/concepts/preemption/configurable_preemption)**: {{< feature-state state="alpha" for_version="v0.20" >}}
  ClusterQueues can declare custom preemption triggers (such as insufficient topology when using Topology-Aware Scheduling)
  and candidate selectors (priority comparisons, relational scopes, custom numeric labels, and workload/queue selectors)
  via the `PreemptionConfig` CRD. In Alpha, candidate outputs from `PreemptionConfig` are merged with candidates from Classic or Fair Sharing preemption.

## Classic Preemption

An incoming Workload, which does not fit within the unused quota, is eligible
to issue preemptions when one of the following is true:
- the requests of the Workload are below the flavor's nominal quota, or
- `borrowWithinCohort` is enabled, or
- `withinClusterQueue` preemption is enabled and there are preemption candidates in the same
  ClusterQueue as the preempting Workload.

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

Fair Sharing introduces the concepts of ClusterQueue share values and preemption
strategies. These work together with the preemption policies set in
`withinClusterQueue` and `reclaimWithinCohort` (but __not__ `borrowWithinCohort`) to determine if a pending
Workload can preempt an admitted Workload in Fair Sharing. Fair Sharing uses preemptions to
achieve an equal or weighted share of the borrowable resources between the
tenants of a cohort.

{{< feature-state state="stable" for_version="v0.7" >}}

To enable Fair Sharing, [use a Kueue Configuration](/docs/installation#install-a-custom-configured-release-version) similar to the following:

```yaml
apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
fairSharing:
  preemptionStrategies: [LessThanOrEqualToFinalShare, LessThanInitialShare]
```

The attributes in this Kueue Configuration are described in the following sections.

### Share value

When you enable Fair Sharing, Kueue assigns a numeric share value to each ClusterQueue to summarize
the usage of borrowed resources in a ClusterQueue, in comparison to others in the same cohort.
The share value is weighted by the `.spec.fairSharing.weight` defined in a ClusterQueue.

During admission, Kueue prefers to admit Workloads from ClusterQueues that have the lowest share value first.
During preemption, Kueue prefers to preempt Workloads from ClusterQueues that have the highest share value first.

You can obtain the share value of a ClusterQueue in the `.status.fairSharing.weightedShare` field or querying
the [`kueue_cluster_queue_weighted_share` metric](/docs/reference/metrics#optional-metrics).

In a [CohortTree](/docs/concepts/cohort/#hierarchical-cohorts), each Cohort also has a share value,
weighted by the `.spec.fairSharing.weight` of the Cohort. It summarizes how much the whole subtree of the
Cohort uses above the quota of that subtree. The quota of a subtree is the nominal quota of all the
ClusterQueues and Cohorts in it. When one of them has a `lendingLimit`, the quota that it cannot lend
does not count toward the subtree, and neither does its usage within that quota. A ClusterQueue or Cohort that uses no more than its quota has a share value of 0.

You can obtain the share value of a Cohort in its `.status.fairSharing.weightedShare` field or querying
the [`kueue_cohort_weighted_share` metric](/docs/reference/metrics#cohort-status). For how Kueue
computes share values, see the
[fair sharing KEP](https://github.com/kubernetes-sigs/kueue/blob/main/keps/1714-fair-sharing/README.md#share-value-function-and-weights).

### Admission order

With Fair Sharing, Kueue decides which pending Workload to consider next by comparing share values at
each level of the CohortTree, from the ClusterQueues up to the root Cohort:

1. Each ClusterQueue proposes one Workload: a Workload that already reserved quota and waits for its
   second scheduling pass (for example, for a delayed topology assignment), or otherwise its head
   Workload.
2. Each Cohort compares the Workloads proposed by its children, using the share value that each child
   ClusterQueue or Cohort would have if its Workload were admitted. The Cohort passes the preferred
   Workload to its parent Cohort.
3. Kueue considers first the Workload that the root Cohort prefers. Then it repeats the comparison for
   the remaining Workloads, taking into account the quota reserved so far.

At each Cohort, Kueue prefers, in order:

1. By default, a Workload whose child would not borrow on the flavors and resources that the Workload
   requests. You can disable this preference by editing the `FairSharingPrioritizeNonBorrowing`
   feature gate.
2. The Workload whose child would have the lower share value.
3. By default, the Workload with the higher priority. You can disable this preference by editing the
   `PrioritySortingWithinCohort` feature gate.
4. The Workload that was queued first.

If you enable the `PrioritizePreemptorWorkloads` feature gate (Alpha in v0.20), Kueue prefers a
Workload that is waiting for preempted Workloads to release their quota over all the others.

{{< feature-state state="alpha" for_version="v0.20" >}}
If you enable the `FairSharingRefill` feature gate, Kueue can consider more than one Workload from the
same ClusterQueue in a scheduling cycle. A Workload considered this way is only admitted in that cycle
if it fits without preemption. See the
[Fair Sharing refill KEP](https://github.com/kubernetes-sigs/kueue/blob/main/keps/14596-fair-sharing-refill/README.md).

Refer to the [Installation guide](/docs/installation/#change-the-feature-gates-configuration) for
instructions on configuring feature gates.

Fair Sharing only orders Workloads within the same CohortTree.

#### Example

Consider the following CohortTree, with a single resource (`cpu`), all weights equal to 1, and no
borrowing or lending limits:

- `org` (root Cohort)
  - `research` (Cohort)
    - `r-train` (ClusterQueue, nominal quota of 15 CPUs)
    - `r-eval` (ClusterQueue, nominal quota of 15 CPUs)
  - `prod` (Cohort)
    - `p-serve` (ClusterQueue, nominal quota of 10 CPUs)

In this tree, the share value of a ClusterQueue or Cohort is proportional to the amount of CPUs that it
borrows. The current usage is `r-train=20`, `r-eval=4` and `p-serve=8`. The head Workloads are `wT`
(4 CPUs) in `r-train`, `wE` (4 CPUs) in `r-eval` and `wP` (6 CPUs) in `p-serve`.

- In `research`, `r-train` with `wT` would use `20+4=24` CPUs and borrow 9, while `r-eval` with `wE`
  would use `4+4=8` CPUs, within its nominal quota. `research` prefers `wE`.
- In `prod`, `wP` is the only Workload.
- In `org`, `research` with `wE` would use `20+4+4=28` CPUs, within its quota of `15+15=30` CPUs, while
  `prod` with `wP` would use `8+6=14` CPUs and borrow 4. Kueue considers `wE` first.
- If `wE` is admitted, `research` with `wT` would borrow 2 CPUs, and `prod` with `wP` would borrow 4.
  `research` has the lower share value, so Kueue considers `wT` before `wP`.

### Preemption strategies

The `preemptionStrategies` field in the Kueue Configuration indicates which constraints should a
preemption satisfy, with regards to the share values of the target and preempting ClusterQueues,
before and after preempting a particular Workload.

In a hierarchical cohort, the share values compared by the preemption strategies are the shares of
the AlmostLeastCommonAncestors (AlmostLCA) of the preempting and target ClusterQueues, as defined
in the [fair sharing KEP](https://github.com/kubernetes-sigs/kueue/blob/main/keps/1714-fair-sharing/README.md),
rather than the shares of the ClusterQueues themselves. AlmostLCA(x, y) is the last but one
node on the path from x to the lowest common ancestor of x and y; the strategies compare
AlmostLCA(preemptor, target) with AlmostLCA(target, preemptor). In a flat cohort, where the
preempting and target ClusterQueues share the same parent cohort, each AlmostLCA is the
corresponding ClusterQueue itself.

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
- `LessThanOrEqualToFinalShare`: Only preempt a Workload if the share of
  AlmostLCA(preemptor, target) with the preemptor Workload admitted is less than or equal to the
  share of AlmostLCA(target, preemptor) without the workload to be preempted.
  This strategy might favor preemption of smaller workloads in the target ClusterQueue,
  regardless of priority or start time, in an effort to keep the share of AlmostLCA(target, preemptor)
  as high as possible.
- `LessThanInitialShare`: Only preempt a Workload if the share of AlmostLCA(preemptor, target)
  with the preemptor Workload admitted is strictly less than the share of
  AlmostLCA(target, preemptor) with the workload to be preempted.
  Note that this strategy doesn't depend on the share usage of the Workload being preempted.
  As a result, the strategy chooses to first preempt workloads with the lowest priority and
  newest start time within the target ClusterQueue.

Kueue only supports the following `preemptionStrategies` lists:

- `[LessThanOrEqualToFinalShare]`
- `[LessThanInitialShare]`
- `[LessThanOrEqualToFinalShare, LessThanInitialShare]`

Any other combination or ordering, such as
`[LessThanInitialShare, LessThanOrEqualToFinalShare]`, fails configuration
validation and Kueue does not start.

### Algorithm overview

The initial step of the algorithm is to identify the [Workloads that are candidate for preemption](#candidates),
with the same criteria and ordering as the classic preemption, and grouped by ClusterQueue.

Next, the above candidates are qualified as preemption targets,
following an algorithm that can be summarized as follows:

```
FindFairPreemptionTargets(X ClusterQueue, W Workload)
  For each preemption strategy:
    While W does not fit and there are workloads that are preemption candidates:
      Find the target ClusterQueue Y, walking down the CohortTree from the root Cohort.
      Find the next admitted Workload U in ClusterQueue Y such that Y is X, or, by default,
      AlmostLCA(X, Y) is within its quota, or U satisfies the preemption strategy.
      Add workload U to the list of targets.
  If W does not fit:
    Discard the list of targets.
  In the reverse order of the list of targets:
    Attempt to remove a Workload from the targets, while W still fits.
```

By default, Kueue also:
- Preempts from a borrowing ClusterQueue without comparing share values when AlmostLCA(preemptor, target)
  stays within its quota, on the resources that the preemptor requests, with the preemptor admitted.
  You can disable this by editing the `FairSharingPreemptWithinNominal` feature gate.
- Applies the first preemption strategy once more after it selects Workloads from the preempting
  ClusterQueue, because preempting them lowers the share value of the preempting ClusterQueue.
  You can disable this by editing the `FairSharingReevaluatePreemptionCandidates` feature gate,
  which is enabled by default since v0.21.
- Checks the final targets from other ClusterQueues against the preemption strategies again, with the
  preemptor admitted, unless AlmostLCA(preemptor, target) is still within its quota. If a target no
  longer satisfies any strategy, Kueue does not preempt any Workload in this scheduling cycle. You can
  disable this check by editing the `FairSharingVerifyFinalTargets` feature gate, which is enabled by
  default since v0.21.

#### Choosing the target ClusterQueue

To find the target ClusterQueue, Kueue walks down the CohortTree from the root Cohort. At each Cohort,
it moves to the child ClusterQueue or Cohort with the highest share value, computed as if the preemptor
were admitted, until it reaches a ClusterQueue. If a child Cohort and a child ClusterQueue have the same
share value, Kueue moves to the Cohort.

Kueue only considers:
- ClusterQueues and Cohorts that are borrowing the flavors and resources that need preemption. The
  preempting ClusterQueue and the Cohorts that contain it are considered even if they are not borrowing.
- In other ClusterQueues, Workloads that use a borrowed flavor and resource that needs preemption. The
  flavor and resource must be borrowed by the ClusterQueue of the Workload and by every Cohort above it,
  up to but not including the lowest common ancestor with the preempting ClusterQueue.

As a result, Kueue does not preempt from a Cohort that does not contain the preempting ClusterQueue and
that, as a whole, uses no more than its quota, even if a ClusterQueue inside it borrows from its siblings.

For example, consider the CohortTree from the [admission order example](#example), with the usage
`r-train=26`, `r-eval=10` and `p-serve=4`, so all 40 CPUs are in use. `p-serve` has
`reclaimWithinCohort: Any`, and a Workload `wS` that requests 6 CPUs is pending in it:

- In `org`, `research` uses `26+10=36` CPUs and borrows 6. `prod` would use `4+6=10` CPUs with `wS`
  admitted, within its quota, but Kueue still considers it because it contains `p-serve`. `research`
  has the higher share value, so Kueue moves to `research`.
- In `research`, `r-eval` is not borrowing, so Kueue moves to `r-train`, which borrows 11 CPUs.
- AlmostLCA(`p-serve`, `r-train`) is `prod`, which stays within its quota with `wS` admitted. Kueue
  preempts Workloads from `r-train`, without comparing share values, until `wS` fits.

If the usage were `r-train=22`, `r-eval=8` and `p-serve=10` instead, `research` would use exactly its
quota of 30 CPUs. Kueue would not preempt any Workload from `research`, even though `r-train` borrows
7 CPUs from `r-eval`.
