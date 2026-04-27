# KEP-9988: MultiKueue Manager Quota Automation

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [The role of manager quotas](#the-role-of-manager-quotas)
  - [Manager vs. workers separation](#manager-vs-workers-separation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User stories](#user-stories)
    - [Story 1](#story-1)
    - [Story 2](#story-2)
    - [Story 3](#story-3)
    - [Story 4 (left unaddressed)](#story-4-left-unaddressed)
  - [Notes/Constraints/Caveats](#notesconstraintscaveats)
    - [Factors influencing desired manager quota](#factors-influencing-desired-manager-quota)
      - [Potential reasons for increasing manager quota](#potential-reasons-for-increasing-manager-quota)
      - [Potential reasons for decreasing manager quota](#potential-reasons-for-decreasing-manager-quota)
    - [Defining related worker ClusterQueues](#defining-related-worker-clusterqueues)
    - [Treatment of ResourceFlavors](#treatment-of-resourceflavors)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design details](#design-details)
  - [API surface](#api-surface)
  - [Guiding principles for the implementation](#guiding-principles-for-the-implementation)
  - [MultiKueue ClusterQueue Reconciler](#multikueue-clusterqueue-reconciler)
  - [MultiKueue Cluster Reconciler](#multikueue-cluster-reconciler)
  - [Test Plan](#test-plan)
    - [Unit Tests](#unit-tests)
    - [Integration Tests](#integration-tests)
    - [E2e Tests](#e2e-tests)
  - [Possible Follow-ups](#possible-follow-ups)
    - [Introduce a MultiKueue manager ClusterQueue quota reservation overbooking multiplier](#introduce-a-multikueue-manager-clusterqueue-quota-reservation-overbooking-multiplier)
    - [Use cached remote clients for low-volume resource kinds](#use-cached-remote-clients-for-low-volume-resource-kinds)
    - [Add metrics for multiplier usage](#add-metrics-for-multiplier-usage)
  - [Graduation Criteria](#graduation-criteria)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
  - [Support quota automation for multiple manager-side ResourceFlavors](#support-quota-automation-for-multiple-manager-side-resourceflavors)
  - [Support quota automation for no manager-side ResourceFlavors (auto-create one)](#support-quota-automation-for-no-manager-side-resourceflavors-auto-create-one)
  - [Avoid the quota automation multiplier](#avoid-the-quota-automation-multiplier)
  - [Different handling of many-to-many relationships between manager-side and worker-side ClusterQueues](#different-handling-of-many-to-many-relationships-between-manager-side-and-worker-side-clusterqueues)
  - [Make the <code>MultiKueueManagerQuotaAutomation</code> Condition message more informative](#make-the-multikueuemanagerquotaautomation-condition-message-more-informative)
  - [Expose &quot;non-multiplied&quot; total quotas in the manager ClusterQueue object](#expose-non-multiplied-total-quotas-in-the-manager-clusterqueue-object)
  - [Take Cohorts into account](#take-cohorts-into-account)
<!-- /toc -->

## Summary

This KEP outlines the design for automatic management of ClusterQueue quotas on the manager cluster in a MultiKueue setup, based on aggregation of quotas of worker ClusterQueues.

## Motivation

MultiKueue involves **two layers** of Kueue quota management. A MultiKueue workload must first reserve quota on the **manager** cluster in order to be dispatched to (one or more) workers; then it must reserve quota on a **worker** in order to execute there.

### The role of manager quotas

While _worker quotas_ are the _ultimate definition_ of the actual resource availability, the role of _manager quotas_ is subtler, yet still important.

Specifically, manager quotas **may** serve:

* As a **gate-keeping** mechanism, which allows distributing the _scheduling load_ between the manager and the workers. \
  (This may be irrelevant for small MultiKueue setups - but is essential for scalability of the largest ones).

* As a **representation** of resource availability and usage, viewable by the user without connecting to the worker clusters.

Manager quotas are **effectively optional**: when considered not useful, the Batch Admin can effectively disable them by specifying very large values ("infinite quota"). This may work well for smaller setups (and some users actually do this) - but is not recommended generally.

### Manager vs. workers separation

Currently, quota management is **fully separated** between the manager and workers, up to the point that both sides are **unaware** of each other's quotas.

While this approach offers simplicity and network savings, it also leads to problems:

1. For the manager quotas to serve their purposes discussed [above](#the-role-of-manager-quotas), they often must be **maintained in sync** with the worker quotas. \
   (The exact meaning of "in sync" may vary by use case; see [User Story 1](#story-1)). \
   Currently, MultiKueue does not help in this maintenance, making it entirely **user's responsibility**. \
   This is inconvenient and error-prone.

2. For a user to get the overview of actual resource availability and usage across the worker fleet, it is necessary to connect to all worker clusters and aggregate the information on their own.

3. For the MultiKueue controller on the manager cluster, having no awareness of workers' quotas (and their usage) **prevents more effective dispatching** choices. \
   Even though we generally don't want the manager to take over _the whole_ orchestrating work from the workers (as we intend to share it between both sides), some improvements are likely possible. For example, if a workload fits within the quota of worker A but not of worker B, there is no point in dispatching it to B before A. (At least, unless B could admit it through borrowing).

   While designing specific improvements to dispatching is **out of scope** of this KEP, all this improvements require as the **first step** that the manager *knows* workers' quotas, which this KEP proposes. \
   (Then, the second step would be to let the manager *persist* this information. That part is not proposed in this KEP, as it's not needed for its goals; however, it will be proposed in the KEP for issue [#10105](https://github.com/kubernetes-sigs/kueue/issues/10105)).

### Goals

* Make the manager cluster aware of worker's quotas.
* Enable automated maintenance of manager quotas to keep it in sync with total worker quotas.
* Make the above automation configurable enough to fit, at least roughly, various use cases.

### Non-Goals

* Fine-tune manager quota automation to achieve the optimal scheduling throughput.
* Expose in the manager cluster a view of resource availability and usage across all connected worker clusters. (This is tracked in [#10105](https://github.com/kubernetes-sigs/kueue/issues/10105)).
* Improve MultiKueue dispatching.

## Proposal

We will add an **optional** feature of **auto-aggregating quotas** from the workers to the manager.

**When enabled** for a manager ClusterQueue `Q`, this feature will set its quota for a resource `R` according to the following recipe:

1. Determine the set of [related worker ClusterQueues](#defining-related-worker-clusterqueues), i.e. those which can serve the dispatched remotes of the workloads submitted to `Q`.

1. Compute the **sum** of quotas for `R` in **all** _related_ worker ClusterQueues, across **all** ResourceFlavors.

1. Multiply the above sum by a user-configurable **multiplier**. \
   (This is intended to address several [reasons for adjusting the manager quota](#factors-influencing-desired-manager-quota), and thus cover most cases of [User Story 1](#story-1). Also, specifying a very large multiplier is technically a way to cover [User Story 2](#story-2)).

1. Last, add the requests of all (manager-side) Workloads which are currently Admitted in `Q` but their remote cluster is currently unreachable. \
   (This is to avoid exaggerated reduction of quotas for `Q` in case when a worker cluster disconnects temporarily, and the workloads running on it still hold quota on `Q`, until `workerLostTimeout` elapses).

Enabling this feature will be controlled by an API field (not just by a feature gate), as we want to permanently retain a way to opt out (see [User Story 3](#story-3)).

Enabling this feature will **require** that the manager ClusterQueue `Q` has **exactly one ResourceFlavor**. (See [Treatment of ResourceFlavors](#treatment-of-resourceflavors) for rationale).

### User stories

#### Story 1

As a Batch Admin, I want to maintain manager quotas  **reasonably synced** to total woker quotas, to achieve a desired gate-keeping behavior on the manager.

Here, the meaning of "reasonably synced" can vary by use case:

* The **baseline approach** (most intuitive, and currently recommended in MultiKueue documentation) is to keep the manager quota **equal to the sum** of worker quotas.

* Yet, there are several reasons for which the user may want to keep the manager quota **higher or lower** than the abovementioned sum. \
  See [Factors influencing desired manager quota](#factors-influencing-desired-manager-quota) for more details.

#### Story 2

As a Batch Admin, I want to keep MultiKueue manager quotas "infinite". 

In my use case, the potential gains from meaningful ("finite") manager quotas are outweighed by the effort of maintaining them in "appropriate sync", or by the cost of missed opportunities to schedule (in case we haven't sufficiently [increased the manager quota](#potential-reasons-for-increasing-manager-quota)).

#### Story 3

As a Batch Admin, I want to keep MultiKueue manager quotas meaningful ("finite") but **not managed** by the feature proposed in this KEP.

This could be because:

* I need multiple manager ResourceFlavors (due to e.g. using a special dispatcher or a heterogenous topology across the workers; see [Treatment of ResourceFlavors](#treatment-of-resourceflavors) for details).

* I need to fine-tune the manager gate-keeping functionality in a way which cannot be easily expressed as "total workers capacity with a multiplier".

* I want to control the manager quotas manually (e.g. to make them more stable, or less confusing).

#### Story 4 (left unaddressed)

_This Story is left unaddressed in this KEP - however, we keep it mentioned here to enhance the discussion._ \
_Addressing this Story is a goal of [#10105](https://github.com/kubernetes-sigs/kueue/issues/10105)._

As a MultiKueue user (Workload Owner or Batch Admin), I want to see a summary of resource availability and usage of my whole MultiKueue setup, surfaced by the manager cluster (which I'm treating as my single control plane).

In particular, I may want to see this summary broken down per worker-side ResourceFlavors.

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
   2. divergences between the "official" quotas on the manager and worker sides, caused by the quota automation multiplier.

2. Introducing management of ClusterQueue quotas which some users may consider undesired (see [User Story 3](#story-3)).

3. Slowing down MultiKueue by adding more reconcilers and computations.

For these, we propose the following mitigations:

* Risks 1.i and 1.ii are mitigated by a dedicated ClusterQueue condition, explaining the automation process as well as the multiplier.

* Risks 1 and 2 are mitigated by introducing feature gates combined with the opt-in/opt-out API. \
  This also mitigates Risk 3, as long as we pay attention to skip computations which are not necessary per the ClusterQueue configuration.

* Risk 3 is considered low (see our [guiding principles](#guiding-principles-for-the-implementation)). If needed, it can be substantially mitigated by caching the relevant information. \
  While this would require introducing a new MultiKueue-specific cache, such a cache seems anyway needed for other tasks approaching, including [#10105](https://github.com/kubernetes-sigs/kueue/issues/10105), [#10614](https://github.com/kubernetes-sigs/kueue/issues/10614) and dispatching improvements (see item 3 [here](#manager-vs-workers-separation)). Hence, potential mitigations to Risk 3 will likely take the form of re-using that cache, possibly combined with extending its content.

## Design details

### API surface

The configuration for this feature will be added to the existing `MultiKueueConfig` struct. (For context, `MultiKueueConfig` objects are referenced from `AdmissionCheckSpec` as [`Parameters`](https://github.com/kubernetes-sigs/kueue/blob/24209c461b72fd6519581aca2234fb8f05dd1ce7/apis/kueue/v1beta2/admissioncheck_types.go#L60); this will allow controlling the feature independently for each ClusterQueue in the manager cluster).

```go
type MultiKueueConfigSpec struct {
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

The status of the feature will be communicated by a new Condition added to ClusterQueueStatus. The Condition will be present in all ClusterQueues enrolled in MultiKueue.

When quota automation is requested and supported, the condition will look like this:

```yaml
type: MultiKueueManagerQuotaAutomation
status: True
reason: QuotaAutomated
message: ClusterQueue quota is automatically managed based on MultiKueue workers. Applying total worker capacity with a multiplier specified in MultiKueueConfig.QuotaAutomation.QuotaMultiplier.
lastTransitionTime: 2026-01-01T00:00:00Z
```

where `lastTransitionTime` only tracks changes affecting the Condition `status` and `message` (i.e. feature enablement, potential errors) but **not** subsequent quota updates. (See [this section](#make-the-multikueuemanagerquotaautomation-condition-message-more-informative) in Alternatives).

When quota automation is not requested, the condition will look like this:

```yaml
type: MultiKueueManagerQuotaAutomation
status: False
reason: NotRequested
message: MultiKueue manager quota automation has not been requested.
lastTransitionTime: 2026-01-01T00:00:00Z
```

Enabling quota automation will be subject to a **constraint** that the manager ClusterQueue has exactly one ResourceFlavor. This constraint will **not** be enforced via validation (e.g. a webhook) but by Kueue controllers code (similarly to [analogous existing AdmissionCheck-related constraints](https://github.com/kubernetes-sigs/kueue/blob/6163e91e5a62befbdd421097fe0ec38b37d406e0/pkg/cache/scheduler/clusterqueue.go#L284-L307), evaluated in the core ClusterQueue controller). A violation of this new constraint will **not** make the ClusterQueue Inactive (as is the case for the existing analogous constraints). In this case of a violation, the queue itself will be still operational, just not supportable by the quota automation feature. This will be communicated as an error of that feature, by its status Condition:

```yaml
type: MultiKueueManagerQuotaAutomation
status: False
reason: MultipleFlavors
message: MultiKueue manager quota automation does not support ClusterQueues with multiple ResourceFlavors.
lastTransitionTime: 2026-01-01T00:00:00Z
```

### Guiding principles for the implementation

The implementation details discussed below are based on the following guiding principles:

1. Generously tolerate API calls for resources which typically don't exist in large numbers.

   * This includes: LocalQueues, ClusterQueues, AdmissionChecks, and MultiKueueConfigs.

   * This includes `.Get()`, `.List()` and `.Watch()` calls, even on remote clients.

   If this seems overly simplistic, keep in mind that:

   * We only add new API calls in infrequently running procedures (and thus have much less need for sophisticated caching than, say, Kueue scheduler code). \
     This **may need revisiting** in a (hypothetical) scenario when some automation external to Kueue frequently updates e.g. worker ClusterQueue quotas.

   * This feels in line with the current MultiKueue approach, in which we e.g. tolerate remote `.Get()` calls [here](https://github.com/kubernetes-sigs/kueue/blob/161a2abc9e484b9fe0d6b3025c0bcac4fb8ef3a4/pkg/controller/admissionchecks/multikueue/workload.go#L314) inside the MultiKueue Workload Reconciler, even if they may be redundant (see [#10629](https://github.com/kubernetes-sigs/kueue/issues/10629)).

   * If this approach ever turns too relaxed, we have numerous ways to win more efficiency with caching.

2. Generally tolerate local API calls, as they use cached clients.

Still, to keep up with Kueue code standards, we will:

3. When possible, make `.List()` calls filtered (by API fields), and (for local calls) make sure that these filters are covered by client indexes.

4. When feasible, pre-guard reconcile routines and `.Watches()` calls with predicates, ensuring that a reconcile is not triggered by an update which only changed irrelevant fields. \
   (Such predicates will be clearly indicated in the recipes below).

5. When feasible, replace multiple remote `.Get()`s with a single `.List()` call.

   Even if this means increased amount of data transferred, the benefit is reduction of actual API QPS, as well as combining good latency with code simplicity (no need to parallelize for multiple `.Get()` calls).

### MultiKueue ClusterQueue Reconciler

Besides the [existing MultiKueue reconcilers](https://github.com/kubernetes-sigs/kueue/blob/161a2abc9e484b9fe0d6b3025c0bcac4fb8ef3a4/pkg/controller/admissionchecks/multikueue/controllers.go#L151-L165), we'll add a new one for ClusterQueue objects (mostly on the manager side). Its operation will be enabled by the `MultiKueueManagerQuotaAutomation` feature gate.

Reconciling a manager ClusterQueue `Q` will proceed as follows:

1. Fetch `Q` from the API client.

2. Find the MultiKueue AdmissionCheck for `Q` (using an unfiltered local API `.List()` call). \
   Then, fetch the MultiKueueConfig for `Q` from the API client. \
   If any of this fails, clear the `MultiKueueManagerQuotaAutomation` Condition (if present) and exit.

3. Determine whether quota automation has been requested and can be supported for `Q` (based on the MultiKueueConfig, the feature gate, and the number of ResourceFlavors). \
   If not, set the `MultiKueueManagerQuotaAutomation` Condition accordingly (to `False`) and exit.

5. Find all manager LocalQueues connected to `Q` (using a filtered local API `.List()` call).

6. Fetch all worker LocalQueues of those names (using unfiltered remote API `.List()` calls, one per worker).

7. Fetch all worker ClusterQueues connected to those LocalQueues (using unfilterd remote API `.List()` calls, at most one per worker).

8. Check if there are currently disconnected remote clients (using their [`.connecting`](https://github.com/kubernetes-sigs/kueue/blob/76676f599fc87883156c81516ea91b9b65cc70fd/pkg/controller/admissionchecks/multikueue/multikueuecluster.go#L102) field).

9. For every disconnected client for a remote cluster `R`, fetch all manager Workloads which are Admitted on `Q`, and last seen running on `R` \
   (using a filtered local API `.List()` call, based on `.Status.Admission.ClusterQueue` as well as `.Status.ClusterName`).

10. Determine the new quota values for `Q` (using the recipe described [here](#proposal), with `QuotaMultiplier` used as the multiplier).

11. If `MultiKueueManagerQuotaAutomation` is not `True`, set it to `True`.

12. Update quotas for `Q` if the desired values differ from the current ones.

(See the "Takeaway" part [here](#make-the-multikueuemanagerquotaautomation-condition-message-more-informative) for the rationale for the ordering of the last 2 steps).

This reconciler will be triggered in the following ways:

1. For (manager) ClusterQueue events (creations, deletions, changes of `.Spec.ResourceGroups` or of `.Spec.AdmissionChecksStrategy`).

2. For (manager) LocalQueue events (creations, deletions), where the update handler function will:
   1. Trigger reconcile for the corresponding ClusterQueue.

3. For (manager) AdmissionCheck events (creations, deletions, changes of "does `.Spec.ControllerName` indicate MultiKueue?" or, if it does, of `.Spec.Parameters`), where the update handler function will:
   1. Find all manager ClusterQueues using it (using a filtered API `.List` call).
   2. Trigger reconcile for all those ClusterQueues.

4. For (manager) MultiKueueConfig events (creations, deletions, changes of `.Spec.QuotaAutomation`), where the update handler function will:
   1. Find all manager ClusterQueues using it (using 2 levels of filtered API `.List` calls, similarly as [here](https://github.com/kubernetes-sigs/kueue/blob/25538a4ea2979d975d75792c1f9a7124a0475c4a/pkg/controller/admissionchecks/multikueue/workload.go#L880-L890)).
   2. Trigger reconcile for all those ClusterQueues.

5. For remote objects events, propagated from the [MultiKueue Cluster Reconciler](#multikueue-cluster-reconciler) (see there for details).

Cases 2-4 will be implemented by additional `.Watches()` calls on the reconciler (like e.g. [here](https://github.com/kubernetes-sigs/kueue/blob/25538a4ea2979d975d75792c1f9a7124a0475c4a/pkg/controller/admissionchecks/multikueue/workload.go#L915)). Case 5 will be implemented by watching a custom channel (like [here](https://github.com/kubernetes-sigs/kueue/blob/25538a4ea2979d975d75792c1f9a7124a0475c4a/pkg/controller/admissionchecks/multikueue/workload.go#L914)).

### MultiKueue Cluster Reconciler

In `multikueuecluster.go`, where the `startWatcher()` method calls `.Watch()` on the remote clusters [for Workloads](https://github.com/kubernetes-sigs/kueue/blob/25538a4ea2979d975d75792c1f9a7124a0475c4a/pkg/controller/admissionchecks/multikueue/multikueuecluster.go#L178) as well as [for the supported job types](https://github.com/kubernetes-sigs/kueue/blob/25538a4ea2979d975d75792c1f9a7124a0475c4a/pkg/controller/admissionchecks/multikueue/multikueuecluster.go#L190), we'll add additional `.Watch()` calls:

1. For (remote) LocalQueue events (creations, deletions), where the update handler function will:
   1. Fetch the corresponding manager LocalQueue from the API client, and check its ClusterQueue.
   2. Trigger the [MultiKueue ClusterQueue Reconciler](#multikueue-clusterqueue-reconciler) for that ClusterQueue.

2. For (remote) ClusterQueue events (creations, deletions, changes of `.Spec.ResourceGroups`), where the update handler function will, for a ClusterQueue `Q` on worker `W`:
   1. Find all LocalQueues on `W` pointing to `Q` (using a filtered remote `.List` API call).
   2. Find all manager ClusterQueues connected with manager LocalQueues of the same names (using a single unfiltered local `.List` API call).
   3. Trigger the [MultiKueue ClusterQueue Reconciler](#multikueue-clusterqueue-reconciler) for all those ClusterQueues.

### Test Plan

[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.

#### Unit Tests

* For the [MultiKueue ClusterQueue Reconciler](#multikueue-clusterqueue-reconciler): 

  * Test the reconcile logic with all exit paths (similarly to e.g. [this suite](https://github.com/kubernetes-sigs/kueue/blob/25538a4ea2979d975d75792c1f9a7124a0475c4a/pkg/controller/admissionchecks/multikueue/workload_test.go#L60)).

  * Test all additional triggering paths from local watchers in the [MultiKueue ClusterQueue Reconciler](#multikueue-clusterqueue-reconciler) (similarly to e.g. [this suite](https://github.com/kubernetes-sigs/kueue/blob/25538a4ea2979d975d75792c1f9a7124a0475c4a/pkg/controller/admissionchecks/multikueue/workload_test.go#L2266))

  * Test the _non-triggering_ paths, e.g. a ClusterQueue event touching only irrelevant fields.

* For the [MultiKueue Cluster Reconciler](#multikueue-cluster-reconciler):

  * Test triggering vs. _non-triggering_ paths (by just verifying the content of the event channel, analogous to [`wlUpdateCh`](https://github.com/kubernetes-sigs/kueue/blob/25538a4ea2979d975d75792c1f9a7124a0475c4a/pkg/controller/admissionchecks/multikueue/multikueuecluster.go#L95)).

#### Integration Tests

* For the integration between [MultiKueue Cluster Reconciler](#multikueue-cluster-reconciler) and [MultiKueue ClusterQueue Reconciler](#multikueue-clusterqueue-reconciler):

  * Test triggering the latter by changes in remote LocalQueues and ClusterQueues. \
    (This could be also unit-tested, by a well-crafted watch interceptor similarly as [here](https://github.com/kubernetes-sigs/kueue/blob/25538a4ea2979d975d75792c1f9a7124a0475c4a/pkg/controller/admissionchecks/multikueue/multikueuecluster_test.go#L69-L70) - but an integration test feels a much cleaner way).

#### E2e Tests

* Test the scenario of a ClusterQueue having quota automation disabled -> then enabled (and reacting to some change) -> then disabled. Verify that:

  * Manager quota is adjusted, as intended, upon:

    - adding a new worker in MultiKueueConfig
    - changing a remote ClusterQueueQuota
    - (if we decide to support that) a worker cluster becoming unreachable

  * New jobs can be admitted at all 3 phases of this scenario (quota automation disabled, then enabled, then disabled again).

* Verify that manager quota auto-adjustments (in either direction: increases and decreases) promptly trigger the expected re-assignments of Workloads.

### Possible Follow-ups

#### Introduce a MultiKueue manager ClusterQueue quota reservation overbooking multiplier

That is, an analogue of the proposed quota mutliplier - but differing in that:

* it would **not affect** the [nominal ClusterQueue quota](https://github.com/kubernetes-sigs/kueue/blob/6163e91e5a62befbdd421097fe0ec38b37d406e0/apis/kueue/v1beta2/clusterqueue_types.go#L250) value,
* it would **not increase** the "total volume" of workloads which can be **admitted** by the manager,
* it would **only** affect the "total volume" of workloads can **reserve quota** on the manager-side ClusterQueue (and hence, get dispatched to the workers).

For example, if there are 2 workers, each with 10 CPU capacity, the `QuotaMultiplier` (as proposed in this KEP) is 0.8 (to leave 20% of worker capacity for "stray workloads"), and the Quota Reservation Overbooking Multiplier (as discussed here) is 3.0, then the manager-side ClusterQueue:

* will have nominal quota set to (10 + 10) * 0.8 = 16,
* will admit workloads requesting at most 16 CPU in total,
* will reserve quota (and hence - dispatch to workers) workloads requesting up to 16 * 3 = 48 CPU in total.

As weird as it seems, this opens a way to "reconcile" various, otherwise clashing, design factors for this KEP; specifically:

* seamless dispatching vs. per-team allocations (see [Drawback #3](#drawbacks)),
* (if `QuotaMultiplier` from this KEP is `1.0`) seamless dispatching vs. intuitive quotas (see [Drawback #2](#drawbacks)).

Yet, this comes with some **disadvantages**:

* Adding another form of user confusion ("why this ClusterQueue reserved quota above its nominal limit, despite using no borrowing?").

* Adding complexity to Kueue behavior.
  * In particular, we'd need to carefully plan how to enforce the "don't admit over the quota" constraint. \
    One approach would be to make the workers ask the manager for a green light before admitting the workload (in some resemblance with [KEP-8303](https://github.com/kubernetes-sigs/kueue/blob/main/keps/8303-multikueue-orchestrated-preemption/README.md)).

* Adding conceptual complexity. (As demonstrated by the length of the name of this section).

* Blurring separation of concerns, by introducing new ways of MultiKueue awareness to core Kueue code.

Therefore, this idea is **shelved** for now. We may consider it later, depending on the feedback.

#### Use cached remote clients for low-volume resource kinds

Currently, the MultiKueue controllers use cached K8s clients for local (manager-side) resources but uncached clients for remote resources. Just enabling cache for remote clients does feel risky, as it could use much memory for Workloads and supported job kinds (which can exist in large numbers).

However, the remote API calls introduced in this KEP involve only "memory-light" resource kinds (LocalQueues and ClusterQueues), for which caching would pose no risk, while avoiding duplication in some cases. \
(In case of a [many-to-many relation](#defining-related-worker-clusterqueues) between manager-side and worker-side ClusterQueues, a change of worker-side `Q'` would trigger reconciles of multiple manager-side ClusterQueues, each of which would make the same remote API `.List()` calls, almost simultaneously).

Then, it seems a promising optimization to cache remote clients _only for selected resource kinds_. \
This could be implemented by equipping the internal [`remoteClient` structure](https://github.com/kubernetes-sigs/kueue/blob/76676f599fc87883156c81516ea91b9b65cc70fd/pkg/controller/admissionchecks/multikueue/multikueuecluster.go#L91-L109) with **two** API clients, the old uncached one (to be used for Workloads etc.), and a new cached one (to be used for LocalQueues etc.). To ensure proper choices among them, both could be wrapped with tiny facades throwing on calls for resource kinds outside an allowlist.

We **postpone** this as doing it right may still involve some complexity. For example, it's not clear how to avoid duplicating API watches (see item 3 in the description of [#10629](http://github.com/kubernetes-sigs/kueue/issues/10629)).

#### Add metrics for multiplier usage

Such metrics could help users determine an optimal value of the multiplier (see [Drawback #1](#drawbacks)).

Example ideas:

* "Multiplier usage": Whenever the manager _reserves quota_ for a workload, emit the quotient between the total quota reserved on the manager side and the total quota on the workers side.

* "Effective multiplier usage": Whenever the manager _admits_ a workload, emit the "multiplier usage" value from the time when that workload obtained _quota reserved_ on the manager. \
  (Rationale: this measure is "effective" in that, if the manager ClusterQueue were configured with a multiplier below the emitted metric value, it would actually have prevented admitting that workload at that time).

### Graduation Criteria

* **Alpha:**
  * The feature implemented behind the `MultiKueueManagerQuotaAutomation` feature gate, disabled by default.
  * Execution of the newly introduced API calls (local & remote) is guarded by feature gates.
  * Tests are implemented.

* **Beta:**
  * The feature gate is enabled by default.
  * The feature has been succesfully used in production environment.
  * No warning signs on performance were seen (in particular, no news of any external automation of worker ClusterQueue quotas; see [here](#guiding-principles-for-the-implementation)) - or, if any were, they have been addressed.
  * The "guiding principles" (see [here](#guiding-principles-for-the-implementation)) are considered fully aligned on.
  * Issue [#10428](https://github.com/kubernetes-sigs/kueue/issues/10428) (see [Drawback #4](#drawbacks)) is diagnosed, its impact on quota automation is understood and any mitigations deemed necessary are implemented.

* **Stable:**
  * The feature (offered functionality, API surface and implementation) has stabilized, without raising concerns.
  * The feature gate is removed.

## Drawbacks

Besides introducing some risks (see [Risks and Mitigations](#risks-and-mitigations)), the proposed solution feels suboptimal mostly in the following ways:

1. The `QuotaMultiplier` value feels very arbitrary, and it may be difficult to determine its optimal value (even for a specific use case). Two approaches coming to mind are:

   1. An experienced Batch Admin can try out various values and judge the effects by observing overall system efficiency. (That, of course, assumes no other factors substantially interfering in such experimenting).

   1. A more data-driven approach could be based on metrics, considered in [this possible follow-up](#add-metrics-for-multiplier-usage).

1. With the `QuotaMultiplier`, the manager-side quota can effectively tell "what amount of workloads we want to dispatch" but may get _very divergent_ from "what amount of workloads we want to execute".

   This is largely counter-intuitive, as the latter is the more "fundamental" meaning of a ClusterQueue quota, likely the most intuitive one for many users.

   This drawback is the main motivation for the possible follow-up idea of [quota reservation overbooking](#introduce-a-multikueue-manager-clusterqueue-quota-reservation-overbooking-multiplier).

1. While the `QuotaMultiplier` allows addressing individual [reasons for increasing manager quota](#potential-reasons-for-increasing-manager-quota) or [for decreasing it](#potential-reasons-for-decreasing-manager-quota), some combinations of those reasons remain not supportable in the most efficient way.

   For example, in the "Per-team quotas on the manager" scenario discussed [here](#potential-reasons-for-decreasing-manager-quota), the Batch Admin will be able to configure manager-side ClusterQuotas for teams A / B / C to have 50% / 30% / 20% of the total workers quota respectively, but then each of the teams will be vulnerable to scheduling delays resulting from quota fragmentation, as explained in the "Divergence of quota reservation" scenario [here](#potential-reasons-for-increasing-manager-quota). \
   Conversely, an attempt to increase the quotas for each of the teams would allow individual teams to consume more quotas than intended. For example, if the Batch Admin applied our default multiplier (3.0) on top of the initial per-team assignments discussed above, team B would be able to consume 90% of the total worker capacity.

   Just like Drawback #2, this may be addressable by the [quota reservation overbooking](#introduce-a-multikueue-manager-clusterqueue-quota-reservation-overbooking-multiplier) idea, if used _together_ with the nominal quota multiplier as in the current proposal. Yet, the overall complexity of such solution feels unacceptable for now. We may consider it later depending on the feedback.

1. The proposed solution fails to react to some types of detectable worker connection issues. \
   See [#10428](https://github.com/kubernetes-sigs/kueue/issues/10428) which describes this problem in more detail, and speculates that it may already affect existing MultiKueue reconcilers (e.g. the one for Workloads).

   If this turns out problematic, it should be fixable by caching the "manager ClusterQueue <-> MultiKueueCluster" relationship.

1. We have fully ignored the topic of Cohorts, shared quota and quota borrowing. This is deliberate, and intended to retain any reasonable simplicity of the feature, at least in the Alpha stage. (See [Take Cohorts into account](#take-cohorts-into-account) in the Alternatives section).

## Alternatives

### Support quota automation for multiple manager-side ResourceFlavors

The proposed constraint that quota automation requires a single ResourceFlavor on the manager side may feel inconvenient; for example, it's an obstacle (independent from the quota multiplier) for supporting [User Story 4](#story-4-left-unaddressed) in its full scope ("broken down per worker-side ResourceFlavors") via the manager ClusterQueue quota, which would be the most intuitive place.

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
   (This seems appealing at the first glance, as it would allow addressing [User Story 4](#story-4-left-unaddressed) via the ClusterQueue quotas, which is the most intuitive place - even if without the breakdown per worker-side flavor).

3. Use the "quota reservation overbooking multiplier" (see [this possible follow-up idea](#introduce-a-multikueue-manager-clusterqueue-quota-reservation-overbooking-multiplier)) _instead_ of the multiplier in the current proposal.

**Reasons for discarding/deferring**

* For #1, our assessment is that the value of quota automation justifies introducing it, especially given our mitigations to make the multiplier possibly understandable, and the permanent option to opt-out.

* For #2, we choose to introduce the multiplier, given the [numerous potential reasons](#factors-influencing-desired-manager-quota) for using it.

  The strongest of these reasons is the "Divergence of quota reservation" case which - let us stress this - applies essentially to every user of MultiKueue. This means that implementing quota automation without the multiplier would be a _regression_ for _most_ users. This feels unacceptable.

  Theoretically, the "Divergence of quota reservation" case could be addressed _differently_ - by changing the general MultiKueue flow of handling workloads to enforce alignment of `QuotaReserved` condition between the manager and workers, for example:

  1. Detach dispatching to workloads from the `QuotaReserved` condition on the manager.
  2. Remove `QuotaReserved` on the manager if a workload failed to get quota on the workers.
  3. Only dispatch to workers if it's known that some of them will reserve quota.

  However, all these approaches mean substantial changes which seem:
  
  - risky for overall MultiKueue performance,
  - likely infeasible, 
  - even if feasible, complex enough to be out of the scope here.

  In particular, while (iii) is related to the goal of "more effective dispatching" mentioned in [Motivation](#motivation), it takes that goal to such a high level that it could easily lead to putting too much scheduling work on the manager side, thus slowing down MultiKueue in largest cases.

* For #3, see the disadvantages discussed [here](#introduce-a-multikueue-manager-clusterqueue-quota-reservation-overbooking-multiplier). \
  Also, using that multiplier _instead_ of `QuotaMultiplier` from the main proposal brings two additional subtle disadvantages:

  * Removing a way to address the "Borrowing on a worker" case discussed [here](#potential-reasons-for-increasing-manager-quota) - because that case is caused by divergence of _admission_, not just _quota reservation_.

  * Making the quota automation feature dependent on the `MultiKueueWaitForWorkloadAdmitted` feature gate (as a fix of [#8585](https://github.com/kubernetes-sigs/kueue/issues/8585)).

### Different handling of many-to-many relationships between manager-side and worker-side ClusterQueues

The "MultiKueue relationship" between manager-side and worker-side ClusterQueues may be in general [many-to-many](#defining-related-worker-clusterqueues), and our proposal of handling it (for a manager-side ClusterQueue `Q`, rely on the _sum_ of quotas of _all_ related worker ClusterQueues) may lead to _distorting the totals_ between the manager and the workers, which may be perceived as misleading.

For a full context, such a distortion will likely happen anyway, due to the quota multipler. However, multiplier-based distortion may be mitigable (e.g. by setting the multiplier to 1, or potentially in the future by switching to the [overbooking multiplier](#introduce-a-multikueue-manager-clusterqueue-quota-reservation-overbooking-multiplier) follow-up), and in those cases our treatment of many-to-many relationships would become a major factor for the distortion.

We've considered 2 ways to work around that:

1. Restrict quota automation support to cases when LocalQueue -> ClusterQueue bindings are compatible between the manager and all workers, so that the considered relationship becomes one-to-one. \
   (In all the other cases, we could refuse to automate, and communicate it via the `MultiKueueManagerQuotaAutomation` Condition).

2. For each worker-side ClusterQueue `Q'` [related](#defining-related-worker-clusterqueues) to multiple manager-side ClusterQueues, split the quota `Q'` into parts "attributed" to each of them. \
   (Likely in a way honoring the Admission Fair Sharing weights on the worker clusters).

**Reasons for discarding/deferring**

* Option 1 leaves reasonable use cases without support, e.g. the per-team manager ClusterQueue setup described [here](#potential-reasons-for-decreasing-manager-quota).

* Option 2 may be more accurate in describing the _admitting potential_ of manager ClusterQueues in case of _omnipresent scarcity_ of resources. \
  However, it fails to describe the _upper bound_ of what can be admitted to a specific manager ClusterQueue, which feels incorrect, and potentially leading to scheduling delays.

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

### Expose "non-multiplied" total quotas in the manager ClusterQueue object

This may feel natural, and would come closer to addressing [User Story 4](#story-4-left-unaddressed).

**Reasons for discarding/deferring**

* There is no clear place for such a field:

  * Choosing `.Spec` feels semantically improper as this field would exist only for visibility, without influencing the resource behavior.

  * Choosing `.Status` would bring the issue of non-atomicity with `.Spec.ResourceGroups`, with all its consequences (see discussion [here](#make-the-multikueuemanagerquotaautomation-condition-message-more-informative)).

* This may be perceived as cluttering the ClusterQueue API which is already mature. \
  In particular, it's not obvious how to name the new field for best clarity.

### Take Cohorts into account

When a ClusterQueue belongs to a Cohort, it may reserve quota above its nominal capacity, by using _shared Cohort quota_ (Cohort's `.nominalQuota`) or by _borrowing_.

If this happens on the worker side, it clearly influences the actual capacity of a ClusterQueue, and thus could be _somehow_ taken into account in the calculation of "total quota".

**Reasons for discarding/deferring**

There are many difficulties in defining the desired behavior. Example questions:

* What if the whole picture of "LocalQueue -> ClusterQueue -> Cohort -> ancestor Cohort" bindings is fully compatible between the manager and all workers? \
  Should we then aggregate `nominalQuota` from worker-side Cohorts to the corresponding manager-side Cohorts?

  * If so, how do we handle setups which are "incompatible", or "partially compatible"? \
    Do we want to support quota automation for _some resources_ in a case of _partial compatibility_?

  * Even assuming "full compatibility" in the above sense, the result could be considered confusing, due to possible per-workload inconsistencies (e.g. a ClusterQueue could borrow from one sibling on the manager but from another one on a worker, or it could use shared Cohort quota on the worker but not on the manager). \
    These inconsistencies would be likely amplified by using a non-trivial quota multiplier. \
    Do we accept this?

* What if Cohorts exist only on the workers, or are "badly incompatible" between the workers? \
  Are we fine with increasing `nominalQuota` of manager-side ClusterQueues to reflect the shared Cohort quota on the workers?

  * If so, how should we "attribute" the additional quota among different ClusterQueues? \
    For example:

    * Suppose there's a manager and 2 workers, which all have `lq1`, `lq2`, `lq3`, pointing to ClusterQueues `cq1`, `cq2`, `cq3` respectively. \
      Also suppose both workers have a Cohort `coh1`, to which all those ClusterQueues belong. \
      `coh1` declares a shared `nominalQuota` of 30 CPU. \
      Shall this (before applying potential multiplier(s)) count as:

      - extra 30 CPU for each of the manager-side ClusterQueues (to reflect the actual upper bound of what can run on them)?
      - extra 10 CPU for each of them (so that "manager's total matches all workers' total")?
      - extra CPU values summing up to 30 CPUs but proportional to the fair sharing weights?

      (While the later options may feel more appealing, keep in mind that the goal of "matching totals" is generally cumbersome to achieve even without supporting Cohorts; see [here](#different-handling-of-many-to-many-relationships-between-manager-side-and-worker-side-clusterqueues)).

    * Now suppose `lq3` and `cq3` don't exist on the manager; should this influence how the quotas for `cq1` and `cq2` are determined?

    * How should we handle [many-to-many relationships](#defining-related-worker-clusterqueues) between manager-side and worker-side ClusterQueues? \
      (Warning: this is [not obvious even when disregarding Cohorts](#different-handling-of-many-to-many-relationships-between-manager-side-and-worker-side-clusterqueues), and adding Cohorts to the picture adds to the complexity of this question).

    * How should we handle incompatible Cohort trees across workers? \
      (This might have a simpler answer: just do the "attribution", whichever way we define it, separately on each worker, and add up the results. Still, it deserves some consideration if the result is acceptable. One alternative would be to refuse to handle such cases).

* Should `borrowingLimit` and `lendingLimit` be aggregated from worker-side ClusterQueues to the [related](#defining-related-worker-clusterqueues) manager-side ClusterQueues?

  * If yes, what should we do if that relationship is many-to-many?

* How should we manage quota automation settings (enablement, multiplier) for Cohorts if they may diverge for the underlying ClusterQueues?
