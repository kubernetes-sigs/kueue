---
title: "Run Workloads With DRA Devices"
linkTitle: "DRA"
date: 2026-03-22
weight: 7
description: >
  Run workloads that request hardware devices managed by Kubernetes
  Dynamic Resource Allocation (DRA) with Kueue quota management.
---

This page shows you how to run workloads that request hardware devices
(such as GPUs) managed by
[Dynamic Resource Allocation (DRA)](https://kubernetes.io/docs/concepts/scheduling-eviction/dynamic-resource-allocation/)
in a Kubernetes cluster with Kueue enabled. The examples use a batch Job, but
the same approach works with any
[workload type that Kueue supports](/docs/concepts/workload).

The intended audience for this page are [batch users](/docs/tasks#batch-user).

For conceptual details about how Kueue handles DRA resources, see
[Dynamic Resource Allocation concepts](/docs/concepts/dynamic_resource_allocation).

## Before you begin

Make sure the following conditions are met:

- A Kubernetes cluster is running.
- The kubectl command-line tool has communication with your cluster.
- [Kueue is installed](/docs/installation).
- The cluster has [quotas configured](/docs/tasks/manage/administer_cluster_quotas)
  with DRA resources included in the `ClusterQueue`.
- Your administrator has
  [set up DRA support in Kueue](/docs/tasks/manage/setup_dra).

## 0. Identify the queues available in your namespace

Run the following command to list the `LocalQueues` available in your namespace.

```shell
kubectl -n default get localqueues
```

The output is similar to the following:

```
NAME         CLUSTERQUEUE    PENDING WORKLOADS
user-queue   cluster-queue   0
```

The [ClusterQueue](/docs/concepts/cluster_queue) defines the quotas for the
Queue.

## 1. Define the workload

Running a workload with DRA devices is similar to
[running a regular Job](/docs/tasks/run/jobs). You must set the
`kueue.x-k8s.io/queue-name` label to select the `LocalQueue` you want to
submit the workload to.

There are several ways to request DRA devices, depending on how your
administrator has configured the cluster. Choose the approach that matches your
setup.

### Using a ResourceClaimTemplate

Use this approach when you need to explicitly describe the device you want.
Create a `ResourceClaimTemplate` and reference it from the workload:

{{< include "examples/dra/sample-dra-rct-job.yaml" "yaml" >}}

### Using a `firstAvailable` request

{{% alert title="Note" color="info" %}}
This feature requires the `KueueDRAIntegrationPrioritizedList` feature gate,
which is disabled by default in v0.20.
{{% /alert %}}

If your administrator has
[set up `firstAvailable` requests](/docs/tasks/manage/setup_dra/#set-up-firstavailable-requests),
list the alternative device classes in order of preference. They must use
`ExactCount`, ask for the same `count`, and map to one logical resource:

{{< include "examples/dra/sample-dra-firstavailable-job.yaml" "yaml" >}}

Kueue charges this one-Pod Job `example.com/gpu: 1`, whichever alternative the
kube-scheduler
[allocates](https://kubernetes.io/docs/concepts/resource-management/dynamic-resource-allocation/dra-api/).

### Using extended resources

Use this approach when a `DeviceClass` with `spec.extendedResourceName` exists
in the cluster. You request devices using the standard `resources.requests`
syntax, just like CPU or memory. No `ResourceClaimTemplate` is needed:

{{< include "examples/dra/sample-dra-extended-resource-job.yaml" "yaml" >}}

### Using partitionable devices

If your administrator has configured
[counter-based quota](/docs/tasks/manage/setup_dra/#set-up-counter-based-quota-partitionable-devices),
your workload is charged by the device's counter value (such as GPU memory)
rather than device count. You submit workloads the same way as the
ResourceClaimTemplate path above.

{{< include "examples/dra/sample-dra-counter-job.yaml" "yaml" >}}

### Using consumable capacity (shared devices)

{{% alert title="Note" color="info" %}}
This feature requires the `KueueDRAIntegrationConsumableCapacity` feature gate,
which is disabled by default in v0.19.
{{% /alert %}}

If your administrator has configured
[capacity-based quota](/docs/tasks/manage/setup_dra/#set-up-capacity-based-quota-consumable-capacity),
your workload is charged by the device's capacity consumption (such as GPU
memory) rather than device count. You submit workloads using a
`ResourceClaimTemplate` with `capacity.requests` specifying how much capacity
you need:

{{< include "examples/dra/sample-dra-capacity-job.yaml" "yaml" >}}

If you omit `capacity.requests`, Kueue charges the device's
`RequestPolicy.Default` or the full device capacity.

### Using Topology-Aware Scheduling

{{% alert title="Note" color="info" %}}
This feature requires the `KueueDRADeviceFeasibility` feature gate, which is
disabled by default in v0.20.
{{% /alert %}}

If your administrator has set up
[Topology-Aware Scheduling with DRA](/docs/tasks/manage/setup_dra/#use-topology-aware-scheduling-with-dra)
for a [Topology-Aware Scheduling](/docs/tasks/run/topology_aware_scheduling/) (TAS)
queue, Kueue places each Pod only on nodes that can allocate the devices it
requests. You request devices the same way, with a `ResourceClaimTemplate` or an
extended resource, and add a topology annotation as for any TAS workload:

{{< include "examples/dra/sample-dra-tas-job.yaml" "yaml" >}}

If you are not sure which approach to use, ask your administrator.

## 2. Run the workload

You can run the workload with the following command.

For a ResourceClaimTemplate-based workload:

```shell
kubectl create -f https://kueue.sigs.k8s.io/examples/dra/sample-dra-rct-job.yaml
```

For a workload with a `firstAvailable` request:

```shell
kubectl create -f https://kueue.sigs.k8s.io/examples/dra/sample-dra-firstavailable-job.yaml
```

For an extended resource-based workload:

```shell
kubectl create -f https://kueue.sigs.k8s.io/examples/dra/sample-dra-extended-resource-job.yaml
```

For a workload in a Topology-Aware Scheduling queue:

```shell
kubectl create -f https://kueue.sigs.k8s.io/examples/dra/sample-dra-tas-job.yaml
```

If you submit the example more than once, `kubectl` reports that the
`ResourceClaimTemplate` `single-gpu-tas` already exists. The Job is still created.

Internally, Kueue will create a corresponding [Workload](/docs/concepts/workload)
for this Job.

## 3. (Optional) Monitor the status of the workload

You can see the Workload status with the following command:

```shell
kubectl -n default get workloads.kueue.x-k8s.io
```

To check whether the workload was admitted and see the DRA resource
accounting:

```shell
kubectl -n default describe workload <workload-name>
```

Look at the `Conditions` section for admission status and the `Events`
section for details. If the workload was admitted, you can verify the
resources charged for quota in the
`status.admission.podSetAssignments[].resourceUsage` field:

```shell
kubectl -n default get workloads.kueue.x-k8s.io <workload-name> -o yaml
```

The Workload does not record which alternative a Pod received. Read it from the
Pod's generated `ResourceClaim`, using the Job name `kubectl create` returned:

```shell
kubectl -n default get pods -l batch.kubernetes.io/job-name=<job-name> -o jsonpath='{.items[*].status.resourceClaimStatuses[*].resourceClaimName}'
```

Then, for each claim name printed:

```shell
kubectl -n default get resourceclaim <claim-name> -o jsonpath='{.status.allocation.devices.results[*].request}'
```

The output is similar to the following:

```
gpu/a100
```

Each value is `<request>/<alternative>`. The output is empty until the claim is
allocated.

The example container waits so that you can inspect the claim. When you finish,
delete each example Job:

```shell
kubectl -n default delete job <job-name>
```

Once nothing uses `a100-or-mig`, delete the template:

```shell
kubectl -n default delete resourceclaimtemplate a100-or-mig
```

## Troubleshooting

### Workload not admitted

If the Workload stays in `Pending` state:

- Verify the `ClusterQueue` has quota for the DRA resource and it is not
  fully consumed by other workloads.
- Run `kubectl -n default describe workload <workload-name>` and look at
  the Events section for admission rejection reasons.

### Workload pending with `draNoFit`

Run `kubectl -n default describe workload <workload-name>` and look at the
`QuotaReserved` condition in the `Conditions` section. If its reason is
`TopologyPlacementFailed` and its message includes `draNoFit: N`, no node in the
topology has the devices a single Pod requests. The devices may be in use by other
workloads, or tainted by an administrator if device taints are enabled. Kueue retries
when devices change, for example when another workload releases them or a taint is
removed. If no node can ever satisfy the request, reduce the number of devices each
Pod requests, or ask your administrator which nodes publish the `DeviceClass` you use.

If the message is `Bypassed scheduling evaluation because an equivalent workload
recently failed`, Kueue skipped your workload because an equivalent one, with the same
requests, was just rejected. Look for another workload in the same queue whose message
includes `draNoFit`; it gives the reason. Both are retried when the devices change.

### `firstAvailable` request not admitted

Run `kubectl -n default describe workload <workload-name>` and look at the
`QuotaReserved` condition. With reason `DRAResourcesUnresolved` (`Inadmissible`
if your administrator disabled `UnadmittedWorkloadsObservability`), the message
names the template and the field at fault. `FirstAvailable device selection is
not supported` means `KueueDRAIntegrationPrioritizedList` is disabled, and
`deviceClassName: Not found` means the class is not in the mapping. Name a
mapped class, or ask your administrator to enable the gate or map the class and
restart the controller. For any other message, create a corrected template
under a new name and submit a new Job.

### Pods pending after the workload is admitted

Kueue checks that a node can serve one Pod, not all the Pods it places there. If a
node has fewer free devices than the Pods placed on it request, some Pods stay
`Pending` until devices are released, for example when another workload finishes.

### Double counting (extended resource path)

If quota usage shows double the expected value (e.g., `2` instead of `1` for
a single GPU), verify that `KueueDRAIntegrationExtendedResource` has not been
explicitly disabled. This gate is enabled by default since v0.19 and ensures
Kueue charges quota only once for extended resources backed by DRA, instead of
counting them as both a standard resource request and a DRA device.

### Missing DeviceClass

For the extended resource path, the `DeviceClass` must exist before you submit
your workload. If it was created after your workload was rejected, the workload
may not be re-evaluated until another cluster event triggers requeuing.
Delete and re-create the workload to force re-evaluation.

For general troubleshooting, see the
[troubleshooting guide](/docs/tasks/troubleshooting).
