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
  - [Compatibility with user-set scheduling](#compatibility-with-user-set-scheduling)
  - [Risks and mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [Defaulting rules](#defaulting-rules)
  - [Escape hatch](#escape-hatch)
  - [Eligibility](#eligibility)
  - [WAS topology and Kueue TAS](#was-topology-and-kueue-tas)
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
A user-set `spec.scheduling` raises a second question, which this KEP also answers: a WAS semantic Kueue does not reason about at admission can conflict with how Kueue admits the Job, so this KEP defines the alpha behavior for each semantic a Job can express.

## Motivation

Quota admission does not confirm that all Pods of a Workload can be placed at the same time.
Fragmentation, taints, topology, DRA, and non-Kueue Pods can prevent placement after quota has been reserved.
A partially placed Job holds resources while making no progress; `waitForPodsReady` can evict and retry it after a timeout, but it cannot prevent the partial placement.

Gang scheduling does not close that gap.
It does not make admission placement-aware, and it does not make an unplaceable Job placeable: a Job blocked by taints, topology, or DRA stays unplaceable either way.
What it changes is the failure mode.
Instead of holding a subset of the nodes while making no progress, the Job stays fully Pending, which is the state Kueue's eviction and requeue path is built to act on.
The upstream WAS and Job KEPs deliberately leave queueing and admission to systems such as Kueue, and the JobSet WAS KEP names the follow-up directly: "a follow-up Kueue design must define queueing and partial-eviction behavior" ([kubernetes-sigs/jobset#1253](https://github.com/kubernetes-sigs/jobset/issues/1253)).
This KEP defines the Kueue-side behavior for `batch/v1` Jobs, allowing administrators to apply gang scheduling without requiring every user to update their manifests.

A Job that sets `spec.scheduling` itself needs an answer of a different kind.
Kueue does not read or reason about most of what a user can write there when it admits the Job.
Some of those semantics are benign, but others can conflict with a decision Kueue is making itself, and a WAS topology constraint next to Kueue TAS is the concrete case: Kueue reserves quota for a `kueue.Workload` the scheduler will not place, and holds it until `waitForPodsReady` evicts the Job.
Defining what Kueue does with each of those semantics is the other half of this KEP.

### Goals

- Add an alpha feature gate, default off, for administrator-controlled gang scheduling defaults.
- Default `spec.scheduling.schedulingPolicy.gang: {}` and `spec.scheduling.disruptionMode.all: {}` on eligible Kueue-managed `batch/v1` Jobs at creation time.
- Never write into a `spec.scheduling` that the user set.
- Define and document alpha's behavior for each WAS semantic a Job can set through `spec.scheduling`, and fail visibly for the combinations alpha determines are incompatible.
- Define Job eligibility and the reason for excluding other Job shapes.
- Define the interaction with partial admission, workload slices, suspend and resume, `waitForPodsReady`, and MultiKueue.
- Define behavior and observability when the upstream API or feature gate is unavailable.
- Re-enable the WAS Job end-to-end test from #13533.

### Non-Goals

- Designing the upstream `spec.scheduling` API.
- Honoring a user-set `schedulingConstraints` during Kueue admission, in either direction.
  Alpha defines what happens when the two topology models meet and nothing more; converging them is out of scope, see [WAS topology and Kueue TAS](#was-topology-and-kueue-tas) and #13151.
- Writing any WAS field other than `schedulingPolicy.gang` and `disruptionMode.all`, in particular `minCount`, `schedulingConstraints`, and DRA `resourceClaims`.
- Acting on the contents of a user-set `spec.scheduling`, beyond the three decisions alpha takes from it: skipping defaulting, skipping partial admission for a Job that carries `gang.minCount`, and refusing a Job that carries `schedulingConstraints`.
  Alpha never writes into a field the user set; interpreting the rest of it is a follow-up ([Future extensions](#future-extensions)).
- Creating `scheduling.k8s.io` objects in Kueue, including `CompositePodGroup`.
  The Job controller is the root controller and compiler for standalone Jobs.
  Creating these objects for plain Pods is covered by [KEP-12385](/keps/12385-was-podgroups), and consuming user-provided ones by [KEP-13150](/keps/13150-bring-your-own-podgroup).
- Defining JobSet queueing or partial-eviction behavior.
  The JobSet WAS KEP places the Kueue-side integration in #13707.
- Defining how group-level disruption interacts with Kueue preemption, eviction, and `WorkloadPriorityClass`.
  This KEP sets the disruption granularity; which system decides, and on whose priority, belongs to a separate KEP (#13533).
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
The webhook also stamps a Kueue-owned annotation recording that it defaulted, which is what makes a later loss of the field detectable; see [Feature gate and API availability](#feature-gate-and-api-availability).

Kueue and the upstream scheduling API both define a `Workload`.
This document writes `kueue.Workload` for the Kueue queueing unit and `scheduling.Workload` for the upstream object the Job controller compiles.

### User Stories

- As a platform administrator of a shared GPU cluster, I enable the feature once instead of asking every team to add `spec.scheduling` to its manifests.
- As an ML engineer, I want an all-at-once Job to start only when all its Pods can be placed, without learning the WAS API.
  For a Job that should not use gang scheduling, I set `schedulingPolicy.basic: {}`.
- As a user who already wrote `spec.scheduling`, I keep what I wrote, and where Kueue cannot honor it I am told at creation rather than watching the Job hold quota and never start.
- As an existing user who writes no `spec.scheduling`, I see no change unless an administrator enables the feature.

### Policy source

The feature gate is the policy source for defaulting, and the mutating webhook is where it is applied.
The proposed compatibility refusal is a second execution point, in the validating webhook, and is proposed not to follow the gate ([OQ8](#open-questions)).
`BatchJobGangSchedulingByDefault` applies the default to every eligible Kueue-managed Job in the cluster; alpha adds no Kueue configuration field.
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
- **API support constrains when Kueue may mutate.**
  A cluster that does not honor the field discards it silently at write time, and it cannot be restored on that Job afterwards.
  See [Feature gate and API availability](#feature-gate-and-api-availability).

### Compatibility with user-set scheduling

Kueue never mutates a user-set `spec.scheduling`, but some of its semantics still interact with Kueue admission.
Alpha handles them as follows:

| WAS intent | Compatibility | Alpha behavior |
|---|---|---|
| `schedulingPolicy.basic` | Compatible | Preserve. This is the supported opt-out. |
| `schedulingPolicy.gang` | Compatible | Preserve. An explicit `minCount` excludes partial admission in alpha ([OQ1](#open-questions), [OQ2](#open-questions)). |
| `disruptionMode.all` | Compatible | Preserve. It matches Kueue's Job-granularity eviction. |
| `disruptionMode.single` | Unresolved | Preserve for alpha; scheduler-side disruption may break up an admitted gang ([OQ9](#open-questions)). |
| `schedulingConstraints.topology` | Potential conflict | Proposed refusal; enforcement and gating are [OQ7](#open-questions) and [OQ8](#open-questions). |
| `resourceClaims` | Out of scope | Preserve. A `resourceClaims`-only Job also loses the default, because rule 5 owns the whole field ([Drawbacks](#drawbacks)). |

`CompositePodGroup` is not expressed through `Job.spec.scheduling` and is not supported by the Job integration today.
Hierarchical-group compatibility is future work.

### Risks and mitigations

**Changing scheduling semantics without a per-Job request.**
The feature is disabled by default and requires an administrator to enable the gate; a Job that sets `spec.scheduling` is never modified, and `basic: {}` is the documented object-level opt-out.
This follows the existing defaulting pattern from [KEP-10765](/keps/10765-workload-priority-class-defaulting), a gated creation-time mutation that preserves explicit user values, and which is itself still alpha and default-off.
The proposed compatibility refusal does not follow the gate, so a cluster that upgrades can have Job creations refused while defaulting is still off; that is the cost [OQ8](#open-questions) weighs.
The comparison is not exact: that feature also requires a `WorkloadPriorityClass` named `default` to exist, so its gate alone changes nothing, while here the gate is the whole switch ([OQ4](#open-questions)).

**Silent loss of the field.**
A create request can succeed while `spec.scheduling` is discarded, with no error, warning, or condition.
Kueue skips clusters below the required version and reports the ones that discard the field anyway; see [Feature gate and API availability](#feature-gate-and-api-availability).

**Replacement Pods can wait for the full gang.**
Upstream evaluates gang satisfaction over the lifetime of the group, and the alternatives are still under discussion (`kubernetes/kubernetes#136334`).
Eligibility is therefore restricted to Jobs meant to run all Pods concurrently.

**Independent preemption systems.**
Kube-scheduler workload-aware preemption and Kueue eviction use different priority and accounting models, and that difference exists today.
Defaulting `disruptionMode.all` changes the granularity on the scheduler side: the compiled `PodGroup` is disrupted as a unit rather than Pod by Pod.
That direction narrows a mismatch rather than adding one, because Kueue already evicts at Job granularity, suspending the Job so that all of its Pods are deleted; `single` is the setting under which the two systems disagree about what a unit is.
What the default does not resolve is which system decides and on whose priority, since a gang admitted by Kueue can still be preempted by the scheduler under a priority that Kueue's accounting never saw.
That gap is now reachable by default rather than hypothetical: the scheduler ranks and preempts the group on the compiled `PodGroup`'s own priority and ignores the priority of the Pods in it, while a `WorkloadPriorityClass` deliberately sets neither.
Group preemption is already active on the scheduler side: a gang that cannot be placed reports `pod group preemption: No preemption victims found for incoming preemptor` on its Pods, so the whole group is evaluated as one preemptor.
Alpha therefore neither projects Kueue's priority onto the compiled objects nor reconciles the two ladders; both belong to the follow-up KEP.
A projection like the one [KEP-12385](/keps/12385-was-podgroups) proposes for the plain-Pod path would also have to respect the upstream intent to reject a `PodGroup` whose priority diverges from its Pods'.

**Admission can succeed when placement cannot.**
A Workload can reserve quota and remain unplaceable, then cycle through `waitForPodsReady`, eviction, and requeue; existing backoff applies.
Gang scheduling narrows the consequence, since an unplaceable Job stays fully Pending instead of holding a subset of the nodes, but it does not make admission placement-aware.
That is separate work, tracked in #13149.

## Design Details

### Defaulting rules

On a Job CREATE request, Kueue injects `schedulingPolicy.gang: {}` and `disruptionMode.all: {}` only when all of the following hold:

1. `BatchJobGangSchedulingByDefault` is enabled.
2. The Job is managed by Kueue.
3. The Job has no Kueue-managed ancestor.
4. The Job is not a copy dispatched by a MultiKueue manager.
5. The Job expresses no scheduling intent of its own, meaning `spec.scheduling` is unset and its Pod template does not set `schedulingGroup`.
6. The Job is eligible.
7. The Job is not opted into workload slices ([OQ5](#open-questions)).
8. The target cluster's API server is at or above the version that serves the field.

Rule 2 reuses the Job integration's existing managed-job decision, including `manageJobsWithoutQueueName` and `managedJobsNamespaceSelector` from [KEP-3589](/keps/3589-manage-jobs-selectively), and runs after LocalQueue defaulting.
Rule 3 reuses the existing Kueue-managed-ancestor check, preventing child Jobs from receiving a second scheduling intent when their root controller owns compilation.
A Pod template that sets `schedulingGroup` counts as user intent for the same reason: upstream treats it as a bring-your-own `PodGroup` and the Job controller compiles nothing for such a Job, so an injected policy would never take effect.
Consuming user-provided `PodGroup`s belongs to [KEP-13150](/keps/13150-bring-your-own-podgroup).

Rule 5 keys on the presence of `spec.scheduling`, not on the scheduling policy inside it.
Presence means presence in the admission request, which is the only version of the object a webhook sees; on a cluster that does not preserve the field, the request carries it and the stored Job does not.
A Job that sets any part of the field owns all of it, so Kueue writes the two fields together or not at all.
What Kueue does with the contents is a separate question from whether it writes them, and is answered in [Compatibility with user-set scheduling](#compatibility-with-user-set-scheduling); a `gang.minCount` that differs from `parallelism` is the one case still left to the follow-up in [Future extensions](#future-extensions).

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
That failure is not only silent but undetectable at runtime: the Pods that would complete the gang are never created, so the scheduler never evaluates the group, and neither the Pods, the events, nor the compiled `PodGroup`'s status carry a reason.
Admission is therefore the only point at which the shape can be caught, and the condition is not waivable.
Kueue skips defaulting for such a Job and reports the reason.

Eligibility does not depend on `completionMode`; Indexed and NonIndexed Jobs of the same shape behave identically.

### WAS topology and Kueue TAS

Kueue TAS and WAS topology can become independent placement authorities.
Kueue TAS commits a placement before the Pods become schedulable, while WAS then applies its own topology constraint.
Their intersection can be empty, leaving an admitted Job holding quota until `waitForPodsReady` evicts it.

Alpha therefore proposes refusing Kueue-managed Jobs that set `schedulingConstraints`, rather than silently admitting this combination.
The exact enforcement point, and whether the refusal follows `BatchJobGangSchedulingByDefault`, remain [OQ7](#open-questions) and [OQ8](#open-questions).

Alpha does not translate between the two topology models.
Kueue TAS has semantics WAS cannot express, and even their required-topology overlap is not symmetric.
Convergence is out of scope and tracked by #13151.

### Compatibility with Kueue mechanisms

| Interaction | Behavior |
|---|---|
| Partial admission, Kueue-injected gang | Supported. Kueue leaves `minCount` unset, so the gang follows the admitted `parallelism`. |
| Partial admission, user-set `minCount` | Excluded in alpha. Rule 5 never defaults such a Job, but Kueue does still reduce its `parallelism`: partial admission keys on the `kueue.x-k8s.io/job-min-parallelism` annotation and never reads `spec.scheduling`, so the two do meet on a Job that carries both. `parallelism` itself is mutable, but lowering it alone is rejected because the resulting state would have `minCount > parallelism`; lowering both in one request is validated against the final state and accepted. Keeping the two apart is therefore work alpha has to do rather than a consequence of rule 5, and whether beta adds that atomic update is [OQ1](#open-questions). |
| Workload slices | Excluded in alpha by rule 7. Kueue already refuses to combine partial admission with elastic Jobs, although it rejects such a Job outright where rule 7 only skips defaulting. Upstream does rescale a gang in place; what is unverified is the Kueue-side slice semantics ([OQ5](#open-questions)). |
| Suspend, eviction, requeue | The Kueue side is unchanged: eviction suspends the Job, its Pods are deleted, the `kueue.Workload` is requeued, and re-admission resumes the Job; the injected policy is immutable and survives the cycle. What is upstream dependent is the compiled-object lifecycle: suspending a Job does not currently delete the compiled objects, and beta is expected to delete and recreate them. Resume reuses the same `PodGroup`, and gang scheduling still applies to the new Pods: on a cluster with room for two Pods of a three-Pod gang, none are placed. |
| `waitForPodsReady` | The scheduler keeps the group Pending while Kueue can still evict it after the timeout ([OQ3](#open-questions)). |
| JobSet | Child Jobs of a Kueue-managed JobSet are excluded by rule 3. The only child Jobs that remain eligible are those whose parent JobSet is not managed by Kueue and that are admitted individually through the Job integration. This KEP does not select a JobSet queueing model. |
| Kueue TAS | Defined by refusal rather than by reconciliation: a Kueue-managed Job that sets `schedulingConstraints` is refused at creation, so alpha never runs the two placement authorities against each other, and a defaulted Job carries no constraints, so Kueue TAS decides placement and the scheduler's group cycle only validates it. See [WAS topology and Kueue TAS](#was-topology-and-kueue-tas). `SchedulerLibraryIntegration`, which swaps Kueue's TAS node-fit check for a scheduler filter plugin set, is covered by the same refusal where the user set a constraint, but its effect on the defaulted path is not analyzed: it replaces the check this row assumes Kueue is making. |
| `WorkloadPriorityClass` | Not projected. A Kueue priority reaches neither the compiled `PodGroup` nor the Pods, so the group is scheduled and preempted at whatever priority the `PodGroup` compiles to ([Risks and mitigations](#risks-and-mitigations)). |
| MultiKueue | Defaulted on the manager only. Neither availability check reaches a worker, and rule 4 keeps the worker-side webhook off the dispatched copy ([Feature gate and API availability](#feature-gate-and-api-availability)). |
| Preemption and eviction | The injected `disruptionMode.all` makes the compiled `PodGroup` one disruption unit on the scheduler side, which matches Kueue's Job-granularity eviction. Which system decides, and on whose priority, is out of scope for this KEP. |
| `ProvisioningRequest` | No interaction defined in alpha. Provisioning is unaware of WAS, and what it writes back are Pod template updates rather than WAS fields, so the two compose as they are. What alpha does not analyze is timing: the booking is not retried once the Workload is admitted, so a gang that cannot be placed can outlive it, which is the same window as [OQ3](#open-questions). |
| CronJob | Each scheduled Job is defaulted on its own, since the fields can only be set at creation. A CronJob-created Job has no Kueue-managed ancestor, so it receives the default whenever Kueue manages it and its shape qualifies. |
| BYO PodGroup ([KEP-13150](/keps/13150-bring-your-own-podgroup)) | Wherever that KEP derives a `PodSet` size from a user-set gang, `gang.minCount` is a floor rather than the group size, so using it as the count under-reserves quota by up to `parallelism - minCount`; a Kueue-injected gang is unaffected, because Kueue writes no `minCount` and the floor then equals `parallelism` ([OQ2](#open-questions)). Whether that derivation reaches a Job at all is that KEP's question to answer: it states the Job integration already sizes `PodSet`s from `parallelism`, and lists reading `batch/v1`'s own gang as future work. |
| DRA `resourceClaims` | This KEP writes none. Rule 5 still treats any existing `spec.scheduling` as user-owned, so a Job that sets only `resourceClaims` receives neither default. Whether the presence check should distinguish policy-bearing fields is left to [Future extensions](#future-extensions). |

### Feature gate and API availability

`BatchJobGangSchedulingByDefault` is alpha and disabled by default.

**Kueue can only write a field it is built against.**
The Job mutating webhook is a typed `admission.Defaulter[*batchv1.Job]`, so it can emit only what the vendored `k8s.io/api/batch/v1` declares, and Kueue is on `k8s.io/api v0.36.3` while `spec.scheduling` first appears in 1.37.
Implementation therefore follows Kueue's Kubernetes dependency bump: until that lands, the feature is not implementable in that defaulter, rather than implemented and switched off.
Writing the field through an untyped patch is possible, and Kueue already reads the compiled objects through unstructured clients in its WAS tests, but it would put a hand-built patch on the Job admission path for the length of one release, which alpha does not need.
The reverse skew is safe on the cluster the user writes to: a Kueue built against an older API does not strip a user-set `spec.scheduling` on a 1.37 cluster, because controller-runtime drops the patch operations that remove fields only its own scheme is missing and Kueue does not opt into `DefaulterRemoveUnknownOrOmitableFields`.
MultiKueue is the exception, and is covered at the end of this section.

**A Kueue built against 1.37 still meets clusters that discard the field.**
REST mapping and OpenAPI cannot detect them, because the Job resource and the field schema remain visible even when the field is not preserved.
The presence of the `scheduling.k8s.io` API group is also only a proxy, because it is governed by `GenericWorkload` rather than by `WorkloadWithJob`, which is the gate that decides whether the Job field survives.
Alpha therefore splits the problem: rule 8 keeps Kueue off the clusters that cannot serve the field at all, and a check after creation reports the ones that serve it and drop it anyway.

Rule 8 is the API server version, taken from the existing `ServerVersionFetcher`, which Kueue fetches once during startup and refreshes every ten minutes, and already passes to job webhooks through `jobframework.WithKubeServerVersion`, although no production path makes a decision from it today.
A cluster below the first version that serves the field is unsupported, and the webhook skips the mutation and reports the reason.
Version is necessary but not sufficient, since a supported version can still run with `WorkloadWithJob` disabled.

The remaining states are detected after creation, in the Job reconciler.
Detection needs a trace that outlives the field, because the API server discards `spec.scheduling` after admission: a stored Job that Kueue defaulted on a cluster with the gate disabled is indistinguishable from one Kueue never touched, and, under rule 5, also from one whose user-set opt-out was discarded the same way.
The webhook therefore records the mutation in a Kueue-owned annotation, provisionally `kueue.x-k8s.io/gang-defaulted`, which the field's feature gate does not cover; Kueue's Pod integration stamps `kueue.x-k8s.io/managed` from its own webhook for the same reason.
A Job that carries the annotation and no `spec.scheduling` proves that the cluster discarded the field, and Kueue reports it.
The same trace does not exist for a Job whose own `spec.scheduling` the cluster discarded, because Kueue does not mutate such a Job and leaves nothing on it.
Alpha accepts that limit rather than stamping Jobs it otherwise leaves untouched.
Alpha reports per Job rather than suppressing further defaulting cluster-wide, because a webhook cannot observe an administrator enabling the gate afterwards, so a cluster-level stop would need a re-arm path this design does not have.
The behavior until an administrator acts is the pre-feature behavior, a Job scheduled Pod by Pod, because once the field is dropped it cannot be restored on that Job.

A dry-run create carrying the field, followed by inspection of the returned object, would detect the same states before the first Job is admitted.
It is not proposed for alpha because it must run outside the admission path, its probe object must be excluded from Kueue's own defaulting, and its result needs a cache contract per target cluster that is invalidated by observed field loss or by a change of version or gate posture.
Whether beta adds it is [OQ6](#open-questions).

Neither check covers the per-component skew, because `WorkloadWithJob` takes effect separately in each component: the API-server gate decides whether the field is preserved, while the controller-manager gate decides whether the Job controller compiles the `scheduling.Workload` and `PodGroup`.
A cluster with the API-server gate enabled and the controller-manager gate disabled preserves the field but never compiles it, so any preservation check passes while the injected policy stays inert.
The reconciler is the only detection point, and it should also report a defaulted gang Job whose compiled objects never appear.
That state is the absence of the objects themselves, and is distinct from a compiled gang that cannot be placed, which does produce them; see [Observability](#observability).

**MultiKueue is not covered by either check.**
Both observe the cluster Kueue runs against: the version comes from the manager's own API server, and the reconciler compares the manager's copy of the Job, which keeps the field.
The remote Job is built from the manager's typed spec, so a manager below the required version drops `spec.scheduling` on the way to the worker, including one the user set.
Alpha therefore leaves the dispatched copy alone, which is rule 4: the remote Job already carries the `kueue.x-k8s.io/multikueue-origin` label, and the worker-side webhook has to skip on it, so a Job's policy, or its absence, is decided once on the manager.
No webhook reads that label today, so this is a check to add rather than one to reuse.
Checking workers independently is a beta item, with the rest of MultiKueue behavior.

### Observability

The Job shows that gang scheduling was requested but not the resolved `minCount`; the compiled `PodGroup` is the source of truth for the effective group size.
The implementation should surface successful defaulting, defaulting skipped for an unsupported cluster, and a defaulted Job whose stored object lost the field.
For a defaulted Job that cannot be placed, the signal is the Pod-level `PodScheduled: False` condition with reason `Unschedulable`, whose message names the unsatisfied group, for example `minCount (3) cannot be satisfied`.
The `PodGroup`'s `PodGroupInitiallyScheduled` condition is not that signal: it latches on the first successful placement and is not re-armed, so after a Kueue eviction and re-admission it still reports `True` while the new Pods are unschedulable.
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
| OQ3 | Do gang waiting and `waitForPodsReady.timeout` need coordination? | Probably not a hard constraint. Kueue can distinguish an unplaceable gang from ordinary startup delay through the Pod-level `Unschedulable` condition; the `PodGroup` latch cannot carry that, as described under [Observability](#observability). |
| OQ4 | What carries the administrator's cluster-level choice once `BatchJobGangSchedulingByDefault` graduates? | Open. Alpha needs nothing, since the gate is default-off and enabling it is itself the administrator's choice. Once the gate defaults on, the behavior becomes cluster-wide with only a per-Job opt-out. `WorkloadPriorityClass` defaulting has a second switch, the presence of a `WorkloadPriorityClass` named `default`, and gang has no object whose presence can carry the same intent. |
| OQ5 | Should defaulting be skipped when workload slices are enabled? | Yes for alpha, because the Kueue-side slice semantics are unverified. |
| OQ6 | Should a preflight probe replace the check after creation for clusters that discard the field? | Beta. The version rule and the annotation comparison in [Feature gate and API availability](#feature-gate-and-api-availability) are enough while the gate is default-off; a probe buys pre-admission detection, and a per-cluster result that MultiKueue workers could be checked against, at the cost of a cache contract. |
| OQ7 | At what point should Kueue refuse a WAS topology constraint: every Kueue-managed Job at CREATE, or only once admission determines that Kueue TAS applies? | Open, and the decision this KEP most needs confirmed. Alpha proposes CREATE, because a webhook cannot resolve the `ResourceFlavor` and refusing later means adding a compatibility check on the admission path. The cost is that a Job bound for a ClusterQueue with no topology-aware flavor is refused although nothing would have conflicted. |
| OQ8 | Should that refusal follow `BatchJobGangSchedulingByDefault`, or stay independent of the defaulting feature? | Alpha proposes independent, on the grounds that a correctness guard should not be something an administrator opts into alongside a default. The counterweight is larger than it first appears: a webhook is shown the admission request rather than the stored object, so an independent guard also refuses Jobs on clusters where `WorkloadWithJob` is disabled on the API server and the field would have been discarded silently. Gating it would leave the guard off by default and tie a compatibility behavior to whether an administrator wanted gang defaults. |
| OQ9 | What compatibility contract should Kueue offer for a user-set `disruptionMode.single`? | Open. Upstream lets the scheduler disrupt Pods of the group individually, so on a Job that also sets `gang` it can preempt Pods out of a group Kueue has admitted. Whether Kueue's eviction and preemption paths assume disruption is all-or-nothing has not been verified. The positions are to preserve it as today, to refuse it as with `schedulingConstraints`, or to reconcile the two disruption units; alpha proposes preserving it. |

### Upstream dependencies

The design relies on the following upstream behavior, defined in [KEP-5547](https://github.com/kubernetes/enhancements/issues/5547).
The published Job documentation is not a second source for it: it still describes the earlier model, in which the Job controller infers a gang from the Job's shape and there is no `spec.scheduling` field to set.

- An omitted `spec.scheduling` means `Basic`, and an omitted `gang.minCount` defaults to `.spec.parallelism`.
  That resolution happens during validation and compilation rather than being persisted, so a Job whose author left the field alone does not acquire it; rule 5 depends on this.
- Every `spec.scheduling` field is immutable after creation except `schedulingPolicy.gang.minCount`, and a `minCount` greater than `parallelism` is rejected.
  Immutability covers whether a field is set at all, so neither the policy nor `disruptionMode` can be added to an existing Job.
- The `Basic` policy and `disruptionMode.all` are rejected together, and for a Job an unset policy resolves to `Basic`.
- A cluster with `WorkloadWithJob` disabled discards `spec.scheduling` at write time without an error, and the field cannot be added to that Job later.
- `gang.minCount` is a floor rather than the group size: the number of Pods placed ranges from `minCount` to `parallelism` with available capacity.
- The Job controller compiles the `scheduling.Workload` and `PodGroup` as soon as the Job exists, including while it is suspended, and recompiles `gang.minCount` when `parallelism` changes.
- The scheduler ranks and preempts a `PodGroup` on the `PodGroup`'s own priority, and divergence from its Pods' priority, tolerated in alpha, is intended to be rejected from beta ([KEP-5710](https://github.com/kubernetes/enhancements/issues/5710)).

These assumptions were verified against released `v1.37.0` on a cluster with both gates enabled, and must be revalidated before implementation.
The compiled defaults were observed directly: a Job without `spec.scheduling` compiles to `basic` with `disruptionMode: single`, and a gang that omits `minCount` compiles to a `minCount` equal to `parallelism`.
Writing the field needs `k8s.io/api` at 1.37, as described under [Feature gate and API availability](#feature-gate-and-api-availability), and that bump is larger than `k8s.io/api`: Kueue also depends directly on `k8s.io/kubernetes`, and on `sigs.k8s.io/scheduler-library`, which the WAS scheduling simulator compiles against and which has a single published version, `v0.1.0-alpha1`, whose `go.mod` pins `k8s.io/kubernetes v1.36.0`.
Tests can reach the compiled objects through unstructured clients while that version lags, as the existing WAS end-to-end test already does.

## Test Plan

[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.

### Prerequisite testing updates

End-to-end coverage requires a Kubernetes version that serves `batch/v1 spec.scheduling`.
The field first appears in `v1.37.0-rc.0`; until 1.37 is released and Kueue can depend on it, the tests run in the WAS lane built from Kubernetes `main`.

### Unit tests

Table-driven Job webhook tests cover the gate disabled, ineligible Job shapes, a child Job with a Kueue-managed ancestor, a Job not managed by Kueue, a Job carrying the MultiKueue origin label, idempotent reinvocation, and a cluster version that does not serve the field.
Rule 5 is covered by every shape of a user-set `spec.scheduling`, each expecting no mutation: an explicit `gang`, an explicit `basic`, a `disruptionMode` alone, `disruptionMode.all` with no policy, `schedulingConstraints` alone, and a nil `schedulingPolicy`.

### Integration tests

Using an `envtest` that serves the field with `WorkloadWithJob` enabled: defaulting enabled and disabled, explicit opt-out, child Job exclusion, eviction of a defaulted Job followed by resume with the injected policy unchanged, and observability for applied defaulting, skipped defaulting, and a preserved but never-compiled policy.
A second `envtest` with the gate disabled on the API server covers the check after creation: the stored Job carries the annotation and no `spec.scheduling`, and the loss is reported.

### e2e tests

- an eligible Kueue-managed Job produces a `PodGroup` whose gang minimum matches the `kueue.Workload` `PodSet` count and whose disruption mode is `all`.
  The assertion can run before the Job is resumed, since compilation does not wait for admission;
- a partially admitted Job converges: the reduced `parallelism` and the compiled `PodGroup` gang minimum reflect the admitted count, while `completions` is unchanged. The `kueue.Workload`'s `spec.podSets[].count` stays at the requested size; the admitted count is recorded in `status.admission.podSetAssignments[].count`, so that is what the assertion reads;
- a Job with `basic: {}` keeps basic scheduling.
  A `Basic` Job is still compiled into a `Workload` and a `PodGroup`, so the assertion is on the compiled policy, not on the absence of a `PodGroup`.
  A Job Kueue leaves alone compiles to `basic` with `disruptionMode: single`, so the compiled `all` is what distinguishes a defaulted Job from an untouched one;
- an unplaceable gang Job stays fully Pending instead of placing a subset of its Pods;
- the disabled test from #13533 is re-enabled.

### Graduation Criteria

#### Alpha

- Kueue's vendored `k8s.io/api` declares `batch/v1 Job.spec.scheduling`; implementation starts after that dependency bump.
- `BatchJobGangSchedulingByDefault` is implemented behind a default-off feature gate, with no additional Kueue configuration field.
- Every defaulting rule has unit and integration coverage, including the annotation, the MultiKueue skip, and the workload-slice skip; defaulting, opt-out, and partial admission additionally have end-to-end coverage.
- An unsupported cluster is handled safely and observably: the webhook skips the mutation below the required version, and the reconciler reports a field that the cluster discarded.
- Partial admission is not performed for Jobs carrying an explicit `gang.minCount`.
- The WAS Job test from #13533 is re-enabled.

#### Beta

- The upstream Job integration has stable suspend and resume semantics for Kueue eviction and requeue.
- OQ1 through OQ6 are resolved and implemented; OQ4 in particular cannot outlive the gate's graduation.
- Observability is finalized.
- MultiKueue behavior is defined and tested.

## Upgrade, Downgrade, and Version Skew

- Enabling the feature affects only Jobs created afterwards; existing Jobs cannot be backfilled.
- Disabling it does not modify existing Jobs.
  Jobs that already carry the gang policy keep it, and the field cannot be cleared.
- A cluster that serves the field and silently drops it is unsupported.
  The loss is reported per Job, and affected Jobs have to be recreated once the cluster supports the field.
- MultiKueue is not defaulted in alpha, and a manager below the required version does not carry a user-set `spec.scheduling` to its workers.
- Kueue older than the Kubernetes dependency bump cannot write the field at all, and does not strip one the user wrote.
  A Kueue that can write it skips the mutation below the required API server version.
- As proposed, the `schedulingConstraints` refusal follows the dependency bump rather than the feature gate, so upgrading into a Kueue that can read the field refuses a Job shape the cluster previously accepted, whether or not `BatchJobGangSchedulingByDefault` is enabled ([OQ8](#open-questions)).
  Existing Jobs are unaffected only because the check is on CREATE alone, and because `spec.scheduling` cannot be added to a Job that was created without it.

## Implementation History

- 2026-08-09: Provisional KEP opened.

## Drawbacks

Kueue changes the scheduling policy of a Job without a per-Job request.
The default-off feature gate, the eligibility rules, and the object-level opt-out reduce that risk without removing it.
Because the gate is the only control over defaulting, an administrator cannot narrow it to part of the cluster except by narrowing which Jobs Kueue manages, and the proposed refusal has no such control at all ([OQ8](#open-questions)).
The implementation also depends on an upstream field whose availability varies across clusters, which adds version and gate checks to both single-cluster and MultiKueue operation.

Refusing every Kueue-managed Job that carries a WAS topology constraint is broader than the conflict it prevents.
A Job whose ClusterQueue uses no topology-aware flavor would have been placed correctly and is refused anyway, because the webhook cannot know at CREATE which flavor admission will assign.
Refusal is the reversible direction, since it can be narrowed later without having to un-admit Jobs a permissive alpha accepted, but until it is narrowed it is a real loss of reach.


Rule 5's presence test costs reach in a case nobody intends.
A Job that sets only `spec.scheduling.resourceClaims` has expressed no scheduling policy at all and still loses both defaults.
Keying on the policy-bearing fields instead would recover it, and is rejected for the reason under [Alternatives](#alternatives), that it makes Kueue read a half-specified field whose meaning upstream has not settled.
Alpha accepts the loss rather than resolving it.


Keying rule 5 on the presence of `spec.scheduling` also gives up reach, and does so unpredictably when another mutating webhook writes the same field.
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

**Defer the compatibility behavior to a follow-up.**
Keep alpha to defaulting, and design what Kueue does with a user-set `spec.scheduling` separately.
This is what the proof of concept does today: it preserves a user-set `spec.scheduling` and acts on none of it.
The cost is that this interaction stays undefined for a release, so a cluster running both features gets the silent divergence in the meantime, and a Job created under a permissive alpha cannot be revisited because `spec.scheduling` is immutable.
The argument for doing it here is that both halves meet on the same field and the same webhook.
This is a live option rather than a rejected one, and it is the alternative to [OQ7](#open-questions).

**Admit the Job and report the incompatibility.**
Surface it on the `kueue.Workload` as a condition and an event instead of refusing at CREATE, leaving the operator to act; deactivating the `kueue.Workload` rather than admitting it is a third position between that and refusal.
It needs no decision about the enforcement point.
The argument against is that the `kueue.Workload` holds quota for the duration and the field cannot be corrected afterwards, so the report describes a state nobody can fix except by deleting and recreating the Job.

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
