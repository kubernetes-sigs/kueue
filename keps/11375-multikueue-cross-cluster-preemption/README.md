# KEP-11375: MultiKueue Cross-Cluster Preemption

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1 — Guaranteed-tier customer SLA](#story-1--guaranteed-tier-customer-sla)
    - [Story 2 — Burst absorption across environments](#story-2--burst-absorption-across-environments)
  - [Notes/Constraints/Caveats](#notesconstraintscaveats)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [Cohort membership: the shared <code>cohortName</code> string](#cohort-membership-the-shared-cohortname-string)
  - [Cohort-level properties via the single-cluster <code>Cohort</code> CR](#cohort-level-properties-via-the-single-cluster-cohort-cr)
  - [Worked example: per-tenant cohort across worker clusters](#worked-example-per-tenant-cohort-across-worker-clusters)
    - [Topology](#topology)
    - [Activation (prerequisite)](#activation-prerequisite)
    - [Setup YAML](#setup-yaml)
    - [What you observe (step-by-step)](#what-you-observe-step-by-step)
    - [Why each knob matters](#why-each-knob-matters)
  - [Dispatcher mode](#dispatcher-mode)
  - [Reconcile algorithm](#reconcile-algorithm)
  - [Per-CQ preemption policy](#per-cq-preemption-policy)
  - [Single-writer lock: victim-claim annotation](#single-writer-lock-victim-claim-annotation)
  - [Eviction primitive](#eviction-primitive)
  - [Interaction with KEP-8303 (Orchestrated Preemption)](#interaction-with-kep-8303-orchestrated-preemption)
  - [Test Plan](#test-plan)
    - [Prerequisite testing updates](#prerequisite-testing-updates)
    - [Unit tests](#unit-tests)
    - [Integration tests](#integration-tests)
    - [e2e tests](#e2e-tests)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
<!-- /toc -->

## Summary

Today MultiKueue dispatches workloads across worker clusters but treats each
worker as a black box for scheduling decisions: a workload is admitted only
if a worker has free quota. There is no way for a workload arriving at the
manager to reclaim its cohort's quota from a cohort-sibling tenant's
workload that is borrowing it on a worker — even when single-cluster Kueue
would already do exactly that locally via `ReclaimWithinCohort`. Owners
wait indefinitely while those borrowing workloads finish naturally.

Note on terminology: "borrowing" here is single-cluster Kueue's local
cohort math, applied independently inside each worker cluster. A tenant's
ClusterQueue on a worker is borrowing whenever its
`Status.FlavorsUsage > NominalQuota`, which means it is using cohort
capacity that another tenant on that same worker is not currently
consuming. Worker clusters do not borrow from each other; the "cross-
cluster" part is the manager's fleet-wide visibility and its ability to
target the eviction at the right worker.

This KEP introduces a cross-cluster preemption capability for MultiKueue:
a new dispatcher mode (`kueue.x-k8s.io/multikueue-dispatcher-cross-cluster-preemption`).
The dispatcher's load-bearing semantic is **quota-based reclaim**: a
sibling CQ is only an eligible victim when it is currently borrowing
cohort capacity (`Status.FlavorsUsage > NominalQuota`). Reclaim is then
gated by the victim CQ's `Spec.Preemption.ReclaimWithinCohort` policy
(`Never` / `LowerPriority` / `Any`) — so priority is one configurable
gate, not the primary driver. With `Any`, an owner reclaims at *equal*
priority; with `LowerPriority`, a strict priority delta is required.
**No new CRDs are introduced**: cross-cluster cohort identity is the shared
`ClusterQueue.spec.cohortName` string across the manager and the workers,
exactly the same mechanism single-cluster Kueue uses. Cohort-level
properties (parent, additional shared quota, fair sharing) live on the
standard single-cluster `Cohort` CR on the manager (and on each worker
that needs them), and the dispatcher inherits the result via each remote
ClusterQueue's `Status.FlavorsUsage` snapshot. Activation is gated by
alpha feature gate `MultiKueueCrossClusterPreemption` (default off).

## Motivation

Operators that split workloads across per-tenant or per-environment worker
clusters need a way to enforce per-tenant quota guarantees across cluster
boundaries. Without cross-cluster preemption:

- A guaranteed-tier customer's workload can be blocked indefinitely when
  a cohort-sibling tenant's workload is borrowing the customer's nominal
  quota on a worker, because the manager has no mechanism to reach into
  that worker and evict the borrowing workload.
- Wait time is bounded only by how long that borrowing workload runs
  naturally, which may be hours.

Priority-driven preemption between tenants is a *secondary* use case
this KEP enables (via `ReclaimWithinCohort=LowerPriority`), but the
primary motivation is honoring per-tenant nominal quota the way
single-cluster Kueue already does within a cluster.

KEP-693 (the original MultiKueue KEP) explicitly listed "synchronize
configuration across the clusters" as a Non-Goal. KEP-8303 (Orchestrated
Preemption) addresses a different problem: avoiding redundant simultaneous
preemptions across replicas of *one* workload dispatched via AllAtOnce.
Neither covers cross-cluster victim selection.

### Goals

- Provide cross-cluster preemption with **the same API surface as
  single-cluster Cohort** so users have one mental model.
- Honor `ClusterQueue.spec.cohortName` for membership, the full per-CQ
  preemption policy (`ReclaimWithinCohort` / `BorrowWithinCohort`), and
  the per-CQ borrowing state (`Status.FlavorsUsage > NominalQuota`)
  exactly as in single-cluster: a victim is preempted iff its home CQ
  is currently borrowing AND the priority/policy gate allows it.
- Stay additive: do not modify the in-tree scheduler or change behavior
  when the feature gate is off.
- Reuse existing primitives (admission checks, the External dispatcher
  extension point, `workload.Evict`, the multikueue origin label) instead of
  introducing parallel infrastructure.

### Non-Goals

- No new CRD. Cross-cluster cohort identity is the shared
  `ClusterQueue.spec.cohortName` string, exactly the mechanism
  single-cluster Kueue uses.
- The cross-cluster dispatcher does not read `Cohort` CRs. Cohort-level
  shared quota, fair-sharing weights, parent chains, and lending/
  borrowing limits remain enforced by each cluster's local scheduler at
  admission time. The dispatcher only consumes the resulting
  `ClusterQueue.Status.FlavorsUsage` snapshot.
- The dispatcher is inherently cross-CQ. Same-CQ preemption stays in the
  worker-local `Spec.Preemption.WithinClusterQueue` policy.
- Single victim per reconcile. Multi-victim packing for one workload is
  not in scope.
- KEP-8303 (`MultiKueueOrchestratedPreemption`) is an independent
  feature gate. Both can be enabled together; they don't coordinate.
- Elastic workloads / `MultiKueueMultiWorkloadAdapter` are out of scope.

## Proposal

We introduce a new External-dispatcher mode for MultiKueue that, on each
Workload reconcile, discovers cohort siblings across configured worker
clusters and evicts a single victim on a sibling worker so the incoming
workload can be admitted there. The dispatcher mirrors single-cluster
cohort preemption semantics: the **quota borrowing filter**
(`cqIsBorrowing` — only currently-borrowing CQs are eligible victim
sources), the **policy gate** (`Spec.Preemption.ReclaimWithinCohort` —
`Never` / `LowerPriority` / `Any`), and the **borrow-while-preempting
gate** (`Spec.Preemption.BorrowWithinCohort`) are all applied jointly.
Across candidates the dispatcher picks the lowest-priority victim
(creation-time + name tiebreak).

### User Stories

#### Story 1 — Guaranteed-tier customer SLA

A platform offers guaranteed-tier customers a bounded wait time as long as
they remain within their per-tenant quota. Workers are split per-tenant for
security isolation, but workers share an on-demand capacity reservation
behind the scenes. When a guaranteed customer submits a workload within
its quota and finds that a cohort-sibling tenant's workload is borrowing
that quota on a worker, the borrowing workload should be reclaimed within
seconds, not hours — even at *equal* priority (the relevant signal is
ownership of the nominal quota, not priority delta).

#### Story 2 — Burst absorption across environments

Multiple test/staging clusters draw from a shared GPU pool. A
high-priority release-blocking test arriving at one cluster should be able
to reclaim slots from lower-priority experimental workloads running on the
other clusters.

### Notes/Constraints/Caveats

- "Borrowing" on the worker side is judged purely from each remote
  ClusterQueue's `Status.FlavorsUsage` snapshot, read at reconcile time. It
  reflects the worker's local scheduler accounting; transient drift between
  worker reconciles and dispatcher reconciles is bounded by the worker's
  status update frequency.
- The dispatcher only changes behavior when both the
  `MultiKueueCrossClusterPreemption` feature gate is on AND the new
  dispatcher mode is selected via `Configuration.MultiKueue.DispatcherName`.
  The default `AllAtOnce` and `Incremental` paths are untouched.

### Risks and Mitigations

1. **Race between victim eviction and nomination.** Two manager reconciles
   could evict different victims. Mitigated by the `victim-of` annotation
   single-writer lock; eviction is also idempotent.
2. **Multiple manager controllers active.** Mitigated by leader election.
3. **Evicting a workload not owned by this manager.** Mitigated by
   filtering listed candidates to those carrying our `MultiKueueOriginLabel`.
4. **Stale priority data.** Read priority from the remote workload's
   `Spec.Priority` (mirrors manager-side priority via existing priority-sync
   logic in MultiKueue's workload reconciler).
5. **Worker disconnect mid-eviction.** Eviction is idempotent; on the next
   reconcile after reconnect, we either observe the eviction completed or
   re-issue it.
6. **Workloads outside the manager's MultiKueueConfig.** The dispatcher
   intersects cohort discovery with the workload's MultiKueueConfig clusters,
   so workers not in the workload's config are skipped.
7. **Worker CQs with the same `cohortName` but in different MultiKueueConfigs.**
   The dispatcher restricts cohort membership to the workload's configured
   worker clusters — so a CQ with the same cohortName on a worker outside
   the workload's MultiKueueConfig will not be a victim candidate.

## Design Details

### Cohort membership: the shared `cohortName` string

Cross-cluster cohort identity is purely the `spec.cohortName` field on
each `ClusterQueue` — the exact same mechanism single-cluster Kueue uses
for cohort membership. There is **no separate cross-cluster cohort CRD**:

- A worker `ClusterQueue` joins a cohort by setting `spec.cohortName: <name>`.
- The manager's `ClusterQueue` (the one that holds the multikueue admission
  check) also sets `spec.cohortName: <name>` to identify which cohort
  applies to its workloads.
- Worker CQs across different worker clusters that share the same cohortName
  are treated as cohort siblings by the cross-cluster-preemption dispatcher.
- (Optional) Create a single-cluster `Cohort` CR on the manager and/or
  workers when you need cohort-level properties (`parentName`,
  `resourceGroups`, `fairSharing`); each side's standard Kueue scheduler
  enforces them locally.

### Cohort-level properties via the single-cluster `Cohort` CR

When the user wants cohort-level shared resources, lending/borrowing
limits, fair-share weights, or hierarchical cohorts, they create the
standard single-cluster `Cohort` CR with the matching name:

```yaml
apiVersion: kueue.x-k8s.io/v1beta2
kind: Cohort
metadata:
  name: shared-gpu     # matches the cohortName on member ClusterQueues
spec:
  resourceGroups:
  - coveredResources: ["cpu", "memory"]
    flavors:
    - name: default-flavor
      resources:
      - name: cpu
        nominalQuota: "0"     # additional shared cohort capacity (optional)
      - name: memory
        nominalQuota: 0
  # parentName and fairSharing are honored locally by the cluster's scheduler
```

This CR is **identical to what users already use in single-cluster Kueue** —
no MultiKueue-specific shape, no new CRD. Each cluster's local scheduler
performs its cohort math; the dispatcher consumes the resulting
`ClusterQueue.Status.FlavorsUsage` to drive cross-cluster victim selection.

### Worked example: per-tenant cohort across worker clusters

This is the canonical setup the feature is built for. Two tenants — A and B —
share GPU capacity across two worker clusters via a cohort named `gpu-pool`.
Each tenant has its own ClusterQueue with its own nominal quota. Tenant A
is the owner of all worker-side capacity; tenant B has no nominal of its
own and runs entirely by borrowing tenant A's unused quota. When tenant A
submits within its quota and tenant B is occupying the workers,
cross-cluster preemption reclaims A's quota from B's borrowed workloads —
**at equal workload priority**, driven purely by quota ownership.

#### Topology

```
                ┌────────────────────────────┐
                │          manager           │
                │  Cohort: gpu-pool          │
                │  ClusterQueue tenant-a-cq  │   nominal=8 GPU
                │  ClusterQueue tenant-b-cq  │   nominal=8 GPU
                │  (manager-side headroom    │   (so manager admits both
                │   ≥ workers' total)        │    tenants without contention)
                │  + multikueue-ac           │
                └─────────────┬──────────────┘
                              │  MultiKueue dispatch
                ┌─────────────┴───────────────┐
                ▼                             ▼
        ┌───────────────────┐         ┌───────────────────┐
        │      worker1      │         │      worker2      │
        │  4 GPU physical   │         │  4 GPU physical   │
        │  Cohort: gpu-pool │         │  Cohort: gpu-pool │
        │  tenant-a-cq      │         │  tenant-a-cq      │
        │    nominal=4 GPU  │         │    nominal=4 GPU  │
        │    Reclaim=Never  │         │    Reclaim=Never  │  ← owner, protected
        │  tenant-b-cq      │         │  tenant-b-cq      │
        │    nominal=0 GPU  │         │    nominal=0 GPU  │
        │    Reclaim=Any    │         │    Reclaim=Any    │  ← borrower, preemptible
        └───────────────────┘         └───────────────────┘
```

Quota math:
- Tenant A's total worker-side nominal = **4 + 4 = 8 GPU** (across the fleet).
- Tenant B's total worker-side nominal = **0 GPU** — runs entirely by
  borrowing from tenant A's unused capacity.
- Manager-side nominals are deliberately larger (8 + 8) so manager-side
  admission never blocks either tenant; the only scarcity is at the
  worker side, which is exactly where the new cross-cluster preemption
  applies.

#### Activation (prerequisite)

The dispatcher only runs when both:

1. The `MultiKueueCrossClusterPreemption` feature gate is enabled on the
   manager's kueue controller-manager (alpha, default off):

   ```
   --feature-gates=MultiKueueCrossClusterPreemption=true
   ```

2. The manager's `Configuration` selects the new dispatcher mode:

   ```yaml
   multiKueue:
     dispatcherName: kueue.x-k8s.io/multikueue-dispatcher-cross-cluster-preemption
   ```

Without both, the rest of this example degrades to standard MultiKueue
AllAtOnce behavior — the per-tenant CQ setup is valid but cross-cluster
preemption never fires, and tenant A's workload would wait indefinitely
when workers are full of B's borrowed work. See [Dispatcher mode](#dispatcher-mode)
below for the full spec.

#### Setup YAML

**Manager cluster** (per-tenant ClusterQueues + the MultiKueue admission
check):

```yaml
apiVersion: kueue.x-k8s.io/v1beta2
kind: Cohort
metadata:
  name: gpu-pool
# spec is optional; included here for parity with workers and to make the
# cohort discoverable. No additional shared resources at this level.
spec:
  resourceGroups: []
---
apiVersion: kueue.x-k8s.io/v1beta2
kind: ClusterQueue
metadata:
  name: tenant-a-cq
spec:
  cohortName: gpu-pool                    # joins the cross-cluster cohort
  namespaceSelector: {}
  admissionChecksStrategy:
    admissionChecks:
    - name: multikueue-ac
  resourceGroups:
  - coveredResources: ["nvidia.com/gpu"]
    flavors:
    - name: default-flavor
      resources:
      - name: nvidia.com/gpu
        nominalQuota: "8"                 # ← tenant A's quota
---
apiVersion: kueue.x-k8s.io/v1beta2
kind: ClusterQueue
metadata:
  name: tenant-b-cq
spec:
  cohortName: gpu-pool
  namespaceSelector: {}
  admissionChecksStrategy:
    admissionChecks:
    - name: multikueue-ac
  resourceGroups:
  - coveredResources: ["nvidia.com/gpu"]
    flavors:
    - name: default-flavor
      resources:
      - name: nvidia.com/gpu
        nominalQuota: "8"                 # ← tenant B's quota
---
apiVersion: kueue.x-k8s.io/v1beta2
kind: LocalQueue
metadata:
  name: tenant-a-lq
  namespace: default
spec:
  clusterQueue: tenant-a-cq
---
apiVersion: kueue.x-k8s.io/v1beta2
kind: LocalQueue
metadata:
  name: tenant-b-lq
  namespace: default
spec:
  clusterQueue: tenant-b-cq
```

**Each worker cluster** (apply identically to `worker1` and `worker2` —
both have the same shape, just smaller per-worker nominals):

```yaml
apiVersion: kueue.x-k8s.io/v1beta2
kind: Cohort
metadata:
  name: gpu-pool
spec:
  resourceGroups: []
---
apiVersion: kueue.x-k8s.io/v1beta2
kind: ClusterQueue
metadata:
  name: tenant-a-cq
spec:
  cohortName: gpu-pool
  namespaceSelector: {}
  preemption:
    reclaimWithinCohort: Never            # owner; never a victim source
  resourceGroups:
  - coveredResources: ["nvidia.com/gpu"]
    flavors:
    - name: default-flavor
      resources:
      - name: nvidia.com/gpu
        nominalQuota: "4"                 # this worker's share of A's 8 GPU
---
apiVersion: kueue.x-k8s.io/v1beta2
kind: ClusterQueue
metadata:
  name: tenant-b-cq
spec:
  cohortName: gpu-pool
  namespaceSelector: {}
  preemption:
    reclaimWithinCohort: Any              # borrower; equal-priority reclaim allowed
  resourceGroups:
  - coveredResources: ["nvidia.com/gpu"]
    flavors:
    - name: default-flavor
      resources:
      - name: nvidia.com/gpu
        nominalQuota: "0"                 # no own quota → must borrow
---
apiVersion: kueue.x-k8s.io/v1beta2
kind: LocalQueue
metadata:
  name: tenant-a-lq
  namespace: default
spec:
  clusterQueue: tenant-a-cq
---
apiVersion: kueue.x-k8s.io/v1beta2
kind: LocalQueue
metadata:
  name: tenant-b-lq
  namespace: default
spec:
  clusterQueue: tenant-b-cq
```

The per-tenant ClusterQueue + LocalQueue + cohort + reclaim-policy
constructs are **all standard single-cluster Kueue API**. The dispatcher
introduced by this KEP just makes the cohort math work across worker
boundaries; users don't learn any new shape.

#### What you observe (step-by-step)

| # | Action | Observed state |
|---|---|---|
| 1 | (idle) | All worker `tenant-{a,b}-cq` Status.FlavorsUsage = 0. |
| 2 | Tenant B submits Job-B1 (4 GPU) to manager `tenant-b-lq` | Manager admits within tenant-b-cq nominal=8. MultiKueue dispatches → say `worker1` admits via cohort borrow. `worker1/tenant-b-cq.Status.FlavorsUsage = 4`, `Nominal = 0` → **borrowing**. |
| 3 | Tenant B submits Job-B2 (4 GPU) | Same flow; `worker1` is full, so `worker2` admits Job-B2 via cohort borrow. Both workers now full of B's borrowed work. |
| 4 | Tenant A submits Job-A1 (4 GPU) to manager `tenant-a-lq`, **at the same priority as Job-B1/B2** | Manager admits within tenant-a-cq nominal=8 (no manager-side contention). MultiKueue admission check goes Pending; cross-cluster-preemption dispatcher reconciles. |
| 5 | Dispatcher decision (see [Reconcile algorithm](#reconcile-algorithm)) | Discovers 4 cohort members (`{worker1,2}` × `{tenant-a-cq,tenant-b-cq}`). Skips tenant-a-cq (Reclaim=Never AND same CQ as incoming). Skips worker2/tenant-b-cq if it's not borrowing in some scenarios; here, both `tenant-b-cq` on worker1 and worker2 are borrowing — both eligible. Picks Job-B1 (tiebreak: creation time). |
| 6 | Dispatcher claims + evicts Job-B1 on manager-side | Job-B1's manager workload gets `Evicted=True`. MultiKueue's standard "delete remote when no quota reservation" path deletes Job-B1's worker workload. `worker1` freed. |
| 7 | Dispatcher nominates `worker1` for Job-A1 | MultiKueue copies Job-A1 to `worker1`; it admits via tenant-a-cq nominal=4. |
| 8 | (Optional) Job-B1 re-enters the manager admission queue | If both workers are still full of A's and B's other jobs, B1 stays pending; otherwise gets re-dispatched. |

Measured on a kind harness with this exact topology (using CPU instead of
GPU for portability): step 4 → step 6 takes ≈ **0.2 s**; step 4 → step 7
completes in ≈ **1.3 s**.

#### Why each knob matters

- **`cohortName: gpu-pool`** on every CQ (manager + workers): this is the
  *only* thing tying these ClusterQueues into a cohort the dispatcher
  considers. No separate cross-cluster cohort CRD.
- **Tenant A's `reclaimWithinCohort: Never`** on each worker: prevents A's
  workloads from ever being preempted by cross-cluster reclaim. A is the
  protected owner.
- **Tenant B's `reclaimWithinCohort: Any`** on each worker: makes B's
  workloads preemptible at any priority when an owner reclaims. With
  `LowerPriority` instead, A would need a strictly higher priority class
  to evict B — which defeats the "owner reclaim at equal priority"
  semantics this use case requires.
- **Per-worker `tenant-b-cq.nominalQuota: "0"`**: makes every B
  admission a *borrow* against A's unused nominal. That's what
  `cqIsBorrowing(tenant-b-cq) = true` keys off of, which is what makes
  tenant B's workloads eligible victims.
- **Manager-side nominals ≥ workers' fleet total**: ensures manager-side
  admission never contends. The dispatcher's preemption only addresses
  *worker-side* scarcity; manager-side scarcity is a separate concern
  handled by standard manager-side cohort reclaim (unrelated to this
  KEP).

### Dispatcher mode

A new value for `Configuration.MultiKueue.DispatcherName`:

```
kueue.x-k8s.io/multikueue-dispatcher-cross-cluster-preemption
```

When set, the new dispatcher reconciler is registered alongside the
existing built-ins. It activates only when the
`MultiKueueCrossClusterPreemption` feature gate is enabled.

### Reconcile algorithm

For each Workload reconcile, with MultiKueue admission check in `Pending`
and quota reserved on the manager:

1. Read the workload's manager-side ClusterQueue
   (`status.admission.clusterQueue`) and capture `Spec.Preemption` +
   `Status.FlavorsReservation` for the BorrowWithinCohort gate below.
2. Read that CQ's `spec.cohortName`. If empty, fall back to nominating all
   configured remote clusters (preserves all-at-once for non-cohort CQs).
3. Discover cohort members: list ClusterQueues across configured worker
   clusters and group by `spec.cohortName == cohort`. Each member
   carries its full `Spec.Preemption`, `Spec.ResourceGroups` (with
   `NominalQuota`), and `Status.FlavorsUsage` so the dispatcher can
   apply the borrowing filter, the policy gate (`ReclaimWithinCohort`),
   and the priority filter when policy is `LowerPriority`.
4. **Same-CQ skip**: skip cohort members whose CQ name equals the
   incoming workload's home CQ name. Cross-cluster preemption is for
   cross-CQ reclaim within a cohort; within-CQ preemption (e.g. a
   higher-priority workload in tenant-a-cq displacing another tenant-a-cq
   workload on a sibling worker) is the worker's local
   `WithinClusterQueue` policy concern, not this dispatcher's.
5. For each remaining member, check `Spec.Preemption.ReclaimWithinCohort != Never`.
   If `Never`, skip — that CQ has opted out of being a victim source.
6. **Quota-based reclaim filter**: skip members where
   `cqIsBorrowing(member) == false`, i.e. the CQ is at-or-below its nominal
   quota for every resource the incoming workload requests. CQs within
   their nominal own that quota and must not be evicted to satisfy a
   sibling. (Mirrors single-cluster `cqIsBorrowing` gating.)
7. List admitted workloads on each remaining member's CQ. Filter to
   candidates eligible for preemption per the CQ's policy:
   - `LowerPriority`: `victim.Spec.Priority < incoming.Spec.Priority`
   - `Any`: any victim allowed
8. **BorrowWithinCohort gate (preemptor side)**: compute whether admitting
   the incoming workload would leave the manager-side CQ above its
   nominal. If yes, the preemption is "preempt while borrowing":
   - If `manager.Spec.Preemption.BorrowWithinCohort` is nil or
     `Policy=Never`, drop all candidates (we can't borrow while preempting).
   - If `Policy=LowerPriority`, keep only candidates where both
     `victim.Priority < incoming.Priority` AND (when
     `MaxPriorityThreshold` is set) `victim.Priority <= MaxPriorityThreshold`.
     The threshold is a victim-side cap: victims above the threshold are
     protected even if the incoming workload outranks them. Mirrors the
     upstream API godoc on `BorrowWithinCohort.MaxPriorityThreshold` and
     single-cluster `isAboveBorrowingThreshold`.
9. Filter out victims with an existing `victim-of` annotation belonging to
   a different incoming workload.
10. Sort candidates: priority ascending, creation timestamp ascending,
    name. Pick the lowest.
11. ClaimAndEvict the chosen victim. On `ErrVictimAlreadyClaimed`, retry
    with the next candidate.
12. On successful eviction, write `wl.Status.NominatedClusterNames` to the
    freed cluster only.
13. If no candidate exists, write all cohort member clusters as nominated
    (fallback all-at-once over the cohort).

### Per-CQ preemption policy

Each worker `ClusterQueue.Spec.Preemption.ReclaimWithinCohort` semantics
mirror single-cluster:

- **`Never`** (default): this CQ is never a victim source for cross-cluster
  preemption.
- **`LowerPriority`**: workloads in this CQ are eligible victims iff (a) the
  CQ is currently borrowing for the contended resource AND (b) their priority
  is strictly less than the incoming workload's priority.
- **`Any`**: workloads in this CQ are eligible victims iff the CQ is currently
  borrowing for the contended resource (priority ignored).

`Spec.Preemption.BorrowWithinCohort` on the **manager-side CQ** controls
whether the dispatcher may evict cohort siblings while leaving the
preemptor's own CQ above nominal. Mirrors single-cluster:

- `nil` or `Policy=Never` (default): no preemption when admitting the
  incoming would leave the manager-side CQ above its nominal quota.
- `Policy=LowerPriority`: allowed iff `victim.Priority < incoming.Priority`
  AND (when `MaxPriorityThreshold` is set) `victim.Priority <= MaxPriorityThreshold`.
  The threshold is a victim-side cap (matches the upstream API godoc on
  `BorrowWithinCohort.MaxPriorityThreshold` and single-cluster
  `isAboveBorrowingThreshold`); victims above the threshold are
  protected from borrow-preemption even when the incoming workload
  outranks them on `Spec.Priority`.

`Spec.Preemption.WithinClusterQueue` is honored locally by each worker's own
Kueue scheduler; cross-cluster preemption does not change its semantics.

### Single-writer lock: victim-claim annotation

Concurrent reconciles for two different incoming workloads must not pick the
same victim. The dispatcher claims a victim by patching annotation
`kueue.x-k8s.io/multikueue-cross-preemption-victim-of` =
`<incomingNamespace>/<incomingName>` on the remote victim Workload, using
optimistic concurrency. On conflict the dispatcher re-fetches and:

- If the annotation now holds a different value, returns
  `ErrVictimAlreadyClaimed` (caller picks another candidate).
- If still unset, the patch is retried.

### Eviction primitive

The dispatcher targets the **manager-side** Workload (not the remote)
when evicting. The manager client is the single source of truth for the
workload's admission state, and MultiKueue already has a clean cleanup
path for "manager workload no longer has quota reservation".

The evictor calls `workload.PatchAdmissionStatus` against the manager
client with a function that:

- Calls `workload.SetEvictedCondition` with reason
  `WorkloadEvictedByPreemption` and a message identifying the incoming
  workload + the freed cluster.
- Clears `Status.ClusterName` and `Status.NominatedClusterNames`.

What happens after the patch:

1. Kueue's manager-side workload controller observes `Evicted=True`,
   suspends the local Job, and clears `Status.Admission` (so
   `HasQuotaReservation -> false`).
2. MultiKueue's `wlReconciler` observes the no-quota state and runs its
   section 2 (`Delete all remote workloads when the local workload is
   finished or has no quota reservation`), deleting the remote Workload
   and remote Job. The worker's cohort capacity is freed.
3. The dispatcher has already written
   `incoming.Status.NominatedClusterNames = [freedCluster]`, so MultiKueue
   dispatches the incoming workload there; the worker admits it using
   the just-freed cohort capacity.
4. The evicted workload re-enters the manager admission queue and gets
   re-dispatched by MultiKueue (potentially to another worker, or stays
   pending if no capacity).

Eviction is idempotent — already-evicted workloads are no-ops.

**Why not evict on the worker side?** A worker-side eviction (setting
`Evicted=True` on the remote Workload) is undone almost immediately by
the worker's own scheduler: while cohort capacity is still free
(borrower's CQ at usage>0, lender's CQ at usage=0 since the incoming
hasn't taken its slot yet on the worker), the worker re-admits the same
workload. Eviction on the manager-side avoids that race because the
remote Workload is *deleted*, not just status-flipped.

### Interaction with KEP-8303 (Orchestrated Preemption)

KEP-8303 introduced `MultiKueuePreemptionGate` to gate parallel preemptions
of *one workload's* replicas across multiple workers in `AllAtOnce` mode.
This KEP introduces a *separate* annotation (`...victim-of`) for a
*different concern* (claiming a sibling's workload as a preemption victim).
Both can exist on the same Workload but mean different things and never
collide.

Both feature gates can be enabled simultaneously: the cross-cluster
dispatcher nominates a single freed cluster (no AllAtOnce race to
coordinate), so KEP-8303's `MultiKueuePreemptionGate` machinery never
activates on workloads our dispatcher touches.

This is validated by an integration test
(`test/integration/multikueue/scheduler/cross_cluster_preemption_test.go`,
`When MultiKueueOrchestratedPreemption is also enabled`): with both
feature gates on, the test asserts the dispatcher still selects + evicts
its cross-cluster victim AND that no workload in the cohort gains a
`Spec.PreemptionGates` entry or a `WorkloadBlockedOnPreemptionGates`
condition.

### Test Plan

[X] I/we understand the owners of the involved components may require
updates to existing tests to make this code solid enough prior to
committing the changes necessary to implement this enhancement.

#### Prerequisite testing updates

None. The existing MultiKueue test wrappers and fake remote-view
abstraction were sufficient; the cross-cluster preemption tests build on
top without modifying upstream test infrastructure.

#### Unit tests

`pkg/controller/workloaddispatcher`:
- `crosspreemption_test.go` — 12 scenarios covering priority-based
  victim selection:
  - Fallback when manager CQ has no cohortName.
  - Happy path: lowest-priority victim found, evicted, nominated.
  - No victim (priorities outprioritize incoming): fallback all-at-once.
  - Multi-candidate selection: lowest priority wins.
  - Already-claimed victim: dispatcher skips and tries next.
  - `ReclaimWithinCohort=Never` excludes a CQ from victim sourcing.
  - `ReclaimWithinCohort=Any` allows equal-priority preemption.
  - **`SkipsNonBorrowingCQ`**: a CQ at-or-below its nominal is not a
    victim source, even with `ReclaimWithinCohort=Any` (quota-based
    reclaim parity).
  - **`BorrowWithinCohortNeverBlocksPreemption`**: when admitting would
    leave the manager-side CQ above nominal and `BorrowWithinCohort` is
    unset (Never), no preemption fires.
  - **`BorrowWithinCohortLowerPriorityAllowsBelowVictim`**: when manager
    CQ would borrow and `BorrowWithinCohort.Policy=LowerPriority`,
    preemption proceeds for victims of strictly lower priority.
  - **`SkipsIncomingWorkloadOwnCQ`**: cross-cluster preemption is
    cross-CQ only — the incoming workload's own ClusterQueue is never a
    victim source (within-CQ preemption is the worker's local
    `WithinClusterQueue` policy concern, not this dispatcher's).
  - **`FallbackDedupsClusterNames`**: when multiple cohort-member CQs
    share a worker cluster (e.g. per-tenant CQs in the same cohort), the
    all-at-once fallback writes a deduped cluster list to
    `Status.NominatedClusterNames`.
- `crosspreemption_quota_test.go` — 13 table-driven cases pinning the
  upstream semantics of `canBorrowAgainstVictim`. Cover the nil-spec
  and `Policy=Never` short-circuits, the standard `LowerPriority`
  check, and every `MaxPriorityThreshold` case using Apple Ray's
  priority tiers (`p0=-100`, `p1=-500`, `p2=-1000`) — including the
  boundary case where the victim is at the threshold (eligible) and
  the protection case where the victim is above the threshold even
  though it is below the incoming priority.

#### Integration tests

`test/integration/multikueue/scheduler/cross_cluster_preemption_test.go`,
running on the existing 1-manager + 2-worker envtest harness. Topology
adapted from the canonical worked example with two adjustments so all
four scenarios can share one setup:

  - Per-worker nominal=1 CPU (instead of 4 GPU) — envtest has no GPUs;
    the dispatcher logic is resource-agnostic so the demonstration is
    identical.
  - Tenant B's `ReclaimWithinCohort=LowerPriority` (instead of `Any`) —
    the priority-gate scenario below needs a priority delta to be
    meaningful; with `Any`, B is preemptible at any priority and the
    priority gate is unobservable.

Two tenants share a cohort: A is the owner (`ReclaimWithinCohort=Never`,
per-worker nominal=1), B is the borrower (`ReclaimWithinCohort=LowerPriority`,
per-worker nominal=0). Tenant B fills both workers via cohort borrow;
tenant A then arrives.

- Happy path: tenant A's high-priority workload reclaims by evicting a
  borrowing tenant-B workload (asserted via the WorkloadEvicted
  condition's preserved Message).
- Priority gate: when B's borrowers are higher priority than A's
  reclaimer, no eviction fires.
- Already-claimed victim: a pre-stamped
  `MultiKueueCrossClusterPreemptionVictimAnnotation` causes the
  dispatcher to skip that victim.
- Joint mode with KEP-8303 (`MultiKueueOrchestratedPreemption=true`):
  dispatcher still evicts its victim, and KEP-8303's PreemptionGate
  machinery does not activate on any workload in the cohort.
- `BorrowWithinCohort.MaxPriorityThreshold` protects the victim:
  tenant A is at its manager-side nominal so admitting a second
  workload triggers borrow-preemption; tenant B is borrowing at a
  priority above the threshold; the dispatcher must NOT evict B even
  though `incoming.Priority > victim.Priority` (the threshold is a
  victim-side cap).

#### e2e tests

No automated e2e tests beyond the integration suite. The feature has
been manually validated end-to-end on a kind-based harness with 1
manager + 2 worker clusters running the modified Kueue manager binary;
the prototype is referenced from the
[Implementation History](#implementation-history) section.

### Graduation Criteria

**Alpha → Beta:**
- Field experience from at least one production deployment.
- Resolution of any race conditions discovered in production.
- Cached cohort-member discovery with watch-based invalidation, to
  replace the per-reconcile listing.

**Beta → Stable:**
- Multi-victim packing.
- Scheduler-integrated implementation (the architecturally pure variant
  considered in [Alternatives](#alternatives)).

## Implementation History

- KEP proposed.

## Drawbacks

- Reading remote `ClusterQueue.Status.FlavorsUsage` at reconcile time
  introduces a small staleness window relative to the worker's actual
  state. For preemption decisions this is acceptable (idempotent eviction,
  retry on conflict), but operators should be aware that the dispatcher
  isn't using a strongly consistent cohort snapshot the way the in-tree
  scheduler does for single-cluster preemption.
- Per-reconcile cohort member discovery. Each dispatcher reconcile
  lists worker `ClusterQueue` objects and groups by `spec.cohortName`.
  For deployments with hundreds of tenants this adds latency; a cached
  member list with watch-based invalidation is a Beta improvement.
- The dispatcher does not coordinate with KEP-8303's
  `MultiKueueOrchestratedPreemption`. Both feature gates may be enabled
  simultaneously — our dispatcher nominates a single freed cluster, so
  KEP-8303's `MultiKueuePreemptionGate` never activates on workloads we
  touch (validated by the joint-mode integration test) — but the two
  paths are independent. A future KEP can unify them.

## Alternatives

**Introduce a separate `MultiKueueCohort` CRD that mirrors single-cluster
`Cohort`.** An earlier draft of this KEP shipped such a CRD as the
forward-compat marker for cross-cluster cohort identity. Rejected after
implementation because it added no behavior in alpha (its spec fields
were "accepted but not enforced"), duplicated the single-cluster `Cohort`
shape one-for-one, and forced users to learn a MultiKueue-specific shape
for the same concept they already use in single-cluster Kueue. The
shared `cohortName` string + standard `Cohort` CR cover every alpha and
beta need; if a future KEP requires multi-cluster-only fields, they can
be added to the existing `Cohort` (e.g. `Cohort.status.crossCluster`)
without a new CRD.

**Cohort membership enumerated as `(clusterName, clusterQueue)` pairs on
a cluster-scoped CR on the manager.** Rejected because it diverges from
the single-cluster Cohort API where membership is via
`ClusterQueue.spec.cohortName`, forcing operators to maintain a parallel
membership list as workers come and go.

**New AdmissionCheck controller.** Sit alongside MultiKueue's admission
check; check for preemption opportunity. Rejected because it duplicates
kubeconfig watching, GC, and RBAC infrastructure for the same fundamental
purpose as the dispatcher. The External dispatcher extension point is a
cleaner seam.

**Modify the in-tree scheduler.** Extend
`pkg/scheduler/preemption/preemption.go` to consider remote ClusterQueues
as cohort siblings, reversing KEP-693's "synchronize configuration across
the clusters" non-goal. Rejected for alpha because it requires a fresh
KEP-level discussion to align maintainers and rebuild the scheduler's
data model. The current additive approach can serve as evidence for that
future discussion.
