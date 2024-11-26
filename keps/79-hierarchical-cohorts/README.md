# KEP-79: Hierarchical Cohorts

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories (Optional)](#user-stories-optional)
    - [Story 1](#story-1)
    - [Story 2](#story-2)
  - [Notes](#notes)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [Test Plan](#test-plan)
      - [Prerequisite testing updates](#prerequisite-testing-updates)
    - [Unit Tests](#unit-tests)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
<!-- /toc -->

## Summary

Introduce Cohort top-level object to allow setting multi-level quota 
hierarchy, with advanced borrowing, lending mechanisms.

## Motivation

The current 2-level hierarchy (ClusterQueues and Cohorts) is not expressive
enough to handle complex use cases of large organizations with tree-like
team and quota/budget structures.

### Goals

* Create a multi-level hierarchy for advanced quota management. 
* Be compatible with the existing ClusterQueue API and mechanics.
* Allow setting constraints about borrowing and lending at all levels.
* Provide quota for groups of Queues.

### Non-Goals

* Change the existing API and mechanics in a not backward-compatible way.
* Introduce an alternative API to ClusterQueue.
* Introduce new ways of fair sharing, like ratio-based sharing (at least 
not in this KEP).
* Introduce additional preemption models (this will be in a separate KEP).

## Proposal

Introduce a new object called Cohort with the similar quota provisioning mechanism
as ClusterQueue. Cohorts additionally may specify its parent, another Cohort,
forming together a tree-like organization structure. ClusterQueues will still be able to specify
the Cohort they belong to. The Cohort mentioned by ClusterQueue doesn't require
an actual object to be present. If such is not provided, it is understood 
that the Cohort doesn't provide any quota, has no parent, doesn't belong to any bigger
structure or has any non-default settings.

The difference between ClusterQueue and Cohort will be that:

* ClusterQueues are leaves in the organization tree, Cohorts are inner nodes. 
* Cohort doesn't accept any workloads.
* Nominal quota provided at the Cohort level is to be shared with the entire organization and doesn't 
  have an owning ClusterQueue.
* Borrowing limit specified at the Cohort level means that the entire subtree cannot borrow more 
  from the rest of the organization tree than the given value.
* Lending limit specified at the Cohort levels means that the rest of the organization tree
  cannot borrow more from the subtree than the given value.

Preemptions and resource reclamation will happen among the whole cohort structure,
in the similar fashion as they are executed now.

### User Stories (Optional)

#### Story 1

I have two multi-team organizations in the company. One that does research and one that runs production
workloads. Both are given some quota that is further distributed among the subteams. I want to grant 
the production workloads the ability to borrow research quota if needed, but not the other way round.

With this proposal, research org's top Cohort will simply set borrowingLimit to 0. Alternatively, production
org's top Cohort can set lendingLimit to 0. BorrowingLimits inside production org's ClusterQueues should
be generous enough to allow borrowing from the research org.

#### Story 2

I have a couple organizations that have dedicated resources. The organizations should not borrow 
from each other, however I want to have an additional "special" queue, with low priority jobs, that can 
borrow unused capacity from any of the organizations.

With this proposal, the cohorts for organizations will set borrowingLimit to 0. Top level Cohort will 
contain all of these Cohorts, plus the "special" ClusterQueue, with borrowingLimit set to infinity. 

### Notes

As Cohorts do not have finalizers, Cohort's deletion will be processed
immediately. If this Cohort is referenced by any Cohorts or ClusterQueues, the
deleted Cohort will still exist in Kueue, but will no longer provide any
resources of its own nor have a parent Cohort.

This immediate deletion could result the tree suddenly losing capacity, which,
depending on configuration, may result in preemptions. Fixing this is
not as simple as just adding finalizers - non-deletions (nodes changing parents
or resources) may trigger this capacity loss as well.  We may consider
preventing a node from making a change which would result invalid balances -
perhaps by requiring cordoning or draining some part of the tree before the
change is executed. This requires further consideration and will not be included
in the initial implementation.

### Risks and Mitigations

* Users may create a cycle in the Cohort hierarchy - Kueue will stop all new admissions within
the entire tree. The already admitted workloads will be allowed to continue. Appropriate 
ClusterQueue/Cohort Status Conditions will be set and Events emitted.

* Scheduling and preemption may require more computation/resources.

## Design Details

The Cohort API will initially start only with the basic functionality. Additional policies
regarding sharing Cohort resources can be added later.

```go

type Cohort struct {
    metav1.TypeMeta   `json:",inline"`
    metav1.ObjectMeta `json:"metadata,omitempty"`

    Spec   CohortSpec   `json:"spec,omitempty"`
}

type CohortSpec struct {
    // Cohort parent name. The parent Cohort object doesn't have to exist.
    // In such case, it is assumed that parent simply doesn't have any
    // quota and limits and doesn't have any other custom settings.
    Parent string `json:"parent,omitempty"`

    // resourceGroups describes groups of resources that the Cohort can
    // share with ClusterQueues within the same group of Cohorts/ClusterQueues.
    // Each resource group defines the list of resources and a list of flavors
    // that provide quotas for these resources.
    // Each resource and each flavor can only form part of one resource group.
    // resourceGroups can be up to 16.
    //
    // BorrowingLimit specifies how much ClusterQueues under this Cohort can borrow
    // from ClusterQueues/Cohorts that are NOT under this Cohort. For Cohorts without
    // a parent (top of the hierarchy) the BorrowingLimit has to be 0.
    //
    // LendingLimit specifies how much ClusterQueues that are NOT under this Cohort
    // can borrow from the ClusterQueues/Cohorts that are under this Cohort.
    // If any of the Limits is not specified it means that there is no limit
    // and ClusterQueues can borrow/lend as much as they want/have.
    // 
    // +listType=atomic
    // +kubebuilder:validation:MaxItems=16
    ResourceGroups []ResourceGroup `json:"resourceGroups,omitempty"`
}
```

Currently, with 2-level hierarchy for each Cohort and ClusterQueue, Kueue
makes sure that the following balances are kept:

* ClusterQueues don't use more resources than they have and could possibly borrow.
* Within a Cohort, the total amount of requested capacity doesn't exceed the total quota
from all ClusterQueues, constrained by LendingLimit.

Admission of a new workload can happen if both balances are kept after 
adding the new workload. Kueue doesn't track who is borrowing/lending from who. It
is enough that balances are kept and with good balances, there exists such borrower-lending 
mapping that fulfills all needs. 

With Hierarchical Cohorts, Kueue will be checking the whole Cohort subtree whether the
correct balances are kept. To be more precise what it means
let's define a function `T(x,r)` that takes either ClusterQueue x 
or Cohort x and resource r (from a specific resource flavor). 

`T(x, r)` returns the amount of resource r that is available at the level of x from ClusterQueues
and Cohorts that are either x or children of x (possibly indirect). In other words, how much of resource r can come from 
the subtree. The value may be negative, what means that the subtree is borrowing from the outside of the subtree (the rest 
of the hierarchy)

`T(x,r)` can be relatively easily calculated while traversing the Cohort tree.

* `T(x,r)` when x is a ClusterQueue:
$$T(x,r) = quota(x,r) - usage(x,r)$$

* `T(x,r)` when x is a Cohort:
$$T(x,r) = quota(x,r) + \sum_{c \in children(x)} min(lendingLimit(c,r), T(c,r))$$

Obviously, with the correct admission process, for any x and r, `T(x,r)  >=  -borrowingLimit(x,r)`
Otherwise there would be too big debt at level x - some subtree is requesting more than allowed.

Slightly less obvious, but also true is: **If there is no too big debt at any level then the admission is correct**.

Negative `T(x,r)` presents the total amount of resources that a subtree is borrowing. Positive `T(x,r)` presents the total 
amount of resources that a subtree can deliver (with respect of the `lendingLimit`). At the very top of hierarchy `T(x,r) >=0` 
(`borrowingLimit` is there 0 since there is no-one to borrow from). `T(x,r)>=0` can occur also within the hierarchy.

`T(x,r)>=0` means that at the level of x, the negative balance of all subtrees can be evened-out by
other subtrees that have some extra capacity, with respect to their lendingLimit. Extra capacity can be 
"passed" to the needing subtrees. Then, after this passing, the previously negative subtree becomes positive, and 
we can re-apply the logic there. All negative sub-sub-trees can be balanced out by positive subtrees and the capacity
that coming from "above". And so on and so on, up to reaching individual ClusterQueues.

So a new workload can be admitted to a ClusterQueue if and only if, after admission, `T(x,r)  >=  -borrowingLimit(x,r)` 
stays true at all elements of the hierarchy.

### Test Plan

[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.

##### Prerequisite testing updates

#### Unit Tests

As the hierarchical cohorts reside entirely inside Kueue, most of the 
tests will be done as unit and integration tests, checking things like:

* Existing functionality at 2-levels.
* Long-distance borrowing on multi-level hierarchy.
* Lending/borrowing limits placed on many levels.
* Preemptions across hierarchy.

### Graduation Criteria

This is an a core API element and will graduate together with the other core APIs.


## Implementation History

* 2023.12.29 - KEP - API and semantics.

## Drawbacks

It makes the scheduling even more complex and computation heavy. With complex limits 
and quotas it may be hard for users to keep them under control.

## Alternatives

* https://github.com/kubernetes-sigs/kueue/pull/1093 - Hierarchical ClusterQueues.
