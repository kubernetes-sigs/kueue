---
title: "运行 RayCluster"
linkTitle: "RayClusters"
date: 2024-08-07
weight: 6
description: 在启用了kueue的环境里运行 RayClusters
---

本页面展示了如何利用 Kueue 的调度和服务管理能力来运行 [RayCluster](https://docs.ray.io/en/latest/cluster/kubernetes/getting-started/raycluster-quick-start.html)。

本指南适用于 [批处理用户](/docs/tasks#batch-user)，他们需要对 Kueue 有基本的了解。更多信息，请参见 [Kueue 概述](/docs/overview)。

## 在开始之前

1. 请确保您使用的是 Kueue v0.6.0 版本或更高版本，以及 KubeRay v1.1.0 或更高版本。

2. 请参见 [Administer cluster quotas](/docs/tasks/manage/administer_cluster_quotas) 了解初始 Kueue 设置的详细信息。

3. 请参见 [KubeRay Installation](https://docs.ray.io/en/latest/cluster/kubernetes/getting-started/raycluster-quick-start.html#step-2-deploy-a-kuberay-operator) 了解 KubeRay 的安装和配置详情。

{{% alert title="Note" color="primary" %}}
在 v0.8.1 之前，您需要重启 Kueue 才能使用 RayCluster。您可以通过运行 `kubectl delete pods -l control-plane=controller-manager -n kueue-system` 来完成此操作。
{{% /alert %}}

## RayCluster 定义

当在 Kueue 上运行 [RayClusters](https://docs.ray.io/en/latest/cluster/kubernetes/getting-started/raycluster-quick-start.html) 时，请考虑以下方面：

### a. 队列选择

目标 [本地队列](/docs/concepts/local_queue) 应在 RayCluster 配置的 `metadata.labels` 部分指定。

```yaml
metadata:
  labels:
    kueue.x-k8s.io/queue-name: user-queue
```

### b. 配置资源需求

工作负载的资源需求可以在 `spec` 中配置。

```yaml
spec:
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

请注意，RayCluster 在存在期间会占用资源配额。为了优化资源管理，您应该删除不再使用的 RayCluster。

### c. 限制
- 有限的 Worker Groups：由于 Kueue 工作负载最多可以有 8 个 PodSets，`spec.workerGroupSpecs` 的最大数量为 7
- 内建自动扩缩禁用：Kueue 管理 RayCluster 的资源分配；因此，集群的内部自动扩缩机制需要禁用

## 示例 RayCluster

RayCluster 如下所示：

{{< include "examples/jobs/ray-cluster-sample.yaml" "yaml" >}}

您可以使用 [CLI](https://docs.ray.io/en/latest/cluster/running-applications/job-submission/quickstart.html) 提交 Ray Job，或者登录 Ray Head 并按照此 [示例](https://ray-project.github.io/kuberay/deploy/helm-cluster/#end-to-end-example) 在 kind 集群中执行作业。

{{% alert title="Note" color="primary" %}}
上述示例来自 [这里](https://raw.githubusercontent.com/ray-project/kuberay/v1.1.1/ray-operator/config/samples/ray-cluster.complete.yaml)
，仅添加了 `queue-name` 标签并更新了请求。
{{% /alert %}}