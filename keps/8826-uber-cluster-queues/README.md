# KEP-8826: Uber Cluster Queues

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories (Optional)](#user-stories-optional)
    - [Story 1: The Foundation Model Training Run](#story-1-the-foundation-model-training-run)
  - [Story 2: Disaster Recovery Simulation](#story-2-disaster-recovery-simulation)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [API](#api)
  - [Implementation](#implementation)
    - [Cohort Interaction and Borrowing Subtree Isolation](#cohort-interaction-and-borrowing-subtree-isolation)
    - [Lending Limits](#lending-limits)
- [Observability and Metrics](#observability-and-metrics)
    - [Metrics](#metrics)
    - [Workload Lifecycle Events](#workload-lifecycle-events)
    - [Status Condition:](#status-condition)
  - [Test Plan](#test-plan)
    - [Unit Tests](#unit-tests)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
<!-- /toc -->

## Summary

This proposal introduces a new operational capability within the Kueue architecture tentatively name
UberClusterQueue or UCQ. The primary objective of this enhancement is to address the operational
friction associated with executing high-priority, resource-intensive "Hero" workloads—jobs that
require immediate, massive capacity, often exceeding the standard fair-sharing agreements and
lending limits defined in a cluster's cohort topology.

Currently, Kueue employs a sophisticated quota management model designed to maximize resource
utilization through cooperative borrowing and fairness algorithms. While highly effective for
steady-state multi-tenant operations, this model creates significant administrative overhead when an
organization faces an "emergency" or "priority override" scenario. The proposed solution introduces
a specialized preemption rule set, UberClusterQueueRules, which empowers a designated ClusterQueue
to preempt any workload within its cohort subtree, irrespective of priority or guaranteed quota
boundaries, provided the victim is not protected by an even higher hierarchical authority. This
feature essentially allows for the declarative configuration of "Break Glass" capacity without
requiring destructive, manual reconfiguration of the cluster's quota definitions.

## Motivation

The efficient orchestration of batch workloads in Kubernetes relies heavily on the concept of
elasticity and resource sharing. Kueue has successfully implemented a model where ClusterQueues can
be grouped into Cohorts to share unused quota. This "Lending and Borrowing" model is predicated on
the assumption of cooperation: a queue borrows resources only when they are unused by the owner, and
returns them when the owner reclaims them.

However, operational reality often presents scenarios that defy the cooperative model. In
high-performance computing (HPC), AI/ML research, and financial modeling, there exists a class of
workloads—termed here as "Hero Jobs"—that are characterized by three critical attributes:

* Urgency: They must start immediately, bypassing standard wait times associated with fair sharing
calculations.  

* Scale: They often require the aggregate capacity of an entire organizational
subtree (e.g., all GPUs assigned to "Department A", spread across "Team 1", "Team 2", and "Team 3").

* Precedence: Their execution priority supersedes the standard "guaranteed quota" contracts of other
tenants.

The Problem of "Hero" Workloads Under the current Kueue architecture, treating a Hero job requires
manual intervention. The existing "strict" guarantees mean that if Team A has a guaranteed quota of
10 GPUs, no other team can preempt those jobs to reclaim the GPUs, even if the other team has a
"higher priority" job, unless specific borrowing conditions are met. When a Hero job arrives, system administrators are currently forced to choose between two
suboptimal operational patterns. 

* Temporary Quota Reorganization involves the
manual reshuffling of quota values. Administrators must edit the ClusterQueue definitions to
artificially inflate the Hero queue's nominal quota to match the cohort total and simultaneously
reduce or zero out the quotas of all sibling queues. This approach is operationally expensive,
error-prone, and disruptive, as it requires a "back-and-forth" modification of declarative
configurations that risks leaving the cluster in an inconsistent state if the reversion is
mishandled.

* Permanent "Head" Reorganization involves restructuring the
organization's quota tree to place all resources in a single "Head" queue and relying entirely on
fair sharing algorithms for distribution. While this simplifies the execution of large jobs, it
permanently obscures the resource guarantees of individual teams. It forces the organization to
abandon the concept of "guaranteed quota" in favor of "weighted shares," which makes capacity
planning, billing, and strict SLAs (Service Level Agreements) difficult to enforce or understand.

Limitations of Current Quota Models The necessity for the UberClusterQueue stems from specific
rigidities in the current preemption logic.

* Guaranteed Quota Immunity: In the standard model, a workload running within its ClusterQueue's
nominal quota is essentially immune to preemption by siblings. A neighbor queue cannot preempt it,
even if the neighbor has a higher priority job, because the running job is consuming its "owned"
resources. Hero jobs require the violation of this immunity.

* Lending Limits: Queues can define lendingLimits to prevent their resources from being borrowed. A
Hero job, by definition, needs to ignore these limits to scavenge all physical capacity available in
the subtree.

* Borrowing Complexity: The current borrowing logic is constrained by fair sharing weights. A Hero
job should not be constrained by "fairness"—it is inherently unfair.

### Goals

The primary goal is to provide a mechanism to run high-priority workloads that can consume all the
quota in a given subtree. This decomposes into the following specific requirements:

* Absolute Preemption Authority: Enable a specific mode where a queue can claim resources
beyond its nominal quota by forcibly evicting workloads from sibling queues.

* Universal Subtree Preemption: Workloads in a Hero CQ (UCQ) must be capable of preempting any
other workload within the same cohort tree. This preemption must occur regardless of the victim's
priority or whether the victim is running within its guaranteed (nominal) quota.

* Hierarchical Protection: A specific immunity hierarchy must be established. Workloads in a
Hero CQ cannot be preempted by standard queues. Furthermore, they cannot be preempted by other Hero
queues unless the preemptor is "higher" in the hierarchy (closer to the root) or has a higher
intra-queue priority.

* Bypass Limits: Workloads in a Hero CQ should ideally not be limited by lendingLimits defined
by other queues in the subtree, although for the initial MVP, adhering to lending limits might be an
acceptable transitional constraint.

* Safety Caps: To prevent a Hero queue from destabilizing the entire cluster (potentially
beyond its organizational scope), it must still be bounded by a borrowingLimit or the total capacity
of the cohort.

* Subtree Containment: Preemption privileges are strictly scoped to the cohort subtree. A Hero
queue cannot preempt workloads from a completely disjoint cohort or a different organizational tree.

* Deterministic Victim Selection: The preemption algorithm must select victims based on a
specific preference order: workloads borrowing quota first, then workloads within nominal quota;
lower priority first; and finally, newer workloads first (protecting long-running jobs).

### Non-Goals

* Cross-Cohort Dominance: It is not a goal to allow a Hero ClusterQueue in "Cohort A" to preempt
resources from "Cohort B". The "Hero" status is a local property of the resource pool defined by the
cohort.

* Dynamic Workload Promotion: We do not intend to allow individual jobs to flag themselves as "Hero"
dynamically. The "Hero" property is an attribute of the ClusterQueue configuration, ensuring that
access to this capability is controlled via RBAC on the Queue resource, not the Job resource.

* Complex Peer Fair Sharing: If multiple Hero queues exist at the same level of the hierarchy, we do
not aim to implement a complex weighted fair sharing mechanism between them for the MVP. Standard
priority and creation time will dictate precedence.

## Proposal

The core proposal is to introduce a new PreemptionRules configuration within the ClusterQueue API.
This configuration effectively switches the queue's scheduler behavior from the default
"Cooperative" mode to a "Dominant" mode.

In the standard cooperative model, queues A, B, and C operate as peers. If Queue A needs resources,
it checks if B or C have unused capacity (Lending). If B is using its capacity, A waits, regardless
of how important A's job is. The resource flow is fluid but constrained by ownership rights.

In contrast, under the proposed Hero model, if Queue A is designated as an UberClusterQueue, the
relationship changes fundamentally. Queue A retains its peer relationship during idle times, but
upon job submission, it assumes a dominant posture. It views the entire nominal quota of the subtree
(A + B + C) as its own, almost nomina, Accessible Quota. It then proceeds to obtain resources from B and C as if
those resources were its own, evicting running workloads to satisfy its demand. This "Scorched
Earth" approach is bounded only by the borrowingLimit configured on Queue A. Contrary to regular
CQ, UberClusterQueue cannot get more resources than it is defined in its subtree, in particluar
borrow from outsider CQs.


### User Stories (Optional)

#### Story 1: The Foundation Model Training Run

Persona: Platform Administrator for an AI Research Lab.

Context: The lab has four research teams (Alpha, Bravo, Charlie, Delta), each allocated 200 GPUs via
ClusterQueues in a shared cohort. This guarantees fairness for daily experiments.

Problem: Once a month, the lab performs a "Foundation Model Training" run that requires 800 GPUs
(100% of the cluster) for 3 days to converge.

Solution: The administrator configures a separate ClusterQueue named training-hero that shares the
cohort but has UberClusterQueueRules enabled. Access to submit jobs to this queue is restricted to
the Lead Scientist. When the training job is submitted to training-hero, Kueue automatically
preempts the experiments of Alpha, Bravo, Charlie, and Delta. The training runs to completion. Once
finished, the resources are released, and the teams resume their standard fair sharing.

Benefit: No manual editing of the 4 team queues is required. The "Peacetime" configuration remains
static.

### Story 2: Disaster Recovery Simulation

Persona: SRE Lead at a FinTech company.

Context: The company runs thousands of Monte Carlo simulations for risk analysis on a shared
cluster, managed by standard fair sharing.

Problem: A regulatory audit requires an immediate "Disaster Recovery" simulation to prove solvency
under extreme market conditions. This simulation must complete within 4 hours, requiring maximum
compute density.

Solution: A standing dr-simulation queue exists with Hero privileges. The audit job is submitted
there. It immediately preempts the low-priority routine risk analysis jobs. The audit completes on
time.

Benefit: Compliance requirements are met without complex operational runbooks to "clear the
cluster."

### Risks and Mitigations

* Preemption Storms.  A flaky Hero job that crashes and restarts could repeatedly preempt and kill
hundreds of standard jobs, wasting massive compute cycles.


* Resource Starvation.  A user mistakenly leaves a non-critical job in a Hero queue, permanently
starving the rest of the organization.  "Hero" capability is attached to the ClusterQueue resource,
which is protected by Kubernetes RBAC. Only trusted admins should have write access to submit jobs
to Hero queues.

* Priority Inversion.  A lower-priority job in a Hero queue preempts a higher-priority job in a
standard queue.  This is intentional behavior. The "Hero" status of the queue overrides the
priorityClassName of the workload. Documentation must clearly explain that Queue topology takes
precedence over Workload priority in this mode.

* Observability Gaps.  Users confuse "Hero Preemption" with cluster instability or bugs.  Distinct
"Reasons" in events and metrics (e.g., UberCQPreemption) will clearly distinguish these evictions
from standard failures.

## Design Details


### API

API Definition The enabling mechanism for the Hero ClusterQueue will be a new field in the
ClusterQueue CRD. We will leverage the existing preemption struct to introduce a "Rules" selection.
This allows for future extensibility if other preemption strategies (e.g., "Weighted Preemption")
are developed.  The API modification is located in pkg/apis/kueue/v1beta1/clusterqueue_types.go.


```go 

/ PreemptionRules designates the set of rules used to determine 
// preemption logic within the ClusterQueue and its cohort.
// +enum
type PreemptionRules string

const(
    // DefaultPreemptionRules indicates the standard Kueue behavior:
    // - Respects CQ nominal quota.
    // - Preempts only lower priority workloads within the CQ.
    // - Reclaims borrowed quota from cohort based on fair sharing/priority.
    DefaultPreemptionRules PreemptionRules = "DefaultPreemptionRules"

    // UberClusterQueueRules indicates the "Hero" behavior:
    // - Preempts any workload within the cohort subtree (except protected ones).
    // - Is not subject to reclamation from standard CQs.
    // - Ignores lending limits (future).
    UberClusterQueueRules PreemptionRules = "UberClusterQueueRules"
)

type ClusterQueuePreemption struct {
    // Rules defines which set of logic to apply when considering workloads
    // within this CQ for admission and preemption.
    //
    // Possible values:
    // - `DefaultPreemptionRules` (default): Standard behavior.
    // - `UberClusterQueueRules`: Hero behavior.
    //
    // +optional
    // +kubebuilder:default=DefaultPreemptionRules
    // +kubebuilder:validation:Enum=DefaultPreemptionRules;UberClusterQueueRules
    Rules PreemptionRules `json:"rules,omitempty"`

    // The existing fields (ReclaimWithinCohort, WithinClusterQueue) remain.
    // Their semantics are effectively overridden or augmented when Rules
    // is set to UberClusterQueueRules.
    //...
}

```

### Implementation

The implementation of the UCQ logic requires a significant
divergence in the scheduler's "candidate selection" phase. The scheduler currently iterates through
workloads to find preemption candidates based on a least disruptive on fair sharing-based strategy,
that respects nominal quotas. The UCQ logic introduces a "Most Aggressive" strategy.  

For a standard queue, the accessible quota (replacing nominal quota in admission calculations)
is a function of its nominal quota plus a share of the cohort's unused capacity.  For a UCQ, 
the definition of "Accessible" expands.  

* Formula: AccessibleHero = Sum(NominalQuota of all CQs in Subtree). 

* Constraint: The accessible amount is clamped by the borrowingLimit of the UCQ itself. If 
borrowingLimit is nil, it defaults to the sum of the subtree.

Crucially, when a Hero workload is admitted, the scheduler must dynamically
update the accessible quota for other queues in the subtree. The mathematical relationship is:

```
Accessible = Nominal - Sum(Workload[k] * Nominal/Total_SubreeNominal for all UCQ workloads k)

```

This formula implies that the Hero workload's usage is conceptually "billed" proportionally against
all queues in the subtree, reducing their accessible capacity (replacing nominal capacity) for 
future admissions, preemption, reclamation and fair sharing calculations. This ensures that while the Hero 
job runs, the remaining capacity (if any) is still distributed fairly among the standard queues.

At the same time, borrowing limit is explained by the amount that was taken from the nominal quota,
to prevent situation where a rightfully running and not preempted workload drops completely outside of quota.


#### Cohort Interaction and Borrowing Subtree Isolation

It is crucial to define the blast radius of a
Hero queue. Requirement explicitly states that workloads in a UCQ cannot borrow from outside of a
cohort subtree:This means that if a ClusterQueue is part of a deeply nested cohort structure, the
"Hero" privileges extend only to the resources defined within that specific branch. This prevents a
misconfigured queue from traversing up to the root and impacting completely unrelated departments
that happen to share the same top-level root but are organizationally distinct.  

#### Lending Limits

Standard Kueue behavior honors LendingLimits to allow queues to "hoard" unused capacity. However, a
true Hero implementation implies overriding these limits. If Queue B has 10 unused GPUs but refuses
to lend them (LendingLimit=0), a Hero Queue A must still be able to take them.


## Observability and Metrics 

The introduction of aggressive preemption capabilities necessitates robust observability. 
Operators must be able to distinguish between "Healthy
Preemption" (Fair Sharing/Hero adjustments) and "Unhealthy Preemption" (Instability). 

#### Metrics 

We will enhance the existing metric kueue_preempted_workloads_total by adding a new value to the reason
label: UberCQPreemption.  
```
kueue_preempted_workloads_total{reason="UberCQPreemption", queue="..."}
```
This allows for alerting on the rate of Hero interventions. A sudden spike here indicates a Hero job
has landed and is clearing the deck.

#### Workload Lifecycle Events 

The lifecycle of a workload changes when it interacts with a Hero queue. Typically, a workload 
preempted for fair sharing might just be requeued. With Hero preemption, the event stream on the 
victim workload must be explicit.  `Event:
Type=Warning, Reason=Preempted, Message="Preempted by Hero Workload <Name> in Queue <UCQQueue>"`

#### Status Condition: 
The Workload status will transition to Evicted with the condition Preempted. The
reason field in the condition object will be set to UberCQPreemption. This specific reason code
allows automation (and users) to understand that their job didn't fail due to a bug, but was
administratively overridden.


### Test Plan

#### Unit Tests

Regular unit and integration tests will be added. The scope of the changes seems relatively
limited to quota based admission and preemption. In particular the test will cover:

* Hero job preempting tasks within their nominal quota under the same cohort.
* Hero job not preempting tasks outside of cohort subtree.
* Hero job tasks not being preempted by high priority jobs.
* Accessible nominal quota and borrowing correctly calculated when the hero job is running.
* UCQ configured above other UCQ in a cohort tree.

### Graduation Criteria

Same as for other features. Positive feedbacks, fixed bugs, adopted API.


## Implementation History


* 2026-01-28: First KEP draft.

## Drawbacks

* The KEP complicates the already complex admission and preemption logic.
* Nominal quotas stop to be guaranteed.
* Well understood rules start to have exceptions.

## Alternatives

* Staying with what is available right now, mentioned in the motivation section. 
* [#6141](https://github.com/kubernetes-sigs/kueue/pull/6141) Preemption policy in WorkloadPriorityClass.
* [#8654](https://github.com/kubernetes-sigs/kueue/issues/8654) Temporary quota overrides.
