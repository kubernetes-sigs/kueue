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
  - [Notes/Constraints/Caveats (Optional)](#notesconstraintscaveats-optional)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [Kueue Configuration API](#kueue-configuration-api)
  - [PodsReady workload condition](#podsready-workload-condition)
  - [Waiting for PodsReady condition](#waiting-for-podsready-condition)
  - [Timeout on reaching the PodsReady condition](#timeout-on-reaching-the-podsready-condition)
  - [Test Plan](#test-plan)
      - [Prerequisite testing updates](#prerequisite-testing-updates)
    - [Unit Tests](#unit-tests)
    - [Integration tests](#integration-tests)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
    - [Delay job start instead of workload admission](#delay-job-start-instead-of-workload-admission)
    - [Pod Resource Reservation](#pod-resource-reservation)
    - [More granular configuration to enable the mechanism](#more-granular-configuration-to-enable-the-mechanism)
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
match the configured cluster quota. The same pair of jobs could run to
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
- a timeout on replacing a failed pod

<!--
List the specific goals of the KEP. What is it trying to achieve? How will we
know that this has succeeded?
-->

### Non-Goals

- guarantee that two jobs would not schedule pods concurrently. Example
scenarios in which two jobs may still concurrently schedule their pods:
  - when succeeded pods are replaced with new because job's parallelism is less than its completions.

<!--
What is out of scope for this KEP? Listing non-goals helps to focus discussion
and make progress.
-->

## Proposal

We introduce a mechanism to ensure jobs get their physical resources
assigned by avoiding concurrent scheduling of their pods. More precisely, we
block admission of new workloads until the first batch of pods for the
unsuspended job is scheduled. Additionally we limit recovery time if any of
Pods fails during runtime. If the Pod doesn't recover in time, the Workload will be requeued.
This behavior can be opted-in at the level of the Kueue configuration.

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

As a Kueue administrator I want to ensure that two or more Jobs, which require
all pods to be running at the same time, would not deadlock when scheduling
their pods. This could happen in case of node provisioning issues to match
the configured cluster queue quota and when the Jobs don't specify priorities
(or specify the same priority).

My use case can be supported by enabling `waitForPodsReady` in the Kueue
configuration.

#### Story 2

As a Kueue administrator I want to ensure that a Workload will be evicted after
configured timeout if a pod fails during its execution and the replacement Pod can't be scheduled.

### Notes/Constraints/Caveats (Optional)

<!--
What are the caveats to the proposal?
What are some important details that didn't come across above?
Go in to as much detail as necessary here.
This might be a good place to talk about core concepts and how they relate.
-->

### Risks and Mitigations

If a workload fails to schedule its pods it could block admission of other
workloads indefinitely.

To mitigate this issue we introduce a timeout on reaching the `PodsReady`
condition by a workload since its job start, and a timeout on reaching the `PodsReady` condition since its pod has failed
 (see:[Timeout on reaching the PodsReady condition](#timeout-on-reaching-the-podsready-condition)).

There's a risk that a Pod will fail and recover in an infinite loop, with each recovery happening within the configured time. In this case, the Workload won't get requeued.

To mitigate this risk, users can do a number of things. First, users should use job types that specify
[`backoffLimit`](https://kubernetes.io/docs/concepts/workloads/controllers/job/#handling-pod-and-container-failures).
An admin can enforce such a requirement by a customized webhook or [Validating Admission Policy](https://kubernetes.io/docs/reference/access-authn-authz/validating-admission-policy/).
Additionally, a user can specify [`maximumExecutionTime`](https://github.com/kubernetes-sigs/kueue/tree/main/keps/3125-maximum-execution-time)
to prevent a Workload from running indefinitely.

Another possible mitigation for this would to partially disable the timeout.

All of the above mitigation will be documented on Kueue website.

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

### Kueue Configuration API

We extend the global Kueue Configuration API to introduce the new fields:
`waitForPodsReady` to opt-in and configure the new behavior.

```golang
// Configuration is the Schema for the kueueconfigurations API
type Configuration struct {
  ...
	// WaitForPodsReady is configuration to provide a time-based all-or-nothing
	// scheduling semantics for Jobs, by ensuring all pods are ready (running
	// and passing the readiness probe) within the specified time. If the timeout
	// is exceeded, then the workload is evicted.
	WaitForPodsReady *WaitForPodsReady `json:"waitForPodsReady,omitempty"`
}

type WaitForPodsReady struct {
	// Enable when true, indicates that each admitted workload
	// blocks admission of other workloads in the cluster, until it is in the
	// `PodsReady` condition. If false, all workloads start as soon as they are
	// admitted and do not block admission of other workloads. The PodsReady
	// condition is only added if this setting is enabled. If unspecified,
	// it defaults to false.
	Enable *bool `json:"enable,omitempty"`

	// Timeout defines the time for an admitted workload to reach the
	// PodsReady=true condition. When the timeout is exceeded, the workload
	// evicted and requeued in the same cluster queue.
	// Defaults to 5min.
	// +optional
	Timeout *metav1.Duration `json:"timeout,omitempty"`

	// BlockAdmission when true, cluster queue will block admissions for all
	// subsequent jobs until the jobs reach the PodsReady=true condition.
	// This setting is only honored when `Enable` is set to true.
	BlockAdmission *bool `json:"blockAdmission,omitempty"`

	// RequeuingStrategy defines the strategy for requeuing a Workload.
	// +optional
	RequeuingStrategy *RequeuingStrategy `json:"requeuingStrategy,omitempty"`

	// RecoveryTimeout defines an opt-in timeout, measured since the
	// last transition to the PodsReady=false condition after a Workload is Admitted and running.
	// Such a transition may happen when a Pod failed and the replacement Pod
	// is awaited to be scheduled.
	// After exceeding the timeout the corresponding job gets suspended again
	// and requeued after the backoff delay. The timeout is enforced only if waitForPodsReady.enable=true.
	// If not set, there is no timeout.
	// +optional
	RecoveryTimeout *metav1.Duration `json:"recoveryTimeout,omitempty"`
}

type RequeuingStrategy struct {
	// Timestamp defines the timestamp used for re-queuing a Workload
	// that was evicted due to Pod readiness. The possible values are:
	//
	// - `Eviction` (default) indicates from Workload `Evicted` condition with `PodsReadyTimeout` reason.
	// - `Creation` indicates from Workload .metadata.creationTimestamp.
	//
	// +optional
	Timestamp *RequeuingTimestamp `json:"timestamp,omitempty"`

	// BackoffLimitCount defines the maximum number of re-queuing retries.
	// Once the number is reached, the workload is deactivated (`.spec.activate`=`false`).
	// When it is null, the workloads will repeatedly and endless re-queueing.
	//
	// Every backoff duration is about "b*2^(n-1)+Rand" where:
	// - "b" represents the base set by "BackoffBaseSeconds" parameter,
	// - "n" represents the "workloadStatus.requeueState.count",
	// - "Rand" represents the random jitter.
	// During this time, the workload is taken as an inadmissible and
	// other workloads will have a chance to be admitted.
	// By default, the consecutive requeue delays are around: (60s, 120s, 240s, ...).
	//
	// Defaults to null.
	// +optional
	BackoffLimitCount *int32 `json:"backoffLimitCount,omitempty"`

	// BackoffBaseSeconds defines the base for the exponential backoff for
	// re-queuing an evicted workload.
	//
	// Defaults to 60.
	// +optional
	BackoffBaseSeconds *int32 `json:"backoffBaseSeconds,omitempty"`

	// BackoffMaxSeconds defines the maximum backoff time to re-queue an evicted workload.
	//
	// Defaults to 3600.
	// +optional
	BackoffMaxSeconds *int32 `json:"backoffMaxSeconds,omitempty"`
}

type RequeuingTimestamp string

const (
	// CreationTimestamp timestamp (from Workload .metadata.creationTimestamp).
	CreationTimestamp RequeuingTimestamp = "Creation"

	// EvictionTimestamp timestamp (from Workload .status.conditions).
	EvictionTimestamp RequeuingTimestamp = "Eviction"
)

```

### PodsReady workload condition

We introduce a new workload condition, called `PodsReady`, to indicate
if the workload's startup requirements are satisfied. More precisely, for a batch/v1 Job we add
the condition when `job.status.ready + len(job.status.uncountedTerimnatedPods.succeeded) + job.status.succeeded` is greater or equal
than `job.spec.parallelism`.

Note that we count `job.status.uncountedTerminatedPods` - this is meant to prevent flickering of the `PodsReady` condition when pods are transitioning to the `Succeeded` state.
This applies only for batch/v1 Job, since Kueue doesn't count active pods for other Job types, and delegate it to third-party Job types operators.

Note that, we don't take failed pods into account when verifying if the
`PodsReady` condition should be added. However, a buggy admitted workload is
eliminated as the corresponding job fails due to exceeding the `.spec.backoffLimit`
limit.

The `PodsReady` condition is added to the workload by the Kueue's Job
Controller in reaction to a status update of the corresponding Job. Note that,
verifying if the condition should be added does not require an extra API call as
the Kueue's Job Controller already fetches the latest Job object at the
beginning of the `Reconcile` function.

This condition is added only when `waitForPodsReady` is enabled in the
Kueue configuration.

### Waiting for PodsReady condition

When the mechanism is enabled, for each admitted workload Kueue's scheduler
blocks admission of queued workloads until the workload has the `PodsReady`
condition. Kueue's scheduler verifies the workload state by a lookup to the
cache of admitted workloads.

Note that, because the mechanism is enabled for all workloads, when a workload
gets admitted, all other admitted workloads are already in the `PodsReady`
condition, so the corresponding job is unsuspended without further waiting.

### Timeout on reaching the PodsReady condition


We introduce two timeouts defined in the `waitForPodsReady.timeout` and `waitForPodsReady.recoveryTimeout` fields.

First one applies before the job has started. It tracks the time between job getting unsuspended for the first time (the time of unsuspending a job is marked by the Job's
`job.status.startTime` field) and reaching the `PodsReady=true` condition (marked by condition's `.lastTransitionTime`).

```mermaid
flowchart TD;
	start@{ shape: f-circ};
	id1(Suspended=true);
	id2("PodsReady=false
	waits for .timeoutSeconds");
	id3(PodsReady=true);
	id4("Suspended=true (Requeued)");

	start--Workload gets admitted-->id1;
	id1 --> id2;

	id2 --"Doesn't exceed the timeout" --> id3
	id2 --"Exceeds the timeout" --> id4
```


Second one applies when the job has already started and some pod failed while the job is running. It tracks the time between changing `PodsReady` condition to `false` and reaching the
`PodsReady=true` condition once again.

```mermaid
flowchart TD;
	start@{ shape: f-circ};
	id1(Suspended=true);
	id2("PodsReady=false(1st)");
	id3(PodsReady=true);
	id4("PodsReady=false(2nd)
	waits for
	.recoveryTimeout");
	id5("Suspended=true (Requeued)");


	start--Workload gets admitted-->id1;
	id1 --> id2;

	id2 --"Job started" --> id3
	id3 --"Pod failed"--> id4
	id4 --"Pod recovered"--> id3
	id4 --"timeout	exceeded"--> id5
```

We introduce new `WorkloadWaitForPodsStart` and `WorkloadWaitForPodsRecovery` reasons to distinguish the reasons of setting the `PodsReady=false` condition.
`WorkloadWaitForPodsStart` will be set before the job started and is replacement for the old `PodsReady` reason. 
`WorkloadWaitForPodsRecovery` will be set after the job started.

When any of the timeouts is exceeded, the Kueue's Job
Controller suspends the Job corresponding to the workload and puts into the
ClusterQueue's `inadmissibleWorkloads` list. It also updates Workload's `.requeueState` field.
When `.requeueState.count` surpasses `waitForPodsReady.requeuingBackoffLimitCount` workloads gets
deactivated and won't be requeued.

Both timeouts apply only when `waitForPodsReady` is enabled.


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

The following scenarios will be covered with integration tests when `waitForPodsReady` is enabled:
- no workloads are admitted if there is already an admitted workload which is not in the `PodsReady` condition
- a workload gets admitted if all other admitted workloads are in the `PodsReady` condition
- a workload which exceeds the `waitForPodsReady.timeout` timeout is suspended and put into the `inadmissibleWorkloads` list
- a workload which exceeds the `waitForPodsReady.recoveryTimeout` timeout is suspended and put into the `inadmissibleWorkloads` list

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
throughput significantly. Especially, if there is enough resource capacity to
which could be otherwise used to start multiple jobs at the same time.

## Alternatives

<!--
What other approaches did you consider, and why did you rule them out? These do
not need to be as detailed as the proposal, but should include enough
information to express the idea and why it was not acceptable.
-->

#### Delay job start instead of workload admission

When a workload is nominated its admission is blocked (rejected) until all the
already admitted workloads are in the `PodsReady` condition. Instead, we could
admit the workload, but delay its job start until the condition is satisfied.

**Reasons for discarding/deferring**

It would leak the implementation details of Kueue scheduling to the Kueue job
controller.

#### Pod Resource Reservation

Pod Resource Reservation (https://docs.google.com/document/d/1sbFUA_9qWtorJkcukNULr12FKX6lMvISiINxAURHNFo/edit#)
is another mechanism, currently under discussion, that could ensure all pods get
the resources assigned.

**Reasons for discarding/deferring**

The mechanism is in early design phase and requires changes to the core Kubernetes,
meaning that it is at least 8 months to be available by default in Kubernetes
(two release cycles, for Alpha and Beta versions). While this might be a viable
long-term solution we aim for a solution which can be adopted by users much
earlier. Additionally, in this work we aim to introduce APIs which will be easy
to adapt in the future to use a different underlying mechanism.

#### More granular configuration to enable the mechanism

Allowing to opt-in for this feature at more granular levels of the
Kueue API (Job level, LocalQueue, ClusterQueue, ResourceFlavor) would increase
admission throughput.

One considered option is to enable the feature per Job with a Job annotation,
however, it would increase the surface of the Job API.

Another possibility is to use LocalQueue for defaulting of the opt-in setting
for workloads submitted to the local queue. Similarly as in case of Job level
the mechanism may not be necessary in case the Job is admitted to a resource
flavor which does not require node provisioning. In that case, one can argue the
mechanism should neither be opted in at the Job nor LocalQueue level.

Further, another option is to opt-in to wait for pods ready at the ResourceFlavor
level to allow concurrent pod scheduling if the underlying resources don't require
provisioning. Here, one concern is that it would make the implementation
more involving as ResourceFlavors are assigned during workload admission, so
admission would not be blocked, but unsuspending of a Job itself. This could in
turn complicate the Kueue's Job Controller, which is responsible for Job
unsuspending.

**Reasons for discarding/deferring**

The support for the all-or-nothing scheduling is likely to evolve in the future
allowing to enable it at more granular levels of the API, however it remains
unclear which level would be best to satisfy user needs long-tern. Thus, we want
to keep the API commitments small for now.
