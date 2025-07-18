---
title: "Workload 优先级类"
date: 2023-10-02
weight: 6
description: >
  一种优先级类，其值被 Kueue 控制器使用，并且独立于 Pod 的优先级。
---

`WorkloadPriorityClass` 允许你控制 [`Workload`](/docs/concepts/workload) 的优先级，而不会影响 Pod 的优先级。
此功能适用于以下场景：
- 希望优先调度那些长时间处于非活跃状态的工作负载
- 希望为开发环境工作负载设置较低优先级，为生产环境工作负载设置较高优先级

一个示例 WorkloadPriorityClass 如下所示：

```yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: WorkloadPriorityClass
metadata:
  name: sample-priority
value: 10000
description: "示例优先级"
```

`WorkloadPriorityClass` 对象是集群范围的，因此可以被任何命名空间下的作业使用。

## 如何在 Job 上使用 WorkloadPriorityClass

你可以通过设置标签 `kueue.x-k8s.io/priority-class` 来指定 `WorkloadPriorityClass`。

```yaml
apiVersion: batch/v1
kind: Job
metadata:
  name: sample-job
  labels:
    kueue.x-k8s.io/queue-name: user-queue
    kueue.x-k8s.io/priority-class: sample-priority
spec:
...
```
Kueue 会为上述 Job 生成如下 `Workload`。
`PriorityClassName` 字段可以接受 `PriorityClass` 或 `WorkloadPriorityClass` 的名称。
为区分两者，当使用 `WorkloadPriorityClass` 时，`priorityClassSource` 字段为
`kueue.x-k8s.io/workloadpriorityclass`；当使用 `PriorityClass` 时，`priorityClassSource`
字段为 `scheduling.k8s.io/priorityclass`。

```yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: Workload
metadata:
  name: job-sample-job-7f173
spec:
  priorityClassSource: kueue.x-k8s.io/workloadpriorityclass
  priorityClassName: sample-priority
  priority: 10000
  queueName: user-queue
...
```

对于其他作业框架，也可以通过相同标签设置 `WorkloadPriorityClass`。
以下是 `MPIJob` 的示例：

```yaml
apiVersion: kubeflow.org/v2beta1
kind: MPIJob
metadata:
  name: pi
  labels:
    kueue.x-k8s.io/queue-name: user-queue
    kueue.x-k8s.io/priority-class: sample-priority
spec:
...
```

## Pod 优先级与 Workload 优先级的关系

为给定作业创建 `Workload` 时，Kueue 会考虑以下场景：
1. 作业同时指定了 `WorkloadPriorityClass` 和 `PriorityClass`
    - `WorkloadPriorityClass` 用于 Workload 的优先级。
    - `PriorityClass` 用于 Pod 的优先级。
2. 作业仅指定了 `WorkloadPriorityClass`
    - `WorkloadPriorityClass` 用于 Workload 的优先级。
    - `WorkloadPriorityClass` 不用于 Pod 的优先级。
3. 作业仅指定了 `PriorityClass`
    - `PriorityClass` 用于 Workload 和 Pod 的优先级。

在某些作业框架中，CRD 可能：
    - 定义多个 Pod 规范，每个规范可以有自己的 Pod 优先级，或
    - 在专用字段中定义整体 Pod 优先级。

默认情况下，kueue 会采用第一个设置了 PriorityClassName 的 PodSet，但 CRD 与 Kueue 的集成可以实现
[`JobWithPriorityClass 接口`](https://github.com/kubernetes-sigs/kueue/blob/e162f8508b503d20feb9b31fd0b27d91e58f2c2f/pkg/controller/jobframework/interface.go#L81-L84) 来改变此行为。
你可以阅读每个作业集成的代码，了解优先级类的获取方式。

## Workload 优先级的用途

Workload 的优先级用于：
- 在 ClusterQueue 中对 Workload 排序。
- 决定某个 Workload 是否可以抢占其他 Workload。

## Workload 优先级值始终可变

`Workload` 的 `Priority` 字段始终是可变的。
如果某个 `Workload` 已经挂起一段时间，你可以根据自己的策略考虑提升其优先级以提前执行。
Workload 的 `PriorityClassSource` 和 `PriorityClassName` 字段是不可变的。

## 下一步？

- 了解如何[运行作业](/docs/tasks/run/jobs)
- 了解如何[使用 Workload 优先级运行作业](/docs/tasks/manage/run_job_with_workload_priority)
- 阅读 `WorkloadPriorityClass` 的 [API 参考](/docs/reference/kueue.v1beta1/#kueue-x-k8s-io-v1beta1-WorkloadPriorityClass)
