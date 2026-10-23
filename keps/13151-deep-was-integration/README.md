# KEP-8871: Workload-Aware Scheduling Deep Integration via Scheduler Library

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1: Preventing split-brain decisions between Kueue admission and kube-scheduler](#story-1-preventing-split-brain-decisions-between-kueue-admission-and-kube-scheduler)
    - [Story 2: Using kube-scheduler and WAS plugins during Kueue admission](#story-2-using-kube-scheduler-and-was-plugins-during-kueue-admission)
  - [Notes, Constraints, and Caveats](#notes-constraints-and-caveats)
  - [Risks and Mitigations](#risks-and-mitigations)
    - [Non-deterministic node placement across scheduling cycles](#non-deterministic-node-placement-across-scheduling-cycles)
    - [Reduced placement optimality during preemption backfill](#reduced-placement-optimality-during-preemption-backfill)
    - [Increased scheduling cycle latency on large clusters](#increased-scheduling-cycle-latency-on-large-clusters)
    - [Fair sharing DRS shifts during multi-victim preemption](#fair-sharing-drs-shifts-during-multi-victim-preemption)
- [Design Details](#design-details)
  - [Feature Gates](#feature-gates)
  - [Assignment Planning Architecture](#assignment-planning-architecture)
  - [Relationship to the Native Kueue Scheduling Path](#relationship-to-the-native-kueue-scheduling-path)
  - [Cluster Snapshot and Virtualization](#cluster-snapshot-and-virtualization)
    - [Real Pod Tracking](#real-pod-tracking)
    - [Virtualizing Admitted and Assumed Workloads](#virtualizing-admitted-and-assumed-workloads)
    - [Virtualizing the Candidate Workload and PodGroups](#virtualizing-the-candidate-workload-and-podgroups)
  - [Delegating Topology-Aware Scheduling](#delegating-topology-aware-scheduling)
  - [Delegating Workload Preemption](#delegating-workload-preemption)
    - [Comparison of Native Kueue Preemption and WAS Integration](#comparison-of-native-kueue-preemption-and-was-integration)
    - [WAS Integration Preemption Protocol](#was-integration-preemption-protocol)
  - [Interaction with Other Kueue Features](#interaction-with-other-kueue-features)
  - [Observability](#observability)
  - [Test Plan](#test-plan)
    - [Prerequisite testing updates](#prerequisite-testing-updates)
    - [Unit tests](#unit-tests)
    - [Integration tests](#integration-tests)
    - [e2e tests](#e2e-tests)
  - [Graduation Criteria](#graduation-criteria)
    - [Alpha](#alpha)
    - [Beta (disabled by default)](#beta-disabled-by-default)
    - [Beta (enabled by default)](#beta-enabled-by-default)
    - [Stable](#stable)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
  - [Maintain and extend bespoke TAS and node filtering in Kueue](#maintain-and-extend-bespoke-tas-and-node-filtering-in-kueue)
  - [Mirror Kueue's linear forward and backward preemption passes in WAS](#mirror-kueues-linear-forward-and-backward-preemption-passes-in-was)
  - [Stateful cross-cycle pod virtualization](#stateful-cross-cycle-pod-virtualization)
  - [Materialize synthetic WAS PodGroup objects in etcd](#materialize-synthetic-was-podgroup-objects-in-etcd)
- [Future Work](#future-work)
  - [Consuming native WAS PodGroup and CompositePodGroup resources](#consuming-native-was-podgroup-and-compositepodgroup-resources)
<!-- /toc -->

## Summary

This KEP proposes replacing a major part of Kueue's internal scheduling logic—most notably Topology-Aware Scheduling (TAS) placement and preemption simulation—by delegating whole-workload scheduling simulation to the upstream Kubernetes [Scheduler Library](https://github.com/kubernetes-sigs/scheduler-library) (`sigs.k8s.io/scheduler-library`).

By running the upstream `kube-scheduler` and Workload-Aware Scheduling (WAS) plugin framework against an in-memory cluster snapshot during admission, `kueue-controller-manager` will:

- Make placement decisions that are consistent with `kube-scheduler`.
- Support standard and custom scheduler plugins without reimplementing them in Kueue.
- Maintain feature parity with existing Kueue functionality while keeping multi-tenant quota and queueing policies in Kueue.

## Motivation

To support Topology-Aware Scheduling (TAS) and topology-aware preemption, `kueue-controller-manager` has grown into a *quasi-scheduler* with its own domain tree search and preemption placement engine.
Meanwhile, upstream Kubernetes (`sig-scheduling`) is building native **Workload-Aware Scheduling (WAS)** in `kube-scheduler` and packaging its scheduling framework, plugins, and workload simulation primitives into `scheduler-library`.

Maintaining a parallel workload placement implementation inside Kueue causes three main problems:

- **Inconsistent decisions ("split-brain" between Kueue and `kube-scheduler`)**:
  Kueue's internal placement logic is only an approximation of `kube-scheduler`. As a result, `kueue-controller-manager` can decide a workload fits (or preempt running workloads for it), admit it, and pin its Pods to specific nodes—only for `kube-scheduler` to reject those Pods as `Unschedulable`.
- **Inability to use `kube-scheduler` and WAS plugins**:
  Kueue's internal TAS solver does not execute `kube-scheduler` framework plugins (`PreFilter`, `Filter`, `Score`, `Reserve`) or WAS workload-level plugins. Upstream features (such as inter-pod affinity, topology spread, volume binding, or Dynamic Resource Allocation) and custom scheduler plugins are ignored unless manually reimplemented in Kueue.
- **Duplicated scheduling logic**:
  Kueue maintains custom code for topological domain packing and preemption feasibility that duplicates capabilities being built into WAS and `scheduler-library`.

This KEP proposes delegating whole-workload TAS placement and preemption simulation from Kueue to `scheduler-library` while preserving feature parity with Kueue's existing capabilities.
Kueue will remain responsible for multi-tenant policy (quotas, cohorts, priorities, and fair sharing) and will rely on `scheduler-library` as the authoritative placement engine shared with `kube-scheduler`.

### Goals

- Replace Kueue's internal TAS placement and preemption simulation logic by delegating whole-workload scheduling simulation to `scheduler-library`.
- Ensure consistent placement decisions between `kueue-controller-manager` and `kube-scheduler` so workloads admitted by Kueue will be schedulable by `kube-scheduler`.
- Allow `kube-scheduler` and WAS plugins to execute during Kueue's admission and topology placement simulation without reimplementing plugin logic in Kueue.
- Achieve feature parity with Kueue's existing TAS and preemption capabilities by combining Kueue's victim ordering policies with `scheduler-library`'s placement simulation.

### Non-Goals

- Modifying or replacing Kueue's multi-tenant queueing, `ClusterQueue` / `Cohort` quota management, or Fair Sharing policy rules.
- Replacing `kube-scheduler` as the final Pod binding authority in the cluster.
- Requiring external workload controllers (such as `batch/v1.Job`, `JobSet`, or `LeaderWorkerSet`) to create WAS `PodGroup` or `CompositePodGroup` objects in the API server in the initial phase.
- Introducing new Kueue user-facing APIs or deprecating Kueue's `Workload` and `PodSet` abstractions.

## Proposal

This KEP proposes delegating whole-workload TAS placement and preemption simulation in `kueue-controller-manager` to `scheduler-library` (`ScheduleWorkload`):

- `kueue-controller-manager` will translate a candidate Kueue `Workload` into virtual Pods and virtual WAS `PodGroup` / `CompositePodGroup` objects in the `ClusterSnapshot`.
- `scheduler-library` will execute the configured `kube-scheduler` and WAS plugin pipeline across the workload and any ordered preemption victim candidates.
- `kueue-controller-manager` will convert the returned Pod-to-Node placements and selected preemption victims into Kueue's `TopologyAssignment` and eviction plan.

### User Stories

#### Story 1: Preventing split-brain decisions between Kueue admission and kube-scheduler

Without `scheduler-library`, Kueue uses simplified node capacity and topology placement logic instead of running the full `kube-scheduler` framework.
Consequently, Kueue can admit a workload (or preempt running workloads for it) and pin its Pods to specific nodes, only for `kube-scheduler` to mark those Pods `Unschedulable` due to pod-level or cross-pod constraints (such as inter-pod anti-affinity, `hostPort` conflicts, topology spread, or volume node affinity).

With scheduling logic delegated to `scheduler-library`:

1. A user will submit a workload combining TAS topology requirements with standard Kubernetes scheduling constraints (for example, inter-pod anti-affinity or `hostPort`s across Pods in a gang).
2. During admission, `kueue-controller-manager` will delegate placement simulation to `scheduler-library`, which will evaluate the candidate's virtual Pods using the same `kube-scheduler` and WAS plugins that `kube-scheduler` runs at bind time.
3. If the plugins determine that the Pods cannot be co-located on the available nodes in the topology domain, `scheduler-library` will report a placement failure.
4. `kueue-controller-manager` will keep the workload queued (and avoid unnecessary preemptions) until a topology domain is available where `kube-scheduler` can bind the Pods.

#### Story 2: Using kube-scheduler and WAS plugins during Kueue admission

A cluster administrator or platform team relies on `kube-scheduler` and WAS plugins—such as upstream workload-aware topology plugins, Dynamic Resource Allocation (`DynamicResources`), or custom hardware/topology plugins—to enforce placement rules.
Currently, Kueue's internal TAS solver cannot execute `kube-scheduler` or WAS plugins; supporting any new plugin requires reimplementing its logic inside Kueue.

With this proposal:

1. The `scheduler-library` instance in `kueue-controller-manager` will be configured with the cluster's `kube-scheduler` and WAS plugins.
2. When evaluating a queued workload for admission or preemption, `kueue-controller-manager` will invoke `scheduler-library` to run the configured plugin pipeline (`PreFilter`, `Filter`, `Score`, `Reserve`, and WAS workload plugins) against the `ClusterSnapshot`.
3. Kueue will respect the feasibility and scoring decisions of those plugins when computing the `TopologyAssignment` and selecting preemption victims, without custom plugin reimplementation in Kueue.

### Notes, Constraints, and Caveats

- **External dependency maturity**:
  This integration will depend on `sigs.k8s.io/scheduler-library` and the upstream `scheduling.k8s.io` APIs (`PodGroup` in `v1beta1`, `CompositePodGroup` in `v1alpha3`). Because virtual `PodGroup` and `CompositePodGroup` objects will be constructed in memory within the `ClusterSnapshot`, the Kubernetes API server will not need those CRDs enabled.
- **Hostname topology level requirement**:
  Because `scheduler-library` assigns Pods to concrete `Node` objects, this integration will require any `Topology` used by a TAS `ResourceFlavor` to include `kubernetes.io/hostname` as its lowest level.

### Risks and Mitigations

#### Non-deterministic node placement across scheduling cycles

When multiple nodes or domains have tied scores, `kube-scheduler` and `scheduler-library` evaluate nodes concurrently and do not guarantee deterministic tie-breaking across runs.
Because preemption spans multiple cycles while victims terminate, non-deterministic placement could cause `kueue-controller-manager` to pick a different victim set on cycle $N+1$ than it evicted on cycle $N$.

**Mitigation**:
- `kueue-controller-manager` will pass in-flight victims already marked for preemption (`CommittedVictims`) into the `scheduler-library` simulation.
- `scheduler-library` will first evaluate whether evicting `CommittedVictims` alone is sufficient to place the workload before considering any new candidates from `PotentialVictims`.
- While exact node assignments within the freed domain may vary between cycles, the set of preempted workloads will remain stable unless cluster state changes invalidate the placement.

#### Reduced placement optimality during preemption backfill

Native Kueue preemption re-adds candidate victims one by one during the backward pass ("fill-back") and recalculates the full TAS placement on each step, which can relocate the preemptor to save a higher-priority victim.
Running a full placement recalculation per victim in `scheduler-library` would be too expensive, so `scheduler-library` will perform in-place reprieval without recalculating the preemptor's node assignment.

**Mitigation**:
- During the preemption forward pass, `scheduler-library` will bound the candidate prefix using `PreemptionFilter.MayFit()` and prefix search.
- Node scoring will prefer placing preemptor Pods onto nodes occupied by victims closer to the front of Kueue's ordered candidate list (lower index = better preemption target).

#### Increased scheduling cycle latency on large clusters

Running `kube-scheduler` `PreFilter`, `Filter`, `Score`, and WAS topology plugins in `scheduler-library` will perform more work per evaluation than Kueue's native capacity arithmetic.

**Mitigation**:
- `PreemptionFilter.MayFit()` will short-circuit placement evaluations until enough victims have been accumulated to satisfy Kueue quota and Fair Sharing constraints, and prefix search will bound the number of full placement evaluations to $O(\log N)$.
- The integration will be guarded by the `SchedulerLibraryDeepIntegration` feature gate for safe opt-in and rollback.
- Dedicated benchmark suites will measure scheduling cycle duration before Beta graduation.

#### Fair sharing DRS shifts during multi-victim preemption

Preempting multiple workloads from the same `ClusterQueue` shifts its Dominant Resource Share (DRS) after each victim removal, which can cause sub-optimal candidate ordering ([#14122](https://github.com/kubernetes-sigs/kueue/issues/14122)) or theoretical preemption loops during backfill ([#14543](https://github.com/kubernetes-sigs/kueue/issues/14543)).

**Mitigation**:
- Candidate ordering passed to `scheduler-library` via `PotentialVictims` will pre-account for cumulative DRS shifts during the forward pass.
- `kueue-controller-manager` will supply a `PreemptionFilter` (`PreemptVictim`, `UnpreemptVictim`, `MayFit`) so `scheduler-library` checks Kueue quota and Fair Sharing constraints before attempting placement in the forward pass and before reprieving any victim in the backward pass.

## Design Details

### Feature Gates

| Feature Gate | Stage | Default | Since | Dependencies | Description |
|---|---|---|---|---|---|
| `SchedulerLibraryDeepIntegration` | Alpha | `false` | `v0.21` | `TopologyAwareScheduling` | Will enable delegating TAS workload placement and preemption simulation to `ScheduleWorkload` in `scheduler-library`. |

- When `SchedulerLibraryDeepIntegration` is disabled, `kueue-controller-manager` will use the native assignment planner.

### Assignment Planning Architecture

During each scheduling cycle, `kueue-controller-manager` will evaluate a candidate Kueue `Workload` in two stages:

1. **Initial Quota and Flavor Assignment**:
   Evaluates `ClusterQueue` and `Cohort` quotas across available `ResourceFlavor`s and assigns an initial mode per `PodSet` (`Fit`, `Preempt`, or `NoFit`).
2. **Assignment and Preemption Planning**:
   If initial quota evaluation is not `NoFit`, an assignment planner will compute the `TopologyAssignment` and preemption targets:
   - **Native Planner (`SchedulerLibraryDeepIntegration=false`)**: Runs Kueue's internal TAS tree algorithm and native preemption loop.
   - **WAS Planner (`SchedulerLibraryDeepIntegration=true`)**: Will virtualize the candidate `Workload` into Pods and WAS `PodGroup`s, call `ScheduleWorkload` on the `ClusterSnapshot`, and convert the returned placements and victims into the assignment plan.

### Relationship to the Native Kueue Scheduling Path

The native Kueue planner and the WAS planner represent different scheduling trade-offs:

- **Native Kueue path**:
  Optimized for low-latency admission and deterministic, fine-grained victim-set minimization (recalculating topological placement on every preemption backfill step) using simplified domain capacity math, at the cost of approximating `kube-scheduler` and not running `kube-scheduler` or WAS plugins.
- **WAS integration path (`SchedulerLibraryDeepIntegration`)**:
  Optimized for placement consistency with `kube-scheduler` and native execution of `kube-scheduler` and WAS plugins, at the cost of higher per-cycle simulation overhead, non-deterministic tie-breaking in node scoring, and in-place preemption reprieval without placement recalculation.

Because replacing the core placement and preemption engine is a significant architectural and behavioral shift, the transition will follow a staged coexistence model:

1. **Feature parity before default enablement**:
   `SchedulerLibraryDeepIntegration` will remain disabled by default through Alpha and into an initial Beta stage (`Beta (disabled by default)`). It will not be enabled by default until it reaches feature parity with native Kueue TAS and proves stability and acceptable performance in production (`Beta (enabled by default)`).
2. **Long-term coexistence vs. replacement**:
   Graduating `SchedulerLibraryDeepIntegration` to Stable and deprecating the native Kueue TAS path will require demonstrating clear superiority over the native algorithm in placement fidelity, extensibility, and acceptable scheduling throughput. If certain cluster environments or workload shapes continue to require the lower latency, strict determinism, or backfill optimality of the native Kueue algorithm, Kueue may maintain both assignment planner paths long-term.

### Cluster Snapshot and Virtualization

Admitting a Kueue `Workload` updates `etcd` asynchronously, after which the workload controller creates real Pods and `kube-scheduler` binds them asynchronously.
Because `scheduler-library` operates on `Pod`, `PodGroup`, and `CompositePodGroup` objects rather than Kueue `Workload` objects, `kueue-controller-manager` will bridge this gap when building the `ClusterSnapshot` at the start of each scheduling cycle.

#### Real Pod Tracking

When `SchedulerLibraryDeepIntegration` is enabled:

- `kueue-controller-manager` will stamp the `kueue.x-k8s.io/workload` annotation onto Pods created by Kueue-managed jobs and track all cluster Pods via an informer.
- Tracked Pods will be indexed by `Namespace/Name` and by owning Kueue `Workload` key (`Namespace/WorkloadName`).
- Non-TAS Pods (such as `DaemonSet` or static Pods) will be included in the informer cache so `scheduler-library` accounts for their node resource usage and host ports.

#### Virtualizing Admitted and Assumed Workloads

To prevent double-booking nodes while admitted Kueue `Workload`s are still in flight (assumed in memory or waiting for real Pods to be created and bound), `kueue-controller-manager` will reconstruct virtual Running Pods at the start of each scheduling cycle:

- **Virtual Pod creation**: For every unfinished, admitted or assumed `Workload` whose `TopologyAssignment` ends at `kubernetes.io/hostname`, `kueue-controller-manager` will generate one virtual `Pod` in phase `Running` per assigned slot with `spec.nodeName` set to the assigned hostname.
- **Deduplication**: If real Pods for that `Workload` are already in the Pod tracker, their tracked entries will be replaced by the virtualized representation for that snapshot so resources are never counted twice.
- **Stateless across cycles**: Virtualization will be computed fresh per cycle from admitted and assumed `Workload`s, avoiding cross-cycle state transitions for individual virtual Pods.

#### Virtualizing the Candidate Workload and PodGroups

When evaluating a candidate Kueue `Workload`, the WAS planner will construct:

1. **Candidate Virtual Pods**:
   Will generate `count` virtual `Pod` objects in phase `Pending` per `PodSet`, merging:
   - The `PodSet`'s `PodTemplateSpec` (labels, annotations, and `PodSpec`).
   - The assigned `ResourceFlavor`'s `nodeLabels` (into `spec.nodeSelector`) and `tolerations`.
   - Active `AdmissionCheck` `PodSetUpdates` (labels, annotations, node selectors, and tolerations).
   - The `kueue.x-k8s.io/pod-set` label and `kueue.x-k8s.io/workload` annotation.
   - `spec.schedulerName` normalized to `default-scheduler` to match the simulator profile.
2. **Virtual `PodGroup` and `CompositePodGroup`**:
   - Will build in-memory WAS `PodGroup` (and `CompositePodGroup` for multi-`PodSet` or sliced workloads) objects with a collision-free name (`virtual-pg-<workload>-<hash>`).
   - Will set `GangSchedulingPolicy.MinCount` to the total Pod count.
   - Will translate TAS annotations (`kueue.x-k8s.io/podset-required-topology` and `kueue.x-k8s.io/podset-slice-required-topology`) into WAS topology constraints and register them in the `ClusterSnapshot`.

### Delegating Topology-Aware Scheduling

Once the candidate virtual Pods and virtual `PodGroup` hierarchy are registered in the `ClusterSnapshot`, the WAS planner will invoke `ScheduleWorkload`:

1. **Plugin execution**: `scheduler-library` will run the configured `kube-scheduler` (`PreFilter`, `Filter`, `Score`) and WAS Topology-Aware Scheduling plugins across the virtual Pods as a gang hierarchy.
2. **Placement output**: `ScheduleWorkload` will return a `WorkloadSchedulingResult` containing overall `Status` and `PodResults` (`[]SchedulingResult`), where each `SchedulingResult` includes either:
   - `SelectedNodeName`: the `Node` assigned to the Pod, or
   - `Status`: the plugin failure reasons explaining why the Pod could not be placed (or `skipped` if an earlier Pod in the gang failed).
3. **Result translation**: `kueue-controller-manager` will group `PodResults` by `kueue.x-k8s.io/pod-set`:
   - If any virtual Pod fails placement, the `Workload` will be marked `NoFit` (`Reason: SchedulingSimulationFailed`) and the plugin failure reasons will be recorded on the `PodSet` status.
   - If all virtual Pods succeed, `kueue-controller-manager` will aggregate the per-node Pod counts into the `TopologyAssignment` stored in `Workload.status.admission.podSetAssignments`.

### Delegating Workload Preemption

#### Comparison of Native Kueue Preemption and WAS Integration

| Aspect | Native Kueue Preemption | WAS Integration |
|---|---|---|
| **Candidate Ordering** | Evaluates candidates dynamically inside Kueue's preemption loop, updating Fair Sharing DRS after each step. | Pre-computes a lazy ordered victim sequence (`PotentialVictims iter.Seq[PreemptionVictim]`) in `kueue-controller-manager` and passes it to `scheduler-library`. |
| **Quota & Fair Sharing Gating** | Checks `ClusterQueue` / `Cohort` quota and Fair Sharing directly inside Kueue's internal preemption loop. | Passes a `PreemptionFilter` callback (`PreemptVictim`, `UnpreemptVictim`, `MayFit`) so `scheduler-library` queries Kueue before running placement or reprieving a victim. |
| **Forward Pass** | Removes candidates linearly one-by-one ($O(N)$ placement evaluations). | First tests `CommittedVictims`, then advances through `PotentialVictims` (skipping placement while `PreemptionFilter.MayFit()` is `false`) using bounded prefix search. |
| **Placement & Node Selection** | Runs Kueue's internal TAS domain capacity math over the exact prefix of victims removed so far. | Runs `kube-scheduler` and WAS plugins in `scheduler-library` (`PodGroupPostFilter` / `DefaultPreemption`) over the candidate prefix, scoring nodes to prefer those held by earlier candidates. |
| **Backfill / Reprieval** | Re-adds victims one-by-one and **recalculates full TAS placement** on every step, allowing the preemptor to relocate to save a victim. | Reprieves victims in-place **without recalculating placement**, restoring only victims that pass `PreemptionFilter` (`UnpreemptVictim` + `MayFit()`) and whose nodes do not conflict with the chosen placement. |
| **Multi-Cycle Determinism** | Fully deterministic placement and victim selection across cycles. | Concurrent node scoring can break ties non-deterministically; victim selection is stabilized across cycles via `CommittedVictims`. |

#### WAS Integration Preemption Protocol

To delegate preemption simulation to `scheduler-library` while preserving Kueue's multi-tenant victim ordering and quota rules, `ScheduleWorkload` accepts `WorkloadPreemptionOptions` (embedded in `ScheduleWorkloadOptions`) and returns `WorkloadSchedulingResult`:

```go
type PreemptionVictim interface {
	Pods() []*corev1.Pod
}

type PreemptionFilter interface {
	PreemptVictim(v PreemptionVictim)
	UnpreemptVictim(v PreemptionVictim)
	MayFit() bool
}

type WorkloadPreemptionOptions struct {
	PotentialVictims iter.Seq[PreemptionVictim]
	CommittedVictims []PreemptionVictim
	PreemptionFilter PreemptionFilter
}

type WorkloadSchedulingResult struct {
	Status            *fwk.Status
	PodResults        []SchedulingResult
	PreemptionVictims []PreemptionVictim
}
```

When initial flavor assignment indicates `Preempt`:

1. **Candidate Ordering and `PreemptionFilter` (`kueue-controller-manager`)**:
   - Will order potential victim `Workload`s using Kueue's preemption policy and wrap each candidate in a `PreemptionVictim` implementation (`Pods() []*corev1.Pod`) yielded lazily via `PotentialVictims` (`iter.Seq[PreemptionVictim]`).
   - Will provide a `PreemptionFilter` implementation that tracks reclaimed `ClusterQueue` / `Cohort` quota and Fair Sharing DRS across `PreemptVictim` and `UnpreemptVictim` calls and reports whether quota is satisfied via `MayFit()`.
2. **Preserving In-Flight Preemptions (`CommittedVictims`)**:
   - Will pass victims already evicted in a previous scheduling cycle that are still terminating in `CommittedVictims` (`[]PreemptionVictim`).
3. **Preemption Search and Reprieval (`scheduler-library`)**:
   - Will first attempt to schedule the candidate workload without preemption; if that fails and `CommittedVictims` is non-empty, will remove `CommittedVictims` from the snapshot and check if the workload fits before evaluating `PotentialVictims`.
   - Otherwise, will iterate through `PotentialVictims`, calling `PreemptionFilter.PreemptVictim(v)` for each candidate and skipping placement checks while `PreemptionFilter.MayFit()` is `false`.
   - Once `MayFit()` returns `true`, will evaluate candidate prefixes via `PodGroupPostFilter` (`DefaultPreemption` / `PodGroupEvaluator`), preferring nodes occupied by earlier victims in `PotentialVictims`.
   - During in-place reprieval, will check `PreemptionFilter` (`UnpreemptVictim` followed by `MayFit()`, reverting via `PreemptVictim` if `false`) before attempting to restore each victim's Pods, ensuring reprieval preserves both Kueue quota/Fair Sharing rules and the chosen node placement.
   - Will return `WorkloadSchedulingResult` containing `PodResults` and `PreemptionVictims` (`CommittedVictims` plus non-reprieved `PotentialVictims`).
4. **Actuation (`kueue-controller-manager`)**:
   - Will extract the underlying Kueue `Workload`s from `WorkloadSchedulingResult.PreemptionVictims` and evict them.

### Interaction with Other Kueue Features

To reach feature parity while delegating placement to `scheduler-library`, existing Kueue features will map to the WAS simulation as follows:

- **Required and Slice Topology (`podset-required-topology`, `podset-slice-required-topology`)**:
  Will be translated into virtual `PodGroup` and `CompositePodGroup` topology constraints in the `ClusterSnapshot` and evaluated by WAS topology plugins in Alpha.
- **Unconstrained Topology (`podset-unconstrained-topology`)**:
  Will be evaluated as standard pod placement on TAS nodes in `scheduler-library` in Alpha.
- **Preferred Topology, Multi-Layer Slices, and Balanced Placement (`podset-preferred-topology`, `podset-slice-required-topology-constraints`, `TASBalancedPlacement`)**:
  Will not be supported in Alpha until upstream WAS adds corresponding plugin support.
- **Cross-PodSet Grouping (`podset-group-name`)**:
  Will be grouped into a virtual `PodGroup` or `CompositePodGroup` in Alpha, with full multi-`PodSet` co-location parity targeted for Beta.
- **Partial Admission (`PartialAdmission`)**:
  Will be supported in Alpha by re-running the `scheduler-library` simulation with reduced `PodSet` counts.
- **Node Failure Recovery (`TASFailedNodeReplacement`) and Elastic Scaling (`ElasticJobsViaWorkloadSlices`)**:
  Will be deferred to Beta.
- **`AdmissionCheck` `PodSetUpdates` and `DynamicResourceAllocation`**:
  `PodSetUpdates` will be merged into candidate virtual Pods before simulation, and `ResourceClaim`s will be evaluated by the `DynamicResources` plugin in `scheduler-library`.

### Observability

When `SchedulerLibraryDeepIntegration` is enabled, `kueue-controller-manager` will surface simulation outcomes through `Workload` conditions and events:

- **`QuotaReserved=False` (`Reasons: SchedulingSimulationFailed`)**:
  Will be set when `scheduler-library` cannot place one or more virtual Pods. The message will include the per-`PodSet` and per-Pod failure reasons returned by the scheduler plugins. `SchedulingSimulationFailed` will initially serve as a temporary catch-all reason until `scheduler-library` exposes structured failure details that can be mapped to more granular `QuotaReserved` reasons (such as `TopologyPlacementFailed`).
- **`QuotaReserved=False` (`Reasons: AssignmentError`)**:
  Will be set when the assignment planner encounters an internal error (such as a failure to virtualize the `Workload` or an internal simulator failure), and a `Warning` event will be emitted on the `Workload`.

### Test Plan

[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.

#### Prerequisite testing updates

The TAS unit and integration test suites will be parameterized to run identical scenarios against both native TAS and `SchedulerLibraryDeepIntegration`.

#### Unit tests

Unit tests will cover:

- Virtual Pod generation for admitted, assumed, and candidate `Workload`s, including `ResourceFlavor` labels/tolerations and `AdmissionCheck` `PodSetUpdates`.
- Virtual `PodGroup` and `CompositePodGroup` construction, collision-free naming, and topology constraint translation.
- Pod tracking and snapshot deduplication between real and virtual Pods.
- Simulator execution for workload placement and preemption simulation.
- WAS assignment planning for `NoFit`, `Fit`, multi-`PodSet` placement, plugin failure aggregation, unknown topology node errors, and preemption delegation.
- End-to-end scheduler cycle unit tests with `SchedulerLibraryDeepIntegration` enabled.

#### Integration tests

Integration tests will cover:

- Pod annotation injection (`kueue.x-k8s.io/workload`) across `Job`, `JobSet`, `LeaderWorkerSet`, and `StatefulSet` reconcilers when `SchedulerLibraryDeepIntegration` is enabled.
- Workload admission and `TopologyAssignment` generation via `scheduler-library`, verifying that assumed workloads reserve capacity in the `ClusterSnapshot` across consecutive cycles before real Pods are bound.
- Preemption candidate selection and eviction with Classical and Fair Sharing preemption delegated to `scheduler-library`.

#### e2e tests

Dedicated end-to-end targets will run the TAS e2e suites against a Kind cluster with WAS and `SchedulerLibraryDeepIntegration` enabled:

- `test-e2e-was-deep-tas-baseline`
- `test-e2e-was-deep-tas-extended`

### Graduation Criteria

#### Alpha

- `SchedulerLibraryDeepIntegration` feature gate implemented and disabled by default (`default: false`).
- Virtual Pod and virtual `PodGroup` / `CompositePodGroup` generation integrated into the `ClusterSnapshot`.
- Assignment planner abstraction implemented, with the WAS planner delegating placement to `ScheduleWorkload` and converting results into `TopologyAssignment`s.
- WAS integration preemption support in `scheduler-library` integrated with Kueue's preemption candidate ordering.
- Unit, integration, and e2e test targets (`test-e2e-was-deep-tas-baseline`) passing in CI.

#### Beta (disabled by default)

- Feature parity achieved with native Kueue TAS across supported TAS capabilities (including preferred topology constraints, balanced placement, multi-`PodSet` topology grouping, multi-level slice constraints, elastic workload slices, and node failure recovery).
- Structured failure reasons surfaced from `scheduler-library` for `UnadmittedWorkloadsObservability` metrics and conditions.
- Resolution of Fair Sharing DRS re-evaluation during multi-victim preemption backfill.
- All reported Alpha bugs fixed and `test-e2e-was-deep-tas-extended` passing in CI.
- `SchedulerLibraryDeepIntegration` promoted to Beta while remaining disabled by default (`default: false`).

#### Beta (enabled by default)

- Demonstrated stability and reliability in production multi-tenant clusters running `SchedulerLibraryDeepIntegration`.
- Performance benchmarks demonstrating acceptable scheduling cycle latency and memory overhead on large clusters (1,000+ nodes and deep queues with preemption).
- All reported Beta bugs fixed.
- `SchedulerLibraryDeepIntegration` enabled by default (`default: true`).

#### Stable

- Demonstrated superiority over the native Kueue placement and preemption algorithm in production workloads across placement accuracy, plugin extensibility, and acceptable scheduling throughput.
- Native consumption of `scheduling.k8s.io` `PodGroup` and `CompositePodGroup` resources when created directly by upstream workload controllers.
- Decision finalized on whether to deprecate the native Kueue TAS placement path or maintain both assignment planner paths long-term.

## Implementation History

- 2026-09: Initial prototyping of workload virtualization and `ScheduleWorkload` integration across `kueue` and `scheduler-library`.
- 2026-10: KEP drafted.

## Drawbacks

- **Higher per-cycle computational cost**:
  Running `kube-scheduler` framework plugins and constructing virtual Pods and `PodGroup`s in `ClusterSnapshot` will require more CPU and memory per scheduling cycle than Kueue's native integer capacity math.
- **Tighter coupling to upstream Kubernetes releases**:
  Because `scheduler-library` vendors `k8s.io/kubernetes` scheduler internals and WAS plugins, adopting new WAS features or fixes in Kueue will require coordinating with upstream release cadences.
- **Behavioral differences in preemption**:
  The WAS integration preemption algorithm will use exponential search and in-place reprieval rather than recalculating topological placement on every backfill step, which can produce different victim sets than native Kueue TAS preemption.

## Alternatives

### Maintain and extend bespoke TAS and node filtering in Kueue

Kueue could keep its internal TAS placement and preemption algorithms and continue reimplementing new Kubernetes scheduling features (such as DRA simulation, inter-pod affinity, and hardware topology constraints) directly in Kueue.

**Reasons for rejection**:
- Perpetuates logic duplication across `kueue-controller-manager`, `kube-scheduler`, and `cluster-autoscaler`.
- Cannot prevent split-brain admission failures when custom or evolving `kube-scheduler` plugins are active.

### Mirror Kueue's linear forward and backward preemption passes in WAS

The preemption simulation in `scheduler-library` could be remodeled to match Kueue's exact algorithm: extending the victim set one by one in the forward pass and recalculating full workload placement on every victim fill-back in the backward pass.

**Reasons for rejection**:
- Each `ScheduleWorkload` call runs the `kube-scheduler` plugin pipeline across all Pods in the gang, making $O(N)$ full placement simulations in both passes too slow for production cluster sizes.
- Exponential search in the forward pass ($O(\log N)$) combined with victim-order-aware node preference and in-place reprieval balances placement quality and latency.

### Stateful cross-cycle pod virtualization

Instead of regenerating virtual Running Pods for admitted and assumed TAS `Workload`s at the start of each scheduling cycle, `kueue-controller-manager` could maintain a stateful cache of virtual Pods across cycles and evict each virtual Pod when its real Pod appears.

**Reasons for rejection**:
- Matching individual real Pods to virtual Pods across arbitrary third-party workload controllers is error-prone; any missed transition leaves permanent ghost capacity in the snapshot.
- Rebuilding virtual Pods from admitted `Workload`s per snapshot is stateless across cycles and simpler to reason about.

### Materialize synthetic WAS PodGroup objects in etcd

Instead of injecting virtual `PodGroup` and `CompositePodGroup` objects into the in-memory `ClusterSnapshot`, `kueue-controller-manager` could create real `scheduling.k8s.io` `PodGroup` and `CompositePodGroup` resources in the API server before running `ScheduleWorkload`.

**Reasons for rejection**:
- Adds API server write latency to the admission path and requires garbage collection to prevent orphaned `PodGroup`s on crashes or preemptions.
- Risks conflicting with workload controllers as they adopt native WAS APIs.

## Future Work

### Consuming native WAS PodGroup and CompositePodGroup resources

As upstream workload controllers (`Job`, `JobSet`, `LeaderWorkerSet`, `Kubeflow` operators) natively create `scheduling.k8s.io` `PodGroup` and `CompositePodGroup` resources, `kueue-controller-manager` will watch and copy those real WAS objects into the `ClusterSnapshot` instead of synthesizing virtual `PodGroup`s from Kueue annotations.
