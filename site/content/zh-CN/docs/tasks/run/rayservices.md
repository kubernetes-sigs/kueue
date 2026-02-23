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

Kueue 通过为 RayService 创建的 RayCluster 来管理 RayService。
因此，RayService 需要在 metadata.labels 中包含 `kueue.x-k8s.io/queue-name: user-queue` 标签，该标签会被传递到相应的 RayCluster，以触发 Kueue 的管理。

本指南面向对 Kueue 有基本了解的、[对外提供服务的用户](/zh-CN/docs/tasks#serving-user)。
更多信息，请参见 [Kueue 概览](/zh-CN/docs/overview)。

## 开始之前 {#before-you-begin}

1. 请确保你使用的是 Kueue v0.6.0 版本或更高版本，以及 KubeRay v1.3.0 或更高版本。

2. 请参见 [管理集群配额](/zh-CN/docs/tasks/manage/administer_cluster_quotas)了解初始 Kueue 设置的详细信息。

3. 请参见 [KubeRay 安装说明](https://docs.ray.io/en/latest/cluster/kubernetes/getting-started/raycluster-quick-start.html#step-2-deploy-a-kuberay-operator)了解 KubeRay 的安装和配置详情。

{{% alert title="注意" color="primary" %}}
RayService 通过 RayCluster 由 Kueue 管理；
在 v0.8.1 之前，你需要在完成安装后重启 Kueue 才能使用 RayCluster。你可以通过运行
`kubectl delete pods -l control-plane=controller-manager -n kueue-system` 来完成此操作。
{{% /alert %}}

## RayService 定义 {#rayservice-definition}

在 Kueue 上运行 [RayService](https://docs.ray.io/en/latest/cluster/kubernetes/getting-started/rayservice-quick-start.html)
时，请考虑以下方面：

### a. 队列选择 {#a-queue-selection}

目标 [本地队列](/zh-CN/docs/concepts/local_queue)应在 RayService 配置的 `metadata.labels`
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

Kueue 控制由 RayService 创建的 RayCluster 的 `spec.suspend` 字段。当 RayCluster 被 Kueue 接纳时，Kueue 会通过将 `spec.suspend` 设置为 `false` 来取消暂停，无论其之前的值是什么。

### d. 限制事项 {#c-limitations}
- 有限的 Worker Group：由于 Kueue 工作负载最多可以有 8 个 PodSet,
  所以`spec.rayClusterConfig.workerGroupSpecs` 的最大数量为 7。
- 内建自动扩缩禁用：Kueue 管理 RayService 的资源分配，因此，集群的内部自动扩缩机制需要禁用。

## RayService 示例{#example-rayservice}

RayService 如下所示：

{{< include "examples/jobs/ray-service-sample.yaml" "yaml" >}}

{{% alert title="注意" color="primary" %}}
上述示例来自[这里](https://raw.githubusercontent.com/ray-project/kuberay/v1.4.2/ray-operator/config/samples/ray-service.sample.yaml)，
仅添加了 `queue-name` 标签并更新了请求。
{{% /alert %}}