---
title: "设置 manageJobsWithoutQueueName"
date: 2024-06-28
weight: 10
description: >
  manageJobsWithoutQueueName 和 mangedJobsNamespaceSelector 可用于控制是否允许没有分配 LocalQueues 的工作负载进入。
---

本页面描述了如何配置 Kueue，以确保所有在命名空间中提交的、旨在供**批处理用户**使用的
Workload 都将由 Kueue 管理，即使它们缺少 `kueue.x-k8s.io/queue-name` 标签。

## 在你开始之前

学习如何[安装带有自定义管理器配置的 Kueue](/zh-CN/docs/installation/#install-a-custom-configured-released-version)。

## 配置

你需要修改管理配置，将 `manageJobsWithoutQueueName` 设置为 true。

如果你还修改默认配置以启用 `Pod`、`Deployment` 或 `StatefulSet` 集成，
你可能还需要覆盖 `managedJobsNamespaceSelector` 的默认值，以限制 `manageJobsWithoutQueueName` 的作用范围，
仅应用于**批处理用户**命名空间。

`managedJobsNamespaceSelector` 的默认值是：

```yaml
matchExpressions:
- key: kubernetes.io/metadata.name
  operator: NotIn
  values: [ kube-system, kueue-system ]
```

这个默认值将 `kube-system` 和 `kueue-system` 命名空间排除在管理之外；
当 `manageJobsWithoutQueueName` 为 true 时，所有其他命名空间都将由 Kueue 管理。

或者，**批处理管理员**可以标记用于**批处理用户**的命名空间，
并定义仅匹配那些命名空间的选择器。例如：

```yaml
managedJobsNamespaceSelector:
  matchLabels:
    kueue.x-k8s.io/managed-namespace: true
```

## 预期行为

在所有匹配命名空间选择器的命名空间中，任何未带有 `kueue.x-k8s.io/queue-name`
标签的 Workload 将被挂起。这些 Workload 在被编辑以添加
`kueue.x-k8s.io/queue-name` 标签之前，不会被 Kueue 考虑准入。
