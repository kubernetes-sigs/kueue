---
title: "运行 RayService"
linkTitle: "RayServices"
date: 2025-09-25
weight: 6
description: 在启用了 Kueue 的环境里运行 RayServices
---

本页面展示了如何利用 Kueue 的调度和服务管理能力来运行[RayService](https://docs.ray.io/en/latest/cluster/kubernetes/getting-started/rayservice-quick-start.html)。

Kueue 通过为其创建的 RayCluster 来管理 RayService。因此，RayService 需要 `kueue.x-k8s.io/queue-name: user-queue` 标签，并且此标签会传播到相关的 RayCluster 以触发 Kueue 的管理。

本指南适用于对 Kueue 有基本了解的[服务用户](/zh-CN/docs/tasks#serving-users)。更多信息，请参见 [Kueue 概览](/zh-CN/docs/overview)。

## 准备工作

1. 确保您使用的是 Kueue v0.6.0 或更高版本以及 KubeRay v1.3.0 或更高版本。

2. 有关 Kueue 初始设置的详细信息，请查看[管理集群配额](/zh-CN/docs/tasks/manage/administer_cluster_quotas)。

3. 有关 KubeRay 的安装和配置详细信息，请参见 [KubeRay 安装](https://docs.ray.io/en/latest/cluster/kubernetes/getting-started/kuberay-operator-installation.html)。

{{% alert title="注意" color="primary" %}}
在 v0.8.1 之前，你需要重启 Kueue 才能使用 RayService。你可以通过运行 `kubectl delete pods -l control-plane=controller-manager -n kueue-system` 来完成此操作。
{{% /alert %}}

## RayService 定义 {#rayservice-definition}

在 Kueue 上运行 [RayService](https://docs.ray.io/en/latest/cluster/kubernetes/getting-started/rayservice-quick-start.html) 时，请考虑以下方面：

### a. 队列选择 {#a-queue-selection}

目标[本地队列](/zh-CN/docs/concepts/local_queue)应在 RayService 配置的 `metadata.labels` 部分中指定，并且此标签将传播到其 RayCluster。

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

### c. 限制 {#c-limitations}
- 有限的工作组：由于一个 Kueue 工作负载最多可以有 8 个 PodSet，`spec.rayClusterConfig.workerGroupSpecs` 的最大数量为 7。
- 禁用内置自动扩容：Kueue 管理 RayService 的资源分配；因此，需要禁用内部自动扩容机制。

## 示例 RayService

RayService 如下所示：

{{< include "examples/jobs/ray-service-sample.yaml" "yaml" >}}

{{% alert title="注意" color="primary" %}}
以上示例来自[这里](https://raw.githubusercontent.com/ray-project/kuberay/v1.4.2/ray-operator/config/samples/ray-service.sample.yaml)，
仅添加了 `queue-name` 标签并更新了请求。
{{% /alert %}}
