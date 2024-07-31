# KEP-NNNN: Your short, descriptive title

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
    - [Story 2](#story-2)
  - [Notes/Constraints/Caveats (Optional)](#notesconstraintscaveats-optional)
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

This KEP proposes a mechanism of optional garbage collection of finished Workloads.

## Motivation

<!--
This section is for explicitly listing the motivation, goals, and non-goals of
this KEP.  Describe why the change is important and the benefits to users. The
motivation section can optionally provide links to [experience reports] to
demonstrate the interest in a KEP within the wider Kubernetes community.

[experience reports]: https://github.com/golang/go/wiki/ExperienceReports
-->

Currently, kueue doesn't delete its own K8s objects leaving it to K8s' GC to manage retention of 
objects created by kueue such as Pods, Jobs, Workloads etc. We don't set any expiration on them so
by default they are kept forever. Based on the usage pattern including
amount of jobs created, their complexity and frequency over time those objects can accumulate which leads
to unnecessary usage of etcd storage as well as gradual increase of kueue's memory footprint. Some of these objects,
like finished Workloads don't contain any useful information which could be used for additional
purposes like debugging. That's why based on user's feedback in [#1618](https://github.com/kubernetes-sigs/kueue/issues/1618)
or [#1789](https://github.com/kubernetes-sigs/kueue/issues/1789) a mechanism of garabge collection
of finished Workloads is being proposed.

With the mechanism in place deleting finished Workloads has several benefits:
- **deletion of superfluous information** - finished Workloads don't store any useful runtime related
information (that's being stored by Jobs), they aren't useful for debugging, possibly could be used 
for auditing purposes
- **decreasing kueue memory footprint** - finished Workloads are being loaded during kueue's 
initialization and kept in memory.
- **freeing up etcd's storage** - finished Workloads are stored in etcd's memory until K8s builtin
GC mechanism collects it but by default Custom Resources are being stored indefinitely.

### Goals

<!--
List the specific goals of the KEP. What is it trying to achieve? How will we
know that this has succeeded?
-->

- support deletion of finished Workload objects
- introduction of configuration of global retention policies for kueue authored 
K8s objects starting with finished Workloads
- maintain backward compatibility (the feature needs to be explicitly enabled and configured)

### Non-Goals

<!--
What is out of scope for this KEP? Listing non-goals helps to focus discussion
and make progress.
-->

- support deletion/expiration of any kueue authored K8s object
- configuring retention policies (like `.spec.ttlSecondsAfterFinished`) attached per 
K8s object

## Proposal

<!--
This is where we get down to the specifics of what the proposal actually is.
This should have enough detail that reviewers can understand exactly what
you're proposing, but should not include things like API designs or
implementation. What is the desired outcome and how do we measure success?.
The "Design Details" section below is for the real
nitty-gritty.
-->

Add a new section to the configuration called `objectRetentionPolicies`
which will include a retention policy for finished Workloads under an option
called `FinishedWorkloadRetention`. The section could be extended with
options for other kueue authored objects.


### User Stories

<!--
Detail the things that people will be able to do if this KEP is implemented.
Include as much detail as possible so that people can understand the "how" of
the system. The goal here is to make this feel real for users without getting
bogged down.
-->

Based on how the retention policy is configured for finished Workloads there are
multiple user stories being proposed below.

#### Story 0 - Retention policy for finished Workloads is misconfigured

If the finished Workloads policy is not configured correctly (provided value is not a valid
time.Duration) kueue will crash during config parsing.

#### Story 1 - Retention policy for finished Workloads is not configured (default)

**This story ensures backward compatibility.**
If the object retention policy section is not configured or finished Workloads retention 
is either not configured or set to null Workload objects won't be deleted by kueue.

#### Story 2 - Retention policy for finished Workloads is configured and set to a value > 0s

If the object retention policy is configured to a value > 0s finished Workload retention policy
will be evaluated against every completed Workload. The flow is follow:
1. During kueue's initial run of the reconciliation loop all previously finished Workloads 
will be evaluated against the Workload retention policy and they'll be either deleted immediately if they
are already expired or they'll be requeued for reconciliation after their
retention period has expired.
2. During following runs of the reconciliation loop each Workload will be evaluated using
the same approach as in 1. but during the subsequent run of the reconciliation loop
after they were declared finished and based on the evaluation result against the retention policy
they'll be either requeued for later reconciliation or deleted.

#### Story 3 - Retention policy for finished Workloads is configured and set to 0s

When the object retention policy is set to 0s finished workloads will be deleted
during the first run of the reconciliation loop after they were declared finished.

### Notes/Constraints/Caveats (Optional)

<!--
What are the caveats to the proposal?
What are some important details that didn't come across above?
Go in to as much detail as necessary here.
This might be a good place to talk about core concepts and how they relate.
-->

- Initially other naming was considered for config keys namely instead of Objects 
the word Resources was used but based on the feedback from @alculquicondor it was
changed because of Resources bearing too many meanings in the context of kueue 
(K8s resources, physical resources, etc.).
- A different implementation was proposed initially. The deletion logic wasn't supposed
to be part of the Reconcile function but rather it was supposed to have its own 
handler/watcher pair. During implementation, it was challenging to implement it this way
without substantial changes to the code. Also creating a watcher for completed Workloads
proved to be very difficult and complex.
- The deletion mechanism assumes that finished Workload objects don't have
any finalizers attached to them. After an investigation from @mimowo the [exact place in
code](https://github.com/kubernetes-sigs/kueue/blob/a128ae3354a56670ffa58695fef1eca270fe3a47/pkg/controller/jobframework/reconciler.go#L332-L340) 
was found which removes the resource in use finalizer when the Workload state
transitions to finished. If that behavior ever changes or if there's an external
source of a finalizer being attached to the Workload it will be marked for deletion
by the K8s client but it won't actually be deleted until the finalizer is removed.

### Risks and Mitigations

<!--
What are the risks of this proposal, and how do we mitigate? Think broadly.
For example, consider both security and how this will impact the larger
Kubernetes ecosystem.

How will security be reviewed, and by whom?

How will UX be reviewed, and by whom?

Consider including folks who also work outside the SIG or subproject.
-->

- In clusters with a big number of existing finished Workloads (1000s, 10000s, ...) 
it may take some time to delete all previously finished Workloads that qualify 
for deletion. Since the reconciliation loop is effectively synchronous it may 
impact reconciling new jobs. From the user's perspective it may seem like the kueue's
initialization time is really long for that initial run until all expired objects
are deleted. Unfortunately there's no good way of mitigating this
without complicating the code. A potential improvement would be to have a special
internal queue that deals with deletion outside of the reconciliation loop or 
deleting expired objects in batches so that after a batch of deletions is 
completed all expired objects that are left would be skipped over until the next run
of the loop but it'd increase  its against the synchronous nature of the reconciliation loop.
The author proposes to include it as part of the documentation 

## Design Details

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

[X] I understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.

[//]: # (##### Prerequisite testing updates)

[//]: # ()
[//]: # (<!--)

[//]: # (Based on reviewers feedback describe what additional tests need to be added prior)

[//]: # (implementing this enhancement to ensure the enhancements have also solid foundations.)

[//]: # (-->)

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

The following new unit tests or modifications to the existing ones were added 
to test new functionality:

- `apis/config/v1beta1/defaults_test.go`
  - updated existing tests with `defaultObjectRetentionPolicies` which sets 
  `FinishedWorkloadRetention` to nil which ensures that the default 
  scenario maintains backward compatibility and no retention settings are set.
  - new unit test: `set object retention policy for finished workloads` which
  checks if provided can configuration overrides default values.
- `pkg/config/config_test.go`
  - updated existing tests with `defaultObjectRetentionPolicies` set to nil 
  which tests if for existing tests the default behavior maintains backward
  compatibility and results in no retention settings.
  - new unit test: `objectRetentionPolicies config` which sets some example 
  retention policy and then checks if it can be retrieved.
- `pkg/controller/core/workload_controller_test.go`
  - added following new tests:
    - `shouldn't delete the workload because, object retention not configured`
    - `shouldn't try to delete the workload (no event emitted) because it is already being deleted by kubernetes, object retention configured`
    - `shouldn't try to delete the workload because the retention period hasn't elapsed yet, object retention configured`
    - `should delete the workload because the retention period has elapsed, object retention configured`
    - `should delete the workload immediately after it completed because the retention period is set to 0s`

#### Integration tests

<!--
Describe what tests will be added to ensure proper quality of the enhancement.

After the implementation PR is merged, add the names of the tests here.
-->

- TBA


[//]: # (### Graduation Criteria)

[//]: # ()
[//]: # (<!--)

[//]: # ()
[//]: # (Clearly define what it means for the feature to be implemented and)

[//]: # (considered stable.)

[//]: # ()
[//]: # (If the feature you are introducing has high complexity, consider adding graduation)

[//]: # (milestones with these graduation criteria:)

[//]: # (- [Maturity levels &#40;`alpha`, `beta`, `stable`&#41;][maturity-levels])

[//]: # (- [Feature gate][feature gate] lifecycle)

[//]: # (- [Deprecation policy][deprecation-policy])

[//]: # ()
[//]: # ([feature gate]: https://git.k8s.io/community/contributors/devel/sig-architecture/feature-gates.md)

[//]: # ([maturity-levels]: https://git.k8s.io/community/contributors/devel/sig-architecture/api_changes.md#alpha-beta-and-stable-versions)

[//]: # ([deprecation-policy]: https://kubernetes.io/docs/reference/using-api/deprecation-policy/)

[//]: # (-->)

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

- Initial PR created for the feature pre-KEP [#2686](https://github.com/kubernetes-sigs/kueue/pull/2686).

## Drawbacks

<!--
Why should this KEP _not_ be implemented?
-->

- Deleting things is tricky and GC behavior can often be surprising for users.
Even though the mechanism proposed in this KEP is really simple and needs to be 
explicitly enabled, it is a global mechanism applied at the controller level.
The author made sure to introduce some observability in the controller itself
(logging and emitting a deletion event) but K8s users and developers used to
tracking K8s objects' lifecycle using per object attached policies might be
surprised with this behavior.


## Alternatives

<!--
What other approaches did you consider, and why did you rule them out? These do
not need to be as detailed as the proposal, but should include enough
information to express the idea and why it was not acceptable.
-->

- Leave the current state of things as is,
- Implement attaching retention policies attached per object like (like `.spec.ttlSecondsAfterFinished`)
instead of having global retention policies configured at kueue level
but it'd complicate the implementation and add complexity to the feature itself
e.g. during reconfiguration of an existing retention policy all existing K8s objects
would need to be updated with the new policy.
