# KEP-8692: Composable MultiKueue Dispatcher

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1: Region-constrained GPU jobs](#story-1-region-constrained-gpu-jobs)
    - [Story 2: Cost-aware hardware-tier dispatching](#story-2-cost-aware-hardware-tier-dispatching)
    - [Story 3: Cluster taints for maintenance](#story-3-cluster-taints-for-maintenance)
    - [Story 4: Data-locality-aware dispatching for ML training](#story-4-data-locality-aware-dispatching-for-ml-training)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [Mental Model: Clusters as Nodes](#mental-model-clusters-as-nodes)
  - [Dispatch Pipeline](#dispatch-pipeline)
  - [API: MultiKueueCluster — Taints and Labels](#api-multikueuecluster--taints-and-labels)
  - [API: WorkloadSpec — Cluster Placement Fields](#api-workloadspec--cluster-placement-fields)
  - [API: ClusterDispatcherProfile CRD](#api-clusterdispatcherprofile-crd)
  - [API: ClusterQueue Changes](#api-clusterqueue-changes)
  - [Workload Annotation: Dispatch Mode](#workload-annotation-dispatch-mode)
  - [Plugin Interfaces](#plugin-interfaces)
  - [Built-in Plugins](#built-in-plugins)
    - [Filter Plugins](#filter-plugins)
    - [Score Plugins](#score-plugins)
  - [Dispatch Algorithm](#dispatch-algorithm)
  - [Interaction with Existing Dispatchers](#interaction-with-existing-dispatchers)
  - [Validation](#validation)
  - [Test Plan](#test-plan)
  - [Graduation Criteria](#graduation-criteria)
    - [Alpha](#alpha)
    - [Beta](#beta)
    - [Stable](#Stable)
  - [Observability](#observability)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Future Work](#future-work)
<!-- /toc -->

## Summary

This KEP introduces a composable, plugin-based dispatcher for MultiKueue that treats child
clusters as schedulable units — directly analogous to how the Kubernetes scheduler treats
nodes. Workloads express cluster placement requirements (`clusterSelector`, `clusterAffinity`,
`clusterTolerations`) directly in `WorkloadSpec`, mirroring how pods express node placement
via `nodeSelector`, `nodeAffinity`, and `tolerations`. Operators configure a
`Filter → Score → Normalize → Select` pipeline through a single new CRD:
`ClusterDispatcherProfile` (operator-level plugin selection and scoring weights). The
dispatcher nominates exactly one cluster at a time, offering a new dispatcher alongside the 
existing AllAtOnce and Incremental dispatchers.

## Motivation

MultiKueue today provides two dispatching strategies:

- **AllAtOnce**: nominates every cluster in the `MultiKueueConfig` simultaneously; the first
  cluster to admit the workload wins. All other nominated remote workloads are then cleaned
  up via sequential deletion. This is wasteful — O(N) remote objects are created and O(N-1)
  are deleted per admission — and admission order is non-deterministic.

- **Incremental**: nominates clusters in alphabetical batches of three, with a five-minute
  round timeout. Cluster order is unrelated to any workload or cluster property, so
  placement is essentially arbitrary.

Neither strategy allows workloads to express placement requirements ("run only on GPU clusters
in us-west-2") or operators to rank clusters by preference ("prefer A100 clusters before H100
clusters"). The only filtering today is cluster health (the `Active` condition), and that
filtering is implicit and hard-coded.

As MultiKueue deployments grow — spanning regions, cloud providers, availability zones, and
hardware tiers — both workload owners and operators need a principled way to control where
workloads land. This KEP provides that control through a workload-driven placement model
paired with a declarative operator plugin pipeline.

### Goals

- Introduce a `Filter` extension point: plugins that eliminate ineligible clusters before
  scoring (hard constraints).
- Introduce a `Score` extension point: plugins that rank remaining clusters by preference
  (soft constraints), with operator-configurable weights.
- Add `spec.clusterSelector`, `spec.clusterAffinity`, and `spec.clusterTolerations` fields
  to `WorkloadSpec`: workload-owned placement requirements, analogous to pod `nodeSelector`,
  `nodeAffinity`, and `tolerations`.
- Implement `ClusterAffinity` filter: evaluates `WorkloadSpec.ClusterSelector` and
  `WorkloadSpec.ClusterAffinity.Required` against `MultiKueueCluster` labels.
- Implement `ClusterTaints` filter: rejects clusters whose `NoSchedule` taints are not
  tolerated by `WorkloadSpec.ClusterTolerations`.
- Implement `ClusterHealth` filter: rejects clusters whose `Active` condition is not `True`.
- Implement `ClusterAffinityScore`: scores clusters by `WorkloadSpec.ClusterAffinity.Preferred`
  terms, analogous to Kubernetes preferred node affinity scoring.
- Implement `ClusterTaintScore`: reduces scores for clusters with untolerated
  `PreferNoSchedule` taints.
- Introduce `ClusterDispatcherProfile` CRD: operator-controlled plugin list and scoring
  weights.
- Top-1 nomination model (compare to AllAtOnce/Incremental).
- Gate all new behavior behind a `ComposableDispatcher` feature gate.
- Implement `ClusterFeasibility` filter: rejects clusters that cannot admit the workload
  based on quota headroom visible in `MultiKueueCluster.Status`. Blocked on
  [kubernetes-sigs/kueue#10105](https://github.com/kubernetes-sigs/kueue/issues/10105)
  (worker resource stats visibility in the manager cluster); the interface is defined at
  alpha and the implementation ships at beta.

### Non-Goals

- Out-of-tree (webhook or gRPC) plugins. The initial KEP ships in-tree plugins only; async
  out-of-tree extension is deferred (see [Alternatives](#out-of-tree-plugins-via-webhook)).
- Per-ClusterQueue scoring weight overrides. Weights are operator-level to prevent tenants
  from gaming dispatch order (see [Alternatives](#per-clusterqueue-weight-overrides)).
- Cross-cluster preemption scheduling.
- Multi-cluster topology-aware scheduling (see KEP-2724).
- Changes to how workloads are synchronized to or from child clusters after nomination.

## Proposal

Treat child clusters as first-class schedulable units. Operators label `MultiKueueCluster`
objects (e.g., `kueue.x-k8s.io/region: us-west-2`, `kueue.x-k8s.io/accelerator: a100`)
and optionally add taints. Workload owners express placement requirements directly in the
workload via `WorkloadSpec.ClusterSelector`, `WorkloadSpec.ClusterAffinity`, and
`WorkloadSpec.ClusterTolerations`, populated from well-known annotations on the owner job.
A `ClusterDispatcherProfile` defines which Filter and Score plugins run, and at what weights.
The dispatcher runs a `Filter → Score → Normalize → Select` cycle and nominates exactly one
cluster.

### User Stories

#### Story 1: Region-constrained GPU jobs

A batch administrator runs clusters in `us-west-2` and `eu-central-1`. Data-sovereignty
rules require that certain GPU training jobs stay within `us-west-2`, but this constraint
varies per job — not all workloads in the same ClusterQueue have the same residency
requirement. The admin labels each `MultiKueueCluster` with `kueue.x-k8s.io/region`.

Job authors annotate their jobs with
`kueue.x-k8s.io/cluster-selector: '{"kueue.x-k8s.io/region":"us-west-2"}'`, which Kueue
propagates to `WorkloadSpec.ClusterSelector`. The `ClusterAffinity` filter plugin evaluates
this selector against each candidate cluster's labels and rejects any cluster whose region
label does not match. Workloads without a `clusterSelector` are dispatched to any available
cluster. Workloads with the selector wait until a matching cluster can admit them rather
than crossing regions.

#### Story 2: Cost-aware hardware-tier dispatching

An organization runs two GPU clusters: one with A100s (`kueue.x-k8s.io/accelerator: a100`,
lower cost per hour) and one with H100s (`kueue.x-k8s.io/accelerator: h100`, higher cost
per hour). Some training jobs do not require peak throughput and should prefer A100s to
minimize cost; H100s are for overflow when A100 capacity is exhausted.

Job authors annotate their jobs to express a preferred affinity for `accelerator: a100`,
which Kueue propagates to `WorkloadSpec.ClusterAffinity.Preferred`. A
`ClusterDispatcherProfile` enables `ClusterAffinityScore` (weight 10). The dispatcher scores
the A100 cluster higher and nominates it first; it falls back to the H100 cluster
automatically on retry when the A100 cluster cannot admit the workload.

#### Story 3: Cluster taints for maintenance

A cluster is being upgraded. The admin adds a `NoSchedule` taint
`kueue.x-k8s.io/under-maintenance: true` to its `MultiKueueCluster`. The `ClusterTaints`
filter rejects it for all workloads whose `WorkloadSpec.ClusterTolerations` do not include
a matching toleration. Workloads that explicitly need to run on this cluster (e.g., a
migration job tied to specific hardware) can opt in by annotating the owner job with a
matching toleration, which Kueue propagates to `WorkloadSpec.ClusterTolerations`. Existing
admitted workloads are unaffected. Once the upgrade is complete, the admin removes the taint
and the cluster re-enters the candidate pool.

#### Story 4: Data-locality-aware dispatching for ML training

A team runs large LLM training jobs against a dataset stored in an object store in
`us-west-2`. Reading training data across regions adds significant network cost and
increases step time due to I/O bottlenecks. They want each workload dispatched to the
cluster whose region matches where that workload's data lives.

The admin labels each `MultiKueueCluster` with `kueue.x-k8s.io/region`. Job authors label
their workloads with `kueue.x-k8s.io/data-region: us-west-2` to indicate where their
training dataset lives. A `ClusterDispatcherProfile` enables `LocalityScore` (weight 3) and
`CapacityScore` (weight 2). The `LocalityScore` plugin reads `kueue.x-k8s.io/data-region`
from the workload and compares it against the `kueue.x-k8s.io/region` label on each
candidate cluster: a matching cluster scores 100, others score 0. Combined with
`CapacityScore`, the dispatcher strongly prefers the cluster co-located with the data when
it has headroom, and falls back to remote clusters only when local capacity is unavailable.

### Risks and Mitigations

**Risk: Misconfigured clusterSelector starves a workload.**
If a workload's `ClusterSelector` matches no cluster (e.g., a label value with a typo),
that workload will be permanently blocked. Mitigation: the dispatcher emits a
`NoFeasibleCluster` event and sets a condition on the workload; a validation webhook warns
when a `ClusterSelector` references label keys not present on any known cluster.

**Risk: Top-1 retry amplifies tail latency when the top cluster is slow to respond.**
If the highest-scoring cluster is temporarily degraded but still `Active`, the 15-minute
`workerLostTimeout` elapses before the retry moves to the next candidate. Mitigation: the
`ClusterHealth` filter removes truly inactive clusters immediately; operators can reduce
`workerLostTimeout` for time-sensitive queues. A future KEP can add a per-cluster admission
deadline separate from the lost-worker timeout.

**Risk: Scoring weights allow operator misconfiguration (e.g., all weights zero).**
If all score plugins have weight 0, all clusters receive equal final scores and selection
degenerates to arbitrary ordering. Mitigation: the profile admission webhook rejects
configurations where all active score plugins have weight 0.

**Risk: Labels on MultiKueueCluster are mutable.**
An operator removing a label from a cluster mid-dispatch could cause a workload already
nominated to that cluster to fail the affinity check on retry. Mitigation: affinity is only
evaluated at nomination time, not re-checked after nomination. Labels should be treated as
stable cluster metadata; label removal is equivalent to a soft drain.

## Design Details

### Mental Model: Clusters as Nodes

The Kubernetes scheduler matches pods to nodes. Pods express placement requirements directly
in `PodSpec`; nodes carry labels and taints. The composable dispatcher applies the same
model to MultiKueue: workloads express cluster placement requirements in `WorkloadSpec`;
`MultiKueueCluster` objects carry labels and taints.

| Pod / Node concept | MultiKueue equivalent |
|---|---|
| `pod.spec.nodeSelector` | `workload.spec.clusterSelector` |
| `pod.spec.affinity.nodeAffinity.requiredDuringScheduling...` | `workload.spec.clusterAffinity.requiredDuringScheduling...` |
| `pod.spec.affinity.nodeAffinity.preferredDuringScheduling...` | `workload.spec.clusterAffinity.preferredDuringScheduling...` |
| `pod.spec.tolerations` | `workload.spec.clusterTolerations` |
| Node labels | `MultiKueueCluster` labels |
| Node taints | `MultiKueueCluster.spec.taints` |

The composable dispatcher implements a subset of the Kubernetes scheduler pipeline:

```
PreFilter → Filter → Score → NormalizeScore → Select
```

`Select` picks the top-scoring cluster and writes it as the sole `NominatedClusterName`.
The existing `wlReconciler` machinery (remote workload creation, synchronization, cleanup)
is unchanged.

### Dispatch Pipeline

```
┌─────────────────────────────────────────────────────────┐
│  Candidate set                                          │
│  = MultiKueueConfig.Clusters                            │
└──────────────────────────┬──────────────────────────────┘
                           │
               ┌───────────▼───────────┐
               │  Filter plugins       │  (ClusterHealth,
               │  (hard constraints)   │   ClusterTolerations,
               │                       │   ClusterTaints, ...)
               └───────────┬───────────┘
                           │  feasible clusters
               ┌───────────▼───────────┐
               │  Score plugins        │  weighted sum of
               │  (soft preferences)   │  normalized [0,100] scores
               └───────────┬───────────┘
                           │  ranked clusters
               ┌───────────▼───────────┐
               │  Select               │  nominate top-1
               └───────────┬───────────┘
                           │
               NominatedClusterNames = [winner]
```

### API: MultiKueueCluster — Taints and Labels

Labels on `MultiKueueCluster.metadata.labels` serve as the primary cluster metadata surface.
Operators are expected to use them freely. This KEP defines a set of well-known label keys
under the `kueue.x-k8s.io/` prefix that built-in plugins recognize:

| Key | Applied to | Example value | Used by |
|---|---|---|---|
| `kueue.x-k8s.io/region` | MultiKueueCluster | `us-west-2` | ClusterAffinity, LocalityScore |
| `kueue.x-k8s.io/zone` | MultiKueueCluster | `us-west-2a` | ClusterAffinity |
| `kueue.x-k8s.io/accelerator` | MultiKueueCluster | `a100`, `h100` | ClusterAffinity, ClusterAffinityScore |
| `kueue.x-k8s.io/instance-family` | MultiKueueCluster | `g5`, `p4d` | ClusterAffinity |
| `kueue.x-k8s.io/data-region` | Workload | `us-west-2` | LocalityScore |

Plugins may also read arbitrary user-defined labels; the well-known set is not exhaustive.

Taints are added as a new `spec.taints` field on `MultiKueueCluster`:

```go
// ClusterTaint represents a cluster taint, analogous to a node taint.
type ClusterTaint struct {
    Key    string             `json:"key"`
    // +optional
    Value  string             `json:"value,omitempty"`
    // NoSchedule: ClusterTaints filter rejects unless tolerated.
    // PreferNoSchedule: ClusterTaintScore penalizes unless tolerated.
    Effect ClusterTaintEffect `json:"effect"`
}

// +enum
type ClusterTaintEffect string

const (
    ClusterTaintEffectNoSchedule       ClusterTaintEffect = "NoSchedule"
    ClusterTaintEffectPreferNoSchedule ClusterTaintEffect = "PreferNoSchedule"
)
```

`MultiKueueClusterSpec` gains:

```go
// +optional
// +listType=atomic
Taints []ClusterTaint `json:"taints,omitempty"`
```

### API: WorkloadSpec — Cluster Placement Fields

Three new optional fields are added to `WorkloadSpec`, mirroring the pod-to-node placement
API. These are the canonical inputs to Filter and Score plugins; all built-in plugins read
exclusively from these fields.

```go
type WorkloadSpec struct {
    // ... existing fields unchanged ...

    // ClusterSelector is a map of label key/value pairs. The target MultiKueueCluster
    // must have all listed labels to be eligible for dispatch.
    // Analogous to pod.spec.nodeSelector.
    // Only evaluated when a MultiKueue AdmissionCheck is active on the workload's
    // ClusterQueue.
    // +optional
    ClusterSelector map[string]string `json:"clusterSelector,omitempty"`

    // ClusterAffinity defines required and preferred cluster label matching rules,
    // evaluated against MultiKueueCluster labels.
    // Analogous to pod.spec.affinity.nodeAffinity.
    // +optional
    ClusterAffinity *ClusterAffinity `json:"clusterAffinity,omitempty"`

    // ClusterTolerations lists tolerations for cluster taints. The workload is
    // dispatched to a tainted cluster only if a matching toleration is present.
    // Analogous to pod.spec.tolerations.
    // +optional
    // +listType=atomic
    ClusterTolerations []ClusterToleration `json:"clusterTolerations,omitempty"`
}

// ClusterAffinity mirrors Kubernetes NodeAffinity semantics applied to clusters.
type ClusterAffinity struct {
    // RequiredDuringSchedulingIgnoredDuringExecution specifies cluster label rules
    // that MUST be satisfied. Evaluated by the ClusterAffinity filter plugin.
    // +optional
    RequiredDuringSchedulingIgnoredDuringExecution *ClusterSelector `json:"requiredDuringSchedulingIgnoredDuringExecution,omitempty"`

    // PreferredDuringSchedulingIgnoredDuringExecution specifies cluster label rules
    // that SHOULD be satisfied. Evaluated by the ClusterAffinityScore plugin.
    // +optional
    // +listType=atomic
    PreferredDuringSchedulingIgnoredDuringExecution []PreferredClusterSchedulingTerm `json:"preferredDuringSchedulingIgnoredDuringExecution,omitempty"`
}

// ClusterSelector contains a list of selector terms.
// A cluster matches if it matches ANY term (OR across terms; AND within a term).
type ClusterSelector struct {
    // +listType=atomic
    ClusterSelectorTerms []ClusterSelectorTerm `json:"clusterSelectorTerms"`
}

type ClusterSelectorTerm struct {
    // +optional
    // +listType=atomic
    MatchExpressions []metav1.LabelSelectorRequirement `json:"matchExpressions,omitempty"`
    // +optional
    MatchLabels map[string]string `json:"matchLabels,omitempty"`
}

type PreferredClusterSchedulingTerm struct {
    // +kubebuilder:validation:Minimum=1
    // +kubebuilder:validation:Maximum=100
    Weight     int32               `json:"weight"`
    Preference ClusterSelectorTerm `json:"preference"`
}

// ClusterToleration allows a workload to tolerate a cluster taint.
// Analogous to pod.spec.tolerations entries.
type ClusterToleration struct {
    // +optional
    Key string `json:"key,omitempty"`
    // +kubebuilder:default=Equal
    Operator ClusterTolerationOperator `json:"operator,omitempty"`
    // +optional
    Value string `json:"value,omitempty"`
    // +optional
    Effect ClusterTaintEffect `json:"effect,omitempty"`
}

// +enum
type ClusterTolerationOperator string

const (
    ClusterTolerationOpEqual  ClusterTolerationOperator = "Equal"
    ClusterTolerationOpExists ClusterTolerationOperator = "Exists"
)
```

#### Populating WorkloadSpec from job annotations

`WorkloadSpec.ClusterSelector`, `ClusterAffinity`, and `ClusterTolerations` are populated
by Kueue's job framework controllers from well-known annotations on the owner job. This
mirrors how pod placement fields are set in a job's pod template:

| WorkloadSpec field | Source annotation on owner job |
|---|---|
| `clusterSelector` | `kueue.x-k8s.io/cluster-selector` (JSON `map[string]string`) |
| `clusterAffinity` | `kueue.x-k8s.io/cluster-affinity` (JSON-encoded `ClusterAffinity`) |
| `clusterTolerations` | `kueue.x-k8s.io/cluster-tolerations` (JSON array of `ClusterToleration`) |

Workloads created without these annotations have nil/empty cluster placement fields; all
built-in plugins treat nil fields as no constraint (no-op).

### API: ClusterDispatcherProfile CRD

`ClusterDispatcherProfile` is a cluster-scoped CRD under `kueue.x-k8s.io/v1beta2`. It
selects which Filter and Score plugins run and assigns weights to score plugins. Only
cluster administrators can create or modify profiles.

```go
type ClusterDispatcherProfile struct {
    metav1.TypeMeta   `json:",inline"`
    metav1.ObjectMeta `json:"metadata,omitempty"`
    Spec ClusterDispatcherProfileSpec `json:"spec,omitempty"`
}

type ClusterDispatcherProfileSpec struct {
    // Plugins configures which Filter and Score plugins are active and their weights.
    Plugins DispatcherPluginSet `json:"plugins"`
}

type DispatcherPluginSet struct {
    // Filter lists the filter plugins to run, in evaluation order.
    // All listed plugins must pass for a cluster to remain a candidate.
    // If empty, defaults to [ClusterHealth, ClusterAffinity, ClusterTaints].
    // +optional
    // +listType=map
    // +listMapKey=name
    Filter []PluginRef `json:"filter,omitempty"`

    // Score lists the score plugins to run and their weights.
    // Final cluster score = sum(plugin.Weight * normalizedScore(plugin, cluster)).
    // If empty, defaults to [ClusterAffinityScore(weight=1)].
    // +optional
    // +listType=map
    // +listMapKey=name
    Score []WeightedPlugin `json:"score,omitempty"`
}

type PluginRef struct {
    Name string `json:"name"`
}

type WeightedPlugin struct {
    Name string `json:"name"`
    // +optional
    // +kubebuilder:default=1
    // +kubebuilder:validation:Minimum=0
    Weight int64 `json:"weight,omitempty"`
}
```

A profile named `default` is created by the Kueue installation with the full default
plugin set. ClusterQueues that do not reference a profile explicitly use `default`.

### API: ClusterQueue Changes

One optional field is added to `ClusterQueueSpec`:

```go
type ClusterQueueSpec struct {
    // ... existing fields unchanged ...

    // DispatcherProfileRef references a ClusterDispatcherProfile that defines
    // the plugin pipeline (filter plugins and weighted score plugins).
    // Only evaluated when DispatcherName is set to the composable dispatcher.
    // Defaults to the profile named "default" if not set.
    // +optional
    DispatcherProfileRef *DispatcherProfileReference `json:"dispatcherProfileRef,omitempty"`
}

type DispatcherProfileReference struct {
    Name string `json:"name"`
}
```

### Workload Annotation: Dispatch Mode

Workload owners control when the local admission check transitions to `Ready` by annotating
their job (or `Workload` directly) with `kueue.x-k8s.io/dispatch-mode`. The annotation is
read by the composable dispatcher at nomination time.

| Annotation value | Behaviour |
|---|---|
| `BestEffort` (default) | Admission check becomes `Ready` immediately after the workload object is created on the remote cluster, without waiting for admission confirmation. If the remote cluster later evicts the workload, the eviction propagates back to the manager via the existing eviction path. |
| `MustBeAdmitted` | Admission check becomes `Ready` only after the remote cluster sets `WorkloadAdmitted=True`. If the remote does not admit within `workerLostTimeout`, the dispatcher retries with the next-best cluster. |

The annotation is propagated from the owner job to the `Workload` object using the standard
Kueue label/annotation propagation mechanism. If the annotation is absent, `BestEffort`
is assumed.

#### BestEffort

The dispatcher scores and nominates the top cluster as normal, then creates the remote
workload object and immediately marks the local admission check `Ready`. No watch on the
remote `WorkloadAdmitted` condition is needed to unblock the local workload.

**Use when:** scoring provides high confidence that the nominated cluster has capacity (e.g.,
`CapacityScore` is active with fresh quota data), or the workload priority is low enough that
an eviction-and-retry cycle is acceptable. Reduces placement latency by eliminating the
remote admission round-trip.

#### MustBeAdmitted

The local admission check remains `Pending` until `WorkloadAdmitted=True` is observed on
the remote workload. Only then does the local workload proceed.

**Use when:** resource guarantees matter more than placement speed, or when scoring cannot
reliably predict cluster capacity. Each failed nomination adds up to `workerLostTimeout`
(default 15 min) of retry latency.

#### Mode comparison

| | BestEffort (default) | MustBeAdmitted |
|---|---|---|
| Admission check becomes Ready | On remote workload creation | On remote `WorkloadAdmitted=True` |
| Retry trigger | Eviction propagation from remote | `workerLostTimeout` expiry |
| Placement latency | Lower | Higher per failed cluster |
| Reservation guarantee | Optimistic | Confirmed |
| Recommended with | `CapacityScore` enabled | Any scoring configuration |

### Plugin Interfaces

Plugins are registered in an in-process registry at startup, analogous to the Kubernetes
scheduler framework.

```go
type Registry map[string]PluginFactory
type PluginFactory func(config runtime.Object) (Plugin, error)

type Plugin interface {
    Name() string
}

type FilterPlugin interface {
    Plugin
    // Filter returns Success if the cluster is eligible; non-Success removes the cluster
    // from the candidate set. Returning an error requeues the workload.
    Filter(ctx context.Context, state *CycleState, wl *kueue.Workload, cluster ClusterInfo) *Status
}

type ScorePlugin interface {
    Plugin
    // Score returns a score in [MinClusterScore, MaxClusterScore] for the cluster.
    Score(ctx context.Context, state *CycleState, wl *kueue.Workload, cluster ClusterInfo) (int64, *Status)
    ScoreExtensions() ScoreExtensions
}

type ScoreExtensions interface {
    // NormalizeScore rescales scores in-place to [MinClusterScore, MaxClusterScore].
    NormalizeScore(ctx context.Context, state *CycleState, scores ClusterScoreList) *Status
}

type PreFilterPlugin interface {
    Plugin
    PreFilter(ctx context.Context, state *CycleState, wl *kueue.Workload) *Status
}

const (
    MinClusterScore int64 = 0
    MaxClusterScore int64 = 100
)

// ClusterInfo is a read-only snapshot of a MultiKueueCluster at dispatch time.
type ClusterInfo struct {
    Name   string
    Labels map[string]string
    Taints []ClusterTaint
    Active bool
}

type ClusterScore     struct { Cluster ClusterInfo; Score int64 }
type ClusterScoreList []ClusterScore

// CycleState is allocated per dispatch cycle and is not shared across workloads. Plugins
// store pre-computed data using typed keys to avoid redundant computation. For example,
// ClusterAffinity parses WorkloadSpec.ClusterSelector and WorkloadSpec.ClusterAffinity
// once in PreFilter and stores the result; the filter and score phases read from state
// rather than re-parsing on each cluster evaluation.
type CycleState struct {
    mu   sync.RWMutex
    data map[CycleStateKey]StateData
}

type CycleStateKey string
type StateData interface{ Clone() StateData }
```

### Built-in Plugins

#### Filter Plugins

**ClusterHealth**

Rejects clusters whose `MultiKueueCluster.Status` `Active` condition is not `True`. This
plugin is always included in any profile; it cannot be removed.

**ClusterAffinity**

Evaluates `WorkloadSpec.ClusterSelector` and
`WorkloadSpec.ClusterAffinity.RequiredDuringSchedulingIgnoredDuringExecution` against
cluster labels. If neither field is set on the workload, this plugin is a no-op.

**ClusterTaints**

Rejects clusters whose `NoSchedule` taints are not tolerated by
`WorkloadSpec.ClusterTolerations`.

#### Score Plugins

**ClusterAffinityScore**

Converts `WorkloadSpec.ClusterAffinity.Preferred` terms into a score. Each satisfied
preferred term contributes `term.Weight` to a raw sum; the raw sum is then normalized
to [0, 100] proportionally across the candidate set.

**ClusterTaintScore**

Reduces the score of clusters with `PreferNoSchedule` taints not tolerated by
`WorkloadSpec.ClusterTolerations`. `Score` returns the count of untolerated `PreferNoSchedule` taints, and
`NormalizeScore` inverts the result so the cluster with the fewest untolerated taints scores
`MaxClusterScore` and the cluster with the most scores 0. The penalty is relative across
the candidate set, not a fixed constant per taint.

**CapacityScore** *(blocked on [kubernetes-sigs/kueue#10105](https://github.com/kubernetes-sigs/kueue/issues/10105))*

Requires worker cluster resource stats (quota headroom) to be visible in the manager
cluster via `MultiKueueCluster.Status`. Interface name reserved at alpha; implementation
ships at beta once [kubernetes-sigs/kueue#10105](https://github.com/kubernetes-sigs/kueue/issues/10105)
lands.

**LocalityScore** *(TODO: MAYBE DEFER THIS?)*

Scores clusters by proximity to the workload's training data. The plugin reads the
`kueue.x-k8s.io/data-region` label from the workload (propagated from the owner job by
the standard Kueue label propagation mechanism) and compares it against the
`kueue.x-k8s.io/region` label on each candidate cluster. A cluster whose region matches
the workload's data region scores `MaxClusterScore`; all others score 0. If the workload
carries no `data-region` label, the plugin returns 0 for all clusters (no-op).

### Nomination: Top-1 with Retry

The composable dispatcher nominates exactly one cluster per dispatch cycle. If admission
does not occur within `workerLostTimeout`, the workload transitions to
`CheckStateRetry` and the dispatcher is called again. The previously nominated cluster is
passed as a hint but is not automatically excluded unless it is now filtered out by
`ClusterHealth` or another filter plugin.

Operators who need faster fallback should set a lower `workerLostTimeout` on the
`MultiKueueConfig`, or taint the degraded cluster to cause it to be filtered.


### Interaction with Existing Dispatchers

The composable dispatcher is selected the same way as the existing dispatchers: via the
global `MultiKueue.DispatcherName` field in the Kueue controller configuration. Three
values are supported:

| `DispatcherName` | Behaviour |
|---|---|
| `kueue.x-k8s.io/multikueue-dispatcher-all-at-once` (default) | Nominates all clusters simultaneously; first to admit wins. |
| `kueue.x-k8s.io/multikueue-dispatcher-incremental` | Nominates clusters in batches of three with a round timeout. |
| `kueue.x-k8s.io/multikueue-dispatcher-composable` | Runs the Filter → Score → Select pipeline; profile per ClusterQueue via `dispatcherProfileRef`. |

When `DispatcherName` is set to `kueue.x-k8s.io/multikueue-dispatcher-composable`, the
per-ClusterQueue `dispatcherProfileRef` field selects which `ClusterDispatcherProfile`
governs that queue's plugin pipeline. ClusterQueues without a `dispatcherProfileRef` use
the `default` profile.

### Validation

A validating webhook enforces:

1. `ClusterDispatcherProfile.spec.plugins.score` must have at least one entry with
   `weight > 0` (prevents all-zero weight degeneration).
2. `ClusterDispatcherProfile` referenced by a `ClusterQueue` must exist.
3. `ClusterTaint.Effect` must be `NoSchedule` or `PreferNoSchedule`.
4. `ClusterToleration.Operator` must be `Equal` or `Exists`; `Exists` with a non-empty
   `Value` is rejected.
5. `PreferredClusterSchedulingTerm.Weight` must be in [1, 100].
6. `WorkloadSpec.ClusterSelector` keys must be valid label keys.

A separate admission warning (non-blocking) is emitted when a `WorkloadSpec.ClusterSelector`
or required `ClusterAffinity` references label keys not present on any `MultiKueueCluster`
currently in the candidate pool.

### Test Plan

[X] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.

### Graduation Criteria

#### Alpha

- Feature gate `ComposableDispatcher` (default: disabled).
- `ClusterDispatcherProfile` CRD with filter and weighted score plugin configuration.
- `spec.taints` field on `MultiKueueCluster`.
- `spec.clusterSelector`, `spec.clusterAffinity`, `spec.clusterTolerations` fields on
  `WorkloadSpec`.
- Job framework annotation propagation for cluster placement fields.
- `spec.dispatcherProfileRef` field on `ClusterQueue`.
- Built-in plugins: `ClusterHealth`, `ClusterAffinity`, `ClusterTaints` (filter);
  `ClusterAffinityScore`, `ClusterTaintScore` (score).
- `ClusterFeasibility` filter interface defined and registered (no-op implementation);
  full implementation deferred to beta pending [kubernetes-sigs/kueue#10105](https://github.com/kubernetes-sigs/kueue/issues/10105).
- `kueue.x-k8s.io/dispatch-mode` workload annotation (`BestEffort` / `MustBeAdmitted`).
- `default` profile installed by Kueue.
- Top-1 nomination with retry.
- Validation webhook.
- Unit and integration tests passing.

#### Beta

- Feature gate enabled by default.
- `ClusterFeasibility` filter fully implemented (requires [kubernetes-sigs/kueue#10105](https://github.com/kubernetes-sigs/kueue/issues/10105)).
- `CapacityScore` plugin implemented (also requires [kubernetes-sigs/kueue#10105](https://github.com/kubernetes-sigs/kueue/issues/10105)).
- `LocalityScore` plugin implemented.
- Metrics (see [Observability](#observability)).
- E2E tests passing.

#### Stable

- Feature gate removed (behavior always on for ClusterQueues that opt in).
- Async out-of-tree plugin extension point (if follow-on KEP is approved).
- Stability: no breaking API changes since beta.
- Graduation of `ClusterDispatcherProfile` to `v1`.

### Observability

The following metrics are introduced at beta:

| Metric | Type | Description |
|---|---|---|
| `kueue_dispatcher_filter_duration_seconds` | Histogram | Latency of each filter plugin, labeled by `plugin` |
| `kueue_dispatcher_score_duration_seconds` | Histogram | Latency of each score plugin, labeled by `plugin` |
| `kueue_dispatcher_cycle_duration_seconds` | Histogram | Full dispatch cycle latency, labeled by `result` (nominated, no_feasible_cluster, error) |
| `kueue_dispatcher_filtered_clusters_total` | Counter | Clusters eliminated, labeled by `plugin` and `reason` |
| `kueue_dispatcher_nominated_cluster` | Gauge | Most recently nominated cluster per workload |
| `kueue_dispatcher_no_feasible_cluster_total` | Counter | Dispatch cycles that found no feasible cluster |

Events emitted on the `Workload` object:

- `NoFeasibleCluster`: all candidates filtered; includes the filter plugin and reason for
  each eliminated cluster.
- `ClusterNominated`: top-1 cluster selected; includes the cluster name and final scores.
- `DispatchRetry`: nomination timed out; workload re-entering dispatch cycle.

## Drawbacks

**Added API surface on WorkloadSpec.** Three new fields on `WorkloadSpec` increase the
Workload API that users interact with. The fields are optional and no-op when empty, so
existing workloads are unaffected.

**Top-1 retry increases worst-case admission latency.** If the top-scoring cluster fails to
admit (e.g., due to capacity that the dispatcher cannot observe), each retry adds up to
`workerLostTimeout` (15 min by default). This is a regression compared to AllAtOnce, where
the next cluster admits in parallel. Mitigation: cluster health filtering removes truly
inactive clusters; `CapacityScore` will reduce this significantly once
[kubernetes-sigs/kueue#10105](https://github.com/kubernetes-sigs/kueue/issues/10105) lands.

**In-tree plugins require Kueue code changes for new scoring logic.** Operators cannot add
custom cluster scoring without building a fork or waiting for upstream acceptance. The
out-of-tree extension path is deferred, not excluded.

## Future Work

- **Top-K nomination.** The current dispatcher nominates exactly one cluster per cycle
  (top-1). A future KEP can add parallel nomination of K clusters simultaneously as an
  opt-in `nominationMode: TopK` field on `ClusterDispatcherProfile`, reducing tail latency
  when the top-scoring cluster is slow to admit. Top-1 remains the default.

- **Out-of-tree plugins.** In-tree plugins require Kueue code changes for new scoring logic.
  A follow-on KEP can introduce an asynchronous annotation-based extension model: plugins
  write scores to `MultiKueueCluster.Status`; the dispatcher reads them at nomination time
  without blocking. This preserves Kueue's async-extension philosophy established by
  `AdmissionCheck`.

- **Per-ClusterQueue weight presets.** Once audit logging of scoring decisions is available,
  operator-approved weight presets could be surfaced as a named subset of profiles that
  tenants may choose from, without allowing arbitrary weight overrides.