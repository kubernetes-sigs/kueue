# KEP-10852: Inadmissible Workloads Observability

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1: Operator Alerting for Configuration Errors](#story-1-operator-alerting-for-configuration-errors)
    - [Story 2: Platform Team Diagnostics and Dashboards](#story-2-platform-team-diagnostics-and-dashboards)
    - [Story 3: Programmatic Clients and Automation Tools](#story-3-programmatic-clients-and-automation-tools)
  - [Notes/Constraints/Caveats](#notesconstraintscaveats)
    - [API and Backwards Compatibility](#api-and-backwards-compatibility)
    - [Status Patch Co-location for Performance](#status-patch-co-location-for-performance)
  - [Risks and Mitigations](#risks-and-mitigations)
    - [API Server Write Volume and Load](#api-server-write-volume-and-load)
- [Upgrade / Downgrade &amp; Backwards Compatibility Strategy](#upgrade--downgrade--backwards-compatibility-strategy)
  - [Upgrade Path](#upgrade-path)
  - [Downgrade Path](#downgrade-path)
- [Design Details](#design-details)
  - [Tiered Reason Precedence &amp; Resolution](#tiered-reason-precedence--resolution)
    - [Multi-Flavor Precedence Resolution](#multi-flavor-precedence-resolution)
  - [Admitted Condition Initialization and Lifecycle](#admitted-condition-initialization-and-lifecycle)
    - [Simplification: Removal of NoReservationUnsatisfiedChecks Reason](#simplification-removal-of-noreservationunsatisfiedchecks-reason)
  - [Prometheus Metrics Schema](#prometheus-metrics-schema)
  - [Troubleshooting &amp; End-User Inspection](#troubleshooting--end-user-inspection)
    - [Concrete Status Scenarios (Before &amp; After)](#concrete-status-scenarios-before--after)
      - [Scenario 1: Newly Created Workload (Initial Reconcile)](#scenario-1-newly-created-workload-initial-reconcile)
      - [Scenario 2: Waiting for Cluster Capacity (Waiting for Quota)](#scenario-2-waiting-for-cluster-capacity-waiting-for-quota)
      - [Scenario 3: Configuration Error (e.g. Missing Queue or Flavor Mismatch)](#scenario-3-configuration-error-eg-missing-queue-or-flavor-mismatch)
  - [Future Work](#future-work)
  - [Test Plan](#test-plan)
    - [Unit Tests](#unit-tests)
    - [Integration Tests](#integration-tests)
  - [Graduation Criteria](#graduation-criteria)
    - [Beta (v0.19)](#beta-v019)
    - [GA (v0.20)](#ga-v020)
- [Alternatives Considered](#alternatives-considered)
  - [Introducing a New Separate Status Condition Instead of Updating QuotaReserved Reasons](#introducing-a-new-separate-status-condition-instead-of-updating-quotareserved-reasons)
<!-- /toc -->

## Summary

This KEP introduces enhanced observability for pending and inadmissible
workloads in Kueue. The feature improves diagnostic capabilities by
distinguishing configuration-related failures (such as resource flavor
mismatches, missing local queues, and requests exceeding maximum limits) from
general resource availability waits. It introduces granular, priority-tiered
status reasons for when a workload lacks a quota reservation (`QuotaReserved`
condition status is `False`), along with a detailed set of Prometheus
metrics to monitor and alert on unadmitted workloads.

To ensure safe operational rollouts, the functionality is divided behind two
separate feature gates:
- `UnadmittedWorkloadsObservability`: Controls the overall feature.
  It enables the granular Prometheus metrics (`kueue_unadmitted_workloads`
  and `kueue_local_queue_unadmitted_workloads`) and updates status reasons for
  the `QuotaReserved` and `Admitted` conditions during reconciliations and
  scheduler cycles to use the new tiered reasons instead of the generic
  "Pending".
- `UnadmittedWorkloadsExplicitStatus`: Gates the immediate, proactive
  initialization of both `QuotaReserved` and `Admitted` status conditions
  to `False` (with `PendingEvaluation` and `NoReservation` reasons) during
  a workload's first reconciliation. 
  * **When enabled:** Both conditions are explicitly written immediately
    upon creation, ensuring continuous status-based lifecycle tracking.
  * **When disabled:** Workloads are created with empty status
    conditions (saving API server write volume under heavy load), and
    conditions are only updated on subsequent scheduler cycles.

## Motivation

Currently, when a workload cannot be admitted due to incompatibility with any
available `ResourceFlavor` (e.g., untolerated taints, node affinity mismatch, or
incorrect label/selector values), the `QuotaReserved` condition is set to
`False` with a generic reason of `Pending`. 

Using `Pending` as a reason is sub-optimal because a status condition's `Reason`
field is intended to provide actionable context for its `Status` (answering the
question: "Why is the quota not reserved?"). Saying a condition is `False` due
to being `Pending` merely restates the wait state instead of providing the
underlying cause.

Programmatic clients, dashboard operators, and monitoring tools cannot easily
distinguish a configuration-related "flavor mismatch" or "missing queue" state
from a workload that is simply waiting for capacity under an otherwise valid
configuration.

Furthermore, the existing pending workloads metrics (`kueue_pending_workloads`
and `kueue_local_queue_pending_workloads`) only categorize pending workloads under
two statuses:
- `active`: In the admission queue.
- `inadmissible`: Failed admission attempt and waiting for cluster conditions
  to change.

Note that the "Inadmissible" condition `Reason` is currently used both for
misconfigured queue setups (such as referencing a non-existent queue) and for
suspended/inactive queues. **Crucially, workloads referencing a non-existent
`LocalQueue` or `ClusterQueue` are not counted in these metrics, as they never
successfully enter any queue structure in the manager or cache.**

This lack of granularity and the overlapping terminology create substantial
operational confusion:
1. Workloads with critical configuration issues (such as referencing a missing
   queue) are completely omitted from the pending metrics instead of being
   flagged as problematic.
2. All other valid workloads that are simply waiting for resource availability
   are grouped together under the same massive, generic `inadmissible` label,
   making it impossible to distinguish between a healthy capacity wait state
   and actual configuration errors.

This lack of granularity and terminology conflict makes it difficult to detect,
alert on, and resolve workloads stuck due to configuration faults versus those
that are waiting normally for cluster capacity.

### Goals

- Introduce a priority-ordered list of reasons for when the `QuotaReserved`
  condition status is `False` to separate structural/configuration issues from
  normal capacity waiting states.
- Introduce metrics (`kueue_unadmitted_workloads` and
  `kueue_local_queue_unadmitted_workloads`) detailing the reasons and
  underlying causes of why workloads are unadmitted.
- Initialize status conditions to `False` on the first reconciliation cycle
  of a workload to allow continuous lifetime tracking that matches tracking
  from workload status and the metrics.

### Non-Goals

- Modifying the underlying workload admission state machine or scheduling
  decisions.
- Automatically correcting or mutating workload specs when configurations are
  invalid.
- Exposing scheduling queue internals beyond standard condition transitions and
  aggregated metrics.

## Proposal

To address the limitations in diagnostics, the proposal details:
1. Standardized reasons for the `QuotaReserved` condition (when status is `False`)
   resolved by a priority tier model.
2. Dynamic first-cycle initialization of status conditions to guarantee
   detailed lifecycle visibility.
3. Decoupling the `Admitted` and `QuotaReserved` lifecycle by removing the
   hybrid reason `NoReservationUnsatisfiedChecks`.
4. A multi-level metrics reporting schema to provide detailed alerts for
   common configuration mistakes.

### User Stories

#### Story 1: Operator Alerting for Configuration Errors

As a cluster operator, I want to create Prometheus alerts when a tenant submits
a workload with a typo in their `LocalQueue` label or an invalid
toleration configuration that prevents resource flavor matching. Currently,
these workloads are either omitted from metrics or included into a generic
`inadmissible` group, making specific alerting impossible without writing custom
controllers. With the new `kueue_unadmitted_workloads` metric, we can define
alerts targeting the `Misconfigured` underlying cause, notifying tenants to fix
their configurations immediately.

#### Story 2: Platform Team Diagnostics and Dashboards

As a platform team, we want to construct a unified Grafana dashboard to track
queue queueing characteristics in real-time. We need to distinguish between
workloads waiting for normal quota capacity (a standard queueing event) versus
workloads blocked because they request resources exceeding maximum limits or
because their LocalQueue or ClusterQueue has an active `StopPolicy`.
The two-level label scheme (`reason` and `underlying_cause`) allows our team to
plot a stacked area chart showing queue metrics broken down by blocking category,
improving platform diagnostics.

#### Story 3: Programmatic Clients and Automation Tools

As a developer building orchestration tools on top of Kueue, I want to
programmatically inspect the status of a workload and execute recovery tasks.
With distinct, parseable reason tokens like `WaitingForQuota` or `Misconfigured` instead
of the generic `Pending` status string, programmatic clients can take separate,
automated actions (such as auto-canceling misconfigured jobs or scaling up
node groups for capacity-bound jobs) without needing fragile regex matches on the
condition's message text.

### Notes/Constraints/Caveats

#### API and Backwards Compatibility

Replacing the legacy `Pending` reason token for the `QuotaReserved` condition
with granular tokens might affect external programmatic clients that strictly
string-match on the `Pending` reason. While the condition status (`False`) remains
unchanged, this change presents a potential risk for legacy client integrations.

To minimize this risk:
- The feature is isolated behind feature gates. Although the gates are enabled
  by default starting in the initial Beta milestone (v0.19), operators can
  disable them to revert to legacy behavior if needed.
- External systems are encouraged to match on condition status first, and use standard
  reason tokens as a supplementary classification.

#### Status Patch Co-location for Performance

To prevent double-patching and keep API request count minimal, first-cycle status writes
are co-located within existing update routines:
- **No Admission Checks**: The reconciler initializes both conditions to `False` in
  exactly one unified status patch.
- **With Admission Checks**: The registration of pending admission checks and the
  initialization of the `False` status conditions are batched together into a single
  API transaction.

### Risks and Mitigations

#### API Server Write Volume and Load

Initializing conditions to `False` on the first reconciliation cycle of newly created
workloads increases the total number of API writes per workload, which could generate
significant API server load and database write spikes under high workload submission
velocities.

To mitigate this concern:
- The status initialization logic is decoupled from the main metric gates and
  placed behind a distinct feature gate (`UnadmittedWorkloadsExplicitStatus`).
- This separation allows operators to disable explicit status write overhead under heavy
  load environments while still benefiting from real-time Prometheus metrics.
- Performance scaling validation will be monitored and analyzed using early
  feedback from the opt-in backport to v0.18.

## Upgrade / Downgrade & Backwards Compatibility Strategy

### Upgrade Path

Upon enabling the feature gates, existing workloads will have their status reasons
updated to the new tiered values during their next reconciliation or scheduler
cycle.

### Downgrade Path

Disabling the feature gates will cause Kueue to revert to using the generic
"Pending" reason. Existing metrics series for unadmitted workloads will stop
being updated.

## Design Details

### Tiered Reason Precedence & Resolution

To separate different blocks and keep the status field highly structured, when
the `QuotaReserved` status is `False`, the controller will resolve the reason
field according to a strict priority hierarchy. Lower tier numbers represent
structural failures and block scheduling early, thus taking absolute precedence
over higher tiers.

Tiers are evaluated either at the **workload-wide** level (independent of flavor
assignments) or at the **per-flavor** assignment level:

| Tier | Scope | Classification | Reason Token | Description / Scenario | Location in Memory |
| :--- | :--- | :--- | :--- | :--- | :--- |
| **Tier 1** | Workload-wide | Workload Deactivation | `Deactivated` | Workload is explicitly deactivated (`spec.active: false`). | None |
| **Tier 2** | Mixed | Structural Blockers | `Misconfigured` | Permanent structural error preventing admission:<br>- Missing local/cluster queue (Workload-wide)<br>- Resource flavor mismatch (Per-flavor)<br>- DRA misconfiguration (Workload-wide) | Missing local/cluster queue - None<br>ResourceFlavor mismatch - `ClusterQueue.inadmissibleWorkloads`<br>DRA issues - None |
| **Tier 3** | Per-flavor | Structural Quota Blockers | `ExceedsMaxQuota` | Workload requests resource quantities exceeding ClusterQueue or Cohort maximum limits. | `ClusterQueue.inadmissibleWorkloads` |
| **Tier 4** | Workload-wide | Orchestration & Administrative Holds | `Suspended`, `AdmissionGated`, `WaitingForPodsReady` | Workload is structurally valid, but admission is halted due to:<br>- Administrative hold (the queue's StopPolicy is active).<br>- Gated state (AdmissionGatedBy annotation).<br>- Scheduling hold (waiting for previously admitted workloads to reach PodsReady under waitForPodsReady configuration). | AdmissionGated - None<br>StopPolicy - None<br>WaitingForPodsReady - blocks and waits |
| **Tier 5** | Per-flavor | Resource Deficits | `WaitingForQuota` | Workload fits within the maximum limits of the ClusterQueue or Cohort, but the cluster currently lacks sufficient unreserved capacity (including when preemption is required but is blocked by preemption gates). | `ClusterQueue.inadmissibleWorkloads` |
| **Tier 6** | Workload-wide | Active Queueing | `PendingEvaluation` | Workload is submitted, structurally valid, resides actively in the queue, and is awaiting its capacity evaluation. Set also after eviction. | `ClusterQueue.heap` |

*Note: Precedence Resolution examples:*
- *If a workload requests resource quantities exceeding the ClusterQueue
  maximum limits (Tier 3) but is also blocked by an unsatisfied
  admission gate (Tier 4), the resolved reason is written as
  `ExceedsMaxQuota` due to Tier 3 priority.*
- *If a workload is waiting for capacity (Tier 5) but also has unsatisfied
  admission gates (Tier 4), the resolved reason is written as
  `AdmissionGated` due to Tier 4 priority.*

#### Multi-Flavor Precedence Resolution

When a workload is evaluated against multiple `ResourceFlavor` paths within a
ClusterQueue, Kueue attempts to find the most schedulable assignment. If
scheduling fails, the blocker tier and reason reported in the workload status
are emitted from the most schedulable flavor assignment candidate (i.e., the
candidate that blocks at the highest tier number).

This ensures that operators receive actionable alerts regarding the closest
viable scheduling path (e.g., a limits or capacity block on a compatible
flavor) rather than generic configuration mismatch reports from incompatible
flavors.

### Admitted Condition Initialization and Lifecycle

When the `UnadmittedWorkloadsExplicitStatus` feature gate is enabled, both
status conditions are explicitly initialized to `False` during the first
reconciliation cycle to enable continuous tracking from workload creation,
with their reasons dynamically resolved based on the workload state and queue
parameters:
- `QuotaReserved`: `False` (with the reason dynamically resolved according to
  the Tiered Precedence model, e.g., `PendingEvaluation` for normal active
  queueing, `Deactivated` if inactive, or `Misconfigured` if queue validation
  fails early).
- `Admitted`: `False` (with the reason dynamically resolved to `NoReservation`
  on the first cycle).

#### Simplification: Removal of NoReservationUnsatisfiedChecks Reason

To simplify status reasoning and decouple the `QuotaReserved` and `Admitted`
condition lifecycles, this design explicitly avoids using the hybrid reason
`NoReservationUnsatisfiedChecks` at all when a workload simultaneously lacks a
quota reservation and has unsatisfied admission checks.

Instead:
- A workload without a quota reservation will always have its `Admitted`
  condition reason set to `NoReservation`, regardless of the state of its
  admission checks.
- Once quota reservation is successfully obtained (`QuotaReserved` status is
  `True`), if admission checks are still pending, the `Admitted` condition
  will transition its reason to `UnsatisfiedChecks`.

### Prometheus Metrics Schema

A set of metrics is introduced to track unadmitted workloads when the
`Admitted` condition status is `False`. The metrics are:
- `kueue_unadmitted_workloads`: Tracks unadmitted workloads at the
  ClusterQueue level. It includes the following labels:
  - `cluster_queue`: The name of the ClusterQueue.
  - `reason`: Mapped 1:1 to the reason for the `Admitted` condition
    being `False` (e.g., `NoReservation`, `UnsatisfiedChecks`,
    `PendingDelayedTopologyRequests`).
  - `underlying_cause`: Mapped 1:1 to the proposed priority reasons for the
    `QuotaReserved` condition status being `False` (e.g., `PendingEvaluation`,
    `Misconfigured`, `Suspended`, `WaitingForPodsReady`, `WaitingForQuota`,
    `AdmissionGated`).
- `kueue_local_queue_unadmitted_workloads`: Tracks unadmitted workloads at the
  LocalQueue level. It includes the following labels:
  - `name`: The name of the LocalQueue.
  - `namespace`: The namespace of the LocalQueue.
  - `cluster_queue`: The name of the ClusterQueue.
  - `reason`: Mapped 1:1 to the reason for the `Admitted` condition
    being `False` (e.g., `NoReservation`, `UnsatisfiedChecks`,
    `PendingDelayedTopologyRequests`).
  - `underlying_cause`: Mapped 1:1 to the proposed priority reasons for the
    `QuotaReserved` condition status being `False` (e.g., `PendingEvaluation`,
    `Misconfigured`, `Suspended`, `WaitingForPodsReady`, `WaitingForQuota`,
    `AdmissionGated`).

If a workload has successfully obtained a quota reservation (`QuotaReserved` is `True`),
the `underlying_cause` label is populated as follows:
- For `UnsatisfiedChecks`: Set to `ChecksNotReady`.
- For `PendingDelayedTopologyRequests`: Set to `PendingTopology`.

**Handling of Missing or Unset Status Conditions**

If the `UnadmittedWorkloadsExplicitStatus` feature gate is disabled, a newly
created workload will have no status conditions set. For the purpose of metrics
tracking:
- The missing `Admitted` condition is assumed to represent `False` with the
  reason `NoReservation`.
- The missing `QuotaReserved` condition is assumed to represent `False` with the
  reason `PendingEvaluation`.

This ensures that pending workloads are fully tracked from the moment of their
creation, regardless of whether explicit status initialization is active.

When the `reason` label is `NoReservation` (the workload lacks a quota
reservation), the `underlying_cause` label maps 1:1 to the `QuotaReserved`
condition's reason (covering all 6 precedence tiers). Representative examples
of these mapping combinations are detailed below:

| Admitted Reason | QuotaReserved Reason | `reason` Label | `underlying_cause` Label | Description / Scenario |
| :--- | :--- | :--- | :--- | :--- |
| `NoReservation` | `False (WaitingForQuota)` | `NoReservation` | `WaitingForQuota` | Workload is waiting for queue capacity. |
| `NoReservation` | `False (Misconfigured)` | `NoReservation` | `Misconfigured` | Workload has structural/configuration errors. |
| `UnsatisfiedChecks` | `True (N/A)` | `UnsatisfiedChecks` | `ChecksNotReady` | Quota is reserved, but blocked by pending admission checks. |
| `PendingDelayedTopologyRequests` | `True (N/A)` | `PendingDelayedTopologyRequests` | `PendingTopology` | Quota is reserved, but blocked by delayed topology paths. |


### Troubleshooting & End-User Inspection

Users can inspect why a workload is pending by looking at the `QuotaReserved`
and `Admitted` conditions in the Workload status, as well as the Detailed
Workload Status (`.status.admissionChecks`). By using `kubectl get workload`,
administrators can access these granular reasons to enable automated
troubleshooting scripts and more precise Grafana dashboards, allowing them to
quickly distinguish between cluster-wide resource exhaustion and individual
workload configuration errors.

#### Concrete Status Scenarios (Before & After)

Below are detailed before-and-after YAML comparisons highlighting the changes across common scheduling states.

##### Scenario 1: Newly Created Workload (Initial Reconcile)

Newly submitted workloads in the scheduling queue awaiting their first evaluation.

* **Before KEP-10852:** Both conditions are completely absent from the status:
```yaml
status: {}
```

* **After KEP-10852:** Explicitly updated status:
```yaml
status:
  conditions:
  # QuotaReserved condition is ONLY initialized/present on creation if the
  # UnadmittedWorkloadsExplicitStatus gate is ENABLED. If disabled, it is absent.
  - type: QuotaReserved
    status: "False"
    reason: PendingEvaluation # Tier 6 Reason: Awaiting initial scheduler evaluation
    message: "Workload is pending evaluation in the scheduling queue"
    observedGeneration: 1
  # Admitted condition is ONLY initialized/present on creation if the
  # UnadmittedWorkloadsExplicitStatus gate is ENABLED. If disabled, it is absent.
  - type: Admitted
    status: "False"
    reason: NoReservation
    message: "The workload has no reservation"
    observedGeneration: 1
```

##### Scenario 2: Waiting for Cluster Capacity (Waiting for Quota)

The workload is structurally valid but must wait because the ClusterQueue has insufficient capacity.

* **Before KEP-10852:** Uses generic `Pending` reason and the `Admitted` condition is completely absent:
```yaml
status:
  conditions:
  - type: QuotaReserved
    status: "False"
    reason: Pending
    message: "couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor default-flavor, 2 more needed"
    observedGeneration: 1
```

* **After KEP-10852:** Updated status:
```yaml
status:
  conditions:
  - type: QuotaReserved
    status: "False"
    reason: WaitingForQuota # Tier 5 Reason
    message: "couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor default-flavor, 2 more needed"
    observedGeneration: 1
  # Admitted condition is ONLY initialized/present if the
  # UnadmittedWorkloadsExplicitStatus gate is ENABLED. If disabled, it remains absent.
  - type: Admitted
    status: "False"
    reason: NoReservation
    message: "The workload has no reservation"
    observedGeneration: 1
```

##### Scenario 3: Configuration Error (e.g. Missing Queue or Flavor Mismatch)

The workload points to a non-existent queue, blocking scheduling evaluation early.

* **Before KEP-10852:**
```yaml
status:
  conditions:
  - type: QuotaReserved
    status: "False"
    reason: Inadmissible
    message: "LocalQueue local-queue doesn't exist"
    observedGeneration: 1
```

* **After KEP-10852:** Updated status:
```yaml
status:
  conditions:
  - type: QuotaReserved
    status: "False"
    reason: Misconfigured # Tier 2 Reason
    message: "LocalQueue local-queue doesn't exist"
    observedGeneration: 1
  # Admitted condition is ONLY initialized/present if the
  # UnadmittedWorkloadsExplicitStatus gate is ENABLED. If disabled, it remains absent.
  - type: Admitted
    status: "False"
    reason: NoReservation
    message: "The workload has no reservation"
    observedGeneration: 1
```



### Future Work

- **Skip Scheduling Queue for Specific Inadmissible Workloads**: Avoid adding
  workloads that fail due to a `ResourceFlavor` mismatch (Tier 2
  `Misconfigured`) or exceed limits (Tier 3 `ExceedsMaxQuota`) to the
  `inadmissibleWorkloads` list. Since these workloads cannot become
  schedulable until a cluster configuration changes (such as creating a
  missing resource flavor or increasing maximum limits), keeping them out of
  the active scheduling loop reduces scheduler evaluation overhead.

### Test Plan

[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough before committing the changes
necessary to implement this enhancement.

#### Unit Tests

- **Precedence Tier Resolver Tests**: Verify correct classification across all 6 precedence tiers,
  confirming proper ranking decisions (e.g., structural flavor issues take absolute priority over
  requeue suspensions).
- **Metrics Lifecycle Unit Tests**: Verify dynamic metric updates, correct
  label resolution under diverse workload states, and correct timeseries cleanup.
- **Webhook Status Update Integration Unit Tests**: Verify proper batching of initial condition writes
  and registration tasks, confirming exactly one single write transaction is performed.

#### Integration Tests

- **Lifecycle Integration Tests**: Submit various valid and invalid workloads (such as missing queues,
  unsatisfied limits, and normal capacity wait queues) and assert that:
  - Status conditions are properly initialized to `False` (when the gate is active).
  - Condition reasons and metric values are updated to match the expected design matrix.
  - Deleting or admitting the workloads decrements dynamic metrics back to zero.
- **Scale and Load Testing**: Run scale benchmarks (up to 5,000 workloads) to evaluate API server write
  latency and controller CPU profiles when `UnadmittedWorkloadsExplicitStatus` is enabled.

### Graduation Criteria

#### Beta (v0.19)

- Feature gates enabled by default.
- Unit and integration tests for unadmitted metrics and status conditions.
- Initial Alpha backport to v0.18 via cherry-pick release to collect early
  opt-in feedback.
- Validate scale performance of first-cycle status writes (when
  `UnadmittedWorkloadsExplicitStatus` is enabled) under heavy load to ensure
  no API server saturation or request latency issues.

#### GA (v0.20)

- Feature gates locked to true.

## Alternatives Considered

### Introducing a New Separate Status Condition Instead of Updating QuotaReserved Reasons

An alternative considered was leaving the existing `QuotaReserved` condition's reason as a generic
`Pending` value to eliminate backwards-compatibility risks for older scripts, and instead introducing
a separate condition type (such as `QuotaAllocated` or `QuotaAcquisition`) to carry the granular,
tiered reasons.

- **Pros**: Zero backwards-compatibility risk for legacy scripts that expect the exact string `Pending`
  on the `QuotaReserved` condition.
- **Cons**: Substantially increases status API complexity and introduces condition redundancy. Under
  Kubernetes API design guidelines, a status condition's `Reason` is explicitly intended to explain
  the cause of its `Status`. Creating a parallel condition simply to explain the reason of another
  active condition violates typical API design structures, cluttering the status surface of the
  Workload resource. Therefore, updating the existing condition reasons with proper documentation
  and feature gate isolation was chosen as the correct architecture.
