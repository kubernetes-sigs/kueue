# Scenario format

## Purpose

A scenario is one declarative YAML file. `state-diff run` executes the same file
unchanged on the merge base and on the PR head, then diffs the normalized state at
each checkpoint. Each side runs in a throwaway kueue worktree with an untracked,
generic Go driver overlaid into the scenario's integration suite. Nothing is
committed upstream.

Built-in scenarios live in `scenarios/`; `--scenario` also takes a file path.
`state-diff compile --scenario <file>` validates a file and prints the compiled
driver input and the runner it would pick.

## Top-level keys

| Key | Required | Meaning |
|-----|----------|---------|
| `name` | yes | Kebab-case id; names `<out>/state-diff/<name>/`. Shipped scenarios must equal the file stem (unit-tested, not checked by the CLI). |
| `description` | recommended | One line; shown by `state-diff list`. Required for shipped scenarios (unit-tested). |
| `test` | no | Upstream test the scenario derives from, `file: spec name`. Copied to `run.test` in both captures. |
| `featureGates` | no | `{GateName: true\|false}`, set on both sides. A gate the base tree does not define is dropped for base, listed in `run.json` as `gatesUnknownAtBase`, and noted in the block. A gate unknown at head makes the run broken (exit 3). |
| `namespaces` | yes | List of one or more kebab-case names, created by the driver before the first step. Only Workloads and LocalQueues in these namespaces are captured. With exactly one namespace it is the default for every namespaced ref and manifest; with more, each must name its namespace. |
| `timeout` | no | Default timeout for each `waitFor`. Default `10s`. |
| `settleTimeout` | no | Max time a `capture` waits for state to stop changing. Default `10s`. |
| `settleWindow` | no | How long the state must stay unchanged before a `capture`. Default `2s`, longer than Kueue's 1s ClusterQueue status batch period (`constants.UpdatesBatchPeriod`); shorter windows capture stale ClusterQueue counts. |
| `requires` | no | Subset of `pods-ready`, `dra`, `tas-node-fit`, `multikueue`. Any entry selects the kind runner. |
| `runner` | no | `auto` (default), `envtest`, or `kind`. A pin wins over `requires`. `--runner` on the CLI wins over both. |
| `suite` | no | Integration suite directory under `test/integration/singlecluster/` (or `test/integration/`), e.g. `tas` or `scheduler/fairsharing`. Default `scheduler`. |
| `managerSetup` | no | Who starts the Kueue manager. By default the CLI reads the suite's `suite_test.go`: if `BeforeSuite` calls `fwk.StartManager`, the driver reuses that manager; otherwise the driver starts one in `BeforeAll` (after setting the gates) with `managerAndSchedulerSetup`/`managerSetup` when it takes no required arguments, and stops it in `AfterAll`. Set a Go expression (e.g. `managerAndSchedulerSetup(nil)`) when the setup function needs arguments, or `suite` to force reuse. A reused manager was set up before the scenario's `featureGates`, so gates read at controller setup keep their suite value; the CLI warns. Set `managerSetup` when the PR depends on such a gate. |
| `steps` | yes | Non-empty list of steps. |

Durations take an `ms`, `s`, or `m` suffix (`500ms`, `10s`, `2m`). A bare number
means seconds.

## Steps

Each list item is a mapping with exactly one of the keys below. A ref is `kind`,
`name`, optional `namespace`, and optional `apiVersion` (see API versions).

- **apply**: a manifest mapping, a list of manifests, or (in a file loaded by the
  CLI) a block string of `---`-separated documents; empty documents are dropped.
  Each manifest needs `apiVersion`, `kind`, and `metadata.name` (no
  `generateName`). Namespaced kinds without `metadata.namespace` get the single
  default namespace. The driver creates each object; if the manifest has a
  `status`, it is removed for the create and written afterwards with a status
  update. A `v1` Node whose applied status is `Ready=True` also loses the
  `node.kubernetes.io/not-ready` taint the API server adds at creation (envtest
  has no node lifecycle controller), as upstream `CreateNodesWithStatus` does.
- **waitFor**: a ref plus either `condition` (with `status`, default `True`, one of
  `True|False|Unknown`, and an optional `reason`) or `field` with `equals`. `field`
  is a dot-separated path into the object, compared as text with `equals`. Use
  one form per step. Optional `timeout` overrides the scenario default; optional
  `text` replaces the generated description used in the capture's `assertion`
  and in failure messages. The generated text looks like
  `Workload pr-evidence/a Evicted=True (Preempted)`.
- **capture**: a kebab-case name (a string, or `{name: ...}`), unique within the
  scenario. It must follow at least one `waitFor` or `finishEviction` since the
  previous capture; `apply`, `patch`, `finish`, and `delete` do not count. The
  checkpoint is taken only after those assertions pass and the state is stable
  (see below).
- **patch**: a ref, a `merge` mapping (JSON merge patch), and `subresource`
  (`""`, the default, or `status`). Not an assertion.
- **finishEviction**: a Workload ref (`kind` defaults to Workload; other kinds are
  rejected). Envtest has no job controller, so this calls the tree's
  `FinishEvictionForWorkloads` helper to simulate the job reacting to eviction.
  It counts as an assertion for the next capture, with text `Workload ns/name is
  evicted`. The driver does not check that the Workload is evicted, so put a
  `waitFor` on `Evicted` first.
- **finish**: a Workload ref; calls the tree's `FinishWorkloads` helper, which
  marks it Finished. Not an assertion.
- **delete**: a ref; deletes it and ignores NotFound. It does not wait for the
  object to disappear.

## API versions

- Kueue kinds in refs (Workload, ClusterQueue, LocalQueue, ResourceFlavor, Cohort,
  WorkloadPriorityClass, AdmissionCheck, Topology, ProvisioningRequestConfig)
  default to `kueue.x-k8s.io/<api>`, where `<api>` is `v1beta2` if the tree has
  `apis/kueue/v1beta2`, else `v1beta1`. Applies to `waitFor`, `patch`,
  `finishEviction`, `finish`, and `delete`.
- Core kinds in refs (Node, Namespace, Pod, ConfigMap, Secret, LimitRange,
  ResourceQuota) default to `v1`. Other kinds, and every `apply` manifest, need an
  explicit `apiVersion`. Standalone `compile` assumes `v1beta2` unless `--api`.

## Waits, settling, and failures

- No sleeps. Every wait is an assertion (`Eventually`, polled every 200 ms). A
  capture is gated on its preceding assertions and then waits until the state
  fingerprint is identical for three consecutive polls and for at least
  `settleWindow` (default 2s). If that does not happen
  within `settleTimeout`, the capture step fails.
- A capture contains workloads, ClusterQueues, LocalQueues, and the events since
  the previous checkpoint, all restricted to the scenario's namespaces and the
  applied ClusterQueues.
- The first failed step ends the run for that side. The driver records the step
  index, action, assertion text, masked message, and an observed snapshot at the
  failure point. That snapshot is diffed as the next checkpoint, marked failed.
  Later checkpoints that side never reached show a single `✗ not reached` row
  (no field rows: there is nothing observed to compare).
- A **base** failure is evidence (the bug reproduced). The block says `Base fails
  ...`, and the exit code is 0. A **head** failure is exit 3: evidence is
  incomplete and the block must not be published. If both sides fail, the run is
  broken (exit 3). Exit 2 means a usage or scenario error, including the kind
  runner (see [SKILL.md](SKILL.md)).

## Outputs

`<out>` defaults to `${XDG_CACHE_HOME:-~/.cache}/kueue-pr-evidence/<head sha12>`
(`--out` overrides it). The paths are stable; `kueue-pr-evidence-timeline` reads
the captures.

- `<out>/state-diff/<scenario>/capture.base.json`, `capture.head.json`:
  `pr-evidence.capture/v1` documents, one per side, in the format of
  [capture-schema.md](../kueue-pr-evidence-common/capture-schema.md).
- `<out>/state-diff/<scenario>/run.json`: block metadata (runner, SHAs, gates,
  k8s version, failures, checkpoint count, scenario YAML). `render` reads it.
- `<out>/state-diff/<scenario>/diff.json`: per-checkpoint rows.
- `<out>/state-diff/<scenario>/raw.base.json`, `raw.head.json`: driver output
  before normalization.
- `<out>/state-diff/<scenario>/scenario.base.json`, `scenario.head.json`: the
  compiled driver input per side, with gates applied.
- `<out>/state-diff/<scenario>/base.log`, `head.log`: `make` and `go test` output.
- `<out>/kueue-pr-evidence-state-diff.md`: block content for `inject-pr-notes`.
  `run` deletes it first, and a head failure writes
  `<out>/state-diff/<scenario>/block.head-failed.md` instead.

`state-diff render --dir <out>/state-diff/<scenario>` rebuilds the block from these
files without rerunning either side.

## Minimal example

```yaml
# One flavor, one ClusterQueue, one Workload that fits the quota and is admitted.
name: minimal-admit
description: >-
  A Workload that fits the nominal quota is admitted.
namespaces: [pr-evidence]
steps:
  - apply: |
      apiVersion: kueue.x-k8s.io/v1beta2
      kind: ResourceFlavor
      metadata: {name: alpha}
      ---
      apiVersion: kueue.x-k8s.io/v1beta2
      kind: ClusterQueue
      metadata: {name: cq}
      spec:
        namespaceSelector: {}
        resourceGroups:
        - coveredResources: [cpu]
          flavors:
          - name: alpha
            resources:
            - {name: cpu, nominalQuota: "2"}
      ---
      apiVersion: kueue.x-k8s.io/v1beta2
      kind: LocalQueue
      metadata: {name: q}
      spec: {clusterQueue: cq}
  - waitFor: {kind: ClusterQueue, name: cq, condition: Active}
  - apply:
      apiVersion: kueue.x-k8s.io/v1beta2
      kind: Workload
      metadata: {name: job}
      spec:
        queueName: q
        priority: 0
        podSets:
        - name: main
          count: 1
          template:
            spec:
              restartPolicy: Never
              containers:
              - name: c
                image: registry.k8s.io/pause:3.10
                resources: {requests: {cpu: "1"}}
  - waitFor: {kind: Workload, name: job, condition: Admitted}
  - capture: job-admitted
```
