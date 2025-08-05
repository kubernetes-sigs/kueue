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

本指南适用于[为用户提供服务](/zh-CN/docs/tasks#serving-user)并对 Kueue 有基本理解的人们。
有关更多信息，请参见 [Kueue 概述](/zh-CN/docs/overview)。

## 开始之前 {#before-you-begin}

1. 学习如何[安装 Kueue 并使用自定义管理器配置](/zh-CN/docs/installation/#install-a-custom-configured-released-version)。

2. 确保你已启用 `leaderworkerset.x-k8s.io/leaderworkerset` 集成，例如：
   ```yaml
   apiVersion: config.kueue.x-k8s.io/v1beta1
   kind: Configuration
   integrations:
     frameworks:
      - "pod"
      - "leaderworkerset.x-k8s.io/leaderworkerset"
   ```
   同时，请参见[运行普通 Pod](/zh-CN/docs/tasks/run/plain_pods/#before-you-begin) 中所述的步骤，
   了解如何启用和配置 Pod 集成。

3. 查阅[管理集群配额](/zh-CN/docs/tasks/manage/administer_cluster_quotas)了解初始 Kueue 设置的详细信息。

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
kubectl create -f sample-leaderworkerset.yaml
```
