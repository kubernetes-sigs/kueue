# KEP-13746: Topology Spreading

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1: Serving a small AI inference model with multiple replicas](#story-1-serving-a-small-ai-inference-model-with-multiple-replicas)
    - [Story 2: Serving a large AI inference model with multiple replicas](#story-2-serving-a-large-ai-inference-model-with-multiple-replicas)
    - [Story 3: Serving a large AI inference model with multiple replicas using PodGroups](#story-3-serving-a-large-ai-inference-model-with-multiple-replicas-using-podgroups)
    - [Story 4: Soft spreading with limited capacity](#story-4-soft-spreading-with-limited-capacity)
  - [Risks and Mitigations](#risks-and-mitigations)
    - [User confusion when choosing maxShareAllowingPlacement](#user-confusion-when-choosing-maxshareallowingplacement)
    - [Performance impact of counting PodSet groups per domain](#performance-impact-of-counting-podset-groups-per-domain)
- [Design Details](#design-details)
  - [API](#api)
    - [Annotation format](#annotation-format)
    - [Field definitions](#field-definitions)
    - [Validation and error handling](#validation-and-error-handling)
  - [Scheduler integration](#scheduler-integration)
    - [Computing per-domain PodSet group counts](#computing-per-domain-podset-group-counts)
    - [Banned and over-threshold domains](#banned-and-over-threshold-domains)
    - [Scoring](#scoring)
  - [Visibility](#visibility)
  - [Feature gate](#feature-gate)
- [Test Plan](#test-plan)
  - [Prerequisite testing updates](#prerequisite-testing-updates)
  - [Unit tests](#unit-tests)
  - [Integration tests](#integration-tests)
  - [e2e tests](#e2e-tests)
  - [Graduation Criteria](#graduation-criteria)
    - [Alpha](#alpha)
    - [Beta](#beta)
    - [Stable](#stable)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
  - [Other API design alternatives](#other-api-design-alternatives)
    - [Use maxSkew](#use-maxskew)
    - [Use minDomains](#use-mindomains)
  - [Dedicated TopologySpreadingPolicy CRD](#dedicated-topologyspreadingpolicy-crd)
  - [Enforce spreading via kube-scheduler Pod Topology Spread Constraints](#enforce-spreading-via-kube-scheduler-pod-topology-spread-constraints)
<!-- /toc -->

## Summary

Topology Spreading is a Kueue feature that distributes admitted workloads across
topology domains (e.g., availability zones, racks, superblocks) to improve service
availability. It complements Topology Aware Scheduling (KEP-2724), which optimizes
for dense co-location of pods within a workload for low-latency communication.
While dense co-location is desirable for batch ML training, user-facing inference
services benefit from spreading replicas across failure domains so that a localized
infrastructure outage does not take down the entire service.

The feature is configured via a single annotation placed on a workload's **PodSet
template**. The annotation carries the spreading configuration inline as a JSON
value. The annotation is respected at admission time by Kueue's TAS to place the
considered workload's PodSet with respect to the specified configuration.

## Motivation

Kueue's Topology Aware Scheduling focuses on maximizing workload density to
improve communication throughput and minimize latency. This is the right default
for batch ML training. However, Kueue is increasingly used for mixed
training/inference scenarios where the same cluster hosts long-running inference
services alongside batch jobs. For inference services, availability is a primary
concern: if all replicas are placed in a single zone or rack, a single failure can
take down the entire service.

Core Kubernetes provides
[Pod Topology Spread Constraints](https://kubernetes.io/docs/concepts/scheduling-eviction/topology-spread-constraints/)
for intra-workload pod distribution, but this mechanism operates at the level of
individual pods and cannot express cross-workload spreading (i.e., distributing
separate Kueue workload objects across domains). Kueue must be aware of the
full placement picture — including which topology domains are already heavily used
by other workloads — to make good admission decisions.

For modern days AI inference a large distributed model is served by a group of Pods 
communicating with each other. So, the Pods serving the model need to be co-located, 
making the AI serving naturally 2-layered: spreading of PodGroups, and bin packing 
within PodGroups. Such constraint cannot be expressed by topologySpreading which 
assumes individual pods are spread.

### Goals

- Introduce an annotation which allows to express topology spreading constraints to 
  support integrations focusing on serving, in particular: LWS, Deployment and
  PodGroups.

### Non-Goals

- Spreading across non-topology ResourceFlavors.
- Cross-namespace spreading.
- More than two topology levels in the alpha milestone.
- Continuous rebalancing or eviction of already-admitted workloads when a                                                                                 
  spreading constraint is later violated; spreading is enforced only at                                                                                   
  admission time.

## Proposal

The feature is configured entirely via a single annotation on a workload's PodSet
template. The annotation value is a JSON object that specifies topology spreading
configuration. No separate CRD is required.

At admission time, the Kueue scheduler reads the annotation from the candidate
workload, finds all admitted workloads in the namespace whose labels match the
selector, counts how many PodSet groups are in each topology domain, and then
decides whether the candidate domain satisfies the rules.

### User Stories

#### Story 1: Serving a small AI inference model with multiple replicas

A platform team runs an inference service using a small model, fitting a single Pod,
running through a Deployment. In this setup each Pod is represented by a workload.

The team wants to ensure high availability when some of the Nodes go down, so they
want at most 45% of the inference pods in any single availability zone at the time
the next pod is placed there.

Because each Deployment pod has its own `kueue.x-k8s.io/job-uid` label (the pod's
own UID), the default `workloadLabelSelector` would only match the workload being
placed and not group all Deployment pods together. The team must specify an explicit
`workloadLabelSelector` using a shared label (e.g., `app`) and ensure that label is
propagated to the Workload via `integrations.labelKeysToCopy`:

```yaml
integrations:
  labelKeysToCopy:
    - app
```

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: main-inference-service
spec:
  template:
    metadata:
      labels:
        app: main-inference-service
      annotations:
        kueue.x-k8s.io/podset-topology-spreading: |
          {
            "workloadLabelSelector": "app=main-inference-service",
            "rules": [
              {"key": "topology.kubernetes.io/zone", "maxShareAllowingPlacement": 45}
            ]
          }
```

#### Story 2: Serving a large AI inference model with multiple replicas

A platform team runs an inference service based on a large model. The model requires
multiple closely connected Pods, so the team is using an LWS group to represent a single
replica.

The team wants to ensure high availability when some of the racks go down, so they want
a rack to hold at most 45% of the LWS groups before the next group is placed there.

```yaml
apiVersion: leaderworkerset.x-k8s.io/v1
kind: LeaderWorkerSet
metadata:
  name: large-inference-service
spec:
  replicas: 10
  leaderWorkerTemplate:
    size: 4
    leaderTemplate:
      metadata:
        annotations:
          kueue.x-k8s.io/podset-group-name: large-inference-service
          kueue.x-k8s.io/podset-required-topology: cloud.provider.com/rack
          kueue.x-k8s.io/podset-topology-spreading: |
            {
              "rules": [
                {"key": "cloud.provider.com/rack", "maxShareAllowingPlacement": 45}
              ]
            }
    workerTemplate:
      metadata:
        annotations:
          kueue.x-k8s.io/podset-group-name: large-inference-service
          kueue.x-k8s.io/podset-required-topology: cloud.provider.com/rack
          kueue.x-k8s.io/podset-topology-spreading: |
            {
              "rules": [
                {"key": "cloud.provider.com/rack", "maxShareAllowingPlacement": 45}
              ]
            }
```

Because all 10 groups are created from a single LWS object, all their Workloads share
the same `kueue.x-k8s.io/job-uid` label value (the LWS object's UID). Kueue defaults
`workloadLabelSelector` to this label, so no explicit selector is required.

#### Story 3: Serving a large AI inference model with multiple replicas using PodGroups

A platform team runs an inference service based on a large model that requires multiple
closely connected Pods. The team has an in-house Pod management system that integrates
with Kueue via PodGroups. One PodGroup represents a single model replica.

The team wants to ensure high availability when some of the racks go down, so they want
a rack to hold at most 45% of the PodGroups before the next one is placed there.

Unlike LWS, PodGroups have no single parent object: each PodGroup is an independent set
of Pods with no shared owner. Kueue therefore cannot default `workloadLabelSelector` to
a `kueue.x-k8s.io/job-uid` value — there is no job UID shared across all PodGroup
replicas of the service. The selector must be specified explicitly:

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: large-inference-service-0
  labels:
    kueue.x-k8s.io/pod-group-name: large-inference-service-0
    app: large-inference-service
  annotations:
    kueue.x-k8s.io/pod-group-total-count: "4"
    kueue.x-k8s.io/podset-group-name: large-inference-service-0
    kueue.x-k8s.io/podset-required-topology: cloud.provider.com/rack
    kueue.x-k8s.io/podset-topology-spreading: |
      {
        "workloadLabelSelector": "app=large-inference-service",
        "rules": [
          {"key": "cloud.provider.com/rack", "maxShareAllowingPlacement": 45}
        ]
      }
```

Each pod in the group carries the same annotations. The `app` label on each Pod is
propagated to the Workload via `integrations.labelKeysToCopy`, making it available
for the `workloadLabelSelector`.

#### Story 4: Soft spreading with limited capacity

An operator prefers zone spreading but recognizes that capacity constraints may
prevent strict enforcement. They configure `enforcementMode: Preferred` in the annotation:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: main-inference-service
spec:
  template:
    metadata:
      labels:
        app: main-inference-service
      annotations:
        kueue.x-k8s.io/podset-topology-spreading: |
          {
            "rules": [
              {"key": "topology.kubernetes.io/zone", "maxShareAllowingPlacement": 45, "enforcementMode": "Preferred"}
            ]
          }
```

The scheduler still admits the workload, but uses spread-aware domain ordering 
to prefer zones that are below the threshold, falling back to crowded zones 
only when no better option is available.

### Risks and Mitigations

#### User confusion when choosing maxShareAllowingPlacement

The semantics of `maxShareAllowingPlacement` are non-trivial: users must reason
about the maximum share a domain may hold *before* the next group is placed there,
which determines both the minimum number of domains that will be opened and the
maximum concentration allowed per domain. Choosing the wrong value can lead to
either excessive fragmentation (value too low, many domains opened) or poor
availability (value too high, groups concentrated in few domains).

To mitigate this, documentation will explain the field with best practices and
examples. In particular, the following reference table maps value ranges to the
minimum number of domains opened initially and to example steady-state
distributions:

| `maxShareAllowingPlacement` | Minimum domains before reuse | Example steady-state shares |
|---:|---:|---|
| 50–99 | 2 | `50` → `[50, 50]`; `60` → `[60, 40]`; `80` → `[80, 20]` |
| 34–49 | 3 | `34` → `[34, 34, 32]`; `40` → `[40, 40, 20]`; `45` → `[45, 45, 10]` |
| 25–33 | 4 | `25` → `[25, 25, 25, 25]`; `30` → `[30, 30, 30, 10]`; `33` → `[33, 33, 33, 1]` |
| 20–24 | 5 | `20` → `[20, 20, 20, 20, 20]`; `22` → `[22, 22, 22, 22, 12]`; `24` → `[24, 24, 24, 24, 4]` |

The general rule is: a value of `maxShareAllowingPlacement = V` opens
`ceil(100 / V)` domains before any domain is reused.

#### Performance impact of counting PodSet groups per domain

Counting matching PodSet groups across the namespace on every scheduling cycle
could be expensive at high workload counts. In alpha the scheduler performs a
plain scan of the in-memory snapshot on each scheduling cycle; cross-cycle caching
is deferred to beta pending performance benchmarks.

## Design Details

### API

#### Annotation format

Workloads opt into topology spreading by adding an annotation to their **PodSet
template**. The annotation key is:

```text
kueue.x-k8s.io/podset-topology-spreading
```

The annotation value is a JSON object with the following structure:

```json
{
  "workloadLabelSelector": "<label-selector-string>",
  "rules": [
    {
      "key": "<topology-level-key>",
      "maxShareAllowingPlacement": 45,
      "enforcementMode": "Required"
    }
  ]
}
```

`workloadLabelSelector` is optional. When omitted, it defaults to
`kueue.x-k8s.io/job-uid=<value>`, where `<value>` is the `kueue.x-k8s.io/job-uid`
label of the current workload. This means the spreading group is all workloads that
share the same parent job (e.g., all groups from one LWS object).

Example with two rules at different topology levels:

```yaml
apiVersion: kueue.x-k8s.io/v1beta2
kind: Workload
metadata:
  labels:
    app: main-inference-service
spec:
  podSets:
    - name: main
      template:
        metadata:
          annotations:
            kueue.x-k8s.io/podset-topology-spreading: |
              {
                "workloadLabelSelector": "app=main-inference-service",
                "rules": [
                  {"key": "topology.kubernetes.io/zone", "maxShareAllowingPlacement": 45},
                  {"key": "cloud.google.com/gke-tpu-partition-4x4x4-id", "maxShareAllowingPlacement": 22}
                ]
              }
```

The top-level JSON fields are:

| Field | Type | Required | Default | Description |
|---|---|---|---|---|
| `workloadLabelSelector` | string | no | `kueue.x-k8s.io/job-uid=<current-job-uid>` | A Kubernetes label selector string (e.g., `"app=my-service"`). Identifies which admitted workloads in the same namespace form the spreading group for counting purposes. When omitted, defaults to matching all workloads that share the same parent job UID. |
| `rules` | array | yes | — | One or more spreading rules. Each rule independently targets one topology level. At least one rule must be present. At most two rules may be specified in alpha. |

Each element of `rules` is:

| Field | Type | Required | Default | Description |
|---|---|---|---|---|
| `key` | string | yes | — | The topology level key for this rule. Must correspond to one of the `spec.levels[].nodeLabel` values in the `Topology` resource referenced by the evaluated `ResourceFlavor`'s `spec.topologyName`. Not validated at annotation creation time — validation happens at scheduling time. |
| `maxShareAllowingPlacement` | integer | yes | — | Integer percentage in [1, 99]. A domain is eligible to receive the next PodSet group only if its current share of the total is at most this value. |
| `enforcementMode` | string | no | `"Required"` | Enforcement mode: `"Required"` blocks admission into over-threshold domains; `"Preferred"` deprioritizes them via spread-aware domain ordering but still allows admission. |

#### Field definitions

**`workloadLabelSelector`**

An optional Kubernetes label selector string, following the same syntax as
`metav1.LabelSelector` but expressed as a compact string (the format accepted by
`labels.Parse`). The selector is evaluated against the `metadata.labels` of every
admitted `Workload` object in the same namespace. All workloads whose labels
match the selector contribute to the domain counts, regardless of whether they
themselves carry the `kueue.x-k8s.io/podset-topology-spreading` annotation.

When omitted, the selector defaults to `kueue.x-k8s.io/job-uid=<value>`, where
`<value>` is the `kueue.x-k8s.io/job-uid` label of the current workload. The
`kueue.x-k8s.io/job-uid` label is set automatically on every Workload by Kueue and
requires no additional configuration. This default is appropriate for integrations
where all spreading-group members share a common parent object (e.g., all replica
groups created from one LWS object).

When an explicit selector is used with custom labels (e.g., `"app=my-service"`),
those labels must be propagated to the Workload via `integrations.labelKeysToCopy`:

> **Dependency on `integrations.labelKeysToCopy`**: Kueue does not copy all labels
> from the underlying Job onto the created Workload automatically — only label keys
> explicitly listed in `integrations.labelKeysToCopy` in the Kueue `Configuration`
> are propagated. For example, to use `"workloadLabelSelector": "app=main-inference-service"`,
> add `app` to `integrations.labelKeysToCopy`:
>
> ```yaml
> integrations:
>   labelKeysToCopy:
>     - app
> ```
>
> Without this configuration the label will not appear on the Workload and the
> explicit selector will match nothing, causing spreading to have no effect. The
> default job-uid selector does not require this configuration.

**`maxShareAllowingPlacement`**

An integer value in the range [1, 99]. It is a before-placement gate: a domain is
eligible to receive the next PodSet group only if the domain's current share of
the total admitted PodSet groups in the spreading group is at most this value. In
other words, `maxShareAllowingPlacement` is the maximum current share a domain may
hold for the next placement to be allowed there — the new group itself is not yet
counted when the check is performed.

**`enforcementMode`**

- `"Required"` (default): Kueue will not admit the workload into a domain where
  the constraint would be violated.
- `"Preferred"`: Kueue will admit the workload into any available domain, but will
  deprioritize domains that exceed `maxShareAllowingPlacement` via spread-aware
  domain ordering.

#### Spreading group

**Scheduling unit.** The unit of placement for spreading is the **PodSet**. When
a PodSet carries `kueue.x-k8s.io/podset-group-name`, all PodSets in that group
share the same topology domain assignment and are treated as one unit — a
**PodSet group**. PodSets without `podset-group-name` are each their own unit.

**Accounting key.** Domain occupancy is tracked per tuple
`(namespace, effective-label-selector, effective-podset-name, topology-domain)`,
where *effective-podset-name* is the `podset-group-name` value if set, or the
individual PodSet name otherwise. This mirrors how TAS tracks topology assignments.

**Spreading group.** The spreading group for a rule is the set of admitted
Workloads in the namespace whose `metadata.labels` match the effective label
selector (`workloadLabelSelector` if specified, otherwise
`kueue.x-k8s.io/job-uid=<current-job-uid>`). Only Workloads with
`status.admission` set are counted; pending or suspended Workloads contribute
nothing. A Workload need not carry the annotation to be counted — it only needs
matching labels and a topology assignment at the relevant level. Workloads that do
carry the annotation enforce the rules at their own admission time.

#### Admission constraint formula

For a given domain `D` and rule with `key` K, let:

- `count(D)` = number of admitted PodSet groups in domain `D` (at topology level K)
  among workloads matching the effective selector
- `N` = total number of admitted PodSet groups across all domains at level K
  among workloads matching the effective selector

A domain `D` is admissible for a new PodSet group if and only if:

```text
N == 0 || 100 * count(D) <= maxShareAllowingPlacement * N
```

where `maxShareAllowingPlacement` is the integer value of the field (e.g., `45`).
The check is performed **before** the new group is counted — `count(D)` and `N`
reflect only the currently admitted groups, not the candidate being placed.

When `N == 0` (no PodSet groups have been admitted yet), any domain is
admissible — the first PodSet group can always be placed regardless of which
domain is selected (cold-start case).

The `<=` means a domain whose current share is exactly `maxShareAllowingPlacement`
percent of the total is still eligible to receive the next group. A domain
becomes ineligible only when its share strictly exceeds the threshold.

The following example illustrates the formula for 3 domains and
`maxShareAllowingPlacement: 34` (each row represents one incoming PodSet group):

| PodSet group # | Domain A (count → %) | Domain B (count → %) | Domain C (count → %) | Placed in |
|---|---|---|----------------------|---|
| 1 | 0 → 0% ✓ | 0 → 0% ✓ | 0 → 0% ✓             | A |
| 2 | 1 → 100% ✗ | 0 → 0% ✓ | 0 → 0% ✓             | B |
| 3 | 1 → 50% ✗ | 1 → 50% ✗ | 0 → 0% ✓             | C |
| 4 | 1 → 33% ✓ | 1 → 33% ✓ | 1 → 33% ✓            | A |
| 5 | 2 → 50% ✗ | 1 → 25% ✓ | 1 → 25% ✓            | B |
| 6 | 2 → 40% ✗ | 2 → 40% ✗ | 1 → 20% ✓            | C |

#### Validation and error handling

Validation is split into two layers:

**Workload webhook (at creation/update time):**

1. If the PodSet template annotation `kueue.x-k8s.io/podset-topology-spreading` is
   present, its value must be valid JSON that parses according to the schema above:
   an optional `workloadLabelSelector` string, and a `rules` array with 1–2 elements
   (alpha milestone limit), each containing a valid `key`, a `maxShareAllowingPlacement`
   integer in [1, 99], and an optional `enforcementMode` of `"Required"` or `"Preferred"`.
2. If `workloadLabelSelector` is present, it must be a syntactically valid
   Kubernetes label selector string.
3. All `key` values within the `rules` array must be distinct. Two rules for the
   same topology key are rejected because the combined behavior would be ambiguous.
4. For each distinct `kueue.x-k8s.io/podset-group-name` value in the workload,
   all PodSets in that group must either all carry the same
   `kueue.x-k8s.io/podset-topology-spreading` annotation value or none must carry it.
   Partial annotating within a group (some PodSets annotated, others not) is rejected.

If the workload webhook check fails, the workload creation or update is rejected
with a descriptive error message.

**Key validity at scheduling time:**

Rule `key` values are not validated against topology levels in `ResourceFlavor`
objects at annotation creation time — ResourceFlavor configurations can change
independently and the scheduler is better positioned to evaluate the match at
admission time.

When a key does not match any topology level, the rule is ignored for that flavor
regardless of whether the `enforcementMode` is `"Required"` or `"Preferred"` — the workload is
admitted if capacity is available, without spreading being applied. To surface
the misconfiguration, the scheduler sets a `TopologySpreadKeyNotFound` workload
condition identifying the unmatched key. See [Visibility](#visibility).

### Scheduler integration

The spreading logic is applied inside `findTopologyAssignment`, during the
per-flavor evaluation phase of the Kueue scheduler. Topology Spreading only applies
to topology-enabled ResourceFlavors (those with `spec.topologyName` set).

#### Computing per-domain PodSet group counts

The annotation is copied automatically from the Job's pod template by each job
integration's `PodSets()` method. Before evaluating a candidate workload, the
scheduler reads the annotation from each PodSet template, resolves the effective
label selector, and for each rule's `key` builds a `domainID → count` map by
scanning the admitted Workloads in the spreading group (see [Spreading
group](#spreading-group)): each PodSet (or PodSet group, if `podset-group-name`
is set) that has a topology assignment at the rule's `key` level contributes 1
to the count for its assigned domain value. The candidate itself is not yet
admitted and is not included.


#### Banned and over-threshold domains

From the per-domain PodSet group counts, two derived sets are computed for each rule:

- **Banned domains** (applies to `Required` rules): domains where
  `N > 0 && 100 * count(D) > maxShareAllowingPlacement * N`. **Candidates whose
  topology domain is banned are removed from the candidate list** — the entire
  candidate entry is dropped, not just individual nodes within it. When `N == 0`
  no domain is banned.
- **Over-threshold domains** (applies to `Preferred` rules): domains where
  `N > 0 && 100 * count(D) > maxShareAllowingPlacement * N`. These domains remain
  in the candidate list but are placed in a lower-priority scoring tier (see
  [Scoring](#scoring) below).

#### Scoring

For `Required` rules, banned domains are removed from the candidate list before
scoring. For `Preferred` rules, over-threshold domains are deprioritized but remain
eligible.

The spread-aware domain ordering integrates with the existing TAS candidate ordering.
Domains below the threshold are preferred over over-threshold ones; among
over-threshold domains (`Preferred` only), the least-loaded is chosen first.

### Visibility

**`Required` rule — temporarily blocked (domains exist but are full):**

When a workload cannot be admitted because all eligible domains exceed
`maxShareAllowingPlacement`, the following surfaces are updated:

- **Workload condition**: `QuotaReserved: False` or `Admitted: False` with
  `reason: TopologySpreadConstraintNotMet` and a message describing which rule,
  which PodSet, and which domains are blocked. The condition is cleared when the
  workload is eventually admitted. This reason indicates a **transient** state —
  the workload will be retried and may be admitted once capacity frees up.
- **Logs**: a structured log entry at `V(3)` naming the workload, the PodSet
  name, the effective selector, the violated rule key, and the per-domain
  PodSet group counts.

**`Required` or `Preferred` rule — key not found on ResourceFlavor (admitted but rule not applied):**

When a rule's `key` does not match any `spec.levels[].nodeLabel` in the `Topology`
referenced by the evaluated `ResourceFlavor`, the rule is ignored for that flavor
and the workload is admitted if capacity is available. To surface the
misconfiguration, the scheduler sets a new workload condition of type
`TopologySpreadKeyNotFound` (value `True`) with a message identifying the unmatched
key. This is a new `ConditionType` that will be added to the Workload API alongside
the existing `QuotaReserved` and `Admitted` types.

**Admitted onto a non-topology flavor (spreading not applied):**

When a workload carrying the annotation is admitted onto a `ResourceFlavor` that
has no `spec.topologyName` (non-TAS flavor), spreading cannot be evaluated:

- **Workload condition**: a new condition of type `TopologySpreadingNotApplied`
  (value `True`) with a message identifying the affected PodSet name(s) and the
  flavor each was assigned to. This is cleared if the workload is later
  re-admitted onto topology-enabled flavors for all affected PodSets.
- **Logs**: a structured log entry at `V(3)`.

**`Preferred` rule — admitted into an over-threshold domain (SpreadTier 2):**

When a workload with a `Preferred` rule is admitted into a domain that exceeds
`maxShareAllowingPlacement` (because no SpreadTier 0 or SpreadTier 1 domain was
available), the workload
is admitted successfully. **No condition is set** — `Preferred` semantics
explicitly allow admission into crowded domains, so this is not an error or
warning state. A log entry at `V(4)` records the workload name, PodSet, the effective selector,
and the domain chosen along with its domain count.

### Feature gate

The entire feature is gated behind a feature gate named `TASTopologySpreading`
(disabled by default in alpha). When the gate is off:

- The `kueue.x-k8s.io/podset-topology-spreading` annotation is ignored: all workloads
  are scheduled as if the annotation were absent.
- The Workload webhook checks for the topology-spreading annotation are disabled.
  This allows operators to annotate workloads without being blocked by validation
  checks, making it possible to stage configuration before enabling the gate.

## Test Plan

[ ] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes
necessary to implement this enhancement.

### Prerequisite testing updates

- Ensure that the existing TAS integration tests cover the topology-key/label
  model that this feature relies on, to avoid regressions when the spreading
  logic interacts with TAS assignments.

### Unit tests

The following unit tests will be added or extended:

- `pkg/scheduler` (annotation parsing and rule application): target ≥ 80%
- `pkg/scheduler` (banned/over-threshold domain computation): target ≥ 80%
- `pkg/scheduler` (spread-aware domain ordering): target ≥ 80%
- `pkg/cache` (cache invalidation on admit/evict): target ≥ 80%

Concrete test cases:

1. Workload webhook rejects creation when the annotation value is not valid JSON.
2. Workload webhook rejects creation when `workloadLabelSelector` is present but
   not a valid Kubernetes label selector.
3. Workload webhook rejects creation when `maxShareAllowingPlacement` is outside [1, 99].
4. Workload webhook rejects creation when `rules` contains duplicate `key` values.
5. Workload webhook rejects creation when PodSets within the same
   `kueue.x-k8s.io/podset-group-name` group carry different annotation values (or
   a mix of annotated and unannotated PodSets).
6. Workloads with a valid annotation are parsed and rules applied correctly.
7. Only admitted workloads whose labels match the effective selector are counted
   (workloads that do not match are ignored).
8. Only PodSet groups with a topology assignment at the rule's `key` level contribute
   to the count; groups without that topology level are skipped.
9. Only PodSets in the same namespace are counted (cross-namespace spreading is not
   supported).
10. Correct per-domain PodSet group count computation for:
    - Multiple workloads in multiple domains.
    - A workload with multiple annotated PodSets: each PodSet counted independently
      in its own domain (one workload can contribute more than 1 to the total).
    - A workload with some PodSet groups annotated and some not: all PodSet groups
      with a topology assignment at the rule's key level are counted, regardless of
      whether the PodSet carries the annotation.
    - Workloads that are candidates for preemption (their PodSet groups excluded
      from the count).
    - Workloads that were preemption candidates but are no longer (count restored).
11. Banned domain set correctly derived for `Required` rules.
12. Over-threshold domain set correctly derived for `Preferred` rules.
13. Multiple rules (different topology level keys) in a single annotation all applied.
14. Correct counts after a workload matching the label selector is admitted within
    the same scheduling cycle.
15. Correct counts after a workload matching the label selector is evicted within
    the same scheduling cycle (snapshot reflects the removal).
16. PodSet template annotation propagated correctly from Job pod template to Workload
    PodSet template.
17. Targeting a topology key not present on the ResourceFlavor: workload is admitted
    (rule ignored for that flavor regardless of `Required` or `Preferred`) and
    condition `TopologySpreadKeyNotFound` is set in both cases.

### Integration tests

1. End-to-end admission: a new workload is denied admission into a banned domain
   and held pending until another domain becomes available (Required rule).
2. Preemption-driven spreading: a high-priority workload triggers preemption of
   a lower-priority workload to free a less-loaded domain.
3. Preferred spreading (happy path): successive workloads are spread across
   available domains when SpreadTier 0 or SpreadTier 1 domains exist — each new
   workload is admitted into the highest-priority domain ordering.
4. Preferred spreading (fallback): a workload is admitted into a SpreadTier 2
   (over-threshold) domain when no SpreadTier 0 or SpreadTier 1 domain is available;
   the workload reaches `Admitted` state with no spreading-related condition set
   (Preferred semantics allow crowded-domain admission).
5. Multiple rules at two independent topology levels (e.g., zone and rack) in a
   single annotation — both caps applied simultaneously.
6. Feature gate disabled: annotation is silently ignored; workloads are
   scheduled normally.
7. Cross-workload counting: admitted workloads from separate jobs but with
   matching labels all contribute to domain counts for a newly scheduled workload.
8. Workloads without the annotation but with matching labels are counted in the
   spreading group.
9. Integration with LWS, Deployment, and plain Pods (alpha-supported integrations):
   annotation is read from the respective Kueue Workload object; LWS multi-PodSet
   workloads are verified to contribute one count per annotated PodSet group.

### e2e tests

E2e tests verifying zone-level spreading on a multi-block kind cluster with
topology-labeled nodes, covering both Required and Preferred modes, using the
`kueue.x-k8s.io/podset-topology-spreading` annotation targeting multiple concurrently
admitted workloads, using both the default job-uid selector and explicit selectors.

### Graduation Criteria

#### Alpha

- Feature gate `TASTopologySpreading` introduced, disabled by default.
- Support for up to two topology levels.
- `Required` and `Preferred` rule types implemented.
- Annotation supported for Deployment, plain Pods, and LWS only; other integrations
  (e.g. JobSet) are rejected in alpha.
- Unit and integration tests covering all cases listed in [Test Plan](#test-plan).

#### Beta

- Feature gate enabled by default.
- More than two topology levels supported (following user feedback on alpha usage).
- Metrics added to track workloads blocked by spreading constraints.
- Early validation of the `kueue.x-k8s.io/podset-topology-spreading` annotation added
  to built-in job integration webhooks (Job, JobSet, LWS, etc.) via a shared
  `jobframework` helper, so that users get an immediate rejection at job creation
  time rather than discovering the misconfiguration via a Workload condition.
- E2e test on a multi-block cluster.
- Performance benchmarks (`BenchmarkSchedulerTAS` in
  `pkg/scheduler/scheduler_tas_bench_test.go`) extended with a spreading-enabled
  scenario to measure scheduling throughput overhead introduced by the per-cycle
  domain count scan, and to establish the baseline for evaluating any caching improvement.
- Cross-cycle domain count cache added only if benchmarks demonstrate a performance
  need; implementation must be accompanied by benchmarks showing the improvement.
- No performance regression in TAS scheduling benchmarks (< 5% overhead for
  clusters with ≤ 1000 concurrent workloads).
- Re-evaluate which additional job integrations (e.g. Job, JobSet, RayJob, PyTorchJob,
  AppWrapper) should support the annotation based on user feedback gathered during alpha.
- Re-evaluate whether to replace the annotation-based configuration with a typed API
  field in the PodSet spec based on user feedback gathered during alpha.
- Documentation updated.
- No known correctness or performance bugs

#### Stable

- Feature gate removed; feature unconditionally enabled.
- All known bugs fixed and users' feedback addressed.

## Implementation History

- 2026-07-28: KEP created based on design document authored by Marcin Wielgus.

## Drawbacks

- Inline annotation-based configuration is less discoverable than a dedicated CRD:
  administrators cannot list all spreading configurations with `kubectl get` on a
  single resource type. This is mitigated by the fact that the annotation resides
  on the user's Job or Deployment pod template, which is already the primary
  configuration artifact.
- Changing a spreading configuration requires updating all workloads that carry the
  annotation, rather than updating a single shared policy object. For large fleets
  of identical workloads managed through higher-level abstractions (e.g., a
  Deployment), a single annotation update propagates to all replicas via the
  template, so this is primarily a concern for workloads managed without a shared
  template.
- The per-domain counting adds scheduler complexity. This is mitigated by the
  constraint that matching is namespace-scoped and by the lightweight per-cycle
  snapshot scan used in alpha.
- Spreading is not enforced after admission. In dynamic environments where
  workloads frequently complete and new ones are admitted, the actual distribution
  may drift from the target. This is a deliberate trade-off to avoid continuous
  eviction storms.

## Alternatives

### Other API design alternatives

#### Use maxSkew

Kubernetes `topologySpreadConstraints` uses `maxSkew` — a maximum allowed
difference in pod counts between any two domains. This is intuitive for small
homogeneous clusters but becomes awkward when:

- Domains vary significantly in size or capacity (a skew of 1 between a 1-pod
  domain and a 100-pod domain is very different from a skew of 1 between two
  50-pod domains).
- Capacity is located in a single zone or superblock that provides many smaller
  domains (e.g., TPU cubes, GB200 racks), where fragmenting across all domains
  is not recommended.

More importantly, `maxSkew` does not control the **spreading footprint** — the
number of domains actually used. For AI inference workloads composed of
multi-pod groups (e.g., LWS with 8 groups of 2 pods each), `maxSkew` says
nothing about how many racks those groups are spread across. A distribution of
`1/1/1/1/1/1/1/1` (8 racks), `2/2/2/2` (4 racks), and `4/4` (2 racks) all
satisfy `maxSkew: 0`, yet they have very different fragmentation profiles.
Spraying 8 groups across 8 racks consumes 8× the rack-level resources and
leaves small, hard-to-fill gaps in each rack — exactly the fragmentation pattern
that hurts dense GPU/accelerator clusters. Users of serving workloads need to
express a cap on how concentrated the load can be in any single domain, which
directly controls the minimum footprint; `maxSkew` provides no such lever.

`maxShareAllowingPlacement` expresses the constraint as a fraction of the total,
which naturally accommodates growth and heterogeneous domain sizes without
requiring reconfiguration as the workload population changes.

#### Use minDomains

The `minDomains` approach would grow the number of occupied domains until
`minDomains` is reached, then gradually fill existing domains until exhausted,
and only then expand to new domains. This is easy to describe and reason about.

The key trade-off is that `minDomains` allows highly unbalanced distributions
such as 98/1/1 once the minimum domain count is satisfied, whereas
`maxShareAllowingPlacement` bounds the maximum concentration in any single
domain regardless of how many domains are occupied. For AI inference workloads
where availability (not just domain count) is the goal, a cap on concentration
is a stronger and more direct guarantee.

### Dedicated TopologySpreadingPolicy CRD

An earlier design introduced a separate namespaced `TopologySpreadingPolicy` CRD
that held the spreading configuration, with workloads referencing it by name via
a simpler annotation (`kueue.x-k8s.io/podset-topology-spreading-policy: <policy-name>`).
The policy name served as the spreading group identifier. The inline annotation
approach was chosen instead because:

- **No referential integrity management**: with a separate CRD, a validating
  webhook on `TopologySpreadingPolicy` was required to block deletion when
  workloads still referenced the policy. With inline configuration, there is no
  separate object to delete and no dangling-reference problem.
- **No ordering dependency**: workloads can be created and annotated without first
  creating a policy object. This simplifies bootstrapping and CI pipelines.
- **Self-contained workload spec**: the full spreading configuration is visible
  on the workload's own pod template rather than being distributed across two
  separate objects. Operators debugging admission behavior can read all relevant
  configuration from a single `kubectl describe`.
- The `workloadLabelSelector` field provides an equally clear and unambiguous
  group identifier: all workloads whose labels match the selector are in the same
  spreading group. Operators who want separate spreading groups use different
  label selectors.

The CRD approach has the advantage of centralized, reusable policy objects and
CRD-schema-enforced validation at policy creation time. It would be appropriate
to revisit this approach if future requirements call for shared policy management
across large fleets or fine-grained RBAC separation between spreading
configuration and workload creation.

### Enforce spreading via kube-scheduler Pod Topology Spread Constraints

One could translate Kueue spreading rules into pod-level
`topologySpreadConstraints` at admission time. This approach has several problems:

1. It operates at pod granularity, not workload granularity — a single workload
   with many pods would trivially satisfy a zone-level spread even if all pods
   land in the same zone.
2. kube-scheduler evaluates spreading using the live pod count, which may change
   between Kueue admission and pod scheduling, leading to races.
3. kube-scheduler does not understand Kueue workload identity, so the
   `labelSelector` would need to target pods rather than workloads, making it
   impossible to express "one workload per domain" semantics.