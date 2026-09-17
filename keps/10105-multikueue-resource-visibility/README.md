# KEP-10105: MultiKueue Resource Visibility

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
- [Proposal](#proposal)
  - [User stories](#user-stories)
    - [Story 1](#story-1)
  - [Notes/Constraints/Caveats](#notesconstraintscaveats)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design details](#design-details)
  - [API surface](#api-surface)
    - [Utilization stats: worker-centric (new) vs. manager-centric (existing)](#utilization-stats-worker-centric-new-vs-manager-centric-existing)
  - [Guiding principle for the implementation](#guiding-principle-for-the-implementation)
  - [MultiKueue Cache](#multikueue-cache)
  - [MultiKueue Workload Reconciler](#multikueue-workload-reconciler)
    - [Ensuring cache availability on non-Kueue-leader Pods](#ensuring-cache-availability-on-non-kueue-leader-pods)
  - [Visibility Server](#visibility-server)
  - [Test Plan](#test-plan)
    - [Unit Tests](#unit-tests)
    - [Integration Tests](#integration-tests)
    - [E2e Tests](#e2e-tests)
  - [Possible Follow-ups](#possible-follow-ups)
    - [Expand the cross-worker resource stats](#expand-the-cross-worker-resource-stats)
  - [Graduation Criteria](#graduation-criteria)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
  - [Expose the cross-worker resource stats in the manager ClusterQueue Status](#expose-the-cross-worker-resource-stats-in-the-manager-clusterqueue-status)
<!-- /toc -->

## Summary

This KEP proposes exposing aggregated resource stats (capacity and usage) on the MultiKueue manager cluster, through the Visibility API. It comes as a counterpart to [KEP-9988](https://github.com/kubernetes-sigs/kueue/pull/10145), addressing the User Story left unaddressed in that KEP.

## Motivation

In any job orchestrating system, it is a frequent user journey to request an overview of resource usage and capacity.

In a single-cluster Kueue deployment, this information is easily available broken down per ClusterQueue and ResourceFlavor, through the [.ResourceGroups](https://github.com/kubernetes-sigs/kueue/blob/78109e3799c9ff98ada439b99fb92f00c70c634c/apis/kueue/v1beta2/clusterqueue_types.go#L58-L66) field (for capacity) and [these Status fields](https://github.com/kubernetes-sigs/kueue/blob/78109e3799c9ff98ada439b99fb92f00c70c634c/apis/kueue/v1beta2/clusterqueue_types.go#L294-L309) (for usage).

In a MultiKueue setup, things get more complicated, in that:

* For the manager-side ClusterQueue, the abovementioned fields become less informative:

  * `.ResourceGroups` describes the _manager-side quotas_, which may diverge from the _total worker-side quotas_ for various reasons (see [KEP-9988](https://github.com/kubernetes-sigs/kueue/pull/10145)).
  * Similarly, `.FlavorsReservation` describes quota reservations on the _manager side_, which may diverge from the total quota reservation on the worker side.
  * Also, all these statistics are grouped by _manager-side flavors_, which in most cases will not map to _worker-side flavors_ but rather aggregate them all into a single-flavor bag (as discussed [here](https://github.com/olekzabl/kueue/blob/br35/keps/9988-multikueue-manager-quota-automation/README.md#treatment-of-resourceflavors)).

* For the worker-side ClusterQueues, the resource stats fields are more useful; however:

  * The user currently needs to fan out to all workers and aggregate the information on their own. That's inconvenient.

  * Manager-side and worker-side ClusterQueues [may not form a strictly 1-1 correspondence](https://github.com/olekzabl/kueue/blob/br35/keps/9988-multikueue-manager-quota-automation/README.md#defining-related-worker-clusterqueues). \
    As soon as the correspondence gets "skewed" for any reason (which could be, for example, one of the scenarios described [here](https://github.com/olekzabl/kueue/blob/br35/keps/9988-multikueue-manager-quota-automation/README.md#potential-reasons-for-decreasing-manager-quota)), a MultiKueue user wishing to treat the manager-cluster as their "control plane", and mentally categorizing their workloads by the manager-side ClusterQueue to which they're submitted, will not be able to gather relevant stats by aggregating those available for worker-side ClusterQueues.

### Goals

* Expose a **centralized** view (i.e. exposed by the manager cluster) of resource availability and usage across all connected worker clusters.
* Make the above view organized per **manager-side ClusterQueue** and **worker-side ResourceFlavor**.

## Proposal

In the existing [Visibility API](https://github.com/kubernetes-sigs/kueue/tree/main/keps/168-2-pending-workloads-visibility), we will add a new endpoint, exposing **cross-worker resource stats** for a given MultiKueue manager ClusterQueue.

In the Alpha stage, we plan this endpoint to surface the following information:

* `FlavorsReservation` and `FlavorsUsage` - in the same format and analogous meaning as [already used in ClusterQueueStatus](https://github.com/kubernetes-sigs/kueue/blob/6163e91e5a62befbdd421097fe0ec38b37d406e0/apis/kueue/v1beta2/clusterqueue_types.go#L294-L309); however, based on the **worker-side** workload statuses and flavor assignments.

* The values of per-flavor quotas, aggregated from the specs of all [related worker ClusterQueues](#defining-related-worker-clusterqueues), exposed in the same format as the above usage stats.

Depending on the user feedback from the Alpha stage, we may decide to provide analogous resource stats also in other formats (see [here](#expand-the-cross-worker-resource-stats) for details).

### User stories

#### Story 1

As a MultiKueue user (Workload Owner or Batch Admin), I want to see a summary of resource availability and usage of my whole MultiKueue setup, surfaced by the manager cluster (which I'm treating as my single control plane).

In particular, I may want to see this summary broken down per worker-side ResourceFlavors.

### Notes/Constraints/Caveats

While designing this functionality, one should keep in mind the following caveats: \
(as they're shared between this KEP and [KEP-9988](https://github.com/kubernetes-sigs/kueue/pull/10145), let us just mention them briefly here and refer to more detailed discussions there):

1. MultiKueue [does **not** enforce](https://github.com/olekzabl/kueue/blob/br35/keps/9988-multikueue-manager-quota-automation/README.md#defining-related-worker-clusterqueues) 1-1 correspondence between manager-side and worker-side **ClusterQueues**. \
  Instead, their relation is defined by matching names (and namespaces) of LocalQueues, and hence can be many-to-many.

2. Also, MultiKueue [does **not** enforce](https://github.com/olekzabl/kueue/blob/br35/keps/9988-multikueue-manager-quota-automation/README.md#treatment-of-resourceflavors) (and even by default prevents) a correspondence between manager-side and worker-side **ResourceFlavors**.

3. [KEP-9988](https://github.com/kubernetes-sigs/kueue/pull/10145) is closely related to this KEP as it proposes automatic management of manager-side quotas based on worker-side totals.

   While this may sound like just achieving a part of this KEP's goal (namely, exposing workers' quota stats), that specific functionality cannot be reused here for two major reasons:

   * Auto-managed quota values are not broken down per worker-side ResourceFlavor. \
     (They couldn't feasibly be, given that they're used as quotas on the manager-side; see the previous Caveat). \
     This restriction does not hold in the Visibility API, and is lifted in this KEP.

   * Auto-managed quota values [may in the future diverge](https://github.com/olekzabl/kueue/blob/br35/keps/9988-multikueue-manager-quota-automation/README.md#add-a-way-to-adjust-the-manager-side-quota) from the sum of worker-side quotas.

### Risks and Mitigations

The main risks of this proposal are the following:

1. User confusion about the exact semantics of the new stats in Visibility API.

2. Slowing down MultiKueue by adding more reconcilers and computations.

3. Consuming too much memory with MultiKueue cache.

For these, we propose the following mitigations:

* All risks will be initially mitigated by introducing a feature gate.

* Risk 1 is mitigated by meaningful comments on the new API fields.

* Risk 2 can be substantially mitigated by extending [MultiKueue Cache](#multikueue-cache); see our [guiding principles](#guiding-principles-for-the-implementation).

* Risk 3 feels low: even though [MultiKueue Cache](#multikueue-cache) scales linearly with the number of all workloads (which can grow large), the amount of data stored per workload is low, likely below what's in the Kueue scheduler cache. \
  Still, if this turns out problematic, the current cache format can be drastically compressed by tweaking [`FlavorResourceQuantities`](https://github.com/kubernetes-sigs/kueue/blob/3f0c4b2884fe5577d7a7ae4c3579a49718077be3/pkg/resources/resource.go#L37) to use short map keys (instead of `Flavor` and `Resource`, use their indices in some global map; these will certainly fit in an `int32`). \
  For now, it feels a premature optimization, so we don't propose it right away.

## Design details

### API surface

In the Kueue Visibility API, we will add a new [query](https://github.com/kubernetes-sigs/kueue/blob/3f0c4b2884fe5577d7a7ae4c3579a49718077be3/pkg/visibility/storage/storage.go#L26-L31), `clusterqueues/workerresourcestats`, allowing fetching two kinds of resource stats:

* **per-flavor utilization** stats (`FlavorsReservation`, `FlavorsUsage`),
* **per-flavor quota** stats (`FlavorsQuotas`).

```go
// ...
// +genclient:method=GetMultiKueueWorkerResourceStats,verb=get,subresource=multikueueworkerresourcestats,result=sigs.k8s.io/kueue/apis/visibility/v1beta2.MultiKueueWorkerResourceStats
type ClusterQueue struct {
    // ...

    // multiKueueWorkerResourceStats contain aggregated resource stats (quota and utilization)
    // of the remote workloads dispatched from the given MultiKueue manager ClusterQueue.
    // Retrieving this field returns an error for any other ClusterQueue,
    // or when the feature gate MultiKueueResourceVisibility is disabled.
    MultiKueueWorkerResourceStats MultiKueueWorkerResourceStats `json:"multiKueueWorkerResourceStats,omitempty"`
}

// +k8s:openapi-gen=true
// +kubebuilder:object:root=true
type MultiKueueWorkerResourceStats struct {
    metav1.TypeMeta   `json:",inline"`
    metav1.ObjectMeta `json:"metadata,omitempty"`

    // flavorsReservation are the quotas *reserved* on MultiKueue workers
    // by the remote clones of workloads from this manager ClusterQueue.
    // They are grouped by worker-side flavors.
    // +listType=map
    // +listMapKey=name
    FlavorsReservation []WorkerFlavorUsage `json:"flavorsReservation,omitempty"`

    // flavorsUsage are the quotas used on MultiKueue workers
    // by the *admitted* remote clones of workloads from this manager ClusterQueue.
    // They are grouped by worker-side flavors.
    // +listType=map
    // +listMapKey=name
    FlavorsUsage []WorkerFlavorUsage `json:"flavorsUsage,omitempty"`

    // flavorsQuotas are the total quotas of all ClusterQueues on the
    // MultiKueue workers which may serve workloads from this manager ClusterQueue.
    // They are grouped by worker-side flavors.
    // +listType=map
    // +listMapKey=name
    FlavorsQuotas []WorkerFlavorUsage `json:"flavorsQuotas,omitempty"`
}

type WorkerFlavorUsage struct {
    // name of the flavor.
    // +required
    Name ResourceFlavorReference `json:"name,omitempty"`

    // resources lists the quota usage for the resources in this flavor.
    // +listType=map
    // +listMapKey=name
    // +kubebuilder:validation:MaxItems=64
    // +required
    Resources []WorkerResourceUsage `json:"resources,omitempty"`
}

type WorkerResourceUsage struct {
    // name of the resource
    // +required
    Name corev1.ResourceName `json:"name,omitempty"`

    // total is the total quantity of used quota, including the amount borrowed
    // from the cohort.
    // +optional
    Total resource.Quantity `json:"total,omitempty"`
}
```

The `WorkerFlavorUsage` type is deliberately forked from [`FlavorUsage`](https://github.com/kubernetes-sigs/kueue/blob/6163e91e5a62befbdd421097fe0ec38b37d406e0/apis/kueue/v1beta2/clusterqueue_types.go#L333-L344), already used in ClusterQueueStatus; the only difference is removing the [`Borrowed`](https://github.com/kubernetes-sigs/kueue/blob/6163e91e5a62befbdd421097fe0ec38b37d406e0/apis/kueue/v1beta2/clusterqueue_types.go#L359) field - for which it could be cumbersome to define the intended meaning in a MultiKueue setup (supporting that is [a possible follow-up](#expand-the-cross-worker-resource-stats)).

#### Utilization stats: worker-centric (new) vs. manager-centric (existing)

The above **flavor utilization** stats closely correspond to [already existing ClusterQueueStatus fields](https://github.com/kubernetes-sigs/kueue/blob/6163e91e5a62befbdd421097fe0ec38b37d406e0/apis/kueue/v1beta2/clusterqueue_types.go#L294-L309), of the same name and type.

While those *existing fields* expose a *manager-centric* view of the resource stats, the **new fields** will differ by being mostly **worker-centric**, specifically:

* grouped by _worker-side_ flavors,
* tied to the _worker-side_ workload status (quota reserved / admitted),
* aggregating _worker-side_ quota reservations, that is: if a single manager workload has multiple remotes reserving quota (which is possible following [#8592](https://github.com/kubernetes-sigs/kueue/pull/8592), especially if worker clusters observe long-running AdmissionChecks), we'll add up those quota reservations on all the workers.

In comparison to the [existing manager-centric fields](https://github.com/kubernetes-sigs/kueue/blob/6163e91e5a62befbdd421097fe0ec38b37d406e0/apis/kueue/v1beta2/clusterqueue_types.go#L294-L309), we've also removed the limit of list length (set to [64 items](https://github.com/kubernetes-sigs/kueue/blob/6163e91e5a62befbdd421097fe0ec38b37d406e0/apis/kueue/v1beta2/clusterqueue_types.go#L299)). This is because the number of [related worker ClusterQueues](#defining-related-worker-clusterqueues) is theoretically unbounded, and each of those [could have different ResourceFlavor names](#treatment-of-resourceflavors).

However, the new flavor utilization stats are **not** going to be just aggregates of the [existing ClusterQueueStatus fields](https://github.com/kubernetes-sigs/kueue/blob/6163e91e5a62befbdd421097fe0ec38b37d406e0/apis/kueue/v1beta2/clusterqueue_types.go#L294-L309) from all [related worker ClusterQueues](#defining-related-worker-clusterqueues). Doing so would include various kinds of "stray" workloads unrelated to the considered manager ClusterQueue (see [Potential reasons for decreasing manager quota](#potential-reasons-for-decreasing-manager-quota)). Instead, we will aggregate resource stats **only** for remotes **dispatched from** the given manager ClusterQueue.

### Guiding principle for the implementation

While we generally intend to generously allow various API calls (as discussed [here](https://github.com/olekzabl/kueue/blob/br35/keps/9988-multikueue-manager-quota-automation/README.md#a-note-on-client-usage) in [KEP-9988](https://github.com/kubernetes-sigs/kueue/pull/10145)), an important exception is that we want to **avoid** API calls with **O(workload count)** response size, especially remote ones.

This is to avoid processing lots of data at once. For context, workload count can reach hundreds of thousands.

While we'll tolerate some *O(workload count) computations*, we'll prefer that their input are smaller than full Workload objects, to reduce the total amount of data processed.

**Remote** API calls of that kind should be **strongly avoided** because, even though MultiKueue already watches remote Workloads, it doesn't currently cache them. (And if we enabled client-level caching here, it would have a significantly larger memory footprint than the proposed [MultiKueue Cache](#multikueue-cache)).

### MultiKueue Cache

A new **MultiKueue Cache** will store the minimal information on the watched remote workloads that is needed to avoid bulk-fetching them from worker clusters.

Specifically, it will contain:

* for each manager-side ClusterQueue - the set of workloads with quota reserved in it;

* for each manager-side workload `W` - the aggregated resource usage of all its remotes, broken down per worker-side resource flavor, and per "reserved" / "admitted" usage type.

The cache instance will be created in the [MultiKueue `SetupControllers` method](https://github.com/kubernetes-sigs/kueue/blob/25538a4ea2979d975d75792c1f9a7124a0475c4a/pkg/controller/admissionchecks/multikueue/controllers.go#L111), and passed down to specific controllers as necessary.

### MultiKueue Workload Reconciler

The MultiKueue Workload Reconciler will be responsible for maintaining the [MultiKueue Cache](#multikueue-cache).

This will be handled in every run of [`Reconcile`](https://github.com/kubernetes-sigs/kueue/blob/3f0c4b2884fe5577d7a7ae4c3579a49718077be3/pkg/controller/admissionchecks/multikueue/workload.go#L158), after [this call](https://github.com/kubernetes-sigs/kueue/blob/25538a4ea2979d975d75792c1f9a7124a0475c4a/pkg/controller/admissionchecks/multikueue/workload.go#L224) to `.readGroup()`. \
For non-deletion scenarios, it will be postponed to follow [this call](https://github.com/kubernetes-sigs/kueue/blob/083e985b0f750901b813e70a1e168348b21a42f8/pkg/controller/admissionchecks/multikueue/workload.go#L240) to `.reconcileGroup()`. 

On a Kueue restart, the cache will be eventually re-populated, as the controller-runtime library will schedule `Reconcile()` for all pre-existing (local) Workload objects.

To ensure no "permanent zombie entries" in the cache (in a case of a missed delete event), we will add a "garbage collection routine" analogous to [this one](https://github.com/kubernetes-sigs/kueue/blob/249ad53617ee4d2aa0ee6573d4b38418bc549304/pkg/controller/admissionchecks/multikueue/multikueuecluster.go#L665). In the Alpha version, we will hook it to the same "GC interval" setting, defined [here](https://github.com/kubernetes-sigs/kueue/blob/9c92d93bf6f1988e59a459623dc9b345c2161ef6/apis/config/v1beta2/configuration_types.go#L302-L305).

#### Ensuring cache availability on non-Kueue-leader Pods

If the Kueue controller is deployed on multiple Pods, the Visibility Server will operate on all those Pods (leader as well as followers). This requires ensuring availability of [MultiKueue Cache](#multikueue-cache) on all those Pods.

For the Kueue core cache (on which the Visibility Server already depends), this is ensured by making the core controllers ["leader aware"](https://github.com/kubernetes-sigs/kueue/blob/main/pkg/controller/core/leader_aware_reconciler.go), so that the follower instances run only the predicate methods (`.Create()`/ `.Update()` / `.Delete()`) but not the `Reconcile()` method. Then, as long as the cache is populated in predicates, it can be kept up-to-date also on the followers, without the proper `.Reconcile()` methods racing for etcd updates.

This approach is **insufficient** in our case, as [MultiKueue Cache](#multikueue-cache) depends on _remote_ API Workloads which we need to fetch in the `.Reconcile()` method. This logic is too heavy to be moved to predicates.

Therefore, for our case, we propose a **variation** of [`leader_aware_reconciler`](https://github.com/kubernetes-sigs/kueue/blob/main/pkg/controller/core/leader_aware_reconciler.go), which would accept a controller implementing a richer interface

```golang
type ReconcilerWithFollowerObserver interface {
  TypedReconciler[Request]
  Observe(context.Context, Request) (Result, error)
}
```

and, when about to reconcile an event, delegate to `.Reconcile()` or `.Observe()`, depending on the current leader/follower role.

Then, we'll make the MultiKueue Workload Reconciler wrapped with that, with the `.Observe()` method responsible only for updating [MultiKueue Cache](#multikueue-cache). (Internally, there will be some code sharing to avoid duplication).

**Note:** If necessary, this plan should be also easily adaptable to the proposal of migrating the Visibility Server to a separate Pod / Deployment, which was initially proposed in [#10553](https://github.com/kubernetes-sigs/kueue/issues/10553). (However, the current plan for solving that issue is anyway different).

### Visibility Server

The Visibility Server will be provided with [MultiKueue Cache](#multikueue-cache) - [via `main.go`](https://github.com/kubernetes-sigs/kueue/blob/3f0c4b2884fe5577d7a7ae4c3579a49718077be3/cmd/kueue/main.go#L380), similarly as it's already given the core queues cache.

Based on that, the new REST endpoint (`clusterqueues/workerresourcestats`), given a ClusterQueue `Q`, will do the following:

1. Check if `Q` is a MultiKueue manager queue. If not, return an error.

2. Determine and fetch the [related worker ClusterQueues](https://github.com/olekzabl/kueue/blob/br35/keps/9988-multikueue-manager-quota-automation/README.md#defining-related-worker-clusterqueues). \
   (By a sequence of local and remote API calls, involving LocalQueues and ClusterQueues).

3. Set `FlavorsQuotas` based on summing up the quotas for all related worker ClusterQueues.

4. Set `FlavorsReservation` and `FlavorsUsage` to the total usage stats, based on the [MultiKueue Cache](#multikueue-cache).

   * Notably, this involves no API calls.

   * For cleaner blackboxing, this logic will be wrapped in a public method of the MultiKueue Cache object. \
     This way, the Visibility Server won't have write access to the cache (or even read access to its internals).

### Test Plan

[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.

#### Unit Tests

* For the [MultiKueue Cache](#multikueue-cache):

  * Test the public helper exposed to the [Visibility Server](#visibility-server).

* For the [MultiKueue Workload Reconciler](#multikueue-workload-reconciler):

  * Test the updates to MultiKueue Cache.

* For the [Visibility Server](#visibility-server):

  * Test steps 1-3 of the recipe. (Test 4 will be covered by MultiKueue Cache unit tests).

#### Integration Tests

* For the integration between [MultiKueue Workload Reconciler](#multikueue-workload-reconciler) and [Visibility Server](#visibility-server):

  * Test some _relatively simple_ scenario of worker resource stats. \
    (Where we'd like enough complexity to demonstrate that the resource utilization stats are not just aggregates over the values reported by worker-side ClusterQueues).

#### E2e Tests

Analogous as integration tests.

### Possible Follow-ups

#### Expand the cross-worker resource stats

The proposed format of cross-worker resource stats ([overview](#proposal), [API](#api-surface)) is essentially a collection of 2D arrays (indexed by flavor and resource) returned for a ClusterQueue.

Early insights suggest that we might want to develop this in various directions:

* Expose the above data broken down also per worker. \
  This would make it effectively a collection of _3D arrays_, posing a question how to arrange those to be best human-readable right away (and whether we should at all aim at that). \
   Our initial idea is to group by resource, then worker, then flavor, though for now we leave it open for feedback.

* Expose per-LocalQueue stats (like in the existing Visibility API endpoint).

* Support borrowing information, like [here](https://github.com/kubernetes-sigs/kueue/blob/6163e91e5a62befbdd421097fe0ec38b37d406e0/apis/kueue/v1beta2/clusterqueue_types.go#L359). \
  This could be less clear in the MultiKueue case, due to the "stray workloads" story. (For example: if the manager-side ClusterQueue `Q` dispatched a 4 CPU Job to ClusterQueue `Q2` on `worker1`, which happens to also host another 6 CPU Job unrelated to MultiKueue, using 8 CPU of nominal quota and borrowing 2 CPU from a Cohort on `worker2`, then what value of `Borrowed` should we report for `Q`? 2 CPU? 0 CPU? Some "proportional fraction" like 0.8 CPU?)

* Add "external usage" information scoped to all kinds of "stray workloads" (specifically: how many resources are used on the related worker ClusterQueues by workloads submitted *outside* the currently considered manager ClusterQueue).

### Graduation Criteria

This design is covered by the `MultiKueueResourceVisibility` feature gate.

Graduation criteria listed jointly for both:

* **Alpha:**
  * Features implemented behind the feature gate, disabled by default.
  * Execution of the newly introduced API calls (local & remote) is guarded by feature gates.
  * Tests are implemented.

* **Beta:**
  * The feature gate is enabled by default.
  * The feature has been successfully used in production environment.
  * [Potential extensions of cross-worker resource stats](#expand-the-cross-worker-resource-stats) have been considered, and (if desired) implemented.
  * No warning signs on performance were seen - or, if any were, [MultiKueue Cache](#multikueue-cache) is extended accordingly.
  * The "guiding principle" (see [here](#guiding-principle-for-the-implementation)) is considered fully aligned on.

* **Stable:**
  * The features (offered functionality, API surface and implementation) have stabilized, without raising concerns.
  * The feature gate is removed.

## Drawbacks

Besides introducing some risks (see [Risks and Mitigations](#risks-and-mitigations)), the proposed solution feels suboptimal mostly in the following ways:

1. The proposed cross-worker resource stats fall short of giving a full and clear picture of resource availability and usage on the workers. \
   This is mostly due to the "stray Workloads" scenario (see [here](#potential-reasons-for-decreasing-manager-quota)). Whenever a worker-side ClusterQueue `Q2` is [related](#defining-related-worker-clusterqueues) to a manager-side ClusterQueue `Q`, its whole capacity will contribute to `FlavorsQuotas` in our proposed Visibility API output, while any Workloads unrelated to `Q` will **not** contribute to `FlavorsReservation` and `FlavorsUsage`. This may lead a user to have a false impression that the quota already reserved by such "stray Workloads" is still available for use.

   This could be mitigated by emitting "external usage" stats (see [this potential follow-up](#expand-the-cross-worker-resource-stats)). For now, we postpone this, and await feedback.

## Alternatives

### Expose the cross-worker resource stats in the manager ClusterQueue Status

This may seem more natural, especially given how analogous these stats are to some [existing ClusterQueueStatus fields](https://github.com/kubernetes-sigs/kueue/blob/6163e91e5a62befbdd421097fe0ec38b37d406e0/apis/kueue/v1beta2/clusterqueue_types.go#L294-L309).

**Reasons for discarding/deferring**

* These numbers would diverge, possibly confusingly, from the [existing Status fields](https://github.com/kubernetes-sigs/kueue/blob/6163e91e5a62befbdd421097fe0ec38b37d406e0/apis/kueue/v1beta2/clusterqueue_types.go#L294-L309), as well as from the nominal manager ClusterQueue quota in its `.Spec`. \
  (The latter would often happen even if quota automation is enabled, due to `QuotaMultiplier`). \
  Exposing in a more "distant" place (Visibility Server) should help reduce that confusion.

* At this point, the format of those stats is considered unstable. \
  This makes it safer to keep it out from `ClusterQueue` API, one of central focal points of Kueue.

* Kueue Visibility API is on demand, and hence may afford slower computations. \
  Storing the same information in `ClusterQueueStatus` would require maintaining it permanently up to date; in particular, triggering reconciling ClusterQueues on all remote Workload events. (Which, in turn, would force us to migrate from [this flow of API calls](#visibility-server) to a substantially richer [MultiKueue Cache](#multikueue-cache)).
