---
title: "运行普通 Pod"
linkTitle: "普通 Pod"
date: 2023-09-27
weight: 6
description: 在启用了kueue的环境里运行 Pod
---

本页面展示了如何利用 Kueue 的调度和管理能力来运行普通 Pod。Kueue 支持管理
[单个 Pod](#运行一个被 Kueue 管理的 Pod) 或
[Pod 组](#运行一组需要被 Kueue 管理的 Pod)。

本指南适用于 [批处理用户](/docs/tasks#batch-user)，他们需要对 Kueue 有基本的了解。更多信息，请参见 [Kueue 概览](/docs/overview)。

## 开始之前

1. 默认情况下，`pod` 集成未启用。
   了解如何 [安装 Kueue 并使用自定义管理器配置](/docs/installation/#install-a-custom-configured-released-version)
   并启用 `pod` 集成。

   为了允许 Kubernetes 系统 Pod 成功调度，您必须限制 `pod` 集成的范围。
   推荐机制是使用 `managedJobsNamespaceSelector`。

   一种方法是只启用特定命名空间的托管：
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
   另一种方法是豁免系统命名空间：
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
  在 Kueue v0.10 之前，Configuration 字段 `integrations.podOptions.namespaceSelector`
  和 `integrations.podOptions.podSelector` 被使用。Kueue v0.11 中已弃用 `podOptions`。用户应迁移到使用 `managedJobsNamespaceSelector`。
{{% /alert %}}


2. 如果启用了 `pod` 集成，Kueue 将为所有创建的 Pod 运行 webhook。webhook 的 namespaceSelector 可以
   用于过滤需要调谐的 Pod。默认的 webhook namespaceSelector 是：
   ```yaml
   matchExpressions:
   - key: kubernetes.io/metadata.name
     operator: NotIn
     values: [ kube-system, kueue-system ]
   ```
   
   当您 [通过 Helm 安装 Kueue](/docs/installation/#install-via-helm) 时，webhook namespaceSelector 
   将匹配 `values.yaml` 中的 `managedJobsNamespaceSelector`。

   请确保 namespaceSelector 永远不会匹配 kueue 命名空间，否则
   Kueue 部署将无法创建 Pod。

3. Pods 属于 Kueue 管理的其他 API 资源时，`pod` 集成不会将其加入队列。
   例如，`batch/v1.Job` 管理的 Pod 不会被 `pod` 集成管理。

4. 请参见 [管理集群配额](/docs/tasks/manage/administer_cluster_quotas) 了解 Kueue 初始设置的详细信息。

## 运行一个被 Kueue 管理的 Pod

运行 Kueue Pods 时，请考虑以下方面：

### a. 队列选择

Pod 配置的 `metadata.labels` 部分应指定目标 [本地队列](/docs/concepts/local_queue)。

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

Kueue 将注入 `kueue.x-k8s.io/managed=true` 标签，以指示哪些 Pod 由其管理。

### d. 限制

- 一个 Kueue 管理的 Pod 不能在 `kube-system` 或 `kueue-system` 命名空间中创建。
- 在 [抢占](/docs/concepts/cluster_queue/#preemption) 情况下，Pod 将被终止并删除。

## 示例 Pod

以下是一个简单的示例 Pod，它只是休眠几秒钟：

{{< include "examples/pods-kueue/kueue-pod.yaml" "yaml" >}}

您可以使用以下命令创建 Pod：
```sh
# 创建 Pod
kubectl create -f kueue-pod.yaml
```

## 运行一组需要被 Kueue 管理的 Pod

为了将一组 Pod 作为一个单元运行，称为 Pod 组，请在组的所有成员中添加
"pod-group-name" 标签和 "pod-group-total-count" 注释，保持一致：

```yaml
metadata:
  labels:
    kueue.x-k8s.io/pod-group-name: "group-name"
  annotations:
    kueue.x-k8s.io/pod-group-total-count: "2"
```

### 功能限制

Kueue 仅提供运行 Pod 组的最小必要功能，仅适用于 Pods 由外部
控制器直接管理的环境，无需 Job 级 CRD。

由于此设计决策，Kueue 不会重新实现 Kubernetes Job API 中可用的核心
功能，例如高级重试策略。特别是，Kueue 不会重新创建失败的 Pod。

此设计选择影响了
[抢占](/docs/concepts/cluster_queue/#preemption) 场景。当 Kueue 需要抢占一个表示 Pod 组的负载时，kueue 发送
删除请求以删除组中的所有 Pod。这是创建原始 Pods 的用户或控制器的责任，以创建替换 Pods。

{{% alert title="注意" color="primary" %}}
我们建议使用 Kubernetes Job API 或类似的 CRD，如
JobSet、MPIJob、RayJob（请参见更多 [这里](/docs/tasks/#batch-user)）。
{{% /alert %}}

### 终止

Kueue 认为 Pod 组成功，并标记关联的 Workload 完成，当成功 Pod 的数量等于 Pod 组大小时。

如果 Pod 组不成功，您可能希望使用以下两种方法之一来终止 Pod 组的执行以释放预留资源：
1. 对 Workload 对象发出删除请求。Kueue 将终止所有剩余的 Pod。
2. 在组中至少设置一个 Pod 的 `kueue.x-k8s.io/retriable-in-group: false` 注释（可以是替换 Pod）。Kueue 将标记工作负载
   一旦所有 Pod 终止，就完成。

### 示例 Pod 组

以下是一个简单的示例 Pod 组，它只是休眠几秒钟：

{{< include "examples/pods-kueue/kueue-pod-group.yaml" "yaml" >}}

您可以使用以下命令创建 Pod 组：
```sh
kubectl create -f kueue-pod-group.yaml
```

Kueue 创建的关联 Workload 的名称等于 Pod 组的名称。在此示例中，它是 `sample-group`，您可以使用以下命令检查工作负载：
```sh
kubectl describe workload/sample-group
```
