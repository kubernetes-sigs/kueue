# KEP-17401: MultiKueue Capacity Provider

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
tags, build the mdtoc tool with `make mdtoc` and then generate the TOC with `hack/tools/mdtoc/generate.sh`.
-->

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1: Kueue and a GitOps tool overwrite the manager ClusterQueue quota](#story-1-kueue-and-a-gitops-tool-overwrite-the-manager-clusterqueue-quota)
  - [Notes/Constraints/Caveats](#notesconstraintscaveats)
    - [Relation to KEP-9988](#relation-to-kep-9988)
    - [Relation to KEP-12382](#relation-to-kep-12382)
  - [Risks and Mitigations](#risks-and-mitigations)
    - [The CapacityProvider and the DQO can target different ClusterQueues](#the-capacityprovider-and-the-dqo-can-target-different-clusterqueues)
    - [Disconnected worker clusters make the manager quota smaller](#disconnected-worker-clusters-make-the-manager-quota-smaller)
    - [Alpha1 users lose quota automation after the upgrade](#alpha1-users-lose-quota-automation-after-the-upgrade)
- [Design Details](#design-details)
  - [API](#api)
  - [Finding the worker quotas](#finding-the-worker-quotas)
  - [CapacitySynchronized condition](#capacitysynchronized-condition)
  - [Upgrade from KEP-9988 Alpha1](#upgrade-from-kep-9988-alpha1)
  - [Test Plan](#test-plan)
    - [Unit tests](#unit-tests)
    - [Integration tests](#integration-tests)
    - [e2e tests](#e2e-tests)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
  - [<code>spec.parameters</code> refers to a MultiKueueConfig](#specparameters-refers-to-a-multikueueconfig)
  - [<code>spec.parameters</code> refers to the manager ClusterQueue directly](#specparameters-refers-to-the-manager-clusterqueue-directly)
  - [The provider uses the DQO feature gate](#the-provider-uses-the-dqo-feature-gate)
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

This KEP adds a built-in MultiKueue CapacityProvider for Dynamic Quota Orchestration (DQO, [KEP-12382]), as the Alpha2 stage of MultiKueue Manager Quota Automation ([KEP-9988]).
With this provider, the manager ClusterQueue quota changes when the worker quotas change, and Kueue does not change the ClusterQueue `spec`.

[KEP-12382]: https://github.com/kubernetes-sigs/kueue/tree/main/keps/12382-dynamic-quota-orchestration
[KEP-9988]: https://github.com/kubernetes-sigs/kueue/tree/main/keps/9988-multikueue-manager-quota-automation

## Motivation

<!--
This section is for explicitly listing the motivation, goals, and non-goals of
this KEP.  Describe why the change is important and the benefits to users. The
motivation section can optionally provide links to [experience reports] to
demonstrate the interest in a KEP within the wider Kubernetes community.

[experience reports]: https://github.com/golang/go/wiki/ExperienceReports
-->

In KEP-9988 Alpha1, the MultiKueue controller sums the quotas of the [related worker ClusterQueues].
It writes the sum to the manager `ClusterQueue.spec.resourceGroups[].nominalQuota`.
[KEP-9988 Drawbacks] lists this as a drawback:

> We allow Kueue to modify `spec` of objects it's supposed to reconcile (specifically, ClusterQueues). [...]
>
> * It blurs the line between configuration (managed by users) and state (managed by controllers).
> * It brings a risk of an infinite reconciling loop [...].

For example, if a tool manages ClusterQueues from Git, the tool and the controller can overwrite each other's changes.

[KEP-12382] adds DQO.
DQO reads capacity from a list of CapacityProviders, and writes the quota to `ClusterQueue.status.effectiveQuotas`.
A MultiKueue CapacityProvider lets MultiKueue use DQO.
This change gives these benefits:

- MultiKueue uses the same mechanism as other planned providers, for example nodes ([#10270]) and DRA ([#14977]).
- Admins can scale the worker capacity with `effectiveCapacityMultiplier`.
  For example, they can make the manager quota larger than the worker capacity.
  KEP-9988 lists this need as future work.

[related worker ClusterQueues]: https://github.com/kubernetes-sigs/kueue/tree/main/keps/9988-multikueue-manager-quota-automation#defining-related-worker-clusterqueues
[KEP-9988 Drawbacks]: https://github.com/kubernetes-sigs/kueue/tree/main/keps/9988-multikueue-manager-quota-automation#drawbacks
[#10270]: https://github.com/kubernetes-sigs/kueue/issues/10270
[#14977]: https://github.com/kubernetes-sigs/kueue/issues/14977

### Goals

- Define the MultiKueue CapacityProvider and its configuration API, `MultiKueueCapacityProviderConfig`.
- Replace the KEP-9988 Alpha1 mechanism with DQO.
  DQO writes the manager quota to `ClusterQueue.status.effectiveQuotas`, and Kueue no longer changes `ClusterQueue.spec`.

This KEP is successful if the quota of the manager ClusterQueue changes when the worker quotas change.

<!--
List the specific goals of the KEP. What is it trying to achieve? How will we
know that this has succeeded?
-->

### Non-Goals

- Cohort scope.
  The provider covers one manager ClusterQueue, as in Alpha1.
  The API uses a subtree reference, but validation accepts only `kind: ClusterQueue` in this stage.
  To support Cohort later, Kueue must change only the validation, so existing objects stay valid.
- Changes to how the provider finds the related worker ClusterQueues and sums their quotas.
  This works the same way as in Alpha1.
- More than one manager flavor.
  As in Alpha1, the provider sums the quotas of all worker flavors into one manager flavor ([#11862] tracks this).
- Changes to the `CapacityProvider` and DQO APIs from [KEP-12382].
- Changes to Kueue on the worker clusters.
  The provider only reads the worker ClusterQueues and LocalQueues, so the workers do not need an upgrade or a new feature gate.

[#11862]: https://github.com/kubernetes-sigs/kueue/issues/11862

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

Kueue adds a built-in MultiKueue capacity provider.
It works in three steps:

1. The provider finds the related worker ClusterQueues of a manager ClusterQueue and sums their quotas, as in Alpha1.
2. The provider writes the sum to the `CapacityProvider` status.
3. DQO reads the `CapacityProvider` and writes the manager quota to `ClusterQueue.status.effectiveQuotas`.

The admin selects the manager ClusterQueue in a new `MultiKueueCapacityProviderConfig` object.
The `CapacityProvider` refers to this object in `spec.parameters`.

[Upgrade from KEP-9988 Alpha1](#upgrade-from-kep-9988-alpha1) describes how to move existing Alpha1 setups to the provider.

### User Stories

This KEP is the Alpha2 stage of [KEP-9988], and the [KEP-9988 user stories] still apply.
It also implements the MultiKueue capacity provider from [KEP-12382 Story 1].
[KEP-12382 Story 3] covers a manager quota that is larger than the sum of the worker quotas.
This KEP adds one story.

[KEP-9988 user stories]: https://github.com/kubernetes-sigs/kueue/tree/main/keps/9988-multikueue-manager-quota-automation#user-stories
[KEP-12382 Story 1]: https://github.com/kubernetes-sigs/kueue/tree/main/keps/12382-dynamic-quota-orchestration#story-1-dynamically-adjust-quota-based-on-built-in-plugins
[KEP-12382 Story 3]: https://github.com/kubernetes-sigs/kueue/tree/main/keps/12382-dynamic-quota-orchestration#story-3-adjusting-the-discovered-capacity-before-distributing-as-quota

<!--
Detail the things that people will be able to do if this KEP is implemented.
Include as much detail as possible so that people can understand the "how" of
the system. The goal here is to make this feel real for users without getting
bogged down.
-->

#### Story 1: Kueue and a GitOps tool overwrite the manager ClusterQueue quota

As a batch admin, I manage ClusterQueues with a GitOps tool.
With Alpha1, Kueue writes the sum of the worker quotas to the manager `ClusterQueue.spec`, and my tool reverts this change.
I want the manager quota to change when the worker quotas change, and Kueue must not change `spec`.

### Notes/Constraints/Caveats

#### Relation to KEP-9988

This KEP is the Alpha2 stage of [KEP-9988].
KEP-9988 planned Alpha2 as an update to KEP-9988 itself (see [Move the aggregated quota out of ClusterQueueSpec]).
This KEP replaces that plan, because KEP-9988 is already large.
KEP-9988 links to this KEP.

[Move the aggregated quota out of ClusterQueueSpec]: https://github.com/kubernetes-sigs/kueue/tree/main/keps/9988-multikueue-manager-quota-automation#move-the-aggregated-quota-out-of-clusterqueuespec

#### Relation to KEP-12382

KEP-12382 says that an update to KEP-9988 defines the MultiKueue API and the migration.
This KEP defines them instead.
KEP-12382 links to this KEP.

<!--
What are the caveats to the proposal?
What are some important details that didn't come across above?
Go in to as much detail as necessary here.
This might be a good place to talk about core concepts and how they relate.
-->

### Risks and Mitigations

#### The CapacityProvider and the DQO can target different ClusterQueues

The `MultiKueueCapacityProviderConfig` selects the manager ClusterQueue.
The DQO selects the ClusterQueue that gets the quota.
If the two do not match, a ClusterQueue gets the worker quota sum of a different ClusterQueue.
Admission validation cannot detect this mismatch, because a validation rule cannot compare two objects.

**Mitigation:**
- Kueue accepts this as a configuration risk, as [KEP-13396] does for preemption rules.
- The documentation tells admins to use the same ClusterQueue in both objects.
- In Alpha2, the scope is a single ClusterQueue, so the configuration is simple.

#### Disconnected worker clusters make the manager quota smaller

This risk comes from [KEP-9988], and this KEP does not change it.
The provider does not count the quota of a disconnected worker.
This behavior can cause temporary under-utilization.
[KEP-9988] considers the impact minor and postpones a fix ([Account for temporarily unavailable workers]).

#### Alpha1 users lose quota automation after the upgrade

After the upgrade, Kueue ignores `MultiKueueConfig.spec.quotaManagement`.
The manager ClusterQueue `spec` keeps the last sum that Alpha1 wrote, and the quota no longer changes.

**Mitigation:**
- The feature is alpha, so there is no compatibility guarantee.
- The release notes and the documentation describe the migration steps.
- See [Upgrade from KEP-9988 Alpha1](#upgrade-from-kep-9988-alpha1).

[KEP-13396]: https://github.com/kubernetes-sigs/kueue/tree/main/keps/13396-configurable-preemptions
[Account for temporarily unavailable workers]: https://github.com/kubernetes-sigs/kueue/tree/main/keps/9988-multikueue-manager-quota-automation#account-for-temporarily-unavailable-workers

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

Kueue adds the cluster-scoped `MultiKueueCapacityProviderConfig` API in `kueue.x-k8s.io/v1alpha1`.

```go
// MultiKueueCapacityProviderConfig is the configuration of the MultiKueue CapacityProvider.
type MultiKueueCapacityProviderConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec defines the configuration of the MultiKueue CapacityProvider.
	// +required
	Spec MultiKueueCapacityProviderConfigSpec `json:"spec,omitzero"`
}

// +kubebuilder:validation:XValidation:rule="self.managerSubtreeRootRef.kind == 'ClusterQueue'", message="only the ClusterQueue kind is allowed for managerSubtreeRootRef"
type MultiKueueCapacityProviderConfigSpec struct {
	// managerSubtreeRootRef selects the manager ClusterQueue
	// whose related worker ClusterQueues the provider sums.
	// Only kind ClusterQueue is supported.
	// +required
	ManagerSubtreeRootRef CapacityDistributionSubtreeRootRef `json:"managerSubtreeRootRef,omitzero"`
}
```

`managerSubtreeRootRef` uses the same type as the DQO [`subtreeRootQuotaRef`].
With this type, a later KEP can add Cohort scope without a new field.

The MultiKueue CapacityProvider uses these fields:
- `controllerName` is `kueue.x-k8s.io/multikueue`.
- `parameters` refers to a `MultiKueueCapacityProviderConfig`.
- `orchestratedFlavors` has exactly one flavor, which is the flavor of the manager ClusterQueue.

The provider sums the quotas of all worker flavors into this one flavor, as in Alpha1.
Separate totals for each flavor would be wrong (see [Treatment of ResourceFlavors] in [KEP-9988]).

Example:

```yaml
apiVersion: kueue.x-k8s.io/v1alpha1
kind: MultiKueueCapacityProviderConfig
metadata:
  name: team-a
spec:
  managerSubtreeRootRef:
    kind: ClusterQueue
    name: team-a
---
apiVersion: kueue.x-k8s.io/v1alpha1
kind: CapacityProvider
metadata:
  name: team-a-workers
spec:
  controllerName: kueue.x-k8s.io/multikueue
  orchestratedFlavors:
  - name: multikueue-flavor
  parameters:
    apiGroup: kueue.x-k8s.io
    kind: MultiKueueCapacityProviderConfig
    name: team-a
status:
  capacity:
    flavors:
    - name: multikueue-flavor # The sum of the related worker ClusterQueues
      resources:
        cpu: "21"
        memory: 84Gi
        nvidia.com/gpu: "12"
---
apiVersion: kueue.x-k8s.io/v1alpha1
kind: DynamicQuotaOrchestrator
metadata:
  name: team-a
spec:
  capacityDiscovery:
    providers:
    - name: team-a-workers
  capacityDistribution:
    subtreeRootQuotaRef:
      kind: ClusterQueue
      name: team-a # The same ClusterQueue as in managerSubtreeRootRef
```

[`subtreeRootQuotaRef`]: https://github.com/kubernetes-sigs/kueue/tree/main/keps/12382-dynamic-quota-orchestration#dynamicquotaorchestrator
[Treatment of ResourceFlavors]: https://github.com/kubernetes-sigs/kueue/tree/main/keps/9988-multikueue-manager-quota-automation#treatment-of-resourceflavors

### Finding the worker quotas

The provider finds the [related worker ClusterQueues] and sums their quotas, as in Alpha1.
[MultiKueue ClusterQueue Reconciler] in [KEP-9988] describes this process.
The provider writes the sum to `CapacityProvider.status.capacity`, not to the manager ClusterQueue `spec`.
The provider also updates the sum when the `MultiKueueCapacityProviderConfig` or the `CapacityProvider` changes.

The provider reads the quota of each worker ClusterQueue with the [Source of Truth for Quotas] rule from [KEP-12382]: `status.effectiveQuotas` when it is present, otherwise `spec.resourceGroups`.
The provider cannot see the feature gates of a worker cluster.
If a worker has the `DynamicQuotaOrchestration` feature gate disabled but still has an old `status.effectiveQuotas`, the provider uses a quota that the worker ignores.

[MultiKueue ClusterQueue Reconciler]: https://github.com/kubernetes-sigs/kueue/tree/main/keps/9988-multikueue-manager-quota-automation#multikueue-clusterqueue-reconciler
[Source of Truth for Quotas]: https://github.com/kubernetes-sigs/kueue/tree/main/keps/12382-dynamic-quota-orchestration#source-of-truth-for-quotas

### CapacitySynchronized condition

The provider sets the `CapacitySynchronized` condition from [KEP-12382] with these reasons:

| Status | Reason | Description |
| --- | --- | --- |
| True | Synchronized | At least one worker cluster of the manager ClusterQueue is connected. `status.capacity` holds the sum of the connected worker clusters. The message lists the disconnected worker clusters. |
| False | Misconfigured | One of these occurs:<br>• The `spec.parameters` reference or `spec.orchestratedFlavors` configuration is invalid.<br>• The `MultiKueueCapacityProviderConfig`, the manager ClusterQueue, or its MultiKueue AdmissionCheck or `MultiKueueConfig` does not exist.<br>• The manager ClusterQueue does not meet the [KEP-9988 constraint]: exactly one ResourceFlavor, which is the flavor in `spec.orchestratedFlavors` and covers all resources of the related worker ClusterQueues. |
| False | SourceUnavailable | No worker cluster of the manager ClusterQueue is connected. |

If the condition is not True, DQO does not distribute the capacity, and the manager `status.effectiveQuotas` keeps its last value.
This is the DQO behavior from [KEP-12382] ([Stale Effective Quota]).

[Stale Effective Quota]: https://github.com/kubernetes-sigs/kueue/tree/main/keps/12382-dynamic-quota-orchestration#stale-effective-quota
[KEP-9988 constraint]: https://github.com/kubernetes-sigs/kueue/tree/main/keps/9988-multikueue-manager-quota-automation#alpha1-version

### Upgrade from KEP-9988 Alpha1

The alpha feature gate `MultiKueueManagerQuotaAutomation` controls Alpha1, so Kueue can remove the Alpha1 code immediately.

Two Alpha1 items are in the v1beta2 API:
- the `quotaManagement` field in `MultiKueueConfig`;
- the `MultiKueueManagerQuotaAutomation` condition on ClusterQueue.

Kueue cannot remove v1beta2 items before the next API version.
For this reason, Kueue keeps both items and marks them as deprecated.
Kueue ignores the field and does not set the condition.

Kueue keeps the `MultiKueueManagerQuotaAutomation` feature gate, but the gate has no effect.
If the configuration sets an unknown feature gate, Kueue does not start.
For this reason, a removal of the gate can break upgrades.

Kueue adds the alpha feature gate `MultiKueueCapacityProvider`, disabled by default.
This gate depends on the `DynamicQuotaOrchestration` feature gate.
With a dedicated gate, users can use DQO without the MultiKueue provider, and this KEP does not depend on the DQO graduation schedule.

To keep the quota automation after the upgrade, the admin does these steps for each manager ClusterQueue that uses Alpha1:
1. Enable the `DynamicQuotaOrchestration` and `MultiKueueCapacityProvider` feature gates.
2. Create the three objects from the [API](#api) example.

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

We adapt the KEP-9988 Alpha1 tests that find and sum the worker quotas to the new destination (`CapacityProvider.status.capacity`).
The KEP-12382 tests cover the DQO distribution.

#### Unit tests

<!--
In principle every added code should have complete unit test coverage, so providing
the exact set of tests will not bring additional value.
However, if complete unit test coverage is not possible, explain the reason of it
together with explanation why this is acceptable.
-->

- The provider sums the quotas of the related worker ClusterQueues into one flavor.
- The provider uses the worker `status.effectiveQuotas` when it is present, otherwise `spec.resourceGroups`.
- The provider sets each `CapacitySynchronized` reason from the table in [CapacitySynchronized condition](#capacitysynchronized-condition).

#### Integration tests

<!--
Describe what tests will be added to ensure proper quality of the enhancement.

After the implementation PR is merged, add the names of the tests here.
-->

- A change of a worker LocalQueue or ClusterQueue updates `CapacityProvider.status.capacity`.
- The provider, the DQO and the manager `ClusterQueue.status.effectiveQuotas` work together.
- The API rejects a `MultiKueueCapacityProviderConfig` with `managerSubtreeRootRef.kind: Cohort`.
- Kueue ignores `quotaManagement` and does not set the `MultiKueueManagerQuotaAutomation` condition.

#### e2e tests

<!--
This question should be filled when targeting a release.
For Alpha, describe what tests will be added to ensure proper quality of the enhancement.

For Beta and GA, document that tests have been written,
have been executed regularly, and have been stable.
This can be done with:
- permalinks to the GitHub source code
- links to the periodic job (typically a job owned by the SIG responsible for the feature), filtered by the test name

If e2e tests are not necessary or useful, explain why.
-->

- A change of a worker quota updates the manager `ClusterQueue.status.effectiveQuotas`, and the manager admits a workload that fits the new quota.

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

* **Alpha:**
  * The feature is implemented behind the `MultiKueueCapacityProvider` feature gate, disabled by default.
    The gate depends on the `DynamicQuotaOrchestration` feature gate.
  * The KEP-9988 Alpha1 code is removed (see [Upgrade from KEP-9988 Alpha1](#upgrade-from-kep-9988-alpha1)).
  * Tests are implemented.

* **Beta:**
  * The feature gate is enabled by default.
    This step requires that the `DynamicQuotaOrchestration` feature gate is enabled by default.
  * The `MultiKueueManagerQuotaAutomation` feature gate is removed.
  * `managerSubtreeRootRef` supports Cohort scope, as the DQO `subtreeRootQuotaRef` does.
  * User documentation is published on the website, and the release notes say how to opt out.
  * The feature has been successfully used in a production environment.

* **Stable:**
  * The feature (offered functionality, API surface and implementation) has stabilized, without raising concerns.
  * The feature gate is locked to `true` (and then cleaned up in a later release).

## Implementation History

- 2026 OCT 19: Initial KEP draft.

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

We do not see a major drawback.
This KEP moves the MultiKueue quota automation to DQO, which [KEP-12382] already defines, and removes [KEP-9988 Drawbacks] item 1 (Kueue changes the ClusterQueue `spec`).

The only cost we see is a more complex setup than in Alpha1.
In Alpha1, the admin enables one feature gate and sets one field in the `MultiKueueConfig`.
With this KEP, the admin enables two feature gates and creates three objects: a `MultiKueueCapacityProviderConfig`, a `CapacityProvider` and a `DynamicQuotaOrchestrator`.

<!--
Why should this KEP _not_ be implemented?
-->

## Alternatives

### `spec.parameters` refers to a MultiKueueConfig

The CapacityProvider refers to an existing `MultiKueueConfig` instead of a new configuration object.

**Reasons for discarding**

* Many manager ClusterQueues can share one `MultiKueueConfig`, so a `MultiKueueConfig` does not select a single manager ClusterQueue.
  The provider then sums the worker quotas of all these ClusterQueues, and the DQO gets too much quota.

### `spec.parameters` refers to the manager ClusterQueue directly

The CapacityProvider refers to the manager ClusterQueue instead of a new configuration object.

**Reasons for discarding**

* There is no configuration object, so there is no place for future settings.
  [KEP-12382] intends `spec.parameters` to refer to a configuration object of the provider.
* `spec.parameters.name` allows a maximum of 63 characters and no dots.
  ClusterQueue and Cohort names allow 253 characters with dots.

### The provider uses the DQO feature gate

The `DynamicQuotaOrchestration` feature gate enables the MultiKueue CapacityProvider, without a dedicated feature gate.

**Reasons for discarding**

* Users cannot use DQO with other providers and keep the MultiKueue provider disabled.
* This KEP must then graduate together with [KEP-12382].

<!--
What other approaches did you consider, and why did you rule them out? These do
not need to be as detailed as the proposal, but should include enough
information to express the idea and why it was not acceptable.
-->
