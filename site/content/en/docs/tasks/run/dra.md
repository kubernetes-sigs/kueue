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

There are two ways to request DRA devices, depending on how your administrator
has configured the cluster. Choose the approach that matches your setup.

### Using a ResourceClaimTemplate

Use this approach when you need to explicitly describe the device you want.
Create a `ResourceClaimTemplate` and reference it from the workload:

{{< include "examples/dra/sample-dra-rct-job.yaml" "yaml" >}}

### Using extended resources

Use this approach when a `DeviceClass` with `spec.extendedResourceName` exists
in the cluster. You request devices using the standard `resources.requests`
syntax, just like CPU or memory. No `ResourceClaimTemplate` is needed:

{{< include "examples/dra/sample-dra-extended-resource-job.yaml" "yaml" >}}

If you are not sure which approach to use, ask your administrator.

## 2. Run the workload

You can run the workload with the following command.

For a ResourceClaimTemplate-based workload:

```shell
kubectl create -f https://kueue.sigs.k8s.io/examples/dra/sample-dra-rct-job.yaml
```

For an extended resource-based workload:

```shell
kubectl create -f https://kueue.sigs.k8s.io/examples/dra/sample-dra-extended-resource-job.yaml
```

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

## Troubleshooting

### Workload not admitted

If the Workload stays in `Pending` state:

- Verify the `ClusterQueue` has quota for the DRA resource and it is not
  fully consumed by other workloads.
- Run `kubectl -n default describe workload <workload-name>` and look at
  the Events section for admission rejection reasons.

### Double counting (extended resource path)

If quota usage shows double the expected value (e.g., `2` instead of `1` for
a single GPU), the `DRAExtendedResources` feature gate may not be enabled.
Ask your administrator to verify the
[DRA setup](/docs/tasks/manage/setup_dra).

### Missing DeviceClass

For the extended resource path, the `DeviceClass` must exist before you submit
your workload. If it was created after your workload was rejected, the workload
may not be re-evaluated until another cluster event triggers requeuing.
Delete and re-create the workload to force re-evaluation.

For general troubleshooting, see the
[troubleshooting guide](/docs/tasks/troubleshooting).
