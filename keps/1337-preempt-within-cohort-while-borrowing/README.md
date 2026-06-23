# KEP-1337: Preempt within cohort while borrowing

<!--
This is the title of your KEP. Keep it short, simple, and descriptive. A good
title can help communicate what the KEP is and should be considered as part of
any review.
-->

<!--
A table of contents is helpful for quickly jumping to sections of a KEP and for
highlighting any additional information provided beyond the standard KEP
template.

Ensure the TOC is wrapped with
  <code>&lt;!-- toc --&rt;&lt;!-- /toc --&rt;</code>
tags, and then generate with `hack/update-toc.sh`.
-->

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories (Optional)](#user-stories-optional)
    - [Story 1](#story-1)
  - [Notes/Constraints/Caveats (Optional)](#notesconstraintscaveats-optional)
  - [Risks and Mitigations](#risks-and-mitigations)
    - [Flopping between preempting pair of workloads](#flopping-between-preempting-pair-of-workloads)
- [Design Details](#design-details)
  - [API](#api)
  - [Implementation overview](#implementation-overview)
  - [Test Plan](#test-plan)
      - [Prerequisite testing updates](#prerequisite-testing-updates)
    - [Unit Tests](#unit-tests)
    - [Integration tests](#integration-tests)
  - [Graduation Criteria](#graduation-criteria)
    - [Beta](#beta)
    - [GA](#ga)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
  - [Reuse PreemptionPolicy enum](#reuse-preemptionpolicy-enum)
  - [Minimal API](#minimal-api)
  - [ReclaimWithinCohortConfig](#reclaimwithincohortconfig)
  - [Move ReclaimWithinCohort under ReclaimWithinCohortConfig](#move-reclaimwithincohort-under-reclaimwithincohortconfig)
<!-- /toc -->

## Summary

This KEP introduces ClusterQueue-level API to support workload preemption
to reclaim quota within cohort for a workload that requires borrowing.

<!--
This section is incredibly important for producing high-quality, user-focused
documentation such as release notes or a development roadmap. It should be
possible to collect this information before implementation begins, in order to
avoid requiring implementors to split their attention between writing release
notes and implementing the feature itself. KEP editors and SIG Docs
should help to ensure that the tone and content of the `Summary` section is
useful for a wide audience.

A good summary is probably at least a paragraph in length.

Both in this section and below, follow the guidelines of the [documentation
style guide]. In particular, wrap lines to a reasonable length, to make it
easier for reviewers to cite specific portions, and to minimize diff churn on
updates.

[documentation style guide]: https://github.com/kubernetes/community/blob/master/contributors/guide/style-guide.md
-->

## Motivation

Currently, there is no support for preemption to reclaim quota within cohort
for workloads which require borrowing. This is too constraining for some setups.

<!--
This section is for explicitly listing the motivation, goals, and non-goals of
this KEP.  Describe why the change is important and the benefits to users. The
motivation section can optionally provide links to [experience reports] to
demonstrate the interest in a KEP within the wider Kubernetes community.

[experience reports]: https://github.com/golang/go/wiki/ExperienceReports
-->

### Goals

- support preemption to reclaim quota within cohort for workloads that require borrowing.
- support preemption within cluster queue for workloads that require borrowing.
- modify the workload ordering by scheduler to take the need to preempt into account.

<!--
List the specific goals of the KEP. What is it trying to achieve? How will we
know that this has succeeded?
-->

### Non-Goals

- allow preemption of workloads, from other ClusterQueues, which are not borrowing.

<!--
What is out of scope for this KEP? Listing non-goals helps to focus discussion
and make progress.
-->

## Proposal

<!--
This is where we get down to the specifics of what the proposal actually is.
This should have enough detail that reviewers can understand exactly what
you're proposing, but should not include things like API designs or
implementation. What is the desired outcome and how do we measure success?.
The "Design Details" section below is for the real
nitty-gritty.
-->

Introduce a new ClusterLevel API to support preemption while borrowing. The
API proposes additional configuration to the `.preemption.reclaimWithinCohort`
option.

### User Stories (Optional)

<!--
Detail the things that people will be able to do if this KEP is implemented.
Include as much detail as possible so that people can understand the "how" of
the system. The goal here is to make this feel real for users without getting
bogged down.
-->

#### Story 1

As an administrator of Kueue in my organization I manage the following setup
with two teams, `A` and `B`. There is a single cohort, and both teams have a
pair of ClusterQueues in the cohort, called `Standard` and `Best-Effort`. There
is also one ClusterQueue, called `Shared`, which is shared between the teams.

All the `Standard` and `Best-Effort` ClusterQueues have `nominalQuota=0`, and
`borrowingLimit>0`. The actual quota is exposed by the `Shared` ClusterQueue.
The `Shared` ClusterQueue has `nominalQuota>0`, and `borrowingLimit=0`. This
setup is depicted by the table:

|                 | A Standard    | A Best-Effort | B Standard    | B Best-Effort | Shared        |
|-----------------|---------------|---------------|---------------|---------------|---------------|
|nominalQuota     | 0             | 0             | 0             | 0             | 100           |
|borrowingLimit   | 100           | 100           | 100           | 100           | 0             |

The teams send their high-priority (say `>200`) jobs to their `Standard`
ClusterQueue, and the low-priority jobs (say `<100`) to their `Best-Effort`
ClusterQueue. Jobs sent to the `Standard` ClusterQueues should not be preempted.
Jobs in the `Best-Effort` ClusterQueues can be preempted with respect to
priorities.

Note that in this setup every workload is borrowing, so I need to make preemption
while borrowing enabled. Additionally, I need a mechanism to say that workloads
from the `Standard` ClusterQueues are not preempted. A mechanism to only preempt
jobs with priorities below a specified threshold will suffice.

### Notes/Constraints/Caveats (Optional)

<!--
What are the caveats to the proposal?
What are some important details that didn't come across above?
Go in to as much detail as necessary here.
This might be a good place to talk about core concepts and how they relate.
-->

### Risks and Mitigations

#### Flopping between preempting pair of workloads

In a setup as described in (Story 1)[#story-1] consider there are two workloads,
one in ClusterQueue A, and one in ClusterQueue B. There is not enough resources
in the cohort to run both workloads at the same time.

If the preemption is allowed for workloads of any priorities
(as when `.preemption.reclaimWithinCohort` equals `Any`)
then the following flopping happens: workload A preempts workload B, then
preempted workload B returns to the queue and preempts workload A to make progress.
This repeats, and none of the workloads can make any progress.

In order to mitigate this risk we only enable the preemption to reclaim
quota within cohort, for workloads requiring borrowing when such preempted
workloads are of lower priority. This behavior is enabled by setting
`.preemption.borrowWithinCohort.policy: LowerPriority`.

<!--
What are the risks of this proposal, and how do we mitigate? Think broadly.
For example, consider both security and how this will impact the larger
Kubernetes ecosystem.

How will security be reviewed, and by whom?

How will UX be reviewed, and by whom?

Consider including folks who also work outside the SIG or subproject.
-->

## Design Details

### API

```golang
// ClusterQueuePreemption contains policies to preempt Workloads from this
// ClusterQueue or the ClusterQueue's cohort.
type ClusterQueuePreemption struct {
  ...
	// borrowWithinCohort provides configuration to allow preemption within
	// cohort while borrowing. This configuration can only be enabled when
	// reclaimWithinCohort is not `Never`.
	BorrowWithinCohort *BorrowWithinCohort `json:"borrowWithinCohort,omitempty"`
  ...
}

type BorrowWithinCohortPolicy string

const (
	BorrowWithinCohortPolicyNever         BorrowWithinCohortPolicy = "Never"
	BorrowWithinCohortPolicyLowerPriority BorrowWithinCohortPolicy = "LowerPriority"
)

// BorrowWithinCohort contains configuration which allows to preempt workloads
// within cohort while borrowing.
type BorrowWithinCohort struct {
	// policy determines the policy for preemption to reclaim quota within cohort while borrowing.
	// Possible values are:
	// - `Never` (default): do not allow for preemption, in other
	//    ClusterQueues within the cohort, for a borrowing workload.
	// - `LowerPriority`: allow preemption, in other ClusterQueues
	//    within the cohort, for a borrowing workload, but only if
	//    the preempted workloads are of lower priority.
	//
	// +kubebuilder:default=Never
	// +kubebuilder:validation:Enum=Never;LowerPriority
	Policy BorrowWithinCohortPolicy `json:"policy,omitempty"`

	// maxPriorityThreshold allows to restrict the set of workloads which
	// might be preempted by a borrowing workload, to only workloads with
	// priority below or equal the specified level.
	//
	// +optional
	MaxPriorityThreshold *int32 `json:"maxPriorityThreshold,omitempty"`
}
```

### Implementation overview

There are main modifications:
- in the [`fitsResourceQuota`](https://github.com/kubernetes-sigs/kueue/blob/main/pkg/scheduler/flavorassigner/flavorassigner.go#L551)
  we return `FlavorAssignmentMode=Preempt` when the workload does not fit, but
  there is enough quota within the cohort
- modify the [`GetTargets`](https://github.com/kubernetes-sigs/kueue/blob/53d4b657655aff5df3748e65b4226cf11e50eeba/pkg/scheduler/preemption/preemption.go#L77)
and the [`minimalPreemptions`](https://github.com/kubernetes-sigs/kueue/blob/53d4b657655aff5df3748e65b4226cf11e50eeba/pkg/scheduler/preemption/preemption.go#L159) functions
to only, if borrowing, include candidates which satisfy the new API's constrains.

Additionally, we add a webhook validation in `clusterqueue_webhook` to
verify that `.preemption.reclaimWithinCohort` is not `Never`, when
`.preemption.borrowWithinCohort` is enabled.

<!--
This section should contain enough information that the specifics of your
change are understandable. This may include API specs (though not always
required) or even code snippets. If there's any ambiguity about HOW your
proposal will be implemented, this is the place to discuss them.
-->

### Test Plan

<!--
**Note:** *Not required until targeted at a release.*
The goal is to ensure that we don't accept enhancements with inadequate testing.

All code is expected to have adequate tests (eventually with coverage
expectations). Please adhere to the [Kubernetes testing guidelines][testing-guidelines]
when drafting this test plan.

[testing-guidelines]: https://git.k8s.io/community/contributors/devel/sig-testing/testing.md
-->

[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.

##### Prerequisite testing updates

<!--
Based on reviewers feedback describe what additional tests need to be added prior
implementing this enhancement to ensure the enhancements have also solid foundations.
-->

#### Unit Tests

<!--
In principle every added code should have complete unit test coverage, so providing
the exact set of tests will not bring additional value.
However, if complete unit test coverage is not possible, explain the reason of it
together with explanation why this is acceptable.
-->

<!--
Additionally, try to enumerate the core package you will be touching
to implement this enhancement and provide the current unit coverage for those
in the form of:
- <package>: <date> - <current test coverage>

This can inform certain test coverage improvements that we want to do before
extending the production code to implement this enhancement.
-->

- `pkg/scheduler/flavorassigner`: `7 Dec 2023` - `79.2%` (to indicate the workload which needs to borrow, that it can also preempt)
- `pkg/scheduler/preemption`: `7 Dec 2023` - `94%` (to update the `GetTargets` function, identifying the set of workloads to preempt)

#### Integration tests

We are going to cover the following scenarios:
- in the setup described in the [Story 1](#story-1), verify:
  - a workload which require borrowing can preempt a lower priority workload
  - a workload which require borrowing cannot preempt a higher priority workload
  - a workload which require borrowing cannot preempt a lower priority (but above the threshold) workload.
  - a workload which require borrowing cannot preempt a lower priority workload if the workload isn't borrowing.

More scenarios can be considered based on the actual implementation.

<!--
Describe what tests will be added to ensure proper quality of the enhancement.

After the implementation PR is merged, add the names of the tests here.
-->

### Graduation Criteria

<!--

Clearly define what it means for the feature to be implemented and
considered stable.

If the feature you are introducing has high complexity, consider adding graduation
milestones with these graduation criteria:
- [Maturity levels (`alpha`, `beta`, `stable`)][maturity-levels]
- [Feature gate][feature gate] lifecycle
- [Deprecation policy][deprecation-policy]

[feature gate]: https://git.k8s.io/community/contributors/devel/sig-architecture/feature-gates.md
[maturity-levels]: https://git.k8s.io/community/contributors/devel/sig-architecture/api_changes.md#alpha-beta-and-stable-versions
[deprecation-policy]: https://kubernetes.io/docs/reference/using-api/deprecation-policy/
-->

#### Beta

- the feature is implemented and documented as opt-in

#### GA

- all reported bugs are fixed

## Implementation History

<!--
Major milestones in the lifecycle of a KEP should be tracked in this section.
Major milestones might include:
- the `Summary` and `Motivation` sections being merged, signaling SIG acceptance
- the `Proposal` section being merged, signaling agreement on a proposed design
- the date implementation started
- the first Kubernetes release where an initial version of the KEP was available
- the version of Kubernetes where the KEP graduated to general availability
- when the KEP was retired or superseded
-->

## Drawbacks

Increased complexity of the preemption logic in Kueue, which is already complex.

<!--
Why should this KEP _not_ be implemented?
-->

## Alternatives

### Reuse PreemptionPolicy enum

```golang
type ClusterQueuePreemption struct {
  ...
	ReclaimWithinCohortConfig *ReclaimWithinCohortConfig `json:"reclaimWithCohort,omitempty"`
  ...
}

type ReclaimWithinCohortConfig struct {
	// +kubebuilder:default=Never
	// +kubebuilder:validation:Enum=Never;LowerPriority
	WhileBorrowingPolicy PreemptionPolicy `json:"whileBorrowingPolicy,omitempty"`

	// +optional
	PriorityThreshold *int32 `json:"priorityThreshold,omitempty"`
}
```

**Reasons for discarding/deferring**

The existing type has some extra options, such as `Any` which would need to be
rejected by validation, this is not ideal from user's perspective.

### Minimal API

Since we only have one policy `LowerPriority`, the minimal API would be just
to have a boolean toggle with the `Enable` field. For example:

```golang
type ReclaimWithinCohortConfig struct {
	// +kubebuilder:default=false
	EnableWhileBorrowing bool `json:"enableWhileBorrowing,omitempty"`

	// +optional
	PriorityThreshold *int32 `json:"priorityThreshold,omitempty"`
}
```

Pros:
- simpler, yet provides the same functionality

**Reasons for discarding/deferring**

The `EnableWhileBorrowing` field might become redundant if we introduce the `Policy` field in the future.

There might be a need to introduce `Any` option as for other preemption modes in the future.
Note that, the same risk as [Flopping between preempting pair of workloads](#flopping-between-preempting-pair-of-workloads)
would also applies when `preemption.withinClusterQueue: Any`, yet we offer `Any`
as an option.

### ReclaimWithinCohortConfig

```golang
type ClusterQueuePreemption struct {
  ...
	ReclaimWithinCohortConfig *ReclaimWithinCohortConfig `json:"reclaimWithCohort,omitempty"`
  ...
}

type ReclaimWithinCohortWhileBorrowingPolicy string

const (
	ReclaimWithinCohortWhileBorrowingPolicyNever         ReclaimWithinCohortWhileBorrowingPolicy = "Never"
	ReclaimWithinCohortWhileBorrowingPolicyLowerPriority ReclaimWithinCohortWhileBorrowingPolicy = "LowerPriority"
)

type ReclaimWithinCohortConfig struct {
	// +kubebuilder:default=Never
	// +kubebuilder:validation:Enum=Never;LowerPriority
	WhileBorrowingPolicy ReclaimWithinCohortWhileBorrowingPolicy `json:"whileBorrowingPolicy,omitempty"`

	// +optional
	PriorityThreshold *int32 `json:"priorityThreshold,omitempty"`
}
```

**Reasons for discarding/deferring**

The field names for `ReclaimWithinCohortConfig` and `WhileBorrowingPolicy` are lengthy.

### Move ReclaimWithinCohort under ReclaimWithinCohortConfig

We could consider moving the existing enum field `.preemption.reclaimWithinCohort`
under the new configuration structure:

```golang
type ReclaimWithinCohortConfig struct {
	// +kubebuilder:default=Never
	// +kubebuilder:validation:Enum=Never;LowerPriority;Any
	Policy PreemptionPolicy `json:"policy,omitempty"`

	// +kubebuilder:default=Never
	// +kubebuilder:validation:Enum=Never;LowerPriority
	WhileBorrowingPolicy PreemptionPolicy `json:"whileBorrowingPolicy,omitempty"`

	// +optional
	PriorityThreshold *int32 `json:"priorityThreshold,omitempty"`
}
```

This might help to make the API easier to understand by putting the all the
configuration options for `reclaimWithinCohort` together.

**Reasons for discarding/deferring**

Kueue API is already Beta, and we would need a process to deprecate the existing
API. It seems that the gain to effort ratio is not good.
