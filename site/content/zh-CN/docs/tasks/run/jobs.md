---
title: "运行 Kubernetes Job"
linkTitle: "Kubernetes Job"
date: 2022-02-14
weight: 4
description: >
  在启用了 Kueue 的 Kubernetes 集群中运行 Job。
---

本页面向您展示如何在启用了 Kueue 的 Kubernetes 集群中运行 Job。

本页面的目标受众是[批量用户](/docs/tasks#batch-user)。

## 开始之前 {#before-you-begin}

确保满足以下条件：

- Kubernetes 集群正在运行。
- kubectl 命令行工具能够与您的集群通信。
- [已安装 Kueue](/zh-CN/docs/installation)。
- 集群已[配置配额](/zh-CN/docs/tasks/manage/administer_cluster_quotas)。

下图显示了您在本教程中将要学习的所有概念：

![Kueue Components](/images/queueing-components.svg)

## 0. 识别命名空间中可用的队列 {#0-identify-the-queues-available-in-your-namespace}

运行以下命令列出您命名空间中可用的 `LocalQueues`。

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

## 1. 定义 Job {#1-define-the-job}

在 Kueue 中运行 Job 类似于[在 Kubernetes 集群中运行 Job](https://kubernetes.io/docs/tasks/job/)，
只是没有使用 Kueue。但是，您必须考虑以下差异：

- 您应该在[挂起状态](https://kubernetes.io/docs/concepts/workloads/controllers/job/#suspending-a-job)下创建 Job，
  因为 Kueue 将决定何时是启动 Job 的最佳时间。
- 您必须设置要提交 Job 的队列。使用
 `kueue.x-k8s.io/queue-name` 标签。
- 您应该为每个 Job Pod 包含资源请求。

这是一个包含三个 Pod 的示例 Job，它们只是休眠几秒钟。

{{< include "examples/jobs/sample-job.yaml" "yaml" >}}

## 2. 运行 Job {#2-run-the-job}

您可以使用以下命令运行 Job：

```shell
kubectl create -f sample-job.yaml
```

在内部，Kueue 将为此 Job 创建一个与名称匹配的相应[Workload](/zh-CN/docs/concepts/workload)。

```shell
kubectl -n default get workloads
```

输出将类似于以下内容：

```shell
NAME               QUEUE         RESERVED IN   ADMITTED   AGE
sample-job-sl4bm   user-queue                             1s
```

## 3. （可选）监控 Workload 的状态 {#3-optional-monitor-the-status-of-the-workload}

您可以使用以下命令查看 Workload 状态：

```shell
kubectl -n default describe workload sample-job-sl4bm
```

如果 ClusterQueue 没有足够的配额来运行 Workload，
输出将类似于以下内容：

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

当 ClusterQueue 有足够的配额来运行 Workload 时，它将让此 Workload 准入。
要查看 Workload 是否被准入，运行以下命令：

```shell
kubectl -n default get workloads
```

输出类似于以下内容：

```shell
NAME               QUEUE         RESERVED IN   ADMITTED   AGE
sample-job-sl4bm   user-queue    cluster-queue True       1s
```

要查看 Workload 准入的事件，运行以下命令：

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

要继续监控 Workload 进度，您可以运行以下命令：

```shell
kubectl -n default describe workload sample-job-sl4bm
```

一旦 Workload 完成运行，输出类似于以下内容：

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

要查看 Job 状态的更多详细信息，运行以下命令：

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

由于事件的时间戳精度为秒，事件可能与实际发生的顺序略有不同。

## 部分准入 {#partial-admission}

{{< feature-state state="beta" for_version="v0.5" >}}

Kueue 提供了批量用户创建 Job 的能力，这些 Job 理想情况下将以并行度 `P0` 运行，
但如果 Job 不适合可用配额，可以接受较小的并行度 `Pn`。

Kueue 只有在准入过程中同时考虑了_借用_和_抢占_，
并且两者都不可行的情况下，才会尝试减少并行度。

要允许部分准入，您可以在 Job 的 `kueue.x-k8s.io/job-min-parallelism`
注解中提供最小可接受并行度 `Pmin`，`Pn` 应该大于 0 且小于 `P0`。
当 Job 被部分准入时，其并行度将设置为 `Pn`，`Pn` 将设置为 `Pmin` 和 `P0`
之间的最大可接受值。Job 的完成计数不会改变。

例如，由以下清单定义的 Job：

{{< include "examples/jobs/sample-job-partial-admission.yaml" "yaml" >}}

当在只有 9 个 CPU 可用的 ClusterQueue 中排队时，它将以 `parallelism=9`
被准入。请注意，完成次数不会改变。
