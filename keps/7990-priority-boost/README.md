# KEP-7990: Support workload priority boost by external controllers

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [Terminology](#terminology)
  - [User Stories](#user-stories)
    - [Story 1: a platform team automates priority changes](#story-1-a-platform-team-automates-priority-changes)
    - [Story 2: an on-call engineer starts a starved Workload](#story-2-an-on-call-engineer-starts-a-starved-workload)
  - [Notes/Constraints/Caveats](#notesconstraintscaveats)
    - [The boost does not carry over an upgrade or a downgrade](#the-boost-does-not-carry-over-an-upgrade-or-a-downgrade)
    - [Kueue does not copy every later boost change to derived Workloads](#kueue-does-not-copy-every-later-boost-change-to-derived-workloads)
  - [Risks and Mitigations](#risks-and-mitigations)
    - [A boost meant for one ClusterQueue changes preemption in the whole cohort](#a-boost-meant-for-one-clusterqueue-changes-preemption-in-the-whole-cohort)
    - [Two Workloads can preempt each other repeatedly](#two-workloads-can-preempt-each-other-repeatedly)
    - [A user who can write Workloads can protect their own Workloads and delay others](#a-user-who-can-write-workloads-can-protect-their-own-workloads-and-delay-others)
- [Design Details](#design-details)
  - [API](#api)
  - [Validation](#validation)
  - [Handling](#handling)
  - [Re-evaluation after a boost change](#re-evaluation-after-a-boost-change)
  - [Derived Workloads](#derived-workloads)
  - [Interaction with other features](#interaction-with-other-features)
  - [Reference controller](#reference-controller)
  - [Security, Privacy, and RBAC](#security-privacy-and-rbac)
  - [Test Plan](#test-plan)
    - [Unit tests](#unit-tests)
    - [Integration tests](#integration-tests)
    - [e2e tests](#e2e-tests)
  - [Graduation Criteria](#graduation-criteria)
    - [Alpha](#alpha)
    - [Beta](#beta)
    - [GA](#ga)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
  - [Keep the annotation](#keep-the-annotation)
  - [Read the annotation as a fallback next to the field](#read-the-annotation-as-a-fallback-next-to-the-field)
  - [A status field](#a-status-field)
  - [A reference to the writer of the boost](#a-reference-to-the-writer-of-the-boost)
  - [A preemption-cost annotation that only orders preemption candidates](#a-preemption-cost-annotation-that-only-orders-preemption-candidates)
  - [Change the priority class of the Workload](#change-the-priority-class-of-the-workload)
  - [A cost or boost policy inside Kueue](#a-cost-or-boost-policy-inside-kueue)
  - [A boost field with a strategy](#a-boost-field-with-a-strategy)
  - [A maximum boost on the ClusterQueue](#a-maximum-boost-on-the-clusterqueue)
  - [A separate boost in each MultiKueue worker cluster](#a-separate-boost-in-each-multikueue-worker-cluster)
  - [Kueue copies a later boost change to Concurrent Admission variants](#kueue-copies-a-later-boost-change-to-concurrent-admission-variants)
  - [Prevent repeated preemption inside Kueue](#prevent-repeated-preemption-inside-kueue)
  - [A reference controller that raises the boost after each preemption](#a-reference-controller-that-raises-the-boost-after-each-preemption)
<!-- /toc -->

## Summary

This KEP adds an optional priority boost to the `Workload`,
so that an external controller or an admin can raise or lower the priority that Kueue uses for a Workload.
Kueue adds the boost to the Workload's priority.
Kueue uses the result for priority comparisons, except where a Configurable Preemptions rule selects the base priority.
The Pod priority does not change.

## Motivation

The cost of preempting a Workload is not uniform.
Some Workloads are cheap to interrupt, for example while they initialize.
Others are expensive, for example in the middle of an epoch with no checkpoint.
Currently Kueue decides preemption from the ClusterQueue, the fairness configuration, the Workload priority and recency.
Without the boost, it cannot use a runtime signal from an external system.

Currently the priority of a Workload changes only through its priority class.
Platform teams need to adjust it while the Workload runs:

- **A visible adjustment**: the boost shows users and controllers that the priority was adjusted, and by how much.
- **Fine-grained priority without many classes**: a numeric boost can express a priority such as 150 or 156
  without a WorkloadPriorityClass for every combination of base priority and runtime state.
- **One object kind**: the boost is on the `Workload`, so the external controller handles one kind,
  not every job kind.
- **A supported way to change priority at runtime**: some users edit `spec.priority` by hand
  ([#14698](https://github.com/kubernetes-sigs/kueue/issues/14698)).
  Kueue can overwrite such an edit, for example when kueue-controller-manager restarts.

### Goals

- Add an optional boost to the `Workload` that lets an external controller or an admin adjust the priority Kueue uses.
- Use the adjusted priority wherever Kueue compares Workload priorities,
  except where a Configurable Preemptions rule asks for the base priority.
- Provide a reference external controller that sets the boost.

### Non-Goals

- Define a cost or boost model inside Kueue.
- Change how Kueue computes the Dominant Resource Share (DRS).
- Add an API in this KEP that limits the effect of a boost to one ClusterQueue.
  [Configurable Preemptions](../13396-configurable-preemptions/README.md) can scope the effect on preemption.
- Limit how often Workloads preempt each other.
  The external controller sets the pace.
- Guarantee graceful termination or checkpointing.
- Let batch users set the boost on their Jobs.
  Kueue takes the boost from the `Workload` only.

## Proposal

An external controller or an admin sets a signed integer, the boost, in the `priorityBoost` field of a `Workload`.
Kueue adds it to the Workload's priority.
Kueue uses the sum for queue order, for preemption eligibility and for the order of preemption candidates.
Kueue never computes the boost.
It only copies the boost to derived Workloads and keeps a MultiKueue remote copy equal to the manager cluster's Workload.

The field replaces the `kueue.x-k8s.io/priority-boost` annotation of earlier releases.
Kueue does not read the annotation.

### Terminology

- **Base priority**: the value of the Workload's `spec.priority`.
- **Boost**: the value of the Workload's `spec.priorityBoost`. It is 0 when the field is unset.
- **Effective priority**: base priority plus boost.
- **External controller**: a controller outside kueue-controller-manager that sets the boost.
- **Reference controller**: kueue-priority-booster, an experimental external controller in the Kueue repository.
- **Derived Workload**: a Workload that Kueue creates from another Workload, or for the same job as another Workload:
  a replacement slice of an elastic job, a variant under Concurrent Admission, or a remote copy under MultiKueue.
  The parent Workload is the Workload that Kueue creates a variant from.

### User Stories

#### Story 1: a platform team automates priority changes

A platform team runs an external controller that changes the priority of Workloads by a policy.
Users described three policies:

- **Protect work that is not saved**: raise the boost of a running Workload that has not written a checkpoint for a long time.
- **Time slicing**: lower the boost of a Workload after it has run for a set time, for example 30 minutes,
  so that a waiting Workload with the same priority can preempt it.
  The reference controller implements this policy.
- **Let a preempted Workload finish**: raise the boost of a Workload that was preempted several times,
  above the Workloads that keep preempting it.

For time slicing, the external controller sets a negative boost on the running Workload:

```yaml
apiVersion: kueue.x-k8s.io/v1beta2
kind: Workload
metadata:
  name: train-a
spec:
  priorityBoost: -100
```

The waiting Workload now has a higher effective priority and preempts the running one.
This story needs a preemption policy that compares priorities, for example `preemption.withinClusterQueue: LowerPriority`,
which is not the default.

#### Story 2: an on-call engineer starts a starved Workload

A Workload has waited for a long time and has not started.
An on-call engineer wants to move it ahead without changing its priority class.
Currently they edit `spec.priority`, which Kueue does not keep.
With this KEP they set the boost by hand:

```yaml
apiVersion: kueue.x-k8s.io/v1beta2
kind: Workload
metadata:
  name: starved-job
spec:
  priorityBoost: 1000
```

The Workload moves ahead of the Workloads with a lower effective priority in its queue.
With a preemption policy that compares priorities, it can also preempt them.
With Admission Fair Sharing, or when the Workload must borrow, other Workloads can still go first.
The visibility API shows the new position.
[Reference controller](#reference-controller) says when the reference controller keeps a boost that was set by hand.

### Notes/Constraints/Caveats

#### The boost does not carry over an upgrade or a downgrade

A boost that is set only in the `kueue.x-k8s.io/priority-boost` annotation no longer applies after the upgrade of kueue-controller-manager.
It applies again when a client sets the field.

After a downgrade of kueue-controller-manager to a release without the field, Kueue reads only the annotation.
If the CRDs are downgraded too, the API server drops the field.
If they are not, the earlier kueue-controller-manager can remove the field when it updates the Workload.
The boost no longer applies until a client sets the annotation again.

#### Kueue does not copy every later boost change to derived Workloads

Kueue does not copy a later boost change to Concurrent Admission variants.
[Derived Workloads](#derived-workloads) has the full table.

### Risks and Mitigations

#### A boost meant for one ClusterQueue changes preemption in the whole cohort

Kueue uses the effective priority in its priority comparisons, including comparisons between ClusterQueues in a cohort.
An external controller that lowers the boost for time slicing inside a ClusterQueue also exposes that Workload
to reclaim and to borrowing preemption from other ClusterQueues.
With `borrowWithinCohort.maxPriorityThreshold`, a negative boost can move a Workload under the threshold.
A positive boost protects a Workload from other ClusterQueues in the same way.

Mitigation: the docs state the cohort-wide effect.
An admin can limit the effect of the boost on preemption to one ClusterQueue.
They set the `preemption` policies of the ClusterQueue to `Never`.
They add two `PreemptionConfig` rules: `Boosted` for `WithinClusterQueue` and `Base` for `WithinParentCohort`
([KEP-13396](../13396-configurable-preemptions/README.md)).
The `Base` rule also matches Workloads in the same ClusterQueue.
So with this configuration a positive boost does not protect a Workload from a Workload of its own ClusterQueue
that has a higher base priority.
This scopes preemption only.
Queue order, the order of a scheduling cycle across the cohort and the Fair Sharing tie-break still use the effective priority.
Configurable Preemptions is Alpha, so this scoping is not available with the default configuration.

#### Two Workloads can preempt each other repeatedly

Workload A preempts B, the external controller then changes a boost, and B preempts A.
Each round restarts Pods and costs both Workloads their progress.
Time slicing does this on purpose.
The risk is a cycle so fast that neither Workload makes progress.

Mitigation: the external controller must bound how often it changes a boost.
The reference controller waits a configured time after admission before it lowers the boost,
so each Workload runs at least that long in its turn.
Kueue has no rate limit for preemptions.
Preemption Limits (issue [#16925](https://github.com/kubernetes-sigs/kueue/issues/16925), in progress) will rate limit preemptions.
Its first version covers Configurable Preemptions only.
It does not yet limit the preemption by the `preemption` policies of the ClusterQueue, which a boost drives.

#### A user who can write Workloads can protect their own Workloads and delay others

A user who can create, update or patch `Workload` objects can set a large positive boost.
That Workload moves to the head of its queue and other users' Workloads cannot preempt it with priority-based policies.
In a StrictFIFO ClusterQueue, a boosted Workload that does not fit blocks the Workloads behind it.
Each change of the boost on an admitted Workload makes kueue-controller-manager evaluate the inadmissible Workloads
of its ClusterQueue again, or of its whole cohort tree.
[Re-evaluation after a boost change](#re-evaluation-after-a-boost-change) says how Kueue batches these evaluations.

Mitigation: a Job has no field, label or annotation that sets the boost, so a batch user who can only create Jobs cannot set it.
[Security, Privacy, and RBAC](#security-privacy-and-rbac) says who can set the boost.
Admins can limit the range of the value with a ValidatingAdmissionPolicy, which is GA since Kubernetes 1.30.

## Design Details

### API

`priorityBoost` is a new optional field in the `Workload` spec:

```go
type WorkloadSpec struct {
	// ...

	// priorityBoost is added to priority to get the effective priority of the Workload.
	// Kueue uses the effective priority to order Workloads and to select preemption candidates.
	// A positive value raises the effective priority and a negative value lowers it.
	// If unspecified, the boost is 0.
	//
	// This is an alpha field and requires enabling PriorityBoost feature gate.
	// +optional
	PriorityBoost *int32 `json:"priorityBoost,omitempty"`
}
```

```yaml
apiVersion: kueue.x-k8s.io/v1beta2
kind: Workload
metadata:
  name: example-workload
  namespace: default
spec:
  priorityClassRef:
    group: kueue.x-k8s.io
    kind: WorkloadPriorityClass
    name: medium
  priority: 200
  priorityBoost: 100
  # podSets omitted
```

- The boost applies to every source of the base priority: a WorkloadPriorityClass, a Kubernetes PriorityClass,
  or no class.
- The field is in the spec because it is desired state that another component changes after creation.
  The [Kubernetes API conventions](https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md#spec-and-status)
  put such properties in the spec.

### Validation

- The API server rejects a value that is not a 32-bit integer.
  There is no other minimum or maximum, as for `spec.priority`.
  A boost must be able to move a Workload above or below any base priority.
  Kueue computes the effective priority as a 64-bit sum, so it cannot overflow.
  A value of 0 has the same effect as an unset field.
- The field is mutable at any time, also on an admitted Workload.
- The API server accepts the field when the `PriorityBoost` feature gate is off.
- Kueue does not read or validate the `kueue.x-k8s.io/priority-boost` annotation.
  It reports nothing for a Workload that still carries the annotation.

### Handling

- For a set value, Kueue uses the effective priority in the comparisons listed in
  [Interaction with other features](#interaction-with-other-features).
- When Kueue preempts a Workload, it records an event on the preempted Workload and on the preemptor.
  Both events give the base priority, the boost and the effective priority of the two Workloads.
  The eviction condition on the preempted Workload does not carry the priorities.
- The visibility API reports the base priority of a pending Workload.
  The position it reports follows the effective priority.
- When the `PriorityBoost` feature gate is off, Kueue uses the base priority.
  The field keeps its value and applies again when an admin turns the gate on.
  MultiKueue copies and updates the field in the remote copy also when the gate is off.
- Kueue builds the spec of a Workload from its Job, and builds it again when a pending Job changes.
  Kueue keeps the boost when it does so.
  If a client changes the boost at the same time, the update from Kueue fails with a conflict.
  Kueue then retries with the new value.
- When Kueue deletes a Workload and creates a new one for the same Job, the new Workload has no boost.

### Re-evaluation after a boost change

A boost change on a pending Workload makes Kueue evaluate that Workload again.
A boost change on an admitted Workload makes Kueue evaluate the inadmissible Workloads of its ClusterQueue again.
When the ClusterQueue is in a cohort, Kueue evaluates the inadmissible Workloads of the whole cohort tree,
because one of them may now be able to preempt it.
Kueue delays each evaluation by one second.
It merges the notifications for the same ClusterQueue or cohort tree that arrive in that time into one evaluation.
Each evaluation clears the scheduling equivalence results of the ClusterQueue.
An external controller that gives many Workloads different boosts also puts them in different classes.

### Derived Workloads

| Derived Workload | Boost at creation | Later change of the boost on the original |
|---|---|---|
| Replacement slice of an elastic job | Kueue copies it from the slice it replaces. | Does not apply. The replaced slice is finished. |
| Variant under Concurrent Admission | Copied from the parent Workload. | Not copied. |
| Remote copy under MultiKueue | Copied from the manager cluster's Workload. | Kueue updates the remote copy in place. |

Kueue creates the replacement slice in the same cluster to continue the same job.
An external controller cannot set the boost before Kueue evaluates the new slice.
So Kueue keeps the boost across that replacement.

A variant is a separate Workload.
An external controller that wants to change the boost of a job under Concurrent Admission sets it on the variants.

Under MultiKueue the manager cluster owns the boost, as it owns the base priority.
The external controller runs in the manager cluster.
The manager cluster replaces a boost that a client sets on a remote copy with the value of its own Workload.
The update in place does not interrupt the job in the worker cluster.

A worker cluster on a release without the field drops it from the remote copy.
The boost then has no effect in that worker cluster.
The manager cluster does not replace a remote copy because the worker cluster dropped the boost.
A manager cluster on a release without the field does not sync it.
That manager copies the annotation, and the worker cluster ignores it.
It can also remove the field from a remote copy when it updates that copy.
Admins upgrade the worker clusters first, then the manager cluster, then the external controller.

### Interaction with other features

| Feature | What the boost changes |
|---|---|
| Queue order in a ClusterQueue | Workloads are ordered by effective priority, then by timestamp. |
| Admission Fair Sharing | LocalQueue usage is compared before priority. The boost only orders Workloads whose LocalQueues have equal usage. |
| Order of a scheduling cycle across a cohort | After Workloads that fit without borrowing, a higher effective priority goes first. This needs the `PrioritySortingWithinCohort` feature gate, which is on by default. |
| Fair Sharing | DRS values do not change. When two ClusterQueues tie on DRS, the Workload with the higher effective priority goes first, under the same feature gate. |
| `withinClusterQueue` and `reclaimWithinCohort` policies | `LowerPriority` compares effective priorities, and so does `LowerOrNewerEqualPriority` of `withinClusterQueue`. Two Workloads with different base priorities and the same effective priority count as equal. |
| `borrowWithinCohort.maxPriorityThreshold` | The threshold is compared with the effective priority of the candidate. |
| Order of preemption candidates | Candidates with a lower effective priority are preempted first, after the existing rules for evicted Workloads, other ClusterQueues and LocalQueue usage. |
| Configurable Preemptions (Alpha) | A priority selector chooses `Boosted` (effective priority) or `Base` (base priority). In Alpha, configured candidates are added after the candidates of the `preemption` policies, and those policies use the effective priority. |
| Scheduling equivalence classes | The effective priority is part of the class. Two Workloads that differ only in boost are in different classes. |
| Change of the base priority | The boost stays, so the effective priority moves by the same amount. |
| Elastic jobs, Concurrent Admission, MultiKueue | See [Derived Workloads](#derived-workloads). |
| Topology Aware Scheduling | No change. |
| WaitForPodsReady | The boost stays on the Workload when Kueue evicts and requeues it. Kueue orders the requeued Workload by effective priority, then by the timestamp of the requeuing strategy. |

### Reference controller

`kueue-priority-booster` is an experimental controller in the Kueue repository.
Admins deploy it separately from kueue-controller-manager.
It sets the `priorityBoost` field.
It implements the time slicing policy of Story 1:

1. While a Workload is admitted for less than `timeSharing.interval`, kueue-priority-booster leaves the boost unset.
2. After that time, it sets the boost to the negative of `timeSharing.negativeBoost`.
3. When the Workload is no longer admitted, it removes the boost.

It removes a boost that another client set on a Workload that it manages.
For a Workload outside its `workloadSelector` or above `maxWorkloadPriority`, it removes a negative boost and keeps a zero or positive boost.
So a boost set by hand works only on a Workload that the reference controller does not manage.

### Security, Privacy, and RBAC

kueue-controller-manager needs no new permission.
An external controller needs `get`, `list`, `watch` and `patch` on `workloads.kueue.x-k8s.io`.
The reference controller also creates events.
Anyone with `create`, `update` or `patch` on `workloads.kueue.x-k8s.io` can set the boost.
The batch-admin role that Kueue ships grants all three.
Admins give these verbs only to trusted users and controllers.

### Test Plan

#### Unit tests

- The effective priority is base priority plus boost, and an unset field is 0.
- Queue order, preemption eligibility and candidate order use the effective priority.
- A boost change is detected for each source of the base priority.
- Preemption events give the base priority, the boost and the effective priority.
- The visibility API orders pending Workloads by effective priority.
- MultiKueue: a boost change in the manager cluster updates the remote copy in place, for each source of the base priority.
- MultiKueue: the manager cluster still replaces a remote copy that is out of sync in another field.
- MultiKueue: the manager cluster does not replace a remote copy when the worker cluster drops the field.
- The reference controller computes the boost for representative Workload states and sets it idempotently.

#### Integration tests

- Two Workloads with the same base priority and different boosts: the preemption order follows the boost.
- A boost that crosses a priority class boundary changes the queue order and protects the Workload under `LowerPriority`.
- `LowerOrNewerEqualPriority`: two Workloads with different base priorities and the same effective priority count as equal.
- `borrowWithinCohort.maxPriorityThreshold`: a boost moves a Workload across the threshold.
- A boost change on an admitted Workload makes a pending Workload preempt it, for a WorkloadPriorityClass,
  a Kubernetes PriorityClass and no class.
- The API server rejects a value that is not a 32-bit integer.
- A Workload that carries only the annotation gets no boost, and Kueue can admit and delete it with any annotation value.
- MultiKueue: the remote copy gets the boost at creation.
  A boost change in the manager cluster updates the remote copy and does not replace it.
- MultiKueue: the manager cluster replaces a boost that a client sets on a remote copy.
- A change of a pending Job that makes Kueue rebuild the Workload spec does not change the boost.
- The replacement slice of an elastic job keeps the boost.
- A Concurrent Admission variant gets the boost of the parent Workload at creation.
- With the `PriorityBoost` feature gate off, Kueue ignores the field.
- Configurable Preemptions: a rule with mode `Base` ignores the boost and a rule with mode `Boosted` uses it.
- The reference controller changes the boost and the preemption follows.

#### e2e tests

None.
The feature does not depend on Pods, the kubelet or kube-controller-manager, so integration tests cover it.

### Graduation Criteria

#### Alpha

- The feature is behind the `PriorityBoost` feature gate, off by default.
- The boost is the `kueue.x-k8s.io/priority-boost` annotation on the Workload, from v0.17 to v0.20.
- Kueue uses the effective priority for queue order, preemption eligibility and candidate order.
- The reference controller is available.

In v0.21, still behind the feature gate and off by default:

- The boost is the `spec.priorityBoost` field, and Kueue does not read or validate the annotation.
- The reference controller sets the field.
- A pending Workload keeps the boost when Kueue rebuilds its spec from a changed Job.
- MultiKueue updates the boost of a remote copy in place for every source of the base priority,
  and still replaces a remote copy that is out of sync in another field.
- A boost on the manager cluster's Workload does not make MultiKueue replace the remote copy in a worker cluster
  that runs a release without the field.
- The docs pages that name the annotation name the field.
- The release notes tell users of the annotation to set the field.

#### Beta

- The feature gate is on by default.
- All reported bugs are fixed.
- A boost change on an admitted Workload re-evaluates inadmissible Workloads for every source of the base priority.
- The replacement slice of an elastic job keeps the boost.
- The integration tests in the Test Plan are in place.
- The docs have a concept page for the boost.
  It states the cohort-wide effect, how to scope it with Configurable Preemptions, and what an external controller
  must do for derived Workloads.
  It has an example ValidatingAdmissionPolicy that limits the range of the boost.
  The satellite-projects page says that the reference controller lowers the boost once, by a fixed amount.

#### GA

- The scoping of the boost through Configurable Preemptions is at least Beta.
  If users need a cohort scope that excludes the ClusterQueue of the preemptor, KEP-13396 is amended first.
- Review the handling of the boost for Concurrent Admission variants when that feature graduates.
- Review whether the eviction condition of a preempted Workload should carry the priorities, with user feedback.
- Review whether the Workload needs a reference to the writer of the boost, with user feedback.
- Review whether the visibility API should report the boost, with user feedback.

## Implementation History

- 2026-01-12: Initial KEP drafted as a "preemption cost" design.
- 2026-02-20: KEP rewritten to the "priority boost" approach after feedback in #7990 and PR #8551.
- 2026-03-31: Alpha in v0.17.
- 2026-11: KEP updated. The boost moves from the annotation to a field in v0.21, and the Beta criteria are added.

## Drawbacks

- One more signal makes scheduling and preemption harder to reason about when it is set incorrectly.
- A boost can cross priority class boundaries, which surprises users who expect strict separation of classes.

## Alternatives

### Keep the annotation

The Alpha annotation could stay as the API.
It does not, because the [Kubernetes API conventions](https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md#label-selector-and-annotation-conventions)
deprecate annotations as a representation of API fields.
They keep annotations for extensions that the controller of the resource does not read.
kueue-controller-manager reads the boost.

Kueue could also graduate with the annotation and move to a field in a later release.
That move would then break every user of a feature that is on by default.
Now it breaks only users who turned on the feature gate.
The Kubernetes API change guide gives the same reason against annotations:
an annotation cannot move to a field in the same API version without two representations of one value.

An earlier version of this KEP kept the boost outside the spec because Kueue builds the spec from the Job.
[Handling](#handling) says how Kueue keeps the boost when it builds the spec again.

An annotation is also untyped.
Currently the Workload validating webhook of kueue-controller-manager parses the value on every update, including status updates.
A Workload that stored an invalid value while the feature gate was off then rejects every later update.
Kueue can neither admit it nor finish its deletion.
Ratcheting validation in the webhook could fix this for the annotation.
A typed field needs no validation code in Kueue.

### Read the annotation as a fallback next to the field

Kueue could use the annotation when the field is unset, for one release.
This is not done, because one property then has two representations in the same API version.
A client cannot tell which one Kueue uses.
The annotation is Alpha, so the field replaces it.

### A status field

The boost could be a field in the Workload status.
It stays in the spec, because the boost is desired state that a client sets, as `spec.priority` is.
It is not a value that Kueue computes.
The batch-admin role also cannot update the status subresource.

### A reference to the writer of the boost

The Workload could record which controller owns the boost.
`status.effectiveQuotas.orchestratorRef` does this for Dynamic Quota Orchestration
([KEP-12382](../12382-dynamic-quota-orchestration/README.md)).
There, the reference names a `DynamicQuotaOrchestrator` object, and a second orchestrator that reads the reference does not write.
The boost has no such object, because a person or any external controller can set it.
A reference would need a new API kind for writers, or a name that Kueue cannot check.
The need does not depend on where the boost is stored.
Two writers can overwrite each other in the annotation and in the field.
It is not added now.
A later reference can be a separate field next to `priorityBoost`, so the integer does not change.
A GA criterion reviews it.

### A preemption-cost annotation that only orders preemption candidates

The first design in this KEP proposed a `kueue.x-k8s.io/preemption-cost` annotation that changed only the order of candidates.
It was rejected for two reasons.
A preempted mid-priority Workload could at once preempt the low-priority Workload with the high cost,
because eligibility still used the static priorities.
Starvation prevention needs to cross priority class boundaries, which candidate ordering cannot do.

The Alpha KEP left open whether to add such a mechanism next to the boost.
It is not added.
A Configurable Preemptions rule with priority mode `Base` gives preemption that ignores the boost.

### Change the priority class of the Workload

Rejected for the reasons in [Motivation](#motivation).

### A cost or boost policy inside Kueue

Rejected, to keep Kueue neutral about policy and to let external controllers express organization-specific logic.

### A boost field with a strategy

[#13414](https://github.com/kubernetes-sigs/kueue/issues/13414) proposed `spec.priorityBoost` as a struct with a field that says when the boost applies,
for example only when the preempted Workloads are in the same ClusterQueue.
It was closed in favor of Configurable Preemptions, which approximates this scoping per ClusterQueue.
Its cohort rule also applies within the same ClusterQueue.
So the field is a plain integer.

### A maximum boost on the ClusterQueue

A `maxPriorityBoost` limit would protect against a runaway external controller.
It is not added.
No user asked for it, RBAC on `Workload` already limits who can set the boost,
and a ValidatingAdmissionPolicy can limit the range.

### A separate boost in each MultiKueue worker cluster

MultiKueue could leave the boost of a remote copy out of the sync, as it does not copy preemption gates from the manager cluster.
An external controller in each worker cluster would then own it.
This is not done, because the base priority already comes from the manager cluster.
One external controller in the manager cluster is also simpler to run.
A spec field is also part of what MultiKueue compares between the two Workloads.

### Kueue copies a later boost change to Concurrent Admission variants

Kueue could sync a later boost change from a parent Workload to its variants.
It is not done for Beta.
The question is left to the graduation of that feature.

### Prevent repeated preemption inside Kueue

Kueue could refuse to preempt a Workload that was admitted a short time ago.
It is not part of this KEP.
[Two Workloads can preempt each other repeatedly](#two-workloads-can-preempt-each-other-repeatedly) describes the related work on preemption limits.

### A reference controller that raises the boost after each preemption

The Alpha KEP described a reference controller that adds a bounded increment for every N preemptions of a Workload.
It could also combine the job phase, the checkpoint status and the crash count into one boost.
It was not implemented.
The reference controller implements time slicing with a negative boost.
The idea stays possible for an external controller, as in Story 1.
