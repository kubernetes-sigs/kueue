# KEP-168: Pending workloads visibility

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
    - [Too large objects](#too-large-objects)
    - [Status updates for pending workloads slowing down other operations](#status-updates-for-pending-workloads-slowing-down-other-operations)
    - [Large number of API requests triggered after workload admissions](#large-number-of-api-requests-triggered-after-workload-admissions)
- [Design Details](#design-details)
  - [Local Queue API](#local-queue-api)
  - [Cluster Queue API](#cluster-queue-api)
  - [Configuration API](#configuration-api)
  - [In-memory snapshot of the ClusterQueue](#in-memory-snapshot-of-the-clusterqueue)
  - [Throttling of status updates](#throttling-of-status-updates)
  - [Choosing the limits and defaults for MaxCount](#choosing-the-limits-and-defaults-for-maxcount)
  - [Limitation of the approach](#limitation-of-the-approach)
  - [Test Plan](#test-plan)
      - [Prerequisite testing updates](#prerequisite-testing-updates)
    - [Unit Tests](#unit-tests)
    - [Integration tests](#integration-tests)
  - [Graduation Criteria](#graduation-criteria)
    - [Alpha](#alpha)
    - [Deprecation](#deprecation)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
  - [Alternative approaches](#alternative-approaches)
    - [Coarse-grained ordering information per workload in workload status](#coarse-grained-ordering-information-per-workload-in-workload-status)
    - [Ordering information per workload in events or metrics](#ordering-information-per-workload-in-events-or-metrics)
    - [On-demand http endpoint](#on-demand-http-endpoint)
  - [Alternatives within the proposal](#alternatives-within-the-proposal)
    - [Unlimited MaxCount parameter](#unlimited-maxcount-parameter)
    - [Expose the pending workloads only for LocalQueues](#expose-the-pending-workloads-only-for-localqueues)
    - [Do not expose ClusterQueue positions in LocalQueues](#do-not-expose-clusterqueue-positions-in-localqueues)
    - [Use self-balancing search trees for ClusterQueue representation](#use-self-balancing-search-trees-for-clusterqueue-representation)
<!-- /toc -->

## Summary

The enhancement extends the API of LocalQueue and ClusterQueue to expose the
information about the order of their pending workloads.

## Motivation

Currently, there is no visibility of the contents of the queues. This is
problematic for Kueue users, who have no means to estimate when their jobs will
start. Also, it is problematic for administrators, who would like to monitor
the pipeline of pending jobs, and help users to debug issues.

<!--
This section is for explicitly listing the motivation, goals, and non-goals of
this KEP.  Describe why the change is important and the benefits to users. The
motivation section can optionally provide links to [experience reports] to
demonstrate the interest in a KEP within the wider Kubernetes community.

[experience reports]: https://github.com/golang/go/wiki/ExperienceReports
-->

### Goals

- expose the order of workloads in the LocalQueue and ClusterQueue

<!--
List the specific goals of the KEP. What is it trying to achieve? How will we
know that this has succeeded?
-->

### Non-Goals

- expose the information about workload position for each pending workload in
  in case of very long queues

<!--
What is out of scope for this KEP? Listing non-goals helps to focus discussion
and make progress.
-->

## Proposal

The proposal is to extend the APIs for the status of LocalQueue and ClusterQueue
to expose the order of pending workloads. The order will be only exposed up to
some configurable depth, in order to keep the size of the information
constrained.

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

As a user of Kueue with LocalQueue visibility only, I would like to know the
position of my workload in the ClusterQueue, I have no direct visibility into.
Knowing the position, and assuming stable velocity in the ClusterQueue, would
allow me to estimate the arrival time of my workload.

#### Story 2

As an administrator of Kueue with ClusterQueue visibility I would like to be
able to check directly and compare positions of pending workloads in the queue.
This will help me to answer users' questions about their workloads.

Note that, merging the information exposed by individual local queues is not
enough, because they may be showing inconsistent data due to delays in updates.
For example, two workloads in different local queues may return the same
position in ClusterQueue.

### Notes/Constraints/Caveats (Optional)

<!--
What are the caveats to the proposal?
What are some important details that didn't come across above?
Go in to as much detail as necessary here.
This might be a good place to talk about core concepts and how they relate.
-->

### Risks and Mitigations

#### Too large objects

As the number of pending workloads is arbitrarily large there is a risk that the
status information about the workloads may exceed the etcd limit of 1.5Mi on
object size.

Exceeding the etcd limit has a risk that the LocalQueue controller updates can
fail.

In order to mitigate this risk we introduce the `MaxCount` configuration
parameter to limit the maximal number of pending workloads in the status.
Additionally, limit the maximal value of the parameter to 4000, see
also [Choosing the limits and defaults for MaxCount](#choosing-the-limits-and-defaults-for-maxcount).

We should also note that large queue objects might be problematic for the
kubernetes API server, even if the etcd limit is not exceeded. For example,
when there are many LocalQueue instances with watches, because in that case
the entire LocalQueue objects need to be sent though the watch channels.

To mitigate this risk we also extend the Kueue's user-facing documentation to
warn about setting this number high on clusters with many LocalQueue instances,
especially, when watches on the objects are used.

#### Status updates for pending workloads slowing down other operations

The operation of computing and updating the list of top pending workloads can
have a degrading impact on the overall performance of other Kueue operations.

This risk exists because the operation requires iteration over the contents of
the cluster queue, which requires a read lock on the queue. Also, positional
changes to the list of pending workloads may require more frequent updates if
attempt to keep the information up-to-date.

In order to mitigate the risk we maintain the statuses on best-effort basis,
and issue at most one update request in a configured interval,
see [throttling of status updates](#throttling-of-status-updates).

Additionally, we take periodically an in-memory snapshot of the ClusterQueue to
allow generation of the status with `MaxCount` elements for LocalQueues and
ClusterQueues without taking the read lock for a prolonged time:
[In-memory snapshot of the ClusterQueue](#In-memory snapshot of the ClusterQueue).

#### Large number of API requests triggered after workload admissions

In a scenario when we have multiple LocalQueues pointing to the same
ClusterQueue a workload that is admitted in one LocalQueue shifts positions of
pending workloads in other LocalQueues. In the worst case scenario updating the
LocalQueue statuses with new positions requires as many API requests as the
number of LocalQueues. In particular, sending over 100 requests after workload
admission would degrade Kueue performance.

First, we propose to batch the LocalQueue updates by time intervals. This helps
to avoid sending API requests per LocalQueue if the positions are shifted
multiple times in a short period of time.

Second, we introduce the `MaxPosition` parameter configuration parameter. With
this parameter, the number of LocalQueues requiring an update can be controlled,
because only LocalQueues with workloads at the top positions require an update.

Finally, setting the `MaxCount` parameter for LocalQueues to 0 allows to stop
visibility updates to LocalQueues.

<!--
What are the risks of this proposal, and how do we mitigate? Think broadly.
For example, consider both security and how this will impact the larger
Kubernetes ecosystem.

How will security be reviewed, and by whom?

How will UX be reviewed, and by whom?

Consider including folks who also work outside the SIG or subproject.
-->

## Design Details

The APIs of the status for LocalQueue and ClusterQueue are extended by
structures which contain the list of pending workloads. In case of LocalQueue,
also the workload position in the ClusterQueue is exposed.

Updates to the structures are throttled, allowing for at most one update within
a configured interval. Additionally, we take periodically an in-memory snapshot
of the ClusterQueue.

### Local Queue API

```golang
// LocalQueuePendingWorkload contains the information identifying a pending
// workload in the local queue.
type LocalQueuePendingWorkload struct {
	// Name indicates the name of the pending workload.
	Name string

	// Position indicates the position of the workload in the cluster queue.
	Position *int32
}

type LocalQueuePendingWorkloadsStatus struct {
	// Head contains the list of top pending workloads.
	// +listType=map
	// +listMapKey=name
	// +optional
	Head []LocalQueuePendingWorkload

	// LastChangeTime indicates the time of the last change of the structure.
	LastChangeTime metav1.Time
}

// LocalQueueStatus defines the observed state of LocalQueue
type LocalQueueStatus struct {
...
	// PendingWorkloadsStatus contains the information exposed about the current
	// status of pending workloads in the local queue.
	// +optional
	PendingWorkloadsStatus *LocalQueuePendingWorkloadsStatus
...
}
```

### Cluster Queue API

```golang
// ClusterQueuePendingWorkload contains the information identifying a pending workload
// in the cluster queue.
type ClusterQueuePendingWorkload struct {
	// Name indicates the name of the pending workload.
	Name string

	// Namespace indicates the name of the pending workload.
	Namespace string
}

type ClusterQueuePendingWorkloadsStatus struct {
	// Head contains the list of top pending workloads.
	// +listType=map
	// +listMapKey=name
	// +listMapKey=namespace
	// +optional
	Head []ClusterQueuePendingWorkload

	// LastChangeTime indicates the time of the last change of the structure.
	LastChangeTime metav1.Time
}

// ClusterQueueStatus defines the observed state of ClusterQueueStatus
type ClusterQueueStatus struct {
...
	// PendingWorkloadsStatus contains the information exposed about the current
	// status of the pending workloads in the cluster queue.
	// +optional
	PendingWorkloadsStatus *ClusterQueuePendingWorkloadsStatus
...
}
```

### Configuration API

```golang
// Configuration is the Schema for the kueueconfigurations API
type Configuration struct {
...
	// QueueVisibility is configuration to expose the information about the top
	// pending workloads.
	QueueVisibility *QueueVisibility
}

type QueueVisibility struct {
	// LocalQueues is configuration to expose the information
	// about the top pending workloads in the local queue.
	LocalQueues *LocalQueueVisibility

	// ClusterQueues is configuration to expose the information
	// about the top pending workloads in the cluster queue.
	ClusterQueues *ClusterQueueVisibility

	// UpdateInterval specifies the time interval for updates to the structure
	// of the top pending workloads in the queues.
	// Defaults to 5s.
	UpdateInterval time.Duration
}

type LocalQueueVisibility struct {
	// MaxCount indicates the maximal number of pending workloads exposed in the
	// local queue status. When the value is set to 0, then LocalQueue visibility
	// updates are disabled.
	// The maximal value is 4000.
	// Defaults to 10.
	MaxCount int32

	// MaxPosition indicates the maximal position of the workload in the cluster
	// queue returned in the head.
	MaxPosition *int32
}

type ClusterQueueVisibility struct {
	// MaxCount indicates the maximal number of pending workloads exposed in the
	// cluster queue status.  When the value is set to 0, then LocalQueue
	// visibility updates are disabled.
	// The maximal value is 4000.
	// Defaults to 10.
	MaxCount int32
}
```

### In-memory snapshot of the ClusterQueue

In order to be able to quickly compute the top pending workloads per LocalQueue,
without a need for a prolonged read lock on the ClusterQueue, we create
periodically in-memory snapshot of the ClusterQueue, organized as a map
from the LocalQueue to the list of workloads belonging to the ClusterQueue,
along with their positions. Then, the LocalQueue and ClusterQueue controllers
do lookup into the cached structure.

The snapshots are taken periodically, per ClusterQueue, by multiple workers
processing a queue of snapshot-taking tasks. The tasks are re-enqueued to the
queue with `QueueVisibility.UpdateInterval` delay just after taking the previous
snapshot for as long as a given ClusterQueue exists.

The model of using snapshot workers allows to control the number of snapshot
updates after Kueue startup, and thus cascading ClusterQueues updates. The
number of workers is 5.

Note that taking the snapshot requires taking the ClusterQueue read lock
only for the duration of copying the underlying heap data

When `MaxCount` for both LocalQueues and ClusterQueues is 0, then the feature
is disabled, and the snapshot is not computed.

### Throttling of status updates

The updates to the structure of top pending workloads for LocalQueue (or
ClusterQueue) are managed by the LocalQueue controller (or ClusterQueue controller)
and are part of regular status updates of the queue.

The updates to the structure of the pending workloads are generated based on the
periodically taken snapshot.

In particular, when LocalQueue reconciles, and the `LastChangeTime` indicates
that `QueueVisibility.UpdateInterval` elapsed, then we generate the new structure
based on the snapshot. If there is a change to the structure, then `LastChangeTime`
is bumped, and the request is sent. If there is no change to the structure,
then the controller enqueues another reconciliation when the snapshot will be
regenerated.

### Choosing the limits and defaults for MaxCount

One constraining factor for the default for `MaxCount` is the maximal object
size for etcd, see [Too large objects](#too-large-objects).

A similar consideration was done for the [Backoff Limit Per Index](https://github.com/kubernetes/enhancements/tree/master/keps/sig-apps/3850-backoff-limits-per-index-for-indexed-jobs#the-job-object-too-big)
feature where we set the parameter limits to constrain the size of the object in
the worst case scenario around 500Ki. Such approach allows to stay relatively
far from the 1.5Mi limit, and allow future extensions of the structures.

Following this approach in case of Kueue we are limiting the `MaxCount`
parameter to `4000` for ClusterQueues and LocalQueues. This translates to
around `4000*63*2=0.48Mi` for ClusterQueues, and `4000*(63+4)=0.26Mi` for
LocalQueues.

The defaults are tuned for lower-scale usage in order to minimize the risk of
issues on upgrading Kueue, as the feature is going to be enabled by default.
For comparison, the Backoff Limit Per Index, the feature is opted-in per Job, so
the consequences of issues are smaller that when the feature is enabled for
all workloads.

Similarly, we default the `MaxPosition` configuration parameter for LocalQueues
to `10`. This parameter allows to control the number of LocalQueues which
are updated after a workload admission (see also:
[Large number of API requests triggered after workload admissions](#large-number-of-api-requests-triggered-after-workload-admissions)).

Enabling the feature by default will allow more users to discover the feature.
Then, based on their needs and setup they can increase the `MaxCount` and
`MaxPosition` parameters.

### Limitation of the approach

We acknowledge the limitation of the proposed approach that only to N workloads
are exposed. This might be problematic for some large-scale setups.

This means that the feature may be superseded by one of the
[Alternative approaches](#alternative-approaches) in the future, and potentially
be deprecated.

Still, we believe it makes sense to proceed with the proposed approach as it is
relatively simple to implement, and will already start providing value to
the Kueue users with relatively small setups.

Finally, the proposed solution is likely to co-exist with another alternative,
because it would be advantageous in a smaller scale. Finally, the internal code
extensions, such as the in-memory snapshot for the ClusterQueue, are likely to
be reused as a building block for other approaches.

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

- `<package>`: `<date>` - `<test coverage>`

#### Integration tests

The integration tests will cover scenarios:
- the local queue status is updated when a workload in this local queue is added,
  preempted or admitted,
- the addition of a workload to one local queue triggers and update of the
  structure in another local queue connected with the same cluster queue,
- changes of the workload positions beyond the configured threshold for top
  pending workloads don't trigger an update of the pending workloads status.

<!--
Describe what tests will be added to ensure proper quality of the enhancement.

After the implementation PR is merged, add the names of the tests here.
-->

### Graduation Criteria

#### Alpha

First iteration (0.5):

- support visibility for ClusterQueues

Second iteration (0.6):

- support visibility for LocalQueues, but without positions,
  to avoid the complication of avoiding the risk [Large number of API requests triggered after workload admissions](#large-number-of-api-requests-triggered-after-workload-admissions)

Third iteration (0.7):

- reevaluate the need for exposing positions and support if needed

#### Deprecation

We deprecate the feature, and going to remove it in feature versions of the API,
in favor of monitoring pending workloads using the [on-demand visibility API](https://kueue.sigs.k8s.io/docs/tasks/manage/monitor_pending_workloads/pending_workloads_on_demand/).

First iteration (0.9):

- deprecate the QueueVisibility feature gate and corresponding API.

Second iteration (v1beta2):

- remove the QueueVisibility feature gate,
- remove PendingWorkloadsStatus field from ClusterQueue object and QueueVisibility field from Config object on bumping the API version.

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

### Alternative approaches

The alternatives are designed to solve the limitation for the maximal number of
pending workloads which is returned in the status.

#### Coarse-grained ordering information per workload in workload status

The idea is to distribute the ordering information among workloads to avoid
keeping the ordering information centralized, thus avoiding creating objects
constrained by the etcd limit.

The main complication with distributing the ordering information is that a
workload admission, or a new workload with a high priority can move the entire
ordering, warranting update requests to all workloads in the queue. This could
mean cascades of thousands of requests after such event.

The proposal to control the number of update requests to workloads when a
workload is admitted or added, is to bucket workload positions. The bucket
intervals could grow exponentially, allowing for logarithmic number of requests
needed. With this approach, the number of requests to update workloads is limited
by the number of buckets, as only the workloads on bucket boundary are updated.

The update requests could be sent by a periodic routine which iterates over the
cluster queue and triggers workload reconciliation for workloads for which the
ordering is changed.

Pros:
- allows to expose the ordering information for all workloads, guaranteeing the
  user to know its workload position even if it is beyond the top N threshold
  in the proposed approach.

Cons:
- it requires a substantial number of requests when a workload is admitted, or
  a high priority workload is inserted. For example, assuming 1000 workloads,
  and exponential bucketing with base 2, this is 10 requests.
- it is not clear if the coarse-grained information would satisfy user
  expectations. For example, a user may need to wait long to observe reduction
  of a bucket.
- an external system which wants to display a pipeline of workloads needs to
  fetch all workloads. Similarly, as system which wants to list top 10 workloads
  may need to query all workloads.
- a natural extension of the mechanism to return ETA in the workload status
  may also increase the number of requests in a less controlled way.

#### Ordering information per workload in events or metrics

The motivation for this approach is similar as for distributing the information
in workload statuses. However, it builds on the assumption that update requests
are more costly than events or metric updates. For example, sending events or
updating metrics does not trigger a workload reconciliation.

Pros:
- more lightweight than updating workload status,

Cons:
- the API based on events or metrics would be less convenient to end users than
  object-based.
- probably still requires bucketing, thus inheriting the usability cons related
  to bucking from the workload status approach.

#### On-demand http endpoint

The idea is that Kueue exposes an endpoint which allows to fetch the ordering
information for all pending workloads, or for a selected workloads.

Pros:
- eliminates wasting QPS for updating kubernetes objects

Cons:
- the API will lack of the API server features, such as watches or P&F throttling,
  load-balancing. Also, the ensuring security of the new workload might be
  more involving, making it technically challenging.

One possible way of to deal with the security concern of
[On-demand http endpoint](#on-demand-http-endpoint) is to use
[Extension API Server](https://kubernetes.io/docs/tasks/extend-kubernetes/setup-extension-api-server/),
exposed via
[API Aggregation Layer](https://kubernetes.io/docs/concepts/extend-kubernetes/api-extension/apiserver-aggregation/).
Then, the aggregation layer could take the responsibility of authenticating and
authorizing the requests.

### Alternatives within the proposal

Here are some alternatives to solve smaller problems within the realm of the
proposal.

#### Unlimited MaxCount parameter

The `MaxCount` parameter constrains the maximal size of the ClusterQueue and
LocalQueue statuses to ensure that the object size limit of etcd is not exceeded,
see [Too large objects](#too-large-objects).

The actual maximal number might depends of the lengths of the names of namespaces
and names. Such names typically will be far from the maximum. In particular,
the namespaces might be created based on team names, which may have an internal
policy of not exceeding, say 100, characters. In that case, the estimation
would be too constraining. We propose to add a soft warning when 2000 is
exceeded, and warn in documentation.

**Reasons for discarding/deferring**

Setting hard limits for the parameters allows to avoid users to crash their
systems. We will re-evaluate the decision based on users feedback. One alternative
is to make the limit soft, rather than hard. Another is to implement and support
another alternative solution for large-scale usage.

#### Expose the pending workloads only for LocalQueues

It was proposed, that for administrators, with full access to the cluster we
could have an alternative approaches, which don't involve the status of the
ClusterQueue.

**Reasons for discarding/deferring**

The solution proposed for LocalQueues is easy to transfer for ClusterQueues.
Developing another approach just focused on admins might be problematic.

#### Do not expose ClusterQueue positions in LocalQueues

It was proposed, that without exposing the positions in the cluster queues we
don't need to update LocalQueues when workloads from another LocalQueue are
admitted, or added to. Additionally, the positional information does not reveal
much about the actual time to admit the workloads, the other workloads might
be small or big.

**Reasons for discarding/deferring**

First, getting to know the positional information gives some hits about the
expected arrival time. Especially as users of the systems gain some experience
about the velocity of the ClusterQueue. In particular, it could be estimated,
based on historical data, data that 10 workloads are admitted every 1h. This
makes already a difference if a user knows that its workload is positioned
1 or 100.

With the throttling for updating the list of pending workloads the
change in positional information will not trigger too many status updates.

Also, even without positional information it is possible that an update is
needed because while one workload is admitted another one is added. Such
situations would require additional updates, so we should introduce some
throttling mechanism for updates.

#### Use self-balancing search trees for ClusterQueue representation

Using self-balancing search trees for ClusterQueue could be used to quickly
provide the list of top workloads in ClusterQueue.

**Reasons for discarding/deferring**

It does not solve the issue of exposing the information for LocalQueues. If
we have many (or just multiple) LocalQueues pointing to the same ClusterQueue,
each of them would need to take a read lock for the iteration, and potentially
iterate over the entire ClusterQueue.

<!--
What other approaches did you consider, and why did you rule them out? These do
not need to be as detailed as the proposal, but should include enough
information to express the idea and why it was not acceptable.
-->
