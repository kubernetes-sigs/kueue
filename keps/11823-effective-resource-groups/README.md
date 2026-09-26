# KEP-11823: EffectiveResourceGroups


<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
- [Proposal](#proposal)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [Default behavior](#default-behavior)
  - [Core Controller](#core-controller)
  - [MultiKueue quota automation](#multikueue-quota-automation)
  - [Test Plan](#test-plan)
    - [Prerequisite testing updates](#prerequisite-testing-updates)
    - [Unit tests](#unit-tests)
    - [Integration tests](#integration-tests)
    - [e2e tests](#e2e-tests)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
  - [Treat MultiKueue automation as priority](#treat-multikueue-automation-as-priority)
  - [Let spec override effective quota](#let-spec-override-effective-quota)
<!-- /toc -->

## Summary

This KEP extends the current resource group definition mechanism of Kueue to accommodate automated quota management.

The proposal is to introduce a new ClusterQueue Status field: `EffectiveResourceGroups`, used to represent and track the ClusterQueue quota as interpreted by the Kueue scheduler.

## Motivation

We need a way to track the ClusterQueue quota. In the default case the quota is static and described by the user in `spec.ResourceGroups`. There are however two ongoing initiatives aiming to automate ClusterQueue quota management:
1. [KEP 8826](https://github.com/kubernetes-sigs/kueue/pull/8864)
1. [KEP 9988](https://github.com/kubernetes-sigs/kueue/tree/main/keps/9988-multikueue-manager-quota-automation#move-the-aggregated-quota-out-of-clusterqueuespec)

When automating effective quota calculation, using `spec.ResourceGroups` to store the value would mean allowing Kueue to modify spec of objects it's supposed to reconcile (specifically, ClusterQueues). This may be perceived as a design smell because:
* It blurs the line between configuration (managed by users) and state (managed by controllers).
* It brings a risk of an infinite reconciling loop.

To resolve this issue and allow the aforementioned initiatives to be implemented in a robust and clean way, we propose introducing a new ClusterQueue Status field: `EffectiveResourceGroups` to store the automated quota value.

### Goals

1. Implement the `EffectiveResourceGroups` status field in ClusterQueue API.
2. Define a clear contract on how `EffectiveResourceGroups` is managed and utilized by Kueue in cases of:
    * Default setup: MultiKueue disabled; this case will be the basis for any possible future additions of more automated ClusterQueue quota management schemes,
    * MultiKueue setup without Quota automation: MultiKueue enabled, MultiKueueManagerQuotaAutomation feature disabled.
    * MultiKueue with MultiKueueManagerQuotaAutomation enabled.

## Proposal

We introduce the new `status.EffectiveResourceGroups` field alongside the `spec.ResourceGroups` field.
The `status.EffectiveResourceGroups` will add a layer between the `spec.ResourceGroups` (set directly by users) and Kueue logic. The field will always be populated. By default it will reflect the value set in spec.

```go
type ClusterQueueStatus struct {

	// [...]

	// effectiveResourceGroups describes the groups of resources as seen by Kueue controllers.
	// Each resource group defines the list of resources and a list of flavors
	// that provide quotas for these resources.
	// Each resource and each flavor can only form part of one resource group.
	// By default, it's equal to spec.resourceGroups. However, in some scenarios (e.g. MultiKueue)
	// it may differ from spec.resourceGroups.
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=16
	// +optional
	EffectiveResourceGroups []ResourceGroup `json:"effectiveResourceGroups,omitempty"`

	// [...]

}
```

All internal Kueue logic will use either the `spec.ResourceGroups` or the `status.EffectiveResourceGroups` as a source of truth for the ClusterQueue's quota, based on whether the `EffectiveResourceQuotas` featureGate is enabled.

Setting the value of the effective quota will be handled by the Core-ClusterQueue-Controller and MultiKueue-ClusterQueue-Controller depending on the configuration:

1. By default, `EffectiveResourceGroups` will always mirror `spec.ResourceGroups`. `EffectiveResourceGroups` will be synced to `spec.ResourceGroups` during reconciliation in the Core ClusterQueue Controller.
This covers both the cases when the MultiKueue ClusterQueue Controller is not created, and when it is but the ClusterQueue is not a MultiKueue CQ (does not have the MK AdmissionCheck).
2. When the MultiKueue ClusterQueue Controller is active, it will handle all ClusterQueues with the Multikueue Admission Check enabled. 
When the automation is **enabled** and **configured correctly** for a given MK CQ, `EffectiveResourceGroups` will be set directly to a value representing the aggregated quota across the Manager Queue's Workers, calculated as decided upon in [issue 11862](https://github.com/kubernetes-sigs/kueue/issues/11862). Otherwise it will be synced to `spec.ResourceGroups` as well.

The MultiKueue ClusterQueue Controller will be created only when the following feature gates are enabled:
1. MultiKueue
2. MultiKueueManagerQuotaAutomation
3. **EffectiveResourceQuotas**

We consider MultiKueue Automated Quota Management **enabled** and **configured correctly** when:
1. The Quota Management Strategy value is **"Automated"**.
2. **spec.ResourceGroups has a valid format.** For the details what is considered a valid format see [KEP 8826](../keps/9988-multikueue-manager-quota-automation#planned-changes-in-alpha2-version).

### Risks and Mitigations

The major risks here are allowing `spec.ResourceGroups` to be empty and allowing a period of time where `status.EffectiveResourceGroups` might be empty as well.

Mitigating this requires an expansive sweep of existing logic, ensuring the code is made secure against:
* referencing an empty `spec.ResourceGroups` or `status.EffectiveResourceGroups` properties,
* a misalignment/delay of `status.EffectiveResourceGroups` being synced to `spec.ResourceGroups`.

## Design Details

`status.EffectiveResourceGroups` will be managed by either the Core ClusterQueue Controller or the MultiKueue ClusterQueue Controller (mutually exclusive) as described in the [Proposal](#proposal) section.

The `status.EffectiveResourceGroups` will be used to provide resource quota only when the **EffectiveResourceQuotas** feature is enabled. Otherwise we will default to using the spec. To make this feasible, all effective calls to retrieve the ResourceGroups will be handled by a dedicated method:


MultiKueue quota automation will, since its [Alpha2](https://github.com/kubernetes-sigs/kueue/tree/main/keps/9988-multikueue-manager-quota-automation#graduation-criteria) stage, require the **EffectiveResourceQuotas** feature to be enabled alongside its dedicated feature gate.

### Default behavior

By default `status.EffectiveResourceGroups` will be synced to the current value `spec.ResourceGroups`.


### Core Controller

The Core Controller is the default controller handling the new parameter.
The reconciliation logic will check if the ClusterQueue is subject to the default handling scheme and perform the [Default Behavior](#default-behavior) if applicable.

As of right now the only reason for special handling is the MultiKueue Manager ClusterQueue quota automation scheme, but more can be added in the future.
When the controller detects a reason for special handling of quota sync, it will log it and skip the default behavior.


### MultiKueue quota automation

When feature.MultiKueue and feature.MultiKueueManagerQuotaAutomation are enabled, the MultiKueue Controller will be created. We will delegate the handling of MultiKueue ClusterQueues when it is set up. The Controller itself will:
1. Check if the ClusterQueue has the MultiKueue admission check assigned. If not: it will skip the CQ as it is not considered an MK CQ.
2. Check if:
    1. the ClusterQueue's MultiKueue Config has the **Quota Management Strategy** set to "Automated"; if not - set the `MultiKueueManagerQuotaAutomation` condition to **false**, reason: "Not Requested" and perform **Default Behavior**,
    2. `spec.ResourceGroups` has a valid format; if not - set the `MultiKueueManagerQuotaAutomation` condition to **false**, reason: "Unsupported Configuration" and return error,
4. If both conditions are true: the controller will calculate the aggregated quota across the Manager Queue's Workers and set it as the value of `status.EffectiveResourceGroups`.

### Test Plan

[ ] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.

#### Prerequisite testing updates

- **Beta** - Existing tests must be adjusted to use `status.EffectiveResourceGroups` instead of `spec.ResourceGroups` as the de-facto source-of-truth for effective quota.

#### Unit tests

- **Alpha** - Tests added/adjusted in pkg/controller/admissionchecks/multikueue/clusterqueue_test.go.

#### Integration tests

- **Alpha** - Tests added to:
	- test/integration/multikueue
	- test/integration/singlecluster/controller/core/clusterqueue_controller_test.go

#### e2e tests

- **Alpha** - Dedicated tests are added in:
	- test/e2e/multikueue/baseline
	- test/e2e/singlecluster/baseline


### Graduation Criteria

- **Alpha** - `status.EffectiveResourceGroups` is added and tested. *`EffectiveResourceGroups`* feature is set to **disabled** by default.
- **Beta** - all existing tests are adjusted to work with the feature enabled. The feature is set to **enabled** by default.
- **Stable** - Kueue and MultiKueue are confirmed to work as expected with the feature enabled and no issues are reported in the field. The feature gate is removed.

## Implementation History

- 01.06.2026 - KEP created

## Drawbacks

Adding an additional layer of resource group tracking requires reworking core kueue logic and updating most existing tests to take the distinction between the spec and status values into account. Mitigating the main risks will be an extensive effort, prone to error by virtue of being such a fundamental change.

## Alternatives

### Treat MultiKueue automation as priority

Instead of using the spec when automation is enabled (and notifying the user that automation is misconfigured) we could instead override the authority of the spec, setting the effective groups to the automated value.

The main drawback here is that the users are likely to expect the spec they set manually to take precedence as it is a user-facing, user-configured and well established field.

### Let spec override effective quota

In code, we could alternatively allow for the method used to retrieve effective quota to override the value of the `status.EffectiveResourceGroups` field when spec is set.
This could be counter intuitive as the users would see both the `spec.ResourceGroups` and the `status.EffectiveResourceGroups` reflecting different values, and Kueue using the value of spec being the actual effective value for resource groups. To avoid this it is recommended for `status.EffectiveResourceGroups` to reflect the true effective value at all times.
