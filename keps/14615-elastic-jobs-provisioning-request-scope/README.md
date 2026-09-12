# KEP-14615: Elastic Jobs and ProvisioningRequest Compatibility Constraints

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [Per-pod vs. per-template annotation lifetime](#per-pod-vs-per-template-annotation-lifetime)
  - [<code>provisioningClassName</code> scope](#provisioningclassname-scope)
  - [Notes/Constraints/Caveats](#notesconstraintscaveats)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [Test Plan](#test-plan)
    - [Unit Tests](#unit-tests)
    - [Integration Tests](#integration-tests)
- [Alternatives](#alternatives)
<!-- /toc -->

## Summary

This KEP documents the compatibility constraints between elastic Jobs
(`ElasticJobsViaWorkloadSlices`) and Kueue's `ProvisioningRequest` admission-check
integration. It formalizes two things raised during review of the elastic-job
scale-up/scale-down support ([#14316](https://github.com/kubernetes-sigs/kueue/pull/14316)):

1. Which `provisioningClassName`s have been exercised against the elastic-job code
   path and are considered in scope, vs. which are explicitly out of scope today.
2. The per-pod vs. per-template lifetime rule for `ProvisioningRequest` identity
   annotations that makes elastic scale-up safe to combine with GKE's
   nodeSelector-injection behavior for `queued-provisioning.gke.io`.

## Motivation

Elastic Jobs replace a running Workload's PodSet counts in place via
`WorkloadSlice` replacement, re-admitting the same underlying Job/RayCluster
template through multiple scale-up/scale-down cycles. `ProvisioningRequest`
admission checks (in particular `queued-provisioning.gke.io` on GKE) rely on a
per-request identity annotation (`autoscaling.x-k8s.io/consume-provisioning-request`)
to let the cloud provider's admission webhook inject a nodeSelector that scopes a
pod to the specific node(s) created for that request. If that identity annotation
were allowed to persist on the long-lived, shared elastic job template, every
future pod (from later, unrelated scale-ups) would inherit a stale value, and a
reviewer correctly flagged that a missing/stale nodeSelector would let a pod
schedule onto arbitrary capacity instead of the capacity actually reserved for it.

This KEP exists to write that constraint down explicitly, rather than leave it as
implicit knowledge in `podset.go`/`elastic_job_ungater.go`, and to scope which
`provisioningClassName`s have actually been verified against it.

### Goals
- Document the per-pod vs. per-template lifetime rule for
  `ProvisioningRequest` identity annotations (`consume-provisioning-request`,
  `provisioning-class-name`) on elastic Jobs.
- Enumerate which `provisioningClassName`s are verified-safe for elastic Jobs
  today, and which are explicitly out of scope / unverified.
- Give follow-up KEPs/issues a documented baseline instead of re-deriving this
  from code each time a new `provisioningClassName` or elastic-job interaction
  is proposed.

### Non-Goals
- Implementing support for any of the out-of-scope classes/scenarios listed
  below (TPU multi-host queued provisioning, non-GKE queued/reservation
  classes, elastic + MultiKueue, concurrent overlapping scale-ups).
- Changing the `ProvisioningRequest` admission-check controller's class-agnostic
  behavior — Kueue does not and should not branch on `provisioningClassName`.
- Re-designing the elastic-job / `WorkloadSlice` replacement mechanism itself.

## Proposal

### Per-pod vs. per-template annotation lifetime

Two `ProvisioningRequest` identity annotations are relevant:

- `autoscaling.x-k8s.io/consume-provisioning-request` — identifies *one specific*
  `ProvisioningRequest`. Changes every time a new PRQ is created, i.e. on every
  scale-up of an elastic Job.
- `autoscaling.x-k8s.io/provisioning-class-name` — stable for the life of the
  Workload/AdmissionCheck.

For a normal (non-elastic) Job, both annotations are written once onto the Job's
Pod template at admission — there is no lifetime conflict, since the Job (and its
template) is never re-admitted.

For an **elastic** Job, the Job/RayCluster template is long-lived and shared by
every pod the workload will ever have, but each scale-up mints a *new*
`ProvisioningRequest` with a *new* `consume-provisioning-request` value. Writing
that value onto the shared template would leak a stale PRQ identity onto every
subsequently created pod — including pods from future, unrelated scale-ups —
silently invalidating the nodeSelector-scoping guarantee.

The rule this KEP formalizes:

- **Template-level**: `consume-provisioning-request` must never persist on the
  elastic job's shared template. It is stripped on every metadata refresh/merge.
  `provisioning-class-name` may persist there, since it is stable across the
  workload's lifetime.
- **Per-pod**: each gated pod receives its correct, current
  `consume-provisioning-request` value filled in exactly once, only while absent.
  Once set on a pod, it is immutable — a pod is never ungated with a
  mismatched/stale PRQ identity relative to the update it's being reconciled
  against.

Reference implementation (as of [#14316](https://github.com/kubernetes-sigs/kueue/pull/14316)):
- `pkg/podset/podset.go`, `Merge()` — strips `consume-provisioning-request` from
  the elastic job template before merge.
- `pkg/controller/elasticjobs/elastic_job_ungater.go`,
  `refreshPodAdmission()` / `podAdmissionCompatible()` — fills the annotation
  per-pod only while absent, and refuses to overwrite a conflicting existing
  value.

### `provisioningClassName` scope

Kueue's `ProvisioningRequest` admission-check controller
(`pkg/controller/admissionchecks/provisioning`) is class-agnostic by design — it
never branches on the `provisioningClassName` string. Scope here is about which
classes have actually been exercised against the elastic-job code path (workload
slicing, mid-flight PodSet metadata refresh, per-pod annotation gating) and
reasoned about for the nodeSelector-safety property above.

**In scope — verified:**

| `provisioningClassName` | Mechanism | Verified how |
|---|---|---|
| `queued-provisioning.gke.io` | GKE flex-start w/ queued provisioning (DWS resize-request, atomic gang-provisioning) | Live end-to-end repro on GKE against real GPU capacity: confirmed `consume-provisioning-request` present per-pod, GKE's webhook injected the matching nodeSelector + `cloud.google.com/gke-queued` toleration honored. Elastic scale-up/scale-down exercised separately with `cpu-atomic-provisioning-check`. |
| `best-effort-atomic-scale-up.autoscaling.x-k8s.io` | Community cluster-autoscaler best-effort atomic scale-up | No nodeSelector-injection step for this class (capacity-check only, no per-PRQ node pinning) — the risk this KEP documents doesn't apply. Covered by unit/integration tests. |
| `check-capacity.autoscaling.x-k8s.io` | Community cluster-autoscaler capacity check (no provisioning side effect) | Same as above: check-only semantics, no node identity to leak. Covered by existing test suite. |

**Out of scope — not exercised, tracked as follow-up:**

| `provisioningClassName` / scenario | Why out of scope |
|---|---|
| TPU-flavored `queued-provisioning.gke.io` (multi-host slice gang scheduling) | The same annotation mechanism is expected to apply, but multi-host TPU slices add gang-scheduling-across-nodes semantics not yet run against an elastic PodSet. |
| Non-GKE queued/reservation classes | Whether and how a cloud's admission webhook injects a nodeSelector is implementation-specific per cloud/autoscaler; not verified for any non-GKE class. |
| Elastic Job + MultiKueue, combined with any `ProvisioningRequest` class | MultiKueue delays TAS/PRQ assignment to the worker cluster and follows a different flavor-assignment path; combining that with elastic re-slicing is untested. |
| Concurrent/overlapping scale-ups on the same elastic Workload | The current implementation tracks one scale-up in flight at a time via a previous-PodSet-counts annotation; a second scale-up arriving before the first is fully admitted is an explicit known limitation, not a verified-safe path. |

### Notes/Constraints/Caveats

- This KEP documents existing behavior introduced in
  [#14316](https://github.com/kubernetes-sigs/kueue/pull/14316); it does not
  introduce new API surface.
- The out-of-scope table above should be treated as a living list — closing one
  of those gaps should update this KEP rather than only the implementation PR.

### Risks and Mitigations

- **R**: A future change to `podset.go`/`elastic_job_ungater.go` could
  accidentally let `consume-provisioning-request` leak back onto the shared
  template. **M**: covered by existing unit tests asserting the annotation is
  stripped on merge; this KEP gives reviewers of future PRs the "why" to enforce
  that invariant explicitly in review.
- **R**: A new `provisioningClassName` (e.g. a different cloud's queued/reserved
  provisioning) is adopted by a user running elastic Jobs without verifying the
  nodeSelector-injection assumption holds. **M**: the out-of-scope table above
  should be checked/extended before recommending elastic Jobs with a new class.

## Design Details

No new API surface. This KEP is documentation of existing, shipped behavior and
its verified scope; see the `Proposal` section for the concrete code references.

### Test Plan

#### Unit Tests
- `pkg/podset`: `Merge` strips `consume-provisioning-request` from the elastic
  job template.
- `pkg/controller/elasticjobs`: `refreshPodAdmission` / `podAdmissionCompatible`
  fill the annotation per-pod once, and refuse to overwrite a conflicting value.

#### Integration Tests
- `test/integration/singlecluster/controller/admissionchecks/provisioning`:
  elastic Job scale-up while a `ProvisioningRequest` admission check is enabled;
  running Job's pod template stays immutable/stale-safe across a metadata
  refresh attempt.

## Alternatives

- **Branch admission-check behavior on `provisioningClassName`**: rejected —
  Kueue's provisioning admission-check controller is intentionally class-agnostic;
  adding class-specific branching would couple Kueue core to cloud-specific
  webhook behavior it doesn't control.
- **Persist `consume-provisioning-request` on the template and rely on the
  ungater to correct it before use**: rejected — this reintroduces a window
  where a newly created pod could briefly carry a stale identity before
  correction, which is exactly the risk this KEP documents avoiding.
