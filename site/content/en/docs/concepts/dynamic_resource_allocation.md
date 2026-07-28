---
title: "Dynamic Resource Allocation"
date: 2026-03-22
weight: 7
description: >
  Quota management for workloads using Kubernetes Dynamic Resource Allocation (DRA).
---

{{% alert title="Warning" color="warning" %}}
In Kueue 0.18, the DRA feature gates were renamed to avoid conflicts with upstream
Kubernetes feature gates: `DynamicResourceAllocation` is now `KueueDRAIntegration`,
and `DRAExtendedResources` is now `KueueDRAIntegrationExtendedResource`.
{{% /alert %}}

## Dynamic Resource Allocation

[Dynamic Resource Allocation (DRA)](https://kubernetes.io/docs/concepts/scheduling-eviction/dynamic-resource-allocation/)
is a Kubernetes API for requesting and managing hardware devices such as GPUs,
FPGAs, and network adapters. Kueue can account for DRA devices in quota
management through two paths:

1. **ResourceClaimTemplate path**: Pods explicitly reference a
   `ResourceClaimTemplate` that specifies a device request. Kueue maps each
   `DeviceClass` referenced by the claim to a logical resource name using
   `deviceClassMappings` in the Kueue Configuration.

2. **Extended resource path**: Pods request DRA devices using the traditional
   `resources.requests` syntax (e.g., `nvidia.com/gpu: 1`). When the
   Kubernetes `DeviceClass` has an `extendedResourceName` field set
   ([KEP-5004](https://github.com/kubernetes/enhancements/issues/5004)),
   the kube-scheduler automatically creates `ResourceClaim` objects from
   these requests. Kueue detects this and avoids double counting.

{{% alert title="Note" color="info" %}}
DRA support in Kueue requires a Kubernetes cluster running **version 1.34 or
later** where the DRA API (`resource.k8s.io`) is v1.
{{% /alert %}}

**Which path should I use?** If your workloads already use `resources.requests`
for devices (e.g., `nvidia.com/gpu: 1`), use the extended resource path. If
your workloads explicitly create `ResourceClaimTemplate` objects, use the
ResourceClaimTemplate path.

## How the ResourceClaimTemplate path works

{{< feature-state state="beta" for_version="v0.18" >}}

When a Pod references a `ResourceClaimTemplate`, Kueue reads the
`deviceClassName` from the template's `exactly` field and looks it up in
`deviceClassMappings`. This mapping tells Kueue which logical resource name
to charge quota against. The number of units charged is determined by the
`count` field in the device request (default 1).

Only the `ExactCount` allocation mode is supported. The
`All` allocation mode is not supported.

For setup instructions, see
[Set Up Dynamic Resource Allocation](/docs/tasks/manage/setup_dra).

## How the extended resource path works

{{< feature-state state="beta" for_version="v0.19" >}}

When a Pod requests an extended resource backed by DRA (e.g.,
`nvidia.com/gpu: 1`), the kube-scheduler auto-creates a `ResourceClaim`.
Kueue detects the matching `DeviceClass`, uses `extendedResourceName` as the
quota key, and drops the auto-created claim from accounting. This prevents
quota from being charged for both the `resources.requests` entry **and** the
auto-created claim, which would double count the same device. No
`deviceClassMappings` configuration is needed; the mapping is discovered
from the `DeviceClass` automatically.

This behavior is controlled by the `KueueDRAIntegrationExtendedResource`
feature gate, which is enabled by default since v0.19.

{{% alert title="Note" color="info" %}}
The extended resource path additionally requires the Kubernetes
`DRAExtendedResource` feature gate on kube-apiserver and kube-scheduler
(beta in Kubernetes 1.36).
{{% /alert %}}

## Path separation

The two paths are independent:
- **ResourceClaimTemplate path**: uses `deviceClassMappings` configuration.
- **Extended resource path**: uses auto-discovery from `DeviceClass` objects.

Do not configure the same `DeviceClass` in both paths for the same workload.
If overlap occurs, Kueue merges the resources using the `deviceClassMappings`
logical name as the quota key, which may result in incorrect quota accounting.

## Quota accounting

DRA resources are tracked in `ClusterQueue` quotas just like CPU or memory.
The administrator includes the DRA resource name in `coveredResources` and
sets a `nominalQuota`. Kueue supports three quota accounting modes:

- **Device count** (default): Charges the `count` value from the device
  request (default 1 when omitted). A `ClusterQueue` with `example.com/gpu: 8`
  allows up to 8 concurrent device allocations.
- **Counter-based**: Charges the device's `consumesCounters` value (e.g.,
  GPU memory). See [Counter-based quota](#counter-based-quota-for-partitionable-devices).
- **Capacity-based**: Charges the workload's `capacity.requests` value
  rounded per the device's `RequestPolicy`. See
  [Capacity-based quota](#capacity-based-quota-for-shared-devices-consumable-capacity).

## Admission and scheduling gap

There is a timing gap between Kueue admitting a workload (quota check) and
the kube-scheduler allocating the actual device. Kueue does not know which
specific device will be allocated — it only verifies that quota is available.

If the cluster state changes between these two steps (e.g., another system
consumes the device), the scheduler may fail to allocate. The
[WaitForPodsReady](/docs/tasks/manage/setup_wait_for_pods_ready/) feature
provides a safety net by evicting workloads that fail to become ready within
a configured timeout.

## MultiKueue

DRA workloads are supported with [MultiKueue](/docs/concepts/multikueue).
MultiKueue syncs the workload and its owning job to worker clusters, but
`ResourceClaimTemplate` and `DeviceClass` objects are not automatically
synced. These must be created on each worker cluster separately by the
cluster administrator.

## Counter-based quota for partitionable devices

{{< feature-state state="beta" for_version="v0.19" >}}

By default, Kueue tracks DRA quota by device count: each device request
charges `count` units regardless of the device's capacity. This means a
small GPU partition and a full GPU both count as "1 device", which does not
reflect the actual resource consumption.

Kueue can track quota using **counter values** published by DRA drivers
in `ResourceSlice` objects. This allows quota to reflect actual device
capacity (e.g., GPU memory) rather than device count.

This behavior is controlled by the `KueueDRAIntegrationPartitionableDevices`
feature gate, which is enabled by default since v0.19.

A `DeviceClass` uses either device-count quota (no `sources` configured) or
counter-based quota (with `sources`), not both. Kueue rejects configurations
that map the same `DeviceClass` to multiple resource names.

### How it works

1. The administrator configures a `sources` entry in `deviceClassMappings`
   that specifies which counter to track, which DRA driver to query, and
   a CEL expression to scope eligible devices.

2. When a workload is submitted, Kueue reads the `consumesCounters` field
   from the matching devices in `ResourceSlice` objects to determine the
   actual counter charge.

3. Kueue uses **conservative charging**: it takes the maximum
   `consumesCounters` value across all matched devices and multiplies by
   the request `count`. This ensures quota is not undercharged when
   different devices consume different amounts.

4. The `ClusterQueue` quota is set in counter units (e.g., `800Gi` for
   GPU memory) instead of device count.

### Prerequisites

- Kubernetes 1.35 or later with the `DRAPartitionableDevices` feature gate
  enabled (beta in Kubernetes 1.36).
- A DRA driver that publishes `consumesCounters` on devices in
  `ResourceSlice` objects.

For setup instructions, see
[Set Up Dynamic Resource Allocation](/docs/tasks/manage/setup_dra/#set-up-counter-based-quota-partitionable-devices).

## Counter-based vs capacity-based quota

Both modes track quota by actual resource consumption rather than device count,
but they serve different device types:

| | Counter-based (PD) | Capacity-based (CC) |
|---|---|---|
| **Device type** | Partitioned devices (e.g., NVIDIA MIG) | Shared devices (e.g., GPU time-slicing, MPS) |
| **Charge source** | Device's `consumesCounters` | Workload's `capacity.requests` |
| **Who decides consumption** | Driver (fixed per partition) | User (variable per workload) |
| **Upstream K8s feature** | KEP-4815 (`DRAPartitionableDevices`) | KEP-5075 (`DRAConsumableCapacity`) |

If your GPUs use hardware partitioning (MIG), use counter-based quota. If
your GPUs allow software-level sharing where workloads request variable
amounts of capacity, use capacity-based quota.

A cluster can use both modes simultaneously with different DeviceClasses
using the same DRA driver. One DeviceClass with counter sources for
partitioned devices and another with capacity sources for shared devices.
Counter and capacity sources cannot be mixed within the same DeviceClass
mapping.

## Capacity-based quota for shared devices (consumable capacity)

{{< feature-state state="alpha" for_version="v0.19" >}}

Some devices allow multiple workloads to share them simultaneously using
software-level sharing mechanisms such as GPU time-slicing or MPS. These
devices publish a `Capacity` field on each device in `ResourceSlice` objects
(defined by [KEP-5075](https://github.com/kubernetes/enhancements/issues/5075))
instead of using `consumesCounters`. Workloads specify how much capacity they
need via `capacity.requests` on the device request.

Kueue can track quota using these capacity dimensions so that the total
consumed capacity across all sharing workloads does not exceed the device's
published capacity.

This behavior is controlled by the `KueueDRAIntegrationConsumableCapacity`
feature gate (Alpha, disabled by default in v0.19).

A `DeviceClass` uses either device-count quota (no `sources`), counter-based
quota (with `counter` sources), or capacity-based quota (with `capacity`
sources). Counter and capacity sources cannot be mixed in the same mapping.

### How it works

1. The administrator configures a `capacity` source entry in
   `deviceClassMappings` that specifies which capacity dimension to track,
   which DRA driver to query, and a CEL expression to scope eligible devices.

2. When a workload is submitted, Kueue reads the workload's
   `capacity.requests` from the `ExactDeviceRequest` for the configured
   dimension. If `capacity.requests` is omitted, Kueue uses the device's
   `RequestPolicy.Default` or the full `Capacity.Value` as the charge.

3. Kueue rounds the request per the device's `RequestPolicy` (`ValidValues`
   or `ValidRange` with `Step`) to prevent quota gaming where a small request
   consumes more actual capacity after rounding by the kube-scheduler.

4. For each matched device, Kueue computes the charge independently using
   the device's own Default and policy, then takes the **maximum** across
   all devices. This ensures quota is never undercharged even if the
   `deviceSelector` matches heterogeneous devices.

5. The `ClusterQueue` quota is set in capacity units (e.g., `800Gi` for GPU
   memory) instead of device count.

### Prerequisites

- Kubernetes 1.36 or later with the `DRAConsumableCapacity` feature gate
  enabled (beta, enabled by default in Kubernetes 1.36).
- A DRA driver that publishes `Capacity` and `AllowMultipleAllocations` on
  devices in `ResourceSlice` objects.
- The `KueueDRAIntegrationConsumableCapacity` feature gate enabled in Kueue
  Configuration.

For setup instructions, see
[Set Up Dynamic Resource Allocation](/docs/tasks/manage/setup_dra/#set-up-capacity-based-quota-consumable-capacity).

## Limitations

The following limitations apply:

- **ResourceClaimTemplates only**: Only `ResourceClaimTemplate` references
  are supported. Direct `ResourceClaim` references in the Pod spec are not
  supported and will result in inadmissible workloads.
- **ExactCount allocation mode only**: Only device requests using `exactly`
  are supported. `FirstAvailable` device selection and the `All` allocation
  mode are not supported.
- **No device constraints or config**: Device `constraints` (MatchAttribute)
  and per-request `config` are not supported.
- **No AdminAccess**: Device requests with `adminAccess: true` are not
  supported.
- **No DRA + Topology Aware Scheduling (TAS)**: DRA resources are not
  accounted for in TAS capacity calculations. Using both features together
  may result in incorrect topology assignments for DRA devices.
- **No support for DRADeviceTaints or DRAPrioritizedLists**: These Kubernetes
  DRA features are not factored into Kueue's quota decisions.
