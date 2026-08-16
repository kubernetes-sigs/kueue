# DRAFT / POC: MultiKueue user-specified nominated clusters

Status: draft proposal (POC branch `poc-mk-user-nominated-clusters`). Not a
numbered KEP yet; this note exists for design review before implementation.

## Summary

Let a user restrict, directly on the CR, which MultiKueue worker clusters a
Workload may be dispatched to — without running an external dispatcher. The user
provides a list of worker cluster names; MultiKueue nominates only those, always
intersected with the clusters the Workload's ClusterQueue is already authorized
for.

## Motivation

Today, per-Workload cluster placement in MultiKueue is either:

- implicit (the built-in `AllAtOnce` / `Incremental` dispatchers nominate from all
  authorized clusters), or
- delegated to an **external dispatcher** (custom controller that sets
  `.status.nominatedClusterNames`).

The external dispatcher is the only way to pin a Workload to a specific cluster,
which is heavyweight for the common "I already know the target cluster" case.

### Motivating use case: co-located async checkpoint eval

A training job runs as a `RayCluster` on some worker cluster. Every N steps the
user's orchestrator launches a separate `RayJob` to run an async checkpoint eval,
which must run **on the same worker cluster** as the training job (checkpoint data
locality).

The orchestrator already knows the training cluster at launch time (it reads the
training Workload's `.status.clusterName`, or it placed training on a known
cluster). So it does not need Kueue to resolve cross-Workload affinity; it only
needs a simple, declarative way to tell Kueue "put this eval on cluster X".

Cross-Workload affinity ("co-locate with Workload Y", resolved by Kueue) is a
larger, separate feature and is out of scope here. This proposal is the smaller
building block that already satisfies the use case when the caller resolves the
target cluster itself.

## Goals

- Allow a Workload to declare a set of preferred/required worker clusters via the
  CR, honored by the built-in dispatcher.
- Never let this widen the authorized cluster set — it can only narrow it.
- Keep it opt-in and small (annotation-based MVP, no CRD/API change).

## Non-goals

- Cross-Workload cluster affinity ("same cluster as Workload Y") resolved by
  Kueue. (Possible future work; the internal `getComponentWorkloadsClusterName`
  primitive shows it is feasible.)
- Preferred / soft ordering with fallback (future; would build on `Incremental`).
- A typed `Workload.Spec` field (future graduation from the annotation).

## API (MVP)

Annotation on the user's Job (propagated to the Workload):

```yaml
metadata:
  annotations:
    kueue.x-k8s.io/multikueue-cluster-names: "worker-a,worker-b"
```

- Value: comma-separated `MultiKueueCluster` names.
- Semantics: **required** — nominate only these clusters (∩ authorized). If none
  can admit, the Workload waits (Pending).
- Absent annotation: unchanged behavior (all authorized clusters).

Why an annotation and not a typed field for the MVP: no CRD/API version bump,
works for every integration, fastest to prototype. Graduation path is a typed
`Workload.Spec` field.

## Behavior

### Propagation Job → Workload

The dispatcher runs against the manager `Workload` (`group.local`). The annotation
must reach it. Preferred: add the annotation key to the Workload-construction
annotation copy set (`NewWorkload` already copies an allowlist of annotations).
Alternative: read the owner Job's annotation directly (one extra Get).

### Dispatcher change

Single insertion point: `nominateAndSynchronizeWorkers`
(`pkg/controller/admissionchecks/multikueue/workload.go`). The authorized
candidate set is `keys(group.remoteClients)` (built in `readGroup` from the
Workload's AdmissionCheck → `MultiKueueConfig.spec.clusters`).

```
authorized = keys(group.remoteClients)
if workload has the annotation:
    candidates = parse(annotation) ∩ authorized     // narrow only — never widen
else:
    candidates = authorized                         // current behavior
→ apply the dispatcher mode within `candidates`
```

### Precedence

Within `nominateAndSynchronizeWorkers`, in order:

1. Component-workload assigned cluster (LWS) — invariant, wins.
2. Elastic prior-slice cluster — wins.
3. **User annotation** — constrain candidates to `specified ∩ authorized`.
4. Dispatcher mode (`AllAtOnce` / `Incremental` / `External`) runs within the
   constrained candidate set.

### Empty intersection

If `specified ∩ authorized` is empty, the Workload cannot be placed. Surface a
clear condition/event on the MultiKueue AdmissionCheck (e.g. "requested clusters
are not available or authorized") and leave the Workload Pending. This is a
runtime condition rather than a hard admission-webhook rejection because cluster
availability is dynamic and config-dependent. A validating webhook may still check
the annotation's **format**.

## Security

The intersection with the authorized set is the core safety property: a tenant can
only choose among the clusters its ClusterQueue's `MultiKueueConfig` already
allows. It can restrict placement, never escape the authorized set. Values outside
the authorized set are dropped (and, if that empties the set, handled as above).

## Feature gate

`MultiKueueClusterNames` (alpha, default off).

## Observability

- Event when the annotation constrains nomination.
- Condition on the MultiKueue AdmissionCheck when the intersection is empty,
  naming the requested vs authorized clusters.

## Open questions (for review)

1. Annotation (MVP) vs a typed `Workload.Spec` field for GA.
2. `required`-only for the MVP (sufficient for the motivating use case) vs adding
   a `preferred` mode now.
3. Empty intersection: Pending + condition (proposed) vs webhook rejection at
   admission when there is no overlap.
4. Annotation name: `kueue.x-k8s.io/multikueue-cluster-names` vs
   `.../multikueue-nominated-clusters` vs `.../multikueue-placement-clusters`.

## Alternatives considered

- **External dispatcher** (exists): fully general but requires running a custom
  controller and owning nomination for all Workloads. Overkill for the
  "caller knows the cluster" case.
- **Node-label + nodeSelector / ResourceFlavor targeting** (exists, no code
  change): label each worker cluster's nodes with a distinct key, define a
  `ResourceFlavor` per worker whose `nodeLabels` match that key (on both the
  manager and the workers), and set the Job's `nodeSelector` to the target
  cluster's label. Kueue's flavor assignment then admits the Workload only on the
  cluster whose ClusterQueue has a matching flavor — each worker assigns flavors
  independently, so a non-matching worker cannot admit — effectively routing it
  there.

  This works today and is more idiomatic than per-cluster queues, but it is
  fragile and indirect:
  - It requires per-cluster, mutually-exclusive restrictive flavors on **every**
    cluster (manager included). Any permissive/catch-all flavor (empty
    `nodeLabels`) lets a non-target cluster admit the Workload and then leaves its
    pods unschedulable (no matching node).
  - It encodes cluster identity indirectly via node labels rather than selecting a
    `MultiKueueCluster` directly.

  These are the main reasons to prefer an explicit cluster-selection API: it
  targets a `MultiKueueCluster` directly and does not depend on every cluster's
  flavor configuration being exactly right.
- **Cross-Workload affinity** (future): "same cluster as Workload Y" resolved by
  Kueue; larger feature, out of scope here.
