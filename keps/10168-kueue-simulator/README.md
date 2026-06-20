# KEP-10168: Kueue Simulator

Issue: [kubernetes-sigs/kueue#10168](https://github.com/kubernetes-sigs/kueue/issues/10168)

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals (initially)](#non-goals-initially)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
  - [Relationship to existing tooling](#relationship-to-existing-tooling)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [Architecture (phased)](#architecture-phased)
  - [Simulation model](#simulation-model)
  - [API and configuration surface](#api-and-configuration-surface)
  - [Observability and outputs](#observability-and-outputs)
  - [Test Plan](#test-plan)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
<!-- /toc -->

## Summary

Introduce a **lightweight Kueue simulator** so users can exercise Kueue’s
queueing, admission, preemption, cohort borrowing, and fair-sharing decisions
**without** provisioning a full Kubernetes cluster or running real Pods. For the
MVP, fidelity of **Kueue decision-making** matters more than modeling kube-scheduler
or node-level placement. Optional integration with kube-scheduler may follow and
must not block the initial deliverable.

## Motivation

Validating Kueue behavior today usually requires a real cluster and real
workloads, which is slow and expensive. Teams need a cheap way to:

- Test policies and admission behavior
- Compare queueing, preemption, and fair-sharing configurations
- Reproduce waiting, admission, and eviction scenarios
- Run large-scale what-if analysis
- Perform regression testing for Kueue changes

### Goals

- Provide a documented path to simulate many workloads virtually at low cost.
- Reuse Kueue’s real scheduling and cache logic where practical so behavior
  matches production controllers.
- Support scripted scenarios (create/update/delete workloads and queue config,
  advance simulated time, inject completions) suitable for CI and benchmarks.
- Produce reproducible artifacts (event traces, metrics snapshots, summaries)
  for comparison across Kueue versions.

### Non-Goals (initially)

- Accurate end-to-end Kubernetes execution (real kubelet, networking, volumes).
- Mandatory kube-scheduler integration in the first milestone.
- Replacing KWOK or performance tests; the simulator complements them.

## Proposal

Deliver the feature in **phases**, with a **design doc (this KEP)**, **user-facing
documentation**, and **explicit configuration/API** for scenarios.

**Phase 1 (MVP):** A standalone command (or documented workflow) that runs Kueue’s
core controllers and scheduler against a **minimal Kubernetes API surface**
(e.g. envtest-style apiserver without real nodes), plus a **workload driver** that
creates and updates Kueue objects and **mimics workload lifecycle** (similar to
the existing scalability runner’s execution model). Scenario inputs are
**versioned YAML** (and optionally a small JSON schema) describing queue
topology, workload templates, arrival patterns, and duration model.

**Phase 2:** Optional **Kubernetes API extensions** (e.g. a `Simulation` or
`WorkloadTrace` CRD) if the community wants in-cluster orchestration,
status, and RBAC—only after Phase 1 proves the UX and semantics.

**Phase 3 (optional):** Pluggable **kube-scheduler** or placement shim for
scenarios that need node-level feasibility, behind a feature flag.

### User Stories

1. As a **Kueue maintainer**, I want to run a fixed scenario in CI and compare
   admission order and preemption outcomes across commits.
2. As a **platform engineer**, I want to model two ClusterQueue configurations
   against the same workload mix and compare wait times and quota usage.
3. As a **support engineer**, I want to replay a reduced scenario that mirrors a
   customer’s queue setup to reason about eviction and borrowing.

### Relationship to existing tooling

- **MinimalKueue + scalability runner** (`test/performance/scheduler/`): already
  runs core Kueue with envtest, generates objects from YAML, and mimics workload
  execution. The simulator should **reuse or extract** this stack rather than
  fork behavior.
- **KWOK / real clusters**: higher fidelity for Kubernetes; higher cost. The
  simulator targets **Kueue semantics first**.

### Risks and Mitigations

| Risk | Mitigation |
|------|------------|
| Drift between simulator and production controllers | Share libraries and integration tests; run the same scenario in envtest and (selectively) e2e |
| Unclear scope (scheduler vs Kueue) | Document non-goals; phase scheduler work |
| Scenario format churn | Version the scenario schema; keep backward compatibility for N releases |

## Design Details

### Architecture (phased)

1. **Scenario loader** — parses versioned scenario files (queues, flavors,
   workloads, timing, completion model).
2. **Driver** — applies object churn to the API at configured rates (QPS/burst
   already tuned in existing runner).
3. **Kueue runtime** — MinimalKueue-equivalent controllers + scheduler (extend
   `test/performance/scheduler/minimalkueue` or promote to `cmd/`).
4. **Lifecycle simulator** — transitions workloads (e.g. admitted → finished)
   without real Pods, aligned with how the scalability runner records events.
5. **Recorder** — event stream, Prometheus scrape or file export, summary stats
   (reuse `runner/recorder` and `runner/stats` patterns).

### Simulation model

- **Time:** support **logical** stepped time for determinism and **wall-clock**
  mode for throughput experiments.
- **Resources:** model nominal/borrowing/lending limits via real
  ClusterQueue/ResourceFlavor objects so the cache sees real structures.
- **Workloads:** prefer real `Workload` API objects; optional higher-level
  generators that emit Workloads from templates.
- **Admission checks:** MVP may stub or disable complex checks; document which
  features are simulated vs skipped.

### API and configuration surface

**MVP (required for issue completion):**

- **Scenario API:** versioned file format (e.g. `apiVersion:
  simulator.kueue.x-k8s.io/v1alpha1` or documented `generator.yaml` evolution)
  checked into docs with examples.

**Optional (Phase 2):**

- CRDs for running simulations in-cluster and surfacing status in Kubernetes,
  if maintainers approve additional API surface.

### Observability and outputs

- Structured **event log** (admit, preempt, requeue, finish).
- **Metrics** compatible with existing Kueue Prometheus metrics where feasible.
- **Exit criteria** hooks for CI (e.g. compare to golden trace or thresholds,
  similar to `rangespec.yaml` in performance tests).

### Test Plan

- **Unit tests:** scenario parsing, time advancement, lifecycle transitions.
- **Integration tests:** run short scenarios under envtest with MinimalKueue;
  assert ordering and quota invariants.
- **Regression:** add one scenario to periodic CI (alongside existing
  performance-scheduler job).

### Graduation Criteria

- **Alpha:** documented command/workflow, scenario schema v1, CI scenario.
- **Beta:** stable schema, feature parity list vs real controller for core
  queueing/preemption paths.
- **GA:** maintainer sign-off, production support statement in docs.

## Implementation History

- 2026-03-31: KEP created (provisional) to track [issue #10168](https://github.com/kubernetes-sigs/kueue/issues/10168).

## Drawbacks

- Another artifact to maintain; must stay aligned with controller refactors.
- Users may assume kube-scheduler fidelity before it exists—docs must be explicit.

## Alternatives

1. **KWOK only** — good for large fake clusters; still heavier than a Kueue-only
   loop and does not focus on Kueue semantics alone.
2. **Record/replay from production** — valuable future extension; higher PII and
   complexity.
3. **Pure in-process fake client** — minimal apiserver footprint but high wiring
   cost to satisfy all informers and indexes; envtest + MinimalKueue is the
   pragmatic MVP base.
