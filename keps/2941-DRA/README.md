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
  - [DRA Device Feasibility](#dra-device-feasibility)
    - [What the check does](#what-the-check-does)
    - [Extended resources](#extended-resources-1)
    - [Cost](#cost)
    - [The allocator](#the-allocator)
    - [What the check does not decide](#what-the-check-does-not-decide)
    - [Validation](#validation-2)
  - [Architecture Details](#architecture-details)
    - [Queue Manager Extensions](#queue-manager-extensions)
  - [Prioritized List Quota](#prioritized-list-quota)
    - [Accounting rule](#accounting-rule)
    - [Why this is an upper bound](#why-this-is-an-upper-bound)
    - [Alpha support matrix](#alpha-support-matrix)
    - [Clearing a rejection](#clearing-a-rejection)
    - [Exactness and composition](#exactness-and-composition)
    - [Integration requirements](#integration-requirements)
    - [Feature gate, version skew and MultiKueue](#feature-gate-version-skew-and-multikueue)
    - [Relationship with Kubernetes ResourceQuota](#relationship-with-kubernetes-resourcequota)
    - [Limitations and tradeoffs](#limitations-and-tradeoffs)
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
      - [KueueDRADeviceFeasibility (v0.20)](#kueuedradevicefeasibility-v020)
      - [KueueDRAIntegrationPrioritizedList (v0.20)](#kueuedraintegrationprioritizedlist-v020)
    - [Beta](#beta)
      - [KueueDRAIntegration (v0.18)](#kueuedraintegration-v018)
      - [KueueDRAIntegrationExtendedResource](#kueuedraintegrationextendedresource)
      - [KueueDRAIntegrationPartitionableDevices](#kueuedraintegrationpartitionabledevices)
      - [KueueDRAIntegrationConsumableCapacity](#kueuedraintegrationconsumablecapacity)
      - [KueueDRADeviceFeasibility](#kueuedradevicefeasibility)
      - [KueueDRAIntegrationPrioritizedList](#kueuedraintegrationprioritizedlist)
    - [GA](#ga)
      - [KueueDRAIntegration](#kueuedraintegration)
      - [KueueDRAIntegrationPrioritizedList](#kueuedraintegrationprioritizedlist-1)
      - [KueueDRAIntegrationExtendedResource](#kueuedraintegrationextendedresource-1)
      - [KueueDRAIntegrationPartitionableDevices](#kueuedraintegrationpartitionabledevices-1)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
  - [Adding the dynamicresources Plugin to the Simulated Filters](#adding-the-dynamicresources-plugin-to-the-simulated-filters)
  - [Charging a prioritized list other than by its envelope](#charging-a-prioritized-list-other-than-by-its-envelope)
  - [Refusing a request whose mapped resource an excluded prefix covers](#refusing-a-request-whose-mapped-resource-an-excluded-prefix-covers)
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
- With `KueueDRADeviceFeasibility` and its dependencies enabled, Kueue does not reserve
  quota for a Workload whose devices no single node can supply, whether the Pod names a
  ResourceClaimTemplate or requests a DRA-backed extended resource.
- Admins can enforce quota for prioritized-list (`firstAvailable`) requests over count-based
  DeviceClass mappings, charging the component-wise maximum over the alternatives.

### Non-Goals

- Counter-backed and capacity-backed `firstAvailable` (`DRAPrioritizedList`) alternatives are
  out of scope for the initial Alpha.
- The queue and status mechanisms that record a rejection and retire a stale queue entry belong
  to `KueueDRAIntegration`. This KEP states the properties prioritized-list quota needs from them
  and does not design them.
- Quota accounting for DRADeviceTaints is not included: a tainted device is charged like
  any other. Taints written into a ResourceSlice are honored by the per-node feasibility
  check instead; taints applied by a `DeviceTaintRule` are not, as
  [DRA Device Feasibility](#dra-device-feasibility) records.
- Multi-host partitionable devices (e.g., NVLink fabrics spanning multiple nodes) are not
  supported.
- Kubernetes DRA features that change what kube-scheduler computes outside the allocator
  are not modeled. [DRA Device Feasibility](#dra-device-feasibility) lists which, and why.
- Quota accounting stays independent of Topology Aware Scheduling: the two are computed
  separately and neither reads the other's result. The only place they meet is the per-node
  device feasibility check in [DRA Device Feasibility](#dra-device-feasibility), which runs
  inside the TAS assignment.

## Proposal

This proposal extends Kueue to support workloads using DRA APIs for quota management, borrowing and preemptable
scheduling. This includes:

1. Extending the existing Kueue Configuration API with `DeviceClassMappings` to map device classes to logical resource
   names
2. Supporting workloads that use ResourceClaimTemplates (ResourceClaims are not supported in alpha)
3. Supporting workloads that use extended resource requests backed by DRA DeviceClasses,
   named either by the class's `extendedResourceName` or by the implicit name every class
   carries (requires the Kubernetes `DRAExtendedResource` feature gate, stable in k8s 1.37)
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
- Device class uniqueness is enforced. Each device class can only map to one resource name to prevent quota ambiguity. Counter-based mappings relax this when counter names differ.
- Configuration-based approach - device class mappings are configured through the Kueue Configuration API
- Quota accounting is independent of Kueue's Topology Aware Scheduling feature. The only
  place the two meet is the per-node device check in
  [DRA Device Feasibility](#dra-device-feasibility).
- DRA resource preprocessing is not scoped by ResourceFlavor node constraints. Counter
  charges and device matching are computed globally before flavor assignment.
- AdminAccess requests are skipped in quota counting (zero charge) since they provide
  shared read-only access to already-allocated devices. Count-based `firstAvailable`
  (`DRAPrioritizedList`) quota is supported behind `KueueDRAIntegrationPrioritizedList`.
  DRADeviceTaints is not supported.
- **Single-node partitionable devices (e.g., MIG) are supported** via counter-based
  quota. See [Partitionable Devices](#partitionable-devices). Multi-host partitionable
  devices are not supported.
- **Extended Resources** covers a DeviceClass named by its `spec.extendedResourceName` or
  by the implicit name every class carries. This depends on the Kubernetes
  `DRAExtendedResource` feature gate (stable in k8s 1.37).
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
  to prevent quota leaks. Count-based `firstAvailable` requests compile their selectors but skip this
  device check, since only one alternative has to be satisfiable. This validation uses the upstream DRA CEL compiler from [`k8s.io/dynamic-resource-allocation/cel`](https://github.com/kubernetes/dynamic-resource-allocation/tree/master/cel).
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

With `DRAPrioritizedList` (stable in K8s 1.36), there is a risk that effective tallying of
resources will not be available until after allocation. The mitigation approach is documented here:
1. For `DRAPrioritizedList`: count-based alternatives are charged the component-wise maximum over
   the alternatives after DeviceClass-to-logical-resource mapping, an upper bound on any single
   realized allocation. Counter-backed and capacity-backed alternatives are rejected.
2. AdminAccess requests are skipped in quota counting. This feature can only be enabled in
   admin namespaces (gated by the `resource.kubernetes.io/admin-access` label), and provides
   shared read-only access to already-allocated devices. Charging quota would double-count the
   device. This matches the Kubernetes scheduler which excludes AdminAccess from `allocatedDevices`.
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
     Since the extended resources path resolves the quota key from the selected DeviceClass,
     the mapped logical name when that class is in `deviceClassMappings` and the
     `extendedResourceName` otherwise, a class switch drifts the quota key itself whenever the
     two classes map to different logical resources, and not only the physical device behind a
     stable key.
   To mitigate:
   - Kueue uses a controller-runtime field indexer on `DeviceClass` by `spec.extendedResourceName`
     to resolve DeviceClasses deterministically.
   - Per KEP-5004, admins should ensure one `extendedResourceName` maps to at most one
     DeviceClass.
   - On a DeviceClass change, only pending Workloads (`QuotaReserved=False`) are requeued.
     Workloads that have already reserved quota keep their admission-time charge and are
     not revisited, because dropping an existing reservation to re-admit would need the
     DeviceClass the scheduler actually allocated from, which the workload controller
     does not watch today.
   - TAS + DRA is the longer-term path to closing this admission-scheduling gap.
     [DRA Device Feasibility](#dra-device-feasibility) closes the part where no node can
     satisfy the claims at all; a device that disappears between admission and scheduling
     is still not covered.

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
- `KueueDRAIntegrationExtendedResource` (Alpha): gates extended resources support, including
  DeviceClass auto-discovery via `extendedResourceName`. Requires `KueueDRAIntegration`.
- `KueueDRAIntegrationPartitionableDevices` (Alpha): gates counter-based quota for
  partitionable devices. Enables the `counter` source type on `deviceClassMappings` entries.
  Requires `KueueDRAIntegration`. Also requires the Kubernetes `DRAPartitionableDevices`
  feature gate (beta in K8s 1.36).
- `KueueDRAIntegrationConsumableCapacity` (Alpha): gates capacity-based quota for devices
  that allow multiple allocations. Enables the `capacity` source type on
  `deviceClassMappings` entries. Requires `KueueDRAIntegration`. Also requires the
  Kubernetes `DRAConsumableCapacity` feature gate (beta in K8s 1.36).
- `KueueDRAIntegrationPrioritizedList` (Alpha, default off): gates count-based quota accounting
  for `firstAvailable` (prioritized alternative) requests via the component-wise-max envelope.
  Requires `KueueDRAIntegration`, and a cluster that has not disabled the upstream
  `DRAPrioritizedList` gate (on by default since Kubernetes 1.34, GA since 1.36).
  Counter-backed and capacity-backed alternatives are rejected.
- `KueueDRARejectWorkloadsWhenDRADisabled` (Beta, default on since v0.18): rejects workloads
  that use DRA resources (ResourceClaimTemplates or ResourceClaims) when `KueueDRAIntegration`
  is disabled. Without this gate, DRA workloads submitted while `KueueDRAIntegration` is off
  are silently admitted with zero device resource usage, bypassing quota enforcement entirely.
  See [Workload Rejection When DRA Is Disabled](#workload-rejection-when-dra-is-disabled).
- `KueueDRADeviceFeasibility` (Alpha): gates per-node device availability checking before
  admission, so a Workload using ResourceClaimTemplates is not admitted when no node can
  satisfy its claims. Requires `KueueDRAIntegration`, `TopologyAwareScheduling` and
  `TASNodeFeasibilityForAllLevels`.
  See [DRA Device Feasibility](#dra-device-feasibility).

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
remains deferred to beta, while counter-based mappings already relax this constraint
for distinct counter names.

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
kube-scheduler automatically creates ResourceClaims for a DeviceClass addressed by its
`spec.extendedResourceName` or by the implicit name every class carries.
This requires the Kubernetes `DRAExtendedResource` feature gate (stable in k8s 1.37).

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

1. Kueue detects extended resources in `resources.requests` and computes each
   original resource name's Pod-level request (max across init containers, sum
   across regular containers, then max of the two) before any DeviceClass lookup
   or quota-key mapping. Two different resource names later mapped to the same
   quota key are therefore aggregated independently first, so neither collapses
   into the other's contribution.
2. Looks up DeviceClasses by `extendedResourceName` by field indexer
3. If no matching DeviceClass is found, the resource is not DRA-backed and Kueue
   processes it through the standard resource quota path (counted via `node.Status.Allocatable`)
4. If one or more DeviceClasses match, picks the one the scheduler would use: the latest
   creation time wins, and the name breaks ties when they were created in the same second.
   Uses that class's `deviceClassMappings` entry as the quota key, or falls back to
   `extendedResourceName` when the selected class is not mapped.
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
Kueue still treats the resource as DRA-backed, and the selected class determines the charge.
Kueue picks the class the scheduler would use and reads the quota key from it, so the charge
follows the allocation rather than whichever class the lookup happened to return first.

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
domain. See [Processing Flow](#processing-flow-1) for details. A `firstAvailable` alternative
whose DeviceClass mapping configures a counter source is rejected, since the counter path reads
`Exactly` requests only.

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

Only `ExactDeviceRequest` with `count` is supported. A `firstAvailable` alternative whose
DeviceClass mapping configures a capacity source is rejected, consistent with the existing exclusion
for partitionable devices; a subrequest `capacity` requirement under a source-less mapping is
charged by device count.

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
be evaluated for [Beta](#beta). For counter-based mappings, the same DeviceClass can appear in different mappings when the counter names differ. For device-count and capacity-based mappings, DeviceClass uniqueness remains enforced. 

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

### DRA Device Feasibility

This section is gated behind the `KueueDRADeviceFeasibility` Kueue feature gate.

Quota limits how many devices a ClusterQueue admits, not where those devices are. Without
a per-node check, a Workload whose ResourceClaims no single node can satisfy is admitted
on quota alone. Kueue then removes the scheduling gate, kube-scheduler finds no node that
can allocate the claims, and the Pods remain Pending while the Workload holds quota.

#### What the check does

Before admission, Kueue tries to allocate the PodSet's claims on each candidate node and
drops the nodes where that allocation fails. When no node is left, the Workload stays
pending and its condition message counts the nodes dropped for devices as `draNoFit`,
separately from a generic no-fit.

#### Extended resources

A Pod that requests a DRA-backed extended resource names no claim of its own, because
kube-scheduler creates one for it only after Kueue has admitted. The check derives the
same claim from the DeviceClass that declares the extended resource, so both paths are
filtered alike. It asks for the whole Pod's devices in one request per DeviceClass, which
is enough to decide whether a node fits because the classes it derives carry no selectors.
That count can exceed what kube-scheduler ultimately requests, since it lets an init
container reuse a later container's devices, so the check stays restrictive rather than
over-admitting.

A DeviceClass that declares an extended resource is what supplies it, so no Node
advertises it and topology stops counting it against a domain's capacity, leaving it to
the device check. kube-scheduler leaves the same resources to its own DRA filter. With the
check disabled the resource keeps being counted, which is why such a Workload finds no
domain unless a device plugin advertises the resource.

#### Cost

The check costs one allocation attempt per candidate node, so it scales with the number
of nodes that survive the other filters and with the devices each advertises. Two things
bound that. A Pod that neither names a claim nor requests a DRA-backed extended resource
is answered without consulting the allocator, so Workloads that do not use devices pay
nothing. And the result is reused for the rest of the scheduling cycle, so it is not
repeated for each preemption the cycle evaluates.

#### The allocator

The check allocates with `structured.Allocator` from
`k8s.io/dynamic-resource-allocation`, which is what kube-scheduler's `dynamicresources`
plugin builds as well, so the same claims against the same ResourceSlices produce the same
answer on both sides.

That allocator is configured by the Kubernetes DRA feature gates, not the Kueue ones: the
Kueue gates decide what quota charges, while the Kubernetes gates decide which devices the
allocator may pick, and which of its three implementations (`stable`, `incubating`,
`experimental`) it selects. Kueue and kube-scheduler therefore have to run with the same
values, and Kueue does not compare them.

#### What the check does not decide

The check asks whether a node can serve one Pod of the PodSet, not how many. A node that
can satisfy one Pod's claims is kept even when the PodSet asks for more Pods than it has
devices for, so the extra Pods can still be left Pending. Counting devices per node is a
Beta criterion below.

Two cases delay the check rather than skip it, both because topology itself is delayed.
A Workload in a ClusterQueue with a MultiKueue admission check has its topology assigned
on the worker cluster, so the check is the worker's to run and the manager reserves quota
without it. A Workload waiting on a ProvisioningRequest has topology delayed on the first
scheduling pass only; the second pass, once quota is reserved, runs the check as usual.

- reads the Kubernetes DRA gates from Kueue's own process, not from the API server, and
  does not check that the two agree or enforce a minimum Kubernetes version
- device state read once per scheduling cycle, not cached across cycles
- devices held by a preempted Workload are not released, so preemption cannot make a
  Workload device-feasible; the check stays restrictive rather than over-admitting
- feasibility filters nodes but does not bound how many device-consuming Pods a domain
  receives: a PodSet needing more devices than a node has can still be placed there, and
  the surplus Pods stay Pending. Unlike the other gaps here this one over-admits rather
  than staying restrictive. Counts are Beta work
- the per-node allocation attempt is not bounded. kube-scheduler gives its own attempt a
  deadline and treats a timeout as retryable; here a slow DeviceClass selector stretches
  the scheduling cycle instead

These Kubernetes DRA features change what kube-scheduler does without changing what Kueue
predicts, so a cluster running one of them gets a different answer than this check gives:

| Feature | Effect | What it waits on |
|---|---|---|
| `DRADeviceTaintRules` | admits onto a node whose devices a rule has tainted | reading the rules with the slices, [#15621](https://github.com/kubernetes-sigs/kueue/issues/15621) |
| `DRAFractionalCapacityRange` | charges whole units for a fractional policy | charging in the policy's own units |
| `DRAOptionalNodeOperations` | never admits: the device is rejected on every node | carrying the node's declared features |
| `DRAPrioritizedList` | refuses a request that offers alternatives | quota for alternatives, [#13601](https://github.com/kubernetes-sigs/kueue/pull/13601) |
| `DRAWorkloadResourceClaims` | charges a shared claim once per Pod, and spreads a group its allocation pins to one node | a claim shared by a group; upstream owns placement |
| `DRANodeAllocatableResources` | a domain looks emptier than it is, so it takes more Pods than fit | knowing the device, which Kueue admits without |
| `DRADeviceBindingConditions` | admits, then waits for the devices to become ready | knowing the device, as above |

#### Validation

- `KueueDRADeviceFeasibility` requires `KueueDRAIntegration`, `TopologyAwareScheduling`
  and `TASNodeFeasibilityForAllLevels`. Enabling it without them is rejected while the
  feature gates are parsed, before the manager starts. It does not require
  `SchedulerLibraryIntegration`: the check wraps whichever simulator is in use, so a
  cluster not running the scheduler library gets it too.
- A claim reaches the check from a ResourceClaimTemplate or from a DRA-backed extended
  resource request. A Workload that references a ResourceClaim directly is marked
  inadmissible by the workload controller before scheduling, which is what depending on
  `KueueDRAIntegration` guarantees.
- With the gate disabled, no per-node device check runs and a Workload using
  ResourceClaimTemplates is admitted on quota alone.
- The extended resource path needs `KueueDRAIntegrationExtendedResource`. Without it no
  extended resource is treated as DRA-backed, so a Workload requesting one is left to the
  node filters, which is also what happens for a resource a device plugin advertises.

The three required gates are not alike:

| Gate | Why it is required |
|---|---|
| `KueueDRAIntegration` | marks a Workload referencing a ResourceClaim directly inadmissible, which the check relies on rather than repeating |
| `TopologyAwareScheduling` | the check runs while a topology domain is chosen, so without it nothing asks |
| `TASNodeFeasibilityForAllLevels` | makes every leaf a single node; without it a leaf can span several, leaving no node to ask about, and the check is skipped silently |

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
default off). It adds quota accounting for prioritized-list requests, the `firstAvailable` field of
a `DeviceRequest` (the Kubernetes `DRAPrioritizedList` feature, GA in 1.36). A `firstAvailable`
request lists up to eight ordered alternatives (`FirstAvailableDeviceRequestMaxSize`);
kube-scheduler selects exactly one at allocation time, which Kueue does not know when it reserves
quota. With the gate off, such requests stay rejected as they are today. It adds no
configuration field and no API field; the feature gate is the only new surface, and every input
it reads, `deviceClassMappings`, `excludeResourcePrefixes` and the claim template itself, already
exists.

The gate decides one thing: whether a `firstAvailable` request may enter accounting or is refused
as unsupported. Toggling it changes nothing observable for a Workload with no `firstAvailable`
request. The queue and status mechanisms that record a rejection and retire a stale queue entry
are `KueueDRAIntegration` properties, listed as parent prerequisites with the
[Alpha criteria](#kueuedraintegrationprioritizedlist-v020) and designed with the parent path.

#### Accounting rule

For each top-level `firstAvailable` request, Kueue resolves every alternative's DeviceClass to its
logical quota resource through `deviceClassMappings`, then charges, per resource, the largest
amount any single alternative would need; that charge is the request's envelope.

The per-Pod DRA charge is the existing `Exactly` charges plus the sum of the envelopes of the
`firstAvailable` requests. PodSet scaling is unchanged: the per-Pod charge is multiplied by the
effective PodSet count, and with `ElasticJobsViaWorkloadSlices` each slice carries its own count
and computes its envelope independently.

| Alternative | Request | DeviceClass | Maps to | Charge |
|---|---|---|---|---|
| 1st choice | 1 device | `a100.example.com` | `example.com/gpu` | 1 |
| fallback | 2 devices | `t4.example.com` | `example.com/gpu` | 2 |
| **envelope** | | | | **2** |

A second, independent request for one device of the same resource brings the per-Pod charge to 3,
before PodSet scaling. If kube-scheduler picks the A100, the Workload consumes 1 and holds 2.

Effective values, and what does not affect the charge:

- An omitted `allocationMode` is `ExactCount`, and an omitted `count` under it is one.
- `DeviceSubRequest` has no `adminAccess`, so the zero-charge rule stays confined to `Exactly`.
- A subrequest `capacity` requirement does not change how many devices an alternative asks for.
- An omitted `capacity` is not the absence of capacity consumption, so under a source-less mapping
  it is charged by device count, as the `Exactly` path already charges it.

#### Why this is an upper bound

kube-scheduler selects exactly one alternative per request, so for every logical resource the
selected alternative's charge is at most the envelope, and summed over requests the realized
charge cannot exceed the admitted one.

This holds when:

- every alternative resolves to a complete, non-negative per-resource charge, which is why
  unmapped, unsupported and unknown forms are refused rather than charged;
- every other contribution to a charged resource arrives at the merge exactly once, non-negative,
  and as the value its own source defines.

The classifier sees only the request forms in the Kubernetes API version Kueue is compiled
against, so bumping that dependency means reviewing any new field that affects the charge.

#### Alpha support matrix

| Request | Alpha |
| --- | --- |
| `ResourceClaimTemplate` reference | supported |
| `ExactCount` alternatives, including an omitted `allocationMode` or `count` | supported, charged by count |
| alternatives under count-based mappings (no `sources`) that all resolve to one logical resource | supported |
| subrequest `capacity` under a source-less mapping | supported, charged by device count |
| subrequest `selectors` and `tolerations` | selectors compiled; neither is part of the charge; no device cardinality check |
| direct `ResourceClaim` reference | rejected |
| an alternative with allocation mode `All` | rejected |
| unknown allocation mode, or a malformed union | rejected |
| an alternative with an unmapped DeviceClass | rejected |
| an alternative whose mapping configures a `counter` or `capacity` source | rejected |
| alternatives resolving to more than one logical resource | rejected |
| a source-backed `Exactly` request in the same Workload, unless `adminAccess` | rejected |
| a non-DRA contribution on a resource an envelope is charged on | rejected |

Every row has the same outcome, an inadmissible Workload, recorded as
[Clearing a rejection](#clearing-a-rejection) describes; the rows differ only in what the message
names.

Why each refusal exists:

- **Any unsupported alternative refuses the whole request.** The envelope is a maximum, so dropping
  one alternative stops it bounding what kube-scheduler may still choose.
- **Source-backed alternatives**: the counter and capacity paths process only `Exactly` today. They
  can be added later by charging each alternative through its source path and taking the
  component-wise maximum.
- **More than one logical resource** would charge every dimension while kube-scheduler consumes
  one, so a fallback would make admission strictly harder than no fallback. Beta must solve this
  before the limit is lifted. The limit is on the mapping rather than on the request shape: the
  same Workload becomes supported once the administrator maps those DeviceClasses to one resource.
- **A source-backed `Exactly` request in the same Workload**: with the ResourceSlice API
  unavailable, a counter or capacity source contributes zero today rather than failing closed, and
  this gate stays clear of that shared defect rather than admitting a Workload whose other charges
  may fall short. Beta lifts the limit once such a source fails closed.
- **A non-DRA contribution on a charged resource** is refused rather than merged. An `Exactly`
  charge is not one, since it is charged against the same total, and neither is a DRA-backed
  extended resource, which is replaced before the merge reads it. Lifting this limit takes an
  amendment here or Beta criteria naming the combinations that become supported; prerequisite
  work merging does not lift it on its own.

The shape Alpha covers is a fallback within one budget. The plainest case needs no mapping change
at all: one DeviceClass with two alternatives that differ only in their selectors, an 80 GiB card
or else any card of that class. The other is two generations of one accelerator, an H100 class or
else an A100 class, mapped to one logical resource because the quota is counted per accelerator.
A fallback across budgets, a GPU or else a TPU, is what the restriction refuses, and the hazard
above is what it protects.

A missing DeviceClass is not a rejection on this path. The envelope reads `deviceClassName`, the
mapping and the declared count, never the DeviceClass object, so the class's existence and its
selectors are left to kube-scheduler along with feasibility.

#### Clearing a rejection

What re-evaluates each rejection is the event that changes its cause, and an implementation that
requeues on Workload updates alone waits for the wrong event:

| Rejection | Cleared by |
| --- | --- |
| a direct `ResourceClaim` reference | a PodSet template change |
| `All` in an alternative, or an allocation mode this build does not know | a template change; `ResourceClaimTemplate.spec` is immutable, so a template repair is a delete and a recreate; an unknown mode also clears once Kueue is built against the API that defines it |
| a malformed union | the apiserver rejects it at creation; Kueue fails closed if one arrives, and a template or PodSet change clears it |
| the configuration around it: an unmapped DeviceClass, a `counter` or `capacity` source on an alternative's mapping, alternatives over more than one logical resource | a request or template change, or a manager restart with the changed mappings |
| a container, init-container or Pod-level request on a charged resource, including a limit read as a missing request, or `spec.overhead` on the Pod template | a PodSet template update |
| a `LimitRange` default on a charged resource | that `LimitRange` created, updated or deleted, or a template update that stops it applying |
| RuntimeClass overhead on a charged resource | that RuntimeClass created, updated or deleted, or a `runtimeClassName` change |
| a resource transformation output on a charged resource | a manager running with the changed configuration, or a change to the input it reads |
| a source-backed `Exactly` request in the same Workload | the claim definition changing, or Kueue starting with a mapping that makes it count-based |
| a template the request names that does not exist | that template being created, through a watch keyed by namespace and name, so a same-name template in another namespace wakes nothing |
| a transient read or API error | not a verdict; retried |

Every event in the second column is already delivered by a watch the workload controller has,
except the template one, which needs an index from a Workload to the templates it references and a
watch on their lifecycle.

A rejection is recorded the way the `Exactly` path records one today: `QuotaReserved=False` with
reason `Misconfigured`, or `Inadmissible` where `UnadmittedWorkloadsObservability` is disabled, and
`Requeued=False` with reason `Inadmissible`, so the reason does not distinguish the rows. The
message does: it carries every refused field's path and detail, from the PodSet and
`resourceClaims` index down to the alternative, and each row above maps to one detail string. A
per-row reason would be a change to the parent path's condition schema and is not made here.

The static shapes are refused where the `Exactly` path refuses them today, at admission, rather
than by the Workload webhook. Two of them live in the template: `All` is valid upstream, and an
unknown allocation mode is one a newer apiserver may accept while the API asks clients to refuse
it. The webhook does not read the template, and a template can be deleted and recreated under
the same name after the Workload has passed the webhook, so a check there would be neither
complete nor final. A malformed union does not reach Kueue from a validated apiserver, and the
classifier's refusal is a fail-closed default for an object that bypassed validation. A direct
`ResourceClaim` reference is visible in the Workload, so the webhook could refuse it; the parent
path parks such a Workload today, and refusing it earlier is a change both request kinds would
take together, which Beta re-evaluates. A Workload is also created by the job reconciler rather
than by the user, so a webhook denial surfaces as a create error the reconciler retries, while
an inadmissible Workload carries the reason in a condition.

#### Exactness and composition

The envelopes of a claim are summed in `resources.Amount`, which holds an integer exactly at any
magnitude, and the sum becomes a whole-unit `resource.Quantity` the way the `Exactly` count does,
so a logical resource named `cpu` is charged one core per device on both paths. From there a DRA
charge is treated like any other request: the per-Workload request arithmetic saturates at
`math.MaxInt64`, and a charge of that magnitude is admissible only against a quota of the same
magnitude, since the scheduler compares amounts exactly. That saturation is shared behaviour
([#14371](https://github.com/kubernetes-sigs/kueue/issues/14371)); this gate neither depends on it
nor changes it.

Each operand is checked non-negative where the merge happens. A negative request on a charged
resource would subtract from the envelope, and `FloorToZero` afterwards would hide the cancellation
rather than prevent it. The `WorkloadValidateResourcesAreNonNegative` gate is not relied on for
this, since an administrator can turn it off.

Preprocessing carries the set of logical resource names the envelopes reached alongside the merged
charge, and both survive the same requeue. The workload request builder reads the same effective
resources the DRA pass read, before extended-resource replacement runs, refuses a Workload carrying
a non-DRA contribution on any of those names anywhere in the Workload, and checks the merged value
of what remains. The `Exactly` path merges such a contribution today and keeps doing so until a
`firstAvailable` request reaches the name, an asymmetry that is deliberate: it keeps this gate
clear of the shared accounting defects rather than making it their fix. Summing by resource name
before flavors are assigned is stricter than the per-flavor accounting that follows, which is
intended.

How existing mechanisms interact with an envelope-touched resource:

| Mechanism | Effect |
|---|---|
| `excludeResourcePrefixes` | Applies to the Pod's own requests, before transformations. A logical resource an explicit `deviceClassMappings` entry synthesizes stays chargeable, as on the `Exactly` path. |
| `transformations` | Run over the Pod's requests before the merge, so a logical resource named as an input or multiplier matches nothing, while outputs aimed at one reach it. |
| `quotaCheckStrategy: IgnoreUndeclared` ([KEP-7513](../7513-quota-check-strategy/README.md)) | Filters the resource on the same terms as an `Exactly` charge or an ordinary request on that name, per request rather than per alternative. An administrator who wants DRA quota enforced declares the mapped resource or keeps `BlockUndeclared`. |
| A non-DRA contribution on a charged resource: a container, init-container or Pod-level request, a `LimitRange` default, RuntimeClass overhead or a transformation output | The Workload is refused rather than merged, for any of those names anywhere in the Workload. An `Exactly` charge on the name is not such a contribution, and neither is a DRA-backed extended resource. |

Whether an overlap between a prefix and a mapping should be refused at configuration load is a
question for every mapping and is left to its own issue.

#### Integration requirements

The quota path, the MultiKueue admission check and any admission-time feasibility path consume one
static-support classifier, in two stages. The first reads the API shape alone, with no gate, no
mapping and no cluster state; it names every form of the `PodResourceClaim` and `DeviceRequest`
unions, refuses the malformed ones, and fails closed on an unset kind. The second combines that
kind with the gate and the mapping to reach a disposition. An infeasible result is never surfaced
as unsupported, and a failed read is neither.

Selectors in every alternative are compiled with the DRA CEL compiler and syntax-checked, through
the DRA path's shared compile cache, which is keyed on the expression text and holds 256 entries,
so a reconcile compiles only the expressions it has not seen. The worst case for one request is
`FirstAvailableDeviceRequestMaxSize` times `DeviceSelectorsMaxSize`, 8 × 32 = 256 distinct
expressions against 32 for an `Exactly` request, which is the size of that cache; a request of
that shape evicts every other entry, and the cost is stated here rather than measured. The
`Exactly` device-cardinality check is not reused, since it would require every alternative to be
satisfiable while only one has to be. Skipping it does not affect quota safety, only whether an
unschedulable Workload can hold the envelope reservation, which `WaitForPodsReady`, when enabled,
eventually releases. A selector stored in the supported API version must compile in a DRA CEL
environment compatible with the apiserver's, which is a shared DRA prerequisite
([#14372](https://github.com/kubernetes-sigs/kueue/issues/14372)).

A count-based mapping registers no driver with the ResourceSlice controller, so a Workload waiting
on quota is re-evaluated on the next ClusterQueue event, and once quota is reserved kube-scheduler
owns feasibility. The TAS+DRA work
([#10548](https://github.com/kubernetes-sigs/kueue/issues/10548)) and the admission-time
feasibility umbrella ([#12422](https://github.com/kubernetes-sigs/kueue/issues/12422)) inform
accuracy and do not block this design.

The parent DRA fixes and their regression coverage are tracked with the implementation in
[#14130](https://github.com/kubernetes-sigs/kueue/pull/14130). This amendment does not select the
queue, status or builder mechanisms that provide these guarantees; the properties it depends on
are listed with the Alpha criteria.

#### Feature gate, version skew and MultiKueue

- `KueueDRAIntegrationPrioritizedList` requires `KueueDRAIntegration`; a configuration enabling it
  without the parent is refused by configuration validation. It also requires that the cluster has
  not disabled the upstream `DRAPrioritizedList` gate, on by default since Kubernetes 1.34 and GA
  in 1.36. If the API does not offer `firstAvailable`, no envelope is charged.
- Disabling the gate returns a new `firstAvailable` Workload to the current rejection. A Workload
  that has reserved quota keeps the accounting recorded in its status, since an admitted Workload
  is rebuilt from `status.admission` rather than recomputed, so a restart does not lose the
  envelope. A binary downgrade to a version without this feature is not covered and wants such
  Workloads released first; validating one is a Beta criterion.
- MultiKueue is out of scope for the initial Alpha. A manager and a worker resolve
  ResourceClaimTemplates against their own objects and could arrive at different envelopes, so the
  MultiKueue admission check refuses a local Workload whose static classification finds a
  `firstAvailable` request, before any remote Workload or Job is created and whether the gate is on
  or off; a template that cannot be read yet stays retryable.

#### Relationship with Kubernetes ResourceQuota

This is a Kueue-specific policy and does not change Kubernetes `ResourceQuota` (KEP-4816). For
`firstAvailable` requests:

| | core `ResourceQuota` | Kueue |
|---|---|---|
| Within one `firstAvailable` | largest device count among alternatives naming a given DeviceClass | same maximum, applied after mapping DeviceClasses to logical resources |
| Across requests | adds the per-class maxima | adds the per-resource maxima |
| Several DeviceClasses mapped to one logical resource | kept apart | collapsed into one charge |
| Allocation mode `All` | finite worst case from `AllocationResultsMaxSize` | refused for non-admin requests in this Alpha |
| An allocation mode the build does not know | not counted | request refused |

Namespace `ResourceQuota` and `ClusterQueue` quota may both apply to one Workload, and
`firstAvailable` is not a way around either.

#### Limitations and tradeoffs

- The envelope charges the largest alternative even when a smaller one runs. Alpha confines a
  request to one logical resource, so this is one dimension, but a request whose first choice is
  four devices and whose fallback is one still reserves four. This is conservative rather than
  unsafe, and it affects admission, cohort borrowing, preemption, Admission Fair Sharing usage,
  ordering and utilization. Preemption sizes its victim set from this charge, since
  `Assignment.TotalRequestsFor` feeds `workloadUsage.Quota.Assigned`, so Kueue evicts enough work
  to free the envelope rather than the realized allocation; that is the strongest argument for
  adjusting the reservation after allocation, which Beta re-evaluates. Charging less than the
  envelope, or shrinking the reservation after allocation, is discussed under
  [Alternatives](#charging-a-prioritized-list-other-than-by-its-envelope).
- The quota bound is defined for a fixed `ResourceClaimTemplate` identity and quota-affecting
  `ResourceClaimSpec`, from the reservation until the generated `ResourceClaim` is created. Kueue
  does not bind a reservation to a template deleted and recreated under the same name in that
  interval, so the generated claim can differ from the spec that was charged
  ([#13842](https://github.com/kubernetes-sigs/kueue/issues/13842)). The gap is inherited from the
  `Exactly` path; the gate stays Alpha and off by default while it is open, and the binding is
  re-evaluated before Beta.
- Feasibility is not checked at admission on this path, so an unschedulable Workload can hold its
  reservation until `WaitForPodsReady`, when enabled, evicts it.

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

The `firstAvailable` envelope is computed on the path the `Exactly` charge already takes. The
shared DRA defects on that path, and the regression that closes each, are tracked with the
implementation in [#14130](https://github.com/kubernetes-sigs/kueue/pull/14130); the
prioritized-list implementation does not ship while any of these properties is unmet, and each
repaired path is regressed with a `firstAvailable` envelope on the resource, the shape with
nothing in the Pod spec to fall back on. The direct dependencies are a backoff requeue keeping the
preprocessed charge, a charge and the spec it came from moving together, one queueing point
owning the charge, a rejection being recoverable, and the CEL compiler environment.

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
- pkg/cache/scheduler/simulator: 09/16/2026 - 80.0%

Package coverage this scope touches, measured on 09/02/2026 with `go test -cover`:

- pkg/cache/queue: 83.3%
- pkg/config: 89.9%
- pkg/controller/core: 53.8%
- pkg/dra: 66.7%
- pkg/workload: 63.2%

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
- DRA device feasibility: a Workload whose ResourceClaims no node can satisfy stays pending
  and reports `draNoFit` rather than a generic no-fit, one whose claims a single node can
  satisfy is assigned to that node, and a Workload without claims is assigned to any node
- Prioritized list envelope: two DeviceClasses mapped to one logical resource charged the maximum
  rather than the sum, the largest count rather than the first, several top-level requests summed,
  mixed `Exactly` and `firstAvailable`, PodSet count above one, `ElasticJobsViaWorkloadSlices`
  slices each charged from their own count, a logical resource named `cpu` round-tripping the
  milli-unit convention, and the envelope properties stated over the alternatives: order
  independence, monotonicity in any alternative's count, and every alternative's charge at most
  the envelope
- Prioritized list Alpha scope: every row of the support matrix, including omitted
  `allocationMode` and `count` charged as the API defaults, `capacity` under a source-less mapping
  charged its count, every form of the `PodResourceClaim` and `DeviceRequest` unions frozen to its
  kind with the zero kinds failing closed, a source-backed `Exactly` request in the same Workload
  refused with `adminAccess` exempt, and each composition contribution refused and then cleared by
  the event its row names
- Prioritized list exactness: a sum of envelopes past the `int64` range is kept exactly in
  `resources.Amount`, becomes a whole-unit Quantity like the `Exactly` count, including on a
  logical resource named `cpu`, and is saturated by the request path like any other resource
- Prioritized list lifecycle: a backoff requeue and an inflight requeue admit on the charge the
  current revision produces; a stale entry whose inputs changed (the request, a template deleted
  and recreated under the same name, the mapping, the gate) issues no admission, preemption or
  migration; a template that appears re-evaluates the Workloads naming it and one deleted enqueues
  them, while one of the same name in another namespace wakes nothing; a manager killed
  mid-transition rebuilds the same verdict; an admitted Workload rebuilt from `status.admission`
  keeps the total it was admitted on; reordering `spec.podSets` changes nothing
- Prioritized list gates and integrations: an `Exactly`-only Workload charged the same with the
  gate on and off; the child gate with `KueueDRAIntegration` off refused by configuration
  validation; `excludeResourcePrefixes` dropping an ordinary request while a mapped logical
  resource stays charged; `IgnoreUndeclared` filtering an envelope-touched resource on the same
  terms as an `Exactly` charge; the MultiKueue check refusing before any remote object exists,
  with the gate on and off, and retrying a transient template read

#### E2E Test

Use existing dra-example-driver or Kubernetes test driver for e2e testing. For partitionable
devices, integration tests create ResourceSlice objects directly via the API since no test
driver publishes `SharedCounters` yet
([kubernetes-sigs/dra-example-driver#150](https://github.com/kubernetes-sigs/dra-example-driver/pull/150)
tracks adding this). This follows the same pattern as upstream K8s integration tests in
`test/integration/dra/`.

For prioritized lists, the e2e test forces the fallback rather than allowing it: one
preferred-class device and two fallback-class devices on a node, and two Pods sharing a template
that asks for one preferred device or two fallback ones. The first Pod takes the preferred device
and the second has to take both fallback ones. The allocation result records the selected
alternative as `<request>/<subrequest>`, so the test asserts that each alternative was selected
once, that the admitted envelope is 2 per Pod and 4 in total, and that the realized total of
1 + 2 = 3 is at most the admitted 4.

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

##### KueueDRADeviceFeasibility (v0.20)

- per-node device feasibility for Workloads using ResourceClaimTemplates or DRA-backed
  extended resources, so quota is not reserved for a Workload kube-scheduler cannot place
- requires `KueueDRAIntegration`, `TopologyAwareScheduling` and
  `TASNodeFeasibilityForAllLevels`; enabling it without them is rejected at startup
- unit and integration tests

##### KueueDRAIntegrationPrioritizedList (v0.20)

- Envelope accounting for count-based `firstAvailable` (KEP-4816, GA in k8s 1.36): each
  alternative resolved through `deviceClassMappings`, charged the per-resource maximum, merged
  with the existing `Exactly` charge
- The Alpha support matrix enforced by one classifier shared with the MultiKueue admission check,
  refusing the whole request when any alternative is unsupported
- The envelope-touched resource names carried with the charge through queue and requeue, so a
  non-DRA contribution on those names is refused
- Envelopes summed in `resources.Amount`, emitted as a whole-unit Quantity like the `Exactly`
  count and saturated by the request path like any other resource, and a negative operand refused
  at the merge
- Each rejection re-evaluated by the event that clears it, and read failures retried
- Unit, integration and e2e tests, including the forced-fallback e2e

Parent prerequisites, provided by `KueueDRAIntegration` and not designed here:

- an unchanged preprocessing result survives a backoff or a requeue
- a Workload revision that changes what is charged, once observed, invalidates the earlier result
- a recomputation that fails leaves no schedulable entry built from the superseded result, and one
  queueing point owns the result
- a deterministic rejection is recoverable

#### Beta

##### KueueDRAIntegration (v0.18)

- feature gate enabled by default
- support integration with MultiKueue
- e2e tests
- CEL expression validation against ResourceSlice devices

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

##### KueueDRADeviceFeasibility

- feature gate enabled by default
- cache ResourceSlices and DeviceClasses across scheduling cycles instead of reading
  them once per cycle
- surface per-node device capacity counts (not just feasibility filtering). The DRA
  allocator exposes no capacity query, so a count means allocating repeatedly against a
  mutable allocated-device set until it fails, capped at the number the domain needs.
  Cluster Autoscaler estimates node capacity the same way
- release a preempted Workload's devices so preemption can make a Workload
  device-feasible. Upstream tracks the same gap for kube-scheduler in KEP-5690, which
  defers the workload-aware preemption path Kueue uses. That KEP also notes devices
  become allocatable when the resourceclaim controller deallocates, not when the victim
  Pods are deleted, so the simulation has to model that interval rather than assume
  release at eviction
- e2e tests covering that a Pod admitted by the feasibility check is actually placed
  by kube-scheduler on a node with the devices

##### KueueDRAIntegrationPrioritizedList

- feature gate enabled by default
- quota and feasibility paths agree on the supported-versus-rejected predicate, with tests
- alternatives bound to a ResourceFlavor, so a request can fall back between logical resources
  rather than being confined to alternatives an administrator already mapped to one
- upgrade and downgrade behavior verified, including a downgrade with a reserved `firstAvailable`
  Workload
- E2E stability for the count-based case
- re-evaluate binding the admission-time DRA charge to the `ResourceClaimSpec` actually
  instantiated, including a same-name `ResourceClaimTemplate` deleted and recreated between the
  reservation and claim creation
- re-evaluate source-backed alternatives, charging each through its source path and taking the
  component-wise maximum of the resulting vectors
- support non-DRA contributions on envelope-touched resources once the shared accounting
  invariants hold, naming which combinations become supported rather than lifting the limit as a
  whole
- support a source-backed `Exactly` request in the same Workload, once an unavailable source fails
  closed rather than contributing zero
- re-evaluate refusing the static request shapes at the Workload webhook, together with the
  `Exactly` path
- re-evaluate adjusting a reservation to the realized alternative once the generated
  `ResourceClaim` is allocated, since preemption sizes its victim set from the envelope

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
- DRA device feasibility: September 2026 by @sohankunkerkar — added per-node device
  checking before admission, so quota is not reserved for unplaceable Workloads

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

### Adding the dynamicresources Plugin to the Simulated Filters

Kueue already runs kube-scheduler's node filters through the scheduler-library:
`nodeunschedulable`, `tainttoleration`, `nodeaffinity` and `nodeports`. Adding
`dynamicresources` to that set would cover devices through the same mechanism as the other
filters, instead of a second code path.

It does not work before the Pods exist. The plugin walks `pod.Spec.ResourceClaims` and
fetches the ResourceClaim object each entry names; for a template-backed entry that object
is created by the Pod controller when the Pods are created, and its generated name is read
from `pod.Status.ResourceClaimStatuses`. Kueue decides before any of that exists, when it
has only the PodSet template.

Building the claims ourselves and handing them to the plugin does not bridge that gap.
The plugin reaches its claims through a `SharedDRAManager` the scheduler-library builds
from live informers, and offers no way to supply one; the cache behind it refuses an
object that did not come from the API server, so a made-up claim cannot be seeded. That
manager is fixed when the simulation is built, while Kueue asks once per PodSet against
the one simulation, so it is also the wrong lifetime for per-question claims. The
plugin's own extended resource path shows the shape is otherwise workable: there it
fabricates a claim in memory and needs no Pod status, and only the manager stands in
the way.

Kueue therefore calls `structured.Allocator` directly, the same allocator the plugin
builds, and resolves the claims from the PodSet template instead of from Pod status.
This is meant to be temporary: once the scheduler-library can be given a simulated view of
claims, tracked in [scheduler-library#34](https://github.com/kubernetes-sigs/scheduler-library/issues/34),
Kueue should drop its own check and run the plugin with the other node filters, so the two
stay in step by construction rather than by sharing an allocator.

### Charging a prioritized list other than by its envelope

Charging the first alternative was rejected: a request whose first choice is one device and whose
fallback is two would reserve one and could be allocated two, which is the leak the maximum
prevents. Deferring the charge until allocation, or shrinking the reservation to the realized
alternative afterwards, was rejected for Alpha: quota is decided at admission, before any
`ResourceClaim` exists, and shrinking afterwards needs a mechanism that observes the generated
claims and updates admitted usage, cache, fair sharing, borrowing and preemption state. Charging
`All` its worst case, `AllocationResultsMaxSize` devices as core `ResourceQuota` does, was rejected
because Kueue's quota drives admission rather than capping a namespace: reserving 32 devices for a
request that may take one would block admission and hold capacity nobody uses, and the `Exactly`
path refuses `All` today.

### Refusing a request whose mapped resource an excluded prefix covers

Rejecting a `firstAvailable` request whose logical resource an `excludeResourcePrefixes` entry
covers was considered and rejected: it gives the same `deviceClassMappings` entry different
meanings for `Exactly` and `firstAvailable`, and the party told about it, the Workload's author, is
not the one who can fix it. Refusing the configuration at startup is the other consistent answer,
at the price of failing a startup over mappings this feature never touches; a follow-up issue
should weigh it across every mapping rather than one request shape.

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
