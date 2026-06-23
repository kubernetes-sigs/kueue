---
title: "队列故障排除"
date: 2024-03-21
weight: 2
description: >
  LocalQueue 或 ClusterQueue 状态的故障排除
---

## 为什么 LocalQueue 中没有 workload 被准入？ {#why-no-workloads-are-admitted-in-the-localqueue}

[LocalQueue](/docs/concepts/local_queue) 的状态包含 LocalQueue 上任何配置问题的详细信息，
作为 `Active` 条件的一部分。

运行以下命令查看 LocalQueue 的状态：

```bash
kubectl get localqueue -n my-namespace my-local-queue -o yaml
```

LocalQueue 的状态将类似于以下内容：

```yaml
status:
  admittedWorkloads: 0
  conditions:
  - lastTransitionTime: "2024-05-03T18:57:32Z"
    message: Can't submit new workloads to clusterQueue
    reason: ClusterQueueIsInactive
    status: "False"
    type: Active
```

在上面的示例中，`Active` 条件的状态为 `False`，因为 ClusterQueue 不活跃。

## 为什么 ClusterQueue 中没有 workload 被准入？ {#why-no-workloads-are-admitted-in-the-clusterqueue}

[ClusterQueue](/docs/concepts/cluster_queue) 的状态包含 ClusterQueue 上任何配置问题的详细信息，
作为 `Active` 条件的一部分。

运行以下命令查看 ClusterQueue 的状态：

```bash
kubectl get clusterqueue my-clusterqueue -o yaml
```

ClusterQueue 的状态将类似于以下内容：

```yaml
status:
  admittedWorkloads: 0
  conditions:
  - lastTransitionTime: "2024-05-03T18:22:30Z"
    message: 'Can''t admit new workloads: FlavorNotFound'
    reason: FlavorNotFound
    status: "False"
    type: Active
```

在上面的示例中，`Active` 条件的状态为 `False`，因为配置的 flavor 不存在。
阅读[管理 ClusterQueue](/docs/tasks/manage/administer_cluster_quotas)了解如何配置 ClusterQueue。

如果 ClusterQueue 配置正确，状态将类似于以下内容：

```yaml
status:
  admittedWorkloads: 1
  conditions:
  - lastTransitionTime: "2024-05-03T18:35:28Z"
    message: Can admit new workloads
    reason: Ready
    status: "True"
    type: Active
```

如果 ClusterQueue 的 `Active` 条件状态为 `True`，但你仍然没有观察到 workload 被准入，
那么问题更可能出现在单个 workload 上。
阅读 [Job 故障排除](/docs/tasks/troubleshooting/troubleshooting_jobs)了解为什么单个作业无法被准入。
