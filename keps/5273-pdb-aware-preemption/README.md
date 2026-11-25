# KEP-5273: Prefer preemption victims whose PodDisruptionBudgets allow the disruption

<!-- toc -->
- [Summary](#summary)
  - [Terminology](#terminology)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1: serving tenants sharing a cohort](#story-1-serving-tenants-sharing-a-cohort)
    - [Story 2: not enough budget](#story-2-not-enough-budget)
    - [Story 3: a borrower with no PDB](#story-3-a-borrower-with-no-pdb)
  - [Notes/Constraints/Caveats](#notesconstraintscaveats)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [Today's behavior](#todays-behavior)
  - [PDB lookup](#pdb-lookup)
  - [Victim selection](#victim-selection)
  - [Pod groups](#pod-groups)
  - [Observability](#observability)
  - [Interaction with other features](#interaction-with-other-features)
  - [Test Plan](#test-plan)
    - [Unit tests](#unit-tests)
    - [Integration tests](#integration-tests)
    - [e2e tests](#e2e-tests)
  - [Graduation Criteria](#graduation-criteria)
  - [Unresolved Questions](#unresolved-questions)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
<!-- /toc -->

## Summary

When Kueue preempts Workloads managed by the Pod integration (plain Pods, pod
groups, and the Deployment, StatefulSet and LeaderWorkerSet integrations built on
it), it picks victims without looking at PodDisruptionBudgets (PDBs). It can
take all of the Pods it needs from one Deployment, far below that Deployment's
PDB, while another Deployment of the same priority has budget to spare.

This KEP makes PDBs a factor in victim selection, on a best-effort basis, the
same way kube-scheduler treats them in Pod priority preemption
("PodDisruptionBudget is supported, but not guaranteed"):

1. When choosing victims, the scheduler prefers candidates whose PDBs still
   allow a disruption, counting the victims it has already chosen in the same
   attempt.
2. Candidates whose PDB has no budget left are used only when the preemptor
   cannot fit otherwise.
3. Victims are stopped exactly as today, by deleting their Pods. A PDB never
   delays or blocks a preemption.

The change is limited to the scheduler, behind a feature gate.

### Terminology

- **Victim:** a Workload selected for preemption, so that a pending Workload
  (the preemptor) can be admitted. Kueue code calls these preemption targets.
- **Candidate:** a Workload that the existing preemption rules allow to be a
  victim for a given preemptor.
- **Budget:** a PDB's `status.disruptionsAllowed`, as computed by the
  disruption controller, minus the victims already chosen under that PDB in the
  current preemption attempt.
- **Within budget / over budget:** a candidate is within budget if every PDB
  that selects one of its Pods has enough budget for all of its Pods that the
  PDB selects. Otherwise it is over budget. A candidate with no PDB is within
  budget.

## Motivation

Kueue's preemption is increasingly used for long-running serving workloads, not
only batch. A common setup is a cohort shared by several tenants: each
ClusterQueue has a guaranteed nominal quota, lends what it does not use, and
reclaims it with `reclaimWithinCohort` when its own demand returns. Serving
tenants express their disruption tolerance with PDBs, and every other voluntary
disruption in the cluster (node drains, upgrades, the cluster autoscaler)
takes them into account. Kueue preemption ignores them: Workloads of the same
priority are ordered by admission time, so the Pods of the most recently scaled
Deployment are taken first, and a single reclaim can remove all of them.

Issue [#5273](https://github.com/kubernetes-sigs/kueue/issues/5273) asks for PDBs
to be respected. The discussion there settled two points:

- PDBs should be supported in the same way as in Pod priority preemption:
  taken into account, but not guaranteed.
- There must be no strict mode. A PDB is namespace-scoped and written by the
  tenant being preempted; it must not be able to override the quota and
  priorities set by the administrator.

This KEP follows both.

### Goals

- When the preemptor's need can be covered by candidates within budget, choose
  them instead of candidates that would exceed a PDB.
- When it cannot, violate as few PDBs as the existing selection allows, and
  report it.
- Never delay or block a preemption because of a PDB.
- Keep today's behavior unchanged when the feature gate is disabled.

### Non-Goals

- Guaranteeing PDBs. The scheduler reads PDB status from its cache, which can
  lag, and it deletes victims; see [Alternatives](#alternatives) for an
  eviction-based design that enforces PDBs at the API server.
- Strict enforcement, where a PDB can prevent a preemption.
- Job-based integrations (batch/Job, JobSet, RayJob, Kubeflow jobs, and so on).
  Stopping one Pod of a job stops the whole job, so a per-Pod budget does not
  describe their disruption tolerance.
- Fair sharing preemption in alpha (see [Graduation Criteria](#graduation-criteria)).
- Topology- or node-aware victim selection.

## Proposal

Add a feature gate `PreemptionRespectsPodDisruptionBudgets` (alpha, disabled by
default). With the gate enabled, the classical preemption strategy (used when
fair sharing is disabled) chooses, among candidates that tie on priority and
on every ordering key before it, those within budget before those over budget.
Nothing else about preemption changes: the set of candidates, the order of
priorities, how victims are stopped, and when quota is released all stay as
they are.

### User Stories

#### Story 1: serving tenants sharing a cohort

ClusterQueues `team-a` and `team-b` share a cohort. Overnight `team-b`
borrowed 3 CPUs of `team-a`'s nominal quota. `team-b` runs three Deployments,
`web`, `api` and `db`, with 3 replicas of 1 CPU each, all of the same priority.
Each has a PDB like:

```yaml
apiVersion: policy/v1
kind: PodDisruptionBudget
metadata:
  name: web
  namespace: team-b
spec:
  maxUnavailable: 1
  selector:
    matchLabels:
      app: web
```

In the morning `team-a` scales up and reclaims 3 CPUs. `web` was scaled most
recently, so its Pods come first in today's order.

| Candidate | Today | With this KEP |
|---|---|---|
| `web-3` | victim | victim (`web` budget 1, then 0) |
| `web-2` | victim | skipped, over budget |
| `web-1` | victim | skipped, over budget |
| `api-3` | | victim (`api` budget 1, then 0) |
| `api-2`, `api-1` | | skipped, over budget |
| `db-3` | | victim (`db` budget 1, then 0) |

Today `web` loses all three Pods. With this KEP each Deployment loses one and
stays within its PDB. `team-a` is admitted as quickly as today, because the
victims are deleted as today.

#### Story 2: not enough budget

Same as Story 1, but `team-b` runs only `web` and `api`. After `web-3` and
`api-3` the preemptor still needs 1 CPU, and every remaining candidate is over
budget. The scheduler then takes over-budget candidates in the usual order and
picks `web-2`. `web` goes below its PDB, as it would today, but `api` keeps two
Pods instead of `web` losing all three. The preemption is reported as violating
a PDB (see [Observability](#observability)).

#### Story 3: a borrower with no PDB

A training tenant has no PDBs. All of its candidates are within budget, so
victim selection is the same as today.

### Notes/Constraints/Caveats

- **Best effort.** PDB status is computed by the disruption controller and read
  from the scheduler's informer cache, so it can lag. For example, a Pod that
  became unready a moment ago may not yet be reflected in
  `disruptionsAllowed`. The scheduler may then delete a victim it believed was
  within budget. This is the same trade-off kube-scheduler makes.
- **Priority comes first.** A within-budget candidate is preferred only over
  over-budget candidates that tie with it on priority and on every ordering key
  before priority (see [Victim selection](#victim-selection)). The KEP never
  chooses a higher-priority victim to spare a lower-priority one's PDB.
  [Unresolved](#unresolved-questions) asks whether PDBs should rank above
  priority, as in kube-scheduler.
- **Counting.** Each Pod of a victim that a PDB selects and that is not already
  terminating counts as one disruption against that PDB. This is conservative
  for Pods that are already unhealthy, which an eviction might allow under the
  PDB's `unhealthyPodEvictionPolicy`.
- **Deletion bypasses PDBs.** Victims are deleted, not evicted, so the API
  server does not check PDBs when the victim is stopped. PDBs only influence
  which victims are chosen.

### Risks and Mitigations

| Risk | Mitigation |
|---|---|
| Stale PDB status leads to a PDB being exceeded. | Same as kube-scheduler; documented as best effort. Exceeded PDBs are reported in the preemption message and a metric. An eviction-based follow-up is described in [Alternatives](#alternatives). |
| Scheduler cost of reading PDBs and Pods. | Reads come from the shared informer cache, only during a preemption attempt, only for Pod-integration candidates, and are memoized per attempt. No API calls are added to the scheduling cycle. |
| Victim choice changes in a way users do not expect, for example older Workloads are preempted before newer ones. | Gated; the change only reorders candidates of equal priority; the preemption message names the PDB that was considered. |
| A tenant writes `maxUnavailable: 0` to avoid being chosen. | Its candidates are over budget, so they are chosen last among equal-priority candidates but are still chosen when needed. A PDB never makes a candidate ineligible. |

## Design Details

### Today's behavior

1. `Preemptor.getTargets` (`pkg/scheduler/preemption/preemption.go`) walks the
   candidates produced by the preemption strategy, in `CandidatesOrdering`
   order (already evicted, other ClusterQueue, LocalQueue usage, priority,
   quota reservation time, UID). It removes them one by one from a snapshot
   until the preemptor fits, then `fillBackWorkloads` puts back any that turn
   out not to be needed.
2. `IssuePreemptions` marks each victim `Evicted` with reason `Preempted`.
3. The Pod integration's job reconciler deletes the victim's Pods; quota is
   released once they are no longer active.

Only step 1 changes.

### PDB lookup

For a Pod-integration candidate, the scheduler finds its Pods through the
Workload's owner references (a Pod-integration Workload is owned by its Pods)
and reads them from the manager's informer cache, which already holds Pods for
the Pod integration. It lists the PDBs in the candidate's namespace from the
cache and matches each PDB's selector against each Pod's labels.

Results are memoized for the duration of one preemption attempt: the PDBs per
namespace, and the matching PDBs per candidate.

Candidates from other integrations are treated as having no PDB.

The manager gains read access to PDBs:

```go
// +kubebuilder:rbac:groups=policy,resources=poddisruptionbudgets,verbs=get;list;watch
```

The informer for PDBs is started lazily on the first read, so it is not created
while the feature gate is disabled.

### Victim selection

In `getTargets`, for the classical strategy and with the gate enabled, each
attempt keeps a budget per PDB, initialized from `status.disruptionsAllowed`.
Candidates are still visited in `CandidatesOrdering` order, grouped into runs
that tie on every key up to and including priority:

1. Within a run, a candidate within budget is chosen as today, and the budget
   of each PDB that selects its Pods is reduced by the number of its Pods that
   the PDB selects.
2. An over-budget candidate is set aside.
3. When the run ends, or the candidates are exhausted, and the preemptor still
   does not fit, the set-aside candidates of that run are chosen in their
   original order, before moving on to the next run.

So within-budget candidates are preferred within a priority level, and a lower
priority level is always used up before a higher one, as today.

`fillBackWorkloads` tries to put back over-budget victims before within-budget
ones, so that a victim exceeding a PDB is kept only if it is needed.

Candidates that are already `Evicted` are unaffected: they come first in the
order and their Pods are already terminating.

The preemption oracle used during flavor assignment is unchanged: the change
only reorders candidates and never removes one, so whether preemption can make
room is the same.

### Pod groups

A pod group is one Workload. It is within budget only if every PDB selecting
any of its Pods has budget for all of the Pods it selects. A group is never
split: it is chosen or not as a whole, as today.

### Observability

- The preemption message on the victim's `Evicted` condition and the
  `Preempted` event name the PDB that was exceeded, when the victim was chosen
  over budget.
- `kueue_preemption_pdb_exceeded_total{cluster_queue}`: victims chosen while
  over budget, by the preempting ClusterQueue.

### Interaction with other features

- **Fair sharing:** unchanged in alpha. With fair sharing enabled the gate has
  no effect; see [Graduation Criteria](#graduation-criteria).
- **Configurable preemption:** candidates found by preemption rules go through
  the same `getTargets` loop and are reordered the same way.
- **FastQuotaReleaseInPodIntegration, WaitForPodsReady, TAS node failure
  handling:** unchanged. They do not choose victims.
- **MultiKueue:** preemption on a worker cluster is done by the worker's Kueue;
  the manager is unaffected.

### Test Plan

[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.

#### Unit tests

- `pkg/scheduler/preemption`:
  - within-budget candidates are chosen before over-budget ones of the same
    priority, with the budget reduced by earlier choices in the same attempt;
  - an over-budget candidate is chosen when nothing else fits, and a
    lower-priority over-budget candidate is still chosen before a
    higher-priority within-budget one;
  - a Pod selected by two PDBs needs budget in both;
  - pod groups are counted per selected Pod;
  - fill-back puts back over-budget victims first;
  - Pods that are terminating do not count;
  - candidates without PDBs, and from other integrations, are ordered as today;
  - with the gate disabled, targets are identical to today's.
- `pkg/scheduler/preemption/policy`: no change to `CandidatesOrdering`.

#### Integration tests

Using envtest. Envtest runs no kube-controller-manager, so tests create Pods
directly and set each PDB's status (`observedGeneration`,
`disruptionsAllowed`, `currentHealthy`, `desiredHealthy`) by hand.

- Story 1: three groups of Pods of the same priority, each under a PDB that
  allows one disruption; reclaim takes one Pod from each group.
- Story 2: not enough budget; the preemptor is admitted, one PDB is exceeded,
  and the metric and message report it.
- Priority: a lower-priority over-budget candidate is preempted before a
  higher-priority within-budget one.
- Gate disabled: victims are the same as today.

#### e2e tests

A cohort with two ClusterQueues and Deployments with PDBs, where PDB status is
computed by the real disruption controller: the lender scales up and reclaims,
and no Deployment goes below its PDB while another Deployment of the same
priority has budget.

### Graduation Criteria

**Alpha (target v0.22):**

- Gate disabled by default.
- Classical preemption, Pod-integration Workloads.
- Unit and integration tests.

**Beta:**

- Fair sharing preemption supported, or a recorded decision that it is out of
  scope.
- A recorded decision on the [unresolved questions](#unresolved-questions).
- e2e tests pass in CI.
- Feedback from at least two users.
- Gate enabled by default.

**Stable:**

- No open bugs reporting a PDB exceeded when a within-budget alternative of the
  same priority existed and the PDB status was current.
- No changes to the selection rules for at least one release.

### Unresolved Questions

- **PDBs above priority.** kube-scheduler ranks minimizing PDB violations above
  victim priority. This KEP ranks priority first, so it never preempts a
  higher-priority Workload to spare a lower-priority one. Should there be an
  option for the kube-scheduler order?
- **Enforcement at the API server.** Should a later stage evict victims that
  were chosen within budget (see "Evict and wait" in
  [Alternatives](#alternatives)), so that stale PDB status cannot lead to a PDB
  being exceeded?

## Implementation History

- 2026-11-02: KEP drafted with an eviction-based design (evict, retry, time out,
  withdraw blocked victims).
- 2026-11-23: Revised to best-effort victim selection, following the direction
  in #5273. The eviction-based design moved to Alternatives.

## Drawbacks

- PDBs are not guaranteed, which may surprise users who expect the behavior of
  the eviction API.
- The scheduler reads PDBs and Pods, which it did not need before.
- Victim selection depends on PDB status, which makes it harder to predict
  which Workloads will be preempted.

## Alternatives

- **Evict and wait.** Stop victims through the Pod eviction subresource, so the
  API server enforces PDBs; retry refused evictions with backoff, and delete
  after a configurable timeout. Rejected for alpha: a refused victim stays
  `Evicted` and still holds its quota, so `getTargets` keeps selecting it and
  the preemptor waits for the whole timeout. During a reclaim the budget often
  cannot recover, because the evicted Pod's replacement cannot be admitted
  without the quota being reclaimed. It needs a new Workload condition, new
  configuration and new RBAC. It could be added later for the victims chosen
  within budget, to cover stale PDB status.
- **Evict, then withdraw and replace.** As above, but when a victim is refused,
  the scheduler clears its `Evicted` condition and chooses another victim.
  Rejected: needs ordering between the scheduler's withdrawal and an in-flight
  eviction, and an eviction already in flight can still disrupt one extra Pod
  per withdrawn victim. Choosing within budget up front avoids most refusals.
- **Evict from the scheduler.** Call the eviction subresource in
  `IssuePreemptions` and mark a victim `Evicted` only if the eviction is
  accepted. Rejected: adds Pod API calls to the scheduling cycle, and the
  scheduler would need integration-specific knowledge of Pods.
- **Dry-run evictions during target selection.** A dry run checks a PDB as it
  is now, without the victims already chosen in the same attempt, so it needs
  the same per-PDB counting as this KEP, plus an API call per candidate.
- **Prefer within-budget candidates without counting.** Order candidates by
  whether their PDB has any budget, as a plain sort key. Rejected: all of a
  Deployment's candidates would look within budget, so it would still take all
  of them.
- **A Kueue-native Workload Disruption Budget.** Avoids mapping Pods to
  Workloads and works for every integration. Rejected for now: for Pod
  integration Workloads it would duplicate PDBs, which tenants already write
  and which also govern node drains and the cluster autoscaler.
- **Strict enforcement.** Rejected in #5273: a PDB must not override quota and
  priority.
