---
title: "Dynamic Resource Allocation"
date: 2026-03-22
weight: 7
description: >
  Quota management and topology-aware placement for workloads using Kubernetes
  Dynamic Resource Allocation (DRA).
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
`deviceClassMappings`. With `KueueDRAIntegrationPrioritizedList` enabled it
reads a request's `firstAvailable` alternatives as well; see the limitations
below for what that charges. This mapping tells Kueue which logical resource
name to charge quota against. The number of units charged is determined by the
`count` field in the device request (default 1).

Only the `ExactCount` allocation mode is supported. The
`All` allocation mode is not supported.

For setup instructions, see
[Set Up Dynamic Resource Allocation](/docs/tasks/manage/setup_dra).
For `firstAvailable` requests, see
[Set up `firstAvailable` requests](/docs/tasks/manage/setup_dra/#set-up-firstavailable-requests)
and [Using a `firstAvailable` request](/docs/tasks/run/dra/#using-a-firstavailable-request).

## How the extended resource path works

{{< feature-state state="beta" for_version="v0.19" >}}

When a Pod requests an extended resource backed by DRA (e.g.,
`nvidia.com/gpu: 1`), the kube-scheduler auto-creates a `ResourceClaim`.
Kueue detects the matching `DeviceClass`, uses `extendedResourceName` as the
quota key, and drops the auto-created claim from accounting. This prevents
quota from being charged for both the `resources.requests` entry **and** the
auto-created claim, which would double count the same device. No
`deviceClassMappings` configuration is needed; the mapping is discovered
from the `DeviceClass` automatically. A `deviceClassMappings` entry covering
that `DeviceClass` moves the charge to the mapping's logical name.

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

With [Topology-Aware Scheduling](/docs/concepts/topology_aware_scheduling/),
Kueue can also check before admission that a node has the devices a Pod needs.
See [Topology-Aware Scheduling with DRA](#topology-aware-scheduling-with-dra).

## Topology-Aware Scheduling with DRA

{{< feature-state state="alpha" for_version="v0.20" >}}
{{% alert title="Note" color="info" %}}
`KueueDRADeviceFeasibility` is currently an alpha feature and is disabled by default.

You can enable it by editing the `KueueDRADeviceFeasibility` feature gate. Refer to the
[Installation guide](/docs/installation/#change-the-feature-gates-configuration)
for instructions on configuring feature gates. It requires `KueueDRAIntegration`,
`TopologyAwareScheduling` and `TASNodeFeasibilityForAllLevels` to be enabled as well.
{{% /alert %}}

`KueueDRADeviceFeasibility` adds a device check to
[Topology-Aware Scheduling](/docs/concepts/topology_aware_scheduling/) (TAS): TAS
places each Pod only on nodes that can allocate the devices it requests. Quota limits
how many devices a `ClusterQueue` admits, not where they are, and without this feature
TAS cannot tell which nodes have them. What goes wrong depends on how a Pod requests
its devices:

- **`ResourceClaimTemplate` path**: the devices are not in the Pod's resource requests,
  so TAS places the Pod by its other resources alone. For example, a `ClusterQueue` with
  a quota of 8 GPUs on two nodes with 4 GPUs each admits a Pod whose
  `ResourceClaimTemplate` requests 6 GPUs: the quota allows it, but no node has 6. The
  Pod stays `Pending` while the workload holds the quota.
- **Extended resource path**: the Pod requests a resource such as `example.com/gpu`, and
  TAS looks for it in each node's allocatable, where a resource that only a
  `DeviceClass` provides never appears, so the workload fits on no node; see the
  warning in [When the check runs](#when-the-check-runs).

### How the device check works

1. A workload is assigned a flavor with a `topologyName` (a TAS flavor).
2. Because a Pod's `ResourceClaim` objects do not exist before admission, Kueue builds
   the claims each Pod will need: from its `ResourceClaimTemplate`s, or from the
   `DeviceClass` for an extended resource. For every node that the flavor and the Pod's
   other scheduling constraints allow, Kueue tries to allocate these claims, using the
   same allocator as the kube-scheduler.
3. Nodes where the allocation fails are dropped, and TAS places the Pods on the
   remaining nodes. For an extended resource, the check replaces TAS's lookup in node
   allocatable, except on nodes that advertise the resource through a device plugin.
   This works for any topology, including one whose lowest level is not
   `kubernetes.io/hostname`.
4. When no node is left, the workload stays pending, and its `QuotaReserved`
   condition message counts the nodes rejected for devices as `draNoFit`:

   ```
   couldn't assign flavors to pod set main: topology "dra-topology" doesn't allow to fit any of 1 pod(s). Total nodes: 2; excluded: draNoFit: 2
   ```

5. Kueue checks the workload again when a `ResourceSlice` or `DeviceClass` changes,
   or when a `ResourceClaim` releases its devices.

### When the check runs

| Workload | Device check |
|---|---|
| Assigned a flavor with a `topologyName`, on the `ResourceClaimTemplate` or extended resource path | Runs |
| Assigned a flavor without a `topologyName` | Does not run |
| In a `ClusterQueue` with a MultiKueue admission check | Runs on the worker cluster, where topology is assigned, not on the manager |
| In a `ClusterQueue` with a `ProvisioningRequest` admission check | Skipped on the first scheduling pass, which assigns no topology; runs on the second pass, after quota is reserved |

{{% alert title="Warning" color="warning" %}}
A workload on the extended resource path, such as one requesting
`example.com/gpu: 1`, requires the device check to be admitted to a TAS flavor. Without it,
TAS looks for the resource in each node's allocatable, where a resource that only a
`DeviceClass` provides never appears, so the workload fits on no node:
`excluded: resource "example.com/gpu": 2`. Nodes that advertise the resource through a
device plugin are counted as before.
{{% /alert %}}

### Prerequisites

- A `Topology` and a `ResourceFlavor` with `topologyName`, as described in
  [Setup Topology-Aware Scheduling](/docs/tasks/manage/setup_topology_aware_scheduling/).
- A DRA driver that publishes each node's devices in `ResourceSlice` objects.
- The `KueueDRADeviceFeasibility` feature gate enabled in Kueue Configuration. The
  gates it requires are enabled by default; if one of them is disabled, Kueue does not
  start and logs `conflicting feature gates detected`.

### Device taints

{{< feature-state state="alpha" for_version="v0.20" >}}
{{% alert title="Note" color="info" %}}
`KueueDRAIntegrationDeviceTaints` is currently an alpha feature and is disabled by
default. It requires `KueueDRADeviceFeasibility` to be enabled as well.
{{% /alert %}}

With this gate, the check skips devices with a `NoSchedule` or `NoExecute`
[device taint](https://kubernetes.io/docs/concepts/resource-management/dynamic-resource-allocation/device-taints/)
that the request does not tolerate, whether a DRA driver publishes the taint in a
`ResourceSlice` or an administrator applies it with a `DeviceTaintRule`. `None` taints
are ignored. A change to a `DeviceTaintRule` makes Kueue check rejected workloads again.

Kueue reads `DeviceTaintRule` objects only from Kubernetes 1.37 onwards, which serves them as
`resource.k8s.io/v1`; on earlier versions it ignores taints from rules. With this gate
disabled, it ignores all device taints. In both cases Kueue can admit a workload onto
tainted devices that the kube-scheduler then refuses, so enable this gate together
with `KueueDRADeviceFeasibility`.

{{% alert title="Warning" color="warning" %}}
The check runs only before admission. When a `NoExecute` taint is added to devices
that running Pods use, the Kubernetes eviction controller can delete those Pods, and
their replacements stay `Pending`, while Kueue keeps the workload admitted and its
quota reserved. Device taints do not trigger node replacement the way
[`TASReplaceNodeOnNodeTaints`](/docs/concepts/topology_aware_scheduling/#replace-node-on-node-taints)
does for node taints. To requeue such workloads, enable
[WaitForPodsReady](/docs/tasks/manage/setup_wait_for_pods_ready/) with a
`recoveryTimeout`; the check then keeps them pending until the taint is removed.
{{% /alert %}}

### Limitations of the check

- **One Pod per node**: the check asks whether a node can serve one Pod of the
  PodSet, not how many. Kueue can place more Pods on a node than it has devices
  for, and the Pods that do not get a device stay `Pending`.
- **No release on preemption**: devices held by workloads that Kueue would preempt
  are not freed in the check, so preemption cannot make a workload fit on devices.
- **Kubernetes DRA feature gates are read from the Kueue process**: the check follows
  the gates of the Kubernetes version Kueue is built with, Kubernetes 1.37 for Kueue
  v0.20, not the cluster's. This matters only when objects carry the fields of a DRA
  feature that the kube-scheduler has disabled, for example when the feature is
  disabled on the kube-scheduler but not on the kube-apiserver, or disabled after
  objects already used it. Otherwise the kube-apiserver drops the fields of a disabled
  feature, so Kueue and the kube-scheduler see the same devices.
- **Allocation time is not bounded**: each check tries an allocation on every
  node. A slow `DeviceClass` CEL selector makes every scheduling cycle slower,
  rather than timing out.
- **Not every Kubernetes DRA feature is modeled**: some, such as
  `DRADeviceBindingConditions`, change what the kube-scheduler does but not what the
  check predicts ([full list](https://github.com/kubernetes-sigs/kueue/tree/main/keps/2941-DRA#what-the-check-does-not-decide)).

For setup instructions, see
[Use Topology-Aware Scheduling with DRA](/docs/tasks/manage/setup_dra/#use-topology-aware-scheduling-with-dra).

## MultiKueue

DRA workloads are supported with [MultiKueue](/docs/concepts/multikueue),
except for `firstAvailable` requests; see the limitations below.
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
- **ExactCount allocation mode only**: the `All` allocation mode is not
  supported, in an `exactly` request or in an alternative of a `firstAvailable`
  one. Kueue reads a `firstAvailable` request only when the
  `KueueDRAIntegrationPrioritizedList` feature gate is enabled. That gate is
  alpha and off by default; see the note below for what it covers.
- **No device constraints or config**: Device `constraints` (MatchAttribute)
  and per-request `config` are not supported.
- **No AdminAccess**: Device requests with `adminAccess: true` are not
  supported.
- **TAS does not see devices**: without
  [Topology-Aware Scheduling with DRA](#topology-aware-scheduling-with-dra), TAS may
  place a Pod on a node without the devices its `ResourceClaimTemplate` requests, and
  a workload that requests an extended resource only a `DeviceClass` provides fits on
  no node.
- **Device taints do not change quota**: A tainted device is charged like any
  other. The per-node check can honor taints; see
  [Device taints](#device-taints).
- **`firstAvailable` requests are charged, within limits**: This support is
  experimental; do not enable it in production. With
  `KueueDRAIntegrationPrioritizedList` enabled, a `firstAvailable` request is
  charged once, the count every alternative asks for, which is what the
  scheduler allocates whichever alternative it picks. Every alternative of a
  request has to ask for the same count and map to the same logical resource;
  a request whose alternatives differ in count is refused, and so is an
  alternative on a mapping with a `counter` or `capacity` source. An
  alternative that sets `capacity` on the subrequest is charged its declared
  count like any other. Without `KueueDRADeviceFeasibility`, Kueue does not
  check that any alternative can be satisfied by the cluster, so a request
  whose alternatives are all infeasible holds its quota until the Workload is
  evicted, for example by
  [WaitForPodsReady](/docs/tasks/manage/setup_wait_for_pods_ready/) where it is
  configured; with it, such a Workload stays pending instead. The charge lands
  on the one ResourceFlavor the PodSet is assigned, and the Pods carry that
  flavor's node labels, so keep every alternative's devices behind the same
  flavors; with a flavor per device model, only the alternative with devices on
  the assigned flavor can run. MultiKueue does not support `firstAvailable`
  requests: a manager and a worker may resolve different templates, and nothing
  refuses such a Workload before dispatch yet.
