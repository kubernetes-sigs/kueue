# KEP-752: Support Weight in localQueue for better resource partitioning

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
- [KEP-752: Support Weight in localQueue for better resource partitioning](#kep-752-support-weight-in-localqueue-for-better-resource-partitioning)
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
    - [API](#api)
    - [Support Weight in localQueue](#support-weight-in-localqueue)
    - [Local Queue Filtering and Sorting Policy Interface](#local-queue-filtering-and-sorting-policy-interface)
    - [Extension Points in Cluster Queue](#extension-points-in-cluster-queue)
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

Add a extension point to determine whether a workload in certain local queue can
be scheduled.  

## Motivation

<!--
This section is for explicitly listing the motivation, goals, and non-goals of
this KEP.  Describe why the change is important and the benefits to users. The
motivation section can optionally provide links to [experience reports] to
demonstrate the interest in a KEP within the wider Kubernetes community.

[experience reports]: https://github.com/golang/go/wiki/ExperienceReports
-->

Currently, for queues inside one CQ, we can submit queues as much as we want until the bottleneck of CQ, and one queue can occupy the whole resources with higher priorities. A limitation for a local queue is helpful for resource partitioning.

### Goals

<!--
List the specific goals of the KEP. What is it trying to achieve? How will we
know that this has succeeded?
-->

- Define an interface which supports local queues filtering and sorting
- Implement a policy to filter and sort the local queue based on the queue usage and local queue's weight

### Non-Goals

- Use min-max to guarantee the resources.

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

### User Stories (Optional)

<!--
Detail the things that people will be able to do if this KEP is implemented.
Include as much detail as possible so that people can understand the "how" of
the system. The goal here is to make this feel real for users without getting
bogged down.
-->

#### Story 1

As a tenant, I wish each tenant's workload should be scheduled fairly. No one 
can occupy the whole cluster queue by submiting a lot of workloads or seting all his 
workloads as highest level.

### Notes/Constraints/Caveats (Optional)

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

### API

We add a new CRD named `CoHort` to define the details about a `cohort`.
```golang
type CoHortSpec struct {
  Name string
  ParentCQ string
}
type CoHortStatus struct {
  Usage []FlavorUsage
}
```

If a `CoHort` belongs to any ClusterQueue, sum of the nominal resources 
for CQs with `CQ.cohort=CoHort.name` must less or equal than the nominal 
resources of the parent cluster queue.

We will prevent any local queue point to a parent cluster queue to only
allow submitting workloads to leaf cluster queues.

And we will add a weight property in local queue to allow setting weights.
We will allow users to change the property, and use `XValidation` to prevent
from users to change cluster queue field.
```golang
// LocalQueueSpec defines the desired state of LocalQueue
type LocalQueueSpec struct {
  // +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Value is immutable"
	ClusterQueue ClusterQueueReference `json:"clusterQueue,omitempty"`
  weight *int
}
```

In this way, our users can fine tune resource sharing between their workloads, and admins
can offer multi-level hierarchy for their users.

We add a knob in cluster queue to allow users choose local queue sort and filter plugin.
```golang
type Plugin string
const Fairness Plugin = "fairness"
const None Plugin = "none"

type PluginConfig struct {
  Name string
  Args map[string]interface{}
}

// ClusterQueueSpec defines the desired state of ClusterQueue
type ClusterQueueSpec struct {
  ...
  LocalQueueFilterPlugin Plugin
  LocalQueueSortPlugin Plugin
  PluginConfig PluginConfig
}
```

We add an option in `ClusterQueuePreemption` to allow users set preemption policy when
local queue use too much resources. When we set `WithinClusterQueue` to `PreemptionPolicyExceedWeight`,
we will preempt from those local queues whose usage exceed their weight.

```golang
const PreemptionPolicyExceedWeight PreemptionPolicy = "ExceedWeight"
```

### Support Weight in localQueue

We maintain current weight for each queue. We will reject the head workload 
in a local queue whose current weight is greater than desired weight and then 
sort local queues in a cluster queue based on their current weights.
We use the dominant resource to calculate the current weight.

We calculate current weight for each queue based on the following method:
```
totalResources = sum of all admitted workloads
currentWeight = map[stirng]int64
for lq in cq:
  drfWeight = 0
  for resName, resCount in lq.Res:
    calculate resCount/totalResources[resName]
    if resCount/totalResources[resName] > drfWeight:
      drfWeight = resCount/totalResources[resName]
  currentWeight[lq.Name] = drfWeight
return currentWeight
```

### Local Queue Filtering and Sorting Policy Interface

```golang

type Plugin interface {
  Name() string
}

type LocalQueueSortPlugin interface {
  Plugin
  Less(cq ClusterQueue, lqs []LocalQueue, i, j int) int
}

type LocalQueueFilteringPlugin interface {
  Plugin
  Filter(cq ClusterQueue, lqs []LocalQueue, i int) Status
}

```

### Extension Points in Cluster Queue

We will modify `entryOrdering` to support local queue sort plugins and modify `Manager.Heads` to support local queue 
filter plugins.

Besides, we will modify `preemptor` to preempt workloads if local queue's usage exceed its weight.

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
