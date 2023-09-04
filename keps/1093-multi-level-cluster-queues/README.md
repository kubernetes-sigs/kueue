# KEP-1093: Multi Level Cluster Queues

<!--
this is the title of your kep. keep it short, simple, and descriptive. a good
title can help communicate what the kep is and should be considered as part of
any review.
-->

<!--
a table of contents is helpful for quickly jumping to sections of a kep and for
highlighting any additional information provided beyond the standard KEP
template.

Ensure the TOC is wrapped with
  <code>&lt;!-- toc --&rt;&lt;!-- /toc --&rt;</code>
tags, and then generate with `hack/update-toc.sh`.
-->

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [PlanA](#plana)
  - [Validation](#validation)
    - [ClusterQueue](#clusterqueue)
    - [Hierachy](#hierachy)
    - [ResourceFlavor](#resourceflavor)
    - [localqueue](#localqueue)
  - [Schedule Behavior](#schedule-behavior)
  - [API](#api)
- [Implementation](#implementation)
- [Testing Plan](#testing-plan)
  - [NonRegression](#nonregression)
  - [Unit Tests](#unit-tests)
  - [Integration tests](#integration-tests)
- [Implementation History](#implementation-history)
<!-- /toc -->

## Summary
This proposal allow cluster admins to define a multi-level hierachy for cluster queues. Multi-level of cluster queues allow admins and users to define different dequeue policies for different node. This will allow Kueue to manage more levels of resources.
## Motivation
Systems like Yarn allow creating a hierarchy of fair sharing, which allows modeling deeper organizational structures with fair-sharing.
Kueue currently supports three organizational levels: Cohort (models a business unit), ClusterQueue (models divisions within a business unit), namespace (models teams within a division). However fair-sharing is only supported at one level, within a cohort. This is not convinent if there are more than one level in an organization or if some users want to manage the resource consumption of his/her own jobs.
### Goals

- Allow admins to define multi-level hierachy of cluster queues.
- Allow users to define weights for different job of him/her. 
### Non-Goals

-  Cohort will not be deprecated because Kueue is forward campatible.
## Proposal
We will extend cluster queues to allow a cluster queue to be the parent of another cluster queue. And allow admins to define weights for all these cluster queues.
### PlanA
We propose to add a `.Spec.Children`field to the ClusterQueue CRD. `.Spec.Children`field is a list of `string` to define the children CQs, we will not check if the child CQ exist or not. Noted that `.Spec.Children` cannot exist with `.Spec.Cohort` concurrently.
We propose to add a crd named `ClusterQueueTree` to contains `.status.trees[]` to display current trees' state. This crd will only show the hierachy, if admins want to query the resource consumption of cluster queue, they need to query through `ClusterQueue` crd.
```go
type Policy string
var FIFO Policy = "fifo"
var Fair Policy = "fair"
var Priority Policy = "priority"
var Capacity Policy = "Capacity"
type ClusterQueueSpec struct {
	...
    Policy Policy
    Children []string
    // only make effect when Policy == Capacity
    MaxWeight *int32
    MinWeight *int32
    // can only be set if weight is nil
    ResourceGroups []ResourceGroup
}

type ResourceFlavor struct {
    ...
    RelatedElasticQuota
}
```
### PlanB
We propose a clustered crd named `ClusterQuotaTree` to define the hierachy of all cluster queues. 
```go
type WeightedClusterQuota struct {
    Name string
    // only make effect when Policy == Capacity
    // this can not be set with MinResources and MaxResources
    // you can not set Weight and Resources in one tree
    MinWeight *int32
    MaxWeight *int32
    // only make effect when Policy == Capacity
    // this can not be set with MinWeight and MaxWeight
    // you can not set Weight and Resources in one tree
    MinResources []FlavorQuotas
    MaxResources []FlavorQuotas
    // will create cluster queue with the template if not found
    // ClusterQuotaTemplate *v1.ObjectReference
    ClusterQuotaTemplate *ClusterQueueSpec
}

type PreemptionFence string
const EnableCrossQuotaPreemption PreemptionFence
const DisableCrossQuotaPreemption PreemptionFence

type ClusterQuotas struct {
    // By default we disable cross quota preemption
    Fence          *PreemptionFence
    WeightedQuotas []WeightedClusterQuota
}

type ClusterQuotaTree struct {
    Hierachies           map[string]ClusterQuotas
    ResourceGroup        ResourceGroup
    // will create cluster queue with the template if not found
    // RootQuotaTemplate    *v1.ObjectReference
    ClusterQuotaTemplate *ClusterQueueSpec
}
```
Properties other than hierachy and resource groups will still be defined on cluster queues. 
When a tree is created, we will check if all cluster queues in the tree belong to exactly one tree. Ohterwise we will reject the tree.
After a tree is created, controller will create corresponding cluster queues for it with the `ClusterQuotaTemplate` if any queue does not exist and config the resource group field for the queue. 
### Validation
#### ClusterQueue
We don't support to use cohort and cluster queue tree for same cluster queue.
If a cluster queue is in a tree, ites resource groups is immutable, cluster queue tree will set the field.
#### Hierachy
Sum of `MinWeight` of ClusterQuotas belong to one Quota must be 100. `MaxWeight` can not be larger than 100.
Sum of `MinResources` must be lower than that of the parent quota. `MaxResources` must be lower that of the parent quota.
#### ResourceFlavor 
#### localqueue
No local queue can point to cluster queues that have children in cluster queue tree.
### Schedule Behavior
We will check the ClusterQueues' Resource Flavors level by level to ensure all flavors have enough quota.
### API

## Implementation

### 

## Testing Plan

### NonRegression
The new implementation should not impact any of the existing unit, integration or e2e tests. 
### Unit Tests
All the Kueue's core components must be covered by unit tests.
### Integration tests

-  Scheduler 
   - Checking if a Workload gets admitted when an admitted Workload releases a part of it's assigned resources.
-  Kueue Job Controller (Optional) 
   - Checking the resources owned by a Job are released to the cache and clusterQueue when a Pod of the Job succeed.
## Implementation History