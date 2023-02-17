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
  - [Job Interface](#job-interface)
  - [Kubernetes Job implementation details](#kubernetes-job-implementation-details)
  - [Working Flow in pseudo-code](#working-flow-in-pseudo-code)
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

Define a set of methods(known as golang interface) to shape the default behaviors of job.
In addition, provide a full controller and one implementation example(based on Kubernetes Job) for developers
to follow when building their own controllers.
This will help the community to integrate with Kueue more easily.

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

But the complexity lays in developers who are familiar with job-like applications may have little knowledge of the
implementation details of Kueue and they have no idea where to start to build the controller. In this case, if we can provide an
interface which defines the default behaviors of the Job, and serve the Kubernetes Job as a standard template, it will do them
a great favor.

### Goals

<!--
List the specific goals of the KEP. What is it trying to achieve? How will we
know that this has succeeded?
-->

- Define an interface which shapes the default behaviors of Job
- Provide a full controller implementation which can be reused for different jobs
- Make Kubernetes Job an implementation template of the interface

### Non-Goals

<!--
What is out of scope for this KEP? Listing non-goals helps to focus discussion
and make progress.
-->

- Integrate any job-like applications

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

We collected feedback from the community about how to fully integrate Kueue with MPIJob,
see [#499](https://github.com/kubernetes-sigs/kueue/issues/499).

### Notes/Constraints/Caveats (Optional)

<!--
What are the caveats to the proposal?
What are some important details that didn't come across above?
Go in to as much detail as necessary here.
This might be a good place to talk about core concepts and how they relate.
-->

- Job interface defined here is a hint for developers to build their own controllers.
It's a hard constrain if they wish to use the controller, but they can always write the controllers from scratch.
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

Provide a full controller may lead to the interface changes more frequently,
we can make the interface as small as possible to mitigate this.

## Design Details

<!--
This section should contain enough information that the specifics of your
change are understandable. This may include API specs (though not always
required) or even code snippets. If there's any ambiguity about HOW your
proposal will be implemented, this is the place to discuss them.
-->

### Job Interface

We will define a new interface named GenericJob, this should be implemented by custom job-like applications:

```golang
type GenericJob interface {
  // Object returns the job instance.
  Object() client.Object
  // IsSuspended returns whether the job is suspend or not.
  IsSuspended() bool
  // Suspend will suspend the job.
  Suspend() error
  // UnSuspend will unsuspend the job.
  UnSuspend() error
  // InjectNodeAffinity will inject the node affinity extracting from workload to job.
  InjectNodeSelectors(nodeSelectors []map[string]string) error
  // RestoreNodeAffinity will restore the original node affinity of job.
  RestoreNodeSelectors(nodeSelectors []map[string]string) error
  // Finished means whether the job is completed/failed or not,
  Finished()  finished bool
  // PodSets returns the podSets corresponding to the job.
  PodSets() []kueue.PodSet
  // EquivalentToWorkload validates whether the workload is semantically equal to the job.
  EquivalentToWorkload(wl kueue.Workload) bool
  // PriorityClass returns the job's priority class name.
  PriorityClass() string
  // QueueName returns the queue name the job enqueued.
  QueueName() string
  // IgnoreFromQueueing returns whether the job should be ignored in queuing, e.g. lacking the queueName.
  IgnoreFromQueueing() bool
  // PodsReady instructs whether job derived pods are all ready now.
  PodsReady() bool
```

### Kubernetes Job implementation details

We'll wrap the batchv1.Job to `BatchJob` who implements the GenericJob interface.

```golang
type BatchJob struct {
  batchv1.Job
}

var _ GenericJob = &BatchJob{}
```

Besides, we'll provide a full controller for developers to follow, all they need to do is just implement the _GenericJob_ interface.

```golang
type reconcileOptions struct {
  client                     client.Client
  scheme                     *runtime.Scheme
  record                     record.EventRecorder
  manageJobsWithoutQueueName bool
  waitForPodsReady           bool
}

func GenericReconcile(ctx context.Context, req ctrl.Request, reconcileOptions) (ctrl.Result, error) {
    // generic logics here
}

// Take batchv1.Job for example, all we want to do is just calling the GenericReconcile()
func (r *JobReconciler) Reconcile(ctx context.Context, req ctrl.Request, job GenericInterface) (ctrl.Result, error) {
  var batchJob BatchJob
  return GenericReconcile(ctx, req, &batchJob, reconcileOptions)
}


```

### Working Flow in pseudo-code

```golang
GenericReconcile:
    // Ignore unmanaged jobs, like lacking queueName.
    if job.Ignored():
        return

    // Ensure there's only one corresponding workload and
    // return the matched workload, it could be nil.
    workload = EnsureOneWorkload()

    // Handing job is finished.
    if job.Finished():
        // Processing marking workload finished if not.
        SetWorkloadCondition()
        return

    // Handing workload is nil.
    if workload == nil:
        // If workload is nil, the job should be unsuspend.
        if !job.IsSuspend():
            // When stopping the job, we'll call Suspend(), RestoreNodeAffinity() etc.,
            // and update the job with client.
            StopJob()

        // When creating workload, we'll call PodSets(), QueueName(), PodsCount() etc.
        // to fill up the workload.
        workload = CreateWorkload()
        // creating the constructed workload with client
        // ...

    // Handing job is suspend.
    if job.IsSuspend():
        // If job is suspend but workload is admitted,
        // we should start the job.
        if workload.Spec.Admission != nil:
            // When starting the job, we'll call Unsuspend(), InjectNodeAffinity() etc..
            StartJob()
            return

        // If job is suspend but we changed its queueName,
        // we should update the workload's queueName.
        // ...

    // Handing job is unsuspend.

    // If job is unsuspend but workload is unadmitted,
    // we should suspend the job.
    if workload.Spec.Admission == nil:
        StopJob()
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

- It will increase some maintenance costs, like if we change the interface,
so we should minimize this kind of changes.

## Alternatives

<!--
What other approaches did you consider, and why did you rule them out? These do
not need to be as detailed as the proposal, but should include enough
information to express the idea and why it was not acceptable.
-->

Each job implements their controller from scratch.
