# KEP-4378: Refresh Assignments During Scheduling Cycle

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
- [KEP-4378: Refresh Assignments During Scheduling Cycle](#kep-4378-refresh-assignments-during-scheduling-cycle)
  - [Summary](#summary)
  - [Understanding the Scheduling problem today](#understanding-the-scheduling-problem-today)
    - [Simplification of Kueue Scheduling today](#simplification-of-kueue-scheduling-today)
    - [Pods slow to terminate](#pods-slow-to-terminate)
    - [Scheduling slowness horizontally](#scheduling-slowness-horizontally)
  - [Motivation](#motivation)
    - [Goals](#goals)
    - [Non-Goals](#non-goals)
  - [Proposal](#proposal)
    - [Exclude Overlapping Preemption Targets](#exclude-overlapping-preemption-targets)
    - [Issues with this approach](#issues-with-this-approach)
    - [Allow Multiple Preemptors to claim same Preemption target](#allow-multiple-preemptors-to-claim-same-preemption-target)
    - [User Stories (Optional)](#user-stories-optional)
    - [Risks and Mitigations](#risks-and-mitigations)
  - [Design Details](#design-details)
    - [Test Plan](#test-plan)
        - [Prerequisite testing updates](#prerequisite-testing-updates)
      - [Unit Tests](#unit-tests)
      - [Integration tests](#integration-tests)
    - [Graduation Criteria](#graduation-criteria)
  - [Implementation History](#implementation-history)
  - [Drawbacks](#drawbacks)
  - [Alternatives](#alternatives)
<!-- /toc -->

## Summary

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

In specific scenarios, Kueue scheduling is slow across 2 dimensions. Horizontally across clusterQueue heads and vertically
down a single clusterQueue. The impact of this slowness results means workloads are not able to reclaim their nominalQuota (ie guarantees)
within an acceptable period of time, ultimately resulting in what we consider a "guarantee violation".

This KEP is a proposal to address scheduling issues in the horizontal dimension across CQ heads.

## Understanding the Scheduling problem today

### Simplification of Kueue Scheduling today

1. nomination: For the admissible clusterqueue head workloads, assign preemption targets. This produces admission candidates.
   - Preemption target assignment: actively terminating workloads are prioritized as preemption targets
2. order admission candidates.
3. evaluate admissibility of admission candidate. If the admission candidate has overlapping preemption targets, skip this workload.
   - Ex: Preemptor A has preemption targets 1, 2. Preemptor B has preemption targets 2, 3. Preemptor B will be skipped in this `schedule()` invocation.
4. If the admission candidate is admissible and its preemption target has not completed preemption, requeue admission candidate. 
   (ie CQ heads will block its own CQ until it admits)

### Pods slow to terminate

Day to day, this algorithm is pretty effective. Where it causes scheduling slowness, is when the pod is slow to terminate for any reason. 
There's 2 categories of reasons so far:

1. Node health: we call these zombie pods where the pod gets stuck in termination
2. Pod scoped:
   - pod termination grace period
   - pod gets stuck if it was pulling a docker image at time of termination

### Scheduling slowness horizontally

(While slowly terminating pods causes scheduling slowness vertically down 1 clusterQueue, that is out of scope for this document.)
When there's pods slow to terminate, this causes scheduling slowness horizontally across clusterQueues. This is because:

1. admission candidates (preemptor) will first prioritize workloads already marked for preemption to be selected as a preemption target.
   - See: `CandidatesOrdering` function: `// 0. Workloads already marked for preemption first.`
2. All CQ heads that select this slowly terminating pod as a preemption target will be skipped for the current `schedule()` invocation
3. Because CQ heads are blocking, the skipped CQ head will continue to block admission for the rest of the CQ

So a slowly terminating pod or pod stuck in termination can clog up scheduling across clusterQueues. 

## Motivation

<!--
This section is for explicitly listing the motivation, goals, and non-goals of
this KEP.  Describe why the change is important and the benefits to users. The
motivation section can optionally provide links to [experience reports] to
demonstrate the interest in a KEP within the wider Kubernetes community.

[experience reports]: https://github.com/golang/go/wiki/ExperienceReports
-->

### Goals

<!--
List the specific goals of the KEP. What is it trying to achieve? How will we
know that this has succeeded?
-->

- Isolate scheduling algorithm inefficiencies to 1 CQ instead of across CQs

### Non-Goals

<!--
What is out of scope for this KEP? Listing non-goals helps to focus discussion
and make progress.
-->
- Fix vertical scheduling algorithm inefficiency where preemption is slow and serialized down 1 CQ
  if grace period termination is set

## Proposal

<!--
This is where we get down to the specifics of what the proposal actually is.
This should have enough detail that reviewers can understand exactly what
you're proposing, but should not include things like API designs or
implementation. What is the desired outcome and how do we measure success?.
The "Design Details" section below is for the real
nitty-gritty.
-->
This KEP proposes to recalculate preemption targets when it detects overlapping preemption targets. 
Due to how Kueue's scheduling algorithm works, this KEP also proposes to allow multiple preemptors to claim
the same preemption target. Below is how we've come to that conclusion.

### Exclude Overlapping Preemption Targets

Naively, you could recalculate preemption targets and exclude workloads already selected as preemption targets. For this approach...

- The preemptor's usage is added regardless of preemption target completion
- As a result, you need to remove usage of preemption targets from the snapshot to properly account for resources being freed during the scheduling cycle. 
  This is essential for RefreshAssignmentsDuringSchedulingCycle to work correctly. Without this, workloadFits() will fail on assignment retry because we add 
  the admission candidate's usage in `AddUsage()` without removing the usage from the preemption target.

### Issues with this approach

This would introduce the following bug:

- Preemptor 1 (1GPU) preempts a 2GPU target. The preemptor's assignment mode is correctly set as `Preempt`.
- Preemptor 2 (2GPU) would be incorrectly admitted with assignment mode set incorrectly to `Fit`. 

### Allow Multiple Preemptors to claim same Preemption target

Instead, allow preemptors to claim only the usage they need, so that the preemption target could be attributable to more than 1 preemptor. This would allow:

1. preemption target recalculation
2. correctly set the preemptor's assignment mode to `Preempt` so that it requeues until the preemption target completes preemption

### User Stories (Optional)

<!--
Detail the things that people will be able to do if this KEP is implemented.
Include as much detail as possible so that people can understand the "how" of
the system. The goal here is to make this feel real for users without getting
bogged down.
-->

See [Pods slow to terminate](#pods-slow-to-terminate)

<!--
What are the caveats to the proposal?
What are some important details that didn't come across above?
Go in to as much detail as necessary here.
This might be a good place to talk about core concepts and how they relate.
-->

### Risks and Mitigations

<!--
What are the risks of this proposal, and how do we mitigate? Think broadly.
For example, consider both security and how this will impact the larger
Kubernetes ecosystem.

How will security be reviewed, and by whom?

How will UX be reviewed, and by whom?

Consider including folks who also work outside the SIG or subproject.
-->

## Design Details

<!--
This section should contain enough information that the specifics of your
change are understandable. This may include API specs (though not always
required) or even code snippets. If there's any ambiguity about HOW your
proposal will be implemented, this is the place to discuss them.
-->

In `preempted_workloads.go` create a new PreemptedWorkloads type:

```
// PreemptedWorkloadUsageTracker represents a preemption target with usage tracking
type PreemptedWorkloadUsageTracker struct {
	WorkloadInfo *workload.Info
	// OriginalUsage is the original usage of this workload
	OriginalUsage workload.Usage
	// ClaimedUsage tracks how much of this workload's usage has been claimed by preemptors
	ClaimedUsage workload.Usage
}

// PreemptedWorkloadsV2 allows multiple preemptors to target the same workload
type PreemptedWorkloadsV2 map[workload.Reference]*PreemptedWorkloadUsageTracker
```

At a high level...

- On preemption target overlap, recalculate preemption targets for the preemptor
- For preemption target recalculation in `getAssignments`, exclude targets who do not have remaining claimable usage
  - ie in `PreemptedWorkloadUsageTracker`, per resource, `OriginalUsage - ClaimedUsage = 0`
- For preemption target calculation, greedily calculate how much usage can be claimed from a target. Gather enough targets with remaining claimable usage
  to fit preemptor.

More comprehensively, this would involve key changes to:

- scheduler:
  - Recalculate preemption targets on target overlap
  - Remove only the usage claimed by preemptors from the snapshot after a successful fit check
- preemption:
  - exclude workloads that have no remaining claimable usage
  - collect workloads with enough claimable usage to fit preemptor


### Test Plan

<!--
**Note:** *Not required until targeted at a release.*
The goal is to ensure that we don't accept enhancements with inadequate testing.

All code is expected to have adequate tests (eventually with coverage
expectations). Please adhere to the [Kubernetes testing guidelines][testing-guidelines]
when drafting this test plan.

[testing-guidelines]: https://git.k8s.io/community/contributors/devel/sig-testing/testing.md
-->

[ ] I/we understand the owners of the involved components may require updates to
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

- `<package>`: `<date>` - `<test coverage>`

#### Integration tests

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

<!--
Why should this KEP _not_ be implemented?
-->

## Alternatives

<!--
What other approaches did you consider, and why did you rule them out? These do
not need to be as detailed as the proposal, but should include enough
information to express the idea and why it was not acceptable.
-->

One alternative considered is simply removing any actively terminating + expired grace period pod from the snapshot. And consider its usage freed, similar to how the k8s ResourceQuota controller works today. However, this would cause issues where workloads would be admitted but never scheduled by the underlying k8s cluster.