# KEP-14190: Scheduler Configuration

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
  - [API](#api)
  - [Interaction with the feature gate](#interaction-with-the-feature-gate)
  - [Implementation](#implementation)
  - [Test Plan](#test-plan)
    - [Prerequisite testing updates](#prerequisite-testing-updates)
    - [Unit tests](#unit-tests)
    - [Integration tests](#integration-tests)
    - [e2e tests](#e2e-tests)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
  - [Bool field mirroring the feature gate](#bool-field-mirroring-the-feature-gate)
  - [Per Cohort or per ClusterQueue setting](#per-cohort-or-per-clusterqueue-setting)
  - [Graduate the feature gate to GA](#graduate-the-feature-gate-to-ga)
  - [Recommend fair sharing or admission fair sharing only](#recommend-fair-sharing-or-admission-fair-sharing-only)
  - [Keep the feature gate](#keep-the-feature-gate)
  - [Add the field to the <code>fairSharing</code> section](#add-the-field-to-the-fairsharing-section)
<!-- /toc -->

## Summary

Add an optional `scheduler` section to the Kueue Configuration API for
cluster-wide scheduler behavior. This KEP adds one field to it,
`crossClusterQueueOrdering`, which controls whether priority is used when
ordering workloads from different ClusterQueues in the same cohort. The field
replaces the `PrioritySortingWithinCohort` feature gate, which is deprecated and
then removed.

## Motivation

Kueue uses some feature gates as long-lived configuration knobs. Feature gates
are meant to be temporary, so this blocks their graduation and removal.

`PrioritySortingWithinCohort` (Beta, enabled by default since v0.6) is one of
them. It was added in [#1283](https://github.com/kubernetes-sigs/kueue/issues/1283)
so that teams sharing a cohort cannot get ahead of each other by raising
priorities. Graduating it to GA ([#9259](https://github.com/kubernetes-sigs/kueue/pull/9259))
was dropped because some users still disable it on clusters that do not use
fair sharing ([#9402](https://github.com/kubernetes-sigs/kueue/issues/9402),
[#7133](https://github.com/kubernetes-sigs/kueue/issues/7133)). In these
clusters each ClusterQueue belongs to a different team, and priorities are not
comparable across ClusterQueues.

More scheduler settings are expected
([#14190](https://github.com/kubernetes-sigs/kueue/issues/14190)), and they need
a place in the Configuration API.

### Goals

- Add a `scheduler` section to the Configuration API for cluster-wide scheduler
  behavior.
- Provide a permanent way to disable priority ordering across ClusterQueues in a
  cohort.
- Deprecate and remove the `PrioritySortingWithinCohort` feature gate without
  breaking users who disable it.

### Non-Goals

- Define other scheduler settings, such as the inadmissible workloads requeue
  interval ([#14190](https://github.com/kubernetes-sigs/kueue/issues/14190)),
  the fair sharing refill budget ([KEP-14596](/keps/14596-fair-sharing-refill)),
  or the number of workloads popped per ClusterQueue
  ([#14477](https://github.com/kubernetes-sigs/kueue/issues/14477)). They can be
  added to the `scheduler` section later.
- Per Cohort or per ClusterQueue ordering settings.
- Change the ordering of workloads within a single ClusterQueue.
- Move the Configuration from a ConfigMap to a CRD
  ([#5646](https://github.com/kubernetes-sigs/kueue/issues/5646)).

## Proposal

Add `scheduler.crossClusterQueueOrdering` with two values:

- `Priority` (default): the current behavior with the feature gate enabled.
- `Timestamp`: the current behavior with the feature gate disabled.

Deprecate the `PrioritySortingWithinCohort` feature gate in v0.21 and remove it
in v0.23.

### User Stories

#### Story 1

As an administrator of a cluster without fair sharing, where each team owns a
ClusterQueue in a shared cohort, I want workloads that compete for borrowed
quota to be considered in the order they were queued, regardless of the
priorities each team uses.

```yaml
apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
scheduler:
  crossClusterQueueOrdering: Timestamp
```

#### Story 2

As an administrator who disables the `PrioritySortingWithinCohort` feature gate
today, I want to upgrade Kueue without behavior changes and have time to move to
the configuration field.

### Notes/Constraints/Caveats

The field only affects the priority step of the ordering. The steps before it
are unchanged:

- without fair sharing: workloads with quota already reserved, preemptor
  workloads when `PrioritizePreemptorWorkloads` is enabled, and workloads that
  fit within nominal quota;
- with fair sharing: the fair sharing tournament based on dominant resource
  share.

With `Timestamp`, a workload preempted to reclaim quota borrowed in the cohort
(reason `InCohortReclaimWhileBorrowing`) is ordered after its preemptor. This
prevents the preemption loop described in
[#2821](https://github.com/kubernetes-sigs/kueue/issues/2821) and matches the
current behavior with the gate disabled.

### Risks and Mitigations

- Users who disable the gate may miss the deprecation. The gate keeps working
  for two releases, setting it logs a deprecation warning, and the release note
  includes an `ACTION REQUIRED` section.
- The `scheduler` section could collect settings that are not cluster-wide.
  Settings scoped to an integration, a Cohort or a ClusterQueue belong to those
  APIs, as with `scheduling.quotaReleaseStrategy`
  ([#13224](https://github.com/kubernetes-sigs/kueue/pull/13224), reverted in
  [#15371](https://github.com/kubernetes-sigs/kueue/pull/15371)).
- The Configuration is decoded in strict mode, so an older Kueue fails to start
  when the `scheduler` section is set. Users need to remove it before
  downgrading.

## Design Details

### API

```go
type Configuration struct {
	// ...

	// Scheduler provides configuration options for the Kueue scheduler.
	// +optional
	Scheduler *Scheduler `json:"scheduler,omitempty"`
}

// CrossClusterQueueOrdering defines how workloads from different
// ClusterQueues in the same cohort are ordered.
type CrossClusterQueueOrdering string

const (
	// CrossClusterQueueOrderingPriority orders workloads by priority, then by
	// the queue order timestamp.
	CrossClusterQueueOrderingPriority CrossClusterQueueOrdering = "Priority"

	// CrossClusterQueueOrderingTimestamp orders workloads by the queue order
	// timestamp, ignoring priority.
	CrossClusterQueueOrderingTimestamp CrossClusterQueueOrdering = "Timestamp"
)

type Scheduler struct {
	// CrossClusterQueueOrdering defines how workloads from different
	// ClusterQueues in the same cohort are ordered when they compete for quota.
	// Possible values are:
	// - Priority: higher priority workloads go first. Ties are broken by the
	//   queue order timestamp.
	// - Timestamp: priority is ignored, and workloads go in the order of their
	//   queue order timestamp. A workload preempted to reclaim quota borrowed in
	//   the cohort is ordered after its preemptor.
	// Ordering steps applied before priority, such as preferring workloads that
	// fit within nominal quota, or the dominant resource share when fair sharing
	// is enabled, are not affected.
	// Defaults to Priority.
	// +optional
	CrossClusterQueueOrdering *CrossClusterQueueOrdering `json:"crossClusterQueueOrdering,omitempty"`
}
```

Defaulting sets `crossClusterQueueOrdering` to `Priority`, so the effective value
is shown in the logged configuration. Validation rejects other values.

### Interaction with the feature gate

The effective ordering is computed at startup:

| `PrioritySortingWithinCohort` | `crossClusterQueueOrdering` | Effective ordering |
|-------------------------------|-----------------------------|--------------------|
| enabled (default)             | `Priority` (default)        | `Priority`         |
| enabled (default)             | `Timestamp`                 | `Timestamp`        |
| disabled                      | `Priority`                  | `Timestamp`        |
| disabled                      | `Timestamp`                 | `Timestamp`        |

Disabling either one selects `Timestamp`, so users who disable the gate keep
their behavior after upgrading.

Gate lifecycle:

- v0.21: the gate is marked `Deprecated` and stays enabled by default. Setting
  it logs a deprecation warning.
- v0.23: the gate is removed.

### Implementation

The effective value is stored in `workload.Ordering`, which already carries
`PodsReadyRequeuingTimestamp` from the Configuration and is used by the
scheduler, the queue manager and preemption. The code that reads the feature
gate today reads the value from there instead:

- the classical iterator in `pkg/scheduler/scheduler.go`;
- the fair sharing iterator in `pkg/scheduler/fair_sharing_iterator.go`;
- `Ordering.GetQueueOrderTimestamp` in `pkg/workload/workload.go`.

`cmd/kueue/main.go` computes the effective value and passes it to the scheduler
and the queue manager, in the same way as `PodsReadyRequeuingTimestamp`.

No new feature gate is added, because the field exposes behavior that has been
Beta since v0.6.

### Test Plan

[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.

#### Prerequisite testing updates

None.

#### Unit tests

- `apis/config/v1beta2`: defaulting of `scheduler.crossClusterQueueOrdering`.
- `pkg/config`: loading and validation of the `scheduler` section.
- `pkg/scheduler`: ordering cases that toggle the feature gate today use the
  field, plus the combinations from the table above.
- `pkg/workload`: `GetQueueOrderTimestamp` with both values.

#### Integration tests

- `test/integration/singlecluster/scheduler`: the scenarios for
  `borrowWithinCohort` with the gate disabled run with
  `crossClusterQueueOrdering: Timestamp`. One scenario keeps using the gate
  until it is removed.

#### e2e tests

Not needed. The behavior is internal to the Kueue scheduler and is covered by
integration tests.

### Graduation Criteria

The field is added to the v1beta2 Configuration API in v0.21 without a feature
gate.

The `PrioritySortingWithinCohort` feature gate is deprecated in v0.21 and
removed in v0.23.

## Implementation History

- 2026-10-06: KEP proposed.

## Drawbacks

Users who disable the gate need to update their configuration within two
releases.

## Alternatives

### Bool field mirroring the feature gate

`scheduler.prioritySortingWithinCohort: false` maps one to one to the gate. Not
chosen because the Kubernetes API conventions recommend enums over booleans,
which leaves room for more orderings later.

### Per Cohort or per ClusterQueue setting

Suggested in [#1283](https://github.com/kubernetes-sigs/kueue/issues/1283). Not
chosen because the gate has always been cluster-wide and no user asked for mixed
behavior. A more specific setting can be added later and take precedence over
the global one.

### Graduate the feature gate to GA

Attempted in [#9259](https://github.com/kubernetes-sigs/kueue/pull/9259). Not
chosen because it removes the option for users who cannot use fair sharing.

### Recommend fair sharing or admission fair sharing only

Fair sharing orders ClusterQueues by dominant resource share, which also stops
teams from using priority to get ahead. It remains the recommended setup, but
some users cannot enable it.

### Keep the feature gate

Not chosen because feature gates are temporary by design.

### Add the field to the `fairSharing` section

Not chosen because the setting mainly matters when fair sharing is disabled.
