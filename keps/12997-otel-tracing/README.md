# KEP-12997: Pluggable Distributed Tracing (OpenTelemetry)

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
  - [Notes/Constraints/Caveats](#notesconstraintscaveats)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [Configuration](#configuration)
  - [Feature gate](#feature-gate)
  - [Provider bootstrap and propagation](#provider-bootstrap-and-propagation)
  - [Instrumented spans](#instrumented-spans)
  - [Workload-lifecycle traces](#workload-lifecycle-traces)
  - [Test Plan](#test-plan)
    - [Unit tests](#unit-tests)
    - [Integration tests](#integration-tests)
    - [e2e tests](#e2e-tests)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
<!-- /toc -->

## Summary

Add opt-in distributed tracing to the Kueue controller using OpenTelemetry,
following the [Kubernetes system-component tracing][k8s-tracing] pattern used by
kubelet and kube-apiserver. Kueue exports spans as OTLP over gRPC to an
OpenTelemetry Collector, configured via a `Tracing` block on the Kueue
`Configuration` and gated behind an alpha `KueueTracing` feature gate.

[k8s-tracing]: https://kubernetes.io/docs/concepts/cluster-administration/system-traces/

## Motivation

Kueue admits workloads through a chain of asynchronous reconcile loops —
scheduling, quota reservation, and admission checks. When a workload is slow to
admit or gets stuck, metrics and logs show *that* it happened but not *where*
the time went across those loops. Distributed traces make the admission path
observable end to end and let operators correlate Kueue's work with the rest of
their OpenTelemetry-instrumented stack.

### Goals

- Emit spans for the workload admission path (scheduling cycle, quota
  reservation, admission checks, workload reconcile).
- Reuse the K8s system-component tracing configuration shape and OTLP →
  Collector export model, so the operator experience matches kubelet/apiserver.
- Ship opt-in and off by default behind a feature gate, with negligible overhead
  when disabled.

### Non-Goals

- Replacing or changing the existing Prometheus metrics (tracked separately in
  #8848 for OTLP metrics export).
- Tracing outside the controller (e.g. `kueuectl`, kueueviz).
- Prescribing a specific tracing backend; export terminates at an OTel Collector.

## Proposal

Add a `Tracing` configuration block and a `KueueTracing` feature gate. When
enabled, bootstrap an OpenTelemetry `TracerProvider` at controller startup,
propagate trace context on outbound kube-apiserver calls, and open spans on the
admission path.

### User Stories

- As a cluster operator, I enable tracing pointed at my OTel Collector and see a
  span breakdown of how long each workload spends in scheduling vs. quota
  reservation vs. admission checks.
- As a Kueue developer, I use traces to locate latency regressions in the
  scheduling pipeline without adding ad-hoc logging.

### Notes/Constraints/Caveats

The interesting unit to trace — a workload's admission journey — spans multiple
reconciles over time, which does not map onto a single request-scoped context
the way kubelet's per-CRI-call traces do. See
[Workload-lifecycle traces](#workload-lifecycle-traces) for the phased approach.

Configuration is **cluster-scoped only**. The `Tracing` block is set by the
cluster admin on the Kueue `Configuration`; this proposal adds no per-object
(Workload/Queue) API to control tracing. A per-object opt-in / sampling API
could be added later (see [Alternatives](#alternatives)) but is intentionally
out of scope for alpha — it enlarges the API surface and would introduce a
user-facing trigger for span emission (see the security note below).

### Risks and Mitigations

- **Overhead / cardinality:** sampling is controlled via
  `samplingRatePerMillion`; disabled by default. Spans are scoped to the
  admission path rather than every reconcile of every object.
- **Annotation churn (full phase):** persisting span context on Workloads adds
  writes; mitigated by writing it once at workload creation and gating the full
  phase separately.

## Design Details

### Configuration

Add `Tracing *TracingConfiguration` to the Kueue `Configuration`, reusing
`k8s.io/component-base/tracing/api/v1.TracingConfiguration` so the fields
(`endpoint`, `samplingRatePerMillion`) and validation match kubelet and
kube-apiserver exactly.

### Feature gate

New alpha feature gate `KueueTracing`, off by default, `disable-supported: true`.

### Provider bootstrap and propagation

At controller startup, build a `TracerProvider` via
`k8s.io/component-base/tracing.NewProvider(...)`, register it globally, and set
the global text-map propagator. Wrap the kube-apiserver client transport so
outbound API calls emit spans and propagate context.

### Instrumented spans

Open spans on the admission path, nesting on the reconcile `context.Context`:

- scheduling cycle
- quota reservation
- admission checks
- workload reconcile

### Workload-lifecycle traces

Two phases:

1. **Alpha (MVP):** per-reconcile / per-cycle spans only, no cross-reconcile
   correlation. Immediately useful for latency debugging and low risk.
2. **Later:** stitch a single trace across a workload's lifecycle by persisting
   the trace/span context on the Workload in a **controller-written**
   annotation (e.g. `kueue.x-k8s.io/trace-context`, holding a W3C
   `traceparent`). Kueue generates this context itself at admission; it is
   **not** a user-supplied opt-in signal, and Kueue does not trace a workload
   merely because a user set an annotation.

   **Security / DoS:** because the annotation is written by Kueue rather than
   honored from user input, and sampling stays governed server-side by
   `samplingRatePerMillion` plus the feature gate, users cannot force or
   amplify span emission — so this is not a DoS vector. If a future revision
   chose to honor a user-provided trace context (e.g. to link with an external
   submitter's trace), it must remain subject to the same server-side sampling
   cap and validation to preserve this property. Larger design/review surface;
   gated separately.

### Test Plan

[ ] I/we understand the owners of the involved components may require updates to
existing tests.

#### Unit tests

- Config defaulting/validation for the `Tracing` block.
- Provider wiring is a no-op when the feature gate or config is absent.

#### Integration tests

- With tracing enabled against an in-test OTLP receiver, admitting a workload
  produces the expected spans on the admission path.

#### e2e tests

- Alpha: verify controller starts and admits workloads with tracing enabled
  pointing at a Collector; no functional regression when disabled.

### Graduation Criteria

- **Alpha:** feature gate off by default; MVP spans on the admission path;
  config + docs merged.
- **Beta:** feedback incorporated; consider workload-lifecycle traces;
  performance validated under load.

## Implementation History

- 2026-07-12: Issue #12997 filed; KEP drafted (provisional).

## Drawbacks

Adds an optional dependency surface (OTel SDK/exporters) and a new config/gate to
maintain. Mitigated by keeping it opt-in and reusing the shared K8s tracing
libraries.

## Alternatives

- **Prometheus/exemplars only:** no causal, cross-component view of the admission
  path.
- **Custom tracing config instead of `component-base/tracing`:** diverges from
  the established kubelet/apiserver UX for no benefit.
- **Per-object tracing API (deferred):** a field on Workload / LocalQueue /
  ClusterQueue to opt in or set per-object sampling. Deferred: it enlarges the
  API surface and creates a user-controlled path to emit spans (see the
  security note under [Workload-lifecycle traces](#workload-lifecycle-traces));
  the cluster-scoped config covers the alpha use cases. Can be revisited if
  operators need finer-grained control.

Prior art in Kueue: @vladikkuzn's draft tracing experiment in #10818 explored
this direction; this KEP formalizes it. Related: #8848 (OTLP metrics export) is a
complementary push-based transport sharing the same OTLP → Collector model.
