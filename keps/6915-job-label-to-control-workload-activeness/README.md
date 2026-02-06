# KEP-6915: Label based switch to control Workload activeness  

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
  - [Behavior](#behavior)
  - [User Stories](#user-stories)
    - [Story 1](#story-1)
    - [Story 2](#story-2)
  - [Notes/Constraints/Caveats](#notesconstraintscaveats)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [Test Plan](#test-plan)
      - [Prerequisite testing updates](#prerequisite-testing-updates)
    - [Unit Tests](#unit-tests)
    - [Integration tests](#integration-tests)
      - [Activate Workload via Label Flip](#activate-workload-via-label-flip)
      - [Deactivate Workload via Label Flip](#deactivate-workload-via-label-flip)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
  - [Annotation instead of label](#annotation-instead-of-label)
  - [Admission policy-based activation or queue-level configuration](#admission-policy-based-activation-or-queue-level-configuration)
  - [Direct Workload Interaction](#direct-workload-interaction)
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

Introduce label `kueue.x-k8s.io/active={true|false}` on supported Job-like objects. If `false`, Kueue creates the Workload with `spec.active=false`. Removing the label or setting `true` updates the Workload to `spec.active=true`. Missing label defaults to active.

## Motivation

<!--
This section is for explicitly listing the motivation, goals, and non-goals of
this KEP.  Describe why the change is important and the benefits to users. The
motivation section can optionally provide links to [experience reports] to
demonstrate the interest in a KEP within the wider Kubernetes community.

[experience reports]: https://github.com/golang/go/wiki/ExperienceReports
-->

Provide explicit user control over activation by interacting directly with the Job objects.

### Goals

<!--
List the specific goals of the KEP. What is it trying to achieve? How will we
know that this has succeeded?
-->

- Respect label at create and update
- Ensure consistent transitions without breaking queue semantics

### Non-Goals

<!--
What is out of scope for this KEP? Listing non-goals helps to focus discussion
and make progress.
-->

- New CRDs or scheduler policy changes

## Proposal

<!--
This is where we get down to the specifics of what the proposal actually is.
This should have enough detail that reviewers can understand exactly what
you're proposing, but should not include things like API designs or
implementation. What is the desired outcome and how do we measure success?.
The "Design Details" section below is for the real
nitty-gritty.
-->

### Behavior

- Kueue managed Jobs can have a label `kueue.x-k8s.io/active` with default value `true`
- Create: label false sets the field `Workload.spec.active` to `false`
- Update: remove or set true sets the field `Workload.spec.active` to `true`
- If a Job is unsuspended while Workload inactive, controller re-suspends  


### User Stories

<!--
Detail the things that people will be able to do if this KEP is implemented.
Include as much detail as possible so that people can understand the "how" of
the system. The goal here is to make this feel real for users without getting
bogged down.
-->

#### Story 1

A user submits a Job they do not want to start queueing immediately. They add the label  
`kueue.x-k8s.io/active: "false"`. Kueue creates the associated Workload as inactive. 
The Job is now effectively suspended and will not queue while the label remains `false`.

Later, the user decides the Job is ready to queue. They either set the label to  
`kueue.x-k8s.io/active: "true"` or remove it entirely.

Kueue updates the Workload to active and treats the Job like any other queued job.

#### Story 2

A user submits a Job without the `kueue.x-k8s.io/active` label. Kueue creates a regular 
active Workload. However, the user realizes they do not want the Job to start executing, 
so they add the label `kueue.x-k8s.io/active: "false"`.

Kueue marks the associated Workload as inactive. The Job is now effectively suspended 
and will not queue while the label remains `false`.

Later, the user decides the Job is ready to queue. They either set the label to  
`kueue.x-k8s.io/active: "true"` or remove it entirely.

Kueue updates the Workload to active and treats the Job like any other queued job.

### Notes/Constraints/Caveats

<!--
What are the caveats to the proposal?
What are some important details that didn't come across above?
Go in to as much detail as necessary here.
This might be a good place to talk about core concepts and how they relate.
-->

This feature enables users to force the value of `spec.active` in the `Workload` object that is associated with the user's Job.

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

- No new fields; reuse `Workload.spec.active`  
- Label contract: `kueue.x-k8s.io/active` values `true` or `false`  
- Invalid values are ignored and assumed to be `true`
- Feature gate: `WorkloadActiveLabel` (Alpha, default off)  


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

- `<package>`: `<date>` - `<test coverage>`

#### Integration tests

<!--
Describe what tests will be added to ensure proper quality of the enhancement.

After the implementation PR is merged, add the names of the tests here.
-->

##### Activate Workload via Label Flip

1. Create Job with inactive label
   - Create a `batch/v1 Job` manifest with label `kueue.x-k8s.io/active: "false"`
2. Observe Workload in inactive state
   - Wait for corresponding `Workload` resource.
   - Observe its field: `spec.active == false`
3. Flip label to active
   - Set the label of the Job object to `kueue.x-k8s.io/active: "true"`
4. Observe Workload becomes active
   - Wait for `Workload.spec.active == true`.

##### Deactivate Workload via Label Flip

1. Create a Job
   - Create a `batch/v1 Job` manifest
2. Observe Workload in Active state
   - Wait for corresponding `Workload` resource.
   - Observe its field: `spec.active == true`
3. Flip label to inactive
   - Set the label of the Job object to `kueue.x-k8s.io/active: "false"`
4. Observe Workload becomes inactive
   - Wait for `Workload.spec.active == false`.

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

TBD

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

- Adds another control surface that can be misused  

## Alternatives

<!--
What other approaches did you consider, and why did you rule them out? These do
not need to be as detailed as the proposal, but should include enough
information to express the idea and why it was not acceptable.
-->

### Annotation instead of label

Annotations are immutable and thus the user would be unable to flip an "inactive" Job back into the "active" state.

### Admission policy-based activation or queue-level configuration

These methods do not allow per-job granularity and requires cluster admin intervention. In turn, they reduce flexibility for regular users.

### Direct Workload Interaction

Users could patch the Workload object directly and toggle spec.active between true and false. This requires RBAC permissions for users to enable modifying Kueue CRs.
Also, this would break abstraction as users will interact with internal Kueue objects instead of their own Jobs.