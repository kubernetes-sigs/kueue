# KEP-9988: MultiKueue Global Quotas

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [The role of manager quotas](#the-role-of-manager-quotas)
  - [Manager vs. workers separation](#manager-vs-workers-separation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [Manager quota automation](#manager-quota-automation)
  - [Cross-worker resource stats in Visibility API](#cross-worker-resource-stats-in-visibility-api)
  - [User stories](#user-stories)
    - [Story 1](#story-1)
    - [Story 2](#story-2)
    - [Story 3](#story-3)
    - [Story 4](#story-4)
  - [Notes/Constraints/Caveats](#notesconstraintscaveats)
    - [Factors influencing desired manager quota](#factors-influencing-desired-manager-quota)
      - [Potential reasons for increasing manager quota](#potential-reasons-for-increasing-manager-quota)
      - [Potential reasons for decreasing manager quota](#potential-reasons-for-decreasing-manager-quota)
    - [Defining related worker ClusterQueues](#defining-related-worker-clusterqueues)
    - [Treatment of ResourceFlavors](#treatment-of-resourceflavors)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design details](#design-details)
  - [API surface](#api-surface)
    - [Manager quota automation](#manager-quota-automation-1)
    - [Cross-worker resource stats in Visibility API](#cross-worker-resource-stats-in-visibility-api-1)
      - [Utilization stats: worker-centric (new) vs. manager-centric (existing)](#utilization-stats-worker-centric-new-vs-manager-centric-existing)
  - [MultiKueue Cache](#multikueue-cache)
    - [Maintaining the cache up to date](#maintaining-the-cache-up-to-date)
    - [The guiding principles](#the-guiding-principles)
  - [MultiKueue ClusterQueue Reconciler](#multikueue-clusterqueue-reconciler)
  - [MultiKueue Workload Reconciler](#multikueue-workload-reconciler)
  - [Visibility Server](#visibility-server)
  - [Test Plan](#test-plan)
  - [Graduation Criteria](#graduation-criteria)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
  - [Support quota automation for multiple manager-side ResourceFlavors](#support-quota-automation-for-multiple-manager-side-resourceflavors)
  - [Support quota automation for no manager-side ResourceFlavors (auto-create one)](#support-quota-automation-for-no-manager-side-resourceflavors-auto-create-one)
  - [Avoid the quota automation multiplier](#avoid-the-quota-automation-multiplier)
  - [Make the `MultiKueueManagerQuotaAutomation` Condition message more informative](#make-the-multikueuemanagerquotaautomation-condition-message-more-informative)
  - [Store another set of fields in MultiKueue Cache](#store-another-set-of-fields-in-multikueue-cache)
<!-- /toc -->

## Summary

This KEP outlines the design for automatic aggregation of quotas of worker ClusterQueues in a MultiKueue setup. In this area, we distinguish two aspects: 

* surfacing aggregated quota information on the manager for user visibility;
* automatically adjusting the manager-side quota for effective gatekeeping.

## Motivation

MultiKueue involves **two layers** of Kueue quota management. A MultiKueue workload must first reserve quota on the **manager** cluster in order to be dispatched to (one or more) workers; then it must reserve quota on a **worker** in order to execute there.

### The role of manager quotas

While _worker quotas_ are the _ultimate definition_ of the actual resource availability, the  role of _manager quotas_ is subtler, yet still important.

Specifically, manager quotas **may** serve:

- As a **gate-keeping** mechanism, which allows distributing the _scheduling load_ between the manager and the workers. \
  (This may be irrelevant for small MultiKueue setups - but is essential for scalability of the largest ones).

- As a **representation** of resource availability and usage, viewable by the user without connecting to the worker clusters.

Manager quotas are **effectively optional**: when considered not useful, the Batch Admin can effectively disable them by specifying very large values ("infinite quota"). This may work well for smaller setups (and some users actually do this) - but is not recommended generally.

### Manager vs. workers separation

Currently, quota management is **fully separated** between the manager and workers, up to the point that both sides are **unaware** of each other's quotas.

While this approach offers simplicity and network savings, it also leads to problems:

1. For the manager quotas to serve their purposes discussed [above](#the-role-of-manager-quotas), they often must be **maintained in sync** with the worker quotas. \
   (The exact meaning of "in sync" may vary by use case; see [User Story 2](#story-2)). \
   Currently, MultiKueue does not help in this maintenance, making it entirely **user's responsibility**. \
   This is inconvenient and error-prone.

2. For a user to get the overview of actual resource availability and usage across the worker fleet, it is necessary to connect to all worker clusters and aggregate the information on their own.

3. For the MultiKueue controller on the manager cluster, having no awareness of workers' quotas (and their usage) makes dispatching choices less effective. \
   Even though we generally don't want the manager to take over _the whole_ orchestrating work from the workers (as we intend to share it between both sides), some improvements are likely possible. For example, if a workload fits within the quota of worker A but not of worker B, there is no point in dispatching it to B before A. (At least, unless B could admit it through borrowing).

   While designing specific improvements to dispatching is **out of scope** of this KEP, they all **depend** on building a mechanism allowing for manager awareness of workers' quotas - which this KEP proposes.

### Goals

* Make the manager cluster aware of worker's quotas, at real time.
* Expose in the manager cluster a view of resource availability and usage across all connected worker clusters.
* Enable automated maintenance of manager quotas to keep it in sync with total worker quotas.
* Make the above automation configurable enough to fit, at least roughly, various use cases.

### Non-Goals

* Fine-tune manager quota automation to achieve the optimal scheduling throughput.
* Improve MultiKueue dispatching.

## Proposal

The **core** of the proposed solution is to let the MultiKueue controller on the manager cluster **watch** the ClusterQueue and LocalQueue objects in the workers. (Just like it already watches other worker objects, e.g. Workloads).

Based on that shared core, we propose **two enhancements** of the user-facing APIs, described below.

### Manager quota automation

We will add an **optional** feature of **auto-aggregating quotas** from the workers to the manager.

**When enabled** for a manager ClusterQueue `Q`, this feature will set its quota for a resource `R` according to the following recipe:

1. Determine the set of [related worker ClusterQueues](#defining-related-worker-clusterqueues), i.e. those which can serve the dispatched remotes of the workloads submitted to `Q`.

1. Compute the **sum** of quotas for `R` in **all** _related_ worker ClusterQueues, across **all** ResourceFlavors.

1. Multiply the above sum by a user-configurable **multiplier**. \
   (This is intended to address several [reasons for adjusting the manager quota](#factors-influencing-desired-manager-quota), and thus cover most cases of [User Story 2](#story-2). Also, specifying a very large multiplier is technically a way to cover [User Story 3](#story-3)).

Enabling this feature will be controlled by an API field (not just by a feature gate), as we want to permanently retain a way to opt out (see [User Story 4](#story-4)).

Enabling this feature will **require** that the manager ClusterQueue `Q` has **exactly one ResourceFlavor**. (See [Treatment of ResourceFlavors](#treatment-of-resourceflavors) for rationale).

### Cross-worker resource stats in Visibility API

In the existing [Visibility API](https://github.com/kubernetes-sigs/kueue/tree/main/keps/168-2-pending-workloads-visibility), we will add a new endpoint, exposing **cross-worker resource stats** for a given MultiKueue manager ClusterQueue.

In the Alpha stage, we plan this endpoint to surface the following information:

* `FlavorsReservation` and `FlavorsUsage` - in the same format and analogous meaning as [already used in ClusterQueueStatus](https://github.com/kubernetes-sigs/kueue/blob/6163e91e5a62befbdd421097fe0ec38b37d406e0/apis/kueue/v1beta2/clusterqueue_types.go#L294-L309); however, based on the worker-side workload statuses and flavor assignments.

* The values of per-flavor quotas, aggregated from the specs of all [related worker ClusterQueues](#defining-related-worker-clusterqueues), exposed in the same format as the above usage stats.

Depending on the user feedback from the Alpha stage, we may decide to provide analogous resource stats also in other formats. Essentially, our "raw" data is a multi-dimensional array of numbers (where the dimensions are: worker cluster, worker ClusterQueue, ResourceFlavor, resource name, and the choice of "nominalQuota/reserved/used") - it is yet to be chosen how to arrange that information to be most useful.

### User stories

#### Story 1

As a MultiKueue user (Workload Owner or Batch Admin), I want to see a summary of resource availability and usage of my whole MultiKueue setup, surfaced by the manager cluster (which I'm treating as my single control plane).

In particular, I may want to see this summary broken down per worker-side ResourceFlavors.

#### Story 2

As a Batch Admin, I want to maintain manager quotas  **reasonably synced** to total woker quotas, to achieve a desired gate-keeping behavior on the manager.

Here, the meaning of "reasonably synced" can vary by use case:

* The **baseline approach** (most intuitive, and currently recommended in MultiKueue documentation) is to keep the manager quota **equal to the sum** of worker quotas.

* Yet, there are several reasons for which the user may want to keep the manager quota **higher or lower** than the abovementioned sum. \
  See [Factors influencing desired manager quota](#factors-influencing-desired-manager-quota) for more details.

#### Story 3

As a Batch Admin, I want to keep MultiKueue manager quotas "infinite". 

In my use case, the potential gains from meaningful ("finite") manager quotas are outweighed by the effort of maintaining them in "appropriate sync", or by the cost of missed opportunities to schedule (in case we haven't sufficiently [increased the manager quota](#potential-reasons-for-increasing-manager-quota)).

#### Story 4

As a Batch Admin, I want to keep MultiKueue manager quotas meaningful ("finite") but **not managed** by the feature proposed in this KEP.

This could be because:

* I need multiple manager ResourceFlavors (due to e.g. using a special dispatcher or a heterogenous topology across the workers; see [Treatment of ResourceFlavors](#treatment-of-resourceflavors) for details).

* I need to fine-tune the manager gate-keeping functionality in a way which cannot be easily expressed as "total workers capacity with a multiplier".

* I want to control the manager quotas manually (e.g. to make them more stable, or less confusing).

### Notes/Constraints/Caveats

#### Factors influencing desired manager quota

While the "baseline approach" (keep manager quota **equal** to total worker quota) is the most intuitive one, there are several reasons for which the user may wish to maintain manager quotas at a higher or a lower level.

##### Potential reasons for increasing manager quota

1. **Divergence of quota reservation** between manager and workers - caused e.g. by how MultiKueue design deals with quota fragmentation among the workers.

   For example, suppose there are 2 workers of capacity 10 CPU, and workloads `wl1`, `wl2`, `wl3`, `wl4` of sizes 7, 7, 6, 3 CPU respectively are submitted to the manager (in that order).

   Then, if manager quota is 10+10, `wl3` will block quota on the manager and get stuck on both workers, preventing `wl4` from reserving quota on the manager.

   OTOH, if manager quota is 23 or more, `wl4` will reserve quota on the manager, and get promptly scheduled on one of the workers, with no unnecessary delays.

2. **Divergence of admission** between manager and workers - caused e.g. by issue [#8585](https://github.com/kubernetes-sigs/kueue/issues/8585) or issue [#9338](https://github.com/kubernetes-sigs/kueue/issues/9338).

   In both these issues, it is possible to have a workload which is _admitted_ on the manager but _not admitted_ on _any_ of the workers (and to have such state for a prolonged time). Whenever that happens, a "perfect sync" between manager and workers quotas could lead to under-utilizing worker resources.

   Even when both those issues become closed, the fixes will be placed behind feature gates, making them still observable in the upcoming release.

3. **Borrowing on a worker**. This may raise the effective capacity of a worker CQ beyond its nominal quota. If the manager quota does not account for that, we risk under-utilization.

4. Other reasons _may_ also exist. For example - assuming hypothetically that a user wants to apply different preemption policies on different worker ClusterQueues (e.g. `Never` on one vs. `LowerPriority` on another) - "over-booking quota" on the manager would be the only way to have both policies honored without extra delays.

##### Potential reasons for decreasing manager quota

1. **Stray workloads on a worker**. When a LocalQueue `lq` points to a ClusterQueue `cq`, both on the manager and a worker, there are numerous ways to have a workload admitted to `cq` on the worker but on the manager:

   * the worker could be submitted directly to `lq` on the worker, "by-passing" MultiKueue;
   * the worker could be submitted to another LocalQueue `lq2` on the worker, where `lq2` also points to `cq` on the worker but is unrelated to MultiKueue;
   * the worker could be submitted to another LocalQueue `lq2` on the worker, while `lq2` exists on the manager but points to another `cq2` with MultiKueue enabled.

   Some users consider such scenarios valid, and expressed a desire to keep a part of the worker capacity reserved for such "stray workloads". In such cases, the manager quota should be accordingly _decreased_.

2. **Per-team quotas on the manager** (strictly speaking, a special case of "stray workloads").

   A Batch Admin may want to maintain a set of per-team manager ClusterQueues in order to specify separate quotas for them (possibly, but not necessarily, including Cohorts and borrowing), while simplifying the worker-side setup to single ClusterQueues representing actual resource availability.

   In this scenario, each manager ClusterQueue should have quota _significantly below_ the total worker quota; we'd rather consider _the sum_ of all manager quotas to be "on par" with total worker quotas (up to all the divergences considered above).

   Nevertheless, some automatic synchronization may be still useful, if equipped with per-team multipliers. For example: "Let the nominal quota for teams A / B / C be respectively defined as 50% / 30% / 20% of the total available worker quota".

#### Defining related worker ClusterQueues

MultiKueue dispatching connects **LocalQueues** by **their names**: for a workload submitted to a LocalQueue `lq1` on the manager, its remotes will be submitted to LocalQueues named `lq1` on the workers. The **ClusterQueue names** on either side are **irrelevant** for this connection.

Therefore, for a given manager ClusterQueue `Q`, the set of **related worker ClusterQueues** is the set `R` defined by the following recipe:

1. Let `N` = the set of names of all manager LocalQueues pointing to `Q`.

2. Let `L` = the set of LocalQueues on all workers whose name belongs to `N`.

3. Let `R` = the set of all ClusterQueues on all workers to which at least one LocalQueue from `L` points.

(For simplicity, we've ignored resource borrowing between ClusterQueues, on either manager or worker side).

#### Treatment of ResourceFlavors

MultiKueue enforces **no relationships** between ResourceFlavors on the manager and worker sides. The flavors on both sides may have different names; they may even be differently grouped into ResourceGroups.

If a MultiKueue AdmissionCheck is attached to a manger ClusterQueue with multiple ResourceFlavors, it must apply to **all** those flavors (since [#2047](https://github.com/kubernetes-sigs/kueue/pull/2047)). Then, the selection of ResourceFlavor on the manager side has typically **no effect** on the remotes - it doesn't affect neither the built-in dispatchers nor scheduling on the worker side.

Therefore, the proposed restriction of the quota automation feature to single-flavor manager configs is _very mild_: multiple flavors on the manager side _should not be needed for most users_. Theoretical cases when it would be needed include:

* Using a flavor-aware custom MultiKueue dispatcher.
* Using TAS on the workers, with incompatible names of topology levels on different workers.

Our proposal for now is to leave this cases unsupported. (Some other ideas are discussed in Alternatives, see [here](#support-quota-automation-for-multiple-manager-side-resourceflavors) and [here](#support-quota-automation-for-no-manager-side-resourceflavors-auto-create-one)).

### Risks and Mitigations

The main risks of this proposal are the following:

1. User confusion - the users may be surprised by, and have troubles understanding, several aspects:
   
   1. ClusterQueue quotas being adjusted automatically, without their initiative;
   1. divergences between the "official" quotas on the manager and worker sides, caused by the quota automation multiplier;
   1. the exact semantics of the new stats in Visibility API.

1. Introducing management of ClusterQueue quotas which some users consider undesired (see [User Story 4](#story-4)).

1. Decreasing MultiKueue performance by adding more reconcilers and computations.

For these, we propose the following mitigations:

* Risks 1.i and 1.ii are mitigated by a dedicated ClusterQueue condition, explaining the automation process as well as the multiplier.

* Risk 1.iii is mitigated by meaningful comments on the new API fields.

* Risks 1 and 2 are mitigated by introducing feature gates as well as a permanent opt-out API. \
  This also mitigates Risk 3, as long as we pay attention to skip computations which are not necessary per the ClusterQueue configuration.

* Risk 3 is mitigated by a careful planning of [MultiKueue Cache](#multikueue-cache); see in particular [its guiding principles](#the-guiding-principles).

## Design details

### API surface

#### Manager quota automation

The configuration for this feature will be added to the existing `MultiKueueConfig` struct. (For context, `MultiKueueConfig` objects are referenced from `AdmissionCheckSpec` as [`Parameters`](https://github.com/kubernetes-sigs/kueue/blob/24209c461b72fd6519581aca2234fb8f05dd1ce7/apis/kueue/v1beta2/admissioncheck_types.go#L60); this will allow controlling the feature independently for each ClusterQueue in the manager cluster).

```go
type MultiKueueConfig struct {
   // ...

   // quotaAutomation specifies whether (and how) the ClusterQueue quotas 
   // in the manager cluster should be automatically updated 
   // based on the total quota in all related worker ClusterQueues.
   QuotaAutomation *QuotaAutomation`json:"quotaAutomation,omitEmpty"`
}

type QuotaAutomation struct {
   // enabled specifies whether ClusterQueue quotas in the manager cluster
   // should be automatically set based on worker-side quotas.
   // The default value depends on the feature gate MultiKueueManagerQuotaAutomation.
   Enabled *bool `json:"enabled,omitEmpty"`

   // quotaMultiplier will be applied on top of the total worker-side quotas
   // in order to define the manager-side quota to be automatically set.
   // This value is ignored if the quota automation feature is disabled.
   // Defaults to 3.
   QuotaMultiplier *float32 `json:"quotaMultiplier,omitEmpty"`
}
```

The status of the feature will be communicated by a new Condition added to ClusterQueueStatus. The Condition will be present whenever quota automation is enabled (either by `.Enabled == True` or by the feature gate).

When the manager quota has been automated successfully, the condition will look like this:

```yaml
type: MultiKueueManagerQuotaAutomation
status: True
reason: QuotaAutomated
message: ClusterQueue quota is automatically managed based on MultiKueue workers. Applying total worker capacity with a multiplier specified in MultiKueueConfig.QuotaAutomation.QuotaMultiplier.
lastTransitionTime: 2026-01-01T00:00:00Z
```

where `lastTransitionTime` only tracks changes affecting the Condition `status` and `message` (i.e. feature enablement, potential errors) but **not** subsequent quota updates. (An alternative approach is discussed [here](#make-the-multikueuemanagerquotaautomation-condition-message-more-informative)).

Enabling quota automation will be subject to a **constraint** that the manager ClusterQueue has exactly one ResourceFlavor. This constraint will **not** be enforced via validation (e.g. a webhook) but by Kueue controllers code (similarly to [analogous existing AdmissionCheck-related constraints](https://github.com/kubernetes-sigs/kueue/blob/6163e91e5a62befbdd421097fe0ec38b37d406e0/pkg/cache/scheduler/clusterqueue.go#L284-L307), evaluated in the core ClusterQueue controller). A violation of this new constraint will **not** make the ClusterQueue Inactive (as is the case for the existing analogous constraints). In this case of a violation, the queue itself will be still operational, just not supportable by the quota automation feature. This will be communicated as an error of that feature, by its status Condition:

```yaml
type: MultiKueueManagerQuotaAutomation
status: False
reason: MultipleFlavors
message: MultiKueue manager quota automation does not support ClusterQueues with multiple ResourceFlavors.
lastTransitionTime: 2026-01-01T00:00:00Z
```

#### Cross-worker resource stats in Visibility API

In the Kueue Visbility API, we will add a new [query](https://github.com/kubernetes-sigs/kueue/blob/3f0c4b2884fe5577d7a7ae4c3579a49718077be3/pkg/visibility/storage/storage.go#L26-L31), `clusterqueues/workerresourcestats`, allowing fetching two kinds of resource stats:

* **per-flavor utilization** stats (`FlavorsReservation`, `FlavorsUsage`),
* **per-flavor quota** stats (`FlavorsQuota`).

```go
// ...
// +genclient:method=GetMultiKueueWorkerResourceStats,verb=get,subresource=multikueueworkerresourcestats,result=sigs.k8s.io/kueue/apis/visibility/v1beta2.MultiKueueWorkerResourceStats
type ClusterQueue struct {
   // ...

   // multiKueueWorkerResourceStats contain aggregated resource stats (quota and utilization)
   // of the remote workloads dispatched from the given MultiKueue manager ClusterQueue.
   // Retrieving this field returns an error for any other ClusterQueue,
   // or when the feature gate MultiKueueWorkerResourceStats is disabled.
   MultiKueueWorkerResourceStats MultiKueueWorkerResourceStats `json:"multiKueueWorkerResourceStats,omitEmpty"`
}

type MultiKueueWorkerResourceStats struct {
   // flavorsReservation are the quotas *reserved* on MultiKueue workers
   // by the remote clones of workloads from this manager ClusterQueue.
   // They are grouped by worker-side flavors.
   // +listType=map
   // +listMapKey=name
   FlavorsReservation []FlavorUsage `json:"flavorsReservation,omitempty"`

   // flavorsUsage are the quotas used on MultiKueue workers
   // by the *admitted* remote clones of workloads from this manager ClusterQueue.
   // They are grouped by worker-side flavors.
   // +listType=map
   // +listMapKey=name
   FlavorsUsage []FlavorUsage `json:"flavorsUsage,omitempty"`

   // flavorsQuotas are the total quotas of all ClusterQueues on the
   // MultiKueue workers which may serve workloads from this manager ClusterQueue.
   // They are grouped by worker-side flavors.
   // +listType=map
   // +listMapKey=name
   FlavorsQuotas []FlavorUsage `json:"flavorsQuotas,omitempty"`
}
```

All these fields use the `FlavorUsage` type already defined [here](https://github.com/kubernetes-sigs/kueue/blob/6163e91e5a62befbdd421097fe0ec38b37d406e0/apis/kueue/v1beta2/clusterqueue_types.go#L333-L344).

##### Utilization stats: worker-centric (new) vs. manager-centric (existing)

The above **flavor utilization** stats closely correspond to [already existing ClusterQueueStatus fields](https://github.com/kubernetes-sigs/kueue/blob/6163e91e5a62befbdd421097fe0ec38b37d406e0/apis/kueue/v1beta2/clusterqueue_types.go#L294-L309), of the same name and type.

While those *existing fields* expose a *manager-centric* view of the resource stats, the **new fields** will differ by being mostly **worker-centric**, specifically:

* grouped by _worker-side_ flavors,
* tied to the _worker-side_ workload status (quota reserved / admitted),
* aggregating _worker-side_ quota reservations, that is: if a single manager workload has multiple remotes reserving quota (which is possible following [#8592](https://github.com/kubernetes-sigs/kueue/pull/8592), especially if worker clusters observe long-running AdmissionChecks), we'll add up those quota reservations on all the workers.

In comparison to the [existing manager-centric fields](https://github.com/kubernetes-sigs/kueue/blob/6163e91e5a62befbdd421097fe0ec38b37d406e0/apis/kueue/v1beta2/clusterqueue_types.go#L294-L309), we've also removed the limit of list length (set to [64 items](https://github.com/kubernetes-sigs/kueue/blob/6163e91e5a62befbdd421097fe0ec38b37d406e0/apis/kueue/v1beta2/clusterqueue_types.go#L299)). This is because the number of [related worker ClusterQueues](#defining-related-worker-clusterqueues) is theoretically unbounded, and each of those [could have different ResourceFlavor names](#treatment-of-resourceflavors).

However, the new flavor utilization stats are **not** going to be just aggregates of the [existing ClusterQueueStatus fields](https://github.com/kubernetes-sigs/kueue/blob/6163e91e5a62befbdd421097fe0ec38b37d406e0/apis/kueue/v1beta2/clusterqueue_types.go#L294-L309) from all [related worker ClusterQueues](#defining-related-worker-clusterqueues). Doing so would include various kinds of "stray" workloads unrelated to the considered manager ClusterQueue (see [Potential reasons for decreasing manager quota](#potential-reasons-for-decreasing-manager-quota)). Instead, we will aggregate resource stats **only** for remotes **dispatched from** the given manager ClusterQueue.

### MultiKueue Cache

Both features will be based on a new **MultiKueue Cache**, storing some information retrieved from the watched resources and fetched workloads.

The cache format will be tentatively as follows: \
(referring to the existing definitions of [`FlavorResourceQuantities`](https://github.com/kubernetes-sigs/kueue/blob/3f0c4b2884fe5577d7a7ae4c3579a49718077be3/pkg/resources/resource.go#L37) and [`LocalQueueReference`](https://github.com/kubernetes-sigs/kueue/blob/3f0c4b2884fe5577d7a7ae4c3579a49718077be3/pkg/util/queue/local_queue.go#L25-L26))

```go
type clusterReference string

type clusterQueueReference string

type workloadReference struct {
   Name string
   Namespace string
}

type FlavorsUtilization struct {
   Reserved FlavorResourceQuantities
   Admitted FlavorResourceQuantities 
}

type wlGroupResourceInfo struct {
   // This is not nil if and only if the workload has quota reserved locally.
   LocalClusterQueue *clusterQueueReference
   FlavorsUtilization FlavorsUtilization
}

type MultiKueueCache struct {
   localLqToCqMap map[LocalQueueReference]clusterQueueReference

   localMultiKueueConfigs map[clusterQueueReference]MultiKueueConfig

   remoteLqToCqMap map[clusterReference]map[LocalQueueReference]clusterQueueReference

   remoteCqQuotas map[clusterReference]map[clusterQueueReference]FlavorResourceQuantities

   wlGroupResourceStats map[workloadReference]wlGroupResourceInfo

   remoteResourceStatsByLocalCq map[clusterQueueReference]FlavorsUtilization
}
```

The cache instance will be created in the [MultiKueue `SetupControllers` method](https://github.com/kubernetes-sigs/kueue/blob/25538a4ea2979d975d75792c1f9a7124a0475c4a/pkg/controller/admissionchecks/multikueue/controllers.go#L111), and passed down to specific controllers as necessary.

#### Maintaining the cache up to date

The cache fields will be kept up to date in the following way:

* `localLqToCqMap` will be maintained by the new [MK ClusterQueue Reconciler](#multikueue-clusterqueue-reconciler), on which we'll also call `.Watches()` to watch LocalQueues on the manager cluster (analogously as e.g. [here](https://github.com/kubernetes-sigs/kueue/blob/25538a4ea2979d975d75792c1f9a7124a0475c4a/pkg/controller/admissionchecks/multikueue/workload.go#L915)).

* `localMultiKueueConfigs` will be maintained by the new [MK ClusterQueue Reconciler](#multikueue-clusterqueue-reconciler), on which we'll also call `.Watches()` to watch AdmissionChecks and MultiKueueConfigs on the manager cluster. \
  (This way, we'll detect *any* change in the "CQ -> AC -> MKConfig" dependency chain; cf. issue [#10122](https://github.com/kubernetes-sigs/kueue/issues/10122)).

* `remoteLqToCqMap` will be maintained by `.Watch()` API calls for LocalQueues in the remote clients (like it's done [here](https://github.com/kubernetes-sigs/kueue/blob/25538a4ea2979d975d75792c1f9a7124a0475c4a/pkg/controller/admissionchecks/multikueue/multikueuecluster.go#L207) for other resource kinds). 

  However, unlike the existing Workloads handling (where change events travel through a special channel to finally trigger a `Reconcile()` in the MK Workload Reconciler), `remoteLqToCqMap` will be updated _directly_ in response to any `.Watch()` results. \
  (This is because, in our current case, we need to handle the update while knowing in which remote cluster it happened, and that bit information is essentially not passable via the standard kube-controller workqueues).

* `remoteCqQuotas` will be maintained by `.Watch()` API calls for ClusterQueues in the remote clients, also in a _direct_ manner (similarly as `remoteLqToCqMap` in the previous point).

* `wlGroupResourceStats` and `remoteResourceStatsByLocalCq` will be updated in the MK Workload Reconciler. \
  The latter map is simply an aggregating derivative of the former; yet, maintaining both allows O(1) computations while reconciling workloads and serving Visiblity API queries. See [below](#multikueue-workload-reconciler) for more details.

While we propose to bundle all fields in a single structure for conceptual complexity, the **need** for specific fields will **depend on the feature gates** introduced in this KEP, as follows:

| field \ feature gate           | `MultiKueueManagerQuotaAutomation` | `MultiKueueWorkerResourceStats` |
| ------------------------------ | :--------------------------------: | :-----------------------------: |
| `localLqToCqMap`               | needed                             | needed                          |
| `localMultiKueueConfigs`       | needed                             | -                               |
| `remoteLqToCqMap`              | needed                             | needed                          |
| `remoteCqQuotas`               | needed                             | needed                          |
| `wlGroupResourceStats`         | -                                  | needed                          |
| `remoteResourceStatsByLocalCq` | -                                  | needed                          |

A specific field will be maintained only if needed for some enabled feature gate.

#### The guiding principles

The proposed format of MultiKueue Cache has been chosen to keep it _possibly simple_ under the following _principles_:

1. Avoid O(workload count) computations.
2. Avoid cross-cluster API calls.
3. Avoid unbounded batches of local API calls triggered by a single Kubernetes event.
4. Tolerate computations of time complexity O(total number of LocalQueues and ClusterQueues on the manager and worker clusters).

These principles may be debatable. In particular, #3 is already violated in MultiKueue code, e.g. in `queueWorkloadsForConfig` making [a batch](https://github.com/kubernetes-sigs/kueue/blob/15764bcf9d471f7452697a43df09c7bbf20c888a/pkg/controller/admissionchecks/multikueue/workload.go#L883-L890) of `List` calls. The proposed `localMultiKueueConfigs` map may be used to eliminate that batch as well.

This choice is also [discussed](#store-another-set-of-fields-in-multikueue-cache) in the Alternatives section.

### MultiKueue ClusterQueue Reconciler

Besides the [existing MultiKueue reconcilers](https://github.com/kubernetes-sigs/kueue/blob/161a2abc9e484b9fe0d6b3025c0bcac4fb8ef3a4/pkg/controller/admissionchecks/multikueue/controllers.go#L151-L165), we'll add a new one for (manager) ClusterQueue objects. 

Its operation will be enabled by the `MultiKueueManagerQuotaAutomation` feature gate, and heavily based on the [MultiKueue Cache](#multikueue-cache).

Reconciling a manager ClusterQueue `Q` will proceed as follows:

1. Fetch `Q` from the API client.

2. Determine whether quota automation has been requested for `Q` (based on `localMultiKueueConfigs` and the feature gate). \
   If not, exit.

3. Determine whether quota automation can be supported for `Q` (i.e. if it has exactly one ResourceFlavor). \
   If not, set the `MultiKueueManagerQuotaAutomation` Condition accordingly (to `False`), and exit.

4. Determine the new quota values for `Q` (based on all cache fields needed for quota automation).

5. Compare the desired state of `Q` (quotas, `MultiKueueManagerQuotaAutomation` Condition status and message) with the desired values. If there are any differences, update `Q`.
   * In some cases, this may mean *two* updates: one of Spec (quotas) and one of Status (the Condition). \
     In such cases, update the Condition first. (See the "Takeaway" part [here](#make-the-multikueuemanagerquotaautomation-condition-message-more-informative) for the rationale).

Triggering such a reconcile will happen in the following situations: \
(in a large part following the [definition of related worker queues](#defining-related-worker-clusterqueues))

1. On a creation or deletion of a manager LocalQueue, we'll update `localLqToCqMap`, and then trigger reconcile for its ClusterQueue. \
   (Updates can be ignored because the LocalQueue -> ClusterQueue assignment is immutable).

2. On a creation or deletion of a worker LocalQueue, we'll update `remoteLqToCqMap`, and then trigger reconcile for the manager ClusterQueue assigned to the corresponding manager LocalQueue (same name & namespace).

3. On any event regarding a worker ClusterQueue `Q2`, we'll update `remoteCqQuotas`, then find all LocalQueues assigned to `Q2` (by scanning `remoteLqToCqMap`), and trigger reconciles for all local ClusterQueues as dictated by `localLqToCqMap`.

4. On any event regarding a manager AdmissionCheck, we'll find all manager ClusterQueues using it (with an API `.List` call), and trigger reconciles for them (having updated `localMultiKueueConfigs` if necessary).

5. On any event regarding a manager MultiKueueConfig, we'll find all ClusterQueues using it (by scanning `localMultiKueueConfigs`), and trigger reconciles for them (having updated `localMultiKueueConfigs` before).

6. Finally, out of the box, a reconcile will be triggered by any Kubernetes event regarding `Q` itself. \
   As a result, the quota automation - when enabled - will promptly override any quota changes made by any other party. \
   If this effect is not desired, the user can always opt out from quota automation.

### MultiKueue Workload Reconciler

In the MK Workload Reconciler, every run of [`Reconcile`](https://github.com/kubernetes-sigs/kueue/blob/3f0c4b2884fe5577d7a7ae4c3579a49718077be3/pkg/controller/admissionchecks/multikueue/workload.go#L158) will update the values of `wlGroupResourceStats` and `remoteResourceStatsByLocalCq` in the [MultiKueue Cache](#multikueue-cache).

The cache maintenance will happen after [this call](https://github.com/kubernetes-sigs/kueue/blob/25538a4ea2979d975d75792c1f9a7124a0475c4a/pkg/controller/admissionchecks/multikueue/workload.go#L224) `.readGroup()`. For non-deletion scenarios, it will be postponed to follow [this call](https://github.com/kubernetes-sigs/kueue/blob/083e985b0f750901b813e70a1e168348b21a42f8/pkg/controller/admissionchecks/multikueue/workload.go#L240) to `.reconcileGroup()`. For a given workload group `W`, it will proceed as follows:

1. Let `info := wlGroupResourceStats[W]`.

2. Subtract `info.FlavorsUtilization` from `remoteResourceStatsByLocalCq[*info.LocalClusterQueue]`. \
   (Skip this if `info.LocalClusterQueue` is `nil`).

3. Update `info.LocalClusterQueue` based on the status of `W.local`.

4. Update `info.FlavorsUtilization`, based on the resource requests and statuses of `W`'s remotes.

5. Store the updated `info` back in `wlGroupResourceStats[W]`.

6. Add `info.FlavorsUtilization` to `remoteResourceStatsByLocalCq[*info.LocalClusterQueue]`. \
   (Skip this if `info.LocalClusterQueue` is `nil`).

Thus, maintaining the aggregated values will be incremental, taking O(1) time per each reconcile. It's also idempotent.

Notably, we anticipate no need for "snapshotting", even when Kueue is started (or restarted) in an existing cluster, or a new MK worker appears, etc. In such cases, [the `.Watch()` calls on remote workloads](https://github.com/kubernetes-sigs/kueue/blob/3f0c4b2884fe5577d7a7ae4c3579a49718077be3/pkg/controller/admissionchecks/multikueue/multikueuecluster.go#L178) will lead to a wave of reconciles for all workloads unknown to MultiKueue, through which the MultiKueue Cache will be updated incrementally.

### Visibility Server

The Visibility Server will be provided with [MultiKueue Cache](#multikueue-cache) [via `main.go`](https://github.com/kubernetes-sigs/kueue/blob/3f0c4b2884fe5577d7a7ae4c3579a49718077be3/cmd/kueue/main.go#L380), similarly as it's already given the core queues cache.

Based on that, the new REST endpoint (`clusterqueues/workerresourcestats`), given a ClusterQueue `Q`, will do the following:

1. Check if `Q` is a MultiKueue manager queue. Return an error otherwise.

2. Set `FlavorsReservation` and `FlavorsUsage` based on `remoteResourceStatsByLocalCq[Q]`.

3. [Determine the set of related worker ClusterQueues](#defining-related-worker-clusterqueues), based on `localLqToCqMap` and `remoteLqToCqMap`.

4. Set `FlavorsQuotas` based on summing up `remoteCqQuotas` values for all related worker ClusterQueues.

This will involve conversions from [`FlavorResourceQuantities`](https://github.com/kubernetes-sigs/kueue/blob/3f0c4b2884fe5577d7a7ae4c3579a49718077be3/pkg/resources/resource.go#L37) to [`FlavorUsage`](https://github.com/kubernetes-sigs/kueue/blob/3f0c4b2884fe5577d7a7ae4c3579a49718077be3/apis/kueue/v1beta2/clusterqueue_types.go#L333). This is deliberate; the former format allows more efficient and convenient arithmetics.

### Test Plan

TODO

### Graduation Criteria

TODO

## Drawbacks

Besides introducing some risks (see [Risks and Mitigations](#risks-and-mitigations)), the proposed solution feels suboptimal mostly in the following ways:

1. The `QuotaMultiplier` value feels very arbitrary, and it may be difficult to determine its optimal value (even for a specific use case). Two approaches coming to mind are:

   1. An experienced Batch Admin can try out various values and judge the effects by observing overall system efficiency. (That, of course, assumes no other factors substantially interfering in such experimenting).

   1. The quotient between manager-side reserved quota and total worker quota could be exposed as a metric and monitored. \
      The proposed solution makes this easier to implement (in exposing the total worker quota via Visibility API) but does not include setting up such a metric specifically. Even if it did, monitoring the metric (and likely collecting it for some period of time) would be necessarily the responsibility of the user.

1. While the `QuotaMultiplier` allows addressing individual [reasons for increasing manager quota](#potential-reasons-for-increasing-manager-quota) or [for decreasing it](#potential-reasons-for-decreasing-manager-quota), some combinations of those reasons remain not supportable in the most efficient way. \
   For example, in the "Per-team quotas on the manager" scenario discussed [here](#potential-reasons-for-decreasing-manager-quota), the Batch Admin will be able to configure manager-side ClusterQuotas for teams A / B / C to have 50% / 30% / 20% of the total workers quota respectively, but then each of the teams will be vulnerable to scheduling delays resulting from quota fragmentation, as explained in the "Divergence of quota reservation" scenario [here](#potential-reasons-for-increasing-manager-quota). \
   Conversely, an attempt to increase the quotas for each of the teams would allow individual teams to consume more quotas than intended. For example, if the Batch Admin applied our default multiplier (3.0) on top of the initial per-team assignments discussed above, team B would be able to consume 90% of the total worker capacity.

   This drawback may be addressable by introducing a separate "quota overbooking multiplier" (discussed in #3 [here](#avoid-the-quota-automation-multiplier)) - yet, the overall complexity of such solution feels unacceptable for now. We may consider it later depending on the feedback.

## Alternatives

### Support quota automation for multiple manager-side ResourceFlavors

The proposed constraint that quota automation requires a single ResourceFlavor on the manager side may feel inconvenient; for example, it prevents from implementing [User Story 1](#story-1) in its full scope ("broken down per worker-side ResourceFlavors") via the manager ClusterQueue quota, which would be the most intuitive place.

**Reasons for discarding**

* As explained [here](#treatment-of-resourceflavors), ResourceFlavors on the manager side are typically meaningless for MultiKueue scheduling. Even if we enforced alignment between ResourceFlavors on the manager and worker sides (e.g. two ResourceFlavors A, B on both sides), there could easily exist workloads e.g. admitted on A on the manager but on B on the worker, which would be unnecessarily confusing.

* Even if we introduced an enforced alignment of flavor assignments between the manager and workers (to address the above issue), this would open a new set of difficult questions:

  * What should determine the flavor assignment on the ClusterQueue side? \
    (It could be manager-side scheduler logic or the outcome of MultiKueue dispatching, neither option perfect).
  * What if the flavors setup is changed on one of the workers? Should we re-arrange manager flavors accordingly?
  * What if the flavors setup is conflicting across the workers, so that no compatible manager-side arrangement exists? \
    (For example, worker1 has `flavorA` covering `cpu` and `flavorB` covering `memory` while worker2 has only `flavorA` covering both. A "naive aggregation" on the manager would produce `flavorA` covering both and `flavorB` covering just `memory`, which is invalid inside `.Spec.ResourceGroups` of a ClusterQueue). \
    Should we disable quota automation in those cases? Or validate against them? (Again, neither option perfect).

### Support quota automation for no manager-side ResourceFlavors (auto-create one)

In the case when a user creates a MultiKueue setup with quota automation enabled right away, as we'll intend to auto-maintain quotas in a single ResourceFlavor, it may seem "nicest" for the user to let them skip that flavor altogether in their ClusterQueueSpec. (A bit surprisingly, the current validation rules allow `.Spec.ResourceGroups` to be empty).

Then, we could auto-create a single ResourceFlavor, with quotas determined by automation - thus saving user's time for typing a few more lines of YAML configs.

**Reasons for discarding/deferring**

This would become problematic in combination with TAS. Supporting TAS workloads requires the manager-side ResourceFlavor to be compatible with the worker-side TAS levels. Therefore, automatic setup of manager-side ResourceFlavor would need to either leave TAS use cases unsupported (which feels poor) or fetch topology setups from the workers (leading to questions how to deal with any inconsistencies between them). This feels overly complex, at least for the start.

### Avoid the quota automation multiplier

The proposed multiplier mechanism is admittedly quite counter-intuitive, and we've considered various approaches to avoid it, for example:

1. Abandon quota automation altogether. (Focus on exposing visibility).

2. Implement quota automation but abandon the multiplier. \
   (This seems appealing at the first glance, as it would allow implementing [User Story 1](#story-1) via the ClusterQueue quotas, which is the most intuitive place - even if without the breakdown per worker-side flavor).

3. Set manager quota to the total worker quota (without the multiplier), but on top of that allow manager-side "overbooking" **only for quota reservation** (not for admitting) by an analogous MK-specific multiplier (just with a different name). \
   For example: if there are 2 workers of capacity 10 CPU each, quota automation is enabled and `MultiKueueConfig` has `ManagerQuotaOverbookingMultiplier` set to `3`, then the manager ClusterQueue quota would be set to 20 CPU; yet, Kueue controller on the manager cluster would allow _quota reservation_ (and hence dispatching to worker clusters) for up to 60 CPU, and then enforce that no set of workloads requesting more than 20 CPU in total can get _admitted_.

**Reasons for discarding/deferring**

* For #1, our assessment is that the value of quota automation justifies introducing it, especially given our mitigations to make the multiplier possibly understandable, and the permanent option to opt-out.

* For #2, we choose to introduce the multiplier, given the [numerous potential reasons](#factors-influencing-desired-manager-quota) for using it.

* For #3, we identified a number of subtle disadvantages:

  * Replacing one form of confusion ("why is manager quota so high?") with another ("why do we have more quota reserved on the manager than the nominal limit?").

  * Adding complexity to core Kueue code. (In particular, we'd need to carefully plan how to enforce the "don't admit over the quota" constraint).

  * Blurring separation of concerns, by introducing new ways of MultiKueue awareness to core Kueue code.

  * Removing a way to address the "Borrowing on a worker" case discussed [here](#potential-reasons-for-increasing-manager-quota) - because that case is caused by divergence of _admission_, not just _quota reservation_.

  * Making the quota automation feature dependent on the `MultiKueueWaitForWorkloadAdmitted` feature gate (as a fix of [#8585](https://github.com/kubernetes-sigs/kueue/issues/8585)).

  Still, this idea remains appealing, for at least two reasons:

  * It allows retaining the most natural meaning of manager-side quota ("what is the total weight of workloads that we want to allow to _execute_ at a time") while still allowing to _dispatch above the quota levels_ which seems necessary for best MultiKueue efficiency, as discussed in the "Divergence of quota reservation" scenario [here](#potential-reasons-for-increasing-manager-quota).

  * Implementing _both kinds_ of multipliers (one for "admissible quota", _and_ one for "reservation over-booking") would allow reconciling various reasons for increasing and decreasing manager quota (see [Drawback #2](#drawbacks)). \
    Specifically, in the example scenario described there, the Batch Admin could define the manager quotas for ClusterQueues for teams A / B / C as 50% / 30% / 20% of the total worker capacity, and _on top of that_ introduce a 3x "reservation over-booking" multiplier. \
    (Also, this would offer a way to handle the abovementioned "Borrowing on a worker" scenario). \
    Yet, the complexity of this approach feels unacceptable for the start.

  Therefore, overall, we propose to **shelve** this idea for now, to be possibly revisited based on user feedback.

### Make the `MultiKueueManagerQuotaAutomation` Condition message more informative

The Condition message could include specific numbers to help the user understand quota calculation, e.g.

```yaml
message: ClusterQueue quota is automatically managed based on MultiKueue workers. Applying total worker capacity (cpu: 10, memory: 25Gi) with a 3x buffer multiplier.
lastTransitionTime: 2026-01-01T00:00:00Z
```

Correspondingly, `lastTransitionTime` would track the last moment when such a richer message changed, effectively indicating when the last automatic adjustment of the ClusterQueue quota happened.

**Reasons for discarding/deferring**

The ClusterQueue quotas (part of `.Spec`) and Conditions (part of `.Status`) cannot be updated simultaneously. Thus, every auto-adjustment of quotas would introduce a short-lived divergence between the Condition and the actual quotas, also at the level of specific numbers. This would bring a risk of user confusion, of at least two kinds:

* A "sufficiently atomic" snapshot of cluster content might reveal a ClusterQueue's `.Status` inconsistent with its `.Spec` (for example, nominal quota being 50 CPU while the Condition claiming it's "20 CPU with a 3x multiplier").

* The Condition's `lastTransitionTime` would become somewhat misleading in one way or another. \
  (That is, it will necessarily diverge either from the actual quota change time or from the Condition update time, as these two times will differ. Each choice may be perceived as confusing).

**A takeaway for the proposed approach**

While our proposed design does not _fully eliminate_ the above inconsistencies (when enabling quota automation for a pre-existing ClusterQueue, we'll still need to update the quota and the Condition _separately_, in some order), it decreases its frequency, and makes it less confusing. \
In particular, if we choose to update the Condition before the quota, the intermediate state can be still interpreted as the Condition saying "quota management has been [just] enabled [but hasn't yet managed to act]", which may be considered legitimate.

### Store another set of fields in MultiKueue Cache

The content of MultiKueueCache could be changed in various ways, e.g.:

* **Richer:** to eliminate "wasteful" linear-time computations, e.g.:
  * instead of doing inverse lookup in `localLqToCqMap`, maintain an additional `map[clusterQueueReference]sets.Set[LocalQueueReference]`
  * instead of summing up values from `remoteCqQuotas` for the related worker ClusterQueues, maintain totals ready to serve in O(1) time.

* **Simpler:** drop some of the cache fields - replacing them with ad-hoc computations - to decrease its conceptual complexity.

  * In particular, if we choose to allow unbounded batches of local API calls, we could drop the "local" cache maps (`localLqToCqMap` and `localMultiKueueConfigs`).

**Reasons for discarding**

The current design feels to be the "golden mean" between simplicity and performance, codified by the "principles" outlined [here](#a-note-on-performance).

* **Why not richer:** The tolerated performance overheads (single local API calls, computations linear in terms of Kueue setup size but not in terms of workload count) are negligible, in particular given that they're going to execute relatively infrequently (in event handlers for relatively stable objects, like LocalQueues, ClusterQueues etc.), as opposed to Workload event handlers or Kueue scheduling cycle.

* **Why not simpler:** This is a less obvious judgement call; yet, to maintain Kueue healthy, we'd prefer to avoid delays noticeable by human perception, even in relatively infrequent reconciling routines.
