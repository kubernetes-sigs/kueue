# KEP-1432: AdmissionChecks per ResouceFlavor

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
    - [Unit Tests](#unit-tests)
    - [Integration tests](#integration-tests)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
<!-- /toc -->

## Summary

This KEP introduces assigning AdmissionChecks to a ResourceFlavor.

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

Currently, AdmissionChecks are only assigned to a ClusterQueue. This results in running `AdmissionChecks` regardless of
the ResourceFlavor used by a Workload, even when its unnecessary. We would like to have an ability to assign AdmissionChecks
to a specific ResourceFlavor, so the whole mechanism is more flexible and efficient.


<!--
This section is for explicitly listing the motivation, goals, and non-goals of
this KEP.  Describe why the change is important and the benefits to users. The
motivation section can optionally provide links to [experience reports] to
demonstrate the interest in a KEP within the wider Kubernetes community.

[experience reports]: https://github.com/golang/go/wiki/ExperienceReports
-->

### Goals
- Support running AdmissionChecks only when the ResourceFlavor is used.
- Reduce the number of unnecessary AdmissionChecks.

<!--
List the specific goals of the KEP. What is it trying to achieve? How will we
know that this has succeeded?
-->

### Non-Goals

<!--
What is out of scope for this KEP? Listing non-goals helps to focus discussion
and make progress.
-->

## Proposal

Add new field to FlavorQuota API, so it contains list of AdmissionChecks thatshould run when the ResourceFlavor is used.

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
As a user who has reserved machines at my cloud provider I would like to use them first. If they are not obtainable then
switch to spot machines, using ProvisioningRequest. It only makes sense to run Provisioning AdmissionCheck when the ResourceFlavor is Spot.



### Notes/Constraints/Caveats (Optional)
All `AdmissionChecks` assigned to a Workload must have different controllers.

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
We propose adding a new field to `FlavorQuota` API called `AdmissionChecks` in following manner:

```
type FlavorQuotas struct {
  // name of this flavor. The name should match the .metadata.name of a
  // ResourceFlavor. If a matching ResourceFlavor does not exist, the
  // ClusterQueue will have an Active condition set to False.
  Name ResourceFlavorReference `json:"name"`  

  // resources is the list of quotas for this flavor per resource.
  // There could be up to 16 resources.
  // +listType=map
  // +listMapKey=name
  // +kubebuilder:validation:MinItems=1
  // +kubebuilder:validation:MaxItems=16
  Resources []ResourceQuota `json:"resources"`

  // admissionChecks is the list of AdmissionChecks that should run when the ResourceFlavor is used.
  // No two AdmissionChecks should have the same controller.
  // +listType=set
  AdmissionChecks []string `json:"admissionchecks"`
}
```

At the same time, we want to preserve the existing `AdmissionChecks` field in `ClusterQueue` API. A Workload should have
assigned `AdmissionChecks` both from `ClusterQueue` and `FlavorQuota` APIs. In case two `AdmissionChecks` have the same
controller, the one from `FlavorQuota` API should take over.

This enables having default `AdmissionChecks` that apply to all Workloads submitted to a `ClusterQueue`, and more
specific ones that apply only to Workloads using a specific `ResourceFlavor`.


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

[X] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.

<!-- ##### Prerequisite testing updates -->

<!--
Based on reviewers feedback describe what additional tests need to be added prior
implementing this enhancement to ensure the enhancements have also solid foundations.
-->

#### Unit Tests

Test Workload's Controller's function that assigns AdmissionChecks to a Workload
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

<!-- - `<package>`: `<date>` - `<test coverage>` -->

#### Integration tests
- Create 2 ResourceFlavors with associated 2 AdmissionCheck and test if a Workload contains the AdmissionCheck associated with one of the ResourceFlavors;
- Create 2 AdmissionChecks with different controllers, one associated with ResourceFlavor and the other with ClusterQueue. Test if a Workload contains both;
- Create 2 AdmissionChecks with the same controller, one associated with ResourceFlavor and the other with ClusterQueue. Test if a Workload contains one associated with ResourceFlavor;

<!--
Describe what tests will be added to ensure proper quality of the enhancement.

After the implementation PR is merged, add the names of the tests here.
-->

<!-- ### Graduation Criteria -->

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

<!-- ## Implementation History -->

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
Alternatively we could change `AdmissionChecks` API so it contained selector with a list of `ResourceFlavors` to which
it should be assigned. There are no strong pros or cons for either approach. We chose the current approach because we
believe it is more intuitive for users.

<!--
What other approaches did you consider, and why did you rule them out? These do
not need to be as detailed as the proposal, but should include enough
information to express the idea and why it was not acceptable.
-->
