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
  - [Copy all annotations with an exclusion list](#copy-all-annotations-with-an-exclusion-list)
  - [Extend metrics.customLabels instead of adding a field](#extend-metricscustomlabels-instead-of-adding-a-field)
  - [Add the reserved-domain check to labelKeysToCopy in this KEP](#add-the-reserved-domain-check-to-labelkeystocopy-in-this-kep)
  - [Enforce a per-value size cap or a denylist of bulky keys](#enforce-a-per-value-size-cap-or-a-denylist-of-bulky-keys)
<!-- /toc -->

## Summary

When Kueue creates a Workload for a job, it can copy a configured set of labels from the job object into the Workload (KEP-1834, `labelKeysToCopy`). This KEP adds the same mechanism for annotations: a new `integrations.annotationKeysToCopy` field in the Kueue Configuration lists the annotation keys to copy from the job object into the Workload at creation time. The mechanism applies to every integration, works without `CustomMetricLabels` (KEP-7066), and composes with the annotation sources that `CustomMetricLabels` already copies.

## Motivation

Workloads are Kueue's internal representation of jobs, so carrying selected job metadata on the Workload makes it possible to identify, filter, and debug Workloads without inspecting the owning job. Labels have had this since KEP-1834. Annotations are the other half of object metadata and typically hold what labels cannot: values longer than 63 characters, free-form text, and identifiers with characters that are invalid in label values, such as cost centers, ticket references, or ownership records.

Today the only way to copy an annotation into a Workload is to declare it as a `sourceAnnotationKey` under `metrics.customLabels` (KEP-7066). That ties a general need to a metrics feature behind the alpha `CustomMetricLabels` gate, and the copy itself is gated on that feature in the code. The copy mechanism already exists and is exercised by every integration. What is missing is a user-facing way to configure it that does not depend on `CustomMetricLabels`. The gap surfaced while fixing [#13277](https://github.com/kubernetes-sigs/kueue/issues/13277) in [#16187](https://github.com/kubernetes-sigs/kueue/pull/16187).

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

Add a field named `annotationKeysToCopy` to the Configuration API under `Integrations`, next to `labelKeysToCopy`. It holds a list of annotation keys. The configuration is global: it applies to every job framework. The list is empty by default, so existing installations are unaffected. Unless a section says otherwise, this KEP describes the behavior with the `AnnotationKeysToCopy` feature gate enabled, its default.

The semantics follow `labelKeysToCopy`:

- A key missing on the job object is skipped; the Workload is created without that annotation.
- Annotations are copied only when the Workload is created. Later changes to the job's annotations are not propagated.
- For pod groups, where one Workload is built from several Pods, every Pod that carries a listed key must have the same value. Pods that do not carry the key are ignored. If values differ, Workload creation fails with the existing annotation-mismatch error, exactly as labels do.

Two points deliberately differ from `labelKeysToCopy`:

- Each key is validated at configuration load as a qualified name, with the same syntax rules as `sourceAnnotationKey`.
- Keys in the reserved `kueue.x-k8s.io` domain, including subdomains such as `provreq.kueue.x-k8s.io`, are rejected. Kueue reads behavior-carrying annotations from Workloads, for example `kueue.x-k8s.io/admission-gated-by`, `kueue.x-k8s.io/wait-for-pods-ready`, `kueue.x-k8s.io/workload-slice-name`, and `kueue.x-k8s.io/job-owner-gvk`. A job annotation must not be able to reach those keys through a generic list. Kueue never copies a fixed list of its internal label and annotation keys, whatever the configuration lists, and the domain rule does not replace that behavior (see [Building the set of annotations to copy](#building-the-set-of-annotations-to-copy)). `labelKeysToCopy` is left unchanged because a retroactive check could break existing configurations; see [Alternatives](#alternatives).

Copying from `annotationKeysToCopy` does not depend on the `CustomMetricLabels` feature gate. When that gate is enabled, the effective set of copied annotations is the union of `annotationKeysToCopy` and the Workload-sourced `sourceAnnotationKey` entries in `metrics.customLabels`, with reserved keys removed. When it is disabled, the effective set is exactly `annotationKeysToCopy`. Reserved keys are removed rather than rejected for `sourceAnnotationKey`, which only gets a syntax check, so metrics that read an annotation Kueue itself writes on the Workload keep working.

### User Stories

#### Story 1

As a platform administrator, I annotate every batch job with `billing.example.com/cost-center`, whose values are free-form identifiers that do not fit label value rules. I set `integrations.annotationKeysToCopy: ["billing.example.com/cost-center"]` so chargeback reports can be produced from Workload listings alone, without resolving each Workload to its job.

#### Story 2

As a cluster operator, I already use `metrics.customLabels` with a `sourceAnnotationKey` to break down metrics by team. I want the same annotation on Workloads for debugging on clusters where the `CustomMetricLabels` gate is not enabled. I list the key in `annotationKeysToCopy`, and the Workload carries it with the `CustomMetricLabels` gate on or off.

### Notes/Constraints/Caveats

No collisions. Keys in the `kueue.x-k8s.io` domain and its subdomains never enter the copy set. They are rejected in `annotationKeysToCopy` and removed from the metric sources, so a copied annotation can never collide with an annotation Kueue writes. ProvisioningRequest annotations (`provreq.kueue.x-k8s.io/`) keep being copied by the existing prefix rule, which is separate from this copy set. Because they are reserved, they cannot also be listed.

Downgrade. The Configuration is decoded strictly, so a configuration that sets `annotationKeysToCopy` fails to load on a Kueue version without the field. Operators must remove the field before downgrading. The `AnnotationKeysToCopy` feature gate does not change this, because the older version does not know the field.

Change for `CustomMetricLabels` users. Kueue already drops its internal annotation keys from the metric sources. With this KEP and the `CustomMetricLabels` gate enabled, any other key in the reserved domain listed as a Workload-sourced `sourceAnnotationKey` is no longer copied from the job either. The release note of the implementation says so. Disabling the `AnnotationKeysToCopy` feature gate restores the earlier behavior.

### Risks and Mitigations

- Annotation size. Label values are limited to 63 characters, while annotations share a 256 KiB budget per object. A copied value is stored on the Workload, held in the scheduler cache, and replicated by MultiKueue. The operator chooses the keys, so bulky annotations such as `kubectl.kubernetes.io/last-applied-configuration` should not be listed. The documentation will say so. A per-value size cap is discussed under [Alternatives](#alternatives).
- Pod-group creation failures. A pod group with mismatching values for a listed key fails Workload creation. This only affects keys the operator explicitly listed and mirrors the label behavior. Kueue emits no event for a mismatch today: the reconcile returns the error and is retried, so the user sees Pods that stay gated and a line in the controller log. This KEP keeps that behavior so labels and annotations fail the same way. An event covering both can be added separately.
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
	// A key with uppercase letters in its prefix is rejected too, although Kubernetes accepts it on an object.
	// When the AnnotationKeysToCopy feature gate is disabled, the keys are still validated but not copied.
	// +optional
	AnnotationKeysToCopy []string `json:"annotationKeysToCopy,omitempty"`
}
```

### Validation

Kueue validates each entry of `annotationKeysToCopy` when it loads the configuration. It rejects these keys with an error at the field path `integrations.annotationKeysToCopy[i]`:

- A key that is not a valid qualified name. The syntax rules are the same as for `metrics.customLabels[].sourceAnnotationKey`. A key with uppercase letters in its prefix is rejected, although Kubernetes accepts it on an object.
- A key in the `kueue.x-k8s.io` domain or any of its subdomains. The same domain rule decides which keys are removed when the set is built.

Kueue runs these checks whether the `AnnotationKeysToCopy` feature gate is enabled or disabled, so a valid configuration stays valid when an admin enables the gate again.

### Building the set of annotations to copy

Kueue builds the set of annotation keys to copy once at startup and uses the same set for every integration. When the `CustomMetricLabels` gate is enabled, the set is the union of `annotationKeysToCopy` and the Workload-sourced `sourceAnnotationKey` entries in `metrics.customLabels`. When `CustomMetricLabels` is disabled, the set is exactly `annotationKeysToCopy`. In both cases, Kueue removes the keys in the `kueue.x-k8s.io` domain and its subdomains from the set.

Kueue logs the removed keys once at startup. A `CustomMetricLabels` user whose `sourceAnnotationKey` is no longer copied can find the key there (see [Notes/Constraints/Caveats](#notesconstraintscaveats)).

The domain rule does not replace the existing rule that Kueue never copies a fixed list of its internal label and annotation keys. The domain rule applies at validation and when the set is built, and the fixed list stays as a last guard. This KEP adds no domain rule for labels, so for labels the fixed list stays the only keys that are never copied.

### Copy path

Copying a key in the set does not depend on the `CustomMetricLabels` gate, which only decides whether the metric sources are added to the set. With `CustomMetricLabels` disabled and the field unset, the set is empty, so behavior is unchanged for installations that do not opt in.

The annotations are read from the object the Workload is built from, which differs per integration:

- Job and the other integrations not listed below: the metadata of the job object itself.
- Plain Pod: the metadata of the Pod.
- Pod group: the metadata of the Pods in the group, subject to the matching rule above.
- Deployment: Kueue creates no Workload for the Deployment itself. Each of its Pods gets its own Workload through the Pod integration, so a listed annotation has to be set in the Pod template (`spec.template.metadata.annotations`). An annotation on the Deployment's own metadata is not copied.
- StatefulSet and LeaderWorkerSet: the metadata of the StatefulSet or LeaderWorkerSet object, not its Pod template.

### Documentation

The "Replicate labels from Jobs into Workloads" section of the Workload concept page is extended to cover annotations, including the reserved-domain rule, the object that is read for each integration, and the size guidance. The Configuration reference is generated from the field comment.

### Test Plan

[x] I/we understand the owners of the involved components may require updates to existing tests to make this code solid enough prior to committing the changes necessary to implement this enhancement.

#### Prerequisite testing updates

Before this KEP, label copying for LeaderWorkerSet had no unit coverage. A unit case that copies a label listed in `labelKeysToCopy` is added first, so the annotation case has a baseline to mirror.

#### Unit tests

- Configuration validation: valid keys accepted; a key with invalid syntax, a key with uppercase letters in its prefix, a `kueue.x-k8s.io/` key, and a `provreq.kueue.x-k8s.io/` key rejected with the expected field path. With the `AnnotationKeysToCopy` gate disabled, valid keys are still accepted and a reserved key is still rejected.
- The set of annotations to copy: `CustomMetricLabels` off with the field set, `CustomMetricLabels` on with the field set and metric sources, `CustomMetricLabels` off with neither, `CustomMetricLabels` on with only metric sources, and a reserved `sourceAnnotationKey` being removed.
- The set of annotations to copy with the `AnnotationKeysToCopy` gate disabled: the field is ignored and logged once, and a reserved `sourceAnnotationKey` stays in the set.
- Workload construction from a job object with an empty set and with a populated set, with the `CustomMetricLabels` gate disabled.
- Pod-group construction with matching values, with mismatching values, and with some Pods lacking the key, with the `CustomMetricLabels` gate disabled.
- StatefulSet and LeaderWorkerSet: configured annotations are copied with the `CustomMetricLabels` gate disabled.

#### Integration tests

With `CustomMetricLabels` disabled and `annotationKeysToCopy` set, for each of Job, a pod group, StatefulSet, and LeaderWorkerSet: the Workload carries the listed annotations present on the job, does not carry unlisted ones, and a later change to the job's annotation does not reach the Workload. For a pod group, mismatching values prevent Workload creation. The Workload also does not carry a key that only a `sourceAnnotationKey` in `metrics.customLabels` names, or a key in the reserved domain.

#### e2e tests

Not needed. The behavior is confined to Workload metadata at creation time and is fully observable in integration tests.

### Graduation Criteria

Beta (v0.21):

- The `AnnotationKeysToCopy` feature gate is enabled by default.
- Validation, unit and integration tests, and documentation ship with the field.

GA:

- All reported bugs are fixed.
- The `AnnotationKeysToCopy` gate is locked to enabled.

## Implementation History

- 2026-09-25: Requested during the review of [#16187](https://github.com/kubernetes-sigs/kueue/pull/16187).
- 2026-09-26: Scope confirmed in the same thread as a user-facing, gate-independent `integrations.annotationKeysToCopy` analogous to `labelKeysToCopy` ([reply](https://github.com/kubernetes-sigs/kueue/pull/16187#discussion_r4110368374), acknowledged by the reviewer).
- 2026-10-01: Tracking issue [#16471](https://github.com/kubernetes-sigs/kueue/issues/16471) opened; first draft of this KEP.
- 2026-11-20: Implemented in [#19088](https://github.com/kubernetes-sigs/kueue/pull/19088). The review asked for a safety mechanism, so the implementation added the `AnnotationKeysToCopy` feature gate, Beta and enabled by default.

## Drawbacks

Some Workloads that are created today could fail to be created if they are based on a pod group with mismatched annotation values for a listed key. This only happens when an operator explicitly lists the key.

Copied values go stale if the job's annotation changes after the Workload exists, which may be confusing. This matches `labelKeysToCopy`.

Annotation values are larger than label values, so a poorly chosen key can inflate every Workload.

With `CustomMetricLabels` enabled, a reserved key listed as a Workload-sourced `sourceAnnotationKey` stops being copied from the job, which changes an alpha behavior.

## Alternatives

### Copy all annotations with an exclusion list

Rejected. Annotations are unbounded in number and size, and an allow list keeps reserved and bulky keys out by construction.

### Extend metrics.customLabels instead of adding a field

Rejected. It couples a general metadata need to a metrics feature and its feature gate, and it would require a metric label for every copied annotation.

### Add the reserved-domain check to labelKeysToCopy in this KEP

Deferred. Kueue already drops three internal label keys from the copy set (`kueue.x-k8s.io/multikueue-origin`, `kueue.x-k8s.io/concurrent-admission-parent`, and `kueue.x-k8s.io/job-uid`) and logs at startup when `labelKeysToCopy` names one of them. Any other `kueue.x-k8s.io/` label key in a configuration is still copied today, so rejecting the whole domain would need an upgrade note and is a separate change. It can be proposed as a follow-up.

### Enforce a per-value size cap or a denylist of bulky keys

Not included. The operator chooses the keys explicitly, and a cap would make copying partial and surprising. Documentation guidance is used instead. A cap can be added later if misuse is observed.
