---
title: "运行 RayService"
linkTitle: "RayService"
date: 2025-06-30
weight: 6
description: >
  在 Kueue 上运行 RayService 的指南。
---

本页演示如何利用 Kueue 的调度与资源管理能力运行
[RayService](https://docs.ray.io/en/latest/cluster/kubernetes/getting-started/rayservice-quick-start.html) 。

Kueue 可以直接管理 RayService，类似其直接管理 RayJob。

本指南面向对 Kueue 有基本了解的、[对外提供服务的用户](/zh-cn/docs/tasks#serving-user)。
更多信息，请参见 [Kueue 概览](/zh-cn/docs/overview)。

## 开始之前 {#before-you-begin}

1. 请确保你使用的是 KubeRay v1.3.0 或更高版本。

2. 请参见 [管理集群配额](/zh-cn/docs/tasks/manage/administer_cluster_quotas)了解初始 Kueue 设置的详细信息。

3. 请参见 [KubeRay 安装说明](https://docs.ray.io/en/latest/cluster/kubernetes/getting-started/raycluster-quick-start.html#step-2-deploy-a-kuberay-operator)了解 KubeRay 的安装和配置详情。

## RayService 定义 {#rayservice-definition}

在 Kueue 上运行 [RayService](https://docs.ray.io/en/latest/cluster/kubernetes/getting-started/rayservice-quick-start.html)
时，请考虑以下方面：

### a. 队列选择 {#a-queue-selection}

目标 [本地队列](/zh-cn/docs/concepts/local_queue)应在 RayService 配置的 `metadata.labels`
部分指定，该标签会被传递到其 RayCluster。

```yaml
metadata:
  labels:
    kueue.x-k8s.io/queue-name: user-queue
```

### b. 配置资源需求 {#b-configure-the-resource-needs}

工作负载的资源需求可以在 `spec.rayClusterConfig` 中配置。

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

### c. Suspend 控制 {#c-suspend-control}

Kueue 控制 RayService 的 `spec.rayClusterConfig.suspend` 字段。当 RayService 被 Kueue 接纳时，Kueue 会通过将 `spec.rayClusterConfig.suspend` 设置为 `false` 来取消暂停，无论其之前的值是什么。

### d. 限制事项 {#c-limitations}
- 有限的 Worker Group：由于 Kueue 工作负载最多可以有 18 个 PodSet，所以 `spec.rayClusterConfig.workerGroupSpecs` 的最大数量为 17。
- 内建自动扩缩约束：自动扩缩仅支持[弹性](/zh-cn/docs/concepts/elastic_workload) RayService 对象。要启用内建自动扩缩：

  1. 启用 `ElasticJobsViaWorkloadSlices` 特性门控。
  2. 为 RayService 对象添加注解：

     ```yaml
     metadata:
       annotations:
         kueue.x-k8s.io/elastic-job: "true"
     ```
  3. 设置以下字段启用 RayService 的 Ray 自动扩缩器：

     ```yaml
     spec:
       rayClusterConfig:
         enableInTreeAutoscaling: true
     ```

- 滚动升级限制：Kueue 的工作负载切片特性目前仅管理单个活跃集群的配额。启用工作负载切片时，暂不支持创建二级临时集群的升级策略（`spec.upgradeStrategy.type: NewCluster` 或 `NewClusterWithIncrementalUpgrade`），因为待处理集群的 Pod 会保持被门控状态。要在使用工作负载切片的同时进行自动扩缩，请使用 `spec.upgradeStrategy.type: None` 或进行就地更新。

## RayService 示例{#example-rayservice}

RayService 如下所示：

{{< include "examples/jobs/ray-service-sample.yaml" "yaml" >}}

{{% alert title="注意" color="primary" %}}
上述示例来自[这里](https://raw.githubusercontent.com/ray-project/kuberay/v1.7.0/ray-operator/config/samples/ray-service.sample.yaml)，
仅添加了 `queue-name` 标签。
{{% /alert %}}