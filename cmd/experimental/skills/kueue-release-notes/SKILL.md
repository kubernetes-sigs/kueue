---
name: kueue-release-notes
description: Write or review Kueue PR release notes. Use when a user asks to draft, refine, validate, or combine release notes for a Kueue pull request, especially when deciding whether the note should be framed as a bugfix, feature, observability improvement, or breaking change.
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

# Skill: Kueue Release Notes

Use this skill when writing or reviewing a Kueue release note for a pull request.

The goal is to produce a concise, professional release note describing the user-observable change rather than only the implementation detail.

## Critical rule for breaking changes

For breaking changes, include a mitigation section starting exactly with `ACTION REQUIRED:`.

Use this when users must change manifests, Helm values, CLI flags, configuration, workloads, namespace layout, RBAC, CRDs, or operational procedures before or during upgrade.

The mitigation must be specific and actionable. For installer-specific behavior, distinguish Helm and manifest-based installs explicitly.

Example:

```text
<Prefix>: <short description of changed behavior>.

ACTION REQUIRED: If <affected scenario>, <mitigation before upgrading>. <Optional caveat>.
```

## Process

### 1. Read the PR context

Read the PR description, linked issue, release-note text, and relevant changed files.

Identify:

- the affected Kueue area or component;
- whether this is a bugfix, feature, observability improvement, documentation-only change, cleanup, or breaking change;
- the scenario users could observe before the change;
- the behavior users should expect after the change;
- whether installation mode matters, for example Helm versus manifest-based installs;
- whether users need to take action before upgrading.

Do not rely only on the PR title. PR titles often describe the implementation, while release notes should describe the user-visible effect.

### 2. Choose a narrow prefix

Start the note with a prefix that narrows the scope.

Prefer the smallest accurate user-facing area, for example:

- `MultiKueue:`
- `TAS:`
- `Helm:`
- `KueueViz:`
- `Observability:`
- `Scheduling:`
- `VisibilityOnDemand:`
- `LeaderWorkerSet:`
- `ElasticJobsViaWorkloadSlices:`
- `<integration name>:` such as `JobSet:`, `StatefulSet:`, or `RayJob:`

Use a combined prefix when the change is intentionally scoped to two areas, for example:

- `LeaderWorkerSet & StatefulSet:`
- `Helm and manifests:`

Avoid broad prefixes such as `Kueue:` unless the change truly affects the whole project or no narrower area applies.

### 3. Focus on user-observable behavior

Describe what changes for users, operators, or integrators.

Good user-observable aspects include:

- workloads being admitted, blocked, evicted, requeued, or left inactive;
- queueing, quota, scheduling, or preemption behavior;
- webhook behavior;
- generated manifests or Helm chart behavior;
- API fields, feature gates, flags, configuration, and CRDs;
- logs, events, metrics, and visibility endpoints, because these are part of the observability surface;
- UI behavior in KueueViz.

Avoid release notes that only say what code changed, such as:

- “refactored locking”;
- “added helper function”;
- “changed timeout implementation”;
- “updated tests”;
- “renamed internal variable”.

If an implementation detail is important to understand the user-visible behavior, include it briefly after the scenario.

### 4. Frame bugfixes as bugfixes

For bugfixes, make the broken scenario clear.

Preferred shape:

```text
<Prefix>: Fixed a bug where <scenario> could <bad user-visible effect>.
```

Optional second sentence:

```text
Kueue now <brief fix or new behavior>.
```

Examples of good bugfix framing:

```text
MultiKueue: Fixed a bug where one slow or unresponsive remote cluster could stall reconciliation for other MultiKueueClusters, even when `controller.groupKindConcurrency["MultiKueueCluster.kueue.x-k8s.io"]` was set above 1. This could delay or block workload admission through other healthy clusters.
```

```text
KueueViz: Fixed the navigation bar to avoid layout breakage on narrow mobile screens.
```

Avoid starting with implementation details:

```text
MultiKueue: Changed remote watch establishment to use per-cluster locking.
```

That implementation detail can be included only if it explains the user-visible outcome.

### 5. Handle observability changes explicitly

Logs, metrics, events, and visibility endpoints are user-facing for operators.

Use `Observability:` when the main effect is improved debugging, reduced noise, corrected metrics, or clearer operational signals.

Examples:

```text
Observability: Fixed `kueue_cohort_subtree_quota` and `kueue_cohort_subtree_resource_reservations` to report CPU resources in CPU units instead of raw milliCPU values.
```

```text
Observability: Improved FairSharing strategy-evaluation logs by including DRS share values at verbosity level V(4).
```

### 6. Handle breaking changes

Follow the critical breaking-change rule near the top of this skill.

The mitigation must tell affected users exactly what to change before upgrading.

When behavior differs by installation method, explicitly distinguish manifest-based and Helm-based installations.

Example mitigation details may include:

- which field, flag, Helm value, or manifest entry to change;
- whether the change applies only to customized installations;
- whether a workaround is discouraged;
- what behavior users should expect after applying the mitigation.

### 7. Keep it concise and professional

Default to one bullet with one or two sentences.

Use:

- past tense for fixes: `Fixed`, `Improved`, `Removed`;
- active voice;
- code formatting for fields, flags, metrics, feature gates, resource names, and configuration keys;
- precise nouns such as `Workload`, `ClusterQueue`, `LocalQueue`, `MultiKueueCluster`, `AdmissionCheck`, `namespaceSelector`.

Avoid:

- vague correctness claims such as “fixed handling” without describing the expected behavior;
- “various” or “some”;
- excessive implementation details;
- blame-oriented wording;
- speculative impact;
- very long lists of changed files or internal functions.

### 8. Validate accuracy before finalizing

Before proposing the final note, check:

- Does the prefix describe the narrowest accurate scope?
- Does the note describe a user-visible change?
- For a bugfix, does it name the broken scenario?
- For observability, does it mention the log, metric, event, or endpoint users see?
- For breaking changes, does it include a specific mitigation?
- Are Helm and manifest-based installations distinguished when relevant?
- Are API fields, flags, metrics, and feature gates spelled exactly as in the code?
- Is the note concise enough to fit as a release bullet?

### 8. Final proposal variants and rationale

Always propose three variants:
- Details - the release note is very detailed, sometimes leaking implementation details
- Concise - the release note is only about user-observable impact
- Balanced - the release note is useually only about user-observable impact, but sometimes
  it may include key implementation decisions.

In all cases the wording is concise and professional.

Provide also rationale for the key decisions when formulating the release note.

## Templates

### Bugfix

```text
<Prefix>: Fixed a bug where <scenario> could <user-visible impact>.
```

### Bugfix with brief implementation note

```text
<Prefix>: Fixed a bug where <scenario> could <user-visible impact>. Kueue now <brief new behavior>.
```

### Feature or enhancement

```text
<Prefix>: Added <capability>, allowing users to <user-visible outcome>.
```

### Observability

```text
Observability: Improved <logs|metrics|events|visibility endpoint> by <specific change>, making it easier to <debug|monitor|understand user-visible behavior>.
```

### Breaking change

```text
<Prefix>: <short description of changed behavior>.

ACTION REQUIRED: If <affected scenario>, <mitigation before upgrading>. <Optional caveat>.
```

### Removal of deprecated option

```text
<Prefix>: Removed the deprecated `<flag-or-field>` option. Use `<replacement>` instead.

ACTION REQUIRED: If your installation uses `<flag-or-field>`, remove it before upgrading and configure `<replacement>`.
```
