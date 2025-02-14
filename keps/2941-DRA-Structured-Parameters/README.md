# KEP-2941: Structured Parameters

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
  - [Background](#background)
    - [DRA Example](#dra-example)
    - [Workload Example](#workload-example)
    - [Example Driver Cluster Resources](#example-driver-cluster-resources)
      - [Resource slices](#resource-slices)
      - [Device classes](#device-classes)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories (Optional)](#user-stories-optional)
    - [Story 1](#story-1)
  - [Notes/Constraints/Caveats (Optional)](#notesconstraintscaveats-optional)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [Resource Quota API](#resource-quota-api)
  - [Workloads](#workloads)
  - [Test Plan](#test-plan)
      - [Prerequisite testing updates](#prerequisite-testing-updates)
    - [Unit Tests](#unit-tests)
    - [Integration tests](#integration-tests)
    - [E2E Test](#e2e-test)
  - [Graduation Criteria](#graduation-criteria)
    - [Feature Gate](#feature-gate)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
  - [Resource Claim By Count](#resource-claim-by-count)
<!-- /toc -->

## Summary

Dynamic Resource Allocation (DRA) is a major effort to improve device support in Kubernetes.
It changes how one can request resources in a myriad of ways.

## Motivation

Dynamic Resource Allocation (DRA) provides the groundwork for more sophisticated device allocations to Pods.
Quota management is about enforcing rules around the use of resources.
For example, GPUs are resource constrained and a popular request is the ability to enforce fair sharing of GPU resources.
With these devices, many users want access and sometimes some users want the ability to preempt other users if their workloads have a higher priority. Kueue provides support for this.

DRA provides a future where users could schedule partitionable GPU devices (MIG) or time slicing. As devices gain a more robust way to schedule, it is important to walk through how support of DRA will work with Kueue.

### Background

DRA has three APIs that are relevant for a Kueue:

- Resource Claims
- DeviceClasses
- ResourceSlices

#### DRA Example

I found the easiest way to test DRA was to use [dra example driver repository](https://github.com/kubernetes-sigs/dra-example-driver)

You can clone that repo and run `make setup-e2e` and that will create a Kind cluster with the DRA feature gate and install a mock dra driver.

This does not use actual GPUs so it is perfect for a test environment for exploring Kueue and DRA integration.

#### Workload Example

An example workload that uses DRA:

```yaml
---

apiVersion: resource.k8s.io/v1alpha3
kind: ResourceClaimTemplate
metadata:
  namespace: gpu-test1
  name: single-gpu
spec:
  spec:
    devices:
      requests:
      - name: gpu
        deviceClassName: gpu.example.com

---

apiVersion: batch/v1
kind: Job
metadata:
  namespace: gpu-test1
  name: job0
  labels:
    app: job
    kueue.x-k8s.io/queue-name: user-queue
spec:
  template:
    spec:
      restartPolicy: Never
      containers:
      - name: ctr0
        image: ubuntu:22.04
        command: ["bash", "-c"]
        args: ["export; sleep 9999"]
        resources:
          claims:
          - name: gpu
          requests:
            cpu: 1
            memory: "200Mi"
      resourceClaims:
      - name: gpu
        resourceClaimTemplateName: gpu.example.com
```

#### Example Driver Cluster Resources

The dra-example-driver creates a resource slice and a device class for the entire cluster.

##### Resource slices

Resource slices are meant for communication between drivers and the control planes. These are not expected to be used for workloads.

Kueue does not need to be aware of these resources.

##### Device classes

Each driver creates a device class and every resource claim will reference the device class.

The dra-example-driver has a simple device class named `gpu.example.com`.

This can be a way to enforce quota limits.

### Goals

- Users can submit workloads using resource claims and Kueue can monitor the usage.
- Admins can enforce the number of requests to a given device class.

<!--
List the specific goals of the KEP. What is it trying to achieve? How will we
know that this has succeeded?
-->

### Non-Goals

- We are limiting scope for DRA to structured parameters (beta in 1.32)

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

As an user, I want to use resource claims to provide more control over the scheduling of devices.
I have a dra driver installed on my cluster and I am interested in using DRA for scheduling.

I want to enforce quota usage for a ClusterQueue and forbid admitting workloads once they exceed the cluster queue limit.


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

### Resource Quota API

```golang
type ResourceQuota struct {
  // ...
	// Kind is the type of resource that this resource is
	// +kubebuilder:validation:Enum={Core,DeviceClass}
	// +kubebuilder:default=Core
	Kind ResourceKind `json:"kind"`
}
```

Kind allows one to distinguish between a Core resource and a Device class.

With this, a cluster queue could be defined as follows:

```yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: ClusterQueue
metadata:
  name: "cluster-queue"
spec:
  namespaceSelector: {} # match all.
  resourceGroups:
  - coveredResources: ["cpu", "memory", "gpu.example.com"]
    flavors:
    - name: "default-flavor"
      resources:
      - name: "cpu"
        nominalQuota: 9
      - name: "memory"
        nominalQuota: "200Mi"
      - name: "gpu.example.com"
        nominalQuota: 2
        kind: "DeviceClass"
```

### Workloads

When a user submits a workload and KueueDynamicResourceAllocation feature gate is on, Kueue will do the following:

a. Claims will be read from resources.claims in the PodTemplateSpec.
b. The name of the claim will be used to look up the corresponding `ResourceClaimTemplateName` in the PodTemplateSpec.
c. The ResourceClaim will be read given the name in b and using the same namespace as the workload.
d. From the ResourceClaimTemplate, the deviceClassName will be read.
e. Every claim that requests the same deviceClassName will be tallied and reported in the ResourceUsage.

```yaml
---

apiVersion: batch/v1
kind: Job
metadata:
  namespace: gpu-test1
  name: job0
  labels:
    app: job
    kueue.x-k8s.io/queue-name: user-queue
spec:
  template:
    spec:
      restartPolicy: Never
      containers:
      - name: ctr0
        image: ubuntu:22.04
        command: ["bash", "-c"]
        args: ["export; sleep 9999"]
        resources:
          claims:
          - name: gpu. #a) read the claim from resources.claims
          requests:
            cpu: 1
            memory: "200Mi"
      resourceClaims:
      - name: gpu # b) use the name in resources.claim
        resourceClaimTemplateName: single-gpu # c) the name for resource claim templates 
---
apiVersion: resource.k8s.io/v1alpha3
kind: ResourceClaimTemplate
metadata:
  namespace: gpu-test1
  name: single-gpu
spec:
  spec:
    devices:
      requests:
      - name: gpu
        deviceClassName: gpu.example.com # d) the name of the device class

```
<!--
This section should contain enough information that the specifics of your
change are understandable. This may include API specs (though not always
required) or even code snippets. If there's any ambiguity about HOW your
proposal will be implemented, this is the place to discuss them.
-->

### Test Plan

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

TBD
- `<package>`: `<date>` - `<test coverage>`

#### Integration tests

I am not sure if we can test DRA functionality (requiring alpha features enabled) at the integration level.

DRA requires a kubelet plugin so this may not be a good candidate for an integration test.

#### E2E Test

It may be worth creating install dra-example-driver and testing this e2e.

<!--
Describe what tests will be added to ensure proper quality of the enhancement.

After the implementation PR is merged, add the names of the tests here.
-->

### Graduation Criteria

#### Feature Gate

We will introduce a KueueDynamicResourceAllocation feature gate.

This feature gate will go beta once DRA is beta.

The goal will be limit changes only if this feature gate is enabled in combination with the DRA feature.

## Implementation History

- Draft on September 16th 2024.

## Drawbacks

NA. Kueue should be able to schedule devices following what upstream is proposing. 
The only drawbacks are that workloads will have to fetch the resource claim if they are specifying resource claims.

## Alternatives

### Resource Claim By Count

Originally I was thinking one could keep a tally of the resource claims for a given workload. 
The issue with this is that resource claims are namespaced scoped.
To enforce quota usage across namespaces we need to use cluster scoped resources.