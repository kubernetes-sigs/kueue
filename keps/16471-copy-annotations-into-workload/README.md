# KEP-16471: Copy annotations from jobs into the Workload object

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1](#story-1)
    - [Story 2](#story-2)
  - [Notes/Constraints/Caveats](#notesconstraintscaveats)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [API change](#api-change)
  - [Validation](#validation)
  - [Building the set of annotations to copy](#building-the-set-of-annotations-to-copy)
  - [Copy path](#copy-path)
  - [Documentation](#documentation)
  - [Test Plan](#test-plan)
    - [Prerequisite testing updates](#prerequisite-testing-updates)
    - [Unit tests](#unit-tests)
    - [Integration tests](#integration-tests)
    - [e2e tests](#e2e-tests)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
  - [Guard the field with a feature gate](#guard-the-field-with-a-feature-gate)
  - [Copy all annotations with an exclusion list](#copy-all-annotations-with-an-exclusion-list)
  - [Extend metrics.customLabels instead of adding a field](#extend-metricscustomlabels-instead-of-adding-a-field)
  - [Add the reserved-domain check to labelKeysToCopy in this KEP](#add-the-reserved-domain-check-to-labelkeystocopy-in-this-kep)
  - [Enforce a per-value size cap or a denylist of bulky keys](#enforce-a-per-value-size-cap-or-a-denylist-of-bulky-keys)
<!-- /toc -->

## Summary

When Kueue creates a Workload for a job, it can copy a configured set of labels from the job object into the Workload (KEP-1834, `labelKeysToCopy`). This KEP adds the same mechanism for annotations: a new `integrations.annotationKeysToCopy` field in the Kueue Configuration lists the annotation keys to copy from the job object into the Workload at creation time. The mechanism applies to every integration, does not depend on any feature gate, and composes with the annotation sources that `CustomMetricLabels` (KEP-7066) already copies.

## Motivation

Workloads are Kueue's internal representation of jobs, so carrying selected job metadata on the Workload makes it possible to identify, filter, and debug Workloads without inspecting the owning job. Labels have had this since KEP-1834. Annotations are the other half of object metadata and typically hold what labels cannot: values longer than 63 characters, free-form text, and identifiers with characters that are invalid in label values, such as cost centers, ticket references, or ownership records.

Today the only way to copy an annotation into a Workload is to declare it as a `sourceAnnotationKey` under `metrics.customLabels` (KEP-7066). That ties a general need to a metrics feature behind the alpha `CustomMetricLabels` gate, and the copy itself is gated on that feature in the code. The copy mechanism already exists and is exercised by every integration except StatefulSet, which [#16187](https://github.com/kubernetes-sigs/kueue/pull/16187) fixes; what is missing is a user-facing, gate-independent way to configure it. The gap surfaced while fixing [#13277](https://github.com/kubernetes-sigs/kueue/issues/13277) in [#16187](https://github.com/kubernetes-sigs/kueue/pull/16187).

### Goals

- Add a Configuration field that lists annotation keys to copy from the job object into the Workload when the Workload is created.
- Apply it uniformly to all integrations, including pod groups, LeaderWorkerSet, and StatefulSet.
- Keep the annotation copying driven by `CustomMetricLabels` working unchanged, except that keys reserved by Kueue are no longer copied.
- Validate the configured keys and reject keys reserved by Kueue.

### Non-Goals

- Copying all annotations, or an exclusion-list model.
- Updating annotations on an existing Workload when the job's annotations change after creation.
- Changing the semantics of `labelKeysToCopy`, or adding validation to it.
- Changing which annotations `CustomMetricLabels` reads or how its metrics are reported.

## Proposal

Add a field named `annotationKeysToCopy` to the Configuration API under `Integrations`, next to `labelKeysToCopy`. It holds a list of annotation keys. The configuration is global: it applies to every job framework. The list is empty by default, so existing installations are unaffected.

The semantics follow `labelKeysToCopy`:

- A key missing on the job object is skipped; the Workload is created without that annotation.
- Annotations are copied only when the Workload is created. Later changes to the job's annotations are not propagated.
- For pod groups, where one Workload is built from several Pods, every Pod that carries a listed key must have the same value. Pods that do not carry the key are ignored. If values differ, Workload creation fails with the existing annotation-mismatch error, exactly as labels do.

Two points deliberately differ from `labelKeysToCopy`:

- Each key is validated at configuration load as a qualified annotation key, using the same validator that `sourceAnnotationKey` already goes through.
- Keys in the reserved `kueue.x-k8s.io` domain, including subdomains such as `provreq.kueue.x-k8s.io`, are rejected. Kueue reads behavior-carrying annotations from Workloads, for example `kueue.x-k8s.io/admission-gated-by`, `kueue.x-k8s.io/wait-for-pods-ready`, `kueue.x-k8s.io/workload-slice-name`, and `kueue.x-k8s.io/job-owner-gvk`. A job annotation must not be able to reach those keys through a generic list. `labelKeysToCopy` is left unchanged because a retroactive check could break existing configurations; see [Alternatives](#alternatives).

Copying from `annotationKeysToCopy` does not depend on the `CustomMetricLabels` feature gate. When that gate is enabled, the effective set of copied annotations is the union of `annotationKeysToCopy` and the Workload-sourced `sourceAnnotationKey` entries in `metrics.customLabels`, with reserved keys removed. When it is disabled, the effective set is exactly `annotationKeysToCopy`. Reserved keys are removed rather than rejected for `sourceAnnotationKey`, which only gets a syntax check, so metrics that read an annotation Kueue itself writes on the Workload keep working.

### User Stories

#### Story 1

As a platform administrator, I annotate every batch job with `billing.example.com/cost-center`, whose values are free-form identifiers that do not fit label value rules. I set `integrations.annotationKeysToCopy: ["billing.example.com/cost-center"]` so chargeback reports can be produced from Workload listings alone, without resolving each Workload to its job.

#### Story 2

As a cluster operator, I already use `metrics.customLabels` with a `sourceAnnotationKey` to break down metrics by team. I want the same annotation on Workloads for debugging on clusters where the `CustomMetricLabels` gate is not enabled. I list the key in `annotationKeysToCopy`, and the Workload carries it with the gate on or off.

### Notes/Constraints/Caveats

Precedence. For a single job object, annotations are copied before Kueue writes its own, such as `kueue.x-k8s.io/job-owner-gvk`; for a pod group, they are copied after `kueue.x-k8s.io/is-group-workload` and `kueue.x-k8s.io/admission-gated-by` are written. Neither order is relied on: reserved keys are removed from the copy set whatever their source, so no copied key can collide with an annotation Kueue manages. ProvisioningRequest annotations (`provreq.kueue.x-k8s.io/`) keep being copied by the existing prefix rule; since they are in the reserved domain, they cannot also be listed.

Downgrade. The Configuration is decoded strictly, so a configuration that sets `annotationKeysToCopy` fails to load on a Kueue version without the field. Operators must remove the field before downgrading. A feature gate would not change this, since the field would still be unknown to the older version.

Change for `CustomMetricLabels` users. With the gate enabled, a reserved key listed as a Workload-sourced `sourceAnnotationKey` is no longer copied from the job. The implementation PR's release note will say so.

### Risks and Mitigations

- Annotation size. Label values are limited to 63 characters, while annotations share a 256 KiB budget per object. A copied value is stored on the Workload, held in the scheduler cache, served by the visibility API, and replicated by MultiKueue. The operator chooses the keys, so bulky annotations such as `kubectl.kubernetes.io/last-applied-configuration` should not be listed. The documentation will say so. A per-value size cap is discussed under [Alternatives](#alternatives).
- Pod-group creation failures. A pod group with mismatching values for a listed key fails Workload creation. This only affects keys the operator explicitly listed, mirrors the label behavior, and surfaces through the existing error and event.
- Reserved keys. Rejected in `annotationKeysToCopy` and removed from the copy set for every source, so they never reach the copy path.

## Design Details

### API change

Add `AnnotationKeysToCopy` to the `Integrations` struct of the v1beta2 Configuration API:

```go
type Integrations struct {
	...
	LabelKeysToCopy []string `json:"labelKeysToCopy,omitempty"`

	// annotationKeysToCopy is a list of annotation keys that should be copied from the
	// job into the workload object. It is not required for the job to have all the
	// annotations from this list. If a job does not have some annotation with the given
	// key from this list, the constructed workload object will be created without this
	// annotation. In the case of creating a workload from a composable job (pod group),
	// if multiple objects have annotations with some key from the list, the values of
	// these annotations must match or otherwise the workload creation would fail. The
	// annotations are copied only during the workload creation and are not updated even
	// if the annotations of the underlying job are changed. Keys in the
	// kueue.x-k8s.io domain and its subdomains are reserved and rejected at configuration validation.
	AnnotationKeysToCopy []string `json:"annotationKeysToCopy,omitempty"`
}
```

The field is added to v1beta2 only. The v1beta1 API keeps its current shape, and a manual `Convert_v1beta2_Integrations_To_v1beta1_Integrations` function in `apis/config/v1beta1/configuration_conversion.go` drops the field, in the same way the existing manual conversion ignores the deprecated `PodOptions` field in the other direction.

### Validation

`validateIntegrations` in `pkg/config/validation.go` gains a check for each entry of `annotationKeysToCopy` at the field path `integrations.annotationKeysToCopy[i]`:

- the key must be a valid qualified name, using the `validateLabelKey` helper that `metrics.customLabels[].sourceAnnotationKey` already uses;
- the key must not be in the `kueue.x-k8s.io` domain or any of its subdomains, checked by the same helper that removes reserved keys when the set is built.

### Building the set of annotations to copy

`cmd/kueue/main.go` builds the sets of label and annotation keys once and passes them to every integration through `jobframework.WithLabelKeysToCopy` and `jobframework.WithAnnotationsToCopy`. Today the annotation set is empty unless `CustomMetricLabels` is enabled:

```go
func getLabelsAndAnnotationsToCopy(cfg *configapi.Configuration) (labelKeysToCopy, annotationsToCopy sets.Set[string]) {
	if !features.Enabled(features.CustomMetricLabels) {
		return sets.New(cfg.Integrations.LabelKeysToCopy...), sets.New[string]()
	}
	labelKeysToCopy, annotationsToCopy = metrics.WorkloadCustomLabelSources(cfg.Metrics.CustomLabels)
	labelKeysToCopy.Insert(cfg.Integrations.LabelKeysToCopy...)
	return
}
```

The set builder and the reserved-key helper move from `cmd/kueue/main.go` to `pkg/controller/jobframework`, which `pkg/config` already imports, so validation and set building share the helper. It becomes (names are illustrative):

```go
func KeysToCopy(cfg *configapi.Configuration) (labelKeysToCopy, annotationsToCopy sets.Set[string]) {
	labelKeysToCopy = sets.New(cfg.Integrations.LabelKeysToCopy...)
	annotationsToCopy = sets.New(cfg.Integrations.AnnotationKeysToCopy...)
	if features.Enabled(features.CustomMetricLabels) {
		metricLabels, metricAnnotations := metrics.WorkloadCustomLabelSources(cfg.Metrics.CustomLabels)
		labelKeysToCopy = labelKeysToCopy.Union(metricLabels)
		annotationsToCopy = annotationsToCopy.Union(metricAnnotations)
	}
	for key := range annotationsToCopy {
		if IsReservedAnnotationKey(key) {
			annotationsToCopy.Delete(key)
		}
	}
	return labelKeysToCopy, annotationsToCopy
}
```

Any key removed this way is logged once at startup, so an operator affected by the change can see it.

### Copy path

Two places currently check the `CustomMetricLabels` gate before copying annotations: `jobframework.NewWorkload` in `pkg/controller/jobframework/utils.go`, and the pod-group path in `pkg/controller/jobs/pod/pod_controller.go` that collects annotation values across the Pods of a group. Both checks are removed. The copy path copies whatever set it receives; with the gate off and the field unset the set is empty, so behavior is unchanged for installations that do not opt in. The gate is applied only where the metric sources are merged, as shown above.

No integration-specific change is needed once [#16187](https://github.com/kubernetes-sigs/kueue/pull/16187) merges, which is a prerequisite. The Job reconciler, the Pod controller, and the LeaderWorkerSet reconciler already receive and pass the annotation set; on main, the StatefulSet reconciler still passes `nil`, and #16187 fixes that.

### Documentation

The "Replicate labels from Jobs into Workloads" section of the Workload concept page is extended to cover annotations, including the reserved-domain rule and the size guidance. The Configuration reference is generated from the field comment.

### Test Plan

[x] I/we understand the owners of the involved components may require updates to existing tests to make this code solid enough prior to committing the changes necessary to implement this enhancement.

#### Prerequisite testing updates

[#16187](https://github.com/kubernetes-sigs/kueue/pull/16187) is merged first, so the StatefulSet reconciler passes the sets.

The LeaderWorkerSet reconciler unit test declares a `labelKeysToCopy` option but no case sets it, so label copying for LeaderWorkerSet has no unit coverage. A case for `labelKeysToCopy` is added first, so the annotation case has a baseline to mirror.

#### Unit tests

- Configuration validation: valid keys accepted; a key with invalid syntax, a `kueue.x-k8s.io/` key, and a `provreq.kueue.x-k8s.io/` key rejected with the expected field path.
- `KeysToCopy`: gate off with the field set, gate on with the field set and metric sources, gate off with neither, gate on with only metric sources, and a reserved `sourceAnnotationKey` being removed.
- `jobframework.NewWorkload` with an empty set and with a populated set, with the `CustomMetricLabels` gate disabled.
- Pod-group construction with matching values, with mismatching values, and with some Pods lacking the key, with the gate disabled.
- StatefulSet and LeaderWorkerSet reconcilers: configured annotations are copied with the gate disabled. The gate-off cases added for StatefulSet in [#16187](https://github.com/kubernetes-sigs/kueue/pull/16187), which assert that annotations are not copied, are updated: once the gate is applied where the set is built, a reconciler handed a non-empty set copies it regardless of the gate.

The existing tests that pin today's gate-off behavior are updated as well: the `TestReconciler` cases "workload is created without annotations for pod group when CustomMetricLabels is disabled" and "annotations mismatch is ignored for pod group when CustomMetricLabels is disabled" in `pkg/controller/jobs/pod`, and the integration spec "Should not copy annotations when the feature gate is disabled". In the mismatch case, the outcome changes from ignoring the mismatch to failing Workload creation.

#### Integration tests

With `CustomMetricLabels` disabled and `annotationKeysToCopy` set, for each of Job, a pod group, StatefulSet, and LeaderWorkerSet: the Workload carries the listed annotations present on the job, does not carry unlisted ones, and a later change to the job's annotation does not reach the Workload. For a pod group, mismatching values prevent Workload creation. The test setup builds the sets from a `Configuration` with `KeysToCopy` instead of injecting them, so the gate merge and the removal of reserved keys are covered too.

#### e2e tests

Not needed. The behavior is confined to Workload metadata at creation time and is fully observable in integration tests.

### Graduation Criteria

The feature has no feature gate, following `labelKeysToCopy`. It is considered beta when it ships with validation, tests, and documentation in v0.21, and stable after one release without behavior changes, consistent with the history of KEP-1834.

## Implementation History

- 2026-09-25: Requested during the review of [#16187](https://github.com/kubernetes-sigs/kueue/pull/16187).
- 2026-09-26: Scope confirmed in the same thread as a user-facing, gate-independent `integrations.annotationKeysToCopy` analogous to `labelKeysToCopy` ([reply](https://github.com/kubernetes-sigs/kueue/pull/16187#discussion_r4110368374), acknowledged by the reviewer).
- 2026-10-01: Tracking issue [#16471](https://github.com/kubernetes-sigs/kueue/issues/16471) opened; first draft of this KEP.

## Drawbacks

Some Workloads that are created today could fail to be created if they are based on a pod group with mismatched annotation values for a listed key. This only happens when an operator explicitly lists the key.

Copied values go stale if the job's annotation changes after the Workload exists, which may be confusing. This matches `labelKeysToCopy`.

Annotation values are larger than label values, so a poorly chosen key can inflate every Workload.

With `CustomMetricLabels` enabled, a reserved key listed as a Workload-sourced `sourceAnnotationKey` stops being copied from the job, which changes an alpha behavior.

## Alternatives

### Guard the field with a feature gate

A new alpha gate was considered and rejected. The field is the opt-in: an empty list changes nothing, so a gate would be a second switch guarding the same behavior, and both require a restart to flip. The change is confined to Workload metadata at creation time. A gate does not help the downgrade story, because the Configuration is decoded strictly and the field is unknown to older versions either way. `labelKeysToCopy` shipped without a gate and set the precedent. If reviewers prefer a gate, adding one does not change the design.

### Copy all annotations with an exclusion list

Rejected. Annotations are unbounded in number and size, and an allow list keeps reserved and bulky keys out by construction.

### Extend metrics.customLabels instead of adding a field

Rejected. It couples a general metadata need to a metrics feature and its feature gate, and it would require a metric label for every copied annotation.

### Add the reserved-domain check to labelKeysToCopy in this KEP

Deferred. A configuration that lists a `kueue.x-k8s.io/` label key works today, so adding the check would need an upgrade note and is a separate change. It can be proposed as a follow-up.

### Enforce a per-value size cap or a denylist of bulky keys

Not included. The operator chooses the keys explicitly, and a cap would make copying partial and surprising. Documentation guidance is used instead. A cap can be added later if misuse is observed.
