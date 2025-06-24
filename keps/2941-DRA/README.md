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
  - [DynamicResourceAllocationConfig API](#dynamicresourceallocationconfig-api)
  - [Device Class Resolution and Ambiguity Handling](#device-class-resolution-and-ambiguity-handling)
    - [Device Class Mapping Ambiguity](#device-class-mapping-ambiguity)
  - [Workloads](#workloads)
  - [Test Plan](#test-plan)
      - [Prerequisite testing updates](#prerequisite-testing-updates)
    - [Unit Tests](#unit-tests)
    - [Integration tests](#integration-tests)
    - [E2E Test](#e2e-test)
  - [Graduation Criteria](#graduation-criteria)
    - [Alpha](#alpha)
    - [Beta](#beta)
    - [GA](#ga)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
  - [ResourceClaim By Count](#resourceclaim-by-count)
  - [Using devices in ResourceSlice to Count](#using-devices-in-resourceslice-to-count)
  - [Using a CEL expression](#using-a-cel-expression)
  - [Defining DeviceClass mapping in ClusterQuota](#defining-deviceclass-mapping-in-clusterquota)
  - [Using ResourceFlavor for DeviceClass Mapping](#using-resourceflavor-for-deviceclass-mapping)
  - [Using ConfigMap for DeviceClass Mapping](#using-configmap-for-deviceclass-mapping)
<!-- /toc -->

## Summary

[Dynamic Resource Allocation (DRA)](https://kubernetes.io/docs/concepts/scheduling-eviction/dynamic-resource-allocation/)
is a major effort to improve device support in Kubernetes. It changes how one can request resources in a myriad of ways.

## Motivation

Dynamic Resource Allocation (DRA) provides the groundwork for more sophisticated device allocations to Pods.
Quota management is about enforcing rules around the use of resources.
For example, GPUs are resource constrained and a popular request is the ability to enforce fair sharing of GPU
resources.
With these devices, many users want access and sometimes some users want the ability to preempt other users if their
workloads have a higher priority. Kueue provides support for this.

DRA provides a future where users could schedule partitionable GPU devices (MIG) or time slicing. As devices gain a
more robust way to schedule, it is important to walk through how support of DRA will work with Kueue.

### Background

DRA has four APIs that are relevant for a Kueue:

- ResourceClaims
- ResourceClaimTemplates
- DeviceClasses
- ResourceSlices

#### DRA Example

The easiest way to test DRA is to
use [dra example driver repository](https://github.com/kubernetes-sigs/dra-example-driver). Cloning that repo and
running
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

ResourceSlices are meant for communication between drivers and the control planes. These are not expected to be used for
workloads.

##### DeviceClasses

Each driver creates a device class and every resource claim will reference the device class. The dra-example-driver has
a simple device class named `gpu.example.com`. This will be the way to enforce quota limits.

### Goals

- Users can submit workloads using resource claims and Kueue can monitor the usage.
- Admins can enforce the quota for number of devices for a given DeviceClass.


### Non-Goals

- We are limiting scope for DRA to structured parameters (beta in 1.32 and 1.33)
    - Support for alpha features like DRADeviceTaints, DRAAdminAccess, DRAPrioritizedLists and DRAPartitionableDevices
      will not be included.
- This design does not work with Topology Aware Scheduling feature of Kueue. It is a significant amount of work, will be
  addressed in the future with a separate body of work

## Proposal

This proposal is to extend the APIs for allowing workloads using DRA APIs to be tallied against quota management,
borrowing and preemptable scheduling. This includes:
1. Introducing a new API to allow creating a mapping of resource name -> device class names list
2. Allowing admins to refer to resource name defined in DynamicResourceAllocationConfig in ClusterQueue to define
   nominalQuota. The nominalQuota here will be applied to workloads requesting devices from any DeviceClasses mentioned
   in the deviceClassNames list for the specific resource name. More details are documented in
   [Design Details](#design-details)

### User Stories (Optional)

#### Story 1

As a Kueue user, I want to use DRA devices for batch workloads in Kubernetes using Kueue

#### Story 2

As an administrator of Kueue with ClusterQueue, I have a DRA driver installed in the cluster. I would like to enforce
queuing, quota management and preemptable workloads for cluster users.

### Notes/Constraints/Caveats (Optional)

- The `ResourceClaims` and `ResourceClaimTemplates` APIs for DRA in k8s are immutable.
- Device classes mapping to multiple resource names can create ambiguity during quota enforcement and requires careful
  handling.
- This design does not work with Kueue's Topology Aware Scheduling feature and will be addressed in future work.
- This implementation focuses on structured parameters (beta in 1.32 and 1.33) and does not support alpha DRA features
  like DRADeviceTaints, DRAAdminAccess, DRAPrioritizedLists, and DRAPartitionableDevices.

### Risks and Mitigations

With newer DRA features like DRAAdminAccess and DRAPrioritizedLists, there is a risk that
effective tallying of resources will not be available until after allocation of these resources by kube-scheduler.

In order to mitigate this risk, Kueue can take the following approach:
1. For DRAPrioritizedLists: all the mentioned device classes in the request will be counted against the quota
2. For DRAAdminAccess: This feature can only be enabled in admin namespace, therefore it should be skipped for being
   counted against ClusterQuota. This is a different stance than Kubernetes ResourceQuota because kubernetes
   ResourceQuota is namespace scoped. As a result, admin users can account for quota independent of user workloads.
   On the contrary, with Kueue, since quota is part of ClusterQuota a cluster scoped object, admin workloads using
   devices in admin namespace if counted against quota, will eat up quota meant for user workloads.
3. For ResourceClaims with allocation mode `All`: worst-case scenario of the max number of devices that could be
   allocated to a single claim will be used against quota.


## Design Details

A new feature gate DynamicResourceAllocation will be introduced, allowing users to test it in dev environments while
making the changes dormant for production users. The following sections will explain the design in detail.

### DynamicResourceAllocationConfig API

```golang
// +genclient
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// DynamicResourceAllocationConfig is a singleton CRD that maps resource names to device classes
// used in ClusterQueue resource quotas. It is singleton as "default" is the only allowed named for the CRD instance in
// Kueue namespace.
type DynamicResourceAllocationConfig struct {
    metav1.TypeMeta   `json:",inline"`
    metav1.ObjectMeta `json:"metadata,omitempty"`

    // Spec defines the desired state of DynamicResourceAllocationConfig
    Spec DynamicResourceAllocationConfigSpec `json:"spec"`
}

// DynamicResourceAllocationConfigSpec defines the mappings between resource names and device classes
type DynamicResourceAllocationConfigSpec struct {
    // Resources is a list of mappings from resource name to device classes
    // +listType=map
    // +listMapKey=name
    // +kubebuilder:validation:MaxItems=16
    Resources []DynamicResource `json:"resources"`
}

// DynamicResource defines a mapping from a resource name to a list of device classes
type DynamicResource struct {
    // Name is the resource name that will be referred to in ClusterQueue and Workload admission status.
    Name corev1.ResourceName `json:"name"`

    // DeviceClassNames lists the names of all the device classes that will count against
    // the quota defined for this resource name
    // +listType=set
    // +kubebuilder:validation:MaxItems=8
    DeviceClassNames []corev1.ResourceName `json:"deviceClassNames"`
}
```

The ResourceFlavor spec will have a field called dynamicResource. The cluster admin has to define the list of
deviceClasses that will be represented by a resource name and can be used to define quotas in cluster queue. With this
a ClusterQueue with nominalQuota can be defined as follows:

```yaml
apiVersion: kueue.x-k8s.io/v1alpha1
kind: DynamicResourceAllocationConfig
metadata:
  name: "default"  # Fixed name - singleton CR, only one instance allowed
  namespace: "kueue-system"
spec:
  resources:
  - name: whole-gpus
    deviceClassNames:
    - gpu.example.com
  - name: shared-gpus
    deviceClassNames:
    - ts-shard-gpus.example.com
    - sp-shared-gpus.example.com
---
apiVersion: kueue.x-k8s.io/v1beta1
kind: ResourceFlavor
metadata:
  name: "default-gpu-flavor"
spec:
  # No changed needed here
---
apiVersion: kueue.x-k8s.io/v1beta1
kind: ClusterQueue
metadata:
  name: "gpus-cluster-queue"
spec:
  resourceGroups:
  - coveredResources: ["cpu", "memory", "whole-gpus", "shared-gpus"]
    flavors:
    - name: "default-gpu-flavor"
      resources:
      - name: "cpu"
        nominalQuota: 9
      - name: "memory"
        nominalQuota: "1200Mi"
      - name: 'whole-gpus'
        nominalQuota: 2
      - name: 'shared-gpus'
        nominalQuota: 2
```

The above ClusterQueue is an example configuration of a queue, with half quota configured for single allocation of
example
GPUs, and half quota configured for GPUs that are shared by workloads. Similarly, when DRAPartitionableDevices feature
is supported in kubernetes, GPUs partitions can be represented by a single device class.

### Device Class Resolution and Ambiguity Handling

#### Device Class Mapping Ambiguity

Ambiguity occurs when a device class appears in multiple mappings within the DynamicResourceAllocationConfig CR. This
creates uncertainty at admission time about which resource name and corresponding quota should be used.

Example of ambiguous configuration:
```yaml
# Configuration allowing device class to map to multiple resource names
apiVersion: kueue.x-k8s.io/v1alpha1
kind: DynamicResourceAllocationConfig
metadata:
  name: "default"
  namespace: "kueue-system"
spec:
  resources:
  - name: whole-gpus
    deviceClassNames:
    - gpus.example.com          # Appears here
  - name: fast-gpus
    deviceClassNames:
    - gpus.example.com          # And also here - creates ambiguity
---
# ClusterQueue using resource names from the DynamicResourceAllocationConfig
apiVersion: kueue.x-k8s.io/v1beta1
kind: ClusterQueue
metadata:
  name: multi-zone-queue
spec:
  resourceGroups:
  - coveredResources: ["cpu", "memory", "whole-gpus", "fast-gpus"]
    flavors:
    - name: zone-a-flavor
      resources:
      - name: whole-gpus
        nominalQuota: 2
    - name: zone-b-flavor
      resources:
      - name: fast-gpus
        nominalQuota: 3
```

When a device class like `gpus.example.com` maps to multiple resource names (`whole-gpus` and `fast-gpus`), Kueue must
determine which quota to use for admission.

Kueue resolves this ambiguity using a top-down flavor matching process:

1. Examine flavors in the order they are listed in the ClusterQueue spec
2. For each flavor, check if any of its resources match the resource names that correspond to the requested device
   class
3. Select the first flavor that contains a resource mapping to the device class and has sufficient quota available

For example, when a workload requests `gpus.example.com`:
- First check `zone-a-flavor` for available `whole-gpus` quota
- If `zone-a-flavor` is exhausted, check `zone-b-flavor` for available `fast-gpus` quota
- If no flavors have available quota, the workload waits in queue

This approach ensures predictable behavior where the ClusterQueue flavor ordering determines quota evaluation priority.

### Workloads

When a user submits a workload and DynamicResourceAllocation feature gate is on, Kueue will do the following:

1. Claims will be read from podSpec.resourceClaims in the PodTemplateSpec.
2. ResourceClaimSpec will be looked up either by using:
    1. the name of the ResourceClaimTemplate or
    2. the name of the ResourceClaim

   Both ResourceClaimTemplate or ResourceClaim will be in the same namespace as the workload.
3. From the ResourceClaimSpec, the deviceClassName will be read.
4. Every claim, deviceClassName for each request will be looked at
    1. For the workload a deviceClassMap will be created by:
        1. Retrieving the singleton DynamicResourceAllocationConfig resource (named "default")
        2. Creating a map from DeviceClass names to resource names using the
           DynamicResourceAllocationConfig.spec.resources entries
        3. For resource claims that are already allocated, Kueue will maintain a mapping called `admittedResourceClaims`
           where the key follows the format `{clusterQueueName}/{resourceClaimNamespace}/{resourceClaimName}`
        4. If a workload is using an already allocated resource claim and the key
           `{targetClusterQueueName}/{resourceClaimNamespace}/{resourceClaimName}` exists in the
           `admittedResourceClaims`
           map, Kueue will skip counting that claim against quota and automatically admit the workload
        5. This approach ensures that within a single cluster queue, reusing allocated resource claims doesn't consume
           additional quota, while still enforcing quota boundaries between different cluster queues
    2. For each device class the resource name will be looked up from the deviceClassMap and resource will be
       counted against it.
5. Once the Kueue counts and admits the workloads, it saves the count in workload status. This does not require any API
   change.

All the step above are reflect in the YAMLs below:
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
          - name: gpu # 1) read the claim from resources.claims
          requests:
            cpu: 1
            memory: "200Mi"
      resourceClaims:
      - name: gpu # use the name read from #1
        resourceClaimTemplateName: single-gpu # 2.1) the name for resource claim template
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
apiVersion: kueue.x-k8s.io/v1alpha1
kind: DynamicResourceAllocationConfig # 4.1.1) Singleton CR that defines all device class mappings
metadata:
  name: "default"
  namespace: "kueue-system"
spec:
  resources:
  - name: whole-gpus
    deviceClassNames:
    - gpu.example.com # 4.1.2) Creating map{"gpu.example.com" -> "whole-gpus"}
---
apiVersion: kueue.x-k8s.io/v1beta1
kind: ClusterQueue
metadata:
  name: "gpus-cluster-queue"
spec:
  resourceGroups:
  - coveredResources: ["cpu", "memory", "whole-gpus"]
    flavors:
    - name: "default-gpu-flavor"
      resources:
      - name: "cpu"
        nominalQuota: 9
      - name: "memory"
        nominalQuota: "1200Mi"
      - name: 'whole-gpus' # 4.2) The resource name from DynamicResourceAllocationConfig
        nominalQuota: 2
---
apiVersion: kueue.x-k8s.io/v1beta1
kind: ResourceFlavor
metadata:
  name: "default-gpu-flavor"
spec:
  # No resources field - mapping is now in DynamicResourceAllocationConfig CR
---
apiVersion: kueue.x-k8s.io/v1beta1
kind: Workload
metadata:
  name: job-job0-6f46e
  namespace: gpu-test1
...
...
status:
  admission:
    clusterQueue: single-gpus-cluster-queue
    podSetAssignments:
    - count: 2
      flavors:
        cpu: whole-gpu-flavor
        memory: whole-gpu-flavor
        whole-gpus: default-gpu-flavor # 5) selected flavor is reflected here in workload status
      name: main
      resourceUsage:
        cpu: "1"
        memory: 400Mi
        whole-gpus: "1" # 5) selected device count is reflected here in workload status
```

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

Integration tests in Kueue use controller-runtime's envtest framework, which provides a real Kubernetes API server
without requiring kubelet or other cluster components. While DRA device allocation requires kubelet plugins, the core
DRA integration functionality for Kueue can be tested at the integration level by:

- Testing DynamicResourceAllocationConfig CRD creation and validation
- Verifying workload admission logic with DRA resource claims
- Testing quota enforcement against device class mappings
- Validating resource counting and flavor assignment for DRA resources

The integration tests would focus on Kueue's quota management and admission logic rather than actual device allocation,
using mock ResourceClaims and DeviceClasses to simulate DRA workloads.

#### E2E Test

Use existing dra-example-driver or Kubernetes test driver for e2e testing.

### Graduation Criteria

#### Alpha

- the implementation behind the feature gate flag in alpha
- support Beta API of the core k8s
- initial e2e tests for baseline scenario

#### Beta

- the feature gate in Beta
- all known bugs are fixed
- support integration with v1 API of DRA in core k8s
- support integration with MultiKueue
- e2e tests
- re-evaluate the support for TopologyAwareScheduling (might be moved to GA)

#### GA

- the feature gate in stable
- integration with TopologyAwareScheduling

## Implementation History

- Initial draft on September 16th 2024 by @kannon92
- Polished on April 25th 2025 by @alaypatel07

## Drawbacks

NA. Kueue should be able to schedule devices following what upstream is proposing.
The only drawbacks are that workloads will have to fetch the resource claim if they are specifying resource claims.

## Alternatives

### ResourceClaim By Count

Keeping a tally of the resource claims for a given workload could be another mechanism for enforcing quota.
However, the issue with this is that resource claims are namespaced scoped, to enforce quota usage across namespaces
kueue need to rely on a cluster-scope resource.

Additionally, ResourceClaims capture the intent of the user on what kind of device is request. The request could mean
anything from one small allocatable device to several devices or entire resource pool. Therefore, tracking the number
of requests becomes non-intuitive. The need is to count devices going to be allocated to those requests.

### Using devices in ResourceSlice to Count

DRA drivers publish resources for each node, which could be used as a mechanism for counting resources. However, in DRA
implementation, ResourceSlices are used for driver/scheduler communication. The only way users can request dynamic
resources is via ResourceClaims. ResourceClaims does not have the notion of what devices will be allocated a priori.

Enforcing quota requires two inputs, 1) user request and 2) system usages. With using ResourceSlice, the first
requirement
is missing.

### Using a CEL expression

Cluster admin might have to create new deviceclass for narrowing set of target devices in existing device class for
setting quota. Moreover, when existing users use the old device classes, they might have to migrate to the new
deviceclass.
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

### Defining DeviceClass mapping in ClusterQuota

The definition of what DeviceClasses construct a DRA device could be in ClusterQuota just before declaring the nominal
count for the device.

```golang
type DynamicResourceMapping struct {
	// Name is the resource name of this mapping. This will be referred in ClusterQueue
	// and Workload status
	Name corev1.ResourceName `json:"name"`

	// deviceClassNames lists the names of all the device classes that will count against
	// the quota defined in this resource
	// +listType=atomic
	DeviceClassNames []corev1.ResourceName `json:"deviceClassNames"`
}

type ResourceFlavorSpec struct {
	// dynamicResources defines Kubernetes Dynamic Resource Allocation resources
	// +optional
	// +featureGate=DynamicResourceStructuredParameters
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=16
	DynamicResources []DynamicResourceMapping `json:"dynamicResources,omitempty"`
}
```

This presents a problem where the same resource name could be used to define DeviceClasses A and B in one ClusterQueue
and DeviceClasses C, D and E in another ClusterQueue leading to conflicts. Since the mapping resource name to list of
DeviceClasses is not shared, it is hard to implement borrowing as it becomes very non-deterministic. Hence, this
approach
is not feasible.

### Using ResourceFlavor for DeviceClass Mapping

An earlier design considered embedding device class mappings directly in the ResourceFlavor API instead of creating
a separate DynamicResourceAllocationConfig CRD:

```golang
type DynamicResourceMapping struct {
	// Name is the resource name of this mapping. This will be referred in ClusterQueue
	// and Workload status
	Name corev1.ResourceName `json:"name"`

	// deviceClassNames lists the names of all the device classes that will count against
	// the quota defined in this resource
	// +listType=atomic
	DeviceClassNames []corev1.ResourceName `json:"deviceClassNames"`
}

type ResourceFlavorSpec struct {
	// dynamicResources defines Kubernetes Dynamic Resource Allocation resources
	// +optional
	// +featureGate=DynamicResourceStructuredParameters
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=16
	DynamicResources []DynamicResourceMapping `json:"dynamicResources,omitempty"`
}
```

However, this design had a major drawback. The biggest issue was semantic confusion - a dynamicResource like `gpu` in
ResourceFlavor1 could have deviceClass gpu-a.example.com while the same dynamicResource name in ResourceFlavor2 could
have the completely different deviceClass gpu-b.example.com. This creates significant confusion for cluster
administrators because the same resource name would have different meanings depending on which ResourceFlavor was being
referenced. The singleton DynamicResourceAllocationConfig CRD approach addresses this by providing a single source of
truth for all device class mappings in the cluster.

### Using ConfigMap for DeviceClass Mapping

The existing configmap which holds the configuration for `kueue-controller-manager` could be used to define the resource
name to device class mappings. This would be a single source of truth for all the device class mappings in the cluster.

However, this approach is not ideal because:
1. The global configuration prevents dynamic Kueue configuration changes without needing to restart kueue-controller-manager
2. The configmap is already in beta, adding changes for alpha level feature to it is not preferred
