---
title: "运行普通 Pod"
linkTitle: "普通 Pod"
date: 2023-09-27
weight: 6
description: >
  以 Kueue 管理的作业来运行一个或一组 Pod。
---

本页面展示了如何在运行普通 Pod 时利用 Kueue 的调度和资源管理功能。
Kueue 支持管理[单个 Pod](#running-a-single-pod-admitted-by-kueue)
或[一组 Pod](#running-a-group-of-pods-to-be-admitted-together)。

本指南适用于对 Kueue 有基本了解的[批处理用户](/zh-CN/docs/tasks#batch-user)。更多信息请参见 [Kueue 概述](/zh-CN/docs/overview)。

## 开始之前

1. 默认情况下，`pod` 集成未启用。
   学习如何[使用自定义管理器配置安装 Kueue](/zh-CN/docs/installation/#install-a-custom-configured-released-version)
   并启用 `pod` 集成。

   为了允许 Kubernetes 系统 Pod 成功调度，您必须限制 `pod` 集成的范围。
   推荐的机制是使用 `managedJobsNamespaceSelector`。

   一种方法是仅对特定命名空间启用管理：
   ```yaml
   apiVersion: config.kueue.x-k8s.io/v1beta1
   kind: Configuration
   managedJobsNamespaceSelector:
     matchLabels:
      kueue-managed: "true"
   integrations:
     frameworks:
      - "pod"
   ```
   另一种方法是免除系统命名空间的管理：
   ```yaml
   apiVersion: config.kueue.x-k8s.io/v1beta1
   kind: Configuration
   managedJobsNamespaceSelector:
      matchExpressions:
      - key: kubernetes.io/metadata.name
        operator: NotIn
        values: [ kube-system, kueue-system ]
   integrations:
     frameworks:
      - "pod"
   ```

{{% alert title="注意" color="primary" %}}
  在 Kueue v0.10 之前，使用的是配置字段 `integrations.podOptions.namespaceSelector`
  和 `integrations.podOptions.podSelector`。`podOptions` 的使用在 Kueue v0.11 中已被弃用。
  用户应迁移到使用 `managedJobsNamespaceSelector`。
{{% /alert %}}


2. 如果启用了 Pod 集成，Kueue 将为所有创建的 Pod 运行 Webhook。Webhook namespaceSelector
   可用于过滤需要协调的 Pod。默认的 Webhook namespaceSelector 是：
   ```yaml
   matchExpressions:
   - key: kubernetes.io/metadata.name
     operator: NotIn
     values: [ kube-system, kueue-system ]
   ```
   
   当您[通过 Helm 安装 Kueue](/zh-CN/docs/installation/#install-via-helm) 时，Webhook
   命名空间选择器将匹配 `values.yaml` 中的 `managedJobsNamespaceSelector`。

   确保 namespaceSelector 永远不匹配 kueue 命名空间，否则
   Kueue 部署将无法创建 Pod。

3. 属于 Kueue 管理的其他 API 资源的 Pod 被排除在 `pod` 集成的队列之外。
   例如，由 `batch/v1.Job` 管理的 Pod 不会被 `pod` 集成管理。

4. 查看[管理集群配额](/zh-CN/docs/tasks/manage/administer_cluster_quotas)了解初始 Kueue 设置的详细信息。

## 运行由 Kueue 准入的单个 Pod

在 Kueue 上运行 Pod 时，请考虑以下方面：

### a. 队列选择

目标[本地队列](/zh-CN/docs/concepts/local_queue)应在 Pod 配置的 `metadata.labels` 部分中指定。

```yaml
metadata:
  labels:
    kueue.x-k8s.io/queue-name: user-queue
```

### b. 配置资源需求

工作负载的资源需求可以在 `spec.containers` 中配置。

```yaml
    - resources:
        requests:
          cpu: 3
```

### c. "managed" 标签

Kueue 将注入 `kueue.x-k8s.io/managed=true` 标签来指示哪些 Pod 由其管理。

### d. 限制

- Kueue 管理的 Pod 不能在 `kube-system` 或 `kueue-system` 命名空间中创建。
- 在[抢占](/docs/concepts/cluster_queue/#preemption)的情况下，Pod 将被终止和删除。

## 示例 Pod

这是一个仅休眠几秒钟的示例 Pod：

{{< include "examples/pods-kueue/kueue-pod.yaml" "yaml" >}}

您可以使用以下命令创建 Pod：
```sh
# 创建 Pod
kubectl create -f kueue-pod.yaml
```

## 运行一组一起被准入的 Pod

为了将一组 Pod 作为单个单元运行（称为 Pod 组），请一致地为组的所有成员添加
"pod-group-name" 标签和 "pod-group-total-count" 注解：

```yaml
metadata:
  labels:
    kueue.x-k8s.io/pod-group-name: "group-name"
  annotations:
    kueue.x-k8s.io/pod-group-total-count: "2"
```

### 功能限制

Kueue 仅提供运行 Pod 组所需的最小功能，
仅用于 Pod 由外部控制器直接管理而不需要 Job 级别 CRD 的环境。

作为此设计决策的结果，Kueue 不会重新实现 Kubernetes Job API 中可用的核心功能，
例如高级重试策略。特别是，Kueue 不会重新创建失败的 Pod。

此设计选择影响[抢占](/docs/concepts/cluster_queue/#preemption)场景。
当 Kueue 需要抢占表示 Pod 组的工作负载时，Kueue 会发送
删除组中所有 Pod 的请求。创建原始 Pod 的用户或控制器有责任创建替换 Pod。

{{% alert title="注意" color="primary" %}}
我们建议使用 Kubernetes Job API 或类似的 CRD，如
JobSet、MPIJob、RayJob（更多信息请参见[这里](/zh-CN/docs/tasks/#batch-user)）。
{{% /alert %}}

### 终止

当成功的 Pod 数量等于 Pod 组大小时，Kueue 认为 Pod 组成功，
并将相关的工作负载标记为已完成。

如果 Pod 组不成功，您可以使用两种方式来终止 Pod 组的执行以释放保留的资源：
1. 对工作负载对象发出删除请求。Kueue 将终止所有剩余的 Pod。
2. 在组中至少一个 Pod（可以是替换 Pod）上设置 `kueue.x-k8s.io/retriable-in-group: false` 注解。
   Kueue 将在所有 Pod 终止后将工作负载标记为已完成。

### 示例 Pod 组

这是一个仅休眠几秒钟的示例 Pod 组：

{{< include "examples/pods-kueue/kueue-pod-group.yaml" "yaml" >}}

您可以使用以下命令创建 Pod 组：
```sh
kubectl create -f kueue-pod-group.yaml
```

Kueue 创建的相关工作负载的名称等于 Pod 组的名称。在此示例中为 `sample-group`，
您可以使用以下命令检查工作负载：
```sh
kubectl describe workload/sample-group
```
