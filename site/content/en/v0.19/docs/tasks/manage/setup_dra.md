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

The intended audience for this page are [batch administrators](/v0.19/docs/tasks#batch-administrator).

For conceptual details, see
[Dynamic Resource Allocation concepts](/v0.19/docs/concepts/dynamic_resource_allocation).
For instructions on submitting workloads with DRA devices, see
[Run Workloads With DRA Devices](/v0.19/docs/tasks/run/dra).

## Before you begin

Make sure the following conditions are met:

- A Kubernetes cluster running version 1.34 or later.
- A DRA driver installed in the cluster (e.g.,
  [dra-example-driver](https://github.com/kubernetes-sigs/dra-example-driver)
  for testing, or a vendor driver like
  [NVIDIA k8s-dra-driver-gpu](https://github.com/NVIDIA/k8s-dra-driver-gpu)
  for production).
- [Kueue is installed](/v0.19/docs/installation).

{{% alert title="Warning" color="warning" %}}
In Kueue 0.18, the DRA feature gates were renamed to avoid conflicts with upstream
Kubernetes feature gates: `DynamicResourceAllocation` is now `KueueDRAIntegration`,
and `DRAExtendedResources` is now `KueueDRAIntegrationExtendedResource`.
{{% /alert %}}

## Choose a quota accounting path

Kueue supports two paths for accounting DRA devices in quota. Choose the one
that matches how your users submit workloads:

| Path | User's Pod spec | Kueue feature gate | Admin configuration |
|------|----------------|-------------------|-------------------|
| ResourceClaimTemplate | References a `ResourceClaimTemplate` | `KueueDRAIntegration` | `deviceClassMappings` required |
| Extended resource | Uses `resources.requests` (e.g., `nvidia.com/gpu: 1`) | `KueueDRAIntegration` + `KueueDRAIntegrationExtendedResource` | No mapping needed |

## Set up the ResourceClaimTemplate path

{{< feature-state state="beta" for_version="v0.18" >}}

Use this path when your users submit workloads that explicitly reference
`ResourceClaimTemplate` objects.

### 1. Configure deviceClassMappings

Add a `deviceClassMappings` entry to the Kueue Configuration that maps each
`DeviceClass` to a logical resource name for quota:

```yaml
apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
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

### 2. Add the DRA resource to your ClusterQueue

Include the logical resource name from `deviceClassMappings` in the
`coveredResources` of your `ClusterQueue`:

{{< include "v0.19/examples/dra/sample-dra-queues.yaml" "yaml" >}}

```shell
kubectl apply -f https://kueue.sigs.k8s.io/examples/dra/sample-dra-queues.yaml
```

The `example.com/gpu` resource in the `ClusterQueue` corresponds to the `name`
field in `deviceClassMappings`. Each device request referencing a mapped
`DeviceClass` consumes `count` units of this quota (default 1 when omitted).

## Set up the extended resource path

{{< feature-state state="beta" for_version="v0.19" >}}

Use this path when your users submit workloads using the standard
`resources.requests` syntax (e.g., `nvidia.com/gpu: 1`) and a `DeviceClass`
with `spec.extendedResourceName` exists in the cluster. Both
`KueueDRAIntegration` and `KueueDRAIntegrationExtendedResource` feature gates
are enabled by default since v0.19. The Kubernetes cluster also needs the
`DRAExtendedResource` feature gate enabled on kube-apiserver and kube-scheduler
(beta in Kubernetes 1.36).

### 1. Verify the DeviceClass

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


No `deviceClassMappings` configuration is needed for this path. Kueue
auto-discovers the mapping by indexing `DeviceClass` objects.

### 2. Add the extended resource to your ClusterQueue

The `coveredResources` must include the extended resource name that matches
`spec.extendedResourceName` on the `DeviceClass`:

{{< include "v0.19/examples/dra/sample-dra-queues.yaml" "yaml" >}}

```shell
kubectl apply -f https://kueue.sigs.k8s.io/examples/dra/sample-dra-queues.yaml
```

### Late DeviceClass creation

Kueue watches `DeviceClass` objects for create, update, and delete events.
When a `DeviceClass` is created or its `extendedResourceName` changes, Kueue
requeues pending workloads that request the affected extended resource so they
are re-evaluated through the DRA path.

If the `DeviceClass` does not exist when a workload is submitted, Kueue
processes the extended resource through the normal (non-DRA) quota path. The
workload is admitted if the ClusterQueue covers the extended resource name,
but the pod stays Pending because the Kubernetes scheduler cannot create a
ResourceClaim without a DeviceClass. Once the DeviceClass is created, the
Kubernetes scheduler creates a ResourceClaim and the pod runs.

Alternatively, configure a `deviceClassMappings` entry and use the mapped
logical name in the ClusterQueue. Without a DeviceClass, Kueue skips DRA
resolution and the raw extended resource name does not match the logical
name in the ClusterQueue, so the workload stays inadmissible.

### Why this path exists

When a Pod requests an extended resource backed by DRA, the kube-scheduler
auto-creates a `ResourceClaim`. Kueue detects the matching `DeviceClass` and
charges quota only for the extended resource backed by DRA, preventing double
counting of both the `resources.requests` entry and the auto-created claim.

## Set up counter-based quota (partitionable devices)

{{< feature-state state="beta" for_version="v0.19" >}}

Use this when your cluster has partitionable devices and you want quota to
reflect actual device capacity rather than device count. This requires
Kubernetes 1.35+ with the `DRAPartitionableDevices` feature gate enabled
and a DRA driver that publishes `consumesCounters` in `ResourceSlice` objects.
Both `KueueDRAIntegration` and `KueueDRAIntegrationPartitionableDevices`
feature gates are enabled by default since v0.19.

### 1. Configure counter sources

Configure a `sources` entry in `deviceClassMappings`. Follow the
[custom configuration installation instructions](/v0.19/docs/installation/#install-a-custom-configured-released-version).

```yaml
apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
resources:
  deviceClassMappings:
  - name: gpu.memory
    deviceClassNames:
    - gpu.example.com
    sources:
    - counter:
        name: memory
        driver: gpu.example.com
        deviceSelector:
          cel:
            expression: "device.driver == 'gpu.example.com'"
```

The `sources[].counter.name` must match a counter key published by your DRA
driver in `ResourceSlice` devices. You can inspect these with:

```shell
kubectl get resourceslices -o jsonpath='{range .items[*]}{.spec.driver}{"\t"}{range .spec.devices[*]}{.name}: {.consumesCounters}{"\n"}{end}{end}'
```

The output is similar to the following:

```
gpu.example.com  gpu-0: [{"counterSet":"shared","counters":{"memory":{"value":"10Gi"}}}]
```

### 2. Add the counter resource to your ClusterQueue

Set the quota in counter units (e.g., `256Mi`) instead of device count (e.g., `1`). When ClusterQueues
share a cohort, ensure all queues use the same unit scale for counter
resources. Kueue does not validate unit consistency across ClusterQueues.

{{< include "v0.19/examples/dra/sample-dra-counter-queues.yaml" "yaml" >}}

```shell
kubectl apply -f https://kueue.sigs.k8s.io/examples/dra/sample-dra-counter-queues.yaml
```

### 3. Verify counter-based quota is working

Submit a test workload:

{{< include "v0.19/examples/dra/sample-dra-counter-job.yaml" "yaml" >}}

```shell
kubectl create -f https://kueue.sigs.k8s.io/examples/dra/sample-dra-counter-job.yaml
```

Check the workload's `resourceUsage` to confirm quota was charged by
counter value:

```shell
kubectl -n default get workloads.kueue.x-k8s.io -o jsonpath='{range .items[*]}{.metadata.name}: {.status.admission.podSetAssignments[0].resourceUsage}{"\n"}{end}'
```

The output is similar to the following:

```
job-sample-dra-counter-job-xxxxx: {"gpu.memory":"85899345920"}
```

### Troubleshooting counter-based quota

**Workload rejected with "insufficient matching devices"**: Kueue could not
find enough devices matching the `deviceSelector` CEL expression. This can
happen if `ResourceSlice` objects are not yet populated (e.g., during driver
startup or node registration). Verify that ResourceSlices exist and contain
devices matching your selector.

**Workload rejected with "no consumesCounters entry for counter"**: The
devices in `ResourceSlice` objects do not have a `consumesCounters` entry
matching the `name` configured in `sources[].counter.name`. Verify the
counter name matches what your DRA driver publishes (see step 1).

**Kueue fails to start with "CEL compilation failed"**: The `deviceSelector`
CEL expression has a syntax or type error. Check the expression against the
[DRA CEL environment](https://kubernetes.io/docs/concepts/scheduling-eviction/dynamic-resource-allocation/#device-selector).

## Path separation

The two paths are independent. Do not configure the same `DeviceClass` in
both paths for the same workload. If overlap occurs, Kueue merges the
resources using the `deviceClassMappings` logical name as the quota key,
which may result in incorrect quota accounting.

## Recommended: enable WaitForPodsReady

There is a timing gap between Kueue admitting a workload and the
kube-scheduler allocating the actual device. If the cluster state changes
between these two steps, the scheduler may fail to allocate. Enabling
[WaitForPodsReady](/v0.19/docs/tasks/manage/setup_wait_for_pods_ready/) provides a
safety net by evicting workloads that fail to become ready within a configured
timeout, allowing them to be re-queued and retried.

## MultiKueue considerations

DRA workloads are supported with [MultiKueue](/v0.19/docs/concepts/multikueue).
MultiKueue syncs the workload and its owning job to worker clusters, but
`ResourceClaimTemplate` and `DeviceClass` objects are not automatically
synced. These must be created on each worker cluster separately.
