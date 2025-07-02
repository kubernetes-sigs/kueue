# KEP-4136: Admission Fair Sharing

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [Entry penalty](#entry-penalty)
  - [User Stories (Optional)](#user-stories-optional)
    - [Story 1](#story-1)
    - [Story 2](#story-2)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [Test Plan](#test-plan)
      - [Prerequisite testing updates](#prerequisite-testing-updates)
    - [Unit Tests](#unit-tests)
    - [Integration tests](#integration-tests)
  - [Graduation Criteria](#graduation-criteria)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
<!-- /toc -->

## Summary

This KEP describes the mechanism for fair admission of workloads coming from a group of
different sources (like Cluster and Local Queues) based on the source shared resource usage.
Workloads from sources that use less are admitted before workloads coming from sources that use more. 

## Motivation

Currently Kueue has a Fair Sharing mechanism that enforces fair-sharing of unused resources
via preemption. If one Cluster Queue is using much more of the resources than the other one
that is in need, some workloads from the first one may be preempted to allow more “fair” distribution. 

This model has multiple assumptions:

* Workloads can be preempted.
* Users get a proper quota to do their regular business.
* Users come from multiple teams/organizations and they would rather have a strict policy but 
fair policy than show understanding to those consuming all shared resources. 

However, these assumptions are not universal. Sometimes:

* Workloads should not be preempted.
* Users operate on a shared but bigger quota.
* Fairness should not win over getting the workloads eventually completed.

In that case the existing mode doesn’t work and a different one needs to be employed.

### Goals

* Establish a method for how shared resource usage is calculated and recorded and how users can fine tune the mechanism.
* Allow to specify a fair admission scope at either individual Cluster Queue or Cohort scope.
* Allow to specify the relative importance of LocalQueues targeting the same ClusterQueue.
* Amend the admission mechanism to work on admission scopes instead of only on ClusterQueues. 
* Select the appropriate admission candidates for each of the admission scopes and admit them according to the selected queueing policy.
* Make the new mechanism complementary to the existing preemption-based fair sharing

### Non-Goals

* Store time series data inside K8S.
* Provide precise shared resource usage accounting or billing.

## Proposal

* Modify CQ’s FairSharing struct with 

```go
type FairSharing struct {
	// Weight denotes how important the given queue when competing against other queues 
// for unused shared resources. The exact impact of the weight in fair share calculations
// depends on the fair share algorithm used. Default = 1.
	Weight *resource.Quantity `json:"weight,omitempty"`
}
```

* Expand LQ spec with the same FairSharing struct (Cohort will be expanded with FairSharing 
as a part of the Preemptive FS implementation in hierarchical structure).

* Modify FairSharingStatus struct with 

```go
type FairSharingStatus struct {
	// WeightedShare represents the usage above nominal quota, with the weight applied in.
	// The bigger the value is the more shared resources has been allocated and the less 
	// entitled the queue is for more shared resources. 
	// The exact details and the interpretation of the value depends on 
    // the fair sharing algorithm used.
	WeightedShare int64 `json:"weightedShare"`

	
	// ConsumedResources represents the aggregated usage of resources over time,
	// with decaying function applied. 
	// The value is populated if usage consumption functionality is enabled in Kueue config.
	ConsumedResources corev1.ResourceList `json:"consumedResources,omitempty"`

	// LastUpdate is the time when share and consumed resources were updated.
	LastUpdate metav1.Time `json:"lastUpdate,omitempty"`
}
```

* Add FairSharingStatus to LocalQueue (and Cohort).

* Create a new struct AdmissionScope and make it an optional field for CQ and Cohort Spec. If
not provided, CQ or Cohort is not considered an AdmissionScope and is not a subject for new
admission logic. If there are two AdmissionScopes on the path from CQ/Cohort to the top of
the hierarchy tree, the higher one is used.

```go
const (
	// FairSharing based on usage, with QueuingStrategy as defined in CQ.
	UsageBasedAdmissionFairSharing AdmissionMode = UsageBasedFairSharing

    NoAdmissionFairSharing AdmissionMode = NoAdmissionFairSharing
)

type AdmissionScope struct {
    AdmissionMode AdmissionMode
}
```

* When selecting candidates for admission groups all workloads from LQ to CQ or the
topmost Cohort that is marked with AdmissionScope. Then sort them using criterias:

- Usage vector  built from ConsumedResources from TopCohort to LQ
- Priority
- Timestamp

The usage vector will be sum of the `ConsumedResources` weighted according to `resourceWeights`
(mentioned later in the KEP)

Let’s look at a couple scenarios:

1. AdmissionScope at CQ, CQ queueing policy is FIFO, 3 LQ pointing to CQ. Kueue considers
all CQ resources and potentially borrowed resources as “shared” resources and fair sharing
is applied to all workloads.
Kueue sorts the workloads by their LQ usage (if mode is `UsageBasedFairSharing`), priority and
timestamp and tries to admit the first one from the list. Other workloads are not attempted
until the first one is not admitted.

2.  AdmissionScope at CQ, CQ queueing policy is BestEffort, 3 LQ pointing to CQ. 
Kueue considers all CQ resources and potentially borrowed resources as “shared” resources and
fair sharing is applied to all workloads.

Kueue sorts the workloads by their LQ usage (if mode is `UsageBasedFairSharing`), priority and
timestamp and tries to admit the first one from the list. If it fails and the second, third
or following is possible then that workload is admitted, under condition that it might get preempted.

3. AdmissionScope at Cohort level - Kueue operates in a mixed mode. Inside CQ workloads are
selected according to their AdmissionMode (if specified). If a workload fits entirely into
nominal quota, then it is admitted immediately, if not it goes into cohort-level fair sharing.
For Cohort we select all the “sticking out” workloads, and sort them by their CQ usage, priority
and timestamp. Kueue attempts to admit the first workload from the list of sticking-out
(workload + current_usage > resources), just like if it was one big strict FIFO queue. 
For multi-level hierarchy under one AdmissionScope we would treat underlying Cohorts as Fifo CQs.


* Additionally there will be the resource usage calculation loop. The frequency of the
calculation will be controlled globally in Kueue’s config file. Accounting would be done
using something like geometric average:

usage_sum = (1-A) * previous_usage_sum + A * current_usage.  

The value will be stored in FairSharingStatus for all LQ, CQ, and Cohorts. The value will not be zeroed
after Kueue restart or after brief period of downtime. However if the period is longer,
the value should be automatically zeroed.


The user will be able to configure the decaying factor A in Kueue’s config file by specifying
the half life decay time - after what time the current shared usage will decay to half of its original value.

A = 1 - 0.5 ^ (sampling/half_life_decay)

* Configuration will sit in FairSharing stuct in Kueue config. There will be the following modifications:

  - usageHalfLifeDecayTime - half life decay time of usage, as described above.
  - usageSamplingInterval - how often usage is calculated.
  - resourceWeights - how much consumption of individual resources is important when comparing usage.
  - resetInactivityPeriod - if Kueue has not updated the value for this period then the value should be zeroed.
 
If the user doesn't want any preemptions while fair sharing, preemptionStrategies should be left empty.

* If preemptionStrategies is non empty Kueue attempts to combine two fair sharings at the same time. 
For each of the Admission Scopes Kueue selects one workload to be attempted. And then these pre-selected 
workloads are sorted based on their Preemption-based fair share value. If some of them don't fit,
fair sharing preemption may be executed. So admission-based fair sharing only reshuffles workloads
within AdmissionScope and then other mechanisms are applied as usual.

*  Since Kueue v0.13, when picking the candidates for preemption, with AdmissionScope equal ClusterQueue, Kueue orders
workloads based on the LocalQueue resource usage when considering workloads within the same ClusterQueue. 
Workloads from different ClusterQueues are not compared against each other using the resource usage dimension.

### Entry penalty
The mechanism as implemented in Kueue 0.12 can be exploited, because a tenant
can submit thousands of jobs which get scheduled in a short period of time.

E.g. Let's consider:
```
Tenant's A FairSharingStatus: 0.1 CPU
Tenant's B FairSharingStatus: 0.05 CPU
usageSamplingInterval: 5mins
```

Here, all Jobs submitted by Tenant B  within the next 5min get scheduled.
Even if Tenant B submitted a job that consumed 1000 CPUs, consecutive jobs would still be prioritized because of the delay in updating the status.

Hence, since v0.13 Kueue adds an entry penalty to FairSharingStatus every time it admits a Workload. The penalty should be calculated with the formula: `penalty = A * requested_resource`, where `A` is as above:

`A = 1 - 0.5 ^ (sampling/half_life_decay)`. 

This an equivalent of a job's usage that has been admitted `samplingInterval` ago. The value of penalty is an arbitrary decision for now.
After we collect customers' feedback, we'll consider introducing an API that allows to configure it.

### User Stories (Optional)
#### Story 1

I have multiple users using the same ClusterQueue. Each has its own namespace and LocalQueue
through which they submit workloads. I want to fairly admit their workloads so that one active
user doesn’t block the cluster too much.

#### Story 2

I have multiple teams that may be sending workloads of various sizes. I want to give each team
some guaranteed capacity and at the same time, allow them to fairly share some bigger pool of resources. 

### Risks and Mitigations

* Having 2 fair sharing mechanisms and confusion between preemption-based fair sharing and admission time fair sharing.

* Increased complexity of the project.

## Design Details

Covered in Proposal.

### Test Plan
[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.
##### Prerequisite testing updates

#### Unit Tests
The code will be thoroughly covered with unit tests.

In particular:
* CQ level fair sharing between LQ for both strict FIFO and best effort.
* Cohort with owned resources, and CQ with guaranteed quota.


#### Integration tests

They will mainly focus on larger scope scheduling (involving multiple cohorts/cqs) and
interactions with preemptive fair sharing outside admission scope.

### Graduation Criteria

The implementation will be split into 2 subfeatures:

* CQ + LQ level support
* Multi-level Cohort+CQ+LQ support

Obviously, the second depends on the first to some extent. The first however may reach
Beta/GA without starting the second.

The graduation criterias are quite standard:

* Beta - positive feedback from Alpha, api seems reasonable.
* GA - positive feedback, no bugs, no api changes needed.

We hope to have CQ+LQ in alpha for the next Kueue release (0.12).

## Drawbacks
* Adds additional complexity to the system.
* Creates yet another fair sharing mechanism.

## Alternatives

* Not having the feature.
* Modifying/replacing the existing preemptive fair sharing algorithm.

