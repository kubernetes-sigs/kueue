---
title: "运行 LeaderWorkerSet"
linkTitle: "LeaderWorkerSet"
date: 2025-02-17
weight: 6
description: 将 LeaderWorkerSet 作为由 Kueue 管理的工作负载运行
---

本页面展示了如何通过运行 [LeaderWorkerSet](https://github.com/kubernetes-sigs/lws)
来利用 Kueue 的调度和资源管理能力。

我们演示了如何支持调度 LeaderWorkerSet，
其中一组 Pod 构成由工作负载所表示的准入单元。
这允许逐组扩大和缩小 LeaderWorkerSet。

此集成基于[普通的 Pod 组](https://kueue.sigs.k8s.io/docs/tasks/run/plain_pods/)集成。

本指南适用于对 Kueue 有基本了解的[服务用户](/zh-CN/docs/tasks#serving-user)。
有关更多信息，请参见 [Kueue 概述](/zh-CN/docs/overview)。

## 开始之前 {#before-you-begin}

1. `leaderworkerset.x-k8s.io/leaderworkerset` 集成默认启用。

2. 对于 Kueue v0.15 及更早版本，学习如何[安装 Kueue 并使用自定义管理器配置](/zh-CN/docs/installation/#install-a-custom-configured-released-version)，
   并确保你已启用 `leaderworkerset.x-k8s.io/leaderworkerset` 集成，例如：

   ```yaml
   apiVersion: config.kueue.x-k8s.io/v1beta2
   kind: Configuration
   integrations:
     frameworks:
      - "leaderworkerset.x-k8s.io/leaderworkerset"
   ```
   {{% alert title="Pod 集成要求" color="primary" %}}
   自 Kueue v0.15 起，你不需要显式启用 `"pod"` 集成即可使用 `"leaderworkerset.x-k8s.io/leaderworkerset"` 集成。
   
   对于 Kueue v0.14 及更早版本，必须显式启用 `"pod"` 集成。
   
   有关配置详情，请参阅[运行普通 Pod](/zh-CN/docs/tasks/run/plain_pods/#before-you-begin)。
   {{% /alert %}}
   
3. 查看[管理集群配额](/zh-CN/docs/tasks/manage/administer_cluster_quotas)了解初始 Kueue 设置的详细信息。

## 运行一个由 Kueue 准入的 LeaderWorkerSet {#running-a-leaderworkerset-admitted-by-kueue}

运行 LeaderWorkerSet 时，请考虑以下方面：

### a. 队列选择 {#a-queue-selection}

目标[本地队列](/zh-CN/docs/concepts/local_queue)应在 LeaderWorkerSet 配置的 `metadata.labels` 部分指定。

```yaml
metadata:
   labels:
      kueue.x-k8s.io/queue-name: user-queue
```

### b. 配置资源需求 {#b-configure-the-resource-needs}
工作负载的资源需求可以在 `spec.template.spec.containers` 中配置。

```yaml
spec:
  leaderWorkerTemplate:
    leaderTemplate:
      spec:
        containers:
          - resources:
              requests:
                cpu: "100m"
    workerTemplate:
      spec:
        containers:
          - resources:
              requests:
                cpu: "100m"
```

### c. 扩缩 {#c-scaling}

你可以对 LeaderWorkerSet `.spec.replicas` 执行扩缩操作。

扩缩的单位是 LWS 组。通过更改 LWS 中的 `replicas` 数量，
你可以创建或删除整个 Pod 组。
扩容后，新建的 Pod 组会由调度门控挂起，直到相应的工作负载被准入为止。

## 示例 {#examples}
以下是一个 LeaderWorkerSet 的示例：

{{< include "examples/serving-workloads/sample-leaderworkerset.yaml" "yaml" >}}

你可以使用以下命令创建 LeaderWorkerSet：

```sh
kubectl create -f https://kueue.sigs.k8s.io/examples/serving-workloads/sample-leaderworkerset.yaml
```

## 配置拓扑感知调度 {#configure-topology-aware-scheduling}

对于大规模推理或分布式训练等性能敏感型工作负载，你可能需要将 Leader 和 Worker Pod
放置在特定网络拓扑域（例如机架或数据中心区块）内，以最大程度地减少延迟。

Kueue 支持通过从 Pod 模板读取注解来为 LeaderWorkerSet 提供拓扑感知调度（Topology Aware Scheduling，TAS）。
要启用此功能：

- [为拓扑感知调度配置集群](/zh-CN/docs/concepts/topology_aware_scheduling)。
- 向 `leaderTemplate` 和 `workerTemplate` 添加 `kueue.x-k8s.io/podset-required-topology` 注解。
- 向 `leaderTemplate` 和 `workerTemplate` 添加 `kueue.x-k8s.io/podset-group-name`
  注解，并使用相同的值。这可以确保 Leader 和 Workers 被调度到同一拓扑域。

### 示例：机架级共置

以下示例使用 `podset-group-name` 注解来确保 Leader 和所有 Worker
被调度到同一机架内（由 `cloud.provider.com/topology-rack` 标签表示）。

{{< include "examples/serving-workloads/sample-leaderworkerset-tas.yaml" "yaml" >}}

当 `replicas` 大于 1 时（如上面的示例中 `replicas: 2`），拓扑约束适用于每个副本。
这意味着对于每个副本，Leader 及其 Workers 将被共置在同一拓扑域（例如机架）中，
但不同的副本可能被分配到不同的拓扑域。

## MultiKueue

有关在 MultiKueue 环境中运行 LeaderWorkerSet 的详细信息，请查看 [MultiKueue](/zh-CN/docs/tasks/run/multikueue/leaderworkerset)。
