---
title: "运行一个 Kubernetes Job"
linkTitle: "Kubernetes Jobs"
date: 2022-02-14
weight: 4
description: >
  在启用了 Kueue 的 Kubernetes 集群中运行 Job。
---

本页面向您展示如何在启用了 Kueue 的 Kubernetes 集群中运行 Job。

本页面的目标受众是[批量用户](/docs/tasks#batch-user)。

## 开始之前

确保满足以下条件：

- Kubernetes 集群正在运行。
- kubectl 命令行工具能够与您的集群通信。
- [已安装 Kueue](/docs/installation)。
- 集群已[配置配额](/docs/tasks/manage/administer_cluster_quotas)。

下图显示了您在本教程中将要交互的所有概念：

![Kueue Components](/images/queueing-components.svg)

## 0. 识别您命名空间中可用的队列

运行以下命令列出您命名空间中可用的 `LocalQueues`。

```shell
kubectl -n default get localqueues
# 或使用 'queues' 别名
kubectl -n default get queues
```

输出类似于以下内容：

```bash
NAME         CLUSTERQUEUE    PENDING WORKLOADS
user-queue   cluster-queue   3
```

[ClusterQueue](/docs/concepts/cluster_queue) 定义队列的配额。

## 1. 定义 Job

在 Kueue 中运行 Job 与[在 Kubernetes 集群中运行 Job](https://kubernetes.io/docs/tasks/job/) 类似，只是没有使用 Kueue。但是，您必须设置 `kueue.x-k8s.io/queue-name` 标签来选择您要提交 Job 的 LocalQueue。
如果使用 [LocalQueue 默认值](/docs/tasks/manage/enforce_job_management/setup_default_local_queue)，您也可以跳过设置 "queue name" 标签。

您应该为每个 Job Pod 指定[资源请求或限制](https://kubernetes.io/docs/concepts/configuration/manage-resources-containers/)。
如果您只指定限制，Kueue 会将限制值视为请求。请参阅 [Kueue 如何使用资源请求](/docs/concepts/workload#resource-requests) 了解详细信息。

请注意，您不需要在[挂起状态](https://kubernetes.io/docs/concepts/workloads/controllers/job/#suspending-a-job)下创建 Job。
Kueue 通过 webhook 自动管理 Job 的挂起，并决定启动 Job 的最佳时机。

这是一个包含三个 Pod 的示例 Job，这些 Pod 只是休眠几秒钟。

{{< include "examples/jobs/sample-job.yaml" "yaml" >}}

## 2. 运行 Job

您可以使用以下命令运行 Job：

```shell
kubectl create -f sample-job.yaml
```

在内部，Kueue 将为此 Job 创建一个名称匹配的相应 [Workload](/docs/concepts/workload)。

```shell
kubectl -n default get workloads.kueue.x-k8s.io
```

输出类似于以下内容：

```shell
NAME               QUEUE         RESERVED IN   ADMITTED   AGE
sample-job-sl4bm   user-queue                             1s
```

## 3.（可选）监控工作负载的状态

您可以使用以下命令查看 Workload 状态：

```shell
kubectl -n default describe workload sample-job-sl4bm
```

如果 ClusterQueue 没有足够的配额来运行 Workload，输出类似于以下内容：

```shell
Name:         sample-job-sl4bm
Namespace:    default
Labels:       <none>
Annotations:  <none>
API Version:  kueue.x-k8s.io/v1beta2
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

当 ClusterQueue 有足够的配额运行 Workload 时，它将 admission 该 Workload。要查看 Workload 是否已被 admission，请运行以下命令：

```shell
kubectl -n default get workloads.kueue.x-k8s.io
```

输出类似于以下内容：

```shell
NAME               QUEUE         RESERVED IN   ADMITTED   AGE
sample-job-sl4bm   user-queue    cluster-queue True       1s
```

要查看 Workload admission 的事件，请运行以下命令：

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

当 Workload 运行完成后，输出类似于以下内容：

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

要查看有关 Job 状态的更多详细信息，请运行以下命令：

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

由于事件的时间戳精确到秒，事件的列出顺序可能与实际发生顺序略有不同。

## 部分 Admission

{{< feature-state state="beta" for_version="v0.5" >}}

Kueue 为批量用户提供了创建 Job 的能力，这些 Job 理想情况下将使用并行度 `P0` 运行，但如果 Job 不适合可用配额，则可以接受较小的并行度 `Pn`。

Kueue 只会在 admission 过程中考虑了 _borrowing_ 和 _preemption_ 之后且两者都不可行时，才尝试降低并行度。

要允许部分 admission，您可以在 Job 的 `kueue.x-k8s.io/job-min-parallelism` 注解中提供最小可接受的并行度 `Pmin`，`Pn` 应该大于 0 且小于 `P0`。当 Job 被部分 admission 时，其并行度将设置为 `Pn`，`Pn` 将设置为 `Pmin` 和 `P0` 之间的最大可接受值。Job 的完成计数不会更改。

例如，由以下清单定义的 Job：

{{< include "examples/jobs/sample-job-partial-admission.yaml" "yaml" >}}

当在只有 9 个 CPU 可用的 ClusterQueue 中排队时，它将以 `parallelism=9` 被 admission。请注意，完成的数量不会更改。

## ElasticJob

{{< feature-state state="alpha" for_version="v0.13" >}}

Kueue 支持批量用户**无需重新创建、重新启动或挂起** Job 即可扩展 Job 的并行度的能力。

要启用此功能，请确保以下条件：

* `ElasticJobsViaWorkloadSlices` 功能门控设置为 `true`
* Job 带有 `kueue.x-k8s.io/elastic-job: "true"` 注解

例如，由以下清单定义的 Job：

{{< include "examples/jobs/sample-scalable-job.yaml" "yaml" >}}

创建 Job 后，您可以通过更新 `parallelism` 字段并应用更改来扩展它。

当**扩展**时，您应该观察到：

* 为更新后的并行度创建并 admission 了新的 **Workload**
* 之前的 **Workload** 被标记为 `Finished`

### 示例演练

#### 创建

1. 使用提供的示例创建 Job。
2. 观察 Job、Pod 和 Workload 对象。

```text
kubectl get job,deploy,rs,pod,workload

NAME                           STATUS    COMPLETIONS   DURATION   AGE
job.batch/sample-elastic-job   Running   0/10          4s         4s

NAME                           READY   STATUS    RESTARTS   AGE
pod/sample-elastic-job-468nl   1/1     Running   0          4s
pod/sample-elastic-job-gd9qn   1/1     Running   0          4s
pod/sample-elastic-job-snk6j   1/1     Running   0          4s

NAME                                                   QUEUE       RESERVED IN   ADMITTED   FINISHED   ACTIVE   POD-COUNT   AGE
workload.kueue.x-k8s.io/job-sample-elastic-job-615b3   user-queue  user-queue    True                  true     3           4s
```

#### 缩容

1. 更新示例，将 `parallelism` 值从 `3` 减少到 `1`。
2. 观察 Job、Pod 和 Workload 对象的更改。

```text
kubectl get job,deploy,rs,pod,workload

NAME                           STATUS    COMPLETIONS   DURATION   AGE
job.batch/sample-elastic-job   Running   0/10          27s        27s

NAME                           READY   STATUS        RESTARTS   AGE
pod/sample-elastic-job-468nl   1/1     Terminating   0          27s <-- 
pod/sample-elastic-job-gd9qn   1/1     Running       0          27s
pod/sample-elastic-job-snk6j   1/1     Terminating   0          27s <--

NAME                                                   QUEUE       RESERVED IN   ADMITTED   FINISHED   ACTIVE   POD-COUNT   AGE
workload.kueue.x-k8s.io/job-sample-elastic-job-615b3   user-queue  user-queue    True                  true     1           4s
```

* 两个 Pod 正在终止，而剩余的 Pod 继续运行。
* 没有创建新的 Workload 对象。

#### 扩容

1. 更新示例，将 `parallelism` 值从 `1` 增加到 `4`。
2. 观察 Job、Pod 和 Workload 对象的更改。

```text
kubectl get job,deploy,rs,pod,workload

NAME                           STATUS    COMPLETIONS   DURATION   AGE
job.batch/sample-elastic-job   Running   1/10          82s        82s

NAME                           READY   STATUS      RESTARTS   AGE
pod/sample-elastic-job-4xtsq   1/1     Running     0          10s 
pod/sample-elastic-job-fl6j4   1/1     Running     0          10s
pod/sample-elastic-job-gd9qn   1/1     Running     0          52s <-- 旧 Pod
pod/sample-elastic-job-q482v   1/1     Running     0          10s


NAME                                                   QUEUE       RESERVED IN   ADMITTED   FINISHED   ACTIVE   POD-COUNT   AGE
workload.kueue.x-k8s.io/job-sample-elastic-job-615b3   user-queue  user-queue    True       True       true     1           52s <-- 旧 Workload
workload.kueue.x-k8s.io/job-sample-elastic-job-992ea   user-queue  user-queue    True                  true     4           10s <-- 新 Workload
```

* 创建并运行新的 Pod。
* 创建了具有更新后 Pod 计数的**新 Workload**。
* **旧的 Workload** 被标记为 `Finished`。