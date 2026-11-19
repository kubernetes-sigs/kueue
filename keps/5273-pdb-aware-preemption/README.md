# KEP-5273: Respect PodDisruptionBudgets when preempting Pod-integration Workloads

<!-- toc -->
- [Summary](#summary)
  - [Terminology](#terminology)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1: serving tenants sharing a cohort](#story-1-serving-tenants-sharing-a-cohort)
    - [Story 2: a borrower with no PDB](#story-2-a-borrower-with-no-pdb)
    - [Story 3: a victim whose budget is used up](#story-3-a-victim-whose-budget-is-used-up)
  - [Notes/Constraints/Caveats](#notesconstraintscaveats)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [Today's behavior](#todays-behavior)
  - [Overview](#overview)
  - [API](#api)
    - [Configuration](#configuration)
    - [Validation](#validation)
    - [Handling](#handling)
    - [Workload condition](#workload-condition)
  - [Pod integration: evict instead of delete](#pod-integration-evict-instead-of-delete)
  - [Retrying a refused eviction](#retrying-a-refused-eviction)
  - [Timeout and fallback to deletion](#timeout-and-fallback-to-deletion)
  - [Scheduler: re-selecting victims](#scheduler-re-selecting-victims)
  - [Pod groups](#pod-groups)
  - [Interaction with other features](#interaction-with-other-features)
  - [Metrics](#metrics)
  - [Test Plan](#test-plan)
    - [Unit tests](#unit-tests)
    - [Integration tests](#integration-tests)
    - [e2e tests](#e2e-tests)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
<!-- /toc -->

## Summary

When Kueue preempts a Workload managed by the Pod integration (plain Pods, pod
groups, and the Deployment, StatefulSet and LeaderWorkerSet integrations built on
it), it deletes the Pods directly. PodDisruptionBudgets (PDBs) are never
consulted.

This KEP adds an opt-in mode in which the Pod integration stops a preempted
Workload through the Pod `eviction` subresource, so PDBs are honored on a
best-effort basis:

1. A refused eviction is retried.
2. While a victim's eviction is refused, the scheduler may pick another victim
   whose budget allows the disruption, and withdraw the preemption of the
   blocked one.
3. A configurable timeout bounds how long any eviction can be refused. After it
   expires, Kueue deletes the Pods as it does today, so a PDB can delay a
   preemption but can never prevent it.

### Terminology

- **Victim:** a Workload selected for preemption, so that a pending Workload
  (the preemptor) can be admitted. Kueue code calls these preemption targets.
- **Stop policy:** how the Pod integration stops the Pods of a victim: `Delete`
  (today) or `Evict` (through the eviction subresource).
- **Blocked victim:** a victim with the `Evict` stop policy whose Pods the API
  server refused to evict, and which therefore has `DisruptionBlocked=True`.
- **Withdraw:** to cancel the preemption of a blocked victim, by clearing its
  `Evicted` condition, so it keeps running with its admission.
- **Scheduler** and **job reconciler:** both run in kueue-controller-manager.
  The scheduler selects victims and marks them `Evicted`; the job reconciler of
  the Pod integration stops their Pods.

## Motivation

Kueue's preemption is increasingly used for long-running serving workloads, not
only batch. A common setup is a cohort shared by several tenants: each
ClusterQueue has a guaranteed nominal quota, lends what it does not use, and
reclaims it with `reclaimWithinCohort` when its own demand returns. Serving
tenants express their disruption tolerance with PDBs, and every other voluntary
disruption in the cluster (node drains, upgrades, the cluster autoscaler)
respects them. Kueue preemption is the exception: it can take a Deployment below
its declared `minAvailable` in one scheduling cycle.

Issue [#5273](https://github.com/kubernetes-sigs/kueue/issues/5273) asks for PDBs
to be respected. The concern raised there is valid: if a PDB could block
preemption outright, a tenant could keep borrowed quota forever, or a
low-priority Workload could block a high-priority one, just by writing a strict
PDB. Quota and priority are set by the administrator; PDBs are written by the
tenant being preempted, and must not be able to override the former.

This proposal resolves that tension the same way kube-scheduler does: PDBs shape
which victims are chosen and how fast they are disrupted, and a bounded timeout
guarantees that preemption always completes.

### Goals

- Stop Pod-integration Workloads evicted by preemption through the Pod eviction
  subresource, so PDBs are honored, behind a feature gate and a configuration
  option.
- Never let a PDB block a preemption indefinitely: bound every refusal with a
  configurable timeout, then delete as today.
- When a victim's PDB refuses, let the scheduler preempt a different eligible
  victim instead of waiting, so honoring PDBs does not slow reclaim when other
  victims can be disrupted.
- Keep today's behavior unchanged when the feature is disabled.

### Non-Goals

- Job-based integrations (batch/Job, JobSet, RayJob, Kubeflow jobs, and so on).
  Kueue stops those by suspending the job, and their own controllers remove the
  Pods.
- Evictions for reasons other than preemption (deactivation, PodsReady timeout,
  admission checks, a stopped ClusterQueue, node failures, Workload deletion).
  These keep deleting Pods.
- Strict PDB enforcement, where a PDB can prevent a preemption forever.
- Topology- or node-aware victim selection.

## Proposal

Add a feature gate `PreemptionRespectsPodDisruptionBudgets` (alpha, disabled by
default) and a configuration option to choose how the Pod integration stops a
preempted Workload: `Delete` (today's behavior and the default) or `Evict`.

With `Evict`:

1. The Pod integration calls the eviction subresource instead of deleting.
2. If the API server refuses (HTTP 429, a PDB has no disruptions left), Kueue
   records a `DisruptionBlocked` condition on the Workload and retries with
   backoff.
3. On its next cycle, the scheduler treats a blocked victim as not progressing.
   If other eligible victims can cover the preemptor's need, it preempts them and
   withdraws the preemption of the blocked victim, which keeps its admission.
4. If a victim stays blocked for longer than `evictionTimeout`, Kueue deletes its
   Pods, as today.

### User Stories

All three stories assume the administrator has opted in. Both settings are off
by default: the feature gate is disabled, and `stopPolicy` defaults to `Delete`.

```yaml
apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
featureGates:
  PreemptionRespectsPodDisruptionBudgets: true # default: false
integrations:
  frameworks:
  - pod
  - deployment
  podPreemption:
    stopPolicy: Evict      # default: Delete
    evictionTimeout: 10m   # default when unset: 10m
```

#### Story 1: serving tenants sharing a cohort

Two serving tenants share a cohort. Tenant A lends idle capacity overnight;
tenant B borrows it. In the morning A's Deployments scale up and reclaim. B runs
many Deployments, each with a PDB allowing one Pod to be unavailable:

```yaml
apiVersion: policy/v1
kind: PodDisruptionBudget
metadata:
  name: web
  namespace: tenant-b
spec:
  maxUnavailable: 1
  selector:
    matchLabels:
      app: web
```

Today Kueue deletes as many of B's Pods as A needs at once, possibly most of one
Deployment. With the configuration above, B gives back the same capacity spread
across its Deployments, each within its budget, and A is not delayed because the
scheduler moves to the next victim whenever a budget refuses.

#### Story 2: a borrower with no PDB

A training tenant borrows idle capacity and has no PDBs. With the configuration
above, the eviction subresource behaves like deletion for Pods not covered by a
PDB, so preemption of this tenant is unchanged.

#### Story 3: a victim whose budget is used up

With the configuration above, every candidate's PDB currently has no
disruptions left, for example a PDB like the one in Story 1 on a Deployment
where one replica is already unready. Kueue retries for `evictionTimeout` (10
minutes here), then deletes, as today. The preemptor waited at most
`evictionTimeout`.

### Notes/Constraints/Caveats

- PDBs are evaluated by the API server at eviction time. Kueue does not compute
  budgets itself in alpha (see [Alternatives](#alternatives)).
- Only the `Preempted` eviction reason uses the eviction subresource. A Workload
  evicted for any other reason, or deleted, keeps today's deletion path.
- A Pod's quota is still released only when the Pod stops being active
  (`Pod.IsActive`). A refused eviction leaves the Pod running and its quota
  reserved, so a refusal never leads to over-admission.

### Risks and Mitigations

| Risk | Mitigation |
|---|---|
| A tenant uses a strict PDB to keep borrowed quota. | `evictionTimeout` always applies; a refusal can only delay preemption. |
| Over-preemption: the scheduler picks a replacement victim, then the blocked victim's eviction succeeds anyway. | The blocked victim's preemption is withdrawn (Evicted cleared) before or together with issuing the replacement, so the reconciler stops retrying it. The remaining race (a retry in flight when the withdrawal lands) can disrupt at most one extra Pod per withdrawn victim; documented, and counted by a metric. |
| Ping-pong: a withdrawn victim is re-selected every cycle. | Candidates that were blocked within the last `evictionTimeout` are ordered after other candidates of the same priority. |
| Slower reclaim when every candidate is blocked. | Bounded by `evictionTimeout`; the default (10 minutes) is chosen to cover a typical rolling restart, and is configurable. |
| More API calls (retries). | Retries use exponential backoff capped at 30 seconds per Workload. |

## Design Details

### Today's behavior

1. `Preemptor.IssuePreemptions` (`pkg/scheduler/preemption/preemption.go`) calls
   `workloadevict.Evict`, which sets the `Evicted` condition with reason
   `Preempted`. Victims already marked `Evicted` are reported as "Preemption
   ongoing" and counted as successfully preempted.
2. The job reconciler (`pkg/controller/jobframework/reconciler.go`,
   `reconcileEvicted`) calls `stopJob` with reason `StopReasonWorkloadEvicted`.
   For a `ComposableJob`, `stopJob` appends the `Evicted` condition's reason and
   calls `Stop` with `WorkloadEvictedDueToPreempted`.
3. `Pod.Stop` (`pkg/controller/jobs/pod/pod_controller.go`) sets the
   `TerminationTarget` Pod condition and deletes the Pod.
4. The reconciler clears the Workload's quota reservation once
   `job.IsActive()` is false.
5. `CandidatesOrdering` (`pkg/scheduler/preemption/common/ordering.go`) orders
   already-evicted Workloads first, so a victim that never stops keeps being
   re-selected and the preemptor waits.

With deletion, step 5 never stalls. With eviction and no further change it
would stall for as long as the PDB refuses. The scheduler change below is what
prevents that.

### Overview

```
scheduler                         job reconciler (Pod integration)
---------                         --------------------------------
Evict(victim, Preempted)  ----->  Stop(): POST pods/eviction
                                    ok   -> Pod terminates, quota released (as today)
                                    429  -> set DisruptionBlocked=True, requeue (backoff)
next cycle:                                 after evictionTimeout -> Delete (as today)
  victim Evicted && DisruptionBlocked?
    other victims can cover need ->
      withdraw victim (clear Evicted), evict others
    none can -> keep waiting (timeout will delete)
```

### API

#### Configuration

A new optional field in `Configuration.Integrations` (v1beta2):

```go
type Integrations struct {
	// ...existing fields...

	// podPreemption configures how the Pod integration stops Pods of a
	// Workload that was evicted by preemption. Requires the
	// PreemptionRespectsPodDisruptionBudgets feature gate.
	// +optional
	PodPreemption *PodPreemption `json:"podPreemption,omitempty"`
}

type PodPreemptionStopPolicy string

const (
	// PodPreemptionStopPolicyDelete deletes the Pods. This is the default
	// and today's behavior.
	PodPreemptionStopPolicyDelete PodPreemptionStopPolicy = "Delete"
	// PodPreemptionStopPolicyEvict stops the Pods through the eviction
	// subresource, so PodDisruptionBudgets are honored until evictionTimeout.
	PodPreemptionStopPolicyEvict PodPreemptionStopPolicy = "Evict"
)

type PodPreemption struct {
	// stopPolicy is Delete (default) or Evict.
	// +optional
	StopPolicy PodPreemptionStopPolicy `json:"stopPolicy,omitempty"`

	// evictionTimeout bounds how long Kueue keeps retrying an eviction
	// refused by a PodDisruptionBudget before deleting the Pods.
	// If unset, the timeout is 10m. If set, it must be greater than 0.
	// +optional
	EvictionTimeout *metav1.Duration `json:"evictionTimeout,omitempty"`
}
```

A per-ClusterQueue override (`spec.preemption.evictionTimeout`) is deferred to
beta, pending feedback on whether a global value is enough.

#### Validation

Configuration validation runs when kueue-controller-manager starts, like the
other `Configuration` fields. An invalid configuration stops the manager from
starting, with an error naming the field.

- `stopPolicy` must be `Delete`, `Evict`, or unset (which means `Delete`). Any
  other value is rejected as not supported.
- `evictionTimeout`, if set, must be greater than 0.
- `stopPolicy: Evict` while the `PreemptionRespectsPodDisruptionBudgets` gate is
  disabled is rejected as forbidden ("requires the
  PreemptionRespectsPodDisruptionBudgets feature gate"), following the
  existing pattern for `waitForPodsReady.unscheduledTimeout`. An unset
  `podPreemption`, or `stopPolicy: Delete`, is accepted with the gate disabled
  and has no effect.

#### Handling

- **Mutability:** `podPreemption` is read at startup. Changing it, or the feature
  gate, takes effect when kueue-controller-manager restarts.
- **Gate or policy turned off while Workloads are blocked:** after the restart,
  a Workload with `DisruptionBlocked=True` is handled with the `Delete` policy.
  Its Pods are deleted on the next reconcile, as today, and the condition is set
  to `False` with reason `Deleted`. Users see the Pods terminate and a `Stopped`
  event, as with any preemption today.
- **Gate turned on:** only preemptions handled after the restart use `Evict`.
  Pods that were already being deleted are not affected.

#### Workload condition

A new Workload condition type:

```go
const (
	// WorkloadDisruptionBlocked means that stopping the Workload's Pods after
	// a preemption was refused by a PodDisruptionBudget. LastTransitionTime is
	// when the refusals started; the message names the refusing budgets.
	WorkloadDisruptionBlocked = "DisruptionBlocked"
)
```

The condition has three states, and it is never removed once set:

| State | Meaning | Set when |
|---|---|---|
| absent | No eviction of this Workload has been refused. | Default. |
| `True` (reason `EvictionRefused` or `EvictionFailed`) | The Workload is a blocked victim. `LastTransitionTime` is when the current run of refusals started. | The first refusal or persistent eviction error of a preemption (see [Pod integration](#pod-integration-evict-instead-of-delete)). |
| `False` (reason `Evicted`, `Withdrawn` or `Deleted`) | The Workload is no longer blocked. `LastTransitionTime` is when the block ended. | Its Pods were evicted after all, its preemption was withdrawn, or its Pods were deleted (after `evictionTimeout`, or because the policy is no longer `Evict`). |

How the rest of the design reads it:

- `evictionTimedOut` only looks at `True`: the timeout expires at
  `LastTransitionTime + evictionTimeout`. Absent and `False` never time out.
- `CandidatesOrdering` sorts a candidate later when the condition is `False`
  and its `LastTransitionTime` is within the last `evictionTimeout`, so a
  recently withdrawn victim is not picked again on the next cycle. This is why
  the condition is kept as `False` rather than removed.
- If a victim is selected again and refused again, the condition goes from
  `False` to `True`, which sets a new `LastTransitionTime`. The timeout starts
  over for the new preemption instead of using an old, already expired one.

### Pod integration: evict instead of delete

In `Pod.Stop`, when the gate is enabled, the policy is `Evict`, and the stop
reason is `WorkloadEvictedDueToPreempted`:

```go
// Delete the pod if it is not already deleted.
if err == nil {
	if p.shouldEvict(stopReason) && !p.evictionTimedOut(wl) {
		err := c.SubResource("eviction").Create(ctx, podInGroup.Object(),
			&policyv1.Eviction{ObjectMeta: metav1.ObjectMeta{
				Name: podInGroup.pod.Name, Namespace: podInGroup.pod.Namespace}})
		switch {
		case err == nil, apierrors.IsNotFound(err):
			// evicted, or already gone
		case isDisruptionBudgetRefusal(err):
			refused = append(refused, podInGroup.Object())
			continue // not stopped; reported to the reconciler below
		case isTransient(err):
			return stoppedNow, err // ordinary requeue, no timeout
		default:
			failed = append(failed, podInGroup.Object())
			continue // not stopped; counted against evictionTimeout
		}
	} else if err := c.Delete(ctx, podInGroup.Object()); client.IgnoreNotFound(err) != nil {
		return stoppedNow, err
	}
	// ...finalizer handling unchanged...
}
```

The `TerminationTarget` condition is still applied before the eviction, as
today.

Eviction errors are classified from the API server's response:

| Response | Classified as | Handling |
|---|---|---|
| 429 with a status cause of type `DisruptionBudget` | PDB refusal | `DisruptionBlocked=True`, reason `EvictionRefused`; retried until `evictionTimeout`. |
| 429 without that cause (API Priority and Fairness throttling), server timeout, 503, conflict | Transient | Returned as an error; the reconciler retries with controller-runtime's usual backoff. The timeout clock does not start. |
| Any other error, for example 500 "more than one PodDisruptionBudget" for overlapping PDBs, or 403 for a PDB with invalid status | Persistent failure | `DisruptionBlocked=True`, reason `EvictionFailed`, with the API message; retried until `evictionTimeout`, then deleted like a refusal. |

This keeps throttling from being reported as a PDB refusal, and keeps an
eviction that can never succeed (such as overlapping PDBs) from being retried
forever: it is bounded by `evictionTimeout` like a refusal.

### Retrying a refused eviction

`Stop` reports refused and failed Pods to the reconciler through a typed error
(`jobframework.ErrDisruptionBlocked`, carrying the Pods and the messages from
the API responses). On that error, the reconciler's eviction step:

1. sets `DisruptionBlocked=True` on the Workload, only if it is not already
   `True`, so `LastTransitionTime` marks when the current run of refusals
   started,
2. computes the next retry delay from that `LastTransitionTime` and requeues
   the Workload after it,
3. does not clear the quota reservation, because `job.IsActive()` is still true.

The retry state is stored only in the Workload's status, as the
`DisruptionBlocked` condition's `LastTransitionTime`. No attempt counter is
kept, in memory or in the API. The delay is a function of how long the Workload
has been blocked: retries happen about 1, 3, 7, 15 and 31 seconds after the
first refusal (each wait doubling from 1 second), then every 30 seconds. The
requeue delay itself carries no state; it only schedules the next attempt.

Because the state is in the API, it survives a restart or leader change of
kueue-controller-manager. The new leader reconciles the Workload on its
initial sync, retries the eviction right away, and continues the schedule from
the stored `LastTransitionTime`. The timeout is not reset by a restart.

### Timeout and fallback to deletion

`evictionTimedOut` is true when `DisruptionBlocked` has been `True` for longer
than `evictionTimeout`. `Stop` then deletes the Pods, exactly as today, and
emits an event and a metric. The timeout is measured from the first refusal,
not from the preemption, so a victim that is evicted normally is never affected.

### Scheduler: re-selecting victims

Without this, a refused victim stalls its preemptor until the timeout, which
would be strictly worse than today. Two changes:

1. **Blocked victims do not count as progress.** In `IssuePreemptions`, a target
   that is `Evicted` and `DisruptionBlocked` is not counted as successfully
   preempted. During target selection, its resources are not treated as about
   to be freed.
2. **Withdraw and replace.** When the preemptor's need can be covered by other
   eligible candidates (the existing target-selection logic, run with blocked
   victims excluded), the scheduler withdraws the preemption of each blocked
   victim it no longer needs, then evicts the replacements. Withdrawing clears
   the `Evicted` condition and sets `DisruptionBlocked` to `False` with reason
   `Withdrawn`; because admission was never cleared, the Workload simply
   continues to run. If no replacement set exists,
   blocked victims stay evicted and the timeout eventually deletes them.

Withdrawal is only allowed when none of the victim's Pods have started
terminating (always true for a single-Pod Workload whose eviction was refused).

To avoid re-selecting a withdrawn victim on the next cycle, `CandidatesOrdering`
gains a key after the existing "already evicted" and "other ClusterQueue" keys:
candidates whose `DisruptionBlocked` condition transitioned within the last
`evictionTimeout` sort after other candidates of the same priority (see
[Workload condition](#workload-condition)).

This gives the round-robin behavior from Story 1: each refusal moves the
scheduler to the next eligible victim, so disruption spreads across the
borrower's Workloads and each PDB paces its own Workload.

### Pod groups

A pod group is one Workload. Kueue evicts all of its Pods; if any are refused,
the Workload is `DisruptionBlocked`. If some Pods were evicted and others
refused, the group cannot be withdrawn (it is already partially stopped); the
remaining Pods are retried and deleted at the timeout. Groups that are not
partially stopped can be withdrawn like single Pods.

### Interaction with other features

- **FastQuotaReleaseInPodIntegration:** unchanged. Quota is released once
  evicted Pods have a `DeletionTimestamp`, which a successful eviction sets.
- **Fair sharing and admission fair sharing:** victim selection is unchanged
  except for blocked victims being skipped.
- **MultiKueue:** preemption on a worker cluster is handled by the worker's
  Kueue; the manager side is unaffected.
- **WaitForPodsReady:** evictions for PodsReady timeout use deletion, as today.

### Metrics

- `kueue_pod_eviction_refused_total{cluster_queue}`: refused eviction attempts.
- `kueue_preemption_eviction_timeouts_total{cluster_queue}`: Workloads deleted
  after `evictionTimeout`.
- `kueue_preemption_withdrawn_total{cluster_queue}`: preemptions withdrawn in
  favor of another victim.
- `kueue_preemption_stop_duration_seconds{cluster_queue,stop_policy}`: time from
  `Evicted` to the Workload releasing quota.

### Test Plan

[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.

#### Unit tests

- `pkg/controller/jobs/pod`: `Stop` evicts instead of deleting only for the
  preemption reason with the gate and policy enabled; reports refused Pods;
  deletes after the timeout; deletes for every other stop reason.
- `pkg/controller/jobframework`: the reconciler sets `DisruptionBlocked` once,
  requeues with capped backoff, never clears admission while blocked.
- `pkg/scheduler/preemption`: blocked victims are not counted as progress; a
  replacement set is chosen when one exists and blocked victims are withdrawn;
  no replacement leaves blocked victims evicted; recently blocked candidates sort
  later.
- `apis/config`: defaulting and validation of `podPreemption`.

#### Integration tests

Using envtest, which runs the eviction subresource and its PDB checks in the API
server. Envtest runs no kube-controller-manager, so there is no Deployment,
ReplicaSet or disruption controller. The tests therefore create Pods directly
(plain Pods and pod groups, labeled to match the PDBs), and set each PDB's
status (`observedGeneration`, `disruptionsAllowed`, `currentHealthy`,
`desiredHealthy`) by hand to make it allow or refuse evictions.

- A PDB whose status allows no disruptions, covering the only victim: the
  eviction is refused, `DisruptionBlocked` is `True`, and the Pods are deleted
  after the timeout.
- Two candidate groups of Pods, one covered by a PDB that allows no
  disruptions: the other is preempted, and the blocked one is withdrawn and
  keeps running.
- A pod group where the PDB allows evicting some Pods but not others: the
  allowed Pods are evicted, the rest are refused, the group is not withdrawn,
  and the remaining Pods are deleted after the timeout.
- A Pod covered by two PDBs: the eviction fails with a 500, `DisruptionBlocked`
  is `True` with reason `EvictionFailed`, and the Pod is deleted after the
  timeout.
- Gate disabled: Pods are deleted immediately regardless of PDBs.

Scenarios that need real Deployment controllers and PDB status computed by the
disruption controller are covered by the e2e tests below.

#### e2e tests

A cohort with two ClusterQueues and Deployment workloads with PDBs: the lender
scales up and reclaims; no Deployment goes below its `minAvailable` while
another candidate is available.

### Graduation Criteria

**Alpha (target v0.22):** gate disabled by default; global configuration; unit
and integration tests.

**Beta:**

- Gate enabled by default, with the policy still defaulting to `Delete`.
- A decision on the per-ClusterQueue timeout, recorded in this KEP.
- e2e tests from the [e2e tests](#e2e-tests) section pass in CI.
- Feedback from at least two users running `Evict`.
- A decision, recorded in this KEP, on the PDB-informer ordering hint from
  [Alternatives](#alternatives). If it is adopted: candidates whose PDB reports
  `disruptionsAllowed: 0` are ordered after other candidates of the same
  priority, verified by unit tests of `CandidatesOrdering` and an integration
  test in which the scheduler selects the candidate whose PDB allows the
  disruption without first issuing a refused eviction.

**Stable:**

- Integration and e2e tests assert both bounds:
  - over-preemption: each withdrawn victim loses at most one extra Pod;
  - stalls: a preemptor whose need can be covered is admitted within
    `evictionTimeout` plus one scheduling cycle.
- Every bug filed against the feature that reports a violation of either bound
  is fixed, with a regression test.
- The `PodPreemption` API has had no breaking changes for at least one release.

## Implementation History

- 2026-11-02: KEP drafted. A prototype of the Pod-integration part (evict,
  retry, timeout) accompanies it, with a fixed 10 minute timeout in place of the
  configuration field and without the scheduler changes, which wait for
  agreement on the design.

## Drawbacks

- Preemption becomes slower when victims' budgets refuse, by up to
  `evictionTimeout` in the worst case.
- The scheduler gains a new state (blocked victim) and a withdrawal path.

## Alternatives

- **Strict enforcement (no timeout).** Rejected: lets a PDB override quota and
  priority, the concern raised in #5273.
- **Evict without scheduler changes (Pod integration changes only).** Rejected: a refusal makes
  the preemptor wait for the full timeout and then deletes the same victim
  anyway, which is strictly worse than today.
- **Compute PDB budgets in the scheduler (PDB informer) and skip victims whose
  budget is exhausted before evicting.** More precise and avoids failed API
  calls, but duplicates the disruption controller's accounting and can be stale.
  Could be added in beta as an ordering hint on top of this design.
- **Dry-run evictions during target selection.** Accurate, but adds API calls to
  the scheduling cycle.
- **A Kueue-native "workload disruption budget".** A larger API surface; PDBs are
  already the standard way tenants declare disruption tolerance.
