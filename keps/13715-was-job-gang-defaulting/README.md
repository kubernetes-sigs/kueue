# KEP-13715: Default Gang Scheduling for Kueue-managed Jobs

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
  - [Policy source](#policy-source)
  - [Notes and constraints](#notes-and-constraints)
  - [Risks and mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [Defaulting rules](#defaulting-rules)
  - [Escape hatch](#escape-hatch)
  - [Eligibility](#eligibility)
  - [Compatibility with Kueue mechanisms](#compatibility-with-kueue-mechanisms)
  - [Feature gate and API availability](#feature-gate-and-api-availability)
  - [Observability](#observability)
  - [Future extensions](#future-extensions)
  - [Open questions](#open-questions)
  - [Upstream dependencies](#upstream-dependencies)
- [Test Plan](#test-plan)
  - [Prerequisite testing updates](#prerequisite-testing-updates)
  - [Unit tests](#unit-tests)
  - [Integration tests](#integration-tests)
  - [e2e tests](#e2e-tests)
  - [Graduation Criteria](#graduation-criteria)
    - [Alpha](#alpha)
    - [Beta](#beta)
- [Upgrade, Downgrade, and Version Skew](#upgrade-downgrade-and-version-skew)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
<!-- /toc -->

## Summary

Kueue admits Workloads based on quota, but `kube-scheduler` still schedules the Pods of an admitted `batch/v1` Job independently. Workload-Aware Scheduling (WAS) lets a Job request gang scheduling through `spec.scheduling`, and an omitted scheduling policy means `Basic`, so Pods are scheduled independently.

This KEP adds an administrator-controlled default that sets `spec.scheduling.schedulingPolicy.gang: {}` on eligible Kueue-managed Jobs at creation time. A scheduling policy chosen by the user, including `basic: {}`, is never overwritten.

## Motivation

Quota admission does not confirm that all Pods of a Workload can be placed at the same time. Fragmentation, taints, topology, DRA, and non-Kueue Pods can prevent placement after quota has been reserved. A partially placed Job holds resources while making no progress; `waitForPodsReady` can evict and retry it after a timeout, but it cannot prevent the partial placement.

Gang scheduling closes that gap at the scheduler level. The upstream WAS and Job KEPs deliberately leave queueing and admission to systems such as Kueue, and the JobSet WAS KEP names the follow-up directly: "a follow-up Kueue design must define queueing and partial-eviction behavior" (`kubernetes-sigs/jobset#1253`). This KEP defines the Kueue-side behavior for `batch/v1` Jobs, allowing administrators to apply gang scheduling without requiring every user to update their manifests.

### Goals

- Add an alpha feature gate, default off, for administrator-controlled gang scheduling defaults.
- Default `spec.scheduling.schedulingPolicy.gang: {}` on eligible Kueue-managed `batch/v1` Jobs at creation time.
- Preserve every scheduling policy explicitly selected by the user.
- Define Job eligibility and the reason for excluding other Job shapes.
- Define the interaction with partial admission, workload slices, suspend and resume, `waitForPodsReady`, and MultiKueue.
- Define behavior and observability when the upstream API or feature gate is unavailable.
- Re-enable the WAS Job end-to-end test from #13533.

### Non-Goals

- Designing the upstream `spec.scheduling` API.
- Writing any WAS field other than `schedulingPolicy.gang`, in particular `minCount`, `disruptionMode`, `schedulingConstraints`, and DRA `resourceClaims`.
- Creating `scheduling.k8s.io` objects in Kueue, including `CompositePodGroup`. The Job controller is the root controller and compiler for standalone Jobs. Creating these objects for plain Pods is covered by [KEP-12385](/keps/12385-was-podgroups), and consuming user-provided ones by [KEP-13150](/keps/13150-bring-your-own-podgroup).
- Defining JobSet queueing or partial-eviction behavior. The JobSet WAS KEP places the Kueue-side integration in #13707.
- Defining how `disruptionMode` interacts with Kueue preemption, eviction, and `WorkloadPriorityClass`. That belongs to a separate KEP, as proposed by @kannon92 in #13533.
- Changing the `kueue.x-k8s.io` `Workload` API.
- Extending defaulting to JobSet, RayJob, TrainJob, LWS, or other integrations during alpha.

## Proposal

The existing `batch/v1` Job mutating webhook additionally sets:

```yaml
spec:
  scheduling:
    schedulingPolicy:
      gang: {}
```

The mutation applies only when the feature is enabled, the administrator has configured the `batch/job` policy as `Gang`, and the Job satisfies the [Defaulting rules](#defaulting-rules).

Kueue and the upstream scheduling API both define a `Workload`. This document writes `kueue.Workload` for the Kueue queueing unit and `scheduling.Workload` for the upstream object the Job controller compiles.

### User Stories

- As a platform administrator of a shared GPU cluster, I enable the feature once instead of asking every team to add `spec.scheduling` to its manifests.
- As an ML engineer, I want an all-at-once Job to start only when all its Pods can be placed, without learning the WAS API. For a Job that should not use gang scheduling, I set `schedulingPolicy.basic: {}`.
- As an existing user, I see no change unless an administrator enables the feature.

### Policy source

The mutating webhook is the execution point; the Kueue Configuration API is the policy source. A provisional configuration shape is:

```yaml
apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
gangSchedulingDefaults:
- framework: "batch/job"
  policy: Gang         # Gang | None, default None
  appliesTo: FixedSize # FixedSize | All
```

Keying the configuration by framework allows future integrations to add their own policy without changing the `batch/job` entry. The final field names must be settled before the KEP becomes implementable ([OQ4](#open-questions)).

### Notes and constraints

- **Creation only.** The webhook mutates CREATE requests, and `spec.scheduling` is immutable except for `gang.minCount`. Existing Jobs cannot be backfilled.
- **Kueue does not write `minCount`.** The Job controller resolves it from `parallelism`. Writing it would couple partial admission to upstream validation and would make a Kueue-set value indistinguishable from a user-set one.
- **API support must be verified before mutating.** A cluster that does not honor the field discards it silently at write time, and it cannot be restored on that Job afterwards. See [Feature gate and API availability](#feature-gate-and-api-availability).

### Risks and mitigations

**Changing scheduling semantics without a per-Job request.** The feature is disabled by default and requires explicit administrator configuration; an explicit user policy is never overwritten, and `basic: {}` is an object-level opt-out. This follows the existing defaulting pattern from [KEP-10765](/keps/10765-workload-priority-class-defaulting), a gated creation-time mutation that preserves explicit user values, which graduated to beta in v0.20.

**Silent loss of the field.** A create request can succeed while `spec.scheduling` is discarded, with no error, warning, or condition. Kueue must not default unless it can verify in advance that the cluster preserves the field; see [Feature gate and API availability](#feature-gate-and-api-availability).

**Replacement Pods can wait for the full gang.** Upstream evaluates gang satisfaction over the lifetime of the group, and the alternatives are still under discussion (`kubernetes/kubernetes#136334`). Eligibility is therefore restricted to Jobs meant to run all Pods concurrently.

**Independent preemption systems.** Kube-scheduler workload-aware preemption and Kueue eviction use different priority and accounting models. This difference exists today. Kueue injects no `disruptionMode`, and the compiled `PodGroup` carries the upstream default of `single`, so defaulting adds no group-level disruption behavior.

**Admission can succeed when placement cannot.** A Workload can reserve quota and remain unplaceable, then cycle through `waitForPodsReady`, eviction, and requeue; existing backoff applies. Gang scheduling narrows the consequence, since an unplaceable Job stays fully Pending instead of holding a subset of the nodes, but it does not make admission placement-aware. That is separate work, tracked in #13149.

## Design Details

### Defaulting rules

On a Job CREATE request, Kueue injects `schedulingPolicy.gang: {}` only when all of the following hold:

1. `GangSchedulingByDefault` is enabled.
2. The configured policy for `batch/job` is `Gang`.
3. The Job is managed by Kueue.
4. The Job has no Kueue-managed ancestor.
5. The Job has not selected a scheduling policy, meaning `spec.scheduling` is unset or its `schedulingPolicy` is nil.
6. The Job is eligible.
7. Kueue can verify that the target cluster will preserve the field.

Rule 3 reuses the Job integration's existing managed-job decision, including `manageJobsWithoutQueueName` and `managedJobsNamespaceSelector` from [KEP-3589](/keps/3589-manage-jobs-selectively), and runs after LocalQueue defaulting. Rule 4 reuses the existing Kueue-managed-ancestor check, preventing child Jobs from receiving a second scheduling intent when their root controller owns compilation.

The mutation is idempotent and writes no other WAS fields.

### Escape hatch

An explicit `schedulingPolicy` disables defaulting for that Job, and `schedulingPolicy.basic: {}` is the documented opt-out. Rule 5 keys on the policy rather than on the presence of `spec.scheduling`, so a Job that sets only `schedulingConstraints` can still receive the administrator's default. The stricter reading is [OQ7](#open-questions).

### Eligibility

The default applies to Jobs whose Pods are meant to run concurrently as one unit:

- `parallelism > 1`
- `completions` is set
- `completions == parallelism`

`completions == parallelism` is a safety condition rather than a heuristic. Upstream derives the gang size from `parallelism`, while the Job controller never runs more than `completions` Pods at once, so a Job with `completions < parallelism` compiles to a gang that can never be satisfied: it never starts and never fails. Because that failure is silent, `appliesTo: All` does not waive this condition; it waives only `parallelism > 1`. Kueue skips defaulting for such a Job under either setting and reports the reason.

Eligibility does not depend on `completionMode`; Indexed and NonIndexed Jobs of the same shape behave identically.

### Compatibility with Kueue mechanisms

| Interaction | Behavior |
|---|---|
| Partial admission, Kueue-injected gang | Supported. Kueue leaves `minCount` unset, so the gang follows the admitted `parallelism`. |
| Partial admission, user-set `minCount` | Excluded in alpha. `parallelism` itself is mutable, but lowering it alone is rejected because the resulting state would have `minCount > parallelism`; lowering both in one request is validated against the final state and accepted. Whether beta adds that atomic update is [OQ1](#open-questions). |
| Workload slices | Excluded in alpha, following the existing exclusion between partial admission and elastic Jobs. Upstream does rescale a gang in place; what is unverified is the Kueue-side slice semantics ([OQ5](#open-questions)). |
| Suspend, eviction, requeue | Upstream dependent. Suspending a Job does not currently delete the compiled objects; beta is expected to delete and recreate them. |
| `waitForPodsReady` | The scheduler keeps the group Pending while Kueue can still evict it after the timeout ([OQ3](#open-questions)). |
| JobSet | Child Jobs of a Kueue-managed JobSet are excluded; ReplicatedJobs admitted independently through the Job integration remain eligible. This KEP does not select a JobSet queueing model. |
| Kueue TAS | Not combined. This KEP writes no `schedulingConstraints`. |
| MultiKueue | Every worker cluster must preserve the field; version and gate skew must be reported. |
| Preemption and eviction | Out of scope. This KEP writes no `disruptionMode`. |
| `ProvisioningRequest` | Open. The overlap between provisioning and gang placement needs an explicit policy. |
| BYO PodGroup ([KEP-13150](/keps/13150-bring-your-own-podgroup)) | `gang.minCount` is a floor, not the group size, so deriving `PodSet.Count` from it under-reserves quota by up to `parallelism - minCount`. A Kueue-injected gang is unaffected, because Kueue writes no `minCount` and the floor then equals `parallelism` ([OQ2](#open-questions)). |
| DRA `resourceClaims` | Out of scope. This KEP writes none. |

### Feature gate and API availability

`GangSchedulingByDefault` is alpha and disabled by default.

The implementation must verify that the target cluster preserves `batch/v1 Job.spec.scheduling` before defaulting. REST mapping and OpenAPI are insufficient, because the Job resource and the field schema remain visible even when the field is not preserved. The presence of the `scheduling.k8s.io` API group is also only a proxy, because it is governed by `GenericWorkload` rather than by `WorkloadWithJob`, which is the gate that decides whether the Job field survives. Kueue therefore needs a preflight mechanism, such as a dry-run create that carries the field followed by inspection of the returned object. Selecting it is [OQ6](#open-questions) and blocks implementation, because rule 7 depends on it.

Independently, a controller-side comparison after creation should report unexpected loss of the field, including on a MultiKueue worker. That is defensive reporting, not a substitute: once the field is dropped it cannot be restored on that Job.

### Observability

The Job shows that gang scheduling was requested but not the resolved `minCount`; the compiled `PodGroup` is the source of truth for the effective group size. The implementation should surface successful defaulting, defaulting skipped for an unsupported cluster, and loss of the field on a MultiKueue worker. The specific observability mechanism is deferred until the KEP becomes implementable.

### Future extensions

Alpha supports only `batch/v1` Job. The read direction can be shared through `jobframework`, as KEP-13150 does, but the write direction is framework-specific: Job, JobSet, LWS, KubeRay, and TrainJob express scheduling intent through different fields and API versions. A shared write abstraction should wait until those interfaces stabilize.

### Open questions

| # | Question | Leaning |
|---|---|---|
| OQ1 | Should partial admission later support an explicit `gang.minCount` by updating it together with `parallelism`? | Beta, not alpha. The apiserver accepts the atomic update, but the published documentation does not describe that path. |
| OQ2 | With KEP-13150 enabled, which source controls `PodSet.Count`? | The Job spec. A Kueue-injected gang does not change the Job's represented Pod count; an explicit `minCount` below `parallelism` is the exceptional case. |
| OQ3 | Do gang waiting and `waitForPodsReady.timeout` need coordination? | Probably not a hard constraint, but Kueue should distinguish an unplaceable gang from ordinary startup delay. |
| OQ4 | What is the final Configuration API shape? | Settle before implementable. |
| OQ5 | Should defaulting be skipped when workload slices are enabled? | Yes for alpha, because the Kueue-side slice semantics are unverified. |
| OQ6 | How can Kueue reliably detect that the apiserver preserves `spec.scheduling`? | Not from REST mapping or OpenAPI. Blocks implementation together with rule 7. |
| OQ7 | Does `spec.scheduling` with a nil `schedulingPolicy` count as user intent? | No. Blocks implementation, since it fixes rule 5, the escape hatch, and the webhook tests together. |

### Upstream dependencies

The design relies on the following upstream behavior, documented in the Kubernetes Job documentation for `WorkloadWithJob` and in [KEP-5547](https://github.com/kubernetes/enhancements/issues/5547):

- An omitted `spec.scheduling` means `Basic`, and an omitted `gang.minCount` defaults to `.spec.parallelism`.
- Every `spec.scheduling` field is immutable after creation except `schedulingPolicy.gang.minCount`, and a `minCount` greater than `parallelism` is rejected.
- A cluster with `WorkloadWithJob` disabled discards `spec.scheduling` at write time without an error, and the field cannot be added to that Job later.
- `gang.minCount` is a floor rather than the group size: the number of Pods placed ranges from `minCount` to `parallelism` with available capacity.

These assumptions were verified against Kubernetes `main` and must be revalidated before implementation. Kueue's current `k8s.io/api` dependency does not yet expose the required APIs.

## Test Plan

[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.

### Prerequisite testing updates

End-to-end coverage requires a Kubernetes version that serves `batch/v1 spec.scheduling`, which no released version does yet. Until one exists, the tests run in the WAS lane built from Kubernetes `main`.

### Unit tests

Table-driven Job webhook tests cover the gate disabled, the configured policy `None`, an explicit `gang`, an explicit `basic`, `spec.scheduling` with a nil policy, ineligible Job shapes, a child Job with a Kueue-managed ancestor, a Job not managed by Kueue, idempotent reinvocation, and a target cluster that does not preserve the field, covering both an absent API and an API that is served while `WorkloadWithJob` is disabled.

### Integration tests

Using an `envtest` that serves the field with `WorkloadWithJob` enabled: defaulting enabled and disabled, explicit opt-out, child Job exclusion, unsupported API behavior, and observability for applied and skipped defaulting.

### e2e tests

- an eligible Kueue-managed Job produces a `PodGroup` whose gang minimum matches the `kueue.Workload` `PodSet` count;
- a Job with `basic: {}` keeps basic scheduling. A `Basic` Job is still compiled into a `Workload` and a `PodGroup`, so the assertion is on the compiled policy, not on the absence of a `PodGroup`;
- an unplaceable gang Job stays fully Pending instead of placing a subset of its Pods;
- the disabled test from #13533 is re-enabled.

### Graduation Criteria

#### Alpha

- OQ6 and OQ7 are settled before implementation starts.
- `GangSchedulingByDefault` is implemented behind a default-off feature gate, with the policy controlled through Kueue configuration.
- Defaulting, opt-out, eligibility, and child Job exclusion have unit, integration, and end-to-end coverage.
- An unsupported cluster is handled safely and observably: Kueue does not default when the preflight check fails, and reports why.
- Partial admission is not performed for Jobs carrying an explicit `gang.minCount`.
- The WAS Job test from #13533 is re-enabled.

#### Beta

- The upstream Job integration has stable suspend and resume semantics for Kueue eviction and requeue.
- OQ1, OQ2, and OQ3 are resolved and implemented.
- Observability is finalized.
- MultiKueue behavior is defined and tested.

## Upgrade, Downgrade, and Version Skew

- Enabling the feature affects only Jobs created afterwards; existing Jobs cannot be backfilled.
- Disabling it does not modify existing Jobs. Jobs that already carry the gang policy keep it, and the field cannot be cleared.
- Each target cluster must preserve the field, checked independently for MultiKueue workers. A cluster that silently drops it is unsupported and must be reported; affected Jobs have to be recreated once the cluster supports the field.

## Implementation History

- 2026-08-09: Provisional KEP opened.

## Drawbacks

Kueue changes the scheduling policy of a Job without a per-Job request. The feature gate, administrator configuration, eligibility rules, and object-level opt-out reduce that risk without removing it. The implementation also depends on an upstream field whose availability varies across clusters, which adds version and gate checks to both single-cluster and MultiKueue operation.

## Alternatives

**Feature gate without configuration.** Simpler, but it offers no administrator policy beyond enabling or disabling the behavior for every eligible Job.

**Policy on LocalQueue or ClusterQueue.** Useful per-queue control and possibly a better long-term model, but it requires a Kueue API change and is deferred from alpha.

**Per-Job annotation.** Still requires users to opt in on every Job, and duplicates the purpose of `spec.scheduling`.

**Documentation only.** No implementation risk, but no administrator-controlled default either.

**Kueue creates the WAS objects.** Rejected for Jobs because the Job controller is the root controller and already owns compilation and lifecycle. Kueue should express policy through the Job API rather than compete for the compiled objects; KEP-12385 draws the same line in its Non-Goals.

**Immediate `jobframework` write abstraction.** Premature while the integration APIs still differ and change.
