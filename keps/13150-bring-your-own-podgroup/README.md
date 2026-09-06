# KEP-13150: Bring Your Own PodGroup

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories (Optional)](#user-stories-optional)
    - [Story 1](#story-1)
    - [Story 2](#story-2)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [Test Plan](#test-plan)
    - [Unit tests](#unit-tests)
    - [Integration tests](#integration-tests)
    - [e2e tests](#e2e-tests)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
- [Future Work](#future-work)
<!-- /toc -->

## Summary

Kueue determines a workload's `PodSet` shape (gang size, etc.) two ways today:
per-integration logic (a Job's `parallelism`/`completions`, a JobSet's replicated jobs),
or, for unowned plain Pods, Kueue-specific markers
(`kueue.x-k8s.io/pod-group-name`/`-total-count`). Kubernetes' Workload-Aware Scheduling
(WAS) effort standardizes the same information:

- A Pod can reference a standalone `PodGroup` via `spec.schedulingGroup.podGroupName`,
  which carries the gang size in `spec.schedulingPolicy.gang.minCount`.
- An owning object (Job, JobSet, RayJob, ...) can link to a `Workload` object via
  `spec.controllerRef`, whose `spec.podGroupTemplates[]` give a gang size per role.

This KEP lets Kueue derive `PodSet` shape from these standard APIs for **any**
integration, behind a new alpha feature gate (`BringYourOwnPodGroup`) — so clusters that
already produce standard WAS objects don't also need Kueue-specific markers.

WAS's `PodGroup`/`Workload` types are beta as of Kubernetes 1.37 (vendored as
`scheduling.k8s.io/v1beta1`).

## Motivation

Requiring Kueue's own annotations on top of standard WAS fields means two sources of
truth that can drift, and a disagreement between what kube-scheduler sees (the standard
API) and what Kueue sees (its own annotations) can cause kube-scheduler to reject Kueue's
admission decisions.

This implements the "bring your own PodGroup" idea from
[kubernetes-sigs/kueue#13150](https://github.com/kubernetes-sigs/kueue/issues/13150)'s
[design comment](https://github.com/kubernetes-sigs/kueue/issues/13150#issuecomment-5122519009),
generalized beyond plain Pods: whenever a workload already carries a standard WAS
grouping reference, Kueue should read its shape from that reference directly.

### Goals

- Plain Pods: derive group name and expected size from
  `pod.spec.schedulingGroup.podGroupName` and the referenced `PodGroup`'s
  `spec.schedulingPolicy.gang.minCount`, as an alternative to
  `kueue.x-k8s.io/pod-group-name`/`-total-count`.
- Any other job framework (Job, JobSet, RayJob, MPIJob, etc.): if the owning object links
  to a `Workload` via `spec.controllerRef`, derive each `PodSet`'s size from the matching
  `PodGroupTemplate.schedulingPolicy.gang.minCount` instead of the framework's own spec
  fields.
- Implement both paths once, in Kueue's shared job-framework core, so every integration
  benefits without per-integration code.
- Gate this behind an off-by-default alpha feature gate (`BringYourOwnPodGroup`) so
  existing clusters see no behavior change unless they opt in.

### Non-Goals

- Deriving TAS topology constraints from `schedulingConstraints.topology`.
- Integrating standard workload-level priority (`priority`/`priorityClassName`) into
  Kueue's queue ordering or preemption.
- Kueue creating or managing `PodGroup`/`Workload` lifecycle — Kueue only reads them.

These are natural follow-ups, left to separate KEPs so this one stays scoped to deriving
`PodSet` shape and gang size.

## Proposal

When `BringYourOwnPodGroup` is enabled, Kueue sources `PodSet` shape from standard WAS
APIs via one of two paths, depending on whether the workload has an owner:

- **Unowned Pods**: if a Pod sets `spec.schedulingGroup.podGroupName` and not the legacy
  `kueue.x-k8s.io/pod-group-name` label, Kueue uses that as the group name and reads the
  referenced `PodGroup`'s `spec.schedulingPolicy.gang.minCount` as the expected group
  size. A `PodGroup` that doesn't exist yet is treated like a
  group that hasn't reached its expected size — Kueue watches `PodGroup` objects and
  requeues Pods once their group appears.
- **Owned workloads** (Job, JobSet, RayJob, MPIJob, and any other `GenericJob`
  integration): if a `Workload` object's `spec.controllerRef` points at the integration's
  owning object, Kueue matches each `PodGroupTemplate` to the corresponding `PodSet` and
  uses its `gang.minCount` for the count. If no matching `Workload` or `PodGroupTemplate`
  exists, the integration's existing spec-derived count is used unchanged — this path
  only ever narrows to standard-API-derived counts, never breaks an integration that
  doesn't use WAS.

Both paths are implemented once in Kueue's shared job-framework core, so adding support
for a new job framework doesn't require reimplementing this logic.

### User Stories (Optional)

#### Story 1

A user's existing tooling already sets `schedulingGroup.podGroupName` on their plain Pods
and creates a `PodGroup` with `gang.minCount: 4`. Today they'd also need to annotate
every Pod with `kueue.x-k8s.io/pod-group-name` and `total-count: "4"` for Kueue to admit
the group atomically. With this feature enabled, Kueue reads the same information from
the `PodGroup` they already have.

#### Story 2

A user runs a JobSet on a WAS-enabled cluster, where a controller has already produced a
`Workload` and `PodGroups` with one `PodGroupTemplate` per replicated job, linked back to the
JobSet via `controllerRef`. Kueue's JobSet integration derives each role's gang size from
those templates instead of the JobSet's own replica counts — no JobSet-specific Kueue
configuration required.

### Risks and Mitigations

- **Coupling to an evolving upstream API.** WAS's types are themselves beta. Mitigated
  by keeping this behind its own gate and not promoting past alpha until upstream
  stabilizes.
- **Ambiguous dual sources of group identity.** Mitigated by webhook validation rejecting
  disagreeing values, and by only overriding a spec-derived count where a
  `PodGroupTemplate` match actually exists.
- **Cross-framework logic in shared code adds risk to every integration.** Mitigated by
  keeping the new path strictly additive and gated: with the gate off, or no matching
  `Workload`/`PodGroupTemplate`, every integration's existing behavior is untouched.

## Design Details

- Add an alpha feature gate, `BringYourOwnPodGroup`, in `pkg/features`.
- API Discovery for PodGroup / Workload: Use api discovery to decide if
  `PodGroup` or `Workload` are available on the cluster. If not, the feature will be disabled.
  The API is changing quite a bit so the goal is to support only 1.37 and future.
- **Owned workloads**: `jobframework.JobPodSets` — the shared choke point used by every
  `NewGenericReconcilerFactory`-based integration (Job, JobSet, RayJob, MPIJob, etc.) —
  calls `wasapi.PodGroupTemplateGangMinCounts` after computing an integration's own
  `PodSet`s, and overrides `PodSet.Count` where a same-named `PodGroupTemplate` exists.
  No per-integration code changes were needed. The plain-Pod integration is excluded from
  this path (a bare Pod has no separate owning object), and uses the path below instead.
- **Unowned Pods** (`pkg/controller/jobs/pod`, `pkg/util/pod`):
  - `utilpod.GetPodGroupName` falls back to `pod.Spec.SchedulingGroup.PodGroupName` when
    no legacy label/annotation is set and the gate is enabled.
    `utilpod.HasStandardPodGroupName` reports whether the resolved name came from that
    fallback.
  - `Pod.groupTotalCount` (`pod_controller.go`) calls `wasapi.PodGroupGangMinCount`
    instead of reading `kueue.x-k8s.io/pod-group-total-count` when
    `HasStandardPodGroupName` is true. `Pod` gained a `client` field (set in `Load`) so
    this is reachable from `Finished`, whose `GenericJob` interface has no client
    parameter.
  - The per-pod consistency check in `validatePodGroupMetadata` is skipped for
    standard-field groups: there's no per-pod annotation to compare, and agreement is
    already implied by every pod resolving to the same `PodGroup`.
  - The Pod validating webhook (`pod_webhook.go`) gained
    `validateStandardPodGroupNameConflict`, rejecting a Pod whose standard field and
    legacy label/annotation disagree, and no longer requires
    `GroupTotalCountAnnotation` when the group name came from the standard field.
- Controller watches, so a Pod/job that arrives before its `PodGroup`/`Workload` is
  reconciled once one appears, rather than waiting for the next backoff retry:
  - `pkg/controller/jobs/pod`: resolves the `PodGroup` GVK at startup (skipping the watch,
    without failing manager startup, if the API isn't installed) and watches it, mapping
    a changed `PodGroup` directly to its own name/namespace as the reconcile key.
  - `pkg/controller/jobframework`: `genericReconciler.SetupWithManager` — shared by every
    `NewGenericReconcilerFactory`-based integration — similarly resolves the `Workload`
    GVK and watches it, mapping a changed `Workload` to a reconcile request for the
    owning object named by its `spec.controllerRef`.
- Add `get`/`list`/`watch` RBAC on `podgroups.scheduling.k8s.io` (Pod integration) and
  `workloads.scheduling.k8s.io` (shared) for the kueue-controller-manager ClusterRole.

Once 1.37 is released, this feature will be updated to import 1.37 apis and use those objects.

### Test Plan

[ ] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.

#### Unit tests

- `pkg/util/wasapi`: `ResolveGVK`, `PodGroupGangMinCount`, and
  `PodGroupTemplateGangMinCounts`, including not-installed/not-found/no-gang-policy
  cases, against a fake client with a synthetic `RESTMapper`.
- `pkg/util/pod`: `GetPodGroupName`/`HasStandardPodGroupName` resolution, including the
  standard-field fallback, legacy-takes-precedence, and gate-off paths (`pod_test.go`).
- `pkg/controller/jobs/pod`: `groupTotalCount` resolution from a `PodGroup`, including
  the not-yet-existing case; webhook rejection of conflicting group identities and
  skipping the legacy total-count-annotation requirement.
- `pkg/controller/jobframework`: `JobPodSets`'s `Workload`/`controllerRef` override,
  including the no-match fallback and plain-Pod exclusion (`utils_test.go`); the
  `Workload`-to-owner watch mapping function.

Done, as of the alpha implementation.

#### Integration tests

All in a new `test/integration/singlecluster/controller/was` suite, run against envtest
with `GenericWorkload=true` and `scheduling.k8s.io/v1beta1=true`:

- Plain Pods, gate enabled: a group defined only via `schedulingGroup.podGroupName` +
  `PodGroup` is admitted once all member Pods exist.
- Plain Pods: `PodGroup` created after its member Pods — Pods are requeued and admitted
  once it appears.
- Plain Pods: conflicting group identity is rejected by the webhook.
- Job (as the owned-integration representative), gate enabled: a
  `Workload`/`PodGroupTemplates` object overrides the Job's `parallelism`-derived count.
- Job, gate enabled, no matching `Workload`: `parallelism`-derived count is unchanged.

Done, as of the alpha implementation. Gate-disabled behavior is covered at the unit level
rather than duplicated here.

#### e2e tests

Planned: extend the existing WAS e2e suite
(`test/e2e/singlecluster/was`) with a plain-Pod case relying only on the standard
`PodGroup` reference, and a case for at least one owned integration relying only on a
`Workload`/`PodGroupTemplates` object. Tracked as outstanding for Alpha graduation.

### Graduation Criteria

**Alpha**: feature gate added (disabled by default); unit/integration coverage above;
e2e coverage on a WAS-enabled kind cluster for plain Pods and at least one owned
integration.

**Beta**: feature gate enabled by default; coverage extended to the other in-tree
`GenericJob` integrations; positive feedback from a real WAS-integrated user/tooling
combination.

## Implementation History

- 2026-07-29: Initial KEP drafted from
  [kubernetes-sigs/kueue#13150](https://github.com/kubernetes-sigs/kueue/issues/13150)
  and its [design comment](https://github.com/kubernetes-sigs/kueue/issues/13150#issuecomment-5122519009).

## Drawbacks

Couples Kueue to an upstream KEP that is itself still alpha, via the shape of its JSON
fields — `pkg/util/wasapi` reads objects as unstructured data rather than vendoring
generated types, so this isn't a Go dependency in the usual sense, but Kueue's code still
silently breaks if a future upstream field rename isn't just a package rename, as
`v1alpha2` → `v1alpha3` was during this KEP's own implementation. The feature gate limits
the blast radius but doesn't eliminate the churn risk.

## Alternatives

- **Status quo**: keep requiring Kueue's own annotations exclusively, leaving
  translation to external tooling. Rejected — this is exactly the config-drift problem
  the feature exists to avoid.
- **A separate translation controller** that copies the standard fields into Kueue's
  annotations. Rejected as an extra moving part when Kueue can read the standard fields
  directly with a small, contained change.

## Future Work

This KEP's "owned workloads" path matches a `PodSet` against an *external* `Workload`
object linked via `controllerRef`. Not every WAS-integrated controller needs a separate
object for that — some embed the scheduling policy directly in their own spec instead.
Under the `WorkloadWithJob` feature gate (KEP-5547), `batch/v1` Job itself gains a
`spec.scheduling.schedulingPolicy.gang` field — see this
[gang-job example](https://github.com/kannon92/kubecon-eu-2025-demo/blob/main/examples/gang-job/scheduling/gang/02-gang-job.yaml):

```yaml
apiVersion: batch/v1
kind: Job
spec:
  parallelism: 4
  completions: 4
  completionMode: Indexed
  scheduling:
    schedulingPolicy:
      gang: {}
```

Here the gang size is implied by the Job's own `parallelism`/`completions` — there's no
separately-created `Workload` for Kueue to find via `controllerRef`; `WorkloadWithJob`
derives (and may create) the underlying `PodGroup` from the Job object itself. Kueue's
`pkg/controller/jobs/job` integration already derives its `PodSet` count from
`parallelism`/`completions`, so the values happen to agree without any code change — but
`spec.scheduling` being present is itself a signal Kueue isn't yet using (e.g. to
validate its own derived count against it, or to take the standard-API path even before
any live `Workload` object exists).

More generally, a workload controller can embed a scheduling/gang policy directly in its
own CRD instead of delegating to a standalone `Workload` object. Kueue can't generically
discover an arbitrary CRD's home-grown scheduling field the way it can discover a
standard `Workload`'s `controllerRef` — that requires an explicit, per-integration
change, the same way `pkg/controller/jobs/<framework>` already has framework-specific
code to read each one's replica/parallelism fields. This KEP does not attempt to solve
that generically; left as follow-up work:

- Extend `pkg/controller/jobs/job` to also recognize `batch/v1`'s own
  `spec.scheduling.schedulingPolicy.gang`, once `WorkloadWithJob` has moved past alpha,
  as the first concrete case of an *embedded* (rather than externally referenced)
  standard scheduling API.
- Revisit whether the shared helper this KEP introduces in `pkg/controller/jobframework`
  should grow a second strategy — "read the embedded scheduling policy off the owning
  object itself" — alongside the "look up an external `Workload` by `controllerRef`"
  strategy it implements today.
- For genuinely custom/third-party CRDs with bespoke scheduling fields, continue to
  require a dedicated Kueue integration (as today) rather than any generic mechanism.
- Migrate from `scheduling.k8s.io/v1alpha2` to whichever package eventually reaches
  beta/GA once this repo's other dependencies on `k8s.io/kubernetes` internals
  (`kubeflow/trainer`, `scheduler-library`) support the corresponding `k8s.io/api`
  version.

This keeps this KEP's alpha scope unchanged — unowned Pods via standalone `PodGroup`, and
owned workloads via an externally-referenced `Workload` object — while giving reviewers a
concrete plan for the case where a workload controller's own API is the source of truth
instead.
