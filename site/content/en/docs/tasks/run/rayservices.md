---
title: "Run A RayService"
linkTitle: "RayServices"
date: 2025-06-30
weight: 10
description: >
  Run a RayService with Kueue.
---

This page shows how to leverage Kueue's scheduling and resource management capabilities when running [RayService](https://docs.ray.io/en/latest/cluster/kubernetes/getting-started/rayservice-quick-start.html).

Kueue manages the RayService as a top-level job, similar to how Kueue manages the RayJob.

This guide is for [serving users](/docs/tasks#serving-user) that have a basic understanding of Kueue. For more information, see [Kueue's overview](/docs/overview).

## Before you begin

1. Make sure you are using Kueue v0.17.0 or newer and KubeRay v1.3.0 or newer.

2. Check [Administer cluster quotas](/docs/tasks/manage/administer_cluster_quotas) for details on the initial Kueue setup.

3. See [KubeRay Installation](https://docs.ray.io/en/latest/cluster/kubernetes/getting-started/kuberay-operator-installation.html) for installation and configuration details of KubeRay.

## RayService definition

When running [RayService](https://docs.ray.io/en/latest/cluster/kubernetes/getting-started/rayservice-quick-start.html) on Kueue, take into consideration the following aspects:

### a. Queue selection

The target [local queue](/docs/concepts/local_queue) should be specified in the `metadata.labels` section of the RayService configuration, and this label will be propagated to its RayCluster.

```yaml
metadata:
  labels:
    kueue.x-k8s.io/queue-name: user-queue
```

### b. Configure the resource needs

The resource needs of the workload can be configured in the `spec.rayClusterConfig`.

```yaml
spec:
  rayClusterConfig:
    headGroupSpec:
    template:
      spec:
        containers:
          - resources:
              requests:
                cpu: "1"
    workerGroupSpecs:
    - template:
        spec:
          containers:
            - resources:
                requests:
                  cpu: "1"
```

### c. Suspend control

Kueue controls the `spec.rayClusterConfig.suspend` field of the RayService. When a RayService is admitted by Kueue, Kueue will unsuspend it by setting `spec.rayClusterConfig.suspend` to `false`, regardless of its previous value.

### d. Limitations

- Limited Worker Groups: Because a Kueue workload can have a maximum of 18 PodSets, the maximum number of `spec.rayClusterConfig.workerGroupSpecs` is 17.

## Autoscaling (a.k.a InTreeAutoscaling)

[Ray Autoscaling](https://docs.ray.io/en/latest/cluster/kubernetes/user-guides/configuring-autoscaling.html) can automatically add or remove Ray worker pods based on resource demand.

This feature is supported for RayService starting in Kueue v0.19.0.

Autoscaling is only supported for [elastic](/docs/concepts/elastic_workload) RayService objects.

### How to enable autoscaling in RayService

1. Enable the feature gate for [Elastic Workloads (Workload Slices)](/docs/concepts/elastic_workload):

   ```yaml
   ElasticJobsViaWorkloadSlices: true
   ```

2. Add the workload slicing annotation to the RayService object:

   ```yaml
   metadata:
     annotations:
       kueue.x-k8s.io/elastic-job: "true"
   ```

3. Enable the Ray autoscaler in the RayService object:

   ```yaml
   spec:
     rayClusterConfig:
       enableInTreeAutoscaling: true
   ```

## Zero-downtime upgrades

During a zero-downtime upgrade (`spec.upgradeStrategy.type: NewCluster`), KubeRay runs the active RayCluster and a replacement RayCluster at the same time.
Kueue reserves quota for both RayClusters only for [elastic](/docs/concepts/elastic_workload) RayService objects.

### Requirements

- The `ElasticJobsViaWorkloadSlices` feature gate is enabled.
- The RayService has the `kueue.x-k8s.io/elastic-job: "true"` annotation.
- KubeRay v1.7.0 or newer.

### Limitations

- **Resource requests must not change.**
  A workload slice can change the Pod count of a PodSet, but not its per-Pod resource requests.
- **`NewClusterWithIncrementalUpgrade` is not supported.**
- **Non-elastic RayServices are not supported.**
  Without workload slicing, Kueue reserves quota for one RayCluster only.
  Use `spec.upgradeStrategy.type: None` for RayServices without the `kueue.x-k8s.io/elastic-job` annotation.

## Example RayService

The RayService looks like the following:

{{< include "examples/jobs/ray-service-sample.yaml" "yaml" >}}

{{% alert title="Note" color="primary" %}}
The example above comes from [the KubeRay RayService sample](https://raw.githubusercontent.com/ray-project/kuberay/v1.4.2/ray-operator/config/samples/ray-service.sample.yaml)
and only has the `queue-name` label added and requests updated.
{{% /alert %}}
