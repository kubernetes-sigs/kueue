# KEP-582: Preempt Based On Flavor Order

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
  - [Cluster Queue API](#cluster-queue-api)
  - [Behavior Changes](#behavior-changes)
  - [Implementation](#implementation)
  - [Test Plan](#test-plan)
      - [Prerequisite testing updates](#prerequisite-testing-updates)
    - [Unit Tests](#unit-tests)
    - [Integration tests](#integration-tests)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
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
This proposal introduces an opt-in mechanism to borrow quota or preempt workloads in a flavor
before trying the next flavors in the ClusterQueue.

## Motivation

<!--
This section is for explicitly listing the motivation, goals, and non-goals of
this KEP.  Describe why the change is important and the benefits to users. The
motivation section can optionally provide links to [experience reports] to
demonstrate the interest in a KEP within the wider Kubernetes community.

[experience reports]: https://github.com/golang/go/wiki/ExperienceReports
-->

The order of ResourceFlavors within a ClusterQueue represents preference of 
consumption. Jobs with higher priorities sometimes prefer to consume resources
in preferred ResourceFlavors.

### Goals

<!--
List the specific goals of the KEP. What is it trying to achieve? How will we
know that this has succeeded?
-->
- a mechanism to enable high priority jobs preempt low priority jobs using a flavor or borrow before considering the
  next resource flavor when scheduling

### Non-Goals

- change the behavior to judge whether a podset can get enough resource in certain resource flavor. 
- change the preemption and admission process.
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

As a Kueue administrator I want to ensure more important jobs running on more 
stable resources. This can happen in case that there are normal and spot instances
in my cluster. In this case I prefer my high priority jobs not running on spot 
instances. If high priority jobs can preempt jobs in standard instances before trying spot instances,
stability can be achieved.

My use case can be supported by setting `.Spec.FlavorFungibility.WhenCanPreempt` to `Preempt` in the ClusterQueue's spec.

#### Story 2

As an admin of system managed by Kueue I would like to minimize the risk that admitted workloads get preempted soon after admission. Since every borrowing workload is a preemption (reclaim) candidate, to minimize the risk, I would like to prioritize selecting flavors which are preempting rather than borrowing.

My use case can be supported by setting `Spec.FlavorFungibility.WhenCanPreempt: Preempt`.

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

### Cluster Queue API

We extend the Cluster Queue API to introduce the new fields: flavorFungibility to opt-in and configure the new behavior.

For each type of resource in each podSet, Kueue will traverse all resource groups and resource flavors to find a available flavor in present. When there are insufficient resources in the flavor, kueue will prioritize preemption or borrowing based on the configured policy. 

```
const (
  Borrow FlavorFungibilityPolicy = "Borrow"
  Preempt  FlavorFungibilityPolicy = "Preempt"
  TryNextFlavor FlavorFungibilityPolicy = "TryNextFlavor"
)

type FlavorFungibility struct {
  // whenCanBorrow determines whether a workload should try the next flavor
  // or stop the search. The possible values are:
  //
  // - `Borrow` (default): stop searching and use the best flavor found so far
  //   according to the selection strategy
  // - `TryNextFlavor`: try next flavor even if the current
  //   flavor has enough resources to borrow.
  //
  // +kubebuilder:validation:Enum={Borrow,TryNextFlavor}
  // +kubebuilder:default="Borrow"
  WhenCanBorrow FlavorFungibilityPolicy  `json:"whenCanBorrow"`
  // whenCanPreempt determines whether a workload should try the next flavor
  // or stop the search. The possible values are:
  //
  // - `Preempt`: stop searching and use the best flavor found so far 
  //   according to the selection strategy
  // - `TryNextFlavor` (default): try next flavor even if there are enough
  //   candidates for preemption in the current flavor.
  //
  // +kubebuilder:validation:Enum={Preempt,TryNextFlavor}
  // +kubebuilder:default="TryNextFlavor"
  WhenCanPreempt FlavorFungibilityPolicy `json:"whenCanPreempt"`
}

// ClusterQueueSpec defines the desired state of ClusterQueue
type ClusterQueueSpec struct {
	...
	FlavorFungibility FlavorFungibility `json:"flavorFungibility"`
}
```

If flavorFungibility is nil in configuration, we will set the `WhenCanBorrow` to `Borrow` and set `WhenCanPreempt` to `TryNextFlavor` to maintain consistency with the current behavior.

### Behavior Changes

We will not change the behavior to judge whether a podset can get enough resource in certain resource flavor. Preemption and admission will not be influenced also. We don't change the order these flavors are considered, but `WhenCanBorrow` and `WhenCanPreempt` control how many flavors are considered.

We try to schedule a podset in succesive resource flavors in a loop and we decide whether to traverse to the next flavor or break from the loop based on the `flavorFungibility`. The behavior depending on the simulated scheduling result and the configuration is as follows:

| Simulation result  |Configuration  | Behavior |
|------ | -------- | ------- |
| `NoFit` | any | continue |
| `Preempt`, `NoBorrow` | `WhenCanPreempt = TryNextFlavor` | continue |
| `Preempt`, `NoBorrow` | `WhenCanPreempt = Preempt` | break |
| `Fit`, `Borrow` | `WhenCanBorrow = TryNextFlavor` | continue |
| `Fit`, `Borrow` | `WhenCanBorrow = Borrow` | break |
| `Fit`, `NoBorrow`| any| break| 

By `Borrow`/`NoBorrow` we mean whether borrowing is required or not to fit the considered podset in the sumulation result. After we complete the loop, either by trying all the flavors or by breaking out of it, we end up with a list of possible flavors containing all the considered flavors that yielded a result different than `NoFit`. We choose the flavor to assign based on the following default preference order: (`Fit`, `NoBorrow`), (`Fit`, `Borrow`), (`Preempt`, `NoBorrow`), (`Preempt`, `Borrow`).

An alternative order of preference can be set by enabling the feature gate `FlavorFungibilityImplicitPreferenceDefault`.
The alternative order prioritizes assignments that don't borrow: (`Fit`, `NoBorrow`),(`Preempt`, `NoBorrow`), (`Fit`, `Borrow`), (`Preempt`, `Borrow`). It is used in two cases:

1. If `WhenCanPreempt = Preempt` and `WhenCanBorrow = TryNextFlavor`
2. If `WhenCanPreempt = TryNextFlavor` and `WhenCanBorrow = TryNextFlavor`

We will store the scheduling context in workload info so that we can start from where we stop in previous scheduling attempts. This will be useful to avoid to waste time in one flavor all the time if we try to preempt in a flavor and failed. Scheduling context will contain the `LastTriedFlavorIdx`, `ClusterQueueGeneration` attached to the CQ and `CohortGeneration`. Any changes to these properties will lead to a scheduling from the first flavor.

`ClusterQueueGeneration` and `CohortGeneration` mark record the resource consumption of the CQs and Cohort. Any time the available resources of the CQs or Cohort increase, we will increase the generation. So that if the Generation in scheduling context is lower, we should retry from the first flavor. Note that increasing after decreasing of the available resource will also make the generation increased, but I think this is acceptable since we can save the memory by just storing the generation instead of the usage state for each scheduling attempt.

For example, if cluster queue has 2 resource groups and workload has 1 podSet as the following:

```
...
  - coveredResources: ["cpu", "memory"]
    flavors:
    - name: "default-flavor1"
      resources:
      - name: "cpu"
        nominalQuota: 3 
      - name: "memory"
        nominalQuota: 600Mi 
    - name: "default-flavor2"
      resources:
      - name: "cpu"
        nominalQuota: 3 
      - name: "memory"
        nominalQuota: 600Mi 
  - coveredResources: ["gpu"]
    flavors:
    - name: "vendor1"
      resources:
      - name: "gpu"
        nominalQuota: 9  
    - name: "vendor2"
      resources:
      - name: "gpu"
        nominalQuota: 9  
---  
...
  podSets:
  - count: 3
    spec:
      containers:
      - ...
        resources:
          requests:
            cpu: "1"
            memory: 200Mi
            gpu: 1
```

We will first try `default-flavor1` for cpu and memory resources. If `default-flavor1` doesn't fit, we try preempt in `default-flavor1`. And if we can not find enough candidates in `default-flavor1`, the workload will start from `default-flavor2` in the next time.

### Implementation

```
func assignFlavors(log logr.Logger, requests []workload.PodSetResources, podSets []kueue.PodSet, resourceFlavors map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor, cq *cache.ClusterQueue, lastAssignment *workload.AssignmentClusterQueueState) Assignment {
	var assignment Assignment
	if lastAssignment != nil {
		assignment = Assignment{
			TotalBorrow: make(cache.FlavorResourceQuantities),
			PodSets:     make([]PodSetAssignment, 0, len(requests)),
			LastState:   *lastAssignment,
			Usage:       make(cache.FlavorResourceQuantities),
		}
	} else {
		assignment = Assignment{
			TotalBorrow: make(cache.FlavorResourceQuantities),
			PodSets:     make([]PodSetAssignment, 0, len(requests)),
			LastState: workload.AssignmentClusterQueueState{
				LastAssignedFlavorIdx:  make([]map[corev1.ResourceName]int, 0, len(podSets)),
				CohortGeneration:       0,
				ClusterQueueGeneration: cq.Generation,
			},
			Usage: make(cache.FlavorResourceQuantities),
		}
		if cq.Cohort != nil {
			assignment.LastState.CohortGeneration = cq.Cohort.Generation
		}
	}
  ...
}

func shouldTryNextFlavor(representativeMode FlavorAssignmentMode, flavorFungibility v1beta1.FlavorFungibility, whetherNeedBorrowing bool) bool {
	policyPreempt := flavorFungibility.WhenCanPreempt
	policyBorrow := flavorFungibility.WhenCanBorrow
	if representativeMode == Preempt && policyPreempt == v1beta1.Preempt {
		return false
	}

	if representativeMode == Fit && whetherNeedBorrowing && policyBorrow == v1beta1.Borrow {
		return false
	}

	if representativeMode == Fit && !whetherNeedBorrowing {
		return false
	}

	return true
}
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

[Y] I/we understand the owners of the involved components may require updates to
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

- `pkg/cache`: `2023-8-22` - `82.9%`
- `pkg/scheduler`: `2023-8-22` - `80.7%`
- `pkg/webhook`: `2023-8-22` - `71.2%`
- `pkg/workload`: `2023-8-22` - `54.9%`

#### Integration tests

<!--
Describe what tests will be added to ensure proper quality of the enhancement.

After the implementation PR is merged, add the names of the tests here.
-->
Scenarios that `WhenCanBorrow` is set as `Borrow` and `WhenCanPreempt` is set as `tryNextFlavor` are same with current behavior. So the added integration tests will these cover scenarios:

- `WhenCanBorrow` is set as `tryNextFlavor`,
- `WhenCanPreempt` is set as `Preempt`.

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

The feature gate `FlavorFungibilityImplicitPreferenceDefault` is a temporary measure
until a new shape of the API, that allows to explicitely define the preference
is implemented. The feature gate will be removed once the API is updated
(planned for kueue v0.14).

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
