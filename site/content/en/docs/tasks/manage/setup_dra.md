---
title: "Set Up Dynamic Resource Allocation"
linkTitle: "Dynamic Resource Allocation"
date: 2026-03-22
weight: 7
description: >
  Configure Kueue to manage quota for workloads using Kubernetes Dynamic Resource Allocation (DRA).
---

This page shows you how to configure Kueue to account for DRA devices in quota
management.

The intended audience for this page are [batch administrators](/docs/tasks#batch-administrator).

For conceptual details, see
[Dynamic Resource Allocation concepts](/docs/concepts/dynamic_resource_allocation).
For instructions on submitting workloads with DRA devices, see
[Run Workloads With DRA Devices](/docs/tasks/run/dra).

## Before you begin

Make sure the following conditions are met:

- A Kubernetes cluster running version 1.34 or later.
- A DRA driver installed in the cluster (e.g.,
  [dra-example-driver](https://github.com/kubernetes-sigs/dra-example-driver)
  for testing, or a vendor driver like
  [NVIDIA k8s-dra-driver-gpu](https://github.com/NVIDIA/k8s-dra-driver-gpu)
  for production).
- [Kueue is installed](/docs/installation).

## Choose a quota accounting path

Kueue supports two paths for accounting DRA devices in quota. Choose the one
that matches how your users submit workloads:

| Path | User's Pod spec | Kueue feature gate | Admin configuration |
|------|----------------|-------------------|-------------------|
| ResourceClaimTemplate | References a `ResourceClaimTemplate` | `DynamicResourceAllocation` | `deviceClassMappings` required |
| Extended resource | Uses `resources.requests` (e.g., `nvidia.com/gpu: 1`) | `DynamicResourceAllocation` + `DRAExtendedResources` | No mapping needed |

## Set up the ResourceClaimTemplate path

{{< feature-state state="alpha" for_version="v0.14" >}}

Use this path when your users submit workloads that explicitly reference
`ResourceClaimTemplate` objects.

### 1. Enable the feature gate

Install or reconfigure Kueue with the `DynamicResourceAllocation` feature gate
enabled. Follow the
[custom configuration installation instructions](/docs/installation#install-a-custom-configured-released-version).

### 2. Configure deviceClassMappings

Add a `deviceClassMappings` entry to the Kueue Configuration that maps each
`DeviceClass` to a logical resource name for quota:

```yaml
apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
featureGates:
  DynamicResourceAllocation: true
resources:
  deviceClassMappings:
  - name: example.com/gpu           # Logical resource name for quota
    deviceClassNames:
    - gpu.example.com               # DeviceClass name(s)
```

- `name`: The resource name used in `ClusterQueue` quotas and `Workload` status.
- `deviceClassNames`: One or more `DeviceClass` names that map to this resource.

Multiple device classes can map to the same logical resource name. For example,
if you have separate device classes for different GPU models but want a single
quota pool:

```yaml
resources:
  deviceClassMappings:
  - name: example.com/gpu
    deviceClassNames:
    - gpu-a100.example.com
    - gpu-h100.example.com
```

### 3. Add the DRA resource to your ClusterQueue

Include the logical resource name from `deviceClassMappings` in the
`coveredResources` of your `ClusterQueue`:

{{< include "examples/dra/sample-dra-queues.yaml" "yaml" >}}

```shell
kubectl apply -f https://kueue.sigs.k8s.io/examples/dra/sample-dra-queues.yaml
```

The `example.com/gpu` resource in the `ClusterQueue` corresponds to the `name`
field in `deviceClassMappings`. Each device request referencing a mapped
`DeviceClass` consumes `count` units of this quota (default 1 when omitted).

## Set up the extended resource path

{{< feature-state state="alpha" for_version="v0.17" >}}

Use this path when your users submit workloads using the standard
`resources.requests` syntax (e.g., `nvidia.com/gpu: 1`) and a `DeviceClass`
with `spec.extendedResourceName` exists in the cluster.

### 1. Enable the feature gates

Install or reconfigure Kueue with both feature gates enabled:

```yaml
apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
featureGates:
  DynamicResourceAllocation: true
  DRAExtendedResources: true
```

The Kubernetes cluster also needs the `DRAExtendedResource` feature gate
enabled on kube-apiserver and kube-scheduler. This is alpha (disabled by
default) in Kubernetes 1.34.

### 2. Verify the DeviceClass

Ensure the `DeviceClass` has `spec.extendedResourceName` set. This is
typically configured by the DRA driver or cluster administrator:

```shell
kubectl get deviceclass gpu.example.com -o jsonpath='{.spec.extendedResourceName}'
```

If you need to create or update the `DeviceClass`:

```yaml
apiVersion: resource.k8s.io/v1
kind: DeviceClass
metadata:
  name: gpu.example.com
spec:
  extendedResourceName: example.com/gpu
  selectors:
  - cel:
      expression: device.driver == "gpu.example.com"
```

{{% alert title="Important" color="warning" %}}
The `DeviceClass` must exist **before** users submit workloads. There is no
DeviceClass watcher in alpha. If a `DeviceClass` is created after a workload
was marked inadmissible, the workload will not be re-evaluated until the next
cluster event that triggers inadmissible workload requeuing (e.g., another
workload completes or quota changes).
{{% /alert %}}

No `deviceClassMappings` configuration is needed for this path. Kueue
auto-discovers the mapping by indexing `DeviceClass` objects.

### 3. Add the extended resource to your ClusterQueue

The `coveredResources` must include the extended resource name that matches
`spec.extendedResourceName` on the `DeviceClass`:

{{< include "examples/dra/sample-dra-queues.yaml" "yaml" >}}

```shell
kubectl apply -f https://kueue.sigs.k8s.io/examples/dra/sample-dra-queues.yaml
```

### Why this path exists

Without the `DRAExtendedResources` feature gate, Kueue charges quota for both
the `resources.requests` entry and the auto-created `ResourceClaim`, double
counting the same device. With the feature gate enabled, Kueue detects the
matching `DeviceClass` and charges quota only for the extended resource.

## Path separation

The two paths are independent. Do not configure the same `DeviceClass` in
both paths for the same workload. If overlap occurs, Kueue merges the
resources using the `deviceClassMappings` logical name as the quota key,
which may result in incorrect quota accounting.

## Recommended: enable WaitForPodsReady

There is a timing gap between Kueue admitting a workload and the
kube-scheduler allocating the actual device. If the cluster state changes
between these two steps, the scheduler may fail to allocate. Enabling
[WaitForPodsReady](/docs/tasks/manage/setup_wait_for_pods_ready/) provides a
safety net by evicting workloads that fail to become ready within a configured
timeout, allowing them to be re-queued and retried.

## MultiKueue considerations

DRA workloads are supported with [MultiKueue](/docs/concepts/multikueue).
MultiKueue syncs the workload and its owning job to worker clusters, but
`ResourceClaimTemplate` and `DeviceClass` objects are not automatically
synced. These must be created on each worker cluster separately.
