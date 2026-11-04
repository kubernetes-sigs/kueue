# pr-evidence capture schema (`pr-evidence.capture/v1`)

Producer: `kueue-pr-evidence-state-diff` (#18142). Consumers:
`kueue-pr-evidence-state-diff` (base vs head diff) and
`kueue-pr-evidence-timeline` (#18145). Example:
[examples/capture.example.json](examples/capture.example.json).

Write one JSON document per side of one scenario: `capture.base.json`, run at
`MERGE_BASE` in a [kueue-worktree.sh](scripts/kueue-worktree.sh) tree, and
`capture.head.json`, run at `HEAD_SHA`. Both must have the same `run.scenario`
and the same ordered checkpoint `name`s. Diff by checkpoint `name`, never by
position alone. Keep captures outside the repository, under
`${XDG_CACHE_HOME:-~/.cache}/kueue-pr-evidence/`.

## Document

| Field | Type | Notes |
|-------|------|-------|
| `schemaVersion` | string | Always `pr-evidence.capture/v1`. Bump on breaking change. |
| `run` | object | Run metadata (below). |
| `checkpoints` | array | Ordered by `index` (0..n-1, contiguous). |
| `failure` | object \| null | Optional. `null` when every assertion passed; otherwise `step`, `action`, `assertion`, `message`, `checkpoint` (the capture it gated) and `observed` (a checkpoint taken at the failure). |

### `run`

| Field | Type | Notes |
|-------|------|-------|
| `producer` | string | Skill that wrote the file. |
| `scenario` | string | Stable kebab-case scenario id. |
| `side` | `base` \| `head` | Which tree ran. |
| `sha` | string | Full SHA that ran (`== baseSha` or `== headSha`). |
| `baseSha`, `headSha` | string | Full SHAs from `merge-base.sh`. |
| `runner` | `envtest` \| `kind` | `kind` only for real Pods (PodsReady), DRA, TAS node-fit, MultiKueue. |
| `k8sVersion` | string | envtest assets or kind node version, e.g. `1.34.1`. |
| `featureGates` | object | `{GateName: bool}` explicitly set for the run (not defaults). |
| `test` | string | Optional. Test file + spec name the scenario is derived from. |

### `checkpoints[]`

A checkpoint is a snapshot taken **right after an assertion passed** (a
`gomega.Eventually`/`Expect` in the scenario). Without an assertion there is no checkpoint. A
failed assertion ends the capture and the producer records it in its block.

| Field | Type | Notes |
|-------|------|-------|
| `index` | int | 0-based, contiguous. |
| `name` | string | Stable kebab-case, unique per document. |
| `assertion` | string | Human text of the assertion that gated the snapshot. |
| `workloads` | array | Workloads in scenario namespaces, sorted by `namespace`, `name`. |
| `clusterQueues` | array | Sorted by `name`. |
| `localQueues` | array | Sorted by `namespace`, `name`. |
| `events` | array | Events **since the previous checkpoint**, sorted by `order`. |

### Workload

```json
{"namespace": "default", "name": "low", "queueName": "lq-a", "priority": 100,
 "conditions": [{"type": "Evicted", "status": "True", "reason": "Preempted", "message": "..."}],
 "admission": {"clusterQueue": "cq-a", "podSetAssignments": [{
   "name": "main", "count": 2, "flavors": {"cpu": "default"}, "resourceUsage": {"cpu": "2"},
   "topologyAssignment": {"levels": ["kubernetes.io/hostname"],
                          "domains": [{"values": ["node-1"], "count": 2}]}}]},
 "requeueState": {"count": 1}}
```

- `conditions[]`: only `type`, `status`, `reason`, `message`; sorted by `type`.
- `admission`: `null` when not admitted. `topologyAssignment` is omitted when
  absent; otherwise **expanded** from the v1beta2 compressed `slices` form to
  `levels` + `domains[{values, count}]` (use `pkg/util/tas.InternalFrom`), with
  domains sorted by `values`.
- `requeueState`: only `count` (drop `requeueAt`); omit when absent.
- Quantities are canonical `resource.Quantity.String()` values.

### ClusterQueue / LocalQueue

- ClusterQueue: `name`, `pendingWorkloads`, `reservingWorkloads`,
  `admittedWorkloads`, `flavorsUsage[{name, resources[{name, total, borrowed}]}]`
  (sorted by flavor, then resource name).
- LocalQueue: `namespace`, `name`, `clusterQueue`, `pendingWorkloads`,
  `reservingWorkloads`, `admittedWorkloads`.

### Event

`order` (int, monotonic across the whole document), `type`, `reason`,
`involvedObject{kind, namespace, name}`, `message`. Deduplicate repeated events
(`count > 1`) into one entry at their first `order`.

## Normalization (both sides identically)

- **Drop**: `uid`, `resourceVersion`, `generation`, `managedFields`,
  `ownerReferences`, `creationTimestamp`, every `lastTransitionTime` /
  `lastHeartbeatTime` / `requeueAt` / event timestamp, `observedGeneration`.
- **Order** replaces time: event `order` is the only ordering signal.
- **Keep verbatim**: condition `type`/`reason`, event `reason`, and Kueue
  reason constants. Never rename, translate, or summarize them.
- **Mask** volatile substrings inside messages only: UIDs become `<uid>`,
  durations (`12ms`, `1.5s`, `2m3s`) become `<duration>`, generated name suffixes
  (`-abc12`) become `-<hash>`.
- **Exclude** objects outside the scenario namespaces and Kueue system objects.
- Serialize with sorted object keys and 2-space indent so text diffs are stable.
