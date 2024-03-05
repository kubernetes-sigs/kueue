# KEP-1778: Passing parameters to ProvisioningRequest

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
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [Test Plan](#test-plan)
    - [Unit Tests](#unit-tests)
- [Alternatives](#alternatives)
<!-- /toc -->

## Summary

This KEP proposes a method for passing parameters to ProvisioningRequests. This will allow users to pass parameters specific to individual ProvisioningRequest classes.

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
Currently, no mechanism exists to adjust these parameters for an individual Workload. Right now, users can only change the parameters with a ProvisioningRequestConfig, which then applies to all Workloads submitted to a specific ClusterQueue. This limits the usefulness/flexibility of ProvisioningRequests in Kueue.

<!--
This section is for explicitly listing the motivation, goals, and non-goals of
this KEP.  Describe why the change is important and the benefits to users. The
motivation section can optionally provide links to [experience reports] to
demonstrate the interest in a KEP within the wider Kubernetes community.

[experience reports]: https://github.com/golang/go/wiki/ExperienceReports
-->

### Goals
- Provide a mechanism to pass parameters to ProvisioningRequest

<!--
List the specific goals of the KEP. What is it trying to achieve? How will we
know that this has succeeded?
-->

### Non-Goals
- Describe how the ProvisioningRequest controller handles these parameters

<!--
What is out of scope for this KEP? Listing non-goals helps to focus discussion
and make progress.
-->

## Proposal
We propose introducing a new group of annotations with the
***provreq.kueue.x-k8s.io/ prefix***. Annotations using this prefix will be directly copied to the ProvisioningRequest's Parameters field, including both name and value.
Kueue would act in the pass through manner, meaning it would pass the parameters without checking if such parameters actually exists or validating its type/value.
The annotations are set by a user in the definition of batch/v1.Job, Kubeflow Jobs, Pods, or other objects that are abstracted by Kueue Workload.

Additionally, the annotations would override a default value set by an administrator in the ProvisioningRequestConfig.

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
As a Kueue's user I want to use ProvisioningRequest with [**atomic-scale-up.kubernetes.io class**](https://github.com/kubernetes/autoscaler/blob/master/cluster-autoscaler/proposals/provisioning-request.md#atomic-scale-upkubernetesio-class).
I would also like to pass the ```ValidUntilSeconds``` parameter. In the definition of my Job I add annotation ```provreq.kueue.x-k8s.io/ValidUntilSeconds: "60"``` to do so.

### Story 2
As an administrator I would like all workloads submitted to ClusterQueue X to use ProvisioningRequest with parameters ```ValidUntilSeconds``` set to 0.
In the definition of ProvisioningRequestConfig used by AdmissionCheck used in ClusterQueue X I specify the parameter ```ValidUntilSeconds: "0"```.
Unless a user of ClusterQueue X intentionally overrides it in the Job definition, it's set to 0.
<!--
What are the caveats to the proposal?
What are some important details that didn't come across above?
Go in to as much detail as necessary here.
This might be a good place to talk about core concepts and how they relate.
-->

### Risks and Mitigations
- R: Users can pass parameters with invalid name, type or value that exceeds ProvisioningRequest class bounds.\
  M: As long as a ProvisioningRequest controllers return some kind of error, we can propagate it into Workload's status.
  However, it's user's responsibility to provide valid parameters.

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

<!--
Based on reviewers feedback describe what additional tests need to be added prior
implementing this enhancement to ensure the enhancements have also solid foundations.
-->

#### Unit Tests
- Check if annotations are passed during Workload creation
- Check if params are passed from Workload annotation to ProvisioningRequest params

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

<!-- ## Drawbacks -->

<!--
Why should this KEP _not_ be implemented?
-->

## Alternatives
Using ProvisioningRequestConfig we could extend the annotation idea and leverage some mapping mechanism to provide validation. This could look alike:
```
  mapping:
    - annotationName: myValidUntilSecondsAnnotation
    - parameterName: ValidUntilSeconds
    - type: Int
    - minVal: 0
    - maxVal: 1
```
However, we decided to drop this idea as we believe Kueue should only act in the pass through manner, and such validation should be performed on the ProvisioningRequest side.

<!--
What other approaches did you consider, and why did you rule them out? These do
not need to be as detailed as the proposal, but should include enough
information to express the idea and why it was not acceptable.
-->
