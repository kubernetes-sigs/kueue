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
  - [Consumable Capacity](#consumable-capacity)
    - [ResourceSlice Structure](#resourceslice-structure-1)
    - [User Workload](#user-workload-1)
    - [Configuration](#configuration-2)
    - [Processing Flow](#processing-flow-2)
    - [Path Interactions](#path-interactions-1)
    - [Capacity Lifecycle Scenarios](#capacity-lifecycle-scenarios)
    - [Validation](#validation-1)
  - [Architecture Details](#architecture-details)
    - [Queue Manager Extensions](#queue-manager-extensions)
  - [Prioritized List Quota](#prioritized-list-quota)
    - [Accounting rule](#accounting-rule)
    - [Safety argument](#safety-argument)
    - [Scope](#scope)
    - [Coordination with feasibility](#coordination-with-feasibility)
    - [Relationship with Kubernetes ResourceQuota](#relationship-with-kubernetes-resourcequota)
    - [Feature gate lifecycle, version skew, and observability](#feature-gate-lifecycle-version-skew-and-observability)
    - [Tradeoffs](#tradeoffs)
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
      - [KueueDRAIntegrationConsumableCapacity (v0.19)](#kueuedraintegrationconsumablecapacity-v019)
      - [KueueDRAIntegrationPrioritizedList (v0.20)](#kueuedraintegrationprioritizedlist-v020)
    - [Beta](#beta)
      - [KueueDRAIntegration (v0.18)](#kueuedraintegration-v018)
      - [KueueDRAIntegrationExtendedResource](#kueuedraintegrationextendedresource)
      - [KueueDRAIntegrationPartitionableDevices](#kueuedraintegrationpartitionabledevices)
      - [KueueDRAIntegrationConsumableCapacity](#kueuedraintegrationconsumablecapacity)
      - [KueueDRAIntegrationPrioritizedList](#kueuedraintegrationprioritizedlist)
    - [GA](#ga)
      - [KueueDRAIntegration](#kueuedraintegration)
      - [KueueDRAIntegrationPrioritizedList](#kueuedraintegrationprioritizedlist-1)
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
  - [KEP-5941 Shared Consumable Capacity](#kep-5941-shared-consumable-capacity)
  - [KEP-5963 Device Compatibility Groups](#kep-5963-device-compatibility-groups)
<!-- /toc -->

## Summary

[Dynamic Resource Allocation (DRA)](https://kubernetes.io/docs/concepts/scheduling-eviction/dynamic-resource-allocation/)
is a major effort to improve device support in Kubernetes. It changes how one can request resources in a myriad of ways.

This KEP supports four approaches for DRA integration with Kueue:
1. **ResourceClaimTemplates**: Pods explicitly reference ResourceClaimTemplates that specify device requests.
2. **Extended Resources**: Pods request DRA devices via standard `resources.requests` (e.g., `example.com/gpu: 1`), and kube-scheduler automatically creates ResourceClaims when the DeviceClass has an `extendedResourceName` field set.
3. **Partitionable Devices**: Counter-based quota for devices that can be dynamically
   partitioned (e.g., NVIDIA MIG). Instead of counting devices, Kueue tracks counter
   consumption (e.g., GPU memory) from the `SharedCounters` and `ConsumesCounters` fields
   defined by [KEP-4815](https://github.com/kubernetes/enhancements/issues/4815).
4. **Consumable Capacity**: Capacity-based quota for devices that allow software-level
   sharing. Kueue tracks consumed capacity dimensions such as GPU memory and compute
   cores from the device's `Capacity` field as defined by
   [KEP-5075](https://github.com/kubernetes/enhancements/issues/5075).

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
- Admins can enforce capacity-based quota for devices that allow software-level sharing
  (e.g., GPU memory and compute cores quota for time-sliced or fractional GPU devices).
- Admins can enforce quota for prioritized-list (`firstAvailable`) requests over count-based
  DeviceClass mappings, charging the component-wise maximum over the alternatives.

### Non-Goals

- Counter-backed or capacity-backed `firstAvailable` (`DRAPrioritizedList`) alternatives are
  out of scope for the initial Alpha. Count-based prioritized-list quota is supported; see
  [Prioritized List Quota](#prioritized-list-quota).
- Support for DRA features like DRADeviceTaints is not included.
- Multi-host partitionable devices (e.g., NVLink fabrics spanning multiple nodes) are not
  supported.
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
7. Supporting capacity-based quota for devices that allow multiple allocations via the
   `capacity` source type on `deviceClassMappings` (requires Kubernetes
   `DRAConsumableCapacity` feature gate, beta in K8s 1.36). Kueue charges the workload's
   `capacity.requests` rounded per the device's `RequestPolicy`.
8. Supporting count-based quota accounting for `firstAvailable` (prioritized alternative) requests
   through a component-wise envelope after DeviceClass-to-logical-resource mapping, behind
   `KueueDRAIntegrationPrioritizedList` (requires Kubernetes `DRAPrioritizedList`, stable in K8s 1.36).

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
- AdminAccess requests are skipped in quota counting (zero charge) since they provide
  shared read-only access to already-allocated devices. DRADeviceTaints is not supported.
- Count-based `firstAvailable` (`DRAPrioritizedList`) quota is supported behind the
  `KueueDRAIntegrationPrioritizedList` feature gate; see
  [Prioritized List Quota](#prioritized-list-quota). Counter-backed and capacity-backed
  alternatives remain unsupported.
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
  validation can make previously-unsatisfiable workloads satisfiable. Kueue has a
  `ResourceSliceReconciler`, but it registers watched drivers only from source-backed
  `deviceClassMappings` (their `sources`), so a mapping with no sources (this CEL-validated
  count path included) registers no driver and is not requeued on ResourceSlice changes;
  such workloads are re-evaluated only when the ClusterQueue is notified through other events
  such as quota changes. Extending event-driven requeuing to cover these paths is an Alpha
  graduation criterion. `WaitForPodsReady`, when enabled, serves as the safety net for cases where
  the validation state diverges from actual device availability at scheduling time.

### Risks and Mitigations

**Silent quota bypass when DRA is disabled**: When the `DynamicResourceAllocation` feature
gate is disabled, DRA workloads are admitted without any device resource accounting, allowing
unlimited GPU consumption outside Kueue's control. The `KueueDRARejectWorkloadsWhenDRADisabled` feature gate
(default: enabled, Beta) mitigates this by rejecting DRA workloads when the DRA feature is off.
See [Workload Rejection When DRA Is Disabled](#workload-rejection-when-dra-is-disabled).

With `DRAPrioritizedList` (stable in K8s 1.36), there is a risk that the effective resource
tally is not available until kube-scheduler allocates. The mitigation for each case is
documented here:
1. For `DRAPrioritizedList`: for count-based mappings, Kueue charges the component-wise maximum
   over the alternatives after DeviceClass-to-logical-resource mapping, which is a per-resource
   upper bound on any single realized allocation (see [Prioritized List Quota](#prioritized-list-quota)).
   Counter-backed and capacity-backed alternatives are rejected until their accounting paths can
   produce per-alternative charge vectors, as are alternatives using allocation mode `All` or a
   mode Kueue does not recognise.
2. AdminAccess requests are skipped in quota counting. This feature can only be enabled in
   admin namespaces (gated by the `resource.kubernetes.io/admin-access` label), and provides
   shared read-only access to already-allocated devices. Charging quota would double-count the
   device. This matches the Kubernetes scheduler which excludes AdminAccess from `allocatedDevices`.
3. For allocation mode `All`: the request is rejected rather than charged. `countDevicesPerClass`
   refuses it on any request it reads, since how many devices the claim would receive is not in the
   spec, and Kueue has no worst-case charge for it. Whether a finite policy can be defined is left
   to a later stage.
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

**Consumable capacity under-charge on exclusive devices**: if the `deviceSelector` matches
devices without `AllowMultipleAllocations`, the scheduler consumes the entire device while
Kueue charges only the partial `capacity.requests` amount. This is specific to capacity
sources because the charge comes from the workload's request, not the device (counter
sources charge from the device's `consumesCounters` and do not have this issue).
Mitigation: include `device.allowMultipleAllocations == true` in the `deviceSelector`.
The general admission-scheduling timing gap applies equally to all DRA source types.
`waitForPodsReady` catches scheduling failures and evicts the workload so quota is
released.

## Design Details

Feature gates controlling DRA support in Kueue:
- `KueueDRAIntegration` (Beta, default on since v0.18): gates ResourceClaimTemplate-based
  DRA quota accounting. Uses `deviceClassMappings` for DeviceClass-to-quota-resource mapping.
- `KueueDRAIntegrationExtendedResource` (Beta, default on since v0.19): gates extended resources support, including
  DeviceClass auto-discovery via `extendedResourceName`. Requires `KueueDRAIntegration`.
- `KueueDRAIntegrationPartitionableDevices` (Beta, default on since v0.19): gates counter-based quota for
  partitionable devices. Enables the `counter` source type on `deviceClassMappings` entries.
  Requires `KueueDRAIntegration`. Also requires the Kubernetes `DRAPartitionableDevices`
  feature gate (beta in K8s 1.36).
- `KueueDRAIntegrationConsumableCapacity` (Alpha): gates capacity-based quota for devices
  that allow multiple allocations. Enables the `capacity` source type on
  `deviceClassMappings` entries. Requires `KueueDRAIntegration`. Also requires the
  Kubernetes `DRAConsumableCapacity` feature gate (beta in K8s 1.36).
- `KueueDRAIntegrationPrioritizedList` (Alpha, default off): gates count-based quota accounting
  for `firstAvailable` (prioritized alternative) requests via the component-wise-max envelope.
  Requires `KueueDRAIntegration`; the manager fails validation at startup if this gate is enabled
  while `KueueDRAIntegration` is off. The upstream `DRAPrioritizedList` gate has been Beta and on by
  default since Kubernetes 1.34 and GA since 1.36, and upstream plans to lock it to enabled in 1.37,
  so the requirement on a cluster is that the gate has not been turned off rather than that an admin
  turns it on. Counter-backed and capacity-backed alternatives are rejected.
- `KueueDRARejectWorkloadsWhenDRADisabled` (Beta, default on since v0.18): rejects workloads
  that use DRA resources (ResourceClaimTemplates or ResourceClaims) when `KueueDRAIntegration`
  is disabled. Without this gate, DRA workloads submitted while `KueueDRAIntegration` is off
  are silently admitted with zero device resource usage, bypassing quota enforcement entirely.
  See [Workload Rejection When DRA Is Disabled](#workload-rejection-when-dra-is-disabled).

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

This validation approach eliminates ambiguity at configuration time rather than requiring
complex runtime resolution logic, ensuring predictable and efficient workload admission.

**Note**: A single mapping can have multiple capacity sources that sum into one quota
resource, which does not violate this constraint. Tracking independent capacity
dimensions as separate quota resources (same DeviceClass, different resource names)
requires relaxing this uniqueness constraint and is deferred to beta.

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
    // Counter sources require KueueDRAIntegrationPartitionableDevices.
    // Capacity sources require KueueDRAIntegrationConsumableCapacity.
    // Extended resource requests that resolve to a DeviceClass with sources
    // configured are marked inadmissible.
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

    // Capacity configures capacity-based quota for devices that allow
    // multiple allocations (consumable capacity).
    // +optional
    Capacity *DeviceClassCapacitySource `json:"capacity,omitempty"`
}

// DeviceClassCounterSource configures counter-based quota tracking
// for partitionable devices (KEP-4815).
type DeviceClassCounterSource struct {
    // Name identifies the counter dimension to track for quota
    // (e.g., "gpu.memory").
    // Must be a valid QualifiedName; the name part must not exceed
    // 63 characters.
    // +required
    Name string `json:"name"`

    // Driver is the DRA driver name used to filter relevant ResourceSlices.
    // Must match the spec.driver field on ResourceSlice objects.
    // Must not exceed 63 characters (DriverNameMaxLength).
    // +required
    Driver string `json:"driver"`

    // DeviceSelector scopes which devices are eligible for quota accounting.
    // Typically matches a GPU model (e.g., productName) so all partition
    // profiles on that model share one quota pool.
    // The selector is compiled at config load time using the upstream dracel
    // compiler.
    // +required
    DeviceSelector resourcev1.DeviceSelector `json:"deviceSelector"`
}

// DeviceClassCapacitySource configures capacity-based quota tracking
// for devices that allow multiple allocations (KEP-5075).
type DeviceClassCapacitySource struct {
    // Name identifies the capacity dimension to track for quota
    // (e.g., "gpu.example.com/memory").
    // Must be a valid DRA QualifiedName.
    // +required
    Name resourcev1.QualifiedName `json:"name"`

    // Driver is the DRA driver name used to filter relevant ResourceSlices.
    // Must match the spec.driver field on ResourceSlice objects.
    // Must not exceed 63 characters (DriverNameMaxLength).
    // +required
    Driver string `json:"driver"`

    // DeviceSelector scopes which devices are eligible for quota accounting.
    // Matches devices whose capacity dimensions should be tracked against
    // the quota pool.
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
   this window are marked inadmissible. The ResourceSlice controller watches for
   create/update/delete events on ResourceSlices matching configured drivers and
   triggers requeuing of inadmissible workloads when device availability changes.

#### Validation

- At config load time: each counter source requires the `KueueDRAIntegrationPartitionableDevices`
  feature gate; each capacity source requires `KueueDRAIntegrationConsumableCapacity`.
  At most one counter source is allowed per mapping; multiple capacity sources are allowed
  (see [Consumable Capacity Validation](#validation-1) for the full cardinality rules).
  Counter and capacity sources must not be mixed within the same mapping. Each source is validated for
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
- Tightened driver name max length from 253 to 63 to match resourcev1.DriverNameMaxLength since Kueue v0.19.0

### Consumable Capacity

This section is gated behind the `KueueDRAIntegrationConsumableCapacity` Kueue feature gate.

Kueue supports capacity-based quota for DRA devices that allow software-level sharing as
defined by Kubernetes [KEP-5075](https://github.com/kubernetes/enhancements/issues/5075).
Kueue tracks consumed capacity dimensions such as GPU memory and compute cores from the
device's `Capacity` field and the workload's `capacity.requests` on `ExactDeviceRequest`.

Only `ExactDeviceRequest` with `count` is supported. `FirstAvailable` subrequests with
`capacity.requests` are not supported, consistent with the existing exclusion for
partitionable devices. Count-based `firstAvailable` quota (see
[Prioritized List Quota](#prioritized-list-quota)) therefore rejects any alternative whose
DeviceClass mapping configures a capacity source.

#### ResourceSlice Structure

Unlike partitionable devices where `SharedCounters` and `Devices` are in separate
ResourceSlices, consumable capacity puts `Capacity` directly on the `Device` alongside
`AllowMultipleAllocations` in a single ResourceSlice:

```yaml
# ResourceSlice: GPU devices with consumable capacity
spec:
  driver: gpu.example.com
  pool:
    name: node1-gpu0
    generation: 1
    resourceSliceCount: 1
  nodeName: node1
  devices:
  - name: gpu-0
    attributes:
      gpu.example.com/model:
        string: "A100-80GB"
    allowMultipleAllocations: true
    capacity:
      gpu.example.com/memory:
        value: "80Gi"
        requestPolicy:
          default: "80Gi"
          validRange:
            min: "1Mi"
            max: "80Gi"
      gpu.example.com/cores:
        value: "100"
        requestPolicy:
          default: "100"
          validValues: ["10", "20", "30", "50", "100"]
```

The `Capacity` field on each device defines the total capacity per dimension. A device
may publish multiple capacity dimensions (memory, cores), but Kueue only tracks the
dimensions explicitly configured in `deviceClassMappings` sources. The `RequestPolicy`
constrains how workloads consume that capacity:
- `Default`: the charge applied when a workload's request omits this capacity dimension
- `ValidValues`: a discrete set of acceptable request amounts (max 10, sorted ascending)
- `ValidRange`: a continuous range with optional `Step` for rounding granularity

#### User Workload

Users request shared capacity via `capacity.requests` on the `ExactDeviceRequest` within
a ResourceClaimTemplate:

```yaml
apiVersion: resource.k8s.io/v1
kind: ResourceClaimTemplate
metadata:
  name: gpu-share-small
spec:
  spec:
    devices:
      requests:
      - name: gpu
        exactly:
          deviceClassName: gpu.example.com
          count: 1
          selectors:
          - cel:
              expression: "device.attributes['gpu.example.com'].model == 'A100-80GB'"
          capacity:
            requests:
              gpu.example.com/memory: "4Gi"
              gpu.example.com/cores: "30"
```

If `capacity.requests` is omitted, the kube-scheduler applies the `RequestPolicy.Default`
value for each capacity dimension, or charges the full `Capacity.Value` if no policy is
set. Kueue applies the same defaults for accurate quota charging.

#### Configuration

The `DeviceClassSourceConfig` struct includes both `Counter` and `Capacity` fields
(see [Partitionable Devices Configuration](#configuration-1) for the full type
definitions). When a capacity source is present, Kueue tracks quota in capacity
units instead of device count.

GPU fractional sharing setup with capacity-based `gpu.memory` quota:

```yaml
# Kueue Configuration
apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
featureGates:
  KueueDRAIntegrationConsumableCapacity: true
resources:
  deviceClassMappings:
  - name: gpu.memory
    deviceClassNames: [gpu.example.com]
    sources:
    - capacity:
        name: "gpu.example.com/memory"
        driver: gpu.example.com
        deviceSelector:
          cel:
            expression: "device.driver == 'gpu.example.com'"
---
# ClusterQueue: 10 GPUs worth of memory (80Gi each)
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

Quota usage examples against `gpu.memory: 800Gi`:

| Workload | capacity.requests | RequestPolicy (from device) | Effective Charge | Quota Charge |
|----------|-------------------|-----------------------------|------------------|--------------|
| A | `memory: 4Gi` | ValidRange(min=1Mi, max=80Gi) | `4Gi` | `gpu.memory: 4Gi` |
| B | `memory: 20Gi` | ValidRange(min=1Mi, max=80Gi) | `20Gi` | `gpu.memory: 20Gi` |
| C | (omitted) | Default=80Gi | `80Gi` | `gpu.memory: 80Gi` |
| D | `memory: 90Gi` | ValidRange(min=1Mi, max=80Gi) | inadmissible | (rejected) |

When `sources` is absent on a mapping, the mapping behaves exactly as today (device-count
quota). This is backward compatible.

A single mapping can have multiple capacity sources, whose charges are summed into the
mapping's quota resource. Each source should track the same dimension (e.g., `gpu.memory`)
with different `deviceSelector` scopes, for example, aggregating memory from two GPU
models into one pool. Mixing different dimensions (e.g., memory + cores) into one mapping
produces a meaningless sum and is not a supported use case. Independent dimensions require
separate mappings with distinct resource names.

Independent dimension tracking (same DeviceClass, different resource names, different
mappings) requires relaxing the DeviceClass uniqueness validation. This is deferred to
beta, consistent with the same deferral for partitionable devices multi-counter support.

#### Processing Flow

The device matching pipeline (steps 1-6) is identical to partitionable devices. Only
the charge computation (step 7) differs: capacity reads the charge from the workload's
request, while counters read it from the device's `consumesCounters`.

Each PodSet in the workload is processed independently. Multi-PodSet workloads (e.g.,
RayJob with head and worker PodSets) can have different capacity requests per PodSet,
and each PodSet's charges are tracked separately in `resourceUsage`.

1. Kueue looks up the ResourceClaimTemplate's DeviceClass in `deviceClassMappings`
2. If the mapping has a capacity source configured, enters the capacity-based path
3. Reads the workload's `capacity.requests` from the `ExactDeviceRequest` for the
   configured capacity dimension name
4. Lists ResourceSlices for the configured driver, groups them by pool and checks
   completeness. Device listing is needed to read `RequestPolicy` for rounding and
   `Default` for omitted requests
5. Filters devices by `deviceSelector` to narrow the candidate pool
6. From filtered devices, matches using DeviceClass selectors and the workload's
   ResourceClaimTemplate request selectors using the `dracel` compiler
7. Resolves the effective charge per device independently, then takes the
   maximum across all matched devices:
   - For each matched device with the capacity dimension, determines the
     base charge: `capacity.requests[<name>]` if specified, otherwise the
     device's own `RequestPolicy.Default` if set, otherwise the device's
     `Capacity.Value`
   - Rounds the base charge against the device's own `RequestPolicy`.
     If the device has no `RequestPolicy`, the base charge is used as-is.
     `ValidValues` rounds up to the smallest valid value >= base;
     `ValidRange` rounds up to `Min` if below, aligns to `Min + n*Step`
     if `Step` is set. If the rounded value exceeds `Max` or all valid
     values, the device is skipped (it cannot satisfy the request)
   - Takes the maximum rounded charge across all devices. This ensures
     Kueue never under-charges even if the `deviceSelector` matches
     heterogeneous devices with different Defaults or policies
   - If no matched device has the capacity dimension or can satisfy the
     request, the workload is marked inadmissible
8. For `count > 1`, multiplies per-device charge by count using saturating
   arithmetic (capped at `MaxInt64` rather than wrapping)

The rounded charge (not the raw request) appears in
`status.admission.podSetAssignments[].resourceUsage`, preserving the quantity format
from the rounded value. Rounding prevents under-charging: the kube-scheduler rounds
requests per `RequestPolicy` before allocating, so Kueue must apply the same rounding
to keep quota charges aligned with actual device consumption.

Inadmissibility rules follow the same pattern as partitionable devices: insufficient
matched devices, unavailable ResourceSlice data, and incomplete pools (latest generation,
`resourceSliceCount` check) all result in inadmissible status.

#### Path Interactions

A `deviceClassMappings` entry uses either device-count, counter-based, or capacity-based
quota, determined by whether and what type of `sources` is set. When counter or capacity
sources are configured for a DeviceClass, the device-count charge is skipped for that
class to avoid double-counting.

**Consumable capacity and partitionable devices:**

`DeviceClassSourceConfig` enforces exactly one source type per entry. Counter sources
track partitioned devices via `consumesCounters`, capacity sources track shared devices
via `Capacity`. These serve different device types and are not combined on a single
mapping entry.

In [Alpha](#alpha), each mapping must have a unique resource name and counter and
capacity sources cannot be mixed within the same mapping. This means a unified
quota pool across both device types (e.g., partitioned and time-sliced GPUs both
charging `gpu.memory`) is not supported. Relaxing the resource name uniqueness
to allow separate counter and capacity mappings to share a quota resource is to
be evaluated for [Beta](#beta). The same DeviceClass cannot appear in two different
mappings due to the DeviceClass uniqueness constraint across mappings.

**Extended resources and capacity sources are not supported together:**

Same rationale as partitionable devices. Extended resource requests carry only a device
count without `capacity.requests`, so Kueue cannot determine an accurate capacity charge.

**Cohort and borrowing:**

Capacity resources participate in Cohort borrowing and lending like any other resource.
`nominalQuota`, `borrowingLimit`, and `lendingLimit` are all in capacity units.

#### Capacity Lifecycle Scenarios

1. **No capacity data on matched devices**: the driver published devices without the
   configured capacity dimension. The workload is marked inadmissible.

2. **RequestPolicy violation**: the workload requests more capacity than
   `ValidRange.Max` or exceeds all `ValidValues`. The workload is marked inadmissible.

3. **Request omits capacity**: For each matched device, Kueue charges the device's
   own `RequestPolicy.Default` if set, or the device's full `Capacity.Value` if
   `Default` is unset or no `RequestPolicy` exists. The maximum charge across all
   devices is used.

4. **AllowMultipleAllocations not set**: the apiserver enforces that `RequestPolicy`
   requires `AllowMultipleAllocations: true`. If the `deviceSelector` also matches
   exclusive devices, the kube-scheduler consumes the entire device while Kueue charges
   only the partial request. Admins should include
   `device.allowMultipleAllocations == true` in the `deviceSelector`.

Scenarios for ResourceSlice changes after admission and driver restart are the same as
[Counter Lifecycle Scenarios](#counter-lifecycle-scenarios) 3-4. The existing ResourceSlice
controller is reused: capacity source drivers are added to the watched driver set at
startup, so create/update/delete events on those ResourceSlices trigger the same
inadmissible workload requeuing. No new controller logic is needed.

#### Validation

- At config load time: when a `capacity` source is present on a `deviceClassMappings`
  entry, the `KueueDRAIntegrationConsumableCapacity` feature gate must be enabled.
  Each source entry must be either a counter or a capacity source (not both). At most
  one counter source is allowed per mapping. Multiple capacity sources are allowed per
  mapping (summed into one quota resource). Counter and capacity sources must not be
  mixed within the same mapping. Each source is validated for required fields (`name`,
  `driver`, `deviceSelector`). The `deviceSelector` CEL expression is compiled at config
  load time using the upstream `dracel` compiler to catch syntax and type errors early.
- At runtime: no cross-validation between `name` and actual ResourceSlice capacity keys.
  This is consistent with how `deviceClassMappings` does not validate DeviceClass
  existence at config load time.
- `KueueDRAIntegrationConsumableCapacity` requires `KueueDRAIntegration` to be enabled.
  Validated at startup in `pkg/config/validation.go`.

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

### Prioritized List Quota

This section is gated behind the `KueueDRAIntegrationPrioritizedList` Kueue feature gate (Alpha,
default off). It adds quota accounting for prioritized-list requests, expressed with the
`firstAvailable` field of a `DeviceRequest` (the Kubernetes `DRAPrioritizedList` feature, GA in
1.36). A `firstAvailable` request lists ordered alternatives, at most eight of them
(`FirstAvailableDeviceRequestMaxSize`); kube-scheduler selects exactly one at allocation time, which
is not known when Kueue reserves quota. With the gate off, such requests remain rejected as
before.

#### Accounting rule

For a top-level `firstAvailable` request `q` with alternatives `A_q`, Kueue resolves each
subrequest's DeviceClass to its logical quota resource through `deviceClassMappings` and forms a
non-negative charge vector `charge(q, a)` per alternative `a`. `DeviceSubRequest` has no
`adminAccess` field, so admin access cannot appear on an alternative and its zero-charge rule stays
confined to `Exactly` requests. The request is charged the component-wise maximum:

```text
envelope(q)[r] = max over a in A_q of charge(q, a)[r]
```

The per-Pod DRA charge is the existing `Exactly` charges plus the sum, over all `firstAvailable`
requests, of `envelope(q)`. PodSet scaling is unchanged: the per-Pod charge is multiplied by the
effective PodSet count by the existing workload-request calculation. With
`ElasticJobsViaWorkloadSlices`, each slice carries its own PodSet count, so each computes its
envelope independently and multiplies the same per-Pod envelope by its own count; nothing is shared
between the slices of one Job.

Saturating arithmetic stops a sum from wrapping, but a saturated sum no longer stands for the value
it replaced, so it cannot carry the bound: clamping to `MaxInt64` reports less than the total it
came from. The merge into `resources.Requests` is weaker still, since `MapRequests.Add` uses plain
`+` and can wrap negative where a DRA logical resource shares a key with an ordinary Pod resource. A
charge that cannot be represented exactly as `int64` therefore makes the workload inadmissible
rather than being clamped and admitted, at each step that can produce one: the per-Pod sum across
requests, the merge with ordinary resources, and the PodSet-count multiplication. Nor is a saturated
aggregate divided to recover a smaller request. `PodSetResources` holds the aggregate alone and
scales by dividing by the old count and multiplying by the new, which is exact for what it admits:
an aggregate that had to be exactly the per-Pod value times the count divides back to that value,
so partial admission, `ReclaimablePods`, and workload-slice replacement recover it rather than
needing it stored.

Converting a combined `resource.Quantity` back to `int64` follows the unit convention in
`resources.ResourceValue`, milli-units for `cpu` and absolute units otherwise. That helper calls
`Value()` for everything but `cpu`, so following it is not on its own enough: a quantity that does
not convert exactly has to be reported rather than silently changed, which is what makes the
rejection above possible. Exactly means at milli scale for `cpu` and at scale zero otherwise, and
covers rounding as well as range, since `Value()` rounds away from zero: a fractional non-`cpu`
quantity, which a transformation output can produce, is as much a rejection as one past `MaxInt64`.
Nothing currently stops an administrator from naming a logical resource `cpu`, where an
unconditional `SafeValue` would read `8` as 8 milliCPU. `pods` is worse, since flavor assignment
writes that key from the PodSet count and the write replaces rather than adds, so a charge mapped
there is discarded rather than misread; refusing both names in configuration is
[#13988](https://github.com/kubernetes-sigs/kueue/issues/13988). The component-wise maximum keeps the
envelope itself bounded, and tests cover multiple `firstAvailable` requests, `Exactly` plus
`firstAvailable` on one resource, and PodSet scaling past the representable range.

The same check is needed wherever these quantities are summed, not only within a Pod. Two PodSets
can each have a representable total for a logical resource while the sum across PodSets does not
fit in an `int64`.

There is currently no way to report any of this. `totalRequestsFromPodSets` returns
`[]PodSetResources` and no error, and `NewInfo` and `Info.Update` do not return errors either. The
implementation will need to add an error path, so that the workload can be marked inadmissible
instead of saturating. Until that exists, the Alpha bound holds for charges the shared path can
represent, which is the condition the existing `Exactly` charges already depend on.

The maximum is taken after DeviceClass-to-logical-resource mapping. Two DeviceClasses may
intentionally map to one logical resource (for example an H100 and an A100 class both mapping to a
`gpu` resource); taking the maximum per DeviceClass and then summing the mapped classes would
overcharge in that case.

The envelope is a bound in resource space, and it does not record which ResourceFlavor an
alternative would have used. When two alternatives map to different logical resources, Kueue
assigns a flavor to each one, and `podset.FromAssignment` copies the node labels of every assigned
flavor into the same PodSet. `podSetAssignments[].flavors` is a map, so when two of those flavors
set the same label key, the value that survives depends on map iteration order. This is not
specific to prioritized lists, since any PodSet assigned two such flavors behaves the same way, but
a prioritized list makes it easy to reach: the alternatives are mutually exclusive, and a user is
likely to expect a fallback rather than the labels of both branches. Labels that do not share a key
are the worse case, not the safe one: they accumulate, so a Pod ends up selecting a node that
satisfies every branch at once, while the request asks for any one of them.

Which alternatives belong to one request is known only while the claim is being read.
`GetResourceRequestsForResourceClaimTemplates` returns one `corev1.ResourceList` per PodSet, so by
the time flavors are assigned there is nothing left to say that two logical resources were
alternatives rather than both needed. A rule phrased over the flavors those resources take
therefore has no place to run.

For Alpha the rule is phrased where the grouping still exists: every alternative of one
`firstAvailable` request has to map to the same logical resource, and a request whose alternatives
map to more than one is permanently inadmissible. The envelope then covers a single resource, which
takes a single flavor, whose labels are the ones every alternative would have wanted. Nothing has to
be said about those labels, and nothing downstream has to know the request was a prioritized list.

This keeps the case the feature is for, several DeviceClasses that an administrator already maps to
one logical resource, and leaves out fallback between different resources. That one needs an
alternative bound to a flavor, which is where the per-flavor attribution of the charge belongs too,
and both are for a later stage.

#### Safety argument

kube-scheduler selects exactly one subrequest from each `firstAvailable`, so for every logical
resource `r` the selected alternative's charge is at most `envelope(q)[r]`. Summing over all
top-level requests, the realized charge cannot exceed the admitted envelope. This holds only if
every alternative resolves to a complete, non-negative charge vector; unmapped, unsupported, or
unknown forms are rejected rather than charged.

The other operand has to be non-negative too. A logical resource is merged with whatever the PodSet
already requests under that name, and the total is floored to zero afterwards, so a negative
ordinary request on that key subtracts from the envelope and leaves a clean zero where a device was
charged. Two ways in. A resource transformation output is a factor supplied by configuration, and a negative
one reaches the merge as a request; that is
[#13985](https://github.com/kubernetes-sigs/kueue/issues/13985). And `validatePodSet` does not reach
`spec.overhead`, which `PodRequests` adds to the charge, so a negative overhead survives even with
`WorkloadValidateResourcesAreNonNegative` on; that is
[#13991](https://github.com/kubernetes-sigs/kueue/issues/13991). Non-negativity
of both operands belongs with exact representability in what has to hold before the merge; treating
`FloorToZero` as the guard hides the cancellation rather than preventing it. This only covers the request forms that exist in
the Kubernetes API version Kueue is compiled against. A classifier written over the Go types cannot
see a field that was added in a later Kubernetes minor release, so bumping the API dependency will
require reviewing any new fields that affect the charge.

The subrequest fields Kueue can see today, and how the classifier treats each one:

| Field | Treatment |
| --- | --- |
| `deviceClassName` | maps to the logical resource; an unmapped class is rejected |
| `allocationMode` | only an effective `ExactCount` is charged |
| `count` | the charge itself |
| `selectors` | compiled, and otherwise not part of the charge |
| `tolerations` | feasibility only, and not part of the charge |
| `capacity` | rejected, for the same reason a capacity-backed mapping is: the count path does not charge a consumable-capacity request |

The API specifies the defaults, so the classifier reads effective values: an omitted
`allocationMode` is `ExactCount`, and an omitted `count` under that mode is one. The same type
states that clients must refuse to handle requests with unknown modes, which is what the rejection
above does. Claim-level `constraints` and `config` do not change the device count, and `pkg/dra`
does not read them.

Kubernetes 1.37 adds `derivedAttributes` to `DeviceSubRequest`. Kueue is compiled against 1.36,
where the type ends at `capacity`, so a 1.36 build talking to a 1.37 apiserver decodes a subrequest
without that field and cannot reject an alternative that uses it. The envelope still charges the
declared count, which is what a count-based mapping produces in any case. Raising the dependency to
1.37 and classifying the field is a prerequisite for this gate leaving Alpha.

The bound rests on two further assumptions. First, the `ResourceClaimSpec` Kueue charges is the one
later used to create the generated `ResourceClaim`. A `ResourceClaimTemplate` spec is immutable in
place, but deleting and recreating one under the same name can change the spec between charging
(while the workload is `Pending`) and claim generation, so the running claim could differ from the
charged envelope. This gap is shared by all Kueue DRA charging, including the existing `Exactly`
path, rather than introduced here. It is a known limitation tracked in
[#13842](https://github.com/kubernetes-sigs/kueue/issues/13842), and the bound above is stated for a
fixed `ResourceClaimSpec` rather than made conditional on that issue landing first. Second, the
bound covers the logical resources the target `ClusterQueue` manages; `quotaCheckStrategy:
IgnoreUndeclared` deliberately omits undeclared dimensions from enforcement. Since every alternative
of a request resolves to the same logical resource, that leaves out the request as a whole rather
than one of its alternatives.

#### Scope

Supported: `ResourceClaimTemplate` references; `ExactCount` subrequests; count-based
`deviceClassMappings` (no `sources`); request-selector CEL compilation.

Rejected: direct `ResourceClaim` references; `All` and unknown allocation modes; unmapped
DeviceClasses; any alternative that sets `capacity` on the subrequest; and any alternative whose
DeviceClass mapping configures a counter or capacity `source`. Source-backed alternatives are excluded because the counter and consumable-capacity
accounting paths process only `Exactly` requests today; they can be added later by charging each
alternative through its source path and taking the component-wise maximum of the resulting vectors.

CEL selectors in every subrequest are compiled and syntax-checked. The `Exactly`
device-cardinality check against ResourceSlices is not reused, because it would require every
alternative to be satisfiable while only one has to be; the initial scope compiles CEL but skips
that cardinality check for `firstAvailable`. Skipping it does not affect quota safety, since the
envelope depends only on the declared counts; it can, however, let an unschedulable workload hold
the envelope reservation. `WaitForPodsReady`, when enabled, eventually releases that reservation.
It is a liveness and utilization safety net, not a premise of the quota upper bound; admission-time
feasibility may reduce such cases in the future.

The compiler that check uses is shared with the `Exactly` path, and Kueue builds its cache with an
empty `dracel.Features`. The apiserver compiles the same selectors with the consumable capacity and
list attribute features available, so a selector it accepted can fail to compile in Kueue. That is
not introduced here, but a classifier that compiles every alternative will hit it more often, so
the two environments should be aligned before this scope is implemented.

#### Coordination with feasibility

This quota accounting is independently safe. Whichever alternative kube-scheduler picks, that
alternative's charge is at most the envelope, so reserving the envelope cannot under-charge the
allocation that follows. The argument never needs to predict the choice, so it does not rest on
ResourceSlice inspection or on any node-level fit check. Two related efforts inform admission
accuracy and are linked here but do not block this KEP: the TAS+DRA work
([#10548](https://github.com/kubernetes-sigs/kueue/issues/10548)), and the broader admission-time
scheduling-feasibility umbrella
([#12422](https://github.com/kubernetes-sigs/kueue/issues/12422)), which passes the full claim spec
to `structured.Allocator`.

Two properties must be distinguished:

- Static support: whether Kueue can correctly account for a request shape. Unsupported shapes are
  permanent errors, rejected as inadmissible. Where one alternative carries a counter or capacity
  source, the whole request is rejected rather than that alternative skipped: the envelope is a
  maximum over every alternative, so dropping one of them stops bounding the allocation the
  scheduler may still choose.
- Dynamic feasibility: whether the current cluster state can schedule a supported claim. A
  supported-but-infeasible claim is retryable rather than a permanent error. Kueue has a
  ResourceSlice controller, but it builds its driver set from the `sources` of each device class
  mapping and filters slice events on that set, so a count-only mapping registers no driver and its
  slice changes trigger no requeue. The initial Alpha also reserves quota without running the
  cardinality or allocator checks, so Kueue never produces a supported-but-infeasible result to
  requeue in the first place; once quota is reserved, kube-scheduler owns dynamic feasibility and
  retries the Pending Pod on ResourceClaim, DeviceClass, ResourceSlice, and node events. A workload
  still waiting on quota is requeued on the next ClusterQueue event, and an admitted one whose Pod
  never becomes schedulable holds its reservation until `WaitForPodsReady`, when enabled,
  evicts it. Extending the controller to count-only mappings, or reaching feasibility through the
  scheduler library, is a graduation criterion rather than something the current controller covers.

The quota path MUST consume the static-support classification from a single helper, and any
admission-time feasibility path MUST consume that same helper once one exists, so a claim is never
accepted by one and rejected by the other. Splitting the two invites the failures that are hardest
to notice: a shape the quota path charges and the feasibility path refuses forever, a shape the
feasibility path admits and the quota path never charges, or a new API field that only one of them
learns to reject. A feasibility result of "infeasible" must not be
surfaced as "unsupported", and a failed object read or slice listing is neither.

#### Relationship with Kubernetes ResourceQuota

This is a Kueue-specific quota policy and does not change Kubernetes `ResourceQuota`. Core
`ResourceQuota` (KEP-4816) takes, within each top-level `firstAvailable`, the largest device count
among the alternatives that name a given DeviceClass, and adds those per-class maxima across
top-level requests; it does not sum the alternatives of one request. Kueue applies the same
per-request maximum, but after mapping DeviceClasses into logical resources, so a mapping that
sends several DeviceClasses to one logical resource collapses charges that core `ResourceQuota`
keeps apart. Namespace `ResourceQuota` and Kueue `ClusterQueue` quota may both apply
to the same workload; this design does not let a user use `firstAvailable` to bypass namespace
`ResourceQuota`.

#### Feature gate lifecycle, version skew, and observability

- Rollback: the gate is Alpha, default off. Disabling it returns a new `firstAvailable` workload to
  the current rejection. A workload that has reserved quota keeps the accounting recorded in its
  status: `Info.rebuildTotalRequests` reads from the admission once `status.admission` is set, which
  a reservation does, rather than recomputing from the PodSets. So a controller restart or downgrade
  does not lose the envelope, and the case of a workload holding a reservation while it waits for an
  AdmissionCheck settles the same way an admitted one does.
- Version skew: the gate requires `KueueDRAIntegration`, and it requires that a cluster has not
  disabled the upstream `DRAPrioritizedList` gate, which has been Beta and on by default since
  Kubernetes 1.34 and GA since 1.36. If the API does not offer `firstAvailable`, no envelope is
  charged and the request is handled as today.
- MultiKueue: `firstAvailable` is out of scope for the initial Alpha. MultiKueue resolves
  ResourceClaimTemplates against cluster-local objects, and the DRA end-to-end test depends on that:
  it gives the manager and both workers a template of the same name with different device counts,
  and asserts the admitted usage on the worker matches the worker's own template rather than the
  manager's. A manager and a worker can therefore select different alternatives, or map the same
  DeviceClass differently, and arrive at different envelopes. Deciding which side is authoritative,
  and covering a template or mapping mismatch, is Beta work. Until then the MultiKueue admission
  check rejects a local workload whose claim templates carry `firstAvailable` before any remote
  Workload or Job is created, so the existing rejected-check path deactivates the workload and
  releases its reservation instead of dispatching an envelope the worker would not reproduce. A
  template that cannot be read yet stays retryable rather than becoming a rejection. The check
  looks up the template by name, so it has the same limitation as the envelope itself. If the
  template is deleted and recreated in between, the check will not see the `firstAvailable` request
  it is meant to reject.
- Observability: the enforced portion of the envelope appears in
  `Workload.status.admission.podSetAssignments[].resourceUsage`; under `IgnoreUndeclared` an omitted
  logical resource appears in neither that usage nor ClusterQueue quota, borrowing, or preemption;
  a debug log records each alternative's charge vector and the resulting envelope, and a rejection
  names the alternative, DeviceClass, or source that is unsupported. A metric or event is evaluated
  before Beta.

#### Tradeoffs

The envelope charges the largest alternative even when a smaller one runs. Alpha confines a request
to alternatives over one logical resource, so this is one dimension rather than every dimension the
request could have reached, but a fallback whose first choice is four devices and whose second is
one still reserves four. This is conservative rather than unsafe, and it affects admission
eligibility, cohort borrowing, preemption, Admission Fair Sharing usage, workload ordering, and
utilization, so it is called out here as an explicit policy tradeoff. Shrinking the reservation to
the realized alternative after allocation is out of scope for this design; it would need a separate
mechanism to observe the generated ResourceClaims and update admitted usage, cache, fair-sharing,
borrowing, and preemption state.

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

DRA workloads are supported with MultiKueue through the existing workload synchronization mechanism. ResourceClaimTemplates must be deployed on worker clusters by users; they are not automatically synced. Count-based `firstAvailable` requests are excluded while `KueueDRAIntegrationPrioritizedList` is Alpha; they remain rejected (fail-closed) rather than dispatched, so a manager and worker cannot charge different envelopes for the same workload for a fixed template identity. The check looks the template up by name, so it
carries the limitation described in the prioritized-list section.

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
- Consumable capacity: Explicit capacity request charged correctly, default charge
  when no capacity request specified, rounding per `ValidValues` policy,
  `ValidRange` with `Step` alignment, `Max`/`ValidValues` exceeded marked inadmissible,
  count multiplication for `count > 1`, negative and overflow clamping
- Capacity validation: Capacity source rejected when CC gate disabled, valid
  multi-dimension capacity sources accepted, counter+capacity mixing rejected,
  capacity source field validation (name, driver, deviceSelector)
- Extended resources with capacity sources rejection: extended resource request
  resolved to a DeviceClass with capacity sources is marked inadmissible
- Capacity device-count skip: device-count charge skipped when capacity sources
  are configured for the DeviceClass (prevents double-counting)
- Prioritized list quota: `firstAvailable` envelope charge with two DeviceClasses mapped to one
  logical resource (max, not sum), alternatives asking different counts of that resource (the
  largest, not the first), multiple top-level `firstAvailable` requests summed, mixed `Exactly` and
  `firstAvailable`, PodSet count greater than one, and a total that exceeds the representable range
  rejected rather than saturated
- Prioritized list rejection: unmapped alternative, `All` and unknown allocation modes, a
  counter-backed or capacity-backed alternative, and the feature gate disabled all marked
  inadmissible
- Prioritized list representability: a per-Pod sum, a merge with an ordinary resource that shares
  the same key, a PodSet multiplication, and a sum across two PodSets that each fit on their own,
  none of which can be represented as `int64`. Each of these marks the workload inadmissible instead
  of clamping. Scaling a PodSet down stays exact, because only an aggregate that is exactly the
  per-Pod value times the count is admitted in the first place
- Prioritized list cancellation: a negative request under a name an alternative charges, whether
  written on the Workload or produced by a resource transformation output, marks the workload
  inadmissible rather than merging to a zero that `FloorToZero` leaves looking deliberate
- Prioritized list with a logical resource named `cpu`: the charge round-trips through the
  milli-unit convention rather than being read as absolute units
- Prioritized list flavors: alternatives mapping to one logical resource are admitted and render
  the selector that resource's flavor implies; alternatives mapping to more than one are rejected
  while the claim is read, rather than reaching flavor assignment as a set of resources nothing can
  tell apart from ones that are all needed
- Prioritized list under MultiKueue: the admission check rejects before any remote Workload or Job
  exists, and a transient template read error is retried instead of rejected
- Prioritized list under `quotaCheckStrategy: IgnoreUndeclared`: a request whose one logical
  resource is undeclared is left out of enforcement and out of reported usage as a whole

#### E2E Test

Use existing dra-example-driver or Kubernetes test driver for e2e testing. For partitionable
devices, integration tests create ResourceSlice objects directly via the API since no test
driver publishes `SharedCounters` yet
([kubernetes-sigs/dra-example-driver#150](https://github.com/kubernetes-sigs/dra-example-driver/pull/150)
tracks adding this). This follows the same pattern as upstream K8s integration tests in
`test/integration/dra/`.

For prioritized lists, an e2e test submits `firstAvailable` workloads where different Pods can
select different alternatives, and verifies each PodSet's admitted envelope stays an upper bound on
the realized allocation.

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

##### KueueDRAIntegrationConsumableCapacity (v0.19)

- support for consumable capacity devices via capacity-based quota (KEP-5075, beta in k8s 1.36)
- rounding per RequestPolicy (ValidValues and ValidRange with Step) to prevent quota gaming
- multiple capacity sources on a single mapping (same dimension, different device
  selectors, summed into one quota resource)
- reuses the existing ResourceSlice controller from partitionable devices; capacity
  source drivers are added to the watched driver set at startup
- integration and e2e tests

##### KueueDRAIntegrationPrioritizedList (v0.20)

- count-based `firstAvailable` quota via the component-wise-max envelope, computed after
  DeviceClass-to-logical-resource mapping
- rejection of counter-backed and capacity-backed alternatives, of an alternative setting
  `capacity` on the subrequest, and of `All`, unknown allocation modes, and unmapped DeviceClasses
- every alternative of one `firstAvailable` request mapping to the same logical resource, with a
  request whose alternatives map to more than one rejected while the claim is read. The envelope
  then covers one resource, which takes one flavor, so nothing downstream has to be told the
  request was a prioritized list
- selectors compiled with the DRA CEL feature environment the supported Kubernetes API uses, so a
  selector the apiserver accepted is not refused here
- a shared static-support classifier that the quota path consumes, with table-driven tests freezing
  the supported-versus-rejected contract (agreement with the feasibility path is a Beta criterion)
- a charge that cannot be represented exactly makes the workload permanently inadmissible instead
  of being saturated, at the envelope and at every shared aggregation boundary it passes through.
  Mixing `Exactly` and `firstAvailable` can overflow a boundary neither reaches alone, so this
  cannot wait for Beta
- the same for an operand that is negative on a key an alternative charges. `FloorToZero` runs
  after the merge, so a negative request under that name subtracts from the envelope and leaves a
  zero that reads as nothing having been asked for
- integration and e2e tests

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
- consolidate ResourceSlice listing between CEL validation and counter processing
  into a shared request-scoped cache, eliminating duplicate API calls within a
  single workload reconciliation.
- extend CEL validation path to use driver-based indexed ResourceSlice listing for
  DeviceClasses with counter sources, instead of listing all ResourceSlices
  unfiltered. When a broader listing is already cached, per-driver requests filter
  from cached results.
- iterate all ConsumesCounters entries per device, consistent with the upstream K8s
  allocator behavior. Takes MAX across all matching counter sets per device.
- support multi-counter tracking by relaxing the DeviceClass uniqueness constraint
  across mappings when both have counter sources with different counter names,
  allowing memory and compute as separate quota resources for the same DeviceClass

##### KueueDRAIntegrationConsumableCapacity

- feature gate enabled by default
- re-evaluate independent dimension tracking (same DeviceClass, different resource names
  across mappings) by relaxing the DeviceClass uniqueness constraint. Same relaxation
  deferred for partitionable devices multi-counter support
- re-evaluate unified quota pools across counter and capacity mappings by relaxing the
  resource name uniqueness constraint
- re-evaluate consolidating ResourceSlice listing between CEL, counter, and capacity
  processing into a shared layer
- re-evaluate caching `deviceSelector` and `RequestPolicy` evaluation results
- re-evaluate surfacing the rounded charge vs raw request in a workload condition or
  event for operator visibility

##### KueueDRAIntegrationPrioritizedList

- feature gate enabled by default
- quota and feasibility paths agree on the supported-versus-rejected predicate, with tests
- alternatives bound to a ResourceFlavor, so a request can fall back between logical resources
  rather than being confined to alternatives an administrator already mapped to one
- upgrade and downgrade behavior verified
- E2E stability for the count-based case
- re-evaluate source-backed alternatives, charging each through its source path and taking the
  component-wise maximum of the resulting vectors

#### GA

##### KueueDRAIntegration

- the feature gate in stable
- TAS + DRA integration and testing
- re-evaluate support for AdminAccess requests
- re-evaluate support for AllocationMode All
- re-evaluate closing the admission-scheduling timing gap via scheduler-library
  integration

##### KueueDRAIntegrationPrioritizedList

- the feature gate in stable, with a lock-to-default or removal plan
- production adoption feedback
- final decision on counter-backed and capacity-backed alternatives, reusing the existing source
  paths rather than a separate implementation
- final decision on post-allocation reconciliation
- version-skew and MultiKueue behavior validated

##### KueueDRAIntegrationExtendedResource

- the feature gate in stable
- user adoption feedback confirms stability
- re-evaluate DeviceClass watcher performance at scale

##### KueueDRAIntegrationPartitionableDevices

- the feature gate in stable
- user adoption feedback with MIG workloads confirms counter-based quota accuracy
- re-evaluate MAX-based counter charging for heterogeneous device profiles. Accurate
  charging requires knowing which device the scheduler will allocate. Depends on
  scheduler-library integration ([#12422](https://github.com/kubernetes-sigs/kueue/issues/12422)).
- re-evaluate pool-aware flavor assignment for counter resources. Connecting
  ResourceSlice pools to flavors requires scheduler-library awareness of which
  node a workload will land on. Depends on scheduler-library integration
  ([#12422](https://github.com/kubernetes-sigs/kueue/issues/12422)).

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
- Promoted KueueDRAIntegrationExtendedResource to Beta: July 2026 by @PannagaRao
- Consumable capacity design: July 2026 by @sohankunkerkar — added KEP-5075 integration
  for software-level device sharing
- Promoted KueueDRAIntegrationPartitionableDevices to Beta: July 2026 by @PannagaRao
- Prioritized-list (`firstAvailable`) quota design: July 2026 by @thc1006
  (see [#13599](https://github.com/kubernetes-sigs/kueue/issues/13599))

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

### KEP-5941 Shared Consumable Capacity

[KEP-5941](https://github.com/kubernetes/enhancements/issues/5941) (alpha in K8s 1.37)
adds request-driven consumption against shared counter sets via a `valueFrom` mapping on
`consumesCounters`. This is different from KEP-5075: KEP-5075 tracks capacity on the
device itself via `Device.Capacity`, while KEP-5941 tracks request-driven amounts against
parent-scoped `SharedCounters`. When KEP-5941 ships, Kueue can extend the existing
counter source to understand `valueFrom` mappings since the charge still flows through
`consumesCounters`. The capacity source designed in this KEP is not affected.

### KEP-5963 Device Compatibility Groups

[KEP-5963](https://github.com/kubernetes/enhancements/issues/5963) (alpha in K8s 1.37)
adds `compatibilityGroups` on `consumesCounters` entries to express mutual exclusion
between partitioning schemes on the same counter set. This is a partitionable devices
concern: it lives on `DeviceCounterConsumption` and only applies to devices sharing a
counter set. Consumable capacity devices that use `Device.Capacity` without
`consumesCounters` are not affected. Kueue would handle compatibility groups as part
of the counter source path. Counter-backed alternatives are rejected in this Alpha, so
compatibility groups do not interact with prioritized-list quota.
