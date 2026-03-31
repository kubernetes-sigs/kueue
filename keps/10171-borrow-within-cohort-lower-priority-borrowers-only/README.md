# KEP-10171: borrowWithinCohort `LowerPriorityBorrowersOnly` policy

<!--
This KEP extends [KEP-1337](https://github.com/kubernetes-sigs/kueue/tree/main/keps/1337-preempt-within-cohort-while-borrowing)
(`borrowWithinCohort`) with an additional policy that protects other tenants’
nominal quota when a workload preempts while borrowing. It accompanies
[Issue #10171](https://github.com/kubernetes-sigs/kueue/issues/10171).
-->

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User stories](#user-stories)
  - [Risks and mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [Relationship to KEP-1337](#relationship-to-kep-1337)
  - [API](#api)
  - [Semantics](#semantics)
  - [Implementation sketch](#implementation-sketch)
  - [Fair Sharing](#fair-sharing)
  - [Test Plan](#test-plan)
    - [Unit tests](#unit-tests)
    - [Integration tests](#integration-tests)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
<!-- /toc -->

## Summary

This KEP adds a new value `LowerPriorityBorrowersOnly` for
`.spec.preemption.borrowWithinCohort.policy` on `ClusterQueue` (classical
preemption only). It sits between `Never` and `LowerPriority`:

- With **`LowerPriority`**, a borrowing preemptor may evict **any** eligible
  lower-priority workload in another queue in the cohort, including workloads
  whose resources are **entirely backed by that queue’s nominal quota**.
- With **`LowerPriorityBorrowersOnly`**, a workload in another `ClusterQueue`
  is a preemption target **only if** removing it would leave that queue **at or
  above nominal** for every **contested** flavor-resource involved in the
  preemption decision. Intuitively: the preemptor may reclaim **borrowed**
  capacity from other queues, not capacity that is still **guaranteed** to the
  victim queue under nominal quota.

`maxPriorityThreshold` continues to apply: a candidate must satisfy the priority
and threshold constraints **and** the nominal-remainder rule above.

## Motivation

In multi-tenant clusters, teams are often given **nominal quota** as a hard
allocation. Borrowing from a cohort is understood as **best-effort** relative
to that guarantee. Today, `borrowWithinCohort.policy: LowerPriority` does not
distinguish borrowed usage from usage within nominal on the **victim**
`ClusterQueue`. A high-priority borrowing workload can therefore preempt
lower-priority workloads that are not “using borrowed headroom” in a precise
sense—e.g. preemptions that would push the victim **below** nominal on a
resource if the preempted workload were removed.

Operators need a middle ground:

- **`Never`**: no cross-queue preemption while borrowing (too strict for some).
- **`LowerPriority`**: unrestricted (among priorities) preemption of lower
  priority workloads (too aggressive for nominal guarantees).
- **`LowerPriorityBorrowersOnly`**: allow reclaiming capacity that keeps the
  victim at or above nominal after the removal, aligning preemption with the
  idea that nominal quota should remain protected.

### Goals

- Add `LowerPriorityBorrowersOnly` to `BorrowWithinCohortPolicy`.
- Define clear semantics for **per-candidate** validation against **nominal**
  quota for **contested** flavor-resources (resources for which preemption is
  being considered).
- Preserve composition with `maxPriorityThreshold`.
- Keep behavior scoped to **classical preemption**; do not extend
  `borrowWithinCohort` to Fair Sharing (unchanged from KEP-1337).

### Non-Goals

- Changing `reclaimWithinCohort`, `withinClusterQueue`, or hierarchical reclaim
  rules beyond what follows naturally from the new `borrowWithinCohort`
  policy.
- Introducing new Fair Sharing behavior for `borrowWithinCohort`.
- Defining a different notion of “borrowing” than usage vs nominal already used
  in the scheduler cache (see [Semantics](#semantics)).

## Proposal

Extend `BorrowWithinCohortPolicy` with one new enum value and document it in the
API. No new fields are required on `BorrowWithinCohort`.

### User stories

1. **Tenant B** has nominal 10 GPUs and is using 13 (borrowed 3). **Tenant A**
   submits a borrowing workload that needs to reclaim GPUs. Under
   `LowerPriorityBorrowersOnly`, a workload in B that uses **2** GPUs may be
   preempted (13 − 2 = 11 ≥ 10), but a workload that uses **5** GPUs may not
   (13 − 5 = 8 &lt; 10).
2. **Tenant C** is **not** borrowing (usage ≤ nominal on contested resources).
   No workloads in C are considered for this preemption path when the cohort
   logic already restricts to over-nominal queues (existing behavior);
   `LowerPriorityBorrowersOnly` then adds the per-workload remainder check for
   queues that *are* over nominal.

### Risks and mitigations

| Risk | Mitigation |
|------|------------|
| Harder to schedule if fewer candidates qualify | Opt-in policy; clusters keep `LowerPriority` if they prefer the old behavior. |
| Misunderstanding “contested” resources | Document that the check applies per flavor-resource in the set needing preemption for the incoming assignment. |
| Interaction with multi-resource workloads | Require **all** contested flavor-resources to remain at or above nominal after subtracting the candidate’s usage on each. |

## Design Details

### Relationship to KEP-1337

[KEP-1337](/keps/1337-preempt-within-cohort-while-borrowing) introduced
`borrowWithinCohort` with `Never` and `LowerPriority`. This KEP **only** adds an
enum value and refines which workloads are eligible when that policy is
selected. Webhook rules (e.g. `reclaimWithinCohort` must not be `Never` when
`borrowWithinCohort` is enabled) stay the same.

### API

```go
type BorrowWithinCohortPolicy string

const (
	BorrowWithinCohortPolicyNever                      BorrowWithinCohortPolicy = "Never"
	BorrowWithinCohortPolicyLowerPriority              BorrowWithinCohortPolicy = "LowerPriority"
	BorrowWithinCohortPolicyLowerPriorityBorrowersOnly BorrowWithinCohortPolicy = "LowerPriorityBorrowersOnly"
)

type BorrowWithinCohort struct {
	// policy ...
	// - `LowerPriorityBorrowersOnly`: like `LowerPriority`, but a workload in
	//    another ClusterQueue is a valid target only if that ClusterQueue is
	//    borrowing for the contested resources and removing the target workload
	//    would not reduce that ClusterQueue's usage below its nominal quota
	//    for those resources.
	//
	// +kubebuilder:validation:Enum=Never;LowerPriority;LowerPriorityBorrowersOnly
	Policy BorrowWithinCohortPolicy `json:"policy,omitempty"`
	// maxPriorityThreshold unchanged
	MaxPriorityThreshold *int32 `json:"maxPriorityThreshold,omitempty"`
}
```

(CRD and both API versions `v1beta1` / `v1beta2` gain the same enum extension.)

### Semantics

Let:

- `frs` = set of `(resource flavor, resource name)` pairs that need preemption
  for the incoming workload’s assignment.
- For a **candidate** workload `W` in **victim** `ClusterQueue` `Q`:
  - `usage_Q(fr)` = current usage of `Q` for `fr`.
  - `nominal_Q(fr)` = nominal quota of `Q` for `fr`.
  - `usage_W(fr)` = usage of `W` for `fr` (from admitted assignment).

**`LowerPriorityBorrowersOnly`** (in addition to KEP-1337 priority / threshold
rules): `W` is **not** a valid target if there exists `fr ∈ frs` such that

```text
usage_Q(fr) − usage_W(fr) < nominal_Q(fr)
```

Equivalently, for all `fr ∈ frs`:

```text
usage_Q(fr) − usage_W(fr) ≥ nominal_Q(fr)
```

If `W` is invalid under this rule, it must **not** be selected as a preemption
target for this preemptor (including when the scheduler tries “fit without
borrowing” passes), so that nominal protection is not bypassed by a later
attempt.

**Note:** Queues that are not over nominal for `frs` are already excluded from
the usual cohort candidate set; this policy tightens **which workloads** in an
already-over-nominal queue can be preempted.

### Implementation sketch

1. When classifying a cross-queue candidate for “reclaim while borrowing” for
   the preemptor’s `ClusterQueue`, if
   `borrowWithinCohort.policy == LowerPriorityBorrowersOnly`, compute the
   candidate’s flavor-resource usage and compare post-removal usage to nominal
   for each `fr` in `frs`. If any check fails, treat the candidate as
   **ineligible** (do not preempt it).
2. Helper on the victim `ClusterQueue` snapshot (conceptually): given a map of
   usage to subtract and `frs`, return whether
   `usage[fr] - subtract[fr] >= nominal[fr]` for all `fr ∈ frs`.
3. **`IsBorrowingWithinCohortForbidden`**: unchanged—`LowerPriorityBorrowersOnly`
   is not `Never`, enabling borrow-within-cohort preemption like
   `LowerPriority`.

### Fair Sharing

Unchanged: `borrowWithinCohort` applies only to **classical** preemption, not
Fair Sharing (per KEP-1337).

### Test Plan

[x] Owners of involved components may require test updates before merge.

#### Unit tests

- `pkg/scheduler/preemption`: cohort with victim over nominal; multiple victim
  workloads; assert `LowerPriorityBorrowersOnly` preempts only workloads whose
  removal keeps victim ≥ nominal; assert `LowerPriority` can still preempt a
  larger workload that fails the remainder check.
- Cover interaction with `maxPriorityThreshold` (optional dedicated case).

#### Integration tests

- Webhook / API: create `ClusterQueue` with
  `borrowWithinCohort.policy: LowerPriorityBorrowersOnly` and valid
  `reclaimWithinCohort` (not `Never`); admission succeeds.
- Scheduler integration (optional): end-to-end scenario matching user story (1).

### Graduation Criteria

**Alpha**

- API and implementation merged behind normal API review.
- Documented in ClusterQueue / preemption docs.

**Beta / GA**

- Follow subproject conventions for CRD stability; address user feedback and
  bugs.

## Implementation History

- 2026-03-30: KEP drafted (Issue [#10171](https://github.com/kubernetes-sigs/kueue/issues/10171)).

## Drawbacks

- Slightly more complex preemption and API surface.
- Schedules may fail more often when nominal protection removes large
  victims from the candidate set; operators must tune policies and quotas.

## Alternatives

1. **Separate boolean** `protectVictimNominal: true` alongside `LowerPriority`
   instead of a new enum value—rejected as harder to read and combinatorial with
   future policies.
2. **`OnlyBorrowedUsage` naming**—rejected in favor of the issue’s name
   `LowerPriorityBorrowersOnly` for consistency with `LowerPriority`.
3. **Global cluster policy** instead of `ClusterQueue`—rejected; KEP-1337 is
   already per–ClusterQueue for the preemptor.
