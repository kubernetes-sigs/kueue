# KEP-349: All-or-nothing semantics for job resource assignment

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
  - [Notes/Contraints/Caveats (Optional)](#notescontraintscaveats-optional)
    - [Choosing the API object to opt-in for the mechanism](#choosing-the-api-object-to-opt-in-for-the-mechanism)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [API](#api)
    - [Workload API](#workload-api)
    - [Job annotation](#job-annotation)
  - [StartedUp workload condition](#startedup-workload-condition)
  - [The mechanism](#the-mechanism)
  - [Timeout on reaching the StartedUp condition](#timeout-on-reaching-the-startedup-condition)
  - [Test Plan](#test-plan)
      - [Prerequisite testing updates](#prerequisite-testing-updates)
    - [Unit Tests](#unit-tests)
    - [Integration tests](#integration-tests)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
    - [Delay job start instead of workload admission](#delay-job-start-instead-of-workload-admission)
    - [Block job start only if two workloads overlap in flavors](#block-job-start-only-if-two-workloads-overlap-in-flavors)
    - [Pod Resource Reservation](#pod-resource-reservation)
    - [Use LocalQueue to default the Workload fields](#use-localqueue-to-default-the-workload-fields)
<!-- /toc -->

## Summary

This proposal introduces an opt-in mechanism to ensure that a job gets the
physical resources assigned once unsuspended by Kueue.

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

Some jobs need all pods to be running at the same time to make progress, for
example, when they require pod-to-pod communication. In that case a pair of
large jobs may deadlock if there are issues with resource provisioning to
match the configured cluster quota. The jobs could successfully run to
completion if their pods were scheduled sequentially.

<!--
This section is for explicitly listing the motivation, goals, and non-goals of
this KEP.  Describe why the change is important and the benefits to users. The
motivation section can optionally provide links to [experience reports] to
demonstrate the interest in a KEP within the wider Kubernetes community.

[experience reports]: https://github.com/golang/go/wiki/ExperienceReports
-->

### Goals

- a mechanism to ensure that a job gets assigned physical resources when
unsuspended by Kueue
- a timeout on getting the physical resources assigned by a Job since
unsuspended by Kueue

<!--
List the specific goals of the KEP. What is it trying to achieve? How will we
know that this has succeeded?
-->

### Non-Goals

- guarantee that two jobs would not schedule pods concurrently. Example
scenarios in which two jobs may still concurrently schedule their pods:
  - when succeeded pods are replaced with new because job's parallelism is less than its completions;
  - when a failed pod gets replaced

<!--
What is out of scope for this KEP? Listing non-goals helps to focus discussion
and make progress.
-->

## Proposal

We introduce a mechanism to ensure workloads get their physical resources
assigned by avoiding concurrent scheduling of workloads. More precisely, we
block admission of new workloads until the first batch of the Job is scheduled.
Additionally, we await with starting the Job until all other admitted workloads
have their pods scheduled. This behavior can be opted-in by a Job by a new
annotation.

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

As a machine learning researcher I want to run Jobs that require all their pods
to run at the same time. I want to be able to run my Jobs on on-demand resources,
however, I don't want to risk my Jobs would deadlock in case of issues with
provisioning nodes to satisfy the configured cluster queue quota. This could
happen when some Jobs don't specify priorities or specify the same priority.

My use case can be supported by adding
`kueue.x-k8s.io/startup-requirements: AllPodsReady` annotation to the jobs
I run.

#### Story 2

As an administrator of a batch processing in my organization I want to ensure
that two or more Jobs would not deadlock in case of node provisioning issues.

My use case can be supported by adding
`kueue.x-k8s.io/startup-requirements: AllPodsReady` annotation to all jobs.

### Notes/Contraints/Caveats (Optional)

<!--
What are the caveats to the proposal?
What are some important details that didn't come across above?
Go in to as much detail as necessary here.
This might be a good place to talk about core concepts and how they relate.
-->

#### Choosing the API object to opt-in for the mechanism

Here we review the options for the best place for the API to opt-in for the
mechanism:
- job (current proposal)
- local queue
- cluster queue
- component

**Job level pros**:
- throughput - the same cluster queue may run workloads which don't need all-or-nothing guarantees and thus get admitted
faster,
- requiring all pods to run at the same time is a property of a Job itself.

**Job level cons**:
- introducing a new annotation on a Job level extends the surface of the Job API via annotation

**Local Queue level pros**:
- could be a natual place to also define timeout for reaching `StartedUp`, which is not component-level

**Local Queue level cons**:
- might be harder to withdraw from API commitments to the LocalQueue than to deprecate a Job annotation
- slightly higher complexity of implementation comparing to workload level - we need to either default the workload API
field or lookup the LocalQueue value on every workload use. Still, in order to ensure exclusive start of pods we still
need to both block admission of new workloads until in `StartedUp` and await for all already workloads to be in the
`StartedUp` condition, as some workloads might be executed on the cluster bypassing the queues.

**Cluster Queue level cons**:
- cluster queue is only assigned after admission so it blocks us the ability to block admission for a given workload
- otherwise, same cons as for Local Queue.

**Component level pros**:
- lower complexity of implementation comparing to workload level - we could just block admission until the workload is
in the `AllPodsReady` state. We wouldn't need to check if there are any already admitted workloads - we know they all
have already awaited to be in the `AllPodsReady` condition.

**Component level cons**:
- throughput - would degrade substantially by sequencing of all workloads, even those which don't require the semantics

### Risks and Mitigations

If a workload fails to schedule its pods it could block admission of other
workloads indefinitely.

To mitigate this issue we introduce a timeout on reaching the `StartedUp`
condition by a workload since its job start (see:
[Timeout on reaching the StartedUp condition](#timeout-on-reaching-the-startedup-condition)).

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


### API

#### Workload API

We extend the Workload API with the new `startupRequirements` field to
enable the new behavior.

```golang
// StartupRequirements describes workload startup requirements
type StartupRequirements string

const (
  // AllPodsReady: means that workload requires all of its pods to be running
  // at the same time to make progress towards completion.
  StartupRequirementsAllPodsReady StartupRequirements = "AllPodsReady"
  // AtLeastOnePodReady means that the workload requires at least one pod
  // to run to make progress towards completion.
  StartupRequirementsAtLeastOnePodReady StartupRequirements = "AtLeastOnePodReady"
)

// WorkloadSpec defines the desired state of Workload
type WorkloadSpec struct {
  ...
  // StartupRequirements describes the workload startup requirements to ensure
  // the workload can make progress towards completion once running.
  // Possible values are:
  // - AllPodsReady: means that workload requires all of its pods to be running
  // at the same time to make progress towards completion,
  // - AtLeastOnePodReady means that the workload requires at least one pod
  // to run to make progress towards completion.
  // Defaults to AtLeastOnePodReady.
  StartupRequirements *StartupRequirements `json:"startupRequirements,omitempty"`
}
```

#### Job annotation

We introduce a new Job annotation, called `kueue.x-k8s.io/startup-requirements`,
as a JobAPI counterpart of the `startupRequirements` Workload API field. When
the annotation is present, its value is used as the field value during workload
creation. A Job with unkonwn value of the annotation results in rejecting the
Job by a new validation webhook.

### StartedUp workload condition

We introduce a new workload condition, called `StartedUp`, to indicate
if the workload's startup requirements are satisfied. More precisely, we add
the condition when `job.status.ready + job.status.succeeded` is greater or equal
than `job.spec.parallelism` or `1` in case of workloads with `AllPodsReady`
or `AtLeastOnePodReady` startup requirements, respectively.

The `StartedUp` condition is addded to the workload by the Kueue's Job
reconciler in reaction to a status update of the corresponding Job. Note that,
verifying if the condition should be added does not require an extra API call as
the Kueue's Job reconciller already fetches the latest Job object at the
beginning of the `Reconcile` function. Also note, there is no new condition
introduced to the Job API.

Note that, we don't take failed pods directly into the computation for verifying
the `StartedUp` condition. However, a buggy workload is eliminated as the
correspoding job fails due to exceeding the `.spec.backoffLimit` limit.

### The mechanism

When a workload with `startupRequirements=AllPodsReady` is nominated for admission,
kueue scheduler blocks its admission (and admission of other workloads nominated
in this cycle) until all already admitted workloads are in the `StartedUp`
state. Kueue scheduler verifies this condition by a lookup to the cache
of admitted workloads. When two or more such workloads are nomitated in a given
cycle only one of them is admitted.

Once such a workload is admitted, kueue's scheduler blocks admission of further
workloads until the workload has the `StartedUp` condition. Similarly
as in the previous case, Kueue's scheduler verifies the workload state by
a lookup to the cache of admitted workloads.

Note that, there is at most one workload with `startupRequirements=AllPodsReady`
admitted at any given time.

### Timeout on reaching the StartedUp condition

We introduce a timeout on reaching the `StartedUp` condition since the job
is unsuspended. The timeout is specified as the Kueue manager component level
as the `startedUpTimeout` API field.

When the timeout is exceeded we suspend the Job corresponding to the workload
and put into the `inadmissibleWorkloads` list.

### Test Plan

<!--
**Note:** *Not required until targeted at a release.*
The goal is to ensure that we don't accept enhancements with inadequate testing.

All code is expected to have adequate tests (eventually with coverage
expectations). Please adhere to the [Kubernetes testing guidelines][testing-guidelines]
when drafting this test plan.

[testing-guidelines]: https://git.k8s.io/community/contributors/devel/sig-testing/testing.md
-->

[x] I understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.

##### Prerequisite testing updates

<!--
Based on reviewers feedback describe what additional tests need to be added prior
implementing this enhancement to ensure the enhancements have also solid foundations.
-->

We consider the unit test coverage of `pkg/scheduler` and cache `pkg/cache` to
be sufficient as a prerequisite for development.

There is no unit test coverage for the `pkg/controller/workload/job` package,
but it is thoroughly tested at the integration level. Some unit tests on the
path of creating a workload based on a job, which will be modified in this work,
might be added depending on the reviewers feedback.

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

- `pkg/scheduler`: `25 Nov 2022` - `91.0%`
- `pkg/cache`: `25 Nov 2022` - `83.1%`
- `pkg/controller/workload/job`: `25 Nov 2022` - `0%`

#### Integration tests

The following scenarios will be covered with integration tests:
- Workload created for a Job with `kueue.x-k8s.io/startup-requirements` has `.spec.startupRequirements=AllPodsReady`
- No workload is admitted if the `.spec.startupRequirements=AllPodsReady` is not in the `StartedUp`  condition
- workloads are admitted if the `.spec.startupRequirements=AllPodsReady` is in the `StartedUp`  condition
- a workload with `.spec.startupRequirements=AllPodsReady` does not get admitted if there is another admitted workload which is not in the `StartedUp`  condition
- a workload with `.spec.startupRequirements=AllPodsReady` gets admitted if all other admitted workloads are in the `StartedUp`  condition
- a workload which exceeds the `kueue.x-k8s.io/wait-for-pods-ready-seconds` timeout is suspended and put into the `inadmissibleWorkloads` list

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

N/A

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

Delaying of workload admission until all pods are scheduled may decrease
thoughput significantly. Especially, if there is enough resource capacity to
which could be otherwise used to start multiple jobs at the same time.

## Alternatives

<!--
What other approaches did you consider, and why did you rule them out? These do
not need to be as detailed as the proposal, but should include enough
information to express the idea and why it was not acceptable.
-->

#### Delay job start instead of workload admission

When a workload with `startupRequirements=AllPodsReady` is nominated its admission is
blocked (rejected) until all the already admitted workloads are in the `StartedUp`
condition. Instead, we could admit the workload, but delay its job start
until the condition is satisfied.

**Reasons for discarding/deferring**

There is good candidate event that would re-trigger the job start as a change
in one workload (that got the `StartedUp` condition) would need to trigger
the job start in the admitted workload. One relatively easy solution would be
to create a goroutine which periodically monitors if the condition is satisfied,
but it seems an unnecessary complication as it would leak the implementation
details of Kueue scheduling to the Kueue job controller.

#### Block job start only if two workloads overlap in flavors

One possible optimization would be to allow admission of multiple workloads
with `startupRequirements=AllPodsReady` and allow their concurrent scheduling if they
use disjoint flavors.

**Reasons for discarding/deferring**

Allowing multiple workloads with `startupRequirements=AllPodsReady` introduces a
complication as it requires implementation of a queuing mechanism of admitted
workloads. It might be done in the future.

#### Pod Resource Reservation

Pod Resource Reservation (https://docs.google.com/document/d/1sbFUA_9qWtorJkcukNULr12FKX6lMvISiINxAURHNFo/edit#)
is another mechanism, currently under discussion, that could ensure all pods get
the resources assigned.

**Reasons for discarding/deferring**

The mechanism is in early design phase and requires changes to the core Kubernetes,
meaning that it is at least 6 months to be available by default in Kubernetes
(two release cycles, for Alpha and Beta versions). While this might be a viable
long-term solution we aim for a solution which can be adopted by users much
earlier. Additionally, in this work we aim to introduce APIs which will be easy
to adapt in the future to use a different underlying mechanism.

#### Use LocalQueue to default the Workload fields

We could use LocalQueue to set defaults for the Workload fields, with the
`spec.startupRequirements` field as first example. The defaults would be set when
a workload is created. This is the possible extension of the LocalQueueSpec:

```golang
// WorkloadDefaults defines the set of workload defaults
type WorkloadDefaults struct {
  ...
  // StartupRequirements describes the default value for the workload
  // startupRequirements field.
  StartupRequirements *StartupRequirements
}

// LocalQueueSpec defines the desired state of LocalQueue
type LocalQueueSpec struct {
  ...
  // WorkloadDefaults describes the set of values used as defaults for
  // workload fields
  WorkloadDefaults *WorkloadDefaults
}
```

**Reasons for discarding/deferring**

The support for the all-or-nothing scheduling is likely to evolve in the future
with the new API for Pod Resource Reservation. Thus, we want to keep the API
commitments small. It might be harder to withdraw from commitments to the
LocalQueue API, than to deprecate a Job annotation.
