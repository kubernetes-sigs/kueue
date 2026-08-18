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

Kueue admits Workloads based on quota, but `kube-scheduler` still schedules the Pods of an admitted `batch/v1` Job independently.
Workload-Aware Scheduling (WAS) lets a Job request gang scheduling through `spec.scheduling`, and an omitted scheduling policy means `Basic`, so Pods are scheduled independently.

This KEP adds an administrator-controlled default that sets `spec.scheduling.schedulingPolicy.gang: {}` and `spec.scheduling.disruptionMode.all: {}` on eligible Kueue-managed Jobs at creation time.
A Job that sets `spec.scheduling` itself is never modified.

## Motivation

Quota admission does not confirm that all Pods of a Workload can be placed at the same time.
Fragmentation, taints, topology, DRA, and non-Kueue Pods can prevent placement after quota has been reserved.
A partially placed Job holds resources while making no progress; `waitForPodsReady` can evict and retry it after a timeout, but it cannot prevent the partial placement.

Gang scheduling closes that gap at the scheduler level.
The upstream WAS and Job KEPs deliberately leave queueing and admission to systems such as Kueue, and the JobSet WAS KEP names the follow-up directly: "a follow-up Kueue design must define queueing and partial-eviction behavior" ([kubernetes-sigs/jobset#1253](https://github.com/kubernetes-sigs/jobset/issues/1253)).
This KEP defines the Kueue-side behavior for `batch/v1` Jobs, allowing administrators to apply gang scheduling without requiring every user to update their manifests.

### Goals

- Add an alpha feature gate, default off, for administrator-controlled gang scheduling defaults.
- Default `spec.scheduling.schedulingPolicy.gang: {}` and `spec.scheduling.disruptionMode.all: {}` on eligible Kueue-managed `batch/v1` Jobs at creation time.
- Leave every Job that sets `spec.scheduling` itself untouched.
- Define Job eligibility and the reason for excluding other Job shapes.
- Define the interaction with partial admission, workload slices, suspend and resume, `waitForPodsReady`, and MultiKueue.
- Define behavior and observability when the upstream API or feature gate is unavailable.
- Re-enable the WAS Job end-to-end test from #13533.

### Non-Goals

- Designing the upstream `spec.scheduling` API.
- Writing any WAS field other than `schedulingPolicy.gang` and `disruptionMode.all`, in particular `minCount`, `schedulingConstraints`, and DRA `resourceClaims`.
- Acting on the contents of a user-set `spec.scheduling`, beyond the two decisions alpha already takes from it: skipping defaulting, and skipping partial admission for a Job that carries `gang.minCount`.
  Alpha never writes into a field the user set; interpreting the rest of it is a follow-up ([Future extensions](#future-extensions)).
- Creating `scheduling.k8s.io` objects in Kueue, including `CompositePodGroup`.
  The Job controller is the root controller and compiler for standalone Jobs.
  Creating these objects for plain Pods is covered by [KEP-12385](/keps/12385-was-podgroups), and consuming user-provided ones by [KEP-13150](/keps/13150-bring-your-own-podgroup).
- Defining JobSet queueing or partial-eviction behavior.
  The JobSet WAS KEP places the Kueue-side integration in #13707.
- Defining how group-level disruption interacts with Kueue preemption, eviction, and `WorkloadPriorityClass`.
  This KEP sets the disruption granularity; which system decides, and on whose priority, belongs to a separate KEP, as proposed by @kannon92 in #13533.
- Changing the `kueue.x-k8s.io` `Workload` API.
- Extending defaulting to JobSet, RayJob, TrainJob, LWS, or other integrations during alpha.

## Proposal

The existing `batch/v1` Job mutating webhook additionally sets:

```yaml
spec:
  scheduling:
    schedulingPolicy:
      gang: {}
    disruptionMode:
      all: {}
```

The mutation applies only when the feature is enabled and the Job satisfies the [Defaulting rules](#defaulting-rules).

Kueue and the upstream scheduling API both define a `Workload`.
This document writes `kueue.Workload` for the Kueue queueing unit and `scheduling.Workload` for the upstream object the Job controller compiles.

### User Stories

- As a platform administrator of a shared GPU cluster, I enable the feature once instead of asking every team to add `spec.scheduling` to its manifests.
- As an ML engineer, I want an all-at-once Job to start only when all its Pods can be placed, without learning the WAS API.
  For a Job that should not use gang scheduling, I set `schedulingPolicy.basic: {}`.
- As an existing user, I see no change unless an administrator enables the feature.

### Policy source

The feature gate is the policy source and the mutating webhook is the execution point.
`GangSchedulingByDefault` applies the default to every eligible Kueue-managed Job in the cluster; alpha adds no Kueue configuration field.
An administrator who wants gang scheduling for only part of the cluster can scope which Jobs Kueue manages at all, through `managedJobsNamespaceSelector`.

The alternative, a per-framework policy in the Kueue Configuration API, is recorded under [Alternatives](#alternatives).
What carries the administrator's intent once the gate graduates is [OQ4](#open-questions).

### Notes and constraints

- **Creation only.**
  The webhook mutates CREATE requests, and `spec.scheduling` is immutable except for `gang.minCount`.
  Existing Jobs cannot be backfilled.
- **Kueue does not write `minCount`.**
  The Job controller resolves it from `parallelism`.
  Writing it would couple partial admission to upstream validation and would make a Kueue-set value indistinguishable from a user-set one.
- **API support must be verified before mutating.**
  A cluster that does not honor the field discards it silently at write time, and it cannot be restored on that Job afterwards.
  See [Feature gate and API availability](#feature-gate-and-api-availability).

### Risks and mitigations

**Changing scheduling semantics without a per-Job request.**
The feature is disabled by default and requires an administrator to enable the gate; a Job that sets `spec.scheduling` is never modified, and `basic: {}` is the documented object-level opt-out.
This follows the existing defaulting pattern from [KEP-10765](/keps/10765-workload-priority-class-defaulting), a gated creation-time mutation that preserves explicit user values, which graduated to beta in v0.20.
The comparison is not exact: that feature also requires a `WorkloadPriorityClass` named `default` to exist, so its gate alone changes nothing, while here the gate is the whole switch ([OQ4](#open-questions)).

**Silent loss of the field.**
A create request can succeed while `spec.scheduling` is discarded, with no error, warning, or condition.
Kueue must not default unless it can verify in advance that the cluster preserves the field; see [Feature gate and API availability](#feature-gate-and-api-availability).

**Replacement Pods can wait for the full gang.**
Upstream evaluates gang satisfaction over the lifetime of the group, and the alternatives are still under discussion (`kubernetes/kubernetes#136334`).
Eligibility is therefore restricted to Jobs meant to run all Pods concurrently.

**Independent preemption systems.**
Kube-scheduler workload-aware preemption and Kueue eviction use different priority and accounting models, and that difference exists today.
Defaulting `disruptionMode.all` changes the granularity on the scheduler side: the compiled `PodGroup` is disrupted as a unit rather than Pod by Pod.
That direction narrows a mismatch rather than adding one, because Kueue already evicts at Job granularity, suspending the Job so that all of its Pods are deleted; `single` is the setting under which the two systems disagree about what a unit is.
What the default does not resolve is which system decides and on whose priority, since a gang admitted by Kueue can still be preempted by the scheduler under a priority that Kueue's accounting never saw.

**Admission can succeed when placement cannot.**
A Workload can reserve quota and remain unplaceable, then cycle through `waitForPodsReady`, eviction, and requeue; existing backoff applies.
Gang scheduling narrows the consequence, since an unplaceable Job stays fully Pending instead of holding a subset of the nodes, but it does not make admission placement-aware.
That is separate work, tracked in #13149.

## Design Details

### Defaulting rules

On a Job CREATE request, Kueue injects `schedulingPolicy.gang: {}` and `disruptionMode.all: {}` only when all of the following hold:

1. `GangSchedulingByDefault` is enabled.
2. The Job is managed by Kueue.
3. The Job has no Kueue-managed ancestor.
4. The Job expresses no scheduling intent of its own, meaning `spec.scheduling` is unset and its Pod template does not set `schedulingGroup`.
5. The Job is eligible.
6. Kueue can verify that the target cluster will preserve the field.

Rule 2 reuses the Job integration's existing managed-job decision, including `manageJobsWithoutQueueName` and `managedJobsNamespaceSelector` from [KEP-3589](/keps/3589-manage-jobs-selectively), and runs after LocalQueue defaulting.
Rule 3 reuses the existing Kueue-managed-ancestor check, preventing child Jobs from receiving a second scheduling intent when their root controller owns compilation.
A Pod template that sets `schedulingGroup` counts as user intent for the same reason: upstream treats it as a bring-your-own `PodGroup` and the Job controller compiles nothing for such a Job, so an injected policy would never take effect.
Consuming user-provided `PodGroup`s belongs to [KEP-13150](/keps/13150-bring-your-own-podgroup).

Rule 4 keys on the presence of `spec.scheduling`, not on the scheduling policy inside it.
Presence means presence in the admission request, which is the only version of the object a webhook sees; on a cluster that does not preserve the field, the request carries it and the stored Job does not.
A Job that sets any part of the field owns all of it, so Kueue writes the two fields together or not at all.
What Kueue should do with the contents of a user-set `spec.scheduling`, for example a `gang.minCount` that differs from `parallelism` or `schedulingConstraints` on their own, is deliberately left to the follow-up in [Future extensions](#future-extensions).

The presence test also keeps Kueue out of upstream admission decisions.
A Job that sets `disruptionMode.all` and no policy is rejected today: Job validation resolves an unset policy to `Basic`, through a default configuration it shares with the Job controller so that the two cannot drift, and rejects "the disruptionMode `all` is not supported with the Basic scheduling policy".
A rule keyed on the policy alone would inject a gang into exactly that Job and make the same manifest admissible, so whether the API server accepts an object would depend on Kueue-side state.

The mutation is idempotent and writes no WAS fields beyond these two.

### Escape hatch

Setting `spec.scheduling` disables defaulting for that Job, and `schedulingPolicy.basic: {}` is the documented opt-out because it is also how a user asks for basic scheduling explicitly.
Opting out disables both injected fields, since Kueue writes `disruptionMode` only alongside a gang policy that it selected itself.

### Eligibility

The default applies to Jobs whose Pods are meant to run concurrently as one unit:

- `parallelism > 1`
- `completions` is set
- `completions == parallelism`

`completions == parallelism` is a safety condition rather than a heuristic.
Upstream derives the gang size from `parallelism`, while the Job controller never runs more than `completions` Pods at once, so a Job with `completions < parallelism` compiles to a gang that can never be satisfied: it never starts, and nothing fails unless something external, such as `activeDeadlineSeconds`, deletion, or Kueue eviction after `waitForPodsReady`, terminates the Job.
Because that failure is silent, the condition is not waivable.
Kueue skips defaulting for such a Job and reports the reason.

Eligibility does not depend on `completionMode`; Indexed and NonIndexed Jobs of the same shape behave identically.

### Compatibility with Kueue mechanisms

| Interaction | Behavior |
|---|---|
| Partial admission, Kueue-injected gang | Supported. Kueue leaves `minCount` unset, so the gang follows the admitted `parallelism`. |
| Partial admission, user-set `minCount` | Excluded in alpha. Rule 4 never defaults such a Job, and Kueue does not reduce its `parallelism` either, so partial admission never meets a gang Kueue did not write. `parallelism` itself is mutable, but lowering it alone is rejected because the resulting state would have `minCount > parallelism`; lowering both in one request is validated against the final state and accepted. Whether beta adds that atomic update is [OQ1](#open-questions). |
| Workload slices | Excluded in alpha, following the existing exclusion between partial admission and elastic Jobs. Upstream does rescale a gang in place; what is unverified is the Kueue-side slice semantics ([OQ5](#open-questions)). |
| Suspend, eviction, requeue | The Kueue side is unchanged: eviction suspends the Job, its Pods are deleted, the `kueue.Workload` is requeued, and re-admission resumes the Job; the injected policy is immutable and survives the cycle. What is upstream dependent is the compiled-object lifecycle: suspending a Job does not currently delete the compiled objects, and beta is expected to delete and recreate them. |
| `waitForPodsReady` | The scheduler keeps the group Pending while Kueue can still evict it after the timeout ([OQ3](#open-questions)). |
| JobSet | Child Jobs of a Kueue-managed JobSet are excluded by rule 3. The only child Jobs that remain eligible are those whose parent JobSet is not managed by Kueue and that are admitted individually through the Job integration. This KEP does not select a JobSet queueing model. |
| Kueue TAS | Not combined. This KEP writes no `schedulingConstraints`. |
| MultiKueue | Every worker cluster must preserve the field; version and gate skew must be reported. |
| Preemption and eviction | The injected `disruptionMode.all` makes the compiled `PodGroup` one disruption unit on the scheduler side, which matches Kueue's Job-granularity eviction. Which system decides, and on whose priority, is out of scope for this KEP. |
| `ProvisioningRequest` | No interaction defined in alpha. Provisioning is unaware of WAS, and what it writes back are Pod template updates rather than WAS fields, so the two compose as they are. What alpha does not analyze is timing: the booking is not retried once the Workload is admitted, so a gang that cannot be placed can outlive it, which is the same window as [OQ3](#open-questions). |
| CronJob | Each scheduled Job is defaulted on its own, since the fields can only be set at creation. A CronJob-created Job has no Kueue-managed ancestor, so it receives the default whenever Kueue manages it and its shape qualifies. |
| BYO PodGroup ([KEP-13150](/keps/13150-bring-your-own-podgroup)) | `gang.minCount` is a floor, not the group size, so deriving `PodSet.Count` from it under-reserves quota by up to `parallelism - minCount`. A Kueue-injected gang is unaffected, because Kueue writes no `minCount` and the floor then equals `parallelism` ([OQ2](#open-questions)). |
| DRA `resourceClaims` | Out of scope. This KEP writes none. |

### Feature gate and API availability

`GangSchedulingByDefault` is alpha and disabled by default.

The implementation must verify that the target cluster preserves `batch/v1 Job.spec.scheduling` before defaulting.
REST mapping and OpenAPI are insufficient, because the Job resource and the field schema remain visible even when the field is not preserved.
The presence of the `scheduling.k8s.io` API group is also only a proxy, because it is governed by `GenericWorkload` rather than by `WorkloadWithJob`, which is the gate that decides whether the Job field survives.
The check therefore targets the observable itself rather than any particular gate: every state that loses the field, whether the cluster predates it or the API server runs with `WorkloadWithJob` disabled, collapses into the same symptom, the field is not preserved, and the same behavior, skipping the mutation and reporting the reason.
Kueue therefore needs a preflight mechanism, such as a dry-run create that carries the field followed by inspection of the returned object.
Whatever mechanism is selected must run outside the admission path, since a dry-run create passes through Kueue's own mutating webhook and the probe object must be excluded from defaulting, and its result must be cached rather than re-verified on every request.
The cached result is keyed per target cluster, including each MultiKueue worker; observed field loss or a change in cluster version or gate posture invalidates it, and an unverified state fails closed.
Selecting the mechanism, including the rest of the cache contract, is [OQ6](#open-questions) and blocks implementation, because rule 6 depends on it.

Independently, a controller-side comparison after creation should report unexpected loss of the field, including on a MultiKueue worker.
That is defensive reporting, not a substitute: once the field is dropped it cannot be restored on that Job.

The preflight covers the API-server side of the contract only, because `WorkloadWithJob` takes effect per component: the API-server gate decides whether the field is preserved, while the controller-manager gate decides whether the Job controller compiles the `scheduling.Workload` and `PodGroup`.
A cluster with the API-server gate enabled and the controller-manager gate disabled preserves the field but never compiles it, so the preservation probe passes while the injected policy stays inert.
No API-level probe can detect that skew in advance; the controller-side check above is the detection point and should also report a defaulted gang Job whose compiled objects never appear.

### Observability

The Job shows that gang scheduling was requested but not the resolved `minCount`; the compiled `PodGroup` is the source of truth for the effective group size.
The implementation should surface successful defaulting, defaulting skipped for an unsupported cluster, and loss of the field on a MultiKueue worker.
The specific observability mechanism is deferred until the KEP becomes implementable.

### Future extensions

Alpha supports only `batch/v1` Job.
The read direction can be shared through `jobframework`, as KEP-13150 does, but the write direction is framework-specific: Job, JobSet, LWS, KubeRay, and TrainJob express scheduling intent through different fields and API versions.
A shared write abstraction should wait until those interfaces stabilize.

Alpha reads a user-set `spec.scheduling` only to decide whether to default and whether partial admission applies, and never writes into one.
A follow-up should define what Kueue does with a user-set field, and the cases are already visible.
A `gang.minCount` below `parallelism` stays with this KEP as [OQ1](#open-questions) and [OQ2](#open-questions), because the mechanism it maps onto, the partial-admission floor `Workload.spec.podSets[].minCount`, is Kueue's own.
The other two, `schedulingConstraints` next to Kueue TAS and a user-set `disruptionMode` next to Kueue eviction, belong to the follow-up.
That work depends on upstream settling what a partially specified `spec.scheduling` means, in particular `schedulingConstraints` with no policy and no disruption mode.

### Open questions

| # | Question | Leaning |
|---|---|---|
| OQ1 | Should partial admission later support an explicit `gang.minCount` by updating it together with `parallelism`? | Beta, not alpha. The apiserver accepts the atomic update, but the published documentation does not describe that path. |
| OQ2 | With KEP-13150 enabled, which source controls `PodSet.Count`? | The Job spec. `minCount` is a floor rather than the group size, so sizing the PodSet from it under-reserves quota; a Kueue-injected gang does not change the Job's represented Pod count. The opposite direction, mapping a user-set `minCount` to the partial-admission floor `Workload.spec.podSets[].minCount`, is the OQ1 path and shares its atomic-update constraint. |
| OQ3 | Do gang waiting and `waitForPodsReady.timeout` need coordination? | Probably not a hard constraint, but Kueue should distinguish an unplaceable gang from ordinary startup delay. |
| OQ4 | What carries the administrator's cluster-level choice once `GangSchedulingByDefault` graduates? | Open. Alpha needs nothing, since the gate is default-off and enabling it is itself the administrator's choice. Once the gate defaults on, the behavior becomes cluster-wide with only a per-Job opt-out. `WorkloadPriorityClass` defaulting has a second switch, the presence of a `WorkloadPriorityClass` named `default`, and gang has no object whose presence can carry the same intent. |
| OQ5 | Should defaulting be skipped when workload slices are enabled? | Yes for alpha, because the Kueue-side slice semantics are unverified. |
| OQ6 | How can Kueue reliably detect that the apiserver preserves `spec.scheduling`? | Not from REST mapping or OpenAPI. Blocks implementation together with rule 6. |

### Upstream dependencies

The design relies on the following upstream behavior, documented in the Kubernetes Job documentation for `WorkloadWithJob` and in [KEP-5547](https://github.com/kubernetes/enhancements/issues/5547):

- An omitted `spec.scheduling` means `Basic`, and an omitted `gang.minCount` defaults to `.spec.parallelism`.
  That resolution happens during validation and compilation rather than being persisted, so a Job whose author left the field alone does not acquire it; rule 4 depends on this.
- Every `spec.scheduling` field is immutable after creation except `schedulingPolicy.gang.minCount`, and a `minCount` greater than `parallelism` is rejected.
  Immutability covers whether a field is set at all, so neither the policy nor `disruptionMode` can be added to an existing Job.
- The `Basic` policy and `disruptionMode.all` are rejected together, and for a Job an unset policy resolves to `Basic`.
- A cluster with `WorkloadWithJob` disabled discards `spec.scheduling` at write time without an error, and the field cannot be added to that Job later.
- `gang.minCount` is a floor rather than the group size: the number of Pods placed ranges from `minCount` to `parallelism` with available capacity.
- The Job controller compiles the `scheduling.Workload` and `PodGroup` as soon as the Job exists, including while it is suspended, and recompiles `gang.minCount` when `parallelism` changes.

These assumptions were verified against `v1.37.0-rc.0` on a cluster with both gates enabled, and must be revalidated before implementation.
Kueue cannot depend on that version yet: `sigs.k8s.io/scheduler-library` has a single published version, `v0.1.0-alpha1`, whose `go.mod` pins `k8s.io/kubernetes v1.36.0`.

## Test Plan

[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.

### Prerequisite testing updates

End-to-end coverage requires a Kubernetes version that serves `batch/v1 spec.scheduling`.
The field first appears in `v1.37.0-rc.0`; until 1.37 is released and Kueue can depend on it, the tests run in the WAS lane built from Kubernetes `main`.

### Unit tests

Table-driven Job webhook tests cover the gate disabled, ineligible Job shapes, a child Job with a Kueue-managed ancestor, a Job not managed by Kueue, idempotent reinvocation, and a target cluster that does not preserve the field, covering both an absent API and a schema that is served while the API server drops the field.
Rule 4 is covered by every shape of a user-set `spec.scheduling`, each expecting no mutation: an explicit `gang`, an explicit `basic`, a `disruptionMode` alone, `disruptionMode.all` with no policy, `schedulingConstraints` alone, and a nil `schedulingPolicy`.

### Integration tests

Using an `envtest` that serves the field with `WorkloadWithJob` enabled: defaulting enabled and disabled, explicit opt-out, child Job exclusion, unsupported API behavior, eviction of a defaulted Job followed by resume with the injected policy unchanged, and observability for applied defaulting, skipped defaulting, and a preserved but never-compiled policy.

### e2e tests

- an eligible Kueue-managed Job produces a `PodGroup` whose gang minimum matches the `kueue.Workload` `PodSet` count and whose disruption mode is `all`.
  The assertion can run before the Job is resumed, since compilation does not wait for admission;
- a partially admitted Job converges: the reduced `parallelism`, the `kueue.Workload` `PodSet` count, and the compiled `PodGroup` gang minimum all reflect the admitted count, while `completions` is unchanged;
- a Job with `basic: {}` keeps basic scheduling.
  A `Basic` Job is still compiled into a `Workload` and a `PodGroup`, so the assertion is on the compiled policy, not on the absence of a `PodGroup`;
- an unplaceable gang Job stays fully Pending instead of placing a subset of its Pods;
- the disabled test from #13533 is re-enabled.

### Graduation Criteria

#### Alpha

- OQ6 is settled before implementation starts.
- `GangSchedulingByDefault` is implemented behind a default-off feature gate, with no additional Kueue configuration field.
- Defaulting of both fields, opt-out, eligibility, and child Job exclusion have unit, integration, and end-to-end coverage.
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
- Disabling it does not modify existing Jobs.
  Jobs that already carry the gang policy keep it, and the field cannot be cleared.
- Each target cluster must preserve the field, checked independently for MultiKueue workers.
  A cluster that silently drops it is unsupported and must be reported; affected Jobs have to be recreated once the cluster supports the field.

## Implementation History

- 2026-08-09: Provisional KEP opened.

## Drawbacks

Kueue changes the scheduling policy of a Job without a per-Job request.
The default-off feature gate, the eligibility rules, and the object-level opt-out reduce that risk without removing it.
Because the gate is the only control, an administrator cannot narrow the default to part of the cluster except by narrowing which Jobs Kueue manages.
The implementation also depends on an upstream field whose availability varies across clusters, which adds version and gate checks to both single-cluster and MultiKueue operation.

Keying rule 4 on the presence of `spec.scheduling` also gives up reach, and does so unpredictably when another mutating webhook writes the same field.
A Job that reaches Kueue with `schedulingConstraints` already added loses the default; a Job that reaches Kueue without them, and acquires them afterwards, keeps the injected gang and ends up with the combination this KEP says it does not produce.
Which of the two happens is decided by the order of the webhook chain, and Kueue runs with `reinvocationPolicy: Never`, which is both the chart's default and the API server's, so it is not reinvoked to notice the second case.

## Alternatives

**A per-framework policy in the Kueue Configuration API.**
An earlier revision of this KEP made the Configuration API the policy source, with a list keyed by framework so that future integrations could add their own entry without changing `batch/job`:

```yaml
apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
gangSchedulingDefaults:
- framework: "batch/job"
  policy: Gang         # Gang | None, default None
  appliesTo: FixedSize # FixedSize | All
```

Dropped for alpha.
The gate already expresses the only choice alpha offers, and the field names would have to be settled before it is known which knobs the other integrations need, which is the same reason [Future extensions](#future-extensions) defers a shared write abstraction.

**Key the default on the scheduling policy rather than on `spec.scheduling`.**
An earlier revision defaulted a Job whose `spec.scheduling` was set but whose `schedulingPolicy` was nil, on the grounds that upstream resolves an unset policy to `Basic` at compile time rather than persisting it, so nothing was being overwritten.
Dropped because it makes Kueue read a half-specified field whose meaning upstream has not settled, and because it lets Kueue decide whether the API server accepts a manifest, as described under [Defaulting rules](#defaulting-rules).

**Leave `disruptionMode` to the follow-up KEP.**
An earlier revision kept this KEP to `schedulingPolicy.gang` alone, on the grounds that `all` also changes scheduler-side preemption behavior.
Dropped because `disruptionMode` is immutable and cannot be added to an existing Job, so a later KEP could apply it only to Jobs created after it ships, and the two halves of the default would never meet on the Jobs already running.

**Policy on LocalQueue or ClusterQueue.**
Useful per-queue control and possibly a better long-term model, but it requires a Kueue API change and is deferred from alpha.

**Per-Job annotation.**
Still requires users to opt in on every Job, and duplicates the purpose of `spec.scheduling`.

**Documentation only.**
No implementation risk, but no administrator-controlled default either.

**Kueue creates the WAS objects.**
Rejected for Jobs because the Job controller is the root controller and already owns compilation and lifecycle.
Kueue should express policy through the Job API rather than compete for the compiled objects; KEP-12385 draws the same line in its Non-Goals.

**Immediate `jobframework` write abstraction.**
Premature while the integration APIs still differ and change.
