# KEP-1618: Optional garbage collection of finished Workloads

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
  - [User Stories](#user-stories)
    - [Story 1 - Configurable retention for finished Workloads](#story-1---configurable-retention-for-finished-workloads)
  - [Notes/Constraints/Caveats](#notesconstraintscaveats)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [API](#api)
  - [Behavior](#behavior)
  - [Test Plan](#test-plan)
    - [Unit Tests](#unit-tests)
    - [Integration tests](#integration-tests)
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

This KEP proposes a mechanism for garbage collection of finished Workloads in Kueue. 
Currently, Kueue does not delete its own Kubernetes objects, leading to potential accumulation 
and unnecessary resource consumption. The proposed solution involves adding a new 
API section called `objectRetentionPolicies` to allow users to specify retention 
policies for finished Workloads. This can be set to a specific duration or 
disabled entirely to maintain backward compatibility.

Benefits of implementing this KEP include the deletion of superfluous information, 
decreased memory footprint for Kueue, and freeing up etcd storage. The proposed changes 
are designed to be explicitly enabled and configured by users, ensuring backward compatibility. 
While the primary focus is on finished Workloads, the API section can be extended to include 
other Kueue-authored objects in the future. The implementation involves incorporating the 
deletion logic into the reconciliation loop and includes considerations for 
potential risks, such as handling a large number of existing finished Workloads. 
Overall, this KEP aims to enhance Kueue's resource management and provide users with 
more control over object retention.

## Motivation

<!--
This section is for explicitly listing the motivation, goals, and non-goals of
this KEP.  Describe why the change is important and the benefits to users. The
motivation section can optionally provide links to [experience reports] to
demonstrate the interest in a KEP within the wider Kubernetes community.

[experience reports]: https://github.com/golang/go/wiki/ExperienceReports
-->

Currently, Kueue does not delete its own Kubernetes objects, leaving it to Kubernetes' 
garbage collector (GC) to manage the retention of objects created by Kueue, such as Pods, Jobs, and Workloads. 
We do not set any expiration on these objects, so by default, they are kept indefinitely. Based on usage patterns, 
including the amount of jobs created, their complexity, and frequency over time, these objects can accumulate, 
leading to unnecessary use of etcd storage and a gradual increase in Kueue's memory footprint. Some of these objects, 
like finished Workloads, do not contain any useful information that could be used for additional purposes, such as debugging. 
That's why, based on user feedback in [#1618](https://github.com/kubernetes-sigs/kueue/issues/1618), a mechanism for garbage collection of finished Workloads 
is being proposed.

With this mechanism in place, deleting finished Workloads has several benefits:
- **Deletion of superfluous information**: Finished Workloads do not store any useful runtime-related information 
(which is stored by Jobs), nor are they useful for debugging. They could possibly be useful for auditing purposes, 
but this is not a primary concern.
- **Decreasing Kueue memory footprint**: Finished Workloads are loaded during Kueue's initialization and kept in memory, 
consuming resources unnecessarily.
- **Freeing up etcd storage**: Finished Workloads are stored in etcd memory until Kubernetes' built-in GC mechanism 
collects them, but by default, Custom Resources are stored indefinitely.

### Goals

<!--
List the specific goals of the KEP. What is it trying to achieve? How will we
know that this has succeeded?
-->

- Support the deletion of finished Workload objects.
- Introduce configuration of global retention policies for Kueue-authored Kubernetes objects, 
starting with finished Workloads.
- Maintain backward compatibility (the feature must be explicitly enabled and configured).

### Non-Goals

<!--
What is out of scope for this KEP? Listing non-goals helps to focus discussion
and make progress.
-->

- Support the deletion/expiration of any Kueue-authored Kubernetes object other than finished Workloads.

## Proposal

<!--
This is where we get down to the specifics of what the proposal actually is.
This should have enough detail that reviewers can understand exactly what
you're proposing, but should not include things like API designs or
implementation. What is the desired outcome and how do we measure success?.
The "Design Details" section below is for the real
nitty-gritty.
-->

Add a new API called `objectRetentionPolicies`, 
which will enable specifying a retention policy for finished Workloads under an option 
called `FinishedWorkloadRetention`. This API section could be extended in the future 
with options for other Kueue-authored objects.


### User Stories

<!--
Detail the things that people will be able to do if this KEP is implemented.
Include as much detail as possible so that people can understand the "how" of
the system. The goal here is to make this feel real for users without getting
bogged down.
-->

#### Story 1 - Configurable retention for finished Workloads

As a Kueue administrator, I want to control the retention of 
finished Workloads to minimize the memory footprint and optimize 
storage usage in my Kubernetes cluster. I want the flexibility to 
configure a retention period to automatically delete Workloads after a 
specified duration or to immediately delete them upon completion.

**Note:** Immediate deletion can be configured by setting the retention period to 0 seconds.

### Notes/Constraints/Caveats

<!--
What are the caveats to the proposal?
What are some important details that didn't come across above?
Go in to as much detail as necessary here.
This might be a good place to talk about core concepts and how they relate.
-->

- Initially, other naming was considered for config keys. Namely, instead of "Objects," 
the word "Resources" was used, but based on feedback from `@alculquicondor`, it was changed 
because "Resources" bears too many meanings in the context of Kueue 
(Kubernetes resources, physical resources, etc.).
- The deletion mechanism assumes that finished Workload objects do not have 
any finalizers attached to them. After an investigation by `@mimowo`, 
the [exact place in the code](https://github.com/kubernetes-sigs/kueue/blob/a128ae3354a56670ffa58695fef1eca270fe3a47/pkg/controller/jobframework/reconciler.go#L332-L340) 
was found that removes the resource-in-use 
finalizer when the Workload state transitions to finished. If that behavior ever 
changes, or if there is an external source of a finalizer being attached 
to the Workload, it will be marked for deletion by the Kubernetes client 
but will not actually be deleted until the finalizer is removed.
- If the retention policy for finished Workloads is misconfigured 
(the provided value is not a valid time.Duration), Kueue will fail to start 
during configuration parsing.
- In the default scenario, where the object retention policy section 
is not configured or finished Workloads retention is either not configured 
or set to null, the existing behavior is maintained for backward compatibility. 
This means that Workload objects will not be deleted by Kueue, 
aligning with the behavior before the introduction of this feature.

### Risks and Mitigations

<!--
What are the risks of this proposal, and how do we mitigate? Think broadly.
For example, consider both security and how this will impact the larger
Kubernetes ecosystem.

How will security be reviewed, and by whom?

How will UX be reviewed, and by whom?

Consider including folks who also work outside the SIG or subproject.
-->

- **R**: In clusters with a large number of existing finished Workloads 
(thousands, tens of thousands, etc.), it may take a significant amount of time
to delete all previously finished Workloads that qualify for deletion. Since the 
reconciliation loop is effectively synchronous, this may impact the reconciliation 
of new jobs. From the user's perspective, it may seem like Kueue's initialization 
time is very long for that initial run until all expired objects are deleted.\
**M**: Unfortunately, there is no easy way to mitigate this without complicating the code.
A potential improvement would be to have a dedicated internal queue that handles deletion 
outside the reconciliation loop, or to delete expired objects in batches so that after 
a batch of deletions is completed, any remaining expired objects would be skipped until 
the next run of the loop. However, this would go against the synchronous nature of the reconciliation loop.

## Design Details

<!--
This section should contain enough information that the specifics of your
change are understandable. This may include API specs (though not always
required) or even code snippets. If there's any ambiguity about HOW your
proposal will be implemented, this is the place to discuss them.
-->


### API
```go
// Configuration is the Schema for the kueueconfigurations API
type Configuration struct {
    //...
    // ObjectRetentionPolicies provides configuration options for retention of Kueue owned
    // objects. A nil value will disable automatic deletion for all objects.
    ObjectRetentionPolicies *ObjectRetentionPolicies `json:"objectRetentionPolicies,omitempty"`
}

//...

type ObjectRetentionPolicies struct {
    // FinishedWorkloadRetention is the duration to retain finished Workloads.
    // A duration of 0 will delete finished Workloads immediately.
    // A nil value will disable automatic deletion.
    // The value is represented using the metav1.Duration format, allowing for flexible
    // specification of time units (e.g., "24h", "1h30m", "30s").
    //
    // Defaults to null.
    // +optional
    FinishedWorkloadRetention *metav1.Duration `json:"finishedWorkloadRetention"`
}
```

### Behavior
The new behavior is as follows:

1. During Kueue's initial reconciliation loop, all previously finished Workloads
   will be evaluated against the Workload retention policy. They will either be
   deleted immediately if they are already expired or requeued for reconciliation
   after their retention period has expired.

2. During subsequent reconciliation loops, each Workload will be evaluated
   using the same approach as in step 1. However, the evaluation will occur
   during the next reconciliation loop after the Workload was declared finished.
   Based on the evaluation result against the retention policy, the Workload will
   either be requeued for later reconciliation or deleted.

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

The following new unit tests or modifications to existing ones were added to test 
the new functionality:

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

- Added behavior `Workload controller with resource retention` 
in `test/integration/controller/core/workload_controller_test.go`


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

- Deleting objects is inherently complex, and garbage collection (GC) behavior can often be 
surprising for end-users who did not configure the service themselves. Even though the 
mechanism proposed in this KEP is relatively simple and must be explicitly enabled, 
it is a global mechanism applied at the controller level. The author has added some 
observability to the controller itself (logging and emitting deletion events), 
but Kubernetes users and developers accustomed to tracking object lifecycles 
using per-object policies might be surprised by this behavior.


## Alternatives

<!--
What other approaches did you consider, and why did you rule them out? These do
not need to be as detailed as the proposal, but should include enough
information to express the idea and why it was not acceptable.
-->

- Leave the current state of things as is, not implementing any garbage 
collection mechanism for finished Workloads.
- Initially, a different approach was hypothesized where the deletion logic
  would not be integrated into the `Reconcile` function. Instead, a
  dedicated handler and watcher pair would manage the cleanup of finished Workloads. This approach had the following advantages and disadvantages::
  - Cons:
    - Kueue's internal state management for Workloads happens within the `WorkloadReconciler`. Moving the deletion logic elsewhere would split up code meant to handle a single resource's lifecycle.
    - Implementing a watcher specifically for expired Workloads turned out to be unexpectedly complex, leading to this approach being abandoned.
  - Pros:
    - A separate handler/watcher could offload the deletion logic, making the already large and intricate Reconcile function more focused and potentially easier to maintain.
