# KEP-2941: DRA Support in Kueue

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
    - [Story 3](#story-3)
    - [Story 4](#story-4)
    - [Story 5](#story-5)
  - [Notes/Constraints/Caveats (Optional)](#notesconstraintscaveats-optional)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [Configuration API Extension for DRA](#configuration-api-extension-for-dra)
  - [Device Class Resolution and Conflict Prevention](#device-class-resolution-and-conflict-prevention)
    - [Device Class Mapping Uniqueness](#device-class-mapping-uniqueness)
  - [RBAC Requirements](#rbac-requirements)
  - [CEL Expression Validation](#cel-expression-validation)
    - [Performance Implications](#performance-implications)
  - [Workloads](#workloads)
    - [DRA-Specific Workload Processing](#dra-specific-workload-processing)
    - [Workload Processing Flow](#workload-processing-flow)
    - [Workload Rejection When DRA Is Disabled](#workload-rejection-when-dra-is-disabled)
  - [Extended Resources](#extended-resources)
    - [Configuration](#configuration)
    - [Path Separation](#path-separation)
    - [Processing Flow](#processing-flow)
    - [Same Hardware with Both Paths](#same-hardware-with-both-paths)
    - [DeviceClass Resolution via Field Indexer](#deviceclass-resolution-via-field-indexer)
    - [DeviceClass Lifecycle Scenarios](#deviceclass-lifecycle-scenarios)
    - [Late DeviceClass Creation](#late-deviceclass-creation)
  - [Partitionable Devices](#partitionable-devices)
    - [ResourceSlice Structure](#resourceslice-structure)
    - [User Workload](#user-workload)
    - [Configuration](#configuration-1)
    - [Processing Flow](#processing-flow-1)
    - [Path Interactions](#path-interactions)
    - [Counter Lifecycle Scenarios](#counter-lifecycle-scenarios)
    - [Validation](#validation)
  - [Architecture Details](#architecture-details)
    - [Queue Manager Extensions](#queue-manager-extensions)
  - [Integration with Admission Fair Sharing](#integration-with-admission-fair-sharing)
  - [MultiKueue Integration](#multikueue-integration)
  - [Test Plan](#test-plan)
      - [Prerequisite testing updates](#prerequisite-testing-updates)
    - [Unit Tests](#unit-tests)
    - [Integration tests](#integration-tests)
    - [E2E Test](#e2e-test)
  - [Graduation Criteria](#graduation-criteria)
    - [Alpha](#alpha)
      - [KueueDRAIntegration (v0.14)](#kueuedraintegration-v014)
      - [KueueDRAIntegrationExtendedResource (v0.17)](#kueuedraintegrationextendedresource-v017)
      - [KueueDRAIntegrationExtendedResource (v0.18)](#kueuedraintegrationextendedresource-v018)
      - [KueueDRAIntegrationPartitionableDevices (v0.18)](#kueuedraintegrationpartitionabledevices-v018)
    - [Beta](#beta)
      - [KueueDRAIntegration (v0.18)](#kueuedraintegration-v018)
      - [KueueDRAIntegrationExtendedResource](#kueuedraintegrationextendedresource)
      - [KueueDRAIntegrationPartitionableDevices](#kueuedraintegrationpartitionabledevices)
    - [GA](#ga)
      - [KueueDRAIntegration](#kueuedraintegration)
      - [KueueDRAIntegrationExtendedResource](#kueuedraintegrationextendedresource-1)
      - [KueueDRAIntegrationPartitionableDevices](#kueuedraintegrationpartitionabledevices-1)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
  - [Webhook Rewriting Extended Resources to ResourceClaimTemplates](#webhook-rewriting-extended-resources-to-resourceclaimtemplates)
  - [ResourceClaim By Count](#resourceclaim-by-count)
  - [Using devices in ResourceSlice to Count](#using-devices-in-resourceslice-to-count)
  - [Using a CEL expression](#using-a-cel-expression)
  - [Defining DeviceClass mapping in ClusterQuota](#defining-deviceclass-mapping-in-clusterquota)
  - [Using ResourceFlavor for DeviceClass Mapping](#using-resourceflavor-for-deviceclass-mapping)
  - [Creating a new CRD for device class mapping](#creating-a-new-crd-for-device-class-mapping)
  - [User Annotation as Primary Counter Consumption Mechanism](#user-annotation-as-primary-counter-consumption-mechanism)
  - [Separate counterMappings Struct](#separate-countermappings-struct)
  - [Device-Count Quota with Dual Tracking](#device-count-quota-with-dual-tracking)
  - [Auto-discovery of Counters Without Configuration](#auto-discovery-of-counters-without-configuration)
- [Appendix](#appendix)
  - [Consumable Capacity Compatibility](#consumable-capacity-compatibility)
<!-- /toc -->

## Summary

[Dynamic Resource Allocation (DRA)](https://kubernetes.io/docs/concepts/scheduling-eviction/dynamic-resource-allocation/)
is a major effort to improve device support in Kubernetes. It changes how one can request resources in a myriad of ways.

This KEP supports three approaches for DRA integration with Kueue:
1. **ResourceClaimTemplates**: Pods explicitly reference ResourceClaimTemplates that specify device requests.
2. **Extended Resources**: Pods request DRA devices via standard `resources.requests` (e.g., `example.com/gpu: 1`), and kube-scheduler automatically creates ResourceClaims when the DeviceClass has an `extendedResourceName` field set.
3. **Partitionable Devices**: Counter-based quota for devices that can be dynamically
   partitioned (e.g., NVIDIA MIG). Instead of counting devices, Kueue tracks counter
   consumption (e.g., GPU memory) from the `SharedCounters` and `ConsumesCounters` fields
   defined by [KEP-4815](https://github.com/kubernetes/enhancements/issues/4815).

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

apiVersion: resource.k8s.io/v1
kind: ResourceClaimTemplate
metadata:
  namespace: gpu-test1
  name: single-gpu
spec:
  spec:
    devices:
      requests:
      - name: gpu
        exactly:
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

- Users can submit workloads using ResourceClaimTemplates and Kueue can monitor the usage.
- Users can submit workloads using extended resource requests (e.g., `example.com/gpu: 1`) and
  Kueue can account for quota when the DeviceClass has `extendedResourceName` set.
- Admins can enforce the quota for number of devices for a given DeviceClass.
- Admins can enforce counter-based quota for partitionable devices (e.g., GPU memory quota
  instead of device count quota for MIG profiles).

### Non-Goals

- Quota-aware handling of DRAAdminAccess and DRAPrioritizedLists (beta, default enabled in K8s 1.35)
  is not included in Kueue's alpha. See [Risks and Mitigations](#risks-and-mitigations) for the
  planned approach.
- Support for DRA features like DRADeviceTaints is not included.
- Multi-host partitionable devices (e.g., NVLink fabrics spanning multiple nodes) are not
  supported.
- Support for DRAConsumableCapacity (alpha in K8s 1.34, beta targeting K8s 1.36) is not
  included. Consumable capacity enables software-level device sharing (e.g., MPS, time-slicing)
  where multiple claims share one device. This requires different quota semantics as Kueue
  would need to read the cost from the claim's capacity request rather than the device's
  counters. No GPU driver ships consumable capacity yet.
- This design does not work with Topology Aware Scheduling feature of Kueue. It is a significant
  amount of work, will be addressed in the future with a separate body of work.

## Proposal

This proposal extends Kueue to support workloads using DRA APIs for quota management, borrowing and preemptable
scheduling. This includes:

1. Extending the existing Kueue Configuration API with `DeviceClassMappings` to map device classes to logical resource
   names
2. Supporting workloads that use ResourceClaimTemplates (ResourceClaims are not supported in alpha)
3. Supporting workloads that use extended resource requests backed by DRA DeviceClasses with
   `extendedResourceName` set (requires Kubernetes `DRAExtendedResource` feature gate, alpha in k8s 1.35)
4. Allowing admins to define quota for DRA resources in ClusterQueues using the logical resource names from device class
   mappings
5. Implementing validation to prevent device class conflicts and ensure predictable quota behavior
6. Extending `deviceClassMappings` with a `sources` field to support counter-based quota for
   partitionable devices (requires Kubernetes `DRAPartitionableDevices` feature gate, beta in
   K8s 1.36). This builds on CEL expression support which adds ResourceSlice access and
   device matching to Kueue.

More details are documented in [Design Details](#design-details)

### User Stories (Optional)

#### Story 1

As a Kueue user, I want to use DRA devices for batch workloads in Kubernetes using Kueue

#### Story 2

As an administrator of Kueue with ClusterQueue, I have a DRA driver installed in the cluster. I would like to enforce
queuing, quota management and preemptable workloads for cluster users.

#### Story 3

As a cluster administrator, I want clear validation feedback when I misconfigure device class mappings so I can quickly
identify and fix configuration conflicts before they affect workload scheduling.

#### Story 4

As a Kueue user, I want to request DRA devices using standard resource requests (e.g., `resources.requests: {"example.com/gpu": 1}`)
instead of ResourceClaimTemplates, so my existing workloads can benefit from DRA without modification when the cluster
administrator configures DeviceClasses with `extendedResourceName`.

#### Story 5

As a cluster administrator, I want to enforce GPU memory quota for MIG partitions so that teams
sharing a pool of partitionable GPUs get fair access based on counter consumption, not
just device counts. A team requesting a 1g.10gb MIG profile should consume about 9856Mi of
GPU memory quota, while a team requesting a 7g.80gb profile should consume 80Gi.

### Notes/Constraints/Caveats (Optional)

- The `ResourceClaims` and `ResourceClaimTemplates` APIs for DRA in k8s are immutable.
- ResourceClaims are not supported in alpha - workloads must use ResourceClaimTemplates.
  Direct ResourceClaim references will result in inadmissible workloads.
- Device class uniqueness is enforced - each device class can only map to one resource name to prevent quota ambiguity.
- Configuration-based approach - device class mappings are configured through the Kueue Configuration API
- This design does not work with Kueue's Topology Aware Scheduling feature and will be addressed in future work.
- DRA resource preprocessing is not scoped by ResourceFlavor node constraints. Counter
  charges and device matching are computed globally before flavor assignment.
- Quota-aware handling of DRAAdminAccess and DRAPrioritizedLists (beta in K8s 1.35) is deferred
  to beta graduation. DRADeviceTaints is not supported in alpha.
- **Single-node partitionable devices (e.g., MIG) are supported** via counter-based
  quota. See [Partitionable Devices](#partitionable-devices). Multi-host partitionable
  devices are not supported.
- **Extended Resources** requires DeviceClasses to have `spec.extendedResourceName` set.
  This depends on the Kubernetes `DRAExtendedResource` feature gate (alpha in k8s 1.35).
  When enabled, kube-scheduler automatically creates ResourceClaims for pods requesting extended resources.
  Extended resources support in Kueue is gated behind the `KueueDRAIntegrationExtendedResource` feature gate.
- **GPU time-slicing and MPS via extended resources are not supported in Alpha.**
  Time-slicing and MPS sharing modes require opaque parameters on the DeviceClass
  (e.g., `GpuConfig` with `sharing.strategy: TimeSlicing`). When kube-scheduler creates
  ResourceClaims from extended resource requests, correct quota accounting for shared
  devices requires [consumable capacity](https://github.com/kubernetes/enhancements/issues/5075)
  integration with both the DRA driver and Kueue. MPS additionally requires
  [KEP-5691 (Restricted Sharing)](https://github.com/kubernetes/enhancements/issues/5691)
  to restrict sharing to the same namespace. These will be evaluated for Beta once the
  upstream dependencies are available.
  With structured parameters, GPU sharing is supported via ResourceClaimTemplates where
  containers within the same pod share a GPU. Cross-pod sharing via direct ResourceClaims
  is not supported.
- **Kueue does not validate DeviceClass existence at config load time.** Admins should
  create DeviceClasses before submitting workloads but strict ordering is not enforced.

- **When a DeviceClass is referenced by both `deviceClassMappings` and has an
  `extendedResourceName`, Kueue unifies quota** using the `deviceClassMappings` logical
  name as the quota key for both paths, preventing over-allocation.

- CEL selectors in ResourceClaimTemplates are validated against cluster devices (ResourceSlices) at quota reservation
  time on a best-effort basis. Workloads with CEL selectors that match fewer devices than requested are rejected
  to prevent quota leaks. This validation uses the upstream DRA CEL compiler from [`k8s.io/dynamic-resource-allocation/cel`](https://github.com/kubernetes/dynamic-resource-allocation/tree/master/cel).
  On the other hand, devices can be allocated between Kueue's check and scheduling, and new ResourceSlices published after
  validation can make previously-unsatisfiable workloads satisfiable. Kueue does not
  currently have a ResourceSlice informer. Inadmissible workloads are only re-evaluated
  when the ClusterQueue is notified through other events such as quota changes. Adding
  event-driven requeuing on ResourceSlice changes is an Alpha graduation criterion.
  `WaitForPodsReady` serves as the safety net for cases where the validation state
  diverges from actual device availability at scheduling time.

### Risks and Mitigations

**Silent quota bypass when DRA is disabled**: When the `DynamicResourceAllocation` feature
gate is disabled, DRA workloads are admitted without any device resource accounting, allowing
unlimited GPU consumption outside Kueue's control. The `KueueDRARejectWorkloadsWhenDRADisabled` feature gate
(default: enabled, Beta) mitigates this by rejecting DRA workloads when the DRA feature is off.
See [Workload Rejection When DRA Is Disabled](#workload-rejection-when-dra-is-disabled).

With DRAAdminAccess and DRAPrioritizedLists (both beta, default enabled in K8s 1.35), there is a
risk that effective tallying of resources will not be available until after allocation of these
resources by kube-scheduler. Support for these is deferred to Beta but the mitigation approach
is documented here:
1. For DRAPrioritizedLists: all the mentioned device classes in the request will be counted against the quota
2. For DRAAdminAccess: This feature can only be enabled in admin namespace, therefore it should be skipped for being
   counted against ClusterQuota. This is a different stance than Kubernetes ResourceQuota because kubernetes
   ResourceQuota is namespace scoped. As a result, admin users can account for quota independent of user workloads.
   On the contrary, with Kueue, since quota is part of ClusterQuota a cluster scoped object, admin workloads using
   devices in admin namespace if counted against quota, will eat up quota meant for user workloads.
3. For ResourceClaims with allocation mode `All`: worst-case scenario of the max number of devices that could be
   allocated to a single claim will be used against quota.
4. For Extended Resources: if a DeviceClass is created or updated between Kueue admitting a
   workload and kube-scheduler scheduling it, the two components may pick different DeviceClasses
   for the same `extendedResourceName` (a TOCTOU gap). This can happen during valid operational
   scenarios. KEP-5004 documents a transition pattern where two DeviceClasses temporarily
   coexist (create new class, then clear old mapping), and the scheduler picks the newer one.
   There are two failure modes:
   - **Scheduling failure**: the scheduler cannot allocate devices. `waitForPodsReady` catches
     this by timing out the Pending pod and evicting/re-queuing the workload.
     Users deploying DRA with Kueue should enable `waitForPodsReady`.
   - **Quota drift**: the scheduler allocates from a different DeviceClass than Kueue charged
     quota against, but the pod runs successfully. `waitForPodsReady` does not catch this.
     Since the extended resources path uses `extendedResourceName` directly as the quota key,
     quota accounting remains correct at the resource name level, though not at the physical
     DeviceClass level.
   To mitigate:
   - Kueue uses a controller-runtime field indexer on `DeviceClass` by `spec.extendedResourceName`
     to resolve DeviceClasses deterministically.
   - Per KEP-5004, admins should ensure one `extendedResourceName` maps to at most one
     DeviceClass.
   - Post-scheduling quota reconciliation will be evaluated for Beta.
   - TAS + DRA is the longer-term path to closing this admission-scheduling gap.

## Design Details

Three feature gates control DRA support in Kueue:
- `DynamicResourceAllocation`: gates ResourceClaimTemplate-based DRA quota accounting.
  Uses `deviceClassMappings` for DeviceClass-to-quota-resource mapping.
- `KueueDRAIntegrationExtendedResource`: gates extended resources support, including DeviceClass
  auto-discovery via `extendedResourceName`. Does not use `deviceClassMappings`.
  Requires `DynamicResourceAllocation` to also be enabled.
- `KueueDRAIntegrationPartitionableDevices`: gates counter-based quota for partitionable
  devices. Enables the `sources` field on `deviceClassMappings` entries. Requires
  `DynamicResourceAllocation` to also be enabled. Also requires the Kubernetes
  `DRAPartitionableDevices` feature gate (beta in K8s 1.36).
- `KueueDRARejectWorkloadsWhenDRADisabled` (default: enabled, Beta since v0.18): rejects workloads that
  use DRA resources (ResourceClaimTemplates or ResourceClaims) when the
  `DynamicResourceAllocation` feature gate is disabled. Without this gate, DRA workloads
  submitted while `DynamicResourceAllocation` is off are silently admitted with zero
  device resource usage, bypassing quota enforcement entirely. See
  [Workload Rejection When DRA Is Disabled](#workload-rejection-when-dra-is-disabled).

The following sections will explain the design in detail.

### Configuration API Extension for DRA

DRA device class mappings are configured through the existing Kueue Configuration API rather than a standalone CRD.
This approach provides a centralized configuration mechanism and avoids the complexity of managing additional CRDs.

```golang
// Resources struct in the Configuration API
type Resources struct {
    // DeviceClassMappings defines mappings from device classes to logical resources
    // for Dynamic Resource Allocation support.
    // +optional
    DeviceClassMappings []DeviceClassMapping `json:"deviceClassMappings,omitempty"`
}

// DeviceClassMapping holds device class to logical resource mappings
// for Dynamic Resource Allocation support.
type DeviceClassMapping struct {
    // Name is referenced in ClusterQueue.nominalQuota and Workload status.
    // Must be a valid fully qualified name consisting of an optional DNS subdomain prefix
    // followed by a slash and a DNS label, or just a DNS label.
    // DNS labels consist of lower-case alphanumeric characters or hyphens,
    // and must start and end with an alphanumeric character.
    // DNS subdomain prefixes follow the same rules as DNS labels but can contain periods.
    // The total length must not exceed 253 characters.
    Name corev1.ResourceName `json:"name"`

    // DeviceClassNames enumerates the DeviceClasses represented by this resource name.
    // Each device class name must be a valid qualified name consisting of an optional DNS subdomain prefix
    // followed by a slash and a DNS label, or just a DNS label.
    // DNS labels consist of lower-case alphanumeric characters or hyphens,
    // and must start and end with an alphanumeric character.
    // DNS subdomain prefixes follow the same rules as DNS labels but can contain periods.
    // The total length of each name must not exceed 253 characters.
    DeviceClassNames []corev1.ResourceName `json:"deviceClassNames"`
}
```

The cluster admin defines the mappings from device classes to logical resource names, which can then be used to
define quotas in ClusterQueues.

**Configuration Example:**
```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: kueue-controller-manager-config
  namespace: kueue-system
data:
  config.yaml: |
    apiVersion: config.kueue.x-k8s.io/v1beta2
    kind: Configuration
    namespace: kueue-system
    manageJobsWithoutQueueName: false
    resources:
      deviceClassMappings:
      - name: whole-gpus
        deviceClassNames:
        - gpu.example.com
      - name: shared-gpus
        deviceClassNames:
        - ts-shard-gpus.example.com
        - sp-shared-gpus.example.com
---
apiVersion: kueue.x-k8s.io/v1beta2
kind: ResourceFlavor
metadata:
  name: "default-gpu-flavor"
spec:
  # No changed needed here
---
apiVersion: kueue.x-k8s.io/v1beta2
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
GPUs, and half quota configured for GPUs that are shared by workloads. Similarly, when KueueDRAIntegrationPartitionableDevices feature
is supported in kubernetes, GPUs partitions can be represented by a single device class.

### Device Class Resolution and Conflict Prevention

#### Device Class Mapping Uniqueness

To ensure predictable and deterministic quota enforcement, Kueue enforces strict uniqueness constraints on device class
mappings. Each device class can only map to one resource name across all device class mappings in the configuration.

Kueue prevents ambiguous configurations through validation at configuration load time. The following configuration
would be rejected:

```yaml
# INVALID - This configuration will be rejected during validation
apiVersion: v1
kind: ConfigMap
metadata:
  name: kueue-controller-manager-config
  namespace: kueue-system
data:
  config.yaml: |
    apiVersion: config.kueue.x-k8s.io/v1beta2
    kind: Configuration
    resources:
      deviceClassMappings:
      - name: whole-gpus
        deviceClassNames:
        - gpus.example.com          # Appears here
      - name: fast-gpus
        deviceClassNames:
        - gpus.example.com          # ERROR: Duplicate device class name
```

Example of valid configuration:
```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: kueue-controller-manager-config
  namespace: kueue-system
data:
  config.yaml: |
    apiVersion: config.kueue.x-k8s.io/v1beta2
    kind: Configuration
    resources:
      deviceClassMappings:
      - name: whole-gpus
        deviceClassNames:
        - whole-gpus.example.com     # Unique device class
      - name: fast-gpus
        deviceClassNames:
        - fast-gpus.example.com      # Different device class
```

This validation approach eliminates ambiguity at configuration time rather than requiring complex runtime resolution logic, ensuring predictable and efficient workload admission.

### RBAC Requirements

DRA support requires additional RBAC permissions for the Kueue controller to access DRA resources:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: kueue-controller-role
rules:
# ... existing permissions ...

# DRA-specific permissions
- apiGroups: ["resource.k8s.io"]
  resources: ["resourceclaims"]
  verbs: ["get", "list", "watch"]
- apiGroups: ["resource.k8s.io"]
  resources: ["resourceclaimtemplates"]
  verbs: ["get", "list", "watch"]
- apiGroups: ["resource.k8s.io"]
  resources: ["resourceslices", "deviceclasses"]
  verbs: ["get", "list", "watch"]
```

**Required Permissions:**
- `resourceclaims`: Read access to validate ResourceClaim references (though not supported for quota)
- `resourceclaimtemplates`: Read access to process ResourceClaimTemplates and extract device class information
- `resourceslices`: Read access to list cluster devices for CEL selector validation and `consumesCounters` reading.
  Kueue only reads device attributes for CEL matching and counter values for quota.
- `deviceclasses`: Read access to resolve DeviceClass selectors for device pre-filtering during CEL evaluation

**Security Considerations:**
- Kueue only requires read permissions - no create, update, or delete access to DRA resources
- Permissions are cluster-scoped to allow processing workloads across all namespaces
- No elevated privileges required beyond standard Kueue controller permissions

### CEL Expression Validation

ResourceClaimTemplates may include CEL (Common Expression Language) selectors that constrain which devices
can satisfy a request. Kueue validates these CEL selectors before admitting a workload to prevent quota from
being consumed by workloads whose pods can never be scheduled.

The validation has two stages:

1. **CEL Compilation**: Each CEL expression in the request's selectors is compiled using the upstream DRA CEL
   compiler ([`k8s.io/dynamic-resource-allocation/cel`](https://github.com/kubernetes/dynamic-resource-allocation/tree/master/cel)). This catches syntax errors, type errors, and other
   compilation issues before quota reservation.

2. **CEL Evaluation Against Cluster Devices**: Kueue lists all ResourceSlices in the cluster and evaluates
   the compiled CEL selectors against actual devices. For each request:
   - The DeviceClass is resolved and its selectors are compiled to pre-filter devices by class, avoiding
     CEL evaluation against unrelated devices (e.g., NICs when requesting GPUs).
   - The request's CEL selectors are evaluated against matching devices.
   - If fewer devices match than the requested count, the workload is marked inadmissible with a descriptive
     error indicating that no matching devices exist in the cluster, preventing quota consumption for
     unsatisfiable requests. The `QuotaReserved` condition message clearly distinguishes between device-based inadmissibility
     (e.g., "insufficient matching devices for CEL selector") and quota-based inadmissibility so that users
     know whether they need to adjust their CEL selectors / request admin hardware changes, or wait for
     quota to become available.

**Example**: A ResourceClaimTemplate requesting 2 GPUs with `device.capacity["gpu.example.com"].memory.compareTo(quantity("80Gi")) >= 0`
will be checked against actual devices in the cluster. If only 1 device matches, the workload is rejected
before consuming quota.

```yaml
apiVersion: resource.k8s.io/v1
kind: ResourceClaimTemplate
metadata:
  name: large-gpu
spec:
  spec:
    devices:
      requests:
      - name: gpu
        exactly:
          deviceClassName: gpu.example.com
          count: 2
          selectors:
          - cel:
              expression: 'device.capacity["gpu.example.com"].memory.compareTo(quantity("80Gi")) >= 0'
```

**Dependencies**: This feature imports [`k8s.io/dynamic-resource-allocation/cel`](https://github.com/kubernetes/dynamic-resource-allocation/tree/master/cel) for CEL compilation and
evaluation. This package provides the same CEL environment used by the Kubernetes scheduler for DRA device
matching.

#### Performance Implications

In clusters with large number of ResourceSlices, it may be necessary to index the slices based on DeviceNames.
This work will be deferred to beta.

Its not entirely clear if this is a performance bottleneck at this time due to the number of ResourceSlices being small.

### Workloads

#### DRA-Specific Workload Processing

DRA workloads require special handling to ensure proper resource validation and quota enforcement. Unlike standard
workloads that are processed immediately in event handlers, DRA workloads are processed in the controller's Reconcile
loop to enable proper error handling and retry logic.


1. Event Handler Behavior: When a DRA workload is created or updated, the event handlers detect the presence of
   ResourceClaimTemplates or ResourceClaims and if feature gate is enabled, skip normal queue operations, deferring
   processing to the Reconcile loop.
2. Reconcile Loop Processing: The Reconcile method handles all DRA-specific logic including:
   - Feature gate validation
   - ResourceClaim vs ResourceClaimTemplate support validation
   - Device class mapping resolution
   - Resource preprocessing and queue admission
3. Error Handling: DRA processing errors are properly handled with exponential backoff retry logic.

#### Workload Processing Flow

When a user submits a workload and DynamicResourceAllocation feature gate is enabled, Kueue processes it as follows:
1. DRA Detection: Kueue detects DRA workloads by checking for ResourceClaimTemplates or ResourceClaims in
   podSpec.resourceClaims.
2. Feature Gate Validation: Verify that the DynamicResourceAllocation feature gate is enabled. If disabled, continue
   with legacy behavior.
3. ResourceClaim Support Validation: Check if the workload uses ResourceClaims (not supported in alpha) or
   ResourceClaimTemplates (supported):
   - ResourceClaims: Mark workload as inadmissible with an error message
   - ResourceClaimTemplates: Continue processing
4. Device Class Resolution: For each ResourceClaimTemplate:
   - Read the ResourceClaimTemplate from the same namespace as the workload
   - Extract deviceClassName from each request in the template spec
   - Look up the corresponding resource name using the device class mappings from the Configuration API
5. CEL Selector Validation: For requests with CEL selectors:
   - Compile CEL expressions and reject workloads with invalid syntax
   - Evaluate CEL selectors against actual devices from ResourceSlices, pre-filtering by DeviceClass
   - Reject workloads where fewer devices match than requested count
6. Resource Preprocessing:
   - Calculate total device count per device class across all containers and init containers
   - Map device classes to resource names using the configuration
   - Generate preprocessed resource requests for queue admission
7. Queue Reservation: Add the workload to the queue with preprocessed DRA resources.
8. Status Update: Once the quota is reserved, the workload status reflects the assigned flavors and resource usage, including DRA resources.

Note: The flow above applies to the ResourceClaimTemplate path (`DynamicResourceAllocation` gate).
When the `KueueDRAIntegrationExtendedResource` gate is also enabled, workloads with extended resources in
`resources.requests` follow a separate resolution path through the ExtendedResourceCache.
See [Extended Resources](#extended-resources) for details. Both paths can be active simultaneously
for workloads that use both ResourceClaimTemplates and extended resources.

#### Workload Rejection When DRA Is Disabled

When the `DynamicResourceAllocation` feature gate is disabled, the DRA processing pipeline
is skipped entirely. Without additional safeguards, workloads that reference
ResourceClaimTemplates or ResourceClaims are silently admitted based on CPU/memory only,
with zero device resource usage recorded. This allows unlimited DRA workloads to bypass
quota enforcement, since the Kubernetes DRA scheduler still allocates devices directly.

The `KueueDRARejectWorkloadsWhenDRADisabled` feature gate (default: enabled, Beta) closes this gap. When
enabled and `DynamicResourceAllocation` is disabled, Kueue detects workloads with DRA
resources (via `HasDRA()` which checks for `ResourceClaimTemplateName` or
`ResourceClaimName` in any PodSet) and rejects them as inadmissible.

The rejection is enforced in the Reconcile loop: workloads are marked with
`WorkloadQuotaReserved=False` (reason: `WorkloadInadmissible`) and `WorkloadRequeued=False`,
with a message indicating that the `DynamicResourceAllocation` feature gate is not enabled.

Administrators who intentionally want to admit DRA workloads without Kueue quota
management can disable `KueueDRARejectWorkloadsWhenDRADisabled` and `DynamicResourceAllocation` to restore the previous behavior.

The steps above are reflected in the complete configuration and workload example below:

```yaml
# Step 1: Configure device class mappings in Kueue Configuration
apiVersion: v1
kind: ConfigMap
metadata:
  name: kueue-controller-manager-config
  namespace: kueue-system
data:
  config.yaml: |
    apiVersion: config.kueue.x-k8s.io/v1beta2
    kind: Configuration
    namespace: kueue-system
    resources:
      deviceClassMappings:
      - name: whole-gpus
        deviceClassNames:
        - gpu.example.com # Maps gpu.example.com -> whole-gpus
---
# Step 2: Define ClusterQueue with DRA resource quotas
apiVersion: kueue.x-k8s.io/v1beta2
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
      - name: 'whole-gpus' # References the resource name from Configuration
        nominalQuota: 2
---
# Step 3: Create ResourceClaimTemplate (only templates are supported)
apiVersion: resource.k8s.io/v1
kind: ResourceClaimTemplate
metadata:
  namespace: gpu-test1
  name: single-gpu
spec:
  spec:
    devices:
      requests:
      - name: gpu
        exactly:
          deviceClassName: gpu.example.com # Device class from the mapping
---
# Step 4: Submit workload using ResourceClaimTemplate
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
          - name: gpu # Reference to the resource claim
          requests:
            cpu: 1
            memory: "200Mi"
      resourceClaims:
      - name: gpu
        resourceClaimTemplateName: single-gpu # Must use template, not direct claim
---
# Step 5: Resulting Workload status after admission
apiVersion: kueue.x-k8s.io/v1beta2
kind: Workload
metadata:
  name: job-job0-6f46e
  namespace: gpu-test1
# ... spec omitted for brevity ...
status:
  admission:
    clusterQueue: gpus-cluster-queue
    podSetAssignments:
    - count: 1
      flavors:
        cpu: default-gpu-flavor
        memory: default-gpu-flavor
        whole-gpus: default-gpu-flavor # DRA resource assigned to flavor
      name: main
      resourceUsage:
        cpu: "1"
        memory: "200Mi"
        whole-gpus: "1" # DRA device count reflected in status
```

### Extended Resources

This section is gated behind the `KueueDRAIntegrationExtendedResource` Kueue feature gate.

Kueue also supports workloads requesting DRA devices via `resources.requests` (e.g., `example.com/gpu: 1`).
When a DeviceClass has `spec.extendedResourceName` set, kube-scheduler automatically creates ResourceClaims.
This requires the Kubernetes `DRAExtendedResource` feature gate (alpha in k8s 1.35).

An extended resource can be identified by verifying that qualified resource names containing `/` are not in the `kubernetes.io/` or `requests.` namespaces and are not standard resources like `cpu`, `memory`, `ephemeral-storage`, or `hugepages-*`.

#### Configuration

The extended resources path does not require `deviceClassMappings`. Kueue auto-discovers
DeviceClasses via a field indexer on `spec.extendedResourceName` and uses the
`extendedResourceName` as the default quota key. If the DeviceClass is also in
`deviceClassMappings`, Kueue uses the mapped logical name instead to unify quota
with the ResourceClaimTemplate path.

DeviceClass with `extendedResourceName` (DeviceClass API is v1/GA, but the `extendedResourceName`
field requires the Kubernetes `DRAExtendedResource` feature gate):
```yaml
apiVersion: resource.k8s.io/v1
kind: DeviceClass
metadata:
  name: gpu.example.com
spec:
  extendedResourceName: example.com/gpu
```

ClusterQueue uses the `extendedResourceName` directly as the quota resource:
```yaml
apiVersion: kueue.x-k8s.io/v1beta2
kind: ClusterQueue
metadata:
  name: gpu-queue
spec:
  resourceGroups:
  - coveredResources: ["example.com/gpu"]
    flavors:
    - name: default
      resources:
      - name: example.com/gpu
        nominalQuota: 8
```

No Kueue configuration changes are needed. No `deviceClassMappings` entry is required for
extended resources. This is a clean separation from the ResourceClaimTemplate path, which
continues to use `deviceClassMappings`.

#### Path Separation

The two DRA paths resolve quota independently:
1. **Extended resources** (`KueueDRAIntegrationExtendedResource` gate): auto-discovers DeviceClass via
   field indexer. Uses `extendedResourceName` as the default quota key. If the resolved
   DeviceClass is also present in `deviceClassMappings`, Kueue uses the mapped logical
   name instead to unify quota with the ResourceClaimTemplate path. If the mapping has
   counter sources configured, the workload is marked inadmissible because extended resources
   do not carry the profile-level information needed for counter-based charging.
2. **ResourceClaimTemplates** (`DynamicResourceAllocation` gate): uses `deviceClassMappings`
   to map DeviceClass names to logical resource names. When the mapping has counter sources
   configured, charges counter units instead of device count.

#### Processing Flow

1. Kueue detects extended resources in `resources.requests`
2. Looks up DeviceClass by `extendedResourceName` by field indexer
3. If no matching DeviceClass is found, the resource is not DRA-backed and Kueue
   processes it through the standard resource quota path (counted via `node.Status.Allocatable`)
4. If a matching DeviceClass is found, resolves the quota key. If the DeviceClass is also
   in `deviceClassMappings`, uses the mapped logical name. Otherwise uses `extendedResourceName`.
5. If the mapping has counter sources configured, the workload is marked inadmissible.
   Extended resources do not carry profile-level information for counter-based charging.
   Otherwise charges device count.
6. Removes original extended resource from the workload's effective resource requests
   (tracked internally per PodSet) to avoid double-counting
7. Admits workload against the resolved quota key

The extended resource translation reads directly from the workload spec before
`excludeResourcePrefixes` filtering is applied. The processing order:
1. Extended resource translation runs first, reading the original spec
2. `excludeResourcePrefixes` filters the pod's `resources.requests`
3. Original extended resource is removed from the workload's effective resource requests
4. Translated resource is added through `preprocessedDRAResources`

This ensures no overlap or double-counting between the two mechanisms.

#### Same Hardware with Both Paths

When the same hardware needs to serve both ResourceClaimTemplate users and extended resource
users, admins configure separate flavors under the same ClusterQueue. Assuming a cluster
with 1 node and 8 GPU devices available:

```yaml
# DeviceClass
apiVersion: resource.k8s.io/v1
kind: DeviceClass
metadata:
  name: gpu.example.com
spec:
  extendedResourceName: example.com/gpu
---
# Kueue config: deviceClassMappings only needed for ResourceClaimTemplate path
apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
resources:
  deviceClassMappings:
  - name: gpu-claims
    deviceClassNames:
    - gpu.example.com
---
# ClusterQueue with quota for both paths
apiVersion: kueue.x-k8s.io/v1beta2
kind: ClusterQueue
metadata:
  name: gpu-queue
spec:
  resourceGroups:
  - coveredResources: ["example.com/gpu", "gpu-claims"]
    flavors:
    - name: default
      resources:
      - name: example.com/gpu    # for extended resource users
        nominalQuota: 4
      - name: gpu-claims          # for ResourceClaimTemplate users
        nominalQuota: 4
```

Both quota buckets draw from the same physical hardware. The admin controls how capacity is
split between the two user populations. Since these are different resource names, the split
is fixed at configuration time.

#### DeviceClass Resolution via Field Indexer

Kueue resolves `extendedResourceName` to DeviceClasses using a controller-runtime field indexer
on `DeviceClass` by `spec.extendedResourceName`. This provides fast lookups without adding
dependencies on non-staging k8s repos.

Even when multiple DeviceClasses share the same `extendedResourceName` (which K8s
[permits with deterministic tiebreaking](https://github.com/kubernetes/kubernetes/blob/v1.35.0/staging/src/k8s.io/api/resource/v1/types.go#L1816-L1820)),
Kueue still treats the resource as DRA-backed. The quota key is the `extendedResourceName`
itself, not the DeviceClass name, so multiple matching DeviceClasses do not affect quota accounting.

#### DeviceClass Lifecycle Scenarios

1. Two DeviceClasses with the same `extendedResourceName`: per KEP-5004, admins should ensure
   one `extendedResourceName` maps to at most one DeviceClass.

2. New DeviceClass created after Kueue admits a workload: a TOCTOU gap exists where Kueue
   and the scheduler may resolve differently. `waitForPodsReady` handles scheduling failures.
   See [Risks and Mitigations](#risks-and-mitigations) for the full breakdown.

3. DeviceClass `extendedResourceName` updated: the DeviceClass event handler triggers the
   workload controller's Reconcile, and only the pending workloads requesting the affected
   `extendedResourceName` are requeued (resolved via the workload index). The same TOCTOU
   considerations as scenario 2 apply.

#### Late DeviceClass Creation

If a DeviceClass does not exist when a workload is created, the extended resource
is treated as a normal (non-DRA) extended resource and may become inadmissible if the
ClusterQueue only has quota for the DeviceClass-mapped logical name.

The workload controller watches DeviceClass objects for create, update, and delete events.
When a DeviceClass changes, only the pending workloads requesting the specific
`extendedResourceName` from that DeviceClass are requeued for re-evaluation. Workloads
with domain-qualified resources that are not DRA-backed (e.g., `example.com/gpu` without
a corresponding DeviceClass) skip DRA processing entirely.

### Partitionable Devices

This section is gated behind the `KueueDRAIntegrationPartitionableDevices` Kueue feature gate.

Kueue supports counter-based quota for partitionable DRA devices as defined by
Kubernetes [KEP-4815](https://github.com/kubernetes/enhancements/issues/4815). Instead of
counting devices, Kueue tracks counter consumption (e.g., GPU memory) from the
`SharedCounters` and `ConsumesCounters` fields on ResourceSlices.

Counter-based resources fit into Kueue's existing (Flavor, Resource) quota model.
Borrowing, lending, cohorts, preemption, and fair sharing work with counter resources.
The `deviceSelector` ensures accurate charging by narrowing the accounting
domain. See [Processing Flow](#processing-flow-1) for details.

#### ResourceSlice Structure

Starting in K8s 1.35, `SharedCounters` and `Devices` are mutually exclusive in a single
ResourceSlice (`+zeroOrOneOf=ResourceSliceType`). Drivers must split them into separate
slices in the same pool. On K8s 1.34 (where partitionable devices are alpha) this
validation does not apply and some drivers put both in one slice
(`resourceSliceCount: 1`). Pool completeness checks `len(slices) == resourceSliceCount`
so both layouts work.

The example below shows the separate-slice layout. Only the `memory` counter is shown.
The driver also
publishes `multiprocessors` and `memory-slice-0` through `memory-slice-7` which the
kube-scheduler uses for MIG placement but Kueue does not need for quota:

```yaml
# ResourceSlice 1: SharedCounters (total capacity for this GPU)
spec:
  driver: gpu.nvidia.com
  pool:
    name: node1-gpu0
    generation: 1
    resourceSliceCount: 2
  sharedCounters:
  - name: gpu-0-counter-set
    counters:
      memory:
        value: 80Gi
      # Driver also publishes multiprocessors, memory-slice-0 through
      # memory-slice-7 - used by kube-scheduler, not by Kueue.
---
# ResourceSlice 2: Devices with ConsumesCounters
spec:
  driver: gpu.nvidia.com
  pool:
    name: node1-gpu0
    generation: 1
    resourceSliceCount: 2
  nodeName: node1
  devices:
  - name: gpu-0-mig-1g.10gb-0
    attributes:
      gpu.nvidia.com/profile:
        string: "1g.10gb"
    consumesCounters:
    - counterSet: gpu-0-counter-set
      counters:
        memory:
          value: 9856Mi
  - name: gpu-0-mig-7g.80gb-0
    attributes:
      gpu.nvidia.com/profile:
        string: "7g.80gb"
    consumesCounters:
    - counterSet: gpu-0-counter-set
      counters:
        memory:
          value: 80Gi
```

#### User Workload

Users request MIG profiles via CEL selectors on ResourceClaimTemplates. Counter tracking
is transparent to the user:

```yaml
apiVersion: resource.k8s.io/v1
kind: ResourceClaimTemplate
metadata:
  name: mig-1g-10gb
spec:
  spec:
    devices:
      requests:
      - name: gpu
        exactly:
          deviceClassName: mig.nvidia.com
          selectors:
          - cel:
              expression: "device.attributes['gpu.nvidia.com'].profile == '1g.10gb'"
```

#### Configuration

The existing `DeviceClassMapping` struct is extended with an optional `sources` field.
When a counter source is present, Kueue tracks quota in counter units (e.g., GPU memory)
instead of device count. This can allow whole-GPU and MIG DeviceClasses to share a single
quota pool. See [Path Interactions](#path-interactions) for caveats on unified pool charging.

The `DeviceClassMapping` struct is extended:

```golang
type DeviceClassMapping struct {
    // ...existing fields (Name, DeviceClassNames)...

    // Sources configures resource accounting sources for this mapping.
    // Each source defines how quota is tracked for this DeviceClass.
    // Currently only counter sources are supported (for partitionable devices).
    // Extended resource requests that resolve to a DeviceClass with sources
    // configured are marked inadmissible.
    // Requires the KueueDRAIntegrationPartitionableDevices feature gate.
    // +optional
    Sources []DeviceClassSourceConfig `json:"sources,omitempty"`
}

// DeviceClassSourceConfig defines a resource accounting source for a DeviceClassMapping.
// Exactly one of the source types must be set.
type DeviceClassSourceConfig struct {
    // Counter configures counter-based quota for partitionable devices.
    // Maps a DRA driver counter to the parent DeviceClassMapping's Kueue quota resource.
    // +optional
    Counter *DeviceClassCounterSource `json:"counter,omitempty"`
}

// DeviceClassCounterSource identifies where to read counter data from and which counter to track.
type DeviceClassCounterSource struct {
    // Name is the counter name within the device's consumesCounters
    // entries to track for quota. Must match a counter name published by
    // the driver in ResourceSlice devices' consumesCounters field.
    // Counter set names are per-device identifiers (e.g., gpu-0-counter-set,
    // gpu-1-counter-set), so name matches across all counter sets
    // for a given driver without requiring one mapping per device.
    // +required
    Name string `json:"name"`

    // Driver is the DRA driver name used to filter relevant ResourceSlices.
    // Must match the spec.driver field on ResourceSlice objects.
    // +required
    Driver string `json:"driver"`

    // DeviceSelector scopes which devices are eligible for counter-based
    // quota accounting. Typically matches a GPU model (e.g., productName)
    // so all partition profiles on that model share one quota pool.
    // Per-workload charging is determined by the workload's own
    // ResourceClaimTemplate selector, which narrows to the requested profile.
    // The selector is compiled at config load time using the upstream dracel
    // compiler.
    // +required
    DeviceSelector resourcev1.DeviceSelector `json:"deviceSelector"`
}
```

Multi-profile MIG setup sharing a single `gpu.memory` quota pool. The
`deviceSelector` scopes the accounting domain to devices from the configured
driver. Per-workload charging comes from the workload's own ResourceClaimTemplate selector, which narrows
to the requested profile:

```yaml
# Kueue Configuration
apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
featureGates:
  KueueDRAIntegration: true
  KueueDRAIntegrationPartitionableDevices: true
resources:
  deviceClassMappings:
  - name: gpu.memory
    deviceClassNames: [mig.nvidia.com]
    sources:
    - counter:
        name: memory
        driver: gpu.nvidia.com
        deviceSelector:
          cel:
            expression: "device.driver == 'gpu.nvidia.com'"
---
# ClusterQueue: 10 A100 GPUs worth of memory
apiVersion: kueue.x-k8s.io/v1beta2
kind: ClusterQueue
metadata:
  name: gpu-queue
spec:
  resourceGroups:
  - coveredResources: ["gpu.memory"]
    flavors:
    - name: a100-pool
      resources:
      - name: gpu.memory
        nominalQuota: "800Gi"
```

Workloads requesting different MIG profiles share the same `gpu.memory` quota. Kueue
matches devices using both the DeviceClass selectors and the workload's ResourceClaimTemplate selectors
(step 4 in [Processing Flow](#processing-flow-1)), then reads `consumesCounters.memory`
from the matched devices:

```yaml
# Workload A: requests 1g.10gb profile
apiVersion: resource.k8s.io/v1
kind: ResourceClaimTemplate
metadata:
  name: mig-small
spec:
  spec:
    devices:
      requests:
      - name: gpu
        exactly:
          deviceClassName: mig.nvidia.com
          count: 1
          selectors:
          - cel:
              expression: "device.attributes['gpu.nvidia.com'].profile == '1g.10gb'"
---
# Workload B: requests 7g.80gb profile
apiVersion: resource.k8s.io/v1
kind: ResourceClaimTemplate
metadata:
  name: mig-large
spec:
  spec:
    devices:
      requests:
      - name: gpu
        exactly:
          deviceClassName: mig.nvidia.com
          count: 1
          selectors:
          - cel:
              expression: "device.attributes['gpu.nvidia.com'].profile == '7g.80gb'"
```

Resulting quota usage against `gpu.memory: 800Gi`:

| Workload | ResourceClaimTemplate selector | consumesCounters.memory | Charge |
|----------|-------------|------------------------|--------|
| A | `profile == '1g.10gb'` | 9856Mi | `gpu.memory: 9856Mi` |
| B | `profile == '7g.80gb'` | 80Gi | `gpu.memory: 80Gi` |

The `deviceSelector` does not select the MIG profile. It scopes which devices
are eligible for counter-based accounting. The workload's own ResourceClaimTemplate selector narrows to the
requested profile. Different profiles produce different charges against the same quota
because Kueue reads the actual `consumesCounters` value from the matched devices. When a
workload uses a broad ResourceClaimTemplate selector matching multiple profiles, Kueue charges conservatively
using the maximum `consumesCounters` value across matched devices.

`deviceSelector` is required when a counter source is configured. Single-profile
DeviceClasses that match devices with identical counter values do not need counter-based
quota and can use device-count quota instead.

When `sources` is absent on a mapping, the mapping behaves exactly as today (device-count
quota). This is backward compatible and covers non-GPU DRA devices where device count is
the appropriate unit.

Counter names are driver-specific, so the counter source's `name` maps them to the Kueue
quota resource name. The admin chooses which counters to track for quota.

#### Processing Flow

Partitionable devices reuses the upstream `dracel` compiler from the CEL expression
support but performs its own
ResourceSlice listing with pool-aware processing, since the CEL validation path does not
return matched device objects or do pool grouping:

1. Kueue looks up the ResourceClaimTemplate's DeviceClass in `deviceClassMappings`
2. If the mapping has counter sources configured, enters the counter-based path
3. Lists ResourceSlices for the configured driver, groups them by pool and checks completeness
4. Filters devices by `deviceSelector` to narrow the candidate pool
5. From filtered devices, matches using DeviceClass selectors and the workload's
   ResourceClaimTemplate request selectors using the `dracel` compiler
6. For each matched device, resolves the counter charge:
   - Device has `consumesCounters` containing the configured `name`: uses the
     actual value (e.g., 9856Mi for a 1g.10gb MIG profile)
   - Device has `consumesCounters` but `name` is not found: workload is
     marked inadmissible
   - Device has no `consumesCounters`: workload is marked inadmissible
7. Uses the maximum consumption across matched devices as the per-device counter charge
8. For requests with `count > 1`, multiplies per-device consumption by count

Counter resources are injected through the existing `WithPreprocessedDRAResources` path.

In Alpha, partitionable devices performs its own ResourceSlice listing independently from
the CEL validation path because the two have different requirements: CEL validation only
needs a match count, while counter processing needs matched device objects and pool-aware
grouping. In Beta, these can be consolidated into a shared ResourceSlice listing layer.

The workload is marked inadmissible if the matching device count is less than the
requested count, or if ResourceSlice data is unavailable. Workloads are never admitted
with zero counter charge when counter sources are configured. For each pool, Kueue only
considers ResourceSlices with the latest generation. If a pool's slice count is less
than its `resourceSliceCount`, the pool is incomplete and its devices are excluded from
matching.

For `count > 1`, the per-device consumption is multiplied by count.

The Kubernetes API limits each device to 2 `consumesCounters` entries
(`ResourceSliceMaxDeviceCounterConsumptionsPerDevice`), each referencing a different counter
set. In Alpha, only single-node partitionable devices are supported where each device
consumes from a single local counter set. Multi-counter-set semantics (e.g., a device
consuming from both a local GPU pool and a shared NVLink fabric pool) will be addressed
with multi-host support in future work.

#### Path Interactions

A `deviceClassMappings` entry uses either device-count or source-based quota, determined
by whether `sources` is set. A DeviceClass appears in exactly one mapping entry.

When a counter source is set, the charge comes from the matched device's `consumesCounters`.
If the device has no `consumesCounters`, the workload is marked inadmissible.

**Extended resources and counter sources are not supported together:**

If an extended resource request resolves to a DeviceClass whose mapping has counter sources
configured, the workload is marked inadmissible. Extended resources carry only a device
count (e.g., `nvidia.com/gpu: 1`) without profile-level CEL selectors, so Kueue cannot
determine an accurate counter charge. Workloads requiring counter-based quota should use
ResourceClaimTemplates with CEL selectors.

**Unified quota pools:**

Whole-GPU and MIG DeviceClasses can share one mapping entry with counter sources configured.
The `deviceSelector` must be compatible with both request shapes for unified
charging to work. Workloads that need both a whole GPU and a MIG slice from the same
counter pool should use ResourceClaimTemplates for both.

**Cohort and borrowing:**

Counter resources participate in Cohort borrowing and lending like any other resource.
`nominalQuota`, `borrowingLimit`, and `lendingLimit` are all in counter units. A
ClusterQueue with `nominalQuota: "0"` borrows counter capacity from other ClusterQueues
or the Cohort. Counter resources use a distinct resource name (e.g., `gpu.memory`) so
borrowing only operates within the same resource.

#### Counter Lifecycle Scenarios

1. **No counter data on matched devices**: the driver published devices without
   `consumesCounters` entries. The workload is marked inadmissible. Drivers using
   partitionable devices must publish `consumesCounters` on all devices for counter-based
   quota to work.

2. **Non-existent counter name**: the configured `name` does not match any entry in
   matched devices' `consumesCounters`. The workload is marked inadmissible.

3. **ResourceSlice changes after admission**: Kueue does not re-evaluate admitted workloads.
   If ResourceSlices change and the scheduler cannot find a matching device, the pod stays
   pending and `waitForPodsReady` evicts the workload. If the scheduler allocates a smaller
   partition than what Kueue charged, the pod runs fine but quota stays over-reserved until
   the workload finishes.

4. **Driver restart**: ResourceSlices may temporarily disappear. Workloads submitted during
   this window are marked inadmissible. Kueue does not currently have a ResourceSlice
   informer, so inadmissible workloads are only re-evaluated when the ClusterQueue is
   notified through other events. Event-driven requeuing on ResourceSlice changes is an
   Alpha graduation criterion.

#### Validation

- At config load time: when `sources` is present on a `deviceClassMappings` entry, the
  `KueueDRAIntegrationPartitionableDevices` feature gate must be enabled. `sources` must have exactly
  one entry in Alpha, and it must be a counter source. Each counter source is validated for
  required fields (`name`, `driver`, `deviceSelector`). The `deviceSelector` CEL expression
  is compiled at config load time using the upstream `dracel` compiler to catch syntax
  and type errors early. Exactly one source type must be set per entry.
- Duplicate `(driver, name)` tuples within a single mapping's `sources` are
  rejected. Across different mappings, the same `(driver, name)` is allowed
  since DeviceClass uniqueness already prevents double-counting. This supports separate
  quota for GPU models that share a driver and counter name (e.g., A100 vs H100).
- At runtime: no cross-validation between `name` and actual ResourceSlice counter
  names. This is consistent with how `deviceClassMappings` does not validate DeviceClass
  existence at config load time.
- `KueueDRAIntegrationPartitionableDevices` requires `KueueDRAIntegration` to be enabled. Validated
  at startup in `pkg/config/validation.go`.

### Architecture Details

#### Queue Manager Extensions

The queue manager has been extended to support DRA resource preprocessing through the InfoOption pattern:

```golang
// Extended method signatures
func (m *Manager) AddOrUpdateWorkload(wl *kueue.Workload, opts ...workload.InfoOption) error
func (m *Manager) UpdateWorkload(oldWl, newWl *kueue.Workload, opts ...workload.InfoOption) error

// DRA-specific InfoOption
func WithPreprocessedDRAResources(
	draResources map[kueue.PodSetReference]corev1.ResourceList,
	replacedExtendedResources map[kueue.PodSetReference]sets.Set[corev1.ResourceName],
) workload.InfoOption
```

Processing Flow:
1. DRA Preprocessing: Controller processes ResourceClaimTemplates and calculates resource requirements
2. InfoOption Creation: Preprocessed resources are wrapped in `WithPreprocessedDRAResources` option
3. Queue Integration: Queue manager receives workload with preprocessed DRA data
4. Scheduler Access: Scheduler gets workload with DRA resources already calculated and validated

This architecture separates concerns between DRA processing (controller) and queue management (scheduler), enabling robust error handling and retry logic for DRA-specific operations.

### Integration with Admission Fair Sharing

DRA logical resources participate in Admission Fair Sharing (AFS) when both DRA and AFS are enabled.
The logical resource from `deviceClassMappings.name` lands in the workload's admitted `ResourceUsage`,
and the AFS penalty accounting mechanism applies weights to all resources without filtering for DRA. This means
the existing `AdmissionFairSharing.ResourceWeights` configuration handles DRA resources naturally
without any additional API fields.

**Configuration Example:**
```yaml
admissionFairSharing:
  usageHalfLifeTime: 10m
  usageSamplingInterval: 5m
  resourceWeights:
    whole-gpus: 5.0  # GPUs are weighted 5x compared to default resources
resources:
  deviceClassMappings:
  - name: whole-gpus
    deviceClassNames:
    - gpu.example.com
```

When a workload with DRA resources is admitted, the logical resource usage (e.g., `whole-gpus`) is
tracked in `LocalQueue.Status.FairSharing.AdmissionFairSharingStatus.ConsumedResources` and factored
into admission ordering decisions. Administrators can use `resourceWeights` to express that GPU time
is more valuable than CPU time for fair sharing purposes.

### MultiKueue Integration

DRA workloads are supported with MultiKueue through the existing workload synchronization mechanism. ResourceClaimTemplates must be deployed on worker clusters by users; they are not automatically synced.

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
- pkg/cache/queue/manager.go: 09/17/2025 - 61.5%
- pkg/config/validation.go: 09/17/2025 - 97.3%
- pkg/controller/core/workload_controller.go: 09/17/2025 - 55.8%
- pkg/dra/claims.go: 09/17/2025 - 83.3%
- pkg/dra/extended_resources.go: TODO (pkg/dra overall: 89.6%)
- pkg/workload/workload.go: 09/17/2025 - 72.3%

#### Integration tests

Integration tests in Kueue use controller-runtime's envtest framework, which provides a real Kubernetes API server
without requiring kubelet or other cluster components. While DRA device allocation requires kubelet plugins, the core
DRA integration functionality for Kueue can be tested at the integration level by:

- Testing Configuration API validation for device class mappings
- Verifying workload admission logic with DRA ResourceClaimTemplates
- Testing quota enforcement against device class mappings
- Validating resource counting and flavor assignment for DRA resources
- Testing error scenarios (feature gate disabled, unsupported ResourceClaims, unmapped device classes)
- Verifying DRA workload processing in Reconcile loop vs event handlers

The integration tests focus on Kueue's quota management and admission logic rather than actual device allocation,
using mock ResourceClaimTemplates and DeviceClasses to simulate DRA workloads. Key test scenarios include:

- Configuration validation: Testing device class conflict detection
- Workload inadmissibility: Testing various error conditions and proper WorkloadInadmissible condition setting
- Resource preprocessing: Verifying correct device count calculation from ResourceClaimTemplates
- Queue integration: Testing workload admission with preprocessed DRA resources
- Extended Resources: Testing extended resource detection, DeviceClass lookup, and resource translation
- Late DeviceClass creation: Testing workload inadmissibility when DeviceClass does not exist
- CEL validation: Testing CEL compilation errors, evaluation against ResourceSlice devices, and rejection
  of workloads with unsatisfiable CEL selectors
- DRA disabled rejection: Testing that workloads with ResourceClaimTemplates or ResourceClaims are
  rejected as inadmissible when `DynamicResourceAllocation` is off and `KueueDRARejectWorkloadsWhenDRADisabled` is on,
  and that non-DRA workloads are still admitted normally
- Partitionable devices: Testing `sources` config validation on `deviceClassMappings`
  (required fields, duplicate driver+name tuples, device selector CEL compilation
  at config load time, exactly one source type set)
- Extended resources with counter sources rejection: extended resource request resolved to a
  DeviceClass with counter sources is marked inadmissible
- Counter consumption: Verifying counter charge from matched devices' `consumesCounters`,
  device selector-based pre-filtering,
  maximum consumption across matched devices, count multiplication for `count > 1`
- Unified quota: Whole-GPU and MIG DeviceClasses sharing one quota pool via single
  `deviceClassMappings` entry with counter sources
- Counter inadmissibility: Workload inadmissible when ResourceSlice data is unavailable,
  pool is incomplete, configured `name` has no match in devices, or device has no
  `consumesCounters` not present on matched device
- ResourceSlice handling: Pool completeness via generation and resourceSliceCount,
  correct filtering by driver name

#### E2E Test

Use existing dra-example-driver or Kubernetes test driver for e2e testing. For partitionable
devices, integration tests create ResourceSlice objects directly via the API since no test
driver publishes `SharedCounters` yet
([kubernetes-sigs/dra-example-driver#150](https://github.com/kubernetes-sigs/dra-example-driver/pull/150)
tracks adding this). This follows the same pattern as upstream K8s integration tests in
`test/integration/dra/`.

### Graduation Criteria

#### Alpha

##### KueueDRAIntegration (v0.14)

- ResourceClaimTemplate-based DRA quota accounting
- support v1 API of DRA in core k8s
- initial e2e tests for baseline scenario

##### KueueDRAIntegrationExtendedResource (v0.17)

- DeviceClass auto-discovery via field indexer on `extendedResourceName`
- extended resource detection and resource translation
- double-counting prevention with `deviceClassMappings`

##### KueueDRAIntegrationExtendedResource (v0.18)

- event-driven DeviceClass tracking for late DeviceClass creation
- DRABackedResources cache to ensure non-DRA workloads with domain-qualified resources
  skip DRA processing
- workload index by extended resource names for targeted DeviceClass event handling
- integration and e2e tests for DeviceClass lifecycle scenarios

##### KueueDRAIntegrationPartitionableDevices (v0.18)

- support for partitionable devices via counter-based quota (KEP-4815, beta in k8s 1.36)
- CEL expression validation against ResourceSlice devices
- event-driven requeuing of inadmissible workloads on ResourceSlice changes

#### Beta

##### KueueDRAIntegration (v0.18)

- feature gate enabled by default
- support integration with MultiKueue
- e2e tests
- CEL expression validation against ResourceSlice devices
- re-evaluate post-scheduling quota reconciliation for DeviceClass drift

##### KueueDRAIntegrationExtendedResource

- feature gate enabled by default

##### KueueDRAIntegrationPartitionableDevices

- feature gate enabled by default
- re-evaluate extending ResourceSlice indexing to CEL validation path. Counter processing
  already uses the driver-based index but CEL validation lists all ResourceSlices unfiltered.
- re-evaluate handling of devices with multiple ConsumesCounters entries referencing
  different counter sets. Currently only the first matching entry is used.
- re-evaluate MAX-based counter charging semantics for heterogeneous device profiles
  within the same pool, such as mixed MIG sizes where charging the maximum value
  across all matched devices may overcharge.
- re-evaluate pool-aware flavor assignment for counter resources
- re-evaluate caching `deviceSelector` evaluation results to avoid repeated ResourceSlice
  evaluation
- re-evaluate consolidating ResourceSlice listing between CEL validation and counter
  processing into a shared layer
- re-evaluate multi-counter tracking, such as memory and compute as separate quota resources
  for the same DeviceClass, by relaxing the DeviceClass uniqueness constraint across
  mappings or the single counter source limitation within a mapping

#### GA

##### KueueDRAIntegration

- the feature gate in stable
- TAS + DRA integration and testing
- re-evaluate support for AdminAccess requests
- re-evaluate support for FirstAvailable device selection
- re-evaluate support for AllocationMode All

##### KueueDRAIntegrationExtendedResource

- the feature gate in stable
- user adoption feedback confirms stability
- re-evaluate DeviceClass watcher performance at scale

##### KueueDRAIntegrationPartitionableDevices

- the feature gate in stable
- user adoption feedback with MIG workloads confirms counter-based quota accuracy

## Implementation History

- Initial draft on September 16th 2024 by @kannon92
- Implementation development: September-December 2024
- Design evolution from standalone CRD to Configuration API approach: October 2024
- Alpha implementation completed: December 2024
- KEP updated to reflect actual implementation: September 2025 by @alaypatel07
- Extended Resources implementation: January 2026 by @sohankunkerkar
- Integration with Admission Fair Sharing: April 2026 — added integration tests and documentation
  confirming DRA logical resources work with existing `AdmissionFairSharing.ResourceWeights`
- CEL expression validation support added: April 2026 by @kannon92
- Promoted KueueDRAIntegration to Beta: May 2026 by @sohankunkerkar
- Partitionable devices support: May 2026 by @sohankunkerkar
- `KueueDRARejectWorkloadsWhenDRADisabled` feature gate added: May 2026 by @kannon92 — rejects DRA workloads
  when the `DynamicResourceAllocation` feature gate is disabled to prevent silent quota bypass
  (see [#10504](https://github.com/kubernetes-sigs/kueue/issues/10504))

**Key Design Evolution:**
- **Original Design**: Standalone DynamicResourceAllocationConfig CRD with runtime ambiguity resolution
- **Final Implementation**: Configuration API extension with strict validation and conflict prevention
- **Architecture Decision**: DRA processing moved to Reconcile loop for proper error handling
- **Scope Refinement**: ResourceClaims support removed, focus on ResourceClaimTemplates only
- **Extended Resources**: Added support for workloads requesting DRA devices via `resources.requests`
  using DeviceClass `extendedResourceName` field (alpha in k8s 1.35)

## Drawbacks

**Configuration Restart Requirement**: Changes to device class mappings require controller restart, which may cause brief service interruption. This is acceptable for alpha feature but should be addressed in future versions.

**ResourceClaims Not Supported**: Users with existing ResourceClaim-based workloads cannot use Kueue quota management and must migrate to ResourceClaimTemplates.

**Limited Dynamic Reconfiguration**: Unlike some other Kueue features, DRA configuration cannot be changed dynamically and requires controller restart.

## Alternatives

### Webhook Rewriting Extended Resources to ResourceClaimTemplates

For extended resources support, an alternative approach was considered: use Kueue's existing
mutating webhook to rewrite extended resource requests (e.g., `example.com/gpu: 1`) into
ResourceClaimTemplate references at admission time. This would eliminate the need for a
separate DeviceClass resolution path in Kueue, since the existing ResourceClaimTemplate
processing would handle quota accounting.

This approach was rejected for several reasons:
1. Creating a ResourceClaimTemplate from a webhook is a side effect, violating the
   `sideEffects: None` declaration on Kueue webhooks.
2. Late DeviceClass creation cannot be handled. If the DeviceClass does not exist when the
   webhook fires, the webhook must either reject the workload (creating an ordering
   dependency on admin configuration) or pass it through unchanged (requiring a controller
   fallback that duplicates the logic).
3. Webhook ordering and reinvocation issues with external frameworks that modify pod specs
   after Kueue's webhook runs.
4. DRA processing in Kueue follows the pattern of handling logic in the Reconcile loop
   rather than event handlers or webhooks, enabling proper error handling and retry.
5. It goes against the architectural direction of reducing webhook surface area in Kueue.

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
  devices:
    requests:
    - name: gpu-large
      exactly:
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
  devices:
    requests:
    - name: middle-or-large-gpu
      exactly:
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

### Creating a new CRD for device class mapping

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
However, this approach was introducing a significant amount of complexity in implementing the feature so it was rejected.

### User Annotation as Primary Counter Consumption Mechanism

Users could declare counter consumption via a pod annotation like
`kueue.x-k8s.io/counter-requests: '{"gpu.memory": "20Gi"}'`. Kueue trusts the annotation,
scheduler handles actual allocation. The problem is the annotation can drift from the CEL
selectors. If the CEL matches a 7g.80gb profile but the annotation says 20Gi, quota is
undercharged. Reading `consumesCounters` from matched devices avoids this because it stays
in sync with what the CEL actually selects.

### Separate counterMappings Struct

Counter config could be a separate top-level `counterMappings` struct alongside
`deviceClassMappings`. This aligns the config surface with data sources (DeviceClasses vs
ResourceSlices) but creates two independent quota pools for the same physical hardware.
Whole-GPU and MIG workloads end up in separate quota dimensions with no way to borrow,
fair-share, or preempt across them.

### Device-Count Quota with Dual Tracking

Quota in device units with Kueue maintaining both device count and counter budget as two
coupled dimensions at runtime. A MIG workload would consume a fractional device (e.g.,
0.25) and a counter value simultaneously, requiring borrowing, preemption, and fair
sharing to reason about both dimensions. The adopted approach uses counter-unit
`nominalQuota` directly, tracking only counter units internally.

### Auto-discovery of Counters Without Configuration

Kueue could read counter names directly from ResourceSlices without needing any
counter config. But counter names are driver-specific (NVIDIA uses `memory`,
others might use `gpu-mem`) and there is no way to connect them to the admin-chosen quota
resource names in the ClusterQueue (e.g., `gpu.memory`) without an explicit mapping.

## Appendix

### Consumable Capacity Compatibility

[KEP-5075 (Consumable Capacity)](https://github.com/kubernetes/enhancements/issues/5075)
enables software-level device sharing where multiple ResourceClaims share one physical
device. Kueue's `(Flavor, Resource)` quota model supports adding Consumable Capacity in
the future. Both partitionable devices and consumable capacity produce a
`resource.Quantity` charge that fits the existing quota tracking, borrowing, preemption,
and fair sharing without changes.
