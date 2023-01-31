# KEP-369: Add job interface to help integrating Kueue with other job-like applications

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

Define a set of methods(known as golang interface) to help other job-like applications to smoothly integrate with Kueue.

## Motivation

<!--
This section is for explicitly listing the motivation, goals, and non-goals of
this KEP.  Describe why the change is important and the benefits to users. The
motivation section can optionally provide links to [experience reports] to
demonstrate the interest in a KEP within the wider Kubernetes community.

[experience reports]: https://github.com/golang/go/wiki/ExperienceReports
-->

From day 0 in Kueue, we natively support Kubernetes Job by leveraging the capacity of [_suspend_](https://kubernetes.io/docs/concepts/workloads/controllers/job/#suspending-a-job),
this helps us to build a multi-tenant job queueing system in Kubernetes, this is attractive to other job-like applications
like MPIJob, who lacks the capacity of queueing.

The good news is Kueue is extensible and simple to integrate with through the intermediate medium, we named _Workload_ in Kueue,
what we need to do is to build a controller to reconcile the workload and the job itself.

But the complexity lies in developers who are familiar with job-like applications may have little knowledge of the
implementation details of Kueue and they have no idea where to start to build the controller. In this case, if we can provide an
interface which defines the default behaviors of the Job, and serve the Kubernetes Job as a standard template, it will do them
a great favor.

### Goals

<!--
List the specific goals of the KEP. What is it trying to achieve? How will we
know that this has succeeded?
-->

- Define an interface which shapes the default behaviors of Job
- Make Kubernetes Job an implementation template of the interface

### Non-Goals

<!--
What is out of scope for this KEP? Listing non-goals helps to focus discussion
and make progress.
-->

- Integrate with other job-like applications

## Proposal

<!--
This is where we get down to the specifics of what the proposal actually is.
This should have enough detail that reviewers can understand exactly what
you're proposing, but should not include things like API designs or
implementation. What is the desired outcome and how do we measure success?.
The "Design Details" section below is for the real
nitty-gritty.
-->

### User Stories (Optional)

<!--
Detail the things that people will be able to do if this KEP is implemented.
Include as much detail as possible so that people can understand the "how" of
the system. The goal here is to make this feel real for users without getting
bogged down.
-->

#### Story 1

We collected feedbacks from the community about how to fully integrate Kueue with MPIJob,
see [#499](https://github.com/kubernetes-sigs/kueue/issues/499).

### Notes/Constraints/Caveats (Optional)

<!--
What are the caveats to the proposal?
What are some important details that didn't come across above?
Go in to as much detail as necessary here.
This might be a good place to talk about core concepts and how they relate.
-->

- Job interface defined here is a hint for developers to build their own controllers,
it's not a hard constraint(to implement the interface) but a good reference.
- This will increase the code complexity by wrapping the original Jobs.

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

Part I: Job Interface

We will define a new interface named GenericJob, this should be implemented by custom job-like applications:

```golang
type GenericJob interface {
  // Suspend instructs whether the job is suspended or not.
  Suspend() bool
  // Start will arm the job ready to start, like unsuspending the job and injecting nodeAffinity.
  Start(ctx context.Context, wl *kueue.Workload) error
  // Stop will disarm the job ready to stop, like suspending the job and restoring nodeAffinity.
  Stop(ctx context.Context, wl *kueue.Workload) error
  // InjectNodeAffinity will inject the node affinity extracting from workload to job.
  InjectNodeAffinity(ctx context.Context, nodeSelectors []map[string]string) error
  // RestoreNodeAffinity will restore the original node affinity of job.
  RestoreNodeAffinity(ctx context.Context, nodeSelectors []map[string]string) error
  // Finished means whether the job is completed/failed or not.
  Finished() bool
  // ConstructWorkload will build a workload corresponding to the job.
  ConstructWorkload() (*kueue.Workload, error)
  // PriorityClass returns the job's priority class name.
  PriorityClass() string
  // QueueName returns the queue name the job enqueued.
  QueueName() string
  // Ignored instructs whether this job should be ignored in reconciling, e.g. lacking the queueName.
  Ignored() bool
  // PodsCount returns the pods number job requires.
  PodsCount() int32
}
```

Also defined another interface for all-or-nothing scheduling required job-like applications:

```golang
type GangSchedulingJob interface {
  // GangSchedulingSucceeded instructs whether job is scheduled successfully in gang-scheduling.
  GangSchedulingSucceeded() bool
}
```

Part II: Kubernetes Job implementation details

We'll wrap the batchv1.Job to `BatchJob` who implements the Job interface, as well as the GangSchedulingJob interface.

```golang
type BatchJob struct {
  batchv1.Job
}

var _ GenericJobJob = &BatchJob{}
var _ GangSchedulingJob = &BatchJob{}
```

Part III: Working Flow in pseudo-code

```golang
Reconcile:
    // Ignore unmanaged jobs, like lacking queueName.
    if job.Ignored():
        return

    // Ensure there's only one corresponding workload and
    // return the matched workload, it could be nil.
    workload = EnsureOneWorkload()

    if job.Finished():
        // Processing marking workload finished if not.
        return

    if workload == nil:
        // If workload is nil, the job should be unsuspend.
        if !job.Suspend():
            // When stopping the job, we'll call RestoreNodeAffinity() etc..
            job.Stop()
            // Processing job update with client.
            // ...
            return

        // When calling ConstructWorkload, we'll call QueueName(), PodsCount() etc.
        // to fill up the workload.
        workload = job.ConstructWorkload()
        // creating the constructed workload with client
        // ...

    if job.Suspend():
        // If job is suspend but workload is admitted,
        // we should start the job.
        if workload.Spec.Admission != nil:
            // When starting the job, we'll call InjectNodeAffinity() etc..
            job.Start()
            return

        // If job is suspend but we changed its queueName,
        // we should update the workload's queueName.
        // ...

    if !job.Suspend():
        // If job is unsuspend but workload is unadmitted,
        // we should suspend the job.
        if workload.Spec.Admission == nil:
            job.Stop()
            return

    // Processing other logics like all-or-nothing scheduling
    // ...
```

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

No.

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

- `pkg/controller/workload/job`: `2023.01.30` - `5.5%` (This is output via the go tool)

#### Integration tests

<!--
Describe what tests will be added to ensure proper quality of the enhancement.

After the implementation PR is merged, add the names of the tests here.
-->

This is more like a refactor of the current implementation, theoretically no need to add more
integration tests.

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

- 2023.01.09: KEP proposed for review, including motivation, proposal, risks,
test plan.

## Drawbacks

<!--
Why should this KEP _not_ be implemented?
-->

- Developers who want to implement their own controllers will have to read more source code for
better understandings.
- Bad for the project long-term maintenance.

## Alternatives

<!--
What other approaches did you consider, and why did you rule them out? These do
not need to be as detailed as the proposal, but should include enough
information to express the idea and why it was not acceptable.
-->
