---
title: "Pod 故障排除"
date: 2024-03-21
weight: 4
description: >
  Pod 或 Pod 组状态的故障排除
---

本文档介绍如何排除由 Kueue 直接管理的[普通 Pod](/docs/tasks/run/plain_pods/) 的故障，
换句话说，就是不由 Kubernetes Job 或支持的 CRD 管理的 Pod。

{{% alert title="注意" color="primary" %}}
本文档重点介绍 Kueue 管理 Pod 时与其他作业集成不同的行为。
你可以阅读 [Job 故障排除](troubleshooting_jobs)了解更多通用的故障排除步骤。
{{% /alert %}}

## 我的 Pod 是否由 Kueue 直接管理？ {#is-my-pod-directly-managed-by-kueue}

Kueue 向其管理的 Pod 添加标签 `kueue.x-k8s.io/managed`，值为 `true`。
如果 Pod 上没有此标签，意味着 Kueue 不会直接准入或统计此 Pod 的资源使用情况。

Pod 可能没有 `kueue.x-k8s.io/managed` 标签的原因如下：

1. [Pod 集成被禁用](/docs/tasks/run/plain_pods/#before-you-begin)。
2. Pod 所属的命名空间不满足
   [`managedJobsNamespaceSelector`](/docs/reference/kueue-config.v1beta1/#Configuration)
   的要求。
3. Pod 由 Kueue 管理的 Job 或等效 CRD 拥有。
4. Pod 没有 `kueue.x-k8s.io/queue-name` 标签，并且
   [`manageJobsWithoutQueueName`](/docs/reference/kueue-config.v1beta1/#Configuration)
   设置为 `false`。

{{% alert title="注意" color="primary" %}}
在 Kueue v0.10 之前，使用配置字段 `integrations.podOptions.namespaceSelector`。
在 Kueue v0.11 中，`podOptions` 的使用被弃用。
用户应该迁移到使用 `managedJobsNamespaceSelector`。
{{% /alert %}}

## 识别你的 Pod 对应的 Workload {#identifying-the-workload-for-your-pod}

当使用 [Pod 组](/docs/tasks/run/plain_pods/#running-a-group-of-pods-to-be-admitted-together)时，
Workload 的名称与标签 `kueue.x-k8s.io/pod-group-name` 的值匹配。

当使用[单个 Pod](/docs/tasks/run/plain_pods/#running-a-single-pod-admitted-by-kueue)时，
你可以按照[识别 Job 的 Workload](troubleshooting_jobs/#identifying-the-workload-for-your-job)
指南来识别其对应的 Workload。

## 为什么我的 Pod 组没有 Workload？ {#why-doesnt-a-workload-exist-for-my-pod-group}

在创建 Workload 对象之前，Kueue 期望为该组创建所有 Pod。
Pod 应该都具有标签 `kueue.x-k8s.io/pod-group-name` 的相同值，
并且 Pod 的数量应该等于注解 `kueue.x-k8s.io/pod-group-total-count` 的值。

你可以运行以下命令来识别 Kueue 是否为 Pod 创建了 Workload：

```bash
kubectl describe pod my-pod -n my-namespace
```

如果 Kueue 没有创建 Workload 对象，你将看到类似以下的输出：

```text
...
Events:
  Type     Reason              Age   From                  Message
  ----     ------              ----  ----                  -------
  Warning  ErrWorkloadCompose  14s   pod-kueue-controller  'my-pod-group' group has fewer runnable pods than expected
```

{{% alert title="注意" color="primary" %}}
上述事件可能会在 Kueue 观察到的第一个 Pod 上显示，即使 Kueue 稍后成功为 Pod 组创建了 Workload，
该事件也会保留。
{{% /alert %}}

一旦 Kueue 观察到该组的所有 Pod，你将看到类似以下的输出：

```text
...
Events:
  Type     Reason              Age   From                  Message
  ----     ------              ----  ----                  -------
  Normal   CreatedWorkload     14s   pod-kueue-controller  Created Workload: my-namespace/my-pod-group
```

## 为什么我的 Pod 消失了？ {#why-did-my-pod-disappear}

当你启用[抢占](/docs/concepts/cluster_queue/#preemption)时，
Kueue 可能会抢占 Pod 以容纳更高优先级的作业或回收配额。
抢占通过 `DELETE` 调用实现，这是在 Kubernetes 中终止 Pod 的标准方式。

当使用单个 Pod 时，Kubernetes 会与 Pod 一起删除 Workload 对象，
因为没有其他东西持有对它的所有权。

Kueue 通常不会在抢占时完全删除 Pod 组中的 Pod。
请参阅下一个问题以了解 Pod 组中 Pod 的删除机制。

## 为什么 Pod 组中的 Pod 在 Failed 或 Succeeded 时不会被删除？ {#why-arent-pods-in-a-pod-group-deleted-when-failed-or-succeeded}

当使用 Pod 组时，Kueue 保留一个 [finalizer](https://kubernetes.io/docs/concepts/overview/working-with-objects/finalizers/)
`kueue.x-k8s.io/managed` 来防止 Pod 被删除，并能够跟踪组的进度。
你不应该手动修改 finalizer。

Kueue 会在以下情况下从 Pod 中移除 finalizer：

- 组满足[终止](/docs/tasks/run/plain_pods/#termination)条件，例如，
  当所有 Pod 成功终止时。
- 对于 Failed 的 Pod，当 Kueue 观察到替换 Pod 时。
- 你删除 Workload 对象时。

一旦 Pod 没有任何 finalizer，Kubernetes 将根据以下条件删除 Pod：

- 用户或控制器是否已发出 Pod 删除请求。
- [Pod 垃圾收集器](https://kubernetes.io/docs/concepts/workloads/pods/pod-lifecycle/#pod-garbage-collection)。
