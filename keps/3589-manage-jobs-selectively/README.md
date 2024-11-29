# KEP-3589: Uniformly filter manageJobsWithoutQueueNames by namespace

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories (Optional)](#user-stories-optional)
    - [Story 1](#story-1)
- [Design Details](#design-details)
  - [API](#api)
  - [Implementation](#implementation)
  - [Test Plan](#test-plan)
    - [Unit Tests](#unit-tests)
    - [Integration tests](#integration-tests)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
  - [Do nothing](#do-nothing)
  - [Generalize filtering, but don't change the API.](#generalize-filtering-but-dont-change-the-api)
  - [Validating Admission Policies](#validating-admission-policies)
<!-- /toc -->

## Summary

This KEP adds a field `managedJobsNamespaceSelector` to the Kueue `Configuration` struct
that enables how Jobs submitted without a `kueue.x-k8s.io/queue-name` label are
processed by Kueue to be controlled on a per-namespace basis for all Kueue integrations.

This is a generalization of `integrations.podOptions.namespaceSelector`, which
provides per-namespace processing of Jobs submitted without a `queue-name` for
only the `Pod`, `Deployment`, and `StatefulSet` integrations.  This KEP will
deprecate `integrations.podOptions.namespaceSelector`, which will be removed entirely
in a future release.
-->

## Motivation

When Kueue is deployed on multi-tenant clusters, it is essential that cluster
admins be able to configure Kueue such that users cannot bypass its quota system.
In particular, there must be a robust and recommended mechanism for configuring Kueue
such that all Pod-creating Kinds in user namespaces are subject to management by Kueue.

Currently, the primary mechanism for doing this is setting `manageJobsWithoutQueueName` to
true.  When `manageJobsWithoutQueueName` is true, then Kueue will manage all instances of
Kinds with Kueue integrations, even if they do not have a `kueue.x-k8s.io/queue-name` label.
As a practical matter, Pods running in system namespaces must be exempted from being
managed by Kueue even if `manageJobsWithoutQueueName` is true.  Therefore Kueue's Pod
integration limits the scope of `manageJobsWithoutQueueName` by restricting its effect to
Pods that are in namespaces that match against the `integrations.podOptions.namespaceSelector`.

The recently added `Deployment` and `StatefulSet` integrations are built on top of the
`Pod` integration and therefore the effect of `manageJobsWithoutQueueName` for these
two integrations is also filtered by `integrations.podOptions.namespaceSelector`.  A similar
rationale holds for `Deployment` and less strongly for `StatefulSet` that some mechanism
to limit `manageJobsWithoutQueueName` is required otherwise it would be impossible in
practice to ever set `manageJobsWithoutQueueName` to true.

No other integrations beyond these three consider the `namespaceSelector` when deciding whether or not to
suspend jobs that do not have a `queue-name` label.  This results in an irregular API and makes it
harder to configure Kueue such that quotas will be enforced (cannot be bypassed by users).
The irregularity between `batchv1/Job` and `Deployment` is likely to be especially confusing
to users and cluster admins who would view both of these types as "built-in" to Kubernetes and
would expect them to be configured in the same way.

### Goals

Provide a simple and uniform API to enable cluster admins to filter Kueue's
processing of Jobs submitted without a `kueue.x-k8s.io/queue-name` label.

### Non-Goals

Consider any scope for filtering the processing of Jobs submitted without a
`queue-name` label that is not based on namespaces.

## Proposal

We add a `managedJobsNamespaceSelector` of type `*metav1.LabelSelector`
to the top level of the Kueue `Configuration` struct (same level as `manageJobsWithoutQueueName`).

We use this new configuration for all integrations in the same way that the current
`namespaceSelector` is used by the Pod integration.  Specifically,
1. If `manageJobsWithoutQueueName` is false, `managedJobsNamespaceSelector` has no effect: Kueue will manage
exactly those instances of supported Kinds that have a `queue-name` label.
2. If `manageJobsWithoutQueueName` is true, then Kueue will (a) manage all instances of supported Kinds
that have a `queue-name` label and (b) will manage all instances of supported Kinds that do not
have a `queue-name` label if they are in namespaces that match `managedJobsNamespaceSelector`.

We deprecate `podOptions.namespaceSelector` and remove it in a future release.

### User Stories (Optional)

#### Story 1

Cluster admins will be able to use the combination of `managedJobsNamespaceSelector` and
`manageJobsWithoutQueueName` to configure Kueue to enable quota enforcement on a per namespace basis.

One approach would be to exempt selected namespaces from management:
```yaml
managedJobsNamespaceSelector:
  matchExpressions:
  - key: kubernetes.io/metadata.name
    operator: NotIn
    values: [ kube-system, kueue-system ]
```
Another approach would be for the cluster admin to label namespaces that are subject to management:
```yaml
managedJobsNamespaceSelector:
  matchLabels:
    kueue.x-k8s.io/managed-namespace: true
```

## Design Details

### API

The `Configuration` struct is extended to add ManagedJobsNamespaceSelector
```go
type Configuration struct {

  ...

	// ManageJobsWithoutQueueName controls whether or not Kueue reconciles
	// jobs that don't set the label kueue.x-k8s.io/queue-name.
	// If set to true, then those jobs will be suspended and never started unless
	// they are assigned a queue and eventually admitted. This also applies to
	// jobs created before starting the kueue controller.
	// Defaults to false; therefore, those jobs are not managed and if they are created
	// unsuspended, they will start immediately.
	ManageJobsWithoutQueueName bool `json:"manageJobsWithoutQueueName"`

  // ManagedJobsNamespaceSelector can be used to omit some namespaces from ManagedJobsWithoutQueueName
	ManagedJobsNamespaceSelector *metav1.LabelSelector `json:"managedJobsNamespaceSelector,omitempty"`

  ...
}
```

### Implementation

The configuration of all Job webhooks are extended to propagate the namespace selector and it is
used to filter `manageJobsWithoutQueueName` as is currently done in the Pod integration. Specifically,
jobs without queue names are only managed if both `manageJobsWithoutQueueName` is true and
the jobs namespace matches the `managedJobsNamespaceSelector`.

Configuration parsing during startup is extended to give a deprecation notice if `podOptions.namespaceSelector` is set.

In the first release with `managedJobsNamespaceSelector`, we filter the `Pod`, `Deployment`,
and `StatefulSet` integrations by doing an intersection of both `managedJobsNamespaceSelector`
and `podOptions.namespaceSelector`.  In the subsequent release, we flag configurations that have `podOptions.namespaceSelector`
set as erroneous. We drop `podOptions.namespaceSelector` from the Configuration API as part of migrating to `v1beta2`.

### Test Plan

[ X ] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.

#### Unit Tests

Unit tests for configuration parsing will be added.

All unit tests for the current Pod integration namespaceSelector will be extended across all integrations.

#### Integration tests

All integration tests for the current Pod integration namespaceSelector will be extended across all integrations.

## Implementation History

## Drawbacks

KEP-2936 proposes a mechanism (currently under design)
for configuring Kueue to inject a default `queue-name` into Jobs that do not have one.
If (a) that mechanism is adopted and (b) it is already implicitly filtered on a per-namespace
basis (by the presence of a `LocalQueue` in the Job's namespace that is somehow marked as the default),
and (c) as a result we remove `manageJobsWithoutQueueName`, then `managedJobsNamespaceSelector`
will be a non-useful configuration parameter.

## Alternatives

### Do nothing

We could do nothing and simply document that despite its historical placement in
a subfield of the `Configuration` struct, `podOptions.namespaceSelector` also applies
to the `StatefulSet` and `Deployment` integrations.

### Generalize filtering, but don't change the API.

We could change the webhooks to apply `podOptions.namespaceSelector` to all integrations without
introducing `managedJobsNamespaceSelector`. This increases the confusion around the API, but
could be a useful tactical move if we expect to remove `manageJobsWithoutQueueNames` in favor
of KEP-2936.

### Validating Admission Policies

As previously discussed in [issue 2119](https://github.com/kubernetes-sigs/kueue/issues/2119),
we could phase out `manageJobsWithoutQueueNames` and instead recommend the
use of ValidatingAdmissionPolicies to ensure that all Kinds that could be managed by Kueue that
are created in "user" namespaces have a queue name label (and thus will be managed by Kueue).

However, while exploring this idea further a complication arose. The ValidatingAdmissionPolicy
must be able to distinguish between top-level resources without queue names (which should be denied)
and child resources that are owned by Kueue-managed parents (which must be admitted even if they do not have a queue name).
In other words, the VAP must implement the same functionality as `jobframework.IsOwnerManagedByKueue`
and cross check controller-references against managed Kinds.  This approach may be challenging to scale
as the number of Kueue managed Kinds increases and especially as the possible pairs of parent/child
relationships grows. We expect JobSets to be adopted as an implementation mechanism by multiple Kueue
managed Kinds. AppWrappers can be the parent for any Kueue managed Kind. RayJobs create child Jobs.

As a concrete example, here is the VAP to enforce user quotas
used in [MLBatch](https://github.com/project-codeflare/mlbatch/blob/main/setup.k8s-v1.30/admission-policy.yaml)).
It only has to cover the subset of Kueue integrations that MLBatch users are permitted to created (gated by other RBAC rules)
and therefore only has to consider `AppWrapper` as a possible Kueue-managed parent Kind.
```yaml
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicy
metadata:
  name: mlbatch-require-queue-name
spec:
  failurePolicy: Fail
  matchConstraints:
    resourceRules:
      operations:  ["CREATE", "UPDATE"]
    - apiGroups:   ["kubeflow.org"]
      apiVersions: ["v1"]
      operations:  ["CREATE", "UPDATE"]
      resources:   ["pytorchjobs"]
    - apiGroups:   ["ray.io"]
      apiVersions: ["v1"]
      operations:  ["CREATE", "UPDATE"]
      resources:   ["rayjobs","rayclusters"]
  matchConditions:
  - name: exclude-appwrapper-owned
    expression: "!(has(object.metadata.ownerReferences) && object.metadata.ownerReferences.exists(o, o.apiVersion=='workload.codeflare.dev/v1beta2'&&o.kind=='AppWrapper'&&o.controller))"
  validations:
    - expression: "has(object.metadata.labels) && 'kueue.x-k8s.io/queue-name' in object.metadata.labels && object.metadata.labels['kueue.x-k8s.io/queue-name'] != ''"
      message: "All non-AppWrapper workloads must have a 'kueue.x-k8s.io/queue-name' label with non-empty value."
---
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicyBinding
metadata:
  name: mlbatch-require-queue-name
spec:
  policyName: mlbatch-require-queue-name
  validationActions: [Deny]
  matchResources:
    namespaceSelector:
      matchLabels:
        mlbatch-team-namespace: "true"
```
