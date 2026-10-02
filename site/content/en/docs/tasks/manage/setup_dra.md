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

{{% alert title="Warning" color="warning" %}}
In Kueue 0.18, the DRA feature gates were renamed to avoid conflicts with upstream
Kubernetes feature gates: `DynamicResourceAllocation` is now `KueueDRAIntegration`,
and `DRAExtendedResources` is now `KueueDRAIntegrationExtendedResource`.
{{% /alert %}}

## Choose a quota accounting path

Kueue supports four modes for accounting DRA devices in quota. Choose the
one that matches your device type and how your users submit workloads:

| Path | Quota unit | User's Pod spec | Enabled by default | Admin configuration |
|------|-----------|----------------|-------------------|-------------------|
| ResourceClaimTemplate | Device count | References a `ResourceClaimTemplate` | Yes (v0.18+) | `deviceClassMappings` required |
| Extended resource | Device count | Uses `resources.requests` (e.g., `nvidia.com/gpu: 1`) | Yes (v0.19+) | No mapping needed |
| Counter-based (PD) | Counter value (e.g., GPU memory) | References a `ResourceClaimTemplate` | Yes (v0.19+) | `deviceClassMappings` with `counter` source |
| Capacity-based (CC) | Capacity dimension (e.g., GPU memory) | References a `ResourceClaimTemplate` with `capacity.requests` | No (Alpha in v0.19) | `deviceClassMappings` with `capacity` source |

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

{{% alert title="Note" color="primary" %}}
Kueue reads the Configuration once, at manager startup. If you change
`deviceClassMappings` on a running cluster, restart the controller so the new
mapping takes effect:

```shell
kubectl rollout restart deployment/kueue-controller-manager -n kueue-system
```

Until then the manager keeps using the mapping it loaded at startup. A Workload
whose claim references a `DeviceClass` that mapping does not contain is not
admitted, rather than admitted without quota accounting: Kueue unsets its quota
reservation and marks the Workload inadmissible, with `DeviceClass <class> is not
mapped in DRA configuration for podset <podset>`.
{{% /alert %}}

### 2. Add the DRA resource to your ClusterQueue

Include the logical resource name from `deviceClassMappings` in the
`coveredResources` of your `ClusterQueue`:

{{< include "examples/dra/sample-dra-queues.yaml" "yaml" >}}

```shell
kubectl apply -f https://kueue.sigs.k8s.io/examples/dra/sample-dra-queues.yaml
```

The `example.com/gpu` resource in the `ClusterQueue` corresponds to the `name`
field in `deviceClassMappings`. Each device request referencing a mapped
`DeviceClass` consumes `count` units of this quota (default 1 when omitted).

## Set up `firstAvailable` requests

{{< feature-state state="alpha" for_version="v0.20" >}}

Use this when your users' `ResourceClaimTemplate` objects list alternative
device classes under `firstAvailable`. Complete the
[ResourceClaimTemplate path](#set-up-the-resourceclaimtemplate-path) first.
Every alternative of a request must use `ExactCount`, ask for the same `count`,
and map to one logical resource that has no `counter` or `capacity` source.
Kueue charges that count once per request.

### 1. Verify the DeviceClasses

Every `DeviceClass` the alternatives name must exist. Outside
[Topology-Aware Scheduling with DRA](#use-topology-aware-scheduling-with-dra), a
missing one leaves the Pod `Pending` while the Workload holds its quota.

To try the example with the
[dra-example-driver](https://github.com/kubernetes-sigs/dra-example-driver),
create two mock classes. Both select the same mock devices, so the example
shows only the quota charge:

{{< include "examples/dra/sample-dra-firstavailable-deviceclasses.yaml" "yaml" >}}

```shell
kubectl apply -f https://kueue.sigs.k8s.io/examples/dra/sample-dra-firstavailable-deviceclasses.yaml
```

Check that the classes exist:

```shell
kubectl get deviceclass a100.example.com a100-mig.example.com
```

### 2. Enable the gate and map the classes

Merge the following into your Kueue Configuration, following the
[custom configuration installation instructions](/docs/installation/#install-a-custom-configured-released-version),
and restart the controller. The two classes join the `example.com/gpu` entry
from the ResourceClaimTemplate path; a second entry with the same `name` is
rejected.

```yaml
apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
featureGates:
  KueueDRAIntegrationPrioritizedList: true
resources:
  deviceClassMappings:
  - name: example.com/gpu
    deviceClassNames:
    - gpu.example.com
    - a100.example.com
    - a100-mig.example.com
```

`KueueDRAIntegration` and the Kubernetes `DRAPrioritizedList` gate are needed
too. Both are on by default, `DRAPrioritizedList` since Kubernetes 1.34, so on
1.34 or later you do not need to enable them. The `sample-dra-queues.yaml`
`ClusterQueue` already covers `example.com/gpu`. Continue with the
[Job example](/docs/tasks/run/dra/#using-a-firstavailable-request). The
[limitations](/docs/concepts/dynamic_resource_allocation/#limitations) list
what Kueue does not check for these requests.

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
`spec.extendedResourceName` on the `DeviceClass`. Where a `deviceClassMappings`
entry remaps that name, only the containers' request moves to the mapped name, so
cover that one alongside the original, which still carries a chargeable Pod
overhead or a resource-transformation output written to it.

{{< include "examples/dra/sample-dra-queues.yaml" "yaml" >}}

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
[custom configuration installation instructions](/docs/installation/#install-a-custom-configured-released-version).

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

{{< include "examples/dra/sample-dra-counter-queues.yaml" "yaml" >}}

```shell
kubectl apply -f https://kueue.sigs.k8s.io/examples/dra/sample-dra-counter-queues.yaml
```

### 3. Verify counter-based quota is working

Submit a test workload:

{{< include "examples/dra/sample-dra-counter-job.yaml" "yaml" >}}

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

## Set up capacity-based quota (consumable capacity)

{{< feature-state state="alpha" for_version="v0.19" >}}

Use this when your cluster has devices that allow multiple allocations
(e.g., GPU time-slicing or MPS) and you want quota to reflect consumed
capacity rather than device count. This requires Kubernetes 1.36+ with the
`DRAConsumableCapacity` feature gate enabled and a DRA driver that publishes
`Capacity` and `AllowMultipleAllocations` on devices in `ResourceSlice`
objects.

Enable the feature gate in Kueue Configuration:

```yaml
featureGates:
  KueueDRAIntegrationConsumableCapacity: true
```

### 1. Configure capacity sources

Configure a `capacity` source entry in `deviceClassMappings`. Follow the
[custom configuration installation instructions](/docs/installation/#install-a-custom-configured-released-version).

```yaml
apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
featureGates:
  KueueDRAIntegrationConsumableCapacity: true
resources:
  deviceClassMappings:
  - name: gpu.memory                   # Logical resource name for quota
    deviceClassNames:
    - gpu.example.com                  # DeviceClass name(s)
    sources:
    - capacity:
        name: "gpu.example.com/memory" # Capacity dimension to track
        driver: gpu.example.com        # DRA driver name
        deviceSelector:
          cel:
            expression: "device.driver == 'gpu.example.com'"
```

The `deviceSelector` scopes which devices Kueue considers for capacity
charging. Ensure it matches only devices that support shared allocation.
If your cluster has a mix of exclusive and shareable devices under the
same driver, narrow the selector using device attributes.

The `sources[].capacity.name` must match a capacity dimension key published by
your DRA driver in `ResourceSlice` devices. You can inspect these with:

```shell
kubectl get resourceslices -o jsonpath='{range .items[*]}{.spec.driver}{"\t"}{range .spec.devices[*]}{.name}: capacity={.capacity}{"\n"}{end}{end}'
```

### 2. Add the capacity resource to your ClusterQueue

Set the quota in capacity units (e.g., `800Gi` for 10 GPUs with 80Gi each)
instead of device count:

{{< include "examples/dra/sample-dra-capacity-queues.yaml" "yaml" >}}

```shell
kubectl apply -f https://kueue.sigs.k8s.io/examples/dra/sample-dra-capacity-queues.yaml
```

### 3. Verify capacity-based quota is working

Submit a test workload with `capacity.requests`:

{{< include "examples/dra/sample-dra-capacity-job.yaml" "yaml" >}}

```shell
kubectl create -f https://kueue.sigs.k8s.io/examples/dra/sample-dra-capacity-job.yaml
```

Check the workload's `resourceUsage` to confirm quota was charged by capacity
value:

```shell
kubectl -n default get workloads.kueue.x-k8s.io -o jsonpath='{range .items[*]}{.metadata.name}: {.status.admission.podSetAssignments[0].resourceUsage}{"\n"}{end}'
```

The output should show the capacity-based charge (e.g., `4Gi`) instead of a
device count.

### Rounding and RequestPolicy

Kueue rounds capacity requests according to the device's `RequestPolicy`:

| RequestPolicy | Behavior |
|---------------|----------|
| `ValidValues` | Rounds up to the smallest valid value >= request |
| `ValidRange` with `Step` | Rounds up to `Min + n*Step` |
| `ValidRange` without `Step` | Rounds up to `Min` if below; errors if exceeds `Max` |
| No policy | Uses request as-is |

If the rounded value exceeds `Max` or all valid values, the workload is
marked inadmissible.

### Troubleshooting capacity-based quota

**Workload rejected with "insufficient matching devices"**: Kueue could not
find enough devices matching the `deviceSelector` CEL expression. Verify that
ResourceSlices exist and contain devices matching your selector.

**Workload rejected with "matched devices have no capacity dimension"**: The
devices in `ResourceSlice` objects do not have a capacity entry matching the
`name` configured in `sources[].capacity.name`. Verify the capacity dimension
name matches what your DRA driver publishes.

**Workload rejected with "capacity request cannot be satisfied"**: The
requested amount exceeds the device's `RequestPolicy` limits (e.g., request
exceeds `ValidRange.Max` or all `ValidValues`). Adjust the workload's
`capacity.requests` to a value within the device's policy.

## Use Topology-Aware Scheduling with DRA

{{< feature-state state="alpha" for_version="v0.20" >}}

Use this feature when DRA workloads run in a [Topology-Aware Scheduling](/docs/concepts/topology_aware_scheduling/)
(TAS) flavor and you want Kueue to place each Pod only on nodes that can allocate
the devices it requests. This requires the `ResourceClaimTemplate` path or the extended
resource path to be set up as described above. See
[Topology-Aware Scheduling with DRA](/docs/concepts/dynamic_resource_allocation/#topology-aware-scheduling-with-dra)
for the prerequisites and limitations.

### 1. Enable the feature gates

```yaml
apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
featureGates:
  KueueDRADeviceFeasibility: true
  KueueDRAIntegrationDeviceTaints: true  # optional: honor device taints
```

`KueueDRADeviceFeasibility` also requires `KueueDRAIntegration`,
`TopologyAwareScheduling` and `TASNodeFeasibilityForAllLevels`, which are enabled by
default.

### 2. Create a TAS flavor with the DRA resource

The nodes need the labels that the example `Topology` and flavor below refer to. On
an existing cluster, label each node with its block and rack, for example:

```shell
kubectl label node <node-name> cloud.provider.com/node-group=tas-group cloud.provider.com/topology-block=b1 cloud.provider.com/topology-rack=r1
```

Verify the labels:

```shell
kubectl get nodes -L cloud.provider.com/topology-block,cloud.provider.com/topology-rack
```

For more about topology labels, see
[Setup Topology-Aware Scheduling](/docs/tasks/manage/setup_topology_aware_scheduling/).

The check runs only for workloads assigned a `ResourceFlavor` with a
`topologyName`. The following example creates a `Topology`, a flavor for the nodes
labeled `cloud.provider.com/node-group: tas-group`, and a `ClusterQueue` that covers
`example.com/gpu`, the resource name mapped in `deviceClassMappings` above:

{{< include "examples/dra/sample-dra-tas-queues.yaml" "yaml" >}}

```shell
kubectl apply -f https://kueue.sigs.k8s.io/examples/dra/sample-dra-tas-queues.yaml
```

### 3. Verify the check is working

Submit a workload that requests one GPU per Pod:

{{< include "examples/dra/sample-dra-tas-job.yaml" "yaml" >}}

```shell
kubectl create -f https://kueue.sigs.k8s.io/examples/dra/sample-dra-tas-job.yaml
```

If you submit the example more than once, `kubectl` reports that the
`ResourceClaimTemplate` `single-gpu-tas` already exists. The Job is still created.

Check the node the workload was assigned to:

```shell
kubectl -n default get workloads.kueue.x-k8s.io -o jsonpath='{range .items[*]}{.metadata.name}: {.status.admission.podSetAssignments[0].topologyAssignment}{"\n"}{end}'
```

The output is similar to the following:

```
job-sample-dra-tas-job-xxxxx: {"levels":["kubernetes.io/hostname"],"slices":[{"domainCount":1,"podCounts":{"universal":2},"valuesPerLevel":[{"universal":"gpu-node-1"}]}]}
```

The node is one that publishes `gpu.example.com` devices. You can list those nodes
with:

```shell
kubectl get resourceslices -o custom-columns=NODE:.spec.nodeName,DRIVER:.spec.driver,DEVICES:.spec.devices[*].name
```

When no node in the topology has the devices a Pod requests, for example because their
devices are in use or, with `KueueDRAIntegrationDeviceTaints`, tainted, the workload
stays pending. Its `QuotaReserved` condition counts the nodes rejected for devices as
`draNoFit`:

```shell
kubectl -n default get workloads.kueue.x-k8s.io -o jsonpath='{range .items[*]}{.metadata.name}: {.status.conditions[?(@.type=="QuotaReserved")].message}{"\n"}{end}'
```

The output is similar to the following:

```
job-sample-dra-tas-job-xxxxx: couldn't assign flavors to pod set main: topology "dra-topology" doesn't allow to fit any of 2 pod(s). Total nodes: 2; excluded: draNoFit: 2
```

If the request also exceeds the quota, the message reports insufficient quota
instead, because quota is checked first.

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
synced. These must be created on each worker cluster separately. That support
does not extend to `firstAvailable` requests. See the
[limitations](/docs/concepts/dynamic_resource_allocation/#limitations).
