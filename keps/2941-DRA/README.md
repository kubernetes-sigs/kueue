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
      - [Where the verdict is allowed to arrive](#where-the-verdict-is-allowed-to-arrive)
  - [Prioritized List Quota](#prioritized-list-quota)
    - [Accounting rule](#accounting-rule)
      - [What revokes an entry the scheduler is holding](#what-revokes-an-entry-the-scheduler-is-holding)
    - [Safety argument](#safety-argument)
    - [Scope](#scope)
    - [Coordination with feasibility](#coordination-with-feasibility)
    - [Relationship with Kubernetes ResourceQuota](#relationship-with-kubernetes-resourcequota)
    - [Feature gate lifecycle, version skew, and observability](#feature-gate-lifecycle-version-skew-and-observability)
    - [Reservation-to-claim identity boundary](#reservation-to-claim-identity-boundary)
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

This KEP supports four approaches for DRA integration with Kueue, with prioritized alternatives
as an additional quota mode of the ResourceClaimTemplate approach rather than a fifth one:
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
- Admission-time device-taint feasibility is not in scope. Subrequest tolerations do not change what
  is charged and stay available to kube-scheduler.
- Multi-host partitionable devices (e.g., NVLink fabrics spanning multiple nodes) are not
  supported.
- This design does not work with Topology Aware Scheduling feature of Kueue. It is a significant
  amount of work, will be addressed in the future with a separate body of work.
- The mechanism that records a verdict is out of scope here. The condition and reason a refusal is
  written to, whatever carries a revision's identity, whether that is a fingerprint, a revision token,
  a UID set, an immutable snapshot or a cache epoch, the builder signature that carries a verdict out
  of the build, and the queue invalidation that retires a stale entry belong to `KueueDRAIntegration`
  and
  are designed with the rest of the outcome protocol. This KEP states the properties it needs from
  them and nothing about how they are provided.
- A Workload carrying an ordinary request, `spec.overhead`, or a resource transformation output on a
  resource an envelope is charged on is refused in the initial Alpha rather than charged through the
  shared accounting those contributions pass. This is a limit on the composition rather than on the
  request shape. It holds for the Alpha stage: prerequisite work merging does not lift it on its
  own, since a Workload going from inadmissible to supported changes what users can submit. Lifting
  it takes a reviewed amendment here, or the Beta criteria naming the combinations that become
  supported.

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
- Device class uniqueness is enforced. Each device class can only map to one resource name to prevent quota ambiguity. Counter-based mappings relax this when counter names differ.
- Configuration-based approach - device class mappings are configured through the Kueue Configuration API
- This design does not work with Kueue's Topology Aware Scheduling feature and will be addressed in future work.
- DRA resource preprocessing is not scoped by ResourceFlavor node constraints. Counter
  charges and device matching are computed globally before flavor assignment.
- AdminAccess requests are skipped in quota counting (zero charge) since they provide
  administrative monitoring or management access to devices, ignoring the access modes and
  allocations that bind ordinary claims. Device taints are left to kube-scheduler.
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

- On the `Exactly` and source-backed paths, CEL selectors in ResourceClaimTemplates are validated against cluster devices (ResourceSlices) at quota reservation
  time on a best-effort basis. Workloads with CEL selectors that match fewer devices than requested are rejected
  to prevent quota leaks. Count-based `firstAvailable` compiles the request selectors and does not run
  this check: every alternative would have to be satisfiable while only one has to be, and the count
  envelope does not depend on which one is. See [Prioritized List Quota](#prioritized-list-quota). This validation uses the upstream DRA CEL compiler from [`k8s.io/dynamic-resource-allocation/cel`](https://github.com/kubernetes/dynamic-resource-allocation/tree/master/cel).
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

**Silent quota bypass when DRA is disabled**: When the `KueueDRAIntegration` feature
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
   administrative monitoring or management access rather than an ordinary allocation. Charging
   quota would double-count the device. This matches the Kubernetes scheduler, which excludes
   AdminAccess from `allocatedDevices`.
3. For allocation mode `All`: a request that is not `adminAccess` is rejected rather than charged.
   `countDevicesPerClass` refuses it on any such request it reads, since how many devices the claim
   would receive is not in the spec, and Kueue has no worst-case charge for it. The order matters
   and is deliberate: `adminAccess` is classified before the allocation-mode accounting, so an
   `Exactly` request carrying `adminAccess` keeps the zero charge whatever its `allocationMode` is,
   and `All` with `adminAccess` is exempt rather than refused. Whether a finite policy can be
   defined for the non-admin case is left to a later stage.
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
  default since Kubernetes 1.34, GA since 1.36, and locked to its enabled default in 1.37, so the
  requirement on a cluster is that the gate has not been turned off rather than that an admin turns
  it on. Counter-backed and capacity-backed alternatives are rejected.
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

To ensure predictable and deterministic quota enforcement, Kueue enforces uniqueness constraints on device class
mappings. For count-based mappings, each device class maps to one resource name across all device class mappings in the
configuration. Counter-backed mappings are the exception validation already makes: the same device class may appear in
several of them as long as each names a different counter, since those track different dimensions of the same device.

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

The validation has two stages, and not every path runs both. `Exactly` and source-backed requests
run compilation and then evaluation. Count-based `firstAvailable` in this Alpha runs compilation
only: it reads no DeviceClass, runs no ResourceSlice cardinality check, and leaves both device
existence and feasibility to kube-scheduler, so an alternative that nothing in the cluster can
satisfy is not refused here.

1. **CEL Compilation**: Each CEL expression in the request's selectors is compiled using the upstream DRA CEL
   compiler ([`k8s.io/dynamic-resource-allocation/cel`](https://github.com/kubernetes/dynamic-resource-allocation/tree/master/cel)). This catches syntax errors, type errors, and other
   compilation issues before quota reservation.

2. **CEL Evaluation Against Cluster Devices**: Kueue lists all ResourceSlices in the cluster and evaluates
   the compiled CEL selectors against actual devices. This step belongs to the `Exactly` path;
   count-based `firstAvailable` skips it, reads no DeviceClass, and leaves both device existence and
   feasibility to kube-scheduler. For each `Exactly` request:
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

When a user submits a workload and KueueDRAIntegration feature gate is enabled, Kueue processes it as follows:
1. DRA Detection: Kueue detects DRA workloads by checking for ResourceClaimTemplates or ResourceClaims in
   podSpec.resourceClaims.
2. Feature Gate Validation: Verify that the KueueDRAIntegration feature gate is enabled. If disabled, continue
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

Note: The flow above applies to the ResourceClaimTemplate path (`KueueDRAIntegration` gate).
When the `KueueDRAIntegrationExtendedResource` gate is also enabled, workloads with extended resources in
`resources.requests` follow a separate resolution path through the ExtendedResourceCache.
See [Extended Resources](#extended-resources) for details. Both paths can be active simultaneously
for workloads that use both ResourceClaimTemplates and extended resources.

#### Workload Rejection When DRA Is Disabled

When the `KueueDRAIntegration` feature gate is disabled, the DRA processing pipeline
is skipped entirely. Without additional safeguards, workloads that reference
ResourceClaimTemplates or ResourceClaims are silently admitted based on CPU/memory only,
with zero device resource usage recorded. This allows unlimited DRA workloads to bypass
quota enforcement, since the Kubernetes DRA scheduler still allocates devices directly.

The `KueueDRARejectWorkloadsWhenDRADisabled` feature gate (default: enabled, Beta) closes this gap. When
enabled and `KueueDRAIntegration` is disabled, Kueue detects workloads with DRA
resources (via `HasDRA()` which checks for `ResourceClaimTemplateName` or
`ResourceClaimName` in any PodSet) and rejects them as inadmissible.

The rejection is enforced in the Reconcile loop: workloads are marked with
`WorkloadQuotaReserved=False` (reason: `WorkloadInadmissible`) and `WorkloadRequeued=False`,
with a message naming the gate.

Administrators who intentionally want to admit DRA workloads without Kueue quota
management can disable `KueueDRARejectWorkloadsWhenDRADisabled` and `KueueDRAIntegration` to restore the previous behavior.

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
   name instead to unify quota with the ResourceClaimTemplate path. If the mapping configures a
   counter or a capacity source, the workload is marked inadmissible, because extended resources
   carry neither the profile-level information counter charging needs nor the `capacity.requests`
   a capacity charge reads.
2. **ResourceClaimTemplates** (`KueueDRAIntegration` gate): uses `deviceClassMappings`
   to map DeviceClass names to logical resource names. When the mapping configures a counter or a
   capacity source, charges counter or capacity units instead of device count.

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
5. If the mapping configures a counter or a capacity source, the workload is marked inadmissible.
   Extended resources carry neither the profile-level information counter charging needs nor the
   `capacity.requests` a capacity charge reads. Otherwise charges device count.
6. Subtracts the container contribution that the logical charge replaces, tracked internally per
   PodSet, to avoid double-counting. It is a subtraction rather than a deletion of the key: pod
   overhead and resource-transformation output can land on the same extended-resource name without
   being DRA-backed, and removing the key would drop them
   ([#14154](https://github.com/kubernetes-sigs/kueue/issues/14154))
7. Admits workload against the resolved quota key

The extended resource translation reads directly from the workload spec before
`excludeResourcePrefixes` filtering is applied. The processing order:
1. Extended resource translation runs first, reading the original spec
2. `excludeResourcePrefixes` filters the pod's `resources.requests`
3. The replaced container contribution is subtracted from the workload's effective resource
   requests, leaving anything else that landed on the same key
4. Translated resource is added through `preprocessedDRAResources`

This ensures no overlap or double-counting between the two mechanisms.

#### Same Hardware with Both Paths

When the same hardware serves both ResourceClaimTemplate users and extended resource users, the two
paths share one quota bucket rather than splitting into two. `resolveQuotaKey` looks the selected
DeviceClass up in `deviceClassMappings` and, when it finds it, uses the mapped logical name so that
the extended-resource charge lands where the ResourceClaimTemplate charge lands. Configuring a
second bucket under the DeviceClass's own `extendedResourceName` does not divide the hardware; it
creates a quota nothing is ever charged against. Assuming a cluster with 1 node and 8 GPU devices
available:

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
# ClusterQueue: one bucket, because both paths resolve to the mapped name
apiVersion: kueue.x-k8s.io/v1beta2
kind: ClusterQueue
metadata:
  name: gpu-queue
spec:
  resourceGroups:
  - coveredResources: ["gpu-claims"]
    flavors:
    - name: default
      resources:
      - name: gpu-claims
        nominalQuota: 8
```

The eight devices are one pool that both populations draw from, which is the point of mapping the
class: a request through `example.com/gpu` and a request through a ResourceClaimTemplate naming
`gpu.example.com` consume the same `gpu-claims` quota, so the total admitted never exceeds the
hardware. Splitting capacity between two populations is a different thing and this mapping cannot
express it. It needs the populations to be separable at the DeviceClass level, through distinct
DeviceClasses under distinct mapping entries, or through a policy layer above quota; naming the
same class twice does not do it.

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

Counter resources are injected through the same preprocessed payload as every other DRA charge,
which reaches the builder as a parameter rather than as an option.

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

Only `ExactDeviceRequest` with `count` is supported here, so count-based `firstAvailable` quota
(see [Prioritized List Quota](#prioritized-list-quota)) rejects any alternative whose DeviceClass
mapping configures a capacity source, consistent with the existing exclusion for partitionable
devices. A subrequest carrying `capacity.requests` under a source-less mapping is charged by
device count like any other, since the field does not change how many devices the alternative
asks for.

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

### Architecture Details

#### Queue Manager Extensions

The queue manager takes DRA resource preprocessing through an `InfoOption` today. What this design
needs of whatever route an implementation keeps is two properties rather than a shape. The charge,
the keys it replaces and the record of where it came from are never observed out of step with each
other, since a reader that sees one without the others answers for a workload that never existed.
And what a schedulable entry is built from has passed the check required
[below](#where-the-verdict-is-allowed-to-arrive) rather than having been able to skip it.

##### Where the verdict is allowed to arrive

A verdict is what one preprocessing pass concludes about a Workload: supported with a charge,
refused for a reason some later change can clear, or a transient failure to retry. What records it,
and where, is the parent path's rather than this gate's; what this section is about is that the
build has somewhere to put one.

`AddOrUpdateWorkload` returns an `error` and nothing else, and it builds the `Info` itself, so a
builder that finds a merged total it cannot represent has no return for the verdict. An
implementation handed that signature has three ways out and all three break something this design
states: demote the verdict to an error, and a total that can never change is retried forever; drop
it, and the Workload is queued on a charge nobody stands behind; or keep calling the unchecked
constructor, and the merged-total check does not run at all. The last is what the code does today,
and it compiles.

Alpha requires, without prescribing the shapes here:

- a verdict that survives the build rather than being flattened into an error or dropped, reached
  before the queue manager is asked to hold anything, and no path to a schedulable entry that skips
  the check;
- one view of a Workload's resources, read by the DRA pass and by the build alike, so a committed
  entry cannot be internally consistent over two separately adjusted copies;
- a commit that installs only while what it was built from is still what Kueue has observed, and
  refuses rather than overwrites when it is not.

These are `KueueDRAIntegration` requirements rather than prioritized-list ones, since every one of
them is reachable from the existing `Exactly` path.

### Prioritized List Quota

This section is gated behind the `KueueDRAIntegrationPrioritizedList` Kueue feature gate (Alpha,
default off). It adds quota accounting for prioritized-list requests, expressed with the
`firstAvailable` field of a `DeviceRequest` (the Kubernetes `DRAPrioritizedList` feature, GA in
1.36). A `firstAvailable` request lists ordered alternatives, at most eight of them
(`FirstAvailableDeviceRequestMaxSize`); kube-scheduler selects exactly one at allocation time, which
is not known when Kueue reserves quota. With the gate off, such requests remain rejected as
before.

One thing this section depends on is not this gate's to provide. A rejection has to be recordable
and clearable, and an obsolete charge has to stop being schedulable, for every DRA request shape
rather than for `firstAvailable` alone. Those are `KueueDRAIntegration` properties rather than this
gate's, stated as requirements in [where the verdict is allowed to
arrive](#where-the-verdict-is-allowed-to-arrive) and [what revokes an entry the scheduler is
holding](#what-revokes-an-entry-the-scheduler-is-holding), and designed separately.
`KueueDRAIntegrationPrioritizedList` decides one thing: whether a `RequestFirstAvailable` request
may enter accounting, or is refused as unsupported.

The line matters because the alternative is an Alpha child gate whose toggle changes rejection and
recovery for Workloads that never used the feature it gates. Stated as an invariant a test can
hold: toggling `KueueDRAIntegrationPrioritizedList` changes nothing observable for a Workload with
no `firstAvailable` request.

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
effective PodSet count by the existing workload-request calculation, which is the count after
`ReclaimablePods` has been taken off. The representability check reads `spec.podSets[].count`
instead, the largest count the workload can ask for, so a request is refused on what it asks for
rather than on what a reclaim happens to have left. The two agree until pods are reclaimed, and
where they differ the check is the stricter one. With
`ElasticJobsViaWorkloadSlices`, each slice carries its own PodSet count, so each computes its
envelope independently and multiplies the same per-Pod envelope by its own count; nothing is shared
between the slices of one Job.

Saturating arithmetic stops a sum from wrapping, but a saturated sum no longer stands for the value
it replaced, so it cannot carry the bound: a clamped total reports less than the total it came
from, and wherever the accounting clamps rather than reports, nothing carries the bound. A charge
is admissible only when every accounting and persistence representation active in the tree can
preserve it exactly, so one that any of them cannot makes the workload inadmissible rather than
being clamped and admitted, at each step that can produce one: the per-Pod sum across requests, the
merge with the `Exactly` charges on the same name, and the PodSet-count multiplication. Nor is
a saturated aggregate divided to recover a smaller request. `PodSetResources` holds the aggregate
alone and
scales by dividing by the old count and multiplying by the new, which is exact for what it admits:
an aggregate that had to be exactly the per-Pod value times the count divides back to that value,
so partial admission, `ReclaimablePods`, and workload-slice replacement recover it rather than
needing it stored.

Converting a combined `resource.Quantity` back to `int64` follows the unit convention in
`resources.ResourceValue`, milli-units for `cpu` and absolute units otherwise. That helper reaches
`Value()` through `SafeMilliValue` and `SafeValue`, which clamp a quantity the representation
cannot hold rather than reporting it, so following it is not on its own enough: a quantity that
does not convert exactly has to be reported rather than silently changed, which is what makes the
rejection above possible. Exactly means at milli scale for `cpu` and at scale zero otherwise, and
covers rounding as well as range, since `Value()` rounds away from zero: a fractional non-`cpu`
total is as much a rejection as one the representation cannot hold, and the rule holds whether or
not this Alpha admits a contribution that can produce a fraction.

Where each property is checked matters, because checking exactness too early refuses a request
that is representable. Each operand is checked non-negative as it arrives, since that is what stops
one cancelling another and leaving a clean zero. Exactness is checked on the merged total at the
conversion, then on the PodSet multiplication, then on the running total across PodSets. Two
contributions of `0.5` to a non-`cpu` resource merge to `1`, which converts exactly, and refusing
either one on its own would turn away a workload nothing is wrong with.
What the check has to cover is the resources an envelope is charged on. The Alpha composition
limit in [Scope](#scope) refuses a workload carrying a non-DRA contribution on one of those names
rather than merging it, so what is left to check exactly is the envelope, the `Exactly` charges on
the same name, the PodSet multiplication and the sum across PodSets. A resource no envelope reaches
keeps the conversion it has now, so turning the gate on does not change what an unrelated
fractional request admits to.

Before the gate ships, preprocessing MUST carry the set of logical resource names the
`firstAvailable` envelopes reached, and the workload request builder MUST read the same effective
resources the DRA pass read, refuse a workload carrying a non-DRA contribution on any of those
names, and check the merged value of what remains. It has to read them before extended-resource
replacement runs, since replacement removes a DRA-backed extended resource by name and takes
`spec.overhead` or a transformation output on that name with it: a check placed after it sees a
clean key and admits the composition it exists to refuse. The builder is where both happen because
it is
the one place that sees the DRA charge and the rest of the PodSet's requests together; a check
placed anywhere earlier answers for the DRA charges while the other operands on the same resource
go through unread, so it does not satisfy this and the gate does not ship on one.

Which resources those are is not in what preprocessing hands over.
`PreprocessedDRA` carries one `ResourceList` per PodSet with every DRA path already
merged into it, so nothing downstream can tell a key an envelope reached from one that only an
`Exactly` request or a DRA-backed extended resource reached. Preprocessing has to carry that set of
names alongside the merged charge, and it has to survive the same requeue the charge does. The scope
is their union across the workload rather than the set for one PodSet: an ordinary `gpu` request in
a PodSet with no alternatives at all still lands in the same cross-PodSet total as an envelope
charged on `gpu` somewhere else.

What decides whether an update is worth re-evaluating has to read that set and not only the totals,
since the two move independently: a Workload whose charge is unchanged and whose set of
envelope-touched names is not still has to be re-evaluated, because the composition check reads the
names. Which of the templates behind the charge have moved is part of the same question. How a
revision is identified so that comparison can be made, and the commit discipline that keeps an
entry's fields from being assembled out of two revisions, are `KueueDRAIntegration` mechanism and
are designed with the rest of the outcome protocol. The invariant an implementation has to be
checkable against:

```text
For a Workload revision, exactly one of these is externally observable:

1. a valid queue entry with nothing in it read from a revision other than
   that one; or
2. no schedulable entry, with the reason visible on the Workload; or
3. no queue entry at all.

A stale schedulable entry is never retained after preprocessing or rebuilding
for a newer revision has failed, whether it failed with a verdict or with a
transient error.
```

The failure that has to be covered as much as the verdict is an entry built for revision A, an
input that changes, and a `Get` that times out on the next pass. A reconcile that writes nothing
and touches nothing leaves the scheduler able to pop revision A's charge. Both outcomes stop the
Workload being schedulable and differ only in what is written; whichever the parent path records
for each, neither keeps the old valid charge. What the queue has to provide for that is
[below](#what-revokes-an-entry-the-scheduler-is-holding).

##### What revokes an entry the scheduler is holding

Recording a refusal keeps a Workload out of a queue it has not entered. It does not revoke an entry already
in the heap, one a scheduling cycle has popped, one a requeue is about to mutate, or one held by a
replica that is not currently leading. Preemption is the part that cannot be taken back: a Workload
can be evicted to make room for a charge already known to be wrong, and unlike an admission rolled
back by a failed patch, nothing un-evicts it.

Current, wherever this document says an entry is built from something still current, means current
with respect to the Workload and dependency revisions Kueue has observed at the point the decision
is committed. An informer delivers a change after the API server has taken it, so a guarantee
written against the API server's own state is one no mechanism here can provide, and writing it
that way would be claiming a property the implementation cannot hold. Two windows stay open and are
stated rather than claimed shut: a change the API server has accepted and Kueue has not yet
observed, and the interval between the charge and the generated `ResourceClaim`, which is
[#13842](https://github.com/kubernetes-sigs/kueue/issues/13842) and the boundary stated
[below](#reservation-to-claim-identity-boundary).

Alpha requires these properties of the queue. How each is satisfied is being written with the
implementation rather than fixed here:

- an entry carries what it was built from, and once Kueue has observed a change to any of it, no
  irreversible effect is issued for that entry afterwards;
- an effect and an invalidation never interleave into a third outcome: an effect is either decided
  before the invalidation or does not begin;
- the guarantee holds on whichever replica acts, including one that built an entry while following
  and then becomes the one that schedules;
- a requeue keeps a previously computed total only while everything that total was computed from is
  unchanged;
- a status write that fails leaves the entry out of the queue rather than in it, and one that can
  never land still leaves the Workload diagnosable rather than only safe.

None of this is specific to `firstAvailable`. Every race it closes is reachable from the existing
`Exactly` path, which is why these are stated as `KueueDRAIntegration` requirements rather than
designed here.

#### Safety argument

kube-scheduler selects exactly one subrequest from each `firstAvailable`, so for every logical
resource `r` the selected alternative's charge is at most `envelope(q)[r]`. Summing over all
top-level requests, the realized charge cannot exceed the admitted envelope. This holds only if
every alternative resolves to a complete, non-negative charge vector; unmapped, unsupported, or
unknown forms are rejected rather than charged.

The other operand has to be non-negative too. A logical resource is merged with whatever the PodSet
already requests under that name, and the total is floored to zero afterwards, so a negative
ordinary request on that key subtracts from the envelope and leaves a clean zero where a device was
charged. Three ways in. A resource transformation output can be negative on purpose, which is how a
per-unit allowance is written, and the total one resource generates is not confined to the outputs
it was written against: it reached the merge and came off the envelope; that is
[#13985](https://github.com/kubernetes-sigs/kueue/issues/13985), where an envelope of `8` with a
generated `-3` under that name was charged `5`. That one is closed by
[#13986](https://github.com/kubernetes-sigs/kueue/pull/13986); it is written out here because the
premise it violated is the one the envelope rests on, and the regression that keeps it closed has
to run with an envelope on the resource. And `validatePodSet` does not reach
`spec.overhead`, which `PodRequests` adds to the charge, so a negative overhead survives even with
`WorkloadValidateResourcesAreNonNegative` on; that is
[#13991](https://github.com/kubernetes-sigs/kueue/issues/13991). The third is the guard itself.
`WorkloadValidateResourcesAreNonNegative` is Beta and on by default, and an administrator can turn
it off, after which an ordinary container request carries the sign into `PodRequests` with nothing
between it and the merge. Requiring that gate alongside `KueueDRAIntegrationPrioritizedList` would
leave the bound resting on something an administrator can switch off, so Alpha checks the operand
where the merge happens instead. The arithmetic is the same as the first way in, reached from the
other side: a `-3` that came from the container rather than from a transformation still leaves an
envelope of `8` charged `5`, and a check that read the sign of that result rather than of the
operand would pass it. What has to be non-negative is what arrives at the merge, which is not the same as every
value on the way there, since an allowance is negative by design and is spent before it gets that
far. That belongs with exact representability in what has to hold before the merge; treating
`FloorToZero` as the guard hides the cancellation rather than preventing it.

Those two premises say something about the operands and nothing about them arriving. One more has
to: every contribution to a key a logical resource is charged on reaches the merge exactly once.
`applyResourceTransformations` accumulates the outputs of transformations but assigns a retained or
untransformed input, so when an output shares its name with something the PodSet requests directly,
which of them survives is decided by the order the input map is walked, and the same workload can be
charged differently from one call to the next. No sign or range is involved: a positive request
disappears. That is
[#13990](https://github.com/kubernetes-sigs/kueue/issues/13990), closed by the same
[#13986](https://github.com/kubernetes-sigs/kueue/pull/13986), and the bound said nothing until it
held. There is a second way to lose one, on the extended-resource path rather than in the
transformation. A DRA-backed extended resource is removed from the PodSet's requests by name and its
charge added back from the container requests alone, so `spec.overhead` or a transformation output
on that name is deleted with it, and a restartable init container is summed into the requests and
maxed out of what replaces them; that is
[#14004](https://github.com/kubernetes-sigs/kueue/issues/14004). Neither of those is about the size
of a charge, and a third one is. A multiplier is read out of the list transformations run over, so
one that `excludeResourcePrefixes` has already removed is simply absent and the input is carried
through unscaled; that is
[#14007](https://github.com/kubernetes-sigs/kueue/issues/14007), and the output it produces still
arrives, once, positive and exactly representable, at whatever a multiplier of one gives instead of
the configured one. So the premise covers both: every contribution to a key a logical resource is
charged on reaches the merge exactly once, and reaches it as the value its own source defines. What
`PodRequests` aggregates for an ordinary request or overhead, the input as the PodSet asked for it
under `Retain`, the configured input and multiplier for an output, and only the request it stands in
for under an extended-resource replacement. A contribution arriving at whatever the code computed
rules nothing out, since a wrong value is delivered as faithfully as a right one. The envelope
bounds a resource, and it bounds nothing if the other charges on that resource arrive wrong or not
at all.

The order decides what a transformation can see, too. `applyResourceTransformations` runs over the
pod's requests, before the logical resources are merged in, so a logical resource that exists only
after the merge is not there to be transformed. Named as `transformations[].input` it matches
nothing and produces no output, and named as `multiplyBy` it is absent, which leaves the input
carried through unmultiplied rather than scaled by the device count. Outputs aimed at a logical
resource are the other direction and do reach it through the merge. Alpha does not add a second
pass over the merged requests; the restriction is written down so that a configuration reading as
though it scales with the devices is not taken for one that does.

That filter, the one #14007 above is about, is applied in the same place, to the pod's requests and
before the merge, so it does not reach a logical resource either. With `example.com/` excluded, an
ordinary `example.com/other` request drops to zero while a logical `example.com/gpu` is charged 8.
Nothing is undercharged and the bound is untouched, but one part of the configuration says a
resource is ignored while another charges it.

The rule is that `excludeResourcePrefixes` applies to the Pod's own requests, before transformations
run, and a logical resource name an explicit `deviceClassMappings` entry synthesizes stays
chargeable. That is what the `Exactly` path does today, and stating it rather than adding an
exception keeps one administrator configuration meaning one thing whichever shape the claim
request takes. Refusing a `firstAvailable` request over the overlap would have made the same pair of
settings mean two different things depending on how a user wrote their claim, and it would have put
the refusal on a workload whose author cannot remove the overlap, since both the mapping and the
prefix belong to the administrator. Whether an overlap should be refused at all belongs with
configuration validation, for every mapping rather than one request shape, and is worth its own
issue rather than being decided here.

This only covers the request forms that exist in
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
| `capacity` | accepted, and not part of the charge: it filters which devices qualify and how much of each one an allocation consumes, while `count` still decides how many are allocated |

The API specifies the defaults, so the classifier reads effective values: an omitted
`allocationMode` is `ExactCount`, and an omitted `count` under that mode is one. The same type
states that clients must refuse to handle requests with unknown modes, which is what the rejection
above does. This gate requires the effective reading on the `firstAvailable` path and nothing more.
The `Exactly` walker reads the raw fields today, so an unset mode falls through to unsupported
there while a subrequest with the same gap is charged one device; an object that went through the
apiserver carries the defaults either way, so the difference is reachable from a fake client and an
undefaulted unit object rather than from a live one. Giving both walkers one reading is worth doing
and is parent cleanup: changing the `Exactly` path here would contradict the gate-isolation
criterion, which is that an `Exactly`-only Workload is charged the same whether this gate is on or
off. Claim-level `constraints` and `config` do not change the device count, and `pkg/dra`
does not read them.

Reading `capacity` as an effective value is what decides its treatment. An omitted `capacity` is
not the absence of a capacity requirement: on a device that allows multiple allocations the API
consumes `RequestPolicy.Default` for each dimension, or the device's full `Capacity.Value` where no
policy is set. Refusing the explicit form would therefore turn away the alternative that consumes
less and admit the one that claims the whole device, which is not a boundary worth drawing. What the
field decides is which devices qualify, described in the same type as equivalent to a CEL selector,
and how much of each one the allocation consumes; `count` still decides how many are allocated.
Charging the declared count while an allocation takes part of a shared device overcharges, which is
the direction this envelope is allowed to be wrong in. The `Exactly` path already accepts the field
under a source-less mapping and charges the same count with it as without, so rejecting it here
would be an asymmetry with nothing behind it.

This gate accounts for the number of device allocations rather than for consumable capacity, and
the boundary that carries that is the mapping rather than the request: an alternative whose
DeviceClass mapping configures a counter or capacity source stays rejected, because those paths
read `Exactly` requests only and would charge such an alternative nothing.

Kubernetes 1.37 adds `derivedAttributes` to `DeviceSubRequest`. Kueue is compiled against 1.36,
where the type ends at `capacity`, so a 1.36 build talking to a 1.37 apiserver decodes a subrequest
without that field and cannot reject an alternative that uses it. The envelope still charges the
declared count, which is what a count-based mapping produces in any case. Raising the dependency to
1.37 and classifying the field is a prerequisite for this gate leaving Alpha.

The bound rests on three further assumptions. The first is the reservation-to-claim identity boundary
stated [below](#reservation-to-claim-identity-boundary): the `ResourceClaimSpec` Kueue charges is
the one later used to create the generated `ResourceClaim`. Second, the
bound covers the logical resources the target `ClusterQueue` manages, and claims nothing outside
them. `quotaCheckStrategy: IgnoreUndeclared`, from
[KEP-7513](../7513-quota-check-strategy/README.md), leaves a resource the ClusterQueue does not
declare out of the quota check, and an envelope-touched resource is not an exception to it. It is
filtered where an `Exactly` charge, an extended resource and an ordinary request on the same name
are filtered, so the strategy reads as one rule rather than one per claim shape. An administrator
who wants DRA quota enforced declares the mapped logical resource or leaves the strategy at
`BlockUndeclared`. Making a DRA charge unwaivable is a defensible policy and not this gate's to set:
it would have to cover `Exactly`, extended resources, counters, consumable capacity and
`firstAvailable` together, which is KEP-7513 or the parent DRA path. Since every alternative of a
request resolves to the same logical resource, the filter takes the request as a whole rather than
one of its alternatives.

Third, the envelope has to survive as far as the queue entry. It is carried by the preprocessed
payload, and a `Workload` requeued after its backoff has elapsed is added again without it, so the
entry is rebuilt from the pod spec. A `firstAvailable` request
asks for its devices through a `ResourceClaimTemplate` rather than through container resources, so
nothing of it is left in that rebuild: the charge is not reduced, it is gone. This is the existing
`Exactly` path's problem too and is tracked in
[#13930](https://github.com/kubernetes-sigs/kueue/issues/13930), but the envelope is worth nothing
until it holds, so the Alpha implementation waits on it.

#### Scope

Supported: `ResourceClaimTemplate` references; `ExactCount` subrequests, including an alternative
that omits `allocationMode` or `count` and takes the API defaults of `ExactCount` and one;
count-based `deviceClassMappings` (no `sources`); request-selector CEL compilation; subrequest
`capacity` requirements, which are charged by device count.

Representability is decided against `spec.podSets[].count`, the largest count the workload can ask
for, even when the effective count is already smaller. The builder multiplies by the count left
after a reclaim and an admitted Workload is rebuilt from `podSetAssignments[].count`, so this is not
a number the first `TotalRequests` mechanically has to hold. It is a deliberate choice: validity
then does not depend on partial admission or on reclaim state, which are both things that move
after the verdict was given. The cost is a utilization one rather than a safety one, and worth
naming as such. A request whose spec count is not representable while its `minCount` or its
after-reclaim count would be is refused anyway, and Alpha does not use either to rescue it.

Rejected, and by what re-evaluates each one rather than as a single list, since an implementation
written from a list that does not say waits for the wrong event:

- the request's own shape, cleared by a request or template change alone: direct `ResourceClaim`
  references, `All` for a request that is not `adminAccess`, and unknown allocation modes;
- the configuration around it, cleared by a request or template change or by a manager restart: an
  unmapped DeviceClass, any
  alternative whose DeviceClass mapping configures a counter or capacity `source`, and alternatives
  that resolve to more than one logical resource. The last is a configuration-dependent restriction
  and not a property of the request: the same Workload becomes supported when the administrator maps
  those DeviceClasses to one logical resource, so it must not be filed as a shape the Workload has
  to change;
- what else the Workload carries on a resource an envelope is charged on, which is an
  effective-resource composition rather than a request shape: any non-DRA contribution the merge
  reads there;
- a source-backed `Exactly` request anywhere in the same Workload, unless it carries `adminAccess`,
  which is classified first and charges nothing. This one is cleared by the claim definition or by
  the mapping, so it sits across the two classes above rather than inside either.

The initial Alpha refuses the last two rather than charging them through shared accounting whose
defects are still open in the first prerequisite table. Closing those is what makes lifting the
limit possible and not what lifts it: the combinations that become supported are named at Beta or in
an amendment here, so the surface a user can submit does not widen as unrelated pull requests
merge.

These last ones do not share one clearing event, and filing them together would be the mistake this
list exists to avoid: an implementation that waits for a Workload update alone leaves a workload
refused for a `LimitRange` default sitting there while the administrator edits the `LimitRange` and
nothing wakes it. What clears each:

| Contribution | Cleared by |
| --- | --- |
| a container, init-container or Pod-level request, including a limit read as a missing request | a PodSet template update |
| `spec.overhead` on the Pod template | a PodSet template update |
| a `LimitRange` default | that `LimitRange` created, updated or deleted, or a PodSet template update that stops it applying |
| RuntimeClass overhead | that RuntimeClass created, updated or deleted, or a `runtimeClassName` change |
| a resource transformation output | a manager running with the changed configuration, or a change to the effective input it reads |
| a source-backed `Exactly` request in the same Workload | the claim definition changing, whether the request is removed, the template deleted and recreated, or the Workload made to reference a different one, or Kueue starting with mapping or capability state that makes the request count-based |

Every event in that column is already delivered by a watch the workload controller has, so the
limit adds no watch surface. Two things on a charged resource are not contributions of this kind:
an `Exactly` charge, which is charged and checked against the same total, and a DRA-backed extended
resource, which is replaced by its DRA charge before the merge reads it.

Source-backed alternatives are excluded because the counter and consumable-capacity accounting
paths process only `Exactly` requests today; they can be added later by charging each alternative
through its source path and taking the component-wise maximum of the resulting vectors.

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
list attribute features available, so a selector it accepted can fail to compile in Kueue; that is
[#14372](https://github.com/kubernetes-sigs/kueue/issues/14372).

The contract is that Kueue compiles a stored selector against the deliberate superset the
Kubernetes API version it is built against exposes, rather than against the runtime feature set.
The argument for the superset is narrow and worth stating as such: a selector that reached
`ResourceClaimTemplate.spec` or `DeviceClass.spec` was validated by the apiserver first, so
widening the environment can only remove false negatives for those. It does not extend to
selectors that never passed apiserver validation, which includes the `sources` selectors an
administrator writes in the Kueue Configuration, objects a fake client returns, and unit-test
fixtures built without apiserver defaulting. Those need their own validation rather than the
inherited guarantee, and an expression whose evaluation type depends on the feature environment
needs a test that compiles and evaluates it under both. The cache is built at package
initialization, before any flag or configuration has been read, which a runtime set cannot be. The
same compiler serves the `Exactly` path, since compiling only the new shape correctly would leave
the existing one refusing selectors the apiserver accepted.
[#14372](https://github.com/kubernetes-sigs/kueue/issues/14372) is open and this is not resolved by
stating the contract.

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

Three dispositions must be distinguished, and what separates them is not only whether a retry
changes the answer but what has to change before one does.

- Unsupported request definition, cleared by a request or template change: a property of the
  request itself,
  such as a non-admin `All`, an unknown allocation mode, or a direct `ResourceClaim` reference.
  Rejected as inadmissible. A retry against the same definition rebuilds the same rejection, so
  nothing requeues it, but an update to the PodSet template, or an event on the identity or spec of
  the claim template it names, has to be evaluated afresh rather than inheriting the old verdict.
  The template half matters because `ResourceClaimTemplate.spec` is immutable: repairing a request
  that lives in one means deleting and recreating it, which is a new object rather than an edit. Where one alternative is refused for any reason, the whole request is refused rather than
  that alternative skipped: the envelope is a maximum over every alternative, so dropping one of
  them stops bounding the allocation the scheduler may still choose.
- Repairable dependency or configuration: the request is well formed and something outside it is
  not. An unmapped DeviceClass, a mapping that configures a counter or capacity source, alternatives
  that reach more than one logical resource, and a gate rolled back are all of this kind. Each of
  them turns on configuration rather than on the request: the same Workload becomes supported once
  an administrator maps the class, drops the source, or maps the two classes to one resource. Calling these permanent is wrong in the way
  that matters: the administrator who can fix them is not the Workload's author, and the Workload
  has to become admissible again once they do. Mappings are read from the Configuration at startup,
  so the repair arrives with a controller restart rather than through a watch, and the rejection
  cannot depend on a condition nothing clears afterwards.
  [#13969](https://github.com/kubernetes-sigs/kueue/issues/13969) is that failure already: the
  `Requeued=False` with reason `Inadmissible` that the DRA path writes has no complete
  persisted-clear-and-requeue path. The success path removes the false conditions from the in-memory
  copy the queue is handed, but that removal is not written back through a status patch, the
  disable and restart paths do not always reach the clearing code, and the queue eligibility latch
  and the condition are not closed against each other, so a workload refused once can stay out of
  the scheduling heap. Editing the Workload, deleting and recreating the referenced template,
  and stopping and starting the queue may none of them bring it back. Alpha cannot write a rejection
  of this kind until that clears. Clearing the condition is also not enough on its own for a claim
  template, since `ResourceClaimTemplate.spec` is immutable and a repair means deleting and
  recreating it. The workload controller watches Workloads, LimitRanges, RuntimeClasses,
  ClusterQueues, LocalQueues and, under its gate, DeviceClasses, and nothing else delivers that
  event. Recovery needs an index from a Workload to the templates it references, keyed by
  `namespace/name` so a template in one namespace does not wake Workloads in another, and a watch
  covering the whole lifecycle rather than creation alone:

  - missing to created: the pending Workloads referencing it are evaluated again;
  - valid to deleted: the Workloads whose entry was built on that template are reconciled, so the
    entry is replaced rather than left admitting on a charge whose source is gone;
  - deleted and recreated under the same name: the new spec is read and the charge replaced, since
    the name is all the reference carries.

  A watch enqueues a reconcile; it does not reach into the queue. Between the delete event and the
  reconcile that replaces the entry, the scheduler can still pop the entry built on the template
  that is gone, so this narrows the interval rather than closing it. Closing it needs one of
  three mechanisms: removing the affected Workloads from schedulable
  positions in the event handler, holding the template revision on the queue entry and re-checking
  it before admission, or comparing the entry against the revisions Kueue has observed on pop. All
  three act on what Kueue has observed, which is why the guarantee is written against that and not
  against the API server. Alpha ships the watch and states the bound, and the entry-level guarantee
  is an Alpha requirement listed with the criteria below; which of the three closes it is the parent path's
  choice rather than this document's. Between Kueue taking the charge and the generated ResourceClaim being created, a
  same-name recreation can still leave the realized claim different from the charged one, which is
  the separate boundary stated [below](#reservation-to-claim-identity-boundary).
- Waiting for a dependency: the claim template the request names does not exist. Nothing about the
  request or the configuration is wrong, and what resolves it is the object being created, so the
  trigger is a watch rather than a retry or a restart. This is separate from the two above because
  neither a change to the request definition nor a manager restart brings it back.

  A missing DeviceClass is not in this class for the Alpha scope, and the reason is that the
  count-only path does not read the object at all. The envelope reads a subrequest's
  `deviceClassName` and looks it up in `deviceClassMappings`; the charge is the declared count, so
  nothing in the bound depends on the DeviceClass existing or on what its selectors say. The
  `firstAvailable` path therefore compiles the request selectors, which are persisted in the claim
  spec and were validated by the apiserver, and does not fetch the class to compile
  `DeviceClass.spec.selectors`. An unmapped class stays a configuration verdict, and the existence
  of a mapped one is left to kube-scheduler along with the feasibility this Alpha already skips.

  The alternative was to keep reading the class and treat a missing one as a transient error, and
  it is the worst of the three positions available. A `NotFound` is an absence that creating an
  object repairs, not a transport failure, so retrying it as one means waiting on rate-limited
  reconciles for an event nothing delivers: the DeviceClass watch the controller has is registered
  under `KueueDRAIntegrationExtendedResource` and finds Workloads by `spec.extendedResourceName`,
  which is not how a claim template names a class. A selector edited in place would change the
  compile verdict with nothing recording which revision was compiled and no watch to notice.

  If class selectors are wanted on this path later, the cost is the whole set and not part of it: a
  DeviceClass UID or generation recorded with the charge, an index from a claim template to the
  `deviceClassName`s it carries, a cluster-scoped DeviceClass create, update and delete watch
  registered under the core DRA gate, a rejection that creating the missing class
  clears, and a test that a selector change at an unchanged total re-evaluates the Workload.
  That is a graduation criterion. Compiling the class selectors here would also not close
  [#14372](https://github.com/kubernetes-sigs/kueue/issues/14372), which is about the environment
  the shared compiler is built with; that stays a prerequisite in its own right.
- Transient read or API error: a listing that fails, a cache that is not synced, an API transport
  error. Not a verdict on the request at all, and surfaced as a reconcile error to retry.

Those three verdicts and the plain error are the whole set. A transient failure is not a verdict on
the request at all, so nothing durable is recorded for it and nothing has to clear it: it is the
`error` return.

Dynamic feasibility is a fourth question, cutting across the second: whether the current cluster
state can schedule a supported claim. A supported-but-infeasible claim is retryable rather than an
unsupported one. Kueue has a ResourceSlice controller, but it builds its driver set from the `sources` of each device class
mapping and filters slice events on that set, so a count-only mapping registers no driver and its
slice changes trigger no requeue. The initial Alpha also reserves quota without running the
cardinality or allocator checks, so Kueue never produces a supported-but-infeasible result to
requeue in the first place; once quota is reserved, kube-scheduler owns dynamic feasibility and
retries the Pending Pod on ResourceClaim, DeviceClass, ResourceSlice, and node events. A workload
still waiting on quota is requeued on the next ClusterQueue event, and an admitted one whose Pod
never becomes schedulable holds its reservation until `WaitForPodsReady`, when enabled,
evicts it. Extending the controller to count-only mappings, or reaching feasibility through the
scheduler library, is a graduation criterion rather than something the current controller covers.

The quota path MUST consume the static-support classification from a single helper, and the
MultiKueue check and any admission-time feasibility path MUST consume that same helper, so a claim
is never classified one way by one of them and another way by the rest. `ProcessDRA` owns the outer
reference classification, which means the `workload.HasResourceClaim` branch the reconciler takes
before it reaches claim-template processing is removed, or reduced to a call into the same
classifier. Leaving it where it is would keep the quota path answering the direct-reference case on
its own while a shared classifier answered it for everyone else, which is the split this paragraph
is written to prevent. Splitting them invites the
failures that are hardest to notice: a shape the quota path charges and the feasibility path
refuses forever, a shape the feasibility path admits and the quota path never charges, or a new API
field that only one of them learns to reject. A feasibility result of "infeasible" must not be
surfaced as "unsupported", and a failed object read or slice listing is neither.

Classification runs in two stages. The first reads the API shape alone, with no feature gate, no
mapping, and no cluster state:

```go
// Which reference the PodSet carries, read before any DeviceRequest is.
// The zero value is invalid on purpose: an unset field, a missed assignment
// or a branch added later must not classify as a supported shape.
type ClaimReferenceKind uint8

const (
    ClaimReferenceUnknown ClaimReferenceKind = iota
    ClaimTemplateReference
    DirectClaimReference
    MalformedClaimReference
)

// The shape of one DeviceRequest, with the same zero value rule.
type RequestKind uint8

const (
    RequestUnknown RequestKind = iota
    RequestExactly
    RequestFirstAvailable
    RequestMalformed
)
```

A direct `ResourceClaim` reference is not a `DeviceRequest` shape, so it cannot be a `RequestKind`.
It lives on the outer `PodResourceClaim` union, and the outer classification runs first: the
builder skips a reference that is not a template today, which is a decision the type system has to
be able to express rather than a gap in the loop.

`PodResourceClaim` requires exactly one of `resourceClaimName` and `resourceClaimTemplateName`, so
the union has forms the API forbids and a classifier that reads only the template field cannot tell
them from a template reference. Every form is named:

| Shape | Kind | Cleared by |
| --- | --- | --- |
| template name set, not empty | `ClaimTemplateReference` | processing continues |
| claim name set, not empty | `DirectClaimReference` | request or template change |
| both set | `MalformedClaimReference` | request or template change |
| neither set | `MalformedClaimReference` | request or template change |
| either set to the empty string | `MalformedClaimReference` | request or template change |

The inner `DeviceRequest` union is read the same way, and every form is named:

| Shape | Kind | Cleared by |
| --- | --- | --- |
| `exactly` only | `RequestExactly` | processing continues |
| `firstAvailable` only, with alternatives | `RequestFirstAvailable` | processing continues |
| both set | `RequestMalformed` | request change |
| `firstAvailable` only, no alternatives | `RequestMalformed` | request change |
| neither set | `RequestMalformed` | request change, or a later Kueue version |

The last row is two causes these types cannot separate. `DeviceRequest` declares `exactly` and
`firstAvailable` as a required one-of, so a request carrying neither is one the API server would
have refused, and a request in a shape a later API version adds decodes through this version's
types with both fields nil as well. The classifier reads one value in both cases and refuses it,
which is the conservative answer to either. Telling them apart would take a raw or unstructured
decode, which this design does not do and which the charge does not need.

`ClaimReferenceUnknown` and `RequestUnknown` are the zero values and are never a classification
outcome. They exist so an unset kind fails closed wherever it is consumed, which is a testable
property rather than a convention: the tests pass each zero value through the quota path and the
MultiKueue check and assert a refusal, and putting the fail-closed branch back to a fall-through
turns them red.

The second reads the gate and the mapping and decides whether the request may be charged. What
that decision is recorded as, and how a Workload gets out of it, is the parent path's rather than
this gate's.

#### Relationship with Kubernetes ResourceQuota

This is a Kueue-specific quota policy and does not change Kubernetes `ResourceQuota`. For the
`ExactCount` alternatives this Alpha covers, core `ResourceQuota` takes, within each top-level
`firstAvailable`, the largest device count among the alternatives that name a given DeviceClass, and
adds those per-class maxima across top-level requests; it does not sum the alternatives of one
request. The behaviour compared here is the one the quota evaluator implements in
`pkg/quota/v1/evaluator/core/resource_claims.go`, which keeps a per-DeviceClass maximum across the
alternatives, rather than the sentence in KEP-4816 saying a user must have quota for each
`DeviceSubRequest`, which reads as though the alternatives were summed. Kueue applies the same per-request maximum, but after mapping
DeviceClasses into logical resources, so a mapping that sends several DeviceClasses to one logical
resource collapses charges that core `ResourceQuota` keeps apart. The two policies part company
outside that scope: core `ResourceQuota` gives allocation mode `All` a finite worst-case charge
from `AllocationResultsMaxSize`, while Kueue refuses a non-admin `All` in this Alpha rather than
charging it. Namespace `ResourceQuota` and Kueue `ClusterQueue` quota may both apply
to the same workload; this design does not let a user use `firstAvailable` to bypass namespace
`ResourceQuota`.

#### Feature gate lifecycle, version skew, and observability

- Rollback: the gate is Alpha, default off. Disabling it returns a new `firstAvailable` workload to
  the current rejection. The MultiKueue rejection below is not conditioned on the gate, so a
  workload that reserved quota under it and is still waiting for its admission check is refused
  remote dispatch on the same terms once the gate goes off. Rolling the binary back is a different
  thing and this cannot promise it: a version from before this feature carries no such rejection,
  and does not reach the `Pending` path that would refuse the workload either, because it already
  has an admission recorded. For Alpha a downgrade wants those workloads released or deactivated
  first, and validating one that does not is a Beta criterion. A workload that has reserved quota keeps the accounting recorded in its
  status: `Info.rebuildTotalRequests` reads from the admission once `status.admission` is set, which
  a reservation does, rather than recomputing from the PodSets. So a controller restart or downgrade
  does not lose the envelope, and the case of a workload holding a reservation while it waits for an
  AdmissionCheck settles the same way an admitted one does.
- Version skew: the gate requires `KueueDRAIntegration`, and a configuration enabling it without the
  parent is refused by configuration validation rather than running a prioritized-list charge on a
  path the parent does not provide. It also requires that a cluster has not
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
  check refuses a local workload whose gate-independent request-kind classification contains
  `RequestFirstAvailable`, before any remote Workload or Job is created, so the existing
  rejected-check path deactivates the workload and releases its reservation instead of dispatching
  an envelope the worker would not reproduce. A template that cannot be read yet stays retryable
  rather than becoming a rejection. A template deleted and recreated under the same name between the
  reservation and the check is read as it stands then, which is the boundary in
  [Reservation-to-claim identity boundary](#reservation-to-claim-identity-boundary) rather than
  something this check closes.

#### Reservation-to-claim identity boundary

For Alpha, the quota upper-bound guarantee is defined for a fixed `ResourceClaimTemplate` identity
and quota-affecting `ResourceClaimSpec`, from the time Kueue computes the reservation until the
generated `ResourceClaim` is created.

Kueue does not bind a reservation to a template that is deleted and recreated under the same name in
that interval, so the generated claim can differ from the spec that was charged. The gap is
inherited from the existing `Exactly` path rather than introduced by `firstAvailable`, and it is
adjacent to the envelope rather than part of it: the envelope answers what the largest alternative
of a given spec costs, and this answers whether that spec is the one Kubernetes instantiates.

The prioritized-list gate stays Alpha and off by default while the boundary is open, and the binding
is re-evaluated before Beta. Closing it is not a second `Get` before reserving or before unsuspending,
since either leaves the same window open between the read and claim creation, and it is not a rewrite
of `status.admission` when a template moves, since a reservation is a point-in-time decision and
re-accounting a reserved Workload on every dependency event is the churn the queue design refuses
elsewhere. What would close it is recording the template identity and a digest of the
quota-affecting spec with the reservation, comparing them against the generated claim before the Pod
can be scheduled, and releasing the reservation for a fresh admission when they differ rather than
editing the numbers already recorded. That needs status provenance, a watch on the generated claims
and a pre-scheduling barrier, which is its own design rather than an extension of this one.

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
A logical resource from `deviceClassMappings.name` that the ClusterQueue covers lands in the workload's admitted
`ResourceUsage`, and the AFS penalty accounting mechanism applies weights to all resources without filtering for DRA.
One the ClusterQueue does not declare is skipped from the usage count under `IgnoreUndeclared`, so it does not enter
admitted usage or AFS either, and an envelope-touched resource is filtered on the same terms. This means
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

DRA workloads are supported with MultiKueue through the existing workload synchronization mechanism. ResourceClaimTemplates must be deployed on worker clusters by users; they are not automatically synced. Count-based `firstAvailable` requests are excluded while `KueueDRAIntegrationPrioritizedList` is
Alpha; they remain rejected rather than dispatched, so a manager and a worker cannot charge
different envelopes for the same workload.

### Test Plan

[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.

#### Prerequisite testing updates

<!--
Based on reviewers feedback describe what additional tests need to be added prior
implementing this enhancement to ensure the enhancements have also solid foundations.
-->

The envelope is computed on a path several existing defects run through, and each of them can turn
a correct one into a wrong charge before it is admitted. They are shared with the `Exactly` path
rather than introduced here, so they are prerequisites rather than work this design adds, but the
prioritized-list implementation does not ship while any of these failure modes is still reachable.
What closes one is a merged fix or an equivalent guard, not the state of the issue tracking it, so
the table names both. Every row needs its own regression with a `firstAvailable` envelope on the
resource, which is the shape with nothing in the pod spec to fall back on.

| Invariant | Tracked by | Closed by | State |
| --- | --- | --- | --- |
| `pods` does not overwrite a logical charge | [#13988](https://github.com/kubernetes-sigs/kueue/issues/13988) | [#13989](https://github.com/kubernetes-sigs/kueue/pull/13989) | satisfied |
| a transformation output is not spent as though it were a request | [#13990](https://github.com/kubernetes-sigs/kueue/issues/13990), [#13985](https://github.com/kubernetes-sigs/kueue/issues/13985), [#13992](https://github.com/kubernetes-sigs/kueue/issues/13992) | [#13986](https://github.com/kubernetes-sigs/kueue/pull/13986) | satisfied |
| a conversion past `int64` is reported rather than wrapped | [#13998](https://github.com/kubernetes-sigs/kueue/issues/13998) | [#14042](https://github.com/kubernetes-sigs/kueue/pull/14042) | satisfied |
| an extended charge is aggregated under its own name before it is mapped | | [#14200](https://github.com/kubernetes-sigs/kueue/pull/14200) | satisfied |
| a negative extended request does not cancel a charge | | [#14367](https://github.com/kubernetes-sigs/kueue/pull/14367) | satisfied |
| a relaxed LimitRange requeues what it blocked | | [#14967](https://github.com/kubernetes-sigs/kueue/pull/14967) | satisfied |
| a backoff requeue keeps the preprocessed charge | [#13930](https://github.com/kubernetes-sigs/kueue/issues/13930) | [#13967](https://github.com/kubernetes-sigs/kueue/pull/13967) | open |
| one queueing point owns the charge | [#14035](https://github.com/kubernetes-sigs/kueue/issues/14035) | [#14791](https://github.com/kubernetes-sigs/kueue/pull/14791), rebased onto the outcome protocol tracked separately | open |
| a charge and the spec it came from move together | [#14535](https://github.com/kubernetes-sigs/kueue/issues/14535) | | open |
| a rejection can be recovered from | [#13969](https://github.com/kubernetes-sigs/kueue/issues/13969) | | open |
| the shared request reader does not mutate what it reads | | [#14407](https://github.com/kubernetes-sigs/kueue/pull/14407) | open |
| one immutable effective resource view rather than an adjusted copy per caller | [#14964](https://github.com/kubernetes-sigs/kueue/issues/14964) | [#15095](https://github.com/kubernetes-sigs/kueue/pull/15095) in part | open |
| the CEL environment is not narrower than the apiserver's | [#14372](https://github.com/kubernetes-sigs/kueue/issues/14372) | | open |

The Alpha composition limit in [Scope](#scope) takes the rows below off that critical path, on three
conditions rather than by assertion. Each of the three is required above, and the deferral is only
as good as they are.

The detector reads the same effective-resource view the merge reads, so a contribution the merge
would charge is one the detector sees, and one neither sees reaches no envelope to corrupt; that
view is the immutable-view row in the first table. It runs before any step that removes what it is
looking for, which is why the builder is required to read the requests ahead of extended-resource
replacement. And it decides by presence rather than by value, so a contribution whose number a
defect below gets wrong is still a contribution it refuses.

Given those three, each row below reaches an envelope only through something this Alpha has already
refused. Fail any one of them and these rows belong in the first table, and lifting the limit puts
them there regardless of which mechanism the detector ends up using.

| Invariant | Tracked by | Closed by | State |
| --- | --- | --- | --- |
| a negative container or pod-level request does not spend another charge under the same name | [#14015](https://github.com/kubernetes-sigs/kueue/issues/14015) | [#14047](https://github.com/kubernetes-sigs/kueue/pull/14047) | open |
| an invalid pod overhead neither enters accounting nor cancels a charge | [#13991](https://github.com/kubernetes-sigs/kueue/issues/13991) | [#13995](https://github.com/kubernetes-sigs/kueue/pull/13995), or the equivalent normalization if [#14047](https://github.com/kubernetes-sigs/kueue/pull/14047) absorbs it | open |
| a transformation multiplier is read from the requests | [#14007](https://github.com/kubernetes-sigs/kueue/issues/14007) | [#14048](https://github.com/kubernetes-sigs/kueue/pull/14048) | open |
| a replacement removes only what it replaces | [#14004](https://github.com/kubernetes-sigs/kueue/issues/14004) | [#14154](https://github.com/kubernetes-sigs/kueue/pull/14154) | open |
| the overhead charged is the larger of the two sources that define it | | [#14289](https://github.com/kubernetes-sigs/kueue/pull/14289) | open |
| a source-backed class is not silently left uncharged | [#14249](https://github.com/kubernetes-sigs/kueue/issues/14249) | | open |

These two tables are the canonical list. Keeping a second copy in prose is how the two drifted
apart before, so the detail that does not fit a row belongs here rather than in a parallel list.

Two groups of rows are not independent of each other, and merging them one at a time in whatever
order they become ready is how an invariant gets resolved away in a conflict. The queue lifecycle
rows are one protocol rather than five separate fixes, and the shared request-accounting rows are an
order rather than a chain of consequences: one immutable view of a Pod's requests, which the first
table gates, has to exist before a multiplier can be read from it or a replacement can remove what
it replaced, which the second table defers.
Which pull requests carry which row, in what sequence, and what each still has to take back is
tracked with the implementation in
[#14130](https://github.com/kubernetes-sigs/kueue/pull/14130) rather than here, because it moves
every time one of them merges.

[#14249](https://github.com/kubernetes-sigs/kueue/issues/14249) is the row that needs a policy
rather than only a fix, because the failure is silence. A source-backed device class is charged
nothing when the ResourceSlice API is absent. Refusing source-backed alternatives alone would not
reach it, since one claim can carry a count-based `firstAvailable` request and a source-backed
`Exactly` request and charging the first correctly does not repair the second being charged zero;
the composition limit refuses that Workload as a whole, which is why the row sits in the second
table. The policy still has to be settled before the limit is lifted, and three answers are
available. A manager that fails to start turns a missing optional
API into an outage for every workload, including the ones that never touch DRA. Treating it as a
transient error retries a condition that does not clear on its own. This design takes the third: the
Workload fails closed rather than being admitted without the charge it cannot value. Falling back to
a device count is not among the options, because a counter or capacity charge is denominated in
units a device count cannot stand in for. How that rejection is recorded, and what lifts it, is the
parent DRA path's to define along with the rest of its outcome handling.

Each fix regresses against the path it lives on, which is the `Exactly` charge those paths carry
today; a fix cannot test a shape the feature it precedes has not introduced. The implementation
repeats each scenario with a `firstAvailable` envelope, which is the shape with nothing in the pod
spec to fall back on, and so shows the new charge crossing the repaired path rather than only that
the path was repaired.

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
Package coverage this scope touches, measured on 09/02/2026 with `go test -cover`:

- pkg/cache/queue: 83.3%
- pkg/config: 89.9%
- pkg/controller/core: 53.8%
- pkg/dra: 66.7%
- pkg/resources: not measured separately; the paths this scope touches are covered by the Amount
  and formatter tests
- pkg/workload: 63.2%

The per-file figures this section carried before were taken in September 2025 and no longer
describe the tree. The scope also reaches the queue manager, the workload request builder, the
workload controller, the MultiKueue admission check, and feature and configuration validation, and
each of those is where the accounting a `firstAvailable` charge passes through actually lives.

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
- Late DeviceClass creation: Testing workload inadmissibility when DeviceClass does not exist. This
  covers the extended-resource path, where the class is resolved by `extendedResourceName`, and the
  `Exactly` path that compiles class selectors. It does not extend to count-based `firstAvailable`,
  which reads no DeviceClass object, so a missing class there is neither a rejection nor a
  dependency this controller waits on
- CEL validation: Testing CEL compilation errors, evaluation against ResourceSlice devices, and rejection
  of workloads with unsatisfiable CEL selectors
- DRA disabled rejection: Testing that workloads with ResourceClaimTemplates or ResourceClaims are
  rejected as inadmissible when `KueueDRAIntegration` is off and `KueueDRARejectWorkloadsWhenDRADisabled` is on,
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
- Prioritized list envelope properties, stated over the alternatives rather than case by case: the
  envelope does not depend on the order the alternatives are written in, raising any one
  alternative's count never lowers the charge, and every alternative's own charge is at most the
  envelope, which is what makes the bound hold whichever one kube-scheduler selects
- Prioritized list rejection: unmapped alternative, `All` and unknown allocation modes, a
  counter-backed or capacity-backed alternative, and the feature gate disabled all marked
  inadmissible
- Prioritized list against `excludeResourcePrefixes`: an excluded ordinary Pod resource is dropped
  while a logical resource an explicit `deviceClassMappings` entry synthesizes is still charged,
  for `Exactly` and `firstAvailable` alike
- Prioritized list capacity: an alternative setting `capacity` on the subrequest under a
  source-less mapping charged its declared count, and charged the same as the alternative that
  omits the field
- Prioritized list representability against the spec count: a charge that overflows at
  `spec.podSets[].count` and would fit at `minCount`, or at the count left after a reclaim, is
  refused rather than admitted and scaled down later
- Prioritized list representability: a per-Pod sum, a PodSet multiplication, and a sum across two
  PodSets that each fit on their own,
  none of which the representation in the tree can hold exactly. The boundary values are read off
  that representation rather than written into the test, which is what keeps the test meaning the
  same across the change described above, since what `resources.Amount` holds exactly is itself
  changing and a boundary written into the test would be wrong after it does. Each of these marks
  the workload inadmissible instead of
  clamping. Scaling a PodSet down stays exact, because only an aggregate that is exactly the
  per-Pod value times the count is admitted in the first place
- Prioritized list composition on a charged key: every contribution the composition table names,
  landing on the resource an envelope is charged on, makes the Workload inadmissible in this Alpha
  rather than merging into the envelope. Driven with a positive operand and with the
  negative one from [#13985](https://github.com/kubernetes-sigs/kueue/issues/13985), where an
  envelope of `8` with a generated `-3` under that name was charged `5`. A transformation producing
  a negative output is not on its own the refusal; what is refused is the contribution reaching the
  merge. The same Workload without the overlap is admitted, so the refusal is on the composition
  rather than on the envelope, and an `Exactly` charge on that resource is not an overlap and stays
  merged and checked
- Prioritized list a DRA-backed extended resource shares the key: an extended resource the mapping
  makes DRA-backed, on the same logical resource an envelope is charged on, is admitted and added
  exactly rather than treated as an overlap, since replacement leaves a DRA charge there and not an
  ordinary request
- Prioritized list an overlap survives replacement: the same extended-resource key carrying a
  residual ordinary request, `spec.overhead` or a transformation output is refused, driven so that
  the check runs where replacement would otherwise have removed the evidence. A build that checks
  after replacement admits it and fails here
- Prioritized list a source-backed `Exactly` in the same Workload: a non-admin one refuses the whole
  Workload beside an envelope, driven with the two in one `ResourceClaimTemplate`, in two templates
  one PodSet references, and in two PodSets, since a check looking only within a claim passes the
  last two. One carrying `adminAccess` keeps its zero charge and does not refuse, which is the
  exemption the classifier applies before allocation mode. Removing the `source` from the mapping
  and restarting makes the same Workload supported
- Prioritized list a composition rejection waits for its own event: a Workload refused for each row
  of the composition table is cleared by the event that row names, driven once per row. An
  implementation that requeues on a Workload update alone passes the first two rows and leaves the
  `LimitRange` and RuntimeClass ones refused while the object that caused them is edited, which is
  the failure the table is written against
- Prioritized list with a logical resource named `cpu`: the charge round-trips through the
  milli-unit convention rather than being read as absolute units
- Prioritized list flavors: alternatives mapping to one logical resource are admitted and render
  the selector that resource's flavor implies; alternatives mapping to more than one are rejected
  while the claim is read, rather than reaching flavor assignment as a set of resources nothing can
  tell apart from ones that are all needed
- Prioritized list under a relaxed LimitRange: the requeue
  [#14967](https://github.com/kubernetes-sigs/kueue/pull/14967) added for a pending Workload
  admits the Workload on the charge its current revision produces rather than on an earlier one,
  and a run in which the requests changed while it was pending fails if the earlier total is used
- Prioritized list template lifecycle: a Workload refused for a template that does not exist is
  evaluated again when the template is created; deleting a referenced template enqueues the
  Workloads built on it, and once that delete has been observed no admission or preemption uses the
  charge computed from the template that is gone; replacement of the entry is what is eventual. The
  interval before the delete is observed is the open window named in the safety argument, so a test
  asserting there is no such interval would be asserting something no mechanism here provides. A template deleted and
  recreated under the same name has its charge replaced from the new spec, and a template of the
  same name in another namespace wakes nothing, since the index is keyed by namespace and name
- Prioritized list a stale entry is not acted on: an entry built for one revision whose inputs
  change before the scheduling cycle acts on it issues no preemption, no migration and no admission.
  Driven once per way the inputs move, since they move through different fields: the PodSet request,
  the referenced template deleted and recreated under the same name, the mapping, and the gate. The
  Workload is then admitted on the charge its current revision produces
- Prioritized list crash windows do not resurrect a charge: the manager is killed part way through
  the transition and again after it, and in both the restarted manager reaches the same verdict by
  building again rather than by inserting what it held
- Prioritized list a requeue does not preserve across a revision: a DRA entry is popped, an input
  changes, and the scheduler requeues it. The old totals are not carried onto the new object, and the
  Workload is rebuilt. Driven once per input that leaves `Generation` untouched, since an input
  that moves it is already covered by the generation comparison every path makes
- Prioritized list the range follows the representation: the boundary cases read the largest total
  the representation in the tree holds exactly, and the first one past it, from that representation
  rather than from a constant. Changing the representation moves the boundary and the cases follow
  it; a run whose expectations are spelled as literals fails, which is what keeps the exactness
  contract the thing under test rather than one build's numbers
- Prioritized list one resource view per pass: the DRA pass and the build read the same adjusted
  resources, and a test that hands them independently adjusted copies fails rather than producing an
  entry that is internally consistent over two views of the same Workload
- Prioritized list identity is semantic: reordering `spec.podSets` or a PodSet's `resourceClaims`
  without changing any of them leaves the charge and the disposition as they were, so a reorder that
  means nothing does not refuse a Workload or move what it is charged
- Prioritized list reference multiplicity: a template referenced twice in one PodSet is charged
  twice rather than once, moving the same template to a different PodSet changes what the PodSet
  count multiplies, and exchanging two equal-total templates leaves the total alone while each
  Workload still answers for the template it names
- Prioritized list union shapes: every form of the `PodResourceClaim` union, which is a template
  name, a claim name, both, neither, and either set to the empty string, and every form of the
  `DeviceRequest` union, which is `exactly` alone, `firstAvailable` alone, both, neither, and a
  non-nil `firstAvailable` with no alternatives. Each is frozen to its `ClaimReferenceKind` or
  `RequestKind` and to the verdict it produces
- Prioritized list unknown enum values fail closed: the zero value of `ClaimReferenceKind` and
  `RequestKind` is refused by the quota path and by the MultiKueue check, and turning the refusal
  into a fall-through turns the tests red
- Prioritized list gate isolation: a Workload whose requests are all `Exactly` is charged the same
  with the gate on as with it off, so turning an Alpha gate on does not move accounting for a user
  who has no `firstAvailable` at all
- Prioritized list gate composition: a configuration enabling the child gate with
  `KueueDRAIntegration` off is refused by configuration validation, so no manager runs with the
  child gate on and the parent off, and no partial path exists for a Workload to reach
- Prioritized list omitted subrequest fields: an alternative with `allocationMode` unset is charged
  as `ExactCount`, and one with `count` unset is charged as one, which are the defaults the API
  documents rather than a zero charge
- Prioritized list references within one PodSet: two `PodResourceClaim` entries naming the same
  template are two claims rather than one, and a PodSet referencing one template that resolves and
  one that does not produces no charge rather than the first one's. The same for one that resolves
  and one that is unsupported
- Prioritized list lifecycle: a Workload that becomes unrepresentable on update has its earlier
  queue entry removed or replaced rather than left holding the smaller total it was queued on, one
  that becomes representable again is evaluated afresh rather than keeping the old verdict, and the
  charge it is admitted on is the one its current revision produces across a backoff requeue, an
  inflight requeue that sees a newer generation, and the gate being turned on and then off again
- Prioritized list partial admission: the count in `status.admission.podSetAssignments[]` is the one
  an admitted charge is read back against, so a Workload rebuilt from its status carries the total
  it was admitted on rather than one recomputed from the PodSets
- Prioritized list under MultiKueue: the admission check rejects before any remote Workload or Job
  exists, with the gate on and with it off since the check does not read it, and a transient
  template read error is retried instead of rejected. Asserting only that the check ends up failed
  would pass a build that created the remote objects first
- Prioritized list under `quotaCheckStrategy: IgnoreUndeclared`: an envelope on a resource the target
  ClusterQueue does not declare is left out of the quota check on the same terms as an `Exactly` DRA
  charge and an ordinary request on that name, so the strategy reads as one rule rather than one per
  claim shape. Driven with one logical resource reached through both shapes in turn, since a run that
  exercises only one would pass a build that filtered whichever it happened to recognise

#### E2E Test

Use existing dra-example-driver or Kubernetes test driver for e2e testing. For partitionable
devices, integration tests create ResourceSlice objects directly via the API, which follows the
same pattern as upstream K8s integration tests in `test/integration/dra/`. The driver itself gained
`SharedCounters` support in
[kubernetes-sigs/dra-example-driver#150](https://github.com/kubernetes-sigs/dra-example-driver/pull/150),
merged in May 2026, so an e2e that wants a published slice rather than a hand-written one has a
driver to get it from.

For prioritized lists, an e2e test has to make the fallback happen rather than allow it. A run
where every Pod picked the first alternative passes a bound that was never exercised and tests
nothing the single-alternative case does not. The fixture puts one preferred-class device and two
fallback-class devices on a node, and two Pods share a template asking for one preferred or two
fallback. The first Pod takes the preferred device; the second finds it allocated and has to take
both fallback ones. The allocation result records the selected alternative as
`<main request>/<subrequest>`, so the test reads it per claim and asserts that one Pod selected each
alternative, that the admitted envelope is `2 x 2`, that the realized total is `1 + 2`, and that the
realized total is at most the admitted one. Where a fixture cannot pin one Pod to each, it has to
assert at least that two distinct subrequest names were selected, or it is a claim execution test
rather than a prioritized-list one.

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
- rejection of counter-backed and capacity-backed alternatives, and of `All`, unknown allocation
  modes, and unmapped DeviceClasses
- `excludeResourcePrefixes` filtering the Pod's own requests while a logical resource an explicit
  mapping synthesizes stays chargeable, the same for both request shapes
- every alternative of one `firstAvailable` request mapping to the same logical resource, with a
  request whose alternatives resolve to more than one rejected while the claim is read, under
  a manager restart or a request change rather than as a request the Workload alone has to change.
  The envelope then
  covers one resource, which takes one flavor, so nothing downstream has to be told the request was
  a prioritized list
- request selectors compiled with the DRA CEL feature environment the supported Kubernetes API
  uses, so a selector the apiserver accepted is not refused here. This does not close
  [#14372](https://github.com/kubernetes-sigs/kueue/issues/14372), which is about the environment
  the shared compiler is built with and stays open in the prerequisite table
- a shared classifier in two stages, with table-driven tests freezing each:
  - the request kind describes the persisted API shape alone and reads no feature gate, no
    `deviceClassMappings` entry and no cluster state, so the same object classifies the same way
    whatever the cluster is configured to do;
  - the quota path combines that kind with the mapping and the gate to reach a disposition;
  - the MultiKueue check consumes the kind alone and refuses `RequestFirstAvailable` in Alpha
    whether the Kueue gate is on or off, so a rollback cannot take the refusal with it.

  (agreement with the feasibility path is a Beta criterion)
- preprocessing keeping the logical-resource names a `firstAvailable` request reached, carried
  through the queue and requeue paths with the charge, since a merged `ResourceList` alone cannot
  say which key an envelope is on
- the union of those names read across the whole workload, so a contribution in a PodSet with no
  alternatives is seen on a key another PodSet's envelope charges: an ordinary request,
  `spec.overhead` or a transformation output there makes the workload inadmissible in this Alpha,
  and an `Exactly` charge there is charged and checked against the same total. Summing by resource
  name before flavors are assigned is stricter than
  the accounting that follows, which keys assigned usage by flavor and resource: two PodSets close
  to the range on one resource but bound for different flavors are refused here and would have been
  charged apart later. That is intended rather than exact. The pending and unassigned
  representations carry no flavor dimension and have to stay valid on their own
- a charge no boundary can represent exactly makes the workload inadmissible instead of being
  saturated, at the envelope and at every shared aggregation boundary it passes through. The
  admissible range is read off the representation rather than fixed here, since what
  `resources.Amount` holds exactly is itself changing and a criterion pinned to today's value would
  be wrong after it does. Mixing `Exactly` and `firstAvailable` can overflow a boundary neither
  reaches alone, so this cannot wait for Beta
- a rejection carrying the class that says what clears it: a request rejection re-evaluated when
  the request definition changes, in the PodSet or in the template it names; a configuration or
  capability rejection lifted once the manager runs with the changed configuration; a dependency
  rejection lifted when an object the
  Workload names, or one that applies to its namespace, appears or changes; and a read failure retried rather than
  recorded as a verdict. A Workload carrying more than one becomes admissible only when the last
  of them clears
- that refusal covering the negative case which motivates it, since `FloorToZero` runs after the
  merge and a negative request on a key an alternative charges would otherwise subtract from the
  envelope and leave a zero that reads as nothing having been asked for
- every failure mode in the first prerequisite table answered by a merged fix or an equivalent guard
  in this implementation, each with a regression, since an issue can be closed as a duplicate or
  left open after its fix lands and neither says whether the envelope still survives the path. The
  second table is what the composition limit defers, and pulling a row back here is what would
  quietly restore the broad Alpha
- integration and e2e tests

Parent prerequisites. These are properties this gate depends on and does not design. How
`KueueDRAIntegration` provides them is its own. This KEP does not choose what carries a revision's
identity, a condition schema, a constructor signature, a field manager, or an invalidation
primitive, so a revision token, a UID set, an immutable snapshot, a cache epoch and a fingerprint
are all open to it.

- an unchanged preprocessing result survives a backoff or a requeue, so a Workload is not
  scheduled on a total its inputs no longer produce
  ([#13930](https://github.com/kubernetes-sigs/kueue/issues/13930))
- a Workload revision that changes what is charged, once observed, invalidates the earlier result
  rather than being acted on with it
  ([#14535](https://github.com/kubernetes-sigs/kueue/issues/14535))
- a recomputation that fails leaves no schedulable entry built from the result it superseded
  ([#14035](https://github.com/kubernetes-sigs/kueue/issues/14035))
- a deterministic rejection is recoverable, reaching the Workload as an outcome some later change
  can clear rather than as an error retried forever
  ([#13969](https://github.com/kubernetes-sigs/kueue/issues/13969))
- one queueing point owns the result, so no path installs a schedulable entry from an unchecked or
  superseded one ([#14035](https://github.com/kubernetes-sigs/kueue/issues/14035))

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
- re-evaluate binding the admission-time DRA charge to the `ResourceClaimSpec` actually
  instantiated, including a same-name `ResourceClaimTemplate` deleted and recreated between the
  reservation and claim creation
- re-evaluate source-backed alternatives, charging each through its source path and taking the
  component-wise maximum of the resulting vectors
- support non-DRA effective requests, effective overhead and transformation outputs on
  envelope-touched resources, once the shared accounting invariants in the first prerequisite table
  hold, naming which of them become supported rather than lifting the limit as a whole
- support a mixed source-backed `Exactly` request, once an unavailable source fails closed rather
  than contributing zero

#### GA

##### KueueDRAIntegration

- the feature gate in stable
- TAS + DRA integration and testing
- validate the AdminAccess zero-charge policy against production feedback
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
  when the `KueueDRAIntegration` feature gate is disabled to prevent silent quota bypass
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

### Refusing a request whose mapped resource an excluded prefix covers

Restricting only what this gate adds, by rejecting a `firstAvailable` request whose logical resource
an `excludeResourcePrefixes` entry covers, was considered and rejected. It fails closed without
settling the older question on everyone else's behalf, but it gives the same `deviceClassMappings`
entry different meanings for `Exactly` and `firstAvailable`, and the party who could fix it is not
the party told about it. Refusing the configuration at startup is the other consistent answer, at
the price of failing a startup over mappings this feature never touches, and it is the one a
follow-up issue should weigh across every mapping rather than one request shape.

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
