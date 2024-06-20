# KEP-2349: MultiKueue external custom Job support

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

This KEP introduces the ability to create Multikueue Admission Check controllers with custom set of adapters.

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

Many Kueue adaptors have company specific CustomResources.
Kueue should give a possibility to manage such CustomResources across multiple clusters.

<!--
This section is for explicitly listing the motivation, goals, and non-goals of
this KEP.  Describe why the change is important and the benefits to users. The
motivation section can optionally provide links to [experience reports] to
demonstrate the interest in a KEP within the wider Kubernetes community.

[experience reports]: https://github.com/golang/go/wiki/ExperienceReports
-->

### Goals

Allow specifying a controller and adapter for an external custom Job processed by Kueue.

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

<!--
This is where we get down to the specifics of what the proposal actually is.
This should have enough detail that reviewers can understand exactly what
you're proposing, but should not include things like API designs or
implementation. What is the desired outcome and how do we measure success?.
The "Design Details" section below is for the real
nitty-gritty.
-->

Make Multikueue admission check controller to run simultaneously with another set of Multkiueue adapters in one cluster.
Provide a custom admission check controller name and adapters for Multikueue.
Specified controller is used by workload, cluster and admission check reconcilers.

### User Stories (Optional)

<!--
Detail the things that people will be able to do if this KEP is implemented.
Include as much detail as possible so that people can understand the "how" of
the system. The goal here is to make this feel real for users without getting
bogged down.
-->

#### Story 1

I would like to support the extension mechanism so that we can implement the MultiKueue controller for the custom / in-house Jobs.

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

Provide a way to integrate external controllers to manage Jobs in the Kueue.
MulikueueCluster API needs to be modified to ensure that one multikueue cluster is only managed by one admission check controller.

1. Add ControllerName to the `MultiKueueClusterSpec`
```golang
type MultiKueueClusterSpec struct {
  ...
	// controllerName is name of the controller which will actually perform
	// the checks. This is the name with which controller identifies with,
	// not necessarily a K8S Pod or Deployment name. Cannot be empty.
	// +kubebuilder:validation:Required
	// +kubebuilder:default="kueue.x-k8s.io/multikueue"
	// +kubebuilder:validation:XValidation:rule="self == oldSelf", message="field is immutable"
	ControllerName string `json:"controllerName"`
  ...
```

2. Make Controller Name used in Multikueue admission check configurable.
Extend MultiKueue Admission Check controller options to include controllerName.

```golang
type SetupOptions struct {
  ...
	controllerName    string
  ...
}

// WithControllerName - sets the controller name for which the multikueue
// admission check match.
func WithControllerName(controllerName string) SetupOption {
	return func(o *SetupOptions) {
		o.controllerName = controllerName
	}
}

func SetupControllers(mgr ctrl.Manager, namespace string, opts ...SetupOption) error {
...
}
```

3. Make the Adapters list for the MultiKueue Admission Check controller configurable.

```golang
type SetupOptions struct {
  ...
	adapters          map[string]jobframework.MultiKueueAdapter
  ...
}

// WithAdapters - sets all the MultiKueue adapters.
func WithAdapters(adapters map[string]jobframework.MultiKueueAdapter) SetupOption {
	return func(o *SetupOptions) {
		o.adapters = adapters
	}
}

// WithAdapter - sets or updates the adapter of the MultiKueue adapters.
func WithAdapter(adapter jobframework.MultiKueueAdapter) SetupOption {
	return func(o *SetupOptions) {
		o.adapters[adapter.GVK().String()] = adapter
	}
}
```

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

Target is to keep same level or increase the coverage for all of the modified packages.
Cover the critical part of newly created code.

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

We are going to cover the following scenario:

Setup 2 distinct MultiKueue Admission Checks over the same multicluster.
Each Admission Check with different set of adapters and different controller names.
Prove that 2 different MultiKueue controllers can manage different job types runnning successfully in parallel.

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

* 2024-06-20 First draft
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
The proposed solution requires that another custom MultiKueue Admission Check is created aside from the one running for built-in Jobs.

## Alternatives

<!--
What other approaches did you consider, and why did you rule them out? These do
not need to be as detailed as the proposal, but should include enough
information to express the idea and why it was not acceptable.
-->

1. Dedicated API for MultiKueue communication between admission check controller and abstraction layer controller.

2. Don't do any code changes, a custom controller can:
Import `multikueue` and `jobframework`, add adapters and run the MultKiueue admission check controller.
Will conflict with MultiKueue admission check controller in Kueue, which should be disabled (e.g. by command line parameter) in order to work.

3. Instead of having MultiKueue cluster associated with controller name, change the Multikueue cluster status to use a custom active condition for old controllers (controller name) that use it. 
