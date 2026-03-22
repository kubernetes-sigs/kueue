---
title: "Dynamic Resource Allocation"
date: 2026-03-22
weight: 7
description: >
  Quota management for workloads using Kubernetes Dynamic Resource Allocation (DRA).
---

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

{{< feature-state state="alpha" for_version="v0.14" >}}

When a Pod references a `ResourceClaimTemplate`, Kueue reads the
`deviceClassName` from the template's `exactly` field and looks it up in
`deviceClassMappings`. This mapping tells Kueue which logical resource name
to charge quota against. The number of units charged is determined by the
`count` field in the device request (default 1).

Only the `ExactCount` allocation mode is supported. CEL selectors and the
`All` allocation mode are not supported in alpha.

For setup instructions, see
[Set Up Dynamic Resource Allocation](/docs/tasks/manage/setup_dra).

## How the extended resource path works

{{< feature-state state="alpha" for_version="v0.17" >}}

When a Pod requests an extended resource backed by DRA (e.g.,
`nvidia.com/gpu: 1`), the kube-scheduler auto-creates a `ResourceClaim`.
Without the `DRAExtendedResources` feature gate enabled, Kueue would charge
quota for both the `resources.requests` entry **and** the auto-created claim,
double counting the same device.

With `DRAExtendedResources` enabled, Kueue detects the matching `DeviceClass`,
uses `extendedResourceName` as the quota key, and drops the auto-created claim
from accounting. No `deviceClassMappings` configuration is needed — the
mapping is discovered from the `DeviceClass` automatically.

{{% alert title="Note" color="info" %}}
The extended resource path additionally requires the Kubernetes
`DRAExtendedResource` feature gate on kube-apiserver and kube-scheduler
(alpha in Kubernetes 1.34), in addition to Kueue's `DynamicResourceAllocation`
and `DRAExtendedResources` feature gates.
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
sets a `nominalQuota`. When a workload is admitted, Kueue charges quota based
on the `count` value in each device request (default 1 when omitted).

For example, a `ClusterQueue` with `example.com/gpu: 8` allows up to 8
concurrent device allocations across all workloads using that queue.

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

## Limitations

The following limitations apply to the alpha release:

- **ResourceClaimTemplates only**: Only `ResourceClaimTemplate` references
  are supported. Direct `ResourceClaim` references in the Pod spec are not
  supported and will result in inadmissible workloads.
- **ExactCount allocation mode only**: Only device requests using `exactly`
  are supported. `FirstAvailable` device selection and the `All` allocation
  mode are not supported.
- **No CEL selectors**: Device requests with CEL selectors in the
  `ResourceClaimTemplate` are not supported. The device class name is used
  directly for quota mapping.
- **No device constraints or config**: Device `constraints` (MatchAttribute)
  and per-request `config` are not supported.
- **No AdminAccess**: Device requests with `adminAccess: true` are not
  supported.
- **No support for partitionable devices (MIG)**: Quota is tracked by device
  count, not by device capacity. A 1g.10gb MIG partition and a full A100 GPU
  both count as "1 device". Counter-based quota for partitionable devices
  will be addressed in a future release.
- **No DRA + Topology Aware Scheduling (TAS)**: DRA resources are not
  accounted for in TAS capacity calculations. Using both features together
  may result in incorrect topology assignments for DRA devices.
- **No support for DRADeviceTaints or DRAPrioritizedLists**: These Kubernetes
  DRA features are not factored into Kueue's quota decisions.
- **No GPU time-slicing or MPS**: Software-based GPU sharing mechanisms
  are out of scope for alpha. These require upstream Kubernetes support
  ([KEP-5075](https://github.com/kubernetes/enhancements/issues/5075),
  [KEP-5691](https://github.com/kubernetes/enhancements/issues/5691))
  that is not yet available.
- **No DeviceClass watcher**: If a `DeviceClass` is created after a workload
  was already rejected, the workload is not immediately retried. It will be
  re-evaluated when the next cluster event triggers inadmissible workload
  requeuing (e.g., another workload completes or quota changes). To avoid
  delays, ensure the `DeviceClass` exists before submitting workloads.
