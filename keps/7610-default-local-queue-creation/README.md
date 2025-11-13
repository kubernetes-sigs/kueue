# KEP-7610: Create Default LocalQueues Based on NamespaceSelector

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
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [API Proposal](#api-proposal)
  - [Controller Logic](#controller-logic)
  - [Admission Webhook](#admission-webhook)
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

This KEP proposes a change to the `ClusterQueue` API to introduce an opt-in
feature that automatically creates a default `LocalQueue` in namespaces that
match a `ClusterQueue`'s `namespaceSelector`. This avoids the need for administrators
to manually create a `LocalQueue` in each namespace. To implement this,
the `clusterqueue-controller` will be updated to watch for matching namespaces
and create the `LocalQueue` automatically. The entire feature will be managed
by a feature gate.

## Motivation

<!--
This section is for explicitly listing the motivation, goals, and non-goals of
this KEP.  Describe why the change is important and the benefits to users. The
motivation section can optionally provide links to [experience reports] to
demonstrate the interest in a KEP within the wider Kubernetes community.

[experience reports]: https://github.com/golang/go/wiki/ExperienceReports
-->

Kueue `ClusterQueue` can select which namespaces are allowed to submit workloads
via a `namespaceSelector`. However, this selection only serves as an admission
rule. For users to actually submit workloads, an administrator must still
manually create a `LocalQueue` resource in that namespace and point it to the
appropriate `ClusterQueue`. This manual step is repetitive and could be
automated.

Automating the creation of a default `LocalQueue` makes the administrator's
experience much smoother. When a namespace is created or labeled to match a
`ClusterQueue`'s selector, the corresponding `LocalQueue` will be provisioned
automatically, making the namespace immediately ready for workload submission.

### Goals

<!--
List the specific goals of the KEP. What is it trying to achieve? How will we
know that this has succeeded?
-->

- Automate the creation of a default `LocalQueue` within a namespace when its
  labels match a `ClusterQueue`'s `namespaceSelector`.
- Provide a clear, opt-in mechanism on the `ClusterQueue` to enable and
  configure this behavior.
- Prevent name conflicts and undefined behavior by validating that no two
  `ClusterQueue`s with this feature enabled have overlapping `namespaceSelector`s.
- Ensure that automatically created `LocalQueue`s are clearly identifiable via
  labels and annotations for easy discovery and management.

### Non-Goals

<!--
What is out of scope for this KEP? Listing non-goals helps to focus discussion
and make progress.
-->

- Automatically deleting the `LocalQueue` if a namespace no longer matches the
  selector. The initial implementation will leave the `LocalQueue` in place to
  avoid disrupting active workloads. Cleanup will remain a manual administrative
  task.
- A "garbage collection" mechanism for orphaned, auto-generated `LocalQueue`s.
  This could be considered in a future iteration.
- Supporting complex configurations for the auto-generated `LocalQueue` beyond
  its name.

## Proposal

<!--
This is where we get down to the specifics of what the proposal actually is.
This should have enough detail that reviewers can understand exactly what
you're proposing, but should not include things like API designs or
implementation. What is the desired outcome and how do we measure success?.
The "Design Details" section below is for the real
nitty-gritty.
-->

This proposal introduces a new, optional field `defaultLocalQueue` to the
`ClusterQueue` API specification. When this field is present, it signals the
controller to enable the automatic creation of `LocalQueue`s. This field will be
an object containing the configuration for the `LocalQueue` to be created,
starting with a required `name` field.

The existing `clusterqueue-controller` will be extended to manage this logic. It
will watch `Namespace`s in addition to `ClusterQueue`s. When a namespace is
created or updated to match the `namespaceSelector` of a `ClusterQueue` with
this feature enabled, the controller will create a `LocalQueue` with the
specified name in that namespace.

To prevent conflicts where two `ClusterQueue`s might try to create a `LocalQueue`
with the same name in the same namespace, new logic to the existing `ClusterQueue`
admission webhook will be added. This webhook will reject the creation or update
of a `ClusterQueue` if its `namespaceSelector` (with `defaultLocalQueue` enabled)
overlaps with an existing `ClusterQueue` that also has the feature enabled.



### Risks and Mitigations

<!--
What are the risks of this proposal, and how do we mitigate? Think broadly.
For example, consider both security and how this will impact the larger
Kubernetes ecosystem.

How will security be reviewed, and by whom?

How will UX be reviewed, and by whom?

Consider including folks who also work outside the SIG or subproject.
-->

Risk: Race Conditions and Existing `LocalQueue`s

A `LocalQueue` with the configured name might already exist in a target
namespace, either created manually or by another process.

- Mitigation 1: The admission webhook will reject a `ClusterQueue` if a
  `LocalQueue` with the target name already exists in a namespace matched by the
  selector at the time of `ClusterQueue` creation/update.

- Mitigation 2: The `clusterqueue-controller`'s runtime logic will be written
  defensively to handle cases where a `LocalQueue` already exists. If the
  controller identifies a namespace that should receive a default `LocalQueue`
  named `<lq-default>`, its reconciliation process will be as follows:

  1. Verification: Before taking any action, the controller will first check if
     a `LocalQueue` named `<lq-default>` already exists in the target namespace.

  2. No-Op on Conflict: If the `LocalQueue` already exists, the controller will
     not attempt to create a new one or modify the existing one. This prevents
     the controller from overwriting a potentially customized, manually created
     `LocalQueue`.

  3. Emit Warning Event: To ensure administrators are aware of the situation,
     the controller will emit a `Warning` event on the parent `ClusterQueue`. The
     event message will clearly state that the creation of the default
     `LocalQueue` was skipped in a specific namespace because a `LocalQueue` with
     that name already exists.

  4. Continue Reconciliation: The controller will then proceed with its
     reconciliation loop without being blocked, handling other namespaces or
     tasks as needed. The `ClusterQueue` itself remains fully operational.

## Design Details

<!--
This section should contain enough information that the specifics of your
change are understandable. This may include API specs (though not always
required) or even code snippets. If there's any ambiguity about HOW your
proposal will be implemented, this is the place to discuss them.
-->

### API Proposal

This proposal adds a new `defaultLocalQueue` field to the `ClusterQueueSpec`.

```go
// ClusterQueueSpec defines the desired state of ClusterQueue
type ClusterQueueSpec struct {
    // ... existing fields

	// defaultLocalQueue specifies the configuration for automatically creating
	// LocalQueues in namespaces that match the ClusterQueue's namespaceSelector.
	// This feature is controlled by the `DefaultLocalQueue` feature gate.
	// If this field is set, a LocalQueue with the specified name will be created
	// in each matching namespace. The LocalQueue will reference this ClusterQueue.
	// +optional
	DefaultLocalQueue *DefaultLocalQueue `json:"defaultLocalQueue,omitempty"`
}

// DefaultLocalQueue defines the configuration for automatically created LocalQueues.
type DefaultLocalQueue struct {
	// name is the name of the LocalQueue to be created in matching namespaces.
	// This name must be a valid DNS subdomain name.
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern="^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$"
	Name string `json:"name"`
}
```

The auto-generated `LocalQueue` will also be given identifying labels and
annotations:

- Label: `kueue.x-k8s.io/auto-generated: "true"`

- Annotation: `kueue.x-k8s.io/created-by-clusterqueue: "<cluster-queue-name>"`

### Controller Logic

The existing `clusterqueue-controller` will be extended with the following
reconciliation logic:

1. The controller will watch for events on both `ClusterQueue` and `Namespace`
   resources.
2. On a change, it will iterate through `ClusterQueue`s that have
   `spec.defaultLocalQueue` enabled.
3. For each such `ClusterQueue`, it will list all `Namespace`s that match its
   `spec.namespaceSelector`.
4. For each matching `Namespace`, it will check if a `LocalQueue` with the name
   from `spec.defaultLocalQueue.name` already exists.
5. If the `LocalQueue` does not exist, the controller will create it. The new
   `LocalQueue` will reference the current `ClusterQueue` (via
   `spec.clusterQueue` field) and will include the identifying labels and
   annotations.
6. If a `LocalQueue` already exists, do nothing and emit a warning event.

### Admission Webhook

Add new validation admission logic to the existing `ClusterQueue` webhook to
prevent selector overlap.

1. The webhook triggers on `CREATE` and `UPDATE` of `ClusterQueue` resources.
2. If `spec.defaultLocalQueue` is not set, the validation is skipped.
3. If set, the webhook lists all other `ClusterQueue`s in the cluster.
4. It compares the `namespaceSelector` of the incoming `ClusterQueue` with every
   other `ClusterQueue` that also has `defaultLocalQueue` enabled.
5. If a selector overlap is detected and the `defaultLocalQueue.name` is the same,
   the request is rejected with an error detailing the conflict. This prevents
   two `ClusterQueues` from attempting to manage the same `LocalQueue` resource.

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
existing tests to make this code solid enough prior to committing the changes
necessary to implement this enhancement.

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

The primary drawback is the added complexity to the existing
`clusterqueue-controller` and webhook logic. A misconfiguration, though
mitigated, could still have unintended consequences. It also introduces a
"magical" behavior where resources are created automatically, which might be
surprising to users not familiar with the feature. Clear documentation and
events will be crucial.

## Alternatives

<!--
What other approaches did you consider, and why did you rule them out? These do
not need to be as detailed as the proposal, but should include enough
information to express the idea and why it was not acceptable.
-->

- Controller without Webhook: Implement the controller logic but only emit
  warning events on conflicting `ClusterQueue`s instead of blocking them. This is
  less safe, as it allows administrators to create ambiguous configurations that
  might not behave as expected. The immediate feedback from a webhook gives
  better user experience.

