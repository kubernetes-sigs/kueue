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
- [KEP-582: Preempt Based On Flavor Order](#kep-582-preempt-based-on-flavor-order)
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
    - [Cluster Queue API](#cluster-queue-api)
      - [Plan A](#plan-a)
        - [Advantages](#advantages)
        - [Disadvantages](#disadvantages)
    - [Implementation](#implementation)
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
This proposal introduces an opt-in mechanism to preempt workloads based on the 
order of flavors defined in cluster queue.

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
- a mechanism to enable high priority jobs preempt low priority jobs using a flavor before considering the
  next resource flavor when scheduling

### Non-Goals

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

My use case can be supported by setting `flavorFungibility` to `BeforeNextFlavor`  in the Kueue configuration.

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

#### Plan A

```
const (
	Borrow FlavorFungibilityPolicy = "Borrow"
	Preempt  FlavorFungibilityPolicy = "Preempt"
  TryNextFlavor FlavorFungibilityPolicy = "TryNextFlavor"
)

type FlavorFungibility struct {
  // +kubebuilder:validation:Enum="Borrow,TryNextFlavor"
  WhenCanBorrow FlavorFungibilityPolicy  `json:"whenCanBorrow"`
  // +kubebuilder:validation:Enum="Preempt,TryNextFlavor"
  WhenCanPreempt FlavorFungibilityPolicy `json:"whenCanPreempt"`
}

// ClusterQueuePreemption contains policies to preempt Workloads from this
// ClusterQueue or the ClusterQueue's cohort.
type ClusterQueuePreemption struct {
	...
	FlavorFungibility FlavorFungibility `json:"flavorFungibility"`
}
```

If flavorFungibility is nil in configuration, we will set the `WhenCanBorrow` to `true` and set `WhenCanPreempt` to `false` to maintain consistency with the current behavior.

If flavorFungibility is not nil and `WhenCanBorrow` is `Borrow`, we calculate the total resource consumption in the flavor and current amount of borrowed resources to determine if the workload can fit the flavor by borrowing before try next flavor. We return `Fit` and the count of resources to be borrowed if there are enough unused resources in cohort.

If flavorFungibility is not nil and `WhenCanPreempt` is `Preempt`, we will preempt in current flavor before try allocate in next flavor. We return current assignment directly if `mode=Preempt` so that preemptor can do preemption in this flavor. If we preempt fail in the flavor, we put the worklaod back to the cluster queue and try from the first flavor next time.

If flavorFungibility is not nil and `WhenCanBorrow` is `TryNextFlavor`, we will try to borrow after all flavors have been consiedered.

If flavorFungibility is not nil and `WhenCanPreempt` is `TryNextFlavor`, we will try to preempt after all flavors have been consiedered.

If both `WhenCanPreempt` and `WhenCanBorrow` are `true`, both policy will be enabled, and we will return if any case above was hit.

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

We will first try `default-flavor1` for cpu and memory resources. If `default-flavor1` doesn't fit, we try preempt in `default-flavor1`. And if we can not find enough candidates in `default-flavor1`, the workload will start from `default-flavor1` again next time.

##### Advantages

##### Disadvantages

### Implementation

```
func (a *Assignment) findFlavorForResourceGroup(...) (ResourceAssignment, *Status) {
  ...
	for _, flvQuotas := range rg.Flavors {
		...

		whetherNeedBorrowing := false
		assignments := make(ResourceAssignment, len(requests))
		// Calculate representativeMode for this assignment as the worst mode among all requests.
		representativeMode := Fit
		for rName, val := range requests {
			...
		}

		if shouldTryNextFlavor(representativeMode, cq.FlavorFungibility, whetherNeedBorrowing) {
			return assignments, nil
		}
		if representativeMode > bestAssignmentMode {
			bestAssignment = assignments
			bestAssignmentMode = representativeMode
			// if bestAssignmentMode == Fit {
			// 	// All the resources fit in the cohort, no need to check more flavors.
			// 	return bestAssignment, nil
			// }
		}
	}
	return bestAssignment, status
}

func shouldTryNextFlavor(representativeMode FlavorAssignmentMode, flavorFungibility v1beta1.FlavorFungibility, whetherNeedBorrowing bool) bool {
	policyPreempt := flavorFungibility.WhenCanPreempt
	policyBorrow := flavorFungibility.WhenCanBorrow
	if representativeMode == Preempt && policyPreempt == v1beta1.Preempt {
		return true
	}

	if representativeMode == Fit && whetherNeedBorrowing && policyBorrow == v1beta1.Borrow {
		return true
	}

	if representativeMode == Fit && !whetherNeedBorrowing {
		return true
	}

	return false
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

- `<package>`: `<date>` - `<test coverage>`

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
