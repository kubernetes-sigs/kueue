---
title: "Run A RayService"
linkTitle: "RayServices"
date: 2025-06-30
weight: 10
description: >
  Run a RayService with Kueue.
---

This page shows how to leverage Kueue's scheduling and resource management capabilities when running [RayService](https://docs.ray.io/en/latest/cluster/kubernetes/getting-started/rayservice-quick-start.html).

Kueue manages the RayService through the RayCluster created for it. Therefore, RayService needs the label of `kueue.x-k8s.io/queue-name: user-queue` and this label is propagated to the relevant RayCluster to trigger Kueue's management.

This guide is for [serving users](/docs/tasks#serving-user) that have a basic understanding of Kueue. For more information, see [Kueue's overview](/docs/overview).

## Before you begin

1. Make sure you are using Kueue v0.6.0 version or newer and KubeRay v1.3.0 or newer.

2. Check [Administer cluster quotas](/docs/tasks/manage/administer_cluster_quotas) for details on the initial Kueue setup.

3. See [KubeRay Installation](https://docs.ray.io/en/latest/cluster/kubernetes/getting-started/kuberay-operator-installation.html) for installation and configuration details of KubeRay.

{{% alert title="Note" color="primary" %}}
RayService is managed by Kueue through RayCluster, and in order to use RayCluster, prior to v0.8.1, you need to restart Kueue after the installation.
You can do it by running: `kubectl delete pods -l control-plane=controller-manager -n kueue-system`.
{{% /alert %}}

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

### c. Limitations
- Limited Worker Groups: Because a Kueue workload can have a maximum of 8 PodSets, the maximum number of `spec.rayClusterConfig.workerGroupSpecs` is 7
- In-Tree Autoscaling Disabled: Kueue manages resource allocation for the RayService; therefore, the internal autoscaling mechanisms need to be disabled

## Example RayService

The RayService looks like the following:

{{< include "examples/jobs/ray-service-sample.yaml" "yaml" >}}

{{% alert title="Note" color="primary" %}}
The example above comes from [here](https://raw.githubusercontent.com/ray-project/kuberay/v1.4.2/ray-operator/config/samples/ray-service.sample.yaml)
and only has the `queue-name` label added and requests updated.
{{% /alert %}}