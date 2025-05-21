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
      - [ResourceSlices](#resourceslices)
      - [DeviceClasses](#deviceclasses)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories (Optional)](#user-stories-optional)
    - [Story 1](#story-1)
    - [Story 2](#story-2)
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
  - [Using devices in ResourceSlice to Count](#using-devices-in-resourceslice-to-count)
<!-- /toc -->

## Summary

Dynamic Resource Allocation (DRA) is a major effort to improve device support in Kubernetes.
It changes how one can request resources in a myriad of ways.

## Motivation

Dynamic Resource Allocation (DRA) provides the groundwork for more sophisticated device allocations to Pods.
Quota management is about enforcing rules around the use of resources.
For example, GPUs are resource constrained and a popular request is the ability to enforce fair sharing of GPU resources.
With these devices, many users want access and sometimes some users want the ability to preempt other users if their
workloads have a higher priority. Kueue provides support for this.

DRA provides a future where users could schedule partitionable GPU devices (MIG) or time slicing. As devices gain a 
more robust way to schedule, it is important to walk through how support of DRA will work with Kueue.

### Background

DRA has three APIs that are relevant for a Kueue:

- Resource Claims
- DeviceClasses
- ResourceSlices

#### DRA Example

The easiest way to test DRA is to use [dra example driver repository](https://github.com/kubernetes-sigs/dra-example-driver). Cloning that repo and running 
`make setup-e2e` will create a Kind cluster with the DRA feature gate and install a mock dra driver. This does not use
actual GPUs so it is perfect for a test environment for exploring Kueue and DRA integration.

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
        resourceClaimTemplateName: single-gpu
```

#### Example Driver Cluster Resources

The dra-example-driver creates a ResourceSlice for each node and a DeviceClass named `gpu.example.com` for the entire 
cluster.

##### ResourceSlices

ResourceSlices are meant for communication between drivers and the control planes. These are not expected to be used for workloads.
Kueue does not need to be aware of these resources.

##### DeviceClasses

Each driver creates a device class and every resource claim will reference the device class. The dra-example-driver has
a simple device class named `gpu.example.com`. This will be the way to enforce quota limits.

### Goals

- Users can submit workloads using resource claims and Kueue can monitor the usage.
- Admins can enforce the quota for number of devices for a given DeviceClass.


### Non-Goals

- We are limiting scope for DRA to structured parameters (beta in 1.32 and 1.33)
  - Support for alpha features like DRADeviceTaints, DRAAdminAccess, DRAPrioritizedLists and DRAPartitionableDevices will not be
    included.

## Proposal

This proposal is to extend the APIs for allowing workloads using DRA APIs to be tallied against quota management, 
borrowing and preemptable scheduling. This includes modifying the `ResourceQuota` struct to allow configuring the field
to distinguish a core resource from a dynamic resource.

### User Stories (Optional)

#### Story 1

As a Kueue user, I want to use the DRA API to provide more control over the scheduling of devices.

#### Story 2

As an administrator of Kueue with ClusterQueue, I have a DRA driver installed in the cluster. I would like to enforce
queuing, quota management and preemptable workloads for cluster users. 

### Notes/Constraints/Caveats (Optional)


### Risks and Mitigations

With newer DRA features like DRAAdminAccess and DRAPrioritizedLists, there is a risk that
effective tallying of resources will not be available until after allocation of these resources by kube-scheduler.

In order to mitigate this risk, Kueue can take the following approach:
1. For DRAPrioritizedLists: all the mentioned device classes in the request will be counted against the quota
2. For DRAAdminAccess: This feature can only be enabled in admin namespace, therefore it should be skipped for being 
   counted against ClusterQuota. This is a different stance than Kubernetes ResourceQuota because kubernetes ResourceQuota is
   namespace scoped. As a result, admin users can account for quota independent of user workloads. On the contrary, with 
   Kueue, since quota is part of ClusterQuota a cluster scoped object, admin workloads using devices in admin namespace
   if counted against quota, will eat up quota meant for user workloads.
3. For ResourceClaims with allocation mode `All`: worst-case scenario of the max number of devices that could allocated 
   to a single claim will be used against quota.


## Design Details

### Resource Quota API

```golang
type ResourceQuota struct {
    // kind is used to configure if this is for a DRA Device. Its value will be DeviceClass.
    // +featureGate=KueueDynamicResourceAllocation
    Kind *string `json:"kind,omitempty"`
    
    // deviceClassNames lists the names of all the device classes that will count against
    // the quota defined in this resource quota object.
    // +listType=atomic
    // +optional
    // +featureGate=KueueDynamicResourceAllocation
    DeviceClassNames []corev1.ResourceName `json:"deviceClassNames,omitempty"`
}
```

Kind field in ResourceQuota allows Kueue to distinguish between a Core resource and a DeviceClass. When the value of kind
field is configured to be DeviceClass, the `name` becomes a canonical name of the collection of resources that can be 
provisioned for each device class present in `deviceClassNames` field.


With this, a ClusterQueue could be defined as follows:

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
      - name: "single-gpu"
        deviceClassNames: ["gpu.example.com"]
        nominalQuota: 2
        kind: "DeviceClass"
      - name: "shared-gpus"
        deviceClassNames: 
        - "ts-shared.gpu.example.com"
        - "sp-shared.gpu.example.com"
        nominalQuota: 2
        kind: "DeviceClass"
```

The above ClusterQueue is an example configuration of a queue, with half quota configured for single allocation of example
GPUs, and half quota configured for GPUs that are shared by workloads. Similarly, when DRAPartitionableDevices feature
is supported in kubernetes, GPUs partitions can be represented by a single device class. This allows for quota to be 
configured in Kueue without any modifications. 

### Workloads

When a user submits a workload and KueueDynamicResourceAllocation feature gate is on, Kueue will do the following:

1. Claims will be read from resources.claims in the PodTemplateSpec.
2. ResourceClaimSpec will be looked up either by using:
     1. the name of the ResourceClaimTemplate or
     2. the name of the ResourceClaim
   
   Both ResourceClaimTemplate or ResourceClaim will be in the same namespace as the workload.
3. From the ResourceClaimSpec, the deviceClassName will be read.
4. Every claim, deviceClassName for each request will be looked at 
   1. For the workload a deviceClassMap will be created, which is map of deviceClass -> canonical name in cluster queue
   2. for each device class the canonical quota name will be looked up and resource will be counted against it.

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
          - name: gpu. #1) read the claim from resources.claims
          requests:
            cpu: 1
            memory: "200Mi"
      resourceClaims:
      - name: gpu # use the name from read from #1
        resourceClaimTemplateName: single-gpu # 2.i) the name for resource claim templates 
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
        deviceClassName: gpu.example.com # 3) the name of the device class
---
apiVersion: kueue.x-k8s.io/v1beta1
kind: ClusterQueue
metadata:
  name: "cluster-queue"
spec:
  namespaceSelector: {} # match all.
  resourceGroups:
    - coveredResources: ["cpu", "memory", "single-gpus"]
      flavors:
        - name: "default-flavor"
          resources:
            - name: "cpu"
              nominalQuota: 9
            - name: "memory"
              nominalQuota: "200Mi"
            - name: "single-gpu"    #4.ii) lookup the cannonical name of the collection of deviceClassNames and count quota against it
              deviceClassNames: ["gpu.example.com"]
              nominalQuota: 2
              kind: "DeviceClass"
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

Use existing dra-example-driver or Kubernetes test driver for e2e testing.

### Graduation Criteria

#### Feature Gate

We will introduce a KueueDynamicResourceAllocation feature gate.

This feature gate will go beta depending on community feedback of functionality in alpha.

The goal will be limit changes only if this feature gate is enabled in combination with the DRA feature.

## Implementation History

- Initial draft on September 16th 2024 by @kannon92
- Polished on April 25th 2025 by @alaypatel07

## Drawbacks

NA. Kueue should be able to schedule devices following what upstream is proposing. 
The only drawbacks are that workloads will have to fetch the resource claim if they are specifying resource claims.

## Alternatives

### Resource Claim By Count

Keeping a tally of the resource claims for a given workload could be another mechanism for enforcing quota. 
However, the issue with this is that resource claims are namespaced scoped, to enforce quota usage across namespaces kueue
need to rely on a cluster-scope resource.

### Using devices in ResourceSlice to Count

DRA drivers publish resources for each node, which could be used as a mechanism for counting resources. However, in DRA
implementation, ResourceSlices are used for driver/scheduler communication. The only way users can request dynamic 
resources is via ResourceClaims. ResourceClaims does not have the notion of what devices will be allocated a priori.

Enforcing quota requires two inputs, 1) user request and 2) system usages. With using ResourceSlice, the first requirement 
is missing.

### Using a CEL expression

Cluster admin might have to create new deviceclass for narrowing set of target devices in existing device class for
setting quota. Moreover, when existing users use the old device classes, they might have to migrate to the new deviceclass.
For example, assume gpu.example.com deviceclass exists, and each device has device attribute "memory" there are existing
users who have resourceclaims with the deviceclass and selector like this:
```yaml
kind: ResourceClaim
name: one-large-gpu
spec:
requests:
- name: gpu-large
  deviceClassName: gpu.example.com
  selectors:
    - cel:
      expression: device.attributes["memory"] >= 80g
```

Now, if Kueue admin wants to set quota for gpu.example.com devices with device.attribute["memory"]>=80g, Kueue admin
might have to create a device class and use the new device class in clusterqueue:

```yaml
kind: DeviceClass
name: large-gpu.example.com
spec:
  selectors:
  - cel:
      expression: device.driver == "gpu.example.com" && device.attributes["memory"] >= 80g
```

Then, existing users might have to migrate/switch their ResourceClaim with large-gpu.example.com device class from
existing one.

For minimizing user impact, there could be an API change that allows CEL expression along with deviceclass name in
defining the Kueue quota, like this:
```yaml
kind: Device
nominalQuota: 2
devices:
  # We might be able to extend this object to support
  # partitionable devices, etc. in the future??
- className: gpu.example.com
  selectors:
  - cel:
      expression: device.attributes["memory"] >= 80g
```

This indeed improves the user experience, but with a cel expression like that, whether a device having attributes that
evaluates the cel expression to true or not, will only be available after the scheduler allocates the device for the
claim. Kueue needs to know the device and count it before admitting the workload and hence before it hits the
kube-scheduler. Any inclusion relationship between two boolean formulae in ResourceClaim and ClusterQueue cannot be
assumed.

For example, assume the following ResourceClaim and ClusterQueue exist. In this situation, it is clear that there could
be both cases where the allocation result consumes and does not consume the quota (i.e. this means we have to wait for
the allocation result).

```yaml
kind: ResourceClaim
name: one-mid-or-large-gpu
spec:
  requests:
  - name: middle-or-large-gpu
    deviceClassName: gpu.example.com
    selectors:
    - cel:
        expression: 50g < device.attributes["memory"] and device.attributes["memory"] <= 100g
---
kind: Device
devices:
- className: gpu.example.com
  selectors:
  - cel:
      expression: device.attributes["memory"] <= 80g
```