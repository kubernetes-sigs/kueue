---
title: "Run A Kubernetes Job"
linkTitle: "Kubernetes Jobs"
date: 2022-02-14
weight: 4
description: 在启用了kueue的环境里运行 Jobs
---

本页面展示了如何在 Kubernetes 集群中运行带有 Kueue 的 Job。

本页面的受众是 [批处理用户](/docs/tasks#batch-user)。

## 在开始之前

请确保满足以下条件：

- 运行中的 Kubernetes 集群。
- kubectl 命令行工具与您的集群通信正常。
- [Kueue 已安装](/docs/installation)。
- 集群已配置 [配额](/docs/tasks/manage/administer_cluster_quotas)。

以下图片展示了您在本教程中将交互的所有概念：

![Kueue Components](/images/queueing-components.svg)

## 0. 识别您命名空间中的队列

运行以下命令以列出您命名空间中的 `LocalQueues`。

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

[ClusterQueue](/docs/concepts/cluster_queue) 定义了队列的配额。

## 1. 定义 Job

在 Kueue 中运行 Job 与在 Kubernetes 集群中运行 Job 类似，但您必须考虑以下差异：

- 您应该在 [挂起状态](https://kubernetes.io/docs/concepts/workloads/controllers/job/#suspending-a-job) 下创建 Job，
  因为 Kueue 将决定何时启动 Job 最佳。
- 您必须设置要提交 Job 的队列。使用 `kueue.x-k8s.io/queue-name` 标签。
- 您应该为每个 Job Pod 包含资源请求。

以下是包含三个 Pod 的 Job，这些 Pod 只是短暂休眠。

{{< include "examples/jobs/sample-job.yaml" "yaml" >}}

## 2. 运行 Job

您可以使用以下命令运行 Job：

```shell
kubectl create -f sample-job.yaml
```

Kueue 内部会为该 Job 创建一个对应名称的 [Workload](/docs/concepts/workload)。

```shell
kubectl -n default get workloads
```

输出类似于以下内容：

```shell
NAME               QUEUE         RESERVED IN   ADMITTED   AGE
sample-job-sl4bm   user-queue                             1s
```

## 3. (可选) 监控工作负载状态

您可以使用以下命令查看工作负载状态：

```shell
kubectl -n default describe workload sample-job-sl4bm
```

如果 ClusterQueue 没有足够的配额来运行 Workload，输出类似于以下内容：

```shell
Name:         sample-job-sl4bm
Namespace:    default
Labels:       <none>
Annotations:  <none>
API Version:  kueue.x-k8s.io/v1beta1
Kind:         Workload
Metadata:
  ...
Spec:
  ...
Status:
  Conditions:
    Last Probe Time:       2022-03-28T19:43:03Z
    Last Transition Time:  2022-03-28T19:43:03Z
    Message:               workload didn't fit
    Reason:                Pending
    Status:                False
    Type:                  Admitted
Events:               <none>
```

当 ClusterQueue 有足够的配额来运行 Workload 时，它将承认 Workload。要查看 Workload 是否被承认，请运行以下命令：

```shell
kubectl -n default get workloads
```

输出类似于以下内容：

```shell
NAME               QUEUE         RESERVED IN   ADMITTED   AGE
sample-job-sl4bm   user-queue    cluster-queue True       1s
```

要查看 Workload 的承认事件，请运行以下命令：

```shell
kubectl -n default describe workload sample-job-sl4bm
```

输出类似于以下内容：

```shell
...
Events:
  Type    Reason    Age   From           Message
  ----    ------    ----  ----           -------
  Normal  Admitted  50s   kueue-manager  Admitted by ClusterQueue cluster-queue
```

要继续监控 Workload 的进度，您可以运行以下命令：

```shell
kubectl -n default describe workload sample-job-sl4bm
```

一旦 Workload 运行完成，输出类似于以下内容：

```shell
...
Status:
  Conditions:
    ...
    Last Probe Time:       2022-03-28T19:43:37Z                                                                                                                      
    Last Transition Time:  2022-03-28T19:43:37Z                                                                                                                      
    Message:               Job finished successfully                                                                                                                 
    Reason:                JobFinished                                                                                                                               
    Status:                True                                                                                                                                      
    Type:                  Finished
...
```

要查看 Job 状态的更多详细信息，请运行以下命令：

```shell
kubectl -n default describe job sample-job-sl4bm
```

输出类似于以下内容：

```shell
Name:             sample-job-sl4bm
Namespace:        default
...
Start Time:       Mon, 28 Mar 2022 15:45:17 -0400
Completed At:     Mon, 28 Mar 2022 15:45:49 -0400
Duration:         32s
Pods Statuses:    0 Active / 3 Succeeded / 0 Failed
Pod Template:
  ...
Events:
  Type    Reason            Age   From                  Message
  ----    ------            ----  ----                  -------
  Normal  Suspended         22m   job-controller        Job suspended
  Normal  CreatedWorkload   22m   kueue-job-controller  Created Workload: default/sample-job-sl4bm
  Normal  SuccessfulCreate  19m   job-controller        Created pod: sample-job-sl4bm-7bqld
  Normal  Started           19m   kueue-job-controller  Admitted by clusterQueue cluster-queue
  Normal  SuccessfulCreate  19m   job-controller        Created pod: sample-job-sl4bm-7jw4z
  Normal  SuccessfulCreate  19m   job-controller        Created pod: sample-job-sl4bm-m7wgm
  Normal  Resumed           19m   job-controller        Job resumed
  Normal  Completed         18m   job-controller        Job completed
```

由于事件具有秒级分辨率的时间戳，事件的顺序可能与它们实际发生的时间略有不同。

## 部分承认

{{< feature-state state="beta" for_version="v0.5" >}}

Kueue 提供了批处理用户创建 Job 的能力，这些 Job 理想情况下会以并行度 `P0` 运行，但可以接受较小的并行度 `Pn`，如果 Job 不适应可用配额。

Kueue 仅在 admission 过程中考虑了 _borrowing_ 和 _preemption_ 后，才尝试减少并行度，且两者均不可行。

要允许部分承认，您可以在 Job 的 `kueue.x-k8s.io/job-min-parallelism` 注解中提供最小可接受的并行度 `Pmin`，`Pn` 应大于 0 且小于 `P0`。当 Job 部分承认时，其并行度将设置为 `Pn`，`Pn` 将设置为 `Pmin` 和 `P0` 之间的最大可接受值。Job 的完成数量不会改变。

例如，由以下清单定义的 Job：

{{< include "examples/jobs/sample-job-partial-admission.yaml" "yaml" >}}

当在仅 9 个 CPU 配额的 ClusterQueue 中排队时，它将被承认，并行度为 `parallelism=9`。请注意，完成数量不会改变。
