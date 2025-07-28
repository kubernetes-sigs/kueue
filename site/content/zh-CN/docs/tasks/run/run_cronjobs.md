---
title: "运行 CronJob"
linkTitle: "Kubernetes CronJobs"
date: 2023-12-12
weight: 5
description: 在启用了 Kueue 的环境里运行 CronJob
---

本页面展示了如何在 Kubernetes 集群中运行带有 Kueue 的 CronJob。

本页面的受众是[批处理用户](/zh-CN/docs/tasks#batch-user)。

## 开始之前 {#before-you-begin}

请确保满足以下条件：

- 已运行 Kubernetes 集群。
- kubectl 命令行工具已与你的集群建立通信。
- [Kueue 安装文档](/zh-CN/docs/installation)。
- 集群已配置 [配额](/zh-CN/docs/tasks/administer_cluster_quotas)。

## 0. 识别你命名空间中的可用队列 {#0-identify-the-queues-available-in-your-namespace}

运行以下命令以列出你命名空间中的 `LocalQueues`。

```shell
kubectl -n default get localqueues
# 或使用 'queues' 别名。
kubectl -n default get queues
```

输出类似于以下内容：

```bash
NAME         CLUSTERQUEUE    PENDING WORKLOADS
user-queue   cluster-queue   3
```

[ClusterQueue](/zh-CN/docs/concepts/cluster_queue) 定义了队列的配额。

## 1. 定义作业 {#1-define-the-job}

在 Kueue 中运行 CronJob 与在 Kubernetes 集群中运行 CronJob 类似，
但你必须考虑以下差异：

- 你应该在 CronJob 中将 JobTemplate 设置为[暂停状态](https://kubernetes.io/docs/concepts/workloads/controllers/job/#suspending-a-job)，
  因为 Kueue 将决定何时启动 Job。
- 你必须设置要提交 Job 的队列。在 `jobTemplate.metadata` 中添加 `kueue.x-k8s.io/queue-name` 标签。
- 你应该为每个 Job Pod 包含资源请求。
- 你应该设置 [`spec.concurrencyPolicy`](https://kubernetes.io/docs/concepts/workloads/controllers/cron-jobs/#concurrency-policy)
  以控制并发策略。默认是 `Allow`。你也可以将其设置为 `Forbid` 以防止并发运行。
- 你应该设置 [`spec.startingDeadlineSeconds`](https://kubernetes.io/docs/concepts/workloads/controllers/cron-jobs/#starting-deadline)
  以控制 Job 启动的截止日期。默认没有截止日期。

以下是包含三个 Pod 的 CronJob 示例，这些 Pod 仅睡眠 10 秒。CronJob 每分钟运行一次。

{{< include "examples/jobs/sample-cronjob.yaml" "yaml" >}}

## 2. 运行 CronJob {#2-run-the-cronjob}

你可以使用以下命令运行 CronJob：

```shell
kubectl create -f sample-cronjob.yaml
```

内部，Kueue 将为每次 Job 运行创建一个对应 [Workload](/zh-CN/docs/concepts/workload)
其名称与 Job 匹配。

```shell
kubectl -n default get workloads
```

输出类似于以下内容：

```shell
NAME                                QUEUE        ADMITTED BY     AGE
job-sample-cronjob-28373362-0133d   user-queue   cluster-queue   69m
job-sample-cronjob-28373363-e2aa0   user-queue   cluster-queue   68m
job-sample-cronjob-28373364-b42ac   user-queue   cluster-queue   67m
```

你还可以[监控 Workload 的状态](/zh-CN/docs/tasks/run_jobs#3-optional-monitor-the-status-of-the-workload)。
