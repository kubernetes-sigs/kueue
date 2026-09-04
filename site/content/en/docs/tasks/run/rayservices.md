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

1. Make sure you are using KubeRay v1.3.0 or newer.

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
- In-Tree Autoscaling Constraints: Autoscaling is only supported for [elastic](/docs/concepts/elastic_workload) RayService objects. To enable in-tree autoscaling:

  1. Activate the `ElasticJobsViaWorkloadSlices` feature gate.
  2. Annotate the RayService object with:

     ```yaml
     metadata:
       annotations:
         kueue.x-k8s.io/elastic-job: "true"
     ```
  3. Enable the Ray autoscaler of your RayService object by setting:

     ```yaml
     spec:
       rayClusterConfig:
         enableInTreeAutoscaling: true
     ```

- Rolling Upgrades: Kueue's Workload Slices feature currently manages quota for a single active cluster. Upgrade strategies that provision a secondary surge cluster (`spec.upgradeStrategy.type: NewCluster` or `NewClusterWithIncrementalUpgrade`) are not currently supported when workload slicing is enabled because pending cluster pods will remain gated. To use workload slicing with autoscaling, use `spec.upgradeStrategy.type: None` or apply updates in-place.

## Example RayService

The RayService looks like the following:

{{< include "examples/jobs/ray-service-sample.yaml" "yaml" >}}

{{% alert title="Note" color="primary" %}}
The example above comes from [here](https://raw.githubusercontent.com/ray-project/kuberay/v1.7.0/ray-operator/config/samples/ray-service.sample.yaml)
and only has the `queue-name` label added.
{{% /alert %}}
