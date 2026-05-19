# Kueue priority booster

The `kueue-priority-booster` is an experimental controller that implements a
time-sharing fairness mechanism for Kueue Workloads by decreasing their
effective priority after a configurable amount of time using the
`kueue.x-k8s.io/priority-boost` annotation. The decrease can lead to preemption
issued by pending Workloads with the same priority. See KEP-7990 for the design
rationale.

Concretely: during an initial admit window the controller leaves
`kueue.x-k8s.io/priority-boost` **unset**, then applies a **negative** boost
after that window while the workload remains admitted, so same–base-priority
waiters can preempt under **`withinClusterQueue: LowerPriority`**.

## Purpose

In a `ClusterQueue` with a fixed quota, multiple workloads of the same priority
and unknown duration can starve peers. With **LowerPriority**, workloads with the
same effective priority do not preempt each other. During `timeSharingWindow` the
controller leaves the annotation unset, so the admitted workload keeps its base
effective priority. After the window, the controller sets a negative annotation
so same–base-priority pending workloads can preempt, producing round-robin style
fairness at admission time.

**Why not `LowerOrNewerEqualPriority` or `Any`?** Those policies allow preemption
against equal or higher effective priority in ways that do not match this
“defer signal until after a minimum admit time” mechanism. This controller is
documented for **`withinClusterQueue: LowerPriority`**.

This component demonstrates an external policy controller for KEP-7990 (priority
boost signal). It is not part of the main Kueue binary and is deployed separately.

Treat it as a **plugin-style integration**: only one controller should reconcile
`kueue.x-k8s.io/priority-boost` for a given cluster; additional controllers would
fight over the same annotation.

**Requires**: the `PriorityBoost` feature gate enabled in Kueue, and the
`ClusterQueue` must use `withinClusterQueue: LowerPriority`.

## How It Works

```
Workload admitted
      │
      ▼
no annotation               ← same effective priority as peers; no same-priority
  (timeSharingWindow)            preemption under LowerPriority
      │
  [window elapses, still admitted]
      │
      ▼
annotation = -negativeBoostValue    ← post-window preemption signal (magnitude = negativeBoostValue)
      │
  [workload no longer admitted]
      │
      ▼
annotation cleared
```

`EffectivePriority = basePriority + boost`. With no annotation, boost is 0.
After the window, boost is `-negativeBoostValue`, lowering effective priority so peers
can preempt.

## Build

This directory is part of the root `sigs.k8s.io/kueue` Go module (same layout as `kueue-populator`); there is no separate `go.mod` here.

```bash
make build
```

```bash
make test
```

Unit tests only (`./pkg/...`). Integration tests use envtest and Ginkgo:

```bash
make test-integration
```

Requires compiled CRDs from repo root first: `make compile-crd-manifests` (the `test-integration` target runs this automatically).

```bash
make image-build
```

## Deploy

```bash
kubectl apply -k config
```

## Configuration

The controller reads `--config` YAML:

| Field | Type | Default (in code) | Description |
|-------|------|-------------------|-------------|
| `timeSharing.duration` | duration | `"0"` | How long after admission to leave **no** annotation. After that, while still admitted, set **negative** boost. `"0"` disables the controller behavior. |
| `timeSharing.negativeBoost` | int32 | `100000` | Magnitude of the **negative** boost after the window (stored as `-timeSharing.negativeBoost` in the annotation). |
| `workloadSelector` | `LabelSelector` | omit | If set, only workloads whose labels match are managed; others have any boost annotation **removed**. |
| `maxWorkloadPriority` | int32 | omit | If set, workloads with `spec.priority` **greater** than this are out of scope (unset `spec.priority` is treated as `0`). |

Example manifests: `config/manager/controller_config_map.yaml`.

### Example

```yaml
timeSharing:
  duration: "30m"
  negativeBoost: 100000
# workloadSelector:
#   matchLabels:
#     tier: batch
# maxWorkloadPriority: 1000
```

## Helm

Chart path: `charts/kueue-priority-booster`
